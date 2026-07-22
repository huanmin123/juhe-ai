import type { Request, Response } from 'express'

import { errorLogFields, logger } from '../../../shared/logger.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import { type GatewaySettings } from '../policy/account-error-policy.service.js'
import type { GatewayTimeoutProfile } from '../policy/timeout-profile.js'
import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  clearAccountStreamFailureStateWithCacheInvalidation,
  handleStreamFailure,
} from '../runtime/account-effects.js'
import { rememberCodexTurnStreamFailureAsync } from '../client-profiles/codex-turn-retry.service.js'
import { downstreamConnectionClosedMessage } from './client-abort.js'
import {
  gatewayClientAllowsUpstreamSemanticInterpretation,
  type OpenAIGatewayClientStrategyContext
} from '../client-profiles/strategy.js'
import {
  confirmClientIpAccountAvoidanceAfterFinalFailureAsync,
  confirmClientIpAccountAvoidanceAfterSuccessAsync,
  rememberClientIpAccountPendingFailure,
  type ClientIpAccountAvoidanceTracker
} from '../runtime/client-ip-account-avoidance.service.js'
import {
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from '../runtime/account-side-effects.service.js'
import { recordClientIpErrorCircuitSuccessAsync } from '../runtime/client-ip-error-circuit.service.js'
import {
  NonStreamUpstreamBodyPipeError,
  endResponse,
  pipeNonStreamUpstreamResponse,
  pipeNonStreamUpstreamResponseForInspection,
  nonStreamResponseCaptureBytes
} from '../upstream/body.js'
import {
  responseInspectionAuditMetadata
} from '../audit/metadata.js'
import {
  forgetOpenAIAccountForSessionAsync,
  forgetOpenAIAccountForSessionBestEffort
} from '../runtime/session-affinity.service.js'
import {
  gatewayStreamClientRetryErrorCode,
  gatewayStreamClientRetryMessage,
  isOpenAIJsonResponseContentType,
  writeGatewayStreamFailureEvent,
  type GatewayErrorProtocol
} from './responses.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  pipeUpstreamStream,
  type StreamFailureContext,
  type StreamBodyOmissionSummary
} from './stream.js'
import {
  inspectResponseSemanticFrames,
  resolveRuntimeResponseInspectionPolicies,
  type ResponseInspectionDecision
} from './inspection.js'
import {
  isEffectiveOpenAIStreamRequest,
  isUpstreamRequestAbortedError,
  UpstreamRequestAbortedError,
  type GatewayUpstreamResponse
} from '../upstream/request.js'
import { isGatewayFirstByteTimeoutError } from '../upstream/first-byte-timeout.js'
import type { FirstByteDeadlineHandler } from '../upstream/first-byte-deadline.js'
import {
  buildUsageResponseSnapshot,
  type UsageRequestSnapshot
} from '../usage/snapshots.js'
import {
  emptyUsage,
  type ParsedUsage
} from '../usage/types.js'
import { isAccountDiagnosticTrafficSource } from '../usage/traffic-source.js'
import {
  applyGatewayProtocolStreamUsageFallbackForRequest,
  extractGatewayProtocolJsonSemanticFramesForRequest,
  gatewayProtocolClientErrorProtocolForRequest,
  gatewayProtocolDefaultClientProfileForRequest,
  gatewayProtocolResponseEndpointFamilyForRequest,
  gatewayProtocolResponseProtocolForRequest,
  parseGatewayProtocolUsageFromJsonBufferForRequest,
  parseGatewayProtocolUsageFromJsonTextFragmentForRequest,
  parseGatewayProtocolErrorPayloadForRequest
} from '../protocols/registry.js'
import {
  requestModel
} from '../request/metadata.js'
import {
  estimateTokenCountFromText
} from '../protocols/openai-v1/stream-events.js'
import {
  recordGatewayUpstreamBucketSuccessAsync
} from '../runtime/proxy-health.service.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import type { HybridGatewayRuntimeRoute } from '../hybrid/routing.service.js'
import {
  inspectHybridGatewayQuality,
  type HybridQualityInspectionOutcome
} from '../hybrid/quality-inspection.service.js'
import {
  recordClientAbortedUpstreamAttempt,
  recordCompletedUpstreamAttempt,
  type GatewayUsageContext
} from '../usage/records.js'
import {
  preCommitStreamServerRetryErrorCode,
  shouldExcludeCurrentAccountForStreamServerRetry,
  shouldRememberCodexTurnStreamFailure,
  shouldRetryPreCommitStreamFailureOnServer,
  shouldRetryResponseInspectionOnServer,
  type StreamServerRetryReason
} from './stream-finalization-retry-decision.js'
import {
  applyResponseInspectionObservationDecisions,
  applyResponseInspectionPolicyRuntimeSideEffects
} from './inspection-runtime-effects.js'
import type { UpstreamResponseHandlingResult } from './response-handling-result.js'
import { inspectBufferedGatewayJsonResponse } from './non-stream-json-inspection.js'
import { prepareUpstreamResponseForDownstream } from './downstream-headers.js'
import type { GatewayDownstreamCommitState } from './downstream-commit-state.js'
import {
  deleteGeminiInteractionAffinityAsync,
  GeminiInteractionAffinityUnavailableError,
  geminiInteractionResourceIdFromRequest,
  geminiInteractionIdFromJsonPrefix,
  geminiInteractionIdFromResponseBody,
  isGeminiInteractionCreateRequest,
  isGeminiInteractionResourceRequest,
  rememberGeminiInteractionAffinityAsync
} from '../protocols/gemini-v1beta/interaction-affinity.service.js'

export type { StreamServerRetryReason } from './stream-finalization-retry-decision.js'
export type { UpstreamResponseHandlingResult } from './response-handling-result.js'

export function isSuccessfulEmptyUpstreamResponseAllowed(input: {
  req: Request
  account: UpstreamAccount
  statusCode: number
}): boolean {
  if (input.statusCode < 200 || input.statusCode >= 300) return false
  if (input.req.method.toUpperCase() !== 'DELETE') return false
  const requestPath = (input.req.originalUrl || input.req.path || '').split('?', 1)[0].toLowerCase()
  const normalizedPath = requestPath.replace(/^\/v1beta(?=\/|$)/, '') || '/'
  if (!/^\/interactions\/[^/]+$/.test(normalizedPath)) return false
  return gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account) === 'interactions'
}

interface HandleUpstreamResponseInput {
  req: Request
  res: Response
  account: UpstreamAccount
  upstreamResponse: GatewayUpstreamResponse
  upstreamUrl: string
  auditAttemptId: string
  auditCapture: AuditCaptureContext
  settings: GatewaySettings
  timeoutProfile: GatewayTimeoutProfile
  usageContext: GatewayUsageContext
  startedAt: number
  signal: AbortSignal
  firstByteTimeoutMs?: number
  firstByteDeadlineMs?: number
  onFirstByteDeadline?: FirstByteDeadlineHandler
  sessionAffinityKey?: string
  clientStrategy?: OpenAIGatewayClientStrategyContext
  responseInspectionPolicies?: ResponseInspectionPolicySummary[]
  hybridRoute?: HybridGatewayRuntimeRoute
  markFirstOutput?: () => void
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
  accountStateMutationEnabled?: boolean
  automaticAccountStateMutationEnabled?: boolean
  codexTurnAccountAvoidanceApplied?: boolean
  downstreamCommitState: GatewayDownstreamCommitState
}

interface FinalizeHandledUpstreamResponseInput extends HandleUpstreamResponseInput {
  result: Exclude<UpstreamResponseHandlingResult, { alreadyFinalized: true } | { retryUpstream: true }>
  completedAtMs?: number
}

const nonStreamResponseInspectionMaxBytes = 1024 * 1024

