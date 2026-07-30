import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-edit-basic-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-edit-basic-projection-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, editBasicRepository, detailRoutes, authRequestContext] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-edit-basic.repository.js'),
  import('../../modules/accounts/account-detail.routes.js'),
  import('../../modules/auth/request-context.js')
])

let server: ReturnType<ReturnType<typeof express>['listen']> | undefined

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '基础编辑窄投影分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = await repositories.createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '基础编辑窄投影账户',
    notes: '仅基础编辑需要的备注',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-edit-basic',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_sse'],
      service_tier_override: 'priority',
      reasoning_effort_override: 'high'
    },
    supportedModels: ['gpt-5.4-mini'],
    healthCheckModel: 'gpt-5.4-mini',
    healthCheckEndpointMode: 'responses_sse',
    tags: ['基础标签'],
    groupId: group.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, access)

  const database = databaseModule.getBusinessDatabase()
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

  let detail: Awaited<ReturnType<typeof editBasicRepository.findAccountEditBasicDetailAsync>>
  try {
    detail = await editBasicRepository.findAccountEditBasicDetailAsync(account.id, access)
  } finally {
    database.prepare = originalPrepare
  }
  assert(detail, '基础编辑投影应返回账户')
  assert.deepEqual(Object.keys(detail).sort(), [
    'boundGroupId',
    'boundGroupName',
    'clientCompatibility',
    'concurrencyLimit',
    'configRevision',
    'credentials',
    'fallbackEnabled',
    'healthCheckEndpointMode',
    'healthCheckModel',
    'id',
    'name',
    'notes',
    'ownerSystemAccountId',
    'priority',
    'protocolCode',
    'protocolVersion',
    'providerCode',
    'providerProtocolProfileId',
    'status',
    'superPriorityEnabled',
    'supportedModels',
    'systemAccountId',
    'tags',
    'type'
  ].sort(), 'edit-basic 必须使用独立字段白名单')
  const expectedEditableCredentialKeys = [
    'api_key',
    'base_url',
    'supported_endpoint_modes'
  ].sort()
  assert.deepEqual(Object.keys(detail.credentials).sort(), expectedEditableCredentialKeys)
  assert.deepEqual(detail.supportedModels, ['gpt-5.4-mini'])
  assert.deepEqual(detail.tags.map((tag) => tag.name), ['基础标签'])
  assert.equal(capturedSql.length, 3, `edit-basic 应固定为主投影、已选模型和标签三条查询，实际 ${capturedSql.length} 条`)
  const sql = capturedSql.join('\n')
  assert.doesNotMatch(sql, /SELECT\s+\*/i)
  assert.match(sql, /account_supported_models/i)
  assert.doesNotMatch(sql, /account_model_mappings/i)
  assert.doesNotMatch(sql, /usage|account_quality|api_key_runtime|balance_snapshot/i)
  assert.doesNotMatch(sql, /permissions|authorization_sources/i)

  const app = express()
  app.use((_req, _res, next) => authRequestContext.withRequestAuthContext({
    systemAccountId: access.systemAccountId,
    username: 'admin',
    displayName: 'Administrator',
    role: access.role,
    mustChangePassword: false,
    sessionId: 'account-edit-basic-projection-session'
  }, next))
  const router = express.Router()
  detailRoutes.registerAccountDetailRoutes(router)
  app.use('/accounts', router)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', 'edit-basic HTTP 回归服务地址不可用')
  const response = await fetch(`http://127.0.0.1:${address.port}/accounts/${account.id}/edit-basic`)
  assert.equal(response.status, 200)
  assert.equal(response.headers.get('cache-control'), 'no-store')
  const payload = await response.json() as { data?: Record<string, unknown> }
  assert(payload.data, 'edit-basic HTTP 响应应包含 data')
  assert.deepEqual(Object.keys(payload.data).sort(), Object.keys(detail).sort(), 'HTTP 层不得在窄投影上重新拼接字段')
  assert.deepEqual(
    Object.keys(payload.data.credentials as Record<string, unknown>).sort(),
    expectedEditableCredentialKeys
  )

  const otherOwner = repositories.createSystemAccount({
    username: 'account_edit_basic_other_owner',
    displayName: '基础编辑其他用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const deniedQueries: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.get = ((...params: SQLInputValue[]) => {
      deniedQueries.push({ sql, params })
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      deniedQueries.push({ sql, params })
      return originalAll(...params)
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare
  let deniedDetail: Awaited<ReturnType<typeof editBasicRepository.findAccountEditBasicDetailAsync>>
  try {
    deniedDetail = await editBasicRepository.findAccountEditBasicDetailAsync(account.id, {
      systemAccountId: otherOwner.id,
      role: 'user'
    })
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(deniedDetail, undefined, '其他用户不能读取账户基础编辑投影')
  assert.equal(deniedQueries.length, 1, '跨 owner 查询必须在主投影定位阶段结束，不得继续查询模型或标签')
  assert.match(
    deniedQueries[0]!.sql,
    /WHERE\s+accounts\.id\s*=\s*\?[\s\S]*AND\s+accounts\.system_account_id\s*=\s*\?/i,
    '包含凭据列的主投影 SQL 必须同时约束账户 owner'
  )
  assert.deepEqual(deniedQueries[0]!.params, [account.id, otherOwner.id], '普通用户 owner 条件必须作为 SQL 绑定参数')

  assert.equal(
    await editBasicRepository.findAccountEditBasicDetailAsync(account.id, {
      systemAccountId: access.systemAccountId,
      role: 'admin',
      systemAccountFilterId: otherOwner.id
    }),
    undefined,
    '管理员显式目标 scope 不能越过 SQL owner 条件读取其他目标账户'
  )
  assert(
    await editBasicRepository.findAccountEditBasicDetailAsync(account.id, {
      systemAccountId: access.systemAccountId,
      role: 'admin',
      systemAccountFilterId: access.systemAccountId
    }),
    '管理员显式选择账户 owner scope 后仍应读取基础编辑投影'
  )

  console.log('AI 账户 edit-basic 投影回归通过：只读取基础表单字段、已选模型和标签，不访问统计、运行态或高级策略')
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
