import { nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import type { ObservationRow } from './model-trust.repository.js'
import {
  euclideanVectorDistance,
  median,
  quantile,
  robustVectorDistance,
  robustVectorSummary
} from './model-trust-statistics.js'

const featureWidth = 8

type AccountModelKey = { systemAccountId: string; accountId: string; requestedModel: string }

type SourceFeatureRow = {
  system_account_id: string
  account_id: string
  population_key_hmac: string
  requested_model: string
  upstream_bucket_hmac: string
  feature_version: string
  sample_count: number
  constraint_pass_count: number
  first_observed_at: string
  last_observed_at: string
  latest_feature_1: number
  latest_feature_2: number
  latest_feature_3: number
  latest_feature_4: number
  latest_feature_5: number
  latest_feature_6: number
  latest_feature_7: number
  latest_feature_8: number
}

type BaselineRow = {
  baseline_version: number
  version_status: string
  evidence_status: string
  independent_source_count: number
  median_vector_json: string
  mad_vector_json: string
  first_observed_at: string
  last_observed_at: string
}

export interface IdentityTrustEvaluation {
  identityStatus: string
  evidenceStatus: string
  identityObservationCount: number
  pairedProbeCount: number
  independentSourceCount: number
  identityDistance?: number
  pairedDistance?: number
  pairedBaselineMedian?: number
  pairedBaselineMad?: number
  baselineVersion?: number
  baselineVersionStatus?: string
  featureVersion?: string
  lastObservedAt?: string
  reasonCodes: string[]
}

export function isIdentityObservation(row: ObservationRow): boolean {
  return row.probe_family.startsWith('identity_')
    && row.observation_status === 'observed'
    && Boolean(row.observed_model?.trim())
    && observationVector(row).every(Number.isFinite)
    && identityFeatureValues(row).every((value) => value !== null)
}

export async function upsertIdentitySourceFeature(client: DatabaseClient, row: ObservationRow): Promise<void> {
  if (!isIdentityObservation(row)) return
  const table = client.dialect.qualifyTable('juhe_stats', 'model_identity_source_features')
  const vector = observationVector(row)
  await client.execute(`
    INSERT INTO ${table} (
      system_account_id, account_id, population_key_hmac, requested_model, upstream_bucket_hmac,
      probe_key_hmac, feature_version, sample_count,
      sum_feature_1, sum_feature_2, sum_feature_3, sum_feature_4, sum_feature_5, sum_feature_6, sum_feature_7, sum_feature_8,
      latest_feature_1, latest_feature_2, latest_feature_3, latest_feature_4, latest_feature_5, latest_feature_6, latest_feature_7, latest_feature_8,
      constraint_pass_count, first_observed_at, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (system_account_id, account_id, population_key_hmac, requested_model, upstream_bucket_hmac, probe_key_hmac, feature_version) DO UPDATE SET
      sample_count = model_identity_source_features.sample_count + 1,
      sum_feature_1 = model_identity_source_features.sum_feature_1 + excluded.sum_feature_1,
      sum_feature_2 = model_identity_source_features.sum_feature_2 + excluded.sum_feature_2,
      sum_feature_3 = model_identity_source_features.sum_feature_3 + excluded.sum_feature_3,
      sum_feature_4 = model_identity_source_features.sum_feature_4 + excluded.sum_feature_4,
      sum_feature_5 = model_identity_source_features.sum_feature_5 + excluded.sum_feature_5,
      sum_feature_6 = model_identity_source_features.sum_feature_6 + excluded.sum_feature_6,
      sum_feature_7 = model_identity_source_features.sum_feature_7 + excluded.sum_feature_7,
      sum_feature_8 = model_identity_source_features.sum_feature_8 + excluded.sum_feature_8,
      latest_feature_1 = excluded.latest_feature_1,
      latest_feature_2 = excluded.latest_feature_2,
      latest_feature_3 = excluded.latest_feature_3,
      latest_feature_4 = excluded.latest_feature_4,
      latest_feature_5 = excluded.latest_feature_5,
      latest_feature_6 = excluded.latest_feature_6,
      latest_feature_7 = excluded.latest_feature_7,
      latest_feature_8 = excluded.latest_feature_8,
      constraint_pass_count = model_identity_source_features.constraint_pass_count + excluded.constraint_pass_count,
      last_observed_at = excluded.last_observed_at,
      updated_at = excluded.updated_at
  `, [
    row.system_account_id, row.account_id, row.population_key_hmac, row.requested_model, row.upstream_bucket_hmac,
    row.probe_key_hmac, row.feature_version,
    ...vector, ...vector, row.constraint_passed === 1 ? 1 : 0,
    row.created_at, row.created_at, nowIso()
  ])
}

export async function refreshIdentityBaselines(client: DatabaseClient, rows: ObservationRow[]): Promise<string[]> {
  const keys = new Map<string, { populationKey: string; model: string; featureVersion: string }>()
  for (const row of rows.filter(isIdentityObservation)) {
    const key = { populationKey: row.population_key_hmac, model: row.requested_model, featureVersion: row.feature_version }
    keys.set(`${key.populationKey}\u0000${key.model}\u0000${key.featureVersion}`, key)
  }
  const changedPopulations = new Set<string>()
  for (const key of keys.values()) {
    if (await refreshBaseline(client, key)) changedPopulations.add(key.populationKey)
  }
  return [...changedPopulations]
}

export async function listIdentityAccountModelsForPopulations(client: DatabaseClient, populations: string[]): Promise<AccountModelKey[]> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_identity_source_features')
  const result = new Map<string, AccountModelKey>()
  for (const population of [...new Set(populations)]) {
    const members = await client.query<{ system_account_id: string; account_id: string; requested_model: string }>(`
      SELECT DISTINCT system_account_id, account_id, requested_model FROM ${table}
      WHERE population_key_hmac = ?
      ORDER BY system_account_id, account_id, requested_model
    `, [population])
    for (const member of members) {
      const key = { systemAccountId: member.system_account_id, accountId: member.account_id, requestedModel: member.requested_model }
      result.set(`${key.systemAccountId}\u0000${key.accountId}\u0000${key.requestedModel}`, key)
    }
  }
  return [...result.values()]
}

