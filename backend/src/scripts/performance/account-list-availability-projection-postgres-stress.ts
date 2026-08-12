import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { runAccountListAvailabilityProjectionMaintenance } from '../../modules/accounts/account-list-availability-projection.service.js'
import { accountNameSearchQueryTerms } from '../../storage/account-name-search.repository.js'
import {
  closePostgresPool,
  getPostgresPool
} from '../../storage/postgres-client.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import {
  AccountListAvailabilityProjectionUnavailableError,
  completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient,
  ensureAccountListAvailabilityProjectionRuntimeDependencyInClient,
  listAccountListAvailabilityProjectionPageInClient,
  markAccountListAvailabilityDirtyInClient,
  refreshAccountListAvailabilityProjectionViewerHealthInClient,
  touchAccountListAvailabilityProjectionRuntimeDependencyInClient
} from '../../storage/account-list-availability-projection.repository.js'

const viewerSystemAccountId = 'account-list-projection-stress-viewer-20260810'
const accountPrefix = 'account-list-projection-stress-account-20260810-'
const rowCount = 20_000
const defaultDurationMs = 45_000
const defaultConcurrency = 80
const pageSize = 20

interface StressCase {
  name: string
  options: Record<string, unknown>
  includeDynamicOverlays: boolean
}

interface LatencySummary {
  name: string
  count: number
  p50Ms: number
  p95Ms: number
  p99Ms: number
  queryP95Ms: number
  postQueryP95Ms: number
  maxMs: number
  errors: number
}

interface PoolMetrics {
  maxTotal: number
  maxIdle: number
  maxWaiting: number
}

interface PoolMetricsSource {
  totalCount?: number
  idleCount?: number
  waitingCount?: number
}

