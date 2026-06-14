import type { AccountClientCompatibility, AccountGroupBindStatus, AccountSummary } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { isGptVendorCode } from '../domain/provider-protocol.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { loadAccountCurrentConcurrencyByIds } from '../shared/account-concurrency.js'
import { userVisibleSystemAccountId, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { accountCredentialsForList, findAccountRowForAccess, hydrateAccountRowsWithRuntimeState, listAccountRowsForAccess, listAccountRowsPageForAccess, loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { normalizeAccountListOptions, type AccountListOptions } from './account-list-options.js'
import {
  isAccountAvailabilityScheduleAllowed,
  parseAccountAvailabilityScheduleJson
} from './account-availability-schedule.js'
import { authorizationRuntimeBlockingStatus, disableExpiredAccounts } from './account-runtime-status.js'
import { loadAccountTagsByAccountIds } from './account-tags.repository.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { accountSystemAccountId, activeAccountAuthorization, activeResourceAuthorizationById, canManageResourceOwner, sanitizeAuthorizationSourcesForViewer, usageScope } from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountListRow, ResourceAuthorizationRow } from './repository-row-types.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopes } from './usage-summary-loaders.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from '../modules/gateway/quota/request-quota-checker.js'
import { optionalString } from './value-utils.js'
import { loadAccountApiKeyRuntimeSummariesByAccountIds } from './account-api-key-runtime-state.repository.js'

export interface AccountListResult {
  items: AccountSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface AccountSummaryBuildOptions {
  includeCredentials?: boolean
}

export function listAccounts(access?: AccessScope, options?: AccountListOptions): AccountSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const rows = hydrateAccountRowsWithRuntimeState(listAccountRowsForAccess(access, listOptions), { includeCredentials: true })
  return accountSummariesFromRows(rows, access, viewerSystemAccountId)
}

export function listAccountsPage(access?: AccessScope, options?: AccountListOptions): AccountListResult {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const databasePage = listAccountRowsPageForAccess(access, listOptions, { includeCredentials: false })
  const page = {
    rows: hydrateAccountRowsWithRuntimeState(databasePage.rows, { includeCredentials: false }),
    total: databasePage.total
  }
  const rows = page.rows
  return {
    items: accountSummariesFromRows(rows, access, viewerSystemAccountId, { includeCredentials: false }),
    total: page.total,
    hasMore: page.total > listOptions.page * listOptions.pageSize,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export function findAccountSummary(accountId: string, access?: AccessScope): AccountSummary | undefined {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions({ page: 1, pageSize: 1 })
  const row = findAccountRowForAccess(access, accountId, listOptions)
  if (!row) return undefined
  const hydratedRows = hydrateAccountRowsWithRuntimeState([row], { includeCredentials: true })
  return accountSummariesFromRows(hydratedRows, access, viewerSystemAccountId)[0]
}

function accountSummariesFromRows(
  rows: AccountListRow[],
  access: AccessScope | undefined,
  viewerSystemAccountId: string | undefined,
  options: AccountSummaryBuildOptions = {}
): AccountSummary[] {
  const includeCredentials = options.includeCredentials ?? true
  const timezone = usageStatsTimezone()
  const accountIds = rows.map((row) => row.id)
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(accountIds)
  const tagsByAccount = loadAccountTagsByAccountIds(accountIds)
  const accountUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const usageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes)
  const todayUsageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes, todayDateKey(timezone))
  const authorizationStatsByAccount = loadResourceAuthorizationStatsByResourceIds('account', accountIds)
  const authorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const usageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes)
  const todayUsageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes, todayDateKey(timezone))
  const quotaExceededByAuthorization = loadAuthorizationQuotaExceededByAuthorizationId(rows)
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const oauthUsageByAccount = loadOpenAICodexUsageSnapshotsByAccountIds(rows.map((row) => accountResourceFactAccountId(row)))
  const apiKeyRuntimeByAccount = loadAccountApiKeyRuntimeSummariesByAccountIds(accountIds)
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const accountNames = includeSystemAccountFields(access) || hasAuthorizedRows
    ? loadSystemAccountNameMapByIds(rows.flatMap((row) => [
        row.system_account_id,
        row.authorization_resource_owner_system_account_id ?? '',
        row.authorization_instance_owner_system_account_id ?? ''
      ]))
    : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const usage = isAuthorizedView && row.authorization_id
      ? usageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : usageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const authorizationStats = authorizationStatsByAccount.get(row.id) ?? { authorizationCount: 0, authorizationTeamCount: 0 }
    const groupBindingSystemAccountId = row.system_account_id
    const groupBinding = groupBindingSystemAccountId
      ? accountGroupBindingFromRow(row, groupBindingSystemAccountId) ?? accountGroupBinding(row.id, groupBindingSystemAccountId)
      : undefined
    const currentNow = nowIso()
    const effectiveAuthorizedStatus = isAuthorizedView
      ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
      : row.status
    const effectiveAuthorizedSchedulable = isAuthorizedView
      ? Boolean(groupBinding && groupBinding.groupBindStatus === 'bound')
        && authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) === undefined
        && Boolean(accountResourceFactAccountId(row))
        && isAuthorizedSourceAccountAvailableForDispatch(row, currentNow)
        && row.status === 'active'
        && row.schedulable === 1
        && isAccountAvailabilityScheduleAllowed(row.availability_schedule_json, new Date(Date.parse(currentNow)))
        && !isLaterIso(row.cooldown_until ?? undefined, currentNow)
      : row.schedulable === 1
    const displayOwnerSystemAccountId = isAuthorizedView
      ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
      : row.system_account_id
    const resourceProviderCode = accountResourceProviderCode(row)
    const resourceType = accountResourceType(row)
    const dispatchPriority = isAuthorizedView ? Number(row.bound_group_local_priority ?? row.priority ?? 0) : row.priority
    const dispatchSuperPriorityEnabled = isAuthorizedView ? row.bound_group_local_super_priority_enabled === 1 : row.super_priority_enabled === 1
    const dispatchFallbackEnabled = isAuthorizedView ? row.bound_group_local_fallback_enabled === 1 : row.fallback_enabled === 1
    const clientCompatibility = accountResourceClientCompatibility(row)
    const availabilitySchedule = parseAccountAvailabilityScheduleJson(row.availability_schedule_json)
    const currentNowMs = Date.parse(currentNow)
    const currentNowDate = Number.isFinite(currentNowMs) ? new Date(currentNowMs) : new Date()
    const availabilityScheduleActive = isAccountAvailabilityScheduleAllowed(row.availability_schedule_json, currentNowDate)
    const sourceAvailabilityScheduleActive = isAuthorizedView
      ? isAccountAvailabilityScheduleAllowed(row.source_availability_schedule_json, currentNowDate)
      : undefined
    const authorizationSources = row.authorization_id ? sourcesByAuthorization.get(row.authorization_id) ?? [] : []
    const authorizationQuotaExceeded = row.authorization_id ? quotaExceededByAuthorization.get(row.authorization_id) : undefined
    return accountSummaryWithEffectiveAvailability({
      id: row.id,
      systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: displayOwnerSystemAccountId,
      ownerSystemAccountName: accountNames.get(displayOwnerSystemAccountId),
      providerCode: resourceProviderCode,
      providerProtocolProfileId: accountResourceProviderProtocolProfileId(row),
      protocolCode: accountResourceProtocolCode(row),
      protocolVersion: accountResourceProtocolVersion(row),
      name: row.name,
      notes: isAuthorizedView ? undefined : row.notes ?? undefined,
      type: resourceType,
      credentials: accountCredentialsForList(row, includeCredentials),
      status: effectiveAuthorizedStatus,
      concurrencyLimit: accountResourceConcurrencyLimit(row),
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: dispatchPriority,
      superPriorityEnabled: dispatchSuperPriorityEnabled,
      fallbackEnabled: dispatchFallbackEnabled,
      clientCompatibility,
      supportedModels: [...(row.supported_models ?? [])],
      modelMappings: [...(row.model_mappings ?? [])],
      tags: tagsByAccount.get(row.id) ?? [],
      lastSuccessfulTestModel: optionalString(row.last_successful_test_model),
      qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
      qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
      qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
      qualityRecentAvgFirstTokenMs: typeof row.quality_recent_avg_first_token_ms === 'number' ? row.quality_recent_avg_first_token_ms : undefined,
      qualityRecentRequestCount: typeof row.quality_recent_request_count === 'number' ? row.quality_recent_request_count : undefined,
      qualityRecentErrorCount: typeof row.quality_recent_error_count === 'number' ? row.quality_recent_error_count : undefined,
      qualityRecentSuccessRate: typeof row.quality_recent_success_rate === 'number' ? row.quality_recent_success_rate : undefined,
      qualityLastErrorAt: row.quality_last_error_at ?? undefined,
      qualityLastErrorMessage: row.quality_last_error_message ?? undefined,
      qualityUpdatedAt: row.quality_updated_at ?? undefined,
      proxyProfileId: accountResourceProxyProfileId(row) ?? undefined,
      schedulable: isAuthorizedView ? effectiveAuthorizedSchedulable && authorizationQuotaExceeded !== true : effectiveAuthorizedSchedulable,
      availabilitySchedule,
      availabilityScheduleActive,
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: isAuthorizedView ? undefined : row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: isAuthorizedView ? 0 : Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: isAuthorizedView ? undefined : row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: isAuthorizedView ? undefined : row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: isAuthorizedView ? undefined : optionalNumber(row.cooldown_retest_last_status_code),
      apiKeyRuntime: apiKeyRuntimeByAccount.get(row.id),
      streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
      streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
      lastUsedAt: isAuthorizedView ? usage.lastUsedAt : row.last_used_at ?? undefined,
      todayUsage,
      usage,
      oauthUsage: isGptVendorCode(resourceProviderCode) && resourceType === 'oauth' ? oauthUsageByAccount.get(accountResourceFactAccountId(row)) : undefined,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      authorizationInstanceSourceAccountId: isAuthorizedView ? row.authorization_instance_source_account_id ?? undefined : undefined,
      authorizationInstanceOwnerSystemAccountId: isAuthorizedView ? row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined : undefined,
      authorizationInstanceSourceAccountStatus: isAuthorizedView ? row.source_status ?? undefined : undefined,
      authorizationInstanceSourceAccountSchedulable: isAuthorizedView && typeof row.source_schedulable === 'number' ? row.source_schedulable === 1 : undefined,
      authorizationInstanceSourceAccountAvailabilitySchedule: isAuthorizedView ? parseAccountAvailabilityScheduleJson(row.source_availability_schedule_json) : undefined,
      authorizationInstanceSourceAccountScheduleActive: sourceAvailabilityScheduleActive,
      authorizationInstanceSourceAccountExpiresAt: isAuthorizedView ? row.source_account_expires_at ?? undefined : undefined,
      authorizationInstanceSourceAccountCooldownUntil: isAuthorizedView ? row.source_cooldown_until ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorCode: isAuthorizedView ? row.source_last_error_code ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorMessage: isAuthorizedView ? row.source_last_error_message ?? undefined : undefined,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      bindingSystemAccountId: isAuthorizedView && groupBinding ? groupBindingSystemAccountId : undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      authorizationQuotaExceeded,
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(authorizationSources, isAuthorizedView) : undefined,
      permissions: isAuthorizedView ? authorizedAccountPermissions(hasActiveManualAuthorizationSource(authorizationSources)) : ownerPermissions(),
      authorizationUsageAvailable: !isAuthorizedView && authorizationStats.authorizationCount > 0 && canManageResourceOwner(row.system_account_id, access),
      authorizationCount: isAuthorizedView ? 0 : authorizationStats.authorizationCount,
      authorizationTeamCount: isAuthorizedView ? 0 : authorizationStats.authorizationTeamCount
    })
  })
}

