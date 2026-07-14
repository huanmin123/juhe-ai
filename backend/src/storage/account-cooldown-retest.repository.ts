import type { AccountSummary } from '../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../domain/provider-protocol.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { runtimeConfig } from '../config/runtime.js'
import { loadAccountCurrentConcurrencyByIds, loadAccountCurrentConcurrencyByIdsAsync } from '../shared/account-concurrency.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { hydrateAccountRowsWithRuntimeState } from './account-read.repository.js'
import { disableExpiredAccounts, disableExpiredAccountsAsync } from './account-runtime-status.js'
import {
  accountGroupBindingFromRow,
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
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { loadModelMappingsByAccountIdsAsync } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIdsAsync } from './account-supported-models.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { listOpenAIProtocolProfileIds, listOpenAIProtocolProfileIdsAsync } from './provider.repository.js'
import { sqlPlaceholders } from './query-utils.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, ownerPermissions } from './resource-permissions.js'
import { invalidateAccountLookupCache, loadSystemAccountNameMapByIds, loadSystemAccountNameMapByIdsAsync } from './repository-lookups.js'
import type { AccountListRow } from './repository-row-types.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { DEFAULT_SYSTEM_SETTINGS } from './schema-defaults.js'
import { getSettings, getSettingsAsync } from './settings.repository.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { optionalString } from './value-utils.js'

const temporaryUnavailableInitialBackoffSeconds = 3
const temporaryUnavailableFastThresholdSeconds = 60
const temporaryUnavailableBackoffMultiplier = 2
const cooldownRetestLongTermUnavailableCode = 'cooldown_retest_long_term_unavailable'
const businessSchemaName = 'juhe_business'
const defaultSystemSettingsByKey = new Map<string, unknown>(DEFAULT_SYSTEM_SETTINGS.map(([key, value]) => [key, value]))

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

export interface CooldownAccountRetestCursor {
  cooldownUntil: string
  priority: number
  createdAt: string
  id: string
}

export interface CooldownAccountRetestPage {
  accounts: AccountSummary[]
  nextCursor?: CooldownAccountRetestCursor
}

export function findAccountForCooldownRetest(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  return cooldownRetestDueAccountSummaries(queryAccountsDueForCooldownRetest(1, accountId))[0]
}

export async function findAccountForCooldownRetestAsync(accountId: string): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findAccountForCooldownRetest(accountId)
  }
  await disableExpiredAccountsAsync()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return (await cooldownRetestDueAccountSummariesAsync(client, await queryAccountsDueForCooldownRetestAsync(client, 1, accountId)))[0]
}

export function listAccountsDueForCooldownRetest(limit = 20): AccountSummary[] {
  return listAccountsDueForCooldownRetestPage(limit).accounts
}

export async function listAccountsDueForCooldownRetestAsync(limit = 20): Promise<AccountSummary[]> {
  return (await listAccountsDueForCooldownRetestPageAsync(limit)).accounts
}

export function listAccountsDueForCooldownRetestPage(
  limit = 20,
  cursor?: CooldownAccountRetestCursor
): CooldownAccountRetestPage {
  disableExpiredAccounts()
  const normalizedLimit = normalizedCooldownRetestLimit(limit)
  const rows = queryAccountsDueForCooldownRetest(cooldownRetestScanLimit(normalizedLimit), undefined, cursor)
  return cooldownRetestPageFromRows(rows, cooldownRetestDueAccountSummaries(rows), normalizedLimit)
}

export async function listAccountsDueForCooldownRetestPageAsync(
  limit = 20,
  cursor?: CooldownAccountRetestCursor
): Promise<CooldownAccountRetestPage> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAccountsDueForCooldownRetestPage(limit, cursor)
  }
  await disableExpiredAccountsAsync()
  const normalizedLimit = normalizedCooldownRetestLimit(limit)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await queryAccountsDueForCooldownRetestAsync(client, cooldownRetestScanLimit(normalizedLimit), undefined, cursor)
  return cooldownRetestPageFromRows(rows, await cooldownRetestDueAccountSummariesAsync(client, rows), normalizedLimit)
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

