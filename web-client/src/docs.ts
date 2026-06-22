export const SKILLHUB_PUBLISH_CURL_TEMPLATE = (baseURL: string) =>
  `curl -X POST ${baseURL}/api/v1/skills \\\n  -H "Authorization: Bearer YOUR_TOKEN" \\\n  -F "slug=my-skill" \\\n  -F "version=1.0.0" \\\n  -F "displayName=My Skill" \\\n  -F "summary=A brief description" \\\n  -F "tags=python,automation" \\\n  -F "category=general" \\\n  -F "changelog=Initial release" \\\n  -F "files=@SKILL.md" \\\n  -F "files=@prompt.md"`

export type ApiEndpointRow = [endpoint: string, method: string, descriptionKey: string]

export const SKILLHUB_API_ENDPOINTS: readonly ApiEndpointRow[] = [
  ['POST /api/v1/skills', 'Multipart', 'publish.api_publish'],
  ['GET /api/v1/skills', 'JSON', 'publish.api_list'],
  ['GET /api/v1/skills/:slug', 'JSON', 'publish.api_detail'],
  ['GET /api/v1/download', 'ZIP', 'publish.api_download'],
  ['GET /api/v1/search', 'JSON', 'publish.api_search'],
  ['GET /.well-known/clawhub.json', 'JSON', 'publish.api_discovery']
] as const
