import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'
import { fixedUsageStatsDefaultRange, rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-overview-progressive-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-overview-progressive-http-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { statsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  usageStatsRepository,
  usageStatsHelpers,
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/stats/stats.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-stats', forceSelfAccessScope, statsRouter)
app.use('/__aisys__/api/stats', requireAdmin, statsRouter)

const statDate = usageStatsHelpers.dateKey(new Date(), usageStatsHelpers.usageStatsTimezone())
const range = { startDate: statDate, endDate: statDate, days: 1, maxDays: 31 }
const defaultRange = fixedUsageStatsDefaultRange(usageStatsHelpers.usageStatsTimezone(), statDate)
const user = repositories.createSystemAccount({
  username: 'stats_progressive_user',
  displayName: '统计渐进用户',
  password: 'password',
  role: 'user',
  mustChangePassword: false
})
const adminCookie = sessionCookie('sys_admin')
const userCookie = sessionCookie(user.id)
seedScope(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, 10)
seedScope(user.id, 3)
assert.equal(usageStatsRepository.getUsageStatsOverviewSummary({ systemAccountId: 'sys_admin', role: 'admin' }, range).summary.requestCount, 10, 'HTTP 夹具应能直接读取管理全局 summary')
assertOverviewQueryBoundaries()

let server: Server | undefined
try {
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}/__aisys__/api`
  const query = `startDate=${statDate}&endDate=${statDate}`

  const defaultSummary = await getData<Record<string, unknown>>(`${baseUrl}/stats/usage-overview/summary`, adminCookie)
  assert.equal((defaultSummary.range as { days?: number }).days, 31, '统计概览未传日期时必须默认近 31 天')
  assert.equal((defaultSummary.summary as { requestCount?: number }).requestCount, 10, '默认近 31 天 summary 应直读对应窗口')

  const adminSummary = await getData<Record<string, unknown>>(`${baseUrl}/stats/usage-overview/summary?${query}`, adminCookie)
  assert.deepEqual(Object.keys(adminSummary).sort(), ['range', 'summary'], 'summary 端点必须只返回 range/summary')
  assert.deepEqual(Object.keys(adminSummary.summary as Record<string, unknown>).sort(), [
    'averageDurationMs', 'averageFirstTokenMs', 'cacheReadTokens', 'errorCount', 'errorRate',
    'inputTokens', 'outputTokens', 'requestCount', 'successCount', 'totalCost', 'totalTokens'
  ], 'summary 必须只返回首页实际渲染字段')
  assert.equal((adminSummary.summary as { requestCount?: number }).requestCount, 10, `管理全局 summary 应读取 global scope，实际：${JSON.stringify(adminSummary)}`)

  const scopedSummary = await getData<Record<string, unknown>>(`${baseUrl}/stats/usage-overview/summary?${query}&systemAccountId=${user.id}`, adminCookie)
  assert.equal((scopedSummary.summary as { requestCount?: number }).requestCount, 3, '管理指定用户 summary 应按 scope 隔离')

  const selfSummary = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/summary?${query}&systemAccountId=sys_admin`, userCookie)
  assert.equal((selfSummary.summary as { requestCount?: number }).requestCount, 3, '个人 summary 必须忽略伪造 systemAccountId')

  const adminDailyTrend = await getData<DailyTrendPayload>(`${baseUrl}/stats/usage-overview/daily-trend`, adminCookie)
  assert.deepEqual(Object.keys(adminDailyTrend).sort(), ['dailyTrend', 'range'], 'daily trend 端点必须只返回 range/dailyTrend')
  assert.equal(adminDailyTrend.range.days, 31, 'daily trend 未传日期时必须默认近 31 个自然日')
  assert.equal(adminDailyTrend.dailyTrend.length, 31, '默认 daily trend 必须补齐 31 个日点')
  assert.deepEqual(adminDailyTrend.dailyTrend.at(-1), { statDate, totalTokens: 120, totalCost: 0.1 }, '管理全局 daily trend 应读取 global system_account 日聚合')

  const filteredDailyTrend = await getData<DailyTrendPayload>(`${baseUrl}/stats/usage-overview/daily-trend?${query}`, adminCookie)
  assert.equal(filteredDailyTrend.range.days, 1, 'daily trend 必须遵守日期筛选')
  assert.equal(filteredDailyTrend.dailyTrend.length, 1, '单日筛选只应返回一个补齐后的日点')

  const scopedDailyTrend = await getData<DailyTrendPayload>(`${baseUrl}/stats/usage-overview/daily-trend?systemAccountId=${user.id}`, adminCookie)
  assert.deepEqual(scopedDailyTrend.dailyTrend.at(-1), { statDate, totalTokens: 36, totalCost: 0.03 }, '管理指定用户 daily trend 应按 system account scope 隔离')

  const selfDailyTrend = await getData<DailyTrendPayload>(`${baseUrl}/my-stats/usage-overview/daily-trend?${query}&systemAccountId=sys_admin`, userCookie)
  assert.equal(selfDailyTrend.range.days, 1, '个人 daily trend 必须遵守合法日期筛选')
  assert.deepEqual(selfDailyTrend.dailyTrend.at(-1), { statDate, totalTokens: 36, totalCost: 0.03 }, '个人 daily trend 必须忽略伪造 systemAccountId')

  const hourly = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/hourly-trend?${query}`, userCookie)
  assert.deepEqual(Object.keys(hourly).sort(), ['hourlyTrend', 'range'], 'hourly trend 端点必须只返回 range/hourlyTrend')
  assert.deepEqual(Object.keys((hourly.hourlyTrend as Array<Record<string, unknown>>)[0] ?? {}).sort(), [
    'averageDurationMs', 'errorCount', 'requestCount', 'statHour'
  ], 'hourly trend 行必须只返回图表实际渲染字段')
  const models = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/model-distribution?${query}`, userCookie)
  assert.deepEqual(Object.keys(models).sort(), ['modelDistribution', 'range'], 'model distribution 端点必须只返回 range/modelDistribution')
  assert.deepEqual(Object.keys((models.modelDistribution as Array<Record<string, unknown>>)[0] ?? {}).sort(), [
    'model', 'providerCode', 'requestCount', 'totalCost', 'totalTokens'
  ], 'model distribution 行必须只返回图表实际渲染字段')
  const errors = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/errors?${query}`, userCookie)
  assert.deepEqual(Object.keys(errors).sort(), ['errors', 'range'], 'errors 端点必须只返回 range/errors')
  assert.deepEqual(Object.keys((errors.errors as Array<Record<string, unknown>>)[0] ?? {}).sort(), [
    'errorCode', 'errorCount', 'errorMessage', 'providerCode', 'statusCode'
  ], 'errors 行必须只返回页面实际渲染字段')

  await assertStatus(`${baseUrl}/stats/usage-overview/summary?${query}`, userCookie, 403, '普通用户不能访问管理统计端点')
  await assertStatus(`${baseUrl}/stats/usage-overview?${query}`, adminCookie, 404, '旧组合管理端点必须退场')
  await assertStatus(`${baseUrl}/my-stats/usage-overview?${query}`, userCookie, 404, '旧组合自助端点必须退场')
  await assertStatus(`${baseUrl}/my-stats/usage-overview/summary?startDate=bad`, userCookie, 400, '非法日期必须返回 400')
  await assertStatus(`${baseUrl}/my-stats/usage-overview/daily-trend?startDate=bad`, userCookie, 400, '日趋势非法日期必须返回 400')

  console.log('首页统计渐进 HTTP 契约回归通过：默认近 31 天、日趋势跟随筛选、摘要单行窗口、管理/个人 scope 与日期边界保持正确')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

interface DailyTrendPayload {
  range: { startDate: string; endDate: string; days: number; maxDays: number }
  dailyTrend: Array<{ statDate: string; totalTokens: number; totalCost: number }>
}

function assertOverviewQueryBoundaries(): void {
  const source = readFileSync(new URL('../../storage/usage-stats.repository.ts', import.meta.url), 'utf8')
  const summarySource = source.slice(
    source.indexOf('function loadUsageOverviewSummaryRow('),
    source.indexOf('type UsageOverviewSummaryWindowRow')
  )
  assert.match(summarySource, /FROM (?:juhe_stats\.)?usage_overview_summary_windows/, '摘要必须直读 usage_overview_summary_windows')
  assert.doesNotMatch(summarySource, /usage_stats_daily|aggregateUsageRowsForRange|\bSUM\s*\(|\bGROUP BY\b/i, '摘要请求路径不得读取日表或临时聚合')
  assert.doesNotMatch(summarySource, /cache_read_cost_usd|cache_write_|thinking_tokens|image_tokens|last_used_at/i, '摘要 SQL 不得读取页面未展示字段')

  const hourlySource = source.slice(
    source.indexOf('export function getUsageStatsOverviewHourlyTrend'),
    source.indexOf('export function getUsageStatsOverviewModelDistribution')
  )
  assert.doesNotMatch(hourlySource, /input_tokens|output_tokens|cache_read_|cache_write_|thinking_tokens|image_tokens|total_cost/i, '小时趋势 SQL 不得读取页面未展示字段')

  const modelSource = source.slice(
    source.indexOf('export function getUsageStatsOverviewModelDistribution'),
    source.indexOf('export function getUsageStatsOverviewErrors')
  )
  assert.doesNotMatch(modelSource, /cache_read_|cache_write_|thinking_tokens|image_tokens/i, '模型分布 SQL 不得读取页面未展示字段')

  const dailyTrendSource = source.slice(
    source.indexOf('export function getUsageStatsOverviewDailyTrend'),
    source.indexOf('export function getUsageStatsOverviewHourlyTrend')
  )
  assert.match(dailyTrendSource, /FROM (?:juhe_stats\.)?usage_stats_daily/, '日趋势必须直读 usage_stats_daily')
  assert.match(dailyTrendSource, /scope_type = 'system_account'/, '日趋势必须读取 system_account 作用域')
  assert.doesNotMatch(dailyTrendSource, /\bSUM\s*\(|\bGROUP BY\b|usage_records/i, '日趋势请求路径不得临时聚合或扫描 usage_records')

  const summaryPlan = databaseModule.getStatsDatabase().prepare(`
    EXPLAIN QUERY PLAN
    SELECT request_count
    FROM usage_overview_summary_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).all(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, rangeWindowKey(range), range.startDate, range.endDate) as unknown as Array<{ detail?: string }>
  assert.match(summaryPlan.map((row) => row.detail ?? '').join('\n'), /sqlite_autoindex_usage_overview_summary_windows/i, '摘要必须命中 system_account/window_key 主键索引')

  const plan = databaseModule.getStatsDatabase().prepare(`
    EXPLAIN QUERY PLAN
    SELECT stat_date, input_tokens, output_tokens, total_cost_usd
    FROM usage_stats_daily
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_date >= ?
      AND stat_date <= ?
    ORDER BY stat_date ASC
  `).all(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, GLOBAL_STATS_SYSTEM_ACCOUNT_ID, range.startDate, range.endDate) as unknown as Array<{ detail?: string }>
  assert.match(plan.map((row) => row.detail ?? '').join('\n'), /idx_usage_stats_daily_scope_date|sqlite_autoindex_usage_stats_daily/i, '日趋势必须命中 scope/date 索引')
}