export function accountGroupBinding(accountId: string, systemAccountId: string): { groupId: string; groupName: string; groupBindStatus: AccountGroupBindStatus } | undefined {
  const row = getBusinessDatabase()
    .prepare(`
      SELECT
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        groups.name AS group_name
      FROM group_accounts
      INNER JOIN groups ON groups.id = group_accounts.group_id
      WHERE group_accounts.account_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    `)
    .get(accountId, systemAccountId) as unknown as { group_id?: string; group_name?: string; account_authorization_id?: string | null } | undefined
  if (!row?.group_id) {
    return undefined
  }
  const ownerId = accountSystemAccountId(accountId)
  const authorization = ownerId && ownerId !== systemAccountId ? activeAccountAuthorization(accountId, systemAccountId) : authorizationInstanceRuntimeAuthorization(accountId, systemAccountId)
  return {
    groupId: row.group_id,
    groupName: row.group_name ?? '',
    groupBindStatus: row.account_authorization_id && authorization?.id !== row.account_authorization_id ? 'authorization_unavailable' : 'bound'
  }
}

export function accountGroupBindingFromRow(row: AccountListRow, systemAccountId?: string): { groupId: string; groupName: string; groupBindStatus: AccountGroupBindStatus } | undefined {
  if (!row.bound_group_id || row.binding_system_account_id !== systemAccountId) {
    return undefined
  }
  const accountOwnerId = row.system_account_id
  const activeAuthorizationId = accountOwnerId && systemAccountId && row.authorization_instance_authorization_id
    ? row.authorization_id ?? undefined
    : accountOwnerId && systemAccountId && accountOwnerId !== systemAccountId
    ? row.authorization_id ?? undefined
    : undefined
  return {
    groupId: row.bound_group_id,
    groupName: row.bound_group_name ?? '',
    groupBindStatus: row.bound_group_account_authorization_id && activeAuthorizationId !== row.bound_group_account_authorization_id ? 'authorization_unavailable' : 'bound'
  }
}

