import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  deleteAccountAsync,
  deleteGroupAsync,
  getAccountUsageStatsOverviewPageAsync
} from '../../storage/repositories.js'
import { getAccountUsageStatsSummaryAsync, getAccountUsageStatsTrendAsync } from '../../storage/account-usage.repository.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账号用量统计 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `account_usage_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`
const keyword = `account_usage_keyword_${Math.random().toString(16).slice(2, 10)}`
const ownerSystemAccountId = `sys_${marker}`
const access: AccessScope = { systemAccountId: ownerSystemAccountId, role: 'user' }
const range = { startDate: '2026-06-28', endDate: '2026-06-28', days: 1, maxDays: 31 }
const statsUpdatedAt = new Date().toISOString()
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  await cleanupSmokeRows()
  await seedOwnerSystemAccount()
  const group = await createGroupAsync({
    name: `账号用量 PG smoke 分组 ${marker}`,
    providerCode: 'gpt'
  }, access)
  createdGroupIds.push(group.id)

  const matchedAccount = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${keyword} 主账号`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-usage-pg-smoke-${marker}-matched`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-4o-mini'],
    status: 'active'
  }, access)
  createdAccountIds.push(matchedAccount.id)

  const selectedAccount = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `账号用量 PG smoke 手动补入 ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-usage-pg-smoke-${marker}-selected`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-4o-mini'],
    status: 'active'
  }, access)
  createdAccountIds.push(selectedAccount.id)

  await seedStats(matchedAccount.id, selectedAccount.id)

  const defaultResult = await getAccountUsageStatsOverviewPageAsync(access, {
    page: 1,
    pageSize: 1,
    range
  })
  assert.deepEqual(defaultResult.rows.map((row) => row.id), [matchedAccount.id], 'PG 账号用量默认列表应按 caller_account 窗口请求数排序')
  assert.equal(defaultResult.hasMore, true, 'PG 账号用量分页应保留窗口表 hasMore')
  const defaultSummary = await getAccountUsageStatsSummaryAsync(access, range)
  assert.equal(defaultSummary.summary.requestCount, 38, 'PG 账号用量独立汇总应读取 system_account 范围窗口汇总')
  assert.deepEqual(defaultResult.defaultTrendAccountIds.slice(0, 1), [matchedAccount.id], 'PG 账号用量默认趋势账号应读取 rank snapshot')

  const keywordResult = await getAccountUsageStatsOverviewPageAsync(access, {
    keyword,
    page: 1,
    pageSize: 10,
    range
  })
  assert.deepEqual(keywordResult.rows.map((row) => row.id), [matchedAccount.id], 'PG 账号用量关键词应先解析账号 ID 再命中窗口表')
  assert.equal(keywordResult.rows[0]?.rangeUsage.requestCount, 31, 'PG 账号用量关键词结果应读取范围窗口用量')
  assert.deepEqual(keywordResult.rows[0]?.dailyUsage, [], 'PG 账号用量列表不应携带日趋势')
  const keywordTrend = await getAccountUsageStatsTrendAsync(access, range, [matchedAccount.id])
  const dailyPoint = keywordTrend.rows[0]?.dailyUsage.find((point) => point.statDate === range.startDate)
  assert.equal(dailyPoint?.requestCount, 31, 'PG 账号用量日趋势应读取 usage_stats_daily')

  const selectedResult = await getAccountUsageStatsOverviewPageAsync(access, {
    keyword: 'account-usage-not-found',
    accountIds: [selectedAccount.id],
    page: 1,
    pageSize: 10,
    range
  })
  assert.deepEqual(selectedResult.rows.map((row) => row.id), [selectedAccount.id], 'PG 账号用量关键词未命中时仍应按 scope_id 补入手动选中账号')
  assert.equal(selectedResult.rows[0]?.rangeUsage.requestCount, 7, 'PG 账号用量手动补入行应读取选中账号窗口用量')

  console.log(JSON.stringify({
    message: '账号用量统计 PG smoke 通过',
    matchedAccountId: matchedAccount.id,
    selectedAccountId: selectedAccount.id,
    summaryRequestCount: defaultSummary.summary.requestCount
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
  `, [ownerSystemAccountId, marker, `账号用量 PG smoke 用户 ${marker}`, statsUpdatedAt])
}

async function seedStats(matchedAccountId: string, selectedAccountId: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_stats.usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
      last_used_at, last_error_at, updated_at
    ) VALUES
      ($1, 'caller_account', $2, $4, $5, 31, 31, 0, 310, 155, 12, 0.012, 0.456, 3100, 31, 180, 620, 31, 60, 1, $6, NULL, $6),
      ($1, 'caller_account', $3, $4, $5, 7, 7, 0, 70, 35, 3, 0.003, 0.111, 700, 7, 90, 140, 7, 40, 1, $6, NULL, $6),
      ($1, 'system_account', $1, $4, $5, 38, 38, 0, 380, 190, 15, 0.015, 0.567, 3800, 38, 180, 760, 38, 60, 1, $6, NULL, $6)
  `, [ownerSystemAccountId, matchedAccountId, selectedAccountId, range.startDate, range.endDate, statsUpdatedAt])
  await pool.query(`
    INSERT INTO juhe_stats.usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    ) VALUES ($1, 'caller_account', $2, $3, 31, 31, 0, 310, 155, 12, 0.012, 0.456, 3100, 31, 180, 620, 31, 60, $4, NULL, $4)
  `, [ownerSystemAccountId, matchedAccountId, range.startDate, statsUpdatedAt])
  await pool.query(`
    INSERT INTO juhe_stats.usage_rank_snapshots (
      system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at
    ) VALUES ($1, 'caller_account', 'last7d', 'request_count', $2, 1, $3, 31, $2)
  `, [ownerSystemAccountId, statsUpdatedAt, matchedAccountId])
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_scope_range_windows WHERE system_account_id = $1', [ownerSystemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_daily WHERE system_account_id = $1', [ownerSystemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_rank_snapshots WHERE system_account_id = $1', [ownerSystemAccountId])
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
