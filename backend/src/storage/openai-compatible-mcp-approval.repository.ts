import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'

export type OpenAICompatibleMcpApprovalStatus = 'pending' | 'approved' | 'rejected' | 'expired' | 'consumed'

export interface OpenAICompatibleMcpApprovalScope {
  systemAccountId: string
  apiKeyId: string
  groupId: string
}

export interface OpenAICompatibleMcpApprovalCreateInput {
  scope: OpenAICompatibleMcpApprovalScope
  serverLabel: string
  serverUrl: string
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  traceId?: string
  ttlSeconds: number
}

export interface OpenAICompatibleMcpApprovalResolveInput {
  approvalRequestId: string
  scope: OpenAICompatibleMcpApprovalScope
  serverLabel: string
  serverUrl: string
  toolName: string
  argumentsDigest: string
  approved: boolean
  rejectReason?: string
}

export interface OpenAICompatibleMcpApprovalRecord {
  id: string
  systemAccountId: string
  apiKeyId: string
  groupId: string
  serverLabel: string
  serverUrl: string
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  status: OpenAICompatibleMcpApprovalStatus
  traceId?: string
  createdAt: string
  updatedAt: string
  expiresAt: string
  approvedAt?: string
  rejectedAt?: string
  consumedAt?: string
  rejectReason?: string
}

export interface OpenAICompatibleMcpApprovalPageOptions {
  page?: number
  pageSize?: number
  apiKeyId?: string
  groupId?: string
  traceId?: string
  serverLabel?: string
  toolName?: string
  status?: OpenAICompatibleMcpApprovalStatus
  startAt?: string
  endAt?: string
}

export interface OpenAICompatibleMcpApprovalListResult {
  items: OpenAICompatibleMcpApprovalRecord[]
  total: number
  page: number
  pageSize: number
}

export type OpenAICompatibleMcpApprovalResolveResult =
  | { ok: true; approved: true; record: OpenAICompatibleMcpApprovalRecord }
  | { ok: true; approved: false; record: OpenAICompatibleMcpApprovalRecord }
  | {
      ok: false
      reason: 'not_found' | 'scope_mismatch' | 'target_mismatch' | 'arguments_mismatch' | 'expired' | 'not_pending'
      record?: OpenAICompatibleMcpApprovalRecord
    }

interface OpenAICompatibleMcpApprovalRow {
  id: string
  system_account_id: string
  api_key_id: string
  group_id: string
  server_label: string
  server_url: string
  tool_name: string
  arguments_digest: string
  arguments_preview: string
  status: string
  trace_id: string | null
  created_at: string
  updated_at: string
  expires_at: string
  approved_at: string | null
  rejected_at: string | null
  consumed_at: string | null
  reject_reason: string | null
}

const defaultMcpApprovalPage = 1
const defaultMcpApprovalPageSize = 20
const maxMcpApprovalPageSize = 100

export function createOpenAICompatibleMcpApprovalRequest(
  input: OpenAICompatibleMcpApprovalCreateInput
): OpenAICompatibleMcpApprovalRecord {
  const now = nowIso()
  const expiresAt = new Date(Date.parse(now) + normalizeTtlSeconds(input.ttlSeconds) * 1000).toISOString()
  const id = newId('mcpr')
  getBusinessDatabase().prepare(`
    INSERT INTO openai_compatible_mcp_approval_requests (
      id, system_account_id, api_key_id, group_id, server_label, server_url,
      tool_name, arguments_digest, arguments_preview, status, trace_id,
      created_at, updated_at, expires_at, approved_at, rejected_at, consumed_at, reject_reason
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, NULL, NULL, NULL, NULL)
  `).run(
    id,
    input.scope.systemAccountId,
    input.scope.apiKeyId,
    input.scope.groupId,
    input.serverLabel,
    input.serverUrl,
    input.toolName,
    input.argumentsDigest,
    input.argumentsPreview,
    input.traceId ?? null,
    now,
    now,
    expiresAt
  )
  const record = findOpenAICompatibleMcpApprovalRequest(id)
  if (!record) {
    throw new Error(`MCP approval request ${id} was not readable after insert`)
  }
  return record
}

