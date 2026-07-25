import type { Response } from 'express'

import { getRequestLogger, markRequestProtocolTerminalOutcome } from '../../../shared/request-context.js'
import type {
  GatewayCommittedFailureSignal,
  OpenAIGatewayDownstreamProtocol
} from '../client-profiles/strategy.js'
import type { GatewayTimeoutProfile } from '../policy/timeout-profile.js'
import { downstreamConnectionClosedMessage } from './client-abort.js'
import {
  emptyUsage,
  type ParsedUsage
} from '../usage/types.js'
import {
  isStartedUpstreamBodyTransportError,
  isUpstreamRequestAbortedError,
  UpstreamRequestAbortedError
} from '../upstream/request.js'
import { GatewayFirstByteTimeoutError, isGatewayFirstByteTimeoutError } from '../upstream/first-byte-timeout.js'
import {
  decideFirstByteDeadlineAfterPendingRead,
  GatewayResponsePrecommitDeadlineError,
  isGatewayResponsePrecommitDeadlineError,
  observeFirstBytePendingRead,
  type FirstByteDeadlineAction,
  type FirstByteDeadlineHandler
} from '../upstream/first-byte-deadline.js'
import {
  gatewayStreamClientRetryErrorCode,
  gatewayStreamClientRetryMessage,
  gatewayStreamFailureCode,
  type GatewayErrorProtocol,
  writeGatewayStreamFailureEvent
} from './responses.js'
import { buildStreamReadPlan } from './stream-read-plan.js'
import {
  closeAsyncIterator,
  destroyResponseForUpstreamBodyError,
  endResponse,
  LimitedBufferCapture,
  bufferFromUint8Array,
  responseBackpressureWarnThresholdMs,
  writeResponseChunk
} from '../upstream/body.js'
import {
  responseInspectionFailurePayloadForDecision,
  type ResponseInspectionDecision,
  type ResponseInspectionRuntimeContext,
  type RuntimeResponseInspectionPolicy
} from './inspection.js'
import {
  OpenAIResponseInspectionBuffer,
  type ResponseInspectionSseResult
} from '../protocols/openai-v1/response-inspection-buffer.js'
import type {
  ResponseEndpointFamily,
  ResponseProtocolCode
} from '../protocols/openai-v1/response-semantics.js'
import type {
  GatewayStreamInspection,
  GatewayStreamInspector
} from '../protocols/_shared/types.js'
import { requireGatewayProtocolDriverForResponseProtocol } from '../protocols/registry.js'
import {
  appendStreamPreCommitChunk,
  canKeepStreamPreCommitChunk,
  clearStreamPreCommitChunks,
  createStreamPreCommitBufferState,
  shouldFailBeforeStreamDownstreamCommit,
  StreamPreCommitSseEvidence,
  takeStreamPreCommitChunks,
  uncommittedStreamResponseBody,
  wouldExceedStreamPreCommitBuffer
} from './stream-pre-commit-buffer.js'
import {
  shouldReturnResponseInspectionBeforeDownstreamWrite,
  streamClientFailureCode
} from './stream-retry-decision.js'
import {
  streamBodyOmissionSummary,
  streamResult,
  type StreamBodyOmissionSummary,
  type StreamPipeResult,
  type StreamTransportFailure
} from './stream-result.js'
import { GatewayDownstreamCommitState } from './downstream-commit-state.js'
import {
  rewriteCodexResponsesSseEvent,
  type CodexResponsesResponseGuard
} from '../codex-responses/response-guard.js'
export type { StreamBodyOmissionSummary, StreamPipeResult } from './stream-result.js'

export interface StreamFailureContext {
  downstreamBytesWritten: number
  outputReceived: boolean
  protocolFailureEventReceived?: boolean
}

export interface StreamPipeOptions {
  clientRetryEnabled?: boolean
  committedFailureSignal?: GatewayCommittedFailureSignal
  interpretProtocolFailures?: boolean
  onFirstOutput?: () => void
  captureSuccessPayloads?: boolean
  retryBeforeDownstreamWriteUntilOutput?: boolean
  responseInspectionPolicies?: RuntimeResponseInspectionPolicy[]
  responseInspectionContext?: ResponseInspectionRuntimeContext
  downstreamProtocol?: OpenAIGatewayDownstreamProtocol
  responseProtocol?: ResponseProtocolCode
  endpointFamily?: ResponseEndpointFamily
  firstByteTimeoutMs?: number
  firstByteDeadlineMs?: number
  responsePrecommitDeadlineAtMs?: number
  onFirstByteDeadline?: FirstByteDeadlineHandler
  onFirstByteDeadlineSuperseded?: () => void
  prepareDownstream?: () => void
  beforeDownstreamCommit?: (input: { responseResourceId?: string }) => Promise<void>
  transformUpstreamChunk?: (chunk: Buffer) => Buffer[]
  flushTransformedUpstreamChunks?: () => Buffer[]
  downstreamCommitState?: GatewayDownstreamCommitState
  codexResponsesGuard?: CodexResponsesResponseGuard
}

const streamDiagnosticCaptureBytes = 256 * 1024
const streamAuditCaptureBytes = 1024 * 1024
const streamTerminalKeepAliveDrainMs = 50
const streamProgressLogIntervalMs = 60_000
const streamBackpressureLogIntervalMs = 30_000
const maxResponseInspectionObservationCount = 20

class StreamReadPlanTimeoutError extends Error {
  constructor(message: string, readonly timeoutKind: ReturnType<typeof buildStreamReadPlan>['timeoutKind']) {
    super(message)
    this.name = 'StreamReadPlanTimeoutError'
  }
}

class StreamMaxLifetimeExceededError extends StreamReadPlanTimeoutError {
  constructor(message: string) {
    super(message, 'stream_lifetime')
    this.name = 'StreamMaxLifetimeExceededError'
  }
}

class StreamPreCommitBufferExceededError extends Error {
  readonly code = 'stream_precommit_buffer_exceeded'

  constructor() {
    super('流式响应在语义提交前超过安全缓冲上限')
    this.name = 'StreamPreCommitBufferExceededError'
  }
}

class StreamBeforeDownstreamCommitError extends Error {
  constructor(readonly originalError: unknown) {
    super('流式响应下游提交前准备失败')
    this.name = 'StreamBeforeDownstreamCommitError'
  }
}

interface StreamFirstByteDeadlineReadDecision {
  action?: FirstByteDeadlineAction
  decisionError?: unknown
}

interface StreamChunkReadResult {
  result: IteratorResult<Uint8Array>
  firstByteDeadlineObserved: boolean
  firstByteDeadlineReadDecision?: StreamFirstByteDeadlineReadDecision
}

