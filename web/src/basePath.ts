import { appAbsoluteURL, appBasePath, appURL } from '../../../web-shared/src/base-path';

export function skillHubBasePath(): string {
  return appBasePath(import.meta.env.BASE_URL);
}

export function skillHubURL(path: string): string {
  return appURL(skillHubBasePath(), path);
}

export function skillHubAbsoluteURL(path: string): string {
  return appAbsoluteURL(skillHubBasePath(), path);
}
