import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountClientCompatibility, AccountType, GroupType, ProviderCode } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { isOpenAIProtocolProfile } from '../domain/provider-protocol.js'
import { loadModelMappingsByAccountIds, loadModelMappingsForAccount } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIds, loadSupportedModelsForAccount } from './account-supported-models.repository.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, nowIso } from './database.js'
import {
  applyGatewayDispatchCandidateQualityRows,
  emptyGatewayDispatchCandidateDiagnostics,
  gatewayDispatchCandidateQualityFreshAfterIso,
  listGatewayDispatchCandidateRows,
  loadFreshGatewayDispatchCandidateQualityRows,
  orderGatewayDispatchCandidateRowsForDispatch
} from './gateway-dispatch-candidate-window.repository.js'
import { ProxyProfileUnavailableError, resolveProxyUrlForProfile, resolveProxyUrlsForProfiles, type ProxyProfileUrlResolution } from './proxy.repository.js'
import { accountApiKeyEntries, isAccountApiKeyPoolIsolationEnabled } from './account-api-key-rotation.js'
import { loadAccountApiKeyRuntimeStatesByAccountIds } from './account-api-key-runtime-state.repository.js'
import {
  gatewayDispatchAccountCandidateLimit,
  gatewayDispatchAccountCandidateScanLimit
} from './openai-account-selector.types.js'
import type {
  EligibleOpenAIGroupAccountSelection,
  GroupAccountRow,
  GroupUsageAccessMetadata,
  OpenAIAccountAccess,
  OpenAIAccountRow,
  OpenAIAccountSecret,
  OpenAIAccountsForGroupDiagnostics,
  OpenAIAccountsForGroupResult,
  OpenAIAccountSecretOptions,
  OpenAIGroupAccountSelectionRow
} from './openai-account-selector.types.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { activeResourceAuthorization, activeResourceAuthorizationById, activeResourceAuthorizationsByIds } from './resource-authorization-helpers.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'

export type {
  GroupUsageAccessMetadata,
  OpenAIAccountSecret,
  OpenAIAccountsForGroupDiagnostics,
  OpenAIAccountsForGroupResult
} from './openai-account-selector.types.js'

export function selectOpenAIAccountForGroup(groupId: string, systemAccountId: string): OpenAIAccountSecret | undefined {
  return listOpenAIAccountsForGroup(groupId, systemAccountId)[0]
}

