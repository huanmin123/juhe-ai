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
  loadRequestQuotaCostsBatch,
  loadRequestQuotaCostsBatchAsync,
  requestQuotaCostKey,
  requestQuotaCostKeyAsync,
  type RequestQuotaCostInput,
  type RequestQuotaCosts
} from '../modules/gateway/quota/request-quota-checker.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { authorizationRuntimeBlockingStatus } from './account-runtime-status.js'
import {
  emptyAccountManagementListUsage,
  loadAccountManagementListUsageAsync,
  type AccountManagementListUsageScope,
  type AccountManagementListUsageValue
} from './account-management-list-usage.repository.js'
import {
  dateKey,
  monthKey,
  nextZonedHourBoundaryIso,
  startOfZonedDateKeyIso,
  todayDateKey,
  usageStatsTimezoneAsync,
  weekKey
} from './usage-stats-helpers.js'
import { nextDateKey } from './usage-stats-window-helpers.js'
import { chunkValues } from './query-utils.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export interface AccountStatusProjectionSeed {
  id: string
  config_revision?: number | string
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
  source_availability_schedule_json: string | null
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
  bound_group_account_authorization_id: string | null
}

export interface AccountManagementStatusSeed {
  id: string
  config_revision?: number | string
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
  source_availability_schedule_json: string | null
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
  bound_group_account_authorization_id: string | null
}

/** 运行态筛选只保留有效可用性分类所需字段，候选扫描不读取列表展示用量。 */
export interface AccountStatusProjection extends AccountEffectiveAvailabilityInput {
  id: string
  configRevision?: number
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
  /** Internal transition used to make a quota-exceeded projection naturally due. */
  quotaResetAt?: string
}

export type AccountStatusFilterProjection = Omit<
  AccountStatusProjection,
  'todayUsage' | 'balanceQueryEnabled' | 'balanceQueryNextRefreshAt' | 'lastUsedAt' | 'sourceAccountProbe'
>

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

export async function hydrateAccountManagementStatusSeedsAsync(
  seeds: AccountManagementStatusSeed[]
): Promise<AccountStatusProjection[]> {
  if (seeds.length === 0) return []
  if (runtimeConfig.databaseDriver === 'sqlite' && sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'hydrate_account_management_status_seeds_read_only',
      seeds
    })
  }
  return hydrateAccountManagementStatusSeedsDirect(seeds, seeds.map((seed) => seed.id))
}

export async function hydrateAccountManagementStatusFilterSeedsAsync(
  seeds: AccountManagementStatusSeed[]
): Promise<AccountStatusFilterProjection[]> {
  if (seeds.length === 0) return []
  if (runtimeConfig.databaseDriver === 'sqlite' && sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'hydrate_account_management_status_filter_seeds_read_only',
      seeds
    })
  }
  return hydrateAccountManagementStatusFilterSeedsDirect(
    await accountStatusDatabaseClient(),
    seeds,
    seeds.map((seed) => seed.id)
  )
}

export async function hydrateAccountManagementStatusSeedsReadOnly(
  seeds: AccountManagementStatusSeed[]
): Promise<AccountStatusProjection[]> {
  if (seeds.length === 0) return []
  return hydrateAccountManagementStatusSeedsDirect(seeds, seeds.map((seed) => seed.id))
}

export async function hydrateAccountManagementStatusFilterSeedsReadOnly(
  seeds: AccountManagementStatusSeed[]
): Promise<AccountStatusFilterProjection[]> {
  if (seeds.length === 0) return []
  return hydrateAccountManagementStatusFilterSeedsDirect(
    createSqliteDatabaseClient(getBusinessDatabase()),
    seeds,
    seeds.map((seed) => seed.id)
  )
}

