import type { NextFunction, Response } from 'express'

import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import { parseGatewayJsonBodyInWorker } from './openai-gateway-json-parser.js'
import {
  createGatewayRequestBodyState,
  gatewayJsonBodyLargeWarningBytes,
  isGatewayJsonContentType,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'

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
        getRequestLogger().warn({
          event: 'gateway_large_json_body_parse',
          method: req.method,
          path: req.path,
          originalUrl: sanitizeUrlForLog(req.originalUrl),
          rawBodyBytes: rawBody.length,
          jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes
        }, '网关大 JSON 请求体进入异步兼容解析')
      }
      try {
        req.body = rawBody.length > gatewayJsonBodyLargeWarningBytes
          ? await parseLargeGatewayJsonBody(req, res, rawBody)
          : JSON.parse(rawBody.toString('utf8')) as unknown
        req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'parsed' })
      } catch {
        req.gatewayRequestBody = createGatewayRequestBodyState({ rawBody, contentType, jsonParseStatus: 'invalid_json' })
        req.body = undefined
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

async function parseLargeGatewayJsonBody(req: GatewayRawBodyRequest, res: Response, rawBody: Buffer): Promise<unknown> {
  const abortController = new AbortController()
  const abort = () => abortController.abort()
  req.once('aborted', abort)
  res.once('close', abort)
  try {
    return await parseGatewayJsonBodyInWorker(rawBody, undefined, abortController.signal)
  } finally {
    req.off('aborted', abort)
    res.off('close', abort)
  }
}
