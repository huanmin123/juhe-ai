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
  system_account_id: string
  account_id: string
  upstream_bucket_hmac: string
  intercept: number
  slope: number
  round_count: number
  valid_sample_count: number
  first_observed_at: string
  last_observed_at: string
}

type CollapsedSourceIntercept = SourceInterceptRow & {
  source_valid_sample_count: number
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

type SourceSnapshot = {
  sources: CollapsedSourceIntercept[]
  bucketsByAccount: Map<string, Set<string>>
  accountKeys: TokenInterceptAccountKey[]
}

export type TokenInterceptEvaluationContext = SourceSnapshot & {
  scope: TokenInterceptScope
  baseline?: BaselineRow
}

const maximumBaselineSources = 1_000
const maximumBaselineRows = maximumBaselineSources * 20
const maximumActivationAccounts = 5_000

export async function refreshTokenInterceptBaselines(
  client: DatabaseClient,
  scopes: TokenInterceptScope[]
): Promise<Map<string, TokenInterceptEvaluationContext>> {
  const contexts = new Map<string, TokenInterceptEvaluationContext>()
  for (const scope of uniqueScopes(scopes)) {
    const snapshot = await loadSourceInterceptSnapshot(client, scope)
    const sources = snapshot.sources
    if (sources.length) {
      const active = await findBaseline(client, scope, 'active')
      const pending = await findBaseline(client, scope, 'calibration_pending')
      const version = pending?.baseline_version ?? ((active?.baseline_version ?? 0) + 1)
      const values = sources.map((source) => source.intercept).sort((left, right) => left - right)
      const median = percentile(values, 0.5)
      const deviations = values.map((value) => Math.abs(value - median)).sort((left, right) => left - right)
      const firstObservedAt = sources.map((source) => source.first_observed_at).sort()[0] as string
      const lastObservedAt = sources.map((source) => source.last_observed_at).sort().at(-1) as string
      const evidence = tokenInterceptEvidence(sources)
      const table = client.dialect.qualifyTable('juhe_stats', 'model_token_intercept_baseline_versions')
      await client.execute(`
        INSERT INTO ${table} (
          cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, baseline_version,
          version_status, evidence_status, independent_source_count, retained_source_count, excluded_source_count,
          median_intercept, mad_intercept, q10_intercept, q90_intercept,
          strong_threshold_intercept, strong_gate_enabled, calibration_note,
          first_observed_at, last_observed_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, 'calibration_pending', ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, NULL, ?, ?, ?)
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
        evidence.status, sources.length, evidence.retainedSourceCount, sources.length - evidence.retainedSourceCount,
        median, percentile(deviations, 0.5), percentile(values, 0.1), percentile(values, 0.9),
        firstObservedAt, lastObservedAt, new Date().toISOString()
      ])
    }
    const baseline = await findBaseline(client, scope, 'active')
      ?? await findBaseline(client, scope, 'calibration_pending')
    contexts.set(tokenInterceptScopeKey(scope), contextFor(scope, baseline, snapshot))
  }
  return contexts
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
  await client.transaction(async (tx) => {
    const table = tx.dialect.qualifyTable('juhe_stats', 'model_token_intercept_baseline_versions')
    const candidate = await tx.one<BaselineRow>(`
      SELECT baseline_version, version_status, evidence_status, independent_source_count, q90_intercept,
        strong_threshold_intercept, strong_gate_enabled
      FROM ${table}
      WHERE cohort_key_hmac = ? AND requested_model = ? AND tokenizer_version = ?
        AND probe_set_version = ? AND baseline_version = ?
      LIMIT 1${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [input.cohortKeyHmac, input.requestedModel, input.tokenizerVersion, input.probeSetVersion, input.baselineVersion])
    if (!candidate || candidate.version_status !== 'calibration_pending') throw new Error('固定截距待校准基线版本不存在')
    if (candidate.evidence_status !== 'stable' || Number(candidate.independent_source_count) < 10) {
      throw new Error('固定截距基线尚未达到稳定独立来源门槛')
    }
    const q90 = optionalNumber(candidate.q90_intercept)
    if (q90 === undefined || input.strongThresholdIntercept < q90) {
      throw new Error('固定截距校准阈值不能低于当前 cohort 的 q90')
    }
    const updatedAt = new Date().toISOString()
    await tx.execute(`
      UPDATE ${table}
      SET version_status = 'retired', strong_gate_enabled = 0, updated_at = ?
      WHERE cohort_key_hmac = ? AND requested_model = ? AND tokenizer_version = ?
        AND probe_set_version = ? AND version_status = 'active'
    `, [updatedAt, input.cohortKeyHmac, input.requestedModel, input.tokenizerVersion, input.probeSetVersion])
    const activated = await tx.execute(`
      UPDATE ${table}
      SET version_status = 'active', strong_threshold_intercept = ?, strong_gate_enabled = 1,
        calibration_note = ?, updated_at = ?
      WHERE cohort_key_hmac = ? AND requested_model = ? AND tokenizer_version = ?
        AND probe_set_version = ? AND baseline_version = ? AND version_status = 'calibration_pending'
    `, [
      input.strongThresholdIntercept, note, updatedAt, input.cohortKeyHmac,
      input.requestedModel, input.tokenizerVersion, input.probeSetVersion, input.baselineVersion
    ])
    if (activated.changes !== 1) throw new Error('固定截距基线激活冲突，请刷新后重试')
    const snapshot = await loadSourceInterceptSnapshot(tx, input)
    const active = await findBaseline(tx, input, 'active')
    if (!active) throw new Error('固定截距基线激活后读取失败')
    await rematerializeTokenInterceptLatest(tx, contextFor(input, active, snapshot))
  })
}

export function evaluateTokenInterceptBaseline(
  context: TokenInterceptEvaluationContext | undefined,
  key: TokenInterceptAccountKey,
  accountIntercept: number
): TokenInterceptBaselineEvaluation {
  const baseline = context?.baseline
  if (!baseline) return unavailableEvaluation()
  const candidateBuckets = context.bucketsByAccount.get(accountKey(key)) ?? new Set<string>()
  const sources = context.sources.filter((source) => !candidateBuckets.has(source.upstream_bucket_hmac))
  const values = sources.map((source) => source.intercept).sort((left, right) => left - right)
  const median = values.length ? percentile(values, 0.5) : undefined
  const deviations = median === undefined ? [] : values.map((value) => Math.abs(value - median)).sort((left, right) => left - right)
  const threshold = optionalNumber(baseline.strong_threshold_intercept)
  const active = baseline.version_status === 'active'
  const gateEnabled = active
    && candidateBuckets.size > 0
    && baseline.strong_gate_enabled === 1
    && baseline.evidence_status === 'stable'
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

export function tokenInterceptScopeKey(scope: TokenInterceptScope): string {
  return `${scope.cohortKeyHmac}\u0000${scope.requestedModel}\u0000${scope.tokenizerVersion}\u0000${scope.probeSetVersion}`
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

async function loadSourceInterceptSnapshot(
  client: DatabaseClient,
  scope: TokenInterceptScope
): Promise<SourceSnapshot> {
  const windows = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_windows')
  const sources = client.dialect.qualifyTable('juhe_stats', 'model_trust_window_sources')
  const rows = await client.query<SourceInterceptRow>(`
    SELECT w.system_account_id, w.account_id, s.upstream_bucket_hmac,
      w.intercept, w.slope, w.round_count, w.valid_sample_count,
      w.first_observed_at, w.last_observed_at
    FROM ${windows} w
    INNER JOIN ${sources} s
      ON s.system_account_id = w.system_account_id
      AND s.account_id = w.account_id
      AND s.cohort_key_hmac = w.cohort_key_hmac
    WHERE w.cohort_key_hmac = ? AND w.requested_model = ? AND w.tokenizer_version = ?
      AND w.probe_set_version = ? AND w.intercept IS NOT NULL AND w.slope BETWEEN 0.97 AND 1.03
      AND w.round_count >= 3 AND w.valid_sample_count >= 6
      AND NOT EXISTS (
        SELECT 1 FROM ${sources} other
        WHERE other.system_account_id = s.system_account_id
          AND other.account_id = s.account_id
          AND other.cohort_key_hmac = s.cohort_key_hmac
          AND other.upstream_bucket_hmac <> s.upstream_bucket_hmac
      )
    ORDER BY s.upstream_bucket_hmac, w.last_observed_at DESC
    LIMIT ${maximumBaselineRows + 1}
  `, [scope.cohortKeyHmac, scope.requestedModel, scope.tokenizerVersion, scope.probeSetVersion])
  if (rows.length > maximumBaselineRows) {
    throw new Error('固定截距 cohort 超过单批聚合上限，已回滚并等待离线分阶段重建')
  }
  const bucketsByAccount = new Map<string, Set<string>>()
  const accountKeys = new Map<string, TokenInterceptAccountKey>()
  const byBucket = new Map<string, SourceInterceptRow[]>()
  for (const row of rows) {
    const account = {
      systemAccountId: row.system_account_id,
      accountId: row.account_id,
      requestedModel: scope.requestedModel
    }
    const key = accountKey(account)
    accountKeys.set(key, account)
    const accountBuckets = bucketsByAccount.get(key) ?? new Set<string>()
    accountBuckets.add(row.upstream_bucket_hmac)
    bucketsByAccount.set(key, accountBuckets)
    const bucketRows = byBucket.get(row.upstream_bucket_hmac) ?? []
    bucketRows.push(row)
    byBucket.set(row.upstream_bucket_hmac, bucketRows)
  }
  const collapsed = [...byBucket.entries()].slice(0, maximumBaselineSources).map(([bucket, bucketRows]) => {
    const intercepts = bucketRows.map((row) => Number(row.intercept)).filter(Number.isFinite).sort((left, right) => left - right)
    const representative = bucketRows[0] as SourceInterceptRow
    return {
      ...representative,
      upstream_bucket_hmac: bucket,
      intercept: percentile(intercepts, 0.5),
      source_valid_sample_count: bucketRows.reduce((sum, row) => sum + Number(row.valid_sample_count), 0),
      first_observed_at: bucketRows.map((row) => row.first_observed_at).sort()[0] as string,
      last_observed_at: bucketRows.map((row) => row.last_observed_at).sort().at(-1) as string
    }
  })
  return { sources: collapsed, bucketsByAccount, accountKeys: [...accountKeys.values()] }
}

async function rematerializeTokenInterceptLatest(
  client: DatabaseClient,
  context: TokenInterceptEvaluationContext
): Promise<void> {
  const windows = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_windows')
  const latest = client.dialect.qualifyTable('juhe_stats', 'model_account_trust_results')
  const rows = await client.query<{
    system_account_id: string
    account_id: string
    requested_model: string
    intercept: number | null
    usage_integrity_status: string
    reason_codes_json: string | null
  }>(`
    SELECT w.system_account_id, w.account_id, w.requested_model, w.intercept,
      w.usage_integrity_status, l.reason_codes_json
    FROM ${windows} w
    LEFT JOIN ${latest} l
      ON l.system_account_id = w.system_account_id
      AND l.account_id = w.account_id
      AND l.requested_model = w.requested_model
    WHERE w.cohort_key_hmac = ? AND w.requested_model = ? AND w.tokenizer_version = ?
      AND w.probe_set_version = ?
    ORDER BY w.account_id
    LIMIT ${maximumActivationAccounts + 1}
  `, [
    context.scope.cohortKeyHmac, context.scope.requestedModel,
    context.scope.tokenizerVersion, context.scope.probeSetVersion
  ])
  if (rows.length > maximumActivationAccounts) {
    throw new Error('固定截距基线影响账户超过激活上限，需要离线分阶段重物化')
  }
  const updatedAt = new Date().toISOString()
  const writes: unknown[][] = []
  for (const row of rows) {
    const hasIntercept = row.intercept !== null && Number.isFinite(Number(row.intercept))
    const evaluation = hasIntercept
      ? evaluateTokenInterceptBaseline(context, {
          systemAccountId: row.system_account_id,
          accountId: row.account_id,
          requestedModel: row.requested_model
        }, Number(row.intercept))
      : {
          baselineVersion: Number(context.baseline?.baseline_version),
          baselineStatus: 'active' as const,
          evidenceStatus: context.baseline?.evidence_status ?? 'insufficient',
          strongGateEnabled: false,
          suspectedFixedPadding: false
        }
    const reasonCodes = parseReasonCodes(row.reason_codes_json)
      .filter((code) => code !== 'fixed_intercept_padding' && code !== 'fixed_intercept_calibration_pending')
    if (evaluation.suspectedFixedPadding) reasonCodes.push('fixed_intercept_padding')
    writes.push([
      row.system_account_id, row.account_id, row.requested_model,
      evaluation.suspectedFixedPadding ? 'suspected_padding' : row.usage_integrity_status,
      evaluation.looMedian ?? null, evaluation.looMad ?? null, evaluation.baselineVersion ?? null,
      evaluation.baselineStatus, evaluation.strongGateEnabled ? 1 : 0,
      JSON.stringify([...new Set(reasonCodes)]), updatedAt
    ])
  }
  for (const batch of chunks(writes, 50)) {
    const values = batch.map(() => `(${Array.from({ length: 11 }, () => '?').join(', ')})`).join(', ')
    await client.execute(`
      INSERT INTO ${latest} (
        system_account_id, account_id, requested_model, usage_integrity_status,
        intercept_baseline_median, intercept_baseline_mad, intercept_baseline_version,
        intercept_baseline_status, intercept_strong_gate_enabled, reason_codes_json, updated_at
      ) VALUES ${values}
      ON CONFLICT (system_account_id, account_id, requested_model) DO UPDATE SET
        usage_integrity_status = excluded.usage_integrity_status,
        intercept_baseline_median = excluded.intercept_baseline_median,
        intercept_baseline_mad = excluded.intercept_baseline_mad,
        intercept_baseline_version = excluded.intercept_baseline_version,
        intercept_baseline_status = excluded.intercept_baseline_status,
        intercept_strong_gate_enabled = excluded.intercept_strong_gate_enabled,
        reason_codes_json = excluded.reason_codes_json,
        updated_at = excluded.updated_at
    `, batch.flat())
  }
}

function chunks<T>(values: T[], size: number): T[][] {
  const result: T[][] = []
  for (let index = 0; index < values.length; index += size) result.push(values.slice(index, index + size))
  return result
}

function tokenInterceptEvidence(sources: CollapsedSourceIntercept[]): { status: string; retainedSourceCount: number } {
  const stages = [
    { status: 'stable', sourceCount: 10, sampleCount: 300, days: 14, samplesPerSource: 30 },
    { status: 'candidate', sourceCount: 5, sampleCount: 100, days: 7, samplesPerSource: 20 },
    { status: 'bootstrap', sourceCount: 3, sampleCount: 30, days: 3, samplesPerSource: 10 }
  ] as const
  for (const stage of stages) {
    const qualified = sources.filter((source) => (
      sourceDurationDays(source) >= stage.days
      && source.source_valid_sample_count >= stage.samplesPerSource
    ))
    const sampleCount = qualified.reduce((sum, source) => sum + source.source_valid_sample_count, 0)
    if (qualified.length >= stage.sourceCount && sampleCount >= stage.sampleCount) {
      return { status: stage.status, retainedSourceCount: qualified.length }
    }
  }
  return { status: 'insufficient', retainedSourceCount: 0 }
}

function sourceDurationDays(source: Pick<CollapsedSourceIntercept, 'first_observed_at' | 'last_observed_at'>): number {
  return Math.max(1, Math.ceil((Date.parse(source.last_observed_at) - Date.parse(source.first_observed_at)) / 86_400_000) + 1)
}

function contextFor(
  scope: TokenInterceptScope,
  baseline: BaselineRow | undefined,
  snapshot: SourceSnapshot
): TokenInterceptEvaluationContext {
  return { scope, baseline, ...snapshot }
}

function uniqueScopes(scopes: TokenInterceptScope[]): TokenInterceptScope[] {
  const unique = new Map<string, TokenInterceptScope>()
  for (const scope of scopes) unique.set(tokenInterceptScopeKey(scope), scope)
  return [...unique.values()]
}

function accountKey(key: TokenInterceptAccountKey): string {
  return `${key.systemAccountId}\u0000${key.accountId}\u0000${key.requestedModel}`
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

function parseReasonCodes(value: unknown): string[] {
  try {
    const parsed = JSON.parse(String(value ?? '[]'))
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string').slice(0, 20) : []
  } catch {
    return []
  }
}

function unavailableEvaluation(): TokenInterceptBaselineEvaluation {
  return {
    baselineStatus: 'unavailable',
    evidenceStatus: 'insufficient',
    strongGateEnabled: false,
    suspectedFixedPadding: false
  }
}
