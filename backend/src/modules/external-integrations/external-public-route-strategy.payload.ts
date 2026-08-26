import type {
  PublicRouteStrategyAddInput,
  PublicRouteStrategyUpdateInput
} from './external-public-route-strategy.types.js'

export function publicRouteStrategyPayload(
  input: PublicRouteStrategyAddInput | PublicRouteStrategyUpdateInput,
  partial = false
): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if ('name' in input && input.name !== undefined) payload.name = input.name
  if ('description' in input && input.description !== undefined) payload.description = input.description
  if ('mode' in input && input.mode !== undefined) payload.mode = input.mode
  if ('status' in input && input.status !== undefined) payload.status = input.status
  if ('groupBindings' in input && input.groupBindings !== undefined) payload.groupBindings = input.groupBindings
  if ('normalRoutingConfig' in input && input.normalRoutingConfig !== undefined) {
    payload.normalRoutingConfig = input.normalRoutingConfig
  }
  if ('hybridRoutingConfig' in input && input.hybridRoutingConfig !== undefined) {
    payload.hybridRoutingConfig = input.hybridRoutingConfig
  }
  if (!partial && !Array.isArray(payload.groupBindings)) {
    throw new Error('路由策略至少需要绑定一个分组')
  }
  if (partial && Object.keys(payload).length === 0) {
    throw new Error('路由策略修改至少提供一个要修改的字段')
  }
  return payload
}
