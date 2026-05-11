package handler

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/cinience/skillhub/pkg/auth"
	"github.com/cinience/skillhub/pkg/metrics"
	"github.com/cinience/skillhub/pkg/middleware"
	"github.com/cinience/skillhub/pkg/repository"
	"github.com/cinience/skillhub/pkg/service"
)

var validScopes = []string{"full", "read", "publish"}

// validTeamScopes is a strict subset of validScopes — team tokens must NOT carry
// "full" because that scope grants user-account-level operations (token CRUD
// for the underlying user, etc.) which a namespace-bound credential should
// never grant. A leaked team token with "full" scope is essentially a leaked
// user session for the issuer.
var validTeamScopes = []string{"read", "publish"}

// maxTeamTokenLifetime caps team-token expiresIn at 365 days. Indefinite team
// tokens routinely outlive employment / project scope; forcing renewal at most
// yearly is a cheap defense against stale credentials. Personal tokens are
// unaffected — the user owns them and can revoke at will.
const maxTeamTokenLifetime = 365 * 24 * time.Hour

// maxTeamTokensPerNamespace caps the number of *active* (non-revoked) team
// tokens per namespace. Picked at 50 because:
//   - leaves headroom for one CI runner per microservice in a medium org
//     (typical fleets cluster around 10-30 active credentials per team)
//   - low enough that an automated mint-loop trips the limit within minutes,
//     before the namespace's token table grows into the thousands
// Owners can rotate by revoking old tokens; we do not auto-purge expired ones
// from this count because the repo's CountActiveByNamespace already filters
// revoked_at IS NULL but does NOT filter on expiresAt — that's intentional,
// because an expired-but-not-revoked token is still a credential the issuer
// chose to leave around (and it shows up in their list).
const maxTeamTokensPerNamespace = 50

// defaultListLimit is the page size used by ListForNamespace when no ?limit=
// is specified. Aligned with the repo's own default cap; keeps the JSON
// payload modest for the common case of a single API render.
const defaultListLimit = 20

type TokenHandler struct {
	authSvc   *auth.Service
	tokenRepo *repository.TokenRepo
	nsSvc     *service.NamespaceService // 团队 token 端点用；nil 时禁用 /namespaces/:slug/tokens
	auditSvc  *service.AuditService     // 可选；nil 时跳过审计日志记录
	metrics   *metrics.Metrics          // 可选；nil 时回退到 metrics.Default
}

func NewTokenHandler(authSvc *auth.Service, tokenRepo *repository.TokenRepo, nsSvc *service.NamespaceService) *TokenHandler {
	return &TokenHandler{authSvc: authSvc, tokenRepo: tokenRepo, nsSvc: nsSvc}
}

// SetAuditService wires the audit service. When wired, team-token create/revoke
// emit AuditLog rows so security review and incident response can replay
// "who minted what for which namespace, when, from which IP".
//
// 之所以做成可选 setter：保持 NewTokenHandler 签名稳定，与 NamespaceService 的
// SetInvitationRepo / SetTokenRepo 风格一致。
func (h *TokenHandler) SetAuditService(s *service.AuditService) {
	h.auditSvc = s
}

// SetMetrics injects a *metrics.Metrics instance. nil falls back to metrics.Default.
func (h *TokenHandler) SetMetrics(m *metrics.Metrics) {
	h.metrics = m
}

func (h *TokenHandler) metricsOrDefault() *metrics.Metrics {
	if h.metrics != nil {
		return h.metrics
	}
	return metrics.Default
}

// List handles GET /api/v1/tokens — list current user's tokens.
func (h *TokenHandler) List(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	tokens, err := h.tokenRepo.GetByUserID(c.Request.Context(), user.ID)
	if err != nil {
		writeInternalError(c, "list_user_tokens", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": tokens})
}

// Create handles POST /api/v1/tokens — create a new token for the current user.
func (h *TokenHandler) Create(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	var req struct {
		Label     string `json:"label"`
		Scope     string `json:"scope"`
		ExpiresIn string `json:"expiresIn"` // e.g. "720h" for 30 days
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	scope := req.Scope
	if scope == "" {
		scope = "full"
	}
	if !slices.Contains(validScopes, scope) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid scope, must be one of: full, read, publish"})
		return
	}

	var expiresIn time.Duration
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || d <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid expiresIn duration"})
			return
		}
		expiresIn = d
	}

	rawToken, token, err := h.authSvc.CreateToken(c.Request.Context(), user.ID, req.Label, scope, expiresIn)
	if err != nil {
		writeInternalError(c, "create_user_token", err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"token":    rawToken,
		"metadata": token,
	})
}

