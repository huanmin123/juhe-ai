import type { NextFunction, Request, Response } from 'express'

import { errorLogFields, logger } from '../../shared/logger.js'
import { getRequestContext, getTraceId, sanitizeUrlForLog } from '../../shared/request-context.js'
import type { PublicApiLogCaptureStatus, PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import type { ExternalIntegrationSourceAuthContext } from '../../storage/external-integration-source-types.js'
import { enqueuePublicApiLog } from './public-api-log-queue.service.js'

type ResponsePayload = string | Buffer | Record<string, unknown> | unknown[] | undefined

interface CapturedSnapshot {
  data: Record<string, unknown>
  status: PublicApiLogCaptureStatus
  sizeBytes: number
}

const publicApiSnapshotMaxBytes = 32 * 1024
const publicApiSnapshotMaxDepth = 8
const publicApiSnapshotMaxEntries = 200
const publicApiSnapshotStringPreviewBytes = 4096

export function capturePublicApiLog(req: Request, res: Response, next: NextFunction): void {
  const startedAt = new Date()
  const startedMs = Date.now()
  const traceId = getTraceId()
  const clientIp = getRequestContext()?.clientIp
  let responsePayload: ResponsePayload
  let responseSizeBytes = 0
  let responseCaptured = false
  let recorded = false
  const socket = req.socket

  const recordClosedPublicApiLog = () => {
    if (recorded) return
    recorded = true
    req.off('aborted', recordClosedPublicApiLog)
    req.off('close', recordIncompleteRequestClose)
    socket.off('close', recordClosedPublicApiLog)
    try {
      enqueuePublicApiLog(buildPublicApiLogInput(req, res, {
        startedAt,
        durationMs: Date.now() - startedMs,
        responsePayload,
        responseSizeBytes,
        closed: true,
        traceId,
        clientIp
      }))
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'public_api_log_capture_failed',
        method: req.method,
        path: `${req.baseUrl}${req.path}`,
        statusCode: 499
      }), '公开接口日志采集失败')
    }
  }

  const recordIncompleteRequestClose = () => {
    if (!req.complete) {
      recordClosedPublicApiLog()
    }
  }

  const originalJson = res.json.bind(res)
  res.json = ((body?: unknown) => {
    if (!responseCaptured) {
      responsePayload = body as ResponsePayload
      responseSizeBytes = estimatePayloadSizeBytes(body)
      responseCaptured = true
    }
    return originalJson(body)
  }) as Response['json']

  const originalSend = res.send.bind(res)
  res.send = ((body?: unknown) => {
    if (!responseCaptured) {
      responsePayload = normalizeSendPayload(body)
      responseSizeBytes = estimatePayloadSizeBytes(body)
      responseCaptured = true
    }
    return originalSend(body)
  }) as Response['send']

  res.once('finish', () => {
    if (recorded) return
    recorded = true
    req.off('aborted', recordClosedPublicApiLog)
    req.off('close', recordIncompleteRequestClose)
    socket.off('close', recordClosedPublicApiLog)
    try {
      enqueuePublicApiLog(buildPublicApiLogInput(req, res, {
        startedAt,
        durationMs: Date.now() - startedMs,
        responsePayload,
        responseSizeBytes,
        closed: false,
        traceId,
        clientIp
      }))
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'public_api_log_capture_failed',
        method: req.method,
        path: `${req.baseUrl}${req.path}`,
        statusCode: res.statusCode
      }), '公开接口日志采集失败')
    }
  })

  res.once('close', recordClosedPublicApiLog)
  req.once('aborted', recordClosedPublicApiLog)
  req.once('close', recordIncompleteRequestClose)
  socket.once('close', recordClosedPublicApiLog)

  next()
}

