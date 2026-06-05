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

	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"
	"github.com/saker-ai/skillhub/pkg/search"
	"github.com/saker-ai/skillhub/pkg/semver"
	"github.com/saker-ai/skillhub/pkg/store"
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
	nsSvc        *NamespaceService
}

// SetNamespaceService injects namespace support for plugin publishing.
func (s *PluginService) SetNamespaceService(ns *NamespaceService) {
	s.nsSvc = ns
}

func (s *PluginService) loggerOrDefault() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
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
	Slug           string
	Version        string
	Category       string
	DisplayName    string
	Summary        string
	Changelog      string
	Tags           []string
	Files          map[string][]byte
	OwnerID        uuid.UUID
	NamespaceSlug  string
	User           *model.User
	TokenNamespace *uuid.UUID
}

type PluginPublishResult struct {
	Plugin  model.Plugin
	Version model.PluginVersion
}

func pluginStorageSlug(namespace, slug string) string {
	if namespace == "" {
		return slug
	}
	return namespace + "__" + slug
}

func parsePluginRef(ref string) model.SkillRef {
	if strings.HasPrefix(ref, "@") {
		if idx := strings.IndexByte(ref[1:], '/'); idx > 0 {
			return model.SkillRef{Namespace: ref[1 : idx+1], Slug: ref[idx+2:]}
		}
	}
	return model.SkillRef{Slug: ref}
}

