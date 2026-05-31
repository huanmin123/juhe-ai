import { randomBytes } from 'node:crypto'
import type { SQLInputValue } from 'node:sqlite'

import { hashSecret } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'

export const externalIntegrationSourceAuthDemoScope = 'external_integrations:source_auth_demo:read'
export const externalIntegrationIpUsageReadScope = 'juhe_ai_ip_usage:read'
export const externalIntegrationAccountPushScope = 'juhe_ai_account_push:write'
export const externalIntegrationTestToken = process.env.JUHE_AI_EXTERNAL_SOURCE_TEST_TOKEN?.trim() || 'juis_test_mock_public_token'
export const externalIntegrationTestTokenPrefix = externalIntegrationTestToken.slice(0, 12)

export const externalIntegrationScopeOptions = [
  { value: externalIntegrationSourceAuthDemoScope, label: '来源鉴权 demo' },
  { value: externalIntegrationIpUsageReadScope, label: 'IP 聚合读取' },
  { value: externalIntegrationAccountPushScope, label: '公开资源写入' }
] as const

export type ExternalIntegrationSourceStatus = 'active' | 'disabled'
export type ExternalIntegrationSourceTokenStatus = 'active' | 'disabled' | 'revoked'

export interface ExternalIntegrationRateLimitRule {
  windowSeconds: number
  maxRequests: number
}

export interface ExternalIntegrationSourceInput {
  name: string
  status?: ExternalIntegrationSourceStatus
  scopes?: string[]
  rateLimits?: ExternalIntegrationRateLimitRule[]
  expiresAt?: string | null
  notes?: string | null
}

export interface ExternalIntegrationSourceUpdateInput {
  name?: string
  status?: ExternalIntegrationSourceStatus
  scopes?: string[]
  rateLimits?: ExternalIntegrationRateLimitRule[]
  expiresAt?: string | null
  notes?: string | null
}

export interface ExternalIntegrationSourceTokenInput {
  sourceRefId?: string
  name: string
  token?: string
  status?: ExternalIntegrationSourceTokenStatus
  scopes?: string[]
  expiresAt?: string | null
}

export interface ExternalIntegrationSourceTokenUpdateInput {
  name?: string
  status?: ExternalIntegrationSourceTokenStatus
  scopes?: string[]
  expiresAt?: string | null
}

export interface CreatedExternalIntegrationSourceToken {
  id: string
  name: string
  token: string
  tokenPrefix: string
  scopes: string[]
  expiresAt?: string
}

export interface ExternalIntegrationSourceTokenSummary {
  id: string
  name: string
  tokenPrefix: string
  status: ExternalIntegrationSourceTokenStatus
  scopes: string[]
  expiresAt?: string
  lastUsedAt?: string
  createdAt: string
  updatedAt: string
  revokedAt?: string
}

export interface ExternalIntegrationSourceSummary {
  id: string
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  expiresAt?: string
  notes?: string
  lastUsedAt?: string
  createdAt: string
  updatedAt: string
  tokenCount: number
  activeTokenCount: number
  tokens: ExternalIntegrationSourceTokenSummary[]
}

export interface ExternalIntegrationSourceListResult {
  items: ExternalIntegrationSourceSummary[]
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
}

export interface ExternalIntegrationSourceListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  status?: ExternalIntegrationSourceStatus | 'all'
}

export interface ExternalIntegrationSourceAuthContext {
  sourceRefId: string
  sourceName: string
  tokenId: string
  tokenName: string
  tokenPrefix: string
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  authenticatedAt: string
  isTestToken: boolean
}

export type ExternalIntegrationSourceAuthResult =
  | { ok: true; context: ExternalIntegrationSourceAuthContext }
  | { ok: false; statusCode: 401 | 403; code: string; message: string }

interface ExternalIntegrationSourceTokenRow {
  source_row_id: string
  source_name: string
  source_status: string
  source_scopes_json: string
  source_rate_limits_json: string | null
  source_expires_at: string | null
  source_last_used_at: string | null
  token_id: string
  token_name: string
  token_prefix: string
  token_status: string
  token_scopes_json: string
  token_expires_at: string | null
  token_last_used_at: string | null
}

interface ExternalIntegrationSourceRow {
  id: string
  name: string
  status: string
  scopes_json: string
  rate_limits_json: string | null
  expires_at: string | null
  notes: string | null
  last_used_at: string | null
  created_at: string
  updated_at: string
}

