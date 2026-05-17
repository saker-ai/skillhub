package store

import (
	"context"
	"io"
	"path"
	"strings"
	"time"
)

// Store is the abstraction for skill file storage backends (git, S3, OSS, etc.).
type Store interface {
	// Publish writes files and creates a version. Returns a version identifier
	// (e.g. git commit hash, S3 ETag).
	Publish(ctx context.Context, opts PublishOpts) (string, error)

	// Archive returns a zip archive stream for the given version.
	Archive(owner, slug, version string) (io.ReadCloser, error)

	// GetFile reads a single file from a specific version.
	GetFile(owner, slug, version, path string) ([]byte, error)

	// ListVersions returns all version strings sorted descending.
	ListVersions(owner, slug string) ([]string, error)

	// Exists checks whether storage for the given skill exists.
	Exists(owner, slug string) bool

	// Rename renames skill storage from oldSlug to newSlug.
	Rename(owner, oldSlug, newSlug string) error

	// Delete removes all storage for a skill (used by admin purge).
	Delete(owner, slug string) error
}

// SanitizeStorePath cleans a file path to prevent directory traversal in storage keys.
//
// 阶段 3 起改为导出，便于子包（pkg/store/s3、pkg/store/oss 等）共享同一份实现，
// 避免每个 backend 各自手写一遍逻辑导致漂移。
func SanitizeStorePath(p string) string {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	// Reject any remaining traversal
	if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return "invalid"
	}
	return p
}

// PublishOpts contains backend-agnostic parameters for publishing a skill version.
type PublishOpts struct {
	Owner   string
	Slug    string
	Version string            // e.g. "1.0.0"
	Files   map[string][]byte // path → content
	Author  string
	Email   string
	Message string   // changelog / commit message
	Tags    []string // extra tags
}

// PublishMeta 是 object-store 类后端（S3 / OSS）写入 meta.json 的统一序列化格式。
//
// git backend 不使用本结构（git 直接由 commit 记录元信息）。
// 阶段 3 起从 store 子包提升到本包以便 s3 / oss 子包共用。
type PublishMeta struct {
	Version   string    `json:"version"`
	Author    string    `json:"author"`
	Email     string    `json:"email"`
	Message   string    `json:"message"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
