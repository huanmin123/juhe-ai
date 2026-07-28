import {
  accountEffectiveAvailability,
  type AccountEffectiveAvailabilityInput
} from '../domain/account-effective-availability.js'
import { accountAvailabilityPresentation } from '../domain/account-status-presentation.js'
import type {
  AccountListPermissions,
  AccountListUsageSummary,
  AccountProbeSummary,
  AccountStatus,
  AuthorizationStatus,
  RequestQuotaLimits
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import {
  isRequestQuotaExceeded,
  loadRequestQuotaCostsBatchAsync,
  requestQuotaCostKeyAsync,
  type RequestQuotaCostInput
} from '../modules/gateway/quota/request-quota-checker.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { authorizationRuntimeBlockingStatus } from './account-runtime-status.js'
import {
  emptyAccountManagementListUsage,
  loadAccountManagementListUsageAsync,
  type AccountManagementListUsageScope,
  type AccountManagementListUsageValue
} from './account-management-list-usage.repository.js'
import { todayDateKey, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { chunkValues } from './query-utils.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

interface AccountStatusProjectionRow {
  id: string
  system_account_id: string
  status: AccountStatus
  schedulable: number
  balance_query_enabled: number
  balance_query_next_refresh_at: string | null
  account_expires_at: string | null
  cooldown_until: string | null
  last_error_code: string | null
  last_error_message: string | null
  last_error_trace_id: string | null
  last_health_check_at: string | null
  next_health_check_at: string | null
  last_health_check_status_code: number | null
  last_health_check_error_code: string | null
  last_health_check_error_message: string | null
  last_health_check_trace_id: string | null
  cooldown_retest_last_at: string | null
  cooldown_retest_last_status_code: number | null
  cooldown_retest_failure_count: number
  cooldown_retest_observation_started_at: string | null
  last_used_at: string | null
  last_health_success_at: string | null
  health_check_failure_count: number
  health_check_failure_started_at: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
  authorization_instance_source_account_id: string | null
  authorization_id: string | null
  authorization_status: AuthorizationStatus | null
  authorization_expires_at: string | null
  authorization_limits_json: string | null
  authorization_resource_owner_system_account_id: string | null
  authorization_effective_source_team_id: string | null
  authorization_effective_source_type: 'manual' | 'team' | null
  source_status: AccountStatus | null
  source_schedulable: number | null
  source_account_expires_at: string | null
  source_cooldown_until: string | null
  source_last_error_code: string | null
  source_last_error_message: string | null
  source_last_error_trace_id: string | null
  source_cooldown_retest_last_at: string | null
  source_cooldown_retest_last_status_code: number | null
  source_last_health_check_at: string | null
  source_next_health_check_at: string | null
  source_last_health_check_status_code: number | null
  source_last_health_check_error_code: string | null
  source_last_health_check_error_message: string | null
  source_last_health_check_trace_id: string | null
  binding_system_account_id: string | null
  bound_group_id: string | null
  bound_group_name: string | null
  bound_group_account_authorization_id: string | null
  bound_group_local_priority: number | null
  bound_group_local_super_priority_enabled: number | null
  bound_group_local_fallback_enabled: number | null
}

export interface AccountStatusProjection extends AccountEffectiveAvailabilityInput {
  id: string
  runtimeKey: string
  concurrencyAccountId: string
  authorizationLimits?: RequestQuotaLimits
  sourceAccountProbe?: AccountProbeSummary
  cooldownRetestFailureCount?: number
  cooldownRetestObservationStartedAt?: string
  healthCheckFailureCount?: number
  healthCheckFailureStartedAt?: string
  streamFailureCount?: number
  streamFailureWindowStartedAt?: string
  todayUsage: AccountListUsageSummary
  balanceQueryEnabled?: boolean
  balanceQueryNextRefreshAt?: string
  lastUsedAt?: string
}

export async function listAccountStatusProjectionsAsync(
  access: AccessScope | undefined,
  accountIds: string[]
): Promise<AccountStatusProjection[]> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (ids.length === 0) return []
  if (runtimeConfig.databaseDriver === 'sqlite' && sqliteReadWorkerPoolEnabled()) {
    return loadAccountStatusProjectionBatches(ids, (accountIdsBatch) => requestSqliteReadWorker({
      type: 'list_account_status_snapshots_read_only',
      access,
      accountIds: accountIdsBatch
    }))
  }
  const client = await accountStatusDatabaseClient()
  return loadAccountStatusProjectionBatches(ids, (accountIdsBatch) => (
    listAccountStatusProjectionsDirect(access, accountIdsBatch, client)
  ))
}

export async function listAccountStatusProjectionsReadOnly(
  access: AccessScope | undefined,
  accountIds: string[]
): Promise<AccountStatusProjection[]> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (ids.length === 0) return []
  const client = createSqliteDatabaseClient(getBusinessDatabase())
  return loadAccountStatusProjectionBatches(ids, (accountIdsBatch) => (
    listAccountStatusProjectionsDirect(access, accountIdsBatch, client)
  ))
}