export async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  timeoutProfile: GatewayTimeoutProfile,
  startedAt: number,
  handleStreamFailure: (reason: string, errorCode: string | undefined, context: StreamFailureContext) => Promise<void>,
  signal?: AbortSignal,
  options: StreamPipeOptions = {}
): Promise<StreamPipeResult> {
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const responseProtocol = options.responseProtocol ?? 'openai_v1'
  const protocolDriver = requireGatewayProtocolDriverForResponseProtocol(responseProtocol)
  const gatewayErrorProtocol = protocolDriver.clientErrorProtocol
  const committedProtocolFailureEventEnabled = (
    options.committedFailureSignal
      ?? (options.clientRetryEnabled === true ? 'protocol_error_event' : 'disconnect')
  ) === 'protocol_error_event'
  const inspector = protocolDriver.createStreamInspector()
  const codexResponsesGuard = options.codexResponsesGuard
  const codexSafeRepairEnabled = codexResponsesGuard?.mode === 'safe_repair'
  const codexStrictInterceptEnabled = codexResponsesGuard?.mode === 'strict_intercept'
  if (codexResponsesGuard && !codexSafeRepairEnabled && !codexStrictInterceptEnabled && inspector.setParsedEventObserver) {
    inspector.setParsedEventObserver((event) => {
      try {
        codexResponsesGuard.inspectOpenAiSseEvent(event)
      } catch (error) {
        getRequestLogger().error({ error }, 'Codex Responses 流式协议检查失败，shadow 模式继续透传')
      }
    })
  }
  if (codexResponsesGuard && inspector.setParserCoverageObserver) {
    inspector.setParserCoverageObserver(() => {
      codexResponsesGuard.observeCoverageGap()
    })
  }
  const interpretProtocolFailures = options.interpretProtocolFailures !== false
  const hasResponseInspectionPolicies = (options.responseInspectionPolicies?.length ?? 0) > 0
  const responseInspectionEnabled = interpretProtocolFailures
    && options.responseInspectionContext?.clientProfile !== 'generic_anthropic'
    && (options.clientRetryEnabled === true || hasResponseInspectionPolicies)
    || hasResponseInspectionPolicies
    || codexSafeRepairEnabled
    || codexStrictInterceptEnabled
  const interceptor = responseInspectionEnabled
    ? new OpenAIResponseInspectionBuffer({
      clientRetryEnabled: options.clientRetryEnabled === true,
      policies: options.responseInspectionPolicies,
      endpointFamily: protocolDriver.responseInspectionEndpointFamily(options.endpointFamily),
      context: options.responseInspectionContext,
      extractSemanticFrames: (event) => protocolDriver.extractSseSemanticFrames(event, options.endpointFamily),
      ...(protocolDriver.sseResponseInspectionFailureEvent === 'none'
        ? {
            buildFailureEvent: () => undefined
          }
        : {}),
      ...(codexSafeRepairEnabled && codexResponsesGuard
        ? {
            transformEvent: (event: import('../protocols/openai-v1/stream-events.js').ParsedOpenAIStreamEvent) => {
              const result = codexResponsesGuard.inspectOpenAiSseEvent(event)
              if (result.outcome === 'blocked' && result.retryable) {
                return { intercepted: codexResponsesProtocolDecision(result, false) }
              }
              const rewritten = rewriteCodexResponsesSseEvent(event, result.repairs)
              if (rewritten) codexResponsesGuard.recordAppliedSseRepairs(result.repairs.length)
              return rewritten
            }
          }
        : {}),
      ...(codexStrictInterceptEnabled && codexResponsesGuard
        ? {
            transformEvent: (event: import('../protocols/openai-v1/stream-events.js').ParsedOpenAIStreamEvent) => {
              const result = codexResponsesGuard.inspectOpenAiSseEvent(event)
              if (result.outcome !== 'repairable' && result.outcome !== 'blocked') return undefined
              return { intercepted: codexResponsesProtocolDecision(result, true) }
            }
          }
        : {})
    })
    : undefined
  const captureSuccessPayloads = options.captureSuccessPayloads !== false
  // A protocol-declared failure terminal is a framing fact, not a claim about
  // the provider-specific code or message carried inside it.
  const interpretedProtocolFailure = (inspection: GatewayStreamInspection) => inspection.failedReceived
  const responseCapture = new LimitedBufferCapture(captureSuccessPayloads ? streamAuditCaptureBytes : -1)
  const upstreamCapture = new LimitedBufferCapture(captureSuccessPayloads ? streamAuditCaptureBytes : streamDiagnosticCaptureBytes)
  const diagnosticCapture = new LimitedBufferCapture(streamDiagnosticCaptureBytes)
  const streamLogger = getRequestLogger()
  let completed = false
  let parserSkipLogged = false
  let responseInspectionParserSkipLogged = false
  let firstTokenMs: number | undefined
  let firstByteDeadlineObserved = false
  let pendingFirstByteDeadlineReadDecision: StreamFirstByteDeadlineReadDecision | undefined
  let waitingForFirstChunk = true
  let lastUpstreamActivityAt = startedAt
  let lastSseEventActivityAt: number | undefined
  let lastSseEventCount = 0
  let upstreamChunkReceived = false
  let semanticResultReceived = false
  let responseResourceId: string | undefined
  let pendingProtocolEvent = false
  let streamParserSkipped = false
  let protocolTerminalReceived = false
  let chunkIndex = 0
  let totalUpstreamBytes = 0
  let totalResponseBytes = 0
  let lastProgressLogAt = startedAt
  let lastBackpressureLogAt = 0
  let clientClosed = false
  let terminalEventWritten = false
  let bodyCaptureOmitted = false
  let downstreamPrepared = false
  const downstreamCommit = options.downstreamCommitState ?? new GatewayDownstreamCommitState()
  let downstreamCommitPrepared = false
  const preCommitBuffer = createStreamPreCommitBufferState(options.retryBeforeDownstreamWriteUntilOutput === true)
  const preCommitSseEvidence = new StreamPreCommitSseEvidence()
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
  const ensureBeforeDownstreamCommit = async () => {
    if (downstreamCommitPrepared || !options.beforeDownstreamCommit) return
    try {
      await options.beforeDownstreamCommit({ responseResourceId })
      downstreamCommitPrepared = true
    } catch (error) {
      throw new StreamBeforeDownstreamCommitError(error)
    }
  }
  const writeDownstreamChunk = async (chunk: Buffer, semantic = false) => {
    await ensureBeforeDownstreamCommit()
    captureDownstreamChunk(chunk)
    prepareDownstreamForWrite()
    const writeResult = await writeResponseChunk(res, chunk)
    interceptor?.markDownstreamWrite()
    totalResponseBytes += chunk.length
    if (semantic) {
      downstreamCommit.markSemanticCommitted(chunk.length)
    } else {
      downstreamCommit.markTransportCommitted(chunk.length)
    }
    return writeResult
  }
  const canKeepPreCommitBuffered = (inspection: GatewayStreamInspection, chunk: Buffer) => {
    return canKeepStreamPreCommitChunk(preCommitBuffer, {
      inspection,
      chunk,
      totalResponseBytes,
      response: res
    })
  }
  const appendPreCommitChunk = (chunk: Buffer) => {
    if (preCommitSseEvidence.onlyNonSemanticFramingObserved) {
      clearStreamPreCommitChunks(preCommitBuffer)
      return
    }
    appendStreamPreCommitChunk(preCommitBuffer, chunk)
  }
  const flushPreCommitChunks = async () => {
    const chunks = takeStreamPreCommitChunks(preCommitBuffer)
    if (chunks.length === 0) {
      return
    }
    const semantic = preCommitSseEvidence.dataPayloadStarted || preCommitSseEvidence.dataEventObserved
    for (let index = 0; index < chunks.length; index += 1) {
      await writeDownstreamChunk(chunks[index]!, semantic && index === chunks.length - 1)
    }
  }
  const discardPreCommitChunks = () => {
    if (preCommitBuffer.chunks.length > 0) takeStreamPreCommitChunks(preCommitBuffer)
  }
  const shouldFailBeforeDownstreamCommit = () => {
    return shouldFailBeforeStreamDownstreamCommit(preCommitBuffer, {
      totalResponseBytes,
      response: res
    })
  }
  const shouldKeepNonSemanticSseFramingPrivate = () => {
    return preCommitBuffer.buffering
      && preCommitSseEvidence.onlyNonSemanticFramingObserved
      && totalResponseBytes === 0
      && !res.writableEnded
      && !res.destroyed
      && !downstreamCommit.semanticCommitted
  }
  const shouldRejectOversizedUncommittedSseFraming = (
    inspection: GatewayStreamInspection,
    chunk: Buffer
  ) => {
    return preCommitBuffer.buffering
      && totalResponseBytes === 0
      && !res.writableEnded
      && !res.destroyed
      && !downstreamCommit.semanticCommitted
      && !inspection.outputReceived
      && !inspection.terminalReceived
      && !inspection.failedReceived
      && !preCommitSseEvidence.onlyNonSemanticFramingObserved
      && !preCommitSseEvidence.dataPayloadStarted
      && !preCommitSseEvidence.dataEventObserved
      && (inspection.skipped || wouldExceedStreamPreCommitBuffer(preCommitBuffer, chunk))
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
  const markFirstSemanticOutput = (inspection: GatewayStreamInspection) => {
    if (firstTokenMs !== undefined || !streamOutputReceived(inspection)) return
    firstTokenMs = Date.now() - startedAt
    options.onFirstOutput?.()
  }
  const updateStreamInspectionProgress = (inspection: GatewayStreamInspection) => {
    markFirstSemanticOutput(inspection)
    semanticResultReceived = semanticResultReceived || streamSemanticResultReceived(inspection)
    pendingProtocolEvent = inspection.pendingEvent
    streamParserSkipped = inspection.skipped
    protocolTerminalReceived = protocolTerminalReceived || inspection.terminalReceived
    responseResourceId ??= inspection.responseResourceId
    if (inspection.failedReceived) {
      markRequestProtocolTerminalOutcome('failure')
    } else if (inspection.terminalReceived) {
      markRequestProtocolTerminalOutcome('success')
    }
  }
  const settleStreamFirstByteDeadlineReadDecision = (semanticResultInRead: boolean) => {
    const decision = pendingFirstByteDeadlineReadDecision
    if (!decision) return
    pendingFirstByteDeadlineReadDecision = undefined
    if (semanticResultInRead) {
      options.onFirstByteDeadlineSuperseded?.()
      return
    }
    if (decision.decisionError !== undefined) throw decision.decisionError
    if (decision.action === 'abort') {
      const deadlineMs = options.firstByteDeadlineMs ?? 0
      throw new GatewayFirstByteTimeoutError(
        `上游流式响应 ${Math.ceil(deadlineMs / 1000)}s 后仍未返回首个有效输出`,
        deadlineMs,
        'configured_deadline'
      )
    }
  }
  const closeIterator = () => {
    clientClosed = true
    void closeAsyncIterator(iterator)
  }
  const omitBodyCaptureIfImageStream = (
    inspection: GatewayStreamInspection,
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
  const bodyOmissionFor = (inspection: GatewayStreamInspection) => bodyCaptureOmitted
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
  ): StreamPipeResult => {
    const guardSnapshot = codexResponsesGuard?.snapshot()
    codexResponsesGuard?.dispose()
    return streamResult(
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
      downstreamCommit.downstreamBytesWritten,
      totalResponseBytes,
      downstreamCommit.transportCommitted || res.headersSent,
      downstreamCommit.semanticCommitted,
      uncommittedStreamResponseBody(preCommitBuffer),
      responseResourceId,
      guardSnapshot,
      completed && protocolTerminalReceived && !streamParserSkipped
    )
  }
  const signalCommittedStreamFailure = async (
    inspection: GatewayStreamInspection
  ): Promise<'signaled' | 'interrupted'> => {
    if (terminalEventWritten || !committedProtocolFailureEventEnabled) {
      interruptResponse(res)
      return 'interrupted'
    }
    const clientMessage = inspection.outputReceived
      ? '上游流式响应在输出后中断'
      : gatewayStreamClientRetryMessage
    const clientErrorCode = inspection.outputReceived
      ? gatewayStreamFailureCode(clientMessage)
      : gatewayStreamClientRetryErrorCode
    prepareDownstreamForWrite()
    const failureEvent = await writeGatewayStreamFailureEventWithBackpressure(
      res,
      clientMessage,
      clientErrorCode,
      gatewayErrorProtocol,
      options.downstreamProtocol
    )
    if (!failureEvent) {
      interruptResponse(res)
      return 'interrupted'
    }
    if (!bodyCaptureOmitted) {
      responseCapture.push(failureEvent)
      diagnosticCapture.push(failureEvent)
    }
    totalResponseBytes += failureEvent.length
    downstreamCommit.markSemanticCommitted(failureEvent.length)
    terminalEventWritten = true
    endResponse(res)
    return 'signaled'
  }
  const finishTerminalSuccess = async (
    inspection: GatewayStreamInspection,
    input: { drainForKeepAlive?: boolean; eofPendingFlush?: boolean } = {}
  ): Promise<StreamPipeResult> => {
    let finalInspection = inspection
    let closeIteratorAfterEnd = false
    omitBodyCaptureIfImageStream(finalInspection, { eofPendingFlush: input.eofPendingFlush })
    if (input.drainForKeepAlive && !interpretedProtocolFailure(finalInspection)) {
      res.off('close', closeIterator)
      finalInspection = await drainIteratorAfterTerminalForInspection(iterator, inspector, {
        lightweightImageStream: bodyCaptureOmitted || finalInspection.imageOutputReceived
      })
      updateStreamInspectionProgress(finalInspection)
      omitBodyCaptureIfImageStream(finalInspection, { eofPendingFlush: true })
    } else {
      res.off('close', closeIterator)
      closeIteratorAfterEnd = true
    }
    if (interpretedProtocolFailure(finalInspection)) {
      const message = '上游流式响应在成功终态后返回矛盾失败终态'
      const errorCode = 'upstream_protocol_failure'
      await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, finalInspection.outputReceived, interpretedProtocolFailure(finalInspection)))
      interruptResponse(res)
      if (closeIteratorAfterEnd) {
        void closeAsyncIterator(iterator)
      }
      streamLogger.warn({
        event: 'gateway_stream_failed_after_terminal',
        elapsedMs: Date.now() - startedAt,
        chunkCount: chunkIndex,
        totalUpstreamBytes,
        totalResponseBytes,
        firstTokenMs,
        message,
        errorCode,
        sseEventCount: finalInspection.eventCount,
        sseEventTypeCounts: finalInspection.eventTypeCounts,
        recentSseEventTypes: finalInspection.recentEventTypes,
        outputReceived: finalInspection.outputReceived,
        outputEventCount: finalInspection.outputEventCount,
        eofPendingFlush: input.eofPendingFlush === true || undefined
      }, '网关在终止事件后解析到失败事件，按失败流式响应收尾')
      return finishStreamResult(false, message, errorCode, firstTokenMs, finalInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, finalInspection.outputReceived, finalInspection.estimatedOutputTokens, finalInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(finalInspection))
    }
    await ensureBeforeDownstreamCommit()
    endResponse(res)
    if (closeIteratorAfterEnd) {
      void closeAsyncIterator(iterator)
    }
    streamLogger.debug({
      event: 'gateway_stream_finished_success_after_terminal',
      elapsedMs: Date.now() - startedAt,
      chunkCount: chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      firstTokenMs,
      sseEventCount: finalInspection.eventCount,
      sseEventTypeCounts: finalInspection.eventTypeCounts,
      recentSseEventTypes: finalInspection.recentEventTypes,
      outputReceived: finalInspection.outputReceived,
      outputEventCount: finalInspection.outputEventCount,
      upstreamDrainScheduledForKeepAlive: input.drainForKeepAlive === true || undefined,
      eofPendingFlush: input.eofPendingFlush === true || undefined
    }, '网关已收到协议终止事件并成功结束流式响应')
    return finishStreamResult(true, '已完成', undefined, firstTokenMs, finalInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, finalInspection.outputReceived, finalInspection.estimatedOutputTokens, finalInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(finalInspection))
  }
  res.once('close', closeIterator)

  streamLogger.debug({
    event: 'gateway_stream_pipe_started',
    firstResponseTimeoutMs: timeoutProfile.firstResponseTimeoutMs,
    idleTimeoutMs: timeoutProfile.idleTimeoutMs,
    uncommittedAttemptMaxLifetimeMs: timeoutProfile.uncommittedAttemptMaxLifetimeMs,
    startedAt
  }, '网关开始转发上游流式响应')

  try {
    while (true) {
      if (clientClosed || res.destroyed) {
        throw new Error('客户端连接已断开')
      }
      const readStartedAt = Date.now()
      const readResult = await readNextStreamChunk(iterator, timeoutProfile, startedAt, {
        waitingForFirstChunk,
        lastUpstreamActivityAt,
        lastSseEventActivityAt,
        upstreamChunkReceived,
        semanticResultReceived,
        pendingProtocolEvent,
        parserSkipped: streamParserSkipped,
        waitingForFirstOutput: options.firstByteDeadlineMs !== undefined
          && firstTokenMs === undefined
          && totalResponseBytes === 0
          && !downstreamCommit.semanticCommitted,
        firstByteDeadlineObserved
      }, signal, {
        firstByteDeadlineMs: options.firstByteDeadlineMs,
        responsePrecommitDeadlineAtMs: options.responsePrecommitDeadlineAtMs,
        onFirstByteDeadline: options.onFirstByteDeadline,
        onFirstByteDeadlineSuperseded: options.onFirstByteDeadlineSuperseded
      })
      firstByteDeadlineObserved = readResult.firstByteDeadlineObserved
      pendingFirstByteDeadlineReadDecision = readResult.firstByteDeadlineReadDecision
      const result = readResult.result
      const readWaitMs = Date.now() - readStartedAt

      if (result.done) {
        settleStreamFirstByteDeadlineReadDecision(false)
        completed = true
        break
      }

      const buffer = bufferFromUint8Array(result.value)
      chunkIndex += 1
      totalUpstreamBytes += buffer.length
      upstreamChunkReceived = true
      waitingForFirstChunk = false
      lastUpstreamActivityAt = Date.now()
      if (!bodyCaptureOmitted) {
        upstreamCapture.push(buffer)
      }
      const transformedChunks = options.transformUpstreamChunk ? options.transformUpstreamChunk(buffer) : [buffer]
      const interceptResult = interceptor
        ? pushResponseInspectionChunks(interceptor, transformedChunks)
        : passThroughResponseInspectionChunks(transformedChunks)
      pendingProtocolEvent = interceptResult.pendingEvent === true
      if (interceptResult.pendingEvent === true) {
        lastSseEventActivityAt = lastUpstreamActivityAt
      }
      if (interceptResult.parserSkipped && !responseInspectionParserSkipLogged) {
        responseInspectionParserSkipLogged = true
        streamLogger.info({
          event: 'gateway_response_inspection_parser_skipped'
        }, '网关流式事件过大，兜底拦截停止解析并继续原样转发')
      }
      recordResponseInspectionObservations(interceptResult.observations)
      let latestInspection = inspector.snapshot()
      if (interceptResult.chunks.length === 0 && !interceptResult.intercepted) {
        settleStreamFirstByteDeadlineReadDecision(false)
      }
      if (shouldReturnResponseInspectionBeforeDownstreamWrite(interceptResult.intercepted, res, totalResponseBytes)) {
        settleStreamFirstByteDeadlineReadDecision(true)
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
        settleStreamFirstByteDeadlineReadDecision(true)
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
      let chunkWroteDownstream = false
      for (let outboundIndex = 0; outboundIndex < interceptResult.chunks.length; outboundIndex += 1) {
        const outbound = interceptResult.chunks[outboundIndex]!
        if (!preCommitSseEvidence.dataEventObserved && !downstreamCommit.semanticCommitted) {
          preCommitSseEvidence.push(outbound)
        }
        latestInspection = inspector.pushChunk(outbound, {
          lightweightImageStream: bodyCaptureOmitted || latestInspection.imageOutputReceived
        })
        updateStreamInspectionProgress(latestInspection)
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
        chunkCanEndAfterTerminal = chunkCanEndAfterTerminal || inspector.drainEventSummariesCanEndStream()
        lastSseEventCount = latestInspection.eventCount
        if (latestInspection.skipped) {
          lastSseEventActivityAt = undefined
        } else if (outboundSseEventCount > 0 || latestInspection.pendingEvent) {
          lastSseEventActivityAt = lastUpstreamActivityAt
        }
        if (pendingFirstByteDeadlineReadDecision) {
          const semanticResultInRead = streamSemanticResultReceived(latestInspection)
            || preCommitSseEvidence.dataPayloadStarted
          const lastOutboundInRead = outboundIndex === interceptResult.chunks.length - 1
          if (semanticResultInRead || lastOutboundInRead) {
            settleStreamFirstByteDeadlineReadDecision(semanticResultInRead)
          } else {
            // Keep all transformed fragments from the same raw read private
            // until we know whether that read contains a semantic result.
            if (canKeepPreCommitBuffered(latestInspection, outbound)) {
              appendPreCommitChunk(outbound)
            } else if (shouldRejectOversizedUncommittedSseFraming(latestInspection, outbound)) {
              throw new StreamPreCommitBufferExceededError()
            } else if (shouldKeepNonSemanticSseFramingPrivate()) {
              clearStreamPreCommitChunks(preCommitBuffer)
            } else {
              throw new StreamPreCommitBufferExceededError()
            }
            continue
          }
        }
        if (canKeepPreCommitBuffered(latestInspection, outbound)) {
          appendPreCommitChunk(outbound)
          continue
        }
        if (shouldRejectOversizedUncommittedSseFraming(latestInspection, outbound)) {
          throw new StreamPreCommitBufferExceededError()
        }
        if (shouldKeepNonSemanticSseFramingPrivate()) {
          clearStreamPreCommitChunks(preCommitBuffer)
          continue
        }
        if (interpretedProtocolFailure(latestInspection)) {
          const beforeDownstreamCommit = shouldFailBeforeDownstreamCommit()
          if (beforeDownstreamCommit) discardPreCommitChunks()
          const message = '上游流式响应返回失败终态'
          const errorCode = 'upstream_protocol_failure'
          await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, latestInspection.outputReceived, interpretedProtocolFailure(latestInspection)))
          await closeAsyncIterator(iterator)
          const committedFailureDisposition = beforeDownstreamCommit
            ? undefined
            : await signalCommittedStreamFailure(latestInspection)
          streamLogger.warn({
            event: beforeDownstreamCommit
              ? 'gateway_stream_failure_before_downstream_commit'
              : committedFailureDisposition === 'signaled'
                ? 'gateway_stream_failure_after_downstream_commit_signaled'
                : 'gateway_stream_failure_after_downstream_commit_interrupted',
            message,
            errorCode,
            totalUpstreamBytes,
            totalResponseBytes,
            chunkIndex,
            sseEventCount: latestInspection.eventCount,
            recentSseEventTypes: latestInspection.recentEventTypes
          }, beforeDownstreamCommit
            ? '网关在下游提交前解析到流式失败，交由上层决定是否服务端换号重试'
            : committedFailureDisposition === 'signaled'
              ? '网关在下游提交后解析到流式失败，已丢弃供应商失败原文并补发受控协议失败事件'
              : '网关在下游提交后解析到流式失败，已丢弃供应商失败原文并中断连接')
          return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
        }
        const writeStartedAt = Date.now()
        await flushPreCommitChunks()
        const writeResult = await writeDownstreamChunk(
          outbound,
          streamSemanticResultReceived(latestInspection) || preCommitSseEvidence.dataPayloadStarted
        )
        if (latestInspection.terminalReceived && !interpretedProtocolFailure(latestInspection)) {
          terminalEventWritten = true
        }
        const writeMs = Date.now() - writeStartedAt
        chunkWriteMs += writeMs
        chunkWroteDownstream = true
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
        streamLogger.debug({
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
          semanticResultReceived,
          pendingProtocolEvent,
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
        await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, latestInspection.outputReceived, false))
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
      if (!interceptor && (chunkWroteDownstream || preCommitBuffer.chunks.length > 0) && latestInspection.terminalReceived && !interpretedProtocolFailure(latestInspection) && chunkCanEndAfterTerminal && !pendingProtocolEvent) {
        await flushPreCommitChunks()
        terminalEventWritten = true
        return await finishTerminalSuccess(inspector.finish(), {
          drainForKeepAlive: protocolDriver.drainForKeepAliveAfterTerminal,
          eofPendingFlush: true
        })
      }
    }

    const eofTransformedChunks = options.flushTransformedUpstreamChunks?.() ?? []
    const eofInterceptResult = interceptor
      ? mergeResponseInspectionSseResults(
        pushResponseInspectionChunks(interceptor, eofTransformedChunks),
        interceptor.flushPendingOnEof()
      )
      : passThroughResponseInspectionChunks(eofTransformedChunks)
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
      let eofWroteDownstream = false
      for (const outbound of eofInterceptResult.chunks) {
        if (!preCommitSseEvidence.dataEventObserved && !downstreamCommit.semanticCommitted) {
          preCommitSseEvidence.push(outbound)
        }
        latestInspection = inspector.pushChunk(outbound, {
          lightweightImageStream: bodyCaptureOmitted || latestInspection.imageOutputReceived
        })
        updateStreamInspectionProgress(latestInspection)
        omitBodyCaptureIfImageStream(latestInspection, { eofPendingFlush: true })
        if (latestInspection.skipped && !parserSkipLogged) {
          parserSkipLogged = true
          streamLogger.warn({
            event: 'gateway_stream_inspector_skipped',
            reason: latestInspection.skipReason
          }, '网关流式解析超过上限，已停止解析并继续转发')
        }
        const outboundSseEventCount = latestInspection.eventCount - lastSseEventCount
        eofCanEndAfterTerminal = eofCanEndAfterTerminal || inspector.drainEventSummariesCanEndStream()
        lastSseEventCount = latestInspection.eventCount
        if (latestInspection.skipped) {
          lastSseEventActivityAt = undefined
        } else if (outboundSseEventCount > 0 || latestInspection.pendingEvent) {
          lastSseEventActivityAt = lastUpstreamActivityAt
        }
        if (canKeepPreCommitBuffered(latestInspection, outbound)) {
          appendPreCommitChunk(outbound)
          continue
        }
        if (shouldRejectOversizedUncommittedSseFraming(latestInspection, outbound)) {
          throw new StreamPreCommitBufferExceededError()
        }
        if (shouldKeepNonSemanticSseFramingPrivate()) {
          clearStreamPreCommitChunks(preCommitBuffer)
          continue
        }
        if (interpretedProtocolFailure(latestInspection)) {
          const beforeDownstreamCommit = shouldFailBeforeDownstreamCommit()
          if (beforeDownstreamCommit) discardPreCommitChunks()
          const message = '上游流式响应返回失败终态'
          const errorCode = 'upstream_protocol_failure'
          await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, latestInspection.outputReceived, interpretedProtocolFailure(latestInspection)))
          const committedFailureDisposition = beforeDownstreamCommit
            ? undefined
            : await signalCommittedStreamFailure(latestInspection)
          streamLogger.warn({
            event: beforeDownstreamCommit
              ? 'gateway_stream_failure_before_downstream_commit'
              : committedFailureDisposition === 'signaled'
                ? 'gateway_stream_failure_after_downstream_commit_signaled'
                : 'gateway_stream_failure_after_downstream_commit_interrupted',
            message,
            errorCode,
            totalUpstreamBytes,
            totalResponseBytes,
            chunkIndex,
            sseEventCount: latestInspection.eventCount,
            recentSseEventTypes: latestInspection.recentEventTypes,
            eofPendingFlush: true
          }, beforeDownstreamCommit
            ? '网关在 EOF pending 下游提交前解析到流式失败，交由上层决定是否服务端换号重试'
            : committedFailureDisposition === 'signaled'
              ? '网关在 EOF pending 下游提交后解析到流式失败，已补发受控协议失败事件'
              : '网关在 EOF pending 下游提交后解析到流式失败，已中断当前连接')
          return finishStreamResult(false, message, errorCode, firstTokenMs, latestInspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, latestInspection.outputReceived, latestInspection.estimatedOutputTokens, latestInspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(latestInspection))
        }
        await flushPreCommitChunks()
        const writeResult = await writeDownstreamChunk(
          outbound,
          streamSemanticResultReceived(latestInspection) || preCommitSseEvidence.dataPayloadStarted
        )
        if (latestInspection.terminalReceived && !interpretedProtocolFailure(latestInspection)) {
          terminalEventWritten = true
        }
        eofWroteDownstream = true
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
        await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, latestInspection.outputReceived, false))
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
      if ((eofWroteDownstream || preCommitBuffer.chunks.length > 0) && latestInspection.terminalReceived && !interpretedProtocolFailure(latestInspection) && eofCanEndAfterTerminal && !pendingProtocolEvent) {
        await flushPreCommitChunks()
        terminalEventWritten = true
        return await finishTerminalSuccess(latestInspection, { eofPendingFlush: true })
      }
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    if (error instanceof StreamBeforeDownstreamCommitError) {
      throw error.originalError
    }
    if (isUpstreamRequestAbortedError(error) || signal?.aborted) {
      const inspection = inspector.finish()
      omitBodyCaptureIfImageStream(inspection, { eofPendingFlush: true })
      if (terminalEventWritten && !interpretedProtocolFailure(inspection)) {
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
        }, '客户端在协议终止事件后关闭连接，按成功流式响应收尾')
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
    if (isGatewayResponsePrecommitDeadlineError(error)) {
      const inspection = inspector.finish()
      const message = error.message
      const upstreamResponseCommitted = totalResponseBytes > 0
      streamLogger.warn({
        event: 'gateway_stream_response_precommit_deadline_exceeded',
        elapsedMs: Date.now() - startedAt,
        deadlineAtMs: error.deadlineAtMs,
        chunkCount: chunkIndex,
        totalUpstreamBytes,
        totalResponseBytes,
        upstreamResponseCommitted,
        outputReceived: inspection.outputReceived,
        terminalReceived: inspection.terminalReceived
      }, '网关请求墙钟到达时流式响应仍未产生可提交语义结果')
      if (upstreamResponseCommitted) {
        interruptResponse(res)
      }
      return finishStreamResult(
        false,
        message,
        error.code,
        firstTokenMs,
        inspection.usage,
        responseCapture,
        upstreamCapture,
        diagnosticCapture,
        undefined,
        inspection.outputReceived,
        inspection.estimatedOutputTokens,
        inspection.imageOutputReceived,
        captureSuccessPayloads,
        bodyOmissionFor(inspection)
      )
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
    if (terminalEventWritten && !interpretedProtocolFailure(inspection)) {
      endResponse(res)
      streamLogger.info({
        event: 'gateway_stream_error_ignored_after_terminal',
        elapsedMs: Date.now() - startedAt,
        rawMessage
      }, '网关已收到终止事件，忽略终止后的流式异常')
      return finishStreamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
    }
    const transportFailure = streamTransportFailureForError(error, rawMessage)
    const gatewayLocalFailure = isGatewayLocalStreamFailure(
      error,
      interpretedProtocolFailure(inspection),
      transportFailure
    )
    const message = publicStreamFailureMessage(error, interpretedProtocolFailure(inspection), transportFailure)
    const errorCode = streamClientFailureCode(
      interpretedProtocolFailure(inspection)
        ? 'upstream_protocol_failure'
        : isGatewayFirstByteTimeoutError(error)
          ? 'first_byte_timeout'
          : error instanceof StreamPreCommitBufferExceededError
            ? error.code
            : gatewayStreamFailureCode(message),
      inspection.outputReceived,
      options.clientRetryEnabled === true,
      totalResponseBytes
    )
    if (isGatewayFirstByteTimeoutError(error) && error.source === 'configured_deadline') {
      await closeAsyncIterator(iterator)
    }
    const failureBeforeDownstreamCommit = shouldFailBeforeDownstreamCommit()
    if (
      failureBeforeDownstreamCommit
      && (interpretedProtocolFailure(inspection) || error instanceof StreamPreCommitBufferExceededError)
    ) {
      discardPreCommitChunks()
    }
    await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived, interpretedProtocolFailure(inspection)))
    if (failureBeforeDownstreamCommit) {
      streamLogger.warn({
        event: 'gateway_stream_failure_before_downstream_commit',
        message,
        errorCode,
        totalUpstreamBytes,
        totalResponseBytes
      }, '网关在下游提交前捕获流式失败，交由上层决定是否服务端换号重试')
      return withTransportFailureIfProven(
        withGatewayLocalFailureIfNeeded(
          finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection)),
          gatewayLocalFailure
        ),
        transportFailure
      )
    }
    const committedFailureDisposition = await signalCommittedStreamFailure(inspection)
    streamLogger.warn({
      event: committedFailureDisposition === 'signaled'
        ? 'gateway_stream_failure_after_downstream_commit_signaled'
        : 'gateway_stream_failure_after_downstream_commit_interrupted',
      message,
      errorCode,
      totalUpstreamBytes,
      totalResponseBytes,
      transportFailureKind: transportFailure?.kind
    }, committedFailureDisposition === 'signaled'
      ? '网关已为精确客户端补发一次脱敏失败终态'
      : '网关已中断提交后的失败流')
    return withTransportFailureIfProven(
      withGatewayLocalFailureIfNeeded(
        finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection)),
        gatewayLocalFailure
      ),
      transportFailure
    )
  } finally {
    res.off('close', closeIterator)
  }

  preCommitSseEvidence.finish()
  if (shouldKeepNonSemanticSseFramingPrivate()) {
    clearStreamPreCommitChunks(preCommitBuffer)
  }
  const inspection = inspector.finish()
  omitBodyCaptureIfImageStream(inspection, { eofPendingFlush: true })
  // Generic clients keep opaque upstream SSE semantics: once at least one real
  // SSE data event was observed, a clean transport EOF is sufficient even when
  // the protocol driver does not recognize a provider-specific terminal.
  // Comments/heartbeats are transport-only and must remain pre-commit so an
  // empty or keep-alive-only stream can still be retried by the server.
  // Precise clients use interpretProtocolFailures and still require framing.
  if (!interpretProtocolFailures && completed && preCommitSseEvidence.dataEventObserved) {
    await flushPreCommitChunks()
    endResponse(res)
    return finishStreamResult(true, '已完成', undefined, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
  }
  if (inspection.skipped && preCommitSseEvidence.dataEventObserved) {
    const success = completed && !interpretedProtocolFailure(inspection)
    const message = success ? '已完成' : '上游流式响应返回失败终态'
    const errorCode = success ? undefined : streamClientFailureCode(
      'upstream_protocol_failure',
      inspection.outputReceived,
      options.clientRetryEnabled === true,
      totalResponseBytes
    )
    if (!success) {
      await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived, interpretedProtocolFailure(inspection)))
      await signalCommittedStreamFailure(inspection)
    } else {
      endResponse(res)
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
    const message = '上游流在协议终止事件前结束'
    const errorCode = streamClientFailureCode(
      gatewayStreamFailureCode(message),
      inspection.outputReceived,
      options.clientRetryEnabled === true,
      totalResponseBytes
    )
    await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived, false))
    streamLogger.warn({
      event: 'gateway_stream_missing_terminal',
      elapsedMs: Date.now() - startedAt,
      chunkCount: chunkIndex,
      totalUpstreamBytes,
      totalResponseBytes,
      sseEventCount: inspection.eventCount,
      sseEventTypeCounts: inspection.eventTypeCounts,
      recentSseEventTypes: inspection.recentEventTypes
    }, '上游 EOF 前未收到协议终止事件')
    if (shouldFailBeforeDownstreamCommit()) {
      streamLogger.warn({
        event: 'gateway_stream_missing_terminal_before_downstream_commit',
        errorCode,
        totalUpstreamBytes,
        totalResponseBytes
      }, '网关在下游提交前发现上游缺少终止事件，交由上层决定是否服务端换号重试')
      return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
    }
    const committedFailureDisposition = await signalCommittedStreamFailure(inspection)
    streamLogger.warn({
      event: committedFailureDisposition === 'signaled'
        ? 'gateway_stream_missing_terminal_failure_signaled'
        : 'gateway_stream_missing_terminal_interrupted',
      errorCode,
      totalUpstreamBytes,
      totalResponseBytes,
      sseEventCount: inspection.eventCount,
      recentSseEventTypes: inspection.recentEventTypes
    }, committedFailureDisposition === 'signaled'
      ? '网关已因缺少终止事件补发一次脱敏失败终态'
      : '网关已因缺少终止事件中断连接')
    return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
  }

  if (!completed || interpretedProtocolFailure(inspection)) {
    const message = interpretedProtocolFailure(inspection)
      ? '上游流式响应返回失败终态'
      : '上游流式响应已中断'
    const errorCode = streamClientFailureCode(
      interpretedProtocolFailure(inspection) ? 'upstream_protocol_failure' : gatewayStreamFailureCode(message),
      inspection.outputReceived,
      options.clientRetryEnabled === true,
      totalResponseBytes
    )
    await handleStreamFailure(message, errorCode, streamFailureContext(totalResponseBytes, inspection.outputReceived, interpretedProtocolFailure(inspection)))
    if (shouldFailBeforeDownstreamCommit()) {
      streamLogger.warn({
        event: 'gateway_stream_finished_failed_before_downstream_commit',
        completed,
        errorCode,
        totalUpstreamBytes,
        totalResponseBytes,
        sseEventCount: inspection.eventCount,
        recentSseEventTypes: inspection.recentEventTypes
      }, '网关在 EOF pending 收尾后识别到失败，交由上层决定是否服务端换号重试')
      return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
    }
    const committedFailureDisposition = await signalCommittedStreamFailure(inspection)
    streamLogger.warn({
      event: committedFailureDisposition === 'signaled'
        ? 'gateway_stream_finished_failed_signaled'
        : 'gateway_stream_finished_failed_interrupted',
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
    }, committedFailureDisposition === 'signaled'
      ? '网关已为精确客户端补发一次脱敏失败终态'
      : '网关已中断失败流')
    return finishStreamResult(false, message, errorCode, firstTokenMs, inspection.usage, responseCapture, upstreamCapture, diagnosticCapture, undefined, inspection.outputReceived, inspection.estimatedOutputTokens, inspection.imageOutputReceived, captureSuccessPayloads, bodyOmissionFor(inspection))
  }

  await flushPreCommitChunks()
  endResponse(res)

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

