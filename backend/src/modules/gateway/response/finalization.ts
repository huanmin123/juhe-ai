import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import { type GatewaySettings } from '../policy/account-error-policy.service.js'
import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  clearAccountStreamFailureStateWithCacheInvalidation,
  handleStreamFailure,
} from '../runtime/account-effects.js'
import { rememberCodexTurnStreamFailure } from '../client-profiles/codex-turn-retry.service.js'
import { downstreamConnectionClosedMessage } from './client-abort.js'
import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import {
  confirmClientIpAccountAvoidanceAfterFinalFailure,
  confirmClientIpAccountAvoidanceAfterSuccess,
  rememberClientIpAccountPendingFailure,
  type ClientIpAccountAvoidanceTracker
} from '../runtime/client-ip-account-avoidance.service.js'
import {
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from '../runtime/account-side-effects.service.js'
import { recordClientIpErrorCircuitSuccess } from '../runtime/client-ip-error-circuit.service.js'
import {
  NonStreamUpstreamBodyPipeError,
  endResponse,
  pipeNonStreamUpstreamResponse,
  pipeNonStreamUpstreamResponseForInspection,
  nonStreamResponseCaptureBytes,
  readUpstreamBodyLimited
} from '../upstream/body.js'
import {
  responseInspectionAuditMetadata
} from '../audit/metadata.js'
import {
  forgetOpenAIAccountForSession
} from '../runtime/session-affinity.service.js'
import {
  gatewayErrorPayload,
  gatewayErrorPayloadForProtocol,
  isOpenAIJsonResponseContentType,
  sendGatewayErrorResponse,
  writeGatewayStreamFailureEvent
} from './responses.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  pipeUpstreamStream,
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
  upstreamRequestTimeoutMs,
  UpstreamRequestAbortedError,
  type GatewayUpstreamResponse
} from '../upstream/request.js'
import {
  buildUsageResponseSnapshot,
  type UsageRequestSnapshot
} from '../usage/snapshots.js'
import {
  emptyUsage,
  type ParsedUsage
} from '../usage/types.js'
import {
  applyGatewayProtocolStreamUsageFallback,
  extractGatewayProtocolJsonSemanticFrames,
  gatewayProtocolClientErrorProtocolForProfile,
  gatewayProtocolDefaultClientProfileForProfile,
  gatewayProtocolResponseEndpointFamilyForRequest,
  gatewayProtocolResponseProtocolForProfile,
  parseGatewayProtocolUsageFromJsonBuffer,
  parseGatewayProtocolUsageFromJsonTextFragment,
  parseGatewayProtocolErrorPayload
} from '../protocols/registry.js'
import {
  requestModel
} from '../request/metadata.js'
import {
  estimateTokenCountFromText
} from '../protocols/openai-v1/stream-events.js'
import {
  recordGatewayUpstreamBucketSuccess,
  suppressGatewayUpstreamBucketLocallyForSeconds
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
  shouldRetryCodexPreCommitStreamFailureOnServer,
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

export type { StreamServerRetryReason } from './stream-finalization-retry-decision.js'
export type { UpstreamResponseHandlingResult } from './response-handling-result.js'

interface HandleUpstreamResponseInput {
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
  signal: AbortSignal
  sessionAffinityKey?: string
  clientStrategy?: OpenAIGatewayClientStrategyContext
  responseInspectionPolicies?: ResponseInspectionPolicySummary[]
  hybridRoute?: HybridGatewayRuntimeRoute
  markFirstOutput?: () => void
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
  accountStateMutationEnabled?: boolean
  codexTurnAccountAvoidanceApplied?: boolean
}

interface FinalizeHandledUpstreamResponseInput extends HandleUpstreamResponseInput {
  result: Exclude<UpstreamResponseHandlingResult, { alreadyFinalized: true } | { retryUpstream: true }>
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
    accountStateMutationEnabled
  } = input

  const clientErrorProtocol = gatewayProtocolClientErrorProtocolForProfile(account)
  const responseProtocol = gatewayProtocolResponseProtocolForProfile(account)
  const responseEndpointFamily = gatewayProtocolResponseEndpointFamilyForRequest(req, account)
  const defaultClientProfile = gatewayProtocolDefaultClientProfileForProfile(account)
  if (!upstreamResponse.body) {
    const responsePayload = gatewayErrorPayload('上游响应体为空', 'upstream_response_error')
    const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, clientErrorProtocol)
    sendGatewayErrorResponse(res, upstreamResponse.status, responsePayload, { protocol: clientErrorProtocol })
    forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
    auditCapture.completeAttempt(auditAttemptId, {
      statusCode: upstreamResponse.status,
      responseHeaders: upstreamResponse.headers,
      success: false,
      errorPhase: 'upstream_response',
      errorMessage: '上游响应体为空'
    })
    recordCompletedUpstreamAttempt(req, {
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
        errorMessage: '上游响应体为空'
      }),
      errorMessage: '上游响应体为空'
    })
    auditCapture.finalize({
      outcome: 'stream_failed',
      success: false,
      statusCode: upstreamResponse.status,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(clientPayload),
      responsePartType: 'gateway_response',
      errorPhase: 'upstream_response',
      errorMessage: '上游响应体为空',
      accountId: account.id
    })
    return { alreadyFinalized: true }
  }

  let streamResult: Awaited<ReturnType<typeof pipeUpstreamStream>>
  try {
    streamResult = await pipeUpstreamStream(
      upstreamResponse.body,
      res,
      settings,
      startedAt,
      (message, errorCode, context) => handleStreamFailure(account, message, settings, errorCode, context, usageContext, accountStateMutationEnabled !== false),
      signal,
      {
        clientRetryEnabled: clientStrategy?.allowCodexStreamClientRetry === true,
        retryBeforeDownstreamWriteUntilOutput: true,
        onFirstOutput: markFirstOutput,
        captureSuccessPayloads: auditCapture.shouldCaptureSuccessPayloads(),
        responseInspectionPolicies: resolveRuntimeResponseInspectionPolicies({
          account,
          managementPolicies: input.responseInspectionPolicies
        }),
        responseInspectionContext: {
          clientProfile: clientStrategy?.clientProfile ?? defaultClientProfile,
          accountClientCompatibility: account.clientCompatibility,
          codexCompactionExpected: clientStrategy?.codexCompactionExpected
        },
        responseProtocol,
        endpointFamily: responseEndpointFamily,
        prepareDownstream: () => prepareUpstreamResponseForDownstream(res, upstreamResponse, true)
      }
    )
  } catch (error) {
    if (isUpstreamRequestAbortedError(error) || signal.aborted) {
      forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
      recordClientAbortedUpstreamAttempt(req, {
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

  const streamUsageFallback = applyGatewayProtocolStreamUsageFallback(req, account, streamResult.usage, {
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
  await applyResponseInspectionObservationHandling(streamResult, account, settings, auditCapture, accountStateMutationEnabled !== false)
  if (streamResult.responseInspection) {
    await applyResponseInspectionPolicyRuntimeSideEffects(streamResult.responseInspection, account, settings, accountStateMutationEnabled !== false)
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
    const requestSnapshot = usageRequestSnapshotWithBodyOmission(usageContext.requestSnapshot, streamResult.bodyOmission)
    forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
    recordCompletedUpstreamAttempt(req, {
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
    rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
      statusCode: upstreamResponse.status,
      errorCode: streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode,
      errorPhase: 'stream',
      errorMessage: streamResult.message,
      endpoint: usageContext.endpoint
    })
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
    if (shouldRetryCodexPreCommitStreamFailureOnServer(streamResult, clientStrategy, res)) {
      auditCapture.addGatewayMetadata({
        label: 'codex_pre_commit_stream_server_retry',
        metadata: {
          errorCode: streamResult.errorCode,
          message: streamResult.message,
          downstreamBytesWritten: streamResult.downstreamBytesWritten,
          outputReceived: streamResult.outputReceived,
          accountId: account.id
        }
      })
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        retryReason: 'codex_pre_commit_stream_failure',
        excludeCurrentAccount: true,
        message: streamResult.message,
        errorCode: streamResult.errorCode,
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
    if (shouldRememberCodexTurnStreamFailure(streamResult, clientStrategy)) {
      const codexTurnFailure = rememberCodexTurnStreamFailure(clientStrategy, account.id, {
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
    const clientFailureResponseBody = writePreCommitStreamFailureToClient(
      res,
      upstreamResponse,
      streamResult,
      clientErrorProtocol
    )
    const clientIpAvoidanceResult = confirmClientIpAccountAvoidanceAfterFinalFailure(
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
      }, '流式失败已返回客户端，客户端 IP 级账号回避状态已立即确认')
      auditCapture.addGatewayMetadata({
        label: 'client_ip_account_avoidance_update',
        metadata: {
          reason: 'stream_failure_finalized_to_client',
          confirmedAccountIds: clientIpAvoidanceResult.confirmedAccountIds
        }
      })
    }
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
    bodyOmission: streamResult.bodyOmission,
    errorPayload: {}
  }
}

function writePreCommitStreamFailureToClient(
  res: Response,
  upstreamResponse: GatewayUpstreamResponse,
  streamResult: GatewayStreamPipeResult,
  protocol: 'openai' | 'anthropic'
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
  const failureEvent = writeGatewayStreamFailureEvent(res, streamResult.message, streamResult.errorCode, protocol)
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
  accountStateMutationEnabled: boolean
): Promise<void> {
  await applyResponseInspectionObservationDecisions(
    streamResult.responseInspectionObservations,
    streamResult.responseInspectionObservationOmittedCount,
    account,
    settings,
    auditCapture,
    accountStateMutationEnabled
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
    markFirstOutput
  } = input

  if (signal.aborted) {
    throw new UpstreamRequestAbortedError('请求已取消', true)
  }
  let responseBody: Buffer | undefined
  let responseBodyText: string | undefined
  let responseUsageText: string | undefined
  let responseSemanticText: string | undefined
  let firstTokenMs: number | undefined
  let usage = emptyUsage()
  let errorPayload: Record<string, unknown> = {}
  try {
    if (!upstreamResponse.body) {
      responseBody = Buffer.alloc(0)
      responseBodyText = ''
      firstTokenMs = Date.now() - startedAt
      prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
      markFirstOutput?.()
      res.end()
    } else if (upstreamResponse.ok) {
      const contentType = upstreamResponse.headers.get('content-type') ?? ''
      const inspectJsonResponse = isOpenAIJsonResponseContentType(contentType)
      if (inspectJsonResponse) {
        const pipeResult = await pipeNonStreamUpstreamResponseForInspection(upstreamResponse.body, res, {
          startedAt,
          inspectBytes: nonStreamResponseInspectionMaxBytes,
          captureBody: auditCapture.shouldCaptureSuccessPayloads(),
          signal,
          firstByteTimeoutMs: upstreamRequestTimeoutMs(req, input.settings, account),
          prepareDownstream: () => prepareUpstreamResponseForDownstream(res, upstreamResponse, false),
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
            responseInspectionPolicies: input.responseInspectionPolicies,
            clientStrategy: input.clientStrategy,
            accountStateMutationEnabled: accountStateMutationEnabled !== false,
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
          responseSemanticText = completeBodyText
          if (firstTokenMs === undefined) {
            firstTokenMs = Date.now() - startedAt
          }
          prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
          res.send(completeBody)
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
          firstByteTimeoutMs: upstreamRequestTimeoutMs(req, input.settings, account),
          prepareDownstream: () => prepareUpstreamResponseForDownstream(res, upstreamResponse, false),
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
      const readResult = await readUpstreamBodyLimited(upstreamResponse.body, {
        startedAt,
        signal,
        onFirstByte: markFirstOutput
      })
      responseBody = readResult.body
      responseBodyText = readResult.diagnosticBodyText
      responseUsageText = responseBodyText
      firstTokenMs = readResult.firstByteMs ?? Date.now() - startedAt
      if (readResult.truncated) {
        logger.warn({
          event: 'gateway_upstream_error_body_truncated',
          accountId: account.id,
          statusCode: upstreamResponse.status,
          readBytes: readResult.readBytes,
          endpoint: usageContext.endpoint
        }, '上游错误响应体超过网关捕获上限，已截断用于诊断')
      }
      prepareUpstreamResponseForDownstream(res, upstreamResponse, false)
      res.send(readResult.body)
    }
  } catch (error) {
    if (isUpstreamRequestAbortedError(error) || signal.aborted) {
      recordClientAbortedUpstreamAttempt(req, {
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
      forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
      if (accountStateMutationEnabled !== false) {
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
            forcePrecheck: localSuppression.action === 'precheck_required'
          })
        } else {
          applyAccountErrorHandlingWithCacheInvalidation(account, {
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
      recordCompletedUpstreamAttempt(req, {
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
    usage = parseGatewayProtocolUsageFromJsonBuffer(account, responseBody)
  } else if (upstreamResponse.ok) {
    usage = parseGatewayProtocolUsageFromJsonTextFragment(account, responseUsageText)
  }
  if (upstreamResponse.ok) {
    usage = applyNonStreamUsageFallback({
      req,
      account,
      usage,
      responseBody,
      responseBodyText: responseBodyText ?? responseSemanticText,
      endpoint: usageContext.endpoint
    })
  }
  if (!upstreamResponse.ok) {
    errorPayload = parseGatewayProtocolErrorPayload(account, responseBodyText ?? '', upstreamResponse.headers)
  }
  auditCapture.completeAttempt(auditAttemptId, {
    statusCode: upstreamResponse.status,
    responseHeaders: upstreamResponse.headers,
    responseBody,
    success: upstreamResponse.ok,
    errorPhase: upstreamResponse.ok ? undefined : 'upstream_response',
    errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
    errorMessage: typeof errorPayload.message === 'string' ? errorPayload.message : undefined
  })

  return {
    alreadyFinalized: false,
    usage,
    firstTokenMs,
    responseBodyText,
    errorPayload
  }
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
  const quality = await inspectHybridGatewayQuality({
    req: input.req,
    apiKeyRecord: hybridRoute.apiKeyRecord,
    config: hybridRoute.config,
    scoring: hybridRoute.scoring,
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
  const usage = parseGatewayProtocolUsageFromJsonBuffer(input.account, input.responseBody)
  const message = quality.result?.reason
    ? `混合路由质量评分未通过：${quality.result.reason}`
    : '混合路由质量评分未通过'
  const errorCode = `hybrid_quality_${quality.result?.failureType ?? 'failed'}`
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
  return {
    alreadyFinalized: false,
    retryUpstream: true,
    retryReason: 'hybrid_quality',
    excludeCurrentAccount: false,
    message,
    errorCode,
    hybridQuality: quality
  }
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
  const frames = extractGatewayProtocolJsonSemanticFrames(parsed, input.req, input.account)
  const outputText = frames
    .filter((frame) => (frame.frameType === 'output_text_delta' || frame.frameType === 'output_text_done') && typeof frame.text === 'string')
    .map((frame) => frame.text ?? '')
    .filter(Boolean)
    .join('\n')
  const outputReceived = outputText.length > 0 || frames.some((frame) => frame.visibleOutput === true)
  if (!outputReceived) return input.usage
  const fallback = applyGatewayProtocolStreamUsageFallback(input.req, input.account, input.usage, {
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

export function finalizeHandledUpstreamResponse(input: FinalizeHandledUpstreamResponseInput): void {
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
  if (upstreamResponse.ok) {
    const clearedProxyFailure = recordGatewayUpstreamBucketSuccess(account)
    if (clearedProxyFailure) {
      getRequestLogger().info({
        event: 'gateway_upstream_failure_bucket_recovered',
        accountId: account.id,
        accountName: account.name
      }, '上游桶运行态失败已按成功响应恢复')
      auditCapture.addGatewayMetadata({
        label: 'upstream_bucket_health',
        metadata: {
          recovered: true
        }
      })
    }
    if (input.accountStateMutationEnabled !== false) {
      applyAccountErrorHandlingWithCacheInvalidation(account, {
        success: true,
        settings,
        trafficSource: usageContext.trafficSource
      })
    }
    const clearedClientIpErrorCircuit = recordClientIpErrorCircuitSuccess({
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
    const clientIpAvoidanceResult = confirmClientIpAccountAvoidanceAfterSuccess(
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
    if (input.accountStateMutationEnabled !== false && (account.streamFailureCount > 0 || account.streamFailureWindowStartedAt || account.lastErrorMessage)) {
      clearAccountStreamFailureStateWithCacheInvalidation(account)
    }
  }

  recordCompletedUpstreamAttempt(req, {
    ...usageContext,
    account,
    stream: isEffectiveOpenAIStreamRequest(req, account),
    statusCode: upstreamResponse.status,
    success: upstreamResponse.ok,
    firstTokenMs: result.firstTokenMs,
    startedAt,
    usage: result.usage,
    errorCode: typeof result.errorPayload.code === 'string' ? result.errorPayload.code : undefined,
    errorMessage: typeof result.errorPayload.message === 'string' ? result.errorPayload.message : undefined,
    requestSnapshot: result.bodyOmission
      ? usageRequestSnapshotWithBodyOmission(usageContext.requestSnapshot, result.bodyOmission)
      : upstreamResponse.ok ? undefined : usageContext.requestSnapshot,
    responseSnapshot: result.bodyOmission
      ? buildUsageResponseSnapshot({
        upstreamUrl,
        statusCode: upstreamResponse.status,
        headers: upstreamResponse.headers,
        bodyOmission: result.bodyOmission
      })
      : upstreamResponse.ok ? undefined : buildUsageResponseSnapshot({
        upstreamUrl,
        statusCode: upstreamResponse.status,
        headers: upstreamResponse.headers,
        bodyText: result.responseBodyText
      })
  })
  auditCapture.finalize({
    outcome: upstreamResponse.ok ? 'success' : 'upstream_failed',
    success: upstreamResponse.ok,
    statusCode: upstreamResponse.status,
    responseHeaders: responseHeadersToObject(res),
    responseBody: result.responseBodyText,
    responsePartType: upstreamResponse.ok ? 'gateway_response' : 'gateway_error',
    errorPhase: upstreamResponse.ok ? undefined : 'upstream_response',
    errorCode: typeof result.errorPayload.code === 'string' ? result.errorPayload.code : undefined,
    errorMessage: typeof result.errorPayload.message === 'string' ? result.errorPayload.message : undefined,
    accountId: account.id,
    firstTokenMs: result.firstTokenMs
  })
}
