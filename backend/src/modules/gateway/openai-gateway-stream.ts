import type { Response } from 'express'

import { getRequestLogger } from '../../shared/request-context.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import { emptyUsage, OpenAIStreamInspector, type ParsedUsage } from './openai-gateway-usage.js'
import {
  isUpstreamRequestAbortedError,
  readStreamChunkWithAbort,
  readStreamChunkWithTimeout
} from './openai-gateway-upstream.js'
import { gatewayStreamFailureCode, writeGatewayStreamFailureEvent } from './openai-gateway-responses.js'
import { closeAsyncIterator, endResponse, LimitedBufferCapture, writeResponseChunk } from './openai-gateway-body.js'
import { OpenAIStreamInterceptBuffer, type StreamInterceptDecision } from './openai-gateway-stream-intercept.js'
import { openAIStreamInterceptRules } from './openai-gateway-stream-rules.js'

export interface StreamPipeResult {
  completed: boolean
  message: string
  errorCode?: string
  firstTokenMs?: number
  usage: ParsedUsage
  responseBodyText?: string
  auditResponseBody?: Buffer
  auditUpstreamBody?: Buffer
  streamIntercept?: StreamInterceptDecision
}

export interface StreamPipeOptions {
  endpoint?: string
  onStreamIntercept?: (decision: StreamInterceptDecision) => void
}

const streamDiagnosticCaptureBytes = 256 * 1024
const streamAuditCaptureBytes = 1024 * 1024
const streamProgressLogIntervalMs = 60_000
const streamBackpressureLogIntervalMs = 30_000

