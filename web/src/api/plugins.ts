import { apiFetch, apiFetchRaw } from './client';

export interface Plugin {
  id: string;
  slug: string;
  displayName?: string;
  summary?: string;
  visibility: string;
  category: string;
  tags: string[];
  downloads: number;
  starsCount: number;
  versionsCount?: number;
  ownerHandle: string;
  latestVersionId?: string;
  createdAt: string;
  updatedAt: string;
}

export interface PluginVersion {
  id: string;
  pluginId: string;
  version: string;
  fingerprint: string;
  changelog?: string;
  createdAt: string;
  yankedAt?: string;
  yankReason?: string;
}

export interface PluginListResponse {
  data: Plugin[];
  nextCursor: string;
}

export function listPlugins(limit = 20, cursor = '', category = ''): Promise<PluginListResponse> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (cursor) params.set('cursor', cursor);
  if (category) params.set('category', category);
  return apiFetch(`/plugins?${params}`);
}

export function getPlugin(slug: string): Promise<Plugin> {
  return apiFetch(`/plugins/${slug}`);
}

export function getPluginVersions(slug: string): Promise<{ versions: PluginVersion[] }> {
  return apiFetch(`/plugins/${slug}/versions`);
}

export function getPluginFile(slug: string, version: string, path: string): Promise<Response> {
  const params = new URLSearchParams({ slug, version, path });
  return apiFetchRaw(`/plugins/file?${params}`);
}
