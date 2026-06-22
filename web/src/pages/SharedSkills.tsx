import { StandaloneNativeAppPage } from '@saker/web-shared/pages'

export default function SharedSkills() {
  return <StandaloneNativeAppPage appId="skillhub" apiBaseUrl="/api/v1" proxyHref="/skills" />
}
