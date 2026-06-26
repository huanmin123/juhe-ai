import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import type { OpenAICompatibleMcpApprovalScope } from './openai-compatible-mcp-approval.repository.js'

export type OpenAICompatibleMcpExecutionStatus = 'succeeded' | 'failed'

export interface OpenAICompatibleMcpExecutionCreateInput {
  scope: OpenAICompatibleMcpApprovalScope
  traceId?: string
  approvalRequestId?: string
  serverLabel: string
  serverUrl: string
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  status: OpenAICompatibleMcpExecutionStatus
  outputDigest?: string
  outputBytes?: number
  outputTruncated?: boolean
  errorCode?: string
  errorMessage?: string
  omissionMetadata?: Record<string, unknown>
  startedAt: string
  finishedAt: string
  durationMs: number
}

export interface OpenAICompatibleMcpExecutionRecord {
  id: string
  systemAccountId: string
  apiKeyId: string
  groupId: string
  traceId?: string
  approvalRequestId?: string
  serverLabel: string
  serverUrl: string
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  status: OpenAICompatibleMcpExecutionStatus
  outputDigest?: string
  outputBytes: number
  outputTruncated: boolean
  errorCode?: string
  errorMessage?: string
  omissionMetadata?: Record<string, unknown>
  startedAt: string
  finishedAt: string
  durationMs: number
  createdAt: string
  updatedAt: string
}

export interface OpenAICompatibleMcpExecutionListOptions {
  scope: OpenAICompatibleMcpApprovalScope
  limit?: number
}

export interface OpenAICompatibleMcpExecutionPageOptions {
  page?: number
  pageSize?: number
  apiKeyId?: string
  groupId?: string
  traceId?: string
  approvalRequestId?: string
  serverLabel?: string
  toolName?: string
  status?: OpenAICompatibleMcpExecutionStatus
  startAt?: string
  endAt?: string
}

export interface OpenAICompatibleMcpExecutionListResult {
  items: OpenAICompatibleMcpExecutionRecord[]
  total: number
  page: number
  pageSize: number
}

interface OpenAICompatibleMcpExecutionRow {
  id: string
  system_account_id: string
  api_key_id: string
  group_id: string
  trace_id: string | null
  approval_request_id: string | null
  server_label: string
  server_url: string
  tool_name: string
  arguments_digest: string
  arguments_preview: string
  status: string
  output_digest: string | null
  output_bytes: number
  output_truncated: number
  error_code: string | null
  error_message: string | null
  omission_metadata_json: string | null
  started_at: string
  finished_at: string
  duration_ms: number
  created_at: string
  updated_at: string
}

const defaultMcpExecutionListLimit = 20
const maxMcpExecutionListLimit = 100
const defaultMcpExecutionPage = 1
const defaultMcpExecutionPageSize = 20
const maxMcpExecutionPageSize = 100

export function createOpenAICompatibleMcpExecutionRecord(
  input: OpenAICompatibleMcpExecutionCreateInput
): OpenAICompatibleMcpExecutionRecord {
  const now = nowIso()
  const id = newId('mcpexec')
  getBusinessDatabase().prepare(`
    INSERT INTO openai_compatible_mcp_execution_records (
      id, system_account_id, api_key_id, group_id, trace_id, approval_request_id,
      server_label, server_url, tool_name, arguments_digest, arguments_preview,
      status, output_digest, output_bytes, output_truncated, error_code, error_message,
      omission_metadata_json, started_at, finished_at, duration_ms, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    id,
    input.scope.systemAccountId,
    input.scope.apiKeyId,
    input.scope.groupId,
    input.traceId ?? null,
    input.approvalRequestId ?? null,
    input.serverLabel,
    input.serverUrl,
    input.toolName,
    input.argumentsDigest,
    input.argumentsPreview,
    input.status,
    input.outputDigest ?? null,
    normalizeNonNegativeInteger(input.outputBytes),
    input.outputTruncated ? 1 : 0,
    input.errorCode ?? null,
    input.errorMessage ? truncateText(input.errorMessage, 500) : null,
    input.omissionMetadata ? JSON.stringify(input.omissionMetadata) : null,
    input.startedAt,
    input.finishedAt,
    normalizeNonNegativeInteger(input.durationMs),
    now,
    now
  )
  const record = findOpenAICompatibleMcpExecutionRecord(id)
  if (!record) {
    throw new Error(`MCP execution record ${id} was not readable after insert`)
  }
  return record
}

export function findOpenAICompatibleMcpExecutionRecord(id: string): OpenAICompatibleMcpExecutionRecord | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT ${openAICompatibleMcpExecutionColumns()}
    FROM openai_compatible_mcp_execution_records
    WHERE id = ?
    LIMIT 1
  `).get(id) as unknown as OpenAICompatibleMcpExecutionRow | undefined
  return row ? openAICompatibleMcpExecutionFromRow(row) : undefined
}