export async function evaluateIdentityTrust(client: DatabaseClient, key: AccountModelKey): Promise<IdentityTrustEvaluation | undefined> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_identity_source_features')
  const ownRows = await client.query<SourceFeatureRow>(`
    SELECT * FROM ${table}
    WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
    ORDER BY last_observed_at DESC
  `, [key.systemAccountId, key.accountId, key.requestedModel])
  if (!ownRows.length) return undefined
  const current = ownRows[0] as SourceFeatureRow
  const populationRows = await client.query<SourceFeatureRow>(`
    SELECT * FROM ${table}
    WHERE population_key_hmac = ? AND feature_version = ?
    ORDER BY upstream_bucket_hmac, requested_model, account_id
  `, [current.population_key_hmac, current.feature_version])
  const signatures = collapseSourceSignatures(populationRows)
  const targetPopulationRows = populationRows.filter((row) => row.requested_model === key.requestedModel)
  const ownBuckets = new Set(ownRows.map((row) => row.upstream_bucket_hmac))
  const ownVector = averageVectors(ownRows.map(sourceVector))
  const looTarget = signatures.filter((item) => item.model === key.requestedModel && !ownBuckets.has(item.upstreamBucket))
  const looSummary = robustVectorSummary(looTarget.map((item) => item.vector))
  const identityDistance = looSummary.median.length ? robustVectorDistance(ownVector, looSummary.median, looSummary.mad) : undefined
  const baseline = await latestBaseline(client, current.population_key_hmac, key.requestedModel, current.feature_version)
  const driftProtected = baseline?.version_status === 'drift_protected'
  const durationDays = populationDurationDays(targetPopulationRows)
  const evidenceStatus = evidenceStatusFor(looSummary.retainedCount, targetPopulationRows.reduce((sum, row) => sum + Number(row.sample_count), 0), durationDays)
  const pair = pairedModels(key.requestedModel)
  const ownAlternateRows = populationRows.filter((row) => row.account_id === key.accountId && pair.includes(row.requested_model) && row.requested_model !== key.requestedModel)
  const ownAlternateByModel = groupRowsByModel(ownAlternateRows)
  const pairDistances = [...ownAlternateByModel.values()].map((rows) => euclideanVectorDistance(ownVector, averageVectors(rows.map(sourceVector))))
  const pairedDistance = pairDistances.length ? Math.min(...pairDistances) : undefined
  const populationPairDistances = independentPairDistances(signatures, key.requestedModel, pair).filter((item) => !ownBuckets.has(item.upstreamBucket))
  const distanceValues = populationPairDistances.map((item) => item.distance)
  const pairedBaselineMedian = distanceValues.length ? median(distanceValues) : undefined
  const pairedBaselineMad = distanceValues.length && pairedBaselineMedian !== undefined
    ? median(distanceValues.map((value) => Math.abs(value - pairedBaselineMedian)))
    : undefined
  const pairedQ10 = distanceValues.length ? quantile(distanceValues, 0.1) : undefined
  const sameSourceThreshold = pairedQ10 === undefined || pairedBaselineMad === undefined
    ? undefined
    : Math.max(0.005, pairedQ10 - 3 * Math.max(0.005, pairedBaselineMad * 1.4826))
  const sameSource = pairedDistance !== undefined && sameSourceThreshold !== undefined
    && looSummary.retainedCount >= 3 && pairedDistance <= sameSourceThreshold
  const downgradeModel = downgradeComparisonModel(key.requestedModel)
  const downgradeSignatures = downgradeModel
    ? signatures.filter((item) => item.model === downgradeModel && !ownBuckets.has(item.upstreamBucket))
    : []
  const downgradeSummary = robustVectorSummary(downgradeSignatures.map((item) => item.vector))
  const downgradeDistance = downgradeSummary.median.length ? robustVectorDistance(ownVector, downgradeSummary.median, downgradeSummary.mad) : undefined
  const suspectedDowngrade = identityDistance !== undefined && downgradeDistance !== undefined
    && identityDistance >= 2.5 && downgradeDistance + 0.75 < identityDistance
  let identityStatus = 'insufficient_evidence'
  const reasonCodes: string[] = []
  if (driftProtected) {
    reasonCodes.push('population_drift_protected')
  } else if (evidenceStatus !== 'insufficient') {
    if (sameSource) {
      identityStatus = 'suspected_same_source'
      reasonCodes.push('paired_models_collapsed')
    } else if (suspectedDowngrade) {
      identityStatus = 'suspected_downgrade'
      reasonCodes.push('closer_to_lower_model_baseline')
    } else if (identityDistance !== undefined && identityDistance >= 3.5) {
      identityStatus = 'population_outlier'
      reasonCodes.push('identity_population_outlier')
    } else {
      identityStatus = 'consistent'
    }
  } else {
    reasonCodes.push('population_baseline_unavailable')
  }
  await upsertPairedWindow(client, {
    key,
    populationKey: current.population_key_hmac,
    featureVersion: current.feature_version,
    baselineVersion: baseline?.baseline_version,
    pairKey: pair.join(':'),
    pairedProbeCount: pairDistances.length,
    independentSourceCount: distanceValues.length,
    pairedDistance,
    pairedBaselineMedian,
    pairedBaselineMad,
    pairedQ10,
    status: sameSource ? 'suspected_same_source' : evidenceStatus === 'insufficient' ? 'insufficient_evidence' : 'consistent',
    lastObservedAt: current.last_observed_at
  })
  return {
    identityStatus,
    evidenceStatus,
    identityObservationCount: ownRows.reduce((sum, row) => sum + Number(row.sample_count), 0),
    pairedProbeCount: pairDistances.length,
    independentSourceCount: looSummary.retainedCount,
    identityDistance,
    pairedDistance,
    pairedBaselineMedian,
    pairedBaselineMad,
    baselineVersion: baseline?.baseline_version,
    baselineVersionStatus: baseline?.version_status,
    featureVersion: current.feature_version,
    lastObservedAt: current.last_observed_at,
    reasonCodes
  }
}

