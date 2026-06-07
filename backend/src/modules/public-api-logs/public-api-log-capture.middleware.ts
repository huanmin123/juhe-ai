import type { NextFunction, Request, Response } from 'express'

import { errorLogFields, logger } from '../../shared/logger.js'
import { getRequestContext, getTraceId, sanitizeUrlForLog } from '../../shared/request-context.js'
import type { PublicApiLogCaptureStatus, PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import type { ExternalIntegrationSourceAuthContext } from '../../storage/external-integration-source.repository.js'
import { enqueuePublicApiLog } from './public-api-log-queue.service.js'

type ResponsePayload = string | Buffer | Record<string, unknown> | unknown[] | undefined

interface CapturedSnapshot {
  data: Record<string, unknown>
  status: PublicApiLogCaptureStatus
  sizeBytes: number
}

const publicApiSnapshotMaxBytes = 32 * 1024

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
  const json = safeJsonStringify(data)
  if (isSnapshotEmpty(data)) {
    return {
      data,
      status: 'empty',
      sizeBytes: sanitizedSizeBytes
    }
  }
  const jsonSizeBytes = Buffer.byteLength(json, 'utf8')
  if (jsonSizeBytes <= publicApiSnapshotMaxBytes) {
    return {
      data,
      status: 'complete',
      sizeBytes: sanitizedSizeBytes || jsonSizeBytes
    }
  }
  return {
    data: {
      truncated: true,
      originalJsonSizeBytes: jsonSizeBytes,
      preview: sliceUtf8(json, publicApiSnapshotMaxBytes)
    },
    status: 'truncated',
    sizeBytes: sanitizedSizeBytes || jsonSizeBytes
  }
}

function isSnapshotEmpty(data: Record<string, unknown>): boolean {
  const body = data.body
  const query = data.query
  if (body !== undefined && body !== null && !(typeof body === 'object' && !Array.isArray(body) && Object.keys(body).length === 0)) {
    return false
  }
  if (query && typeof query === 'object' && !Array.isArray(query) && Object.keys(query).length > 0) {
    return false
  }
  return body === undefined || body === null
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
  const json = safeJsonStringify(value)
  return Buffer.byteLength(json, 'utf8')
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