export function findOpenAICompatibleMcpExecutionRecordForAccess(
  id: string,
  access?: AccessScope
): OpenAICompatibleMcpExecutionRecord | undefined {
  const filter = buildOpenAICompatibleMcpExecutionAccessFilters(access, {})
  const row = getBusinessDatabase().prepare(`
    SELECT ${openAICompatibleMcpExecutionColumns()}
    FROM openai_compatible_mcp_execution_records
    WHERE id = ?
      ${filter.clause}
    LIMIT 1
  `).get(id, ...filter.params) as unknown as OpenAICompatibleMcpExecutionRow | undefined
  return row ? openAICompatibleMcpExecutionFromRow(row) : undefined
}

export function listOpenAICompatibleMcpExecutionRecordsPage(
  access?: AccessScope,
  options: OpenAICompatibleMcpExecutionPageOptions = {}
): OpenAICompatibleMcpExecutionListResult {
  const page = normalizePage(options.page)
  const pageSize = normalizePageSize(options.pageSize)
  const offset = (page - 1) * pageSize
  const filter = buildOpenAICompatibleMcpExecutionAccessFilters(access, options)
  const database = getBusinessDatabase()
  const totalRow = database.prepare(`
    SELECT COUNT(*) AS count
    FROM openai_compatible_mcp_execution_records
    WHERE 1 = 1
      ${filter.clause}
  `).get(...filter.params) as { count?: number } | undefined
  const rows = database.prepare(`
    SELECT ${openAICompatibleMcpExecutionColumns()}
    FROM openai_compatible_mcp_execution_records
    WHERE 1 = 1
      ${filter.clause}
    ORDER BY created_at DESC, id DESC
    LIMIT ? OFFSET ?
  `).all(...filter.params, pageSize, offset) as unknown as OpenAICompatibleMcpExecutionRow[]
  return {
    items: rows.map(openAICompatibleMcpExecutionFromRow),
    total: normalizeNonNegativeInteger(totalRow?.count),
    page,
    pageSize
  }
}

export function listOpenAICompatibleMcpExecutionRecords(
  options: OpenAICompatibleMcpExecutionListOptions
): OpenAICompatibleMcpExecutionRecord[] {
  const limit = normalizeListLimit(options.limit)
  const rows = getBusinessDatabase().prepare(`
    SELECT ${openAICompatibleMcpExecutionColumns()}
    FROM openai_compatible_mcp_execution_records
    WHERE system_account_id = ?
      AND api_key_id = ?
      AND group_id = ?
    ORDER BY created_at DESC, id DESC
    LIMIT ?
  `).all(options.scope.systemAccountId, options.scope.apiKeyId, options.scope.groupId, limit) as unknown as OpenAICompatibleMcpExecutionRow[]
  return rows.map(openAICompatibleMcpExecutionFromRow)
}

