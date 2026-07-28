import { isDeepStrictEqual } from 'node:util'

import { runtimeConfig } from '../config/runtime.js'
import type { AccountStatus, RequestQuotaLimits } from '../domain/types.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import {
  createPostgresDatabaseClient,
  createSqliteDatabaseClient,
  type DatabaseClient
} from './database-client.js'
import { refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { getPostgresPool } from './postgres-client.js'
import {
  isRequestQuotaExceeded,
  loadRequestQuotaCostsBatch,
  loadRequestQuotaCostsBatchAsync,
  requestQuotaCostKey,
  requestQuotaCostKeyAsync,
  type RequestQuotaCostInput
} from './request-quota-checker.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { invalidateGatewayRuntimeAfterBusinessWrite, isAccountExpired } from './account-runtime-mutation-helpers.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'

export interface AuthorizedAccountDispatchInput {
  expectedConfigRevision: number
  status?: 'active' | 'disabled'
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  clearFailureState?: boolean
}

export interface AuthorizedAccountDispatchChange {
  field: string
  before: unknown
  after: unknown
}

export interface AuthorizedAccountDispatchMutationPatch {
  status?: AccountStatus
  schedulable?: boolean
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  failureStateCleared?: true
}

export interface AuthorizedAccountDispatchMutationResult {
  id: string
  configRevision: number
  changedFields: string[]
  patch: AuthorizedAccountDispatchMutationPatch
  changes: AuthorizedAccountDispatchChange[]
  name: string
  ownerSystemAccountId: string
  runtimeRestoreRequired: boolean
  authorizedBinding: {
    systemAccountId: string
    groupId: string
    accountAuthorizationId: string
  }
}

export class AuthorizedAccountDispatchRevisionConflictError extends Error {
  constructor(
    readonly accountId: string,
    readonly expectedConfigRevision: number,
    readonly actualConfigRevision?: number
  ) {
    super(`授权账户配置已发生并发变更，请重试：${accountId}`)
    this.name = 'AuthorizedAccountDispatchRevisionConflictError'
  }
}

interface AuthorizedAccountDispatchRow {
  id: string
  config_revision: number | bigint | string
  system_account_id: string
  name: string
  status: AccountStatus
  schedulable: number | boolean | string
  account_expires_at: string | null
  cooldown_until: string | null
  last_error_code: string | null
  last_error_message: string | null
  last_error_trace_id: string | null
  cooldown_retest_failure_count: number | bigint | string
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_generation: string | null
  cooldown_retest_last_at: string | null
  cooldown_retest_last_status_code: number | bigint | string | null
  stream_failure_count: number | bigint | string
  stream_failure_window_started_at: string | null
  authorization_instance_source_account_id: string | null
  authorization_instance_authorization_id: string | null
  authorization_status: 'active' | 'paused' | 'expired' | 'revoked' | 'returned'
  authorization_expires_at: string | null
  authorization_limits_json: string | null
  authorization_effective_source_team_id: string | null
  source_id: string | null
  source_status: AccountStatus | null
  source_schedulable: number | boolean | string | null
  source_account_expires_at: string | null
  source_cooldown_until: string | null
  source_last_error_code: string | null
  source_last_error_message: string | null
}

interface AuthorizedAccountBindingRow {
  group_id: string
  account_authorization_id: string
  local_priority: number | bigint | string
  local_super_priority_enabled: number | boolean | string
  local_fallback_enabled: number | boolean | string
}

interface AuthorizedDispatchTransactionOutcome extends AuthorizedAccountDispatchMutationResult {
  groupStatsAffected: boolean
  gatewayRuntimeAffected: boolean
}

export async function updateAuthorizedAccountBindingDispatchAsync(
  accountId: string,
  input: AuthorizedAccountDispatchInput,
  access?: AccessScope
): Promise<AuthorizedAccountDispatchMutationResult | undefined> {
  assertAuthorizedDispatchInput(input)
  const systemAccountId = authorizedBindingSystemAccountId(access)
  const client = await authorizedDispatchDatabaseClient()
  const outcome = await client.transaction(async (tx) => {
    const row = await loadAuthorizedAccountDispatchRowForUpdate(tx, accountId, systemAccountId)
    if (!row?.authorization_instance_source_account_id || !row.authorization_instance_authorization_id) {
      return undefined
    }
    const binding = await loadAuthorizedAccountBindingForUpdate(tx, row)
    if (!binding) return undefined
    const currentRevision = integerValue(row.config_revision)
    if (currentRevision !== input.expectedConfigRevision) {
      throw new AuthorizedAccountDispatchRevisionConflictError(accountId, input.expectedConfigRevision, currentRevision)
    }
    if (row.authorization_status === 'revoked' || row.authorization_status === 'returned') {
      return undefined
    }
    return patchAuthorizedAccountDispatchInTransaction(tx, row, binding, input)
  })
  if (!outcome) return undefined
  if (outcome.changedFields.length > 0) {
    await applyAuthorizedDispatchPostCommitEffects(outcome, client)
  }
  const {
    groupStatsAffected: _groupStatsAffected,
    gatewayRuntimeAffected: _gatewayRuntimeAffected,
    ...result
  } = outcome
  return result
}

async function applyAuthorizedDispatchPostCommitEffects(
  outcome: AuthorizedDispatchTransactionOutcome,
  client: DatabaseClient
): Promise<void> {
  const effects: Promise<unknown>[] = []
  if (outcome.groupStatsAffected) {
    effects.push(refreshGroupAccountStatsAfterWriteAsync({
      groupIds: [outcome.authorizedBinding.groupId],
      accountIds: [outcome.id],
      reason: 'authorized_binding_dispatch'
    }, client))
  }
  runAuthorizedDispatchPostCommitSyncEffect(outcome.id, () => invalidateAccountLookupCache(outcome.id))
  if (outcome.gatewayRuntimeAffected) {
    runAuthorizedDispatchPostCommitSyncEffect(outcome.id, () => {
      invalidateGatewayRuntimeAfterBusinessWrite('authorized_binding_dispatch')
    })
  }
  const settled = await Promise.allSettled(effects)
  for (const result of settled) {
    if (result.status !== 'rejected') continue
    logger.warn(errorLogFields(result.reason, {
      event: 'authorized_dispatch_post_commit_effect_failed',
      accountId: outcome.id
    }), '授权账户调度更新已提交，但后置统计刷新失败')
  }
}

function runAuthorizedDispatchPostCommitSyncEffect(accountId: string, effect: () => void): void {
  try {
    effect()
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'authorized_dispatch_post_commit_effect_failed',
      accountId
    }), '授权账户调度更新已提交，但后置缓存失效失败')
  }
}

