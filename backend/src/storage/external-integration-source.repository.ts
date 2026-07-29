import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, newId, nowIso, runInDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { isBuiltInExternalIntegrationTestSourceId } from './external-integration-source-constants.js'
import { mapSourceListItem, mapSourceRecord, mapSourceSummary } from './external-integration-source-mappers.js'
import {
  decodeRateLimits,
  decodeScopes,
  encodeRateLimits,
  encodeScopes,
  normalizeNullableIso,
  normalizeNullableText,
  normalizeSourceStatus,
  normalizeSourceStatusInput
} from './external-integration-source-normalizers.js'
import {
  createExternalIntegrationSourceToken,
  createExternalIntegrationSourceTokenInClientAsync,
  loadExternalIntegrationSourcePrimaryTokensBySourceIds,
  loadExternalIntegrationSourcePrimaryTokensBySourceIdsAsync,
  loadExternalIntegrationSourceTokensBySourceIds,
  loadExternalIntegrationSourceTokensBySourceIdsAsync,
  latestExternalIntegrationSourceTokenUpdatedAt,
  lockExternalIntegrationSourceTokenUpdatedAtAsync,
  syncExternalIntegrationSourceTokenStatus,
  syncExternalIntegrationSourceTokenStatusAsync
} from './external-integration-source-token.repository.js'
import type {
  CreatedExternalIntegrationSourceAuthorization,
  ExternalIntegrationSourceDeleteReceipt,
  ExternalIntegrationSourceInput,
  ExternalIntegrationSourceListOptions,
  ExternalIntegrationSourceListResult,
  ExternalIntegrationSourceListProjectionRow,
  ExternalIntegrationSourceListRow,
  ExternalIntegrationSourcePatchChange,
  ExternalIntegrationSourcePatchOutcome,
  ExternalIntegrationSourceRecord,
  ExternalIntegrationSourceRow,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceUpdateInput
} from './external-integration-source-types.js'
import {
  assertKnownInputKeys,
  ExternalIntegrationSourcePatchConflictError,
  isUniqueConstraintError,
  nextExternalIntegrationUpdatedAt,
  normalizeNameOrThrow
} from './external-integration-source-write-helpers.js'
import { getPostgresPool } from './postgres-client.js'
import { normalizeListPage } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export {
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestTokenId,
  externalIntegrationAccountAddWriteScope,
  externalIntegrationAccountDeleteWriteScope,
  externalIntegrationAccountListReadScope,
  externalIntegrationAccountUpdateWriteScope,
  externalIntegrationApiKeyAddWriteScope,
  externalIntegrationApiKeyDeleteWriteScope,
  externalIntegrationApiKeyListReadScope,
  externalIntegrationApiKeyUpdateWriteScope,
  externalIntegrationGroupAddWriteScope,
  externalIntegrationGroupDeleteWriteScope,
  externalIntegrationGroupListReadScope,
  externalIntegrationGroupUpdateWriteScope,
  externalIntegrationRouteStrategyAddWriteScope,
  externalIntegrationRouteStrategyDeleteWriteScope,
  externalIntegrationRouteStrategyListReadScope,
  externalIntegrationRouteStrategyUpdateWriteScope,
  externalIntegrationScopeOptions
} from './external-integration-source-constants.js'

export {
  flushExternalIntegrationSourceLastUsedTouchesForTest,
  loadExternalIntegrationSourceTokenForAuthReadOnly,
  validateExternalIntegrationSourceToken,
  validateExternalIntegrationSourceTokenAsync
} from './external-integration-source-auth.repository.js'
export { ExternalIntegrationSourcePatchConflictError } from './external-integration-source-write-helpers.js'
export {
  createExternalIntegrationSourceToken,
  createExternalIntegrationSourceTokenAsync,
  findExternalIntegrationSourceTokenSecret,
  findExternalIntegrationSourceTokenSecretAsync,
  resetBuiltInExternalIntegrationTestToken,
  resetBuiltInExternalIntegrationTestTokenAsync,
  updateExternalIntegrationSourceTokenAsync,
  updateExternalIntegrationSourceToken
} from './external-integration-source-token.repository.js'

