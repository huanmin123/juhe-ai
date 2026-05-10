import type { AnnouncementLevel, AnnouncementStatus, AnnouncementSummary } from '../domain/types.js'
import { getDatabase, newId, nowIso } from './database.js'
import { sqlPlaceholders } from './query-utils.js'
import { loadSystemAccountsByIds } from './repository-lookups.js'
import type { AnnouncementRow } from './repository-row-types.js'

const announcementLevels: readonly AnnouncementLevel[] = ['critical', 'warning', 'info', 'normal']
const announcementStatuses: readonly AnnouncementStatus[] = ['draft', 'published', 'archived']
const publicAnnouncementLimit = 30

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

export function listPublicAnnouncements(systemAccountId: string, limit = publicAnnouncementLimit): AnnouncementSummary[] {
  const safeLimit = normalizePublicLimit(limit)
  const rows = getDatabase()
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
      ORDER BY announcements.published_at DESC, announcements.created_at DESC
      LIMIT ?
    `)
    .all(systemAccountId, safeLimit) as unknown as PublicAnnouncementRow[]
  return announcementSummaries(rows, false)
}

export function markPublicAnnouncementsRead(systemAccountId: string, announcementIds: string[]): AnnouncementReadResult {
  const ids = [...new Set(announcementIds.map((id) => id.trim()).filter(Boolean))].slice(0, publicAnnouncementLimit)
  const readAt = nowIso()
  if (!ids.length) return { readAt, count: 0 }

  const database = getDatabase()
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

  try {
    database.exec('BEGIN')
    const statement = database.prepare(`
      INSERT INTO announcement_reads (announcement_id, system_account_id, read_at)
      VALUES (?, ?, ?)
      ON CONFLICT(announcement_id, system_account_id)
      DO UPDATE SET read_at = excluded.read_at
    `)
    for (const row of publishedRows) {
      statement.run(row.id, systemAccountId, readAt)
    }
    database.exec('COMMIT')
  } catch (error) {
    try {
      database.exec('ROLLBACK')
    } catch {
      // Ignore rollback failures so the original write error is preserved.
    }
    throw error
  }
  return { readAt, count: publishedRows.length }
}

export function listAnnouncements(): AnnouncementSummary[] {
  const rows = getDatabase()
    .prepare(`
      SELECT *
      FROM announcements
      ORDER BY updated_at DESC, created_at DESC
    `)
    .all() as unknown as AnnouncementRow[]
  return announcementSummaries(rows, true)
}

export function createAnnouncement(input: AnnouncementInput, actorSystemAccountId: string): AnnouncementSummary {
  const now = nowIso()
  const status = normalizeStatus(input.status, 'draft')
  const id = newId('ann')
  getDatabase()
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
  const current = getAnnouncementRow(id)
  if (!current) return undefined

  const now = nowIso()
  const nextStatus = input.status ? normalizeStatus(input.status, current.status) : current.status
  const shouldResetReadState = nextStatus === 'published' && current.status !== 'published'
  const nextPublishedAt = shouldResetReadState ? now : current.published_at

  const database = getDatabase()
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
  const database = getDatabase()
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
  getDatabase()
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
  const result = getDatabase().prepare('DELETE FROM announcements WHERE id = ?').run(id)
  return Number(result.changes ?? 0) > 0
}

function normalizePublicLimit(limit: unknown): number {
  const value = Number(limit)
  if (!Number.isFinite(value) || value <= 0) return publicAnnouncementLimit
  return Math.max(1, Math.min(publicAnnouncementLimit, Math.floor(value)))
}

function normalizeRequiredText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function normalizeLevel(value: unknown, fallback: AnnouncementLevel): AnnouncementLevel {
  return typeof value === 'string' && announcementLevels.includes(value as AnnouncementLevel)
    ? value as AnnouncementLevel
    : fallback
}

function normalizeStatus(value: unknown, fallback: AnnouncementStatus): AnnouncementStatus {
  return typeof value === 'string' && announcementStatuses.includes(value as AnnouncementStatus)
    ? value as AnnouncementStatus
    : fallback
}

function getAnnouncementRow(id: string): AnnouncementRow | undefined {
  return getDatabase().prepare('SELECT * FROM announcements WHERE id = ?').get(id) as unknown as AnnouncementRow | undefined
}

function getAnnouncementOrThrow(id: string): AnnouncementSummary {
  const row = getAnnouncementRow(id)
  if (!row) throw new Error('公告不存在')
  return announcementSummaries([row], true)[0]
}

function announcementSummaries(rows: Array<AnnouncementRow | PublicAnnouncementRow>, includeActors: boolean): AnnouncementSummary[] {
  const accountMap = includeActors
    ? loadSystemAccountsByIds(rows.flatMap((row) => [row.created_by, row.updated_by ?? '']).filter(Boolean))
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
      summary.createdByName = createdBy?.displayName ?? createdBy?.username
      summary.updatedBy = row.updated_by ?? undefined
      summary.updatedByName = updatedBy?.displayName ?? updatedBy?.username
    }
    return summary
  })
}
