package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// List handles GET /api/agent/skills?Page=N&PageSize=M.
func (h *AgentSkillHandler) List(c *gin.Context) {
	page := parseIntDefault(c.Query("Page"), 1)
	pageSize := parseIntDefault(c.Query("PageSize"), 100)
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
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
			c.JSON(http.StatusInternalServerError, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": err.Error()})
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
		"RequestId": agentRequestID(c),
		"Items":     out,
		"Total":     total,
		"Page":      page,
		"PageSize":  pageSize,
	})
}

// Detail handles GET /api/agent/skills/:id.
func (h *AgentSkillHandler) Detail(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "invalid skill id"})
		return
	}

	viewer := middleware.GetUser(c)
	skill, ver, err := h.svc.ResolveVersionByID(c.Request.Context(), id, "latest", viewer)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "skill not found"})
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
		"RequestId": agentRequestID(c),
		"Data":      mapSkillToAgentDetail(skill, ver, skillMdContent),
	})
}

// Download handles GET /api/agent/skills/:id/download — returns a JSON body
// with DownloadUrl pointing to the /archive sibling endpoint.
func (h *AgentSkillHandler) Download(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "invalid skill id"})
		return
	}

	viewer := middleware.GetUser(c)
	_, ver, err := h.svc.ResolveVersionByID(c.Request.Context(), id, "latest", viewer)
	if err != nil || ver == nil {
		c.JSON(http.StatusNotFound, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "skill not found"})
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
		"RequestId":   agentRequestID(c),
		"DownloadUrl": archiveURL,
	})
}

// Archive handles GET /api/agent/skills/:id/archive — streams the zip directly.
func (h *AgentSkillHandler) Archive(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "invalid skill id"})
		return
	}

	viewer := middleware.GetUser(c)
	skill, ver, err := h.svc.ResolveVersionByID(c.Request.Context(), id, "latest", viewer)
	if err != nil || ver == nil {
		c.JSON(http.StatusNotFound, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "skill not found"})
		return
	}

	ref := model.SkillRef{Namespace: skill.NamespaceSlug, Slug: skill.Slug}
	result, err := h.svc.Download(c.Request.Context(), ref, ver.Version, "", viewer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "download failed"})
		return
	}
	defer result.Archive.Close()

	buf, err := io.ReadAll(io.LimitReader(result.Archive, agentSkillMaxArchiveBytes+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "read archive failed"})
		return
	}
	if len(buf) > agentSkillMaxArchiveBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"RequestId": agentRequestID(c), "ErrorMessage": "archive too large"})
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
	ID          string    `json:"Id"`
	Name        string    `json:"Name"`
	Description string    `json:"Description"`
	Detail      string    `json:"Detail"`
	Type        string    `json:"Type"`
	Scope       string    `json:"Scope"`
	UserID      string    `json:"UserId"`
	Nickname    string    `json:"Nickname"`
	Status      string    `json:"Status"`
	SkillhubID  string    `json:"SkillhubId"`
	GmtCreate   time.Time `json:"GmtCreate"`
	GmtModified time.Time `json:"GmtModified"`
}

type agentSkillDetail struct {
	ID             string    `json:"Id"`
	Name           string    `json:"Name"`
	Description    string    `json:"Description"`
	Detail         string    `json:"Detail"`
	Type           string    `json:"Type"`
	Scope          string    `json:"Scope"`
	UserID         string    `json:"UserId"`
	Nickname       string    `json:"Nickname"`
	WorkspaceID    string    `json:"WorkspaceId"`
	Status         string    `json:"Status"`
	SkillhubID     string    `json:"SkillhubId"`
	SkillMdContent string    `json:"SkillMdContent"`
	GmtCreate      time.Time `json:"GmtCreate"`
	GmtModified    time.Time `json:"GmtModified"`
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
		GmtCreate:   skill.CreatedAt,
		GmtModified: skill.UpdatedAt,
	}
}

func mapSkillToAgentDetail(skill *model.SkillWithOwner, ver *model.SkillVersion, skillMdContent string) agentSkillDetail {
	status := "ACTIVE"
	if skill.SoftDeletedAt != nil {
		status = "DELETED"
	}
	return agentSkillDetail{
		ID:             skill.ID.String(),
		Name:           skill.Slug,
		Description:    stringOrEmpty(skill.Summary),
		Type:           mapKindToType(skill.Kind),
		Nickname:       skill.OwnerHandle,
		Status:         status,
		SkillhubID:     agentSkillhubID(skill),
		SkillMdContent: skillMdContent,
		GmtCreate:      skill.CreatedAt,
		GmtModified:    skill.UpdatedAt,
	}
}

func agentSkillhubID(skill *model.SkillWithOwner) string {
	if skill.NamespaceSlug != "" {
		return "@" + skill.NamespaceSlug + "/" + skill.Slug
	}
	return "@" + skill.Slug
}

func mapKindToType(kind string) string {
	switch strings.ToLower(kind) {
	case "builtin":
		return "BUILTIN"
	default:
		return "NORMAL"
	}
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
