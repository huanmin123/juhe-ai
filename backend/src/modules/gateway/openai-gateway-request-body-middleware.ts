import type { NextFunction, Response } from 'express'

import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import {
  createGatewayRequestBodyState,
  extractGatewayJsonBodyMetadata,
  gatewayJsonBodyLargeWarningBytes,
  isGatewayJsonContentType,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import { parseGatewayJsonBodyInWorker } from './openai-gateway-json-parser.js'

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
        const metadata = extractGatewayJsonBodyMetadata(rawBody)
        console.error('debug middleware large before worker', rawBody.length)
        getRequestLogger().warn({
          event: 'gateway_large_json_body_deferred',
          method: req.method,
          path: req.path,
          originalUrl: sanitizeUrlForLog(req.originalUrl),
          rawBodyBytes: rawBody.length,
          jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
          model: metadata.model,
          stream: metadata.stream
        }, '网关大 JSON 请求体延迟到账号适配阶段按需解析')
        const parsedBody = await parseLargeJsonBodyForGatewayMetadata(req, res, rawBody)
        console.error('debug middleware large after worker', parsedBody.parsed)
        if (parsedBody.parsed) {
          req.gatewayParsedJsonBodyAvailable = true
          req.gatewayParsedJsonBody = parsedBody.value
          req.gatewayRequestBody = createGatewayRequestBodyState({
            rawBody,
            contentType,
            jsonParseStatus: 'deferred_large_json',
            parsedBody: parsedBody.value
          })
          const parsedState = req.gatewayRequestBody
          getRequestLogger().warn({
            event: 'gateway_large_json_body_parse',
            method: req.method,
            path: req.path,
            originalUrl: sanitizeUrlForLog(req.originalUrl),
            rawBodyBytes: rawBody.length,
            jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
            model: parsedState.model,
            stream: parsedState.stream,
            imageGeneration: parsedState.imageGeneration
          }, '网关大 JSON 请求体已在 worker 中完成元数据解析并继续调度')
        } else {
          req.gatewayRequestBody = createGatewayRequestBodyState({
            rawBody,
            contentType,
            jsonParseStatus: 'invalid_json',
            model: metadata.model,
            stream: metadata.stream
          })
        }
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

    if (req.destroyed || req.aborted || res.destroyed) {
      return
    }
    next()
  } catch (error) {
    next(error)
  }
}

async function parseLargeJsonBodyForGatewayMetadata(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBody: Buffer
): Promise<{ parsed: true; value: unknown } | { parsed: false }> {
  const abortController = new AbortController()
  const abort = () => abortController.abort()
  addOneShotListener(req, 'aborted', abort)
  addOneShotListener(res, 'close', abort)
  try {
    const value = await parseGatewayJsonBodyInWorker(rawBody, undefined, abortController.signal)
    return { parsed: true, value }
  } catch (error) {
    if (req.destroyed || req.aborted || res.destroyed || abortController.signal.aborted) {
      return { parsed: false }
    }
    getRequestLogger().warn({
      event: 'gateway_large_json_body_parse_failed',
      method: req.method,
      path: req.path,
      originalUrl: sanitizeUrlForLog(req.originalUrl),
      rawBodyBytes: rawBody.length,
      errorMessage: error instanceof Error ? error.message : String(error)
    }, '网关大 JSON 请求体解析失败，将按非法 JSON 拒绝')
    return { parsed: false }
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
