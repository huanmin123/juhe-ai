import type { Request, Response } from 'express'

import { logger } from '../../shared/logger.js'
import { parseErrorPayload, type GatewaySettings } from './account-error-policy.service.js'
import { responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  clearAccountStreamFailureStateWithCacheInvalidation,
  handleStreamFailure
} from './openai-gateway-account-effects.js'
import { rememberCodexTurnStreamFailure } from './openai-gateway-codex-turn-retry.service.js'
import type { OpenAIGatewayClientStrategyContext } from './openai-gateway-client-strategy.js'
import {
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
import { pipeUpstreamStream } from './openai-gateway-stream.js'
import { isCodexRetryableAfterOutputStreamFailureCode } from './openai-gateway-stream-intercept.js'
import {
  copyResponseHeaders,
  isEffectiveOpenAIStreamRequest,
  isUpstreamRequestAbortedError,
  UpstreamRequestAbortedError,
  type GatewayUpstreamResponse
} from './openai-gateway-upstream.js'
import {
  buildUsageResponseSnapshot,
  emptyUsage,
  parseOpenAIUsageFromJsonBuffer,
  parseOpenAIUsageFromJsonTextFragment,
  requestModel,
  type ParsedUsage
} from './openai-gateway-usage.js'
import { applyOpenAIStreamUsageFallback } from './openai-gateway-stream-inspection.js'
import {
  recordClientAbortedUpstreamAttempt,
  recordCompletedUpstreamAttempt,
  type GatewayUsageContext
} from './openai-gateway-usage-records.js'

export type UpstreamResponseHandlingResult =
  | { alreadyFinalized: true }
  | {
    alreadyFinalized: false
    usage: ParsedUsage
    firstTokenMs?: number
    responseBodyText?: string
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
  markFirstOutput?: () => void
}

interface FinalizeHandledUpstreamResponseInput extends HandleUpstreamResponseInput {
  result: Exclude<UpstreamResponseHandlingResult, { alreadyFinalized: true }>
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
    markFirstOutput
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
      (message, errorCode, context) => handleStreamFailure(account, message, settings, errorCode, context),
      signal,
      {
        clientRetryEnabled: clientStrategy?.allowCodexStreamClientRetry === true,
        onFirstOutput: markFirstOutput
      }
    )
  } catch (error) {
    if (isUpstreamRequestAbortedError(error) || signal.aborted) {
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
          errorMessage: '请求已取消'
        })
      })
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        success: false,
        errorPhase: 'client',
        errorMessage: '请求已取消'
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
  if (streamResult.streamIntercept) {
    auditCapture.addGatewayMetadata({
      label: 'stream_intercept',
      metadata: streamInterceptAuditMetadata(streamResult.streamIntercept)
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
      requestSnapshot: usageContext.requestSnapshot,
      responseSnapshot: buildUsageResponseSnapshot({
        upstreamUrl,
        statusCode: upstreamResponse.status,
        headers: upstreamResponse.headers,
        bodyText: streamResult.responseBodyText,
        errorMessage: streamResult.message
      }),
      errorMessage: streamResult.message
    })
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
    errorPayload: {}
  }
}

type GatewayStreamPipeResult = Awaited<ReturnType<typeof pipeUpstreamStream>>

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

export async function handleNonStreamUpstreamResponse(input: HandleUpstreamResponseInput): Promise<UpstreamResponseHandlingResult> {
  const {
    req,
    res,
    account,
    upstreamResponse,
    upstreamUrl,
    auditAttemptId,
    auditCapture,
    usageContext,
    startedAt,
    signal,
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
      markFirstOutput?.()
      res.end()
    } else if (upstreamResponse.ok) {
      const pipeResult = await pipeNonStreamUpstreamResponse(upstreamResponse.body, res, {
        startedAt,
        signal,
        onFirstByte: markFirstOutput
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
          errorMessage: '请求已取消'
        })
      })
      auditCapture.completeAttempt(auditAttemptId, {
        statusCode: upstreamResponse.status,
        responseHeaders: upstreamResponse.headers,
        success: false,
        errorPhase: 'client',
        errorMessage: '请求已取消'
      })
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
    result
  } = input
  if (upstreamResponse.ok) {
    applyAccountErrorHandlingWithCacheInvalidation(account, {
      success: true,
      settings
    })
    if (account.streamFailureCount > 0 || account.streamFailureWindowStartedAt || account.lastErrorMessage) {
      clearAccountStreamFailureStateWithCacheInvalidation(account.id)
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
    requestSnapshot: upstreamResponse.ok ? undefined : usageContext.requestSnapshot,
    responseSnapshot: upstreamResponse.ok
      ? undefined
      : buildUsageResponseSnapshot({
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
