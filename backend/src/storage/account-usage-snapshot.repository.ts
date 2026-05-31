import { currentSystemAccountId } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export interface AccountUsageSnapshotUpsertInput {
  accountId: string
  kind: 'openai_codex'
  source?: string
  snapshot: Record<string, unknown>
  updatedAt?: string
}

export function upsertAccountUsageSnapshot(input: AccountUsageSnapshotUpsertInput): void {
  upsertAccountUsageSnapshots([input])
}

export function upsertAccountUsageSnapshots(inputs: AccountUsageSnapshotUpsertInput[]): void {
  if (inputs.length === 0) return

  const now = nowIso()
  const fallbackSystemAccountId = currentSystemAccountId()
  const ownersByAccountId = loadAccountSystemAccountIds(inputs.map((input) => input.accountId))
  const database = getStatsDatabase()
  const upsert = database.prepare(`
    INSERT INTO account_usage_snapshots (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_success_at, last_error_message, updated_at, created_at
    )
    VALUES (?, ?, ?, ?, ?, 'fresh', ?, NULL, ?, ?)
    ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      source = excluded.source,
      snapshot_json = excluded.snapshot_json,
      refresh_status = 'fresh',
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      updated_at = excluded.updated_at
  `)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      const updatedAt = input.updatedAt ?? now
      const systemAccountId = ownersByAccountId.get(input.accountId) ?? fallbackSystemAccountId
      upsert.run(
        systemAccountId,
        input.accountId,
        input.kind,
        input.source ?? null,
        JSON.stringify(input.snapshot),
        updatedAt,
        updatedAt,
        now
      )
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function updateAccountUsageSnapshotRefreshState(input: {
  accountId: string
  kind: 'openai_codex'
  status: 'pending' | 'fresh' | 'failed' | 'rate_limited'
  attemptedAt?: string
  successAt?: string
  nextRefreshAfter?: string
  errorMessage?: string
}): void {
  const now = nowIso()
  const systemAccountId = accountSystemAccountId(input.accountId) ?? currentSystemAccountId()
  getStatsDatabase()
    .prepare(`
      INSERT INTO account_usage_snapshots (
        system_account_id, account_id, kind, source, snapshot_json, refresh_status,
        last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
      )
      VALUES (?, ?, ?, NULL, '{}', ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
        refresh_status = excluded.refresh_status,
        last_attempt_at = COALESCE(excluded.last_attempt_at, account_usage_snapshots.last_attempt_at),
        last_success_at = COALESCE(excluded.last_success_at, account_usage_snapshots.last_success_at),
        next_refresh_after = excluded.next_refresh_after,
        last_error_message = excluded.last_error_message,
        updated_at = excluded.updated_at
    `)
    .run(
      systemAccountId,
      input.accountId,
      input.kind,
      input.status,
      input.attemptedAt ?? null,
      input.successAt ?? (input.status === 'fresh' ? now : null),
      input.nextRefreshAfter ?? null,
      input.errorMessage ?? null,
      now,
      now
    )
}

function accountSystemAccountId(accountId: string): string | undefined {
  const row = getBusinessDatabase().prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(accountId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function loadAccountSystemAccountIds(accountIds: string[]): Map<string, string> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  const output = new Map<string, string>()
  for (const chunk of chunkValues(ids, 900)) {
    const rows = getBusinessDatabase()
      .prepare(`SELECT id, system_account_id FROM accounts WHERE id IN (${sqlPlaceholders(chunk.length)})`)
      .all(...chunk) as unknown as Array<{ id?: string; system_account_id?: string }>
    for (const row of rows) {
      if (row.id && row.system_account_id) {
        output.set(row.id, row.system_account_id)
      }
    }
  }
  return output
}
