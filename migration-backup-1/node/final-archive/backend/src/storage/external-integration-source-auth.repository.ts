import { getBusinessDatabase, isSqliteDatabaseLocked, nowIso, runWithSqliteBusyTimeout } from './database.js'
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
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'

const touchLastUsedIntervalMs = 60_000
const sqliteTouchLastUsedBusyTimeoutMs = 25
const sqliteTouchLastUsedRetryDelayMs = 250
const sqliteTouchLastUsedMaxAttempts = 5
const sqliteTouchLastUsedBatchSize = 100

interface PendingLastUsedTouch {
  tokenId?: string
  sourceRefId?: string
  now: string
  attempts: number
}

const pendingLastUsedTouches = new Map<string, PendingLastUsedTouch>()
let pendingLastUsedTouchTimer: NodeJS.Timeout | undefined

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

  const row = loadExternalIntegrationSourceTokenForAuthReadOnly(token)

  if (!row) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_unauthorized',
      message: '来源系统或 token 无效'
    }
  }

  const now = nowIso()
  const result = validateExternalIntegrationSourceTokenRow(row, input, now)
  if (!result.ok) return result
  touchExternalIntegrationSourceLastUsed(row, now)
  return result
}

export function loadExternalIntegrationSourceTokenForAuthReadOnly(token: string): ExternalIntegrationSourceTokenRow | undefined {
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
  if (!row) return undefined
  return normalizeExternalIntegrationSourceTokenRow(row)
}