export async function handleStreamUpstreamResponse(input: HandleUpstreamResponseInput): Promise<UpstreamResponseHandlingResult> {
  const {
    req,
    res,
    account,
    upstreamResponse,
    upstreamUrl,
    auditAttemptId,
    auditCapture,
    settings,
    usageContext,
    startedAt,
    signal,
    sessionAffinityKey,
    clientStrategy,
    markFirstOutput,
    clientIpAccountAvoidanceTracker,
    accountStateMutationEnabled,
    automaticAccountStateMutationEnabled = accountStateMutationEnabled
  } = input

  const clientErrorProtocol = gatewayProtocolClientErrorProtocolForRequest(req, account)
  const responseProtocol = gatewayProtocolResponseProtocolForRequest(req, account)
  const responseEndpointFamily = gatewayProtocolResponseEndpointFamilyForRequest(req, account)
  const defaultClientProfile = gatewayProtocolDefaultClientProfileForRequest(req, account)
  const interpretUpstreamResponseSemantics = clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(clientStrategy)
    : false
  if (!upstreamResponse.body) {
    if (!interpretUpstreamResponseSemantics) {
      prepareUpstreamResponseForDownstream(res, upstreamResponse, true)
      input.downstreamCommitState.markTransportCommitted()
      endResponse(res)
      input.downstreamCommitState.markSemanticCommitted()
      return {
        alreadyFinalized: false,
        usage: emptyUsage(),
        firstTokenMs: Date.now() - startedAt,
        errorPayload: {}
      }
    }
    const errorMessage = '上游响应体为空'
    await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
    auditCapture.completeAttempt(auditAttemptId, {
      statusCode: upstreamResponse.status,
      responseHeaders: upstreamResponse.headers,
      success: false,
      errorPhase: 'upstream_response',
      errorCode: 'upstream_empty_body',
      errorMessage
    })
    await recordCompletedUpstreamAttempt(req, {
      ...usageContext,
      account,
      statusCode: upstreamResponse.status,
      success: false,
      stream: isEffectiveOpenAIStreamRequest(req, account),
      startedAt,
      usage: emptyUsage(),
      requestSnapshot: usageContext.requestSnapshot,
      responseSnapshot: buildUsageResponseSnapshot({
        upstreamUrl,
        statusCode: upstreamResponse.status,
        headers: upstreamResponse.headers,
        errorMessage
      }),
      errorCode: 'upstream_empty_body',
      errorMessage
    })
    rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
      statusCode: upstreamResponse.status,
      errorCode: 'upstream_empty_body',
      errorPhase: 'upstream_response',
      errorMessage,
      endpoint: usageContext.endpoint
    })
    if (automaticAccountStateMutationEnabled !== false) {
      const localSuppression = suppressGatewayAccountLocally(account, settings, errorMessage)
      if (usageContext.trafficSource === 'gateway') {
        recordGatewayAccountFailureForPrecheck(account, settings, {
          systemAccountId: usageContext.systemAccountId,
          groupId: usageContext.groupId,
          apiKeyId: usageContext.apiKeyId,
          clientIp: usageContext.clientIp,
          endpoint: usageContext.endpoint,
          reason: errorMessage,
          statusCode: upstreamResponse.status,
          forcePrecheck: localSuppression.action === 'precheck_required',
          localSuppressionDelayMs: localSuppression.delayMs
        })
      }
    }
    return {
      alreadyFinalized: false,
      retryUpstream: true,
      retryReason: 'upstream_protocol_failure',
      excludeCurrentAccount: true,
      message: errorMessage,
      errorCode: 'upstream_empty_body'
    }
  }

  let streamResult: Awaited<ReturnType<typeof pipeUpstreamStream>>
  const shouldMutateAccountForStreamFailure = (
    errorCode: string | undefined,
    context: StreamFailureContext
  ): boolean => {
    if (automaticAccountStateMutationEnabled === false) return false
    return !(
      (input.firstByteTimeoutMs !== undefined || input.firstByteDeadlineMs !== undefined)
      && errorCode === 'first_byte_timeout'
      && context.downstreamBytesWritten === 0
      && !context.outputReceived
    )
  }
  try {
    streamResult = await pipeUpstreamStream(
      upstreamResponse.body,
      res,
      input.timeoutProfile,
      startedAt,
      (message, errorCode, context) => handleStreamFailure(account, message, settings, errorCode, context, usageContext, shouldMutateAccountForStreamFailure(errorCode, context)),
      signal,
      {
        clientRetryEnabled: interpretUpstreamResponseSemantics
          && clientStrategy?.retryCoordination.committedFailureSignal === 'protocol_error_event',
        interpretProtocolFailures: interpretUpstreamResponseSemantics,
        retryBeforeDownstreamWriteUntilOutput: true,
        onFirstOutput: markFirstOutput,
        captureSuccessPayloads: auditCapture.shouldCaptureSuccessPayloads(),
        firstByteTimeoutMs: input.firstByteTimeoutMs,
        firstByteDeadlineMs: input.firstByteDeadlineMs,
        onFirstByteDeadline: input.onFirstByteDeadline,
        responseInspectionPolicies: runtimeResponseInspectionPoliciesForInput(input),
        responseInspectionContext: {
          clientProfile: clientStrategy?.clientProfile ?? defaultClientProfile,
          accountClientCompatibility: account.clientCompatibility,
          codexCompactionExpected: clientStrategy?.codexCompactionExpected
        },
        downstreamProtocol: clientStrategy?.downstreamProtocol,
        responseProtocol,
        endpointFamily: responseEndpointFamily,
        prepareDownstream: () => prepareUpstreamResponseForDownstream(res, upstreamResponse, true),
        downstreamCommitState: input.downstreamCommitState,
        beforeDownstreamCommit: isGeminiInteractionCreateRequest(req) && upstreamResponse.ok
          ? async ({ responseResourceId }) => {
            await rememberGeminiInteractionBeforeDownstreamCommit({
              auditCapture,
              account,
              usageContext,
              responseResourceId
            })
          }
          : undefined
      }
    )
  } catch (error) {
    if (isUpstreamRequestAbortedError(error) || signal.aborted) {
      await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
      await recordClientAbortedUpstreamAttempt(req, {
        ...usageContext,
        account,
        statusCode: upstreamResponse.status,
        stream: isEffectiveOpenAIStreamRequest(req, account),
        startedAt,
        requestSnapshot: usageContext.requestSnapshot,
        responseSnapshot: buildUsageResponseSnapshot({
          upstreamUrl,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          errorMessage: downstreamConnectionClosedMessage
        })
      })
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        success: false,
        errorPhase: 'client',
        errorMessage: downstreamConnectionClosedMessage
      })
    }
    throw error
  }

  const streamUsageFallback = applyGatewayProtocolStreamUsageFallbackForRequest(req, account, streamResult.usage, {
    completed: streamResult.completed,
    outputReceived: streamResult.outputReceived,
    estimatedOutputTokens: streamResult.estimatedOutputTokens
  })
  if (streamUsageFallback.estimated) {
    logger.warn({
      event: 'gateway_stream_usage_estimated',
      accountId: account.id,
      endpoint: usageContext.endpoint,
      model: requestModel(req),
      completed: streamResult.completed,
      outputReceived: streamResult.outputReceived,
      estimatedInputTokens: streamUsageFallback.estimatedInputTokens,
      estimatedOutputTokens: streamUsageFallback.estimatedOutputTokens
    }, '上游流式响应缺少 usage，网关已按可见输出估算 token 成本')
  }
  await applyResponseInspectionObservationHandling(streamResult, account, settings, auditCapture, accountStateMutationEnabled !== false, usageContext)
  if (streamResult.responseInspection) {
    await applyResponseInspectionPolicyRuntimeSideEffects(
      streamResult.responseInspection,
      account,
      settings,
      accountStateMutationEnabled !== false,
      usageContext
    )
    auditCapture.addGatewayMetadata({
      label: 'response_inspection',
      metadata: responseInspectionAuditMetadata(streamResult.responseInspection)
    })
  }
  if (streamResult.bodyOmission) {
    auditCapture.omitPayloadBodies({
      label: 'stream_body_omission',
      metadata: { ...streamResult.bodyOmission }
    })
  }
  auditCapture.completeAttempt(auditAttemptId, {
    statusCode: upstreamResponse.status,
    responseHeaders: upstreamResponse.headers,
    responseBody: streamResult.auditUpstreamBody,
    success: streamResult.completed && upstreamResponse.ok,
    errorPhase: streamResult.completed ? undefined : 'stream',
    errorCode: streamResult.completed ? undefined : streamResult.errorCode,
    errorMessage: streamResult.completed ? undefined : streamResult.message
  })
  if (!streamResult.completed) {
    const speedFirstFirstByteCutover = input.firstByteDeadlineMs !== undefined && isFirstByteTimeoutStreamResult(streamResult)
    const requestSnapshot = usageRequestSnapshotWithBodyOmission(usageContext.requestSnapshot, streamResult.bodyOmission)
    await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
    await recordCompletedUpstreamAttempt(req, {
      ...usageContext,
      account,
      statusCode: upstreamResponse.status,
      success: false,
      stream: isEffectiveOpenAIStreamRequest(req, account),
      firstTokenMs: streamResult.firstTokenMs,
      startedAt,
      usage: streamUsageFallback.usage,
      errorCode: streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode,
      requestSnapshot,
      responseSnapshot: buildUsageResponseSnapshot({
        upstreamUrl,
        statusCode: upstreamResponse.status,
        headers: upstreamResponse.headers,
        bodyText: streamResult.bodyOmission ? undefined : streamResult.responseBodyText,
        bodyOmission: streamResult.bodyOmission,
        errorMessage: streamResult.message
      }),
      errorMessage: streamResult.message
    })
    if (!speedFirstFirstByteCutover) {
      rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
        statusCode: upstreamResponse.status,
        errorCode: streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode,
        errorPhase: 'stream',
        errorMessage: streamResult.message,
        endpoint: usageContext.endpoint
      })
    }
    if (speedFirstFirstByteCutover) {
      auditCapture.addGatewayMetadata({
        label: 'normal_route_speed_first_confirmed_cutover',
        metadata: {
          accountId: account.id,
          accountName: account.name,
          timeoutMs: input.firstByteDeadlineMs,
          message: streamResult.message
        }
      })
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'speed_first_first_byte_timeout',
        excludeCurrentAccount: true,
        message: streamResult.message,
        errorCode: streamResult.errorCode,
        uncommittedResponseBody: streamResult.uncommittedResponseBody
      }
    }
    if (shouldRetryResponseInspectionOnServer(streamResult, res)) {
      auditCapture.addGatewayMetadata({
        label: 'response_inspection_server_retry',
        metadata: responseInspectionAuditMetadata(streamResult.responseInspection)
      })
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'response_inspection',
        responseInspection: streamResult.responseInspection,
        excludeCurrentAccount: shouldExcludeCurrentAccountForStreamServerRetry(streamResult.responseInspection),
        message: streamResult.message,
        errorCode: streamResult.responseInspection.upstreamErrorCode ?? streamResult.errorCode,
        uncommittedResponseBody: streamResult.uncommittedResponseBody
      }
    }
    if (shouldRetryPreCommitStreamFailureOnServer(streamResult, res)) {
      const clientFacingErrorCode = preCommitStreamServerRetryErrorCode(streamResult, clientStrategy)
      auditCapture.addGatewayMetadata({
        label: 'pre_commit_stream_server_retry',
        metadata: {
          errorCode: streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode,
          clientFacingErrorCode,
          message: streamResult.message,
          downstreamBytesWritten: streamResult.downstreamBytesWritten,
          outputReceived: streamResult.outputReceived,
          accountId: account.id
        }
      })
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'pre_commit_stream_failure',
        responseInspection: streamResult.responseInspection,
        excludeCurrentAccount: true,
        message: streamResult.message,
        errorCode: clientFacingErrorCode,
        uncommittedResponseBody: streamResult.uncommittedResponseBody
      }
    }
    if (
      !isAccountDiagnosticTrafficSource(usageContext.trafficSource)
      && shouldRememberCodexTurnStreamFailure(streamResult, clientStrategy)
    ) {
      const codexTurnFailure = await rememberCodexTurnStreamFailureAsync(clientStrategy, account.id, {
        errorCode: streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode,
        message: streamResult.message
      })
      if (codexTurnFailure) {
        auditCapture.addGatewayMetadata({
          label: 'codex_turn_stream_failure',
          metadata: {
            stateKey: codexTurnFailure.stateKey,
            failureCount: codexTurnFailure.failureCount,
            failedAccountIds: codexTurnFailure.failedAccountIds,
            accountId: account.id
          }
        })
      }
    }
    if (!isAccountDiagnosticTrafficSource(usageContext.trafficSource)) {
      const clientIpAvoidanceResult = await confirmClientIpAccountAvoidanceAfterFinalFailureAsync(
        clientIpAccountAvoidanceTracker,
        settings
      )
      if (clientIpAvoidanceResult.confirmedAccountIds.length > 0) {
        getRequestLogger().warn({
          event: 'gateway_client_ip_account_failure_confirmed_after_stream_failure',
          accountId: account.id,
          confirmedAccountIds: clientIpAvoidanceResult.confirmedAccountIds,
          systemAccountId: usageContext.systemAccountId,
          apiKeyId: usageContext.apiKeyId,
          groupId: usageContext.groupId,
          clientIp: usageContext.clientIp
        }, '流式失败即将返回客户端，客户端 IP 级账号回避状态已确认')
        auditCapture.addGatewayMetadata({
          label: 'client_ip_account_avoidance_update',
          metadata: {
            reason: 'stream_failure_finalized_to_client',
            confirmedAccountIds: clientIpAvoidanceResult.confirmedAccountIds
          }
        })
      }
    }
    const clientFailureResponseBody = writePreCommitStreamFailureToClient(
      res,
      upstreamResponse,
      streamResult,
      clientErrorProtocol,
      clientStrategy
    )
    auditCapture.finalize({
      outcome: 'stream_failed',
      success: false,
      statusCode: upstreamResponse.status,
      responseHeaders: responseHeadersToObject(res),
      responseBody: clientFailureResponseBody ?? streamResult.auditResponseBody,
      responsePartType: 'gateway_response',
      errorPhase: 'stream',
      errorCode: streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode,
      errorMessage: streamResult.message,
      accountId: account.id,
      firstTokenMs: streamResult.firstTokenMs
    })
    return { alreadyFinalized: true }
  }

  return {
    alreadyFinalized: false,
    usage: streamUsageFallback.usage,
    firstTokenMs: streamResult.firstTokenMs,
    responseBodyText: streamResult.responseBodyText,
    responseResourceId: streamResult.responseResourceId,
    bodyOmission: streamResult.bodyOmission,
    errorPayload: {}
  }
}

