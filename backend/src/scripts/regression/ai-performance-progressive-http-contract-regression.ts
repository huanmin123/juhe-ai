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
const directAuthorizedSource = repositories.createAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: providerProfile.id,
  name: 'AI 性能直接授权来源账号',
  type: 'api_key',
  credentials: { api_key: 'sk-ai-performance-direct-source', base_url: 'https://api.openai.com/v1' },
  supportedModels: ['gpt-5.5'],
  groupId: adminGroup.id
}, adminAccess)
repositories.createResourceAuthorization({
  resourceType: 'account',
  resourceId: directAuthorizedSource.id,
  granteeType: 'system_account',
  granteeId: user.id,
  targetGroupId: userGroup.id,
  remark: 'AI 性能 HTTP 直接授权回归'
}, adminAccess)
const directAuthorizedInstance = repositories.listAccounts(userAccess)
  .find((account) => account.authorizationInstanceSourceAccountId === directAuthorizedSource.id)
assert(directAuthorizedInstance?.id, 'AI 性能 HTTP 回归需要直接授权实例账户')

const groupAuthorizedGroup = repositories.createGroup({ name: 'AI 性能 HTTP 分组授权', providerCode: 'gpt' }, adminAccess)
const groupAuthorizedSource = repositories.createAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: providerProfile.id,
  name: 'AI 性能分组授权来源账号',
  type: 'api_key',
  credentials: { api_key: 'sk-ai-performance-group-source', base_url: 'https://api.openai.com/v1' },
  supportedModels: ['gpt-5.5'],
  groupId: groupAuthorizedGroup.id
}, adminAccess)
repositories.createResourceAuthorization({
  resourceType: 'group',
  resourceId: groupAuthorizedGroup.id,
  granteeType: 'system_account',
  granteeId: user.id,
  remark: 'AI 性能 HTTP 分组授权回归'
}, adminAccess)

const revokedGroup = repositories.createGroup({ name: 'AI 性能 HTTP 已撤销分组', providerCode: 'gpt' }, adminAccess)
const revokedGroupSource = repositories.createAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: providerProfile.id,
  name: 'AI 性能已撤销授权账号',
  type: 'api_key',
  credentials: { api_key: 'sk-ai-performance-revoked-source', base_url: 'https://api.openai.com/v1' },
  supportedModels: ['gpt-5.5'],
  groupId: revokedGroup.id
}, adminAccess)
const revokedAuthorization = repositories.createResourceAuthorization({
  resourceType: 'group',
  resourceId: revokedGroup.id,
  granteeType: 'system_account',
  granteeId: user.id,
  remark: 'AI 性能 HTTP 已撤销授权回归'
}, adminAccess)
repositories.revokeResourceAuthorization(revokedAuthorization.id, adminAccess)

const expiredGroup = repositories.createGroup({ name: 'AI 性能 HTTP 已过期分组', providerCode: 'gpt' }, adminAccess)
const expiredGroupSource = repositories.createAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: providerProfile.id,
  name: 'AI 性能已过期授权账号',
  type: 'api_key',
  credentials: { api_key: 'sk-ai-performance-expired-source', base_url: 'https://api.openai.com/v1' },
  supportedModels: ['gpt-5.5'],
  groupId: expiredGroup.id
}, adminAccess)
const expiredAuthorization = repositories.createResourceAuthorization({
  resourceType: 'group',
  resourceId: expiredGroup.id,
  granteeType: 'system_account',
  granteeId: user.id,
  expiresAt: new Date(Date.now() + 60_000).toISOString(),
  remark: 'AI 性能 HTTP 已过期授权回归'
}, adminAccess)
repositories.updateResourceAuthorization(expiredAuthorization.id, {
  status: 'expired',
  expiresAt: new Date(Date.now() - 60_000).toISOString()
}, adminAccess)

seedBaseStats(user.id, 'caller_account', visibleAccount.id, 7)
seedBaseStats('global', 'account', hiddenAccount.id, 23)
seedHourlyStats(user.id, 'caller_account', directAuthorizedInstance.id, 11)
seedHourlyStats(user.id, 'caller_account', groupAuthorizedSource.id, 13)
seedHourlyStats(user.id, 'caller_account', revokedGroupSource.id, 17)
seedHourlyStats(user.id, 'caller_account', expiredGroupSource.id, 19)
seedHourlyStats('global', 'account', groupAuthorizedSource.id, 913)
seedHourlyStats('sys_admin', 'caller_account', groupAuthorizedSource.id, 813)

