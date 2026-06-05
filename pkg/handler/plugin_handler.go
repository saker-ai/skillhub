package handler

import (
	"fmt"
	"io"
	"net/http"

	"github.com/cinience/skillhub/pkg/middleware"
	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/repository"
	"github.com/cinience/skillhub/pkg/service"
	"github.com/gin-gonic/gin"
)

type PluginHandler struct {
	svc *service.PluginService
}

func NewPluginHandler(svc *service.PluginService) *PluginHandler {
	return &PluginHandler{svc: svc}
}

// POST /api/v1/plugins
func (h *PluginHandler) Publish(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	if err := c.Request.ParseMultipartForm(64 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid multipart form: " + err.Error()})
		return
	}

	input, err := h.svc.ParseMultipartPublish(c.Request.MultipartForm)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	input.OwnerID = user.ID
	input.User = user
	if ns := c.PostForm("namespace"); ns != "" {
		input.NamespaceSlug = ns
	}

	result, err := h.svc.Publish(c.Request.Context(), *input)
	if err != nil {
		writeServiceError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"plugin":  result.Plugin,
		"version": result.Version,
	})
}

// GET /api/v1/plugins
func (h *PluginHandler) List(c *gin.Context) {
	opts := repository.PluginListOptions{
		Category: c.Query("category"),
		Sort:     c.Query("sort"),
		Cursor:   c.Query("cursor"),
		Limit:    queryInt(c, "limit", 20),
	}

	plugins, nextCursor, err := h.svc.List(c.Request.Context(), opts)
	if err != nil {
		writeInternalError(c, "list_plugins", err)
		return
	}

	data := make([]gin.H, 0, len(plugins))
	for _, p := range plugins {
		data = append(data, pluginToJSON(p))
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       data,
		"nextCursor": nextCursor,
	})
}

// GET /api/v1/plugins/:slug
func (h *PluginHandler) Get(c *gin.Context) {
	slug := c.Param("slug")
	p, err := h.svc.Get(c.Request.Context(), slug)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, pluginToJSON(*p))
}

// GET /api/v1/plugins/:slug/versions
func (h *PluginHandler) Versions(c *gin.Context) {
	slug := c.Param("slug")
	versions, err := h.svc.Versions(c.Request.Context(), slug)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": versions})
}

// GET /api/v1/plugins/file?slug=x&version=y&path=z
func (h *PluginHandler) GetFile(c *gin.Context) {
	slug := c.Query("slug")
	version := c.Query("version")
	filePath := c.Query("path")

	if slug == "" || filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug and path are required"})
		return
	}

	data, err := h.svc.GetFile(c.Request.Context(), slug, version, filePath)
	if err != nil {
		writeServiceError(c, err)
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Writer.Write(data)
}

// GET /api/v1/plugins/download?slug=x&version=y
func (h *PluginHandler) Download(c *gin.Context) {
	slug := c.Query("slug")
	version := c.Query("version")

	if slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug is required"})
		return
	}

	reader, etag, err := h.svc.Download(c.Request.Context(), slug, version)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	defer reader.Close()

	if etag != "" {
		c.Header("ETag", etag)
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", slug+".zip"))
	io.Copy(c.Writer, reader)
}

func pluginToJSON(p model.PluginWithOwner) gin.H {
	result := gin.H{
		"id":          p.ID,
		"slug":        p.Slug,
		"visibility":  p.Visibility,
		"category":    p.Category,
		"tags":        p.Tags,
		"downloads":   p.Downloads,
		"starsCount":  p.StarsCount,
		"ownerHandle": p.OwnerHandle,
		"createdAt":   p.CreatedAt,
		"updatedAt":   p.UpdatedAt,
	}
	if p.DisplayName != nil {
		result["displayName"] = *p.DisplayName
	}
	if p.Summary != nil {
		result["summary"] = *p.Summary
	}
	if p.LatestVersionID != nil {
		result["latestVersionId"] = *p.LatestVersionID
	}

	return result
}

func (h *PluginHandler) Delete(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")
	if err := h.svc.SoftDelete(c.Request.Context(), user, slug); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "plugin deleted"})
}

func (h *PluginHandler) Undelete(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")
	if err := h.svc.Undelete(c.Request.Context(), user, slug); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "plugin restored"})
}

func (h *PluginHandler) YankVersion(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")
	version := c.Param("version")
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	if err := h.svc.YankVersion(c.Request.Context(), user, slug, version, body.Reason); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "version yanked"})
}

func (h *PluginHandler) UnyankVersion(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	slug := c.Param("slug")
	version := c.Param("version")
	if err := h.svc.UnyankVersion(c.Request.Context(), user, slug, version); err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "version unyanked"})
}

func queryInt(c *gin.Context, key string, def int) int {
	v := c.Query(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return def
	}
	if n > 100 {
		n = 100
	}
	return n
}
