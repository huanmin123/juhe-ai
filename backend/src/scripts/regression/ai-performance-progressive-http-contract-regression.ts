import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-ai-performance-progressive-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'ai-performance-progressive-http-secret'
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
  usageStatsHelpers,
  providerRepository,
  { closeSqliteReadWorkerPool, getSqliteReadWorkerPoolRuntime }
] = await Promise.all([
  import('../../modules/stats/stats.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/provider.repository.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-stats', forceSelfAccessScope, statsRouter)
app.use('/__aisys__/api/stats', requireAdmin, statsRouter)

const statDate = usageStatsHelpers.dateKey(new Date(), usageStatsHelpers.usageStatsTimezone())
const statHour = `${statDate}T00`
const range = { startDate: statDate, endDate: statDate, days: 1, maxDays: 31 }
const user = repositories.createSystemAccount({
  username: 'aiperformanceprogressiveuser',
  displayName: 'AI性能渐进用户',
  password: 'password',
  role: 'user',
  mustChangePassword: false
})
const userAccess = { systemAccountId: user.id, role: 'user' as const }
const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
const providerProfile = await providerRepository.defaultProviderProtocolProfile('gpt')
if (!providerProfile) throw new Error('AI 性能渐进 HTTP 回归缺少 GPT 协议档案')
const userGroup = repositories.createGroup({ name: 'AI 性能渐进用户分组', providerCode: 'gpt' }, userAccess)
const adminGroup = repositories.createGroup({ name: 'AI 性能渐进管理分组', providerCode: 'gpt' }, adminAccess)
const visibleAccount = repositories.createAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: providerProfile.id,
  name: 'AI 性能可见账号',
  type: 'api_key',
  credentials: { api_key: 'sk-ai-performance-visible', base_url: 'https://api.openai.com/v1' },
  supportedModels: ['gpt-5.5'],
  groupId: userGroup.id
}, userAccess)
const hiddenAccount = repositories.createAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: providerProfile.id,
  name: 'AI 性能不可见账号',
  type: 'api_key',
  credentials: { api_key: 'sk-ai-performance-hidden', base_url: 'https://api.openai.com/v1' },
  supportedModels: ['gpt-5.5'],
  groupId: adminGroup.id
}, adminAccess)
seedUserStats(visibleAccount.id)

const userCookie = sessionCookie(user.id)
let server: Server | undefined
try {
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}/__aisys__/api/my-stats/ai-performance`
  const dateQuery = `startDate=${statDate}&endDate=${statDate}`

  const base = await getData<Record<string, unknown>>(`${baseUrl}?${dateQuery}`, userCookie)
  assert.deepEqual(Object.keys(base).sort(), ['accounts', 'hourlySeries', 'range', 'summary'], 'base 必须只返回 range/summary/accounts/hourlySeries')
  const baseAccounts = base.accounts as Array<{ id: string }>
  assert(baseAccounts.length <= 10, 'base 默认账号不能超过 10 个')
  assert.deepEqual(baseAccounts.map((account) => account.id), [visibleAccount.id], 'base 应返回当前 scope 默认排行账号')
  assert.deepEqual(Object.keys(baseAccounts[0] ?? {}).sort(), ['id', 'name', 'providerCode'], '自助 base 的自有账户不得返回管理端所属用户或授权标签字段')

  const defaultRangeBase = await getData<{ range: { days: number; maxDays: number } }>(baseUrl, userCookie)
  assert.equal(defaultRangeBase.range.days, 3, '未传日期时 AI 性能必须按 Node 统计时区返回默认最近 3 天')
  assert.equal(defaultRangeBase.range.maxDays, 31)

  await assertStatus(`${baseUrl}?${dateQuery}&accountIds=${visibleAccount.id}`, userCookie, 400, 'base 必须拒绝 accountIds')
  await assertStatus(`${baseUrl}?${dateQuery}&accountIds[]=${visibleAccount.id}`, userCookie, 400, 'base 必须拒绝 bracket accountIds')

  const series = await getData<Record<string, unknown>>(
    `${baseUrl}/series?${dateQuery}&accountIds=${visibleAccount.id}&accountIds=${hiddenAccount.id}`,
    userCookie
  )
  assert.deepEqual(Object.keys(series).sort(), ['accounts', 'hourlySeries', 'range'], 'series 必须只返回 range/accounts/hourlySeries')
  assert.deepEqual((series.accounts as Array<{ id: string }>).map((account) => account.id), [visibleAccount.id], '不可见 accountId 必须静默省略')
  assert.deepEqual(Object.keys((series.accounts as Array<Record<string, unknown>>)[0] ?? {}).sort(), ['id', 'name', 'providerCode'], '自助 series 的自有账户必须保持最小标签 DTO')
  assert.equal((series.hourlySeries as Array<{ accountId: string }>)[0]?.accountId, visibleAccount.id, 'series 应返回可见账号小时序列')

  const bracketSeries = await getData<Record<string, unknown>>(`${baseUrl}/series?${dateQuery}&accountIds[]=${visibleAccount.id}`, userCookie)
  assert.deepEqual((bracketSeries.accounts as Array<{ id: string }>).map((account) => account.id), [visibleAccount.id], 'series 可兼容 bracket 参数')

  await assertStatus(`${baseUrl}/series?${dateQuery}`, userCookie, 400, 'series 必须至少提供一个 accountIds')
  await assertStatus(`${baseUrl}/series?${dateQuery}&accountIds=`, userCookie, 400, 'series 必须拒绝空 accountIds')
  await assertStatus(`${baseUrl}/series?${dateQuery}&accountIds=${visibleAccount.id},${hiddenAccount.id}`, userCookie, 400, 'series 必须拒绝 CSV')
  const tooMany = Array.from({ length: 21 }, (_, index) => `accountIds=account-${index}`).join('&')
  await assertStatus(`${baseUrl}/series?${dateQuery}&${tooMany}`, userCookie, 400, 'series 必须拒绝 21 个 accountIds')

  assert(getSqliteReadWorkerPoolRuntime().handledJobs >= 3, 'base/series HTTP 回归必须实际经过 SQLite read worker')

  console.log('AI 性能渐进 HTTP 契约回归通过：base/series 窄响应、参数上限和 scope 静默过滤正确')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUserStats(accountId: string): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = new Date().toISOString()
  database.prepare(`
    INSERT INTO usage_rank_snapshots (
      system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at
    ) VALUES (?, 'caller_account', 'last7d', 'request_count', ?, 1, ?, 7, ?)
  `).run(user.id, updatedAt, accountId, updatedAt)
  database.prepare(`
    INSERT INTO usage_stats_hourly (
      system_account_id, scope_type, scope_id, stat_hour, request_count, success_count,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at
    ) VALUES (?, 'caller_account', ?, ?, 7, 7, 700, 7, 120, 210, 7, 40, ?)
  `).run(user.id, accountId, statHour, updatedAt)
  database.prepare(`
    INSERT INTO ai_performance_summary_windows (
      system_account_id, window_key, start_date, end_date, request_count,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at
    ) VALUES (?, ?, ?, ?, 7, 700, 7, 120, 210, 7, 40, ?)
  `).run(user.id, rangeWindowKey(range), statDate, statDate, updatedAt)
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
