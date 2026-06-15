import type { Request, Response } from 'express'

import type { GroupUsageAccessMetadata } from '../../../storage/repositories.js'
import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import { gatewayErrorPayload } from './responses.js'
import { buildOpenAIModelsResponse } from '../protocols/openai-v1/route-helpers.js'
import { listCachedProviderModelCatalogAsync } from '../runtime/runtime-cache.service.js'
import { extractBearerToken } from '../request/metadata.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import { enqueueUsageRecord } from '../usage/record-queue.service.js'
import { GPT_VENDOR_CODE } from '../../../domain/provider-protocol.js'

interface OpenAIModelsResponseUsageContext {
  traceId: string
  trafficSource: OpenAIGatewayTrafficSource
  clientIp?: string
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  groupOwnerSystemAccountId?: string
  groupAccessType?: GroupUsageAccessMetadata['groupAccessType']
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: GroupUsageAccessMetadata['groupAuthorizationSourceType']
  groupAuthorizationSourceTeamId?: string
  providerCode?: string
  endpoint: string
}

interface SendOpenAIModelsGatewayResponseInput {
  req: Request
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
  const locals = res.locals as Record<string, unknown>
  const authErrorMessage = typeof locals.gatewayAuthFailureErrorMessage === 'string'
    ? locals.gatewayAuthFailureErrorMessage
    : extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少访问令牌'
  const authErrorCode = typeof locals.gatewayAuthFailureErrorCode === 'string'
    ? locals.gatewayAuthFailureErrorCode
    : 'invalid_request_error'
  const authErrorPayload = gatewayErrorPayload(authErrorMessage, 'invalid_request_error', authErrorCode)
  auditCapture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: res.statusCode,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(authErrorPayload),
    responsePartType: 'gateway_error',
    errorPhase: 'auth',
    errorCode: authErrorCode,
    errorMessage: authErrorMessage
  })
}

export async function sendOpenAIModelsGatewayResponse(input: SendOpenAIModelsGatewayResponseInput): Promise<void> {
  const { req, res, auditCapture, usageContext, startedAt } = input
  const providerCode = usageContext.providerCode ?? GPT_VENDOR_CODE
  const catalog = await listCachedProviderModelCatalogAsync({
    providerCode,
    systemAccountId: usageContext.systemAccountId
  })
  const responsePayload = buildOpenAIModelsResponse(catalog, req)
  res.status(200).json(responsePayload)
  enqueueUsageRecord({
    ...usageContext,
    providerCode,
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
