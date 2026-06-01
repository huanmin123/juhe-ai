import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { UsageStatsRecordRow } from '../../storage/usage-stats-types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-record-ownership-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-record-ownership.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-record-ownership-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAuth },
  { requestContextMiddleware },
  { flushAllOperationLogQueue },
  { usageStatsEntries },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../storage/usage-stats-aggregation.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)

interface ApiEnvelope<T> {
  data: T
}

interface AccountSummary {
  id: string
  name: string
  authorizationInstanceSourceAccountId?: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('授权账户记录归属回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const owner = repositories.createSystemAccount({
    username: 'record_ownership_owner',
    displayName: '记录归属所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'record_ownership_grantee',
    displayName: '记录归属被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '记录归属被授权人分组',
    providerCode: 'openai'
  }, granteeAccess)
  const ownerAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '记录归属来源账户',
    type: 'api_key',
    credentials: { api_key: 'sk-record-ownership', base_url: 'http://127.0.0.1:9/v1' }
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '授权账户记录归属回归'
  }, ownerAccess)

  const granteeAccount = authorizedInstanceForSource(ownerAccount.id, granteeAccess)
  const boundAccount = await postEnvelope<AccountSummary>(
    baseUrl,
    `/__aisys__/api/my-accounts/${granteeAccount.id}/group`,
    sessionCookie(grantee.id),
    { groupId: granteeGroup.id }
  )
  assert.equal(boundAccount.id, granteeAccount.id, '被授权人应能绑定自己的授权实例到账户池分组')

  flushAllOperationLogQueue()

  const granteeLogs = repositories.listOperationLogsForViewer(grantee.id, {
    action: 'bind_group',
    resourceId: granteeAccount.id,
    pageSize: 20
  })
  assert.equal(granteeLogs.items.length, 1, '被授权人应能看到自己绑定授权实例产生的操作日志')
  assert.equal(granteeLogs.items[0]?.operationScopeSystemAccountId, grantee.id, '授权实例本地操作日志 scope 应归被授权人')

  const ownerLogs = repositories.listOperationLogsForViewer(owner.id, {
    action: 'bind_group',
    resourceId: granteeAccount.id,
    pageSize: 20
  })
  assert.equal(ownerLogs.items.length, 0, '资源归属人不应看到被授权人绑定授权实例产生的操作日志')

  const detail = repositories.getOperationLogDetail(granteeLogs.items[0].id)
  assert(detail, '操作日志详情应已落库')
  assert.equal(detail.operationScopeSystemAccountId, grantee.id, '操作日志详情 scope 应保持被授权人')
  assert.equal(
    detail.viewers.some((item) => item.systemAccountId === owner.id),
    false,
    '操作日志 viewer 不应包含来源账户归属人'
  )

  const groupAuthorizedEntries = usageStatsEntries({
    ...usageStatsRecordBase(grantee.id, ownerAccount.id),
    group_id: 'group_owner_resource',
    account_owner_system_account_id: owner.id,
    group_owner_system_account_id: owner.id,
    account_access_type: 'group_authorized',
    group_access_type: 'authorized',
    group_authorization_id: 'auth_group_owner_resource'
  })
  assert(
    groupAuthorizedEntries.some((entry) => entry.systemAccountId === grantee.id && entry.scopeType === 'caller_account' && entry.scopeId === ownerAccount.id),
    '分组授权调用必须写入被授权人的 caller_account 统计'
  )
  assert.equal(
    groupAuthorizedEntries.some((entry) => entry.systemAccountId === owner.id && entry.scopeType === 'account' && entry.scopeId === ownerAccount.id),
    false,
    '分组授权调用不应写入资源归属人的普通账户统计'
  )
  assert.equal(
    groupAuthorizedEntries.some((entry) => entry.systemAccountId === owner.id && entry.scopeType === 'group' && entry.scopeId === 'group_owner_resource'),
    false,
    '分组授权调用不应写入资源归属人的普通分组统计'
  )

  const accountAuthorizedEntries = usageStatsEntries({
    ...usageStatsRecordBase(grantee.id, granteeAccount.id),
    group_id: granteeGroup.id,
    account_owner_system_account_id: owner.id,
    group_owner_system_account_id: grantee.id,
    account_access_type: 'account_authorized',
    group_access_type: 'owner',
    account_authorization_id: 'auth_account_owner_resource'
  })
  assert(
    accountAuthorizedEntries.some((entry) => entry.systemAccountId === grantee.id && entry.scopeType === 'account' && entry.scopeId === granteeAccount.id),
    '账号授权实例的账户统计应归被授权人的实例账户'
  )
  assert.equal(
    accountAuthorizedEntries.some((entry) => entry.systemAccountId === owner.id && entry.scopeType === 'account' && entry.scopeId === granteeAccount.id),
    false,
    '账号授权实例不应把普通账户统计写给来源账户归属人'
  )

  console.log('授权账户本地记录归属回归通过')
} finally {
  await closeServer(server)
  try {
    flushAllOperationLogQueue()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }): AccountSummary {
  const account = repositories.listAccounts(access)
    .find((item: AccountSummary) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

function usageStatsRecordBase(systemAccountId: string, accountId: string): UsageStatsRecordRow {
  return {
    id: `usage_record_ownership_${accountId}`,
    system_account_id: systemAccountId,
    trace_id: `trace_${accountId}`,
    traffic_source: 'gateway',
    client_ip: null,
    api_key_id: 'api_key_record_ownership',
    group_id: null,
    account_id: accountId,
    endpoint: '/v1/responses',
    provider_code: 'openai',
    model: 'gpt-5.5',
    status_code: 200,
    success: 1,
    first_token_ms: 100,
    duration_ms: 200,
    input_tokens: 10,
    output_tokens: 5,
    cache_read_tokens: 0,
    cache_read_cost_usd: 0,
    cost_usd: 0.001,
    error_code: null,
    error_message: null,
    account_owner_system_account_id: systemAccountId,
    group_owner_system_account_id: systemAccountId,
    account_access_type: 'owner',
    group_access_type: 'owner',
    account_authorization_id: null,
    account_authorization_source_type: null,
    account_authorization_source_team_id: null,
    group_authorization_id: null,
    group_authorization_source_type: null,
    group_authorization_source_team_id: null,
    created_at: new Date().toISOString()
  }
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolve, reject) => {
    listeningServer.once('listening', resolve)
    listeningServer.once('error', reject)
  })
}

async function closeServer(listeningServer: ReturnType<typeof app.listen> | undefined): Promise<void> {
  if (!listeningServer) return
  await new Promise<void>((resolve, reject) => {
    listeningServer.close((error) => error ? reject(error) : resolve())
  }).catch(() => undefined)
}