func (s *PluginService) resolvePluginRef(ctx context.Context, ref string, includeDeleted bool) (*model.PluginWithOwner, error) {
	parsed := parsePluginRef(ref)
	if parsed.IsQualified() {
		if s.nsSvc == nil {
			return nil, nil
		}
		ns, err := s.nsSvc.GetBySlug(ctx, parsed.Namespace)
		if err != nil || ns == nil {
			return nil, nil
		}
		if includeDeleted {
			return s.pluginRepo.GetByNSAndSlugIncludeDeleted(ctx, ns.ID, parsed.Slug)
		}
		return s.pluginRepo.GetByNSAndSlug(ctx, ns.ID, parsed.Slug)
	}

	var all []model.PluginWithOwner
	var err error
	if includeDeleted {
		all, err = s.pluginRepo.GetBySlugGlobalIncludeDeleted(ctx, parsed.Slug)
	} else {
		all, err = s.pluginRepo.GetBySlugGlobal(ctx, parsed.Slug)
	}
	if err != nil {
		return nil, err
	}
	switch len(all) {
	case 0:
		return nil, nil
	case 1:
		return &all[0], nil
	default:
		candidates := make([]AmbiguousCandidate, 0, len(all))
		for i := range all {
			candidates = append(candidates, AmbiguousCandidate{
				Namespace:   all[i].NamespaceSlug,
				Slug:        all[i].Slug,
				OwnerHandle: all[i].OwnerHandle,
				SkillID:     all[i].ID.String(),
			})
		}
		return nil, &AmbiguousSlugError{Slug: parsed.Slug, Candidates: candidates}
	}
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

	// Resolve namespace (same pattern as skills).
	var nsID *uuid.UUID
	if s.nsSvc != nil {
		if input.NamespaceSlug != "" {
			ns, err := s.nsSvc.GetBySlug(ctx, input.NamespaceSlug)
			if err != nil || ns == nil {
				return nil, fmt.Errorf("namespace '%s' not found", input.NamespaceSlug)
			}
			if input.User != nil {
				can, err := s.nsSvc.CanPublish(ctx, input.NamespaceSlug, input.User.ID)
				if err != nil {
					return nil, fmt.Errorf("check namespace membership: %w", err)
				}
				if !can && !input.User.IsAdmin() {
					return nil, fmt.Errorf("%w: not a member of namespace '%s'", ErrForbidden, input.NamespaceSlug)
				}
			}
			nsID = &ns.ID
		} else if input.User != nil {
			ns, err := s.nsSvc.EnsurePersonalNamespace(ctx, input.User)
			if err != nil {
				return nil, fmt.Errorf("ensure personal namespace: %w", err)
			}
			nsID = &ns.ID
			input.NamespaceSlug = ns.Slug
		}
	}
	if input.TokenNamespace != nil {
		if nsID == nil || *nsID != *input.TokenNamespace {
			return nil, fmt.Errorf("%w: team token can only publish to its bound namespace", ErrForbidden)
		}
	}

	// Get or create plugin (scoped to namespace if available)
	var existing *model.Plugin
	if nsID != nil {
		p, err := s.pluginRepo.GetByNSAndSlug(ctx, *nsID, input.Slug)
		if err != nil {
			return nil, fmt.Errorf("lookup plugin: %w", err)
		}
		if p != nil {
			existing = &p.Plugin
		}
	} else {
		p, err := s.pluginRepo.GetBySlug(ctx, input.Slug)
		if err != nil && !isNotFound(err) {
			return nil, fmt.Errorf("lookup plugin: %w", err)
		}
		existing = p
	}

	var plug model.Plugin
	if existing != nil {
		if existing.OwnerID != input.OwnerID {
			return nil, fmt.Errorf("%w: not the plugin owner", ErrForbidden)
		}
		plug = *existing
	} else {
		plug = model.Plugin{
			ID:          uuid.New(),
			Slug:        input.Slug,
			OwnerID:     input.OwnerID,
			NamespaceID: nsID,
			Visibility:  "public",
			Category:    "general",
			Tags:        model.StringArray{},
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
	fingerprint := computeFingerprint(input.Files)

	// Build files manifest
	filesManifest := buildFilesManifest(input.Files)
	filesJSON, _ := json.Marshal(filesManifest)

	// Store files via the standard Publish interface.
	// We use "_plugins_" as the owner namespace to separate from skill storage.
	storageSlug := pluginStorageSlug(input.NamespaceSlug, input.Slug)
	_, storeErr := s.fileStore.Publish(ctx, store.PublishOpts{
		Owner:   "_plugins_",
		Slug:    storageSlug,
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
			if cleanErr := s.fileStore.DeleteVersion(ctx, "_plugins_", storageSlug, input.Version); cleanErr != nil {
				s.loggerOrDefault().Warn("failed to clean orphaned plugin files",
					"slug", input.Slug, "version", input.Version, "err", cleanErr)
			}
		}
		return nil, err
	}
	plug.LatestVersionID = &versionID

	s.loggerOrDefault().Info("plugin published",
		"slug", input.Slug, "version", input.Version, "owner", input.OwnerID)

	if s.searchClient != nil {
		ownerHandle := input.OwnerID.String()
		if p, _ := s.pluginRepo.GetWithOwner(ctx, plug.Slug); p != nil {
			ownerHandle = p.OwnerHandle
		}
		doc := &search.PluginDocument{
			ID:               plug.ID.String(),
			Slug:             plug.Slug,
			DisplayName:      derefStr(plug.DisplayName),
			Summary:          derefStr(plug.Summary),
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
			s.loggerOrDefault().Warn("failed to index plugin", "slug", input.Slug, "err", err)
		}
	}

	return &PluginPublishResult{Plugin: plug, Version: ver}, nil
}

func (s *PluginService) Get(ctx context.Context, ref string) (*model.PluginWithOwner, error) {
	p, err := s.resolvePluginRef(ctx, ref, false)
	if err != nil {
		return nil, fmt.Errorf("get plugin: %w", err)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: plugin %s", ErrNotFound, ref)
	}
	return p, nil
}

func (s *PluginService) List(ctx context.Context, opts repository.PluginListOptions) ([]model.PluginWithOwner, string, error) {
	return s.pluginRepo.List(ctx, opts)
}

func (s *PluginService) Versions(ctx context.Context, ref string) ([]model.PluginVersion, error) {
	p, err := s.resolvePluginRef(ctx, ref, false)
	if err != nil {
		return nil, fmt.Errorf("get plugin: %w", err)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: plugin %s", ErrNotFound, ref)
	}
	return s.pluginRepo.ListVersions(ctx, p.ID)
}

func (s *PluginService) GetFile(ctx context.Context, ref, version, filePath string) ([]byte, error) {
	filePath = store.SanitizeStorePath(filePath)
	if filePath == "invalid" {
		return nil, fmt.Errorf("%w: invalid file path", ErrValidation)
	}

	p, err := s.resolvePluginRef(ctx, ref, false)
	if err != nil {
		return nil, fmt.Errorf("get plugin: %w", err)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: plugin %s", ErrNotFound, ref)
	}

	if version == "" || version == "latest" {
		v, err := s.pluginRepo.GetLatestVersion(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: no versions found", ErrNotFound)
		}
		version = v.Version
	}

	data, err := s.fileStore.GetFile(ctx, "_plugins_", pluginStorageSlug(p.NamespaceSlug, p.Slug), version, filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: file not found", ErrNotFound)
	}
	return data, nil
}

func (s *PluginService) Download(ctx context.Context, ref, version string) (io.ReadCloser, string, error) {
	p, err := s.resolvePluginRef(ctx, ref, false)
	if err != nil {
		return nil, "", fmt.Errorf("get plugin: %w", err)
	}
	if p == nil {
		return nil, "", fmt.Errorf("%w: plugin %s", ErrNotFound, ref)
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

	reader, err := s.fileStore.Archive(ctx, "_plugins_", pluginStorageSlug(p.NamespaceSlug, p.Slug), version)
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
