import type { NextFunction, Response } from 'express'

import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import {
  createGatewayRequestBodyState,
  extractGatewayJsonBodyMetadata,
  gatewayJsonBodyLargeWarningBytes,
  isGatewayJsonContentType,
  type GatewayJsonBodyMetadata,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import { extractGatewayJsonBodyMetadataInWorker, isGatewayJsonWorkerQueueFullError } from './openai-gateway-json-parser.js'

export async function captureGatewayRawBody(
  req: GatewayRawBodyRequest,
  res: Response,
  next: NextFunction
): Promise<void> {
  try {
    const rawBody = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0)
    req.rawBody = rawBody

    const contentType = req.headers['content-type'] ?? ''
    const isJson = isGatewayJsonContentType(contentType)
    if (rawBody.length === 0) {
      req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'empty' })
      req.body = undefined
    } else if (!isJson) {
      req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'not_json' })
      req.body = undefined
    } else {
      if (rawBody.length > gatewayJsonBodyLargeWarningBytes) {
        const metadata = await extractLargeJsonBodyMetadata(req, res, rawBody)
        if (!metadata) {
          return
        }
        getRequestLogger().warn({
          event: 'gateway_large_json_body_deferred',
          method: req.method,
          path: req.path,
          originalUrl: sanitizeUrlForLog(req.originalUrl),
          rawBodyBytes: rawBody.length,
          jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
          model: metadata.model,
          stream: metadata.stream,
          imageGeneration: metadata.imageGeneration,
          imageGenerationForced: metadata.imageGenerationForced,
          invalidJson: metadata.invalidJson
        }, '网关大 JSON 请求体已完成顶层元数据扫描，完整解析延迟到账号适配或请求改写阶段')
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
      } else {
        try {
          const parsedBody = JSON.parse(rawBody.toString('utf8')) as unknown
          req.body = parsedBody
          req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'parsed', parsedBody })
        } catch {
          req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'invalid_json' })
          req.body = undefined
        }
      }
    }

    if (req.aborted || res.destroyed) {
      return
    }
    next()
  } catch (error) {
    next(error)
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
    }, '网关大 JSON 请求体元数据 worker 扫描失败，将退回同步扫描')
    return extractGatewayJsonBodyMetadata(rawBody)
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