export function findOpenAICompatibleMcpApprovalRequest(id: string): OpenAICompatibleMcpApprovalRecord | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT ${openAICompatibleMcpApprovalColumns()}
    FROM openai_compatible_mcp_approval_requests
    WHERE id = ?
    LIMIT 1
  `).get(id) as unknown as OpenAICompatibleMcpApprovalRow | undefined
  return row ? openAICompatibleMcpApprovalFromRow(row) : undefined
}

export function findOpenAICompatibleMcpApprovalRequestForAccess(
  id: string,
  access?: AccessScope
): OpenAICompatibleMcpApprovalRecord | undefined {
  const filter = buildOpenAICompatibleMcpApprovalAccessFilters(access, {})
  const row = getBusinessDatabase().prepare(`
    SELECT ${openAICompatibleMcpApprovalColumns()}
    FROM openai_compatible_mcp_approval_requests
    WHERE id = ?
      ${filter.clause}
    LIMIT 1
  `).get(id, ...filter.params) as unknown as OpenAICompatibleMcpApprovalRow | undefined
  return row ? openAICompatibleMcpApprovalFromRow(row) : undefined
}

export function listOpenAICompatibleMcpApprovalRequestsPage(
  access?: AccessScope,
  options: OpenAICompatibleMcpApprovalPageOptions = {}
): OpenAICompatibleMcpApprovalListResult {
  const page = normalizePage(options.page)
  const pageSize = normalizePageSize(options.pageSize)
  const offset = (page - 1) * pageSize
  const filter = buildOpenAICompatibleMcpApprovalAccessFilters(access, options)
  const database = getBusinessDatabase()
  const totalRow = database.prepare(`
    SELECT COUNT(*) AS count
    FROM openai_compatible_mcp_approval_requests
    WHERE 1 = 1
      ${filter.clause}
  `).get(...filter.params) as { count?: number } | undefined
  const rows = database.prepare(`
    SELECT ${openAICompatibleMcpApprovalColumns()}
    FROM openai_compatible_mcp_approval_requests
    WHERE 1 = 1
      ${filter.clause}
    ORDER BY created_at DESC, id DESC
    LIMIT ? OFFSET ?
  `).all(...filter.params, pageSize, offset) as unknown as OpenAICompatibleMcpApprovalRow[]
  return {
    items: rows.map(openAICompatibleMcpApprovalFromRow),
    total: normalizeNonNegativeInteger(totalRow?.count),
    page,
    pageSize
  }
}

export function approveOpenAICompatibleMcpApprovalRequestForAccess(
  id: string,
  access?: AccessScope
): OpenAICompatibleMcpApprovalRecord | undefined {
  const filter = buildOpenAICompatibleMcpApprovalAccessFilters(access, {})
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_mcp_approval_requests
    SET status = 'approved',
        approved_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'pending'
      AND expires_at > ?
      ${filter.clause}
  `).run(now, now, id, now, ...filter.params)
  return findOpenAICompatibleMcpApprovalRequestForAccess(id, access)
}

export function rejectOpenAICompatibleMcpApprovalRequestForAccess(
  id: string,
  access?: AccessScope,
  rejectReason?: string
): OpenAICompatibleMcpApprovalRecord | undefined {
  const filter = buildOpenAICompatibleMcpApprovalAccessFilters(access, {})
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_mcp_approval_requests
    SET status = 'rejected',
        rejected_at = ?,
        reject_reason = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'pending'
      AND expires_at > ?
      ${filter.clause}
  `).run(now, normalizeRejectReason(rejectReason) ?? null, now, id, now, ...filter.params)
  return findOpenAICompatibleMcpApprovalRequestForAccess(id, access)
}

export function resolveOpenAICompatibleMcpApprovalResponse(
  input: OpenAICompatibleMcpApprovalResolveInput
): OpenAICompatibleMcpApprovalResolveResult {
  const record = findOpenAICompatibleMcpApprovalRequest(input.approvalRequestId)
  if (!record) return { ok: false, reason: 'not_found' }
  if (!sameApprovalScope(record, input.scope)) {
    return { ok: false, reason: 'scope_mismatch', record }
  }
  if (record.serverLabel !== input.serverLabel || record.serverUrl !== input.serverUrl || record.toolName !== input.toolName) {
    return { ok: false, reason: 'target_mismatch', record }
  }
  if (record.argumentsDigest !== input.argumentsDigest) {
    return { ok: false, reason: 'arguments_mismatch', record }
  }
  if (record.status === 'expired' || approvalExpired(record)) {
    const expired = markOpenAICompatibleMcpApprovalExpired(record.id) ?? record
    return { ok: false, reason: 'expired', record: expired }
  }
  if (input.approved && record.status === 'approved') {
    return { ok: true, approved: true, record }
  }
  if (record.status !== 'pending') {
    return { ok: false, reason: 'not_pending', record }
  }
  if (!input.approved) {
    const rejected = markOpenAICompatibleMcpApprovalRejected(record.id, input.rejectReason) ?? record
    return { ok: true, approved: false, record: rejected }
  }
  const approved = markOpenAICompatibleMcpApprovalApproved(record.id) ?? record
  return { ok: true, approved: true, record: approved }
}

export function consumeOpenAICompatibleMcpApprovalRequest(id: string): OpenAICompatibleMcpApprovalRecord | undefined {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_mcp_approval_requests
    SET status = 'consumed',
        consumed_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'approved'
  `).run(now, now, id)
  return findOpenAICompatibleMcpApprovalRequest(id)
}