const accountStatusProjectionBatchSize = 500

async function loadAccountStatusProjectionBatches(
  ids: string[],
  load: (ids: string[]) => Promise<AccountStatusProjection[]>
): Promise<AccountStatusProjection[]> {
  const output: AccountStatusProjection[] = []
  for (const batch of chunkValues(ids, accountStatusProjectionBatchSize)) {
    output.push(...await load(batch))
  }
  return output
}

async function listAccountStatusProjectionsDirect(
  access: AccessScope | undefined,
  ids: string[],
  client: DatabaseClient
): Promise<AccountStatusProjection[]> {
  const accounts = businessTable(client, 'accounts')
  const authorizations = businessTable(client, 'resource_authorizations')
  const groupAccounts = businessTable(client, 'group_accounts')
  const groups = businessTable(client, 'groups')
  const systemAccountId = scopedSystemAccountId(access)
  const rows = await client.query<AccountStatusProjectionRow>(`
    SELECT
      accounts.id, accounts.system_account_id, accounts.status, accounts.schedulable,
      accounts.balance_query_enabled, accounts.balance_query_next_refresh_at,
      accounts.account_expires_at, accounts.cooldown_until, accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
      accounts.last_health_check_at, accounts.next_health_check_at, accounts.last_health_check_status_code,
      accounts.last_health_check_error_code, accounts.last_health_check_error_message, accounts.last_health_check_trace_id,
      accounts.cooldown_retest_last_at, accounts.cooldown_retest_last_status_code,
      accounts.cooldown_retest_failure_count, accounts.cooldown_retest_observation_started_at,
      accounts.last_used_at, accounts.last_health_success_at,
      accounts.health_check_failure_count, accounts.health_check_failure_started_at,
      accounts.stream_failure_count, accounts.stream_failure_window_started_at,
      accounts.authorization_instance_source_account_id,
      ra.id AS authorization_id, ra.status AS authorization_status, ra.expires_at AS authorization_expires_at,
      ra.limits_json AS authorization_limits_json,
      ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
      ra.effective_source_team_id AS authorization_effective_source_team_id,
      ra.effective_source_type AS authorization_effective_source_type,
      source_accounts.status AS source_status, source_accounts.schedulable AS source_schedulable,
      source_accounts.account_expires_at AS source_account_expires_at,
      source_accounts.cooldown_until AS source_cooldown_until,
      source_accounts.last_error_code AS source_last_error_code,
      source_accounts.last_error_message AS source_last_error_message,
      source_accounts.last_error_trace_id AS source_last_error_trace_id,
      source_accounts.cooldown_retest_last_at AS source_cooldown_retest_last_at,
      source_accounts.cooldown_retest_last_status_code AS source_cooldown_retest_last_status_code,
      source_accounts.last_health_check_at AS source_last_health_check_at,
      source_accounts.next_health_check_at AS source_next_health_check_at,
      source_accounts.last_health_check_status_code AS source_last_health_check_status_code,
      source_accounts.last_health_check_error_code AS source_last_health_check_error_code,
      source_accounts.last_health_check_error_message AS source_last_health_check_error_message,
      source_accounts.last_health_check_trace_id AS source_last_health_check_trace_id,
      group_bindings.system_account_id AS binding_system_account_id,
      group_bindings.group_id AS bound_group_id, bound_groups.name AS bound_group_name,
      group_bindings.account_authorization_id AS bound_group_account_authorization_id,
      group_bindings.local_priority AS bound_group_local_priority,
      group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
      group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled
    FROM ${accounts} accounts
    LEFT JOIN ${authorizations} ra ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${accounts} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN (
      SELECT account_id, system_account_id, group_id, account_authorization_id,
        local_priority, local_super_priority_enabled, local_fallback_enabled
      FROM (
        SELECT group_accounts.*,
          ROW_NUMBER() OVER (
            PARTITION BY group_accounts.account_id, group_accounts.system_account_id
            ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
          ) AS binding_rank
        FROM ${groupAccounts} group_accounts
        WHERE group_accounts.enabled = 1
      ) ranked_group_bindings
      WHERE binding_rank = 1
    ) group_bindings
      ON group_bindings.account_id = accounts.id
      AND group_bindings.system_account_id = accounts.system_account_id
    LEFT JOIN ${groups} bound_groups ON bound_groups.id = group_bindings.group_id
    WHERE accounts.deleted_at IS NULL
      AND accounts.id IN (${ids.map(() => '?').join(', ')})
      ${systemAccountId ? 'AND accounts.system_account_id = ?' : ''}
      AND (accounts.authorization_instance_authorization_id IS NULL OR ra.status IN ('active', 'paused', 'expired'))
  `, [...ids, ...(systemAccountId ? [systemAccountId] : [])])

  const timezone = await usageStatsTimezoneAsync()
  const todayScopes: AccountManagementListUsageScope[] = []
  const authorizationTotalScopes: AccountManagementListUsageScope[] = []
  for (const row of rows) {
    if (row.authorization_id) {
      const scope: AccountManagementListUsageScope = {
        rowKey: row.id,
        systemAccountId: row.system_account_id,
        scopeType: 'account_authorization',
        scopeId: row.authorization_id
      }
      todayScopes.push(scope)
      authorizationTotalScopes.push(scope)
    } else {
      todayScopes.push({
        rowKey: row.id,
        systemAccountId: row.system_account_id,
        scopeType: 'account',
        scopeId: row.id
      })
    }
  }
  const [todayUsage, authorizationTotal, quotaExceeded] = await Promise.all([
    loadAccountManagementListUsageAsync(todayScopes, todayDateKey(timezone)),
    loadAccountManagementListUsageAsync(authorizationTotalScopes),
    loadAccountStatusAuthorizationQuotaExceededAsync(client, rows)
  ])
  const byId = new Map(rows.map((row) => [row.id, row]))
  return ids.flatMap((id) => {
    const row = byId.get(id)
    if (!row) return []
    const isAuthorized = Boolean(row.authorization_id)
    const groupBinding = accountStatusGroupBinding(row)
    const runtimeKey = isAuthorized && groupBinding && row.authorization_id
      ? `${row.id}:authorized:${row.system_account_id}:${groupBinding.groupId}:${row.authorization_id}`
      : row.id
    const effectiveStatus = isAuthorized
      ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
      : row.status
    const projection: AccountStatusProjection = {
      id: row.id,
      runtimeKey,
      concurrencyAccountId: row.authorization_instance_source_account_id || row.id,
      permissions: accountStatusPermissions(isAuthorized, row.authorization_effective_source_type),
      accessType: isAuthorized ? 'authorized' : 'owner',
      boundGroupId: groupBinding?.groupId,
      groupBindStatus: groupBinding?.groupBindStatus,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: row.authorization_id ? parseRequestQuotaLimitsJson(row.authorization_limits_json) : undefined,
      authorizationQuotaExceeded: row.authorization_id ? quotaExceeded.get(row.authorization_id) : undefined,
      authorizationInstanceSourceAccountId: row.authorization_instance_source_account_id ?? undefined,
      authorizationInstanceSourceAccountStatus: row.source_status ?? undefined,
      authorizationInstanceSourceAccountSchedulable: row.source_schedulable === null ? undefined : row.source_schedulable === 1,
      authorizationInstanceSourceAccountExpiresAt: row.source_account_expires_at ?? undefined,
      authorizationInstanceSourceAccountCooldownUntil: row.source_cooldown_until ?? undefined,
      authorizationInstanceSourceAccountLastErrorCode: row.source_last_error_code ?? undefined,
      authorizationInstanceSourceAccountLastErrorMessage: row.source_last_error_message ?? undefined,
      accountExpiresAt: row.account_expires_at ?? undefined,
      status: effectiveStatus,
      schedulable: row.schedulable === 1,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.authorization_id ? undefined : row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      lastErrorTraceId: row.authorization_id ? undefined : row.last_error_trace_id ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)) || undefined,
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      lastHealthCheckAt: row.last_health_check_at ?? undefined,
      nextHealthCheckAt: row.next_health_check_at ?? undefined,
      lastHealthSuccessAt: row.last_health_success_at ?? undefined,
      healthCheckFailureCount: Math.max(0, Number(row.health_check_failure_count ?? 0)) || undefined,
      healthCheckFailureStartedAt: row.health_check_failure_started_at ?? undefined,
      lastHealthCheckStatusCode: row.last_health_check_status_code ?? undefined,
      lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
      lastHealthCheckErrorMessage: row.last_health_check_error_message ?? undefined,
      lastHealthCheckTraceId: row.last_health_check_trace_id ?? undefined,
      cooldownRetestLastAt: row.authorization_id ? undefined : row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: row.authorization_id ? undefined : row.cooldown_retest_last_status_code ?? undefined,
      streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)) || undefined,
      streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
      balanceQueryEnabled: isAuthorized ? undefined : row.balance_query_enabled === 1,
      balanceQueryNextRefreshAt: isAuthorized ? undefined : row.balance_query_next_refresh_at ?? undefined,
      todayUsage: accountStatusTodayUsage(todayUsage.get(row.id)),
      lastUsedAt: isAuthorized ? authorizationTotal.get(row.id)?.lastUsedAt : row.last_used_at ?? undefined
    }
    projection.sourceAccountProbe = accountStatusSourceAccountProbe(projection, row)
    return [projection]
  })
}

