import type { McpProxyServerRuntimeConfig } from '../config/runtime.js'
import { runtimeConfig } from '../config/runtime.js'
import { stableJsonStringify } from './audit-log-stable-json.js'
import { buildSystemAccountScopeClause, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'

export type OpenAICompatibleMcpServerApprovalPolicy = 'always' | 'never'

export interface OpenAICompatibleMcpServerInput {
  label: string
  serverUrl: string
  description?: string | null
  enabled?: boolean
  allowedTools?: string[]
  defaultApprovalPolicy?: OpenAICompatibleMcpServerApprovalPolicy
  timeoutMs?: number | null
  maxRetries?: number | null
  retryDelayMs?: number | null
  maxBodyBytes?: number | null
  maxOutputBytes?: number | null
  allowRequestAuthorization?: boolean
  authorizationRef?: string | null
}

export interface OpenAICompatibleMcpServerRecord {
  id: string
  systemAccountId: string
  label: string
  serverUrl: string
  description?: string
  enabled: boolean
  allowedTools: string[]
  defaultApprovalPolicy: OpenAICompatibleMcpServerApprovalPolicy
  timeoutMs?: number
  maxRetries?: number
  retryDelayMs?: number
  maxBodyBytes?: number
  maxOutputBytes?: number
  allowRequestAuthorization: boolean
  authorizationRef?: string
  createdAt: string
  updatedAt: string
}

export interface OpenAICompatibleMcpServerListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  enabled?: boolean
}

export interface OpenAICompatibleMcpServerListResult {
  items: OpenAICompatibleMcpServerRecord[]
  total: number
  page: number
  pageSize: number
}

export interface OpenAICompatibleMcpToolCacheInput {
  serverId: string
  systemAccountId: string
  serverLabel: string
  serverUrl: string
  tools: Array<{
    name: string
    description?: string
    inputSchema?: Record<string, unknown>
    annotations?: unknown
  }>
  checkedAt?: string
  expiresAt?: string | null
}

export interface OpenAICompatibleMcpToolCacheRecord {
  id: string
  serverId: string
  systemAccountId: string
  serverLabel: string
  serverUrl: string
  toolName: string
  description?: string
  inputSchema: Record<string, unknown>
  annotations: unknown
  lastCheckedAt: string
  expiresAt?: string
  createdAt: string
  updatedAt: string
}

export interface OpenAICompatibleMcpServerDiagnosticInput {
  serverId: string
  systemAccountId: string
  serverLabel: string
  serverUrl: string
  status: 'succeeded' | 'failed'
  toolCount?: number
  errorCode?: string
  errorMessage?: string
  omissionMetadata?: Record<string, unknown>
  startedAt: string
  finishedAt: string
  durationMs: number
}

export interface OpenAICompatibleMcpServerDiagnosticRecord {
  id: string
  serverId: string
  systemAccountId: string
  serverLabel: string
  serverUrl: string
  status: 'succeeded' | 'failed'
  toolCount: number
  errorCode?: string
  errorMessage?: string
  omissionMetadata?: Record<string, unknown>
  startedAt: string
  finishedAt: string
  durationMs: number
  createdAt: string
  updatedAt: string
}

interface OpenAICompatibleMcpServerRow {
  id: string
  system_account_id: string
  label: string
  server_url: string
  description: string | null
  enabled: number
  allowed_tools_json: string
  default_approval_policy: string
  timeout_ms: number | null
  max_retries: number | null
  retry_delay_ms: number | null
  max_body_bytes: number | null
  max_output_bytes: number | null
  allow_request_authorization: number
  authorization_ref: string | null
  created_at: string
  updated_at: string
}

interface OpenAICompatibleMcpToolCacheRow {
  id: string
  server_id: string
  system_account_id: string
  server_label: string
  server_url: string
  tool_name: string
  description: string | null
  input_schema_json: string
  annotations_json: string
  last_checked_at: string
  expires_at: string | null
  created_at: string
  updated_at: string
}

interface OpenAICompatibleMcpServerDiagnosticRow {
  id: string
  server_id: string
  system_account_id: string
  server_label: string
  server_url: string
  status: string
  tool_count: number
  error_code: string | null
  error_message: string | null
  omission_metadata_json: string | null
  started_at: string
  finished_at: string
  duration_ms: number
  created_at: string
  updated_at: string
}

