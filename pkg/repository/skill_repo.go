package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/model"
	"gorm.io/gorm"
)

type SkillRepo struct {
	db *gorm.DB
	// cache 可能为 nil——SetCache 没被调用时所有 Get* 直接走 DB；
	// SkillCache 的方法在 nil 接收者上安全，所以 GetBySlug 不需要写 if-guard。
	cache *SkillCache
}

func NewSkillRepo(db *gorm.DB) *SkillRepo {
	return &SkillRepo{db: db}
}

// SetCache 注入 *SkillCache。装配阶段调用一次即可——运行期切换没意义，
// 且会让正在飞行的请求看到不一致的缓存语义。传 nil 等价于关闭缓存。
func (r *SkillRepo) SetCache(c *SkillCache) {
	r.cache = c
}

// InvalidateCache 显式清除一批 slug 的缓存条目。
// service 层在 metadata 写入（SetVisibility / SoftDelete / Rename / 重新发布）
// 之后必须调用——repo 自身的 mutator 拿不到 slug，service 层才有完整上下文。
//
// 不存在的 slug 静默跳过；缓存关闭（cache == nil）时整个调用是 noop。
func (r *SkillRepo) InvalidateCache(slugs ...string) {
	r.cache.Invalidate(slugs...)
}

// InvalidateCacheNS clears namespace-qualified cache entries.
func (r *SkillRepo) InvalidateCacheNS(nsID string, slugs ...string) {
	r.cache.InvalidateNS(nsID, slugs...)
}

func (r *SkillRepo) Create(ctx context.Context, skill *model.Skill) error {
	return r.db.WithContext(ctx).Create(skill).Error
}

// skillWithOwnerSelect is the shared SELECT clause for all queries that return SkillWithOwner.
const skillWithOwnerSelect = "skills.*, users.handle AS owner_handle, users.display_name AS owner_display_name, users.avatar_url AS owner_avatar_url, namespaces.slug AS namespace_slug"

func (r *SkillRepo) GetBySlug(ctx context.Context, slug string) (*model.SkillWithOwner, error) {
	if cached, ok := r.cache.Get(slug); ok {
		return cached, nil
	}
	var skill model.SkillWithOwner
	err := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.slug = ? AND skills.soft_deleted_at IS NULL", slug).
		First(&skill).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.cache.Set(slug, &skill)
	return &skill, nil
}

// GetByNSAndSlug looks up a skill by its namespace ID and slug (compound unique key).
func (r *SkillRepo) GetByNSAndSlug(ctx context.Context, namespaceID uuid.UUID, slug string) (*model.SkillWithOwner, error) {
	cacheKey := "ns:" + namespaceID.String() + ":" + slug
	if cached, ok := r.cache.Get(cacheKey); ok {
		return cached, nil
	}
	var skill model.SkillWithOwner
	err := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.namespace_id = ? AND skills.slug = ? AND skills.soft_deleted_at IS NULL", namespaceID, slug).
		First(&skill).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.cache.Set(cacheKey, &skill)
	return &skill, nil
}

// GetByNSAndSlugIncludeDeleted looks up a skill by namespace and slug without
// filtering soft-deleted rows. It intentionally bypasses the live-skill cache.
func (r *SkillRepo) GetByNSAndSlugIncludeDeleted(ctx context.Context, namespaceID uuid.UUID, slug string) (*model.SkillWithOwner, error) {
	var skill model.SkillWithOwner
	err := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.namespace_id = ? AND skills.slug = ?", namespaceID, slug).
		First(&skill).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &skill, nil
}

// GetBySlugGlobal returns all non-deleted skills matching a slug across all namespaces.
// Used for disambiguation when a bare slug is ambiguous.
func (r *SkillRepo) GetBySlugGlobal(ctx context.Context, slug string) ([]model.SkillWithOwner, error) {
	var skills []model.SkillWithOwner
	err := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.slug = ? AND skills.soft_deleted_at IS NULL", slug).
		Find(&skills).Error
	return skills, err
}

// GetBySlugGlobalIncludeDeleted returns all skills matching a slug across all
// namespaces, including soft-deleted rows. Used by restore paths that must be
// able to resolve the deleted record before clearing SoftDeletedAt.
func (r *SkillRepo) GetBySlugGlobalIncludeDeleted(ctx context.Context, slug string) ([]model.SkillWithOwner, error) {
	var skills []model.SkillWithOwner
	err := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.slug = ?", slug).
		Find(&skills).Error
	return skills, err
}

