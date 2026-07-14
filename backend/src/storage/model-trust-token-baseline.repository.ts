import type { DatabaseClient } from './database-client.js'

export type TokenInterceptScope = {
  cohortKeyHmac: string
  requestedModel: string
  tokenizerVersion: string
  probeSetVersion: string
}

export type TokenInterceptAccountKey = {
  systemAccountId: string
  accountId: string
  requestedModel: string
}

export type TokenInterceptBaselineEvaluation = {
  baselineVersion?: number
  baselineStatus: 'unavailable' | 'calibration_pending' | 'active'
  evidenceStatus: string
  looMedian?: number
  looMad?: number
  strongGateEnabled: boolean
  suspectedFixedPadding: boolean
}

type SourceInterceptRow = {
  upstream_bucket_hmac: string
  intercept: number
  slope: number
  round_count: number
  valid_sample_count: number
  first_observed_at: string
  last_observed_at: string
}

type BaselineRow = {
  baseline_version: number
  version_status: string
  evidence_status: string
  independent_source_count: number
  q90_intercept: number | null
  strong_threshold_intercept: number | null
  strong_gate_enabled: number
}

const maximumBaselineSources = 1_000

export async function refreshTokenInterceptBaselines(
  client: DatabaseClient,
  scopes: TokenInterceptScope[]
): Promise<void> {
  for (const scope of uniqueScopes(scopes)) {
    const sources = await listCollapsedSourceIntercepts(client, scope)
    if (!sources.length) continue
    const active = await findBaseline(client, scope, 'active')
    const pending = await findBaseline(client, scope, 'calibration_pending')
    const version = pending?.baseline_version ?? ((active?.baseline_version ?? 0) + 1)
    const values = sources.map((source) => source.intercept).sort((left, right) => left - right)
    const median = percentile(values, 0.5)
    const deviations = values.map((value) => Math.abs(value - median)).sort((left, right) => left - right)
    const firstObservedAt = sources.map((source) => source.first_observed_at).sort()[0] as string
    const lastObservedAt = sources.map((source) => source.last_observed_at).sort().at(-1) as string
    const evidenceStatus = tokenInterceptEvidenceStatus(sources.length, firstObservedAt, lastObservedAt)
    const table = client.dialect.qualifyTable('juhe_stats', 'model_token_intercept_baseline_versions')
    await client.execute(`
      INSERT INTO ${table} (
        cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, baseline_version,
        version_status, evidence_status, independent_source_count, retained_source_count, excluded_source_count,
        median_intercept, mad_intercept, q10_intercept, q90_intercept,
        strong_threshold_intercept, strong_gate_enabled, calibration_note,
        first_observed_at, last_observed_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'calibration_pending', ?, ?, ?, 0, ?, ?, ?, ?, NULL, 0, NULL, ?, ?, ?)
      ON CONFLICT (cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, baseline_version) DO UPDATE SET
        evidence_status = excluded.evidence_status,
        independent_source_count = excluded.independent_source_count,
        retained_source_count = excluded.retained_source_count,
        excluded_source_count = excluded.excluded_source_count,
        median_intercept = excluded.median_intercept,
        mad_intercept = excluded.mad_intercept,
        q10_intercept = excluded.q10_intercept,
        q90_intercept = excluded.q90_intercept,
        first_observed_at = excluded.first_observed_at,
        last_observed_at = excluded.last_observed_at,
        updated_at = excluded.updated_at
      WHERE model_token_intercept_baseline_versions.version_status = 'calibration_pending'
    `, [
      scope.cohortKeyHmac, scope.requestedModel, scope.tokenizerVersion, scope.probeSetVersion, version,
      evidenceStatus, sources.length, sources.length, median, percentile(deviations, 0.5),
      percentile(values, 0.1), percentile(values, 0.9), firstObservedAt, lastObservedAt, new Date().toISOString()
    ])
  }
}

