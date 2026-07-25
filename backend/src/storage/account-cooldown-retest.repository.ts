import type { AccountSummary } from '../domain/types.js'
import {
  EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE,
  isExplicitAccountErrorPolicyCooldown
} from '../domain/account-runtime-provenance.js'
import { ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES } from '../domain/account-health-check-endpoint-mode.js'
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
  loadAuthorizationQuotaExceededByAuthorizationId,
  loadAuthorizationQuotaExceededByAuthorizationIdAsync
} from './account-summary.repository.js'
import { isCoolingAccountStatus } from './account-status.js'
import { newCooldownRetestGeneration } from './account-runtime-mutation-helpers.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, nowIso, runInDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { loadModelMappingsByAccountIdsAsync } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIdsAsync } from './account-supported-models.repository.js'
import { getPostgresPool } from './postgres-client.js'
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
const cooldownRetestObservationTimeoutCode = 'cooldown_retest_observation_timeout'
const cooldownRetestLimitedProbeTimeoutCode = 'cooldown_retest_limited_probe_timeout'
const cooldownRetestLongTermIntervalSeconds = 60 * 60
const cooldownRetestObservationTimeoutSeconds = 7 * 24 * 60 * 60
const businessSchemaName = 'juhe_business'
const defaultSystemSettingsByKey = new Map<string, unknown>(DEFAULT_SYSTEM_SETTINGS.map(([key, value]) => [key, value]))
const ecmaScriptTrimWhitespaceCodePoints = [
  9, 10, 11, 12, 13, 32, 160, 5760,
  8192, 8193, 8194, 8195, 8196, 8197, 8198, 8199, 8200, 8201, 8202,
  8232, 8233, 8239, 8287, 12288, 65279
]
const sqliteCooldownRetestWhitespaceSql = ecmaScriptTrimWhitespaceCodePoints.map((codePoint) => `char(${codePoint})`).join(' || ')
const postgresCooldownRetestWhitespaceSql = ecmaScriptTrimWhitespaceCodePoints.map((codePoint) => `CHR(${codePoint})`).join(' || ')

function sqliteTrimCooldownRetestText(expression: string): string {
  return `TRIM(${expression}, ${sqliteCooldownRetestWhitespaceSql})`
}

function postgresTrimCooldownRetestText(expression: string): string {
  return `BTRIM(${expression}, ${postgresCooldownRetestWhitespaceSql})`
}

export interface CooldownAccountRetestFailureInput {
  expectedConfigRevision: number
  expectedDispatchRevision: number
  expectedObservationStartedAt: string
  expectedGeneration: string
  expectedSourceConfigRevision?: number
  traceId?: string
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  initialBackoffSeconds?: number
  fastThresholdSeconds?: number
  maxPauseMinutes?: number
  maxRecoveryHours?: number
  backoffMultiplier?: number
}

export interface CooldownAccountRetestFailureResult {
  action: 'retry_immediately' | 'cooldown' | 'long_term_cooldown' | 'error' | 'discard'
  changed: boolean
  failureCount: number
  account?: AccountSummary
  cooldownUntil?: string
  backoffSeconds?: number
  backoffMinutes?: number
  recoveryStage?: 'fast' | 'slow' | 'long_term' | 'terminal'
  fastThresholdSeconds?: number
  maxPauseSeconds?: number
  maxRecoverySeconds?: number
  longTermIntervalSeconds?: number
  maxedFailureCount?: number
  observationStartedAt?: string
  observationElapsedSeconds?: number
  observationTimeoutSeconds?: number
  transitionedToError?: boolean
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

export interface CooldownAccountRetestDeferResult {
  changed: boolean
  cooldownUntil?: string
}

export interface CooldownAccountRetestSuccessInput {
  expectedConfigRevision: number
  expectedDispatchRevision: number
  expectedObservationStartedAt: string
  expectedGeneration: string
  expectedSourceConfigRevision?: number
}

export interface CooldownAccountRetestDeferInput extends CooldownAccountRetestSuccessInput {
  delaySeconds?: number
}

interface CooldownRetestExpectedState {
  expectedConfigRevision: number
  expectedDispatchRevision: number
  expectedObservationStartedAt: string
  expectedGeneration: string
  expectedSourceConfigRevision?: number
}

function cooldownRetestExpectedStateGuard(
  input: CooldownRetestExpectedState,
  options: {
    accountsTable: string
    authorizationsTable: string
    groupAccountsTable: string
    now: string
    targetAlias?: string
  }
): {
  sql: string
  params: Array<number | string | null>
} {
  const targetAlias = options.targetAlias ?? 'accounts'
  const expectedObservationStartedAt = input.expectedObservationStartedAt?.trim()
  const expectedGeneration = input.expectedGeneration?.trim()
  if (
    !Number.isInteger(input.expectedConfigRevision)
    || input.expectedConfigRevision < 1
    || !Number.isInteger(input.expectedDispatchRevision)
    || input.expectedDispatchRevision < 1
    || !expectedObservationStartedAt
    || !Number.isFinite(Date.parse(expectedObservationStartedAt))
    || !expectedGeneration
    || (input.expectedSourceConfigRevision !== undefined
      && (!Number.isInteger(input.expectedSourceConfigRevision) || input.expectedSourceConfigRevision < 1))
  ) {
    return { sql: '      AND 1 = 0', params: [] }
  }
  const clauses = [
    `AND ${targetAlias}.config_revision = ?`,
    `AND ${targetAlias}.dispatch_revision = ?`,
    `AND ${targetAlias}.cooldown_retest_observation_started_at = ?`,
    `AND ${targetAlias}.cooldown_retest_generation = ?`,
    `AND ${targetAlias}.schedulable = 1`,
    `AND (${targetAlias}.account_expires_at IS NULL OR ${targetAlias}.account_expires_at > ?)`
  ]
  const params: Array<number | string | null> = [
    input.expectedConfigRevision,
    input.expectedDispatchRevision,
    expectedObservationStartedAt,
    expectedGeneration,
    options.now
  ]
  if (input.expectedSourceConfigRevision === undefined) {
    clauses.push(`AND ${targetAlias}.authorization_instance_authorization_id IS NULL`)
    clauses.push(`AND ${targetAlias}.authorization_instance_source_account_id IS NULL`)
    clauses.push(`AND ${targetAlias}.authorization_instance_owner_system_account_id IS NULL`)
  } else {
    clauses.push(`AND ${targetAlias}.authorization_instance_authorization_id IS NOT NULL`)
    clauses.push(`AND ${targetAlias}.authorization_instance_source_account_id IS NOT NULL`)
    clauses.push(`AND ${targetAlias}.authorization_instance_owner_system_account_id IS NOT NULL`)
    clauses.push(`AND EXISTS (
        SELECT 1
        FROM ${options.accountsTable} cooldown_source_accounts
        JOIN ${options.authorizationsTable} cooldown_authorizations
          ON cooldown_authorizations.id = ${targetAlias}.authorization_instance_authorization_id
        WHERE cooldown_source_accounts.id = ${targetAlias}.authorization_instance_source_account_id
          AND cooldown_source_accounts.deleted_at IS NULL
          AND cooldown_source_accounts.config_revision = ?
          AND cooldown_source_accounts.status = 'active'
          AND cooldown_source_accounts.schedulable = 1
          AND (cooldown_source_accounts.account_expires_at IS NULL OR cooldown_source_accounts.account_expires_at > ?)
          AND (cooldown_source_accounts.cooldown_until IS NULL OR cooldown_source_accounts.cooldown_until <= ?)
          AND (cooldown_source_accounts.last_error_code IS NULL OR cooldown_source_accounts.last_error_code <> 'account_expired')
          AND cooldown_authorizations.resource_type = 'account'
          AND cooldown_authorizations.resource_id = cooldown_source_accounts.id
          AND cooldown_authorizations.resource_owner_system_account_id = cooldown_source_accounts.system_account_id
          AND cooldown_authorizations.grantee_system_account_id = ${targetAlias}.system_account_id
          AND ${targetAlias}.authorization_instance_owner_system_account_id = cooldown_source_accounts.system_account_id
          AND cooldown_authorizations.status = 'active'
          AND (cooldown_authorizations.expires_at IS NULL OR cooldown_authorizations.expires_at > ?)
          AND EXISTS (
            SELECT 1
            FROM ${options.groupAccountsTable} cooldown_group_accounts
            WHERE cooldown_group_accounts.account_id = ${targetAlias}.id
              AND cooldown_group_accounts.system_account_id = ${targetAlias}.system_account_id
              AND cooldown_group_accounts.account_authorization_id = ${targetAlias}.authorization_instance_authorization_id
              AND cooldown_group_accounts.enabled = 1
          )
      )`)
    params.push(input.expectedSourceConfigRevision, options.now, options.now, options.now)
  }
  return {
    sql: clauses.map((clause) => `      ${clause}`).join('\n'),
    params
  }
}

export function deferCooldownAccountRetest(id: string, input: CooldownAccountRetestDeferInput): CooldownAccountRetestDeferResult {
  const now = nowIso()
  const cooldownUntil = new Date(Date.now() + normalizedTaskFailureDelaySeconds(input.delaySeconds ?? 30) * 1000).toISOString()
  const expectedState = cooldownRetestExpectedStateGuard(input, {
    accountsTable: 'accounts',
    authorizationsTable: 'resource_authorizations',
    groupAccountsTable: 'group_accounts',
    now,
    targetAlias: 'target'
  })
  const result = getBusinessDatabase().prepare(`
    UPDATE accounts AS target
    SET cooldown_until = ?, updated_at = ?
    WHERE target.id = ?
      AND target.deleted_at IS NULL
      AND target.status IN ('temporary_unavailable', 'rate_limited')
      AND (target.cooldown_until IS NULL OR target.cooldown_until < ?)
${expectedState.sql}
  `).run(cooldownUntil, now, id, cooldownUntil, ...expectedState.params)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) invalidateAccountLookupCache(id)
  return { changed, cooldownUntil: changed ? cooldownUntil : undefined }
}

