import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountStatus, AccountType, GroupSchedulingPolicy, GroupType, ResourceAuthorizationSourceType } from '../domain/types.js'
import { loadSupportedModelsByAccountIds, loadSupportedModelsForAccount } from './account-supported-models.repository.js'
import { isAccountAvailabilityScheduleAllowed } from './account-availability-schedule.js'
import { currentSystemAccountId } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getDatabase, getStatsDatabase, nowIso } from './database.js'
import { ProxyProfileUnavailableError, resolveProxyUrlForProfile, resolveProxyUrlsForProfiles, type ProxyProfileUrlResolution } from './proxy.repository.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { activeResourceAuthorization, activeResourceAuthorizationById, activeResourceAuthorizationsByIds, activeResourceAuthorizationsByResourceIds } from './resource-authorization-helpers.js'
import { ensureAccountAuthorizationInstancesForGrantee } from './resource-authorization-write-state.repository.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'

export interface OpenAIAccountSecret {
  id: string
  providerCode?: 'openai'
  systemAccountId: string
  accountOwnerSystemAccountId: string
  groupOwnerSystemAccountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationExpiresAt?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  bindingSystemAccountId?: string
  boundGroupId?: string
  groupAuthorizationId?: string
  groupAuthorizationExpiresAt?: string
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
  name: string
  type: AccountType
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  supportedModels?: string[]
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
  passthroughEnabled: boolean
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
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
}

interface GroupAccountRow {
  account_id: string
  binding_system_account_id?: string | null
  group_id?: string | null
  account_authorization_id?: string | null
  local_status?: AccountStatus | null
  local_cooldown_until?: string | null
  local_last_error_message?: string | null
  local_priority?: number | null
  local_stream_failure_count?: number | null
  local_stream_failure_window_started_at?: string | null
  local_super_priority_enabled?: number | null
  local_fallback_enabled?: number | null
}

type OpenAIGroupAccountSelectionRow = GroupAccountRow & OpenAIAccountRow

type OpenAIAccountAccess = {
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  accountOwnerSystemAccountId?: string
  accountAuthorizationId?: string
  accountAuthorizationExpiresAt?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
}

type OpenAIAccountSecretOptions = {
  enforceSchedulableAuthorization?: boolean
  accountAuthorizationsByIdOrResourceId?: Map<string, ResourceAuthorizationRow>
  proxyProfilesById?: Map<string, ProxyProfileUrlResolution>
  supportedModelsByAccountId?: Map<string, string[]>
  accountAccess?: OpenAIAccountAccess
}

export function selectOpenAIAccountForGroup(groupId: string, systemAccountId = currentSystemAccountId()): OpenAIAccountSecret | undefined {
  return listOpenAIAccountsForGroup(groupId, systemAccountId)[0]
}

