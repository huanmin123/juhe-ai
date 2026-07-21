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
  normalizeSourceStatus,
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
  ExternalIntegrationSourceTokenSummary,
  ExternalIntegrationSourceTokenUpdateInput
} from './external-integration-source-types.js'
import {
  assertKnownInputKeys,
  isUniqueConstraintError,
  normalizeNameOrThrow
} from './external-integration-source-write-helpers.js'
import { getPostgresPool } from './postgres-client.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

const externalIntegrationSourceTokenInputKeys = new Set(['sourceRefId', 'name', 'token', 'status', 'scopes', 'expiresAt'])
const externalIntegrationSourceTokenUpdateInputKeys = new Set(['name', 'status', 'scopes', 'expiresAt'])

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

export function updateExternalIntegrationSourceToken(sourceRefId: string, tokenId: string, input: ExternalIntegrationSourceTokenUpdateInput): ExternalIntegrationSourceTokenSummary | undefined {
  assertKnownInputKeys(input, externalIntegrationSourceTokenUpdateInputKeys, '来源系统 token')
  if (isBuiltInExternalIntegrationTestSourceId(sourceRefId) || isBuiltInExternalIntegrationTestTokenId(tokenId)) {
    throw new Error('内置测试 Token 不支持编辑')
  }
  const existing = getBusinessDatabase().prepare(`
    SELECT tokens.*
    FROM external_integration_source_tokens AS tokens
    JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
    WHERE sources.id = ? AND tokens.id = ?
  `).get(sourceRefId, tokenId) as ExternalIntegrationSourceTokenListRow | undefined
  if (!existing) {
    return undefined
  }
  const nextStatus = input.status === undefined ? normalizeTokenStatus(existing.status) : normalizeTokenStatusInput(input.status)
  const revokedAt = nextStatus === 'revoked' && existing.status !== 'revoked'
    ? nowIso()
    : nextStatus === 'revoked'
      ? existing.revoked_at
      : null
  getBusinessDatabase().prepare(`
    UPDATE external_integration_source_tokens
    SET name = ?, status = ?, scopes_json = ?, expires_at = ?, revoked_at = ?, updated_at = ?
    WHERE id = ?
  `).run(
    input.name === undefined ? existing.name : normalizeNameOrThrow(input.name, '来源系统 token 名称不能为空', '来源系统 token 名称不能超过 80 个字符'),
    nextStatus,
    input.scopes === undefined ? existing.scopes_json : encodeScopes(input.scopes),
    input.expiresAt === undefined ? existing.expires_at : normalizeNullableIso(input.expiresAt),
    revokedAt,
    nowIso(),
    tokenId
  )
  return findExternalIntegrationSourceTokenSummary(sourceRefId, tokenId)
}