export async function deferCooldownAccountRetestAsync(id: string, input: CooldownAccountRetestDeferInput): Promise<CooldownAccountRetestDeferResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') return deferCooldownAccountRetest(id, input)
  const now = nowIso()
  const cooldownUntil = new Date(Date.now() + normalizedTaskFailureDelaySeconds(input.delaySeconds ?? 30) * 1000).toISOString()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const accountsTable = cooldownRetestTable(client, 'accounts')
  const expectedState = cooldownRetestExpectedStateGuard(input, {
    accountsTable,
    authorizationsTable: cooldownRetestTable(client, 'resource_authorizations'),
    groupAccountsTable: cooldownRetestTable(client, 'group_accounts'),
    now,
    targetAlias: 'target'
  })
  const result = await client.execute(`
    UPDATE ${accountsTable} AS target
    SET cooldown_until = ?, updated_at = ?
    WHERE target.id = ?
      AND target.deleted_at IS NULL
      AND target.status IN ('temporary_unavailable', 'rate_limited')
      AND (target.cooldown_until IS NULL OR target.cooldown_until < ?)
${expectedState.sql}
  `, [cooldownUntil, now, id, cooldownUntil, ...expectedState.params])
  const changed = Number(result.changes ?? 0) > 0
  if (changed) invalidateAccountLookupCache(id)
  return { changed, cooldownUntil: changed ? cooldownUntil : undefined }
}

export function recordCooldownAccountRetestSuccess(
  id: string,
  input: CooldownAccountRetestSuccessInput
): { changed: boolean; accountStatus?: string } {
  const now = nowIso()
  const expectedState = cooldownRetestExpectedStateGuard(input, {
    accountsTable: 'accounts',
    authorizationsTable: 'resource_authorizations',
    groupAccountsTable: 'group_accounts',
    now,
    targetAlias: 'target'
  })
  const changed = runInDatabaseTransaction(() => {
    const result = getBusinessDatabase().prepare(`
      UPDATE accounts AS target
      SET status = 'active', schedulable = 1, cooldown_until = NULL,
          last_error_code = NULL, last_error_message = NULL, last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0, cooldown_retest_observation_started_at = NULL,
          cooldown_retest_generation = NULL,
          cooldown_retest_last_at = NULL, cooldown_retest_last_status_code = NULL,
          updated_at = ?
      WHERE target.id = ? AND target.deleted_at IS NULL
        AND target.status IN ('temporary_unavailable', 'rate_limited')
${expectedState.sql}
    `).run(now, id, ...expectedState.params)
    const updated = Number(result.changes ?? 0) > 0
    if (updated) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown_retest_restored' })
    }
    return updated
  })
  if (changed) {
    invalidateAccountLookupCache(id)
    notifyGatewayRuntimeCacheInvalidation('account_cooldown_retest_restored')
  }
  return { changed, accountStatus: changed ? 'active' : undefined }
}

