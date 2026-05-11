package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// Logging 返回一个把每个请求摘要打到 slog.Default() 的中间件，向后兼容旧调用点。
// 阶段 2 之后新代码请使用 LoggingWith 注入自定义 logger。
func Logging() gin.HandlerFunc {
	return LoggingWith(nil)
}

// LoggingWith returns a request-summary middleware that writes to the given logger.
// 传 nil 时回退到 slog.Default() —— 这是嵌入方“不需要单独 logger”时的常见路径。
func LoggingWith(lg *slog.Logger) gin.HandlerFunc {
	if lg == nil {
		lg = slog.Default()
	}
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		clientIP := c.ClientIP()
		requestID, _ := c.Get("request_id")
		errs := c.Errors.ByType(gin.ErrorTypePrivate).String()

		lg.Info("http request",
			"request_id", requestID,
			"method", method,
			"path", path,
			"status", status,
			"latency", latency,
			"client_ip", clientIP,
			"errors", errs,
		)
	}
}