export type {
  CreatedExternalIntegrationSourceAuthorization,
  CreatedExternalIntegrationSourceToken,
  ExternalIntegrationRateLimitRule,
  ExternalIntegrationSourceAuthContext,
  ExternalIntegrationSourceDeleteReceipt,
  ExternalIntegrationSourceAuthResult,
  ExternalIntegrationSourceInput,
  ExternalIntegrationSourceListOptions,
  ExternalIntegrationSourceListResult,
  ExternalIntegrationSourceListItem,
  ExternalIntegrationSourceMutationResult,
  ExternalIntegrationSourcePatchOutcome,
  ExternalIntegrationSourcePrimaryTokenSummary,
  ExternalIntegrationSourceRecord,
  ExternalIntegrationSourceListRow,
  ExternalIntegrationSourceRow,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenInput,
  ExternalIntegrationSourceTokenListRow,
  ExternalIntegrationSourceTokenPatchOutcome,
  ExternalIntegrationSourceTokenRow,
  ExternalIntegrationSourceTokenSecret,
  ExternalIntegrationSourceTokenStatus,
  ExternalIntegrationSourceTokenSummary,
  ExternalIntegrationSourceTokenUpdateInput,
  ExternalIntegrationSourceUpdateInput
} from './external-integration-source-types.js'

const defaultPageSize = 20
const maxPageSize = 100
const externalIntegrationSourceInputKeys = new Set(['name', 'status', 'scopes', 'rateLimits', 'expiresAt', 'notes'])
const externalIntegrationSourceUpdateInputKeys = new Set(['expectedUpdatedAt', ...externalIntegrationSourceInputKeys])

export function listExternalIntegrationSources(options: ExternalIntegrationSourceListOptions = {}): ExternalIntegrationSourceListResult {
  const pageSize = Math.max(1, Math.min(Math.trunc(options.pageSize ?? defaultPageSize), maxPageSize))
  const page = normalizeListPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const where: string[] = []
  const params: SQLInputValue[] = []
  if (options.status && options.status !== 'all') {
    where.push('sources.status = ?')
    params.push(options.status)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    where.push("(sources.name = ? OR sources.name LIKE ? ESCAPE '\\')")
    params.push(keyword, `${escapeExternalIntegrationLikePrefix(keyword)}%`)
  }
  const whereSql = where.length ? `WHERE ${where.join(' AND ')}` : ''
  const rows = getBusinessDatabase().prepare(`
    SELECT
      sources.id,
      sources.name,
      sources.status,
      sources.scopes_json,
      sources.rate_limits_json,
      sources.expires_at,
      sources.notes,
      sources.last_used_at,
      sources.updated_at
    FROM external_integration_sources AS sources
    ${whereSql}
    ORDER BY sources.updated_at DESC, sources.id DESC
    LIMIT ? OFFSET ?
  `).all(...params, pageSize + 1, offset) as unknown as ExternalIntegrationSourceListProjectionRow[]

  const pageRows = rows.slice(0, pageSize)
  const pageSourceIds = pageRows.map((row) => row.id)
  const primaryTokensBySourceId = loadExternalIntegrationSourcePrimaryTokensBySourceIds(pageSourceIds)
  return {
    items: pageRows.map((row) => mapSourceListItem(row, primaryTokensBySourceId.get(row.id))),
    page,
    pageSize,
    pageUpperBound: offset + pageRows.length + (rows.length > pageSize ? 1 : 0),
    hasMore: rows.length > pageSize
  }
}