async function patchAuthorizedAccountDispatchInTransaction(
  client: DatabaseClient,
  row: AuthorizedAccountDispatchRow,
  binding: AuthorizedAccountBindingRow,
  input: AuthorizedAccountDispatchInput
): Promise<AuthorizedDispatchTransactionOutcome> {
  const hasStatusInput = hasOwn(input, 'status')
  const hasPriorityInput = hasOwn(input, 'priority')
  const hasSuperPriorityInput = hasOwn(input, 'superPriorityEnabled')
  const hasFallbackInput = hasOwn(input, 'fallbackEnabled')
  const clearFailureState = input.clearFailureState === true

  if ((hasStatusInput || clearFailureState) && row.status === 'pending_test') {
    throw new Error('待检查账户需等待后台健康检查通过后才能参与调度')
  }
  if (input.status === 'active'
    || clearFailureState
    || input.superPriorityEnabled === true
    || input.fallbackEnabled === true) {
    const unavailableMessage = await authorizedDispatchUnavailableMessage(client, row, binding, {
      allowLocalRecovery: input.status === 'active' || clearFailureState
    })
    if (unavailableMessage) throw new Error(unavailableMessage)
  }

  const currentPriority = integerValue(binding.local_priority)
  const currentSuperPriority = databaseBoolean(binding.local_super_priority_enabled)
  const currentFallback = databaseBoolean(binding.local_fallback_enabled)
  const nextPriority = hasPriorityInput ? normalizedPriority(input.priority) : currentPriority
  const nextSuperPriority = hasSuperPriorityInput ? input.superPriorityEnabled === true : currentSuperPriority
  const nextFallback = hasFallbackInput ? input.fallbackEnabled === true : currentFallback
  if (nextSuperPriority && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }

  const accountColumns = new Map<string, unknown>()
  const bindingColumns = new Map<string, unknown>()
  const changes: AuthorizedAccountDispatchChange[] = []
  const patch: AuthorizedAccountDispatchMutationPatch = {}
  const addChange = (field: string, before: unknown, after: unknown): void => {
    if (isDeepStrictEqual(before, after)) return
    changes.push({ field, before, after })
  }
  const setColumn = (columns: Map<string, unknown>, column: string, before: unknown, after: unknown, value = after): void => {
    if (!isDeepStrictEqual(before, after)) columns.set(column, value)
  }

  if (hasPriorityInput) {
    setColumn(bindingColumns, 'local_priority', currentPriority, nextPriority)
    addChange('priority', currentPriority, nextPriority)
    if (currentPriority !== nextPriority) patch.priority = nextPriority
  }
  if (hasSuperPriorityInput) {
    setColumn(bindingColumns, 'local_super_priority_enabled', currentSuperPriority, nextSuperPriority, nextSuperPriority ? 1 : 0)
    addChange('superPriorityEnabled', currentSuperPriority, nextSuperPriority)
    if (currentSuperPriority !== nextSuperPriority) patch.superPriorityEnabled = nextSuperPriority
  }
  if (hasFallbackInput) {
    setColumn(bindingColumns, 'local_fallback_enabled', currentFallback, nextFallback, nextFallback ? 1 : 0)
    addChange('fallbackEnabled', currentFallback, nextFallback)
    if (currentFallback !== nextFallback) patch.fallbackEnabled = nextFallback
  }

  const currentSchedulable = databaseBoolean(row.schedulable)
  const nextStatus: AccountStatus = hasStatusInput
    ? input.status === 'disabled' ? 'disabled' : 'active'
    : clearFailureState ? 'active' : row.status
  const nextSchedulable = hasStatusInput
    ? input.status === 'disabled' ? false : true
    : clearFailureState ? true : currentSchedulable
  if (hasStatusInput || clearFailureState) {
    setColumn(accountColumns, 'status', row.status, nextStatus)
    setColumn(accountColumns, 'schedulable', currentSchedulable, nextSchedulable, nextSchedulable ? 1 : 0)
    addChange('status', row.status, nextStatus)
    addChange('schedulable', currentSchedulable, nextSchedulable)
    if (row.status !== nextStatus) patch.status = nextStatus
    if (currentSchedulable !== nextSchedulable) patch.schedulable = nextSchedulable
    const failureStateChanged = clearAuthorizedFailureStateColumns(accountColumns, row)
    if (failureStateChanged) {
      addChange('failureState', '异常状态', '已清除')
      patch.failureStateCleared = true
    }
  }

  if (accountColumns.size === 0 && bindingColumns.size === 0) {
    return unchangedOutcome(row, binding)
  }

  const now = nowIso()
  const accountAssignments = [...accountColumns.keys()]
    .map((column) => `${client.dialect.quoteIdentifier(column)} = ?`)
  accountAssignments.push('config_revision = config_revision + 1', 'updated_at = ?')
  const accountResult = await client.execute(`
    UPDATE ${authorizedDispatchTable(client, 'accounts')}
    SET ${accountAssignments.join(', ')}
    WHERE id = ?
      AND system_account_id = ?
      AND authorization_instance_authorization_id = ?
      AND config_revision = ?
      AND deleted_at IS NULL
  `, [
    ...accountColumns.values(),
    now,
    row.id,
    row.system_account_id,
    row.authorization_instance_authorization_id,
    integerValue(row.config_revision)
  ])
  if (accountResult.changes !== 1) {
    throw new AuthorizedAccountDispatchRevisionConflictError(row.id, integerValue(row.config_revision))
  }

  if (bindingColumns.size > 0) {
    const bindingAssignments = [...bindingColumns.keys()]
      .map((column) => `${client.dialect.quoteIdentifier(column)} = ?`)
    bindingAssignments.push('updated_at = ?')
    const bindingResult = await client.execute(`
      UPDATE ${authorizedDispatchTable(client, 'group_accounts')}
      SET ${bindingAssignments.join(', ')}
      WHERE account_id = ?
        AND system_account_id = ?
        AND group_id = ?
        AND account_authorization_id = ?
        AND enabled = 1
    `, [
      ...bindingColumns.values(),
      now,
      row.id,
      row.system_account_id,
      binding.group_id,
      binding.account_authorization_id
    ])
    if (bindingResult.changes !== 1) {
      throw new Error('授权账户分组绑定已发生变化，请刷新后重试')
    }
  }

  return {
    id: row.id,
    configRevision: integerValue(row.config_revision) + 1,
    changedFields: changes.map((change) => change.field).sort(),
    patch,
    changes,
    name: row.name,
    ownerSystemAccountId: row.system_account_id,
    runtimeRestoreRequired: clearFailureState || input.status === 'active',
    authorizedBinding: authorizedBindingResult(row, binding),
    groupStatsAffected: true,
    gatewayRuntimeAffected: true
  }
}