function validateExternalIntegrationSourceTokenRow(
  row: ExternalIntegrationSourceTokenRow,
  input: { requiredScope?: string },
  now: string
): ExternalIntegrationSourceAuthResult {
  const normalizedRow = normalizeExternalIntegrationSourceTokenRow(row)
  const sourceScopes = decodeScopes(normalizedRow.source_scopes_json)
  const tokenScopes = decodeScopes(normalizedRow.token_scopes_json)
  const grantedScopes = tokenScopes.filter((scope) => sourceScopes.includes(scope))
  const context = externalIntegrationAuthContextFromRow(normalizedRow, grantedScopes, now)
  const sourceStatus = normalizeSourceStatus(normalizedRow.source_status)
  const tokenStatus = normalizeTokenStatus(normalizedRow.token_status)
  if (sourceStatus !== 'active') {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_disabled',
      message: '来源系统未启用',
      context
    }
  }
  const nowMs = rfc3339InstantMilliseconds(now)
  if (nowMs === undefined) throw new Error('外部集成鉴权 now 必须是带 Z 或数值 offset 的 RFC3339 时间')
  const sourceExpiresAtMs = rfc3339InstantMilliseconds(normalizedRow.source_expires_at)
  const tokenExpiresAtMs = rfc3339InstantMilliseconds(normalizedRow.token_expires_at)
  if (sourceExpiresAtMs !== undefined && sourceExpiresAtMs <= nowMs) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_expired',
      message: '来源系统已过期',
      context
    }
  }
  if (tokenStatus !== 'active' || (tokenExpiresAtMs !== undefined && tokenExpiresAtMs <= nowMs)) {
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
    const token = input.token.trim()
    if (!token) {
      return {
        ok: false,
        statusCode: 401,
        code: 'external_source_token_missing',
        message: '缺少来源系统 token'
      }
    }
    const row = sqliteReadWorkerPoolEnabled()
      ? await requestSqliteReadWorker({
        type: 'load_external_integration_source_token_for_auth_read_only',
        token
      })
      : loadExternalIntegrationSourceTokenForAuthReadOnly(token)
    if (!row) {
      return {
        ok: false,
        statusCode: 401,
        code: 'external_source_unauthorized',
        message: '来源系统或 token 无效'
      }
    }
    const normalizedRow = normalizeExternalIntegrationSourceTokenRow(row)
    const now = nowIso()
    const result = validateExternalIntegrationSourceTokenRow(normalizedRow, input, now)
    if (result.ok) {
      scheduleExternalIntegrationSourceLastUsedTouch(normalizedRow, now)
    }
    return result
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

  const normalizedRow = normalizeExternalIntegrationSourceTokenRow(row)
  const now = nowIso()
  const result = validateExternalIntegrationSourceTokenRow(normalizedRow, input, now)
  if (result.ok) {
    await touchExternalIntegrationSourceLastUsedAsync(client, normalizedRow, now)
  }
  return result
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

function scheduleExternalIntegrationSourceLastUsedTouch(row: ExternalIntegrationSourceTokenRow, now: string): void {
  const tokenId = shouldTouchLastUsed(row.token_last_used_at, now) ? row.token_id : undefined
  const sourceRefId = shouldTouchLastUsed(row.source_last_used_at, now) ? row.source_row_id : undefined
  if (!tokenId && !sourceRefId) {
    return
  }
  enqueueExternalIntegrationSourceLastUsedTouch({
    tokenId,
    sourceRefId,
    now,
    attempts: 0
  })
}

function enqueueExternalIntegrationSourceLastUsedTouch(touch: PendingLastUsedTouch): void {
  mergeExternalIntegrationSourceLastUsedTouch(touch)
  scheduleExternalIntegrationSourceLastUsedFlush(0)
}

function mergeExternalIntegrationSourceLastUsedTouch(touch: PendingLastUsedTouch): void {
  const key = `${touch.sourceRefId ?? ''}\u0000${touch.tokenId ?? ''}`
  const current = pendingLastUsedTouches.get(key)
  pendingLastUsedTouches.set(key, current
    ? {
      tokenId: touch.tokenId ?? current.tokenId,
      sourceRefId: touch.sourceRefId ?? current.sourceRefId,
      now: laterInstant(touch.now, current.now),
      attempts: Math.max(current.attempts, touch.attempts)
    }
    : touch)
}

function scheduleExternalIntegrationSourceLastUsedFlush(delayMs: number): void {
  if (pendingLastUsedTouchTimer) {
    return
  }
  pendingLastUsedTouchTimer = setTimeout(() => {
    pendingLastUsedTouchTimer = undefined
    try {
      flushExternalIntegrationSourceLastUsedTouches()
    } catch {
      // last_used_at 是低频观测字段，后台 touch 失败不能影响公开查询响应。
    }
  }, Math.max(0, delayMs))
  pendingLastUsedTouchTimer.unref()
}

function flushExternalIntegrationSourceLastUsedTouches(): void {
  const batch = [...pendingLastUsedTouches.entries()].slice(0, sqliteTouchLastUsedBatchSize)
  if (!batch.length) {
    return
  }
  for (const [key] of batch) {
    pendingLastUsedTouches.delete(key)
  }
  try {
    runWithSqliteBusyTimeout(getBusinessDatabase(), sqliteTouchLastUsedBusyTimeoutMs, () => {
      const database = getBusinessDatabase()
      const touchToken = database.prepare('UPDATE external_integration_source_tokens SET last_used_at = ?, updated_at = ? WHERE id = ?')
      const touchSource = database.prepare('UPDATE external_integration_sources SET last_used_at = ?, updated_at = ? WHERE id = ?')
      for (const [, touch] of batch) {
        if (touch.tokenId) {
          touchToken.run(touch.now, touch.now, touch.tokenId)
        }
        if (touch.sourceRefId) {
          touchSource.run(touch.now, touch.now, touch.sourceRefId)
        }
      }
    })
  } catch (error) {
    if (isSqliteDatabaseLocked(error)) {
      for (const [, touch] of batch) {
        if (touch.attempts < sqliteTouchLastUsedMaxAttempts) {
          mergeExternalIntegrationSourceLastUsedTouch({
            ...touch,
            attempts: touch.attempts + 1
          })
        }
      }
      scheduleExternalIntegrationSourceLastUsedFlush(sqliteTouchLastUsedRetryDelayMs)
      return
    }
    throw error
  }
  if (pendingLastUsedTouches.size) {
    scheduleExternalIntegrationSourceLastUsedFlush(0)
  }
}

export async function flushExternalIntegrationSourceLastUsedTouchesForTest(): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'sqlite') {
    throw new Error('flushExternalIntegrationSourceLastUsedTouchesForTest 仅支持 SQLite 测试路径；PostgreSQL 测试必须使用 async driver 刷新')
  }
  if (pendingLastUsedTouchTimer) {
    clearTimeout(pendingLastUsedTouchTimer)
    pendingLastUsedTouchTimer = undefined
  }
  flushExternalIntegrationSourceLastUsedTouches()
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
  if (previous === null) {
    return true
  }
  const previousTime = rfc3339InstantMilliseconds(previous)
  const nowTime = rfc3339InstantMilliseconds(now)
  if (previousTime === undefined) throw new Error('外部集成来源 last_used_at 必须是带 Z 或数值 offset 的 RFC3339 时间')
  if (nowTime === undefined) throw new Error('外部集成来源当前时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  return nowTime - previousTime >= touchLastUsedIntervalMs
}

function laterInstant(first: string, second: string): string {
  const firstMs = rfc3339InstantMilliseconds(first)
  const secondMs = rfc3339InstantMilliseconds(second)
  if (firstMs === undefined) throw new Error('外部集成来源当前时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  if (secondMs === undefined) throw new Error('外部集成来源当前时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  return firstMs >= secondMs ? first : second
}

function normalizeExternalIntegrationSourceTokenRow(row: ExternalIntegrationSourceTokenRow): ExternalIntegrationSourceTokenRow {
  return {
    ...row,
    source_expires_at: optionalInstant(row.source_expires_at, '外部集成来源 expires_at'),
    source_last_used_at: optionalInstant(row.source_last_used_at, '外部集成来源 last_used_at'),
    token_expires_at: optionalInstant(row.token_expires_at, '外部集成 token expires_at'),
    token_last_used_at: optionalInstant(row.token_last_used_at, '外部集成 token last_used_at')
  }
}

function optionalInstant(value: unknown, label: string): string | null {
  if (value === undefined || value === null) return null
  return requiredRfc3339Instant(value, label)
}

function externalIntegrationAuthBusinessTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
}