export function accountResourceFactAccountId(row: AccountListRow): string {
  if (row.access_type !== 'authorized') return row.id
  return row.authorization_instance_source_account_id && row.source_provider_code
    ? row.authorization_instance_source_account_id
    : ''
}

export function accountResourceProviderCode(row: AccountListRow): AccountListRow['provider_code'] {
  return row.access_type === 'authorized' && row.source_provider_code
    ? row.source_provider_code
    : row.provider_code
}

export function accountResourceProviderProtocolProfileId(row: AccountListRow): string {
  return row.access_type === 'authorized' && row.source_provider_protocol_profile_id
    ? row.source_provider_protocol_profile_id
    : row.provider_protocol_profile_id
}

export function accountResourceProtocolCode(row: AccountListRow): string {
  return row.access_type === 'authorized' && row.source_protocol_code
    ? row.source_protocol_code
    : row.protocol_code
}

export function accountResourceProtocolVersion(row: AccountListRow): string {
  return row.access_type === 'authorized' && row.source_protocol_version
    ? row.source_protocol_version
    : row.protocol_version
}

export function accountResourceType(row: AccountListRow): AccountListRow['type'] {
  return row.access_type === 'authorized' && row.source_type
    ? row.source_type
    : row.type
}

export function accountResourceConcurrencyLimit(row: AccountListRow): number {
  if (row.access_type !== 'authorized') return Number(row.concurrency_limit)
  return Number(row.source_concurrency_limit ?? 0)
}

