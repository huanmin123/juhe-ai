import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountClientCompatibility, AccountModelMapping, AccountStatus, AccountType, GroupSchedulingPolicy, GroupType, ResourceAuthorizationSourceType } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { loadModelMappingsByAccountIds, loadModelMappingsForAccount } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIds, loadSupportedModelsForAccount } from './account-supported-models.repository.js'
import { isAccountAvailabilityScheduleAllowed } from './account-availability-schedule.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { ProxyProfileUnavailableError, resolveProxyUrlForProfile, resolveProxyUrlsForProfiles, type ProxyProfileUrlResolution } from './proxy.repository.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { activeResourceAuthorization, activeResourceAuthorizationById, activeResourceAuthorizationsByIds } from './resource-authorization-helpers.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'

export interface OpenAIAccountSecret {
  id: string
  providerCode: 'openai'
  systemAccountId: string
  accountOwnerSystemAccountId: string
  groupOwnerSystemAccountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationExpiresAt?: string
  accountAuthorizationQuotaLimited?: boolean
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  bindingSystemAccountId?: string
  boundGroupId?: string
  groupAuthorizationId?: string
  groupAuthorizationExpiresAt?: string
  groupAuthorizationQuotaLimited?: boolean
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
  name: string
  type: AccountType
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  lastSuccessfulTestModel?: string
  qualityScore?: number
  qualityState?: string
  qualityEwmaFirstTokenMs?: number
  currentConcurrency?: number
  baseUrl: string
  apiKey: string
  refreshToken?: string
  clientId?: string
  credentialSourceAccountId?: string
  proxyProfileId?: string
  proxyUrl?: string
  proxyProfileUnavailable?: boolean
  proxyProfileErrorMessage?: string
  errorPolicyId?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  streamFailureCount: number
  streamFailureWindowStartedAt?: string
  availabilityScheduleJson?: string
  accountExpiresAt?: string
  expiresAt?: string
  credentials: Record<string, unknown>
}

export interface GroupUsageAccessMetadata {
  groupOwnerSystemAccountId: string
  groupAccessType: 'owner' | 'authorized'
  groupType?: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
  groupAuthorizationId?: string
  groupAuthorizationExpiresAt?: string
  groupAuthorizationQuotaLimited?: boolean
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
}

interface GroupAccountRow {
  account_id: string
  binding_system_account_id?: string | null
  group_id?: string | null
  account_authorization_id?: string | null
  local_priority?: number | null
  local_super_priority_enabled?: number | null
  local_fallback_enabled?: number | null
}

type OpenAIGroupAccountSelectionRow = GroupAccountRow & OpenAIAccountRow

type OpenAIAccountAccess = {
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  accountOwnerSystemAccountId?: string
  accountAuthorizationId?: string
  accountAuthorizationExpiresAt?: string
  accountAuthorizationQuotaLimited?: boolean
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
}

type EligibleOpenAIGroupAccountSelection = {
  row: OpenAIGroupAccountSelectionRow
  accountAccess: OpenAIAccountAccess
}

type OpenAIAccountSecretOptions = {
  enforceSchedulableAuthorization?: boolean
  accountAuthorizationsByIdOrResourceId?: Map<string, ResourceAuthorizationRow>
  proxyProfilesById?: Map<string, ProxyProfileUrlResolution>
  supportedModelsByAccountId?: Map<string, string[]>
  modelMappingsByAccountId?: Map<string, AccountModelMapping[]>
  accountAccess?: OpenAIAccountAccess
}

const gatewayDispatchAccountCandidateLimit = 256
const gatewayDispatchAccountCandidateScanLimit = gatewayDispatchAccountCandidateLimit * 2

