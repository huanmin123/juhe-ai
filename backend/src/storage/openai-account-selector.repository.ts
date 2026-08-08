import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountClientCompatibility, AccountType, GatewayRequestEndpointFamily, GroupType, ProviderCode, ResourceAuthorizationResourceType } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { normalizeOpenAIEndpointModesForRuntime } from '../domain/openai-endpoint-modes.js'
import { normalizeAnthropicEndpointModesForRuntime } from '../domain/anthropic-endpoint-modes.js'
import { normalizeGeminiEndpointModesForRuntime } from '../domain/gemini-endpoint-modes.js'
import { isAnthropicProtocolProfile, isGeminiProtocolProfile, isHybridProviderCode } from '../domain/provider-protocol.js'
import { normalizeHybridEndpointModesForRuntime } from '../modules/providers/drivers/hybrid/account-credentials.js'
import { runtimeConfig } from '../config/runtime.js'
import { loadModelMappingsByAccountIds, loadModelMappingsByAccountIdsAsync, loadModelMappingsForAccount } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIds, loadSupportedModelsByAccountIdsAsync, loadSupportedModelsForAccount } from './account-supported-models.repository.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import {
  applyGatewayDispatchCandidateQualityRows,
  emptyGatewayDispatchCandidateDiagnostics,
  gatewayDispatchCandidateQualityFreshAfterIso,
  listGatewayDispatchCandidateRows,
  listGatewayDispatchCandidateRowsAsync,
  listGatewayDispatchModelCandidateRows,
  listGatewayDispatchModelCandidateRowsAsync,
  loadFreshGatewayDispatchCandidateQualityRows,
  loadFreshGatewayDispatchCandidateQualityRowsAsync,
  orderGatewayDispatchCandidateRowsForDispatch
} from './gateway-dispatch-candidate-window.repository.js'
import { ProxyProfileUnavailableError, resolveProxyUrlForProfile, resolveProxyUrlsForProfiles, resolveProxyUrlsForProfilesAsync, type ProxyProfileUrlResolution } from './proxy.repository.js'
import { accountApiKeyEntries, isAccountApiKeyPoolIsolationEnabled } from './account-api-key-rotation.js'
import { loadAccountApiKeyRuntimeStatesByAccountIds, loadAccountApiKeyRuntimeStatesByAccountIdsAsync } from './account-api-key-runtime-state.repository.js'
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
import { activeResourceAuthorization, activeResourceAuthorizationById, activeResourceAuthorizationsByIds, resourceAuthorizationSelectColumns } from './resource-authorization-helpers.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues } from './query-utils.js'

const businessSchemaName = 'juhe_business'

