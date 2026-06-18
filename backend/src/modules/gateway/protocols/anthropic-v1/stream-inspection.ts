import type { Request } from 'express'

import {
  emptyUsage,
  hasAnyUsageValue,
  mergeUsage,
  type ParsedUsage
} from '../../usage/types.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../../request/body.js'
import {
  estimateTokenCountFromText
} from '../openai-v1/stream-events.js'
import {
  extractAnthropicStreamEventError,
  parseAnthropicSseEventText
} from './response-semantics.js'
import {
  extractAnthropicUsage
} from './usage.js'

export interface AnthropicStreamInspection {
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

export interface AnthropicStreamEventSummary {
  type: string
  dataBytes: number
  terminal: boolean
  canEndStream: boolean
  failed: boolean
  output: boolean
  usage: boolean
  parseError?: boolean
}

export interface AnthropicStreamUsageFallbackResult {
  usage: ParsedUsage
  estimated: boolean
  estimatedInputTokens?: number
  estimatedOutputTokens?: number
}

const streamInspectorMaxEventBytes = 256 * 1024
const streamInspectorRecentEventTypes = 8

export function applyAnthropicStreamUsageFallback(
  req: Request,
  usage: ParsedUsage,
  input: {
    outputReceived: boolean
    estimatedOutputTokens?: number
  }
): AnthropicStreamUsageFallbackResult {
  if (!input.outputReceived || (usage.inputTokens !== undefined && usage.outputTokens !== undefined)) {
    return { usage, estimated: false }
  }
  const nextUsage: ParsedUsage = { ...usage }
  let estimated = false
  let estimatedInputTokens: number | undefined
  let estimatedOutputTokens: number | undefined

  if (nextUsage.inputTokens === undefined) {
    const inputTokens = estimateAnthropicRequestInputTokens(req)
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

export function inspectAnthropicStreamText(text: string): AnthropicStreamInspection {
  const inspector = new AnthropicStreamInspector()
  inspector.pushText(text)
  return inspector.finish()
}

export class AnthropicStreamInspector {
  private inspection: AnthropicStreamInspection = {
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
  private pendingEventSummaries: AnthropicStreamEventSummary[] = []

  pushChunk(chunk: Buffer | Uint8Array | string, _options: { lightweightImageStream?: boolean } = {}): AnthropicStreamInspection {
    const text = typeof chunk === 'string' ? chunk : Buffer.from(chunk).toString('utf8')
    return this.pushText(text)
  }

  pushText(text: string): AnthropicStreamInspection {
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

  finish(): AnthropicStreamInspection {
    if (this.inspection.skipped) return this.snapshot()
    if (this.pendingLine.length > 0) {
      this.processLine(this.pendingLine)
      this.pendingLine = ''
    }
    this.flushEvent()
    return this.snapshot()
  }

  snapshot(): AnthropicStreamInspection {
    return {
      ...this.inspection,
      eventTypeCounts: { ...this.inspection.eventTypeCounts },
      recentEventTypes: [...this.inspection.recentEventTypes],
      usage: { ...this.inspection.usage }
    }
  }

  drainEventSummaries(): AnthropicStreamEventSummary[] {
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
      this.inspection.skipReason = 'anthropic_stream_event_too_large'
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
    const dataText = this.dataLines.join('\n').trim()
    const rawText = `${eventName ? `event: ${eventName}\n` : ''}${this.dataLines.map((line) => `data: ${line}`).join('\n')}\n\n`
    const event = parseAnthropicSseEventText(rawText)
    const eventType = event.eventType || event.eventName || eventName || 'message'
    const data = event.data
    const summary = this.classifyEvent(eventType, data, event.dataParseError, this.dataBytes)
    this.recordEventSummary(summary)
    this.resetEvent()
  }

  private classifyEvent(
    eventType: string,
    data: Record<string, unknown> | undefined,
    parseError: boolean,
    dataBytes: number
  ): AnthropicStreamEventSummary {
    this.inspection.eventCount += 1
    this.inspection.lastEventType = eventType
    this.inspection.eventTypeCounts[eventType] = (this.inspection.eventTypeCounts[eventType] ?? 0) + 1
    this.inspection.recentEventTypes.push(eventType)
    if (this.inspection.recentEventTypes.length > streamInspectorRecentEventTypes) {
      this.inspection.recentEventTypes.shift()
    }

    const error = data ? extractAnthropicStreamEventError(data) : undefined
    const failed = Boolean(error)
    const terminal = eventType === 'message_stop' || failed
    const outputText = data ? outputTextFromAnthropicStreamEvent(eventType, data) : undefined
    const output = Boolean(outputText)
    const usage = data ? usageFromAnthropicStreamEvent(data) : emptyUsage()

    if (failed) {
      this.inspection.failedReceived = true
      this.inspection.errorCode = stringValue(error?.type) ?? stringValue(error?.code)
      this.inspection.errorMessage = stringValue(error?.message) ?? 'Anthropic 流式响应失败'
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

    return {
      type: eventType,
      dataBytes,
      terminal,
      canEndStream: eventType === 'message_stop',
      failed,
      output,
      usage: hasAnyUsageValue(usage),
      parseError
    }
  }

  private recordEventSummary(summary: AnthropicStreamEventSummary): void {
    this.pendingEventSummaries.push({ ...summary })
  }

  private resetEvent(): void {
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
  }
}

function outputTextFromAnthropicStreamEvent(eventType: string, data: Record<string, unknown>): string | undefined {
  if (eventType === 'content_block_start') {
    const block = objectValue(data.content_block)
    return block?.type === 'tool_use' ? JSON.stringify(block) : undefined
  }
  if (eventType !== 'content_block_delta') return undefined
  const delta = objectValue(data.delta)
  if (!delta) return undefined
  if (delta.type === 'text_delta') return stringValue(delta.text)
  if (delta.type === 'input_json_delta') return stringValue(delta.partial_json)
  if (delta.type === 'thinking_delta') return stringValue(delta.thinking)
  if (delta.type === 'signature_delta') return stringValue(delta.signature)
  return undefined
}

function usageFromAnthropicStreamEvent(data: Record<string, unknown>): ParsedUsage {
  const usage = objectValue(data.usage)
  if (usage) return extractAnthropicUsage(usage)
  const message = objectValue(data.message)
  const messageUsage = objectValue(message?.usage)
  return messageUsage ? extractAnthropicUsage(messageUsage) : emptyUsage()
}

function estimateAnthropicRequestInputTokens(req: Request): number | undefined {
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

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}