async function refreshBaseline(client: DatabaseClient, key: { populationKey: string; model: string; featureVersion: string }): Promise<boolean> {
  const sourceTable = client.dialect.qualifyTable('juhe_stats', 'model_identity_source_features')
  const baselineTable = client.dialect.qualifyTable('juhe_stats', 'model_identity_baseline_versions')
  const rows = await client.query<SourceFeatureRow>(`
    SELECT * FROM ${sourceTable}
    WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ?
    ORDER BY upstream_bucket_hmac, account_id
  `, [key.populationKey, key.model, key.featureVersion])
  const signatures = collapseSourceSignatures(rows).map((item) => item.vector)
  if (signatures.length < 3) return false
  const summary = robustVectorSummary(signatures)
  const active = await client.one<BaselineRow>(`
    SELECT * FROM ${baselineTable}
    WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ? AND version_status = 'active'
    ORDER BY baseline_version DESC LIMIT 1
  `, [key.populationKey, key.model, key.featureVersion])
  const first = rows.reduce((value, row) => value < row.first_observed_at ? value : row.first_observed_at, rows[0]?.first_observed_at ?? nowIso())
  const last = rows.reduce((value, row) => value > row.last_observed_at ? value : row.last_observed_at, '')
  const evidence = evidenceStatusFor(signatures.length, rows.reduce((sum, row) => sum + Number(row.sample_count), 0), populationDurationDays(rows))
  if (!active) {
    await insertBaseline(client, baselineTable, key, 1, 'active', evidence, summary, first, last)
    return true
  }
  const activeMedian = parseVector(active.median_vector_json)
  const activeMad = parseVector(active.mad_vector_json)
  const shiftedShare = signatures.filter((vector) => robustVectorDistance(vector, activeMedian, activeMad) >= 3).length / signatures.length
  if (signatures.length >= 5 && shiftedShare >= 0.6) {
    const candidateVersion = Number(active.baseline_version) + 1
    const candidate = await client.one<BaselineRow>(`
      SELECT * FROM ${baselineTable}
      WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ? AND baseline_version = ?
      LIMIT 1
    `, [key.populationKey, key.model, key.featureVersion, candidateVersion])
    if (!candidate) {
      await insertBaseline(client, baselineTable, key, candidateVersion, 'drift_protected', evidence, summary, last, last)
      return true
    }
    const candidateAgeDays = durationDays(candidate.first_observed_at, last)
    const candidateDistance = robustVectorDistance(summary.median, parseVector(candidate.median_vector_json), parseVector(candidate.mad_vector_json))
    if (candidateAgeDays >= 3 && candidateDistance <= 1.5 && evidence !== 'insufficient') {
      await client.execute(`UPDATE ${baselineTable} SET version_status = 'retired', updated_at = ? WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ? AND baseline_version = ?`, [nowIso(), key.populationKey, key.model, key.featureVersion, active.baseline_version])
      await client.execute(`UPDATE ${baselineTable} SET version_status = 'active', evidence_status = ?, last_observed_at = ?, updated_at = ? WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ? AND baseline_version = ?`, [evidence, last, nowIso(), key.populationKey, key.model, key.featureVersion, candidateVersion])
      return true
    }
  }
  const populationChanged = active.evidence_status !== evidence || Number(active.independent_source_count) !== signatures.length
  await client.execute(`
    UPDATE ${baselineTable}
    SET evidence_status = ?, independent_source_count = ?, retained_source_count = ?, excluded_source_count = ?,
      last_observed_at = ?, updated_at = ?
    WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ? AND baseline_version = ?
  `, [evidence, signatures.length, summary.retainedCount, summary.excludedCount, last, nowIso(), key.populationKey, key.model, key.featureVersion, active.baseline_version])
  return populationChanged
}

