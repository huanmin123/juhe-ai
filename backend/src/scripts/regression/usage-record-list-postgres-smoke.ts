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
  `usage_${marker}_audio_image`,
  `usage_${marker}_account_trace`,
  `usage_${marker}_unknown_pricing_snapshot`
]
const usageCreatedAts = usageIds.map((_id, index) => new Date(createdAtBase + index).toISOString())
const accountId = `acct_${marker}`
const accountKeyword = `PG 使用记录账户 ${marker}`
const tracePrefix = `trace_${marker}`
const model = `model-${marker}`
const pricedModel = 'gpt-5.5'
const audioImagePricingModelId = `custom_model_${marker}`
const audioImagePricingModel = `gpt-regression-audio-image-${marker}`
const clientIpPrefix = `198.18.${100 + (createdAtBase % 100)}.`
const primaryClientIp = `${clientIpPrefix}10`
const pool = await getPostgresPool()
const smokeAccess = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: 'sys_admin' }

try {
  await cleanupLegacySmokeRows()
  await seedSmokeAccount()
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
      createdAt: usageCreatedAts[0]
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
      createdAt: usageCreatedAts[1]
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
      createdAt: usageCreatedAts[2]
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
      requestedServiceTier: 'flex',
      effectiveServiceTier: 'priority',
      billedServiceTier: 'priority',
      requestedReasoningEffort: 'low',
      effectiveReasoningEffort: 'high',
      statusCode: 200,
      success: true,
      durationMs: 90,
      firstTokenMs: 24,
      inputTokens: 1_000_000,
      outputTokens: 1_000_000,
      cacheReadTokens: 100_000,
      createdAt: usageCreatedAts[3]
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
      createdAt: usageCreatedAts[4]
    },
    {
      id: usageIds[5],
      traceId: `${tracePrefix}_account_trace`,
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      accountId,
      clientIp: '198.18.206.3',
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: `${model}-account`,
      statusCode: 200,
      success: true,
      durationMs: 75,
      firstTokenMs: 18,
      inputTokens: 5,
      outputTokens: 6,
      createdAt: usageCreatedAts[5]
    },
    {
      id: usageIds[6],
      traceId: `historical_lock_${marker}`,
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      endpoint: '/v1/responses',
      providerCode: `unknown-provider-${marker}`,
      model: `unknown-model-${marker}`,
      statusCode: 200,
      success: true,
      durationMs: 60,
      firstTokenMs: 15,
      inputTokens: 100,
      outputTokens: 20,
      costUsd: 0.456,
      createdAt: usageCreatedAts[6]
    }
  ])
  const writeCounts = await readSmokeWriteCounts()
  assert.equal(writeCounts.records, usageIds.length, 'PG 使用记录 smoke 应写入 usage_records 主表')
  assert.equal(writeCounts.entries, 0, 'PG 使用记录 smoke 不应再写入 usage_record_shard_entries catalog')

  const traceList = await listUsageRecordsAsync(smokeAccess, {
    traceId: tracePrefix,
    page: 1,
    pageSize: 10
  })
  assert.deepEqual(traceList.items.map((item) => item.id), [usageIds[5], usageIds[4], usageIds[3], usageIds[2], usageIds[1], usageIds[0]], 'PG 使用记录列表应按用户范围直接读取 usage_records 主表')
  const pricedListItem = traceList.items.find((item) => item.id === usageIds[3])
  assert.equal(pricedListItem?.billedServiceTier, 'priority', 'PG 使用记录列表投影必须保留实际计费服务档位')
  assert.equal(pricedListItem?.requestedReasoningEffort, 'low', 'PG 使用记录列表投影必须保留请求思考级别')
  assert.equal(pricedListItem?.effectiveReasoningEffort, 'high', 'PG 使用记录列表投影必须保留实际上游思考级别')

  const traceAccountList = await listUsageRecordsAsync(smokeAccess, {
    traceId: tracePrefix,
    accountKeyword,
    page: 1,
    pageSize: 10
  })
  assert.deepEqual(traceAccountList.items.map((item) => item.id), [usageIds[5]], 'PG 使用记录 trace + accountKeyword 组合查询应保留 trace 前缀索引路径并按账户命中')

  const failedList = await listUsageRecordsAsync(smokeAccess, {
    model,
    result: 'failed',
    sortBy: 'createdAt',
    sortOrder: 'desc',
    page: 1,
    pageSize: 10
  })
  assert.deepEqual(failedList.items.map((item) => item.id), [usageIds[1]], 'PG 使用记录列表应支持用户范围内 model + failed 筛选')

  const clientIpList = await listUsageRecordsAsync(smokeAccess, {
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
    serviceTier: 'priority',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000,
    cacheReadTokens: 100_000
  })
  const expectedCacheReadCost = await estimateCatalogCacheReadCostUsdAsync({
    providerCode: 'gpt',
    model: pricedModel,
    serviceTier: 'priority',
    cacheReadTokens: 100_000
  })
  assert.equal(pricedDetail.pricingModel, pricedModel, 'PG 使用记录写入前应异步补齐 pricingModel')
  assert.equal(pricedDetail.costUsd, expectedCost, 'PG 使用记录写入前应异步补齐 costUsd')
  assert.equal(pricedDetail.cacheReadCostUsd, expectedCacheReadCost, 'PG 使用记录写入前应异步补齐 cacheReadCostUsd')
  assert.equal(pricedDetail.pricingSnapshot?.accountChargeUsd, expectedCost, 'PG 使用记录必须固化请求时成本与最终单价快照')

  const unknownPricingDetail = await getUsageRecordDetailAsync(usageIds[6], smokeAccess)
  assert(unknownPricingDetail, 'PG 使用记录应读取目录未命中的历史锁定样本')
  assert.equal(unknownPricingDetail.pricingSnapshot?.accountChargeUsd, 0.456, 'PG 目录未命中时仍应固化写入时已有成本')
  assert.equal(unknownPricingDetail.pricingSnapshot?.serviceTierPricingSource, 'unknown', 'PG 目录未命中时必须固化 unknown 快照')

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

  await assertUsageRecordExplainPlans(tracePrefix, model, clientIpPrefix, accountId)

  console.log(JSON.stringify({
    message: '使用记录列表 PG smoke 通过',
    records: writeCounts.records,
    entries: writeCounts.entries,
    traceItems: traceList.items.length,
    traceAccountItems: traceAccountList.items.length,
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

async function assertUsageRecordExplainPlans(traceId: string, targetModel: string, clientIp: string, targetAccountId: string): Promise<void> {
  await assertIndexedPlan(
    '使用记录 trace + account PG 查询',
    `
      WITH matched_usage_records AS MATERIALIZED (
        SELECT ur.id, ur.created_at
        FROM juhe_usage.usage_records ur
        WHERE ur.system_account_id = $1
          AND ur.trace_id COLLATE "C" >= $2
          AND ur.trace_id COLLATE "C" < $3
          AND ur.account_id IN ($4)
        ORDER BY ur.created_at DESC, ur.id DESC
        LIMIT 11
      )
      SELECT ur.id
      FROM matched_usage_records matched_usage_records
      INNER JOIN juhe_usage.usage_records ur
        ON ur.created_at = matched_usage_records.created_at
        AND ur.id = matched_usage_records.id
      ORDER BY ur.created_at DESC, ur.id DESC
      LIMIT 11
    `,
    ['sys_admin', traceId, usageRecordTextPrefixUpperBound(traceId), targetAccountId],
    [
      'idx_usage_records_system_trace_c_created_sort',
      'system_account_id_trace_id_created__idx',
      'system_account_id_account_id_created_idx'
    ]
  )
  await assertIndexedPlan(
    '使用记录 trace 前缀 PG 查询',
    `
      WITH matched_usage_records AS MATERIALIZED (
        SELECT ur.id, ur.created_at
        FROM juhe_usage.usage_records ur
        WHERE ur.system_account_id = $1
          AND ur.trace_id COLLATE "C" >= $2
          AND ur.trace_id COLLATE "C" < $3
      )
      SELECT ur.id
      FROM matched_usage_records matched_usage_records
      INNER JOIN juhe_usage.usage_records ur
        ON ur.created_at = matched_usage_records.created_at
        AND ur.id = matched_usage_records.id
      ORDER BY ur.created_at DESC, ur.id DESC
      LIMIT 11
    `,
    ['sys_admin', traceId, usageRecordTextPrefixUpperBound(traceId)],
    ['idx_usage_records_system_trace_c_created_sort', 'system_account_id_trace_id_created__idx']
  )
  await assertIndexedPlan(
    '使用记录 model 筛选 PG 查询',
    `
      SELECT id
      FROM juhe_usage.usage_records
      WHERE system_account_id = $1
        AND model = $2
      ORDER BY created_at DESC, id DESC
      LIMIT 11
    `,
    ['sys_admin', targetModel],
    ['idx_usage_records_system_account_created_sort', 'system_account_id_created_at_id_idx']
  )
  await assertIndexedPlan(
    '使用记录 client IP 前缀 PG 查询',
    `
      SELECT id
      FROM juhe_usage.usage_records
      WHERE system_account_id = $1
        AND client_ip COLLATE "C" >= $2
        AND client_ip COLLATE "C" < $3
      ORDER BY created_at DESC, id DESC
      LIMIT 11
    `,
    ['sys_admin', clientIp, usageRecordTextPrefixUpperBound(clientIp)],
    ['idx_usage_records_system_account_created_sort', 'system_account_id_created_at_id_idx']
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
  await deleteUsageRecordsByPartitionKeys(usageIds.map((id, index) => ({ id, createdAt: usageCreatedAts[index] })))
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = $1', [accountId])
}

async function cleanupLegacySmokeRows(): Promise<void> {
  await pool.query("DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id LIKE 'usage_usage_record_list_pg_smoke_%'")
  const legacyRows = await pool.query(`
    SELECT id, created_at
    FROM juhe_usage.usage_records
    WHERE id LIKE 'usage_usage_record_list_pg_smoke_%'
    LIMIT 5000
  `)
  await deleteUsageRecordsByPartitionKeys(legacyRows.rows.map((row: Record<string, unknown>) => ({
    id: String(row.id ?? ''),
    createdAt: String(row.created_at ?? '')
  })))
  await pool.query("DELETE FROM juhe_business.custom_provider_models WHERE id LIKE 'custom_model_usage_record_list_pg_smoke_%'")
  await pool.query("DELETE FROM juhe_business.accounts WHERE id LIKE 'acct_usage_record_list_pg_smoke_%'")
}

async function deleteUsageRecordsByPartitionKeys(rows: Array<{ id: string; createdAt: string }>): Promise<void> {
  const keys = rows
    .map((row) => ({ id: row.id.trim(), createdAt: row.createdAt.trim() }))
    .filter((row) => row.id && row.createdAt)
  if (!keys.length) return
  await pool.query(
    `DELETE FROM juhe_usage.usage_records WHERE (created_at, id) IN (${keys.map((_row, index) => `($${index * 2 + 1}, $${index * 2 + 2})`).join(', ')})`,
    keys.flatMap((row) => [row.createdAt, row.id])
  )
}

async function seedSmokeAccount(): Promise<void> {
  const profileResult = await pool.query(`
    SELECT id, protocol_code, protocol_version
    FROM juhe_business.provider_protocol_profiles
    WHERE provider_code = 'gpt'
    ORDER BY id ASC
    LIMIT 1
  `)
  const profile = profileResult.rows[0] as { id?: string; protocol_code?: string; protocol_version?: string } | undefined
  assert(profile, 'PG 使用记录 smoke 需要默认 gpt provider protocol profile')
  const now = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_mask, health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES ($1, 'sys_admin', 'gpt', $2, $3, $4, $5, 'api_key', 'active', '{}', '', 'gpt-5.4-mini', 'responses_sse', $6, $6)
    ON CONFLICT (id) DO UPDATE SET
      name = EXCLUDED.name,
      status = EXCLUDED.status,
      updated_at = EXCLUDED.updated_at
  `, [accountId, profile.id, profile.protocol_code, profile.protocol_version, accountKeyword, now])
}
