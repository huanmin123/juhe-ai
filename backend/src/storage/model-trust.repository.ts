import { runtimeConfig } from '../config/runtime.js'
import { getDatasetDatabase, getStatsDatabase, newId, nowIso } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

const aggregationJobName = 'model-trust-observation-aggregation'

export interface ModelCheckObservationInput {
  id?: string
  runId: string
  systemAccountId: string
  accountId: string
  providerCode: string
  providerProtocolProfileId: string
  endpointFamily: string
  requestedModel: string
  mappedUpstreamModel: string
  observedModel?: string
  mappingApplied: boolean
  upstreamBucketHmac: string
  cohortKeyHmac: string
  probeKeyHmac: string
  systemFingerprintHmac?: string
  probeFamily: string
  probeSetVersion: string
  tokenizerVersion: string
  roundIndex: number
  paddingTokens: number
  localInputTokens: number
  reportedInputTokens?: number
  cachedInputTokens?: number
  observationStatus: string
  identityStatus: string
  mappingStatus: string
  protocolStatus: string
  evidenceCoverage: number
  traceId?: string
  createdAt?: string
}

export interface ModelAccountTrustResult {
  identityStatus: string
  mappingStatus: string
  usageIntegrityStatus: string
  protocolStatus: string
  evidenceStatus: string
  evidenceCoverage: number
  observationCount: number
  roundCount: number
  independentSourceCount: number
  slope?: number
  intercept?: number
  tokenizerVersion?: string
  probeSetVersion?: string
  reasonCodes: string[]
  lastObservedAt?: string
}

interface ObservationRow {
  id: string
  run_id: string
  system_account_id: string
  account_id: string
  provider_code: string
  provider_protocol_profile_id: string
  endpoint_family: string
  requested_model: string
  mapped_upstream_model: string
  observed_model: string | null
  mapping_applied: number
  upstream_bucket_hmac: string
  cohort_key_hmac: string
  probe_key_hmac: string
  system_fingerprint_hmac: string | null
  probe_family: string
  probe_set_version: string
  tokenizer_version: string
  round_index: number
  padding_tokens: number
  local_input_tokens: number
  reported_input_tokens: number | null
  cached_input_tokens: number | null
  observation_status: string
  identity_status: string
  mapping_status: string
  protocol_status: string
  evidence_coverage: number
  trace_id: string | null
  created_at: string
}

interface WindowRow {
  observation_count: number
  valid_sample_count: number
  sum_local: number
  sum_reported: number
  sum_local_squared: number
  sum_local_reported: number
  sum_reported_squared: number
  bucket_aligned_count: number
  first_observed_at: string
  last_observed_at: string
}

export async function createModelCheckObservationsAsync(inputs: ModelCheckObservationInput[]): Promise<number> {
  if (!inputs.length) return 0
  const client = await datasetClient()
  const table = client.dialect.qualifyTable('juhe_dataset', 'model_check_observations')
  await client.transaction(async (tx) => {
    for (const input of inputs) {
      const row = normalizedObservation(input)
      await tx.execute(`
        INSERT INTO ${table} (
          id, run_id, system_account_id, account_id, provider_code, provider_protocol_profile_id,
          endpoint_family, requested_model, mapped_upstream_model, observed_model, mapping_applied,
          upstream_bucket_hmac, cohort_key_hmac, probe_key_hmac, system_fingerprint_hmac, probe_family, probe_set_version,
          tokenizer_version, round_index, padding_tokens, local_input_tokens, reported_input_tokens,
          cached_input_tokens, observation_status, identity_status, mapping_status, protocol_status,
          evidence_coverage, trace_id, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `, Object.values(row))
    }
  })
  return inputs.length
}

