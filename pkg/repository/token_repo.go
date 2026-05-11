package repository

import (
	"context"
	"time"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type TokenRepo struct {
	db *gorm.DB
}

func NewTokenRepo(db *gorm.DB) *TokenRepo {
	return &TokenRepo{db: db}
}

func (r *TokenRepo) Create(ctx context.Context, token *model.APIToken) error {
	return r.db.WithContext(ctx).Create(token).Error
}

func (r *TokenRepo) GetByPrefix(ctx context.Context, prefix string) ([]model.APIToken, error) {
	var tokens []model.APIToken
	err := r.db.WithContext(ctx).
		Where("prefix = ? AND revoked_at IS NULL", prefix).
		Find(&tokens).Error
	return tokens, err
}

func (r *TokenRepo) GetByUserID(ctx context.Context, userID uuid.UUID) ([]model.APIToken, error) {
	var tokens []model.APIToken
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Order("created_at DESC").
		Find(&tokens).Error
	return tokens, err
}

// GetByNamespaceID 列出某个 namespace 下未吊销的全部 token，按创建时间倒序。
//
// 团队成员管理 token 的端点用：列表展示 + 撤销 (admin 可吊销任意一个，非 admin 只能
// 吊销自己创建的——权限判定在 handler 层做，这里只负责取数据)。
func (r *TokenRepo) GetByNamespaceID(ctx context.Context, namespaceID uuid.UUID) ([]model.APIToken, error) {
	var tokens []model.APIToken
	err := r.db.WithContext(ctx).
		Where("namespace_id = ? AND revoked_at IS NULL", namespaceID).
		Order("created_at DESC").
		Find(&tokens).Error
	return tokens, err
}

// GetByNamespacePaged 同 GetByNamespaceID 但支持游标分页。
//
// cursor 编码上一页最后一条记录的 created_at（RFC3339Nano）。空 cursor 表示首页。
// 返回 (tokens, nextCursor)：nextCursor 为空表示没有更多。
//
// 为什么 cursor 用 created_at 而不是 id：order by created_at DESC 时分页天然稳定，
// 无需引入 (created_at, id) 复合 cursor —— 同一毫秒内同 namespace 多次签发的概率
// 接近 0（owner/admin 才能签发）。
func (r *TokenRepo) GetByNamespacePaged(ctx context.Context, namespaceID uuid.UUID, limit int, cursor string) ([]model.APIToken, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := r.db.WithContext(ctx).
		Where("namespace_id = ? AND revoked_at IS NULL", namespaceID).
		Order("created_at DESC").
		Limit(limit + 1)
	if cursor != "" {
		t, err := time.Parse(time.RFC3339Nano, cursor)
		if err == nil {
			q = q.Where("created_at < ?", t)
		}
	}
	var tokens []model.APIToken
	if err := q.Find(&tokens).Error; err != nil {
		return nil, "", err
	}
	var next string
	if len(tokens) > limit {
		next = tokens[limit-1].CreatedAt.Format(time.RFC3339Nano)
		tokens = tokens[:limit]
	}
	return tokens, next, nil
}

// CountActiveByNamespace 统计某 namespace 下未吊销的 token 数量。供配额校验用。
func (r *TokenRepo) CountActiveByNamespace(ctx context.Context, namespaceID uuid.UUID) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&model.APIToken{}).
		Where("namespace_id = ? AND revoked_at IS NULL", namespaceID).
		Count(&n).Error
	return n, err
}

// RevokeByNamespaceAndUser 把 (namespace, user) 名下未吊销的 token 全部 mark revoked。
// 返回受影响行数（用于审计/指标）。成员被踢出时调用。
func (r *TokenRepo) RevokeByNamespaceAndUser(ctx context.Context, namespaceID, userID uuid.UUID) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&model.APIToken{}).
		Where("namespace_id = ? AND user_id = ? AND revoked_at IS NULL", namespaceID, userID).
		Update("revoked_at", time.Now())
	return res.RowsAffected, res.Error
}

// RevokeByNamespace 把整个 namespace 下未吊销的 token 全部 mark revoked。
// namespace 被删除时调用。
func (r *TokenRepo) RevokeByNamespace(ctx context.Context, namespaceID uuid.UUID) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&model.APIToken{}).
		Where("namespace_id = ? AND revoked_at IS NULL", namespaceID).
		Update("revoked_at", time.Now())
	return res.RowsAffected, res.Error
}

func (r *TokenRepo) UpdateLastUsed(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&model.APIToken{}).
		Where("id = ?", id).
		Update("last_used_at", time.Now()).Error
}

func (r *TokenRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&model.APIToken{}).
		Where("id = ?", id).
		Update("revoked_at", time.Now()).Error
}

func (r *TokenRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.APIToken, error) {
	var token model.APIToken
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&token).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &token, err
}
