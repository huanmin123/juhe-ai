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
  defaultRouteStrategy?: UserDefaultRouteStrategyReference
}

export interface UserReferenceData {
  systemAccountId: string
  providerDefaults: UserProviderDefaultReference[]
  preferredDefaultRouteStrategy?: UserDefaultRouteStrategyReference
}
