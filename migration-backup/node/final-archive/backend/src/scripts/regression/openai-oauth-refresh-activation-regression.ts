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
  accountTestTasks,
  oauthRotationRepository,
  oauthUsageLoaders
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-test-tasks.repository.js'),
  import('../../storage/oauth-credential-rotation.repository.js'),
  import('../../storage/oauth-usage-loaders.js')
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

  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'error',
        schedulable = 0,
        last_error_code = 'oauth_token_refresh_failed',
        last_error_message = '受管刷新错误'
    WHERE id = ?
  `).run(created.id)
  const recoverable = await oauthRotationRepository.rotateOAuthCredentialsAsync({
    accountId: created.id,
    expectedConfigRevision: created.configRevision ?? 1,
    expectedProviderCode: 'gpt',
    expectedAccountType: 'oauth',
    expectedProviderProtocolProfileId: created.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
    credentials: created.credentials,
    recoverableLastErrorCodes: ['oauth_token_refresh_failed'],
    access
  })
  assert.equal(recoverable?.changed, true, '凭据同值时仍应恢复受管 OAuth 刷新错误')
  assert.equal(recoverable?.configRevision, (created.configRevision ?? 1) + 1, '恢复健康检查状态必须推进配置版本')
  const recoveredRow = databaseModule.getBusinessDatabase().prepare(`
    SELECT status, schedulable, last_error_code
    FROM accounts
    WHERE id = ?
  `).get(created.id) as { status: string; schedulable: number; last_error_code: string | null }
  assert.equal(recoveredRow.status, 'pending_test', '受管 OAuth 错误恢复后必须等待后台健康检查')
  assert.equal(recoveredRow.schedulable, 0, '后台健康检查通过前不得参与调度')
  assert.equal(recoveredRow.last_error_code, null, '受管 OAuth 错误必须在 rotation 事务内清理')

  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'error',
        schedulable = 0,
        last_error_code = 'manual_operator_error',
        last_error_message = '人工错误不得自动恢复'
    WHERE id = ?
  `).run(created.id)
  const unmanaged = await oauthRotationRepository.rotateOAuthCredentialsAsync({
    accountId: created.id,
    expectedConfigRevision: recoverable?.configRevision ?? (created.configRevision ?? 1) + 1,
    expectedProviderCode: 'gpt',
    expectedAccountType: 'oauth',
    expectedProviderProtocolProfileId: created.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
    credentials: created.credentials,
    recoverableLastErrorCodes: ['oauth_token_refresh_failed'],
    access
  })
  assert.equal(unmanaged?.changed, false, '凭据同值且错误码不在白名单时必须零写入')
  const unmanagedRow = databaseModule.getBusinessDatabase().prepare(`
    SELECT status, schedulable, last_error_code
    FROM accounts
    WHERE id = ?
  `).get(created.id) as { status: string; schedulable: number; last_error_code: string | null }
  assert.equal(unmanagedRow.status, 'error', '非受管错误状态不得被重新授权隐式清理')
  assert.equal(unmanagedRow.schedulable, 0, '非受管错误不得被重新授权恢复调度')
  assert.equal(unmanagedRow.last_error_code, 'manual_operator_error', '非受管错误码必须保持不变')

  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'error',
        schedulable = 0,
        account_expires_at = ?,
        last_error_code = 'oauth_token_refresh_failed',
        last_error_message = '过期账户不得恢复调度'
    WHERE id = ?
  `).run(new Date(Date.now() - 60_000).toISOString(), created.id)
  const expired = await oauthRotationRepository.rotateOAuthCredentialsAsync({
    accountId: created.id,
    expectedConfigRevision: recoverable?.configRevision ?? (created.configRevision ?? 1) + 1,
    expectedProviderCode: 'gpt',
    expectedAccountType: 'oauth',
    expectedProviderProtocolProfileId: created.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
    credentials: created.credentials,
    recoverableLastErrorCodes: ['oauth_token_refresh_failed'],
    access
  })
  assert.equal(expired?.changed, true, '过期账户的受管 OAuth 错误仍应归一化为过期状态')
  assert.equal(expired?.configRevision, recoverable?.configRevision, '仅归一化过期运行态不得再次推进配置版本')
  const expiredRow = databaseModule.getBusinessDatabase().prepare(`
    SELECT status, schedulable, last_error_code
    FROM accounts
    WHERE id = ?
  `).get(created.id) as { status: string; schedulable: number; last_error_code: string | null }
  assert.equal(expiredRow.status, 'disabled', '已过期账户重新授权后必须保持停用')
  assert.equal(expiredRow.schedulable, 0, '已过期账户重新授权后不得参与调度')
  assert.equal(expiredRow.last_error_code, 'account_expired', '已过期账户必须保留明确的过期原因')

  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'error',
        account_expires_at = '2026-08-16T06:34:49.137',
        last_error_code = 'oauth_token_refresh_failed'
    WHERE id = ?
  `).run(created.id)
  await assert.rejects(
    () => oauthRotationRepository.rotateOAuthCredentialsAsync({
      accountId: created.id,
      expectedConfigRevision: expired?.configRevision ?? recoverable?.configRevision ?? created.configRevision ?? 1,
      expectedProviderCode: 'gpt',
      expectedAccountType: 'oauth',
      expectedProviderProtocolProfileId: created.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
      credentials: created.credentials,
      recoverableLastErrorCodes: ['oauth_token_refresh_failed'],
      access
    }),
    /accounts\.account_expires_at必须是带 Z 或数值 offset 的 RFC3339 时间/,
    'OAuth 轮换必须拒绝持久化的裸账户到期时间'
  )

  const usageUpdatedAt = '2026-08-16T06:34:49.137+08:00'
  const usageResetAt = '2099-08-16T07:34:49.137+09:00'
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO account_usage_snapshots (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, updated_at, created_at
    ) VALUES (?, ?, 'openai_codex', 'regression', ?, 'fresh', ?, ?, ?)
  `).run(
    owner.id,
    created.id,
    JSON.stringify({ codex_5h_used_percent: 40, codex_5h_reset_at: usageResetAt }),
    '2026-08-16T08:34:49.137+10:00',
    usageUpdatedAt,
    usageUpdatedAt
  )
  const usageSnapshot = oauthUsageLoaders.loadOpenAICodexUsageSnapshotsByAccountIds([created.id]).get(created.id)
  assert.equal(usageSnapshot?.updatedAt, '2026-08-15T22:34:49.137Z', 'OAuth 用量快照 updatedAt 必须 canonical UTC')
  assert.equal(usageSnapshot?.lastAttemptAt, '2026-08-15T22:34:49.137Z', 'OAuth 用量快照 lastAttemptAt 必须 canonical UTC')
  assert.equal(usageSnapshot?.fiveHour?.resetsAt, '2099-08-15T22:34:49.137Z', 'OAuth 用量窗口 resetAt 必须 canonical UTC')
  databaseModule.getStatsDatabase().prepare(`
    UPDATE account_usage_snapshots
    SET snapshot_json = ?
    WHERE system_account_id = ? AND account_id = ? AND kind = 'openai_codex'
  `).run(JSON.stringify({ codex_5h_used_percent: 40, codex_5h_reset_at: '2099-08-16T07:34:49.137' }), owner.id, created.id)
  assert.throws(
    () => oauthUsageLoaders.loadOpenAICodexUsageSnapshotsByAccountIds([created.id]),
    /account_usage_snapshots 5h resetAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
    'OAuth 用量快照必须拒绝裸 resetAt'
  )

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
    'openai_standard',
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
