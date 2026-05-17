// Package s3 是 SkillHub 的 AWS S3（含 S3 兼容服务，如 MinIO）backend 子包。
//
// 通过 init() 自注册到 store 驱动表，调用方只需 blank import：
//
//	import _ "github.com/cinience/skillhub/pkg/store/s3"
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
	"sort"
	"strings"
	"time"

	"github.com/cinience/skillhub/pkg/config"
	"github.com/cinience/skillhub/pkg/semver"
	"github.com/cinience/skillhub/pkg/store"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	client *s3.Client
	bucket string
	prefix string
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
		client: client,
		bucket: cfg.Bucket,
		prefix: prefix,
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

	// Write version metadata
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

func (b *Backend) Archive(owner, slug, version string) (io.ReadCloser, error) {
	ctx := context.Background()
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

func (b *Backend) GetFile(owner, slug, version, path string) ([]byte, error) {
	ctx := context.Background()
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

func (b *Backend) ListVersions(owner, slug string) ([]string, error) {
	ctx := context.Background()
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

func (b *Backend) Exists(owner, slug string) bool {
	ctx := context.Background()
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

func (b *Backend) Delete(owner, slug string) error {
	return nil // TODO: implement S3 object deletion
}

func (b *Backend) Rename(owner, oldSlug, newSlug string) error {
	ctx := context.Background()
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
