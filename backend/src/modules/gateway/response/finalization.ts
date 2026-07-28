import type { Request, Response } from 'express'

import { errorLogFields, logger } from '../../../shared/logger.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import { runtimeConfig } from '../../../config/runtime.js'
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
  type ClientIpAccountAvoidanceTracker
} from '../runtime/client-ip-account-avoidance.service.js'
import { recordClientIpErrorCircuitSuccessAsync } from '../runtime/client-ip-error-circuit.service.js'
import {
  NonStreamUpstreamBodyPipeError,
  endResponse,
  isProvenUpstreamBodyTransportError,
  markGatewayForcedDownstreamClose,
  pipeNonStreamUpstreamResponse,
  pipeNonStreamUpstreamResponseForInspection,
  nonStreamResponseCaptureBytes
} from '../upstream/body.js'
import {
  codexResponsesGuardAuditMetadata,
  responseInspectionAuditMetadata
} from '../audit/metadata.js'
import {
  codexResponsesGuardUsageSummary,
  createCodexResponsesResponseGuard,
  type CodexResponsesGuardUsageSummary,
  type CodexResponsesResponseGuard
} from '../codex-responses/response-guard.js'
import { resolveCodexResponsesGuardMode } from '../codex-responses/account-policy.js'
import {
  forgetOpenAIAccountForSessionAsync
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
  automaticUpstreamReplayAllowedAfterDispatch,
  resolveOpenAIGatewayRequestLane
} from '../protocols/openai-v1/request-lane.js'
import {
  pipeUpstreamStream,
  type StreamFailureContext,
  type StreamBodyOmissionSummary
} from './stream.js'
import { dispatchRequestFailureAccountHealthCheck } from './request-failure-health-check.js'
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
import {
  isGatewayResponsePrecommitDeadlineError,
  type FirstByteDeadlineHandler
} from '../upstream/first-byte-deadline.js'
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
  parseGatewayProtocolUsageFromJsonTextFragment,
  parseGatewayProtocolUsageFromJsonTextFragmentForRequest,
  parseGatewayProtocolUsageFromJsonValue,
  parseGatewayProtocolUsageFromJsonValueForRequest,
  parseGatewayProtocolErrorPayload,
  parseGatewayProtocolErrorPayloadFromJsonValue
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
import { recordGatewayAccountApiKeySuccess } from '../runtime/account-api-key-effects.service.js'
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
  geminiInteractionIdFromParsedResponse,
  isGeminiInteractionCreateRequest,
  isGeminiInteractionResourceRequest,
  rememberGeminiInteractionAffinityAsync
} from '../protocols/gemini-v1beta/interaction-affinity.service.js'
import {
  gatewayNonStreamJsonBodyFromValue,
  parseGatewayNonStreamJsonBody,
  publishGatewayNonStreamJsonBody,
  type GatewayNonStreamJsonBody
} from './non-stream-json-body.js'

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

export function protocolValidatedNonStreamResponse(input: {
  req: Request
  account: UpstreamAccount
  responseBodyText?: string
  parsedJsonBody?: GatewayNonStreamJsonBody
  statusCode: number
}): boolean {
  if (input.statusCode < 200 || input.statusCode >= 300) return false
  if (isSuccessfulEmptyUpstreamResponseAllowed(input)) return true
  const parsedJsonBody = input.parsedJsonBody ?? parseGatewayNonStreamJsonBody(input.responseBodyText)
  if (parsedJsonBody.status !== 'valid') return false
  const parsed = parsedJsonBody.value
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return false
  const root = parsed as Record<string, unknown>
  if (isRecordValue(root.error) || root.type === 'error' || root.status === 'failed') return false
  const path = (input.req.originalUrl || input.req.path || '').split('?', 1)[0].toLowerCase()
  if (path === '/models' || path === '/v1/models' || path === '/v1beta/models') {
    return Array.isArray(root.data) || root.object === 'model'
  }
  const endpointFamily = gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account)
  switch (endpointFamily) {
    case 'chat_completions':
      return Array.isArray(root.choices)
        && root.choices.some((choice) => {
          if (!isRecordValue(choice) || isRecordValue(choice.error)) return false
          return (isRecordValue(choice.message) && !isRecordValue(choice.message.error))
            || typeof choice.text === 'string'
        })
    case 'responses':
      return (root.object === 'response' || root.type === 'response')
        && typeof root.id === 'string'
        && Array.isArray(root.output)
        && root.status !== 'failed'
    case 'messages':
      return root.type === 'message' && Array.isArray(root.content)
    case 'models':
      return Array.isArray(root.data) || root.object === 'model'
    case 'message_token_counting':
      return typeof root.input_tokens === 'number'
    case 'generate_content':
    case 'stream_generate_content':
      return Array.isArray(root.candidates) || isRecordValue(root.promptFeedback)
    case 'count_tokens':
      return typeof root.totalTokens === 'number'
    case 'embed_content':
      return isRecordValue(root.embedding) || Array.isArray(root.embeddings)
    case 'interactions':
      return typeof root.id === 'string' || typeof root.name === 'string'
    case 'unknown': {
      return (path.includes('/embeddings') || path.includes('/images')) && Array.isArray(root.data)
    }
  }
}

