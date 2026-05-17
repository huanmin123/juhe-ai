import type { AccountStatus, AccountType, ResourceAuthorizationSourceType } from '../domain/types.js'
import { buildSystemAccountScopeClause, currentSystemAccountId } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getDatabase, getRecordDatabase, nowIso } from './database.js'
import { ProxyProfileUnavailableError, resolveProxyUrlForProfile } from './proxy.repository.js'
import { getSettings } from './settings.repository.js'
import { refreshGroupAccountStatsCache } from './usage-stats.repository.js'

export interface OpenAIAccountSecret {
  id: string
  systemAccountId: string
  accountOwnerSystemAccountId: string
  groupOwnerSystemAccountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
  name: string
  type: AccountType
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  qualityScore?: number
  qualityState?: string
  qualityEwmaFirstTokenMs?: number
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
  expiresAt?: string
  credentials: Record<string, unknown>
}

export interface GroupUsageAccessMetadata {
  groupOwnerSystemAccountId: string
  groupAccessType: 'owner' | 'authorized'
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
}

interface GroupAccountRow {
  account_id: string
  account_authorization_id?: string | null
  local_status?: AccountStatus | null
  local_cooldown_until?: string | null
  local_last_error_message?: string | null
  local_super_priority_enabled?: number | null
  local_fallback_enabled?: number | null
}

type ResourceAuthorizationRow = {
  id: string
  effective_source_type: ResourceAuthorizationSourceType | null
  effective_source_team_id: string | null
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
  disableExpiredAccountsForSelection(groupAccess.groupOwnerSystemAccountId)
  const forceAvailability = options.ignoreAvailability === true
  const groupAccount = getDatabase()
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.account_authorization_id,
        group_accounts.local_status, group_accounts.local_cooldown_until, group_accounts.local_last_error_message,
        group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled
      FROM group_accounts
      WHERE group_accounts.group_id = ?
        AND group_accounts.account_id = ?
        AND group_accounts.enabled = 1
      LIMIT 1
    `)
    .get(groupId, accountId) as unknown as GroupAccountRow | undefined
  if (!groupAccount) {
    return undefined
  }

  const availabilityClause = forceAvailability
    ? `
          AND status <> 'disabled'
    `
    : options.includeUnavailable
    ? `
          AND schedulable = 1
          AND status IN ('active', 'rate_limited', 'temporary_unavailable')
    `
    : `
          AND schedulable = 1
          AND status = 'active'
          AND (cooldown_until IS NULL OR cooldown_until <= ?)
    `
  const params = forceAvailability || options.includeUnavailable ? [accountId, now] : [accountId, now, now]
  const row = getDatabase()
    .prepare(`
      SELECT id, system_account_id, name, type, status, concurrency_limit, priority, super_priority_enabled, fallback_enabled, credentials_encrypted, proxy_profile_id, passthrough_enabled, error_policy_id, cooldown_until, last_error_message, stream_failure_count, stream_failure_window_started_at,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM accounts
      WHERE id = ?
        AND provider_code = 'openai'
        AND type IN ('api_key', 'oauth')
        AND (account_expires_at IS NULL OR account_expires_at > ?)
        ${availabilityClause}
    `)
    .get(...params) as unknown as OpenAIAccountRow | undefined
  if (!row) {
    return undefined
  }
  return openAIAccountSecretFromRow(row, groupAccess, systemAccountId, groupAccount, {
    enforceSchedulableAuthorization: !forceAvailability && !options.includeUnavailable
  })
}

export function resolveGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const group = groupOwnerAndProvider(groupId)
  if (!group) return undefined
  if (group.systemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId: group.systemAccountId, groupAccessType: 'owner' }
  }
  const authorization = activeResourceAuthorization('group', groupId, systemAccountId)
  if (!authorization) return undefined
  return {
    groupOwnerSystemAccountId: group.systemAccountId,
    groupAccessType: 'authorized',
    groupAuthorizationId: authorization.id,
    groupAuthorizationSourceType: authorization.effective_source_type ?? undefined,
    groupAuthorizationSourceTeamId: authorization.effective_source_team_id ?? undefined
  }
}

export function listOpenAIAccountsForGroup(groupId: string, systemAccountId = currentSystemAccountId()): OpenAIAccountSecret[] {
  const database = getDatabase()
  const now = nowIso()
  const qualityFreshAfter = qualityFreshAfterIso()
  const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return []
  }
  disableExpiredAccountsForSelection(groupAccess.groupOwnerSystemAccountId)
  const groupAccountRows = database
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.account_authorization_id,
        group_accounts.local_status, group_accounts.local_cooldown_until, group_accounts.local_last_error_message,
        group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled
      FROM group_accounts
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      WHERE group_accounts.group_id = ? AND group_accounts.enabled = 1
      ORDER BY
        CASE WHEN group_accounts.account_authorization_id IS NOT NULL THEN group_accounts.local_fallback_enabled ELSE accounts.fallback_enabled END ASC,
        CASE WHEN group_accounts.account_authorization_id IS NOT NULL THEN group_accounts.local_super_priority_enabled ELSE accounts.super_priority_enabled END DESC,
        CASE WHEN group_accounts.account_authorization_id IS NOT NULL THEN 0 ELSE accounts.priority END ASC,
        group_accounts.weight DESC,
        group_accounts.created_at ASC,
        group_accounts.account_id ASC
    `)
    .all(groupId) as unknown as GroupAccountRow[]
  const qualityByAccountId = loadFreshAccountQualityRows(groupAccountRows.map((row) => row.account_id), qualityFreshAfter)

  const accounts: OpenAIAccountSecret[] = []
  for (const groupAccount of groupAccountRows) {
    const row = database
      .prepare(`
        SELECT accounts.id, accounts.system_account_id, accounts.name, accounts.type, accounts.status, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
          accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.passthrough_enabled, accounts.error_policy_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
          NULL AS quality_score,
          NULL AS quality_state,
          NULL AS quality_ewma_first_token_ms
        FROM accounts
        WHERE accounts.id = ?
          AND accounts.provider_code = 'openai'
          AND type IN ('api_key', 'oauth')
          AND schedulable = 1
          AND (account_expires_at IS NULL OR account_expires_at > ?)
          AND status = 'active'
          AND (cooldown_until IS NULL OR cooldown_until <= ?)
      `)
      .get(groupAccount.account_id, now, now) as unknown as OpenAIAccountRow | undefined
    if (!row) {
      continue
    }
    const quality = qualityByAccountId.get(row.id)
    if (quality) {
      row.quality_score = quality.quality_score
      row.quality_state = quality.quality_state
      row.quality_ewma_first_token_ms = quality.quality_ewma_first_token_ms
    }
    const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
    const apiKey = row.type === 'oauth'
      ? typeof credentials.access_token === 'string' ? credentials.access_token : ''
      : typeof credentials.api_key === 'string' ? credentials.api_key : ''
    if (!apiKey) {
      continue
    }
    if (!isGroupAccountLocallyAvailable(groupAccount, now)) {
      continue
    }
    const accountAccess = resolveOpenAIAccountAccess(row.id, row.system_account_id, systemAccountId, groupAccess, groupAccount.account_authorization_id ?? undefined)
    if (!accountAccess) {
      continue
    }
    if (!canScheduleAuthorizedAccount({
      accountId: row.id,
      accountAccessType: accountAccess.accountAccessType,
      authorizationId: accountAccess.accountAuthorizationId,
      systemAccountId
    })) {
      continue
    }
    const account = openAIAccountSecretFromRow(row, groupAccess, systemAccountId, groupAccount)
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
  quality_score?: number | null
  quality_state?: string | null
  quality_ewma_first_token_ms?: number | null
}