export async function listExternalIntegrationSourcesAsync(options: ExternalIntegrationSourceListOptions = {}): Promise<ExternalIntegrationSourceListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_external_integration_sources_read_only',
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listExternalIntegrationSources(options)
  }
  const pageSize = Math.max(1, Math.min(Math.trunc(options.pageSize ?? defaultPageSize), maxPageSize))
  const page = normalizeListPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const where: string[] = []
  const params: string[] = []
  if (options.status && options.status !== 'all') {
    where.push('sources.status = ?')
    params.push(options.status)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    where.push('(LOWER(sources.name) = LOWER(?) OR LOWER(sources.name) LIKE LOWER(?) ESCAPE \'\\\')')
    params.push(keyword, `${escapeExternalIntegrationLikePrefix(keyword)}%`)
  }
  const whereSql = where.length ? `WHERE ${where.join(' AND ')}` : ''
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<ExternalIntegrationSourceListProjectionRow>(`
    SELECT
      sources.id,
      sources.name,
      sources.status,
      sources.scopes_json,
      sources.rate_limits_json,
      sources.expires_at,
      sources.notes,
      sources.last_used_at,
      sources.updated_at
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')} AS sources
    ${whereSql}
    ORDER BY sources.updated_at DESC, sources.id DESC
    LIMIT ? OFFSET ?
  `, [...params, pageSize + 1, offset])

  const pageRows = rows.slice(0, pageSize)
  const pageSourceIds = pageRows.map((row) => row.id)
  const primaryTokensBySourceId = await loadExternalIntegrationSourcePrimaryTokensBySourceIdsAsync(pageSourceIds, client)
  return {
    items: pageRows.map((row) => mapSourceListItem(row, primaryTokensBySourceId.get(row.id))),
    page,
    pageSize,
    pageUpperBound: offset + pageRows.length + (rows.length > pageSize ? 1 : 0),
    hasMore: rows.length > pageSize
  }
}

export function findExternalIntegrationSource(id: string): ExternalIntegrationSourceSummary | undefined {
  const row = getBusinessDatabase()
    .prepare('SELECT *, 0 AS token_count, 0 AS active_token_count FROM external_integration_sources WHERE id = ?')
    .get(id) as ExternalIntegrationSourceListRow | undefined
  if (!row) {
    return undefined
  }
  const tokens = loadExternalIntegrationSourceTokensBySourceIds([row.id]).get(row.id) ?? []
  return mapSourceSummary({
    ...row,
    token_count: tokens.length,
    active_token_count: tokens.filter((token) => token.status === 'active').length
  }, tokens)
}

export async function findExternalIntegrationSourceAsync(id: string): Promise<ExternalIntegrationSourceSummary | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_external_integration_source_read_only',
      id
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findExternalIntegrationSource(id)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return findExternalIntegrationSourceInClientAsync(client, id)
}

async function findExternalIntegrationSourceInClientAsync(client: DatabaseClient, id: string): Promise<ExternalIntegrationSourceSummary | undefined> {
  const row = await client.one<ExternalIntegrationSourceListRow>(`
    SELECT *, 0 AS token_count, 0 AS active_token_count
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')}
    WHERE id = ?
  `, [id])
  if (!row) {
    return undefined
  }
  const tokens = (await loadExternalIntegrationSourceTokensBySourceIdsAsync([row.id], client)).get(row.id) ?? []
  return mapSourceSummary({
    ...row,
    token_count: tokens.length,
    active_token_count: tokens.filter((token) => token.status === 'active').length
  }, tokens)
}

export function createExternalIntegrationSource(input: ExternalIntegrationSourceInput): ExternalIntegrationSourceRecord {
  const row = buildExternalIntegrationSourceCreateRow(input)
  ensureSourceNameAvailable(row.name)
  try {
    getBusinessDatabase().prepare(`
      INSERT INTO external_integration_sources (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      row.id,
      row.name,
      row.status,
      row.scopes_json,
      row.rate_limits_json,
      row.expires_at,
      row.notes,
      row.created_at,
      row.updated_at
    )
  } catch (error) {
    if (isUniqueConstraintError(error)) {
      throw new Error('来源系统名称已存在')
    }
    throw error
  }
  return mapSourceRecord(row)
}

export async function createExternalIntegrationSourceAsync(input: ExternalIntegrationSourceInput): Promise<ExternalIntegrationSourceRecord> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createExternalIntegrationSource(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return createExternalIntegrationSourceInClientAsync(client, input)
}

async function createExternalIntegrationSourceInClientAsync(client: DatabaseClient, input: ExternalIntegrationSourceInput): Promise<ExternalIntegrationSourceRecord> {
  const row = buildExternalIntegrationSourceCreateRow(input)
  await ensureSourceNameAvailableAsync(client, row.name)
  try {
    await client.execute(`
      INSERT INTO ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')} (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [
      row.id,
      row.name,
      row.status,
      row.scopes_json,
      row.rate_limits_json,
      row.expires_at,
      row.notes,
      row.created_at,
      row.updated_at
    ])
  } catch (error) {
    if (isUniqueConstraintError(error)) {
      throw new Error('来源系统名称已存在')
    }
    throw error
  }
  return mapSourceRecord(row)
}

