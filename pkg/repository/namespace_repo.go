package repository

import (
	"context"
	"errors"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type NamespaceRepo struct {
	db *gorm.DB
}

func NewNamespaceRepo(db *gorm.DB) *NamespaceRepo {
	return &NamespaceRepo{db: db}
}

func (r *NamespaceRepo) Create(ctx context.Context, ns *model.Namespace) error {
	return r.db.WithContext(ctx).Create(ns).Error
}

func (r *NamespaceRepo) GetBySlug(ctx context.Context, slug string) (*model.Namespace, error) {
	var ns model.Namespace
	err := r.db.WithContext(ctx).Where("slug = ?", slug).First(&ns).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &ns, err
}

func (r *NamespaceRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Namespace, error) {
	var ns model.Namespace
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&ns).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &ns, err
}

func (r *NamespaceRepo) Update(ctx context.Context, ns *model.Namespace) error {
	return r.db.WithContext(ctx).Save(ns).Error
}

// ListByUser returns all namespaces where the user is a member.
func (r *NamespaceRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Namespace, error) {
	var namespaces []model.Namespace
	err := r.db.WithContext(ctx).
		Joins("JOIN namespace_members ON namespace_members.namespace_id = namespaces.id").
		Where("namespace_members.user_id = ?", userID).
		Order("namespaces.created_at DESC").
		Find(&namespaces).Error
	return namespaces, err
}

// AddMember adds a user to a namespace with the given role.
func (r *NamespaceRepo) AddMember(ctx context.Context, member *model.NamespaceMember) error {
	return r.db.WithContext(ctx).Create(member).Error
}

// RemoveMember removes a user from a namespace.
func (r *NamespaceRepo) RemoveMember(ctx context.Context, namespaceID, userID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("namespace_id = ? AND user_id = ?", namespaceID, userID).
		Delete(&model.NamespaceMember{}).Error
}

// UpdateMemberRole changes a member's role in a namespace.
func (r *NamespaceRepo) UpdateMemberRole(ctx context.Context, namespaceID, userID uuid.UUID, role string) error {
	return r.db.WithContext(ctx).
		Model(&model.NamespaceMember{}).
		Where("namespace_id = ? AND user_id = ?", namespaceID, userID).
		Update("role", role).Error
}

// GetMemberRole returns the member's role in a namespace, or "" if not a member.
func (r *NamespaceRepo) GetMemberRole(ctx context.Context, namespaceID, userID uuid.UUID) (string, error) {
	var member model.NamespaceMember
	err := r.db.WithContext(ctx).
		Where("namespace_id = ? AND user_id = ?", namespaceID, userID).
		First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return member.Role, nil
}

// ListMembers returns all members of a namespace with user info.
func (r *NamespaceRepo) ListMembers(ctx context.Context, namespaceID uuid.UUID) ([]model.NamespaceMemberWithUser, error) {
	var members []model.NamespaceMemberWithUser
	err := r.db.WithContext(ctx).
		Table("namespace_members").
		Select("namespace_members.*, users.handle, users.display_name").
		Joins("JOIN users ON users.id = namespace_members.user_id").
		Where("namespace_members.namespace_id = ?", namespaceID).
		Order("namespace_members.created_at ASC").
		Find(&members).Error
	return members, err
}

// TransferOwnership atomically promotes newOwnerID to "owner" and demotes the
// previous owner to "admin", and updates namespaces.owner_id.
func (r *NamespaceRepo) TransferOwnership(ctx context.Context, namespaceID, oldOwnerID, newOwnerID uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.NamespaceMember{}).
			Where("namespace_id = ? AND user_id = ?", namespaceID, newOwnerID).
			Update("role", "owner").Error; err != nil {
			return err
		}
		if err := tx.Model(&model.NamespaceMember{}).
			Where("namespace_id = ? AND user_id = ?", namespaceID, oldOwnerID).
			Update("role", "admin").Error; err != nil {
			return err
		}
		return tx.Model(&model.Namespace{}).
			Where("id = ?", namespaceID).
			Update("owner_id", newOwnerID).Error
	})
}

// GetPersonalByOwnerID returns the personal namespace for a user, or nil if none exists.
func (r *NamespaceRepo) GetPersonalByOwnerID(ctx context.Context, ownerID uuid.UUID) (*model.Namespace, error) {
	var ns model.Namespace
	err := r.db.WithContext(ctx).
		Where("owner_id = ? AND type = 'personal'", ownerID).
		First(&ns).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &ns, err
}

// EnsurePersonalNamespace finds or creates a personal namespace for the given user.
// The namespace slug is set to the user's handle. Thread-safe: concurrent calls
// for the same user will race on INSERT but the loser hits a unique constraint
// violation and falls back to a SELECT.
func (r *NamespaceRepo) EnsurePersonalNamespace(ctx context.Context, user *model.User) (*model.Namespace, error) {
	ns, err := r.GetPersonalByOwnerID(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if ns != nil {
		return ns, nil
	}

	ns = &model.Namespace{
		ID:      uuid.New(),
		Slug:    user.Handle,
		OwnerID: user.ID,
		Type:    "personal",
		Status:  "active",
	}

	if err := r.db.WithContext(ctx).Create(ns).Error; err != nil {
		// Race: another goroutine created it first — just fetch.
		existing, err2 := r.GetPersonalByOwnerID(ctx, user.ID)
		if err2 != nil {
			return nil, err2
		}
		if existing != nil {
			return existing, nil
		}
		return nil, err
	}

	member := &model.NamespaceMember{
		ID:          uuid.New(),
		NamespaceID: ns.ID,
		UserID:      user.ID,
		Role:        "owner",
	}
	if err := r.db.WithContext(ctx).Create(member).Error; err != nil {
		return nil, err
	}

	return ns, nil
}

// ListMemberIDs returns all member user IDs for a namespace.
func (r *NamespaceRepo) ListMemberIDs(ctx context.Context, namespaceID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).
		Model(&model.NamespaceMember{}).
		Where("namespace_id = ?", namespaceID).
		Pluck("user_id", &ids).Error
	return ids, err
}

// Delete removes a namespace.
func (r *NamespaceRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("namespace_id = ?", id).Delete(&model.NamespaceMember{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.Namespace{}).Error
	})
}