const userCookie = sessionCookie(user.id)
const adminCookie = sessionCookie('sys_admin')
let server: Server | undefined
try {
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('HTTP 回归服务地址不可用')
  const apiBaseUrl = `http://127.0.0.1:${address.port}/__aisys__/api`
  const baseUrl = `${apiBaseUrl}/my-stats/ai-performance`
  const adminBaseUrl = `${apiBaseUrl}/stats/ai-performance`
  const dateQuery = `startDate=${statDate}&endDate=${statDate}`

  const base = await getData<Record<string, unknown>>(`${baseUrl}?${dateQuery}`, userCookie)
  assert.deepEqual(Object.keys(base).sort(), ['accounts', 'hourlySeries', 'range', 'summary'], 'base 必须只返回 range/summary/accounts/hourlySeries')
  assertExactRangeDto(base.range)
  assertExactSummaryDto(base.summary)
  const baseAccounts = base.accounts as Array<{ id: string }>
  assert(baseAccounts.length <= 10, 'base 默认账号不能超过 10 个')
  assert.deepEqual(baseAccounts.map((account) => account.id), [visibleAccount.id], 'base 应返回当前 scope 默认排行账号')
  assert.deepEqual(Object.keys(baseAccounts[0] ?? {}).sort(), ['id', 'name', 'providerCode'], '自助 base 的自有账户不得返回管理端所属用户或授权标签字段')
  assertExactSeriesDto((base.hourlySeries as Array<Record<string, unknown>>)[0])

  const forgedSelfScope = await getData<Record<string, unknown>>(`${baseUrl}?${dateQuery}&systemAccountId=sys_admin`, userCookie)
  assert.deepEqual((forgedSelfScope.accounts as Array<{ id: string }>).map((account) => account.id), [visibleAccount.id], 'my-stats 必须忽略伪造 systemAccountId 并保持当前用户作用域')
  await assertStatus(`${adminBaseUrl}?${dateQuery}`, userCookie, 403, '普通用户不能访问管理侧 AI 性能 base')

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
  assertExactSeriesDto((series.hourlySeries as Array<Record<string, unknown>>)[0])

  const authorizedSeries = await getData<Record<string, unknown>>(
    `${baseUrl}/series?${dateQuery}`
      + `&accountIds=${directAuthorizedInstance.id}`
      + `&accountIds=${groupAuthorizedSource.id}`
      + `&accountIds=${revokedGroupSource.id}`
      + `&accountIds=${expiredGroupSource.id}`
      + `&accountIds=${hiddenAccount.id}`,
    userCookie
  )
  const authorizedAccounts = authorizedSeries.accounts as Array<Record<string, unknown> & { id: string }>
  assert.deepEqual(
    authorizedAccounts.map((account) => account.id),
    [directAuthorizedInstance.id, groupAuthorizedSource.id],
    'series 必须只保留当前用户有效的直接授权实例与分组授权来源账户'
  )
  for (const account of authorizedAccounts) {
    assert.deepEqual(
      Object.keys(account).sort(),
      ['accessType', 'id', 'name', 'ownerSystemAccountName', 'providerCode'],
      '用户侧授权账户必须只返回渲染标签所需字段'
    )
  }
  const authorizedHourlySeries = authorizedSeries.hourlySeries as Array<Record<string, unknown> & { accountId: string; points: Array<{ requestCount: number }> }>
  assert.deepEqual(authorizedHourlySeries.map((item) => item.accountId), [directAuthorizedInstance.id, groupAuthorizedSource.id])
  assert.equal(authorizedHourlySeries[0]?.points[0]?.requestCount, 11, '直接授权实例必须读取当前调用方 caller_account 数据')
  assert.equal(authorizedHourlySeries[1]?.points[0]?.requestCount, 13, '分组授权来源必须读取当前调用方 caller_account 数据，不能混入 owner/global 统计')
  for (const item of authorizedHourlySeries) assertExactSeriesDto(item)

  const directOptions = await getData<Array<Record<string, unknown> & { id: string }>>(
    `${baseUrl}/accounts?keyword=${encodeURIComponent(directAuthorizedSource.name)}&limit=10&systemAccountId=sys_admin`,
    userCookie
  )
  assert.deepEqual(directOptions.map((item) => item.id), [directAuthorizedInstance.id], '用户侧 options 必须忽略伪造 owner 并按来源当前名称返回授权实例')
  assert.deepEqual(Object.keys(directOptions[0] ?? {}).sort(), ['accessType', 'id', 'name', 'ownerSystemAccountName', 'providerCode'], '用户侧授权 options 必须保持精确 DTO')
  const groupOptions = await getData<Array<Record<string, unknown> & { id: string }>>(
    `${baseUrl}/accounts?keyword=${encodeURIComponent(groupAuthorizedSource.name)}&limit=10`,
    userCookie
  )
  assert.deepEqual(groupOptions.map((item) => item.id), [groupAuthorizedSource.id], '用户侧 options 必须返回有效分组授权来源账户')
  assert.deepEqual(Object.keys(groupOptions[0] ?? {}).sort(), ['accessType', 'id', 'name', 'ownerSystemAccountName', 'providerCode'], '分组授权 options 必须保持精确 DTO')
  assert.deepEqual(await getData(`${baseUrl}/accounts?keyword=${encodeURIComponent(revokedGroupSource.name)}&limit=10`, userCookie), [], '已撤销分组授权不能继续出现在 options')
  assert.deepEqual(await getData(`${baseUrl}/accounts?keyword=${encodeURIComponent(expiredGroupSource.name)}&limit=10`, userCookie), [], '已过期分组授权不能继续出现在 options')
  await assertStatus(`${adminBaseUrl}/accounts?keyword=AI`, userCookie, 403, '普通用户不能访问管理侧 AI 性能 options')

  const adminGlobalBase = await getData<Record<string, unknown>>(`${adminBaseUrl}?${dateQuery}`, adminCookie)
  assert.deepEqual((adminGlobalBase.accounts as Array<{ id: string }>).map((account) => account.id), [hiddenAccount.id], '管理员未筛选用户时必须读取 global account 排行')
  assert.deepEqual(Object.keys((adminGlobalBase.accounts as Array<Record<string, unknown>>)[0] ?? {}).sort(), ['id', 'name', 'providerCode', 'systemAccountName'], '管理员全局 base 必须只额外返回所属用户展示名')
  assertExactRangeDto(adminGlobalBase.range)
  assertExactSummaryDto(adminGlobalBase.summary)
  assertExactSeriesDto((adminGlobalBase.hourlySeries as Array<Record<string, unknown>>)[0])

  const adminScopedBase = await getData<Record<string, unknown>>(`${adminBaseUrl}?${dateQuery}&systemAccountId=${user.id}`, adminCookie)
  assert.deepEqual((adminScopedBase.accounts as Array<{ id: string }>).map((account) => account.id), [visibleAccount.id], '管理员指定用户时必须读取该用户 caller_account 排行')
  const adminScopedSeries = await getData<Record<string, unknown>>(
    `${adminBaseUrl}/series?${dateQuery}&systemAccountId=${user.id}&accountIds=${groupAuthorizedSource.id}&accountIds=${hiddenAccount.id}`,
    adminCookie
  )
  assert.deepEqual((adminScopedSeries.accounts as Array<{ id: string }>).map((account) => account.id), [groupAuthorizedSource.id], '管理员指定用户 series 不能穿透到其他 owner 不可见账户')
  assert.equal((adminScopedSeries.hourlySeries as Array<{ points: Array<{ requestCount: number }> }>)[0]?.points[0]?.requestCount, 13, '管理员指定用户 series 必须读取该调用方 caller_account 数据')
  assert.deepEqual(
    Object.keys((adminScopedSeries.accounts as Array<Record<string, unknown>>)[0] ?? {}).sort(),
    ['accessType', 'id', 'name', 'ownerSystemAccountName', 'providerCode', 'systemAccountName'],
    '管理员指定用户的授权账户 DTO 只能增加所属用户展示字段'
  )

  const adminOptions = await getData<Array<Record<string, unknown> & { id: string }>>(
    `${adminBaseUrl}/accounts?keyword=${encodeURIComponent(hiddenAccount.name)}&limit=10`,
    adminCookie
  )
  assert.deepEqual(adminOptions.map((item) => item.id), [hiddenAccount.id], '管理员全局 options 必须能够查询全局账户')
  assert.deepEqual(Object.keys(adminOptions[0] ?? {}).sort(), ['id', 'name', 'providerCode', 'systemAccountName'], '管理员全局 options 必须保持精确展示 DTO')

  const bracketSeries = await getData<Record<string, unknown>>(`${baseUrl}/series?${dateQuery}&accountIds[]=${visibleAccount.id}`, userCookie)
  assert.deepEqual((bracketSeries.accounts as Array<{ id: string }>).map((account) => account.id), [visibleAccount.id], 'series 可兼容 bracket 参数')

  await assertStatus(`${baseUrl}/series?${dateQuery}`, userCookie, 400, 'series 必须至少提供一个 accountIds')
  await assertStatus(`${baseUrl}/series?${dateQuery}&accountIds=`, userCookie, 400, 'series 必须拒绝空 accountIds')
  await assertStatus(`${baseUrl}/series?${dateQuery}&accountIds=${visibleAccount.id},${hiddenAccount.id}`, userCookie, 400, 'series 必须拒绝 CSV')
  const tooMany = Array.from({ length: 21 }, (_, index) => `accountIds=account-${index}`).join('&')
  await assertStatus(`${baseUrl}/series?${dateQuery}&${tooMany}`, userCookie, 400, 'series 必须拒绝 21 个 accountIds')

  assert(getSqliteReadWorkerPoolRuntime().handledJobs >= 3, 'base/series HTTP 回归必须实际经过 SQLite read worker')

  console.log('AI 性能渐进 HTTP 契约回归通过：base/series/options 精确 DTO、授权租户隔离和管理/个人权限矩阵正确')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedBaseStats(systemAccountId: string, scopeType: 'account' | 'caller_account', accountId: string, requestCount: number): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = new Date().toISOString()
  database.prepare(`
    INSERT INTO usage_rank_snapshots (
      system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at
    ) VALUES (?, ?, 'last7d', 'request_count', ?, 1, ?, ?, ?)
  `).run(systemAccountId, scopeType, updatedAt, accountId, requestCount, updatedAt)
  seedHourlyStats(systemAccountId, scopeType, accountId, requestCount, updatedAt)
  database.prepare(`
    INSERT INTO ai_performance_summary_windows (
      system_account_id, window_key, start_date, end_date, request_count,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 120, ?, ?, 40, ?)
  `).run(
    systemAccountId,
    rangeWindowKey(range),
    statDate,
    statDate,
    requestCount,
    requestCount * 100,
    requestCount,
    requestCount * 30,
    requestCount,
    updatedAt
  )
}

