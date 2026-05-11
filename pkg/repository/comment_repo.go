package repository

import (
	"context"
	"time"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CommentRepo struct {
	db *gorm.DB
}

func NewCommentRepo(db *gorm.DB) *CommentRepo {
	return &CommentRepo{db: db}
}

func (r *CommentRepo) Create(ctx context.Context, c *model.Comment) error {
	return r.db.WithContext(ctx).Create(c).Error
}

func (r *CommentRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Comment, error) {
	var c model.Comment
	err := r.db.WithContext(ctx).
		Where("id = ? AND soft_deleted_at IS NULL", id).
		First(&c).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &c, err
}

// ListBySkill returns comments newest-first with cursor pagination.
// cursor is a comment ID; returns nextCursor for the next page.
func (r *CommentRepo) ListBySkill(ctx context.Context, skillID uuid.UUID, limit int, cursor string) ([]model.CommentWithUser, string, error) {
	q := r.db.WithContext(ctx).
		Table("comments").
		Select("comments.*, users.handle AS handle, users.display_name AS display_name, users.avatar_url AS avatar_url").
		Joins("JOIN users ON comments.user_id = users.id").
		Where("comments.skill_id = ? AND comments.soft_deleted_at IS NULL", skillID)

	if cursor != "" {
		// Tuple-cursor on (created_at, id) prevents skipping rows when
		// multiple comments share the same created_at timestamp.
		q = q.Where("(comments.created_at, comments.id) < (SELECT created_at, id FROM comments WHERE id = ?)", cursor)
	}

	var rows []model.CommentWithUser
	err := q.Order("comments.created_at DESC, comments.id DESC").Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return nil, "", err
	}
	var nextCursor string
	if len(rows) > limit {
		nextCursor = rows[limit].ID.String()
		rows = rows[:limit]
	}
	return rows, nextCursor, nil
}

func (r *CommentRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&model.Comment{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"soft_deleted_at": &now,
			"updated_at":      now,
		}).Error
}
