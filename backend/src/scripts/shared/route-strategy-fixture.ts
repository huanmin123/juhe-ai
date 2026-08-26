import type { AccessScope } from '../../storage/access-scope.js'
import type { createApiKeyRecord, createRouteStrategy } from '../../storage/repositories.js'

type RepositoryFixtureApi = {
  createApiKeyRecord: typeof createApiKeyRecord
  createRouteStrategy: typeof createRouteStrategy
}

type ApiKeyFixtureInput = Record<string, unknown> & {
  groupBindings?: unknown[]
  routeStrategyId?: unknown
  routeMode?: unknown
  groupRouteStrategy?: unknown
  normalRoutingConfig?: unknown
  hybridRoutingConfig?: unknown
  name?: unknown
}

export function createApiKeyRecordWithRouteStrategy(
  repositories: RepositoryFixtureApi,
  input: ApiKeyFixtureInput,
  access?: AccessScope
): ReturnType<typeof createApiKeyRecord> {
  if (input.routeStrategyId || !Array.isArray(input.groupBindings)) {
    return repositories.createApiKeyRecord(input, access)
  }
  const routeStrategyInput: Record<string, unknown> = {
    name: routeStrategyFixtureName(input.name),
    mode: routeStrategyFixtureMode(input),
    groupBindings: input.groupBindings,
    hybridRoutingConfig: input.hybridRoutingConfig
  }
  if (Object.prototype.hasOwnProperty.call(input, 'normalRoutingConfig')) {
    routeStrategyInput.normalRoutingConfig = input.normalRoutingConfig
  }
  const routeStrategy = repositories.createRouteStrategy(routeStrategyInput, access)
  const {
    groupBindings: _groupBindings,
    routeMode: _routeMode,
    groupRouteStrategy: _groupRouteStrategy,
    normalRoutingConfig: _normalRoutingConfig,
    hybridRoutingConfig: _hybridRoutingConfig,
    ...apiKeyInput
  } = input
  return repositories.createApiKeyRecord({
    ...apiKeyInput,
    routeStrategyId: routeStrategy.id
  }, access)
}

function routeStrategyFixtureName(value: unknown): string {
  const base = typeof value === 'string' && value.trim() ? value.trim() : 'API Key'
  return `${base} 路由策略`
}

function routeStrategyFixtureMode(input: ApiKeyFixtureInput): 'normal' | 'hybrid_smart' | 'weighted' | 'failover' | 'round_robin' {
  if (input.routeMode === 'hybrid' || input.hybridRoutingConfig) return 'hybrid_smart'
  if (input.groupRouteStrategy === 'weighted_round_robin') return 'weighted'
  if (input.groupRouteStrategy === 'round_robin') return 'round_robin'
  const activeBindingCount = (input.groupBindings ?? []).filter((binding) => {
    if (!binding || typeof binding !== 'object' || Array.isArray(binding)) return false
    return (binding as { status?: unknown }).status !== 'disabled'
  }).length
  return (input.groupBindings?.length ?? 0) > 1 || activeBindingCount > 1 ? 'failover' : 'normal'
}
