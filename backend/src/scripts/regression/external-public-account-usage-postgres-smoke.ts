import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { getPublicAccountUsageAsync } from '../../modules/external-integrations/external-public-welfare.service.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createAccountAsync, createGroupAsync, deleteAccountAsync, deleteGroupAsync } from '../../storage/repositories.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '公开账号用量 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `pub_usage_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const keyword = `pub_usage_keyword_${Math.random().toString(16).slice(2, 10)}`
const ownerSystemAccountId = `sys_${marker}`
const access: AccessScope = { systemAccountId: ownerSystemAccountId, role: 'user' }
const statsUpdatedAt = new Date().toISOString()
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  await cleanupSmokeRows()
  await seedOwnerSystemAccount()

  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const group = await createGroupAsync({
    name: `公开账号用量 PG smoke 分组 ${marker}`,
    providerCode: 'gpt'
  }, access)
  createdGroupIds.push(group.id)

  const matchedAccount = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: group.providerProtocolProfileId,
    name: `${keyword} 主账号`,
    type: 'api_key',
    credentials: {
      api_key: `sk-public-usage-pg-smoke-${marker}-matched`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-4o-mini'],
    status: 'active'
  }, access)
  createdAccountIds.push(matchedAccount.id)

  const topAccount = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: group.providerProtocolProfileId,
    name: `公开账号用量 PG smoke Top ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-public-usage-pg-smoke-${marker}-top`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-4o-mini'],
    status: 'disabled'
  }, access)
  createdAccountIds.push(topAccount.id)

  await seedGlobalAccountWindows(today, matchedAccount.id, topAccount.id)

  const topResult = await getPublicAccountUsageAsync({
    range: 'today',
    page: 1,
    pageSize: 1,
    sortField: 'requestCount',
    sortOrder: 'desc'
  })
  assert.equal(topResult.source, 'stats', 'PG 公开账号用量应返回 stats 数据源')
  assert.equal(topResult.rangeReady, true, 'PG 公开账号用量应命中全局账号范围窗口')
  assert.equal(topResult.items[0]?.accountId, topAccount.id, 'PG 公开账号用量应按窗口请求数排序')
  assert.equal(topResult.items[0]?.accountName, topAccount.name, 'PG 公开账号用量应补齐账号名称元数据')
  assert.equal(topResult.items[0]?.status, 'disabled', 'PG 公开账号用量应补齐账号状态元数据')

  const keywordResult = await getPublicAccountUsageAsync({
    range: 'today',
    keyword,
    page: 1,
    pageSize: 10,
    sortField: 'totalTokens',
    sortOrder: 'desc'
  })
  assert.deepEqual(keywordResult.items.map((item) => item.accountId), [matchedAccount.id], 'PG 公开账号用量关键词应先解析账号 ID 再命中窗口表')
  assert.equal(keywordResult.items[0]?.requestCount, 1234, 'PG 公开账号用量关键词结果应读取范围窗口请求数')
  assert.equal(Object.prototype.hasOwnProperty.call(keywordResult.items[0] ?? {}, 'ownerSystemAccountId'), false, 'PG 公开账号用量不能暴露内部系统账户 ID')

  const mockResult = await getPublicAccountUsageAsync({ range: 'today', keyword: '公益', page: 1, pageSize: 2 }, { mock: true })
  assert.equal(mockResult.source, 'mock', 'PG 公开账号用量测试 token 应返回 mock 数据源')
  assert.ok(mockResult.items.length > 0, 'PG 公开账号用量 mock 分支应可用')

  await assertPublicAccountUsageIndexedPlans(today, keyword, matchedAccount.id)

  console.log(JSON.stringify({
    message: '公开账号用量 PG smoke 通过',
    matchedAccountId: matchedAccount.id,
    topAccountId: topAccount.id,
    rangeStartDate: topResult.range.startDate,
    rangeEndDate: topResult.range.endDate,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function seedOwnerSystemAccount(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
    ) VALUES ($1, $2, $3, 'user', 'active', 'pg-smoke-password-hash', 0, 0, $4, $4)
  `, [ownerSystemAccountId, marker, `公开账号用量 PG smoke 用户 ${marker}`, statsUpdatedAt])
}

async function seedGlobalAccountWindows(today: string, matchedAccountId: string, topAccountId: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_stats.usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
      last_used_at, last_error_at, updated_at
    ) VALUES
      ($1, 'account', $2, $4, $4, 1234, 1200, 34, 123400, 56700, 12000, 0.012, 1.234, 123400, 1234, 620, 43210, 1234, 90, 1, $5, $5, $5),
      ($1, 'account', $3, $4, $4, 2000000000, 1999999999, 1, 200000000, 100000000, 5000000, 0.5, 99.9, 2000000000, 2000000000, 900, 60000000, 2000000000, 120, 1, $5, NULL, $5)
  `, [GLOBAL_STATS_SYSTEM_ACCOUNT_ID, matchedAccountId, topAccountId, today, statsUpdatedAt])
}

async function assertPublicAccountUsageIndexedPlans(today: string, keywordValue: string, matchedAccountId: string): Promise<void> {
  const [lowerKeyword, lowerKeywordUpperBound] = publicAccountUsageLowerPrefixBounds(keywordValue)
  await assertIndexedPlan(
    '公开账号用量账号名称前缀 PG 查询',
    `
      SELECT id
      FROM juhe_business.accounts
      WHERE lower(name) >= $1 AND lower(name) < $2 AND starts_with(lower(name), $1)
      ORDER BY lower(name) ASC, id ASC
      LIMIT 10
    `,
    [lowerKeyword, lowerKeywordUpperBound],
    ['idx_accounts_name_lookup']
  )
  await assertIndexedPlan(
    '公开账号用量范围窗口请求数排序 PG 查询',
    `
      SELECT scope_id
      FROM juhe_stats.usage_scope_range_windows
      WHERE system_account_id = $1
        AND scope_type = 'account'
        AND start_date = $2
        AND end_date = $2
      ORDER BY request_count DESC, scope_id ASC
      LIMIT 2
    `,
    [GLOBAL_STATS_SYSTEM_ACCOUNT_ID, today],
    ['idx_usage_scope_range_windows_request_count']
  )
  await assertIndexedPlan(
    '公开账号用量关键词窗口 PG 查询',
    `
      SELECT scope_id
      FROM juhe_stats.usage_scope_range_windows
      WHERE system_account_id = $1
        AND scope_type = 'account'
        AND start_date = $2
        AND end_date = $2
        AND scope_id IN ($3)
      ORDER BY (input_tokens + output_tokens) DESC, scope_id ASC
      LIMIT 11
    `,
    [GLOBAL_STATS_SYSTEM_ACCOUNT_ID, today, matchedAccountId],
    ['idx_usage_scope_range_windows_lookup', 'idx_usage_scope_range_windows_total_tokens']
  )
}

async function assertIndexedPlan(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<void> {
  const pool = await getPostgresPool()
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

function publicAccountUsageLowerPrefixBounds(value: string): [string, string] {
  const lowerValue = value.toLowerCase()
  if (!lowerValue) return ['', '\uffff']
  const chars = [...lowerValue]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return [lowerValue, `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`]
  }
  return [lowerValue, `${lowerValue}\uffff`]
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_scope_range_windows WHERE system_account_id = $1 AND scope_id = ANY($2::text[])', [
    GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
    createdAccountIds
  ])
  for (const accountId of createdAccountIds) {
    await deleteAccountAsync(accountId, access).catch(() => false)
  }
  for (const groupId of createdGroupIds) {
    await deleteGroupAsync(groupId, access).catch(() => undefined)
  }
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = $1', [ownerSystemAccountId])
}
