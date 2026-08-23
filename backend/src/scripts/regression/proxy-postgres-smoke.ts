import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { currentSystemAccountId, type AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool, postgresApplicationName } from '../../storage/postgres-client.js'
import {
  createProxyAsync,
  deleteProxyAsync,
  findProxyAsync,
  getProxyTestConfigAsync,
  listEnabledProxyTestConfigsAsync,
  listProxiesPageAsync,
  listProxyOptionsAsync,
  patchProxyForManagementAsync,
  ProxyProfileUpdateConflictError,
  resolveProxyUrlForProfileAsync,
  updateProxyAsync,
} from '../../storage/proxy.repository.js'
import { closeRedisClients } from '../../shared/redis-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '代理 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `proxy_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdProxyIds: string[] = []

try {
  assert.equal(currentSystemAccountId(access), 'sys_admin', '代理 PG smoke 需要管理员访问上下文')

  const created = await createProxyAsync({
    name: `代理 PG smoke ${marker}`,
    description: 'proxy postgres smoke',
    type: 'http',
    host: '127.0.0.1',
    port: 18_080,
    enabled: true
  }, access)
  createdProxyIds.push(created.id)

  const listed = await listProxiesPageAsync({ page: 1, pageSize: 20, keyword: `代理 PG smoke ${marker}` })
  assert.ok(listed.items.some((proxy) => proxy.id === created.id), 'PG 代理列表应返回刚创建的代理')
  const listedProxy = listed.items.find((proxy) => proxy.id === created.id)
  assert(listedProxy?.updatedAt, 'PG 代理列表必须携带 CAS 版本')
  assert.match(listedProxy.updatedAt, /\.\d{6}Z$/, 'PG 列表版本必须保留数据库微秒精度')

  const narrowPatch = await patchProxyForManagementAsync(created.id, {
    description: 'proxy postgres narrow patch'
  }, listedProxy.updatedAt)
  assert.deepEqual(narrowPatch?.mutation.values, { description: 'proxy postgres narrow patch' }, 'PG 字段级 PATCH 只返回真实变化字段')
  const narrowVersion = narrowPatch?.mutation.updatedAt ?? ''
  const narrowNoOp = await patchProxyForManagementAsync(created.id, {
    description: 'proxy postgres narrow patch'
  }, narrowVersion)
  assert.deepEqual(narrowNoOp?.mutation, { id: created.id, updatedAt: narrowVersion, changed: false, values: {} }, 'PG 同值 PATCH 必须零写入并保留版本')
  await assert.rejects(
    () => patchProxyForManagementAsync(created.id, { description: 'stale overwrite' }, listedProxy.updatedAt),
    (error: unknown) => error instanceof ProxyProfileUpdateConflictError,
    'PG 陈旧版本 PATCH 必须返回 CAS 冲突'
  )

  const options = await listProxyOptionsAsync({ keyword: `代理 PG smoke ${marker}` })
  assert.ok(options.some((proxy) => proxy.id === created.id), 'PG 代理 options 应返回刚创建的启用代理')

  const found = await findProxyAsync(created.id)
  assert.equal(found?.name, created.name, 'PG findProxyAsync 应按 ID 读取代理')

  const updated = await updateProxyAsync(created.id, {
    description: 'proxy postgres smoke updated',
    username: 'proxy-user',
    password: ' p@ss ',
    enabled: true
  })
  assert.equal(updated?.description, 'proxy postgres smoke updated', 'PG updateProxyAsync 应返回更新后的代理')

  await setProxyConfigRevisionWithMicroseconds(created.id)
  const testConfig = await getProxyTestConfigAsync(created.id)
  assert.equal(testConfig?.proxyUrl, 'http://proxy-user:%20p%40ss%20@127.0.0.1:18080', 'PG 代理测试配置应能解密并保留密码空格')
  assert.ok(testConfig?.configUpdatedAt, 'PG 代理测试配置应携带内部配置 revision')
  assert.match(testConfig?.configUpdatedAt ?? '', /\.123456(?:Z|[+-]00(?::?00)?)$/, 'PG 代理配置 revision 必须保留数据库微秒精度')

  const resolvedUrl = await resolveProxyUrlForProfileAsync(created.id)
  assert.equal(resolvedUrl, testConfig?.proxyUrl, 'PG 代理 URL 解析应与测试配置一致')

  const microsecondSummary = await findProxyAsync(created.id)
  assert.match(microsecondSummary?.updatedAt ?? '', /\.123456Z$/, 'PG 管理摘要不得把微秒版本截断成毫秒')
  const microsecondPatch = await patchProxyForManagementAsync(created.id, {
    description: 'proxy postgres microsecond CAS patch'
  }, microsecondSummary?.updatedAt ?? '')
  assert.equal(microsecondPatch?.mutation.changed, true, 'PG 六位微秒版本必须能直接完成字段级 CAS')

  await assertConcurrentDisjointProxyUpdatesAreMerged(created.id, marker)
  const configAfterConcurrentUpdate = await getProxyTestConfigAsync(created.id)
  assert.notEqual(configAfterConcurrentUpdate?.configUpdatedAt, testConfig?.configUpdatedAt, 'PG 管理更新必须推进代理配置 revision')
  const enabledConfigs = await listEnabledProxyTestConfigsAsync(50)
  assert.ok(enabledConfigs.some((proxy) => proxy.id === created.id), 'PG 启用代理检测候选应包含刚创建的代理')
  await assertLatencyRefreshCandidateExplainUsesIndex()

  const deleted = await deleteProxyAsync(created.id)
  assert.equal(deleted, true, 'PG deleteProxyAsync 应删除未被账号引用的代理')
  const afterDelete = await findProxyAsync(created.id)
  assert.equal(afterDelete, undefined, 'PG deleteProxyAsync 后按 ID 应查不到代理')

  console.log(JSON.stringify({
    message: '代理 PG smoke 通过',
    createdProxyId: created.id,
    listChecked: true,
    optionChecked: true,
    testStateChecked: false,
    microsecondRevisionChecked: true,
    concurrentUpdateChecked: true,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}
async function assertLatencyRefreshCandidateExplainUsesIndex(): Promise<void> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL enable_seqscan = off')
    const planRows = await client.query(`
      EXPLAIN (COSTS OFF)
      SELECT id
      FROM juhe_business.proxy_profiles
      WHERE enabled = true
      ORDER BY (last_tested_at IS NOT NULL) ASC, last_tested_at ASC, updated_at DESC, id ASC
      LIMIT 20
    `)
    const plan = planRows.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(plan, /idx_proxy_profiles_latency_refresh_due/, 'PG 代理延迟刷新候选查询应命中 due 索引')
    assert.doesNotMatch(plan, /\bSeq Scan\b/, 'PG 代理延迟刷新候选查询不应出现 Seq Scan')
  } finally {
    await client.query('ROLLBACK').catch(() => undefined)
    client.release()
  }
}

async function setProxyConfigRevisionWithMicroseconds(proxyId: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.proxy_profiles
    SET updated_at = TIMESTAMPTZ '2026-07-25T03:12:34.123456Z'
    WHERE id = $1
  `, [proxyId])
}

