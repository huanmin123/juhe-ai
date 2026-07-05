import type {
  ApiKeyHybridRoutingConfig,
  RouteStrategyMode,
  RouteStrategyNormalRoutingConfig,
  RouteStrategyStatus
} from '../../domain/types.js'

export interface PublicRouteStrategyGroupBindingInput {
  groupId: string
  priority?: number
  weight?: number
  status?: 'active' | 'disabled'
}

export interface PublicRouteStrategyAddInput {
  targetUsername: string
  name: string
  description?: string | null
  mode?: RouteStrategyMode
  status?: RouteStrategyStatus
  groupBindings: PublicRouteStrategyGroupBindingInput[]
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig | null
  hybridRoutingConfig?: Record<string, unknown> | null
}

export interface PublicRouteStrategyUpdateInput {
  targetUsername?: string
  routeStrategyId: string
  name?: string
  description?: string | null
  mode?: RouteStrategyMode
  status?: RouteStrategyStatus
  groupBindings?: PublicRouteStrategyGroupBindingInput[]
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig | null
  hybridRoutingConfig?: Record<string, unknown> | null
}

export interface PublicRouteStrategyDeleteInput {
  targetUsername?: string
  routeStrategyId: string
}

export interface PublicRouteStrategyListInput {
  targetUsername: string
  keyword?: string
  mode?: RouteStrategyMode | 'all'
  status?: RouteStrategyStatus | 'all'
  page?: number
  pageSize?: number
}

export interface PublicRouteStrategyGroupBindingSummary {
  id: string
  groupId: string
  groupName?: string
  providerCode?: string
  priority: number
  weight: number
  status: 'active' | 'disabled'
  groupEnabled: boolean
}

export interface PublicRouteStrategySummary {
  id: string
  name: string
  description?: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
  isDefault: boolean
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig
  hybridRoutingConfig?: ApiKeyHybridRoutingConfig
  groupBindings: PublicRouteStrategyGroupBindingSummary[]
  apiKeyCount?: number
  createdAt: string
  updatedAt: string
}

export interface PublicRouteStrategyResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'updated' | 'deleted' | 'not_found' | 'mock'
  target: {
    username: string
    displayName: string
    systemAccountId: string
    created: boolean
  }
  routeStrategy: PublicRouteStrategySummary | null
}

export interface PublicRouteStrategyListResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  target: PublicRouteStrategyResponse['target']
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicRouteStrategySummary[]
}