export function findOpenAIAccountForGroup(
  groupId: string,
  accountId: string,
  systemAccountId = currentSystemAccountId(),
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean } = {}
): OpenAIAccountSecret | undefined {
  const now = nowIso()
  const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return undefined
  }
  ensureAccountAuthorizationInstancesForGrantee(systemAccountId)
  const forceAvailability = options.ignoreAvailability === true
  const groupAccount = getDatabase()
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_status, group_accounts.local_cooldown_until, group_accounts.local_last_error_message,
        group_accounts.local_stream_failure_count, group_accounts.local_stream_failure_window_started_at,
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

  const row = getDatabase()
    .prepare(`
      SELECT accounts.id, accounts.system_account_id, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
        accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.passthrough_enabled, accounts.error_policy_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.availability_schedule_json, accounts.account_expires_at, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.type AS resource_type,
        source_accounts.credentials_encrypted AS resource_credentials_encrypted,
        source_accounts.proxy_profile_id AS resource_proxy_profile_id,
        source_accounts.concurrency_limit AS resource_concurrency_limit,
        source_accounts.passthrough_enabled AS resource_passthrough_enabled,
        source_accounts.error_policy_id AS resource_error_policy_id,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM accounts
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE accounts.id = ?
        AND accounts.provider_code = 'openai'
        AND COALESCE(source_accounts.type, accounts.type) IN ('api_key', 'oauth')
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
    accountAccess
  })
}

export function resolveGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const groupRow = getDatabase()
    .prepare('SELECT system_account_id, group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(groupId) as unknown as { system_account_id?: string; group_type?: GroupType | null; scheduling_policy_json?: string | null } | undefined
  const groupOwnerSystemAccountId = groupRow?.system_account_id
  if (!groupOwnerSystemAccountId) return undefined
  const groupType = normalizeGroupType(groupRow?.group_type)
  const schedulingPolicy = parseGroupSchedulingPolicyJson(groupRow?.scheduling_policy_json ?? null, groupType)
  if (groupOwnerSystemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId, groupAccessType: 'owner', groupType, schedulingPolicy }
  }
  const authorization = activeResourceAuthorization('group', groupId, systemAccountId)
  if (!authorization) return undefined
  return {
    groupOwnerSystemAccountId,
    groupAccessType: 'authorized',
    groupType,
    schedulingPolicy,
    groupAuthorizationId: authorization.id,
    groupAuthorizationExpiresAt: authorization.expires_at ?? undefined,
    groupAuthorizationSourceType: authorization.effective_source_type ?? undefined,
    groupAuthorizationSourceTeamId: authorization.effective_source_team_id ?? undefined
  }
}

export function listOpenAIAccountsForGroup(
  groupId: string,
  systemAccountId = currentSystemAccountId(),
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata } = {}
): OpenAIAccountSecret[] {
  const database = getDatabase()
  const now = nowIso()
  const qualityFreshAfter = qualityFreshAfterIso()
  const groupAccess = options.preResolvedGroupAccess ?? resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return []
  }
  ensureAccountAuthorizationInstancesForGrantee(systemAccountId, database)
  const groupAccountRows = database
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_status, group_accounts.local_cooldown_until, group_accounts.local_last_error_message,
        group_accounts.local_stream_failure_count, group_accounts.local_stream_failure_window_started_at,
        group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
        accounts.id, accounts.system_account_id, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
        accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.passthrough_enabled, accounts.error_policy_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.availability_schedule_json, accounts.account_expires_at, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.type AS resource_type,
        source_accounts.credentials_encrypted AS resource_credentials_encrypted,
        source_accounts.proxy_profile_id AS resource_proxy_profile_id,
        source_accounts.concurrency_limit AS resource_concurrency_limit,
        source_accounts.passthrough_enabled AS resource_passthrough_enabled,
        source_accounts.error_policy_id AS resource_error_policy_id,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM group_accounts
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_code = 'openai'
        AND COALESCE(source_accounts.type, accounts.type) IN ('api_key', 'oauth')
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY
        group_accounts.local_fallback_enabled ASC,
        group_accounts.local_super_priority_enabled DESC,
        group_accounts.local_priority ASC,
        group_accounts.created_at ASC,
        group_accounts.account_id ASC
    `)
    .all(groupId, groupAccess.groupOwnerSystemAccountId, now) as unknown as OpenAIGroupAccountSelectionRow[]
  const accountAuthorizationsByIdOrResourceId = loadAccountAuthorizationsForSelection(groupAccountRows, groupAccess, systemAccountId)
  const eligibleRows = groupAccountRows
    .map((row) => ({
      row,
      accountAccess: resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId })
    }))
    .filter((item): item is { row: OpenAIGroupAccountSelectionRow; accountAccess: OpenAIAccountAccess } => Boolean(item.accountAccess))
    .filter((item) => isOpenAIAccountAvailableForSelection(item.row, item.row, item.accountAccess, now, false))
  const qualityByAccountId = loadFreshAccountQualityRows(eligibleRows.map((item) => item.row.account_id), qualityFreshAfter)
  const supportedModelsByAccountId = loadSupportedModelsByAccountIds(eligibleRows.map((item) => openAIAccountResourceAccountId(item.row)))
  const proxyProfilesById = loadProxyProfilesForSelection(eligibleRows.map((item) => item.row))

  const accounts: OpenAIAccountSecret[] = []
  for (const { row, accountAccess } of eligibleRows) {
    const quality = qualityByAccountId.get(row.id)
    if (quality) {
      row.quality_score = quality.quality_score
      row.quality_state = quality.quality_state
      row.quality_ewma_first_token_ms = quality.quality_ewma_first_token_ms
    }
    const account = openAIAccountSecretFromRow(row, groupAccess, systemAccountId, row, { accountAuthorizationsByIdOrResourceId, proxyProfilesById, supportedModelsByAccountId, accountAccess })
    if (account) {
      accounts.push(account)
    }
  }

  return orderOpenAIAccountsForDispatch(accounts)
}

