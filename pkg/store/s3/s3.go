// Package s3 是 SkillHub 的 AWS S3（含 S3 兼容服务，如 MinIO）backend 子包。
//
// 通过 init() 自注册到 store 驱动表，调用方只需 blank import：
//
//	import _ "github.com/saker-ai/skillhub/pkg/store/s3"
//
// 即可让 cfg.Store.Backend == "s3" 自动解析到本 backend。
//
// 嵌入方如果不需要 S3 后端，可跳过 blank import 以减小二进制大小
// （不会链接 aws-sdk-go-v2，节省约几 MB）。
package s3

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/semver"
	"github.com/saker-ai/skillhub/pkg/store"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func init() {
	store.Register("s3", openS3)
}

// openS3 是 store.Factory 实现：从 OpenContext 取出 cfg.S3 子树并构造 Backend。
func openS3(oc store.OpenContext) (store.Store, error) {
	return New(oc.Cfg.S3)
}

// Config 是 S3 backend 的旧版直构造配置。
//
// 为兼容嵌入方现有代码（NewWithConfig 等命名），保留本类型并提供与
// config.StoreS3Config 互相转换的能力，但配置文件路径仍走 config 包。
type Config = config.StoreS3Config

// Backend implements store.Store using AWS S3 (or S3-compatible) storage.
type Backend struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	prefix  string
}

// New 是直接构造入口（不经过 driver registry）。
// 推荐路径仍是 store.Open("s3", ...)。
func New(cfg Config) (*Backend, error) {
	ctx := context.Background()

	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "skills"
	}

	return &Backend{
		client:  client,
		presign: s3.NewPresignClient(client),
		bucket:  cfg.Bucket,
		prefix:  prefix,
	}, nil
}

// key builds an S3 object key: {prefix}/{owner}/{slug}/versions/{version}/files/{path}
func (b *Backend) key(owner, slug, version, filePath string) string {
	filePath = store.SanitizeStorePath(filePath)
	return fmt.Sprintf("%s/%s/%s/versions/%s/files/%s", b.prefix, owner, slug, version, filePath)
}

// metaKey returns the metadata object key for a version.
func (b *Backend) metaKey(owner, slug, version string) string {
	return fmt.Sprintf("%s/%s/%s/versions/%s/meta.json", b.prefix, owner, slug, version)
}

func (b *Backend) Provider() string { return "s3" }

func (b *Backend) ObjectKey(owner, slug, version, filePath string) string {
	return b.key(owner, slug, version, filePath)
}

// versionPrefix returns the prefix for listing all versions of a skill.
func (b *Backend) versionPrefix(owner, slug string) string {
	return fmt.Sprintf("%s/%s/%s/versions/", b.prefix, owner, slug)
}

func (b *Backend) Publish(ctx context.Context, opts store.PublishOpts) (string, error) {
	// Upload each file
	for filePath, content := range opts.Files {
		key := b.key(opts.Owner, opts.Slug, opts.Version, filePath)
		_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &b.bucket,
			Key:    &key,
			Body:   bytes.NewReader(content),
		})
		if err != nil {
			return "", fmt.Errorf("upload %s: %w", filePath, err)
		}
	}

	return b.PutMeta(ctx, opts)
}

func (b *Backend) PutMeta(ctx context.Context, opts store.PublishOpts) (string, error) {
	meta := store.PublishMeta{
		Version:   opts.Version,
		Author:    opts.Author,
		Email:     opts.Email,
		Message:   opts.Message,
		Tags:      opts.Tags,
		CreatedAt: time.Now(),
	}
	metaBytes, _ := json.Marshal(meta)
	metaKey := b.metaKey(opts.Owner, opts.Slug, opts.Version)
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &b.bucket,
		Key:    &metaKey,
		Body:   bytes.NewReader(metaBytes),
	})
	if err != nil {
		return "", fmt.Errorf("upload meta: %w", err)
	}

	return fmt.Sprintf("s3://%s/%s", b.bucket, metaKey), nil
}

func (b *Backend) PresignPut(ctx context.Context, owner, slug, version, filePath, contentType string, expires time.Duration) (*store.DirectObjectURL, error) {
	key := b.key(owner, slug, version, filePath)
	input := &s3.PutObjectInput{
		Bucket: &b.bucket,
		Key:    &key,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}
	out, err := b.presign.PresignPutObject(ctx, input, func(o *s3.PresignOptions) {
		o.Expires = expires
	})
	if err != nil {
		return nil, fmt.Errorf("presign put object: %w", err)
	}
	return &store.DirectObjectURL{
		Provider:  b.Provider(),
		Bucket:    b.bucket,
		Key:       key,
		Method:    out.Method,
		URL:       out.URL,
		Headers:   headerMap(out.SignedHeader),
		ExpiresAt: time.Now().Add(expires),
	}, nil
}

