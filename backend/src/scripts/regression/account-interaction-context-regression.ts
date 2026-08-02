import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  XAI_OPENAI_V1_PROFILE_ID,
  XAI_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { postgresDialect, type DatabaseClient } from '../../storage/database-client.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-interaction-context-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-interaction-context-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, contextRepository, detailRoutes, authRequestContext] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-interaction-context.repository.js'),
  import('../../modules/accounts/account-detail.routes.js'),
  import('../../modules/auth/request-context.js')
])

assert.equal(contextRepository.accountInteractionContextTrueLiteral('sqlite'), '1')
assert.equal(contextRepository.accountInteractionContextTrueLiteral('postgres'), '1')

const postgresCloneProjectionSql: string[] = []
const stopAfterPostgresCloneRevisionFence = new Error('PostgreSQL clone SQL capture complete')
let postgresCloneProjectionQueryCount = 0
const fakePostgresCloneProjectionClient: DatabaseClient = {
  driver: 'postgres',
  dialect: postgresDialect,
  async query() {
    return []
  },
  async one<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
    postgresCloneProjectionSql.push(postgresDialect.bind(sql, params).sql)
    postgresCloneProjectionQueryCount += 1
    if (postgresCloneProjectionQueryCount === 1) {
      return {
        id: 'postgres-clone-account',
        config_revision: 1,
        system_account_id: 'sys_admin',
        authorization_instance_authorization_id: null,
        authorization_instance_source_account_id: null,
        bound_group_id: null,
        bound_group_binding_updated_at: null,
        bound_group_record_updated_at: null
      } as T
    }
    throw stopAfterPostgresCloneRevisionFence
  },
  async execute() {
    return { changes: 0 }
  },
  async transaction() {
    throw new Error('PostgreSQL clone SQL capture must not open a transaction')
  }
}

await assert.rejects(
  () => contextRepository.findAccountCloneContextAsync('postgres-clone-account', { systemAccountId: 'sys_admin', role: 'admin' }, fakePostgresCloneProjectionClient),
  (error: unknown) => error === stopAfterPostgresCloneRevisionFence,
  'PostgreSQL clone SQL capture must stop after the revision fence'
)
assert.equal(postgresCloneProjectionSql.length, 2, 'PostgreSQL clone SQL capture must reach the main projection and revision fence')
const [postgresCloneMainProjectionSql, postgresCloneRevisionFenceSql] = postgresCloneProjectionSql
for (const [label, sql, expectedIntegerLiteralCount] of [
  ['主投影', postgresCloneMainProjectionSql, 4],
  ['revision fence', postgresCloneRevisionFenceSql, 3]
] as const) {
  assert.equal((sql.match(/group_accounts\.enabled\s*=\s*1/gi) ?? []).length, expectedIntegerLiteralCount, `PostgreSQL 克隆${label}必须使用 ${expectedIntegerLiteralCount} 个整数启用条件`)
  assert.doesNotMatch(sql, /group_accounts\.enabled\s*=\s*TRUE/i, `PostgreSQL 克隆${label}不得使用布尔字面量比较整数列`)
  assert.match(sql, /CASE\s+WHEN\s+group_accounts\.enabled\s*=\s*1/i, `PostgreSQL 克隆${label}必须优先选择启用的来源分组绑定`)
}

