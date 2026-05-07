import type { AccountStatus, AccountType } from '../domain/types.js'
import { getDatabase, nowIso } from './database.js'

export type AccountQualityState = 'fresh' | 'stale' | 'probing' | 'failed' | 'unknown'

export interface AccountQualityRefreshCandidate {
  accountId: string
  accountOwnerSystemAccountId: string
  probeSystemAccountId: string
  groupId: string
  providerCode: string
  type: AccountType
  name: string
  priority: number
  superPriorityEnabled: boolean
  qualityState?: AccountQualityState
  qualityScore?: number
  lastProbeAt?: string
  lastSampleAt?: string
  updatedAt?: string
}

export interface AccountQualityScoreInput {
  accountId: string
  systemAccountId: string
  providerCode: string
  firstTokenMs?: number
  success: boolean
  errorMessage?: string
  sampledAt?: string
  source: 'probe' | 'traffic'
}

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
  last_probe_at: string | null
  last_success_at: string | null
  last_error_at: string | null
  last_error_message: string | null
  updated_at: string
}

interface CandidateRow {
  account_id: string
  account_owner_system_account_id: string
  probe_system_account_id: string
  group_id: string
  provider_code: string
  type: AccountType
  name: string
  status: AccountStatus
  priority: number
  super_priority_enabled: number
  quality_state?: AccountQualityState | null
  quality_score?: number | null
  last_probe_at?: string | null
  last_sample_at?: string | null
  quality_updated_at?: string | null
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
  database.exec('BEGIN')
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
        lastProbeAt: previous?.last_probe_at ?? undefined,
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

    database.exec('COMMIT')
    return { refreshed: rows.length, removed: Number(deleteResult.changes ?? 0), windowStartedAt, windowEndedAt }
  } catch (error) {
    try {
      database.exec('ROLLBACK')
    } catch {
    }
    throw error
  }
}

export function listAccountQualityProbeCandidates(input: {
  limit: number
  staleAfterMinutes: number
  minTieGroupSize?: number
  activeAfter?: string
}): AccountQualityRefreshCandidate[] {
  const limit = Math.max(1, Math.min(Math.trunc(input.limit), 200))
  const minTieGroupSize = Math.max(2, Math.trunc(input.minTieGroupSize ?? 2))
  const staleBefore = new Date(Date.now() - Math.max(1, input.staleAfterMinutes) * 60_000).toISOString()
  const activeAfter = typeof input.activeAfter === 'string' && input.activeAfter.trim() ? input.activeAfter.trim() : undefined
  const activeGroupClause = activeAfter
    ? `
          AND EXISTS (
            SELECT 1
            FROM usage_records recent_usage
            WHERE recent_usage.group_id = group_accounts.group_id
              AND recent_usage.api_key_id IS NOT NULL
              AND recent_usage.created_at >= ?
            LIMIT 1
          )
    `
    : ''
  const activeAuthorizationClause = `
          AND (
            group_accounts.account_authorization_id IS NULL
            OR EXISTS (
              SELECT 1
              FROM resource_authorizations active_authorization
              WHERE active_authorization.id = group_accounts.account_authorization_id
                AND active_authorization.resource_type = 'account'
                AND active_authorization.resource_id = accounts.id
                AND active_authorization.grantee_system_account_id = group_accounts.system_account_id
                AND active_authorization.status = 'active'
                AND (active_authorization.expires_at IS NULL OR active_authorization.expires_at > ?)
              LIMIT 1
            )
          )
    `
  const now = nowIso()
  const rows = getDatabase()
    .prepare(`
      WITH tie_groups AS (
        SELECT
          group_accounts.system_account_id,
          group_accounts.group_id,
          accounts.priority,
          CASE WHEN accounts.status = 'active' AND accounts.super_priority_enabled = 1 THEN 1 ELSE 0 END AS super_priority_enabled,
          COUNT(*) AS candidate_count
        FROM group_accounts
        INNER JOIN accounts ON accounts.id = group_accounts.account_id
        WHERE group_accounts.enabled = 1
          AND accounts.provider_code = 'openai'
          AND accounts.type IN ('api_key', 'oauth')
          AND accounts.status = 'active'
          AND accounts.schedulable = 1
          AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
          AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
          ${activeGroupClause}
          ${activeAuthorizationClause}
        GROUP BY group_accounts.system_account_id, group_accounts.group_id, accounts.priority, CASE WHEN accounts.status = 'active' AND accounts.super_priority_enabled = 1 THEN 1 ELSE 0 END
        HAVING COUNT(*) >= ?
      )
      SELECT
        accounts.id AS account_id,
        accounts.system_account_id AS account_owner_system_account_id,
        group_accounts.system_account_id AS probe_system_account_id,
        group_accounts.group_id,
        accounts.provider_code,
        accounts.type,
        accounts.name,
        accounts.status,
        accounts.priority,
        accounts.super_priority_enabled,
        quality.quality_state,
        quality.quality_score,
        quality.last_probe_at,
        quality.last_sample_at,
        quality.updated_at AS quality_updated_at
      FROM tie_groups
      INNER JOIN group_accounts
        ON group_accounts.system_account_id = tie_groups.system_account_id
        AND group_accounts.group_id = tie_groups.group_id
      INNER JOIN accounts
        ON accounts.id = group_accounts.account_id
        AND accounts.priority = tie_groups.priority
        AND CASE WHEN accounts.status = 'active' AND accounts.super_priority_enabled = 1 THEN 1 ELSE 0 END = tie_groups.super_priority_enabled
      LEFT JOIN account_quality_scores quality ON quality.account_id = accounts.id
      WHERE group_accounts.enabled = 1
        AND accounts.provider_code = 'openai'
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
        ${activeAuthorizationClause}
        AND (
          quality.account_id IS NULL
          OR quality.quality_state IN ('unknown', 'stale', 'failed')
          OR quality.updated_at < ?
          OR quality.last_probe_at IS NULL
        )
      ORDER BY
        CASE WHEN quality.account_id IS NULL THEN 0 ELSE 1 END ASC,
        quality.updated_at ASC,
        accounts.priority ASC,
        group_accounts.created_at ASC
      LIMIT ?
    `)
    .all(...[
      now,
      now,
      ...(activeAfter ? [activeAfter] : []),
      now,
      minTieGroupSize,
      now,
      now,
      now,
      staleBefore,
      limit
    ]) as unknown as CandidateRow[]

  const seen = new Set<string>()
  const candidates: AccountQualityRefreshCandidate[] = []
  for (const row of rows) {
    if (seen.has(row.account_id)) {
      continue
    }
    seen.add(row.account_id)
    candidates.push({
      accountId: row.account_id,
      accountOwnerSystemAccountId: row.account_owner_system_account_id,
      probeSystemAccountId: row.probe_system_account_id,
      groupId: row.group_id,
      providerCode: row.provider_code,
      type: row.type,
      name: row.name,
      priority: Number(row.priority ?? 0),
      superPriorityEnabled: row.status === 'active' && row.super_priority_enabled === 1,
      qualityState: row.quality_state ?? undefined,
      qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
      lastProbeAt: row.last_probe_at ?? undefined,
      lastSampleAt: row.last_sample_at ?? undefined,
      updatedAt: row.quality_updated_at ?? undefined
    })
  }
  return candidates
}

