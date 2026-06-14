package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/model"
	"gorm.io/gorm"
)

type NamespaceInvitationRepo struct {
	db *gorm.DB
}

func NewNamespaceInvitationRepo(db *gorm.DB) *NamespaceInvitationRepo {
	return &NamespaceInvitationRepo{db: db}
}

func (r *NamespaceInvitationRepo) Create(ctx context.Context, inv *model.NamespaceInvitation) error {
	return r.db.WithContext(ctx).Create(inv).Error
}

func (r *NamespaceInvitationRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.NamespaceInvitation, error) {
	var inv model.NamespaceInvitation
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&inv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &inv, err
}

// GetPending returns the pending invitation for a (namespace, invitee) pair, if any.
func (r *NamespaceInvitationRepo) GetPending(ctx context.Context, namespaceID, inviteeID uuid.UUID) (*model.NamespaceInvitation, error) {
	var inv model.NamespaceInvitation
	err := r.db.WithContext(ctx).
		Where("namespace_id = ? AND invitee_id = ? AND status = ?", namespaceID, inviteeID, "pending").
		First(&inv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &inv, err
}

// ListByNamespace returns invitations for a namespace, optionally filtered by status.
func (r *NamespaceInvitationRepo) ListByNamespace(ctx context.Context, namespaceID uuid.UUID, status string) ([]model.NamespaceInvitation, error) {
	var invs []model.NamespaceInvitation
	q := r.db.WithContext(ctx).Where("namespace_id = ?", namespaceID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	err := q.Order("created_at DESC").Find(&invs).Error
	return invs, err
}

// ListByInvitee returns pending invitations for a user.
func (r *NamespaceInvitationRepo) ListByInvitee(ctx context.Context, inviteeID uuid.UUID) ([]model.NamespaceInvitation, error) {
	var invs []model.NamespaceInvitation
	err := r.db.WithContext(ctx).
		Where("invitee_id = ? AND status = ? AND expires_at > ?", inviteeID, "pending", time.Now()).
		Order("created_at DESC").
		Find(&invs).Error
	return invs, err
}

// UpdateStatus marks an invitation with a new status and stamps responded_at.
func (r *NamespaceInvitationRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&model.NamespaceInvitation{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": status, "responded_at": &now}).Error
}

// AcceptAndAddMember atomically marks the invitation accepted and creates the
// membership row, so a partial failure cannot leave a half-joined state.
func (r *NamespaceInvitationRepo) AcceptAndAddMember(ctx context.Context, inv *model.NamespaceInvitation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&model.NamespaceInvitation{}).
			Where("id = ? AND status = ?", inv.ID, "pending").
			Updates(map[string]any{"status": "accepted", "responded_at": &now}).Error; err != nil {
			return err
		}
		member := &model.NamespaceMember{
			ID:          uuid.New(),
			NamespaceID: inv.NamespaceID,
			UserID:      inv.InviteeID,
			Role:        inv.Role,
		}
		return tx.Create(member).Error
	})
}
