import type { AnnouncementLevel, AnnouncementStatus, AnnouncementSummary } from '../domain/types.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadSystemAccountPrincipalMapByIds } from './repository-lookups.js'
import type { AnnouncementRow } from './repository-row-types.js'

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

type PublicAnnouncementRow = AnnouncementRow & { read_at: string | null }

export interface AnnouncementReadResult {
  readAt: string
  count: number
}

export interface AnnouncementListOptions {
  page?: number
  pageSize?: number
}

export interface AnnouncementListResult {
  items: AnnouncementSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export function listPublicAnnouncements(systemAccountId: string, limit = publicAnnouncementLimit): AnnouncementSummary[] {
  const safeLimit = normalizePublicLimit(limit)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT
        announcements.*,
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
    .all(systemAccountId, safeLimit) as unknown as PublicAnnouncementRow[]
  return announcementSummaries(rows, false)
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

export function listAnnouncements(): AnnouncementSummary[] {
  return listAnnouncementsPage({ page: 1, pageSize: maxAnnouncementPageSize }).items
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
    .all(normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize) as unknown as AnnouncementRow[]
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = announcementSummaries(pageRows.rows, true)
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

export function deleteAnnouncement(id: string): boolean {
  const result = getBusinessDatabase().prepare('DELETE FROM announcements WHERE id = ?').run(id)
  return Number(result.changes ?? 0) > 0
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

function announcementListSelectColumns(): string {
  return [
    'id',
    'title',
    "CASE WHEN length(content) > 240 THEN substr(content, 1, 240) || '...' ELSE content END AS content",
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

function announcementSummaries(rows: Array<AnnouncementRow | PublicAnnouncementRow>, includeActors: boolean): AnnouncementSummary[] {
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
      readAt: 'read_at' in row ? row.read_at ?? undefined : undefined,
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
