import type { Request } from 'express'

import {
  emptyUsage,
  hasAnyUsageValue,
  mergeUsage,
  type ParsedUsage
} from '../../usage/types.js'
import {
  parseOpenAIUsageFromJsonTextFragment
} from './usage.js'
import {
  classifyOpenAIStreamEvent,
  estimateOpenAIRequestInputTokens,
  isOpenAIImageStreamEventType,
  parseOpenAIStreamEventData,
  type OpenAIStreamEventClassification
} from './stream-events.js'

export interface OpenAIStreamInspection {
  terminalReceived: boolean
  failedReceived: boolean
  outputReceived: boolean
  imageOutputReceived: boolean
  outputEventCount: number
  estimatedOutputTokens?: number
  eventCount: number
  eventTypeCounts: Record<string, number>
  lastEventType?: string
  recentEventTypes: string[]
  skipped: boolean
  skipReason?: string
  errorCode?: string
  errorMessage?: string
  usage: ParsedUsage
}

export interface OpenAIStreamEventSummary {
  type: string
  dataBytes: number
  terminal: boolean
  canEndStream: boolean
  failed: boolean
  output: boolean
  usage: boolean
  parseError?: boolean
}

export interface OpenAIStreamUsageFallbackResult {
  usage: ParsedUsage
  estimated: boolean
  estimatedInputTokens?: number
  estimatedOutputTokens?: number
}

export function parseOpenAIUsageFromSseText(text: string): ParsedUsage {
  return inspectOpenAIStreamText(text).usage
}

export function applyOpenAIStreamUsageFallback(
  req: Request,
  usage: ParsedUsage,
  input: {
    outputReceived: boolean
    estimatedOutputTokens?: number
  }
): OpenAIStreamUsageFallbackResult {
  if (!input.outputReceived || (usage.inputTokens !== undefined && usage.outputTokens !== undefined)) {
    return { usage, estimated: false }
  }

  const nextUsage: ParsedUsage = { ...usage }
  let estimated = false
  let estimatedInputTokens: number | undefined
  let estimatedOutputTokens: number | undefined

  if (nextUsage.inputTokens === undefined) {
    const inputTokens = estimateOpenAIRequestInputTokens(req)
    if (inputTokens !== undefined) {
      nextUsage.inputTokens = inputTokens
      estimatedInputTokens = inputTokens
      estimated = true
    }
  }

  if (nextUsage.outputTokens === undefined) {
    const outputTokens = Math.max(1, input.estimatedOutputTokens ?? 0)
    nextUsage.outputTokens = outputTokens
    estimatedOutputTokens = outputTokens
    estimated = true
  }

  return {
    usage: nextUsage,
    estimated,
    estimatedInputTokens,
    estimatedOutputTokens
  }
}

export function inspectOpenAIStreamText(text: string): OpenAIStreamInspection {
  const inspector = new OpenAIStreamInspector()
  inspector.pushText(text)
  return inspector.finish()
}

export class OpenAIStreamInspector {
  private inspection: OpenAIStreamInspection = {
    terminalReceived: false,
    failedReceived: false,
    outputReceived: false,
    imageOutputReceived: false,
    outputEventCount: 0,
    eventCount: 0,
    eventTypeCounts: {},
    recentEventTypes: [],
    skipped: false,
    usage: emptyUsage()
  }
  private eventName = ''
  private dataLines: string[] = []
  private dataBytes = 0
  private pendingLine = ''
  private pendingLineBytes = 0
  private pendingLineExceeded = false
  private pendingLineTail = ''
  private oversizedEvent = false
  private oversizedEventType: string | undefined
  private oversizedEventImageOutput = false
  private oversizedEventUsage: ParsedUsage = emptyUsage()
  private lightweightFragmentTail = ''
  private lightweightTerminalPending = false
  private pendingEventSummaries: OpenAIStreamEventSummary[] = []

