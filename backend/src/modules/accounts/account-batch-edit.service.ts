import { isDeepStrictEqual } from 'node:util'

import { accountCircuitCredentialOwnerIdentity } from '../../domain/account-circuit-owner.js'

import type {
  AccountBatchEditResult,
  AccountModelMapping,
  AccountSummary,
  AccountStatus,
  AccountSupportedEndpointMode
} from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  assertAnthropicEndpointModesCompatible
} from '../../domain/anthropic-endpoint-modes.js'
import {
  assertGeminiEndpointModesCompatible
} from '../../domain/gemini-endpoint-modes.js'
import {
  assertOpenAIEndpointModesCompatible
} from '../../domain/openai-endpoint-modes.js'
import { resolveHealthCheckEndpointMode } from '../../domain/account-health-check-endpoint-mode.js'
import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  AccountBatchUpdateAccessError,
  type AccountBatchUpdateLockedAccount,
  type AccountBatchUpdatePreparedAccount,
  updateAccountsBatchAsync
} from '../../storage/account-batch-update.repository.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountStatusForScheduleMutation,
  nextAccountAvailabilityScheduleCheckAt
} from '../../storage/account-availability-schedule.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/account-credentials-normalization.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  assertAccountSupportedModelsRequired,
  normalizeAccountModelMappingsForProviderAsync,
  normalizeAccountSupportedModelsForProviderAsync
} from '../../storage/account-model-normalization.js'
import { normalizeAccountTagNamesInput } from '../../storage/account-tags.repository.js'
import { findAccountSummaryAsync } from '../../storage/account-summary.repository.js'
import { isAccountExpired } from '../../storage/account-runtime-mutation-helpers.js'
import { normalizeNullableTextInput } from '../../storage/repository-input-normalization.js'
import { nullableServerDateTimeIso } from '../../storage/value-utils.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { normalizeAccountErrorHandlingRules } from './account-error-policy-validation.js'
import type { AccountBatchEditRequest } from './account-request.schemas.js'
import { normalizeAccountResponseInspectionRules } from './account-response-inspection-policy-validation.js'
import { assertAccountGptRequestOverridesSupportedAsync } from './account-gpt-request-overrides.validation.js'
import { effectiveAccountApiKeyCount } from './account-balance-config.js'
import { cleanupAccountBalanceSnapshotAfterSave } from './account-balance-snapshot-cleanup.service.js'

const modelConfigurationFields = new Set([
  'supportedModels',
  'healthCheckModel',
  'healthCheckEndpointMode',
  'modelMappings',
  'supportedEndpointModes',
  'serviceTierOverride',
  'reasoningEffortOverride'
])

export async function loadAccountBatchEditContextAsync(
  accountIds: string[],
  access?: AccessScope
): Promise<AccountSummary[]> {
  const accounts = await Promise.all(accountIds.map((accountId) => findAccountSummaryAsync(accountId, access)))
  if (accounts.some((account) => !account)) {
    throw new AccountBatchUpdateAccessError()
  }
  const resolved = accounts.filter((account): account is NonNullable<typeof account> => Boolean(account))
  const owners = new Set<string>()
  for (const account of resolved) {
    if (
      account.accessType === 'authorized'
      || account.accountAuthorizationId
      || account.authorizationInstanceSourceAccountId
      || account.permissions?.canEdit === false
      || account.permissions?.canViewCredentials === false
    ) {
      throw new AccountBatchUpdateAccessError()
    }
    const ownerSystemAccountId = account.ownerSystemAccountId ?? account.systemAccountId
    if (!ownerSystemAccountId) {
      throw new AccountBatchUpdateAccessError()
    }
    owners.add(ownerSystemAccountId)
  }
  if (owners.size !== 1) {
    throw new AccountBatchUpdateAccessError('批量编辑账户必须属于同一系统账户作用域')
  }
  return resolved
}