interface ExternalIntegrationSourceListRow extends ExternalIntegrationSourceRow {
  token_count: number
  active_token_count: number
}

interface ExternalIntegrationSourceTokenListRow {
  id: string
  source_ref_id: string
  name: string
  token_prefix: string
  status: string
  scopes_json: string
  expires_at: string | null
  last_used_at: string | null
  created_at: string
  updated_at: string
  revoked_at: string | null
}

const generatedTokenPrefix = 'juis_'
const touchLastUsedIntervalMs = 60_000
const defaultPageSize = 20
const maxPageSize = 100

export function listExternalIntegrationSources(options: ExternalIntegrationSourceListOptions = {}): ExternalIntegrationSourceListResult {
  const page = Math.max(1, Math.trunc(options.page ?? 1))
  const pageSize = Math.max(1, Math.min(Math.trunc(options.pageSize ?? defaultPageSize), maxPageSize))
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
  const rows = getDatabase().prepare(`
    SELECT
      sources.*,
      COUNT(tokens.id) AS token_count,
      SUM(CASE WHEN tokens.status = 'active' THEN 1 ELSE 0 END) AS active_token_count
    FROM external_integration_sources AS sources
    LEFT JOIN external_integration_source_tokens AS tokens ON tokens.source_ref_id = sources.id
    ${whereSql}
    GROUP BY sources.id
    ORDER BY sources.updated_at DESC, sources.id DESC
    LIMIT ? OFFSET ?
  `).all(...params, pageSize + 1, offset) as unknown as ExternalIntegrationSourceListRow[]

  const pageRows = rows.slice(0, pageSize)
  const tokensBySourceId = loadTokensBySourceIds(pageRows.map((row) => row.id))
  return {
    items: pageRows.map((row) => mapSourceSummary(row, tokensBySourceId.get(row.id) ?? [])),
    page,
    pageSize,
    pageUpperBound: offset + pageRows.length + (rows.length > pageSize ? 1 : 0),
    hasMore: rows.length > pageSize
  }
}

export function findExternalIntegrationSource(id: string): ExternalIntegrationSourceSummary | undefined {
  const row = getDatabase()
    .prepare('SELECT *, 0 AS token_count, 0 AS active_token_count FROM external_integration_sources WHERE id = ?')
    .get(id) as ExternalIntegrationSourceListRow | undefined
  if (!row) {
    return undefined
  }
  const tokens = loadTokensBySourceIds([row.id]).get(row.id) ?? []
  return mapSourceSummary({
    ...row,
    token_count: tokens.length,
    active_token_count: tokens.filter((token) => token.status === 'active').length
  }, tokens)
}

