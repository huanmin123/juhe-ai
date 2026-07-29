import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  deleteAccountAsync,
  deleteGroupAsync
} from '../../storage/repositories.js'
import { defaultProviderProtocolProfileAsync } from '../../storage/provider.repository.js'
import {
  getAiPerformanceBaseAsync,
  getAiPerformanceSeriesAsync,
  listAiPerformanceAccountOptionsAsync
} from '../../storage/usage-stats.repository.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'AI 性能监控 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `ai_perf_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`
const ownerSystemAccountId = `sys_${marker}`
const access: AccessScope = { systemAccountId: ownerSystemAccountId, role: 'user' }
const range = { startDate: '2026-06-28', endDate: '2026-06-28', days: 1, maxDays: 31 }
const statHour = `${range.startDate}T00`
const statsUpdatedAt = new Date().toISOString()
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  await seedOwnerSystemAccount()
  const providerProfile = await defaultProviderProtocolProfileAsync('gpt')
  assert(providerProfile, 'AI 性能 PG smoke 需要 GPT 默认协议档案')
  const group = await createGroupAsync({
    name: `AI 性能 PG smoke 分组 ${marker}`,
    providerCode: 'gpt'
  }, access)
  createdGroupIds.push(group.id)

  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: providerProfile.id,
    name: `${marker} AI 性能 PG smoke 账号`,
    type: 'api_key',
    credentials: {
      api_key: `sk-ai-performance-pg-smoke-${marker}`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-4o-mini'],
    status: 'active'
  }, access)
  createdAccountIds.push(account.id)

  await seedStats(account.id)

  const options = await listAiPerformanceAccountOptionsAsync(access, {
    keyword: marker,
    limit: 10
  })
  assert.deepEqual(options.map((item) => item.id), [account.id], 'PG AI 性能账号选项应按名称前缀命中临时账号')
  assert.equal('requestCountLast7d' in (options[0] ?? {}), false, 'PG AI 性能账号选项不得返回页面未渲染的 TopN 请求数')

  const selectedOptions = await listAiPerformanceAccountOptionsAsync(access, {
    keyword: 'not-found',
    accountIds: [account.id],
    limit: 10
  })
  assert(selectedOptions.some((item) => item.id === account.id), 'PG AI 性能账号选项应回填显式选中账号')

  const base = await getAiPerformanceBaseAsync(access, range)
  assert.equal(base.summary.requestCount, 42, 'PG AI 性能 base 应读取 summary window')
  assert(base.accounts.length <= 10, 'PG AI 性能 base 默认账号不得超过 10 个')
  const selected = await getAiPerformanceSeriesAsync(access, range, [account.id])
  const series = selected.hourlySeries.find((item) => item.accountId === account.id)
  assert(series, 'PG AI 性能 series 应返回选中账号趋势')
  const point = series.points.find((item) => item.statHour === statHour)
  assert.equal(point?.requestCount, 42, 'PG AI 性能小时趋势应读取 usage_stats_hourly')
  assert.equal(point?.averageFirstTokenMs, 12, 'PG AI 性能小时趋势应计算首 token 平均耗时')
  assert.equal(point?.averageDurationMs, 60, 'PG AI 性能小时趋势应计算平均总耗时')

  console.log(JSON.stringify({
    message: 'AI 性能监控 PG smoke 通过',
    accountId: account.id,
    optionCount: options.length,
    summaryRequestCount: base.summary.requestCount
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function seedStats(accountId: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_stats.usage_rank_snapshots (
      system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at
    ) VALUES ($1, 'caller_account', 'last7d', 'request_count', $2, 1, $3, 42, $2)
  `, [ownerSystemAccountId, statsUpdatedAt, accountId])
  await pool.query(`
    INSERT INTO juhe_stats.usage_stats_hourly (
      system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      updated_at
    ) VALUES ($1, 'caller_account', $2, $3, 42, 42, 0, 2520, 42, 90, 504, 42, 30, $4)
  `, [ownerSystemAccountId, accountId, statHour, statsUpdatedAt])
  await pool.query(`
    INSERT INTO juhe_stats.ai_performance_summary_windows (
      system_account_id, window_key, start_date, end_date, request_count,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      updated_at
    ) VALUES ($1, $2, $3, $4, 42, 2520, 42, 90, 504, 42, 30, $5)
  `, [ownerSystemAccountId, rangeWindowKey(range), range.startDate, range.endDate, statsUpdatedAt])
}

async function seedOwnerSystemAccount(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
    ) VALUES ($1, $2, $3, 'user', 'active', 'pg-smoke-password-hash', 0, 0, $4, $4)
  `, [ownerSystemAccountId, marker, `AI 性能 PG smoke 用户 ${marker}`, statsUpdatedAt])
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_rank_snapshots WHERE system_account_id = $1 AND scope_id = ANY($2::text[])', [ownerSystemAccountId, createdAccountIds])
  await pool.query('DELETE FROM juhe_stats.usage_stats_hourly WHERE system_account_id = $1 AND scope_id = ANY($2::text[])', [ownerSystemAccountId, createdAccountIds])
  await pool.query('DELETE FROM juhe_stats.ai_performance_summary_windows WHERE system_account_id = $1 AND updated_at = $2', [ownerSystemAccountId, statsUpdatedAt])
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
