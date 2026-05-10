import { createHash } from 'node:crypto'

import { decryptJson, encryptJson } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { loadAccountNameMap, loadGroupNameMap, loadSystemAccountNameMap } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'client_aborted'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error'

export interface AuditLogPayloadInput {
  id?: string
  attemptTempId?: string
  partType: AuditPayloadPartType
  sequenceIndex?: number
  contentType?: string
  contentEncoding?: string
  headers?: Record<string, string | string[]>
  body?: Buffer | string
  createdAt?: string
}

export interface AuditLogAttemptInput {
  id?: string
  tempId?: string
  attemptIndex: number
  accountId?: string
  accountOwnerSystemAccountId?: string
  groupId?: string
  proxyUrl?: string
  providerCode?: string
  upstreamMethod: string
  upstreamUrl: string
  upstreamStatusCode?: number
  success?: boolean
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt?: string
  durationMs?: number
}

export interface AuditLogInput {
  id?: string
  traceId: string
  systemAccountId?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  providerCode?: string
  method: string
  path: string
  queryString?: string
  model?: string
  stream?: boolean
  clientIp?: string
  userAgent?: string
  auditOutcome: AuditOutcome
  success: boolean
  finalStatusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  sampleBucket: number
  sampleReason: string
  captureStatus?: 'complete' | 'dropped' | 'overflow'
  startedAt: string
  endedAt: string
  durationMs?: number
  firstTokenMs?: number
  attempts: AuditLogAttemptInput[]
  payloads: AuditLogPayloadInput[]
  createdAt?: string
}

export interface AuditLogSummary {
  id: string
  traceId: string
  systemAccountId?: string
  systemAccountName?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  providerCode?: string
  method: string
  path: string
  queryString?: string
  model?: string
  stream: boolean
  clientIp?: string
  userAgent?: string
  auditOutcome: AuditOutcome
  success: boolean
  finalStatusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  sampleBucket: number
  sampleReason: string
  attemptCount: number
  payloadCount: number
  payloadBytes: number
  captureStatus: string
  startedAt: string
  endedAt: string
  durationMs?: number
  firstTokenMs?: number
  createdAt: string
}

export interface AuditLogAttemptSummary {
  id: string
  attemptIndex: number
  accountId?: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  groupId?: string
  groupName?: string
  proxyUrl?: string
  providerCode?: string
  upstreamMethod: string
  upstreamUrl: string
  upstreamStatusCode?: number
  success: boolean
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt?: string
  durationMs?: number
}

export interface AuditLogPayloadSummary {
  id: string
  attemptId?: string
  partType: AuditPayloadPartType
  sequenceIndex: number
  contentType?: string
  contentEncoding?: string
  headersSha256?: string
  bodySha256?: string
  sizeBytes: number
  createdAt: string
  hasHeaders: boolean
  hasBody: boolean
}

export interface AuditLogDetail extends AuditLogSummary {
  attempts: AuditLogAttemptSummary[]
  payloads: AuditLogPayloadSummary[]
}

export interface AuditLogPayloadDetail extends AuditLogPayloadSummary {
  headers?: Record<string, string | string[]>
  bodyText?: string
  bodyBase64?: string
}

export interface AuditLogListOptions {
  page?: number
  pageSize?: number
  limit?: number
  traceId?: string
  outcome?: AuditOutcome | 'all'
  statusCode?: number
  path?: string
  model?: string
  systemAccountId?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  clientIp?: string
}

export interface AuditLogListResult {
  items: AuditLogSummary[]
  total: number
  page: number
  pageSize: number
}

type AuditLogRow = Record<string, unknown>
type AuditLogFilterValue = string | number

const auditLogDefaultPageSize = 100
const auditLogMaxPageSize = 100

