import type { AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus } from '../domain/types.js'
import {
  EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE,
  isExplicitAccountErrorPolicyCooldown,
  LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX
} from '../domain/account-runtime-provenance.js'
import { runtimeConfig } from '../config/runtime.js'
import { currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { accountEnabledGroupId } from './account-group-binding-write.repository.js'
import { isAccountAvailabilityScheduleAllowed } from './account-availability-schedule.js'
import { findAccountSummary, findAccountSummaryAsync } from './account-summary.repository.js'
import { isCoolingAccountStatus, isHardUnavailableAccountStatus, normalizeAccountStatus } from './account-status.js'
import { normalizedDispatchPriority } from './account-write-input.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { getPostgresPool } from './postgres-client.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'
import type { AccountFailureRow, AccountRow } from './repository-row-types.js'
import { accountSystemAccountId, canManageResourceOwner } from './resource-authorization-helpers.js'
import {
  cooldownRetestObservationStartedAtForStatus,
  defaultTemporaryUnschedulableMinutes,
  defaultTemporaryUnschedulableMinutesAsync,
  initialCooldownUntilForStatus,
  initialCooldownUntilForStatusAsync,
  invalidateGatewayRuntimeAfterBusinessWrite,
  isAccountExpired,
  newCooldownRetestGeneration,
  temporaryUnavailableRuntimeState
} from './account-runtime-mutation-helpers.js'

const manualTrafficMigrationReason = '手动迁移流量'
const internalAccountReadAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

function findInternalAccountSummary(accountId: string): AccountSummary | undefined {
  return findAccountSummary(accountId, internalAccountReadAccess)
}

async function findInternalAccountSummaryAsync(accountId: string): Promise<AccountSummary | undefined> {
  return findAccountSummaryAsync(accountId, internalAccountReadAccess)
}

function accountRowForManage(accountId: string, access?: AccessScope): AccountRow | undefined {
  const row = getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ? AND deleted_at IS NULL').get(accountId) as unknown as AccountRow | undefined
  if (!row || !canManageResourceOwner(row.system_account_id, access)) {
    return undefined
  }
  return row
}

async function accountRowForManageAsync(client: DatabaseClient, accountId: string, access?: AccessScope): Promise<AccountRow | undefined> {
  const row = await client.one<AccountRow>(`
    SELECT *
    FROM ${accountRuntimeMutationTable(client, 'accounts')}
    WHERE id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [accountId])
  if (!row || !canManageResourceOwner(row.system_account_id, access)) {
    return undefined
  }
  return row
}

function accountDispatchUnavailableMessage(account: AccountSummary, options: { requireAuthorizedBinding?: boolean } = {}): string | undefined {
  if (account.accessType === 'authorized' && options.requireAuthorizedBinding && !account.boundGroupId) {
    return '授权账户需要先绑定到你的分组'
  }
  if (account.effectiveAvailability.available === false) {
    return account.effectiveAvailability.reason ?? account.effectiveAvailability.label
  }
  return undefined
}

function authorizedBindingSystemAccountId(access?: AccessScope): string {
  return scopedSystemAccountId(access) ?? currentSystemAccountId(access)
}

function isLaterIso(value?: string, current?: string): boolean {
  if (!value) return false
  if (!current) return true
  const nextTime = Date.parse(value)
  const currentTime = Date.parse(current)
  return Number.isFinite(nextTime) && (!Number.isFinite(currentTime) || nextTime > currentTime)
}

async function accountEnabledGroupIdForClientAsync(client: DatabaseClient, accountId: string, systemAccountId: string): Promise<string | undefined> {
  const row = await client.one<{ group_id?: string }>(`
    SELECT group_id
    FROM ${accountRuntimeMutationTable(client, 'group_accounts')}
    WHERE account_id = ?
      AND system_account_id = ?
      AND enabled = 1
    ORDER BY updated_at DESC, group_id ASC, account_id ASC
    LIMIT 1
  `, [accountId, systemAccountId])
  return row?.group_id
}

interface ClearAccountFailureStateOptions {
  allowErrorRestore?: boolean
  allowPendingTestRestore?: boolean
  allowExplicitPolicyRestore?: boolean
  expectedLastErrorCodes?: readonly string[]
}

export interface AccountFailureStateClearResult {
  account?: AccountSummary
  changed: boolean
}

function normalizedExpectedLastErrorCodes(value: readonly string[] | undefined): string[] | undefined {
  if (!value) return undefined
  const codes = [...new Set(value.map((item) => item.trim()).filter(Boolean))]
  return codes.length ? codes : undefined
}

function expectedLastErrorCodePredicate(codes: readonly string[] | undefined): string {
  if (!codes?.length) return ''
  return `
          AND status = 'error'
          AND last_error_code IN (${codes.map(() => '?').join(', ')})`
}

function normalizedLastErrorTraceId(value?: string): string | null {
  return typeof value === 'string' && value.trim() ? value.trim().slice(0, 200) : null
}

export interface AccountForceActivateResult {
  account?: AccountSummary
  changed: boolean
}

export function forceActivatePendingAccount(id: string, access?: AccessScope): AccountForceActivateResult {
  const accountAccess = access ?? internalAccountReadAccess
  const current = accountRowForManage(id, accountAccess)
  if (!current || current.authorization_instance_authorization_id || current.status !== 'pending_test' || isAccountExpired(current.account_expires_at ?? undefined)) {
    return { account: findAccountSummary(id, accountAccess), changed: false }
  }
  const checkedAt = nowIso()
  const nextStatus = isAccountAvailabilityScheduleAllowed(current.availability_schedule_json, new Date(checkedAt))
    ? 'active'
    : 'disabled'
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  let changed = false
  try {
    const result = database.prepare(`
    UPDATE accounts
    SET status = ?,
        schedulable = ?,
        cooldown_until = NULL,
        last_error_code = NULL,
        last_error_message = NULL,
        last_error_trace_id = NULL,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        next_health_check_at = NULL,
        health_check_failure_count = 0,
        health_check_failure_started_at = NULL,
        last_health_check_error_code = NULL,
        last_health_check_error_message = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id IS NULL
      AND deleted_at IS NULL
      AND status = 'pending_test'
      AND config_revision = ?
      AND (account_expires_at IS NULL OR account_expires_at > ?)
  `).run(nextStatus, 1, checkedAt, id, current.system_account_id, current.config_revision, checkedAt)
    changed = Number(result.changes ?? 0) > 0
    if (changed) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_pending_force_activated' })
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (changed) {
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_pending_force_activated')
  }
  return { account: findAccountSummary(id, accountAccess), changed }
}

export async function forceActivatePendingAccountAsync(id: string, access?: AccessScope): Promise<AccountForceActivateResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return forceActivatePendingAccount(id, access)
  }
  const accountAccess = access ?? internalAccountReadAccess
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const current = await accountRowForManageAsync(client, id, accountAccess)
  if (!current || current.authorization_instance_authorization_id || current.status !== 'pending_test' || isAccountExpired(current.account_expires_at ?? undefined)) {
    return { account: await findAccountSummaryAsync(id, accountAccess), changed: false }
  }
  const checkedAt = nowIso()
  const nextStatus = isAccountAvailabilityScheduleAllowed(current.availability_schedule_json, new Date(checkedAt))
    ? 'active'
    : 'disabled'
  const changed = await client.transaction(async (tx) => {
    const result = await tx.execute(`
      UPDATE ${accountRuntimeMutationTable(tx, 'accounts')}
      SET status = ?,
        schedulable = 1,
        cooldown_until = NULL,
        last_error_code = NULL,
        last_error_message = NULL,
        last_error_trace_id = NULL,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        next_health_check_at = NULL,
        health_check_failure_count = 0,
        health_check_failure_started_at = NULL,
        last_health_check_error_code = NULL,
        last_health_check_error_message = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id IS NULL
      AND deleted_at IS NULL
      AND status = 'pending_test'
      AND config_revision = ?
      AND (account_expires_at IS NULL OR account_expires_at > ?)
    `, [nextStatus, checkedAt, id, current.system_account_id, current.config_revision, checkedAt])
    const updated = Number(result.changes ?? 0) > 0
    if (updated) {
      await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_pending_force_activated' }, tx)
    }
    return updated
  })
  if (changed) {
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_pending_force_activated')
  }
  return { account: await findAccountSummaryAsync(id, accountAccess), changed }
}

export function clearAccountFailureState(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): AccountSummary | undefined {
  return clearAccountFailureStateResult(id, access, options).account
}

export async function clearAccountFailureStateAsync(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): Promise<AccountSummary | undefined> {
  return (await clearAccountFailureStateResultAsync(id, access, options)).account
}

export function clearAccountFailureStateResult(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): AccountFailureStateClearResult {
  const accountAccess = access ?? internalAccountReadAccess
  const current = findAccountSummary(id, accountAccess)
  if (!current) {
    return { changed: false }
  }
  const ownerSystemAccountId = accountSystemAccountId(id)
  if (ownerSystemAccountId && !canManageResourceOwner(ownerSystemAccountId, accountAccess)) {
    return { changed: false }
  }
  const expectedLastErrorCodes = normalizedExpectedLastErrorCodes(options.expectedLastErrorCodes)
  if (expectedLastErrorCodes && (current.status !== 'error' || !current.lastErrorCode || !expectedLastErrorCodes.includes(current.lastErrorCode))) {
    return { account: current, changed: false }
  }
  const expectedLastErrorClause = expectedLastErrorCodePredicate(expectedLastErrorCodes)
  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (current.status === 'disabled' && !expiredByPackage) {
    return { account: current, changed: false }
  }
  if (current.status === 'pending_test' && options.allowPendingTestRestore !== true) {
    return { account: current, changed: false }
  }
  if (current.status === 'error' && options.allowErrorRestore === false) {
    return { account: current, changed: false }
  }
  if (!expiredByPackage
    && options.allowExplicitPolicyRestore !== true
    && isExplicitAccountErrorPolicyCooldown(current.lastErrorCode, current.lastErrorMessage)) {
    return { account: current, changed: false }
  }
  if (expiredByPackage) {
    const result = getBusinessDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = 'account_expired',
            last_error_message = ?,
            last_error_trace_id = NULL,
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND deleted_at IS NULL
          ${expectedLastErrorClause}
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id, ...(expectedLastErrorCodes ?? []))
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_expired' })
      invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
    }
    return { account: findAccountSummary(id, accountAccess), changed }
  }

  if (current.status === 'pending_test' || current.status === 'error') {
    const result = getBusinessDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'pending_test',
            schedulable = 0,
            config_revision = config_revision + 1,
            cooldown_until = NULL,
            last_error_code = NULL,
            last_error_message = '账户已重置，等待后台健康检查',
            last_error_trace_id = NULL,
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            last_health_check_at = NULL,
            next_health_check_at = NULL,
            last_health_success_at = NULL,
            health_check_failure_count = 0,
            health_check_failure_started_at = NULL,
            last_health_check_status_code = NULL,
            last_health_check_error_code = NULL,
            last_health_check_error_message = NULL,
            last_health_check_trace_id = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND deleted_at IS NULL
          AND status = ?
          ${expectedLastErrorClause}
      `)
      .run(nowIso(), id, current.status, ...(expectedLastErrorCodes ?? []))
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_health_check_restarted' })
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_health_check_restarted')
    }
    return { account: findAccountSummary(id, accountAccess), changed }
  }

  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'active',
          schedulable = 1,
          cooldown_until = NULL,
          last_error_code = NULL,
          last_error_message = NULL,
          last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND status <> 'disabled'
        AND (? = 1 OR status <> 'error')
        AND (? = 1 OR status <> 'pending_test')
        AND (? = 1 OR NOT (
          COALESCE(last_error_code, '') = ?
          OR (last_error_code IS NULL AND COALESCE(last_error_message, '') LIKE ?)
        ))
        ${expectedLastErrorClause}
        AND (
          status <> 'active'
          OR schedulable <> 1
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NOT NULL
          OR last_error_trace_id IS NOT NULL
          OR cooldown_retest_failure_count > 0
          OR cooldown_retest_observation_started_at IS NOT NULL
          OR cooldown_retest_last_at IS NOT NULL
          OR cooldown_retest_last_status_code IS NOT NULL
          OR stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
        )
    `)
    .run(
      nowIso(),
      id,
      options.allowErrorRestore === false ? 0 : 1,
      options.allowPendingTestRestore === true ? 1 : 0,
      options.allowExplicitPolicyRestore === true ? 1 : 0,
      EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE,
      `${LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX}%`,
      ...(expectedLastErrorCodes ?? [])
    )
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_restored' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_restored')
  }

  return { account: findAccountSummary(id, accountAccess), changed }
}

export async function clearAccountFailureStateResultAsync(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): Promise<AccountFailureStateClearResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return clearAccountFailureStateResult(id, access, options)
  }
  const accountAccess = access ?? internalAccountReadAccess
  const current = await findAccountSummaryAsync(id, accountAccess)
  if (!current) {
    return { account: current, changed: false }
  }
  if (current.accessType === 'authorized') {
    if (!current.boundGroupId || !current.accountAuthorizationId) {
      return { account: current, changed: false }
    }
    return clearAuthorizedAccountBindingFailureStateByContextAsync({
      accountId: id,
      systemAccountId: authorizedBindingSystemAccountId(access),
      groupId: current.boundGroupId,
      accountAuthorizationId: current.accountAuthorizationId
    }, options)
  }
  const ownerSystemAccountId = current.ownerSystemAccountId
  if (!ownerSystemAccountId || !canManageResourceOwner(ownerSystemAccountId, accountAccess)) {
    return { changed: false }
  }
  const expectedLastErrorCodes = normalizedExpectedLastErrorCodes(options.expectedLastErrorCodes)
  if (expectedLastErrorCodes && (current.status !== 'error' || !current.lastErrorCode || !expectedLastErrorCodes.includes(current.lastErrorCode))) {
    return { account: current, changed: false }
  }
  const expectedLastErrorClause = expectedLastErrorCodePredicate(expectedLastErrorCodes)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (current.status === 'disabled' && !expiredByPackage) {
    return { account: current, changed: false }
  }
  if (current.status === 'pending_test' && options.allowPendingTestRestore !== true) {
    return { account: current, changed: false }
  }
  if (current.status === 'error' && options.allowErrorRestore === false) {
    return { account: current, changed: false }
  }
  if (!expiredByPackage
    && options.allowExplicitPolicyRestore !== true
    && isExplicitAccountErrorPolicyCooldown(current.lastErrorCode, current.lastErrorMessage)) {
    return { account: current, changed: false }
  }

  if (expiredByPackage) {
    const result = await client.execute(`
      UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = 'account_expired',
          last_error_message = ?,
          last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND deleted_at IS NULL
        ${expectedLastErrorClause}
    `, ['账户套餐已过期，已自动停用', nowIso(), id, ownerSystemAccountId, ...(expectedLastErrorCodes ?? [])])
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_expired' }, client)
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
    }
    return { account: await findAccountSummaryAsync(id, accountAccess), changed }
  }

  if (current.status === 'pending_test' || current.status === 'error') {
    const result = await client.execute(`
      UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
      SET status = 'pending_test',
          schedulable = 0,
          config_revision = config_revision + 1,
          cooldown_until = NULL,
          last_error_code = NULL,
          last_error_message = '账户已重置，等待后台健康检查',
          last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          last_health_check_at = NULL,
          next_health_check_at = NULL,
          last_health_success_at = NULL,
          health_check_failure_count = 0,
          health_check_failure_started_at = NULL,
          last_health_check_status_code = NULL,
          last_health_check_error_code = NULL,
          last_health_check_error_message = NULL,
          last_health_check_trace_id = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND deleted_at IS NULL
        AND status = ?
        ${expectedLastErrorClause}
    `, [nowIso(), id, ownerSystemAccountId, current.status, ...(expectedLastErrorCodes ?? [])])
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_health_check_restarted' }, client)
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_health_check_restarted')
    }
    return { account: await findAccountSummaryAsync(id, accountAccess), changed }
  }

  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET status = 'active',
        schedulable = 1,
        cooldown_until = NULL,
        last_error_code = NULL,
        last_error_message = NULL,
        last_error_trace_id = NULL,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND deleted_at IS NULL
      AND status <> 'disabled'
      AND (? = 1 OR status <> 'error')
      AND (? = 1 OR status <> 'pending_test')
      AND (? = 1 OR NOT (
        COALESCE(last_error_code, '') = ?
        OR (last_error_code IS NULL AND COALESCE(last_error_message, '') LIKE ?)
      ))
      ${expectedLastErrorClause}
      AND (
        status <> 'active'
        OR schedulable <> 1
        OR cooldown_until IS NOT NULL
        OR last_error_code IS NOT NULL
        OR last_error_message IS NOT NULL
        OR last_error_trace_id IS NOT NULL
        OR cooldown_retest_failure_count > 0
        OR cooldown_retest_observation_started_at IS NOT NULL
        OR cooldown_retest_last_at IS NOT NULL
        OR cooldown_retest_last_status_code IS NOT NULL
        OR stream_failure_count > 0
        OR stream_failure_window_started_at IS NOT NULL
      )
  `, [
    nowIso(),
    id,
    ownerSystemAccountId,
    options.allowErrorRestore === false ? 0 : 1,
    options.allowPendingTestRestore === true ? 1 : 0,
    options.allowExplicitPolicyRestore === true ? 1 : 0,
    EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE,
    `${LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX}%`,
    ...(expectedLastErrorCodes ?? [])
  ])
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_restored' }, client)
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_restored')
  }

  return { account: await findAccountSummaryAsync(id, accountAccess), changed }
}

