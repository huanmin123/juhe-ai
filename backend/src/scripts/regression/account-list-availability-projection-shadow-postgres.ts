import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import http from 'node:http'

import { runAccountListAvailabilityProjectionMaintenance } from '../../modules/accounts/account-list-availability-projection.service.js'
import { shadowCompareAccountListAvailabilityProjectionPage } from '../../modules/accounts/account-list-availability-projection-shadow.service.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import {
  markAccountListAvailabilityProjectionRuntimeDependencyUnavailableInClient,
  claimAccountListAvailabilityDirtyInClient,
  listAccountListAvailabilityProjectionPageInClient,
  releaseAccountListAvailabilityDirtyForReplayInClient
} from '../../storage/account-list-availability-projection.repository.js'
import { accountNameSearchQueryTerms } from '../../storage/account-name-search.repository.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createSessionAsync, listAccountOptionsAsync } from '../../storage/repositories.js'
import { runtimeConfig } from '../../config/runtime.js'
import { tryAcquireAccountConcurrencyAsync } from '../../shared/account-concurrency.js'
import type { AccountListSort } from '../../storage/account-list-options.js'
import { todayDateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

const viewerId = 'account-list-projection-shadow-viewer-20260810'
const accountPrefix = 'account-list-projection-shadow-account-20260810-'
const groupId = 'account-list-projection-shadow-group-20260810'
const tagId = 'account-list-projection-shadow-tag-20260810'
const accountCount = 32
const scheduledSourceAccountId = 'account-list-projection-shadow-scheduled-source-20260810'
const scheduledAuthorizationId = 'account-list-projection-shadow-scheduled-authorization-20260810'
const scheduledAuthorizedAccountId = 'account-list-projection-shadow-scheduled-authorized-20260810'
const teamQuotaId = 'account-list-projection-shadow-team-quota-20260810'
const teamQuotaMemberId = 'account-list-projection-shadow-team-member-20260810'
const teamQuotaAuthorizationId = 'account-list-projection-shadow-team-authorization-20260810'
const teamQuotaGrantId = 'account-list-projection-shadow-team-grant-20260810'
const teamQuotaAuthorizedAccountId = 'account-list-projection-shadow-team-authorized-20260810'

async function main(): Promise<void> {
  assertScratchDatabase()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const previousProcessRole = runtimeConfig.processRole
  // The shadow oracle needs the same durable circuit summary that a healthy
  // DB-service process returns. This stays inside the isolated test process;
  // it neither starts a service nor changes any deployment configuration.
  runtimeConfig.processRole = 'db-service'
  await cleanup(client)
  try {
    const profile = await seed(client)
    const initialDirty = await client.one<{ count: string }>(`
      SELECT count(*)::text AS count
      FROM ${table(client, 'account_list_availability_dirty')}
      WHERE account_id LIKE ?
    `, [`${accountPrefix}%`])
    assert.equal(Number(initialDirty?.count ?? 0), accountCount, '前缀夹具账户创建和绑定写入必须在事务内留下每账户一个脏标记')
    const preflightClaims = await claimAccountListAvailabilityDirtyInClient(client, {
      ownerId: 'account-list-projection-shadow-preflight',
      limit: 100,
      leaseMs: 30_000
    })
    assert.equal(preflightClaims.length, accountCount + 2, 'PostgreSQL 脏队列必须可由 worker 认领')
    for (const claim of preflightClaims) {
      assert(await releaseAccountListAvailabilityDirtyForReplayInClient(client, {
        accountId: claim.accountId,
        generation: claim.generation,
        claimToken: claim.claimToken,
        reason: 'shadow_preflight_replay',
        retryDelayMs: 0
      }), '预检认领必须可释放给正式 materializer')
    }
    const maintenance = await runAccountListAvailabilityProjectionMaintenance({
      ownerId: 'account-list-projection-shadow-regression',
      batchSize: 100
    })
    assert.equal(maintenance.claimed, accountCount + 2, '影子对账前必须完整认领初始投影脏标记')
    assert.equal(maintenance.projected, accountCount + 2, '影子对账前必须完整物化账户投影')
    assert.equal(maintenance.released, 0, '初始影子投影不应需要重放')
    await verifyAuthorizedSourceScheduleTransition(client)
    await verifyLastUsedAtProjectionUpdate(client)
    const access = { systemAccountId: viewerId, role: 'user' as const }
    await verifyLivePageDynamicOverlays(client, access)
    const cases = [
      { name: 'default', options: { page: 1, pageSize: 20 } },
      { name: 'status-active', options: { page: 1, pageSize: 20, status: 'active' } },
      { name: 'schedulable-enabled', options: { page: 1, pageSize: 20, schedulable: 'enabled' as const } },
      { name: 'group-status', options: { page: 1, pageSize: 20, groupId, status: 'active' } },
      { name: 'tag-status', options: { page: 1, pageSize: 20, tagIds: [tagId], status: 'active' } },
      { name: 'keyword', options: { page: 1, pageSize: 20, keyword: 'shadow account' } },
      { name: 'provider', options: { page: 1, pageSize: 20, providerCode: profile.providerCode } }
    ]
    for (const testCase of cases) {
      const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
        access,
        options: testCase.options
      })
      assert.equal(comparison.outcome, 'equal', `影子对账 ${testCase.name} 必须与旧链路完全一致：${comparison.reason ?? ''}`)
    }

    await verifyTeamQuotaThresholdProjection(client)
    await verifyOptionsProjectionParity(access)
    await verifySortParity(client, access)
    await verifyRuntimeDependencyFailureRecovery(client, access)

    await seedIndexedKeywordDocuments(client)
    const indexedDirty = await client.one<{ count: string }>(`
      SELECT count(*)::text AS count
      FROM ${table(client, 'account_list_availability_dirty')}
      WHERE account_id LIKE ?
    `, [`${accountPrefix}%`])
    assert.equal(Number(indexedDirty?.count ?? 0), accountCount, '名称检索文档完成后必须重建每个账户投影')
    const indexedMaintenance = await runAccountListAvailabilityProjectionMaintenance({
      ownerId: 'account-list-projection-shadow-indexed-keyword',
      batchSize: 100
    })
    assert(indexedMaintenance.claimed >= accountCount, '检索文档变更必须完整领取全部前缀账户投影')
    assert(indexedMaintenance.projected >= accountCount, '检索文档变更必须完整重投影全部前缀账户')
    const expectedKeywordTermCount = accountNameSearchQueryTerms('shadow account').length
    const indexedProjectionTerms = await client.one<{ count: string }>(`
      SELECT count(*)::text AS count
      FROM ${table(client, 'account_list_availability_projection_search_terms')}
      WHERE account_id LIKE ?
    `, [`${accountPrefix}%`])
    assert.equal(
      Number(indexedProjectionTerms?.count ?? 0),
      accountCount * expectedKeywordTermCount,
      '已完成检索文档的全部检索词必须写入账户投影'
    )
    const indexedTermValues = await client.query<{ term: string }>(`
      SELECT DISTINCT term
      FROM ${table(client, 'account_list_availability_projection_search_terms')}
      WHERE account_id LIKE ?
      ORDER BY term ASC
    `, [`${accountPrefix}%`])
    assert.deepEqual(
      indexedTermValues.map((row) => row.term),
      [...accountNameSearchQueryTerms('shadow account')].sort(),
      '投影检索词值必须与来源检索文档一致'
    )
    const indexedKeyword = await shadowCompareAccountListAvailabilityProjectionPage(client, {
      access,
      options: { page: 1, pageSize: 20, keyword: 'shadow account' }
    })
    assert.equal(indexedKeyword.outcome, 'equal', `已完成关键词索引必须与旧链路一致：${indexedKeyword.reason ?? ''}`)
    assert.equal(indexedKeyword.legacyItemIds.length, 20, '已完成关键词索引应返回第一页完整结果')
    await verifyHttpProjectionReadPath(client)
    await verifyProjectionDeletionRecovery(client, access)
    await verifyConcurrencyOverlayProjection(client)
    console.log('account list availability projection PostgreSQL shadow regression passed')
  } finally {
    runtimeConfig.processRole = previousProcessRole
    await cleanup(client)
    await closePostgresPool()
  }
}