type AccountAvailabilityScheduleCandidateFilter = 'without_schedule' | 'with_schedule'

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
      SELECT accounts.id, accounts.system_account_id, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
        accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.error_policy_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.availability_schedule_json, accounts.account_expires_at, accounts.last_successful_test_model, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.type AS resource_type,
        source_accounts.status AS resource_status,
        source_accounts.schedulable AS resource_schedulable,
        source_accounts.availability_schedule_json AS resource_availability_schedule_json,
        source_accounts.account_expires_at AS resource_account_expires_at,
        source_accounts.cooldown_until AS resource_cooldown_until,
        source_accounts.last_error_code AS resource_last_error_code,
        source_accounts.credentials_encrypted AS resource_credentials_encrypted,
        source_accounts.proxy_profile_id AS resource_proxy_profile_id,
        source_accounts.concurrency_limit AS resource_concurrency_limit,
        source_accounts.error_policy_id AS resource_error_policy_id,
        source_accounts.client_compatibility AS resource_client_compatibility,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM accounts
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE accounts.id = ?
        AND accounts.provider_code = 'openai'
        AND accounts.deleted_at IS NULL
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth'))
          OR (accounts.authorization_instance_authorization_id IS NOT NULL AND source_accounts.deleted_at IS NULL AND source_accounts.type IN ('api_key', 'oauth'))
        )
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    `)
    .get(accountId, now) as unknown as OpenAIAccountRow | undefined
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
  return openAIAccountSecretFromRow(row, groupAccess, systemAccountId, groupAccount, {
    supportedModelsByAccountId: new Map([[openAIAccountResourceAccountId(row), loadSupportedModelsForAccount(openAIAccountResourceAccountId(row))]]),
    modelMappingsByAccountId: new Map([[openAIAccountResourceAccountId(row), loadModelMappingsForAccount(openAIAccountResourceAccountId(row))]]),
    accountAccess
  })
}

export function resolveGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const groupRow = getBusinessDatabase()
    .prepare('SELECT system_account_id, enabled, group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(groupId) as unknown as { system_account_id?: string; enabled?: number; group_type?: GroupType | null; scheduling_policy_json?: string | null } | undefined
  const groupOwnerSystemAccountId = groupRow?.system_account_id
  if (!groupOwnerSystemAccountId) return undefined
  if (groupRow.enabled !== 1) return undefined
  const groupType = normalizeGroupType(groupRow?.group_type)
  const schedulingPolicy = parseGroupSchedulingPolicyJson(groupRow?.scheduling_policy_json ?? null, groupType)
  if (groupOwnerSystemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId, groupAccessType: 'owner', groupType, schedulingPolicy }
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

export interface OpenAIAccountsForGroupResult {
  accounts: OpenAIAccountSecret[]
  hasAccountAvailabilitySchedule: boolean
}

export function runtimeOpenAIAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  copyRuntimeCredentialText(credentials, output, 'account_id')
  copyRuntimeCredentialValue(credentials, output, 'error_handling_rules')
  copyRuntimeCredentialValue(credentials, output, 'stream_intercept_rules')
  return output
}

export function listOpenAIAccountsForGroupResult(
  groupId: string,
  systemAccountId: string,
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata } = {}
): OpenAIAccountsForGroupResult {
  const database = getBusinessDatabase()
  const now = nowIso()
  const qualityFreshAfter = qualityFreshAfterIso()
  const groupAccess = options.preResolvedGroupAccess ?? resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return { accounts: [], hasAccountAvailabilitySchedule: false }
  }
  const groupAccountRows = [
    ...listOpenAIGroupAccountSelectionRows(database, groupId, groupAccess.groupOwnerSystemAccountId, now, 'without_schedule'),
    ...listOpenAIGroupAccountSelectionRows(database, groupId, groupAccess.groupOwnerSystemAccountId, now, 'with_schedule')
  ]
  const hasAccountAvailabilitySchedule = groupAccountRows.some((row) => Boolean(row.availability_schedule_json || row.resource_availability_schedule_json))
  const accountAuthorizationsByIdOrResourceId = loadAccountAuthorizationsForSelection(groupAccountRows, groupAccess, systemAccountId)
  const eligibleRows = groupAccountRows
    .map((row) => ({
      row,
      accountAccess: resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId })
    }))
    .filter((item): item is EligibleOpenAIGroupAccountSelection => Boolean(item.accountAccess))
    .filter((item) => isOpenAIAccountAvailableForSelection(item.row, item.row, item.accountAccess, now, false))
  const qualityByAccountId = loadFreshAccountQualityRows(eligibleRows.map((item) => item.row.account_id), qualityFreshAfter)
  applyOpenAIAccountQualityRows(eligibleRows, qualityByAccountId)

  const accounts: OpenAIAccountSecret[] = []
  const orderedEligibleRows = orderOpenAIGroupAccountRowsForDispatch(eligibleRows)
  for (let offset = 0; offset < orderedEligibleRows.length && accounts.length < gatewayDispatchAccountCandidateLimit; offset += gatewayDispatchAccountCandidateLimit) {
    const hydrationRows = orderedEligibleRows.slice(offset, offset + gatewayDispatchAccountCandidateLimit)
    const resourceAccountIds = hydrationRows.map((item) => openAIAccountResourceAccountId(item.row))
    const supportedModelsByAccountId = loadSupportedModelsByAccountIds(resourceAccountIds)
    const modelMappingsByAccountId = loadModelMappingsByAccountIds(resourceAccountIds)
    const proxyProfilesById = loadProxyProfilesForSelection(hydrationRows.map((item) => item.row))
    for (const { row, accountAccess } of hydrationRows) {
      const account = openAIAccountSecretFromRow(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId, proxyProfilesById, supportedModelsByAccountId, modelMappingsByAccountId, accountAccess })
      if (account) {
        accounts.push(account)
        if (accounts.length >= gatewayDispatchAccountCandidateLimit) {
          break
        }
      }
    }
  }

  return {
    accounts,
    hasAccountAvailabilitySchedule
  }
}

function orderOpenAIGroupAccountRowsForDispatch(items: EligibleOpenAIGroupAccountSelection[]): EligibleOpenAIGroupAccountSelection[] {
  const buckets = new Map<string, number>()
  for (const item of items) {
    const bucketKey = openAIGroupAccountDispatchBucketKey(item.row)
    buckets.set(bucketKey, (buckets.get(bucketKey) ?? 0) + 1)
  }
  return [...items].sort((left, right) => {
    const leftFallback = openAIGroupAccountDispatchFallbackRank(left.row)
    const rightFallback = openAIGroupAccountDispatchFallbackRank(right.row)
    if (leftFallback !== rightFallback) return leftFallback - rightFallback
    const leftSuper = openAIGroupAccountDispatchSuperRank(left.row)
    const rightSuper = openAIGroupAccountDispatchSuperRank(right.row)
    if (leftSuper !== rightSuper) return rightSuper - leftSuper
    const leftPriority = openAIGroupAccountDispatchPriority(left.row)
    const rightPriority = openAIGroupAccountDispatchPriority(right.row)
    if (leftPriority !== rightPriority) return leftPriority - rightPriority
    const bucketKey = openAIGroupAccountDispatchBucketKey(left.row)
    if (bucketKey === openAIGroupAccountDispatchBucketKey(right.row) && (buckets.get(bucketKey) ?? 0) >= 2) {
      const qualityDelta = compareOpenAIAccountRowsByQuality(left.row, right.row)
      if (qualityDelta !== 0) return qualityDelta
    }
    const nameDelta = left.row.name.localeCompare(right.row.name, 'zh-CN')
    return nameDelta !== 0 ? nameDelta : left.row.id.localeCompare(right.row.id)
  })
}

function openAIGroupAccountDispatchBucketKey(row: OpenAIGroupAccountSelectionRow): string {
  return `${openAIGroupAccountDispatchFallbackRank(row)}:${openAIGroupAccountDispatchSuperRank(row)}:${openAIGroupAccountDispatchPriority(row)}`
}

function openAIGroupAccountDispatchFallbackRank(row: OpenAIGroupAccountSelectionRow): number {
  return row.local_fallback_enabled === 1 ? 1 : 0
}

function openAIGroupAccountDispatchSuperRank(row: OpenAIGroupAccountSelectionRow): number {
  return row.local_super_priority_enabled === 1 ? 1 : 0
}

function openAIGroupAccountDispatchPriority(row: OpenAIGroupAccountSelectionRow): number {
  return Number(row.local_priority ?? row.priority ?? 0)
}

function compareOpenAIAccountRowsByQuality(left: OpenAIAccountRow, right: OpenAIAccountRow): number {
  const leftQuality = left.quality_score
  const rightQuality = right.quality_score
  const leftHasQuality = typeof leftQuality === 'number'
  const rightHasQuality = typeof rightQuality === 'number'
  if (leftHasQuality !== rightHasQuality) return leftHasQuality ? -1 : 1
  if (leftHasQuality && rightHasQuality && leftQuality !== rightQuality) {
    return leftQuality - rightQuality
  }
  const nameDelta = left.name.localeCompare(right.name, 'zh-CN')
  return nameDelta !== 0 ? nameDelta : left.id.localeCompare(right.id)
}

function applyOpenAIAccountQualityRows(
  rows: EligibleOpenAIGroupAccountSelection[],
  qualityByAccountId: Map<string, Pick<OpenAIAccountRow, 'quality_score' | 'quality_state' | 'quality_ewma_first_token_ms'>>
): void {
  for (const { row } of rows) {
    const quality = qualityByAccountId.get(row.id)
    if (quality) {
      row.quality_score = quality.quality_score
      row.quality_state = quality.quality_state
      row.quality_ewma_first_token_ms = quality.quality_ewma_first_token_ms
    }
  }
}

function listOpenAIGroupAccountSelectionRows(
  database: ReturnType<typeof getBusinessDatabase>,
  groupId: string,
  groupOwnerSystemAccountId: string,
  now: string,
  scheduleFilter: AccountAvailabilityScheduleCandidateFilter
): OpenAIGroupAccountSelectionRow[] {
  const scheduleClause = scheduleFilter === 'with_schedule'
    ? 'AND (accounts.availability_schedule_json IS NOT NULL OR source_accounts.availability_schedule_json IS NOT NULL)'
    : 'AND accounts.availability_schedule_json IS NULL AND source_accounts.availability_schedule_json IS NULL'
  return database
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
        accounts.id, accounts.system_account_id, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
        accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.error_policy_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.availability_schedule_json, accounts.account_expires_at, accounts.last_successful_test_model, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.type AS resource_type,
        source_accounts.status AS resource_status,
        source_accounts.schedulable AS resource_schedulable,
        source_accounts.availability_schedule_json AS resource_availability_schedule_json,
        source_accounts.account_expires_at AS resource_account_expires_at,
        source_accounts.cooldown_until AS resource_cooldown_until,
        source_accounts.last_error_code AS resource_last_error_code,
        source_accounts.credentials_encrypted AS resource_credentials_encrypted,
        source_accounts.proxy_profile_id AS resource_proxy_profile_id,
        source_accounts.concurrency_limit AS resource_concurrency_limit,
        source_accounts.error_policy_id AS resource_error_policy_id,
        source_accounts.client_compatibility AS resource_client_compatibility,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM group_accounts INDEXED BY idx_group_accounts_dispatch_candidate_window
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_code = 'openai'
        AND accounts.deleted_at IS NULL
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        ${scheduleClause}
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth'))
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND source_accounts.deleted_at IS NULL
            AND source_accounts.type IN ('api_key', 'oauth')
            AND source_accounts.status = 'active'
            AND source_accounts.schedulable = 1
            AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
            AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
            AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
          )
        )
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY
        group_accounts.local_fallback_enabled ASC,
        group_accounts.local_super_priority_enabled DESC,
        group_accounts.local_priority ASC,
        group_accounts.created_at ASC,
        group_accounts.account_id ASC
      LIMIT ?
    `)
    .all(groupId, groupOwnerSystemAccountId, now, now, now, now, gatewayDispatchAccountCandidateScanLimit) as unknown as OpenAIGroupAccountSelectionRow[]
}