export async function clearAuthorizedAccountBindingFailureStateByContextAsync(
  input: AuthorizedAccountBindingRuntimeTarget,
  options: ClearAccountFailureStateOptions = {}
): Promise<AccountFailureStateClearResult> {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return { changed: false }
  }
  const accountAccess: AccessScope = { systemAccountId: target.systemAccountId, role: 'user' }
  const current = await findAccountSummaryAsync(target.accountId, accountAccess)
  if (!current || current.accessType !== 'authorized' || current.status === 'disabled') {
    return { account: current, changed: false }
  }
  if (current.status === 'pending_test' && options.allowPendingTestRestore !== true) {
    return { account: current, changed: false }
  }
  if (options.allowExplicitPolicyRestore !== true
    && isExplicitAccountErrorPolicyCooldown(current.lastErrorCode, current.lastErrorMessage)) {
    return { account: current, changed: false }
  }
  const hasFailureState = current.status !== 'active'
    || Boolean(current.cooldownUntil)
    || Boolean(current.lastErrorMessage)
    || Boolean(current.streamFailureCount)
    || Boolean(current.streamFailureWindowStartedAt)
  if (!hasFailureState) {
    return { account: current, changed: false }
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const now = nowIso()
  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET status = 'active',
        schedulable = 1,
        cooldown_until = NULL,
        last_error_code = NULL,
        last_error_message = NULL,
        last_error_trace_id = NULL,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id = ?
      AND deleted_at IS NULL
      AND status <> 'disabled'
      AND (? = 1 OR status <> 'error')
      AND (? = 1 OR status <> 'pending_test')
      AND (? = 1 OR NOT (
        COALESCE(last_error_code, '') = ?
        OR (last_error_code IS NULL AND COALESCE(last_error_message, '') LIKE ?)
      ))
      AND (
        status <> 'active'
        OR schedulable <> 1
        OR cooldown_until IS NOT NULL
        OR last_error_code IS NOT NULL
        OR last_error_message IS NOT NULL
        OR last_error_trace_id IS NOT NULL
        OR cooldown_retest_failure_count > 0
        OR cooldown_retest_observation_started_at IS NOT NULL
        OR cooldown_retest_last_at IS NOT NULL
        OR cooldown_retest_last_status_code IS NOT NULL
        OR stream_failure_count > 0
        OR stream_failure_window_started_at IS NOT NULL
      )
      AND EXISTS (
        SELECT 1
        FROM ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
      )
  `, [
    now,
    target.accountId,
    target.systemAccountId,
    target.accountAuthorizationId,
    options.allowErrorRestore === false ? 0 : 1,
    options.allowPendingTestRestore === true ? 1 : 0,
    options.allowExplicitPolicyRestore === true ? 1 : 0,
    EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE,
    `${LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX}%`,
    target.systemAccountId,
    target.groupId,
    target.accountAuthorizationId
  ])
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    await refreshGroupAccountStatsAfterWriteAsync({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_restored' }, client)
    invalidateAccountLookupCache(target.accountId)
    invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_restored')
  }
  return {
    account: await findAccountSummaryAsync(target.accountId, accountAccess),
    changed
  }
}

function accountRuntimeMutationTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

export function clearAuthorizedAccountBindingFailureState(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): AccountFailureStateClearResult {
  const current = findAccountSummary(id, access)
  if (!current || current.accessType !== 'authorized' || !current.boundGroupId || !current.accountAuthorizationId) {
    return { account: current, changed: false }
  }
  if (current.status === 'disabled') {
    return { account: current, changed: false }
  }
  if (current.status === 'pending_test' && options.allowPendingTestRestore !== true) {
    return { account: current, changed: false }
  }
  const hasFailureState = current.status !== 'active'
    || Boolean(current.cooldownUntil)
    || Boolean(current.lastErrorMessage)
    || Boolean(current.streamFailureCount)
    || Boolean(current.streamFailureWindowStartedAt)
  if (!hasFailureState) {
    return { account: current, changed: false }
  }
  return clearAuthorizedAccountBindingFailureStateByContext({
    accountId: id,
    systemAccountId: authorizedBindingSystemAccountId(access),
    groupId: current.boundGroupId,
    accountAuthorizationId: current.accountAuthorizationId
  }, options)
}

export interface AuthorizedAccountBindingRuntimeTarget {
  accountId: string
  systemAccountId?: string
  groupId?: string
  accountAuthorizationId?: string
}

export interface AccountPrecheckMutationState {
  status: AccountStatus
  dispatchRevision: number
  updatedAt?: string
  lastUsedAt?: string
  lastHealthSuccessAt?: string
}

export interface AccountPrecheckMutationGuard {
  expectedDispatchRevision: number
  expectedStatus: AccountStatus
  precheckStartedAt: string
}

export interface AccountRuntimeFailureObservationGuard {
  expectedDispatchRevision: number
  observedAt: string
}

export interface AccountRuntimeSuccessObservationInput {
  accountId: string
  expectedDispatchRevision?: number
  observedAt: string
  authorizedBinding?: AuthorizedAccountBindingRuntimeTarget
}

export interface AccountRuntimeSuccessObservationResult {
  accepted: boolean
  changed: boolean
  accountStatus?: AccountStatus
}

function normalizedAuthorizedAccountBindingRuntimeTarget(
  input: AuthorizedAccountBindingRuntimeTarget
): Required<AuthorizedAccountBindingRuntimeTarget> | undefined {
  const accountId = input.accountId?.trim()
  const systemAccountId = input.systemAccountId?.trim()
  const groupId = input.groupId?.trim()
  const accountAuthorizationId = input.accountAuthorizationId?.trim()
  if (!accountId || !systemAccountId || !groupId || !accountAuthorizationId) {
    return undefined
  }
  return { accountId, systemAccountId, groupId, accountAuthorizationId }
}

function authorizedAccountRuntimeBindingExists(target: Required<AuthorizedAccountBindingRuntimeTarget>): boolean {
  const row = getBusinessDatabase()
    .prepare(`
      SELECT accounts.id
      FROM accounts
      INNER JOIN group_accounts
        ON group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = ?
        AND group_accounts.group_id = ?
        AND group_accounts.enabled = 1
        AND group_accounts.account_authorization_id = ?
      WHERE accounts.id = ?
        AND accounts.system_account_id = ?
        AND accounts.authorization_instance_authorization_id = ?
        AND accounts.deleted_at IS NULL
      LIMIT 1
    `)
    .get(target.systemAccountId, target.groupId, target.accountAuthorizationId, target.accountId, target.systemAccountId, target.accountAuthorizationId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

async function authorizedAccountRuntimeBindingExistsAsync(target: Required<AuthorizedAccountBindingRuntimeTarget>): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return authorizedAccountRuntimeBindingExists(target)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<{ id?: string }>(`
    SELECT accounts.id
    FROM ${accountRuntimeMutationTable(client, 'accounts')} accounts
    INNER JOIN ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
      ON group_accounts.account_id = accounts.id
      AND group_accounts.system_account_id = ?
      AND group_accounts.group_id = ?
      AND group_accounts.enabled = 1
      AND group_accounts.account_authorization_id = ?
    WHERE accounts.id = ?
      AND accounts.system_account_id = ?
      AND accounts.authorization_instance_authorization_id = ?
      AND accounts.deleted_at IS NULL
    LIMIT 1
  `, [target.systemAccountId, target.groupId, target.accountAuthorizationId, target.accountId, target.systemAccountId, target.accountAuthorizationId])
  return Boolean(row?.id)
}

export function getAccountPrecheckMutationState(input: {
  accountId: string
  authorizedBinding?: AuthorizedAccountBindingRuntimeTarget
}): AccountPrecheckMutationState | undefined {
  const target = input.authorizedBinding
    ? normalizedAuthorizedAccountBindingRuntimeTarget(input.authorizedBinding)
    : undefined
  if (target) {
    const row = getBusinessDatabase()
      .prepare(`
        SELECT
          accounts.status,
          accounts.dispatch_revision,
          accounts.updated_at,
          accounts.last_used_at,
          accounts.last_health_success_at
        FROM group_accounts
        INNER JOIN accounts ON accounts.id = group_accounts.account_id
        WHERE group_accounts.account_id = ?
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
          AND accounts.system_account_id = ?
          AND accounts.authorization_instance_authorization_id = ?
          AND accounts.deleted_at IS NULL
        LIMIT 1
      `)
      .get(target.accountId, target.systemAccountId, target.groupId, target.accountAuthorizationId, target.systemAccountId, target.accountAuthorizationId) as unknown as {
        status?: AccountStatus | null
        dispatch_revision?: number | bigint | string | null
        updated_at?: string | null
        last_used_at?: string | null
        last_health_success_at?: string | null
      } | undefined
    if (!row) {
      return undefined
    }
    return {
      status: normalizeAccountStatus(row.status),
      dispatchRevision: Number(row.dispatch_revision ?? 0),
      updatedAt: row.updated_at ?? undefined,
      lastUsedAt: row.last_used_at ?? undefined,
      lastHealthSuccessAt: row.last_health_success_at ?? undefined
    }
  }

  const row = getBusinessDatabase()
    .prepare('SELECT status, dispatch_revision, updated_at, last_used_at, last_health_success_at FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
    .get(input.accountId) as unknown as {
      status?: AccountStatus | null
      dispatch_revision?: number | bigint | string | null
      updated_at?: string | null
      last_used_at?: string | null
      last_health_success_at?: string | null
    } | undefined
  if (!row) {
    return undefined
  }
  return {
    status: normalizeAccountStatus(row.status),
    dispatchRevision: Number(row.dispatch_revision ?? 0),
    updatedAt: row.updated_at ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    lastHealthSuccessAt: row.last_health_success_at ?? undefined
  }
}

export async function getAccountPrecheckMutationStateAsync(input: {
  accountId: string
  authorizedBinding?: AuthorizedAccountBindingRuntimeTarget
}): Promise<AccountPrecheckMutationState | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAccountPrecheckMutationState(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const target = input.authorizedBinding
    ? normalizedAuthorizedAccountBindingRuntimeTarget(input.authorizedBinding)
    : undefined
  const row = target
    ? await client.one<{
      status?: AccountStatus | null
      dispatch_revision?: number | bigint | string | null
      updated_at?: string | null
      last_used_at?: string | null
      last_health_success_at?: string | null
    }>(`
      SELECT
        accounts.status,
        accounts.dispatch_revision,
        accounts.updated_at,
        accounts.last_used_at,
        accounts.last_health_success_at
      FROM ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
      INNER JOIN ${accountRuntimeMutationTable(client, 'accounts')} accounts ON accounts.id = group_accounts.account_id
      WHERE group_accounts.account_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.group_id = ?
        AND group_accounts.enabled = 1
        AND group_accounts.account_authorization_id = ?
        AND accounts.system_account_id = ?
        AND accounts.authorization_instance_authorization_id = ?
        AND accounts.deleted_at IS NULL
      LIMIT 1
    `, [target.accountId, target.systemAccountId, target.groupId, target.accountAuthorizationId, target.systemAccountId, target.accountAuthorizationId])
    : await client.one<{
      status?: AccountStatus | null
      dispatch_revision?: number | bigint | string | null
      updated_at?: string | null
      last_used_at?: string | null
      last_health_success_at?: string | null
    }>(`
      SELECT status, dispatch_revision, updated_at, last_used_at, last_health_success_at
      FROM ${accountRuntimeMutationTable(client, 'accounts')}
      WHERE id = ?
        AND deleted_at IS NULL
      LIMIT 1
    `, [input.accountId])
  if (!row) {
    return undefined
  }
  return {
    status: normalizeAccountStatus(row.status),
    dispatchRevision: Number(row.dispatch_revision ?? 0),
    updatedAt: row.updated_at ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    lastHealthSuccessAt: row.last_health_success_at ?? undefined
  }
}

interface AccountRuntimeSuccessObservationRow {
  status?: AccountStatus | null
  dispatch_revision?: number | bigint | string | null
  account_expires_at?: string | null
  cooldown_until?: string | null
  last_error_code?: string | null
  last_error_message?: string | null
  last_error_trace_id?: string | null
  cooldown_retest_failure_count?: number | bigint | string | null
  cooldown_retest_observation_started_at?: string | null
  cooldown_retest_last_at?: string | null
  cooldown_retest_last_status_code?: number | bigint | string | null
  stream_failure_count?: number | bigint | string | null
  stream_failure_window_started_at?: string | null
  last_health_success_at?: string | null
  updated_at?: string | null
}

export function recordAccountRuntimeSuccessObservation(
  input: AccountRuntimeSuccessObservationInput
): AccountRuntimeSuccessObservationResult {
  const observedAt = normalizedRuntimeObservationAt(input.observedAt)
  if (!observedAt) return { accepted: false, changed: false }
  const target = input.authorizedBinding
    ? normalizedAuthorizedAccountBindingRuntimeTarget(input.authorizedBinding)
    : undefined
  if (input.authorizedBinding && !target) return { accepted: false, changed: false }
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  let result: AccountRuntimeSuccessObservationResult = { accepted: false, changed: false }
  try {
    const row = target
      ? database.prepare(`
          SELECT accounts.status, accounts.dispatch_revision, accounts.account_expires_at,
            accounts.cooldown_until, accounts.last_error_code, accounts.last_error_message,
            accounts.last_error_trace_id, accounts.cooldown_retest_failure_count,
            accounts.cooldown_retest_observation_started_at, accounts.cooldown_retest_last_at,
            accounts.cooldown_retest_last_status_code, accounts.stream_failure_count,
            accounts.stream_failure_window_started_at, accounts.last_health_success_at,
            accounts.updated_at
          FROM group_accounts
          INNER JOIN accounts ON accounts.id = group_accounts.account_id
          WHERE group_accounts.account_id = ?
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
            AND accounts.system_account_id = ?
            AND accounts.authorization_instance_authorization_id = ?
            AND accounts.deleted_at IS NULL
          LIMIT 1
        `).get(target.accountId, target.systemAccountId, target.groupId, target.accountAuthorizationId, target.systemAccountId, target.accountAuthorizationId) as unknown as AccountRuntimeSuccessObservationRow | undefined
      : database.prepare(`
          SELECT status, dispatch_revision, account_expires_at, cooldown_until, last_error_code,
            last_error_message, last_error_trace_id, cooldown_retest_failure_count,
            cooldown_retest_observation_started_at, cooldown_retest_last_at,
            cooldown_retest_last_status_code, stream_failure_count,
            stream_failure_window_started_at, last_health_success_at, updated_at
          FROM accounts
          WHERE id = ? AND deleted_at IS NULL
          LIMIT 1
        `).get(input.accountId) as unknown as AccountRuntimeSuccessObservationRow | undefined
    result = applyAccountRuntimeSuccessObservationSync(database, input, observedAt, target, row)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  finalizeAccountRuntimeSuccessObservation(input, target, result)
  return result
}

export async function recordAccountRuntimeSuccessObservationAsync(
  input: AccountRuntimeSuccessObservationInput
): Promise<AccountRuntimeSuccessObservationResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountRuntimeSuccessObservation(input)
  }
  const observedAt = normalizedRuntimeObservationAt(input.observedAt)
  if (!observedAt) return { accepted: false, changed: false }
  const target = input.authorizedBinding
    ? normalizedAuthorizedAccountBindingRuntimeTarget(input.authorizedBinding)
    : undefined
  if (input.authorizedBinding && !target) return { accepted: false, changed: false }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.transaction(async (tx) => {
    const row = target
      ? await tx.one<AccountRuntimeSuccessObservationRow>(`
          SELECT accounts.status, accounts.dispatch_revision, accounts.account_expires_at,
            accounts.cooldown_until, accounts.last_error_code, accounts.last_error_message,
            accounts.last_error_trace_id, accounts.cooldown_retest_failure_count,
            accounts.cooldown_retest_observation_started_at, accounts.cooldown_retest_last_at,
            accounts.cooldown_retest_last_status_code, accounts.stream_failure_count,
            accounts.stream_failure_window_started_at, accounts.last_health_success_at,
            accounts.updated_at
          FROM ${accountRuntimeMutationTable(tx, 'group_accounts')} group_accounts
          INNER JOIN ${accountRuntimeMutationTable(tx, 'accounts')} accounts ON accounts.id = group_accounts.account_id
          WHERE group_accounts.account_id = ?
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
            AND accounts.system_account_id = ?
            AND accounts.authorization_instance_authorization_id = ?
            AND accounts.deleted_at IS NULL
          LIMIT 1
          FOR UPDATE
        `, [target.accountId, target.systemAccountId, target.groupId, target.accountAuthorizationId, target.systemAccountId, target.accountAuthorizationId])
      : await tx.one<AccountRuntimeSuccessObservationRow>(`
          SELECT status, dispatch_revision, account_expires_at, cooldown_until, last_error_code,
            last_error_message, last_error_trace_id, cooldown_retest_failure_count,
            cooldown_retest_observation_started_at, cooldown_retest_last_at,
            cooldown_retest_last_status_code, stream_failure_count,
            stream_failure_window_started_at, last_health_success_at, updated_at
          FROM ${accountRuntimeMutationTable(tx, 'accounts')}
          WHERE id = ? AND deleted_at IS NULL
          LIMIT 1
          FOR UPDATE
        `, [input.accountId])
    return await applyAccountRuntimeSuccessObservationAsync(tx, input, observedAt, target, row)
  })
  await finalizeAccountRuntimeSuccessObservationAsync(client, input, target, result)
  return result
}

function applyAccountRuntimeSuccessObservationSync(
  database: ReturnType<typeof getBusinessDatabase>,
  input: AccountRuntimeSuccessObservationInput,
  observedAt: string,
  target: Required<AuthorizedAccountBindingRuntimeTarget> | undefined,
  row: AccountRuntimeSuccessObservationRow | undefined
): AccountRuntimeSuccessObservationResult {
  const decision = accountRuntimeSuccessObservationDecision(input, observedAt, row)
  if (!decision.accepted || !row) return decision
  const statement = database.prepare(`
    UPDATE accounts
    SET status = CASE WHEN ? = 1 THEN 'active' ELSE status END,
        schedulable = CASE WHEN ? = 1 THEN 1 ELSE schedulable END,
        cooldown_until = CASE WHEN ? = 1 THEN NULL ELSE cooldown_until END,
        last_error_code = CASE WHEN ? = 1 THEN NULL ELSE last_error_code END,
        last_error_message = CASE WHEN ? = 1 THEN NULL ELSE last_error_message END,
        last_error_trace_id = CASE WHEN ? = 1 THEN NULL ELSE last_error_trace_id END,
        cooldown_retest_failure_count = CASE WHEN ? = 1 THEN 0 ELSE cooldown_retest_failure_count END,
        cooldown_retest_observation_started_at = CASE WHEN ? = 1 THEN NULL ELSE cooldown_retest_observation_started_at END,
        cooldown_retest_last_at = CASE WHEN ? = 1 THEN NULL ELSE cooldown_retest_last_at END,
        cooldown_retest_last_status_code = CASE WHEN ? = 1 THEN NULL ELSE cooldown_retest_last_status_code END,
        stream_failure_count = CASE WHEN ? = 1 THEN 0 ELSE stream_failure_count END,
        stream_failure_window_started_at = CASE WHEN ? = 1 THEN NULL ELSE stream_failure_window_started_at END,
        last_health_success_at = ?,
        updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END
    WHERE id = ? AND deleted_at IS NULL
      ${target ? 'AND system_account_id = ? AND authorization_instance_authorization_id = ?' : ''}
      ${input.expectedDispatchRevision ? 'AND dispatch_revision = ?' : ''}
      AND (last_health_success_at IS NULL OR last_health_success_at <= ?)
      ${target ? `AND EXISTS (
        SELECT 1 FROM group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
      )` : ''}
  `)
  const restore = decision.changed ? 1 : 0
  const params: Array<string | number> = [
    restore, restore, restore, restore, restore, restore,
    restore, restore, restore, restore, restore, restore,
    observedAt, observedAt, observedAt,
    input.accountId
  ]
  if (target) params.push(target.systemAccountId, target.accountAuthorizationId)
  if (input.expectedDispatchRevision) params.push(input.expectedDispatchRevision)
  params.push(observedAt)
  if (target) params.push(target.systemAccountId, target.groupId, target.accountAuthorizationId)
  const write = statement.run(...params)
  if (Number(write.changes ?? 0) <= 0) return { accepted: false, changed: false, accountStatus: decision.accountStatus }
  return decision
}

async function applyAccountRuntimeSuccessObservationAsync(
  client: DatabaseClient,
  input: AccountRuntimeSuccessObservationInput,
  observedAt: string,
  target: Required<AuthorizedAccountBindingRuntimeTarget> | undefined,
  row: AccountRuntimeSuccessObservationRow | undefined
): Promise<AccountRuntimeSuccessObservationResult> {
  const decision = accountRuntimeSuccessObservationDecision(input, observedAt, row)
  if (!decision.accepted || !row) return decision
  const restore = decision.changed ? 1 : 0
  const params: Array<string | number> = [
    restore, restore, restore, restore, restore, restore,
    restore, restore, restore, restore, restore, restore,
    observedAt, observedAt, observedAt,
    input.accountId
  ]
  if (target) params.push(target.systemAccountId, target.accountAuthorizationId)
  if (input.expectedDispatchRevision) params.push(input.expectedDispatchRevision)
  params.push(observedAt)
  if (target) params.push(target.systemAccountId, target.groupId, target.accountAuthorizationId)
  const write = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET status = CASE WHEN ? = 1 THEN 'active' ELSE status END,
        schedulable = CASE WHEN ? = 1 THEN 1 ELSE schedulable END,
        cooldown_until = CASE WHEN ? = 1 THEN NULL ELSE cooldown_until END,
        last_error_code = CASE WHEN ? = 1 THEN NULL ELSE last_error_code END,
        last_error_message = CASE WHEN ? = 1 THEN NULL ELSE last_error_message END,
        last_error_trace_id = CASE WHEN ? = 1 THEN NULL ELSE last_error_trace_id END,
        cooldown_retest_failure_count = CASE WHEN ? = 1 THEN 0 ELSE cooldown_retest_failure_count END,
        cooldown_retest_observation_started_at = CASE WHEN ? = 1 THEN NULL ELSE cooldown_retest_observation_started_at END,
        cooldown_retest_last_at = CASE WHEN ? = 1 THEN NULL ELSE cooldown_retest_last_at END,
        cooldown_retest_last_status_code = CASE WHEN ? = 1 THEN NULL ELSE cooldown_retest_last_status_code END,
        stream_failure_count = CASE WHEN ? = 1 THEN 0 ELSE stream_failure_count END,
        stream_failure_window_started_at = CASE WHEN ? = 1 THEN NULL ELSE stream_failure_window_started_at END,
        last_health_success_at = ?,
        updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END
    WHERE id = ? AND deleted_at IS NULL
      ${target ? 'AND system_account_id = ? AND authorization_instance_authorization_id = ?' : ''}
      ${input.expectedDispatchRevision ? 'AND dispatch_revision = ?' : ''}
      AND (last_health_success_at IS NULL OR last_health_success_at <= ?)
      ${target ? `AND EXISTS (
        SELECT 1 FROM ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
      )` : ''}
  `, params)
  if (Number(write.changes ?? 0) <= 0) return { accepted: false, changed: false, accountStatus: decision.accountStatus }
  return decision
}