function codexResponsesProtocolDecision(
  result: import('../codex-responses/response-guard.js').CodexResponsesGuardSseResult,
  strict: boolean
): ResponseInspectionDecision {
  const codes = [...new Set(result.issues.map((issue) => issue.code))]
  const detail = codes.length > 0 ? `：${codes.join(', ')}` : ''
  return {
    reason: 'configured_response_policy',
    action: 'replace_with_failure',
    transport: 'sse',
    triggerPhase: result.commit.semanticCommitted ? 'after_downstream_write' : 'before_downstream_write',
    endpointFamily: 'responses',
    frameType: 'raw_json_path',
    upstreamErrorCode: strict ? 'codex_responses_protocol_intercepted' : 'codex_responses_protocol_blocked',
    upstreamErrorMessage: strict
      ? `Codex Responses 流式响应协议异常，严格模式已拦截并请求更换账户${detail}`
      : `Codex Responses 流式响应协议异常，安全模式已阻止本次响应并请求下一账户${detail}`,
    clientProfile: 'codex',
    downstreamWritten: result.commit.semanticCommitted,
    policyId: strict ? 'codex_responses_strict_intercept' : 'codex_responses_protocol_blocked',
    policyName: strict ? 'Codex Responses 严格拦截' : 'Codex Responses 协议阻断',
    policySource: 'account',
    policyProtocolCode: 'openai_v1',
    executionMode: 'intercept',
    dataHandling: 'replace_with_failure',
    retryEnabled: true,
    accountSwitch: 'request_next_account',
    accountState: strict ? 'runtime_avoidance' : 'none'
  }
}

