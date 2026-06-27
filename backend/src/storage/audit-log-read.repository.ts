import { getDatasetDatabase } from './database.js'
import {
  auditPayloadBodyDetail,
  readAuditHeadersBlobDetail,
  readAuditHeadersBlobDetailWithClient,
  readAuditPayloadBlobWindowWithClient,
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
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

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

export async function listAuditLogsAsync(options: AuditLogListOptions = {}): Promise<AuditLogListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAuditLogs(options)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return listAuditLogsWithClientAsync(client, options)
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

export async function listAuditLogsByIdsAsync(ids: string[]): Promise<AuditLogSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAuditLogsByIds(ids)
  }
  const uniqueIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (uniqueIds.length === 0) return []
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows: AuditLogRow[] = []
  for (const chunk of chunkValues(uniqueIds, 900)) {
    rows.push(...await client.query<AuditLogRow>(`
      SELECT ${auditLogSelectColumns('al')}
      FROM juhe_dataset.audit_logs al
      ${auditLogNameJoins()}
      WHERE al.id IN (${sqlPlaceholders(chunk.length)})
    `, chunk))
  }
  const order = new Map(uniqueIds.map((id, index) => [id, index]))
  rows.sort((left, right) => {
    const leftOrder = order.get(String(left.id ?? '')) ?? Number.MAX_SAFE_INTEGER
    const rightOrder = order.get(String(right.id ?? '')) ?? Number.MAX_SAFE_INTEGER
    return leftOrder - rightOrder
  })
  return rows.map((row) => auditLogSummaryFromRow(row, systemAccountNamesFromRows(rows)))
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

export async function listAuditErrorGroupsAsync(options: AuditErrorGroupListOptions = {}): Promise<AuditErrorGroupListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAuditErrorGroups(options)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return listAuditErrorGroupsWithClientAsync(client, options)
}

export function listAuditErrorGroupEvents(errorGroupId: string, options: AuditLogListOptions = {}): AuditLogListResult {
  return listAuditLogs({ ...options, errorGroupId })
}

export async function listAuditErrorGroupEventsAsync(errorGroupId: string, options: AuditLogListOptions = {}): Promise<AuditLogListResult> {
  return listAuditLogsAsync({ ...options, errorGroupId })
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

export async function getAuditLogDetailAsync(id: string): Promise<AuditLogDetail | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAuditLogDetail(id)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<AuditLogRow>(`
    SELECT ${auditLogSelectColumns('al')}
    FROM juhe_dataset.audit_logs al
    ${auditLogNameJoins()}
    WHERE al.id = ?
  `, [id])
  if (!row) return undefined

  const attemptRows = await client.query<AuditLogRow>(`
    SELECT attempts.*, accounts.name AS account_name, groups.name AS group_name
    FROM juhe_dataset.audit_log_attempts attempts
    LEFT JOIN juhe_business.accounts accounts ON accounts.id = attempts.account_id
    LEFT JOIN juhe_business.groups groups ON groups.id = attempts.group_id
    WHERE attempts.audit_log_id = ?
    ORDER BY attempts.attempt_index ASC, attempts.id ASC
  `, [id])
  const payloadRows = await getAuditPayloadRowsAsync(client, id)
  const errorGroupId = optionalString(row.error_group_id)
  const accountNames = namesFromRows(attemptRows, 'account_id', 'account_name')
  const groupNames = namesFromRows(attemptRows, 'group_id', 'group_name')
  return {
    ...auditLogSummaryFromRow(row, systemAccountNamesFromRows([row])),
    attempts: attemptRows.map((attempt) => auditLogAttemptFromRow(attempt, accountNames, groupNames)),
    errorGroup: errorGroupId ? await getAuditErrorGroupByIdAsync(client, errorGroupId) : undefined,
    payloads: payloadRows.map(auditLogPayloadSummaryFromRow)
  }
}

export async function getAuditLogPayload(
  auditLogId: string,
  payloadId: string,
  options: AuditLogPayloadReadOptions = {}
): Promise<AuditLogPayloadDetail | undefined> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<AuditLogRow>(`
      SELECT *
      FROM juhe_dataset.audit_payload_refs
      WHERE audit_log_id = ? AND id = ?
    `, [auditLogId, payloadId])
    if (!row) return undefined
    const summary = auditLogPayloadSummaryFromRow(row)
    const headers = await readAuditHeadersBlobDetailWithClient(client, optionalString(row.headers_blob_id))
    const bodyWindow = await readAuditPayloadBlobWindowWithClient(client, optionalString(row.body_blob_id), options)
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

