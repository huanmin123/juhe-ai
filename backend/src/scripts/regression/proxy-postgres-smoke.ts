import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { currentSystemAccountId, type AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createProxyAsync,
  deleteProxyAsync,
  findProxyAsync,
  getProxyTestConfigAsync,
  listEnabledProxyTestConfigsAsync,
  listProxiesPageAsync,
  listProxyOptionsAsync,
  resolveProxyUrlForProfileAsync,
  updateProxyAsync,
  updateProxyTestStateAsync
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

  const testConfig = await getProxyTestConfigAsync(created.id)
  assert.equal(testConfig?.proxyUrl, 'http://proxy-user:%20p%40ss%20@127.0.0.1:18080', 'PG 代理测试配置应能解密并保留密码空格')

  const resolvedUrl = await resolveProxyUrlForProfileAsync(created.id)
  assert.equal(resolvedUrl, testConfig?.proxyUrl, 'PG 代理 URL 解析应与测试配置一致')

  const tested = await updateProxyTestStateAsync(created.id, {
    testStatus: 'passed',
    latencyMs: 12,
    outboundIp: '203.0.113.10',
    outboundRegion: '测试地区',
    lastTestMessage: 'PG smoke 检测通过'
  })
  assert.equal(tested?.testStatus, 'passed', 'PG updateProxyTestStateAsync 应更新检测状态')
  assert.equal(tested?.latencyMs, 12, 'PG updateProxyTestStateAsync 应保留检测延迟')

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
    testStateChecked: true,
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
      WHERE enabled = 1
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

async function cleanupSmokeRows(): Promise<void> {
  if (createdProxyIds.length === 0) return
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.proxy_profiles WHERE id = ANY($1::text[])', [createdProxyIds])
}