export function createExternalIntegrationSourceAuthorization(input: ExternalIntegrationSourceInput): CreatedExternalIntegrationSourceAuthorization {
  return runInDatabaseTransaction(() => {
    const source = createExternalIntegrationSource({
      ...input
    })
    const token = createExternalIntegrationSourceToken({
      sourceRefId: source.id,
      name: `${source.name} 生产 Token`,
      status: source.status,
      scopes: source.scopes,
      expiresAt: source.expiresAt ?? null
    })
    return {
      source,
      token
    }
  })
}

export async function createExternalIntegrationSourceAuthorizationAsync(input: ExternalIntegrationSourceInput): Promise<CreatedExternalIntegrationSourceAuthorization> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createExternalIntegrationSourceAuthorization(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return client.transaction(async (tx) => {
    const source = await createExternalIntegrationSourceInClientAsync(tx, input)
    const token = await createExternalIntegrationSourceTokenInClientAsync(tx, {
      sourceRefId: source.id,
      name: `${source.name} 生产 Token`,
      status: source.status,
      scopes: source.scopes,
      expiresAt: source.expiresAt ?? null
    })
    return {
      source,
      token
    }
  })
}

export function upsertExternalIntegrationSource(input: ExternalIntegrationSourceInput): { id: string; name: string } {
  assertKnownInputKeys(input, externalIntegrationSourceInputKeys, '来源系统')
  const name = normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  const database = getBusinessDatabase()
  const existing = database
    .prepare('SELECT id, name FROM external_integration_sources WHERE lower(name) = lower(?)')
    .get(name) as Pick<ExternalIntegrationSourceRow, 'id' | 'name'> | undefined
  const id = existing?.id ?? newId('extsrc')
  const now = nowIso()
  if (existing) {
    database.prepare(`
      UPDATE external_integration_sources
      SET name = ?, status = ?, scopes_json = ?, rate_limits_json = ?, expires_at = ?, notes = ?, updated_at = ?
      WHERE id = ?
    `).run(
      name,
      normalizeSourceStatusInput(input.status),
      encodeScopes(input.scopes),
      encodeRateLimits(input.rateLimits),
      normalizeNullableIso(input.expiresAt),
      normalizeNullableText(input.notes),
      now,
      id
    )
    return { id, name }
  }
  database.prepare(`
    INSERT INTO external_integration_sources (
      id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    id,
    name,
    normalizeSourceStatusInput(input.status),
    encodeScopes(input.scopes),
    encodeRateLimits(input.rateLimits),
    normalizeNullableIso(input.expiresAt),
    normalizeNullableText(input.notes),
    now,
    now
  )
  return { id, name }
}

export async function upsertExternalIntegrationSourceAsync(input: ExternalIntegrationSourceInput): Promise<{ id: string; name: string }> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return upsertExternalIntegrationSource(input)
  }
  assertKnownInputKeys(input, externalIntegrationSourceInputKeys, '来源系统')
  const name = normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const existing = await client.one<Pick<ExternalIntegrationSourceRow, 'id' | 'name'>>(`
    SELECT id, name
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')}
    WHERE lower(name) = lower(?)
    LIMIT 1
  `, [name])
  const id = existing?.id ?? newId('extsrc')
  const now = nowIso()
  if (existing) {
    await client.execute(`
      UPDATE ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')}
      SET name = ?, status = ?, scopes_json = ?, rate_limits_json = ?, expires_at = ?, notes = ?, updated_at = ?
      WHERE id = ?
    `, [
      name,
      normalizeSourceStatusInput(input.status),
      encodeScopes(input.scopes),
      encodeRateLimits(input.rateLimits),
      normalizeNullableIso(input.expiresAt),
      normalizeNullableText(input.notes),
      now,
      id
    ])
    return { id, name }
  }
  await client.execute(`
    INSERT INTO ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')} (
      id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    id,
    name,
    normalizeSourceStatusInput(input.status),
    encodeScopes(input.scopes),
    encodeRateLimits(input.rateLimits),
    normalizeNullableIso(input.expiresAt),
    normalizeNullableText(input.notes),
    now,
    now
  ])
  return { id, name }
}

