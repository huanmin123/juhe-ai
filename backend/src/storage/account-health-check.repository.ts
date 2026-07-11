import type { AccountSummary } from '../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../domain/provider-protocol.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { runtimeConfig } from '../config/runtime.js'
import { loadAccountCurrentConcurrencyByIds, loadAccountCurrentConcurrencyByIdsAsync } from '../shared/account-concurrency.js'
import {
  isAccountAvailabilityScheduleAllowed,
  parseAccountAvailabilityScheduleJson
} from './account-availability-schedule.js'
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
import { decryptJson } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { loadModelMappingsByAccountIdsAsync } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIdsAsync } from './account-supported-models.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { listOpenAIProtocolProfileIds, listOpenAIProtocolProfileIdsAsync } from './provider.repository.js'
import { sqlPlaceholders } from './query-utils.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, ownerPermissions } from './resource-permissions.js'
import { invalidateAccountLookupCache, loadSystemAccountNameMapByIds, loadSystemAccountNameMapByIdsAsync } from './repository-lookups.js'
import type { AccountListRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { optionalString } from './value-utils.js'

const businessSchemaName = 'juhe_business'

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
  countTowardsThreshold?: boolean
  expectedConfigRevision?: number
  observedAt?: string
}

export interface AccountHealthCheckFailureResult {
  changed: boolean
  failureCount: number
  reachedThreshold: boolean
  checkedAt: string
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

export async function listAccountsDueForHealthCheckAsync(options: AccountHealthCheckListOptions): Promise<AccountSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAccountsDueForHealthCheck(options)
  }
  await disableExpiredAccountsAsync()
  const normalizedLimit = normalizedHealthCheckLimit(options.limit)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await queryAccountsDueForHealthCheckAsync(client, healthCheckScanLimit(normalizedLimit), undefined)
  const dueRows = (await dueHealthCheckRowsAsync(client, rows, options)).slice(0, normalizedLimit)
  return await healthCheckAccountSummariesAsync(client, dueRows)
}

export function findAccountForHealthCheck(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  return healthCheckAccountSummaries(queryAccountsDueForHealthCheck(1, accountId))[0]
}

export async function findAccountForHealthCheckAsync(accountId: string): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findAccountForHealthCheck(accountId)
  }
  await disableExpiredAccountsAsync()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await queryAccountsDueForHealthCheckAsync(client, 1, accountId)
  return (await healthCheckAccountSummariesAsync(client, rows))[0]
}

