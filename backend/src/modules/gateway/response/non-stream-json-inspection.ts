import type { Request, Response } from 'express'

import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import {
  responseHeadersToObject,
  type AuditCaptureContext
} from '../audit/capture.service.js'
import { responseInspectionAuditMetadata } from '../audit/metadata.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  extractOpenAIJsonSemanticFrames,
  openAIResponseEndpointFamilyFromRequest
} from '../protocols/openai-v1/response-semantics.js'
import { parseOpenAIUsageFromJsonBuffer } from '../protocols/openai-v1/usage.js'
import {
  forgetOpenAIAccountForSession
} from '../runtime/session-affinity.service.js'
import {
  isEffectiveOpenAIStreamRequest,
  type GatewayUpstreamResponse
} from '../upstream/request.js'
import { buildUsageResponseSnapshot } from '../usage/snapshots.js'
import {
  recordCompletedUpstreamAttempt,
  type GatewayUsageContext
} from '../usage/records.js'
import {
  inspectResponseSemanticFrames,
  resolveRuntimeResponseInspectionPolicies
} from './inspection.js'
import {
  applyResponseInspectionObservationDecisions,
  applyResponseInspectionPolicyRuntimeSideEffects
} from './inspection-runtime-effects.js'
import type { UpstreamResponseHandlingResult } from './response-handling-result.js'
import {
  gatewayErrorPayload,
  sendGatewayErrorResponse
} from './responses.js'
import {
  shouldExcludeCurrentAccountForStreamServerRetry,
  shouldRetryResponseInspectionDecisionOnServer
} from './stream-finalization-retry-decision.js'
export async function inspectBufferedOpenAIJsonResponse(input: {
  req: Request
  res: Response
  account: UpstreamAccount
  upstreamResponse: GatewayUpstreamResponse
  upstreamUrl: string
  auditAttemptId: string
  auditCapture: AuditCaptureContext
  settings: GatewaySettings
  usageContext: GatewayUsageContext
  startedAt: number
  responseBody: Buffer
  responseBodyText: string
  firstTokenMs?: number
  responseInspectionPolicies?: ResponseInspectionPolicySummary[]
  accountStateMutationEnabled: boolean
  sessionAffinityKey?: string
}): Promise<UpstreamResponseHandlingResult | undefined> {
  let parsedJson: unknown
  try {
    parsedJson = input.responseBody.length > 0
      ? JSON.parse(input.responseBodyText) as unknown
      : undefined
  } catch {
    return undefined
  }
  const endpointFamily = openAIResponseEndpointFamilyFromRequest(input.req)
  const frames = extractOpenAIJsonSemanticFrames(parsedJson, endpointFamily)
  if (frames.length === 0) return undefined

  const policies = resolveRuntimeResponseInspectionPolicies({
    account: input.account,
    managementPolicies: input.responseInspectionPolicies
  })
  const inspection = inspectResponseSemanticFrames({
    frames,
    policies,
    downstreamWritten: false,
    transport: 'json'
  })
  await applyResponseInspectionObservationDecisions(
    inspection.observations,
    undefined,
    input.account,
    input.settings,
    input.auditCapture,
    input.accountStateMutationEnabled
  )
  if (!inspection.decision) return undefined

  const decision = inspection.decision
  await applyResponseInspectionPolicyRuntimeSideEffects(decision, input.account, input.settings, input.accountStateMutationEnabled)
  input.auditCapture.addGatewayMetadata({
    label: 'response_inspection',
    metadata: responseInspectionAuditMetadata(decision)
  })
  const usage = parseOpenAIUsageFromJsonBuffer(input.responseBody)
  const message = decision.upstreamErrorMessage ?? decision.rewriteMessage ?? `JSON 响应命中检查策略：${decision.policyName ?? decision.policyId ?? '未命名策略'}`
  const errorCode = decision.rewriteErrorCode ?? decision.upstreamErrorCode ?? 'response_inspection_matched'
  forgetOpenAIAccountForSession(input.sessionAffinityKey, input.account.id)
  input.auditCapture.completeAttempt(input.auditAttemptId, {
    statusCode: input.upstreamResponse.status,
    responseHeaders: input.upstreamResponse.headers,
    responseBody: input.responseBody,
    success: false,
    errorPhase: 'response_inspection',
    errorCode,
    errorMessage: message
  })
  recordCompletedUpstreamAttempt(input.req, {
    ...input.usageContext,
    account: input.account,
    statusCode: input.upstreamResponse.status,
    success: false,
    stream: isEffectiveOpenAIStreamRequest(input.req, input.account),
    firstTokenMs: input.firstTokenMs,
    startedAt: input.startedAt,
    usage,
    errorCode,
    requestSnapshot: input.usageContext.requestSnapshot,
    responseSnapshot: buildUsageResponseSnapshot({
      upstreamUrl: input.upstreamUrl,
      statusCode: input.upstreamResponse.status,
      headers: input.upstreamResponse.headers,
      bodyText: input.responseBodyText,
      errorMessage: message
    }),
    errorMessage: message
  })

  if (shouldRetryResponseInspectionDecisionOnServer(decision, input.res)) {
    input.auditCapture.addGatewayMetadata({
      label: 'response_inspection_server_retry',
      metadata: responseInspectionAuditMetadata(decision)
    })
    return {
      alreadyFinalized: false,
      retryUpstream: true,
      retryReason: 'response_inspection',
      responseInspection: decision,
      excludeCurrentAccount: shouldExcludeCurrentAccountForStreamServerRetry(decision),
      message,
      errorCode
    }
  }

  const responsePayload = gatewayErrorPayload(message, 'response_inspection_failed', errorCode)
  sendGatewayErrorResponse(input.res, 503, responsePayload)
  input.auditCapture.finalize({
    outcome: 'upstream_failed',
    success: false,
    statusCode: 503,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody: JSON.stringify(responsePayload),
    responsePartType: 'gateway_error',
    errorPhase: 'response_inspection',
    errorCode,
    errorMessage: message,
    accountId: input.account.id,
    firstTokenMs: input.firstTokenMs
  })
  return { alreadyFinalized: true }
}
