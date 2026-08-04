import { buildGatewayStreamFailureEvent } from '../../response/responses.js'
import {
  inspectResponseSemanticFrames,
  responseInspectionFailurePayloadForDecision,
  type ResponseInspectionDecision,
  type ResponseInspectionRuntimeContext,
  type RuntimeResponseInspectionPolicy
} from '../../response/inspection.js'
import {
  parseOpenAISseEventText,
  type ParsedOpenAIStreamEvent
} from './stream-events.js'
import {
  extractOpenAISseSemanticFrames,
  type OpenAIResponseEndpointFamily,
  type ResponseEndpointFamily,
  type ResponseSemanticFrame
} from './response-semantics.js'
import {
  codexCompactionContractMismatchFrame,
  countCodexCompactionOutputItemsFromStreamEvent,
  type CodexCompactionContractCounts
} from '../../response/codex-compaction-contract.js'

export interface ResponseInspectionSseResult {
  chunks: Buffer[]
  intercepted?: ResponseInspectionDecision
  observations?: ResponseInspectionDecision[]
  passthroughUpstreamFailure?: boolean
  pendingEvent: boolean
  parserSkipped: boolean
}

export interface OpenAIResponseInspectionBufferOptions {
  clientRetryEnabled?: boolean
  policies?: RuntimeResponseInspectionPolicy[]
  endpointFamily: ResponseEndpointFamily
  context?: ResponseInspectionRuntimeContext
  extractSemanticFrames?: (event: ParsedOpenAIStreamEvent) => ResponseSemanticFrame[]
  buildFailureEvent?: (decision: ResponseInspectionDecision, clientRetryEnabled: boolean) => Buffer | undefined
  transformEvent?: (event: ParsedOpenAIStreamEvent) => ResponseInspectionEventTransform
}

export type ResponseInspectionEventTransform = Buffer | {
  buffer?: Buffer
  parsedEvent?: ParsedOpenAIStreamEvent
  intercepted?: ResponseInspectionDecision
} | undefined

export interface ParsedResponseInspectionSseChunk {
  event: ParsedOpenAIStreamEvent
  dataBytes: number
}

const maxBufferedSseEventBytes = 256 * 1024

export class OpenAIResponseInspectionBuffer {
  private readonly pendingBuffer = new PendingSseEventBuffer()
  private readonly clientRetryEnabled: boolean
  private readonly policies: RuntimeResponseInspectionPolicy[]
  private readonly inspectVisibleOutputTextEvents: boolean
  private readonly endpointFamily: ResponseEndpointFamily
  private readonly context: ResponseInspectionRuntimeContext | undefined
  private readonly extractSemanticFrames: (event: ParsedOpenAIStreamEvent) => ResponseSemanticFrame[]
  private readonly buildFailureEvent: (decision: ResponseInspectionDecision, clientRetryEnabled: boolean) => Buffer | undefined
  private readonly transformEvent: ((event: ParsedOpenAIStreamEvent) => ResponseInspectionEventTransform) | undefined
  private readonly parsedEventByChunk = new WeakMap<Buffer, ParsedResponseInspectionSseChunk>()
  private readonly deferredLeadingNoopChunks: Buffer[] = []
  private readonly deferredCodexCompactionChunks: Buffer[] = []
  private codexCompactionOutputItemCount = 0
  private codexCompactionItemCount = 0
  private codexCompactionTerminalReceived = false
  private parserSkipped = false
  private downstreamWritten = false

  constructor(options: OpenAIResponseInspectionBufferOptions) {
    this.clientRetryEnabled = options.clientRetryEnabled === true
    this.policies = options.policies ?? []
    this.inspectVisibleOutputTextEvents = this.policies.some(policyRequiresVisibleOutputTextInspection)
    this.endpointFamily = options.endpointFamily
    this.context = options.context
    this.extractSemanticFrames = options.extractSemanticFrames ?? ((event) => extractOpenAISseSemanticFrames(event, openAIEndpointFamilyOrUnknown(this.endpointFamily)))
    this.buildFailureEvent = options.buildFailureEvent ?? failureEventForDecision
    this.transformEvent = options.transformEvent
  }

  markDownstreamWrite(): void {
    if (!this.clientRetryEnabled && this.policies.length === 0 && !this.transformEvent) return
    this.downstreamWritten = true
  }

  parsedEventForChunk(chunk: Buffer): ParsedResponseInspectionSseChunk | undefined {
    return this.parsedEventByChunk.get(chunk)
  }

