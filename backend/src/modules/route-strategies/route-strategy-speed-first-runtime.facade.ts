import { runtimeConfig } from '../../config/runtime.js'
import type { CompleteRouteStrategyListItem, RouteStrategySummary } from '../../domain/types.js'
import {
  requestServerNormalRouteSpeedFirstLatencyRuntimeSnapshot
} from '../db-service/db-service-ipc.js'
import {
  listNormalRouteLatencyDegradedRuntimeAsync,
  type NormalRouteLatencyDegradedRuntimeItem
} from '../gateway/runtime/normal-route-latency-degradation.service.js'

export interface RouteStrategySpeedFirstLatencyRuntimeSummary {
  runtimeAvailable: boolean
  degradedCount: number
}

export interface RouteStrategySpeedFirstLatencyRuntimeSnapshot extends RouteStrategySpeedFirstLatencyRuntimeSummary {
  items: NormalRouteLatencyDegradedRuntimeItem[]
}

type RouteStrategyRuntimeListItem = Pick<
  CompleteRouteStrategyListItem,
  'id' | 'systemAccountId' | 'mode' | 'normalRoutingConfig'
>

const maxRouteStrategyIdsPerRuntimeQuery = 50
const runtimeReadTimeoutMs = 300

/**
 * 获取单个策略路由的速度优先运行态。
 *
 * standalone 的 DB service 没有 gateway memory，因此必须经窄 IPC 向 server
 * 读取；performance 的 Redis runtime state 则由当前进程直接读取。
 */
export async function loadRouteStrategySpeedFirstLatencyRuntimeAsync(input: {
  systemAccountId: string | undefined
  routeStrategyId: string
}): Promise<RouteStrategySpeedFirstLatencyRuntimeSnapshot> {
  const systemAccountId = input.systemAccountId?.trim()
  const routeStrategyId = input.routeStrategyId.trim()
  // systemAccountId 只是可选的 owner 二次收窄：个人侧 summary 与全局策略
  // （system_account_id 为空）不会携带 owner，此时按唯一策略 ID 查询即可。
  if (!routeStrategyId) return unavailableRuntimeSnapshot()
  return await loadSpeedFirstLatencyRuntimeForScopeAsync({
    ...(systemAccountId ? { systemAccountId } : {}),
    routeStrategyIds: [routeStrategyId]
  })
}

/**
 * 一次列表请求至多发起一次运行态批量查询，避免为每条策略路由读取一次 store。
 * DB service 已先用当前 access scope 筛出页面项，因此未筛选 owner 的管理列表也可
 * 将当前页 routeStrategyIds 一次传入；超过 IPC 上限或运行态读取失败时，对
 * speed_first 项统一标记 unavailable，列表本身仍可正常返回。
 */
export async function summarizeRouteStrategySpeedFirstLatencyRuntimeAsync(
  items: RouteStrategyRuntimeListItem[]
): Promise<Map<string, RouteStrategySpeedFirstLatencyRuntimeSummary>> {
  const speedFirstItems = items.filter(isNormalSpeedFirstRouteStrategy)
  const summaries = new Map<string, RouteStrategySpeedFirstLatencyRuntimeSummary>()
  if (speedFirstItems.length === 0) return summaries

  if (speedFirstItems.length > maxRouteStrategyIdsPerRuntimeQuery) {
    for (const item of speedFirstItems) summaries.set(item.id, unavailableRuntimeSummary())
    return summaries
  }

  const runtime = await loadSpeedFirstLatencyRuntimeForScopeAsync({
    routeStrategyIds: speedFirstItems.map((item) => item.id)
  })
  const degradedCounts = new Map<string, number>()
  for (const item of runtime.items) {
    degradedCounts.set(item.scope.routeStrategyId, (degradedCounts.get(item.scope.routeStrategyId) ?? 0) + 1)
  }
  for (const item of speedFirstItems) {
    summaries.set(item.id, {
      runtimeAvailable: runtime.runtimeAvailable,
      degradedCount: runtime.runtimeAvailable ? (degradedCounts.get(item.id) ?? 0) : 0
    })
  }
  return summaries
}

export function isNormalSpeedFirstRouteStrategy(
  routeStrategy: Pick<RouteStrategySummary, 'mode' | 'normalRoutingConfig'>
): boolean {
  return routeStrategy.mode === 'normal'
    && routeStrategy.normalRoutingConfig?.schedulingPreference === 'speed_first'
}

async function loadSpeedFirstLatencyRuntimeForScopeAsync(input: {
  systemAccountId?: string
  routeStrategyIds: string[]
}): Promise<RouteStrategySpeedFirstLatencyRuntimeSnapshot> {
  try {
    const items = runtimeConfig.runtimeStateDriver === 'memory' && runtimeConfig.processRole === 'db-service'
      ? await requestServerNormalRouteSpeedFirstLatencyRuntimeSnapshot(input, runtimeReadTimeoutMs)
      : await listNormalRouteLatencyDegradedRuntimeAsync(input)
    if (!items) return unavailableRuntimeSnapshot()
    const dedupedItems = dedupeRuntimeItemsByAccount(items)
    return {
      runtimeAvailable: true,
      degradedCount: dedupedItems.length,
      items: dedupedItems
    }
  } catch {
    return unavailableRuntimeSnapshot()
  }
}

/**
 * 管理端展示按账号收敛：普通路由策略只绑定一个分组，同一账号在策略内出现多条
 * 只可能来自过渡期残留（owner / 授权形态切换、换绑分组后清理失败），保留降级
 * 截止时间最新的一条，保证详情行和 degradedCount 都是账号粒度。
 */
function dedupeRuntimeItemsByAccount(
  items: NormalRouteLatencyDegradedRuntimeItem[]
): NormalRouteLatencyDegradedRuntimeItem[] {
  const latestByAccount = new Map<string, NormalRouteLatencyDegradedRuntimeItem>()
  for (const item of items) {
    const dedupeKey = `${item.scope.routeStrategyId}|${item.accountId}`
    const current = latestByAccount.get(dedupeKey)
    if (!current || current.degradedUntil.localeCompare(item.degradedUntil) < 0) {
      latestByAccount.set(dedupeKey, item)
    }
  }
  return [...latestByAccount.values()]
}

function unavailableRuntimeSummary(): RouteStrategySpeedFirstLatencyRuntimeSummary {
  return { runtimeAvailable: false, degradedCount: 0 }
}

function unavailableRuntimeSnapshot(): RouteStrategySpeedFirstLatencyRuntimeSnapshot {
  return { ...unavailableRuntimeSummary(), items: [] }
}