export async function recordCooldownAccountRetestSuccessAsync(
  id: string,
  input: CooldownAccountRetestSuccessInput
): Promise<{ changed: boolean; accountStatus?: string }> {
  if (runtimeConfig.databaseDriver !== 'postgres') return recordCooldownAccountRetestSuccess(id, input)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const now = nowIso()
  const changed = await client.transaction(async (tx) => {
    const accountsTable = cooldownRetestTable(tx, 'accounts')
    const expectedState = cooldownRetestExpectedStateGuard(input, {
      accountsTable,
      authorizationsTable: cooldownRetestTable(tx, 'resource_authorizations'),
      groupAccountsTable: cooldownRetestTable(tx, 'group_accounts'),
      now,
      targetAlias: 'target'
    })
    const result = await tx.execute(`
      UPDATE ${accountsTable} AS target
      SET status = 'active', schedulable = 1, cooldown_until = NULL,
          last_error_code = NULL, last_error_message = NULL, last_error_trace_id = NULL,
          cooldown_retest_failure_count = 0, cooldown_retest_observation_started_at = NULL,
          cooldown_retest_generation = NULL,
          cooldown_retest_last_at = NULL, cooldown_retest_last_status_code = NULL,
          updated_at = ?
      WHERE target.id = ? AND target.deleted_at IS NULL
        AND target.status IN ('temporary_unavailable', 'rate_limited')
${expectedState.sql}
    `, [now, id, ...expectedState.params])
    const updated = Number(result.changes ?? 0) > 0
    if (updated) {
      await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [id], reason: 'account_cooldown_retest_restored' }, tx)
    }
    return updated
  })
  if (changed) {
    invalidateAccountLookupCache(id)
    notifyGatewayRuntimeCacheInvalidation('account_cooldown_retest_restored')
  }
  return { changed, accountStatus: changed ? 'active' : undefined }
}

function normalizedTaskFailureDelaySeconds(value: number): number {
  if (!Number.isFinite(value)) return 30
  return Math.max(3, Math.min(15 * 60, Math.trunc(value)))
}

interface LegacyCooldownRetestStateRow {
  id: string
  config_revision: number | bigint | string
  dispatch_revision: number | bigint | string
  status: string
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_generation: string | null
}

function repairLegacyCooldownRetestStateInSqlite(
  limit: number,
  accountId?: string,
  cursor?: CooldownAccountRetestCursor
): void {
  const database = getBusinessDatabase()
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND id = ?' : ''
  const cursorFilter = !accountId && cursor
    ? 'AND (cooldown_until, priority, created_at, id) > (?, ?, ?, ?)'
    : ''
  const accountsSource = accountId ? 'accounts' : 'accounts INDEXED BY idx_accounts_cooldown_retest_candidate_order'
  const scanParams: Array<string | number> = [now]
  if (accountId) scanParams.push(accountId)
  if (!accountId && cursor) {
    scanParams.push(cursor.cooldownUntil, cursor.priority, cursor.createdAt, cursor.id)
  }
  const trimmedObservation = sqliteTrimCooldownRetestText('cooldown_retest_observation_started_at')
  const trimmedGeneration = sqliteTrimCooldownRetestText('cooldown_retest_generation')
  const legacyPredicate = `
        deleted_at IS NULL
        AND status IN ('temporary_unavailable', 'rate_limited')
        AND schedulable = 1
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND cooldown_until IS NOT NULL
        AND cooldown_until <= ?
        AND (
          cooldown_retest_observation_started_at IS NULL
          OR ${trimmedObservation} = ''
          OR cooldown_retest_generation IS NULL
          OR ${trimmedGeneration} = ''
          OR cooldown_retest_observation_started_at <> ${trimmedObservation}
          OR cooldown_retest_generation <> ${trimmedGeneration}
          OR ${trimmedObservation} NOT GLOB '????-??-??T??:??:??.???Z'
          OR strftime('%s', ${trimmedObservation}) IS NULL
        )
        ${accountIdFilter}
        ${cursorFilter}`
  const repairRequired = database.prepare(`
    SELECT 1
    FROM ${accountsSource}
    WHERE ${legacyPredicate}
    LIMIT 1
  `).get(...scanParams)
  if (!repairRequired) return
  const params = [...scanParams, normalizedCooldownRetestLimit(limit)]

  runInDatabaseTransaction(() => {
    const rows = database.prepare(`
      SELECT id, config_revision, dispatch_revision, status,
        cooldown_retest_observation_started_at, cooldown_retest_generation
      FROM ${accountsSource}
      WHERE ${legacyPredicate}
      ORDER BY cooldown_until ASC, priority ASC, created_at ASC, id ASC
      LIMIT ?
    `).all(...params) as unknown as LegacyCooldownRetestStateRow[]
    for (const row of rows) {
      const observationStartedAt = validCooldownRetestObservation(row.cooldown_retest_observation_started_at) ?? now
      const generation = optionalString(row.cooldown_retest_generation)?.trim()
      if (observationStartedAt === row.cooldown_retest_observation_started_at && generation === row.cooldown_retest_generation) continue
      const nextGeneration = newCooldownRetestGeneration()
      database.prepare(`
        UPDATE accounts
        SET cooldown_retest_observation_started_at = ?, cooldown_retest_generation = ?
        WHERE id = ?
          AND deleted_at IS NULL
          AND status = ?
          AND config_revision = ?
          AND dispatch_revision = ?
          AND cooldown_retest_observation_started_at IS ?
          AND cooldown_retest_generation IS ?
      `).run(
        observationStartedAt,
        nextGeneration,
        row.id,
        row.status,
        Number(row.config_revision),
        Number(row.dispatch_revision),
        row.cooldown_retest_observation_started_at,
        row.cooldown_retest_generation
      )
    }
  }, database)
}