function writePreCommitStreamFailureToClient(
  res: Response,
  upstreamResponse: GatewayUpstreamResponse,
  streamResult: GatewayStreamPipeResult,
  protocol: GatewayErrorProtocol,
  clientStrategy: OpenAIGatewayClientStrategyContext | undefined
): Buffer | undefined {
  if (
    streamResult.downstreamBytesWritten !== 0
    || res.writableEnded
    || res.destroyed
    || streamResult.errorCode === undefined
  ) {
    return undefined
  }
  if (!res.headersSent) {
    prepareUpstreamResponseForDownstream(res, upstreamResponse, true)
  }
  const failureEvent = writeGatewayStreamFailureEvent(
    res,
    streamResult.message,
    streamResult.errorCode,
    protocol,
    clientStrategy?.downstreamProtocol
  )
  const chunks = [
    streamResult.uncommittedResponseBody,
    failureEvent
  ].filter((chunk): chunk is Buffer => Boolean(chunk?.length))
  for (const chunk of chunks) {
    res.write(chunk)
  }
  endResponse(res)
  return chunks.length > 0 ? Buffer.concat(chunks) : undefined
}

type GatewayStreamPipeResult = Awaited<ReturnType<typeof pipeUpstreamStream>>

async function applyResponseInspectionObservationHandling(
  streamResult: GatewayStreamPipeResult,
  account: UpstreamAccount,
  settings: GatewaySettings,
  auditCapture: AuditCaptureContext,
  accountStateMutationEnabled: boolean,
  usageContext: GatewayUsageContext
): Promise<void> {
  await applyResponseInspectionObservationDecisions(
    streamResult.responseInspectionObservations,
    streamResult.responseInspectionObservationOmittedCount,
    account,
    settings,
    auditCapture,
    accountStateMutationEnabled,
    usageContext
  )
}

