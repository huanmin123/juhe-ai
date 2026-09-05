import { decryptJson, encryptJson } from './crypto.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import {
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestTokenId,
  createExternalIntegrationSourceTokenValue,
  hashExternalIntegrationSourceTokenValue,
  isBuiltInExternalIntegrationTestSourceId,
  isBuiltInExternalIntegrationTestTokenId
} from './external-integration-source-constants.js'
import { mapTokenSummary } from './external-integration-source-mappers.js'
import {
  decodeScopes,
  encodeScopes,
  normalizeNullableIso,
  normalizeScopes,
  normalizeTokenStatus,
  normalizeTokenStatusInput
} from './external-integration-source-normalizers.js'
import type {
  CreatedExternalIntegrationSourceToken,
  ExternalIntegrationSourceRow,
  ExternalIntegrationSourceTokenInput,
  ExternalIntegrationSourceTokenListRow,
  ExternalIntegrationSourcePrimaryTokenRow,
  ExternalIntegrationSourcePrimaryTokenSummary,
  ExternalIntegrationSourceTokenSecret,
  ExternalIntegrationSourceTokenStats,
  ExternalIntegrationSourceTokenPatchChange,
  ExternalIntegrationSourceTokenPatchOutcome,
  ExternalIntegrationSourceTokenSummary,
  ExternalIntegrationSourceTokenUpdateInput
} from './external-integration-source-types.js'
import {
  assertKnownInputKeys,
  ExternalIntegrationSourcePatchConflictError,
  isUniqueConstraintError,
  nextExternalIntegrationUpdatedAt,
  normalizeNameOrThrow
} from './external-integration-source-write-helpers.js'
import { getPostgresPool } from './postgres-client.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

const externalIntegrationSourceTokenInputKeys = new Set(['sourceRefId', 'name', 'token', 'status', 'scopes', 'expiresAt'])
const externalIntegrationSourceTokenUpdateInputKeys = new Set(['expectedUpdatedAt', 'name', 'status', 'scopes', 'expiresAt'])

export function createExternalIntegrationSourceToken(input: ExternalIntegrationSourceTokenInput): CreatedExternalIntegrationSourceToken {
  assertKnownInputKeys(input, externalIntegrationSourceTokenInputKeys, '来源系统 token')
  const name = normalizeNameOrThrow(input.name, '来源系统 token 名称不能为空', '来源系统 token 名称不能超过 80 个字符')
  const source = resolveSourceForToken(input)
  if (isBuiltInExternalIntegrationTestSourceId(source.id)) {
    throw new Error('内置测试 Token 不支持新增 Token')
  }
  const token = normalizeTokenValue(input.token)
  const scopes = normalizeScopes(input.scopes)
  const now = nowIso()
  const id = newId('exttok')
  const tokenPrefix = token.slice(0, 8)
  const tokenSuffix = token.slice(-8)
  try {
    getBusinessDatabase().prepare(`
      INSERT INTO external_integration_source_tokens (
        id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      id,
      source.id,
      name,
      hashExternalIntegrationSourceTokenValue(token),
      encryptJson({ token }),
      tokenPrefix,
      tokenSuffix,
      normalizeTokenStatusInput(input.status),
      JSON.stringify(scopes),
      normalizeNullableIso(input.expiresAt),
      now,
      now
    )
  } catch (error) {
    if (isUniqueConstraintError(error)) {
      throw new Error('来源系统 token 已存在，请重新生成')
    }
    throw error
  }

  return {
    id,
    name,
    token,
    tokenPrefix,
    tokenSuffix,
    scopes,
    expiresAt: normalizeNullableIso(input.expiresAt) ?? undefined
  }
}

export async function createExternalIntegrationSourceTokenAsync(input: ExternalIntegrationSourceTokenInput): Promise<CreatedExternalIntegrationSourceToken> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createExternalIntegrationSourceToken(input)
  }
  return createExternalIntegrationSourceTokenInClientAsync(createPostgresDatabaseClient(await getPostgresPool()), input)
}

export async function createExternalIntegrationSourceTokenInClientAsync(client: DatabaseClient, input: ExternalIntegrationSourceTokenInput): Promise<CreatedExternalIntegrationSourceToken> {
  assertKnownInputKeys(input, externalIntegrationSourceTokenInputKeys, '来源系统 token')
  const name = normalizeNameOrThrow(input.name, '来源系统 token 名称不能为空', '来源系统 token 名称不能超过 80 个字符')
  const source = await resolveSourceForTokenAsync(client, input)
  if (isBuiltInExternalIntegrationTestSourceId(source.id)) {
    throw new Error('内置测试 Token 不支持新增 Token')
  }
  const token = normalizeTokenValue(input.token)
  const scopes = normalizeScopes(input.scopes)
  const now = nowIso()
  const id = newId('exttok')
  const tokenPrefix = token.slice(0, 8)
  const tokenSuffix = token.slice(-8)
  try {
    await client.execute(`
      INSERT INTO ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')} (
        id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [
      id,
      source.id,
      name,
      hashExternalIntegrationSourceTokenValue(token),
      encryptJson({ token }),
      tokenPrefix,
      tokenSuffix,
      normalizeTokenStatusInput(input.status),
      JSON.stringify(scopes),
      normalizeNullableIso(input.expiresAt),
      now,
      now
    ])
  } catch (error) {
    if (isUniqueConstraintError(error)) {
      throw new Error('来源系统 token 已存在，请重新生成')
    }
    throw error
  }

  return {
    id,
    name,
    token,
    tokenPrefix,
    tokenSuffix,
    scopes,
    expiresAt: normalizeNullableIso(input.expiresAt) ?? undefined
  }
}

