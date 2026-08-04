import { runtimeConfig } from '../config/runtime.js'
import type { AccountBalanceQueryConfig, AccountBalanceSnapshot } from '../modules/accounts/account-balance.types.js'
import { effectiveAccountApiKeyCount, normalizeAccountBalanceConfig } from '../modules/accounts/account-balance-config.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { decryptJson } from './crypto.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'

export interface AccountBalanceRefreshCandidate {
  id: string
  systemAccountId: string
  configRevision: number
  credentials: Record<string, unknown>
  config: AccountBalanceQueryConfig
  nextRefreshAt: string | null
  proxyProfileId?: string
}

export interface AccountBalanceDetectionCandidate {
  id: string
  systemAccountId: string
  configRevision: number
  credentials: Record<string, unknown>
  /**
   * A non-null value is the durable first-detection intent written when the
   * account first passes health checking. Maintenance backfills omit it.
   */
  nextRefreshAt?: string | null
  proxyProfileId?: string
}

export interface AccountBalanceDetectionCandidatePage {
  candidates: AccountBalanceDetectionCandidate[]
  nextAfterId?: string
}

export interface AccountBalanceSnapshotRecord {
  snapshot: AccountBalanceSnapshot
  nextRefreshAfter?: string
  updatedAt: string
}

interface BalanceCandidateRow {
  id: string
  system_account_id: string
  config_revision: number
  credentials_encrypted: string
  balance_query_config_json: string
  balance_query_next_refresh_at: string | null
  proxy_profile_id?: string | null
}

interface BalanceDetectionCandidateRow {
  id: string
  system_account_id: string
  config_revision: number
  credentials_encrypted: string
  balance_query_next_refresh_at?: string | null
  proxy_profile_id?: string | null
}

/**
 * Every first-detection read and writeback must retain this eligibility guard.
 * Availability scheduling can revoke active/schedulable without changing the
 * account configuration revision while an upstream query is still in flight.
 */
function balanceDetectionCandidateWhere(): string {
  return `
    status = 'active'
    AND ${balanceBooleanPredicate('schedulable', true)}
    AND type = 'api_key'
    AND ${balanceBooleanPredicate('balance_query_enabled', false)}
    AND balance_query_config_json = '{}'
    AND deleted_at IS NULL
    AND authorization_instance_authorization_id IS NULL
  `
}

/**
 * Automatic refresh writebacks must recheck the current scheduling boundary.
 * The refresh due value fences concurrent automatic attempts, while this guard
 * prevents an in-flight result from reviving an account that was disabled or
 * otherwise removed from automatic eligibility without a config revision.
 */
function balanceRefreshAutomaticCandidateWhere(): string {
  return `
    type = 'api_key'
    AND status = 'active'
    AND ${balanceBooleanPredicate('schedulable', true)}
    AND ${balanceBooleanPredicate('balance_query_enabled', true)}
    AND deleted_at IS NULL
    AND authorization_instance_authorization_id IS NULL
  `
}

interface BalanceDueCursor {
  nextRefreshAt: string
  id: string
}

let sqliteBalanceDueCursor: BalanceDueCursor | undefined
let postgresBalanceDueCursor: BalanceDueCursor | undefined
let sqliteBalanceDetectionDueCursor: BalanceDueCursor | undefined
let postgresBalanceDetectionDueCursor: BalanceDueCursor | undefined

export function listAccountsDueForBalanceRefresh(options: { now?: string; limit?: number } = {}): AccountBalanceRefreshCandidate[] {
  const limit = normalizedLimit(options.limit)
  const now = options.now ?? nowIso()
  const scanPageSize = Math.max(40, limit * 4)
  const selected: AccountBalanceRefreshCandidate[] = []
  const selectedIds = new Set<string>()
  let wrapped = false
  for (let page = 0; page < 4 && selected.length < limit; page += 1) {
    const cursor = sqliteBalanceDueCursor
    const rows = getBusinessDatabase().prepare(`
      SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_config_json,
             balance_query_next_refresh_at, proxy_profile_id
      FROM accounts
      WHERE type = 'api_key'
        AND status = 'active'
        AND schedulable = 1
        AND balance_query_enabled = 1
        AND balance_query_next_refresh_at IS NOT NULL
        AND balance_query_next_refresh_at <= ?
        AND (? = '' OR balance_query_next_refresh_at > ? OR (balance_query_next_refresh_at = ? AND id > ?))
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL
      ORDER BY balance_query_next_refresh_at ASC, id ASC
      LIMIT ?
    `).all(now, cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.id ?? '', scanPageSize) as unknown as BalanceCandidateRow[]
    if (rows.length === 0) {
      sqliteBalanceDueCursor = undefined
      if (wrapped) break
      wrapped = true
      page -= 1
      continue
    }
    const appendResult = appendUniqueBalanceCandidates(selected, selectedIds, rows, limit)
    sqliteBalanceDueCursor = balanceDueCursorFromRow(appendResult.lastExamined)
    if (appendResult.consumedAll && rows.length < scanPageSize) {
      sqliteBalanceDueCursor = undefined
      if (wrapped) break
      wrapped = true
    }
  }
  return selected
}