function usageRequestSnapshotWithBodyOmission(
  requestSnapshot: UsageRequestSnapshot,
  bodyOmission: StreamBodyOmissionSummary | undefined
): UsageRequestSnapshot {
  if (!bodyOmission) {
    return requestSnapshot
  }
  const metadataOnlySnapshot: UsageRequestSnapshot = { ...requestSnapshot }
  delete metadataOnlySnapshot.body
  return {
    ...metadataOnlySnapshot,
    bodyOmission
  }
}

export async function handleNonStreamUpstreamResponse(input: HandleUpstreamResponseInput): Promise<UpstreamResponseHandlingResult> {
  const {
    req,
    res,
    account,
    upstreamResponse,
    upstreamUrl,
    auditAttemptId,
    auditCapture,
    settings,
    usageContext,
    startedAt,
    signal,
    sessionAffinityKey,
    accountStateMutationEnabled,
    markFirstOutput,
    clientIpAccountAvoidanceTracker
  } = input

  if (signal.aborted) {
    throw new UpstreamRequestAbortedError('请求已取消', true)
  }
  if (input.downstreamCommitState.transportCommitted && !input.downstreamCommitState.semanticCommitted) {
    return finalizeNonStreamResponseAfterSseHeartbeat(input)
  }
  let responseBody: Buffer | undefined
  let responseBodyText: string | undefined
  let responseResourceId: string | undefined
  let responseUsageText: string | undefined
  let responseSemanticText: string | undefined
  let bodyOmission: StreamBodyOmissionSummary | undefined
  let firstTokenMs: number | undefined
  let usage = emptyUsage()
  let errorPayload: Record<string, unknown> = {}
  const responseProtocol = gatewayProtocolResponseProtocolForRequest(req, account)
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  const forwardedResponseSuccessful = upstreamResponse.ok
  try {
    if (upstreamResponse.ok && isGeminiInteractionDeleteRequest(req, account)) {
      await deleteGeminiInteractionBeforeDownstreamCommit({ req, auditCapture, account, usageContext })
    }
    if (!upstreamResponse.body && isSuccessfulEmptyUpstreamResponseAllowed({
      req,
      account,
      statusCode: upstreamResponse.status
    })) {
      prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
      input.downstreamCommitState.markTransportCommitted()
      endResponse(res)
      input.downstreamCommitState.markSemanticCommitted()
      firstTokenMs = Date.now() - startedAt
      markFirstOutput?.()
    } else if (!upstreamResponse.body && interpretUpstreamResponseSemantics) {
      const errorMessage = '上游响应体为空'
      await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        success: false,
        errorPhase: 'upstream_response',
        errorCode: 'upstream_empty_body',
        errorMessage
      })
      await recordCompletedUpstreamAttempt(req, {
        ...usageContext,
        account,
        statusCode: upstreamResponse.status,
        success: false,
        stream: isEffectiveOpenAIStreamRequest(req, account),
        startedAt,
        usage: emptyUsage(),
        requestSnapshot: usageContext.requestSnapshot,
        responseSnapshot: buildUsageResponseSnapshot({
          upstreamUrl,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          errorMessage
        }),
        errorCode: 'upstream_empty_body',
        errorMessage
      })
      rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
        statusCode: upstreamResponse.status,
        errorCode: 'upstream_empty_body',
        errorPhase: 'upstream_response',
        errorMessage,
        endpoint: usageContext.endpoint
      })
      if (input.automaticAccountStateMutationEnabled !== false) {
        const localSuppression = suppressGatewayAccountLocally(account, settings, errorMessage)
        if (usageContext.trafficSource === 'gateway') {
          recordGatewayAccountFailureForPrecheck(account, settings, {
            systemAccountId: usageContext.systemAccountId,
            groupId: usageContext.groupId,
            apiKeyId: usageContext.apiKeyId,
            clientIp: usageContext.clientIp,
            endpoint: usageContext.endpoint,
            reason: errorMessage,
            statusCode: upstreamResponse.status,
            forcePrecheck: localSuppression.action === 'precheck_required',
            localSuppressionDelayMs: localSuppression.delayMs
          })
        }
      }
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'upstream_protocol_failure',
        excludeCurrentAccount: true,
        message: errorMessage,
        errorCode: 'upstream_empty_body'
      }
    } else if (upstreamResponse.body) {
      const contentType = upstreamResponse.headers.get('content-type') ?? ''
      const inspectJsonResponse = isOpenAIJsonResponseContentType(contentType)
        && shouldBufferNonStreamJsonResponse(input)
      if (inspectJsonResponse) {
        const pipeResult = await pipeNonStreamUpstreamResponseForInspection(upstreamResponse.body, res, {
          startedAt,
          inspectBytes: nonStreamResponseInspectionMaxBytes,
          captureBody: auditCapture.shouldCaptureSuccessPayloads(),
          signal,
          firstByteTimeoutMs: input.timeoutProfile.firstByteTimeoutMs,
          firstByteDeadlineMs: input.firstByteDeadlineMs,
          onFirstByteDeadline: input.onFirstByteDeadline,
          prepareDownstream: () => {
            prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
            input.downstreamCommitState.markTransportCommitted()
          },
          onChunkWritten: (bytesWritten) => input.downstreamCommitState.markSemanticCommitted(bytesWritten),
          beforeDownstreamCommit: isGeminiInteractionCreateRequest(req) && upstreamResponse.ok
            ? async (inspectionBody) => {
              responseResourceId = geminiInteractionIdFromJsonPrefix(inspectionBody)
              await rememberGeminiInteractionBeforeDownstreamCommit({
                auditCapture,
                account,
                usageContext,
                responseResourceId
              })
            }
            : undefined,
          onFirstByte: () => {
            firstTokenMs = Date.now() - startedAt
            markFirstOutput?.()
          }
        })
        responseBody = pipeResult.capturedBody
        responseBodyText = pipeResult.captureTruncated ? undefined : pipeResult.capturedBodyText
        responseUsageText = pipeResult.usageTailText
        firstTokenMs = pipeResult.firstByteMs
        if (pipeResult.fullyBuffered) {
          const completeBody = pipeResult.completeBody ?? Buffer.alloc(0)
          const completeBodyText = pipeResult.completeBodyText ?? completeBody.toString('utf8')
          if (isGeminiInteractionCreateRequest(req)) {
            responseResourceId = geminiInteractionIdFromResponseBody(completeBodyText)
          }
          const jsonInspectionResult = await inspectBufferedGatewayJsonResponse({
            req,
            res,
            account,
            upstreamResponse,
            upstreamUrl,
            auditAttemptId,
            auditCapture,
            settings,
            usageContext,
            startedAt,
            responseBody: completeBody,
            responseBodyText: completeBodyText,
            firstTokenMs,
            responseInspectionPolicies: managementResponseInspectionPoliciesForInput(input),
            clientStrategy: input.clientStrategy,
            accountStateMutationEnabled: accountStateMutationEnabled !== false,
            automaticAccountStateMutationEnabled: input.automaticAccountStateMutationEnabled !== false,
            protocolValidationEnabled: interpretUpstreamResponseSemantics,
            sessionAffinityKey
          })
          if (jsonInspectionResult) {
            return jsonInspectionResult
          }
          const hybridQualityResult = await inspectBufferedHybridQualityResponse({
            ...input,
            responseBody: completeBody,
            responseBodyText: completeBodyText,
            firstTokenMs
          })
          if (hybridQualityResult) {
            return hybridQualityResult
          }
          if (isGeminiInteractionCreateRequest(req)) {
            await rememberGeminiInteractionBeforeDownstreamCommit({
              auditCapture,
              account,
              usageContext,
              responseResourceId
            })
          }
          responseSemanticText = completeBodyText
          if (firstTokenMs === undefined) {
            firstTokenMs = Date.now() - startedAt
          }
          prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
          input.downstreamCommitState.markTransportCommitted()
          res.send(completeBody)
          input.downstreamCommitState.markSemanticCommitted(completeBody.length)
          if (!responseBody && auditCapture.shouldCaptureSuccessPayloads()) {
            responseBody = completeBody
            responseBodyText = completeBodyText
          }
          responseUsageText = completeBodyText
        } else {
          logger.warn({
            event: 'gateway_non_stream_response_inspection_omitted',
            accountId: account.id,
            statusCode: upstreamResponse.status,
            transferredBytes: pipeResult.transferredBytes,
            inspectBytes: nonStreamResponseInspectionMaxBytes,
            endpoint: usageContext.endpoint
          }, '网关非流式 JSON 响应超过检查窗口，已边转发并跳过完整语义检查')
        }
        if (pipeResult.captureTruncated) {
          logger.warn({
            event: 'gateway_non_stream_response_capture_truncated',
            accountId: account.id,
            statusCode: upstreamResponse.status,
            transferredBytes: pipeResult.transferredBytes,
            endpoint: usageContext.endpoint
          }, '网关非流式响应过大，已边转发并跳过完整响应捕获')
        }
      } else {
        const pipeResult = await pipeNonStreamUpstreamResponse(upstreamResponse.body, res, {
          startedAt,
          captureBody: auditCapture.shouldCaptureSuccessPayloads(),
          signal,
          firstByteTimeoutMs: input.timeoutProfile.firstByteTimeoutMs,
          firstByteDeadlineMs: input.firstByteDeadlineMs,
          onFirstByteDeadline: input.onFirstByteDeadline,
          prepareDownstream: () => {
            prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
            input.downstreamCommitState.markTransportCommitted()
          },
          onChunkWritten: (bytesWritten) => input.downstreamCommitState.markSemanticCommitted(bytesWritten),
          onBodyCompleted: (transferredBytes) => {
            if (transferredBytes === 0) input.downstreamCommitState.markSemanticCommitted()
          },
          onFirstByte: () => {
            firstTokenMs = Date.now() - startedAt
            markFirstOutput?.()
          }
        })
        responseBody = pipeResult.capturedBody
        responseBodyText = pipeResult.captureTruncated ? undefined : pipeResult.capturedBodyText
        responseUsageText = pipeResult.usageTailText
        firstTokenMs = pipeResult.firstByteMs
        if (pipeResult.captureTruncated) {
          logger.warn({
            event: 'gateway_non_stream_response_capture_truncated',
            accountId: account.id,
            statusCode: upstreamResponse.status,
            transferredBytes: pipeResult.transferredBytes,
            endpoint: usageContext.endpoint
          }, '网关非流式响应过大，已边转发并跳过完整响应捕获')
        }
      }
    } else {
      prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
      input.downstreamCommitState.markTransportCommitted()
      endResponse(res)
      input.downstreamCommitState.markSemanticCommitted()
      firstTokenMs = Date.now() - startedAt
      markFirstOutput?.()
    }
  } catch (error) {
    if (
      input.firstByteDeadlineMs !== undefined
      && isGatewayFirstByteTimeoutError(error)
      && error.source === 'speed_first_deadline'
      && error.timeoutMs === input.firstByteDeadlineMs
      && !res.headersSent
      && !res.writableEnded
      && !res.destroyed
    ) {
      const errorMessage = error.message
      await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        success: false,
        errorPhase: 'upstream_response',
        errorCode: error.code,
        errorMessage
      })
      await recordCompletedUpstreamAttempt(req, {
        ...usageContext,
        account,
        statusCode: upstreamResponse.status,
        success: false,
        stream: isEffectiveOpenAIStreamRequest(req, account),
        startedAt,
        usage: emptyUsage(),
        errorCode: error.code,
        errorMessage,
        requestSnapshot: usageContext.requestSnapshot,
        responseSnapshot: buildUsageResponseSnapshot({
          upstreamUrl,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          errorMessage
        })
      })
      auditCapture.addGatewayMetadata({
        label: 'normal_route_speed_first_confirmed_cutover',
        metadata: {
          accountId: account.id,
          accountName: account.name,
          timeoutMs: input.firstByteDeadlineMs,
          message: errorMessage
        }
      })
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'speed_first_first_byte_timeout',
        excludeCurrentAccount: true,
        message: errorMessage,
        errorCode: error.code
      }
    }
    if (isUpstreamRequestAbortedError(error) || signal.aborted) {
      await recordClientAbortedUpstreamAttempt(req, {
        ...usageContext,
        account,
        statusCode: upstreamResponse.status,
        stream: isEffectiveOpenAIStreamRequest(req, account),
        firstTokenMs,
        startedAt,
        requestSnapshot: usageContext.requestSnapshot,
        responseSnapshot: buildUsageResponseSnapshot({
          upstreamUrl,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          bodyText: responseBodyText,
          errorMessage: downstreamConnectionClosedMessage
        })
      })
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        success: false,
        errorPhase: 'client',
        errorMessage: downstreamConnectionClosedMessage
      })
    } else if (error instanceof NonStreamUpstreamBodyPipeError && (res.headersSent || res.writableEnded || res.destroyed)) {
      responseBody = error.partialResult.capturedBody
      responseBodyText = error.partialResult.diagnosticBodyText ?? error.partialResult.usageTailText
      responseUsageText = error.partialResult.usageTailText
      firstTokenMs = error.partialResult.firstByteMs ?? firstTokenMs
      const errorMessage = error instanceof Error ? error.message : '上游非流式响应正文中断'
      logger.warn({
        event: 'gateway_non_stream_body_interrupted_after_output',
        accountId: account.id,
        accountName: account.name,
        statusCode: upstreamResponse.status,
        endpoint: usageContext.endpoint,
        firstTokenMs,
        transferredBytes: error.partialResult.transferredBytes,
        captureTruncated: error.partialResult.captureTruncated,
        errorMessage
      }, '上游非流式响应正文已输出后中断，下游连接已按网络失败关闭')
      await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
      if (input.automaticAccountStateMutationEnabled !== false) {
        const runtimeReason = `上游非流式响应正文中断：${errorMessage}`
        const localSuppression = suppressGatewayAccountLocally(account, settings, runtimeReason)
        if (usageContext.trafficSource === 'gateway') {
          recordGatewayAccountFailureForPrecheck(account, settings, {
            systemAccountId: usageContext.systemAccountId,
            groupId: usageContext.groupId,
            apiKeyId: usageContext.apiKeyId,
            clientIp: usageContext.clientIp,
            endpoint: usageContext.endpoint,
            reason: runtimeReason,
            statusCode: upstreamResponse.status,
            forcePrecheck: localSuppression.action === 'precheck_required',
            localSuppressionDelayMs: localSuppression.delayMs
          })
        } else {
          await applyAccountErrorHandlingWithCacheInvalidation(account, {
            success: false,
            statusCode: upstreamResponse.status,
            headers: upstreamResponse.headers,
            bodyText: responseBodyText || errorMessage,
            errorMessage,
            settings,
            trafficSource: usageContext.trafficSource
          })
        }
        auditCapture.addGatewayMetadata({
          label: 'non_stream_body_interrupted_runtime_avoidance',
            metadata: {
              accountId: account.id,
              delayMs: localSuppression.delayMs,
              localFailureCount: localSuppression.localFailureCount,
              transferredBytes: error.partialResult.transferredBytes
            }
          })
      }
      await recordCompletedUpstreamAttempt(req, {
        ...usageContext,
        account,
        statusCode: upstreamResponse.status,
        success: false,
        stream: isEffectiveOpenAIStreamRequest(req, account),
        firstTokenMs,
        startedAt,
        usage: emptyUsage(),
        errorCode: 'upstream_body_interrupted',
        errorMessage,
        requestSnapshot: usageContext.requestSnapshot,
        responseSnapshot: buildUsageResponseSnapshot({
          upstreamUrl,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          bodyText: responseBodyText,
          errorMessage
        })
      })
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        responseBody: responseBody ?? responseBodyText,
        success: false,
        errorPhase: 'upstream_response',
        errorCode: 'upstream_body_interrupted',
        errorMessage
      })
      auditCapture.finalize({
        outcome: 'upstream_failed',
        success: false,
        statusCode: upstreamResponse.status,
        responseHeaders: responseHeadersToObject(res),
        responseBody: responseBodyText,
        responsePartType: 'gateway_response',
        errorPhase: 'upstream_response',
        errorCode: 'upstream_body_interrupted',
        errorMessage,
        accountId: account.id,
        firstTokenMs
      })
      return { alreadyFinalized: true }
    }
    throw error
  }
  if (responseBody) {
    usage = parseGatewayProtocolUsageFromJsonBufferForRequest(req, account, responseBody)
  } else if (responseUsageText) {
    usage = parseGatewayProtocolUsageFromJsonTextFragmentForRequest(req, account, responseUsageText)
  }
  if (forwardedResponseSuccessful) {
    usage = applyNonStreamUsageFallback({
      req,
      account,
      usage,
      responseBody,
      responseBodyText: responseBodyText ?? responseSemanticText,
      endpoint: usageContext.endpoint
    })
  }
  if (interpretUpstreamResponseSemantics && !upstreamResponse.ok) {
    errorPayload = parseGatewayProtocolErrorPayloadForRequest(req, account, responseBodyText ?? '', upstreamResponse.headers)
  }
  if (forwardedResponseSuccessful) {
    bodyOmission = nonStreamImageResponseBodyOmission(responseBodyText ?? responseSemanticText ?? responseBody?.toString('utf8'), responseBody?.byteLength)
    if (bodyOmission) {
      responseBody = undefined
      responseBodyText = undefined
      responseSemanticText = undefined
    }
  }
  auditCapture.completeAttempt(auditAttemptId, {
    statusCode: upstreamResponse.status,
    responseHeaders: upstreamResponse.headers,
    responseBody: bodyOmission ? undefined : responseBody,
    success: forwardedResponseSuccessful,
    errorPhase: forwardedResponseSuccessful ? undefined : 'upstream_response',
    errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
    errorMessage: typeof errorPayload.message === 'string' ? errorPayload.message : undefined
  })

  return {
    alreadyFinalized: false,
    usage,
    firstTokenMs,
    responseBodyText,
    responseResourceId,
    bodyOmission,
    errorPayload
  }
}