  pushChunk(chunk: Buffer): ResponseInspectionSseResult {
    if (!this.clientRetryEnabled && this.policies.length === 0 && !this.transformEvent) {
      return { chunks: [chunk], pendingEvent: false, parserSkipped: false }
    }
    if (this.parserSkipped) {
      return { chunks: [...this.drainDeferredLeadingNoopChunks(), chunk], pendingEvent: false, parserSkipped: true }
    }

    this.pendingBuffer.push(chunk)
    if (this.pendingBuffer.length > maxBufferedSseEventBytes && !this.shouldInspectCodexCompactionContract()) {
      const buffered = this.pendingBuffer.drain()
      this.parserSkipped = true
      return { chunks: [...this.drainDeferredLeadingNoopChunks(), buffered], pendingEvent: false, parserSkipped: true }
    }

    const chunks: Buffer[] = []
    const observations: ResponseInspectionDecision[] = []
    let semanticOutputBuffered = false

    while (true) {
      const rawBuffer = this.pendingBuffer.shiftEvent()
      if (!rawBuffer) break
      if (this.canPassThroughCommonResponsesTextDeltaBuffer(rawBuffer)) {
        semanticOutputBuffered = true
        chunks.push(...this.drainDeferredLeadingNoopChunks())
        chunks.push(rawBuffer)
        continue
      }
      const event = parseOpenAISseEventText(rawBuffer.toString('utf8'))
      const transformed = normalizeEventTransform(this.transformEvent?.(event), rawBuffer, event)
      const outboundBuffer = transformed.buffer
      this.rememberParsedEvent(outboundBuffer, transformed.parsedEvent)
      if (transformed.intercepted) {
        this.clearDeferredLeadingNoopChunks()
        this.clearDeferredCodexCompactionChunks()
        if (!this.downstreamWritten && !semanticOutputBuffered) chunks.length = 0
        return {
          chunks,
          intercepted: transformed.intercepted,
          observations: observations.length > 0 ? observations : undefined,
          pendingEvent: this.hasPendingProtocolEvent(),
          parserSkipped: this.parserSkipped
        }
      }
      if (!this.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event)) {
        this.deferredLeadingNoopChunks.push(outboundBuffer)
        continue
      }
      if (this.canPassThroughUninspectableVisibleOutputTextEvent(event)) {
        semanticOutputBuffered = true
        chunks.push(...this.drainDeferredLeadingNoopChunks())
        chunks.push(outboundBuffer)
        continue
      }
      const codexCompaction = this.prepareCodexCompactionEvent(event, outboundBuffer)
      if (isCodexResponsesCyberPolicyFailedTerminal(event, this.endpointFamily, this.context)) {
        this.clearDeferredLeadingNoopChunks()
        chunks.push(outboundBuffer)
        return {
          chunks,
          passthroughUpstreamFailure: true,
          observations: observations.length > 0 ? observations : undefined,
          pendingEvent: this.hasPendingProtocolEvent(),
          parserSkipped: this.parserSkipped
        }
      }
      if (codexCompaction.structuralFailure) {
        this.clearDeferredLeadingNoopChunks()
        chunks.push(outboundBuffer)
        continue
      }
      const frames = this.extractSemanticFrames(event)
      if (codexCompaction.contractFrame) {
        frames.push(codexCompaction.contractFrame)
      }
      const visibleOutputBeforeCurrentEvent = semanticOutputBuffered
      const inspection = inspectResponseSemanticFrames({
        frames,
        policies: codexCompaction.defer && !isExactOpenAIErrorTerminal(event)
          ? this.policies.filter((policy) => policy.source !== 'system_default')
          : this.policies,
        downstreamWritten: this.downstreamWritten || visibleOutputBeforeCurrentEvent,
        transport: 'sse',
        context: this.context
      })
      if (inspection.observations) observations.push(...inspection.observations)
      if (inspection.decision) {
        const decision = inspection.decision
        if (decision.action === 'discard_event') {
          this.removeLastDeferredCodexCompactionChunk(outboundBuffer)
          continue
        }
        this.clearDeferredLeadingNoopChunks()
        this.clearDeferredCodexCompactionChunks()
        if (!this.downstreamWritten && !semanticOutputBuffered) {
          chunks.length = 0
        }
        const failureEvent = this.buildFailureEvent(decision, this.clientRetryEnabled)
        if (failureEvent) {
          chunks.push(failureEvent)
        }
        return {
          chunks,
          intercepted: decision,
          observations: observations.length > 0 ? observations : undefined,
          pendingEvent: this.hasPendingProtocolEvent(),
          parserSkipped: this.parserSkipped
        }
      }
      semanticOutputBuffered = semanticOutputBuffered || frames.some((frame) => frame.visibleOutput === true)
      if (codexCompaction.defer) {
        continue
      }
      if (codexCompaction.releaseChunks) {
        chunks.push(...this.drainDeferredLeadingNoopChunks())
        chunks.push(...codexCompaction.releaseChunks)
        continue
      }
      chunks.push(...this.drainDeferredLeadingNoopChunks())
      chunks.push(outboundBuffer)
    }