export async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  settings: GatewaySettings,
  startedAt: number,
  handleStreamFailure: (reason: string, errorCode?: string) => void,
  signal?: AbortSignal,
  options: StreamPipeOptions = {}
): Promise<StreamPipeResult> {
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const inspector = new OpenAIStreamInspector()
  const interceptor = new OpenAIStreamInterceptBuffer(openAIStreamInterceptRules, {
    provider: 'openai',
    endpoint: options.endpoint ?? '',
    streamOnly: true
  })
  const responseCapture = new LimitedBufferCapture(streamAuditCaptureBytes)
  const upstreamCapture = new LimitedBufferCapture(streamAuditCaptureBytes)
  const diagnosticCapture = new LimitedBufferCapture(streamDiagnosticCaptureBytes)
  const streamLogger = getRequestLogger()
  let completed = false
  let parserSkipLogged = false
  let interceptParserSkipLogged = false
  let firstTokenMs: number | undefined
  let waitingForFirstChunk = true
  let lastUpstreamActivityAt = startedAt
  let lastSseEventActivityAt: number | undefined
  let lastSseEventCount = 0
  let upstreamChunkReceived = false
  let chunkIndex = 0
  let totalUpstreamBytes = 0
  let totalResponseBytes = 0
  let lastProgressLogAt = startedAt
  let lastBackpressureLogAt = 0
  let clientClosed = false
  const closeIterator = () => {
    clientClosed = true
    void closeAsyncIterator(iterator)
  }
  res.once('close', closeIterator)

  streamLogger.info({
    event: 'gateway_stream_pipe_started',
    streamCircuitBreakerEnabled: settings.streamCircuitBreakerEnabled,
    streamRequestTimeoutSeconds: settings.streamRequestTimeoutSeconds,
    streamIdleTimeoutSeconds: settings.streamIdleTimeoutSeconds,
    startedAt
  }, '网关开始转发上游流式响应')

  try {
    while (true) {
      if (clientClosed || res.destroyed) {
        throw new Error('客户端连接已断开')
      }
      const readStartedAt = Date.now()
      const result = await readNextStreamChunk(iterator, settings, startedAt, {
        waitingForFirstChunk,
        lastUpstreamActivityAt,
        lastSseEventActivityAt,
        upstreamChunkReceived
      }, signal)
      const readWaitMs = Date.now() - readStartedAt

      if (result.done) {
        completed = true
        break
      }

      const buffer = Buffer.from(result.value)
      chunkIndex += 1
      totalUpstreamBytes += buffer.length
      upstreamChunkReceived = true
      waitingForFirstChunk = false
      lastUpstreamActivityAt = Date.now()
      if (firstTokenMs === undefined) {
        firstTokenMs = lastUpstreamActivityAt - startedAt
      }
      upstreamCapture.push(buffer)
      const interceptResult = interceptor.pushChunk(buffer)
      if (interceptResult.parserSkipped && !interceptParserSkipLogged) {
        interceptParserSkipLogged = true
        streamLogger.warn({
          event: 'gateway_stream_intercept_parser_skipped'
        }, '网关流式特征拦截解析超过上限，已停止拦截并继续原样转发')
      }
      for (const decision of interceptResult.sideEffects) {
        options.onStreamIntercept?.(decision)
      }
      let latestInspection = inspector.snapshot()
      let chunkSseEventCount = 0
      let chunkWriteMs = 0
      for (const outbound of interceptResult.chunks) {
        responseCapture.push(outbound)
        diagnosticCapture.push(outbound)
        latestInspection = inspector.pushChunk(outbound)
        if (latestInspection.skipped && !parserSkipLogged) {
          parserSkipLogged = true
          streamLogger.warn({
            event: 'gateway_stream_inspector_skipped',
            reason: latestInspection.skipReason
          }, '网关流式解析超过上限，已停止解析并继续转发')
        }
        const outboundSseEventCount = latestInspection.eventCount - lastSseEventCount
        chunkSseEventCount += outboundSseEventCount
        inspector.drainEventSummaries()
        lastSseEventCount = latestInspection.eventCount
        if (latestInspection.skipped) {
          lastSseEventActivityAt = undefined
        } else if (lastSseEventActivityAt === undefined || outboundSseEventCount > 0) {
          lastSseEventActivityAt = lastUpstreamActivityAt
        }
        const writeStartedAt = Date.now()
        const writeResult = await writeResponseChunk(res, outbound)
        const writeMs = Date.now() - writeStartedAt
        chunkWriteMs += writeMs
        totalResponseBytes += outbound.length
        const writeNow = Date.now()
        if (writeResult.backpressure && writeNow - lastBackpressureLogAt >= streamBackpressureLogIntervalMs) {
          lastBackpressureLogAt = writeNow
          streamLogger.warn({
            event: 'gateway_stream_response_backpressure',
            elapsedMs: writeNow - startedAt,
            chunkIndex,
            totalUpstreamBytes,
            totalResponseBytes,
            downstreamDrainWaitMs: writeResult.drainWaitMs
          }, '网关流式响应写入下游出现背压')
        }
      }
      if (!latestInspection.skipped && lastSseEventActivityAt === undefined) {
        lastSseEventActivityAt = lastUpstreamActivityAt
      }
      const currentLastSseEventActivityAt = lastSseEventActivityAt ?? lastUpstreamActivityAt
      const timeSinceLastSseEventActivityMs = Date.now() - currentLastSseEventActivityAt
      const now = Date.now()
      if (now - lastProgressLogAt >= streamProgressLogIntervalMs) {
        lastProgressLogAt = now
        streamLogger.info({
          event: 'gateway_stream_progress',
          elapsedMs: now - startedAt,
          chunkIndex,
          totalUpstreamBytes,
          totalResponseBytes,
          firstTokenMs,
          lastReadWaitMs: readWaitMs,
          lastWriteMs: chunkWriteMs,
          lastChunkSseEventCount: chunkSseEventCount,
          sseEventCount: latestInspection.eventCount,
          lastSseEventType: latestInspection.lastEventType,
          timeSinceLastUpstreamActivityMs: now - lastUpstreamActivityAt,
          timeSinceLastSseEventActivityMs,
          recentSseEventTypes: latestInspection.recentEventTypes,
          terminalReceived: latestInspection.terminalReceived,
          failedReceived: latestInspection.failedReceived,
          outputReceived: latestInspection.outputReceived,
          outputEventCount: latestInspection.outputEventCount,
          parserSkipped: latestInspection.skipped,
          skipReason: latestInspection.skipReason
        }, '网关流式响应进度摘要')
      }
      if (interceptResult.intercepted) {
        options.onStreamIntercept?.(interceptResult.intercepted)
        await closeAsyncIterator(iterator)
        endResponse(res)
        const decision = interceptResult.intercepted
        const message = decision.upstreamErrorMessage ?? decision.rewriteMessage
        const errorCode = decision.upstreamErrorCode ?? decision.rewriteErrorCode
        streamLogger.warn({
          event: 'gateway_stream_intercepted',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          totalResponseBytes,
          ruleId: decision.ruleId,
          ruleName: decision.ruleName,
          action: decision.action,
          upstreamEventType: decision.upstreamEventType,
          upstreamErrorCode: decision.upstreamErrorCode,
          rewriteErrorCode: decision.rewriteErrorCode,
          accountPolicy: decision.accountPolicy,
          outputSeen: decision.outputSeen
        }, '网关已拦截上游流式错误并改写为客户端可重试事件')
        return streamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision)
      }
    }

    const eofInterceptResult = interceptor.flushPendingOnEof()
    if (eofInterceptResult.parserSkipped && !interceptParserSkipLogged) {
      interceptParserSkipLogged = true
      streamLogger.warn({
        event: 'gateway_stream_intercept_parser_skipped'
      }, '网关流式特征拦截解析超过上限，已停止拦截并继续原样转发')
    }
    for (const decision of eofInterceptResult.sideEffects) {
      options.onStreamIntercept?.(decision)
    }
    if (eofInterceptResult.chunks.length > 0 || eofInterceptResult.intercepted) {
      let latestInspection = inspector.snapshot()
      for (const outbound of eofInterceptResult.chunks) {
        responseCapture.push(outbound)
        diagnosticCapture.push(outbound)
        latestInspection = inspector.pushChunk(outbound)
        if (latestInspection.skipped && !parserSkipLogged) {
          parserSkipLogged = true
          streamLogger.warn({
            event: 'gateway_stream_inspector_skipped',
            reason: latestInspection.skipReason
          }, '网关流式解析超过上限，已停止解析并继续转发')
        }
        const outboundSseEventCount = latestInspection.eventCount - lastSseEventCount
        inspector.drainEventSummaries()
        lastSseEventCount = latestInspection.eventCount
        if (latestInspection.skipped) {
          lastSseEventActivityAt = undefined
        } else if (lastSseEventActivityAt === undefined || outboundSseEventCount > 0) {
          lastSseEventActivityAt = lastUpstreamActivityAt
        }
        const writeResult = await writeResponseChunk(res, outbound)
        totalResponseBytes += outbound.length
        const writeNow = Date.now()
        if (writeResult.backpressure && writeNow - lastBackpressureLogAt >= streamBackpressureLogIntervalMs) {
          lastBackpressureLogAt = writeNow
          streamLogger.warn({
            event: 'gateway_stream_response_backpressure',
            elapsedMs: writeNow - startedAt,
            chunkIndex,
            totalUpstreamBytes,
            totalResponseBytes,
            downstreamDrainWaitMs: writeResult.drainWaitMs
          }, '网关流式响应写入下游出现背压')
        }
      }
      if (eofInterceptResult.intercepted) {
        options.onStreamIntercept?.(eofInterceptResult.intercepted)
        endResponse(res)
        const decision = eofInterceptResult.intercepted
        const message = decision.upstreamErrorMessage ?? decision.rewriteMessage
        const errorCode = decision.upstreamErrorCode ?? decision.rewriteErrorCode
        streamLogger.warn({
          event: 'gateway_stream_intercepted',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          totalResponseBytes,
          ruleId: decision.ruleId,
          ruleName: decision.ruleName,
          action: decision.action,
          upstreamEventType: decision.upstreamEventType,
          upstreamErrorCode: decision.upstreamErrorCode,
          rewriteErrorCode: decision.rewriteErrorCode,
          accountPolicy: decision.accountPolicy,
          outputSeen: decision.outputSeen,
          eofPendingFlush: true
        }, '网关已在上游 EOF 时拦截未收尾流式错误并改写为客户端可重试事件')
        return streamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision)
      }
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    if (isUpstreamRequestAbortedError(error) || signal?.aborted) {
      const inspection = inspector.finish()
      streamLogger.warn({
        event: 'gateway_stream_aborted',
        elapsedMs: Date.now() - startedAt,
        chunkCount: chunkIndex,
        totalUpstreamBytes,
        totalResponseBytes,
        signalAborted: signal?.aborted,
        terminalReceived: inspection.terminalReceived,
        failedReceived: inspection.failedReceived,
        outputReceived: inspection.outputReceived,
        outputEventCount: inspection.outputEventCount,
        sseEventCount: inspection.eventCount,
        sseEventTypeCounts: inspection.eventTypeCounts,
        recentSseEventTypes: inspection.recentEventTypes,
        parserSkipped: inspection.skipped,
        skipReason: inspection.skipReason,
        errorMessage: error instanceof Error ? error.message : String(error)
      }, '网关流式转发因请求取消而结束')
      endResponse(res)
      throw error
    }
    const rawMessage = error instanceof Error ? error.message : '上游流式响应已中断'
    const inspection = inspector.finish()
    streamLogger.warn({
      event: 'gateway_stream_pipe_error',
      elapsedMs: Date.now() - startedAt,
      chunkCount: chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      rawMessage,
      terminalReceived: inspection.terminalReceived,
      failedReceived: inspection.failedReceived,
      outputReceived: inspection.outputReceived,
      outputEventCount: inspection.outputEventCount,
      sseEventCount: inspection.eventCount,
      sseEventTypeCounts: inspection.eventTypeCounts,
      recentSseEventTypes: inspection.recentEventTypes,
      parserSkipped: inspection.skipped,
      skipReason: inspection.skipReason
    }, '网关流式转发捕获异常')
    if (inspection.terminalReceived && !inspection.failedReceived) {
      endResponse(res)
      streamLogger.info({
        event: 'gateway_stream_error_ignored_after_terminal',
        elapsedMs: Date.now() - startedAt,
        rawMessage
      }, '网关已收到终止事件，忽略终止后的流式异常')
      return streamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
    }
    const message = inspection.errorMessage ?? rawMessage
    const errorCode = inspection.errorCode ?? gatewayStreamFailureCode(message)
    handleStreamFailure(message, errorCode)
    if (!inspection.failedReceived) {
      streamLogger.warn({
        event: 'gateway_stream_failure_event_writing',
        message
      }, '网关准备补发 response.failed')
      const failureEvent = await writeGatewayStreamFailureEventWithBackpressure(res, message)
      if (failureEvent) {
        responseCapture.push(failureEvent)
        diagnosticCapture.push(failureEvent)
        totalResponseBytes += failureEvent.length
        streamLogger.warn({
          event: 'gateway_stream_failure_event_written',
          message,
          failureEventBytes: failureEvent.length,
          totalResponseBytes
        }, '网关已补发 response.failed')
      } else {
        streamLogger.warn({
          event: 'gateway_stream_failure_event_skipped',
          message
        }, '网关补发 response.failed 失败或响应已结束')
      }
    }
    endResponse(res)
    return streamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  } finally {
    res.off('close', closeIterator)
  }

  const inspection = inspector.finish()
  if (inspection.skipped) {
    endResponse(res)
    const success = completed && !inspection.failedReceived
    const message = success ? '已完成' : (inspection.errorMessage ?? '上游流式响应失败')
    const errorCode = success ? undefined : inspection.errorCode ?? gatewayStreamFailureCode(message)
    if (!success) {
      handleStreamFailure(message, errorCode)
    }
    streamLogger.warn({
      event: 'gateway_stream_completed_with_parser_skipped',
      completed,
      success,
      elapsedMs: Date.now() - startedAt,
      chunkCount: chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      terminalReceived: inspection.terminalReceived,
      failedReceived: inspection.failedReceived,
      skipReason: inspection.skipReason
    }, '网关流式解析已跳过，按原始转发结果结束')
    return streamResult(success, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  }
  if (!inspection.terminalReceived) {
    const message = '上游流在 OpenAI 终止事件前结束'
    const errorCode = gatewayStreamFailureCode(message)
    handleStreamFailure(message, errorCode)
    streamLogger.warn({
      event: 'gateway_stream_missing_terminal',
      elapsedMs: Date.now() - startedAt,
      chunkCount: chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      sseEventCount: inspection.eventCount,
      sseEventTypeCounts: inspection.eventTypeCounts,
      recentSseEventTypes: inspection.recentEventTypes
    }, '上游 EOF 前未收到 OpenAI 终止事件')
    const failureEvent = await writeGatewayStreamFailureEventWithBackpressure(res, message)
    if (failureEvent) {
      responseCapture.push(failureEvent)
      diagnosticCapture.push(failureEvent)
      totalResponseBytes += failureEvent.length
      streamLogger.warn({
        event: 'gateway_stream_missing_terminal_failure_event_written',
        failureEventBytes: failureEvent.length,
        totalResponseBytes
      }, '网关已因缺少终止事件补发 response.failed')
    } else {
      streamLogger.warn({
        event: 'gateway_stream_missing_terminal_failure_event_skipped'
      }, '网关因缺少终止事件补发 response.failed 失败或响应已结束')
    }
    endResponse(res)
    return streamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  }

  endResponse(res)

  if (!completed || inspection.failedReceived) {
    const message = inspection.errorMessage ?? '上游流式响应失败'
    const errorCode = inspection.errorCode ?? gatewayStreamFailureCode(message)
    handleStreamFailure(message, errorCode)
    streamLogger.warn({
      event: 'gateway_stream_finished_failed',
      completed,
      elapsedMs: Date.now() - startedAt,
      chunkCount: chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      message,
      terminalReceived: inspection.terminalReceived,
      failedReceived: inspection.failedReceived,
      sseEventCount: inspection.eventCount,
      sseEventTypeCounts: inspection.eventTypeCounts,
      recentSseEventTypes: inspection.recentEventTypes
    }, '网关流式响应以失败结束')
    return streamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  }

  streamLogger.info({
    event: 'gateway_stream_finished_success',
    elapsedMs: Date.now() - startedAt,
    chunkCount: chunkIndex,
    totalUpstreamBytes,
    totalResponseBytes,
    firstTokenMs,
    sseEventCount: inspection.eventCount,
    sseEventTypeCounts: inspection.eventTypeCounts,
    recentSseEventTypes: inspection.recentEventTypes,
    outputReceived: inspection.outputReceived,
    outputEventCount: inspection.outputEventCount
  }, '网关流式响应已成功结束')
  return streamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
}