const defaultMcpServerPage = 1
const defaultMcpServerPageSize = 20
const maxMcpServerPageSize = 100

export function createOpenAICompatibleMcpServer(
  input: OpenAICompatibleMcpServerInput,
  access: AccessScope
): OpenAICompatibleMcpServerRecord {
  const systemAccountId = manageableMcpServerSystemAccountId(access)
  const now = nowIso()
  const id = newId('mcpsrv')
  const normalized = normalizeMcpServerInput(input, { partial: false })
  getBusinessDatabase().prepare(`
    INSERT INTO openai_compatible_mcp_servers (
      id, system_account_id, label, server_url, description, enabled, allowed_tools_json,
      default_approval_policy, timeout_ms, max_retries, retry_delay_ms, max_body_bytes,
      max_output_bytes, allow_request_authorization, authorization_ref, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    id,
    systemAccountId,
    normalized.label,
    normalized.serverUrl,
    normalized.description ?? null,
    normalized.enabled ? 1 : 0,
    JSON.stringify(normalized.allowedTools),
    normalized.defaultApprovalPolicy,
    normalized.timeoutMs ?? null,
    normalized.maxRetries ?? null,
    normalized.retryDelayMs ?? null,
    normalized.maxBodyBytes ?? null,
    normalized.maxOutputBytes ?? null,
    normalized.allowRequestAuthorization ? 1 : 0,
    normalized.authorizationRef ?? null,
    now,
    now
  )
  const record = findOpenAICompatibleMcpServerForAccess(id, access)
  if (!record) {
    throw new Error(`MCP server ${id} was not readable after insert`)
  }
  return record
}

export function updateOpenAICompatibleMcpServer(
  id: string,
  input: Partial<OpenAICompatibleMcpServerInput>,
  access: AccessScope
): OpenAICompatibleMcpServerRecord | undefined {
  const current = findOpenAICompatibleMcpServerForAccess(id, access)
  if (!current) return undefined
  const normalized = normalizeMcpServerInput({ ...current, ...input }, { partial: false })
  const now = nowIso()
  const scope = buildSystemAccountScopeClause(access)
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_mcp_servers
    SET label = ?,
        server_url = ?,
        description = ?,
        enabled = ?,
        allowed_tools_json = ?,
        default_approval_policy = ?,
        timeout_ms = ?,
        max_retries = ?,
        retry_delay_ms = ?,
        max_body_bytes = ?,
        max_output_bytes = ?,
        allow_request_authorization = ?,
        authorization_ref = ?,
        updated_at = ?
    WHERE id = ?
      ${scope.clause}
  `).run(
    normalized.label,
    normalized.serverUrl,
    normalized.description ?? null,
    normalized.enabled ? 1 : 0,
    JSON.stringify(normalized.allowedTools),
    normalized.defaultApprovalPolicy,
    normalized.timeoutMs ?? null,
    normalized.maxRetries ?? null,
    normalized.retryDelayMs ?? null,
    normalized.maxBodyBytes ?? null,
    normalized.maxOutputBytes ?? null,
    normalized.allowRequestAuthorization ? 1 : 0,
    normalized.authorizationRef ?? null,
    now,
    id,
    ...scope.params
  )
  return findOpenAICompatibleMcpServerForAccess(id, access)
}

export function deleteOpenAICompatibleMcpServer(id: string, access: AccessScope): boolean {
  const scope = buildSystemAccountScopeClause(access)
  const result = getBusinessDatabase().prepare(`
    DELETE FROM openai_compatible_mcp_servers
    WHERE id = ?
      ${scope.clause}
  `).run(id, ...scope.params)
  return result.changes > 0
}

export function findOpenAICompatibleMcpServerForAccess(
  id: string,
  access?: AccessScope
): OpenAICompatibleMcpServerRecord | undefined {
  const scope = buildSystemAccountScopeClause(access)
  const row = getBusinessDatabase().prepare(`
    SELECT ${mcpServerColumns()}
    FROM openai_compatible_mcp_servers
    WHERE id = ?
      ${scope.clause}
    LIMIT 1
  `).get(id, ...scope.params) as unknown as OpenAICompatibleMcpServerRow | undefined
  return row ? mcpServerFromRow(row) : undefined
}