export function hasOpenAIAccountAvailabilityScheduleForGroup(
  groupId: string,
  systemAccountId: string,
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata } = {}
): boolean {
  const groupAccess = options.preResolvedGroupAccess ?? resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) return false
  const row = getBusinessDatabase()
    .prepare(`
      SELECT 1
      FROM group_accounts
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_code = 'openai'
        AND accounts.deleted_at IS NULL
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth'))
          OR (accounts.authorization_instance_authorization_id IS NOT NULL AND source_accounts.deleted_at IS NULL AND source_accounts.type IN ('api_key', 'oauth'))
        )
        AND (
          accounts.availability_schedule_json IS NOT NULL
          OR source_accounts.availability_schedule_json IS NOT NULL
        )
      LIMIT 1
    `)
    .get(groupId, groupAccess.groupOwnerSystemAccountId) as unknown
  return Boolean(row)
}

interface OpenAIAccountRow {
  id: string
  system_account_id: string
  name: string
  type: AccountType
  status: AccountStatus
  schedulable: number
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  client_compatibility: AccountClientCompatibility
  credentials_encrypted: string
  proxy_profile_id: string | null
  error_policy_id: string | null
  cooldown_until: string | null
  last_error_message: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
  availability_schedule_json: string | null
  account_expires_at: string | null
  last_successful_test_model: string | null
  authorization_instance_source_account_id?: string | null
  authorization_instance_authorization_id?: string | null
  authorization_instance_owner_system_account_id?: string | null
  resource_account_id?: string | null
  resource_type?: AccountType | null
  resource_status?: AccountStatus | null
  resource_schedulable?: number | null
  resource_availability_schedule_json?: string | null
  resource_account_expires_at?: string | null
  resource_cooldown_until?: string | null
  resource_last_error_code?: string | null
  resource_credentials_encrypted?: string | null
  resource_proxy_profile_id?: string | null
  resource_concurrency_limit?: number | null
  resource_error_policy_id?: string | null
  resource_client_compatibility?: AccountClientCompatibility | null
  quality_score?: number | null
  quality_state?: string | null
  quality_ewma_first_token_ms?: number | null
}