export function updateExternalIntegrationSource(
  id: string,
  input: ExternalIntegrationSourceUpdateInput
): ExternalIntegrationSourcePatchOutcome | undefined {
  assertExternalIntegrationSourcePatchInput(id, input)
  return runInDatabaseTransaction(() => {
    const existing = findExternalIntegrationSourcePatchRow(id, input)
    if (!existing) return undefined
    if (existing.updated_at !== input.expectedUpdatedAt) throw new ExternalIntegrationSourcePatchConflictError()
    const update = buildExternalIntegrationSourceUpdate(input, existing)
    if (!update.columns.length) return sourcePatchOutcome(existing, update.changes, existing.updated_at)
    if (update.nameChanged) ensureSourceNameAvailable(update.sourceName, id)
    const tokenUpdatedAt = !isBuiltInExternalIntegrationTestSourceId(id) && update.nextStatus
      ? latestExternalIntegrationSourceTokenUpdatedAt(id)
      : undefined
    const updatedAt = nextExternalIntegrationUpdatedAt(latestExternalIntegrationUpdatedAt(existing.updated_at, tokenUpdatedAt))
    executeExternalIntegrationSourceUpdate(getBusinessDatabase(), id, input.expectedUpdatedAt, updatedAt, update.columns)
    if (!isBuiltInExternalIntegrationTestSourceId(id) && update.nextStatus) {
      syncExternalIntegrationSourceTokenStatus(id, update.nextStatus, updatedAt)
    }
    return sourcePatchOutcome(existing, update.changes, updatedAt)
  })
}

export async function updateExternalIntegrationSourceAsync(
  id: string,
  input: ExternalIntegrationSourceUpdateInput
): Promise<ExternalIntegrationSourcePatchOutcome | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateExternalIntegrationSource(id, input)
  }
  assertExternalIntegrationSourcePatchInput(id, input)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return client.transaction(async (tx) => {
    const existing = await findExternalIntegrationSourcePatchRowAsync(tx, id, input)
    if (!existing) return undefined
    if (existing.updated_at !== input.expectedUpdatedAt) throw new ExternalIntegrationSourcePatchConflictError()
    const update = buildExternalIntegrationSourceUpdate(input, existing)
    if (!update.columns.length) return sourcePatchOutcome(existing, update.changes, existing.updated_at)
    if (update.nameChanged) await ensureSourceNameAvailableAsync(tx, update.sourceName, id)
    const tokenUpdatedAt = !isBuiltInExternalIntegrationTestSourceId(id) && update.nextStatus
      ? await lockExternalIntegrationSourceTokenUpdatedAtAsync(tx, id)
      : undefined
    const updatedAt = nextExternalIntegrationUpdatedAt(latestExternalIntegrationUpdatedAt(existing.updated_at, tokenUpdatedAt))
    await executeExternalIntegrationSourceUpdateAsync(tx, id, input.expectedUpdatedAt, updatedAt, update.columns)
    if (!isBuiltInExternalIntegrationTestSourceId(id) && update.nextStatus) {
      await syncExternalIntegrationSourceTokenStatusAsync(id, update.nextStatus, updatedAt, tx)
    }
    return sourcePatchOutcome(existing, update.changes, updatedAt)
  })
}

type ExternalIntegrationSourcePatchRow = Pick<ExternalIntegrationSourceRow, 'id' | 'name' | 'updated_at'>
  & Partial<Pick<ExternalIntegrationSourceRow, 'status' | 'scopes_json' | 'rate_limits_json' | 'expires_at' | 'notes'>>