export function hasOpenAIAccountAvailabilityScheduleForGroup(
  groupId: string,
  systemAccountId = currentSystemAccountId(),
  options: { preResolvedGroupAccess?: GroupUsageAccessMetadata } = {}
): boolean {
  const groupAccess = options.preResolvedGroupAccess ?? resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) return false
  const row = getDatabase()
    .prepare(`
      SELECT 1
      FROM group_accounts
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_code = 'openai'
        AND COALESCE(source_accounts.type, accounts.type) IN ('api_key', 'oauth')
        AND accounts.availability_schedule_json IS NOT NULL
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
  credentials_encrypted: string
  proxy_profile_id: string | null
  passthrough_enabled: number
  error_policy_id: string | null
  cooldown_until: string | null
  last_error_message: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
  availability_schedule_json: string | null
  account_expires_at: string | null
  authorization_instance_source_account_id?: string | null
  authorization_instance_authorization_id?: string | null
  authorization_instance_owner_system_account_id?: string | null
  resource_account_id?: string | null
  resource_type?: AccountType | null
  resource_credentials_encrypted?: string | null
  resource_proxy_profile_id?: string | null
  resource_concurrency_limit?: number | null
  resource_passthrough_enabled?: number | null
  resource_error_policy_id?: string | null
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

function openAIAccountResourcePassthroughEnabled(row: OpenAIAccountRow): number {
  return Number(row.resource_passthrough_enabled ?? row.passthrough_enabled ?? 0)
}

function openAIAccountResourceErrorPolicyId(row: OpenAIAccountRow): string | null {
  return row.resource_error_policy_id ?? row.error_policy_id
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
    accountAuthorizationSourceType: accountAccess.accountAuthorizationSourceType,
    accountAuthorizationSourceTeamId: accountAccess.accountAuthorizationSourceTeamId,
    bindingSystemAccountId: isLocalAccountAuthorized ? groupAccount?.binding_system_account_id ?? groupAccess.groupOwnerSystemAccountId : undefined,
    boundGroupId: isLocalAccountAuthorized ? groupAccount?.group_id ?? undefined : undefined,
    groupAuthorizationId: groupAccess.groupAuthorizationId,
    groupAuthorizationExpiresAt: groupAccess.groupAuthorizationExpiresAt,
    groupAuthorizationSourceType: groupAccess.groupAuthorizationSourceType,
    groupAuthorizationSourceTeamId: groupAccess.groupAuthorizationSourceTeamId,
    name: row.name,
    type: resourceType,
    status: runtimeStatus,
    concurrencyLimit: openAIAccountResourceConcurrencyLimit(row),
    priority: dispatchPriority,
    superPriorityEnabled: runtimeStatus === 'active' && dispatchSuperPriorityEnabled,
    fallbackEnabled: runtimeStatus === 'active' && dispatchFallbackEnabled,
    supportedModels: [...(options.supportedModelsByAccountId?.get(resourceAccountId) ?? [])],
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
    passthroughEnabled: openAIAccountResourcePassthroughEnabled(row) === 1,
    errorPolicyId: openAIAccountResourceErrorPolicyId(row) ?? undefined,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
    streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
    availabilityScheduleJson: row.availability_schedule_json ?? undefined,
    accountExpiresAt: row.account_expires_at ?? undefined,
    expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : undefined,
    credentials
  }
}

function compareOpenAIAccountsByQuality(left: OpenAIAccountSecret, right: OpenAIAccountSecret): number {
  const leftQuality = left.qualityScore
  const rightQuality = right.qualityScore
  const leftHasQuality = typeof leftQuality === 'number'
  const rightHasQuality = typeof rightQuality === 'number'
  if (leftHasQuality !== rightHasQuality) return leftHasQuality ? -1 : 1
  if (leftHasQuality && rightHasQuality && leftQuality !== rightQuality) {
    return leftQuality - rightQuality
  }
  const nameDelta = left.name.localeCompare(right.name, 'zh-CN')
  return nameDelta !== 0 ? nameDelta : left.id.localeCompare(right.id)
}

function orderOpenAIAccountsForDispatch(accounts: OpenAIAccountSecret[]): OpenAIAccountSecret[] {
  const buckets = new Map<string, OpenAIAccountSecret[]>()
  for (const account of accounts) {
    const bucketKey = `${account.fallbackEnabled ? 1 : 0}:${account.superPriorityEnabled ? 1 : 0}:${account.priority}`
    const bucket = buckets.get(bucketKey)
    if (bucket) {
      bucket.push(account)
    } else {
      buckets.set(bucketKey, [account])
    }
  }
  return [...accounts].sort((left, right) => {
    const leftBucket = buckets.get(`${left.fallbackEnabled ? 1 : 0}:${left.superPriorityEnabled ? 1 : 0}:${left.priority}`) ?? []
    const rightBucket = buckets.get(`${right.fallbackEnabled ? 1 : 0}:${right.superPriorityEnabled ? 1 : 0}:${right.priority}`) ?? []
    const leftFallback = left.fallbackEnabled ? 1 : 0
    const rightFallback = right.fallbackEnabled ? 1 : 0
    if (leftFallback !== rightFallback) return leftFallback - rightFallback
    const leftSuper = left.superPriorityEnabled ? 1 : 0
    const rightSuper = right.superPriorityEnabled ? 1 : 0
    if (leftSuper !== rightSuper) return rightSuper - leftSuper
    if (left.priority !== right.priority) return left.priority - right.priority
    if (leftBucket.length >= 2 && rightBucket.length >= 2) {
      return compareOpenAIAccountsByQuality(left, right)
    }
    const nameDelta = left.name.localeCompare(right.name, 'zh-CN')
    return nameDelta !== 0 ? nameDelta : left.id.localeCompare(right.id)
  })
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
  const legacyAuthorizedAccountIds = rows
    .filter((row) => row.system_account_id !== systemAccountId)
    .map((row) => row.id)
  for (const authorization of activeResourceAuthorizationsByResourceIds('account', legacyAuthorizedAccountIds, systemAccountId).values()) {
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
        accountAuthorizationSourceType: authorization.effective_source_type ?? undefined,
        accountAuthorizationSourceTeamId: authorization.effective_source_team_id ?? undefined
      }
    : undefined
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