function openAIAccountSecretFromRow(
  row: OpenAIAccountRow,
  groupAccess: GroupUsageAccessMetadata,
  systemAccountId: string,
  groupAccount?: GroupAccountRow,
  options: { enforceSchedulableAuthorization?: boolean } = {}
): OpenAIAccountSecret | undefined {
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  const apiKey = row.type === 'oauth'
    ? typeof credentials.access_token === 'string' ? credentials.access_token : ''
    : typeof credentials.api_key === 'string' ? credentials.api_key : ''
  if (!apiKey) {
    return undefined
  }
  const accountAccess = resolveOpenAIAccountAccess(row.id, row.system_account_id, systemAccountId, groupAccess, groupAccount?.account_authorization_id ?? undefined)
  if (!accountAccess) {
    return undefined
  }
  if (options.enforceSchedulableAuthorization !== false && !canScheduleAuthorizedAccount({
    accountId: row.id,
    accountAccessType: accountAccess.accountAccessType,
    authorizationId: accountAccess.accountAuthorizationId,
    systemAccountId
  })) {
    return undefined
  }
  const proxyProfile = resolveOpenAIAccountProxyUrl(row.proxy_profile_id)
  const isAccountAuthorized = accountAccess.accountAccessType === 'account_authorized'
  const isLocalAccountAuthorized = isAccountAuthorized && Boolean(groupAccount?.account_authorization_id)
  const localSuperPriorityEnabled = isLocalAccountAuthorized && groupAccount?.local_super_priority_enabled === 1
  const localFallbackEnabled = isLocalAccountAuthorized && groupAccount?.local_fallback_enabled === 1
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    accountOwnerSystemAccountId: row.system_account_id,
    groupOwnerSystemAccountId: groupAccess.groupOwnerSystemAccountId,
    accountAccessType: accountAccess.accountAccessType,
    groupAccessType: groupAccess.groupAccessType,
    accountAuthorizationId: accountAccess.accountAuthorizationId,
    accountAuthorizationSourceType: accountAccess.accountAuthorizationSourceType,
    accountAuthorizationSourceTeamId: accountAccess.accountAuthorizationSourceTeamId,
    groupAuthorizationId: groupAccess.groupAuthorizationId,
    groupAuthorizationSourceType: groupAccess.groupAuthorizationSourceType,
    groupAuthorizationSourceTeamId: groupAccess.groupAuthorizationSourceTeamId,
    name: row.name,
    type: row.type,
    status: row.status,
    concurrencyLimit: Number(row.concurrency_limit ?? 1),
    priority: isLocalAccountAuthorized ? 0 : Number(row.priority ?? 0),
    superPriorityEnabled: row.status === 'active' && (isLocalAccountAuthorized ? localSuperPriorityEnabled : row.super_priority_enabled === 1),
    fallbackEnabled: row.status === 'active' && (isLocalAccountAuthorized ? localFallbackEnabled : row.fallback_enabled === 1),
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
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
    streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
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

function isGroupAccountLocallyAvailable(groupAccount: GroupAccountRow, now: string): boolean {
  const localStatus = groupAccount.local_status ?? 'active'
  if (localStatus === 'active') {
    return true
  }
  if (localStatus === 'temporary_unavailable' && groupAccount.local_cooldown_until) {
    return groupAccount.local_cooldown_until <= now
  }
  return false
}

function qualityFreshAfterIso(): string {
  return new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString()
}

function loadFreshAccountQualityRows(accountIds: string[], freshAfter: string): Map<string, Pick<OpenAIAccountRow, 'quality_score' | 'quality_state' | 'quality_ewma_first_token_ms'>> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows = getRecordDatabase()
    .prepare(`
      SELECT account_id, quality_score, quality_state, ewma_first_token_ms AS quality_ewma_first_token_ms
      FROM account_quality_scores
      WHERE account_id IN (${ids.map(() => '?').join(',')})
        AND last_sample_at >= ?
    `)
    .all(...ids, freshAfter) as unknown as Array<{
      account_id: string
      quality_score: number | null
      quality_state: string | null
      quality_ewma_first_token_ms: number | null
    }>
  return new Map(rows.map((row) => [row.account_id, row]))
}