export async function listAccountsDueForBalanceRefreshAsync(options: { now?: string; limit?: number } = {}): Promise<AccountBalanceRefreshCandidate[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') return listAccountsDueForBalanceRefresh(options)
  const limit = normalizedLimit(options.limit)
  const now = options.now ?? nowIso()
  const scanPageSize = Math.max(40, limit * 4)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const selected: AccountBalanceRefreshCandidate[] = []
  const selectedIds = new Set<string>()
  let wrapped = false
  for (let page = 0; page < 4 && selected.length < limit; page += 1) {
    const cursor = postgresBalanceDueCursor
    const rows = await client.query<BalanceCandidateRow>(`
      SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_config_json,
             balance_query_next_refresh_at, proxy_profile_id
      FROM juhe_business.accounts
      WHERE type = 'api_key'
        AND status = 'active'
        AND schedulable = 1
        AND balance_query_enabled = 1
        AND balance_query_next_refresh_at IS NOT NULL
        AND balance_query_next_refresh_at <= ?
        AND (? = '' OR balance_query_next_refresh_at > ? OR (balance_query_next_refresh_at = ? AND id > ?))
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL
      ORDER BY balance_query_next_refresh_at ASC, id ASC
      LIMIT ?
    `, [now, cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.id ?? '', scanPageSize])
    if (rows.length === 0) {
      postgresBalanceDueCursor = undefined
      if (wrapped) break
      wrapped = true
      page -= 1
      continue
    }
    const appendResult = appendUniqueBalanceCandidates(selected, selectedIds, rows, limit)
    postgresBalanceDueCursor = balanceDueCursorFromRow(appendResult.lastExamined)
    if (appendResult.consumedAll && rows.length < scanPageSize) {
      postgresBalanceDueCursor = undefined
      if (wrapped) break
      wrapped = true
    }
  }
  return selected
}

let sqliteBalanceRecoveryAfterId = ''
let postgresBalanceRecoveryAfterId = ''

export async function listAccountsNeedingBalanceRefreshRecoveryAsync(options: { limit?: number } = {}): Promise<AccountBalanceRefreshCandidate[]> {
  const limit = normalizedLimit(options.limit)
  const scanPageSize = Math.max(40, limit * 4)
  const maxScanPages = runtimeConfig.background.accountBalanceRecoveryMaxScanPages
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const queryRows = async (afterId: string) => await client.query<BalanceCandidateRow>(`
      SELECT a.id, a.system_account_id, a.config_revision, a.credentials_encrypted, a.balance_query_config_json,
             a.balance_query_next_refresh_at, a.proxy_profile_id
      FROM juhe_business.accounts a
      WHERE a.id > ?
        AND a.type = 'api_key'
        AND a.status = 'active'
        AND a.schedulable = 1
        AND a.balance_query_enabled = 1
        AND a.balance_query_next_refresh_at IS NULL
        AND a.deleted_at IS NULL
        AND a.authorization_instance_authorization_id IS NULL
      ORDER BY a.id ASC
      LIMIT ?
    `, [afterId, scanPageSize])
    const selected: AccountBalanceRefreshCandidate[] = []
    const selectedIds = new Set<string>()
    let wrapped = false
    for (let page = 0; page < maxScanPages && selected.length < limit; page += 1) {
      const rows = await queryRows(postgresBalanceRecoveryAfterId)
      if (rows.length === 0) {
        postgresBalanceRecoveryAfterId = ''
        if (wrapped) break
        wrapped = true
        page -= 1
        continue
      }
      const appendResult = appendUniqueBalanceCandidates(selected, selectedIds, rows, limit)
      postgresBalanceRecoveryAfterId = appendResult.lastExamined?.id ?? ''
      if (appendResult.consumedAll && rows.length < scanPageSize) {
        postgresBalanceRecoveryAfterId = ''
        if (wrapped) break
        wrapped = true
      }
    }
    return selected
  }
  const selected: AccountBalanceRefreshCandidate[] = []
  const selectedIds = new Set<string>()
  let wrapped = false
  for (let page = 0; page < maxScanPages && selected.length < limit; page += 1) {
    const rows = getBusinessDatabase().prepare(`
    SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_config_json,
           balance_query_next_refresh_at, proxy_profile_id
    FROM accounts
    WHERE id > ?
       AND type = 'api_key'
      AND status = 'active'
      AND schedulable = 1
      AND balance_query_enabled = 1
      AND balance_query_next_refresh_at IS NULL
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
    ORDER BY id ASC
    LIMIT ?
    `).all(sqliteBalanceRecoveryAfterId, scanPageSize) as unknown as BalanceCandidateRow[]
    if (rows.length === 0) {
      sqliteBalanceRecoveryAfterId = ''
      if (wrapped) break
      wrapped = true
      page -= 1
      continue
    }
    const appendResult = appendUniqueBalanceCandidates(selected, selectedIds, rows, limit)
    sqliteBalanceRecoveryAfterId = appendResult.lastExamined?.id ?? ''
    if (appendResult.consumedAll && rows.length < scanPageSize) {
      sqliteBalanceRecoveryAfterId = ''
      if (wrapped) break
      wrapped = true
    }
  }
  return selected
}

export async function findAccountBalanceRefreshCandidateAsync(accountId: string): Promise<AccountBalanceRefreshCandidate | undefined> {
  const sql = `
    SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_config_json,
           balance_query_next_refresh_at, proxy_profile_id
    FROM accounts
    WHERE id = ?
       AND type = 'api_key'
      AND status = 'active'
      AND ${balanceBooleanPredicate('schedulable', true)}
      AND ${balanceBooleanPredicate('balance_query_enabled', true)}
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
    LIMIT 1
  `
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<BalanceCandidateRow>(sql.replace('FROM accounts', 'FROM juhe_business.accounts'), [accountId])
    return row ? balanceCandidatesFromRows([row], 1)[0] : undefined
  }
  const row = getBusinessDatabase().prepare(sql).get(accountId) as unknown as BalanceCandidateRow | undefined
  return row ? balanceCandidatesFromRows([row], 1)[0] : undefined
}

