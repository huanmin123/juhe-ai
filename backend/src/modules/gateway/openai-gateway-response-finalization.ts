import type { Request, Response } from 'express'

import { logger } from '../../shared/logger.js'
import { getRequestLogger } from '../../shared/request-context.js'
import { parseErrorPayload, type GatewaySettings } from './account-error-policy.service.js'
import { responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  clearAccountStreamFailureStateWithCacheInvalidation,
  handleStreamFailure
} from './openai-gateway-account-effects.js'
import { rememberCodexTurnStreamFailure } from './openai-gateway-codex-turn-retry.service.js'
import { downstreamConnectionClosedMessage } from './openai-gateway-client-abort.js'
import type { OpenAIGatewayClientStrategyContext } from './openai-gateway-client-strategy.js'
import {
  confirmClientIpAccountAvoidanceAfterSuccess,
  type ClientIpAccountAvoidanceTracker
} from './openai-gateway-client-ip-account-avoidance.service.js'
import { suppressGatewayAccountLocallyForSeconds } from './gateway-account-side-effects.service.js'
import { recordClientIpErrorCircuitSuccess } from './openai-gateway-client-ip-error-circuit.service.js'
import {
  NonStreamUpstreamBodyPipeError,
  pipeNonStreamUpstreamResponse,
  readUpstreamBodyLimited
} from './openai-gateway-body.js'
import { streamInterceptAuditMetadata } from './openai-gateway-audit-metadata.js'
import {
  forgetOpenAIAccountForSession
} from './openai-gateway-session-affinity.service.js'
import {
  gatewayStreamClientRetryErrorCode,
  gatewayErrorPayload,
  sendGatewayErrorResponse
} from './openai-gateway-responses.js'
import { type UpstreamAccount } from './openai-gateway-route-helpers.js'
import {
  pipeUpstreamStream,
  type StreamBodyOmissionSummary
} from './openai-gateway-stream.js'
import { isCodexRetryableAfterOutputStreamFailureCode, type StreamInterceptDecision } from './openai-gateway-stream-intercept.js'
import {
  copyResponseHeaders,
  isEffectiveOpenAIStreamRequest,
  isUpstreamRequestAbortedError,
  upstreamRequestTimeoutMs,
  UpstreamRequestAbortedError,
  type GatewayUpstreamResponse
} from './openai-gateway-upstream.js'
import {
  buildUsageResponseSnapshot,
  emptyUsage,
  parseOpenAIUsageFromJsonBuffer,
  parseOpenAIUsageFromJsonTextFragment,
  requestModel,
  type ParsedUsage,
  type UsageRequestSnapshot
} from './openai-gateway-usage.js'
import { applyOpenAIStreamUsageFallback } from './openai-gateway-stream-inspection.js'
import {
  recordGatewayUpstreamBucketSuccess,
  suppressGatewayUpstreamBucketLocallyForSeconds
} from './openai-gateway-proxy-health.service.js'
import { resolveRuntimeStreamInterceptPolicies } from './openai-gateway-stream-policy.js'
import type { StreamInterceptPolicySummary } from '../../storage/stream-intercept-policy.repository.js'
import {
  recordClientAbortedUpstreamAttempt,
  recordCompletedUpstreamAttempt,
  type GatewayUsageContext
} from './openai-gateway-usage-records.js'
import { sanitizeDiagnosticPayload } from './payload-sanitizer.js'

export type UpstreamResponseHandlingResult =
  | { alreadyFinalized: true }
  | {
    alreadyFinalized: false
    retryUpstream: true
    streamIntercept: StreamInterceptDecision
    message: string
    errorCode?: string
  }
  | {
    alreadyFinalized: false
    retryUpstream?: false
    usage: ParsedUsage
    firstTokenMs?: number
    responseBodyText?: string
    bodyOmission?: StreamBodyOmissionSummary
    errorPayload: Record<string, unknown>
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
  usageContext: GatewayUsageContext
  startedAt: number
  signal: AbortSignal
  sessionAffinityKey?: string
  clientStrategy?: OpenAIGatewayClientStrategyContext
  streamInterceptPolicies?: StreamInterceptPolicySummary[]
  markFirstOutput?: () => void
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
  accountStateMutationEnabled?: boolean
}