async function hydrateAccountManagementStatusSeedsDirect(
  rows: AccountManagementStatusSeed[],
  orderedIds: string[]
): Promise<AccountStatusProjection[]> {
  const timezone = await usageStatsTimezoneAsync()
  const todayScopes: AccountManagementListUsageScope[] = []
  const authorizationTotalScopes: AccountManagementListUsageScope[] = []
  for (const row of rows) {
    const scope: AccountManagementListUsageScope = row.authorization_id
      ? { rowKey: row.id, systemAccountId: row.system_account_id, scopeType: 'account_authorization', scopeId: row.authorization_id }
      : { rowKey: row.id, systemAccountId: row.system_account_id, scopeType: 'account', scopeId: row.id }
    todayScopes.push(scope)
    if (row.authorization_id) authorizationTotalScopes.push(scope)
  }
  const client = await accountStatusDatabaseClient()
  const [todayUsage, authorizationTotal, quotaStatusByAuthorization] = await Promise.all([
    loadAccountManagementListUsageAsync(todayScopes, todayDateKey(timezone)),
    loadAccountManagementListUsageAsync(authorizationTotalScopes),
    loadAccountStatusAuthorizationQuotaExceededAsync(client, rows)
  ])
  const filtersById = new Map((await hydrateAccountManagementStatusFilterSeedsDirect(client, rows, orderedIds, quotaStatusByAuthorization))
    .map((projection) => [projection.id, projection]))
  const rowsById = new Map(rows.map((row) => [row.id, row]))
  return orderedIds.flatMap((id) => {
    const row = rowsById.get(id)
    const filterProjection = filtersById.get(id)
    if (!row || !filterProjection) return []
    const projection: AccountStatusProjection = {
      ...filterProjection,
      ...(row.config_revision === undefined ? {} : { configRevision: Number(row.config_revision) }),
      balanceQueryEnabled: filterProjection.accessType === 'authorized' ? undefined : row.balance_query_enabled === 1,
      balanceQueryNextRefreshAt: filterProjection.accessType === 'authorized' ? undefined : row.balance_query_next_refresh_at ?? undefined,
      todayUsage: accountStatusTodayUsage(todayUsage.get(row.id)),
      lastUsedAt: filterProjection.accessType === 'authorized' ? authorizationTotal.get(row.id)?.lastUsedAt : row.last_used_at ?? undefined
    }
    projection.sourceAccountProbe = accountStatusSourceAccountProbe(projection, row)
    return [projection]
  })
}

async function hydrateAccountManagementStatusFilterSeedsDirect(
  client: DatabaseClient,
  rows: AccountManagementStatusSeed[],
  orderedIds: string[],
  providedQuotaStatusByAuthorization?: Map<string, AccountAuthorizationQuotaStatus>
): Promise<AccountStatusFilterProjection[]> {
  const quotaStatusByAuthorization = providedQuotaStatusByAuthorization ?? await loadAccountStatusAuthorizationQuotaExceededAsync(client, rows)
  const byId = new Map(rows.map((row) => [row.id, row]))
  return orderedIds.flatMap((id) => {
    const row = byId.get(id)
    if (!row) return []
    const isAuthorized = Boolean(row.authorization_id)
    const groupBinding = accountStatusGroupBinding(row)
    const projection: AccountStatusFilterProjection = {
      id: row.id,
      runtimeKey: isAuthorized && groupBinding && row.authorization_id
        ? `${row.id}:authorized:${row.system_account_id}:${groupBinding.groupId}:${row.authorization_id}`
        : row.id,
      concurrencyAccountId: row.authorization_instance_source_account_id || row.id,
      permissions: accountStatusPermissions(isAuthorized, row.authorization_effective_source_type),
      accessType: isAuthorized ? 'authorized' as const : 'owner' as const,
      boundGroupId: groupBinding?.groupId,
      groupBindStatus: groupBinding?.groupBindStatus,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: row.authorization_id ? parseRequestQuotaLimitsJson(row.authorization_limits_json) : undefined,
      authorizationQuotaExceeded: row.authorization_id ? quotaStatusByAuthorization.get(row.authorization_id)?.exceeded : undefined,
      quotaResetAt: row.authorization_id ? quotaStatusByAuthorization.get(row.authorization_id)?.resetAt : undefined,
      authorizationInstanceSourceAccountId: row.authorization_instance_source_account_id ?? undefined,
      authorizationInstanceSourceAccountStatus: row.source_status ?? undefined,
      authorizationInstanceSourceAccountSchedulable: row.source_schedulable === null ? undefined : row.source_schedulable === 1,
      authorizationInstanceSourceAccountExpiresAt: row.source_account_expires_at ?? undefined,
      authorizationInstanceSourceAccountCooldownUntil: row.source_cooldown_until ?? undefined,
      authorizationInstanceSourceAccountLastErrorCode: row.source_last_error_code ?? undefined,
      authorizationInstanceSourceAccountLastErrorMessage: accountListDiagnosticText(row.source_last_error_message),
      authorizationInstanceSourceAccountLastErrorTraceId: row.source_last_error_trace_id ?? undefined,
      authorizationInstanceSourceAccountCooldownRetestLastAt: row.source_cooldown_retest_last_at ?? undefined,
      authorizationInstanceSourceAccountCooldownRetestLastStatusCode: row.source_cooldown_retest_last_status_code ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckAt: row.source_last_health_check_at ?? undefined,
      authorizationInstanceSourceAccountNextHealthCheckAt: row.source_next_health_check_at ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckStatusCode: row.source_last_health_check_status_code ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckErrorCode: row.source_last_health_check_error_code ?? undefined,
      authorizationInstanceSourceAccountLastHealthCheckErrorMessage: accountListDiagnosticText(row.source_last_health_check_error_message),
      authorizationInstanceSourceAccountLastHealthCheckTraceId: row.source_last_health_check_trace_id ?? undefined,
      accountExpiresAt: row.account_expires_at ?? undefined,
      status: isAuthorized
        ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
        : row.status,
      schedulable: row.schedulable === 1,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: accountListDiagnosticText(row.last_error_message),
      lastErrorTraceId: row.last_error_trace_id ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)) || undefined,
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      lastHealthCheckAt: row.last_health_check_at ?? undefined,
      nextHealthCheckAt: row.next_health_check_at ?? undefined,
      lastHealthSuccessAt: row.last_health_success_at ?? undefined,
      healthCheckFailureCount: Math.max(0, Number(row.health_check_failure_count ?? 0)) || undefined,
      healthCheckFailureStartedAt: row.health_check_failure_started_at ?? undefined,
      lastHealthCheckStatusCode: row.last_health_check_status_code ?? undefined,
      lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
      lastHealthCheckErrorMessage: accountListDiagnosticText(row.last_health_check_error_message),
      lastHealthCheckTraceId: row.last_health_check_trace_id ?? undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: row.cooldown_retest_last_status_code ?? undefined,
      streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)) || undefined,
      streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined
    }
    return [projection]
  })
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

