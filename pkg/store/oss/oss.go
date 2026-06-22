// Package oss 是 SkillHub 的阿里云 OSS backend 子包。
//
// 通过 init() 自注册到 store 驱动表，调用方只需 blank import：
//
//	import _ "github.com/saker-ai/skillhub/pkg/store/oss"
//
// 即可让 cfg.Store.Backend == "oss" 自动解析到本 backend。
//
// 嵌入方如果不需要 OSS，可跳过 blank import 以减小二进制大小
// （不会链接 alibabacloud-oss-go-sdk-v2）。
package oss

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/semver"
	"github.com/saker-ai/skillhub/pkg/store"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	osscredentials "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

func init() {
	store.Register("oss", openOSS)
}

// openOSS 是 store.Factory 实现：从 OpenContext 取出 cfg.OSS 子树并构造 Backend。
func openOSS(oc store.OpenContext) (store.Store, error) {
	return New(oc.Cfg.OSS)
}

// Config 是 OSS backend 的旧版直构造配置。
//
// 与 config.StoreOSSConfig 等价，保留 alias 仅为命名习惯。
type Config = config.StoreOSSConfig

// Backend implements store.Store using Alibaba Cloud OSS.
type Backend struct {
	client *oss.Client
	bucket string
	prefix string
}

// New 是直接构造入口（不经过 driver registry）。
// 推荐路径仍是 store.Open("oss", ...)。
func New(cfg Config) (*Backend, error) {
	ossCfg := oss.LoadDefaultConfig().
		WithRegion(cfg.Region)

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		provider := osscredentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey)
		ossCfg = ossCfg.WithCredentialsProvider(provider)
	}

	if cfg.Endpoint != "" {
		ossCfg = ossCfg.WithEndpoint(cfg.Endpoint)
	}

	client := oss.NewClient(ossCfg)

	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "skills"
	}

	return &Backend{
		client: client,
		bucket: cfg.Bucket,
		prefix: prefix,
	}, nil
}

func (b *Backend) key(owner, slug, version, filePath string) string {
	filePath = store.SanitizeStorePath(filePath)
	return fmt.Sprintf("%s/%s/%s/versions/%s/files/%s", b.prefix, owner, slug, version, filePath)
}

func (b *Backend) metaKey(owner, slug, version string) string {
	return fmt.Sprintf("%s/%s/%s/versions/%s/meta.json", b.prefix, owner, slug, version)
}

func (b *Backend) Provider() string { return "oss" }

func (b *Backend) ObjectKey(owner, slug, version, filePath string) string {
	return b.key(owner, slug, version, filePath)
}

func (b *Backend) versionPrefix(owner, slug string) string {
	return fmt.Sprintf("%s/%s/%s/versions/", b.prefix, owner, slug)
}

