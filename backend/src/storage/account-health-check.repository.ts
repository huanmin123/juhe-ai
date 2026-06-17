import type { AccountSummary } from '../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../domain/provider-protocol.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { loadAccountCurrentConcurrencyByIds } from '../shared/account-concurrency.js'
import {
  isAccountAvailabilityScheduleAllowed,
  parseAccountAvailabilityScheduleJson
} from './account-availability-schedule.js'
import { hydrateAccountRowsWithRuntimeState } from './account-read.repository.js'
import { disableExpiredAccounts } from './account-runtime-status.js'
import {
  accountGroupBinding,
  accountResourceClientCompatibility,
  accountResourceConcurrencyLimit,
  accountResourceProtocolCode,
  accountResourceProtocolVersion,
  accountResourceProviderCode,
  accountResourceProviderProtocolProfileId,
  accountResourceProxyProfileId,
  accountResourceType,
  isAuthorizedSourceAccountAvailableForDispatch,
  loadAuthorizationQuotaExceededByAuthorizationId
} from './account-summary.repository.js'
import { decryptJson } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { listOpenAIProtocolProfileIds } from './provider.repository.js'
import { sqlPlaceholders } from './query-utils.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, ownerPermissions } from './resource-permissions.js'
import { invalidateAccountLookupCache, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import type { AccountListRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { optionalString } from './value-utils.js'

export interface AccountHealthCheckSettings {
  intervalHours: number
  jitterMinutes: number
  failureThreshold: number
}

export interface AccountHealthCheckListOptions extends AccountHealthCheckSettings {
  limit: number
}

export interface AccountHealthCheckFailureInput extends AccountHealthCheckSettings {
  statusCode?: number
  errorCode?: string
  errorMessage?: string
}

export interface AccountHealthCheckFailureResult {
  changed: boolean
  failureCount: number
  reachedThreshold: boolean
  nextHealthCheckAt: string
  errorCode: string
  errorMessage: string
}

export function listAccountsDueForHealthCheck(options: AccountHealthCheckListOptions): AccountSummary[] {
  disableExpiredAccounts()
  const normalizedLimit = normalizedHealthCheckLimit(options.limit)
  const rows = queryAccountsDueForHealthCheck(healthCheckScanLimit(normalizedLimit), undefined)
  const dueRows = dueHealthCheckRows(rows, options).slice(0, normalizedLimit)
  return healthCheckAccountSummaries(dueRows)
}

export function findAccountForHealthCheck(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  return healthCheckAccountSummaries(queryAccountsDueForHealthCheck(1, accountId))[0]
}

export function recordAccountHealthCheckSuccess(accountId: string, input: AccountHealthCheckSettings & {
  checkedAt?: string
  statusCode?: number
}): boolean {
  const checkedAt = normalizedIso(input.checkedAt) ?? nowIso()
  const nextHealthCheckAt = nextHealthCheckAtForAccount(accountId, checkedAt, input)
  const statusCode = normalizedStatusCode(input.statusCode)
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET last_health_check_at = ?,
          last_health_success_at = ?,
          next_health_check_at = ?,
          health_check_failure_count = 0,
          last_health_check_status_code = ?,
          last_health_check_error_code = NULL,
          last_health_check_error_message = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(checkedAt, checkedAt, nextHealthCheckAt, statusCode, checkedAt, accountId)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateAccountLookupCache(accountId)
  }
  return changed
}

export function recordAccountHealthCheckFailure(accountId: string, input: AccountHealthCheckFailureInput): AccountHealthCheckFailureResult {
  const database = getBusinessDatabase()
  const row = database
    .prepare(`
      SELECT health_check_failure_count
      FROM accounts
      WHERE id = ?
        AND deleted_at IS NULL
      LIMIT 1
    `)
    .get(accountId) as unknown as { health_check_failure_count?: number } | undefined
  const previousFailureCount = Math.max(0, Math.trunc(Number(row?.health_check_failure_count ?? 0)))
  const failureCount = previousFailureCount + 1
  const now = nowIso()
  const nextHealthCheckAt = nextHealthCheckAtAfterFailure(now, failureCount, input.intervalHours)
  const errorCode = normalizedHealthCheckErrorCode(input)
  const errorMessage = normalizedHealthCheckErrorMessage(input, errorCode)
  const statusCode = normalizedStatusCode(input.statusCode)
  const result = database
    .prepare(`
      UPDATE accounts
      SET last_health_check_at = ?,
          next_health_check_at = ?,
          health_check_failure_count = ?,
          last_health_check_status_code = ?,
          last_health_check_error_code = ?,
          last_health_check_error_message = ?,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(now, nextHealthCheckAt, failureCount, statusCode, errorCode, errorMessage, now, accountId)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateAccountLookupCache(accountId)
  }
  return {
    changed,
    failureCount,
    reachedThreshold: failureCount >= normalizedFailureThreshold(input.failureThreshold),
    nextHealthCheckAt,
    errorCode,
    errorMessage
  }
}

export function recordAccountHealthSuccessSignals(
  accountSuccessAt: Map<string, string>,
  options: Partial<AccountHealthCheckSettings> = {}
): void {
  if (accountSuccessAt.size === 0) return
  const settings = normalizedHealthCheckSettings(options)
  const database = getBusinessDatabase()
  const statement = database.prepare(`
    UPDATE accounts
    SET last_health_success_at = ?,
        next_health_check_at = ?,
        health_check_failure_count = 0,
        last_health_check_error_code = NULL,
        last_health_check_error_message = NULL,
        updated_at = ?
    WHERE id = ?
      AND deleted_at IS NULL
      AND health_check_enabled = 1
      AND (
        last_health_success_at IS NULL
        OR last_health_success_at <= ?
      )
      AND (
        next_health_check_at IS NULL
        OR next_health_check_at < ?
        OR next_health_check_at > ?
        OR health_check_failure_count <> 0
        OR last_health_check_error_code IS NOT NULL
        OR last_health_check_error_message IS NOT NULL
      )
  `)
  const changedIds: string[] = []
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const [accountId, successAt] of accountSuccessAt) {
      const normalizedSuccessAt = normalizedIso(successAt)
      if (!normalizedSuccessAt) continue
      const nextHealthCheckAt = nextHealthCheckAtForAccount(accountId, normalizedSuccessAt, settings)
      const refreshAfterAt = healthSuccessRefreshAfterAt(normalizedSuccessAt, settings.intervalHours)
      const result = statement.run(
        normalizedSuccessAt,
        nextHealthCheckAt,
        normalizedSuccessAt,
        accountId,
        normalizedSuccessAt,
        refreshAfterAt,
        nextHealthCheckAt
      )
      if (Number(result.changes ?? 0) > 0) {
        changedIds.push(accountId)
      }
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  for (const accountId of changedIds) {
    invalidateAccountLookupCache(accountId)
  }
}

function dueHealthCheckRows(rows: AccountListRow[], options: AccountHealthCheckSettings): AccountListRow[] {
  const nowMs = Date.now()
  const cutoffMs = nowMs - normalizedIntervalHours(options.intervalHours) * 60 * 60_000
  const dueRows: AccountListRow[] = []
  const recentSuccessSignals = new Map<string, string>()
  for (const row of rows) {
    if (!isAccountAvailabilityScheduleAllowed(row.availability_schedule_json)) {
      continue
    }
    const recentSuccessAt = normalizedIso(row.last_health_success_at)
    const recentSuccessMs = recentSuccessAt ? Date.parse(recentSuccessAt) : NaN
    if (recentSuccessAt && Number.isFinite(recentSuccessMs) && recentSuccessMs >= cutoffMs) {
      recentSuccessSignals.set(row.id, recentSuccessAt)
      continue
    }
    dueRows.push(row)
  }
  recordAccountHealthSuccessSignals(recentSuccessSignals, options)
  return dueRows
}

function queryAccountsDueForHealthCheck(limit: number, accountId: string | undefined): AccountListRow[] {
  const providerProtocolProfileIds = openAIProtocolProfileIdsForQuery()
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND accounts.id = ?' : ''
  const params: Array<string | number> = [
    ...providerProtocolProfileIds,
    now,
    now,
    now,
    now
  ]
  if (accountId) {
    params.push(accountId)
  }
  params.push(normalizedHealthCheckLimit(limit))
  return hydrateAccountRowsWithRuntimeState(getBusinessDatabase()
    .prepare(`
      SELECT ${healthCheckAccountSelectColumns()}
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      WHERE accounts.provider_protocol_profile_id IN (${sqlPlaceholders(providerProtocolProfileIds.length)})
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.deleted_at IS NULL
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND accounts.health_check_enabled = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR (
            ra.id IS NOT NULL
            AND ra.status = 'active'
            AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          )
        )
        AND (accounts.next_health_check_at IS NULL OR accounts.next_health_check_at <= ?)
        ${accountIdFilter}
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = accounts.system_account_id
            AND group_accounts.enabled = 1
            AND (
              accounts.authorization_instance_authorization_id IS NULL
              OR group_accounts.account_authorization_id = accounts.authorization_instance_authorization_id
            )
        )
      ORDER BY accounts.next_health_check_at IS NOT NULL ASC,
        accounts.next_health_check_at ASC,
        accounts.last_health_check_at ASC,
        accounts.created_at ASC,
        accounts.id ASC
      LIMIT ?
    `)
    .all(...params) as unknown as AccountListRow[], { includeCredentials: true })
    .filter((row) => row.access_type !== 'authorized' || (
      Boolean(row.source_provider_code)
      && isAuthorizedSourceAccountAvailableForDispatch(row, now)
    ))
}

function healthCheckAccountSelectColumns(): string {
  return `
        accounts.*,
        CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' ELSE 'owner' END AS access_type,
        accounts.authorization_instance_authorization_id AS authorization_id,
        ra.status AS authorization_status,
        ra.expires_at AS authorization_expires_at,
        ra.limits_json AS authorization_limits_json,
        ra.effective_source_type AS authorization_effective_source_type,
        ra.effective_source_team_id AS authorization_effective_source_team_id,
        ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
        ra.resource_id AS authorization_resource_id`
}

function healthCheckAccountSummaries(rows: AccountListRow[]): AccountSummary[] {
  if (!rows.length) return []
  const accountNames = loadSystemAccountNameMapByIds(rows.flatMap((row) => [
    row.system_account_id,
    row.authorization_resource_owner_system_account_id ?? '',
    row.authorization_instance_owner_system_account_id ?? ''
  ]))
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(rows.map((row) => row.id))
  const quotaExceededByAuthorization = loadAuthorizationQuotaExceededByAuthorizationId(rows)
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const groupBinding = accountGroupBinding(row.id, row.system_account_id)
    const displayOwnerSystemAccountId = isAuthorizedView
      ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
      : row.system_account_id
    return accountSummaryWithEffectiveAvailability({
      id: row.id,
      systemAccountId: row.system_account_id,
      systemAccountName: accountNames.get(row.system_account_id),
      ownerSystemAccountId: displayOwnerSystemAccountId,
      ownerSystemAccountName: accountNames.get(displayOwnerSystemAccountId),
      providerCode: accountResourceProviderCode(row),
      providerProtocolProfileId: accountResourceProviderProtocolProfileId(row),
      protocolCode: accountResourceProtocolCode(row),
      protocolVersion: accountResourceProtocolVersion(row),
      name: row.name,
      notes: isAuthorizedView ? undefined : row.notes ?? undefined,
      type: accountResourceType(row),
      credentials: accountRuntimeCredentialsFromRow(row),
      status: row.status,
      concurrencyLimit: accountResourceConcurrencyLimit(row),
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: row.priority,
      superPriorityEnabled: row.super_priority_enabled === 1,
      fallbackEnabled: row.fallback_enabled === 1,
      clientCompatibility: accountResourceClientCompatibility(row),
      supportedModels: row.supported_models ?? [],
      modelMappings: row.model_mappings ?? [],
      lastSuccessfulTestModel: optionalString(row.last_successful_test_model),
      proxyProfileId: accountResourceProxyProfileId(row) ?? undefined,
      schedulable: row.schedulable === 1,
      availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: optionalNumber(row.cooldown_retest_last_status_code),
      healthCheckEnabled: row.health_check_enabled === 1,
      lastHealthCheckAt: row.last_health_check_at ?? undefined,
      nextHealthCheckAt: row.next_health_check_at ?? undefined,
      lastHealthSuccessAt: row.last_health_success_at ?? undefined,
      healthCheckFailureCount: Math.max(0, Number(row.health_check_failure_count ?? 0)),
      lastHealthCheckStatusCode: optionalNumber(row.last_health_check_status_code),
      lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
      lastHealthCheckErrorMessage: row.last_health_check_error_message ?? undefined,
      lastUsedAt: row.last_used_at ?? undefined,
      todayUsage: emptyAccountUsageSummary(),
      usage: emptyAccountUsageSummary(),
      oauthUsage: undefined,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: isAuthorizedView ? row.authorization_id ?? undefined : undefined,
      authorizationInstanceSourceAccountId: isAuthorizedView ? row.authorization_instance_source_account_id ?? undefined : undefined,
      authorizationInstanceOwnerSystemAccountId: isAuthorizedView ? row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined : undefined,
      authorizationInstanceSourceAccountStatus: isAuthorizedView ? row.source_status ?? undefined : undefined,
      authorizationInstanceSourceAccountSchedulable: isAuthorizedView && typeof row.source_schedulable === 'number' ? row.source_schedulable === 1 : undefined,
      authorizationInstanceSourceAccountAvailabilitySchedule: isAuthorizedView ? parseAccountAvailabilityScheduleJson(row.source_availability_schedule_json) : undefined,
      authorizationInstanceSourceAccountScheduleActive: isAuthorizedView ? isAccountAvailabilityScheduleAllowed(row.source_availability_schedule_json) : undefined,
      authorizationInstanceSourceAccountExpiresAt: isAuthorizedView ? row.source_account_expires_at ?? undefined : undefined,
      authorizationInstanceSourceAccountCooldownUntil: isAuthorizedView ? row.source_cooldown_until ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorCode: isAuthorizedView ? row.source_last_error_code ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorMessage: isAuthorizedView ? row.source_last_error_message ?? undefined : undefined,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      bindingSystemAccountId: isAuthorizedView && groupBinding ? row.system_account_id : undefined,
      authorizationStatus: isAuthorizedView ? row.authorization_status ?? undefined : undefined,
      authorizationExpiresAt: isAuthorizedView ? row.authorization_expires_at ?? undefined : undefined,
      authorizationQuotaExceeded: isAuthorizedView && row.authorization_id ? quotaExceededByAuthorization.get(row.authorization_id) : undefined,
      permissions: isAuthorizedView ? authorizedAccountPermissions(false) : ownerPermissions()
    })
  }).filter((account) => {
    if (account.accessType !== 'authorized') return true
    if (!account.accountAuthorizationId || !account.bindingSystemAccountId || !account.boundGroupId || account.groupBindStatus !== 'bound') {
      return false
    }
    if (account.authorizationStatus && account.authorizationStatus !== 'active') {
      return false
    }
    if (isResourceAuthorizationExpired(account.authorizationExpiresAt)) {
      return false
    }
    if (account.authorizationQuotaExceeded) {
      return false
    }
    return true
  })
}

export function normalizedHealthCheckSettings(input: Partial<AccountHealthCheckSettings> = {}): AccountHealthCheckSettings {
  const settings = getSettings()
  return {
    intervalHours: normalizedIntervalHours(input.intervalHours ?? settings.accountHealthCheckIntervalHours),
    jitterMinutes: normalizedJitterMinutes(input.jitterMinutes ?? settings.accountHealthCheckJitterMinutes),
    failureThreshold: normalizedFailureThreshold(input.failureThreshold ?? settings.accountHealthCheckFailureThreshold)
  }
}

function nextHealthCheckAtForAccount(accountId: string, baseIso: string, options: Pick<AccountHealthCheckSettings, 'intervalHours' | 'jitterMinutes'>): string {
  const baseMs = Date.parse(baseIso)
  const safeBaseMs = Number.isFinite(baseMs) ? baseMs : Date.now()
  const intervalMs = normalizedIntervalHours(options.intervalHours) * 60 * 60_000
  const jitterMs = stableAccountJitterMs(accountId, normalizedJitterMinutes(options.jitterMinutes))
  return new Date(safeBaseMs + intervalMs + jitterMs).toISOString()
}

function nextHealthCheckAtAfterFailure(baseIso: string, failureCount: number, intervalHours: number): string {
  const baseMs = Date.parse(baseIso)
  const safeBaseMs = Number.isFinite(baseMs) ? baseMs : Date.now()
  const exponent = Math.max(0, Math.min(Math.trunc(failureCount) - 1, 8))
  const backoffMinutes = Math.min(normalizedIntervalHours(intervalHours) * 60, 15 * Math.pow(2, exponent))
  return new Date(safeBaseMs + Math.max(5, backoffMinutes) * 60_000).toISOString()
}

function healthSuccessRefreshAfterAt(successAt: string, intervalHours: number): string {
  const successMs = Date.parse(successAt)
  const safeSuccessMs = Number.isFinite(successMs) ? successMs : Date.now()
  const intervalMs = normalizedIntervalHours(intervalHours) * 60 * 60_000
  return new Date(safeSuccessMs + Math.max(5 * 60_000, Math.floor(intervalMs / 2))).toISOString()
}

function stableAccountJitterMs(accountId: string, jitterMinutes: number): number {
  const normalizedJitterMinutes = normalizedJitterMinutesValue(jitterMinutes)
  if (normalizedJitterMinutes <= 0) return 0
  let hash = 2166136261
  for (let index = 0; index < accountId.length; index += 1) {
    hash ^= accountId.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  const bucket = Math.abs(hash >>> 0) % (normalizedJitterMinutes * 60)
  return bucket * 1000
}

function normalizedHealthCheckLimit(limit: number): number {
  const parsed = Math.trunc(Number(limit))
  return Number.isFinite(parsed) ? Math.max(1, Math.min(parsed, 200)) : 20
}

function healthCheckScanLimit(limit: number): number {
  return Math.max(limit, 200)
}

function normalizedIntervalHours(value: unknown): number {
  const parsed = Math.trunc(Number(value))
  return Number.isFinite(parsed) ? Math.max(1, Math.min(parsed, 168)) : 12
}

function normalizedJitterMinutes(value: unknown): number {
  return normalizedJitterMinutesValue(value)
}

function normalizedJitterMinutesValue(value: unknown): number {
  const parsed = Math.trunc(Number(value))
  return Number.isFinite(parsed) ? Math.max(0, Math.min(parsed, 1440)) : 120
}

function normalizedFailureThreshold(value: unknown): number {
  const parsed = Math.trunc(Number(value))
  return Number.isFinite(parsed) ? Math.max(1, Math.min(parsed, 10)) : 3
}

function normalizedHealthCheckErrorCode(input: AccountHealthCheckFailureInput): string {
  const code = optionalString(input.errorCode)
  if (code) return code.slice(0, 120)
  if (typeof input.statusCode === 'number' && Number.isFinite(input.statusCode)) {
    return `http_${Math.trunc(input.statusCode)}`
  }
  return 'account_health_check_failed'
}

function normalizedHealthCheckErrorMessage(input: AccountHealthCheckFailureInput, errorCode: string): string {
  const message = optionalString(input.errorMessage) ?? '后台健康检测失败'
  const parts: string[] = []
  if (typeof input.statusCode === 'number' && Number.isFinite(input.statusCode)) {
    parts.push(`HTTP ${Math.trunc(input.statusCode)}`)
  }
  if (errorCode && !errorCode.startsWith('http_') && !message.includes(errorCode)) {
    parts.push(errorCode)
  }
  parts.push(message)
  return parts.join('；').slice(0, 1000)
}

function normalizedStatusCode(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : null
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function normalizedIso(value: unknown): string | undefined {
  const text = optionalString(value)
  if (!text) return undefined
  const time = Date.parse(text)
  return Number.isFinite(time) ? new Date(time).toISOString() : undefined
}

function accountRuntimeCredentialsFromRow(row: AccountListRow): Record<string, unknown> {
  if (row.access_type === 'authorized') {
    return row.source_credentials_encrypted ? decryptJson<Record<string, unknown>>(row.source_credentials_encrypted) : {}
  }
  return decryptJson<Record<string, unknown>>(row.credentials_encrypted)
}

function openAIProtocolProfileIdsForQuery(): string[] {
  const profileIds = listOpenAIProtocolProfileIds().map((profileId) => profileId.trim()).filter(Boolean)
  return profileIds.length ? profileIds : [GPT_OPENAI_V1_PROFILE_ID]
}