function accountRuntimeSuccessObservationDecision(
  input: AccountRuntimeSuccessObservationInput,
  observedAt: string,
  row: AccountRuntimeSuccessObservationRow | undefined
): AccountRuntimeSuccessObservationResult {
  if (!row) return { accepted: false, changed: false }
  const status = normalizeAccountStatus(row.status)
  const dispatchRevision = Number(row.dispatch_revision ?? 0)
  if (input.expectedDispatchRevision && dispatchRevision !== input.expectedDispatchRevision) {
    return { accepted: false, changed: false, accountStatus: status }
  }
  if (row.last_health_success_at && row.last_health_success_at > observedAt) {
    return { accepted: false, changed: false, accountStatus: status }
  }
  const expired = isAccountExpired(row.account_expires_at, Date.parse(observedAt))
  const explicitPolicyCooldown = isExplicitAccountErrorPolicyCooldown(row.last_error_code, row.last_error_message)
  const observationCanRestoreCurrentState = !isCoolingAccountStatus(status)
    || !row.updated_at
    || row.updated_at <= observedAt
  const restoreAllowed = status !== 'disabled'
    && status !== 'error'
    && status !== 'pending_test'
    && !isCoolingAccountStatus(status)
    && !explicitPolicyCooldown
    && !expired
  const changed = restoreAllowed && observationCanRestoreCurrentState && (
    status !== 'active'
    || Boolean(row.cooldown_until)
    || Boolean(row.last_error_code)
    || Boolean(row.last_error_message)
    || Boolean(row.last_error_trace_id)
    || Number(row.cooldown_retest_failure_count ?? 0) > 0
    || Boolean(row.cooldown_retest_observation_started_at)
    || Boolean(row.cooldown_retest_last_at)
    || row.cooldown_retest_last_status_code !== null && row.cooldown_retest_last_status_code !== undefined
    || Number(row.stream_failure_count ?? 0) > 0
    || Boolean(row.stream_failure_window_started_at)
  )
  return { accepted: true, changed, accountStatus: changed ? 'active' : status }
}