function buildPublicApiLogInput(
  req: Request,
  res: Response,
  input: {
    startedAt: Date
    durationMs: number
    responsePayload: ResponsePayload
    responseSizeBytes: number
    closed: boolean
    traceId?: string
    clientIp?: string
  }
): PublicApiLogInput {
  const endedAt = new Date()
  const sanitizedUrl = sanitizeUrlForLog(req.originalUrl)
  const [path, ...queryParts] = sanitizedUrl.split('?')
  const queryString = queryParts.length ? queryParts.join('?') : undefined
  const sourceContext = publicApiSourceContext(res)
  const statusCode = input.closed ? 499 : res.statusCode
  const requestSnapshot = buildRequestSnapshot(req, res, statusCode)
  const responseSnapshot = buildResponseSnapshot(input.responsePayload, statusCode)
  const errorInfo = input.closed
    ? { errorCode: 'public_api_client_closed', errorMessage: '客户端连接提前关闭' }
    : extractPublicApiErrorInfo(input.responsePayload, statusCode)

  return {
    traceId: input.traceId,
    sourceRefId: sourceContext?.sourceRefId,
    sourceName: sourceContext?.sourceName,
    tokenId: sourceContext?.tokenId,
    tokenName: sourceContext?.tokenName,
    tokenPrefix: sourceContext?.tokenPrefix,
    isTestToken: sourceContext?.isTestToken,
    method: req.method.toUpperCase(),
    path: path || `${req.baseUrl}${req.path}`,
    queryString,
    clientIp: input.clientIp,
    userAgent: req.header('user-agent'),
    statusCode,
    success: !input.closed && statusCode >= 200 && statusCode < 400,
    durationMs: input.durationMs,
    requestSizeBytes: requestSnapshot.sizeBytes,
    responseSizeBytes: input.responseSizeBytes || responseSnapshot.sizeBytes,
    requestCaptureStatus: requestSnapshot.status,
    responseCaptureStatus: responseSnapshot.status,
    requestData: requestSnapshot.data,
    responseData: responseSnapshot.data,
    errorCode: errorInfo.errorCode,
    errorMessage: errorInfo.errorMessage,
    startedAt: input.startedAt.toISOString(),
    endedAt: endedAt.toISOString(),
    createdAt: endedAt.toISOString()
  }
}

function buildRequestSnapshot(req: Request, res: Response, statusCode: number): CapturedSnapshot {
  const bodyRejectedReason = requestBodyRejectedReason(req, res, statusCode)
  const contentType = req.header('content-type')
  const contentLength = req.header('content-length')
  const query = req.query
  const body = bodyRejectedReason
    ? {
        dropped: true,
        reason: bodyRejectedReason
      }
    : req.body
  const headers = {
    contentType,
    contentLength
  }
  const data = {
    method: req.method.toUpperCase(),
    path: `${req.baseUrl}${req.path}`,
    query,
    body,
    headers
  }
  const bodySizeBytes = contentLengthBytes(req) ?? estimatePayloadSizeBytes(req.body)
  const querySizeBytes = req.originalUrl.includes('?') ? Buffer.byteLength(req.originalUrl.split('?').slice(1).join('?'), 'utf8') : 0
  const snapshot = boundedSnapshot(data, bodySizeBytes + querySizeBytes)
  return bodyRejectedReason ? { ...snapshot, status: 'dropped' } : snapshot
}

function buildResponseSnapshot(payload: ResponsePayload, statusCode: number): CapturedSnapshot {
  const data = {
    statusCode,
    body: payload
  }
  return boundedSnapshot(data, estimatePayloadSizeBytes(payload))
}

function boundedSnapshot(data: Record<string, unknown>, sizeBytes: number): CapturedSnapshot {
  const sanitizedSizeBytes = Math.max(0, Math.trunc(sizeBytes))
  if (isSnapshotEmpty(data)) {
    return {
      data,
      status: 'empty',
      sizeBytes: sanitizedSizeBytes
    }
  }
  const bounded = boundedSnapshotValue(data, publicApiSnapshotMaxBytes)
  const json = safeJsonStringify(bounded.value)
  const jsonSizeBytes = Buffer.byteLength(json, 'utf8')
  if (!bounded.truncated && jsonSizeBytes <= publicApiSnapshotMaxBytes) {
    return {
      data: bounded.value as Record<string, unknown>,
      status: 'complete',
      sizeBytes: sanitizedSizeBytes || jsonSizeBytes
    }
  }
  return {
    data: {
      truncated: true,
      originalJsonSizeBytes: sanitizedSizeBytes || Math.max(jsonSizeBytes, publicApiSnapshotMaxBytes + 1),
      preview: sliceUtf8(json, publicApiSnapshotMaxBytes)
    },
    status: 'truncated',
    sizeBytes: sanitizedSizeBytes || Math.max(jsonSizeBytes, publicApiSnapshotMaxBytes + 1)
  }
}

