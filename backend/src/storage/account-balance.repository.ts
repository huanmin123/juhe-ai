import { runtimeConfig } from '../config/runtime.js'
import type { AccountBalanceQueryConfig, AccountBalanceSnapshot } from '../modules/accounts/account-balance.types.js'
import { normalizeAccountBalanceConfig } from '../modules/accounts/account-balance-config.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { decryptJson } from './crypto.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export interface AccountBalanceRefreshCandidate {
  id: string
  systemAccountId: string
  credentials: Record<string, unknown>
  config: AccountBalanceQueryConfig
  nextRefreshAt: string
  proxyProfileId?: string
}

interface BalanceCandidateRow {
  id: string
  system_account_id: string
  credentials_encrypted: string
  balance_query_config_json: string
  balance_query_next_refresh_at: string
  proxy_profile_id?: string | null
}

export function listAccountsDueForBalanceRefresh(options: { now?: string; limit?: number } = {}): AccountBalanceRefreshCandidate[] {
  const limit = normalizedLimit(options.limit)
  const rows = getBusinessDatabase().prepare(`
    SELECT id, system_account_id, credentials_encrypted, balance_query_config_json,
           balance_query_next_refresh_at, proxy_profile_id
    FROM accounts
    WHERE status = 'active'
      AND schedulable = 1
      AND type = 'api_key'
      AND balance_query_enabled = 1
      AND balance_query_next_refresh_at IS NOT NULL
      AND balance_query_next_refresh_at <= ?
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
    ORDER BY balance_query_next_refresh_at ASC, id ASC
    LIMIT ?
  `).all(options.now ?? nowIso(), limit * 4) as unknown as BalanceCandidateRow[]
  return balanceCandidatesFromRows(rows, limit)
}

export async function listAccountsDueForBalanceRefreshAsync(options: { now?: string; limit?: number } = {}): Promise<AccountBalanceRefreshCandidate[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') return listAccountsDueForBalanceRefresh(options)
  const limit = normalizedLimit(options.limit)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<BalanceCandidateRow>(`
    SELECT id, system_account_id, credentials_encrypted, balance_query_config_json,
           balance_query_next_refresh_at, proxy_profile_id
    FROM juhe_business.accounts
    WHERE status = 'active'
      AND schedulable = 1
      AND type = 'api_key'
      AND balance_query_enabled = 1
      AND balance_query_next_refresh_at IS NOT NULL
      AND balance_query_next_refresh_at <= ?
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
    ORDER BY balance_query_next_refresh_at ASC, id ASC
    LIMIT ?
  `, [options.now ?? nowIso(), limit * 4])
  return balanceCandidatesFromRows(rows, limit)
}

export async function findAccountBalanceRefreshCandidateAsync(accountId: string): Promise<AccountBalanceRefreshCandidate | undefined> {
  const sql = `
    SELECT id, system_account_id, credentials_encrypted, balance_query_config_json,
           balance_query_next_refresh_at, proxy_profile_id
    FROM accounts
    WHERE id = ?
      AND status = 'active'
      AND schedulable = 1
      AND type = 'api_key'
      AND balance_query_enabled = 1
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

export function loadAccountBalanceSnapshotsByAccountIds(accountIds: string[]): Map<string, AccountBalanceSnapshot> {
  const output = new Map<string, AccountBalanceSnapshot>()
  for (const chunk of chunkValues([...new Set(accountIds.filter(Boolean))], 900)) {
    const rows = getStatsDatabase().prepare(`
      SELECT account_id, snapshot_json
      FROM account_usage_snapshots
      WHERE kind = 'relay_balance' AND account_id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as Array<{ account_id: string; snapshot_json: string }>
    for (const row of rows) output.set(row.account_id, parseSnapshot(row.snapshot_json))
  }
  return output
}

export async function loadAccountBalanceSnapshotsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountBalanceSnapshot>> {
  if (runtimeConfig.databaseDriver !== 'postgres') return loadAccountBalanceSnapshotsByAccountIds(accountIds)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const output = new Map<string, AccountBalanceSnapshot>()
  for (const chunk of chunkValues([...new Set(accountIds.filter(Boolean))], 900)) {
    const rows = await client.query<{ account_id: string; snapshot_json: string }>(`
      SELECT account_id, snapshot_json
      FROM juhe_stats.account_usage_snapshots
      WHERE kind = 'relay_balance' AND account_id = ANY(?::text[])
    `, [chunk])
    for (const row of rows) output.set(row.account_id, parseSnapshot(row.snapshot_json))
  }
  return output
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
    const current = await client.one<{ balance_query_enabled: number; balance_query_config_json: string; balance_query_next_refresh_at?: string | null }>(`
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
    if (!input.enabled || configurationChanged(current.balance_query_config_json, config)) {
      await client.execute(`DELETE FROM juhe_stats.account_usage_snapshots WHERE account_id = ? AND kind = 'relay_balance'`, [input.accountId])
    }
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
  if (!input.enabled || configurationChanged(current.balance_query_config_json, config)) {
    getStatsDatabase().prepare(`DELETE FROM account_usage_snapshots WHERE account_id = ? AND kind = 'relay_balance'`).run(input.accountId)
  }
  return { enabled: input.enabled, config, nextRefreshAt }
}

export async function deleteAccountBalanceSnapshotAsync(accountId: string): Promise<void> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    await client.execute(`DELETE FROM juhe_stats.account_usage_snapshots WHERE account_id = ? AND kind = 'relay_balance'`, [accountId])
    return
  }
  getStatsDatabase().prepare(`DELETE FROM account_usage_snapshots WHERE account_id = ? AND kind = 'relay_balance'`).run(accountId)
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
      const rows = await client.query<{ id: string; balance_query_enabled: number; balance_query_config_json: string; balance_query_next_refresh_at?: string | null }>(`
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
      credentials,
      config,
      nextRefreshAt: row.balance_query_next_refresh_at,
      proxyProfileId: row.proxy_profile_id ?? undefined
    })
    if (output.length >= limit) break
  }
  return output
}

function hasExactlyOneApiKey(credentials: Record<string, unknown>): boolean {
  const pool = Array.isArray(credentials.api_keys)
    ? credentials.api_keys.filter((value) => typeof value === 'string' && value.trim().length > 0)
    : []
  if (pool.length > 0) return pool.length === 1
  return typeof credentials.api_key === 'string' && credentials.api_key.trim().length > 0
}

function normalizedLimit(value: number | undefined): number {
  if (value === undefined) return 100
  return Number.isInteger(value) ? Math.min(100, Math.max(1, value)) : 100
}

function parseSnapshot(value: string): AccountBalanceSnapshot {
  const parsed = JSON.parse(value) as AccountBalanceSnapshot
  return parsed
}

function configurationChanged(currentJson: string, config: AccountBalanceQueryConfig | undefined): boolean {
  try {
    return JSON.stringify(JSON.parse(currentJson)) !== JSON.stringify(config ?? {})
  } catch {
    return true
  }
}

function nextBalanceRefreshAt(
  current: { balance_query_enabled: number; balance_query_config_json: string; balance_query_next_refresh_at?: string | null },
  enabled: boolean,
  config: AccountBalanceQueryConfig | undefined,
  now: string
): string | undefined {
  if (!enabled) return undefined
  if (current.balance_query_enabled !== 1 || configurationChanged(current.balance_query_config_json, config)) return now
  return current.balance_query_next_refresh_at ?? now
}

function addBalanceConfigurations(
  output: Map<string, { enabled: boolean; config?: AccountBalanceQueryConfig; nextRefreshAt?: string }>,
  rows: Array<{ id: string; balance_query_enabled: number; balance_query_config_json: string; balance_query_next_refresh_at?: string | null }>
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
      enabled: row.balance_query_enabled === 1,
      config,
      nextRefreshAt: row.balance_query_next_refresh_at ?? undefined
    })
  }
}
