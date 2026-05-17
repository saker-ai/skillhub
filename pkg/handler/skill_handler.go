package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cinience/skillhub/pkg/middleware"
	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/service"
	"github.com/gin-gonic/gin"
)

type SkillHandler struct {
	svc    *service.SkillService
	logger *slog.Logger
}

func NewSkillHandler(svc *service.SkillService) *SkillHandler {
	return &SkillHandler{svc: svc}
}

// SetLogger 注入 *slog.Logger。nil 等价于走 slog.Default()。
// 与 SetMetrics 同样必须在 Server 装配阶段调用。
func (h *SkillHandler) SetLogger(lg *slog.Logger) {
	h.logger = lg
}

func (h *SkillHandler) loggerOrDefault() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// List handles GET /api/v1/skills
func (h *SkillHandler) List(c *gin.Context) {
	cursor := c.Query("cursor")
	sort := c.DefaultQuery("sort", "created")
	limit := 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}

	category := c.Query("category")
	viewer := middleware.GetUser(c)
	skills, nextCursor, err := h.svc.ListSkills(c.Request.Context(), limit, cursor, sort, category, viewer)
	if err != nil {
		h.loggerOrDefault().Error("ListSkills error", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       skills,
		"nextCursor": nextCursor,
	})
}

// Get handles GET /api/v1/skills/:slug
func (h *SkillHandler) Get(c *gin.Context) {
	slug := c.Param("slug")
	viewer := middleware.GetUser(c)
	skill, err := h.svc.GetSkill(c.Request.Context(), slug, viewer)
	if err != nil {
		h.loggerOrDefault().Error("GetSkill error", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if skill == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "skill not found"})
		return
	}
	c.JSON(http.StatusOK, skill)
}

// Publish handles POST /api/v1/skills
func (h *SkillHandler) Publish(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	if err := c.Request.ParseMultipartForm(50 << 20); err != nil { // 50MB limit
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid multipart form"})
		return
	}

	files, err := service.ReadMultipartFiles(c.Request.MultipartForm)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req := service.PublishRequest{
		Slug:           c.PostForm("slug"),
		Version:        c.PostForm("version"),
		Changelog:      c.PostForm("changelog"),
		DisplayName:    c.PostForm("displayName"),
		Summary:        c.PostForm("summary"),
		Category:       c.PostForm("category"),
		Kind:           c.PostForm("kind"),
		Visibility:     c.PostForm("visibility"),
		NamespaceSlug:  c.PostForm("namespace"),
		Files:          files,
		TokenNamespace: middleware.GetTokenNamespace(c),
	}
	if tags := c.PostForm("tags"); tags != "" {
		req.Tags = splitTags(tags)
	}
	if depsRaw := c.PostForm("dependencies"); depsRaw != "" {
		var deps []model.SkillDependency
		if err := json.Unmarshal([]byte(depsRaw), &deps); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dependencies JSON: " + err.Error()})
			return
		}
		req.Dependencies = deps
	}
	if sigRaw := c.PostForm("signature"); sigRaw != "" {
		// Cap bundle size: real cosign bundles are ~5–20 KB; reject anything
		// pathological to avoid filling the version row with junk.
		const maxSigBytes = 64 * 1024
		if len(sigRaw) > maxSigBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "signature bundle exceeds 64 KB"})
			return
		}
		req.SignatureBundle = []byte(sigRaw)
	}

	skill, version, err := h.svc.PublishVersion(c.Request.Context(), user, req)
	if err != nil {
		writeServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"skill":   skill,
		"version": version,
	})
}

// Delete handles DELETE /api/v1/skills/:slug
func (h *SkillHandler) Delete(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")

	if err := h.svc.SoftDelete(c.Request.Context(), user, slug, middleware.GetTokenNamespace(c)); err != nil {
		writeServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "skill deleted"})
}

func (h *SkillHandler) Purge(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")
	if err := h.svc.PurgeBySlug(c.Request.Context(), user, slug); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "skill purged"})
}

// Undelete handles POST /api/v1/skills/:slug/undelete
func (h *SkillHandler) Undelete(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")

	if err := h.svc.Undelete(c.Request.Context(), user, slug, middleware.GetTokenNamespace(c)); err != nil {
		writeServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "skill restored"})
}

// Versions handles GET /api/v1/skills/:slug/versions
func (h *SkillHandler) Versions(c *gin.Context) {
	slug := c.Param("slug")
	viewer := middleware.GetUser(c)
	versions, err := h.svc.GetVersions(c.Request.Context(), slug, viewer)
	if err != nil {
		h.loggerOrDefault().Error("GetVersions error", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": versions})
}

// Version handles GET /api/v1/skills/:slug/versions/:version
func (h *SkillHandler) Version(c *gin.Context) {
	slug := c.Param("slug")
	ver := c.Param("version")
	viewer := middleware.GetUser(c)

	version, err := h.svc.GetVersion(c.Request.Context(), slug, ver, viewer)
	if err != nil {
		writeInternalError(c, "get_skill_version", err)
		return
	}
	if version == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "version not found"})
		return
	}
	c.JSON(http.StatusOK, version)
}

// GetFile handles GET /api/v1/skills/:slug/file
func (h *SkillHandler) GetFile(c *gin.Context) {
	slug := c.Param("slug")
	version := c.DefaultQuery("version", "latest")
	path := c.Query("path")
	if path == "" {
		path = "SKILL.md"
	}
	viewer := middleware.GetUser(c)

	content, err := h.svc.GetFile(c.Request.Context(), slug, version, path, viewer)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.Data(http.StatusOK, "text/plain; charset=utf-8", content)
}

// YankVersion handles POST /api/v1/skills/:slug/versions/:version/yank
func (h *SkillHandler) YankVersion(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	// Reason is optional metadata, so an empty body (io.EOF) is fine — but a
	// malformed body should fail loudly so the caller learns their JSON is
	// broken instead of silently yanking with no reason recorded.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}

	if err := h.svc.YankVersion(c.Request.Context(), user, c.Param("slug"), c.Param("version"), req.Reason, middleware.GetTokenNamespace(c)); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "version yanked"})
}

// UnyankVersion handles DELETE /api/v1/skills/:slug/versions/:version/yank
func (h *SkillHandler) UnyankVersion(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	if err := h.svc.UnyankVersion(c.Request.Context(), user, c.Param("slug"), c.Param("version"), middleware.GetTokenNamespace(c)); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "version unyanked"})
}

// DeprecateVersion handles POST /api/v1/skills/:slug/versions/:version/deprecate
func (h *SkillHandler) DeprecateVersion(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	// Message is optional, so an empty body (io.EOF) is fine — but reject
	// malformed JSON instead of silently deprecating with no message.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}

	if err := h.svc.DeprecateVersion(c.Request.Context(), user, c.Param("slug"), c.Param("version"), req.Message, middleware.GetTokenNamespace(c)); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "version deprecated"})
}

// UndeprecateVersion handles DELETE /api/v1/skills/:slug/versions/:version/deprecate
func (h *SkillHandler) UndeprecateVersion(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	if err := h.svc.UndeprecateVersion(c.Request.Context(), user, c.Param("slug"), c.Param("version"), middleware.GetTokenNamespace(c)); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "version undeprecated"})
}

// RequestPublic handles POST /api/v1/skills/:slug/request-public
func (h *SkillHandler) RequestPublic(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")

	if err := h.svc.RequestPublic(c.Request.Context(), user, slug); err != nil {
		writeServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "public review requested"})
}

func splitTags(s string) []string {
	var tags []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool {
		return strings.ContainsRune(",; ", r)
	}) {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}