async function verifyOptionsProjectionParity(
  access: { systemAccountId: string; role: 'user' }
): Promise<void> {
  const source = readFileSync(new URL('../../storage/account-options.repository.ts', import.meta.url), 'utf8')
  const projectionBranch = source.indexOf("if (resolvedAccess?.role === 'user'")
  const quotaBranch = source.indexOf('if (accountOptionQuotaFilterRequested(listOptions))')
  assert(projectionBranch >= 0 && quotaBranch >= 0 && projectionBranch < quotaBranch, 'options projection 分支必须位于 legacy quota candidate branch 之前')
  const projectionBranchSource = source.slice(projectionBranch, quotaBranch)
  assert.match(projectionBranchSource, /listAccountListAvailabilityProjectionPageInClient/, 'options projection 分支必须直接读取 projection')
  assert.match(projectionBranchSource, /includeDynamicOverlays:\s*false/, 'options projection 分支必须关闭 dynamic overlays')
  assert.doesNotMatch(projectionBranchSource, /listAccountOptionsWithQuotaFilterAsync/, 'options projection 分支不得调用 legacy quota candidate path')

  const cases = [
    { name: 'default', options: { page: 1, limit: 20 } },
    { name: 'page-2', options: { page: 2, limit: 2 } },
    { name: 'status-active', options: { page: 1, limit: 20, status: 'active' } },
    { name: 'status-rate-limited', options: { page: 1, limit: 20, status: 'rate_limited' } },
    { name: 'schedulable-enabled', options: { page: 1, limit: 20, schedulable: 'enabled' as const } },
    { name: 'schedulable-disabled', options: { page: 1, limit: 20, schedulable: 'disabled' as const } },
    { name: 'schedulable-cooling', options: { page: 1, limit: 20, schedulable: 'cooling' as const } },
    { name: 'ids', options: { page: 1, limit: 20, ids: [teamQuotaAuthorizedAccountId] } },
    { name: 'keyword', options: { page: 1, limit: 20, keyword: 'Projection shadow team quota authorized' } },
    { name: 'group', options: { page: 1, limit: 20, groupId } },
    { name: 'tag', options: { page: 1, limit: 20, tagIds: [tagId] } }
  ]
  const previousReadEnabled = runtimeConfig.background.accountListAvailabilityProjectionReadEnabled
  try {
    for (const testCase of cases) {
      runtimeConfig.background.accountListAvailabilityProjectionReadEnabled = false
      const legacy = await listAccountOptionsAsync(access, testCase.options)
      runtimeConfig.background.accountListAvailabilityProjectionReadEnabled = true
      const projected = await listAccountOptionsAsync(access, testCase.options)
      const normalize = (items: Array<{ id: string; status: string; accessType?: string }>) => items.map((item) => ({
        id: item.id,
        status: item.status,
        accessType: item.accessType ?? ''
      }))
      assert.deepEqual(
        normalize(projected),
        normalize(legacy),
        `options projection ${testCase.name} 必须与 legacy user options 的 ID/status/accessType 顺序一致`
      )
    }
  } finally {
    runtimeConfig.background.accountListAvailabilityProjectionReadEnabled = previousReadEnabled
  }
  assert(cases.some((testCase) => testCase.name === 'status-rate-limited'), 'options 回归必须覆盖 rate_limited')
}