export async function recordCooldownAccountRetestFailureAsync(id: string, input: CooldownAccountRetestFailureInput): Promise<CooldownAccountRetestFailureResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordCooldownAccountRetestFailure(id, input)
  }
  const current = await findAccountCooldownRetestStateAsync(id)
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
  const recovery = cooldownRetestRecoveryPlan(failureCount, {
    ...input,
    maxPauseMinutes: input.maxPauseMinutes ?? await defaultTemporaryUnschedulableMinutesAsync()
  }, nowDate, observationStartedAt)

  const cooldownUntil = new Date(nowDate.getTime() + recovery.backoffSeconds * 1000).toISOString()
  const persistedErrorCode = recovery.stage === 'long_term'
    ? cooldownRetestLongTermUnavailableCode
    : errorCode
  const cooldownMessage = recovery.stage === 'long_term'
    ? cooldownRetestLongTermMessage(failureCount, recovery.maxRecoverySeconds, recovery.backoffSeconds, testErrorMessage)
    : cooldownRetestCooldownMessage(failureCount, recovery.backoffSeconds, recovery.stage, testErrorMessage)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${cooldownRetestTable(client, 'accounts')}
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
  `, [cooldownUntil, persistedErrorCode, cooldownMessage, failureCount, observationStartedAt, now, lastStatusCode, now, id, current.status])
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_cooldown_retest_backoff' }, client)
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
    account: await failureAccountSummaryAsync(id, current),
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

async function findAccountCooldownRetestStateAsync(accountId: string): Promise<AccountSummary | undefined> {
  await disableExpiredAccountsAsync()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return (await cooldownRetestAccountSummariesAsync(client, await queryAccountCooldownRetestStateAsync(client, accountId)))[0]
}

function queryAccountsDueForCooldownRetest(
  limit: number,
  accountId?: string,
  cursor?: CooldownAccountRetestCursor
): AccountListRow[] {
  const providerProtocolProfileIds = openAIProtocolProfileIdsForQuery()
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND accounts.id = ?' : ''
  const cursorFilter = !accountId && cursor
    ? 'AND (accounts.cooldown_until, accounts.priority, accounts.created_at, accounts.id) > (?, ?, ?, ?)'
    : ''
  const params: Array<string | number> = [
    ...providerProtocolProfileIds,
    now,
    now,
    now
  ]
  if (accountId) {
    params.push(accountId)
  }
  if (!accountId && cursor) {
    params.push(cursor.cooldownUntil, cursor.priority, cursor.createdAt, cursor.id)
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
        ${cursorFilter}
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
  return hydrateAccountRowsWithRuntimeState(rows, { includeCredentials: true })
    .filter((row) => row.access_type !== 'authorized' || (
      Boolean(row.source_provider_code)
      && isAuthorizedSourceAccountAvailableForDispatch(row, now)
  ))
}

async function queryAccountsDueForCooldownRetestAsync(
  client: DatabaseClient,
  limit: number,
  accountId?: string,
  cursor?: CooldownAccountRetestCursor
): Promise<AccountListRow[]> {
  const providerProtocolProfileIds = await openAIProtocolProfileIdsForQueryAsync()
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND accounts.id = ?' : ''
  const cursorFilter = !accountId && cursor
    ? 'AND (accounts.cooldown_until, accounts.priority, accounts.created_at, accounts.id) > (?, ?, ?, ?)'
    : ''
  const params: Array<string | number> = [
    ...providerProtocolProfileIds,
    now,
    now,
    now
  ]
  if (accountId) {
    params.push(accountId)
  }
  if (!accountId && cursor) {
    params.push(cursor.cooldownUntil, cursor.priority, cursor.createdAt, cursor.id)
  }
  params.push(normalizedCooldownRetestLimit(limit))
  const rows = await client.query<AccountListRow>(`
    SELECT ${cooldownRetestAccountSelectColumnsAsync()}
    FROM ${cooldownRetestTable(client, 'accounts')} accounts
    LEFT JOIN ${cooldownRetestTable(client, 'resource_authorizations')} ra
      ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${cooldownRetestTable(client, 'accounts')} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN LATERAL (
      SELECT
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.updated_at
      FROM ${cooldownRetestTable(client, 'group_accounts')} group_accounts
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = accounts.system_account_id
        AND group_accounts.enabled = 1
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR group_accounts.account_authorization_id = accounts.authorization_instance_authorization_id
        )
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    ) group_bindings ON TRUE
    LEFT JOIN ${cooldownRetestTable(client, 'groups')} bound_groups
      ON bound_groups.id = group_bindings.group_id
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
      ${cursorFilter}
      AND group_bindings.group_id IS NOT NULL
    ORDER BY accounts.cooldown_until ASC, accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
    LIMIT ?
  `, params)
  return rows.filter((row) => row.access_type !== 'authorized' || (
    Boolean(row.source_provider_code)
    && isAuthorizedSourceAccountAvailableForDispatch(row, now)
  ))
}

function cooldownRetestPageFromRows(
  rows: AccountListRow[],
  summaries: AccountSummary[],
  limit: number
): CooldownAccountRetestPage {
  const accounts = summaries.slice(0, limit)
  const lastAccountId = accounts.at(-1)?.id
  const cursorRow = lastAccountId
    ? rows.find((row) => row.id === lastAccountId)
    : rows.at(-1)
  return {
    accounts,
    nextCursor: cursorRow ? cooldownRetestCursorFromRow(cursorRow) : undefined
  }
}

function cooldownRetestCursorFromRow(row: AccountListRow): CooldownAccountRetestCursor {
  return {
    cooldownUntil: String(row.cooldown_until),
    priority: Number(row.priority),
    createdAt: String(row.created_at),
    id: String(row.id)
  }
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

async function queryAccountCooldownRetestStateAsync(client: DatabaseClient, accountId: string): Promise<AccountListRow[]> {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) return []
  const providerProtocolProfileIds = await openAIProtocolProfileIdsForQueryAsync()
  return await client.query<AccountListRow>(`
    SELECT ${cooldownRetestAccountSelectColumnsAsync()}
    FROM ${cooldownRetestTable(client, 'accounts')} accounts
    LEFT JOIN ${cooldownRetestTable(client, 'resource_authorizations')} ra
      ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${cooldownRetestTable(client, 'accounts')} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN LATERAL (
      SELECT
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.updated_at
      FROM ${cooldownRetestTable(client, 'group_accounts')} group_accounts
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = accounts.system_account_id
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    ) group_bindings ON TRUE
    LEFT JOIN ${cooldownRetestTable(client, 'groups')} bound_groups
      ON bound_groups.id = group_bindings.group_id
    WHERE accounts.id = ?
      AND accounts.provider_protocol_profile_id IN (${sqlPlaceholders(providerProtocolProfileIds.length)})
      AND accounts.type IN ('api_key', 'oauth')
      AND accounts.deleted_at IS NULL
    LIMIT 1
  `, [normalizedAccountId, ...providerProtocolProfileIds])
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

function cooldownRetestAccountSelectColumnsAsync(): string {
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
        ra.resource_id AS authorization_resource_id,
        source_accounts.provider_code AS source_provider_code,
        source_accounts.provider_protocol_profile_id AS source_provider_protocol_profile_id,
        source_accounts.protocol_code AS source_protocol_code,
        source_accounts.protocol_version AS source_protocol_version,
        source_accounts.type AS source_type,
        source_accounts.status AS source_status,
        source_accounts.schedulable AS source_schedulable,
        source_accounts.availability_schedule_json AS source_availability_schedule_json,
        source_accounts.account_expires_at AS source_account_expires_at,
        source_accounts.cooldown_until AS source_cooldown_until,
        source_accounts.last_error_code AS source_last_error_code,
        source_accounts.last_error_message AS source_last_error_message,
        source_accounts.credential_mask AS source_credential_mask,
        source_accounts.credentials_encrypted AS source_credentials_encrypted,
        source_accounts.proxy_profile_id AS source_proxy_profile_id,
        source_accounts.concurrency_limit AS source_concurrency_limit,
        source_accounts.client_compatibility AS source_client_compatibility,
        group_bindings.system_account_id AS binding_system_account_id,
        group_bindings.group_id AS bound_group_id,
        bound_groups.name AS bound_group_name,
        group_bindings.account_authorization_id AS bound_group_account_authorization_id`
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
      healthCheckModel: row.health_check_model.trim(),
      healthCheckEndpointMode: row.health_check_endpoint_mode,
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

