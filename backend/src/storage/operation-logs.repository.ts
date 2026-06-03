import type { SystemAccountRole } from '../domain/types.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

export type OperationLogActorRole = SystemAccountRole
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
  summaryKeyword?: string
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
type OperationLogInsertStatement = ReturnType<ReturnType<typeof getDatasetDatabase>['prepare']>

interface OperationLogInsertStatements {
  insertLog: OperationLogInsertStatement
  insertTarget: OperationLogInsertStatement
  insertViewer: OperationLogInsertStatement
  insertSearchTerm: OperationLogInsertStatement
}

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
const operationLogMaxListWindowRows = 1001
const operationLogSearchMinTermLength = 2
const operationLogSearchMaxTermLength = 128
const operationLogSearchMaxFieldChars = 256
const operationLogSearchMaxTermsPerLog = 1500

export function createOperationLog(input: OperationLogInput): OperationLogSummary {
  const database = getDatasetDatabase()
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

  const database = getDatasetDatabase()
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

export function cleanupOperationLogsBefore(cutoffCreatedAt: string, limit = 1000): number {
  const database = getDatasetDatabase()
  const rows = database
    .prepare('SELECT id FROM operation_logs WHERE created_at < ? ORDER BY created_at ASC, id ASC LIMIT ?')
    .all(cutoffCreatedAt, Math.max(1, Math.trunc(limit))) as OperationLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = sqlPlaceholders(ids.length)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const result = database.prepare(`DELETE FROM operation_logs WHERE id IN (${placeholders})`).run(...ids)
    commitDatabaseTransaction(database, transactionStarted)
    return Number(result.changes ?? 0)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
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

function operationLogOuterListSelectColumns(): string {
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
    'changes_json',
    'metadata_json',
    'method',
    'path',
    'status_code',
    'client_ip',
    'user_agent',
    'created_at'
  ].join(', ')
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

function operationLogSearchTermFromKeyword(value?: string): string | undefined {
  const normalized = normalizeOperationLogSearchText(value)
  if (!normalized) return undefined
  if (normalized.length >= operationLogSearchMinTermLength && normalized.length <= operationLogSearchMaxTermLength) {
    return normalized
  }

  const compact = compactOperationLogSearchText(normalized)
  if (compact.length >= operationLogSearchMinTermLength && compact.length <= operationLogSearchMaxTermLength) {
    return compact
  }
  return undefined
}

function buildOperationLogSearchTerms(prepared: PreparedOperationLogInput): string[] {
  const input = prepared.input
  const terms = new Set<string>()
  collectOperationLogSearchTerms(terms, input.summary)
  return [...terms]
}

function collectOperationLogSearchTerms(terms: Set<string>, value: unknown): void {
  const normalized = normalizeOperationLogSearchText(value)
  if (!normalized) return

  const compact = compactOperationLogSearchText(normalized)
  addOperationLogSearchExactTerm(terms, normalized)
  if (compact !== normalized) {
    addOperationLogSearchExactTerm(terms, compact)
  }
  for (const part of normalized.split(' ')) {
    addOperationLogSearchExactTerm(terms, part)
  }
  if (terms.size >= operationLogSearchMaxTermsPerLog) return

  addOperationLogSearchSubstrings(terms, normalized)
  if (terms.size >= operationLogSearchMaxTermsPerLog) return

  for (const part of normalized.split(' ')) {
    addOperationLogSearchSubstrings(terms, part)
    if (terms.size >= operationLogSearchMaxTermsPerLog) return
  }

  if (compact !== normalized) {
    addOperationLogSearchSubstrings(terms, compact)
  }
}

function addOperationLogSearchExactTerm(terms: Set<string>, value: string): void {
  const term = value.trim()
  if (term.length >= operationLogSearchMinTermLength && term.length <= operationLogSearchMaxTermLength) {
    terms.add(term)
  }
}

function addOperationLogSearchSubstrings(terms: Set<string>, value: string): void {
  if (terms.size >= operationLogSearchMaxTermsPerLog) return
  const chars = [...value].slice(0, operationLogSearchMaxFieldChars)
  const maxLength = Math.min(operationLogSearchMaxTermLength, chars.length)
  for (let length = operationLogSearchMinTermLength; length <= maxLength; length += 1) {
    for (let start = 0; start + length <= chars.length; start += 1) {
      const term = chars.slice(start, start + length).join('').trim()
      if (term.length >= operationLogSearchMinTermLength) {
        terms.add(term)
        if (terms.size >= operationLogSearchMaxTermsPerLog) return
      }
    }
  }
}

function normalizeOperationLogSearchText(value: unknown): string {
  if (typeof value !== 'string') return ''
  return value
    .normalize('NFKC')
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, ' ')
    .replace(/\s+/g, ' ')
    .trim()
}

function compactOperationLogSearchText(value: string): string {
  return value.replace(/\s+/g, '')
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

function prepareOperationLogInsertStatements(database: ReturnType<typeof getDatasetDatabase>): OperationLogInsertStatements {
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
    `),
    insertSearchTerm: database.prepare(`
      INSERT OR IGNORE INTO operation_log_summary_search_terms (
        operation_log_id, term, created_at
      ) VALUES (?, ?, ?)
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

  for (const term of buildOperationLogSearchTerms(prepared)) {
    statements.insertSearchTerm.run(prepared.id, term, prepared.createdAt)
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
  return loadSystemAccountNameMapByIds(uniqueIds)
}

function parseJsonArray(value: unknown): OperationLogChange[] {
  if (typeof value !== 'string' || !value.trim()) return []
  const parsed = JSON.parse(value) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('操作日志 changes_json 必须是数组')
  }
  return parsed as OperationLogChange[]
}

function parseJsonObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'string' || !value.trim()) return {}
  const parsed = JSON.parse(value) as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('操作日志 metadata_json 必须是对象')
  }
  return parsed as Record<string, unknown>
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
