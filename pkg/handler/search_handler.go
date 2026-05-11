package handler

import (
	"net/http"
	"strconv"

	"github.com/cinience/skillhub/pkg/metrics"
	"github.com/cinience/skillhub/pkg/search"
	"github.com/gin-gonic/gin"
)

type SearchHandler struct {
	searchClient *search.Client
	// metrics 由 SetMetrics 注入；nil 时走 metrics.Default。
	// 阶段 2 改造：避免直接访问包级全局变量。
	metrics *metrics.Metrics
}

func NewSearchHandler(sc *search.Client) *SearchHandler {
	return &SearchHandler{searchClient: sc}
}

// SetMetrics 注入 *metrics.Metrics 实例。nil 等价于走 metrics.Default。
func (h *SearchHandler) SetMetrics(m *metrics.Metrics) {
	h.metrics = m
}

// metricsOrDefault 返回当前注入的 metrics 实例；未注入时回退到 Default 单例。
func (h *SearchHandler) metricsOrDefault() *metrics.Metrics {
	if h.metrics != nil {
		return h.metrics
	}
	return metrics.Default
}

// Search handles GET /api/v1/search
func (h *SearchHandler) Search(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'q' is required"})
		return
	}

	limit := 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(c.Query("offset")); err == nil && o >= 0 {
		offset = o
	}

	var sortFields []string
	if s := c.Query("sort"); s != "" {
		sortFields = []string{s}
	}

	filters := "visibility = public AND moderationStatus = approved AND isDeleted = false"

	result, err := h.searchClient.Search(c.Request.Context(), query, limit, offset, sortFields, filters)
	if err != nil {
		writeInternalError(c, "search_skills", err)
		return
	}

	h.metricsOrDefault().SearchQueries.Inc()
	c.JSON(http.StatusOK, result)
}