export type {
  DispatchAccountSecret,
  DispatchAccountsForGroupResult,
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
        accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.provider_code AS resource_provider_code,
        source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
        source_accounts.protocol_code AS resource_protocol_code,
        source_accounts.protocol_version AS resource_protocol_version,
        source_accounts.type AS resource_type,
        source_accounts.status AS resource_status,
        source_accounts.schedulable AS resource_schedulable,
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
        AND accounts.provider_code = ?
        AND accounts.deleted_at IS NULL
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND source_accounts.deleted_at IS NULL
            AND source_accounts.provider_code = ?
            AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
          )
        )
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    `)
    .get(accountId, groupAccess.providerCode, groupAccess.providerCode, now) as unknown as OpenAIAccountRow | undefined
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

export async function findOpenAIAccountForGroupAsync(
  groupId: string,
  accountId: string,
  systemAccountId: string,
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean } = {}
): Promise<OpenAIAccountSecret | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findOpenAIAccountForGroup(groupId, accountId, systemAccountId, options)
  }
  const now = nowIso()
  const client = await getOpenAIAccountSelectorDatabaseClient()
  const groupAccess = await resolveGroupUsageAccessMetadataAsync(groupId, systemAccountId)
  if (!groupAccess) {
    return undefined
  }
  const forceAvailability = options.ignoreAvailability === true
  const groupAccount = await client.one<GroupAccountRow>(`
    SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
      group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled
    FROM ${selectorTable(client, 'group_accounts')} group_accounts
    WHERE group_accounts.group_id = ?
      AND group_accounts.system_account_id = ?
      AND group_accounts.account_id = ?
      AND group_accounts.enabled = 1
    LIMIT 1
  `, [groupId, groupAccess.groupOwnerSystemAccountId, accountId])
  if (!groupAccount) {
    return undefined
  }

  const row = await client.one<OpenAIAccountRow>(`
    SELECT accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
      accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
      accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
      source_accounts.id AS resource_account_id,
      source_accounts.provider_code AS resource_provider_code,
      source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
      source_accounts.protocol_code AS resource_protocol_code,
      source_accounts.protocol_version AS resource_protocol_version,
      source_accounts.type AS resource_type,
      source_accounts.status AS resource_status,
      source_accounts.schedulable AS resource_schedulable,
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
    FROM ${selectorTable(client, 'accounts')} accounts
    LEFT JOIN ${selectorTable(client, 'accounts')} source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
    WHERE accounts.id = ?
      AND accounts.provider_code = ?
      AND accounts.deleted_at IS NULL
      AND (
        (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
        OR (
          accounts.authorization_instance_authorization_id IS NOT NULL
          AND source_accounts.deleted_at IS NULL
          AND source_accounts.provider_code = ?
          AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
        )
      )
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    LIMIT 1
  `, [accountId, groupAccess.providerCode, groupAccess.providerCode, now])
  if (!row) {
    return undefined
  }

  const selectionRow = { ...row, ...groupAccount } as OpenAIGroupAccountSelectionRow
  const accountAuthorizationsByIdOrResourceId = await loadAccountAuthorizationsForSelectionAsync(client, [selectionRow], groupAccess, systemAccountId) ?? new Map()
  const accountAccess = resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, groupAccount, { accountAuthorizationsByIdOrResourceId })
  if (!accountAccess) {
    return undefined
  }
  if (!forceAvailability && !isOpenAIAccountAvailableForSelection(row, groupAccount, accountAccess, now, options.includeUnavailable === true)) {
    return undefined
  }
  const resourceAccountId = openAIAccountResourceAccountId(row)
  return openAIAccountSecretFromRow(row, groupAccess, systemAccountId, groupAccount, {
    accountAuthorizationsByIdOrResourceId,
    proxyProfilesById: await loadProxyProfilesForSelectionAsync([selectionRow]),
    supportedModelsByAccountId: await loadSupportedModelsByAccountIdsAsync([resourceAccountId]),
    modelMappingsByAccountId: await loadModelMappingsByAccountIdsAsync([resourceAccountId]),
    apiKeyRuntimeStatesByAccountId: await loadAccountApiKeyRuntimeStatesByAccountIdsAsync([resourceAccountId]),
    accountAccess
  })
}

export function resolveGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const groupRow = getBusinessDatabase()
    .prepare('SELECT system_account_id, provider_code, enabled, group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode; enabled?: number; group_type?: GroupType | null; scheduling_policy_json?: string | null } | undefined
  const groupOwnerSystemAccountId = groupRow?.system_account_id
  if (!groupOwnerSystemAccountId) return undefined
  const providerCode = groupRow.provider_code
  if (!providerCode) return undefined
  if (groupRow.enabled !== 1) return undefined
  const groupType = normalizeGroupType(groupRow?.group_type)
  const schedulingPolicy = parseGroupSchedulingPolicyJson(groupRow?.scheduling_policy_json ?? null, groupType)
  if (groupOwnerSystemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId, providerCode, groupAccessType: 'owner', groupType, schedulingPolicy }
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

export async function resolveGroupUsageAccessMetadataAsync(groupId: string, systemAccountId: string): Promise<GroupUsageAccessMetadata | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  }
  const client = await getOpenAIAccountSelectorDatabaseClient()
  const groupRow = await client.one<{
    system_account_id?: string
    provider_code?: ProviderCode
    enabled?: number
    group_type?: GroupType | null
    scheduling_policy_json?: string | null
  }>(`
    SELECT system_account_id, provider_code, enabled, group_type, scheduling_policy_json
    FROM ${selectorTable(client, 'groups')}
    WHERE id = ?
  `, [groupId])
  const groupOwnerSystemAccountId = groupRow?.system_account_id
  if (!groupOwnerSystemAccountId) return undefined
  const providerCode = groupRow.provider_code
  if (!providerCode) return undefined
  if (groupRow.enabled !== 1) return undefined
  const groupType = normalizeGroupType(groupRow?.group_type)
  const schedulingPolicy = parseGroupSchedulingPolicyJson(groupRow?.scheduling_policy_json ?? null, groupType)
  if (groupOwnerSystemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId, providerCode, groupAccessType: 'owner', groupType, schedulingPolicy }
  }
  const authorization = await activeResourceAuthorizationForSelectorAsync(client, 'group', groupId, systemAccountId)
  if (!authorization) return undefined
  const localSettings = await client.one<{ enabled?: number; group_type?: GroupType | null; scheduling_policy_json?: string | null }>(`
    SELECT enabled, group_type, scheduling_policy_json
    FROM ${selectorTable(client, 'group_authorization_settings')}
    WHERE authorization_id = ? AND system_account_id = ? AND group_id = ?
    LIMIT 1
  `, [authorization.id, systemAccountId, groupId])
  if (localSettings?.enabled === 0) return undefined
  const localGroupType = normalizeGroupType(localSettings?.group_type ?? groupRow.group_type)
  const localSchedulingPolicy = parseGroupSchedulingPolicyJson(localSettings?.scheduling_policy_json ?? groupRow.scheduling_policy_json ?? null, localGroupType)
  return {
    groupOwnerSystemAccountId,
    providerCode,
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
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata; requestedModel?: string; requestedEndpointFamily?: GatewayRequestEndpointFamily; includeUnavailable?: boolean } = {}
): OpenAIAccountSecret[] {
  return listOpenAIAccountsForGroupResult(groupId, systemAccountId, options).accounts
}

export function listRecoverableUnavailableOpenAIAccountsForGroup(
  groupId: string,
  systemAccountId: string,
  options: {
    requestedModel?: string
    requestedEndpointFamily?: GatewayRequestEndpointFamily
    windowMs?: number
  } = {}
): OpenAIAccountSecret[] {
  const nowMs = Date.now()
  const windowMs = normalizeRecoverableUnavailableWindowMs(options.windowMs)
  const latestRecoverableAtMs = nowMs + windowMs
  return listOpenAIAccountsForGroupResult(groupId, systemAccountId, {
    requestedModel: options.requestedModel,
    requestedEndpointFamily: options.requestedEndpointFamily,
    includeUnavailable: true
  }).accounts.filter((account) => {
    const cooldownUntilMs = accountRecoverableCooldownUntilMs(account)
    if (cooldownUntilMs === undefined || cooldownUntilMs > latestRecoverableAtMs) {
      return false
    }
    if (account.status === 'active') {
      return cooldownUntilMs > nowMs
    }
    return account.status === 'temporary_unavailable' || account.status === 'rate_limited'
  })
}

export function runtimeOpenAIAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const key of [
    'access_token',
    'refresh_token',
    'expires_at',
    'client_id',
    'client_secret',
    'quota_project_id',
    'oauth_type',
    'project_id',
    'tier_id',
    'token_type',
    'scope'
  ]) {
    copyRuntimeCredentialText(credentials, output, key)
  }
  copyRuntimeCredentialText(credentials, output, 'account_id')
  copyRuntimeCredentialText(credentials, output, 'api_key_strategy')
  copyRuntimeCredentialText(credentials, output, 'service_tier_override')
  copyRuntimeCredentialText(credentials, output, 'reasoning_effort_override')
  copyRuntimeCredentialValue(credentials, output, 'supported_endpoint_modes')
  copyRuntimeCredentialValue(credentials, output, 'api_key_weights')
  copyRuntimeCredentialValue(credentials, output, 'error_handling_rules')
  copyRuntimeCredentialValue(credentials, output, 'response_inspection_rules')
  return output
}

export function listOpenAIAccountsForGroupResult(
  groupId: string,
  systemAccountId: string,
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata; requestedModel?: string; requestedEndpointFamily?: GatewayRequestEndpointFamily; includeUnavailable?: boolean } = {}
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
  const candidateWindowOptions = { includeUnavailable: options.includeUnavailable === true }
  const modelCandidateRows = options.requestedModel?.trim()
    ? listGatewayDispatchModelCandidateRows(database, groupId, groupAccess, now, options.requestedModel, options.requestedEndpointFamily, candidateWindowOptions)
    : undefined
  const groupAccountRows = modelCandidateRows
    ? mergeOpenAIGroupAccountRowsForDispatch(
      modelCandidateRows.rows,
      listGatewayDispatchCandidateRows(database, groupId, groupAccess, now, candidateWindowOptions)
    )
    : listGatewayDispatchCandidateRows(database, groupId, groupAccess, now, candidateWindowOptions)
  const accountAuthorizationsByIdOrResourceId = loadAccountAuthorizationsForSelection(groupAccountRows, groupAccess, systemAccountId)
  const eligibleRows = groupAccountRows
    .map((row) => ({
      row,
      accountAccess: resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId })
    }))
    .filter((item): item is EligibleOpenAIGroupAccountSelection => Boolean(item.accountAccess))
    .filter((item) => isOpenAIAccountAvailableForSelection(item.row, item.row, item.accountAccess, now, options.includeUnavailable === true))
  const qualityByAccountId = loadFreshGatewayDispatchCandidateQualityRows(eligibleRows.map((item) => item.row.account_id), qualityFreshAfter)
  applyGatewayDispatchCandidateQualityRows(eligibleRows, qualityByAccountId)

  const accounts: OpenAIAccountSecret[] = []
  const orderedEligibleRows = orderGatewayDispatchCandidateRowsForDispatch(eligibleRows, {
    modelRankByAccountId: modelCandidateRows?.modelRankByAccountId
  })
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

export async function listOpenAIAccountsForGroupResultAsync(
  groupId: string,
  systemAccountId: string,
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata; requestedModel?: string; requestedEndpointFamily?: GatewayRequestEndpointFamily; includeUnavailable?: boolean } = {}
): Promise<OpenAIAccountsForGroupResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listOpenAIAccountsForGroupResult(groupId, systemAccountId, options)
  }
  const client = await getOpenAIAccountSelectorDatabaseClient()
  const now = nowIso()
  const qualityFreshAfter = gatewayDispatchCandidateQualityFreshAfterIso()
  const groupAccess = options.preResolvedGroupAccess ?? await resolveGroupUsageAccessMetadataAsync(groupId, systemAccountId)
  if (!groupAccess) {
    return {
      accounts: [],
      diagnostics: emptyGatewayDispatchCandidateDiagnostics()
    }
  }
  const candidateWindowOptions = { includeUnavailable: options.includeUnavailable === true }
  const modelCandidateRows = options.requestedModel?.trim()
    ? await listGatewayDispatchModelCandidateRowsAsync(client, groupId, groupAccess, now, options.requestedModel, options.requestedEndpointFamily, candidateWindowOptions)
    : undefined
  const groupAccountRows = modelCandidateRows
    ? mergeOpenAIGroupAccountRowsForDispatch(
      modelCandidateRows.rows,
      await listGatewayDispatchCandidateRowsAsync(client, groupId, groupAccess, now, candidateWindowOptions)
    )
    : await listGatewayDispatchCandidateRowsAsync(client, groupId, groupAccess, now, candidateWindowOptions)
  const accountAuthorizationsByIdOrResourceId = await loadAccountAuthorizationsForSelectionAsync(client, groupAccountRows, groupAccess, systemAccountId) ?? new Map()
  const eligibleRows = groupAccountRows
    .map((row) => ({
      row,
      accountAccess: resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId })
    }))
    .filter((item): item is EligibleOpenAIGroupAccountSelection => Boolean(item.accountAccess))
    .filter((item) => isOpenAIAccountAvailableForSelection(item.row, item.row, item.accountAccess, now, options.includeUnavailable === true))
  const qualityByAccountId = await loadFreshGatewayDispatchCandidateQualityRowsAsync(client, eligibleRows.map((item) => item.row.account_id), qualityFreshAfter)
  applyGatewayDispatchCandidateQualityRows(eligibleRows, qualityByAccountId)

  const accounts: OpenAIAccountSecret[] = []
  const orderedEligibleRows = orderGatewayDispatchCandidateRowsForDispatch(eligibleRows, {
    modelRankByAccountId: modelCandidateRows?.modelRankByAccountId
  })
  let hydrationBatchCount = 0
  let hydrationDroppedCount = 0
  for (let offset = 0; offset < orderedEligibleRows.length && accounts.length < gatewayDispatchAccountCandidateLimit; offset += gatewayDispatchAccountCandidateLimit) {
    const hydrationRows = orderedEligibleRows.slice(offset, offset + gatewayDispatchAccountCandidateLimit)
    hydrationBatchCount += 1
    const resourceAccountIds = hydrationRows.map((item) => openAIAccountResourceAccountId(item.row))
    const supportedModelsByAccountId = await loadSupportedModelsByAccountIdsAsync(resourceAccountIds)
    const modelMappingsByAccountId = await loadModelMappingsByAccountIdsAsync(resourceAccountIds)
    const apiKeyRuntimeStatesByAccountId = await loadAccountApiKeyRuntimeStatesByAccountIdsAsync(resourceAccountIds)
    const proxyProfilesById = await loadProxyProfilesForSelectionAsync(hydrationRows.map((item) => item.row))
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

function normalizeRecoverableUnavailableWindowMs(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(0, Math.trunc(value))
    : 30_000
}

function accountRecoverableCooldownUntilMs(account: OpenAIAccountSecret): number | undefined {
  if (!account.cooldownUntil) {
    return undefined
  }
  const cooldownUntilMs = Date.parse(account.cooldownUntil)
  return Number.isFinite(cooldownUntilMs) ? cooldownUntilMs : undefined
}

function mergeOpenAIGroupAccountRowsForDispatch(
  preferredRows: OpenAIGroupAccountSelectionRow[],
  fallbackRows: OpenAIGroupAccountSelectionRow[]
): OpenAIGroupAccountSelectionRow[] {
  const seenAccountIds = new Set<string>()
  const rows: OpenAIGroupAccountSelectionRow[] = []
  for (const row of [...preferredRows, ...fallbackRows]) {
    const accountId = row.account_id || row.id
    if (seenAccountIds.has(accountId)) {
      continue
    }
    seenAccountIds.add(accountId)
    rows.push(row)
  }
  return rows
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
  const apiKey = runtimeCredentialSource(resourceType, credentials, apiKeyEntries[0]?.key)
  if (!apiKey) {
    return undefined
  }
  const apiKeys = resourceType === 'api_key'
    ? apiKeyEntries.map((entry) => entry.key)
    : undefined
  const resourceAccountId = openAIAccountResourceAccountId(row)
  const apiKeyPoolEnabled = isAccountApiKeyPoolIsolationEnabled({
    providerCode: openAIAccountResourceProviderCode(row),
    protocolCode: openAIAccountResourceProtocolCode(row),
    protocolVersion: openAIAccountResourceProtocolVersion(row),
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
  const providerCode = openAIAccountResourceProviderCode(row)
  const protocolCode = openAIAccountResourceProtocolCode(row)
  const protocolVersion = openAIAccountResourceProtocolVersion(row)
  const clientCompatibility = openAIAccountResourceClientCompatibility(row)
  const providerProtocolProfileId = openAIAccountResourceProviderProtocolProfileId(row)
  const supportedEndpointModes = normalizeGatewayEndpointModesForRuntime(credentials.supported_endpoint_modes, {
    providerCode,
    accountType: resourceType,
    clientCompatibility,
    providerProtocolProfileId,
    protocolCode,
    protocolVersion
  })
  return {
    id: row.id,
    configRevision: Number(row.config_revision ?? 1),
    dispatchRevision: Number(row.dispatch_revision),
    providerCode,
    providerProtocolProfileId,
    protocolCode,
    protocolVersion,
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
    clientCompatibility,
    supportedEndpointModes,
    supportedModels: [...(options.supportedModelsByAccountId?.get(resourceAccountId) ?? [])],
    modelMappings: [...(options.modelMappingsByAccountId?.get(resourceAccountId) ?? [])],
    healthCheckModel: row.health_check_model.trim(),
    healthCheckEndpointMode: row.health_check_endpoint_mode,
    qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
    qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
    qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
    baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : defaultBaseUrlForProtocol(protocolCode, protocolVersion),
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

function runtimeCredentialSource(
  accountType: AccountType,
  credentials: Record<string, unknown>,
  apiKey: string | undefined
): string {
  if (accountType === 'oauth' || accountType === 'google_oauth') {
    return typeof credentials.access_token === 'string' && credentials.access_token
      ? credentials.access_token
      : typeof credentials.refresh_token === 'string' ? credentials.refresh_token : ''
  }
  return apiKey ?? ''
}

function normalizeGatewayEndpointModesForRuntime(
  value: unknown,
  input: {
    providerCode: string
    accountType: AccountType
    clientCompatibility: AccountClientCompatibility
    providerProtocolProfileId: string
    protocolCode: string
    protocolVersion: string
  }
) {
  if (isHybridProviderCode(input.providerCode)) {
    return normalizeHybridEndpointModesForRuntime(value)
  }
  if (isAnthropicProtocolProfile(input)) {
    return normalizeAnthropicEndpointModesForRuntime(value, {
      providerCode: input.providerCode,
      accountType: input.accountType,
      protocolCode: input.protocolCode,
      protocolVersion: input.protocolVersion,
      providerProtocolProfileId: input.providerProtocolProfileId
    })
  }
  if (isGeminiProtocolProfile(input)) {
    return normalizeGeminiEndpointModesForRuntime(value, {
      providerCode: input.providerCode,
      accountType: input.accountType,
      protocolCode: input.protocolCode,
      protocolVersion: input.protocolVersion,
      providerProtocolProfileId: input.providerProtocolProfileId
    })
  }
  return normalizeOpenAIEndpointModesForRuntime(value, {
    providerCode: input.providerCode,
    accountType: input.accountType,
    clientCompatibility: input.clientCompatibility
  })
}

function defaultBaseUrlForProtocol(protocolCode: string, protocolVersion: string): string {
  if (isAnthropicProtocolProfile({ protocolCode, protocolVersion })) {
    return 'https://api.anthropic.com/v1'
  }
  if (isGeminiProtocolProfile({ protocolCode, protocolVersion })) {
    return 'https://generativelanguage.googleapis.com'
  }
  return 'https://api.openai.com/v1'
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

async function loadAccountAuthorizationsForSelectionAsync(
  client: DatabaseClient,
  rows: OpenAIGroupAccountSelectionRow[],
  groupAccess: GroupUsageAccessMetadata,
  systemAccountId: string
): Promise<Map<string, ResourceAuthorizationRow> | undefined> {
  if (groupAccess.groupAccessType === 'authorized') return undefined
  const result = new Map<string, ResourceAuthorizationRow>()
  const authorizationIds = rows
    .map((row) => row.authorization_instance_authorization_id ?? '')
    .filter(Boolean)
  for (const authorization of (await activeResourceAuthorizationsByIdsForSelectorAsync(client, authorizationIds, systemAccountId)).values()) {
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

async function loadProxyProfilesForSelectionAsync(rows: OpenAIGroupAccountSelectionRow[]): Promise<Map<string, ProxyProfileUrlResolution> | undefined> {
  const proxyProfileIds = rows
    .map((row) => openAIAccountResourceProxyProfileId(row) ?? '')
    .filter(Boolean)
  if (!proxyProfileIds.length) return undefined
  return await resolveProxyUrlsForProfilesAsync(proxyProfileIds)
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

async function activeResourceAuthorizationForSelectorAsync(
  client: DatabaseClient,
  resourceType: ResourceAuthorizationResourceType,
  resourceId: string,
  granteeSystemAccountId: string
): Promise<ResourceAuthorizationRow | undefined> {
  const now = nowIso()
  return await client.one<ResourceAuthorizationRow>(`
    SELECT ${resourceAuthorizationSelectColumns()}
    FROM ${selectorTable(client, 'resource_authorizations')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_system_account_id = ?
      AND status = 'active'
      AND (expires_at IS NULL OR expires_at > ?)
    LIMIT 1
  `, [resourceType, resourceId, granteeSystemAccountId, now])
}

async function activeResourceAuthorizationsByIdsForSelectorAsync(
  client: DatabaseClient,
  authorizationIds: string[],
  granteeSystemAccountId: string
): Promise<Map<string, ResourceAuthorizationRow>> {
  const ids = [...new Set(authorizationIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const now = nowIso()
  const rows: ResourceAuthorizationRow[] = []
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...await client.query<ResourceAuthorizationRow>(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM ${selectorTable(client, 'resource_authorizations')}
      WHERE grantee_system_account_id = ?
        AND status = 'active'
        AND (expires_at IS NULL OR expires_at > ?)
        AND id IN (${chunk.map(() => '?').join(', ')})
    `, [granteeSystemAccountId, now, ...chunk]))
  }
  return new Map(rows.map((row) => [row.id, row]))
}

async function getOpenAIAccountSelectorDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function selectorTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
