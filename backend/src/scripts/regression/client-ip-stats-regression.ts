import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

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

const [databaseModule, repositories, clientIpStats, usageStatsHelpers, clientIpPolicyCache] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/client-ip-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../modules/gateway/client-ip-policy-cache.service.js')
])

try {
  assertGatewayPolicyLookupDoesNotRideRuntimeSnapshot()
  assertIpStatsViewSeparatesUsageWindowAndLastUsedFilter()
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
      trafficSource: 'gateway',
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
      trafficSource: 'gateway',
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
      trafficSource: 'gateway',
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
      trafficSource: 'gateway',
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
      trafficSource: 'gateway',
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
      trafficSource: 'gateway',
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
      trafficSource: 'gateway',
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
  const policyColumns = new Set(
    (statsDatabase.prepare('PRAGMA table_info(client_ip_policies)').all() as Array<{ name?: string }>)
      .map((column) => column.name)
      .filter((name): name is string => Boolean(name))
  )
  for (const column of [
    'id',
    'ip_hash',
    'status',
    'reason',
    'expires_at',
    'created_by_system_account_id',
    'created_at',
    'updated_at',
    'disabled_at',
    'disabled_by_system_account_id',
    'disabled_reason'
  ]) {
    assert(policyColumns.has(column), `IP 封禁策略表应包含当前字段 ${column}`)
  }

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

  const previousDay = usageStatsHelpers.dateKey(new Date(createdAtBase - 24 * 60 * 60 * 1000), usageStatsHelpers.usageStatsTimezone())
  const secondaryOriginalLastSeenAt = new Date(createdAtBase + 3).toISOString()
  statsDatabase.prepare('UPDATE client_ip_registry SET last_seen_at = ? WHERE ip_hash = ?')
    .run(new Date(createdAtBase - 24 * 60 * 60 * 1000).toISOString(), secondaryIpv4Identity.ipHash)
  const lastUsedTodayList = clientIpStats.listClientIpStats({
    startDate: today,
    endDate: today,
    lastUsedStartDate: today,
    lastUsedEndDate: today,
    pageSize: 10,
    sortField: 'requestCount',
    sortOrder: 'desc'
  })
  assert.equal(lastUsedTodayList.items.some((item) => item.ipHash === ipv4Identity.ipHash), true, 'IP 管理最后使用筛选应保留全局最后使用在今天的 IP')
  assert.equal(lastUsedTodayList.items.some((item) => item.ipHash === secondaryIpv4Identity.ipHash), false, 'IP 管理最后使用筛选不应只看窗口内 last_used_at')
  const lastUsedPreviousDayList = clientIpStats.listClientIpStats({
    startDate: today,
    endDate: today,
    lastUsedStartDate: previousDay,
    lastUsedEndDate: previousDay,
    pageSize: 10
  })
  assert.deepEqual(lastUsedPreviousDayList.items.map((item) => item.ipHash), [secondaryIpv4Identity.ipHash], 'IP 管理最后使用筛选应按注册表全局 last_seen_at 命中')
  const rangeLastUsedSortList = clientIpStats.listClientIpStats({
    startDate: today,
    endDate: today,
    pageSize: 10,
    sortField: 'lastUsedAt',
    sortOrder: 'desc'
  })
  assert.deepEqual(
    rangeLastUsedSortList.items.map((item) => item.ipHash),
    [secondaryIpv4Identity.ipHash, ipv4Identity.ipHash],
    '默认 lastUsedAt 排序应保持窗口内最近使用语义，避免影响公开 IP 用量接口'
  )
  const globalLastUsedSortList = clientIpStats.listClientIpStats({
    startDate: today,
    endDate: today,
    pageSize: 10,
    sortField: 'lastUsedAt',
    sortOrder: 'desc',
    lastUsedSortScope: 'global'
  })
  assert.deepEqual(
    globalLastUsedSortList.items.map((item) => item.ipHash),
    [ipv4Identity.ipHash, secondaryIpv4Identity.ipHash],
    'IP 管理 lastUsedAt 排序应使用注册表全局 last_seen_at，与页面展示一致'
  )
  statsDatabase.prepare('UPDATE client_ip_registry SET last_seen_at = ? WHERE ip_hash = ?')
    .run(secondaryOriginalLastSeenAt, secondaryIpv4Identity.ipHash)

  repositories.createUsageRecordsBatch([
    {
      id: 'client_ip_stats_ipv4_late_success',
      traceId: 'trace-client-ip-stats-ipv4-late-success',
      trafficSource: 'gateway',
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
  assert.equal(staleList.rangeReady, false, '已发布窗口存在时，新数据仍应让范围窗口进入未就绪状态')
  assert.equal(staleList.pageUpperBound, 0, '过期窗口不应继续返回已发布分页上界')
  clientIpStats.refreshClientIpUsageRangeWindows()
  const refreshedList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10, sortField: 'requestCount', sortOrder: 'desc' })
  const refreshedIpv4Row = refreshedList.items.find((item) => item.ipHash === ipv4Identity.ipHash)
  assert(refreshedIpv4Row, '窗口重新刷新后 IPv4 行应恢复可见')
  assert.equal(refreshedIpv4Row.rangeUsage.requestCount, 3, '窗口重新刷新后应包含新增 IP 用量')
  assert.equal(refreshedIpv4Row.rangeUsage.maxDurationMs, 400, '窗口重新刷新后最大总耗时应更新')

  statsDatabase.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
    VALUES ('client_ip_range_window', ?, 'client_ip_range_window_refresh', NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      last_success_at = NULL,
      updated_at = excluded.updated_at
  `).run(`${today}:${today}`, new Date().toISOString())
  statsDatabase.prepare('DELETE FROM client_ip_range_window_dirty_ips').run()
  clientIpStats.clearClientIpRangeWindowDirtyMemoryForTest()
  const staleWithoutDirtyList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10 })
  assert.equal(staleWithoutDirtyList.rangeReady, false, '窗口 stale 但 dirty 为空时列表应继续标记未就绪')
  clientIpStats.refreshClientIpUsageRangeWindows()
  const selfHealedList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, pageSize: 10, sortField: 'requestCount', sortOrder: 'desc' })
  const selfHealedIpv4Row = selfHealedList.items.find((item) => item.ipHash === ipv4Identity.ipHash)
  assert.equal(selfHealedList.rangeReady, true, '窗口 stale 且 dirty 为空时后台刷新应完整重建并恢复 ready')
  assert(selfHealedIpv4Row, '自愈重建后 IPv4 行应继续可见')
  assert.equal(selfHealedIpv4Row.rangeUsage.requestCount, 3, '自愈重建后不应丢失已聚合用量')

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
  assert.equal(clientIpStats.findActiveClientIpPolicyByHash(ipv4Identity.ipHash)?.id, policy.id, '运行态封禁检查应能按 ip_hash 精确读取 active 策略')
  assertClientIpPolicyLookupQueryPlan(ipv4Identity.ipHash)
  clientIpPolicyCache.clearClientIpPolicyCacheLocal()
  assert.equal((await clientIpPolicyCache.inspectClientIpPolicy(ipv4Identity.clientIp, { cacheOnly: true })).blocked, false, '来源级缓存未命中时不应在前置 cacheOnly 检查里查库')
  const loadedPolicyDecision = await clientIpPolicyCache.inspectClientIpPolicy(ipv4Identity.clientIp)
  assert.equal(loadedPolicyDecision.blacklistPolicy?.id, policy.id, '来源级缓存未命中后应按当前 IP 精确查询封禁策略')
  assert.equal((await clientIpPolicyCache.inspectClientIpPolicy(ipv4Identity.clientIp, { cacheOnly: true })).blacklistPolicy?.id, policy.id, '精确查询后应写入来源级短 TTL 缓存')
  const blacklistedList = clientIpStats.listClientIpStats({ startDate: today, endDate: today, status: 'blacklisted', pageSize: 10 })
  assert.deepEqual(blacklistedList.items.map((item) => item.ipHash), [ipv4Identity.ipHash], 'IP 列表封禁筛选应只返回 active 封禁 IP')
  assertClientIpListPolicyQueryPlan(today)
  assertClientIpListSortQueryPlans(today)
  assertClientIpListGlobalLastUsedSortQueryPlan(today)
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

function assertClientIpListPolicyQueryPlan(today: string): void {
  const policyNow = new Date().toISOString()
  const details = explainStatsQuery(`
    SELECT registry.ip_hash
    FROM client_ip_usage_range_windows range_stats
    INNER JOIN client_ip_registry registry ON registry.ip_hash = range_stats.ip_hash
    WHERE range_stats.start_date = ?
      AND range_stats.end_date = ?
      AND EXISTS (
        SELECT 1
        FROM client_ip_policies active_policies
        WHERE active_policies.status = 'active'
          AND active_policies.ip_hash = registry.ip_hash
          AND (active_policies.expires_at IS NULL OR active_policies.expires_at > ?)
        LIMIT 1
      )
    ORDER BY range_stats.request_count DESC, range_stats.ip_hash ASC
    LIMIT ? OFFSET ?
  `, [today, today, policyNow, 11, 0])
  assert(details.includes('idx_client_ip_range_requests'), `IP 列表应通过范围窗口排序索引读取当前页，实际计划：${details}`)
  assert(details.includes('idx_client_ip_policies_active'), `IP 封禁筛选应按 ip_hash 命中策略索引，实际计划：${details}`)
  assert(!details.includes('MATERIALIZE'), `IP 列表不应 materialize 封禁策略全集，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR GROUP BY'), `IP 列表不应为封禁策略做临时 GROUP BY，实际计划：${details}`)
}

function assertClientIpListSortQueryPlans(today: string): void {
  const sortIndexes = new Map([
    ['requestCount', 'idx_client_ip_range_requests'],
    ['successCount', 'idx_client_ip_range_success'],
    ['errorCount', 'idx_client_ip_range_errors'],
    ['errorRate', 'idx_client_ip_range_error_rate'],
    ['totalTokens', 'idx_client_ip_range_total_tokens'],
    ['totalCost', 'idx_client_ip_range_cost'],
    ['activeDays', 'idx_client_ip_range_active_days'],
    ['lastUsedAt', 'idx_client_ip_range_last_used']
  ])
  for (const [sortField, indexName] of sortIndexes) {
    const orderBy = clientIpListOrderByForPlan(sortField)
    const details = explainStatsQuery(`
      SELECT registry.ip_hash
      FROM client_ip_usage_range_windows range_stats
      INNER JOIN client_ip_registry registry ON registry.ip_hash = range_stats.ip_hash
      WHERE range_stats.start_date = ?
        AND range_stats.end_date = ?
      ORDER BY ${orderBy}, range_stats.ip_hash ASC
      LIMIT ? OFFSET ?
    `, [today, today, 11, 0])
    assert(details.includes(indexName), `${sortField} 排序应使用 ${indexName}，实际计划：${details}`)
    assert(!/USE TEMP B-TREE/i.test(details), `${sortField} 排序不应创建临时排序树，实际计划：${details}`)
  }
}

function assertClientIpListGlobalLastUsedSortQueryPlan(today: string): void {
  const descDetails = explainStatsQuery(`
    SELECT registry.ip_hash
    FROM client_ip_registry registry INDEXED BY idx_client_ip_registry_last_seen
    INNER JOIN client_ip_usage_range_windows range_stats ON registry.ip_hash = range_stats.ip_hash
    WHERE range_stats.start_date = ?
      AND range_stats.end_date = ?
    ORDER BY registry.last_seen_at DESC, registry.ip_hash ASC
    LIMIT ? OFFSET ?
  `, [today, today, 11, 0])
  assert(descDetails.includes('idx_client_ip_registry_last_seen'), `IP 管理全局最后使用降序应使用注册表最近使用索引，实际计划：${descDetails}`)
  assert(!/USE TEMP B-TREE/i.test(descDetails), `IP 管理全局最后使用降序不应创建临时排序树，实际计划：${descDetails}`)

  const ascDetails = explainStatsQuery(`
    SELECT registry.ip_hash
    FROM client_ip_registry registry INDEXED BY idx_client_ip_registry_last_seen
    INNER JOIN client_ip_usage_range_windows range_stats ON registry.ip_hash = range_stats.ip_hash
    WHERE range_stats.start_date = ?
      AND range_stats.end_date = ?
    ORDER BY registry.last_seen_at ASC, registry.ip_hash DESC
    LIMIT ? OFFSET ?
  `, [today, today, 11, 0])
  assert(ascDetails.includes('idx_client_ip_registry_last_seen'), `IP 管理全局最后使用升序应反向使用注册表最近使用索引，实际计划：${ascDetails}`)
  assert(!/USE TEMP B-TREE/i.test(ascDetails), `IP 管理全局最后使用升序不应创建临时排序树，实际计划：${ascDetails}`)
}

function clientIpListOrderByForPlan(sortField: string): string {
  switch (sortField) {
    case 'successCount':
      return 'range_stats.success_count DESC'
    case 'errorCount':
      return 'range_stats.error_count DESC'
    case 'errorRate':
      return 'CASE WHEN range_stats.request_count > 0 THEN CAST(range_stats.error_count AS REAL) / range_stats.request_count ELSE 0 END DESC'
    case 'totalTokens':
      return '(range_stats.input_tokens + range_stats.output_tokens) DESC'
    case 'totalCost':
      return 'range_stats.total_cost_usd DESC'
    case 'activeDays':
      return 'range_stats.active_days DESC'
    case 'lastUsedAt':
      return 'range_stats.last_used_at DESC'
    case 'requestCount':
    default:
      return 'range_stats.request_count DESC'
  }
}

function assertIpStatsViewSeparatesUsageWindowAndLastUsedFilter(): void {
  const source = readFileSync(resolve('..', 'frontend', 'src', 'views', 'ip-stats', 'IpStatsView.vue'), 'utf8')
  const buildListParamsSource = sourceFunctionBlock(source, 'function buildListParams')
  assert(buildListParamsSource.includes('const usageRange = usageWindowDateRange(usageWindow.value)'), 'IP 管理页面应使用固定用量窗口构造统计范围')
  assert(buildListParamsSource.includes('startDate: formatDateKey(usageRange[0])'), 'IP 管理 startDate 应来自用量统计窗口')
  assert(buildListParamsSource.includes('endDate: formatDateKey(usageRange[1])'), 'IP 管理 endDate 应来自用量统计窗口')
  assert(buildListParamsSource.includes('lastUsedStartDate: formatDateKey(lastUsedDateRange.value[0])'), 'IP 管理最后使用开始日期应独立提交')
  assert(buildListParamsSource.includes('lastUsedEndDate: formatDateKey(lastUsedDateRange.value[1])'), 'IP 管理最后使用结束日期应独立提交')
  assert(!buildListParamsSource.includes('startDate: formatDateKey(lastUsedDateRange.value[0])'), 'IP 管理 startDate 不能直接绑定最后使用日期')
  assert(!buildListParamsSource.includes('endDate: formatDateKey(lastUsedDateRange.value[1])'), 'IP 管理 endDate 不能直接绑定最后使用日期')
}

function assertClientIpPolicyLookupQueryPlan(ipHash: string): void {
  const policyNow = new Date().toISOString()
  const details = explainStatsQuery(`
    SELECT policies.id
    FROM client_ip_policies policies
    INNER JOIN client_ip_registry registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.ip_hash = ?
      AND policies.status = 'active'
      AND (policies.expires_at IS NULL OR policies.expires_at > ?)
    ORDER BY policies.created_at DESC, policies.id DESC
    LIMIT 1
  `, [ipHash, policyNow])
  assert(/idx_client_ip_policies_(active|ip)/.test(details), `IP 封禁运行态查询应按 ip_hash 命中策略索引，实际计划：${details}`)
  assert(!/SCAN (client_ip_policies|policies)\b/.test(details), `IP 封禁运行态查询不应扫描策略表，实际计划：${details}`)
}

function assertGatewayPolicyLookupDoesNotRideRuntimeSnapshot(): void {
  const handlersSource = readFileSync(new URL('../../modules/db-service/db-service-handlers.ts', import.meta.url), 'utf8')
  const readRuntimeBody = sourceFunctionBlock(handlersSource, 'function readGatewayRuntime')
  assert(!readRuntimeBody.includes('listActiveClientIpPolicies'), '网关 runtime 读取不能携带全量 active IP 封禁策略')
  assert(!readRuntimeBody.includes('clientIpPolicies'), '网关 runtime 响应不能携带全量 IP 封禁策略数组')
  const cacheSource = readFileSync(new URL('../../modules/gateway/client-ip-policy-cache.service.ts', import.meta.url), 'utf8')
  assert(cacheSource.includes("type: 'find_active_client_ip_policy'"), '网关 IP 封禁缓存未命中时应使用按 ip_hash 精确查询')
  assert(!cacheSource.includes("type: 'list_active_client_ip_policies'"), '网关 IP 封禁请求路径不能加载全量 active IP 封禁策略')
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  const nextFunction = source.indexOf('\nfunction ', start + marker.length)
  return source.slice(start, nextFunction === -1 ? undefined : nextFunction)
}

function explainStatsQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getStatsDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}
