function normalizeBasePath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed || trimmed === '/' || trimmed === '.' || trimmed === './') return '';
  return `/${trimmed.replace(/^\/+|\/+$/g, '')}`;
}

function basePathFromScript(): string {
  const scripts = Array.from(document.querySelectorAll<HTMLScriptElement>('script[src]')).reverse();
  for (const script of scripts) {
    try {
      const src = new URL(script.src, window.location.href);
      const marker = '/assets/';
      const index = src.pathname.lastIndexOf(marker);
      if (index >= 0) return normalizeBasePath(src.pathname.slice(0, index));
    } catch {
      // Ignore malformed script URLs and continue looking for the Vite entry.
    }
  }
  return '';
}

export function skillHubBasePath(): string {
  const viteBase = normalizeBasePath(import.meta.env.BASE_URL);
  if (viteBase) return viteBase;
  return basePathFromScript();
}

export function skillHubURL(path: string): string {
  const base = skillHubBasePath();
  return `${base}${path.startsWith('/') ? path : `/${path}`}`;
}

export function skillHubAbsoluteURL(path: string): string {
  return `${window.location.origin}${skillHubURL(path)}`;
}