export async function aggregateModelTrustObservationsAsync(limit = 500): Promise<number> {
  const dataset = await datasetClient()
  const stats = await statsClient()
  const observationsTable = dataset.dialect.qualifyTable('juhe_dataset', 'model_check_observations')
  const state = await readAggregationState(stats)
  const rows = await dataset.query<ObservationRow>(`
    SELECT * FROM ${observationsTable}
    WHERE (created_at > ? OR (created_at = ? AND id > ?))
    ORDER BY created_at, id
    LIMIT ?
  `, [state.createdAt, state.createdAt, state.id, boundedLimit(limit)])
  if (!rows.length) return 0
  await stats.transaction(async (tx) => {
    for (const row of rows) {
      await upsertSource(tx, row)
      await upsertWindow(tx, row)
    }
    const affected = uniqueAccountModels(rows)
    for (const key of affected) {
      await refreshLatestResult(tx, key, rows)
    }
    const last = rows[rows.length - 1] as ObservationRow
    await writeAggregationState(tx, last.created_at, last.id)
  })
  return rows.length
}

export async function findModelAccountTrustResultAsync(systemAccountId: string, accountId: string, requestedModel: string): Promise<ModelAccountTrustResult | undefined> {
  const client = await statsClient()
  const table = client.dialect.qualifyTable('juhe_stats', 'model_account_trust_results')
  const row = await client.one<Record<string, unknown>>(`
    SELECT * FROM ${table}
    WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
    LIMIT 1
  `, [systemAccountId, accountId, requestedModel])
  if (!row) return undefined
  return {
    identityStatus: String(row.identity_status),
    mappingStatus: String(row.mapping_status),
    usageIntegrityStatus: String(row.usage_integrity_status),
    protocolStatus: String(row.protocol_status),
    evidenceStatus: String(row.evidence_status),
    evidenceCoverage: Number(row.evidence_coverage ?? 0),
    observationCount: Number(row.observation_count ?? 0),
    roundCount: Number(row.round_count ?? 0),
    independentSourceCount: Number(row.independent_source_count ?? 0),
    slope: optionalNumber(row.slope),
    intercept: optionalNumber(row.intercept),
    tokenizerVersion: optionalText(row.tokenizer_version),
    probeSetVersion: optionalText(row.probe_set_version),
    reasonCodes: parseReasonCodes(row.reason_codes_json),
    lastObservedAt: optionalText(row.last_observed_at)
  }
}

async function upsertSource(client: DatabaseClient, row: ObservationRow): Promise<void> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_trust_window_sources')
  await client.execute(`
    INSERT INTO ${table} (
      system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac, first_observed_at,
      last_observed_at, observation_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)
    ON CONFLICT (system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac) DO UPDATE SET
      last_observed_at = excluded.last_observed_at,
      observation_count = observation_count + 1,
      updated_at = excluded.updated_at
  `, [row.system_account_id, row.account_id, row.cohort_key_hmac, row.mapped_upstream_model, row.upstream_bucket_hmac, row.created_at, row.created_at, nowIso()])
}

async function upsertWindow(client: DatabaseClient, row: ObservationRow): Promise<void> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_windows')
  const valid = row.reported_input_tokens !== null
  const local = valid ? row.local_input_tokens : 0
  const reported = valid ? Number(row.reported_input_tokens) : 0
  await client.execute(`
    INSERT INTO ${table} (
      system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version,
      probe_set_version, observation_count, valid_sample_count, round_count, sum_local,
      sum_reported, sum_local_squared, sum_local_reported, sum_reported_squared,
      bucket_aligned_count, first_observed_at, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 1, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version, probe_set_version) DO UPDATE SET
      observation_count = observation_count + 1,
      valid_sample_count = valid_sample_count + excluded.valid_sample_count,
      sum_local = sum_local + excluded.sum_local,
      sum_reported = sum_reported + excluded.sum_reported,
      sum_local_squared = sum_local_squared + excluded.sum_local_squared,
      sum_local_reported = sum_local_reported + excluded.sum_local_reported,
      sum_reported_squared = sum_reported_squared + excluded.sum_reported_squared,
      bucket_aligned_count = bucket_aligned_count + excluded.bucket_aligned_count,
      last_observed_at = excluded.last_observed_at,
      updated_at = excluded.updated_at
  `, [
    row.system_account_id, row.account_id, row.requested_model, row.cohort_key_hmac,
    row.tokenizer_version, row.probe_set_version, valid ? 1 : 0, local, reported,
    local * local, local * reported, reported * reported,
    valid && row.padding_tokens > 0 && reported % 64 === 0 ? 1 : 0,
    row.created_at, row.created_at, nowIso()
  ])
}