// Revoke handles DELETE /api/v1/tokens/:id — revoke a token owned by the current user.
func (h *TokenHandler) Revoke(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	tokenID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token ID"})
		return
	}

	token, err := h.tokenRepo.GetByID(c.Request.Context(), tokenID)
	if err != nil {
		writeInternalError(c, "revoke_user_token_lookup", err)
		return
	}
	if token == nil || token.UserID != user.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
		return
	}

	if err := h.tokenRepo.Revoke(c.Request.Context(), tokenID); err != nil {
		writeInternalError(c, "revoke_user_token", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "token revoked"})
}

// ============================================================================
// Namespace (team) tokens — POST/GET/DELETE /api/v1/namespaces/:slug/tokens
//
// 设计要点：
//   - 写权限要求 owner 或 admin 角色（普通 member 只能用、不能签发）；
//   - token 的 author 仍是签发它的用户（便于审计）；
//   - 用此 token 调用写接口时，目标 skill 必须挂在该 namespace 下，否则被
//     SkillService 层拒绝（见 Phase C）；
//   - 列表只显示该 namespace 下未吊销的 token；
//   - 撤销允许：系统 admin / namespace owner|admin / token 创建者本人。
// ============================================================================

// CreateForNamespace handles POST /api/v1/namespaces/:slug/tokens.
func (h *TokenHandler) CreateForNamespace(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	if h.nsSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "namespace service unavailable"})
		return
	}

	slug := c.Param("slug")
	ns, err := h.nsSvc.GetBySlug(c.Request.Context(), slug)
	if err != nil || ns == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "namespace not found"})
		return
	}
	if !h.nsSvc.CanManageTokens(c.Request.Context(), ns.ID, user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only namespace owner/admin can manage tokens"})
		return
	}

	var req struct {
		Label     string `json:"label"`
		Scope     string `json:"scope"`
		ExpiresIn string `json:"expiresIn"` // e.g. "720h"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	scope := req.Scope
	if scope == "" {
		// Default to "publish" — least privilege that still lets the team-token
		// do its primary job. "full" is intentionally not the default for team
		// tokens; see validTeamScopes for the rationale.
		scope = "publish"
	}
	if !slices.Contains(validTeamScopes, scope) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid team-token scope, must be one of: read, publish"})
		return
	}

	var expiresIn time.Duration
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || d <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid expiresIn duration"})
			return
		}
		if d > maxTeamTokenLifetime {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expiresIn exceeds maximum team-token lifetime of 365d"})
			return
		}
		expiresIn = d
	} else {
		// Required, not optional: never-expiring team tokens are exactly what we
		// are defending against. Reject empty rather than silently defaulting,
		// so a forgetful CLI user gets a clear 400 instead of an immortal token.
		c.JSON(http.StatusBadRequest, gin.H{"error": "expiresIn is required for team tokens (max 365d)"})
		return
	}

	// Quota check — done after validation but before mint, so a malformed
	// request still gets the more informative 400 rather than a quota 409.
	// 409 (Conflict) over 429 (Too Many Requests): this is a steady-state cap on
	// resources, not a rate limit; the client's correct response is "revoke an
	// old token", not "back off and retry".
	activeCount, err := h.tokenRepo.CountActiveByNamespace(c.Request.Context(), ns.ID)
	if err != nil {
		writeInternalError(c, "ns_token_count_active", err)
		return
	}
	if activeCount >= maxTeamTokensPerNamespace {
		h.metricsOrDefault().TeamTokenQuotaRejected.Inc()
		c.JSON(http.StatusConflict, gin.H{
			"error": "namespace already has the maximum number of active team tokens; revoke an unused token before creating a new one",
			"limit": maxTeamTokensPerNamespace,
			"count": activeCount,
		})
		return
	}

	rawToken, token, err := h.authSvc.CreateNamespaceToken(c.Request.Context(), user.ID, ns.ID, req.Label, scope, expiresIn)
	if err != nil {
		writeInternalError(c, "ns_token_create", err)
		return
	}

	if h.auditSvc != nil {
		// details encodes namespace slug + scope so reviewers don't have to
		// dereference token.NamespaceID against a possibly-already-deleted ns row.
		h.auditSvc.Log(c.Request.Context(), &user.ID, "team_token_create", "api_token", &token.ID,
			"namespace="+slug+",scope="+scope, c.ClientIP())
	}
	h.metricsOrDefault().TeamTokenCreated.WithLabelValues(scope).Inc()

	c.JSON(http.StatusCreated, gin.H{
		"token":    rawToken,
		"metadata": token,
	})
}