async function assertConcurrentDisjointProxyUpdatesAreMerged(proxyId: string, markerValue: string): Promise<void> {
  const pool = await getPostgresPool()
  assert.ok(runtimeConfig.postgres.poolMax >= 2, 'PG 并发更新测试需要至少两条应用连接')
  await Promise.all([
    pool.query('SELECT pg_sleep(0.05)'),
    pool.query('SELECT pg_sleep(0.05)')
  ])
  const { Client } = await import('pg')
  const blocker = new Client({ connectionString: runtimeConfig.postgres.url })
  await blocker.connect()
  let transactionOpen = false
  let updateError: unknown
  let updatesSettled = false
  let updates: Promise<unknown[]> | undefined
  const nextName = `代理 PG 并发 ${markerValue}`
  try {
    await blocker.query('BEGIN')
    transactionOpen = true
    await blocker.query('SELECT id FROM juhe_business.proxy_profiles WHERE id = $1 FOR UPDATE', [proxyId])

    updates = Promise.all([
      updateProxyAsync(proxyId, { description: 'proxy postgres concurrent description' }),
      updateProxyAsync(proxyId, { name: nextName })
    ]).then(
      (result) => {
        updatesSettled = true
        return result
      },
      (error: unknown) => {
        updatesSettled = true
        updateError = error
        return []
      }
    )

    await waitForBlockedProxyMutations(blocker, () => {
      if (updateError) return updateError
      return updatesSettled ? new Error('两条代理更新在阻塞事务释放前意外结束') : undefined
    })
    await blocker.query('COMMIT')
    transactionOpen = false
    await updates
    if (updateError) throw updateError

    const finalProxy = await findProxyAsync(proxyId)
    assert.equal(finalProxy?.name, nextName, 'PG 并发更新后不应丢失名称写入')
    assert.equal(finalProxy?.description, 'proxy postgres concurrent description', 'PG 并发更新后不应丢失描述写入')
  } finally {
    if (transactionOpen) {
      await blocker.query('ROLLBACK').catch(() => undefined)
    }
    await updates
    await blocker.end()
  }
}

async function waitForBlockedProxyMutations(
  client: { query: (text: string, values?: unknown[]) => Promise<{ rows: unknown[] }> },
  getUpdateError: () => unknown
): Promise<void> {
  const deadlineAt = Date.now() + 1_500
  while (Date.now() < deadlineAt) {
    const updateError = getUpdateError()
    if (updateError) throw updateError
    const result = await client.query(`
      SELECT COUNT(*) AS blocked_count
      FROM pg_stat_activity
      WHERE datname = current_database()
        AND pid <> pg_backend_pid()
        AND application_name = $1
        AND wait_event_type = 'Lock'
    `, [postgresApplicationName()])
    const row = result.rows[0] as { blocked_count?: number | string } | undefined
    // A pool may expose one worker connection while the second mutation waits
    // for pool checkout. One database-side waiter plus unsettled promises is
    // sufficient to prove the row-lock interleaving without making the smoke
    // depend on pool scheduling details.
    if (Number(row?.blocked_count ?? 0) >= 1) return
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
  const snapshot = await client.query(`
    SELECT pid, wait_event_type, wait_event, pg_blocking_pids(pid) AS blocking, state, left(query, 160) AS query
    FROM pg_stat_activity
    WHERE datname = current_database() AND pid <> pg_backend_pid()
  `)
  throw new Error(`等待代理并发更新进入 PostgreSQL 行锁队列超时: ${JSON.stringify(snapshot.rows)}`)
}

async function cleanupSmokeRows(): Promise<void> {
  if (createdProxyIds.length === 0) return
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.proxy_profiles WHERE id = ANY($1::text[])', [createdProxyIds])
}
