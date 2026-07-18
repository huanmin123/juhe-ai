import type { NextFunction, Response } from 'express'

import { runtimeConfig } from '../../../config/runtime.js'
import type { UsageFailureAttribution } from '../../../storage/usage-records.repository.js'
import { getRequestContext } from '../../../shared/request-context.js'
import {
  createTraceId,
  getRequestLogger,
  getTraceId,
  sanitizeUrlForLog
} from '../../../shared/request-context.js'
import { recordDroppedAuditCapture } from '../../audit-logs/audit-log-queue.service.js'
import {
  createGatewayRequestBodyState,
  gatewayImageRawBodyHardLimitBytes,
  gatewayJsonBodyInlineParseMaxBytes,
  gatewayJsonBodyLargeWarningBytes,
  gatewayRawBodyHardLimitBytes,
  gatewayTextRawBodyHardLimitBytes,
  gatewayTextRawBodyLimitBytes,
  getGatewayRequestBodyInFlightState,
  isGatewayJsonContentType,
  releaseGatewayRequestBodyInFlightBytes,
  tryAcquireGatewayRequestBodyInFlightBytes,
  type GatewayRawBodyRequest
} from './body.js'
import type { GatewayJsonBodyMetadata } from './json-metadata-scanner.js'
import { extractGatewayJsonBodyMetadataInWorker, isGatewayJsonWorkerQueueFullError } from './json-parser.js'
import { resolveOpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { buildUsageRequestSnapshot } from '../usage/snapshots.js'
import type { UsageRequestSnapshot } from '../usage/snapshots.js'
import { groupUsageMetadata, recordGatewayFailure } from '../usage/records.js'
import { gatewayErrorPayload, type GatewayErrorPayload } from '../response/responses.js'
import { extractClientIp, requestEndpoint } from './metadata.js'
import { buildGatewayUsageContext } from './preflight.js'

export type GatewayRawBodyLimitScope = 'gateway' | 'text' | 'image'
type GatewayBodyRejectReason = 'gateway_body_parser' | 'gateway_body_size_limit' | 'gateway_body_in_flight_limit' | 'gateway_body_metadata_worker' | 'gateway_body_admission'

export interface GatewayBodyRejectionInput {
  statusCode: number
  responsePayload: GatewayErrorPayload
  rawBodyBytes: number
  reason: GatewayBodyRejectReason
  errorCode?: string
  errorMessage?: string
  limitBytes?: number
  limitScope?: GatewayRawBodyLimitScope
  failureAttribution?: UsageFailureAttribution
}

export interface GatewayRawBodyParserError {
  status?: number
  statusCode?: number
  type?: string
  received?: number
  length?: number
  limit?: number
}

export interface GatewayRawBodyParserErrorResponse {
  statusCode: number
  message: string
  errorType: string
  failureAttribution: UsageFailureAttribution
}

export function classifyGatewayRawBodyParserError(error: GatewayRawBodyParserError): GatewayRawBodyParserErrorResponse {
  const parserType = error.type?.trim()
  if (parserType === 'request.aborted' || parserType === 'request.size.invalid') {
    return {
      statusCode: 408,
      message: '请求体上传未完成，请重试',
      errorType: 'request_timeout',
      failureAttribution: 'client_lifecycle'
    }
  }
  const statusCode = Number.isInteger(error.statusCode)
    ? Number(error.statusCode)
    : Number.isInteger(error.status)
      ? Number(error.status)
      : 400
  if (statusCode === 413) {
    return {
      statusCode,
      message: '请求体过大',
      errorType: 'request_too_large',
      failureAttribution: 'gateway_policy'
    }
  }
  return {
    statusCode,
    message: '网关请求体无效',
    errorType: 'invalid_request_error',
    failureAttribution: 'gateway_policy'
  }
}

export async function captureGatewayRawBody(
  req: GatewayRawBodyRequest,
  res: Response,
  next: NextFunction
): Promise<void> {
  try {
    const rawBody = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0)
    const contentType = req.headers['content-type'] ?? ''
    const isJson = isGatewayJsonContentType(contentType)
    if (rawBody.length > gatewayRawBodyHardLimitBytes) {
      req.gatewayRequestBody = {
        rawBodyBytes: rawBody.length,
        contentType: String(contentType ?? ''),
        isJson,
        jsonParseStatus: isJson ? 'deferred_large_json' : 'not_json',
        jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes
      }
      await rejectGatewayRawBodyTooLarge(req, res, rawBody, gatewayRawBodyHardLimitBytes, 'gateway')
      return
    }

    if (!tryAcquireGatewayRequestBodyInFlightBytes(req, res, rawBody.length, runtimeConfig.gateway.bodyInFlightMaxBytes)) {
      req.gatewayRequestBody = {
        rawBodyBytes: rawBody.length,
        contentType: String(contentType ?? ''),
        isJson,
        jsonParseStatus: isJson ? 'deferred_large_json' : 'not_json',
        jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes
      }
      await rejectGatewayRawBodyInFlightLimit(req, res, rawBody)
      return
    }

    req.rawBody = rawBody

    if (rawBody.length === 0) {
      req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'empty' })
      req.body = undefined
    } else if (!isJson) {
      req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'not_json' })
      req.body = undefined
      if (await rejectGatewayRawBodyByRequestLane(req, res, rawBody)) {
        return
      }
    } else {
      if (rawBody.length > gatewayJsonBodyInlineParseMaxBytes) {
        const metadata = await extractLargeJsonBodyMetadata(req, res, rawBody)
        if (!metadata) {
          req.rawBody = undefined
          req.body = undefined
          releaseGatewayRequestBodyInFlightBytes(req)
          return
        }
        const logPayload = {
          event: 'gateway_large_json_body_deferred',
          method: req.method,
          path: req.path,
          originalUrl: sanitizeUrlForLog(req.originalUrl),
          rawBodyBytes: rawBody.length,
          jsonInlineParseMaxBytes: gatewayJsonBodyInlineParseMaxBytes,
          jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
          model: metadata.model,
          stream: metadata.stream,
          serviceTier: metadata.serviceTier,
          reasoningEffort: metadata.reasoningEffort,
          maxOutputTokens: metadata.maxOutputTokens,
          imageGeneration: metadata.imageGeneration,
          imageGenerationForced: metadata.imageGenerationForced,
          invalidJson: metadata.invalidJson
        }
        if (rawBody.length > gatewayJsonBodyLargeWarningBytes) {
          getRequestLogger().warn(logPayload, '网关大 JSON 请求体已完成顶层元数据扫描，完整解析延迟到账号适配或请求改写阶段')
        } else {
          getRequestLogger().debug(logPayload, '网关 JSON 请求体超过主进程内联解析阈值，已转入 worker 元数据扫描')
        }
        req.gatewayRequestBody = createGatewayRequestBodyState({
          rawBody,
          contentType,
          jsonParseStatus: metadata.invalidJson ? 'invalid_json' : 'deferred_large_json',
          model: metadata.model,
          stream: metadata.stream,
          serviceTier: metadata.serviceTier,
          reasoningEffort: metadata.reasoningEffort,
          maxOutputTokens: metadata.maxOutputTokens,
          imageGeneration: metadata.imageGeneration,
          imageGenerationForced: metadata.imageGenerationForced
        })
        req.body = undefined
        if (await rejectGatewayRawBodyByRequestLane(req, res, rawBody)) {
          return
        }
      } else {
        try {
          const parsedBody = JSON.parse(rawBody.toString('utf8')) as unknown
          req.body = parsedBody
          req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'parsed', parsedBody })
        } catch {
          req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'invalid_json' })
          req.body = undefined
        }
        if (await rejectGatewayRawBodyByRequestLane(req, res, rawBody)) {
          return
        }
      }
    }

    if (req.aborted || res.destroyed) {
      req.rawBody = undefined
      req.body = undefined
      releaseGatewayRequestBodyInFlightBytes(req)
      return
    }
    next()
  } catch (error) {
    releaseGatewayRequestBodyInFlightBytes(req)
    next(error)
  }
}