export async function batchEditAccountsAsync(
  input: AccountBatchEditRequest,
  access?: AccessScope
): Promise<AccountBatchEditResult> {
  const updates = enabledBatchUpdates(input.updates)
  const changedFields = Object.keys(updates)
  if (!changedFields.length) {
    throw new Error('请至少选择一项需要覆盖的配置')
  }
  const fallbackAccounts = await loadAccountBatchEditContextAsync(
    input.targets.map((target) => target.accountId),
    access
  )
  const repositoryResult = await updateAccountsBatchAsync({
    targets: input.targets,
    access,
    prepare: async ({ client, accounts }) => prepareBatchUpdatesAsync(client, accounts, updates)
  })
  cleanupChangedBalanceSnapshots(
    repositoryResult.balanceSnapshotCleanupAccountIds,
    repositoryResult.configRevisions,
    repositoryResult.batchId
  )
  let accounts: AccountSummary[]
  try {
    const refreshed = await Promise.all(
      repositoryResult.accountIds.map((accountId) => findAccountSummaryAsync(accountId, access))
    )
    if (refreshed.some((account) => !account)) {
      throw new Error('部分账户摘要不存在')
    }
    accounts = refreshed.filter((account): account is NonNullable<typeof account> => Boolean(account))
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_batch_update_summary_refresh_failed',
      batchId: repositoryResult.batchId,
      accountCount: repositoryResult.accountIds.length
    }), '批量编辑已提交，但账户摘要刷新失败，返回提交前安全摘要')
    accounts = fallbackAccounts.map((account) => ({
      ...account,
      configRevision: repositoryResult.configRevisions[account.id] ?? account.configRevision
    }))
  }
  return {
    batchId: repositoryResult.batchId,
    changedFields,
    accounts
  }
}

async function prepareBatchUpdatesAsync(
  client: DatabaseClient,
  accounts: AccountBatchUpdateLockedAccount[],
  updates: Record<string, unknown>
): Promise<AccountBatchUpdatePreparedAccount[]> {
  if (Object.keys(updates).some((field) => modelConfigurationFields.has(field))) {
    assertHomogeneousModelConfigurationBatch(accounts)
  }
  const proxyProfileId = Object.prototype.hasOwnProperty.call(updates, 'proxyProfileId')
    ? await enabledProxyProfileIdAsync(client, updates.proxyProfileId, accounts[0]?.systemAccountId)
    : undefined
  return await Promise.all(accounts.map((account) => prepareAccountUpdateAsync(account, updates, proxyProfileId)))
}