export function accountResourceProxyProfileId(row: AccountListRow): string | null {
  return row.access_type === 'authorized' ? row.source_proxy_profile_id ?? null : row.proxy_profile_id
}

export function accountResourceClientCompatibility(row: AccountListRow): AccountClientCompatibility {
  return normalizeOpenAIAccountClientCompatibility(
    accountResourceProviderCode(row),
    accountResourceType(row),
    row.access_type === 'authorized' ? row.source_client_compatibility : row.client_compatibility,
    'openai_standard',
    { protocolCode: accountResourceProtocolCode(row), protocolVersion: accountResourceProtocolVersion(row) }
  )
}

export function isAuthorizedSourceAccountAvailableForDispatch(row: AccountListRow, now: string): boolean {
  if (row.access_type !== 'authorized') return true
  const nowMs = Date.parse(now)
  const nowDate = Number.isFinite(nowMs) ? new Date(nowMs) : new Date()
  return Boolean(row.source_status)
    && row.source_status === 'active'
    && row.source_schedulable === 1
    && isAccountAvailabilityScheduleAllowed(row.source_availability_schedule_json, nowDate)
    && row.source_last_error_code !== 'account_expired'
    && !isAccountExpired(row.source_account_expires_at ?? undefined, Number.isFinite(nowMs) ? nowMs : undefined)
    && !isLaterIso(row.source_cooldown_until ?? undefined, now)
}