async function refreshLatestResult(client: DatabaseClient, key: AccountModelKey, batchRows: ObservationRow[]): Promise<void> {
  const windows = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_windows')
  const sources = client.dialect.qualifyTable('juhe_stats', 'model_trust_window_sources')
  const latest = client.dialect.qualifyTable('juhe_stats', 'model_account_trust_results')
  const window = await client.one<WindowRow & { cohort_key_hmac: string; tokenizer_version: string; probe_set_version: string }>(`
    SELECT * FROM ${windows}
    WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
    ORDER BY last_observed_at DESC LIMIT 1
  `, [key.systemAccountId, key.accountId, key.requestedModel])
  if (!window) return
  const sourceRow = await client.one<{ source_count: number }>(`
    SELECT COUNT(DISTINCT upstream_bucket_hmac) AS source_count FROM ${sources}
    WHERE cohort_key_hmac = ?
  `, [window.cohort_key_hmac])
  const sourceCount = Number(sourceRow?.source_count ?? 0)
  const regression = regressionFromWindow(window)
  const roundCount = Math.floor(Number(window.observation_count) / 3)
  const durationDays = Math.max(1, Math.ceil((Date.parse(window.last_observed_at) - Date.parse(window.first_observed_at)) / 86_400_000) + 1)
  const evidenceStatus = sourceCount >= 10 && window.observation_count >= 300 && durationDays >= 14
    ? 'stable'
    : sourceCount >= 5 && window.observation_count >= 100 && durationDays >= 7
      ? 'candidate'
      : sourceCount >= 3 && window.observation_count >= 30 && durationDays >= 3
        ? 'bootstrap'
        : 'insufficient'
  const usage = tokenStatusFromWindow(window, regression.slope, regression.confidenceLow, regression.confidenceHigh, roundCount)
  await client.execute(`
    UPDATE ${windows}
    SET round_count = ?, slope = ?, intercept = ?, usage_integrity_status = ?, updated_at = ?
    WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
      AND cohort_key_hmac = ? AND tokenizer_version = ? AND probe_set_version = ?
  `, [
    roundCount, regression.slope, regression.intercept, usage.status, nowIso(),
    key.systemAccountId, key.accountId, key.requestedModel, window.cohort_key_hmac,
    window.tokenizer_version, window.probe_set_version
  ])
  const representative = [...batchRows].reverse().find((row) => row.system_account_id === key.systemAccountId && row.account_id === key.accountId && row.requested_model === key.requestedModel)
  const reasonCodes = [
    ...usage.reasonCodes,
    ...(representative?.mapping_status === 'configured_mapping' ? ['configured_model_mapping'] : []),
    ...(representative?.mapping_status === 'undeclared_mismatch' ? ['undeclared_response_model_mismatch'] : []),
    ...(representative?.protocol_status === 'failed' ? ['protocol_check_failed'] : [])
  ]
  const evidenceCoverage = Math.min(100, Math.round((Math.min(roundCount, 3) / 3) * 50 + (Math.min(sourceCount, 3) / 3) * 50))
  await client.execute(`
    INSERT INTO ${latest} (
      system_account_id, account_id, requested_model, identity_status, mapping_status,
      usage_integrity_status, protocol_status, evidence_status, evidence_coverage,
      observation_count, round_count, independent_source_count, slope, intercept,
      tokenizer_version, probe_set_version, reason_codes_json, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (system_account_id, account_id, requested_model) DO UPDATE SET
      identity_status = excluded.identity_status,
      mapping_status = excluded.mapping_status,
      usage_integrity_status = excluded.usage_integrity_status,
      protocol_status = excluded.protocol_status,
      evidence_status = excluded.evidence_status,
      evidence_coverage = excluded.evidence_coverage,
      observation_count = excluded.observation_count,
      round_count = excluded.round_count,
      independent_source_count = excluded.independent_source_count,
      slope = excluded.slope,
      intercept = excluded.intercept,
      tokenizer_version = excluded.tokenizer_version,
      probe_set_version = excluded.probe_set_version,
      reason_codes_json = excluded.reason_codes_json,
      last_observed_at = excluded.last_observed_at,
      updated_at = excluded.updated_at
  `, [
    key.systemAccountId, key.accountId, key.requestedModel,
    representative?.identity_status ?? 'insufficient_evidence', representative?.mapping_status ?? 'unknown',
    usage.status, representative?.protocol_status ?? 'insufficient_evidence', evidenceStatus, evidenceCoverage,
    window.observation_count, roundCount, sourceCount, regression.slope, regression.intercept,
    window.tokenizer_version, window.probe_set_version, JSON.stringify(reasonCodes), window.last_observed_at, nowIso()
  ])
}

