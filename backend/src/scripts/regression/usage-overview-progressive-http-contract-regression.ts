import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

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

let server: Server | undefined
try {
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}/__aisys__/api`
  const query = `startDate=${statDate}&endDate=${statDate}`

  const adminSummary = await getData<Record<string, unknown>>(`${baseUrl}/stats/usage-overview/summary?${query}`, adminCookie)
  assert.deepEqual(Object.keys(adminSummary).sort(), ['range', 'summary'], 'summary 端点必须只返回 range/summary')
  assert.equal((adminSummary.summary as { requestCount?: number }).requestCount, 10, `管理全局 summary 应读取 global scope，实际：${JSON.stringify(adminSummary)}`)

  const scopedSummary = await getData<Record<string, unknown>>(`${baseUrl}/stats/usage-overview/summary?${query}&systemAccountId=${user.id}`, adminCookie)
  assert.equal((scopedSummary.summary as { requestCount?: number }).requestCount, 3, '管理指定用户 summary 应按 scope 隔离')

  const selfSummary = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/summary?${query}&systemAccountId=sys_admin`, userCookie)
  assert.equal((selfSummary.summary as { requestCount?: number }).requestCount, 3, '个人 summary 必须忽略伪造 systemAccountId')

  const hourly = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/hourly-trend?${query}`, userCookie)
  assert.deepEqual(Object.keys(hourly).sort(), ['hourlyTrend', 'range'], 'hourly trend 端点必须只返回 range/hourlyTrend')
  const models = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/model-distribution?${query}`, userCookie)
  assert.deepEqual(Object.keys(models).sort(), ['modelDistribution', 'range'], 'model distribution 端点必须只返回 range/modelDistribution')
  const errors = await getData<Record<string, unknown>>(`${baseUrl}/my-stats/usage-overview/errors?${query}`, userCookie)
  assert.deepEqual(Object.keys(errors).sort(), ['errors', 'range'], 'errors 端点必须只返回 range/errors')

  await assertStatus(`${baseUrl}/stats/usage-overview/summary?${query}`, userCookie, 403, '普通用户不能访问管理统计端点')
  await assertStatus(`${baseUrl}/my-stats/usage-overview/summary?startDate=bad`, userCookie, 400, '非法日期必须返回 400')

  console.log('首页统计渐进 HTTP 契约回归通过：四端点窄响应、管理/个人 scope 与日期边界保持正确')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedScope(systemAccountId: string, requestCount: number): void {
  const database = databaseModule.getStatsDatabase()
  const windowKey = rangeWindowKey(range)
  const updatedAt = '2026-01-01T01:00:00.000Z'
  database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, ?, 1, 10, 2, 0, 0, 0.01, 100, ?, 100, 20, ?, 20, ?, ?)
  `).run(systemAccountId, systemAccountId, statDate, requestCount, Math.max(0, requestCount - 1), requestCount, requestCount, `${statDate}T00:00:00.000Z`, updatedAt)
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