export function markOpenAICompatibleMcpApprovalExpired(id: string): OpenAICompatibleMcpApprovalRecord | undefined {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_mcp_approval_requests
    SET status = 'expired',
        updated_at = ?
    WHERE id = ?
      AND status IN ('pending', 'expired')
  `).run(now, id)
  return findOpenAICompatibleMcpApprovalRequest(id)
}

function markOpenAICompatibleMcpApprovalApproved(id: string): OpenAICompatibleMcpApprovalRecord | undefined {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_mcp_approval_requests
    SET status = 'approved',
        approved_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'pending'
  `).run(now, now, id)
  return findOpenAICompatibleMcpApprovalRequest(id)
}

function markOpenAICompatibleMcpApprovalRejected(
  id: string,
  rejectReason: string | undefined
): OpenAICompatibleMcpApprovalRecord | undefined {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_mcp_approval_requests
    SET status = 'rejected',
        rejected_at = ?,
        reject_reason = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'pending'
  `).run(now, rejectReason ?? null, now, id)
  return findOpenAICompatibleMcpApprovalRequest(id)
}

function sameApprovalScope(
  record: OpenAICompatibleMcpApprovalRecord,
  scope: OpenAICompatibleMcpApprovalScope
): boolean {
  return record.systemAccountId === scope.systemAccountId
    && record.apiKeyId === scope.apiKeyId
    && record.groupId === scope.groupId
}

function approvalExpired(record: OpenAICompatibleMcpApprovalRecord): boolean {
  const expiresAt = Date.parse(record.expiresAt)
  return Number.isFinite(expiresAt) && expiresAt <= Date.now()
}

function openAICompatibleMcpApprovalColumns(): string {
  return [
    'id',
    'system_account_id',
    'api_key_id',
    'group_id',
    'server_label',
    'server_url',
    'tool_name',
    'arguments_digest',
    'arguments_preview',
    'status',
    'trace_id',
    'created_at',
    'updated_at',
    'expires_at',
    'approved_at',
    'rejected_at',
    'consumed_at',
    'reject_reason'
  ].join(', ')
}

function openAICompatibleMcpApprovalFromRow(row: OpenAICompatibleMcpApprovalRow): OpenAICompatibleMcpApprovalRecord {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    apiKeyId: row.api_key_id,
    groupId: row.group_id,
    serverLabel: row.server_label,
    serverUrl: row.server_url,
    toolName: row.tool_name,
    argumentsDigest: row.arguments_digest,
    argumentsPreview: row.arguments_preview,
    status: normalizeStatus(row.status),
    traceId: row.trace_id ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    expiresAt: row.expires_at,
    approvedAt: row.approved_at ?? undefined,
    rejectedAt: row.rejected_at ?? undefined,
    consumedAt: row.consumed_at ?? undefined,
    rejectReason: row.reject_reason ?? undefined
  }
}

function normalizeStatus(value: string): OpenAICompatibleMcpApprovalStatus {
  if (value === 'approved' || value === 'rejected' || value === 'expired' || value === 'consumed') return value
  return 'pending'
}

function buildOpenAICompatibleMcpApprovalAccessFilters(
  access: AccessScope | undefined,
  options: OpenAICompatibleMcpApprovalPageOptions
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
  if (!Number.isFinite(value)) return defaultMcpApprovalPage
  return Math.max(1, Math.trunc(value ?? defaultMcpApprovalPage))
}

function normalizePageSize(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultMcpApprovalPageSize
  return Math.max(1, Math.min(Math.trunc(value ?? defaultMcpApprovalPageSize), maxMcpApprovalPageSize))
}

function normalizeNonNegativeInteger(value: number | undefined): number {
  if (!Number.isFinite(value)) return 0
  return Math.max(0, Math.trunc(value ?? 0))
}

function normalizeRejectReason(value: string | undefined): string | undefined {
  const text = normalizeOptionalText(value)
  if (!text) return undefined
  return text.length > 500 ? `${text.slice(0, 500)}...` : text
}

function normalizeTtlSeconds(value: number): number {
  if (!Number.isFinite(value)) return 300
  return Math.max(1, Math.min(Math.trunc(value), 86_400))
}