  pushChunk(
    chunk: Buffer | Uint8Array | string,
    options: { lightweightImageStream?: boolean } = {}
  ): OpenAIStreamInspection {
    if (typeof chunk !== 'string') {
      const buffer = bufferFromChunk(chunk)
      if (options.lightweightImageStream === true && buffer.length > 0) {
        return this.pushImageStreamChunkLightweight(buffer)
      }
      if (
        buffer.length > streamInspectorImageLightweightChunkBytes
        && hasOpenAIImageStreamPayloadHintBytes(buffer)
      ) {
        return this.pushImageStreamChunkLightweight(buffer)
      }
      const text = buffer.toString('utf8')
      return this.pushText(text)
    }
    const text = chunk
    return this.pushText(text)
  }

  pushText(text: string): OpenAIStreamInspection {
    if (this.inspection.skipped) {
      return this.snapshot()
    }
    let offset = 0
    while (offset < text.length) {
      const newlineIndex = text.indexOf('\n', offset)
      const segmentEnd = newlineIndex < 0 ? text.length : newlineIndex
      this.appendPendingLineSegment(text.slice(offset, segmentEnd))
      if (newlineIndex < 0) break
      this.flushPendingLine()
      offset = newlineIndex + 1
    }
    return this.snapshot()
  }

  finish(): OpenAIStreamInspection {
    if (this.inspection.skipped) {
      return this.snapshot()
    }
    if (this.pendingLine.length > 0 || this.pendingLineExceeded) {
      this.flushPendingLine()
    }
    if (this.inspection.skipped) {
      return this.snapshot()
    }
    this.flushEvent()
    if (this.lightweightTerminalPending) {
      this.inspection.terminalReceived = true
      this.lightweightTerminalPending = false
      this.recordEventSummary({
        type: 'image_stream_eof',
        dataBytes: 0,
        terminal: true,
        canEndStream: true,
        failed: false,
        output: false,
        usage: false,
        parseError: true
      })
    }
    return this.snapshot()
  }

  snapshot(): OpenAIStreamInspection {
    return {
      ...this.inspection,
      eventTypeCounts: { ...this.inspection.eventTypeCounts },
      recentEventTypes: [...this.inspection.recentEventTypes],
      usage: { ...this.inspection.usage }
    }
  }

  drainEventSummaries(): OpenAIStreamEventSummary[] {
    const summaries = this.pendingEventSummaries
    this.pendingEventSummaries = []
    return summaries.map((event) => ({ ...event }))
  }

  private processLine(line: string): void {
    if (line === '') {
      this.flushEvent()
      return
    }
    if (line.startsWith('event:')) {
      this.eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      const dataLine = line.slice(5).trimStart()
      this.dataBytes += Buffer.byteLength(dataLine, 'utf8')
      if (this.oversizedEvent) {
        this.rememberOversizedEventType(dataLine)
        this.rememberOversizedEventUsage(dataLine)
        return
      }
      this.rememberOversizedEventType(dataLine)
      if (this.dataBytes > streamInspectorMaxEventBytes) {
        this.markOversizedEvent(dataLine)
        return
      }
      this.dataLines.push(dataLine)
    }
  }

  private flushEvent(): void {
    if (this.oversizedEvent) {
      this.flushOversizedEvent()
      return
    }
    if (this.dataLines.length === 0) {
      this.eventName = ''
      this.dataBytes = 0
      return
    }
    const currentEventName = this.eventName
    const currentDataBytes = this.dataBytes
    const data = this.dataLines.join('\n').trim()
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
    if (!data) return
    const event = parseOpenAIStreamEventData(data, currentEventName)
    if (event.dataParseError) {
      this.recordEventSummary({
        type: currentEventName || 'message',
        dataBytes: currentDataBytes,
        terminal: false,
        canEndStream: false,
        failed: false,
        output: false,
        usage: false,
        parseError: true
      })
      this.resetOversizedEventState()
      return
    }

    const classification = classifyOpenAIStreamEvent(event, this.inspection.estimatedOutputTokens ?? 0)
    if (classification.visibleOutput) {
      this.inspection.outputReceived = true
      this.inspection.outputEventCount += 1
    }
    if (classification.imageOutput) {
      this.inspection.imageOutputReceived = true
    }
    if (classification.estimatedOutputTokens > 0) {
      this.inspection.estimatedOutputTokens = (this.inspection.estimatedOutputTokens ?? 0) + classification.estimatedOutputTokens
    }
    if (classification.terminal) {
      this.inspection.terminalReceived = true
    }
    if (classification.failed) {
      this.inspection.failedReceived = true
      this.inspection.errorCode = classification.errorCode ?? this.inspection.errorCode
      this.inspection.errorMessage = classification.errorMessage ?? this.inspection.errorMessage
    }
    if (classification.usageFound) {
      this.inspection.usage = mergeUsage(this.inspection.usage, classification.usage)
    }
    this.recordEventSummary({
      type: classification.eventType || currentEventName || 'message',
      dataBytes: currentDataBytes,
      terminal: classification.terminal,
      canEndStream: classification.terminal,
      failed: classification.failed,
      output: classification.visibleOutput,
      usage: classification.usageFound
    })
    this.resetOversizedEventState()
  }

