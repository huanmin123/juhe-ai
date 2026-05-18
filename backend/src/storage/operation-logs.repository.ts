import { beginDatabaseTransaction, commitDatabaseTransaction, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, compatiblePagedTotal, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadSystemAccountsByIds } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

export type OperationLogActorRole = 'admin' | 'user'
export type OperationLogMode = 'self' | 'admin'
export type OperationLogDetailLevel = 'full' | 'summary'
export type OperationLogVisibilityScope = 'targeted' | 'all_users' | 'admin_only'
export type OperationLogTargetRelation = 'primary' | 'affected' | 'created' | 'deleted' | 'owner' | 'grantee' | 'team_member' | 'bound_resource'
export type OperationLogVisibilityReason =
  | 'actor_self'
  | 'resource_owner'
  | 'admin_managed_my_resource'
  | 'authorization_owner'
  | 'authorization_grantee'
  | 'team_member'
  | 'team_authorization'
  | 'global_affected'
  | 'bound_resource_affected'

export interface OperationLogChange {
  field: string
  label: string
  before?: unknown
  after?: unknown
  sensitive?: boolean
}

export interface OperationLogTargetInput {
  targetType: string
  targetId?: string
  targetName?: string
  targetOwnerSystemAccountId?: string
  relation?: OperationLogTargetRelation
}

export interface OperationLogViewerInput {
  systemAccountId: string
  visibilityReason: OperationLogVisibilityReason
  detailLevel?: OperationLogDetailLevel
}

export interface OperationLogInput {
  id?: string
  traceId?: string
  actorSystemAccountId: string
  actorUsername?: string
  actorDisplayName?: string
  actorRole: OperationLogActorRole
  operationScopeSystemAccountId?: string
  mode?: OperationLogMode
  module: string
  action: string
  operationKey: string
  resourceType: string
  resourceId?: string
  resourceName?: string
  summary: string
  detailLevel?: OperationLogDetailLevel
  visibilityScope?: OperationLogVisibilityScope
  changes?: OperationLogChange[]
  metadata?: Record<string, unknown>
  method?: string
  path?: string
  statusCode?: number
  clientIp?: string
  userAgent?: string
  targets?: OperationLogTargetInput[]
  viewers?: OperationLogViewerInput[]
  createdAt?: string
}

export interface OperationLogListOptions {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
  module?: string
  action?: string
  resourceType?: string
  resourceId?: string
  actorSystemAccountId?: string
  affectedSystemAccountId?: string
  operationScopeSystemAccountId?: string
  traceId?: string
  startAt?: string
  endAt?: string
}

export interface OperationLogListResult {
  items: OperationLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface OperationLogTargetSummary {
  id: string
  targetType: string
  targetId?: string
  targetName?: string
  targetOwnerSystemAccountId?: string
  targetOwnerSystemAccountName?: string
  relation: string
  createdAt: string
}

export interface OperationLogViewerSummary {
  systemAccountId: string
  systemAccountName?: string
  visibilityReason: OperationLogVisibilityReason
  detailLevel: OperationLogDetailLevel
  createdAt: string
}

export interface OperationLogSummary {
  id: string
  traceId?: string
  actorSystemAccountId: string
  actorUsername?: string
  actorDisplayName?: string
  actorSystemAccountName?: string
  actorRole: OperationLogActorRole
  operationScopeSystemAccountId?: string
  operationScopeSystemAccountName?: string
  mode: OperationLogMode
  module: string
  action: string
  operationKey: string
  resourceType: string
  resourceId?: string
  resourceName?: string
  summary: string
  detailLevel: OperationLogDetailLevel
  visibilityScope: OperationLogVisibilityScope
  changes: OperationLogChange[]
  metadata: Record<string, unknown>
  method?: string
  path?: string
  statusCode?: number
  clientIp?: string
  userAgent?: string
  createdAt: string
}

export interface OperationLogDetail extends OperationLogSummary {
  targets: OperationLogTargetSummary[]
  viewers: OperationLogViewerSummary[]
}

type OperationLogRow = Record<string, unknown>
type OperationLogFilterValue = string | number
type OperationLogInsertStatement = ReturnType<ReturnType<typeof getRecordDatabase>['prepare']>

interface OperationLogInsertStatements {
  insertLog: OperationLogInsertStatement
  insertTarget: OperationLogInsertStatement
  insertViewer: OperationLogInsertStatement
}

interface PreparedOperationLogInput {
  id: string
  input: OperationLogInput
  createdAt: string
  visibilityScope: OperationLogVisibilityScope
  detailLevel: OperationLogDetailLevel
  targets: OperationLogTargetInput[]
  viewers: OperationLogViewerInput[]
  changesJson: string
  metadataJson: string
}

const operationLogDefaultPageSize = 100
const operationLogMaxPageSize = 100

export function createOperationLog(input: OperationLogInput): OperationLogSummary {
  const database = getRecordDatabase()
  const prepared = prepareOperationLogInput(input)
  const statements = prepareOperationLogInsertStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    insertPreparedOperationLog(statements, prepared)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }

  return operationLogSummaryFromPrepared(prepared)
}

export function createOperationLogsBatch(inputs: OperationLogInput[]): void {
  if (inputs.length === 0) return

  const database = getRecordDatabase()
  const statements = prepareOperationLogInsertStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      insertPreparedOperationLog(statements, prepareOperationLogInput(input))
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export function listOperationLogs(options: OperationLogListOptions = {}): OperationLogListResult {
  const filters = buildOperationLogFilters(options)
  return listOperationLogsWithFilters(filters, options)
}

export function listOperationLogsForViewer(systemAccountId: string, options: OperationLogListOptions = {}): OperationLogListResult {
  return listVisibleOperationLogsForViewer(systemAccountId, options)
}

export function getOperationLogDetail(id: string): OperationLogDetail | undefined {
  return getOperationLogDetailWithClause('ol.id = ?', [id])
}

export function getOperationLogDetailForViewer(id: string, systemAccountId: string): OperationLogDetail | undefined {
  const detail = getOperationLogDetailWithClause(`
    ol.id = ?
    AND (
      ol.visibility_scope = 'all_users'
      OR (
        ol.visibility_scope = 'targeted'
        AND EXISTS (
          SELECT 1 FROM operation_log_viewers olv
          WHERE olv.operation_log_id = ol.id AND olv.system_account_id = ?
        )
      )
    )
  `, [id, systemAccountId])
  if (!detail) return undefined

  const viewerLevel = loadViewerDetailLevels([detail.id], systemAccountId).get(detail.id)
  return sanitizeOperationLogDetailForViewer(detail, effectiveViewerDetailLevel(detail.detailLevel, viewerLevel, detail.visibilityScope))
}

export function cleanupOperationLogsBefore(cutoffCreatedAt: string, limit?: number): number {
  const database = getRecordDatabase()
  if (!limit) {
    const result = database
      .prepare('DELETE FROM operation_logs WHERE created_at < ?')
      .run(cutoffCreatedAt)
    return Number(result.changes ?? 0)
  }

  const rows = database
    .prepare('SELECT id FROM operation_logs WHERE created_at < ? ORDER BY created_at ASC, id ASC LIMIT ?')
    .all(cutoffCreatedAt, Math.max(1, Math.trunc(limit))) as OperationLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = sqlPlaceholders(ids.length)
  const result = database.prepare(`DELETE FROM operation_logs WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

function listOperationLogsWithFilters(filters: { clause: string; params: OperationLogFilterValue[] }, options: OperationLogListOptions, viewerSystemAccountId?: string): OperationLogListResult {
  const pageSize = normalizeOperationLogPageSize(options.pageSize ?? options.limit)
  const page = normalizeOperationLogPage(options.page)
  const offset = (page - 1) * pageSize
  const database = getRecordDatabase()
  const rows = database
    .prepare(`
      SELECT ol.*
      FROM operation_logs ol
      ${filters.clause}
      ORDER BY ol.created_at DESC, ol.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as OperationLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const systemAccountNames = loadSystemAccountNames(pageRows.rows.flatMap((row) => [
    optionalString(row.actor_system_account_id),
    optionalString(row.operation_scope_system_account_id)
  ]))
  const viewerDetailLevels = viewerSystemAccountId
    ? loadViewerDetailLevels(pageRows.rows.map((row) => String(row.id)), viewerSystemAccountId)
    : new Map<string, OperationLogDetailLevel>()
  const items = pageRows.rows.map((row) => {
    const summary = operationLogSummaryFromRow(row, systemAccountNames, { includePayload: false })
    if (!viewerSystemAccountId) return summary
    return sanitizeOperationLogSummaryForViewer(summary, effectiveViewerDetailLevel(summary.detailLevel, viewerDetailLevels.get(summary.id), summary.visibilityScope))
  })
  return {
    items,
    total: compatiblePagedTotal(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

function listVisibleOperationLogsForViewer(systemAccountId: string, options: OperationLogListOptions): OperationLogListResult {
  const pageSize = normalizeOperationLogPageSize(options.pageSize ?? options.limit)
  const page = normalizeOperationLogPage(options.page)
  const offset = (page - 1) * pageSize
  const database = getRecordDatabase()
  const commonFilters = buildCommonOperationLogFilters(options)
  const targetedClause = ['ol.visibility_scope = \'targeted\'', ...commonFilters.clauses].join(' AND ')
  const allUsersClause = ['ol.visibility_scope = \'all_users\'', ...commonFilters.clauses].join(' AND ')
  const targetedParams: OperationLogFilterValue[] = [systemAccountId, ...commonFilters.params]
  const allUsersParams = commonFilters.params

  const targetedVisibleJoin = `
    INNER JOIN (
      SELECT DISTINCT operation_log_id
      FROM operation_log_viewers
      WHERE system_account_id = ?
    ) visible ON visible.operation_log_id = ol.id
  `
  const rows = database
    .prepare(`
      SELECT *
      FROM (
        SELECT ol.*
        FROM operation_logs ol
        ${targetedVisibleJoin}
        WHERE ${targetedClause}
        UNION ALL
        SELECT ol.*
        FROM operation_logs ol
        WHERE ${allUsersClause}
      ) visible_logs
      ORDER BY created_at DESC, id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...targetedParams, ...allUsersParams, pageSize + 1, offset) as OperationLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const systemAccountNames = loadSystemAccountNames(pageRows.rows.flatMap((row) => [
    optionalString(row.actor_system_account_id),
    optionalString(row.operation_scope_system_account_id)
  ]))
  const viewerDetailLevels = loadViewerDetailLevels(pageRows.rows.map((row) => String(row.id)), systemAccountId)
  const items = pageRows.rows.map((row) => {
    const summary = operationLogSummaryFromRow(row, systemAccountNames, { includePayload: false })
    return sanitizeOperationLogSummaryForViewer(summary, effectiveViewerDetailLevel(summary.detailLevel, viewerDetailLevels.get(summary.id), summary.visibilityScope))
  })
  return {
    items,
    total: compatiblePagedTotal(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

function getOperationLogDetailWithClause(whereClause: string, params: OperationLogFilterValue[]): OperationLogDetail | undefined {
  const database = getRecordDatabase()
  const row = database
    .prepare(`SELECT ol.* FROM operation_logs ol WHERE ${whereClause} LIMIT 1`)
    .get(...params) as OperationLogRow | undefined
  if (!row) return undefined

  const targetRows = database
    .prepare('SELECT * FROM operation_log_targets WHERE operation_log_id = ? ORDER BY created_at ASC, id ASC')
    .all(String(row.id)) as OperationLogRow[]
  const viewerRows = database
    .prepare('SELECT * FROM operation_log_viewers WHERE operation_log_id = ? ORDER BY created_at ASC, system_account_id ASC')
    .all(String(row.id)) as OperationLogRow[]
  const systemAccountNames = loadSystemAccountNames([
    optionalString(row.actor_system_account_id),
    optionalString(row.operation_scope_system_account_id),
    ...targetRows.map((target) => optionalString(target.target_owner_system_account_id)),
    ...viewerRows.map((viewer) => optionalString(viewer.system_account_id))
  ])
  return {
    ...operationLogSummaryFromRow(row, systemAccountNames),
    targets: targetRows.map((target) => operationLogTargetFromRow(target, systemAccountNames)),
    viewers: viewerRows.map((viewer) => operationLogViewerFromRow(viewer, systemAccountNames))
  }
}

function buildOperationLogFilters(options: OperationLogListOptions): { clause: string; params: OperationLogFilterValue[] } {
  const { clauses, params } = buildCommonOperationLogFilters(options)
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function buildCommonOperationLogFilters(options: OperationLogListOptions): { clauses: string[]; params: OperationLogFilterValue[] } {
  const clauses: string[] = []
  const params: OperationLogFilterValue[] = []
  pushCommonOperationLogFilters(clauses, params, options)
  return { clauses, params }
}

function pushCommonOperationLogFilters(clauses: string[], params: OperationLogFilterValue[], options: OperationLogListOptions): void {
  pushLikeFilter(clauses, params, 'ol.trace_id', options.traceId)
  pushExactFilter(clauses, params, 'ol.module', options.module)
  pushExactFilter(clauses, params, 'ol.action', options.action)
  pushExactFilter(clauses, params, 'ol.resource_type', options.resourceType)
  pushExactFilter(clauses, params, 'ol.resource_id', options.resourceId)
  pushExactFilter(clauses, params, 'ol.actor_system_account_id', options.actorSystemAccountId)
  pushExactFilter(clauses, params, 'ol.operation_scope_system_account_id', options.operationScopeSystemAccountId)

  const affectedSystemAccountId = options.affectedSystemAccountId?.trim()
  if (affectedSystemAccountId) {
    clauses.push(`(
      ol.visibility_scope = 'all_users'
      OR EXISTS (
        SELECT 1 FROM operation_log_viewers affected
        WHERE affected.operation_log_id = ol.id AND affected.system_account_id = ?
      )
    )`)
    params.push(affectedSystemAccountId)
  }

  const keyword = options.keyword?.trim()
  if (keyword) {
    clauses.push('(ol.summary LIKE ? OR ol.resource_name LIKE ? OR ol.resource_id LIKE ? OR ol.actor_display_name LIKE ? OR ol.actor_username LIKE ?)')
    const pattern = `%${keyword}%`
    params.push(pattern, pattern, pattern, pattern, pattern)
  }

  const startAt = options.startAt?.trim()
  if (startAt) {
    clauses.push('ol.created_at >= ?')
    params.push(startAt)
  }
  const endAt = options.endAt?.trim()
  if (endAt) {
    clauses.push('ol.created_at <= ?')
    params.push(endAt)
  }
}

function pushExactFilter(clauses: string[], params: OperationLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text || text === 'all') return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function pushLikeFilter(clauses: string[], params: OperationLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} LIKE ?`)
  params.push(`%${text}%`)
}

function prepareOperationLogInput(input: OperationLogInput): PreparedOperationLogInput {
  const detailLevel = input.detailLevel ?? 'full'
  return {
    id: input.id ?? newId('oplog'),
    input,
    createdAt: input.createdAt ?? nowIso(),
    visibilityScope: input.visibilityScope ?? 'targeted',
    detailLevel,
    targets: normalizeTargets(input),
    viewers: normalizeViewers(input),
    changesJson: safeJsonStringify(input.changes ?? [], '[]'),
    metadataJson: safeJsonStringify(input.metadata ?? {}, '{}')
  }
}

function prepareOperationLogInsertStatements(database: ReturnType<typeof getRecordDatabase>): OperationLogInsertStatements {
  return {
    insertLog: database.prepare(`
      INSERT INTO operation_logs (
        id, trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
        operation_scope_system_account_id, mode, module, action, operation_key, resource_type, resource_id,
        resource_name, summary, detail_level, visibility_scope, changes_json, metadata_json, method, path,
        status_code, client_ip, user_agent, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `),
    insertTarget: database.prepare(`
      INSERT INTO operation_log_targets (
        id, operation_log_id, target_type, target_id, target_name, target_owner_system_account_id, relation, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `),
    insertViewer: database.prepare(`
      INSERT OR IGNORE INTO operation_log_viewers (
        operation_log_id, system_account_id, visibility_reason, detail_level, created_at
      ) VALUES (?, ?, ?, ?, ?)
    `)
  }
}

function insertPreparedOperationLog(statements: OperationLogInsertStatements, prepared: PreparedOperationLogInput): void {
  const input = prepared.input
  statements.insertLog.run(
    prepared.id,
    input.traceId ?? null,
    input.actorSystemAccountId,
    input.actorUsername ?? null,
    input.actorDisplayName ?? null,
    input.actorRole,
    input.operationScopeSystemAccountId ?? null,
    input.mode ?? 'self',
    input.module,
    input.action,
    input.operationKey,
    input.resourceType,
    input.resourceId ?? null,
    input.resourceName ?? null,
    input.summary,
    prepared.detailLevel,
    prepared.visibilityScope,
    prepared.changesJson,
    prepared.metadataJson,
    input.method ?? null,
    input.path ?? null,
    integerOrNull(input.statusCode),
    input.clientIp ?? null,
    input.userAgent ?? null,
    prepared.createdAt
  )

  for (const target of prepared.targets) {
    statements.insertTarget.run(
      newId('optgt'),
      prepared.id,
      target.targetType,
      target.targetId ?? null,
      target.targetName ?? null,
      target.targetOwnerSystemAccountId ?? null,
      target.relation ?? 'affected',
      prepared.createdAt
    )
  }

  for (const viewer of prepared.viewers) {
    statements.insertViewer.run(
      prepared.id,
      viewer.systemAccountId,
      viewer.visibilityReason,
      viewer.detailLevel ?? prepared.detailLevel,
      prepared.createdAt
    )
  }
}

function operationLogSummaryFromPrepared(prepared: PreparedOperationLogInput): OperationLogSummary {
  const input = prepared.input
  return operationLogSummaryFromRow({
    id: prepared.id,
    trace_id: input.traceId,
    actor_system_account_id: input.actorSystemAccountId,
    actor_username: input.actorUsername,
    actor_display_name: input.actorDisplayName,
    actor_role: input.actorRole,
    operation_scope_system_account_id: input.operationScopeSystemAccountId,
    mode: input.mode ?? 'self',
    module: input.module,
    action: input.action,
    operation_key: input.operationKey,
    resource_type: input.resourceType,
    resource_id: input.resourceId,
    resource_name: input.resourceName,
    summary: input.summary,
    detail_level: prepared.detailLevel,
    visibility_scope: prepared.visibilityScope,
    changes_json: prepared.changesJson,
    metadata_json: prepared.metadataJson,
    method: input.method,
    path: input.path,
    status_code: input.statusCode,
    client_ip: input.clientIp,
    user_agent: input.userAgent,
    created_at: prepared.createdAt
  }, new Map())
}

function operationLogSummaryFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>, options: { includePayload?: boolean } = {}): OperationLogSummary {
  const actorSystemAccountId = String(row.actor_system_account_id)
  const operationScopeSystemAccountId = optionalString(row.operation_scope_system_account_id)
  const includePayload = options.includePayload !== false
  return {
    id: String(row.id),
    traceId: optionalString(row.trace_id),
    actorSystemAccountId,
    actorUsername: optionalString(row.actor_username),
    actorDisplayName: optionalString(row.actor_display_name),
    actorSystemAccountName: systemAccountNames.get(actorSystemAccountId),
    actorRole: String(row.actor_role) as OperationLogActorRole,
    operationScopeSystemAccountId,
    operationScopeSystemAccountName: operationScopeSystemAccountId ? systemAccountNames.get(operationScopeSystemAccountId) : undefined,
    mode: String(row.mode) as OperationLogMode,
    module: String(row.module),
    action: String(row.action),
    operationKey: String(row.operation_key),
    resourceType: String(row.resource_type),
    resourceId: optionalString(row.resource_id),
    resourceName: optionalString(row.resource_name),
    summary: String(row.summary),
    detailLevel: String(row.detail_level) as OperationLogDetailLevel,
    visibilityScope: String(row.visibility_scope) as OperationLogVisibilityScope,
    changes: includePayload ? parseJsonArray(row.changes_json) : [],
    metadata: includePayload ? parseJsonObject(row.metadata_json) : {},
    method: optionalString(row.method),
    path: optionalString(row.path),
    statusCode: numberValue(row.status_code),
    clientIp: optionalString(row.client_ip),
    userAgent: optionalString(row.user_agent),
    createdAt: String(row.created_at)
  }
}

function operationLogTargetFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>): OperationLogTargetSummary {
  const targetOwnerSystemAccountId = optionalString(row.target_owner_system_account_id)
  return {
    id: String(row.id),
    targetType: String(row.target_type),
    targetId: optionalString(row.target_id),
    targetName: optionalString(row.target_name),
    targetOwnerSystemAccountId,
    targetOwnerSystemAccountName: targetOwnerSystemAccountId ? systemAccountNames.get(targetOwnerSystemAccountId) : undefined,
    relation: String(row.relation),
    createdAt: String(row.created_at)
  }
}

function operationLogViewerFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>): OperationLogViewerSummary {
  const systemAccountId = String(row.system_account_id)
  return {
    systemAccountId,
    systemAccountName: systemAccountNames.get(systemAccountId),
    visibilityReason: String(row.visibility_reason) as OperationLogVisibilityReason,
    detailLevel: String(row.detail_level) as OperationLogDetailLevel,
    createdAt: String(row.created_at)
  }
}

function loadViewerDetailLevels(operationLogIds: string[], systemAccountId: string): Map<string, OperationLogDetailLevel> {
  const ids = [...new Set(operationLogIds.filter((id) => id.trim()))]
  if (ids.length === 0) return new Map()

  const rows: OperationLogRow[] = []
  const database = getRecordDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT operation_log_id, detail_level
        FROM operation_log_viewers
        WHERE system_account_id = ? AND operation_log_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(systemAccountId, ...chunk) as OperationLogRow[])
  }
  const levels = new Map<string, OperationLogDetailLevel>()
  for (const row of rows) {
    const id = optionalString(row.operation_log_id)
    if (!id) continue
    const level = row.detail_level === 'summary' ? 'summary' : 'full'
    const current = levels.get(id)
    if (current === 'full') continue
    levels.set(id, level)
  }
  return levels
}