function clearAuthorizedFailureStateColumns(
  columns: Map<string, unknown>,
  row: AuthorizedAccountDispatchRow
): boolean {
  const beforeSize = columns.size
  setColumnIfChanged(columns, 'cooldown_until', row.cooldown_until, null)
  setColumnIfChanged(columns, 'last_error_code', row.last_error_code, null)
  setColumnIfChanged(columns, 'last_error_message', row.last_error_message, null)
  setColumnIfChanged(columns, 'last_error_trace_id', row.last_error_trace_id, null)
  setColumnIfChanged(columns, 'cooldown_retest_failure_count', integerValue(row.cooldown_retest_failure_count), 0)
  setColumnIfChanged(columns, 'cooldown_retest_observation_started_at', row.cooldown_retest_observation_started_at, null)
  setColumnIfChanged(columns, 'cooldown_retest_generation', row.cooldown_retest_generation, null)
  setColumnIfChanged(columns, 'cooldown_retest_last_at', row.cooldown_retest_last_at, null)
  setColumnIfChanged(columns, 'cooldown_retest_last_status_code', nullableInteger(row.cooldown_retest_last_status_code), null)
  setColumnIfChanged(columns, 'stream_failure_count', integerValue(row.stream_failure_count), 0)
  setColumnIfChanged(columns, 'stream_failure_window_started_at', row.stream_failure_window_started_at, null)
  return columns.size > beforeSize
}