  private skipParsing(reason: string): OpenAIStreamInspection {
    this.pendingLine = ''
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
    this.pendingLineBytes = 0
    this.pendingLineTail = ''
    this.pendingLineExceeded = false
    this.oversizedEvent = false
    this.oversizedEventType = undefined
    this.oversizedEventImageOutput = false
    this.oversizedEventUsage = emptyUsage()
    this.lightweightFragmentTail = ''
    this.lightweightTerminalPending = false
    this.inspection.skipped = true
    this.inspection.skipReason = reason
    return this.snapshot()
  }

  private recordEventSummary(summary: OpenAIStreamEventSummary): void {
    this.inspection.eventCount += 1
    this.inspection.lastEventType = summary.type
    this.inspection.eventTypeCounts[summary.type] = (this.inspection.eventTypeCounts[summary.type] ?? 0) + 1
    this.inspection.recentEventTypes.push(summary.type)
    if (this.inspection.recentEventTypes.length > streamInspectorRecentEventLimit) {
      this.inspection.recentEventTypes = this.inspection.recentEventTypes.slice(-streamInspectorRecentEventLimit)
    }
    this.pendingEventSummaries.push(summary)
  }

  private appendPendingLineSegment(segment: string): void {
    if (segment.length === 0) {
      return
    }
    const segmentBytes = Buffer.byteLength(segment, 'utf8')
    if (this.pendingLineExceeded) {
      this.pendingLineBytes += segmentBytes
      this.pendingLineTail = appendRollingTextTail(this.pendingLineTail, segment, streamInspectorUsageTailBytes)
      return
    }
    if (this.pendingLineBytes + segmentBytes > streamInspectorMaxLineBytes) {
      const prefixChars = Math.max(0, streamInspectorMaxLineBytes - this.pendingLineBytes + 1)
      this.pendingLineTail = appendRollingTextTail('', this.pendingLine + segment, streamInspectorUsageTailBytes)
      this.pendingLine += segment.slice(0, prefixChars)
      this.pendingLineBytes += segmentBytes
      this.pendingLineExceeded = true
      return
    }
    this.pendingLine += segment
    this.pendingLineBytes += segmentBytes
  }

  private flushPendingLine(): void {
    const line = this.pendingLine.endsWith('\r') ? this.pendingLine.slice(0, -1) : this.pendingLine
    const lineBytes = this.pendingLineBytes
    const lineTail = this.pendingLineTail.endsWith('\r') ? this.pendingLineTail.slice(0, -1) : this.pendingLineTail
    const exceeded = this.pendingLineExceeded
    this.pendingLine = ''
    this.pendingLineBytes = 0
    this.pendingLineExceeded = false
    this.pendingLineTail = ''
    if (exceeded) {
      this.processOversizedLine(line, lineBytes, lineTail)
      return
    }
    this.processLine(line)
  }

