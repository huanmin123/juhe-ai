import { isDeepStrictEqual } from 'node:util'

import type {
  AccountBatchEditContextField,
  AccountBatchEditContextItem,
  AccountBatchEditResult,
  AccountModelMapping,
  AccountSupportedEndpointMode
} from '../../domain/types.js'
import { assertAnthropicEndpointModesCompatible } from '../../domain/anthropic-endpoint-modes.js'
import { assertGeminiEndpointModesCompatible } from '../../domain/gemini-endpoint-modes.js'
import { assertOpenAIEndpointModesCompatible } from '../../domain/openai-endpoint-modes.js'
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
  type AccountBatchUpdateItemResult,
  type AccountBatchUpdateLockedAccount,
  type AccountBatchUpdatePreparedAccount,
  updateAccountsBatchAsync
} from '../../storage/account-batch-update.repository.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson,
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
import {
  accountHealthCheckModelSupportsImages,
  loadAccountModelValidationContextAsync
} from '../../storage/account-model-validation.repository.js'
import { normalizeAccountTagNamesInput } from '../../storage/account-tags.repository.js'
import { loadAccountBatchEditContextRecordsAsync } from '../../storage/account-batch-edit-context.repository.js'
import { isAccountExpired } from '../../storage/account-runtime-mutation-helpers.js'
import { encryptJson } from '../../storage/crypto.js'
import { normalizeNullableTextInput } from '../../storage/repository-input-normalization.js'
import { nullableServerDateTimeIso } from '../../storage/value-utils.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { normalizeAccountErrorHandlingRules } from './account-error-policy-validation.js'
import type { AccountBatchEditRequest } from './account-request.schemas.js'
import { normalizeAccountResponseInspectionRules } from './account-response-inspection-policy-validation.js'
import { assertAccountGptRequestOverridesSupportedAsync } from './account-gpt-request-overrides.validation.js'
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

export interface AccountBatchEditServiceResult extends AccountBatchEditResult {
  ownerSystemAccountId: string
}

export async function loadAccountBatchEditContextAsync(
  accountIds: string[],
  fields: readonly AccountBatchEditContextField[],
  access?: AccessScope
): Promise<AccountBatchEditContextItem[]> {
  const records = await loadAccountBatchEditContextRecordsAsync(accountIds, fields, access)
  if (records.length !== accountIds.length) throw new AccountBatchUpdateAccessError()
  const owners = new Set(records.map((account) => account.ownerSystemAccountId))
  if (owners.size !== 1) throw new AccountBatchUpdateAccessError('批量编辑账户必须属于同一系统账户作用域')
  return records.map(({ ownerSystemAccountId: _ownerSystemAccountId, ...item }) => item)
}

