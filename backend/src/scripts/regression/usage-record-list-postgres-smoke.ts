import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { getUsageRecordDetailAsync, listUsageRecordsAsync } from '../../storage/repositories.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createUsageRecordsBatchAsync } from '../../storage/usage-records.repository.js'
import {
  estimateCatalogCacheReadCostUsdAsync,
  estimateCatalogCostUsdAsync,
  removeCustomProviderModelAsync,
  saveCustomProviderModelAsync
} from '../../modules/model-pricing/model-catalog.service.js'
import { withCostBreakdownAsync } from '../../modules/usage-records/usage-records.routes.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '使用记录列表 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `usage_record_list_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const createdAtBase = Date.now() - 60_000
const usageIds = [
  `usage_${marker}_success`,
  `usage_${marker}_failed`,
  `usage_${marker}_other`,
  `usage_${marker}_priced`,
  `usage_${marker}_audio_image`
]
const tracePrefix = `trace_${marker}`
const model = `model-${marker}`
const pricedModel = 'gpt-5.5'
const audioImagePricingModelId = `custom_model_${marker}`
const audioImagePricingModel = `gpt-regression-audio-image-${marker}`
const clientIpPrefix = '198.18.204.'
const primaryClientIp = `${clientIpPrefix}10`
const pool = await getPostgresPool()
const smokeAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  await saveCustomProviderModelAsync({
    id: audioImagePricingModelId,
    providerCode: 'gpt',
    model: audioImagePricingModel,
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    audioInputUsdPer1M: 4,
    audioOutputUsdPer1M: 12,
    outputUsdPerImage: 0.04,
    actorSystemAccountId: 'sys_admin'
  })
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
    },
    {
      id: usageIds[3],
      traceId: `${tracePrefix}_priced`,
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      clientIp: '198.18.206.1',
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: pricedModel,
      statusCode: 200,
      success: true,
      durationMs: 90,
      firstTokenMs: 24,
      inputTokens: 1_000_000,
      outputTokens: 1_000_000,
      cacheReadTokens: 100_000,
      createdAt: new Date(createdAtBase + 3).toISOString()
    },
    {
      id: usageIds[4],
      traceId: `${tracePrefix}_audio_image`,
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      clientIp: '198.18.206.2',
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: audioImagePricingModel,
      statusCode: 200,
      success: true,
      durationMs: 95,
      firstTokenMs: 26,
      inputAudioTokens: 1_000_000,
      outputAudioTokens: 1_000_000,
      outputImageCount: 2,
      createdAt: new Date(createdAtBase + 4).toISOString()
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
  assert.deepEqual(traceList.items.map((item) => item.id), [usageIds[4], usageIds[3], usageIds[2], usageIds[1], usageIds[0]], 'PG 使用记录列表应按 trace 前缀读取 catalog 窗口')

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

  const detail = await getUsageRecordDetailAsync(usageIds[0], smokeAccess)
  assert(detail, 'PG 使用记录详情应按 ID 读取')
  assert.equal(detail.traceId, `${tracePrefix}_success`, 'PG 使用记录详情 traceId 应正确')
  assert.equal(detail.inputTokens, 12, 'PG 使用记录详情 token 应正确')

  const pricedDetail = await getUsageRecordDetailAsync(usageIds[3], smokeAccess)
  assert(pricedDetail, 'PG 使用记录详情应读取补价记录')
  const expectedCost = await estimateCatalogCostUsdAsync({
    providerCode: 'gpt',
    model: pricedModel,
    inputTokens: 1_000_000,
    outputTokens: 1_000_000,
    cacheReadTokens: 100_000
  })
  const expectedCacheReadCost = await estimateCatalogCacheReadCostUsdAsync({
    providerCode: 'gpt',
    model: pricedModel,
    cacheReadTokens: 100_000
  })
  assert.equal(pricedDetail.pricingModel, pricedModel, 'PG 使用记录写入前应异步补齐 pricingModel')
  assert.equal(pricedDetail.costUsd, expectedCost, 'PG 使用记录写入前应异步补齐 costUsd')
  assert.equal(pricedDetail.cacheReadCostUsd, expectedCacheReadCost, 'PG 使用记录写入前应异步补齐 cacheReadCostUsd')

  const audioImageDetail = await getUsageRecordDetailAsync(usageIds[4], smokeAccess)
  assert(audioImageDetail, 'PG 使用记录详情应读取音频/按张图片补价记录')
  const expectedAudioImageCost = await estimateCatalogCostUsdAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: audioImagePricingModel,
    inputAudioTokens: 1_000_000,
    outputAudioTokens: 1_000_000,
    outputImageCount: 2
  })
  assert.equal(expectedAudioImageCost, 16.08, '测试自定义模型应能异步计算音频/按张图片成本')
  assert.equal(audioImageDetail.pricingModel, audioImagePricingModel, 'PG 使用记录写入前应保留音频/按张图片计价模型')
  assert.equal(audioImageDetail.inputAudioTokens, 1_000_000, 'PG 使用记录详情应保留输入音频 Tokens')
  assert.equal(audioImageDetail.outputAudioTokens, 1_000_000, 'PG 使用记录详情应保留输出音频 Tokens')
  assert.equal(audioImageDetail.outputImageCount, 2, 'PG 使用记录详情应保留输出图片张数')
  assert.equal(audioImageDetail.costUsd, expectedAudioImageCost, 'PG 使用记录写入前应异步补齐音频/按张图片 costUsd')
  const audioImageResponse = await withCostBreakdownAsync(audioImageDetail)
  assert.equal(audioImageResponse.costBreakdown?.inputAudioCostUsd, 4, '使用记录响应成本拆解应包含输入音频成本')
  assert.equal(audioImageResponse.costBreakdown?.outputAudioCostUsd, 12, '使用记录响应成本拆解应包含输出音频成本')
  assert.equal(audioImageResponse.costBreakdown?.outputImageUnitCostUsd, 0.08, '使用记录响应成本拆解应包含按张图片成本')
  assert.equal(audioImageResponse.costBreakdown?.outputUsdPerImage, 0.04, '使用记录响应成本拆解应包含每张图片单价')

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
      WHERE trace_id COLLATE "C" >= $1 AND trace_id COLLATE "C" < $2
      ORDER BY created_at DESC, usage_id DESC
      LIMIT 11
    `,
    [traceId, usageRecordTextPrefixUpperBound(traceId)],
    ['idx_usage_record_shard_entries_trace_c_created_sort']
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
      WHERE client_ip COLLATE "C" >= $1 AND client_ip COLLATE "C" < $2
      ORDER BY created_at DESC, usage_id DESC
      LIMIT 11
    `,
    [clientIp, usageRecordTextPrefixUpperBound(clientIp)],
    ['idx_usage_record_shard_entries_client_ip_c_created_sort']
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
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\u{10ffff}`
}

async function cleanupSmokeRows(): Promise<void> {
  await removeCustomProviderModelAsync(audioImagePricingModelId).catch(() => false)
  await pool.query('DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY($1::text[])', [usageIds])
  await pool.query('DELETE FROM juhe_usage.usage_records WHERE id = ANY($1::text[])', [usageIds])
}