function withTransportFailureIfProven(
  result: StreamPipeResult,
  transportFailure: StreamTransportFailure | undefined
): StreamPipeResult {
  if (!transportFailure) return result
  return {
    ...result,
    transportFailure
  }
}

function withGatewayLocalFailureIfNeeded(
  result: StreamPipeResult,
  gatewayLocalFailure: boolean
): StreamPipeResult {
  return gatewayLocalFailure
    ? { ...result, gatewayLocalFailure: true }
    : result
}

function streamTransportFailureForError(
  error: unknown,
  diagnosticMessage: string
): StreamTransportFailure | undefined {
  if (error instanceof StreamReadPlanTimeoutError) {
    return {
      kind: 'timeout',
      reason: '上游流式响应传输超时'
    }
  }
  if (!isStartedUpstreamBodyTransportError(error)) return undefined
  const kind = streamTransportFailureKind(error, diagnosticMessage)
  return {
    kind,
    reason: kind === 'timeout'
      ? '上游流式响应传输超时'
      : '上游流式响应读取未完成'
  }
}

function streamTransportFailureKind(error: unknown, diagnosticMessage: string): 'timeout' | 'read_incomplete' {
  const diagnostic = [
    error instanceof Error ? error.name : '',
    typeof error === 'object' && error !== null && 'code' in error ? String(error.code) : '',
    diagnosticMessage
  ].join(' ').toLowerCase()
  return /timeout|timedout|timed out|etimedout|超时/.test(diagnostic) ? 'timeout' : 'read_incomplete'
}