let server: ReturnType<ReturnType<typeof express>['listen']> | undefined

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const geminiAccount = await repositories.createAccountAsync({
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    name: 'Gemini 重授权与克隆上下文',
    notes: 'clone-note',
    type: 'google_oauth',
    credentials: {
      access_token: 'gemini-access-secret',
      refresh_token: 'gemini-refresh-secret',
      client_id: 'gemini-client-id',
      client_secret: 'gemini-client-secret',
      quota_project_id: 'quota-project',
      oauth_type: 'ai_studio',
      project_id: 'project-id',
      tier_id: 'aistudio_paid',
      base_url: 'https://generativelanguage.googleapis.com',
      supported_endpoint_modes: ['generate_content_json'],
      service_tier_override: 'priority',
      reasoning_effort_override: 'high',
      error_handling_rules: [{
        enabled: true,
        name: 'clone-error-rule',
        priority: 1,
        status_codes: [429],
        action: 'retry_next'
      }],
      response_inspection_rules: [{
        enabled: true,
        name: 'clone-response-rule',
        priority: 1,
        match: { outputTextIncludes: ['blocked'] },
        action: 'retry_next_account'
      }]
    },
    supportedModels: ['gemini-2.5-pro'],
    healthCheckModel: 'gemini-2.5-pro',
    healthCheckEndpointMode: 'generate_content_json',
    tags: ['clone-tag'],
    status: 'active',
    skipInitialHealthCheck: true
  }, access)
  const openAIAccount = await repositories.createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'OpenAI API Key 克隆上下文',
    type: 'api_key',
    credentials: {
      api_keys: ['openai-api-key-secret-a', 'openai-api-key-secret-b'],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [3, 7],
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_sse']
    },
    supportedModels: ['gpt-5.4-mini'],
    healthCheckModel: 'gpt-5.4-mini',
    healthCheckEndpointMode: 'responses_sse',
    status: 'active',
    skipInitialHealthCheck: true,
    balanceQueryEnabled: true,
    balanceQueryConfig: {
      adapter: 'custom',
      intervalMinutes: 8,
      custom: {
        path: '/balance',
        remainingPointer: '/data/remaining',
        divisor: '100'
      }
    }
  }, access)
  const anthropicAccount = await repositories.createAccountAsync({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: 'Anthropic 账户不读取上下文',
    type: 'oauth',
    credentials: {
      access_token: 'anthropic-access-secret',
      refresh_token: 'anthropic-refresh-secret',
      base_url: 'https://api.anthropic.com/v1',
      supported_endpoint_modes: ['messages_json']
    },
    supportedModels: ['claude-opus-4-8'],
    healthCheckModel: 'claude-opus-4-8',
    healthCheckEndpointMode: 'messages_json',
    status: 'active',
    skipInitialHealthCheck: true
  }, access)
  const grokAccount = await repositories.createAccountAsync({
    providerCode: XAI_PROVIDER_CODE,
    providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
    name: 'Grok 账户不读取上下文',
    type: 'oauth',
    credentials: {
      access_token: 'grok-access-secret',
      refresh_token: 'grok-refresh-secret',
      base_url: 'https://cli-chat-proxy.grok.com/v1',
      supported_endpoint_modes: ['responses_sse']
    },
    supportedModels: ['grok-4.5'],
    healthCheckModel: 'grok-4.5',
    healthCheckEndpointMode: 'responses_sse',
    status: 'active',
    skipInitialHealthCheck: true
  }, access)
  const codeAssistAccount = await repositories.createAccountAsync({
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    name: 'Gemini Code Assist 窄上下文',
    type: 'google_oauth',
    credentials: {
      access_token: 'code-assist-access-secret',
      refresh_token: 'code-assist-refresh-secret',
      client_id: 'code-assist-client-not-needed-by-form',
      client_secret: 'code-assist-client-secret-not-needed-by-form',
      oauth_type: 'code_assist',
      project_id: 'code-assist-project',
      tier_id: 'gcp_standard',
      base_url: 'https://cloudcode-pa.googleapis.com',
      supported_endpoint_modes: ['generate_content_json']
    },
    supportedModels: ['gemini-2.5-pro'],
    healthCheckModel: 'gemini-2.5-pro',
    healthCheckEndpointMode: 'generate_content_json',
    status: 'active',
    skipInitialHealthCheck: true
  }, access)

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capture = async <T>(operation: () => Promise<T>): Promise<{ result: T; sql: string[] }> => {
    const sql: string[] = []
    database.prepare = ((statementSql: string) => {
      const statement = originalPrepare(statementSql)
      const originalGet = statement.get.bind(statement) as typeof statement.get
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.get = ((...params: SQLInputValue[]) => {
        sql.push(statementSql)
        return originalGet(...params)
      }) as typeof statement.get
      statement.all = ((...params: SQLInputValue[]) => {
        sql.push(statementSql)
        return originalAll(...params)
      }) as typeof statement.all
      return statement
    }) as typeof database.prepare
    try {
      return { result: await operation(), sql }
    } finally {
      database.prepare = originalPrepare
    }
  }

  const oauthCapture = await capture(() => contextRepository.findAccountOAuthReauthorizationContextAsync(geminiAccount.id, access))
  assert(oauthCapture.result, 'Gemini 重授权上下文应返回账户')
  assert.deepEqual(Object.keys(oauthCapture.result).sort(), [
    'baseUrl',
    'clientId',
    'clientSecret',
    'configRevision',
    'id',
    'oauthType',
    'projectId',
    'quotaProjectId',
    'tierId'
  ].sort(), '重授权上下文必须使用精确字段白名单')
  assert.equal(oauthCapture.sql.length, 1, 'Gemini 重授权上下文只允许一条业务查询')
  assert.match(oauthCapture.sql[0]!, /accounts\.provider_code\s*=\s*'gemini'/i, 'Gemini provider 白名单必须进入携带密文的定位 SQL')
  assert.match(oauthCapture.sql[0]!, /accounts\.type\s*=\s*'google_oauth'/i, 'Google OAuth 类型白名单必须进入携带密文的定位 SQL')
  assert.doesNotMatch(oauthCapture.sql[0]!, /account_supported_models|account_tags|groups|usage|runtime|balance|diagnostic/i)
  assert.doesNotMatch(JSON.stringify(oauthCapture.result), /gemini-access-secret|gemini-refresh-secret/)

  const codeAssistCapture = await capture(() => contextRepository.findAccountOAuthReauthorizationContextAsync(codeAssistAccount.id, access))
  assert(codeAssistCapture.result, 'Gemini Code Assist 重授权上下文应返回账户')
  assert.equal(codeAssistCapture.result.oauthType, 'code_assist')
  assert.equal(codeAssistCapture.result.clientId, undefined, 'Code Assist 不得返回表单不需要的 Client ID')
  assert.equal(codeAssistCapture.result.clientSecret, undefined, 'Code Assist 不得返回表单不需要的 Client Secret')
  assert.doesNotMatch(
    JSON.stringify(codeAssistCapture.result),
    /code-assist-access-secret|code-assist-refresh-secret|code-assist-client-not-needed-by-form|code-assist-client-secret-not-needed-by-form/
  )

  for (const [providerName, account] of [
    ['OpenAI', openAIAccount],
    ['Anthropic', anthropicAccount],
    ['Grok', grokAccount]
  ] as const) {
    const nonGeminiCapture = await capture(() => contextRepository.findAccountOAuthReauthorizationContextAsync(account.id, access))
    assert.equal(nonGeminiCapture.result, undefined, `${providerName} 重新授权不得读取 Gemini OAuth 上下文`)
    assert.equal(nonGeminiCapture.sql.length, 1, `${providerName} 错误调用只能执行一次白名单定位查询`)
    assert.match(nonGeminiCapture.sql[0]!, /accounts\.provider_code\s*=\s*'gemini'/i)
    assert.match(nonGeminiCapture.sql[0]!, /accounts\.type\s*=\s*'google_oauth'/i)
  }

  const cloneCapture = await capture(() => contextRepository.findAccountCloneContextAsync(geminiAccount.id, access))
  assert(cloneCapture.result, '克隆上下文应返回账户')
  assert.equal(cloneCapture.sql.length, 3, `克隆上下文应固定为主投影、关系合并和 revision fence 三条查询，实际 ${cloneCapture.sql.length} 条`)
  assert.match(cloneCapture.sql[0]!, /group_accounts\.enabled\s*=\s*1/i, 'SQLite 克隆主投影必须使用数值布尔字面量')
  assert.match(cloneCapture.sql[2]!, /group_accounts\.enabled\s*=\s*1/i, 'SQLite 克隆 revision fence 必须使用数值布尔字面量')
  assert.match(cloneCapture.sql[1]!, /UNION ALL/i)
  assert.match(cloneCapture.sql[1]!, /WITH\s+scoped_account\s+AS[\s\S]*system_account_id\s*=\s*\?/i, '克隆关系投影必须先建立 owner 作用域')
  assert.match(cloneCapture.sql[1]!, /account_supported_models[\s\S]*INNER JOIN scoped_account/i, '支持模型关系必须复用 owner 作用域')
  assert.match(cloneCapture.sql[1]!, /account_tag_bindings[\s\S]*INNER JOIN scoped_account[\s\S]*account_tag_bindings\.system_account_id/i, '标签关系必须同时约束账户和绑定 owner')
  assert.match(cloneCapture.sql[1]!, /account_model_mappings[\s\S]*INNER JOIN scoped_account/i, '模型映射关系必须复用 owner 作用域')
  assert.deepEqual(cloneCapture.result.supportedModels, ['gemini-2.5-pro'])
  assert.deepEqual(cloneCapture.result.tags.map((item) => item.name), ['clone-tag'])
  assert.deepEqual(Object.keys(cloneCapture.result.credentialOptions).sort(), [
    'base_url',
    'client_id',
    'error_handling_rules',
    'oauth_type',
    'project_id',
    'quota_project_id',
    'reasoning_effort_override',
    'response_inspection_rules',
    'service_tier_override',
    'supported_endpoint_modes',
    'tier_id'
  ].sort(), '克隆上下文只允许返回克隆表单实际复制的非建号选项')
  assert.equal(cloneCapture.result.status, 'active')
  assert.equal(cloneCapture.result.credentialOptions.quota_project_id, 'quota-project')
  assert.equal(cloneCapture.result.credentialOptions.client_id, 'gemini-client-id')
  assert.equal(cloneCapture.result.credentialOptions.oauth_type, 'ai_studio')
  assert.equal(cloneCapture.result.credentialOptions.project_id, 'project-id')
  assert.equal(cloneCapture.result.credentialOptions.tier_id, 'aistudio_paid')
  assert.doesNotMatch(
    JSON.stringify(cloneCapture.result),
    /gemini-access-secret|gemini-refresh-secret|gemini-client-secret/
  )
  assert.doesNotMatch(cloneCapture.sql.join('\n'), /usage|runtime|authorization_limits|diagnostic/i)

  const apiKeyCloneContext = await contextRepository.findAccountCloneContextAsync(openAIAccount.id, access)
  assert(apiKeyCloneContext, 'API Key 克隆上下文应返回账户')
  assert.equal(apiKeyCloneContext.status, 'active')
  assert.deepEqual(apiKeyCloneContext.credentialOptions.api_key_count, 2)
  assert.equal(apiKeyCloneContext.credentialOptions.api_key_strategy, 'weighted_round_robin')
  assert.deepEqual(apiKeyCloneContext.credentialOptions.api_key_weights, [3, 7])
  assert.deepEqual(apiKeyCloneContext.balanceQueryConfig, {
    adapter: 'custom',
    intervalMinutes: 8,
    custom: { path: '/balance', remainingPointer: '/data/remaining', divisor: '100' }
  })
  assert.equal(apiKeyCloneContext.balanceQueryEnabled, false, '多个 API Key 时必须保留来源账户实际的自动关闭状态')
  assert.doesNotMatch(JSON.stringify(apiKeyCloneContext), /openai-api-key-secret-a|openai-api-key-secret-b/)

  const sourceGroup = repositories.createGroup({
    name: '克隆来源自定义分组',
    providerCode: GEMINI_PROVIDER_CODE,
    enabled: true
  }, access)
  const sourceGroupAt = new Date().toISOString()
  originalPrepare(`
    INSERT INTO group_accounts (
      system_account_id, group_id, account_id, account_authorization_id,
      local_priority, local_super_priority_enabled, local_fallback_enabled,
      enabled, created_at, updated_at
    ) VALUES (?, ?, ?, NULL, 0, 0, 0, 1, ?, ?)
  `).run(access.systemAccountId, sourceGroup.id, geminiAccount.id, sourceGroupAt, sourceGroupAt)
  const customGroupClone = await contextRepository.findAccountCloneContextAsync(geminiAccount.id, access)
  assert.equal(customGroupClone?.boundGroupId, sourceGroup.id, '克隆必须保留来源账户的非默认分组')
  assert.equal(customGroupClone?.boundGroupName, sourceGroup.name, '克隆必须返回来源非默认分组名称')
  originalPrepare('UPDATE group_accounts SET enabled = 0 WHERE system_account_id = ? AND account_id = ?')
    .run(access.systemAccountId, geminiAccount.id)
  const historicalGroupClone = await contextRepository.findAccountCloneContextAsync(geminiAccount.id, access)
  assert.equal(historicalGroupClone?.boundGroupId, sourceGroup.id, '没有启用绑定时仍应保留最近的来源分组')
  assert.equal(historicalGroupClone?.boundGroupName, sourceGroup.name, '没有启用绑定时仍应保留来源分组名称')

  const otherOwner = repositories.createSystemAccount({
    username: 'account_interaction_other_owner',
    displayName: '其他账户所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const deniedCapture = await capture(() => contextRepository.findAccountOAuthReauthorizationContextAsync(geminiAccount.id, {
    systemAccountId: otherOwner.id,
    role: 'user'
  }))
  assert.equal(deniedCapture.result, undefined)
  assert.equal(deniedCapture.sql.length, 1)
  assert.match(deniedCapture.sql[0]!, /accounts\.system_account_id\s*=\s*\?/i, 'owner 条件必须进入携带密文的定位 SQL')
  const deniedCloneCapture = await capture(() => contextRepository.findAccountCloneContextAsync(geminiAccount.id, {
    systemAccountId: otherOwner.id,
    role: 'user'
  }))
  assert.equal(deniedCloneCapture.result, undefined)
  assert.equal(deniedCloneCapture.sql.length, 1, '越权克隆必须在主投影阶段终止')
  assert.match(deniedCloneCapture.sql[0]!, /accounts\.system_account_id\s*=\s*\?/i, '克隆 owner 条件必须进入携带密文的定位 SQL')

  const fenceGroup = repositories.createGroup({
    name: '克隆分组 fence',
    providerCode: GEMINI_PROVIDER_CODE,
    enabled: true
  }, access)
  let groupFenceMutationCount = 0
  database.prepare = ((statementSql: string) => {
    const statement = originalPrepare(statementSql)
    if (!/UNION ALL/i.test(statementSql)) return statement
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.all = ((...params: SQLInputValue[]) => {
      const rows = originalAll(...params)
      if (groupFenceMutationCount === 0) {
        const changedAt = new Date().toISOString()
        originalPrepare(`
          INSERT INTO group_accounts (
            system_account_id, group_id, account_id, account_authorization_id,
            local_priority, local_super_priority_enabled, local_fallback_enabled,
            enabled, created_at, updated_at
          ) VALUES (?, ?, ?, NULL, 0, 0, 0, 1, ?, ?)
        `).run(access.systemAccountId, fenceGroup.id, geminiAccount.id, changedAt, changedAt)
      }
      groupFenceMutationCount += 1
      return rows
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare
  let groupFencedClone: Awaited<ReturnType<typeof contextRepository.findAccountCloneContextAsync>>
  try {
    groupFencedClone = await contextRepository.findAccountCloneContextAsync(geminiAccount.id, access)
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(groupFenceMutationCount, 2, '分组关系独立变化必须触发一次克隆上下文重试')
  assert.equal(groupFencedClone?.boundGroupId, fenceGroup.id, '重试后必须返回同一分组关系版本')

  const crossOwnerTagId = 'tag_cross_owner_clone_context'
  const crossOwnerGroup = repositories.createGroup({
    name: '其他 owner 分组',
    providerCode: GEMINI_PROVIDER_CODE,
    enabled: true
  }, { systemAccountId: otherOwner.id, role: 'user' })
  const crossOwnerAt = new Date(Date.now() + 1000).toISOString()
  originalPrepare('INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)')
    .run(crossOwnerTagId, otherOwner.id, 'cross-owner-tag', crossOwnerAt, crossOwnerAt)
  originalPrepare('INSERT INTO account_tag_bindings (account_id, tag_id, system_account_id, created_at) VALUES (?, ?, ?, ?)')
    .run(geminiAccount.id, crossOwnerTagId, otherOwner.id, crossOwnerAt)
  originalPrepare(`
    INSERT INTO group_accounts (
      system_account_id, group_id, account_id, account_authorization_id,
      local_priority, local_super_priority_enabled, local_fallback_enabled,
      enabled, created_at, updated_at
    ) VALUES (?, ?, ?, NULL, 0, 0, 0, 1, ?, ?)
  `).run(access.systemAccountId, crossOwnerGroup.id, geminiAccount.id, crossOwnerAt, crossOwnerAt)
  const tenantScopedClone = await contextRepository.findAccountCloneContextAsync(geminiAccount.id, access)
  assert(tenantScopedClone)
  assert.equal(tenantScopedClone.tags.some((item) => item.id === crossOwnerTagId), false, '克隆上下文不得返回其他 owner 标签')
  assert.notEqual(tenantScopedClone.boundGroupId, crossOwnerGroup.id, '克隆上下文不得返回其他 owner 分组')

  const grantee = repositories.createSystemAccount({
    username: 'account_interaction_grantee',
    displayName: '克隆上下文授权用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '克隆上下文授权目标分组',
    providerCode: GEMINI_PROVIDER_CODE,
    enabled: true
  }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: geminiAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '克隆上下文授权实例边界'
  }, access)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === geminiAccount.id)
  assert(authorizedInstance, '账户授权应生成被授权者作用域的实例账户')
  await assert.rejects(
    () => contextRepository.findAccountCloneContextAsync(authorizedInstance.id, granteeAccess),
    contextRepository.AccountInteractionContextForbiddenError,
    '授权实例不能克隆'
  )

  const app = express()
  app.use((_req, _res, next) => authRequestContext.withRequestAuthContext({
    systemAccountId: access.systemAccountId,
    username: 'admin',
    displayName: 'Administrator',
    role: access.role,
    mustChangePassword: false,
    sessionId: 'account-interaction-context-session'
  }, next))
  const router = express.Router()
  detailRoutes.registerAccountDetailRoutes(router)
  app.use('/accounts', router)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string')
  const baseUrl = `http://127.0.0.1:${address.port}/accounts/${geminiAccount.id}`
  const oauthResponse = await fetch(`${baseUrl}/oauth-reauthorization-context`)
  assert.equal(oauthResponse.status, 200)
  assert.equal(oauthResponse.headers.get('cache-control'), 'no-store')
  const openAIContextResponse = await fetch(
    `http://127.0.0.1:${address.port}/accounts/${openAIAccount.id}/oauth-reauthorization-context`
  )
  assert.equal(openAIContextResponse.status, 404, '非 Gemini OAuth 账户不得暴露重授权上下文')
  const cloneResponse = await fetch(`${baseUrl}/clone-context`)
  assert.equal(cloneResponse.status, 200)
  assert.equal(cloneResponse.headers.get('cache-control'), 'no-store')
  const clonePayload = await cloneResponse.json() as { data?: Record<string, unknown> }
  assert(clonePayload.data)
  assert.equal('credentials' in clonePayload.data, false, '克隆 HTTP 响应不得保留宽凭据容器')
  assert.equal(clonePayload.data.balanceQueryEnabled, false, '克隆 HTTP 响应必须返回来源余额查询开关')
  assert.doesNotMatch(
    JSON.stringify(clonePayload),
    /gemini-access-secret|gemini-refresh-secret|gemini-client-secret/
  )
  const authorizedCloneResponse = await fetch(`http://127.0.0.1:${address.port}/accounts/${authorizedInstance.id}/clone-context`)
  assert.equal(authorizedCloneResponse.status, 403, '授权实例的克隆 HTTP 请求必须拒绝')
  assert.equal(authorizedCloneResponse.headers.get('cache-control'), 'no-store')

  let concurrentMutations = 0
  database.prepare = ((statementSql: string) => {
    const statement = originalPrepare(statementSql)
    if (!/UNION ALL/i.test(statementSql)) return statement
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.all = ((...params: SQLInputValue[]) => {
      const rows = originalAll(...params)
      originalPrepare('UPDATE accounts SET config_revision = config_revision + 1 WHERE id = ?').run(geminiAccount.id)
      concurrentMutations += 1
      return rows
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare
  try {
    await assert.rejects(
      () => contextRepository.findAccountCloneContextAsync(geminiAccount.id, access),
      contextRepository.AccountInteractionContextConflictError,
      '克隆上下文持续发生并发变化时必须拒绝拼接跨版本快照'
    )
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(concurrentMutations, 2, '克隆上下文只允许一次有界重试')

  console.log('AI 账户交互上下文回归通过：四供应商重授权按需分流、Gemini 单查询窄 DTO，克隆 revision 一致')
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
