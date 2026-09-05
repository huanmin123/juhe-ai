import { isDeepStrictEqual } from 'node:util'

import { accountCircuitCredentialOwnerIdentity } from '../domain/account-circuit-owner.js'
import { resolveHealthCheckEndpointMode } from '../domain/account-health-check-endpoint-mode.js'
import { assertAnthropicEndpointModesCompatible } from '../domain/anthropic-endpoint-modes.js'
import { assertGeminiEndpointModesCompatible } from '../domain/gemini-endpoint-modes.js'
import { assertOpenAIEndpointModesCompatible } from '../domain/openai-endpoint-modes.js'
import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '../domain/provider-protocol.js'
import type {
  AccountClientCompatibility,
  AccountHealthCheckEndpointMode,
  AccountModelMapping,
  AccountStatus,
  AccountSummary,
  AccountSupportedEndpointMode
} from '../domain/types.js'
import {
  accountBalanceQueryIdentity,
  normalizeAccountBalanceConfig,
  validateAccountBalanceCapability
} from '../modules/accounts/account-balance-config.js'
import {
  accountErrorPolicyValidationMessage,
  validateAccountCredentialsErrorHandlingRules
} from '../modules/accounts/account-error-policy-validation.js'
import {
  accountResponseInspectionPolicyValidationMessage,
  validateAccountCredentialsResponseInspectionRules
} from '../modules/accounts/account-response-inspection-policy-validation.js'
import {
  accountGptRequestOverridesNeedModelCatalog,
  assertAccountGptRequestOverridesSupportedAsync
} from '../modules/accounts/account-gpt-request-overrides.validation.js'
import {
  applyAccountCredentialsPatch,
  credentialsRecordValue,
  mergeAccountCredentialsForUpdate
} from '../modules/accounts/account-credential-update.js'
import { changedCredentialPatchFields } from '../modules/accounts/account-update-delta.js'
import { clearNormalRouteLatencyDegradationForAccountBindingAsync } from '../modules/gateway/runtime/normal-route-latency-degradation.service.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { canAccessAll, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson,
  accountStatusForScheduleMutation,
  nextAccountAvailabilityScheduleCheckAt,
  parseAccountAvailabilityScheduleJson
} from './account-availability-schedule.js'
import {
  advanceAccountCircuitDispatchRevisionFamilyInTransaction
} from './account-circuit-control-plane.repository.js'
import { normalizeAccountCredentialsForWrite, requiredAccountCredentialSource } from './account-credentials-normalization.js'
import { accountCredentialFingerprint } from './account-identity.js'
import { accountApiKeyEntries, isAccountApiKeyPoolIsolationEnabled } from './account-api-key-rotation.js'
import {
  initializeAddedAccountApiKeyRuntimeStatesInClient,
  loadAccountApiKeyRuntimeStatesForAccountInClient
} from './account-api-key-runtime-state.repository.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  assertAccountSupportedModelsRequired,
  normalizeAccountModelMappingsForProviderAsync,
  normalizeAccountSupportedModelsForProviderAsync
} from './account-model-normalization.js'
import {
  accountHealthCheckModelSupportsImages,
  loadAccountModelValidationContextAsync,
  type AccountModelValidationContext
} from './account-model-validation.repository.js'
import {
  normalizeAccountModelMappingsInput,
  replaceAccountModelMappingsInClientAsync
} from './account-model-mappings.repository.js'
import { maxAccountNameLength, replaceAccountNameSearchTermsAsync } from './account-name-search.repository.js'
import {
  cooldownRetestObservationStartedAtForStatus,
  initialCooldownUntilForStatus,
  isAccountExpired,
  newCooldownRetestGeneration
} from './account-runtime-mutation-helpers.js'
import { normalizeAccountTagNamesInput, replaceAccountTagsAsync } from './account-tags.repository.js'
import {
  normalizeAccountSupportedModelsInput,
  replaceAccountSupportedModelsInClientAsync
} from './account-supported-models.repository.js'
import {
  normalizeFallbackInput,
  normalizedOptionalDispatchPriority,
  normalizedPositiveIntegerInput,
  normalizeSuperPriorityInput,
  openAIOAuthRefreshMetadata
} from './account-write-input.js'
import { runtimeConfig } from '../config/runtime.js'
import { decryptJson, encryptJson, maskSecret } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import {
  createPostgresDatabaseClient,
  createSqliteDatabaseClient,
  type DatabaseClient
} from './database-client.js'
import { refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { getPostgresPool } from './postgres-client.js'
import {
  hasOwnInput,
  normalizeNullableIdInput,
  normalizeNullableTextInput,
  normalizeOptionalBooleanInput,
  requiredTextInput
} from './repository-input-normalization.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'
import { reserveAndEnqueueAccountHealthJobsInputInTransactionAsync } from './account-health-jobs-input-outbox.repository.js'
import { canManageResourceOwner } from './resource-authorization-helpers.js'
import { isCoolingAccountStatus, isHardUnavailableAccountStatus, normalizedAccountStatusInput } from './account-status.js'
import { nullableServerDateTimeIso } from './value-utils.js'

const defaultDisabledMultiKeyBalanceConfig = { adapter: 'builtin', intervalMinutes: 5 } as const

export interface AccountManagementPatchInput extends Record<string, unknown> {
  expectedConfigRevision: number
}

export interface AccountManagementPatchChange {
  field: string
  before: unknown
  after: unknown
}

export interface AccountManagementPatchResult {
  id: string
  configRevision: number
  changedFields: string[]
  authorizationInstancesAffected: boolean
  changes: AccountManagementPatchChange[]
  name: string
  ownerSystemAccountId: string
  status: AccountStatus
  previousStatus: AccountStatus
  healthCheckRequired: boolean
  healthCheckReason?: 'activation' | 'configuration'
  runtimeRestoreRequired: boolean
  authorizedBinding?: {
    systemAccountId: string
    groupId: string
    accountAuthorizationId: string
  }
  balanceIdentityChanged: boolean
  balanceAutoDisabledForMultipleApiKeys: boolean
}

export class AccountManagementPatchRevisionConflictError extends Error {
  constructor(
    readonly accountId: string,
    readonly expectedConfigRevision: number,
    readonly actualConfigRevision?: number
  ) {
    super(`账户配置已发生并发变更，请重试：${accountId}`)
    this.name = 'AccountManagementPatchRevisionConflictError'
  }
}

interface AccountPatchRow {
  id: string
  config_revision: number | bigint | string
  system_account_id: string
  provider_code: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  notes: string | null
  type: string
  status: AccountStatus
  credentials_encrypted?: string
  proxy_profile_id: string | null
  concurrency_limit: number | bigint | string
  priority: number | bigint | string
  super_priority_enabled: number | boolean | string
  fallback_enabled: number | boolean | string
  client_compatibility: AccountClientCompatibility
  schedulable: number | boolean | string
  availability_schedule_json: string | null
  availability_schedule_next_check_at: string | null
  account_expires_at: string | null
  cooldown_until: string | null
  last_error_code: string | null
  last_error_message: string | null
  last_error_trace_id: string | null
  cooldown_retest_failure_count: number | bigint | string
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_generation: string | null
  cooldown_retest_last_at: string | null
  cooldown_retest_last_status_code: number | bigint | string | null
  temporary_unavailable_continuous_probe_enabled: number | boolean | string
  health_check_model: string
  health_check_endpoint_mode: AccountHealthCheckEndpointMode
  last_health_check_at: string | null
  next_health_check_at: string | null
  last_health_success_at: string | null
  health_check_failure_count: number | bigint | string
  health_check_failure_started_at: string | null
  last_health_check_status_code: number | bigint | string | null
  last_health_check_error_code: string | null
  last_health_check_error_message: string | null
  last_health_check_trace_id: string | null
  stream_failure_count: number | bigint | string
  stream_failure_window_started_at: string | null
  authorization_instance_source_account_id: string | null
  authorization_instance_authorization_id: string | null
  balance_query_enabled: number | boolean | string
  balance_query_config_json: string
  balance_query_next_refresh_at: string | null
}

interface AccountPatchTransactionResult extends AccountManagementPatchResult {
  previousGroupId?: string
  nextGroupId?: string
  renamedAuthorizationInstanceIds: string[]
  groupStatsAffected: boolean
  gatewayRuntimeAffected: boolean
  accountLookupAffected: boolean
}

interface PatchContext {
  client: DatabaseClient
  row: AccountPatchRow
  input: AccountManagementPatchInput
  access?: AccessScope
  now: string
  nowMs: number
}

export async function patchAccountManagementAsync(
  accountId: string,
  input: AccountManagementPatchInput,
  access?: AccessScope
): Promise<AccountManagementPatchResult | undefined> {
  const expectedConfigRevision = input.expectedConfigRevision
  if (!Number.isInteger(expectedConfigRevision) || expectedConfigRevision < 1) {
    throw new Error('账户配置版本无效')
  }
  const client = await accountPatchDatabaseClient()
  let outcome: AccountPatchTransactionResult | undefined
  try {
    outcome = await client.transaction(async (tx) => {
      const row = await loadAccountPatchRowForUpdate(tx, accountId, input, access)
      if (!row || !canManageResourceOwner(row.system_account_id, access)) return undefined
      const now = nowIso()
      if ((row.authorization_instance_source_account_id || row.authorization_instance_authorization_id)
        && !(await authorizedInstanceIsActiveInClient(tx, row, now))) {
        return undefined
      }
      const currentRevision = integerValue(row.config_revision)
      if (currentRevision !== expectedConfigRevision) {
        throw new AccountManagementPatchRevisionConflictError(accountId, expectedConfigRevision, currentRevision)
      }
      const context: PatchContext = {
        client: tx,
        row,
        input,
        access,
        now,
        nowMs: Date.now()
      }
      return input.clearFailureState === true
        ? patchAccountFailureStateInTransaction(context)
        : patchOwnerAccountInTransaction(context)
    })
  } catch (error) {
    if (isDuplicateAccountNameError(error)) {
      throw new Error('同一用户下账户名称已存在')
    }
    throw error
  }
  return finalizeAccountManagementPatchOutcome(outcome)
}

async function finalizeAccountManagementPatchOutcome(
  outcome: AccountPatchTransactionResult | undefined
): Promise<AccountManagementPatchResult | undefined> {
  if (!outcome) return undefined
  if (outcome.changedFields.length > 0) await applyAccountPatchPostCommitEffects(outcome)
  const {
    previousGroupId: _previousGroupId,
    nextGroupId: _nextGroupId,
    renamedAuthorizationInstanceIds: _renamedAuthorizationInstanceIds,
    groupStatsAffected: _groupStatsAffected,
    gatewayRuntimeAffected: _gatewayRuntimeAffected,
    accountLookupAffected: _accountLookupAffected,
    ...result
  } = outcome
  return result
}

async function patchOwnerAccountInTransaction(context: PatchContext): Promise<AccountPatchTransactionResult> {
  const { client, row, input, now, nowMs } = context
  const requestedKeys = Object.keys(input).filter((key) => key !== 'expectedConfigRevision' && key !== 'clearFailureState')
  const authorizationInstance = Boolean(
    row.authorization_instance_source_account_id || row.authorization_instance_authorization_id
  )
  if (authorizationInstance) {
    return patchAuthorizedAccountLocalInTransaction(context, requestedKeys)
  }
  if (requestedKeys.length === 0) return unchangedPatchResult(row)

  const credentialsRequired = accountManagementPatchNeedsCredentials(input)
  if (credentialsRequired && !row.credentials_encrypted) {
    throw new Error('账户凭据数据缺失')
  }
  const currentCredentials = row.credentials_encrypted
    ? decryptJson<Record<string, unknown>>(row.credentials_encrypted)
    : {}
  const nextClientCompatibility = row.client_compatibility
  const credentialPatch = credentialsRecordValue(input.credentialsPatch)
  const legacyCredentials = credentialsRecordValue(input.credentials)
  const hasCredentialInput = Boolean(credentialPatch || legacyCredentials)
  const credentialCandidate = credentialPatch
    ? applyAccountCredentialsPatch(currentCredentials, credentialPatch)
    : legacyCredentials
      ? mergeAccountCredentialsForUpdate({ type: row.type, credentials: currentCredentials } as AccountSummary, legacyCredentials)
      : currentCredentials
  const nextCredentials = hasCredentialInput
    ? normalizeAccountCredentialsForWrite(row.type, credentialCandidate, {
        providerCode: row.provider_code,
        accountType: row.type,
        clientCompatibility: nextClientCompatibility,
        providerProtocolProfileId: row.provider_protocol_profile_id,
        protocolCode: row.protocol_code,
        protocolVersion: row.protocol_version
      })
    : currentCredentials
  if (hasCredentialInput) validateCredentialPolicies(nextCredentials)
  const credentialsChanged = hasCredentialInput && !isDeepStrictEqual(currentCredentials, nextCredentials)
  const endpointModesChanged = credentialsChanged && !isDeepStrictEqual(
    currentCredentials.supported_endpoint_modes,
    nextCredentials.supported_endpoint_modes
  )

  const requiresModelState = hasOwnInput(input, 'supportedModels')
    || hasOwnInput(input, 'healthCheckModel')
    || hasOwnInput(input, 'healthCheckEndpointMode')
    || hasOwnInput(input, 'modelMappings')
    || hasCredentialInput
  let currentSupportedModels: string[] = []
  let nextSupportedModels: string[] = []
  let currentModelMappings: AccountModelMapping[] = []
  let nextModelMappings: AccountModelMapping[] = []
  let nextHealthCheckModel = row.health_check_model
  let nextHealthCheckEndpointMode = row.health_check_endpoint_mode
  if (requiresModelState) {
    currentSupportedModels = await loadSupportedModelsInClient(client, row.id)
    currentModelMappings = await loadModelMappingsInClient(client, row.id)
    const validationSupportedModels = hasOwnInput(input, 'supportedModels')
      ? normalizeAccountSupportedModelsInput(input.supportedModels) ?? []
      : currentSupportedModels
    const validationModelMappings = hasOwnInput(input, 'modelMappings')
      ? normalizeAccountModelMappingsInput(input.modelMappings) ?? []
      : currentModelMappings
    const supportedModelsNeedValidation = !isHybridProviderCode(row.provider_code)
      && hasOwnInput(input, 'supportedModels')
      && !unorderedStringListEquals(currentSupportedModels, validationSupportedModels)
    const modelMappingsNeedValidation = validationModelMappings.length > 0
      && (endpointModesChanged || (
        hasOwnInput(input, 'modelMappings')
        && !accountModelMappingsEqual(currentModelMappings, validationModelMappings)
      ))
    const requestOverridesNeedValidation = (
      hasCredentialInput || hasOwnInput(input, 'supportedModels')
    ) && accountGptRequestOverridesNeedModelCatalog(nextCredentials)
    const healthCheckEndpointModeNeedsImageValidation = (
      hasOwnInput(input, 'healthCheckEndpointMode')
        ? input.healthCheckEndpointMode
        : row.health_check_endpoint_mode
    ) === 'images_json'
    const modelValidationContext = supportedModelsNeedValidation
      || modelMappingsNeedValidation
      || requestOverridesNeedValidation
      || healthCheckEndpointModeNeedsImageValidation
      ? await loadAccountModelValidationContextAsync(client, {
          providerCode: row.provider_code,
          systemAccountId: row.system_account_id,
          models: [
            ...validationSupportedModels,
            ...(typeof input.healthCheckModel === 'string'
              ? [input.healthCheckModel]
              : [row.health_check_model])
          ],
          mappings: modelMappingsNeedValidation ? validationModelMappings : []
        })
      : undefined
    nextSupportedModels = await normalizedSupportedModelsForPatch(
      row,
      input,
      currentSupportedModels,
      modelValidationContext
    )
    nextModelMappings = await normalizedModelMappingsForPatch(
      row,
      input,
      currentModelMappings,
      nextCredentials,
      endpointModesChanged,
      modelValidationContext
    )
    assertAccountSupportedModelsRequired(nextSupportedModels)
    assertAccountModelMappingUpstreamsAllowedBySupportedModels(nextModelMappings, nextSupportedModels)
    nextHealthCheckModel = normalizedHealthCheckModel(
      hasOwnInput(input, 'healthCheckModel') ? input.healthCheckModel : row.health_check_model,
      nextSupportedModels
    )
    nextHealthCheckEndpointMode = resolveHealthCheckEndpointMode({
      value: hasOwnInput(input, 'healthCheckEndpointMode') ? input.healthCheckEndpointMode : row.health_check_endpoint_mode,
      providerCode: row.provider_code,
      providerProtocolProfileId: row.provider_protocol_profile_id,
      enabledEndpointModes: supportedEndpointModes(nextCredentials),
      modelSupportsImages: modelValidationContext
        ? accountHealthCheckModelSupportsImages(modelValidationContext, nextHealthCheckModel)
        : false
    })
    assertAccountEndpointModesCompatible(protocolProfileFromRow(row), {
      modes: supportedEndpointModes(nextCredentials),
      modelMappings: nextModelMappings,
      accountType: row.type,
      clientCompatibility: nextClientCompatibility
    })
    if (hasCredentialInput || hasOwnInput(input, 'supportedModels')) {
      await assertAccountGptRequestOverridesSupportedAsync({
        providerCode: row.provider_code,
        accountType: row.type,
        credentials: nextCredentials,
        supportedModels: nextSupportedModels,
        systemAccountId: row.system_account_id,
        validationContext: modelValidationContext
      })
    }
  }

  const mainColumns = new Map<string, unknown>()
  const changes: AccountManagementPatchChange[] = []
  const changedFields = new Set<string>()
  const addChange = (field: string, before: unknown, after: unknown): void => {
    if (isDeepStrictEqual(before, after)) return
    changedFields.add(field)
    changes.push({ field, before, after })
  }
  const setColumn = (column: keyof AccountPatchRow | string, current: unknown, next: unknown, value = next): void => {
    if (isDeepStrictEqual(current, next)) return
    mainColumns.set(column, value)
  }

  const nextName = hasOwnInput(input, 'name') ? normalizedAccountName(input.name) : row.name
  setColumn('name', row.name, nextName)
  addChange('name', row.name, nextName)
  const nextNotes = hasOwnInput(input, 'notes') ? normalizeNullableTextInput(input.notes, '账户备注') ?? null : row.notes
  setColumn('notes', row.notes, nextNotes)
  addChange('notes', row.notes ?? undefined, nextNotes ?? undefined)

  if (credentialsChanged) {
    const credentialSource = requiredAccountCredentialSource(row.type, nextCredentials)
    const oauthMetadata = openAIOAuthRefreshMetadata(row.type, nextCredentials, protocolProfileFromRow(row))
    mainColumns.set('credentials_encrypted', encryptJson(nextCredentials))
    mainColumns.set('credential_fingerprint', accountCredentialFingerprint(credentialSource) || null)
    mainColumns.set('credential_mask', maskSecret(credentialSource))
    mainColumns.set('oauth_access_token_expires_at', oauthMetadata.accessTokenExpiresAt)
    mainColumns.set('oauth_refresh_token_present', oauthMetadata.refreshTokenPresent ? 1 : 0)
    const credentialFields = credentialPatch
      ? changedCredentialPatchFields(currentCredentials, credentialPatch)
      : ['credentials']
    for (const field of credentialFields.length ? credentialFields : ['credentials']) {
      addChange(field, '已设置', '已变更')
    }
  }
  setColumn('client_compatibility', row.client_compatibility, nextClientCompatibility)
  addChange('clientCompatibility', row.client_compatibility, nextClientCompatibility)

  const currentProxyProfileId = row.proxy_profile_id ?? undefined
  const requestedProxyProfileId = hasOwnInput(input, 'proxyProfileId')
    ? normalizeNullableIdInput(input.proxyProfileId, '代理配置')
    : currentProxyProfileId
  const nextProxyProfileId = requestedProxyProfileId !== currentProxyProfileId
    ? await resolveEnabledProxyProfileIdInClient(client, requestedProxyProfileId)
    : currentProxyProfileId
  setColumn('proxy_profile_id', currentProxyProfileId, nextProxyProfileId, nextProxyProfileId ?? null)
  addChange('proxyProfileId', currentProxyProfileId, nextProxyProfileId)

  const nextConcurrencyLimit = normalizedPositiveIntegerInput(input.concurrencyLimit, integerValue(row.concurrency_limit), '并发限制')
  setColumn('concurrency_limit', integerValue(row.concurrency_limit), nextConcurrencyLimit)
  addChange('concurrencyLimit', integerValue(row.concurrency_limit), nextConcurrencyLimit)

  const currentPriority = integerValue(row.priority)
  const currentSuperPriority = databaseBoolean(row.super_priority_enabled)
  const currentFallback = databaseBoolean(row.fallback_enabled)
  const hasSuperPriorityInput = hasOwnInput(input, 'superPriorityEnabled')
  const hasFallbackInput = hasOwnInput(input, 'fallbackEnabled')
  const nextPriority = normalizedOptionalDispatchPriority(input.priority, currentPriority)
  let nextSuperPriority = normalizeSuperPriorityInput(input.superPriorityEnabled, currentSuperPriority)
  let nextFallback = normalizeFallbackInput(input.fallbackEnabled, currentFallback)
  if (hasSuperPriorityInput && nextSuperPriority && hasFallbackInput && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  if (hasSuperPriorityInput && nextSuperPriority) nextFallback = false
  if (hasFallbackInput && nextFallback) nextSuperPriority = false
  setColumn('priority', currentPriority, nextPriority)
  setColumn('super_priority_enabled', currentSuperPriority, nextSuperPriority, nextSuperPriority ? 1 : 0)
  setColumn('fallback_enabled', currentFallback, nextFallback, nextFallback ? 1 : 0)
  addChange('priority', currentPriority, nextPriority)
  addChange('superPriorityEnabled', currentSuperPriority, nextSuperPriority)
  addChange('fallbackEnabled', currentFallback, nextFallback)
  const dispatchBindingChanged = currentPriority !== nextPriority
    || currentSuperPriority !== nextSuperPriority
    || currentFallback !== nextFallback

  const currentSchedule = parseAccountAvailabilityScheduleJson(row.availability_schedule_json)
  const hasScheduleInput = hasOwnInput(input, 'availabilitySchedule')
  const nextSchedule = hasScheduleInput ? accountAvailabilityScheduleFromRequest(input) : currentSchedule
  const currentScheduleJson = accountAvailabilityScheduleJson(currentSchedule)
  const nextScheduleJson = accountAvailabilityScheduleJson(nextSchedule)
  const scheduleChanged = currentScheduleJson !== nextScheduleJson
  if (scheduleChanged) {
    mainColumns.set('availability_schedule_json', nextScheduleJson)
    mainColumns.set('availability_schedule_next_check_at', nextAccountAvailabilityScheduleCheckAt(nextSchedule, new Date(nowMs)))
    addChange('availabilitySchedule', currentSchedule, nextSchedule)
  }

  const currentExpiresAt = row.account_expires_at ?? null
  const nextExpiresAt = hasOwnInput(input, 'accountExpiresAt')
    ? nullableServerDateTimeIso(input.accountExpiresAt, '账户套餐到期时间')
    : currentExpiresAt
  setColumn('account_expires_at', currentExpiresAt, nextExpiresAt, nextExpiresAt ?? null)
  addChange('accountExpiresAt', currentExpiresAt ?? undefined, nextExpiresAt ?? undefined)

  const currentSchedulable = databaseBoolean(row.schedulable)
  const apiKeyMembershipChanged = credentialsChanged && !accountApiKeyFingerprintSetsEqual(
    currentCredentials,
    nextCredentials
  )
  const baseUrlChanged = credentialsChanged && !isDeepStrictEqual(
    currentCredentials.base_url,
    nextCredentials.base_url
  )
  const retainedActiveApiKey = !baseUrlChanged
    && currentProxyProfileId === nextProxyProfileId
    && isAccountApiKeyPoolIsolationEnabled({
      providerCode: row.provider_code,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      type: row.type,
      credentials: nextCredentials
    })
    && row.status === 'active'
    && currentSchedulable
    && await hasRetainedActiveAccountApiKeyInClient(client, row.id, currentCredentials, nextCredentials)
  const connectionChanged = currentProxyProfileId !== nextProxyProfileId
    || baseUrlChanged
    || (apiKeyMembershipChanged && !retainedActiveApiKey)
  const hasStatusInput = hasOwnInput(input, 'status')
  const requestedStatus = normalizedAccountStatusInput(input.status, row.status)
  assertStatusMutationAllowed(row.status, requestedStatus, hasStatusInput)
  const expiresAtChanged = currentExpiresAt !== nextExpiresAt
  const requestedSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', currentSchedulable, '账户是否参与调度')
  const expiredByPackage = expiresAtChanged && isAccountExpired(nextExpiresAt, nowMs)
  const scheduledStatus = expiredByPackage
    ? 'disabled'
    : scheduleChanged
      ? accountStatusForScheduleMutation({ requestedStatus, schedule: nextSchedule, now: new Date(nowMs) })
      : requestedStatus
  const nextStatus = connectionChanged && scheduledStatus !== 'disabled' && !hasStatusInput
    ? 'pending_test'
    : scheduledStatus
  const statusChanged = row.status !== nextStatus

  const nextSchedulable = expiredByPackage || (statusChanged && accountStatusForcesSchedulableOff(nextStatus))
    ? false
    : statusChanged && nextStatus !== 'disabled'
      ? true
      : requestedSchedulable
  const explicitActivationRequested = hasStatusInput
    && requestedStatus === 'active'
    && (row.status !== 'active' || !currentSchedulable)
  const explicitSchedulingEnableRequested = hasOwnInput(input, 'schedulable')
    && requestedSchedulable
    && !currentSchedulable
  const enablesAccount = explicitActivationRequested
    || explicitSchedulingEnableRequested
    || (row.status !== 'active' && nextStatus === 'active')
    || (!currentSchedulable && nextSchedulable)
  if (enablesAccount && isAccountExpired(nextExpiresAt, nowMs)) {
    throw new Error('账户套餐已到期，不能启用或参与调度')
  }
  setColumn('status', row.status, nextStatus)
  setColumn('schedulable', currentSchedulable, nextSchedulable, nextSchedulable ? 1 : 0)
  addChange('status', row.status, nextStatus)
  addChange('schedulable', currentSchedulable, nextSchedulable)

  const runtimeStateMayChange = statusChanged || connectionChanged || expiredByPackage
  if (runtimeStateMayChange) {
    const runtimeState = nextRuntimeState(row, {
      nextStatus,
      hasStatusInput: statusChanged,
      connectionChanged,
      expiredByPackage,
      nowMs
    })
    const columnsBeforeRuntimeState = mainColumns.size
    applyRuntimeStateColumns(mainColumns, row, runtimeState)
    const derivedRuntimeStateChanged = mainColumns.size > columnsBeforeRuntimeState
    if (derivedRuntimeStateChanged
      && !changedFields.has('status')
      && !changedFields.has('schedulable')) {
      changedFields.add('runtimeState')
      changes.push({ field: 'runtimeState', before: '需归一化', after: '已归一化' })
    }
  }

  const currentContinuousProbe = databaseBoolean(row.temporary_unavailable_continuous_probe_enabled, true)
  const nextContinuousProbe = normalizeOptionalBooleanInput(
    input,
    'temporaryUnavailableContinuousProbeEnabled',
    currentContinuousProbe,
    '临时不可调用持续恢复探活'
  )
  const continuousProbeChanged = currentContinuousProbe !== nextContinuousProbe
  setColumn(
    'temporary_unavailable_continuous_probe_enabled',
    currentContinuousProbe,
    nextContinuousProbe,
    nextContinuousProbe ? 1 : 0
  )
  addChange('temporaryUnavailableContinuousProbeEnabled', currentContinuousProbe, nextContinuousProbe)
  const boundedRecoveryActivated = currentContinuousProbe && !nextContinuousProbe
  const boundedRecoveryGeneration = boundedRecoveryActivated
    ? newCooldownRetestGeneration()
    : null
  if (boundedRecoveryActivated && row.status === 'temporary_unavailable') {
    mainColumns.set('cooldown_retest_failure_count', 0)
    mainColumns.set('cooldown_retest_observation_started_at', now)
    mainColumns.set('cooldown_retest_generation', boundedRecoveryGeneration)
    mainColumns.set('cooldown_retest_last_at', null)
    mainColumns.set('cooldown_retest_last_status_code', null)
    mainColumns.set('cooldown_until', initialCooldownUntilForStatus('temporary_unavailable', nowMs) ?? null)
  }

  const supportedModelsChanged = requiresModelState
    && hasOwnInput(input, 'supportedModels')
    && !unorderedStringListEquals(currentSupportedModels, nextSupportedModels)
  const modelMappingsChanged = requiresModelState
    && (hasOwnInput(input, 'modelMappings') || endpointModesChanged)
    && !accountModelMappingsEqual(currentModelMappings, nextModelMappings)
  const healthCheckModelChanged = row.health_check_model !== nextHealthCheckModel
  const healthCheckEndpointModeChanged = row.health_check_endpoint_mode !== nextHealthCheckEndpointMode
  setColumn('health_check_model', row.health_check_model, nextHealthCheckModel)
  setColumn('health_check_endpoint_mode', row.health_check_endpoint_mode, nextHealthCheckEndpointMode)
  if (supportedModelsChanged) addChange('supportedModels', currentSupportedModels, nextSupportedModels)
  if (modelMappingsChanged) addChange('modelMappings', currentModelMappings, nextModelMappings)
  addChange('healthCheckModel', row.health_check_model, nextHealthCheckModel)
  addChange('healthCheckEndpointMode', row.health_check_endpoint_mode, nextHealthCheckEndpointMode)
  const healthCheckRequired = connectionChanged
    || supportedModelsChanged
    || modelMappingsChanged
    || healthCheckModelChanged
    || healthCheckEndpointModeChanged
    || endpointModesChanged
  if (healthCheckRequired) {
    setColumn('next_health_check_at', row.next_health_check_at, null)
  }
  if (connectionChanged) {
    setColumn('last_health_check_at', row.last_health_check_at, null)
    setColumn('last_health_success_at', row.last_health_success_at, null)
    setColumn('health_check_failure_count', integerValue(row.health_check_failure_count), 0)
    setColumn('health_check_failure_started_at', row.health_check_failure_started_at, null)
    setColumn('last_health_check_status_code', nullableInteger(row.last_health_check_status_code), null)
    setColumn('last_health_check_error_code', row.last_health_check_error_code, null)
    setColumn('last_health_check_error_message', row.last_health_check_error_message, null)
    setColumn('last_health_check_trace_id', row.last_health_check_trace_id, null)
  }

  const currentTagNames = hasOwnInput(input, 'tags') ? await loadTagNamesInClient(client, row.id) : []
  const nextTagNames = hasOwnInput(input, 'tags') ? normalizeAccountTagNamesInput(input.tags) ?? [] : []
  const tagsChanged = hasOwnInput(input, 'tags') && !unorderedStringListEquals(currentTagNames, nextTagNames)
  if (tagsChanged) addChange('tags', currentTagNames, nextTagNames)

  const currentGroupId = hasOwnInput(input, 'groupId')
    ? await loadEnabledGroupIdInClient(client, row.id, row.system_account_id)
    : undefined
  const requestedGroupId = hasOwnInput(input, 'groupId') ? requiredTextInput(input.groupId, '账户分组') : undefined
  const groupChanged = requestedGroupId !== undefined && requestedGroupId !== currentGroupId
  if (groupChanged) {
    await assertGroupCanBindInClient(client, requestedGroupId, row)
    addChange('groupId', currentGroupId, requestedGroupId)
  }

  const currentBalanceConfig = parseBalanceConfig(row.balance_query_config_json)
  const currentBalanceEnabled = databaseBoolean(row.balance_query_enabled)
  const balanceRelevant = hasOwnInput(input, 'balanceQueryEnabled')
    || hasOwnInput(input, 'balanceQueryConfig')
    || credentialsChanged
    || currentProxyProfileId !== nextProxyProfileId
  let balanceIdentityChanged = false
  let balanceAutoDisabledForMultipleApiKeys = false
  if (balanceRelevant) {
    const requestedBalanceEnabled = hasOwnInput(input, 'balanceQueryEnabled')
      ? input.balanceQueryEnabled === true
      : currentBalanceEnabled
    const balanceDecision = validateAccountBalanceCapability({
      type: row.type,
      credentials: nextCredentials,
      authorizationInstanceAuthorizationId: row.authorization_instance_authorization_id
    }, requestedBalanceEnabled)
    balanceAutoDisabledForMultipleApiKeys = balanceDecision.autoDisabledForMultipleApiKeys
    const hasBalanceConfig = hasOwnInput(input, 'balanceQueryConfig')
    let nextBalanceConfig = hasBalanceConfig
      ? normalizeAccountBalanceConfig(input.balanceQueryConfig)
      : currentBalanceConfig
    if (balanceDecision.autoDisabledForMultipleApiKeys && !nextBalanceConfig) {
      nextBalanceConfig = { ...defaultDisabledMultiKeyBalanceConfig }
    }
    if (balanceDecision.enabled && !nextBalanceConfig) {
      throw new Error('开启上游余额查询时必须选择查询类型')
    }
    balanceIdentityChanged = !isDeepStrictEqual(
      accountBalanceQueryIdentity({
        enabled: currentBalanceEnabled,
        config: currentBalanceConfig,
        providerCode: row.provider_code,
        accountType: row.type,
        credentials: currentCredentials,
        proxyProfileId: currentProxyProfileId
      }),
      accountBalanceQueryIdentity({
        enabled: balanceDecision.enabled,
        config: nextBalanceConfig,
        providerCode: row.provider_code,
        accountType: row.type,
        credentials: nextCredentials,
        proxyProfileId: nextProxyProfileId
      })
    )
    setColumn('balance_query_enabled', currentBalanceEnabled, balanceDecision.enabled, balanceDecision.enabled ? 1 : 0)
    setColumn(
      'balance_query_config_json',
      currentBalanceConfig,
      nextBalanceConfig,
      JSON.stringify(nextBalanceConfig ?? {})
    )
    const nextBalanceRefreshAt = balanceIdentityChanged && balanceDecision.enabled
      ? now
      : balanceDecision.enabled
        ? row.balance_query_next_refresh_at
        : null
    setColumn('balance_query_next_refresh_at', row.balance_query_next_refresh_at, nextBalanceRefreshAt)
    addChange('balanceQueryEnabled', currentBalanceEnabled, balanceDecision.enabled)
    addChange('balanceQueryConfig', currentBalanceConfig, nextBalanceConfig)
  }

  const relationChanged = supportedModelsChanged || modelMappingsChanged || tagsChanged || groupChanged
  if (mainColumns.size === 0 && !relationChanged) return unchangedPatchResult(row)

  const updateResult = await executeAccountCasUpdate(client, row, mainColumns, now)
  if (updateResult !== 1) {
    throw new AccountManagementPatchRevisionConflictError(row.id, integerValue(row.config_revision))
  }
  const apiKeyProbeScheduled = retainedActiveApiKey
    && credentialsChanged
    && await initializeAddedAccountApiKeyRuntimeStatesInClient(client, {
      accountId: row.id,
      systemAccountId: row.system_account_id,
      providerCode: row.provider_code,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      type: row.type,
      currentCredentials,
      nextCredentials,
      now
    })

  let renamedAuthorizationInstanceIds: string[] = []
  if (row.name !== nextName) {
    await replaceAccountNameSearchTermsAsync(client, row.id, row.system_account_id, nextName, now)
    renamedAuthorizationInstanceIds = await syncAuthorizationInstanceNamesInClient(client, row.id, nextName, now)
  }
  if (continuousProbeChanged) {
    const instanceRows = await client.query<{ id: string }>(`
      SELECT id
      FROM ${patchTable(client, 'accounts')}
      WHERE authorization_instance_source_account_id = ?
        AND deleted_at IS NULL
      ORDER BY id ASC${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [row.id])
    if (instanceRows.length > 0) {
      await client.execute(`
        UPDATE ${patchTable(client, 'accounts')}
        SET temporary_unavailable_continuous_probe_enabled = ?,
            config_revision = config_revision + 1,
            cooldown_retest_failure_count = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN 0 ELSE cooldown_retest_failure_count END,
            cooldown_retest_observation_started_at = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_retest_observation_started_at END,
            cooldown_retest_generation = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_retest_generation END,
            cooldown_retest_last_at = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN NULL ELSE cooldown_retest_last_at END,
            cooldown_retest_last_status_code = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN NULL ELSE cooldown_retest_last_status_code END,
            cooldown_until = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_until END,
            updated_at = ?
        WHERE authorization_instance_source_account_id = ?
          AND deleted_at IS NULL
      `, [
        nextContinuousProbe ? 1 : 0,
        boundedRecoveryActivated ? 1 : 0,
        boundedRecoveryActivated ? 1 : 0,
        boundedRecoveryActivated ? now : null,
        boundedRecoveryActivated ? 1 : 0,
        boundedRecoveryGeneration,
        boundedRecoveryActivated ? 1 : 0,
        boundedRecoveryActivated ? 1 : 0,
        boundedRecoveryActivated ? 1 : 0,
        boundedRecoveryActivated ? initialCooldownUntilForStatus('temporary_unavailable', nowMs) ?? null : null,
        now,
        row.id
      ])
      renamedAuthorizationInstanceIds.push(...instanceRows.map((item) => item.id))
    }
  }
  if (supportedModelsChanged) {
    await replaceAccountSupportedModelsInClientAsync(client, row.id, row.provider_code, nextSupportedModels)
  }
  if (modelMappingsChanged) {
    await replaceAccountModelMappingsInClientAsync(client, row.id, row.provider_code, nextModelMappings)
  }
  if (tagsChanged) {
    await replaceAccountTagsAsync(client, row.id, row.system_account_id, nextTagNames, now)
  }
  if (groupChanged && requestedGroupId) {
    await replaceGroupBindingInClient(client, row, currentGroupId, requestedGroupId, {
      priority: nextPriority,
      superPriorityEnabled: nextSuperPriority,
      fallbackEnabled: nextFallback
    }, now)
  } else if (dispatchBindingChanged) {
    await client.execute(`
      UPDATE ${patchTable(client, 'group_accounts')}
      SET local_priority = ?,
          local_super_priority_enabled = ?,
          local_fallback_enabled = ?,
          updated_at = ?
      WHERE account_id = ?
        AND system_account_id = ?
        AND enabled = 1
    `, [
      nextPriority,
      nextSuperPriority ? 1 : 0,
      nextFallback ? 1 : 0,
      now,
      row.id,
      row.system_account_id
    ])
  }

  const circuitOwnerChanged = !isDeepStrictEqual(
    accountCircuitCredentialOwnerIdentity(currentCredentials),
    accountCircuitCredentialOwnerIdentity(nextCredentials)
  ) || currentProxyProfileId !== nextProxyProfileId || row.client_compatibility !== nextClientCompatibility
  if (circuitOwnerChanged) {
    await advanceAccountCircuitDispatchRevisionFamilyInTransaction(client, {
      accountId: row.id,
      accountRuntimeKey: row.id,
      transitionId: newId('dispatch'),
      nowMs
    })
  }

  const statsFields = new Set(['status', 'schedulable', 'concurrencyLimit', 'runtimeState'])
  const groupStatsAffected = apiKeyProbeScheduled || groupChanged || [...changedFields].some((field) => statsFields.has(field))
  const gatewayFields = new Set([
    'status', 'schedulable', 'concurrencyLimit', 'priority', 'superPriorityEnabled', 'fallbackEnabled',
    'proxyProfileId', 'clientCompatibility', 'supportedModels', 'modelMappings', 'healthCheckModel',
    'healthCheckEndpointMode', 'availabilitySchedule', 'accountExpiresAt',
    'temporaryUnavailableContinuousProbeEnabled', 'runtimeState'
  ])
  const gatewayRuntimeAffected = groupChanged
    || credentialsChanged
    || [...changedFields].some((field) => gatewayFields.has(field))
  const authorizationDependencyFields = new Set([
    'name', 'status', 'schedulable', 'concurrencyLimit', 'proxyProfileId', 'clientCompatibility',
    'supportedModels', 'modelMappings', 'healthCheckModel', 'healthCheckEndpointMode',
    'availabilitySchedule', 'accountExpiresAt', 'temporaryUnavailableContinuousProbeEnabled', 'runtimeState'
  ])
  const authorizationInstancesAffected = credentialsChanged
    || [...changedFields].some((field) => authorizationDependencyFields.has(field))
  const accountHealthInputAffected = credentialsChanged
    || supportedModelsChanged
    || modelMappingsChanged
    || groupChanged
    || [...changedFields].some((field) => new Set([
      'status', 'schedulable', 'proxyProfileId', 'healthCheckModel', 'healthCheckEndpointMode',
      'availabilitySchedule', 'accountExpiresAt', 'temporaryUnavailableContinuousProbeEnabled'
    ]).has(field))
  if (accountHealthInputAffected) {
    const revision = await client.one<{ config_revision: number | string | bigint, dispatch_revision: number | string | bigint }>(`
      SELECT config_revision, dispatch_revision
      FROM ${patchTable(client, 'accounts')}
      WHERE id = ?
    `, [row.id])
    if (!revision) throw new Error('J1 input outbox 找不到已更新账户')
    await reserveAndEnqueueAccountHealthJobsInputInTransactionAsync(client, {
      accountId: row.id,
      configRevision: Number(revision.config_revision),
      dispatchRevision: Number(revision.dispatch_revision),
      kind: 'snapshot',
      reason: 'account_management_patch'
    })
  }
  if (authorizationInstancesAffected) {
    const instances = await client.query<{ id: string, config_revision: number | string | bigint, dispatch_revision: number | string | bigint }>(`
      SELECT id, config_revision, dispatch_revision
      FROM ${patchTable(client, 'accounts')}
      WHERE authorization_instance_source_account_id = ?
        AND deleted_at IS NULL
        AND provider_code IN ('gpt', 'openai', 'xai', 'anthropic', 'deepseek', 'glm', 'gemini', 'hybrid')
        AND type IN ('api_key', 'oauth', 'google_oauth')
      ORDER BY id ASC
      ${client.driver === 'postgres' ? 'FOR UPDATE' : ''}
    `, [row.id])
    for (const instance of instances) {
      await reserveAndEnqueueAccountHealthJobsInputInTransactionAsync(client, {
        accountId: instance.id,
        configRevision: Number(instance.config_revision),
        dispatchRevision: Number(instance.dispatch_revision),
        kind: 'snapshot',
        reason: 'authorization_source_account_changed'
      })
    }
  }
  return {
    id: row.id,
    configRevision: integerValue(row.config_revision) + 1,
    changedFields: [...changedFields].sort(),
    authorizationInstancesAffected,
    changes,
    name: nextName,
    ownerSystemAccountId: row.system_account_id,
    status: nextStatus,
    previousStatus: row.status,
    healthCheckRequired,
    healthCheckReason: healthCheckRequired ? 'configuration' : undefined,
    runtimeRestoreRequired: row.status !== 'active' && nextStatus === 'active',
    balanceIdentityChanged,
    balanceAutoDisabledForMultipleApiKeys,
    previousGroupId: groupChanged ? currentGroupId : undefined,
    nextGroupId: groupChanged ? requestedGroupId : undefined,
    renamedAuthorizationInstanceIds: [...new Set(renamedAuthorizationInstanceIds)],
    groupStatsAffected,
    gatewayRuntimeAffected,
    accountLookupAffected: row.name !== nextName || currentExpiresAt !== nextExpiresAt
  }
}

async function patchAuthorizedAccountLocalInTransaction(
  context: PatchContext,
  requestedKeys: string[]
): Promise<AccountPatchTransactionResult> {
  const { client, row, input, now } = context
  const allowedFields = new Set([
    'groupId',
    'tags',
    'priority',
    'superPriorityEnabled',
    'fallbackEnabled'
  ])
  const sourceControlledFields = requestedKeys.filter((field) => !allowedFields.has(field))
  if (sourceControlledFields.length > 0) {
    throw new Error('授权账户配置由来源账户控制，不能在被授权账户上修改')
  }
  if (!requestedKeys.length) return unchangedPatchResult(row)
  if (!row.authorization_instance_source_account_id || !row.authorization_instance_authorization_id) {
    throw new Error('账户授权已失效')
  }

  const changes: AccountManagementPatchChange[] = []
  const changedFields = new Set<string>()
  const addChange = (field: string, before: unknown, after: unknown): void => {
    if (isDeepStrictEqual(before, after)) return
    changedFields.add(field)
    changes.push({ field, before, after })
  }
  const mainColumns = new Map<string, unknown>()
  const currentPriority = integerValue(row.priority)
  const currentSuperPriority = databaseBoolean(row.super_priority_enabled)
  const currentFallback = databaseBoolean(row.fallback_enabled)
  const hasSuperPriorityInput = hasOwnInput(input, 'superPriorityEnabled')
  const hasFallbackInput = hasOwnInput(input, 'fallbackEnabled')
  const nextPriority = normalizedOptionalDispatchPriority(input.priority, currentPriority)
  let nextSuperPriority = normalizeSuperPriorityInput(input.superPriorityEnabled, currentSuperPriority)
  let nextFallback = normalizeFallbackInput(input.fallbackEnabled, currentFallback)
  if (hasSuperPriorityInput && nextSuperPriority && hasFallbackInput && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  if (hasSuperPriorityInput && nextSuperPriority) nextFallback = false
  if (hasFallbackInput && nextFallback) nextSuperPriority = false
  setMapIfChanged(mainColumns, 'priority', currentPriority, nextPriority)
  setMapIfChanged(mainColumns, 'super_priority_enabled', currentSuperPriority, nextSuperPriority, nextSuperPriority ? 1 : 0)
  setMapIfChanged(mainColumns, 'fallback_enabled', currentFallback, nextFallback, nextFallback ? 1 : 0)
  addChange('priority', currentPriority, nextPriority)
  addChange('superPriorityEnabled', currentSuperPriority, nextSuperPriority)
  addChange('fallbackEnabled', currentFallback, nextFallback)
  const dispatchBindingChanged = currentPriority !== nextPriority
    || currentSuperPriority !== nextSuperPriority
    || currentFallback !== nextFallback

  const currentTagNames = hasOwnInput(input, 'tags') ? await loadTagNamesInClient(client, row.id) : []
  const nextTagNames = hasOwnInput(input, 'tags') ? normalizeAccountTagNamesInput(input.tags) ?? [] : []
  const tagsChanged = hasOwnInput(input, 'tags') && !unorderedStringListEquals(currentTagNames, nextTagNames)
  if (tagsChanged) addChange('tags', currentTagNames, nextTagNames)

  const currentGroupId = hasOwnInput(input, 'groupId')
    ? await loadEnabledGroupIdInClient(client, row.id, row.system_account_id)
    : undefined
  const requestedGroupId = hasOwnInput(input, 'groupId') ? requiredTextInput(input.groupId, '账户分组') : undefined
  const groupChanged = requestedGroupId !== undefined && requestedGroupId !== currentGroupId
  if (groupChanged && requestedGroupId) {
    await assertGroupCanBindInClient(client, requestedGroupId, row)
    addChange('groupId', currentGroupId, requestedGroupId)
  }

  if (mainColumns.size === 0 && !tagsChanged && !groupChanged) return unchangedPatchResult(row)
  const updated = await executeAccountCasUpdate(client, row, mainColumns, now)
  if (updated !== 1) {
    throw new AccountManagementPatchRevisionConflictError(row.id, integerValue(row.config_revision))
  }
  if (tagsChanged) {
    await replaceAccountTagsAsync(client, row.id, row.system_account_id, nextTagNames, now)
  }
  if (groupChanged && requestedGroupId) {
    await replaceGroupBindingInClient(client, row, currentGroupId, requestedGroupId, {
      priority: nextPriority,
      superPriorityEnabled: nextSuperPriority,
      fallbackEnabled: nextFallback
    }, now)
  } else if (dispatchBindingChanged) {
    await client.execute(`
      UPDATE ${patchTable(client, 'group_accounts')}
      SET local_priority = ?,
          local_super_priority_enabled = ?,
          local_fallback_enabled = ?,
          updated_at = ?
      WHERE account_id = ?
        AND system_account_id = ?
        AND account_authorization_id = ?
        AND enabled = 1
    `, [
      nextPriority,
      nextSuperPriority ? 1 : 0,
      nextFallback ? 1 : 0,
      now,
      row.id,
      row.system_account_id,
      row.authorization_instance_authorization_id
    ])
  }
  return {
    id: row.id,
    configRevision: integerValue(row.config_revision) + 1,
    changedFields: [...changedFields].sort(),
    authorizationInstancesAffected: false,
    changes,
    name: row.name,
    ownerSystemAccountId: row.system_account_id,
    status: row.status,
    previousStatus: row.status,
    healthCheckRequired: false,
    runtimeRestoreRequired: false,
    balanceIdentityChanged: false,
    balanceAutoDisabledForMultipleApiKeys: false,
    authorizedBinding: currentGroupId && row.authorization_instance_authorization_id
      ? {
          systemAccountId: row.system_account_id,
          groupId: requestedGroupId ?? currentGroupId,
          accountAuthorizationId: row.authorization_instance_authorization_id
        }
      : undefined,
    previousGroupId: groupChanged ? currentGroupId : undefined,
    nextGroupId: groupChanged ? requestedGroupId : undefined,
    renamedAuthorizationInstanceIds: [],
    groupStatsAffected: groupChanged || dispatchBindingChanged,
    gatewayRuntimeAffected: groupChanged || dispatchBindingChanged,
    accountLookupAffected: tagsChanged
  }
}

async function patchAccountFailureStateInTransaction(context: PatchContext): Promise<AccountPatchTransactionResult> {
  const { client, row, input, now, nowMs } = context
  const mixedFields = Object.keys(input).filter((key) => !['expectedConfigRevision', 'clearFailureState'].includes(key))
  if (mixedFields.length > 0) {
    throw new Error('重新检查或异常恢复不能与账户字段修改同时提交')
  }
  let authorizedBinding: AccountManagementPatchResult['authorizedBinding']
  if (row.authorization_instance_authorization_id) {
    const binding = await client.one<{ group_id: string; account_authorization_id: string }>(`
      SELECT group_id, account_authorization_id
      FROM ${patchTable(client, 'group_accounts')}
      WHERE account_id = ?
        AND system_account_id = ?
        AND account_authorization_id = ?
        AND enabled = 1
      ORDER BY updated_at DESC, group_id ASC
      LIMIT 1${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [row.id, row.system_account_id, row.authorization_instance_authorization_id])
    if (!binding?.group_id || !binding.account_authorization_id) return unchangedPatchResult(row)
    authorizedBinding = {
      systemAccountId: row.system_account_id,
      groupId: binding.group_id,
      accountAuthorizationId: binding.account_authorization_id
    }
  }
  if (row.status === 'pending_test'
    && !(row.last_health_check_at && (row.last_health_check_error_code || row.last_health_check_error_message))) {
    throw new Error('账户正在等待首次后台健康检查，无需重新检查')
  }
  const expiredByPackage = isAccountExpired(row.account_expires_at, nowMs)
  if (row.status === 'disabled' && !expiredByPackage) return unchangedPatchResult(row)
  if (authorizedBinding && row.status === 'disabled') return unchangedPatchResult(row)

  const mainColumns = new Map<string, unknown>()
  const nextStatus: AccountStatus = expiredByPackage
    ? 'disabled'
    : row.status === 'pending_test' || row.status === 'error'
      ? 'pending_test'
      : 'active'
  setMapIfChanged(mainColumns, 'status', row.status, nextStatus)
  setMapIfChanged(mainColumns, 'schedulable', databaseBoolean(row.schedulable), nextStatus === 'active', nextStatus === 'active' ? 1 : 0)
  setMapIfChanged(mainColumns, 'cooldown_until', row.cooldown_until, null)
  setMapIfChanged(mainColumns, 'last_error_code', row.last_error_code, expiredByPackage ? 'account_expired' : null)
  setMapIfChanged(
    mainColumns,
    'last_error_message',
    row.last_error_message,
    expiredByPackage
      ? '账户套餐已过期，已自动停用'
      : nextStatus === 'pending_test'
        ? '账户已重置，等待后台健康检查'
        : null
  )
  setMapIfChanged(mainColumns, 'last_error_trace_id', row.last_error_trace_id, null)
  setMapIfChanged(mainColumns, 'cooldown_retest_failure_count', integerValue(row.cooldown_retest_failure_count), 0)
  setMapIfChanged(mainColumns, 'cooldown_retest_observation_started_at', row.cooldown_retest_observation_started_at, null)
  setMapIfChanged(mainColumns, 'cooldown_retest_generation', row.cooldown_retest_generation, null)
  setMapIfChanged(mainColumns, 'cooldown_retest_last_at', row.cooldown_retest_last_at, null)
  setMapIfChanged(mainColumns, 'cooldown_retest_last_status_code', nullableInteger(row.cooldown_retest_last_status_code), null)
  setMapIfChanged(mainColumns, 'stream_failure_count', integerValue(row.stream_failure_count), 0)
  setMapIfChanged(mainColumns, 'stream_failure_window_started_at', row.stream_failure_window_started_at, null)
  if (nextStatus === 'pending_test') {
    setMapIfChanged(mainColumns, 'last_health_check_at', row.last_health_check_at, null)
    setMapIfChanged(mainColumns, 'next_health_check_at', row.next_health_check_at, null)
    setMapIfChanged(mainColumns, 'last_health_success_at', row.last_health_success_at, null)
    setMapIfChanged(mainColumns, 'health_check_failure_count', integerValue(row.health_check_failure_count), 0)
    setMapIfChanged(mainColumns, 'health_check_failure_started_at', row.health_check_failure_started_at, null)
    setMapIfChanged(mainColumns, 'last_health_check_status_code', nullableInteger(row.last_health_check_status_code), null)
    setMapIfChanged(mainColumns, 'last_health_check_error_code', row.last_health_check_error_code, null)
    setMapIfChanged(mainColumns, 'last_health_check_error_message', row.last_health_check_error_message, null)
    setMapIfChanged(mainColumns, 'last_health_check_trace_id', row.last_health_check_trace_id, null)
  }
  if (mainColumns.size === 0) return unchangedPatchResult(row)
  const updated = await executeAccountCasUpdate(client, row, mainColumns, now)
  if (updated !== 1) {
    throw new AccountManagementPatchRevisionConflictError(row.id, integerValue(row.config_revision))
  }
  return {
    id: row.id,
    configRevision: integerValue(row.config_revision) + 1,
    changedFields: ['clearFailureState'],
    authorizationInstancesAffected: true,
    changes: [{ field: 'clearFailureState', before: false, after: true }],
    name: row.name,
    ownerSystemAccountId: row.system_account_id,
    status: nextStatus,
    previousStatus: row.status,
    healthCheckRequired: nextStatus === 'pending_test',
    healthCheckReason: nextStatus === 'pending_test' ? 'activation' : undefined,
    runtimeRestoreRequired: true,
    authorizedBinding,
    balanceIdentityChanged: false,
    balanceAutoDisabledForMultipleApiKeys: false,
    renamedAuthorizationInstanceIds: [],
    groupStatsAffected: true,
    gatewayRuntimeAffected: true,
    accountLookupAffected: false
  }
}

async function loadAccountPatchRowForUpdate(
  client: DatabaseClient,
  id: string,
  input: AccountManagementPatchInput,
  access?: AccessScope
): Promise<AccountPatchRow | undefined> {
  const scopedOwnerId = manageableSystemAccountId(access)
  if (!scopedOwnerId && !canAccessAll(access)) return undefined
  const columns = accountManagementPatchProjection(input)
    .map((column) => client.dialect.quoteIdentifier(column))
    .join(', ')
  const ownerScopeClause = scopedOwnerId
    ? ` AND ${client.dialect.quoteIdentifier('system_account_id')} = ?`
    : ''
  return client.one<AccountPatchRow>(`
    SELECT ${columns}
    FROM ${patchTable(client, 'accounts')}
    WHERE id = ?
      AND deleted_at IS NULL${ownerScopeClause}${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, scopedOwnerId ? [id, scopedOwnerId] : [id])
}

function accountManagementPatchProjection(input: AccountManagementPatchInput): string[] {
  const columns = new Set<string>([
    'id',
    'config_revision',
    'system_account_id',
    'name',
    'status',
    'authorization_instance_source_account_id',
    'authorization_instance_authorization_id'
  ])
  const add = (...values: string[]): void => {
    for (const value of values) columns.add(value)
  }
  const addRuntimeState = (): void => add(
    'cooldown_until',
    'last_error_code',
    'last_error_message',
    'last_error_trace_id',
    'cooldown_retest_failure_count',
    'cooldown_retest_observation_started_at',
    'cooldown_retest_generation',
    'cooldown_retest_last_at',
    'cooldown_retest_last_status_code'
  )
  const addHealthState = (): void => add(
    'last_health_check_at',
    'next_health_check_at',
    'last_health_success_at',
    'health_check_failure_count',
    'health_check_failure_started_at',
    'last_health_check_status_code',
    'last_health_check_error_code',
    'last_health_check_error_message',
    'last_health_check_trace_id'
  )

  if (input.clearFailureState === true) {
    add('schedulable', 'account_expires_at', 'stream_failure_count', 'stream_failure_window_started_at')
    addRuntimeState()
    addHealthState()
    return [...columns]
  }

  if (hasOwnInput(input, 'notes')) add('notes')
  if (hasOwnInput(input, 'concurrencyLimit')) add('concurrency_limit')
  if (hasOwnInput(input, 'availabilitySchedule')) {
    add('availability_schedule_json', 'availability_schedule_next_check_at', 'schedulable', 'account_expires_at')
    addRuntimeState()
  }
  if (hasOwnInput(input, 'accountExpiresAt')) {
    add('account_expires_at', 'schedulable')
    addRuntimeState()
  }
  if (hasOwnInput(input, 'status')) {
    add('schedulable', 'account_expires_at')
    addRuntimeState()
  }
  if (hasOwnInput(input, 'schedulable')) add('schedulable', 'account_expires_at')
  if (hasOwnInput(input, 'temporaryUnavailableContinuousProbeEnabled')) {
    add('temporary_unavailable_continuous_probe_enabled')
    addRuntimeState()
  }

  const dispatchRelevant = [
    'priority',
    'superPriorityEnabled',
    'fallbackEnabled',
    'groupId'
  ].some((field) => hasOwnInput(input, field))
  if (dispatchRelevant) add('priority', 'super_priority_enabled', 'fallback_enabled')
  if (hasOwnInput(input, 'groupId')) add('provider_code')

  const modelRelevant = [
    'supportedModels',
    'healthCheckModel',
    'healthCheckEndpointMode',
    'modelMappings',
    'credentials',
    'credentialsPatch'
  ].some((field) => hasOwnInput(input, field))
  if (accountManagementPatchNeedsCredentials(input)) {
    add(
      'provider_code',
      'provider_protocol_profile_id',
      'protocol_code',
      'protocol_version',
      'type',
      'credentials_encrypted',
      'client_compatibility',
      'proxy_profile_id'
    )
  }
  if (modelRelevant) add('health_check_model', 'health_check_endpoint_mode', 'next_health_check_at')

  const connectionRelevant = ['credentials', 'credentialsPatch', 'proxyProfileId']
    .some((field) => hasOwnInput(input, field))
  if (connectionRelevant) {
    add('schedulable')
    addRuntimeState()
    addHealthState()
  }

  const balanceRelevant = [
    'balanceQueryEnabled',
    'balanceQueryConfig',
    'credentials',
    'credentialsPatch',
    'proxyProfileId'
  ].some((field) => hasOwnInput(input, field))
  if (balanceRelevant) {
    add('balance_query_enabled', 'balance_query_config_json', 'balance_query_next_refresh_at')
  }
  return [...columns]
}

async function authorizedInstanceIsActiveInClient(
  client: DatabaseClient,
  row: AccountPatchRow,
  now: string
): Promise<boolean> {
  if (!row.authorization_instance_source_account_id || !row.authorization_instance_authorization_id) return false
  const authorization = await client.one<{ id: string }>(`
    SELECT id
    FROM ${patchTable(client, 'resource_authorizations')}
    WHERE id = ?
      AND resource_type = 'account'
      AND resource_id = ?
      AND grantee_system_account_id = ?
      AND status = 'active'
      AND (expires_at IS NULL OR expires_at > ?)
    LIMIT 1${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [
    row.authorization_instance_authorization_id,
    row.authorization_instance_source_account_id,
    row.system_account_id,
    now
  ])
  return Boolean(authorization)
}

function accountManagementPatchNeedsCredentials(input: AccountManagementPatchInput): boolean {
  return [
    'credentials',
    'credentialsPatch',
    'supportedModels',
    'healthCheckModel',
    'healthCheckEndpointMode',
    'modelMappings',
    'proxyProfileId',
    'balanceQueryEnabled',
    'balanceQueryConfig'
  ].some((field) => hasOwnInput(input, field))
}

async function executeAccountCasUpdate(
  client: DatabaseClient,
  row: AccountPatchRow,
  columns: ReadonlyMap<string, unknown>,
  updatedAt: string
): Promise<number> {
  const assignments = [...columns.keys()].map((column) => `${client.dialect.quoteIdentifier(column)} = ?`)
  assignments.push('config_revision = config_revision + 1', 'updated_at = ?')
  const result = await client.execute(`
    UPDATE ${patchTable(client, 'accounts')}
    SET ${assignments.join(', ')}
    WHERE id = ?
      AND system_account_id = ?
      AND config_revision = ?
      AND deleted_at IS NULL
  `, [...columns.values(), updatedAt, row.id, row.system_account_id, integerValue(row.config_revision)])
  return result.changes
}

function accountApiKeyFingerprintSetsEqual(
  currentCredentials: Record<string, unknown>,
  nextCredentials: Record<string, unknown>
): boolean {
  const current = new Set(accountApiKeyEntries(currentCredentials).map((entry) => entry.fingerprint))
  const next = new Set(accountApiKeyEntries(nextCredentials).map((entry) => entry.fingerprint))
  if (current.size !== next.size) return false
  return [...current].every((fingerprint) => next.has(fingerprint))
}

async function hasRetainedActiveAccountApiKeyInClient(
  client: DatabaseClient,
  accountId: string,
  currentCredentials: Record<string, unknown>,
  nextCredentials: Record<string, unknown>
): Promise<boolean> {
  const nextFingerprints = new Set(accountApiKeyEntries(nextCredentials).map((entry) => entry.fingerprint))
  if (!nextFingerprints.size) return false
  const runtimeStatusByFingerprint = new Map(
    (await loadAccountApiKeyRuntimeStatesForAccountInClient(client, accountId))
      .map((state) => [state.keyFingerprint, state.status])
  )
  return accountApiKeyEntries(currentCredentials).some((entry) => (
    nextFingerprints.has(entry.fingerprint)
    && (runtimeStatusByFingerprint.get(entry.fingerprint) ?? 'active') === 'active'
  ))
}

async function normalizedSupportedModelsForPatch(
  row: AccountPatchRow,
  input: AccountManagementPatchInput,
  current: string[],
  validationContext?: AccountModelValidationContext
): Promise<string[]> {
  if (!hasOwnInput(input, 'supportedModels')) return current
  const normalized = normalizeAccountSupportedModelsInput(input.supportedModels) ?? []
  if (unorderedStringListEquals(current, normalized)) return normalized
  const profile = protocolProfileFromRow(row)
  return await normalizeAccountSupportedModelsForProviderAsync(
    input.supportedModels,
    row.provider_code,
    row.system_account_id,
    profile,
    false,
    validationContext
  ) ?? []
}

async function normalizedModelMappingsForPatch(
  row: AccountPatchRow,
  input: AccountManagementPatchInput,
  current: AccountModelMapping[],
  credentials: Record<string, unknown>,
  endpointModesChanged: boolean,
  validationContext?: AccountModelValidationContext
): Promise<AccountModelMapping[]> {
  const hasInput = hasOwnInput(input, 'modelMappings')
  if (!hasInput && !endpointModesChanged) return current
  const source = hasInput ? input.modelMappings : current
  const normalized = normalizeAccountModelMappingsInput(source) ?? []
  if (!endpointModesChanged && accountModelMappingsEqual(current, normalized)) return normalized
  const profile = protocolProfileFromRow(row)
  const options = { supportedEndpointModes: supportedEndpointModes(credentials) }
  return await normalizeAccountModelMappingsForProviderAsync(source, row.provider_code, row.system_account_id, profile, {
    ...options,
    validationContext
  }) ?? []
}

async function loadSupportedModelsInClient(client: DatabaseClient, accountId: string): Promise<string[]> {
  const rows = await client.query<{ model: string }>(`
    SELECT model
    FROM ${patchTable(client, 'account_supported_models')}
    WHERE account_id = ?
    ORDER BY model ASC
  `, [accountId])
  return rows.map((row) => row.model)
}

async function loadModelMappingsInClient(client: DatabaseClient, accountId: string): Promise<AccountModelMapping[]> {
  const rows = await client.query<{
    source_model: string
    source_endpoint_family: AccountModelMapping['sourceEndpointFamily']
    upstream_model: string
    upstream_endpoint_family: AccountModelMapping['upstreamEndpointFamily']
    enabled: number | boolean | string
  }>(`
    SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
    FROM ${patchTable(client, 'account_model_mappings')}
    WHERE account_id = ?
    ORDER BY source_model ASC, source_endpoint_family ASC
  `, [accountId])
  return rows.map((row) => ({
    sourceModel: row.source_model,
    sourceEndpointFamily: row.source_endpoint_family,
    upstreamModel: row.upstream_model,
    upstreamEndpointFamily: row.upstream_endpoint_family,
    enabled: databaseBoolean(row.enabled)
  }))
}

async function loadTagNamesInClient(client: DatabaseClient, accountId: string): Promise<string[]> {
  const rows = await client.query<{ name: string }>(`
    SELECT account_tags.name
    FROM ${patchTable(client, 'account_tag_bindings')} account_tag_bindings
    INNER JOIN ${patchTable(client, 'account_tags')} account_tags
      ON account_tags.id = account_tag_bindings.tag_id
    WHERE account_tag_bindings.account_id = ?
    ORDER BY account_tags.name ASC
  `, [accountId])
  return rows.map((row) => row.name)
}

async function loadEnabledGroupIdInClient(
  client: DatabaseClient,
  accountId: string,
  systemAccountId: string
): Promise<string | undefined> {
  const row = await client.one<{ group_id: string }>(`
    SELECT group_id
    FROM ${patchTable(client, 'group_accounts')}
    WHERE account_id = ?
      AND system_account_id = ?
      AND enabled = 1
    ORDER BY updated_at DESC, group_id ASC, account_id ASC
    LIMIT 1${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [accountId, systemAccountId])
  return row?.group_id
}

async function assertGroupCanBindInClient(client: DatabaseClient, groupId: string, account: AccountPatchRow): Promise<void> {
  const group = await client.one<{
    system_account_id: string
    provider_code: string
    enabled: number | boolean | string
  }>(`
    SELECT system_account_id, provider_code, enabled
    FROM ${patchTable(client, 'groups')}
    WHERE id = ?${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [groupId])
  if (!group
    || group.system_account_id !== account.system_account_id
    || group.provider_code !== account.provider_code
    || !databaseBoolean(group.enabled)) {
    throw new Error('账户分组无效')
  }
}

async function replaceGroupBindingInClient(
  client: DatabaseClient,
  account: AccountPatchRow,
  _previousGroupId: string | undefined,
  groupId: string,
  dispatch: { priority: number; superPriorityEnabled: boolean; fallbackEnabled: boolean },
  timestamp: string
): Promise<void> {
  await client.execute(`
    DELETE FROM ${patchTable(client, 'group_accounts')}
    WHERE account_id = ?
      AND system_account_id = ?
  `, [account.id, account.system_account_id])
  await client.execute(`
    INSERT INTO ${patchTable(client, 'group_accounts')} (
      system_account_id, group_id, account_id, account_authorization_id,
      local_priority, local_super_priority_enabled, local_fallback_enabled,
      enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
    ON CONFLICT(group_id, account_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      account_authorization_id = excluded.account_authorization_id,
      local_priority = excluded.local_priority,
      local_super_priority_enabled = excluded.local_super_priority_enabled,
      local_fallback_enabled = excluded.local_fallback_enabled,
      enabled = 1,
      updated_at = excluded.updated_at
  `, [
    account.system_account_id,
    groupId,
    account.id,
    account.authorization_instance_authorization_id,
    dispatch.priority,
    dispatch.superPriorityEnabled ? 1 : 0,
    dispatch.fallbackEnabled ? 1 : 0,
    timestamp,
    timestamp
  ])
}

async function syncAuthorizationInstanceNamesInClient(
  client: DatabaseClient,
  sourceAccountId: string,
  sourceName: string,
  timestamp: string
): Promise<string[]> {
  const rows = await client.query<{
    id: string
    system_account_id: string
    authorization_instance_authorization_id: string
    name: string
  }>(`
    SELECT id, system_account_id, authorization_instance_authorization_id, name
    FROM ${patchTable(client, 'accounts')}
    WHERE authorization_instance_source_account_id = ?
      AND deleted_at IS NULL
    ORDER BY created_at ASC, id ASC${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [sourceAccountId])
  const changedIds: string[] = []
  for (const row of rows) {
    if (!row.id || !row.system_account_id || !row.authorization_instance_authorization_id) continue
    const name = await uniqueAuthorizedAccountNameInClient(
      client,
      sourceName,
      row.system_account_id,
      row.authorization_instance_authorization_id,
      row.id
    )
    if (name === row.name) continue
    await client.execute(`
      UPDATE ${patchTable(client, 'accounts')}
      SET name = ?, updated_at = ?
      WHERE id = ?
    `, [name, timestamp, row.id])
    await replaceAccountNameSearchTermsAsync(client, row.id, row.system_account_id, name, timestamp)
    changedIds.push(row.id)
  }
  return changedIds
}

async function uniqueAuthorizedAccountNameInClient(
  client: DatabaseClient,
  sourceName: string,
  systemAccountId: string,
  authorizationId: string,
  exceptAccountId: string
): Promise<string> {
  const baseName = sourceName.trim() || '授权账户'
  const shortId = authorizationId.split('_').pop()?.slice(0, 6) || authorizationId.slice(-6)
  const candidates = [baseName, `${baseName}-${shortId}`]
  for (let index = 2; index <= 1000; index += 1) candidates.push(`${baseName}-${shortId}-${index}`)
  for (const candidate of candidates) {
    const existing = await client.one<{ id: string }>(`
      SELECT id
      FROM ${patchTable(client, 'accounts')}
      WHERE system_account_id = ?
        AND name = ?
        AND id <> ?
        AND deleted_at IS NULL
      LIMIT 1
    `, [systemAccountId, candidate, exceptAccountId])
    if (!existing) return candidate
  }
  return `${baseName}-${shortId}-${Date.now()}`
}

function nextRuntimeState(
  row: AccountPatchRow,
  input: {
    nextStatus: AccountStatus
    hasStatusInput: boolean
    connectionChanged: boolean
    expiredByPackage: boolean
    nowMs: number
  }
): {
  cooldownUntil: string | null
  lastErrorCode: string | null
  lastErrorMessage: string | null
  lastErrorTraceId: string | null
  cooldownRetestFailureCount: number
  cooldownRetestObservationStartedAt: string | null
  cooldownRetestGeneration: string | null
  cooldownRetestLastAt: string | null
  cooldownRetestLastStatusCode: number | null
} {
  let cooldownUntil = row.cooldown_until
  let lastErrorCode = row.last_error_code
  let lastErrorMessage = row.last_error_message
  let lastErrorTraceId = row.last_error_trace_id
  let cooldownRetestFailureCount = integerValue(row.cooldown_retest_failure_count)
  let cooldownRetestObservationStartedAt = row.cooldown_retest_observation_started_at
  let cooldownRetestGeneration = row.cooldown_retest_generation
  let cooldownRetestLastAt = row.cooldown_retest_last_at
  let cooldownRetestLastStatusCode = nullableInteger(row.cooldown_retest_last_status_code)
  let clearRetest = false
  if (input.hasStatusInput || input.connectionChanged) {
    if (input.nextStatus === 'active') {
      cooldownUntil = null
      lastErrorCode = null
      lastErrorMessage = null
      lastErrorTraceId = null
      cooldownRetestObservationStartedAt = null
      clearRetest = true
    } else if (input.nextStatus === 'pending_test') {
      cooldownUntil = null
      lastErrorCode = null
      lastErrorMessage = '账户配置已保存，等待后台检查'
      lastErrorTraceId = null
      cooldownRetestObservationStartedAt = null
      clearRetest = true
    } else if (input.nextStatus === 'disabled' || input.nextStatus === 'error') {
      cooldownUntil = null
      cooldownRetestObservationStartedAt = null
      if (input.nextStatus === 'disabled') {
        lastErrorCode = null
        lastErrorMessage = null
        lastErrorTraceId = null
        clearRetest = true
      }
    } else if (isCoolingAccountStatus(input.nextStatus)
      && (input.nextStatus !== row.status || !cooldownUntil)) {
      cooldownUntil = initialCooldownUntilForStatus(input.nextStatus, input.nowMs) ?? null
      cooldownRetestObservationStartedAt = cooldownRetestObservationStartedAtForStatus(input.nextStatus, input.nowMs) ?? null
      cooldownRetestGeneration = cooldownRetestObservationStartedAt
        ? newCooldownRetestGeneration()
        : null
      lastErrorCode = null
      lastErrorMessage = input.nextStatus === 'temporary_unavailable'
        ? '手动设置为临时不可调用'
        : '手动设置为限流中'
      clearRetest = input.nextStatus === 'temporary_unavailable'
    }
  }
  if (input.expiredByPackage) {
    cooldownUntil = null
    lastErrorCode = 'account_expired'
    lastErrorMessage = '账户套餐已过期，已自动停用'
    lastErrorTraceId = null
    cooldownRetestObservationStartedAt = null
    clearRetest = true
  }
  if (clearRetest) {
    cooldownRetestFailureCount = 0
    if (!cooldownRetestObservationStartedAt) cooldownRetestGeneration = null
    cooldownRetestLastAt = null
    cooldownRetestLastStatusCode = null
  }
  return {
    cooldownUntil,
    lastErrorCode,
    lastErrorMessage,
    lastErrorTraceId,
    cooldownRetestFailureCount,
    cooldownRetestObservationStartedAt,
    cooldownRetestGeneration,
    cooldownRetestLastAt,
    cooldownRetestLastStatusCode
  }
}

function applyRuntimeStateColumns(
  columns: Map<string, unknown>,
  row: AccountPatchRow,
  state: ReturnType<typeof nextRuntimeState>
): void {
  setMapIfChanged(columns, 'cooldown_until', row.cooldown_until, state.cooldownUntil)
  setMapIfChanged(columns, 'last_error_code', row.last_error_code, state.lastErrorCode)
  setMapIfChanged(columns, 'last_error_message', row.last_error_message, state.lastErrorMessage)
  setMapIfChanged(columns, 'last_error_trace_id', row.last_error_trace_id, state.lastErrorTraceId)
  setMapIfChanged(columns, 'cooldown_retest_failure_count', integerValue(row.cooldown_retest_failure_count), state.cooldownRetestFailureCount)
  setMapIfChanged(columns, 'cooldown_retest_observation_started_at', row.cooldown_retest_observation_started_at, state.cooldownRetestObservationStartedAt)
  setMapIfChanged(columns, 'cooldown_retest_generation', row.cooldown_retest_generation, state.cooldownRetestGeneration)
  setMapIfChanged(columns, 'cooldown_retest_last_at', row.cooldown_retest_last_at, state.cooldownRetestLastAt)
  setMapIfChanged(columns, 'cooldown_retest_last_status_code', nullableInteger(row.cooldown_retest_last_status_code), state.cooldownRetestLastStatusCode)
}

function setMapIfChanged(
  columns: Map<string, unknown>,
  column: string,
  current: unknown,
  next: unknown,
  value = next
): void {
  if (!isDeepStrictEqual(current, next)) columns.set(column, value)
}

function unchangedPatchResult(row: AccountPatchRow): AccountPatchTransactionResult {
  return {
    id: row.id,
    configRevision: integerValue(row.config_revision),
    changedFields: [],
    authorizationInstancesAffected: false,
    changes: [],
    name: row.name,
    ownerSystemAccountId: row.system_account_id,
    status: row.status,
    previousStatus: row.status,
    healthCheckRequired: false,
    runtimeRestoreRequired: false,
    balanceIdentityChanged: false,
    balanceAutoDisabledForMultipleApiKeys: false,
    renamedAuthorizationInstanceIds: [],
    groupStatsAffected: false,
    gatewayRuntimeAffected: false,
    accountLookupAffected: false
  }
}

async function applyAccountPatchPostCommitEffects(outcome: AccountPatchTransactionResult): Promise<void> {
  const effectTasks: Promise<unknown>[] = []
  if (outcome.groupStatsAffected) {
    effectTasks.push(refreshGroupAccountStatsAfterWriteAsync(
      outcome.previousGroupId || outcome.nextGroupId
        ? { groupIds: [outcome.previousGroupId, outcome.nextGroupId], reason: 'account_management_patch' }
        : { accountIds: [outcome.id], reason: 'account_management_patch' }
    ))
  }
  if (outcome.previousGroupId || outcome.nextGroupId) {
    runPostCommitSyncEffect(outcome.id, () => {
      if (outcome.previousGroupId) invalidateGroupAccountIdsCache(outcome.previousGroupId)
      if (outcome.nextGroupId && outcome.nextGroupId !== outcome.previousGroupId) {
        invalidateGroupAccountIdsCache(outcome.nextGroupId)
      }
    })
    effectTasks.push(clearNormalRouteLatencyDegradationForAccountBindingAsync({
      systemAccountId: outcome.ownerSystemAccountId,
      accountId: outcome.id,
      groupIds: [outcome.previousGroupId, outcome.nextGroupId]
    }))
  }
  runPostCommitSyncEffect(outcome.id, () => {
    if (outcome.accountLookupAffected) invalidateAccountLookupCache(outcome.id)
    for (const instanceId of outcome.renamedAuthorizationInstanceIds) invalidateAccountLookupCache(instanceId)
    if (outcome.gatewayRuntimeAffected) notifyGatewayRuntimeCacheInvalidation('account_management_patch')
  })
  if (!effectTasks.length) return
  const settled = await Promise.allSettled(effectTasks)
  for (const result of settled) {
    if (result.status === 'rejected') {
      logger.warn(errorLogFields(result.reason, {
        event: 'account_management_patch_post_commit_effect_failed',
        accountId: outcome.id
      }), 'AI 账户管理更新已提交，但后置缓存刷新失败')
    }
  }
}

function runPostCommitSyncEffect(accountId: string, effect: () => void): void {
  try {
    effect()
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_management_patch_post_commit_effect_failed',
      accountId
    }), 'AI 账户管理更新已提交，但后置缓存刷新失败')
  }
}

function validateCredentialPolicies(credentials: Record<string, unknown>): void {
  const errorPolicyMessage = accountErrorPolicyValidationMessage(validateAccountCredentialsErrorHandlingRules(credentials))
  if (errorPolicyMessage) throw new Error(errorPolicyMessage)
  const responseInspectionMessage = accountResponseInspectionPolicyValidationMessage(
    validateAccountCredentialsResponseInspectionRules(credentials)
  )
  if (responseInspectionMessage) throw new Error(responseInspectionMessage)
}

function assertStatusMutationAllowed(current: AccountStatus, requested: AccountStatus, hasInput: boolean): void {
  if (!hasInput) return
  if (requested === 'active' || requested === 'pending_test' || requested === 'disabled') return
  if (current === requested) return
  if (current === 'active' && requested === 'temporary_unavailable') return
  throw new Error('编辑状态只支持可调度、待检查或停用；正常账户可通过人工隔离进入临时不可调用')
}

function accountStatusForcesSchedulableOff(status: AccountStatus): boolean {
  return status === 'temporary_unavailable'
    || (isHardUnavailableAccountStatus(status) && status !== 'disabled')
}

function normalizedAccountName(value: unknown): string {
  const name = requiredTextInput(value, '账户名称')
  if ([...name].length > maxAccountNameLength) {
    throw new Error(`账户名称不能超过 ${maxAccountNameLength} 个字符`)
  }
  return name
}

function normalizedHealthCheckModel(value: unknown, supportedModels: readonly string[]): string {
  const model = typeof value === 'string' ? value.trim() : ''
  if (!model) throw new Error('账户检查模型不能为空')
  if (!supportedModels.includes(model)) throw new Error('账户检查模型必须属于账户支持模型')
  return model
}

function assertAccountEndpointModesCompatible(
  profile: ReturnType<typeof protocolProfileFromRow>,
  input: {
    modes: readonly AccountSupportedEndpointMode[]
    modelMappings?: readonly AccountModelMapping[]
    accountType?: string
    clientCompatibility: AccountClientCompatibility
  }
): void {
  if (isHybridProviderCode(profile.providerCode)) return
  if (isAnthropicProtocolProfile(profile)) {
    assertAnthropicEndpointModesCompatible({ modes: input.modes, accountType: input.accountType })
    return
  }
  if (isOpenAIProtocolProfile(profile)) {
    assertOpenAIEndpointModesCompatible({
      modes: input.modes,
      modelMappings: input.modelMappings,
      providerCode: profile.providerCode,
      providerProtocolProfileId: profile.providerProtocolProfileId,
      accountType: input.accountType,
      clientCompatibility: input.clientCompatibility
    })
    return
  }
  if (isGeminiProtocolProfile(profile)) {
    assertGeminiEndpointModesCompatible({ modes: input.modes, accountType: input.accountType })
  }
}

function protocolProfileFromRow(row: AccountPatchRow): {
  id: string
  providerCode: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
} {
  return {
    id: row.provider_protocol_profile_id,
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version
  }
}

function supportedEndpointModes(credentials: Record<string, unknown>): AccountSupportedEndpointMode[] {
  return Array.isArray(credentials.supported_endpoint_modes)
    ? credentials.supported_endpoint_modes.filter((mode): mode is AccountSupportedEndpointMode => typeof mode === 'string')
    : []
}

function unorderedStringListEquals(left: readonly string[] | undefined, right: readonly string[] | undefined): boolean {
  const normalizedLeft = [...(left ?? [])].sort()
  const normalizedRight = [...(right ?? [])].sort()
  return normalizedLeft.length === normalizedRight.length
    && normalizedLeft.every((value, index) => value === normalizedRight[index])
}

function accountModelMappingsEqual(
  left: readonly AccountModelMapping[] | undefined,
  right: readonly AccountModelMapping[] | undefined
): boolean {
  const key = (mapping: AccountModelMapping): string => [
    mapping.sourceEndpointFamily,
    mapping.sourceModel,
    mapping.upstreamEndpointFamily,
    mapping.upstreamModel,
    mapping.enabled === false ? '0' : '1'
  ].join('\u0000')
  const normalizedLeft = [...(left ?? [])].map(key).sort()
  const normalizedRight = [...(right ?? [])].map(key).sort()
  return normalizedLeft.length === normalizedRight.length
    && normalizedLeft.every((value, index) => value === normalizedRight[index])
}

function parseBalanceConfig(value: string | null | undefined): AccountSummary['balanceQueryConfig'] {
  if (!value) return undefined
  try {
    const parsed = JSON.parse(value)
    return parsed && typeof parsed === 'object' && Object.keys(parsed).length > 0
      ? normalizeAccountBalanceConfig(parsed)
      : undefined
  } catch {
    return undefined
  }
}

function databaseBoolean(value: unknown, fallback = false): boolean {
  if (value === true || value === 1 || value === '1') return true
  if (value === false || value === 0 || value === '0') return false
  return fallback
}

function integerValue(value: number | bigint | string | null | undefined): number {
  const parsed = Number(value ?? 0)
  return Number.isFinite(parsed) ? Math.trunc(parsed) : 0
}

function nullableInteger(value: number | bigint | string | null | undefined): number | null {
  return value === null || value === undefined ? null : integerValue(value)
}

function patchTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function accountPatchDatabaseClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

async function resolveEnabledProxyProfileIdInClient(
  client: DatabaseClient,
  proxyProfileId: string | undefined
): Promise<string | undefined> {
  if (!proxyProfileId) return undefined
  const row = await client.one<{ id: string; enabled: number | boolean | string }>(`
    SELECT id, enabled
    FROM ${patchTable(client, 'proxy_profiles')}
    WHERE id = ?
  `, [proxyProfileId])
  if (!row?.id || !databaseBoolean(row.enabled)) {
    const error = new Error('代理不存在或已停用，请选择一个已启用的代理')
    error.name = 'ProxyProfileUnavailableError'
    throw error
  }
  return row.id
}

function isDuplicateAccountNameError(error: unknown): boolean {
  const record = error as { code?: unknown; constraint?: unknown; message?: unknown }
  const message = typeof record?.message === 'string' ? record.message : ''
  return (record?.code === '23505'
    && String(record.constraint ?? '').includes('account'))
    || message.includes('idx_accounts_owner_name_unique')
    || message.includes('idx_accounts_owner_name_unique_lower')
    || message.includes('accounts.system_account_id, accounts.name')
    || message.includes('accounts.system_account_id, lower(name)')
    || message.includes('accounts_system_account_name')
}
