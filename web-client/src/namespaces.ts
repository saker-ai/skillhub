import { apiFetch } from './client';

export interface Namespace {
  id: string;
  slug: string;
  displayName?: string;
  description?: string;
  ownerId: string;
  type: 'personal' | 'team';
  status: string;
  createdAt: string;
  updatedAt: string;
}

export interface NamespaceMember {
  id: string;
  namespaceId: string;
  userId: string;
  role: 'owner' | 'admin' | 'member';
  handle: string;
  displayName?: string;
  createdAt: string;
}

export function listMyNamespaces(): Promise<{ data: Namespace[] }> {
  return apiFetch('/namespaces');
}

export function getNamespace(slug: string): Promise<Namespace> {
  return apiFetch(`/namespaces/${slug}`);
}

export function createNamespace(body: {
  slug: string;
  displayName?: string;
  description?: string;
  type?: 'personal' | 'team';
}): Promise<Namespace> {
  return apiFetch('/namespaces', { method: 'POST', body: JSON.stringify(body) });
}

export function listMembers(slug: string): Promise<{ data: NamespaceMember[] }> {
  return apiFetch(`/namespaces/${slug}/members`);
}

export function addMember(slug: string, handle: string, role = 'member'): Promise<void> {
  return apiFetch(`/namespaces/${slug}/members`, {
    method: 'POST',
    body: JSON.stringify({ handle, role }),
  });
}

export function removeMember(slug: string, handle: string): Promise<void> {
  return apiFetch(`/namespaces/${slug}/members/${handle}`, { method: 'DELETE' });
}

export function deleteNamespace(slug: string): Promise<void> {
  return apiFetch(`/namespaces/${slug}`, { method: 'DELETE' });
}
