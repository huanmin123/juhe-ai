import type { Request, Response } from 'express'

import type { AuditCaptureContext } from './audit-capture.service.js'
import {
  filterGatewayAccountsByRequestCapability
} from './openai-gateway-account-capability-filter.js'
import {
  filterGatewayAccountsByRequestedModel,
  gatewayModelFilterFailureMessage
} from './openai-gateway-model-filter.js'
import { sendGatewayFailureResponse } from './openai-gateway-failure-response.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'
import { requestModel } from './openai-gateway-usage.js'
import type { GatewayFailureUsageContext } from './openai-gateway-usage-records.js'
import { recordClientIpRequestErrorSample } from './openai-gateway-local-request-errors.js'
import type { OpenAIGatewayDispatchContext } from './openai-gateway-request-preflight.js'

export interface RequestCandidateFallbackResult {
  attempted: boolean
  context?: OpenAIGatewayDispatchContext
}

export type RequestCandidateFilterResult =
  | { outcome: 'accounts'; accounts: UpstreamAccount[] }
  | { outcome: 'fallback'; context?: OpenAIGatewayDispatchContext }
  | { outcome: 'completed' }

export async function filterOpenAIGatewayRequestCandidateAccounts(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  rawCandidateAccounts: UpstreamAccount[]
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
  endpoint: string
  attemptFallback: (reason: string) => Promise<RequestCandidateFallbackResult>
}): Promise<RequestCandidateFilterResult> {
  const capabilityFilter = filterGatewayAccountsByRequestCapability(input.req, input.rawCandidateAccounts)
  if (capabilityFilter.skippedCount > 0) {
    input.auditCapture.addGatewayMetadata({
      label: 'account_request_capability_filter',
      metadata: {
        skippedCount: capabilityFilter.skippedCount,
        remainingCount: capabilityFilter.accounts.length,
        reason: capabilityFilter.reason
      }
    })
  }
  if (input.rawCandidateAccounts.length > 0 && capabilityFilter.accounts.length === 0) {
    const fallback = await input.attemptFallback('request_capability_mismatch')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
    }
    const statusCode = 400
    const message = '当前分组无账户支持请求路径或客户端协议'
    const responsePayload = gatewayErrorPayload(message, 'invalid_request_error', 'request_capability_mismatch')
    recordClientIpRequestErrorSample({
      auditCapture: input.auditCapture,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId,
      groupId: input.groupId,
      clientIp: input.clientIp,
      endpoint: input.endpoint,
      reason: 'request_capability_mismatch',
      signature: `${input.req.method.toUpperCase()} ${input.req.path || input.req.originalUrl.split('?')[0] || '/'}`
    })
    sendGatewayFailureResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'request_validation',
        errorCode: 'request_capability_mismatch',
        errorMessage: message
      }
    })
    return { outcome: 'completed' }
  }

  const modelFilter = filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, requestModel(input.req))
  if (modelFilter.skippedCount > 0 || modelFilter.mappingMatchedCount > 0) {
    input.auditCapture.addGatewayMetadata({
      label: 'account_model_filter',
      metadata: {
        requestedModel: modelFilter.requestedModel,
        skippedCount: modelFilter.skippedCount,
        limitedAccountCount: modelFilter.limitedAccountCount,
        directMatchedCount: modelFilter.directMatchedCount,
        mappingMatchedCount: modelFilter.mappingMatchedCount,
        remainingCount: modelFilter.accounts.length,
        reason: modelFilter.reason
      }
    })
  }
  if (capabilityFilter.accounts.length > 0 && modelFilter.accounts.length === 0) {
    const fallback = await input.attemptFallback(modelFilter.reason ?? 'unsupported_model')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
    }
    const statusCode = 400
    const message = gatewayModelFilterFailureMessage(modelFilter)
    const responsePayload = gatewayErrorPayload(message, 'invalid_request_error')
    recordClientIpRequestErrorSample({
      auditCapture: input.auditCapture,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId,
      groupId: input.groupId,
      clientIp: input.clientIp,
      endpoint: input.endpoint,
      reason: 'unsupported_model',
      signature: modelFilter.reason ?? modelFilter.requestedModel ?? 'unsupported_model'
    })
    sendGatewayFailureResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'request_validation',
        errorCode: modelFilter.reason ?? 'unsupported_model',
        errorMessage: message
      }
    })
    return { outcome: 'completed' }
  }

  return { outcome: 'accounts', accounts: modelFilter.accounts }
}
