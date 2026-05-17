import type { NextFunction, Response } from 'express'

import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import {
  createGatewayRequestBodyState,
  extractGatewayJsonBodyMetadata,
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
        const metadata = extractGatewayJsonBodyMetadata(rawBody)
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
        req.gatewayRequestBody = createGatewayRequestBodyState({
          rawBody,
          contentType,
          jsonParseStatus: 'deferred_large_json',
          model: metadata.model,
          stream: metadata.stream
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

    if (req.destroyed || req.aborted || res.destroyed) {
      return
    }
    next()
  } catch (error) {
    next(error)
  }
}
