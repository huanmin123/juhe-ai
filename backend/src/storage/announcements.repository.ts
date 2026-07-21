import type {
  AnnouncementLevel,
  AnnouncementListItem,
  AnnouncementStatus,
  AnnouncementSummary,
  PublicAnnouncementDetail,
  PublicAnnouncementListItem
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadSystemAccountPrincipalMapByIds, loadSystemAccountPrincipalMapByIdsAsync } from './repository-lookups.js'
import type { AnnouncementRow } from './repository-row-types.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

const announcementLevels: readonly AnnouncementLevel[] = ['critical', 'warning', 'info', 'normal']
const announcementStatuses: readonly AnnouncementStatus[] = ['draft', 'published', 'archived']
const publicAnnouncementLimit = 30
const defaultAnnouncementPageSize = 50
const maxAnnouncementPageSize = 100
const announcementInputKeys = new Set(['title', 'content', 'level', 'status'])

export interface AnnouncementInput {
  title: string
  content: string
  level?: AnnouncementLevel
  status?: AnnouncementStatus
}

interface PublicAnnouncementListRow {
  id: string
  title: string
  level: AnnouncementLevel
  published_at: string
  read_at: string | null
}

interface PublicAnnouncementDetailRow {
  id: string
  title: string
  content: string
  level: AnnouncementLevel
  published_at: string
}

type AnnouncementListRow = Omit<AnnouncementRow, 'content'> & { content_preview: string }

export interface AnnouncementReadResult {
  readAt: string
  count: number
}

export interface AnnouncementListOptions {
  page?: number
  pageSize?: number
}

export interface AnnouncementListResult {
  items: AnnouncementListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export function listPublicAnnouncements(systemAccountId: string, limit = publicAnnouncementLimit): PublicAnnouncementListItem[] {
  const safeLimit = normalizePublicLimit(limit)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT
        announcements.id,
        announcements.title,
        announcements.level,
        announcements.published_at,
        announcement_reads.read_at
      FROM announcements
      LEFT JOIN announcement_reads
        ON announcement_reads.announcement_id = announcements.id
        AND announcement_reads.system_account_id = ?
      WHERE announcements.status = 'published'
        AND announcements.published_at IS NOT NULL
      ORDER BY announcements.published_at DESC, announcements.created_at DESC, announcements.id DESC
      LIMIT ?
    `)
    .all(systemAccountId, safeLimit) as unknown as PublicAnnouncementListRow[]
  return publicAnnouncementListItems(rows)
}

export async function listPublicAnnouncementsAsync(systemAccountId: string, limit = publicAnnouncementLimit): Promise<PublicAnnouncementListItem[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_public_announcements_read_only',
      systemAccountId,
      limit
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listPublicAnnouncements(systemAccountId, limit)
  }
  const safeLimit = normalizePublicLimit(limit)
  const client = await announcementDatabaseClient()
  const rows = await client.query<PublicAnnouncementListRow>(`
    SELECT
      announcements.id,
      announcements.title,
      announcements.level,
      announcements.published_at,
      announcement_reads.read_at
    FROM ${announcementTable(client, 'announcements')} announcements
    LEFT JOIN ${announcementTable(client, 'announcement_reads')} announcement_reads
      ON announcement_reads.announcement_id = announcements.id
      AND announcement_reads.system_account_id = ?
    WHERE announcements.status = 'published'
      AND announcements.published_at IS NOT NULL
    ORDER BY announcements.published_at DESC, announcements.created_at DESC, announcements.id DESC
    LIMIT ?
  `, [systemAccountId, safeLimit])
  return publicAnnouncementListItems(rows)
}

export function findPublicAnnouncement(id: string): PublicAnnouncementDetail | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT id, title, content, level, published_at
    FROM announcements
    WHERE id = ?
      AND status = 'published'
      AND published_at IS NOT NULL
  `).get(id) as unknown as PublicAnnouncementDetailRow | undefined
  return row ? publicAnnouncementDetail(row) : undefined
}