function seedHourlyStats(
  systemAccountId: string,
  scopeType: 'account' | 'caller_account',
  accountId: string,
  requestCount: number,
  updatedAt = new Date().toISOString()
): void {
  const database = databaseModule.getStatsDatabase()
  database.prepare(`
    INSERT INTO usage_stats_hourly (
      system_account_id, scope_type, scope_id, stat_hour, request_count, success_count,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 120, ?, ?, 40, ?)
  `).run(
    systemAccountId,
    scopeType,
    accountId,
    statHour,
    requestCount,
    requestCount,
    requestCount * 100,
    requestCount,
    requestCount * 30,
    requestCount,
    updatedAt
  )
}

function assertExactRangeDto(value: unknown): void {
  assert.deepEqual(Object.keys(value as Record<string, unknown>).sort(), ['days', 'endDate', 'maxDays', 'startDate'], 'range 必须保持精确 DTO')
}

function assertExactSummaryDto(value: unknown): void {
  assert.deepEqual(
    Object.keys(value as Record<string, unknown>).sort(),
    ['averageDurationMs', 'averageFirstTokenMs', 'maxDurationMs', 'maxFirstTokenMs', 'requestCount'],
    'summary 必须保持精确 DTO'
  )
}

function assertExactSeriesDto(value: Record<string, unknown> | undefined): void {
  assert(value, '小时序列必须存在')
  assert.deepEqual(Object.keys(value).sort(), ['accountId', 'accountName', 'points', 'providerCode'], '小时 series 必须保持精确 DTO')
  const points = value.points as Array<Record<string, unknown>>
  assert.deepEqual(
    Object.keys(points[0] ?? {}).sort(),
    ['averageDurationMs', 'averageFirstTokenMs', 'maxDurationMs', 'maxFirstTokenMs', 'requestCount', 'statHour'],
    '小时 point 必须保持精确 DTO'
  )
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