export async function batchEditAccountsAsync(
  input: AccountBatchEditRequest,
  access?: AccessScope
): Promise<AccountBatchEditServiceResult> {
  const updates = enabledBatchUpdates(input.updates)
  const requestedFields = Object.keys(updates)
  if (!requestedFields.length) throw new Error('请至少选择一项需要覆盖的配置')
  const result = await updateAccountsBatchAsync({
    targets: input.targets,
    requestedFields,
    access,
    prepare: async ({ client, accounts }) => prepareBatchUpdatesAsync(client, accounts, updates)
  })
  cleanupChangedBalanceSnapshots(result.balanceSnapshotCleanupAccountIds, result.items, result.batchId)
  return {
    batchId: result.batchId,
    ownerSystemAccountId: result.ownerSystemAccountId,
    changedFields: result.changedFields,
    items: result.items
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
  const proxyProfileId = hasOwn(updates, 'proxyProfileId')
    ? await enabledProxyProfileIdAsync(client, updates.proxyProfileId, accounts[0]?.systemAccountId)
    : undefined
  return Promise.all(accounts.map((account) => prepareAccountUpdateAsync(client, account, updates, proxyProfileId)))
}

async function prepareAccountUpdateAsync(
  client: DatabaseClient,
  account: AccountBatchUpdateLockedAccount,
  updates: Record<string, unknown>,
  resolvedProxyProfileId: string | undefined
): Promise<AccountBatchUpdatePreparedAccount> {
  const mainColumns = new Map<string, unknown>()
  const changedFields = new Set<string>()
  const addChange = (field: string, before: unknown, after: unknown): boolean => {
    if (isDeepStrictEqual(before, after)) return false
    changedFields.add(field)
    return true
  }
  const setColumn = (column: string, before: unknown, after: unknown, value = after): boolean => {
    if (isDeepStrictEqual(before, after)) return false
    mainColumns.set(column, value)
    return true
  }

  const credentialUpdateKeys = [
    'errorHandlingRules',
    'responseInspectionRules',
    'supportedEndpointModes',
    'serviceTierOverride',
    'reasoningEffortOverride'
  ]
  const hasCredentialConfigUpdate = hasAnyOwnKey(updates, credentialUpdateKeys)
  let nextCredentials = account.credentials
  let credentialsChanged = false
  if (hasCredentialConfigUpdate) {
    const mergedCredentials = { ...account.credentials }
    if (hasOwn(updates, 'errorHandlingRules')) {
      mergedCredentials.error_handling_rules = normalizeAccountErrorHandlingRules(updates.errorHandlingRules)
    }
    if (hasOwn(updates, 'responseInspectionRules')) {
      mergedCredentials.response_inspection_rules = normalizeAccountResponseInspectionRules(updates.responseInspectionRules)
    }
    if (hasOwn(updates, 'supportedEndpointModes')) {
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
    const credentialFieldMap: Array<[string, string]> = [
      ['errorHandlingRules', 'error_handling_rules'],
      ['responseInspectionRules', 'response_inspection_rules'],
      ['supportedEndpointModes', 'supported_endpoint_modes'],
      ['serviceTierOverride', 'service_tier_override'],
      ['reasoningEffortOverride', 'reasoning_effort_override']
    ]
    for (const [updateKey, credentialKey] of credentialFieldMap) {
      if (hasOwn(updates, updateKey)) {
        credentialsChanged = addChange(updateKey, account.credentials[credentialKey], nextCredentials[credentialKey]) || credentialsChanged
      }
    }
    if (credentialsChanged) mainColumns.set('credentials_encrypted', encryptJson(nextCredentials))
  }

  const supportedModelsRelevant = hasAnyOwnKey(updates, [
    'supportedModels', 'healthCheckModel', 'modelMappings', 'supportedEndpointModes',
    'serviceTierOverride', 'reasoningEffortOverride'
  ])
  const nextSupportedModels = hasOwn(updates, 'supportedModels')
    ? await normalizeAccountSupportedModelsForProviderAsync(
        updates.supportedModels,
        account.providerCode,
        account.systemAccountId,
        account
      ) ?? []
    : account.supportedModels
  if (supportedModelsRelevant) assertAccountSupportedModelsRequired(nextSupportedModels)
  const supportedModelsChanged = hasOwn(updates, 'supportedModels')
    && !unorderedStringListEquals(account.supportedModels, nextSupportedModels)
  if (supportedModelsChanged) addChange('supportedModels', account.supportedModels, nextSupportedModels)

  if (supportedModelsChanged || hasAnyOwnKey(updates, ['serviceTierOverride', 'reasoningEffortOverride'])) {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode: account.providerCode,
      accountType: account.type,
      credentials: nextCredentials,
      supportedModels: nextSupportedModels,
      systemAccountId: account.systemAccountId
    })
  }

  let nextHealthCheckModel = account.healthCheckModel
  if (hasOwn(updates, 'healthCheckModel')) nextHealthCheckModel = requiredText(updates.healthCheckModel, '账户检查模型')
  if (hasOwn(updates, 'healthCheckModel') || supportedModelsChanged) {
    if (!nextSupportedModels.includes(nextHealthCheckModel)) {
      throw new Error(`账户 ${account.id} 的检查模型必须属于最终支持模型`)
    }
  }
  const healthCheckModelChanged = hasOwn(updates, 'healthCheckModel')
    && addChange('healthCheckModel', account.healthCheckModel, nextHealthCheckModel)
  if (healthCheckModelChanged) mainColumns.set('health_check_model', nextHealthCheckModel)

  let nextHealthCheckEndpointMode = account.healthCheckEndpointMode
  if (hasOwn(updates, 'healthCheckEndpointMode') || hasOwn(updates, 'supportedEndpointModes')) {
    const requestedHealthCheckEndpointMode = hasOwn(updates, 'healthCheckEndpointMode')
      ? updates.healthCheckEndpointMode
      : account.healthCheckEndpointMode
    const imageCheckConfigurationChanged = hasOwn(updates, 'healthCheckEndpointMode')
      || hasOwn(updates, 'healthCheckModel')
      || hasOwn(updates, 'supportedModels')
    const modelValidationContext = requestedHealthCheckEndpointMode === 'images_json' && imageCheckConfigurationChanged
      ? await loadAccountModelValidationContextAsync(client, {
          providerCode: account.providerCode,
          systemAccountId: account.systemAccountId,
          models: [nextHealthCheckModel]
        })
      : undefined
    nextHealthCheckEndpointMode = resolveHealthCheckEndpointMode({
      value: requestedHealthCheckEndpointMode,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      enabledEndpointModes: nextCredentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
      modelSupportsImages: modelValidationContext
        ? accountHealthCheckModelSupportsImages(modelValidationContext, nextHealthCheckModel)
        : account.healthCheckEndpointMode === 'images_json'
    })
  }
  const healthCheckEndpointModeChanged = addChange(
    'healthCheckEndpointMode',
    account.healthCheckEndpointMode,
    nextHealthCheckEndpointMode
  )
  if (healthCheckEndpointModeChanged) mainColumns.set('health_check_endpoint_mode', nextHealthCheckEndpointMode)

  const shouldValidateMappings = hasOwn(updates, 'modelMappings') || hasOwn(updates, 'supportedEndpointModes')
  const nextModelMappings = shouldValidateMappings
    ? await normalizeAccountModelMappingsForProviderAsync(
        hasOwn(updates, 'modelMappings') ? updates.modelMappings : account.modelMappings,
        account.providerCode,
        account.systemAccountId,
        {
          id: account.providerProtocolProfileId,
          providerProtocolProfileId: account.providerProtocolProfileId,
          providerCode: account.providerCode,
          protocolCode: account.protocolCode,
          protocolVersion: account.protocolVersion
        },
        { supportedEndpointModes: nextCredentials.supported_endpoint_modes as AccountSupportedEndpointMode[] }
      ) ?? []
    : account.modelMappings
  if (hasOwn(updates, 'modelMappings') || supportedModelsChanged || hasOwn(updates, 'supportedEndpointModes')) {
    assertAccountModelMappingUpstreamsAllowedBySupportedModels(nextModelMappings, nextSupportedModels)
  }
  if (shouldValidateMappings || hasOwn(updates, 'healthCheckEndpointMode')) {
    assertEndpointModesCompatible(account, nextCredentials, nextModelMappings)
  }
  const modelMappingsChanged = shouldValidateMappings && !modelMappingsEqual(account.modelMappings, nextModelMappings)
  if (modelMappingsChanged) addChange('modelMappings', account.modelMappings, nextModelMappings)

  const nextTags = hasOwn(updates, 'tags') ? normalizeAccountTagNamesInput(updates.tags) ?? [] : account.tags
  const tagsChanged = hasOwn(updates, 'tags') && !unorderedStringListEquals(account.tags, nextTags)
  if (tagsChanged) addChange('tags', account.tags, nextTags)

  const nextProxyProfileId = hasOwn(updates, 'proxyProfileId') ? resolvedProxyProfileId : account.proxyProfileId
  const proxyChanged = hasOwn(updates, 'proxyProfileId')
    && addChange('proxyProfileId', account.proxyProfileId, nextProxyProfileId)
  if (proxyChanged) {
    mainColumns.set('proxy_profile_id', nextProxyProfileId ?? null)
    if (account.balanceQueryEnabled) mainColumns.set('balance_query_next_refresh_at', new Date().toISOString())
  }

  const nextConcurrencyLimit = numberValue(updates, 'concurrencyLimit', account.concurrencyLimit)
  if (hasOwn(updates, 'concurrencyLimit') && addChange('concurrencyLimit', account.concurrencyLimit, nextConcurrencyLimit)) {
    mainColumns.set('concurrency_limit', nextConcurrencyLimit)
  }
  const nextPriority = numberValue(updates, 'priority', account.priority)
  let nextSuperPriorityEnabled = booleanValue(updates, 'superPriorityEnabled', account.superPriorityEnabled)
  let nextFallbackEnabled = booleanValue(updates, 'fallbackEnabled', account.fallbackEnabled)
  if (nextSuperPriorityEnabled && nextFallbackEnabled) {
    if (updates.superPriorityEnabled === true && !hasOwn(updates, 'fallbackEnabled')) nextFallbackEnabled = false
    else if (updates.fallbackEnabled === true && !hasOwn(updates, 'superPriorityEnabled')) nextSuperPriorityEnabled = false
    else throw new Error('超级优先和降级备用不能同时开启')
  }
  const priorityChanged = addChange('priority', account.priority, nextPriority)
  const superPriorityChanged = addChange('superPriorityEnabled', account.superPriorityEnabled, nextSuperPriorityEnabled)
  const fallbackChanged = addChange('fallbackEnabled', account.fallbackEnabled, nextFallbackEnabled)
  if (priorityChanged) mainColumns.set('priority', nextPriority)
  if (superPriorityChanged) mainColumns.set('super_priority_enabled', nextSuperPriorityEnabled ? 1 : 0)
  if (fallbackChanged) mainColumns.set('fallback_enabled', nextFallbackEnabled ? 1 : 0)
  const dispatchChanged = priorityChanged || superPriorityChanged || fallbackChanged

  const nextAccountExpiresAt = hasOwn(updates, 'accountExpiresAt')
    ? nullableServerDateTimeIso(updates.accountExpiresAt, '账户套餐到期时间') ?? undefined
    : account.accountExpiresAt
  const expiresAtChanged = hasOwn(updates, 'accountExpiresAt')
    && addChange('accountExpiresAt', account.accountExpiresAt, nextAccountExpiresAt)
  if (expiresAtChanged) mainColumns.set('account_expires_at', nextAccountExpiresAt ?? null)

  const nextAvailabilitySchedule = hasOwn(updates, 'availabilitySchedule')
    ? accountAvailabilityScheduleFromRequest({ availabilitySchedule: updates.availabilitySchedule })
    : account.availabilitySchedule
  const currentScheduleJson = accountAvailabilityScheduleJson(account.availabilitySchedule)
  const nextScheduleJson = accountAvailabilityScheduleJson(nextAvailabilitySchedule)
  const scheduleChanged = hasOwn(updates, 'availabilitySchedule') && currentScheduleJson !== nextScheduleJson
  if (scheduleChanged) {
    changedFields.add('availabilitySchedule')
    mainColumns.set('availability_schedule_json', nextScheduleJson)
    mainColumns.set('availability_schedule_next_check_at', nextAccountAvailabilityScheduleCheckAt(nextAvailabilitySchedule))
  }
  if (hasOwn(updates, 'notes')) {
    const nextNotes = normalizeNullableTextInput(updates.notes, '账户备注')
    if (addChange('notes', account.notes, nextNotes)) mainColumns.set('notes', nextNotes ?? null)
  }

  let nextStatus = account.status
  let nextSchedulable = account.schedulable
  const expiredByChangedPackage = expiresAtChanged && isAccountExpired(nextAccountExpiresAt)
  if (expiredByChangedPackage) {
    nextStatus = 'disabled'
    nextSchedulable = false
    setColumn('cooldown_until', account.cooldownUntil, undefined, null)
    setColumn('last_error_code', account.lastErrorCode, 'account_expired')
    setColumn('last_error_message', account.lastErrorMessage, '账户套餐已过期，已自动停用')
    setColumn('cooldown_retest_failure_count', account.cooldownRetestFailureCount, 0)
    setColumn('cooldown_retest_observation_started_at', account.cooldownRetestObservationStartedAt, undefined, null)
    mainColumns.set('cooldown_retest_generation', null)
    setColumn('cooldown_retest_last_at', account.cooldownRetestLastAt, undefined, null)
    setColumn('cooldown_retest_last_status_code', account.cooldownRetestLastStatusCode, undefined, null)
  } else if (scheduleChanged) {
    nextStatus = accountStatusForScheduleMutation({
      requestedStatus: account.status,
      schedule: nextAvailabilitySchedule,
      now: new Date()
    })
    if (nextStatus !== account.status && statusForcesSchedulableOff(nextStatus)) nextSchedulable = false
  }
  if (setColumn('status', account.status, nextStatus)) changedFields.add('status')
  if (setColumn('schedulable', account.schedulable, nextSchedulable, nextSchedulable ? 1 : 0)) {
    changedFields.add('schedulable')
  }

  const shouldScheduleHealthCheck = proxyChanged
    || supportedModelsChanged
    || healthCheckModelChanged
    || healthCheckEndpointModeChanged
    || modelMappingsChanged
    || (credentialsChanged && hasOwn(updates, 'supportedEndpointModes'))
  if (shouldScheduleHealthCheck && nextStatus !== 'disabled') mainColumns.set('next_health_check_at', null)

  const gatewayFields = new Set([
    'status', 'schedulable', 'concurrencyLimit', 'priority', 'superPriorityEnabled', 'fallbackEnabled',
    'proxyProfileId', 'supportedModels', 'modelMappings', 'healthCheckModel', 'healthCheckEndpointMode',
    'availabilitySchedule', 'accountExpiresAt', 'errorHandlingRules', 'responseInspectionRules',
    'supportedEndpointModes', 'serviceTierOverride', 'reasoningEffortOverride'
  ])
  const groupStatsFields = new Set(['status', 'schedulable', 'concurrencyLimit'])
  const sortedChangedFields = [...changedFields].sort()
  return {
    accountId: account.id,
    expectedConfigRevision: account.configRevision,
    changedFields: sortedChangedFields,
    mainColumns,
    supportedModels: supportedModelsChanged ? nextSupportedModels : undefined,
    modelMappings: modelMappingsChanged ? nextModelMappings : undefined,
    tags: tagsChanged ? nextTags : undefined,
    dispatchBinding: dispatchChanged ? {
      priority: nextPriority,
      superPriorityEnabled: nextSuperPriorityEnabled,
      fallbackEnabled: nextFallbackEnabled
    } : undefined,
    dispatchRevisionChanged: proxyChanged,
    balanceSnapshotCleanup: proxyChanged && account.balanceQueryEnabled,
    groupStatsAffected: sortedChangedFields.some((field) => groupStatsFields.has(field)),
    gatewayRuntimeAffected: sortedChangedFields.some((field) => gatewayFields.has(field))
  }
}

