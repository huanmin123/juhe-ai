import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import type { AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-openai-oauth-refresh-activation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'openai-oauth-refresh-activation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  accountTestTasks
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-test-tasks.repository.js')
])

try {
  const owner = repositories.createSystemAccount({
    username: 'oauth_refresh_activation_owner',
    displayName: 'OAuthRefresh激活回归用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: 'OAuth Refresh 激活回归分组',
    providerCode: 'gpt'
  }, access)
  const draft = oauthDraftActivationSnapshot({
    groupId: group.id,
    groupName: group.name,
    name: 'OAuth Refresh 草稿激活账户',
    ownerSystemAccountId: owner.id,
    refreshToken: 'refresh-token-oauth-success'
  })
  const task = accountTestTasks.createAccountTestTask({
    account: draftAccountSummary(draft),
    access,
    diagnostics: 'full',
    draftAccount: draft
  })
  assert(accountTestTasks.markAccountTestTaskRunning(task.id), 'OAuth 草稿测试任务应能进入运行中')
  const result: AccountTestResult = {
    accountId: task.accountId,
    accountName: task.accountName,
    providerCode: task.providerCode,
    type: task.type,
    success: true,
    statusCode: 200,
    message: 'OAuth 草稿测试成功',
    model: 'gpt-5.5'
  }
  assert(accountTestTasks.completeAccountTestTask(task.id, result), 'OAuth 草稿测试任务应能完成')

  const created = repositories.createAccount(oauthCreateRequest({
    groupId: group.id,
    name: draft.name,
    refreshToken: 'refresh-token-oauth-success'
  }), access)
  assert.equal(created.status, 'pending_test', 'OAuth 草稿人工测试成功不能直接激活新账户')
  assert.equal(created.schedulable, false, 'OAuth 新账户必须等待后台检查成功后参与调度')
  assert.equal(created.healthCheckModel, 'gpt-5.5', 'OAuth 新账户应保存表单检查模型')

  const storedTask = accountTestTasks.getAccountTestTaskRecord(task.id)
  assert.equal(storedTask?.status, 'success', '创建 OAuth 账户不应消费或改写人工测试任务')
  assert.equal(storedTask?.result?.model, 'gpt-5.5', 'OAuth 人工测试只保留本次诊断模型')

  console.log('OpenAI OAuth 草稿诊断隔离回归通过：人工测试不参与账户激活')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function oauthDraftActivationSnapshot(input: {
  groupId: string
  groupName: string
  name: string
  ownerSystemAccountId: string
  refreshToken: string
}): AccountTestDraftSnapshot {
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    'gpt',
    'oauth',
    undefined,
    { protocolCode: OPENAI_PROTOCOL_CODE, protocolVersion: OPENAI_PROTOCOL_VERSION }
  )
  return {
    id: `acctdraft_${input.name}`,
    ownerSystemAccountId: input.ownerSystemAccountId,
    groupId: input.groupId,
    groupName: input.groupName,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    name: input.name,
    type: 'oauth',
    credentials: repositories.normalizeAccountCredentialsForWrite('oauth', oauthActivationCredentials(input.refreshToken), {
      providerCode: 'gpt',
      accountType: 'oauth',
      clientCompatibility,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION
    }),
    concurrencyLimit: 20,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'responses_sse',
    modelMappings: []
  }
}

function oauthCreateRequest(input: {
  groupId: string
  name: string
  refreshToken: string
}) {
  return {
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: input.name,
    type: 'oauth',
    credentials: oauthActivationCredentials(input.refreshToken),
    groupId: input.groupId,
    status: 'active' as const,
    concurrencyLimit: 20,
    priority: 0,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'responses_sse',
    modelMappings: []
  }
}

function oauthActivationCredentials(refreshToken: string): Record<string, unknown> {
  return {
    refresh_token: refreshToken,
    base_url: 'https://api.openai.com/v1',
    supported_endpoint_modes: ['responses_json', 'responses_sse']
  }
}

function draftAccountSummary(draft: AccountTestDraftSnapshot): AccountSummary {
  const usage = emptyUsageSummary()
  return {
    id: draft.id,
    systemAccountId: draft.ownerSystemAccountId,
    ownerSystemAccountId: draft.ownerSystemAccountId,
    providerCode: draft.providerCode,
    providerProtocolProfileId: draft.providerProtocolProfileId,
    protocolCode: draft.protocolCode,
    protocolVersion: draft.protocolVersion,
    name: draft.name,
    type: draft.type,
    credentials: draft.credentials,
    status: 'active',
    concurrencyLimit: draft.concurrencyLimit,
    currentConcurrency: 0,
    priority: draft.priority,
    superPriorityEnabled: draft.superPriorityEnabled,
    fallbackEnabled: draft.fallbackEnabled,
    clientCompatibility: draft.clientCompatibility,
    supportedModels: draft.supportedModels,
    healthCheckModel: draft.healthCheckModel,
    healthCheckEndpointMode: draft.healthCheckEndpointMode,
    modelMappings: draft.modelMappings,
    schedulable: true,
    todayUsage: usage,
    usage,
    accessType: 'owner',
    boundGroupId: draft.groupId,
    boundGroupName: draft.groupName,
    groupBindStatus: 'bound',
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: false,
      canViewCredentials: true,
      canManageAccounts: true,
      canBindToApiKey: true
    },
    effectiveAvailability: {
      available: true,
      status: 'available',
      label: '草稿测试',
      color: 'blue'
    }
  }
}

function emptyUsageSummary(): AccountSummary['usage'] {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}
