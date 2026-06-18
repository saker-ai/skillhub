package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/middleware"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/service"
)

const (
	runtimeManifestInclude = "manifest"
	runtimeSkillMdInclude  = "skillMd"
	runtimeMaxBundleBytes  = 100 << 20
)

// RuntimeSkillHandler exposes the runtime-first skill loading API consumed by Saker.
type RuntimeSkillHandler struct {
	svc *service.SkillService
}

func NewRuntimeSkillHandler(svc *service.SkillService) *RuntimeSkillHandler {
	return &RuntimeSkillHandler{svc: svc}
}

func (h *RuntimeSkillHandler) Capabilities(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"runtimeSkills": gin.H{
			"version": "2026-06-17",
			"resolve": true,
			"files":   true,
			"bundle":  true,
		},
	})
}

func (h *RuntimeSkillHandler) Resolve(c *gin.Context) {
	var req runtimeResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "invalid resolve request", gin.H{"cause": err.Error()})
		return
	}
	if len(req.Refs) == 0 {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "refs is required", nil)
		return
	}
	if len(req.Refs) > 100 {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "too many refs", gin.H{"max": 100})
		return
	}

	include := parseRuntimeInclude(strings.Join(req.Include, ","))
	viewer := middleware.GetUser(c)
	results := make([]runtimeResolveResult, 0, len(req.Refs))
	for i, ref := range req.Refs {
		result := runtimeResolveResult{Index: i, Ref: ref}
		skill, ver, err := h.resolveRef(c, ref, viewer)
		if err != nil {
			result.Error = runtimeErrorFromError(err)
			results = append(results, result)
			continue
		}
		out, err := h.runtimeSkillResponse(c, skill, ver, include)
		if err != nil {
			result.Error = runtimeErrorFromError(err)
			results = append(results, result)
			continue
		}
		result.Skill = out
		results = append(results, result)
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}

func (h *RuntimeSkillHandler) List(c *gin.Context) {
	limit := runtimeLimit(c.DefaultQuery("limit", "100"))
	order := c.DefaultQuery("order", "updated_at_desc")
	sortKey := runtimeSortKey(order)
	viewer := middleware.GetUser(c)
	skills, next, err := h.svc.ListSkills(c.Request.Context(), limit, c.Query("cursor"), sortKey, "", c.Query("owner"), viewer)
	if err != nil {
		writeRuntimeError(c, http.StatusInternalServerError, "temporarily_unavailable", "list skills failed", nil)
		return
	}

	data := make([]runtimeListSkill, 0, len(skills))
	for i := range skills {
		_, ver, err := h.svc.ResolveVersion(c.Request.Context(), skillRefFromSkill(skills[i]), "latest", viewer)
		if err != nil || ver == nil || ver.YankedAt != nil {
			continue
		}
		manifest, err := buildRuntimeManifest(ver)
		if err != nil {
			continue
		}
		data = append(data, runtimeListSkill{
			ID:          skills[i].ID.String(),
			Slug:        runtimeSlug(skills[i]),
			Name:        skills[i].Slug,
			DisplayName: displayName(skills[i]),
			Description: stringValue(skills[i].Summary),
			Kind:        skills[i].Kind,
			Version:     ver.Version,
			Digest:      manifest.ManifestDigest,
			Status:      "active",
			UpdatedAt:   skills[i].UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": data, "nextCursor": next})
}

func (h *RuntimeSkillHandler) ByID(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "invalid skill id", nil)
		return
	}
	skill, ver, err := h.resolveQueryRef(c, model.SkillRef{}, &id, c.Query("version"), middleware.GetUser(c), c.Query("digest"))
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	h.writeSingleSkill(c, skill, ver)
}

func (h *RuntimeSkillHandler) BySlug(c *gin.Context) {
	ref, ok := parseRuntimeSlug(c.Query("slug"))
	if !ok {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "slug is required", nil)
		return
	}
	skill, ver, err := h.resolveQueryRef(c, ref, nil, c.Query("version"), middleware.GetUser(c), c.Query("digest"))
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	h.writeSingleSkill(c, skill, ver)
}

