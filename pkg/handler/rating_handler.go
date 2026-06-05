package handler

import (
	"net/http"
	"strconv"

	"github.com/cinience/skillhub/pkg/middleware"
	"github.com/cinience/skillhub/pkg/service"
	"github.com/gin-gonic/gin"
)

type RatingHandler struct {
	svc *service.RatingService
}

func NewRatingHandler(svc *service.RatingService) *RatingHandler {
	return &RatingHandler{svc: svc}
}

// Rate handles POST /api/v1/skills/:slug/ratings
func (h *RatingHandler) Rate(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	ref := extractSkillRef(c)
	var req struct {
		Score   int    `json:"score" binding:"required"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "score is required (1-5)"})
		return
	}

	if err := h.svc.Rate(c.Request.Context(), user.ID, ref, req.Score, req.Comment); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "rating saved"})
}

// List handles GET /api/v1/skills/:slug/ratings
func (h *RatingHandler) List(c *gin.Context) {
	ref := extractSkillRef(c)
	limit := 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}
	cursor := c.Query("cursor")

	ratings, nextCursor, err := h.svc.GetRatings(c.Request.Context(), ref, limit, cursor)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       ratings,
		"nextCursor": nextCursor,
	})
}

// Delete handles DELETE /api/v1/skills/:slug/ratings
func (h *RatingHandler) Delete(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	ref := extractSkillRef(c)
	if err := h.svc.DeleteRating(c.Request.Context(), user.ID, ref); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "rating deleted"})
}
