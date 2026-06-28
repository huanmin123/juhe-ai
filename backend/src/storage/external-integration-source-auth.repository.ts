import { getBusinessDatabase, nowIso } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import {
  hashExternalIntegrationSourceTokenValue,
  isBuiltInExternalIntegrationTestSourceId,
  isBuiltInExternalIntegrationTestTokenId
} from './external-integration-source-constants.js'
import {
  decodeRateLimits,
  decodeScopes,
  normalizeSourceStatus,
  normalizeTokenStatus
} from './external-integration-source-normalizers.js'
import type {
  ExternalIntegrationSourceAuthContext,
  ExternalIntegrationSourceAuthResult,
  ExternalIntegrationSourceTokenRow
} from './external-integration-source-types.js'
import { getPostgresPool } from './postgres-client.js'

const touchLastUsedIntervalMs = 60_000

export function validateExternalIntegrationSourceToken(input: {
  token: string
  requiredScope?: string
}): ExternalIntegrationSourceAuthResult {
  const token = input.token.trim()
  if (!token) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_token_missing',
      message: '缺少来源系统 token'
    }
  }

  const row = getBusinessDatabase().prepare(`
    SELECT
      sources.id AS source_row_id,
      sources.name AS source_name,
      sources.status AS source_status,
      sources.scopes_json AS source_scopes_json,
      sources.rate_limits_json AS source_rate_limits_json,
      sources.expires_at AS source_expires_at,
      sources.last_used_at AS source_last_used_at,
      tokens.id AS token_id,
      tokens.name AS token_name,
      tokens.token_prefix,
      tokens.token_suffix,
      tokens.status AS token_status,
      tokens.scopes_json AS token_scopes_json,
      tokens.expires_at AS token_expires_at,
      tokens.last_used_at AS token_last_used_at
    FROM external_integration_source_tokens AS tokens
    JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
    WHERE tokens.token_hash = ?
    LIMIT 1
  `).get(hashExternalIntegrationSourceTokenValue(token)) as ExternalIntegrationSourceTokenRow | undefined

  if (!row) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_unauthorized',
      message: '来源系统或 token 无效'
    }
  }

  const now = nowIso()
  const sourceScopes = decodeScopes(row.source_scopes_json)
  const tokenScopes = decodeScopes(row.token_scopes_json)
  const grantedScopes = tokenScopes.filter((scope) => sourceScopes.includes(scope))
  const context = externalIntegrationAuthContextFromRow(row, grantedScopes, now)
  const sourceStatus = normalizeSourceStatus(row.source_status)
  const tokenStatus = normalizeTokenStatus(row.token_status)
  if (sourceStatus !== 'active') {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_disabled',
      message: '来源系统未启用',
      context
    }
  }
  if (row.source_expires_at && row.source_expires_at <= now) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_expired',
      message: '来源系统已过期',
      context
    }
  }
  if (tokenStatus !== 'active' || (row.token_expires_at && row.token_expires_at <= now)) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_token_unavailable',
      message: '来源系统 token 不可用',
      context
    }
  }

  if (input.requiredScope && !grantedScopes.includes(input.requiredScope)) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_scope_forbidden',
      message: '来源系统没有调用该接口的权限',
      context
    }
  }

  touchExternalIntegrationSourceLastUsed(row, now)
  return {
    ok: true,
    context
  }
}

