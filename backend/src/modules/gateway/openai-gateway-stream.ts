import type { Response } from 'express'

import { getRequestLogger } from '../../shared/request-context.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import { downstreamConnectionClosedMessage } from './openai-gateway-client-abort.js'
import { emptyUsage, type ParsedUsage } from './openai-gateway-usage.js'
import {
  OpenAIStreamInspector,
  type OpenAIStreamInspection
} from './openai-gateway-stream-inspection.js'
import {
  isUpstreamRequestAbortedError,
  readStreamChunkWithAbort,
  readStreamChunkWithTimeout
} from './openai-gateway-upstream.js'
import {
  gatewayStreamClientRetryErrorCode,
  gatewayStreamFailureCode,
  writeGatewayStreamFailureEvent
} from './openai-gateway-responses.js'
import {
  closeAsyncIterator,
  endResponse,
  LimitedBufferCapture,
  responseBackpressureWarnThresholdMs,
  writeResponseChunk
} from './openai-gateway-body.js'
import {
  OpenAIResponseInspectionBuffer,
  responseInspectionFailurePayloadForDecision,
  type ResponseInspectionDecision,
  type RuntimeResponseInspectionPolicy,
  type ResponseInspectionSseResult
} from './openai-gateway-response-inspection.js'
import type { OpenAIResponseEndpointFamily } from './openai-gateway-response-semantics.js'

export interface StreamPipeResult {
  completed: boolean
  message: string
  errorCode?: string
  firstTokenMs?: number
  usage: ParsedUsage
  outputReceived: boolean
  imageOutputReceived: boolean
  estimatedOutputTokens?: number
  responseBodyText?: string
  auditResponseBody?: Buffer
  auditUpstreamBody?: Buffer
  downstreamBytesWritten: number
  uncommittedResponseBody?: Buffer
  responseInspection?: ResponseInspectionDecision
  responseInspectionObservations?: ResponseInspectionDecision[]
  responseInspectionObservationOmittedCount?: number
  bodyOmission?: StreamBodyOmissionSummary
}

export interface StreamBodyOmissionSummary {
  omitted: true
  reason: 'image_stream_payload'
  message: string
  totalUpstreamBytes: number
  totalResponseBytes: number
  sseEventCount: number
  lastSseEventType?: string
  recentSseEventTypes: string[]
  imageOutputReceived: boolean
  terminalReceived: boolean
  failedReceived: boolean
}

export interface StreamFailureContext {
  downstreamBytesWritten: number
  outputReceived: boolean
}

export interface StreamPipeOptions {
  clientRetryEnabled?: boolean
  onFirstOutput?: () => void
  captureSuccessPayloads?: boolean
  retryBeforeDownstreamWriteUntilOutput?: boolean
  responseInspectionPolicies?: RuntimeResponseInspectionPolicy[]
  endpointFamily?: OpenAIResponseEndpointFamily
  prepareDownstream?: () => void
  transformUpstreamChunk?: (chunk: Buffer) => Buffer[]
  flushTransformedUpstreamChunks?: () => Buffer[]
}

const streamDiagnosticCaptureBytes = 256 * 1024
const streamAuditCaptureBytes = 1024 * 1024
const streamTerminalKeepAliveDrainMs = 50
const streamProgressLogIntervalMs = 60_000
const streamBackpressureLogIntervalMs = 30_000
const maxResponseInspectionObservationCount = 20
const streamPreCommitBufferMaxBytes = 256 * 1024