function accountStatusSourceAccountProbe(
  projection: AccountStatusProjection,
  row: AccountStatusProjectionRow,
  now = new Date()
): AccountProbeSummary | undefined {
  const effectiveAvailability = accountEffectiveAvailability(projection, now.getTime())
  if (!effectiveAvailability.status.startsWith('source_')) return undefined
  return accountAvailabilityPresentation({
    ...projection,
    effectiveAvailability,
    authorizationInstanceSourceAccountLastErrorTraceId: row.source_last_error_trace_id ?? undefined,
    authorizationInstanceSourceAccountCooldownRetestLastAt: row.source_cooldown_retest_last_at ?? undefined,
    authorizationInstanceSourceAccountCooldownRetestLastStatusCode: row.source_cooldown_retest_last_status_code ?? undefined,
    authorizationInstanceSourceAccountLastHealthCheckAt: row.source_last_health_check_at ?? undefined,
    authorizationInstanceSourceAccountNextHealthCheckAt: row.source_next_health_check_at ?? undefined,
    authorizationInstanceSourceAccountLastHealthCheckStatusCode: row.source_last_health_check_status_code ?? undefined,
    authorizationInstanceSourceAccountLastHealthCheckErrorCode: row.source_last_health_check_error_code ?? undefined,
    authorizationInstanceSourceAccountLastHealthCheckErrorMessage: row.source_last_health_check_error_message ?? undefined,
    authorizationInstanceSourceAccountLastHealthCheckTraceId: row.source_last_health_check_trace_id ?? undefined
  }, now).probe
}