export async function validateExternalIntegrationSourceTokenAsync(input: {
  token: string
  requiredScope?: string
}): Promise<ExternalIntegrationSourceAuthResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return validateExternalIntegrationSourceToken(input)
  }
  const token = input.token.trim()
  if (!token) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_token_missing',
      message: '缺少来源系统 token'
    }
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<ExternalIntegrationSourceTokenRow>(`
    SELECT
      sources.id AS source_row_id,
      sources.name AS source_name,
      sources.status AS source_status,
      sources.scopes_json AS source_scopes_json,
      sources.rate_limits_json AS source_rate_limits_json,
      sources.expires_at AS source_expires_at,
      sources.last_used_at AS source_last_used_at,
      tokens.id AS token_id,
      tokens.name AS token_name,
      tokens.token_prefix,
      tokens.token_suffix,
      tokens.status AS token_status,
      tokens.scopes_json AS token_scopes_json,
      tokens.expires_at AS token_expires_at,
      tokens.last_used_at AS token_last_used_at
    FROM ${externalIntegrationAuthBusinessTable(client, 'external_integration_source_tokens')} AS tokens
    JOIN ${externalIntegrationAuthBusinessTable(client, 'external_integration_sources')} AS sources ON sources.id = tokens.source_ref_id
    WHERE tokens.token_hash = ?
    LIMIT 1
  `, [hashExternalIntegrationSourceTokenValue(token)])

  if (!row) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_unauthorized',
      message: '来源系统或 token 无效'
    }
  }

  const now = nowIso()
  const sourceScopes = decodeScopes(row.source_scopes_json)
  const tokenScopes = decodeScopes(row.token_scopes_json)
  const grantedScopes = tokenScopes.filter((scope) => sourceScopes.includes(scope))
  const context = externalIntegrationAuthContextFromRow(row, grantedScopes, now)
  const sourceStatus = normalizeSourceStatus(row.source_status)
  const tokenStatus = normalizeTokenStatus(row.token_status)
  if (sourceStatus !== 'active') {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_disabled',
      message: '来源系统未启用',
      context
    }
  }
  if (row.source_expires_at && row.source_expires_at <= now) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_expired',
      message: '来源系统已过期',
      context
    }
  }
  if (tokenStatus !== 'active' || (row.token_expires_at && row.token_expires_at <= now)) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_token_unavailable',
      message: '来源系统 token 不可用',
      context
    }
  }

  if (input.requiredScope && !grantedScopes.includes(input.requiredScope)) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_scope_forbidden',
      message: '来源系统没有调用该接口的权限',
      context
    }
  }

  await touchExternalIntegrationSourceLastUsedAsync(client, row, now)
  return {
    ok: true,
    context
  }
}

function externalIntegrationAuthContextFromRow(
  row: ExternalIntegrationSourceTokenRow,
  grantedScopes: string[],
  authenticatedAt: string
): ExternalIntegrationSourceAuthContext {
  return {
    sourceRefId: row.source_row_id,
    sourceName: row.source_name,
    tokenId: row.token_id,
    tokenName: row.token_name,
    tokenPrefix: row.token_prefix,
    scopes: grantedScopes,
    rateLimits: decodeRateLimits(row.source_rate_limits_json),
    authenticatedAt,
    isTestToken: isBuiltInExternalIntegrationTestSourceId(row.source_row_id) || isBuiltInExternalIntegrationTestTokenId(row.token_id)
  }
}

function touchExternalIntegrationSourceLastUsed(row: ExternalIntegrationSourceTokenRow, now: string): void {
  const database = getBusinessDatabase()
  if (shouldTouchLastUsed(row.token_last_used_at, now)) {
    database
      .prepare('UPDATE external_integration_source_tokens SET last_used_at = ?, updated_at = ? WHERE id = ?')
      .run(now, now, row.token_id)
  }
  if (shouldTouchLastUsed(row.source_last_used_at, now)) {
    database
      .prepare('UPDATE external_integration_sources SET last_used_at = ?, updated_at = ? WHERE id = ?')
      .run(now, now, row.source_row_id)
  }
}

async function touchExternalIntegrationSourceLastUsedAsync(client: DatabaseClient, row: ExternalIntegrationSourceTokenRow, now: string): Promise<void> {
  const updates: Promise<unknown>[] = []
  if (shouldTouchLastUsed(row.token_last_used_at, now)) {
    updates.push(client.execute(`
      UPDATE ${externalIntegrationAuthBusinessTable(client, 'external_integration_source_tokens')}
      SET last_used_at = ?, updated_at = ?
      WHERE id = ?
    `, [now, now, row.token_id]))
  }
  if (shouldTouchLastUsed(row.source_last_used_at, now)) {
    updates.push(client.execute(`
      UPDATE ${externalIntegrationAuthBusinessTable(client, 'external_integration_sources')}
      SET last_used_at = ?, updated_at = ?
      WHERE id = ?
    `, [now, now, row.source_row_id]))
  }
  await Promise.all(updates)
}

function shouldTouchLastUsed(previous: string | null, now: string): boolean {
  if (!previous) {
    return true
  }
  const previousTime = Date.parse(previous)
  const nowTime = Date.parse(now)
  if (!Number.isFinite(previousTime) || !Number.isFinite(nowTime)) {
    return true
  }
  return nowTime - previousTime >= touchLastUsedIntervalMs
}

function externalIntegrationAuthBusinessTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
}