export function findOpenAIAccountForGroup(
  groupId: string,
  accountId: string,
  systemAccountId: string,
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean } = {}
): OpenAIAccountSecret | undefined {
  const now = nowIso()
  const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return undefined
  }
  const forceAvailability = options.ignoreAvailability === true
  const groupAccount = getBusinessDatabase()
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled
      FROM group_accounts
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.account_id = ?
        AND group_accounts.enabled = 1
      LIMIT 1
    `)
    .get(groupId, groupAccess.groupOwnerSystemAccountId, accountId) as unknown as GroupAccountRow | undefined
  if (!groupAccount) {
    return undefined
  }

  const row = getBusinessDatabase()
    .prepare(`
      SELECT accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
        accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.availability_schedule_active, accounts.account_expires_at, accounts.last_successful_test_model, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.provider_code AS resource_provider_code,
        source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
        source_accounts.protocol_code AS resource_protocol_code,
        source_accounts.protocol_version AS resource_protocol_version,
        source_accounts.type AS resource_type,
        source_accounts.status AS resource_status,
        source_accounts.schedulable AS resource_schedulable,
        source_accounts.availability_schedule_active AS resource_availability_schedule_active,
        source_accounts.account_expires_at AS resource_account_expires_at,
        source_accounts.cooldown_until AS resource_cooldown_until,
        source_accounts.last_error_code AS resource_last_error_code,
        source_accounts.credentials_encrypted AS resource_credentials_encrypted,
        source_accounts.proxy_profile_id AS resource_proxy_profile_id,
        source_accounts.concurrency_limit AS resource_concurrency_limit,
        source_accounts.client_compatibility AS resource_client_compatibility,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM accounts
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE accounts.id = ?
        AND accounts.provider_protocol_profile_id = ?
        AND accounts.deleted_at IS NULL
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth'))
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND source_accounts.deleted_at IS NULL
            AND source_accounts.provider_protocol_profile_id = ?
            AND source_accounts.type IN ('api_key', 'oauth')
          )
        )
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    `)
    .get(accountId, groupAccess.providerProtocolProfileId, groupAccess.providerProtocolProfileId, now) as unknown as OpenAIAccountRow | undefined
  if (!row) {
    return undefined
  }
  const accountAccess = resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, groupAccount, {})
  if (!accountAccess) {
    return undefined
  }
  if (!forceAvailability && !isOpenAIAccountAvailableForSelection(row, groupAccount, accountAccess, now, options.includeUnavailable === true)) {
    return undefined
  }
  const resourceAccountId = openAIAccountResourceAccountId(row)
  return openAIAccountSecretFromRow(row, groupAccess, systemAccountId, groupAccount, {
    supportedModelsByAccountId: new Map([[resourceAccountId, loadSupportedModelsForAccount(resourceAccountId)]]),
    modelMappingsByAccountId: new Map([[resourceAccountId, loadModelMappingsForAccount(resourceAccountId)]]),
    apiKeyRuntimeStatesByAccountId: loadAccountApiKeyRuntimeStatesByAccountIds([resourceAccountId]),
    accountAccess
  })
}

export function resolveGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const groupRow = getBusinessDatabase()
    .prepare('SELECT system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, enabled, group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode; provider_protocol_profile_id?: string; protocol_code?: string; protocol_version?: string; enabled?: number; group_type?: GroupType | null; scheduling_policy_json?: string | null } | undefined
  const groupOwnerSystemAccountId = groupRow?.system_account_id
  if (!groupOwnerSystemAccountId) return undefined
  const providerCode = groupRow.provider_code
  if (!providerCode) return undefined
  const providerProtocolProfileId = groupRow.provider_protocol_profile_id?.trim()
  const protocolCode = groupRow.protocol_code?.trim()
  const protocolVersion = groupRow.protocol_version?.trim()
  if (!providerProtocolProfileId || !protocolCode || !protocolVersion) return undefined
  if (!isOpenAIProtocolProfile({ protocolCode, protocolVersion })) return undefined
  if (groupRow.enabled !== 1) return undefined
  const groupType = normalizeGroupType(groupRow?.group_type)
  const schedulingPolicy = parseGroupSchedulingPolicyJson(groupRow?.scheduling_policy_json ?? null, groupType)
  if (groupOwnerSystemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId, providerCode, providerProtocolProfileId, protocolCode, protocolVersion, groupAccessType: 'owner', groupType, schedulingPolicy }
  }
  const authorization = activeResourceAuthorization('group', groupId, systemAccountId)
  if (!authorization) return undefined
  const localSettings = getBusinessDatabase()
    .prepare('SELECT enabled, group_type, scheduling_policy_json FROM group_authorization_settings WHERE authorization_id = ? AND system_account_id = ? AND group_id = ? LIMIT 1')
    .get(authorization.id, systemAccountId, groupId) as unknown as { enabled?: number; group_type?: GroupType | null; scheduling_policy_json?: string | null } | undefined
  if (localSettings?.enabled === 0) return undefined
  const localGroupType = normalizeGroupType(localSettings?.group_type ?? groupRow.group_type)
  const localSchedulingPolicy = parseGroupSchedulingPolicyJson(localSettings?.scheduling_policy_json ?? groupRow.scheduling_policy_json ?? null, localGroupType)
  return {
    groupOwnerSystemAccountId,
    providerCode,
    providerProtocolProfileId,
    protocolCode,
    protocolVersion,
    groupAccessType: 'authorized',
    groupType: localGroupType,
    schedulingPolicy: localSchedulingPolicy,
    groupAuthorizationId: authorization.id,
    groupAuthorizationExpiresAt: authorization.expires_at ?? undefined,
    groupAuthorizationQuotaLimited: resourceAuthorizationQuotaLimited(authorization),
    groupAuthorizationSourceType: authorization.effective_source_type ?? undefined,
    groupAuthorizationSourceTeamId: authorization.effective_source_team_id ?? undefined
  }
}

export function listOpenAIAccountsForGroup(
  groupId: string,
  systemAccountId: string,
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata } = {}
): OpenAIAccountSecret[] {
  return listOpenAIAccountsForGroupResult(groupId, systemAccountId, options).accounts
}

export function runtimeOpenAIAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  copyRuntimeCredentialText(credentials, output, 'account_id')
  copyRuntimeCredentialText(credentials, output, 'api_key_strategy')
  copyRuntimeCredentialValue(credentials, output, 'api_key_weights')
  copyRuntimeCredentialValue(credentials, output, 'error_handling_rules')
  copyRuntimeCredentialValue(credentials, output, 'response_inspection_rules')
  return output
}

export function listOpenAIAccountsForGroupResult(
  groupId: string,
  systemAccountId: string,
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata } = {}
): OpenAIAccountsForGroupResult {
  const database = getBusinessDatabase()
  const now = nowIso()
  const qualityFreshAfter = gatewayDispatchCandidateQualityFreshAfterIso()
  const groupAccess = options.preResolvedGroupAccess ?? resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return {
      accounts: [],
      diagnostics: emptyGatewayDispatchCandidateDiagnostics()
    }
  }
  const groupAccountRows = listGatewayDispatchCandidateRows(database, groupId, groupAccess, now)
  const accountAuthorizationsByIdOrResourceId = loadAccountAuthorizationsForSelection(groupAccountRows, groupAccess, systemAccountId)
  const eligibleRows = groupAccountRows
    .map((row) => ({
      row,
      accountAccess: resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId })
    }))
    .filter((item): item is EligibleOpenAIGroupAccountSelection => Boolean(item.accountAccess))
    .filter((item) => isOpenAIAccountAvailableForSelection(item.row, item.row, item.accountAccess, now, false))
  const qualityByAccountId = loadFreshGatewayDispatchCandidateQualityRows(eligibleRows.map((item) => item.row.account_id), qualityFreshAfter)
  applyGatewayDispatchCandidateQualityRows(eligibleRows, qualityByAccountId)

  const accounts: OpenAIAccountSecret[] = []
  const orderedEligibleRows = orderGatewayDispatchCandidateRowsForDispatch(eligibleRows)
  let hydrationBatchCount = 0
  let hydrationDroppedCount = 0
  for (let offset = 0; offset < orderedEligibleRows.length && accounts.length < gatewayDispatchAccountCandidateLimit; offset += gatewayDispatchAccountCandidateLimit) {
    const hydrationRows = orderedEligibleRows.slice(offset, offset + gatewayDispatchAccountCandidateLimit)
    hydrationBatchCount += 1
    const resourceAccountIds = hydrationRows.map((item) => openAIAccountResourceAccountId(item.row))
    const supportedModelsByAccountId = loadSupportedModelsByAccountIds(resourceAccountIds)
    const modelMappingsByAccountId = loadModelMappingsByAccountIds(resourceAccountIds)
    const apiKeyRuntimeStatesByAccountId = loadAccountApiKeyRuntimeStatesByAccountIds(resourceAccountIds)
    const proxyProfilesById = loadProxyProfilesForSelection(hydrationRows.map((item) => item.row))
    for (const { row, accountAccess } of hydrationRows) {
      const account = openAIAccountSecretFromRow(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId, proxyProfilesById, supportedModelsByAccountId, modelMappingsByAccountId, apiKeyRuntimeStatesByAccountId, accountAccess })
      if (account) {
        accounts.push(account)
        if (accounts.length >= gatewayDispatchAccountCandidateLimit) {
          break
        }
      } else {
        hydrationDroppedCount += 1
      }
    }
  }

  return {
    accounts,
    diagnostics: {
      scanLimit: gatewayDispatchAccountCandidateScanLimit,
      finalLimit: gatewayDispatchAccountCandidateLimit,
      candidateRowCount: groupAccountRows.length,
      scannedRowCount: groupAccountRows.length,
      eligibleRowCount: eligibleRows.length,
      hydrationBatchCount,
      hydratedAccountCount: accounts.length,
      hydrationDroppedCount,
      finalAccountCount: accounts.length,
      scanLimitReached: groupAccountRows.length >= gatewayDispatchAccountCandidateScanLimit
    }
  }
}

function openAIAccountResourceAccountId(row: OpenAIAccountRow): string {
  return row.resource_account_id ?? row.id
}

function openAIAccountResourceProviderCode(row: OpenAIAccountRow): ProviderCode {
  return row.resource_provider_code ?? row.provider_code
}

function openAIAccountResourceProviderProtocolProfileId(row: OpenAIAccountRow): string {
  return row.resource_provider_protocol_profile_id ?? row.provider_protocol_profile_id
}

function openAIAccountResourceProtocolCode(row: OpenAIAccountRow): string {
  return row.resource_protocol_code ?? row.protocol_code
}

function openAIAccountResourceProtocolVersion(row: OpenAIAccountRow): string {
  return row.resource_protocol_version ?? row.protocol_version
}

function openAIAccountResourceType(row: OpenAIAccountRow): AccountType {
  return row.resource_type ?? row.type
}

function openAIAccountResourceCredentialsEncrypted(row: OpenAIAccountRow): string {
  return row.resource_credentials_encrypted ?? row.credentials_encrypted
}

function openAIAccountResourceProxyProfileId(row: OpenAIAccountRow): string | null {
  return row.resource_proxy_profile_id ?? row.proxy_profile_id
}

function openAIAccountResourceConcurrencyLimit(row: OpenAIAccountRow): number {
  return Number(row.resource_concurrency_limit ?? row.concurrency_limit ?? 1)
}

function openAIAccountResourceClientCompatibility(row: OpenAIAccountRow): AccountClientCompatibility {
  return normalizeOpenAIAccountClientCompatibility(
    openAIAccountResourceProviderCode(row),
    openAIAccountResourceType(row),
    row.resource_client_compatibility ?? row.client_compatibility,
    'openai_standard',
    { protocolCode: openAIAccountResourceProtocolCode(row), protocolVersion: openAIAccountResourceProtocolVersion(row) }
  )
}

function openAIAccountSecretFromRow(
  row: OpenAIAccountRow,
  groupAccess: GroupUsageAccessMetadata,
  systemAccountId: string,
  groupAccount?: GroupAccountRow,
  options: OpenAIAccountSecretOptions = {}
): OpenAIAccountSecret | undefined {
  const accountAccess = options.accountAccess ?? resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, groupAccount, options)
  if (!accountAccess) {
    return undefined
  }
  const resourceType = openAIAccountResourceType(row)
  let credentials: Record<string, unknown>
  try {
    credentials = decryptJson<Record<string, unknown>>(openAIAccountResourceCredentialsEncrypted(row))
  } catch {
    return undefined
  }
  const apiKeyEntries = accountApiKeyEntries(credentials)
  const apiKey = resourceType === 'oauth'
    ? typeof credentials.access_token === 'string' ? credentials.access_token : ''
    : apiKeyEntries[0]?.key ?? ''
  if (!apiKey) {
    return undefined
  }
  const apiKeys = resourceType === 'api_key'
    ? apiKeyEntries.map((entry) => entry.key)
    : undefined
  const resourceAccountId = openAIAccountResourceAccountId(row)
  const apiKeyPoolEnabled = isAccountApiKeyPoolIsolationEnabled({
    providerCode: openAIAccountResourceProviderCode(row),
    type: resourceType,
    credentials
  })
  const resourceProxyProfileId = openAIAccountResourceProxyProfileId(row)
  const proxyProfile = resolveOpenAIAccountProxyUrl(resourceProxyProfileId, options.proxyProfilesById)
  const isAccountAuthorized = accountAccess.accountAccessType === 'account_authorized'
  const isLocalAccountAuthorized = isAccountAuthorized && Boolean(groupAccount?.account_authorization_id)
  const bindingSystemAccountId = isLocalAccountAuthorized ? groupAccount?.binding_system_account_id?.trim() : undefined
  if (isLocalAccountAuthorized && !bindingSystemAccountId) {
    throw new Error('授权账户绑定缺少系统账户上下文')
  }
  const runtimeStatus = row.status
  const dispatchPriority = Number(groupAccount?.local_priority ?? row.priority ?? 0)
  const dispatchSuperPriorityEnabled = groupAccount?.local_super_priority_enabled === 1
  const dispatchFallbackEnabled = groupAccount?.local_fallback_enabled === 1
  const accountOwnerSystemAccountId = isAccountAuthorized
    ? accountAccess.accountOwnerSystemAccountId ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
    : row.system_account_id
  return {
    id: row.id,
    providerCode: openAIAccountResourceProviderCode(row),
    providerProtocolProfileId: openAIAccountResourceProviderProtocolProfileId(row),
    protocolCode: openAIAccountResourceProtocolCode(row),
    protocolVersion: openAIAccountResourceProtocolVersion(row),
    systemAccountId: row.system_account_id,
    accountOwnerSystemAccountId,
    groupOwnerSystemAccountId: groupAccess.groupOwnerSystemAccountId,
    accountAccessType: accountAccess.accountAccessType,
    groupAccessType: groupAccess.groupAccessType,
    accountAuthorizationId: accountAccess.accountAuthorizationId,
    accountAuthorizationExpiresAt: accountAccess.accountAuthorizationExpiresAt,
    accountAuthorizationQuotaLimited: accountAccess.accountAuthorizationQuotaLimited,
    accountAuthorizationSourceType: accountAccess.accountAuthorizationSourceType,
    accountAuthorizationSourceTeamId: accountAccess.accountAuthorizationSourceTeamId,
    bindingSystemAccountId,
    boundGroupId: isLocalAccountAuthorized ? groupAccount?.group_id ?? undefined : undefined,
    groupAuthorizationId: groupAccess.groupAuthorizationId,
    groupAuthorizationExpiresAt: groupAccess.groupAuthorizationExpiresAt,
    groupAuthorizationQuotaLimited: groupAccess.groupAuthorizationQuotaLimited,
    groupAuthorizationSourceType: groupAccess.groupAuthorizationSourceType,
    groupAuthorizationSourceTeamId: groupAccess.groupAuthorizationSourceTeamId,
    name: row.name,
    type: resourceType,
    status: runtimeStatus,
    concurrencyLimit: openAIAccountResourceConcurrencyLimit(row),
    priority: dispatchPriority,
    superPriorityEnabled: runtimeStatus === 'active' && dispatchSuperPriorityEnabled,
    fallbackEnabled: runtimeStatus === 'active' && dispatchFallbackEnabled,
    clientCompatibility: openAIAccountResourceClientCompatibility(row),
    supportedModels: [...(options.supportedModelsByAccountId?.get(resourceAccountId) ?? [])],
    modelMappings: [...(options.modelMappingsByAccountId?.get(resourceAccountId) ?? [])],
    lastSuccessfulTestModel: row.last_successful_test_model?.trim() || undefined,
    qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
    qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
    qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
    baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : 'https://api.openai.com/v1',
    apiKey,
    apiKeys,
    apiKeyRuntimeStates: apiKeyPoolEnabled
      ? [...(options.apiKeyRuntimeStatesByAccountId?.get(resourceAccountId) ?? [])]
      : undefined,
    refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : undefined,
    clientId: typeof credentials.client_id === 'string' ? credentials.client_id : undefined,
    credentialSourceAccountId: resourceAccountId !== row.id ? resourceAccountId : undefined,
    proxyProfileId: resourceProxyProfileId ?? undefined,
    proxyUrl: proxyProfile.proxyUrl,
    proxyProfileUnavailable: proxyProfile.unavailable,
    proxyProfileErrorMessage: proxyProfile.errorMessage,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
    streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
    accountExpiresAt: row.account_expires_at ?? undefined,
    expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : undefined,
    credentials: runtimeOpenAIAccountCredentials(credentials)
  }
}

function copyRuntimeCredentialText(input: Record<string, unknown>, output: Record<string, unknown>, key: string): void {
  const value = input[key]
  if (typeof value === 'string' && value.trim()) {
    output[key] = value.trim()
  }
}

function copyRuntimeCredentialValue(input: Record<string, unknown>, output: Record<string, unknown>, key: string): void {
  if (Object.prototype.hasOwnProperty.call(input, key)) {
    output[key] = input[key]
  }
}

function isOpenAIPhysicalAccountAvailableForSelection(row: OpenAIAccountRow, now: string, includeUnavailable: boolean): boolean {
  if (row.account_expires_at && row.account_expires_at <= now) {
    return false
  }
  if (row.schedulable !== 1) {
    return false
  }
  if (row.availability_schedule_active !== 1) {
    return false
  }
  if (includeUnavailable) {
    return row.status === 'active' || row.status === 'rate_limited' || row.status === 'temporary_unavailable'
  }
  return row.status === 'active' && (!row.cooldown_until || row.cooldown_until <= now)
}

function isOpenAIResourceAccountAvailableForSelection(row: OpenAIAccountRow, now: string, includeUnavailable: boolean): boolean {
  if (!row.authorization_instance_authorization_id) {
    return true
  }
  if (!row.resource_account_id || !row.resource_status) {
    return false
  }
  if (row.resource_account_expires_at && row.resource_account_expires_at <= now) {
    return false
  }
  if (row.resource_last_error_code === 'account_expired') {
    return false
  }
  if (row.resource_schedulable !== 1) {
    return false
  }
  if (row.resource_availability_schedule_active !== 1) {
    return false
  }
  if (includeUnavailable) {
    return row.resource_status === 'active' || row.resource_status === 'rate_limited' || row.resource_status === 'temporary_unavailable'
  }
  return row.resource_status === 'active' && (!row.resource_cooldown_until || row.resource_cooldown_until <= now)
}

function isOpenAIAccountAvailableForSelection(
  row: OpenAIAccountRow,
  groupAccount: GroupAccountRow | undefined,
  accountAccess: OpenAIAccountAccess,
  now: string,
  includeUnavailable: boolean
): boolean {
  if (!isOpenAIPhysicalAccountAvailableForSelection(row, now, includeUnavailable)) {
    return false
  }
  if (!isOpenAIResourceAccountAvailableForSelection(row, now, includeUnavailable)) {
    return false
  }
  if (accountAccess.accountAccessType === 'account_authorized') {
    if (!groupAccount?.group_id || !groupAccount.account_authorization_id || groupAccount.account_authorization_id !== accountAccess.accountAuthorizationId) {
      return false
    }
  }
  return true
}

function resolveSchedulableOpenAIAccountAccess(
  row: OpenAIAccountRow,
  groupAccess: GroupUsageAccessMetadata,
  systemAccountId: string,
  groupAccount: GroupAccountRow | undefined,
  options: OpenAIAccountSecretOptions
): OpenAIAccountAccess | undefined {
  const accountAccess = resolveOpenAIAccountAccess(row, systemAccountId, groupAccess, groupAccount?.account_authorization_id ?? undefined, options.accountAuthorizationsByIdOrResourceId)
  if (!accountAccess) {
    return undefined
  }
  if (options.enforceSchedulableAuthorization !== false && !canScheduleAuthorizedAccount({
    accountId: row.id,
    accountAccessType: accountAccess.accountAccessType,
    authorizationId: accountAccess.accountAuthorizationId,
    systemAccountId,
    accountAuthorizationsByIdOrResourceId: options.accountAuthorizationsByIdOrResourceId
  })) {
    return undefined
  }
  if (accountAccess.accountAccessType === 'account_authorized' && !row.resource_account_id) {
    return undefined
  }
  return accountAccess
}

function loadAccountAuthorizationsForSelection(
  rows: OpenAIGroupAccountSelectionRow[],
  groupAccess: GroupUsageAccessMetadata,
  systemAccountId: string
): Map<string, ResourceAuthorizationRow> | undefined {
  if (groupAccess.groupAccessType === 'authorized') return undefined
  const result = new Map<string, ResourceAuthorizationRow>()
  const authorizationIds = rows
    .map((row) => row.authorization_instance_authorization_id ?? '')
    .filter(Boolean)
  for (const authorization of activeResourceAuthorizationsByIds(authorizationIds, systemAccountId).values()) {
    result.set(authorization.id, authorization)
    result.set(authorization.resource_id, authorization)
  }
  return result.size ? result : undefined
}

function loadProxyProfilesForSelection(rows: OpenAIGroupAccountSelectionRow[]): Map<string, ProxyProfileUrlResolution> | undefined {
  const proxyProfileIds = rows
    .map((row) => openAIAccountResourceProxyProfileId(row) ?? '')
    .filter(Boolean)
  if (!proxyProfileIds.length) return undefined
  return resolveProxyUrlsForProfiles(proxyProfileIds)
}

function resolveOpenAIAccountProxyUrl(proxyProfileId: string | null, proxyProfilesById?: Map<string, ProxyProfileUrlResolution>): ProxyProfileUrlResolution {
  if (proxyProfileId && proxyProfilesById) {
    return proxyProfilesById.get(proxyProfileId) ?? { unavailable: true, errorMessage: new ProxyProfileUnavailableError(proxyProfileId).message }
  }
  try {
    return { proxyUrl: resolveProxyUrlForProfile(proxyProfileId) }
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      return { unavailable: true, errorMessage: error.message }
    }
    return { unavailable: true, errorMessage: '代理凭据不可解密，请检查代理配置' }
  }
}

function resolveOpenAIAccountAccess(
  row: OpenAIAccountRow,
  callerSystemAccountId: string,
  groupAccess: GroupUsageAccessMetadata,
  boundAccountAuthorizationId?: string,
  accountAuthorizationsByIdOrResourceId?: Map<string, ResourceAuthorizationRow>
): OpenAIAccountAccess | undefined {
  const accountId = row.id
  const accountOwnerSystemAccountId = row.system_account_id
  if (row.authorization_instance_authorization_id) {
    if (accountOwnerSystemAccountId !== callerSystemAccountId) {
      return undefined
    }
    const authorization = accountAuthorizationsByIdOrResourceId
      ? accountAuthorizationsByIdOrResourceId.get(row.authorization_instance_authorization_id)
      : activeResourceAuthorizationById(row.authorization_instance_authorization_id, callerSystemAccountId)
    if (!authorization || (boundAccountAuthorizationId && authorization.id !== boundAccountAuthorizationId)) {
      return undefined
    }
    return {
      accountAccessType: 'account_authorized',
      accountOwnerSystemAccountId: authorization.resource_owner_system_account_id,
      accountAuthorizationId: authorization.id,
      accountAuthorizationExpiresAt: authorization.expires_at ?? undefined,
      accountAuthorizationQuotaLimited: resourceAuthorizationQuotaLimited(authorization),
      accountAuthorizationSourceType: authorization.effective_source_type ?? undefined,
      accountAuthorizationSourceTeamId: authorization.effective_source_team_id ?? undefined
    }
  }
  if (accountOwnerSystemAccountId === callerSystemAccountId) {
    return { accountAccessType: 'owner' }
  }
  if (groupAccess.groupAccessType === 'authorized') {
    return accountOwnerSystemAccountId === groupAccess.groupOwnerSystemAccountId
      ? { accountAccessType: 'group_authorized' }
      : undefined
  }
  const authorization = accountAuthorizationsByIdOrResourceId
    ? accountAuthorizationsByIdOrResourceId.get(boundAccountAuthorizationId ?? accountId)
    : activeResourceAuthorization('account', accountId, callerSystemAccountId)
  if (boundAccountAuthorizationId && authorization?.id !== boundAccountAuthorizationId) {
    return undefined
  }
  return authorization
    ? {
        accountAccessType: 'account_authorized',
        accountOwnerSystemAccountId: authorization.resource_owner_system_account_id,
        accountAuthorizationId: authorization.id,
        accountAuthorizationExpiresAt: authorization.expires_at ?? undefined,
        accountAuthorizationQuotaLimited: resourceAuthorizationQuotaLimited(authorization),
        accountAuthorizationSourceType: authorization.effective_source_type ?? undefined,
        accountAuthorizationSourceTeamId: authorization.effective_source_team_id ?? undefined
      }
    : undefined
}

function resourceAuthorizationQuotaLimited(authorization: ResourceAuthorizationRow): boolean {
  return hasEnabledRequestQuotaLimit(parseRequestQuotaLimitsJson(authorization.limits_json))
}

function canScheduleAuthorizedAccount(input: {
  accountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  authorizationId?: string
  systemAccountId: string
  accountAuthorizationsByIdOrResourceId?: Map<string, ResourceAuthorizationRow>
}): boolean {
  if (input.accountAccessType === 'owner' || input.accountAccessType === 'group_authorized') {
    return true
  }
  if (!input.authorizationId) {
    return false
  }
  const authorization = input.accountAuthorizationsByIdOrResourceId
    ? input.accountAuthorizationsByIdOrResourceId.get(input.authorizationId) ?? input.accountAuthorizationsByIdOrResourceId.get(input.accountId)
    : activeResourceAuthorizationById(input.authorizationId, input.systemAccountId)
  return authorization?.id === input.authorizationId
}
