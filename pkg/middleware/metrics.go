package middleware

import (
	"strconv"
	"time"

	"github.com/saker-ai/skillhub/pkg/metrics"
	"github.com/gin-gonic/gin"
)

// Metrics 是 MetricsWith(metrics.Default) 的便捷调用，向后兼容旧调用点。
// 阶段 2 之后新代码请用 MetricsWith 显式注入实例。
func Metrics() gin.HandlerFunc {
	return MetricsWith(metrics.Default)
}

// MetricsWith records request count + latency to the provided *metrics.Metrics.
//
// 使用 gin 匹配后的路由模板（c.FullPath()）作为 route label，让 /skills/:slug 这类
// 动态段折叠成单一时间序列。404 fallback 为 "unmatched"，避免裸 URL 把基数打爆。
//
// 传 nil 时静默回退到 metrics.Default —— 让嵌入方在“不关心 metrics”时也能直接用。
func MetricsWith(m *metrics.Metrics) gin.HandlerFunc {
	if m == nil {
		m = metrics.Default
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(c.Writer.Status())
		method := c.Request.Method
		m.HTTPRequests.WithLabelValues(method, route, status).Inc()
		m.HTTPDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	}
}
