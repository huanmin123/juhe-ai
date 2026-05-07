import type { Request, Response } from 'express'

import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import {
  validateGatewayApiKey,
  type GatewayApiKeyRow
} from '../../storage/repositories.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import type { DbServiceGatewayRuntime } from '../db-service/db-service-types.js'
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
    }, '网关认证失败')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('缺少 Bearer Token', 'invalid_request_error'))
    return undefined
  }

  const apiKeyRecord = validateGatewayApiKey(gatewayApiKey)
  if (!apiKeyRecord) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'invalid_api_key',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证失败')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('API Key 无效', 'invalid_request_error'))
    return undefined
  }

  return apiKeyRecord
}

export async function resolveGatewayApiKeyAsync(req: Request, res: Response): Promise<GatewayApiKeyRow | undefined> {
  const gatewayApiKey = extractBearerToken(req.header('authorization'))
  if (!gatewayApiKey) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'missing_bearer_token',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证失败')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('缺少 Bearer Token', 'invalid_request_error'))
    return undefined
  }

  const apiKeyRecord = await requestDbService({
    type: 'validate_gateway_api_key',
    key: gatewayApiKey
  })
  if (!apiKeyRecord) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'invalid_api_key',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证失败')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('API Key 无效', 'invalid_request_error'))
    return undefined
  }

  return apiKeyRecord
}

export async function resolveGatewayRuntimeAsync(req: Request, res: Response): Promise<DbServiceGatewayRuntime | undefined> {
  const gatewayApiKey = extractBearerToken(req.header('authorization'))
  if (!gatewayApiKey) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'missing_bearer_token',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证失败')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('缺少 Bearer Token', 'invalid_request_error'))
    return undefined
  }

  const runtime = await requestDbService({
    type: 'read_gateway_runtime',
    key: gatewayApiKey
  })
  if (!runtime.apiKey) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'invalid_api_key',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证失败')
    sendGatewayJsonError(res, 401, gatewayErrorPayload('API Key 无效', 'invalid_request_error'))
    return undefined
  }

  return runtime
}

export function isOpenAIStreamRequest(req: Request): boolean {
  return req.body?.stream === true
}