export function listOpenAICompatibleMcpServersPage(
  access?: AccessScope,
  options: OpenAICompatibleMcpServerListOptions = {}
): OpenAICompatibleMcpServerListResult {
  const page = normalizePage(options.page)
  const pageSize = normalizePageSize(options.pageSize)
  const offset = (page - 1) * pageSize
  const filter = buildMcpServerFilters(access, options)
  const database = getBusinessDatabase()
  const totalRow = database.prepare(`
    SELECT COUNT(*) AS count
    FROM openai_compatible_mcp_servers
    WHERE 1 = 1
      ${filter.clause}
  `).get(...filter.params) as { count?: number } | undefined
  const rows = database.prepare(`
    SELECT ${mcpServerColumns()}
    FROM openai_compatible_mcp_servers
    WHERE 1 = 1
      ${filter.clause}
    ORDER BY updated_at DESC, id DESC
    LIMIT ? OFFSET ?
  `).all(...filter.params, pageSize, offset) as unknown as OpenAICompatibleMcpServerRow[]
  return {
    items: rows.map(mcpServerFromRow),
    total: normalizeNonNegativeInteger(totalRow?.count),
    page,
    pageSize
  }
}

export function listRuntimeOpenAICompatibleMcpServers(systemAccountId?: string): McpProxyServerRuntimeConfig[] {
  const owner = typeof systemAccountId === 'string' ? systemAccountId.trim() : ''
  if (!owner) return []
  const rows = getBusinessDatabase().prepare(`
    SELECT ${mcpServerColumns()}
    FROM openai_compatible_mcp_servers
    WHERE system_account_id = ?
      AND enabled = 1
    ORDER BY updated_at DESC, id DESC
  `).all(owner) as unknown as OpenAICompatibleMcpServerRow[]
  return rows.map(mcpServerRuntimeConfigFromRecord)
}

export function listOpenAICompatibleMcpServerLabelsForSystemAccount(systemAccountId?: string): Set<string> {
  const owner = typeof systemAccountId === 'string' ? systemAccountId.trim() : ''
  if (!owner) return new Set()
  const rows = getBusinessDatabase().prepare(`
    SELECT label
    FROM openai_compatible_mcp_servers
    WHERE system_account_id = ?
  `).all(owner) as Array<{ label?: string | null }>
  return new Set(
    rows
      .map((row) => normalizeOptionalText(row.label ?? undefined))
      .filter((label): label is string => Boolean(label))
  )
}

