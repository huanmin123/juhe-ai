import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, newId, nowIso, runInDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { isBuiltInExternalIntegrationTestSourceId } from './external-integration-source-constants.js'
import { mapSourceListItem, mapSourceSummary } from './external-integration-source-mappers.js'
import {
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
  syncExternalIntegrationSourceTokenState,
  syncExternalIntegrationSourceTokenStateAsync
} from './external-integration-source-token.repository.js'
import type {
  CreatedExternalIntegrationSourceAuthorization,
  ExternalIntegrationSourceInput,
  ExternalIntegrationSourceListOptions,
  ExternalIntegrationSourceListResult,
  ExternalIntegrationSourceListRow,
  ExternalIntegrationSourceRow,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceUpdateInput
} from './external-integration-source-types.js'
import {
  assertKnownInputKeys,
  isUniqueConstraintError,
  normalizeNameOrThrow
} from './external-integration-source-write-helpers.js'
import { getPostgresPool } from './postgres-client.js'
import { normalizeListPage } from './query-utils.js'

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

export { validateExternalIntegrationSourceToken, validateExternalIntegrationSourceTokenAsync } from './external-integration-source-auth.repository.js'
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
  ExternalIntegrationSourceAuthResult,
  ExternalIntegrationSourceInput,
  ExternalIntegrationSourceListOptions,
  ExternalIntegrationSourceListResult,
  ExternalIntegrationSourceListRow,
  ExternalIntegrationSourceRow,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenInput,
  ExternalIntegrationSourceTokenListRow,
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
    where.push('(sources.name = ? OR sources.name LIKE ?)')
    params.push(keyword, `${keyword}%`)
  }
  const whereSql = where.length ? `WHERE ${where.join(' AND ')}` : ''
  const rows = getBusinessDatabase().prepare(`
    SELECT
      sources.*,
      (
        SELECT COUNT(*)
        FROM external_integration_source_tokens AS tokens
        WHERE tokens.source_ref_id = sources.id
      ) AS token_count,
      (
        SELECT COUNT(*)
        FROM external_integration_source_tokens AS tokens
        WHERE tokens.source_ref_id = sources.id
          AND tokens.status = 'active'
      ) AS active_token_count
    FROM external_integration_sources AS sources
    ${whereSql}
    ORDER BY sources.updated_at DESC, sources.id DESC
    LIMIT ? OFFSET ?
  `).all(...params, pageSize + 1, offset) as unknown as ExternalIntegrationSourceListRow[]

  const pageRows = rows.slice(0, pageSize)
  const primaryTokensBySourceId = loadExternalIntegrationSourcePrimaryTokensBySourceIds(pageRows.map((row) => row.id))
  return {
    items: pageRows.map((row) => mapSourceListItem(row, primaryTokensBySourceId.get(row.id))),
    page,
    pageSize,
    pageUpperBound: offset + pageRows.length + (rows.length > pageSize ? 1 : 0),
    hasMore: rows.length > pageSize
  }
}