function finalizeAccountRuntimeSuccessObservation(
  input: AccountRuntimeSuccessObservationInput,
  target: Required<AuthorizedAccountBindingRuntimeTarget> | undefined,
  result: AccountRuntimeSuccessObservationResult
): void {
  if (!result.accepted) return
  invalidateAccountLookupCache(input.accountId)
  if (!result.changed) return
  refreshGroupAccountStatsAfterWrite({
    accountIds: [input.accountId],
    ...(target ? { groupIds: [target.groupId] } : {}),
    reason: target ? 'authorized_account_runtime_success' : 'account_runtime_success'
  })
  invalidateGatewayRuntimeAfterBusinessWrite(target ? 'authorized_account_runtime_success' : 'account_runtime_success')
}

async function finalizeAccountRuntimeSuccessObservationAsync(
  client: DatabaseClient,
  input: AccountRuntimeSuccessObservationInput,
  target: Required<AuthorizedAccountBindingRuntimeTarget> | undefined,
  result: AccountRuntimeSuccessObservationResult
): Promise<void> {
  if (!result.accepted) return
  invalidateAccountLookupCache(input.accountId)
  if (!result.changed) return
  await refreshGroupAccountStatsAfterWriteAsync({
    accountIds: [input.accountId],
    ...(target ? { groupIds: [target.groupId] } : {}),
    reason: target ? 'authorized_account_runtime_success' : 'account_runtime_success'
  }, client)
  invalidateGatewayRuntimeAfterBusinessWrite(target ? 'authorized_account_runtime_success' : 'account_runtime_success')
}

function normalizedRuntimeObservationAt(value: string): string | undefined {
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : undefined
}

export function clearAuthorizedAccountBindingFailureStateByContext(
  input: AuthorizedAccountBindingRuntimeTarget,
  options: ClearAccountFailureStateOptions = {}
): AccountFailureStateClearResult {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return { changed: false }
  }
  const now = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'active',
          schedulable = 1,
          cooldown_until = NULL,
          last_error_code = NULL,
          last_error_message = NULL,
          last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND deleted_at IS NULL
        AND status <> 'disabled'
        AND (? = 1 OR status <> 'error')
        AND (? = 1 OR status <> 'pending_test')
        AND (? = 1 OR NOT (
          COALESCE(last_error_code, '') = ?
          OR (last_error_code IS NULL AND COALESCE(last_error_message, '') LIKE ?)
        ))
        AND (
          status <> 'active'
          OR schedulable <> 1
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NOT NULL
          OR last_error_trace_id IS NOT NULL
          OR cooldown_retest_failure_count > 0
          OR cooldown_retest_observation_started_at IS NOT NULL
          OR cooldown_retest_last_at IS NOT NULL
          OR cooldown_retest_last_status_code IS NOT NULL
          OR stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
        )
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(
      now,
      target.accountId,
      target.systemAccountId,
      target.accountAuthorizationId,
      options.allowErrorRestore === false ? 0 : 1,
      options.allowPendingTestRestore === true ? 1 : 0,
      options.allowExplicitPolicyRestore === true ? 1 : 0,
      EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE,
      `${LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX}%`,
      target.systemAccountId,
      target.groupId,
      target.accountAuthorizationId
    )
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_restored' })
    invalidateAccountLookupCache(target.accountId)
    invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_restored')
  }
  return {
    account: findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' }),
    changed
  }
}

export function markAuthorizedAccountBindingTemporaryUnavailableByContext(
  input: AuthorizedAccountBindingRuntimeTarget & {
    reason: string
    traceId?: string
    healthCheckGuard?: AccountHealthCheckMutationGuard
    precheckGuard?: AccountPrecheckMutationGuard
  }
): AccountSummary | undefined {
  return markAuthorizedAccountBindingCooldownByContext({
    ...input,
    status: 'temporary_unavailable'
  })
}

export async function markAuthorizedAccountBindingTemporaryUnavailableByContextAsync(
  input: AuthorizedAccountBindingRuntimeTarget & {
    reason: string
    traceId?: string
    healthCheckGuard?: AccountHealthCheckMutationGuard
    precheckGuard?: AccountPrecheckMutationGuard
  }
): Promise<AccountSummary | undefined> {
  return markAuthorizedAccountBindingCooldownByContextAsync({
    ...input,
    status: 'temporary_unavailable'
  })
}

export function markAuthorizedAccountBindingCooldownByContext(
  input: AuthorizedAccountBindingRuntimeTarget & {
    cooldownUntil?: string
    reason: string
    traceId?: string
    status?: AccountStatus
    failureCode?: string
    healthCheckGuard?: AccountHealthCheckMutationGuard
    precheckGuard?: AccountPrecheckMutationGuard
    runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
  }
): AccountSummary | undefined {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return undefined
  }
  const cooldownStatus: AccountStatus = input.status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownNowMs = Date.now()
  const temporaryState = cooldownStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(cooldownNowMs)
    : undefined
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? temporaryState!.cooldownUntil
    : input.cooldownUntil ?? initialCooldownUntilForStatus(cooldownStatus, cooldownNowMs) ?? new Date(cooldownNowMs + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
  const observationStartedAt = temporaryState?.observationStartedAt
    ?? cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)
  const cooldownGeneration = newCooldownRetestGeneration()
  const now = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = 1,
          cooldown_until = ?,
          last_error_code = ?,
          last_error_message = ?,
          last_error_trace_id = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_generation = ?,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ${accountRuntimeFailureUpdatedAtSql(input.runtimeFailureGuard)}
      WHERE id = ?
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND deleted_at IS NULL
        AND status NOT IN ('disabled', 'error')
        ${accountHealthCheckGuardSql(input.healthCheckGuard)}
        ${accountPrecheckMutationGuardSql(input.precheckGuard)}
        ${accountRuntimeFailureObservationGuardSql(input.runtimeFailureGuard)}
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(
      cooldownStatus,
      cooldownUntil,
      input.failureCode?.trim().slice(0, 120) || null,
      input.reason || null,
      normalizedLastErrorTraceId(input.traceId),
      observationStartedAt ?? null,
      cooldownGeneration,
      ...accountRuntimeFailureUpdatedAtParams(input.runtimeFailureGuard, now),
      target.accountId,
      target.systemAccountId,
      target.accountAuthorizationId,
      ...accountHealthCheckGuardParams(input.healthCheckGuard),
      ...accountPrecheckMutationGuardParams(input.precheckGuard),
      ...accountRuntimeFailureObservationGuardParams(input.runtimeFailureGuard),
      target.systemAccountId,
      target.groupId,
      target.accountAuthorizationId
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_cooldown' })
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_cooldown')
  return findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
}

