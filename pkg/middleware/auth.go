package middleware

import (
	"net/http"

	"github.com/cinience/skillhub/pkg/auth"
	"github.com/cinience/skillhub/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	UserContextKey    = "user"
	TokenScopeKey     = "token_scope"
	TokenNamespaceKey = "token_namespace_id" // *uuid.UUID — 当前 token 绑定的 namespace；仅在 team token 时存在
)

// RequireAuth 强制要求请求必须携带可被 IdentityProvider 解析的合法身份。
//
// 阶段 5 重构：参数由 *auth.Service 改为 auth.IdentityProvider 接口。
// 由于 *auth.Service 实现了 IdentityProvider，旧调用点 RequireAuth(s.authSvc)
// 仍然能编译；嵌入方可以传入自定义的 IdentityProvider 来对接宿主鉴权。
func RequireAuth(idp auth.IdentityProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, scope, nsID := identify(c, idp)
		if user == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			c.Abort()
			return
		}
		c.Set(UserContextKey, user)
		if scope != "" {
			c.Set(TokenScopeKey, scope)
		}
		if nsID != nil {
			c.Set(TokenNamespaceKey, nsID)
		}
		c.Next()
	}
}

// OptionalAuth 在请求附带身份时填充 user/scope，否则透明放行。
//
// 与 RequireAuth 同样接收 IdentityProvider，便于嵌入方注入自定义实现。
func OptionalAuth(idp auth.IdentityProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, scope, nsID := identify(c, idp)
		if user != nil {
			c.Set(UserContextKey, user)
			if scope != "" {
				c.Set(TokenScopeKey, scope)
			}
			if nsID != nil {
				c.Set(TokenNamespaceKey, nsID)
			}
		}
		c.Next()
	}
}

func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u, exists := c.Get(UserContextKey)
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			c.Abort()
			return
		}
		user := u.(*model.User)
		for _, role := range roles {
			if user.Role == role {
				c.Next()
				return
			}
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
		c.Abort()
	}
}

// identify 把 IdentityProvider 错误一律视作"未认证"，
// 不向调用方暴露 DB / 解析细节。错误日志由 IdentityProvider 实现侧负责。
func identify(c *gin.Context, idp auth.IdentityProvider) (*model.User, string, *uuid.UUID) {
	if idp == nil {
		return nil, "", nil
	}
	user, scope, nsID, err := idp.Identify(c.Request.Context(), c.Request)
	if err != nil || user == nil {
		return nil, "", nil
	}
	return user, scope, nsID
}

// RequireScope checks that the token scope allows the current request method.
// Scope rules: "read" → GET only; "publish" → GET + POST /skills; "full" → all.
func RequireScope(allowedScopes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		scopeVal, exists := c.Get(TokenScopeKey)
		if !exists {
			// No scope set means session cookie (full access)
			c.Next()
			return
		}
		scope := scopeVal.(string)
		if scope == "full" {
			c.Next()
			return
		}

		method := c.Request.Method

		switch scope {
		case "read":
			if method != "GET" && method != "HEAD" {
				c.JSON(http.StatusForbidden, gin.H{"error": "token scope 'read' only allows GET requests"})
				c.Abort()
				return
			}
		case "publish":
			if method != "GET" && method != "HEAD" {
				if method != "POST" {
					c.JSON(http.StatusForbidden, gin.H{"error": "token scope 'publish' does not allow this method"})
					c.Abort()
					return
				}
				// Restrict POST to skill publish/star routes only
				path := c.FullPath()
				if path != "/api/v1/skills" && path != "/api/v1/stars/:slug" {
					c.JSON(http.StatusForbidden, gin.H{"error": "token scope 'publish' only allows publishing skills"})
					c.Abort()
					return
				}
			}
		}

		c.Next()
	}
}

// GetUser retrieves the authenticated user from the context.
func GetUser(c *gin.Context) *model.User {
	u, exists := c.Get(UserContextKey)
	if !exists {
		return nil
	}
	return u.(*model.User)
}

// GetTokenNamespace 返回当前请求 token 绑定的 namespace ID。
//
// 返回 nil 的情况：
//   - 未认证 / 用 cookie 登录（session 不绑定 namespace）；
//   - 用 personal token 调用；
//   - 嵌入方注入的 IdentityProvider 没返回 namespace。
//
// 调用方语义：返回非 nil 即"本次请求是团队 token"，写操作必须把目标 skill 限制在
// 该 namespace 下。读操作可忽略本字段。
func GetTokenNamespace(c *gin.Context) *uuid.UUID {
	v, exists := c.Get(TokenNamespaceKey)
	if !exists {
		return nil
	}
	id, ok := v.(*uuid.UUID)
	if !ok {
		return nil
	}
	return id
}