async function repairLegacyCooldownRetestStateInPostgres(
  client: DatabaseClient,
  limit: number,
  accountId?: string,
  cursor?: CooldownAccountRetestCursor
): Promise<void> {
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND id = ?' : ''
  const cursorFilter = !accountId && cursor
    ? 'AND (cooldown_until, priority, created_at, id) > (?, ?, ?, ?)'
    : ''
  const params: Array<string | number> = [now]
  if (accountId) params.push(accountId)
  if (!accountId && cursor) {
    params.push(cursor.cooldownUntil, cursor.priority, cursor.createdAt, cursor.id)
  }
  params.push(normalizedCooldownRetestLimit(limit))
  const accountsTable = cooldownRetestTable(client, 'accounts')
  const trimmedObservation = postgresTrimCooldownRetestText('cooldown_retest_observation_started_at')
  const trimmedGeneration = postgresTrimCooldownRetestText('cooldown_retest_generation')
  await client.transaction(async (tx) => {
    const rows = await tx.query<LegacyCooldownRetestStateRow>(`
      SELECT id, config_revision, dispatch_revision, status,
        cooldown_retest_observation_started_at, cooldown_retest_generation
      FROM ${accountsTable}
      WHERE deleted_at IS NULL
        AND status IN ('temporary_unavailable', 'rate_limited')
        AND schedulable = 1
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND cooldown_until IS NOT NULL
        AND cooldown_until <= ?
        AND (
          cooldown_retest_observation_started_at IS NULL
          OR ${trimmedObservation} = ''
          OR cooldown_retest_generation IS NULL
          OR ${trimmedGeneration} = ''
          OR cooldown_retest_observation_started_at <> ${trimmedObservation}
          OR cooldown_retest_generation <> ${trimmedGeneration}
          OR ${trimmedObservation} !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[.][0-9]{3}Z$'
          OR NOT pg_input_is_valid(${trimmedObservation}, 'timestamptz')
        )
        ${accountIdFilter}
        ${cursorFilter}
      ORDER BY cooldown_until ASC, priority ASC, created_at ASC, id ASC
      LIMIT ?
      FOR UPDATE SKIP LOCKED
    `, params)
    for (const row of rows) {
      const observationStartedAt = validCooldownRetestObservation(row.cooldown_retest_observation_started_at) ?? now
      const generation = optionalString(row.cooldown_retest_generation)?.trim()
      if (observationStartedAt === row.cooldown_retest_observation_started_at && generation === row.cooldown_retest_generation) continue
      const nextGeneration = newCooldownRetestGeneration()
      await tx.execute(`
        UPDATE ${accountsTable}
        SET cooldown_retest_observation_started_at = ?, cooldown_retest_generation = ?
        WHERE id = ?
          AND deleted_at IS NULL
          AND status = ?
          AND config_revision = ?
          AND dispatch_revision = ?
          AND cooldown_retest_observation_started_at IS NOT DISTINCT FROM ?
          AND cooldown_retest_generation IS NOT DISTINCT FROM ?
      `, [
        observationStartedAt,
        nextGeneration,
        row.id,
        row.status,
        Number(row.config_revision),
        Number(row.dispatch_revision),
        row.cooldown_retest_observation_started_at,
        row.cooldown_retest_generation
      ])
    }
  })
}

function validCooldownRetestObservation(value: string | null | undefined): string | undefined {
  const normalized = optionalString(value)?.trim()
  if (!normalized) return undefined
  const parsed = Date.parse(normalized)
  return Number.isFinite(parsed) ? new Date(parsed).toISOString() : undefined
}

function cooldownRetestExpectedStateMatchesAccount(
  account: AccountSummary,
  input: CooldownRetestExpectedState
): boolean {
  if ((account.configRevision ?? 1) !== input.expectedConfigRevision) return false
  if ((account.cooldownRetestDispatchRevision ?? 1) !== input.expectedDispatchRevision) return false
  if (account.cooldownRetestObservationStartedAt !== input.expectedObservationStartedAt?.trim()) return false
  if (account.cooldownRetestGeneration !== input.expectedGeneration?.trim()) return false
  if (input.expectedSourceConfigRevision === undefined) return account.accessType !== 'authorized'
  return account.accessType === 'authorized'
    && account.cooldownRetestSourceConfigRevision === input.expectedSourceConfigRevision
}

function cooldownRetestExpectedStateMatchesRow(
  row: AccountListRow,
  input: CooldownRetestExpectedState
): boolean {
  if (Number(row.config_revision ?? 1) !== input.expectedConfigRevision) return false
  if (Number(row.dispatch_revision ?? 1) !== input.expectedDispatchRevision) return false
  if (row.cooldown_retest_observation_started_at !== input.expectedObservationStartedAt?.trim()) return false
  if (row.cooldown_retest_generation !== input.expectedGeneration?.trim()) return false
  if (input.expectedSourceConfigRevision === undefined) return row.access_type !== 'authorized'
  return row.access_type === 'authorized'
    && optionalNumber(row.source_config_revision) === input.expectedSourceConfigRevision
}

function laterCooldownUntil(current: string | undefined, candidate: string): string {
  const currentMs = current ? Date.parse(current) : Number.NaN
  const candidateMs = Date.parse(candidate)
  if (!Number.isFinite(currentMs)) return candidate
  if (!Number.isFinite(candidateMs)) return current ?? candidate
  return currentMs >= candidateMs ? current! : candidate
}

export function findAccountForCooldownRetest(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  repairLegacyCooldownRetestStateInSqlite(1, accountId)
  return cooldownRetestDueAccountSummaries(queryAccountsDueForCooldownRetest(1, accountId))[0]
}

export async function findAccountForCooldownRetestAsync(accountId: string): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findAccountForCooldownRetest(accountId)
  }
  await disableExpiredAccountsAsync()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await repairLegacyCooldownRetestStateInPostgres(client, 1, accountId)
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
  const scanLimit = cooldownRetestScanLimit(normalizedLimit)
  repairLegacyCooldownRetestStateInSqlite(scanLimit, undefined, cursor)
  const rows = queryAccountsDueForCooldownRetest(scanLimit, undefined, cursor)
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
  const scanLimit = cooldownRetestScanLimit(normalizedLimit)
  await repairLegacyCooldownRetestStateInPostgres(client, scanLimit, undefined, cursor)
  const rows = await queryAccountsDueForCooldownRetestAsync(client, scanLimit, undefined, cursor)
  return cooldownRetestPageFromRows(rows, await cooldownRetestDueAccountSummariesAsync(client, rows), normalizedLimit)
}

export function recordCooldownAccountRetestFailure(id: string, input: CooldownAccountRetestFailureInput): CooldownAccountRetestFailureResult {
  disableExpiredAccounts()
  const result = runInDatabaseTransaction(() => recordCooldownAccountRetestFailureInSqliteTransaction(id, input))
  if (result.changed) {
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite(result.transitionedToError ? 'account_cooldown_retest_timeout' : 'account_cooldown_retest_backoff')
  }
  return result
}