function publicStreamFailureMessage(
  error: unknown,
  protocolFailure: boolean,
  transportFailure: StreamTransportFailure | undefined
): string {
  if (protocolFailure) return '上游流式响应返回失败终态'
  if (
    error instanceof StreamReadPlanTimeoutError
    || isGatewayFirstByteTimeoutError(error)
    || error instanceof StreamPreCommitBufferExceededError
  ) {
    return error.message
  }
  if (transportFailure) return transportFailure.reason
  return '网关处理流式响应失败'
}

function isGatewayLocalStreamFailure(
  error: unknown,
  protocolFailure: boolean,
  transportFailure: StreamTransportFailure | undefined
): boolean {
  return !protocolFailure
    && !transportFailure
    && !isGatewayFirstByteTimeoutError(error)
    && !(error instanceof StreamPreCommitBufferExceededError)
}

async function readNextStreamChunk(
  iterator: AsyncIterator<Uint8Array>,
  timeoutProfile: GatewayTimeoutProfile,
  startedAt: number,
  status: {
    waitingForFirstChunk: boolean
    lastUpstreamActivityAt: number
    lastSseEventActivityAt?: number
    upstreamChunkReceived: boolean
    semanticResultReceived: boolean
    pendingProtocolEvent: boolean
    parserSkipped: boolean
    waitingForFirstOutput: boolean
    firstByteDeadlineObserved: boolean
  },
  signal?: AbortSignal,
  options: {
    firstByteDeadlineMs?: number
    responsePrecommitDeadlineAtMs?: number
    onFirstByteDeadline?: FirstByteDeadlineHandler
    onFirstByteDeadlineSuperseded?: () => void
  } = {}
): Promise<StreamChunkReadResult> {
  const pendingRead = observeFirstBytePendingRead(iterator.next())
  let firstByteDeadlineObserved = status.firstByteDeadlineObserved

  while (true) {
    const now = Date.now()
    const firstByteDeadlineMs = options.firstByteDeadlineMs
    const responsePrecommitRemainingMs = !status.semanticResultReceived
      && options.responsePrecommitDeadlineAtMs !== undefined
      ? options.responsePrecommitDeadlineAtMs - now
      : undefined
    if (responsePrecommitRemainingMs !== undefined && responsePrecommitRemainingMs <= 0) {
      throw new GatewayResponsePrecommitDeadlineError(options.responsePrecommitDeadlineAtMs ?? 0)
    }
    const firstByteRemainingMs = status.waitingForFirstOutput
      && !status.parserSkipped
      && !firstByteDeadlineObserved
      && firstByteDeadlineMs !== undefined
      ? startedAt + firstByteDeadlineMs - now
      : undefined
    if (firstByteRemainingMs !== undefined && firstByteRemainingMs <= 0) {
      firstByteDeadlineObserved = true
      const deadlineMs = firstByteDeadlineMs ?? 0
      const decision = await decideFirstByteDeadlineAfterPendingRead(pendingRead, options.onFirstByteDeadline, {
        elapsedMs: Date.now() - startedAt,
        timeoutMs: deadlineMs,
        transport: 'stream'
      }, {
        responsePrecommitDeadlineAtMs: options.responsePrecommitDeadlineAtMs,
        onResponsePrecommitDeadline: options.onFirstByteDeadlineSuperseded
      })
      if (decision.type === 'response_precommit_deadline') throw decision.error
      if (decision.type === 'read') {
        return {
          result: decision.result,
          firstByteDeadlineObserved,
          firstByteDeadlineReadDecision: {
            action: decision.action,
            decisionError: decision.decisionError
          }
        }
      }
      if (decision.action === 'abort') {
        throw new GatewayFirstByteTimeoutError(`上游流式响应 ${Math.ceil(deadlineMs / 1000)}s 后仍未返回首个有效输出`, deadlineMs, 'configured_deadline')
      }
      continue
    }

    const readPlan = buildStreamReadPlan(timeoutProfile, startedAt, status)
    if (readPlan.timeoutMs <= 0) {
      throw streamReadPlanTimeoutError(readPlan)
    }
    const race = await raceStreamReadWithDeadlines(pendingRead.promise, {
      signal,
      softTimeoutMs: firstByteRemainingMs,
      planTimeoutMs: readPlan.timeoutMs,
      responsePrecommitTimeoutMs: responsePrecommitRemainingMs
    })
    if (race.type === 'read') {
      if (
        options.responsePrecommitDeadlineAtMs !== undefined
        && !status.semanticResultReceived
        && (pendingRead.settledAtMs() ?? Date.now()) > options.responsePrecommitDeadlineAtMs
      ) {
        throw new GatewayResponsePrecommitDeadlineError(options.responsePrecommitDeadlineAtMs)
      }
      return { result: race.result, firstByteDeadlineObserved }
    }
    if (race.type === 'abort') {
      throw new UpstreamRequestAbortedError('请求已取消', true)
    }
    if (race.type === 'plan_timeout') {
      throw streamReadPlanTimeoutError(readPlan)
    }
    if (race.type === 'response_precommit_timeout') {
      throw new GatewayResponsePrecommitDeadlineError(options.responsePrecommitDeadlineAtMs ?? 0)
    }

    firstByteDeadlineObserved = true
    const decision = await decideFirstByteDeadlineAfterPendingRead(pendingRead, options.onFirstByteDeadline, {
      elapsedMs: Date.now() - startedAt,
      timeoutMs: options.firstByteDeadlineMs ?? 0,
      transport: 'stream'
    }, {
      responsePrecommitDeadlineAtMs: options.responsePrecommitDeadlineAtMs,
      onResponsePrecommitDeadline: options.onFirstByteDeadlineSuperseded
    })
    if (decision.type === 'response_precommit_deadline') throw decision.error
    if (decision.type === 'read') {
      return {
        result: decision.result,
        firstByteDeadlineObserved,
        firstByteDeadlineReadDecision: {
          action: decision.action,
          decisionError: decision.decisionError
        }
      }
    }
    if (decision.action === 'abort') {
      throw new GatewayFirstByteTimeoutError(`上游流式响应 ${Math.ceil((options.firstByteDeadlineMs ?? 0) / 1000)}s 后仍未返回首个有效输出`, options.firstByteDeadlineMs ?? 0, 'configured_deadline')
    }
  }
}