export function createExternalIntegrationSource(input: ExternalIntegrationSourceInput): ExternalIntegrationSourceSummary {
  const name = normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  const now = nowIso()
  const id = newId('extsrc')
  ensureSourceNameAvailable(name)
  try {
    getDatabase().prepare(`
      INSERT INTO external_integration_sources (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      id,
      name,
      normalizeSourceStatus(input.status),
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

export function upsertExternalIntegrationSource(input: ExternalIntegrationSourceInput): { id: string; name: string } {
  const name = normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  const database = getDatabase()
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
      normalizeSourceStatus(input.status),
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
    normalizeSourceStatus(input.status),
    encodeScopes(input.scopes),
    encodeRateLimits(input.rateLimits),
    normalizeNullableIso(input.expiresAt),
    normalizeNullableText(input.notes),
    now,
    now
  )
  return { id, name }
}

export function updateExternalIntegrationSource(id: string, input: ExternalIntegrationSourceUpdateInput): ExternalIntegrationSourceSummary | undefined {
  const existing = findSourceRow(id)
  if (!existing) {
    return undefined
  }
  const nextName = input.name === undefined ? existing.name : normalizeNameOrThrow(input.name, '来源系统名称不能为空')
  if (nextName !== existing.name) {
    ensureSourceNameAvailable(nextName, id)
  }
  const nextStatus = input.status === undefined ? normalizeSourceStatus(existing.status as ExternalIntegrationSourceStatus) : normalizeSourceStatus(input.status)
  const nextScopes = input.scopes === undefined ? existing.scopes_json : encodeScopes(input.scopes)
  const nextRateLimits = input.rateLimits === undefined ? existing.rate_limits_json : encodeRateLimits(input.rateLimits)
  const nextExpiresAt = input.expiresAt === undefined ? existing.expires_at : normalizeNullableIso(input.expiresAt)
  const nextNotes = input.notes === undefined ? existing.notes : normalizeNullableText(input.notes)
  getDatabase().prepare(`
    UPDATE external_integration_sources
    SET name = ?, status = ?, scopes_json = ?, rate_limits_json = ?, expires_at = ?, notes = ?, updated_at = ?
    WHERE id = ?
  `).run(nextName, nextStatus, nextScopes, nextRateLimits, nextExpiresAt, nextNotes, nowIso(), id)
  return requiredSource(id)
}

export function createExternalIntegrationSourceToken(input: ExternalIntegrationSourceTokenInput): CreatedExternalIntegrationSourceToken {
  const name = normalizeNameOrThrow(input.name, '来源系统 token 名称不能为空')
  const source = resolveSourceForToken(input)
  const token = input.token?.trim() || createExternalIntegrationSourceTokenValue()
  const scopes = normalizeScopes(input.scopes)
  const now = nowIso()
  const id = newId('exttok')
  const tokenPrefix = token.slice(0, 12)
  try {
    getDatabase().prepare(`
      INSERT INTO external_integration_source_tokens (
        id, source_ref_id, name, token_hash, token_prefix, status, scopes_json, expires_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      id,
      source.id,
      name,
      hashExternalIntegrationSourceToken(token),
      tokenPrefix,
      normalizeTokenStatus(input.status),
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
    scopes,
    expiresAt: normalizeNullableIso(input.expiresAt) ?? undefined
  }
}

export function updateExternalIntegrationSourceToken(sourceRefId: string, tokenId: string, input: ExternalIntegrationSourceTokenUpdateInput): ExternalIntegrationSourceTokenSummary | undefined {
  const existing = getDatabase().prepare(`
    SELECT tokens.*
    FROM external_integration_source_tokens AS tokens
    JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
    WHERE sources.id = ? AND tokens.id = ?
  `).get(sourceRefId, tokenId) as ExternalIntegrationSourceTokenListRow | undefined
  if (!existing) {
    return undefined
  }
  const nextStatus = input.status === undefined ? normalizeTokenStatus(existing.status as ExternalIntegrationSourceTokenStatus) : normalizeTokenStatus(input.status)
  const revokedAt = nextStatus === 'revoked' && existing.status !== 'revoked'
    ? nowIso()
    : nextStatus === 'revoked'
      ? existing.revoked_at
      : null
  getDatabase().prepare(`
    UPDATE external_integration_source_tokens
    SET name = ?, status = ?, scopes_json = ?, expires_at = ?, revoked_at = ?, updated_at = ?
    WHERE id = ?
  `).run(
    input.name === undefined ? existing.name : normalizeNameOrThrow(input.name, '来源系统 token 名称不能为空'),
    nextStatus,
    input.scopes === undefined ? existing.scopes_json : encodeScopes(input.scopes),
    input.expiresAt === undefined ? existing.expires_at : normalizeNullableIso(input.expiresAt),
    revokedAt,
    nowIso(),
    tokenId
  )
  return requiredSource(sourceRefId).tokens.find((token) => token.id === tokenId)
}

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

  const testTokenResult = validateExternalIntegrationTestToken(token, input.requiredScope)
  if (testTokenResult) {
    return testTokenResult
  }

  const row = getDatabase().prepare(`
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
      tokens.status AS token_status,
      tokens.scopes_json AS token_scopes_json,
      tokens.expires_at AS token_expires_at,
      tokens.last_used_at AS token_last_used_at
    FROM external_integration_source_tokens AS tokens
    JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
    WHERE tokens.token_hash = ?
    LIMIT 1
  `).get(hashExternalIntegrationSourceToken(token)) as ExternalIntegrationSourceTokenRow | undefined

  if (!row) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_unauthorized',
      message: '来源系统或 token 无效'
    }
  }

  const now = nowIso()
  if (row.source_status !== 'active') {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_disabled',
      message: '来源系统未启用'
    }
  }
  if (row.source_expires_at && row.source_expires_at <= now) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_expired',
      message: '来源系统已过期'
    }
  }
  if (row.token_status !== 'active' || (row.token_expires_at && row.token_expires_at <= now)) {
    return {
      ok: false,
      statusCode: 401,
      code: 'external_source_token_unavailable',
      message: '来源系统 token 不可用'
    }
  }

  const sourceScopes = decodeScopes(row.source_scopes_json)
  const tokenScopes = decodeScopes(row.token_scopes_json)
  const grantedScopes = tokenScopes.filter((scope) => sourceScopes.includes(scope))
  if (input.requiredScope && !grantedScopes.includes(input.requiredScope)) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_scope_forbidden',
      message: '来源系统没有调用该接口的权限'
    }
  }

  touchExternalIntegrationSourceLastUsed(row, now)
  return {
    ok: true,
    context: {
      sourceRefId: row.source_row_id,
      sourceName: row.source_name,
      tokenId: row.token_id,
      tokenName: row.token_name,
      tokenPrefix: row.token_prefix,
      scopes: grantedScopes,
      rateLimits: decodeRateLimits(row.source_rate_limits_json),
      authenticatedAt: now,
      isTestToken: false
    }
  }
}