func (b *Backend) PresignGet(ctx context.Context, owner, slug, version, filePath string, expires time.Duration) (*store.DirectObjectURL, error) {
	key := b.key(owner, slug, version, filePath)
	out, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &b.bucket,
		Key:    &key,
	}, func(o *s3.PresignOptions) {
		o.Expires = expires
	})
	if err != nil {
		return nil, fmt.Errorf("presign get object: %w", err)
	}
	return &store.DirectObjectURL{
		Provider:  b.Provider(),
		Bucket:    b.bucket,
		Key:       key,
		Method:    out.Method,
		URL:       out.URL,
		Headers:   headerMap(out.SignedHeader),
		ExpiresAt: time.Now().Add(expires),
	}, nil
}

func (b *Backend) CreateMultipartUpload(ctx context.Context, owner, slug, version, filePath, contentType string, size, partSize int64, expires time.Duration) (*store.MultipartObjectUpload, error) {
	key := b.key(owner, slug, version, filePath)
	input := &s3.CreateMultipartUploadInput{
		Bucket: &b.bucket,
		Key:    &key,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}
	created, err := b.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("create multipart upload: %w", err)
	}
	uploadID := aws.ToString(created.UploadId)
	parts, err := b.presignMultipartParts(ctx, key, uploadID, size, partSize, expires)
	if err != nil {
		_ = b.AbortMultipartUpload(ctx, owner, slug, version, filePath, uploadID)
		return nil, err
	}
	return &store.MultipartObjectUpload{UploadID: uploadID, PartSize: partSize, Parts: parts}, nil
}

func (b *Backend) presignMultipartParts(ctx context.Context, key, uploadID string, size, partSize int64, expires time.Duration) ([]store.DirectObjectPart, error) {
	if partSize <= 0 {
		return nil, fmt.Errorf("invalid multipart part size")
	}
	partCount := int((size + partSize - 1) / partSize)
	parts := make([]store.DirectObjectPart, 0, partCount)
	for i := 0; i < partCount; i++ {
		partNumber := int32(i + 1)
		offset := int64(i) * partSize
		partBytes := partSize
		if remaining := size - offset; remaining < partBytes {
			partBytes = remaining
		}
		out, err := b.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket:     &b.bucket,
			Key:        &key,
			UploadId:   &uploadID,
			PartNumber: &partNumber,
		}, func(o *s3.PresignOptions) {
			o.Expires = expires
		})
		if err != nil {
			return nil, fmt.Errorf("presign upload part %d: %w", i+1, err)
		}
		parts = append(parts, store.DirectObjectPart{
			PartNumber: i + 1,
			Method:     out.Method,
			URL:        out.URL,
			Headers:    headerMap(out.SignedHeader),
			Offset:     offset,
			Size:       partBytes,
		})
	}
	return parts, nil
}

func (b *Backend) CompleteMultipartUpload(ctx context.Context, owner, slug, version, filePath, uploadID string, parts []store.CompletedUploadPart) error {
	key := b.key(owner, slug, version, filePath)
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		partNumber := int32(part.PartNumber)
		etag := strings.TrimSpace(part.ETag)
		completed = append(completed, types.CompletedPart{PartNumber: &partNumber, ETag: &etag})
	}
	_, err := b.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   &b.bucket,
		Key:      &key,
		UploadId: &uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completed,
		},
	})
	if err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}
	return nil
}

func (b *Backend) AbortMultipartUpload(ctx context.Context, owner, slug, version, filePath, uploadID string) error {
	key := b.key(owner, slug, version, filePath)
	_, err := b.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   &b.bucket,
		Key:      &key,
		UploadId: &uploadID,
	})
	if err != nil {
		return fmt.Errorf("abort multipart upload: %w", err)
	}
	return nil
}

func (b *Backend) Archive(ctx context.Context, owner, slug, version string) (io.ReadCloser, error) {
	prefix := b.key(owner, slug, version, "")

	// List all files under this version
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: &b.bucket,
		Prefix: &prefix,
	})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		for _, obj := range page.Contents {
			// Extract relative path from key
			relPath := strings.TrimPrefix(*obj.Key, prefix)
			if relPath == "" {
				continue
			}

			// Download file
			getOut, err := b.client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: &b.bucket,
				Key:    obj.Key,
			})
			if err != nil {
				return nil, fmt.Errorf("get object %s: %w", relPath, err)
			}

			data, err := io.ReadAll(io.LimitReader(getOut.Body, 10<<20)) // 10MB per file
			getOut.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("read object %s: %w", relPath, err)
			}

			archivePath := fmt.Sprintf("%s-%s/%s", slug, version, relPath)
			w, err := zw.Create(archivePath)
			if err != nil {
				return nil, fmt.Errorf("create zip entry %s: %w", relPath, err)
			}
			if _, err := w.Write(data); err != nil {
				return nil, fmt.Errorf("write zip entry %s: %w", relPath, err)
			}
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}

	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (b *Backend) GetFile(ctx context.Context, owner, slug, version, path string) ([]byte, error) {
	key := b.key(owner, slug, version, path)

	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &b.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("get file %s: %w", path, err)
	}
	defer out.Body.Close()

	return io.ReadAll(io.LimitReader(out.Body, 10<<20)) // 10MB limit
}