const managementCases: StressCase[] = [
  { name: 'default', options: { page: 1, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: true },
  { name: 'status-active', options: { status: 'active', page: 1, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: true },
  { name: 'status-sparse', options: { status: 'quality_isolated', page: 1, pageSize, sorts: [{ field: 'name', order: 'asc' }] }, includeDynamicOverlays: true },
  { name: 'schedulable-cooling', options: { schedulable: 'cooling', page: 1, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: true },
  { name: 'provider-status', options: { providerCode: '__stress_provider__', status: 'active', page: 1, pageSize }, includeDynamicOverlays: true },
  { name: 'group-status', options: { groupId: 'projection-stress-group-3', status: 'active', page: 1, pageSize }, includeDynamicOverlays: true },
  { name: 'tag-status', options: { tagIds: ['projection-stress-tag-3'], status: 'active', page: 1, pageSize }, includeDynamicOverlays: true },
  { name: 'keyword-contains', options: { keyword: 'batch', page: 1, pageSize, sorts: [{ field: 'name', order: 'asc' }] }, includeDynamicOverlays: true },
  { name: 'deep-page', options: { status: 'active', page: 50, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: true }
]

const optionsCases: StressCase[] = [
  { name: 'options-default', options: { page: 1, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: false },
  { name: 'options-status-active', options: { status: 'active', page: 1, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: false },
  { name: 'options-status-sparse', options: { status: 'quality_isolated', page: 1, pageSize, sorts: [{ field: 'name', order: 'asc' }] }, includeDynamicOverlays: false },
  { name: 'options-schedulable-enabled', options: { schedulable: 'enabled', page: 1, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: false },
  { name: 'options-schedulable-disabled', options: { schedulable: 'disabled', page: 1, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: false },
  { name: 'options-provider-status', options: { providerCode: '__stress_provider__', status: 'active', page: 1, pageSize }, includeDynamicOverlays: false },
  { name: 'options-group-status', options: { groupId: 'projection-stress-group-3', status: 'active', page: 1, pageSize }, includeDynamicOverlays: false },
  { name: 'options-tag-status', options: { tagIds: ['projection-stress-tag-3'], status: 'active', page: 1, pageSize }, includeDynamicOverlays: false },
  { name: 'options-keyword-contains', options: { keyword: 'batch', page: 1, pageSize, sorts: [{ field: 'name', order: 'asc' }] }, includeDynamicOverlays: false },
  { name: 'options-deep-page', options: { status: 'active', page: 50, pageSize, sorts: [{ field: 'priority', order: 'asc' }] }, includeDynamicOverlays: false }
]

const cases: StressCase[] = [...managementCases, ...optionsCases]

async function main(): Promise<void> {
  assertScratchDatabase()
  const durationMs = integerArg('--duration-ms', defaultDurationMs, 5_000, 10 * 60_000)
  const concurrency = integerArg('--concurrency', defaultConcurrency, 1, 200)
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)
  const startedAt = new Date().toISOString()
  try {
    console.error('account-list projection stress: cleaning prior fixture')
    await cleanupStressFixture(client)
    console.error('account-list projection stress: seeding fixture')
    const profile = await seedStressFixture(client)
    await refreshAccountListAvailabilityProjectionViewerHealthInClient(client, {
      viewerSystemAccountId
    })
    await touchAccountListAvailabilityProjectionRuntimeDependencyInClient(client, {})
    if (process.argv.includes('--print-explain')) await printProjectionReadiness(client)
    await ensureAccountListAvailabilityProjectionRuntimeDependencyInClient(client, {})
    await touchAccountListAvailabilityProjectionRuntimeDependencyInClient(client, {})
    await completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient(client, {})
    await verifyPostgresDirtyTriggers(client)
    // Trigger coverage intentionally dirties one row; restore the fixture's
    // health watermark before measuring request latency.
    await refreshAccountListAvailabilityProjectionViewerHealthInClient(client, {
      viewerSystemAccountId
    })
    console.error('account-list projection stress: verifying plan and one-query filters')
    await verifyExplainStatusPage(client)
    await verifyOneQueryPerFilteredPage(client)
    const planSummaries = await verifyFilteredQueryPlans(client)
    await verifyDirtyReadFailsClosed(client)
    console.error('account-list projection stress: running concurrent queries')
    const poolMetrics: PoolMetrics = { maxTotal: 0, maxIdle: 0, maxWaiting: 0 }
    await warmPostgresPool(client, concurrency)
    const summaries = await runWithProjectionMaintenanceHeartbeat(
      () => runConcurrentPressure(client, durationMs, concurrency, pool as PoolMetricsSource, poolMetrics)
    )
    const report = {
      database: scratchDatabaseName(),
      redisNamespace: runtimeConfig.redis.namespace,
      rowCount,
      durationMs,
      concurrency,
      startedAt,
      completedAt: new Date().toISOString(),
      profile,
      poolMetrics,
      planSummaries,
      summaries
    }
    console.log(JSON.stringify(report, null, 2))
    assertPressureAcceptance(summaries, concurrency)
  } finally {
    await cleanupStressFixture(client)
    await closePostgresPool()
  }
}

/**
 * The request reader deliberately rejects an expired runtime heartbeat. Keep
 * the pressure run faithful to the enabled deployment by running the same
 * bounded maintenance path that refreshes it in production.
 */
async function runWithProjectionMaintenanceHeartbeat<T>(work: () => Promise<T>): Promise<T> {
  let stopped = false
  let heartbeatFailure: unknown
  const heartbeat = (async () => {
    while (!stopped) {
      try {
        await runAccountListAvailabilityProjectionMaintenance({
          ownerId: `account-list-projection-stress-heartbeat-${process.pid}`,
          batchSize: 100,
          maxBatchesPerRun: 1,
          workerConcurrency: 1
        })
      } catch (error) {
        heartbeatFailure = error
        return
      }
      await waitForProjectionMaintenanceHeartbeat()
    }
  })()
  try {
    const result = await work()
    if (heartbeatFailure) throw heartbeatFailure
    return result
  } finally {
    stopped = true
    await heartbeat
    if (heartbeatFailure) throw heartbeatFailure
  }
}

function waitForProjectionMaintenanceHeartbeat(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 1_000))
}

async function verifyFilteredQueryPlans(client: DatabaseClient): Promise<Array<{ name: string; executionMs: number }>> {
  const summaries: Array<{ name: string; executionMs: number }> = []
  for (const testCase of cases.filter(({ name }) => name === 'default' || name === 'keyword-contains')) {
    let captured: { sql: string; params: readonly unknown[] } | undefined
    const capturingClient: DatabaseClient = {
      ...client,
      async query<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T[]> {
        captured = { sql, params: params ?? [] }
        return client.query<T>(sql, params)
      },
      async one<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T | undefined> {
        throw new Error(`列表查询计划 ${testCase.name} 不应调用 one()：${sql}`)
      }
    }
    await listAccountListAvailabilityProjectionPageInClient(capturingClient, {
      viewerSystemAccountId,
      options: testCase.options,
      includeDynamicOverlays: testCase.includeDynamicOverlays
    })
    assert(captured, `筛选 ${testCase.name} 未捕获到 SQL`)
    const planRows = await client.query<Record<string, unknown>>(
      `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) ${captured.sql}`,
      captured.params
    )
    const plan = JSON.stringify(planRows)
    assert.doesNotMatch(
      plan,
      /"Alias":"(?:stale_projections|due_projections)"/,
      `筛选 ${testCase.name} 的 request freshness 不得扫描账户投影表`
    )
    const executionMs = postgresExplainExecutionMs(planRows)
    if (process.argv.includes('--print-explain')) {
      console.error(JSON.stringify({
        name: testCase.name,
        executionMs,
        hotNodes: postgresExplainHotNodes(planRows),
        jit: postgresExplainJit(planRows)
      }, null, 2))
    }
    assert(
      executionMs <= 50,
      `筛选 ${testCase.name} 的单条 PostgreSQL SQL 执行必须在 50ms 内，实际 ${executionMs}ms；热节点=${JSON.stringify(postgresExplainHotNodes(planRows))}`
    )
    summaries.push({ name: testCase.name, executionMs })
  }
  return summaries
}

async function seedStressFixture(client: DatabaseClient): Promise<{ providerCode: string; protocolProfileId: string }> {
  const accounts = table(client, 'accounts')
  const projections = table(client, 'account_list_availability_projections')
  const projectionIndex = table(client, 'account_list_availability_projection_index')
  const projectionTags = table(client, 'account_list_availability_projection_tags')
  const projectionSearchTerms = table(client, 'account_list_availability_projection_search_terms')
  const dirty = table(client, 'account_list_availability_dirty')
  const accountTags = table(client, 'account_tags')
  const accountTagBindings = table(client, 'account_tag_bindings')
  const searchTerms = table(client, 'account_name_search_terms')
  const searchDocuments = table(client, 'account_name_search_documents')
  const systemAccounts = table(client, 'system_accounts')
  const now = new Date().toISOString()
  const provider = await client.one<{ code: string }>(`
    SELECT code FROM ${table(client, 'providers')} ORDER BY code ASC LIMIT 1
  `)
  const profile = await client.one<{ id: string; provider_code: string; protocol_code: string; protocol_version: string; default_health_check_model: string }>(`
    SELECT id, provider_code, protocol_code, protocol_version, default_health_check_model
    FROM ${table(client, 'provider_protocol_profiles')}
    WHERE enabled = 1
    ORDER BY id ASC
    LIMIT 1
  `)
  assert(provider && profile, '隔离库必须存在默认 provider/profile seed')
  const searchTermsForBatch = accountNameSearchQueryTerms('batch')
  assert(searchTermsForBatch.length > 0, '名称检索夹具必须生成可索引 terms')

  await setProjectionFixtureTriggersEnabled(client, false)
  try {
    await client.transaction(async (tx) => {
    // The scratch fixture intentionally writes 20k rows plus indexed grams.
    // Keep the longer timeout local to this setup transaction only.
    await tx.execute("SET LOCAL statement_timeout = '120s'")
    await tx.execute(`
      INSERT INTO ${systemAccounts} (
        id, username, display_name, role, status, password_hash, created_at, updated_at
      ) VALUES (?, ?, ?, 'user', 'active', 'stress-only-not-a-login-secret', ?, ?)
      ON CONFLICT(id) DO NOTHING
    `, [viewerSystemAccountId, viewerSystemAccountId, 'Projection stress viewer', now, now])
    await tx.execute(`
      INSERT INTO ${accounts} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        health_check_model, health_check_endpoint_mode, created_at, updated_at
      )
      SELECT ? || lpad(gs::text, 6, '0'), ?, ?, ?, ?, ?,
        'projection batch account ' || lpad(gs::text, 6, '0'), 'api_key',
        CASE WHEN gs % 97 = 0 THEN 'quality_isolated'
             WHEN gs % 11 = 0 THEN 'temporary_unavailable'
             WHEN gs % 5 = 0 THEN 'rate_limited'
             ELSE 'active' END,
        '{}', ?, 'chat_json', ?, ?
      FROM generate_series(1, ?) AS generated(gs)
    `, [accountPrefix, viewerSystemAccountId, profile.provider_code, profile.id, profile.protocol_code, profile.protocol_version, profile.default_health_check_model, now, now, rowCount])
    await tx.execute(`
      INSERT INTO ${accountTags} (id, system_account_id, name, created_at, updated_at)
      SELECT 'projection-stress-tag-' || gs::text, ?, 'Projection stress tag ' || gs::text, ?, ?
      FROM generate_series(1, 5) AS generated(gs)
      ON CONFLICT(id) DO NOTHING
    `, [viewerSystemAccountId, now, now])
    await tx.execute(`
      INSERT INTO ${accountTagBindings} (account_id, tag_id, system_account_id, created_at)
      SELECT accounts.id, 'projection-stress-tag-' || ((generated.gs % 5) + 1)::text, ?, ?
      FROM ${accounts} accounts
      INNER JOIN generate_series(1, ?) AS generated(gs)
        ON accounts.id = ? || lpad(generated.gs::text, 6, '0')
    `, [viewerSystemAccountId, now, rowCount, accountPrefix])
    await tx.execute(`
      INSERT INTO ${projections} (
        viewer_system_account_id, account_id, effective_status, schedulable_bucket,
        provider_code, provider_protocol_profile_id, account_type, bound_group_id,
        name_sort_key, priority_sort_key, super_priority_sort_key, fallback_sort_key,
        concurrency_sort_key, created_at_sort_key, payload_json, source_generation,
        projected_at
      )
      SELECT ?, accounts.id,
        accounts.status,
        CASE WHEN generated.gs % 5 = 0 THEN 'cooling'
             WHEN generated.gs % 7 = 0 THEN 'disabled'
             ELSE 'enabled' END,
        '__stress_provider__', ?, accounts.type,
        CASE WHEN generated.gs % 3 = 0 THEN 'projection-stress-group-3' ELSE 'projection-stress-group-1' END,
        accounts.name, generated.gs % 100,
        CASE WHEN generated.gs % 2 = 0 THEN 1 ELSE 0 END,
        CASE WHEN generated.gs % 13 = 0 THEN 1 ELSE 0 END,
        5000, accounts.created_at,
        jsonb_build_object(
          'id', accounts.id,
          'name', accounts.name,
          'providerCode', accounts.provider_code,
          'providerProtocolProfileId', accounts.provider_protocol_profile_id,
          'type', accounts.type,
          'status', accounts.status,
          'schedulable', generated.gs % 7 <> 0,
          'effectiveAvailability', jsonb_build_object('available', accounts.status = 'active'),
          'notes', repeat('projection-stress-', 32),
          'tags', '[]'::jsonb
        )::text,
        1, ?
      FROM ${accounts} accounts
      INNER JOIN generate_series(1, ?) AS generated(gs)
        ON accounts.id = ? || lpad(generated.gs::text, 6, '0')
    `, [viewerSystemAccountId, profile.id, now, rowCount, accountPrefix])
    await tx.execute(`
      INSERT INTO ${projectionIndex} (
        viewer_system_account_id, account_id, effective_status, schedulable_bucket,
        provider_code, provider_protocol_profile_id, account_type, bound_group_id,
        name_sort_key, priority_sort_key, super_priority_sort_key, fallback_sort_key,
        concurrency_sort_key, account_expires_at_sort_key, last_used_at_sort_key,
        created_at_sort_key, access_type_sort_key, search_index_complete, authorization_quota_exceeded
      )
      SELECT ?, accounts.id,
        accounts.status,
        CASE WHEN generated.gs % 5 = 0 THEN 'cooling'
             WHEN generated.gs % 7 = 0 THEN 'disabled'
             ELSE 'enabled' END,
        '__stress_provider__', ?, accounts.type,
        CASE WHEN generated.gs % 3 = 0 THEN 'projection-stress-group-3' ELSE 'projection-stress-group-1' END,
        accounts.name, generated.gs % 100,
        CASE WHEN generated.gs % 2 = 0 THEN 1 ELSE 0 END,
        CASE WHEN generated.gs % 13 = 0 THEN 1 ELSE 0 END,
        5000, NULL, NULL, accounts.created_at, 'owner', 1, 0
      FROM ${accounts} accounts
      INNER JOIN generate_series(1, ?) AS generated(gs)
        ON accounts.id = ? || lpad(generated.gs::text, 6, '0')
    `, [viewerSystemAccountId, profile.id, rowCount, accountPrefix])
    await tx.execute(`
      INSERT INTO ${projectionTags} (viewer_system_account_id, account_id, tag_id)
      SELECT ?, accounts.id, 'projection-stress-tag-' || ((generated.gs % 5) + 1)::text
      FROM ${accounts} accounts
      INNER JOIN generate_series(1, ?) AS generated(gs)
        ON accounts.id = ? || lpad(generated.gs::text, 6, '0')
    `, [viewerSystemAccountId, rowCount, accountPrefix])
    await tx.execute(`
      INSERT INTO ${searchDocuments} (account_id, system_account_id, normalized_name, updated_at)
      SELECT accounts.id, ?, accounts.name, ?
      FROM ${accounts} accounts
      WHERE accounts.id LIKE ?
    `, [viewerSystemAccountId, now, `${accountPrefix}%`])
    const values = searchTermsForBatch.map(() => '(?)').join(', ')
    await tx.execute(`
      INSERT INTO ${searchTerms} (account_id, system_account_id, term, created_at)
      SELECT accounts.id, ?, terms.term, ?
      FROM ${accounts} accounts
      CROSS JOIN (VALUES ${values}) AS terms(term)
      WHERE accounts.id LIKE ?
    `, [viewerSystemAccountId, now, ...searchTermsForBatch, `${accountPrefix}%`])
    await tx.execute(`
      INSERT INTO ${projectionSearchTerms} (
        viewer_system_account_id, account_id, term, name_sort_key, created_at_sort_key
      )
      SELECT ?, accounts.id, terms.term, accounts.name, accounts.created_at
      FROM ${accounts} accounts
      CROSS JOIN (VALUES ${values}) AS terms(term)
      WHERE accounts.id LIKE ?
    `, [viewerSystemAccountId, ...searchTermsForBatch, `${accountPrefix}%`])
    // The fixture is a completed materialization. Trigger coverage is tested
    // separately; leaving bootstrap dirty rows here would only test fail-closed.
    await tx.execute(`
      DELETE FROM ${dirty}
      WHERE account_id LIKE ?
    `, [`${accountPrefix}%`])
    await tx.execute(`ANALYZE ${accounts}`)
    await tx.execute(`ANALYZE ${projections}`)
    await tx.execute(`ANALYZE ${projectionIndex}`)
    await tx.execute(`ANALYZE ${projectionTags}`)
    await tx.execute(`ANALYZE ${projectionSearchTerms}`)
    await tx.execute(`ANALYZE ${searchTerms}`)
    await tx.execute(`ANALYZE ${searchDocuments}`)
    })
  } finally {
    await setProjectionFixtureTriggersEnabled(client, true)
  }
  return { providerCode: profile.provider_code, protocolProfileId: profile.id }
}

const projectionFixtureTriggerTargets = [
  ['accounts', 'account_list_availability_accounts_insert'],
  ['account_tag_bindings', 'account_list_availability_tag_bindings'],
  ['account_name_search_documents', 'account_list_availability_name_search_documents'],
  ['account_name_search_terms', 'account_list_availability_name_search_terms']
] as const

async function setProjectionFixtureTriggersEnabled(client: DatabaseClient, enabled: boolean): Promise<void> {
  for (const [tableName, triggerName] of projectionFixtureTriggerTargets) {
    await client.execute(`
      ALTER TABLE ${table(client, tableName)} ${enabled ? 'ENABLE' : 'DISABLE'} TRIGGER ${triggerName}
    `)
  }
}

async function verifyExplainStatusPage(client: DatabaseClient): Promise<void> {
  const projections = table(client, 'account_list_availability_projection_index')
  const result = await client.one<{ 'QUERY PLAN': unknown }>(`
    EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
    SELECT account_id
    FROM ${projections}
    WHERE viewer_system_account_id = ? AND effective_status = 'quality_isolated'
    ORDER BY priority_sort_key ASC, created_at_sort_key ASC, account_id ASC
    LIMIT 21
  `, [viewerSystemAccountId])
  assert.ok(result, '状态分页 EXPLAIN 必须返回查询计划')
}

async function printProjectionReadiness(client: DatabaseClient): Promise<void> {
  const health = await client.one<Record<string, unknown>>(`
    SELECT projection_count, oldest_projected_at, next_transition_at, is_current, updated_at
    FROM ${table(client, 'account_list_availability_projection_viewer_health')}
    WHERE viewer_system_account_id = ?
  `, [viewerSystemAccountId])
  const dependency = await client.one<Record<string, unknown>>(`
    SELECT dependency_name, state, updated_at
    FROM ${table(client, 'account_list_availability_projection_dependency_health')}
    WHERE dependency_name = 'runtime_state'
  `)
  const dirty = await client.one<{ count: string }>(`
    SELECT count(*)::text AS count
    FROM ${table(client, 'account_list_availability_dirty')}
    WHERE viewer_system_account_id = ?
  `, [viewerSystemAccountId])
  console.error(JSON.stringify({ readiness: { health, dependency, dirty: dirty?.count } }, null, 2))
}

async function verifyOneQueryPerFilteredPage(client: DatabaseClient): Promise<void> {
  for (const testCase of cases) {
    let queryCount = 0
    let capturedSql = ''
    const countedClient: DatabaseClient = {
      ...client,
      async query<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T[]> {
        queryCount += 1
        capturedSql = sql
        return client.query<T>(sql, params)
      },
      async one<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T | undefined> {
        throw new Error(`列表查询 ${testCase.name} 不应调用 one()：${sql}`)
      }
    }
    const page = await listAccountListAvailabilityProjectionPageInClient(countedClient, {
      viewerSystemAccountId,
      options: testCase.options,
      includeDynamicOverlays: testCase.includeDynamicOverlays
    })
    assert.equal(queryCount, 1, `筛选 ${testCase.name} 必须一次 SQL 完成，不得拆成多次聚合`)
    assert.doesNotMatch(capturedSql, /\b(accounts|resource_authorizations)\b/, `筛选 ${testCase.name} 请求 SQL 不得回读业务事实表`)
    assert(page.items.length <= pageSize, `筛选 ${testCase.name} 返回页不能超过 pageSize`)
  }
}

async function verifyPostgresDirtyTriggers(client: DatabaseClient): Promise<void> {
  const targetAccountId = `${accountPrefix}000001`
  const dirty = table(client, 'account_list_availability_dirty')
  const expectedTriggers = [
    'account_list_availability_accounts_update',
    'account_list_availability_authorizations',
    'account_list_availability_group_accounts',
    'account_list_availability_tag_bindings',
    'account_list_availability_api_key_runtime',
    'account_list_availability_circuits'
  ]
  const triggerRows = await client.query<{ tgname: string }>(`
    SELECT triggers.tgname
    FROM pg_trigger triggers
    WHERE NOT triggers.tgisinternal
      AND triggers.tgname IN (${client.dialect.bindPlaceholders(expectedTriggers.length)})
    ORDER BY triggers.tgname ASC
  `, expectedTriggers)
  assert.deepEqual(triggerRows.map((row) => row.tgname), [...expectedTriggers].sort(), '关键业务写入必须已安装投影脏标记 trigger')

  await client.execute(`
    UPDATE ${table(client, 'accounts')}
    SET priority = priority + 1
    WHERE id = ?
  `, [targetAccountId])
  await assertDirtyTrigger(client, dirty, targetAccountId, '账户状态写入')
  await client.execute(`DELETE FROM ${dirty} WHERE account_id = ?`, [targetAccountId])

  const taggedTarget = await client.one<{ account_id: string }>(`
    SELECT account_id
    FROM ${table(client, 'account_tag_bindings')}
    WHERE tag_id = 'projection-stress-tag-2'
    ORDER BY account_id ASC
    LIMIT 1
  `)
  assert(taggedTarget, '标签触发器夹具必须至少存在一个绑定账户')
  await client.execute(`
    UPDATE ${table(client, 'account_tags')}
    SET name = name || ' updated'
    WHERE id = 'projection-stress-tag-2'
  `)
  await assertDirtyTrigger(client, dirty, taggedTarget.account_id, '标签展示写入')
  await client.execute(`DELETE FROM ${dirty} WHERE account_id LIKE ?`, [`${accountPrefix}%`])

  await client.execute(`
    INSERT INTO ${table(client, 'account_api_key_runtime_states')} (
      id, system_account_id, account_id, key_fingerprint, key_index, status,
      created_at, updated_at
    ) VALUES (?, ?, ?, 'projection-stress-runtime-key', 0, 'active', now(), now())
  `, ['projection-stress-runtime-state-20260810', viewerSystemAccountId, targetAccountId])
  await assertDirtyTrigger(client, dirty, targetAccountId, 'API Key 运行态写入')
  await client.execute(`DELETE FROM ${dirty} WHERE account_id = ?`, [targetAccountId])
}

async function assertDirtyTrigger(
  client: DatabaseClient,
  dirty: string,
  accountId: string,
  label: string
): Promise<void> {
  const row = await client.one<{ viewer_system_account_id: string }>(`
    SELECT viewer_system_account_id
    FROM ${dirty}
    WHERE account_id = ?
  `, [accountId])
  assert.equal(row?.viewer_system_account_id, viewerSystemAccountId, `${label}必须在同一 PostgreSQL 事务写入正确 scope 的脏标记`)
}

async function verifyDirtyReadFailsClosed(client: DatabaseClient): Promise<void> {
  const targetAccountId = `${accountPrefix}000001`
  await markAccountListAvailabilityDirtyInClient(client, {
    accountId: targetAccountId,
    reason: 'stress_failure_boundary'
  })
  await assert.rejects(
    () => listAccountListAvailabilityProjectionPageInClient(client, {
      viewerSystemAccountId,
      options: { page: 1, pageSize }
    }),
    AccountListAvailabilityProjectionUnavailableError,
    'dirty 投影必须 fail-closed，不能返回旧快照'
  )
  const dirty = table(client, 'account_list_availability_dirty')
  await client.execute(`DELETE FROM ${dirty} WHERE account_id = ?`, [targetAccountId])
}

async function warmPostgresPool(client: DatabaseClient, concurrency: number): Promise<void> {
  const connectionCount = Math.min(concurrency, runtimeConfig.postgres.poolMax)
  await Promise.all(Array.from({ length: connectionCount }, () => client.query('SELECT 1')))
}

async function runConcurrentPressure(
  client: DatabaseClient,
  durationMs: number,
  concurrency: number,
  pool: PoolMetricsSource,
  poolMetrics: PoolMetrics
): Promise<LatencySummary[]> {
  const samples = new Map<string, number[]>()
  const querySamples = new Map<string, number[]>()
  const postQuerySamples = new Map<string, number[]>()
  const errors = new Map<string, number>()
  for (const testCase of cases) {
    samples.set(testCase.name, [])
    querySamples.set(testCase.name, [])
    postQuerySamples.set(testCase.name, [])
    errors.set(testCase.name, 0)
  }
  const deadline = Date.now() + durationMs
  await Promise.all(Array.from({ length: concurrency }, (_, workerIndex) => (async () => {
    let index = workerIndex % cases.length
    while (Date.now() < deadline) {
      const testCase = cases[index % cases.length]!
      index += 1
      const startedAt = performance.now()
      let queryMs = 0
      const timedClient: DatabaseClient = {
        ...client,
        async query<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T[]> {
          const queryStartedAt = performance.now()
          try {
            return await client.query<T>(sql, params)
          } finally {
            queryMs += performance.now() - queryStartedAt
          }
        }
      }
      try {
        poolMetrics.maxTotal = Math.max(poolMetrics.maxTotal, Number(pool.totalCount ?? 0))
        poolMetrics.maxIdle = Math.max(poolMetrics.maxIdle, Number(pool.idleCount ?? 0))
        poolMetrics.maxWaiting = Math.max(poolMetrics.maxWaiting, Number(pool.waitingCount ?? 0))
        await listAccountListAvailabilityProjectionPageInClient(timedClient, {
          viewerSystemAccountId,
          options: testCase.options,
          includeDynamicOverlays: testCase.includeDynamicOverlays
        })
      } catch {
        errors.set(testCase.name, errors.get(testCase.name)! + 1)
      } finally {
        const totalMs = performance.now() - startedAt
        samples.get(testCase.name)!.push(totalMs)
        querySamples.get(testCase.name)!.push(queryMs)
        postQuerySamples.get(testCase.name)!.push(Math.max(0, totalMs - queryMs))
      }
    }
  })()))
  return cases.map((testCase) => {
    const values = samples.get(testCase.name)!.sort((left, right) => left - right)
    const queryValues = querySamples.get(testCase.name)!.sort((left, right) => left - right)
    const postQueryValues = postQuerySamples.get(testCase.name)!.sort((left, right) => left - right)
    return {
      name: testCase.name,
      count: values.length,
      p50Ms: percentile(values, 0.5),
      p95Ms: percentile(values, 0.95),
      p99Ms: percentile(values, 0.99),
      queryP95Ms: percentile(queryValues, 0.95),
      postQueryP95Ms: percentile(postQueryValues, 0.95),
      maxMs: values.at(-1) ?? 0,
      errors: errors.get(testCase.name)!
    }
  })
}

async function cleanupStressFixture(client: DatabaseClient): Promise<void> {
  const tables = [
    'account_list_availability_projection_tags',
    'account_list_availability_projection_search_terms',
    'account_list_availability_dirty',
    'account_list_availability_projection_index',
    'account_list_availability_projections',
    'account_name_search_terms',
    'account_name_search_documents',
    'accounts'
  ]
  await setProjectionCleanupTriggersEnabled(client, false)
  try {
    await client.transaction(async (tx) => {
      // This is restricted to the isolated fixture prefix. It must not let
      // normal statement timeouts turn cleanup into a lingering load source.
      await tx.execute("SET LOCAL statement_timeout = '120s'")
      for (const tableName of tables) {
        await tx.execute(`DELETE FROM ${table(tx, tableName)} WHERE ${tableName === 'accounts' ? 'id LIKE ?' : 'account_id LIKE ?'}`, [`${accountPrefix}%`])
      }
      await tx.execute(`DELETE FROM ${table(tx, 'account_api_key_runtime_states')} WHERE account_id LIKE ?`, [`${accountPrefix}%`])
      await tx.execute(`DELETE FROM ${table(tx, 'account_tags')} WHERE system_account_id = ?`, [viewerSystemAccountId])
      await tx.execute(`DELETE FROM ${table(tx, 'system_accounts')} WHERE id = ?`, [viewerSystemAccountId])
    })
  } finally {
    await setProjectionCleanupTriggersEnabled(client, true)
  }
}

const projectionCleanupTriggerTargets = [
  ['account_list_availability_projections', 'account_list_availability_projection_delete_health'],
  ['account_tag_bindings', 'account_list_availability_tag_bindings'],
  ['account_name_search_documents', 'account_list_availability_name_search_documents'],
  ['account_name_search_terms', 'account_list_availability_name_search_terms']
] as const

async function setProjectionCleanupTriggersEnabled(client: DatabaseClient, enabled: boolean): Promise<void> {
  for (const [tableName, triggerName] of projectionCleanupTriggerTargets) {
    await client.execute(`
      ALTER TABLE ${table(client, tableName)} ${enabled ? 'ENABLE' : 'DISABLE'} TRIGGER ${triggerName}
    `)
  }
}

function assertScratchDatabase(): void {
  if (runtimeConfig.databaseDriver !== 'postgres') throw new Error('压测必须在 PostgreSQL 模式运行')
  const databaseName = scratchDatabaseName()
  if (!/^juhe_ai_sub2api_dev_[a-z0-9_]{3,80}$/.test(databaseName)) {
    throw new Error(`拒绝压测非临时数据库：${databaseName}`)
  }
  if (!runtimeConfig.redis.namespace.includes('account_list_projection_20260810')) {
    throw new Error(`拒绝使用非本次压测 Redis namespace：${runtimeConfig.redis.namespace}`)
  }
}

function scratchDatabaseName(): string {
  const postgresUrl = runtimeConfig.postgres.url
  if (!postgresUrl) throw new Error('压测缺少 JUHE_AI_POSTGRES_URL')
  return new URL(postgresUrl).pathname.replace(/^\//, '')
}

function table(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
}

function percentile(values: number[], quantile: number): number {
  if (!values.length) return 0
  const index = Math.min(values.length - 1, Math.ceil(values.length * quantile) - 1)
  return Number(values[index]!.toFixed(3))
}

function assertPressureAcceptance(summaries: LatencySummary[], concurrency: number): void {
  const p95LimitMs = concurrency >= 80 ? 700 : 300
  const p99LimitMs = concurrency >= 80 ? 900 : 500
  for (const summary of summaries) {
    assert(summary.count > 0, `筛选 ${summary.name} 压测必须产生样本`)
    assert.equal(summary.errors, 0, `筛选 ${summary.name} 压测不得返回 unavailable 或 SQL 错误`)
    assert(summary.p95Ms <= p95LimitMs, `筛选 ${summary.name} P95 必须 <= ${p95LimitMs}ms，实际 ${summary.p95Ms}ms`)
    assert(summary.p99Ms <= p99LimitMs, `筛选 ${summary.name} P99 必须 <= ${p99LimitMs}ms，实际 ${summary.p99Ms}ms`)
  }
}

function postgresExplainExecutionMs(rows: Array<Record<string, unknown>>): number {
  const match = /"Execution Time":([0-9.]+)/.exec(JSON.stringify(rows))
  const value = Number(match?.[1])
  if (!Number.isFinite(value) || value < 0) throw new Error('PostgreSQL EXPLAIN 未返回 Execution Time')
  return value
}

function postgresExplainHotNodes(rows: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
  const root = rows[0]?.['QUERY PLAN']
  const nodes: Array<Record<string, unknown>> = []
  const visit = (value: unknown): void => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return
    const record = value as Record<string, unknown>
    const actualTotalTime = Number(record['Actual Total Time'])
    if (Number.isFinite(actualTotalTime) && actualTotalTime >= 5) {
      nodes.push({
        node: record['Node Type'],
        relation: record['Relation Name'],
        index: record['Index Name'],
        subplan: record['Subplan Name'],
        cte: record['CTE Name'],
        startupMs: record['Actual Startup Time'],
        totalMs: record['Actual Total Time'],
        rows: record['Actual Rows'],
        loops: record['Actual Loops'],
        filter: record.Filter
      })
    }
    const children = record.Plans
    if (Array.isArray(children)) children.forEach(visit)
    visit(record.Plan)
  }
  if (Array.isArray(root)) root.forEach(visit)
  return nodes
}

function postgresExplainJit(rows: Array<Record<string, unknown>>): unknown {
  const root = rows[0]?.['QUERY PLAN']
  if (!Array.isArray(root)) return undefined
  const first = root[0]
  return first && typeof first === 'object' && !Array.isArray(first)
    ? (first as Record<string, unknown>).JIT
    : undefined
}

function integerArg(name: string, fallback: number, min: number, max: number): number {
  const index = process.argv.indexOf(name)
  const raw = index >= 0 ? process.argv[index + 1] : undefined
  const value = raw === undefined ? fallback : Number(raw)
  if (!Number.isInteger(value) || value < min || value > max) throw new Error(`${name} 必须是 ${min}-${max} 的整数`)
  return value
}

await main().catch(async (error) => {
  console.error(error instanceof Error ? (error.stack ?? error.message) : String(error))
  await closePostgresPool()
  process.exitCode = 1
})
// See the matching shadow regression: this script must not leave runtime
// client handles alive after its fixture cleanup has completed.
process.exit(process.exitCode ?? 0)
