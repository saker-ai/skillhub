import { apiFetch } from './client';

// TeamToken mirrors model.APIToken's HTTP-visible fields. NamespaceID is
// always present on team tokens — the server's GetByNamespaceID filters
// personal tokens out.
export interface TeamToken {
  id: string;
  prefix: string;
  label?: string;
  scope: 'read' | 'publish';
  namespaceId: string;
  createdAt: string;
  expiresAt?: string;
  lastUsedAt?: string;
  revokedAt?: string;
}

export interface CreateTeamTokenResponse {
  // Raw token shown ONCE — show prominently in UI, never re-fetchable.
  token: string;
  metadata: TeamToken;
}

export function listTeamTokens(slug: string): Promise<{ data: TeamToken[] }> {
  return apiFetch(`/namespaces/${encodeURIComponent(slug)}/tokens`);
}

// expiresIn is REQUIRED server-side; UI surfaces this as a non-optional input.
// Format follows time.ParseDuration ("720h", "24h", "30m").
export function createTeamToken(
  slug: string,
  body: { label?: string; scope?: 'read' | 'publish'; expiresIn: string },
): Promise<CreateTeamTokenResponse> {
  return apiFetch(`/namespaces/${encodeURIComponent(slug)}/tokens`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

export function revokeTeamToken(slug: string, tokenId: string): Promise<void> {
  return apiFetch(
    `/namespaces/${encodeURIComponent(slug)}/tokens/${encodeURIComponent(tokenId)}`,
    { method: 'DELETE' },
  );
}
