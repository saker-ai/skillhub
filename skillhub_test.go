package skillhub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saker-ai/skillhub"
	"github.com/saker-ai/skillhub/pkg/auth"
	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/metrics"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"

	// Blank-import the default backends so cfg.Store.Backend == "" 解析为 git。
	// 与 cmd/skillhub/main.go 的导入语义一致。
	_ "github.com/saker-ai/skillhub/pkg/store/git"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newTestHub 构造一个完全自包含的 Hub：sqlite + 临时目录 + 端口 0（不真正监听）。
//
// 用 t.TempDir()，所有副作用在测试结束时自动清理，避免污染开发环境。
func newTestHub(t *testing.T) *skillhub.Hub {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0 // 不会真的 listen——本测试只走 ServeHTTP 内存调用
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

	hub, err := skillhub.New(context.Background(), skillhub.WithConfig(cfg))
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	return hub
}

// TestHub_RegisterRoutes_OnExternalEngine 验证 Stage 4 的核心承诺：
// 嵌入方可以拿一个完全自建的 *gin.Engine，把 SkillHub 路由挂上去，
// 然后通过 ServeHTTP 处理请求——不需要 Hub.Run()。
func TestHub_RegisterRoutes_OnExternalEngine(t *testing.T) {
	hub := newTestHub(t)

	engine := gin.New()
	hub.RegisterRoutes(engine)

	// /healthz 是不依赖 DB 的最简端点
	{
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("healthz status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		var got map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("healthz body decode: %v (body=%s)", err, w.Body.String())
		}
		if got["status"] != "ok" {
			t.Errorf("healthz body = %v, want status=ok", got)
		}
	}

	// /api/v1/skills 走 DB 但应返回空列表
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("/api/v1/skills status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
	}

	// /api/v1/nonexistent — 通过 SPA fallback 之前先验证 RegisterRoutes 不挂
	// 任何 /api/* 之外的兜底；engine.NoRoute 默认会返回 404。
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/this-endpoint-does-not-exist", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for unknown /api path, got %d", w.Code)
		}
	}
}

// TestHub_RegisterStatic_SPAFallback 验证 RegisterStatic 在外部 engine 上能挂 SPA。
func TestHub_RegisterStatic_SPAFallback(t *testing.T) {
	hub := newTestHub(t)

	engine := gin.New()
	hub.RegisterRoutes(engine)
	hub.RegisterStatic(engine)

	// 未知非 /api/ 路径应当走 SPA index.html 兜底
	req := httptest.NewRequest(http.MethodGet, "/some/spa/route", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SPA fallback status = %d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	// index.html 嵌入资源是 React build 产物，不强检具体内容；
	// 只要 Content-Type 是 text/html 且 body 非空即可。
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("SPA Content-Type = %q, want text/html*", ct)
	}
	if len(body) == 0 {
		t.Error("SPA body is empty")
	}

	// /api/* 仍然返回 JSON 404，不会被 SPA 吃掉
	req = httptest.NewRequest(http.MethodGet, "/api/v1/no-such-thing", nil)
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("/api 404 expected, got %d", w.Code)
	}
}

// fakeIdentityProvider 用于阶段 5 的注入测试：
// 当 X-Test-User 头出现时返回一个合成 user，否则返回未认证。
type fakeIdentityProvider struct{ user *model.User }

func (f *fakeIdentityProvider) Identify(ctx context.Context, r *http.Request) (*model.User, string, *uuid.UUID, error) {
	if r.Header.Get("X-Test-User") == "" {
		return nil, "", nil, nil
	}
	return f.user, "full", nil, nil
}

// TestHub_WithIdentityProvider 验证阶段 5 的承诺：
// 嵌入方注入的 IdentityProvider 真的会被 RequireAuth 中间件采用——
// 既不需要 SkillHub 的 token 表，也不需要 cookie，宿主自己的鉴权逻辑足以放行请求。
func TestHub_WithIdentityProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

	syntheticUser := &model.User{
		ID:     uuid.New(),
		Handle: "embedded-tester",
		Role:   "user",
	}
	idp := &fakeIdentityProvider{user: syntheticUser}

	hub, err := skillhub.New(context.Background(),
		skillhub.WithConfig(cfg),
		skillhub.WithIdentityProvider(idp),
	)
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine := gin.New()
	hub.RegisterRoutes(engine)

	// 没带 magic header → 401
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without identity, got %d body=%s", w.Code, w.Body.String())
		}
	}

	// 带上 magic header → 200，并返回我们注入的合成用户
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
		req.Header.Set("X-Test-User", "1")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 with identity, got %d body=%s", w.Code, w.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("/whoami body decode: %v body=%s", err, w.Body.String())
		}
		if got["handle"] != "embedded-tester" {
			t.Errorf("/whoami handle = %v, want embedded-tester (full=%v)", got["handle"], got)
		}
	}
}

// TestHub_SkillsMarkdown 验证 /skills.md 返回面向 agent 的 markdown 操作指南。
// 覆盖两条 base URL 解析路径：
//  1. cfg.Server.BaseURL 已配置 → 直接用该值（生产部署的常态）
//  2. cfg.Server.BaseURL 为空 → 回退到 X-Forwarded-Proto + Host（裸反代场景）
func TestHub_SkillsMarkdown(t *testing.T) {
	t.Run("uses configured BaseURL", func(t *testing.T) {
		hub := newTestHub(t)
		engine := gin.New()
		hub.RegisterRoutes(engine)

		// newTestHub 走 DefaultConfig，BaseURL 默认 http://localhost:10070
		want := hub.Config().Server.BaseURL
		if want == "" {
			t.Fatal("precondition: DefaultConfig should have non-empty BaseURL")
		}

		req := httptest.NewRequest(http.MethodGet, "/skills.md", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("/skills.md status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
			t.Errorf("/skills.md Content-Type = %q, want text/markdown*", ct)
		}
		body := w.Body.String()
		for _, marker := range []string{
			"curl",
			want,
			"/api/v1/auth/device/code",
			"/api/v1/whoami",
			"/api/v1/download",
		} {
			if !strings.Contains(body, marker) {
				t.Errorf("/skills.md body missing %q (len=%d)", marker, len(body))
			}
		}
	})

	t.Run("falls back to request Host when BaseURL empty", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		tmp := t.TempDir()
		cfg := config.DefaultConfig()
		cfg.Server.Port = 0
		cfg.Server.BaseURL = "" // exercise the fallback path
		cfg.Database.Driver = "sqlite"
		cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
		cfg.Database.AutoMigrate = true
		cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
		cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

		hub, err := skillhub.New(context.Background(), skillhub.WithConfig(cfg))
		if err != nil {
			t.Fatalf("skillhub.New: %v", err)
		}
		t.Cleanup(func() { _ = hub.Close() })

		engine := gin.New()
		hub.RegisterRoutes(engine)

		req := httptest.NewRequest(http.MethodGet, "/skills.md", nil)
		req.Host = "registry.example.test"
		req.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "https://registry.example.test") {
			t.Errorf("body missing https://registry.example.test (len=%d, head=%q)", len(body), body[:min(200, len(body))])
		}
	})
}