func (r *SkillRepo) GetBySlugOrAlias(ctx context.Context, slug string) (*model.SkillWithOwner, error) {
	// GetBySlug 自身已经走过缓存，这里直接复用。
	skill, err := r.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if skill != nil {
		return skill, nil
	}
	// Check aliases
	var alias model.SkillSlugAlias
	err = r.db.WithContext(ctx).Where("old_slug = ?", slug).First(&alias).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Found alias, get actual skill
	var s model.SkillWithOwner
	err = r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.id = ? AND skills.soft_deleted_at IS NULL", alias.SkillID).
		First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// 用 alias 旧 slug 与当前 slug 双 key 缓存：下次按旧 slug 来访问也能直接命中。
	r.cache.Set(slug, &s)
	r.cache.Set(s.Slug, &s)
	return &s, nil
}

func (r *SkillRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Skill, error) {
	var skill model.Skill
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&skill).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &skill, err
}

// ListFilter controls visibility filtering for skill listing.
type ListFilter struct {
	ViewerID    *uuid.UUID // nil = anonymous
	IsAdmin     bool       // admin/moderator sees all
	Category    string     // filter by category (empty = all)
	NamespaceID *uuid.UUID // nil = all namespaces
}

func (r *SkillRepo) List(ctx context.Context, limit int, cursor string, sort string, filter ListFilter) ([]model.SkillWithOwner, string, error) {
	orderClause := "skills.created_at DESC, skills.id"
	switch sort {
	case "downloads":
		orderClause = "skills.downloads DESC, skills.id"
	case "stars":
		orderClause = "skills.stars_count DESC, skills.id"
	case "updated":
		orderClause = "skills.updated_at DESC, skills.id"
	case "name":
		orderClause = "skills.slug ASC, skills.id"
	}

	q := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.soft_deleted_at IS NULL")

	if filter.IsAdmin {
		// Admin sees all non-deleted skills
	} else if filter.ViewerID != nil {
		// Logged-in user: public+approved OR own skills
		q = q.Where("(skills.visibility = 'public' AND skills.moderation_status = 'approved') OR skills.owner_id = ?", *filter.ViewerID)
	} else {
		// Anonymous: only public+approved
		q = q.Where("skills.visibility = 'public' AND skills.moderation_status = 'approved'")
	}

	if filter.NamespaceID != nil {
		q = q.Where("skills.namespace_id = ?", *filter.NamespaceID)
	}

	if filter.Category != "" {
		q = q.Where("skills.category = ?", filter.Category)
	}

	if cursor != "" {
		// Cursor comparison must match the sort column
		switch sort {
		case "downloads":
			q = q.Where("(skills.downloads, skills.id) < (SELECT downloads, id FROM skills WHERE id = ?)", cursor)
		case "stars":
			q = q.Where("(skills.stars_count, skills.id) < (SELECT stars_count, id FROM skills WHERE id = ?)", cursor)
		case "updated":
			q = q.Where("(skills.updated_at, skills.id) < (SELECT updated_at, id FROM skills WHERE id = ?)", cursor)
		case "name":
			q = q.Where("(skills.slug, skills.id) > (SELECT slug, id FROM skills WHERE id = ?)", cursor)
		default: // "created"
			q = q.Where("(skills.created_at, skills.id) < (SELECT created_at, id FROM skills WHERE id = ?)", cursor)
		}
	}

	var skills []model.SkillWithOwner
	err := q.Order(orderClause).Limit(limit + 1).Find(&skills).Error
	if err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(skills) > limit {
		nextCursor = skills[limit].ID.String()
		skills = skills[:limit]
	}
	return skills, nextCursor, nil
}

// ListAllForAdmin returns all skills for admin management, with optional visibility filter.
func (r *SkillRepo) ListAllForAdmin(ctx context.Context, limit int, cursor, visibility string) ([]model.SkillWithOwner, string, error) {
	q := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.soft_deleted_at IS NULL")

	switch visibility {
	case "private":
		q = q.Where("skills.visibility = 'private'")
	case "public":
		q = q.Where("skills.visibility = 'public'")
	case "pending":
		q = q.Where("skills.moderation_status = 'pending_review'")
	}

	if cursor != "" {
		q = q.Where("skills.updated_at <= (SELECT updated_at FROM skills WHERE id = ?) AND skills.id != ?", cursor, cursor)
	}

	var skills []model.SkillWithOwner
	err := q.Order("skills.updated_at DESC, skills.id").Limit(limit + 1).Find(&skills).Error
	if err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(skills) > limit {
		nextCursor = skills[limit].ID.String()
		skills = skills[:limit]
	}
	return skills, nextCursor, nil
}

