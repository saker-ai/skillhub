import { apiFetch, apiFetchRaw, apiUploadWithProgress, apiURL } from './client';

export interface Skill {
  id: string;
  slug: string;
  displayName?: string;
  summary?: string;
  tags: string[];
  downloads: number;
  installs: number;
  starsCount: number;
  versionsCount: number;
  ownerHandle: string;
  namespaceSlug: string;
  createdAt: string;
  updatedAt: string;
}

export interface SkillVersion {
  id: string;
  skillId: string;
  version: string;
  files: string[];
  createdAt: string;
}

export interface SkillListResponse {
  data: Skill[];
  nextCursor: string;
}

export function listSkills(limit = 20, cursor = '', sort = 'created', namespace = ''): Promise<SkillListResponse> {
  const params = new URLSearchParams({ limit: String(limit), sort });
  if (cursor) params.set('cursor', cursor);
  if (namespace) params.set('namespace', namespace);
  return apiFetch(`/skills?${params}`);
}

function skillPath(slug: string, namespace?: string): string {
  if (namespace) return `/skills/@${namespace}/${slug}`;
  return `/skills/${slug}`;
}

export function getSkill(slug: string, namespace?: string): Promise<Skill> {
  return apiFetch(skillPath(slug, namespace));
}

export function getVersions(slug: string, namespace?: string): Promise<{ versions: SkillVersion[] }> {
  return apiFetch(`${skillPath(slug, namespace)}/versions`);
}

export function getFile(slug: string, version: string, path: string, namespace?: string): Promise<Response> {
  const params = new URLSearchParams({ version, path });
  return apiFetchRaw(`${skillPath(slug, namespace)}/file?${params}`);
}

export function publishSkill(formData: FormData): Promise<{ skill: Skill; version: SkillVersion }> {
  return apiFetch('/skills', { method: 'POST', body: formData });
}

export function publishSkillWithProgress(formData: FormData, onProgress: (pct: number) => void): Promise<{ skill: Skill; version: SkillVersion }> {
  return apiUploadWithProgress('/skills', formData, onProgress);
}

export function skillDownloadURL(slug: string, version: string, namespace = ''): string {
  const params = new URLSearchParams({ slug, version });
  if (namespace) params.set('namespace', namespace);
  return apiURL(`/download?${params}`);
}

export function deleteSkill(slug: string, namespace?: string): Promise<void> {
  return apiFetch(skillPath(slug, namespace), { method: 'DELETE' });
}