function effectiveViewerDetailLevel(logDetailLevel: OperationLogDetailLevel, viewerDetailLevel?: OperationLogDetailLevel, visibilityScope?: OperationLogVisibilityScope): OperationLogDetailLevel {
  if (visibilityScope === 'all_users') {
    return 'summary'
  }
  if (logDetailLevel === 'summary' || viewerDetailLevel === 'summary') {
    return 'summary'
  }
  return 'full'
}

function sanitizeOperationLogSummaryForViewer(summary: OperationLogSummary, detailLevel: OperationLogDetailLevel): OperationLogSummary {
  const withoutAdminOnlyFields: OperationLogSummary = {
    ...summary,
    userAgent: undefined,
    clientIp: undefined
  }
  if (detailLevel === 'full') {
    return withoutAdminOnlyFields
  }
  return {
    ...withoutAdminOnlyFields,
    detailLevel: 'summary',
    changes: [],
    metadata: {},
    method: undefined,
    path: undefined,
    statusCode: undefined
  }
}

function sanitizeOperationLogDetailForViewer(detail: OperationLogDetail, detailLevel: OperationLogDetailLevel): OperationLogDetail {
  const summary = sanitizeOperationLogSummaryForViewer(detail, detailLevel)
  if (detailLevel === 'summary') {
    return {
      ...summary,
      targets: [],
      viewers: []
    }
  }
  return {
    ...summary,
    targets: detail.targets,
    viewers: []
  }
}