export async function rejectGatewayRawBodyByContentLength(
  req: GatewayRawBodyRequest,
  res: Response,
  next: NextFunction
): Promise<void> {
  const contentLength = requestContentLengthBytes(req)
  if (contentLength === undefined) {
    next()
    return
  }
  const requestLimit = resolveGatewayRawBodyContentLengthLimit(req)
  if (!requestLimit) {
    next()
    return
  }
  if (contentLength <= requestLimit.limitBytes) {
    next()
    return
  }
  getRequestLogger().warn({
    event: 'gateway_raw_body_content_length_limit_rejected',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    rawBodyBytes: contentLength,
    rawBodyLimitBytes: requestLimit.limitBytes,
    rawBodyLimitScope: requestLimit.scope
  }, '网关请求体 Content-Length 超过当前请求类型上限，已在读取 body 前拒绝')
  await recordGatewayBodyRejection(req, {
    statusCode: 413,
    responsePayload: gatewayErrorPayload('请求体过大', 'request_too_large'),
    rawBodyBytes: contentLength,
    reason: 'gateway_body_size_limit',
    errorCode: 'request_too_large',
    errorMessage: '请求体过大',
    limitBytes: requestLimit.limitBytes,
    limitScope: requestLimit.scope
  })
  if (!res.headersSent) {
    res.status(413).json({
      error: {
        message: '请求体过大',
        type: 'request_too_large'
      }
    })
  }
}