export async function findAccountBalanceManualRefreshCandidateAsync(accountId: string): Promise<AccountBalanceRefreshCandidate | undefined> {
  const sql = `
    SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_config_json,
           balance_query_next_refresh_at, proxy_profile_id
    FROM accounts
    WHERE id = ?
      AND type = 'api_key'
      AND ${balanceBooleanPredicate('balance_query_enabled', true)}
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
    LIMIT 1
  `
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<BalanceCandidateRow>(sql.replace('FROM accounts', 'FROM juhe_business.accounts'), [accountId])
    return row ? balanceCandidatesFromRows([row], 1)[0] : undefined
  }
  const row = getBusinessDatabase().prepare(sql).get(accountId) as unknown as BalanceCandidateRow | undefined
  return row ? balanceCandidatesFromRows([row], 1)[0] : undefined
}

export async function findAccountBalanceDetectionCandidateAsync(
  accountId: string,
  expectedConfigRevision: number
): Promise<AccountBalanceDetectionCandidate | undefined> {
  const sql = `
    SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_next_refresh_at, proxy_profile_id
    FROM accounts
    WHERE id = ? AND config_revision = ?
      AND ${balanceDetectionCandidateWhere()}
    LIMIT 1
  `
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<BalanceDetectionCandidateRow>(sql.replace('FROM accounts', 'FROM juhe_business.accounts'), [accountId, expectedConfigRevision])
    return row ? balanceDetectionCandidateFromRow(row) : undefined
  }
  const row = getBusinessDatabase().prepare(sql).get(accountId, expectedConfigRevision) as unknown as BalanceDetectionCandidateRow | undefined
  return row ? balanceDetectionCandidateFromRow(row) : undefined
}

export async function listAccountBalanceDetectionCandidatePageAsync(options: {
  afterId?: string
  limit?: number
} = {}): Promise<AccountBalanceDetectionCandidatePage> {
  const limit = normalizedLimit(options.limit)
  const afterId = options.afterId ?? ''
  const sql = `
    SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_next_refresh_at, proxy_profile_id
    FROM accounts
    WHERE id > ? AND ${balanceDetectionCandidateWhere()}
    ORDER BY id ASC
    LIMIT ?
  `
  let rows: BalanceDetectionCandidateRow[]
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    rows = await client.query<BalanceDetectionCandidateRow>(sql.replace('FROM accounts', 'FROM juhe_business.accounts'), [afterId, limit])
  } else {
    rows = getBusinessDatabase().prepare(sql).all(afterId, limit) as unknown as BalanceDetectionCandidateRow[]
  }
  return {
    candidates: rows.map(balanceDetectionCandidateFromRow).filter((candidate): candidate is AccountBalanceDetectionCandidate => Boolean(candidate)),
    nextAfterId: rows.at(-1)?.id
  }
}

/**
 * Recovers first-detection intents that survived an ops-worker restart or a
 * transient upstream failure. It deliberately uses the same ordered, bounded
 * scan shape as enabled-balance refreshes so an invalid prefix cannot starve
 * later valid accounts.
 */
export async function listAccountsDueForBalanceAutoDetectionAsync(options: {
  now?: string
  limit?: number
} = {}): Promise<AccountBalanceDetectionCandidate[]> {
  const limit = normalizedLimit(options.limit)
  const now = options.now ?? nowIso()
  const scanPageSize = Math.max(40, limit * 4)
  const selected: AccountBalanceDetectionCandidate[] = []
  const selectedIds = new Set<string>()
  let wrapped = false
  for (let page = 0; page < 4 && selected.length < limit; page += 1) {
    const cursor = runtimeConfig.databaseDriver === 'postgres'
      ? postgresBalanceDetectionDueCursor
      : sqliteBalanceDetectionDueCursor
    const sql = `
      SELECT id, system_account_id, config_revision, credentials_encrypted, balance_query_next_refresh_at, proxy_profile_id
      FROM accounts
      WHERE balance_query_next_refresh_at IS NOT NULL
        AND balance_query_next_refresh_at <= ?
        AND (? = '' OR balance_query_next_refresh_at > ? OR (balance_query_next_refresh_at = ? AND id > ?))
        AND ${balanceDetectionCandidateWhere()}
      ORDER BY balance_query_next_refresh_at ASC, id ASC
      LIMIT ?
    `
    let rows: BalanceDetectionCandidateRow[]
    if (runtimeConfig.databaseDriver === 'postgres') {
      const client = createPostgresDatabaseClient(await getPostgresPool())
      rows = await client.query<BalanceDetectionCandidateRow>(sql.replace('FROM accounts', 'FROM juhe_business.accounts'), [
        now, cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.id ?? '', scanPageSize
      ])
    } else {
      rows = getBusinessDatabase().prepare(sql).all(
        now, cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.nextRefreshAt ?? '', cursor?.id ?? '', scanPageSize
      ) as unknown as BalanceDetectionCandidateRow[]
    }
    if (rows.length === 0) {
      if (runtimeConfig.databaseDriver === 'postgres') postgresBalanceDetectionDueCursor = undefined
      else sqliteBalanceDetectionDueCursor = undefined
      if (wrapped) break
      wrapped = true
      page -= 1
      continue
    }
    const appendResult = appendUniqueBalanceDetectionCandidates(selected, selectedIds, rows, limit)
    const nextCursor = balanceDetectionDueCursorFromRow(appendResult.lastExamined)
    if (runtimeConfig.databaseDriver === 'postgres') postgresBalanceDetectionDueCursor = nextCursor
    else sqliteBalanceDetectionDueCursor = nextCursor
    if (appendResult.consumedAll && rows.length < scanPageSize) {
      if (runtimeConfig.databaseDriver === 'postgres') postgresBalanceDetectionDueCursor = undefined
      else sqliteBalanceDetectionDueCursor = undefined
      if (wrapped) break
      wrapped = true
    }
  }
  return selected
}

