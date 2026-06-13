import type { NextFunction, Response } from 'express'

import { runtimeConfig } from '../../../config/runtime.js'
import { getRequestLogger, sanitizeUrlForLog } from '../../../shared/request-context.js'
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
  type GatewayJsonBodyMetadata,
  type GatewayRawBodyRequest
} from './body.js'
import { extractGatewayJsonBodyMetadataInWorker, isGatewayJsonWorkerQueueFullError } from './json-parser.js'
import { resolveOpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'

type GatewayRawBodyLimitScope = 'gateway' | 'text' | 'image'

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
      rejectGatewayRawBodyTooLarge(req, res, rawBody, gatewayRawBodyHardLimitBytes, 'gateway')
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
      rejectGatewayRawBodyInFlightLimit(req, res, rawBody)
      return
    }

    req.rawBody = rawBody

    if (rawBody.length === 0) {
      req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'empty' })
      req.body = undefined
    } else if (!isJson) {
      req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'not_json' })
      req.body = undefined
      if (rejectGatewayRawBodyByRequestLane(req, res, rawBody)) {
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
          imageGeneration: metadata.imageGeneration,
          imageGenerationForced: metadata.imageGenerationForced
        })
        req.body = undefined
        if (rejectGatewayRawBodyByRequestLane(req, res, rawBody)) {
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
        if (rejectGatewayRawBodyByRequestLane(req, res, rawBody)) {
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

function rejectGatewayRawBodyByRequestLane(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer
): boolean {
  const requestLimit = resolveGatewayRawBodyRequestLimit(req)
  if (rawBody.length <= requestLimit.limitBytes) {
    return false
  }
  rejectGatewayRawBodyTooLarge(req, res, rawBody, requestLimit.limitBytes, requestLimit.scope)
  return true
}

function resolveGatewayRawBodyRequestLimit(req: GatewayRawBodyRequest): { limitBytes: number; scope: GatewayRawBodyLimitScope } {
  return resolveOpenAIGatewayRequestLane(req) === 'image'
    ? { limitBytes: gatewayImageRawBodyHardLimitBytes, scope: 'image' }
    : { limitBytes: gatewayTextRawBodyLimitBytes(req.gatewayRuntime?.settings?.gatewayTextRawBodyLimitMegabytes), scope: 'text' }
}

function rejectGatewayRawBodyTooLarge(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer,
  limitBytes: number,
  limitScope: GatewayRawBodyLimitScope
): void {
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
  if (!res.headersSent) {
    res.status(413).json({
      error: {
        message: '请求体过大',
        type: 'request_too_large'
      }
    })
  }
}

function rejectGatewayRawBodyInFlightLimit(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer
): void {
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
      if (!res.headersSent) {
        res.status(503).json({
          error: {
            message: '网关请求解析繁忙，请稍后重试',
            type: 'server_overloaded'
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
    if (!res.headersSent) {
      res.status(503).json({
        error: {
          message: '网关请求解析繁忙，请稍后重试',
          type: 'server_overloaded'
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