export function updateExternalIntegrationSourceToken(
  sourceRefId: string,
  tokenId: string,
  input: ExternalIntegrationSourceTokenUpdateInput
): ExternalIntegrationSourceTokenPatchOutcome | undefined {
  assertExternalIntegrationSourceTokenPatchInput(sourceRefId, tokenId, input)
  const existing = findExternalIntegrationSourceTokenPatchRow(sourceRefId, tokenId, input)
  if (!existing) return undefined
  if (existing.updated_at !== input.expectedUpdatedAt) throw new ExternalIntegrationSourcePatchConflictError()
  const updatedAt = nextExternalIntegrationUpdatedAt(existing.updated_at)
  const patch = buildExternalIntegrationSourceTokenPatch(input, existing, updatedAt)
  if (!patch.columns.length) return tokenPatchOutcome(existing, patch.changes, existing.updated_at)
  const assignments = patch.columns.map(({ column }) => `${column} = ?`)
  const result = getBusinessDatabase().prepare(`
    UPDATE external_integration_source_tokens
    SET ${assignments.join(', ')}, updated_at = ?
    WHERE id = ? AND source_ref_id = ? AND updated_at = ?
  `).run(...patch.columns.map(({ value }) => value), updatedAt, tokenId, sourceRefId, input.expectedUpdatedAt)
  if (Number(result.changes ?? 0) !== 1) throw new ExternalIntegrationSourcePatchConflictError()
  return tokenPatchOutcome(existing, patch.changes, updatedAt)
}

