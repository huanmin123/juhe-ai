import type {
  ModelCheckItemStatus,
  ModelCheckItemSummary,
  ModelCheckLevel,
  ModelCheckProfile,
  ModelCheckRunDetail,
  ModelCheckRunListResult,
  ModelCheckRunStatus,
  ModelCheckRunSummary,
  ModelCheckTargetType
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { buildSystemAccountScopeClause, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { getDatasetDatabase, newId, nowIso, runInDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound, takePageRows } from './query-utils.js'
import { loadAccountNameMap, loadAccountNameMapAsync } from './repository-lookups.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

const defaultModelCheckPageSize = 20
const maxModelCheckPageSize = 100
const maxModelCheckListWindowRows = 1001
const maxSummaryStringLength = 500
const maxSummaryArrayLength = 20
const maxSummaryObjectKeys = 32
const maxSummaryDepth = 4

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
  trustedComparison: boolean
  trustedComparisonAvailable?: boolean
  traceId?: string
  probeSetVersion: string
  startedAt?: string
  requestSummary?: unknown
}

export interface ModelCheckRunFinishInput {
  level: ModelCheckLevel
  score: number
  maxScore?: number
  status: ModelCheckRunStatus
  message: string
  finishedAt?: string
  durationMs?: number
  resultSummary?: unknown
  errorCode?: string
  errorMessage?: string
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
    error_code: null,
    error_message: null,
    created_at: now,
    updated_at: now
  }
  getDatasetDatabase()
    .prepare(`
      INSERT INTO model_check_runs (
        id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, target_name,
        target_owner_system_account_id, account_id, group_id, api_key_id, model, profile,
        trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message,
        trace_id, probe_set_version, started_at, finished_at, duration_ms, request_summary_json,
        result_summary_json, error_code, error_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
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
      run.error_code,
      run.error_message,
      run.created_at,
      run.updated_at
    )
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
    error_code: null,
    error_message: null,
    created_at: now,
    updated_at: now
  }
  await client.execute(`
    INSERT INTO ${modelCheckTable(client, 'model_check_runs')} (
      id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, target_name,
      target_owner_system_account_id, account_id, group_id, api_key_id, model, profile,
      trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message,
      trace_id, probe_set_version, started_at, finished_at, duration_ms, request_summary_json,
      result_summary_json, error_code, error_message, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
    run.error_code,
    run.error_message,
    run.created_at,
    run.updated_at
  ])
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

export function finishModelCheckRun(runId: string, input: ModelCheckRunFinishInput): ModelCheckRunSummary | undefined {
  const finishedAt = input.finishedAt ?? nowIso()
  const updatedAt = nowIso()
  const result = getDatasetDatabase()
    .prepare(`
      UPDATE model_check_runs
      SET level = ?, score = ?, max_score = ?, status = ?, message = ?, finished_at = ?,
        duration_ms = ?, result_summary_json = ?, error_code = ?, error_message = ?, updated_at = ?
      WHERE id = ?
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
  return result.changes > 0 ? findModelCheckRun(runId) : undefined
}

export async function finishModelCheckRunAsync(runId: string, input: ModelCheckRunFinishInput): Promise<ModelCheckRunSummary | undefined> {
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
    WHERE id = ?
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
  return result.changes > 0 ? findModelCheckRunAsync(runId) : undefined
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
  const items = pageRows.rows.map((row) => modelCheckRunFromRow(row, includeSystemAccountFields(access), accountNames, { includeSummaries: false }))
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
  const items = pageRows.rows.map((row) => modelCheckRunFromRow(row, includeSystemAccountFields(access), accountNames, { includeSummaries: false }))
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
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
  const maxPage = Math.max(1, Math.floor((maxModelCheckListWindowRows - 1) / pageSize))
  return {
    page: boundedInteger(options.page, 1, 1, maxPage),
    pageSize,
    targetType: isTargetType(options.targetType) ? options.targetType : undefined,
    targetId: trimText(options.targetId),
    model: trimText(options.model),
    level: isLevel(options.level) ? options.level : undefined,
    status: isStatus(options.status) ? options.status : undefined,
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
    'actor_system_account_id',
    'provider_code',
    'target_type',
    'target_id',
    'target_name',
    'target_owner_system_account_id',
    'account_id',
    'group_id',
    'api_key_id',
    'model',
    'profile',
    'trusted_comparison_enabled',
    'trusted_comparison_available',
    'level',
    'score',
    'max_score',
    'status',
    'message',
    'trace_id',
    'probe_set_version',
    'started_at',
    'finished_at',
    'duration_ms',
    'error_code',
    'error_message',
    'created_at',
    'updated_at'
  ].map((column) => `${prefix}${column}`).concat([
    "'{}' AS request_summary_json",
    "'{}' AS result_summary_json"
  ]).join(', ')
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
      resultSummary: parseJsonRecord(row.result_summary_json)
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