func (h *RuntimeSkillHandler) File(c *gin.Context) {
	ref, id, ok := runtimeQueryRef(c)
	if !ok {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "id or slug is required", nil)
		return
	}
	filePath, ok := cleanRuntimeFilePath(c.Query("path"))
	if !ok {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "invalid file path", nil)
		return
	}
	skill, ver, err := h.resolveQueryRef(c, ref, id, c.Query("version"), middleware.GetUser(c))
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	manifest, err := buildRuntimeManifest(ver)
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	if !digestMatches(c.Query("digest"), manifest.ManifestDigest) {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "digest does not match resolved version", nil)
		return
	}
	file, ok := manifest.file(filePath)
	if !ok {
		writeRuntimeError(c, http.StatusNotFound, "file_not_found", "file not found", nil)
		return
	}
	etag := quoteETag("sha256:" + file.SHA256)
	if matchesETag(c.GetHeader("If-None-Match"), "sha256:"+file.SHA256) {
		c.Header("ETag", etag)
		c.Status(http.StatusNotModified)
		return
	}
	content, err := h.svc.GetFile(c.Request.Context(), skillRefFromSkill(*skill), ver.Version, filePath, middleware.GetUser(c))
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	mediaType := file.MediaType
	if mediaType == "" {
		mediaType = detectMediaType(filePath, content)
	}
	c.Header("ETag", etag)
	c.Header("X-Saker-File-SHA256", file.SHA256)
	c.Header("Cache-Control", runtimeCacheControl(c.Query("digest")))
	c.Data(http.StatusOK, mediaType, content)
}

func (h *RuntimeSkillHandler) Bundle(c *gin.Context) {
	ref, id, ok := runtimeQueryRef(c)
	if !ok {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "id or slug is required", nil)
		return
	}
	format := c.DefaultQuery("format", "zip")
	if format != "zip" {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "unsupported bundle format", gin.H{"format": format})
		return
	}
	skill, ver, err := h.resolveQueryRef(c, ref, id, c.Query("version"), middleware.GetUser(c))
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	manifest, err := buildRuntimeManifest(ver)
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	if !digestMatches(c.Query("digest"), manifest.ManifestDigest) {
		writeRuntimeError(c, http.StatusBadRequest, "invalid_ref", "digest does not match resolved version", nil)
		return
	}
	result, err := h.svc.Download(c.Request.Context(), skillRefFromSkill(*skill), ver.Version, "", middleware.GetUser(c))
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	defer result.Archive.Close()
	buf, err := io.ReadAll(io.LimitReader(result.Archive, runtimeMaxBundleBytes+1))
	if err != nil {
		writeRuntimeError(c, http.StatusInternalServerError, "temporarily_unavailable", "read bundle failed", nil)
		return
	}
	if len(buf) > runtimeMaxBundleBytes {
		writeRuntimeError(c, http.StatusRequestEntityTooLarge, "bundle_too_large", "bundle too large", gin.H{"maxBytes": runtimeMaxBundleBytes})
		return
	}
	sum := sha256.Sum256(buf)
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": result.Filename}))
	c.Header("X-Saker-Bundle-SHA256", hex.EncodeToString(sum[:]))
	c.Header("X-Saker-Skill-Digest", manifest.ManifestDigest)
	c.Data(http.StatusOK, "application/zip", buf)
}

func (h *RuntimeSkillHandler) writeSingleSkill(c *gin.Context, skill *model.SkillWithOwner, ver *model.SkillVersion) {
	include := parseRuntimeInclude(c.Query("include"))
	out, err := h.runtimeSkillResponse(c, skill, ver, include)
	if err != nil {
		writeRuntimeErrorFromError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *RuntimeSkillHandler) resolveRef(c *gin.Context, ref runtimeSkillRef, viewer *model.User) (*model.SkillWithOwner, *model.SkillVersion, error) {
	if strings.TrimSpace(ref.ID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(ref.ID))
		if err != nil {
			return nil, nil, runtimeHTTPError{status: http.StatusBadRequest, code: "invalid_ref", message: "invalid skill id"}
		}
		skill, ver, err := h.resolveQueryRef(c, model.SkillRef{}, &id, ref.Version, viewer, ref.Digest)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(ref.Slug) != "" {
			parsed, ok := parseRuntimeSlug(ref.Slug)
			if !ok || parsed.Namespace != skill.NamespaceSlug || parsed.Slug != skill.Slug {
				return nil, nil, runtimeHTTPError{status: http.StatusBadRequest, code: "invalid_ref", message: "id and slug refer to different skills"}
			}
		}
		return skill, ver, nil
	}
	parsed, ok := parseRuntimeSlug(ref.Slug)
	if !ok {
		return nil, nil, runtimeHTTPError{status: http.StatusBadRequest, code: "invalid_ref", message: "id or slug is required"}
	}
	return h.resolveQueryRef(c, parsed, nil, ref.Version, viewer, ref.Digest)
}