async function listAuditLogsWithClientAsync(client: DatabaseClient, options: AuditLogListOptions = {}): Promise<AuditLogListResult> {
  const filters = buildAuditLogFilters(options)
  const pageSize = normalizePageSize(options.pageSize, auditLogDefaultPageSize, auditLogMaxPageSize)
  const page = normalizePage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const rows = await client.query<AuditLogRow>(`
    SELECT ${auditLogSelectColumns('al')}
    FROM juhe_dataset.audit_logs al
    ${auditLogNameJoins()}
    ${filters.clause}
    ORDER BY al.created_at DESC, al.id DESC
    LIMIT ? OFFSET ?
  `, [...filters.params, pageSize + 1, offset])
  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map((row) => auditLogSummaryFromRow(row, systemAccountNamesFromRows(pageRows.rows)))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

async function listAuditErrorGroupsWithClientAsync(client: DatabaseClient, options: AuditErrorGroupListOptions = {}): Promise<AuditErrorGroupListResult> {
  const filters = buildAuditErrorGroupFilters(options)
  const pageSize = normalizePageSize(options.pageSize, errorGroupDefaultPageSize, errorGroupMaxPageSize)
  const page = normalizePage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const rows = await client.query<AuditLogRow>(`
    SELECT ${auditErrorGroupSelectColumns('aeg')}
    FROM juhe_dataset.audit_error_groups aeg
    ${auditErrorGroupNameJoins()}
    ${filters.clause}
    ORDER BY aeg.updated_at DESC, aeg.id DESC
    LIMIT ? OFFSET ?
  `, [...filters.params, pageSize + 1, offset])
  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map((row) => auditErrorGroupFromRow(row, systemAccountNamesFromRows(pageRows.rows)))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
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

async function getAuditErrorGroupByIdAsync(client: DatabaseClient, id: string): Promise<AuditErrorGroupSummary | undefined> {
  const row = await client.one<AuditLogRow>(`
    SELECT ${auditErrorGroupSelectColumns('aeg')}
    FROM juhe_dataset.audit_error_groups aeg
    ${auditErrorGroupNameJoins()}
    WHERE aeg.id = ?
  `, [id])
  return row ? auditErrorGroupFromRow(row, systemAccountNamesFromRows([row])) : undefined
}

function getAuditPayloadRows(auditLogId: string): AuditLogRow[] {
  return getDatasetDatabase()
    .prepare('SELECT * FROM audit_payload_refs WHERE audit_log_id = ? ORDER BY sequence_index ASC, id ASC')
    .all(auditLogId) as AuditLogRow[]
}

async function getAuditPayloadRowsAsync(client: DatabaseClient, auditLogId: string): Promise<AuditLogRow[]> {
  return client.query<AuditLogRow>(`
    SELECT *
    FROM juhe_dataset.audit_payload_refs
    WHERE audit_log_id = ?
    ORDER BY sequence_index ASC, id ASC
  `, [auditLogId])
}

function auditLogSelectColumns(alias: string): string {
  return [
    `${alias}.*`,
    'api_keys.name AS api_key_name',
    'groups.name AS group_name',
    'accounts.name AS account_name',
    'system_accounts.display_name AS system_account_name'
  ].join(', ')
}

function auditErrorGroupSelectColumns(alias: string): string {
  return [
    auditErrorGroupListSelectColumns(alias),
    'api_keys.name AS api_key_name',
    'groups.name AS group_name',
    'accounts.name AS account_name',
    'system_accounts.display_name AS system_account_name'
  ].join(', ')
}

function auditLogNameJoins(): string {
  return `
    LEFT JOIN juhe_business.api_keys api_keys ON api_keys.id = al.api_key_id
    LEFT JOIN juhe_business.groups groups ON groups.id = al.group_id
    LEFT JOIN juhe_business.accounts accounts ON accounts.id = al.account_id
    LEFT JOIN juhe_business.system_accounts system_accounts ON system_accounts.id = al.system_account_id
  `
}

function auditErrorGroupNameJoins(): string {
  return `
    LEFT JOIN juhe_business.api_keys api_keys ON api_keys.id = aeg.api_key_id
    LEFT JOIN juhe_business.groups groups ON groups.id = aeg.group_id
    LEFT JOIN juhe_business.accounts accounts ON accounts.id = aeg.account_id
    LEFT JOIN juhe_business.system_accounts system_accounts ON system_accounts.id = aeg.system_account_id
  `
}

function systemAccountNamesFromRows(rows: AuditLogRow[]): Map<string, string> {
  return namesFromRows(rows, 'system_account_id', 'system_account_name')
}

function namesFromRows(rows: AuditLogRow[], idColumn: string, nameColumn: string): Map<string, string> {
  const names = new Map<string, string>()
  for (const row of rows) {
    const id = optionalString(row[idColumn])
    const name = optionalString(row[nameColumn])
    if (id && name) {
      names.set(id, name)
    }
  }
  return names
}