export function createAuditLogsBatch(inputs: AuditLogInput[]): void {
  if (inputs.length === 0) return

  const database = getDatabase()
  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, system_account_id, api_key_id, group_id, account_id, provider_code, method, path, query_string,
      model, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, payload_bytes, capture_status,
      started_at, ended_at, duration_ms, first_token_ms, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const insertAttempt = database.prepare(`
    INSERT INTO audit_log_attempts (
      id, audit_log_id, attempt_index, account_id, account_owner_system_account_id, group_id, proxy_url, provider_code,
      upstream_method, upstream_url, upstream_status_code, success, error_phase, error_code, error_message,
      started_at, ended_at, duration_ms
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const insertPayload = database.prepare(`
    INSERT INTO audit_log_payloads (
      id, audit_log_id, attempt_id, part_type, sequence_index, content_type, content_encoding, headers_encrypted,
      body_encrypted, body_sha256, size_bytes, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      if (input.apiKeyId && !apiKeyExists(database, input.apiKeyId)) {
        continue
      }
      const id = input.id ?? newId('audit')
      const createdAt = input.createdAt ?? nowIso()
      const payloads = input.payloads.map((payload, index) => normalizePayloadInput(payload, index))
      const payloadBytes = payloads.reduce((sum, payload) => sum + payload.sizeBytes, 0)
      const attemptIds = new Map<string, string>()

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        input.systemAccountId ?? null,
        input.apiKeyId ?? null,
        input.groupId ?? null,
        input.accountId ?? null,
        input.providerCode ?? null,
        input.method,
        input.path,
        input.queryString ?? null,
        input.model ?? null,
        input.stream ? 1 : 0,
        input.clientIp ?? null,
        input.userAgent ?? null,
        input.auditOutcome,
        input.success ? 1 : 0,
        input.finalStatusCode ?? null,
        input.errorPhase ?? null,
        input.errorCode ?? null,
        input.errorMessage ?? null,
        input.sampleBucket,
        input.sampleReason,
        input.attempts.length,
        payloads.length,
        payloadBytes,
        input.captureStatus ?? 'complete',
        input.startedAt,
        input.endedAt,
        input.durationMs ?? null,
        input.firstTokenMs ?? null,
        createdAt
      )
      if (Number(insertLogResult.changes ?? 0) === 0) {
        continue
      }

      for (const attempt of input.attempts) {
        const attemptId = attempt.id ?? newId('audatt')
        if (attempt.tempId) {
          attemptIds.set(attempt.tempId, attemptId)
        }
        insertAttempt.run(
          attemptId,
          id,
          attempt.attemptIndex,
          attempt.accountId ?? null,
          attempt.accountOwnerSystemAccountId ?? null,
          attempt.groupId ?? null,
          attempt.proxyUrl ?? null,
          attempt.providerCode ?? null,
          attempt.upstreamMethod,
          attempt.upstreamUrl,
          attempt.upstreamStatusCode ?? null,
          attempt.success ? 1 : 0,
          attempt.errorPhase ?? null,
          attempt.errorCode ?? null,
          attempt.errorMessage ?? null,
          attempt.startedAt,
          attempt.endedAt ?? null,
          attempt.durationMs ?? null
        )
      }

      for (const payload of payloads) {
        insertPayload.run(
          payload.id ?? newId('audpay'),
          id,
          payload.attemptTempId ? attemptIds.get(payload.attemptTempId) ?? null : null,
          payload.partType,
          payload.sequenceIndex,
          payload.contentType ?? null,
          payload.contentEncoding ?? null,
          payload.headersEncrypted,
          payload.bodyEncrypted,
          payload.bodySha256 ?? null,
          payload.sizeBytes,
          payload.createdAt ?? createdAt
        )
      }
    }

    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

function apiKeyExists(database: ReturnType<typeof getDatabase>, apiKeyId: string): boolean {
  const row = database.prepare('SELECT id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

export function listAuditLogs(options: AuditLogListOptions = {}): AuditLogListResult {
  const filters = buildAuditLogFilters(options)
  const pageSize = normalizeAuditLogPageSize(options.pageSize ?? options.limit)
  const page = normalizeAuditLogPage(options.page)
  const offset = (page - 1) * pageSize
  const database = getDatabase()
  const totalRow = database
    .prepare(`
      SELECT COUNT(*) AS total
      FROM audit_logs al
      ${filters.clause}
    `)
    .get(...filters.params) as AuditLogRow | undefined
  const rows = database
    .prepare(`
      SELECT
        al.*,
        ak.name AS api_key_name,
        g.name AS group_name,
        a.name AS account_name
      FROM audit_logs al
      LEFT JOIN api_keys ak ON ak.id = al.api_key_id
      LEFT JOIN groups g ON g.id = al.group_id
      LEFT JOIN accounts a ON a.id = al.account_id
      ${filters.clause}
      ORDER BY al.created_at DESC, al.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize, offset) as AuditLogRow[]
  const systemAccountNames = loadSystemAccountNameMap()
  return {
    items: rows.map((row) => auditLogSummaryFromRow(row, systemAccountNames)),
    total: Number(totalRow?.total ?? 0),
    page,
    pageSize
  }
}

export function getAuditLogDetail(id: string): AuditLogDetail | undefined {
  const row = getDatabase()
    .prepare(`
      SELECT
        al.*,
        ak.name AS api_key_name,
        g.name AS group_name,
        a.name AS account_name
      FROM audit_logs al
      LEFT JOIN api_keys ak ON ak.id = al.api_key_id
      LEFT JOIN groups g ON g.id = al.group_id
      LEFT JOIN accounts a ON a.id = al.account_id
      WHERE al.id = ?
    `)
    .get(id) as AuditLogRow | undefined
  if (!row) return undefined

  const attemptRows = getDatabase()
    .prepare('SELECT * FROM audit_log_attempts WHERE audit_log_id = ? ORDER BY attempt_index ASC, id ASC')
    .all(id) as AuditLogRow[]
  const payloadRows = getDatabase()
    .prepare('SELECT * FROM audit_log_payloads WHERE audit_log_id = ? ORDER BY sequence_index ASC, id ASC')
    .all(id) as AuditLogRow[]
  const systemAccountNames = loadSystemAccountNameMap()
  const accountNames = loadAccountNameMap(attemptRows.map((attempt) => String(attempt.account_id ?? '')).filter(Boolean))
  const groupNames = loadGroupNameMap(attemptRows.map((attempt) => String(attempt.group_id ?? '')).filter(Boolean))
  return {
    ...auditLogSummaryFromRow(row, systemAccountNames),
    attempts: attemptRows.map((attempt) => auditLogAttemptFromRow(attempt, accountNames, groupNames)),
    payloads: payloadRows.map(auditLogPayloadSummaryFromRow)
  }
}

export function getAuditLogPayload(auditLogId: string, payloadId: string): AuditLogPayloadDetail | undefined {
  const row = getDatabase()
    .prepare('SELECT * FROM audit_log_payloads WHERE audit_log_id = ? AND id = ?')
    .get(auditLogId, payloadId) as AuditLogRow | undefined
  if (!row) return undefined

  const summary = auditLogPayloadSummaryFromRow(row)
  const headers = decryptPayloadHeaders(row)
  const body = typeof row.body_encrypted === 'string'
    ? decryptJson<{ text?: string; base64?: string }>(row.body_encrypted)
    : undefined
  return {
    ...summary,
    headers,
    bodyText: body?.text,
    bodyBase64: body?.base64
  }
}

export function cleanupAuditLogsBefore(cutoffCreatedAt: string, limit?: number): number {
  const database = getDatabase()
  if (!limit) {
    const result = database
      .prepare('DELETE FROM audit_logs WHERE created_at < ?')
      .run(cutoffCreatedAt)
    return Number(result.changes ?? 0)
  }

  const rows = database
    .prepare('SELECT id FROM audit_logs WHERE created_at < ? ORDER BY created_at ASC, id ASC LIMIT ?')
    .all(cutoffCreatedAt, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

function normalizePayloadInput(payload: AuditLogPayloadInput, fallbackIndex: number): AuditLogPayloadInput & {
  sequenceIndex: number
  headersEncrypted: string | null
  bodyEncrypted: string | null
  bodySha256?: string
  sizeBytes: number
} {
  const bodyBuffer = bodyToBuffer(payload.body)
  const bodyJson = bodyBuffer
    ? isUtf8Text(bodyBuffer)
      ? { text: bodyBuffer.toString('utf8') }
      : { base64: bodyBuffer.toString('base64') }
    : undefined
  return {
    ...payload,
    sequenceIndex: payload.sequenceIndex ?? fallbackIndex,
    headersEncrypted: payload.headers ? encryptJson(payload.headers) : null,
    bodyEncrypted: bodyJson ? encryptJson(bodyJson) : null,
    bodySha256: bodyBuffer ? createHash('sha256').update(bodyBuffer).digest('hex') : undefined,
    sizeBytes: bodyBuffer?.byteLength ?? 0
  }
}

function bodyToBuffer(body: Buffer | string | undefined): Buffer | undefined {
  if (body === undefined) return undefined
  return Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
}

function isUtf8Text(buffer: Buffer): boolean {
  return buffer.toString('utf8').includes('\uFFFD') === false
}

function buildAuditLogFilters(options: AuditLogListOptions): { clause: string; params: AuditLogFilterValue[] } {
  const clauses: string[] = []
  const params: AuditLogFilterValue[] = []
  const pushTextFilter = (column: string, value?: string) => {
    const text = value?.trim()
    if (!text) return
    clauses.push(`${column} LIKE ?`)
    params.push(`%${text}%`)
  }

  pushTextFilter('al.trace_id', options.traceId)
  pushTextFilter('al.path', options.path)
  pushTextFilter('al.model', options.model)
  pushTextFilter('al.client_ip', options.clientIp)
  if (options.outcome && options.outcome !== 'all') {
    clauses.push('al.audit_outcome = ?')
    params.push(options.outcome)
  }
  if (isHttpStatusCode(options.statusCode)) {
    clauses.push('al.final_status_code = ?')
    params.push(options.statusCode)
  }
  for (const [column, value] of [
    ['al.system_account_id', options.systemAccountId],
    ['al.api_key_id', options.apiKeyId],
    ['al.group_id', options.groupId],
    ['al.account_id', options.accountId]
  ] as const) {
    if (value?.trim()) {
      clauses.push(`${column} = ?`)
      params.push(value.trim())
    }
  }

  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function normalizeAuditLogPage(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.max(1, value)
    : 1
}

function normalizeAuditLogPageSize(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(auditLogMaxPageSize, Math.max(1, value))
    : auditLogDefaultPageSize
}

function auditLogSummaryFromRow(row: AuditLogRow, systemAccountNames: Map<string, string>): AuditLogSummary {
  const systemAccountId = optionalString(row.system_account_id)
  return {
    id: String(row.id),
    traceId: String(row.trace_id),
    systemAccountId,
    systemAccountName: systemAccountId ? systemAccountNames.get(systemAccountId) : undefined,
    apiKeyId: optionalString(row.api_key_id),
    apiKeyName: optionalString(row.api_key_name),
    groupId: optionalString(row.group_id),
    groupName: optionalString(row.group_name),
    accountId: optionalString(row.account_id),
    accountName: optionalString(row.account_name),
    providerCode: optionalString(row.provider_code),
    method: String(row.method),
    path: String(row.path),
    queryString: optionalString(row.query_string),
    model: optionalString(row.model),
    stream: row.stream === 1,
    clientIp: optionalString(row.client_ip),
    userAgent: optionalString(row.user_agent),
    auditOutcome: String(row.audit_outcome) as AuditOutcome,
    success: row.success === 1,
    finalStatusCode: numberValue(row.final_status_code),
    errorPhase: optionalString(row.error_phase),
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    sampleBucket: Number(row.sample_bucket ?? 0),
    sampleReason: String(row.sample_reason),
    attemptCount: Number(row.attempt_count ?? 0),
    payloadCount: Number(row.payload_count ?? 0),
    payloadBytes: Number(row.payload_bytes ?? 0),
    captureStatus: String(row.capture_status),
    startedAt: String(row.started_at),
    endedAt: String(row.ended_at),
    durationMs: numberValue(row.duration_ms),
    firstTokenMs: numberValue(row.first_token_ms),
    createdAt: String(row.created_at)
  }
}

function auditLogAttemptFromRow(
  row: AuditLogRow,
  accountNames: Map<string, string>,
  groupNames: Map<string, string>
): AuditLogAttemptSummary {
  const accountId = optionalString(row.account_id)
  const groupId = optionalString(row.group_id)
  return {
    id: String(row.id),
    attemptIndex: Number(row.attempt_index ?? 0),
    accountId,
    accountName: accountId ? accountNames.get(accountId) : undefined,
    accountOwnerSystemAccountId: optionalString(row.account_owner_system_account_id),
    groupId,
    groupName: groupId ? groupNames.get(groupId) : undefined,
    proxyUrl: optionalString(row.proxy_url),
    providerCode: optionalString(row.provider_code),
    upstreamMethod: String(row.upstream_method),
    upstreamUrl: String(row.upstream_url),
    upstreamStatusCode: numberValue(row.upstream_status_code),
    success: row.success === 1,
    errorPhase: optionalString(row.error_phase),
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    startedAt: String(row.started_at),
    endedAt: optionalString(row.ended_at),
    durationMs: numberValue(row.duration_ms)
  }
}

function auditLogPayloadSummaryFromRow(row: AuditLogRow): AuditLogPayloadSummary {
  return {
    id: String(row.id),
    attemptId: optionalString(row.attempt_id),
    partType: String(row.part_type) as AuditPayloadPartType,
    sequenceIndex: Number(row.sequence_index ?? 0),
    contentType: optionalString(row.content_type),
    contentEncoding: optionalString(row.content_encoding),
    headersSha256: payloadHeadersSha256(row),
    bodySha256: optionalString(row.body_sha256),
    sizeBytes: Number(row.size_bytes ?? 0),
    createdAt: String(row.created_at),
    hasHeaders: typeof row.headers_encrypted === 'string' && row.headers_encrypted.length > 0,
    hasBody: typeof row.body_encrypted === 'string' && row.body_encrypted.length > 0
  }
}

function payloadHeadersSha256(row: AuditLogRow): string | undefined {
  try {
    const headers = decryptPayloadHeaders(row)
    return headers ? createHash('sha256').update(stableJsonStringify(headers)).digest('hex') : undefined
  } catch {
    return undefined
  }
}

function decryptPayloadHeaders(row: AuditLogRow): Record<string, string | string[]> | undefined {
  return typeof row.headers_encrypted === 'string'
    ? decryptJson<Record<string, string | string[]>>(row.headers_encrypted)
    : undefined
}

function stableJsonStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableJsonStringify).join(',')}]`
  }
  if (value && typeof value === 'object') {
    const object = value as Record<string, unknown>
    return `{${Object.keys(object)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${stableJsonStringify(object[key])}`)
      .join(',')}}`
  }
  return JSON.stringify(value)
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}