async function authorizedDispatchUnavailableMessage(
  client: DatabaseClient,
  row: AuthorizedAccountDispatchRow,
  binding: AuthorizedAccountBindingRow,
  options: { allowLocalRecovery?: boolean } = {},
  now = new Date()
): Promise<string | undefined> {
  const nowMs = now.getTime()
  if (row.authorization_status === 'expired' || isExpired(row.authorization_expires_at, nowMs)) {
    return '授权已到期，当前账户不能调用'
  }
  if (row.authorization_status === 'paused') return '授权已暂停，当前账户不能调用'
  if (row.authorization_status === 'revoked' || row.authorization_status === 'returned') {
    return '授权关系已失效，当前账户不能调用'
  }
  if (await authorizationQuotaExceeded(client, row, now)) {
    return '授权额度已用完，当前账户不能调用'
  }
  if (!row.source_id || !row.source_status) {
    return '授权方原账户不存在或已删除，当前账户不能调用'
  }
  if (row.source_last_error_code === 'account_expired' || isAccountExpired(row.source_account_expires_at ?? undefined, nowMs)) {
    return '授权方原账户已到期，当前账户不能调用'
  }
  if (row.source_status === 'disabled') return '授权方原账户已停用，当前账户不能调用'
  if (row.source_status === 'pending_test') return '授权方原账户尚未通过后台健康检查，当前账户不能调用'
  if (row.source_status === 'error') return row.source_last_error_message || '授权方原账户处于异常状态，当前账户不能调用'
  if (row.source_status === 'rate_limited') return row.source_last_error_message || '授权方原账户限流中，当前账户不能调用'
  if (row.source_status === 'temporary_unavailable') return row.source_last_error_message || '授权方原账户临时不可调用，当前账户不能调用'
  if (row.source_status === 'quality_isolated') return row.source_last_error_message || '授权方原账户因模型质量不达标已隔离，恢复前不能调用'
  if (isFuture(row.source_cooldown_until, nowMs)) return '授权方原账户正在冷却，恢复前当前账户不能调用'
  if (!databaseBoolean(row.source_schedulable)) return '授权方原账户已关闭调度，当前账户不能调用'
  if (row.last_error_code === 'account_expired' || isAccountExpired(row.account_expires_at ?? undefined, nowMs)) {
    return '授权账户已到期，当前不可用'
  }
  if (!options.allowLocalRecovery) {
    if (row.status === 'disabled') return '授权账户已停用，当前不可用'
    if (row.status === 'pending_test') return '授权账户正在等待后台健康检查，检查通过前不会参与调度'
    if (row.status === 'error') return row.last_error_message || '授权账户处于异常状态，当前不可用'
    if (row.status === 'rate_limited') return row.last_error_message || '授权账户限流中，恢复前不会参与调度'
    if (row.status === 'temporary_unavailable') return row.last_error_message || '授权账户临时不可调用，恢复前不会参与调度'
    if (row.status === 'quality_isolated') return row.last_error_message || '授权账户因模型质量不达标已隔离，质量恢复检查通过前不会参与调度'
    if (isFuture(row.cooldown_until, nowMs)) return '授权账户正在冷却，恢复前不会参与调度'
    if (!databaseBoolean(row.schedulable)) return '授权账户暂时不可调用，恢复前不会参与调度'
  }
  if (!binding.group_id) return '授权账户需要先绑定到你的分组'
  return undefined
}