export async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  settings: GatewaySettings,
  startedAt: number,
  handleStreamFailure: (reason: string, errorCode: string | undefined, context: StreamFailureContext) => void,
  signal?: AbortSignal,
  options: StreamPipeOptions = {}
): Promise<StreamPipeResult> {
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const inspector = new OpenAIStreamInspector()
  const interceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: options.clientRetryEnabled === true,
    policies: options.responseInspectionPolicies,
    endpointFamily: options.endpointFamily ?? 'unknown'
  })
  const captureSuccessPayloads = options.captureSuccessPayloads !== false
  const responseCapture = new LimitedBufferCapture(captureSuccessPayloads ? streamAuditCaptureBytes : -1)
  const upstreamCapture = new LimitedBufferCapture(captureSuccessPayloads ? streamAuditCaptureBytes : streamDiagnosticCaptureBytes)
  const diagnosticCapture = new LimitedBufferCapture(streamDiagnosticCaptureBytes)
  const streamLogger = getRequestLogger()
  let completed = false
  let parserSkipLogged = false
  let responseInspectionParserSkipLogged = false
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
  let terminalEventWritten = false
  let bodyCaptureOmitted = false
  let downstreamPrepared = false
  let preCommitBuffering = options.retryBeforeDownstreamWriteUntilOutput === true
  let preCommitBufferedBytes = 0
  const preCommitChunks: Buffer[] = []
  const responseInspectionObservations: ResponseInspectionDecision[] = []
  let responseInspectionObservationOmittedCount = 0
  const prepareDownstreamForWrite = () => {
    if (downstreamPrepared) return
    downstreamPrepared = true
    options.prepareDownstream?.()
  }
  const captureDownstreamChunk = (chunk: Buffer) => {
    if (bodyCaptureOmitted) return
    responseCapture.push(chunk)
    diagnosticCapture.push(chunk)
  }
  const writeDownstreamChunk = async (chunk: Buffer) => {
    captureDownstreamChunk(chunk)
    prepareDownstreamForWrite()
    const writeResult = await writeResponseChunk(res, chunk)
    interceptor.markDownstreamWrite()
    totalResponseBytes += chunk.length
    return writeResult
  }
  const canKeepPreCommitBuffered = (inspection: OpenAIStreamInspection, chunk: Buffer) => {
    return preCommitBuffering
      && totalResponseBytes === 0
      && !inspection.outputReceived
      && !inspection.terminalReceived
      && !inspection.failedReceived
      && !inspection.skipped
      && preCommitBufferedBytes + chunk.length <= streamPreCommitBufferMaxBytes
      && !res.headersSent
      && !res.writableEnded
      && !res.destroyed
  }
  const flushPreCommitChunks = async () => {
    if (preCommitChunks.length === 0) {
      preCommitBuffering = false
      return
    }
    preCommitBuffering = false
    for (const buffered of preCommitChunks.splice(0)) {
      await writeDownstreamChunk(buffered)
    }
  }
  const shouldFailBeforeDownstreamCommit = () => {
    return preCommitBuffering
      && totalResponseBytes === 0
      && !res.headersSent
      && !res.writableEnded
      && !res.destroyed
  }
  const recordResponseInspectionObservations = (observations: ResponseInspectionDecision[] | undefined) => {
    if (!observations?.length) return
    for (const observation of observations) {
      if (responseInspectionObservations.length < maxResponseInspectionObservationCount) {
        responseInspectionObservations.push(observation)
      } else {
        responseInspectionObservationOmittedCount += 1
      }
    }
  }
  const closeIterator = () => {
    clientClosed = true
    void closeAsyncIterator(iterator)
  }
  const omitBodyCaptureIfImageStream = (
    inspection: ReturnType<OpenAIStreamInspector['snapshot']>,
    input: { eofPendingFlush?: boolean } = {}
  ) => {
    if (!inspection.imageOutputReceived || bodyCaptureOmitted) {
      return
    }
    bodyCaptureOmitted = true
    upstreamCapture.clear()
    responseCapture.clear()
    diagnosticCapture.clear()
    streamLogger.info({
      event: 'gateway_stream_body_capture_omitted',
      reason: 'image_stream_payload',
      elapsedMs: Date.now() - startedAt,
      chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      sseEventCount: inspection.eventCount,
      lastSseEventType: inspection.lastEventType,
      recentSseEventTypes: inspection.recentEventTypes,
      eofPendingFlush: input.eofPendingFlush
    }, '网关识别到图像流输出，已省略流式响应正文捕获，仅保留元信息')
  }
  const bodyOmissionFor = (inspection: OpenAIStreamInspection) => bodyCaptureOmitted
    ? streamBodyOmissionSummary(inspection, totalUpstreamBytes, totalResponseBytes)
    : undefined
  const finishStreamResult = (
    completed: boolean,
    message: string,
    errorCode: string | undefined,
    firstTokenMs: number | undefined,
    usage: ParsedUsage,
    responseCapture: LimitedBufferCapture,
    upstreamCapture: LimitedBufferCapture,
    diagnosticCapture: LimitedBufferCapture,
    responseInspection?: ResponseInspectionDecision,
    outputReceived = false,
    estimatedOutputTokens?: number,
    imageOutputReceived = false,
    captureSuccessPayloads = true,
    bodyOmission?: StreamBodyOmissionSummary
  ): StreamPipeResult => streamResult(
    completed,
    message,
    errorCode,
    firstTokenMs,
    usage,
    responseCapture,
    upstreamCapture,
    diagnosticCapture,
    responseInspection,
    outputReceived,
    estimatedOutputTokens,
    imageOutputReceived,
    captureSuccessPayloads,
    bodyOmission,
    responseInspectionObservations,
    responseInspectionObservationOmittedCount,
    totalResponseBytes,
    preCommitChunks.length > 0 ? Buffer.concat(preCommitChunks) : undefined
  )
  const finishTerminalSuccess = (
    inspection: OpenAIStreamInspection,
    input: { drainForKeepAlive?: boolean; eofPendingFlush?: boolean } = {}
  ): StreamPipeResult => {
    omitBodyCaptureIfImageStream(inspection, { eofPendingFlush: input.eofPendingFlush })
    if (input.drainForKeepAlive) {
      res.off('close', closeIterator)
    }
    endResponse(res)
    if (input.drainForKeepAlive) {
      void drainIteratorAfterTerminalForKeepAlive(iterator)
    }
    streamLogger.info({
      event: 'gateway_stream_finished_success_after_terminal',
      elapsedMs: Date.now() - startedAt,
      chunkCount: chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      firstTokenMs,
      sseEventCount: inspection.eventCount,
      sseEventTypeCounts: inspection.eventTypeCounts,
      recentSseEventTypes: inspection.recentEventTypes,
      outputReceived: inspection.outputReceived,
      outputEventCount: inspection.outputEventCount,
      upstreamDrainScheduledForKeepAlive: input.drainForKeepAlive === true || undefined,
      eofPendingFlush: input.eofPendingFlush === true || undefined
    }, '网关已收到 OpenAI 终止事件并成功结束流式响应')
    return finishStreamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
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
        options.onFirstOutput?.()
      }
      if (!bodyCaptureOmitted) {
        upstreamCapture.push(buffer)
      }
      const transformedChunks = options.transformUpstreamChunk ? options.transformUpstreamChunk(buffer) : [buffer]
      const interceptResult = pushResponseInspectionChunks(interceptor, transformedChunks)
      if (interceptResult.parserSkipped && !responseInspectionParserSkipLogged) {
        responseInspectionParserSkipLogged = true
        streamLogger.info({
          event: 'gateway_response_inspection_parser_skipped'
        }, '网关流式事件过大，兜底拦截停止解析并继续原样转发')
      }
      recordResponseInspectionObservations(interceptResult.observations)
      let latestInspection = inspector.snapshot()
      if (shouldReturnResponseInspectionBeforeDownstreamWrite(interceptResult.intercepted, res, totalResponseBytes)) {
        await closeAsyncIterator(iterator)
        const decision = interceptResult.intercepted!
        const failurePayload = responseInspectionFailurePayloadForDecision(decision, options.clientRetryEnabled === true)
        const message = failurePayload.message
        const errorCode = failurePayload.errorCode
        streamLogger.warn({
          event: 'gateway_response_inspected_before_downstream_write',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          action: decision.action,
          reason: decision.reason,
          policyId: decision.policyId,
          policyName: decision.policyName,
          accountSwitch: decision.accountSwitch,
          retryEnabled: decision.retryEnabled
        }, '网关在写入下游前命中可服务端重试的响应检查策略')
        return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
      }
      if (interceptResult.intercepted && shouldFailBeforeDownstreamCommit()) {
        await closeAsyncIterator(iterator)
        const decision = interceptResult.intercepted
        const failurePayload = responseInspectionFailurePayloadForDecision(decision, options.clientRetryEnabled === true)
        const message = failurePayload.message
        const errorCode = failurePayload.errorCode
        streamLogger.warn({
          event: 'gateway_response_inspected_before_downstream_commit',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          action: decision.action,
          reason: decision.reason,
          upstreamEventType: decision.upstreamEventType,
          upstreamErrorCode: decision.upstreamErrorCode,
          rewriteErrorCode: decision.rewriteErrorCode,
          downstreamWritten: decision.downstreamWritten
        }, '网关在下游提交前命中流式失败，交由上层决定是否服务端换号重试')
        return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
      }
      let chunkSseEventCount = 0
      let chunkWriteMs = 0
      let chunkCanEndAfterTerminal = false
      for (const outbound of interceptResult.chunks) {
        latestInspection = inspector.pushChunk(outbound, {
          lightweightImageStream: bodyCaptureOmitted || latestInspection.imageOutputReceived
        })
        omitBodyCaptureIfImageStream(latestInspection)
        if (latestInspection.skipped && !parserSkipLogged) {
          parserSkipLogged = true
          streamLogger.warn({
            event: 'gateway_stream_inspector_skipped',
            reason: latestInspection.skipReason
          }, '网关流式解析超过上限，已停止解析并继续转发')
        }
        const outboundSseEventCount = latestInspection.eventCount - lastSseEventCount
        chunkSseEventCount += outboundSseEventCount
        const eventSummaries = inspector.drainEventSummaries()
        chunkCanEndAfterTerminal = chunkCanEndAfterTerminal || eventSummaries.some((summary) => summary.canEndStream)
        lastSseEventCount = latestInspection.eventCount
        if (latestInspection.skipped) {
          lastSseEventActivityAt = undefined
        } else if (lastSseEventActivityAt === undefined || outboundSseEventCount > 0) {
          lastSseEventActivityAt = lastUpstreamActivityAt
        }
        if (canKeepPreCommitBuffered(latestInspection, outbound)) {
          preCommitChunks.push(outbound)
          preCommitBufferedBytes += outbound.length
          continue
        }
        const writeStartedAt = Date.now()
        await flushPreCommitChunks()
        const writeResult = await writeDownstreamChunk(outbound)
        const writeMs = Date.now() - writeStartedAt
        chunkWriteMs += writeMs
        if (latestInspection.terminalReceived && !latestInspection.failedReceived && chunkCanEndAfterTerminal) {
          terminalEventWritten = true
          return finishTerminalSuccess(inspector.finish(), { drainForKeepAlive: true, eofPendingFlush: true })
        }
        const writeNow = Date.now()
        if (
          writeResult.backpressure
          && writeResult.logLevel === 'warn'
          && (writeResult.drainWaitMs ?? 0) >= responseBackpressureWarnThresholdMs
          && writeNow - lastBackpressureLogAt >= streamBackpressureLogIntervalMs
        ) {
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
        await closeAsyncIterator(iterator)
        const decision = interceptResult.intercepted
        const failurePayload = responseInspectionFailurePayloadForDecision(decision, options.clientRetryEnabled === true)
        const message = failurePayload.message
        const errorCode = failurePayload.errorCode
        if (shouldFailBeforeDownstreamCommit()) {
          streamLogger.warn({
            event: 'gateway_response_inspected_before_downstream_commit',
            elapsedMs: Date.now() - startedAt,
            chunkCount: chunkIndex,
            totalUpstreamBytes,
            totalResponseBytes,
            action: decision.action,
            reason: decision.reason,
            upstreamEventType: decision.upstreamEventType,
            upstreamErrorCode: decision.upstreamErrorCode,
            rewriteErrorCode: decision.rewriteErrorCode,
            downstreamWritten: decision.downstreamWritten
          }, '网关在下游提交前命中流式失败，交由上层决定是否服务端换号重试')
          return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
        }
        endResponse(res)
        streamLogger.warn({
          event: 'gateway_response_inspected',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          totalResponseBytes,
          action: decision.action,
          reason: decision.reason,
          upstreamEventType: decision.upstreamEventType,
          upstreamErrorCode: decision.upstreamErrorCode,
          rewriteErrorCode: decision.rewriteErrorCode,
          downstreamWritten: decision.downstreamWritten
        }, '网关已命中响应检查策略并结束当前流')
        return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
      }
    }

    const eofTransformedChunks = options.flushTransformedUpstreamChunks?.() ?? []
    const eofInterceptResult = mergeResponseInspectionSseResults(
      pushResponseInspectionChunks(interceptor, eofTransformedChunks),
      interceptor.flushPendingOnEof()
    )
    if (eofInterceptResult.parserSkipped && !responseInspectionParserSkipLogged) {
      responseInspectionParserSkipLogged = true
      streamLogger.info({
        event: 'gateway_response_inspection_parser_skipped'
      }, '网关流式事件过大，兜底拦截停止解析并继续原样转发')
    }
    recordResponseInspectionObservations(eofInterceptResult.observations)
    if (eofInterceptResult.chunks.length > 0 || eofInterceptResult.intercepted) {
      let latestInspection = inspector.snapshot()
      if (shouldReturnResponseInspectionBeforeDownstreamWrite(eofInterceptResult.intercepted, res, totalResponseBytes)) {
        const decision = eofInterceptResult.intercepted!
        const failurePayload = responseInspectionFailurePayloadForDecision(decision, options.clientRetryEnabled === true)
        const message = failurePayload.message
        const errorCode = failurePayload.errorCode
        streamLogger.warn({
          event: 'gateway_response_inspected_before_downstream_write',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          action: decision.action,
          reason: decision.reason,
          policyId: decision.policyId,
          policyName: decision.policyName,
          accountSwitch: decision.accountSwitch,
          retryEnabled: decision.retryEnabled,
          eofPendingFlush: true
        }, '网关在 EOF pending 事件写入下游前命中可服务端重试的响应检查策略')
        return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
      }
      if (eofInterceptResult.intercepted && shouldFailBeforeDownstreamCommit()) {
        const decision = eofInterceptResult.intercepted
        const failurePayload = responseInspectionFailurePayloadForDecision(decision, options.clientRetryEnabled === true)
        const message = failurePayload.message
        const errorCode = failurePayload.errorCode
        streamLogger.warn({
          event: 'gateway_response_inspected_before_downstream_commit',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          action: decision.action,
          reason: decision.reason,
          upstreamEventType: decision.upstreamEventType,
          upstreamErrorCode: decision.upstreamErrorCode,
          rewriteErrorCode: decision.rewriteErrorCode,
          downstreamWritten: decision.downstreamWritten,
          eofPendingFlush: true
        }, '网关在 EOF pending 事件下游提交前命中流式失败，交由上层决定是否服务端换号重试')
        return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
      }
      let eofCanEndAfterTerminal = false
      for (const outbound of eofInterceptResult.chunks) {
        latestInspection = inspector.pushChunk(outbound, {
          lightweightImageStream: bodyCaptureOmitted || latestInspection.imageOutputReceived
        })
        omitBodyCaptureIfImageStream(latestInspection, { eofPendingFlush: true })
        if (latestInspection.skipped && !parserSkipLogged) {
          parserSkipLogged = true
          streamLogger.warn({
            event: 'gateway_stream_inspector_skipped',
            reason: latestInspection.skipReason
          }, '网关流式解析超过上限，已停止解析并继续转发')
        }
        const outboundSseEventCount = latestInspection.eventCount - lastSseEventCount
        const eventSummaries = inspector.drainEventSummaries()
        eofCanEndAfterTerminal = eofCanEndAfterTerminal || eventSummaries.some((summary) => summary.canEndStream)
        lastSseEventCount = latestInspection.eventCount
        if (latestInspection.skipped) {
          lastSseEventActivityAt = undefined
        } else if (lastSseEventActivityAt === undefined || outboundSseEventCount > 0) {
          lastSseEventActivityAt = lastUpstreamActivityAt
        }
        if (canKeepPreCommitBuffered(latestInspection, outbound)) {
          preCommitChunks.push(outbound)
          preCommitBufferedBytes += outbound.length
          continue
        }
        await flushPreCommitChunks()
        const writeResult = await writeDownstreamChunk(outbound)
        if (latestInspection.terminalReceived && !latestInspection.failedReceived && eofCanEndAfterTerminal) {
          terminalEventWritten = true
          return finishTerminalSuccess(latestInspection, { eofPendingFlush: true })
        }
        const writeNow = Date.now()
        if (
          writeResult.backpressure
          && writeResult.logLevel === 'warn'
          && (writeResult.drainWaitMs ?? 0) >= responseBackpressureWarnThresholdMs
          && writeNow - lastBackpressureLogAt >= streamBackpressureLogIntervalMs
        ) {
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
        const decision = eofInterceptResult.intercepted
        const failurePayload = responseInspectionFailurePayloadForDecision(decision, options.clientRetryEnabled === true)
        const message = failurePayload.message
        const errorCode = failurePayload.errorCode
        if (shouldFailBeforeDownstreamCommit()) {
          streamLogger.warn({
            event: 'gateway_response_inspected_before_downstream_commit',
            elapsedMs: Date.now() - startedAt,
            chunkCount: chunkIndex,
            totalUpstreamBytes,
            totalResponseBytes,
            action: decision.action,
            reason: decision.reason,
            upstreamEventType: decision.upstreamEventType,
            upstreamErrorCode: decision.upstreamErrorCode,
            rewriteErrorCode: decision.rewriteErrorCode,
            downstreamWritten: decision.downstreamWritten,
            eofPendingFlush: true
          }, '网关在 EOF pending 事件下游提交前命中流式失败，交由上层决定是否服务端换号重试')
          return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
        }
        endResponse(res)
        streamLogger.warn({
          event: 'gateway_response_inspected',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          totalResponseBytes,
          action: decision.action,
          reason: decision.reason,
          upstreamEventType: decision.upstreamEventType,
          upstreamErrorCode: decision.upstreamErrorCode,
          rewriteErrorCode: decision.rewriteErrorCode,
          downstreamWritten: decision.downstreamWritten,
          eofPendingFlush: true
        }, '网关已在上游 EOF 时命中响应检查策略并结束当前流')
        return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, decision, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
      }
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    if (isUpstreamRequestAbortedError(error) || signal?.aborted) {
      const inspection = inspector.finish()
      omitBodyCaptureIfImageStream(inspection, { eofPendingFlush: true })
      if (inspection.terminalReceived && !inspection.failedReceived) {
        endResponse(res)
        streamLogger.info({
          event: 'gateway_stream_client_closed_after_terminal',
          elapsedMs: Date.now() - startedAt,
          chunkCount: chunkIndex,
          totalUpstreamBytes,
          totalResponseBytes,
          signalAborted: signal?.aborted,
          terminalEventWritten,
          outputReceived: inspection.outputReceived,
          outputEventCount: inspection.outputEventCount,
          sseEventCount: inspection.eventCount,
          sseEventTypeCounts: inspection.eventTypeCounts,
          recentSseEventTypes: inspection.recentEventTypes,
          parserSkipped: inspection.skipped,
          skipReason: inspection.skipReason,
          errorMessage: error instanceof Error ? error.message : String(error)
        }, '客户端在 OpenAI 终止事件后关闭连接，按成功流式响应收尾')
        return finishStreamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
      }
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
          errorMessage: downstreamConnectionClosedMessage
        }, '网关流式转发因下游连接提前关闭而结束')
      endResponse(res)
      throw error
    }
    const rawMessage = error instanceof Error ? error.message : '上游流式响应已中断'
    const inspection = inspector.finish()
    omitBodyCaptureIfImageStream(inspection, { eofPendingFlush: true })
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
      return finishStreamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
    }
    const message = inspection.errorMessage ?? rawMessage
    const errorCode = streamClientFailureCode(
      inspection.errorCode ?? gatewayStreamFailureCode(message),
      inspection.outputReceived,
      options.clientRetryEnabled === true
    )
    handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived))
    if (shouldFailBeforeDownstreamCommit()) {
      streamLogger.warn({
        event: 'gateway_stream_failure_before_downstream_commit',
        message,
        errorCode,
        totalUpstreamBytes,
        totalResponseBytes
      }, '网关在下游提交前捕获流式失败，交由上层决定是否服务端换号重试')
      return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
    }
    if (!inspection.failedReceived) {
      streamLogger.warn({
        event: 'gateway_stream_failure_event_writing',
        message
      }, '网关准备补发 response.failed')
      prepareDownstreamForWrite()
      const failureEvent = await writeGatewayStreamFailureEventWithBackpressure(res, message, errorCode)
      if (failureEvent) {
        if (!bodyCaptureOmitted) {
          responseCapture.push(failureEvent)
          diagnosticCapture.push(failureEvent)
        }
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
    return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
  } finally {
    res.off('close', closeIterator)
  }

  const inspection = inspector.finish()
  omitBodyCaptureIfImageStream(inspection, { eofPendingFlush: true })
  if (inspection.skipped) {
    endResponse(res)
    const success = completed && !inspection.failedReceived
    const message = success ? '已完成' : (inspection.errorMessage ?? '上游流式响应失败')
    const errorCode = success ? undefined : streamClientFailureCode(
      inspection.errorCode ?? gatewayStreamFailureCode(message),
      inspection.outputReceived,
      options.clientRetryEnabled === true
    )
    if (!success) {
      handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived))
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
    return finishStreamResult(success, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
  }
  if (!inspection.terminalReceived) {
    const message = '上游流在 OpenAI 终止事件前结束'
    const errorCode = streamClientFailureCode(
      gatewayStreamFailureCode(message),
      inspection.outputReceived,
      options.clientRetryEnabled === true
    )
    handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived))
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
    if (shouldFailBeforeDownstreamCommit()) {
      streamLogger.warn({
        event: 'gateway_stream_missing_terminal_before_downstream_commit',
        errorCode,
        totalUpstreamBytes,
        totalResponseBytes
      }, '网关在下游提交前发现上游缺少终止事件，交由上层决定是否服务端换号重试')
      return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
    }
    prepareDownstreamForWrite()
    const failureEvent = await writeGatewayStreamFailureEventWithBackpressure(res, message, errorCode)
    if (failureEvent) {
      if (!bodyCaptureOmitted) {
        responseCapture.push(failureEvent)
        diagnosticCapture.push(failureEvent)
      }
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
    return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
  }

  endResponse(res)

  if (!completed || inspection.failedReceived) {
    const message = inspection.errorMessage ?? '上游流式响应失败'
    const errorCode = streamClientFailureCode(
      inspection.errorCode ?? gatewayStreamFailureCode(message),
      inspection.outputReceived,
      options.clientRetryEnabled === true
    )
    handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived))
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
    return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
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
  return finishStreamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
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