// ListForNamespace handles GET /api/v1/namespaces/:slug/tokens.
//
// Query params:
//   - limit  : page size (1..100). Default defaultListLimit. Out-of-range
//              values fall back to the default rather than 400 — a paged list
//              should be tolerant of typos in tooling.
//   - cursor : opaque continuation token from a previous response's
//              `nextCursor`. Empty for the first page.
//
// Response shape:
//
//	{ "data": [...], "nextCursor": "..." }   // nextCursor omitted on last page
//
// The shape is additive to the previous { "data": [...] }: existing clients
// that ignore unknown fields keep working, and the absence of `nextCursor`
// signals "no more pages" so callers don't need to compare lengths.
func (h *TokenHandler) ListForNamespace(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	if h.nsSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "namespace service unavailable"})
		return
	}

	slug := c.Param("slug")
	ns, err := h.nsSvc.GetBySlug(c.Request.Context(), slug)
	if err != nil || ns == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "namespace not found"})
		return
	}
	if !h.nsSvc.CanManageTokens(c.Request.Context(), ns.ID, user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only namespace owner/admin can list tokens"})
		return
	}

	limit := defaultListLimit
	if v := c.Query("limit"); v != "" {
		// strconv.Atoi is fine here — repo's GetByNamespacePaged clamps the
		// value into a sane range (1..100), so a hostile huge number can't
		// blow up the query plan.
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			limit = n
		}
	}
	cursor := c.Query("cursor")

	tokens, next, err := h.tokenRepo.GetByNamespacePaged(c.Request.Context(), ns.ID, limit, cursor)
	if err != nil {
		writeInternalError(c, "ns_token_list", err)
		return
	}

	resp := gin.H{"data": tokens}
	if next != "" {
		resp["nextCursor"] = next
	}
	c.JSON(http.StatusOK, resp)
}

// RevokeFromNamespace handles DELETE /api/v1/namespaces/:slug/tokens/:id.
//
// 允许吊销的角色：系统 admin、namespace owner|admin、token 自己的创建者。
func (h *TokenHandler) RevokeFromNamespace(c *gin.Context) {
	user := middleware.GetUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	if h.nsSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "namespace service unavailable"})
		return
	}

	slug := c.Param("slug")
	ns, err := h.nsSvc.GetBySlug(c.Request.Context(), slug)
	if err != nil || ns == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "namespace not found"})
		return
	}

	tokenID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token ID"})
		return
	}

	token, err := h.tokenRepo.GetByID(c.Request.Context(), tokenID)
	if err != nil {
		writeInternalError(c, "ns_token_revoke_lookup", err)
		return
	}
	// Reject if token doesn't belong to this namespace, or doesn't exist at all.
	if token == nil || token.NamespaceID == nil || *token.NamespaceID != ns.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
		return
	}

	canManage := h.nsSvc.CanManageTokens(c.Request.Context(), ns.ID, user)
	isCreator := token.UserID == user.ID
	if !canManage && !isCreator {
		c.JSON(http.StatusForbidden, gin.H{"error": "only namespace owner/admin or the token creator can revoke"})
		return
	}

	if err := h.tokenRepo.Revoke(c.Request.Context(), tokenID); err != nil {
		writeInternalError(c, "ns_token_revoke", err)
		return
	}

	cause := "self"
	if !isCreator {
		cause = "by_admin"
	}
	if h.auditSvc != nil {
		// by_admin distinguishes "owner/admin revoked someone else's token" from
		// "creator revoked their own" — important for incident review when the
		// token's apparent owner (UserID) didn't initiate the revoke themselves.
		details := "namespace=" + slug
		if !isCreator {
			details += ",by_admin=true"
		}
		h.auditSvc.Log(c.Request.Context(), &user.ID, "team_token_revoke", "api_token", &tokenID,
			details, c.ClientIP())
	}
	h.metricsOrDefault().TeamTokenRevoked.WithLabelValues(cause).Inc()

	c.JSON(http.StatusOK, gin.H{"message": "token revoked"})
}
