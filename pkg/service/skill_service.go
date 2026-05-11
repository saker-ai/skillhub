package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/cinience/skillhub/pkg/gitstore"
	"github.com/cinience/skillhub/pkg/metrics"
	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/repository"
	"github.com/cinience/skillhub/pkg/search"
	"github.com/cinience/skillhub/pkg/security"
	"github.com/cinience/skillhub/pkg/semver"
	"github.com/cinience/skillhub/pkg/store"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[\w.]+)?(\+[\w.]+)?$`)

type SkillService struct {
	db           *gorm.DB
	skillRepo    *repository.SkillRepo
	versionRepo  *repository.VersionRepo
	userRepo     *repository.UserRepo
	downloadRepo *repository.DownloadRepo
	starRepo     *repository.StarRepo
	fileStore    store.Store
	searchClient *search.Client
	mirrorSvc    *gitstore.MirrorService
	auditSvc     *AuditService
	nsSvc        *NamespaceService
	sigVerifier  security.SignatureVerifier
	// metrics 由嵌入方通过 SetMetrics 注入；nil 时走 metrics.Default 单例。
	// 阶段 2 改造：避免直接读包级全局变量，便于宿主进程隔离指标命名空间。
	metrics *metrics.Metrics
	// logger 由 SetLogger 注入；nil 时回退到 slog.Default()。
	// 阶段 4 改造：与 metrics 同样走 setter 注入，让宿主进程的结构化日志管线
	// 能拿到 service 层的事件，而不是走包级 slog.Default() 绕过去。
	logger *slog.Logger
}

// SetMetrics 注入 *metrics.Metrics 实例。nil 等价于走 metrics.Default。
// 必须在 Server 装配阶段调用（即 New 之后、对外开放之前），运行期切换不被支持。
func (s *SkillService) SetMetrics(m *metrics.Metrics) {
	s.metrics = m
}

// metricsOrDefault 返回当前注入的 metrics 实例；未注入时回退到 Default 单例。
func (s *SkillService) metricsOrDefault() *metrics.Metrics {
	if s.metrics != nil {
		return s.metrics
	}
	return metrics.Default
}

// SetLogger 注入 *slog.Logger 实例。nil 等价于走 slog.Default()。
// 与 SetMetrics 同样必须在 Server 装配阶段调用。
func (s *SkillService) SetLogger(lg *slog.Logger) {
	s.logger = lg
}

// loggerOrDefault 返回当前注入的 logger；未注入时回退到 slog.Default()。
func (s *SkillService) loggerOrDefault() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// SetNamespaceService injects the namespace service for membership/publish checks.
// Optional — when nil, namespace-bound publishing is disabled.
func (s *SkillService) SetNamespaceService(ns *NamespaceService) {
	s.nsSvc = ns
}

// SetSignatureVerifier injects a Sigstore-compatible verifier for publish-time
// attestation checks. When nil, uploaded bundles are stored as "unverified".
func (s *SkillService) SetSignatureVerifier(v security.SignatureVerifier) {
	s.sigVerifier = v
}

func NewSkillService(
	db *gorm.DB,
	skillRepo *repository.SkillRepo,
	versionRepo *repository.VersionRepo,
	userRepo *repository.UserRepo,
	downloadRepo *repository.DownloadRepo,
	starRepo *repository.StarRepo,
	fs store.Store,
	sc *search.Client,
	ms *gitstore.MirrorService,
	auditSvc *AuditService,
) *SkillService {
	return &SkillService{
		db:           db,
		skillRepo:    skillRepo,
		versionRepo:  versionRepo,
		userRepo:     userRepo,
		downloadRepo: downloadRepo,
		starRepo:     starRepo,
		fileStore:    fs,
		searchClient: sc,
		mirrorSvc:    ms,
		auditSvc:     auditSvc,
	}
}

type PublishRequest struct {
	Slug          string
	Version       string
	Changelog     string
	DisplayName   string
	Summary       string
	Category      string
	Kind          string
	Tags          []string
	Visibility    string // "" | "private" | "public" — only honored on first create
	NamespaceSlug string // optional team namespace
	Files         map[string][]byte // path → content
	Dependencies  []model.SkillDependency // declared upstream skill deps
	SignatureBundle []byte                // optional sigstore .sigstore JSON

	// TokenNamespace 由 handler 透传当前请求 token 绑定的 namespace ID(*middleware.GetTokenNamespace*)。
	// 非 nil ⇒ 团队 token,要求目标 skill 隶属该 namespace；不一致直接 403。
	// nil ⇒ 个人 token / cookie 会话,沿用旧的 owner-ID 鉴权路径。
	TokenNamespace *uuid.UUID
}

// authorizeSkillWrite 判定 caller 是否可对 skill 做写操作。
//
// 优先看 tokenNS：团队 token 在签发时已校验过 owner/admin 角色,所以这里跳过
// skill.OwnerID 检查,只要求目标 skill 隶属同一个 namespace。
//
// tokenNS == nil(个人 token / cookie 会话)走旧路径：必须本人 owner 或系统 admin。
func (s *SkillService) authorizeSkillWrite(skillNS *uuid.UUID, ownerID uuid.UUID, user *model.User, tokenNS *uuid.UUID) error {
	if tokenNS != nil {
		if skillNS == nil || *skillNS != *tokenNS {
			return fmt.Errorf("%w: team token cannot operate on skills outside its namespace", ErrForbidden)
		}
		return nil
	}
	if ownerID != user.ID && !user.IsAdmin() {
		return fmt.Errorf("%w: only the skill owner or a system admin can perform this action", ErrForbidden)
	}
	return nil
}

func (s *SkillService) PublishVersion(ctx context.Context, user *model.User, req PublishRequest) (*model.SkillWithOwner, *model.SkillVersion, error) {
	// Validate slug
	if req.Slug == "" {
		return nil, nil, fmt.Errorf("slug is required")
	}
	reserved, err := s.skillRepo.IsSlugReserved(ctx, req.Slug)
	if err != nil {
		return nil, nil, fmt.Errorf("check reserved slug: %w", err)
	}
	if reserved {
		return nil, nil, fmt.Errorf("slug '%s' is reserved", req.Slug)
	}
	// Enforce reserved namespaces — animus-builtin/*, animus-domain/* are admin-only.
	if model.IsReservedNamespace(req.Slug) && !user.IsAdmin() {
		return nil, nil, fmt.Errorf("slug '%s' falls in a reserved namespace (admin only)", req.Slug)
	}
	// Validate kind if provided (default: custom)
	if req.Kind == "" {
		req.Kind = "custom"
	}
	if !model.IsValidKind(req.Kind) {
		return nil, nil, fmt.Errorf("invalid kind '%s'", req.Kind)
	}
	// Only admins may publish builtin/domain kinds.
	if (req.Kind == "builtin" || req.Kind == "domain") && !user.IsAdmin() {
		return nil, nil, fmt.Errorf("kind '%s' is admin-only", req.Kind)
	}

	// Validate semver
	if !semverRe.MatchString(req.Version) {
		return nil, nil, fmt.Errorf("invalid version '%s': must be valid semver (e.g. 1.0.0)", req.Version)
	}
	version := req.Version

	// Validate declared dependencies: slug must exist, no self-dependency,
	// version range non-empty. Range syntax is stored verbatim and resolved
	// client-side at install time.
	for _, dep := range req.Dependencies {
		if dep.Slug == "" {
			return nil, nil, fmt.Errorf("dependency slug is required")
		}
		if dep.Slug == req.Slug {
			return nil, nil, fmt.Errorf("dependency '%s' cannot depend on itself", dep.Slug)
		}
		if dep.Version == "" {
			return nil, nil, fmt.Errorf("dependency '%s' requires a version range", dep.Slug)
		}
		depSkill, err := s.skillRepo.GetBySlugOrAlias(ctx, dep.Slug)
		if err != nil {
			return nil, nil, fmt.Errorf("lookup dependency '%s': %w", dep.Slug, err)
		}
		if depSkill == nil {
			return nil, nil, fmt.Errorf("dependency '%s' not found", dep.Slug)
		}
	}

	// Resolve namespace if requested. Membership is required to publish.
	var nsID *uuid.UUID
	if req.NamespaceSlug != "" {
		if s.nsSvc == nil {
			return nil, nil, fmt.Errorf("namespace publishing not configured on this server")
		}
		ns, err := s.nsSvc.GetBySlug(ctx, req.NamespaceSlug)
		if err != nil || ns == nil {
			return nil, nil, fmt.Errorf("namespace '%s' not found", req.NamespaceSlug)
		}
		can, err := s.nsSvc.CanPublish(ctx, req.NamespaceSlug, user.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("check namespace membership: %w", err)
		}
		if !can && !user.IsAdmin() {
			return nil, nil, fmt.Errorf("not a member of namespace '%s'", req.NamespaceSlug)
		}
		nsID = &ns.ID
	}

	// 团队 token 必须指向自己 namespace,不允许借此发个人 skill 或别 namespace 的 skill。
	// 这里只校验 *新 skill* 的 namespace 选择是否合法；下面 existing-skill 分支
	// 还会再用 authorizeSkillWrite 把已存在的 skill 也校验一次。
	if req.TokenNamespace != nil {
		if nsID == nil || *nsID != *req.TokenNamespace {
			return nil, nil, fmt.Errorf("%w: team token can only publish to its bound namespace", ErrForbidden)
		}
	}

	// Validate visibility (only public/private allowed; defaults to private on create)
	visibility := "private"
	if req.Visibility != "" {
		if req.Visibility != "private" && req.Visibility != "public" {
			return nil, nil, fmt.Errorf("visibility must be 'private' or 'public'")
		}
		visibility = req.Visibility
	}

	// Find or create skill
	skill, err := s.skillRepo.GetBySlug(ctx, req.Slug)
	if err != nil {
		return nil, nil, fmt.Errorf("get skill: %w", err)
	}

	if skill == nil {
		// Create new skill (default private)
		category := req.Category
		if category == "" {
			category = "general"
		}
		if !model.IsValidCategory(category) {
			return nil, nil, fmt.Errorf("invalid category '%s'", category)
		}
		// Auto-derive kind from reserved namespace prefix if caller didn't override.
		kind := req.Kind
		if kind == "" || kind == "custom" {
			switch {
			case strings.HasPrefix(req.Slug, "animus-builtin/"):
				kind = "builtin"
			case strings.HasPrefix(req.Slug, "animus-domain/"):
				kind = "domain"
			default:
				kind = req.Kind
			}
		}
		// Public skills always start in pending_review state — only admin/moderator promotes.
		moderation := "approved"
		effectiveVisibility := visibility
		if visibility == "public" && !user.IsAdmin() && !user.IsModerator() {
			effectiveVisibility = "private"
			moderation = "pending_review"
		}
		newSkill := &model.Skill{
			ID:               uuid.New(),
			Slug:             req.Slug,
			OwnerID:          user.ID,
			NamespaceID:      nsID,
			Category:         category,
			Tags:             model.StringArray(req.Tags),
			Visibility:       effectiveVisibility,
			ModerationStatus: moderation,
			Kind:             kind,
		}
		if req.DisplayName != "" {
			newSkill.DisplayName = &req.DisplayName
		}
		if req.Summary != "" {
			newSkill.Summary = &req.Summary
		}
		if err := s.skillRepo.Create(ctx, newSkill); err != nil {
			return nil, nil, fmt.Errorf("create skill: %w", err)
		}
		skill = &model.SkillWithOwner{
			Skill:       *newSkill,
			OwnerHandle: user.Handle,
		}
	} else {
		// Verify ownership / namespace token scoping.
		if err := s.authorizeSkillWrite(skill.NamespaceID, skill.OwnerID, user, req.TokenNamespace); err != nil {
			return nil, nil, err
		}
	}

	// Check version doesn't exist
	existing, err := s.versionRepo.GetBySkillAndVersion(ctx, skill.ID, version)
	if err != nil {
		return nil, nil, err
	}
	if existing != nil {
		return nil, nil, fmt.Errorf("version %s already exists", version)
	}

	// Check version is greater than latest
	latest, _ := s.versionRepo.GetLatest(ctx, skill.ID)
	if latest != nil {
		if semver.Compare(version, latest.Version) <= 0 {
			return nil, nil, fmt.Errorf("version %s must be greater than current latest %s", version, latest.Version)
		}
	}

	// Validate and compute file metadata and fingerprint
	var filesMeta []model.VersionFile
	var hashParts []string
	for path, content := range req.Files {
		cleanPath := sanitizeFilePath(path)
		if cleanPath == "" {
			return nil, nil, fmt.Errorf("invalid file path: %s", path)
		}
		if cleanPath != path {
			req.Files[cleanPath] = content
			delete(req.Files, path)
			path = cleanPath
		}
		h := sha256.Sum256(content)
		fileHash := hex.EncodeToString(h[:])
		filesMeta = append(filesMeta, model.VersionFile{
			Path:   path,
			Size:   int64(len(content)),
			SHA256: fileHash,
		})
		hashParts = append(hashParts, fileHash)
	}
	sort.Strings(hashParts)
	aggregateHash := sha256.Sum256([]byte(strings.Join(hashParts, ":")))
	fingerprint := hex.EncodeToString(aggregateHash[:])

	filesJSON, _ := json.Marshal(filesMeta)

	// Extract parsed info from SKILL.md if present
	parsedJSON := json.RawMessage("{}")
	if content, ok := req.Files["SKILL.md"]; ok {
		parsedJSON = extractFrontmatter(content)
	}

	// Publish to git
	email := user.Handle + "@skillhub.local"
	commitHash, err := s.fileStore.Publish(ctx, store.PublishOpts{
		Owner:   user.Handle,
		Slug:    req.Slug,
		Version: version,
		Files:   req.Files,
		Author:  user.Handle,
		Email:   email,
		Message: req.Changelog,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("git publish: %w", err)
	}

	// Serialize dependencies (default to empty array)
	depsJSON := json.RawMessage("[]")
	if len(req.Dependencies) > 0 {
		if encoded, err := json.Marshal(req.Dependencies); err == nil {
			depsJSON = encoded
		}
	}

	// Verify signature bundle if provided. Reject only on hard "invalid";
	// "unverified" (no verifier configured) is recorded but not rejected.
	signatureStatus := "unsigned"
	var signatureBundlePtr, signatureSubjectPtr, signatureIssuerPtr *string
	if len(req.SignatureBundle) > 0 {
		verifier := s.sigVerifier
		if verifier == nil {
			verifier = security.NopVerifier{}
		}
		result, err := verifier.Verify(ctx, fingerprint, req.SignatureBundle)
		if err != nil {
			return nil, nil, fmt.Errorf("signature verification failed: %w", err)
		}
		if result.Status == "invalid" {
			return nil, nil, fmt.Errorf("invalid signature: %s", result.Reason)
		}
		bundleStr := string(req.SignatureBundle)
		signatureBundlePtr = &bundleStr
		signatureStatus = result.Status
		if result.Subject != "" {
			subj := result.Subject
			signatureSubjectPtr = &subj
		}
		if result.Issuer != "" {
			iss := result.Issuer
			signatureIssuerPtr = &iss
		}
	}

	// Create version record
	ver := &model.SkillVersion{
		ID:               uuid.New(),
		SkillID:          skill.ID,
		Version:          version,
		Fingerprint:      fingerprint,
		GitCommitHash:    &commitHash,
		Files:            filesJSON,
		Parsed:           parsedJSON,
		CreatedBy:        user.ID,
		SHA256Hash:       fingerprint,
		Dependencies:     depsJSON,
		SignatureBundle:  signatureBundlePtr,
		SignatureStatus:  signatureStatus,
		SignatureSubject: signatureSubjectPtr,
		SignatureIssuer:  signatureIssuerPtr,
	}
	if req.Changelog != "" {
		ver.Changelog = &req.Changelog
	}

	// Wrap DB writes in a transaction for atomicity
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ver).Error; err != nil {
			return fmt.Errorf("create version: %w", err)
		}
		if err := tx.Model(&model.Skill{}).Where("id = ?", skill.ID).
			Updates(map[string]interface{}{
				"latest_version_id": ver.ID,
				"versions_count":    gorm.Expr("versions_count + 1"),
				"updated_at":        ver.CreatedAt,
			}).Error; err != nil {
			return fmt.Errorf("update latest version: %w", err)
		}
		// Update metadata if provided
		if req.DisplayName != "" || req.Summary != "" || req.Category != "" || len(req.Tags) > 0 {
			updates := map[string]interface{}{}
			if req.DisplayName != "" {
				skill.DisplayName = &req.DisplayName
				updates["display_name"] = req.DisplayName
			}
			if req.Summary != "" {
				skill.Summary = &req.Summary
				updates["summary"] = req.Summary
			}
			if req.Category != "" && model.IsValidCategory(req.Category) {
				skill.Category = req.Category
				updates["category"] = req.Category
			}
			if len(req.Tags) > 0 {
				skill.Tags = model.StringArray(req.Tags)
				updates["tags"] = skill.Tags
			}
			if len(updates) > 0 {
				if err := tx.Model(&model.Skill{}).Where("id = ?", skill.ID).Updates(updates).Error; err != nil {
					return fmt.Errorf("update skill metadata: %w", err)
				}
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}

	// Index to search
	if s.searchClient != nil {
		skillMdContent := ""
		if content, ok := req.Files["SKILL.md"]; ok {
			skillMdContent = string(content)
		}
		doc := &search.SkillDocument{
			ID:               skill.ID.String(),
			Slug:             skill.Slug,
			DisplayName:      derefStr(skill.DisplayName),
			Summary:          derefStr(skill.Summary),
			SkillMdContent:   skillMdContent,
			Category:         skill.Category,
			Tags:             []string(skill.Tags),
			OwnerHandle:      skill.OwnerHandle,
			Visibility:       skill.Visibility,
			ModerationStatus: skill.ModerationStatus,
			IsSuspicious:     skill.IsSuspicious,
			IsDeleted:        skill.SoftDeletedAt != nil,
			Downloads:        skill.Downloads,
			Stars:            skill.StarsCount,
			UpdatedAt:        skill.UpdatedAt.Unix(),
			CreatedAt:        skill.CreatedAt.Unix(),
		}
		if err := s.searchClient.IndexSkill(ctx, doc); err != nil {
			s.loggerOrDefault().Warn("failed to index skill to search", "err", err)
		}
	}

	// Mirror push (async)
	if s.mirrorSvc != nil && s.mirrorSvc.Enabled() {
		// Snapshot the logger outside the goroutine so the closure doesn't
		// race with a (hypothetical) future SetLogger call mid-flight.
		lg := s.loggerOrDefault()
		go func() {
			if err := s.mirrorSvc.PushMirror(context.Background(), user.Handle, req.Slug); err != nil {
				lg.Warn("mirror push failed", "owner", user.Handle, "slug", req.Slug, "err", err)
			}
		}()
	}

	// Write audit log
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "publish", "skill_version", &ver.ID, "", "")
	}

	s.metricsOrDefault().SkillPublished.WithLabelValues(skill.Visibility).Inc()

	return skill, ver, nil
}

// DownloadResult carries a zip archive plus metadata for ETag / filename.
type DownloadResult struct {
	Archive     io.ReadCloser
	Filename    string
	Fingerprint string
	Version     string
}

// ResolveVersion returns the version record for a slug+version (accepts "latest").
// It enforces visibility. Returned SkillVersion is non-nil on success.
func (s *SkillService) ResolveVersion(ctx context.Context, slug, version string, viewer *model.User) (*model.SkillWithOwner, *model.SkillVersion, error) {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	if skill == nil || !canViewSkill(skill, viewer) {
		return nil, nil, fmt.Errorf("skill not found: %s", slug)
	}

	if version == "" || version == "latest" {
		if skill.LatestVersionID == nil {
			return skill, nil, fmt.Errorf("no versions published")
		}
		v, err := s.versionRepo.GetByID(ctx, *skill.LatestVersionID)
		if err != nil {
			return skill, nil, err
		}
		if v == nil {
			return skill, nil, fmt.Errorf("latest version not found")
		}
		return skill, v, nil
	}

	ver, err := s.versionRepo.GetBySkillAndVersion(ctx, skill.ID, version)
	if err != nil {
		return skill, nil, err
	}
	if ver == nil {
		return skill, nil, fmt.Errorf("version %s not found", version)
	}
	return skill, ver, nil
}

// Download returns a zip archive for a skill version along with its fingerprint.
func (s *SkillService) Download(ctx context.Context, slug, version string, identityHash string, viewer *model.User) (*DownloadResult, error) {
	skill, ver, err := s.ResolveVersion(ctx, slug, version, viewer)
	if err != nil {
		return nil, err
	}

	archive, err := s.fileStore.Archive(skill.OwnerHandle, slug, ver.Version)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}

	// Record download (deduplicate) — best-effort, not fatal.
	if identityHash != "" {
		isNew, _ := s.downloadRepo.RecordDownload(ctx, skill.ID, ver.ID, identityHash)
		if isNew {
			s.skillRepo.IncrementDownloads(ctx, skill.ID)
		}
	}

	s.metricsOrDefault().SkillDownloads.Inc()

	return &DownloadResult{
		Archive:     archive,
		Filename:    fmt.Sprintf("%s-%s.zip", slug, ver.Version),
		Fingerprint: ver.Fingerprint,
		Version:     ver.Version,
	}, nil
}

// GetFile reads a single file from a skill version.
func (s *SkillService) GetFile(ctx context.Context, slug, version, filePath string, viewer *model.User) ([]byte, error) {
	cleanPath := sanitizeFilePath(filePath)
	if cleanPath == "" {
		return nil, fmt.Errorf("invalid file path")
	}
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil {
		return nil, err
	}
	if skill == nil {
		return nil, fmt.Errorf("skill not found: %s", slug)
	}
	if !canViewSkill(skill, viewer) {
		return nil, fmt.Errorf("skill not found: %s", slug)
	}

	if version == "" || version == "latest" {
		if skill.LatestVersionID == nil {
			return nil, fmt.Errorf("no versions published")
		}
		v, err := s.versionRepo.GetByID(ctx, *skill.LatestVersionID)
		if err != nil {
			return nil, err
		}
		version = v.Version
	}

	content, err := s.fileStore.GetFile(skill.OwnerHandle, slug, version, cleanPath)
	if err != nil {
		return nil, err
	}

	// 200KB limit
	if len(content) > 200*1024 {
		return nil, fmt.Errorf("file too large (max 200KB)")
	}
	return content, nil
}

// ResolveFingerprint finds a version by its fingerprint and returns the associated skill.
func (s *SkillService) ResolveFingerprint(ctx context.Context, fingerprint string) (*model.SkillVersion, *model.SkillWithOwner, error) {
	ver, err := s.versionRepo.GetByFingerprint(ctx, fingerprint)
	if err != nil || ver == nil {
		return nil, nil, err
	}
	skill, err := s.skillRepo.GetByID(ctx, ver.SkillID)
	if err != nil || skill == nil {
		return ver, nil, err
	}
	owner, _ := s.userRepo.GetByID(ctx, skill.OwnerID)
	swo := &model.SkillWithOwner{Skill: *skill}
	if owner != nil {
		swo.OwnerHandle = owner.Handle
	}
	return ver, swo, nil
}

// GetSkill returns a skill by slug with visibility check.
// viewer may be nil for anonymous access.
func (s *SkillService) GetSkill(ctx context.Context, slug string, viewer *model.User) (*model.SkillWithOwner, error) {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return nil, err
	}
	if !canViewSkill(skill, viewer) {
		return nil, nil // invisible = not found
	}
	return skill, nil
}

// ListSkills returns a paginated list of skills with visibility filtering.
func (s *SkillService) ListSkills(ctx context.Context, limit int, cursor, sort, category string, viewer *model.User) ([]model.SkillWithOwner, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	filter := repository.ListFilter{Category: category}
	if viewer != nil {
		filter.ViewerID = &viewer.ID
		filter.IsAdmin = viewer.IsModerator()
	}
	return s.skillRepo.List(ctx, limit, cursor, sort, filter)
}

// ListAllSkillsForAdmin returns all skills for admin management.
func (s *SkillService) ListAllSkillsForAdmin(ctx context.Context, limit int, cursor, visibility string) ([]model.SkillWithOwner, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.skillRepo.ListAllForAdmin(ctx, limit, cursor, visibility)
}

// RequestPublic lets the owner request to make a skill public.
func (s *SkillService) RequestPublic(ctx context.Context, user *model.User, slug string) error {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	if skill.OwnerID != user.ID && !user.IsAdmin() {
		return fmt.Errorf("forbidden")
	}
	if skill.Visibility == "public" && skill.ModerationStatus == "approved" {
		return fmt.Errorf("skill is already public")
	}
	if err := s.skillRepo.SetVisibility(ctx, skill.ID, "private", "pending_review"); err != nil {
		return err
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "request_public", "skill", &skill.ID, "", "")
	}
	return nil
}

// ReviewSkill lets an admin/moderator approve or reject a skill for public visibility.
func (s *SkillService) ReviewSkill(ctx context.Context, reviewerID *uuid.UUID, slug string, approve bool) error {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	action := "reject"
	if approve {
		action = "approve"
		if err := s.skillRepo.SetVisibility(ctx, skill.ID, "public", "approved"); err != nil {
			return err
		}
	} else {
		if err := s.skillRepo.SetVisibility(ctx, skill.ID, "private", "rejected"); err != nil {
			return err
		}
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, reviewerID, action, "skill", &skill.ID, "", "")
	}
	return nil
}

// SetSkillVisibility lets an admin directly set a skill's visibility.
func (s *SkillService) SetSkillVisibility(ctx context.Context, adminID *uuid.UUID, slug, visibility string) error {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	if visibility != "public" && visibility != "private" {
		return fmt.Errorf("visibility must be 'public' or 'private'")
	}
	moderationStatus := "approved"
	if err := s.skillRepo.SetVisibility(ctx, skill.ID, visibility, moderationStatus); err != nil {
		return err
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, adminID, "set_visibility", "skill", &skill.ID, visibility, "")
	}
	return nil
}

// canViewSkill checks if a viewer has access to a skill.
func canViewSkill(skill *model.SkillWithOwner, viewer *model.User) bool {
	// Public and approved: visible to all
	if skill.Visibility == "public" && skill.ModerationStatus == "approved" {
		return true
	}
	if viewer == nil {
		return false
	}
	// Admin/moderator can see all
	if viewer.IsModerator() {
		return true
	}
	// Owner can see their own skill
	return skill.OwnerID == viewer.ID
}

// GetVersions returns all versions for a skill.
func (s *SkillService) GetVersions(ctx context.Context, slug string, viewer *model.User) ([]model.SkillVersion, error) {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return nil, err
	}
	if !canViewSkill(skill, viewer) {
		return nil, nil
	}
	return s.versionRepo.ListBySkill(ctx, skill.ID)
}

// GetVersion returns a specific version.
func (s *SkillService) GetVersion(ctx context.Context, slug, version string, viewer *model.User) (*model.SkillVersion, error) {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return nil, err
	}
	if !canViewSkill(skill, viewer) {
		return nil, nil
	}
	return s.versionRepo.GetBySkillAndVersion(ctx, skill.ID, version)
}

// SoftDelete soft-deletes a skill.
//
// tokenNS:当前请求 token 绑定的 namespace ID(由 handler 透传)。
//   - 非 nil ⇒ 团队 token：要求 skill 隶属同一 namespace,跳过 owner 自检；
//   - nil    ⇒ 个人 token / cookie：必须本人 owner 或系统 admin。
func (s *SkillService) SoftDelete(ctx context.Context, user *model.User, slug string, tokenNS *uuid.UUID) error {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	if err := s.authorizeSkillWrite(skill.NamespaceID, skill.OwnerID, user, tokenNS); err != nil {
		return err
	}
	if err := s.skillRepo.SoftDelete(ctx, skill.ID); err != nil {
		return err
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "delete", "skill", &skill.ID, "", "")
	}
	return nil
}

// Undelete restores a soft-deleted skill. tokenNS 语义同 SoftDelete。
func (s *SkillService) Undelete(ctx context.Context, user *model.User, slug string, tokenNS *uuid.UUID) error {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	if err := s.authorizeSkillWrite(skill.NamespaceID, skill.OwnerID, user, tokenNS); err != nil {
		return err
	}
	if err := s.skillRepo.Undelete(ctx, skill.ID); err != nil {
		return err
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "undelete", "skill", &skill.ID, "", "")
	}
	return nil
}

// Star adds a star to a skill (atomic transaction).
func (s *SkillService) Star(ctx context.Context, userID uuid.UUID, slug string) error {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.starRepo.Star(ctx, userID, skill.ID); err != nil {
			return err
		}
		return s.skillRepo.UpdateStarsCount(ctx, skill.ID, 1)
	})
}

// Unstar removes a star from a skill (atomic transaction).
func (s *SkillService) Unstar(ctx context.Context, userID uuid.UUID, slug string) error {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	starred, err := s.starRepo.IsStarred(ctx, userID, skill.ID)
	if err != nil {
		return err
	}
	if !starred {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.starRepo.Unstar(ctx, userID, skill.ID); err != nil {
			return err
		}
		return s.skillRepo.UpdateStarsCount(ctx, skill.ID, -1)
	})
}

const (
	maxUploadFiles   = 500
	maxFileSize      = 5 * 1024 * 1024 // 5MB per file
)

// ReadMultipartFiles reads all files from a multipart form.
func ReadMultipartFiles(form *multipart.Form) (map[string][]byte, error) {
	files := make(map[string][]byte)
	for _, headers := range form.File {
		for _, header := range headers {
			if len(files) >= maxUploadFiles {
				return nil, fmt.Errorf("too many files (max %d)", maxUploadFiles)
			}
			if header.Size > maxFileSize {
				return nil, fmt.Errorf("file %s exceeds max size (%d bytes)", header.Filename, maxFileSize)
			}
			name := sanitizeFilePath(header.Filename)
			if name == "" {
				continue
			}
			f, err := header.Open()
			if err != nil {
				return nil, fmt.Errorf("open file %s: %w", name, err)
			}
			data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("read file %s: %w", name, err)
			}
			if int64(len(data)) > maxFileSize {
				return nil, fmt.Errorf("file %s exceeds max size (%d bytes)", name, maxFileSize)
			}
			files[name] = data
		}
	}
	return files, nil
}

// sanitizeFilePath cleans a user-supplied file path, rejecting traversal attempts.
func sanitizeFilePath(name string) string {
	name = path.Clean(name)
	name = strings.TrimPrefix(name, "/")
	if name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return ""
	}
	return name
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func extractFrontmatter(content []byte) json.RawMessage {
	s := string(content)
	if !strings.HasPrefix(s, "---") {
		return json.RawMessage("{}")
	}
	end := strings.Index(s[3:], "---")
	if end == -1 {
		return json.RawMessage("{}")
	}
	fm := strings.TrimSpace(s[3 : end+3])
	result, _ := json.Marshal(map[string]string{"raw": fm})
	return result
}