function readNextStreamChunk(
  iterator: AsyncIterator<Uint8Array>,
  settings: GatewaySettings,
  startedAt: number,
  status: {
    waitingForFirstChunk: boolean
    lastUpstreamActivityAt: number
    lastSseEventActivityAt?: number
    upstreamChunkReceived: boolean
  },
  signal?: AbortSignal
): Promise<IteratorResult<Uint8Array>> {
  if (!settings.streamCircuitBreakerEnabled) {
    return readStreamChunkWithAbort(iterator, signal)
  }
  const readPlan = buildStreamReadPlan(settings, startedAt, status)
  if (readPlan.timeoutMs === undefined) {
    return readStreamChunkWithAbort(iterator, signal)
  }
  // If downstream writes delayed the next read, upstream bytes may already be buffered locally.
  // Give iterator.next() one tick before declaring the stream idle.
  return readStreamChunkWithTimeout(
    iterator,
    Math.max(0.001, readPlan.timeoutMs / 1000),
    () => new Error(readPlan.timeoutMessage),
    signal
  )
}

interface StreamReadPlan {
  phase: 'first_chunk' | 'active_stream' | 'sse_event' | 'no_circuit_breaker'
  timeoutMs?: number
  rawTimeoutMs?: number
  sseEventTimeoutMs?: number
  timeoutKind?: 'first_chunk' | 'upstream_activity' | 'sse_event'
  timeoutMessage: string
  deadlineExceeded: boolean
}

