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
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/gitstore"
	"github.com/saker-ai/skillhub/pkg/metrics"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"
	"github.com/saker-ai/skillhub/pkg/search"
	"github.com/saker-ai/skillhub/pkg/security"
	"github.com/saker-ai/skillhub/pkg/semver"
	"github.com/saker-ai/skillhub/pkg/store"
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
	notifSvc     *NotificationService
	bgCtx        context.Context
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

// SetBackgroundContext controls best-effort async work spawned by the service.
// Nil resets it to context.Background().
func (s *SkillService) SetBackgroundContext(ctx context.Context) {
	s.bgCtx = ctx
}

func (s *SkillService) backgroundContext() context.Context {
	if s.bgCtx != nil {
		return s.bgCtx
	}
	return context.Background()
}

// invalidateSkillCache clears both bare-slug and namespace-qualified cache entries
// for a skill. Must be called after any mutation that changes skill metadata.
func (s *SkillService) invalidateSkillCache(skill *model.SkillWithOwner) {
	s.skillRepo.InvalidateCache(skill.Slug)
	if skill.NamespaceID != nil {
		s.skillRepo.InvalidateCacheNS(skill.NamespaceID.String(), skill.Slug)
	}
}

// SetNamespaceService injects the namespace service for membership/publish checks.
// Optional — when nil, namespace-bound publishing is disabled.
func (s *SkillService) SetNamespaceService(ns *NamespaceService) {
	s.nsSvc = ns
}

// SetNotificationService injects the notification service for namespace publish notifications.
func (s *SkillService) SetNotificationService(ns *NotificationService) {
	s.notifSvc = ns
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
	Slug            string
	Version         string
	Changelog       string
	DisplayName     string
	Summary         string
	Category        string
	Kind            string
	Tags            []string
	Visibility      string                  // "" | "private" | "public" — only honored on first create
	NamespaceSlug   string                  // optional team namespace
	Files           map[string][]byte       // path → content
	Dependencies    []model.SkillDependency // declared upstream skill deps
	SignatureBundle []byte                  // optional sigstore .sigstore JSON
	ObjectsUploaded bool                    // files already exist in DirectObjectStore final keys; write metadata only

	// TokenNamespace 由 handler 透传当前请求 token 绑定的 namespace ID(*middleware.GetTokenNamespace*)。
	// 非 nil ⇒ 团队 token,要求目标 skill 隶属该 namespace；不一致直接 403。
	// nil ⇒ 个人 token / cookie 会话,沿用旧的 owner-ID 鉴权路径。
	TokenNamespace *uuid.UUID
}