export async function activateTokenInterceptBaselineVersion(client: DatabaseClient, input: TokenInterceptScope & {
  baselineVersion: number
  strongThresholdIntercept: number
  calibrationNote: string
}): Promise<void> {
  if (!Number.isFinite(input.strongThresholdIntercept) || input.strongThresholdIntercept < 0) {
    throw new Error('固定截距校准阈值必须是非负有限数值')
  }
  const note = input.calibrationNote.trim()
  if (!note || note.length > 500) throw new Error('固定截距校准记录必须为 1 到 500 个字符')
  const table = client.dialect.qualifyTable('juhe_stats', 'model_token_intercept_baseline_versions')
  const candidate = await client.one<BaselineRow>(`
    SELECT baseline_version, version_status, evidence_status, independent_source_count, q90_intercept,
      strong_threshold_intercept, strong_gate_enabled
    FROM ${table}
    WHERE cohort_key_hmac = ? AND requested_model = ? AND tokenizer_version = ?
      AND probe_set_version = ? AND baseline_version = ?
    LIMIT 1
  `, [input.cohortKeyHmac, input.requestedModel, input.tokenizerVersion, input.probeSetVersion, input.baselineVersion])
  if (!candidate || candidate.version_status !== 'calibration_pending') throw new Error('固定截距待校准基线版本不存在')
  if (candidate.evidence_status !== 'stable' || Number(candidate.independent_source_count) < 10) {
    throw new Error('固定截距基线尚未达到稳定独立来源门槛')
  }
  const q90 = optionalNumber(candidate.q90_intercept)
  if (q90 === undefined || input.strongThresholdIntercept < q90) {
    throw new Error('固定截距校准阈值不能低于当前 cohort 的 q90')
  }
  await client.execute(`
    UPDATE ${table}
    SET version_status = 'retired', strong_gate_enabled = 0, updated_at = ?
    WHERE cohort_key_hmac = ? AND requested_model = ? AND tokenizer_version = ?
      AND probe_set_version = ? AND version_status = 'active'
  `, [new Date().toISOString(), input.cohortKeyHmac, input.requestedModel, input.tokenizerVersion, input.probeSetVersion])
  await client.execute(`
    UPDATE ${table}
    SET version_status = 'active', strong_threshold_intercept = ?, strong_gate_enabled = 1,
      calibration_note = ?, updated_at = ?
    WHERE cohort_key_hmac = ? AND requested_model = ? AND tokenizer_version = ?
      AND probe_set_version = ? AND baseline_version = ? AND version_status = 'calibration_pending'
  `, [
    input.strongThresholdIntercept, note, new Date().toISOString(), input.cohortKeyHmac,
    input.requestedModel, input.tokenizerVersion, input.probeSetVersion, input.baselineVersion
  ])
}

export async function evaluateTokenInterceptBaseline(
  client: DatabaseClient,
  key: TokenInterceptAccountKey,
  scope: TokenInterceptScope,
  accountIntercept: number
): Promise<TokenInterceptBaselineEvaluation> {
  const active = await findBaseline(client, scope, 'active')
  const pending = active ? undefined : await findBaseline(client, scope, 'calibration_pending')
  const baseline = active ?? pending
  if (!baseline) return unavailableEvaluation()
  const candidateBuckets = await listCandidateBuckets(client, key, scope.cohortKeyHmac)
  const sources = (await listCollapsedSourceIntercepts(client, scope))
    .filter((source) => !candidateBuckets.has(source.upstream_bucket_hmac))
  const values = sources.map((source) => source.intercept).sort((left, right) => left - right)
  const median = values.length ? percentile(values, 0.5) : undefined
  const deviations = median === undefined ? [] : values.map((value) => Math.abs(value - median)).sort((left, right) => left - right)
  const threshold = optionalNumber(active?.strong_threshold_intercept)
  const gateEnabled = active?.strong_gate_enabled === 1
    && active.evidence_status === 'stable'
    && threshold !== undefined
    && values.length >= 9
  return {
    baselineVersion: Number(baseline.baseline_version),
    baselineStatus: active ? 'active' : 'calibration_pending',
    evidenceStatus: baseline.evidence_status,
    looMedian: median,
    looMad: deviations.length ? percentile(deviations, 0.5) : undefined,
    strongGateEnabled: gateEnabled,
    suspectedFixedPadding: gateEnabled && accountIntercept > (threshold as number)
  }
}

async function findBaseline(client: DatabaseClient, scope: TokenInterceptScope, status: string): Promise<BaselineRow | undefined> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_token_intercept_baseline_versions')
  return await client.one<BaselineRow>(`
    SELECT baseline_version, version_status, evidence_status, independent_source_count, q90_intercept,
      strong_threshold_intercept, strong_gate_enabled
    FROM ${table}
    WHERE cohort_key_hmac = ? AND requested_model = ? AND tokenizer_version = ?
      AND probe_set_version = ? AND version_status = ?
    ORDER BY baseline_version DESC LIMIT 1
  `, [scope.cohortKeyHmac, scope.requestedModel, scope.tokenizerVersion, scope.probeSetVersion, status])
}