async function authorizationQuotaExceeded(
  client: DatabaseClient,
  row: AuthorizedAccountDispatchRow,
  now: Date
): Promise<boolean> {
  const checks: Array<{ limits: RequestQuotaLimits; input: RequestQuotaCostInput }> = []
  const directLimits = parseRequestQuotaLimitsJson(row.authorization_limits_json)
  if (hasEnabledRequestQuotaLimit(directLimits)) {
    checks.push({
      limits: directLimits,
      input: {
        systemAccountId: row.system_account_id,
        scopeType: 'account_authorization',
        scopeId: row.authorization_instance_authorization_id ?? '',
        now,
        hourlyWindowHours: directLimits.hourly?.hours
      }
    })
  }
  if (row.authorization_effective_source_team_id && row.authorization_instance_authorization_id) {
    const teamGrant = await client.one<{ limits_json: string | null }>(`
      SELECT grant_rows.limits_json
      FROM ${authorizedDispatchTable(client, 'resource_authorizations')} authorizations
      INNER JOIN ${authorizedDispatchTable(client, 'resource_authorization_grants')} grant_rows
        ON grant_rows.resource_type = authorizations.resource_type
        AND grant_rows.resource_id = authorizations.resource_id
        AND grant_rows.grantee_type = 'team'
        AND grant_rows.grantee_team_id = authorizations.effective_source_team_id
        AND grant_rows.status = 'active'
        AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ?)
      WHERE authorizations.id = ?
        AND authorizations.status = 'active'
        AND (authorizations.expires_at IS NULL OR authorizations.expires_at > ?)
      LIMIT 1
    `, [now.toISOString(), row.authorization_instance_authorization_id, now.toISOString()])
    const teamLimits = parseRequestQuotaLimitsJson(teamGrant?.limits_json)
    if (hasEnabledRequestQuotaLimit(teamLimits)) {
      checks.push({
        limits: teamLimits,
        input: {
          systemAccountId: row.system_account_id,
          scopeType: 'account_authorization_team',
          scopeId: `${row.id}:${row.authorization_effective_source_team_id}`,
          now,
          hourlyWindowHours: teamLimits.hourly?.hours
        }
      })
    }
  }
  if (!checks.length) return false
  if (client.driver === 'postgres') {
    const costs = await loadRequestQuotaCostsBatchAsync(client, checks.map((check) => check.input))
    for (const check of checks) {
      const value = costs.get(await requestQuotaCostKeyAsync(check.input))
      if (value && isRequestQuotaExceeded(check.limits, value)) return true
    }
    return false
  }
  const costs = loadRequestQuotaCostsBatch(getStatsDatabase(), checks.map((check) => check.input))
  return checks.some((check) => {
    const value = costs.get(requestQuotaCostKey(check.input))
    return Boolean(value && isRequestQuotaExceeded(check.limits, value))
  })
}