async function rejectGatewayRawBodyByRequestLane(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer
): Promise<boolean> {
  const requestLimit = resolveGatewayRawBodyRequestLimit(req)
  if (rawBody.length <= requestLimit.limitBytes) {
    return false
  }
  await rejectGatewayRawBodyTooLarge(req, res, rawBody, requestLimit.limitBytes, requestLimit.scope)
  return true
}

function resolveGatewayRawBodyRequestLimit(req: GatewayRawBodyRequest): { limitBytes: number; scope: GatewayRawBodyLimitScope } {
  return resolveOpenAIGatewayRequestLane(req) === 'image'
    ? { limitBytes: gatewayImageRawBodyHardLimitBytes, scope: 'image' }
    : { limitBytes: gatewayTextRawBodyLimitBytes(req.gatewayRuntime?.settings?.gatewayTextRawBodyLimitMegabytes), scope: 'text' }
}

function resolveGatewayRawBodyContentLengthLimit(req: GatewayRawBodyRequest): { limitBytes: number; scope: GatewayRawBodyLimitScope } | undefined {
  const path = String(req.path || req.originalUrl || '').split('?')[0]?.toLowerCase() ?? ''
  if (path === '/images' || path.startsWith('/images/') || path === '/v1/images' || path.startsWith('/v1/images/')) {
    return { limitBytes: gatewayImageRawBodyHardLimitBytes, scope: 'image' }
  }
  if (isGatewayTextJsonBodyPath(path)) {
    return { limitBytes: gatewayTextRawBodyLimitBytes(req.gatewayRuntime?.settings?.gatewayTextRawBodyLimitMegabytes), scope: 'text' }
  }
  return undefined
}

function isGatewayTextJsonBodyPath(path: string): boolean {
  return path === '/chat/completions'
    || path === '/v1/chat/completions'
    || path === '/messages'
    || path.startsWith('/messages/')
    || path === '/v1/messages'
    || path.startsWith('/v1/messages/')
    || path === '/embeddings'
    || path === '/v1/embeddings'
}

function requestContentLengthBytes(req: GatewayRawBodyRequest): number | undefined {
  const value = req.headers['content-length']
  const text = Array.isArray(value) ? value[0] : value
  if (typeof text !== 'string' || text.trim() === '') {
    return undefined
  }
  const parsed = Number(text)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : undefined
}

async function rejectGatewayRawBodyTooLarge(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer,
  limitBytes: number,
  limitScope: GatewayRawBodyLimitScope
): Promise<void> {
  getRequestLogger().warn({
    event: limitScope === 'gateway' ? 'gateway_raw_body_hard_limit_rejected' : 'gateway_raw_body_request_limit_rejected',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    rawBodyBytes: rawBody.length,
    rawBodyLimitBytes: limitBytes,
    rawBodyLimitScope: limitScope,
    rawBodyHardLimitBytes: gatewayRawBodyHardLimitBytes,
    gatewayTextRawBodyHardLimitBytes,
    gatewayImageRawBodyHardLimitBytes
  }, limitScope === 'gateway'
    ? '网关请求体超过硬上限，已拒绝以保护主进程'
    : '网关请求体超过当前请求类型上限，已拒绝以保护主进程')
  req.rawBody = undefined
  req.body = undefined
  releaseGatewayRequestBodyInFlightBytes(req)
  await recordGatewayBodyRejection(req, {
    statusCode: 413,
    responsePayload: gatewayErrorPayload('请求体过大', 'request_too_large'),
    rawBodyBytes: rawBody.length,
    reason: 'gateway_body_size_limit',
    errorCode: 'request_too_large',
    errorMessage: '请求体过大',
    limitBytes,
    limitScope
  })
  if (!res.headersSent) {
    res.status(413).json({
      error: {
        message: '请求体过大',
        type: 'request_too_large'
      }
    })
  }
}

