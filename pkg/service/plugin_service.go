package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"regexp"
	"sort"
	"strings"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/repository"
	"github.com/cinience/skillhub/pkg/search"
	"github.com/cinience/skillhub/pkg/semver"
	"github.com/cinience/skillhub/pkg/store"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var pluginSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,126}[a-z0-9]$`)

type PluginService struct {
	db           *gorm.DB
	pluginRepo   *repository.PluginRepo
	fileStore    store.Store
	searchClient *search.Client
	auditSvc     *AuditService
	logger       *slog.Logger
}

func NewPluginService(db *gorm.DB, repo *repository.PluginRepo, fs store.Store, sc *search.Client, auditSvc *AuditService, logger *slog.Logger) *PluginService {
	return &PluginService{
		db:           db,
		pluginRepo:   repo,
		fileStore:    fs,
		searchClient: sc,
		auditSvc:     auditSvc,
		logger:       logger,
	}
}

type PluginPublishInput struct {
	Slug        string
	Version     string
	Category    string
	DisplayName string
	Summary     string
	Changelog   string
	Tags        []string
	Files       map[string][]byte
	OwnerID     uuid.UUID
}

type PluginPublishResult struct {
	Plugin  model.Plugin
	Version model.PluginVersion
}

func (s *PluginService) Publish(ctx context.Context, input PluginPublishInput) (*PluginPublishResult, error) {
	if !pluginSlugRe.MatchString(input.Slug) {
		return nil, fmt.Errorf("%w: invalid plugin slug %q", ErrValidation, input.Slug)
	}
	if !semverRe.MatchString(input.Version) {
		return nil, fmt.Errorf("%w: invalid semver %q", ErrValidation, input.Version)
	}

	// Validate plugin.json exists
	manifestData, ok := input.Files["plugin.json"]
	if !ok {
		return nil, fmt.Errorf("%w: plugin.json is required at the archive root", ErrValidation)
	}
	if err := validatePluginManifest(manifestData, input.Files); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}

	// Get or create plugin
	existing, err := s.pluginRepo.GetBySlug(ctx, input.Slug)
	if err != nil && !isNotFound(err) {
		return nil, fmt.Errorf("lookup plugin: %w", err)
	}

	var plug model.Plugin
	if existing != nil {
		if existing.OwnerID != input.OwnerID {
			return nil, fmt.Errorf("%w: not the plugin owner", ErrForbidden)
		}
		plug = *existing
	} else {
		plug = model.Plugin{
			ID:         uuid.New(),
			Slug:       input.Slug,
			OwnerID:    input.OwnerID,
			Visibility: "public",
			Category:   "general",
			Tags:       model.StringArray{},
		}
		if err := s.pluginRepo.Create(ctx, &plug); err != nil {
			return nil, fmt.Errorf("create plugin: %w", err)
		}
	}

	// Update metadata if provided
	if input.DisplayName != "" {
		plug.DisplayName = &input.DisplayName
	}
	if input.Summary != "" {
		plug.Summary = &input.Summary
	}
	if input.Category != "" {
		plug.Category = input.Category
	}
	if len(input.Tags) > 0 {
		plug.Tags = input.Tags
	}

	// Compute fingerprint
	fingerprint := computePluginFingerprint(input.Files)

	// Build files manifest
	filesManifest := buildFilesManifest(input.Files)
	filesJSON, _ := json.Marshal(filesManifest)

	// Store files via the standard Publish interface.
	// We use "_plugins_" as the owner namespace to separate from skill storage.
	_, storeErr := s.fileStore.Publish(ctx, store.PublishOpts{
		Owner:   "_plugins_",
		Slug:    input.Slug,
		Version: input.Version,
		Files:   input.Files,
		Author:  input.OwnerID.String(),
		Message: fmt.Sprintf("publish %s@%s", input.Slug, input.Version),
	})
	if storeErr != nil {
		return nil, fmt.Errorf("store plugin files: %w", storeErr)
	}

	// Create version record and update plugin atomically
	versionID := uuid.New()
	ver := model.PluginVersion{
		ID:          versionID,
		PluginID:    plug.ID,
		Version:     input.Version,
		Fingerprint: fingerprint,
		Manifest:    model.JSONRaw(manifestData),
		Files:       model.JSONRaw(filesJSON),
		CreatedBy:   input.OwnerID,
		SHA256Hash:  fingerprint,
	}
	if input.Changelog != "" {
		ver.Changelog = &input.Changelog
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var dupVer model.PluginVersion
		if tx.Where("plugin_id = ? AND version = ?", plug.ID, input.Version).First(&dupVer).Error == nil {
			return fmt.Errorf("%w: version %s already exists", ErrConflict, input.Version)
		}
		if existing != nil {
			var latest model.PluginVersion
			if tx.Where("plugin_id = ? AND soft_deleted_at IS NULL AND yanked_at IS NULL", plug.ID).
				Order("created_at DESC").First(&latest).Error == nil {
				if semver.Compare(input.Version, latest.Version) <= 0 {
					return fmt.Errorf("%w: version %s must be greater than %s", ErrValidation, input.Version, latest.Version)
				}
			}
		}
		if err := tx.Create(&ver).Error; err != nil {
			return fmt.Errorf("create version: %w", err)
		}
		if err := tx.Model(&model.Plugin{}).Where("id = ?", plug.ID).
			Updates(map[string]any{
				"latest_version_id": versionID,
				"versions_count":    gorm.Expr("versions_count + 1"),
			}).Error; err != nil {
			return fmt.Errorf("set latest version: %w", err)
		}
		updates := map[string]any{}
		if input.DisplayName != "" {
			updates["display_name"] = input.DisplayName
		}
		if input.Summary != "" {
			updates["summary"] = input.Summary
		}
		if input.Category != "" {
			updates["category"] = input.Category
		}
		if len(input.Tags) > 0 {
			updates["tags"] = model.StringArray(input.Tags)
		}
		if len(updates) > 0 {
			if err := tx.Model(&model.Plugin{}).Where("id = ?", plug.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("update plugin metadata: %w", err)
			}
		}
		return nil
	}); err != nil {
		if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrValidation) {
			s.logger.Warn("publish transaction failed, stored files may be orphaned",
				"slug", input.Slug, "version", input.Version, "err", err)
		}
		return nil, err
	}
	plug.LatestVersionID = &versionID

	s.logger.Info("plugin published",
		"slug", input.Slug, "version", input.Version, "owner", input.OwnerID)

	if s.searchClient != nil {
		ownerHandle := input.OwnerID.String()
		if p, _ := s.pluginRepo.GetWithOwner(ctx, plug.Slug); p != nil {
			ownerHandle = p.OwnerHandle
		}
		doc := &search.PluginDocument{
			ID:               plug.ID.String(),
			Slug:             plug.Slug,
			DisplayName:      derefStrPtr(plug.DisplayName),
			Summary:          derefStrPtr(plug.Summary),
			DocType:          "plugin",
			Category:         plug.Category,
			Tags:             []string(plug.Tags),
			OwnerHandle:      ownerHandle,
			Visibility:       plug.Visibility,
			ModerationStatus: plug.ModerationStatus,
			IsDeleted:        false,
			Downloads:        plug.Downloads,
			Stars:            plug.StarsCount,
			UpdatedAt:        plug.UpdatedAt.Unix(),
			CreatedAt:        plug.CreatedAt.Unix(),
		}
		if err := s.searchClient.IndexPlugin(ctx, doc); err != nil {
			s.logger.Warn("failed to index plugin", "slug", input.Slug, "err", err)
		}
	}

	return &PluginPublishResult{Plugin: plug, Version: ver}, nil
}

func (s *PluginService) Get(ctx context.Context, slug string) (*model.PluginWithOwner, error) {
	p, err := s.pluginRepo.GetWithOwner(ctx, slug)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: plugin %s", ErrNotFound, slug)
		}
		return nil, fmt.Errorf("get plugin: %w", err)
	}
	return p, nil
}

func (s *PluginService) List(ctx context.Context, opts repository.PluginListOptions) ([]model.PluginWithOwner, string, error) {
	return s.pluginRepo.List(ctx, opts)
}

func (s *PluginService) Versions(ctx context.Context, slug string) ([]model.PluginVersion, error) {
	p, err := s.pluginRepo.GetBySlug(ctx, slug)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: plugin %s", ErrNotFound, slug)
		}
		return nil, fmt.Errorf("get plugin: %w", err)
	}
	return s.pluginRepo.ListVersions(ctx, p.ID)
}

func (s *PluginService) GetFile(ctx context.Context, slug, version, filePath string) ([]byte, error) {
	filePath = store.SanitizeStorePath(filePath)
	if filePath == "invalid" {
		return nil, fmt.Errorf("%w: invalid file path", ErrValidation)
	}

	p, err := s.pluginRepo.GetBySlug(ctx, slug)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: plugin %s", ErrNotFound, slug)
		}
		return nil, fmt.Errorf("get plugin: %w", err)
	}

	if version == "" || version == "latest" {
		v, err := s.pluginRepo.GetLatestVersion(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: no versions found", ErrNotFound)
		}
		version = v.Version
	}

	data, err := s.fileStore.GetFile("_plugins_", slug, version, filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: file not found", ErrNotFound)
	}
	return data, nil
}

func (s *PluginService) Download(ctx context.Context, slug, version string) (io.ReadCloser, string, error) {
	p, err := s.pluginRepo.GetBySlug(ctx, slug)
	if err != nil {
		if isNotFound(err) {
			return nil, "", fmt.Errorf("%w: plugin %s", ErrNotFound, slug)
		}
		return nil, "", fmt.Errorf("get plugin: %w", err)
	}

	if version == "" || version == "latest" {
		v, err := s.pluginRepo.GetLatestVersion(ctx, p.ID)
		if err != nil {
			return nil, "", fmt.Errorf("%w: no versions found", ErrNotFound)
		}
		version = v.Version
	} else {
		ver, err := s.pluginRepo.GetVersion(ctx, p.ID, version)
		if err != nil {
			return nil, "", fmt.Errorf("%w: version %s not found", ErrNotFound, version)
		}
		if ver.YankedAt != nil {
			return nil, "", fmt.Errorf("%w: version %s is yanked", ErrValidation, version)
		}
	}

	reader, err := s.fileStore.Archive("_plugins_", slug, version)
	if err != nil {
		return nil, "", fmt.Errorf("build archive: %w", err)
	}

	_ = s.pluginRepo.IncrementDownloads(ctx, p.ID)

	ver, _ := s.pluginRepo.GetVersion(ctx, p.ID, version)
	etag := ""
	if ver != nil {
		etag = ver.Fingerprint
	}
	return reader, etag, nil
}

// ParseMultipartPublish extracts a PluginPublishInput from a multipart form.
func (s *PluginService) ParseMultipartPublish(form *multipart.Form) (*PluginPublishInput, error) {
	input := &PluginPublishInput{}

	if v := formValue(form, "slug"); v != "" {
		input.Slug = v
	} else {
		return nil, fmt.Errorf("%w: slug is required", ErrValidation)
	}
	if v := formValue(form, "version"); v != "" {
		input.Version = v
	} else {
		return nil, fmt.Errorf("%w: version is required", ErrValidation)
	}

	input.Category = formValue(form, "category")
	input.DisplayName = formValue(form, "displayName")
	input.Summary = formValue(form, "summary")
	input.Changelog = formValue(form, "changelog")
	if tags := formValue(form, "tags"); tags != "" {
		input.Tags = strings.Split(tags, ",")
	}

	files, err := ReadMultipartFiles(form)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	input.Files = files

	return input, nil
}

func formValue(form *multipart.Form, key string) string {
	if vals, ok := form.Value[key]; ok && len(vals) > 0 {
		return strings.TrimSpace(vals[0])
	}
	return ""
}

func validatePluginManifest(data []byte, files map[string][]byte) error {
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Skills  struct {
			Path    string   `json:"path"`
			Entries []string `json:"entries"`
		} `json:"skills"`
		MCPServers map[string]struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"mcp_servers"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid plugin.json: %v", err)
	}
	if manifest.Name == "" {
		return fmt.Errorf("plugin.json: name is required")
	}
	if manifest.Version == "" {
		return fmt.Errorf("plugin.json: version is required")
	}

	// Verify skill entries exist
	skillBase := "skills"
	if manifest.Skills.Path != "" {
		skillBase = strings.TrimSuffix(manifest.Skills.Path, "/")
	}
	for _, entry := range manifest.Skills.Entries {
		skillPath := skillBase + "/" + entry + "/SKILL.md"
		if _, ok := files[skillPath]; !ok {
			// Also try with ./ prefix
			if _, ok := files["./"+skillPath]; !ok {
				return fmt.Errorf("skill entry %q references missing %s", entry, skillPath)
			}
		}
	}

	return nil
}

func computePluginFingerprint(files map[string][]byte) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(files[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type fileManifestEntry struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

func buildFilesManifest(files map[string][]byte) []fileManifestEntry {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]fileManifestEntry, 0, len(keys))
	for _, k := range keys {
		h := sha256.Sum256(files[k])
		entries = append(entries, fileManifestEntry{
			Path:   k,
			Size:   len(files[k]),
			SHA256: hex.EncodeToString(h[:]),
		})
	}
	return entries
}

func derefStrPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