export async function findPublicAnnouncementAsync(id: string): Promise<PublicAnnouncementDetail | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_public_announcement_read_only',
      id
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findPublicAnnouncement(id)
  }
  const client = await announcementDatabaseClient()
  const row = await client.one<PublicAnnouncementDetailRow>(`
    SELECT id, title, content, level, published_at
    FROM ${announcementTable(client, 'announcements')}
    WHERE id = ?
      AND status = 'published'
      AND published_at IS NOT NULL
  `, [id])
  return row ? publicAnnouncementDetail(row) : undefined
}

export function markPublicAnnouncementsRead(systemAccountId: string, announcementIds: string[]): AnnouncementReadResult {
  const ids = [...new Set(announcementIds.map((id) => id.trim()).filter(Boolean))].slice(0, publicAnnouncementLimit)
  const readAt = nowIso()
  if (!ids.length) return { readAt, count: 0 }

  const database = getBusinessDatabase()
  const publishedRows = database
    .prepare(`
      SELECT id
      FROM announcements
      WHERE id IN (${sqlPlaceholders(ids.length)})
        AND status = 'published'
        AND published_at IS NOT NULL
    `)
    .all(...ids) as unknown as Array<{ id: string }>

  if (!publishedRows.length) return { readAt, count: 0 }

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const statement = database.prepare(`
      INSERT INTO announcement_reads (announcement_id, system_account_id, read_at)
      VALUES (?, ?, ?)
      ON CONFLICT(announcement_id, system_account_id)
      DO UPDATE SET read_at = excluded.read_at
    `)
    for (const row of publishedRows) {
      statement.run(row.id, systemAccountId, readAt)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
      // Ignore rollback failures so the original write error is preserved.
    }
    throw error
  }
  return { readAt, count: publishedRows.length }
}

export async function markPublicAnnouncementsReadAsync(systemAccountId: string, announcementIds: string[]): Promise<AnnouncementReadResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return markPublicAnnouncementsRead(systemAccountId, announcementIds)
  }
  const ids = normalizedAnnouncementReadIds(announcementIds)
  const readAt = nowIso()
  if (!ids.length) return { readAt, count: 0 }

  const client = await announcementDatabaseClient()
  const publishedRows = await client.query<{ id: string }>(`
    SELECT id
    FROM ${announcementTable(client, 'announcements')}
    WHERE id IN (${ids.map(() => '?').join(', ')})
      AND status = 'published'
      AND published_at IS NOT NULL
  `, ids)

  if (!publishedRows.length) return { readAt, count: 0 }

  await client.transaction(async (tx) => {
    for (const row of publishedRows) {
      await tx.execute(`
        INSERT INTO ${announcementTable(tx, 'announcement_reads')} (announcement_id, system_account_id, read_at)
        VALUES (?, ?, ?)
        ON CONFLICT(announcement_id, system_account_id)
        DO UPDATE SET read_at = excluded.read_at
      `, [row.id, systemAccountId, readAt])
    }
  })
  return { readAt, count: publishedRows.length }
}

export function listAnnouncements(): AnnouncementListItem[] {
  return listAnnouncementsPage({ page: 1, pageSize: maxAnnouncementPageSize }).items
}

export async function listAnnouncementsAsync(): Promise<AnnouncementListItem[]> {
  return (await listAnnouncementsPageAsync({ page: 1, pageSize: maxAnnouncementPageSize })).items
}

