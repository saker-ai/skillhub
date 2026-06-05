package middleware

import "github.com/gin-gonic/gin"

// DeprecateRoute adds RFC 8594 Deprecation and Sunset headers to signal
// that a route will be removed in the future. Clients should migrate to
// namespace-qualified paths (e.g. /api/v1/skills/@namespace/slug).
func DeprecateRoute(sunset string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Deprecation", "true")
		if sunset != "" {
			c.Header("Sunset", sunset)
		}
		c.Next()
	}
}
