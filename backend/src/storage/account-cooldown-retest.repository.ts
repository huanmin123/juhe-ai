import type { AccountSummary } from '../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../domain/provider-protocol.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { loadAccountCurrentConcurrencyByIds } from '../shared/account-concurrency.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
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
import { isCoolingAccountStatus } from './account-status.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { listOpenAIProtocolProfileIds } from './provider.repository.js'
import { sqlPlaceholders } from './query-utils.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, ownerPermissions } from './resource-permissions.js'
import { invalidateAccountLookupCache, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import type { AccountListRow } from './repository-row-types.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { getSettings } from './settings.repository.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { optionalString } from './value-utils.js'

const temporaryUnavailableInitialBackoffSeconds = 3
const temporaryUnavailableFastThresholdSeconds = 60
const temporaryUnavailableBackoffMultiplier = 2
const cooldownRetestLongTermUnavailableCode = 'cooldown_retest_long_term_unavailable'

export interface CooldownAccountRetestFailureInput {
  traceId?: string
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  initialBackoffSeconds?: number
  fastThresholdSeconds?: number
  maxPauseMinutes?: number
  maxRecoveryHours?: number
  longTermIntervalHours?: number
  backoffMultiplier?: number
}

export interface CooldownAccountRetestFailureResult {
  action: 'retry_immediately' | 'cooldown' | 'long_term_cooldown' | 'discard'
  changed: boolean
  failureCount: number
  account?: AccountSummary
  cooldownUntil?: string
  backoffSeconds?: number
  backoffMinutes?: number
  recoveryStage?: 'fast' | 'slow' | 'long_term'
  fastThresholdSeconds?: number
  maxPauseSeconds?: number
  maxRecoverySeconds?: number
  longTermIntervalSeconds?: number
  maxedFailureCount?: number
  observationStartedAt?: string
  observationElapsedSeconds?: number
  errorCode: string
  errorMessage: string
}

export function findAccountForCooldownRetest(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  return cooldownRetestDueAccountSummaries(queryAccountsDueForCooldownRetest(1, accountId))[0]
}

export function listAccountsDueForCooldownRetest(limit = 20): AccountSummary[] {
  disableExpiredAccounts()
  const normalizedLimit = normalizedCooldownRetestLimit(limit)
  return cooldownRetestDueAccountSummaries(queryAccountsDueForCooldownRetest(cooldownRetestScanLimit(normalizedLimit))).slice(0, normalizedLimit)
}

export function recordCooldownAccountRetestFailure(id: string, input: CooldownAccountRetestFailureInput): CooldownAccountRetestFailureResult {
  const current = findAccountCooldownRetestState(id)
  const errorCode = normalizedCooldownRetestErrorCode(input)
  const testErrorMessage = normalizedCooldownRetestErrorMessage(input, errorCode)
  if (!current || !isCoolingAccountStatus(current.status)) {
    return {
      action: 'discard',
      changed: false,
      failureCount: current?.cooldownRetestFailureCount ?? 0,
      account: current,
      errorCode,
      errorMessage: testErrorMessage
    }
  }

  const nowDate = new Date()
  const now = nowDate.toISOString()
  const failureCount = Math.max(0, current.cooldownRetestFailureCount ?? 0) + 1
  const lastStatusCode = typeof input.statusCode === 'number' && Number.isFinite(input.statusCode) ? Math.trunc(input.statusCode) : null
  const observationStartedAt = current.cooldownRetestObservationStartedAt ?? now
  const recovery = cooldownRetestRecoveryPlan(failureCount, input, nowDate, observationStartedAt)

  const cooldownUntil = new Date(nowDate.getTime() + recovery.backoffSeconds * 1000).toISOString()
  const persistedErrorCode = recovery.stage === 'long_term'
    ? cooldownRetestLongTermUnavailableCode
    : errorCode
  const cooldownMessage = recovery.stage === 'long_term'
    ? cooldownRetestLongTermMessage(failureCount, recovery.maxRecoverySeconds, recovery.backoffSeconds, testErrorMessage)
    : cooldownRetestCooldownMessage(failureCount, recovery.backoffSeconds, recovery.stage, testErrorMessage)
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET schedulable = 1,
          cooldown_until = ?,
          last_error_code = ?,
          last_error_message = ?,
          cooldown_retest_failure_count = ?,
          cooldown_retest_observation_started_at = COALESCE(cooldown_retest_observation_started_at, ?),
          cooldown_retest_last_at = ?,
          cooldown_retest_last_status_code = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND status = ?
    `)
    .run(cooldownUntil, persistedErrorCode, cooldownMessage, failureCount, observationStartedAt, now, lastStatusCode, now, id, current.status)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown_retest_backoff' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown_retest_backoff')
  }
  const action = recovery.stage === 'fast'
    ? 'retry_immediately'
    : recovery.stage === 'long_term' ? 'long_term_cooldown' : 'cooldown'
  return {
    action,
    changed,
    failureCount,
    account: failureAccountSummary(id, current),
    cooldownUntil,
    backoffSeconds: recovery.backoffSeconds,
    backoffMinutes: secondsToCeilMinutes(recovery.backoffSeconds),
    recoveryStage: recovery.stage,
    fastThresholdSeconds: recovery.fastThresholdSeconds,
    maxPauseSeconds: recovery.maxPauseSeconds,
    maxRecoverySeconds: recovery.maxRecoverySeconds,
    longTermIntervalSeconds: recovery.longTermIntervalSeconds,
    maxedFailureCount: recovery.maxedFailureCount,
    observationStartedAt: recovery.observationStartedAt,
    observationElapsedSeconds: recovery.observationElapsedSeconds,
    errorCode: persistedErrorCode,
    errorMessage: cooldownMessage
  }
}

function findAccountCooldownRetestState(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  return cooldownRetestAccountSummaries(queryAccountCooldownRetestState(accountId))[0]
}

function queryAccountsDueForCooldownRetest(limit: number, accountId?: string): AccountListRow[] {
  const providerProtocolProfileIds = openAIProtocolProfileIdsForQuery()
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND accounts.id = ?' : ''
  const params: Array<string | number> = [
    ...providerProtocolProfileIds,
    now,
    now,
    now
  ]
  if (accountId) {
    params.push(accountId)
  }
  params.push(normalizedCooldownRetestLimit(limit))
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${cooldownRetestAccountSelectColumns()}
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      WHERE accounts.provider_protocol_profile_id IN (${sqlPlaceholders(providerProtocolProfileIds.length)})
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.deleted_at IS NULL
        AND accounts.status IN ('temporary_unavailable', 'rate_limited')
        AND accounts.schedulable = 1
        AND accounts.cooldown_until IS NOT NULL
        AND accounts.cooldown_until <= ?
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR (
            ra.id IS NOT NULL
            AND ra.status = 'active'
            AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          )
        )
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
      ORDER BY accounts.cooldown_until ASC, accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(...params) as unknown as AccountListRow[]
  const scheduledRows = rows.filter((row) => row.availability_schedule_active !== 0)
  return hydrateAccountRowsWithRuntimeState(scheduledRows, { includeCredentials: true })
    .filter((row) => row.access_type !== 'authorized' || (
      Boolean(row.source_provider_code)
      && isAuthorizedSourceAccountAvailableForDispatch(row, now)
    ))
}

function queryAccountCooldownRetestState(accountId: string): AccountListRow[] {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) return []
  const providerProtocolProfileIds = openAIProtocolProfileIdsForQuery()
  return hydrateAccountRowsWithRuntimeState(getBusinessDatabase()
    .prepare(`
      SELECT ${cooldownRetestAccountSelectColumns()}
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      WHERE accounts.id = ?
        AND accounts.provider_protocol_profile_id IN (${sqlPlaceholders(providerProtocolProfileIds.length)})
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.deleted_at IS NULL
      LIMIT 1
    `)
    .all(normalizedAccountId, ...providerProtocolProfileIds) as unknown as AccountListRow[], { includeCredentials: true })
}

function cooldownRetestAccountSelectColumns(): string {
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

function cooldownRetestAccountSummaries(rows: AccountListRow[]): AccountSummary[] {
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
      availabilityScheduleActive: row.availability_schedule_active !== 0,
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: optionalNumber(row.cooldown_retest_last_status_code),
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
      authorizationInstanceSourceAccountScheduleActive: isAuthorizedView ? row.source_availability_schedule_active !== 0 : undefined,
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
      authorizationLimits: isAuthorizedView ? parseRequestQuotaLimitsJson(row.authorization_limits_json) : undefined,
      authorizationQuotaExceeded: isAuthorizedView && row.authorization_id ? quotaExceededByAuthorization.get(row.authorization_id) : undefined,
      permissions: isAuthorizedView ? authorizedAccountPermissions(false) : ownerPermissions()
    })
  })
}

function cooldownRetestDueAccountSummaries(rows: AccountListRow[]): AccountSummary[] {
  return cooldownRetestAccountSummaries(rows).filter((account) => {
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

function normalizedCooldownRetestLimit(limit: number): number {
  return Math.max(1, Math.min(Math.trunc(limit), 200))
}

function cooldownRetestScanLimit(limit: number): number {
  return Math.max(limit, 200)
}

function normalizedCooldownRetestErrorCode(input: CooldownAccountRetestFailureInput): string {
  const code = optionalString(input.errorCode)
  if (code) return code.slice(0, 120)
  if (typeof input.statusCode === 'number' && Number.isFinite(input.statusCode)) {
    return `http_${Math.trunc(input.statusCode)}`
  }
  return 'cooldown_retest_failed'
}

function failureAccountSummary(id: string, fallback: AccountSummary): AccountSummary {
  return findAccountCooldownRetestState(id) ?? fallback
}

function normalizedCooldownRetestErrorMessage(input: CooldownAccountRetestFailureInput, errorCode: string): string {
  const message = optionalString(input.errorMessage) ?? '后台冷却复测失败'
  const parts: string[] = []
  const traceId = optionalString(input.traceId)
  if (traceId && !message.includes(traceId)) {
    parts.push(`traceId ${traceId}`)
  }
  if (typeof input.statusCode === 'number' && Number.isFinite(input.statusCode)) {
    parts.push(`HTTP ${Math.trunc(input.statusCode)}`)
  }
  if (errorCode && !errorCode.startsWith('http_') && !message.includes(errorCode)) {
    parts.push(errorCode)
  }
  parts.push(message)
  return parts.join('；').slice(0, 1000)
}

interface CooldownRetestRecoveryPlan {
  stage: 'fast' | 'slow' | 'long_term'
  backoffSeconds: number
  fastThresholdSeconds: number
  maxPauseSeconds: number
  maxRecoverySeconds: number
  longTermIntervalSeconds: number
  maxedFailureCount: number
  observationStartedAt: string
  observationElapsedSeconds: number
}

function cooldownRetestRecoveryPlan(failureCount: number, input: CooldownAccountRetestFailureInput, nowDate: Date, observationStartedAt: string): CooldownRetestRecoveryPlan {
  const initialBackoffSeconds = boundedInteger(input.initialBackoffSeconds, temporaryUnavailableInitialBackoffSeconds, 1, 3600)
  const fastThresholdSeconds = boundedInteger(input.fastThresholdSeconds, temporaryUnavailableFastThresholdSeconds, initialBackoffSeconds, 3600)
  const maxPauseSeconds = boundedInteger(input.maxPauseMinutes, defaultTemporaryUnschedulableMinutes(), 1, 1440) * 60
  const maxRecoverySeconds = boundedInteger(input.maxRecoveryHours, 12, 1, 24 * 30) * 60 * 60
  const longTermIntervalSeconds = boundedInteger(input.longTermIntervalHours, 24, 1, 24 * 30) * 60 * 60
  const multiplier = boundedInteger(input.backoffMultiplier, temporaryUnavailableBackoffMultiplier, 2, 10)
  const exponent = Math.max(0, Math.min(failureCount, 30))
  const uncappedBackoffSeconds = Math.min(Number.MAX_SAFE_INTEGER, initialBackoffSeconds * Math.pow(multiplier, exponent))
  const firstMaxedFailureCount = firstCappedBackoffFailureCount(initialBackoffSeconds, multiplier, maxPauseSeconds)
  const maxedFailureCount = failureCount >= firstMaxedFailureCount ? failureCount - firstMaxedFailureCount + 1 : 0
  const observationElapsedSeconds = cooldownRetestObservationElapsedSeconds(observationStartedAt, nowDate)
  const inLongTermStage = observationElapsedSeconds >= maxRecoverySeconds
  const backoffSeconds = inLongTermStage
    ? longTermIntervalSeconds
    : Math.min(uncappedBackoffSeconds, maxPauseSeconds)
  const stage = inLongTermStage
    ? 'long_term'
    : backoffSeconds <= fastThresholdSeconds ? 'fast' : 'slow'
  return {
    stage,
    backoffSeconds,
    fastThresholdSeconds,
    maxPauseSeconds,
    maxRecoverySeconds,
    longTermIntervalSeconds,
    maxedFailureCount,
    observationStartedAt,
    observationElapsedSeconds
  }
}

function cooldownRetestObservationElapsedSeconds(observationStartedAt: string, nowDate: Date): number {
  const startedAtMs = Date.parse(observationStartedAt)
  if (!Number.isFinite(startedAtMs)) {
    return 0
  }
  return Math.max(0, Math.floor((nowDate.getTime() - startedAtMs) / 1000))
}

function firstCappedBackoffFailureCount(initialBackoffSeconds: number, multiplier: number, maxPauseSeconds: number): number {
  if (initialBackoffSeconds >= maxPauseSeconds) {
    return 1
  }
  const raw = Math.ceil(Math.log(maxPauseSeconds / initialBackoffSeconds) / Math.log(multiplier))
  return Math.max(1, raw)
}

function cooldownRetestCooldownMessage(failureCount: number, backoffSeconds: number, stage: 'fast' | 'slow', lastError: string): string {
  const stageText = stage === 'fast' ? '快速恢复通道' : '慢速恢复通道'
  return `后台冷却复测连续失败 ${failureCount} 次，${stageText}下次复测延后 ${formatDurationSeconds(backoffSeconds)}；最后错误：${lastError}`.slice(0, 1000)
}

function cooldownRetestLongTermMessage(failureCount: number, maxRecoverySeconds: number, backoffSeconds: number, lastError: string): string {
  return `后台冷却复测连续失败 ${failureCount} 次，已超过自动恢复观察窗口 ${formatDurationSeconds(maxRecoverySeconds)}，进入长期不可用低频复测；下次复测延后 ${formatDurationSeconds(backoffSeconds)}；最后错误：${lastError}`.slice(0, 1000)
}

function secondsToCeilMinutes(seconds: number): number {
  return Math.max(1, Math.ceil(Math.max(1, seconds) / 60))
}

function formatDurationSeconds(seconds: number): string {
  const safeSeconds = Math.max(1, Math.trunc(seconds))
  if (safeSeconds < 60) return `${safeSeconds} 秒`
  const minutes = Math.floor(safeSeconds / 60)
  const restSeconds = safeSeconds % 60
  if (minutes < 60) {
    return restSeconds > 0 ? `${minutes} 分钟 ${restSeconds} 秒` : `${minutes} 分钟`
  }
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  return restMinutes > 0 ? `${hours} 小时 ${restMinutes} 分钟` : `${hours} 小时`
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number): number {
  const parsed = typeof value === 'number' ? value : NaN
  if (!Number.isFinite(parsed)) return fallback
  return Math.min(Math.max(Math.trunc(parsed), min), max)
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
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

function defaultTemporaryUnschedulableMinutes(): number {
  const value = getSettings().defaultTemporaryUnschedulableMinutes
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须是整数')
  }
  if (value < 1 || value > 1440) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须在 1 到 1440 之间')
  }
  return value
}

function invalidateGatewayRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
}
