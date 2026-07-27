import type { Request, Response } from 'express'

import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import {
  gatewayClientAllowsUpstreamSemanticInterpretation,
  type OpenAIGatewayClientStrategyContext
} from '../client-profiles/strategy.js'
import {
  responseHeadersToObject,
  type AuditCaptureContext
} from '../audit/capture.service.js'
import {
  codexResponsesGuardAuditMetadata,
  responseInspectionAuditMetadata
} from '../audit/metadata.js'
import {
  codexResponsesGuardUsageSummary,
  createCodexResponsesResponseGuard,
  type CodexResponsesGuardJsonResult,
  type CodexResponsesGuardUsageSummary
} from '../codex-responses/response-guard.js'
import { resolveCodexResponsesGuardMode } from '../codex-responses/account-policy.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  extractGatewayProtocolJsonSemanticFramesForRequest,
  gatewayProtocolClientErrorProtocolForRequest,
  gatewayProtocolDefaultClientProfileForRequest,
  gatewayProtocolResponseEndpointFamilyForRequest,
  parseGatewayProtocolUsageFromJsonTextFragmentForRequest,
  parseGatewayProtocolUsageFromJsonValueForRequest
} from '../protocols/registry.js'
import {
  forgetOpenAIAccountForSessionAsync
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
  shouldExcludeCurrentAccountForStreamServerRetry
} from './stream-finalization-retry-decision.js'
import type { GatewayDownstreamCommitState } from './downstream-commit-state.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { dispatchRequestFailureAccountHealthCheck } from './request-failure-health-check.js'
import {
  parseGatewayNonStreamJsonBody,
  type GatewayNonStreamJsonBody
} from './non-stream-json-body.js'
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
  parsedJsonBody?: GatewayNonStreamJsonBody
  firstTokenMs?: number
  responseInspectionPolicies?: ResponseInspectionPolicySummary[]
  clientStrategy?: OpenAIGatewayClientStrategyContext
  accountStateMutationEnabled: boolean
  automaticAccountStateMutationEnabled: boolean
  protocolValidationEnabled: boolean
  downstreamCommitState: GatewayDownstreamCommitState
  onCodexResponsesGuardResult?: (result: CodexResponsesGuardJsonResult) => void
  sessionAffinityKey?: string
}): Promise<UpstreamResponseHandlingResult | undefined> {
  const parsedJsonBody = input.parsedJsonBody
    ?? parseGatewayNonStreamJsonBody(input.responseBody.length > 0 ? input.responseBodyText : undefined, input.upstreamResponse.headers)
  if (parsedJsonBody.status !== 'valid') {
    const guardResult = inspectCodexResponsesJsonAtMarkedBoundary(input, {})
    if (guardResult) input.onCodexResponsesGuardResult?.(guardResult)
    if (
      guardResult?.mode === 'strict_intercept'
      && (guardResult.outcome === 'repairable' || guardResult.outcome === 'blocked')
    ) {
      return finalizeBufferedJsonProtocolFailure({
        ...input,
        parsedJsonBody,
        codexResponsesGuard: codexResponsesGuardUsageSummary(guardResult)
      }, {
        message: codexResponsesStrictInterceptMessage(guardResult),
        errorCode: 'codex_responses_protocol_intercepted'
      })
    }
    if (guardResult?.mode === 'safe_repair' && guardResult.outcome === 'blocked' && guardResult.retryable) {
      return finalizeBufferedJsonProtocolFailure({
        ...input,
        parsedJsonBody,
        accountStateMutationEnabled: false,
        codexResponsesGuard: codexResponsesGuardUsageSummary(guardResult)
      }, {
        message: codexResponsesBlockedMessage(guardResult),
        errorCode: 'codex_responses_protocol_blocked'
      })
    }
    return undefined
  }
  const parsedJson = parsedJsonBody.value
  const guardResult = inspectCodexResponsesJsonAtMarkedBoundary(input, parsedJson)
  if (guardResult) input.onCodexResponsesGuardResult?.(guardResult)
  if (
    guardResult?.mode === 'strict_intercept'
    && (guardResult.outcome === 'repairable' || guardResult.outcome === 'blocked')
  ) {
    return finalizeBufferedJsonProtocolFailure({
      ...input,
      parsedJsonBody,
      codexResponsesGuard: codexResponsesGuardUsageSummary(guardResult)
    }, {
      message: codexResponsesStrictInterceptMessage(guardResult),
      errorCode: 'codex_responses_protocol_intercepted'
    })
  }
  if (guardResult?.mode === 'safe_repair' && guardResult.outcome === 'blocked' && guardResult.retryable) {
    return finalizeBufferedJsonProtocolFailure({
      ...input,
      parsedJsonBody,
      accountStateMutationEnabled: false,
      codexResponsesGuard: codexResponsesGuardUsageSummary(guardResult)
    }, {
      message: codexResponsesBlockedMessage(guardResult),
      errorCode: 'codex_responses_protocol_blocked'
    })
  }
  const protocolFailure = input.protocolValidationEnabled
    ? validateBufferedJsonProtocolResponse(parsedJson, input)
    : undefined
  if (protocolFailure) {
    return finalizeBufferedJsonProtocolFailure({
      ...input,
      parsedJsonBody,
      accountStateMutationEnabled: input.automaticAccountStateMutationEnabled
    }, protocolFailure)
  }
  if (isGatewayGeneratedResponsesFailure(parsedJson, input)) return undefined
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  if (!interpretUpstreamResponseSemantics && (input.responseInspectionPolicies?.length ?? 0) === 0) return undefined
  const defaultClientProfile = gatewayProtocolDefaultClientProfileForRequest(input.req, input.account)
  const context = {
    clientProfile: input.clientStrategy?.clientProfile ?? defaultClientProfile,
    accountClientCompatibility: input.account.clientCompatibility,
    codexCompactionExpected: input.clientStrategy?.codexCompactionExpected
  }
  if (context.clientProfile === 'generic_anthropic' && (input.responseInspectionPolicies?.length ?? 0) === 0) {
    return undefined
  }
  const frames = extractGatewayProtocolJsonSemanticFramesForRequest(parsedJson, input.req, input.account)
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
  const clientErrorProtocol = gatewayProtocolClientErrorProtocolForRequest(input.req, input.account)

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
    input.accountStateMutationEnabled,
    input.usageContext
  )
  if (!inspection.decision) return undefined

  const decision = inspection.decision
  await applyResponseInspectionPolicyRuntimeSideEffects(decision, input.account, input.settings, input.accountStateMutationEnabled, input.usageContext)
  input.auditCapture.addGatewayMetadata({
    label: 'response_inspection',
    metadata: responseInspectionAuditMetadata(decision)
  })
  const usage = parseGatewayProtocolUsageFromJsonValueForRequest(input.req, input.account, parsedJson)
  const message = decision.upstreamErrorMessage ?? decision.rewriteMessage ?? `JSON 响应命中检查策略：${decision.policyName ?? decision.policyId ?? '未命名策略'}`
  const errorCode = decision.rewriteErrorCode ?? decision.upstreamErrorCode ?? 'response_inspection_matched'
  await forgetOpenAIAccountForSessionAsync(input.sessionAffinityKey, input.account.id)
  input.auditCapture.completeAttempt(input.auditAttemptId, {
    statusCode: input.upstreamResponse.status,
    responseHeaders: input.upstreamResponse.headers,
    responseBody: input.responseBody,
    success: false,
    errorPhase: 'response_inspection',
    errorCode,
    errorMessage: message
  })
  await recordCompletedUpstreamAttempt(input.req, {
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

  const shouldRetryOnServer = decision.retryEnabled
  if (shouldRetryOnServer && !input.res.headersSent && !input.res.writableEnded && !input.res.destroyed) {
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

export function inspectCodexResponsesJsonAtMarkedBoundary(
  input: {
    req: Request
    upstreamResponse: GatewayUpstreamResponse
    auditCapture: AuditCaptureContext
    clientStrategy?: OpenAIGatewayClientStrategyContext
    downstreamCommitState: GatewayDownstreamCommitState
    account?: UpstreamAccount
    guardMode?: 'off' | 'shadow' | 'safe_repair' | 'strict_intercept'
  },
  parsedJson: unknown
): CodexResponsesGuardJsonResult | undefined {
  const marker = input.upstreamResponse.codexResponsesGuardMarker
  const mode = input.guardMode ?? resolveCodexResponsesGuardMode({
    globalMode: runtimeConfig.codexProtocolGuard.mode,
    credentials: input.account?.credentials
  })
  if (
    mode === 'off'
    ||
    !input.upstreamResponse.ok
    || !marker
    || input.clientStrategy?.clientProfile !== 'codex'
    || gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account) !== 'responses'
  ) return undefined
  const guard = createCodexResponsesResponseGuard({
    marker,
    downstreamCommitState: input.downstreamCommitState,
    mode,
    envelopeKind: isResponsesCompactRequest(input.req) ? 'compact' : 'response'
  })
  try {
    const result = guard.inspectJson(plainObject(parsedJson) ?? {})
    if (result.outcome !== 'clean') {
      input.auditCapture.addGatewayMetadata({
        label: 'codex_responses_protocol_guard',
        metadata: codexResponsesGuardAuditMetadata(result)
      })
    }
    return result
  } finally {
    guard.dispose()
  }
}

function codexResponsesStrictInterceptMessage(result: CodexResponsesGuardJsonResult): string {
  const codes = [...new Set(result.issues.map((issue) => issue.code))]
  const detail = codes.length > 0 ? `：${codes.join(', ')}` : ''
  return `Codex Responses 响应协议异常，严格模式已拦截并请求更换账户${detail}`
}

function codexResponsesBlockedMessage(result: CodexResponsesGuardJsonResult): string {
  const codes = [...new Set(result.issues.map((issue) => issue.code))]
  const detail = codes.length > 0 ? `：${codes.join(', ')}` : ''
  return `Codex Responses 响应协议异常，安全模式已阻止本次响应并请求下一账户${detail}`
}

function isResponsesCompactRequest(req: Request): boolean {
  const path = (req.originalUrl || req.path || '').split('?', 1)[0].replace(/^\/v1(?=\/|$)/, '') || '/'
  return req.method?.toUpperCase() === 'POST' && path === '/responses/compact'
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
  if (endpointFamily === 'chat_completions' && (!Array.isArray(root.choices) || root.choices.length === 0)) {
    return {
      message: '上游 Chat JSON 响应结构无效：choices 必须是非空数组',
      errorCode: 'upstream_protocol_error'
    }
  }
  if (endpointFamily === 'messages' && root.type === 'message' && (!Array.isArray(root.content) || root.content.length === 0)) {
    return {
      message: '上游 Anthropic Messages JSON 响应结构无效：content 必须是非空数组',
      errorCode: 'upstream_protocol_error'
    }
  }
  return undefined
}

function isGatewayGeneratedResponsesFailure(
  parsedJson: unknown,
  input: {
    req: Request
    account: UpstreamAccount
  }
): boolean {
  const root = plainObject(parsedJson)
  if (!root) return false
  if (root.status !== 'failed') return false
  const endpointFamily = gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account)
  if (endpointFamily !== 'responses') return false
  const metadata = plainObject(root.metadata)
  return metadata?.gateway_generated_failure === true
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
    parsedJsonBody: GatewayNonStreamJsonBody
    firstTokenMs?: number
    settings: GatewaySettings
    accountStateMutationEnabled: boolean
    codexResponsesGuard?: CodexResponsesGuardUsageSummary
    sessionAffinityKey?: string
  },
  failure: { message: string; errorCode: string }
): Promise<UpstreamResponseHandlingResult> {
  const responsePayload = gatewayErrorPayload(failure.message, 'upstream_response_error', failure.errorCode)
  const usage = input.parsedJsonBody.status === 'valid'
    ? parseGatewayProtocolUsageFromJsonValueForRequest(input.req, input.account, input.parsedJsonBody.value)
    : parseGatewayProtocolUsageFromJsonTextFragmentForRequest(input.req, input.account, input.responseBodyText, true)
  await forgetOpenAIAccountForSessionAsync(input.sessionAffinityKey, input.account.id)
  input.auditCapture.completeAttempt(input.auditAttemptId, {
    statusCode: input.upstreamResponse.status,
    responseHeaders: input.upstreamResponse.headers,
    responseBody: input.responseBody,
    success: false,
    errorPhase: 'upstream_response',
    errorCode: failure.errorCode,
    errorMessage: failure.message
  })
  await recordCompletedUpstreamAttempt(input.req, {
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
      errorMessage: failure.message,
      codexResponsesGuard: input.codexResponsesGuard
    }),
    errorMessage: failure.message
  })
  dispatchRequestFailureAccountHealthCheck(input.req, input.usageContext.trafficSource, input.account.id)
  // A complete 2xx response with an invalid protocol shape is request-local
  // evidence. The request may fail over, while shared account state remains
  // gated by the independent fixed-model health confirmation.
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
  const clientErrorProtocol = gatewayProtocolClientErrorProtocolForRequest(input.req, input.account)
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
