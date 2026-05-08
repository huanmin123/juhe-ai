import type { Response } from 'express'

import { logger } from '../../shared/logger.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import { emptyUsage, OpenAIStreamInspector, type ParsedUsage } from './openai-gateway-usage.js'
import { isUpstreamRequestAbortedError, readStreamChunkWithAbort, readStreamChunkWithIdleTimeout } from './openai-gateway-upstream.js'
import { writeGatewayStreamFailureEvent } from './openai-gateway-responses.js'
import { closeAsyncIterator, endResponse, LimitedBufferCapture, writeResponseChunk } from './openai-gateway-body.js'

export interface StreamPipeResult {
  completed: boolean
  message: string
  firstTokenMs?: number
  usage: ParsedUsage
  responseBodyText?: string
  auditResponseBody?: Buffer
  auditUpstreamBody?: Buffer
}

const streamDiagnosticCaptureBytes = 256 * 1024
const streamAuditCaptureBytes = 1024 * 1024

export async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  settings: GatewaySettings,
  startedAt: number,
  handleStreamFailure: (reason: string) => void,
  signal?: AbortSignal
): Promise<StreamPipeResult> {
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const inspector = new OpenAIStreamInspector()
  const responseCapture = new LimitedBufferCapture(streamAuditCaptureBytes)
  const upstreamCapture = new LimitedBufferCapture(streamAuditCaptureBytes)
  const diagnosticCapture = new LimitedBufferCapture(streamDiagnosticCaptureBytes)
  let completed = false
  let parserSkipLogged = false
  let firstTokenMs: number | undefined
  let clientClosed = false
  const closeIterator = () => {
    clientClosed = true
    void closeAsyncIterator(iterator)
  }
  res.once('close', closeIterator)

  try {
    while (true) {
      if (clientClosed || res.destroyed) {
        throw new Error('客户端连接已断开')
      }
      const result = settings.streamCircuitBreakerEnabled
        ? await readStreamChunkWithIdleTimeout(iterator, settings.streamIdleTimeoutSeconds, signal)
        : await readStreamChunkWithAbort(iterator, signal)

      if (result.done) {
        completed = true
        break
      }

      const buffer = Buffer.from(result.value)
      responseCapture.push(buffer)
      upstreamCapture.push(buffer)
      diagnosticCapture.push(buffer)
      const inspection = inspector.pushChunk(buffer)
      if (inspection.skipped && !parserSkipLogged) {
        parserSkipLogged = true
        logger.warn({
          event: 'gateway_stream_inspector_skipped',
          reason: inspection.skipReason
        }, '网关流式解析超过上限，已停止解析并继续转发')
      }
      if (firstTokenMs === undefined && inspection.outputReceived) {
        firstTokenMs = Date.now() - startedAt
      }
      await writeResponseChunk(res, buffer)
    }
  } catch (error) {
    if (isUpstreamRequestAbortedError(error) || signal?.aborted) {
      await closeAsyncIterator(iterator)
      endResponse(res)
      throw error
    }
    const rawMessage = error instanceof Error ? error.message : '上游流式响应已中断'
    const inspection = inspector.finish()
    if (inspection.terminalReceived && !inspection.failedReceived) {
      endResponse(res)
      return streamResult(true, '已完成', firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
    }
    const message = inspection.errorMessage ?? rawMessage
    handleStreamFailure(message)
    if (!inspection.failedReceived) {
      const failureEvent = await writeGatewayStreamFailureEventWithBackpressure(res, message)
      if (failureEvent) {
        responseCapture.push(failureEvent)
        diagnosticCapture.push(failureEvent)
      }
    }
    endResponse(res)
    return streamResult(false, message, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  } finally {
    res.off('close', closeIterator)
  }

  const inspection = inspector.finish()
  if (inspection.skipped) {
    endResponse(res)
    return streamResult(completed, completed ? '已完成' : '上游流式响应已中断', firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  }
  if (!inspection.terminalReceived) {
    const message = '上游流在 OpenAI 终止事件前结束'
    handleStreamFailure(message)
    const failureEvent = await writeGatewayStreamFailureEventWithBackpressure(res, message)
    if (failureEvent) {
      responseCapture.push(failureEvent)
      diagnosticCapture.push(failureEvent)
    }
    endResponse(res)
    return streamResult(false, message, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  }

  endResponse(res)

  if (!completed || inspection.failedReceived) {
    const message = inspection.errorMessage ?? '上游流式响应失败'
    handleStreamFailure(message)
    return streamResult(false, message, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
  }

  return streamResult(true, '已完成', firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture)
}

function streamResult(
  completed: boolean,
  message: string,
  firstTokenMs: number | undefined,
  usage: ParsedUsage,
  responseCapture: LimitedBufferCapture,
  upstreamCapture: LimitedBufferCapture,
  diagnosticCapture: LimitedBufferCapture
): StreamPipeResult {
  return {
    completed,
    message,
    firstTokenMs,
    usage,
    responseBodyText: diagnosticCapture.toDiagnosticText(),
    auditResponseBody: responseCapture.completeBuffer(),
    auditUpstreamBody: upstreamCapture.completeBuffer()
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