export async function listExternalIntegrationSourcesAsync(options: ExternalIntegrationSourceListOptions = {}): Promise<ExternalIntegrationSourceListResult> {
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
  const rows = await client.query<ExternalIntegrationSourceListRow>(`
    SELECT
      sources.*,
      (
        SELECT COUNT(*)
        FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_source_tokens')} AS tokens
        WHERE tokens.source_ref_id = sources.id
      ) AS token_count,
      (
        SELECT COUNT(*)
        FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_source_tokens')} AS tokens
        WHERE tokens.source_ref_id = sources.id
          AND tokens.status = 'active'
      ) AS active_token_count
    FROM ${externalIntegrationSourceBusinessTable(client, 'external_integration_sources')} AS sources
    ${whereSql}
    ORDER BY sources.updated_at DESC, sources.id DESC
    LIMIT ? OFFSET ?
  `, [...params, pageSize + 1, offset])

  const pageRows = rows.slice(0, pageSize)
  const primaryTokensBySourceId = await loadExternalIntegrationSourcePrimaryTokensBySourceIdsAsync(pageRows.map((row) => row.id), client)
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

export function createExternalIntegrationSource(input: ExternalIntegrationSourceInput): ExternalIntegrationSourceSummary {
  assertKnownInputKeys(input, externalIntegrationSourceInputKeys, '来源系统')
  const name = normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  const now = nowIso()
  const id = newId('extsrc')
  ensureSourceNameAvailable(name)
  try {
    getBusinessDatabase().prepare(`
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
  } catch (error) {
    if (isUniqueConstraintError(error)) {
      throw new Error('来源系统名称已存在')
    }
    throw error
  }
  return requiredSource(id)
}

export async function createExternalIntegrationSourceAsync(input: ExternalIntegrationSourceInput): Promise<ExternalIntegrationSourceSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createExternalIntegrationSource(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const source = await createExternalIntegrationSourceInClientAsync(client, input)
  return requiredSourceAsync(client, source.id)
}

async function createExternalIntegrationSourceInClientAsync(client: DatabaseClient, input: ExternalIntegrationSourceInput): Promise<{ id: string }> {
  assertKnownInputKeys(input, externalIntegrationSourceInputKeys, '来源系统')
  const name = normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  const now = nowIso()
  const id = newId('extsrc')
  await ensureSourceNameAvailableAsync(client, name)
  try {
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
  } catch (error) {
    if (isUniqueConstraintError(error)) {
      throw new Error('来源系统名称已存在')
    }
    throw error
  }
  return { id }
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
      source: requiredSource(source.id),
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
    const createdSource = await createExternalIntegrationSourceInClientAsync(tx, input)
    const source = await requiredSourceAsync(tx, createdSource.id)
    const token = await createExternalIntegrationSourceTokenInClientAsync(tx, {
      sourceRefId: source.id,
      name: `${source.name} 生产 Token`,
      status: source.status,
      scopes: source.scopes,
      expiresAt: source.expiresAt ?? null
    })
    return {
      source: await requiredSourceAsync(tx, source.id),
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

export function updateExternalIntegrationSource(id: string, input: ExternalIntegrationSourceUpdateInput): ExternalIntegrationSourceSummary | undefined {
  assertKnownInputKeys(input, externalIntegrationSourceInputKeys, '来源系统')
  const existing = findSourceRow(id)
  if (!existing) {
    return undefined
  }
  if (isBuiltInExternalIntegrationTestSourceId(id)) {
    assertBuiltInSourceUpdateInput(input)
  }
  const nextName = input.name === undefined ? existing.name : normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  if (nextName !== existing.name) {
    ensureSourceNameAvailable(nextName, id)
  }
  const nextStatus = input.status === undefined ? normalizeSourceStatus(existing.status) : normalizeSourceStatusInput(input.status)
  const nextScopes = input.scopes === undefined ? existing.scopes_json : encodeScopes(input.scopes)
  const nextRateLimits = input.rateLimits === undefined ? existing.rate_limits_json : encodeRateLimits(input.rateLimits)
  const nextExpiresAt = input.expiresAt === undefined ? existing.expires_at : normalizeNullableIso(input.expiresAt)
  const nextNotes = input.notes === undefined ? existing.notes : normalizeNullableText(input.notes)
  getBusinessDatabase().prepare(`
    UPDATE external_integration_sources
    SET name = ?, status = ?, scopes_json = ?, rate_limits_json = ?, expires_at = ?, notes = ?, updated_at = ?
    WHERE id = ?
  `).run(nextName, nextStatus, nextScopes, nextRateLimits, nextExpiresAt, nextNotes, nowIso(), id)
  if (!isBuiltInExternalIntegrationTestSourceId(id)) {
    syncExternalIntegrationSourceTokenState(id)
  }
  return requiredSource(id)
}

export async function updateExternalIntegrationSourceAsync(id: string, input: ExternalIntegrationSourceUpdateInput): Promise<ExternalIntegrationSourceSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateExternalIntegrationSource(id, input)
  }
  assertKnownInputKeys(input, externalIntegrationSourceInputKeys, '来源系统')
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return client.transaction(async (tx) => {
    const existing = await findSourceRowAsync(tx, id)
    if (!existing) {
      return undefined
    }
    if (isBuiltInExternalIntegrationTestSourceId(id)) {
      assertBuiltInSourceUpdateInput(input)
    }
    const nextName = input.name === undefined ? existing.name : normalizeNameOrThrow(input.name, '来源系统名称不能为空')
    if (nextName !== existing.name) {
      await ensureSourceNameAvailableAsync(tx, nextName, id)
    }
    const nextStatus = input.status === undefined ? normalizeSourceStatus(existing.status) : normalizeSourceStatusInput(input.status)
    const nextScopes = input.scopes === undefined ? existing.scopes_json : encodeScopes(input.scopes)
    const nextRateLimits = input.rateLimits === undefined ? existing.rate_limits_json : encodeRateLimits(input.rateLimits)
    const nextExpiresAt = input.expiresAt === undefined ? existing.expires_at : normalizeNullableIso(input.expiresAt)
    const nextNotes = input.notes === undefined ? existing.notes : normalizeNullableText(input.notes)
    await tx.execute(`
      UPDATE ${externalIntegrationSourceBusinessTable(tx, 'external_integration_sources')}
      SET name = ?, status = ?, scopes_json = ?, rate_limits_json = ?, expires_at = ?, notes = ?, updated_at = ?
      WHERE id = ?
    `, [nextName, nextStatus, nextScopes, nextRateLimits, nextExpiresAt, nextNotes, nowIso(), id])
    if (!isBuiltInExternalIntegrationTestSourceId(id)) {
      await syncExternalIntegrationSourceTokenStateAsync(id, tx)
    }
    return requiredSourceAsync(tx, id)
  })
}

export function deleteExternalIntegrationSource(id: string): boolean {
  if (isBuiltInExternalIntegrationTestSourceId(id)) {
    throw new Error('内置测试 Token 不支持删除')
  }
  return runInDatabaseTransaction(() => {
    if (!findSourceRow(id)) {
      return false
    }
    const database = getBusinessDatabase()
    database.prepare('DELETE FROM external_integration_source_tokens WHERE source_ref_id = ?').run(id)
    const result = database.prepare('DELETE FROM external_integration_sources WHERE id = ?').run(id)
    return Number(result.changes ?? 0) > 0
  })
}

export async function deleteExternalIntegrationSourceAsync(id: string): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return deleteExternalIntegrationSource(id)
  }
  if (isBuiltInExternalIntegrationTestSourceId(id)) {
    throw new Error('内置测试 Token 不支持删除')
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return client.transaction(async (tx) => {
    if (!await findSourceRowAsync(tx, id)) {
      return false
    }
    await tx.execute(`
      DELETE FROM ${externalIntegrationSourceBusinessTable(tx, 'external_integration_source_tokens')}
      WHERE source_ref_id = ?
    `, [id])
    const result = await tx.execute(`
      DELETE FROM ${externalIntegrationSourceBusinessTable(tx, 'external_integration_sources')}
      WHERE id = ?
    `, [id])
    return Number(result.changes ?? 0) > 0
  })
}

function requiredSource(id: string): ExternalIntegrationSourceSummary {
  const source = findExternalIntegrationSource(id)
  if (!source) {
    throw new Error('来源系统不存在')
  }
  return source
}

async function requiredSourceAsync(client: DatabaseClient, id: string): Promise<ExternalIntegrationSourceSummary> {
  const source = await findExternalIntegrationSourceInClientAsync(client, id)
  if (!source) {
    throw new Error('来源系统不存在')
  }
  return source
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

function assertBuiltInSourceUpdateInput(input: ExternalIntegrationSourceUpdateInput): void {
  const keys = Object.keys(input)
  const disallowed = keys.filter((key) => key !== 'status')
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
