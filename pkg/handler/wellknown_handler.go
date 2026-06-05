package handler

import (
	"net/http"
	"strings"

	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/gin-gonic/gin"
)

type WellKnownHandler struct {
	cfg *config.Config
}

func NewWellKnownHandler(cfg *config.Config) *WellKnownHandler {
	return &WellKnownHandler{cfg: cfg}
}

// ClawHubJSON handles GET /.well-known/clawhub.json
func (h *WellKnownHandler) ClawHubJSON(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"registryUrl": h.cfg.Server.BaseURL,
		"apiVersion":  "v1",
		"endpoints": gin.H{
			"search":       "/api/v1/search",
			"skills":       "/api/v1/skills",
			"download":     "/api/v1/download",
			"resolve":      "/api/v1/resolve",
			"whoami":       "/api/v1/whoami",
			"publish":      "/api/v1/skills",
			"installGuide": "/skills.md",
			"openapi":      "/api/v1/openapi.yaml",
			"apiDocs":      "/api/docs",
		},
	})
}

// InstallMarkdown handles GET /skills.md.
//
// 面向 AI agent 的"操作指南"——返回一份纯 markdown，用 curl 示例完整展示
// 如何在不依赖任何 SDK 的前提下使用本 SkillHub 实例：
//
//   - 探活 + 发现 (/healthz, /.well-known/clawhub.json)
//   - 搜索 / 列表 / 详情 / 单文件
//   - 下载技能压缩包（含 ETag 缓存）
//   - device flow 完成 CLI 风格鉴权
//   - 鉴权后调用 whoami / publish
//
// 典型用法：用户对 agent 说
//
//	Read https://<host>/skills.md and follow the instructions to install skillhub
//
// agent 拿到本响应后即可照做，无需先读 README 或源码。
//
// 注意事项：
//   - 端点公开、无鉴权——指南本身只描述如何调用，不泄露任何用户数据。
//   - base URL 在运行时根据 cfg.Server.BaseURL 优先；若未设置则按
//     X-Forwarded-Proto + Host 拼出，确保反代场景下也能产生可点击链接。
//   - 加 5 分钟 Cache-Control，避免频繁渲染同样内容。
func (h *WellKnownHandler) InstallMarkdown(c *gin.Context) {
	base := strings.TrimRight(h.cfg.Server.BaseURL, "/")
	if base == "" {
		scheme := "http"
		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		base = scheme + "://" + c.Request.Host
	}

	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(renderInstallMarkdown(base)))
}