  private processOversizedLine(linePrefix: string, lineBytes: number, lineTail: string): void {
    if (linePrefix.startsWith('event:')) {
      this.eventName = linePrefix.slice(6).trim()
      return
    }
    if (!linePrefix.startsWith('data:')) {
      this.skipParsing('SSE 单行超过网关解析上限')
      return
    }
    const dataPrefix = linePrefix.slice(5).trimStart()
    this.dataBytes += Math.max(lineBytes, streamInspectorMaxLineBytes + 1)
    this.markOversizedEvent(dataPrefix)
    this.rememberOversizedEventImageOutput(lineTail)
    this.rememberOversizedEventUsage(dataPrefix)
    this.rememberOversizedEventUsage(lineTail)
    if (!this.oversizedEventType && !this.oversizedEventImageOutput && !this.eventName) {
      this.skipParsing('SSE 单行超过网关解析上限')
    }
  }

  private markOversizedEvent(dataPrefix: string): void {
    this.oversizedEvent = true
    this.dataLines = []
    this.rememberOversizedEventType(dataPrefix)
  }

  private rememberOversizedEventType(dataPrefix: string): void {
    this.oversizedEventType ??= extractOpenAIStreamEventTypeFromJsonPrefix(dataPrefix)
    this.rememberOversizedEventImageOutput(dataPrefix)
  }

  private rememberOversizedEventImageOutput(textFragment: string): void {
    if (!this.oversizedEventImageOutput && hasOpenAIImageStreamPayloadHint(textFragment)) {
      this.oversizedEventImageOutput = true
    }
  }

  private rememberOversizedEventUsage(textFragment: string): void {
    const usage = parseOpenAIUsageFromJsonTextFragment(textFragment)
    if (hasAnyUsageValue(usage)) {
      this.oversizedEventUsage = mergeUsage(this.oversizedEventUsage, usage)
    }
  }

  private flushOversizedEvent(): void {
    const eventType = this.oversizedEventType || this.eventName || 'message'
    const classification = classifyOversizedOpenAIStreamEvent(eventType, this.oversizedEventImageOutput)
    if (classification.visibleOutput) {
      this.inspection.outputReceived = true
      this.inspection.outputEventCount += 1
    }
    if (classification.imageOutput) {
      this.inspection.imageOutputReceived = true
    }
    if (classification.terminal) {
      this.inspection.terminalReceived = true
    }
    if (classification.failed) {
      this.inspection.failedReceived = true
    }
    const usageFound = hasAnyUsageValue(this.oversizedEventUsage)
    if (usageFound) {
      this.inspection.usage = mergeUsage(this.inspection.usage, this.oversizedEventUsage)
    }
    this.recordEventSummary({
      type: eventType,
      dataBytes: this.dataBytes,
      terminal: classification.terminal,
      canEndStream: classification.terminal,
      failed: classification.failed,
      output: classification.visibleOutput,
      usage: usageFound,
      parseError: true
    })
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
    this.resetOversizedEventState()
  }

  private resetOversizedEventState(): void {
    this.oversizedEvent = false
    this.oversizedEventType = undefined
    this.oversizedEventImageOutput = false
    this.oversizedEventUsage = emptyUsage()
  }