export async function updateExternalIntegrationSourceTokenAsync(
  sourceRefId: string,
  tokenId: string,
  input: ExternalIntegrationSourceTokenUpdateInput
): Promise<ExternalIntegrationSourceTokenPatchOutcome | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateExternalIntegrationSourceToken(sourceRefId, tokenId, input)
  }
  assertExternalIntegrationSourceTokenPatchInput(sourceRefId, tokenId, input)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return client.transaction(async (tx) => {
    const existing = await findExternalIntegrationSourceTokenPatchRowAsync(tx, sourceRefId, tokenId, input)
    if (!existing) return undefined
    if (existing.updated_at !== input.expectedUpdatedAt) throw new ExternalIntegrationSourcePatchConflictError()
    const updatedAt = nextExternalIntegrationUpdatedAt(existing.updated_at)
    const patch = buildExternalIntegrationSourceTokenPatch(input, existing, updatedAt)
    if (!patch.columns.length) return tokenPatchOutcome(existing, patch.changes, existing.updated_at)
    const assignments = patch.columns.map(({ column }) => `${column} = ?`)
    const result = await tx.execute(`
      UPDATE ${externalIntegrationTokenBusinessTable(tx, 'external_integration_source_tokens')}
      SET ${assignments.join(', ')}, updated_at = ?
      WHERE id = ? AND source_ref_id = ? AND updated_at = ?
    `, [...patch.columns.map(({ value }) => value), updatedAt, tokenId, sourceRefId, input.expectedUpdatedAt])
    if (result.changes !== 1) throw new ExternalIntegrationSourcePatchConflictError()
    return tokenPatchOutcome(existing, patch.changes, updatedAt)
  })
}

type ExternalIntegrationSourceTokenPatchRow = Pick<
  ExternalIntegrationSourceTokenListRow,
  'id' | 'source_ref_id' | 'name' | 'updated_at'
> & Partial<Pick<ExternalIntegrationSourceTokenListRow, 'status' | 'scopes_json' | 'expires_at'>> & {
  source_name: string
}

interface ExternalIntegrationSourceTokenUpdateColumn {
  column: 'name' | 'status' | 'scopes_json' | 'expires_at' | 'revoked_at'
  value: string | null
}

function assertExternalIntegrationSourceTokenPatchInput(
  sourceRefId: string,
  tokenId: string,
  input: ExternalIntegrationSourceTokenUpdateInput
): void {
  assertKnownInputKeys(input, externalIntegrationSourceTokenUpdateInputKeys, '来源系统 token')
  if (isBuiltInExternalIntegrationTestSourceId(sourceRefId) || isBuiltInExternalIntegrationTestTokenId(tokenId)) {
    throw new Error('内置测试 Token 不支持编辑')
  }
  if (!input.expectedUpdatedAt?.trim()) throw new Error('来源系统 token 版本不能为空')
}

function externalIntegrationSourceTokenPatchProjection(input: ExternalIntegrationSourceTokenUpdateInput): string[] {
  const columns = ['tokens.id', 'tokens.source_ref_id', 'tokens.name', 'tokens.updated_at', 'sources.name AS source_name']
  if (input.status !== undefined) columns.push('tokens.status')
  if (input.scopes !== undefined) columns.push('tokens.scopes_json')
  if (input.expiresAt !== undefined) columns.push('tokens.expires_at')
  return columns
}

function findExternalIntegrationSourceTokenPatchRow(
  sourceRefId: string,
  tokenId: string,
  input: ExternalIntegrationSourceTokenUpdateInput
): ExternalIntegrationSourceTokenPatchRow | undefined {
  return getBusinessDatabase().prepare(`
    SELECT ${externalIntegrationSourceTokenPatchProjection(input).join(', ')}
    FROM external_integration_source_tokens AS tokens
    INNER JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
    WHERE tokens.id = ? AND tokens.source_ref_id = ?
  `).get(tokenId, sourceRefId) as ExternalIntegrationSourceTokenPatchRow | undefined
}

async function findExternalIntegrationSourceTokenPatchRowAsync(
  client: DatabaseClient,
  sourceRefId: string,
  tokenId: string,
  input: ExternalIntegrationSourceTokenUpdateInput
): Promise<ExternalIntegrationSourceTokenPatchRow | undefined> {
  return client.one<ExternalIntegrationSourceTokenPatchRow>(`
    SELECT ${externalIntegrationSourceTokenPatchProjection(input).join(', ')}
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')} AS tokens
    INNER JOIN ${externalIntegrationTokenBusinessTable(client, 'external_integration_sources')} AS sources ON sources.id = tokens.source_ref_id
    WHERE tokens.id = ? AND tokens.source_ref_id = ?
    FOR UPDATE OF tokens
  `, [tokenId, sourceRefId])
}