function accountStatusTodayUsage(
  usage: AccountManagementListUsageValue | undefined
): AccountListUsageSummary {
  const value = usage ?? emptyAccountManagementListUsage()
  return {
    requestCount: value.requestCount,
    totalTokens: value.totalTokens,
    totalCost: value.totalCost
  }
}

async function loadAccountStatusAuthorizationQuotaExceededAsync(
  client: DatabaseClient,
  rows: AccountStatusProjectionRow[]
): Promise<Map<string, boolean>> {
  const output = new Map<string, boolean>()
  const teamLimits = await loadAccountStatusTeamLimitJsonAsync(client, rows)
  const checks: Array<{
    authorizationId: string
    limits: ReturnType<typeof parseRequestQuotaLimitsJson>
    input: RequestQuotaCostInput
  }> = []
  const now = new Date()
  for (const row of rows) {
    if (!row.authorization_id) continue
    output.set(row.authorization_id, false)
    const directLimits = parseRequestQuotaLimitsJson(row.authorization_limits_json)
    if (hasEnabledRequestQuotaLimit(directLimits)) {
      checks.push({
        authorizationId: row.authorization_id,
        limits: directLimits,
        input: {
          systemAccountId: row.system_account_id,
          scopeType: 'account_authorization',
          scopeId: row.authorization_id,
          now,
          hourlyWindowHours: directLimits.hourly?.hours
        }
      })
    }
    if (!row.authorization_effective_source_team_id) continue
    const inheritedLimits = parseRequestQuotaLimitsJson(teamLimits.get(row.authorization_id))
    if (!hasEnabledRequestQuotaLimit(inheritedLimits)) continue
    checks.push({
      authorizationId: row.authorization_id,
      limits: inheritedLimits,
      input: {
        systemAccountId: row.system_account_id,
        scopeType: 'account_authorization_team',
        scopeId: `${row.id}:${row.authorization_effective_source_team_id}`,
        now,
        hourlyWindowHours: inheritedLimits.hourly?.hours
      }
    })
  }
  if (!checks.length) return output
  const costs = await loadRequestQuotaCostsBatchAsync(client, checks.map((check) => check.input))
  for (const check of checks) {
    const value = costs.get(await requestQuotaCostKeyAsync(check.input))
    if (value && isRequestQuotaExceeded(check.limits, value)) {
      output.set(check.authorizationId, true)
    }
  }
  return output
}