func (h *RuntimeSkillHandler) resolveQueryRef(c *gin.Context, ref model.SkillRef, id *uuid.UUID, version string, viewer *model.User, digest ...string) (*model.SkillWithOwner, *model.SkillVersion, error) {
	var skill *model.SkillWithOwner
	var ver *model.SkillVersion
	var err error
	if id != nil {
		skill, ver, err = h.svc.ResolveVersionByID(c.Request.Context(), *id, version, viewer)
	} else {
		skill, ver, err = h.svc.ResolveVersion(c.Request.Context(), ref, version, viewer)
	}
	if err != nil {
		return nil, nil, err
	}
	if ver == nil {
		return nil, nil, runtimeHTTPError{status: http.StatusNotFound, code: "version_not_found", message: "version not found"}
	}
	if ver.YankedAt != nil {
		return nil, nil, runtimeHTTPError{status: http.StatusGone, code: "version_yanked", message: "version yanked"}
	}
	if len(digest) > 0 && strings.TrimSpace(digest[0]) != "" {
		manifest, err := buildRuntimeManifest(ver)
		if err != nil {
			return nil, nil, err
		}
		if !digestMatches(digest[0], manifest.ManifestDigest) {
			return nil, nil, runtimeHTTPError{status: http.StatusBadRequest, code: "invalid_ref", message: "digest does not match resolved version"}
		}
	}
	return skill, ver, nil
}

func (h *RuntimeSkillHandler) runtimeSkillResponse(c *gin.Context, skill *model.SkillWithOwner, ver *model.SkillVersion, include map[string]bool) (*runtimeSkill, error) {
	if skill == nil || ver == nil {
		return nil, runtimeHTTPError{status: http.StatusNotFound, code: "skill_not_found", message: "skill not found"}
	}
	manifest, err := buildRuntimeManifest(ver)
	if err != nil {
		return nil, err
	}
	out := &runtimeSkill{
		ID:          skill.ID.String(),
		Slug:        runtimeSlug(*skill),
		Name:        skill.Slug,
		DisplayName: displayName(*skill),
		Description: stringValue(skill.Summary),
		Kind:        skill.Kind,
		Version:     ver.Version,
		Digest:      manifest.ManifestDigest,
		Status:      "active",
		ResolvedAt:  time.Now().UTC(),
	}
	if include[runtimeManifestInclude] {
		out.Manifest = manifest
	}
	if include[runtimeSkillMdInclude] {
		content, err := h.svc.GetFile(c.Request.Context(), skillRefFromSkill(*skill), ver.Version, "SKILL.md", middleware.GetUser(c))
		if err != nil {
			return nil, err
		}
		out.SkillMd = string(content)
	}
	return out, nil
}

type runtimeResolveRequest struct {
	Refs    []runtimeSkillRef `json:"refs"`
	Include []string          `json:"include"`
}

type runtimeSkillRef struct {
	ID      string `json:"id,omitempty"`
	Slug    string `json:"slug,omitempty"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

type runtimeResolveResult struct {
	Index int               `json:"index"`
	Ref   runtimeSkillRef   `json:"ref"`
	Skill *runtimeSkill     `json:"skill,omitempty"`
	Error *runtimeErrorBody `json:"error,omitempty"`
}

type runtimeListSkill struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	Description string    `json:"description"`
	Kind        string    `json:"kind"`
	Version     string    `json:"version"`
	Digest      string    `json:"digest"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type runtimeSkill struct {
	ID          string           `json:"id"`
	Slug        string           `json:"slug"`
	Name        string           `json:"name"`
	DisplayName string           `json:"displayName"`
	Description string           `json:"description"`
	Kind        string           `json:"kind"`
	Version     string           `json:"version"`
	Digest      string           `json:"digest"`
	Status      string           `json:"status"`
	ResolvedAt  time.Time        `json:"resolvedAt"`
	Manifest    *runtimeManifest `json:"manifest,omitempty"`
	SkillMd     string           `json:"skillMd,omitempty"`
}

type runtimeManifest struct {
	Entrypoint     string              `json:"entrypoint"`
	ManifestDigest string              `json:"manifestDigest"`
	Files          []runtimeFile       `json:"files"`
	Dependencies   []runtimeDependency `json:"dependencies,omitempty"`
}

func (m *runtimeManifest) file(path string) (runtimeFile, bool) {
	for _, file := range m.Files {
		if file.Path == path {
			return file, true
		}
	}
	return runtimeFile{}, false
}

type runtimeFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"mediaType"`
}

type runtimeDependency struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

type runtimeErrorBody struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	RequestID  string         `json:"requestId,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	RetryAfter string         `json:"retryAfter,omitempty"`
}