async function verifyHttpProjectionReadPath(client: DatabaseClient): Promise<void> {
  const previousReadEnabled = runtimeConfig.background.accountListAvailabilityProjectionReadEnabled
  let server: http.Server | undefined
  try {
    runtimeConfig.background.accountListAvailabilityProjectionReadEnabled = true
    const session = await createSessionAsync(viewerId, 1)
    const app = createSystemApiApp({
      systemApiPrefix: '/__aisys__/api',
      trustProxy: true,
      bypassSystemApiRateLimitForTest: true
    })
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const address = server.address()
    assert(address && typeof address !== 'string', 'HTTP 投影回归服务必须分配 TCP 端口')
    const baseUrl = `http://127.0.0.1:${address.port}`
    const cookie = `juhe_ai_session=${session.token}`

    const healthy = await fetch(`${baseUrl}/__aisys__/api/my-accounts?status=active&page=1&pageSize=20`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const healthyBody = await healthy.text()
    assert.equal(healthy.status, 200, `投影 HTTP 列表必须返回 200，实际 ${healthy.status}: ${healthyBody}`)
    assert.match(healthy.headers.get('server-timing') ?? '', /account-list-projection;dur=[0-9.]+/, '投影 HTTP 列表必须暴露投影耗时指标')
    assert.match(healthy.headers.get('server-timing') ?? '', /account-status-filter;dur=0\.0/, '投影 HTTP 列表不得进入旧运行态候选筛选')

    const optionsPage1Response = await fetch(`${baseUrl}/__aisys__/api/my-accounts/options?page=1&limit=1`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const optionsPage1Body = await optionsPage1Response.text()
    assert.equal(optionsPage1Response.status, 200, `账户 options 投影健康时必须返回 200，实际 ${optionsPage1Response.status}: ${optionsPage1Body}`)
    const optionsPage1 = (JSON.parse(optionsPage1Body) as { data: Array<Record<string, unknown>> }).data
    assert.equal(optionsPage1.length, 1, '账户 options 投影第一页必须遵守 limit')
    for (const forbiddenField of ['credentials', 'currentConcurrency', 'todayUsage', 'balanceSnapshot', 'runtimeAvailability', 'effectiveAvailability']) {
      assert.equal(Object.prototype.hasOwnProperty.call(optionsPage1[0], forbiddenField), false, `账户 options 不得暴露 ${forbiddenField}`)
    }

    const optionsPage2Response = await fetch(`${baseUrl}/__aisys__/api/my-accounts/options?page=2&limit=1`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const optionsPage2Body = await optionsPage2Response.text()
    assert.equal(optionsPage2Response.status, 200, `账户 options 投影第二页必须返回 200，实际 ${optionsPage2Response.status}: ${optionsPage2Body}`)
    const optionsPage2 = (JSON.parse(optionsPage2Body) as { data: Array<Record<string, unknown>> }).data
    assert.equal(optionsPage2.length, 1, '账户 options 投影第二页必须遵守 limit')
    assert.notEqual(optionsPage2[0]?.id, optionsPage1[0]?.id, '账户 options 投影 page=2 不得重复第一页账户')

    await client.execute(`
      UPDATE ${table(client, 'accounts')}
      SET status = 'disabled'
      WHERE id = ?
    `, [`${accountPrefix}0001`])
    const blocked = await fetch(`${baseUrl}/__aisys__/api/my-accounts?status=active&page=1&pageSize=20`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const blockedBody = await blocked.text()
    assert.equal(blocked.status, 503, `投影脏标记必须 fail-closed，实际 ${blocked.status}: ${blockedBody}`)
    assert.equal(blocked.headers.get('retry-after'), '1', '投影 unavailable 响应必须明确短重试时间')
    assert.match(blockedBody, /account_list_projection_unavailable/, '投影 unavailable 响应不得静默回退旧扫描')

    const optionsBlocked = await fetch(`${baseUrl}/__aisys__/api/my-accounts/options?page=1&limit=1`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const optionsBlockedBody = await optionsBlocked.text()
    assert.equal(optionsBlocked.status, 503, `账户 options 投影 unavailable 必须返回 503，实际 ${optionsBlocked.status}: ${optionsBlockedBody}`)
    assert.equal(optionsBlocked.headers.get('retry-after'), '1', '账户 options projection unavailable 必须返回 Retry-After: 1')
    assert.match(optionsBlockedBody, /account_list_projection_unavailable/, '账户 options unavailable 不得回退旧候选扫描')

    const rebuilt = await runAccountListAvailabilityProjectionMaintenance({
      ownerId: 'account-list-projection-shadow-http-rebuild',
      batchSize: 100
    })
    assert.equal(rebuilt.claimed, 1, 'HTTP fail-closed 后 worker 必须领取对应投影更新')
    const recovered = await fetch(`${baseUrl}/__aisys__/api/my-accounts?status=active&page=1&pageSize=20`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const recoveredBody = await recovered.text()
    assert.equal(recovered.status, 200, `投影重建后 HTTP 列表必须恢复 200，实际 ${recovered.status}: ${recoveredBody}`)

    const optionsRecovered = await fetch(`${baseUrl}/__aisys__/api/my-accounts/options?page=1&limit=1`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const optionsRecoveredBody = await optionsRecovered.text()
    assert.equal(optionsRecovered.status, 200, `账户 options 投影重建后必须恢复 200，实际 ${optionsRecovered.status}: ${optionsRecoveredBody}`)
  } finally {
    runtimeConfig.background.accountListAvailabilityProjectionReadEnabled = previousReadEnabled
    if (server) await closeServer(server)
  }
}

async function verifyProjectionDeletionRecovery(
  client: DatabaseClient,
  access: { systemAccountId: string; role: 'user' }
): Promise<void> {
  const softDeletedAccountId = `${accountPrefix}0032`
  const hardDeletedAccountId = `${accountPrefix}0031`
  await client.execute(`
    UPDATE ${table(client, 'accounts')}
    SET deleted_at = ?
    WHERE id = ?
  `, [new Date().toISOString(), softDeletedAccountId])
  const softDeleteMaintenance = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-soft-delete',
    batchSize: 100
  })
  assert.equal(softDeleteMaintenance.claimed, 1, '软删除必须留下可领取的投影删除任务')
  assert.equal(softDeleteMaintenance.deleted, 1, '软删除必须移除可见投影')
  await assertShadowDefaultEqual(client, access, 'soft-delete')

  await client.execute(`DELETE FROM ${table(client, 'accounts')} WHERE id = ?`, [hardDeletedAccountId])
  const hardDeleteMaintenance = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-hard-delete',
    batchSize: 100
  })
  assert.equal(hardDeleteMaintenance.claimed, 0, '硬删除依赖级联清理，不应制造无法读取的幽灵 claim')
  await assertShadowDefaultEqual(client, access, 'hard-delete')
}

async function verifyAuthorizedSourceScheduleTransition(client: DatabaseClient): Promise<void> {
  const row = await client.one<{ next_transition_at: string | null }>(`
    SELECT next_transition_at
    FROM ${table(client, 'account_list_availability_projections')}
    WHERE viewer_system_account_id = ? AND account_id = ?
  `, [viewerId, scheduledAuthorizedAccountId])
  assert(row?.next_transition_at, '授权实例必须继承来源账户 availability schedule 的下一个过渡点')
  assert(Date.parse(row.next_transition_at) > Date.now(), '授权实例来源计划的过渡点必须在未来，才能在边界前 fail-closed')

  const boundaryAt = new Date('2026-08-10T12:34:00.000Z')
  const endingSchedule = JSON.stringify({
    enabled: true,
    timezone: 'UTC',
    mode: 'allow_windows',
    windows: [{ daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '12:33', end: '12:34' }]
  })
  await client.execute(`
    UPDATE ${table(client, 'accounts')}
    SET status = 'active',
        availability_schedule_json = ?,
        availability_schedule_next_check_at = ?
    WHERE id = ?
  `, [endingSchedule, boundaryAt.toISOString(), scheduledSourceAccountId])
  const transition = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-schedule-boundary',
    batchSize: 100,
    now: boundaryAt
  })
  assert.equal(transition.claimed, 2, '来源时间计划边界必须同时重投影来源和授权实例')
  assert.equal(transition.projected, 2, '来源时间计划边界重投影必须完整提交')
  const source = await client.one<{ status: string }>(`
    SELECT status FROM ${table(client, 'accounts')} WHERE id = ?
  `, [scheduledSourceAccountId])
  assert.equal(
    source?.status,
    'disabled',
    `时间计划结束边界必须在投影前写回来源账户禁用状态，maintenance=${JSON.stringify(transition)}`
  )
  const activeRows = await client.query<{ account_id: string }>(`
    SELECT account_id
    FROM ${table(client, 'account_list_availability_projections')}
    WHERE viewer_system_account_id = ? AND effective_status = 'active'
  `, [viewerId])
  assert(!activeRows.some((item) => item.account_id === scheduledAuthorizedAccountId), '来源禁用后授权实例不能被投影为 active')
}

async function verifyLastUsedAtProjectionUpdate(client: DatabaseClient): Promise<void> {
  const accountId = `${accountPrefix}0001`
  const lastUsedAt = '2026-08-10T12:35:00.000Z'
  await client.execute(`
    UPDATE ${table(client, 'accounts')}
    SET last_used_at = ?, updated_at = ?
    WHERE id = ?
  `, [lastUsedAt, lastUsedAt, accountId])
  const dirty = await client.one<{ count: string }>(`
    SELECT count(*)::text AS count
    FROM ${table(client, 'account_list_availability_dirty')}
    WHERE account_id = ?
  `, [accountId])
  assert.equal(Number(dirty?.count ?? 0), 0, '仅 last_used_at 遥测更新不得将整个 viewer 列表置为 unavailable')
  const projection = await client.one<{ last_used_at_sort_key: string | null; payload_json: string }>(`
    SELECT last_used_at_sort_key, payload_json
    FROM ${table(client, 'account_list_availability_projections')}
    WHERE viewer_system_account_id = ? AND account_id = ?
  `, [viewerId, accountId])
  assert.equal(projection?.last_used_at_sort_key, lastUsedAt, 'last_used_at 排序键必须原地刷新')
  assert.equal(JSON.parse(projection?.payload_json ?? '{}').lastUsedAt, lastUsedAt, 'last_used_at 展示字段必须原地刷新')
}

/**
 * The base availability projection deliberately does not become dirty for
 * every usage/balance write. These values are instead joined only after the
 * indexed page has been selected, within the same PostgreSQL statement.
 */
async function verifyLivePageDynamicOverlays(
  client: DatabaseClient,
  access: { systemAccountId: string; role: 'user' }
): Promise<void> {
  const accountId = `${accountPrefix}0004`
  const now = new Date().toISOString()
  const nextRefreshAfter = new Date(Date.now() + 60 * 60_000).toISOString()
  await client.execute(`
    UPDATE ${table(client, 'accounts')}
    SET balance_query_enabled = 1,
        balance_query_config_json = ?,
        balance_query_next_refresh_at = ?,
        updated_at = ?
    WHERE id = ?
  `, [JSON.stringify({ adapter: 'builtin', intervalMinutes: 5 }), nextRefreshAfter, now, accountId])
  const rebuilt = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-live-overlays-base',
    batchSize: 100
  })
  assert.equal(rebuilt.claimed, 1, '余额配置变化必须只重建目标账户的基础投影')
  assert.equal(rebuilt.projected, 1, '余额配置变化必须完成基础投影更新')

  const timezone = await usageStatsTimezoneAsync()
  const statDate = todayDateKey(timezone)
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${statsTable(tx, 'usage_stats_daily')} (
        system_account_id, scope_type, scope_id, stat_date,
        request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at
      ) VALUES (?, 'account', ?, ?, 7, 11, 13, 1.23, ?, ?)
      ON CONFLICT(system_account_id, scope_type, scope_id, stat_date) DO UPDATE SET
        request_count = excluded.request_count,
        input_tokens = excluded.input_tokens,
        output_tokens = excluded.output_tokens,
        total_cost_usd = excluded.total_cost_usd,
        last_used_at = excluded.last_used_at,
        updated_at = excluded.updated_at
    `, [viewerId, accountId, statDate, now, now])
    await tx.execute(`
      INSERT INTO ${statsTable(tx, 'account_usage_snapshots')} (
        system_account_id, account_id, kind, snapshot_json,
        refresh_status, last_attempt_at, last_success_at, next_refresh_after, updated_at, created_at
      ) VALUES (?, ?, 'relay_balance', ?, 'fresh', ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
        snapshot_json = excluded.snapshot_json,
        refresh_status = excluded.refresh_status,
        last_attempt_at = excluded.last_attempt_at,
        last_success_at = excluded.last_success_at,
        next_refresh_after = excluded.next_refresh_after,
        updated_at = excluded.updated_at
    `, [
      viewerId,
      accountId,
      JSON.stringify({
        status: 'fresh',
        remainingUsd: '12.340000',
        rawRemaining: '12.34',
        rawUnit: 'usd',
        basis: 'wallet',
        lastAttemptAt: now,
        lastSuccessAt: now
      }),
      now,
      now,
      nextRefreshAfter,
      now,
      now
    ])
  })
  const dirty = await client.one<{ count: string }>(`
    SELECT count(*)::text AS count
    FROM ${table(client, 'account_list_availability_dirty')}
    WHERE account_id = ?
  `, [accountId])
  assert.equal(Number(dirty?.count ?? 0), 0, '显示型用量/余额写入不得让整个列表进入基础投影重建')
  const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access,
    options: { ids: [accountId], page: 1, pageSize: 20 }
  })
  assert.equal(comparison.outcome, 'equal', `同 SQL 动态 overlay 必须与旧链路逐字段一致：${comparison.reason ?? ''}`)

  await client.execute(`
    INSERT INTO ${statsTable(client, 'usage_stats_daily')} (
      system_account_id, scope_type, scope_id, stat_date,
      request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, 'account_authorization', ?, ?, 3, 5, 8, 0.45, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_date) DO UPDATE SET
      request_count = excluded.request_count,
      input_tokens = excluded.input_tokens,
      output_tokens = excluded.output_tokens,
      total_cost_usd = excluded.total_cost_usd,
      last_used_at = excluded.last_used_at,
      updated_at = excluded.updated_at
  `, [viewerId, scheduledAuthorizationId, statDate, now, now])
  const authorizationLastUsedAt = '2026-08-10T12:40:00.000Z'
  await client.execute(`
    INSERT INTO ${statsTable(client, 'usage_stats_totals')} (
      system_account_id, scope_type, scope_id, last_used_at, updated_at
    ) VALUES (?, 'account_authorization', ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      last_used_at = excluded.last_used_at,
      updated_at = excluded.updated_at
  `, [viewerId, scheduledAuthorizationId, authorizationLastUsedAt, now])
  const authorizedComparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access,
    options: { ids: [scheduledAuthorizedAccountId], page: 1, pageSize: 20 }
  })
  assert.equal(
    authorizedComparison.outcome,
    'equal',
    `授权实例的 account_authorization 当日用量必须由同 SQL overlay 返回：${authorizedComparison.reason ?? ''}`
  )
}

async function verifySortParity(
  client: DatabaseClient,
  access: { systemAccountId: string; role: 'user' }
): Promise<void> {
  const alphaAccountId = `${accountPrefix}0001`
  const zuluAccountId = `${accountPrefix}0002`
  await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${table(tx, 'accounts')}
      SET name = CASE id
          WHEN ? THEN 'zulu projection shadow'
          WHEN ? THEN 'Alpha projection shadow'
          ELSE name
        END,
        last_used_at = CASE id
          WHEN ? THEN '2026-08-10T12:36:00.000Z'
          WHEN ? THEN NULL
          ELSE last_used_at
        END,
        updated_at = '2026-08-10T12:36:00.000Z'
      WHERE id IN (?, ?)
    `, [zuluAccountId, alphaAccountId, zuluAccountId, alphaAccountId, zuluAccountId, alphaAccountId])
  })
  const rebuilt = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-sort-parity',
    batchSize: 100
  })
  assert.equal(rebuilt.claimed, 2, '名称排序键变更必须精确重投影对应账户')
  assert.equal(rebuilt.projected, 2, '名称排序键变更必须完整提交')
  const sortCases: Array<{ label: string; sorts: AccountListSort[] }> = [
    { label: 'name-asc', sorts: [{ field: 'name', order: 'asc' }] },
    { label: 'name-desc', sorts: [{ field: 'name', order: 'desc' }] },
    { label: 'last-used-asc', sorts: [{ field: 'lastUsedAt', order: 'asc' }] },
    { label: 'last-used-desc', sorts: [{ field: 'lastUsedAt', order: 'desc' }] }
  ]
  for (const { label, sorts } of sortCases) {
    const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
      access,
      options: { page: 1, pageSize: 20, sorts }
    })
    assert.equal(comparison.outcome, 'equal', `${label} 投影排序必须与旧链路一致：${comparison.reason ?? ''}`)
  }
}