function shouldBufferNonStreamJsonResponse(input: HandleUpstreamResponseInput): boolean {
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  return Boolean(
    runtimeResponseInspectionPoliciesForInput(input).length > 0
    || (
      interpretUpstreamResponseSemantics
      && input.clientStrategy?.codexCompactionExpected === true
    )
    || (
      input.hybridRoute?.config.qualityInspection?.enabled === true
      && !input.hybridRoute.scoringFallbackApplied
    )
    || (
      input.upstreamResponse.ok
      && isGeminiInteractionCreateRequest(input.req)
    )
  )
}

function managementResponseInspectionPoliciesForInput(
  input: HandleUpstreamResponseInput
): ResponseInspectionPolicySummary[] | undefined {
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  return interpretUpstreamResponseSemantics
    ? input.responseInspectionPolicies
    : undefined
}

function runtimeResponseInspectionPoliciesForInput(input: HandleUpstreamResponseInput) {
  return resolveRuntimeResponseInspectionPolicies({
    account: input.account,
    managementPolicies: managementResponseInspectionPoliciesForInput(input)
  })
}

async function finalizeNonStreamResponseAfterSseHeartbeat(
  input: HandleUpstreamResponseInput
): Promise<UpstreamResponseHandlingResult> {
  const message = '等待可用账户期间已建立 SSE 保活连接，但上游返回了非流式响应，请客户端重试'
  try {
    const iterator = input.upstreamResponse.body?.[Symbol.asyncIterator]()
    await iterator?.return?.()
  } catch (error) {
    logger.debug({
      event: 'gateway_non_stream_after_sse_heartbeat_cancel_failed',
      accountId: input.account.id,
      errorMessage: error instanceof Error ? error.message : String(error)
    }, 'SSE 保活连接无法承接非流式响应，取消上游正文时出现非阻断异常')
  }
  const failureEvent = writeGatewayStreamFailureEvent(
    input.res,
    gatewayStreamClientRetryMessage,
    gatewayStreamClientRetryErrorCode,
    gatewayProtocolClientErrorProtocolForRequest(input.req, input.account),
    input.clientStrategy?.downstreamProtocol
  )
  if (failureEvent?.length && !input.res.writableEnded && !input.res.destroyed) {
    input.res.write(failureEvent)
    input.downstreamCommitState.markSemanticCommitted(failureEvent.length)
    endResponse(input.res)
  } else if (!input.res.writableEnded && !input.res.destroyed) {
    input.res.destroy()
  }
  input.auditCapture.completeAttempt(input.auditAttemptId, {
    statusCode: input.upstreamResponse.status,
    responseHeaders: input.upstreamResponse.headers,
    success: false,
    errorPhase: 'downstream',
    errorCode: 'downstream_transport_conflict',
    errorMessage: message
  })
  await recordCompletedUpstreamAttempt(input.req, {
    ...input.usageContext,
    account: input.account,
    statusCode: input.upstreamResponse.status,
    success: false,
    stream: true,
    startedAt: input.startedAt,
    usage: emptyUsage(),
    errorCode: 'downstream_transport_conflict',
    errorMessage: message,
    requestSnapshot: input.usageContext.requestSnapshot,
    responseSnapshot: buildUsageResponseSnapshot({
      upstreamUrl: input.upstreamUrl,
      statusCode: input.upstreamResponse.status,
      headers: input.upstreamResponse.headers,
      errorMessage: message
    })
  })
  input.auditCapture.finalize({
    outcome: 'stream_failed',
    success: false,
    statusCode: input.res.statusCode,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody: failureEvent,
    responsePartType: 'gateway_response',
    errorPhase: 'downstream',
    errorCode: 'downstream_transport_conflict',
    errorMessage: message,
    accountId: input.account.id
  })
  return { alreadyFinalized: true }
}