type runtimeHTTPError struct {
	status  int
	code    string
	message string
	details map[string]any
}

func (e runtimeHTTPError) Error() string { return e.message }

func buildRuntimeManifest(ver *model.SkillVersion) (*runtimeManifest, error) {
	var files []model.VersionFile
	if err := json.Unmarshal(ver.Files, &files); err != nil {
		return nil, fmt.Errorf("parse version files: %w", err)
	}
	out := &runtimeManifest{Entrypoint: "SKILL.md"}
	for _, file := range files {
		cleaned, ok := cleanRuntimeFilePath(file.Path)
		if !ok {
			continue
		}
		out.Files = append(out.Files, runtimeFile{
			Path:      cleaned,
			Size:      file.Size,
			SHA256:    file.SHA256,
			MediaType: mediaTypeForPath(cleaned, file.ContentType),
		})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	if _, ok := out.file("SKILL.md"); !ok {
		return nil, runtimeHTTPError{status: http.StatusNotFound, code: "file_not_found", message: "SKILL.md not found"}
	}
	var deps []model.SkillDependency
	if len(ver.Dependencies) > 0 {
		_ = json.Unmarshal(ver.Dependencies, &deps)
	}
	for _, dep := range deps {
		slug := dep.Slug
		if dep.Namespace != "" {
			slug = dep.Namespace + "/" + dep.Slug
		}
		out.Dependencies = append(out.Dependencies, runtimeDependency{Slug: slug, Version: dep.Version})
	}
	out.ManifestDigest = computeRuntimeManifestDigest(out)
	return out, nil
}

func computeRuntimeManifestDigest(manifest *runtimeManifest) string {
	canonical := struct {
		Entrypoint   string              `json:"entrypoint"`
		Files        []runtimeFile       `json:"files"`
		Dependencies []runtimeDependency `json:"dependencies,omitempty"`
	}{
		Entrypoint:   manifest.Entrypoint,
		Files:        append([]runtimeFile(nil), manifest.Files...),
		Dependencies: append([]runtimeDependency(nil), manifest.Dependencies...),
	}
	sort.Slice(canonical.Files, func(i, j int) bool { return canonical.Files[i].Path < canonical.Files[j].Path })
	sort.Slice(canonical.Dependencies, func(i, j int) bool {
		if canonical.Dependencies[i].Slug == canonical.Dependencies[j].Slug {
			return canonical.Dependencies[i].Version < canonical.Dependencies[j].Version
		}
		return canonical.Dependencies[i].Slug < canonical.Dependencies[j].Slug
	})
	data, _ := json.Marshal(canonical)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func parseRuntimeInclude(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out[part] = true
		}
	}
	return out
}

func parseRuntimeSlug(raw string) (model.SkillRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model.SkillRef{}, false
	}
	raw, _ = url.QueryUnescape(raw)
	raw = strings.TrimPrefix(raw, "@")
	if idx := strings.IndexByte(raw, '/'); idx > 0 && idx < len(raw)-1 {
		return model.SkillRef{Namespace: raw[:idx], Slug: raw[idx+1:]}, true
	}
	return model.SkillRef{Slug: raw}, true
}

func runtimeQueryRef(c *gin.Context) (model.SkillRef, *uuid.UUID, bool) {
	if rawID := strings.TrimSpace(c.Query("id")); rawID != "" {
		id, err := uuid.Parse(rawID)
		if err != nil {
			return model.SkillRef{}, nil, false
		}
		return model.SkillRef{}, &id, true
	}
	ref, ok := parseRuntimeSlug(c.Query("slug"))
	return ref, nil, ok
}

