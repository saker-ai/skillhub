package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/cinience/skillhub/pkg/metrics"
)

// Metrics records request count + latency to Prometheus collectors.
//
// Uses gin's matched route template (c.FullPath()) as the route label so
// dynamic segments like /skills/:slug collapse to a single series.
// Falls back to "unmatched" so 404s do not explode cardinality with raw URLs.
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(c.Writer.Status())
		method := c.Request.Method
		metrics.HTTPRequests.WithLabelValues(method, route, status).Inc()
		metrics.HTTPDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	}
}
