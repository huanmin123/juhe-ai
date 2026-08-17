import { runtimeConfig } from '../config/runtime.js'
import { getDatasetDatabase, getStatsDatabase, newId, nowIso } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import { acquireBackgroundJobLeaseAsync, releaseBackgroundJobLeaseAsync, renewBackgroundJobLeaseAsync } from './background-task-runs.repository.js'
import { pinScheduledJobLeaseInTransaction, type ScheduledJobLeaseFence } from './scheduled-job-lease.repository.js'
import {
  evaluateIdentityTrust,
  hasHardTrustConflict,
  isIdentityObservation,
  refreshIdentityBaselines,
  type IdentityPopulationScope,
  upsertIdentitySourceFeature
} from './model-trust-identity.repository.js'
import {
  activateTokenInterceptBaselineVersion,
  evaluateTokenInterceptBaseline,
  refreshTokenInterceptBaselines,
  tokenInterceptScopeKey,
  type TokenInterceptBaselineEvaluation,
  type TokenInterceptEvaluationContext,
  type TokenInterceptScope
} from './model-trust-token-baseline.repository.js'

const aggregationJobName = 'model-trust-observation-aggregation'
const aggregationLeaseKey = 'scheduled:model-trust-observation-aggregation:global'
const aggregationLeaseDurationMs = 5 * 60_000
const aggregationLeaseRenewIntervalMs = 60_000
const maximumObservationsPerTransaction = 100
const maximumDirtyAccountsPerBatch = 100

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
  populationKeyHmac: string
  probeKeyHmac: string
  systemFingerprintHmac?: string
  probeFamily: string
  probeSetVersion: string
  tokenizerVersion: string
  featureVersion: string
  roundIndex: number
  paddingTokens: number
  localInputTokens: number
  reportedInputTokens?: number
  cachedInputTokens?: number
  constraintPassed?: boolean
  featureVector?: number[]
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
  identityObservationCount: number
  pairedProbeCount: number
  slope?: number
  intercept?: number
  interceptBaselineMedian?: number
  interceptBaselineMad?: number
  interceptBaselineVersion?: number
  interceptBaselineStatus?: string
  interceptStrongGateEnabled: boolean
  identityDistance?: number
  pairedDistance?: number
  pairedBaselineMedian?: number
  pairedBaselineMad?: number
  baselineVersion?: number
  baselineVersionStatus?: string
  featureVersion?: string
  tokenizerVersion?: string
  probeSetVersion?: string
  reasonCodes: string[]
  lastObservedAt?: string
}