// TestHub_ClawHubJSON_AdvertisesInstallGuide 确保 well-known 文档把
// /skills.md 列在 endpoints 里，让 agent 通过单一发现入口找到它。
func TestHub_ClawHubJSON_AdvertisesInstallGuide(t *testing.T) {
	hub := newTestHub(t)
	engine := gin.New()
	hub.RegisterRoutes(engine)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/clawhub.json", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clawhub.json status = %d, want 200", w.Code)
	}
	var got struct {
		Endpoints map[string]string `json:"endpoints"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode clawhub.json: %v body=%s", err, w.Body.String())
	}
	if got.Endpoints["installGuide"] != "/skills.md" {
		t.Errorf("clawhub.json endpoints.installGuide = %q, want /skills.md (full=%v)",
			got.Endpoints["installGuide"], got.Endpoints)
	}
	// 同一发现文档同时把 OpenAPI / Swagger UI 暴露出来，避免 agent 自己猜路径。
	if got.Endpoints["openapi"] != "/openapi.json" {
		t.Errorf("clawhub.json endpoints.openapi = %q, want /openapi.json (full=%v)",
			got.Endpoints["openapi"], got.Endpoints)
	}
	if got.Endpoints["apiDocs"] != "/docs" {
		t.Errorf("clawhub.json endpoints.apiDocs = %q, want /docs (full=%v)",
			got.Endpoints["apiDocs"], got.Endpoints)
	}
}

// TestHub_OpenAPI_ExposesSpec 验证 Huma + humagin 文档契约：
//   - /openapi.json 是 Huma 原生 OpenAPI 3.1 规范
//   - /api/v1/openapi.{json,yaml} 保持为同一份规范的兼容别名
//   - /docs 是 Huma 原生文档页，/api/docs 内部转发兼容旧链接
func TestHub_OpenAPI_ExposesSpec(t *testing.T) {
	hub := newTestHub(t)
	engine := gin.New()
	hub.RegisterRoutes(engine)
	hub.RegisterStatic(engine)

	t.Run("yaml spec served", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("openapi.yaml status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		ct := w.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "application/yaml") && !strings.HasPrefix(ct, "text/yaml") {
			t.Errorf("Content-Type = %q, want application/yaml*", ct)
		}
		body := w.Body.String()
		// 关键字段：必须能在 YAML 里看到我们写的标题与几条核心路径。
		for _, marker := range []string{
			"openapi: 3.1.0",
			"title: SkillHub API",
			"/api/v1/whoami",
			"/api/v1/skills",
			"/api/v1/namespaces/{slug}/tokens",
			"BearerAuth",
		} {
			if !strings.Contains(body, marker) {
				t.Errorf("openapi.yaml missing %q (len=%d)", marker, len(body))
			}
		}
	})

	t.Run("json spec served", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("/openapi.json status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		ct := w.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "application/openapi+json") && !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want OpenAPI JSON", ct)
		}
		var doc map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatalf("openapi.json is not valid JSON: %v (head=%q)",
				err, w.Body.String()[:min(200, w.Body.Len())])
		}
		if doc["openapi"] != "3.1.0" {
			t.Errorf("openapi field = %v, want 3.1.0", doc["openapi"])
		}
		if _, ok := doc["paths"].(map[string]any); !ok {
			t.Errorf("paths is missing or not an object: %T", doc["paths"])
		}
		if _, ok := doc["info"].(map[string]any); !ok {
			t.Errorf("info is missing or not an object: %T", doc["info"])
		}
		// Sanity: 至少 30 条路径（实际 ~50 条）。出现急剧缩水时立刻告警。
		if paths, ok := doc["paths"].(map[string]any); ok {
			if len(paths) < 30 {
				t.Errorf("paths count = %d, want >=30 (spec was likely truncated)", len(paths))
			}
		}
	})

	t.Run("legacy json alias served", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("/api/v1/openapi.json status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("Content-Type = %q, want application/json*", w.Header().Get("Content-Type"))
		}
	})

	for _, path := range []string{"/docs", "/api/docs"} {
		t.Run("huma docs served "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("%s status = %d, want 200; body=%s", path, w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("%s Content-Type = %q, want text/html*", path, ct)
			}
			body := w.Body.String()
			for _, marker := range []string{"SkillHub API Reference", "@stoplight/elements"} {
				if !strings.Contains(body, marker) {
					t.Errorf("%s HTML missing %q; head=%q", path, marker, body[:min(400, len(body))])
				}
			}
		})
	}
}

// TestHub_NewDefaultEngine_HasMiddleware 简单验证默认 engine 能处理请求。
// （RequestID / Logging 等中间件无明显外部副作用，不在本测试中断言其逻辑——
// 那些是中间件包自己的测试范围。）
func TestHub_NewDefaultEngine_HasMiddleware(t *testing.T) {
	hub := newTestHub(t)
	engine := hub.NewDefaultEngine()
	if engine == nil {
		t.Fatal("NewDefaultEngine returned nil")
	}
	hub.RegisterRoutes(engine)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// multiUserIdentityProvider 让一组测试通过 X-Test-User 头切换"当前登录用户"。
// 用于覆盖需要多角色交互的端点（owner 签发 / 路人尝试签发被拒）。
type multiUserIdentityProvider struct{ users map[string]*model.User }

func (p *multiUserIdentityProvider) Identify(_ context.Context, r *http.Request) (*model.User, string, *uuid.UUID, error) {
	handle := r.Header.Get("X-Test-User")
	if handle == "" {
		return nil, "", nil, nil
	}
	u, ok := p.users[handle]
	if !ok {
		return nil, "", nil, nil
	}
	return u, "full", nil, nil
}

// TestHub_NamespaceTokens_Lifecycle 端到端覆盖团队 token 三件套（POST/GET/DELETE
// /api/v1/namespaces/:slug/tokens）以及 owner-only 权限闸门：
//
//  1. owner 创建 namespace
//  2. owner 签发 team token，校验响应里有 raw token 与绑定 namespaceId
//  3. 列表能看到刚刚签发的 token
//  4. 非成员尝试签发 → 403
//  5. 非成员尝试列表 → 403
//  6. owner 撤销 token → 200
//  7. 撤销后列表为空（GetByNamespaceID 过滤 revoked_at IS NULL）
//
// 不通过 SkillService 跑发布流程——那一段已被 TestAuthorizeSkillWrite 单元测试覆盖。
// 这里只验证 HTTP 表面 + handler 鉴权布线。
func TestHub_NamespaceTokens_Lifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

	owner := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	outsider := &model.User{ID: uuid.New(), Handle: "bob", Role: "user"}
	// carol is system admin only so we can read audit-logs at the end of the
	// flow without poking at hub internals. Keeping alice as plain "user"
	// matches the production scenario (namespace owner != system admin).
	auditor := &model.User{ID: uuid.New(), Handle: "carol", Role: "admin"}
	idp := &multiUserIdentityProvider{users: map[string]*model.User{
		"alice": owner,
		"bob":   outsider,
		"carol": auditor,
	}}

	// Private metrics so the create/revoke counter assertions below are not
	// polluted by other tests sharing metrics.Default in the same binary run.
	mx := metrics.New(prometheus.NewRegistry())

	hub, err := skillhub.New(context.Background(),
		skillhub.WithConfig(cfg),
		skillhub.WithIdentityProvider(idp),
		skillhub.WithMetrics(mx),
	)
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine := gin.New()
	hub.RegisterRoutes(engine)

	do := func(method, path, asUser string, body any) (int, []byte) {
		var r io.Reader
		if body != nil {
			buf, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			r = bytes.NewReader(buf)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if asUser != "" {
			req.Header.Set("X-Test-User", asUser)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	// 1) alice creates a team namespace
	code, body := do(http.MethodPost, "/api/v1/namespaces", "alice", map[string]any{
		"slug":        "acme",
		"displayName": "Acme",
		"type":        "team",
	})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create namespace: status=%d body=%s", code, body)
	}

	// 2) alice mints a team token under the namespace
	code, body = do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", map[string]any{
		"label":     "ci-runner",
		"scope":     "publish",
		"expiresIn": "720h",
	})
	if code != http.StatusCreated {
		t.Fatalf("create team token: status=%d body=%s", code, body)
	}
	var created struct {
		Token    string `json:"token"`
		Metadata struct {
			ID          string  `json:"id"`
			NamespaceID *string `json:"namespaceId"`
			Scope       string  `json:"scope"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal create response: %v body=%s", err, body)
	}
	if created.Token == "" {
		t.Errorf("expected non-empty raw token in response")
	}
	if created.Metadata.NamespaceID == nil || *created.Metadata.NamespaceID == "" {
		t.Errorf("expected metadata.namespaceId to be populated; got %+v", created.Metadata)
	}
	if created.Metadata.Scope != "publish" {
		t.Errorf("metadata.scope = %q, want publish", created.Metadata.Scope)
	}
	tokenID := created.Metadata.ID
	if tokenID == "" {
		t.Fatalf("metadata.id is empty: %s", body)
	}

	// 3) alice lists tokens — should see exactly the one we just created
	code, body = do(http.MethodGet, "/api/v1/namespaces/acme/tokens", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("list team tokens: status=%d body=%s", code, body)
	}
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("unmarshal list: %v body=%s", err, body)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != tokenID {
		t.Errorf("listed tokens = %+v, want exactly [%s]", listed.Data, tokenID)
	}

	// 4) bob (not a member) tries to mint a token → 403
	code, body = do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "bob", map[string]any{
		"label": "shouldfail",
		"scope": "full",
	})
	if code != http.StatusForbidden {
		t.Errorf("outsider mint: status=%d body=%s, want 403", code, body)
	}

	// 5) bob (not a member) tries to list tokens → 403
	code, body = do(http.MethodGet, "/api/v1/namespaces/acme/tokens", "bob", nil)
	if code != http.StatusForbidden {
		t.Errorf("outsider list: status=%d body=%s, want 403", code, body)
	}

	// 6) alice revokes her token
	code, body = do(http.MethodDelete, "/api/v1/namespaces/acme/tokens/"+tokenID, "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("revoke: status=%d body=%s", code, body)
	}

	// 7) post-revoke list returns empty (GetByNamespaceID filters revoked_at IS NULL)
	code, body = do(http.MethodGet, "/api/v1/namespaces/acme/tokens", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("post-revoke list: status=%d body=%s", code, body)
	}
	listed.Data = nil
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("unmarshal post-revoke list: %v body=%s", err, body)
	}
	if len(listed.Data) != 0 {
		t.Errorf("post-revoke list = %+v, want empty", listed.Data)
	}

	// 8) audit trail — carol (system admin) reads /api/v1/admin/audit-logs
	//    and sees one create + one revoke entry. Filtering by action keeps the
	//    assertion robust against unrelated audit writes from other handlers.
	type auditRow struct {
		Action       string  `json:"action"`
		ResourceType string  `json:"resourceType"`
		Details      *string `json:"details"`
	}
	var auditResp struct {
		Data []auditRow `json:"data"`
	}

	code, body = do(http.MethodGet, "/api/v1/admin/audit-logs?action=team_token_create", "carol", nil)
	if code != http.StatusOK {
		t.Fatalf("read audit logs (create): status=%d body=%s", code, body)
	}
	if err := json.Unmarshal(body, &auditResp); err != nil {
		t.Fatalf("unmarshal audit (create): %v body=%s", err, body)
	}
	if len(auditResp.Data) == 0 {
		t.Fatalf("expected at least one team_token_create audit row")
	}
	if auditResp.Data[0].ResourceType != "api_token" ||
		auditResp.Data[0].Details == nil ||
		!strings.Contains(*auditResp.Data[0].Details, "namespace=acme") ||
		!strings.Contains(*auditResp.Data[0].Details, "scope=publish") {
		t.Errorf("create audit row malformed: %+v", auditResp.Data[0])
	}

	auditResp.Data = nil
	code, body = do(http.MethodGet, "/api/v1/admin/audit-logs?action=team_token_revoke", "carol", nil)
	if code != http.StatusOK {
		t.Fatalf("read audit logs (revoke): status=%d body=%s", code, body)
	}
	if err := json.Unmarshal(body, &auditResp); err != nil {
		t.Fatalf("unmarshal audit (revoke): %v body=%s", err, body)
	}
	if len(auditResp.Data) == 0 {
		t.Fatalf("expected at least one team_token_revoke audit row")
	}
	// alice is both the namespace owner and the token creator → no by_admin tag.
	if auditResp.Data[0].Details == nil || !strings.Contains(*auditResp.Data[0].Details, "namespace=acme") {
		t.Errorf("revoke audit details = %v, want contain namespace=acme", auditResp.Data[0].Details)
	}
	if auditResp.Data[0].Details != nil && strings.Contains(*auditResp.Data[0].Details, "by_admin=true") {
		t.Errorf("self-revoke should NOT be tagged by_admin=true; details=%q", *auditResp.Data[0].Details)
	}

	// 9) Prometheus counters — handler-level. Asserting exact values is safe
	//    because mx is a per-test private registry (no other counters touch it).
	if got := testutil.ToFloat64(mx.TeamTokenCreated.WithLabelValues("publish")); got != 1 {
		t.Errorf("metric team_token_created{scope=publish} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(mx.TeamTokenRevoked.WithLabelValues("self")); got != 1 {
		t.Errorf("metric team_token_revoked{cause=self} = %v, want 1", got)
	}
	// by_admin and cascade_* counters were never touched in this flow — must be 0.
	if got := testutil.ToFloat64(mx.TeamTokenRevoked.WithLabelValues("by_admin")); got != 0 {
		t.Errorf("metric team_token_revoked{cause=by_admin} = %v, want 0", got)
	}
}