export async function updateExternalIntegrationSourceTokenAsync(sourceRefId: string, tokenId: string, input: ExternalIntegrationSourceTokenUpdateInput): Promise<ExternalIntegrationSourceTokenSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateExternalIntegrationSourceToken(sourceRefId, tokenId, input)
  }
  assertKnownInputKeys(input, externalIntegrationSourceTokenUpdateInputKeys, '来源系统 token')
  if (isBuiltInExternalIntegrationTestSourceId(sourceRefId) || isBuiltInExternalIntegrationTestTokenId(tokenId)) {
    throw new Error('内置测试 Token 不支持编辑')
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const existing = await client.one<ExternalIntegrationSourceTokenListRow>(`
    SELECT tokens.*
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')} AS tokens
    JOIN ${externalIntegrationTokenBusinessTable(client, 'external_integration_sources')} AS sources ON sources.id = tokens.source_ref_id
    WHERE sources.id = ? AND tokens.id = ?
  `, [sourceRefId, tokenId])
  if (!existing) {
    return undefined
  }
  const nextStatus = input.status === undefined ? normalizeTokenStatus(existing.status) : normalizeTokenStatusInput(input.status)
  const revokedAt = nextStatus === 'revoked' && existing.status !== 'revoked'
    ? nowIso()
    : nextStatus === 'revoked'
      ? existing.revoked_at
      : null
  await client.execute(`
    UPDATE ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')}
    SET name = ?, status = ?, scopes_json = ?, expires_at = ?, revoked_at = ?, updated_at = ?
    WHERE id = ?
  `, [
    input.name === undefined ? existing.name : normalizeNameOrThrow(input.name, '来源系统 token 名称不能为空', '来源系统 token 名称不能超过 80 个字符'),
    nextStatus,
    input.scopes === undefined ? existing.scopes_json : encodeScopes(input.scopes),
    input.expiresAt === undefined ? existing.expires_at : normalizeNullableIso(input.expiresAt),
    revokedAt,
    nowIso(),
    tokenId
  ])
  return findExternalIntegrationSourceTokenSummaryAsync(client, sourceRefId, tokenId)
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

export function syncExternalIntegrationSourceTokenState(sourceRefId: string): void {
  const source = getBusinessDatabase()
    .prepare('SELECT * FROM external_integration_sources WHERE id = ?')
    .get(sourceRefId) as ExternalIntegrationSourceRow | undefined
  if (!source) {
    return
  }
  const sourceStatus = normalizeSourceStatus(source.status)
  getBusinessDatabase().prepare(`
    UPDATE external_integration_source_tokens
    SET name = ?,
        status = CASE WHEN status = 'revoked' THEN status ELSE ? END,
        scopes_json = ?,
        expires_at = ?,
        updated_at = ?
    WHERE source_ref_id = ?
  `).run(
    `${source.name} 生产 Token`,
    sourceStatus,
    source.scopes_json,
    source.expires_at,
    nowIso(),
    sourceRefId
  )
}

export async function syncExternalIntegrationSourceTokenStateAsync(sourceRefId: string, clientInput?: DatabaseClient): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres' && !clientInput) {
    syncExternalIntegrationSourceTokenState(sourceRefId)
    return
  }
  const client = clientInput ?? createPostgresDatabaseClient(await getPostgresPool())
  const source = await client.one<ExternalIntegrationSourceRow>(`
    SELECT *
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_sources')}
    WHERE id = ?
  `, [sourceRefId])
  if (!source) {
    return
  }
  const sourceStatus = normalizeSourceStatus(source.status)
  await client.execute(`
    UPDATE ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')}
    SET name = ?,
        status = CASE WHEN status = 'revoked' THEN status ELSE ? END,
        scopes_json = ?,
        expires_at = ?,
        updated_at = ?
    WHERE source_ref_id = ?
  `, [
    `${source.name} 生产 Token`,
    sourceStatus,
    source.scopes_json,
    source.expires_at,
    nowIso(),
    sourceRefId
  ])
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

function findExternalIntegrationSourceTokenSummary(sourceRefId: string, tokenId: string): ExternalIntegrationSourceTokenSummary | undefined {
  const row = getBusinessDatabase()
    .prepare('SELECT * FROM external_integration_source_tokens WHERE source_ref_id = ? AND id = ?')
    .get(sourceRefId, tokenId) as ExternalIntegrationSourceTokenListRow | undefined
  return row ? mapTokenSummary(row) : undefined
}

async function findExternalIntegrationSourceTokenSummaryAsync(client: DatabaseClient, sourceRefId: string, tokenId: string): Promise<ExternalIntegrationSourceTokenSummary | undefined> {
  const row = await client.one<ExternalIntegrationSourceTokenListRow>(`
    SELECT *
    FROM ${externalIntegrationTokenBusinessTable(client, 'external_integration_source_tokens')}
    WHERE source_ref_id = ? AND id = ?
  `, [sourceRefId, tokenId])
  return row ? mapTokenSummary(row) : undefined
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