async function insertBaseline(client: DatabaseClient, table: string, key: { populationKey: string; model: string; featureVersion: string }, version: number, status: string, evidence: string, summary: ReturnType<typeof robustVectorSummary>, first: string, last: string): Promise<void> {
  await client.execute(`
    INSERT INTO ${table} (
      population_key_hmac, requested_model, feature_version, baseline_version, version_status, evidence_status,
      independent_source_count, retained_source_count, excluded_source_count, median_vector_json, mad_vector_json,
      q10_vector_json, q90_vector_json, first_observed_at, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [key.populationKey, key.model, key.featureVersion, version, status, evidence, summary.retainedCount + summary.excludedCount, summary.retainedCount, summary.excludedCount, JSON.stringify(summary.median), JSON.stringify(summary.mad), JSON.stringify(summary.q10), JSON.stringify(summary.q90), first, last, nowIso()])
}

async function latestBaseline(client: DatabaseClient, populationKey: string, model: string, featureVersion: string): Promise<BaselineRow | undefined> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_identity_baseline_versions')
  return await client.one<BaselineRow>(`
    SELECT * FROM ${table}
    WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ?
      AND version_status IN ('drift_protected', 'active')
    ORDER BY CASE WHEN version_status = 'drift_protected' THEN 0 ELSE 1 END, baseline_version DESC
    LIMIT 1
  `, [populationKey, model, featureVersion])
}

async function upsertPairedWindow(client: DatabaseClient, input: {
  key: AccountModelKey; populationKey: string; featureVersion: string; baselineVersion?: number; pairKey: string
  pairedProbeCount: number; independentSourceCount: number; pairedDistance?: number; pairedBaselineMedian?: number
  pairedBaselineMad?: number; pairedQ10?: number; status: string; lastObservedAt: string
}): Promise<void> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_paired_similarity_windows')
  await client.execute(`
    INSERT INTO ${table} (
      system_account_id, account_id, population_key_hmac, pair_key, feature_version, baseline_version,
      paired_probe_count, independent_source_count, median_distance, loo_median_distance, loo_mad_distance,
      loo_q10_distance, similarity_status, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (system_account_id, account_id, population_key_hmac, pair_key, feature_version) DO UPDATE SET
      baseline_version = excluded.baseline_version, paired_probe_count = excluded.paired_probe_count,
      independent_source_count = excluded.independent_source_count, median_distance = excluded.median_distance,
      loo_median_distance = excluded.loo_median_distance, loo_mad_distance = excluded.loo_mad_distance,
      loo_q10_distance = excluded.loo_q10_distance, similarity_status = excluded.similarity_status,
      last_observed_at = excluded.last_observed_at, updated_at = excluded.updated_at
  `, [input.key.systemAccountId, input.key.accountId, input.populationKey, input.pairKey, input.featureVersion, input.baselineVersion ?? null, input.pairedProbeCount, input.independentSourceCount, input.pairedDistance ?? null, input.pairedBaselineMedian ?? null, input.pairedBaselineMad ?? null, input.pairedQ10 ?? null, input.status, input.lastObservedAt, nowIso()])
}

function collapseSourceSignatures(rows: SourceFeatureRow[]): Array<{ upstreamBucket: string; model: string; vector: number[] }> {
  const groups = new Map<string, { upstreamBucket: string; model: string; vectors: number[][] }>()
  for (const row of rows) {
    const key = `${row.upstream_bucket_hmac}\u0000${row.requested_model}`
    const group = groups.get(key) ?? { upstreamBucket: row.upstream_bucket_hmac, model: row.requested_model, vectors: [] }
    group.vectors.push(sourceVector(row))
    groups.set(key, group)
  }
  return [...groups.values()].map((group) => ({ upstreamBucket: group.upstreamBucket, model: group.model, vector: averageVectors(group.vectors) }))
}

function independentPairDistances(signatures: Array<{ upstreamBucket: string; model: string; vector: number[] }>, targetModel: string, models: string[]): Array<{ upstreamBucket: string; distance: number }> {
  const buckets = new Map<string, Map<string, number[]>>()
  for (const signature of signatures) {
    const bucket = buckets.get(signature.upstreamBucket) ?? new Map<string, number[]>()
    bucket.set(signature.model, signature.vector)
    buckets.set(signature.upstreamBucket, bucket)
  }
  const results: Array<{ upstreamBucket: string; distance: number }> = []
  for (const [upstreamBucket, bucket] of buckets) {
    const target = bucket.get(targetModel)
    if (!target) continue
    const distances = models.filter((model) => model !== targetModel).map((model) => bucket.get(model)).filter((value): value is number[] => Boolean(value)).map((value) => euclideanVectorDistance(target, value))
    if (distances.length) results.push({ upstreamBucket, distance: Math.min(...distances) })
  }
  return results
}

function sourceVector(row: SourceFeatureRow): number[] {
  return Array.from({ length: featureWidth }, (_, index) => Number(row[`latest_feature_${index + 1}` as keyof SourceFeatureRow] ?? 0))
}

function observationVector(row: ObservationRow): number[] {
  return Array.from({ length: featureWidth }, (_, index) => Number(row[`feature_${index + 1}` as keyof ObservationRow] ?? 0))
}

function identityFeatureValues(row: ObservationRow): Array<number | null> {
  return Array.from({ length: featureWidth }, (_, index) => row[`feature_${index + 1}` as keyof ObservationRow] as number | null)
}

function averageVectors(vectors: number[][]): number[] {
  if (!vectors.length) return []
  return Array.from({ length: vectors[0]?.length ?? 0 }, (_, index) => vectors.reduce((sum, vector) => sum + Number(vector[index] ?? 0), 0) / vectors.length)
}

function groupRowsByModel(rows: SourceFeatureRow[]): Map<string, SourceFeatureRow[]> {
  const result = new Map<string, SourceFeatureRow[]>()
  for (const row of rows) result.set(row.requested_model, [...(result.get(row.requested_model) ?? []), row])
  return result
}

function pairedModels(model: string): string[] {
  if (['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna'].includes(model)) return ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna']
  if (['gpt-5.5', 'gpt-5.4'].includes(model)) return ['gpt-5.5', 'gpt-5.4']
  return [model]
}

function downgradeComparisonModel(model: string): string | undefined {
  if (model === 'gpt-5.6-sol' || model === 'gpt-5.6-terra') return 'gpt-5.6-luna'
  if (model === 'gpt-5.5') return 'gpt-5.4'
  return undefined
}

function evidenceStatusFor(sourceCount: number, sampleCount: number, durationDaysValue: number): string {
  if (sourceCount >= 10 && sampleCount >= 300 && durationDaysValue >= 14) return 'stable'
  if (sourceCount >= 5 && sampleCount >= 100 && durationDaysValue >= 7) return 'candidate'
  if (sourceCount >= 3 && sampleCount >= 30 && durationDaysValue >= 3) return 'bootstrap'
  return 'insufficient'
}

function populationDurationDays(rows: SourceFeatureRow[]): number {
  if (!rows.length) return 0
  const first = rows.reduce((value, row) => value < row.first_observed_at ? value : row.first_observed_at, rows[0]?.first_observed_at ?? '')
  const last = rows.reduce((value, row) => value > row.last_observed_at ? value : row.last_observed_at, '')
  return durationDays(first, last)
}

function durationDays(first: string, last: string): number {
  return Math.max(1, Math.ceil((Date.parse(last) - Date.parse(first)) / 86_400_000) + 1)
}

function parseVector(value: string): number[] {
  try {
    const parsed = JSON.parse(value) as unknown
    return Array.isArray(parsed) ? parsed.map(Number).filter(Number.isFinite).slice(0, featureWidth) : []
  } catch {
    return []
  }
}