function cleanupChangedBalanceSnapshots(
  accountIds: string[],
  items: AccountBatchUpdateItemResult[],
  batchId: string
): void {
  const revisionById = new Map(items.map((item) => [item.id, item.configRevision]))
  for (const accountId of accountIds) {
    cleanupAccountBalanceSnapshotAfterSave({
      accountId,
      configRevision: revisionById.get(accountId) ?? 1,
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
  if (!hasOwn(updates, updateKey)) return
  const value = updates[updateKey]
  if (value === null || value === '') delete credentials[credentialKey]
  else credentials[credentialKey] = value
}

function enabledBatchUpdates(input: AccountBatchEditRequest['updates']): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const [field, update] of Object.entries(input)) {
    if (update?.enabled) output[field] = update.value
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
  if (!row?.id || row.enabled !== 1) throw new Error('代理不存在或已停用，请选择一个已启用的代理')
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
  if (isGeminiProtocolProfile(profile)) assertGeminiEndpointModesCompatible({ modes, accountType: account.type })
}

function batchTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function requiredText(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label}不能为空`)
  return value.trim()
}

function numberValue(input: Record<string, unknown>, key: string, fallback: number): number {
  return typeof input[key] === 'number' ? input[key] as number : fallback
}

function booleanValue(input: Record<string, unknown>, key: string, fallback: boolean): boolean {
  return typeof input[key] === 'boolean' ? input[key] as boolean : fallback
}

function hasOwn(input: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(input, key)
}

function hasAnyOwnKey(input: Record<string, unknown>, keys: string[]): boolean {
  return keys.some((key) => hasOwn(input, key))
}

function unorderedStringListEquals(left: readonly string[] | undefined, right: readonly string[] | undefined): boolean {
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

function statusForcesSchedulableOff(status: string): boolean {
  return status === 'pending_test'
    || status === 'error'
    || status === 'rate_limited'
    || status === 'temporary_unavailable'
}