export function listAnnouncementsPage(options: AnnouncementListOptions = {}): AnnouncementListResult {
  const normalized = normalizeAnnouncementListOptions(options)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${announcementListSelectColumns()}
      FROM announcements
      ORDER BY updated_at DESC, created_at DESC, id DESC
      LIMIT ? OFFSET ?
    `)
    .all(normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize) as unknown as AnnouncementListRow[]
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = announcementListItems(pageRows.rows)
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export async function listAnnouncementsPageAsync(options: AnnouncementListOptions = {}): Promise<AnnouncementListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_announcements_page_read_only',
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAnnouncementsPage(options)
  }
  const normalized = normalizeAnnouncementListOptions(options)
  const client = await announcementDatabaseClient()
  const rows = await client.query<AnnouncementListRow>(`
    SELECT ${announcementListSelectColumns()}
    FROM ${announcementTable(client, 'announcements')} announcements
    ORDER BY updated_at DESC, created_at DESC, id DESC
    LIMIT ? OFFSET ?
  `, [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize])
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = await announcementListItemsAsync(pageRows.rows, client)
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export function findAnnouncement(id: string): AnnouncementSummary | undefined {
  const row = getAnnouncementRow(id)
  return row ? announcementSummaries([row], true)[0] : undefined
}

export async function findAnnouncementAsync(id: string): Promise<AnnouncementSummary | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_announcement_read_only',
      id
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findAnnouncement(id)
  }
  const client = await announcementDatabaseClient()
  const row = await getAnnouncementRowAsync(id, client)
  return row ? (await announcementSummariesAsync([row], true, client))[0] : undefined
}

export function createAnnouncement(input: AnnouncementInput, actorSystemAccountId: string): AnnouncementSummary {
  assertKnownInputKeys(input, announcementInputKeys, '公告')
  const now = nowIso()
  const status = normalizeStatus(input.status, 'draft')
  const id = newId('ann')
  getBusinessDatabase()
    .prepare(`
      INSERT INTO announcements (
        id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      id,
      normalizeRequiredText(input.title),
      normalizeRequiredText(input.content),
      normalizeLevel(input.level, 'info'),
      status,
      actorSystemAccountId,
      actorSystemAccountId,
      status === 'published' ? now : null,
      now,
      now
    )
  return getAnnouncementOrThrow(id)
}

export async function createAnnouncementAsync(input: AnnouncementInput, actorSystemAccountId: string): Promise<AnnouncementSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createAnnouncement(input, actorSystemAccountId)
  }
  assertKnownInputKeys(input, announcementInputKeys, '公告')
  const now = nowIso()
  const status = normalizeStatus(input.status, 'draft')
  const id = newId('ann')
  const client = await announcementDatabaseClient()
  await client.execute(`
    INSERT INTO ${announcementTable(client, 'announcements')} (
      id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    id,
    normalizeRequiredText(input.title),
    normalizeRequiredText(input.content),
    normalizeLevel(input.level, 'info'),
    status,
    actorSystemAccountId,
    actorSystemAccountId,
    status === 'published' ? now : null,
    now,
    now
  ])
  return await getAnnouncementOrThrowAsync(id, client)
}

export function updateAnnouncement(id: string, input: Partial<AnnouncementInput>, actorSystemAccountId: string): AnnouncementSummary | undefined {
  assertKnownInputKeys(input, announcementInputKeys, '公告')
  const current = getAnnouncementRow(id)
  if (!current) return undefined

  const now = nowIso()
  const nextStatus = input.status ? normalizeStatus(input.status, current.status) : current.status
  const shouldResetReadState = nextStatus === 'published' && current.status !== 'published'
  const nextPublishedAt = shouldResetReadState ? now : current.published_at

  const database = getBusinessDatabase()
  database
    .prepare(`
      UPDATE announcements
      SET title = ?,
          content = ?,
          level = ?,
          status = ?,
          updated_by = ?,
          published_at = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(
      input.title === undefined ? current.title : normalizeRequiredText(input.title),
      input.content === undefined ? current.content : normalizeRequiredText(input.content),
      input.level === undefined ? current.level : normalizeLevel(input.level, current.level),
      nextStatus,
      actorSystemAccountId,
      nextPublishedAt,
      now,
      id
    )
  if (shouldResetReadState) {
    database.prepare('DELETE FROM announcement_reads WHERE announcement_id = ?').run(id)
  }
  return getAnnouncementOrThrow(id)
}

