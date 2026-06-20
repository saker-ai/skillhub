import { NativeAppPage } from '../../../../web-shared/src/pages'
import { createStandaloneHost, nativeRoutes } from '../../../../web-shared/src/runtime'

const route = nativeRoutes.find((item) => item.appId === 'skillhub')

export default function SharedSkills() {
  if (!route) {
    return <p className="muted">Shared SkillHub route is not configured.</p>
  }
  const host = createStandaloneHost({ appId: 'skillhub', apiBaseUrl: '/api', proxyHref: '/skills' })
  return <NativeAppPage host={host} route={route} />
}