async function drainIteratorAfterTerminalForKeepAlive(iterator: AsyncIterator<Uint8Array>): Promise<boolean> {
  const deadline = Date.now() + streamTerminalKeepAliveDrainMs
  try {
    while (Date.now() < deadline) {
      const result = await readIteratorNextWithTimeout(iterator, Math.max(1, deadline - Date.now()))
      if (!result) {
        await closeAsyncIterator(iterator)
        return false
      }
      if (result.done) {
        return true
      }
    }
    await closeAsyncIterator(iterator)
    return false
  } catch {
    await closeAsyncIterator(iterator)
    return false
  }
}

async function readIteratorNextWithTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutMs: number
): Promise<IteratorResult<Uint8Array> | undefined> {
  let clearTimer: (() => void) | undefined
  try {
    return await Promise.race([
      iterator.next(),
      new Promise<undefined>((resolve) => {
        const timer = setTimeout(resolve, Math.max(1, timeoutMs)) as unknown
        clearTimer = () => clearTimeout(timer as Parameters<typeof clearTimeout>[0])
        const unrefableTimer = timer as { unref?: () => void }
        if (typeof timer === 'object' && timer !== null && typeof unrefableTimer.unref === 'function') {
          unrefableTimer.unref()
        }
      })
    ])
  } finally {
    clearTimer?.()
  }
}