export async function updateAnnouncementAsync(id: string, input: Partial<AnnouncementInput>, actorSystemAccountId: string): Promise<AnnouncementSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateAnnouncement(id, input, actorSystemAccountId)
  }
  assertKnownInputKeys(input, announcementInputKeys, '公告')
  const client = await announcementDatabaseClient()
  const current = await getAnnouncementRowAsync(id, client)
  if (!current) return undefined

  const now = nowIso()
  const nextStatus = input.status ? normalizeStatus(input.status, current.status) : current.status
  const shouldResetReadState = nextStatus === 'published' && current.status !== 'published'
  const nextPublishedAt = shouldResetReadState ? now : current.published_at

  return await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${announcementTable(tx, 'announcements')}
      SET title = ?,
          content = ?,
          level = ?,
          status = ?,
          updated_by = ?,
          published_at = ?,
          updated_at = ?
      WHERE id = ?
    `, [
      input.title === undefined ? current.title : normalizeRequiredText(input.title),
      input.content === undefined ? current.content : normalizeRequiredText(input.content),
      input.level === undefined ? current.level : normalizeLevel(input.level, current.level),
      nextStatus,
      actorSystemAccountId,
      nextPublishedAt,
      now,
      id
    ])
    if (shouldResetReadState) {
      await tx.execute(`DELETE FROM ${announcementTable(tx, 'announcement_reads')} WHERE announcement_id = ?`, [id])
    }
    return await getAnnouncementOrThrowAsync(id, tx)
  })
}

export function publishAnnouncement(id: string, actorSystemAccountId: string): AnnouncementSummary | undefined {
  const current = getAnnouncementRow(id)
  if (!current) return undefined
  const now = nowIso()
  const database = getBusinessDatabase()
  database
    .prepare(`
      UPDATE announcements
      SET status = 'published',
          updated_by = ?,
          published_at = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(actorSystemAccountId, now, now, id)
  database.prepare('DELETE FROM announcement_reads WHERE announcement_id = ?').run(id)
  return getAnnouncementOrThrow(id)
}

export async function publishAnnouncementAsync(id: string, actorSystemAccountId: string): Promise<AnnouncementSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return publishAnnouncement(id, actorSystemAccountId)
  }
  const client = await announcementDatabaseClient()
  const current = await getAnnouncementRowAsync(id, client)
  if (!current) return undefined
  const now = nowIso()
  return await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${announcementTable(tx, 'announcements')}
      SET status = 'published',
          updated_by = ?,
          published_at = ?,
          updated_at = ?
      WHERE id = ?
    `, [actorSystemAccountId, now, now, id])
    await tx.execute(`DELETE FROM ${announcementTable(tx, 'announcement_reads')} WHERE announcement_id = ?`, [id])
    return await getAnnouncementOrThrowAsync(id, tx)
  })
}

export function unpublishAnnouncement(id: string, actorSystemAccountId: string): AnnouncementSummary | undefined {
  const current = getAnnouncementRow(id)
  if (!current) return undefined
  const now = nowIso()
  getBusinessDatabase()
    .prepare(`
      UPDATE announcements
      SET status = 'archived',
          updated_by = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(actorSystemAccountId, now, id)
  return getAnnouncementOrThrow(id)
}

export async function unpublishAnnouncementAsync(id: string, actorSystemAccountId: string): Promise<AnnouncementSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return unpublishAnnouncement(id, actorSystemAccountId)
  }
  const client = await announcementDatabaseClient()
  const current = await getAnnouncementRowAsync(id, client)
  if (!current) return undefined
  const now = nowIso()
  await client.execute(`
    UPDATE ${announcementTable(client, 'announcements')}
    SET status = 'archived',
        updated_by = ?,
        updated_at = ?
    WHERE id = ?
  `, [actorSystemAccountId, now, id])
  return await getAnnouncementOrThrowAsync(id, client)
}

