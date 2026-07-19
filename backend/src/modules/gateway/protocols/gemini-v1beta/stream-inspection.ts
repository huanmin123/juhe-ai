import type { Request } from 'express'

import {
  emptyUsage,
  hasAnyUsageValue,
  mergeUsage,
  type ParsedUsage
} from '../../usage/types.js'
import {
  hasPendingSseProtocolEvent
} from '../_shared/sse-pending-event.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../../request/body.js'
import {
  estimateTokenCountFromText,
  parseOpenAISseEventText
} from '../openai-v1/stream-events.js'
import {
  extractGeminiStreamEventError
} from './response-semantics.js'
import {
  extractGeminiUsage
} from './usage.js'

export interface GeminiStreamInspection {
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
  pendingEvent: boolean
  skipped: boolean
  skipReason?: string
  errorCode?: string
  errorMessage?: string
  responseResourceId?: string
  usage: ParsedUsage
}

export interface GeminiStreamEventSummary {
  type: string
  dataBytes: number
  terminal: boolean
  canEndStream: boolean
  failed: boolean
  output: boolean
  usage: boolean
  parseError?: boolean
}

export interface GeminiStreamUsageFallbackResult {
  usage: ParsedUsage
  estimated: boolean
  estimatedInputTokens?: number
  estimatedOutputTokens?: number
}

const streamInspectorMaxEventBytes = 256 * 1024
const streamInspectorRecentEventTypes = 8

