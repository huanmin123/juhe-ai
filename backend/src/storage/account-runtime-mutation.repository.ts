import type { AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus } from '../domain/types.js'
import { currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { accountEnabledGroupId } from './account-group-binding-write.repository.js'
import { findAccountSummary } from './account-summary.repository.js'
import { isCoolingAccountStatus, isHardUnavailableAccountStatus, normalizeAccountStatus } from './account-status.js'
import { normalizedDispatchPriority } from './account-write-input.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'
import type { AccountFailureRow, AccountRow } from './repository-row-types.js'
import { accountSystemAccountId, canManageResourceOwner } from './resource-authorization-helpers.js'
import {
  cooldownRetestObservationStartedAtForStatus,
  defaultTemporaryUnschedulableMinutes,
  initialCooldownUntilForStatus,
  invalidateGatewayRuntimeAfterBusinessWrite,
  isAccountExpired,
  temporaryUnavailableRuntimeState
} from './account-runtime-mutation-helpers.js'

const manualTrafficMigrationReason = '手动迁移流量'
const internalAccountReadAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

function findInternalAccountSummary(accountId: string): AccountSummary | undefined {
  return findAccountSummary(accountId, internalAccountReadAccess)
}

function accountRowForManage(accountId: string, access?: AccessScope): AccountRow | undefined {
  const row = getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ? AND deleted_at IS NULL').get(accountId) as unknown as AccountRow | undefined
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

interface ClearAccountFailureStateOptions {
  allowErrorRestore?: boolean
  allowPendingTestRestore?: boolean
}

export interface AccountFailureStateClearResult {
  account?: AccountSummary
  changed: boolean
}

export function clearAccountFailureState(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): AccountSummary | undefined {
  return clearAccountFailureStateResult(id, access, options).account
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
  if (expiredByPackage) {
    const result = getBusinessDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = 'account_expired',
            last_error_message = ?,
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND deleted_at IS NULL
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_expired' })
      invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
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
        AND (
          status <> 'active'
          OR schedulable <> 1
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NOT NULL
          OR cooldown_retest_failure_count > 0
          OR cooldown_retest_observation_started_at IS NOT NULL
          OR cooldown_retest_last_at IS NOT NULL
          OR cooldown_retest_last_status_code IS NOT NULL
          OR stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
        )
    `)
    .run(nowIso(), id, options.allowErrorRestore === false ? 0 : 1, options.allowPendingTestRestore === true ? 1 : 0)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_restored' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_restored')
  }

  return { account: findAccountSummary(id, accountAccess), changed }
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
  updatedAt?: string
  lastUsedAt?: string
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
          accounts.updated_at,
          accounts.last_used_at
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
        updated_at?: string | null
        last_used_at?: string | null
      } | undefined
    if (!row) {
      return undefined
    }
    return {
      status: normalizeAccountStatus(row.status),
      updatedAt: row.updated_at ?? undefined,
      lastUsedAt: row.last_used_at ?? undefined
    }
  }

  const row = getBusinessDatabase()
    .prepare('SELECT status, updated_at, last_used_at FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
    .get(input.accountId) as unknown as {
      status?: AccountStatus | null
      updated_at?: string | null
      last_used_at?: string | null
    } | undefined
  if (!row) {
    return undefined
  }
  return {
    status: normalizeAccountStatus(row.status),
    updatedAt: row.updated_at ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined
  }
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
        AND (
          status <> 'active'
          OR schedulable <> 1
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NOT NULL
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
    .run(now, target.accountId, target.systemAccountId, target.accountAuthorizationId, options.allowErrorRestore === false ? 0 : 1, options.allowPendingTestRestore === true ? 1 : 0, target.systemAccountId, target.groupId, target.accountAuthorizationId)
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
  input: AuthorizedAccountBindingRuntimeTarget & { reason: string }
): AccountSummary | undefined {
  return markAuthorizedAccountBindingCooldownByContext({
    ...input,
    status: 'temporary_unavailable'
  })
}

export function markAuthorizedAccountBindingCooldownByContext(
  input: AuthorizedAccountBindingRuntimeTarget & { cooldownUntil?: string; reason: string; status?: AccountStatus }
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
  const now = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = 1,
          cooldown_until = ?,
          last_error_code = NULL,
          last_error_message = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND deleted_at IS NULL
        AND status NOT IN ('disabled', 'error')
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
    .run(cooldownStatus, cooldownUntil, input.reason || null, observationStartedAt ?? null, now, target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId)
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_cooldown' })
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_cooldown')
  return findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
}

export function markAuthorizedAccountBindingDisabledByFailure(
  input: AuthorizedAccountBindingRuntimeTarget & { reason: string }
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
    .run(input.reason || null, now, target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId)
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_exception' })
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_exception')
  return findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
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
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND status NOT IN ('disabled', 'error')
        AND (
          stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
          OR (status = 'active' AND last_error_code IS NOT NULL)
          OR (status = 'active' AND last_error_message IS NOT NULL)
        )
    `)
    .run(nowIso(), id)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateGatewayRuntimeAfterBusinessWrite('account_stream_failure_cleared')
  }
  return changed
}

export function markAccountTestTemporaryUnavailable(
  account: AccountSummary,
  reason: string,
  access?: AccessScope
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
    return markAuthorizedAccountBindingTemporaryUnavailable(current, message, access)
  }
  return markAccountTemporaryUnavailable(current.id, message)
}