function normalizeTargets(input: OperationLogInput): OperationLogTargetInput[] {
  const targets = [...(input.targets ?? [])]
  if (input.resourceId || input.resourceName) {
    const hasPrimary = targets.some((target) => target.relation === 'primary')
    if (!hasPrimary) {
      targets.unshift({
        targetType: input.resourceType,
        targetId: input.resourceId,
        targetName: input.resourceName,
        targetOwnerSystemAccountId: input.operationScopeSystemAccountId,
        relation: 'primary'
      })
    }
  }
  return targets
}

function normalizeViewers(input: OperationLogInput): OperationLogViewerInput[] {
  const viewers = [...(input.viewers ?? [])]
  if (input.visibilityScope === 'admin_only' || input.visibilityScope === 'all_users') {
    return []
  }
  viewers.push({ systemAccountId: input.actorSystemAccountId, visibilityReason: 'actor_self', detailLevel: input.detailLevel })
  if (input.operationScopeSystemAccountId && input.operationScopeSystemAccountId !== input.actorSystemAccountId) {
    viewers.push({
      systemAccountId: input.operationScopeSystemAccountId,
      visibilityReason: input.actorRole === 'admin' ? 'admin_managed_my_resource' : 'resource_owner',
      detailLevel: input.detailLevel
    })
  }
  return dedupeViewers(viewers)
}

