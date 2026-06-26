package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/middleware"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/service"
)

const agentSkillMaxArchiveBytes = 20 << 20

// AgentSkillHandler implements the /api/agent/skills loading API consumed by
// Saker's AgentSkillClientAdapter.
type AgentSkillHandler struct {
	svc *service.SkillService
}

func NewAgentSkillHandler(svc *service.SkillService) *AgentSkillHandler {
	return &AgentSkillHandler{svc: svc}
}

// List handles GET /api/agent/skills?page=N&pageSize=M.
func (h *AgentSkillHandler) List(c *gin.Context) {
	page := parseIntDefault(c.Query("page"), 1)
	pageSize := parseIntDefault(c.Query("pageSize"), 20)
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}

	viewer := middleware.GetUser(c)

	var items []model.SkillWithOwner
	cursor := ""
	for i := 0; i < page; i++ {
		var next string
		var err error
		items, next, err = h.svc.ListSkills(c.Request.Context(), pageSize, cursor, "updated", "", "", viewer)
		if err != nil {
			writeAgentError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
			return
		}
		if i < page-1 {
			if next == "" {
				items = nil
				break
			}
			cursor = next
		} else {
			cursor = next
		}
	}

	total := (page-1)*pageSize + len(items)
	if len(items) == pageSize && cursor != "" {
		total = page*pageSize + 1
	}

	out := make([]agentSkillItem, 0, len(items))
	for i := range items {
		skill := &items[i]
		ver, _ := h.latestVersion(c, skill)
		out = append(out, mapSkillToAgentItem(skill, ver))
	}

	c.JSON(http.StatusOK, gin.H{
		"data": out,
		"pagination": gin.H{
			"pageSize": pageSize,
			"total":    total,
			"hasMore":  len(items) == pageSize && cursor != "",
		},
		"requestId": agentRequestID(c),
	})
}

// Detail handles GET /api/agent/skills/:id.
func (h *AgentSkillHandler) Detail(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		writeAgentError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid skill id", nil)
		return
	}

	viewer := middleware.GetUser(c)
	skill, ver, err := h.svc.ResolveVersionByID(c.Request.Context(), id, "latest", viewer)
	if err != nil {
		writeAgentError(c, http.StatusNotFound, "SKILL_NOT_FOUND", "skill not found", gin.H{"id": id.String()})
		return
	}

	var skillMdContent string
	if ver != nil {
		ref := model.SkillRef{Namespace: skill.NamespaceSlug, Slug: skill.Slug}
		content, err := h.svc.GetFile(c.Request.Context(), ref, ver.Version, "SKILL.md", viewer)
		if err == nil {
			skillMdContent = string(content)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      mapSkillToAgentDetail(skill, ver, skillMdContent),
		"requestId": agentRequestID(c),
	})
}

// Download handles GET /api/agent/skills/:id/download.
func (h *AgentSkillHandler) Download(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		writeAgentError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid skill id", nil)
		return
	}

	viewer := middleware.GetUser(c)
	_, ver, err := h.svc.ResolveVersionByID(c.Request.Context(), id, "latest", viewer)
	if err != nil || ver == nil {
		writeAgentError(c, http.StatusNotFound, "SKILL_NOT_FOUND", "skill not found", gin.H{"id": id.String()})
		return
	}

	scheme := "https"
	if c.Request.TLS == nil && strings.HasPrefix(c.Request.Host, "localhost") || strings.HasPrefix(c.Request.Host, "127.0.0.1") {
		scheme = "http"
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	archiveURL := fmt.Sprintf("%s://%s/api/agent/skills/%s/archive", scheme, c.Request.Host, id.String())

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"downloadUrl": archiveURL,
		},
		"requestId": agentRequestID(c),
	})
}

// Publish handles POST /api/agent/skills with the simplified Agent Skill API.
func (h *AgentSkillHandler) Publish(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		writeAgentError(c, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", nil)
		return
	}

	if err := c.Request.ParseMultipartForm(50 << 20); err != nil {
		writeAgentError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid multipart form", nil)
		return
	}

	files, err := service.ReadMultipartFiles(c.Request.MultipartForm)
	if err != nil {
		code := "INVALID_REQUEST"
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "exceeds max size") {
			code = "ARCHIVE_TOO_LARGE"
			status = http.StatusRequestEntityTooLarge
		}
		writeAgentError(c, status, code, err.Error(), nil)
		return
	}
	if len(files) == 0 {
		writeAgentError(c, http.StatusBadRequest, "MISSING_FILES", "missing files", nil)
		return
	}
	if _, ok := files["SKILL.md"]; !ok {
		writeAgentError(c, http.StatusBadRequest, "INVALID_SKILL_MD", "SKILL.md is required", nil)
		return
	}

	req := service.PublishRequest{
		Slug:           strings.TrimSpace(c.PostForm("slug")),
		Version:        strings.TrimSpace(c.PostForm("version")),
		DisplayName:    strings.TrimSpace(c.PostForm("displayName")),
		Visibility:     strings.TrimSpace(c.PostForm("visibility")),
		Files:          files,
		TokenNamespace: middleware.GetTokenNamespace(c),
	}
	if overwrite, ok := parseAgentOverwrite(c.PostForm("overwrite")); ok {
		req.Overwrite = &overwrite
	}

	skill, version, err := h.svc.PublishVersion(c.Request.Context(), user, req)
	if err != nil {
		writeAgentServiceError(c, err, req.Slug)
		return
	}

	status := http.StatusOK
	if skill.VersionsCount == 0 && skill.LatestVersionID == nil {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{
		"data": gin.H{
			"skill": gin.H{
				"slug": skill.Slug,
			},
			"version": gin.H{
				"version":     version.Version,
				"fingerprint": version.Fingerprint,
			},
		},
		"requestId": agentRequestID(c),
	})
}