  private pushImageStreamChunkLightweight(buffer: Buffer): OpenAIStreamInspection {
    const rawFragments = lightweightImageStreamFragments(buffer)
    const fragments = this.lightweightFragmentTail
      ? [this.lightweightFragmentTail + rawFragments[0], ...rawFragments]
      : rawFragments
    const eventTypes = extractLightweightImageStreamEventTypes(fragments, this.eventName)
    const usage = parseLightweightImageStreamUsage(fragments)
    const boundaryObserved = lightweightImageStreamBoundaryObserved(fragments)
    this.lightweightFragmentTail = appendRollingTextTail('', rawFragments[rawFragments.length - 1] ?? '', streamInspectorLightweightRollingTailBytes)
    if (hasAnyUsageValue(usage)) {
      this.inspection.usage = mergeUsage(this.inspection.usage, usage)
    }
    this.pendingLine = ''
    this.pendingLineBytes = 0
    this.pendingLineExceeded = false
    this.pendingLineTail = ''
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
    this.resetOversizedEventState()
    let pendingTerminalReleased = false
    let terminalEndSummaryRecorded = false
    if (this.lightweightTerminalPending && boundaryObserved) {
      this.inspection.terminalReceived = true
      this.lightweightTerminalPending = false
      pendingTerminalReleased = true
    }
    for (const eventType of eventTypes) {
      const classification = classifyOversizedOpenAIStreamEvent(eventType, true)
      if (classification.visibleOutput) {
        this.inspection.outputReceived = true
        this.inspection.outputEventCount += 1
      }
      if (classification.imageOutput) {
        this.inspection.imageOutputReceived = true
      }
      if (classification.terminal) {
        if (isLightweightTerminalSafeToEnd(eventType) || boundaryObserved) {
          this.inspection.terminalReceived = true
          this.lightweightTerminalPending = false
        } else {
          this.lightweightTerminalPending = true
        }
      }
      if (classification.failed) {
        this.inspection.failedReceived = true
      }
      const canEndStream = classification.terminal && (isLightweightTerminalSafeToEnd(eventType) || boundaryObserved)
      terminalEndSummaryRecorded = terminalEndSummaryRecorded || canEndStream
      this.recordEventSummary({
        type: eventType,
        dataBytes: buffer.length,
        terminal: classification.terminal,
        canEndStream,
        failed: classification.failed,
        output: classification.visibleOutput,
        usage: hasAnyUsageValue(usage),
        parseError: true
      })
    }
    if (pendingTerminalReleased && !terminalEndSummaryRecorded) {
      this.recordEventSummary({
        type: 'image_stream_boundary',
        dataBytes: buffer.length,
        terminal: true,
        canEndStream: true,
        failed: false,
        output: false,
        usage: hasAnyUsageValue(usage),
        parseError: true
      })
    }
    return this.snapshot()
  }
}

const streamInspectorMaxLineBytes = 256 * 1024
const streamInspectorMaxEventBytes = 512 * 1024
const streamInspectorUsageTailBytes = 256 * 1024
const streamInspectorRecentEventLimit = 20
const streamInspectorImageLightweightChunkBytes = 64 * 1024
const streamInspectorImageLightweightFragmentBytes = 64 * 1024
const streamInspectorLightweightRollingTailBytes = 64 * 1024

function extractOpenAIStreamEventTypeFromJsonPrefix(dataPrefix: string): string | undefined {
  const match = /"type"\s*:\s*"([^"]{1,160})"/.exec(dataPrefix.slice(0, 4096))
  return match?.[1]
}

function classifyOversizedOpenAIStreamEvent(eventType: string, imageOutputHint = false): Pick<OpenAIStreamEventClassification, 'terminal' | 'failed' | 'visibleOutput' | 'imageOutput'> {
  const terminal = eventType === '[DONE]'
    || eventType === 'response.completed'
    || eventType === 'response.done'
    || eventType === 'response.incomplete'
    || eventType === 'response.failed'
    || eventType === 'image_generation.completed'
    || eventType === 'image_generation.failed'
  const failed = eventType === 'response.failed'
    || eventType === 'image_generation.failed'
    || eventType === 'error'
  const imageOutput = imageOutputHint || isOpenAIImageStreamEventType(eventType)
  const visibleOutput = eventType.endsWith('.delta')
    || eventType === 'response.output_item.added'
    || eventType === 'response.output_item.done'
    || eventType === 'response.completed'
    || eventType === 'response.done'
    || eventType === 'response.incomplete'
    || imageOutput
  return { terminal, failed, visibleOutput, imageOutput }
}

function hasOpenAIImageStreamPayloadHint(textFragment: string): boolean {
  const searchable = textFragment.slice(0, 65536)
  return /"type"\s*:\s*"image_generation_call"/.test(searchable)
    || /"(partial_image_b64|b64_json)"\s*:/.test(searchable)
}

function hasOpenAIImageStreamPayloadHintBytes(buffer: Buffer): boolean {
  return imageStreamPayloadHintTokens.some((token) => buffer.indexOf(token) >= 0)
}