export function recordAccountHealthCheckSuccess(accountId: string, input: AccountHealthCheckSettings & {
  checkedAt?: string
  statusCode?: number
  expectedConfigRevision?: number
}): boolean {
  const checkedAt = normalizedIso(input.checkedAt) ?? nowIso()
  const nextHealthCheckAt = nextHealthCheckAtForAccount(accountId, checkedAt, input)
  const statusCode = normalizedStatusCode(input.statusCode)
  const expectedConfigRevision = normalizedConfigRevision(input.expectedConfigRevision)
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  let changed = false
  try {
    const row = database.prepare(`
      SELECT status, schedulable, availability_schedule_json, config_revision
      FROM accounts
      WHERE id = ?
        AND deleted_at IS NULL
        AND status IN ('active', 'pending_test')
      LIMIT 1
    `).get(accountId) as unknown as AccountHealthCheckSuccessStateRow | undefined
    if (row && (expectedConfigRevision === undefined || Number(row.config_revision) === expectedConfigRevision)) {
      const activationStatus = healthCheckActivationStatus(row, checkedAt)
      const result = database
        .prepare(`
          UPDATE accounts
          SET status = CASE WHEN status = 'pending_test' THEN ? ELSE status END,
              schedulable = CASE WHEN status = 'pending_test' THEN 1 ELSE schedulable END,
              cooldown_until = CASE WHEN status = 'pending_test' THEN NULL ELSE cooldown_until END,
              last_error_code = CASE WHEN status = 'pending_test' THEN NULL ELSE last_error_code END,
              last_error_message = CASE WHEN status = 'pending_test' THEN NULL ELSE last_error_message END,
              last_health_check_at = ?,
              last_health_success_at = ?,
              next_health_check_at = ?,
              health_check_failure_count = 0,
              last_health_check_status_code = ?,
              last_health_check_error_code = NULL,
              last_health_check_error_message = NULL,
              updated_at = ?
          WHERE id = ?
            AND deleted_at IS NULL
            AND status IN ('active', 'pending_test')
            AND (? IS NULL OR config_revision = ?)
        `)
        .run(
          activationStatus,
          checkedAt,
          checkedAt,
          nextHealthCheckAt,
          statusCode,
          checkedAt,
          accountId,
          expectedConfigRevision ?? null,
          expectedConfigRevision ?? null
        )
      changed = Number(result.changes ?? 0) > 0
      if (changed && healthCheckSuccessChangesGroupStats(row, activationStatus)) {
        refreshGroupAccountStatsAfterWrite({ accountIds: [accountId], reason: 'account_health_check_success' })
      }
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (changed) {
    invalidateAccountLookupCache(accountId)
  }
  return changed
}

export async function recordAccountHealthCheckSuccessAsync(accountId: string, input: AccountHealthCheckSettings & {
  checkedAt?: string
  statusCode?: number
  expectedConfigRevision?: number
}): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountHealthCheckSuccess(accountId, input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const checkedAt = normalizedIso(input.checkedAt) ?? nowIso()
  const nextHealthCheckAt = nextHealthCheckAtForAccount(accountId, checkedAt, input)
  const statusCode = normalizedStatusCode(input.statusCode)
  const expectedConfigRevision = normalizedConfigRevision(input.expectedConfigRevision)
  const mutationGuard = postgresHealthCheckMutationGuard({
    expectedConfigRevision
  })
  const changed = await client.transaction(async (tx) => {
    const row = await tx.one<AccountHealthCheckSuccessStateRow>(`
      SELECT status, schedulable, availability_schedule_json, config_revision
      FROM ${healthCheckTable(tx, 'accounts')}
      WHERE id = ?
        AND deleted_at IS NULL
        AND status IN ('active', 'pending_test')
      LIMIT 1
      FOR UPDATE
    `, [accountId])
    if (!row || (expectedConfigRevision !== undefined && Number(row.config_revision) !== expectedConfigRevision)) {
      return false
    }
    const activationStatus = healthCheckActivationStatus(row, checkedAt)
    const result = await tx.execute(`
      UPDATE ${healthCheckTable(tx, 'accounts')}
      SET status = CASE WHEN status = 'pending_test' THEN ? ELSE status END,
          schedulable = CASE WHEN status = 'pending_test' THEN 1 ELSE schedulable END,
          cooldown_until = CASE WHEN status = 'pending_test' THEN NULL ELSE cooldown_until END,
          last_error_code = CASE WHEN status = 'pending_test' THEN NULL ELSE last_error_code END,
          last_error_message = CASE WHEN status = 'pending_test' THEN NULL ELSE last_error_message END,
          last_health_check_at = ?,
          last_health_success_at = ?,
          next_health_check_at = ?,
          health_check_failure_count = 0,
          last_health_check_status_code = ?,
          last_health_check_error_code = NULL,
          last_health_check_error_message = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND status IN ('active', 'pending_test')
        ${mutationGuard.sql}
    `, [
      activationStatus,
      checkedAt,
      checkedAt,
      nextHealthCheckAt,
      statusCode,
      checkedAt,
      accountId,
      ...mutationGuard.params
    ])
    const updated = Number(result.changes ?? 0) > 0
    if (updated && healthCheckSuccessChangesGroupStats(row, activationStatus)) {
      await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [accountId], reason: 'account_health_check_success' }, tx)
    }
    return updated
  })
  if (changed) {
    invalidateAccountLookupCache(accountId)
  }
  return changed
}

interface AccountHealthCheckSuccessStateRow {
  status: string
  schedulable: number
  availability_schedule_json?: string | null
  config_revision?: number
}

function healthCheckSuccessChangesGroupStats(
  row: AccountHealthCheckSuccessStateRow,
  activationStatus: 'active' | 'disabled'
): boolean {
  const nextStatus = row.status === 'pending_test' ? activationStatus : row.status
  const nextSchedulable = row.status === 'pending_test' ? 1 : Number(row.schedulable)
  return nextStatus !== row.status || nextSchedulable !== Number(row.schedulable)
}

function healthCheckActivationStatus(
  row: unknown,
  checkedAt: string
): 'active' | 'disabled' {
  const availabilityScheduleJson = row && typeof row === 'object'
    ? optionalString((row as { availability_schedule_json?: unknown }).availability_schedule_json)
    : undefined
  return isAccountAvailabilityScheduleAllowed(availabilityScheduleJson, new Date(checkedAt))
    ? 'active'
    : 'disabled'
}

