import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountStatus, AccountType, GroupSchedulingPolicy, GroupType, ResourceAuthorizationSourceType } from '../domain/types.js'
import { loadSupportedModelsByAccountIds, loadSupportedModelsForAccount } from './account-supported-models.repository.js'
import { currentSystemAccountId } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getDatabase, getStatsDatabase, nowIso } from './database.js'
import { ProxyProfileUnavailableError, resolveProxyUrlForProfile, resolveProxyUrlsForProfiles, type ProxyProfileUrlResolution } from './proxy.repository.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { activeResourceAuthorization, activeResourceAuthorizationsByResourceIds } from './resource-authorization-helpers.js'
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
  local_stream_failure_count?: number | null
  local_stream_failure_window_started_at?: string | null
  local_super_priority_enabled?: number | null
  local_fallback_enabled?: number | null
}

type OpenAIGroupAccountSelectionRow = GroupAccountRow & OpenAIAccountRow

type OpenAIAccountAccess = {
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  accountAuthorizationId?: string
  accountAuthorizationExpiresAt?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
}

type OpenAIAccountSecretOptions = {
  enforceSchedulableAuthorization?: boolean
  accountAuthorizationsByResourceId?: Map<string, ResourceAuthorizationRow>
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
  const forceAvailability = options.ignoreAvailability === true
  const groupAccount = getDatabase()
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_status, group_accounts.local_cooldown_until, group_accounts.local_last_error_message,
        group_accounts.local_stream_failure_count, group_accounts.local_stream_failure_window_started_at,
        group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled
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
      SELECT id, system_account_id, name, type, status, schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled, credentials_encrypted, proxy_profile_id, passthrough_enabled, error_policy_id, cooldown_until, last_error_message, stream_failure_count, stream_failure_window_started_at,
        account_expires_at,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM accounts
      WHERE id = ?
        AND provider_code = 'openai'
        AND type IN ('api_key', 'oauth')
        AND (account_expires_at IS NULL OR account_expires_at > ?)
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
    supportedModelsByAccountId: new Map([[row.id, loadSupportedModelsForAccount(row.id)]]),
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
  const groupAccountRows = database
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_status, group_accounts.local_cooldown_until, group_accounts.local_last_error_message,
        group_accounts.local_stream_failure_count, group_accounts.local_stream_failure_window_started_at,
        group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
        accounts.id, accounts.system_account_id, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
        accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.passthrough_enabled, accounts.error_policy_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.account_expires_at,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM group_accounts
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_code = 'openai'
        AND accounts.type IN ('api_key', 'oauth')
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY
        CASE WHEN group_accounts.account_authorization_id IS NOT NULL THEN group_accounts.local_fallback_enabled ELSE accounts.fallback_enabled END ASC,
        CASE WHEN group_accounts.account_authorization_id IS NOT NULL THEN group_accounts.local_super_priority_enabled ELSE accounts.super_priority_enabled END DESC,
        CASE WHEN group_accounts.account_authorization_id IS NOT NULL THEN 0 ELSE accounts.priority END ASC,
        group_accounts.created_at ASC,
        group_accounts.account_id ASC
    `)
    .all(groupId, groupAccess.groupOwnerSystemAccountId, now) as unknown as OpenAIGroupAccountSelectionRow[]
  const accountAuthorizationsByResourceId = loadAccountAuthorizationsForSelection(groupAccountRows, groupAccess, systemAccountId)
  const eligibleRows = groupAccountRows
    .map((row) => ({
      row,
      accountAccess: resolveSchedulableOpenAIAccountAccess(row, groupAccess, systemAccountId, row, { accountAuthorizationsByResourceId })
    }))
    .filter((item): item is { row: OpenAIGroupAccountSelectionRow; accountAccess: OpenAIAccountAccess } => Boolean(item.accountAccess))
    .filter((item) => isOpenAIAccountAvailableForSelection(item.row, item.row, item.accountAccess, now, false))
  const qualityByAccountId = loadFreshAccountQualityRows(eligibleRows.map((item) => item.row.account_id), qualityFreshAfter)
  const supportedModelsByAccountId = loadSupportedModelsByAccountIds(eligibleRows.map((item) => item.row.id))
  const proxyProfilesById = loadProxyProfilesForSelection(eligibleRows.map((item) => item.row))

  const accounts: OpenAIAccountSecret[] = []
  for (const { row, accountAccess } of eligibleRows) {
    const quality = qualityByAccountId.get(row.id)
    if (quality) {
      row.quality_score = quality.quality_score
      row.quality_state = quality.quality_state
      row.quality_ewma_first_token_ms = quality.quality_ewma_first_token_ms
    }
    const account = openAIAccountSecretFromRow(row, groupAccess, systemAccountId, row, { accountAuthorizationsByResourceId, proxyProfilesById, supportedModelsByAccountId, accountAccess })
    if (account) {
      accounts.push(account)
    }
  }

  return orderOpenAIAccountsForDispatch(accounts)
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
  account_expires_at: string | null
  quality_score?: number | null
  quality_state?: string | null
  quality_ewma_first_token_ms?: number | null
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
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  const apiKey = row.type === 'oauth'
    ? typeof credentials.access_token === 'string' ? credentials.access_token : ''
    : typeof credentials.api_key === 'string' ? credentials.api_key : ''
  if (!apiKey) {
    return undefined
  }
  const proxyProfile = resolveOpenAIAccountProxyUrl(row.proxy_profile_id, options.proxyProfilesById)
  const isAccountAuthorized = accountAccess.accountAccessType === 'account_authorized'
  const isLocalAccountAuthorized = isAccountAuthorized && Boolean(groupAccount?.account_authorization_id)
  const runtimeStatus = isLocalAccountAuthorized
    ? openAIGroupAccountRuntimeStatus(groupAccount?.local_status, groupAccount?.local_cooldown_until)
    : row.status
  const runtimeCooldownUntil = isLocalAccountAuthorized
    ? groupAccount?.local_cooldown_until ?? undefined
    : row.cooldown_until ?? undefined
  const runtimeLastErrorMessage = isLocalAccountAuthorized
    ? groupAccount?.local_last_error_message ?? undefined
    : row.last_error_message ?? undefined
  const runtimeStreamFailureCount = isLocalAccountAuthorized
    ? Math.max(0, Number(groupAccount?.local_stream_failure_count ?? 0))
    : Math.max(0, Number(row.stream_failure_count ?? 0))
  const runtimeStreamFailureWindowStartedAt = isLocalAccountAuthorized
    ? groupAccount?.local_stream_failure_window_started_at ?? undefined
    : row.stream_failure_window_started_at ?? undefined
  const localSuperPriorityEnabled = isLocalAccountAuthorized && groupAccount?.local_super_priority_enabled === 1
  const localFallbackEnabled = isLocalAccountAuthorized && groupAccount?.local_fallback_enabled === 1
  return {
    id: row.id,
    providerCode: 'openai',
    systemAccountId: row.system_account_id,
    accountOwnerSystemAccountId: row.system_account_id,
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
    type: row.type,
    status: runtimeStatus,
    concurrencyLimit: Number(row.concurrency_limit ?? 1),
    priority: isLocalAccountAuthorized ? 0 : Number(row.priority ?? 0),
    superPriorityEnabled: runtimeStatus === 'active' && (isLocalAccountAuthorized ? localSuperPriorityEnabled : row.super_priority_enabled === 1),
    fallbackEnabled: runtimeStatus === 'active' && (isLocalAccountAuthorized ? localFallbackEnabled : row.fallback_enabled === 1),
    supportedModels: [...(options.supportedModelsByAccountId?.get(row.id) ?? [])],
    qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
    qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
    qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
    baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : 'https://api.openai.com/v1',
    apiKey,
    refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : undefined,
    clientId: typeof credentials.client_id === 'string' ? credentials.client_id : undefined,
    proxyProfileId: row.proxy_profile_id ?? undefined,
    proxyUrl: proxyProfile.proxyUrl,
    proxyProfileUnavailable: proxyProfile.unavailable,
    proxyProfileErrorMessage: proxyProfile.errorMessage,
    passthroughEnabled: row.passthrough_enabled === 1,
    errorPolicyId: row.error_policy_id ?? undefined,
    cooldownUntil: runtimeCooldownUntil,
    lastErrorMessage: runtimeLastErrorMessage,
    streamFailureCount: runtimeStreamFailureCount,
    streamFailureWindowStartedAt: runtimeStreamFailureWindowStartedAt,
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

function openAIGroupAccountRuntimeStatus(
  localStatus: AccountStatus | null | undefined,
  localCooldownUntil: string | null | undefined,
  now = nowIso()
): AccountStatus {
  if ((localStatus === 'temporary_unavailable' || localStatus === 'rate_limited') && localCooldownUntil && localCooldownUntil <= now) {
    return 'active'
  }
  return localStatus ?? 'active'
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
    const localStatus = openAIGroupAccountRuntimeStatus(groupAccount.local_status, groupAccount.local_cooldown_until, now)
    if (includeUnavailable) {
      return localStatus === 'active' || localStatus === 'rate_limited' || localStatus === 'temporary_unavailable'
    }
    return localStatus === 'active'
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
  const accountAccess = resolveOpenAIAccountAccess(row.id, row.system_account_id, systemAccountId, groupAccess, groupAccount?.account_authorization_id ?? undefined, options.accountAuthorizationsByResourceId)
  if (!accountAccess) {
    return undefined
  }
  if (options.enforceSchedulableAuthorization !== false && !canScheduleAuthorizedAccount({
    accountId: row.id,
    accountAccessType: accountAccess.accountAccessType,
    authorizationId: accountAccess.accountAuthorizationId,
    systemAccountId,
    accountAuthorizationsByResourceId: options.accountAuthorizationsByResourceId
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
  const authorizedAccountIds = rows
    .filter((row) => row.system_account_id !== systemAccountId)
    .map((row) => row.id)
  if (!authorizedAccountIds.length) return undefined
  return activeResourceAuthorizationsByResourceIds('account', authorizedAccountIds, systemAccountId)
}

function loadProxyProfilesForSelection(rows: OpenAIGroupAccountSelectionRow[]): Map<string, ProxyProfileUrlResolution> | undefined {
  const proxyProfileIds = rows
    .map((row) => row.proxy_profile_id ?? '')
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
    throw error
  }
}

function resolveOpenAIAccountAccess(
  accountId: string,
  accountOwnerSystemAccountId: string,
  callerSystemAccountId: string,
  groupAccess: GroupUsageAccessMetadata,
  boundAccountAuthorizationId?: string,
  accountAuthorizationsByResourceId?: Map<string, ResourceAuthorizationRow>
): OpenAIAccountAccess | undefined {
  if (accountOwnerSystemAccountId === callerSystemAccountId) {
    return { accountAccessType: 'owner' }
  }
  if (groupAccess.groupAccessType === 'authorized') {
    return accountOwnerSystemAccountId === groupAccess.groupOwnerSystemAccountId
      ? { accountAccessType: 'group_authorized' }
      : undefined
  }
  const authorization = accountAuthorizationsByResourceId
    ? accountAuthorizationsByResourceId.get(accountId)
    : activeResourceAuthorization('account', accountId, callerSystemAccountId)
  if (boundAccountAuthorizationId && authorization?.id !== boundAccountAuthorizationId) {
    return undefined
  }
  return authorization
    ? {
        accountAccessType: 'account_authorized',
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
  accountAuthorizationsByResourceId?: Map<string, ResourceAuthorizationRow>
}): boolean {
  if (input.accountAccessType === 'owner' || input.accountAccessType === 'group_authorized') {
    return true
  }
  if (!input.authorizationId) {
    return false
  }
  const authorization = input.accountAuthorizationsByResourceId
    ? input.accountAuthorizationsByResourceId.get(input.accountId)
    : activeResourceAuthorization('account', input.accountId, input.systemAccountId)
  return authorization?.id === input.authorizationId
}
