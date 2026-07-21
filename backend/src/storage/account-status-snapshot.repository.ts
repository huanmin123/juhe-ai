import type { AccountEffectiveAvailabilityInput } from '../domain/account-effective-availability.js'
import type { AccountStatus, AccountUsageSummary, AuthorizationStatus } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { accountGroupBindingFromRow, loadAuthorizationQuotaExceededByAuthorizationIdAsync } from './account-summary.repository.js'
import { authorizationRuntimeBlockingStatus } from './account-runtime-status.js'
import { loadAccountApiKeyRuntimeSummariesByAccountIdsAsync } from './account-api-key-runtime-state.repository.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopesAsync, loadAuthorizationUsageSummariesForScopesAsync, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'
import { authorizedAccountPermissions, ownerPermissions } from './resource-permissions.js'
import type { AccountListRow } from './repository-row-types.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

interface AccountStatusProjectionRow {
  id: string
  system_account_id: string
  status: AccountStatus
  schedulable: number
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
  last_used_at: string | null
  authorization_instance_source_account_id: string | null
  authorization_id: string | null
  authorization_status: AuthorizationStatus | null
  authorization_expires_at: string | null
  authorization_limits_json: string | null
  authorization_resource_owner_system_account_id: string | null
  authorization_effective_source_team_id: string | null
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
  todayUsage: AccountUsageSummary
  lastUsedAt?: string
}

export async function listAccountStatusProjectionsAsync(
  access: AccessScope | undefined,
  accountIds: string[]
): Promise<AccountStatusProjection[]> {
  const ids = [...new Set(accountIds.filter(Boolean))].slice(0, 100)
  if (ids.length === 0) return []
  if (runtimeConfig.databaseDriver === 'sqlite' && sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_account_status_snapshots_read_only',
      access,
      accountIds: ids
    })
  }
  return listAccountStatusProjectionsDirect(access, ids, await accountStatusDatabaseClient())
}