export function accountStatusGroupBindingsJoin(
  client: Pick<DatabaseClient, 'driver'>,
  groupAccountsTable: string
): string {
  return client.driver === 'postgres'
    ? `LEFT JOIN LATERAL (
        SELECT
          group_accounts.system_account_id,
          group_accounts.group_id,
          group_accounts.account_authorization_id
        FROM ${groupAccountsTable} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = accounts.system_account_id
          AND group_accounts.enabled = 1
        ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
        LIMIT 1
      ) group_bindings ON true`
    : `LEFT JOIN (
        SELECT account_id, system_account_id, group_id, account_authorization_id
        FROM (
          SELECT group_accounts.*,
            ROW_NUMBER() OVER (
              PARTITION BY group_accounts.account_id, group_accounts.system_account_id
              ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
            ) AS binding_rank
          FROM ${groupAccountsTable} group_accounts
          WHERE group_accounts.enabled = 1
        ) ranked_group_bindings
        WHERE binding_rank = 1
      ) group_bindings
      ON group_bindings.account_id = accounts.id
      AND group_bindings.system_account_id = accounts.system_account_id`
}

async function listAccountStatusProjectionsDirect(
  access: AccessScope | undefined,
  ids: string[],
  client: DatabaseClient
): Promise<AccountStatusProjection[]> {
  const accounts = businessTable(client, 'accounts')
  const authorizations = businessTable(client, 'resource_authorizations')
  const groupAccounts = businessTable(client, 'group_accounts')
  const systemAccountId = scopedSystemAccountId(access)
  const groupBindingsJoin = accountStatusGroupBindingsJoin(client, groupAccounts)
  const rows = await client.query<AccountStatusProjectionSeed>(`
    SELECT
      accounts.id, accounts.config_revision, accounts.system_account_id, accounts.status, accounts.schedulable,
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
      source_accounts.availability_schedule_json AS source_availability_schedule_json,
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
      group_bindings.group_id AS bound_group_id,
      group_bindings.account_authorization_id AS bound_group_account_authorization_id
    FROM ${accounts} accounts
    LEFT JOIN ${authorizations} ra ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${accounts} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    ${groupBindingsJoin}
    WHERE accounts.deleted_at IS NULL
      AND accounts.id IN (${ids.map(() => '?').join(', ')})
      ${systemAccountId ? 'AND accounts.system_account_id = ?' : ''}
      AND (accounts.authorization_instance_authorization_id IS NULL OR ra.status IN ('active', 'paused', 'expired'))
  `, [...ids, ...(systemAccountId ? [systemAccountId] : [])])

  return hydrateAccountStatusProjectionSeedsDirect(client, rows, ids)
}

