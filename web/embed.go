package web

import "embed"

//go:embed all:static
var StaticFS embed.FS

// OpenAPISpec is the hand-maintained OpenAPI 3.1 description of the SkillHub HTTP
// API, exposed at /api/v1/openapi.yaml (raw) and rendered as Swagger UI at /api/docs.
//
// 编辑指南：
//   - 这是一个**人工维护**的契约——新增/修改 handler 时同步更新此文件；
//   - 验证可以用 https://editor.swagger.io 粘贴或本地 `npx @redocly/cli lint`；
//   - 不要让它变成日志，每条路径都应可执行（curl 示例参见 /skills.md）。
//
//go:embed openapi.yaml
var OpenAPISpec []byte