function openAICompatibleMcpExecutionColumns(): string {
  return [
    'id',
    'system_account_id',
    'api_key_id',
    'group_id',
    'trace_id',
    'approval_request_id',
    'server_label',
    'server_url',
    'tool_name',
    'arguments_digest',
    'arguments_preview',
    'status',
    'output_digest',
    'output_bytes',
    'output_truncated',
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

function openAICompatibleMcpExecutionFromRow(row: OpenAICompatibleMcpExecutionRow): OpenAICompatibleMcpExecutionRecord {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    apiKeyId: row.api_key_id,
    groupId: row.group_id,
    traceId: row.trace_id ?? undefined,
    approvalRequestId: row.approval_request_id ?? undefined,
    serverLabel: row.server_label,
    serverUrl: row.server_url,
    toolName: row.tool_name,
    argumentsDigest: row.arguments_digest,
    argumentsPreview: row.arguments_preview,
    status: row.status === 'failed' ? 'failed' : 'succeeded',
    outputDigest: row.output_digest ?? undefined,
    outputBytes: normalizeNonNegativeInteger(row.output_bytes),
    outputTruncated: row.output_truncated === 1,
    errorCode: row.error_code ?? undefined,
    errorMessage: row.error_message ?? undefined,
    omissionMetadata: parseJsonObject(row.omission_metadata_json),
    startedAt: row.started_at,
    finishedAt: row.finished_at,
    durationMs: normalizeNonNegativeInteger(row.duration_ms),
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function parseJsonObject(value: string | null): Record<string, unknown> | undefined {
  if (!value) return undefined
  try {
    const parsed = JSON.parse(value) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : undefined
  } catch {
    return undefined
  }
}

function buildOpenAICompatibleMcpExecutionAccessFilters(
  access: AccessScope | undefined,
  options: OpenAICompatibleMcpExecutionPageOptions
): { clause: string; params: Array<string> } {
  const clauses: string[] = []
  const params: string[] = []
  const systemScope = buildSystemAccountScopeClause(access)
  if (systemScope.clause) {
    clauses.push(systemScope.clause.replace(/^ AND /, ''))
    params.push(...systemScope.params)
  }
  addOptionalExactFilter(clauses, params, 'api_key_id', options.apiKeyId)
  addOptionalExactFilter(clauses, params, 'group_id', options.groupId)
  addOptionalExactFilter(clauses, params, 'trace_id', options.traceId)
  addOptionalExactFilter(clauses, params, 'approval_request_id', options.approvalRequestId)
  addOptionalExactFilter(clauses, params, 'server_label', options.serverLabel)
  addOptionalExactFilter(clauses, params, 'tool_name', options.toolName)
  addOptionalExactFilter(clauses, params, 'status', options.status)
  const startAt = normalizeOptionalText(options.startAt)
  if (startAt) {
    clauses.push('created_at >= ?')
    params.push(startAt)
  }
  const endAt = normalizeOptionalText(options.endAt)
  if (endAt) {
    clauses.push('created_at < ?')
    params.push(endAt)
  }
  return {
    clause: clauses.length ? ` AND ${clauses.join(' AND ')}` : '',
    params
  }
}

function addOptionalExactFilter(clauses: string[], params: string[], column: string, value: string | undefined): void {
  const text = normalizeOptionalText(value)
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function normalizeOptionalText(value: string | undefined): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}

function normalizePage(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultMcpExecutionPage
  return Math.max(1, Math.trunc(value ?? defaultMcpExecutionPage))
}

function normalizePageSize(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultMcpExecutionPageSize
  return Math.max(1, Math.min(Math.trunc(value ?? defaultMcpExecutionPageSize), maxMcpExecutionPageSize))
}

function normalizeListLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultMcpExecutionListLimit
  return Math.max(1, Math.min(Math.trunc(value ?? defaultMcpExecutionListLimit), maxMcpExecutionListLimit))
}

function normalizeNonNegativeInteger(value: number | undefined): number {
  if (!Number.isFinite(value)) return 0
  return Math.max(0, Math.trunc(value ?? 0))
}

function truncateText(value: string, maxLength: number): string {
  return value.length > maxLength ? `${value.slice(0, maxLength)}...` : value
}