async function hydrateAccountStatusProjectionSeedsDirect(
  client: DatabaseClient,
  rows: AccountStatusProjectionSeed[],
  orderedIds: string[]
): Promise<AccountStatusProjection[]> {
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
  const [todayUsage, authorizationTotal, quotaStatusByAuthorization] = await Promise.all([
    loadAccountManagementListUsageAsync(todayScopes, todayDateKey(timezone)),
    loadAccountManagementListUsageAsync(authorizationTotalScopes),
    loadAccountStatusAuthorizationQuotaExceededAsync(client, rows)
  ])
  const byId = new Map(rows.map((row) => [row.id, row]))
  return orderedIds.flatMap((id) => {
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
      ...(row.config_revision === undefined ? {} : { configRevision: Number(row.config_revision) }),
      runtimeKey,
      concurrencyAccountId: row.authorization_instance_source_account_id || row.id,
      permissions: accountStatusPermissions(isAuthorized, row.authorization_effective_source_type),
      accessType: isAuthorized ? 'authorized' : 'owner',
      boundGroupId: groupBinding?.groupId,
      groupBindStatus: groupBinding?.groupBindStatus,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: row.authorization_id ? parseRequestQuotaLimitsJson(row.authorization_limits_json) : undefined,
      authorizationQuotaExceeded: row.authorization_id ? quotaStatusByAuthorization.get(row.authorization_id)?.exceeded : undefined,
      quotaResetAt: row.authorization_id ? quotaStatusByAuthorization.get(row.authorization_id)?.resetAt : undefined,
      authorizationInstanceSourceAccountId: row.authorization_instance_source_account_id ?? undefined,
      authorizationInstanceSourceAccountStatus: row.source_status ?? undefined,
      authorizationInstanceSourceAccountSchedulable: row.source_schedulable === null ? undefined : row.source_schedulable === 1,
      authorizationInstanceSourceAccountExpiresAt: row.source_account_expires_at ?? undefined,
      authorizationInstanceSourceAccountCooldownUntil: row.source_cooldown_until ?? undefined,
      authorizationInstanceSourceAccountLastErrorCode: row.source_last_error_code ?? undefined,
      authorizationInstanceSourceAccountLastErrorMessage: accountListDiagnosticText(row.source_last_error_message),
      accountExpiresAt: row.account_expires_at ?? undefined,
      status: effectiveStatus,
      schedulable: row.schedulable === 1,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: accountListDiagnosticText(row.last_error_message),
      lastErrorTraceId: row.last_error_trace_id ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)) || undefined,
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      lastHealthCheckAt: row.last_health_check_at ?? undefined,
      nextHealthCheckAt: row.next_health_check_at ?? undefined,
      lastHealthSuccessAt: row.last_health_success_at ?? undefined,
      healthCheckFailureCount: Math.max(0, Number(row.health_check_failure_count ?? 0)) || undefined,
      healthCheckFailureStartedAt: row.health_check_failure_started_at ?? undefined,
      lastHealthCheckStatusCode: row.last_health_check_status_code ?? undefined,
      lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
      lastHealthCheckErrorMessage: accountListDiagnosticText(row.last_health_check_error_message),
      lastHealthCheckTraceId: row.last_health_check_trace_id ?? undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: row.cooldown_retest_last_status_code ?? undefined,
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
  row: Pick<AccountStatusProjectionSeed,
    | 'source_last_error_trace_id'
    | 'source_cooldown_retest_last_at'
    | 'source_cooldown_retest_last_status_code'
    | 'source_last_health_check_at'
    | 'source_next_health_check_at'
    | 'source_last_health_check_status_code'
    | 'source_last_health_check_error_code'
    | 'source_last_health_check_error_message'
    | 'source_last_health_check_trace_id'
  >,
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
    authorizationInstanceSourceAccountLastHealthCheckErrorMessage: accountListDiagnosticText(row.source_last_health_check_error_message),
    authorizationInstanceSourceAccountLastHealthCheckTraceId: row.source_last_health_check_trace_id ?? undefined
  }, now).probe
}