export async function markAuthorizedAccountBindingCooldownByContextAsync(
  input: AuthorizedAccountBindingRuntimeTarget & {
    cooldownUntil?: string
    reason: string
    traceId?: string
    status?: AccountStatus
    failureCode?: string
    healthCheckGuard?: AccountHealthCheckMutationGuard
    precheckGuard?: AccountPrecheckMutationGuard
    runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
  }
): Promise<AccountSummary | undefined> {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return undefined
  }
  const cooldownStatus: AccountStatus = input.status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownNowMs = Date.now()
  const temporaryState = cooldownStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(cooldownNowMs)
    : undefined
  let cooldownUntil: string
  if (cooldownStatus === 'temporary_unavailable') {
    cooldownUntil = temporaryState!.cooldownUntil
  } else {
    cooldownUntil = input.cooldownUntil
      ?? await initialCooldownUntilForStatusAsync(cooldownStatus, cooldownNowMs)
      ?? new Date(cooldownNowMs + (await defaultTemporaryUnschedulableMinutesAsync()) * 60_000).toISOString()
  }
  const observationStartedAt = temporaryState?.observationStartedAt
    ?? cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)
  const cooldownGeneration = newCooldownRetestGeneration()
  const now = nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')} AS accounts
    SET status = ?,
        schedulable = 1,
        cooldown_until = ?,
        last_error_code = ?,
        last_error_message = ?,
        last_error_trace_id = ?,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = ?,
        cooldown_retest_generation = ?,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ${accountRuntimeFailureUpdatedAtSql(input.runtimeFailureGuard)}
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id = ?
      AND deleted_at IS NULL
      AND status NOT IN ('disabled', 'error')
      ${accountHealthCheckGuardSql(input.healthCheckGuard)}
      ${accountPrecheckMutationGuardSql(input.precheckGuard)}
      ${accountRuntimeFailureObservationGuardSql(input.runtimeFailureGuard)}
      AND EXISTS (
        SELECT 1
        FROM ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
      )
  `, [
    cooldownStatus,
    cooldownUntil,
    input.failureCode?.trim().slice(0, 120) || null,
    input.reason || null,
    normalizedLastErrorTraceId(input.traceId),
    observationStartedAt ?? null,
    cooldownGeneration,
    ...accountRuntimeFailureUpdatedAtParams(input.runtimeFailureGuard, now),
    target.accountId,
    target.systemAccountId,
    target.accountAuthorizationId,
    ...accountHealthCheckGuardParams(input.healthCheckGuard),
    ...accountPrecheckMutationGuardParams(input.precheckGuard),
    ...accountRuntimeFailureObservationGuardParams(input.runtimeFailureGuard),
    target.systemAccountId,
    target.groupId,
    target.accountAuthorizationId
  ])
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  await refreshGroupAccountStatsAfterWriteAsync({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_cooldown' }, client)
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_cooldown')
  return findAccountSummaryAsync(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
}

export function markAuthorizedAccountBindingDisabledByFailure(
  input: AuthorizedAccountBindingRuntimeTarget & {
    reason: string
    runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
  }
): AccountSummary | undefined {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return undefined
  }
  const now = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = 'upstream_failure',
          last_error_message = ?,
          last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ${accountRuntimeFailureUpdatedAtSql(input.runtimeFailureGuard)}
      WHERE id = ?
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND deleted_at IS NULL
        AND status <> 'disabled'
        ${accountRuntimeFailureObservationGuardSql(input.runtimeFailureGuard)}
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(
      input.reason || null,
      ...accountRuntimeFailureUpdatedAtParams(input.runtimeFailureGuard, now),
      target.accountId,
      target.systemAccountId,
      target.accountAuthorizationId,
      ...accountRuntimeFailureObservationGuardParams(input.runtimeFailureGuard),
      target.systemAccountId,
      target.groupId,
      target.accountAuthorizationId
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_exception' })
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_exception')
  return findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
}

export async function markAuthorizedAccountBindingDisabledByFailureAsync(
  input: AuthorizedAccountBindingRuntimeTarget & {
    reason: string
    runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
  }
): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return markAuthorizedAccountBindingDisabledByFailure(input)
  }
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return undefined
  }
  const now = nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')} AS accounts
    SET status = 'error',
        schedulable = 0,
        cooldown_until = NULL,
        last_error_code = 'upstream_failure',
        last_error_message = ?,
        last_error_trace_id = NULL,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ${accountRuntimeFailureUpdatedAtSql(input.runtimeFailureGuard)}
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id = ?
      AND deleted_at IS NULL
      AND status <> 'disabled'
      ${accountRuntimeFailureObservationGuardSql(input.runtimeFailureGuard)}
      AND EXISTS (
        SELECT 1
        FROM ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
      )
  `, [
    input.reason || null,
    ...accountRuntimeFailureUpdatedAtParams(input.runtimeFailureGuard, now),
    target.accountId,
    target.systemAccountId,
    target.accountAuthorizationId,
    ...accountRuntimeFailureObservationGuardParams(input.runtimeFailureGuard),
    target.systemAccountId,
    target.groupId,
    target.accountAuthorizationId
  ])
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  await refreshGroupAccountStatsAfterWriteAsync({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_exception' }, client)
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_exception')
  return findAccountSummaryAsync(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
}

export function clearAuthorizedAccountBindingStreamFailureState(input: AuthorizedAccountBindingRuntimeTarget): boolean {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return false
  }
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          last_error_code = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_code
          END,
          last_error_message = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_message
          END,
          last_error_trace_id = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_trace_id
          END,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND deleted_at IS NULL
        AND status NOT IN ('disabled', 'error')
        AND (
          stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
          OR (status = 'active' AND last_error_code IS NOT NULL)
          OR (status = 'active' AND last_error_message IS NOT NULL)
          OR (status = 'active' AND last_error_trace_id IS NOT NULL)
        )
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(nowIso(), target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateAccountLookupCache(target.accountId)
    invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_stream_failure_cleared')
  }
  return changed
}

export async function clearAuthorizedAccountBindingStreamFailureStateAsync(input: AuthorizedAccountBindingRuntimeTarget): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return clearAuthorizedAccountBindingStreamFailureState(input)
  }
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return false
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')} AS accounts
    SET stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        last_error_code = CASE
          WHEN status = 'active' THEN NULL
          ELSE last_error_code
        END,
        last_error_message = CASE
          WHEN status = 'active' THEN NULL
          ELSE last_error_message
        END,
        last_error_trace_id = CASE
          WHEN status = 'active' THEN NULL
          ELSE last_error_trace_id
        END,
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id = ?
      AND deleted_at IS NULL
      AND status NOT IN ('disabled', 'error')
      AND (
        stream_failure_count > 0
        OR stream_failure_window_started_at IS NOT NULL
        OR (status = 'active' AND last_error_code IS NOT NULL)
        OR (status = 'active' AND last_error_message IS NOT NULL)
        OR (status = 'active' AND last_error_trace_id IS NOT NULL)
      )
      AND EXISTS (
        SELECT 1
        FROM ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
      )
  `, [nowIso(), target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId])
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateAccountLookupCache(target.accountId)
    invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_stream_failure_cleared')
  }
  return changed
}

export function clearAccountStreamFailureState(id: string): boolean {
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          last_error_code = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_code
          END,
          last_error_message = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_message
          END,
          last_error_trace_id = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_trace_id
          END,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND status NOT IN ('disabled', 'error')
        AND (
          stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
          OR (status = 'active' AND last_error_code IS NOT NULL)
          OR (status = 'active' AND last_error_message IS NOT NULL)
          OR (status = 'active' AND last_error_trace_id IS NOT NULL)
        )
    `)
    .run(nowIso(), id)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateGatewayRuntimeAfterBusinessWrite('account_stream_failure_cleared')
  }
  return changed
}

export async function clearAccountStreamFailureStateAsync(id: string): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return clearAccountStreamFailureState(id)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        last_error_code = CASE
          WHEN status = 'active' THEN NULL
          ELSE last_error_code
        END,
        last_error_message = CASE
          WHEN status = 'active' THEN NULL
          ELSE last_error_message
        END,
        last_error_trace_id = CASE
          WHEN status = 'active' THEN NULL
          ELSE last_error_trace_id
        END,
        updated_at = ?
    WHERE id = ?
      AND deleted_at IS NULL
      AND status NOT IN ('disabled', 'error')
      AND (
        stream_failure_count > 0
        OR stream_failure_window_started_at IS NOT NULL
        OR (status = 'active' AND last_error_code IS NOT NULL)
        OR (status = 'active' AND last_error_message IS NOT NULL)
        OR (status = 'active' AND last_error_trace_id IS NOT NULL)
      )
  `, [nowIso(), id])
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateGatewayRuntimeAfterBusinessWrite('account_stream_failure_cleared')
  }
  return changed
}

export function markAccountTestTemporaryUnavailable(
  account: AccountSummary,
  reason: string,
  access?: AccessScope,
  healthCheckGuard?: AccountHealthCheckMutationGuard,
  traceId?: string
): AccountSummary | undefined {
  const current = findAccountSummary(account.id, access)
  if (!current || (current.status !== 'active' && !isCoolingAccountStatus(current.status))) {
    return undefined
  }
  if (current.status === 'active' && !current.schedulable) {
    return undefined
  }
  const message = reason.slice(0, 1000)
  if (current.accessType === 'authorized') {
    return markAuthorizedAccountBindingTemporaryUnavailable(current, message, access, healthCheckGuard, traceId)
  }
  return markAccountTemporaryUnavailable(current.id, message, healthCheckGuard, traceId)
}

interface AccountHealthCheckMutationGuard {
  configRevision: number
  checkedAt: string
  failureCount: number
  observedAt: string
}

export async function markAccountTestTemporaryUnavailableAsync(
  account: AccountSummary,
  reason: string,
  access?: AccessScope,
  healthCheckGuard?: AccountHealthCheckMutationGuard,
  traceId?: string
): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return markAccountTestTemporaryUnavailable(account, reason, access, healthCheckGuard, traceId)
  }
  const current = await findAccountSummaryAsync(account.id, access)
  if (!current || (current.status !== 'active' && !isCoolingAccountStatus(current.status))) {
    return undefined
  }
  if (current.status === 'active' && !current.schedulable) {
    return undefined
  }
  const message = reason.slice(0, 1000)
  if (current.accessType === 'authorized') {
    if (!current.boundGroupId || !current.accountAuthorizationId) {
      return undefined
    }
    return markAuthorizedAccountBindingTemporaryUnavailableByContextAsync({
      accountId: current.id,
      systemAccountId: authorizedBindingSystemAccountId(access),
      groupId: current.boundGroupId,
      accountAuthorizationId: current.accountAuthorizationId,
      reason: message,
      traceId,
      healthCheckGuard
    })
  }
  return markAccountTemporaryUnavailableAsync(current.id, message, healthCheckGuard, traceId)
}

function markAuthorizedAccountBindingTemporaryUnavailable(
  account: AccountSummary,
  reason: string,
  access?: AccessScope,
  healthCheckGuard?: AccountHealthCheckMutationGuard,
  traceId?: string
): AccountSummary | undefined {
  if (!account.boundGroupId || !account.accountAuthorizationId) {
    return undefined
  }
  const systemAccountId = authorizedBindingSystemAccountId(access)
  return markAuthorizedAccountBindingTemporaryUnavailableByContext({
    accountId: account.id,
    systemAccountId,
    groupId: account.boundGroupId,
    accountAuthorizationId: account.accountAuthorizationId,
    reason,
    traceId,
    healthCheckGuard
  })
}

function accountHealthCheckGuardSql(guard: AccountHealthCheckMutationGuard | undefined): string {
  if (!guard) return ''
  return `
    AND config_revision = ?
    AND last_health_check_at = ?
    AND health_check_failure_count = ?
    AND (last_health_success_at IS NULL OR last_health_success_at < ?)
  `
}

function accountHealthCheckGuardParams(guard: AccountHealthCheckMutationGuard | undefined): Array<string | number> {
  if (!guard) return []
  return [
    Math.max(1, Math.trunc(guard.configRevision)),
    guard.checkedAt,
    Math.max(0, Math.trunc(guard.failureCount)),
    guard.observedAt
  ]
}

function accountPrecheckMutationGuardSql(guard: AccountPrecheckMutationGuard | undefined): string {
  if (!guard) return ''
  return `
    AND dispatch_revision = ?
    AND status = ?
    AND (last_health_success_at IS NULL OR last_health_success_at <= ?)
  `
}

function accountPrecheckMutationGuardParams(guard: AccountPrecheckMutationGuard | undefined): Array<string | number> {
  if (!guard) return []
  return [
    Math.max(0, Math.trunc(guard.expectedDispatchRevision)),
    guard.expectedStatus,
    guard.precheckStartedAt
  ]
}

function accountRuntimeFailureObservationGuardSql(guard: AccountRuntimeFailureObservationGuard | undefined): string {
  if (!guard) return ''
  return `
    AND dispatch_revision = ?
    AND (last_health_success_at IS NULL OR last_health_success_at < ?)
    AND (updated_at IS NULL OR updated_at <= ?)
  `
}

function accountRuntimeFailureObservationGuardParams(guard: AccountRuntimeFailureObservationGuard | undefined): Array<string | number> {
  if (!guard) return []
  return [
    Math.max(1, Math.trunc(guard.expectedDispatchRevision)),
    guard.observedAt,
    guard.observedAt
  ]
}

function accountRuntimeFailureUpdatedAtSql(guard: AccountRuntimeFailureObservationGuard | undefined): string {
  return guard
    ? 'CASE WHEN updated_at IS NULL OR updated_at < ? THEN ? ELSE updated_at END'
    : '?'
}

function accountRuntimeFailureUpdatedAtParams(
  guard: AccountRuntimeFailureObservationGuard | undefined,
  fallback: string
): string[] {
  return guard ? [guard.observedAt, guard.observedAt] : [fallback]
}

export function markAccountTemporaryUnavailable(
  id: string,
  reason: string,
  healthCheckGuard?: AccountHealthCheckMutationGuard,
  traceId?: string,
  precheckGuard?: AccountPrecheckMutationGuard,
  runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
): AccountSummary | undefined {
  return markAccountCooldown(id, undefined, reason, 'temporary_unavailable', healthCheckGuard, traceId, precheckGuard, runtimeFailureGuard)
}

export async function markAccountTemporaryUnavailableAsync(
  id: string,
  reason: string,
  healthCheckGuard?: AccountHealthCheckMutationGuard,
  traceId?: string,
  precheckGuard?: AccountPrecheckMutationGuard,
  runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
): Promise<AccountSummary | undefined> {
  return markAccountCooldownAsync(id, undefined, reason, 'temporary_unavailable', healthCheckGuard, traceId, precheckGuard, runtimeFailureGuard)
}

export function markAccountCooldown(
  id: string,
  until: string | undefined,
  reason: string,
  status: AccountStatus = 'temporary_unavailable',
  healthCheckGuard?: AccountHealthCheckMutationGuard,
  traceId?: string,
  precheckGuard?: AccountPrecheckMutationGuard,
  runtimeFailureGuard?: AccountRuntimeFailureObservationGuard,
  failureCode?: string
): AccountSummary | undefined {
  const current = findInternalAccountSummary(id)
  if (!current) {
    return undefined
  }
  if (isHardUnavailableAccountStatus(current.status)) {
    return undefined
  }

  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    const result = getBusinessDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = 'account_expired',
            last_error_message = ?,
            last_error_trace_id = NULL,
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND deleted_at IS NULL
          AND status = ?
          AND config_revision = ?
          ${accountHealthCheckGuardSql(healthCheckGuard)}
          ${accountPrecheckMutationGuardSql(precheckGuard)}
          ${accountRuntimeFailureObservationGuardSql(runtimeFailureGuard)}
      `)
      .run(
        '账户套餐已过期，已自动停用',
        nowIso(),
        id,
        current.status,
        current.configRevision ?? 1,
        ...accountHealthCheckGuardParams(healthCheckGuard),
        ...accountPrecheckMutationGuardParams(precheckGuard),
        ...accountRuntimeFailureObservationGuardParams(runtimeFailureGuard)
      )
    if (Number(result.changes ?? 0) <= 0) {
      return undefined
    }
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_expired' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
    return findInternalAccountSummary(id)
  }

  const cooldownStatus: AccountStatus = status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownNowMs = Date.now()
  const cooldownNow = new Date(cooldownNowMs).toISOString()
  const temporaryState = cooldownStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(cooldownNowMs)
    : undefined
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? temporaryState!.cooldownUntil
    : until ?? initialCooldownUntilForStatus(cooldownStatus, cooldownNowMs) ?? new Date(cooldownNowMs + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
  const cooldownObservationStartedAt = temporaryState?.observationStartedAt
    ?? cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)
  const cooldownGeneration = newCooldownRetestGeneration()

  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = 1,
          cooldown_until = ?,
          last_error_code = ?,
          last_error_message = ?,
          last_error_trace_id = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_generation = ?,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ${accountRuntimeFailureUpdatedAtSql(runtimeFailureGuard)}
      WHERE id = ?
        AND deleted_at IS NULL
        AND status = ?
        AND config_revision = ?
        ${accountHealthCheckGuardSql(healthCheckGuard)}
        ${accountPrecheckMutationGuardSql(precheckGuard)}
        ${accountRuntimeFailureObservationGuardSql(runtimeFailureGuard)}
    `)
    .run(
      cooldownStatus,
      cooldownUntil,
      failureCode?.trim().slice(0, 120) || null,
      reason || null,
      normalizedLastErrorTraceId(traceId),
      cooldownObservationStartedAt ?? null,
      cooldownGeneration,
      ...accountRuntimeFailureUpdatedAtParams(runtimeFailureGuard, cooldownNow),
      id,
      current.status,
      current.configRevision ?? 1,
      ...accountHealthCheckGuardParams(healthCheckGuard),
      ...accountPrecheckMutationGuardParams(precheckGuard),
      ...accountRuntimeFailureObservationGuardParams(runtimeFailureGuard)
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown' })
  invalidateAccountLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown')

  return findInternalAccountSummary(id)
}

export async function markAccountCooldownAsync(
  id: string,
  until: string | undefined,
  reason: string,
  status: AccountStatus = 'temporary_unavailable',
  healthCheckGuard?: AccountHealthCheckMutationGuard,
  traceId?: string,
  precheckGuard?: AccountPrecheckMutationGuard,
  runtimeFailureGuard?: AccountRuntimeFailureObservationGuard,
  failureCode?: string
): Promise<AccountSummary | undefined> {
  const current = await findAccountSummaryAsync(id, internalAccountReadAccess)
  if (!current) {
    return undefined
  }
  if (isHardUnavailableAccountStatus(current.status)) {
    return undefined
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    const result = await client.execute(`
      UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = 'account_expired',
          last_error_message = ?,
          last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND status = ?
        AND config_revision = ?
        ${accountHealthCheckGuardSql(healthCheckGuard)}
        ${accountPrecheckMutationGuardSql(precheckGuard)}
        ${accountRuntimeFailureObservationGuardSql(runtimeFailureGuard)}
    `, [
      '账户套餐已过期，已自动停用',
      nowIso(),
      id,
      current.status,
      current.configRevision ?? 1,
      ...accountHealthCheckGuardParams(healthCheckGuard),
      ...accountPrecheckMutationGuardParams(precheckGuard),
      ...accountRuntimeFailureObservationGuardParams(runtimeFailureGuard)
    ])
    if (Number(result.changes ?? 0) <= 0) {
      return undefined
    }
    await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_expired' }, client)
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
    return findAccountSummaryAsync(id, internalAccountReadAccess)
  }

  const cooldownStatus: AccountStatus = status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownNowMs = Date.now()
  const cooldownNow = new Date(cooldownNowMs).toISOString()
  const temporaryState = cooldownStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(cooldownNowMs)
    : undefined
  const defaultCooldownMinutes = cooldownStatus === 'rate_limited'
    ? await defaultTemporaryUnschedulableMinutesAsync()
    : undefined
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? temporaryState!.cooldownUntil
    : until
      ?? await initialCooldownUntilForStatusAsync(cooldownStatus, cooldownNowMs)
      ?? new Date(cooldownNowMs + (defaultCooldownMinutes ?? 1) * 60_000).toISOString()
  const cooldownObservationStartedAt = temporaryState?.observationStartedAt
    ?? cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)
  const cooldownGeneration = newCooldownRetestGeneration()

  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET status = ?,
        schedulable = 1,
        cooldown_until = ?,
        last_error_code = ?,
        last_error_message = ?,
        last_error_trace_id = ?,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = ?,
        cooldown_retest_generation = ?,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ${accountRuntimeFailureUpdatedAtSql(runtimeFailureGuard)}
    WHERE id = ?
      AND deleted_at IS NULL
      AND status = ?
      AND config_revision = ?
      ${accountHealthCheckGuardSql(healthCheckGuard)}
      ${accountPrecheckMutationGuardSql(precheckGuard)}
      ${accountRuntimeFailureObservationGuardSql(runtimeFailureGuard)}
  `, [
    cooldownStatus,
    cooldownUntil,
    failureCode?.trim().slice(0, 120) || null,
    reason || null,
    normalizedLastErrorTraceId(traceId),
    cooldownObservationStartedAt ?? null,
    cooldownGeneration,
    ...accountRuntimeFailureUpdatedAtParams(runtimeFailureGuard, cooldownNow),
    id,
    current.status,
    current.configRevision ?? 1,
    ...accountHealthCheckGuardParams(healthCheckGuard),
    ...accountPrecheckMutationGuardParams(precheckGuard),
    ...accountRuntimeFailureObservationGuardParams(runtimeFailureGuard)
  ])
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_cooldown' }, client)
  invalidateAccountLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown')

  return findAccountSummaryAsync(id, internalAccountReadAccess)
}

