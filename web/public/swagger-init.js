// Swagger UI 初始化脚本。
//
// 外置成单独 JS 文件是为了让 /api/docs 能在严格 CSP（无 'unsafe-inline'）下加载。
// 入口 HTML 由 pkg/handler/openapi_handler.go 提供，全部静态资源走本仓库
// vendor（web/public/swagger/* 由 npm run build 的 prebuild 钩子从
// node_modules/swagger-ui-dist/ 拷贝过来），因此与 unpkg CDN 解耦。
window.addEventListener('load', function () {
  window.ui = SwaggerUIBundle({
    url: '/api/v1/openapi.json',
    dom_id: '#swagger-ui',
    deepLinking: true,
    docExpansion: 'list',
    defaultModelsExpandDepth: 0,
    tryItOutEnabled: true,
    persistAuthorization: true,
    presets: [SwaggerUIBundle.presets.apis],
  });
});