function resolveOpenAIAccountProxyUrl(proxyProfileId: string | null): { proxyUrl?: string; unavailable?: boolean; errorMessage?: string } {
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
  boundAccountAuthorizationId?: string
): { accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'; accountAuthorizationId?: string; accountAuthorizationSourceType?: ResourceAuthorizationSourceType; accountAuthorizationSourceTeamId?: string } | undefined {
  if (accountOwnerSystemAccountId === callerSystemAccountId) {
    return { accountAccessType: 'owner' }
  }
  if (groupAccess.groupAccessType === 'authorized') {
    return accountOwnerSystemAccountId === groupAccess.groupOwnerSystemAccountId
      ? { accountAccessType: 'group_authorized' }
      : undefined
  }
  const authorization = activeResourceAuthorization('account', accountId, callerSystemAccountId)
  if (boundAccountAuthorizationId && authorization?.id !== boundAccountAuthorizationId) {
    return undefined
  }
  return authorization
    ? {
        accountAccessType: 'account_authorized',
        accountAuthorizationId: authorization.id,
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
}): boolean {
  if (input.accountAccessType === 'owner' || input.accountAccessType === 'group_authorized') {
    return true
  }
  if (!input.authorizationId) {
    return false
  }
  const authorization = activeResourceAuthorization('account', input.accountId, input.systemAccountId)
  return authorization?.id === input.authorizationId
}

function groupOwnerAndProvider(groupId: string): { systemAccountId: string } | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id ? { systemAccountId: row.system_account_id } : undefined
}

function activeResourceAuthorization(resourceType: 'account' | 'group', resourceId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getDatabase()
    .prepare("SELECT id, effective_source_type, effective_source_team_id FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1")
    .get(resourceType, resourceId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
}

function disableExpiredAccountsForSelection(systemAccountId: string): void {
  const scope = buildSystemAccountScopeClause({ systemAccountId, role: 'user' })
  const now = nowIso()
  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = NULL,
          last_error_message = ?,
          updated_at = ?
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND (
          status <> 'disabled'
          OR schedulable <> 0
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NULL
        )${scope.clause}
    `)
    .run('账户套餐已过期，已自动停用', now, now, ...scope.params)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsCache()
  }
}
