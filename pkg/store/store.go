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
	Archive(ctx context.Context, owner, slug, version string) (io.ReadCloser, error)

	// GetFile reads a single file from a specific version.
	GetFile(ctx context.Context, owner, slug, version, path string) ([]byte, error)

	// ListVersions returns all version strings sorted descending.
	ListVersions(ctx context.Context, owner, slug string) ([]string, error)

	// Exists checks whether storage for the given skill exists.
	Exists(ctx context.Context, owner, slug string) bool

	// Rename renames skill storage from oldSlug to newSlug.
	Rename(ctx context.Context, owner, oldSlug, newSlug string) error

	// Delete removes all storage for a skill (used by admin purge).
	Delete(ctx context.Context, owner, slug string) error

	// DeleteVersion removes storage for a single version.
	// Used to clean up orphaned files when a publish transaction fails.
	DeleteVersion(ctx context.Context, owner, slug, version string) error
}

// DirectObjectStore is an optional extension implemented by object-store
// backends that can safely hand callers short-lived signed URLs.
type DirectObjectStore interface {
	Provider() string
	ObjectKey(owner, slug, version, filePath string) string
	PresignPut(ctx context.Context, owner, slug, version, filePath, contentType string, expires time.Duration) (*DirectObjectURL, error)
	PresignGet(ctx context.Context, owner, slug, version, filePath string, expires time.Duration) (*DirectObjectURL, error)
	PutMeta(ctx context.Context, opts PublishOpts) (string, error)
}

type MultipartObjectStore interface {
	DirectObjectStore
	CreateMultipartUpload(ctx context.Context, owner, slug, version, filePath, contentType string, size, partSize int64, expires time.Duration) (*MultipartObjectUpload, error)
	CompleteMultipartUpload(ctx context.Context, owner, slug, version, filePath, uploadID string, parts []CompletedUploadPart) error
	AbortMultipartUpload(ctx context.Context, owner, slug, version, filePath, uploadID string) error
}

type DirectObjectURL struct {
	Provider  string            `json:"provider"`
	Bucket    string            `json:"bucket"`
	Key       string            `json:"key"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

type MultipartObjectUpload struct {
	UploadID string             `json:"upload_id"`
	PartSize int64              `json:"part_size"`
	Parts    []DirectObjectPart `json:"parts"`
}

type DirectObjectPart struct {
	PartNumber int               `json:"part_number"`
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers,omitempty"`
	Offset     int64             `json:"offset"`
	Size       int64             `json:"size"`
}

type CompletedUploadPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag,omitempty"`
}

// ValidatePathComponent checks that a single path component (owner, slug, version)
// does not contain traversal sequences or separators. Returns false for unsafe values.
func ValidatePathComponent(s string) bool {
	return s != "" && s != "." &&
		!strings.Contains(s, "/") && !strings.Contains(s, "\\") &&
		!strings.Contains(s, "..")
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