async function prepareAccountUpdateAsync(
  account: AccountBatchUpdateLockedAccount,
  updates: Record<string, unknown>,
  resolvedProxyProfileId: string | undefined
): Promise<AccountBatchUpdatePreparedAccount> {
  const hasCredentialConfigUpdate = hasAnyOwnKey(updates, [
    'errorHandlingRules',
    'responseInspectionRules',
    'supportedEndpointModes',
    'serviceTierOverride',
    'reasoningEffortOverride'
  ])
  let nextCredentials = account.credentials
  if (hasCredentialConfigUpdate) {
    const mergedCredentials = { ...account.credentials }
    if (Object.prototype.hasOwnProperty.call(updates, 'errorHandlingRules')) {
      mergedCredentials.error_handling_rules = normalizeAccountErrorHandlingRules(updates.errorHandlingRules)
    }
    if (Object.prototype.hasOwnProperty.call(updates, 'responseInspectionRules')) {
      mergedCredentials.response_inspection_rules = normalizeAccountResponseInspectionRules(updates.responseInspectionRules)
    }
    if (Object.prototype.hasOwnProperty.call(updates, 'supportedEndpointModes')) {
      mergedCredentials.supported_endpoint_modes = updates.supportedEndpointModes
    }
    applyNullableCredentialOverride(mergedCredentials, updates, 'serviceTierOverride', 'service_tier_override')
    applyNullableCredentialOverride(mergedCredentials, updates, 'reasoningEffortOverride', 'reasoning_effort_override')
    nextCredentials = normalizeAccountCredentialsForWrite(account.type, mergedCredentials, {
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
  }

  const nextSupportedModels = Object.prototype.hasOwnProperty.call(updates, 'supportedModels')
    ? await normalizeAccountSupportedModelsForProviderAsync(
        updates.supportedModels,
        account.providerCode,
        account.systemAccountId,
        account
      ) ?? []
    : account.supportedModels
  assertAccountSupportedModelsRequired(nextSupportedModels)
  await assertAccountGptRequestOverridesSupportedAsync({
    providerCode: account.providerCode,
    accountType: account.type,
    credentials: nextCredentials,
    supportedModels: nextSupportedModels,
    systemAccountId: account.systemAccountId
  })

  const nextHealthCheckModel = Object.prototype.hasOwnProperty.call(updates, 'healthCheckModel')
    ? requiredText(updates.healthCheckModel, '账户检查模型')
    : account.healthCheckModel
  if (!nextSupportedModels.includes(nextHealthCheckModel)) {
    throw new Error(`账户 ${account.id} 的检查模型必须属于最终支持模型`)
  }
  const nextHealthCheckEndpointMode = resolveHealthCheckEndpointMode({
    value: Object.prototype.hasOwnProperty.call(updates, 'healthCheckEndpointMode')
      ? updates.healthCheckEndpointMode
      : account.healthCheckEndpointMode,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    enabledEndpointModes: nextCredentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
  })

  const shouldValidateMappings = Object.prototype.hasOwnProperty.call(updates, 'modelMappings')
    || Object.prototype.hasOwnProperty.call(updates, 'supportedEndpointModes')
  const nextModelMappings = shouldValidateMappings
    ? await normalizeAccountModelMappingsForProviderAsync(
        Object.prototype.hasOwnProperty.call(updates, 'modelMappings') ? updates.modelMappings : account.modelMappings,
        account.providerCode,
        account.systemAccountId,
        {
          id: account.providerProtocolProfileId,
          providerProtocolProfileId: account.providerProtocolProfileId,
          providerCode: account.providerCode,
          protocolCode: account.protocolCode,
          protocolVersion: account.protocolVersion
        },
        {
          supportedEndpointModes: nextCredentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
        }
      ) ?? []
    : account.modelMappings
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(nextModelMappings, nextSupportedModels)
  assertEndpointModesCompatible(account, nextCredentials, nextModelMappings)

  const nextTags = Object.prototype.hasOwnProperty.call(updates, 'tags')
    ? normalizeAccountTagNamesInput(updates.tags) ?? []
    : account.tags
  const nextProxyProfileId = Object.prototype.hasOwnProperty.call(updates, 'proxyProfileId')
    ? resolvedProxyProfileId
    : account.proxyProfileId
  const nextConcurrencyLimit = numberValue(updates, 'concurrencyLimit', account.concurrencyLimit)
  const nextPriority = numberValue(updates, 'priority', account.priority)
  let nextSuperPriorityEnabled = booleanValue(updates, 'superPriorityEnabled', account.superPriorityEnabled)
  let nextFallbackEnabled = booleanValue(updates, 'fallbackEnabled', account.fallbackEnabled)
  if (nextSuperPriorityEnabled && nextFallbackEnabled) {
    if (updates.superPriorityEnabled === true && !Object.prototype.hasOwnProperty.call(updates, 'fallbackEnabled')) {
      nextFallbackEnabled = false
    } else if (updates.fallbackEnabled === true && !Object.prototype.hasOwnProperty.call(updates, 'superPriorityEnabled')) {
      nextSuperPriorityEnabled = false
    } else {
      throw new Error('超级优先和降级备用不能同时开启')
    }
  }

  const nextAccountExpiresAt = Object.prototype.hasOwnProperty.call(updates, 'accountExpiresAt')
    ? nullableServerDateTimeIso(updates.accountExpiresAt, '账户套餐到期时间') ?? undefined
    : account.accountExpiresAt
  const nextAvailabilitySchedule = Object.prototype.hasOwnProperty.call(updates, 'availabilitySchedule')
    ? accountAvailabilityScheduleFromRequest({ availabilitySchedule: updates.availabilitySchedule })
    : account.availabilitySchedule
  const nextNotes = Object.prototype.hasOwnProperty.call(updates, 'notes')
    ? normalizeNullableTextInput(updates.notes, '账户备注')
    : account.notes

  const supportedModelsChanged = !unorderedStringListEquals(account.supportedModels, nextSupportedModels)
  const modelMappingsChanged = !modelMappingsEqual(account.modelMappings, nextModelMappings)
  const endpointModesChanged = !unorderedStringListEquals(
    account.credentials.supported_endpoint_modes as string[] | undefined,
    nextCredentials.supported_endpoint_modes as string[] | undefined
  )
  const proxyChanged = account.proxyProfileId !== nextProxyProfileId
  const healthCheckModelChanged = account.healthCheckModel !== nextHealthCheckModel
  const healthCheckEndpointModeChanged = account.healthCheckEndpointMode !== nextHealthCheckEndpointMode
  const shouldScheduleHealthCheck = proxyChanged
    || supportedModelsChanged
    || healthCheckModelChanged
    || healthCheckEndpointModeChanged
    || modelMappingsChanged
    || endpointModesChanged
  const expiredByPackage = isAccountExpired(nextAccountExpiresAt)
  const scheduledStatus = expiredByPackage
    ? 'disabled'
    : Object.prototype.hasOwnProperty.call(updates, 'availabilitySchedule')
      ? accountStatusForScheduleMutation({
          requestedStatus: account.status,
          schedule: nextAvailabilitySchedule,
          now: new Date()
        })
      : account.status
  const nextStatus: AccountStatus = scheduledStatus

  return {
    accountId: account.id,
    expectedConfigRevision: account.configRevision,
    credentials: hasCredentialConfigUpdate ? nextCredentials : undefined,
    proxyProfileId: nextProxyProfileId,
    concurrencyLimit: nextConcurrencyLimit,
    priority: nextPriority,
    superPriorityEnabled: nextSuperPriorityEnabled,
    fallbackEnabled: nextFallbackEnabled,
    status: nextStatus,
    schedulable: expiredByPackage || statusForcesSchedulableOff(nextStatus) ? false : account.schedulable,
    availabilitySchedule: nextAvailabilitySchedule,
    availabilityScheduleNextCheckAt: nextAccountAvailabilityScheduleCheckAt(nextAvailabilitySchedule),
    accountExpiresAt: nextAccountExpiresAt,
    notes: nextNotes,
    cooldownUntil: expiredByPackage ? undefined : account.cooldownUntil,
    lastErrorCode: expiredByPackage
      ? 'account_expired'
      : account.lastErrorCode,
    lastErrorMessage: expiredByPackage
      ? '账户套餐已过期，已自动停用'
      : account.lastErrorMessage,
    cooldownRetestFailureCount: expiredByPackage ? 0 : account.cooldownRetestFailureCount,
    cooldownRetestObservationStartedAt: expiredByPackage
      ? undefined
      : account.cooldownRetestObservationStartedAt,
    cooldownRetestLastAt: expiredByPackage ? undefined : account.cooldownRetestLastAt,
    cooldownRetestLastStatusCode: expiredByPackage
      ? undefined
      : account.cooldownRetestLastStatusCode,
    healthCheckModel: nextHealthCheckModel,
    healthCheckEndpointMode: nextHealthCheckEndpointMode,
    supportedModels: nextSupportedModels,
    modelMappings: nextModelMappings,
    tags: nextTags,
    supportedModelsChanged,
    modelMappingsChanged,
    tagsChanged: !unorderedStringListEquals(account.tags, nextTags),
    dispatchChanged: account.priority !== nextPriority
      || account.superPriorityEnabled !== nextSuperPriorityEnabled
      || account.fallbackEnabled !== nextFallbackEnabled,
    dispatchRevisionChanged: !isDeepStrictEqual(
      accountCircuitCredentialOwnerIdentity(account.credentials),
      accountCircuitCredentialOwnerIdentity(nextCredentials)
    )
      || account.proxyProfileId !== nextProxyProfileId,
    resetHealthCheckState: shouldScheduleHealthCheck && nextStatus !== 'disabled',
    disableBalanceQuery: account.type === 'api_key' && effectiveAccountApiKeyCount(nextCredentials) > 1,
    resetBalanceQuery: proxyChanged && account.balanceQueryEnabled
  }
}

function cleanupChangedBalanceSnapshots(
  accountIds: string[],
  configRevisions: Record<string, number>,
  batchId: string
): void {
  if (accountIds.length === 0) return
  for (const accountId of accountIds) {
    cleanupAccountBalanceSnapshotAfterSave({
      accountId,
      configRevision: configRevisions[accountId] ?? 1,
      reason: 'batch_balance_identity_changed',
      batchId
    })
  }
}

function applyNullableCredentialOverride(
  credentials: Record<string, unknown>,
  updates: Record<string, unknown>,
  updateKey: string,
  credentialKey: string
): void {
  if (!Object.prototype.hasOwnProperty.call(updates, updateKey)) return
  const value = updates[updateKey]
  if (value === null || value === '') {
    delete credentials[credentialKey]
    return
  }
  credentials[credentialKey] = value
}

function enabledBatchUpdates(input: AccountBatchEditRequest['updates']): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const [field, update] of Object.entries(input)) {
    if (update?.enabled) {
      output[field] = update.value
    }
  }
  return output
}

