import type {
  PublicRouteStrategyAddInput,
  PublicRouteStrategyDeleteInput,
  PublicRouteStrategyGroupBindingInput,
  PublicRouteStrategyListInput,
  PublicRouteStrategyListResponse,
  PublicRouteStrategyResponse,
  PublicRouteStrategySummary,
  PublicRouteStrategyUpdateInput
} from './external-public-route-strategy.types.js'

export function mockPublicRouteStrategyAdd(input: PublicRouteStrategyAddInput): PublicRouteStrategyResponse {
  return publicMockRouteStrategyResponse('mock', input.targetUsername, mockRouteStrategySummary({
    id: 'mock_route_strategy_public',
    name: normalizedText(input.name) || '公开接口策略路由',
    mode: input.mode ?? 'normal',
    status: input.status ?? 'active',
    groupBindings: input.groupBindings,
    normalRoutingConfig: input.normalRoutingConfig
  }))
}

export function mockPublicRouteStrategyUpdate(input: PublicRouteStrategyUpdateInput): PublicRouteStrategyResponse {
  return publicMockRouteStrategyResponse('mock', input.targetUsername, mockRouteStrategySummary({
    id: normalizedText(input.routeStrategyId) || 'mock_route_strategy_public',
    name: normalizedText(input.name) || '公开接口策略路由',
    mode: input.mode ?? 'normal',
    status: input.status ?? 'active',
    groupBindings: input.groupBindings,
    normalRoutingConfig: input.normalRoutingConfig
  }))
}

export function mockPublicRouteStrategyDelete(input: PublicRouteStrategyDeleteInput): PublicRouteStrategyResponse {
  return publicMockRouteStrategyResponse('mock', input.targetUsername, mockRouteStrategySummary({
    id: normalizedText(input.routeStrategyId) || 'mock_route_strategy_public',
    name: '公开接口策略路由',
    mode: 'normal',
    status: 'disabled'
  }))
}

export function mockPublicRouteStrategyList(input: PublicRouteStrategyListInput): PublicRouteStrategyListResponse {
  const username = normalizedText(input.targetUsername) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    page: input.page ?? 1,
    pageSize: input.pageSize ?? 20,
    pageUpperBound: 1,
    hasMore: false,
    items: [
      mockRouteStrategySummary({
        id: 'mock_route_strategy_public',
        name: normalizedText(input.keyword) || '公开接口策略路由',
        mode: input.mode && input.mode !== 'all' ? input.mode : 'normal',
        status: input.status && input.status !== 'all' ? input.status : 'active'
      })
    ]
  }
}

function publicMockRouteStrategyResponse(
  action: PublicRouteStrategyResponse['action'],
  usernameInput: string | undefined,
  routeStrategy: PublicRouteStrategySummary
): PublicRouteStrategyResponse {
  const username = normalizedText(usernameInput) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    action,
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    routeStrategy
  }
}

function mockRouteStrategySummary(input: {
  id: string
  name: string
  mode: PublicRouteStrategySummary['mode']
  status: PublicRouteStrategySummary['status']
  groupBindings?: PublicRouteStrategyGroupBindingInput[]
  normalRoutingConfig?: PublicRouteStrategySummary['normalRoutingConfig'] | null
}): PublicRouteStrategySummary {
  const now = new Date().toISOString()
  const bindings = input.groupBindings?.length
    ? input.groupBindings
    : [{ groupId: 'mock_group_public', priority: 1, weight: 100, status: 'active' as const }]
  return {
    id: input.id,
    name: input.name,
    mode: input.mode,
    status: input.status,
    isDefault: false,
    normalRoutingConfig: input.mode === 'normal'
      ? input.normalRoutingConfig ?? { schedulingPreference: 'cost_first' }
      : undefined,
    groupBindings: bindings.map((binding, index) => ({
      id: `mock_route_strategy_group_${index + 1}`,
      groupId: normalizedText(binding.groupId) || 'mock_group_public',
      groupName: `公开接口分组${index + 1}`,
      providerCode: 'mock_provider',
      priority: binding.priority ?? index + 1,
      weight: binding.weight ?? 100,
      status: binding.status ?? 'active',
      groupEnabled: true
    })),
    apiKeyCount: 0,
    createdAt: now,
    updatedAt: now
  }
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