// TestHub_TeamTokens_ScopeAndExpiryValidation pins the new P3-10/11 contract:
//   - team tokens MUST NOT carry "full" scope
//   - expiresIn is required and must be ≤ 365d
//   - empty scope defaults to "publish" (least-privilege for team tokens)
//
// 用同一个 acme namespace 发四种请求，互相独立断言；不依赖前一个请求的副作用。
func TestHub_TeamTokens_ScopeAndExpiryValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

	owner := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	idp := &multiUserIdentityProvider{users: map[string]*model.User{"alice": owner}}

	hub, err := skillhub.New(context.Background(),
		skillhub.WithConfig(cfg),
		skillhub.WithIdentityProvider(idp),
	)
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine := gin.New()
	hub.RegisterRoutes(engine)

	do := func(method, path, asUser string, body any) (int, []byte) {
		var r io.Reader
		if body != nil {
			buf, _ := json.Marshal(body)
			r = bytes.NewReader(buf)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if asUser != "" {
			req.Header.Set("X-Test-User", asUser)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	code, body := do(http.MethodPost, "/api/v1/namespaces", "alice", map[string]any{
		"slug":        "acme",
		"displayName": "Acme",
		"type":        "team",
	})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create namespace: status=%d body=%s", code, body)
	}

	cases := []struct {
		name     string
		body     map[string]any
		wantCode int
		wantMsg  string // substring match on error response; "" = don't check body
	}{
		{
			name:     "scope=full is rejected",
			body:     map[string]any{"label": "x", "scope": "full", "expiresIn": "24h"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "must be one of: read, publish",
		},
		{
			name:     "missing expiresIn is rejected",
			body:     map[string]any{"label": "x", "scope": "publish"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "expiresIn is required",
		},
		{
			name:     "expiresIn > 365d is rejected",
			body:     map[string]any{"label": "x", "scope": "publish", "expiresIn": "8761h"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "exceeds maximum",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", tc.body)
			if code != tc.wantCode {
				t.Errorf("status = %d, want %d; body=%s", code, tc.wantCode, body)
			}
			if tc.wantMsg != "" && !strings.Contains(string(body), tc.wantMsg) {
				t.Errorf("body %q must contain %q", body, tc.wantMsg)
			}
		})
	}

	// Empty scope is allowed and defaults to publish — verifies the "least
	// privilege default" half of P3-10.
	code, body = do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", map[string]any{
		"label":     "default-scope",
		"expiresIn": "24h",
	})
	if code != http.StatusCreated {
		t.Fatalf("default-scope create: status=%d body=%s", code, body)
	}
	var created struct {
		Metadata struct {
			Scope string `json:"scope"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if created.Metadata.Scope != "publish" {
		t.Errorf("default scope = %q, want publish", created.Metadata.Scope)
	}
}

// TestHub_TeamTokens_QuotaEnforced pins P2-7 contract: a namespace cannot mint
// more than maxTeamTokensPerNamespace (=50) active team tokens. The 51st
// request returns 409 Conflict with both `limit` and `count` echoed back so
// CLI/UI can render an actionable error.
//
// 写满 50 后再发一次：必须 409；revoke 一个再发：必须 201。
func TestHub_TeamTokens_QuotaEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")
	// Default WriteLimit is 30/120s — too low for a quota test that needs 50+
	// successful mints. Bump high enough to exhaust the *quota* (not the rate
	// limit) so the assertion under test is meaningful.
	cfg.RateLimit.WriteLimit = 1000

	owner := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	idp := &multiUserIdentityProvider{users: map[string]*model.User{"alice": owner}}

	hub, err := skillhub.New(context.Background(),
		skillhub.WithConfig(cfg),
		skillhub.WithIdentityProvider(idp),
	)
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine := gin.New()
	hub.RegisterRoutes(engine)

	do := func(method, path, asUser string, body any) (int, []byte) {
		var r io.Reader
		if body != nil {
			buf, _ := json.Marshal(body)
			r = bytes.NewReader(buf)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if asUser != "" {
			req.Header.Set("X-Test-User", asUser)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	code, body := do(http.MethodPost, "/api/v1/namespaces", "alice", map[string]any{
		"slug": "acme", "displayName": "Acme", "type": "team",
	})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create namespace: status=%d body=%s", code, body)
	}

	// Mint 50 tokens — fill the quota exactly. Use distinct labels so a future
	// failure mode (e.g., dedup by label) would surface as the 50th request
	// failing rather than the 51st.
	const quota = 50
	var firstID string
	for i := 0; i < quota; i++ {
		code, body := do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", map[string]any{
			"label": fmt.Sprintf("ci-%02d", i), "scope": "publish", "expiresIn": "24h",
		})
		if code != http.StatusCreated {
			t.Fatalf("create #%d: status=%d body=%s", i, code, body)
		}
		if i == 0 {
			var resp struct {
				Metadata struct {
					ID string `json:"id"`
				} `json:"metadata"`
			}
			_ = json.Unmarshal(body, &resp)
			firstID = resp.Metadata.ID
		}
	}
	if firstID == "" {
		t.Fatal("first token ID not captured")
	}

	// 51st must be rejected with 409 + actionable payload.
	code, body = do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", map[string]any{
		"label": "overflow", "scope": "publish", "expiresIn": "24h",
	})
	if code != http.StatusConflict {
		t.Fatalf("over-quota: status=%d (want 409); body=%s", code, body)
	}
	var rejected struct {
		Error string  `json:"error"`
		Limit int     `json:"limit"`
		Count float64 `json:"count"`
	}
	if err := json.Unmarshal(body, &rejected); err != nil {
		t.Fatalf("rejected unmarshal: %v body=%s", err, body)
	}
	if rejected.Limit != quota {
		t.Errorf("rejected.limit = %d, want %d", rejected.Limit, quota)
	}
	if int(rejected.Count) != quota {
		t.Errorf("rejected.count = %v, want %d", rejected.Count, quota)
	}
	if !strings.Contains(rejected.Error, "maximum") {
		t.Errorf("rejected.error must mention 'maximum'; got %q", rejected.Error)
	}

	// Free a slot — revoking the first token should let the next mint succeed.
	// This also covers the regression risk that CountActiveByNamespace forgets
	// to filter on revoked_at.
	code, body = do(http.MethodDelete, "/api/v1/namespaces/acme/tokens/"+firstID, "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("revoke first: status=%d body=%s", code, body)
	}
	code, body = do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", map[string]any{
		"label": "after-revoke", "scope": "publish", "expiresIn": "24h",
	})
	if code != http.StatusCreated {
		t.Fatalf("after-revoke create: status=%d body=%s", code, body)
	}
}

// TestHub_TeamTokens_ListPagination pins P2-8 contract: GET
// /api/v1/namespaces/:slug/tokens?limit=N&cursor=X yields stable, cursor-driven
// pages, with `nextCursor` omitted on the last page.
//
// 关注点：
//   - limit < 总数 → 返回 limit 条 + 非空 nextCursor
//   - 第二页跟着 cursor 走 → 拿到剩余的、且 ID 不重叠
//   - 最后一页：nextCursor 字段不出现（nil-coalesce 友好）
//   - 没传 limit 也能用：默认 20，三条数据一次返回
func TestHub_TeamTokens_ListPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

	owner := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	idp := &multiUserIdentityProvider{users: map[string]*model.User{"alice": owner}}

	hub, err := skillhub.New(context.Background(),
		skillhub.WithConfig(cfg),
		skillhub.WithIdentityProvider(idp),
	)
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine := gin.New()
	hub.RegisterRoutes(engine)

	do := func(method, path, asUser string, body any) (int, []byte) {
		var r io.Reader
		if body != nil {
			buf, _ := json.Marshal(body)
			r = bytes.NewReader(buf)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if asUser != "" {
			req.Header.Set("X-Test-User", asUser)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	code, body := do(http.MethodPost, "/api/v1/namespaces", "alice", map[string]any{
		"slug": "acme", "displayName": "Acme", "type": "team",
	})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create namespace: status=%d body=%s", code, body)
	}

	// Mint 5 tokens — small enough to walk pages by hand. Sleep 1ms between
	// to ensure created_at is strictly monotonic so the cursor (created_at < ?)
	// produces stable, non-overlapping pages.
	for i := 0; i < 5; i++ {
		code, body := do(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", map[string]any{
			"label": fmt.Sprintf("k-%d", i), "scope": "publish", "expiresIn": "24h",
		})
		if code != http.StatusCreated {
			t.Fatalf("seed #%d: status=%d body=%s", i, code, body)
		}
		time.Sleep(1 * time.Millisecond)
	}

	type page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		NextCursor string `json:"nextCursor"`
	}

	// Page 1 — limit=2 must return 2 tokens + a cursor.
	code, body = do(http.MethodGet, "/api/v1/namespaces/acme/tokens?limit=2", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("page1: status=%d body=%s", code, body)
	}
	var p1 page
	if err := json.Unmarshal(body, &p1); err != nil {
		t.Fatalf("p1 unmarshal: %v body=%s", err, body)
	}
	if len(p1.Data) != 2 {
		t.Fatalf("p1.data len = %d, want 2", len(p1.Data))
	}
	if p1.NextCursor == "" {
		t.Fatal("p1.nextCursor must be non-empty (more pages remain)")
	}

	// Page 2 — same limit, follow cursor; expect 2 fresh IDs.
	code, body = do(http.MethodGet,
		"/api/v1/namespaces/acme/tokens?limit=2&cursor="+url.QueryEscape(p1.NextCursor),
		"alice", nil)
	if code != http.StatusOK {
		t.Fatalf("page2: status=%d body=%s", code, body)
	}
	var p2 page
	if err := json.Unmarshal(body, &p2); err != nil {
		t.Fatalf("p2 unmarshal: %v body=%s", err, body)
	}
	if len(p2.Data) != 2 {
		t.Fatalf("p2.data len = %d, want 2", len(p2.Data))
	}
	if p2.NextCursor == "" {
		t.Fatal("p2.nextCursor must be non-empty (1 token still remains)")
	}
	// Non-overlap check — page 2 must not repeat any ID from page 1.
	seen := map[string]bool{}
	for _, r := range p1.Data {
		seen[r.ID] = true
	}
	for _, r := range p2.Data {
		if seen[r.ID] {
			t.Errorf("page2 repeats id from page1: %s", r.ID)
		}
	}

	// Page 3 — last token, nextCursor MUST be omitted.
	code, body = do(http.MethodGet,
		"/api/v1/namespaces/acme/tokens?limit=2&cursor="+url.QueryEscape(p2.NextCursor),
		"alice", nil)
	if code != http.StatusOK {
		t.Fatalf("page3: status=%d body=%s", code, body)
	}
	var p3 page
	if err := json.Unmarshal(body, &p3); err != nil {
		t.Fatalf("p3 unmarshal: %v body=%s", err, body)
	}
	if len(p3.Data) != 1 {
		t.Fatalf("p3.data len = %d, want 1", len(p3.Data))
	}
	if p3.NextCursor != "" {
		t.Errorf("p3.nextCursor = %q, want empty (last page)", p3.NextCursor)
	}

	// 默认行为：no limit, no cursor — 5 tokens 全在一页（默认 20），nextCursor 缺失。
	code, body = do(http.MethodGet, "/api/v1/namespaces/acme/tokens", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("default page: status=%d body=%s", code, body)
	}
	var pAll page
	if err := json.Unmarshal(body, &pAll); err != nil {
		t.Fatalf("pAll unmarshal: %v body=%s", err, body)
	}
	if len(pAll.Data) != 5 {
		t.Errorf("default page len = %d, want 5", len(pAll.Data))
	}
	if pAll.NextCursor != "" {
		t.Errorf("default page nextCursor = %q, want empty", pAll.NextCursor)
	}
}

// hybridIDP wires the production *auth.Service token validator side-by-side
// with a small X-Test-User stub. The two are not redundant:
//
//   - Bearer header → real auth.Service.Identify, which hashes the raw token
//     and looks it up in the tokens table. This is the production path the
//     E2E test below is meant to exercise.
//   - X-Test-User header → returns a pre-seeded *model.User without touching
//     the tokens table. Used only to bootstrap (create namespace, mint the
//     team token) before any raw token exists.
//
// 之所以不全用 X-Test-User：那条路径绕过了 token 验证、scope 解析、
// namespace 绑定字段，等于把 P2-9 想验证的全部链路都跳过了。
type hybridIDP struct {
	real auth.IdentityProvider
	stub map[string]*model.User
}

func (h *hybridIDP) Identify(ctx context.Context, r *http.Request) (*model.User, string, *uuid.UUID, error) {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		// Always try the real validator first when a Bearer header is present —
		// only fall through to the stub if it returns "no identity" (so we
		// don't accidentally upgrade a garbage bearer to alice via X-Test-User).
		u, scope, ns, err := h.real.Identify(ctx, r)
		if err == nil && u != nil {
			return u, scope, ns, nil
		}
	}
	if handle := r.Header.Get("X-Test-User"); handle != "" {
		if u, ok := h.stub[handle]; ok {
			return u, "full", nil, nil
		}
	}
	return nil, "", nil, nil
}

// TestHub_TeamToken_PublishSkillE2E pins the P2-9 contract: a raw team token
// returned by POST /api/v1/namespaces/:slug/tokens can actually be used as a
// Bearer credential to publish a skill into that namespace.
//
// Coverage rationale:
//   - The existing namespace-token tests (Lifecycle, ScopeAndExpiryValidation)
//     bypass token validation by stubbing the IDP. They verify the mint+revoke
//     surface but never exercise the validate→GetTokenNamespace→PublishVersion
//     wiring end-to-end. A regression in any of:
//   - auth.Service.ValidateToken returning the wrong NamespaceID
//   - middleware.RequireAuth not propagating TokenNamespaceKey
//   - SkillService.PublishVersion not honoring req.TokenNamespace
//     would silently pass those tests but break real CI publish jobs.
//
// Test flow:
//  1. Boot Hub against a shared *gorm.DB so a side-channel auth.Service can
//     validate raw tokens we mint via the API.
//  2. Persist alice in the users table — required because real ValidateToken
//     ultimately calls userRepo.GetByID(t.UserID).
//  3. Bootstrap via X-Test-User: alice creates "acme" + "bystander" namespaces,
//     and mints a publish-scope team token bound to "acme". Capture raw token.
//  4. Positive case: POST /api/agent/skills + the raw bearer → 201 Created;
//     verify list/detail/download use the simplified Agent Skill API shape.
//  5. The deprecated namespace form field is ignored; the token binding decides
//     the target namespace.
func TestHub_TeamToken_PublishSkillE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

	// Construct the DB ourselves so we can also build a side-channel
	// auth.Service that the hybrid IDP delegates Bearer validation to.
	// WithDB hands the same *gorm.DB into the Hub, so both views see the
	// same tokens/users tables.
	db, err := repository.NewDB(cfg.Database)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}

	userRepo := repository.NewUserRepo(db)
	tokenRepo := repository.NewTokenRepo(db)
	realAuthSvc := auth.NewService(tokenRepo, userRepo)

	// Persist alice — auth.Service.ValidateToken does userRepo.GetByID after
	// matching the token hash; without this row that lookup returns nil and
	// validation reports "unauthenticated".
	alice := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	if err := userRepo.Create(context.Background(), alice); err != nil {
		t.Fatalf("seed alice: %v", err)
	}

	idp := &hybridIDP{
		real: realAuthSvc,
		stub: map[string]*model.User{"alice": alice},
	}

	hub, err := skillhub.New(context.Background(),
		skillhub.WithConfig(cfg),
		skillhub.WithDB(db),
		skillhub.WithIdentityProvider(idp),
	)
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine := gin.New()
	hub.RegisterRoutes(engine)

	doJSON := func(method, path, asUser string, body any) (int, []byte) {
		var r io.Reader
		if body != nil {
			buf, _ := json.Marshal(body)
			r = bytes.NewReader(buf)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if asUser != "" {
			req.Header.Set("X-Test-User", asUser)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	// 1) alice creates two namespaces — acme is the team-token target,
	//    bystander exists only so the negative case has a *valid* sibling
	//    namespace to attempt (avoids false negatives from "namespace not
	//    found" masking the "wrong namespace" check).
	for _, slug := range []string{"acme", "bystander"} {
		// strings.Title 已废弃；slug 这里都是纯 ASCII，手工做首字母大写避免引入 x/text。
		displayName := strings.ToUpper(slug[:1]) + slug[1:]
		code, body := doJSON(http.MethodPost, "/api/v1/namespaces", "alice", map[string]any{
			"slug": slug, "displayName": displayName, "type": "team",
		})
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("create namespace %s: status=%d body=%s", slug, code, body)
		}
	}

	// 2) alice mints a publish-scope team token bound to acme.
	code, body := doJSON(http.MethodPost, "/api/v1/namespaces/acme/tokens", "alice", map[string]any{
		"label": "ci-publisher", "scope": "publish", "expiresIn": "24h",
	})
	if code != http.StatusCreated {
		t.Fatalf("mint team token: status=%d body=%s", code, body)
	}
	var minted struct {
		Token    string `json:"token"`
		Metadata struct {
			NamespaceID *string `json:"namespaceId"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &minted); err != nil {
		t.Fatalf("unmarshal mint: %v body=%s", err, body)
	}
	if minted.Token == "" {
		t.Fatalf("raw token empty: %s", body)
	}
	if minted.Metadata.NamespaceID == nil || *minted.Metadata.NamespaceID == "" {
		t.Fatalf("metadata.namespaceId missing: %s", body)
	}

	// publishMultipart builds a minimal valid skill upload (slug, version,
	// namespace, SKILL.md) and POSTs it as the raw team token's bearer.
	// SKILL.md is the only file PublishVersion specially extracts frontmatter
	// from; the rest of the file map can be arbitrary.
	publishMultipart := func(slug, version, nsSlug, bearer string, extra map[string]string) (int, []byte) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("slug", slug)
		_ = mw.WriteField("version", version)
		_ = mw.WriteField("namespace", nsSlug)
		for k, v := range extra {
			_ = mw.WriteField(k, v)
		}
		fw, err := mw.CreateFormFile("SKILL.md", "SKILL.md")
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		_, _ = fw.Write([]byte("---\nname: e2e\n---\n# E2E " + version + "\n"))
		_ = mw.Close()

		req := httptest.NewRequest(http.MethodPost, "/api/agent/skills", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	// 3) Positive case — publish to acme using the raw team token.
	code, body = publishMultipart("e2e-acme-skill", "1.0.0", "", minted.Token, map[string]string{
		"displayName": "E2E Acme Skill",
	})
	if code != http.StatusCreated {
		t.Fatalf("publish to acme: status=%d body=%s", code, body)
	}
	var pubResp struct {
		Data struct {
			Skill struct {
				Slug string `json:"slug"`
			} `json:"skill"`
			Version struct {
				Version     string `json:"version"`
				Fingerprint string `json:"fingerprint"`
			} `json:"version"`
		} `json:"data"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(body, &pubResp); err != nil {
		t.Fatalf("unmarshal publish resp: %v body=%s", err, body)
	}
	if pubResp.Data.Skill.Slug != "e2e-acme-skill" {
		t.Errorf("published slug = %q, want e2e-acme-skill", pubResp.Data.Skill.Slug)
	}
	if pubResp.Data.Version.Version != "1.0.0" || pubResp.Data.Version.Fingerprint == "" {
		t.Errorf("published version = %+v, want version + fingerprint", pubResp.Data.Version)
	}
	if pubResp.RequestID == "" {
		t.Error("publish response missing requestId")
	}
	assertJSONHasOnlyNewAgentKeys(t, body, "publish", []string{"data", "requestId"}, []string{"skill", "version"})

	agentGET := func(path string) (int, http.Header, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+minted.Token)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code, w.Header(), w.Body.Bytes()
	}

	code, _, body = agentGET("/api/agent/skills?page=1&pageSize=20")
	if code != http.StatusOK {
		t.Fatalf("agent list: status=%d body=%s", code, body)
	}
	var listResp struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Type        string `json:"type"`
			Nickname    string `json:"nickname"`
			Status      string `json:"status"`
			SkillhubID  string `json:"skillhubId"`
		} `json:"data"`
		Pagination struct {
			PageSize int  `json:"pageSize"`
			Total    int  `json:"total"`
			HasMore  bool `json:"hasMore"`
		} `json:"pagination"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatalf("unmarshal agent list: %v body=%s", err, body)
	}
	assertJSONHasOnlyNewAgentKeys(t, body, "list", []string{"data", "pagination", "requestId"}, nil)
	if len(listResp.Data) > 0 {
		itemRaw := agentJSONPath(t, body, "data").([]any)[0].(map[string]any)
		rejectJSONKeys(t, itemRaw, "list item", "Id", "Name", "Description", "Type", "Nickname", "Status", "SkillhubId")
	}
	var skillID string
	for _, item := range listResp.Data {
		if item.Name == "e2e-acme-skill" {
			skillID = item.ID
			if item.SkillhubID != "@acme/e2e-acme-skill" {
				t.Errorf("skillhubId = %q, want @acme/e2e-acme-skill", item.SkillhubID)
			}
			if item.Type != "custom" {
				t.Errorf("type = %q, want custom", item.Type)
			}
		}
	}
	if skillID == "" {
		t.Fatalf("published skill missing from agent list: %+v", listResp.Data)
	}

	code, _, body = agentGET("/api/agent/skills/" + skillID)
	if code != http.StatusOK {
		t.Fatalf("agent detail: status=%d body=%s", code, body)
	}
	var detailResp struct {
		Data struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			SkillMd     string `json:"skillMd"`
			SkillhubID  string `json:"skillhubId"`
			Description string `json:"description"`
		} `json:"data"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(body, &detailResp); err != nil {
		t.Fatalf("unmarshal agent detail: %v body=%s", err, body)
	}
	assertJSONHasOnlyNewAgentKeys(t, body, "detail", []string{"data", "requestId"}, nil)
	detailRaw := agentJSONPath(t, body, "data").(map[string]any)
	rejectJSONKeys(t, detailRaw, "detail", "Id", "Name", "Description", "Detail", "Type", "Nickname", "Status", "SkillhubId", "SkillMdContent")
	if detailResp.Data.ID != skillID || !strings.Contains(detailResp.Data.SkillMd, "name: e2e") {
		t.Fatalf("unexpected detail response: %+v", detailResp.Data)
	}

	code, _, body = agentGET("/api/agent/skills/" + skillID + "/download")
	if code != http.StatusOK {
		t.Fatalf("agent download json: status=%d body=%s", code, body)
	}
	var downloadResp struct {
		Data struct {
			DownloadURL string `json:"downloadUrl"`
		} `json:"data"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(body, &downloadResp); err != nil {
		t.Fatalf("unmarshal download json: %v body=%s", err, body)
	}
	assertJSONHasOnlyNewAgentKeys(t, body, "download", []string{"data", "requestId"}, []string{"downloadUrl"})
	if !strings.Contains(downloadResp.Data.DownloadURL, "/api/agent/skills/"+skillID+"/archive") {
		t.Fatalf("downloadUrl = %q, want archive URL", downloadResp.Data.DownloadURL)
	}

	code, _, body = agentGET("/api/agent/skills/" + skillID + "/download?format=json")
	if code != http.StatusOK {
		t.Fatalf("agent download ignores format query: status=%d body=%s", code, body)
	}
	var downloadWithQuery struct {
		Data struct {
			DownloadURL string `json:"downloadUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &downloadWithQuery); err != nil {
		t.Fatalf("unmarshal download with query json: %v body=%s", err, body)
	}
	if downloadWithQuery.Data.DownloadURL != downloadResp.Data.DownloadURL {
		t.Fatalf("downloadUrl with format query = %q, want %q", downloadWithQuery.Data.DownloadURL, downloadResp.Data.DownloadURL)
	}

	code, body = publishMultipart("e2e-acme-skill", "1.0.1", "bystander", minted.Token, nil)
	if code != http.StatusOK {
		t.Fatalf("publish second version with deprecated namespace ignored: status=%d body=%s", code, body)
	}

	code, body = publishMultipart("e2e-acme-skill", "1.0.2", "", minted.Token, map[string]string{"overwrite": "false"})
	if code != http.StatusConflict {
		t.Fatalf("publish overwrite=false: status=%d body=%s", code, body)
	}
	var conflictResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Slug string `json:"slug"`
			} `json:"details"`
		} `json:"error"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(body, &conflictResp); err != nil {
		t.Fatalf("unmarshal conflict: %v body=%s", err, body)
	}
	if conflictResp.Error.Code != "SKILL_NAME_CONFLICT" || conflictResp.Error.Details.Slug != "e2e-acme-skill" {
		t.Fatalf("unexpected conflict response: %+v", conflictResp)
	}
}

func assertJSONHasOnlyNewAgentKeys(t *testing.T, body []byte, label string, topKeys, dataKeys []string) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal %s raw JSON: %v body=%s", label, err, body)
	}
	rejectJSONKeys(t, root, label, "RequestId", "Data", "Items", "Total", "Page", "PageSize", "DownloadUrl", "downloadUrl")
	for _, key := range topKeys {
		if _, ok := root[key]; !ok {
			t.Fatalf("%s response missing top-level key %q: %v", label, key, root)
		}
	}
	if len(dataKeys) == 0 {
		return
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		t.Fatalf("%s data is not an object: %T", label, root["data"])
	}
	for _, key := range dataKeys {
		if _, ok := data[key]; !ok {
			t.Fatalf("%s data missing key %q: %v", label, key, data)
		}
	}
}

func agentJSONPath(t *testing.T, body []byte, key string) any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal raw JSON: %v body=%s", err, body)
	}
	v, ok := root[key]
	if !ok {
		t.Fatalf("missing JSON key %q in %v", key, root)
	}
	return v
}

func rejectJSONKeys(t *testing.T, obj map[string]any, label string, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := obj[key]; ok {
			t.Fatalf("%s response contains deprecated key %q: %v", label, key, obj)
		}
	}
}

func TestAgentSkillOpenAPIContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("web", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	text := string(body)
	downloadPath := "/api/agent/skills/{id}/download:"
	start := strings.Index(text, downloadPath)
	if start < 0 {
		t.Fatalf("openapi missing %s", downloadPath)
	}
	end := strings.Index(text[start+len(downloadPath):], "\n  /")
	section := text[start:]
	if end >= 0 {
		section = text[start : start+len(downloadPath)+end]
	}
	for _, want := range []string{`"200":`, "downloadUrl", "requestId"} {
		if !strings.Contains(section, want) {
			t.Fatalf("download OpenAPI section missing %q:\n%s", want, section)
		}
	}
	for _, forbidden := range []string{`"302":`, "name: format"} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("download OpenAPI section contains deprecated %q:\n%s", forbidden, section)
		}
	}
}
