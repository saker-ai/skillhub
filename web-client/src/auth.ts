import { appBasePath, appURL } from '@saker/web-shared/base-path';

function skillHubURL(path: string): string {
  return appURL(appBasePath(import.meta.env.BASE_URL), path);
}

const WHOAMI_PATH = '/api/v1/whoami';

export interface User {
  id: string;
  handle: string;
  role: string;
}

export async function whoami(): Promise<User | null> {
  try {
    const res = await fetch(skillHubURL(WHOAMI_PATH), { credentials: 'same-origin' });
    if (!res.ok) return null;
    return res.json();
  } catch {
    return null;
  }
}

export async function login(handle: string, password: string): Promise<void> {
  const body = new URLSearchParams({ handle, password });
  const res = await fetch(skillHubURL('/login'), {
    method: 'POST',
    body,
    credentials: 'same-origin',
  });
  if (res.ok) return;
  const data = await res.json().catch(() => ({}));
  throw new Error(data.error || 'Invalid username or password');
}

export async function logout(): Promise<void> {
  await fetch(skillHubURL('/logout'), { method: 'POST', credentials: 'same-origin', redirect: 'manual' });
}