function dedupeViewers(viewers: OperationLogViewerInput[]): OperationLogViewerInput[] {
  const seen = new Set<string>()
  const output: OperationLogViewerInput[] = []
  for (const viewer of viewers) {
    if (!viewer.systemAccountId.trim()) continue
    const key = `${viewer.systemAccountId}:${viewer.visibilityReason}:${viewer.detailLevel ?? ''}`
    if (seen.has(key)) continue
    seen.add(key)
    output.push(viewer)
  }
  return output
}

function loadSystemAccountNames(ids: Array<string | undefined>): Map<string, string> {
  const uniqueIds = [...new Set(ids.filter((id): id is string => Boolean(id?.trim())))]
  if (uniqueIds.length === 0) return new Map()
  return new Map([...loadSystemAccountsByIds(uniqueIds)].map(([id, account]) => [id, account.displayName ?? account.username]))
}

function parseJsonArray(value: unknown): OperationLogChange[] {
  if (typeof value !== 'string' || !value.trim()) return []
  try {
    const parsed = JSON.parse(value) as unknown
    return Array.isArray(parsed) ? parsed as OperationLogChange[] : []
  } catch {
    return []
  }
}

function parseJsonObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'string' || !value.trim()) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function normalizeOperationLogPage(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.max(1, value)
    : 1
}

function normalizeOperationLogPageSize(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(operationLogMaxPageSize, Math.max(1, value))
    : operationLogDefaultPageSize
}

function integerOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : null
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function safeJsonStringify(value: unknown, fallback: string): string {
  try {
    return JSON.stringify(value) ?? fallback
  } catch {
    return fallback
  }
}