function assertExternalIntegrationSourcePatchInput(id: string, input: ExternalIntegrationSourceUpdateInput): void {
  assertKnownInputKeys(input, externalIntegrationSourceUpdateInputKeys, '来源系统')
  if (!input.expectedUpdatedAt?.trim()) throw new Error('外部来源配置版本不能为空')
  if (isBuiltInExternalIntegrationTestSourceId(id)) assertBuiltInSourceUpdateInput(input)
}

function externalIntegrationSourcePatchProjection(input: ExternalIntegrationSourceUpdateInput): string[] {
  const columns = ['id', 'name', 'updated_at']
  if (input.status !== undefined) columns.push('status')
  if (input.scopes !== undefined) columns.push('scopes_json')
  if (input.rateLimits !== undefined) columns.push('rate_limits_json')
  if (input.expiresAt !== undefined) columns.push('expires_at')
  if (input.notes !== undefined) columns.push('notes')
  return columns
}

function findExternalIntegrationSourcePatchRow(
  id: string,
  input: ExternalIntegrationSourceUpdateInput
): ExternalIntegrationSourcePatchRow | undefined {
  return getBusinessDatabase().prepare(`
    SELECT ${externalIntegrationSourcePatchProjection(input).join(', ')}
    FROM external_integration_sources
    WHERE id = ?
  `).get(id) as ExternalIntegrationSourcePatchRow | undefined
}

async function findExternalIntegrationSourcePatchRowAsync(
  client: DatabaseClient,
  id: string,
  input: ExternalIntegrationSourceUpdateInput
): Promise<ExternalIntegrationSourcePatchRow | undefined> {
  return client.one<ExternalIntegrationSourcePatchRow>(`
    SELECT ${externalIntegrationSourcePatchProjection(input).join(', ')}
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')}
    WHERE id = ?
    FOR UPDATE
  `, [id])
}

function sourcePatchOutcome(
  existing: ExternalIntegrationSourcePatchRow,
  changes: ExternalIntegrationSourcePatchChange[],
  updatedAt: string
): ExternalIntegrationSourcePatchOutcome {
  const renamed = changes.find((change) => change.field === 'name')?.after
  return {
    mutation: { id: existing.id, updatedAt },
    sourceName: typeof renamed === 'string' ? renamed : existing.name,
    changes
  }
}

function latestExternalIntegrationUpdatedAt(sourceUpdatedAt: string, tokenUpdatedAt: string | undefined): string {
  return tokenUpdatedAt && tokenUpdatedAt > sourceUpdatedAt ? tokenUpdatedAt : sourceUpdatedAt
}

interface ExternalIntegrationSourceUpdateColumn {
  column: 'name' | 'status' | 'scopes_json' | 'rate_limits_json' | 'expires_at' | 'notes'
  value: string | null
}