function openAIAccountResourceAccountId(row: OpenAIAccountRow): string {
  return row.resource_account_id ?? row.id
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

function openAIAccountResourceErrorPolicyId(row: OpenAIAccountRow): string | null {
  return row.resource_error_policy_id ?? row.error_policy_id
}

function openAIAccountResourceClientCompatibility(row: OpenAIAccountRow): AccountClientCompatibility {
  return normalizeOpenAIAccountClientCompatibility('openai', openAIAccountResourceType(row), row.resource_client_compatibility ?? row.client_compatibility)
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
  const apiKey = resourceType === 'oauth'
    ? typeof credentials.access_token === 'string' ? credentials.access_token : ''
    : typeof credentials.api_key === 'string' ? credentials.api_key : ''
  if (!apiKey) {
    return undefined
  }
  const resourceAccountId = openAIAccountResourceAccountId(row)
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
    providerCode: 'openai',
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
    refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : undefined,
    clientId: typeof credentials.client_id === 'string' ? credentials.client_id : undefined,
    credentialSourceAccountId: resourceAccountId !== row.id ? resourceAccountId : undefined,
    proxyProfileId: resourceProxyProfileId ?? undefined,
    proxyUrl: proxyProfile.proxyUrl,
    proxyProfileUnavailable: proxyProfile.unavailable,
    proxyProfileErrorMessage: proxyProfile.errorMessage,
    errorPolicyId: openAIAccountResourceErrorPolicyId(row) ?? undefined,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
    streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
    availabilityScheduleJson: row.availability_schedule_json ?? undefined,
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
  if (!isAccountAvailabilityScheduleAllowed(row.availability_schedule_json, new Date(now))) {
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
  if (!isAccountAvailabilityScheduleAllowed(row.resource_availability_schedule_json, new Date(now))) {
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

function qualityFreshAfterIso(): string {
  return new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString()
}

function loadFreshAccountQualityRows(accountIds: string[], freshAfter: string): Map<string, Pick<OpenAIAccountRow, 'quality_score' | 'quality_state' | 'quality_ewma_first_token_ms'>> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows: Array<{
    account_id: string
    quality_score: number | null
    quality_state: string | null
    quality_ewma_first_token_ms: number | null
  }> = []
  const database = getStatsDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT account_id, quality_score, quality_state, ewma_first_token_ms AS quality_ewma_first_token_ms
        FROM account_quality_scores
        WHERE account_id IN (${sqlPlaceholders(chunk.length)})
          AND last_sample_at >= ?
      `)
      .all(...chunk, freshAfter) as unknown as Array<{
        account_id: string
        quality_score: number | null
        quality_state: string | null
        quality_ewma_first_token_ms: number | null
      }>)
  }
  return new Map(rows.map((row) => [row.account_id, row]))
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
