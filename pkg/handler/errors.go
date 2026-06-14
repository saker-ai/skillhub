package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/saker-ai/skillhub/pkg/service"
)

// writeServiceError maps a service-layer error to an HTTP status + JSON body.
//
// 之前所有 handler 一律把 service 错误映射成 400 BadRequest。这丢失了一个关键
// 区分：用户输入坏 vs 用户没权限。/skills.md 的 §6 明确承诺过 "team token
// 操作越界 → 403"，但实际行为返回 400，与文档相悖（P3-12）。
//
// 实现：service 层把鉴权失败包装成 fmt.Errorf("%w: ...", service.ErrForbidden, ...)
// —— sentinel + errors.Is 而非字符串前缀。这样后续修改文案不会让 403 退化成 400
// （这是上一版基于 strings.HasPrefix 的脆弱点）。
//
// 404 不在这里自动映射 —— service 返回的 "skill not found" 一般是 authz 通过后
// 没找到记录，handler 已经在调用点显式处理了（见 skill_handler.GetFile、
// comment_handler.go）。这里保持小而专一：只识别 ErrForbidden。
func writeServiceError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, service.ErrForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	var ambErr *service.AmbiguousSlugError
	if errors.As(err, &ambErr) {
		c.JSON(http.StatusConflict, gin.H{
			"error":      ambErr.Error(),
			"candidates": ambErr.Candidates,
		})
		return
	}
	if errors.Is(err, service.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

// writeInternalError 把内部错误统一映射成 HTTP 500 + 模糊响应体，并把详细错误
// 进 slog（带 op、route、远端 IP）。
//
// 为什么必须这么做：之前 14 个 handler 直接 c.JSON(500, gin.H{"error": err.Error()})
// 把数据库错误、文件路径、依赖库 stack frame 直接吐给客户端。这是经典的信息泄漏：
//   - DB 错误暴露表名 / 列名 / SQL 片段，喂给攻击者
//   - 文件系统错误暴露绝对路径（"/var/lib/skillhub/..."），泄漏部署结构
//   - 依赖库错误（gorm / go-redis / s3 SDK）暴露内部库版本，便于针对性攻击
//
// 客户端拿到详细错误也无济于事 —— 5xx 永远是"重试或联系运维"的语义。所以：
//   - 客户端: {"error":"internal error"}（恒定文案，不带任何上下文）
//   - 服务端: slog.Error 带 op / route / client_ip / err（运维要的全在这）
//
// op 是个简短的、调用方自定的"操作名"，用于日志检索（如 "list_namespaces"）。
// 不要把 op 暴露给客户端 —— 否则就把内部命名规范也泄漏了。
func writeInternalError(c *gin.Context, op string, err error) {
	if err == nil {
		return
	}
	slog.Default().Error("handler: internal error",
		"op", op,
		"route", c.FullPath(),
		"method", c.Request.Method,
		"client_ip", c.ClientIP(),
		"err", err,
	)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
}