export async function enableDetectedAccountBalanceQueryAsync(input: {
  accountId: string
  expectedConfigRevision: number
  expectedNextRefreshAt?: string | null
  config: AccountBalanceQueryConfig
  nextRefreshAt: string
}): Promise<boolean> {
  const config = normalizeAccountBalanceConfig(input.config)
  const updatedAt = nowIso()
  const sql = `
    UPDATE accounts
    SET balance_query_enabled = ${balanceBooleanLiteral(true)},
        balance_query_config_json = ?,
        balance_query_next_refresh_at = ?,
        updated_at = ?
    WHERE id = ?
      AND config_revision = ?
      AND ${balanceDetectionCandidateWhere()}
      ${input.expectedNextRefreshAt === undefined ? '' : input.expectedNextRefreshAt === null ? 'AND balance_query_next_refresh_at IS NULL' : 'AND balance_query_next_refresh_at = ?'}
  `
  let changed = false
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const result = await client.execute(sql.replace('UPDATE accounts', 'UPDATE juhe_business.accounts'), [
      JSON.stringify(config), input.nextRefreshAt, updatedAt, input.accountId, input.expectedConfigRevision,
      ...(input.expectedNextRefreshAt !== undefined && input.expectedNextRefreshAt !== null ? [input.expectedNextRefreshAt] : [])
    ])
    changed = Number(result.changes ?? 0) > 0
  } else {
    const result = getBusinessDatabase().prepare(sql).run(
      JSON.stringify(config), input.nextRefreshAt, updatedAt, input.accountId, input.expectedConfigRevision,
      ...(input.expectedNextRefreshAt !== undefined && input.expectedNextRefreshAt !== null ? [input.expectedNextRefreshAt] : [])
    )
    changed = Number(result.changes ?? 0) > 0
  }
  if (changed) invalidateAccountLookupCache(input.accountId)
  return changed
}

/**
 * Moves or clears a durable first-detection intent without touching a user's
 * balance configuration. The expected due value fences concurrent recovery
 * attempts and any later account save by config revision.
 */
export async function commitAccountBalanceDetectionDueAsync(input: {
  accountId: string
  expectedConfigRevision: number
  expectedNextRefreshAt: string
  nextRefreshAt: string | null
}): Promise<boolean> {
  const updatedAt = nowIso()
  const sql = `
    UPDATE accounts
    SET balance_query_next_refresh_at = ?,
        updated_at = ?
    WHERE id = ?
      AND config_revision = ?
      AND balance_query_next_refresh_at = ?
      AND ${balanceDetectionCandidateWhere()}
  `
  let changed = false
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const result = await client.execute(sql.replace('UPDATE accounts', 'UPDATE juhe_business.accounts'), [
      input.nextRefreshAt, updatedAt, input.accountId, input.expectedConfigRevision, input.expectedNextRefreshAt
    ])
    changed = Number(result.changes ?? 0) > 0
  } else {
    const result = getBusinessDatabase().prepare(sql).run(
      input.nextRefreshAt, updatedAt, input.accountId, input.expectedConfigRevision, input.expectedNextRefreshAt
    )
    changed = Number(result.changes ?? 0) > 0
  }
  if (changed) invalidateAccountLookupCache(input.accountId)
  return changed
}

export async function commitAccountBalanceRefreshAsync(input: {
  accountId: string
  expectedConfigRevision: number
  expectedConfig: AccountBalanceQueryConfig
  expectedNextRefreshAt?: string | null
  nextConfig: AccountBalanceQueryConfig
  nextRefreshAt: string | null
}): Promise<boolean> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const changed = await commitAccountBalanceRefreshInClientAsync(client, input)
    if (changed) invalidateAccountLookupCache(input.accountId)
    return changed
  }
  const expectedConfig = normalizeAccountBalanceConfig(input.expectedConfig)
  const nextConfig = normalizeAccountBalanceConfig(input.nextConfig)
  const automaticRefresh = input.expectedNextRefreshAt !== undefined
  const sql = `
    UPDATE accounts
    SET balance_query_config_json = ?,
        balance_query_next_refresh_at = ?,
        updated_at = ?
    WHERE id = ?
      AND config_revision = ?
      AND balance_query_enabled = 1
      AND balance_query_config_json = ?
      ${input.expectedNextRefreshAt === undefined ? '' : input.expectedNextRefreshAt === null ? 'AND balance_query_next_refresh_at IS NULL' : 'AND balance_query_next_refresh_at = ?'}
      ${automaticRefresh ? `AND ${balanceRefreshAutomaticCandidateWhere()}` : 'AND deleted_at IS NULL'}
  `
  const result = getBusinessDatabase().prepare(sql).run(
    JSON.stringify(nextConfig), input.nextRefreshAt, nowIso(), input.accountId,
    input.expectedConfigRevision, JSON.stringify(expectedConfig),
    ...(input.expectedNextRefreshAt !== undefined && input.expectedNextRefreshAt !== null ? [input.expectedNextRefreshAt] : [])
  )
  const changed = Number(result.changes ?? 0) > 0
  if (changed) invalidateAccountLookupCache(input.accountId)
  return changed
}

