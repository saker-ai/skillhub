package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/cinience/skillhub/pkg/middleware"
	"github.com/cinience/skillhub/pkg/service"
)

type NamespaceHandler struct {
	svc *service.NamespaceService
}

func NewNamespaceHandler(svc *service.NamespaceService) *NamespaceHandler {
	return &NamespaceHandler{svc: svc}
}

// Create handles POST /api/v1/namespaces
func (h *NamespaceHandler) Create(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	var req struct {
		Slug        string `json:"slug" binding:"required"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Type        string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug is required"})
		return
	}

	ns, err := h.svc.Create(c.Request.Context(), user, req.Slug, req.DisplayName, req.Description, req.Type)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, ns)
}

// List handles GET /api/v1/namespaces
func (h *NamespaceHandler) List(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	namespaces, err := h.svc.ListByUser(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": namespaces})
}

// Get handles GET /api/v1/namespaces/:slug
func (h *NamespaceHandler) Get(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	ns, err := h.svc.GetBySlug(c.Request.Context(), slug)
	if err != nil || ns == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "namespace not found"})
		return
	}

	// Check membership or admin
	if !h.svc.IsMemberOrAdmin(c.Request.Context(), ns.ID, user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	c.JSON(http.StatusOK, ns)
}

// Update handles PUT /api/v1/namespaces/:slug
func (h *NamespaceHandler) Update(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	var req struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	ns, err := h.svc.Update(c.Request.Context(), user, slug, req.DisplayName, req.Description)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, ns)
}

// ListMembers handles GET /api/v1/namespaces/:slug/members
func (h *NamespaceHandler) ListMembers(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")

	// Check membership or admin before listing
	ns, err := h.svc.GetBySlug(c.Request.Context(), slug)
	if err != nil || ns == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "namespace not found"})
		return
	}
	if !h.svc.IsMemberOrAdmin(c.Request.Context(), ns.ID, user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	members, err := h.svc.ListMembers(c.Request.Context(), slug)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": members})
}

// AddMember handles POST /api/v1/namespaces/:slug/members
func (h *NamespaceHandler) AddMember(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	var req struct {
		Handle string `json:"handle" binding:"required"`
		Role   string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "handle is required"})
		return
	}

	if err := h.svc.AddMember(c.Request.Context(), user, slug, req.Handle, req.Role); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "member added"})
}

// RemoveMember handles DELETE /api/v1/namespaces/:slug/members/:handle
func (h *NamespaceHandler) RemoveMember(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	handle := c.Param("handle")

	if err := h.svc.RemoveMember(c.Request.Context(), user, slug, handle); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "member removed"})
}

// TransferOwnership handles POST /api/v1/namespaces/:slug/transfer
func (h *NamespaceHandler) TransferOwnership(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	var req struct {
		NewOwner string `json:"newOwner" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "newOwner is required"})
		return
	}

	if err := h.svc.TransferOwnership(c.Request.Context(), user, slug, req.NewOwner); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ownership transferred"})
}

// Leave handles POST /api/v1/namespaces/:slug/leave
func (h *NamespaceHandler) Leave(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	if err := h.svc.Leave(c.Request.Context(), user, slug); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "left namespace"})
}

// Delete handles DELETE /api/v1/namespaces/:slug
func (h *NamespaceHandler) Delete(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	if err := h.svc.Delete(c.Request.Context(), user, slug); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "namespace deleted"})
}

// Invite handles POST /api/v1/namespaces/:slug/invitations
func (h *NamespaceHandler) Invite(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	var req struct {
		Handle  string `json:"handle" binding:"required"`
		Role    string `json:"role"`
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "handle is required"})
		return
	}

	inv, err := h.svc.Invite(c.Request.Context(), user, slug, req.Handle, req.Role, req.Message)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, inv)
}

// ListInvitations handles GET /api/v1/namespaces/:slug/invitations
func (h *NamespaceHandler) ListInvitations(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	status := c.Query("status")
	invs, err := h.svc.ListInvitations(c.Request.Context(), user, slug, status)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": invs})
}

// RevokeInvitation handles DELETE /api/v1/namespaces/:slug/invitations/:id
func (h *NamespaceHandler) RevokeInvitation(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	slug := c.Param("slug")
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid invitation id"})
		return
	}

	if err := h.svc.RevokeInvitation(c.Request.Context(), user, slug, id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "invitation revoked"})
}

// MyInvitations handles GET /api/v1/invitations
func (h *NamespaceHandler) MyInvitations(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	invs, err := h.svc.ListMyInvitations(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": invs})
}

// AcceptInvitation handles POST /api/v1/invitations/:id/accept
func (h *NamespaceHandler) AcceptInvitation(c *gin.Context) {
	h.respondInvitation(c, true)
}

// DeclineInvitation handles POST /api/v1/invitations/:id/decline
func (h *NamespaceHandler) DeclineInvitation(c *gin.Context) {
	h.respondInvitation(c, false)
}

func (h *NamespaceHandler) respondInvitation(c *gin.Context, accept bool) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid invitation id"})
		return
	}

	if err := h.svc.RespondToInvitation(c.Request.Context(), user, id, accept); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	verb := "declined"
	if accept {
		verb = "accepted"
	}
	c.JSON(http.StatusOK, gin.H{"message": "invitation " + verb})
}