async function rejectGatewayRawBodyInFlightLimit(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer
): Promise<void> {
  const state = getGatewayRequestBodyInFlightState(runtimeConfig.gateway.bodyInFlightMaxBytes)
  getRequestLogger().warn({
    event: 'gateway_raw_body_in_flight_limit_rejected',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    rawBodyBytes: rawBody.length,
    bodyInFlightBytes: state.currentBytes,
    bodyInFlightRequestCount: state.requestCount,
    bodyInFlightMaxBytes: state.maxBytes,
    bodyInFlightRejectedCount: state.rejectedCount
  }, '网关请求体在途总量超过上限，已拒绝以保护主进程')
  req.rawBody = undefined
  req.body = undefined
  await recordGatewayBodyRejection(req, {
    statusCode: 503,
    responsePayload: gatewayErrorPayload('网关请求体在途总量过高，请稍后重试', 'server_overloaded', 'gateway_body_in_flight_limit_exceeded'),
    rawBodyBytes: rawBody.length,
    reason: 'gateway_body_in_flight_limit',
    errorCode: 'gateway_body_in_flight_limit_exceeded',
    errorMessage: '网关请求体在途总量过高，请稍后重试'
  })
  if (!res.headersSent) {
    res.setHeader('Retry-After', '1')
    res.status(503).json({
      error: {
        message: '网关请求体在途总量过高，请稍后重试',
        type: 'server_overloaded',
        code: 'gateway_body_in_flight_limit_exceeded'
      }
    })
  }
}

async function extractLargeJsonBodyMetadata(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer
): Promise<GatewayJsonBodyMetadata | undefined> {
  const abortController = new AbortController()
  const abort = () => abortController.abort()
  addOneShotListener(req, 'aborted', abort)
  addOneShotListener(res, 'close', abort)
  try {
    return await extractGatewayJsonBodyMetadataInWorker(rawBody, undefined, abortController.signal)
  } catch (error) {
    if (req.aborted || res.destroyed || abortController.signal.aborted) {
      return undefined
    }
    if (isGatewayJsonWorkerQueueFullError(error)) {
      getRequestLogger().warn({
        event: 'gateway_large_json_body_metadata_worker_queue_full',
        method: req.method,
        path: req.path,
        originalUrl: sanitizeUrlForLog(req.originalUrl),
        rawBodyBytes: rawBody.length
      }, '网关大 JSON 请求体元数据 worker 队列已满，拒绝本次请求以保护主进程')
      await recordGatewayBodyRejection(req, {
        statusCode: 503,
        responsePayload: gatewayErrorPayload('网关请求解析繁忙，请稍后重试', 'server_overloaded', 'gateway_json_parser_busy'),
        rawBodyBytes: rawBody.length,
        reason: 'gateway_body_metadata_worker',
        errorCode: 'gateway_json_parser_busy',
        errorMessage: '网关请求解析繁忙，请稍后重试'
      })
      if (!res.headersSent) {
        res.status(503).json({
          error: {
            message: '网关请求解析繁忙，请稍后重试',
            type: 'server_overloaded',
            code: 'gateway_json_parser_busy'
          }
        })
      }
      return undefined
    }
    getRequestLogger().warn({
      event: 'gateway_large_json_body_metadata_worker_failed',
      method: req.method,
      path: req.path,
      originalUrl: sanitizeUrlForLog(req.originalUrl),
      rawBodyBytes: rawBody.length,
      errorMessage: error instanceof Error ? error.message : String(error)
    }, '网关大 JSON 请求体元数据 worker 扫描失败，拒绝本次请求以保护主进程')
    await recordGatewayBodyRejection(req, {
      statusCode: 503,
      responsePayload: gatewayErrorPayload('网关请求解析暂时不可用，请稍后重试', 'server_overloaded', 'gateway_json_parser_failed'),
      rawBodyBytes: rawBody.length,
      reason: 'gateway_body_metadata_worker',
      errorCode: 'gateway_json_parser_failed',
      errorMessage: '网关请求解析暂时不可用，请稍后重试'
    })
    if (!res.headersSent) {
      res.status(503).json({
        error: {
          message: '网关请求解析暂时不可用，请稍后重试',
          type: 'server_overloaded',
          code: 'gateway_json_parser_failed'
        }
      })
    }
    return undefined
  } finally {
    removeListener(req, 'aborted', abort)
    removeListener(res, 'close', abort)
  }
}

function addOneShotListener(target: unknown, event: string, listener: () => void): void {
  const once = (target as { once?: (event: string, listener: () => void) => void })?.once
  if (typeof once === 'function') {
    once.call(target, event, listener)
  }
}

function removeListener(target: unknown, event: string, listener: () => void): void {
  const off = (target as { off?: (event: string, listener: () => void) => void })?.off
  if (typeof off === 'function') {
    off.call(target, event, listener)
    return
  }
  const remove = (target as { removeListener?: (event: string, listener: () => void) => void })?.removeListener
  if (typeof remove === 'function') {
    remove.call(target, event, listener)
  }
}