export interface ObservationRow {
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
  population_key_hmac: string
  probe_key_hmac: string
  system_fingerprint_hmac: string | null
  probe_family: string
  probe_set_version: string
  tokenizer_version: string
  feature_version: string
  round_index: number
  padding_tokens: number
  local_input_tokens: number
  reported_input_tokens: number | null
  cached_input_tokens: number | null
  constraint_passed: number | null
  feature_1: number | null
  feature_2: number | null
  feature_3: number | null
  feature_4: number | null
  feature_5: number | null
  feature_6: number | null
  feature_7: number | null
  feature_8: number | null
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
          upstream_bucket_hmac, cohort_key_hmac, population_key_hmac, probe_key_hmac, system_fingerprint_hmac, probe_family, probe_set_version,
          tokenizer_version, feature_version, round_index, padding_tokens, local_input_tokens, reported_input_tokens,
          cached_input_tokens, constraint_passed, feature_1, feature_2, feature_3, feature_4, feature_5, feature_6, feature_7, feature_8,
          observation_status, identity_status, mapping_status, protocol_status,
          evidence_coverage, trace_id, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `, Object.values(row))
    }
  })
  return inputs.length
}

export async function aggregateModelTrustObservationsAsync(limit = 500, scheduledLease?: ScheduledJobLeaseFence): Promise<number> {
  if (scheduledLease) {
    return await aggregateModelTrustObservationsWithLeaseAsync(limit, { scheduledLease })
  }
  const ownerId = `${runtimeConfig.processRole}:${process.pid}:${newId('model_trust_lease')}`
  const acquired = await acquireBackgroundJobLeaseAsync({
    leaseKey: aggregationLeaseKey,
    jobName: aggregationJobName,
    shardKey: 'global',
    ownerId,
    leaseUntil: new Date(Date.now() + aggregationLeaseDurationMs).toISOString()
  })
  if (!acquired) return 0
  let renewalRunning = false
  const renewalTimer = setInterval(() => {
    if (renewalRunning) return
    renewalRunning = true
    void renewBackgroundJobLeaseAsync(
      aggregationLeaseKey,
      ownerId,
      new Date(Date.now() + aggregationLeaseDurationMs).toISOString()
    ).catch(() => false).finally(() => { renewalRunning = false })
  }, aggregationLeaseRenewIntervalMs)
  renewalTimer.unref()
  try {
    return await aggregateModelTrustObservationsWithLeaseAsync(limit, { legacyOwnerId: ownerId })
  } finally {
    clearInterval(renewalTimer)
    await releaseBackgroundJobLeaseAsync(aggregationLeaseKey, ownerId)
  }
}

async function aggregateModelTrustObservationsWithLeaseAsync(
  limit: number,
  lease: { scheduledLease?: ScheduledJobLeaseFence; legacyOwnerId?: string }
): Promise<number> {
  const dataset = await datasetClient()
  const stats = await statsClient()
  await cleanupCompletedObservationReceipts(dataset, stats, maximumObservationsPerTransaction)
  const observationsTable = dataset.dialect.qualifyTable('juhe_dataset', 'model_check_observations')
  const rows = (await dataset.query<ObservationRow>(`
    SELECT * FROM ${observationsTable}
    WHERE aggregation_completed_at IS NULL
    ORDER BY created_at, id
    LIMIT ?
  `, [boundedLimit(limit)])).map(normalizedObservationRow)
  let processedRows: ObservationRow[] = []
  await stats.transaction(async (tx) => {
    if (lease.scheduledLease) await pinScheduledJobLeaseInTransaction(tx, lease.scheduledLease)
    processedRows = await recordObservationReceipts(tx, rows)
    for (const row of processedRows) {
      await upsertSource(tx, row)
      await upsertWindow(tx, row)
      await upsertTokenRound(tx, row)
      await upsertIdentitySourceFeature(tx, row)
    }
    const touchedIdentityScopes = await refreshIdentityBaselines(tx, processedRows)
    await enqueueIdentityLatestDirtyAccounts(tx, touchedIdentityScopes)
    const dirtyAccounts = await listModelTrustLatestDirtyAccounts(tx, maximumDirtyAccountsPerBatch)
    const affected = mergeAccountModelKeys(uniqueAccountModels(processedRows.filter(isDiagnosticTrustObservation)), dirtyAccounts)
    for (const key of affected) {
      await refreshLatestResult(tx, key, processedRows)
    }
    const tokenRows = processedRows.filter(isValidTokenObservation)
    const tokenContexts = await refreshTokenInterceptBaselines(tx, tokenRows.map((row) => ({
      cohortKeyHmac: row.cohort_key_hmac,
      requestedModel: row.requested_model,
      tokenizerVersion: row.tokenizer_version,
      probeSetVersion: row.probe_set_version
    })))
    const tokenAffected = mergeAccountModelKeys(
      uniqueAccountModels(tokenRows)
    )
    for (const key of tokenAffected) {
      await refreshLatestResult(tx, key, rows, tokenContexts)
    }
    await deleteModelTrustLatestDirtyAccounts(tx, dirtyAccounts)
    if (lease.legacyOwnerId) await assertAggregationLeaseOwner(tx, lease.legacyOwnerId)
    const last = rows.at(-1)
    if (last) await writeAggregationState(tx, last.created_at, last.id)
  })
  await markObservationsAggregationCompleted(dataset, rows)
  await deleteObservationReceipts(stats, rows)
  return processedRows.length
}

async function recordObservationReceipts(client: DatabaseClient, rows: ObservationRow[]): Promise<ObservationRow[]> {
  if (!rows.length) return []
  const table = client.dialect.qualifyTable('juhe_stats', 'model_trust_observation_receipts')
  const processedAt = nowIso()
  const values = rows.map(() => '(?, ?, ?)').join(', ')
  const inserted = await client.query<{ observation_id: string }>(`
    INSERT INTO ${table} (observation_id, observation_created_at, processed_at)
    VALUES ${values}
    ON CONFLICT (observation_id) DO NOTHING
    RETURNING observation_id
  `, rows.flatMap((row) => [row.id, row.created_at, processedAt]))
  const insertedIds = new Set(inserted.map((row) => row.observation_id))
  return rows.filter((row) => insertedIds.has(row.id))
}

async function markObservationsAggregationCompleted(client: DatabaseClient, rows: ObservationRow[]): Promise<void> {
  if (!rows.length) return
  const table = client.dialect.qualifyTable('juhe_dataset', 'model_check_observations')
  await client.execute(`
    UPDATE ${table}
    SET aggregation_completed_at = ?
    WHERE aggregation_completed_at IS NULL
      AND id IN (${rows.map(() => '?').join(', ')})
  `, [nowIso(), ...rows.map((row) => row.id)])
}

async function deleteObservationReceipts(client: DatabaseClient, rows: ObservationRow[]): Promise<void> {
  if (!rows.length) return
  const table = client.dialect.qualifyTable('juhe_stats', 'model_trust_observation_receipts')
  await client.execute(`
    DELETE FROM ${table}
    WHERE observation_id IN (${rows.map(() => '?').join(', ')})
  `, rows.map((row) => row.id))
}

async function cleanupCompletedObservationReceipts(dataset: DatabaseClient, stats: DatabaseClient, limit: number): Promise<void> {
  const receiptsTable = stats.dialect.qualifyTable('juhe_stats', 'model_trust_observation_receipts')
  const receipts = await stats.query<{ observation_id: string }>(`
    SELECT observation_id FROM ${receiptsTable}
    ORDER BY processed_at, observation_id
    LIMIT ?
  `, [boundedLimit(limit)])
  if (!receipts.length) return
  const observationsTable = dataset.dialect.qualifyTable('juhe_dataset', 'model_check_observations')
  const completed = await dataset.query<{ id: string }>(`
    SELECT id FROM ${observationsTable}
    WHERE aggregation_completed_at IS NOT NULL
      AND id IN (${receipts.map(() => '?').join(', ')})
  `, receipts.map((row) => row.observation_id))
  if (!completed.length) return
  await stats.execute(`
    DELETE FROM ${receiptsTable}
    WHERE observation_id IN (${completed.map(() => '?').join(', ')})
  `, completed.map((row) => row.id))
}

export async function activateModelTokenInterceptBaselineAsync(input: TokenInterceptScope & {
  baselineVersion: number
  strongThresholdIntercept: number
  calibrationNote: string
}): Promise<void> {
  await activateTokenInterceptBaselineVersion(await statsClient(), input)
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
    identityObservationCount: Number(row.identity_observation_count ?? 0),
    pairedProbeCount: Number(row.paired_probe_count ?? 0),
    slope: optionalNumber(row.slope),
    intercept: optionalNumber(row.intercept),
    interceptBaselineMedian: optionalNumber(row.intercept_baseline_median),
    interceptBaselineMad: optionalNumber(row.intercept_baseline_mad),
    interceptBaselineVersion: optionalNumber(row.intercept_baseline_version),
    interceptBaselineStatus: optionalText(row.intercept_baseline_status),
    interceptStrongGateEnabled: Number(row.intercept_strong_gate_enabled ?? 0) === 1,
    identityDistance: optionalNumber(row.identity_distance),
    pairedDistance: optionalNumber(row.paired_distance),
    pairedBaselineMedian: optionalNumber(row.paired_baseline_median),
    pairedBaselineMad: optionalNumber(row.paired_baseline_mad),
    baselineVersion: optionalNumber(row.baseline_version),
    baselineVersionStatus: optionalText(row.baseline_version_status),
    featureVersion: optionalText(row.feature_version),
    tokenizerVersion: optionalText(row.tokenizer_version),
    probeSetVersion: optionalText(row.probe_set_version),
    reasonCodes: parseReasonCodes(row.reason_codes_json),
    lastObservedAt: optionalInstant(row.last_observed_at, 'model_account_trust_results.last_observed_at')
  }
}

async function upsertSource(client: DatabaseClient, row: ObservationRow): Promise<void> {
  if (!isValidTokenObservation(row)) return
  const table = client.dialect.qualifyTable('juhe_stats', 'model_trust_window_sources')
  await client.execute(`
    INSERT INTO ${table} (
      system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac, first_observed_at,
      last_observed_at, observation_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)
    ON CONFLICT (system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac) DO UPDATE SET
      last_observed_at = excluded.last_observed_at,
      observation_count = model_trust_window_sources.observation_count + 1,
      updated_at = excluded.updated_at
  `, [row.system_account_id, row.account_id, row.cohort_key_hmac, row.mapped_upstream_model, row.upstream_bucket_hmac, row.created_at, row.created_at, nowIso()])
}

async function upsertWindow(client: DatabaseClient, row: ObservationRow): Promise<void> {
  if (!isValidTokenObservation(row)) return
  const table = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_windows')
  const local = row.local_input_tokens
  const reported = Number(row.reported_input_tokens)
  await client.execute(`
    INSERT INTO ${table} (
      system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version,
      probe_set_version, observation_count, valid_sample_count, round_count, sum_local,
      sum_reported, sum_local_squared, sum_local_reported, sum_reported_squared,
      bucket_aligned_count, first_observed_at, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 1, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version, probe_set_version) DO UPDATE SET
      observation_count = model_token_integrity_windows.observation_count + 1,
      valid_sample_count = model_token_integrity_windows.valid_sample_count + excluded.valid_sample_count,
      sum_local = model_token_integrity_windows.sum_local + excluded.sum_local,
      sum_reported = model_token_integrity_windows.sum_reported + excluded.sum_reported,
      sum_local_squared = model_token_integrity_windows.sum_local_squared + excluded.sum_local_squared,
      sum_local_reported = model_token_integrity_windows.sum_local_reported + excluded.sum_local_reported,
      sum_reported_squared = model_token_integrity_windows.sum_reported_squared + excluded.sum_reported_squared,
      bucket_aligned_count = model_token_integrity_windows.bucket_aligned_count + excluded.bucket_aligned_count,
      last_observed_at = excluded.last_observed_at,
      updated_at = excluded.updated_at
  `, [
    row.system_account_id, row.account_id, row.requested_model, row.cohort_key_hmac,
    row.tokenizer_version, row.probe_set_version, 1, local, reported,
    local * local, local * reported, reported * reported,
    row.padding_tokens > 0 && reported % 64 === 0 ? 1 : 0,
    row.created_at, row.created_at, nowIso()
  ])
}

async function upsertTokenRound(client: DatabaseClient, row: ObservationRow): Promise<void> {
  if (!isValidTokenObservation(row)) return
  const table = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_rounds')
  const paddingMask = tokenPaddingMask(row.padding_tokens)
  await client.execute(`
    INSERT INTO ${table} (
      system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version,
      probe_set_version, run_id, round_index, valid_sample_count, padding_mask,
      first_observed_at, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)
    ON CONFLICT (
      system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version,
      probe_set_version, run_id, round_index
    ) DO UPDATE SET
      valid_sample_count = model_token_integrity_rounds.valid_sample_count + 1,
      padding_mask = model_token_integrity_rounds.padding_mask | excluded.padding_mask,
      last_observed_at = excluded.last_observed_at,
      updated_at = excluded.updated_at
  `, [
    row.system_account_id, row.account_id, row.requested_model, row.cohort_key_hmac,
    row.tokenizer_version, row.probe_set_version, row.run_id, row.round_index, paddingMask,
    row.created_at, row.created_at, nowIso()
  ])
}

async function refreshLatestResult(
  client: DatabaseClient,
  key: AccountModelKey,
  batchRows: ObservationRow[],
  tokenContexts?: Map<string, TokenInterceptEvaluationContext>
): Promise<void> {
  const windows = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_windows')
  const rounds = client.dialect.qualifyTable('juhe_stats', 'model_token_integrity_rounds')
  const sources = client.dialect.qualifyTable('juhe_stats', 'model_trust_window_sources')
  const latest = client.dialect.qualifyTable('juhe_stats', 'model_account_trust_results')
  const currentLatest = await client.one<Record<string, unknown>>(`
    SELECT * FROM ${latest}
    WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
    LIMIT 1
  `, [key.systemAccountId, key.accountId, key.requestedModel])
  const windowRow = await client.one<WindowRow & { cohort_key_hmac: string; tokenizer_version: string; probe_set_version: string }>(`
    SELECT * FROM ${windows}
    WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
    ORDER BY last_observed_at DESC LIMIT 1
  `, [key.systemAccountId, key.accountId, key.requestedModel])
  const window = windowRow ? normalizedWindowRow(windowRow) : undefined
  const identity = await evaluateIdentityTrust(client, key)
  const representative = [...batchRows].reverse().find((row) => (
    isDiagnosticTrustObservation(row)
    && row.system_account_id === key.systemAccountId
    && row.account_id === key.accountId
    && row.requested_model === key.requestedModel
  ))
  if (!window && !identity && !representative) return
  const sourceRow = window ? await client.one<{ source_count: number }>(`
    SELECT COUNT(DISTINCT upstream_bucket_hmac) AS source_count FROM ${sources}
    WHERE cohort_key_hmac = ?
  `, [window.cohort_key_hmac]) : undefined
  const sourceCount = Number(sourceRow?.source_count ?? 0)
  const regression = window ? regressionFromWindow(window) : undefined
  const validSampleCount = Number(window?.valid_sample_count ?? 0)
  const roundRow = window ? await client.one<{ round_count: number }>(`
    SELECT COUNT(*) AS round_count FROM ${rounds}
    WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
      AND cohort_key_hmac = ? AND tokenizer_version = ? AND probe_set_version = ?
      AND (padding_mask & 7) = 7
  `, [
    key.systemAccountId, key.accountId, key.requestedModel, window.cohort_key_hmac,
    window.tokenizer_version, window.probe_set_version
  ]) : undefined
  const roundCount = Number(roundRow?.round_count ?? 0)
  const durationDays = window ? durationDaysBetween(window.first_observed_at, window.last_observed_at, 'model_token_integrity_windows') : 0
  const tokenEvidenceStatus = sourceCount >= 10 && validSampleCount >= 300 && roundCount >= 100 && durationDays >= 14
    ? 'stable'
    : sourceCount >= 5 && validSampleCount >= 100 && roundCount >= 34 && durationDays >= 7
      ? 'candidate'
      : sourceCount >= 3 && validSampleCount >= 30 && roundCount >= 10 && durationDays >= 3
        ? 'bootstrap'
        : 'insufficient'
  const interceptScope = window ? {
        cohortKeyHmac: window.cohort_key_hmac,
        requestedModel: key.requestedModel,
        tokenizerVersion: window.tokenizer_version,
        probeSetVersion: window.probe_set_version
      } : undefined
  const interceptContext = interceptScope ? tokenContexts?.get(tokenInterceptScopeKey(interceptScope)) : undefined
  const interceptBaseline: TokenInterceptBaselineEvaluation = window && regression && interceptContext
    ? evaluateTokenInterceptBaseline(interceptContext, key, regression.intercept)
    : {
        baselineVersion: optionalNumber(currentLatest?.intercept_baseline_version),
        baselineStatus: (optionalText(currentLatest?.intercept_baseline_status) as TokenInterceptBaselineEvaluation['baselineStatus'] | undefined) ?? 'unavailable',
        evidenceStatus: 'insufficient',
        looMedian: optionalNumber(currentLatest?.intercept_baseline_median),
        looMad: optionalNumber(currentLatest?.intercept_baseline_mad),
        strongGateEnabled: Number(currentLatest?.intercept_strong_gate_enabled ?? 0) === 1,
        suspectedFixedPadding: false
      }
  const slopeUsage = window && regression
    ? tokenStatusFromWindow(window, regression.slope, regression.confidenceLow, regression.confidenceHigh, roundCount)
    : { status: 'insufficient_evidence', reasonCodes: [] }
  const currentReasonCodes = parseReasonCodes(currentLatest?.reason_codes_json)
  const preservedFixedReasons = interceptContext
    ? []
    : currentReasonCodes.filter((code) => code === 'fixed_intercept_padding' || code === 'fixed_intercept_calibration_pending')
  const usage = interceptBaseline.suspectedFixedPadding
    ? { status: 'suspected_padding', reasonCodes: [...slopeUsage.reasonCodes, 'fixed_intercept_padding'] }
    : !interceptContext && currentLatest
      ? { status: optionalText(currentLatest.usage_integrity_status) ?? slopeUsage.status, reasonCodes: [...slopeUsage.reasonCodes, ...preservedFixedReasons] }
      : slopeUsage
  if (window && regression) {
    await client.execute(`
      UPDATE ${windows}
      SET round_count = ?, slope = ?, intercept = ?, usage_integrity_status = ?, updated_at = ?
      WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
        AND cohort_key_hmac = ? AND tokenizer_version = ? AND probe_set_version = ?
    `, [
      roundCount, regression.slope, regression.intercept, slopeUsage.status, nowIso(),
      key.systemAccountId, key.accountId, key.requestedModel, window.cohort_key_hmac,
      window.tokenizer_version, window.probe_set_version
    ])
  }
  const mappingStatus = representative?.mapping_status ?? optionalText(currentLatest?.mapping_status) ?? 'unknown'
  const protocolStatus = representative?.protocol_status ?? optionalText(currentLatest?.protocol_status) ?? 'insufficient_evidence'
  const reasonCodes = [
    ...usage.reasonCodes,
    ...(interceptBaseline.baselineStatus === 'calibration_pending' ? ['fixed_intercept_calibration_pending'] : []),
    ...(identity?.reasonCodes ?? []),
    ...(mappingStatus === 'configured_mapping' ? ['configured_model_mapping'] : []),
    ...(mappingStatus === 'undeclared_mismatch' ? ['undeclared_response_model_mismatch'] : []),
    ...(protocolStatus === 'failed' ? ['protocol_check_failed'] : [])
  ]
  const identityObservationCount = identity?.identityObservationCount ?? Number(currentLatest?.identity_observation_count ?? 0)
  const identitySourceCount = identity?.independentSourceCount ?? Number(currentLatest?.independent_source_count ?? 0)
  const pairedProbeCount = identity?.pairedProbeCount ?? Number(currentLatest?.paired_probe_count ?? 0)
  const identityCoverage = identity ? (Math.min(identity.identityObservationCount, 9) / 9) * 25 + (Math.min(identity.independentSourceCount, 3) / 3) * 25 : 0
  const tokenCoverage = window ? (Math.min(roundCount, 3) / 3) * 25 + (Math.min(sourceCount, 3) / 3) * 25 : 0
  const evidenceCoverage = Math.min(100, Math.round(identityCoverage + tokenCoverage))
  const evidenceStatus = identity?.baselineVersionStatus === 'drift_protected'
    ? 'insufficient'
    : strongerEvidenceStatus(identity?.evidenceStatus, tokenEvidenceStatus)
  const lastObservedAt = latestInstant([
    window?.last_observed_at,
    identity?.lastObservedAt,
    representative?.created_at
  ], '模型可信 latest lastObservedAt')
  await client.execute(`
    INSERT INTO ${latest} (
      system_account_id, account_id, requested_model, identity_status, mapping_status,
      usage_integrity_status, protocol_status, evidence_status, evidence_coverage,
      observation_count, round_count, independent_source_count, identity_observation_count, paired_probe_count,
      slope, intercept, intercept_baseline_median, intercept_baseline_mad, intercept_baseline_version,
      intercept_baseline_status, intercept_strong_gate_enabled,
      identity_distance, paired_distance, paired_baseline_median, paired_baseline_mad,
      baseline_version, baseline_version_status, feature_version,
      tokenizer_version, probe_set_version, reason_codes_json, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
      identity_observation_count = excluded.identity_observation_count,
      paired_probe_count = excluded.paired_probe_count,
      slope = excluded.slope,
      intercept = excluded.intercept,
      intercept_baseline_median = excluded.intercept_baseline_median,
      intercept_baseline_mad = excluded.intercept_baseline_mad,
      intercept_baseline_version = excluded.intercept_baseline_version,
      intercept_baseline_status = excluded.intercept_baseline_status,
      intercept_strong_gate_enabled = excluded.intercept_strong_gate_enabled,
      identity_distance = excluded.identity_distance,
      paired_distance = excluded.paired_distance,
      paired_baseline_median = excluded.paired_baseline_median,
      paired_baseline_mad = excluded.paired_baseline_mad,
      baseline_version = excluded.baseline_version,
      baseline_version_status = excluded.baseline_version_status,
      feature_version = excluded.feature_version,
      tokenizer_version = excluded.tokenizer_version,
      probe_set_version = excluded.probe_set_version,
      reason_codes_json = excluded.reason_codes_json,
      last_observed_at = excluded.last_observed_at,
      updated_at = excluded.updated_at
  `, [
    key.systemAccountId, key.accountId, key.requestedModel,
    identity?.identityStatus ?? representative?.identity_status ?? optionalText(currentLatest?.identity_status) ?? 'insufficient_evidence',
    mappingStatus, usage.status, protocolStatus, evidenceStatus, evidenceCoverage,
    validSampleCount + identityObservationCount, roundCount,
    Math.max(sourceCount, identitySourceCount), identityObservationCount, pairedProbeCount,
    regression?.slope ?? null, regression?.intercept ?? null,
    interceptBaseline.looMedian ?? null, interceptBaseline.looMad ?? null,
    interceptBaseline.baselineVersion ?? null, interceptBaseline.baselineStatus,
    interceptBaseline.strongGateEnabled ? 1 : 0, identity?.identityDistance ?? optionalNumber(currentLatest?.identity_distance) ?? null,
    identity?.pairedDistance ?? optionalNumber(currentLatest?.paired_distance) ?? null,
    identity?.pairedBaselineMedian ?? optionalNumber(currentLatest?.paired_baseline_median) ?? null,
    identity?.pairedBaselineMad ?? optionalNumber(currentLatest?.paired_baseline_mad) ?? null,
    identity?.baselineVersion ?? optionalNumber(currentLatest?.baseline_version) ?? null,
    identity?.baselineVersionStatus ?? optionalText(currentLatest?.baseline_version_status) ?? null,
    identity?.featureVersion ?? optionalText(currentLatest?.feature_version) ?? null,
    window?.tokenizer_version ?? null, window?.probe_set_version ?? representative?.probe_set_version ?? null,
    JSON.stringify([...new Set(reasonCodes)]), lastObservedAt ?? null, nowIso()
  ])
}

function strongerEvidenceStatus(identityStatus: string | undefined, tokenStatus: string): string {
  const rank = new Map([['insufficient', 0], ['bootstrap', 1], ['candidate', 2], ['stable', 3]])
  return (rank.get(identityStatus ?? 'insufficient') ?? 0) >= (rank.get(tokenStatus) ?? 0) ? identityStatus ?? 'insufficient' : tokenStatus
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

function isValidTokenObservation(row: ObservationRow): boolean {
  return row.probe_family === 'token_input_differential'
    && row.observation_status === 'observed'
    && Boolean(row.observed_model?.trim())
    && !hasHardTrustConflict(row)
    && row.reported_input_tokens !== null
    && Number.isFinite(Number(row.reported_input_tokens))
    && tokenPaddingMask(row.padding_tokens) !== 0
}

function isValidTrustObservation(row: ObservationRow): boolean {
  return isValidTokenObservation(row) || isIdentityObservation(row)
}

function isDiagnosticTrustObservation(row: ObservationRow): boolean {
  return isValidTrustObservation(row) || (
    (row.probe_family === 'token_input_differential' || row.probe_family.startsWith('identity_'))
    && row.observation_status === 'observed'
    && Boolean(row.observed_model?.trim())
    && hasHardTrustConflict(row)
  )
}

function tokenPaddingMask(paddingTokens: number): number {
  if (paddingTokens === 0) return 1
  if (paddingTokens === 512) return 2
  if (paddingTokens === 2048) return 4
  return 0
}

async function readAggregationState(client: DatabaseClient): Promise<{ createdAt: string; id: string }> {
  const table = client.dialect.qualifyTable('juhe_stats', 'stats_job_state')
  const row = await client.one<{ cursor_created_at?: string | null; cursor_id?: string | null }>(`
    SELECT cursor_created_at, cursor_id FROM ${table}
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `, [aggregationJobName])
  const createdAt = row?.cursor_created_at === null || row?.cursor_created_at === undefined
    ? ''
    : requiredRfc3339Instant(row.cursor_created_at, 'stats_job_state.cursor_created_at')
  return { createdAt, id: row?.cursor_id ?? '' }
}

async function enqueueIdentityLatestDirtyAccounts(client: DatabaseClient, scopes: IdentityPopulationScope[]): Promise<void> {
  if (!scopes.length) return
  const sources = client.dialect.qualifyTable('juhe_stats', 'model_identity_source_features')
  const dirty = client.dialect.qualifyTable('juhe_stats', 'model_trust_latest_dirty_accounts')
  const updatedAt = nowIso()
  for (const scope of scopes) {
    await client.execute(`
      INSERT INTO ${dirty} (system_account_id, account_id, requested_model, dirty_reason, updated_at)
      SELECT DISTINCT system_account_id, account_id, requested_model, 'identity_baseline_changed', ?
      FROM ${sources}
      WHERE population_key_hmac = ? AND requested_model = ? AND feature_version = ?
      ON CONFLICT (system_account_id, account_id, requested_model) DO NOTHING
    `, [updatedAt, scope.populationKey, scope.requestedModel, scope.featureVersion])
  }
}

async function listModelTrustLatestDirtyAccounts(client: DatabaseClient, limit: number): Promise<AccountModelKey[]> {
  const table = client.dialect.qualifyTable('juhe_stats', 'model_trust_latest_dirty_accounts')
  const rows = await client.query<{ system_account_id: string; account_id: string; requested_model: string }>(`
    SELECT system_account_id, account_id, requested_model FROM ${table}
    ORDER BY updated_at, system_account_id, account_id, requested_model
    LIMIT ?
  `, [limit])
  return rows.map((row) => ({
    systemAccountId: row.system_account_id,
    accountId: row.account_id,
    requestedModel: row.requested_model
  }))
}

async function deleteModelTrustLatestDirtyAccounts(client: DatabaseClient, keys: AccountModelKey[]): Promise<void> {
  if (!keys.length) return
  const table = client.dialect.qualifyTable('juhe_stats', 'model_trust_latest_dirty_accounts')
  for (let index = 0; index < keys.length; index += 100) {
    const batch = keys.slice(index, index + 100)
    const tuples = batch.map(() => '(?, ?, ?)').join(', ')
    await client.execute(`
      DELETE FROM ${table}
      WHERE (system_account_id, account_id, requested_model) IN (${tuples})
    `, batch.flatMap((key) => [key.systemAccountId, key.accountId, key.requestedModel]))
  }
}

async function assertAggregationLeaseOwner(client: DatabaseClient, ownerId: string): Promise<void> {
  const table = client.dialect.qualifyTable('juhe_stats', 'background_job_leases')
  const lease = await client.one<{ owner_id: string }>(`
    SELECT owner_id FROM ${table}
    WHERE lease_key = ? AND owner_id = ? AND lease_until > ?
    LIMIT 1
  `, [aggregationLeaseKey, ownerId, nowIso()])
  if (!lease) throw new Error('模型可信聚合租约已失效，当前事务已回滚并等待新 owner 重试')
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
    upstream_bucket_hmac: boundedHmac(input.upstreamBucketHmac), cohort_key_hmac: boundedHmac(input.cohortKeyHmac), population_key_hmac: boundedHmac(input.populationKeyHmac), probe_key_hmac: boundedHmac(input.probeKeyHmac),
    system_fingerprint_hmac: input.systemFingerprintHmac ? boundedHmac(input.systemFingerprintHmac) : null,
    probe_family: boundedText(input.probeFamily), probe_set_version: boundedText(input.probeSetVersion), tokenizer_version: boundedText(input.tokenizerVersion), feature_version: boundedText(input.featureVersion),
    round_index: nonNegativeInteger(input.roundIndex), padding_tokens: nonNegativeInteger(input.paddingTokens), local_input_tokens: nonNegativeInteger(input.localInputTokens),
    reported_input_tokens: optionalNonNegativeInteger(input.reportedInputTokens), cached_input_tokens: optionalNonNegativeInteger(input.cachedInputTokens),
    constraint_passed: input.constraintPassed === undefined ? null : input.constraintPassed ? 1 : 0,
    ...normalizedFeatureVector(input.featureVector),
    observation_status: boundedText(input.observationStatus), identity_status: boundedText(input.identityStatus), mapping_status: boundedText(input.mappingStatus),
    protocol_status: boundedText(input.protocolStatus), evidence_coverage: Math.min(100, nonNegativeInteger(input.evidenceCoverage)), trace_id: optionalBoundedText(input.traceId),
    created_at: input.createdAt === undefined ? nowIso() : requiredRfc3339Instant(input.createdAt, '模型可信 observation createdAt')
  }
}

function normalizedObservationRow(row: ObservationRow): ObservationRow {
  return {
    ...row,
    created_at: requiredRfc3339Instant(row.created_at, 'model_check_observations.created_at')
  }
}

function normalizedWindowRow(row: WindowRow & { cohort_key_hmac: string; tokenizer_version: string; probe_set_version: string }): WindowRow & { cohort_key_hmac: string; tokenizer_version: string; probe_set_version: string } {
  return {
    ...row,
    first_observed_at: requiredRfc3339Instant(row.first_observed_at, 'model_token_integrity_windows.first_observed_at'),
    last_observed_at: requiredRfc3339Instant(row.last_observed_at, 'model_token_integrity_windows.last_observed_at')
  }
}

function optionalInstant(value: unknown, label: string): string | undefined {
  return value === null || value === undefined ? undefined : requiredRfc3339Instant(value, label)
}

function requiredInstantMilliseconds(value: unknown, label: string): number {
  const normalized = requiredRfc3339Instant(value, label)
  const milliseconds = rfc3339InstantMilliseconds(normalized)
  if (milliseconds === undefined) throw new Error(`${label}解析后不是有效的 RFC3339 时间`)
  return milliseconds
}

function durationDaysBetween(first: string, last: string, label: string): number {
  const firstMilliseconds = requiredInstantMilliseconds(first, `${label}.first_observed_at`)
  const lastMilliseconds = requiredInstantMilliseconds(last, `${label}.last_observed_at`)
  return Math.max(1, Math.ceil((lastMilliseconds - firstMilliseconds) / 86_400_000) + 1)
}

function latestInstant(values: Array<string | undefined>, label: string): string | undefined {
  let latest: string | undefined
  let latestMilliseconds: number | undefined
  for (const value of values) {
    if (value === undefined) continue
    const milliseconds = requiredInstantMilliseconds(value, label)
    if (latestMilliseconds === undefined || milliseconds > latestMilliseconds) {
      latest = requiredRfc3339Instant(value, label)
      latestMilliseconds = milliseconds
    }
  }
  return latest
}

function normalizedFeatureVector(values?: number[]): Record<string, number | null> {
  return Object.fromEntries(Array.from({ length: 8 }, (_, index) => {
    const value = values?.[index]
    return [`feature_${index + 1}`, value === undefined || !Number.isFinite(value) ? null : Math.max(0, Math.min(1, value))]
  }))
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

function mergeAccountModelKeys(...groups: AccountModelKey[][]): AccountModelKey[] {
  const map = new Map<string, AccountModelKey>()
  for (const group of groups) {
    for (const key of group) map.set(`${key.systemAccountId}\u0000${key.accountId}\u0000${key.requestedModel}`, key)
  }
  return [...map.values()]
}

function boundedLimit(value: number): number { return Math.max(1, Math.min(maximumObservationsPerTransaction, Math.trunc(value) || maximumObservationsPerTransaction)) }
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