export function replaceOpenAICompatibleMcpToolCache(input: OpenAICompatibleMcpToolCacheInput): OpenAICompatibleMcpToolCacheRecord[] {
  const now = nowIso()
  const checkedAt = input.checkedAt ?? now
  const tools = normalizeToolCacheTools(input.tools)
  const database = getBusinessDatabase()
  database.exec('BEGIN')
  try {
    database.prepare(`
      DELETE FROM openai_compatible_mcp_tool_cache
      WHERE server_id = ?
    `).run(input.serverId)
    const insert = database.prepare(`
      INSERT INTO openai_compatible_mcp_tool_cache (
        id, server_id, system_account_id, server_label, server_url, tool_name,
        description, input_schema_json, annotations_json, last_checked_at, expires_at,
        created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    for (const tool of tools) {
      insert.run(
        newId('mcptool'),
        input.serverId,
        input.systemAccountId,
        input.serverLabel,
        input.serverUrl,
        tool.name,
        tool.description ?? null,
        stableJsonStringify(tool.inputSchema ?? {}),
        stableJsonStringify(tool.annotations ?? null),
        checkedAt,
        input.expiresAt ?? null,
        now,
        now
      )
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return listOpenAICompatibleMcpToolCacheForServer(input.serverId)
}

export function listOpenAICompatibleMcpToolCacheForServer(
  serverId: string,
  access?: AccessScope
): OpenAICompatibleMcpToolCacheRecord[] {
  const scope = buildSystemAccountScopeClause(access)
  const rows = getBusinessDatabase().prepare(`
    SELECT ${mcpToolCacheColumns()}
    FROM openai_compatible_mcp_tool_cache
    WHERE server_id = ?
      ${scope.clause}
    ORDER BY tool_name ASC, id ASC
  `).all(serverId, ...scope.params) as unknown as OpenAICompatibleMcpToolCacheRow[]
  return rows.map(mcpToolCacheFromRow)
}

export function createOpenAICompatibleMcpServerDiagnostic(
  input: OpenAICompatibleMcpServerDiagnosticInput
): OpenAICompatibleMcpServerDiagnosticRecord {
  const id = newId('mcpdiag')
  const now = nowIso()
  getBusinessDatabase().prepare(`
    INSERT INTO openai_compatible_mcp_server_diagnostics (
      id, server_id, system_account_id, server_label, server_url, status, tool_count,
      error_code, error_message, omission_metadata_json, started_at, finished_at,
      duration_ms, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    id,
    input.serverId,
    input.systemAccountId,
    input.serverLabel,
    input.serverUrl,
    input.status,
    normalizeNonNegativeInteger(input.toolCount),
    normalizeNullableText(input.errorCode, 200) ?? null,
    normalizeNullableText(input.errorMessage, 500) ?? null,
    input.omissionMetadata ? stableJsonStringify(input.omissionMetadata) : null,
    input.startedAt,
    input.finishedAt,
    normalizeNonNegativeInteger(input.durationMs),
    now,
    now
  )
  const record = findLatestOpenAICompatibleMcpServerDiagnostic(input.serverId)
  if (!record || record.id !== id) {
    throw new Error(`MCP server diagnostic ${id} was not readable after insert`)
  }
  return record
}

export function findLatestOpenAICompatibleMcpServerDiagnostic(
  serverId: string,
  access?: AccessScope
): OpenAICompatibleMcpServerDiagnosticRecord | undefined {
  const scope = buildSystemAccountScopeClause(access)
  const row = getBusinessDatabase().prepare(`
    SELECT ${mcpServerDiagnosticColumns()}
    FROM openai_compatible_mcp_server_diagnostics
    WHERE server_id = ?
      ${scope.clause}
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `).get(serverId, ...scope.params) as unknown as OpenAICompatibleMcpServerDiagnosticRow | undefined
  return row ? mcpServerDiagnosticFromRow(row) : undefined
}

function normalizeMcpServerInput(
  input: Partial<OpenAICompatibleMcpServerInput>,
  options: { partial: boolean }
): Required<Omit<OpenAICompatibleMcpServerInput, 'description' | 'timeoutMs' | 'maxRetries' | 'retryDelayMs' | 'maxBodyBytes' | 'maxOutputBytes' | 'authorizationRef'>>
  & Pick<OpenAICompatibleMcpServerInput, 'description' | 'timeoutMs' | 'maxRetries' | 'retryDelayMs' | 'maxBodyBytes' | 'maxOutputBytes' | 'authorizationRef'> {
  const label = normalizeRequiredText(input.label, 'MCP server label', options.partial)
  const serverUrl = normalizeServerUrl(input.serverUrl, options.partial)
  return {
    label,
    serverUrl,
    description: normalizeNullableText(input.description, 500),
    enabled: input.enabled !== false,
    allowedTools: normalizeAllowedTools(input.allowedTools),
    defaultApprovalPolicy: input.defaultApprovalPolicy === 'never' ? 'never' : 'always',
    timeoutMs: normalizeOptionalInteger(input.timeoutMs, 1000, 120000),
    maxRetries: normalizeOptionalInteger(input.maxRetries, 0, 3),
    retryDelayMs: normalizeOptionalInteger(input.retryDelayMs, 0, 5000),
    maxBodyBytes: normalizeOptionalInteger(input.maxBodyBytes, 16 * 1024, 4 * 1024 * 1024),
    maxOutputBytes: normalizeOptionalInteger(input.maxOutputBytes, 4 * 1024, 1024 * 1024),
    allowRequestAuthorization: input.allowRequestAuthorization === true,
    authorizationRef: normalizeNullableText(input.authorizationRef, 500)
  }
}

function mcpServerColumns(): string {
  return [
    'id',
    'system_account_id',
    'label',
    'server_url',
    'description',
    'enabled',
    'allowed_tools_json',
    'default_approval_policy',
    'timeout_ms',
    'max_retries',
    'retry_delay_ms',
    'max_body_bytes',
    'max_output_bytes',
    'allow_request_authorization',
    'authorization_ref',
    'created_at',
    'updated_at'
  ].join(', ')
}

function mcpToolCacheColumns(): string {
  return [
    'id',
    'server_id',
    'system_account_id',
    'server_label',
    'server_url',
    'tool_name',
    'description',
    'input_schema_json',
    'annotations_json',
    'last_checked_at',
    'expires_at',
    'created_at',
    'updated_at'
  ].join(', ')
}

function mcpServerDiagnosticColumns(): string {
  return [
    'id',
    'server_id',
    'system_account_id',
    'server_label',
    'server_url',
    'status',
    'tool_count',
    'error_code',
    'error_message',
    'omission_metadata_json',
    'started_at',
    'finished_at',
    'duration_ms',
    'created_at',
    'updated_at'
  ].join(', ')
}

function mcpServerFromRow(row: OpenAICompatibleMcpServerRow): OpenAICompatibleMcpServerRecord {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    label: row.label,
    serverUrl: row.server_url,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    allowedTools: parseStringList(row.allowed_tools_json),
    defaultApprovalPolicy: row.default_approval_policy === 'never' ? 'never' : 'always',
    timeoutMs: normalizeNullablePositiveInteger(row.timeout_ms),
    maxRetries: normalizeNullableNonNegativeInteger(row.max_retries),
    retryDelayMs: normalizeNullableNonNegativeInteger(row.retry_delay_ms),
    maxBodyBytes: normalizeNullablePositiveInteger(row.max_body_bytes),
    maxOutputBytes: normalizeNullablePositiveInteger(row.max_output_bytes),
    allowRequestAuthorization: row.allow_request_authorization === 1,
    authorizationRef: row.authorization_ref ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function mcpServerRuntimeConfigFromRecord(row: OpenAICompatibleMcpServerRow): McpProxyServerRuntimeConfig {
  const record = mcpServerFromRow(row)
  return {
    label: record.label,
    serverUrl: record.serverUrl,
    enabled: record.enabled,
    allowedTools: record.allowedTools,
    allowRequestAuthorization: record.allowRequestAuthorization,
    timeoutMs: record.timeoutMs,
    maxRetries: record.maxRetries,
    retryDelayMs: record.retryDelayMs,
    maxBodyBytes: record.maxBodyBytes,
    maxOutputBytes: record.maxOutputBytes,
    defaultApprovalPolicy: record.defaultApprovalPolicy
  }
}

function mcpToolCacheFromRow(row: OpenAICompatibleMcpToolCacheRow): OpenAICompatibleMcpToolCacheRecord {
  return {
    id: row.id,
    serverId: row.server_id,
    systemAccountId: row.system_account_id,
    serverLabel: row.server_label,
    serverUrl: row.server_url,
    toolName: row.tool_name,
    description: row.description ?? undefined,
    inputSchema: parseJsonRecord(row.input_schema_json),
    annotations: parseJson(row.annotations_json),
    lastCheckedAt: row.last_checked_at,
    expiresAt: row.expires_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function mcpServerDiagnosticFromRow(row: OpenAICompatibleMcpServerDiagnosticRow): OpenAICompatibleMcpServerDiagnosticRecord {
  return {
    id: row.id,
    serverId: row.server_id,
    systemAccountId: row.system_account_id,
    serverLabel: row.server_label,
    serverUrl: row.server_url,
    status: row.status === 'failed' ? 'failed' : 'succeeded',
    toolCount: normalizeNonNegativeInteger(row.tool_count),
    errorCode: row.error_code ?? undefined,
    errorMessage: row.error_message ?? undefined,
    omissionMetadata: row.omission_metadata_json ? parseJsonRecord(row.omission_metadata_json) : undefined,
    startedAt: row.started_at,
    finishedAt: row.finished_at,
    durationMs: normalizeNonNegativeInteger(row.duration_ms),
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function buildMcpServerFilters(
  access: AccessScope | undefined,
  options: OpenAICompatibleMcpServerListOptions
): { clause: string; params: string[] } {
  const clauses: string[] = []
  const params: string[] = []
  const scope = buildSystemAccountScopeClause(access)
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ AND /, ''))
    params.push(...scope.params)
  }
  const keyword = normalizeOptionalText(options.keyword)
  if (keyword) {
    clauses.push('(label LIKE ? OR server_url LIKE ? OR description LIKE ?)')
    const pattern = `%${keyword}%`
    params.push(pattern, pattern, pattern)
  }
  if (typeof options.enabled === 'boolean') {
    clauses.push('enabled = ?')
    params.push(options.enabled ? '1' : '0')
  }
  return {
    clause: clauses.length ? ` AND ${clauses.join(' AND ')}` : '',
    params
  }
}

function normalizeRequiredText(value: unknown, label: string, partial: boolean): string {
  if ((value === undefined || value === null) && partial) return ''
  if (typeof value !== 'string') {
    throw new Error(`${label}不能为空`)
  }
  const text = value.trim()
  if (!text) {
    throw new Error(`${label}不能为空`)
  }
  if (text.length > 100) {
    throw new Error(`${label}不能超过 100 个字符`)
  }
  if (/\s/.test(text)) {
    throw new Error(`${label}不能包含空格`)
  }
  return text
}

function normalizeServerUrl(value: unknown, partial: boolean): string {
  if ((value === undefined || value === null) && partial) return ''
  if (typeof value !== 'string') {
    throw new Error('MCP server URL 不能为空')
  }
  const text = value.trim()
  try {
    const parsed = new URL(text)
    if (parsed.protocol !== 'https:' && !isAllowedLoopbackHttpMcpUrl(parsed)) {
      throw new Error('MCP server URL 必须是 HTTPS')
    }
  } catch (error) {
    if (error instanceof Error && error.message === 'MCP server URL 必须是 HTTPS') {
      throw error
    }
    throw new Error('MCP server URL 无效')
  }
  return text
}

function isAllowedLoopbackHttpMcpUrl(url: URL): boolean {
  if (url.protocol !== 'http:' || !runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls) return false
  const hostname = url.hostname.toLowerCase()
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]'
}

function normalizeAllowedTools(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value
    .filter((item): item is string => typeof item === 'string')
    .map((item) => item.trim())
    .filter(Boolean)
    .slice(0, 200))]
}

function normalizeNullableText(value: unknown, maxLength: number): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  if (!text) return undefined
  return text.length > maxLength ? text.slice(0, maxLength) : text
}