    return {
      chunks,
      observations: observations.length > 0 ? observations : undefined,
      pendingEvent: this.hasPendingProtocolEvent(),
      parserSkipped: this.parserSkipped
    }
  }

  flushPendingOnEof(): ResponseInspectionSseResult {
    if (!this.clientRetryEnabled && this.policies.length === 0 && !this.transformEvent) {
      return { chunks: [], pendingEvent: false, parserSkipped: false }
    }
    if (!this.parserSkipped && this.pendingBuffer.length === 0 && this.deferredCodexCompactionChunks.length > 0) {
      return this.interceptIncompleteCodexCompactionOnEof()
    }
    if (this.parserSkipped || this.pendingBuffer.length === 0) {
      return {
        chunks: this.parserSkipped ? this.drainDeferredLeadingNoopChunks() : [],
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    const rawBuffer = this.pendingBuffer.drainEnsuringBoundary()
    return this.inspectRawEventBuffer(rawBuffer, true)
  }

  private inspectRawEventBuffer(rawBuffer: Buffer, eofPendingFlush = false): ResponseInspectionSseResult {
    if (this.canPassThroughCommonResponsesTextDeltaBuffer(rawBuffer)) {
      return {
        chunks: [...this.drainDeferredLeadingNoopChunks(), rawBuffer],
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    const event = parseOpenAISseEventText(rawBuffer.toString('utf8'))
    const transformed = normalizeEventTransform(this.transformEvent?.(event), rawBuffer, event)
    const outboundBuffer = transformed.buffer
    this.rememberParsedEvent(outboundBuffer, transformed.parsedEvent)
    if (transformed.intercepted) {
      this.clearDeferredLeadingNoopChunks()
      this.clearDeferredCodexCompactionChunks()
      return {
        chunks: [],
        intercepted: transformed.intercepted,
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    if (!this.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event)) {
      this.deferredLeadingNoopChunks.push(outboundBuffer)
      this.clearDeferredLeadingNoopChunks()
      return { chunks: [], pendingEvent: this.hasPendingProtocolEvent(), parserSkipped: this.parserSkipped }
    }
    if (this.canPassThroughUninspectableVisibleOutputTextEvent(event)) {
      return {
        chunks: [...this.drainDeferredLeadingNoopChunks(), outboundBuffer],
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    const codexCompaction = this.prepareCodexCompactionEvent(event, outboundBuffer)
    if (isCodexResponsesCyberPolicyFailedTerminal(event, this.endpointFamily, this.context)) {
      this.clearDeferredLeadingNoopChunks()
      return {
        chunks: [outboundBuffer],
        passthroughUpstreamFailure: true,
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    if (codexCompaction.structuralFailure) {
      this.clearDeferredLeadingNoopChunks()
      return {
        chunks: [outboundBuffer],
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    const frames = this.extractSemanticFrames(event)
    if (codexCompaction.contractFrame) {
      frames.push(codexCompaction.contractFrame)
    }
    const inspection = inspectResponseSemanticFrames({
      frames,
      policies: codexCompaction.defer && !isExactOpenAIErrorTerminal(event)
        ? this.policies.filter((policy) => policy.source !== 'system_default')
        : this.policies,
      downstreamWritten: this.downstreamWritten,
      transport: 'sse',
      context: this.context
    })
    if (!inspection.decision) {
      if (codexCompaction.defer) {
        if (eofPendingFlush) {
          return this.interceptIncompleteCodexCompactionOnEof(inspection.observations)
        }
        return {
          chunks: [],
          observations: inspection.observations,
          pendingEvent: this.hasPendingProtocolEvent(),
          parserSkipped: this.parserSkipped
        }
      }
      if (codexCompaction.releaseChunks) {
        return {
          chunks: [...this.drainDeferredLeadingNoopChunks(), ...codexCompaction.releaseChunks],
          observations: inspection.observations,
          pendingEvent: this.hasPendingProtocolEvent(),
          parserSkipped: this.parserSkipped
        }
      }
      return {
        chunks: [...this.drainDeferredLeadingNoopChunks(), outboundBuffer],
        observations: inspection.observations,
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    const decision = inspection.decision
    if (decision.action === 'discard_event') {
      this.removeLastDeferredCodexCompactionChunk(outboundBuffer)
      return {
        chunks: [],
        observations: inspection.observations,
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    this.clearDeferredLeadingNoopChunks()
    this.clearDeferredCodexCompactionChunks()
    const failureEvent = this.buildFailureEvent(decision, this.clientRetryEnabled)
    return {
      chunks: failureEvent ? [failureEvent] : [],
      intercepted: decision,
      observations: inspection.observations,
      pendingEvent: this.hasPendingProtocolEvent(),
      parserSkipped: this.parserSkipped
    }
  }

  private drainDeferredLeadingNoopChunks(): Buffer[] {
    if (this.deferredLeadingNoopChunks.length === 0) return []
    const chunks = [...this.deferredLeadingNoopChunks]
    this.deferredLeadingNoopChunks.length = 0
    return chunks
  }

  private clearDeferredLeadingNoopChunks(): void {
    this.deferredLeadingNoopChunks.length = 0
  }

  private interceptIncompleteCodexCompactionOnEof(observations: ResponseInspectionDecision[] | undefined = undefined): ResponseInspectionSseResult {
    const contractFrame = codexCompactionContractMismatchFrame({
      outputItemCount: this.codexCompactionOutputItemCount,
      compactionItemCount: this.codexCompactionItemCount,
      transport: 'sse',
      eventType: 'eof',
      force: true,
      message: 'Codex Remote Compaction V2 流式响应在 EOF 前未收到 response.completed，已按不可接受响应处理'
    })!
    const inspection = inspectResponseSemanticFrames({
      frames: [contractFrame],
      policies: this.policies,
      downstreamWritten: this.downstreamWritten,
      transport: 'sse',
      context: this.context
    })
    const combinedObservations = [
      ...(observations ?? []),
      ...(inspection.observations ?? [])
    ]
    if (!inspection.decision) {
      return {
        chunks: [...this.drainDeferredLeadingNoopChunks(), ...this.drainDeferredCodexCompactionChunks()],
        observations: combinedObservations.length > 0 ? combinedObservations : undefined,
        pendingEvent: this.hasPendingProtocolEvent(),
        parserSkipped: this.parserSkipped
      }
    }
    const decision = inspection.decision
    this.clearDeferredLeadingNoopChunks()
    this.clearDeferredCodexCompactionChunks()
    const failureEvent = this.buildFailureEvent(decision, this.clientRetryEnabled)
    return {
      chunks: failureEvent ? [failureEvent] : [],
      intercepted: decision,
      observations: combinedObservations.length > 0 ? combinedObservations : undefined,
      pendingEvent: this.hasPendingProtocolEvent(),
      parserSkipped: this.parserSkipped
    }
  }

  private hasPendingProtocolEvent(): boolean {
    return this.pendingBuffer.length > 0 || this.deferredCodexCompactionChunks.length > 0
  }

  private prepareCodexCompactionEvent(
    event: ParsedOpenAIStreamEvent,
    rawBuffer: Buffer
  ): { defer?: boolean; releaseChunks?: Buffer[]; contractFrame?: ResponseSemanticFrame; structuralFailure?: boolean } {
    if (!this.shouldInspectCodexCompactionContract()) {
      return {}
    }
    if (this.codexCompactionTerminalReceived) {
      return {}
    }
    const eventType = event.eventType || event.eventName
    if (isExactCodexCompactionFailedTerminal(event)) {
      // The stream pipe owns structural failure handling. Do not turn this
      // protocol terminal into a local compaction-contract mismatch.
      this.codexCompactionTerminalReceived = true
      this.clearDeferredCodexCompactionChunks()
      return { structuralFailure: true }
    }
    if (eventType === 'response.incomplete') {
      // Incomplete is a semantic terminal handled by the normal Codex default
      // rule. It must not remain deferred until EOF and become a compact
      // contract mismatch with different account-switch semantics.
      this.codexCompactionTerminalReceived = true
      this.clearDeferredCodexCompactionChunks()
      return {}
    }
    const eventCounts = countCodexCompactionOutputItemsFromStreamEvent(event)
    if (eventCounts) {
      this.rememberCodexCompactionCounts(eventCounts)
    }

    const terminal = eventType === 'response.completed'
    if (!terminal) {
      const deferred = this.deferCodexCompactionChunk(rawBuffer)
      return deferred ?? { defer: true }
    }

    this.codexCompactionTerminalReceived = true
    const response = objectValue(event.data?.response)
    if (typeof response?.id !== 'string') {
      this.clearDeferredCodexCompactionChunks()
      return {
        contractFrame: codexCompactionContractMismatchFrame({
          outputItemCount: this.codexCompactionOutputItemCount,
          compactionItemCount: this.codexCompactionItemCount,
          transport: 'sse',
          eventType,
          force: true,
          message: 'Codex Remote Compaction V2 response.completed 缺少 response.id 字符串，Codex 客户端无法解析'
        })!
      }
    }
    const mismatchFrame = codexCompactionContractMismatchFrame({
      outputItemCount: this.codexCompactionOutputItemCount,
      compactionItemCount: this.codexCompactionItemCount,
      transport: 'sse',
      eventType
    })
    if (mismatchFrame) {
      this.clearDeferredCodexCompactionChunks()
      return { contractFrame: mismatchFrame }
    }
    return {
      releaseChunks: [...this.drainDeferredCodexCompactionChunks(), rawBuffer]
    }
  }

  private rememberCodexCompactionCounts(counts: CodexCompactionContractCounts): void {
    this.codexCompactionOutputItemCount += counts.outputItemCount
    this.codexCompactionItemCount += counts.compactionItemCount
  }

  private deferCodexCompactionChunk(rawBuffer: Buffer): { contractFrame: ResponseSemanticFrame } | undefined {
    this.deferredCodexCompactionChunks.push(rawBuffer)
    return undefined
  }

  private drainDeferredCodexCompactionChunks(): Buffer[] {
    if (this.deferredCodexCompactionChunks.length === 0) return []
    const chunks = [...this.deferredCodexCompactionChunks]
    this.clearDeferredCodexCompactionChunks()
    return chunks
  }

  private clearDeferredCodexCompactionChunks(): void {
    this.deferredCodexCompactionChunks.length = 0
  }

  private removeLastDeferredCodexCompactionChunk(rawBuffer: Buffer): void {
    const last = this.deferredCodexCompactionChunks[this.deferredCodexCompactionChunks.length - 1]
    if (!last || last !== rawBuffer) return
    this.deferredCodexCompactionChunks.pop()
  }

  private canPassThroughUninspectableVisibleOutputTextEvent(event: ParsedOpenAIStreamEvent): boolean {
    if (this.shouldInspectCodexCompactionContract()) return false
    if (this.inspectVisibleOutputTextEvents) return false
    if (!event.data || event.dataParseError) return false
    if (!isSafeVisibleOutputOnlyRoot(event.data)) return false
    const eventType = event.eventType || event.eventName
    if (eventType === 'response.output_text.delta' || eventType === 'response.output_text.done') {
      return true
    }
    if (eventType !== 'message' && event.eventName !== '') {
      return false
    }
    const choices = Array.isArray(event.data.choices) ? event.data.choices : []
    return choices.length > 0 && choices.every(isVisibleOutputOnlyChatCompletionChoice)
  }

  private canPassThroughCommonResponsesTextDeltaBuffer(rawBuffer: Buffer): boolean {
    if (this.shouldInspectCodexCompactionContract()) return false
    if (this.inspectVisibleOutputTextEvents) return false
    return isCommonResponsesTextDeltaSseBuffer(rawBuffer)
  }

  private shouldInspectCodexCompactionContract(): boolean {
    return this.endpointFamily === 'responses'
      && this.context?.codexCompactionExpected === true
      && this.context.clientProfile === 'codex'
      && this.context.accountClientCompatibility === 'codex_responses'
  }

  private rememberParsedEvent(buffer: Buffer, event: ParsedOpenAIStreamEvent | undefined): void {
    if (!event || event.dataText === '' || buffer.length > maxBufferedSseEventBytes) return
    const dataBytes = event.dataBytes ?? Buffer.byteLength(event.dataText, 'utf8')
    if (dataBytes > maxBufferedSseEventBytes || (event.rawText && /\r(?!\n)/.test(event.rawText))) return
    this.parsedEventByChunk.set(buffer, {
      event,
      dataBytes
    })
  }
}

function isExactCodexCompactionFailedTerminal(event: ParsedOpenAIStreamEvent): boolean {
  return event.eventType === 'response.failed' || event.eventName === 'response.failed'
}

function isExactOpenAIErrorTerminal(event: ParsedOpenAIStreamEvent): boolean {
  return event.eventType === 'error' || event.eventName === 'error'
}

function isCodexResponsesCyberPolicyFailedTerminal(
  event: ParsedOpenAIStreamEvent,
  endpointFamily: ResponseEndpointFamily,
  context: ResponseInspectionRuntimeContext | undefined
): boolean {
  if (
    endpointFamily !== 'responses'
    || context?.clientProfile !== 'codex'
    || !isExactCodexCompactionFailedTerminal(event)
  ) return false
  const response = objectValue(event.data?.response)
  const error = objectValue(response?.error)
  return response?.status === 'failed'
    && error?.code === 'cyber_policy'
}

function normalizeEventTransform(
  value: ResponseInspectionEventTransform,
  fallback: Buffer,
  fallbackEvent: ParsedOpenAIStreamEvent
): { buffer: Buffer; parsedEvent?: ParsedOpenAIStreamEvent; intercepted?: ResponseInspectionDecision } {
  if (Buffer.isBuffer(value)) return { buffer: value }
  return {
    buffer: value?.buffer ?? fallback,
    parsedEvent: value?.parsedEvent ?? (value?.buffer ? undefined : fallbackEvent),
    intercepted: value?.intercepted
  }
}

function openAIEndpointFamilyOrUnknown(endpointFamily: ResponseEndpointFamily): OpenAIResponseEndpointFamily {
  return endpointFamily === 'chat_completions' || endpointFamily === 'responses'
    ? endpointFamily
    : 'unknown'
}


function failureEventForDecision(decision: ResponseInspectionDecision, clientRetryEnabled: boolean): Buffer | undefined {
  if (decision.action === 'discard_event' || decision.action === 'dry_run') return undefined
  const { errorCode, message } = responseInspectionFailurePayloadForDecision(decision, clientRetryEnabled)
  return buildGatewayStreamFailureEvent(message, errorCode)
}


function isDeferrableLeadingChatCompletionNoopEvent(event: ParsedOpenAIStreamEvent): boolean {
  const data = event.data
  if (!data || data.object !== 'chat.completion.chunk') return false
  if (data.error !== undefined || data.usage !== undefined) return false
  const choices = Array.isArray(data.choices) ? data.choices : []
  if (choices.length === 0) return false
  return choices.every(isNoopChatCompletionChoice)
}

function isNoopChatCompletionChoice(value: unknown): boolean {
  const choice = objectValue(value)
  if (!choice) return false
  if (choice.finish_reason !== undefined && choice.finish_reason !== null) return false
  if (typeof choice.text === 'string' && choice.text.length > 0) return false
  if (choice.message !== undefined) return false
  const delta = objectValue(choice.delta)
  if (!delta) return false
  for (const key of Object.keys(delta)) {
    const value = delta[key]
    if (key === 'role' && typeof value === 'string') continue
    if (key === 'content' && (value === '' || value === null || value === undefined)) continue
    return false
  }
  return true
}

function isSafeVisibleOutputOnlyRoot(data: Record<string, unknown>): boolean {
  if (data.error !== undefined || data.response !== undefined || data.usage !== undefined) return false
  if (data.status !== undefined || data.finish_reason !== undefined) return false
  if (data.code !== undefined || data.message !== undefined) return false
  return true
}

function isVisibleOutputOnlyChatCompletionChoice(value: unknown): boolean {
  const choice = objectValue(value)
  if (!choice) return false
  if (choice.finish_reason !== undefined && choice.finish_reason !== null) return false
  if (choice.error !== undefined || choice.message !== undefined) return false
  if (typeof choice.text === 'string') return true
  const delta = objectValue(choice.delta)
  if (!delta) return false
  for (const key of Object.keys(delta)) {
    const value = delta[key]
    if (key === 'content' && typeof value === 'string') continue
    if (key === 'refusal' && typeof value === 'string') continue
    if (key === 'role' && typeof value === 'string') continue
    return false
  }
  return Object.prototype.hasOwnProperty.call(delta, 'content') || Object.prototype.hasOwnProperty.call(delta, 'refusal')
}

function policyRequiresVisibleOutputTextInspection(policy: RuntimeResponseInspectionPolicy): boolean {
  const match = policy.match
  return Boolean(
    match.outputTextIncludes?.length
    || match.outputTextExcludes?.length
    || match.rawTextIncludes?.length
    || match.jsonPathsExists?.some((path) => !isFastPathSafeErrorJsonPath(path))
  )
}

function isFastPathSafeErrorJsonPath(path: string): boolean {
  const normalized = path.split('.').map((part) => part.trim()).filter(Boolean).join('.')
  return normalized === 'error' || normalized === 'response.error'
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

class PendingSseEventBuffer {
  private chunks: Buffer[] = []
  private headIndex = 0
  private size = 0
  private nextBoundaryEndIndex: number | undefined

  get length(): number {
    return this.size
  }

  push(chunk: Buffer): void {
    if (chunk.length === 0) return
    const previousSize = this.size
    const previousTail = this.tail(3)
    this.chunks.push(chunk)
    this.size += chunk.length
    if (this.nextBoundaryEndIndex === undefined) {
      this.nextBoundaryEndIndex = findBoundaryEndAfterAppend(previousSize, previousTail, chunk)
    }
  }

  shiftEvent(): Buffer | undefined {
    if (this.nextBoundaryEndIndex === undefined) return undefined
    const event = this.consumePrefix(this.nextBoundaryEndIndex)
    this.nextBoundaryEndIndex = this.findBoundaryEndFromStart()
    return event
  }

  drain(): Buffer {
    const buffered = this.consumePrefix(this.size)
    this.nextBoundaryEndIndex = undefined
    return buffered
  }

  drainEnsuringBoundary(): Buffer {
    if (this.size === 0) return Buffer.alloc(0)
    const hasBoundary = this.endsWithBoundary()
    const drained = this.drain()
    return hasBoundary
      ? drained
      : Buffer.concat([drained, sseEventBoundarySuffix], drained.length + sseEventBoundarySuffix.length)
  }

  private consumePrefix(length: number): Buffer {
    if (length <= 0 || this.size === 0) return Buffer.alloc(0)
    const boundedLength = Math.min(length, this.size)
    const first = this.chunks[this.headIndex]
    if (first && boundedLength < first.length) {
      const output = first.subarray(0, boundedLength)
      this.chunks[this.headIndex] = first.subarray(boundedLength)
      this.size -= boundedLength
      return output
    }
    if (first && boundedLength === first.length) {
      this.headIndex += 1
      this.size -= boundedLength
      this.compactConsumedChunks()
      return first
    }

    const parts: Buffer[] = []
    let remaining = boundedLength
    while (remaining > 0) {
      const current = this.chunks[this.headIndex]
      if (!current) break
      if (current.length <= remaining) {
        parts.push(current)
        remaining -= current.length
        this.headIndex += 1
      } else {
        parts.push(current.subarray(0, remaining))
        this.chunks[this.headIndex] = current.subarray(remaining)
        remaining = 0
      }
    }

    this.size -= boundedLength - remaining
    this.compactConsumedChunks()
    return parts.length === 1 ? parts[0] : Buffer.concat(parts, boundedLength - remaining)
  }

  private findBoundaryEndFromStart(): number | undefined {
    let offset = 0
    let tail: Buffer = Buffer.alloc(0)
    for (let index = this.headIndex; index < this.chunks.length; index += 1) {
      const chunk = this.chunks[index]
      const boundary = findBoundaryEndInChunk(offset, tail, chunk)
      if (boundary !== undefined) return boundary
      offset += chunk.length
      tail = trailingBytes(tail, chunk, 3)
    }
    return undefined
  }

  private tail(length: number): Buffer {
    if (length <= 0 || this.size === 0) return Buffer.alloc(0)
    const parts: Buffer[] = []
    const targetLength = Math.min(length, this.size)
    let remaining = targetLength
    for (let index = this.chunks.length - 1; index >= this.headIndex && remaining > 0; index -= 1) {
      const chunk = this.chunks[index]
      const partLength = Math.min(chunk.length, remaining)
      parts.unshift(chunk.subarray(chunk.length - partLength))
      remaining -= partLength
    }
    return parts.length === 1 ? parts[0] : Buffer.concat(parts, targetLength - remaining)
  }

  private endsWithBoundary(): boolean {
    const suffix = this.tail(4)
    return bufferEndsWith(suffix, crlfcrlfBoundary)
      || bufferEndsWith(suffix, lflfBoundary)
      || bufferEndsWith(suffix, crcrBoundary)
  }

  private compactConsumedChunks(): void {
    if (this.headIndex === 0) return
    if (this.headIndex >= this.chunks.length) {
      this.chunks = []
      this.headIndex = 0
      return
    }
    if (this.headIndex > 64 && this.headIndex * 2 > this.chunks.length) {
      this.chunks = this.chunks.slice(this.headIndex)
      this.headIndex = 0
    }
  }
}

const crlfcrlfBoundary = Buffer.from('\r\n\r\n', 'utf8')
const lflfBoundary = Buffer.from('\n\n', 'utf8')
const crcrBoundary = Buffer.from('\r\r', 'utf8')
const sseEventBoundarySuffix = lflfBoundary
const commonResponsesTextDeltaSsePrefix = Buffer.from('event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"', 'utf8')
const commonResponsesTextDeltaSseSuffix = Buffer.from('"}\n\n', 'utf8')
const commonResponsesTextDeltaSseCrLfSuffix = Buffer.from('"}\r\n\r\n', 'utf8')
const commonResponsesTextDeltaSseCrSuffix = Buffer.from('"}\r\r', 'utf8')

function isCommonResponsesTextDeltaSseBuffer(buffer: Buffer): boolean {
  if (!startsWithBuffer(buffer, commonResponsesTextDeltaSsePrefix)) {
    return false
  }
  const suffixLength = commonResponsesTextDeltaSseSuffixLength(buffer)
  if (suffixLength === 0) {
    return false
  }
  const contentStart = commonResponsesTextDeltaSsePrefix.length
  const contentEnd = buffer.length - suffixLength
  for (let index = contentStart; index < contentEnd; index += 1) {
    const code = buffer[index]
    if (code === jsonQuoteByte || code === jsonBackslashByte || code < jsonSpaceByte) {
      return false
    }
  }
  return true
}

function commonResponsesTextDeltaSseSuffixLength(buffer: Buffer): number {
  if (bufferEndsWith(buffer, commonResponsesTextDeltaSseSuffix)) return commonResponsesTextDeltaSseSuffix.length
  if (bufferEndsWith(buffer, commonResponsesTextDeltaSseCrLfSuffix)) return commonResponsesTextDeltaSseCrLfSuffix.length
  if (bufferEndsWith(buffer, commonResponsesTextDeltaSseCrSuffix)) return commonResponsesTextDeltaSseCrSuffix.length
  return 0
}

function startsWithBuffer(buffer: Buffer, prefix: Buffer): boolean {
  return buffer.length >= prefix.length && buffer.subarray(0, prefix.length).equals(prefix)
}

const jsonQuoteByte = 34
const jsonBackslashByte = 92
const jsonSpaceByte = 32

function findBoundaryEndAfterAppend(previousSize: number, previousTail: Buffer, chunk: Buffer): number | undefined {
  return findBoundaryEndInChunk(previousSize, previousTail, chunk)
}

function findBoundaryEndInChunk(chunkOffset: number, previousTail: Buffer, chunk: Buffer): number | undefined {
  const crossBoundary = findCrossChunkBoundaryEnd(chunkOffset, previousTail, chunk)
  const inChunkBoundary = findSseEventBoundary(chunk)
  const inChunkBoundaryEnd = inChunkBoundary ? chunkOffset + inChunkBoundary.endIndex : undefined
  if (crossBoundary === undefined) return inChunkBoundaryEnd
  if (inChunkBoundaryEnd === undefined) return crossBoundary
  return Math.min(crossBoundary, inChunkBoundaryEnd)
}

function findCrossChunkBoundaryEnd(chunkOffset: number, previousTail: Buffer, chunk: Buffer): number | undefined {
  if (previousTail.length === 0 || chunk.length === 0) return undefined
  const prefix = chunk.subarray(0, Math.min(3, chunk.length))
  const combined = Buffer.concat([previousTail, prefix], previousTail.length + prefix.length)
  const boundary = findSseEventBoundary(combined)
  if (!boundary || boundary.index >= previousTail.length || boundary.endIndex <= previousTail.length) return undefined
  return chunkOffset - previousTail.length + boundary.endIndex
}

function findSseEventBoundary(buffer: Buffer): { index: number; endIndex: number } | undefined {
  const first = earliestBoundaryCandidate(
    boundaryCandidate(buffer, sseCrLfBoundary),
    boundaryCandidate(buffer, sseLfBoundary),
    boundaryCandidate(buffer, sseCrBoundary)
  )
  if (!first) return undefined
  return { index: first.index, endIndex: first.index + first.length }
}

function earliestBoundaryCandidate(
  ...candidates: Array<{ index: number; length: number } | undefined>
): { index: number; length: number } | undefined {
  let first: { index: number; length: number } | undefined
  for (const candidate of candidates) {
    if (!candidate) continue
    if (!first || candidate.index < first.index || (candidate.index === first.index && candidate.length < first.length)) {
      first = candidate
    }
  }
  return first
}

function boundaryCandidate(buffer: Buffer, tokenBuffer: Buffer): { index: number; length: number } | undefined {
  const index = buffer.indexOf(tokenBuffer)
  return index >= 0 ? { index, length: tokenBuffer.length } : undefined
}

function trailingBytes(previousTail: Buffer, chunk: Buffer, length: number): Buffer {
  if (chunk.length >= length) return chunk.subarray(chunk.length - length)
  const combinedLength = Math.min(length, previousTail.length + chunk.length)
  return Buffer.concat([previousTail, chunk], previousTail.length + chunk.length).subarray(previousTail.length + chunk.length - combinedLength)
}

function bufferEndsWith(buffer: Buffer, suffix: Buffer): boolean {
  return buffer.length >= suffix.length && buffer.subarray(buffer.length - suffix.length).equals(suffix)
}

const sseCrLfBoundary = Buffer.from('\r\n\r\n', 'utf8')
const sseLfBoundary = Buffer.from('\n\n', 'utf8')
const sseCrBoundary = Buffer.from('\r\r', 'utf8')
