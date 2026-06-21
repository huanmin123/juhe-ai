import type { Request, Response } from 'express'

import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import {
  responseHeadersToObject,
  type AuditCaptureContext
} from '../audit/capture.service.js'
import { responseInspectionAuditMetadata } from '../audit/metadata.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  extractGatewayProtocolJsonSemanticFrames,
  gatewayProtocolClientErrorProtocolForProfile,
  gatewayProtocolDefaultClientProfileForProfile,
  gatewayProtocolResponseEndpointFamilyForRequest,
  parseGatewayProtocolUsageFromJsonBuffer
} from '../protocols/registry.js'
import {
  forgetOpenAIAccountForSession
} from '../runtime/session-affinity.service.js'
import {
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from '../runtime/account-side-effects.service.js'
import { requestEndpoint } from '../request/metadata.js'
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
  codexCompactionContractMismatchFrame,
  countCodexCompactionOutputItemsFromJson
} from './codex-compaction-contract.js'
import {
  applyResponseInspectionObservationDecisions,
  applyResponseInspectionPolicyRuntimeSideEffects
} from './inspection-runtime-effects.js'
import type { UpstreamResponseHandlingResult } from './response-handling-result.js'
import {
  gatewayErrorPayload,
  gatewayErrorPayloadForProtocol,
  sendGatewayErrorResponse
} from './responses.js'
import {
  shouldExcludeCurrentAccountForStreamServerRetry,
  shouldRetryResponseInspectionDecisionOnServer
} from './stream-finalization-retry-decision.js'
export async function inspectBufferedGatewayJsonResponse(input: {
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
  clientStrategy?: OpenAIGatewayClientStrategyContext
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
  const protocolFailure = validateBufferedJsonProtocolResponse(parsedJson, input)
  if (protocolFailure) {
    return finalizeBufferedJsonProtocolFailure(input, protocolFailure)
  }
  const defaultClientProfile = gatewayProtocolDefaultClientProfileForProfile(input.account)
  const context = {
    clientProfile: input.clientStrategy?.clientProfile ?? defaultClientProfile,
    accountClientCompatibility: input.account.clientCompatibility,
    codexCompactionExpected: input.clientStrategy?.codexCompactionExpected
  }
  const frames = extractGatewayProtocolJsonSemanticFrames(parsedJson, input.req, input.account)
  if (
    context.codexCompactionExpected === true
    && context.clientProfile === 'codex'
    && context.accountClientCompatibility === 'codex_responses'
  ) {
    const counts = countCodexCompactionOutputItemsFromJson(parsedJson)
    const mismatchFrame = counts
      ? codexCompactionContractMismatchFrame({ ...counts, transport: 'json' })
      : codexCompactionContractMismatchFrame({ outputItemCount: 0, compactionItemCount: 0, transport: 'json' })
    if (mismatchFrame) {
      frames.push(mismatchFrame)
    }
  }
  if (frames.length === 0) return undefined
  const clientErrorProtocol = gatewayProtocolClientErrorProtocolForProfile(input.account)

  const policies = resolveRuntimeResponseInspectionPolicies({
    account: input.account,
    managementPolicies: input.responseInspectionPolicies
  })
  const inspection = inspectResponseSemanticFrames({
    frames,
    policies,
    downstreamWritten: false,
    transport: 'json',
    context
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
  const usage = parseGatewayProtocolUsageFromJsonBuffer(input.account, input.responseBody)
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
  const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, clientErrorProtocol)
  sendGatewayErrorResponse(input.res, 503, responsePayload, { protocol: clientErrorProtocol })
  input.auditCapture.finalize({
    outcome: 'upstream_failed',
    success: false,
    statusCode: 503,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody: JSON.stringify(clientPayload),
    responsePartType: 'gateway_error',
    errorPhase: 'response_inspection',
    errorCode,
    errorMessage: message,
    accountId: input.account.id,
    firstTokenMs: input.firstTokenMs
  })
  return { alreadyFinalized: true }
}

function validateBufferedJsonProtocolResponse(
  parsedJson: unknown,
  input: {
    req: Request
    account: UpstreamAccount
    upstreamResponse: GatewayUpstreamResponse
  }
): { message: string; errorCode: string } | undefined {
  if (!input.upstreamResponse.ok) return undefined
  const root = plainObject(parsedJson)
  if (!root) return undefined
  const endpointFamily = gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account)
  if (endpointFamily !== 'chat_completions') return undefined
  if (!Array.isArray(root.choices) || root.choices.length === 0) {
    return {
      message: '上游 Chat JSON 响应结构无效：choices 必须是非空数组',
      errorCode: 'upstream_protocol_error'
    }
  }
  return undefined
}

async function finalizeBufferedJsonProtocolFailure(
  input: {
    req: Request
    res: Response
    account: UpstreamAccount
    upstreamResponse: GatewayUpstreamResponse
    upstreamUrl: string
    auditAttemptId: string
    auditCapture: AuditCaptureContext
    usageContext: GatewayUsageContext
    startedAt: number
    responseBody: Buffer
    responseBodyText: string
    firstTokenMs?: number
    settings: GatewaySettings
    accountStateMutationEnabled: boolean
    sessionAffinityKey?: string
  },
  failure: { message: string; errorCode: string }
): Promise<UpstreamResponseHandlingResult> {
  const responsePayload = gatewayErrorPayload(failure.message, 'upstream_response_error', failure.errorCode)
  const usage = parseGatewayProtocolUsageFromJsonBuffer(input.account, input.responseBody)
  forgetOpenAIAccountForSession(input.sessionAffinityKey, input.account.id)
  input.auditCapture.completeAttempt(input.auditAttemptId, {
    statusCode: input.upstreamResponse.status,
    responseHeaders: input.upstreamResponse.headers,
    responseBody: input.responseBody,
    success: false,
    errorPhase: 'upstream_response',
    errorCode: failure.errorCode,
    errorMessage: failure.message
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
    errorCode: failure.errorCode,
    requestSnapshot: input.usageContext.requestSnapshot,
    responseSnapshot: buildUsageResponseSnapshot({
      upstreamUrl: input.upstreamUrl,
      statusCode: input.upstreamResponse.status,
      headers: input.upstreamResponse.headers,
      bodyText: input.responseBodyText,
      errorMessage: failure.message
    }),
    errorMessage: failure.message
  })
  if (input.accountStateMutationEnabled && input.usageContext.trafficSource === 'gateway' && !input.account.selectedApiKeyFingerprint) {
    const localSuppression = suppressGatewayAccountLocally(input.account, input.settings, failure.message)
    recordGatewayAccountFailureForPrecheck(input.account, input.settings, {
      systemAccountId: input.usageContext.systemAccountId,
      groupId: input.usageContext.groupId,
      apiKeyId: input.usageContext.apiKeyId,
      clientIp: input.usageContext.clientIp,
      endpoint: requestEndpoint(input.req),
      reason: failure.message,
      statusCode: input.upstreamResponse.status,
      forcePrecheck: localSuppression.action === 'precheck_required'
    })
    input.auditCapture.addGatewayMetadata({
      label: 'upstream_protocol_runtime_avoidance',
      metadata: {
        accountId: input.account.id,
        errorCode: failure.errorCode,
        localFailureCount: localSuppression.localFailureCount,
        delayMs: localSuppression.delayMs
      }
    })
  }
  if (!input.res.headersSent && !input.res.writableEnded && !input.res.destroyed) {
    input.auditCapture.addGatewayMetadata({
      label: 'upstream_protocol_server_retry',
      metadata: {
        accountId: input.account.id,
        errorCode: failure.errorCode
      }
    })
    return {
      alreadyFinalized: false,
      retryUpstream: true,
      retryReason: 'upstream_protocol_failure',
      excludeCurrentAccount: true,
      message: failure.message,
      errorCode: failure.errorCode
    }
  }
  const clientErrorProtocol = gatewayProtocolClientErrorProtocolForProfile(input.account)
  const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, clientErrorProtocol)
  sendGatewayErrorResponse(input.res, 502, responsePayload, { protocol: clientErrorProtocol })
  input.auditCapture.finalize({
    outcome: 'upstream_failed',
    success: false,
    statusCode: 502,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody: JSON.stringify(clientPayload),
    responsePartType: 'gateway_error',
    errorPhase: 'upstream_response',
    errorCode: failure.errorCode,
    errorMessage: failure.message,
    accountId: input.account.id,
    firstTokenMs: input.firstTokenMs
  })
  return { alreadyFinalized: true }
}

function plainObject(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}