function recordCooldownAccountRetestFailureInSqliteTransaction(id: string, input: CooldownAccountRetestFailureInput): CooldownAccountRetestFailureResult {
  const current = findAccountCooldownRetestState(id)
  const errorCode = normalizedCooldownRetestErrorCode(input)
  const testErrorMessage = normalizedCooldownRetestErrorMessage(input, errorCode)
  const traceId = optionalString(input.traceId)?.slice(0, 200) ?? null
  if (!current || !isCoolingAccountStatus(current.status) || !cooldownRetestExpectedStateMatchesAccount(current, input)) {
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
  const recovery = cooldownRetestRecoveryPlan(failureCount, input, nowDate, observationStartedAt, current.status === 'temporary_unavailable' && current.temporaryUnavailableContinuousProbeEnabled === false)

  const transitionedToError = recovery.stage === 'terminal'
  const calculatedCooldownUntil = transitionedToError
    ? undefined
    : new Date(nowDate.getTime() + recovery.backoffSeconds * 1000).toISOString()
  const cooldownUntil = transitionedToError
    ? undefined
    : laterCooldownUntil(current.cooldownUntil, calculatedCooldownUntil!)
  const persistedErrorCode = transitionedToError
    ? (current.status === 'temporary_unavailable' && current.temporaryUnavailableContinuousProbeEnabled === false
        ? cooldownRetestLimitedProbeTimeoutCode
        : cooldownRetestObservationTimeoutCode)
    : isExplicitAccountErrorPolicyCooldown(current.lastErrorCode, current.lastErrorMessage)
      ? EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE
      : recovery.stage === 'long_term' ? cooldownRetestLongTermUnavailableCode : errorCode
  const cooldownMessage = cooldownRetestFailureMessage(failureCount, recovery, testErrorMessage)
  const expectedState = cooldownRetestExpectedStateGuard(input, {
    accountsTable: 'accounts',
    authorizationsTable: 'resource_authorizations',
    groupAccountsTable: 'group_accounts',
    now,
    targetAlias: 'target'
  })
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts AS target
      SET status = CASE WHEN ? = 1 THEN 'error' ELSE status END,
          schedulable = CASE WHEN ? = 1 THEN 0 ELSE 1 END,
          cooldown_until = ?,
          last_error_code = ?,
          last_error_message = ?,
          last_error_trace_id = ?,
          cooldown_retest_failure_count = ?,
          cooldown_retest_observation_started_at = COALESCE(cooldown_retest_observation_started_at, ?),
          cooldown_retest_last_at = ?,
          cooldown_retest_last_status_code = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE target.id = ?
        AND target.deleted_at IS NULL
        AND target.status = ?
${expectedState.sql}
    `)
    .run(
      transitionedToError ? 1 : 0,
      transitionedToError ? 1 : 0,
      cooldownUntil ?? null,
      persistedErrorCode,
      cooldownMessage,
      traceId,
      failureCount,
      observationStartedAt,
      now,
      lastStatusCode,
      now,
      id,
      current.status,
      ...expectedState.params
    )
  const changed = Number(result.changes ?? 0) > 0
  if (!changed) {
    const latest = failureAccountSummary(id, current)
    return {
      action: 'discard',
      changed: false,
      failureCount: latest.cooldownRetestFailureCount ?? 0,
      account: latest,
      cooldownUntil: latest.cooldownUntil,
      errorCode,
      errorMessage: testErrorMessage
    }
  }
  if (changed) {
    refreshGroupAccountStatsAfterWrite({
      accountIds: [id],
      reason: transitionedToError ? 'account_cooldown_retest_timeout' : 'account_cooldown_retest_backoff'
    })
  }
  const action = cooldownRetestAction(recovery.stage)
  return {
    action,
    changed,
    failureCount,
    account: failureAccountSummary(id, current),
    cooldownUntil,
    backoffSeconds: transitionedToError ? undefined : recovery.backoffSeconds,
    backoffMinutes: transitionedToError ? undefined : secondsToCeilMinutes(recovery.backoffSeconds),
    recoveryStage: recovery.stage,
    fastThresholdSeconds: recovery.fastThresholdSeconds,
    maxPauseSeconds: recovery.maxPauseSeconds,
    maxRecoverySeconds: recovery.maxRecoverySeconds,
    longTermIntervalSeconds: recovery.longTermIntervalSeconds,
    maxedFailureCount: recovery.maxedFailureCount,
    observationStartedAt: recovery.observationStartedAt,
    observationElapsedSeconds: recovery.observationElapsedSeconds,
    observationTimeoutSeconds: recovery.observationTimeoutSeconds,
    transitionedToError: changed && transitionedToError,
    errorCode: persistedErrorCode,
    errorMessage: cooldownMessage
  }
}

export async function recordCooldownAccountRetestFailureAsync(id: string, input: CooldownAccountRetestFailureInput): Promise<CooldownAccountRetestFailureResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordCooldownAccountRetestFailure(id, input)
  }
  const errorCode = normalizedCooldownRetestErrorCode(input)
  const testErrorMessage = normalizedCooldownRetestErrorMessage(input, errorCode)
  const traceId = optionalString(input.traceId)?.slice(0, 200) ?? null
  const maxPauseMinutes = input.maxPauseMinutes ?? await defaultTemporaryUnschedulableMinutesAsync()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const accountsTable = cooldownRetestTable(client, 'accounts')
  const transactionResult = await client.transaction(async (tx) => {
    const current = (await queryAccountCooldownRetestStateAsync(tx, id, { forUpdate: true }))[0]
    if (!current || !isCoolingAccountStatus(current.status) || !cooldownRetestExpectedStateMatchesRow(current, input)) {
      return {
        changed: false,
        found: Boolean(current),
        transitionedToError: false,
        result: {
          action: 'discard',
          changed: false,
          failureCount: Math.max(0, Number(current?.cooldown_retest_failure_count ?? 0)),
          errorCode,
          errorMessage: testErrorMessage
        } satisfies CooldownAccountRetestFailureResult
      }
    }

    const nowDate = new Date()
    const now = nowDate.toISOString()
    const expectedState = cooldownRetestExpectedStateGuard(input, {
      accountsTable,
      authorizationsTable: cooldownRetestTable(tx, 'resource_authorizations'),
      groupAccountsTable: cooldownRetestTable(tx, 'group_accounts'),
      now,
      targetAlias: 'target'
    })
    const failureCount = Math.max(0, Number(current.cooldown_retest_failure_count ?? 0)) + 1
    const lastStatusCode = typeof input.statusCode === 'number' && Number.isFinite(input.statusCode) ? Math.trunc(input.statusCode) : null
    const observationStartedAt = current.cooldown_retest_observation_started_at ?? now
    const recovery = cooldownRetestRecoveryPlan(failureCount, {
      ...input,
      maxPauseMinutes
    }, nowDate, observationStartedAt, current.status === 'temporary_unavailable' && current.temporary_unavailable_continuous_probe_enabled === 0)
    const transitionedToError = recovery.stage === 'terminal'
    const calculatedCooldownUntil = transitionedToError
      ? undefined
      : new Date(nowDate.getTime() + recovery.backoffSeconds * 1000).toISOString()
    const cooldownUntil = transitionedToError
      ? undefined
      : laterCooldownUntil(current.cooldown_until ?? undefined, calculatedCooldownUntil!)
    const persistedErrorCode = transitionedToError
      ? (current.status === 'temporary_unavailable' && current.temporary_unavailable_continuous_probe_enabled === 0
          ? cooldownRetestLimitedProbeTimeoutCode
          : cooldownRetestObservationTimeoutCode)
      : isExplicitAccountErrorPolicyCooldown(current.last_error_code, current.last_error_message)
        ? EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE
        : recovery.stage === 'long_term' ? cooldownRetestLongTermUnavailableCode : errorCode
    const cooldownMessage = cooldownRetestFailureMessage(failureCount, recovery, testErrorMessage)
    const update = await tx.execute(`
      UPDATE ${cooldownRetestTable(tx, 'accounts')} AS target
      SET status = CASE WHEN ? = 1 THEN 'error' ELSE status END,
          schedulable = CASE WHEN ? = 1 THEN 0 ELSE 1 END,
          cooldown_until = ?,
          last_error_code = ?,
          last_error_message = ?,
          last_error_trace_id = ?,
          cooldown_retest_failure_count = ?,
          cooldown_retest_observation_started_at = COALESCE(cooldown_retest_observation_started_at, ?),
          cooldown_retest_last_at = ?,
          cooldown_retest_last_status_code = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE target.id = ?
        AND target.deleted_at IS NULL
        AND target.status = ?
${expectedState.sql}
    `, [
      transitionedToError ? 1 : 0,
      transitionedToError ? 1 : 0,
      cooldownUntil ?? null,
      persistedErrorCode,
      cooldownMessage,
      traceId,
      failureCount,
      observationStartedAt,
      now,
      lastStatusCode,
      now,
      id,
      current.status,
      ...expectedState.params
    ])
    const changed = Number(update.changes ?? 0) > 0
    if (!changed) {
      return {
        changed: false,
        found: true,
        transitionedToError: false,
        result: {
          action: 'discard',
          changed: false,
          failureCount: Math.max(0, Number(current.cooldown_retest_failure_count ?? 0)),
          cooldownUntil: current.cooldown_until ?? undefined,
          errorCode,
          errorMessage: testErrorMessage
        } satisfies CooldownAccountRetestFailureResult
      }
    }
    if (changed) {
      await refreshGroupAccountStatsAfterWriteAsync({
        accountIds: [id],
        reason: transitionedToError ? 'account_cooldown_retest_timeout' : 'account_cooldown_retest_backoff'
      }, tx)
    }
    return {
      changed,
      found: true,
      transitionedToError,
      result: {
        action: cooldownRetestAction(recovery.stage),
        changed,
        failureCount,
        cooldownUntil,
        backoffSeconds: transitionedToError ? undefined : recovery.backoffSeconds,
        backoffMinutes: transitionedToError ? undefined : secondsToCeilMinutes(recovery.backoffSeconds),
        recoveryStage: recovery.stage,
        fastThresholdSeconds: recovery.fastThresholdSeconds,
        maxPauseSeconds: recovery.maxPauseSeconds,
        maxRecoverySeconds: recovery.maxRecoverySeconds,
        longTermIntervalSeconds: recovery.longTermIntervalSeconds,
        maxedFailureCount: recovery.maxedFailureCount,
        observationStartedAt: recovery.observationStartedAt,
        observationElapsedSeconds: recovery.observationElapsedSeconds,
        observationTimeoutSeconds: recovery.observationTimeoutSeconds,
        transitionedToError: changed && transitionedToError,
        errorCode: persistedErrorCode,
        errorMessage: cooldownMessage
      } satisfies CooldownAccountRetestFailureResult
    }
  })
  if (transactionResult.changed) {
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite(transactionResult.transitionedToError ? 'account_cooldown_retest_timeout' : 'account_cooldown_retest_backoff')
  }
  return {
    ...transactionResult.result,
    account: transactionResult.found
      ? await findAccountCooldownRetestStateAsync(id)
      : undefined
  }
}

function findAccountCooldownRetestState(accountId: string): AccountSummary | undefined {
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
  const endpointModes = [...ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES]
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND accounts.id = ?' : ''
  const cursorFilter = !accountId && cursor
    ? 'AND (accounts.cooldown_until, accounts.priority, accounts.created_at, accounts.id) > (?, ?, ?, ?)'
    : ''
  const accountsSource = accountId ? 'accounts' : 'accounts INDEXED BY idx_accounts_cooldown_retest_candidate_order'
  const trimmedObservation = sqliteTrimCooldownRetestText('accounts.cooldown_retest_observation_started_at')
  const trimmedGeneration = sqliteTrimCooldownRetestText('accounts.cooldown_retest_generation')
  const params: Array<string | number> = [
    ...endpointModes,
    now,
    now,
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
      FROM ${accountsSource}
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      LEFT JOIN accounts source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
        AND source_accounts.deleted_at IS NULL
      WHERE accounts.health_check_endpoint_mode IN (${sqlPlaceholders(endpointModes.length)})
        AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
        AND accounts.deleted_at IS NULL
        AND accounts.status IN ('temporary_unavailable', 'rate_limited')
        AND accounts.schedulable = 1
        AND accounts.cooldown_until IS NOT NULL
        AND accounts.cooldown_until <= ?
        AND accounts.cooldown_retest_observation_started_at IS NOT NULL
        AND ${trimmedObservation} <> ''
        AND accounts.cooldown_retest_generation IS NOT NULL
        AND ${trimmedGeneration} <> ''
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
        AND (
          (
            accounts.authorization_instance_authorization_id IS NULL
            AND accounts.authorization_instance_source_account_id IS NULL
            AND accounts.authorization_instance_owner_system_account_id IS NULL
          )
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND accounts.authorization_instance_source_account_id IS NOT NULL
            AND accounts.authorization_instance_owner_system_account_id IS NOT NULL
            AND ra.id IS NOT NULL
            AND ra.resource_type = 'account'
            AND ra.resource_id = accounts.authorization_instance_source_account_id
            AND ra.resource_owner_system_account_id = source_accounts.system_account_id
            AND ra.grantee_system_account_id = accounts.system_account_id
            AND accounts.authorization_instance_owner_system_account_id = source_accounts.system_account_id
            AND ra.status = 'active'
            AND (ra.expires_at IS NULL OR ra.expires_at > ?)
            AND source_accounts.provider_code IS NOT NULL
            AND source_accounts.status = 'active'
            AND source_accounts.schedulable = 1
            AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
            AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
            AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
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
}

async function queryAccountsDueForCooldownRetestAsync(
  client: DatabaseClient,
  limit: number,
  accountId?: string,
  cursor?: CooldownAccountRetestCursor
): Promise<AccountListRow[]> {
  const endpointModes = [...ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES]
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND accounts.id = ?' : ''
  const cursorFilter = !accountId && cursor
    ? 'AND (accounts.cooldown_until, accounts.priority, accounts.created_at, accounts.id) > (?, ?, ?, ?)'
    : ''
  const trimmedObservation = postgresTrimCooldownRetestText('accounts.cooldown_retest_observation_started_at')
  const trimmedGeneration = postgresTrimCooldownRetestText('accounts.cooldown_retest_generation')
  const params: Array<string | number> = [
    ...endpointModes,
    now,
    now,
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
    WHERE accounts.health_check_endpoint_mode IN (${sqlPlaceholders(endpointModes.length)})
      AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
      AND accounts.deleted_at IS NULL
      AND accounts.status IN ('temporary_unavailable', 'rate_limited')
      AND accounts.schedulable = 1
      AND accounts.cooldown_until IS NOT NULL
      AND accounts.cooldown_until <= ?
      AND accounts.cooldown_retest_observation_started_at IS NOT NULL
      AND ${trimmedObservation} <> ''
      AND accounts.cooldown_retest_generation IS NOT NULL
      AND ${trimmedGeneration} <> ''
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      AND (
        (
          accounts.authorization_instance_authorization_id IS NULL
          AND accounts.authorization_instance_source_account_id IS NULL
          AND accounts.authorization_instance_owner_system_account_id IS NULL
        )
        OR (
          accounts.authorization_instance_authorization_id IS NOT NULL
          AND accounts.authorization_instance_source_account_id IS NOT NULL
          AND accounts.authorization_instance_owner_system_account_id IS NOT NULL
          AND ra.id IS NOT NULL
          AND ra.resource_type = 'account'
          AND ra.resource_id = accounts.authorization_instance_source_account_id
          AND ra.resource_owner_system_account_id = source_accounts.system_account_id
          AND ra.grantee_system_account_id = accounts.system_account_id
          AND accounts.authorization_instance_owner_system_account_id = source_accounts.system_account_id
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND source_accounts.provider_code IS NOT NULL
          AND source_accounts.status = 'active'
          AND source_accounts.schedulable = 1
          AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
          AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
          AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
        )
      )
      ${accountIdFilter}
      ${cursorFilter}
      AND group_bindings.group_id IS NOT NULL
    ORDER BY accounts.cooldown_until ASC, accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
    LIMIT ?
  `, params)
  return rows
}