export function applyGeminiStreamUsageFallback(
  req: Request,
  usage: ParsedUsage,
  input: {
    outputReceived: boolean
    estimatedOutputTokens?: number
  }
): GeminiStreamUsageFallbackResult {
  if (!input.outputReceived || (positiveTokenCount(usage.inputTokens) && positiveTokenCount(usage.outputTokens))) {
    return { usage, estimated: false }
  }
  const nextUsage: ParsedUsage = { ...usage }
  let estimated = false
  let estimatedInputTokens: number | undefined
  let estimatedOutputTokens: number | undefined

  if (!positiveTokenCount(nextUsage.inputTokens)) {
    const inputTokens = estimateGeminiRequestInputTokens(req)
    if (inputTokens !== undefined) {
      nextUsage.inputTokens = inputTokens
      estimatedInputTokens = inputTokens
      estimated = true
    }
  }

  if (!positiveTokenCount(nextUsage.outputTokens)) {
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

export function inspectGeminiStreamText(text: string): GeminiStreamInspection {
  const inspector = new GeminiStreamInspector()
  inspector.pushText(text)
  return inspector.finish()
}

export class GeminiStreamInspector {
  private inspection: GeminiStreamInspection = {
    terminalReceived: false,
    failedReceived: false,
    outputReceived: false,
    imageOutputReceived: false,
    outputEventCount: 0,
    eventCount: 0,
    eventTypeCounts: {},
    recentEventTypes: [],
    pendingEvent: false,
    skipped: false,
    usage: emptyUsage()
  }
  private eventName = ''
  private dataLines: string[] = []
  private dataBytes = 0
  private pendingLine = ''
  private pendingEventSummaries: GeminiStreamEventSummary[] = []

  pushChunk(chunk: Buffer | Uint8Array | string, _options: { lightweightImageStream?: boolean } = {}): GeminiStreamInspection {
    const text = typeof chunk === 'string' ? chunk : Buffer.from(chunk).toString('utf8')
    return this.pushText(text)
  }

  pushText(text: string): GeminiStreamInspection {
    if (this.inspection.skipped) return this.snapshot()
    let offset = 0
    while (offset < text.length) {
      const newlineIndex = text.indexOf('\n', offset)
      const segmentEnd = newlineIndex < 0 ? text.length : newlineIndex
      this.pendingLine += text.slice(offset, segmentEnd).replace(/\r$/, '')
      if (newlineIndex < 0) break
      this.processLine(this.pendingLine)
      this.pendingLine = ''
      offset = newlineIndex + 1
    }
    return this.snapshot()
  }

  finish(): GeminiStreamInspection {
    if (this.inspection.skipped) return this.snapshot()
    if (this.pendingLine.length > 0) {
      this.processLine(this.pendingLine)
      this.pendingLine = ''
    }
    this.flushEvent()
    if (this.inspection.outputReceived && !this.inspection.failedReceived) {
      this.inspection.terminalReceived = true
    }
    return this.snapshot()
  }

  snapshot(): GeminiStreamInspection {
    return {
      ...this.inspection,
      eventTypeCounts: { ...this.inspection.eventTypeCounts },
      recentEventTypes: [...this.inspection.recentEventTypes],
      pendingEvent: hasPendingSseProtocolEvent({
        skipped: this.inspection.skipped,
        eventName: this.eventName,
        dataLineCount: this.dataLines.length,
        dataBytes: this.dataBytes,
        pendingLine: this.pendingLine
      }),
      usage: { ...this.inspection.usage }
    }
  }

  drainEventSummaries(): GeminiStreamEventSummary[] {
    const summaries = this.pendingEventSummaries
    this.pendingEventSummaries = []
    return summaries.map((event) => ({ ...event }))
  }

  drainEventSummariesCanEndStream(): boolean {
    const summaries = this.pendingEventSummaries
    this.pendingEventSummaries = []
    return summaries.some((event) => event.canEndStream)
  }

  private processLine(line: string): void {
    if (line === '') {
      this.flushEvent()
      return
    }
    if (line.startsWith('event:')) {
      this.eventName = line.slice(6).trim()
      return
    }
    if (!line.startsWith('data:')) return
    const dataLine = line.slice(5).trimStart()
    this.dataBytes += Buffer.byteLength(dataLine, 'utf8')
    if (this.dataBytes > streamInspectorMaxEventBytes) {
      this.inspection.skipped = true
      this.inspection.skipReason = 'gemini_stream_event_too_large'
      this.resetEvent()
      return
    }
    this.dataLines.push(dataLine)
  }

  private flushEvent(): void {
    if (this.inspection.skipped) return
    if (this.dataLines.length === 0) {
      this.resetEvent()
      return
    }
    const eventName = this.eventName
    const rawText = `${eventName ? `event: ${eventName}\n` : ''}${this.dataLines.map((line) => `data: ${line}`).join('\n')}\n\n`
    const event = parseOpenAISseEventText(rawText)
    const eventType = event.eventType || event.eventName || eventName || 'message'
    const summary = this.classifyEvent(eventType, event.data, event.dataParseError, this.dataBytes)
    this.recordEventSummary(summary)
    this.resetEvent()
  }

  private classifyEvent(
    eventType: string,
    data: Record<string, unknown> | undefined,
    parseError: boolean,
    dataBytes: number
  ): GeminiStreamEventSummary {
    this.inspection.eventCount += 1
    this.inspection.lastEventType = eventType
    this.inspection.eventTypeCounts[eventType] = (this.inspection.eventTypeCounts[eventType] ?? 0) + 1
    this.inspection.recentEventTypes.push(eventType)
    if (this.inspection.recentEventTypes.length > streamInspectorRecentEventTypes) {
      this.inspection.recentEventTypes.shift()
    }

    const error = data ? extractGeminiStreamEventError(data) : undefined
    const failed = Boolean(error) || eventType === 'interaction.failed'
    const outputText = data ? outputTextFromGeminiStreamEvent(data) : undefined
    const output = Boolean(outputText)
    const finishReason = data ? firstGeminiFinishReason(data, eventType) : undefined
    const terminal = failed || Boolean(finishReason) || isGeminiTerminalEventType(eventType)
    const usage = data ? extractGeminiUsage(data) : emptyUsage()
    const responseResourceId = data ? interactionResourceId(data, eventType) : undefined

    if (failed) {
      this.inspection.failedReceived = true
      this.inspection.errorCode = stringValue(error?.status) ?? stringValue(error?.code)
      this.inspection.errorMessage = stringValue(error?.message) ?? 'Gemini 流式响应失败'
    }
    if (terminal) {
      this.inspection.terminalReceived = true
    }
    if (output) {
      this.inspection.outputReceived = true
      this.inspection.outputEventCount += 1
      this.inspection.estimatedOutputTokens = (this.inspection.estimatedOutputTokens ?? 0) + estimateTokenCountFromText(outputText ?? '')
    }
    if (hasAnyUsageValue(usage)) {
      this.inspection.usage = mergeUsage(this.inspection.usage, usage)
    }
    if (!this.inspection.responseResourceId && responseResourceId) {
      this.inspection.responseResourceId = responseResourceId
    }

    return {
      type: eventType,
      dataBytes,
      terminal,
      canEndStream: terminal && !failed,
      failed,
      output,
      usage: hasAnyUsageValue(usage),
      parseError
    }
  }

  private recordEventSummary(summary: GeminiStreamEventSummary): void {
    this.pendingEventSummaries.push({ ...summary })
  }

  private resetEvent(): void {
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
  }
}

function interactionResourceId(data: Record<string, unknown>, eventType: string): string | undefined {
  if (!eventType.startsWith('interaction.')) return undefined
  return stringValue(objectValue(data.interaction)?.id) || stringValue(data.interaction_id) || undefined
}

function outputTextFromGeminiStreamEvent(data: Record<string, unknown>): string | undefined {
  const parts = candidateParts(data)
  const text = parts
    .map((part) => stringValue(objectValue(part)?.text))
    .filter((value): value is string => Boolean(value))
    .join('')
  if (text) return text
  const delta = objectValue(data.delta)
  return stringValue(delta?.text) || undefined
}

function firstGeminiFinishReason(data: Record<string, unknown>, eventType?: string): string | undefined {
  if (eventType === 'interaction.completed') {
    return stringValue(objectValue(data.interaction)?.status) ?? 'completed'
  }
  if (eventType === 'interaction.failed') {
    return stringValue(objectValue(data.interaction)?.status) ?? 'failed'
  }
  const candidates = Array.isArray(data.candidates) ? data.candidates : []
  for (const candidate of candidates) {
    const reason = stringValue(objectValue(candidate)?.finishReason)
    if (reason) return reason
  }
  return undefined
}

function isGeminiTerminalEventType(eventType: string): boolean {
  return eventType === 'finish' || eventType === 'done' || eventType === '[DONE]'
}

function candidateParts(data: Record<string, unknown>): unknown[] {
  const candidates = Array.isArray(data.candidates) ? data.candidates : []
  const parts: unknown[] = []
  for (const candidate of candidates) {
    const content = objectValue(objectValue(candidate)?.content)
    const candidateParts = Array.isArray(content?.parts) ? content.parts : []
    parts.push(...candidateParts)
  }
  return parts
}

function estimateGeminiRequestInputTokens(req: Request): number | undefined {
  const bodyState = getGatewayRequestBodyState(req)
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  if (req.body && typeof req.body === 'object') {
    const tokenCount = estimateTokenCountFromText(JSON.stringify(req.body))
    if (tokenCount > 0) return tokenCount
  }
  if (!rawBody || rawBody.length === 0) return undefined
  if (bodyState?.isJson && bodyState.jsonParseStatus === 'parsed') return undefined
  return Math.max(1, Math.ceil(rawBody.length / 4))
}

function positiveTokenCount(value: number | undefined): boolean {
  return typeof value === 'number' && value > 0
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string | undefined {
  if (typeof value === 'string') return value.length > 0 ? value : undefined
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return undefined
}
