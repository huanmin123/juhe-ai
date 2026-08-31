import type {
  ModelCheckItemStatus,
  ModelCheckItemSummary,
  ModelCheckLevel,
  ModelCheckProfile,
  ModelCheckRunDetail,
  ModelCheckRunListItem,
  ModelCheckRunListResult,
  ModelCheckRunStatus,
  ModelCheckRunSummary,
  ModelCheckTargetType,
  ModelCheckTriggerKind,
  ModelQualityDecision,
  ModelQualityPolicySnapshot
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'
import { buildSystemAccountScopeClause, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { getDatasetDatabase, getStatsDatabase, newId, nowIso, runInDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadAccountNameMap, loadAccountNameMapAsync } from './repository-lookups.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

const defaultModelCheckPageSize = 20
const maxModelCheckPageSize = 100
const maxSummaryStringLength = 500
const maxSummaryArrayLength = 20
const maxSummaryObjectKeys = 32
const maxSummaryDepth = 4
const maximumModelCheckRunsPerAccount = 1000
const modelCheckRunTrimBatchSize = 100
const modelTrustAggregationJobName = 'model-trust-observation-aggregation'

export interface ModelCheckRunCreateInput {
  id?: string
  systemAccountId: string
  actorSystemAccountId: string
  providerCode: string
  targetType: ModelCheckTargetType
  targetId: string
  targetName?: string
  targetOwnerSystemAccountId?: string
  accountId?: string
  groupId?: string
  apiKeyId?: string
  model: string
  profile?: ModelCheckProfile
  triggerKind?: ModelCheckTriggerKind
  scheduleId?: string
  trustedComparison: boolean
  trustedComparisonAvailable?: boolean
  traceId?: string
  probeSetVersion: string
  startedAt?: string
  requestSummary?: unknown
  policySnapshot?: ModelQualityPolicySnapshot
}

export interface ModelCheckRunFinishInput {
  level: ModelCheckLevel
  score: number
  maxScore?: number
  status: Exclude<ModelCheckRunStatus, 'running'>
  message: string
  finishedAt?: string
  durationMs?: number
  resultSummary?: unknown
  errorCode?: string
  errorMessage?: string
}

export type ModelCheckRunFinishResult = {
  applied: true
  id: string
  status: Exclude<ModelCheckRunStatus, 'running'>
  finishedAt: string
  updatedAt: string
} | {
  applied: false
  id: string
}

export interface ModelCheckItemCreateInput {
  id?: string
  itemKey: string
  itemType: string
  status: ModelCheckItemStatus
  score: number
  maxScore: number
  durationMs?: number
  traceId?: string
  evidenceSummary?: unknown
  errorCode?: string
  errorMessage?: string
  createdAt?: string
}

export interface ModelCheckRunListOptions {
  page?: number
  pageSize?: number
  targetType?: ModelCheckTargetType
  targetId?: string
  model?: string
  level?: ModelCheckLevel
  status?: ModelCheckRunStatus
  triggerKind?: ModelCheckTriggerKind
  startAt?: string
  endAt?: string
}

interface NormalizedModelCheckRunListOptions {
  page: number
  pageSize: number
  targetType?: ModelCheckTargetType
  targetId?: string
  model?: string
  level?: ModelCheckLevel
  status?: ModelCheckRunStatus
  triggerKind?: ModelCheckTriggerKind
  startAt?: string
  endAt?: string
}

interface ModelCheckRunRow {
  id: string
  system_account_id: string
  actor_system_account_id: string
  provider_code: string
  target_type: ModelCheckTargetType
  target_id: string
  target_name: string | null
  target_owner_system_account_id: string | null
  account_id: string | null
  group_id: string | null
  api_key_id: string | null
  model: string
  profile: ModelCheckProfile
  trigger_kind: ModelCheckTriggerKind
  schedule_id: string | null
  trusted_comparison_enabled: number
  trusted_comparison_available: number
  level: ModelCheckLevel
  score: number
  max_score: number
  status: ModelCheckRunStatus
  message: string
  trace_id: string | null
  probe_set_version: string
  started_at: string
  finished_at: string | null
  duration_ms: number | null
  request_summary_json: string
  result_summary_json: string
  policy_snapshot_json: string
  quality_decision_json: string
  quality_health_sync_status: ModelQualityDecision['healthSyncResult'] | null
  error_code: string | null
  error_message: string | null
  created_at: string
  updated_at: string
}

interface ModelCheckItemRow {
  id: string
  run_id: string
  item_key: string
  item_type: string
  status: ModelCheckItemStatus
  score: number
  max_score: number
  duration_ms: number | null
  trace_id: string | null
  evidence_summary_json: string
  error_code: string | null
  error_message: string | null
  created_at: string
  updated_at: string
}

export function createModelCheckRun(input: ModelCheckRunCreateInput): ModelCheckRunSummary {
  const now = input.startedAt ?? nowIso()
  const run: ModelCheckRunRow = {
    id: input.id ?? newId('mcr'),
    system_account_id: input.systemAccountId,
    actor_system_account_id: input.actorSystemAccountId,
    provider_code: input.providerCode,
    target_type: input.targetType,
    target_id: input.targetId,
    target_name: input.targetName ?? null,
    target_owner_system_account_id: input.targetOwnerSystemAccountId ?? null,
    account_id: input.accountId ?? null,
    group_id: input.groupId ?? null,
    api_key_id: input.apiKeyId ?? null,
    model: input.model,
    profile: input.profile ?? 'quick',
    trigger_kind: input.triggerKind ?? 'manual',
    schedule_id: input.scheduleId ?? null,
    trusted_comparison_enabled: input.trustedComparison ? 1 : 0,
    trusted_comparison_available: input.trustedComparisonAvailable ? 1 : 0,
    level: 'unavailable',
    score: 0,
    max_score: 100,
    status: 'running',
    message: '模型检测运行中',
    trace_id: input.traceId ?? null,
    probe_set_version: input.probeSetVersion,
    started_at: now,
    finished_at: null,
    duration_ms: null,
    request_summary_json: safeJson(input.requestSummary ?? {}),
    result_summary_json: '{}',
    policy_snapshot_json: safeJson(input.policySnapshot ?? {}),
    quality_decision_json: '{}',
    quality_health_sync_status: null,
    error_code: null,
    error_message: null,
    created_at: now,
    updated_at: now
  }
  const database = getDatasetDatabase()
  const insert = database.prepare(`
      INSERT INTO model_check_runs (
        id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, target_name,
        target_owner_system_account_id, account_id, group_id, api_key_id, model, profile, trigger_kind, schedule_id,
        trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message,
        trace_id, probe_set_version, started_at, finished_at, duration_ms, request_summary_json,
        result_summary_json, policy_snapshot_json, quality_decision_json, error_code, error_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
  runInDatabaseTransaction(() => {
    ensureModelCheckRunCapacity(database, run.account_id)
    insert.run(
      run.id,
      run.system_account_id,
      run.actor_system_account_id,
      run.provider_code,
      run.target_type,
      run.target_id,
      run.target_name,
      run.target_owner_system_account_id,
      run.account_id,
      run.group_id,
      run.api_key_id,
      run.model,
      run.profile,
      run.trigger_kind,
      run.schedule_id,
      run.trusted_comparison_enabled,
      run.trusted_comparison_available,
      run.level,
      run.score,
      run.max_score,
      run.status,
      run.message,
      run.trace_id,
      run.probe_set_version,
      run.started_at,
      run.finished_at,
      run.duration_ms,
      run.request_summary_json,
      run.result_summary_json,
      run.policy_snapshot_json,
      run.quality_decision_json,
      run.error_code,
      run.error_message,
      run.created_at,
      run.updated_at
    )
  }, database)
  return modelCheckRunFromRow(run, includeSystemAccountFields())
}

export async function createModelCheckRunAsync(input: ModelCheckRunCreateInput): Promise<ModelCheckRunSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createModelCheckRun(input)
  }
  const client = await modelCheckDatabaseClient()
  const now = input.startedAt ?? nowIso()
  const run: ModelCheckRunRow = {
    id: input.id ?? newId('mcr'),
    system_account_id: input.systemAccountId,
    actor_system_account_id: input.actorSystemAccountId,
    provider_code: input.providerCode,
    target_type: input.targetType,
    target_id: input.targetId,
    target_name: input.targetName ?? null,
    target_owner_system_account_id: input.targetOwnerSystemAccountId ?? null,
    account_id: input.accountId ?? null,
    group_id: input.groupId ?? null,
    api_key_id: input.apiKeyId ?? null,
    model: input.model,
    profile: input.profile ?? 'quick',
    trigger_kind: input.triggerKind ?? 'manual',
    schedule_id: input.scheduleId ?? null,
    trusted_comparison_enabled: input.trustedComparison ? 1 : 0,
    trusted_comparison_available: input.trustedComparisonAvailable ? 1 : 0,
    level: 'unavailable',
    score: 0,
    max_score: 100,
    status: 'running',
    message: '模型检测运行中',
    trace_id: input.traceId ?? null,
    probe_set_version: input.probeSetVersion,
    started_at: now,
    finished_at: null,
    duration_ms: null,
    request_summary_json: safeJson(input.requestSummary ?? {}),
    result_summary_json: '{}',
    policy_snapshot_json: safeJson(input.policySnapshot ?? {}),
    quality_decision_json: '{}',
    quality_health_sync_status: null,
    error_code: null,
    error_message: null,
    created_at: now,
    updated_at: now
  }
  await client.transaction(async (tx) => {
    await ensureModelCheckRunCapacityAsync(tx, run.account_id)
    await tx.execute(`
    INSERT INTO ${modelCheckTable(tx, 'model_check_runs')} (
      id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, target_name,
      target_owner_system_account_id, account_id, group_id, api_key_id, model, profile, trigger_kind, schedule_id,
      trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message,
      trace_id, probe_set_version, started_at, finished_at, duration_ms, request_summary_json,
      result_summary_json, policy_snapshot_json, quality_decision_json, error_code, error_message, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [
    run.id,
    run.system_account_id,
    run.actor_system_account_id,
    run.provider_code,
    run.target_type,
    run.target_id,
    run.target_name,
    run.target_owner_system_account_id,
    run.account_id,
    run.group_id,
    run.api_key_id,
    run.model,
    run.profile,
    run.trigger_kind,
    run.schedule_id,
    run.trusted_comparison_enabled,
    run.trusted_comparison_available,
    run.level,
    run.score,
    run.max_score,
    run.status,
    run.message,
    run.trace_id,
    run.probe_set_version,
    run.started_at,
    run.finished_at,
    run.duration_ms,
    run.request_summary_json,
    run.result_summary_json,
    run.policy_snapshot_json,
    run.quality_decision_json,
    run.error_code,
    run.error_message,
    run.created_at,
      run.updated_at
    ])
  })
  return modelCheckRunFromRow(run, includeSystemAccountFields())
}

export function createModelCheckItems(runId: string, items: ModelCheckItemCreateInput[]): ModelCheckItemSummary[] {
  if (!items.length) return []
  const database = getDatasetDatabase()
  const insert = database.prepare(`
    INSERT INTO model_check_items (
      id, run_id, item_key, item_type, status, score, max_score, duration_ms, trace_id,
      evidence_summary_json, error_code, error_message, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const rows = items.map((item) => {
    const now = item.createdAt ?? nowIso()
    return {
      id: item.id ?? newId('mci'),
      run_id: runId,
      item_key: item.itemKey,
      item_type: item.itemType,
      status: item.status,
      score: Math.max(0, Math.trunc(item.score)),
      max_score: Math.max(0, Math.trunc(item.maxScore)),
      duration_ms: typeof item.durationMs === 'number' ? Math.max(0, Math.trunc(item.durationMs)) : null,
      trace_id: item.traceId ?? null,
      evidence_summary_json: safeJson(item.evidenceSummary ?? {}),
      error_code: item.errorCode ?? null,
      error_message: sanitizeSummaryString(item.errorMessage ?? '') ?? null,
      created_at: now,
      updated_at: now
    } satisfies ModelCheckItemRow
  })
  runInDatabaseTransaction(() => {
    for (const row of rows) {
      insert.run(
        row.id,
        row.run_id,
        row.item_key,
        row.item_type,
        row.status,
        row.score,
        row.max_score,
        row.duration_ms,
        row.trace_id,
        row.evidence_summary_json,
        row.error_code,
        row.error_message,
        row.created_at,
        row.updated_at
      )
    }
  }, database)
  return rows.map(modelCheckItemFromRow)
}

export async function createModelCheckItemsAsync(runId: string, items: ModelCheckItemCreateInput[]): Promise<ModelCheckItemSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createModelCheckItems(runId, items)
  }
  if (!items.length) return []
  const client = await modelCheckDatabaseClient()
  const rows = items.map((item) => {
    const now = item.createdAt ?? nowIso()
    return {
      id: item.id ?? newId('mci'),
      run_id: runId,
      item_key: item.itemKey,
      item_type: item.itemType,
      status: item.status,
      score: Math.max(0, Math.trunc(item.score)),
      max_score: Math.max(0, Math.trunc(item.maxScore)),
      duration_ms: typeof item.durationMs === 'number' ? Math.max(0, Math.trunc(item.durationMs)) : null,
      trace_id: item.traceId ?? null,
      evidence_summary_json: safeJson(item.evidenceSummary ?? {}),
      error_code: item.errorCode ?? null,
      error_message: sanitizeSummaryString(item.errorMessage ?? '') ?? null,
      created_at: now,
      updated_at: now
    } satisfies ModelCheckItemRow
  })
  await client.transaction(async (tx) => {
    for (const row of rows) {
      await tx.execute(`
        INSERT INTO ${modelCheckTable(tx, 'model_check_items')} (
          id, run_id, item_key, item_type, status, score, max_score, duration_ms, trace_id,
          evidence_summary_json, error_code, error_message, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `, [
        row.id,
        row.run_id,
        row.item_key,
        row.item_type,
        row.status,
        row.score,
        row.max_score,
        row.duration_ms,
        row.trace_id,
        row.evidence_summary_json,
        row.error_code,
        row.error_message,
        row.created_at,
        row.updated_at
      ])
    }
  })
  return rows.map(modelCheckItemFromRow)
}

export function finishModelCheckRun(runId: string, input: ModelCheckRunFinishInput): ModelCheckRunFinishResult {
  const finishedAt = input.finishedAt ?? nowIso()
  const updatedAt = nowIso()
  const result = getDatasetDatabase()
    .prepare(`
      UPDATE model_check_runs
      SET level = ?, score = ?, max_score = ?, status = ?, message = ?, finished_at = ?,
        duration_ms = ?, result_summary_json = ?, error_code = ?, error_message = ?, updated_at = ?
      WHERE id = ? AND status = 'running'
    `)
    .run(
      input.level,
      Math.max(0, Math.trunc(input.score)),
      Math.max(0, Math.trunc(input.maxScore ?? 100)),
      input.status,
      sanitizeSummaryString(input.message) ?? '',
      finishedAt,
      typeof input.durationMs === 'number' ? Math.max(0, Math.trunc(input.durationMs)) : null,
      safeJson(input.resultSummary ?? {}),
      input.errorCode ?? null,
      sanitizeSummaryString(input.errorMessage ?? '') ?? null,
      updatedAt,
      runId
    )
  return result.changes > 0
    ? { applied: true, id: runId, status: input.status, finishedAt, updatedAt }
    : { applied: false, id: runId }
}

export async function finishModelCheckRunAsync(runId: string, input: ModelCheckRunFinishInput): Promise<ModelCheckRunFinishResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return finishModelCheckRun(runId, input)
  }
  const client = await modelCheckDatabaseClient()
  const finishedAt = input.finishedAt ?? nowIso()
  const updatedAt = nowIso()
  const result = await client.execute(`
    UPDATE ${modelCheckTable(client, 'model_check_runs')}
    SET level = ?, score = ?, max_score = ?, status = ?, message = ?, finished_at = ?,
      duration_ms = ?, result_summary_json = ?, error_code = ?, error_message = ?, updated_at = ?
    WHERE id = ? AND status = 'running'
  `, [
    input.level,
    Math.max(0, Math.trunc(input.score)),
    Math.max(0, Math.trunc(input.maxScore ?? 100)),
    input.status,
    sanitizeSummaryString(input.message) ?? '',
    finishedAt,
    typeof input.durationMs === 'number' ? Math.max(0, Math.trunc(input.durationMs)) : null,
    safeJson(input.resultSummary ?? {}),
    input.errorCode ?? null,
    sanitizeSummaryString(input.errorMessage ?? '') ?? null,
    updatedAt,
    runId
  ])
  return result.changes > 0
    ? { applied: true, id: runId, status: input.status, finishedAt, updatedAt }
    : { applied: false, id: runId }
}

export function updateModelCheckQualityDecision(runId: string, decision: ModelQualityDecision): ModelCheckRunSummary | undefined {
  const result = getDatasetDatabase().prepare(`
    UPDATE model_check_runs
    SET quality_decision_json = ?, quality_health_sync_status = ?, updated_at = ?
    WHERE id = ?
  `).run(safeJson(decision), decision.healthSyncResult ?? null, nowIso(), runId)
  return result.changes > 0 ? findModelCheckRun(runId) : undefined
}

export async function updateModelCheckQualityDecisionAsync(runId: string, decision: ModelQualityDecision): Promise<ModelCheckRunSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') return updateModelCheckQualityDecision(runId, decision)
  const client = await modelCheckDatabaseClient()
  const result = await client.execute(`
    UPDATE ${modelCheckTable(client, 'model_check_runs')}
    SET quality_decision_json = ?, quality_health_sync_status = ?, updated_at = ?
    WHERE id = ?
  `, [safeJson(decision), decision.healthSyncResult ?? null, nowIso(), runId])
  return result.changes > 0 ? findModelCheckRunAsync(runId) : undefined
}

export async function listFailedModelQualityHealthSyncRunsAsync(limit = 20): Promise<ModelCheckRunSummary[]> {
  const boundedLimit = boundedInteger(limit, 20, 1, 100)
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const rows = getDatasetDatabase().prepare(`
      SELECT * FROM model_check_runs
      WHERE account_id IS NOT NULL
        AND status = 'completed'
        AND quality_health_sync_status = 'failed'
      ORDER BY updated_at ASC, id ASC
      LIMIT ?
    `).all(boundedLimit) as unknown as ModelCheckRunRow[]
    return rows.map((row) => modelCheckRunFromRow(row, true))
  }
  const client = await modelCheckDatabaseClient()
  const rows = await client.query<ModelCheckRunRow>(`
    SELECT * FROM ${modelCheckTable(client, 'model_check_runs')}
    WHERE account_id IS NOT NULL
      AND status = 'completed'
      AND quality_health_sync_status = 'failed'
    ORDER BY updated_at ASC, id ASC
    LIMIT ?
  `, [boundedLimit])
  return rows.map((row) => modelCheckRunFromRow(row, true))
}

export function listModelCheckRuns(access?: AccessScope, options: ModelCheckRunListOptions = {}): ModelCheckRunListResult {
  const normalized = normalizeListOptions(options)
  const filters = buildModelCheckRunFilters(access, normalized)
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT ${modelCheckRunListSelectColumns('mcr')}
      FROM model_check_runs mcr
      ${filters.clause}
      ORDER BY mcr.created_at DESC, mcr.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize) as unknown as ModelCheckRunRow[]
  const pageRows = takePageRows(rows, normalized.pageSize)
  const accountNames = loadModelCheckTargetNameMap(pageRows.rows)
  const items = pageRows.rows.map((row) => modelCheckRunListItemFromRow(row, includeSystemAccountFields(access), accountNames))
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export function getModelCheckRunDetail(runId: string, access?: AccessScope): ModelCheckRunDetail | undefined {
  const scope = buildSystemAccountScopeClause(access, 'mcr.system_account_id')
  const row = getDatasetDatabase()
    .prepare(`
      SELECT *
      FROM model_check_runs mcr
      WHERE mcr.id = ?
      ${scope.clause}
      LIMIT 1
    `)
    .get(runId, ...scope.params) as unknown as ModelCheckRunRow | undefined
  if (!row) return undefined
  const accountNames = loadModelCheckTargetNameMap([row])
  const checks = getDatasetDatabase()
    .prepare(`
      SELECT *
      FROM model_check_items
      WHERE run_id = ?
      ORDER BY created_at ASC, id ASC
    `)
    .all(runId) as unknown as ModelCheckItemRow[]
  const run = modelCheckRunFromRow(row, includeSystemAccountFields(access), accountNames)
  return {
    ...run,
    requestSummary: run.requestSummary ?? {},
    resultSummary: run.resultSummary ?? {},
    checks: checks.map(modelCheckItemFromRow)
  }
}

export async function getModelCheckRunDetailAsync(runId: string, access?: AccessScope): Promise<ModelCheckRunDetail | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_model_check_run_detail_read_only',
      runId,
      access
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getModelCheckRunDetail(runId, access)
  }
  const client = await modelCheckDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'mcr.system_account_id')
  const row = await client.one<ModelCheckRunRow>(`
    SELECT *
    FROM ${modelCheckTable(client, 'model_check_runs')} mcr
    WHERE mcr.id = ?
    ${scope.clause}
    LIMIT 1
  `, [runId, ...scope.params])
  if (!row) return undefined
  const accountNames = await loadModelCheckTargetNameMapAsync(client, [row])
  const checks = await client.query<ModelCheckItemRow>(`
    SELECT *
    FROM ${modelCheckTable(client, 'model_check_items')}
    WHERE run_id = ?
    ORDER BY created_at ASC, id ASC
  `, [runId])
  const run = modelCheckRunFromRow(row, includeSystemAccountFields(access), accountNames)
  return {
    ...run,
    requestSummary: run.requestSummary ?? {},
    resultSummary: run.resultSummary ?? {},
    checks: checks.map(modelCheckItemFromRow)
  }
}

export async function listModelCheckRunsAsync(access?: AccessScope, options: ModelCheckRunListOptions = {}): Promise<ModelCheckRunListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_model_check_runs_read_only',
      access,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listModelCheckRuns(access, options)
  }
  const client = await modelCheckDatabaseClient()
  const normalized = normalizeListOptions(options)
  const filters = buildModelCheckRunFilters(access, normalized)
  const rows = await client.query<ModelCheckRunRow>(`
    SELECT ${modelCheckRunListSelectColumns('mcr')}
    FROM ${modelCheckTable(client, 'model_check_runs')} mcr
    ${filters.clause}
    ORDER BY mcr.created_at DESC, mcr.id DESC
    LIMIT ? OFFSET ?
  `, [...filters.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize])
  const pageRows = takePageRows(rows, normalized.pageSize)
  const accountNames = await loadModelCheckTargetNameMapAsync(client, pageRows.rows)
  const items = pageRows.rows.map((row) => modelCheckRunListItemFromRow(row, includeSystemAccountFields(access), accountNames))
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

function ensureModelCheckRunCapacity(database: ReturnType<typeof getDatasetDatabase>, accountId: string | null): void {
  if (!accountId) return
  let count = modelCheckRunCount(database, accountId)
  while (count >= maximumModelCheckRunsPerAccount) {
    trimOldestModelCheckRunBatch(database, accountId)
    const nextCount = modelCheckRunCount(database, accountId)
    if (nextCount >= count) throwModelCheckRetentionBlocked(accountId, '清理批次未减少日志数量')
    count = nextCount
  }
}

async function ensureModelCheckRunCapacityAsync(client: DatabaseClient, accountId: string | null): Promise<void> {
  if (!accountId) return
  if (client.driver === 'postgres') {
    await client.query('SELECT pg_advisory_xact_lock(hashtext(?))', [`model-check-retention:${accountId}`])
  }
  const runs = modelCheckTable(client, 'model_check_runs')
  const observations = modelCheckTable(client, 'model_check_observations')
  let count = await modelCheckRunCountAsync(client, runs, accountId)
  while (count >= maximumModelCheckRunsPerAccount) {
    await trimOldestModelCheckRunBatchAsync(client, runs, observations, accountId)
    const nextCount = await modelCheckRunCountAsync(client, runs, accountId)
    if (nextCount >= count) throwModelCheckRetentionBlocked(accountId, '清理批次未减少日志数量')
    count = nextCount
  }
}

function modelCheckRunCount(database: ReturnType<typeof getDatasetDatabase>, accountId: string): number {
  return Number((database.prepare('SELECT COUNT(*) AS count FROM model_check_runs WHERE account_id = ?').get(accountId) as { count?: number } | undefined)?.count ?? 0)
}

function trimOldestModelCheckRunBatch(database: ReturnType<typeof getDatasetDatabase>, accountId: string): void {
  const candidates = database.prepare(`
    SELECT id FROM model_check_runs
    WHERE account_id = ?
      AND status <> 'running'
      AND (quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied')
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `).all(accountId, modelCheckRunTrimBatchSize) as unknown as Array<{ id: string }>
  if (candidates.length < modelCheckRunTrimBatchSize) {
    throwModelCheckRetentionBlocked(accountId, '可清理的已结束运行不足 100 条，暂缓清理')
  }
  const ids = candidates.map((row) => row.id)
  const cursor = getStatsDatabase().prepare(`
    SELECT cursor_created_at, cursor_id FROM stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
    LIMIT 1
  `).get(modelTrustAggregationJobName) as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
  const observation = database.prepare(`
    SELECT created_at, id FROM model_check_observations
    WHERE run_id IN (${sqlPlaceholders(ids.length)})
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `).get(...ids) as { created_at: string; id: string } | undefined
  if (observation && !cursorHasConsumed(cursor, observation)) {
    throwModelCheckRetentionBlocked(accountId, '最老 100 条运行仍有 observation 尚未被统计聚合消费')
  }
  database.prepare(`
    DELETE FROM model_check_runs
    WHERE id IN (${sqlPlaceholders(ids.length)})
      AND account_id = ?
      AND status <> 'running'
      AND (quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied')
  `).run(...ids, accountId)
}

async function modelCheckRunCountAsync(client: DatabaseClient, runs: string, accountId: string): Promise<number> {
  const countRow = await client.one<{ count: number }>(`SELECT COUNT(*) AS count FROM ${runs} WHERE account_id = ?`, [accountId])
  return Number(countRow?.count ?? 0)
}

async function trimOldestModelCheckRunBatchAsync(client: DatabaseClient, runs: string, observations: string, accountId: string): Promise<void> {
  const candidates = await client.query<{ id: string }>(`
    SELECT id FROM ${runs}
    WHERE account_id = ?
      AND status <> 'running'
      AND (quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied')
    ORDER BY created_at ASC, id ASC
    LIMIT ?
    FOR UPDATE
  `, [accountId, modelCheckRunTrimBatchSize])
  if (candidates.length < modelCheckRunTrimBatchSize) {
    throwModelCheckRetentionBlocked(accountId, '可清理的已结束运行不足 100 条，暂缓清理')
  }
  const ids = candidates.map((row) => row.id)
  const cursor = await client.one<{ cursor_created_at?: string | null; cursor_id?: string | null }>(`
    SELECT cursor_created_at, cursor_id FROM ${client.dialect.qualifyTable('juhe_stats', 'stats_job_state')}
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
    LIMIT 1
  `, [modelTrustAggregationJobName])
  const observation = await client.one<{ created_at: string; id: string }>(`
    SELECT created_at, id FROM ${observations}
    WHERE run_id IN (${client.dialect.bindPlaceholders(ids.length)})
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `, ids)
  if (observation && !cursorHasConsumed(cursor, observation)) {
    throwModelCheckRetentionBlocked(accountId, '最老 100 条运行仍有 observation 尚未被统计聚合消费')
  }
  await client.execute(`
    DELETE FROM ${runs}
    WHERE id IN (${client.dialect.bindPlaceholders(ids.length)})
      AND account_id = ?
      AND status <> 'running'
      AND (quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied')
  `, [...ids, accountId])
}

function cursorHasConsumed(
  cursor: { cursor_created_at?: string | null; cursor_id?: string | null } | undefined,
  observation: { created_at: string; id: string }
): boolean {
  const cursorCreatedAt = cursor?.cursor_created_at ?? ''
  const cursorId = cursor?.cursor_id ?? ''
  return cursorCreatedAt > observation.created_at
    || (cursorCreatedAt === observation.created_at && cursorId >= observation.id)
}

function throwModelCheckRetentionBlocked(accountId: string, reason: string): never {
  logger.warn({ event: 'model_check_account_retention_blocked', accountId, maximumRuns: maximumModelCheckRunsPerAccount, trimBatchSize: modelCheckRunTrimBatchSize, reason }, '模型检测账户日志达到上限但暂不能安全清理')
  throw new Error(`账户模型质量测试日志已达到 ${maximumModelCheckRunsPerAccount} 条；${reason}，请等待后台聚合后重试`)
}

function findModelCheckRun(runId: string): ModelCheckRunSummary | undefined {
  const row = getDatasetDatabase()
    .prepare('SELECT * FROM model_check_runs WHERE id = ? LIMIT 1')
    .get(runId) as unknown as ModelCheckRunRow | undefined
  return row ? modelCheckRunFromRow(row, includeSystemAccountFields(), loadModelCheckTargetNameMap([row])) : undefined
}

async function findModelCheckRunAsync(runId: string): Promise<ModelCheckRunSummary | undefined> {
  const client = await modelCheckDatabaseClient()
  const row = await client.one<ModelCheckRunRow>(`
    SELECT *
    FROM ${modelCheckTable(client, 'model_check_runs')}
    WHERE id = ?
    LIMIT 1
  `, [runId])
  return row ? modelCheckRunFromRow(row, includeSystemAccountFields(), await loadModelCheckTargetNameMapAsync(client, [row])) : undefined
}

function buildModelCheckRunFilters(access: AccessScope | undefined, options: NormalizedModelCheckRunListOptions): { clause: string; params: Array<string | number | null> } {
  const clauses: string[] = []
  const params: Array<string | number | null> = []
  const scope = buildSystemAccountScopeClause(access, 'mcr.system_account_id')
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^\s*AND\s+/, ''))
    params.push(...scope.params)
  }
  if (options.targetType) {
    clauses.push('mcr.target_type = ?')
    params.push(options.targetType)
  }
  if (options.targetId) {
    clauses.push('mcr.target_id = ?')
    params.push(options.targetId)
  }
  if (options.model) {
    clauses.push('mcr.model = ?')
    params.push(options.model)
  }
  if (options.level) {
    clauses.push('mcr.level = ?')
    params.push(options.level)
  }
  if (options.status) {
    clauses.push('mcr.status = ?')
    params.push(options.status)
  }
  if (options.triggerKind) {
    clauses.push('mcr.trigger_kind = ?')
    params.push(options.triggerKind)
  }
  if (options.startAt) {
    clauses.push('mcr.created_at >= ?')
    params.push(options.startAt)
  }
  if (options.endAt) {
    clauses.push('mcr.created_at <= ?')
    params.push(options.endAt)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function normalizeListOptions(options: ModelCheckRunListOptions): NormalizedModelCheckRunListOptions {
  const pageSize = boundedInteger(options.pageSize, defaultModelCheckPageSize, 1, maxModelCheckPageSize)
  return {
    page: boundedInteger(options.page, 1, 1, Number.MAX_SAFE_INTEGER),
    pageSize,
    targetType: isTargetType(options.targetType) ? options.targetType : undefined,
    targetId: trimText(options.targetId),
    model: trimText(options.model),
    level: isLevel(options.level) ? options.level : undefined,
    status: isStatus(options.status) ? options.status : undefined,
    triggerKind: isTriggerKind(options.triggerKind) ? options.triggerKind : undefined,
    startAt: trimText(options.startAt),
    endAt: trimText(options.endAt)
  }
}

function loadModelCheckTargetNameMap(rows: ModelCheckRunRow[]): Map<string, string> {
  return loadAccountNameMap(rows.map((row) => row.account_id ?? row.target_id))
}

async function loadModelCheckTargetNameMapAsync(client: DatabaseClient, rows: ModelCheckRunRow[]): Promise<Map<string, string>> {
  return loadAccountNameMapAsync(client, rows.map((row) => row.account_id ?? row.target_id))
}

async function modelCheckDatabaseClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function modelCheckTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_dataset', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function modelCheckRunListSelectColumns(alias: string): string {
  const prefix = `${alias}.`
  return [
    'id',
    'system_account_id',
    'provider_code',
    'target_type',
    'target_id',
    'target_name',
    'account_id',
    'model',
    'profile',
    'trigger_kind',
    'trusted_comparison_enabled',
    'level',
    'score',
    'max_score',
    'status',
    'message',
    'duration_ms',
    'error_message',
    'created_at'
  ].map((column) => `${prefix}${column}`).join(', ')
}

function modelCheckRunListItemFromRow(
  row: ModelCheckRunRow,
  showSystemAccountFields: boolean,
  accountNames: Map<string, string>
): ModelCheckRunListItem {
  const targetName = row.target_name?.trim() || accountNames.get(row.account_id ?? row.target_id)
  return {
    id: row.id,
    systemAccountId: showSystemAccountFields ? row.system_account_id : undefined,
    providerCode: row.provider_code,
    targetType: row.target_type,
    targetId: row.target_id,
    targetName: targetName ?? undefined,
    model: row.model,
    profile: row.profile,
    triggerKind: row.trigger_kind,
    trustedComparison: row.trusted_comparison_enabled === 1,
    level: row.level,
    score: Number(row.score ?? 0),
    maxScore: Number(row.max_score ?? 100),
    status: row.status,
    message: row.message,
    durationMs: typeof row.duration_ms === 'number' ? row.duration_ms : undefined,
    errorMessage: row.error_message ?? undefined,
    createdAt: row.created_at
  }
}

function modelCheckRunFromRow(
  row: ModelCheckRunRow,
  showSystemAccountFields: boolean,
  accountNames: Map<string, string> = new Map(),
  options: { includeSummaries?: boolean } = { includeSummaries: true }
): ModelCheckRunSummary {
  const targetName = row.target_name?.trim() || accountNames.get(row.account_id ?? row.target_id)
  return {
    id: row.id,
    systemAccountId: showSystemAccountFields ? row.system_account_id : undefined,
    actorSystemAccountId: showSystemAccountFields ? row.actor_system_account_id : undefined,
    providerCode: row.provider_code,
    targetType: row.target_type,
    targetId: row.target_id,
    targetName: targetName ?? undefined,
    targetOwnerSystemAccountId: showSystemAccountFields ? row.target_owner_system_account_id ?? undefined : undefined,
    accountId: row.account_id ?? undefined,
    groupId: row.group_id ?? undefined,
    apiKeyId: row.api_key_id ?? undefined,
    model: row.model,
    profile: row.profile,
    triggerKind: row.trigger_kind,
    scheduleId: row.schedule_id ?? undefined,
    trustedComparison: row.trusted_comparison_enabled === 1,
    trustedComparisonAvailable: row.trusted_comparison_available === 1,
    level: row.level,
    score: Number(row.score ?? 0),
    maxScore: Number(row.max_score ?? 100),
    status: row.status,
    message: row.message,
    traceId: row.trace_id ?? undefined,
    probeSetVersion: row.probe_set_version,
    startedAt: row.started_at,
    finishedAt: row.finished_at ?? undefined,
    durationMs: typeof row.duration_ms === 'number' ? row.duration_ms : undefined,
    ...(options.includeSummaries === false ? {} : {
      requestSummary: parseJsonRecord(row.request_summary_json),
      resultSummary: parseJsonRecord(row.result_summary_json),
      policySnapshot: parseJsonRecord(row.policy_snapshot_json) as unknown as ModelQualityPolicySnapshot,
      qualityDecision: parseOptionalJsonRecord(row.quality_decision_json) as ModelQualityDecision | undefined
    }),
    errorCode: row.error_code ?? undefined,
    errorMessage: row.error_message ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function modelCheckItemFromRow(row: ModelCheckItemRow): ModelCheckItemSummary {
  return {
    id: row.id,
    runId: row.run_id,
    itemKey: row.item_key,
    itemType: row.item_type,
    status: row.status,
    score: Number(row.score ?? 0),
    maxScore: Number(row.max_score ?? 0),
    durationMs: typeof row.duration_ms === 'number' ? row.duration_ms : undefined,
    traceId: row.trace_id ?? undefined,
    evidenceSummary: parseJsonRecord(row.evidence_summary_json),
    errorCode: row.error_code ?? undefined,
    errorMessage: row.error_message ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function safeJson(value: unknown): string {
  return JSON.stringify(sanitizeSummaryValue(value, 0))
}

function sanitizeSummaryValue(value: unknown, depth: number): unknown {
  if (depth >= maxSummaryDepth) return '[truncated]'
  if (value === null || value === undefined) return value
  if (typeof value === 'string') return sanitizeSummaryString(value)
  if (typeof value === 'number' || typeof value === 'boolean') return value
  if (Array.isArray(value)) {
    return value.slice(0, maxSummaryArrayLength).map((item) => sanitizeSummaryValue(item, depth + 1))
  }
  if (typeof value !== 'object') return String(value)
  const output: Record<string, unknown> = {}
  for (const [key, item] of Object.entries(value as Record<string, unknown>).slice(0, maxSummaryObjectKeys)) {
    output[key] = isSensitiveSummaryKey(key) ? '[redacted]' : sanitizeSummaryValue(item, depth + 1)
  }
  return output
}

function isSensitiveSummaryKey(key: string): boolean {
  return /(authorization|proxy-authorization|cookie|set-cookie|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|rawbody|raw_body|fullresponse|full_response)/i.test(key)
}

function parseJsonRecord(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function parseOptionalJsonRecord(text: string): Record<string, unknown> | undefined {
  const value = parseJsonRecord(text)
  return Object.keys(value).length ? value : undefined
}

function boundedString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  if (!text) return undefined
  return text.length > maxSummaryStringLength ? `${text.slice(0, maxSummaryStringLength)}...` : text
}

function sanitizeSummaryString(value: string): string | undefined {
  const text = boundedString(value)
  if (!text) return undefined
  return text
    .replace(/\bsk-[A-Za-z0-9][A-Za-z0-9_-]{6,}\b/g, '[redacted]')
    .replace(/\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{8,}/gi, '$1 [redacted]')
    .replace(/(https?:\/\/[^/\s:@]+:)[^@\s/]+@/gi, '$1[redacted]@')
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number): number {
  const numeric = typeof value === 'number' ? value : typeof value === 'string' ? Number.parseInt(value, 10) : NaN
  if (!Number.isFinite(numeric)) return fallback
  return Math.max(min, Math.min(max, Math.trunc(numeric)))
}

function trimText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isTargetType(value: unknown): value is ModelCheckTargetType {
  return value === 'account'
}

function isLevel(value: unknown): value is ModelCheckLevel {
  return value === 'high_confidence' || value === 'likely' || value === 'uncertain' || value === 'suspicious' || value === 'unavailable'
}

function isStatus(value: unknown): value is ModelCheckRunStatus {
  return value === 'running' || value === 'completed' || value === 'failed' || value === 'canceled'
}

function isTriggerKind(value: unknown): value is ModelCheckTriggerKind {
  return value === 'manual' || value === 'scheduled' || value === 'quality_recovery'
}
