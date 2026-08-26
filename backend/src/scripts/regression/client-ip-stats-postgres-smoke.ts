import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import {
  aggregateClientIpStatsBatchAsync,
  createClientIpPolicyAsync,
  disableClientIpPoliciesAsync,
  findActiveClientIpPolicyByHashAsync,
  getClientIpStatsDetailAsync,
  listActiveClientIpPoliciesAsync,
  listClientIpStatsAsync,
  normalizeClientIpForStats,
  recordClientIpPolicyHitsAsync,
  refreshClientIpUsageRangeWindowsAsync
} from '../../storage/client-ip-stats.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createUsageRecordsBatchAsync } from '../../storage/usage-records.repository.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '客户端 IP 统计 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
assertRemotePostgresSmokeAllowed()

const marker = `client_ip_stats_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const primaryIp = '198.18.203.10'
const secondaryIp = '198.18.203.11'
const primaryIdentity = normalizeClientIpForStats(primaryIp)
const secondaryIdentity = normalizeClientIpForStats(secondaryIp)
assert(primaryIdentity, 'PG smoke IPv4 should normalize')
assert(secondaryIdentity, 'PG smoke secondary IPv4 should normalize')

const usageIds = [
  `usage_${marker}_primary_success`,
  `usage_${marker}_primary_error`,
  `usage_${marker}_secondary_success`
]
const traceIds = usageIds.map((id) => `trace_${id}`)
const accountIds = [`acc_${marker}_primary`, `acc_${marker}_fallback`]
const ipHashes = [primaryIdentity.ipHash, secondaryIdentity.ipHash]
const policyIds: string[] = []
const createdAtBase = Date.now() - 60_000
const cursorBeforeSmoke = new Date(createdAtBase - 1).toISOString()

const pool = await getPostgresPool()
const savedJobStates = await captureClientIpJobStates()

try {
  await setClientIpStatsCursor(cursorBeforeSmoke, '')
  await seedSmokeAccountRows()
  await createUsageRecordsBatchAsync([
    {
      id: usageIds[0],
      traceId: traceIds[0],
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      accountId: accountIds[0],
      clientIp: primaryIp,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
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
      id: usageIds[1],
      traceId: traceIds[1],
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      accountId: accountIds[0],
      clientIp: primaryIp,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.1',
      statusCode: 429,
      success: false,
      failureAttribution: 'account_upstream',
      durationMs: 240,
      firstTokenMs: 50,
      inputTokens: 40,
      outputTokens: 0,
      costUsd: 0.0004,
      errorCode: 'rate_limit',
      createdAt: new Date(createdAtBase + 1).toISOString()
    },
    {
      id: usageIds[2],
      traceId: traceIds[2],
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      accountId: accountIds[1],
      clientIp: secondaryIp,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.1',
      statusCode: 200,
      success: true,
      durationMs: 80,
      firstTokenMs: 20,
      inputTokens: 7,
      outputTokens: 8,
      costUsd: 0.0002,
      createdAt: new Date(createdAtBase + 2).toISOString()
    }
  ])

  const processed = await aggregateClientIpStatsBatchAsync(20)
  assert(processed >= usageIds.length, `PG IP 聚合应消费 smoke 使用记录，processed=${processed}`)

  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(createdAtBase), timezone)
  const beforeWindow = await listClientIpStatsAsync({ startDate: today, endDate: today, pageSize: 10 })
  assert.equal(beforeWindow.rangeReady, false, 'PG IP 聚合写入 daily 后，窗口刷新前应保持未就绪')

  await refreshClientIpUsageRangeWindowsAsync()
  const list = await listClientIpStatsAsync({
    startDate: today,
    endDate: today,
    pageSize: 10,
    keyword: '198.18.203',
    sortField: 'requestCount',
    sortOrder: 'desc'
  })
  assert.equal(list.rangeReady, true, 'PG IP 窗口刷新后列表应 ready')
  const primary = list.items.find((item) => item.ipHash === primaryIdentity.ipHash)
  assert(primary, 'PG IP 列表应包含 primary IP')
  assert.equal(primary.rangeUsage.requestCount, 2, 'PG IP 列表应累计同 IP 请求数')
  assert.equal(primary.rangeUsage.errorCount, 1, 'PG IP 列表应累计错误数')
  assert.equal(primary.rangeUsage.totalTokens, 160, 'PG IP 列表应累计 token')
  assert.equal(primary.rangeUsage.maxDurationMs, 240, 'PG IP 列表应保留最大耗时')

  const detail = await getClientIpStatsDetailAsync({
    ipHash: primaryIdentity.ipHash,
    startDate: today,
    endDate: today,
    pageSize: 10,
    sortField: 'requestCount',
    sortOrder: 'desc'
  })
  assert(detail, 'PG IP 详情应返回注册表信息')
  assert.equal(detail.rangeReady, true, 'PG IP 详情窗口应 ready')
  assert.deepEqual(detail.items.map((item) => item.accountId), [accountIds[0]], 'PG IP 详情应按 IP+账号窗口聚合')
  assert.equal(detail.items[0]?.accountName, `PG IP 详情主账号 ${marker}`, 'PG IP 详情应批量补齐账号名称')
  assert.equal(detail.items[0]?.accountOwnerSystemAccountId, 'sys_admin', 'PG IP 详情应返回账号所属系统账户 ID')
  assert.equal(detail.items[0]?.accountOwnerSystemAccountName, '超级管理员', 'PG IP 详情应返回账号所属用户名称')
  assert.equal(detail.items[0]?.rangeUsage.requestCount, 2, 'PG IP 详情应累计账号请求数')
  assert.equal(detail.items[0]?.rangeUsage.errorCount, 1, 'PG IP 详情应累计账号错误数')

  await assertClientIpStatsExplainPlans(today, primaryIdentity.ipHash, '198.18.203')

  const policy = await createClientIpPolicyAsync({
    ipHash: primaryIdentity.ipHash,
    reason: `PG smoke ${marker}`,
    actorSystemAccountId: 'sys_admin'
  })
  policyIds.push(policy.id)
  assert.equal(policy.status, 'active', 'PG IP 封禁策略创建后应为 active')
  assert.equal((await listActiveClientIpPoliciesAsync()).some((item) => item.id === policy.id), true, 'PG active IP 封禁策略应进入快照列表')
  assert.equal((await findActiveClientIpPolicyByHashAsync(primaryIdentity.ipHash))?.id, policy.id, 'PG active IP 封禁策略应支持按 hash 精确读取')
  const hits = await recordClientIpPolicyHitsAsync([{
    ipHash: primaryIdentity.ipHash,
    policyId: policy.id,
    hitCount: 3,
    hitAt: new Date(createdAtBase + 30).toISOString()
  }])
  assert.equal(hits.recorded, 1, 'PG IP 封禁命中应批量写入')
  const hitCount = await readPolicyHitCount(policy.id)
  assert.equal(hitCount, 3, 'PG IP 封禁命中计数应累计到 client_ip_policy_hits')
  const disabled = await disableClientIpPoliciesAsync({
    ipHash: primaryIdentity.ipHash,
    reason: 'PG smoke unblock',
    actorSystemAccountId: 'sys_admin'
  })
  assert.equal(disabled.disabledCount, 1, 'PG IP 封禁策略应可解除')
  assert.equal(await findActiveClientIpPolicyByHashAsync(primaryIdentity.ipHash), undefined, 'PG IP 封禁解除后 active 精确读取应为空')

  console.log(JSON.stringify({
    message: '客户端 IP 统计 PG smoke 通过',
    processed,
    listed: list.items.length,
    detailItems: detail.items.length,
    explainIndexed: true,
    policyHits: hitCount
  }))
} finally {
  await cleanupSmokeRows()
  await restoreClientIpJobStates(savedJobStates)
  await closeRedisClients()
  await closePostgresPool()
}

async function assertClientIpStatsExplainPlans(today: string, ipHash: string, keyword: string): Promise<void> {
  const keywordUpperBound = clientIpKeywordPrefixUpperBound(keyword)
  await assertIndexedPlan(
    '客户端 IP 搜索前缀 PG 查询',
    `
      SELECT ip_hash
      FROM juhe_stats.client_ip_registry
      WHERE (aggregate_ip_key >= $1 AND aggregate_ip_key < $2)
        OR (client_ip >= $1 AND client_ip < $2)
      LIMIT 11
    `,
    [keyword, keywordUpperBound],
    ['idx_client_ip_registry_ip', 'idx_client_ip_registry_client_ip']
  )
  await assertIndexedPlan(
    '客户端 IP 列表请求数排序 PG 查询',
    `
      SELECT ip_hash
      FROM juhe_stats.client_ip_usage_range_windows
      WHERE start_date = $1
        AND end_date = $2
      ORDER BY request_count DESC, ip_hash ASC
      LIMIT 11
    `,
    [today, today],
    ['idx_client_ip_range_requests']
  )
  await assertIndexedPlan(
    '客户端 IP 详情账号排序 PG 查询',
    `
      SELECT account_id
      FROM juhe_stats.client_ip_account_usage_range_windows
      WHERE ip_hash = $1
        AND start_date = $2
        AND end_date = $3
      ORDER BY request_count DESC, account_id ASC
      LIMIT 11
    `,
    [ipHash, today, today],
    ['idx_client_ip_account_range_requests']
  )
  await assertIndexedPlan(
    '客户端 IP 封禁策略筛选 PG 查询',
    `
      SELECT id
      FROM juhe_stats.client_ip_policies
      WHERE status = 'active'
        AND ip_hash = $1
        AND (expires_at IS NULL OR expires_at > $2)
      LIMIT 1
    `,
    [ipHash, new Date().toISOString()],
    ['idx_client_ip_policies_active', 'idx_client_ip_policies_ip']
  )
}

function clientIpKeywordPrefixUpperBound(value: string): string {
  for (let index = value.length - 1; index >= 0; index -= 1) {
    const code = value.charCodeAt(index)
    if (code < 0xffff) {
      return `${value.slice(0, index)}${String.fromCharCode(code + 1)}`
    }
  }
  return `${value}\uffff`
}

async function assertIndexedPlan(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<void> {
  const connection = await pool.connect()
  try {
    await connection.query('BEGIN')
    await connection.query('SET LOCAL enable_seqscan = off')
    const planResult = await connection.query(`EXPLAIN (COSTS OFF) ${sql}`, params)
    await connection.query('ROLLBACK')
    const plan = planResult.rows
      .map((row: Record<string, unknown>) => String(row['QUERY PLAN'] ?? ''))
      .filter(Boolean)
      .join('\n')
    assert(!/\bSeq Scan\b/i.test(plan), `${label} 不应退化为 Seq Scan，实际计划：${plan}`)
    assert(
      expectedIndexes.some((indexName) => plan.includes(indexName)),
      `${label} 应命中索引 ${expectedIndexes.join(' / ')}，实际计划：${plan}`
    )
  } catch (error) {
    await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
  }
}

async function captureClientIpJobStates(): Promise<JobStateRow[]> {
  const result = await pool.query(`
    SELECT scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at
    FROM juhe_stats.stats_job_state
    WHERE (scope_type = 'global' AND scope_id = '' AND job_name = 'client_ip_stats_aggregation')
      OR (scope_type = 'client_ip_range_window' AND job_name = 'client_ip_range_window_refresh')
  `)
  return result.rows as unknown as JobStateRow[]
}

async function setClientIpStatsCursor(cursorCreatedAt: string, cursorId: string): Promise<void> {
  await pool.query(`
    INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at)
    VALUES ('global', '', 'client_ip_stats_aggregation', $1, $2, NULL, $3)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = EXCLUDED.cursor_created_at,
      cursor_id = EXCLUDED.cursor_id,
      last_success_at = NULL,
      last_error_message = NULL,
      updated_at = EXCLUDED.updated_at
  `, [cursorCreatedAt, cursorId, new Date().toISOString()])
}

async function cleanupSmokeRows(): Promise<void> {
  await pool.query('DELETE FROM juhe_usage.usage_records WHERE id = ANY($1::text[])', [usageIds])
  await pool.query('DELETE FROM juhe_stats.client_ip_policy_hits WHERE policy_id = ANY($1::text[]) OR ip_hash = ANY($2::text[])', [policyIds, ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_policies WHERE id = ANY($1::text[]) OR ip_hash = ANY($2::text[])', [policyIds, ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_usage_range_windows WHERE ip_hash = ANY($1::text[])', [ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_account_usage_range_windows WHERE ip_hash = ANY($1::text[])', [ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_stats_daily WHERE ip_hash = ANY($1::text[])', [ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_account_stats_daily WHERE ip_hash = ANY($1::text[])', [ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_range_window_dirty_ips WHERE ip_hash = ANY($1::text[])', [ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_account_range_window_dirty_ips WHERE ip_hash = ANY($1::text[])', [ipHashes])
  await pool.query('DELETE FROM juhe_stats.client_ip_registry WHERE ip_hash = ANY($1::text[])', [ipHashes])
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
}

async function seedSmokeAccountRows(): Promise<void> {
  for (const [index, accountId] of accountIds.entries()) {
    await pool.query(`
      INSERT INTO juhe_business.accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, status, credentials_encrypted, credential_mask, health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES ($1, 'sys_admin', 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', $2, 'api_key', 'active', $3, 'sk-client-ip-stats', 'gpt-5.4-mini', 'responses_sse', $4, $4)
      ON CONFLICT (id) DO NOTHING
    `, [
      accountId,
      `${index === 0 ? 'PG IP 详情主账号' : 'PG IP 详情备用账号'} ${marker}`,
      `client-ip-stats-smoke:${accountId}`,
      new Date(createdAtBase - 1000).toISOString()
    ])
  }
}

async function readPolicyHitCount(policyId: string): Promise<number> {
  const result = await pool.query(`
    SELECT COALESCE(SUM(hit_count), 0) AS total
    FROM juhe_stats.client_ip_policy_hits
    WHERE policy_id = $1
  `, [policyId])
  return Number(result.rows[0]?.total ?? 0)
}

async function restoreClientIpJobStates(rows: JobStateRow[]): Promise<void> {
  await pool.query(`
    DELETE FROM juhe_stats.stats_job_state
    WHERE (scope_type = 'global' AND scope_id = '' AND job_name = 'client_ip_stats_aggregation')
      OR (scope_type = 'client_ip_range_window' AND job_name = 'client_ip_range_window_refresh')
  `)
  for (const row of rows) {
    await pool.query(`
      INSERT INTO juhe_stats.stats_job_state (
        scope_type, scope_id, job_name, cursor_created_at, cursor_id,
        last_success_at, last_error_message, lag_seconds, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
    `, [
      row.scope_type,
      row.scope_id,
      row.job_name,
      row.cursor_created_at,
      row.cursor_id,
      row.last_success_at,
      row.last_error_message,
      row.lag_seconds,
      row.updated_at
    ])
  }
}

interface JobStateRow {
  scope_type: string
  scope_id: string
  job_name: string
  cursor_created_at: string | null
  cursor_id: string | null
  last_success_at: string | null
  last_error_message: string | null
  lag_seconds: number | null
  updated_at: string
}

function assertRemotePostgresSmokeAllowed(): void {
  if (process.env.JUHE_AI_ALLOW_CLIENT_IP_STATS_POSTGRES_SMOKE === '1') {
    return
  }
  const postgresUrl = runtimeConfig.postgres.url
  let hostname = ''
  try {
    hostname = postgresUrl ? new URL(postgresUrl).hostname.toLowerCase() : ''
  } catch {
    hostname = ''
  }
  if (hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1') {
    return
  }
  throw new Error('客户端 IP 统计 PG smoke 会临时改写 juhe_stats.stats_job_state 全局游标并写入 smoke 使用记录；非本机 PostgreSQL 必须先确认是测试实例，并设置 JUHE_AI_ALLOW_CLIENT_IP_STATS_POSTGRES_SMOKE=1')
}