export function migrateAccountTraffic(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): { sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string; groupId?: string } | undefined {
  const sourceVisibleAccount = findAccountSummary(input.sourceAccountId, access)
  if (sourceVisibleAccount?.accessType === 'authorized') {
    return migrateAuthorizedAccountBindingTraffic(input, access)
  }
  if (input.sourceAccountId === input.targetAccountId) {
    throw new Error('目标账户不能和当前账户相同')
  }

  const sourceRow = accountRowForManage(input.sourceAccountId, access)
  if (!sourceRow) {
    return undefined
  }
  const targetRow = accountRowForManage(input.targetAccountId, access)
  if (!targetRow) {
    return undefined
  }
  if (sourceRow.system_account_id !== targetRow.system_account_id) {
    throw new Error('目标账户必须和当前账户归属同一个系统账户')
  }
  if (sourceRow.provider_code !== targetRow.provider_code) {
    throw new Error('目标账户必须和当前账户属于同一个供应商')
  }
  const sourceGroupId = accountEnabledGroupId(sourceRow.id, sourceRow.system_account_id)
  const targetGroupId = accountEnabledGroupId(targetRow.id, targetRow.system_account_id)
  if (!sourceGroupId || !targetGroupId || sourceGroupId !== targetGroupId) {
    throw new Error('目标账户必须和当前账户在同一个分组内')
  }
  const targetCooldownUntil = targetRow.cooldown_until ?? undefined
  if (targetRow.status !== 'active' || targetRow.schedulable !== 1 || isAccountExpired(targetRow.account_expires_at) || isLaterIso(targetCooldownUntil, nowIso())) {
    throw new Error('目标账户当前不可调度，请选择正常可用的账户')
  }

  const ownerAccess = { systemAccountId: sourceRow.system_account_id, role: 'user' as const }
  if (input.sourceStatus === 'unchanged') {
    const sourceAccount = findAccountSummary(input.sourceAccountId, ownerAccess)
    const targetAccount = findAccountSummary(input.targetAccountId, ownerAccess)
    return sourceAccount && targetAccount
      ? { sourceAccount, targetAccount, groupId: sourceGroupId }
      : undefined
  }

  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
  const reason = manualTrafficMigrationReason
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(nowMs)
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
  const sourceObservationStartedAt = sourceTemporaryState?.observationStartedAt ?? null
  const sourceCooldownGeneration = sourceTemporaryState ? newCooldownRetestGeneration() : null
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const updateResult = input.sourceStatus === 'disabled'
      ? database
        .prepare(`
          UPDATE accounts
          SET status = 'disabled',
              schedulable = 0,
              cooldown_until = NULL,
              last_error_code = NULL,
              last_error_message = ?,
              last_error_trace_id = NULL,
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = NULL,
              cooldown_retest_generation = NULL,
              cooldown_retest_last_at = NULL,
              cooldown_retest_last_status_code = NULL,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ? AND system_account_id = ?
        `)
        .run(reason, now, sourceRow.id, sourceRow.system_account_id)
      : database
        .prepare(`
          UPDATE accounts
          SET status = 'temporary_unavailable',
              cooldown_until = ?,
              last_error_code = NULL,
              last_error_message = ?,
              last_error_trace_id = NULL,
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = ?,
              cooldown_retest_generation = ?,
              cooldown_retest_last_at = NULL,
              cooldown_retest_last_status_code = NULL,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ? AND system_account_id = ?
        `)
        .run(sourceCooldownUntil, reason, sourceObservationStartedAt ?? null, sourceCooldownGeneration, now, sourceRow.id, sourceRow.system_account_id)
    if (Number(updateResult.changes ?? 0) <= 0) {
      rollbackDatabaseTransaction(database, transactionStarted)
      return undefined
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ accountIds: [sourceRow.id], reason: 'traffic_migration' })
  invalidateGatewayRuntimeAfterBusinessWrite('traffic_migration')

  const sourceAccount = findAccountSummary(input.sourceAccountId, ownerAccess)
  const targetAccount = findAccountSummary(input.targetAccountId, ownerAccess)
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  return { sourceAccount, targetAccount, sourceCooldownUntil: sourceCooldownUntil ?? undefined, groupId: sourceGroupId }
}

export async function migrateAccountTrafficAsync(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): Promise<{ sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string; groupId?: string } | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return migrateAccountTraffic(input, access)
  }

  const sourceVisibleAccount = await findAccountSummaryAsync(input.sourceAccountId, access)
  if (sourceVisibleAccount?.accessType === 'authorized') {
    return migrateAuthorizedAccountBindingTrafficAsync(input, access)
  }
  if (input.sourceAccountId === input.targetAccountId) {
    throw new Error('目标账户不能和当前账户相同')
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const sourceRow = await accountRowForManageAsync(client, input.sourceAccountId, access)
  if (!sourceRow) {
    return undefined
  }
  const targetRow = await accountRowForManageAsync(client, input.targetAccountId, access)
  if (!targetRow) {
    return undefined
  }
  if (sourceRow.system_account_id !== targetRow.system_account_id) {
    throw new Error('目标账户必须和当前账户归属同一个系统账户')
  }
  if (sourceRow.provider_code !== targetRow.provider_code) {
    throw new Error('目标账户必须和当前账户属于同一个供应商')
  }
  const sourceGroupId = await accountEnabledGroupIdForClientAsync(client, sourceRow.id, sourceRow.system_account_id)
  const targetGroupId = await accountEnabledGroupIdForClientAsync(client, targetRow.id, targetRow.system_account_id)
  if (!sourceGroupId || !targetGroupId || sourceGroupId !== targetGroupId) {
    throw new Error('目标账户必须和当前账户在同一个分组内')
  }
  const targetCooldownUntil = targetRow.cooldown_until ?? undefined
  if (targetRow.status !== 'active' || targetRow.schedulable !== 1 || isAccountExpired(targetRow.account_expires_at) || isLaterIso(targetCooldownUntil, nowIso())) {
    throw new Error('目标账户当前不可调度，请选择正常可用的账户')
  }

  const ownerAccess = { systemAccountId: sourceRow.system_account_id, role: 'user' as const }
  if (input.sourceStatus === 'unchanged') {
    const sourceAccount = await findAccountSummaryAsync(input.sourceAccountId, ownerAccess)
    const targetAccount = await findAccountSummaryAsync(input.targetAccountId, ownerAccess)
    return sourceAccount && targetAccount
      ? { sourceAccount, targetAccount, groupId: sourceGroupId }
      : undefined
  }

  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
  const reason = manualTrafficMigrationReason
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(nowMs)
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
  const sourceObservationStartedAt = sourceTemporaryState?.observationStartedAt ?? null
  const sourceCooldownGeneration = sourceTemporaryState ? newCooldownRetestGeneration() : null
  const changed = await client.transaction(async (tx) => {
    const result = input.sourceStatus === 'disabled'
      ? await tx.execute(`
          UPDATE ${accountRuntimeMutationTable(tx, 'accounts')}
          SET status = 'disabled',
              schedulable = 0,
              cooldown_until = NULL,
              last_error_code = NULL,
              last_error_message = ?,
              last_error_trace_id = NULL,
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = NULL,
              cooldown_retest_generation = NULL,
              cooldown_retest_last_at = NULL,
              cooldown_retest_last_status_code = NULL,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ?
            AND system_account_id = ?
        `, [reason, now, sourceRow.id, sourceRow.system_account_id])
      : await tx.execute(`
          UPDATE ${accountRuntimeMutationTable(tx, 'accounts')}
          SET status = 'temporary_unavailable',
              cooldown_until = ?,
              last_error_code = NULL,
              last_error_message = ?,
              last_error_trace_id = NULL,
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = ?,
              cooldown_retest_generation = ?,
              cooldown_retest_last_at = NULL,
              cooldown_retest_last_status_code = NULL,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ?
            AND system_account_id = ?
        `, [sourceCooldownUntil, reason, sourceObservationStartedAt ?? null, sourceCooldownGeneration, now, sourceRow.id, sourceRow.system_account_id])
    return Number(result.changes ?? 0) > 0
  })
  if (!changed) {
    return undefined
  }
  await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [sourceRow.id], reason: 'traffic_migration' }, client)
  invalidateGatewayRuntimeAfterBusinessWrite('traffic_migration')

  const sourceAccount = await findAccountSummaryAsync(input.sourceAccountId, ownerAccess)
  const targetAccount = await findAccountSummaryAsync(input.targetAccountId, ownerAccess)
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  return { sourceAccount, targetAccount, sourceCooldownUntil: sourceCooldownUntil ?? undefined, groupId: sourceGroupId }
}