function lightweightImageStreamFragments(buffer: Buffer): string[] {
  const prefix = buffer.subarray(0, streamInspectorImageLightweightFragmentBytes).toString('utf8')
  const tailStart = Math.max(0, buffer.length - streamInspectorImageLightweightFragmentBytes)
  if (tailStart === 0) {
    return [prefix]
  }
  return [
    prefix,
    buffer.subarray(tailStart).toString('utf8')
  ]
}

function extractLightweightImageStreamEventTypes(fragments: string[], currentEventName: string): string[] {
  const eventTypes: string[] = []
  const remember = (eventType: string | undefined) => {
    const normalized = eventType?.trim()
    if (!normalized || !isLightweightImageStreamEventType(normalized) || eventTypes.includes(normalized)) {
      return
    }
    eventTypes.push(normalized)
  }
  remember(currentEventName)
  for (const fragment of fragments) {
    for (const match of fragment.matchAll(/^event:\s*([^\r\n]+)/gm)) {
      remember(match[1])
    }
    for (const match of fragment.matchAll(/"type"\s*:\s*"([^"]{1,160})"/g)) {
      remember(match[1])
    }
    if (/data:\s*\[DONE\]/.test(fragment)) {
      remember('[DONE]')
    }
    for (const eventType of lightweightImageStreamControlEventTypes) {
      if (fragment.includes(eventType)) {
        remember(eventType)
      }
    }
  }
  if (eventTypes.length === 0 && fragments.some(hasOpenAIImageStreamPayloadHint)) {
    eventTypes.push('image_stream_payload')
  }
  return eventTypes
}

function parseLightweightImageStreamUsage(fragments: string[]): ParsedUsage {
  return fragments.reduce((usage, fragment) => {
    const parsed = parseOpenAIUsageFromJsonTextFragment(fragment)
    return hasAnyUsageValue(parsed) ? mergeUsage(usage, parsed) : usage
  }, emptyUsage())
}

function isLightweightImageStreamEventType(eventType: string): boolean {
  return eventType === '[DONE]'
    || eventType === 'response.completed'
    || eventType === 'response.done'
    || eventType === 'response.incomplete'
    || eventType === 'response.failed'
    || eventType === 'error'
    || isOpenAIImageStreamEventType(eventType)
    || eventType === 'image_stream_payload'
}

function isLightweightTerminalSafeToEnd(eventType: string): boolean {
  return eventType === '[DONE]'
    || eventType === 'response.completed'
    || eventType === 'response.done'
    || eventType === 'response.incomplete'
    || eventType === 'response.failed'
    || eventType === 'error'
}

function lightweightImageStreamBoundaryObserved(fragments: string[]): boolean {
  return fragments.some((fragment) => /\r?\n\r?\n/.test(fragment) || /data:\s*\[DONE\]/.test(fragment))
}

function bufferFromChunk(chunk: Buffer | Uint8Array): Buffer {
  return Buffer.isBuffer(chunk)
    ? chunk
    : Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength)
}

function appendRollingTextTail(current: string, next: string, limitChars: number): string {
  if (limitChars <= 0 || next.length === 0) {
    return current
  }
  const combined = current.length > 0 ? current + next : next
  return combined.length > limitChars ? combined.slice(combined.length - limitChars) : combined
}

const imageStreamPayloadHintTokens = [
  Buffer.from('response.image_generation_call.', 'utf8'),
  Buffer.from('image_generation.partial_image', 'utf8'),
  Buffer.from('image_generation.completed', 'utf8'),
  Buffer.from('image_generation.failed', 'utf8'),
  Buffer.from('image_generation_call', 'utf8'),
  Buffer.from('"partial_image_b64"', 'utf8'),
  Buffer.from('"b64_json"', 'utf8')
]

const lightweightImageStreamControlEventTypes = [
  'response.image_generation_call.partial_image',
  'response.image_generation_call.completed',
  'response.image_generation_call.failed',
  'image_generation.partial_image',
  'image_generation.completed',
  'image_generation.failed',
  'response.completed',
  'response.done',
  'response.incomplete',
  'response.failed',
  'error'
]