interface FinalizeHandledUpstreamResponseInput extends HandleUpstreamResponseInput {
  result: Exclude<UpstreamResponseHandlingResult, { alreadyFinalized: true } | { retryUpstream: true }>
}

export function prepareUpstreamResponseForDownstream(
  res: Response,
  upstreamResponse: GatewayUpstreamResponse,
  shouldHandleAsStream: boolean
): void {
  res.status(upstreamResponse.status)
  copyResponseHeaders(upstreamResponse, res)
  if (shouldHandleAsStream && !res.hasHeader('content-type')) {
    res.setHeader('content-type', 'text/event-stream; charset=utf-8')
  }
  if (shouldHandleAsStream) {
    setGatewayStreamResponseHeaders(res)
    flushResponseHeadersIfSupported(res)
  }
}

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
    accountStateMutationEnabled
  } = input

  if (!upstreamResponse.body) {
    const responsePayload = gatewayErrorPayload('上游响应体为空', 'upstream_response_error')
    sendGatewayErrorResponse(res, upstreamResponse.status, responsePayload)
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
      responseBody: JSON.stringify(responsePayload),
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
        onFirstOutput: markFirstOutput,
        captureSuccessPayloads: auditCapture.shouldCaptureSuccessPayloads(),
        streamInterceptPolicies: resolveRuntimeStreamInterceptPolicies({
          account,
          managementPolicies: input.streamInterceptPolicies
        }),
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

  const streamUsageFallback = applyOpenAIStreamUsageFallback(req, streamResult.usage, {
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
  applyStreamInterceptObservationHandling(streamResult, account, settings, auditCapture, accountStateMutationEnabled !== false)
  if (streamResult.streamIntercept) {
    applyStreamInterceptPolicyRuntimeSideEffects(streamResult.streamIntercept, account, settings, accountStateMutationEnabled !== false)
    auditCapture.addGatewayMetadata({
      label: 'stream_intercept',
      metadata: streamInterceptAuditMetadata(streamResult.streamIntercept)
    })
  }
  if (streamResult.bodyOmission) {
    auditCapture.omitPayloadBodies({
      label: 'stream_body_omission',
      metadata: { ...streamResult.bodyOmission }
    })
  }
  if (shouldRememberCodexTurnStreamFailure(streamResult, clientStrategy)) {
    const codexTurnFailure = rememberCodexTurnStreamFailure(clientStrategy, account.id, {
      errorCode: streamResult.streamIntercept?.upstreamErrorCode ?? streamResult.errorCode,
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
      errorCode: streamResult.streamIntercept?.upstreamErrorCode ?? streamResult.errorCode,
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
    if (shouldRetryStreamInterceptOnServer(streamResult, res)) {
      auditCapture.addGatewayMetadata({
        label: 'stream_intercept_server_retry',
        metadata: streamInterceptAuditMetadata(streamResult.streamIntercept)
      })
      return {
        alreadyFinalized: false,
        retryUpstream: true,
        streamIntercept: streamResult.streamIntercept,
        message: streamResult.message,
        errorCode: streamResult.streamIntercept.upstreamErrorCode ?? streamResult.errorCode
      }
    }
    auditCapture.finalize({
      outcome: 'stream_failed',
      success: false,
      statusCode: upstreamResponse.status,
      responseHeaders: responseHeadersToObject(res),
      responseBody: streamResult.auditResponseBody,
      responsePartType: 'gateway_response',
      errorPhase: 'stream',
      errorCode: streamResult.streamIntercept?.upstreamErrorCode ?? streamResult.errorCode,
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

function applyStreamInterceptPolicyRuntimeSideEffects(
  decision: NonNullable<GatewayStreamPipeResult['streamIntercept']>,
  account: UpstreamAccount,
  settings: GatewaySettings,
  accountStateMutationEnabled: boolean
): void {
  if (!accountStateMutationEnabled || decision.reason !== 'configured_stream_policy' || decision.action === 'dry_run') {
    return
  }
  const reason = `流式拦截策略命中：${decision.policyName ?? decision.policyId ?? decision.matchedValue ?? '未命名策略'}`
  const ttlSeconds = decision.avoidanceTtlSeconds ?? settings.defaultTemporaryUnschedulableMinutes * 60
  if (decision.accountState === 'runtime_avoidance' || decision.accountSwitch === 'avoid_account_ttl') {
    suppressGatewayAccountLocallyForSeconds(account, ttlSeconds, reason)
  }
  if (decision.accountSwitch === 'avoid_upstream_bucket_ttl') {
    suppressGatewayUpstreamBucketLocallyForSeconds(account, ttlSeconds, reason)
  }
}

type GatewayStreamPipeResult = Awaited<ReturnType<typeof pipeUpstreamStream>>

function applyStreamInterceptObservationHandling(
  streamResult: GatewayStreamPipeResult,
  account: UpstreamAccount,
  settings: GatewaySettings,
  auditCapture: AuditCaptureContext,
  accountStateMutationEnabled: boolean
): void {
  const observations = streamResult.streamInterceptObservations ?? []
  if (observations.length === 0) {
    return
  }
  for (const observation of observations) {
    applyStreamInterceptPolicyRuntimeSideEffects(observation, account, settings, accountStateMutationEnabled)
  }
  auditCapture.addGatewayMetadata({
    label: 'stream_intercept_observations',
    metadata: {
      count: observations.length,
      omittedCount: streamResult.streamInterceptObservationOmittedCount,
      observations: observations.map(streamInterceptAuditMetadata)
    }
  })
}

function shouldRetryStreamInterceptOnServer(
  streamResult: GatewayStreamPipeResult,
  res: Response
): streamResult is GatewayStreamPipeResult & { streamIntercept: StreamInterceptDecision } {
  const decision = streamResult.streamIntercept
  return decision?.reason === 'configured_stream_policy'
    && decision.retryEnabled === true
    && decision.policySource !== 'system_default'
    && !res.headersSent
    && !res.writableEnded
    && !res.destroyed
}

function shouldRememberCodexTurnStreamFailure(
  streamResult: GatewayStreamPipeResult,
  clientStrategy: OpenAIGatewayClientStrategyContext | undefined
): clientStrategy is OpenAIGatewayClientStrategyContext {
  const retryableAfterOutput = isCodexRetryableAfterOutputStreamFailureCode(streamResult.streamIntercept?.upstreamErrorCode ?? streamResult.errorCode)
  return !streamResult.completed
    && (!streamResult.outputReceived || retryableAfterOutput)
    && clientStrategy?.allowCodexTurnAccountAvoidance === true
    && (
      streamResult.errorCode === gatewayStreamClientRetryErrorCode
      || streamResult.streamIntercept?.rewriteErrorCode === gatewayStreamClientRetryErrorCode
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
      const errorMessage = sanitizeDiagnosticPayload(error instanceof Error ? error.message : '上游非流式响应正文中断')
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
        const ttlSeconds = Math.max(1, settings.defaultTemporaryUnschedulableMinutes * 60)
        suppressGatewayAccountLocallyForSeconds(account, ttlSeconds, `上游非流式响应正文中断：${errorMessage}`)
        applyAccountErrorHandlingWithCacheInvalidation(account, {
          success: false,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          bodyText: responseBodyText || errorMessage,
          errorMessage,
          settings,
          trafficSource: usageContext.trafficSource
        })
        auditCapture.addGatewayMetadata({
          label: 'non_stream_body_interrupted_runtime_avoidance',
          metadata: {
            accountId: account.id,
            ttlSeconds,
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
    usage = parseOpenAIUsageFromJsonBuffer(responseBody)
  } else if (upstreamResponse.ok) {
    usage = parseOpenAIUsageFromJsonTextFragment(responseUsageText)
  }
  if (!upstreamResponse.ok) {
    errorPayload = parseErrorPayload(responseBodyText ?? '', upstreamResponse.headers)
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

function flushResponseHeadersIfSupported(res: Response): void {
  const flushHeaders = (res as { flushHeaders?: unknown }).flushHeaders
  if (typeof flushHeaders === 'function') {
    flushHeaders.call(res)
  }
}

function setGatewayStreamResponseHeaders(res: Response): void {
  if (!res.hasHeader('cache-control')) {
    res.setHeader('cache-control', 'no-cache, no-transform')
  }
  res.setHeader('x-accel-buffering', 'no')
}
