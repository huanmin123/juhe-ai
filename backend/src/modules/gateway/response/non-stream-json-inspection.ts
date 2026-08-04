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
  responseInspectionAuditMetadata
} from '../audit/metadata.js'
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
  resolveRuntimeResponseInspectionPolicies,
  type ResponseInspectionDecision
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
import type { GatewayDownstreamCommitState } from './downstream-commit-state.js'
import { dispatchRequestFailureAccountHealthCheck } from './request-failure-health-check.js'
import {
  parseGatewayNonStreamJsonBody,
  type GatewayNonStreamJsonBody
} from './non-stream-json-body.js'
import { isSuccessfulEmptyUpstreamResponseAllowed } from './empty-upstream-response.js'
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
  protocolValidationLimitExceeded?: boolean
  downstreamCommitState: GatewayDownstreamCommitState
  sessionAffinityKey?: string
}): Promise<UpstreamResponseHandlingResult | undefined> {
  const parsedJsonBody = input.parsedJsonBody
    ?? parseGatewayNonStreamJsonBody(input.responseBody.length > 0 ? input.responseBodyText : undefined, input.upstreamResponse.headers)
  if (
    parsedJsonBody.status === 'valid'
    && isCodexResponsesCyberPolicyFailedJson({
      req: input.req,
      account: input.account,
      clientStrategy: input.clientStrategy,
      upstreamStatus: input.upstreamResponse.status,
      parsedJson: parsedJsonBody.value
    })
  ) return undefined
  if (parsedJsonBody.status !== 'valid') {
    const emptySuccessAllowed = parsedJsonBody.status === 'empty'
      && isSuccessfulEmptyUpstreamResponseAllowed({
        req: input.req,
        account: input.account,
        statusCode: input.upstreamResponse.status
      })
    const protocolFailure = !emptySuccessAllowed && input.protocolValidationEnabled
      ? validateBufferedJsonProtocolResponse(parsedJsonBody, input)
      : undefined
    if (protocolFailure) {
      return finalizeBufferedJsonProtocolFailure({
        ...input,
        parsedJsonBody,
        accountStateMutationEnabled: input.automaticAccountStateMutationEnabled
      }, protocolFailure)
    }
    return undefined
  }
  const parsedJson = parsedJsonBody.value
  if (isGatewayGeneratedResponsesFailure(parsedJson, input)) return undefined
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  if (!interpretUpstreamResponseSemantics && (input.responseInspectionPolicies?.length ?? 0) === 0) {
    const protocolFailure = input.protocolValidationEnabled
      ? validateBufferedJsonProtocolResponse(parsedJsonBody, input)
      : undefined
    if (protocolFailure) {
      return finalizeBufferedJsonProtocolFailure({
        ...input,
        parsedJsonBody,
        accountStateMutationEnabled: input.automaticAccountStateMutationEnabled
      }, protocolFailure)
    }
    return undefined
  }
  const defaultClientProfile = gatewayProtocolDefaultClientProfileForRequest(input.req, input.account)
  const context = {
    clientProfile: input.clientStrategy?.clientProfile ?? defaultClientProfile,
    accountClientCompatibility: input.account.clientCompatibility,
    codexCompactionExpected: input.clientStrategy?.codexCompactionExpected
  }
  if (context.clientProfile === 'generic_anthropic' && (input.responseInspectionPolicies?.length ?? 0) === 0) {
    const protocolFailure = input.protocolValidationEnabled
      ? validateBufferedJsonProtocolResponse(parsedJsonBody, input)
      : undefined
    if (protocolFailure) {
      return finalizeBufferedJsonProtocolFailure({
        ...input,
        parsedJsonBody,
        accountStateMutationEnabled: input.automaticAccountStateMutationEnabled
      }, protocolFailure)
    }
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
  if (frames.length === 0) {
    const protocolFailure = input.protocolValidationEnabled
      ? validateBufferedJsonProtocolResponse(parsedJsonBody, input)
      : undefined
    if (protocolFailure) {
      return finalizeBufferedJsonProtocolFailure({
        ...input,
        parsedJsonBody,
        accountStateMutationEnabled: input.automaticAccountStateMutationEnabled
      }, protocolFailure)
    }
    return undefined
  }
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
  if (!inspection.decision) {
    const protocolFailure = input.protocolValidationEnabled
      ? validateBufferedJsonProtocolResponse(parsedJsonBody, input)
      : undefined
    if (protocolFailure) {
      return finalizeBufferedJsonProtocolFailure({
        ...input,
        parsedJsonBody,
        accountStateMutationEnabled: input.automaticAccountStateMutationEnabled
      }, protocolFailure)
    }
    return undefined
  }

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

  if (
    (decision.replayAuthority === 'explicit_user_policy' || decision.replayAuthority === 'system_default_retry_next_account')
    && (
      decision.accountSwitch === 'request_next_account'
      || decision.accountSwitch === 'avoid_account_ttl'
      || decision.accountSwitch === 'avoid_upstream_bucket_ttl'
    )
    && decision.retryEnabled === true
    && !input.downstreamCommitState.semanticCommitted
    && input.downstreamCommitState.downstreamBytesWritten === 0
    && !input.res.headersSent
    && !input.res.writableEnded
    && !input.res.destroyed
  ) {
    return {
      alreadyFinalized: false,
      retryUpstream: true,
      retryReason: 'response_inspection',
      responseInspection: decision,
      excludeCurrentAccount: shouldExcludeCurrentAccountForJsonRetry(decision),
      message,
      errorCode
    }
  }

  const responsePayload = gatewayErrorPayload(message, 'response_inspection_failed', errorCode)
  const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, clientErrorProtocol)
  sendGatewayErrorResponse(input.res, 502, responsePayload, { protocol: clientErrorProtocol })
  input.auditCapture.finalize({
    outcome: 'upstream_failed',
    success: false,
    statusCode: 502,
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

function shouldExcludeCurrentAccountForJsonRetry(decision: ResponseInspectionDecision): boolean {
  return decision.accountSwitch === 'request_next_account'
    || decision.accountSwitch === 'avoid_account_ttl'
    || decision.accountSwitch === 'avoid_upstream_bucket_ttl'
    || decision.accountState === 'runtime_avoidance'
}

function validateBufferedJsonProtocolResponse(
  parsedJsonBody: GatewayNonStreamJsonBody,
  input: {
    req: Request
    account: UpstreamAccount
    upstreamResponse: GatewayUpstreamResponse
    protocolValidationLimitExceeded?: boolean
  }
): { message: string; errorCode: string } | undefined {
  if (!input.upstreamResponse.ok) return undefined
  if (input.protocolValidationLimitExceeded) {
    return {
      message: '上游成功响应超过网关协议验证上限，已拒绝透传未验证正文',
      errorCode: 'upstream_protocol_error'
    }
  }
  if (parsedJsonBody.status !== 'valid') {
    return {
      message: '上游成功响应不是有效 JSON，无法满足请求协议',
      errorCode: 'upstream_protocol_error'
    }
  }
  const parsedJson = parsedJsonBody.value
  const root = plainObject(parsedJson)
  if (!root) {
    return {
      message: '上游 JSON 响应根节点无效，无法满足请求协议',
      errorCode: 'upstream_protocol_error'
    }
  }
  const endpointFamily = gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account)
  const requestPath = (input.req.originalUrl || input.req.path || '').split('?', 1)[0].toLowerCase()
  const resourceResponse = isManagementResourceResponsePath(requestPath)
  const responseError = plainObject(root.error)
  if (!resourceResponse && endpointFamily !== 'responses' && (responseError || root.type === 'error' || root.status === 'failed')) {
    const upstreamMessage = typeof responseError?.message === 'string' ? responseError.message.trim() : ''
    return {
      message: upstreamMessage
        ? `上游成功 HTTP 响应包含失败终态：${upstreamMessage}`
        : '上游成功 HTTP 响应包含失败终态，无法满足请求协议',
      errorCode: 'upstream_protocol_error'
    }
  }
  if (
    endpointFamily === 'chat_completions'
    && (!Array.isArray(root.choices) || !root.choices.some(isValidChatCompletionChoice))
  ) {
    return {
      message: '上游 Chat JSON 响应结构无效：choices 必须包含 message 或 text',
      errorCode: 'upstream_protocol_error'
    }
  }
  if (endpointFamily === 'messages' && (root.type !== 'message' || !Array.isArray(root.content))) {
    return {
      message: '上游 Anthropic Messages JSON 响应结构无效：content 必须是数组',
      errorCode: 'upstream_protocol_error'
    }
  }
  if (
    endpointFamily === 'models'
    && (!Array.isArray(root.data) && !Array.isArray(root.models) && root.object !== 'model' && typeof root.name !== 'string')
  ) {
    return {
      message: '上游 Models JSON 响应结构无效：缺少 data、models、model 或 name',
      errorCode: 'upstream_protocol_error'
    }
  }
  if (endpointFamily === 'message_token_counting' && typeof root.input_tokens !== 'number') {
    return protocolStructureFailure('上游 Anthropic Token Counting JSON 响应结构无效：缺少 input_tokens')
  }
  if (
    (endpointFamily === 'generate_content' || endpointFamily === 'stream_generate_content')
    && !Array.isArray(root.candidates)
    && !plainObject(root.promptFeedback)
  ) {
    return protocolStructureFailure('上游 Gemini Generate Content JSON 响应结构无效：缺少 candidates 或 promptFeedback')
  }
  if (endpointFamily === 'count_tokens' && typeof root.totalTokens !== 'number') {
    return protocolStructureFailure('上游 Gemini Count Tokens JSON 响应结构无效：缺少 totalTokens')
  }
  if (endpointFamily === 'embed_content' && !plainObject(root.embedding) && !Array.isArray(root.embeddings)) {
    return protocolStructureFailure('上游 Gemini Embed Content JSON 响应结构无效：缺少 embedding 或 embeddings')
  }
  if (endpointFamily === 'interactions' && typeof root.id !== 'string' && typeof root.name !== 'string') {
    return protocolStructureFailure('上游 Gemini Interactions JSON 响应结构无效：缺少 id 或 name')
  }
  if (endpointFamily === 'unknown') {
    if ((requestPath.includes('/embeddings') || requestPath.includes('/images')) && !Array.isArray(root.data)) {
      return protocolStructureFailure('上游 JSON 响应结构无效：data 必须是数组')
    }
    if (requestPath.includes('/moderations') && !Array.isArray(root.results)) {
      return protocolStructureFailure('上游 Moderations JSON 响应结构无效：results 必须是数组')
    }
    if (/\/audio\/(?:transcriptions|translations)$/.test(requestPath) && typeof root.text !== 'string') {
      return protocolStructureFailure('上游 Audio JSON 响应结构无效：缺少 text')
    }
    if (
      (/\/(?:batches|fine_tuning|vector_stores)(?:\/|$)/.test(requestPath) || /^(?:\/v1)?\/files(?:\/|$)/.test(requestPath))
      && typeof root.id !== 'string'
      && !Array.isArray(root.data)
    ) {
      return protocolStructureFailure('上游管理接口 JSON 响应结构无效：缺少 id 或 data')
    }
  }
  if (endpointFamily === 'responses') {
    if (root.status === 'failed') {
      const error = plainObject(root.error)
      const upstreamMessage = typeof error?.message === 'string' ? error.message.trim() : ''
      return {
        message: upstreamMessage
          ? `上游 Responses 返回失败终态：${upstreamMessage}`
          : '上游 Responses 返回失败终态',
        errorCode: 'upstream_protocol_failure'
      }
    }
    if ((root.object !== 'response' && root.type !== 'response') || typeof root.id !== 'string' || !Array.isArray(root.output)) {
      return {
        message: '上游 Responses JSON 响应结构无效，缺少 response、id 或 output',
        errorCode: 'upstream_protocol_error'
      }
    }
  }
  return undefined
}

function isManagementResourceResponsePath(requestPath: string): boolean {
  return /\/(?:batches|fine_tuning|vector_stores)(?:\/|$)/.test(requestPath)
    || /^(?:\/v1)?\/files(?:\/|$)/.test(requestPath)
}

function isValidChatCompletionChoice(value: unknown): boolean {
  const choice = plainObject(value)
  if (!choice || plainObject(choice.error)) return false
  return (plainObject(choice.message) && !plainObject(plainObject(choice.message)?.error))
    || typeof choice.text === 'string'
}

function protocolStructureFailure(message: string): { message: string; errorCode: string } {
  return {
    message,
    errorCode: 'upstream_protocol_error'
  }
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

export function isCodexResponsesCyberPolicyFailedJson(input: {
  req: Request
  account: UpstreamAccount
  clientStrategy?: OpenAIGatewayClientStrategyContext
  upstreamStatus: number
  parsedJson: unknown
}): boolean {
  return isCodexResponsesNonRetryableFailedJson(input, new Set(['cyber_policy']))
}

function isCodexResponsesNonRetryableFailedJson(input: {
  req: Request
  account: UpstreamAccount
  clientStrategy?: OpenAIGatewayClientStrategyContext
  upstreamStatus: number
  parsedJson: unknown
}, errorCodes = new Set(['cyber_policy'])): boolean {
  if (input.upstreamStatus >= 200 && input.upstreamStatus < 300) return false
  if (gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account) !== 'responses') return false
  const clientProfile = input.clientStrategy?.clientProfile
    ?? gatewayProtocolDefaultClientProfileForRequest(input.req, input.account)
  if (clientProfile !== 'codex') return false
  const root = plainObject(input.parsedJson)
  const error = plainObject(root?.error)
  return (root?.status === 'failed' || root?.status === undefined)
    && typeof error?.code === 'string'
    && errorCodes.has(error.code)
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
      errorMessage: failure.message
    }),
    errorMessage: failure.message
  })
  dispatchRequestFailureAccountHealthCheck(input.req, input.usageContext.trafficSource, input.account.id)
  // A complete but invalid 2xx response is a concrete failure of this
  // attempt.  Return its protocol diagnosis instead of hiding it behind a
  // second account's response.
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
