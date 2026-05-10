import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'

export type AccountQualityState = 'fresh' | 'stale' | 'failed' | 'unknown'

export interface AccountQualityRealtimeRefreshResult {
  refreshed: number
  removed: number
  windowStartedAt: string
  windowEndedAt: string
}

interface AccountQualityRow {
  account_id: string
  system_account_id: string
  provider_code: string
  quality_score: number
  quality_state: AccountQualityState
  recent_request_count: number
  recent_success_count: number
  recent_error_count: number
  recent_first_token_sample_count: number
  recent_avg_first_token_ms: number | null
  ewma_first_token_ms: number | null
  success_rate: number | null
  window_started_at: string
  window_ended_at: string
  last_sample_at: string | null
  last_success_at: string | null
  last_error_at: string | null
  last_error_message: string | null
  updated_at: string
}

const unknownQualityScore = 1_000_000
const failurePenaltyMs = 60_000
const stalePenaltyMs = 5_000

export function refreshAccountQualityFromUsage(windowMinutes = 10): AccountQualityRealtimeRefreshResult {
  const database = getDatabase()
  const now = new Date()
  const windowMs = Math.max(1, Math.min(Math.trunc(windowMinutes), 24 * 60)) * 60 * 1000
  const windowStartedAt = new Date(now.getTime() - windowMs).toISOString()
  const windowEndedAt = now.toISOString()
  const updatedAt = nowIso()

  const rows = database
    .prepare(`
      SELECT
        usage_records.account_id,
        accounts.system_account_id,
        accounts.provider_code,
        COUNT(*) AS recent_request_count,
        SUM(CASE WHEN usage_records.success = 1 THEN 1 ELSE 0 END) AS recent_success_count,
        SUM(CASE WHEN usage_records.success = 1 THEN 0 ELSE 1 END) AS recent_error_count,
        SUM(CASE WHEN usage_records.success = 1 AND usage_records.first_token_ms IS NOT NULL AND usage_records.first_token_ms >= 0 THEN 1 ELSE 0 END) AS recent_first_token_sample_count,
        AVG(CASE WHEN usage_records.success = 1 AND usage_records.first_token_ms IS NOT NULL AND usage_records.first_token_ms >= 0 THEN usage_records.first_token_ms ELSE NULL END) AS recent_avg_first_token_ms,
        MAX(usage_records.created_at) AS last_sample_at,
        MAX(CASE WHEN usage_records.success = 1 THEN usage_records.created_at ELSE NULL END) AS last_success_at,
        MAX(CASE WHEN usage_records.success = 0 THEN usage_records.created_at ELSE NULL END) AS last_error_at,
        (
          SELECT latest_error.error_message
          FROM usage_records latest_error
          WHERE latest_error.account_id = usage_records.account_id
            AND latest_error.api_key_id IS NOT NULL
            AND latest_error.success = 0
            AND latest_error.created_at >= ?
          ORDER BY latest_error.created_at DESC, latest_error.id DESC
          LIMIT 1
        ) AS last_error_message
      FROM usage_records
      INNER JOIN accounts ON accounts.id = usage_records.account_id
      WHERE usage_records.account_id IS NOT NULL
        AND usage_records.api_key_id IS NOT NULL
        AND usage_records.created_at >= ?
      GROUP BY usage_records.account_id, accounts.system_account_id, accounts.provider_code
    `)
    .all(windowStartedAt, windowStartedAt) as unknown as Array<{
      account_id: string
      system_account_id: string
      provider_code: string
      recent_request_count: number
      recent_success_count: number
      recent_error_count: number
      recent_first_token_sample_count: number
      recent_avg_first_token_ms: number | null
      last_sample_at: string | null
      last_success_at: string | null
      last_error_at: string | null
      last_error_message: string | null
    }>

  const activeAccountIds = new Set(rows.map((row) => row.account_id))
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const deleteResult = database
      .prepare('DELETE FROM account_quality_scores WHERE account_id NOT IN (SELECT id FROM accounts)')
      .run()
    for (const row of rows) {
      const previous = accountQualityRow(row.account_id)
      const recentAvg = integerOrNull(row.recent_avg_first_token_ms)
      const previousEwma = previous?.ewma_first_token_ms ?? null
      const ewmaFirstTokenMs = recentAvg === null
        ? previousEwma
        : previousEwma === null
          ? recentAvg
          : Math.round(previousEwma * 0.6 + recentAvg * 0.4)
      const successRate = row.recent_request_count > 0
        ? Math.max(0, Math.min(1, row.recent_success_count / row.recent_request_count))
        : previous?.success_rate ?? null
      const qualityState = row.recent_first_token_sample_count > 0 ? 'fresh' : row.recent_success_count > 0 ? 'unknown' : 'failed'
      const qualityScore = computeQualityScore({
        ewmaFirstTokenMs,
        successRate,
        qualityState,
        updatedAt
      })
      upsertAccountQuality({
        accountId: row.account_id,
        systemAccountId: row.system_account_id,
        providerCode: row.provider_code,
        qualityScore,
        qualityState,
        recentRequestCount: row.recent_request_count,
        recentSuccessCount: row.recent_success_count,
        recentErrorCount: row.recent_error_count,
        recentFirstTokenSampleCount: row.recent_first_token_sample_count,
        recentAvgFirstTokenMs: recentAvg,
        ewmaFirstTokenMs,
        successRate,
        windowStartedAt,
        windowEndedAt,
        lastSampleAt: row.last_sample_at ?? previous?.last_sample_at ?? undefined,
        lastSuccessAt: row.last_success_at ?? previous?.last_success_at ?? undefined,
        lastErrorAt: row.last_error_at ?? previous?.last_error_at ?? undefined,
        lastErrorMessage: row.last_error_message ?? previous?.last_error_message ?? undefined,
        updatedAt
      })
    }

    const existingRows = database.prepare('SELECT account_id FROM account_quality_scores').all() as unknown as Array<{ account_id: string }>
    for (const existing of existingRows) {
      if (activeAccountIds.has(existing.account_id)) {
        continue
      }
      markAccountQualityStale(existing.account_id, windowStartedAt, windowEndedAt, updatedAt)
    }

    commitDatabaseTransaction(database, transactionStarted)
    return { refreshed: rows.length, removed: Number(deleteResult.changes ?? 0), windowStartedAt, windowEndedAt }
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

function accountQualityRow(accountId: string): AccountQualityRow | undefined {
  return getDatabase()
    .prepare('SELECT * FROM account_quality_scores WHERE account_id = ?')
    .get(accountId) as unknown as AccountQualityRow | undefined
}

function markAccountQualityStale(accountId: string, windowStartedAt: string, windowEndedAt: string, updatedAt: string): void {
  const previous = accountQualityRow(accountId)
  if (!previous) {
    return
  }
  const qualityState: AccountQualityState = previous.quality_state === 'fresh' ? 'stale' : previous.quality_state
  const qualityScore = computeQualityScore({
    ewmaFirstTokenMs: previous.ewma_first_token_ms,
    successRate: previous.success_rate,
    qualityState,
    updatedAt
  })
  upsertAccountQuality({
    accountId,
    systemAccountId: previous.system_account_id,
    providerCode: previous.provider_code,
    qualityScore,
    qualityState,
    recentRequestCount: 0,
    recentSuccessCount: 0,
    recentErrorCount: 0,
    recentFirstTokenSampleCount: 0,
    recentAvgFirstTokenMs: undefined,
    ewmaFirstTokenMs: previous.ewma_first_token_ms ?? undefined,
    successRate: previous.success_rate ?? undefined,
    windowStartedAt,
    windowEndedAt,
    lastSampleAt: previous.last_sample_at ?? undefined,
    lastSuccessAt: previous.last_success_at ?? undefined,
    lastErrorAt: previous.last_error_at ?? undefined,
    lastErrorMessage: previous.last_error_message ?? undefined,
    updatedAt
  })
}

function upsertAccountQuality(input: {
  accountId: string
  systemAccountId: string
  providerCode: string
  qualityScore: number
  qualityState: AccountQualityState
  recentRequestCount: number
  recentSuccessCount: number
  recentErrorCount: number
  recentFirstTokenSampleCount: number
  recentAvgFirstTokenMs?: number | null
  ewmaFirstTokenMs?: number | null
  successRate?: number | null
  windowStartedAt: string
  windowEndedAt: string
  lastSampleAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastErrorMessage?: string
  updatedAt: string
}): void {
  getDatabase()
    .prepare(`
      INSERT INTO account_quality_scores (
        account_id, system_account_id, provider_code, quality_score, quality_state,
        recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
        recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
        window_started_at, window_ended_at, last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(account_id) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        provider_code = excluded.provider_code,
        quality_score = excluded.quality_score,
        quality_state = excluded.quality_state,
        recent_request_count = excluded.recent_request_count,
        recent_success_count = excluded.recent_success_count,
        recent_error_count = excluded.recent_error_count,
        recent_first_token_sample_count = excluded.recent_first_token_sample_count,
        recent_avg_first_token_ms = excluded.recent_avg_first_token_ms,
        ewma_first_token_ms = excluded.ewma_first_token_ms,
        success_rate = excluded.success_rate,
        window_started_at = excluded.window_started_at,
        window_ended_at = excluded.window_ended_at,
        last_sample_at = excluded.last_sample_at,
        last_success_at = excluded.last_success_at,
        last_error_at = excluded.last_error_at,
        last_error_message = excluded.last_error_message,
        updated_at = excluded.updated_at
    `)
    .run(
      input.accountId,
      input.systemAccountId,
      input.providerCode,
      input.qualityScore,
      input.qualityState,
      Math.max(0, Math.trunc(input.recentRequestCount)),
      Math.max(0, Math.trunc(input.recentSuccessCount)),
      Math.max(0, Math.trunc(input.recentErrorCount)),
      Math.max(0, Math.trunc(input.recentFirstTokenSampleCount)),
      nullableInteger(input.recentAvgFirstTokenMs),
      nullableInteger(input.ewmaFirstTokenMs),
      nullableRate(input.successRate),
      input.windowStartedAt,
      input.windowEndedAt,
      input.lastSampleAt ?? null,
      input.lastSuccessAt ?? null,
      input.lastErrorAt ?? null,
      input.lastErrorMessage ?? null,
      input.updatedAt
    )
}

function computeQualityScore(input: {
  ewmaFirstTokenMs?: number | null
  successRate?: number | null
  qualityState: AccountQualityState
  updatedAt: string
}): number {
  const latency = typeof input.ewmaFirstTokenMs === 'number' ? input.ewmaFirstTokenMs : unknownQualityScore
  const successRate = typeof input.successRate === 'number' ? input.successRate : 0.5
  const errorPenalty = Math.round((1 - successRate) * 20_000)
  const statePenalty = input.qualityState === 'failed'
    ? failurePenaltyMs
    : input.qualityState === 'stale'
      ? stalePenaltyMs
      : input.qualityState === 'unknown'
        ? 10_000
        : 0
  const agePenalty = agePenaltyMs(input.updatedAt)
  return Math.max(0, Math.trunc(latency + errorPenalty + statePenalty + agePenalty))
}

function agePenaltyMs(updatedAt: string): number {
  const updatedTime = Date.parse(updatedAt)
  if (!Number.isFinite(updatedTime)) {
    return stalePenaltyMs
  }
  const ageMinutes = Math.max(0, Math.floor((Date.now() - updatedTime) / 60_000))
  return Math.min(10_000, ageMinutes * 100)
}

function integerOrNull(value: unknown): number | null {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.max(0, Math.round(number)) : null
}

function nullableInteger(value: unknown): number | null {
  return integerOrNull(value)
}

function nullableRate(value: unknown): number | null {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.max(0, Math.min(1, number)) : null
}