export function loadAuthorizationQuotaExceededByAuthorizationId(rows: AccountListRow[]): Map<string, boolean> {
  const now = new Date()
  const output = new Map<string, boolean>()
  const checks: Array<{
    authorizationId: string
    limits: ReturnType<typeof parseRequestQuotaLimitsJson>
    input: RequestQuotaCostInput
  }> = []
  const teamGrantLimitJsonByAuthorizationId = loadTeamAuthorizationGrantLimitJsonByAuthorizationId(rows)
  for (const row of rows) {
    if (!row.authorization_id) continue
    output.set(row.authorization_id, false)
    const limits = parseRequestQuotaLimitsJson(row.authorization_limits_json)
    if (hasEnabledRequestQuotaLimit(limits)) {
      checks.push({
        authorizationId: row.authorization_id,
        limits,
        input: {
          systemAccountId: row.system_account_id,
          scopeType: 'account_authorization',
          scopeId: row.authorization_id,
          now,
          hourlyWindowHours: limits.hourly?.hours
        }
      })
    }
    const teamId = row.authorization_effective_source_team_id
    if (!teamId) continue
    const teamLimits = parseRequestQuotaLimitsJson(teamGrantLimitJsonByAuthorizationId.get(row.authorization_id))
    if (!hasEnabledRequestQuotaLimit(teamLimits)) continue
    checks.push({
      authorizationId: row.authorization_id,
      limits: teamLimits,
      input: {
        systemAccountId: row.system_account_id,
        scopeType: 'account_authorization_team',
        scopeId: `${row.id}:${teamId}`,
        now,
        hourlyWindowHours: teamLimits.hourly?.hours
      }
    })
  }
  if (!checks.length) return output
  const costsByKey = loadRequestQuotaCostsBatch(getStatsDatabase(), checks.map((check) => check.input))
  for (const check of checks) {
    const costs = costsByKey.get(requestQuotaCostKey(check.input))
    if (costs && isRequestQuotaExceeded(check.limits, costs)) {
      output.set(check.authorizationId, true)
    }
  }
  return output
}

function loadTeamAuthorizationGrantLimitJsonByAuthorizationId(rows: AccountListRow[]): Map<string, string | null> {
  const ids = [...new Set(rows
    .filter((row) => row.authorization_id && row.authorization_effective_source_team_id)
    .map((row) => row.authorization_id as string))]
  if (!ids.length) return new Map()
  const output = new Map<string, string | null>()
  const database = getBusinessDatabase()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 900)) {
    const teamRows = database.prepare(`
      SELECT ra.id AS authorization_id, grant_rows.limits_json
      FROM resource_authorizations ra
      INNER JOIN resource_authorization_grants grant_rows
        ON grant_rows.resource_type = ra.resource_type
        AND grant_rows.resource_id = ra.resource_id
        AND grant_rows.grantee_type = 'team'
        AND grant_rows.grantee_team_id = ra.effective_source_team_id
        AND grant_rows.status = 'active'
        AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ?)
      WHERE ra.status = 'active'
        AND (ra.expires_at IS NULL OR ra.expires_at > ?)
        AND ra.effective_source_team_id IS NOT NULL
        AND ra.id IN (${sqlPlaceholders(chunk.length)})
    `).all(now, now, ...chunk) as unknown as Array<{ authorization_id?: string; limits_json?: string | null }>
    for (const row of teamRows) {
      if (row.authorization_id) {
        output.set(row.authorization_id, row.limits_json ?? null)
      }
    }
  }
  return output
}

function authorizationInstanceRuntimeAuthorization(accountId: string, systemAccountId: string, database = getBusinessDatabase()): ResourceAuthorizationRow | undefined {
  const row = database
    .prepare('SELECT authorization_instance_authorization_id FROM accounts WHERE id = ? AND system_account_id = ? AND deleted_at IS NULL LIMIT 1')
    .get(accountId, systemAccountId) as unknown as { authorization_instance_authorization_id?: string | null } | undefined
  return row?.authorization_instance_authorization_id
    ? activeResourceAuthorizationById(row.authorization_instance_authorization_id, systemAccountId)
    : undefined
}

function isAccountExpired(accountExpiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!accountExpiresAt) return false
  const timestamp = Date.parse(accountExpiresAt)
  return Number.isFinite(timestamp) && timestamp <= now
}

function isLaterIso(value?: string, current?: string): boolean {
  if (!value) return false
  if (!current) return true
  const nextTime = Date.parse(value)
  const currentTime = Date.parse(current)
  return Number.isFinite(nextTime) && (!Number.isFinite(currentTime) || nextTime > currentTime)
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