async function loadAccountStatusTeamLimitJsonAsync(
  client: DatabaseClient,
  rows: AccountStatusProjectionRow[]
): Promise<Map<string, string | null>> {
  const ids = [...new Set(rows
    .filter((row) => row.authorization_id && row.authorization_effective_source_team_id)
    .map((row) => row.authorization_id as string))]
  const output = new Map<string, string | null>()
  const current = nowIso()
  for (const chunk of chunkValues(ids, 900)) {
    const limitRows = await client.query<{ authorization_id: string; limits_json: string | null }>(`
      SELECT authorizations.id AS authorization_id, grants.limits_json
      FROM ${businessTable(client, 'resource_authorizations')} authorizations
      INNER JOIN ${businessTable(client, 'resource_authorization_grants')} grants
        ON grants.resource_type = authorizations.resource_type
        AND grants.resource_id = authorizations.resource_id
        AND grants.grantee_type = 'team'
        AND grants.grantee_team_id = authorizations.effective_source_team_id
        AND grants.status = 'active'
        AND (grants.expires_at IS NULL OR grants.expires_at > ?)
      WHERE authorizations.status = 'active'
        AND (authorizations.expires_at IS NULL OR authorizations.expires_at > ?)
        AND authorizations.effective_source_team_id IS NOT NULL
        AND authorizations.id IN (${chunk.map(() => '?').join(', ')})
    `, [current, current, ...chunk])
    for (const row of limitRows) output.set(row.authorization_id, row.limits_json)
  }
  return output
}

function accountStatusGroupBinding(row: AccountStatusProjectionRow): {
  groupId: string
  groupBindStatus: 'bound' | 'authorization_unavailable'
} | undefined {
  if (!row.bound_group_id || row.binding_system_account_id !== row.system_account_id) return undefined
  return {
    groupId: row.bound_group_id,
    groupBindStatus: row.bound_group_account_authorization_id && row.bound_group_account_authorization_id !== row.authorization_id
      ? 'authorization_unavailable'
      : 'bound'
  }
}

function accountStatusPermissions(
  authorized: boolean,
  sourceType: AccountStatusProjectionRow['authorization_effective_source_type']
): AccountListPermissions {
  return authorized
    ? {
        canUse: true,
        canEdit: false,
        canDelete: false,
        canReturnAuthorization: sourceType === 'manual',
        canAuthorize: false,
        canViewCredentials: false
      }
    : {
        canUse: true,
        canEdit: true,
        canDelete: true,
        canReturnAuthorization: false,
        canAuthorize: true,
        canViewCredentials: true
      }
}

async function accountStatusDatabaseClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

function businessTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