async function verifyTeamQuotaThresholdProjection(client: DatabaseClient): Promise<void> {
  const sourceAccountId = `${accountPrefix}0003`
  const now = new Date().toISOString()
  const limitsJson = JSON.stringify({ daily: { enabled: true, limit: 1 } })
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${table(tx, 'system_teams')} (id, name, status, created_by, created_at, updated_at)
      VALUES (?, 'Projection shadow quota team', 'active', ?, ?, ?)
    `, [teamQuotaId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'system_team_members')} (
        id, team_id, system_account_id, member_role, status, joined_at, created_by, created_at, updated_at
      ) VALUES (?, ?, ?, 'member', 'active', ?, ?, ?, ?)
    `, [teamQuotaMemberId, teamQuotaId, viewerId, now, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'resource_authorizations')} (
        id, resource_type, resource_id, resource_owner_system_account_id,
        grantee_system_account_id, scope, status, effective_source_type, effective_source_team_id,
        created_by, created_at, updated_at
      ) VALUES (?, 'account', ?, ?, ?, 'use', 'active', 'team', ?, ?, ?, ?)
    `, [teamQuotaAuthorizationId, sourceAccountId, viewerId, viewerId, teamQuotaId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'resource_authorization_grants')} (
        id, resource_type, resource_id, resource_owner_system_account_id,
        grantee_type, grantee_team_id, scope, status, limits_json, created_by, created_at, updated_at
      ) VALUES (?, 'account', ?, ?, 'team', ?, 'use', 'active', ?, ?, ?, ?)
    `, [teamQuotaGrantId, sourceAccountId, viewerId, teamQuotaId, limitsJson, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'accounts')} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        priority, schedulable, health_check_model, health_check_endpoint_mode,
        authorization_instance_source_account_id, authorization_instance_authorization_id,
        authorization_instance_owner_system_account_id, created_at, updated_at
      )
      SELECT ?, ?, source.provider_code, source.provider_protocol_profile_id,
        source.protocol_code, source.protocol_version, 'Projection shadow team quota authorized', source.type,
        'active', '{}', 0, 1, source.health_check_model, source.health_check_endpoint_mode,
        source.id, ?, ?, ?, ?
      FROM ${table(tx, 'accounts')} source
      WHERE source.id = ?
    `, [
      teamQuotaAuthorizedAccountId,
      viewerId,
      teamQuotaAuthorizationId,
      viewerId,
      now,
      now,
      sourceAccountId
    ])
    await tx.execute(`
      INSERT INTO ${table(tx, 'group_accounts')} (
        system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 1, ?, ?)
    `, [viewerId, groupId, teamQuotaAuthorizedAccountId, teamQuotaAuthorizationId, now, now])
  })
  const initial = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-team-quota-initial',
    batchSize: 100
  })
  assert(initial.claimed >= 1, '团队授权实例创建必须生成可物化的投影任务')
  assert(initial.projected >= 1, '团队授权实例初始投影必须可读')
  const initialProjection = await client.one<{ account_id: string }>(`
    SELECT account_id
    FROM ${table(client, 'account_list_availability_projections')}
    WHERE viewer_system_account_id = ? AND account_id = ?
  `, [viewerId, teamQuotaAuthorizedAccountId])
  assert.equal(initialProjection?.account_id, teamQuotaAuthorizedAccountId, '团队授权实例必须已完成初始投影')

  const timezone = await usageStatsTimezoneAsync()
  const statDate = todayDateKey(timezone)
  await client.execute(`
    INSERT INTO ${statsTable(client, 'usage_stats_daily')} (
      system_account_id, scope_type, scope_id, stat_date, total_cost_usd, updated_at
    ) VALUES (?, 'account_authorization_team', ?, ?, 1, ?)
  `, [viewerId, `${teamQuotaAuthorizedAccountId}:${teamQuotaId}`, statDate, new Date().toISOString()])
  const dirty = await client.one<{ account_id: string }>(`
    SELECT account_id
    FROM ${table(client, 'account_list_availability_dirty')}
    WHERE account_id = ?
  `, [teamQuotaAuthorizedAccountId])
  assert.equal(dirty?.account_id, teamQuotaAuthorizedAccountId, '团队额度跨阈值必须在同一事务留下授权实例脏标记')
  const unavailable = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access: { systemAccountId: viewerId, role: 'user' },
    options: { ids: [teamQuotaAuthorizedAccountId], page: 1, pageSize: 20 }
  })
  assert.equal(unavailable.outcome, 'projection_unavailable', '额度跨阈值时投影读取不得回退旧运行态聚合')
  const refreshed = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-team-quota-refresh',
    batchSize: 100
  })
  assert.equal(refreshed.claimed, 1, '团队额度跨阈值必须只重投影被影响的实例')
  assert.equal(refreshed.projected, 1, '团队额度跨阈值重投影必须完整提交')
  const projected = await client.one<{ effective_status: string; next_transition_at: string | null }>(`
    SELECT effective_status, next_transition_at
    FROM ${table(client, 'account_list_availability_projections')}
    WHERE viewer_system_account_id = ? AND account_id = ?
  `, [viewerId, teamQuotaAuthorizedAccountId])
  assert.equal(projected?.effective_status, 'rate_limited', '团队额度达到上限后必须投影为 rate_limited')
  assert(projected?.next_transition_at && Date.parse(projected.next_transition_at) > Date.now(), '团队额度重置边界必须进入投影到期调度')
  const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access: { systemAccountId: viewerId, role: 'user' },
    options: { ids: [teamQuotaAuthorizedAccountId], status: 'rate_limited', page: 1, pageSize: 20 }
  })
  assert.equal(comparison.outcome, 'equal', `团队额度跨阈值后投影必须和旧链路一致：${comparison.reason ?? ''}`)
  const managementDisabled = await listAccountListAvailabilityProjectionPageInClient(client, {
    viewerSystemAccountId: viewerId,
    options: { ids: [teamQuotaAuthorizedAccountId], schedulable: 'disabled', page: 1, pageSize: 20 }
  })
  assert.equal(managementDisabled.items.some((item) => item.id === teamQuotaAuthorizedAccountId), false, '管理主列表 disabled 不得包含额度受限 cooling 账户')
  const optionsDisabled = await listAccountListAvailabilityProjectionPageInClient(client, {
    viewerSystemAccountId: viewerId,
    options: { ids: [teamQuotaAuthorizedAccountId], schedulable: 'disabled', page: 1, pageSize: 20 },
    includeDynamicOverlays: false
  })
  assert.equal(optionsDisabled.items.some((item) => item.id === teamQuotaAuthorizedAccountId), true, 'user options disabled 必须保留旧的 authorizationQuotaExceeded 账户语义')
}

async function verifyRuntimeDependencyFailureRecovery(
  client: DatabaseClient,
  access: { systemAccountId: string; role: 'user' }
): Promise<void> {
  await client.execute(`
    UPDATE ${table(client, 'account_list_availability_projection_dependency_health')}
    SET state = 'healthy', reason = NULL, updated_at = ?
    WHERE dependency_name = 'runtime_state'
  `, [new Date(Date.now() - 120_000).toISOString()])
  const staleHeartbeat = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access,
    options: { page: 1, pageSize: 20, status: 'active' }
  })
  assert.equal(staleHeartbeat.outcome, 'projection_unavailable', '运行态依赖心跳过期时必须 fail-closed')

  await markAccountListAvailabilityProjectionRuntimeDependencyUnavailableInClient(client, {
    reason: 'shadow_redis_dependency_outage'
  })
  const unavailable = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access,
    options: { page: 1, pageSize: 20, status: 'active' }
  })
  assert.equal(unavailable.outcome, 'projection_unavailable', 'Redis 运行态不可用时不得返回旧的可用性快照')
  const recovered = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-runtime-recovery',
    batchSize: 100
  })
  assert(recovered.runtimeRecoveryEnqueued > 0, '运行态恢复后必须显式排入全量投影重放')
  assert.equal(recovered.runtimeRecoveryCompleted, 1, '全量重放完成前不得将运行态依赖标为 healthy')
  const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access,
    options: { page: 1, pageSize: 20, status: 'active' }
  })
  assert.equal(comparison.outcome, 'equal', `运行态恢复后投影必须与旧链路一致：${comparison.reason ?? ''}`)
}

async function verifyConcurrencyOverlayProjection(client: DatabaseClient): Promise<void> {
  const accountId = `${accountPrefix}0001`
  const slot = await tryAcquireAccountConcurrencyAsync(accountId, 3, { lane: 'text' })
  assert.equal(slot.acquired, true, 'Redis 并发 overlay 回归必须取得隔离账号的槽位')
  try {
    const reconciled = await runAccountListAvailabilityProjectionMaintenance({
      ownerId: 'account-list-projection-shadow-concurrency-overlay',
      batchSize: 100
    })
    assert(reconciled.runtimeOverlayReconciled >= 1, 'Redis 并发变更必须被后台写入 PostgreSQL overlay')
    const page = await shadowCompareAccountListAvailabilityProjectionPage(client, {
      access: { systemAccountId: viewerId, role: 'user' },
      options: { ids: [accountId], page: 1, pageSize: 20 }
    })
    assert.equal(page.outcome, 'equal', `并发 overlay 更新后投影必须与旧链路一致：${page.reason ?? ''}`)
  } finally {
    slot.release()
  }
}

async function assertShadowDefaultEqual(
  client: DatabaseClient,
  access: { systemAccountId: string; role: 'user' },
  label: string
): Promise<void> {
  const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access,
    options: { page: 1, pageSize: 20 }
  })
  assert.equal(comparison.outcome, 'equal', `${label} 后投影列表必须与旧链路一致：${comparison.reason ?? ''}`)
}

async function seedIndexedKeywordDocuments(client: DatabaseClient): Promise<void> {
  const terms = accountNameSearchQueryTerms('shadow account')
  assert(terms.length > 0, '关键词夹具必须生成检索词')
  const now = new Date().toISOString()
  const values = terms.map(() => '(?)').join(', ')
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_name_search_documents')} (account_id, system_account_id, normalized_name, updated_at)
      SELECT accounts.id, accounts.system_account_id, lower(accounts.name), ?
      FROM ${table(tx, 'accounts')} accounts
      WHERE accounts.id LIKE ?
    `, [now, `${accountPrefix}%`])
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_name_search_terms')} (account_id, system_account_id, term, created_at)
      SELECT accounts.id, accounts.system_account_id, terms.term, ?
      FROM ${table(tx, 'accounts')} accounts
      CROSS JOIN (VALUES ${values}) AS terms(term)
      WHERE accounts.id LIKE ?
    `, [now, ...terms, `${accountPrefix}%`])
  })
}

async function seed(client: DatabaseClient): Promise<{ providerCode: string }> {
  const now = new Date().toISOString()
  const provider = await client.one<{ code: string }>(`
    SELECT code FROM ${table(client, 'providers')} ORDER BY code ASC LIMIT 1
  `)
  const profile = await client.one<{
    id: string
    provider_code: string
    protocol_code: string
    protocol_version: string
    default_health_check_model: string
  }>(`
    SELECT id, provider_code, protocol_code, protocol_version, default_health_check_model
    FROM ${table(client, 'provider_protocol_profiles')}
    WHERE enabled = 1
    ORDER BY id ASC
    LIMIT 1
  `)
  assert(provider && profile, '隔离 PostgreSQL 必须已写入默认 provider/profile seed')
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${table(tx, 'system_accounts')} (
        id, username, display_name, role, status, password_hash, created_at, updated_at
      ) VALUES (?, ?, 'Projection shadow viewer', 'user', 'active', 'shadow-not-a-login-secret', ?, ?)
    `, [viewerId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'groups')} (
        id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at
      ) VALUES (?, ?, 'Projection shadow group', ?, 1, 'personal', ?, ?)
    `, [groupId, viewerId, profile.provider_code, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_tags')} (id, system_account_id, name, created_at, updated_at)
      VALUES (?, ?, 'Projection shadow tag', ?, ?)
    `, [tagId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'accounts')} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        priority, schedulable, health_check_model, health_check_endpoint_mode, created_at, updated_at
      )
      SELECT ? || lpad(gs::text, 4, '0'), ?, ?, ?, ?, ?,
        'projection shadow account ' || lpad(gs::text, 4, '0'), 'api_key',
        CASE WHEN gs % 7 = 0 THEN 'disabled' WHEN gs % 5 = 0 THEN 'temporary_unavailable' ELSE 'active' END,
        '{}', gs % 10, CASE WHEN gs % 7 = 0 THEN 0 ELSE 1 END, ?, 'chat_json', ?, ?
      FROM generate_series(1, ?) AS generated(gs)
    `, [accountPrefix, viewerId, profile.provider_code, profile.id, profile.protocol_code, profile.protocol_version, profile.default_health_check_model, now, now, accountCount])
    const sourceSchedule = JSON.stringify({
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows',
      windows: [{ daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '23:59' }]
    })
    await tx.execute(`
      INSERT INTO ${table(tx, 'accounts')} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        availability_schedule_json, priority, schedulable, health_check_model, health_check_endpoint_mode, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, 'Projection shadow scheduled source', 'api_key', 'active', '{}', ?, 0, 1, ?, 'chat_json', ?, ?)
    `, [scheduledSourceAccountId, viewerId, profile.provider_code, profile.id, profile.protocol_code, profile.protocol_version, sourceSchedule, profile.default_health_check_model, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'resource_authorizations')} (
        id, resource_type, resource_id, resource_owner_system_account_id,
        grantee_system_account_id, scope, status, effective_source_type, created_by, created_at, updated_at
      ) VALUES (?, 'account', ?, ?, ?, 'use', 'active', 'manual', ?, ?, ?)
    `, [scheduledAuthorizationId, scheduledSourceAccountId, viewerId, viewerId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'accounts')} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        priority, schedulable, health_check_model, health_check_endpoint_mode,
        authorization_instance_source_account_id, authorization_instance_authorization_id, authorization_instance_owner_system_account_id,
        created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, 'Projection shadow scheduled authorized', 'api_key', 'active', '{}', 0, 1, ?, 'chat_json', ?, ?, ?, ?, ?)
    `, [scheduledAuthorizedAccountId, viewerId, profile.provider_code, profile.id, profile.protocol_code, profile.protocol_version, profile.default_health_check_model, scheduledSourceAccountId, scheduledAuthorizationId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'group_accounts')} (
        system_account_id, group_id, account_id, enabled, created_at, updated_at
      )
      SELECT ?, ?, ? || lpad(gs::text, 4, '0'), 1, ?, ?
      FROM generate_series(1, ?) AS generated(gs)
      WHERE gs % 2 = 0
    `, [viewerId, groupId, accountPrefix, now, now, accountCount])
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_tag_bindings')} (account_id, tag_id, system_account_id, created_at)
      SELECT ? || lpad(gs::text, 4, '0'), ?, ?, ?
      FROM generate_series(1, ?) AS generated(gs)
      WHERE gs % 3 = 0
    `, [accountPrefix, tagId, viewerId, now, accountCount])
  })
  return { providerCode: provider.code }
}

async function cleanup(client: DatabaseClient): Promise<void> {
  const accountIds = `${accountPrefix}%`
  await client.transaction(async (tx) => {
    await tx.execute(`
      DELETE FROM ${statsTable(tx, 'usage_stats_daily')}
      WHERE scope_id = ?
    `, [`${teamQuotaAuthorizedAccountId}:${teamQuotaId}`])
    await tx.execute(`
      DELETE FROM ${table(tx, 'account_schedule_status_events')}
      WHERE account_id LIKE ? OR account_id IN (?, ?, ?)
    `, [accountIds, scheduledAuthorizedAccountId, scheduledSourceAccountId, teamQuotaAuthorizedAccountId])
    await tx.execute(`
      DELETE FROM ${table(tx, 'accounts')}
      WHERE id = ?
    `, [teamQuotaAuthorizedAccountId])
    await tx.execute(`
      DELETE FROM ${table(tx, 'accounts')}
      WHERE id = ?
    `, [scheduledAuthorizedAccountId])
    await tx.execute(`DELETE FROM ${table(tx, 'accounts')} WHERE id LIKE ?`, [accountIds])
    await tx.execute(`
      DELETE FROM ${table(tx, 'accounts')}
      WHERE id = ?
    `, [scheduledSourceAccountId])
    await tx.execute(`DELETE FROM ${table(tx, 'resource_authorizations')} WHERE id IN (?, ?)`, [scheduledAuthorizationId, teamQuotaAuthorizationId])
    await tx.execute(`DELETE FROM ${table(tx, 'resource_authorization_grants')} WHERE id = ?`, [teamQuotaGrantId])
    await tx.execute(`DELETE FROM ${table(tx, 'system_teams')} WHERE id = ?`, [teamQuotaId])
    await tx.execute(`DELETE FROM ${table(tx, 'account_tags')} WHERE id = ?`, [tagId])
    await tx.execute(`DELETE FROM ${table(tx, 'groups')} WHERE id = ?`, [groupId])
    await tx.execute(`DELETE FROM ${table(tx, 'system_accounts')} WHERE id = ?`, [viewerId])
  })
}

function assertScratchDatabase(): void {
  if (runtimeConfig.databaseDriver !== 'postgres') throw new Error('影子对账必须在 PostgreSQL 模式运行')
  const postgresUrl = runtimeConfig.postgres.url
  if (!postgresUrl) throw new Error('影子对账缺少 JUHE_AI_POSTGRES_URL')
  const name = new URL(postgresUrl).pathname.replace(/^\//, '')
  if (!/^juhe_ai_sub2api_dev_[a-z0-9_]{3,80}$/.test(name)) {
    throw new Error(`影子对账只允许隔离开发库，当前 database=${name}`)
  }
}

function table(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_business', name)
}

function statsTable(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_stats', name)
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

function closeServer(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve())
  })
}

await main().catch(async (error) => {
  console.error(error instanceof Error ? (error.stack ?? error.message) : String(error))
  await closePostgresPool()
  process.exitCode = 1
})
// Runtime Redis clients intentionally stay open in the server process. This
// standalone regression has already finished its awaited cleanup, so force a
// deterministic process boundary instead of leaking a test worker.
process.exit(process.exitCode ?? 0)