function isSnapshotEmpty(data: Record<string, unknown>): boolean {
  const body = data.body
  const query = data.query
  if (body !== undefined && body !== null && !(typeof body === 'object' && !Array.isArray(body) && !hasOwnEnumerableKey(body))) {
    return false
  }
  if (query && typeof query === 'object' && !Array.isArray(query) && hasOwnEnumerableKey(query)) {
    return false
  }
  return body === undefined || body === null
}

function hasOwnEnumerableKey(value: object): boolean {
  for (const key in value) {
    if (Object.prototype.hasOwnProperty.call(value, key)) return true
  }
  return false
}

function extractPublicApiErrorInfo(payload: ResponsePayload, statusCode: number): { errorCode?: string; errorMessage?: string } {
  if (statusCode < 400) return {}
  if (payload && typeof payload === 'object' && !Array.isArray(payload)) {
    const record = payload as Record<string, unknown>
    const nestedError = record.error && typeof record.error === 'object' && !Array.isArray(record.error)
      ? record.error as Record<string, unknown>
      : undefined
    return {
      errorCode: firstString(record.code, record.type, nestedError?.code, nestedError?.type),
      errorMessage: firstString(record.message, nestedError?.message, record.error)
    }
  }
  if (typeof payload === 'string') {
    return {
      errorMessage: payload.slice(0, 1000)
    }
  }
  return {
    errorMessage: statusCode >= 500 ? '服务器内部错误' : `请求失败：HTTP ${statusCode}`
  }
}

function publicApiSourceContext(res: Response): ExternalIntegrationSourceAuthContext | undefined {
  return res.locals.externalIntegrationSource as ExternalIntegrationSourceAuthContext | undefined
}

function contentLengthBytes(req: Request): number | undefined {
  const value = req.header('content-length')
  if (!value) return undefined
  const size = Number(value)
  return Number.isFinite(size) && size >= 0 ? Math.trunc(size) : undefined
}

function requestBodyRejectedReason(req: Request, res: Response, statusCode: number): string | undefined {
  const bodyRejected = res.locals.publicApiRequestBodyRejected as { statusCode?: unknown; errorType?: unknown } | undefined
  if (bodyRejected) {
    return statusCode === 413 || bodyRejected.errorType === 'entity.too.large'
      ? 'request_body_too_large'
      : 'request_body_parse_failed'
  }

  if (req.body !== undefined || statusCode < 400) return undefined
  const method = req.method.toUpperCase()
  if (method !== 'POST' && method !== 'PUT' && method !== 'PATCH') return undefined
  return (contentLengthBytes(req) ?? 0) > 0
    ? statusCode === 413 ? 'request_body_too_large' : 'request_body_parse_failed'
    : undefined
}

function normalizeSendPayload(value: unknown): ResponsePayload {
  if (Buffer.isBuffer(value)) {
    return value
  }
  if (typeof value === 'string') {
    try {
      return JSON.parse(value) as ResponsePayload
    } catch {
      return value
    }
  }
  return value as ResponsePayload
}

function estimatePayloadSizeBytes(value: unknown): number {
  if (value === undefined || value === null) return 0
  if (Buffer.isBuffer(value)) return value.byteLength
  if (typeof value === 'string') return Buffer.byteLength(value, 'utf8')
  const bounded = boundedSnapshotValue(value, publicApiSnapshotMaxBytes + 1)
  const json = safeJsonStringify(bounded.value)
  const jsonSizeBytes = Buffer.byteLength(json, 'utf8')
  return bounded.truncated ? Math.max(jsonSizeBytes, publicApiSnapshotMaxBytes + 1) : jsonSizeBytes
}

interface SnapshotBudgetState {
  remainingBytes: number
  truncated: boolean
  seen: WeakSet<object>
}

function boundedSnapshotValue(value: unknown, maxBytes: number): { value: unknown; truncated: boolean } {
  const state: SnapshotBudgetState = {
    remainingBytes: Math.max(1, Math.trunc(maxBytes)),
    truncated: false,
    seen: new WeakSet<object>()
  }
  return {
    value: cloneSnapshotValue(value, state, 0),
    truncated: state.truncated
  }
}