function buildExternalIntegrationSourceTokenPatch(
  input: ExternalIntegrationSourceTokenUpdateInput,
  existing: ExternalIntegrationSourceTokenPatchRow,
  updatedAt: string
): { columns: ExternalIntegrationSourceTokenUpdateColumn[]; changes: ExternalIntegrationSourceTokenPatchChange[] } {
  const columns: ExternalIntegrationSourceTokenUpdateColumn[] = []
  const changes: ExternalIntegrationSourceTokenPatchChange[] = []
  if (input.name !== undefined) {
    const value = normalizeNameOrThrow(input.name, '来源系统 token 名称不能为空', '来源系统 token 名称不能超过 80 个字符')
    if (value !== existing.name) {
      columns.push({ column: 'name', value })
      changes.push({ field: 'name', before: existing.name, after: value })
    }
  }
  if (input.status !== undefined) {
    const before = normalizeTokenStatus(existing.status)
    const value = normalizeTokenStatusInput(input.status)
    if (value !== before) {
      const revokedAt = value === 'revoked' ? updatedAt : null
      columns.push({ column: 'status', value }, { column: 'revoked_at', value: revokedAt })
      changes.push({ field: 'status', before, after: value })
    }
  }
  if (input.scopes !== undefined) {
    const value = encodeScopes(input.scopes)
    if (value !== existing.scopes_json) {
      columns.push({ column: 'scopes_json', value })
      changes.push({ field: 'scopes', before: decodeScopes(existing.scopes_json ?? '[]'), after: decodeScopes(value) })
    }
  }
  if (input.expiresAt !== undefined) {
    const value = normalizeNullableIso(input.expiresAt)
    if (value !== existing.expires_at) {
      columns.push({ column: 'expires_at', value })
      changes.push({ field: 'expiresAt', before: existing.expires_at ?? undefined, after: value ?? undefined })
    }
  }
  return { columns, changes }
}

function tokenPatchOutcome(
  existing: ExternalIntegrationSourceTokenPatchRow,
  changes: ExternalIntegrationSourceTokenPatchChange[],
  updatedAt: string
): ExternalIntegrationSourceTokenPatchOutcome {
  const renamed = changes.find((change) => change.field === 'name')?.after
  return {
    mutation: { id: existing.id, updatedAt },
    sourceName: existing.source_name,
    tokenName: typeof renamed === 'string' ? renamed : existing.name,
    changes
  }
}