function isRecordValue(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
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
  responsePrecommitDeadlineAtMs?: number
  onFirstByteDeadline?: FirstByteDeadlineHandler
  onFirstByteDeadlineSuperseded?: () => void
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

export interface FinalizeHandledUpstreamResponseInput extends HandleUpstreamResponseInput {
  result: Exclude<UpstreamResponseHandlingResult, { alreadyFinalized: true } | { retryUpstream: true }>
  completedAtMs?: number
  routingEffectsApplied?: boolean
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

  let streamResult: Awaited<ReturnType<typeof pipeUpstreamStream>>
  let codexTurnFailureRemembered = false
  const codexResponsesGuard = createCodexResponsesGuardForInput(input)
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
      async (message, errorCode, context) => {
        await handleStreamFailure(account, message, settings, errorCode, context, usageContext, shouldMutateAccountForStreamFailure(errorCode, context))
        if (context.availabilityProbeEligible) {
          dispatchRequestFailureAccountHealthCheck(req, usageContext.trafficSource, account.id)
        }
      },
      signal,
      {
        clientRetryEnabled: false,
        committedFailureSignal: clientStrategy?.retryCoordination.committedFailureSignal,
        // Enable the policy interceptor for precise clients. Unmatched vendor
        // events remain opaque in pipeUpstreamStream.
        interpretProtocolFailures: interpretUpstreamResponseSemantics,
        retryBeforeDownstreamWriteUntilOutput: true,
        onFirstOutput: markFirstOutput,
        captureSuccessPayloads: auditCapture.shouldCaptureSuccessPayloads(),
        firstByteTimeoutMs: input.firstByteTimeoutMs,
        firstByteDeadlineMs: input.firstByteDeadlineMs,
        responsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
        onFirstByteDeadline: input.onFirstByteDeadline,
        onFirstByteDeadlineSuperseded: input.onFirstByteDeadlineSuperseded,
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
        codexResponsesGuard,
        beforeCommittedFailureSignal: async (context) => {
          if (
            isAccountDiagnosticTrafficSource(usageContext.trafficSource)
            || clientStrategy?.allowCodexTurnAccountAvoidance !== true
            || !context.accountFailureEligible
            || !context.semanticCommitted
            || !context.outputReceived
          ) {
            return
          }
          const codexTurnFailure = await rememberCodexTurnStreamFailureAsync(clientStrategy, account.id, {
            errorCode: context.errorCode,
            message: context.message,
            evidence: 'committed_retry_signal',
            observationId: `${auditAttemptId}:client_visible_failure`
          })
          if (!codexTurnFailure) {
            return
          }
          codexTurnFailureRemembered = true
          auditCapture.addGatewayMetadata({
            label: 'codex_turn_committed_retry_signal',
            metadata: {
              stateKey: codexTurnFailure.stateKey,
              failureCount: codexTurnFailure.failureCount,
              failedAccountIds: codexTurnFailure.failedAccountIds,
              avoidanceActivatedAccountIds: codexTurnFailure.avoidanceActivatedAccountIds,
              duplicateObservation: codexTurnFailure.duplicateObservation,
              accountId: account.id,
              downstreamBytesWritten: context.downstreamBytesWritten
            }
          })
        },
        onIncompleteClientAbort: async (context) => {
          if (
            isAccountDiagnosticTrafficSource(usageContext.trafficSource)
            || clientStrategy?.allowCodexTurnAccountAvoidance !== true
          ) {
            return
          }
          const codexTurnFailure = await rememberCodexTurnStreamFailureAsync(clientStrategy, account.id, {
            errorCode: 'incomplete_downstream_abort',
            message: downstreamConnectionClosedMessage,
            evidence: 'incomplete_downstream_abort',
            observationId: `${auditAttemptId}:incomplete_downstream_abort`
          })
          if (!codexTurnFailure) {
            return
          }
          auditCapture.addGatewayMetadata({
            label: 'codex_turn_incomplete_downstream_abort',
            metadata: {
              stateKey: codexTurnFailure.stateKey,
              failureCount: codexTurnFailure.failureCount,
              failedAccountIds: codexTurnFailure.failedAccountIds,
              avoidanceActivatedAccountIds: codexTurnFailure.avoidanceActivatedAccountIds,
              duplicateObservation: codexTurnFailure.duplicateObservation,
              accountId: account.id,
              downstreamBytesWritten: context.downstreamBytesWritten,
              outputReceived: context.outputReceived
            }
          })
        },
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
    codexResponsesGuard?.dispose()
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
  if (streamResult.codexResponsesGuard && streamResult.codexResponsesGuard.outcome !== 'clean') {
    auditCapture.addGatewayMetadata({
      label: 'codex_responses_protocol_guard',
      metadata: codexResponsesGuardAuditMetadata(streamResult.codexResponsesGuard)
    })
  }
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
      metadata: { ...streamResult.bodyOmission },
      partTypes: streamResult.completed
        ? undefined
        : ['upstream_response', 'gateway_response', 'gateway_error'],
      alreadyOmittedPayloadCount: 2,
      alreadyOmittedBodyBytes: streamResult.bodyOmission.totalUpstreamBytes
        + streamResult.bodyOmission.totalResponseBytes
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
    const upstreamResponseCommitted = isStreamUpstreamResponseCommitted(streamResult)
    const canRetryUpstream = !upstreamResponseCommitted
    const responsePrecommitDeadlineExceeded = isResponsePrecommitDeadlineStreamResult(streamResult)
    const speedFirstFirstByteCutover = canRetryUpstream && (
      responsePrecommitDeadlineExceeded
      || (input.firstByteDeadlineMs !== undefined && isFirstByteTimeoutStreamResult(streamResult))
    )
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
        errorMessage: streamResult.message,
        codexResponsesGuard: streamResult.codexResponsesGuard
          ? codexResponsesGuardUsageSummary(streamResult.codexResponsesGuard)
          : undefined
      }),
      errorMessage: streamResult.message
    })
    if (responsePrecommitDeadlineExceeded) {
      auditCapture.addGatewayMetadata({
        label: 'gateway_request_wall_budget_exhausted',
        metadata: {
          accountId: account.id,
          accountName: account.name,
          timeoutMs: input.firstByteDeadlineMs,
          message: streamResult.message,
          upstreamResponseBytesWritten: streamResult.upstreamResponseBytesWritten,
          upstreamResponseCommitted
        }
      })
    }
    if (speedFirstFirstByteCutover) {
      if (!responsePrecommitDeadlineExceeded) {
        auditCapture.addGatewayMetadata({
          label: 'normal_route_first_byte_deadline_cutover',
          metadata: {
            accountId: account.id,
            accountName: account.name,
            timeoutMs: input.firstByteDeadlineMs,
            message: streamResult.message
          }
        })
      }
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'normal_route_first_byte_timeout',
        excludeCurrentAccount: true,
        message: streamResult.message,
        errorCode: streamResult.errorCode,
        uncommittedResponseBody: streamResult.uncommittedResponseBody,
        transportFailure: streamResult.transportFailure
      }
    }
    if (canRetryUpstream && shouldRetryResponseInspectionOnServer(streamResult, res)) {
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
        uncommittedResponseBody: streamResult.uncommittedResponseBody,
        transportFailure: streamResult.transportFailure
      }
    }
    if (canRetryUpstream && shouldRetryPreCommitStreamFailureOnServer(streamResult, res)) {
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
        uncommittedResponseBody: streamResult.uncommittedResponseBody,
        transportFailure: streamResult.transportFailure
      }
    }
    if (
      !isAccountDiagnosticTrafficSource(usageContext.trafficSource)
      && !codexTurnFailureRemembered
      && shouldRememberCodexTurnStreamFailure(streamResult, clientStrategy)
    ) {
      const codexTurnFailure = await rememberCodexTurnStreamFailureAsync(clientStrategy, account.id, {
        errorCode: streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode,
        message: streamResult.message,
        observationId: `${auditAttemptId}:client_visible_failure`
      })
      if (codexTurnFailure) {
        auditCapture.addGatewayMetadata({
          label: 'codex_turn_stream_failure',
          metadata: {
            stateKey: codexTurnFailure.stateKey,
            failureCount: codexTurnFailure.failureCount,
            failedAccountIds: codexTurnFailure.failedAccountIds,
            avoidanceActivatedAccountIds: codexTurnFailure.avoidanceActivatedAccountIds,
            duplicateObservation: codexTurnFailure.duplicateObservation,
            accountId: account.id
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
    return {
      alreadyFinalized: true,
      errorCode: streamResult.errorCode,
      transportFailure: streamResult.transportFailure,
      gatewayLocalFailure: streamResult.gatewayLocalFailure
    }
  }

  const codexResponsesGuardSnapshot = streamResult.codexResponsesGuard
  return {
    alreadyFinalized: false,
    usage: streamUsageFallback.usage,
    firstTokenMs: streamResult.firstTokenMs,
    responseBodyText: streamResult.responseBodyText,
    responseResourceId: streamResult.responseResourceId,
    bodyOmission: streamResult.bodyOmission,
    codexResponsesGuard: codexResponsesGuardSnapshot && codexResponsesGuardSnapshot.outcome !== 'clean'
      ? codexResponsesGuardUsageSummary(codexResponsesGuardSnapshot)
      : undefined,
    protocolValidatedSuccess: upstreamResponse.ok && streamResult.protocolValidated,
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
  let parsedJsonBody: GatewayNonStreamJsonBody | undefined
  let codexGuardedCompleteBody: Buffer | undefined
  let codexGuardedCompleteBodyText: string | undefined
  let codexResponsesGuard: CodexResponsesGuardUsageSummary | undefined
  let bodyOmission: StreamBodyOmissionSummary | undefined
  let firstTokenMs: number | undefined
  let usage = emptyUsage()
  let errorPayload: Record<string, unknown> = {}
  let firstOutputMarked = false
  const markSemanticOutput = () => {
    if (firstOutputMarked) return
    firstOutputMarked = true
    markFirstOutput?.()
  }
  const responseProtocol = gatewayProtocolResponseProtocolForRequest(req, account)
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  const forwardedResponseSuccessful = upstreamResponse.ok
  try {
    if (upstreamResponse.ok && isGeminiInteractionDeleteRequest(req, account)) {
      await deleteGeminiInteractionBeforeDownstreamCommit({ req, auditCapture, account, usageContext })
    }
    if (!upstreamResponse.body) {
      prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
      input.downstreamCommitState.markTransportCommitted()
      endResponse(res)
      input.downstreamCommitState.markSemanticCommitted()
      firstTokenMs = Date.now() - startedAt
      markFirstOutput?.()
    } else if (upstreamResponse.body) {
      const contentType = upstreamResponse.headers.get('content-type') ?? ''
      const protocolValidationEnabled = isOpenAIJsonResponseContentType(contentType)
        && nonStreamJsonProtocolValidationAllowed(input)
      const inspectJsonResponse = isCodexResponsesGuardEligible(input)
        || (
          isOpenAIJsonResponseContentType(contentType)
          && (protocolValidationEnabled || shouldBufferNonStreamJsonResponse(input))
        )
      if (inspectJsonResponse) {
        const pipeResult = await pipeNonStreamUpstreamResponseForInspection(upstreamResponse.body, res, {
          startedAt,
          inspectBytes: nonStreamResponseInspectionMaxBytes,
          captureBody: auditCapture.shouldCaptureSuccessPayloads(),
          signal,
          firstByteTimeoutMs: input.timeoutProfile.firstByteTimeoutMs,
          firstByteDeadlineMs: input.firstByteDeadlineMs,
          responsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
          maxLifetimeMs: input.timeoutProfile.uncommittedAttemptMaxLifetimeMs,
          onFirstByteDeadline: input.onFirstByteDeadline,
          onFirstByteDeadlineSuperseded: input.onFirstByteDeadlineSuperseded,
          prepareDownstream: () => {
            prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
            input.downstreamCommitState.markTransportCommitted()
          },
          onChunkWritten: (bytesWritten) => {
            input.downstreamCommitState.markSemanticCommitted(bytesWritten)
            markSemanticOutput()
          },
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
          }
        })
        responseBody = pipeResult.capturedBody
        responseBodyText = pipeResult.captureTruncated ? undefined : pipeResult.capturedBodyText
        responseUsageText = pipeResult.usageTailText
        firstTokenMs = pipeResult.firstByteMs
        if (pipeResult.fullyBuffered) {
          const completeBody = pipeResult.completeBody ?? Buffer.alloc(0)
          const completeBodyText = pipeResult.completeBodyText ?? completeBody.toString('utf8')
          parsedJsonBody = parseGatewayNonStreamJsonBody(completeBodyText, upstreamResponse.headers)
          if (isGeminiInteractionCreateRequest(req)) {
            responseResourceId = parsedJsonBody.status === 'valid'
              ? geminiInteractionIdFromParsedResponse(parsedJsonBody.value)
              : undefined
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
            parsedJsonBody,
            firstTokenMs,
            responseInspectionPolicies: managementResponseInspectionPoliciesForInput(input),
            clientStrategy: input.clientStrategy,
            accountStateMutationEnabled: accountStateMutationEnabled !== false,
            automaticAccountStateMutationEnabled: input.automaticAccountStateMutationEnabled !== false,
            protocolValidationEnabled,
            downstreamCommitState: input.downstreamCommitState,
            onCodexResponsesGuardResult: (result) => {
              if (result.outcome !== 'clean') {
                codexResponsesGuard = codexResponsesGuardUsageSummary(result)
              }
              if (result.outcome === 'repaired_safe' || result.outcome === 'repaired_bridge') {
                codexGuardedCompleteBodyText = JSON.stringify(result.value)
                codexGuardedCompleteBody = Buffer.from(codexGuardedCompleteBodyText, 'utf8')
                parsedJsonBody = gatewayNonStreamJsonBodyFromValue(result.value)
              }
            },
            sessionAffinityKey
          })
          if (jsonInspectionResult) {
            return jsonInspectionResult
          }
          const hybridQualityResult = await inspectBufferedHybridQualityResponse({
            ...input,
            responseBody: completeBody,
            responseBodyText: completeBodyText,
            parsedJsonBody,
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
          const downstreamBody = codexGuardedCompleteBody ?? completeBody
          const downstreamBodyText = codexGuardedCompleteBodyText ?? completeBodyText
          responseSemanticText = downstreamBodyText
          if (firstTokenMs === undefined) {
            firstTokenMs = Date.now() - startedAt
          }
          markSemanticOutput()
          prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
          if (codexGuardedCompleteBody) {
            res.setHeader('content-length', String(downstreamBody.length))
          }
          input.downstreamCommitState.markTransportCommitted()
          res.send(downstreamBody)
          input.downstreamCommitState.markSemanticCommitted(downstreamBody.length)
          if (!responseBody && auditCapture.shouldCaptureSuccessPayloads()) {
            responseBody = downstreamBody
            responseBodyText = downstreamBodyText
          }
          responseUsageText = downstreamBodyText
          } else {
            codexResponsesGuard = recordCodexResponsesGuardCoverageGap(input)
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
          responsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
          maxLifetimeMs: input.timeoutProfile.uncommittedAttemptMaxLifetimeMs,
          onFirstByteDeadline: input.onFirstByteDeadline,
          onFirstByteDeadlineSuperseded: input.onFirstByteDeadlineSuperseded,
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
            markSemanticOutput()
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
    const responsePrecommitDeadlineError = responsePrecommitDeadlineErrorFor(error)
    const responsePrecommitDeadlineExceeded = responsePrecommitDeadlineError !== undefined
    if (
      (
        responsePrecommitDeadlineExceeded
        || (
          input.firstByteDeadlineMs !== undefined
          && isGatewayFirstByteTimeoutError(error)
          && error.source === 'configured_deadline'
          && error.timeoutMs === input.firstByteDeadlineMs
        )
      )
      && !res.headersSent
      && !res.writableEnded
      && !res.destroyed
    ) {
      const errorMessage = responsePrecommitDeadlineError?.message
        ?? (error instanceof Error ? error.message : '上游非流式响应首字超时')
      const errorCode = responsePrecommitDeadlineError?.code
        ?? (isGatewayFirstByteTimeoutError(error) ? error.code : 'first_byte_timeout')
      await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        success: false,
        errorPhase: 'upstream_response',
        errorCode,
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
        errorCode,
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
        label: responsePrecommitDeadlineExceeded
          ? 'gateway_request_wall_budget_exhausted'
          : 'normal_route_first_byte_deadline_cutover',
        metadata: {
          accountId: account.id,
          accountName: account.name,
          timeoutMs: responsePrecommitDeadlineExceeded
            ? Math.max(0, (responsePrecommitDeadlineError?.deadlineAtMs ?? startedAt) - startedAt)
            : input.firstByteDeadlineMs,
          deadlineAtMs: responsePrecommitDeadlineError?.deadlineAtMs,
          message: errorMessage
        }
      })
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'normal_route_first_byte_timeout',
        excludeCurrentAccount: true,
        message: errorMessage,
        errorCode
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
      const errorCode = responsePrecommitDeadlineError?.code ?? 'upstream_body_interrupted'
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
      // The downstream response is already committed and cannot be replayed.
      // Keep this request's audit/usage failure; shared account state still
      // requires the independent fixed-model health confirmation below.
      await recordCompletedUpstreamAttempt(req, {
        ...usageContext,
        account,
        statusCode: upstreamResponse.status,
        success: false,
        stream: isEffectiveOpenAIStreamRequest(req, account),
        firstTokenMs,
        startedAt,
        usage: emptyUsage(),
        errorCode,
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
        errorCode,
        errorMessage
      })
      if (responsePrecommitDeadlineError) {
        auditCapture.addGatewayMetadata({
          label: 'gateway_request_wall_budget_exhausted',
          metadata: {
            accountId: account.id,
            accountName: account.name,
            deadlineAtMs: responsePrecommitDeadlineError.deadlineAtMs,
            timeoutMs: Math.max(0, responsePrecommitDeadlineError.deadlineAtMs - startedAt),
            transferredBytes: error.partialResult.transferredBytes,
            message: errorMessage
          }
        })
      }
      auditCapture.finalize({
        outcome: 'upstream_failed',
        success: false,
        statusCode: upstreamResponse.status,
        responseHeaders: responseHeadersToObject(res),
        responseBody: responseBodyText,
        responsePartType: 'gateway_response',
        errorPhase: 'upstream_response',
        errorCode,
        errorMessage,
        accountId: account.id,
        firstTokenMs
      })
      const provenTransportFailure = !responsePrecommitDeadlineError
        && isProvenUpstreamBodyTransportError(error)
      if (provenTransportFailure) {
        dispatchRequestFailureAccountHealthCheck(req, usageContext.trafficSource, account.id)
      }
      return {
        alreadyFinalized: true,
        errorCode,
        ...(provenTransportFailure
          ? {
              transportFailure: {
                kind: 'read_incomplete' as const,
                reason: '上游非流式响应读取未完成'
              }
            }
          : responsePrecommitDeadlineError
            ? {}
            : { gatewayLocalFailure: true })
      }
    }
    throw error
  }
  const availableJsonBodyText = responseBodyText ?? responseSemanticText ?? responseUsageText
  if (
    !parsedJsonBody
    && availableJsonBodyText !== undefined
    && (forwardedResponseSuccessful || interpretUpstreamResponseSemantics)
  ) {
    parsedJsonBody = parseGatewayNonStreamJsonBody(availableJsonBodyText, upstreamResponse.headers)
  }
  if (parsedJsonBody?.status === 'valid') {
    usage = forwardedResponseSuccessful
      ? parseGatewayProtocolUsageFromJsonValueForRequest(req, account, parsedJsonBody.value)
      : parseGatewayProtocolUsageFromJsonValue(account, parsedJsonBody.value)
  } else if (responseUsageText) {
    const skipFullDocumentParse = parsedJsonBody?.status === 'invalid' || !forwardedResponseSuccessful
    usage = forwardedResponseSuccessful
      ? parseGatewayProtocolUsageFromJsonTextFragmentForRequest(req, account, responseUsageText, skipFullDocumentParse)
      : parseGatewayProtocolUsageFromJsonTextFragment(account, responseUsageText, skipFullDocumentParse)
  }
  if (forwardedResponseSuccessful) {
    usage = applyNonStreamUsageFallback({
      req,
      account,
      usage,
      parsedJsonBody,
      endpoint: usageContext.endpoint
    })
  }
  if (interpretUpstreamResponseSemantics && !upstreamResponse.ok) {
    errorPayload = parsedJsonBody?.status === 'valid'
      ? parseGatewayProtocolErrorPayloadFromJsonValue(account, parsedJsonBody.value)
      : parsedJsonBody
        ? {}
        : parseGatewayProtocolErrorPayload(account, responseBodyText ?? '', upstreamResponse.headers)
  }
  if (forwardedResponseSuccessful) {
    bodyOmission = nonStreamImageResponseBodyOmission(
      responseBodyText ?? responseSemanticText ?? responseBody?.toString('utf8'),
      responseBody?.byteLength,
      parsedJsonBody
    )
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

  publishGatewayNonStreamJsonBody(res, parsedJsonBody)

  return {
    alreadyFinalized: false,
    usage,
    firstTokenMs,
    responseBodyText,
    responseResourceId,
    bodyOmission,
    codexResponsesGuard,
    protocolValidatedSuccess: forwardedResponseSuccessful && protocolValidatedNonStreamResponse({
      req,
      account,
      responseBodyText: responseBodyText ?? responseSemanticText ?? responseUsageText,
      parsedJsonBody,
      statusCode: upstreamResponse.status
    }),
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

export function nonStreamJsonProtocolValidationAllowed(input: {
  req: Request
  account: UpstreamAccount
  upstreamResponse: Pick<GatewayUpstreamResponse, 'ok'>
}): boolean {
  if (!input.upstreamResponse.ok) return false
  const endpointFamily = gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account)
  if (endpointFamily !== 'chat_completions' && endpointFamily !== 'messages') return false
  const requestLane = resolveOpenAIGatewayRequestLane(input.req)
  return automaticUpstreamReplayAllowedAfterDispatch(input.req, requestLane)
}

function isCodexResponsesGuardEligible(input: HandleUpstreamResponseInput): boolean {
  return Boolean(
    runtimeConfig.codexProtocolGuard.mode !== 'off'
    && input.upstreamResponse.ok
    && input.upstreamResponse.codexResponsesGuardMarker
    && input.clientStrategy?.clientProfile === 'codex'
    && gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account) === 'responses'
  )
}

function createCodexResponsesGuardForInput(
  input: HandleUpstreamResponseInput
): CodexResponsesResponseGuard | undefined {
  if (!isCodexResponsesGuardEligible(input)) return undefined
  const mode = resolveCodexResponsesGuardMode({
    globalMode: runtimeConfig.codexProtocolGuard.mode,
    credentials: input.account.credentials
  })
  if (mode === 'off') return undefined
  const marker = input.upstreamResponse.codexResponsesGuardMarker
  if (!marker) return undefined
  return createCodexResponsesResponseGuard({
    marker,
    downstreamCommitState: input.downstreamCommitState,
    mode,
    envelopeKind: isCodexResponsesCompactRequest(input.req) ? 'compact' : 'response'
  })
}

function isCodexResponsesCompactRequest(req: Request): boolean {
  const path = (req.originalUrl || req.path || '').split('?', 1)[0].replace(/^\/v1(?=\/|$)/, '') || '/'
  return req.method.toUpperCase() === 'POST' && path === '/responses/compact'
}

function recordCodexResponsesGuardCoverageGap(input: HandleUpstreamResponseInput): CodexResponsesGuardUsageSummary | undefined {
  const guard = createCodexResponsesGuardForInput(input)
  if (!guard) return undefined
  try {
    const result = guard.observeCoverageGap()
    input.auditCapture.addGatewayMetadata({
      label: 'codex_responses_protocol_guard',
      metadata: codexResponsesGuardAuditMetadata(result)
    })
    return codexResponsesGuardUsageSummary(result)
  } finally {
    guard.dispose()
  }
}

function managementResponseInspectionPoliciesForInput(
  input: HandleUpstreamResponseInput
): ResponseInspectionPolicySummary[] | undefined {
  return input.responseInspectionPolicies
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
    markGatewayForcedDownstreamClose(input.res, 'stream_retry_signal_unavailable')
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

function isResponsePrecommitDeadlineStreamResult(
  streamResult: { errorCode?: string }
): boolean {
  return streamResult.errorCode === 'gateway_request_wall_budget_exhausted'
}

function isStreamUpstreamResponseCommitted(streamResult: {
  upstreamResponseBytesWritten: number
  semanticCommitted: boolean
}): boolean {
  return streamResult.upstreamResponseBytesWritten > 0 || streamResult.semanticCommitted
}

function responsePrecommitDeadlineErrorFor(error: unknown) {
  if (isGatewayResponsePrecommitDeadlineError(error)) return error
  if (
    error instanceof NonStreamUpstreamBodyPipeError
    && isGatewayResponsePrecommitDeadlineError(error.originalError)
  ) {
    return error.originalError
  }
  return undefined
}

function nonStreamImageResponseBodyOmission(
  bodyText: string | undefined,
  capturedBytes: number | undefined,
  parsedJsonBody?: GatewayNonStreamJsonBody
): StreamBodyOmissionSummary | undefined {
  if (!bodyText || !nonStreamBodyLooksLikeImageGenerationPayload(bodyText, parsedJsonBody)) return undefined
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

function nonStreamBodyLooksLikeImageGenerationPayload(
  bodyText: string,
  parsedJsonBody?: GatewayNonStreamJsonBody
): boolean {
  if (parsedJsonBody?.status === 'valid') {
    return jsonContainsImageGenerationResult(parsedJsonBody.value)
  }
  return bodyText.includes('"type":"image_generation_call"') && bodyText.includes('"result"')
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
  parsedJsonBody: GatewayNonStreamJsonBody
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
  const usage = input.parsedJsonBody.status === 'valid'
    ? parseGatewayProtocolUsageFromJsonValueForRequest(input.req, input.account, input.parsedJsonBody.value)
    : parseGatewayProtocolUsageFromJsonTextFragmentForRequest(input.req, input.account, input.responseBodyText, true)
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
  parsedJsonBody?: GatewayNonStreamJsonBody
  endpoint: string
}): ParsedUsage {
  if (input.parsedJsonBody?.status !== 'valid') return input.usage
  const frames = extractGatewayProtocolJsonSemanticFramesForRequest(input.parsedJsonBody.value, input.req, input.account)
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

export async function applyHandledUpstreamRoutingEffects(
  input: FinalizeHandledUpstreamResponseInput
): Promise<void> {
  const {
    account,
    upstreamResponse,
    auditCapture,
    settings,
    usageContext,
    result
  } = input
  const interpretUpstreamResponseSemantics = input.clientStrategy
    ? gatewayClientAllowsUpstreamSemanticInterpretation(input.clientStrategy)
    : false
  const forwardedResponseSuccessful = upstreamResponse.ok
  const protocolValidatedSuccess = forwardedResponseSuccessful && result.protocolValidatedSuccess === true
  if (protocolValidatedSuccess && !isAccountDiagnosticTrafficSource(usageContext.trafficSource)) {
    const clearedClientIpErrorCircuit = await applyHandledUpstreamRoutingEffectSafely(
      account,
      auditCapture,
      'client_ip_error_circuit_recovery',
      async () => await recordClientIpErrorCircuitSuccessAsync({
        systemAccountId: usageContext.systemAccountId,
        apiKeyId: usageContext.apiKeyId,
        groupId: usageContext.groupId,
        clientIp: usageContext.clientIp,
        endpoint: usageContext.endpoint
      })
    )
    if (clearedClientIpErrorCircuit) {
      getRequestLogger().info({
        event: 'gateway_client_ip_error_circuit_recovered',
        accountId: account.id,
        systemAccountId: usageContext.systemAccountId,
        apiKeyId: usageContext.apiKeyId,
        groupId: usageContext.groupId,
        clientIp: usageContext.clientIp
      }, '客户端 IP 级错误熔断状态已按完整成功响应恢复')
      auditCapture.addGatewayMetadata({
        label: 'client_ip_error_circuit',
        metadata: {
          recovered: true
        }
      })
    }
  }
  if (interpretUpstreamResponseSemantics && protocolValidatedSuccess) {
    if (!isAccountDiagnosticTrafficSource(usageContext.trafficSource)) {
      if (input.automaticAccountStateMutationEnabled !== false) {
        const clearedProxyFailure = await applyHandledUpstreamRoutingEffectSafely(
          account,
          auditCapture,
          'upstream_bucket_recovery',
          async () => await recordGatewayUpstreamBucketSuccessAsync(account)
        )
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
    }
    if (input.automaticAccountStateMutationEnabled !== false) {
      await applyHandledUpstreamRoutingEffectSafely(
        account,
        auditCapture,
        'account_success_recovery',
        async () => await applyAccountErrorHandlingWithCacheInvalidation(account, {
          success: true,
          settings,
          trafficSource: usageContext.trafficSource
        })
      )
    }
    if (input.automaticAccountStateMutationEnabled !== false
      && usageContext.trafficSource !== 'gateway'
      && (account.streamFailureCount > 0 || account.streamFailureWindowStartedAt || account.lastErrorMessage)) {
      clearAccountStreamFailureStateWithCacheInvalidation(account)
    }
  }

  if (protocolValidatedSuccess) {
    await applyHandledUpstreamRoutingEffectSafely(
      account,
      auditCapture,
      'account_api_key_success_recovery',
      async () => await recordGatewayAccountApiKeySuccess(account, {
        source: 'upstream_attempt_completed',
        trafficSource: usageContext.trafficSource
      })
    )
  }
}

async function applyHandledUpstreamRoutingEffectSafely<T>(
  account: UpstreamAccount,
  auditCapture: AuditCaptureContext,
  operation: string,
  effect: () => Promise<T>
): Promise<T | undefined> {
  try {
    return await effect()
  } catch (error) {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'gateway_upstream_routing_effect_failed',
      accountId: account.id,
      operation
    }), '上游请求已完成，路由状态结算失败已隔离')
    auditCapture.addGatewayMetadata({
      label: 'routing_effect_failure',
      metadata: {
        accountId: account.id,
        operation
      }
    })
    return undefined
  }
}

export async function finalizeHandledUpstreamResponse(input: FinalizeHandledUpstreamResponseInput): Promise<void> {
  const {
    req,
    res,
    account,
    upstreamResponse,
    upstreamUrl,
    auditCapture,
    usageContext,
    startedAt,
    result
  } = input
  if (input.routingEffectsApplied !== true) {
    await applyHandledUpstreamRoutingEffects(input)
  }
  const forwardedResponseSuccessful = upstreamResponse.ok
  const protocolValidatedSuccess = forwardedResponseSuccessful && result.protocolValidatedSuccess === true

  await recordCompletedUpstreamAttempt(req, {
    ...usageContext,
    account,
    stream: isEffectiveOpenAIStreamRequest(req, account),
    statusCode: upstreamResponse.status,
    success: forwardedResponseSuccessful,
    protocolValidatedSuccess,
    accountApiKeySuccessAlreadyRecorded: true,
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
        bodyOmission: result.bodyOmission,
        codexResponsesGuard: result.codexResponsesGuard
      })
      : forwardedResponseSuccessful
        ? result.codexResponsesGuard
          ? buildUsageResponseSnapshot({
            upstreamUrl,
            statusCode: upstreamResponse.status,
            headers: upstreamResponse.headers,
            codexResponsesGuard: result.codexResponsesGuard
          })
          : undefined
        : buildUsageResponseSnapshot({
          upstreamUrl,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          bodyText: result.responseBodyText,
          codexResponsesGuard: result.codexResponsesGuard
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