/** Keep fast-list diagnostics useful without allowing 100 records to exceed its response budget. */
function accountListDiagnosticText(value: string | null | undefined): string | undefined {
  const text = value?.trim()
  if (!text) return undefined
  return text.length <= 96 ? text : `${text.slice(0, 95)}…`
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

interface AccountAuthorizationQuotaStatus {
  exceeded: boolean
  resetAt?: string
}

async function loadAccountStatusAuthorizationQuotaExceededAsync(
  client: DatabaseClient,
  rows: AccountStatusProjectionSeed[]
): Promise<Map<string, AccountAuthorizationQuotaStatus>> {
  const output = new Map<string, AccountAuthorizationQuotaStatus>()
  const teamLimits = await loadAccountStatusTeamLimitJsonAsync(client, rows)
  const checks: Array<{
    authorizationId: string
    limits: ReturnType<typeof parseRequestQuotaLimitsJson>
    input: RequestQuotaCostInput
  }> = []
  const now = new Date()
  const timezone = await usageStatsTimezoneAsync()
  for (const row of rows) {
    if (!row.authorization_id) continue
    output.set(row.authorization_id, { exceeded: false })
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
  if (client.driver === 'postgres') {
    const costs = await loadRequestQuotaCostsBatchAsync(client, checks.map((check) => check.input))
    for (const check of checks) {
      const value = costs.get(await requestQuotaCostKeyAsync(check.input))
      applyAccountAuthorizationQuotaStatus(output, check.authorizationId, check.limits, value, now, timezone)
    }
    return output
  }
  const costs = loadRequestQuotaCostsBatch(getStatsDatabase(), checks.map((check) => check.input))
  for (const check of checks) {
    const value = costs.get(requestQuotaCostKey(check.input))
    applyAccountAuthorizationQuotaStatus(output, check.authorizationId, check.limits, value, now, timezone)
  }
  return output
}

function applyAccountAuthorizationQuotaStatus(
  output: Map<string, AccountAuthorizationQuotaStatus>,
  authorizationId: string,
  limits: RequestQuotaLimits,
  costs: RequestQuotaCosts | undefined,
  now: Date,
  timezone: string
): void {
  if (!costs || !isRequestQuotaExceeded(limits, costs)) return
  const current = output.get(authorizationId) ?? { exceeded: false }
  const resetAt = requestQuotaResetAt(limits, costs, now, timezone)
  output.set(authorizationId, {
    exceeded: true,
    resetAt: earliestFutureIso(current.resetAt, resetAt)
  })
}

function requestQuotaResetAt(
  limits: RequestQuotaLimits,
  costs: RequestQuotaCosts,
  now: Date,
  timezone: string
): string | undefined {
  const resets: string[] = []
  if (limits.hourly?.enabled && costs.hourly >= limits.hourly.limit) {
    resets.push(nextZonedHourBoundaryIso(now, timezone))
  }
  if (limits.daily?.enabled && costs.daily >= limits.daily.limit) {
    const value = startOfZonedDateKeyIso(nextDateKey(dateKey(now, timezone)), timezone)
    if (value) resets.push(value)
  }
  if (limits.weekly?.enabled && costs.weekly >= limits.weekly.limit) {
    let nextWeek = weekKey(now, timezone)
    for (let index = 0; index < 7; index += 1) nextWeek = nextDateKey(nextWeek)
    const value = startOfZonedDateKeyIso(nextWeek, timezone)
    if (value) resets.push(value)
  }
  if (limits.monthly?.enabled && costs.monthly >= limits.monthly.limit) {
    const [yearText, monthText] = monthKey(now, timezone).split('-')
    const year = Number(yearText)
    const month = Number(monthText)
    if (Number.isInteger(year) && Number.isInteger(month)) {
      const nextMonth = new Date(Date.UTC(year, month, 1))
      const value = startOfZonedDateKeyIso(`${nextMonth.getUTCFullYear()}-${String(nextMonth.getUTCMonth() + 1).padStart(2, '0')}-01`, timezone)
      if (value) resets.push(value)
    }
  }
  return resets.sort()[0]
}

function earliestFutureIso(left: string | undefined, right: string | undefined): string | undefined {
  if (!left) return right
  if (!right) return left
  return left < right ? left : right
}

async function loadAccountStatusTeamLimitJsonAsync(
  client: DatabaseClient,
  rows: AccountStatusProjectionSeed[]
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

function accountStatusGroupBinding(row: Pick<AccountStatusProjectionSeed,
  'bound_group_id' | 'binding_system_account_id' | 'system_account_id' | 'bound_group_account_authorization_id' | 'authorization_id'
>): {
  groupId: string
  groupBindStatus: 'bound' | 'authorization_unavailable'
} | undefined {
  if (!row.bound_group_id || row.binding_system_account_id !== row.system_account_id) return undefined
  return {
    groupId: row.bound_group_id,
    groupBindStatus: row.bound_group_account_authorization_id !== row.authorization_id
      ? 'authorization_unavailable'
      : 'bound'
  }
}

function accountStatusPermissions(
  authorized: boolean,
  sourceType: AccountStatusProjectionSeed['authorization_effective_source_type']
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
