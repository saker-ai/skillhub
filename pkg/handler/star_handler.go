package handler

import (
	"net/http"

	"github.com/saker-ai/skillhub/pkg/middleware"
	"github.com/saker-ai/skillhub/pkg/service"
	"github.com/gin-gonic/gin"
)

type StarHandler struct {
	svc *service.SkillService
}

func NewStarHandler(svc *service.SkillService) *StarHandler {
	return &StarHandler{svc: svc}
}

// Star handles POST /api/v1/stars/:slug
func (h *StarHandler) Star(c *gin.Context) {
	user := middleware.GetUser(c)
	ref := extractSkillRef(c)

	if err := h.svc.Star(c.Request.Context(), user.ID, ref); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "starred"})
}

// Unstar handles DELETE /api/v1/stars/:slug
func (h *StarHandler) Unstar(c *gin.Context) {
	user := middleware.GetUser(c)
	ref := extractSkillRef(c)

	if err := h.svc.Unstar(c.Request.Context(), user.ID, ref); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "unstarred"})
}
