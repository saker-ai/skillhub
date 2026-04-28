package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type SkillVersion struct {
	ID              uuid.UUID       `gorm:"column:id;type:text;primaryKey" json:"id"`
	SkillID         uuid.UUID       `gorm:"column:skill_id;type:text;not null;index" json:"skillId"`
	Version         string          `gorm:"column:version;type:varchar(64);not null" json:"version"`
	Fingerprint     string          `gorm:"column:fingerprint;type:varchar(128);not null;index" json:"fingerprint"`
	GitCommitHash   *string         `gorm:"column:git_commit_hash;type:varchar(64)" json:"gitCommitHash,omitempty"`
	Changelog       *string         `gorm:"column:changelog;type:text" json:"changelog,omitempty"`
	ChangelogSource *string         `gorm:"column:changelog_source;type:varchar(20)" json:"changelogSource,omitempty"`
	Files           json.RawMessage `gorm:"column:files;type:text;not null;default:'[]'" json:"files"`
	Parsed          json.RawMessage `gorm:"column:parsed;type:text;not null;default:'{}'" json:"parsed"`
	CreatedBy       uuid.UUID       `gorm:"column:created_by;type:text;not null" json:"createdBy"`
	SHA256Hash      string          `gorm:"column:sha256_hash;type:varchar(128);not null;index" json:"sha256Hash"`
	CreatedAt       time.Time       `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	SoftDeletedAt   *time.Time      `gorm:"column:soft_deleted_at" json:"softDeletedAt,omitempty"`

	// YankedAt: version still installable by exact pin, but excluded from
	// "latest" resolution and emits a warning. npm/PyPI semantics.
	YankedAt   *time.Time `gorm:"column:yanked_at" json:"yankedAt,omitempty"`
	YankReason *string    `gorm:"column:yank_reason;type:text" json:"yankReason,omitempty"`

	// DeprecatedAt: softer than yank — version still resolves normally but
	// installs surface a deprecation notice.
	DeprecatedAt       *time.Time `gorm:"column:deprecated_at" json:"deprecatedAt,omitempty"`
	DeprecationMessage *string    `gorm:"column:deprecation_message;type:text" json:"deprecationMessage,omitempty"`
}

func (SkillVersion) TableName() string { return "skill_versions" }

type VersionFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"contentType,omitempty"`
}
