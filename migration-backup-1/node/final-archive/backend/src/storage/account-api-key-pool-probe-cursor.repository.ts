import type { DatabaseSync } from 'node:sqlite'

import { getBusinessDatabase, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'

export type AccountApiKeyPoolProbeCursorPurpose = 'health_check' | 'cooldown_retest'

export interface AccountApiKeyPoolProbeCursor {
  accountId: string
  purpose: AccountApiKeyPoolProbeCursorPurpose
  lastCompletedKeyFingerprint?: string
  keySetFingerprint: string
  configRevision: number
  dispatchRevision?: number
  cooldownGeneration?: string
  sourceConfigRevision?: number
  updatedAt: string
}

export interface AccountApiKeyPoolProbeCursorInput {
  accountId: string
  purpose: AccountApiKeyPoolProbeCursorPurpose
  lastCompletedKeyFingerprint?: string
  keySetFingerprint: string
  configRevision: number
  dispatchRevision?: number
  cooldownGeneration?: string
  sourceConfigRevision?: number
  updatedAt?: string
}

type CursorRow = {
  account_id: string
  purpose: AccountApiKeyPoolProbeCursorPurpose
  last_completed_key_fingerprint?: string | null
  key_set_fingerprint: string
  config_revision: number
  dispatch_revision?: number | null
  cooldown_generation?: string | null
  source_config_revision?: number | null
  updated_at: string
}

function normalizeInput(input: AccountApiKeyPoolProbeCursorInput): AccountApiKeyPoolProbeCursorInput {
  const accountId = input.accountId.trim()
  const keySetFingerprint = input.keySetFingerprint.trim()
  if (!accountId || !keySetFingerprint || !Number.isInteger(input.configRevision) || input.configRevision < 1) {
    throw new Error('API Key 探针游标参数无效')
  }
  return {
    ...input,
    accountId,
    keySetFingerprint,
    lastCompletedKeyFingerprint: input.lastCompletedKeyFingerprint?.trim() || undefined,
    cooldownGeneration: input.cooldownGeneration?.trim() || undefined,
    updatedAt: input.updatedAt ?? nowIso()
  }
}

function fromRow(row: CursorRow | undefined): AccountApiKeyPoolProbeCursor | undefined {
  if (!row) return undefined
  return {
    accountId: String(row.account_id),
    purpose: row.purpose,
    lastCompletedKeyFingerprint: row.last_completed_key_fingerprint ?? undefined,
    keySetFingerprint: String(row.key_set_fingerprint),
    configRevision: Number(row.config_revision),
    dispatchRevision: row.dispatch_revision == null ? undefined : Number(row.dispatch_revision),
    cooldownGeneration: row.cooldown_generation ?? undefined,
    sourceConfigRevision: row.source_config_revision == null ? undefined : Number(row.source_config_revision),
    updatedAt: String(row.updated_at)
  }
}

export function findAccountApiKeyPoolProbeCursor(
  accountId: string,
  purpose: AccountApiKeyPoolProbeCursorPurpose,
  database: DatabaseSync = getBusinessDatabase()
): AccountApiKeyPoolProbeCursor | undefined {
  const row = database.prepare(`
    SELECT account_id, purpose, last_completed_key_fingerprint, key_set_fingerprint,
      config_revision, dispatch_revision, cooldown_generation, source_config_revision, updated_at
    FROM account_api_key_pool_probe_cursors
    WHERE account_id = ? AND purpose = ?
  `).get(accountId.trim(), purpose) as CursorRow | undefined
  return fromRow(row)
}

export async function findAccountApiKeyPoolProbeCursorAsync(
  client: DatabaseClient,
  accountId: string,
  purpose: AccountApiKeyPoolProbeCursorPurpose
): Promise<AccountApiKeyPoolProbeCursor | undefined> {
  const row = await client.one<CursorRow>(`
    SELECT account_id, purpose, last_completed_key_fingerprint, key_set_fingerprint,
      config_revision, dispatch_revision, cooldown_generation, source_config_revision, updated_at
    FROM ${client.dialect.qualifyTable('juhe_business', 'account_api_key_pool_probe_cursors')}
    WHERE account_id = ? AND purpose = ?
  `, [accountId.trim(), purpose])
  return fromRow(row)
}

export function saveAccountApiKeyPoolProbeCursor(
  input: AccountApiKeyPoolProbeCursorInput,
  database: DatabaseSync = getBusinessDatabase()
): AccountApiKeyPoolProbeCursor {
  const normalized = normalizeInput(input)
  const updatedAt = normalized.updatedAt ?? nowIso()
  database.prepare(`
    INSERT INTO account_api_key_pool_probe_cursors (
      account_id, purpose, last_completed_key_fingerprint, key_set_fingerprint,
      config_revision, dispatch_revision, cooldown_generation, source_config_revision, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id, purpose) DO UPDATE SET
      last_completed_key_fingerprint = excluded.last_completed_key_fingerprint,
      key_set_fingerprint = excluded.key_set_fingerprint,
      config_revision = excluded.config_revision,
      dispatch_revision = excluded.dispatch_revision,
      cooldown_generation = excluded.cooldown_generation,
      source_config_revision = excluded.source_config_revision,
      updated_at = excluded.updated_at
  `).run(
    normalized.accountId,
    normalized.purpose,
    normalized.lastCompletedKeyFingerprint ?? null,
    normalized.keySetFingerprint,
    normalized.configRevision,
    normalized.dispatchRevision ?? null,
    normalized.cooldownGeneration ?? null,
    normalized.sourceConfigRevision ?? null,
    updatedAt
  )
  return { ...normalized, updatedAt } as AccountApiKeyPoolProbeCursor
}

export async function saveAccountApiKeyPoolProbeCursorAsync(
  client: DatabaseClient,
  input: AccountApiKeyPoolProbeCursorInput
): Promise<AccountApiKeyPoolProbeCursor> {
  const normalized = normalizeInput(input)
  const updatedAt = normalized.updatedAt ?? nowIso()
  await client.execute(`
    INSERT INTO ${client.dialect.qualifyTable('juhe_business', 'account_api_key_pool_probe_cursors')} (
      account_id, purpose, last_completed_key_fingerprint, key_set_fingerprint,
      config_revision, dispatch_revision, cooldown_generation, source_config_revision, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id, purpose) DO UPDATE SET
      last_completed_key_fingerprint = excluded.last_completed_key_fingerprint,
      key_set_fingerprint = excluded.key_set_fingerprint,
      config_revision = excluded.config_revision,
      dispatch_revision = excluded.dispatch_revision,
      cooldown_generation = excluded.cooldown_generation,
      source_config_revision = excluded.source_config_revision,
      updated_at = excluded.updated_at
  `, [
    normalized.accountId,
    normalized.purpose,
    normalized.lastCompletedKeyFingerprint ?? null,
    normalized.keySetFingerprint,
    normalized.configRevision,
    normalized.dispatchRevision ?? null,
    normalized.cooldownGeneration ?? null,
    normalized.sourceConfigRevision ?? null,
    updatedAt
  ])
  return { ...normalized, updatedAt } as AccountApiKeyPoolProbeCursor
}

export function deleteAccountApiKeyPoolProbeCursor(
  accountId: string,
  purpose: AccountApiKeyPoolProbeCursorPurpose,
  database: DatabaseSync = getBusinessDatabase()
): void {
  database.prepare('DELETE FROM account_api_key_pool_probe_cursors WHERE account_id = ? AND purpose = ?').run(accountId.trim(), purpose)
}

export async function deleteAccountApiKeyPoolProbeCursorAsync(
  client: DatabaseClient,
  accountId: string,
  purpose: AccountApiKeyPoolProbeCursorPurpose
): Promise<void> {
  await client.execute(`DELETE FROM ${client.dialect.qualifyTable('juhe_business', 'account_api_key_pool_probe_cursors')} WHERE account_id = ? AND purpose = ?`, [accountId.trim(), purpose])
}
