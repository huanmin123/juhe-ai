import type { ModelCheckLevel, ModelCheckProfile } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { getStatsDatabase, nowIso } from './database.js'
import { hourKey, usageStatsTimezoneAsync } from './usage-stats-helpers.js'

export interface ModelQualityHealthFailureInput {
  accountId: string
  systemAccountId: string
  providerCode: string
  observedAt: string
  runId: string
  model: string
  profile: ModelCheckProfile
  score: number
  threshold: number
  level: ModelCheckLevel
  errorCode?: string
  errorMessage?: string
}

export interface ModelQualityHealthFailureResult {
  applied: boolean
  statHour: string
}

export async function recordModelQualityHealthFailureAsync(input: ModelQualityHealthFailureInput): Promise<ModelQualityHealthFailureResult> {
  const timezone = await usageStatsTimezoneAsync()
  const observedAt = normalizedIso(input.observedAt)
  const statHour = hourKey(new Date(observedAt), timezone)
  const client = await statsClient()
  const target = client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_stats', 'account_quality_health_hourly')
    : client.dialect.quoteIdentifier('account_quality_health_hourly')
  const result = await client.execute(`
    INSERT INTO ${target} (
      account_id, system_account_id, provider_code, stat_hour, observed_at, model_check_run_id,
      model, profile, score, threshold, level, error_code, error_message, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id, stat_hour) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      provider_code = excluded.provider_code,
      observed_at = excluded.observed_at,
      model_check_run_id = excluded.model_check_run_id,
      model = excluded.model,
      profile = excluded.profile,
      score = excluded.score,
      threshold = excluded.threshold,
      level = excluded.level,
      error_code = excluded.error_code,
      error_message = excluded.error_message,
      updated_at = excluded.updated_at
    WHERE excluded.observed_at > account_quality_health_hourly.observed_at
       OR (excluded.observed_at = account_quality_health_hourly.observed_at
           AND excluded.model_check_run_id > account_quality_health_hourly.model_check_run_id)
  `, [
    input.accountId,
    input.systemAccountId,
    input.providerCode,
    statHour,
    observedAt,
    input.runId,
    input.model,
    input.profile,
    Math.max(0, Math.trunc(input.score)),
    Math.max(40, Math.min(100, Math.trunc(input.threshold))),
    input.level,
    input.errorCode ?? null,
    input.errorMessage?.slice(0, 1000) ?? null,
    nowIso()
  ])
  return { applied: result.changes > 0, statHour }
}

async function statsClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getStatsDatabase())
}

function normalizedIso(value: string): string {
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : nowIso()
}