function cooldownRetestPageFromRows(
  rows: AccountListRow[],
  summaries: AccountSummary[],
  limit: number
): CooldownAccountRetestPage {
  const accounts = summaries.slice(0, limit)
  const lastAccountId = accounts.at(-1)?.id
  const cursorRow = accounts.length >= limit && lastAccountId
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
  const endpointModes = [...ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES]
  return hydrateAccountRowsWithRuntimeState(getBusinessDatabase()
    .prepare(`
      SELECT ${cooldownRetestAccountSelectColumns()}
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      LEFT JOIN accounts source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
        AND source_accounts.deleted_at IS NULL
      WHERE accounts.id = ?
        AND accounts.health_check_endpoint_mode IN (${sqlPlaceholders(endpointModes.length)})
        AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
        AND accounts.deleted_at IS NULL
      LIMIT 1
    `)
    .all(normalizedAccountId, ...endpointModes) as unknown as AccountListRow[], { includeCredentials: true })
}

async function queryAccountCooldownRetestStateAsync(
  client: DatabaseClient,
  accountId: string,
  options: { forUpdate?: boolean } = {}
): Promise<AccountListRow[]> {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) return []
  const endpointModes = [...ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES]
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
      AND accounts.health_check_endpoint_mode IN (${sqlPlaceholders(endpointModes.length)})
      AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
      AND accounts.deleted_at IS NULL
    LIMIT 1
    ${options.forUpdate ? 'FOR UPDATE OF accounts' : ''}
  `, [normalizedAccountId, ...endpointModes])
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
        ra.resource_id AS authorization_resource_id,
        source_accounts.config_revision AS source_config_revision`
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
        source_accounts.config_revision AS source_config_revision,
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
        source_accounts.temporary_unavailable_continuous_probe_enabled AS source_temporary_unavailable_continuous_probe_enabled,
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
      configRevision: Number(row.config_revision ?? 1),
      cooldownRetestDispatchRevision: Number(row.dispatch_revision ?? 1),
      schedulable: row.schedulable === 1,
      availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      lastErrorTraceId: row.last_error_trace_id ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestGeneration: row.cooldown_retest_generation ?? undefined,
      cooldownRetestSourceConfigRevision: row.access_type === 'authorized'
        ? optionalNumber(row.source_config_revision)
        : undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: optionalNumber(row.cooldown_retest_last_status_code),
      temporaryUnavailableContinuousProbeEnabled: isAuthorizedView
        ? row.source_temporary_unavailable_continuous_probe_enabled === 1
        : row.temporary_unavailable_continuous_probe_enabled === 1,
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
  const [accountNames, supportedModelsByAccount, modelMappingsByAccount, currentConcurrencyByAccount, quotaExceededByAuthorization] = await Promise.all([
    loadSystemAccountNameMapByIdsAsync(client, rows.flatMap((row) => [
      row.system_account_id,
      row.authorization_resource_owner_system_account_id ?? '',
      row.authorization_instance_owner_system_account_id ?? ''
    ])),
    loadSupportedModelsByAccountIdsAsync(runtimeAccountIds),
    loadModelMappingsByAccountIdsAsync(runtimeAccountIds),
    loadAccountCurrentConcurrencyByIdsAsync(rows.map((row) => row.id)),
    loadAuthorizationQuotaExceededByAuthorizationIdAsync(client, rows)
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
      configRevision: Number(row.config_revision ?? 1),
      cooldownRetestDispatchRevision: Number(row.dispatch_revision ?? 1),
      schedulable: row.schedulable === 1,
      availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestGeneration: row.cooldown_retest_generation ?? undefined,
      cooldownRetestSourceConfigRevision: row.access_type === 'authorized'
        ? optionalNumber(row.source_config_revision)
        : undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: optionalNumber(row.cooldown_retest_last_status_code),
      temporaryUnavailableContinuousProbeEnabled: isAuthorizedView
        ? row.source_temporary_unavailable_continuous_probe_enabled === 1
        : row.temporary_unavailable_continuous_probe_enabled === 1,
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
  stage: 'fast' | 'slow' | 'long_term' | 'terminal'
  backoffSeconds: number
  fastThresholdSeconds: number
  maxPauseSeconds: number
  maxRecoverySeconds: number
  longTermIntervalSeconds: number
  maxedFailureCount: number
  observationStartedAt: string
  observationElapsedSeconds: number
  observationTimeoutSeconds: number
}

function cooldownRetestRecoveryPlan(failureCount: number, input: CooldownAccountRetestFailureInput, nowDate: Date, observationStartedAt: string, boundedTemporaryUnavailable: boolean): CooldownRetestRecoveryPlan {
  const initialBackoffSeconds = boundedInteger(input.initialBackoffSeconds, temporaryUnavailableInitialBackoffSeconds, 1, 3600)
  const fastThresholdSeconds = boundedInteger(input.fastThresholdSeconds, temporaryUnavailableFastThresholdSeconds, initialBackoffSeconds, 3600)
  const maxPauseSeconds = boundedInteger(input.maxPauseMinutes, defaultTemporaryUnschedulableMinutes(), 1, 1440) * 60
  const maxRecoverySeconds = boundedInteger(input.maxRecoveryHours, 12, 1, 24 * 30) * 60 * 60
  const longTermIntervalSeconds = cooldownRetestLongTermIntervalSeconds
  const multiplier = boundedInteger(input.backoffMultiplier, temporaryUnavailableBackoffMultiplier, 2, 10)
  const exponent = Math.max(0, Math.min(failureCount - 1, 30))
  const uncappedBackoffSeconds = Math.min(Number.MAX_SAFE_INTEGER, initialBackoffSeconds * Math.pow(multiplier, exponent))
  const firstMaxedFailureCount = firstCappedBackoffFailureCount(initialBackoffSeconds, multiplier, maxPauseSeconds)
  const maxedFailureCount = failureCount >= firstMaxedFailureCount ? failureCount - firstMaxedFailureCount + 1 : 0
  const observationElapsedSeconds = cooldownRetestObservationElapsedSeconds(observationStartedAt, nowDate)
  const inLongTermStage = !boundedTemporaryUnavailable && observationElapsedSeconds >= maxRecoverySeconds
  const observationTimeoutSeconds = boundedTemporaryUnavailable ? 10 * 60 : cooldownRetestObservationTimeoutSeconds
  const timedOut = observationElapsedSeconds >= observationTimeoutSeconds
  const uncappedBackoff = inLongTermStage ? longTermIntervalSeconds : Math.min(uncappedBackoffSeconds, maxPauseSeconds)
  const backoffSeconds = timedOut
    ? 0
    : boundedTemporaryUnavailable
      ? Math.min(uncappedBackoff, Math.max(1, observationTimeoutSeconds - observationElapsedSeconds))
      : uncappedBackoff
  const stage = timedOut
    ? 'terminal'
    : inLongTermStage ? 'long_term' : backoffSeconds <= fastThresholdSeconds ? 'fast' : 'slow'
  return {
    stage,
    backoffSeconds,
    fastThresholdSeconds,
    maxPauseSeconds,
    maxRecoverySeconds,
    longTermIntervalSeconds,
    maxedFailureCount,
    observationStartedAt,
    observationElapsedSeconds,
    observationTimeoutSeconds
  }
}

function cooldownRetestAction(stage: CooldownRetestRecoveryPlan['stage']): CooldownAccountRetestFailureResult['action'] {
  if (stage === 'terminal') return 'error'
  if (stage === 'long_term') return 'long_term_cooldown'
  return stage === 'fast' ? 'retry_immediately' : 'cooldown'
}

function cooldownRetestFailureMessage(failureCount: number, recovery: CooldownRetestRecoveryPlan, lastError: string): string {
  if (recovery.stage === 'terminal') {
    return cooldownRetestObservationTimeoutMessage(failureCount, recovery.observationStartedAt, recovery.observationTimeoutSeconds, lastError)
  }
  if (recovery.stage === 'long_term') {
    return cooldownRetestLongTermMessage(failureCount, recovery.maxRecoverySeconds, recovery.backoffSeconds, lastError)
  }
  return cooldownRetestCooldownMessage(failureCount, recovery.backoffSeconds, recovery.stage, lastError)
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
  return Math.max(1, raw + 1)
}

function cooldownRetestCooldownMessage(failureCount: number, backoffSeconds: number, stage: 'fast' | 'slow', lastError: string): string {
  const stageText = stage === 'fast' ? '快速恢复通道' : '慢速恢复通道'
  return `后台冷却复测连续失败 ${failureCount} 次，${stageText}下次复测延后 ${formatDurationSeconds(backoffSeconds)}；最后错误：${lastError}`.slice(0, 1000)
}

function cooldownRetestLongTermMessage(failureCount: number, maxRecoverySeconds: number, backoffSeconds: number, lastError: string): string {
  return `后台冷却复测连续失败 ${failureCount} 次，已超过自动恢复观察窗口 ${formatDurationSeconds(maxRecoverySeconds)}，进入长期不可用每 1 小时复测；下次复测延后 ${formatDurationSeconds(backoffSeconds)}；最后错误：${lastError}`.slice(0, 1000)
}

function cooldownRetestObservationTimeoutMessage(failureCount: number, observationStartedAt: string, observationTimeoutSeconds: number, lastError: string): string {
  return `后台冷却复测连续失败 ${failureCount} 次，从自动恢复观察开始 ${observationStartedAt} 起已持续 ${formatObservationWindowSeconds(observationTimeoutSeconds)}仍未恢复，账户已转为异常；最后错误：${lastError}`.slice(0, 1000)
}

function formatObservationWindowSeconds(seconds: number): string {
  const wholeDays = Math.trunc(seconds / (24 * 60 * 60))
  if (wholeDays > 0 && wholeDays * 24 * 60 * 60 === seconds) return `${wholeDays} 天`
  return formatDurationSeconds(seconds)
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