export function updateAuthorizedAccountBindingDispatch(
  accountId: string,
  input: { status?: 'active' | 'disabled'; priority?: number; superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean },
  access?: AccessScope
): AccountSummary | undefined {
  const systemAccountId = authorizedBindingSystemAccountId(access)
  const current = findAccountSummary(accountId, access)
  if (current?.accessType !== 'authorized' || !current.boundGroupId || !current.accountAuthorizationId) {
    throw new Error('授权账户需要先绑定到你的分组')
  }
  const enablingDispatchFlag = input.superPriorityEnabled === true || input.fallbackEnabled === true
  if (enablingDispatchFlag) {
    const unavailableMessage = accountDispatchUnavailableMessage(current, { requireAuthorizedBinding: true })
    if (unavailableMessage) {
      throw new Error(unavailableMessage)
    }
  }
  const hasSuperPriorityInput = Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled')
  const hasFallbackInput = Object.prototype.hasOwnProperty.call(input, 'fallbackEnabled')
  const hasPriorityInput = Object.prototype.hasOwnProperty.call(input, 'priority')
  const nextPriority = hasPriorityInput ? normalizedDispatchPriority(input.priority) : current.priority
  const nextSuperPriority = hasSuperPriorityInput ? input.superPriorityEnabled === true : current.superPriorityEnabled
  const nextFallback = hasFallbackInput ? input.fallbackEnabled === true : current.fallbackEnabled
  if (nextSuperPriority && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  if ((input.clearFailureState === true || hasStatusInput) && current.status === 'pending_test') {
    throw new Error('待检查账户需等待后台健康检查通过后才能参与调度')
  }
  const nextStatus: AccountStatus = hasStatusInput
    ? input.status === 'disabled' ? 'disabled' : 'active'
    : input.clearFailureState === true
      ? 'active'
      : current.status
  const shouldClearFailureState = input.clearFailureState === true || hasStatusInput
  const nextSchedulable = hasStatusInput
    ? input.status === 'disabled' ? 0 : 1
    : input.clearFailureState === true
      ? 1
      : current.schedulable ? 1 : 0
  const now = nowIso()
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    let accountChanges = 0
    if (shouldClearFailureState) {
      const result = database.prepare(`
        UPDATE accounts
        SET status = ?,
            schedulable = ?,
            cooldown_until = NULL,
            last_error_code = NULL,
            last_error_message = NULL,
            last_error_trace_id = NULL,
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND system_account_id = ?
          AND authorization_instance_authorization_id = ?
          AND deleted_at IS NULL
          AND EXISTS (
            SELECT 1
            FROM group_accounts
            WHERE group_accounts.account_id = accounts.id
              AND group_accounts.system_account_id = ?
              AND group_accounts.group_id = ?
              AND group_accounts.enabled = 1
              AND group_accounts.account_authorization_id = ?
          )
      `)
        .run(nextStatus, nextSchedulable, now, accountId, systemAccountId, current.accountAuthorizationId, systemAccountId, current.boundGroupId, current.accountAuthorizationId)
      accountChanges = Number(result.changes ?? 0)
      if (accountChanges <= 0) {
        rollbackDatabaseTransaction(database, transactionStarted)
        return undefined
      }
    }
    const dispatchResult = database.prepare(`
        UPDATE group_accounts
        SET local_priority = ?,
            local_super_priority_enabled = ?,
            local_fallback_enabled = ?,
            updated_at = ?
        WHERE account_id = ?
          AND system_account_id = ?
          AND group_id = ?
          AND enabled = 1
          AND account_authorization_id = ?
      `)
      .run(nextPriority, nextSuperPriority ? 1 : 0, nextFallback ? 1 : 0, now, accountId, systemAccountId, current.boundGroupId, current.accountAuthorizationId)
    if (Number(dispatchResult.changes ?? 0) <= 0 && accountChanges <= 0) {
      rollbackDatabaseTransaction(database, transactionStarted)
      return undefined
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [current.boundGroupId], accountIds: [accountId], reason: 'authorized_binding_dispatch' })
  invalidateAccountLookupCache(accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_binding_dispatch')
  return findAccountSummary(accountId, { systemAccountId, role: 'user' })
}

export async function updateAuthorizedAccountBindingDispatchAsync(
  accountId: string,
  input: { status?: 'active' | 'disabled'; priority?: number; superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean },
  access?: AccessScope
): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateAuthorizedAccountBindingDispatch(accountId, input, access)
  }
  const systemAccountId = authorizedBindingSystemAccountId(access)
  const accountAccess: AccessScope = { systemAccountId, role: 'user' }
  const current = await findAccountSummaryAsync(accountId, accountAccess)
  if (current?.accessType !== 'authorized' || !current.boundGroupId || !current.accountAuthorizationId) {
    throw new Error('授权账户需要先绑定到你的分组')
  }
  const enablingDispatchFlag = input.superPriorityEnabled === true || input.fallbackEnabled === true
  if (enablingDispatchFlag) {
    const unavailableMessage = accountDispatchUnavailableMessage(current, { requireAuthorizedBinding: true })
    if (unavailableMessage) {
      throw new Error(unavailableMessage)
    }
  }
  const hasSuperPriorityInput = Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled')
  const hasFallbackInput = Object.prototype.hasOwnProperty.call(input, 'fallbackEnabled')
  const hasPriorityInput = Object.prototype.hasOwnProperty.call(input, 'priority')
  const nextPriority = hasPriorityInput ? normalizedDispatchPriority(input.priority) : current.priority
  const nextSuperPriority = hasSuperPriorityInput ? input.superPriorityEnabled === true : current.superPriorityEnabled
  const nextFallback = hasFallbackInput ? input.fallbackEnabled === true : current.fallbackEnabled
  if (nextSuperPriority && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  if ((input.clearFailureState === true || hasStatusInput) && current.status === 'pending_test') {
    throw new Error('待检查账户需等待后台健康检查通过后才能参与调度')
  }
  const nextStatus: AccountStatus = hasStatusInput
    ? input.status === 'disabled' ? 'disabled' : 'active'
    : input.clearFailureState === true
      ? 'active'
      : current.status
  const shouldClearFailureState = input.clearFailureState === true || hasStatusInput
  const nextSchedulable = hasStatusInput
    ? input.status === 'disabled' ? 0 : 1
    : input.clearFailureState === true
      ? 1
      : current.schedulable ? 1 : 0
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const now = nowIso()
  const changed = await client.transaction(async (tx) => {
    let accountChanges = 0
    if (shouldClearFailureState) {
      const result = await tx.execute(`
        UPDATE ${accountRuntimeMutationTable(tx, 'accounts')}
        SET status = ?,
            schedulable = ?,
            cooldown_until = NULL,
            last_error_code = NULL,
            last_error_message = NULL,
            last_error_trace_id = NULL,
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND system_account_id = ?
          AND authorization_instance_authorization_id = ?
          AND deleted_at IS NULL
          AND EXISTS (
            SELECT 1
            FROM ${accountRuntimeMutationTable(tx, 'group_accounts')} group_accounts
            WHERE group_accounts.account_id = accounts.id
              AND group_accounts.system_account_id = ?
              AND group_accounts.group_id = ?
              AND group_accounts.enabled = 1
              AND group_accounts.account_authorization_id = ?
          )
      `, [
        nextStatus,
        nextSchedulable,
        now,
        accountId,
        systemAccountId,
        current.accountAuthorizationId,
        systemAccountId,
        current.boundGroupId,
        current.accountAuthorizationId
      ])
      accountChanges = Number(result.changes ?? 0)
      if (accountChanges <= 0) {
        return false
      }
    }
    const dispatchResult = await tx.execute(`
      UPDATE ${accountRuntimeMutationTable(tx, 'group_accounts')}
      SET local_priority = ?,
          local_super_priority_enabled = ?,
          local_fallback_enabled = ?,
          updated_at = ?
      WHERE account_id = ?
        AND system_account_id = ?
        AND group_id = ?
        AND enabled = 1
        AND account_authorization_id = ?
    `, [
      nextPriority,
      nextSuperPriority ? 1 : 0,
      nextFallback ? 1 : 0,
      now,
      accountId,
      systemAccountId,
      current.boundGroupId,
      current.accountAuthorizationId
    ])
    return Number(dispatchResult.changes ?? 0) > 0 || accountChanges > 0
  })
  if (!changed) {
    return undefined
  }
  await refreshGroupAccountStatsAfterWriteAsync({ groupIds: [current.boundGroupId], accountIds: [accountId], reason: 'authorized_binding_dispatch' }, client)
  invalidateAccountLookupCache(accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_binding_dispatch')
  return await findAccountSummaryAsync(accountId, accountAccess)
}

function migrateAuthorizedAccountBindingTraffic(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): { sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string; groupId?: string } | undefined {
  const systemAccountId = authorizedBindingSystemAccountId(access)
  if (input.sourceAccountId === input.targetAccountId) {
    throw new Error('目标账户不能和当前账户相同')
  }
  const accountAccess = { systemAccountId, role: 'user' as const }
  const sourceAccount = findAccountSummary(input.sourceAccountId, accountAccess)
  const targetAccount = findAccountSummary(input.targetAccountId, accountAccess)
  if (sourceAccount?.accessType !== 'authorized') {
    return undefined
  }
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  if (!sourceAccount.boundGroupId || !sourceAccount.accountAuthorizationId || !targetAccount.boundGroupId || sourceAccount.boundGroupId !== targetAccount.boundGroupId) {
    throw new Error('目标账户必须和当前账户在你的同一个分组内')
  }
  if (sourceAccount.providerCode !== targetAccount.providerCode) {
    throw new Error('目标账户必须和当前账户属于同一个供应商')
  }
  const targetUnavailableMessage = accountDispatchUnavailableMessage(targetAccount, { requireAuthorizedBinding: targetAccount.accessType === 'authorized' })
  if (targetUnavailableMessage) {
    throw new Error(targetUnavailableMessage)
  }
  if (input.sourceStatus === 'unchanged') {
    return { sourceAccount, targetAccount, groupId: sourceAccount.boundGroupId }
  }
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState()
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
  const sourceCooldownGeneration = sourceTemporaryState ? newCooldownRetestGeneration() : null
  const now = nowIso()
  const sourceStatus: AccountStatus = input.sourceStatus === 'disabled' ? 'disabled' : 'temporary_unavailable'
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = ?,
          cooldown_until = ?,
          last_error_code = NULL,
          last_error_message = ?,
          last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_generation = ?,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND deleted_at IS NULL
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(
      sourceStatus,
      sourceStatus === 'disabled' ? 0 : 1,
      sourceCooldownUntil,
      manualTrafficMigrationReason,
      sourceTemporaryState?.observationStartedAt ?? null,
      sourceCooldownGeneration,
      now,
      sourceAccount.id,
      systemAccountId,
      sourceAccount.accountAuthorizationId,
      systemAccountId,
      sourceAccount.boundGroupId,
      sourceAccount.accountAuthorizationId
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [sourceAccount.boundGroupId], accountIds: [sourceAccount.id], reason: 'authorized_binding_migration' })
  invalidateAccountLookupCache(sourceAccount.id)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_binding_migration')
  const nextSource = findAccountSummary(input.sourceAccountId, accountAccess)
  const nextTarget = findAccountSummary(input.targetAccountId, accountAccess)
  return nextSource && nextTarget
    ? { sourceAccount: nextSource, targetAccount: nextTarget, sourceCooldownUntil: sourceCooldownUntil ?? undefined, groupId: sourceAccount.boundGroupId }
    : undefined
}