export function recordAccountQualityProbe(input: AccountQualityScoreInput): void {
  const sampledAt = input.sampledAt ?? nowIso()
  const previous = accountQualityRow(input.accountId)
  const firstTokenMs = typeof input.firstTokenMs === 'number' && Number.isFinite(input.firstTokenMs)
    ? Math.max(0, Math.trunc(input.firstTokenMs))
    : undefined
  const previousEwma = previous?.ewma_first_token_ms ?? null
  const ewmaFirstTokenMs = input.success && firstTokenMs !== undefined
    ? previousEwma === null
      ? firstTokenMs
      : Math.round(previousEwma * 0.5 + firstTokenMs * 0.5)
    : previousEwma
  const previousRequests = previous?.recent_request_count ?? 0
  const previousSuccesses = previous?.recent_success_count ?? 0
  const previousErrors = previous?.recent_error_count ?? 0
  const recentRequestCount = previousRequests + 1
  const recentSuccessCount = previousSuccesses + (input.success ? 1 : 0)
  const recentErrorCount = previousErrors + (input.success ? 0 : 1)
  const successRate = recentRequestCount > 0 ? recentSuccessCount / recentRequestCount : previous?.success_rate ?? null
  const qualityState: AccountQualityState = input.success && firstTokenMs !== undefined ? 'fresh' : input.success ? 'unknown' : 'failed'
  const qualityScore = computeQualityScore({
    ewmaFirstTokenMs,
    successRate,
    qualityState,
    updatedAt: sampledAt
  })
  upsertAccountQuality({
    accountId: input.accountId,
    systemAccountId: input.systemAccountId,
    providerCode: input.providerCode,
    qualityScore,
    qualityState,
    recentRequestCount,
    recentSuccessCount,
    recentErrorCount,
    recentFirstTokenSampleCount: (previous?.recent_first_token_sample_count ?? 0) + (firstTokenMs !== undefined ? 1 : 0),
    recentAvgFirstTokenMs: firstTokenMs ?? previous?.recent_avg_first_token_ms ?? undefined,
    ewmaFirstTokenMs,
    successRate,
    windowStartedAt: previous?.window_started_at ?? sampledAt,
    windowEndedAt: sampledAt,
    lastSampleAt: sampledAt,
    lastProbeAt: input.source === 'probe' ? sampledAt : previous?.last_probe_at ?? undefined,
    lastSuccessAt: input.success ? sampledAt : previous?.last_success_at ?? undefined,
    lastErrorAt: input.success ? previous?.last_error_at ?? undefined : sampledAt,
    lastErrorMessage: input.success ? previous?.last_error_message ?? undefined : input.errorMessage,
    updatedAt: sampledAt
  })
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
  const lastProbeTime = previous.last_probe_at ? Date.parse(previous.last_probe_at) : NaN
  const windowStartTime = Date.parse(windowStartedAt)
  const hasRecentProbe = Number.isFinite(lastProbeTime) && Number.isFinite(windowStartTime) && lastProbeTime >= windowStartTime
  const qualityState: AccountQualityState = hasRecentProbe && previous.quality_state === 'fresh' ? 'fresh' : 'stale'
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
    lastProbeAt: previous.last_probe_at ?? undefined,
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
  lastProbeAt?: string
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
        window_started_at, window_ended_at, last_sample_at, last_probe_at, last_success_at, last_error_at, last_error_message, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        last_probe_at = excluded.last_probe_at,
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
      input.lastProbeAt ?? null,
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
