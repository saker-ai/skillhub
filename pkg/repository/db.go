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