function buildStreamReadPlan(
  settings: GatewaySettings,
  startedAt: number,
  status: {
    waitingForFirstChunk: boolean
    lastUpstreamActivityAt: number
    lastSseEventActivityAt?: number
    upstreamChunkReceived: boolean
  }
): StreamReadPlan {
  if (!settings.streamCircuitBreakerEnabled) {
    return {
      phase: 'no_circuit_breaker',
      timeoutMessage: '',
      deadlineExceeded: false
    }
  }

  if (!status.waitingForFirstChunk || status.upstreamChunkReceived) {
    const streamIdleTimeoutSeconds = Math.max(1, settings.streamIdleTimeoutSeconds)
    const now = Date.now()
    const rawTimeoutMs = streamIdleTimeoutSeconds * 1000 - (now - status.lastUpstreamActivityAt)
    const sseEventTimeoutMs = status.lastSseEventActivityAt === undefined
      ? undefined
      : streamIdleTimeoutSeconds * 1000 - (now - status.lastSseEventActivityAt)
    if (sseEventTimeoutMs !== undefined && sseEventTimeoutMs < rawTimeoutMs) {
      return {
        phase: 'sse_event',
        timeoutMs: sseEventTimeoutMs,
        rawTimeoutMs,
        sseEventTimeoutMs,
        timeoutKind: 'sse_event',
        timeoutMessage: streamSseEventTimeoutMessage(streamIdleTimeoutSeconds),
        deadlineExceeded: sseEventTimeoutMs <= 0
      }
    }
    return {
      phase: 'active_stream',
      timeoutMs: rawTimeoutMs,
      rawTimeoutMs,
      sseEventTimeoutMs,
      timeoutKind: 'upstream_activity',
      timeoutMessage: streamIdleTimeoutMessage(streamIdleTimeoutSeconds),
      deadlineExceeded: rawTimeoutMs <= 0
    }
  }

  const firstChunkTimeoutSeconds = Math.max(1, settings.streamRequestTimeoutSeconds)
  const timeoutMs = firstChunkTimeoutSeconds * 1000 - (Date.now() - startedAt)
  return {
    phase: 'first_chunk',
    timeoutMs,
    timeoutKind: 'first_chunk',
    timeoutMessage: firstChunkTimeoutMessage(firstChunkTimeoutSeconds),
    deadlineExceeded: timeoutMs <= 0
  }
}

function firstChunkTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式请求 ${timeoutSeconds}s 内未返回首段数据`
}

function streamIdleTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式响应 ${timeoutSeconds}s 内未返回任何新数据`
}

function streamSseEventTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式响应 ${timeoutSeconds}s 内未形成完整 SSE 事件`
}

function streamResult(
  completed: boolean,
  message: string,
  errorCode: string | undefined,
  firstTokenMs: number | undefined,
  usage: ParsedUsage,
  responseCapture: LimitedBufferCapture,
  upstreamCapture: LimitedBufferCapture,
  diagnosticCapture: LimitedBufferCapture,
  streamIntercept?: StreamInterceptDecision
): StreamPipeResult {
  return {
    completed,
    message,
    errorCode,
    firstTokenMs,
    usage,
    responseBodyText: diagnosticCapture.toDiagnosticText(),
    auditResponseBody: responseCapture.completeBuffer(),
    auditUpstreamBody: upstreamCapture.completeBuffer(),
    streamIntercept
  }
}

async function writeGatewayStreamFailureEventWithBackpressure(res: Response, message: string): Promise<Buffer | undefined> {
  const buffer = writeGatewayStreamFailureEvent(res, message)
  if (!buffer) {
    return undefined
  }
  try {
    await writeResponseChunk(res, buffer)
    return buffer
  } catch {
    return undefined
  }
}
