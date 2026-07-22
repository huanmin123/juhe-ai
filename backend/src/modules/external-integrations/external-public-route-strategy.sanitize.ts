import type { RouteStrategySummary, SystemAccountSummary } from '../../domain/types.js'
import type {
  PublicRouteStrategyListResponse,
  PublicRouteStrategyResponse,
  PublicRouteStrategySummary
} from './external-public-route-strategy.types.js'

export type PublicRouteStrategyResolvedTarget = {
  account: SystemAccountSummary
  created: boolean
}

export function publicRouteStrategyTargetSummary(target: PublicRouteStrategyResolvedTarget): PublicRouteStrategyResponse['target'] {
  return {
    username: target.account.username,
    displayName: target.account.displayName,
    systemAccountId: target.account.id,
    created: target.created
  }
}

export function publicRouteStrategyListResponse(
  target: PublicRouteStrategyResolvedTarget,
  page: Pick<PublicRouteStrategyListResponse, 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'items'>
): PublicRouteStrategyListResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    target: publicRouteStrategyTargetSummary(target),
    ...page
  }
}

export function publicRouteStrategyResponse(
  action: PublicRouteStrategyResponse['action'],
  target: PublicRouteStrategyResolvedTarget,
  routeStrategy: PublicRouteStrategySummary | null
): PublicRouteStrategyResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action,
    target: publicRouteStrategyTargetSummary(target),
    routeStrategy
  }
}

export function publicRouteStrategyNotFoundResponse(usernameInput?: string): PublicRouteStrategyResponse {
  const username = normalizedText(usernameInput) || ''
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'not_found',
    target: {
      username,
      displayName: username,
      systemAccountId: '',
      created: false
    },
    routeStrategy: null
  }
}

export function sanitizeRouteStrategy(routeStrategy: RouteStrategySummary): PublicRouteStrategySummary {
  return {
    id: routeStrategy.id,
    name: routeStrategy.name,
    description: routeStrategy.description,
    mode: routeStrategy.mode,
    status: routeStrategy.status,
    isDefault: routeStrategy.isDefault,
    normalRoutingConfig: routeStrategy.normalRoutingConfig,
    hybridRoutingConfig: routeStrategy.hybridRoutingConfig,
    groupBindings: routeStrategy.groupBindings.map((binding) => ({
      id: binding.id,
      groupId: binding.groupId,
      groupName: binding.groupName,
      providerCode: binding.providerCode,
      priority: binding.priority,
      weight: binding.weight,
      status: binding.status,
      groupEnabled: binding.groupEnabled
    })),
    apiKeyCount: routeStrategy.apiKeyCount,
    createdAt: routeStrategy.createdAt,
    updatedAt: routeStrategy.updatedAt
  }
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
