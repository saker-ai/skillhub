package model

import (
	"time"

	"github.com/google/uuid"
)

// NamespaceInvitation represents a pending invite to join a namespace.
// The flow: owner/admin creates an invitation for an existing user (by handle);
// the invitee accepts or declines from their own UI. Until accepted, no membership
// row exists, so the invitee gets no access.
type NamespaceInvitation struct {
	ID            uuid.UUID  `gorm:"column:id;type:text;primaryKey" json:"id"`
	NamespaceID   uuid.UUID  `gorm:"column:namespace_id;type:text;not null;index" json:"namespaceId"`
	InviterID     uuid.UUID  `gorm:"column:inviter_id;type:text;not null;index" json:"inviterId"`
	InviteeID     uuid.UUID  `gorm:"column:invitee_id;type:text;not null;index" json:"inviteeId"`
	InviteeHandle string     `gorm:"column:invitee_handle;type:varchar(64);not null" json:"inviteeHandle"`
	Role          string     `gorm:"column:role;type:varchar(20);not null;default:'member'" json:"role"`
	Status        string     `gorm:"column:status;type:varchar(20);not null;default:'pending';index" json:"status"` // pending|accepted|declined|revoked|expired
	Message       *string    `gorm:"column:message;type:text" json:"message,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	ExpiresAt     time.Time  `gorm:"column:expires_at;not null" json:"expiresAt"`
	RespondedAt   *time.Time `gorm:"column:responded_at" json:"respondedAt,omitempty"`
}

func (NamespaceInvitation) TableName() string { return "namespace_invitations" }