function markAuthorizedAccountBindingTemporaryUnavailable(
  account: AccountSummary,
  reason: string,
  access?: AccessScope
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
    reason
  })
}

export function markAccountTemporaryUnavailable(id: string, reason: string): AccountSummary | undefined {
  return markAccountCooldown(id, undefined, reason, 'temporary_unavailable')
}

export function markAccountCooldown(id: string, until: string | undefined, reason: string, status: AccountStatus = 'temporary_unavailable'): AccountSummary | undefined {
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
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND deleted_at IS NULL
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_expired' })
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
    }
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

  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = 1,
          cooldown_until = ?,
          last_error_code = NULL,
          last_error_message = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(cooldownStatus, cooldownUntil, reason || null, cooldownObservationStartedAt ?? null, cooldownNow, id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown')
  }

  return findInternalAccountSummary(id)
}

export function migrateAccountTraffic(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): { sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string } | undefined {
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

  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
  const reason = manualTrafficMigrationReason
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(nowMs)
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
  const sourceObservationStartedAt = sourceTemporaryState?.observationStartedAt ?? null
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
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = NULL,
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
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = ?,
              cooldown_retest_last_at = NULL,
              cooldown_retest_last_status_code = NULL,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ? AND system_account_id = ?
        `)
        .run(sourceCooldownUntil, reason, sourceObservationStartedAt ?? null, now, sourceRow.id, sourceRow.system_account_id)
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

  const ownerAccess = { systemAccountId: sourceRow.system_account_id, role: 'user' as const }
  const sourceAccount = findAccountSummary(input.sourceAccountId, ownerAccess)
  const targetAccount = findAccountSummary(input.targetAccountId, ownerAccess)
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  return { sourceAccount, targetAccount, sourceCooldownUntil: sourceCooldownUntil ?? undefined }
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
    throw new Error('待测试账户需手动测试通过后才能参与调度')
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

function migrateAuthorizedAccountBindingTraffic(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): { sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string } | undefined {
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
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState()
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
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
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = ?,
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
  return nextSource && nextTarget ? { sourceAccount: nextSource, targetAccount: nextTarget, sourceCooldownUntil: sourceCooldownUntil ?? undefined } : undefined
}

export function markAccountException(
  id: string,
  errorCode: string,
  reason: string,
  options: { preserveDisabled?: boolean } = {}
): AccountSummary | undefined {
  const current = findInternalAccountSummary(id)
  if (!current) {
    return undefined
  }
  if (current.status === 'disabled' && options.preserveDisabled !== false) {
    return undefined
  }

  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = ?,
          last_error_message = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(errorCode || null, reason || null, nowIso(), id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_exception' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_exception')
  }

  return findInternalAccountSummary(id)
}

export function markAccountDisabledByFailure(id: string, reason: string): AccountSummary | undefined {
  const current = findInternalAccountSummary(id)
  if (!current || current.status === 'error') {
    return undefined
  }
  return markAccountException(id, 'upstream_failure', reason)
}

export function recordAccountStreamFailure(input: {
  accountId: string
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  reason: string
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
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(count, windowStartedAt, input.reason || null, nowIsoValue, input.accountId)

  const triggered = count >= Math.max(1, input.thresholdCount) && input.action !== 'none'
  if (!triggered) {
    return { count, triggered: false, account: findInternalAccountSummary(input.accountId) }
  }

  if (input.action === 'cooldown') {
    markAccountTemporaryUnavailable(input.accountId, input.reason)
  } else {
    markAccountDisabledByFailure(input.accountId, input.reason)
  }

  getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(nowIsoValue, input.accountId)
  refreshGroupAccountStatsAfterWrite({ accountIds: [input.accountId], reason: 'stream_failure_threshold' })

  return { count, triggered: true, account: findInternalAccountSummary(input.accountId) }
}

export function recordAuthorizedAccountBindingStreamFailure(input: AuthorizedAccountBindingRuntimeTarget & {
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  reason: string
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
    reason: input.reason
  })
  return {
    count: result.count,
    triggered: result.triggered,
    account: findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' }) ?? result.account
  }
}