func (b *Backend) Publish(ctx context.Context, opts store.PublishOpts) (string, error) {
	// Upload each file
	for filePath, content := range opts.Files {
		key := b.key(opts.Owner, opts.Slug, opts.Version, filePath)
		_, err := b.client.PutObject(ctx, &oss.PutObjectRequest{
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
	_, err := b.client.PutObject(ctx, &oss.PutObjectRequest{
		Bucket: &b.bucket,
		Key:    &metaKey,
		Body:   bytes.NewReader(metaBytes),
	})
	if err != nil {
		return "", fmt.Errorf("upload meta: %w", err)
	}

	return fmt.Sprintf("oss://%s/%s", b.bucket, metaKey), nil
}

func (b *Backend) PresignPut(ctx context.Context, owner, slug, version, filePath, contentType string, expires time.Duration) (*store.DirectObjectURL, error) {
	key := b.key(owner, slug, version, filePath)
	input := &oss.PutObjectRequest{
		Bucket: &b.bucket,
		Key:    &key,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}
	out, err := b.client.Presign(ctx, input, oss.PresignExpires(expires))
	if err != nil {
		return nil, fmt.Errorf("presign put object: %w", err)
	}
	return &store.DirectObjectURL{
		Provider:  b.Provider(),
		Bucket:    b.bucket,
		Key:       key,
		Method:    out.Method,
		URL:       out.URL,
		Headers:   out.SignedHeaders,
		ExpiresAt: out.Expiration,
	}, nil
}

func (b *Backend) PresignGet(ctx context.Context, owner, slug, version, filePath string, expires time.Duration) (*store.DirectObjectURL, error) {
	key := b.key(owner, slug, version, filePath)
	out, err := b.client.Presign(ctx, &oss.GetObjectRequest{
		Bucket: &b.bucket,
		Key:    &key,
	}, oss.PresignExpires(expires))
	if err != nil {
		return nil, fmt.Errorf("presign get object: %w", err)
	}
	return &store.DirectObjectURL{
		Provider:  b.Provider(),
		Bucket:    b.bucket,
		Key:       key,
		Method:    out.Method,
		URL:       out.URL,
		Headers:   out.SignedHeaders,
		ExpiresAt: out.Expiration,
	}, nil
}

func (b *Backend) CreateMultipartUpload(ctx context.Context, owner, slug, version, filePath, contentType string, size, partSize int64, expires time.Duration) (*store.MultipartObjectUpload, error) {
	key := b.key(owner, slug, version, filePath)
	input := &oss.InitiateMultipartUploadRequest{
		Bucket: &b.bucket,
		Key:    &key,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}
	created, err := b.client.InitiateMultipartUpload(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("initiate multipart upload: %w", err)
	}
	uploadID := strings.TrimSpace(oss.ToString(created.UploadId))
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
		out, err := b.client.Presign(ctx, &oss.UploadPartRequest{
			Bucket:        &b.bucket,
			Key:           &key,
			UploadId:      &uploadID,
			PartNumber:    partNumber,
			ContentLength: &partBytes,
		}, oss.PresignExpires(expires))
		if err != nil {
			return nil, fmt.Errorf("presign upload part %d: %w", i+1, err)
		}
		parts = append(parts, store.DirectObjectPart{
			PartNumber: i + 1,
			Method:     out.Method,
			URL:        out.URL,
			Headers:    out.SignedHeaders,
			Offset:     offset,
			Size:       partBytes,
		})
	}
	return parts, nil
}

func (b *Backend) CompleteMultipartUpload(ctx context.Context, owner, slug, version, filePath, uploadID string, parts []store.CompletedUploadPart) error {
	key := b.key(owner, slug, version, filePath)
	completed := make([]oss.UploadPart, 0, len(parts))
	for _, part := range parts {
		etag := strings.TrimSpace(part.ETag)
		completed = append(completed, oss.UploadPart{PartNumber: int32(part.PartNumber), ETag: &etag})
	}
	_, err := b.client.CompleteMultipartUpload(ctx, &oss.CompleteMultipartUploadRequest{
		Bucket:   &b.bucket,
		Key:      &key,
		UploadId: &uploadID,
		CompleteMultipartUpload: &oss.CompleteMultipartUpload{
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
	_, err := b.client.AbortMultipartUpload(ctx, &oss.AbortMultipartUploadRequest{
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

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	var continuationToken *string
	for {
		out, err := b.client.ListObjectsV2(ctx, &oss.ListObjectsV2Request{
			Bucket:            &b.bucket,
			Prefix:            &prefix,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}

		for _, obj := range out.Contents {
			relPath := strings.TrimPrefix(*obj.Key, prefix)
			if relPath == "" {
				continue
			}

			getOut, err := b.client.GetObject(ctx, &oss.GetObjectRequest{
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
				return nil, fmt.Errorf("create zip entry: %w", err)
			}
			if _, err := w.Write(data); err != nil {
				return nil, fmt.Errorf("write zip entry: %w", err)
			}
		}

		if !out.IsTruncated {
			break
		}
		continuationToken = out.NextContinuationToken
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}

	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (b *Backend) GetFile(ctx context.Context, owner, slug, version, path string) ([]byte, error) {
	key := b.key(owner, slug, version, path)

	out, err := b.client.GetObject(ctx, &oss.GetObjectRequest{
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

	var versions []string
	var continuationToken *string
	for {
		out, err := b.client.ListObjectsV2(ctx, &oss.ListObjectsV2Request{
			Bucket:            &b.bucket,
			Prefix:            &prefix,
			Delimiter:         &delimiter,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list versions: %w", err)
		}

		for _, cp := range out.CommonPrefixes {
			ver := strings.TrimPrefix(*cp.Prefix, prefix)
			ver = strings.TrimSuffix(ver, "/")
			if ver != "" {
				versions = append(versions, ver)
			}
		}

		if !out.IsTruncated {
			break
		}
		continuationToken = out.NextContinuationToken
	}

	sort.Slice(versions, func(i, j int) bool {
		return semver.Compare(versions[i], versions[j]) > 0
	})

	return versions, nil
}

func (b *Backend) Exists(ctx context.Context, owner, slug string) bool {
	prefix := b.versionPrefix(owner, slug)
	maxKeys := int32(1)

	out, err := b.client.ListObjectsV2(ctx, &oss.ListObjectsV2Request{
		Bucket:  &b.bucket,
		Prefix:  &prefix,
		MaxKeys: maxKeys,
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
	var keys []string
	var continuationToken *string
	for {
		out, err := b.client.ListObjectsV2(ctx, &oss.ListObjectsV2Request{
			Bucket:            &b.bucket,
			Prefix:            &prefix,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return fmt.Errorf("list objects for delete: %w", err)
		}
		for _, obj := range out.Contents {
			keys = append(keys, *obj.Key)
		}
		if !out.IsTruncated {
			break
		}
		continuationToken = out.NextContinuationToken
	}

	const batchSize = 1000
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := make([]oss.DeleteObject, 0, end-i)
		for _, k := range keys[i:end] {
			k := k
			batch = append(batch, oss.DeleteObject{Key: &k})
		}
		if _, err := b.client.DeleteMultipleObjects(ctx, &oss.DeleteMultipleObjectsRequest{
			Bucket:  &b.bucket,
			Objects: batch,
			Quiet:   true,
		}); err != nil {
			return fmt.Errorf("batch delete objects: %w", err)
		}
	}
	return nil
}

func (b *Backend) Rename(ctx context.Context, owner, oldSlug, newSlug string) error {
	if !store.ValidatePathComponent(owner) || !store.ValidatePathComponent(oldSlug) || !store.ValidatePathComponent(newSlug) {
		return fmt.Errorf("invalid owner or slug")
	}
	oldPrefix := fmt.Sprintf("%s/%s/%s/", b.prefix, owner, oldSlug)
	newPrefix := fmt.Sprintf("%s/%s/%s/", b.prefix, owner, newSlug)

	// Phase 1: Copy all objects to new prefix
	var oldKeys []string
	var continuationToken *string
	for {
		out, err := b.client.ListObjectsV2(ctx, &oss.ListObjectsV2Request{
			Bucket:            &b.bucket,
			Prefix:            &oldPrefix,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return fmt.Errorf("list objects for rename: %w", err)
		}

		for _, obj := range out.Contents {
			newKey := newPrefix + strings.TrimPrefix(*obj.Key, oldPrefix)
			copySource := fmt.Sprintf("/%s/%s", b.bucket, *obj.Key)

			_, err := b.client.CopyObject(ctx, &oss.CopyObjectRequest{
				Bucket:    &b.bucket,
				Key:       &newKey,
				SourceKey: &copySource,
			})
			if err != nil {
				return fmt.Errorf("copy %s: %w", *obj.Key, err)
			}
			oldKeys = append(oldKeys, *obj.Key)
		}

		if !out.IsTruncated {
			break
		}
		continuationToken = out.NextContinuationToken
	}

	// Phase 2: Delete all old objects
	for _, key := range oldKeys {
		k := key
		if _, err := b.client.DeleteObject(ctx, &oss.DeleteObjectRequest{
			Bucket: &b.bucket,
			Key:    &k,
		}); err != nil {
			slog.Default().Warn("failed to delete old key during rename", "key", key, "err", err)
		}
	}

	return nil
}
