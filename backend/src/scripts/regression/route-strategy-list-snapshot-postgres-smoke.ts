import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createApiKeyRecordAsync,
  createGroupAsync,
  createRouteStrategyAsync,
  createSystemAccountAsync,
  listRouteStrategyListItemsPageAsync,
  listRouteStrategyListSnapshotAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '策略路由 list snapshot PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `route_strategy_snapshot_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'admin' }
const createdApiKeyIds: string[] = []
const createdRouteStrategyIds: string[] = []
const createdGroupIds: string[] = []
const createdSystemAccountIds: string[] = []

try {
  await cleanupSmokeRows()
  const groups = []
  for (let index = 0; index < 4; index += 1) {
    const group = await createGroupAsync({
      name: `策略路由 snapshot PG 分组 ${index + 1} ${marker}`,
      providerCode: 'gpt',
      enabled: true
    }, access)
    groups.push(group)
    createdGroupIds.push(group.id)
  }

  const previewStrategy = await createRouteStrategyAsync({
    name: `策略路由 snapshot PG 排序目标 ${marker}`,
    mode: 'round_robin',
    status: 'active',
    groupBindings: groups.map((group, index) => ({
      groupId: group.id,
      priority: index + 1,
      weight: 1,
      status: index === 3 ? 'disabled' : 'active'
    }))
  }, access)
  createdRouteStrategyIds.push(previewStrategy.id)

  const zeroStrategy = await createRouteStrategyAsync({
    name: `策略路由 snapshot PG 零值目标 ${marker}`,
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: groups[0].id, priority: 1, weight: 1, status: 'active' }]
  }, access)
  createdRouteStrategyIds.push(zeroStrategy.id)

  const firstKey = await createApiKeyRecordAsync({
    name: `策略路由 snapshot PG Key 1 ${marker}`,
    routeStrategyId: previewStrategy.id,
    status: 'active'
  }, access)
  const secondKey = await createApiKeyRecordAsync({
    name: `策略路由 snapshot PG Key 2 ${marker}`,
    routeStrategyId: previewStrategy.id,
    status: 'disabled'
  }, access)
  createdApiKeyIds.push(firstKey.id, secondKey.id)

  const pool = await getPostgresPool()
  await pool.query('UPDATE juhe_business.groups SET enabled = 0 WHERE id = $1', [groups[2].id])
  await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id = $1', [zeroStrategy.id])
  const bindingRows = await pool.query(`
    SELECT id, group_id
    FROM juhe_business.route_strategy_groups
    WHERE route_strategy_id = $1
  `, [previewStrategy.id])
  const order = new Map([
    [groups[0].id, { priority: 3, status: 'active', createdAt: '2026-07-23T00:03:00.000Z' }],
    [groups[1].id, { priority: 1, status: 'active', createdAt: '2026-07-23T00:02:00.000Z' }],
    [groups[2].id, { priority: 1, status: 'active', createdAt: '2026-07-23T00:01:00.000Z' }],
    [groups[3].id, { priority: 0, status: 'disabled', createdAt: '2026-07-23T00:00:00.000Z' }]
  ])
  for (const row of bindingRows.rows as Array<{ id: string; group_id: string }>) {
    const value = order.get(row.group_id)
    assert(value)
    await pool.query(`
      UPDATE juhe_business.route_strategy_groups
      SET priority = $1, status = $2, created_at = $3, updated_at = $3
      WHERE id = $4
    `, [value.priority, value.status, value.createdAt, row.id])
  }

  const list = await listRouteStrategyListItemsPageAsync(access, {
    keyword: `策略路由 snapshot PG`,
    page: 1,
    pageSize: 20
  })
  const base = list.items.find((item) => item.id === previewStrategy.id) as unknown as Record<string, unknown> | undefined
  assert(base, 'PG 基础列表必须返回目标策略路由')
  assert.equal('bindingCount' in base, false, 'PG 基础列表不得返回 bindingCount')
  assert.equal('apiKeyCount' in base, false, 'PG 基础列表不得返回 apiKeyCount')
  assert.equal('groupBindingPreview' in base, false, 'PG 基础列表不得返回 groupBindingPreview')

  const snapshot = await listRouteStrategyListSnapshotAsync(access, [
    zeroStrategy.id,
    previewStrategy.id,
    'route_strategy_snapshot_pg_missing',
    previewStrategy.id
  ])
  assert.deepEqual(snapshot.items.map((item) => item.id), [zeroStrategy.id, previewStrategy.id], 'PG snapshot 必须省略不存在 ID、去重并保持请求顺序')
  assert.deepEqual(snapshot.items[0], {
    id: zeroStrategy.id,
    bindingCount: 0,
    apiKeyCount: 0,
    groupBindingPreview: []
  }, 'PG snapshot 必须返回真实零值')
  const preview = snapshot.items[1]
  assert.equal(preview?.bindingCount, 4)
  assert.equal(preview?.apiKeyCount, 2)
  assert.deepEqual(preview?.groupBindingPreview.map((item) => item.groupId), [groups[2].id, groups[1].id, groups[0].id])
  assert.equal(preview?.groupBindingPreview[0]?.groupEnabled, false)

  const owner = await createSystemAccountAsync({
    username: `route_snapshot_owner_${marker.slice(-20)}`,
    displayName: `策略路由 snapshot owner ${marker}`,
    password: `Pwd${marker}Aa1!`,
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  createdSystemAccountIds.push(owner.id)
  const ownerAccess: AccessScope = { systemAccountId: owner.id, role: 'user' }
  const ownerGroup = await createGroupAsync({
    name: `策略路由 snapshot owner 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  createdGroupIds.push(ownerGroup.id)
  const ownerStrategy = await createRouteStrategyAsync({
    name: `策略路由 snapshot owner 策略 ${marker}`,
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: ownerGroup.id, priority: 1, weight: 1, status: 'active' }]
  }, ownerAccess)
  createdRouteStrategyIds.push(ownerStrategy.id)

  const ownerFiltered = await listRouteStrategyListSnapshotAsync({
    ...access,
    systemAccountFilterId: owner.id
  }, [previewStrategy.id, ownerStrategy.id])
  assert.deepEqual(ownerFiltered.items.map((item) => item.id), [ownerStrategy.id], 'PG 管理端指定 owner 必须在 SQL 中省略其他 owner 策略')
  const selfScoped = await listRouteStrategyListSnapshotAsync(ownerAccess, [previewStrategy.id, ownerStrategy.id])
  assert.deepEqual(selfScoped.items.map((item) => item.id), [ownerStrategy.id], 'PG 个人作用域必须静默省略其他 owner 策略')

  console.log(JSON.stringify({
    message: '策略路由 list snapshot PG smoke 通过',
    previewStrategyId: previewStrategy.id,
    zeroStrategyId: zeroStrategy.id,
    bindingCount: preview?.bindingCount,
    apiKeyCount: preview?.apiKeyCount
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const staleSystemAccounts = await pool.query(`
    SELECT id
    FROM juhe_business.system_accounts
    WHERE position($1 in username) > 0 OR position($1 in display_name) > 0
  `, [marker])
  const systemAccountIds = [...new Set([
    ...createdSystemAccountIds,
    ...(staleSystemAccounts.rows as Array<{ id: string }>).map((row) => row.id)
  ])]
  const apiKeyIds = [...new Set(createdApiKeyIds)]
  if (apiKeyIds.length) {
    await pool.query('DELETE FROM juhe_business.api_keys WHERE id = ANY($1::text[])', [apiKeyIds])
  }
  if (systemAccountIds.length) {
    await pool.query('DELETE FROM juhe_business.api_keys WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  }
  const routeStrategyIds = [...new Set(createdRouteStrategyIds)]
  if (routeStrategyIds.length) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id = ANY($1::text[])', [routeStrategyIds])
    await pool.query('DELETE FROM juhe_business.route_strategies WHERE id = ANY($1::text[])', [routeStrategyIds])
  }
  if (systemAccountIds.length) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
    await pool.query('DELETE FROM juhe_business.route_strategies WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  }
  const groupIds = [...new Set(createdGroupIds)]
  if (groupIds.length) {
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
  if (systemAccountIds.length) {
    await pool.query(`
      DELETE FROM juhe_business.group_account_stats_dirty
      WHERE group_id IN (
        SELECT id FROM juhe_business.groups WHERE system_account_id = ANY($1::text[])
      )
    `, [systemAccountIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  }
  await pool.query('DELETE FROM juhe_business.api_keys WHERE position($1 in name) > 0', [marker])
  await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id IN (SELECT id FROM juhe_business.route_strategies WHERE position($1 in name) > 0)', [marker])
  await pool.query('DELETE FROM juhe_business.route_strategies WHERE position($1 in name) > 0', [marker])
  await pool.query('DELETE FROM juhe_business.groups WHERE position($1 in name) > 0', [marker])
  if (systemAccountIds.length) {
    await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])', [systemAccountIds])
  }
}