async function loadAuthorizedAccountDispatchRowForUpdate(
  client: DatabaseClient,
  accountId: string,
  systemAccountId: string
): Promise<AuthorizedAccountDispatchRow | undefined> {
  return client.one<AuthorizedAccountDispatchRow>(`
    SELECT
      accounts.id,
      accounts.config_revision,
      accounts.system_account_id,
      accounts.name,
      accounts.status,
      accounts.schedulable,
      accounts.account_expires_at,
      accounts.cooldown_until,
      accounts.last_error_code,
      accounts.last_error_message,
      accounts.last_error_trace_id,
      accounts.cooldown_retest_failure_count,
      accounts.cooldown_retest_observation_started_at,
      accounts.cooldown_retest_generation,
      accounts.cooldown_retest_last_at,
      accounts.cooldown_retest_last_status_code,
      accounts.stream_failure_count,
      accounts.stream_failure_window_started_at,
      accounts.authorization_instance_source_account_id,
      accounts.authorization_instance_authorization_id,
      authorizations.status AS authorization_status,
      authorizations.expires_at AS authorization_expires_at,
      authorizations.limits_json AS authorization_limits_json,
      authorizations.effective_source_team_id AS authorization_effective_source_team_id,
      source_accounts.id AS source_id,
      source_accounts.status AS source_status,
      source_accounts.schedulable AS source_schedulable,
      source_accounts.account_expires_at AS source_account_expires_at,
      source_accounts.cooldown_until AS source_cooldown_until,
      source_accounts.last_error_code AS source_last_error_code,
      source_accounts.last_error_message AS source_last_error_message
    FROM ${authorizedDispatchTable(client, 'accounts')} accounts
    INNER JOIN ${authorizedDispatchTable(client, 'resource_authorizations')} authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
      AND authorizations.resource_type = 'account'
      AND authorizations.resource_id = accounts.authorization_instance_source_account_id
      AND authorizations.grantee_system_account_id = accounts.system_account_id
    LEFT JOIN ${authorizedDispatchTable(client, 'accounts')} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    WHERE accounts.id = ?
      AND accounts.system_account_id = ?
      AND accounts.authorization_instance_authorization_id IS NOT NULL
      AND accounts.deleted_at IS NULL
    LIMIT 1${client.driver === 'postgres' ? ' FOR UPDATE OF accounts' : ''}
  `, [accountId, systemAccountId])
}