func cleanRuntimeFilePath(raw string) (string, bool) {
	if raw == "" || strings.Contains(raw, "\\") || strings.ContainsRune(raw, 0) {
		return "", false
	}
	cleaned := path.Clean(raw)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return cleaned, true
}

func digestMatches(want, got string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	if !strings.HasPrefix(want, "sha256:") {
		want = "sha256:" + want
	}
	return strings.EqualFold(want, got)
}

func runtimeSlug(skill model.SkillWithOwner) string {
	if skill.NamespaceSlug != "" {
		return skill.NamespaceSlug + "/" + skill.Slug
	}
	return skill.Slug
}

func displayName(skill model.SkillWithOwner) string {
	if value := stringValue(skill.DisplayName); value != "" {
		return value
	}
	return skill.Slug
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func skillRefFromSkill(skill model.SkillWithOwner) model.SkillRef {
	return model.SkillRef{Namespace: skill.NamespaceSlug, Slug: skill.Slug}
}

func runtimeLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 100
	}
	if n > 100 {
		return 100
	}
	return n
}

func runtimeSortKey(order string) string {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "updated_at_desc", "updated":
		return "updated"
	case "name":
		return "name"
	default:
		return "updated"
	}
}

func mediaTypeForPath(filePath, existing string) string {
	if existing != "" {
		return existing
	}
	switch strings.ToLower(path.Ext(filePath)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".py":
		return "text/x-python"
	case ".sh":
		return "text/x-shellscript"
	default:
		return "application/octet-stream"
	}
}

func detectMediaType(filePath string, content []byte) string {
	if mt := mediaTypeForPath(filePath, ""); mt != "application/octet-stream" {
		return mt
	}
	return http.DetectContentType(content)
}

func runtimeCacheControl(digest string) string {
	if strings.TrimSpace(digest) != "" {
		return "public, max-age=31536000, immutable"
	}
	return "private, max-age=300"
}

func writeRuntimeErrorFromError(c *gin.Context, err error) {
	status, body := runtimeErrorFromErrorStatus(err)
	if status >= 500 && err != nil {
		_ = c.Error(err)
	}
	c.JSON(status, gin.H{"error": body})
}

func writeRuntimeError(c *gin.Context, status int, code, message string, details map[string]any) {
	body := &runtimeErrorBody{
		Code:      code,
		Message:   message,
		RequestID: runtimeRequestID(c),
		Details:   details,
	}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		body.RetryAfter = c.GetHeader("Retry-After")
	}
	c.JSON(status, gin.H{"error": body})
}

func runtimeErrorFromError(err error) *runtimeErrorBody {
	_, body := runtimeErrorFromErrorStatus(err)
	body.RequestID = ""
	return body
}

func runtimeErrorFromErrorStatus(err error) (int, *runtimeErrorBody) {
	var rtErr runtimeHTTPError
	if errors.As(err, &rtErr) {
		return rtErr.status, &runtimeErrorBody{Code: rtErr.code, Message: rtErr.message, Details: rtErr.details}
	}
	msg := "internal error"
	code := "temporarily_unavailable"
	status := http.StatusInternalServerError
	if err != nil {
		lower := strings.ToLower(err.Error())
		switch {
		case strings.Contains(lower, "not found"):
			status, code, msg = http.StatusNotFound, "skill_not_found", "skill not found"
		case strings.Contains(lower, "too large"):
			status, code, msg = http.StatusRequestEntityTooLarge, "bundle_too_large", err.Error()
		case strings.Contains(lower, "version"):
			status, code, msg = http.StatusNotFound, "version_not_found", "version not found"
		case strings.Contains(lower, "file"):
			status, code, msg = http.StatusNotFound, "file_not_found", "file not found"
		case strings.Contains(lower, "invalid"):
			status, code, msg = http.StatusBadRequest, "invalid_ref", err.Error()
		}
	}
	return status, &runtimeErrorBody{Code: code, Message: msg}
}

func runtimeRequestID(c *gin.Context) string {
	if v, ok := c.Get("request_id"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if id := c.GetHeader(middleware.RequestIDHeader); id != "" {
		return id
	}
	return requestID(c)
}

func requestID(c *gin.Context) string {
	id := "req_" + uuid.NewString()
	c.Set("request_id", id)
	return id
}