function regressionFromWindow(row: WindowRow): { slope: number; intercept: number; confidenceLow: number; confidenceHigh: number } {
  const n = Number(row.valid_sample_count)
  if (n < 2) return { slope: 0, intercept: 0, confidenceLow: 0, confidenceHigh: 0 }
  const denominator = (n * row.sum_local_squared) - (row.sum_local ** 2)
  if (denominator === 0) return { slope: 0, intercept: 0, confidenceLow: 0, confidenceHigh: 0 }
  const slope = ((n * row.sum_local_reported) - (row.sum_local * row.sum_reported)) / denominator
  const intercept = (row.sum_reported - slope * row.sum_local) / n
  const sse = Math.max(0, row.sum_reported_squared - (intercept * row.sum_reported) - (slope * row.sum_local_reported))
  const ssX = row.sum_local_squared - ((row.sum_local ** 2) / n)
  const standardError = n > 2 && ssX > 0 ? Math.sqrt((sse / (n - 2)) / ssX) : Number.POSITIVE_INFINITY
  return { slope, intercept, confidenceLow: slope - 1.96 * standardError, confidenceHigh: slope + 1.96 * standardError }
}

function tokenStatusFromWindow(row: WindowRow, slope: number, low: number, high: number, roundCount: number): { status: string; reasonCodes: string[] } {
  if (row.valid_sample_count < 6 || roundCount < 3 || slope <= 0.1) {
    return { status: 'unsupported', reasonCodes: [row.valid_sample_count < 6 ? 'reported_usage_missing' : 'reported_usage_incompatible'] }
  }
  const distance = Math.abs(slope - 1)
  if (distance > 0.05 && (low > 1 || high < 1)) return { status: 'suspected_padding', reasonCodes: ['proportional_padding'] }
  if (distance > 0.03) return { status: 'warning', reasonCodes: ['slope_warning'] }
  if (row.bucket_aligned_count / row.valid_sample_count >= 0.5) return { status: 'warning', reasonCodes: ['bucket_rounding'] }
  return { status: 'consistent', reasonCodes: [] }
}

async function readAggregationState(client: DatabaseClient): Promise<{ createdAt: string; id: string }> {
  const table = client.dialect.qualifyTable('juhe_stats', 'stats_job_state')
  const row = await client.one<{ cursor_created_at?: string | null; cursor_id?: string | null }>(`
    SELECT cursor_created_at, cursor_id FROM ${table}
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `, [aggregationJobName])
  return { createdAt: row?.cursor_created_at ?? '', id: row?.cursor_id ?? '' }
}

async function writeAggregationState(client: DatabaseClient, createdAt: string, id: string): Promise<void> {
  const table = client.dialect.qualifyTable('juhe_stats', 'stats_job_state')
  const now = nowIso()
  await client.execute(`
    INSERT INTO ${table} (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at)
    VALUES ('global', '', ?, ?, ?, ?, ?)
    ON CONFLICT (scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      updated_at = excluded.updated_at
  `, [aggregationJobName, createdAt, id, now, now])
}