type DirectUploadFileRequest struct {
	Path        string `json:"path"`
	Size        *int64 `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	ContentType string `json:"contentType"`
}

type DirectUploadPlanRequest struct {
	Slug           string
	Version        string
	NamespaceSlug  string
	Files          []DirectUploadFileRequest
	TokenNamespace *uuid.UUID
}

type DirectUploadFile struct {
	Path        string                       `json:"path"`
	ContentType string                       `json:"contentType,omitempty"`
	Exists      bool                         `json:"exists,omitempty"`
	Object      *store.DirectObjectURL       `json:"object,omitempty"`
	Multipart   *store.MultipartObjectUpload `json:"multipart,omitempty"`
}

type DirectUploadPlan struct {
	Provider string             `json:"provider"`
	Bucket   string             `json:"bucket"`
	Owner    string             `json:"owner"`
	Slug     string             `json:"slug"`
	Version  string             `json:"version"`
	Files    []DirectUploadFile `json:"files"`
}

type DirectUploadCompleteFile struct {
	Path        string                      `json:"path"`
	Size        int64                       `json:"size"`
	SHA256      string                      `json:"sha256"`
	ContentType string                      `json:"contentType"`
	UploadID    string                      `json:"upload_id,omitempty"`
	Parts       []store.CompletedUploadPart `json:"parts,omitempty"`
}

type DirectUploadCompleteRequest struct {
	Slug            string
	Version         string
	Changelog       string
	DisplayName     string
	Summary         string
	Category        string
	Kind            string
	Tags            []string
	Visibility      string
	NamespaceSlug   string
	Files           []DirectUploadCompleteFile
	Dependencies    []model.SkillDependency
	SignatureBundle []byte
	TokenNamespace  *uuid.UUID
}

// authorizeSkillWrite 判定 caller 是否可对 skill 做写操作。
//
// 优先看 tokenNS：团队 token 在签发时已校验过 owner/admin 角色,所以这里跳过
// skill.OwnerID 检查,只要求目标 skill 隶属同一个 namespace。
//
// tokenNS == nil(个人 token / cookie 会话)走旧路径：必须本人 owner 或系统 admin。
func (s *SkillService) authorizeSkillWrite(ctx context.Context, skillNS *uuid.UUID, ownerID uuid.UUID, user *model.User, tokenNS *uuid.UUID) error {
	if tokenNS != nil {
		if skillNS == nil || *skillNS != *tokenNS {
			return fmt.Errorf("%w: team token cannot operate on skills outside its namespace", ErrForbidden)
		}
		return nil
	}
	if ownerID == user.ID || user.IsAdmin() {
		return nil
	}
	if skillNS != nil && s.nsSvc != nil {
		role, err := s.nsSvc.GetMemberRole(ctx, *skillNS, user.ID)
		if err == nil && role != "" && role != "reader" {
			return nil
		}
	}
	return fmt.Errorf("%w: only the skill owner, namespace member, or system admin can perform this action", ErrForbidden)
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
		if dep.Slug == req.Slug && dep.Namespace == "" {
			return nil, nil, fmt.Errorf("dependency '%s' cannot depend on itself", dep.Slug)
		}
		if dep.Version == "" {
			return nil, nil, fmt.Errorf("dependency '%s' requires a version range", dep.Slug)
		}
		depRef := model.SkillRef{Slug: dep.Slug, Namespace: dep.Namespace}
		depSkill, err := s.resolveSkillRef(ctx, depRef)
		if err != nil {
			return nil, nil, fmt.Errorf("lookup dependency '%s': %w", depRef, err)
		}
		if depSkill == nil {
			return nil, nil, fmt.Errorf("dependency '%s' not found", depRef)
		}
	}

	// Resolve namespace. Every skill must belong to a namespace.
	// When none specified, default to the user's personal namespace (lazy-created).
	if s.nsSvc == nil {
		return nil, nil, fmt.Errorf("namespace publishing not configured on this server")
	}
	var resolvedNS *model.Namespace
	if req.NamespaceSlug != "" {
		ns, err := s.nsSvc.GetBySlug(ctx, req.NamespaceSlug)
		if err != nil || ns == nil {
			return nil, nil, fmt.Errorf("namespace '%s' not found", req.NamespaceSlug)
		}
		can, err := s.nsSvc.CanPublish(ctx, req.NamespaceSlug, user.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("check namespace membership: %w", err)
		}
		if !can && !user.IsAdmin() {
			return nil, nil, fmt.Errorf("%w: not a member of namespace '%s'", ErrForbidden, req.NamespaceSlug)
		}
		resolvedNS = ns
	} else {
		ns, err := s.nsSvc.EnsurePersonalNamespace(ctx, user)
		if err != nil {
			return nil, nil, fmt.Errorf("ensure personal namespace: %w", err)
		}
		resolvedNS = ns
		req.NamespaceSlug = ns.Slug
	}
	nsID := &resolvedNS.ID

	// 团队 token 必须指向自己 namespace,不允许借此发个人 skill 或别 namespace 的 skill。
	if req.TokenNamespace != nil {
		if *nsID != *req.TokenNamespace {
			return nil, nil, fmt.Errorf("%w: team token can only publish to its bound namespace", ErrForbidden)
		}
	}

	// Namespace quota check: reject if the namespace has reached its skill limit.
	if resolvedNS.MaxSkills > 0 {
		count, err := s.skillRepo.CountByNamespace(ctx, resolvedNS.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("check namespace quota: %w", err)
		}
		// Only check quota when creating a NEW skill (not re-publishing).
		existingCheck, _ := s.skillRepo.GetByNSAndSlug(ctx, resolvedNS.ID, req.Slug)
		if existingCheck == nil && int(count) >= resolvedNS.MaxSkills {
			return nil, nil, fmt.Errorf("%w: namespace '%s' has reached its skill quota (%d)", ErrConflict, req.NamespaceSlug, resolvedNS.MaxSkills)
		}
	}

	// Validate visibility (defaults to namespace's default visibility)
	visibility := resolvedNS.DefaultVisibility
	if visibility == "" {
		visibility = "private"
	}
	if req.Visibility != "" {
		if req.Visibility != "private" && req.Visibility != "public" {
			return nil, nil, fmt.Errorf("visibility must be 'private' or 'public'")
		}
		visibility = req.Visibility
	}

	// Find or create skill (scoped to the resolved namespace)
	skill, err := s.skillRepo.GetByNSAndSlug(ctx, *nsID, req.Slug)
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
		if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, req.TokenNamespace); err != nil {
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

	// Validate file paths and compute metadata
	var filesMeta []model.VersionFile
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
		filesMeta = append(filesMeta, model.VersionFile{
			Path:   path,
			Size:   int64(len(content)),
			SHA256: hex.EncodeToString(h[:]),
		})
	}
	fingerprint := computeContentFingerprint(req.Files)

	filesJSON, _ := json.Marshal(filesMeta)

	// Extract parsed info from SKILL.md if present
	parsedJSON := model.JSONRaw("{}")
	if content, ok := req.Files["SKILL.md"]; ok {
		parsedJSON = model.JSONRaw(extractFrontmatter(content))
	}

	// Publish files or finalize objects that were already uploaded directly.
	email := user.Handle + "@skillhub.local"
	publishOpts := store.PublishOpts{
		Owner:   user.Handle,
		Slug:    req.Slug,
		Version: version,
		Files:   req.Files,
		Author:  user.Handle,
		Email:   email,
		Message: req.Changelog,
	}
	var commitHash string
	if req.ObjectsUploaded {
		ds, ok := s.fileStore.(store.DirectObjectStore)
		if !ok {
			return nil, nil, fmt.Errorf("%w: direct upload is only available for object storage backends", ErrValidation)
		}
		commitHash, err = ds.PutMeta(ctx, publishOpts)
	} else {
		commitHash, err = s.fileStore.Publish(ctx, publishOpts)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("publish files: %w", err)
	}

	// Serialize dependencies (default to empty array)
	depsJSON := model.JSONRaw("[]")
	if len(req.Dependencies) > 0 {
		if encoded, err := json.Marshal(req.Dependencies); err == nil {
			depsJSON = model.JSONRaw(encoded)
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
		if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrValidation) {
			if cleanErr := s.fileStore.DeleteVersion(ctx, user.Handle, req.Slug, version); cleanErr != nil {
				s.loggerOrDefault().Warn("failed to clean orphaned skill files",
					"slug", req.Slug, "version", version, "err", cleanErr)
			}
		}
		return nil, nil, err
	}

	// 失效缓存：tx 里走的是 raw `tx.Updates(&model.Skill{})`,绕过了 SkillRepo 的
	// mutator,无法借助 repo 自身做失效。在这里显式调用,确保下一次 GetBySlug
	// 读到的是新 latest_version_id / metadata。
	s.invalidateSkillCache(skill)

	s.indexSkill(ctx, skill, req.NamespaceSlug, req.Files)

	// Mirror push (async)
	if s.mirrorSvc != nil && s.mirrorSvc.Enabled() {
		// Snapshot the logger outside the goroutine so the closure doesn't
		// race with a (hypothetical) future SetLogger call mid-flight.
		lg := s.loggerOrDefault()
		bgCtx := s.backgroundContext()
		go func() {
			if err := s.mirrorSvc.PushMirror(bgCtx, user.Handle, req.Slug); err != nil {
				lg.Warn("mirror push failed", "owner", user.Handle, "slug", req.Slug, "err", err)
			}
		}()
	}

	// Write audit log
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "publish", "skill_version", &ver.ID, "", "")
	}

	s.metricsOrDefault().SkillPublished.WithLabelValues(skill.Visibility).Inc()

	// Notify namespace members about the new version (async, best-effort).
	if nsID != nil && s.notifSvc != nil && s.nsSvc != nil {
		capturedSkill := skill
		capturedVer := ver
		bgCtx := s.backgroundContext()
		go func() {
			members, err := s.nsSvc.ListMemberIDs(bgCtx, *nsID)
			if err != nil {
				return
			}
			title := fmt.Sprintf("New version: @%s/%s v%s", req.NamespaceSlug, capturedSkill.Slug, capturedVer.Version)
			link := fmt.Sprintf("/skills/@%s/%s", req.NamespaceSlug, capturedSkill.Slug)
			for _, memberID := range members {
				if memberID == user.ID {
					continue
				}
				s.notifSvc.Notify(bgCtx, memberID, "publish", title, "", link)
			}
		}()
	}

	return skill, ver, nil
}

func (s *SkillService) CreateDirectUploadPlan(ctx context.Context, user *model.User, req DirectUploadPlanRequest) (*DirectUploadPlan, error) {
	if user == nil {
		return nil, fmt.Errorf("%w: authentication required", ErrForbidden)
	}
	ds, ok := s.fileStore.(store.DirectObjectStore)
	if !ok {
		return nil, fmt.Errorf("%w: direct upload is only available for object storage backends", ErrValidation)
	}
	if req.Slug == "" {
		return nil, fmt.Errorf("slug is required")
	}
	if !semverRe.MatchString(req.Version) {
		return nil, fmt.Errorf("invalid version '%s': must be valid semver (e.g. 1.0.0)", req.Version)
	}
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("files are required")
	}
	if len(req.Files) > maxUploadFiles {
		return nil, fmt.Errorf("too many files (max %d)", maxUploadFiles)
	}

	ownerHandle := user.Handle
	var nsID *uuid.UUID
	if s.nsSvc == nil {
		return nil, fmt.Errorf("namespace publishing not configured on this server")
	}
	if req.NamespaceSlug != "" {
		ns, err := s.nsSvc.GetBySlug(ctx, req.NamespaceSlug)
		if err != nil || ns == nil {
			return nil, fmt.Errorf("namespace '%s' not found", req.NamespaceSlug)
		}
		can, err := s.nsSvc.CanPublish(ctx, req.NamespaceSlug, user.ID)
		if err != nil {
			return nil, fmt.Errorf("check namespace membership: %w", err)
		}
		if !can && !user.IsAdmin() {
			return nil, fmt.Errorf("not a member of namespace '%s'", req.NamespaceSlug)
		}
		nsID = &ns.ID
	} else {
		ns, err := s.nsSvc.EnsurePersonalNamespace(ctx, user)
		if err != nil {
			return nil, fmt.Errorf("ensure personal namespace: %w", err)
		}
		nsID = &ns.ID
	}
	if req.TokenNamespace != nil && (nsID == nil || *nsID != *req.TokenNamespace) {
		return nil, fmt.Errorf("%w: team token can only publish to its bound namespace", ErrForbidden)
	}

	skill, err := s.skillRepo.GetByNSAndSlug(ctx, *nsID, req.Slug)
	if err != nil {
		return nil, fmt.Errorf("get skill: %w", err)
	}
	if skill != nil {
		if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, req.TokenNamespace); err != nil {
			return nil, err
		}
		ownerHandle = skill.OwnerHandle
		existing, err := s.versionRepo.GetBySkillAndVersion(ctx, skill.ID, req.Version)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, fmt.Errorf("version %s already exists", req.Version)
		}
		if latest, _ := s.versionRepo.GetLatest(ctx, skill.ID); latest != nil && semver.Compare(req.Version, latest.Version) <= 0 {
			return nil, fmt.Errorf("version %s must be greater than current latest %s", req.Version, latest.Version)
		}
	}

	const presignTTL = 15 * time.Minute
	plan := &DirectUploadPlan{
		Provider: ds.Provider(),
		Owner:    ownerHandle,
		Slug:     req.Slug,
		Version:  req.Version,
		Files:    make([]DirectUploadFile, 0, len(req.Files)),
	}
	seen := map[string]struct{}{}
	for _, f := range req.Files {
		cleanPath := sanitizeFilePath(f.Path)
		if cleanPath == "" {
			return nil, fmt.Errorf("invalid file path: %s", f.Path)
		}
		if _, dup := seen[cleanPath]; dup {
			return nil, fmt.Errorf("duplicate file path: %s", cleanPath)
		}
		seen[cleanPath] = struct{}{}
		if f.Size != nil && f.SHA256 != "" {
			exists, err := s.directObjectMatches(ctx, ownerHandle, req.Slug, req.Version, cleanPath, *f.Size, f.SHA256)
			if err != nil {
				return nil, fmt.Errorf("verify existing object %s: %w", cleanPath, err)
			}
			if exists {
				plan.Files = append(plan.Files, DirectUploadFile{
					Path:        cleanPath,
					ContentType: f.ContentType,
					Exists:      true,
				})
				continue
			}
		}
		if f.Size != nil && *f.Size >= directUploadMultipartThreshold {
			ms, ok := s.fileStore.(store.MultipartObjectStore)
			if ok {
				mp, err := ms.CreateMultipartUpload(ctx, ownerHandle, req.Slug, req.Version, cleanPath, f.ContentType, *f.Size, directUploadMultipartPartSize, presignTTL)
				if err != nil {
					return nil, fmt.Errorf("create multipart upload URLs for %s: %w", cleanPath, err)
				}
				plan.Files = append(plan.Files, DirectUploadFile{
					Path:        cleanPath,
					ContentType: f.ContentType,
					Multipart:   mp,
				})
				continue
			}
		}
		obj, err := ds.PresignPut(ctx, ownerHandle, req.Slug, req.Version, cleanPath, f.ContentType, presignTTL)
		if err != nil {
			return nil, fmt.Errorf("create upload URL for %s: %w", cleanPath, err)
		}
		plan.Bucket = obj.Bucket
		plan.Files = append(plan.Files, DirectUploadFile{
			Path:        cleanPath,
			ContentType: f.ContentType,
			Object:      obj,
		})
	}
	return plan, nil
}

func (s *SkillService) directObjectMatches(ctx context.Context, owner, slug, version, filePath string, size int64, sha256Hex string) (bool, error) {
	if sha256Hex == "" {
		return false, nil
	}
	content, err := s.fileStore.GetFile(ctx, owner, slug, version, filePath)
	if err != nil {
		return false, nil
	}
	if int64(len(content)) != size {
		return false, nil
	}
	sum := sha256.Sum256(content)
	return strings.EqualFold(hex.EncodeToString(sum[:]), sha256Hex), nil
}

func (s *SkillService) CompleteDirectUpload(ctx context.Context, user *model.User, req DirectUploadCompleteRequest) (*model.SkillWithOwner, *model.SkillVersion, error) {
	if user == nil {
		return nil, nil, fmt.Errorf("%w: authentication required", ErrForbidden)
	}
	ds, ok := s.fileStore.(store.DirectObjectStore)
	if !ok {
		return nil, nil, fmt.Errorf("%w: direct upload is only available for object storage backends", ErrValidation)
	}
	if req.Slug == "" {
		return nil, nil, fmt.Errorf("slug is required")
	}
	if !semverRe.MatchString(req.Version) {
		return nil, nil, fmt.Errorf("invalid version '%s': must be valid semver (e.g. 1.0.0)", req.Version)
	}
	if len(req.Files) == 0 {
		return nil, nil, fmt.Errorf("files are required")
	}
	if len(req.Files) > maxUploadFiles {
		return nil, nil, fmt.Errorf("too many files (max %d)", maxUploadFiles)
	}

	ownerHandle := user.Handle
	if s.nsSvc == nil {
		return nil, nil, fmt.Errorf("namespace publishing not configured on this server")
	}
	var nsID *uuid.UUID
	if req.NamespaceSlug != "" {
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
	} else {
		ns, err := s.nsSvc.EnsurePersonalNamespace(ctx, user)
		if err != nil {
			return nil, nil, fmt.Errorf("ensure personal namespace: %w", err)
		}
		nsID = &ns.ID
		req.NamespaceSlug = ns.Slug
	}
	if req.TokenNamespace != nil && (nsID == nil || *nsID != *req.TokenNamespace) {
		return nil, nil, fmt.Errorf("%w: team token can only publish to its bound namespace", ErrForbidden)
	}

	if skill, err := s.skillRepo.GetByNSAndSlug(ctx, *nsID, req.Slug); err != nil {
		return nil, nil, fmt.Errorf("get skill: %w", err)
	} else if skill != nil {
		if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, req.TokenNamespace); err != nil {
			return nil, nil, err
		}
		ownerHandle = skill.OwnerHandle
	}

	files := make(map[string][]byte, len(req.Files))
	seen := map[string]struct{}{}
	for _, f := range req.Files {
		cleanPath := sanitizeFilePath(f.Path)
		if cleanPath == "" {
			return nil, nil, fmt.Errorf("invalid file path: %s", f.Path)
		}
		if _, dup := seen[cleanPath]; dup {
			return nil, nil, fmt.Errorf("duplicate file path: %s", cleanPath)
		}
		seen[cleanPath] = struct{}{}
		if len(f.Parts) > 0 {
			ms, ok := ds.(store.MultipartObjectStore)
			if !ok {
				return nil, nil, fmt.Errorf("%w: multipart direct upload is not available for this backend", ErrValidation)
			}
			uploadID := strings.TrimSpace(f.UploadID)
			if uploadID == "" {
				return nil, nil, fmt.Errorf("%w: multipart upload id is required for %s", ErrValidation, cleanPath)
			}
			if err := ms.CompleteMultipartUpload(ctx, ownerHandle, req.Slug, req.Version, cleanPath, uploadID, f.Parts); err != nil {
				return nil, nil, fmt.Errorf("complete multipart upload %s: %w", cleanPath, err)
			}
		}
		content, err := s.fileStore.GetFile(ctx, ownerHandle, req.Slug, req.Version, cleanPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read uploaded file %s: %w", cleanPath, err)
		}
		if f.Size >= 0 && f.Size != 0 && int64(len(content)) != f.Size {
			return nil, nil, fmt.Errorf("%w: uploaded file %s size mismatch", ErrValidation, cleanPath)
		}
		if f.SHA256 != "" {
			sum := sha256.Sum256(content)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), f.SHA256) {
				return nil, nil, fmt.Errorf("%w: uploaded file %s checksum mismatch", ErrValidation, cleanPath)
			}
		}
		files[cleanPath] = content
	}
	if _, ok := files["SKILL.md"]; !ok {
		return nil, nil, fmt.Errorf("%w: SKILL.md is required", ErrValidation)
	}

	return s.PublishVersion(ctx, user, PublishRequest{
		Slug:            req.Slug,
		Version:         req.Version,
		Changelog:       req.Changelog,
		DisplayName:     req.DisplayName,
		Summary:         req.Summary,
		Category:        req.Category,
		Kind:            req.Kind,
		Tags:            req.Tags,
		Visibility:      req.Visibility,
		NamespaceSlug:   req.NamespaceSlug,
		Files:           files,
		Dependencies:    req.Dependencies,
		SignatureBundle: req.SignatureBundle,
		ObjectsUploaded: true,
		TokenNamespace:  req.TokenNamespace,
	})
}

// DownloadResult carries a zip archive plus metadata for ETag / filename.
type DownloadResult struct {
	Archive     io.ReadCloser
	Filename    string
	Fingerprint string
	Version     string
}

// ResolveVersion returns the version record for a ref+version (accepts "latest").
// It enforces visibility. Returned SkillVersion is non-nil on success.
func (s *SkillService) ResolveVersion(ctx context.Context, ref model.SkillRef, version string, viewer *model.User) (*model.SkillWithOwner, *model.SkillVersion, error) {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	if skill == nil || !s.canViewSkill(ctx, skill, viewer) {
		return nil, nil, fmt.Errorf("skill not found: %s", ref)
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

// ResolveVersionByID returns the version record for a skill ID and version
// (accepts "latest"). It enforces the same visibility rules as ResolveVersion.
func (s *SkillService) ResolveVersionByID(ctx context.Context, id uuid.UUID, version string, viewer *model.User) (*model.SkillWithOwner, *model.SkillVersion, error) {
	skill, err := s.skillRepo.GetWithOwnerByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if skill == nil || !s.canViewSkill(ctx, skill, viewer) {
		return nil, nil, fmt.Errorf("skill not found: %s", id)
	}
	return s.resolveVersionForSkill(ctx, skill, version)
}

func (s *SkillService) resolveVersionForSkill(ctx context.Context, skill *model.SkillWithOwner, version string) (*model.SkillWithOwner, *model.SkillVersion, error) {
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
func (s *SkillService) Download(ctx context.Context, ref model.SkillRef, version, identityHash string, viewer *model.User) (*DownloadResult, error) {
	skill, ver, err := s.ResolveVersion(ctx, ref, version, viewer)
	if err != nil {
		return nil, err
	}

	archive, err := s.fileStore.Archive(ctx, skill.OwnerHandle, skill.Slug, ver.Version)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}

	// Record download (deduplicate) — best-effort, not fatal.
	if identityHash != "" {
		isNew, _ := s.downloadRepo.RecordDownload(ctx, skill.ID, ver.ID, identityHash)
		if isNew {
			// 计数失败不影响下载本身——指标统计可容忍偶发丢失。
			_ = s.skillRepo.IncrementDownloads(ctx, skill.ID)
		}
	}

	s.metricsOrDefault().SkillDownloads.Inc()

	return &DownloadResult{
		Archive:     archive,
		Filename:    fmt.Sprintf("%s-%s.zip", skill.Slug, ver.Version),
		Fingerprint: ver.Fingerprint,
		Version:     ver.Version,
	}, nil
}

func (s *SkillService) DirectDownloadFile(ctx context.Context, ref model.SkillRef, version, filePath string, viewer *model.User) (*store.DirectObjectURL, *model.SkillVersion, error) {
	cleanPath := sanitizeFilePath(filePath)
	if cleanPath == "" {
		return nil, nil, fmt.Errorf("invalid file path")
	}
	ds, ok := s.fileStore.(store.DirectObjectStore)
	if !ok {
		return nil, nil, fmt.Errorf("%w: direct download is only available for object storage backends", ErrValidation)
	}
	skill, ver, err := s.ResolveVersion(ctx, ref, version, viewer)
	if err != nil {
		return nil, nil, err
	}
	const presignTTL = 15 * time.Minute
	obj, err := ds.PresignGet(ctx, skill.OwnerHandle, skill.Slug, ver.Version, cleanPath, presignTTL)
	if err != nil {
		return nil, nil, fmt.Errorf("create download URL: %w", err)
	}
	return obj, ver, nil
}

// GetFile reads a single file from a skill version.
func (s *SkillService) GetFile(ctx context.Context, ref model.SkillRef, version, filePath string, viewer *model.User) ([]byte, error) {
	cleanPath := sanitizeFilePath(filePath)
	if cleanPath == "" {
		return nil, fmt.Errorf("invalid file path")
	}
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if skill == nil {
		return nil, fmt.Errorf("skill not found: %s", ref)
	}
	if !s.canViewSkill(ctx, skill, viewer) {
		return nil, fmt.Errorf("skill not found: %s", ref)
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

	content, err := s.fileStore.GetFile(ctx, skill.OwnerHandle, skill.Slug, version, cleanPath)
	if err != nil {
		return nil, err
	}

	// Runtime clients lazily fetch support files. Keep a conservative
	// single-file cap while allowing markdown/reference files larger than the
	// old UI-focused 200 KiB limit.
	if len(content) > 512*1024 {
		return nil, fmt.Errorf("file too large (max 512KB)")
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

// GetSkill returns a skill by ref with visibility check.
// viewer may be nil for anonymous access.
func (s *SkillService) GetSkill(ctx context.Context, ref model.SkillRef, viewer *model.User) (*model.SkillWithOwner, error) {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if skill == nil {
		return nil, nil
	}
	if !s.canViewSkill(ctx, skill, viewer) {
		return nil, nil // invisible = not found
	}
	return skill, nil
}

// ListSkills returns a paginated list of skills with visibility filtering.
// namespaceSlug is optional; when non-empty only skills in that namespace are returned.
func (s *SkillService) ListSkills(ctx context.Context, limit int, cursor, sortKey, category, namespaceSlug string, viewer *model.User) ([]model.SkillWithOwner, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	filter := repository.ListFilter{Category: category}
	if viewer != nil {
		filter.ViewerID = &viewer.ID
		filter.IsAdmin = viewer.IsModerator()
	}
	if namespaceSlug != "" && s.nsSvc != nil {
		ns, err := s.nsSvc.GetBySlug(ctx, namespaceSlug)
		if err != nil {
			return nil, "", err
		}
		if ns != nil {
			filter.NamespaceID = &ns.ID
		}
	}
	return s.skillRepo.List(ctx, limit, cursor, sortKey, filter)
}

// ListAllSkillsForAdmin returns all skills for admin management.
func (s *SkillService) ListAllSkillsForAdmin(ctx context.Context, limit int, cursor, visibility string) ([]model.SkillWithOwner, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.skillRepo.ListAllForAdmin(ctx, limit, cursor, visibility)
}

// RequestPublic lets the owner request to make a skill public.
func (s *SkillService) RequestPublic(ctx context.Context, user *model.User, ref model.SkillRef) error {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return err
	}
	if skill == nil {
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
	s.invalidateSkillCache(skill)
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "request_public", "skill", &skill.ID, "", "")
	}
	return nil
}

// ReviewSkill lets an admin/moderator approve or reject a skill for public visibility.
func (s *SkillService) ReviewSkill(ctx context.Context, reviewerID *uuid.UUID, ref model.SkillRef, approve bool) error {
	skill, err := s.resolveSkillRef(ctx, ref)
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
	s.invalidateSkillCache(skill)
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, reviewerID, action, "skill", &skill.ID, "", "")
	}
	return nil
}

// SetSkillVisibility lets an admin directly set a skill's visibility.
func (s *SkillService) SetSkillVisibility(ctx context.Context, adminID *uuid.UUID, ref model.SkillRef, visibility string) error {
	skill, err := s.resolveSkillRef(ctx, ref)
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
	updated, err := s.resolveSkillRef(ctx, ref)
	if err != nil || updated == nil {
		return fmt.Errorf("skill not found")
	}
	s.invalidateSkillCache(updated)
	s.indexSkill(ctx, updated, updated.NamespaceSlug, nil)
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, adminID, "set_visibility", "skill", &skill.ID, visibility, "")
	}
	return nil
}

// resolveSkillRef resolves a SkillRef to a SkillWithOwner.
//
// Qualified refs (@namespace/slug) do a direct lookup by namespace+slug.
// Bare refs (slug only) search globally and handle disambiguation:
//   - 0 matches → nil, nil (not found)
//   - 1 match  → return it
//   - N matches → return AmbiguousSlugError (maps to 409)
func (s *SkillService) resolveSkillRef(ctx context.Context, ref model.SkillRef) (*model.SkillWithOwner, error) {
	return resolveSkillRefWith(ctx, ref, s.skillRepo, s.nsSvc)
}

func (s *SkillService) resolveSkillRefIncludeDeleted(ctx context.Context, ref model.SkillRef) (*model.SkillWithOwner, error) {
	if ref.IsQualified() {
		if s.nsSvc == nil {
			return nil, nil
		}
		ns, err := s.nsSvc.GetBySlug(ctx, ref.Namespace)
		if err != nil || ns == nil {
			return nil, nil
		}
		return s.skillRepo.GetByNSAndSlugIncludeDeleted(ctx, ns.ID, ref.Slug)
	}

	all, err := s.skillRepo.GetBySlugGlobalIncludeDeleted(ctx, ref.Slug)
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
		return nil, &AmbiguousSlugError{Slug: ref.Slug, Candidates: candidates}
	}
}

// canViewSkill checks if a viewer has access to a skill.
func (s *SkillService) canViewSkill(ctx context.Context, skill *model.SkillWithOwner, viewer *model.User) bool {
	return canViewSkillWith(ctx, skill, viewer, s.nsSvc)
}

// GetVersions returns all versions for a skill.
func (s *SkillService) GetVersions(ctx context.Context, ref model.SkillRef, viewer *model.User) ([]model.SkillVersion, error) {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if skill == nil {
		return nil, nil
	}
	if !s.canViewSkill(ctx, skill, viewer) {
		return nil, nil
	}
	return s.versionRepo.ListBySkill(ctx, skill.ID)
}

// GetVersion returns a specific version.
func (s *SkillService) GetVersion(ctx context.Context, ref model.SkillRef, version string, viewer *model.User) (*model.SkillVersion, error) {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if skill == nil {
		return nil, nil
	}
	if !s.canViewSkill(ctx, skill, viewer) {
		return nil, nil
	}
	return s.versionRepo.GetBySkillAndVersion(ctx, skill.ID, version)
}

// SoftDelete soft-deletes a skill.
//
// tokenNS:当前请求 token 绑定的 namespace ID(由 handler 透传)。
//   - 非 nil ⇒ 团队 token：要求 skill 隶属同一 namespace,跳过 owner 自检；
//   - nil    ⇒ 个人 token / cookie：必须本人 owner 或系统 admin。
func (s *SkillService) SoftDelete(ctx context.Context, user *model.User, ref model.SkillRef, tokenNS *uuid.UUID) error {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return err
	}
	if skill == nil {
		return fmt.Errorf("skill not found")
	}
	if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, tokenNS); err != nil {
		return err
	}
	if err := s.skillRepo.SoftDelete(ctx, skill.ID); err != nil {
		return err
	}
	s.invalidateSkillCache(skill)
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "delete", "skill", &skill.ID, "", "")
	}
	return nil
}

func (s *SkillService) PurgeByRef(ctx context.Context, user *model.User, ref model.SkillRef) error {
	if user.Role != "admin" {
		return fmt.Errorf("admin required")
	}
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return err
	}
	slug := ref.Slug
	if skill != nil {
		slug = skill.Slug
		_ = s.fileStore.Delete(ctx, skill.OwnerHandle, slug)
	}
	if err := s.skillRepo.HardDeleteBySlug(ctx, slug); err != nil {
		return err
	}
	s.skillRepo.InvalidateCache(slug)
	if skill != nil && skill.NamespaceID != nil {
		s.skillRepo.InvalidateCacheNS(skill.NamespaceID.String(), slug)
	}
	return nil
}

// Undelete restores a soft-deleted skill. tokenNS 语义同 SoftDelete。
func (s *SkillService) Undelete(ctx context.Context, user *model.User, ref model.SkillRef, tokenNS *uuid.UUID) error {
	skill, err := s.resolveSkillRefIncludeDeleted(ctx, ref)
	if err != nil {
		return err
	}
	if skill == nil {
		return fmt.Errorf("skill not found")
	}
	if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, tokenNS); err != nil {
		return err
	}
	if err := s.skillRepo.Undelete(ctx, skill.ID); err != nil {
		return err
	}
	s.invalidateSkillCache(skill)
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "undelete", "skill", &skill.ID, "", "")
	}
	return nil
}

// Star adds a star to a skill (atomic transaction).
func (s *SkillService) Star(ctx context.Context, userID uuid.UUID, ref model.SkillRef) error {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return err
	}
	if skill == nil {
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
func (s *SkillService) Unstar(ctx context.Context, userID uuid.UUID, ref model.SkillRef) error {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return err
	}
	if skill == nil {
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

// TransferSkill moves a skill from its current namespace to a different one.
// The caller must be owner/admin of the source namespace AND a member of the target namespace.
func (s *SkillService) TransferSkill(ctx context.Context, user *model.User, ref model.SkillRef, targetNSSlug string, tokenNS *uuid.UUID) error {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return err
	}
	if skill == nil {
		return fmt.Errorf("%w: skill not found", ErrNotFound)
	}
	if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, tokenNS); err != nil {
		return err
	}

	if s.nsSvc == nil {
		return fmt.Errorf("namespace service not configured")
	}
	targetNS, err := s.nsSvc.GetBySlug(ctx, targetNSSlug)
	if err != nil || targetNS == nil {
		return fmt.Errorf("%w: target namespace '%s' not found", ErrNotFound, targetNSSlug)
	}
	can, err := s.nsSvc.CanPublish(ctx, targetNSSlug, user.ID)
	if err != nil {
		return err
	}
	if !can && !user.IsAdmin() {
		return fmt.Errorf("%w: not a member of target namespace '%s'", ErrForbidden, targetNSSlug)
	}

	// Check slug doesn't conflict in target namespace.
	existing, err := s.skillRepo.GetByNSAndSlug(ctx, targetNS.ID, skill.Slug)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("%w: slug '%s' already exists in namespace '%s'", ErrConflict, skill.Slug, targetNSSlug)
	}

	if err := s.db.WithContext(ctx).Model(&model.Skill{}).
		Where("id = ?", skill.ID).
		Update("namespace_id", targetNS.ID).Error; err != nil {
		return fmt.Errorf("transfer skill: %w", err)
	}

	s.invalidateSkillCache(skill)
	s.skillRepo.InvalidateCacheNS(targetNS.ID.String(), skill.Slug)

	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "transfer", "skill", &skill.ID,
			fmt.Sprintf("from=%s,to=%s", skill.NamespaceSlug, targetNSSlug), "")
	}
	return nil
}

// UpdateFileRequest describes a single-file update that auto-bumps the patch version.
type UpdateFileRequest struct {
	Ref            model.SkillRef
	Path           string
	Content        []byte
	Changelog      string
	TokenNamespace *uuid.UUID
}

// UpdateFile replaces a single file in the latest version of a skill and
// publishes a new version with the patch number incremented.
func (s *SkillService) UpdateFile(ctx context.Context, user *model.User, req UpdateFileRequest) (*model.SkillWithOwner, *model.SkillVersion, error) {
	cleanPath := sanitizeFilePath(req.Path)
	if cleanPath == "" {
		return nil, nil, fmt.Errorf("invalid file path")
	}

	skill, err := s.resolveSkillRef(ctx, req.Ref)
	if err != nil {
		return nil, nil, err
	}
	if skill == nil {
		return nil, nil, fmt.Errorf("skill not found: %s", req.Ref)
	}
	if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, req.TokenNamespace); err != nil {
		return nil, nil, err
	}

	if skill.LatestVersionID == nil {
		return nil, nil, fmt.Errorf("no versions published")
	}
	latest, err := s.versionRepo.GetByID(ctx, *skill.LatestVersionID)
	if err != nil || latest == nil {
		return nil, nil, fmt.Errorf("latest version not found")
	}

	var fileMeta []model.VersionFile
	if err := json.Unmarshal(latest.Files, &fileMeta); err != nil {
		return nil, nil, fmt.Errorf("parse version files: %w", err)
	}

	files := make(map[string][]byte, len(fileMeta)+1)
	for _, fm := range fileMeta {
		content, err := s.fileStore.GetFile(ctx, skill.OwnerHandle, skill.Slug, latest.Version, fm.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("read file %s: %w", fm.Path, err)
		}
		files[fm.Path] = content
	}
	files[cleanPath] = req.Content

	newVersion := semver.BumpPatch(latest.Version)
	changelog := req.Changelog
	if changelog == "" {
		changelog = fmt.Sprintf("update %s", cleanPath)
	}

	pubReq := PublishRequest{
		Slug:           skill.Slug,
		Version:        newVersion,
		Changelog:      changelog,
		Files:          files,
		NamespaceSlug:  skill.NamespaceSlug,
		TokenNamespace: req.TokenNamespace,
	}
	if skill.DisplayName != nil {
		pubReq.DisplayName = *skill.DisplayName
	}
	if skill.Summary != nil {
		pubReq.Summary = *skill.Summary
	}
	pubReq.Category = skill.Category
	pubReq.Tags = []string(skill.Tags)

	return s.PublishVersion(ctx, user, pubReq)
}

// ReindexAll rebuilds the search index for all non-deleted skills.
// Intended for startup or admin-triggered reindex after schema changes.
func (s *SkillService) ReindexAll(ctx context.Context) (int, error) {
	if s.searchClient == nil {
		return 0, nil
	}
	skills, _, err := s.skillRepo.List(ctx, 10000, "", "created", repository.ListFilter{IsAdmin: true})
	if err != nil {
		return 0, fmt.Errorf("list skills for reindex: %w", err)
	}
	count := 0
	for i := range skills {
		sk := &skills[i]
		if s.indexSkill(ctx, sk, sk.NamespaceSlug, nil) {
			count++
		}
	}
	return count, nil
}

func (s *SkillService) indexSkill(ctx context.Context, skill *model.SkillWithOwner, namespaceSlug string, files map[string][]byte) bool {
	if s.searchClient == nil || skill == nil {
		return false
	}
	skillMdContent := ""
	if content, ok := files["SKILL.md"]; ok {
		skillMdContent = string(content)
	} else if skill.LatestVersionID != nil {
		if latest, err := s.versionRepo.GetByID(ctx, *skill.LatestVersionID); err == nil && latest != nil {
			if content, err := s.fileStore.GetFile(ctx, skill.OwnerHandle, skill.Slug, latest.Version, "SKILL.md"); err == nil {
				skillMdContent = string(content)
			}
		}
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
		NamespaceSlug:    namespaceSlug,
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
		s.loggerOrDefault().Warn("failed to index skill to search", "slug", skill.Slug, "err", err)
		return false
	}
	return true
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