function assertHomogeneousModelConfigurationBatch(accounts: AccountBatchUpdateLockedAccount[]): void {
  const signatures = new Set(accounts.map((account) => [
    account.providerCode,
    account.providerProtocolProfileId,
    account.type
  ].join('\u0000')))
  if (signatures.size !== 1) {
    throw new Error('模型与协议配置只能批量覆盖到相同供应商、协议档案和账户类型的账户')
  }
}

async function enabledProxyProfileIdAsync(
  client: DatabaseClient,
  value: unknown,
  systemAccountId: string | undefined
): Promise<string | undefined> {
  if (value === null) return undefined
  const proxyProfileId = requiredText(value, '代理配置')
  const ownerSystemAccountId = requiredText(systemAccountId, '账户归属')
  const row = await client.one<{ id?: string; enabled?: number }>(`
    SELECT id, enabled
    FROM ${batchTable(client, 'proxy_profiles')}
    WHERE id = ?
      AND system_account_id = ?
    LIMIT 1
  `, [proxyProfileId, ownerSystemAccountId])
  if (!row?.id || row.enabled !== 1) {
    throw new Error('代理不存在或已停用，请选择一个已启用的代理')
  }
  return row.id
}

function assertEndpointModesCompatible(
  account: AccountBatchUpdateLockedAccount,
  credentials: Record<string, unknown>,
  modelMappings: AccountModelMapping[]
): void {
  const modes = credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
  if (isHybridProviderCode(account.providerCode)) return
  const profile = {
    id: account.providerProtocolProfileId,
    providerProtocolProfileId: account.providerProtocolProfileId,
    providerCode: account.providerCode,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion
  }
  if (isAnthropicProtocolProfile(profile)) {
    assertAnthropicEndpointModesCompatible({ modes, accountType: account.type })
    return
  }
  if (isOpenAIProtocolProfile(profile)) {
    assertOpenAIEndpointModesCompatible({
      modes,
      modelMappings,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
    return
  }
  if (isGeminiProtocolProfile(profile)) {
    assertGeminiEndpointModesCompatible({ modes, accountType: account.type })
  }
}

function batchTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function requiredText(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}

function numberValue(input: Record<string, unknown>, key: string, fallback: number): number {
  const value = input[key]
  return typeof value === 'number' ? value : fallback
}

function booleanValue(input: Record<string, unknown>, key: string, fallback: boolean): boolean {
  const value = input[key]
  return typeof value === 'boolean' ? value : fallback
}

function hasAnyOwnKey(input: Record<string, unknown>, keys: string[]): boolean {
  return keys.some((key) => Object.prototype.hasOwnProperty.call(input, key))
}

function unorderedStringListEquals(
  left: readonly string[] | undefined,
  right: readonly string[] | undefined
): boolean {
  const normalizedLeft = [...(left ?? [])].sort()
  const normalizedRight = [...(right ?? [])].sort()
  return normalizedLeft.length === normalizedRight.length
    && normalizedLeft.every((value, index) => value === normalizedRight[index])
}

function modelMappingsEqual(
  left: readonly AccountModelMapping[] | undefined,
  right: readonly AccountModelMapping[] | undefined
): boolean {
  const normalizedLeft = [...(left ?? [])].map(modelMappingKey).sort()
  const normalizedRight = [...(right ?? [])].map(modelMappingKey).sort()
  return normalizedLeft.length === normalizedRight.length
    && normalizedLeft.every((value, index) => value === normalizedRight[index])
}

function modelMappingKey(mapping: AccountModelMapping): string {
  return [
    mapping.sourceEndpointFamily,
    mapping.sourceModel,
    mapping.upstreamEndpointFamily,
    mapping.upstreamModel,
    mapping.enabled === false ? '0' : '1'
  ].join('\u0000')
}

function statusForcesSchedulableOff(status: AccountStatus): boolean {
  return status === 'pending_test'
    || status === 'error'
    || status === 'rate_limited'
    || status === 'temporary_unavailable'
}
