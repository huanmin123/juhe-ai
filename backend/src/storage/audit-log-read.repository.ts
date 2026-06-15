import { getDatasetDatabase } from './database.js'
import {
  auditPayloadBodyDetail,
  readAuditHeadersBlobDetail,
  readAuditPayloadBlobWindow
} from './audit-log-payload-blobs.js'
import {
  auditErrorGroupFromRow,
  auditLogAttemptFromRow,
  auditLogPayloadSummaryFromRow,
  auditLogSummaryFromRow,
  hydrateAuditRows,
  type AuditLogRow
} from './audit-log-mappers.js'
import {
  auditErrorGroupListSelectColumns,
  auditLogDefaultPageSize,
  auditLogMaxPageSize,
  buildAuditErrorGroupFilters,
  buildAuditLogFilters,
  errorGroupDefaultPageSize,
  errorGroupMaxPageSize,
  normalizePage,
  normalizePageSize
} from './audit-log-list-query.js'
import type {
  AuditErrorGroupListOptions,
  AuditErrorGroupListResult,
  AuditErrorGroupSummary,
  AuditLogDetail,
  AuditLogListOptions,
  AuditLogListResult,
  AuditLogPayloadDetail,
  AuditLogPayloadReadOptions,
  AuditLogSummary
} from './audit-log-types.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadAccountNameMap, loadGroupNameMap, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

export function listAuditLogs(options: AuditLogListOptions = {}): AuditLogListResult {
  const filters = buildAuditLogFilters(options)
  const pageSize = normalizePageSize(options.pageSize, auditLogDefaultPageSize, auditLogMaxPageSize)
  const page = normalizePage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`
      SELECT
        al.*
      FROM audit_logs al
      ${filters.clause}
      ORDER BY al.created_at DESC, al.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as AuditLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const rowsWithNames = hydrateAuditRows(pageRows.rows)
  const systemAccountNames = loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
  const items = rowsWithNames.map((row) => auditLogSummaryFromRow(row, systemAccountNames))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function listAuditLogsByIds(ids: string[]): AuditLogSummary[] {
  const uniqueIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (uniqueIds.length === 0) return []
  const database = getDatasetDatabase()
  const rows: AuditLogRow[] = []
  for (const chunk of chunkValues(uniqueIds, 900)) {
    rows.push(...database
      .prepare(`
        SELECT al.*
        FROM audit_logs al
        WHERE al.id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as AuditLogRow[])
  }
  const order = new Map(uniqueIds.map((id, index) => [id, index]))
  rows.sort((left, right) => {
    const leftOrder = order.get(String(left.id ?? '')) ?? Number.MAX_SAFE_INTEGER
    const rightOrder = order.get(String(right.id ?? '')) ?? Number.MAX_SAFE_INTEGER
    return leftOrder - rightOrder
  })
  const rowsWithNames = hydrateAuditRows(rows)
  const systemAccountNames = loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
  return rowsWithNames.map((row) => auditLogSummaryFromRow(row, systemAccountNames))
}

export function listAuditErrorGroups(options: AuditErrorGroupListOptions = {}): AuditErrorGroupListResult {
  const filters = buildAuditErrorGroupFilters(options)
  const pageSize = normalizePageSize(options.pageSize, errorGroupDefaultPageSize, errorGroupMaxPageSize)
  const page = normalizePage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`
      SELECT ${auditErrorGroupListSelectColumns('aeg')}
      FROM audit_error_groups aeg
      ${filters.clause}
      ORDER BY aeg.updated_at DESC, aeg.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as AuditLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const rowsWithNames = hydrateAuditRows(pageRows.rows)
  const systemAccountNames = loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
  const items = rowsWithNames.map((row) => auditErrorGroupFromRow(row, systemAccountNames))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function listAuditErrorGroupEvents(errorGroupId: string, options: AuditLogListOptions = {}): AuditLogListResult {
  return listAuditLogs({ ...options, errorGroupId })
}

export function getAuditLogDetail(id: string): AuditLogDetail | undefined {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT
        al.*
      FROM audit_logs al
      WHERE al.id = ?
    `)
    .get(id) as AuditLogRow | undefined
  if (!row) return undefined
  const namedRow = hydrateAuditRows([row])[0] ?? row

  const attemptRows = getDatasetDatabase()
    .prepare('SELECT * FROM audit_log_attempts WHERE audit_log_id = ? ORDER BY attempt_index ASC, id ASC')
    .all(id) as AuditLogRow[]
  const payloadRows = getAuditPayloadRows(id)
  const systemAccountNames = loadSystemAccountNameMapByIds([optionalString(namedRow.system_account_id)])
  const errorGroupId = optionalString(namedRow.error_group_id)
  const accountNames = loadAccountNameMap(attemptRows.map((attempt) => String(attempt.account_id ?? '')).filter(Boolean))
  const groupNames = loadGroupNameMap(attemptRows.map((attempt) => String(attempt.group_id ?? '')).filter(Boolean))
  return {
    ...auditLogSummaryFromRow(namedRow, systemAccountNames),
    attempts: attemptRows.map((attempt) => auditLogAttemptFromRow(attempt, accountNames, groupNames)),
    errorGroup: errorGroupId ? getAuditErrorGroupById(errorGroupId) : undefined,
    payloads: payloadRows.map(auditLogPayloadSummaryFromRow)
  }
}

export async function getAuditLogPayload(
  auditLogId: string,
  payloadId: string,
  options: AuditLogPayloadReadOptions = {}
): Promise<AuditLogPayloadDetail | undefined> {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT *
      FROM audit_payload_refs
      WHERE audit_log_id = ? AND id = ?
    `)
    .get(auditLogId, payloadId) as AuditLogRow | undefined
  if (row) {
    const summary = auditLogPayloadSummaryFromRow(row)
    const headers = await readAuditHeadersBlobDetail(optionalString(row.headers_blob_id))
    const bodyWindow = await readAuditPayloadBlobWindow(optionalString(row.body_blob_id), options)
    return {
      ...summary,
      headers: headers.headers,
      ...auditPayloadBodyDetail(bodyWindow.bytes),
      headersStorageStatus: headers.storageStatus,
      bodyStorageStatus: bodyWindow.storageStatus,
      bodyOffset: bodyWindow.offset,
      bodyLimit: bodyWindow.limit,
      bodyBytesReturned: bodyWindow.bytes?.byteLength ?? 0,
      bodyTotalBytes: bodyWindow.totalBytes,
      bodyNextOffset: bodyWindow.nextOffset,
      bodyTruncated: bodyWindow.truncated
    }
  }
}

function getAuditErrorGroupById(id: string): AuditErrorGroupSummary | undefined {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT
        aeg.*
      FROM audit_error_groups aeg
      WHERE aeg.id = ?
    `)
    .get(id) as AuditLogRow | undefined
  const namedRow = row ? hydrateAuditRows([row])[0] : undefined
  const systemAccountNames = loadSystemAccountNameMapByIds([optionalString(namedRow?.system_account_id)])
  return namedRow ? auditErrorGroupFromRow(namedRow, systemAccountNames) : undefined
}

function getAuditPayloadRows(auditLogId: string): AuditLogRow[] {
  return getDatasetDatabase()
    .prepare('SELECT * FROM audit_payload_refs WHERE audit_log_id = ? ORDER BY sequence_index ASC, id ASC')
    .all(auditLogId) as AuditLogRow[]
}
