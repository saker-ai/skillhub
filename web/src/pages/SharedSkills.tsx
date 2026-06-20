import { StandaloneNativeAppPage } from '../../../../web-shared/src/pages'

export default function SharedSkills() {
  return <StandaloneNativeAppPage appId="skillhub" apiBaseUrl="/api/v1" proxyHref="/skills" />
}