function buildExternalIntegrationSourceUpdate(
  input: ExternalIntegrationSourceUpdateInput,
  existing: ExternalIntegrationSourcePatchRow
): {
  columns: ExternalIntegrationSourceUpdateColumn[]
  sourceName: string
  nameChanged: boolean
  nextStatus?: 'active' | 'disabled'
  changes: ExternalIntegrationSourcePatchChange[]
} {
  const columns: ExternalIntegrationSourceUpdateColumn[] = []
  const changes: ExternalIntegrationSourcePatchChange[] = []
  let sourceName = existing.name
  let nextStatus: 'active' | 'disabled' | undefined
  if (input.name !== undefined) {
    const value = normalizeNameOrThrow(input.name, '来源系统名称不能为空')
    sourceName = value
    if (value !== existing.name) {
      columns.push({ column: 'name', value })
      changes.push({ field: 'name', before: existing.name, after: value })
    }
  }
  if (input.status !== undefined) {
    const before = normalizeSourceStatus(existing.status)
    const value = normalizeSourceStatusInput(input.status)
    if (value !== before) {
      columns.push({ column: 'status', value })
      changes.push({ field: 'status', before, after: value })
      nextStatus = value
    }
  }
  if (input.scopes !== undefined) {
    const value = encodeScopes(input.scopes)
    if (value !== existing.scopes_json) {
      columns.push({ column: 'scopes_json', value })
      changes.push({ field: 'scopes', before: decodeScopes(existing.scopes_json ?? '[]'), after: decodeScopes(value) })
    }
  }
  if (input.rateLimits !== undefined) {
    const value = encodeRateLimits(input.rateLimits)
    if (value !== existing.rate_limits_json) {
      columns.push({ column: 'rate_limits_json', value })
      changes.push({ field: 'rateLimits', before: decodeRateLimits(existing.rate_limits_json), after: decodeRateLimits(value) })
    }
  }
  if (input.expiresAt !== undefined) {
    const value = normalizeNullableIso(input.expiresAt)
    if (value !== existing.expires_at) {
      columns.push({ column: 'expires_at', value })
      changes.push({ field: 'expiresAt', before: existing.expires_at ?? undefined, after: value ?? undefined })
    }
  }
  if (input.notes !== undefined) {
    const value = normalizeNullableText(input.notes)
    if (value !== existing.notes) {
      columns.push({ column: 'notes', value })
      changes.push({ field: 'notes', before: existing.notes ?? undefined, after: value ?? undefined })
    }
  }
  return {
    columns,
    sourceName,
    nameChanged: sourceName !== existing.name,
    nextStatus,
    changes
  }
}

function executeExternalIntegrationSourceUpdate(
  database: ReturnType<typeof getBusinessDatabase>,
  id: string,
  expectedUpdatedAt: string,
  updatedAt: string,
  columns: ExternalIntegrationSourceUpdateColumn[]
): void {
  const assignments = columns.map(({ column }) => `${column} = ?`)
  const result = database.prepare(`
    UPDATE external_integration_sources
    SET ${assignments.join(', ')}, updated_at = ?
    WHERE id = ? AND updated_at = ?
  `).run(...columns.map(({ value }) => value), updatedAt, id, expectedUpdatedAt)
  if (Number(result.changes ?? 0) !== 1) throw new ExternalIntegrationSourcePatchConflictError()
}

async function executeExternalIntegrationSourceUpdateAsync(
  client: DatabaseClient,
  id: string,
  expectedUpdatedAt: string,
  updatedAt: string,
  columns: ExternalIntegrationSourceUpdateColumn[]
): Promise<void> {
  const assignments = columns.map(({ column }) => `${column} = ?`)
  const result = await client.execute(
    `UPDATE ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')} SET ${assignments.join(', ')}, updated_at = ? WHERE id = ? AND updated_at = ?`,
    [...columns.map(({ value }) => value), updatedAt, id, expectedUpdatedAt]
  )
  if (result.changes !== 1) throw new ExternalIntegrationSourcePatchConflictError()
}

export function deleteExternalIntegrationSource(id: string): ExternalIntegrationSourceDeleteReceipt | undefined {
  if (isBuiltInExternalIntegrationTestSourceId(id)) {
    throw new Error('内置测试 Token 不支持删除')
  }
  return runInDatabaseTransaction(() => {
    const source = findSourceDeleteRow(id)
    if (!source) return undefined
    const database = getBusinessDatabase()
    database.prepare('DELETE FROM external_integration_source_tokens WHERE source_ref_id = ?').run(id)
    const result = database.prepare('DELETE FROM external_integration_sources WHERE id = ?').run(id)
    return Number(result.changes ?? 0) > 0 ? source : undefined
  })
}

export async function deleteExternalIntegrationSourceAsync(id: string): Promise<ExternalIntegrationSourceDeleteReceipt | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return deleteExternalIntegrationSource(id)
  }
  if (isBuiltInExternalIntegrationTestSourceId(id)) {
    throw new Error('内置测试 Token 不支持删除')
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return client.transaction(async (tx) => {
    const source = await findSourceDeleteRowAsync(tx, id)
    if (!source) return undefined
    await tx.execute(`
      DELETE FROM ${externalIntegrationSourceBusinessTable(tx, 'external_integration_source_tokens')}
      WHERE source_ref_id = ?
    `, [id])
    const result = await tx.execute(`
      DELETE FROM ${externalIntegrationSourceBusinessTable(tx, 'external_integration_sources')}
      WHERE id = ?
    `, [id])
    return Number(result.changes ?? 0) > 0 ? source : undefined
  })
}

