import type { DatabaseSync } from 'node:sqlite'

import { nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { maxRouteStrategyAvailabilityLossCandidates } from './route-strategy-group-binding-limits.js'

export interface RouteStrategyGroupAvailabilityLossCandidate {
  id: string
  name: string
  systemAccountId: string
  targetBindingStatus?: string | null
}

export function assertRouteStrategiesCanLoseGroupAvailability(
  database: DatabaseSync,
  groupId: string,
  groupName: string | undefined,
  actionLabel: string,
  routeStrategySystemAccountId?: string
): void {
  const affectedRouteStrategies = loadRouteStrategiesAffectedByGroupAvailabilityLoss(database, groupId, routeStrategySystemAccountId)
  if (!affectedRouteStrategies.length) return
  if (affectedRouteStrategies.length > maxRouteStrategyAvailabilityLossCandidates) {
    throw new Error(`该分组关联的策略路由超过 ${maxRouteStrategyAvailabilityLossCandidates} 个，请先分批解除绑定后再${actionLabel}`)
  }
  assertAffectedRouteStrategiesCanLoseGroupAvailability(database, groupId, affectedRouteStrategies, actionLabel, groupName)
}

export function assertAffectedRouteStrategiesCanLoseGroupAvailability(
  database: DatabaseSync,
  groupId: string,
  affectedRouteStrategies: RouteStrategyGroupAvailabilityLossCandidate[],
  actionLabel: string,
  groupName?: string
): void {
  const activeBindingCountByRouteStrategyId = loadActiveRouteStrategyGroupCountExcludingGroup(
    database,
    groupId,
    affectedRouteStrategies.map((routeStrategy) => routeStrategy.id)
  )
  throwIfRouteStrategiesLoseOnlyActiveGroup(affectedRouteStrategies, activeBindingCountByRouteStrategyId, actionLabel, groupName)
}

export async function assertRouteStrategiesCanLoseGroupAvailabilityAsync(
  client: DatabaseClient,
  groupId: string,
  groupName: string | undefined,
  actionLabel: string,
  routeStrategySystemAccountId?: string
): Promise<void> {
  const affectedRouteStrategies = await loadRouteStrategiesAffectedByGroupAvailabilityLossAsync(client, groupId, routeStrategySystemAccountId)
  if (!affectedRouteStrategies.length) return
  if (affectedRouteStrategies.length > maxRouteStrategyAvailabilityLossCandidates) {
    throw new Error(`该分组关联的策略路由超过 ${maxRouteStrategyAvailabilityLossCandidates} 个，请先分批解除绑定后再${actionLabel}`)
  }
  await assertAffectedRouteStrategiesCanLoseGroupAvailabilityAsync(client, groupId, affectedRouteStrategies, actionLabel, groupName)
}

export async function assertAffectedRouteStrategiesCanLoseGroupAvailabilityAsync(
  client: DatabaseClient,
  groupId: string,
  affectedRouteStrategies: RouteStrategyGroupAvailabilityLossCandidate[],
  actionLabel: string,
  groupName?: string
): Promise<void> {
  const activeBindingCountByRouteStrategyId = await loadActiveRouteStrategyGroupCountExcludingGroupAsync(
    client,
    groupId,
    affectedRouteStrategies.map((routeStrategy) => routeStrategy.id)
  )
  throwIfRouteStrategiesLoseOnlyActiveGroup(affectedRouteStrategies, activeBindingCountByRouteStrategyId, actionLabel, groupName)
}

function loadRouteStrategiesAffectedByGroupAvailabilityLoss(
  database: DatabaseSync,
  groupId: string,
  routeStrategySystemAccountId?: string
): RouteStrategyGroupAvailabilityLossCandidate[] {
  const systemAccountClause = routeStrategySystemAccountId ? 'AND route_strategy_groups.system_account_id = ?' : ''
  const params = routeStrategySystemAccountId
    ? [groupId, routeStrategySystemAccountId, maxRouteStrategyAvailabilityLossCandidates + 1]
    : [groupId, maxRouteStrategyAvailabilityLossCandidates + 1]
  return database
    .prepare(`
      SELECT
        route_strategy_groups.route_strategy_id AS id,
        route_strategies.name,
        route_strategies.system_account_id AS systemAccountId,
        route_strategy_groups.status AS targetBindingStatus
      FROM route_strategy_groups
      INNER JOIN route_strategies
        ON route_strategies.id = route_strategy_groups.route_strategy_id
        AND route_strategies.system_account_id = route_strategy_groups.system_account_id
      WHERE route_strategy_groups.group_id = ?
        ${systemAccountClause}
      ORDER BY route_strategy_groups.route_strategy_id ASC
      LIMIT ?
    `)
    .all(...params) as unknown as RouteStrategyGroupAvailabilityLossCandidate[]
}

async function loadRouteStrategiesAffectedByGroupAvailabilityLossAsync(
  client: DatabaseClient,
  groupId: string,
  routeStrategySystemAccountId?: string
): Promise<RouteStrategyGroupAvailabilityLossCandidate[]> {
  const systemAccountClause = routeStrategySystemAccountId ? 'AND route_strategy_groups.system_account_id = ?' : ''
  const params = routeStrategySystemAccountId
    ? [groupId, routeStrategySystemAccountId, maxRouteStrategyAvailabilityLossCandidates + 1]
    : [groupId, maxRouteStrategyAvailabilityLossCandidates + 1]
  return await client.query<RouteStrategyGroupAvailabilityLossCandidate>(`
    SELECT
      route_strategy_groups.route_strategy_id AS id,
      route_strategies.name,
      route_strategies.system_account_id AS "systemAccountId",
      route_strategy_groups.status AS "targetBindingStatus"
    FROM ${routeStrategyAvailabilityTable(client, 'route_strategy_groups')} route_strategy_groups
    INNER JOIN ${routeStrategyAvailabilityTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = route_strategy_groups.route_strategy_id
      AND route_strategies.system_account_id = route_strategy_groups.system_account_id
    WHERE route_strategy_groups.group_id = ?
      ${systemAccountClause}
    ORDER BY route_strategy_groups.route_strategy_id ASC
    LIMIT ?
  `, params)
}

function throwIfRouteStrategiesLoseOnlyActiveGroup(
  affectedRouteStrategies: RouteStrategyGroupAvailabilityLossCandidate[],
  activeBindingCountByRouteStrategyId: Map<string, number>,
  actionLabel: string,
  groupName?: string
): void {
  const blockers = affectedRouteStrategies.filter((routeStrategy) => {
    if (routeStrategy.targetBindingStatus !== 'active') return false
    return (activeBindingCountByRouteStrategyId.get(routeStrategy.id) ?? 0) === 0
  })
  if (!blockers.length) return
  const names = blockers.slice(0, 3).map((routeStrategy) => routeStrategy.name).join('、')
  const suffix = blockers.length > 3 ? ` 等 ${blockers.length} 个` : ''
  const subject = groupName ? `“${groupName}”` : '该分组'
  throw new Error(`无法${actionLabel}${subject}：该分组仍是以下策略路由的唯一可用启用分组：${names}${suffix}。请先到策略路由中切换或新增启用分组，或删除这些策略路由后再操作。`)
}

function loadActiveRouteStrategyGroupCountExcludingGroup(
  database: DatabaseSync,
  groupId: string,
  routeStrategyIds: string[]
): Map<string, number> {
  const result = new Map<string, number>()
  const uniqueIds = [...new Set(routeStrategyIds.filter(Boolean))]
  const now = nowIso()
  for (const chunk of chunkValues(uniqueIds, 500)) {
    const rows = database
      .prepare(`
        SELECT
          route_strategy_groups.route_strategy_id AS routeStrategyId,
          COUNT(*) AS activeBindingCount
        FROM route_strategy_groups
        INNER JOIN groups
          ON groups.id = route_strategy_groups.group_id
          AND groups.enabled = 1
        LEFT JOIN resource_authorizations group_authorization
          ON group_authorization.resource_type = 'group'
          AND group_authorization.resource_id = groups.id
          AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
          AND group_authorization.status = 'active'
          AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
        LEFT JOIN group_authorization_settings
          ON group_authorization_settings.authorization_id = group_authorization.id
          AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
          AND group_authorization_settings.group_id = groups.id
        WHERE route_strategy_groups.status = 'active'
          AND (
            groups.system_account_id = route_strategy_groups.system_account_id
            OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
          )
          AND route_strategy_groups.group_id <> ?
          AND route_strategy_groups.route_strategy_id IN (${sqlPlaceholders(chunk.length)})
        GROUP BY route_strategy_groups.route_strategy_id
    `)
      .all(now, groupId, ...chunk) as unknown as Array<{ routeStrategyId: string; activeBindingCount: number }>
    for (const row of rows) {
      result.set(row.routeStrategyId, Number(row.activeBindingCount) || 0)
    }
  }
  return result
}

async function loadActiveRouteStrategyGroupCountExcludingGroupAsync(
  client: DatabaseClient,
  groupId: string,
  routeStrategyIds: string[]
): Promise<Map<string, number>> {
  const result = new Map<string, number>()
  const uniqueIds = [...new Set(routeStrategyIds.filter(Boolean))]
  const now = nowIso()
  for (const chunk of chunkValues(uniqueIds, 500)) {
    const rows = await client.query<{ routeStrategyId: string; activeBindingCount: number | string }>(`
      SELECT
        route_strategy_groups.route_strategy_id AS "routeStrategyId",
        COUNT(*) AS "activeBindingCount"
      FROM ${routeStrategyAvailabilityTable(client, 'route_strategy_groups')} route_strategy_groups
      INNER JOIN ${routeStrategyAvailabilityTable(client, 'groups')} groups
        ON groups.id = route_strategy_groups.group_id
        AND groups.enabled = 1
      LEFT JOIN ${routeStrategyAvailabilityTable(client, 'resource_authorizations')} group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = groups.id
        AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      LEFT JOIN ${routeStrategyAvailabilityTable(client, 'group_authorization_settings')} group_authorization_settings
        ON group_authorization_settings.authorization_id = group_authorization.id
        AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
        AND group_authorization_settings.group_id = groups.id
      WHERE route_strategy_groups.status = 'active'
        AND (
          groups.system_account_id = route_strategy_groups.system_account_id
          OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
        )
        AND route_strategy_groups.group_id <> ?
        AND route_strategy_groups.route_strategy_id IN (${client.dialect.bindPlaceholders(chunk.length)})
      GROUP BY route_strategy_groups.route_strategy_id
    `, [now, groupId, ...chunk])
    for (const row of rows) {
      result.set(row.routeStrategyId, Number(row.activeBindingCount) || 0)
    }
  }
  return result
}

function routeStrategyAvailabilityTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
