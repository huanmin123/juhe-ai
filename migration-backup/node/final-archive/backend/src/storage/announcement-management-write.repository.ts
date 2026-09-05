import type { AnnouncementLevel, AnnouncementStatus } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

const announcementLevels: readonly AnnouncementLevel[] = ['critical', 'warning', 'info', 'normal']
const announcementStatuses: readonly AnnouncementStatus[] = ['draft', 'published', 'archived']
const announcementPatchKeys = new Set(['title', 'content', 'level', 'status'])

export interface AnnouncementManagementCreateInput {
  title: string
  content: string
  level?: AnnouncementLevel
  status?: AnnouncementStatus
}

export interface AnnouncementManagementPatchInput {
  title?: string
  content?: string
  level?: AnnouncementLevel
  status?: AnnouncementStatus
}

export interface AnnouncementMutationReceipt {
  id: string
  revision: string
}

export interface AnnouncementMutationState {
  id: string
  title: string
  content?: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  publishedAt?: string
  revision: string
}

export interface AnnouncementManagementMutationOutcome {
  receipt: AnnouncementMutationReceipt
  before?: AnnouncementMutationState
  after?: AnnouncementMutationState
  changed: boolean
}

interface AnnouncementMutationRow {
  id: string
  title: string
  content?: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  published_at: string | null
  updated_at: string
}

export class AnnouncementRevisionConflictError extends Error {
  constructor(
    readonly announcementId: string,
    readonly expectedRevision: string,
    readonly currentRevision?: string
  ) {
    super('公告已被其他操作更新，请刷新后重试')
    this.name = 'AnnouncementRevisionConflictError'
  }
}