// Archive handles GET /api/agent/skills/:id/archive — streams the zip directly.
func (h *AgentSkillHandler) Archive(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		writeAgentError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid skill id", nil)
		return
	}

	viewer := middleware.GetUser(c)
	skill, ver, err := h.svc.ResolveVersionByID(c.Request.Context(), id, "latest", viewer)
	if err != nil || ver == nil {
		writeAgentError(c, http.StatusNotFound, "SKILL_NOT_FOUND", "skill not found", gin.H{"id": id.String()})
		return
	}

	ref := model.SkillRef{Namespace: skill.NamespaceSlug, Slug: skill.Slug}
	result, err := h.svc.Download(c.Request.Context(), ref, ver.Version, "", viewer)
	if err != nil {
		writeAgentError(c, http.StatusBadGateway, "BACKEND_ERROR", "download failed", nil)
		return
	}
	defer result.Archive.Close()

	buf, err := io.ReadAll(io.LimitReader(result.Archive, agentSkillMaxArchiveBytes+1))
	if err != nil {
		writeAgentError(c, http.StatusBadGateway, "BACKEND_ERROR", "read archive failed", nil)
		return
	}
	if len(buf) > agentSkillMaxArchiveBytes {
		writeAgentError(c, http.StatusRequestEntityTooLarge, "ARCHIVE_TOO_LARGE", "archive too large", nil)
		return
	}

	sum := sha256.Sum256(buf)
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": result.Filename}))
	c.Header("X-Content-SHA256", hex.EncodeToString(sum[:]))
	c.Data(http.StatusOK, "application/zip", buf)
}

func (h *AgentSkillHandler) latestVersion(c *gin.Context, skill *model.SkillWithOwner) (*model.SkillVersion, error) {
	ref := model.SkillRef{Namespace: skill.NamespaceSlug, Slug: skill.Slug}
	_, ver, err := h.svc.ResolveVersion(c.Request.Context(), ref, "latest", middleware.GetUser(c))
	return ver, err
}

// --- response mapping ---

type agentSkillItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Nickname    string `json:"nickname"`
	Status      string `json:"status"`
	SkillhubID  string `json:"skillhubId"`
}

type agentSkillDetail struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Detail      string `json:"detail"`
	Type        string `json:"type"`
	Nickname    string `json:"nickname"`
	Status      string `json:"status"`
	SkillhubID  string `json:"skillhubId"`
	SkillMd     string `json:"skillMd"`
}

func mapSkillToAgentItem(skill *model.SkillWithOwner, ver *model.SkillVersion) agentSkillItem {
	status := "ACTIVE"
	if skill.SoftDeletedAt != nil {
		status = "DELETED"
	}
	return agentSkillItem{
		ID:          skill.ID.String(),
		Name:        skill.Slug,
		Description: stringOrEmpty(skill.Summary),
		Type:        mapKindToType(skill.Kind),
		Nickname:    skill.OwnerHandle,
		Status:      status,
		SkillhubID:  agentSkillhubID(skill),
	}
}

func mapSkillToAgentDetail(skill *model.SkillWithOwner, ver *model.SkillVersion, skillMdContent string) agentSkillDetail {
	status := "ACTIVE"
	if skill.SoftDeletedAt != nil {
		status = "DELETED"
	}
	return agentSkillDetail{
		ID:          skill.ID.String(),
		Name:        skill.Slug,
		Description: stringOrEmpty(skill.Summary),
		Detail:      stringOrEmpty(skill.Summary),
		Type:        mapKindToType(skill.Kind),
		Nickname:    skill.OwnerHandle,
		Status:      status,
		SkillhubID:  agentSkillhubID(skill),
		SkillMd:     skillMdContent,
	}
}

func agentSkillhubID(skill *model.SkillWithOwner) string {
	if skill.NamespaceSlug != "" {
		return "@" + skill.NamespaceSlug + "/" + skill.Slug
	}
	return "@" + skill.Slug
}

func mapKindToType(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return "custom"
	}
	return kind
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func parseIntDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func agentRequestID(c *gin.Context) string {
	if id := c.GetHeader(middleware.RequestIDHeader); id != "" {
		return id
	}
	return "req_" + uuid.NewString()
}

type agentErrorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeAgentError(c *gin.Context, status int, code, message string, details map[string]any) {
	c.JSON(status, gin.H{
		"error": agentErrorPayload{
			Code:    code,
			Message: message,
			Details: details,
		},
		"requestId": agentRequestID(c),
	})
}

func writeAgentServiceError(c *gin.Context, err error, slug string) {
	if errors.Is(err, service.ErrForbidden) {
		writeAgentError(c, http.StatusForbidden, "FORBIDDEN", err.Error(), nil)
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		writeAgentError(c, http.StatusNotFound, "SKILL_NOT_FOUND", "skill not found", gin.H{"slug": slug})
		return
	}
	if errors.Is(err, service.ErrConflict) {
		writeAgentError(c, http.StatusConflict, "SKILL_NAME_CONFLICT", err.Error(), gin.H{"slug": slug})
		return
	}
	msg := err.Error()
	code := "INVALID_REQUEST"
	if strings.Contains(strings.ToLower(msg), "skill.md") {
		code = "INVALID_SKILL_MD"
	}
	writeAgentError(c, http.StatusBadRequest, code, msg, nil)
}

func parseAgentOverwrite(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return true, false
	case "true", "1", "yes", "y":
		return true, true
	case "false", "0", "no", "n":
		return false, true
	default:
		return true, false
	}
}