export async function persistAccountBalanceRefreshWithSnapshotAsync(input: {
  accountId: string
  systemAccountId: string
  expectedConfigRevision: number
  expectedConfig: AccountBalanceQueryConfig
  expectedNextRefreshAt?: string | null
  nextConfig: AccountBalanceQueryConfig
  nextRefreshAt: string | null
  snapshot: AccountBalanceSnapshot
}): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('余额配置与快照原子提交仅适用于 PostgreSQL')
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const changed = await client.transaction(async (tx) => {
    const committed = await commitAccountBalanceRefreshInClientAsync(tx, input)
    if (!committed) return false
    await replaceAccountBalanceSnapshotAsync({
      accountId: input.accountId,
      systemAccountId: input.systemAccountId,
      snapshot: input.snapshot,
      ...(input.nextRefreshAt ? { nextRefreshAfter: input.nextRefreshAt } : {})
    }, tx)
    return true
  })
  if (changed) invalidateAccountLookupCache(input.accountId)
  return changed
}

async function commitAccountBalanceRefreshInClientAsync(
  client: DatabaseClient,
  input: {
    accountId: string
    expectedConfigRevision: number
    expectedConfig: AccountBalanceQueryConfig
    expectedNextRefreshAt?: string | null
    nextConfig: AccountBalanceQueryConfig
    nextRefreshAt: string | null
  }
): Promise<boolean> {
  const expectedNextRefreshClause = input.expectedNextRefreshAt === undefined
    ? ''
    : input.expectedNextRefreshAt === null
      ? 'AND balance_query_next_refresh_at IS NULL'
      : 'AND balance_query_next_refresh_at = ?'
  const automaticRefreshClause = input.expectedNextRefreshAt === undefined
    ? 'AND deleted_at IS NULL'
    : `AND ${balanceRefreshAutomaticCandidateWhere()}`
  const result = await client.execute(`
    UPDATE juhe_business.accounts
    SET balance_query_config_json = ?,
        balance_query_next_refresh_at = ?,
        updated_at = ?
    WHERE id = ?
      AND config_revision = ?
      AND balance_query_enabled = 1
      AND balance_query_config_json::jsonb = ?::jsonb
      ${expectedNextRefreshClause}
      ${automaticRefreshClause}
  `, [
    JSON.stringify(normalizeAccountBalanceConfig(input.nextConfig)), input.nextRefreshAt, nowIso(), input.accountId,
    input.expectedConfigRevision, JSON.stringify(normalizeAccountBalanceConfig(input.expectedConfig)),
    ...(input.expectedNextRefreshAt !== undefined && input.expectedNextRefreshAt !== null ? [input.expectedNextRefreshAt] : [])
  ])
  return Number(result.changes ?? 0) > 0
}

export function replaceAccountBalanceSnapshot(input: {
  accountId: string
  systemAccountId: string
  snapshot: AccountBalanceSnapshot
  nextRefreshAfter?: string
}): void {
  const updatedAt = nowIso()
  getStatsDatabase().prepare(`
    INSERT INTO account_usage_snapshots (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    ) VALUES (?, ?, 'relay_balance', 'upstream_api', ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
      source = excluded.source,
      snapshot_json = excluded.snapshot_json,
      refresh_status = excluded.refresh_status,
      last_attempt_at = excluded.last_attempt_at,
      last_success_at = excluded.last_success_at,
      next_refresh_after = excluded.next_refresh_after,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at
  `).run(
    input.systemAccountId,
    input.accountId,
    JSON.stringify(input.snapshot),
    input.snapshot.status,
    input.snapshot.lastAttemptAt ?? updatedAt,
    input.snapshot.lastSuccessAt ?? null,
    input.nextRefreshAfter ?? null,
    input.snapshot.errorMessage ?? null,
    updatedAt,
    updatedAt
  )
}

export async function replaceAccountBalanceSnapshotAsync(input: {
  accountId: string
  systemAccountId: string
  snapshot: AccountBalanceSnapshot
  nextRefreshAfter?: string
}, client?: DatabaseClient): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    replaceAccountBalanceSnapshot(input)
    return
  }
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const updatedAt = nowIso()
  await databaseClient.execute(`
    INSERT INTO juhe_stats.account_usage_snapshots (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    ) VALUES (?, ?, 'relay_balance', 'upstream_api', ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
      source = EXCLUDED.source,
      snapshot_json = EXCLUDED.snapshot_json,
      refresh_status = EXCLUDED.refresh_status,
      last_attempt_at = EXCLUDED.last_attempt_at,
      last_success_at = EXCLUDED.last_success_at,
      next_refresh_after = EXCLUDED.next_refresh_after,
      last_error_message = EXCLUDED.last_error_message,
      updated_at = EXCLUDED.updated_at
  `, [
    input.systemAccountId, input.accountId, JSON.stringify(input.snapshot), input.snapshot.status,
    input.snapshot.lastAttemptAt ?? updatedAt, input.snapshot.lastSuccessAt ?? null,
    input.nextRefreshAfter ?? null, input.snapshot.errorMessage ?? null, updatedAt, updatedAt
  ])
}

