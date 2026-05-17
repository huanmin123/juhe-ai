import type { Request } from 'express'

import {
  emptyUsage,
  mergeUsage,
  type ParsedUsage
} from './openai-gateway-usage.js'
import {
  classifyOpenAIStreamEvent,
  estimateOpenAIRequestInputTokens,
  parseOpenAIStreamEventData
} from './openai-gateway-stream-events.js'

export interface OpenAIStreamInspection {
  terminalReceived: boolean
  failedReceived: boolean
  outputReceived: boolean
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
  private pendingEventSummaries: OpenAIStreamEventSummary[] = []

  pushChunk(chunk: Buffer | Uint8Array | string): OpenAIStreamInspection {
    const text = typeof chunk === 'string' ? chunk : Buffer.from(chunk).toString('utf8')
    return this.pushText(text)
  }

  pushText(text: string): OpenAIStreamInspection {
    if (this.inspection.skipped) {
      return this.snapshot()
    }
    this.pendingLine += text
    if (Buffer.byteLength(this.pendingLine, 'utf8') > streamInspectorMaxLineBytes) {
      return this.skipParsing('SSE 单行超过网关解析上限')
    }
    while (true) {
      const newlineIndex = this.pendingLine.indexOf('\n')
      if (newlineIndex < 0) break
      const rawLine = this.pendingLine.slice(0, newlineIndex)
      this.pendingLine = this.pendingLine.slice(newlineIndex + 1)
      this.processLine(rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine)
    }
    return this.snapshot()
  }

  finish(): OpenAIStreamInspection {
    if (this.inspection.skipped) {
      return this.snapshot()
    }
    if (this.pendingLine.length > 0) {
      const line = this.pendingLine.endsWith('\r') ? this.pendingLine.slice(0, -1) : this.pendingLine
      this.pendingLine = ''
      this.processLine(line)
    }
    this.flushEvent()
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
      if (this.dataBytes > streamInspectorMaxEventBytes) {
        this.skipParsing('SSE 单事件超过网关解析上限')
        return
      }
      this.dataLines.push(dataLine)
    }
  }

  private flushEvent(): void {
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
        failed: false,
        output: false,
        usage: false,
        parseError: true
      })
      return
    }

    const classification = classifyOpenAIStreamEvent(event, this.inspection.estimatedOutputTokens ?? 0)
    if (classification.visibleOutput) {
      this.inspection.outputReceived = true
      this.inspection.outputEventCount += 1
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
      failed: classification.failed,
      output: classification.visibleOutput,
      usage: classification.usageFound
    })
  }

  private skipParsing(reason: string): OpenAIStreamInspection {
    this.pendingLine = ''
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
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
}

const streamInspectorMaxLineBytes = 256 * 1024
const streamInspectorMaxEventBytes = 512 * 1024
const streamInspectorRecentEventLimit = 20