function cloneSnapshotValue(value: unknown, state: SnapshotBudgetState, depth: number): unknown {
  if (state.remainingBytes <= 0) return truncatedSnapshotMarker(state)
  if (value === undefined || value === null) {
    chargeSnapshotBytes(state, 4)
    return value
  }
  if (typeof value === 'string') return cloneSnapshotString(value, state)
  if (typeof value === 'number' || typeof value === 'boolean') {
    const text = JSON.stringify(value) ?? 'null'
    chargeSnapshotBytes(state, Buffer.byteLength(text, 'utf8'))
    return value
  }
  if (typeof value === 'bigint') {
    return cloneSnapshotString(value.toString(), state)
  }
  if (typeof value !== 'object') {
    return cloneSnapshotString(String(value), state)
  }
  if (depth >= publicApiSnapshotMaxDepth) {
    return truncatedSnapshotMarker(state)
  }
  if (state.seen.has(value)) {
    return cloneSnapshotString('[Circular]', state)
  }
  state.seen.add(value)
  try {
    if (Buffer.isBuffer(value)) {
      return cloneSnapshotBuffer(value, state)
    }
    if (value instanceof Date) {
      return cloneSnapshotString(value.toISOString(), state)
    }
    if (Array.isArray(value)) {
      return cloneSnapshotArray(value, state, depth)
    }
    return cloneSnapshotObject(value as Record<string, unknown>, state, depth)
  } finally {
    state.seen.delete(value)
  }
}

function cloneSnapshotArray(value: unknown[], state: SnapshotBudgetState, depth: number): unknown[] {
  chargeSnapshotBytes(state, 2)
  const output: unknown[] = []
  const length = Math.min(value.length, publicApiSnapshotMaxEntries)
  for (let index = 0; index < length; index += 1) {
    if (state.remainingBytes <= 0) break
    output.push(cloneSnapshotValue(value[index], state, depth + 1))
  }
  if (value.length > length || state.remainingBytes <= 0) {
    output.push(truncatedSnapshotMarker(state))
  }
  return output
}

function cloneSnapshotObject(value: Record<string, unknown>, state: SnapshotBudgetState, depth: number): Record<string, unknown> {
  chargeSnapshotBytes(state, 2)
  const output: Record<string, unknown> = {}
  let count = 0
  for (const key in value) {
    if (!Object.prototype.hasOwnProperty.call(value, key)) continue
    if (count >= publicApiSnapshotMaxEntries || state.remainingBytes <= 0) {
      output.__truncated = true
      state.truncated = true
      break
    }
    count += 1
    chargeSnapshotBytes(state, Buffer.byteLength(key, 'utf8') + 4)
    try {
      output[key] = cloneSnapshotValue(value[key], state, depth + 1)
    } catch {
      output[key] = '[unavailable]'
    }
  }
  return output
}

function cloneSnapshotBuffer(value: Buffer, state: SnapshotBudgetState): Record<string, unknown> {
  const previewBytes = Math.min(value.byteLength, publicApiSnapshotStringPreviewBytes, Math.max(0, state.remainingBytes))
  chargeSnapshotBytes(state, previewBytes + 64)
  if (value.byteLength > previewBytes) state.truncated = true
  return {
    type: 'Buffer',
    byteLength: value.byteLength,
    preview: sliceUtf8(value.subarray(0, previewBytes).toString('utf8'), previewBytes),
    truncated: value.byteLength > previewBytes
  }
}

function cloneSnapshotString(value: string, state: SnapshotBudgetState): string {
  const size = Buffer.byteLength(value, 'utf8')
  if (size <= state.remainingBytes) {
    chargeSnapshotBytes(state, size)
    return value
  }
  state.truncated = true
  const previewBytes = Math.max(0, Math.min(state.remainingBytes, publicApiSnapshotStringPreviewBytes))
  state.remainingBytes = 0
  return `${sliceUtf8(value, previewBytes)}...[truncated]`
}

function truncatedSnapshotMarker(state: SnapshotBudgetState): string {
  state.truncated = true
  return '[truncated]'
}

function chargeSnapshotBytes(state: SnapshotBudgetState, bytes: number): void {
  state.remainingBytes -= Math.max(0, Math.trunc(bytes))
  if (state.remainingBytes < 0) {
    state.truncated = true
    state.remainingBytes = 0
  }
}

function safeJsonStringify(value: unknown): string {
  try {
    return JSON.stringify(value) ?? ''
  } catch {
    return ''
  }
}

function firstString(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) {
      return value.trim().slice(0, 1000)
    }
  }
  return undefined
}

function sliceUtf8(value: string, maxBytes: number): string {
  const bytes = Buffer.from(value, 'utf8')
  if (bytes.byteLength <= maxBytes) return value
  return bytes.subarray(0, Math.max(0, maxBytes)).toString('utf8')
}