func renderInstallMarkdown(base string) string {
	var b strings.Builder
	b.Grow(8 * 1024)

	b.WriteString("# SkillHub — Agent Operations Guide\n\n")
	b.WriteString("This document describes how to interact with this SkillHub registry using only `curl`. ")
	b.WriteString("It is designed to be read by an AI agent: every section contains a runnable command and ")
	b.WriteString("the shape of the expected response. Follow it top-to-bottom for first-time setup.\n\n")
	b.WriteString("- **Base URL**: `" + base + "`\n")
	b.WriteString("- **API version**: `v1` (all JSON endpoints live under `/api/v1`)\n")
	b.WriteString("- **Auth model**: bearer token in `Authorization: Bearer <token>` header\n")
	b.WriteString("- **Token acquisition**: device-flow (no browser required on the agent host)\n\n")

	b.WriteString("## 0. Sanity check (no auth)\n\n")
	b.WriteString("Verify the server is reachable and discover the public endpoint map.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS " + base + "/healthz\n")
	b.WriteString("# {\"status\":\"ok\"}\n\n")
	b.WriteString("curl -fsS " + base + "/.well-known/clawhub.json\n")
	b.WriteString("# {\"registryUrl\":\"" + base + "\",\"apiVersion\":\"v1\",\"endpoints\":{...}}\n")
	b.WriteString("```\n\n")
	b.WriteString("If `/healthz` returns anything other than 200, stop here — the registry is unhealthy.\n\n")

	b.WriteString("## 1. Discover skills (no auth)\n\n")
	b.WriteString("### Search by keyword\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/search?q=kubernetes&limit=20\"\n")
	b.WriteString("```\n\n")
	b.WriteString("### List skills (paginated, sorted)\n\n")
	b.WriteString("`sort` ∈ {`created` (default), `downloads`, `stars`, `updated`}; `cursor` is opaque, copy from previous response's `nextCursor`.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/skills?sort=downloads&limit=20\"\n")
	b.WriteString("# {\"data\":[{\"slug\":\"...\",\"summary\":\"...\",\"latestVersion\":\"1.2.3\", ...}], \"nextCursor\":\"...\"}\n")
	b.WriteString("```\n\n")
	b.WriteString("### Inspect one skill\n\n")
	b.WriteString("Skills are namespace-scoped. Use `@<namespace>/<slug>` for unambiguous access:\n\n")
	b.WriteString("```bash\n")
	b.WriteString("# Namespace-qualified (recommended)\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/skills/@alice/my-skill\"\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/skills/@alice/my-skill/versions\"\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/skills/@alice/my-skill/versions/1.2.3\"\n\n")
	b.WriteString("# Legacy (bare slug) — works when the slug is globally unique.\n")
	b.WriteString("# Returns 409 with a candidates list if ambiguous.\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/skills/<slug>\"\n")
	b.WriteString("```\n\n")
	b.WriteString("### Read a single file inside a skill (default `SKILL.md`)\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/skills/@<namespace>/<slug>/file?version=latest&path=SKILL.md\"\n")
	b.WriteString("```\n\n")

	b.WriteString("## 2. Download a skill\n\n")
	b.WriteString("Returns a `.zip` of the version's directory tree. Honors `If-None-Match` for cheap revalidation — store the `ETag` from the previous response and pass it back.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsSL -o <slug>.zip -D headers.txt \\\n")
	b.WriteString("  \"" + base + "/api/v1/download?slug=<slug>&namespace=<namespace>&version=latest\"\n\n")
	b.WriteString("# Subsequent calls with the saved ETag → 304 + empty body if unchanged:\n")
	b.WriteString("ETAG=$(awk '/^ETag:/ {print $2}' headers.txt)\n")
	b.WriteString("curl -fsS -o /dev/null -w '%{http_code}\\n' -H \"If-None-Match: $ETAG\" \\\n")
	b.WriteString("  \"" + base + "/api/v1/download?slug=<slug>&version=latest\"\n")
	b.WriteString("```\n\n")
	b.WriteString("Response headers worth caching: `ETag`, `X-Skill-Version`, `X-Skill-Fingerprint`.\n\n")
	b.WriteString("### Resolve by fingerprint (reverse lookup)\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS \"" + base + "/api/v1/resolve?fingerprint=<sha256-hex>\"\n")
	b.WriteString("# {\"version\": {...}, \"skill\": {...}}\n")
	b.WriteString("```\n\n")

	b.WriteString("## 3. Authenticate (device flow)\n\n")
	b.WriteString("No browser on the agent host? Use device flow. Three steps:\n\n")
	b.WriteString("**Step 3a — Request a device code:**\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS -X POST " + base + "/api/v1/auth/device/code\n")
	b.WriteString("# {\n")
	b.WriteString("#   \"deviceCode\":      \"<opaque-device-code>\",\n")
	b.WriteString("#   \"userCode\":        \"ABCD-EFGH\",\n")
	b.WriteString("#   \"verificationUri\": \"" + base + "/auth/device/verify\",\n")
	b.WriteString("#   \"expiresIn\":       900,\n")
	b.WriteString("#   \"interval\":        5\n")
	b.WriteString("# }\n")
	b.WriteString("```\n\n")
	b.WriteString("**Step 3b — Have the user open `verificationUri` in any browser, log in, and submit `userCode`.** ")
	b.WriteString("This step requires a human; the agent cannot complete it alone.\n\n")
	b.WriteString("**Step 3c — Poll for the token (every `interval` seconds, until `expiresIn` elapses):**\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS -X POST -H 'Content-Type: application/json' \\\n")
	b.WriteString("  -d '{\"deviceCode\":\"<opaque-device-code>\"}' \\\n")
	b.WriteString("  " + base + "/api/v1/auth/device/token\n")
	b.WriteString("# 202 → {\"status\":\"pending\"}   (still waiting on user)\n")
	b.WriteString("# 200 → {\"token\":\"clh_...\"}    (success — store this token)\n")
	b.WriteString("```\n\n")
	b.WriteString("Save the returned `token` value to `$SKILLHUB_TOKEN` (or to a config file like `~/.config/skillhub/config.json`).\n\n")

	b.WriteString("## 4. Authenticated calls\n\n")
	b.WriteString("All authenticated endpoints expect `Authorization: Bearer $SKILLHUB_TOKEN`.\n\n")
	b.WriteString("### Whoami (verify the token)\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS -H \"Authorization: Bearer $SKILLHUB_TOKEN\" \\\n")
	b.WriteString("  " + base + "/api/v1/whoami\n")
	b.WriteString("# {\"id\":\"<uuid>\",\"handle\":\"...\",\"role\":\"user\", ...}\n")
	b.WriteString("```\n\n")
	b.WriteString("### Star / unstar\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS -X POST    -H \"Authorization: Bearer $SKILLHUB_TOKEN\" " + base + "/api/v1/stars/<slug>\n")
	b.WriteString("curl -fsS -X DELETE -H \"Authorization: Bearer $SKILLHUB_TOKEN\" " + base + "/api/v1/stars/<slug>\n")
	b.WriteString("```\n\n")

	b.WriteString("## 5. Publish a new skill version\n\n")
	b.WriteString("`POST /api/v1/skills` is `multipart/form-data`. Required fields: `slug`, `version` (semver), and at least one file (`SKILL.md` is mandatory inside the upload).\n\n")
	b.WriteString("Optional fields: `displayName`, `summary`, `category`, `tags` (comma-separated), `kind`, `visibility` (`public|private`), `namespace` (defaults to your personal namespace if omitted), `changelog`, `dependencies` (JSON array of `{slug,versionRange}`), `signature` (cosign bundle, ≤ 64 KB).\n\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -fsS -X POST -H \"Authorization: Bearer $SKILLHUB_TOKEN\" \\\n")
	b.WriteString("  -F slug=my-skill \\\n")
	b.WriteString("  -F version=0.1.0 \\\n")
	b.WriteString("  -F summary='one-line description' \\\n")
	b.WriteString("  -F category=devops \\\n")
	b.WriteString("  -F tags='kubernetes,linting' \\\n")
	b.WriteString("  -F 'changelog=Initial release' \\\n")
	b.WriteString("  -F 'files=@./SKILL.md' \\\n")
	b.WriteString("  -F 'files=@./script.sh' \\\n")
	b.WriteString("  " + base + "/api/v1/skills\n")
	b.WriteString("# {\"skill\": {...}, \"version\": {...}}\n")
	b.WriteString("```\n\n")
	b.WriteString("After publishing, search the index until the new version appears (Bleve indexing is asynchronous — typically <1 s).\n\n")

	b.WriteString("## 5b. Update a single file (auto-bump patch version)\n\n")
	b.WriteString("To update just one file (e.g. fix a typo in SKILL.md) without re-uploading everything, ")
	b.WriteString("use `PUT /api/v1/skills/@<namespace>/<slug>/file`. The server reads all existing files from the ")
	b.WriteString("latest version, replaces the target file, and publishes a new version with the patch number incremented.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("# JSON body\n")
	b.WriteString("curl -fsS -X PUT -H \"Authorization: Bearer $SKILLHUB_TOKEN\" \\\n")
	b.WriteString("  -H 'Content-Type: application/json' \\\n")
	b.WriteString("  -d '{\"path\":\"SKILL.md\",\"content\":\"---\\nname: my-skill\\n---\\n# Updated content\\n\",\"changelog\":\"fix typo\"}' \\\n")
	b.WriteString("  " + base + "/api/v1/skills/@<namespace>/<slug>/file\n\n")
	b.WriteString("# Or multipart\n")
	b.WriteString("curl -fsS -X PUT -H \"Authorization: Bearer $SKILLHUB_TOKEN\" \\\n")
	b.WriteString("  -F path=SKILL.md -F 'file=@./SKILL.md' -F 'changelog=fix typo' \\\n")
	b.WriteString("  " + base + "/api/v1/skills/@<namespace>/<slug>/file\n")
	b.WriteString("# → {\"skill\": {...}, \"version\": {\"version\": \"1.0.1\", ...}}\n")
	b.WriteString("```\n\n")

	b.WriteString("## 5b. Team (namespace) tokens\n\n")
	b.WriteString("Personal tokens authorize their creator only. To let multiple humans (or CI runners) ")
	b.WriteString("publish under a shared namespace without sharing personal credentials, mint a **team token** ")
	b.WriteString("scoped to that namespace. Only namespace `owner` / `admin` can issue or list them; any namespace ")
	b.WriteString("member can revoke their own.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("# Create a team token (owner / admin only)\n")
	b.WriteString("curl -fsS -X POST -H \"Authorization: Bearer $SKILLHUB_TOKEN\" \\\n")
	b.WriteString("  -H 'Content-Type: application/json' \\\n")
	b.WriteString("  -d '{\"label\":\"ci-runner\",\"scope\":\"publish\",\"expiresIn\":\"720h\"}' \\\n")
	b.WriteString("  " + base + "/api/v1/namespaces/<slug>/tokens\n")
	b.WriteString("# 201 → {\"token\":\"clh_...\",\"metadata\":{...}}  (token shown ONCE — store it now)\n\n")
	b.WriteString("# List active tokens for a namespace\n")
	b.WriteString("curl -fsS -H \"Authorization: Bearer $SKILLHUB_TOKEN\" \\\n")
	b.WriteString("  " + base + "/api/v1/namespaces/<slug>/tokens\n\n")
	b.WriteString("# Revoke (owner/admin can revoke any; members can revoke their own)\n")
	b.WriteString("curl -fsS -X DELETE -H \"Authorization: Bearer $SKILLHUB_TOKEN\" \\\n")
	b.WriteString("  " + base + "/api/v1/namespaces/<slug>/tokens/<token-id>\n")
	b.WriteString("```\n\n")
	b.WriteString("**Constraint**: a team token can only operate on skills that live in its namespace. ")
	b.WriteString("Calls that target a personal skill or a skill in a different namespace are rejected with `403`. ")
	b.WriteString("Publishing requires `namespace=<slug>` in the multipart form, and the slug must match the token's bound namespace.\n\n")

	b.WriteString("## 6. Error and rate-limit conventions\n\n")
	b.WriteString("- All errors are JSON: `{\"error\":\"<message>\"}`\n")
	b.WriteString("- `400` — bad request (malformed input, missing required field, validation failure)\n")
	b.WriteString("- `401` — missing or invalid token (re-run device flow)\n")
	b.WriteString("- `403` — authorization failure. Common causes:\n")
	b.WriteString("    - personal token used to publish/modify a skill in a namespace you don't belong to\n")
	b.WriteString("    - team token used outside its bound namespace (see §5b)\n")
	b.WriteString("    - non-owner attempting an owner-only operation (transfer, namespace delete)\n")
	b.WriteString("    - non-owner/admin attempting a member-management operation\n")
	b.WriteString("  Error messages for these cases are prefixed with `forbidden:`.\n")
	b.WriteString("- `404` — skill / version / file not found\n")
	b.WriteString("- `409` — conflict. Common causes:\n")
	b.WriteString("    - ambiguous bare slug: multiple namespaces contain the same slug.\n")
	b.WriteString("      Response: `{\"error\":\"ambiguous slug\",\"candidates\":[{\"namespace\":\"@alice\",...},...]}`.\n")
	b.WriteString("      Re-issue the request with a namespace-qualified path: `/api/v1/skills/@<namespace>/<slug>`.\n")
	b.WriteString("    - namespace already at the team-token quota; revoke an unused one before retrying\n")
	b.WriteString("- `429` — rate-limited; back off and retry with exponential delay\n")
	b.WriteString("- `5xx` — registry-side problem; retry with jitter, fail loudly after 3 attempts\n\n")

	b.WriteString("## 7. Discovery shortcut\n\n")
	b.WriteString("All public endpoints are also listed at `" + base + "/.well-known/clawhub.json`. Re-fetch that document when in doubt about which paths exist on this server.\n")

	return b.String()
}