export function deleteAnnouncement(id: string): boolean {
  const result = getBusinessDatabase().prepare('DELETE FROM announcements WHERE id = ?').run(id)
  return Number(result.changes ?? 0) > 0
}

export async function deleteAnnouncementAsync(id: string): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return deleteAnnouncement(id)
  }
  const client = await announcementDatabaseClient()
  const result = await client.execute(`DELETE FROM ${announcementTable(client, 'announcements')} WHERE id = ?`, [id])
  return result.changes > 0
}

function normalizedAnnouncementReadIds(announcementIds: string[]): string[] {
  return [...new Set(announcementIds.map((id) => id.trim()).filter(Boolean))].slice(0, publicAnnouncementLimit)
}

function normalizePublicLimit(limit: unknown): number {
  const value = Number(limit)
  if (!Number.isFinite(value) || value <= 0) return publicAnnouncementLimit
  return Math.max(1, Math.min(publicAnnouncementLimit, Math.floor(value)))
}

function normalizeRequiredText(value: unknown): string {
  if (typeof value !== 'string') {
    throw new Error('公告文本必须是字符串')
  }
  const text = value.trim()
  if (!text) {
    throw new Error('公告文本不能为空')
  }
  return text
}

function normalizeLevel(value: unknown, fallback: AnnouncementLevel): AnnouncementLevel {
  if (value === undefined) return fallback
  if (typeof value === 'string' && announcementLevels.includes(value as AnnouncementLevel)) {
    return value as AnnouncementLevel
  }
  throw new Error('公告级别无效')
}

function normalizeStatus(value: unknown, fallback: AnnouncementStatus): AnnouncementStatus {
  if (value === undefined) return fallback
  if (typeof value === 'string' && announcementStatuses.includes(value as AnnouncementStatus)) {
    return value as AnnouncementStatus
  }
  throw new Error('公告状态无效')
}

function normalizeAnnouncementListOptions(options: AnnouncementListOptions): Required<AnnouncementListOptions> {
  const pageSize = typeof options.pageSize === 'number' && Number.isInteger(options.pageSize)
    ? Math.min(maxAnnouncementPageSize, Math.max(1, options.pageSize))
    : defaultAnnouncementPageSize
  const page = normalizeListPage(options.page, pageSize)
  return { page, pageSize }
}

function getAnnouncementRow(id: string): AnnouncementRow | undefined {
  return getBusinessDatabase().prepare('SELECT * FROM announcements WHERE id = ?').get(id) as unknown as AnnouncementRow | undefined
}

async function getAnnouncementRowAsync(id: string, client: DatabaseClient): Promise<AnnouncementRow | undefined> {
  return await client.one<AnnouncementRow>(`SELECT * FROM ${announcementTable(client, 'announcements')} WHERE id = ?`, [id])
}

function announcementListSelectColumns(): string {
  return [
    'id',
    'title',
    "CASE WHEN length(content) > 240 THEN substr(content, 1, 240) || '...' ELSE content END AS content_preview",
    'level',
    'status',
    'created_by',
    'updated_by',
    'published_at',
    'created_at',
    'updated_at'
  ].join(', ')
}

function getAnnouncementOrThrow(id: string): AnnouncementSummary {
  const row = getAnnouncementRow(id)
  if (!row) throw new Error('公告不存在')
  return announcementSummaries([row], true)[0]
}

async function getAnnouncementOrThrowAsync(id: string, client: DatabaseClient): Promise<AnnouncementSummary> {
  const row = await getAnnouncementRowAsync(id, client)
  if (!row) throw new Error('公告不存在')
  return (await announcementSummariesAsync([row], true, client))[0]
}

function publicAnnouncementListItems(rows: PublicAnnouncementListRow[]): PublicAnnouncementListItem[] {
  return rows.map((row) => ({
    id: row.id,
    title: row.title,
    level: row.level,
    publishedAt: row.published_at,
    readAt: row.read_at ?? undefined
  }))
}

