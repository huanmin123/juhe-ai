import type { Request, Response } from 'express'

import type { GroupUsageAccessMetadata } from '../../storage/repositories.js'
import { responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import { buildOpenAIModelsResponse } from './openai-gateway-route-helpers.js'
import { extractBearerToken } from './openai-gateway-usage.js'
import { enqueueUsageRecord } from './usage-record-queue.service.js'

interface OpenAIModelsResponseUsageContext {
  traceId: string
  clientIp?: string
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  groupOwnerSystemAccountId?: string
  groupAccessType?: GroupUsageAccessMetadata['groupAccessType']
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: GroupUsageAccessMetadata['groupAuthorizationSourceType']
  groupAuthorizationSourceTeamId?: string
  endpoint: string
}

interface SendOpenAIModelsGatewayResponseInput {
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: OpenAIModelsResponseUsageContext
  startedAt: number
}

export function finalizeGatewayAuthFailureAudit(
  req: Request,
  res: Response,
  auditCapture: AuditCaptureContext
): void {
  const authErrorMessage = extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少 Bearer Token'
  const authErrorPayload = gatewayErrorPayload(authErrorMessage, 'invalid_request_error')
  auditCapture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: res.statusCode,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(authErrorPayload),
    responsePartType: 'gateway_error',
    errorPhase: 'auth',
    errorCode: 'invalid_request_error',
    errorMessage: authErrorMessage
  })
}

export function sendOpenAIModelsGatewayResponse(input: SendOpenAIModelsGatewayResponseInput): void {
  const { res, auditCapture, usageContext, startedAt } = input
  const responsePayload = buildOpenAIModelsResponse()
  res.status(200).json(responsePayload)
  enqueueUsageRecord({
    ...usageContext,
    providerCode: 'openai',
    stream: false,
    statusCode: 200,
    success: true,
    firstTokenMs: Date.now() - startedAt,
    durationMs: Date.now() - startedAt
  })
  auditCapture.finalize({
    outcome: 'success',
    success: true,
    statusCode: 200,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(responsePayload),
    responsePartType: 'gateway_response',
    firstTokenMs: Date.now() - startedAt
  })
}