function isGeminiInteractionDeleteRequest(req: Request, account: UpstreamAccount): boolean {
  return req.method.toUpperCase() === 'DELETE'
    && isGeminiInteractionResourceRequest(req)
    && gatewayProtocolResponseEndpointFamilyForRequest(req, account) === 'interactions'
}

async function rememberGeminiInteractionBeforeDownstreamCommit(input: {
  auditCapture: AuditCaptureContext
  account: UpstreamAccount
  usageContext: GatewayUsageContext
  responseResourceId: string | undefined
}): Promise<void> {
  let mutation
  try {
    mutation = await rememberGeminiInteractionAffinityAsync({
      interactionId: input.responseResourceId,
      account: input.account,
      scope: geminiInteractionAffinityScope(input.usageContext)
    })
  } catch (error) {
    recordGeminiInteractionAffinityPersistenceFailure(error, {
      operation: 'remember',
      auditCapture: input.auditCapture,
      accountId: input.account.id,
      groupId: input.usageContext.groupId
    })
    throw error
  }
  if (mutation.action !== 'none') {
    input.auditCapture.addGatewayMetadata({
      label: 'gemini_interaction_account_affinity_update',
      metadata: { ...mutation, commitPhase: 'before_downstream_commit' }
    })
  }
}

async function deleteGeminiInteractionBeforeDownstreamCommit(input: {
  req: Request
  auditCapture: AuditCaptureContext
  account: UpstreamAccount
  usageContext: GatewayUsageContext
}): Promise<void> {
  let mutation
  try {
    mutation = await deleteGeminiInteractionAffinityAsync({
      interactionId: geminiInteractionResourceIdFromRequest(input.req),
      scope: geminiInteractionAffinityScope(input.usageContext)
    })
  } catch (error) {
    recordGeminiInteractionAffinityPersistenceFailure(error, {
      operation: 'delete',
      auditCapture: input.auditCapture,
      accountId: input.account.id,
      groupId: input.usageContext.groupId
    })
    throw error
  }
  if (mutation.action !== 'none') {
    input.auditCapture.addGatewayMetadata({
      label: 'gemini_interaction_account_affinity_update',
      metadata: { ...mutation, commitPhase: 'before_downstream_commit' }
    })
  }
}