function seedScope(systemAccountId: string, requestCount: number): void {
  const database = databaseModule.getStatsDatabase()
  const windowKey = rangeWindowKey(range)
  const updatedAt = '2026-01-01T01:00:00.000Z'
  const inputTokens = requestCount * 10
  const outputTokens = requestCount * 2
  const totalCost = requestCount / 100
  database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, ?, 1, ?, ?, 0, 0, ?, 100, ?, 100, 20, ?, 20, ?, ?)
  `).run(systemAccountId, systemAccountId, statDate, requestCount + 1000, Math.max(0, requestCount - 1), inputTokens, outputTokens, totalCost, requestCount, requestCount, `${statDate}T00:00:00.000Z`, updatedAt)
  const insertSummary = database.prepare(`
    INSERT INTO usage_overview_summary_windows (
      system_account_id, window_key, start_date, end_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, 0, 0, ?, 100, ?, 20, ?, ?, ?)
  `)
  for (const summaryRange of [range, defaultRange]) {
    insertSummary.run(
      systemAccountId,
      rangeWindowKey(summaryRange),
      summaryRange.startDate,
      summaryRange.endDate,
      requestCount,
      Math.max(0, requestCount - 1),
      inputTokens,
      outputTokens,
      totalCost,
      requestCount,
      requestCount,
      `${statDate}T00:00:00.000Z`,
      updatedAt
    )
  }
  database.prepare(`
    INSERT INTO usage_overview_trend_windows (
      system_account_id, window_key, start_date, end_date, bucket_key, request_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 1, 10, 2, 0, 0, 0.01, 100, ?, ?)
  `).run(systemAccountId, windowKey, statDate, statDate, `${statDate}T00`, requestCount, requestCount, updatedAt)
  database.prepare(`
    INSERT INTO usage_model_rank_windows (
      system_account_id, window_key, start_date, end_date, rank, provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, 1, 'gpt', 'progressive-model', ?, 10, 2, 0, 0, 0.01, ?)
  `).run(systemAccountId, windowKey, statDate, statDate, requestCount, updatedAt)
  database.prepare(`
    INSERT INTO usage_error_rank_windows (
      system_account_id, window_key, start_date, end_date, rank, provider_code, error_code,
      status_code, error_message, error_count, updated_at
    ) VALUES (?, ?, ?, ?, 1, 'gpt', 'progressive_error', 500, 'progressive error', 1, ?)
  `).run(systemAccountId, windowKey, statDate, statDate, updatedAt)
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getData<T>(url: string, cookie: string): Promise<T> {
  const response = await fetch(url, { headers: { cookie } })
  const payload = await response.json() as { data?: T; message?: string }
  assert.equal(response.status, 200, payload.message ?? `GET ${url} 失败`)
  assert(payload.data !== undefined, `GET ${url} 缺少 data`)
  return payload.data
}

async function assertStatus(url: string, cookie: string, expected: number, label: string): Promise<void> {
  const response = await fetch(url, { headers: { cookie } })
  assert.equal(response.status, expected, `${label}，实际 HTTP ${response.status}: ${await response.text()}`)
}

function listen(server: Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server: Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
