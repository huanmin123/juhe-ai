import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { AccountModelMapping, AccountSummary, AccountSupportedEndpointMode, AccountTestResult } from '../../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import type { AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-draft-activation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-draft-activation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const draftChatSourceModel = 'gpt-draft-activation-chat-source'
const draftChatCaseSourceModel = 'GPT-draft-activation-chat-source'
const draftChatUpstreamModel = 'gpt-draft-activation-chat-upstream'

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
    username: 'api_key_draft_activation_owner',
    displayName: 'ApiKeyDraftActivationOwner',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: 'API Key 草稿激活分组',
    providerCode: 'gpt'
  }, access)
  assert.equal(group.providerCode, 'gpt', 'API Key 草稿激活回归需要 GPT 分组')
  registerDraftModelCatalog(owner.id)

  const accountInput = apiKeyActivationRequest({
    groupId: group.id,
    name: 'API Key 草稿激活账户',
    apiKeys: ['sk-api-key-draft-a', 'sk-api-key-draft-b']
  })
  const task = createCompletedDraftActivationTask({
    draft: apiKeyDraftActivationSnapshot({
      ...accountInput,
      ownerSystemAccountId: owner.id,
      groupName: group.name
    }),
    access
  })
  const storedTask = accountTestTasks.getAccountTestTaskRecord(task.id)
  assert.deepEqual(
    storedTask?.draftAccount?.modelMappings,
    accountInput.modelMappings,
    '草稿测试任务记录读回后应保留 Chat Completions 同协议模型别名'
  )

  const { clientCompatibility: _clientCompatibility, ...accountCreateInput } = accountInput
  const created = repositories.createAccount({
    ...accountCreateInput,
    status: 'active'
  }, access)
  assert.equal(created.status, 'pending_test', 'API Key 草稿人工测试成功不能直接激活新账户')
  assert.equal(created.schedulable, false, '新账户必须等待后台检查成功后才能参与调度')
  assert.equal(created.healthCheckModel, draftChatUpstreamModel, '新账户应持久化表单检查模型')

  const storedTaskAfterCreate = accountTestTasks.getAccountTestTaskRecord(task.id)
  assert.equal(storedTaskAfterCreate?.status, 'success', '创建账户不应修改人工测试任务结果')
  assert.equal(storedTaskAfterCreate?.result?.model, draftChatUpstreamModel, '人工测试结果只保留本次诊断模型')

  console.log('API Key 草稿诊断隔离回归通过：人工测试保留诊断结果，新账户始终等待后台检查激活')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function apiKeyActivationRequest(input: {
  groupId: string
  name: string
  apiKeys: string[]
}) {
  return {
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: input.name,
    type: 'api_key',
    credentials: apiKeyActivationCredentials(input.apiKeys),
    groupId: input.groupId,
    concurrencyLimit: 20,
    priority: 0,
    clientCompatibility: 'codex_responses' as const,
    supportedModels: [draftChatUpstreamModel],
    healthCheckModel: draftChatUpstreamModel,
    healthCheckEndpointMode: 'responses_sse' as const,
    modelMappings: [draftChatAliasMapping(), draftChatCaseAliasMapping()],
    notes: 'API Key 草稿测试成功后保存应直接启用'
  }
}

function registerDraftModelCatalog(systemAccountId: string): void {
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: draftChatSourceModel,
    scope: 'personal',
    systemAccountId: systemAccountId,
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: draftChatCaseSourceModel,
    scope: 'personal',
    systemAccountId: systemAccountId,
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: draftChatUpstreamModel,
    scope: 'personal',
    systemAccountId: systemAccountId,
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: systemAccountId
  })
}

function draftChatAliasMapping(): AccountModelMapping {
  return {
    sourceModel: draftChatSourceModel,
    sourceEndpointFamily: 'chat_completions',
    upstreamModel: draftChatUpstreamModel,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }
}

function draftChatCaseAliasMapping(): AccountModelMapping {
  return {
    sourceModel: draftChatCaseSourceModel,
    sourceEndpointFamily: 'chat_completions',
    upstreamModel: draftChatUpstreamModel,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }
}

function apiKeyActivationCredentials(apiKeys: string[]): Record<string, unknown> {
  return {
    api_key: apiKeys[0],
    api_keys: apiKeys,
    api_key_strategy: 'weighted_round_robin',
    api_key_weights: apiKeys.map((_, index) => index + 1),
    base_url: 'https://api.openai.com/v1',
    supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
  }
}

function apiKeyDraftActivationSnapshot(input: ReturnType<typeof apiKeyActivationRequest> & {
  ownerSystemAccountId: string
  groupName: string
}): AccountTestDraftSnapshot {
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    input.providerCode,
    input.type,
    input.clientCompatibility,
    'openai_standard',
    { protocolCode: OPENAI_PROTOCOL_CODE, protocolVersion: OPENAI_PROTOCOL_VERSION }
  )
  const credentials = repositories.normalizeAccountCredentialsForWrite(input.type, input.credentials, {
    providerCode: input.providerCode,
    accountType: input.type,
    clientCompatibility,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION
  })
  return {
    id: `acctdraft_${input.name}`,
    ownerSystemAccountId: input.ownerSystemAccountId,
    groupId: input.groupId,
    groupName: input.groupName,
    providerCode: input.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    name: input.name,
    type: input.type,
    credentials,
    concurrencyLimit: input.concurrencyLimit,
    priority: input.priority,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility,
    supportedModels: input.supportedModels,
    healthCheckModel: input.healthCheckModel,
    healthCheckEndpointMode: input.healthCheckEndpointMode,
    modelMappings: repositories.normalizeAccountModelMappingsForProvider(input.modelMappings, input.providerCode, input.ownerSystemAccountId, {
      providerCode: input.providerCode,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION
    }, {
      supportedEndpointModes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
    }) ?? [],
    availabilityScheduleJson: undefined,
    notes: input.notes
  }
}

function createCompletedDraftActivationTask(input: {
  draft: AccountTestDraftSnapshot
  access: { systemAccountId: string; role: 'user' }
}) {
  const task = accountTestTasks.createAccountTestTask({
    account: draftAccountSummary(input.draft),
    access: input.access,
    diagnostics: 'full',
    draftAccount: input.draft
  })
  assert(accountTestTasks.markAccountTestTaskRunning(task.id), 'API Key 草稿测试任务应能进入运行中')
  const result: AccountTestResult = {
    accountId: task.accountId,
    accountName: task.accountName,
    providerCode: task.providerCode,
    type: task.type,
    success: true,
    statusCode: 200,
    message: 'API Key 草稿测试成功',
    model: draftChatUpstreamModel
  }
  assert(accountTestTasks.completeAccountTestTask(task.id, result), 'API Key 草稿测试任务应能完成')
  return task
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