export function recordAccountHealthCheckFailure(accountId: string, input: AccountHealthCheckFailureInput): AccountHealthCheckFailureResult {
  const database = getBusinessDatabase()
  const checkedAt = nowIso()
  const countTowardsThreshold = input.countTowardsThreshold !== false
  const errorCode = normalizedHealthCheckErrorCode(input)
  const errorMessage = normalizedHealthCheckErrorMessage(input, errorCode)
  const statusCode = normalizedStatusCode(input.statusCode)
  const expectedConfigRevision = normalizedConfigRevision(input.expectedConfigRevision)
  const observedAt = normalizedIso(input.observedAt)
  const transactionStarted = beginDatabaseTransaction(database)
  let changed = false
  let failureCount = 0
  let nextHealthCheckAt = nextHealthCheckAtAfterFailure(checkedAt, 1, input.intervalHours)
  try {
    const row = database
      .prepare(`
        SELECT config_revision, health_check_failure_count, last_health_success_at
        FROM accounts
        WHERE id = ?
          AND deleted_at IS NULL
        LIMIT 1
      `)
      .get(accountId) as unknown as {
        config_revision?: number
        health_check_failure_count?: number
        last_health_success_at?: string | null
      } | undefined
    const configMatches = expectedConfigRevision === undefined
      || Number(row?.config_revision) === expectedConfigRevision
    const newerSuccessExists = Boolean(
      observedAt
      && row?.last_health_success_at
      && row.last_health_success_at > observedAt
    )
    if (row && configMatches && !newerSuccessExists) {
      const previousFailureCount = Math.max(0, Math.trunc(Number(row.health_check_failure_count ?? 0)))
      failureCount = countTowardsThreshold ? previousFailureCount + 1 : previousFailureCount
      nextHealthCheckAt = nextHealthCheckAtAfterFailure(checkedAt, Math.max(1, failureCount), input.intervalHours)
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
            AND (? IS NULL OR config_revision = ?)
            AND (? IS NULL OR last_health_success_at IS NULL OR last_health_success_at <= ?)
        `)
        .run(
          checkedAt,
          nextHealthCheckAt,
          failureCount,
          statusCode,
          errorCode,
          errorMessage,
          checkedAt,
          accountId,
          expectedConfigRevision ?? null,
          expectedConfigRevision ?? null,
          observedAt ?? null,
          observedAt ?? null
        )
      changed = Number(result.changes ?? 0) > 0
    } else {
      failureCount = Math.max(0, Math.trunc(Number(row?.health_check_failure_count ?? 0)))
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (changed) {
    invalidateAccountLookupCache(accountId)
  }
  return {
    changed,
    failureCount,
    reachedThreshold: changed
      && countTowardsThreshold
      && failureCount >= normalizedFailureThreshold(input.failureThreshold),
    checkedAt,
    nextHealthCheckAt,
    errorCode,
    errorMessage
  }
}

export async function recordAccountHealthCheckFailureAsync(accountId: string, input: AccountHealthCheckFailureInput): Promise<AccountHealthCheckFailureResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountHealthCheckFailure(accountId, input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const checkedAt = nowIso()
  const countTowardsThreshold = input.countTowardsThreshold !== false
  const errorCode = normalizedHealthCheckErrorCode(input)
  const errorMessage = normalizedHealthCheckErrorMessage(input, errorCode)
  const statusCode = normalizedStatusCode(input.statusCode)
  const expectedConfigRevision = normalizedConfigRevision(input.expectedConfigRevision)
  const observedAt = normalizedIso(input.observedAt)
  const mutationGuard = postgresHealthCheckMutationGuard({
    expectedConfigRevision,
    observedAt
  })
  const mutation = await client.transaction(async (tx) => {
    const row = await tx.one<{
      config_revision?: number
      health_check_failure_count?: number
      last_health_success_at?: string | null
    }>(`
      SELECT config_revision, health_check_failure_count, last_health_success_at
      FROM ${healthCheckTable(tx, 'accounts')}
      WHERE id = ?
        AND deleted_at IS NULL
      LIMIT 1
      FOR UPDATE
    `, [accountId])
    const previousFailureCount = Math.max(0, Math.trunc(Number(row?.health_check_failure_count ?? 0)))
    const fallbackNextHealthCheckAt = nextHealthCheckAtAfterFailure(
      checkedAt,
      Math.max(1, previousFailureCount),
      input.intervalHours
    )
    if (!row) {
      return { changed: false, failureCount: 0, nextHealthCheckAt: fallbackNextHealthCheckAt }
    }
    if (expectedConfigRevision !== undefined && Number(row.config_revision) !== expectedConfigRevision) {
      return { changed: false, failureCount: previousFailureCount, nextHealthCheckAt: fallbackNextHealthCheckAt }
    }
    if (observedAt && row.last_health_success_at && row.last_health_success_at > observedAt) {
      return { changed: false, failureCount: previousFailureCount, nextHealthCheckAt: fallbackNextHealthCheckAt }
    }
    const failureCount = countTowardsThreshold ? previousFailureCount + 1 : previousFailureCount
    const nextHealthCheckAt = nextHealthCheckAtAfterFailure(
      checkedAt,
      Math.max(1, failureCount),
      input.intervalHours
    )
    const result = await tx.execute(`
      UPDATE ${healthCheckTable(tx, 'accounts')}
      SET last_health_check_at = ?,
          next_health_check_at = ?,
          health_check_failure_count = ?,
          last_health_check_status_code = ?,
          last_health_check_error_code = ?,
          last_health_check_error_message = ?,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        ${mutationGuard.sql}
    `, [
      checkedAt,
      nextHealthCheckAt,
      failureCount,
      statusCode,
      errorCode,
      errorMessage,
      checkedAt,
      accountId,
      ...mutationGuard.params
    ])
    return {
      changed: Number(result.changes ?? 0) > 0,
      failureCount,
      nextHealthCheckAt
    }
  })
  const changed = mutation.changed
  if (changed) {
    invalidateAccountLookupCache(accountId)
  }
  return {
    changed,
    failureCount: mutation.failureCount,
    reachedThreshold: changed
      && countTowardsThreshold
      && mutation.failureCount >= normalizedFailureThreshold(input.failureThreshold),
    checkedAt,
    nextHealthCheckAt: mutation.nextHealthCheckAt,
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

async function recordAccountHealthSuccessSignalsAsync(
  client: DatabaseClient,
  accountSuccessAt: Map<string, string>,
  options: Partial<AccountHealthCheckSettings> = {}
): Promise<void> {
  if (accountSuccessAt.size === 0) return
  const settings = normalizedHealthCheckSettings(options)
  const changedIds: string[] = []
  await client.transaction(async (tx) => {
    for (const [accountId, successAt] of accountSuccessAt) {
      const normalizedSuccessAt = normalizedIso(successAt)
      if (!normalizedSuccessAt) continue
      const nextHealthCheckAt = nextHealthCheckAtForAccount(accountId, normalizedSuccessAt, settings)
      const refreshAfterAt = healthSuccessRefreshAfterAt(normalizedSuccessAt, settings.intervalHours)
      const result = await tx.execute(`
        UPDATE ${healthCheckTable(tx, 'accounts')}
        SET last_health_success_at = ?,
            next_health_check_at = ?,
            health_check_failure_count = 0,
            last_health_check_error_code = NULL,
            last_health_check_error_message = NULL,
            updated_at = ?
        WHERE id = ?
          AND deleted_at IS NULL
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
      `, [
        normalizedSuccessAt,
        nextHealthCheckAt,
        normalizedSuccessAt,
        accountId,
        normalizedSuccessAt,
        refreshAfterAt,
        nextHealthCheckAt
      ])
      if (Number(result.changes ?? 0) > 0) {
        changedIds.push(accountId)
      }
    }
  })
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

async function dueHealthCheckRowsAsync(client: DatabaseClient, rows: AccountListRow[], options: AccountHealthCheckSettings): Promise<AccountListRow[]> {
  const nowMs = Date.now()
  const cutoffMs = nowMs - normalizedIntervalHours(options.intervalHours) * 60 * 60_000
  const dueRows: AccountListRow[] = []
  const recentSuccessSignals = new Map<string, string>()
  for (const row of rows) {
    const recentSuccessAt = normalizedIso(row.last_health_success_at)
    const recentSuccessMs = recentSuccessAt ? Date.parse(recentSuccessAt) : NaN
    if (recentSuccessAt && Number.isFinite(recentSuccessMs) && recentSuccessMs >= cutoffMs) {
      recentSuccessSignals.set(row.id, recentSuccessAt)
      continue
    }
    dueRows.push(row)
  }
  await recordAccountHealthSuccessSignalsAsync(client, recentSuccessSignals, options)
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
        AND accounts.status IN ('active', 'pending_test')
        AND (accounts.status = 'pending_test' OR accounts.schedulable = 1)
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
      ORDER BY CASE WHEN accounts.status = 'pending_test' THEN 0 ELSE 1 END ASC,
        CASE WHEN accounts.status = 'pending_test' THEN accounts.updated_at END DESC,
        accounts.next_health_check_at IS NOT NULL ASC,
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

async function queryAccountsDueForHealthCheckAsync(client: DatabaseClient, limit: number, accountId: string | undefined): Promise<AccountListRow[]> {
  const providerProtocolProfileIds = await openAIProtocolProfileIdsForQueryAsync()
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
  const rows = await client.query<AccountListRow>(`
    SELECT ${healthCheckAccountSelectColumnsAsync(client)}
    FROM ${healthCheckTable(client, 'accounts')} accounts
    LEFT JOIN ${healthCheckTable(client, 'resource_authorizations')} ra
      ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${healthCheckTable(client, 'accounts')} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN LATERAL (
      SELECT
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.updated_at
      FROM ${healthCheckTable(client, 'group_accounts')} group_accounts
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
    LEFT JOIN ${healthCheckTable(client, 'groups')} bound_groups
      ON bound_groups.id = group_bindings.group_id
    WHERE accounts.provider_protocol_profile_id IN (${sqlPlaceholders(providerProtocolProfileIds.length)})
      AND accounts.type IN ('api_key', 'oauth')
      AND accounts.deleted_at IS NULL
      AND accounts.status IN ('active', 'pending_test')
      AND (accounts.status = 'pending_test' OR accounts.schedulable = 1)
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
      AND group_bindings.group_id IS NOT NULL
    ORDER BY CASE WHEN accounts.status = 'pending_test' THEN 0 ELSE 1 END ASC,
      CASE WHEN accounts.status = 'pending_test' THEN accounts.updated_at END DESC,
      accounts.next_health_check_at IS NOT NULL ASC,
      accounts.next_health_check_at ASC,
      accounts.last_health_check_at ASC,
      accounts.created_at ASC,
      accounts.id ASC
    LIMIT ?
  `, params)
  return rows.filter((row) => row.access_type !== 'authorized' || (
    Boolean(row.source_provider_code)
    && isAuthorizedSourceAccountAvailableForDispatch(row, now)
  ))
}

function postgresHealthCheckMutationGuard(input: {
  expectedConfigRevision?: number
  observedAt?: string
}): { sql: string; params: Array<string | number> } {
  const clauses: string[] = []
  const params: Array<string | number> = []
  if (input.expectedConfigRevision !== undefined) {
    clauses.push('AND config_revision = ?')
    params.push(input.expectedConfigRevision)
  }
  if (input.observedAt) {
    clauses.push('AND (last_health_success_at IS NULL OR last_health_success_at <= ?)')
    params.push(input.observedAt)
  }
  return { sql: clauses.join('\n      '), params }
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

function healthCheckAccountSelectColumnsAsync(client: DatabaseClient): string {
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
      configRevision: Number(row.config_revision ?? 1),
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

async function healthCheckAccountSummariesAsync(client: DatabaseClient, rows: AccountListRow[]): Promise<AccountSummary[]> {
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
      configRevision: Number(row.config_revision ?? 1),
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
      authorizationQuotaExceeded: false,
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
    return true
  })
}

export function normalizedHealthCheckSettings(input: Partial<AccountHealthCheckSettings> = {}): AccountHealthCheckSettings {
  const missingDefaults = input.intervalHours === undefined
    || input.jitterMinutes === undefined
    || input.failureThreshold === undefined
  if (missingDefaults && runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 账号健康检测必须由调用方显式传入系统设置，禁止同步读取本地 settings')
  }
  const settings = missingDefaults ? getSettings() : undefined
  return {
    intervalHours: normalizedIntervalHours(input.intervalHours ?? settings?.accountHealthCheckIntervalHours),
    jitterMinutes: normalizedJitterMinutes(input.jitterMinutes ?? settings?.accountHealthCheckJitterMinutes),
    failureThreshold: normalizedFailureThreshold(input.failureThreshold ?? settings?.accountHealthCheckFailureThreshold)
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

function normalizedConfigRevision(value: unknown): number | undefined {
  const parsed = Math.trunc(Number(value))
  return Number.isFinite(parsed) && parsed >= 1 ? parsed : undefined
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

function healthCheckTable(client: DatabaseClient, table: string): string {
  return client.dialect.qualifyTable(businessSchemaName, table)
}
