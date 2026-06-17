package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/saker-ai/skillhub/pkg/middleware"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/service"
)

// AgentSkillHandler exposes ordinary SkillHub skills through the Agent Skill API.
type AgentSkillHandler struct {
	svc *service.SkillService
}

func NewAgentSkillHandler(svc *service.SkillService) *AgentSkillHandler {
	return &AgentSkillHandler{svc: svc}
}

// AgentSkillList handles GET /api/agent/skills.
func (h *AgentSkillHandler) List(c *gin.Context) {
	page := positiveInt(c.DefaultQuery("Page", "1"), 1)
	pageSize := positiveInt(c.DefaultQuery("PageSize", "100"), 100)
	if pageSize > 100 {
		pageSize = 100
	}

	viewer := middleware.GetUser(c)
	offset := (page - 1) * pageSize
	skills, err := h.listVisible(c.Request.Context(), offset+pageSize, viewer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	end := offset + pageSize
	if offset > len(skills) {
		offset = len(skills)
	}
	if end > len(skills) {
		end = len(skills)
	}

	items := make([]agentSkillListItem, 0, end-offset)
	for _, skill := range skills[offset:end] {
		item := agentSkillListItemFromSkill(skill)
		item.DownloadURL = h.downloadURL(c, skill)
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"RequestId": requestID(c),
		"Items":     items,
		"Total":     len(skills),
		"Page":      page,
		"PageSize":  pageSize,
	})
}

// Get handles GET /api/agent/skills/:id.
func (h *AgentSkillHandler) Get(c *gin.Context) {
	skill, ok := h.lookup(c)
	if !ok {
		return
	}

	skillMD, err := h.svc.GetFile(c.Request.Context(), skillRef(skill), "latest", "SKILL.md", middleware.GetUser(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	detail := agentSkillDetailFromSkill(skill)
	detail.SkillMdContent = string(skillMD)
	detail.DownloadURL = h.downloadURL(c, skill)
	c.JSON(http.StatusOK, gin.H{
		"RequestId": requestID(c),
		"Data":      detail,
	})
}

// Download handles GET /api/agent/skills/:id/download.
func (h *AgentSkillHandler) Download(c *gin.Context) {
	skill, ok := h.lookup(c)
	if !ok {
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"RequestId":   requestID(c),
		"DownloadUrl": h.downloadURL(c, skill),
	})
}

func (h *AgentSkillHandler) lookup(c *gin.Context) (model.SkillWithOwner, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid agent skill id"})
		return model.SkillWithOwner{}, false
	}

	viewer := middleware.GetUser(c)
	skills, err := h.listVisible(c.Request.Context(), 10000, viewer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return model.SkillWithOwner{}, false
	}
	for _, skill := range skills {
		if uint64(agentSkillID(skill)) == id {
			return skill, true
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "skill not found"})
	return model.SkillWithOwner{}, false
}

func (h *AgentSkillHandler) listVisible(ctx context.Context, limit int, viewer *model.User) ([]model.SkillWithOwner, error) {
	var out []model.SkillWithOwner
	cursor := ""
	for len(out) < limit {
		batch, next, err := h.svc.ListSkills(ctx, 100, cursor, "created", "", "", viewer)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (h *AgentSkillHandler) downloadURL(c *gin.Context, skill model.SkillWithOwner) string {
	values := url.Values{}
	values.Set("slug", skill.Slug)
	values.Set("version", "latest")
	if skill.NamespaceSlug != "" {
		values.Set("namespace", skill.NamespaceSlug)
	}
	return requestBaseURL(c) + "/api/v1/download?" + values.Encode()
}

func agentSkillListItemFromSkill(skill model.SkillWithOwner) agentSkillListItem {
	return agentSkillListItem{
		ID:          agentSkillID(skill),
		Name:        displayName(skill),
		Description: stringValue(skill.Summary),
		Detail:      stringValue(skill.Summary),
		Type:        skill.Kind,
		Scope:       visibilityScope(skill.Visibility),
		UserID:      skill.OwnerID.String(),
		Nickname:    skill.OwnerHandle,
		Status:      agentSkillStatus(skill),
		SkillhubID:  skillRefString(skill),
		GmtCreate:   skill.CreatedAt,
		GmtModified: skill.UpdatedAt,
	}
}

func agentSkillDetailFromSkill(skill model.SkillWithOwner) agentSkillDetail {
	item := agentSkillListItemFromSkill(skill)
	return agentSkillDetail{
		ID:          item.ID,
		Name:        item.Name,
		Description: item.Description,
		Detail:      item.Detail,
		Type:        item.Type,
		Scope:       item.Scope,
		UserID:      item.UserID,
		Nickname:    item.Nickname,
		WorkspaceID: skill.NamespaceSlug,
		Status:      item.Status,
		SkillhubID:  skillRefString(skill),
		GmtCreate:   item.GmtCreate,
		GmtModified: item.GmtModified,
	}
}

func agentSkillID(skill model.SkillWithOwner) uint {
	id := crc32.ChecksumIEEE([]byte(skillRefString(skill))) & 0x7fffffff
	if id == 0 {
		id = 1
	}
	return uint(id)
}

func skillRef(skill model.SkillWithOwner) model.SkillRef {
	return model.SkillRef{Namespace: skill.NamespaceSlug, Slug: skill.Slug}
}

func skillRefString(skill model.SkillWithOwner) string {
	if skill.NamespaceSlug != "" {
		return "@" + skill.NamespaceSlug + "/" + skill.Slug
	}
	return skill.Slug
}

func displayName(skill model.SkillWithOwner) string {
	if skill.DisplayName != nil && strings.TrimSpace(*skill.DisplayName) != "" {
		return *skill.DisplayName
	}
	return skill.Slug
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func visibilityScope(visibility string) string {
	if visibility == "public" {
		return "PUBLIC"
	}
	return "PRIVATE"
}

func agentSkillStatus(skill model.SkillWithOwner) string {
	if skill.SoftDeletedAt != nil {
		return "DELETED"
	}
	return "PUBLISHED"
}

func requestBaseURL(c *gin.Context) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	return scheme + "://" + host
}

func requestID(c *gin.Context) string {
	if id := c.GetHeader("X-Request-ID"); id != "" {
		return id
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", time.Now().UnixNano(), c.Request.URL.String())))
	return hex.EncodeToString(sum[:8])
}

func positiveInt(raw string, fallback int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

type agentSkillListItem struct {
	ID          uint      `json:"Id"`
	Name        string    `json:"Name"`
	Description string    `json:"Description"`
	Detail      string    `json:"Detail"`
	Type        string    `json:"Type"`
	Scope       string    `json:"Scope"`
	UserID      string    `json:"UserId"`
	Nickname    string    `json:"Nickname"`
	Status      string    `json:"Status"`
	SkillhubID  string    `json:"SkillhubId"`
	DownloadURL string    `json:"DownloadUrl,omitempty"`
	GmtCreate   time.Time `json:"GmtCreate"`
	GmtModified time.Time `json:"GmtModified"`
}

type agentSkillDetail struct {
	ID             uint      `json:"Id"`
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
	DownloadURL    string    `json:"DownloadUrl,omitempty"`
	GmtCreate      time.Time `json:"GmtCreate"`
	GmtModified    time.Time `json:"GmtModified"`
}
