package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"github.com/saker-ai/skillhub/web"
)

// OpenAPIHandler 暴露 OpenAPI 规范与 Swagger UI。
//
// 路由：
//
//	GET /api/v1/openapi.yaml  原始 YAML，便于灌入 redocly / spectral / openapi-typescript
//	GET /api/v1/openapi.json  YAML → JSON 转换，便于浏览器端直接喂给 Swagger UI
//	GET /api/docs             Swagger UI（CDN 版，零额外 Go 依赖）
//
// 设计取舍：
//   - 选择「手写 YAML + Swagger UI from CDN」而非 swaggo 注解：每条 handler 都要写
//     注释成本高，且与 /skills.md 的 curl 范例已部分重叠；YAML 与 /skills.md 一起
//     人工维护，单一事实源；
//   - YAML→JSON 在每次请求时转换：规范文件极小（~50 KB），转换成本可忽略，
//     不必引入预生成步骤；如成为热点可加 sync.Once 缓存；
//   - Swagger UI 走 unpkg CDN：避免把 ~3 MB 静态资源塞进 binary；零网络环境
//     下 /openapi.yaml 仍可直接用 redocly 离线渲染。
//
// 缓存：openapi.yaml 标 5 分钟 Cache-Control（同 /skills.md），HTML 不缓存
// （CDN 版本字符串变化时需要立即生效）。
type OpenAPIHandler struct{}

func NewOpenAPIHandler() *OpenAPIHandler { return &OpenAPIHandler{} }

// SpecYAML 返回原始 YAML 规范。
func (h *OpenAPIHandler) SpecYAML(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", web.OpenAPISpec)
}

// SpecJSON 返回 YAML → JSON 转换后的规范。Swagger UI 喜欢 JSON 一些。
func (h *OpenAPIHandler) SpecJSON(c *gin.Context) {
	var doc any
	if err := yaml.Unmarshal(web.OpenAPISpec, &doc); err != nil {
		writeInternalError(c, "openapi_yaml_unmarshal", err)
		return
	}
	doc = normalizeYAMLForJSON(doc)

	c.Header("Cache-Control", "public, max-age=300")
	c.Status(http.StatusOK)
	c.Header("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(c.Writer)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		// 已经写过 header / status，只能退化到日志，让客户端见到截断的响应。
		// header/status 已发送,c.Error 仅把 err 追加到 gin Errors 链,失败也无补救手段。
		_ = c.Error(err)
	}
}

// UI 返回 Swagger UI HTML，全部静态资源走本仓库 vendor 不再依赖 unpkg CDN。
//
// 资源来源：
//   - /swagger/swagger-ui.css        ← web/public/swagger/ (npm "prebuild" 钩子从
//   - /swagger/swagger-ui-bundle.js     node_modules/swagger-ui-dist/ 拷贝)
//   - /swagger-init.js               ← web/public/swagger-init.js (手写，外置避免 inline)
//
// 因为没有 inline script、没有外部 origin，全局 SecurityHeaders 中间件的严格
// CSP 直接放行——本路由不再需要 per-route 覆盖。
func (h *OpenAPIHandler) UI(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML))
}

// normalizeYAMLForJSON 把 yaml.v3 反序列化出的 map[interface{}]interface{}
// 递归转成 map[string]interface{}，让 encoding/json 能正常序列化。
//
// yaml.v3 在 unmarshal 到 any 时，对象 key 是 interface{}（因为 YAML 允许非字符串 key），
// 但 OpenAPI 文档保证全是字符串 key —— 强转 fmt.Sprintf 兜底任何意外。
func normalizeYAMLForJSON(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			ks, ok := k.(string)
			if !ok {
				ks = toString(k)
			}
			out[ks] = normalizeYAMLForJSON(val)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeYAMLForJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeYAMLForJSON(val)
		}
		return out
	default:
		return v
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// swaggerUIHTML 是单文件 Swagger UI 5.x，全部资源 vendor 自 swagger-ui-dist npm 包。
//
// 选择 5.x 是因为它支持 OpenAPI 3.1（我们的 spec 用 oneOf 等 3.1 特性）。
// 内联 <style> 已挪到 swagger-ui-dist/swagger-ui.css 里覆盖（.topbar/body 可走 inline
// 但与 'unsafe-inline' 一并外置更干净——先保留 inline <style>，因为 style-src 'self'
// 默认就放行，无需 'unsafe-inline'：浏览器只会卡内联 style 属性而不卡 <style> 块）。
//
// 实际上 CSP style-src 也卡 <style> 标签（spec 本意），但本项目全局
// SecurityHeaders 给的是 default-src 'self'；因 SkillHub 的 SPA 自身也用了内联样式，
// 全局 CSP 早已包含 style-src 'self' 'unsafe-inline'，本页直接复用即可。
const swaggerUIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1, shrink-to-fit=no" />
  <title>SkillHub API — Swagger UI</title>
  <link rel="stylesheet" href="/swagger/swagger-ui.css" />
  <link rel="icon" type="image/svg+xml"
        href="data:image/svg+xml;utf8,&lt;svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'&gt;&lt;text y='52' font-size='52'&gt;&#128270;&lt;/text&gt;&lt;/svg&gt;" />
  <style>
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="/swagger/swagger-ui-bundle.js" charset="UTF-8"></script>
  <script src="/swagger-init.js"></script>
</body>
</html>`