export function findExternalIntegrationSourceRecord(id: string): ExternalIntegrationSourceRecord | undefined {
  const row = findSourceRow(id)
  return row ? mapSourceRecord(row) : undefined
}

export async function findExternalIntegrationSourceRecordAsync(id: string): Promise<ExternalIntegrationSourceRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findExternalIntegrationSourceRecord(id)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await findSourceRowAsync(client, id)
  return row ? mapSourceRecord(row) : undefined
}

function findSourceRow(id: string): ExternalIntegrationSourceRow | undefined {
  return getBusinessDatabase()
    .prepare('SELECT * FROM external_integration_sources WHERE id = ?')
    .get(id) as ExternalIntegrationSourceRow | undefined
}

async function findSourceRowAsync(client: DatabaseClient, id: string): Promise<ExternalIntegrationSourceRow | undefined> {
  return await client.one<ExternalIntegrationSourceRow>(`
    SELECT *
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')}
    WHERE id = ?
  `, [id])
}

function findSourceDeleteRow(id: string): ExternalIntegrationSourceDeleteReceipt | undefined {
  return getBusinessDatabase()
    .prepare('SELECT id, name FROM external_integration_sources WHERE id = ?')
    .get(id) as ExternalIntegrationSourceDeleteReceipt | undefined
}

async function findSourceDeleteRowAsync(
  client: DatabaseClient,
  id: string
): Promise<ExternalIntegrationSourceDeleteReceipt | undefined> {
  return await client.one<ExternalIntegrationSourceDeleteReceipt>(`
    SELECT id, name
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')}
    WHERE id = ?
    FOR UPDATE
  `, [id])
}

function buildExternalIntegrationSourceCreateRow(input: ExternalIntegrationSourceInput): ExternalIntegrationSourceRow {
  assertKnownInputKeys(input, externalIntegrationSourceInputKeys, '来源系统')
  const now = nowIso()
  return {
    id: newId('extsrc'),
    name: normalizeNameOrThrow(input.name, '来源系统名称不能为空'),
    status: normalizeSourceStatusInput(input.status),
    scopes_json: encodeScopes(input.scopes),
    rate_limits_json: encodeRateLimits(input.rateLimits),
    expires_at: normalizeNullableIso(input.expiresAt),
    notes: normalizeNullableText(input.notes),
    last_used_at: null,
    created_at: now,
    updated_at: now
  }
}

function assertBuiltInSourceUpdateInput(input: ExternalIntegrationSourceUpdateInput): void {
  const keys = Object.keys(input)
  const disallowed = keys.filter((key) => key !== 'status' && key !== 'expectedUpdatedAt')
  if (disallowed.length) {
    throw new Error('内置测试 Token 只支持启用或停用，不支持编辑名称、授权范围、限频、到期时间或备注')
  }
}

function ensureSourceNameAvailable(name: string, currentId?: string): void {
  const existing = getBusinessDatabase()
    .prepare('SELECT id FROM external_integration_sources WHERE lower(name) = lower(?) LIMIT 1')
    .get(name) as Pick<ExternalIntegrationSourceRow, 'id'> | undefined
  if (existing && existing.id !== currentId) {
    throw new Error('来源系统名称已存在')
  }
}

async function ensureSourceNameAvailableAsync(client: DatabaseClient, name: string, currentId?: string): Promise<void> {
  const existing = await client.one<Pick<ExternalIntegrationSourceRow, 'id'>>(`
    SELECT id
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')}
    WHERE lower(name) = lower(?)
    LIMIT 1
  `, [name])
  if (existing && existing.id !== currentId) {
    throw new Error('来源系统名称已存在')
  }
}

function escapeExternalIntegrationLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

function externalIntegrationSourceBusinessTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
}