function publicAnnouncementDetail(row: PublicAnnouncementDetailRow): PublicAnnouncementDetail {
  return {
    id: row.id,
    title: row.title,
    content: row.content,
    level: row.level,
    publishedAt: row.published_at
  }
}

function announcementListItems(rows: AnnouncementListRow[]): AnnouncementListItem[] {
  const accountMap = loadSystemAccountPrincipalMapByIds(rows.flatMap((row) => [row.created_by, row.updated_by ?? '']).filter(Boolean))
  return rows.map((row) => announcementListItem(row, accountMap))
}

async function announcementListItemsAsync(rows: AnnouncementListRow[], client: DatabaseClient): Promise<AnnouncementListItem[]> {
  const accountMap = await loadSystemAccountPrincipalMapByIdsAsync(client, rows.flatMap((row) => [row.created_by, row.updated_by ?? '']).filter(Boolean))
  return rows.map((row) => announcementListItem(row, accountMap))
}

function announcementListItem(
  row: AnnouncementListRow,
  accountMap: Map<string, { displayName: string }>
): AnnouncementListItem {
  const createdBy = accountMap.get(row.created_by)
  const updatedBy = row.updated_by ? accountMap.get(row.updated_by) : undefined
  return {
    id: row.id,
    title: row.title,
    contentPreview: row.content_preview,
    level: row.level,
    status: row.status,
    createdBy: row.created_by,
    createdByName: createdBy?.displayName,
    updatedBy: row.updated_by ?? undefined,
    updatedByName: updatedBy?.displayName,
    publishedAt: row.published_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function announcementSummaries(rows: AnnouncementRow[], includeActors: boolean): AnnouncementSummary[] {
  const accountMap = includeActors
    ? loadSystemAccountPrincipalMapByIds(rows.flatMap((row) => [row.created_by, row.updated_by ?? '']).filter(Boolean))
    : new Map()
  return rows.map((row) => {
    const createdBy = accountMap.get(row.created_by)
    const updatedBy = row.updated_by ? accountMap.get(row.updated_by) : undefined
    const summary: AnnouncementSummary = {
      id: row.id,
      title: row.title,
      content: row.content,
      level: row.level,
      status: row.status,
      publishedAt: row.published_at ?? undefined,
      createdAt: row.created_at,
      updatedAt: row.updated_at
    }
    if (includeActors) {
      summary.createdBy = row.created_by
      summary.createdByName = createdBy?.displayName
      summary.updatedBy = row.updated_by ?? undefined
      summary.updatedByName = updatedBy?.displayName
    }
    return summary
  })
}

async function announcementSummariesAsync(rows: AnnouncementRow[], includeActors: boolean, client: DatabaseClient): Promise<AnnouncementSummary[]> {
  const accountMap = includeActors
    ? await loadSystemAccountPrincipalMapByIdsAsync(client, rows.flatMap((row) => [row.created_by, row.updated_by ?? '']).filter(Boolean))
    : new Map()
  return rows.map((row) => {
    const createdBy = accountMap.get(row.created_by)
    const updatedBy = row.updated_by ? accountMap.get(row.updated_by) : undefined
    const summary: AnnouncementSummary = {
      id: row.id,
      title: row.title,
      content: row.content,
      level: row.level,
      status: row.status,
      publishedAt: row.published_at ?? undefined,
      createdAt: row.created_at,
      updatedAt: row.updated_at
    }
    if (includeActors) {
      summary.createdBy = row.created_by
      summary.createdByName = createdBy?.displayName
      summary.updatedBy = row.updated_by ?? undefined
      summary.updatedByName = updatedBy?.displayName
    }
    return summary
  })
}

function assertKnownInputKeys(input: object, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input as Record<string, unknown>).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

async function announcementDatabaseClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function announcementTable(client: DatabaseClient, tableName: 'announcements' | 'announcement_reads'): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