function validateExternalIntegrationTestToken(token: string, requiredScope?: string): ExternalIntegrationSourceAuthResult | undefined {
  if (token !== externalIntegrationTestToken) {
    return undefined
  }
  const scopes = externalIntegrationScopeOptions.map((item) => item.value)
  if (requiredScope && !scopes.includes(requiredScope as (typeof scopes)[number])) {
    return {
      ok: false,
      statusCode: 403,
      code: 'external_source_scope_forbidden',
      message: '测试 token 没有调用该接口的权限'
    }
  }
  return {
    ok: true,
    context: {
      sourceRefId: 'mock_external_source',
      sourceName: '内置测试来源',
      tokenId: 'mock_external_source_token',
      tokenName: '内置测试 token',
      tokenPrefix: externalIntegrationTestTokenPrefix,
      scopes,
      rateLimits: [{ windowSeconds: 60, maxRequests: 120 }],
      authenticatedAt: nowIso(),
      isTestToken: true
    }
  }
}

function requiredSource(id: string): ExternalIntegrationSourceSummary {
  const source = findExternalIntegrationSource(id)
  if (!source) {
    throw new Error('来源系统不存在')
  }
  return source
}

function findSourceRow(id: string): ExternalIntegrationSourceRow | undefined {
  return getDatabase()
    .prepare('SELECT * FROM external_integration_sources WHERE id = ?')
    .get(id) as ExternalIntegrationSourceRow | undefined
}

function resolveSourceForToken(input: ExternalIntegrationSourceTokenInput): Pick<ExternalIntegrationSourceRow, 'id'> {
  if (input.sourceRefId) {
    const source = getDatabase()
      .prepare('SELECT id FROM external_integration_sources WHERE id = ?')
      .get(input.sourceRefId) as Pick<ExternalIntegrationSourceRow, 'id'> | undefined
    if (!source) {
      throw new Error('来源系统不存在')
    }
    return source
  }
  throw new Error('来源系统不存在')
}