export async function createAnnouncementForManagementAsync(
  input: AnnouncementManagementCreateInput,
  actorSystemAccountId: string
): Promise<AnnouncementManagementMutationOutcome> {
  assertKnownPatchKeys(input)
  const title = normalizeRequiredText(input.title)
  const content = normalizeRequiredText(input.content)
  const level = normalizeLevel(input.level, 'info')
  const status = normalizeStatus(input.status, 'draft')
  const revision = nowIso()
  const id = newId('ann')
  const client = await announcementManagementClient()

  await client.execute(`
    INSERT INTO ${announcementTable(client, 'announcements')} (
      id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    id,
    title,
    content,
    level,
    status,
    actorSystemAccountId,
    actorSystemAccountId,
    status === 'published' ? revision : null,
    revision,
    revision
  ])

  const after: AnnouncementMutationState = {
    id,
    title,
    content,
    level,
    status,
    publishedAt: status === 'published' ? revision : undefined,
    revision
  }
  return {
    receipt: { id, revision },
    after,
    changed: true
  }
}

export async function patchAnnouncementForManagementAsync(
  id: string,
  input: AnnouncementManagementPatchInput,
  actorSystemAccountId: string,
  expectedRevision: string
): Promise<AnnouncementManagementMutationOutcome | undefined> {
  assertKnownPatchKeys(input)
  const client = await announcementManagementClient()
  const includeContent = Object.prototype.hasOwnProperty.call(input, 'content')

  return client.transaction(async (tx) => {
    const currentRow = await findAnnouncementMutationRow(tx, id, includeContent, true)
    if (!currentRow) return undefined
    const current = announcementMutationState(currentRow)
    assertExpectedRevision(current, expectedRevision)

    const assignments: string[] = []
    const params: unknown[] = []
    let nextTitle = current.title
    let nextContent = current.content
    let nextLevel = current.level
    let nextStatus = current.status

    if (input.title !== undefined) {
      nextTitle = normalizeRequiredText(input.title)
      addChangedAssignment(assignments, params, 'title', current.title, nextTitle)
    }
    if (input.content !== undefined) {
      nextContent = normalizeRequiredText(input.content)
      addChangedAssignment(assignments, params, 'content', current.content, nextContent)
    }
    if (input.level !== undefined) {
      nextLevel = normalizeLevel(input.level, current.level)
      addChangedAssignment(assignments, params, 'level', current.level, nextLevel)
    }
    if (input.status !== undefined) {
      nextStatus = normalizeStatus(input.status, current.status)
      addChangedAssignment(assignments, params, 'status', current.status, nextStatus)
    }

    if (assignments.length === 0) {
      return {
        receipt: { id: current.id, revision: current.revision },
        before: current,
        after: current,
        changed: false
      }
    }

    const revision = nextAnnouncementRevision(current.revision)
    const becamePublished = nextStatus === 'published' && current.status !== 'published'
    if (becamePublished) {
      assignments.push('published_at = ?')
      params.push(revision)
    }
    assignments.push('updated_by = ?', 'updated_at = ?')
    params.push(actorSystemAccountId, revision, id, current.revision)

    const result = await tx.execute(`
      UPDATE ${announcementTable(tx, 'announcements')}
      SET ${assignments.join(', ')}
      WHERE id = ? AND updated_at = ?
    `, params)
    if (result.changes !== 1) {
      throw new AnnouncementRevisionConflictError(id, expectedRevision)
    }
    if (becamePublished) {
      await tx.execute(`
        DELETE FROM ${announcementTable(tx, 'announcement_reads')}
        WHERE announcement_id = ?
      `, [id])
    }

    const after: AnnouncementMutationState = {
      id,
      title: nextTitle,
      content: includeContent ? nextContent : undefined,
      level: nextLevel,
      status: nextStatus,
      publishedAt: becamePublished ? revision : current.publishedAt,
      revision
    }
    return {
      receipt: { id, revision },
      before: current,
      after,
      changed: true
    }
  })
}

export function publishAnnouncementForManagementAsync(
  id: string,
  actorSystemAccountId: string,
  expectedRevision: string
): Promise<AnnouncementManagementMutationOutcome | undefined> {
  return patchAnnouncementForManagementAsync(id, { status: 'published' }, actorSystemAccountId, expectedRevision)
}

export function unpublishAnnouncementForManagementAsync(
  id: string,
  actorSystemAccountId: string,
  expectedRevision: string
): Promise<AnnouncementManagementMutationOutcome | undefined> {
  return patchAnnouncementForManagementAsync(id, { status: 'archived' }, actorSystemAccountId, expectedRevision)
}

export async function deleteAnnouncementForManagementAsync(
  id: string,
  expectedRevision: string
): Promise<AnnouncementManagementMutationOutcome | undefined> {
  const client = await announcementManagementClient()
  return client.transaction(async (tx) => {
    const currentRow = await findAnnouncementMutationRow(tx, id, false, true)
    if (!currentRow) return undefined
    const current = announcementMutationState(currentRow)
    assertExpectedRevision(current, expectedRevision)

    const result = await tx.execute(`
      DELETE FROM ${announcementTable(tx, 'announcements')}
      WHERE id = ? AND updated_at = ?
    `, [id, current.revision])
    if (result.changes !== 1) {
      throw new AnnouncementRevisionConflictError(id, expectedRevision)
    }
    return {
      receipt: { id, revision: current.revision },
      before: current,
      changed: true
    }
  })
}

async function findAnnouncementMutationRow(
  client: DatabaseClient,
  id: string,
  includeContent: boolean,
  lock: boolean
): Promise<AnnouncementMutationRow | undefined> {
  const columns = [
    'id',
    'title',
    ...(includeContent ? ['content'] : []),
    'level',
    'status',
    'published_at',
    'updated_at'
  ]
  return client.one<AnnouncementMutationRow>(`
    SELECT ${columns.join(', ')}
    FROM ${announcementTable(client, 'announcements')}
    WHERE id = ?
    ${lock && client.driver === 'postgres' ? 'FOR UPDATE' : ''}
  `, [id])
}

function announcementMutationState(row: AnnouncementMutationRow): AnnouncementMutationState {
  return {
    id: row.id,
    title: row.title,
    content: row.content,
    level: row.level,
    status: row.status,
    publishedAt: row.published_at ?? undefined,
    revision: row.updated_at
  }
}

function assertExpectedRevision(current: AnnouncementMutationState, expectedRevision: string): void {
  const normalizedExpectedRevision = expectedRevision.trim()
  if (!normalizedExpectedRevision || current.revision !== normalizedExpectedRevision) {
    throw new AnnouncementRevisionConflictError(current.id, normalizedExpectedRevision, current.revision)
  }
}

function addChangedAssignment(
  assignments: string[],
  params: unknown[],
  column: 'title' | 'content' | 'level' | 'status',
  current: unknown,
  next: unknown
): void {
  if (current === next) return
  assignments.push(`${column} = ?`)
  params.push(next)
}

function nextAnnouncementRevision(currentRevision: string): string {
  const currentTime = rfc3339InstantMilliseconds(currentRevision)
  if (currentTime === undefined) throw new Error(`公告 revision 必须是带 Z 或数值 offset 的 RFC3339 时间：${currentRevision}`)
  const now = Date.now()
  return new Date(currentTime >= now ? currentTime + 1 : now).toISOString()
}

function normalizeRequiredText(value: unknown): string {
  if (typeof value !== 'string') throw new Error('公告文本必须是字符串')
  const text = value.trim()
  if (!text) throw new Error('公告文本不能为空')
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

function assertKnownPatchKeys(input: object): void {
  const unknownKeys = Object.keys(input as Record<string, unknown>).filter((key) => !announcementPatchKeys.has(key))
  if (unknownKeys.length > 0) {
    throw new Error(`公告包含未知字段：${unknownKeys.join('、')}`)
  }
}

async function announcementManagementClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

function announcementTable(client: DatabaseClient, tableName: 'announcements' | 'announcement_reads'): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