function normalizeOptionalText(value: string | undefined): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}

function normalizeOptionalInteger(value: unknown, min: number, max: number): number | undefined {
  if (value === undefined || value === null || value === '') return undefined
  if (typeof value !== 'number' || !Number.isFinite(value)) return undefined
  return Math.max(min, Math.min(Math.trunc(value), max))
}

function normalizeNullablePositiveInteger(value: number | null): number | undefined {
  if (!Number.isFinite(value)) return undefined
  return Math.max(1, Math.trunc(value ?? 0))
}

function normalizeNullableNonNegativeInteger(value: number | null): number | undefined {
  if (!Number.isFinite(value)) return undefined
  return Math.max(0, Math.trunc(value ?? 0))
}

function normalizePage(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultMcpServerPage
  return Math.max(1, Math.trunc(value ?? defaultMcpServerPage))
}

function normalizePageSize(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultMcpServerPageSize
  return Math.max(1, Math.min(Math.trunc(value ?? defaultMcpServerPageSize), maxMcpServerPageSize))
}

function normalizeNonNegativeInteger(value: number | undefined): number {
  if (!Number.isFinite(value)) return 0
  return Math.max(0, Math.trunc(value ?? 0))
}

function parseStringList(value: string): string[] {
  try {
    const parsed = JSON.parse(value) as unknown
    return normalizeAllowedTools(parsed)
  } catch {
    return []
  }
}

function parseJson(value: string): unknown {
  try {
    return JSON.parse(value) as unknown
  } catch {
    return null
  }
}

function parseJsonRecord(value: string): Record<string, unknown> {
  const parsed = parseJson(value)
  return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : {}
}

function normalizeToolCacheTools(input: OpenAICompatibleMcpToolCacheInput['tools']): OpenAICompatibleMcpToolCacheInput['tools'] {
  const seen = new Set<string>()
  const output: OpenAICompatibleMcpToolCacheInput['tools'] = []
  for (const item of input) {
    const name = normalizeOptionalText(item.name)
    if (!name || seen.has(name)) continue
    seen.add(name)
    output.push({
      name,
      description: normalizeNullableText(item.description, 1000),
      inputSchema: item.inputSchema && typeof item.inputSchema === 'object' && !Array.isArray(item.inputSchema)
        ? item.inputSchema
        : { type: 'object', properties: {} },
      annotations: item.annotations ?? null
    })
  }
  return output.slice(0, 500)
}

function manageableMcpServerSystemAccountId(access: AccessScope): string {
  return scopedSystemAccountId(access) ?? currentSystemAccountId(access)
}
