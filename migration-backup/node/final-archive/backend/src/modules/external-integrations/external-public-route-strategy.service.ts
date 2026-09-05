import {
  createRouteStrategyAsync,
  deleteRouteStrategyAsync,
  findRouteStrategyMutationVersionAsync,
  findRouteStrategySummaryAsync,
  listRouteStrategiesPageAsync,
  updateRouteStrategyAsync
} from '../../storage/repositories.js'
import { publicRouteStrategyPayload } from './external-public-route-strategy.payload.js'
import {
  publicRouteStrategyListResponse,
  publicRouteStrategyNotFoundResponse,
  publicRouteStrategyResponse,
  sanitizeRouteStrategy
} from './external-public-route-strategy.sanitize.js'
import {
  assertTargetActive,
  normalizedText,
  requirePublicTargetAsync,
  resolvePublicOwnedResourceTargetAsync,
  targetAccess
} from './external-public-account-push.target.js'
import type {
  PublicRouteStrategyAddInput,
  PublicRouteStrategyDeleteInput,
  PublicRouteStrategyListInput,
  PublicRouteStrategyListResponse,
  PublicRouteStrategyResponse,
  PublicRouteStrategyUpdateInput
} from './external-public-route-strategy.types.js'

const publicRouteStrategyLookupAccess = {
  systemAccountId: '__public_route_strategy_lookup__',
  role: 'super_admin' as const
}

export async function listPublicRouteStrategiesAsync(input: PublicRouteStrategyListInput): Promise<PublicRouteStrategyListResponse> {
  const target = await requirePublicTargetAsync(input.targetUsername)
  assertTargetActive(target.account)
  const page = await listRouteStrategiesPageAsync(targetAccess(target.account.id), {
    page: input.page,
    pageSize: input.pageSize,
    keyword: normalizedText(input.keyword),
    mode: input.mode,
    status: input.status
  })
  return publicRouteStrategyListResponse(target, {
    page: page.page,
    pageSize: page.pageSize,
    pageUpperBound: page.total,
    hasMore: page.hasMore,
    items: page.items.map((routeStrategy) => sanitizeRouteStrategy(routeStrategy))
  })
}

export async function addPublicRouteStrategyAsync(input: PublicRouteStrategyAddInput): Promise<PublicRouteStrategyResponse> {
  const target = await requirePublicTargetAsync(input.targetUsername)
  assertTargetActive(target.account)
  const routeStrategy = await createRouteStrategyAsync(publicRouteStrategyPayload(input), targetAccess(target.account.id))
  return publicRouteStrategyResponse('created', target, sanitizeRouteStrategy(routeStrategy))
}

export async function updatePublicRouteStrategyAsync(input: PublicRouteStrategyUpdateInput): Promise<PublicRouteStrategyResponse> {
  const routeStrategyId = normalizedRouteStrategyId(input.routeStrategyId, '路由策略修改必须提供 routeStrategyId')
  const owner = await findPublicRouteStrategyOwnerByIdAsync(routeStrategyId)
  if (!owner) {
    return publicRouteStrategyNotFoundResponse(input.targetUsername)
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicRouteStrategyNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const updated = await updateRouteStrategyAsync(routeStrategyId, {
    ...publicRouteStrategyPayload(input, true),
    expectedUpdatedAt: owner.updatedAt
  }, access)
  return publicRouteStrategyResponse(updated ? 'updated' : 'not_found', target, updated ? sanitizeRouteStrategy(updated) : null)
}

export async function deletePublicRouteStrategyAsync(input: PublicRouteStrategyDeleteInput): Promise<PublicRouteStrategyResponse> {
  const routeStrategyId = normalizedRouteStrategyId(input.routeStrategyId, '路由策略删除必须提供 routeStrategyId')
  const owner = await findPublicRouteStrategyOwnerByIdAsync(routeStrategyId)
  if (!owner) {
    return publicRouteStrategyNotFoundResponse(input.targetUsername)
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicRouteStrategyNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const existing = await findRouteStrategySummaryAsync(routeStrategyId, access)
  if (!existing) {
    return publicRouteStrategyResponse('not_found', target, null)
  }
  const deletedRouteStrategy = sanitizeRouteStrategy(existing)
  const deleted = await deleteRouteStrategyAsync(routeStrategyId, access)
  return publicRouteStrategyResponse(deleted ? 'deleted' : 'not_found', target, deleted ? deletedRouteStrategy : null)
}

async function findPublicRouteStrategyOwnerByIdAsync(routeStrategyId: string): Promise<{ id: string; systemAccountId: string; updatedAt: string } | undefined> {
  const routeStrategy = await findRouteStrategyMutationVersionAsync(routeStrategyId, publicRouteStrategyLookupAccess)
  return routeStrategy?.systemAccountId
    ? { id: routeStrategy.id, systemAccountId: routeStrategy.systemAccountId, updatedAt: routeStrategy.updatedAt }
    : undefined
}

function normalizedRouteStrategyId(value: unknown, message: string): string {
  const routeStrategyId = normalizedText(value)
  if (!routeStrategyId) {
    throw new Error(message)
  }
  return routeStrategyId
}