export function findExternalIntegrationSourceTokenSecret(sourceRefId: string, tokenId: string): ExternalIntegrationSourceTokenSecret | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT tokens.token_secret_encrypted
    FROM external_integration_source_tokens AS tokens
    JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
    WHERE sources.id = ? AND tokens.id = ?
  `).get(sourceRefId, tokenId) as Pick<ExternalIntegrationSourceTokenListRow, 'token_secret_encrypted'> | undefined
  if (!row) {
    return undefined
  }
  return { token: decryptExternalIntegrationSourceTokenSecret(row.token_secret_encrypted) }
}

export async function findExternalIntegrationSourceTokenSecretAsync(sourceRefId: string, tokenId: string): Promise<ExternalIntegrationSourceTokenSecret | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_external_integration_source_token_secret_read_only',
      sourceRefId,
      tokenId
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findExternalIntegrationSourceTokenSecret(sourceRefId, tokenId)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<Pick<ExternalIntegrationSourceTokenListRow, 'token_secret_encrypted'>>(`
    SELECT tokens.token_secret_encrypted
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')} AS tokens
    JOIN ${externalIntegrationTokenBusinessTable(client, 'external_integration_sources')} AS sources ON sources.id = tokens.source_ref_id
    WHERE sources.id = ? AND tokens.id = ?
  `, [sourceRefId, tokenId])
  if (!row) {
    return undefined
  }
  return { token: decryptExternalIntegrationSourceTokenSecret(row.token_secret_encrypted) }
}

export function resetBuiltInExternalIntegrationTestToken(): CreatedExternalIntegrationSourceToken {
  const source = getBusinessDatabase()
    .prepare('SELECT * FROM external_integration_sources WHERE id = ?')
    .get(builtInExternalIntegrationTestSourceId) as ExternalIntegrationSourceRow | undefined
  const tokenRow = getBusinessDatabase()
    .prepare('SELECT * FROM external_integration_source_tokens WHERE id = ? AND source_ref_id = ?')
    .get(builtInExternalIntegrationTestTokenId, builtInExternalIntegrationTestSourceId) as ExternalIntegrationSourceTokenListRow | undefined
  if (!source || !tokenRow) {
    throw new Error('内置测试 Token 不存在')
  }
  const token = createExternalIntegrationSourceTokenValue()
  const tokenPrefix = token.slice(0, 8)
  const tokenSuffix = token.slice(-8)
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE external_integration_source_tokens
    SET token_hash = ?,
        token_secret_encrypted = ?,
        token_prefix = ?,
        token_suffix = ?,
        status = 'active',
        revoked_at = NULL,
        updated_at = ?
    WHERE id = ? AND source_ref_id = ?
  `).run(
    hashExternalIntegrationSourceTokenValue(token),
    encryptJson({ token }),
    tokenPrefix,
    tokenSuffix,
    now,
    builtInExternalIntegrationTestTokenId,
    builtInExternalIntegrationTestSourceId
  )
  getBusinessDatabase()
    .prepare('UPDATE external_integration_sources SET updated_at = ? WHERE id = ?')
    .run(now, builtInExternalIntegrationTestSourceId)
  return {
    id: builtInExternalIntegrationTestTokenId,
    name: tokenRow.name,
    token,
    tokenPrefix,
    tokenSuffix,
    scopes: decodeScopes(source.scopes_json),
    expiresAt: undefined
  }
}

export async function resetBuiltInExternalIntegrationTestTokenAsync(): Promise<CreatedExternalIntegrationSourceToken> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return resetBuiltInExternalIntegrationTestToken()
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const source = await client.one<ExternalIntegrationSourceRow>(`
    SELECT *
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_sources')}
    WHERE id = ?
  `, [builtInExternalIntegrationTestSourceId])
  const tokenRow = await client.one<ExternalIntegrationSourceTokenListRow>(`
    SELECT *
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')}
    WHERE id = ? AND source_ref_id = ?
  `, [builtInExternalIntegrationTestTokenId, builtInExternalIntegrationTestSourceId])
  if (!source || !tokenRow) {
    throw new Error('内置测试 Token 不存在')
  }
  const token = createExternalIntegrationSourceTokenValue()
  const tokenPrefix = token.slice(0, 8)
  const tokenSuffix = token.slice(-8)
  const now = nowIso()
  await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${externalIntegrationTokenBusinessTable(tx, 'external_integration_source_tokens')}
      SET token_hash = ?,
          token_secret_encrypted = ?,
          token_prefix = ?,
          token_suffix = ?,
          status = 'active',
          revoked_at = NULL,
          updated_at = ?
      WHERE id = ? AND source_ref_id = ?
    `, [
      hashExternalIntegrationSourceTokenValue(token),
      encryptJson({ token }),
      tokenPrefix,
      tokenSuffix,
      now,
      builtInExternalIntegrationTestTokenId,
      builtInExternalIntegrationTestSourceId
    ])
    await tx.execute(`
      UPDATE ${externalIntegrationTokenBusinessTable(tx, 'external_integration_sources')}
      SET updated_at = ?
      WHERE id = ?
    `, [now, builtInExternalIntegrationTestSourceId])
  })
  return {
    id: builtInExternalIntegrationTestTokenId,
    name: tokenRow.name,
    token,
    tokenPrefix,
    tokenSuffix,
    scopes: decodeScopes(source.scopes_json),
    expiresAt: undefined
  }
}

type ExternalIntegrationSourceTokenUpdatedAtRow = Pick<ExternalIntegrationSourceTokenListRow, 'updated_at'>

export function latestExternalIntegrationSourceTokenUpdatedAt(sourceRefId: string): string | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT updated_at
    FROM external_integration_source_tokens
    WHERE source_ref_id = ?
    ORDER BY updated_at DESC
    LIMIT 1
  `).get(sourceRefId) as ExternalIntegrationSourceTokenUpdatedAtRow | undefined
  return row?.updated_at
}