async function migrateAuthorizedAccountBindingTrafficAsync(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): Promise<{ sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string; groupId?: string } | undefined> {
  const systemAccountId = authorizedBindingSystemAccountId(access)
  if (input.sourceAccountId === input.targetAccountId) {
    throw new Error('目标账户不能和当前账户相同')
  }
  const accountAccess = { systemAccountId, role: 'user' as const }
  const sourceAccount = await findAccountSummaryAsync(input.sourceAccountId, accountAccess)
  const targetAccount = await findAccountSummaryAsync(input.targetAccountId, accountAccess)
  if (sourceAccount?.accessType !== 'authorized') {
    return undefined
  }
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  if (!sourceAccount.boundGroupId || !sourceAccount.accountAuthorizationId || !targetAccount.boundGroupId || sourceAccount.boundGroupId !== targetAccount.boundGroupId) {
    throw new Error('目标账户必须和当前账户在你的同一个分组内')
  }
  if (sourceAccount.providerCode !== targetAccount.providerCode) {
    throw new Error('目标账户必须和当前账户属于同一个供应商')
  }
  const targetUnavailableMessage = accountDispatchUnavailableMessage(targetAccount, { requireAuthorizedBinding: targetAccount.accessType === 'authorized' })
  if (targetUnavailableMessage) {
    throw new Error(targetUnavailableMessage)
  }
  if (input.sourceStatus === 'unchanged') {
    return { sourceAccount, targetAccount, groupId: sourceAccount.boundGroupId }
  }
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState()
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
  const sourceCooldownGeneration = sourceTemporaryState ? newCooldownRetestGeneration() : null
  const now = nowIso()
  const sourceStatus: AccountStatus = input.sourceStatus === 'disabled' ? 'disabled' : 'temporary_unavailable'
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET status = ?,
        schedulable = ?,
        cooldown_until = ?,
        last_error_code = NULL,
        last_error_message = ?,
        last_error_trace_id = NULL,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = ?,
        cooldown_retest_generation = ?,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id = ?
      AND deleted_at IS NULL
      AND EXISTS (
        SELECT 1
        FROM ${accountRuntimeMutationTable(client, 'group_accounts')} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
      )
  `, [
    sourceStatus,
    sourceStatus === 'disabled' ? 0 : 1,
    sourceCooldownUntil,
    manualTrafficMigrationReason,
    sourceTemporaryState?.observationStartedAt ?? null,
    sourceCooldownGeneration,
    now,
    sourceAccount.id,
    systemAccountId,
    sourceAccount.accountAuthorizationId,
    systemAccountId,
    sourceAccount.boundGroupId,
    sourceAccount.accountAuthorizationId
  ])
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  await refreshGroupAccountStatsAfterWriteAsync({ groupIds: [sourceAccount.boundGroupId], accountIds: [sourceAccount.id], reason: 'authorized_binding_migration' }, client)
  invalidateAccountLookupCache(sourceAccount.id)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_binding_migration')
  const nextSource = await findAccountSummaryAsync(input.sourceAccountId, accountAccess)
  const nextTarget = await findAccountSummaryAsync(input.targetAccountId, accountAccess)
  return nextSource && nextTarget
    ? { sourceAccount: nextSource, targetAccount: nextTarget, sourceCooldownUntil: sourceCooldownUntil ?? undefined, groupId: sourceAccount.boundGroupId }
    : undefined
}

export function markAccountException(
  id: string,
  errorCode: string,
  reason: string,
  options: {
    preserveDisabled?: boolean
    traceId?: string
    runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
    expectedConfigRevision?: number
    expectedStatus?: AccountStatus
  } = {}
): AccountSummary | undefined {
  const current = findInternalAccountSummary(id)
  if (!current) {
    return undefined
  }
  if (current.status === 'error' || (current.status === 'disabled' && options.preserveDisabled !== false)) {
    return undefined
  }
  if (options.expectedStatus !== undefined && current.status !== options.expectedStatus) {
    return undefined
  }
  if (options.expectedConfigRevision !== undefined && (current.configRevision ?? 1) !== options.expectedConfigRevision) {
    return undefined
  }

  const updatedAt = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = ?,
          last_error_message = ?,
          last_error_trace_id = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ${accountRuntimeFailureUpdatedAtSql(options.runtimeFailureGuard)}
      WHERE id = ?
        AND deleted_at IS NULL
        AND status = ?
        AND config_revision = ?
        ${accountRuntimeFailureObservationGuardSql(options.runtimeFailureGuard)}
    `)
    .run(
      errorCode || null,
      reason || null,
      normalizedLastErrorTraceId(options.traceId),
      ...accountRuntimeFailureUpdatedAtParams(options.runtimeFailureGuard, updatedAt),
      id,
      options.expectedStatus ?? current.status,
      options.expectedConfigRevision ?? current.configRevision ?? 1,
      ...accountRuntimeFailureObservationGuardParams(options.runtimeFailureGuard)
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_exception' })
  invalidateAccountLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('account_exception')

  return findInternalAccountSummary(id)
}

export async function markAccountExceptionAsync(
  id: string,
  errorCode: string,
  reason: string,
  options: {
    preserveDisabled?: boolean
    traceId?: string
    runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
    expectedConfigRevision?: number
    expectedStatus?: AccountStatus
  } = {}
): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return markAccountException(id, errorCode, reason, options)
  }
  const current = await findInternalAccountSummaryAsync(id)
  if (!current) {
    return undefined
  }
  if (current.status === 'error' || (current.status === 'disabled' && options.preserveDisabled !== false)) {
    return undefined
  }
  if (options.expectedStatus !== undefined && current.status !== options.expectedStatus) {
    return undefined
  }
  if (options.expectedConfigRevision !== undefined && (current.configRevision ?? 1) !== options.expectedConfigRevision) {
    return undefined
  }

  const updatedAt = nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET status = 'error',
        schedulable = 0,
        cooldown_until = NULL,
        last_error_code = ?,
        last_error_message = ?,
        last_error_trace_id = ?,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        updated_at = ${accountRuntimeFailureUpdatedAtSql(options.runtimeFailureGuard)}
    WHERE id = ?
      AND deleted_at IS NULL
      AND status = ?
      AND config_revision = ?
      ${accountRuntimeFailureObservationGuardSql(options.runtimeFailureGuard)}
  `, [
    errorCode || null,
    reason || null,
    normalizedLastErrorTraceId(options.traceId),
    ...accountRuntimeFailureUpdatedAtParams(options.runtimeFailureGuard, updatedAt),
    id,
    options.expectedStatus ?? current.status,
    options.expectedConfigRevision ?? current.configRevision ?? 1,
    ...accountRuntimeFailureObservationGuardParams(options.runtimeFailureGuard)
  ])
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_exception' }, client)
  invalidateAccountLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('account_exception')

  return await findInternalAccountSummaryAsync(id)
}

export function markAccountDisabledByFailure(
  id: string,
  reason: string,
  runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
): AccountSummary | undefined {
  const current = findInternalAccountSummary(id)
  if (!current || current.status === 'error') {
    return undefined
  }
  return markAccountException(id, 'upstream_failure', reason, { runtimeFailureGuard })
}

export async function markAccountDisabledByFailureAsync(
  id: string,
  reason: string,
  runtimeFailureGuard?: AccountRuntimeFailureObservationGuard
): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return markAccountDisabledByFailure(id, reason, runtimeFailureGuard)
  }
  const current = await findInternalAccountSummaryAsync(id)
  if (!current || current.status === 'error') {
    return undefined
  }
  return await markAccountExceptionAsync(id, 'upstream_failure', reason, { runtimeFailureGuard })
}

export function recordAccountStreamFailure(input: {
  accountId: string
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  reason: string
  traceId?: string
}): { count: number; triggered: boolean; account?: AccountSummary } {
  const row = getBusinessDatabase().prepare('SELECT id, status, stream_failure_count, stream_failure_window_started_at FROM accounts WHERE id = ? AND deleted_at IS NULL').get(input.accountId) as unknown as AccountFailureRow | undefined
  if (!row) {
    return { count: 0, triggered: false }
  }
  if (isHardUnavailableAccountStatus(row.status)) {
    return { count: Math.max(0, row.stream_failure_count), triggered: false, account: findInternalAccountSummary(input.accountId) }
  }

  const now = new Date()
  const nowIsoValue = now.toISOString()
  const thresholdMs = Math.max(1, input.thresholdWindowMinutes) * 60_000
  const startedAt = row.stream_failure_window_started_at ? new Date(row.stream_failure_window_started_at) : undefined
  const windowValid = startedAt !== undefined && !Number.isNaN(startedAt.getTime()) && now.getTime() - startedAt.getTime() < thresholdMs
  const count = windowValid ? Math.max(0, row.stream_failure_count) + 1 : 1
  const windowStartedAt = windowValid ? row.stream_failure_window_started_at : nowIsoValue

  getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = ?,
          stream_failure_window_started_at = ?,
          last_error_message = ?,
          last_error_trace_id = ?,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(count, windowStartedAt, input.reason || null, normalizedLastErrorTraceId(input.traceId), nowIsoValue, input.accountId)

  return { count, triggered: false, account: findInternalAccountSummary(input.accountId) }
}

export async function recordAccountStreamFailureAsync(input: {
  accountId: string
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  reason: string
  traceId?: string
}): Promise<{ count: number; triggered: boolean; account?: AccountSummary }> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountStreamFailure(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<AccountFailureRow>(`
    SELECT id, status, stream_failure_count, stream_failure_window_started_at
    FROM ${accountRuntimeMutationTable(client, 'accounts')}
    WHERE id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [input.accountId])
  if (!row) {
    return { count: 0, triggered: false }
  }
  if (isHardUnavailableAccountStatus(row.status)) {
    return { count: Math.max(0, row.stream_failure_count), triggered: false, account: await findInternalAccountSummaryAsync(input.accountId) }
  }

  const now = new Date()
  const nowIsoValue = now.toISOString()
  const thresholdMs = Math.max(1, input.thresholdWindowMinutes) * 60_000
  const startedAt = row.stream_failure_window_started_at ? new Date(row.stream_failure_window_started_at) : undefined
  const windowValid = startedAt !== undefined && !Number.isNaN(startedAt.getTime()) && now.getTime() - startedAt.getTime() < thresholdMs
  const count = windowValid ? Math.max(0, row.stream_failure_count) + 1 : 1
  const windowStartedAt = windowValid ? row.stream_failure_window_started_at : nowIsoValue

  await client.execute(`
    UPDATE ${accountRuntimeMutationTable(client, 'accounts')}
    SET stream_failure_count = ?,
        stream_failure_window_started_at = ?,
        last_error_message = ?,
        last_error_trace_id = ?,
        updated_at = ?
    WHERE id = ?
      AND deleted_at IS NULL
  `, [count, windowStartedAt, input.reason || null, normalizedLastErrorTraceId(input.traceId), nowIsoValue, input.accountId])

  return { count, triggered: false, account: await findInternalAccountSummaryAsync(input.accountId) }
}

export function recordAuthorizedAccountBindingStreamFailure(input: AuthorizedAccountBindingRuntimeTarget & {
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  reason: string
  traceId?: string
}): { count: number; triggered: boolean; account?: AccountSummary } {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target || !authorizedAccountRuntimeBindingExists(target)) {
    return { count: 0, triggered: false }
  }
  const result = recordAccountStreamFailure({
    accountId: target.accountId,
    thresholdCount: input.thresholdCount,
    thresholdWindowMinutes: input.thresholdWindowMinutes,
    action: input.action,
    reason: input.reason,
    traceId: input.traceId
  })
  return {
    count: result.count,
    triggered: result.triggered,
    account: findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' }) ?? result.account
  }
}

export async function recordAuthorizedAccountBindingStreamFailureAsync(input: AuthorizedAccountBindingRuntimeTarget & {
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  reason: string
  traceId?: string
}): Promise<{ count: number; triggered: boolean; account?: AccountSummary }> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAuthorizedAccountBindingStreamFailure(input)
  }
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target || !await authorizedAccountRuntimeBindingExistsAsync(target)) {
    return { count: 0, triggered: false }
  }
  const result = await recordAccountStreamFailureAsync({
    accountId: target.accountId,
    thresholdCount: input.thresholdCount,
    thresholdWindowMinutes: input.thresholdWindowMinutes,
    action: input.action,
    reason: input.reason,
    traceId: input.traceId
  })
  return {
    count: result.count,
    triggered: result.triggered,
    account: await findAccountSummaryAsync(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' }) ?? result.account
  }
}
