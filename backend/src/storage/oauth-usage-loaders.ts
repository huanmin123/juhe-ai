import type { AccountOAuthUsageSnapshot, AccountOAuthUsageWindow } from '../domain/types.js'
import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { getRecordDatabase } from './database.js'
import { sqlPlaceholders } from './query-utils.js'
import { numberFromUnknown } from './usage-stats-helpers.js'
import { optionalString, parseOptionalJsonObject } from './value-utils.js'

interface AccountUsageSnapshotRow {
  system_account_id: string
  account_id: string
  kind: string
  source: string | null
  snapshot_json: string
  refresh_status: string | null
  last_attempt_at: string | null
  last_success_at: string | null
  next_refresh_after: string | null
  last_error_message: string | null
  updated_at: string
}

function uniqueIds(values: string[]): string[] {
  return [...new Set(values)].filter(Boolean)
}

export function loadOpenAICodexUsageSnapshots(access?: AccessScope): Map<string, AccountOAuthUsageSnapshot> {
  const scope = buildSystemAccountScopeClause(access)
  const rows = getRecordDatabase().prepare(`
    SELECT
      account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at
    FROM account_usage_snapshots
    WHERE kind = 'openai_codex'${scope.clause}
  `).all(...scope.params) as unknown as AccountUsageSnapshotRow[]
  return oauthUsageSnapshotsFromRows(rows)
}

export function loadOpenAICodexUsageSnapshotsByAccountIds(accountIds: string[]): Map<string, AccountOAuthUsageSnapshot> {
  const ids = uniqueIds(accountIds)
  if (!ids.length) return new Map()
  const rows = getRecordDatabase().prepare(`
    SELECT
      account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at
    FROM account_usage_snapshots
    WHERE kind = 'openai_codex' AND account_id IN (${sqlPlaceholders(ids.length)})
  `).all(...ids) as unknown as AccountUsageSnapshotRow[]
  return oauthUsageSnapshotsFromRows(rows)
}

function oauthUsageSnapshotsFromRows(rows: AccountUsageSnapshotRow[]): Map<string, AccountOAuthUsageSnapshot> {
  const result = new Map<string, AccountOAuthUsageSnapshot>()
  for (const row of rows) {
    const snapshot = parseOptionalJsonObject(row.snapshot_json)
    if (!snapshot) continue
    result.set(row.account_id, {
      kind: 'openai_codex',
      source: row.source ?? optionalString(snapshot.source),
      updatedAt: row.updated_at,
      refreshStatus: row.refresh_status ?? undefined,
      lastAttemptAt: row.last_attempt_at ?? undefined,
      lastSuccessAt: row.last_success_at ?? undefined,
      nextRefreshAfter: row.next_refresh_after ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      fiveHour: oauthUsageWindowFromSnapshot(snapshot, '5h', row.updated_at),
      sevenDay: oauthUsageWindowFromSnapshot(snapshot, '7d', row.updated_at)
    })
  }
  return result
}

function oauthUsageWindowFromSnapshot(snapshot: Record<string, unknown>, window: '5h' | '7d', updatedAt: string): AccountOAuthUsageWindow | undefined {
  const utilization = numberFromUnknown(snapshot[`codex_${window}_used_percent`])
  if (utilization === undefined) return undefined
  const resetAt = optionalString(snapshot[`codex_${window}_reset_at`]) ?? resetAtFromSeconds(updatedAt, numberFromUnknown(snapshot[`codex_${window}_reset_after_seconds`]))
  const remainingSeconds = resetAt ? Math.max(0, Math.ceil((Date.parse(resetAt) - Date.now()) / 1000)) : 0
  const isExpired = resetAt ? Date.parse(resetAt) <= Date.now() : false
  return {
    utilization: isExpired ? 0 : utilization,
    resetsAt: resetAt,
    remainingSeconds,
    windowMinutes: numberFromUnknown(snapshot[`codex_${window}_window_minutes`])
  }
}

function resetAtFromSeconds(updatedAt: string, seconds?: number): string | undefined {
  if (seconds === undefined || seconds <= 0) return undefined
  const baseTime = Date.parse(updatedAt)
  if (!Number.isFinite(baseTime)) return undefined
  return new Date(baseTime + seconds * 1000).toISOString()
}