export async function lockExternalIntegrationSourceTokenUpdatedAtAsync(
  client: DatabaseClient,
  sourceRefId: string
): Promise<string | undefined> {
  const rows = await client.query<ExternalIntegrationSourceTokenUpdatedAtRow>(`
    SELECT tokens.updated_at
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')} AS tokens
    WHERE tokens.source_ref_id = ?
    FOR UPDATE OF tokens
  `, [sourceRefId])
  return rows.reduce<string | undefined>((latest, row) => (
    !latest || row.updated_at > latest ? row.updated_at : latest
  ), undefined)
}

export function syncExternalIntegrationSourceTokenStatus(
  sourceRefId: string,
  sourceStatus: 'active' | 'disabled',
  updatedAt: string
): number {
  const result = getBusinessDatabase().prepare(`
    UPDATE external_integration_source_tokens
    SET status = ?, updated_at = ?
    WHERE source_ref_id = ? AND status <> 'revoked' AND status <> ?
  `).run(
    sourceStatus,
    updatedAt,
    sourceRefId,
    sourceStatus
  )
  return Number(result.changes ?? 0)
}

export async function syncExternalIntegrationSourceTokenStatusAsync(
  sourceRefId: string,
  sourceStatus: 'active' | 'disabled',
  updatedAt: string,
  clientInput?: DatabaseClient
): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres' && !clientInput) {
    return syncExternalIntegrationSourceTokenStatus(sourceRefId, sourceStatus, updatedAt)
  }
  const client = clientInput ?? createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')}
    SET status = ?, updated_at = ?
    WHERE source_ref_id = ? AND status <> 'revoked' AND status <> ?
  `, [
    sourceStatus,
    updatedAt,
    sourceRefId,
    sourceStatus
  ])
  return result.changes
}

export function loadExternalIntegrationSourceTokensBySourceIds(sourceIds: string[]): Map<string, ExternalIntegrationSourceTokenSummary[]> {
  const result = new Map<string, ExternalIntegrationSourceTokenSummary[]>()
  if (!sourceIds.length) {
    return result
  }
  const placeholders = sourceIds.map(() => '?').join(',')
  const rows = getBusinessDatabase().prepare(`
    SELECT *
    FROM external_integration_source_tokens
    WHERE source_ref_id IN (${placeholders})
    ORDER BY created_at DESC, id DESC
  `).all(...sourceIds) as unknown as ExternalIntegrationSourceTokenListRow[]
  for (const row of rows) {
    const token = mapTokenSummary(row)
    const tokens = result.get(row.source_ref_id)
    if (tokens) {
      tokens.push(token)
    } else {
      result.set(row.source_ref_id, [token])
    }
  }
  return result
}

export function loadExternalIntegrationSourcePrimaryTokensBySourceIds(sourceIds: string[]): Map<string, ExternalIntegrationSourcePrimaryTokenSummary> {
  const result = new Map<string, ExternalIntegrationSourcePrimaryTokenSummary>()
  const ids = [...new Set(sourceIds.filter(Boolean))]
  if (!ids.length) {
    return result
  }
  const placeholders = ids.map(() => '?').join(',')
  const rows = getBusinessDatabase().prepare(`
    SELECT
      id,
      source_ref_id,
      token_prefix,
      token_suffix
    FROM (
      SELECT
        tokens.id,
        tokens.source_ref_id,
        tokens.token_prefix,
        tokens.token_suffix,
        tokens.status,
        tokens.created_at,
        ROW_NUMBER() OVER (
          PARTITION BY tokens.source_ref_id
          ORDER BY CASE WHEN tokens.status = 'active' THEN 0 ELSE 1 END ASC, tokens.created_at DESC, tokens.id DESC
        ) AS token_rank
      FROM external_integration_source_tokens AS tokens
      WHERE tokens.source_ref_id IN (${placeholders})
    )
    WHERE token_rank = 1
  `).all(...ids) as unknown as ExternalIntegrationSourcePrimaryTokenRow[]
  for (const row of rows) {
    result.set(row.source_ref_id, mapPrimaryTokenSummary(row))
  }
  return result
}

export function loadExternalIntegrationSourceTokenStatsBySourceIds(sourceIds: string[]): Map<string, ExternalIntegrationSourceTokenStats> {
  const result = new Map<string, ExternalIntegrationSourceTokenStats>()
  const ids = [...new Set(sourceIds.filter(Boolean))]
  if (!ids.length) {
    return result
  }
  const placeholders = ids.map(() => '?').join(',')
  const rows = getBusinessDatabase().prepare(`
    SELECT
      source_ref_id,
      COUNT(*) AS token_count,
      SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END) AS active_token_count
    FROM external_integration_source_tokens
    WHERE source_ref_id IN (${placeholders})
    GROUP BY source_ref_id
  `).all(...ids) as unknown as Array<{
    source_ref_id: string
    token_count: number
    active_token_count: number
  }>
  for (const row of rows) {
    result.set(row.source_ref_id, {
      tokenCount: Number(row.token_count),
      activeTokenCount: Number(row.active_token_count)
    })
  }
  return result
}

export async function loadExternalIntegrationSourceTokensBySourceIdsAsync(sourceIds: string[], clientInput?: DatabaseClient): Promise<Map<string, ExternalIntegrationSourceTokenSummary[]>> {
  if (runtimeConfig.databaseDriver !== 'postgres' && !clientInput) {
    return loadExternalIntegrationSourceTokensBySourceIds(sourceIds)
  }
  const result = new Map<string, ExternalIntegrationSourceTokenSummary[]>()
  if (!sourceIds.length) {
    return result
  }
  const client = clientInput ?? createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<ExternalIntegrationSourceTokenListRow>(`
    SELECT *
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')}
    WHERE source_ref_id IN (${client.dialect.bindPlaceholders(sourceIds.length)})
    ORDER BY created_at DESC, id DESC
  `, sourceIds)
  for (const row of rows) {
    const token = mapTokenSummary(row)
    const tokens = result.get(row.source_ref_id)
    if (tokens) {
      tokens.push(token)
    } else {
      result.set(row.source_ref_id, [token])
    }
  }
  return result
}

export async function loadExternalIntegrationSourcePrimaryTokensBySourceIdsAsync(sourceIds: string[], clientInput?: DatabaseClient): Promise<Map<string, ExternalIntegrationSourcePrimaryTokenSummary>> {
  if (runtimeConfig.databaseDriver !== 'postgres' && !clientInput) {
    return loadExternalIntegrationSourcePrimaryTokensBySourceIds(sourceIds)
  }
  const result = new Map<string, ExternalIntegrationSourcePrimaryTokenSummary>()
  const ids = [...new Set(sourceIds.filter(Boolean))]
  if (!ids.length) {
    return result
  }
  const client = clientInput ?? createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<ExternalIntegrationSourcePrimaryTokenRow>(`
    SELECT
      id,
      source_ref_id,
      token_prefix,
      token_suffix
    FROM (
      SELECT
        tokens.id,
        tokens.source_ref_id,
        tokens.token_prefix,
        tokens.token_suffix,
        tokens.status,
        tokens.created_at,
        ROW_NUMBER() OVER (
          PARTITION BY tokens.source_ref_id
          ORDER BY CASE WHEN tokens.status = 'active' THEN 0 ELSE 1 END ASC, tokens.created_at DESC, tokens.id DESC
        ) AS token_rank
      FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')} AS tokens
      WHERE tokens.source_ref_id IN (${client.dialect.bindPlaceholders(ids.length)})
    ) ranked_tokens
    WHERE token_rank = 1
  `, ids)
  for (const row of rows) {
    result.set(row.source_ref_id, mapPrimaryTokenSummary(row))
  }
  return result
}

function mapPrimaryTokenSummary(row: ExternalIntegrationSourcePrimaryTokenRow): ExternalIntegrationSourcePrimaryTokenSummary {
  return {
    id: row.id,
    tokenPrefix: row.token_prefix,
    tokenSuffix: row.token_suffix
  }
}

export async function loadExternalIntegrationSourceTokenStatsBySourceIdsAsync(
  sourceIds: string[],
  clientInput?: DatabaseClient
): Promise<Map<string, ExternalIntegrationSourceTokenStats>> {
  if (runtimeConfig.databaseDriver !== 'postgres' && !clientInput) {
    return loadExternalIntegrationSourceTokenStatsBySourceIds(sourceIds)
  }
  const result = new Map<string, ExternalIntegrationSourceTokenStats>()
  const ids = [...new Set(sourceIds.filter(Boolean))]
  if (!ids.length) {
    return result
  }
  const client = clientInput ?? createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<{
    source_ref_id: string
    token_count: number | string
    active_token_count: number | string
  }>(`
    SELECT
      source_ref_id,
      COUNT(*) AS token_count,
      SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END) AS active_token_count
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')}
    WHERE source_ref_id IN (${client.dialect.bindPlaceholders(ids.length)})
    GROUP BY source_ref_id
  `, ids)
  for (const row of rows) {
    result.set(row.source_ref_id, {
      tokenCount: Number(row.token_count),
      activeTokenCount: Number(row.active_token_count)
    })
  }
  return result
}

function resolveSourceForToken(input: ExternalIntegrationSourceTokenInput): Pick<ExternalIntegrationSourceRow, 'id'> {
  if (input.sourceRefId !== undefined && typeof input.sourceRefId !== 'string') {
    throw new Error('来源系统不存在')
  }
  const sourceRefId = input.sourceRefId?.trim()
  if (sourceRefId) {
    const source = getBusinessDatabase()
      .prepare('SELECT id FROM external_integration_sources WHERE id = ?')
      .get(sourceRefId) as Pick<ExternalIntegrationSourceRow, 'id'> | undefined
    if (!source) {
      throw new Error('来源系统不存在')
    }
    return source
  }
  throw new Error('来源系统不存在')
}

async function resolveSourceForTokenAsync(client: DatabaseClient, input: ExternalIntegrationSourceTokenInput): Promise<Pick<ExternalIntegrationSourceRow, 'id'>> {
  if (input.sourceRefId !== undefined && typeof input.sourceRefId !== 'string') {
    throw new Error('来源系统不存在')
  }
  const sourceRefId = input.sourceRefId?.trim()
  if (sourceRefId) {
    const source = await client.one<Pick<ExternalIntegrationSourceRow, 'id'>>(`
      SELECT id
      FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_sources')}
      WHERE id = ?
    `, [sourceRefId])
    if (!source) {
      throw new Error('来源系统不存在')
    }
    return source
  }
  throw new Error('来源系统不存在')
}

function decryptExternalIntegrationSourceTokenSecret(value: string | null | undefined): string {
  if (!value) {
    throw new Error('来源系统 Token 密文缺少完整 Token')
  }
  const decrypted = decryptJson<{ token?: unknown }>(value)
  if (typeof decrypted.token !== 'string' || decrypted.token.length === 0) {
    throw new Error('来源系统 Token 密文缺少完整 Token')
  }
  return decrypted.token
}

function normalizeTokenValue(value: unknown): string {
  if (value === undefined) {
    return createExternalIntegrationSourceTokenValue()
  }
  if (typeof value !== 'string') {
    throw new Error('来源系统 token 必须是字符串')
  }
  const token = value.trim()
  if (!token) {
    throw new Error('来源系统 token 不能为空')
  }
  return token
}

function externalIntegrationTokenBusinessTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
}