function streamReadPlanTimeoutError(readPlan: ReturnType<typeof buildStreamReadPlan>): Error {
  return readPlan.timeoutKind === 'stream_lifetime'
    ? new StreamMaxLifetimeExceededError(readPlan.timeoutMessage)
    : new StreamReadPlanTimeoutError(readPlan.timeoutMessage, readPlan.timeoutKind)
}

async function raceStreamReadWithDeadlines(
  pendingRead: Promise<IteratorResult<Uint8Array>>,
  input: {
    signal?: AbortSignal
    softTimeoutMs?: number
    planTimeoutMs: number
    responsePrecommitTimeoutMs?: number
  }
): Promise<
  | { type: 'read'; result: IteratorResult<Uint8Array> }
  | { type: 'soft_timeout' }
  | { type: 'plan_timeout' }
  | { type: 'response_precommit_timeout' }
  | { type: 'abort' }
> {
  let softTimer: NodeJS.Timeout | undefined
  let planTimer: NodeJS.Timeout | undefined
  let responsePrecommitTimer: NodeJS.Timeout | undefined
  let abortListener: (() => void) | undefined
  try {
    const races: Array<Promise<
      | { type: 'read'; result: IteratorResult<Uint8Array> }
      | { type: 'soft_timeout' }
      | { type: 'plan_timeout' }
      | { type: 'response_precommit_timeout' }
      | { type: 'abort' }
    >> = [pendingRead.then((result) => ({ type: 'read' as const, result }))]
    const softTimeoutMs = input.softTimeoutMs
    if (softTimeoutMs !== undefined) {
      races.push(new Promise((resolve) => {
        softTimer = setTimeout(() => resolve({ type: 'soft_timeout' as const }), Math.max(1, softTimeoutMs))
      }))
    }
    const responsePrecommitTimeoutMs = input.responsePrecommitTimeoutMs
    if (responsePrecommitTimeoutMs !== undefined) {
      races.push(new Promise((resolve) => {
        responsePrecommitTimer = setTimeout(() => resolve({ type: 'response_precommit_timeout' as const }), Math.max(1, responsePrecommitTimeoutMs))
      }))
    }
    races.push(new Promise((resolve) => {
      planTimer = setTimeout(() => resolve({ type: 'plan_timeout' as const }), Math.max(1, input.planTimeoutMs))
    }))
    if (input.signal) {
      if (input.signal.aborted) {
        return { type: 'abort' }
      }
      races.push(new Promise((resolve) => {
        abortListener = () => resolve({ type: 'abort' as const })
        input.signal?.addEventListener('abort', abortListener, { once: true })
      }))
    }
    return await Promise.race(races)
  } finally {
    if (softTimer) clearTimeout(softTimer)
    if (responsePrecommitTimer) clearTimeout(responsePrecommitTimer)
    if (planTimer) clearTimeout(planTimer)
    if (input.signal && abortListener) {
      input.signal.removeEventListener('abort', abortListener)
    }
  }
}