export async function replaceAccountBalanceSnapshotIfCurrentAsync(input: {
  accountId: string
  systemAccountId: string
  expectedConfigRevision: number
  expectedConfig: AccountBalanceQueryConfig
  snapshot: AccountBalanceSnapshot
  nextRefreshAfter?: string
}): Promise<boolean> {
  if (!await isAccountBalanceConfigurationCurrentAsync({
    accountId: input.accountId,
    expectedConfigRevision: input.expectedConfigRevision,
    expectedConfig: input.expectedConfig
  })) return false
  await replaceAccountBalanceSnapshotAsync(input)
  return true
}

export function loadAccountBalanceSnapshotsByAccountIds(accountIds: string[]): Map<string, AccountBalanceSnapshot> {
  return snapshotsFromRecords(loadAccountBalanceSnapshotRecordsByAccountIds(accountIds))
}

export function loadAccountBalanceSnapshotRecordsByAccountIds(accountIds: string[]): Map<string, AccountBalanceSnapshotRecord> {
  const output = new Map<string, AccountBalanceSnapshotRecord>()
  for (const chunk of chunkValues([...new Set(accountIds.filter(Boolean))], 900)) {
    const rows = getStatsDatabase().prepare(`
      SELECT account_id, snapshot_json, next_refresh_after, updated_at
      FROM account_usage_snapshots
      WHERE kind = 'relay_balance' AND account_id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as Array<{ account_id: string; snapshot_json: string; next_refresh_after?: string | null; updated_at: string }>
    for (const row of rows) {
      output.set(row.account_id, {
        snapshot: parseSnapshot(row.snapshot_json),
        nextRefreshAfter: row.next_refresh_after ?? undefined,
        updatedAt: row.updated_at
      })
    }
  }
  return output
}

export async function loadAccountBalanceSnapshotsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountBalanceSnapshot>> {
  return snapshotsFromRecords(await loadAccountBalanceSnapshotRecordsByAccountIdsAsync(accountIds))
}

export async function loadAccountBalanceSnapshotRecordsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountBalanceSnapshotRecord>> {
  if (runtimeConfig.databaseDriver !== 'postgres') return loadAccountBalanceSnapshotRecordsByAccountIds(accountIds)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const output = new Map<string, AccountBalanceSnapshotRecord>()
  for (const chunk of chunkValues([...new Set(accountIds.filter(Boolean))], 900)) {
    const rows = await client.query<{ account_id: string; snapshot_json: string; next_refresh_after?: string | null; updated_at: string }>(`
      SELECT account_id, snapshot_json, next_refresh_after, updated_at
      FROM juhe_stats.account_usage_snapshots
      WHERE kind = 'relay_balance' AND account_id = ANY(?::text[])
    `, [chunk])
    for (const row of rows) {
      output.set(row.account_id, {
        snapshot: parseSnapshot(row.snapshot_json),
        nextRefreshAfter: row.next_refresh_after ?? undefined,
        updatedAt: row.updated_at
      })
    }
  }
  return output
}

export function accountBalanceSnapshotMatchesConfiguration(
  configuration: { nextRefreshAt?: string },
  record: AccountBalanceSnapshotRecord | undefined
): record is AccountBalanceSnapshotRecord {
  if (!record) return false
  return (record.nextRefreshAfter ?? undefined) === (configuration.nextRefreshAt ?? undefined)
}

export function updateAccountBalanceNextRefresh(accountId: string, nextRefreshAt: string | null): void {
  getBusinessDatabase().prepare(`UPDATE accounts SET balance_query_next_refresh_at = ?, updated_at = ? WHERE id = ?`).run(nextRefreshAt, nowIso(), accountId)
}

export async function updateAccountBalanceNextRefreshAsync(accountId: string, nextRefreshAt: string | null): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') return updateAccountBalanceNextRefresh(accountId, nextRefreshAt)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.execute(`UPDATE juhe_business.accounts SET balance_query_next_refresh_at = ?, updated_at = ? WHERE id = ?`, [nextRefreshAt, nowIso(), accountId])
}

export async function saveAccountBalanceConfigurationAsync(input: {
  accountId: string
  enabled: boolean
  config?: AccountBalanceQueryConfig
}): Promise<{ enabled: boolean; config?: AccountBalanceQueryConfig; nextRefreshAt?: string }> {
  const config = input.config ? normalizeAccountBalanceConfig(input.config) : undefined
  if (input.enabled && !config) throw new Error('开启上游余额查询时必须选择查询类型')
  const now = nowIso()
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const current = await client.one<{ balance_query_enabled: number | boolean; balance_query_config_json: string; balance_query_next_refresh_at?: string | null }>(`
      SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
      FROM juhe_business.accounts WHERE id = ? AND deleted_at IS NULL
    `, [input.accountId])
    if (!current) throw new Error('账户不存在')
    const nextRefreshAt = nextBalanceRefreshAt(current, input.enabled, config, now)
    await client.execute(`
      UPDATE juhe_business.accounts
      SET balance_query_enabled = ?, balance_query_config_json = ?, balance_query_next_refresh_at = ?, updated_at = ?
      WHERE id = ? AND deleted_at IS NULL
    `, [input.enabled ? 1 : 0, JSON.stringify(config ?? {}), nextRefreshAt ?? null, now, input.accountId])
    return { enabled: input.enabled, config, nextRefreshAt }
  }
  const current = getBusinessDatabase().prepare(`
    SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
    FROM accounts WHERE id = ? AND deleted_at IS NULL
  `).get(input.accountId) as unknown as { balance_query_enabled: number; balance_query_config_json: string; balance_query_next_refresh_at?: string | null } | undefined
  if (!current) throw new Error('账户不存在')
  const nextRefreshAt = nextBalanceRefreshAt(current, input.enabled, config, now)
  getBusinessDatabase().prepare(`
    UPDATE accounts
    SET balance_query_enabled = ?, balance_query_config_json = ?, balance_query_next_refresh_at = ?, updated_at = ?
    WHERE id = ? AND deleted_at IS NULL
  `).run(input.enabled ? 1 : 0, JSON.stringify(config ?? {}), nextRefreshAt ?? null, now, input.accountId)
  return { enabled: input.enabled, config, nextRefreshAt }
}

export async function deleteAccountBalanceSnapshotAsync(accountId: string, options: { updatedBefore?: string } = {}): Promise<void> {
  const updatedBefore = options.updatedBefore?.trim()
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    await client.execute(`
      DELETE FROM juhe_stats.account_usage_snapshots
      WHERE account_id = ?
        AND kind = 'relay_balance'
        ${updatedBefore ? 'AND updated_at <= ?' : ''}
    `, updatedBefore ? [accountId, updatedBefore] : [accountId])
    return
  }
  getStatsDatabase().prepare(`
    DELETE FROM account_usage_snapshots
    WHERE account_id = ?
      AND kind = 'relay_balance'
      ${updatedBefore ? 'AND updated_at <= ?' : ''}
  `).run(...(updatedBefore ? [accountId, updatedBefore] : [accountId]))
}

export async function loadAccountBalanceConfigurationsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, {
  enabled: boolean
  config?: AccountBalanceQueryConfig
  nextRefreshAt?: string
}>> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  const output = new Map<string, { enabled: boolean; config?: AccountBalanceQueryConfig; nextRefreshAt?: string }>()
  if (ids.length === 0) return output
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const chunk of chunkValues(ids, 900)) {
      const rows = await client.query<{ id: string; balance_query_enabled: number | boolean; balance_query_config_json: string; balance_query_next_refresh_at?: string | null }>(`
        SELECT id, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
        FROM juhe_business.accounts
        WHERE id = ANY(?::text[]) AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL
      `, [chunk])
      addBalanceConfigurations(output, rows)
    }
    return output
  }
  for (const chunk of chunkValues(ids, 900)) {
    const rows = getBusinessDatabase().prepare(`
      SELECT id, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
      FROM accounts
      WHERE id IN (${sqlPlaceholders(chunk.length)}) AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL
    `).all(...chunk) as unknown as Array<{ id: string; balance_query_enabled: number; balance_query_config_json: string; balance_query_next_refresh_at?: string | null }>
    addBalanceConfigurations(output, rows)
  }
  return output
}

function balanceCandidatesFromRows(rows: BalanceCandidateRow[], limit: number): AccountBalanceRefreshCandidate[] {
  const output: AccountBalanceRefreshCandidate[] = []
  for (const row of rows) {
    let credentials: Record<string, unknown>
    try {
      credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
    } catch {
      continue
    }
    if (!hasExactlyOneApiKey(credentials)) continue
    let config: AccountBalanceQueryConfig
    try {
      config = normalizeAccountBalanceConfig(JSON.parse(row.balance_query_config_json))
    } catch {
      continue
    }
    output.push({
      id: row.id,
      systemAccountId: row.system_account_id,
      configRevision: Number(row.config_revision),
      credentials,
      config,
      nextRefreshAt: row.balance_query_next_refresh_at,
      proxyProfileId: row.proxy_profile_id ?? undefined
    })
    if (output.length >= limit) break
  }
  return output
}

function appendUniqueBalanceCandidates(
  output: AccountBalanceRefreshCandidate[],
  selectedIds: Set<string>,
  rows: BalanceCandidateRow[],
  limit: number
): { lastExamined?: BalanceCandidateRow; consumedAll: boolean } {
  let lastExamined: BalanceCandidateRow | undefined
  for (let index = 0; index < rows.length; index += 1) {
    const row = rows[index]
    lastExamined = row
    const candidate = balanceCandidatesFromRows([row], 1)[0]
    if (!candidate) continue
    if (selectedIds.has(candidate.id)) continue
    selectedIds.add(candidate.id)
    output.push(candidate)
    if (output.length >= limit) {
      return { lastExamined, consumedAll: index === rows.length - 1 }
    }
  }
  return { lastExamined, consumedAll: true }
}

function balanceDueCursorFromRow(row: BalanceCandidateRow | undefined): BalanceDueCursor | undefined {
  const nextRefreshAt = row?.balance_query_next_refresh_at
  return row && nextRefreshAt ? { nextRefreshAt, id: row.id } : undefined
}

function balanceDetectionDueCursorFromRow(row: BalanceDetectionCandidateRow | undefined): BalanceDueCursor | undefined {
  const nextRefreshAt = row?.balance_query_next_refresh_at
  return row && nextRefreshAt ? { nextRefreshAt, id: row.id } : undefined
}

function balanceDetectionCandidateFromRow(row: BalanceDetectionCandidateRow): AccountBalanceDetectionCandidate | undefined {
  let credentials: Record<string, unknown>
  try {
    credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  } catch {
    return undefined
  }
  if (!hasExactlyOneApiKey(credentials)) return undefined
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    configRevision: Number(row.config_revision),
    credentials,
    nextRefreshAt: row.balance_query_next_refresh_at ?? undefined,
    proxyProfileId: row.proxy_profile_id ?? undefined
  }
}

function appendUniqueBalanceDetectionCandidates(
  output: AccountBalanceDetectionCandidate[],
  selectedIds: Set<string>,
  rows: BalanceDetectionCandidateRow[],
  limit: number
): { lastExamined?: BalanceDetectionCandidateRow; consumedAll: boolean } {
  let lastExamined: BalanceDetectionCandidateRow | undefined
  for (let index = 0; index < rows.length; index += 1) {
    const row = rows[index]
    lastExamined = row
    const candidate = balanceDetectionCandidateFromRow(row)
    if (!candidate || selectedIds.has(candidate.id)) continue
    selectedIds.add(candidate.id)
    output.push(candidate)
    if (output.length >= limit) {
      return { lastExamined, consumedAll: index === rows.length - 1 }
    }
  }
  return { lastExamined, consumedAll: true }
}

async function isAccountBalanceConfigurationCurrentAsync(input: {
  accountId: string
  expectedConfigRevision: number
  expectedConfig: AccountBalanceQueryConfig
}): Promise<boolean> {
  const expectedConfigJson = JSON.stringify(normalizeAccountBalanceConfig(input.expectedConfig))
  const sql = `
    SELECT balance_query_config_json
    FROM accounts
    WHERE id = ?
      AND config_revision = ?
      AND ${balanceBooleanPredicate('balance_query_enabled', true)}
      AND deleted_at IS NULL
    LIMIT 1
  `
  let configJson: string | undefined
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<{ balance_query_config_json: string }>(
      sql.replace('FROM accounts', 'FROM juhe_business.accounts'),
      [input.accountId, input.expectedConfigRevision]
    )
    configJson = row?.balance_query_config_json
  } else {
    const row = getBusinessDatabase().prepare(sql).get(input.accountId, input.expectedConfigRevision) as unknown as { balance_query_config_json: string } | undefined
    configJson = row?.balance_query_config_json
  }
  if (!configJson) return false
  try {
    return JSON.stringify(normalizeAccountBalanceConfig(JSON.parse(configJson))) === expectedConfigJson
  } catch {
    return false
  }
}

function hasExactlyOneApiKey(credentials: Record<string, unknown>): boolean {
  return effectiveAccountApiKeyCount(credentials) === 1
}

function normalizedLimit(value: number | undefined): number {
  if (value === undefined) return 100
  return Number.isInteger(value) ? Math.min(100, Math.max(1, value)) : 100
}

function parseSnapshot(value: string): AccountBalanceSnapshot {
  const parsed = JSON.parse(value) as AccountBalanceSnapshot
  return parsed
}

function snapshotsFromRecords(records: Map<string, AccountBalanceSnapshotRecord>): Map<string, AccountBalanceSnapshot> {
  return new Map([...records].map(([accountId, record]) => [accountId, record.snapshot]))
}

function configurationChanged(currentJson: string, config: AccountBalanceQueryConfig | undefined): boolean {
  try {
    return JSON.stringify(JSON.parse(currentJson)) !== JSON.stringify(config ?? {})
  } catch {
    return true
  }
}

function nextBalanceRefreshAt(
  current: { balance_query_enabled: number | boolean; balance_query_config_json: string; balance_query_next_refresh_at?: string | null },
  enabled: boolean,
  config: AccountBalanceQueryConfig | undefined,
  now: string
): string | undefined {
  if (!enabled) return undefined
  if (!databaseBoolean(current.balance_query_enabled) || configurationChanged(current.balance_query_config_json, config)) return now
  return current.balance_query_next_refresh_at ?? now
}

function addBalanceConfigurations(
  output: Map<string, { enabled: boolean; config?: AccountBalanceQueryConfig; nextRefreshAt?: string }>,
  rows: Array<{ id: string; balance_query_enabled: number | boolean; balance_query_config_json: string; balance_query_next_refresh_at?: string | null }>
): void {
  for (const row of rows) {
    let config: AccountBalanceQueryConfig | undefined
    try {
      const parsed = JSON.parse(row.balance_query_config_json)
      if (parsed && Object.keys(parsed).length > 0) config = normalizeAccountBalanceConfig(parsed)
    } catch {
      config = undefined
    }
    output.set(row.id, {
      enabled: databaseBoolean(row.balance_query_enabled),
      config,
      nextRefreshAt: row.balance_query_next_refresh_at ?? undefined
    })
  }
}

function balanceBooleanPredicate(column: 'schedulable' | 'balance_query_enabled', expected: boolean): string {
  return `${column} = ${balanceBooleanLiteral(expected)}`
}

function balanceBooleanLiteral(value: boolean): '1' | '0' {
  return value ? '1' : '0'
}

function databaseBoolean(value: number | boolean): boolean {
  return value === true || value === 1
}