interface StreamReadPlan {
  phase: 'first_chunk' | 'active_stream' | 'no_circuit_breaker'
  timeoutMs?: number
  rawTimeoutMs?: number
  timeoutKind?: 'first_chunk' | 'upstream_activity'
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
    // Raw upstream activity is the hard timeout. Incomplete SSE events are diagnostic only:
    // large or fragmented events can stay valid while bytes continue to arrive.
    return {
      phase: 'active_stream',
      timeoutMs: rawTimeoutMs,
      rawTimeoutMs,
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

function streamFailureContext(downstreamBytesWritten: number, outputReceived: boolean): StreamFailureContext {
  return {
    downstreamBytesWritten,
    outputReceived
  }
}

function pushResponseInspectionChunks(
  interceptor: OpenAIResponseInspectionBuffer,
  chunks: Buffer[]
): ResponseInspectionSseResult {
  let result: ResponseInspectionSseResult = {
    chunks: [],
    parserSkipped: false
  }
  for (const chunk of chunks) {
    result = mergeResponseInspectionSseResults(result, interceptor.pushChunk(chunk))
    if (result.intercepted) {
      break
    }
  }
  return result
}

function mergeResponseInspectionSseResults(
  left: ResponseInspectionSseResult,
  right: ResponseInspectionSseResult
): ResponseInspectionSseResult {
  return {
    chunks: [...left.chunks, ...right.chunks],
    intercepted: left.intercepted ?? right.intercepted,
    observations: [
      ...(left.observations ?? []),
      ...(right.observations ?? [])
    ].length
      ? [...(left.observations ?? []), ...(right.observations ?? [])]
      : undefined,
    parserSkipped: left.parserSkipped || right.parserSkipped
  }
}

function streamClientFailureCode(errorCode: string, outputReceived: boolean, clientRetryEnabled: boolean): string {
  return clientRetryEnabled && !outputReceived
    ? gatewayStreamClientRetryErrorCode
    : errorCode
}

function shouldReturnResponseInspectionBeforeDownstreamWrite(
  decision: ResponseInspectionDecision | undefined,
  res: Response,
  totalResponseBytes: number
): boolean {
  return decision?.reason === 'configured_response_policy'
    && decision.retryEnabled === true
    && decision.policySource !== 'system_default'
    && totalResponseBytes === 0
    && !res.headersSent
    && !res.writableEnded
    && !res.destroyed
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
  responseInspection?: ResponseInspectionDecision,
  outputReceived = false,
  estimatedOutputTokens?: number,
  imageOutputReceived = false,
  captureSuccessPayloads = true,
  bodyOmission?: StreamBodyOmissionSummary,
  responseInspectionObservations: ResponseInspectionDecision[] = [],
  responseInspectionObservationOmittedCount = 0,
  downstreamBytesWritten = 0,
  uncommittedResponseBody?: Buffer
): StreamPipeResult {
  const responseBodyText = bodyOmission || (completed && !captureSuccessPayloads)
    ? undefined
    : diagnosticCapture.toDiagnosticText()
  const auditResponseBody = bodyOmission
    ? undefined
    : captureSuccessPayloads
      ? responseCapture.completeBuffer()
      : completed ? undefined : diagnosticCapture.completeBuffer()
  return {
    completed,
    message,
    errorCode,
    firstTokenMs,
    usage,
    outputReceived,
    imageOutputReceived,
    estimatedOutputTokens,
    responseBodyText,
    auditResponseBody,
    auditUpstreamBody: auditUpstreamBodyForResult(upstreamCapture, completed, captureSuccessPayloads, bodyOmission),
    downstreamBytesWritten,
    uncommittedResponseBody,
    responseInspection,
    responseInspectionObservations: responseInspectionObservations.length ? [...responseInspectionObservations] : undefined,
    responseInspectionObservationOmittedCount: responseInspectionObservationOmittedCount > 0 ? responseInspectionObservationOmittedCount : undefined,
    bodyOmission
  }
}

function auditUpstreamBodyForResult(
  upstreamCapture: LimitedBufferCapture,
  completed: boolean,
  captureSuccessPayloads: boolean,
  bodyOmission?: StreamBodyOmissionSummary
): Buffer | undefined {
  if (bodyOmission) {
    return undefined
  }
  if (captureSuccessPayloads) {
    return upstreamCapture.completeBuffer()
  }
  if (completed) {
    return undefined
  }
  const buffer = upstreamCapture.buffer()
  return buffer.byteLength > 0 ? buffer : undefined
}

function streamBodyOmissionSummary(
  inspection: OpenAIStreamInspection,
  totalUpstreamBytes: number,
  totalResponseBytes: number
): StreamBodyOmissionSummary {
  return {
    omitted: true,
    reason: 'image_stream_payload',
    message: '图像流正文已省略，避免在日志和审计中保存图片字节',
    totalUpstreamBytes,
    totalResponseBytes,
    sseEventCount: inspection.eventCount,
    lastSseEventType: inspection.lastEventType,
    recentSseEventTypes: inspection.recentEventTypes,
    imageOutputReceived: inspection.imageOutputReceived,
    terminalReceived: inspection.terminalReceived,
    failedReceived: inspection.failedReceived
  }
}

async function writeGatewayStreamFailureEventWithBackpressure(res: Response, message: string, code?: string): Promise<Buffer | undefined> {
  const buffer = writeGatewayStreamFailureEvent(res, message, code)
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
