import { getDatasetDatabase } from './database.js'
import {
  operationLogSummaryFromRow,
  operationLogTargetFromRow,
  operationLogViewerFromRow,
  type OperationLogRow
} from './operation-log-mappers.js'
import { operationLogSearchTermFromKeyword } from './operation-log-search.js'
import type {
  OperationLogDetail,
  OperationLogDetailLevel,
  OperationLogListOptions,
  OperationLogListResult,
  OperationLogSummary,
  OperationLogVisibilityScope
} from './operation-log-types.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

type OperationLogFilterValue = string | number

interface OperationLogSqlFilters {
  clauses: string[]
  params: OperationLogFilterValue[]
  searchTerm?: string
}

interface OperationLogWhereFilters {
  clauses: string[]
  params: OperationLogFilterValue[]
  searchTerm?: string
}

const operationLogDefaultPageSize = 100
const operationLogMaxPageSize = 100
const operationLogMaxListWindowRows = 1001

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

function listOperationLogsWithFilters(filters: OperationLogWhereFilters, options: OperationLogListOptions, viewerSystemAccountId?: string): OperationLogListResult {
  const pageSize = normalizeOperationLogPageSize(options.pageSize)
  const page = normalizeOperationLogPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const database = getDatasetDatabase()
  const whereClause = filters.clauses.length ? `WHERE ${filters.clauses.join(' AND ')}` : ''
  const searchWhereClause = filters.clauses.length ? `AND ${filters.clauses.join(' AND ')}` : ''
  const rows = filters.searchTerm
    ? database
      .prepare(`
        SELECT ${operationLogListSelectColumns('ol')}
        FROM operation_log_summary_search_terms search INDEXED BY idx_operation_log_summary_search_terms_term_created
        INNER JOIN operation_logs ol ON ol.id = search.operation_log_id
        WHERE search.term = ?
        ${searchWhereClause}
        ORDER BY search.created_at DESC, search.operation_log_id DESC
        LIMIT ? OFFSET ?
      `)
      .all(filters.searchTerm, ...filters.params, pageSize + 1, offset) as OperationLogRow[]
    : database
      .prepare(`
        SELECT ${operationLogListSelectColumns('ol')}
        FROM operation_logs ol
        ${whereClause}
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
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

function operationLogListSelectColumns(alias: string): string {
  return [
    'id',
    'trace_id',
    'actor_system_account_id',
    'actor_username',
    'actor_display_name',
    'actor_role',
    'operation_scope_system_account_id',
    'mode',
    'module',
    'action',
    'operation_key',
    'resource_type',
    'resource_id',
    'resource_name',
    'summary',
    'detail_level',
    'visibility_scope',
    "'[]' AS changes_json",
    "'{}' AS metadata_json",
    'method',
    'path',
    'status_code',
    'client_ip',
    'user_agent',
    'created_at'
  ].map((column) => column.includes(' AS ') ? column : `${alias}.${column}`).join(', ')
}

function listVisibleOperationLogsForViewer(systemAccountId: string, options: OperationLogListOptions): OperationLogListResult {
  const pageSize = normalizeOperationLogPageSize(options.pageSize)
  const page = normalizeOperationLogPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const database = getDatasetDatabase()
  const commonFilters = buildCommonOperationLogFilters(options)
  const rowWindowSize = offset + pageSize + 1
  const targetedRows = loadVisibleTargetedOperationLogRows(database, systemAccountId, commonFilters, rowWindowSize)
  const allUsersRows = loadVisibleAllUsersOperationLogRows(database, commonFilters, rowWindowSize)
  const rows = mergeOperationLogRowsByCreatedAt(targetedRows, allUsersRows, rowWindowSize)
  const pageRows = takePageRows(rows.slice(offset), pageSize)
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
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

function loadVisibleTargetedOperationLogRows(
  database: ReturnType<typeof getDatasetDatabase>,
  systemAccountId: string,
  filters: OperationLogSqlFilters,
  limit: number
): OperationLogRow[] {
  const filterClause = filters.clauses.length ? `AND ${filters.clauses.join(' AND ')}` : ''
  if (filters.searchTerm) {
    return database
      .prepare(`
        SELECT ${operationLogListSelectColumns('ol')}
        FROM operation_log_summary_search_terms search INDEXED BY idx_operation_log_summary_search_terms_term_created
        INNER JOIN operation_logs ol ON ol.id = search.operation_log_id AND search.term = ?
        INNER JOIN operation_log_viewers visible INDEXED BY idx_operation_log_viewers_log_account
          ON visible.operation_log_id = ol.id AND visible.system_account_id = ?
        WHERE ol.visibility_scope = 'targeted'
        AND NOT EXISTS (
          SELECT 1
          FROM operation_log_viewers previous
          WHERE previous.operation_log_id = visible.operation_log_id
          AND previous.system_account_id = visible.system_account_id
          AND previous.visibility_reason < visible.visibility_reason
        )
        ${filterClause}
        ORDER BY search.created_at DESC, search.operation_log_id DESC
        LIMIT ?
      `)
      .all(filters.searchTerm, systemAccountId, ...filters.params, limit) as OperationLogRow[]
  }

  return database
    .prepare(`
      SELECT ${operationLogListSelectColumns('ol')}
      FROM operation_log_viewers visible INDEXED BY idx_operation_log_viewers_account_created
      INNER JOIN operation_logs ol ON ol.id = visible.operation_log_id
      WHERE visible.system_account_id = ?
      AND ol.visibility_scope = 'targeted'
      AND NOT EXISTS (
        SELECT 1
        FROM operation_log_viewers previous
        WHERE previous.operation_log_id = visible.operation_log_id
        AND previous.system_account_id = visible.system_account_id
        AND previous.visibility_reason < visible.visibility_reason
      )
      ${filterClause}
      ORDER BY visible.created_at DESC, visible.operation_log_id DESC
      LIMIT ?
    `)
    .all(systemAccountId, ...filters.params, limit) as OperationLogRow[]
}

function loadVisibleAllUsersOperationLogRows(
  database: ReturnType<typeof getDatasetDatabase>,
  filters: OperationLogSqlFilters,
  limit: number
): OperationLogRow[] {
  const filterClause = filters.clauses.length ? `AND ${filters.clauses.join(' AND ')}` : ''
  if (filters.searchTerm) {
    return database
      .prepare(`
        SELECT ${operationLogListSelectColumns('ol')}
        FROM operation_log_summary_search_terms search INDEXED BY idx_operation_log_summary_search_terms_term_created
        INNER JOIN operation_logs ol ON ol.id = search.operation_log_id AND search.term = ?
        WHERE ol.visibility_scope = 'all_users'
        ${filterClause}
        ORDER BY search.created_at DESC, search.operation_log_id DESC
        LIMIT ?
      `)
      .all(filters.searchTerm, ...filters.params, limit) as OperationLogRow[]
  }

  return database
    .prepare(`
      SELECT ${operationLogListSelectColumns('ol')}
      FROM operation_logs ol INDEXED BY idx_operation_logs_visibility_created
      WHERE ol.visibility_scope = 'all_users'
      ${filterClause}
      ORDER BY ol.created_at DESC, ol.id DESC
      LIMIT ?
    `)
    .all(...filters.params, limit) as OperationLogRow[]
}

function mergeOperationLogRowsByCreatedAt(leftRows: OperationLogRow[], rightRows: OperationLogRow[], limit: number): OperationLogRow[] {
  const output: OperationLogRow[] = []
  let leftIndex = 0
  let rightIndex = 0
  while (output.length < limit && (leftIndex < leftRows.length || rightIndex < rightRows.length)) {
    const left = leftRows[leftIndex]
    const right = rightRows[rightIndex]
    if (!right || (left && compareOperationLogRowsByCreatedAt(left, right) <= 0)) {
      output.push(left)
      leftIndex += 1
    } else {
      output.push(right)
      rightIndex += 1
    }
  }
  return output
}

function compareOperationLogRowsByCreatedAt(left: OperationLogRow, right: OperationLogRow): number {
  const leftCreatedAt = String(left.created_at ?? '')
  const rightCreatedAt = String(right.created_at ?? '')
  if (leftCreatedAt !== rightCreatedAt) {
    return leftCreatedAt > rightCreatedAt ? -1 : 1
  }
  const leftId = String(left.id ?? '')
  const rightId = String(right.id ?? '')
  if (leftId === rightId) return 0
  return leftId > rightId ? -1 : 1
}

function getOperationLogDetailWithClause(whereClause: string, params: OperationLogFilterValue[]): OperationLogDetail | undefined {
  const database = getDatasetDatabase()
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

function buildOperationLogFilters(options: OperationLogListOptions): OperationLogWhereFilters {
  const filters = buildCommonOperationLogFilters(options)
  return {
    clauses: filters.clauses,
    params: filters.params,
    searchTerm: filters.searchTerm
  }
}

function buildCommonOperationLogFilters(options: OperationLogListOptions): OperationLogSqlFilters {
  const clauses: string[] = []
  const params: OperationLogFilterValue[] = []
  const searchTerm = operationLogSearchTermFromKeyword(options.summaryKeyword)
  pushCommonOperationLogFilters(clauses, params, options)
  if (options.summaryKeyword?.trim() && !searchTerm) {
    clauses.push('0 = 1')
  }
  return {
    clauses,
    params,
    searchTerm
  }
}

function pushCommonOperationLogFilters(clauses: string[], params: OperationLogFilterValue[], options: OperationLogListOptions): void {
  pushPrefixFilter(clauses, params, 'ol.trace_id', options.traceId)
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

function pushPrefixFilter(clauses: string[], params: OperationLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} >= ? AND ${column} < ?`)
  params.push(text, `${text}\uffff`)
}

function loadViewerDetailLevels(operationLogIds: string[], systemAccountId: string): Map<string, OperationLogDetailLevel> {
  const ids = [...new Set(operationLogIds.filter((id) => id.trim()))]
  if (ids.length === 0) return new Map()

  const rows: OperationLogRow[] = []
  const database = getDatasetDatabase()
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

function loadSystemAccountNames(ids: Array<string | undefined>): Map<string, string> {
  const uniqueIds = [...new Set(ids.filter((id): id is string => Boolean(id?.trim())))]
  if (uniqueIds.length === 0) return new Map()
  return loadSystemAccountNameMapByIds(uniqueIds)
}

function normalizeOperationLogPage(value: unknown, pageSize: number): number {
  const maxPage = Math.max(1, Math.floor((operationLogMaxListWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(maxPage, Math.max(1, value))
    : 1
}

function normalizeOperationLogPageSize(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(operationLogMaxPageSize, Math.max(1, value))
    : operationLogDefaultPageSize
}
