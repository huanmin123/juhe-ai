import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-client-ip-stats-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 2
runtimeConfig.secret = 'client-ip-stats-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, clientIpStats, usageStatsHelpers, statsSchema] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/client-ip-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/schema/stats-schema.js')
])

try {
  const createdAtBase = Date.now() - 60_000
  const today = usageStatsHelpers.dateKey(new Date(createdAtBase), usageStatsHelpers.usageStatsTimezone())
  const emptyWindowBeforeBuild = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10 })
  assert.equal(emptyWindowBeforeBuild.rangeReady, false, '空 IP 窗口未刷新前应标记为未就绪')
  clientIpStats.rebuildClientIpUsageRangeWindows()
  const emptyWindowAfterBuild = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10 })
  assert.equal(emptyWindowAfterBuild.rangeReady, true, '空 IP 窗口完成刷新后应返回 ready 空列表')
  assert.equal(emptyWindowAfterBuild.items.length, 0, '空 IP 窗口不应伪造任何汇总行')

  repositories.createUsageRecordsBatch([
    {
      id: 'client_ip_stats_ipv4_success',
      traceId: 'trace-client-ip-stats-ipv4-success',
      systemAccountId: 'sys_admin',
      clientIp: '203.0.113.10',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      durationMs: 120,
      firstTokenMs: 30,
      inputTokens: 100,
      outputTokens: 20,
      cacheReadTokens: 10,
      cacheReadCostUsd: 0.0001,
      costUsd: 0.001,
      createdAt: new Date(createdAtBase).toISOString()
    },
    {
      id: 'client_ip_stats_ipv4_error',
      traceId: 'trace-client-ip-stats-ipv4-error',
      systemAccountId: 'sys_admin',
      clientIp: '203.0.113.10',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 429,
      success: false,
      durationMs: 200,
      firstTokenMs: 50,
      inputTokens: 40,
      outputTokens: 0,
      costUsd: 0.0004,
      errorCode: 'rate_limit',
      createdAt: new Date(createdAtBase + 1).toISOString()
    },
    {
      id: 'client_ip_stats_ipv4_secondary_a',
      traceId: 'trace-client-ip-stats-ipv4-secondary-a',
      systemAccountId: 'sys_admin',
      clientIp: '198.51.100.25',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      inputTokens: 7,
      outputTokens: 8,
      costUsd: 0.0002,
      createdAt: new Date(createdAtBase + 2).toISOString()
    },
    {
      id: 'client_ip_stats_ipv4_secondary_b',
      traceId: 'trace-client-ip-stats-ipv4-secondary-b',
      systemAccountId: 'sys_admin',
      clientIp: '198.51.100.25',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      inputTokens: 9,
      outputTokens: 10,
      costUsd: 0.0003,
      createdAt: new Date(createdAtBase + 3).toISOString()
    },
    {
      id: 'client_ip_stats_non_ipv4_ignored',
      traceId: 'trace-client-ip-stats-non-ipv4-ignored',
      systemAccountId: 'sys_admin',
      clientIp: 'localhost',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      inputTokens: 99,
      outputTokens: 99,
      costUsd: 0.99,
      createdAt: new Date(createdAtBase + 4).toISOString()
    },
    {
      id: 'client_ip_stats_v6_loopback_ignored',
      traceId: 'trace-client-ip-stats-v6-loopback-ignored',
      systemAccountId: 'sys_admin',
      clientIp: '::1',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      inputTokens: 88,
      outputTokens: 88,
      costUsd: 0.88,
      createdAt: new Date(createdAtBase + 5).toISOString()
    },
    {
      id: 'client_ip_stats_cooldown_ignored',
      traceId: 'trace-client-ip-stats-cooldown-ignored',
      trafficSource: 'cooldown_retest',
      systemAccountId: 'sys_admin',
      clientIp: '203.0.113.99',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 503,
      success: false,
      inputTokens: 999,
      outputTokens: 999,
      costUsd: 9,
      createdAt: new Date(createdAtBase + 6).toISOString()
    },
    {
      id: 'client_ip_stats_missing_ip_cursor',
      traceId: 'trace-client-ip-stats-missing-ip-cursor',
      systemAccountId: 'sys_admin',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      inputTokens: 500,
      outputTokens: 500,
      costUsd: 1,
      createdAt: new Date(createdAtBase + 7).toISOString()
    }
  ])

  assert.equal(clientIpStats.aggregateClientIpStatsBatch(100), 7, 'IP 统计应扫描非 cooldown 使用记录并跳过无 IP 和非 IPv4 行')

  const ipv4Identity = clientIpStats.normalizeClientIpForStats('203.0.113.10')
  const secondaryIpv4Identity = clientIpStats.normalizeClientIpForStats('198.51.100.25')
  assert(ipv4Identity, 'IPv4 应可规范化')
  assert(secondaryIpv4Identity, '第二个 IPv4 来源应可规范化')
  assert.equal(clientIpStats.normalizeClientIpForStats('not-an-ip'), undefined, '非 IPv4 来源不参与 IP 管理')
  assert.equal(clientIpStats.normalizeClientIpForStats('::1'), undefined, '非 IPv4 回环地址不应折算进 IPv4 汇总')
  assert.equal(clientIpStats.pendingClientIpRangeWindowDirtyCountForTest(), 2, 'IP 聚合后应只标记变更 IP 等待增量刷新窗口')
  clientIpStats.clearClientIpRangeWindowDirtyMemoryForTest()

  const listBeforeWindow = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10 })
  assert.equal(listBeforeWindow.rangeReady, false, '列表不应在请求内同步重建范围窗口')
  assert.equal(listBeforeWindow.pageUpperBound, 0, '范围窗口未生成时列表应返回空结果等待后台刷新')

  clientIpStats.refreshClientIpUsageRangeWindows({ dirtyLimit: 1 })
  assert.equal(clientIpStats.pendingClientIpRangeWindowDirtyCountForTest(), 1, '部分增量窗口刷新后应保留未处理 dirty IP')
  const partialWindowList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10 })
  assert.equal(partialWindowList.rangeReady, false, 'dirty IP 未全部刷新前窗口不能提前标记 ready')
  assert.equal(partialWindowList.pageUpperBound, 0, 'dirty IP 未全部刷新前列表不应返回部分窗口结果')
  clientIpStats.refreshClientIpUsageRangeWindows()
  assert.equal(clientIpStats.pendingClientIpRangeWindowDirtyCountForTest(), 0, '增量窗口刷新后 dirty IP 集合应清空')

  const list = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10, sortField: 'requestCount', sortOrder: 'desc' })
  assert.equal(list.rangeReady, true, '后台增量刷新后列表应标记当前窗口可用')
  assert.equal(list.pageUpperBound, 2, '列表 pageUpperBound 应使用当前页分页上界，不依赖范围总聚合')
  assert.equal(list.items.length, 2, '列表应只返回有 IP 聚合事实的来源')

  const statsDatabase = databaseModule.getStatsDatabase()
  const policyColumns = statsDatabase.prepare('PRAGMA table_info(client_ip_policies)').all() as Array<{ name?: string }>
  assert.equal(policyColumns.some((column) => column.name === 'policy_type'), false, 'IP 封禁策略表不应保留已废弃的 policy_type 字段')
  assertClientIpPolicySchemaMigration(statsSchema.applyStatsSchema)

  const ipv4Row = list.items.find((item) => item.ipHash === ipv4Identity.ipHash)
  assert(ipv4Row, 'IPv4 聚合行应存在')
  assert.equal(ipv4Row.rangeUsage.requestCount, 2, '同一 IPv4 在当前范围内应合并请求数')
  assert.equal(ipv4Row.rangeUsage.errorCount, 1, 'IPv4 失败数应累计')
  assert.equal(ipv4Row.rangeUsage.inputTokens, 140, 'IPv4 输入 token 应累计')
  assert.equal(ipv4Row.rangeUsage.totalTokens, 160, 'IPv4 总 token 应累计')
  assert.equal(ipv4Row.rangeUsage.averageFirstTokenMs, 40, 'IPv4 平均首 token 应来自预聚合窗口')
  assert.equal(ipv4Row.rangeUsage.averageDurationMs, 160, 'IPv4 平均总耗时应来自预聚合窗口')
  assert.equal(ipv4Row.rangeUsage.maxDurationMs, 200, 'IPv4 最大总耗时应来自预聚合窗口')

  const secondaryIpv4Row = list.items.find((item) => item.ipHash === secondaryIpv4Identity.ipHash)
  assert(secondaryIpv4Row, '第二个 IPv4 聚合行应存在')
  assert.equal(secondaryIpv4Row.aggregateIpKey, '198.51.100.25', 'IPv4 列表应展示规范化 IP')
  assert.equal(secondaryIpv4Row.rangeUsage.requestCount, 2, '同一 IPv4 来源在当前范围内应合并')

  repositories.createUsageRecordsBatch([
    {
      id: 'client_ip_stats_ipv4_late_success',
      traceId: 'trace-client-ip-stats-ipv4-late-success',
      systemAccountId: 'sys_admin',
      clientIp: '203.0.113.10',
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      durationMs: 400,
      firstTokenMs: 100,
      inputTokens: 1,
      outputTokens: 2,
      costUsd: 0.0001,
      createdAt: new Date(createdAtBase + 20).toISOString()
    }
  ])
  assert.equal(clientIpStats.aggregateClientIpStatsBatch(100), 1, '新 IP 用量进入 daily 后应只标记窗口过期，不在请求内刷新')
  const staleList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10 })
  assert.equal(staleList.rangeReady, false, '旧窗口已有行时，新数据仍应让范围窗口进入未就绪状态')
  assert.equal(staleList.pageUpperBound, 0, '过期窗口不应继续返回旧分页上界')
  clientIpStats.refreshClientIpUsageRangeWindows()
  const refreshedList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10, sortField: 'requestCount', sortOrder: 'desc' })
  const refreshedIpv4Row = refreshedList.items.find((item) => item.ipHash === ipv4Identity.ipHash)
  assert(refreshedIpv4Row, '窗口重新刷新后 IPv4 行应恢复可见')
  assert.equal(refreshedIpv4Row.rangeUsage.requestCount, 3, '窗口重新刷新后应包含新增 IP 用量')
  assert.equal(refreshedIpv4Row.rangeUsage.maxDurationMs, 400, '窗口重新刷新后最大总耗时应更新')

  const ipKeywordList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, keyword: '203.0.113', pageSize: 10 })
  assert.equal(ipKeywordList.pageUpperBound, 1, 'IP 管理搜索应支持按 IP 前缀命中')
  const hashKeywordList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, keyword: ipv4Identity.ipHash, pageSize: 10 })
  assert.equal(hashKeywordList.pageUpperBound, 0, 'IP 管理搜索不应支持按 hash 命中')

  const policy = clientIpStats.createClientIpPolicy({
    ipHash: ipv4Identity.ipHash,
    reason: 'regression',
    actorSystemAccountId: 'sys_admin'
  })
  assert.equal(clientIpStats.listActiveClientIpPolicies().some((item) => item.id === policy.id), true, 'active 封禁策略应进入运行态列表')
  assert.equal(
    clientIpStats.recordClientIpPolicyHits([{ ipHash: ipv4Identity.ipHash, policyId: policy.id, hitCount: 3, hitAt: new Date(createdAtBase + 10).toISOString() }]).recorded,
    1,
    '封禁命中计数应可后台累计'
  )
  assert.equal(clientIpStats.disableClientIpPolicies({
    ipHash: ipv4Identity.ipHash,
    reason: 'regression unblock',
    actorSystemAccountId: 'sys_admin'
  }).disabledCount, 1, '解封应停用 active 封禁策略')
  assert.equal(clientIpStats.listActiveClientIpPolicies().some((item) => item.id === policy.id), false, '解封后策略不应继续进入运行态列表')

  const cursorCount = statsDatabase
    .prepare("SELECT COUNT(*) AS total FROM stats_job_state WHERE job_name = 'client_ip_stats_aggregation' AND scope_type = 'usage_shard' AND cursor_id IS NOT NULL")
    .get() as { total?: number } | undefined
  assert(Number(cursorCount?.total ?? 0) > 0, 'IP 统计应维护独立 usage shard 游标')

  console.log('IP 统计回归通过：IPv4 注册、非 IPv4 忽略、预聚合窗口、封禁策略和命中计数均符合预期')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertClientIpPolicySchemaMigration(applyStatsSchema: (database: DatabaseSync) => void): void {
  const database = new DatabaseSync(':memory:')
  try {
    database.exec(`
      CREATE TABLE client_ip_policies (
        id TEXT PRIMARY KEY,
        ip_hash TEXT NOT NULL,
        policy_type TEXT NOT NULL,
        status TEXT NOT NULL,
        reason TEXT,
        expires_at TEXT,
        created_by_system_account_id TEXT NOT NULL,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        disabled_at TEXT,
        disabled_by_system_account_id TEXT,
        disabled_reason TEXT
      );

      CREATE INDEX idx_client_ip_policies_active ON client_ip_policies(status, policy_type, ip_hash, expires_at);
      CREATE INDEX idx_client_ip_policies_ip ON client_ip_policies(ip_hash, status, policy_type, created_at DESC);

      INSERT INTO client_ip_policies (
        id, ip_hash, policy_type, status, reason, expires_at,
        created_by_system_account_id, created_at, updated_at
      ) VALUES
        ('legacy_blacklist', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'blacklist', 'active', 'legacy blacklist', NULL, 'sys_admin', '2026-05-29T00:00:00.000Z', '2026-05-29T00:00:00.000Z'),
        ('legacy_watch', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'watch', 'active', 'legacy watch', NULL, 'sys_admin', '2026-05-29T00:00:00.000Z', '2026-05-29T00:00:00.000Z');
    `)
    applyStatsSchema(database)
    const columns = database.prepare('PRAGMA table_info(client_ip_policies)').all() as Array<{ name?: string }>
    assert.equal(columns.some((column) => column.name === 'policy_type'), false, '旧库迁移后不应保留 policy_type 字段')
    const policies = (database.prepare('SELECT id, reason FROM client_ip_policies ORDER BY id').all() as Array<{ id?: string; reason?: string }>)
      .map((row) => ({ id: row.id, reason: row.reason }))
    assert.deepEqual(policies, [{ id: 'legacy_blacklist', reason: 'legacy blacklist' }], '旧库迁移只保留有效封禁策略')
    const indexes = database.prepare("SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'client_ip_policies' AND sql IS NOT NULL").all() as Array<{ sql?: string }>
    assert.equal(indexes.some((index) => index.sql?.includes('policy_type')), false, '旧库迁移后策略索引不应引用 policy_type')
  } finally {
    database.close()
  }
}