async function listCollapsedSourceIntercepts(client: DatabaseClient, scope: TokenInterceptScope): Promise<SourceInterceptRow[]> {
  const windows = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_windows')
  const sources = client.dialect.qualifyTable('juhe_stats', 'model_trust_window_sources')
  const rows = await client.query<SourceInterceptRow>(`
    SELECT s.upstream_bucket_hmac, w.intercept, w.slope, w.round_count, w.valid_sample_count,
      w.first_observed_at, w.last_observed_at
    FROM ${windows} w
    INNER JOIN ${sources} s
      ON s.system_account_id = w.system_account_id
      AND s.account_id = w.account_id
      AND s.cohort_key_hmac = w.cohort_key_hmac
      AND s.mapped_upstream_model = w.requested_model
    WHERE w.cohort_key_hmac = ? AND w.requested_model = ? AND w.tokenizer_version = ?
      AND w.probe_set_version = ? AND w.intercept IS NOT NULL AND w.slope BETWEEN 0.97 AND 1.03
      AND w.round_count >= 3 AND w.valid_sample_count >= 6
    ORDER BY s.upstream_bucket_hmac, w.last_observed_at DESC
    LIMIT ${maximumBaselineSources * 20}
  `, [scope.cohortKeyHmac, scope.requestedModel, scope.tokenizerVersion, scope.probeSetVersion])
  const byBucket = new Map<string, SourceInterceptRow[]>()
  for (const row of rows) {
    const bucketRows = byBucket.get(row.upstream_bucket_hmac) ?? []
    bucketRows.push(row)
    byBucket.set(row.upstream_bucket_hmac, bucketRows)
  }
  return [...byBucket.entries()].slice(0, maximumBaselineSources).map(([bucket, bucketRows]) => {
    const intercepts = bucketRows.map((row) => Number(row.intercept)).filter(Number.isFinite).sort((left, right) => left - right)
    const representative = bucketRows[0] as SourceInterceptRow
    return {
      ...representative,
      upstream_bucket_hmac: bucket,
      intercept: percentile(intercepts, 0.5),
      first_observed_at: bucketRows.map((row) => row.first_observed_at).sort()[0] as string,
      last_observed_at: bucketRows.map((row) => row.last_observed_at).sort().at(-1) as string
    }
  })
}

async function listCandidateBuckets(
  client: DatabaseClient,
  key: TokenInterceptAccountKey,
  cohortKeyHmac: string
): Promise<Set<string>> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_trust_window_sources')
  const rows = await client.query<{ upstream_bucket_hmac: string }>(`
    SELECT DISTINCT upstream_bucket_hmac FROM ${table}
    WHERE system_account_id = ? AND account_id = ? AND cohort_key_hmac = ? AND mapped_upstream_model = ?
    LIMIT 20
  `, [key.systemAccountId, key.accountId, cohortKeyHmac, key.requestedModel])
  return new Set(rows.map((row) => row.upstream_bucket_hmac))
}

function tokenInterceptEvidenceStatus(sourceCount: number, firstObservedAt: string, lastObservedAt: string): string {
  const days = Math.max(1, Math.ceil((Date.parse(lastObservedAt) - Date.parse(firstObservedAt)) / 86_400_000) + 1)
  if (sourceCount >= 10 && days >= 14) return 'stable'
  if (sourceCount >= 5 && days >= 7) return 'candidate'
  if (sourceCount >= 3 && days >= 3) return 'bootstrap'
  return 'insufficient'
}

function uniqueScopes(scopes: TokenInterceptScope[]): TokenInterceptScope[] {
  const unique = new Map<string, TokenInterceptScope>()
  for (const scope of scopes) {
    unique.set(`${scope.cohortKeyHmac}\u0000${scope.requestedModel}\u0000${scope.tokenizerVersion}\u0000${scope.probeSetVersion}`, scope)
  }
  return [...unique.values()]
}

function percentile(values: number[], ratio: number): number {
  if (!values.length) return 0
  const index = Math.max(0, Math.min(values.length - 1, Math.round((values.length - 1) * ratio)))
  return values[index] as number
}

function optionalNumber(value: unknown): number | undefined {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : undefined
}

function unavailableEvaluation(): TokenInterceptBaselineEvaluation {
  return {
    baselineStatus: 'unavailable',
    evidenceStatus: 'insufficient',
    strongGateEnabled: false,
    suspectedFixedPadding: false
  }
}
