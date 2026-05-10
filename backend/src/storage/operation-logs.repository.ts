import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { sqlPlaceholders } from './query-utils.js'
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

const operationLogDefaultPageSize = 100
const operationLogMaxPageSize = 100

export function createOperationLog(input: OperationLogInput): OperationLogSummary {
  const database = getDatabase()
  const id = input.id ?? newId('oplog')
  const createdAt = input.createdAt ?? nowIso()
  const visibilityScope = input.visibilityScope ?? 'targeted'
  const detailLevel = input.detailLevel ?? 'full'
  const targets = normalizeTargets(input)
  const viewers = normalizeViewers(input)

  const insertLog = database.prepare(`
    INSERT INTO operation_logs (
      id, trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
      operation_scope_system_account_id, mode, module, action, operation_key, resource_type, resource_id,
      resource_name, summary, detail_level, visibility_scope, changes_json, metadata_json, method, path,
      status_code, client_ip, user_agent, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertTarget = database.prepare(`
    INSERT INTO operation_log_targets (
      id, operation_log_id, target_type, target_id, target_name, target_owner_system_account_id, relation, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertViewer = database.prepare(`
    INSERT OR IGNORE INTO operation_log_viewers (
      operation_log_id, system_account_id, visibility_reason, detail_level, created_at
    ) VALUES (?, ?, ?, ?, ?)
  `)

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    insertLog.run(
      id,
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
      detailLevel,
      visibilityScope,
      JSON.stringify(input.changes ?? []),
      JSON.stringify(input.metadata ?? {}),
      input.method ?? null,
      input.path ?? null,
      integerOrNull(input.statusCode),
      input.clientIp ?? null,
      input.userAgent ?? null,
      createdAt
    )

    for (const target of targets) {
      insertTarget.run(
        newId('optgt'),
        id,
        target.targetType,
        target.targetId ?? null,
        target.targetName ?? null,
        target.targetOwnerSystemAccountId ?? null,
        target.relation ?? 'affected',
        createdAt
      )
    }

    for (const viewer of viewers) {
      insertViewer.run(
        id,
        viewer.systemAccountId,
        viewer.visibilityReason,
        viewer.detailLevel ?? detailLevel,
        createdAt
      )
    }

    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }

  return operationLogSummaryFromRow({
    id,
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
    detail_level: detailLevel,
    visibility_scope: visibilityScope,
    changes_json: JSON.stringify(input.changes ?? []),
    metadata_json: JSON.stringify(input.metadata ?? {}),
    method: input.method,
    path: input.path,
    status_code: input.statusCode,
    client_ip: input.clientIp,
    user_agent: input.userAgent,
    created_at: createdAt
  }, loadSystemAccountNames([input.actorSystemAccountId, input.operationScopeSystemAccountId]))
}

export function listOperationLogs(options: OperationLogListOptions = {}): OperationLogListResult {
  const filters = buildOperationLogFilters(options)
  return listOperationLogsWithFilters(filters, options)
}

export function listOperationLogsForViewer(systemAccountId: string, options: OperationLogListOptions = {}): OperationLogListResult {
  const filters = buildViewerOperationLogFilters(systemAccountId, options)
  return listOperationLogsWithFilters(filters, options, systemAccountId)
}

export function getOperationLogDetail(id: string): OperationLogDetail | undefined {
  return getOperationLogDetailWithClause('ol.id = ?', [id])
}

export function getOperationLogDetailForViewer(id: string, systemAccountId: string): OperationLogDetail | undefined {
  const detail = getOperationLogDetailWithClause(`
    ol.id = ?
    AND (
      ol.visibility_scope = 'all_users'
      OR EXISTS (
        SELECT 1 FROM operation_log_viewers olv
        WHERE olv.operation_log_id = ol.id AND olv.system_account_id = ?
      )
    )
  `, [id, systemAccountId])
  if (!detail) return undefined

  const viewerLevel = loadViewerDetailLevels([detail.id], systemAccountId).get(detail.id)
  return sanitizeOperationLogDetailForViewer(detail, effectiveViewerDetailLevel(detail.detailLevel, viewerLevel))
}

export function cleanupOperationLogsBefore(cutoffCreatedAt: string, limit?: number): number {
  const database = getDatabase()
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
  const database = getDatabase()
  const totalRow = database
    .prepare(`
      SELECT COUNT(*) AS total
      FROM operation_logs ol
      ${filters.clause}
    `)
    .get(...filters.params) as OperationLogRow | undefined
  const rows = database
    .prepare(`
      SELECT ol.*
      FROM operation_logs ol
      ${filters.clause}
      ORDER BY ol.created_at DESC, ol.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize, offset) as OperationLogRow[]
  const systemAccountNames = loadSystemAccountNames(rows.flatMap((row) => [
    optionalString(row.actor_system_account_id),
    optionalString(row.operation_scope_system_account_id)
  ]))
  const viewerDetailLevels = viewerSystemAccountId
    ? loadViewerDetailLevels(rows.map((row) => String(row.id)), viewerSystemAccountId)
    : new Map<string, OperationLogDetailLevel>()
  return {
    items: rows.map((row) => {
      const summary = operationLogSummaryFromRow(row, systemAccountNames)
      if (!viewerSystemAccountId) return summary
      return sanitizeOperationLogSummaryForViewer(summary, effectiveViewerDetailLevel(summary.detailLevel, viewerDetailLevels.get(summary.id)))
    }),
    total: Number(totalRow?.total ?? 0),
    page,
    pageSize
  }
}

function getOperationLogDetailWithClause(whereClause: string, params: OperationLogFilterValue[]): OperationLogDetail | undefined {
  const database = getDatabase()
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
  const clauses: string[] = []
  const params: OperationLogFilterValue[] = []
  pushCommonOperationLogFilters(clauses, params, options)
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function buildViewerOperationLogFilters(systemAccountId: string, options: OperationLogListOptions): { clause: string; params: OperationLogFilterValue[] } {
  const clauses: string[] = [
    `(
      ol.visibility_scope = 'all_users'
      OR EXISTS (
        SELECT 1 FROM operation_log_viewers olv
        WHERE olv.operation_log_id = ol.id AND olv.system_account_id = ?
      )
    )`
  ]
  const params: OperationLogFilterValue[] = [systemAccountId]
  pushCommonOperationLogFilters(clauses, params, options)
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
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
    clauses.push(`EXISTS (
      SELECT 1 FROM operation_log_viewers affected
      WHERE affected.operation_log_id = ol.id AND affected.system_account_id = ?
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

function operationLogSummaryFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>): OperationLogSummary {
  const actorSystemAccountId = String(row.actor_system_account_id)
  const operationScopeSystemAccountId = optionalString(row.operation_scope_system_account_id)
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
    changes: parseJsonArray(row.changes_json),
    metadata: parseJsonObject(row.metadata_json),
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

  const rows = getDatabase()
    .prepare(`
      SELECT operation_log_id, detail_level
      FROM operation_log_viewers
      WHERE system_account_id = ? AND operation_log_id IN (${sqlPlaceholders(ids.length)})
    `)
    .all(systemAccountId, ...ids) as OperationLogRow[]
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

function effectiveViewerDetailLevel(logDetailLevel: OperationLogDetailLevel, viewerDetailLevel?: OperationLogDetailLevel): OperationLogDetailLevel {
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
  if (input.visibilityScope === 'admin_only') {
    return dedupeViewers(viewers)
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