function recordGeminiInteractionAffinityPersistenceFailure(
  error: unknown,
  input: {
    operation: 'remember' | 'delete'
    auditCapture: AuditCaptureContext
    accountId: string
    groupId: string
  }
): void {
  const originalError = error instanceof GeminiInteractionAffinityUnavailableError
    ? error.originalError
    : error
  getRequestLogger().error(errorLogFields(originalError, {
    event: 'gateway_gemini_interaction_affinity_update_failed',
    operation: input.operation,
    accountId: input.accountId,
    groupId: input.groupId
  }), 'Gemini Interaction 账号亲和状态更新失败')
  input.auditCapture.addGatewayMetadata({
    label: 'gemini_interaction_account_affinity_update_failed',
    metadata: {
      operation: input.operation,
      accountId: input.accountId
    }
  })
}

function geminiInteractionAffinityScope(usageContext: GatewayUsageContext): {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
} {
  return {
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId
  }
}

function isFirstByteTimeoutStreamResult(streamResult: { errorCode?: string; firstTokenMs?: number; semanticCommitted: boolean; outputReceived: boolean }): boolean {
  return streamResult.errorCode === 'first_byte_timeout'
    && streamResult.firstTokenMs === undefined
    && !streamResult.semanticCommitted
    && !streamResult.outputReceived
}

function nonStreamImageResponseBodyOmission(bodyText: string | undefined, capturedBytes: number | undefined): StreamBodyOmissionSummary | undefined {
  if (!bodyText || !nonStreamBodyLooksLikeImageGenerationPayload(bodyText)) return undefined
  const bodyBytes = capturedBytes ?? Buffer.byteLength(bodyText, 'utf8')
  return {
    omitted: true,
    reason: 'image_json_payload',
    message: '图像 JSON 正文已省略，避免在日志和审计中保存图片字节',
    totalUpstreamBytes: bodyBytes,
    totalResponseBytes: bodyBytes,
    imageOutputReceived: true
  }
}

function nonStreamBodyLooksLikeImageGenerationPayload(bodyText: string): boolean {
  try {
    return jsonContainsImageGenerationResult(JSON.parse(bodyText) as unknown)
  } catch {
    return bodyText.includes('"type":"image_generation_call"') && bodyText.includes('"result"')
  }
}

function jsonContainsImageGenerationResult(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(jsonContainsImageGenerationResult)
  if (typeof value !== 'object' || value === null) return false
  const record = value as Record<string, unknown>
  if (record.type === 'image_generation_call' && typeof record.result === 'string' && record.result.length > 0) {
    return true
  }
  return Object.values(record).some(jsonContainsImageGenerationResult)
}

async function inspectBufferedHybridQualityResponse(input: {
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
  hybridRoute?: HybridGatewayRuntimeRoute
  signal: AbortSignal
}): Promise<UpstreamResponseHandlingResult | undefined> {
  const hybridRoute = input.hybridRoute
  if (!hybridRoute || !input.upstreamResponse.ok) {
    return undefined
  }
  if (hybridRoute.scoringFallbackApplied) {
    input.auditCapture.addGatewayMetadata({
      label: 'hybrid_quality_inspection',
      metadata: {
        triggered: false,
        triggerReason: 'scoring_fallback_skip_quality_inspection',
        pass: true,
        routeLevel: hybridRoute.scoring.level,
        targetModel: hybridRoute.targetModel,
        qualityRetryCount: hybridRoute.qualityRetryCount,
        scoringFallbackApplied: true
      }
    })
    return undefined
  }
  const quality = await inspectHybridGatewayQuality({
    req: input.req,
    apiKeyRecord: hybridRoute.apiKeyRecord,
    config: hybridRoute.config,
    scoring: hybridRoute.scoring,
    targetRoute: hybridRoute.route,
    targetModel: hybridRoute.targetModel,
    responseBodyText: input.responseBodyText,
    traceId: input.usageContext.traceId,
    clientIp: input.usageContext.clientIp,
    endpoint: input.usageContext.endpoint,
    signal: input.signal
  })
  input.auditCapture.addGatewayMetadata({
    label: 'hybrid_quality_inspection',
    metadata: hybridQualityAuditMetadata(quality, hybridRoute)
  })
  if (!quality.triggered || quality.pass) {
    return undefined
  }
  const usage = parseGatewayProtocolUsageFromJsonBufferForRequest(input.req, input.account, input.responseBody)
  const message = hybridQualityFailureMessage(quality)
  const errorCode = quality.errorCode ?? `hybrid_quality_${quality.result?.failureType ?? 'failed'}`
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
  return {
    alreadyFinalized: false,
    retryUpstream: true,
    retryReason: 'hybrid_quality',
    excludeCurrentAccount: false,
    message,
    errorCode,
    statusCode: hybridQualityFailureStatusCode(quality),
    hybridQuality: quality
  }
}

function hybridQualityFailureStatusCode(quality: HybridQualityInspectionOutcome): number {
  if (quality.errorCode === 'no_quality_scoring_account' || quality.errorCode === 'quality_scoring_account_busy') {
    return 503
  }
  return 502
}

function hybridQualityFailureMessage(quality: HybridQualityInspectionOutcome): string {
  if (quality.errorMessage) {
    return `混合路由质量评分不可用：${quality.errorMessage}`
  }
  return quality.result?.reason
    ? `混合路由质量评分未通过：${quality.result.reason}`
    : '混合路由质量评分未通过'
}

function hybridQualityAuditMetadata(
  quality: HybridQualityInspectionOutcome,
  hybridRoute: HybridGatewayRuntimeRoute
): Record<string, unknown> {
  return {
    triggered: quality.triggered,
    triggerReason: quality.triggerReason,
    pass: quality.pass,
    score: quality.result?.score,
    confidence: quality.result?.confidence,
    failureType: quality.result?.failureType,
    retryRecommendation: quality.result?.retryRecommendation,
    actualAction: quality.actualAction,
    reason: quality.result?.reason,
    errorCode: quality.errorCode,
    errorMessage: quality.errorMessage,
    statusCode: quality.statusCode,
    qualityAccountId: quality.qualityAccountId,
    routeLevel: hybridRoute.scoring.level,
    targetModel: hybridRoute.targetModel,
    qualityRetryCount: hybridRoute.qualityRetryCount
  }
}

