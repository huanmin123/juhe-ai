import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-management-patch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-management-patch-regression-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  patchRepository,
  { decryptJson },
  { accountsRouter },
  authRequestContext,
  { registerGatewayRuntimeCacheInvalidator }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-management-patch.repository.js'),
  import('../../storage/crypto.js'),
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/request-context.js'),
  import('../../shared/gateway-cache-invalidation.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
let server: ReturnType<ReturnType<typeof express>['listen']> | undefined

try {
  assertSourceBoundaries()

  const sourceGroup = repositories.createGroup({
    providerCode: 'gpt',
    name: 'PATCH 源分组',
    enabled: true
  }, access)
  const targetGroup = repositories.createGroup({
    providerCode: 'gpt',
    name: 'PATCH 目标分组',
    enabled: true
  }, access)
  const rollbackGroup = repositories.createGroup({
    providerCode: 'gpt',
    name: 'PATCH 回滚分组',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '按需 PATCH 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-management-patch',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    groupId: sourceGroup.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, access)

  const noOp = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(account.id, {
    expectedConfigRevision: 1,
    name: account.name,
    groupId: sourceGroup.id,
    supportedModels: ['gpt-5.5'],
    modelMappings: [],
    tags: [],
    balanceQueryEnabled: false
  }, access))
  assert(noOp.result, '无变化 PATCH 应返回当前版本')
  assert.equal(noOp.result.configRevision, 1)
  assert.deepEqual(noOp.result.changedFields, [])
  assert.deepEqual(noOp.dml, [], '无变化 PATCH 不得执行任何 DML')

  const notesPatch = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(account.id, {
    expectedConfigRevision: 1,
    notes: '仅修改备注'
  }, access))
  assert(notesPatch.result)
  assert.equal(notesPatch.result.configRevision, 2)
  assert.deepEqual(notesPatch.result.changedFields, ['notes'])
  const notesUpdate = requiredAccountUpdate(notesPatch.dml)
  assert.match(notesUpdate, /"notes"\s*=\s*\?/i)
  assert.match(notesUpdate, /config_revision\s*=\s*config_revision\s*\+\s*1/i)
  assert.match(notesUpdate, /config_revision\s*=\s*\?/i, '主表更新必须带 expectedConfigRevision CAS')
  assert.doesNotMatch(notesUpdate, /credentials_encrypted|status\s*=|name\s*=|concurrency_limit\s*=/i)
  assert.equal(relationDml(notesPatch.dml).length, 0, '备注 PATCH 不得重写模型、标签或分组关系')
  assert.doesNotMatch(notesPatch.sql.join('\n'), /\bcredentials_encrypted\b/i, '备注 PATCH 不得读取或解密账户凭据')
  assert.equal(notesPatch.result.healthCheckRequired, false, '备注 PATCH 不得触发健康检查')

  const stale = await captureBusinessDml(() => assert.rejects(
    patchRepository.patchAccountManagementAsync(account.id, {
      expectedConfigRevision: 1,
      notes: '过期版本不得落库'
    }, access),
    patchRepository.AccountManagementPatchRevisionConflictError
  ))
  assert.deepEqual(stale.dml, [], '版本冲突必须在 DML 前终止')
  assert.equal(accountRow(account.id).notes, '仅修改备注')
  assert.equal(accountRow(account.id).config_revision, 2)

  const groupPatch = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(account.id, {
    expectedConfigRevision: 2,
    groupId: targetGroup.id
  }, access))
  assert(groupPatch.result)
  assert.equal(groupPatch.result.configRevision, 3)
  assert.deepEqual(groupPatch.result.changedFields, ['groupId'])
  const groupAccountUpdate = requiredAccountUpdate(groupPatch.dml)
  assert.doesNotMatch(groupAccountUpdate, /"(?:name|notes|status|credentials_encrypted)"\s*=/i)
  assert(groupPatch.dml.some((sql) => /^DELETE FROM "?group_accounts"?/i.test(sql)))
  assert(groupPatch.dml.some((sql) => /^INSERT INTO "?group_accounts"?/i.test(sql)))
  assert.equal(activeGroupId(account.id), targetGroup.id, '分组迁移必须提交最终唯一启用绑定')

  const database = databaseModule.getBusinessDatabase()
  database.exec(`
    CREATE TRIGGER account_management_patch_group_failure
    BEFORE INSERT ON group_accounts
    WHEN NEW.group_id = '${rollbackGroup.id.replace(/'/g, "''")}'
    BEGIN
      SELECT RAISE(ABORT, 'forced account group binding failure');
    END
  `)
  try {
    await assert.rejects(
      patchRepository.patchAccountManagementAsync(account.id, {
        expectedConfigRevision: 3,
        groupId: rollbackGroup.id
      }, access),
      /forced account group binding failure/
    )
  } finally {
    database.exec('DROP TRIGGER account_management_patch_group_failure')
  }
  assert.equal(accountRow(account.id).config_revision, 3, '分组关系写入失败必须回滚账户 revision')
  assert.equal(activeGroupId(account.id), targetGroup.id, '分组关系写入失败必须恢复原绑定')

  const tagPatch = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(account.id, {
    expectedConfigRevision: 3,
    tags: ['主力', '生产']
  }, access))
  assert(tagPatch.result)
  assert.equal(tagPatch.result.configRevision, 4)
  assert.deepEqual(tagPatch.result.changedFields, ['tags'])
  assert(tagPatch.dml.some((sql) => /account_tag_bindings/i.test(sql)), '标签实际变化必须更新关系表')

  const sameTags = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(account.id, {
    expectedConfigRevision: 4,
    tags: ['生产', '主力']
  }, access))
  assert(sameTags.result)
  assert.equal(sameTags.result.configRevision, 4)
  assert.deepEqual(sameTags.result.changedFields, [])
  assert.deepEqual(sameTags.dml, [], '仅标签顺序不同不得重写关系表或推进版本')

  const oauthAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'OAuth PATCH 保留账户',
    type: 'oauth',
    credentials: {
      refresh_token: 'oauth-refresh-preserved',
      client_id: 'oauth-client-preserved',
      id_token: 'oauth-id-token-preserved',
      email: 'owner@example.com',
      base_url: 'https://api.openai.com/v1',
      service_tier_override: 'default'
    },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    groupId: targetGroup.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, access)

  const oauthPatch = await patchRepository.patchAccountManagementAsync(oauthAccount.id, {
    expectedConfigRevision: 1,
    credentialsPatch: { service_tier_override: 'priority' }
  }, access)
  assert(oauthPatch)
  assert.equal(oauthPatch.configRevision, 2)
  assert.deepEqual(oauthPatch.changedFields, ['credentials.service_tier_override'])
  assertPreservedOAuthCredentials(oauthAccount.id, 'priority')

  const oauthClear = await patchRepository.patchAccountManagementAsync(oauthAccount.id, {
    expectedConfigRevision: 2,
    credentialsPatch: { service_tier_override: null }
  }, access)
  assert(oauthClear)
  assert.equal(oauthClear.configRevision, 3)
  assert.deepEqual(oauthClear.changedFields, ['credentials.service_tier_override'])
  assertPreservedOAuthCredentials(oauthAccount.id, undefined)

  const oauthNoOp = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(oauthAccount.id, {
    expectedConfigRevision: 3,
    credentialsPatch: { service_tier_override: null }
  }, access))
  assert(oauthNoOp.result)
  assert.equal(oauthNoOp.result.configRevision, 3)
  assert.deepEqual(oauthNoOp.result.changedFields, [])
  assert.deepEqual(oauthNoOp.dml, [], '重复清除 OAuth 可编辑字段不得覆盖凭据或推进版本')

  database.prepare(`
    UPDATE accounts
    SET cooldown_until = ?,
        last_error_code = 'stale_runtime',
        last_error_message = '待归一化运行态'
    WHERE id = ?
  `).run(new Date(Date.now() + 60_000).toISOString(), oauthAccount.id)
  const gatewayInvalidationReasons: string[] = []
  const unregisterGatewayInvalidation = registerGatewayRuntimeCacheInvalidator((reason) => {
    gatewayInvalidationReasons.push(reason)
  })
  const runtimeNormalization = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(oauthAccount.id, {
    expectedConfigRevision: 3,
    status: 'active'
  }, access)).finally(unregisterGatewayInvalidation)
  assert(runtimeNormalization.result)
  assert.equal(runtimeNormalization.result.configRevision, 4)
  assert.deepEqual(runtimeNormalization.result.changedFields, ['runtimeState'])
  assert.equal(runtimeNormalization.result.healthCheckRequired, false)
  const runtimeUpdate = requiredAccountUpdate(runtimeNormalization.dml)
  assert.match(runtimeUpdate, /"cooldown_until"\s*=\s*\?/i)
  assert.match(runtimeUpdate, /"last_error_code"\s*=\s*\?/i)
  assert.match(runtimeUpdate, /"last_error_message"\s*=\s*\?/i)
  assert.doesNotMatch(runtimeUpdate, /"status"\s*=\s*\?/i, '同状态 PATCH 不得重写 status')
  assert.equal(relationDml(runtimeNormalization.dml).length, 0, '运行态归一化不得重写关系表')
  assert(
    runtimeNormalization.dml.some((sql) => /^INSERT INTO "?group_account_stats_dirty"?/i.test(sql)),
    '运行态归一化必须标记分组统计过期'
  )
  assert.deepEqual(gatewayInvalidationReasons, ['account_management_patch'], '运行态归一化必须失效网关运行时')
  const normalizedRuntimeRow = database.prepare(`
    SELECT cooldown_until, last_error_code, last_error_message, config_revision
    FROM accounts
    WHERE id = ?
  `).get(oauthAccount.id) as unknown as {
    cooldown_until: string | null
    last_error_code: string | null
    last_error_message: string | null
    config_revision: number
  }
  assert.equal(normalizedRuntimeRow.cooldown_until, null)
  assert.equal(normalizedRuntimeRow.last_error_code, null)
  assert.equal(normalizedRuntimeRow.last_error_message, null)
  assert.equal(normalizedRuntimeRow.config_revision, 4)

  const app = express()
  app.use(express.json())
  app.use((_req, _res, next) => authRequestContext.withRequestAuthContext({
    systemAccountId: access.systemAccountId,
    username: 'admin',
    displayName: 'Administrator',
    role: access.role,
    mustChangePassword: false,
    sessionId: 'account-management-patch-session'
  }, next))
  app.use('/accounts', accountsRouter)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', 'PATCH HTTP 回归服务地址不可用')
  const response = await fetch(`http://127.0.0.1:${address.port}/accounts/${account.id}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      expectedConfigRevision: 4,
      notes: 'HTTP 最小响应验证'
    })
  })
  assert.equal(response.status, 200)
  const payload = await response.json() as {
    data?: { id?: string; configRevision?: number; changedFields?: string[] }
  }
  assert(payload.data)
  assert.deepEqual(Object.keys(payload.data).sort(), ['changedFields', 'configRevision', 'id'])
  assert.equal(payload.data.id, account.id)
  assert.equal(payload.data.configRevision, 5)
  assert.deepEqual(payload.data.changedFields, ['notes'])

  const staleHttp = await captureBusinessDml(() => fetch(`http://127.0.0.1:${address.port}/accounts/${account.id}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ expectedConfigRevision: 4, notes: 'HTTP 过期写入' })
  }))
  assert.equal(staleHttp.result.status, 409)
  assert.deepEqual(staleHttp.dml, [], 'HTTP 过期 revision 必须零 DML')

  const noOpHttp = await captureBusinessDml(() => fetch(`http://127.0.0.1:${address.port}/accounts/${account.id}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ expectedConfigRevision: 5, notes: 'HTTP 最小响应验证' })
  }))
  assert.equal(noOpHttp.result.status, 200)
  const noOpPayload = await noOpHttp.result.json() as {
    data?: { id?: string; configRevision?: number; changedFields?: string[] }
  }
  assert.deepEqual(noOpPayload.data, {
    id: account.id,
    configRevision: 5,
    changedFields: []
  })
  assert.deepEqual(noOpHttp.dml, [], 'HTTP no-op 不得写账户、关系或审计业务表')

  const authorizationOwner = repositories.createSystemAccount({
    username: 'patchauthowner',
    displayName: '授权 PATCH 来源用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const authorizationGrantee = repositories.createSystemAccount({
    username: 'patchauthgrantee',
    displayName: '授权 PATCH 被授权用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const authorizationOwnerAccess = { systemAccountId: authorizationOwner.id, role: 'user' as const }
  const authorizationGranteeAccess = { systemAccountId: authorizationGrantee.id, role: 'user' as const }
  const authorizationOwnerGroup = repositories.createGroup({
    providerCode: 'gpt',
    name: '授权 PATCH 来源分组',
    enabled: true
  }, authorizationOwnerAccess)
  const authorizationInitialGroup = repositories.createGroup({
    providerCode: 'gpt',
    name: '授权 PATCH 初始分组',
    enabled: true
  }, authorizationGranteeAccess)
  const authorizationTargetGroup = repositories.createGroup({
    providerCode: 'gpt',
    name: '授权 PATCH 目标分组',
    enabled: true
  }, authorizationGranteeAccess)
  const authorizationSourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '授权 PATCH 来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-authorized-local-patch',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    groupId: authorizationOwnerGroup.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, authorizationOwnerAccess)
  const authorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: authorizationSourceAccount.id,
    granteeType: 'system_account',
    granteeId: authorizationGrantee.id,
    targetGroupId: authorizationInitialGroup.id,
    remark: '授权账户本地字段 PATCH 回归'
  }, authorizationOwnerAccess)
  const authorizationInstance = repositories.listAccounts(authorizationGranteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === authorizationSourceAccount.id)
  assert(authorizationInstance, '账户授权应创建被授权者本地实例')
  const authorizedLocalPatch = await captureBusinessDml(() => patchRepository.patchAccountManagementAsync(
    authorizationInstance.id,
    {
      expectedConfigRevision: authorizationInstance.configRevision ?? 1,
      groupId: authorizationTargetGroup.id,
      priority: 37,
      tags: ['授权本地标签']
    },
    authorizationGranteeAccess
  ))
  assert(authorizedLocalPatch.result)
  assert.equal(authorizedLocalPatch.result.configRevision, (authorizationInstance.configRevision ?? 1) + 1)
  assert.deepEqual(authorizedLocalPatch.result.changedFields, ['groupId', 'priority', 'tags'])
  assert.equal(activeGroupId(authorizationInstance.id), authorizationTargetGroup.id)
  assert.equal(accountRow(authorizationInstance.id).priority, 37)
  assert(authorizedLocalPatch.dml.some((sql) => /account_tag_bindings/i.test(sql)), '授权本地标签变化必须只更新标签关系')
  assert.doesNotMatch(authorizedLocalPatch.sql.join('\n'), /credentials_encrypted/i, '授权本地 PATCH 不得读取来源或实例凭据')

  database.prepare(`
    UPDATE resource_authorizations
    SET status = 'revoked', updated_at = ?
    WHERE id = ?
  `).run(new Date().toISOString(), authorization.id)
  const revokedAuthorizationPatch = await captureBusinessDml(() => assert.rejects(
    patchRepository.patchAccountManagementAsync(authorizationInstance.id, {
      expectedConfigRevision: authorizedLocalPatch.result?.configRevision ?? 2,
      tags: ['不得写入']
    }, authorizationGranteeAccess),
    /账户授权已失效/
  ))
  assert.deepEqual(revokedAuthorizationPatch.dml, [], '失效授权必须在任何业务写入前拒绝本地 PATCH')

  database.prepare(`
    UPDATE accounts
    SET status = 'error',
        schedulable = 0,
        last_error_code = 'regression_error',
        last_error_message = '回归异常'
    WHERE id = ?
  `).run(account.id)
  const restore = await patchRepository.patchAccountManagementAsync(account.id, {
    expectedConfigRevision: 5,
    clearFailureState: true
  }, access)
  assert(restore)
  assert.equal(restore.configRevision, 6)
  assert.deepEqual(restore.changedFields, ['clearFailureState'])
  assert.equal(restore.previousStatus, 'error')
  assert.equal(restore.status, 'pending_test')
  assert.equal(restore.healthCheckRequired, true)
  const restoredRow = database.prepare(`
    SELECT status, schedulable, last_error_code, last_error_message, config_revision
    FROM accounts
    WHERE id = ?
  `).get(account.id) as unknown as {
    status: string
    schedulable: number
    last_error_code: string | null
    last_error_message: string | null
    config_revision: number
  }
  assert.equal(restoredRow.status, 'pending_test')
  assert.equal(restoredRow.schedulable, 0)
  assert.equal(restoredRow.last_error_code, null)
  assert.equal(restoredRow.last_error_message, '账户已重置，等待后台健康检查')
  assert.equal(restoredRow.config_revision, 6)

  database.prepare(`
    UPDATE accounts
    SET authorization_instance_source_account_id = ?
    WHERE id = ?
  `).run(oauthAccount.id, account.id)
  try {
    const sourceMarkedAuthorizationPatch = await captureBusinessDml(() => assert.rejects(
      patchRepository.patchAccountManagementAsync(account.id, {
        expectedConfigRevision: 6,
        notes: '授权实例不得按 owner 更新'
      }, access),
      /授权账户配置由来源账户控制/
    ))
    assert.deepEqual(sourceMarkedAuthorizationPatch.dml, [], '仅 source_account_id 标记的授权实例也不得执行 owner PATCH DML')
    assert.equal(accountRow(account.id).config_revision, 6)
    assert.equal(accountRow(account.id).notes, 'HTTP 最小响应验证')
  } finally {
    database.prepare(`
      UPDATE accounts
      SET authorization_instance_source_account_id = NULL
      WHERE id = ?
    `).run(account.id)
  }

  console.log('account-management-patch-regression passed')
} finally {
  await closeServer(server)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertSourceBoundaries(): void {
  const routeSource = readFileSync(resolve('src', 'modules', 'accounts', 'accounts.routes.ts'), 'utf8')
  const repositorySource = readFileSync(resolve('src', 'storage', 'account-management-patch.repository.ts'), 'utf8')
  const databaseClientSource = readFileSync(resolve('src', 'storage', 'database-client.ts'), 'utf8')
  assert(!routeSource.includes('findAccountForTestAsync'), 'PATCH 路由不得读取完整测试账户摘要')
  assert(!routeSource.includes('findGroupSummaryAsync'), 'PATCH 路由不得为写入再读取完整分组摘要')
  assert(!routeSource.includes('updateAccountAsync('), 'PATCH 路由必须使用专用 delta writer')
  assert(repositorySource.includes("client.driver === 'postgres' ? ' FOR UPDATE' : ''"), 'PostgreSQL 写入前必须锁定账户')
  assert(repositorySource.includes('config_revision = config_revision + 1'))
  assert(repositorySource.includes('AND config_revision = ?'))
  assert.doesNotMatch(repositorySource, /SELECT\s+\*/i, 'PATCH writer 不得查询宽行')
  assert.match(
    repositorySource,
    /if \(outcome\.previousGroupId\) invalidateGroupAccountIdsCache\(outcome\.previousGroupId\)/,
    '仅旧分组 ID 非空时才允许失效缓存'
  )
  assert.match(
    repositorySource,
    /outcome\.nextGroupId && outcome\.nextGroupId !== outcome\.previousGroupId/,
    '仅新分组 ID 非空且确实变化时才允许失效缓存'
  )
  assert.match(routeSource, /runtimeState:\s*'运行状态'/, '隐藏运行态归一化必须有操作日志标签')
  assert(databaseClientSource.includes("database.exec('BEGIN IMMEDIATE')"), 'SQLite writer transaction 必须使用 BEGIN IMMEDIATE')
}

async function captureBusinessDml<T>(operation: () => Promise<T>): Promise<{ result: T; dml: string[]; sql: string[] }> {
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const dml: string[] = []
  const sqlCalls: string[] = []
  database.prepare = ((sql: string) => {
    sqlCalls.push(sql.replace(/\s+/g, ' ').trim())
    const statement = originalPrepare(sql)
    const originalRun = statement.run.bind(statement) as typeof statement.run
    statement.run = ((...params: SQLInputValue[]) => {
      const normalized = sql.replace(/\s+/g, ' ').trim()
      if (/^(?:UPDATE|INSERT|DELETE)\b/i.test(normalized)) dml.push(normalized)
      return originalRun(...params)
    }) as typeof statement.run
    return statement
  }) as typeof database.prepare
  try {
    return { result: await operation(), dml, sql: sqlCalls }
  } finally {
    database.prepare = originalPrepare
  }
}

function requiredAccountUpdate(dml: string[]): string {
  const matches = dml.filter((sql) => /^UPDATE "?accounts"? SET /i.test(sql))
  assert.equal(matches.length, 1, `PATCH 应只更新一次目标账户主表，实际 SQL：${JSON.stringify(dml)}`)
  return matches[0] as string
}

function relationDml(dml: string[]): string[] {
  return dml.filter((sql) => /account_supported_models|account_model_mappings|account_tag_bindings|group_accounts/i.test(sql))
}

function accountRow(accountId: string): { config_revision: number; notes: string | null; priority: number } {
  return databaseModule.getBusinessDatabase().prepare(`
    SELECT config_revision, notes, priority
    FROM accounts
    WHERE id = ?
  `).get(accountId) as unknown as { config_revision: number; notes: string | null; priority: number }
}

function activeGroupId(accountId: string): string | undefined {
  const row = databaseModule.getBusinessDatabase().prepare(`
    SELECT group_id
    FROM group_accounts
    WHERE account_id = ? AND enabled = 1
  `).get(accountId) as unknown as { group_id?: string } | undefined
  return row?.group_id
}

function assertPreservedOAuthCredentials(accountId: string, expectedTier: string | undefined): void {
  const row = databaseModule.getBusinessDatabase().prepare(`
    SELECT credentials_encrypted
    FROM accounts
    WHERE id = ?
  `).get(accountId) as unknown as { credentials_encrypted: string }
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  assert.equal(credentials.refresh_token, 'oauth-refresh-preserved')
  assert.equal(credentials.client_id, 'oauth-client-preserved')
  assert.equal(credentials.id_token, 'oauth-id-token-preserved')
  assert.equal(credentials.email, 'owner@example.com')
  assert.equal(credentials.base_url, 'https://api.openai.com/v1')
  assert.equal(credentials.service_tier_override, expectedTier)
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