async function datasetClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getDatasetDatabase())
}

async function statsClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getStatsDatabase())
}

function normalizedObservation(input: ModelCheckObservationInput): Record<string, string | number | null> {
  return {
    id: input.id ?? newId('mco'), run_id: boundedText(input.runId), system_account_id: boundedText(input.systemAccountId),
    account_id: boundedText(input.accountId), provider_code: boundedText(input.providerCode), provider_protocol_profile_id: boundedText(input.providerProtocolProfileId),
    endpoint_family: boundedText(input.endpointFamily), requested_model: boundedText(input.requestedModel), mapped_upstream_model: boundedText(input.mappedUpstreamModel),
    observed_model: optionalBoundedText(input.observedModel), mapping_applied: input.mappingApplied ? 1 : 0,
    upstream_bucket_hmac: boundedHmac(input.upstreamBucketHmac), cohort_key_hmac: boundedHmac(input.cohortKeyHmac), probe_key_hmac: boundedHmac(input.probeKeyHmac),
    system_fingerprint_hmac: input.systemFingerprintHmac ? boundedHmac(input.systemFingerprintHmac) : null,
    probe_family: boundedText(input.probeFamily), probe_set_version: boundedText(input.probeSetVersion), tokenizer_version: boundedText(input.tokenizerVersion),
    round_index: nonNegativeInteger(input.roundIndex), padding_tokens: nonNegativeInteger(input.paddingTokens), local_input_tokens: nonNegativeInteger(input.localInputTokens),
    reported_input_tokens: optionalNonNegativeInteger(input.reportedInputTokens), cached_input_tokens: optionalNonNegativeInteger(input.cachedInputTokens),
    observation_status: boundedText(input.observationStatus), identity_status: boundedText(input.identityStatus), mapping_status: boundedText(input.mappingStatus),
    protocol_status: boundedText(input.protocolStatus), evidence_coverage: Math.min(100, nonNegativeInteger(input.evidenceCoverage)), trace_id: optionalBoundedText(input.traceId),
    created_at: input.createdAt ?? nowIso()
  }
}

type AccountModelKey = { systemAccountId: string; accountId: string; requestedModel: string }

function uniqueAccountModels(rows: ObservationRow[]): AccountModelKey[] {
  const map = new Map<string, AccountModelKey>()
  for (const row of rows) {
    const key = { systemAccountId: row.system_account_id, accountId: row.account_id, requestedModel: row.requested_model }
    map.set(`${key.systemAccountId}\u0000${key.accountId}\u0000${key.requestedModel}`, key)
  }
  return [...map.values()]
}

function boundedLimit(value: number): number { return Math.max(1, Math.min(5000, Math.trunc(value) || 500)) }
function boundedText(value: string): string { return value.trim().slice(0, 200) }
function optionalBoundedText(value?: string): string | null { return value?.trim() ? boundedText(value) : null }
function boundedHmac(value: string): string {
  const normalized = value.trim()
  if (!/^hmac-sha256-v1:[a-f0-9]{64}$/.test(normalized)) {
    throw new Error('模型可信 observation HMAC 格式无效')
  }
  return normalized
}
function nonNegativeInteger(value: number): number { return Math.max(0, Math.trunc(Number.isFinite(value) ? value : 0)) }
function optionalNonNegativeInteger(value?: number): number | null { return value === undefined ? null : nonNegativeInteger(value) }
function optionalNumber(value: unknown): number | undefined { const parsed = Number(value); return Number.isFinite(parsed) ? parsed : undefined }
function optionalText(value: unknown): string | undefined { return typeof value === 'string' && value.trim() ? value : undefined }
function parseReasonCodes(value: unknown): string[] { try { const parsed = JSON.parse(String(value ?? '[]')); return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string').slice(0, 20) : [] } catch { return [] } }