async function cooldownRetestAccountSummariesAsync(client: DatabaseClient, rows: AccountListRow[]): Promise<AccountSummary[]> {
  if (!rows.length) return []
  const runtimeAccountIds = [...new Set(rows.map((row) => supportedModelAccountIdForRow(row)).filter(Boolean))]
  const [accountNames, supportedModelsByAccount, modelMappingsByAccount, currentConcurrencyByAccount] = await Promise.all([
    loadSystemAccountNameMapByIdsAsync(client, rows.flatMap((row) => [
      row.system_account_id,
      row.authorization_resource_owner_system_account_id ?? '',
      row.authorization_instance_owner_system_account_id ?? ''
    ])),
    loadSupportedModelsByAccountIdsAsync(runtimeAccountIds),
    loadModelMappingsByAccountIdsAsync(runtimeAccountIds),
    loadAccountCurrentConcurrencyByIdsAsync(rows.map((row) => row.id))
  ])
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const groupBinding = accountGroupBindingFromRow(row, row.system_account_id)
    const displayOwnerSystemAccountId = isAuthorizedView
      ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
      : row.system_account_id
    const runtimeAccountId = supportedModelAccountIdForRow(row)
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
      supportedModels: supportedModelsByAccount.get(runtimeAccountId) ?? [],
      modelMappings: modelMappingsByAccount.get(runtimeAccountId) ?? [],
      healthCheckModel: row.health_check_model.trim(),
      healthCheckEndpointMode: row.health_check_endpoint_mode,
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
      authorizationQuotaExceeded: false,
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

async function cooldownRetestDueAccountSummariesAsync(client: DatabaseClient, rows: AccountListRow[]): Promise<AccountSummary[]> {
  return (await cooldownRetestAccountSummariesAsync(client, rows)).filter((account) => {
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

async function failureAccountSummaryAsync(id: string, fallback: AccountSummary): Promise<AccountSummary> {
  return await findAccountCooldownRetestStateAsync(id) ?? fallback
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

function supportedModelAccountIdForRow(row: AccountListRow): string {
  if (row.access_type === 'authorized' && row.authorization_instance_source_account_id && row.source_provider_code) {
    return row.authorization_instance_source_account_id
  }
  return row.id
}

function openAIProtocolProfileIdsForQuery(): string[] {
  const profileIds = listOpenAIProtocolProfileIds().map((profileId) => profileId.trim()).filter(Boolean)
  return profileIds.length ? profileIds : [GPT_OPENAI_V1_PROFILE_ID]
}

async function openAIProtocolProfileIdsForQueryAsync(): Promise<string[]> {
  const profileIds = (await listOpenAIProtocolProfileIdsAsync()).map((profileId) => profileId.trim()).filter(Boolean)
  return profileIds.length ? profileIds : [GPT_OPENAI_V1_PROFILE_ID]
}

function defaultTemporaryUnschedulableMinutes(): number {
  const value = runtimeConfig.databaseDriver === 'postgres'
    ? defaultSystemSettingsByKey.get('defaultTemporaryUnschedulableMinutes')
    : getSettings().defaultTemporaryUnschedulableMinutes
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须是整数')
  }
  if (value < 1 || value > 1440) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须在 1 到 1440 之间')
  }
  return value
}

async function defaultTemporaryUnschedulableMinutesAsync(): Promise<number> {
  const value = (await getSettingsAsync()).defaultTemporaryUnschedulableMinutes
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

function cooldownRetestTable(client: DatabaseClient, table: string): string {
  return client.dialect.qualifyTable(businessSchemaName, table)
}