async function loadAuthorizedAccountBindingForUpdate(
  client: DatabaseClient,
  row: AuthorizedAccountDispatchRow
): Promise<AuthorizedAccountBindingRow | undefined> {
  return client.one<AuthorizedAccountBindingRow>(`
    SELECT group_id, account_authorization_id,
      local_priority, local_super_priority_enabled, local_fallback_enabled
    FROM ${authorizedDispatchTable(client, 'group_accounts')}
    WHERE account_id = ?
      AND system_account_id = ?
      AND account_authorization_id = ?
      AND enabled = 1
    ORDER BY updated_at DESC, group_id ASC
    LIMIT 1${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [row.id, row.system_account_id, row.authorization_instance_authorization_id])
}

function unchangedOutcome(
  row: AuthorizedAccountDispatchRow,
  binding: AuthorizedAccountBindingRow
): AuthorizedDispatchTransactionOutcome {
  return {
    id: row.id,
    configRevision: integerValue(row.config_revision),
    changedFields: [],
    patch: {},
    changes: [],
    name: row.name,
    ownerSystemAccountId: row.system_account_id,
    runtimeRestoreRequired: false,
    authorizedBinding: authorizedBindingResult(row, binding),
    groupStatsAffected: false,
    gatewayRuntimeAffected: false
  }
}

function authorizedBindingResult(
  row: AuthorizedAccountDispatchRow,
  binding: AuthorizedAccountBindingRow
): AuthorizedAccountDispatchMutationResult['authorizedBinding'] {
  return {
    systemAccountId: row.system_account_id,
    groupId: binding.group_id,
    accountAuthorizationId: binding.account_authorization_id
  }
}

function assertAuthorizedDispatchInput(input: AuthorizedAccountDispatchInput): void {
  if (!Number.isInteger(input.expectedConfigRevision) || input.expectedConfigRevision < 1) {
    throw new Error('账户配置版本无效')
  }
  const commandKeys = Object.keys(input).filter((key) => key !== 'expectedConfigRevision')
  if (commandKeys.length === 0
    || (commandKeys.length === 1 && commandKeys[0] === 'clearFailureState' && input.clearFailureState !== true)) {
    throw new Error('请至少提交一项授权账户调度变更')
  }
}

function authorizedBindingSystemAccountId(access?: AccessScope): string {
  return scopedSystemAccountId(access) ?? currentSystemAccountId(access)
}

async function authorizedDispatchDatabaseClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

function authorizedDispatchTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function normalizedPriority(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0) {
    throw new Error('账户优先级必须是大于等于 0 的整数')
  }
  return value
}

function integerValue(value: number | bigint | string | null | undefined): number {
  const parsed = Number(value ?? 0)
  return Number.isFinite(parsed) ? Math.trunc(parsed) : 0
}

function nullableInteger(value: number | bigint | string | null | undefined): number | null {
  return value === null || value === undefined ? null : integerValue(value)
}

function databaseBoolean(value: number | boolean | string | null | undefined): boolean {
  return value === true || value === 1 || value === '1' || value === 'true'
}

function setColumnIfChanged(
  columns: Map<string, unknown>,
  column: string,
  before: unknown,
  after: unknown
): void {
  if (!isDeepStrictEqual(before, after)) columns.set(column, after)
}

function hasOwn(value: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key)
}

function isExpired(value: string | null | undefined, now: number): boolean {
  if (!value) return false
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) && timestamp <= now
}

function isFuture(value: string | null | undefined, now: number): boolean {
  if (!value) return false
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) && timestamp > now
}
