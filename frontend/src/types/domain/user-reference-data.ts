import type { RouteStrategyMode, RouteStrategyStatus } from './access'
import type { ProviderCode } from './base'

export interface UserDefaultGroupReference {
  id: string
  name: string
}

export interface UserDefaultRouteStrategyReference {
  id: string
  name: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
}

export interface UserProviderDefaultReference {
  providerCode: ProviderCode
  defaultGroup: UserDefaultGroupReference
  /** Go uibootstrap 恒随每个 provider 默认项下发（与 preferredDefaultRouteStrategy 并存），前端当前不读，属契约镜像。 */
  defaultRouteStrategy?: UserDefaultRouteStrategyReference
}

export interface UserReferenceData {
  systemAccountId: string
  providerDefaults: UserProviderDefaultReference[]
  preferredDefaultRouteStrategy?: UserDefaultRouteStrategyReference
}
