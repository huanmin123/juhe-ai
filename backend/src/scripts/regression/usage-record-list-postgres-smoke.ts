import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { getUsageRecordDetailAsync, listUsageRecordsAsync } from '../../storage/repositories.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createUsageRecordsBatchAsync } from '../../storage/usage-records.repository.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '使用记录列表 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `usage_record_list_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const createdAtBase = Date.now() - 60_000
const usageIds = [
  `usage_${marker}_success`,
  `usage_${marker}_failed`,
  `usage_${marker}_other`
]
const tracePrefix = `trace_${marker}`
const model = `model-${marker}`
const clientIpPrefix = '198.18.204.'
const primaryClientIp = `${clientIpPrefix}10`
const pool = await getPostgresPool()

try {
  await createUsageRecordsBatchAsync([
    {
      id: usageIds[0],
      traceId: `${tracePrefix}_success`,
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      clientIp: primaryClientIp,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model,
      statusCode: 200,
      success: true,
      durationMs: 120,
      firstTokenMs: 30,
      inputTokens: 12,
      outputTokens: 8,
      costUsd: 0.001,
      createdAt: new Date(createdAtBase).toISOString()
    },
    {
      id: usageIds[1],
      traceId: `${tracePrefix}_failed`,
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      clientIp: `${clientIpPrefix}11`,
      endpoint: '/v1/chat/completions',
      providerCode: 'gpt',
      model,
      statusCode: 429,
      success: false,
      failureAttribution: 'account_upstream',
      durationMs: 260,
      firstTokenMs: 55,
      inputTokens: 30,
      outputTokens: 0,
      errorCode: 'rate_limit',
      createdAt: new Date(createdAtBase + 1).toISOString()
    },
    {
      id: usageIds[2],
      traceId: `${tracePrefix}_other`,
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      clientIp: '198.18.205.1',
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: `${model}-other`,
      statusCode: 200,
      success: true,
      durationMs: 80,
      firstTokenMs: 20,
      inputTokens: 3,
      outputTokens: 4,
      createdAt: new Date(createdAtBase + 2).toISOString()
    }
  ])
  const writeCounts = await readSmokeWriteCounts()
  assert.equal(writeCounts.records, usageIds.length, 'PG 使用记录 smoke 应写入 usage_records 主表')
  assert.equal(writeCounts.entries, usageIds.length, 'PG 使用记录 smoke 应写入 usage_record_shard_entries catalog')

  const traceList = await listUsageRecordsAsync(undefined, {
    traceId: tracePrefix,
    page: 1,
    pageSize: 10
  })
  assert.deepEqual(traceList.items.map((item) => item.id), [usageIds[2], usageIds[1], usageIds[0]], 'PG 使用记录列表应按 trace 前缀读取 catalog 窗口')

  const failedList = await listUsageRecordsAsync(undefined, {
    model,
    result: 'failed',
    sortBy: 'durationMs',
    sortOrder: 'desc',
    page: 1,
    pageSize: 10
  })
  assert.deepEqual(failedList.items.map((item) => item.id), [usageIds[1]], 'PG 使用记录列表应支持 model + failed + duration 排序筛选')

  const clientIpList = await listUsageRecordsAsync(undefined, {
    clientIp: clientIpPrefix,
    page: 1,
    pageSize: 10
  })
  assert.deepEqual(clientIpList.items.map((item) => item.id), [usageIds[1], usageIds[0]], 'PG 使用记录列表应支持客户端 IP 前缀筛选')

  const detail = await getUsageRecordDetailAsync(usageIds[0])
  assert(detail, 'PG 使用记录详情应按 ID 读取')
  assert.equal(detail.traceId, `${tracePrefix}_success`, 'PG 使用记录详情 traceId 应正确')
  assert.equal(detail.inputTokens, 12, 'PG 使用记录详情 token 应正确')

  await assertUsageRecordExplainPlans(tracePrefix, model, clientIpPrefix)

  console.log(JSON.stringify({
    message: '使用记录列表 PG smoke 通过',
    records: writeCounts.records,
    entries: writeCounts.entries,
    traceItems: traceList.items.length,
    failedItems: failedList.items.length,
    clientIpItems: clientIpList.items.length,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function readSmokeWriteCounts(): Promise<{ records: number; entries: number }> {
  const result = await pool.query(`
    SELECT
      (SELECT COUNT(*) FROM juhe_usage.usage_records WHERE id = ANY($1::text[])) AS records,
      (SELECT COUNT(*) FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY($1::text[])) AS entries
  `, [usageIds])
  return {
    records: Number(result.rows[0]?.records ?? 0),
    entries: Number(result.rows[0]?.entries ?? 0)
  }
}

async function assertUsageRecordExplainPlans(traceId: string, targetModel: string, clientIp: string): Promise<void> {
  await assertIndexedPlan(
    '使用记录 trace 前缀 PG 查询',
    `
      SELECT usage_id
      FROM juhe_usage.usage_record_shard_entries
      WHERE trace_id >= $1 AND trace_id < $2
      ORDER BY created_at DESC, usage_id DESC
      LIMIT 11
    `,
    [traceId, usageRecordTextPrefixUpperBound(traceId)],
    ['idx_usage_record_shard_entries_trace_created_sort']
  )
  await assertIndexedPlan(
    '使用记录 model 筛选 PG 查询',
    `
      SELECT usage_id
      FROM juhe_usage.usage_record_shard_entries
      WHERE model = $1
      ORDER BY created_at DESC, usage_id DESC
      LIMIT 11
    `,
    [targetModel],
    ['idx_usage_record_shard_entries_model_created_sort']
  )
  await assertIndexedPlan(
    '使用记录 client IP 前缀 PG 查询',
    `
      SELECT usage_id
      FROM juhe_usage.usage_record_shard_entries
      WHERE client_ip >= $1 AND client_ip < $2
      ORDER BY created_at DESC, usage_id DESC
      LIMIT 11
    `,
    [clientIp, usageRecordTextPrefixUpperBound(clientIp)],
    ['idx_usage_record_shard_entries_client_ip_created_sort']
  )
  await assertIndexedPlan(
    '使用记录耗时排序 PG 查询',
    `
      SELECT usage_id
      FROM juhe_usage.usage_record_shard_entries
      ORDER BY duration_ms DESC, created_at DESC, usage_id DESC
      LIMIT 11
    `,
    [],
    ['idx_usage_record_shard_entries_duration_sort']
  )
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

function usageRecordTextPrefixUpperBound(value: string): string {
  const comparable = /[.:]$/.test(value) ? value.slice(0, -1) : value
  for (let index = comparable.length - 1; index >= 0; index -= 1) {
    const code = comparable.charCodeAt(index)
    if (code < 0xffff) {
      return `${comparable.slice(0, index)}${String.fromCharCode(code + 1)}`
    }
  }
  return `${value}\uffff`
}

async function cleanupSmokeRows(): Promise<void> {
  await pool.query('DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY($1::text[])', [usageIds])
  await pool.query('DELETE FROM juhe_usage.usage_records WHERE id = ANY($1::text[])', [usageIds])
}
