import { decryptJson, encryptJson } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
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
  ExternalIntegrationSourceTokenSecret,
  ExternalIntegrationSourceTokenSummary,
  ExternalIntegrationSourceTokenUpdateInput
} from './external-integration-source-types.js'
import {
  assertKnownInputKeys,
  isUniqueConstraintError,
  normalizeNameOrThrow
} from './external-integration-source-write-helpers.js'

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

function findExternalIntegrationSourceTokenSummary(sourceRefId: string, tokenId: string): ExternalIntegrationSourceTokenSummary | undefined {
  const row = getBusinessDatabase()
    .prepare('SELECT * FROM external_integration_source_tokens WHERE source_ref_id = ? AND id = ?')
    .get(sourceRefId, tokenId) as ExternalIntegrationSourceTokenListRow | undefined
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