// SetVisibility updates a skill's visibility and moderation status.
func (r *SkillRepo) SetVisibility(ctx context.Context, skillID uuid.UUID, visibility, moderationStatus string) error {
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Updates(map[string]interface{}{
			"visibility":        visibility,
			"moderation_status": moderationStatus,
			"updated_at":        time.Now(),
		}).Error
}

func (r *SkillRepo) UpdateLatestVersion(ctx context.Context, skillID uuid.UUID, versionID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Updates(map[string]interface{}{
			"latest_version_id": versionID,
			"versions_count":    gorm.Expr("versions_count + 1"),
			"updated_at":        time.Now(),
		}).Error
}

// SetLatestVersion repoints the latest pointer without incrementing
// versions_count. Used to recover after a yank invalidates the previous
// latest. Pass uuid.Nil to clear when no eligible version remains.
func (r *SkillRepo) SetLatestVersion(ctx context.Context, skillID, versionID uuid.UUID) error {
	updates := map[string]interface{}{"updated_at": time.Now()}
	if versionID == uuid.Nil {
		updates["latest_version_id"] = nil
	} else {
		updates["latest_version_id"] = versionID
	}
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Updates(updates).Error
}

func (r *SkillRepo) IncrementDownloads(ctx context.Context, skillID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Update("downloads", gorm.Expr("downloads + 1")).Error
}

func (r *SkillRepo) UpdateStarsCount(ctx context.Context, skillID uuid.UUID, delta int) error {
	expr := gorm.Expr("stars_count + ?", delta)
	if delta < 0 {
		// Prevent negative stars: use MAX(0, stars_count + delta)
		expr = gorm.Expr("MAX(0, stars_count + ?)", delta)
	}
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Update("stars_count", expr).Error
}

func (r *SkillRepo) UpdateRatingStats(ctx context.Context, skillID uuid.UUID, avg float64, count int) error {
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Updates(map[string]interface{}{
			"average_rating": avg,
			"ratings_count":  count,
		}).Error
}

func (r *SkillRepo) SoftDelete(ctx context.Context, skillID uuid.UUID) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Updates(map[string]interface{}{
			"soft_deleted_at": now,
			"updated_at":      now,
		}).Error
}

func (r *SkillRepo) GetBySlugIncludeDeleted(ctx context.Context, slug string) (*model.SkillWithOwner, error) {
	var skill model.SkillWithOwner
	err := r.db.WithContext(ctx).
		Table("skills").
		Select(skillWithOwnerSelect).
		Joins("JOIN users ON skills.owner_id = users.id").
		Joins("LEFT JOIN namespaces ON skills.namespace_id = namespaces.id").
		Where("skills.slug = ?", slug).
		First(&skill).Error
	if err != nil {
		return nil, err
	}
	return &skill, nil
}

func (r *SkillRepo) HardDeleteBySlug(ctx context.Context, slug string) error {
	return r.db.WithContext(ctx).
		Unscoped().
		Where("slug = ?", slug).
		Delete(&model.Skill{}).Error
}

func (r *SkillRepo) Undelete(ctx context.Context, skillID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skillID).
		Updates(map[string]interface{}{
			"soft_deleted_at": nil,
			"updated_at":      time.Now(),
		}).Error
}

// CountByNamespace returns the number of non-deleted skills in a namespace.
func (r *SkillRepo) CountByNamespace(ctx context.Context, namespaceID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("namespace_id = ? AND soft_deleted_at IS NULL", namespaceID).
		Count(&count).Error
	return count, err
}

func (r *SkillRepo) IsSlugReserved(ctx context.Context, slug string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.ReservedSlug{}).Where("slug = ?", slug).Count(&count).Error
	return count > 0, err
}

func (r *SkillRepo) Update(ctx context.Context, skill *model.Skill) error {
	return r.db.WithContext(ctx).
		Model(&model.Skill{}).
		Where("id = ?", skill.ID).
		Updates(map[string]interface{}{
			"display_name": skill.DisplayName,
			"summary":      skill.Summary,
			"category":     skill.Category,
			"tags":         skill.Tags,
			"updated_at":   time.Now(),
		}).Error
}

func (r *SkillRepo) Rename(ctx context.Context, skillID uuid.UUID, namespaceID *uuid.UUID, oldSlug, newSlug string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Skill{}).Where("id = ?", skillID).
			Update("slug", newSlug).Error; err != nil {
			return fmt.Errorf("rename skill: %w", err)
		}
		alias := model.SkillSlugAlias{
			ID:          uuid.New(),
			SkillID:     skillID,
			NamespaceID: namespaceID,
			OldSlug:     oldSlug,
		}
		return tx.Create(&alias).Error
	})
}
