import type { Request, Response } from 'express'

import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import {
  validateGatewayApiKey,
  type GatewayApiKeyRow
} from '../../storage/repositories.js'
import { extractBearerToken } from './openai-gateway-usage.js'
import {
  gatewayErrorPayload,
  sendGatewayJsonError
} from './openai-gateway-responses.js'

export function resolveGatewayApiKey(req: Request, res: Response): GatewayApiKeyRow | undefined {
  const gatewayApiKey = extractBearerToken(req.header('authorization'))
  if (!gatewayApiKey) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'missing_bearer_token',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, 'Gateway authentication failed')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('Missing bearer token', 'invalid_request_error'))
    return undefined
  }

  const apiKeyRecord = validateGatewayApiKey(gatewayApiKey)
  if (!apiKeyRecord) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'invalid_api_key',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, 'Gateway authentication failed')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('Invalid API key', 'invalid_request_error'))
    return undefined
  }

  return apiKeyRecord
}

export function isOpenAIStreamRequest(req: Request): boolean {
  return req.body?.stream === true
}