function streamOutputReceived(inspection: GatewayStreamInspection): boolean {
  return inspection.outputReceived || inspection.imageOutputReceived
}

function streamSemanticResultReceived(inspection: GatewayStreamInspection): boolean {
  return inspection.outputReceived
    || inspection.imageOutputReceived
    || inspection.terminalReceived
    || inspection.failedReceived
}

async function drainIteratorAfterTerminalForInspection(
  iterator: AsyncIterator<Uint8Array>,
  inspector: GatewayStreamInspector,
  options: { lightweightImageStream?: boolean } = {}
): Promise<GatewayStreamInspection> {
  const deadline = Date.now() + streamTerminalKeepAliveDrainMs
  try {
    while (Date.now() < deadline) {
      const result = await readIteratorNextWithTimeout(iterator, Math.max(1, deadline - Date.now()))
      if (!result) {
        await closeAsyncIterator(iterator)
        return inspector.finish()
      }
      if (result.done) {
        return inspector.finish()
      }
      inspector.pushChunk(bufferFromUint8Array(result.value), {
        lightweightImageStream: options.lightweightImageStream
      })
    }
    await closeAsyncIterator(iterator)
    return inspector.finish()
  } catch {
    await closeAsyncIterator(iterator)
    return inspector.finish()
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

function streamFailureContext(
  downstreamBytesWritten: number,
  outputReceived: boolean,
  protocolFailureEventReceived?: boolean
): StreamFailureContext {
  return {
    downstreamBytesWritten,
    outputReceived,
    protocolFailureEventReceived
  }
}

function pushResponseInspectionChunks(
  interceptor: OpenAIResponseInspectionBuffer,
  chunks: Buffer[]
): ResponseInspectionSseResult {
  let result: ResponseInspectionSseResult = {
    chunks: [],
    pendingEvent: false,
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

function passThroughResponseInspectionChunks(chunks: Buffer[]): ResponseInspectionSseResult {
  return {
    chunks,
    pendingEvent: false,
    parserSkipped: false
  }
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
    pendingEvent: right.pendingEvent ?? left.pendingEvent,
    parserSkipped: left.parserSkipped || right.parserSkipped
  }
}

function interruptResponse(res: Response): void {
  destroyResponseForUpstreamBodyError(res)
}

async function writeGatewayStreamFailureEventWithBackpressure(
  res: Response,
  message: string,
  code?: string,
  protocol: GatewayErrorProtocol = 'openai',
  downstreamProtocol?: OpenAIGatewayDownstreamProtocol
): Promise<Buffer | undefined> {
  const buffer = writeGatewayStreamFailureEvent(res, message, code, protocol, downstreamProtocol)
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