func (b *Backend) ListVersions(ctx context.Context, owner, slug string) ([]string, error) {
	prefix := b.versionPrefix(owner, slug)
	delimiter := "/"

	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket:    &b.bucket,
		Prefix:    &prefix,
		Delimiter: &delimiter,
	})

	var versions []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list versions: %w", err)
		}
		for _, cp := range page.CommonPrefixes {
			// Extract version from prefix like "skills/owner/slug/versions/1.0.0/"
			ver := strings.TrimPrefix(*cp.Prefix, prefix)
			ver = strings.TrimSuffix(ver, "/")
			if ver != "" {
				versions = append(versions, ver)
			}
		}
	}

	// Sort descending by semver
	sort.Slice(versions, func(i, j int) bool {
		return semver.Compare(versions[i], versions[j]) > 0
	})

	return versions, nil
}

func (b *Backend) Exists(ctx context.Context, owner, slug string) bool {
	prefix := b.versionPrefix(owner, slug)
	maxKeys := int32(1)

	out, err := b.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  &b.bucket,
		Prefix:  &prefix,
		MaxKeys: &maxKeys,
	})
	if err != nil {
		return false
	}
	return len(out.Contents) > 0
}

func (b *Backend) Delete(ctx context.Context, owner, slug string) error {
	if !store.ValidatePathComponent(owner) || !store.ValidatePathComponent(slug) {
		return fmt.Errorf("invalid owner or slug")
	}
	prefix := fmt.Sprintf("%s/%s/%s/", b.prefix, owner, slug)
	return b.deleteByPrefix(ctx, prefix)
}

func (b *Backend) DeleteVersion(ctx context.Context, owner, slug, version string) error {
	if !store.ValidatePathComponent(owner) || !store.ValidatePathComponent(slug) || !store.ValidatePathComponent(version) {
		return fmt.Errorf("invalid owner, slug, or version")
	}
	prefix := fmt.Sprintf("%s/%s/%s/versions/%s/", b.prefix, owner, slug, version)
	return b.deleteByPrefix(ctx, prefix)
}

func (b *Backend) deleteByPrefix(ctx context.Context, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: &b.bucket,
		Prefix: &prefix,
	})

	const batchSize = 1000
	batch := make([]types.ObjectIdentifier, 0, batchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		quiet := true
		resp, err := b.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &b.bucket,
			Delete: &types.Delete{Objects: batch, Quiet: &quiet},
		})
		batch = batch[:0]
		if err != nil {
			return fmt.Errorf("batch delete objects: %w", err)
		}
		if resp != nil && len(resp.Errors) > 0 {
			first := resp.Errors[0]
			return fmt.Errorf("batch delete: %d object(s) failed, first: key=%s code=%s",
				len(resp.Errors), deref(first.Key), deref(first.Code))
		}
		return nil
	}

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list objects for delete: %w", err)
		}
		for _, obj := range page.Contents {
			batch = append(batch, types.ObjectIdentifier{Key: obj.Key})
			if len(batch) >= batchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	return flush()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func headerMap(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, vals := range h {
		out[k] = strings.Join(vals, ",")
	}
	return out
}

func (b *Backend) Rename(ctx context.Context, owner, oldSlug, newSlug string) error {
	if !store.ValidatePathComponent(owner) || !store.ValidatePathComponent(oldSlug) || !store.ValidatePathComponent(newSlug) {
		return fmt.Errorf("invalid owner or slug")
	}
	oldPrefix := fmt.Sprintf("%s/%s/%s/", b.prefix, owner, oldSlug)
	newPrefix := fmt.Sprintf("%s/%s/%s/", b.prefix, owner, newSlug)

	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: &b.bucket,
		Prefix: &oldPrefix,
	})

	// Phase 1: Copy all objects to new prefix
	var oldKeys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list objects for rename: %w", err)
		}
		for _, obj := range page.Contents {
			newKey := newPrefix + strings.TrimPrefix(*obj.Key, oldPrefix)
			copySource := fmt.Sprintf("%s/%s", b.bucket, *obj.Key)

			_, err := b.client.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket:     &b.bucket,
				Key:        &newKey,
				CopySource: &copySource,
			})
			if err != nil {
				return fmt.Errorf("copy %s: %w", *obj.Key, err)
			}
			oldKeys = append(oldKeys, *obj.Key)
		}
	}

	// Phase 2: Delete all old objects
	for _, key := range oldKeys {
		k := key
		if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &b.bucket,
			Key:    &k,
		}); err != nil {
			slog.Default().Warn("failed to delete old key during rename", "key", key, "err", err)
		}
	}

	return nil
}