function applyNonStreamUsageFallback(input: {
  req: Request
  account: UpstreamAccount
  usage: ParsedUsage
  responseBody?: Buffer
  responseBodyText?: string
  endpoint: string
}): ParsedUsage {
  const bodyText = input.responseBody
    ? input.responseBody.toString('utf8')
    : input.responseBodyText
  if (!bodyText) return input.usage
  let parsed: unknown
  try {
    parsed = JSON.parse(bodyText) as unknown
  } catch {
    return input.usage
  }
  const frames = extractGatewayProtocolJsonSemanticFramesForRequest(parsed, input.req, input.account)
  const outputText = frames
    .filter((frame) => (frame.frameType === 'output_text_delta' || frame.frameType === 'output_text_done') && typeof frame.text === 'string')
    .map((frame) => frame.text ?? '')
    .filter(Boolean)
    .join('\n')
  const outputReceived = outputText.length > 0 || frames.some((frame) => frame.visibleOutput === true)
  if (!outputReceived) return input.usage
  const fallback = applyGatewayProtocolStreamUsageFallbackForRequest(input.req, input.account, input.usage, {
    outputReceived,
    estimatedOutputTokens: outputText ? estimateTokenCountFromText(outputText) : undefined
  })
  if (fallback.estimated) {
    logger.warn({
      event: 'gateway_non_stream_usage_estimated',
      accountId: input.account.id,
      endpoint: input.endpoint,
      model: requestModel(input.req),
      estimatedInputTokens: fallback.estimatedInputTokens,
      estimatedOutputTokens: fallback.estimatedOutputTokens
    }, '上游非流式响应缺少有效 usage，网关已按响应语义估算 token 成本')
  }
  return fallback.usage
}

export async function finalizeHandledUpstreamResponse(input: FinalizeHandledUpstreamResponseInput): Promise<void> {
  const {
    req,
    res,
    account,
    upstreamResponse,
    upstreamUrl,
    auditCapture,
    settings,
    usageContext,
    startedAt,
    result,
    clientIpAccountAvoidanceTracker
  } = input
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  const forwardedResponseSuccessful = upstreamResponse.ok
  if (!interpretUpstreamResponseSemantics && !upstreamResponse.ok) {
    forgetOpenAIAccountForSessionBestEffort(input.sessionAffinityKey, account.id)
  }
  if (interpretUpstreamResponseSemantics && upstreamResponse.ok) {
    if (!isAccountDiagnosticTrafficSource(usageContext.trafficSource)) {
      if (input.automaticAccountStateMutationEnabled !== false) {
        const clearedProxyFailure = await recordGatewayUpstreamBucketSuccessAsync(account)
        if (clearedProxyFailure) {
          getRequestLogger().info({
            event: 'gateway_upstream_failure_bucket_recovered',
            accountId: account.id,
            accountName: account.name
          }, '上游桶运行态失败已按后台确认恢复')
          auditCapture.addGatewayMetadata({
            label: 'upstream_bucket_health',
            metadata: {
              recovered: true
            }
          })
        }
      }
      const clearedClientIpErrorCircuit = await recordClientIpErrorCircuitSuccessAsync({
        systemAccountId: usageContext.systemAccountId,
        apiKeyId: usageContext.apiKeyId,
        groupId: usageContext.groupId,
        clientIp: usageContext.clientIp,
        endpoint: usageContext.endpoint
      })
      if (clearedClientIpErrorCircuit) {
        getRequestLogger().info({
          event: 'gateway_client_ip_error_circuit_recovered',
          accountId: account.id,
          systemAccountId: usageContext.systemAccountId,
          apiKeyId: usageContext.apiKeyId,
          groupId: usageContext.groupId,
          clientIp: usageContext.clientIp
        }, '客户端 IP 级错误熔断状态已按成功响应恢复')
        auditCapture.addGatewayMetadata({
          label: 'client_ip_error_circuit',
          metadata: {
            recovered: true
          }
        })
      }
    }
    if (input.automaticAccountStateMutationEnabled !== false) {
      await applyAccountErrorHandlingWithCacheInvalidation(account, {
        success: true,
        settings,
        trafficSource: usageContext.trafficSource
      })
    }
    if (input.automaticAccountStateMutationEnabled !== false
      && usageContext.trafficSource !== 'gateway'
      && (account.streamFailureCount > 0 || account.streamFailureWindowStartedAt || account.lastErrorMessage)) {
      clearAccountStreamFailureStateWithCacheInvalidation(account)
    }
  }

  if (upstreamResponse.ok && !isAccountDiagnosticTrafficSource(usageContext.trafficSource)) {
    const clientIpAvoidanceResult = await confirmClientIpAccountAvoidanceAfterSuccessAsync(
      clientIpAccountAvoidanceTracker,
      account.id,
      settings
    )
    if (clientIpAvoidanceResult.confirmedAccountIds.length > 0 || clientIpAvoidanceResult.cleared) {
      getRequestLogger().info({
        event: 'gateway_client_ip_account_failure_confirmed',
        accountId: account.id,
        confirmedAccountIds: clientIpAvoidanceResult.confirmedAccountIds,
        clearedAccountId: clientIpAvoidanceResult.cleared ? clientIpAvoidanceResult.clearedAccountId : undefined
      }, '客户端 IP 级账号回避状态已按成功响应更新')
      auditCapture.addGatewayMetadata({
        label: 'client_ip_account_avoidance_update',
        metadata: {
          confirmedAccountIds: clientIpAvoidanceResult.confirmedAccountIds,
          clearedAccountId: clientIpAvoidanceResult.cleared ? clientIpAvoidanceResult.clearedAccountId : undefined
        }
      })
    }
  }

  await recordCompletedUpstreamAttempt(req, {
    ...usageContext,
    account,
    stream: isEffectiveOpenAIStreamRequest(req, account),
    statusCode: upstreamResponse.status,
    success: forwardedResponseSuccessful,
    firstTokenMs: result.firstTokenMs,
    startedAt,
    completedAtMs: input.completedAtMs,
    usage: result.usage,
    errorCode: typeof result.errorPayload.code === 'string' ? result.errorPayload.code : undefined,
    errorMessage: typeof result.errorPayload.message === 'string' ? result.errorPayload.message : undefined,
    requestSnapshot: result.bodyOmission
      ? usageRequestSnapshotWithBodyOmission(usageContext.requestSnapshot, result.bodyOmission)
      : forwardedResponseSuccessful ? undefined : usageContext.requestSnapshot,
    responseSnapshot: result.bodyOmission
      ? buildUsageResponseSnapshot({
        upstreamUrl,
        statusCode: upstreamResponse.status,
        headers: upstreamResponse.headers,
        bodyOmission: result.bodyOmission
      })
      : forwardedResponseSuccessful ? undefined : buildUsageResponseSnapshot({
        upstreamUrl,
        statusCode: upstreamResponse.status,
        headers: upstreamResponse.headers,
        bodyText: result.responseBodyText
      })
  })
  if (result.bodyOmission) {
    auditCapture.omitPayloadBodies({
      label: 'non_stream_body_omission',
      metadata: { ...result.bodyOmission },
      partTypes: ['upstream_response']
    })
  }
  auditCapture.finalize({
    outcome: forwardedResponseSuccessful ? 'success' : 'upstream_failed',
    success: forwardedResponseSuccessful,
    statusCode: upstreamResponse.status,
    responseHeaders: responseHeadersToObject(res),
    responseBody: result.bodyOmission ? undefined : result.responseBodyText,
    responsePartType: forwardedResponseSuccessful ? 'gateway_response' : 'gateway_error',
    errorPhase: forwardedResponseSuccessful ? undefined : 'upstream_response',
    errorCode: typeof result.errorPayload.code === 'string' ? result.errorPayload.code : undefined,
    errorMessage: typeof result.errorPayload.message === 'string' ? result.errorPayload.message : undefined,
    accountId: account.id,
    firstTokenMs: result.firstTokenMs
  })
}