function loadTokensBySourceIds(sourceIds: string[]): Map<string, ExternalIntegrationSourceTokenSummary[]> {
  const result = new Map<string, ExternalIntegrationSourceTokenSummary[]>()
  if (!sourceIds.length) {
    return result
  }
  const placeholders = sourceIds.map(() => '?').join(',')
  const rows = getDatabase().prepare(`
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

function mapSourceSummary(row: ExternalIntegrationSourceListRow, tokens: ExternalIntegrationSourceTokenSummary[]): ExternalIntegrationSourceSummary {
  return {
    id: row.id,
    name: row.name,
    status: normalizeSourceStatus(row.status as ExternalIntegrationSourceStatus),
    scopes: decodeScopes(row.scopes_json),
    rateLimits: decodeRateLimits(row.rate_limits_json),
    expiresAt: row.expires_at ?? undefined,
    notes: row.notes ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    tokenCount: Number(row.token_count ?? tokens.length),
    activeTokenCount: Number(row.active_token_count ?? tokens.filter((token) => token.status === 'active').length),
    tokens
  }
}

function mapTokenSummary(row: ExternalIntegrationSourceTokenListRow): ExternalIntegrationSourceTokenSummary {
  return {
    id: row.id,
    name: row.name,
    tokenPrefix: row.token_prefix,
    status: normalizeTokenStatus(row.status as ExternalIntegrationSourceTokenStatus),
    scopes: decodeScopes(row.scopes_json),
    expiresAt: row.expires_at ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    revokedAt: row.revoked_at ?? undefined
  }
}

function normalizeNameOrThrow(value: string, message: string): string {
  const name = value.trim()
  if (!name) {
    throw new Error(message)
  }
  return name
}

function ensureSourceNameAvailable(name: string, currentId?: string): void {
  const existing = getDatabase()
    .prepare('SELECT id FROM external_integration_sources WHERE lower(name) = lower(?) LIMIT 1')
    .get(name) as Pick<ExternalIntegrationSourceRow, 'id'> | undefined
  if (existing && existing.id !== currentId) {
    throw new Error('来源系统名称已存在')
  }
}

function hashExternalIntegrationSourceToken(token: string): string {
  return hashSecret(`external-integration-source-token:${token}`)
}

function createExternalIntegrationSourceTokenValue(): string {
  return `${generatedTokenPrefix}${randomBytes(32).toString('base64url')}`
}

function normalizeSourceStatus(status: ExternalIntegrationSourceStatus | undefined): ExternalIntegrationSourceStatus {
  return status === 'disabled' ? 'disabled' : 'active'
}

function normalizeTokenStatus(status: ExternalIntegrationSourceTokenStatus | undefined): ExternalIntegrationSourceTokenStatus {
  if (status === 'disabled' || status === 'revoked') {
    return status
  }
  return 'active'
}

function encodeScopes(scopes: string[] | undefined): string {
  return JSON.stringify(normalizeScopes(scopes))
}

function normalizeScopes(scopes: string[] | undefined): string[] {
  const values = new Set<string>()
  for (const scope of scopes ?? []) {
    const value = scope.trim()
    if (value) {
      values.add(value)
    }
  }
  return [...values].sort()
}

function decodeScopes(value: string): string[] {
  try {
    const parsed = JSON.parse(value)
    if (!Array.isArray(parsed)) {
      return []
    }
    return parsed.filter((item): item is string => typeof item === 'string' && item.length > 0)
  } catch {
    return []
  }
}

function encodeRateLimits(rules: ExternalIntegrationRateLimitRule[] | undefined): string {
  return JSON.stringify(normalizeRateLimits(rules))
}

function decodeRateLimits(value: string | null | undefined): ExternalIntegrationRateLimitRule[] {
  if (!value) {
    return []
  }
  try {
    const parsed = JSON.parse(value)
    return normalizeRateLimits(Array.isArray(parsed) ? parsed : [])
  } catch {
    return []
  }
}

function normalizeRateLimits(rules: ExternalIntegrationRateLimitRule[] | undefined): ExternalIntegrationRateLimitRule[] {
  const normalized: ExternalIntegrationRateLimitRule[] = []
  const seen = new Set<number>()
  for (const rule of rules ?? []) {
    const windowSeconds = Math.trunc(Number(rule.windowSeconds))
    const maxRequests = Math.trunc(Number(rule.maxRequests))
    if (!Number.isFinite(windowSeconds) || !Number.isFinite(maxRequests)) {
      continue
    }
    if (windowSeconds < 1 || windowSeconds > 86_400 || maxRequests < 1 || maxRequests > 100_000) {
      continue
    }
    if (seen.has(windowSeconds)) {
      continue
    }
    seen.add(windowSeconds)
    normalized.push({ windowSeconds, maxRequests })
  }
  return normalized.sort((a, b) => a.windowSeconds - b.windowSeconds)
}

function normalizeNullableIso(value: string | null | undefined): string | null {
  const text = value?.trim()
  if (!text) {
    return null
  }
  const time = Date.parse(text)
  if (!Number.isFinite(time)) {
    throw new Error('过期时间无效')
  }
  return new Date(time).toISOString()
}

function normalizeNullableText(value: string | null | undefined): string | null {
  return value?.trim() || null
}

function touchExternalIntegrationSourceLastUsed(row: ExternalIntegrationSourceTokenRow, now: string): void {
  const database = getDatabase()
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

function isUniqueConstraintError(error: unknown): boolean {
  return error instanceof Error && error.message.includes('UNIQUE constraint failed')
}
