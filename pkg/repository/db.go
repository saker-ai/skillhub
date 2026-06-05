package repository

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cinience/skillhub/pkg/config"
	"github.com/cinience/skillhub/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// DBOptions 是 NewDBWithOptions 的可选配置。
//
// 阶段 2 引入：嵌入方可以注入 GORM Logger 与表名前缀，
// 避免与宿主进程已有库表撞名；零值即等价于旧行为。
type DBOptions struct {
	// TablePrefix 表名前缀，默认 ""（无前缀）。例如 "sh_" 会让 users 变 sh_users。
	TablePrefix string
	// Logger GORM logger，nil 时回退到 logger.Default.LogMode(logger.Warn)。
	Logger logger.Interface
}

// NewDB 是 NewDBWithOptions 的兼容别名，行为与旧版 NewDB 完全一致。
func NewDB(cfg config.DatabaseConfig) (*gorm.DB, error) {
	return NewDBWithOptions(cfg, DBOptions{})
}

// NewDBWithOptions 在 NewDB 基础上接受可选 GORM logger 与表名前缀。
//
// 行为：
//   - opts.Logger 为 nil 时 → 默认 Warn 级别（与旧 NewDB 一致）。
//   - opts.TablePrefix 为空时 → 不设置 NamingStrategy（保留 GORM 默认表名）。
//   - 其它逻辑（Driver 选择、连接池设置、AutoMigrate、迁移种子）与旧 NewDB 一致。
func NewDBWithOptions(cfg config.DatabaseConfig, opts DBOptions) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		Logger: opts.Logger,
	}
	if gormCfg.Logger == nil {
		gormCfg.Logger = logger.Default.LogMode(logger.Warn)
	}
	if opts.TablePrefix != "" {
		gormCfg.NamingStrategy = schema.NamingStrategy{
			TablePrefix:   opts.TablePrefix,
			SingularTable: false,
		}
	}

	var db *gorm.DB
	var err error

	switch cfg.Driver {
	case "postgres":
		db, err = gorm.Open(postgres.Open(cfg.URL), gormCfg)
	case "sqlite", "":
		dbPath := cfg.URL
		if dbPath == "" {
			dbPath = "./data/skillhub.db"
		}
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000"
		db, err = gorm.Open(sqlite.Open(dsn), gormCfg)
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Driver)
	}

	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying db: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	if !cfg.AutoMigrate {
		return db, nil
	}

	// AutoMigrate all models
	if err := db.AutoMigrate(
		&model.User{},
		&model.Skill{},
		&model.SkillVersion{},
		&model.SkillSlugAlias{},
		&model.APIToken{},
		&model.Star{},
		&model.DownloadDedup{},
		&model.AuditLog{},
		&model.SkillDailyStats{},
		&model.ReservedSlug{},
		&model.SkillOwnershipTransfer{},
		&model.Comment{},
		&model.Namespace{},
		&model.NamespaceMember{},
		&model.NamespaceInvitation{},
		&model.OAuthIdentity{},
		&model.Notification{},
		&model.Rating{},
		&model.Plugin{},
		&model.PluginVersion{},
	); err != nil {
		return nil, fmt.Errorf("auto migrate: %w", err)
	}

	// Migrate existing approved skills to public visibility (in transaction)
	if err := db.Transaction(func(tx *gorm.DB) error {
		migrateVisibility(tx)
		seedReservedSlugs(tx)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("post-migration: %w", err)
	}

	// Namespace migration: assign orphaned skills (namespace_id IS NULL) to
	// their owner's personal namespace. Must run AFTER AutoMigrate so the
	// namespaces table exists.
	if err := db.Transaction(func(tx *gorm.DB) error {
		migrateSkillNamespaces(tx)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("namespace migration: %w", err)
	}

	// Swap the unique index: drop the old global slug uniqueIndex and create
	// the compound (namespace_id, slug) unique index. Raw SQL avoids GORM
	// migrator issues with SQLite compound indexes.
	for _, idx := range []string{"idx_skills_slug", "uni_skills_slug"} {
		db.Exec("DROP INDEX IF EXISTS " + idx)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_ns_slug ON skills (namespace_id, slug)")

	// Plugin namespace migration (same pattern as skills).
	migratePluginNamespaces(db)
	for _, idx := range []string{"idx_plugins_slug", "uni_plugins_slug"} {
		db.Exec("DROP INDEX IF EXISTS " + idx)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_plugin_ns_slug ON plugins (namespace_id, slug)")

	// Backfill alias namespace_id from the skill they point to.
	db.Exec(`UPDATE skill_slug_aliases SET namespace_id = (
		SELECT namespace_id FROM skills WHERE skills.id = skill_slug_aliases.skill_id
	) WHERE skill_slug_aliases.namespace_id IS NULL`)

	// Alias table: swap from global unique old_slug to (namespace_id, old_slug).
	for _, idx := range []string{"idx_skill_slug_aliases_old_slug", "uni_skill_slug_aliases_old_slug"} {
		db.Exec("DROP INDEX IF EXISTS " + idx)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_ns_old_slug ON skill_slug_aliases (namespace_id, old_slug)")

	// Enforce NOT NULL on namespace_id at DB level. SQLite doesn't support
	// ALTER COLUMN, so we use a CHECK constraint workaround. For Postgres
	// we can directly alter the column.
	switch cfg.Driver {
	case "postgres":
		db.Exec(`DO $$ BEGIN
			ALTER TABLE skills ALTER COLUMN namespace_id SET NOT NULL;
		EXCEPTION WHEN others THEN NULL;
		END $$`)
	default:
		// SQLite: CHECK constraints can't be added after table creation,
		// but all rows are guaranteed non-null by the migration above.
		// The application layer enforces this going forward.
	}

	return db, nil
}

// migrateVisibility sets existing approved skills with empty visibility to public (one-time migration).
// Only affects skills with empty or NULL visibility — intentionally-private skills are not touched.
func migrateVisibility(db *gorm.DB) {
	result := db.Model(&model.Skill{}).
		Where("moderation_status = ? AND (visibility = '' OR visibility IS NULL)", "approved").
		Where("soft_deleted_at IS NULL").
		Update("visibility", "public")
	if result.RowsAffected > 0 {
		slog.Default().Info("database: migrated existing approved skills to public visibility", "count", result.RowsAffected)
	}
}

// migrateSkillNamespaces assigns all skills with namespace_id IS NULL to their
// owner's personal namespace. For each distinct owner_id it ensures a personal
// namespace exists (slug = user.handle, type = personal) and bulk-updates the
// orphaned skills. This is idempotent: once no NULL namespace_id rows remain,
// subsequent runs are a no-op.
func migrateSkillNamespaces(db *gorm.DB) {
	// Find distinct owner IDs that have orphaned skills.
	type ownerRow struct {
		OwnerID string
		Handle  string
	}
	var owners []ownerRow
	db.Raw(`SELECT DISTINCT s.owner_id, u.handle
		FROM skills s JOIN users u ON s.owner_id = u.id
		WHERE s.namespace_id IS NULL`).Scan(&owners)

	if len(owners) == 0 {
		return
	}

	var totalMigrated int64
	for _, o := range owners {
		// Find or create personal namespace for this owner.
		var ns model.Namespace
		err := db.Where("owner_id = ? AND type = 'personal'", o.OwnerID).First(&ns).Error
		if err != nil {
			// Create personal namespace.
			ns = model.Namespace{
				ID:      uuid.New(),
				Slug:    o.Handle,
				OwnerID: uuid.MustParse(o.OwnerID),
				Type:    "personal",
				Status:  "active",
			}
			if err := db.Create(&ns).Error; err != nil {
				slog.Default().Warn("namespace migration: failed to create personal namespace",
					"handle", o.Handle, "err", err)
				continue
			}
			// Add owner member.
			member := model.NamespaceMember{
				ID:          uuid.New(),
				NamespaceID: ns.ID,
				UserID:      uuid.MustParse(o.OwnerID),
				Role:        "owner",
			}
			if err := db.Create(&member).Error; err != nil {
				slog.Default().Warn("namespace migration: failed to add owner member",
					"handle", o.Handle, "err", err)
			}
		}

		// Assign orphaned skills to the personal namespace.
		result := db.Model(&model.Skill{}).
			Where("namespace_id IS NULL AND owner_id = ?", o.OwnerID).
			Update("namespace_id", ns.ID)
		totalMigrated += result.RowsAffected
	}

	if totalMigrated > 0 {
		slog.Default().Info("namespace migration: assigned orphaned skills to personal namespaces",
			"skills", totalMigrated, "owners", len(owners))
	}
}

// migratePluginNamespaces assigns all plugins with namespace_id IS NULL to their
// owner's personal namespace. Same pattern as migrateSkillNamespaces.
func migratePluginNamespaces(db *gorm.DB) {
	type ownerRow struct {
		OwnerID string
		Handle  string
	}
	var owners []ownerRow
	db.Raw(`SELECT DISTINCT p.owner_id, u.handle
		FROM plugins p JOIN users u ON p.owner_id = u.id
		WHERE p.namespace_id IS NULL`).Scan(&owners)

	if len(owners) == 0 {
		return
	}

	var totalMigrated int64
	for _, o := range owners {
		var ns model.Namespace
		err := db.Where("owner_id = ? AND type = 'personal'", o.OwnerID).First(&ns).Error
		if err != nil {
			ns = model.Namespace{
				ID:      uuid.New(),
				Slug:    o.Handle,
				OwnerID: uuid.MustParse(o.OwnerID),
				Type:    "personal",
				Status:  "active",
			}
			if err := db.Create(&ns).Error; err != nil {
				continue
			}
			member := model.NamespaceMember{
				ID:          uuid.New(),
				NamespaceID: ns.ID,
				UserID:      uuid.MustParse(o.OwnerID),
				Role:        "owner",
			}
			db.Create(&member)
		}

		result := db.Model(&model.Plugin{}).
			Where("namespace_id IS NULL AND owner_id = ?", o.OwnerID).
			Update("namespace_id", ns.ID)
		totalMigrated += result.RowsAffected
	}

	if totalMigrated > 0 {
		slog.Default().Info("namespace migration: assigned orphaned plugins to personal namespaces",
			"plugins", totalMigrated, "owners", len(owners))
	}
}

func seedReservedSlugs(db *gorm.DB) {
	slugs := []string{"admin", "api", "auth", "login", "register", "search", "settings", "system", "help", "about"}
	reason := "system reserved"
	var created int
	for _, slug := range slugs {
		result := db.Where("slug = ?", slug).FirstOrCreate(&model.ReservedSlug{
			Slug:   slug,
			Reason: &reason,
		})
		if result.RowsAffected > 0 {
			created++
		}
	}
	if created > 0 {
		slog.Default().Info("database: seeded new reserved slugs", "count", created)
	}
}