export async function listAccountStatusProjectionsReadOnly(
  access: AccessScope | undefined,
  accountIds: string[]
): Promise<AccountStatusProjection[]> {
  const ids = [...new Set(accountIds.filter(Boolean))].slice(0, 100)
  if (ids.length === 0) return []
  return listAccountStatusProjectionsDirect(access, ids, createSqliteDatabaseClient(getBusinessDatabase()))
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
      accounts.account_expires_at, accounts.cooldown_until, accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
      accounts.last_health_check_at, accounts.next_health_check_at, accounts.last_health_check_status_code,
      accounts.last_health_check_error_code, accounts.last_health_check_error_message, accounts.last_health_check_trace_id,
      accounts.cooldown_retest_last_at, accounts.cooldown_retest_last_status_code,
      accounts.last_used_at, accounts.authorization_instance_source_account_id,
      ra.id AS authorization_id, ra.status AS authorization_status, ra.expires_at AS authorization_expires_at,
      ra.limits_json AS authorization_limits_json,
      ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
      ra.effective_source_team_id AS authorization_effective_source_team_id,
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
  const ownerScopes: UsageSummaryScopeRequest[] = []
  const authorizationScopes: UsageSummaryScopeRequest[] = []
  for (const row of rows) {
    if (row.authorization_id) {
      authorizationScopes.push({ rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.authorization_id })
    } else {
      ownerScopes.push({ rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.id })
    }
  }
  const [ownerToday, authorizationToday, authorizationTotal, apiKeyRuntime, quotaExceeded] = await Promise.all([
    loadAccountUsageSummariesForScopesAsync(ownerScopes, todayDateKey(timezone)),
    loadAuthorizationUsageSummariesForScopesAsync(authorizationScopes, 'account_authorization', todayDateKey(timezone)),
    loadAuthorizationUsageSummariesForScopesAsync(authorizationScopes, 'account_authorization'),
    loadAccountApiKeyRuntimeSummariesByAccountIdsAsync(rows.map((row) => row.id)),
    loadAuthorizationQuotaExceededByAuthorizationIdAsync(client, rows as unknown as AccountListRow[])
  ])
  const byId = new Map(rows.map((row) => [row.id, row]))
  return ids.flatMap((id) => {
    const row = byId.get(id)
    if (!row) return []
    const isAuthorized = Boolean(row.authorization_id)
    const groupBinding = accountGroupBindingFromRow(row as unknown as AccountListRow, row.system_account_id)
    const runtimeKey = isAuthorized && groupBinding && row.authorization_id
      ? `${row.id}:authorized:${row.system_account_id}:${groupBinding.groupId}:${row.authorization_id}`
      : row.id
    const usage = isAuthorized
      ? authorizationToday.get(row.id) ?? emptyAccountUsageSummary()
      : ownerToday.get(row.id) ?? emptyAccountUsageSummary()
    const effectiveStatus = isAuthorized
      ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
      : row.status
    return [{
      id: row.id,
      runtimeKey,
      concurrencyAccountId: row.authorization_instance_source_account_id || row.id,
      permissions: isAuthorized ? authorizedAccountPermissions(true) : ownerPermissions(),
      accessType: isAuthorized ? 'authorized' : 'owner',
      boundGroupId: groupBinding?.groupId,
      groupBindStatus: groupBinding?.groupBindStatus,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationQuotaExceeded: row.authorization_id ? quotaExceeded.get(row.authorization_id) : undefined,
      authorizationInstanceSourceAccountId: row.authorization_instance_source_account_id ?? undefined,
      authorizationInstanceSourceAccountStatus: row.source_status ?? undefined,
      authorizationInstanceSourceAccountSchedulable: row.source_schedulable === null ? undefined : row.source_schedulable === 1,
      authorizationInstanceSourceAccountExpiresAt: row.source_account_expires_at ?? undefined,
      authorizationInstanceSourceAccountCooldownUntil: row.source_cooldown_until ?? undefined,
      authorizationInstanceSourceAccountLastErrorCode: row.source_last_error_code ?? undefined,
      authorizationInstanceSourceAccountLastErrorMessage: row.source_last_error_message ?? undefined,
      authorizationInstanceSourceAccountLastErrorTraceId: row.source_last_error_trace_id ?? undefined,
      authorizationInstanceSourceAccountCooldownRetestLastAt: row.source_cooldown_retest_last_at ?? undefined,
      authorizationInstanceSourceAccountCooldownRetestLastStatusCode: row.source_cooldown_retest_last_status_code ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckAt: row.source_last_health_check_at ?? undefined,
      authorizationInstanceSourceAccountNextHealthCheckAt: row.source_next_health_check_at ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckStatusCode: row.source_last_health_check_status_code ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckErrorCode: row.source_last_health_check_error_code ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckErrorMessage: row.source_last_health_check_error_message ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckTraceId: row.source_last_health_check_trace_id ?? undefined,
      accountExpiresAt: row.account_expires_at ?? undefined,
      status: effectiveStatus,
      schedulable: row.schedulable === 1,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.authorization_id ? undefined : row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      lastErrorTraceId: row.authorization_id ? undefined : row.last_error_trace_id ?? undefined,
      lastHealthCheckAt: row.last_health_check_at ?? undefined,
      nextHealthCheckAt: row.next_health_check_at ?? undefined,
      lastHealthCheckStatusCode: row.last_health_check_status_code ?? undefined,
      lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
      lastHealthCheckErrorMessage: row.last_health_check_error_message ?? undefined,
      lastHealthCheckTraceId: row.last_health_check_trace_id ?? undefined,
      cooldownRetestLastAt: row.authorization_id ? undefined : row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: row.authorization_id ? undefined : row.cooldown_retest_last_status_code ?? undefined,
      apiKeyRuntime: apiKeyRuntime.get(row.id),
      todayUsage: usage,
      lastUsedAt: isAuthorized ? authorizationTotal.get(row.id)?.lastUsedAt : row.last_used_at ?? undefined
    }]
  })
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