export async function recordGatewayBodyRejection(req: GatewayRawBodyRequest, input: GatewayBodyRejectionInput): Promise<void> {
  try {
    const context = getRequestContext()
    const traceId = getTraceId() ?? createTraceId()
    const clientIp = requestClientIp(req)
    const startedAt = context?.startedAt ?? Date.now()
    const [path, ...queryParts] = req.originalUrl.includes('?')
      ? req.originalUrl.split('?')
      : [req.originalUrl.split('?')[0] || req.path]
    const runtime = req.gatewayRuntime
    const apiKey = runtime?.apiKey
    const groupUsageFields = runtime?.groupAccess ? groupUsageMetadata(runtime.groupAccess) : undefined
    const auditErrorMessage = input.limitBytes !== undefined && input.limitScope
      ? `${input.errorMessage ?? input.responsePayload.error.message}（rawBodyBytes=${input.rawBodyBytes}, limitBytes=${input.limitBytes}, limitScope=${input.limitScope}）`
      : input.errorMessage ?? input.responsePayload.error.message
    recordDroppedAuditCapture({
      traceId,
      auditOutcome: 'gateway_failed',
      success: false,
      bytes: input.rawBodyBytes,
      reason: 'gateway_body_rejected',
      method: req.method,
      path: path || req.path,
      queryString: queryParts.length ? queryParts.join('?') : undefined,
      statusCode: input.statusCode,
      errorPhase: 'gateway',
      errorCode: input.errorCode,
      errorMessage: auditErrorMessage,
      contentType: requestHeaderValue(req, 'content-type'),
      clientIp,
      userAgent: requestHeaderValue(req, 'user-agent'),
      trafficSource: 'gateway',
      systemAccountId: apiKey?.system_account_id,
      apiKeyId: apiKey?.id,
      groupId: apiKey?.selected_group_id,
      providerCode: groupUsageFields?.providerCode
    })

    if (!apiKey) {
      return
    }
    await recordGatewayFailure(req, buildGatewayUsageContext({
      traceId,
      clientIp,
      identity: {
        systemAccountId: apiKey.system_account_id,
        apiKeyId: apiKey.id,
        groupId: apiKey.selected_group_id
      },
      trafficSource: 'gateway',
      groupUsageFields,
      endpoint: requestEndpoint(req),
      requestSnapshot: buildBodyRejectionUsageRequestSnapshot(req, traceId, clientIp, input)
    }), {
      statusCode: input.statusCode,
      startedAt,
      responsePayload: input.responsePayload,
      errorMessage: input.errorMessage,
      errorCode: input.errorCode,
      failureAttribution: input.failureAttribution
    })
  } catch (error) {
    getRequestLogger().warn({
      event: 'gateway_body_rejection_record_failed',
      reason: input.reason,
      method: req.method,
      path: req.path,
      originalUrl: sanitizeUrlForLog(req.originalUrl),
      statusCode: input.statusCode,
      rawBodyBytes: input.rawBodyBytes,
      errorMessage: error instanceof Error ? error.message : String(error)
    }, '网关请求体拒绝记录写入失败，已保留原始拒绝响应')
  }
}

function buildBodyRejectionUsageRequestSnapshot(
  req: GatewayRawBodyRequest,
  traceId: string,
  clientIp: string | undefined,
  input: GatewayBodyRejectionInput
): UsageRequestSnapshot {
  const snapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const metadataOnlySnapshot: UsageRequestSnapshot = { ...snapshot }
  delete metadataOnlySnapshot.body
  return {
    ...metadataOnlySnapshot,
    bodyOmission: {
      omitted: true,
      reason: input.reason,
      message: input.errorMessage ?? input.responsePayload.error.message,
      rawBodyBytes: input.rawBodyBytes,
      statusCode: input.statusCode,
      errorCode: input.errorCode
    }
  }
}

function requestHeaderValue(req: GatewayRawBodyRequest, name: string): string | undefined {
  const header = (req as { header?: (name: string) => string | undefined }).header
  if (typeof header === 'function') {
    return header.call(req, name)
  }
  const value = req.headers[name.toLowerCase()]
  return Array.isArray(value) ? value.join(', ') : value
}

function requestClientIp(req: GatewayRawBodyRequest): string | undefined {
  const context = getRequestContext()
  if (context?.clientIp) {
    return context.clientIp
  }
  try {
    return extractClientIp(req)
  } catch {
    return undefined
  }
}
