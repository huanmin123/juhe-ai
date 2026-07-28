import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-advanced-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-advanced-projection-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, advancedRepository, detailRoutes, authRequestContext] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-advanced-detail.repository.js'),
  import('../../modules/accounts/account-detail.routes.js'),
  import('../../modules/auth/request-context.js')
])

let server: ReturnType<ReturnType<typeof express>['listen']> | undefined

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '高级编辑窄投影分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const proxy = repositories.createProxy({
    name: '高级编辑窄投影代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_088,
    enabled: true
  }, access)
  const account = await repositories.createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '高级编辑窄投影账户',
    notes: '仅高级编辑需要的备注',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-advanced',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_sse'],
      service_tier_override: 'priority',
      reasoning_effort_override: 'high',
      codex_responses_safe_repair_enabled: true
    },
    supportedModels: ['gpt-5.4-mini'],
    modelMappings: [],
    healthCheckModel: 'gpt-5.4-mini',
    healthCheckEndpointMode: 'responses_sse',
    tags: ['高级标签'],
    proxyProfileId: proxy.id,
    groupId: group.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, access)

  const database = databaseModule.getBusinessDatabase()
  database.prepare(`
    UPDATE accounts
    SET availability_schedule_json = ?,
        account_expires_at = ?,
        temporary_unavailable_continuous_probe_enabled = 0,
        balance_query_enabled = 1,
        balance_query_config_json = ?
    WHERE id = ?
  `).run(
    JSON.stringify({
      enabled: true,
      timezone: 'Asia/Shanghai',
      mode: 'allow_windows',
      windows: [{ daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '23:59' }]
    }),
    '2027-01-01T00:00:00.000Z',
    JSON.stringify({ adapter: 'builtin', intervalMinutes: 10 }),
    account.id
  )

  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedSql: string[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.get = ((...params: SQLInputValue[]) => {
      capturedSql.push(sql)
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      capturedSql.push(sql)
      return originalAll(...params)
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare

  let detail: Awaited<ReturnType<typeof advancedRepository.findAccountAdvancedDetailAsync>>
  try {
    detail = await advancedRepository.findAccountAdvancedDetailAsync(account.id, access)
  } finally {
    database.prepare = originalPrepare
  }
  assert(detail, '高级编辑投影应返回账户')
  const expectedKeys = [
    'accessType',
    'accountExpiresAt',
    'availabilitySchedule',
    'balanceQueryConfig',
    'balanceQueryEnabled',
    'configRevision',
    'credentials',
    'id',
    'modelMappings',
    'proxyProfileId',
    'temporaryUnavailableContinuousProbeEnabled'
  ].sort()
  assert.deepEqual(Object.keys(detail).sort(), expectedKeys, 'advanced 必须使用独立字段白名单')
  assert.deepEqual(detail.modelMappings, [])
  assert.deepEqual(Object.keys(detail.credentials ?? {}).sort(), [
    'codex_responses_safe_repair_enabled',
    'codex_responses_strict_intercept_enabled',
    'reasoning_effort_override',
    'service_tier_override'
  ])
  assert.equal(detail.proxyProfileId, proxy.id)
  assert.equal(detail.balanceQueryConfig?.intervalMinutes, 10)
  assert.equal(detail.temporaryUnavailableContinuousProbeEnabled, false)
  assert.equal(capturedSql.length, 2, `advanced 应固定为主投影与模型映射两条查询，实际 ${capturedSql.length} 条`)
  const sql = capturedSql.join('\n')
  assert.doesNotMatch(sql, /SELECT\s+\*/i)
  assert.match(sql, /account_model_mappings/i)
  assert.doesNotMatch(sql, /account_supported_models|account_tag_bindings/i)
  assert.doesNotMatch(sql, /\b(?:usage|today_usage|account_quality|api_key_runtime|balance_snapshot)\b/i)
  assert.doesNotMatch(sql, /permissions|authorization_sources/i)

  const app = express()
  app.use((_req, _res, next) => authRequestContext.withRequestAuthContext({
    systemAccountId: access.systemAccountId,
    username: 'admin',
    displayName: 'Administrator',
    role: access.role,
    mustChangePassword: false,
    sessionId: 'account-advanced-projection-session'
  }, next))
  const router = express.Router()
  detailRoutes.registerAccountDetailRoutes(router)
  app.use('/accounts', router)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', 'advanced HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`
  const response = await fetch(`${baseUrl}/accounts/${account.id}/advanced`)
  assert.equal(response.status, 200)
  assert.equal(response.headers.get('cache-control'), 'no-store')
  const payload = await response.json() as { data?: Record<string, unknown> }
  assert(payload.data, 'advanced HTTP 响应应包含 data')
  assert.deepEqual(Object.keys(payload.data).sort(), expectedKeys, 'HTTP 层不得在高级编辑投影上重新拼接宽字段')

  const otherOwner = repositories.createSystemAccount({
    username: 'account_advanced_other_owner',
    displayName: '高级编辑其他用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  assert.equal(
    await advancedRepository.findAccountAdvancedDetailAsync(account.id, { systemAccountId: otherOwner.id, role: 'user' }),
    undefined,
    '其他用户不能读取账户高级编辑投影'
  )

  const grantee = repositories.createSystemAccount({
    username: 'account_advanced_grantee',
    displayName: '高级编辑授权用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '高级编辑授权目标分组',
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '高级编辑窄投影授权实例边界'
  }, access)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === account.id)
  assert(authorizedInstance, '账户授权应创建被授权者作用域内的实例账户')
  const authorizedDetail = await advancedRepository.findAccountAdvancedDetailAsync(authorizedInstance.id, access)
  assert(authorizedDetail, '授权实例应返回独立的只读高级投影')
  assert.equal(authorizedDetail.accessType, 'authorized')
  assert.equal(authorizedDetail.credentials, undefined, '授权实例高级投影不得读取或返回来源账户凭据')
  assert.equal(authorizedDetail.authorizationInstanceSourceAccountStatus, 'active')
  assert.equal(authorizedDetail.authorizationInstanceSourceAccountSchedulable, true)
  assert.equal(authorizedDetail.accountExpiresAt, '2027-01-01T00:00:00.000Z')
  assert(!JSON.stringify(authorizedDetail).includes('sk-account-advanced'), '授权实例高级投影不得泄露来源账户 API Key')
  const authorizedResponse = await fetch(`${baseUrl}/accounts/${authorizedInstance.id}/advanced`)
  assert.equal(authorizedResponse.status, 200, '授权实例高级编辑应返回只读投影')
  const authorizedPayload = await authorizedResponse.json() as { data?: Record<string, unknown> }
  assert.equal(authorizedPayload.data?.accessType, 'authorized')
  assert(!JSON.stringify(authorizedPayload).includes('sk-account-advanced'), '授权实例 HTTP 响应不得泄露来源凭据')

  console.log('AI 账户 advanced 窄投影回归通过：自有账户与授权实例只读取各自表单所需字段')
} finally {
  await closeServer(server)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function onceListening(target: NonNullable<typeof server>): Promise<void> {
  if (target.listening) return
  await new Promise<void>((resolvePromise, reject) => {
    target.once('listening', resolvePromise)
    target.once('error', reject)
  })
}

async function closeServer(target: typeof server): Promise<void> {
  if (!target) return
  await new Promise<void>((resolvePromise) => target.close(() => resolvePromise()))
}
