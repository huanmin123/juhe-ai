import { buildGatewayStreamFailureEvent } from './openai-gateway-responses.js'
import {
  isExecutableClientRetryStreamRule,
  type StreamClientRetryInterceptRule,
  type StreamInterceptRule,
  type StreamInterceptRuleTriggerPhase
} from './openai-gateway-stream-rules.js'

export interface StreamInterceptContext {
  provider: 'openai'
  endpoint: string
  streamOnly: boolean
}

export interface StreamInterceptDecision {
  ruleId: string
  ruleName: string
  action: StreamInterceptRule['action']
  triggerPhase: StreamInterceptRuleTriggerPhase
  upstreamEventType: string
  upstreamErrorCode?: string
  upstreamErrorMessage?: string
  rewriteErrorCode: string
  rewriteMessage: string
  accountPolicy: StreamInterceptRule['accountPolicy']
  cooldownMinutes?: number
  outputSeen: boolean
}

export interface StreamInterceptorResult {
  chunks: Buffer[]
  intercepted?: StreamInterceptDecision
  sideEffects: StreamInterceptDecision[]
  parserSkipped: boolean
}

interface ParsedSseEvent {
  rawText: string
  eventName: string
  dataText: string
  data?: Record<string, unknown>
  dataParseError: boolean
  eventType: string
  errorCode?: string
  errorMessage?: string
}

const maxBufferedSseEventBytes = 256 * 1024

export class OpenAIStreamInterceptBuffer {
  private pendingBuffer = Buffer.alloc(0)
  private parserSkipped = false
  private outputSeen = false
  private readonly activeRules: StreamClientRetryInterceptRule[]

  constructor(
    rules: StreamInterceptRule[],
    private readonly context: StreamInterceptContext
  ) {
    this.activeRules = rules.filter((rule): rule is StreamClientRetryInterceptRule => isExecutableClientRetryStreamRule(rule) && isRuleRelevantToContext(rule, context))
  }

  pushChunk(chunk: Buffer): StreamInterceptorResult {
    if (this.parserSkipped || this.activeRules.length === 0) {
      return {
        chunks: [chunk],
        sideEffects: [],
        parserSkipped: this.parserSkipped
      }
    }

    this.pendingBuffer = Buffer.concat([this.pendingBuffer, chunk])
    if (this.pendingBuffer.length > maxBufferedSseEventBytes) {
      const buffered = this.pendingBuffer
      this.pendingBuffer = Buffer.alloc(0)
      this.parserSkipped = true
      return {
        chunks: [buffered],
        sideEffects: [],
        parserSkipped: true
      }
    }

    const chunks: Buffer[] = []
    const sideEffects: StreamInterceptDecision[] = []

    while (true) {
      const boundary = findSseEventBoundary(this.pendingBuffer)
      if (!boundary) break
      const rawBuffer = this.pendingBuffer.subarray(0, boundary.endIndex)
      this.pendingBuffer = this.pendingBuffer.subarray(boundary.endIndex)
      const rawText = rawBuffer.toString('utf8')
      const event = parseSseEvent(rawText)
      const decision = matchStreamInterceptRule(this.activeRules, event, this.outputSeen)
      if (decision) {
        chunks.push(buildGatewayStreamFailureEvent(decision.rewriteMessage, decision.rewriteErrorCode))
        return {
          chunks,
          intercepted: decision,
          sideEffects,
          parserSkipped: this.parserSkipped
        }
      }
      chunks.push(rawBuffer)
      if (isVisibleOutputEvent(event)) {
        this.outputSeen = true
      }
    }

    return {
      chunks,
      sideEffects,
      parserSkipped: this.parserSkipped
    }
  }

  flushPendingOnEof(): StreamInterceptorResult {
    if (this.parserSkipped || this.activeRules.length === 0 || this.pendingBuffer.length === 0) {
      return {
        chunks: [],
        sideEffects: [],
        parserSkipped: this.parserSkipped
      }
    }

    const rawBuffer = ensureSseEventBoundary(this.pendingBuffer)
    this.pendingBuffer = Buffer.alloc(0)
    const event = parseSseEvent(rawBuffer.toString('utf8'))
    const decision = matchStreamInterceptRule(this.activeRules, event, this.outputSeen)
    if (decision) {
      return {
        chunks: [buildGatewayStreamFailureEvent(decision.rewriteMessage, decision.rewriteErrorCode)],
        intercepted: decision,
        sideEffects: [],
        parserSkipped: this.parserSkipped
      }
    }
    if (isVisibleOutputEvent(event)) {
      this.outputSeen = true
    }
    return {
      chunks: [rawBuffer],
      sideEffects: [],
      parserSkipped: this.parserSkipped
    }
  }
}

function matchStreamInterceptRule(
  rules: StreamClientRetryInterceptRule[],
  event: ParsedSseEvent,
  outputSeen: boolean
): StreamInterceptDecision | undefined {
  const rule = rules.find((item) => matchesStreamInterceptRule(item, event, outputSeen))
  return rule ? buildDecision(rule, event, outputSeen) : undefined
}

function isRuleRelevantToContext(
  rule: StreamInterceptRule,
  context: StreamInterceptContext
): boolean {
  if (!rule.enabled) return false
  if (rule.streamOnly && !context.streamOnly) return false
  if (rule.provider !== 'all' && rule.provider !== context.provider) return false
  if (rule.endpoint !== 'all' && !normalizeEndpoint(context.endpoint).endsWith(rule.endpoint)) return false
  return true
}

function matchesStreamInterceptRule(
  rule: StreamClientRetryInterceptRule,
  event: ParsedSseEvent,
  outputSeen: boolean
): boolean {
  if (!matchesTriggerPhase(rule.triggerPhase, outputSeen)) return false
  if (!matchesEventTypes(rule.match.eventTypes, event)) return false
  if (rule.match.errorCodes && rule.match.errorCodes.length > 0 && !rule.match.errorCodes.includes(event.errorCode ?? '')) return false
  if (rule.match.messageKeywords && rule.match.messageKeywords.length > 0) {
    const message = (event.errorMessage ?? event.dataText).toLowerCase()
    if (!rule.match.messageKeywords.every((keyword) => message.includes(keyword.toLowerCase()))) return false
  }
  if (rule.match.dataKeywords && rule.match.dataKeywords.length > 0) {
    const dataText = event.dataText.toLowerCase()
    if (!rule.match.dataKeywords.every((keyword) => dataText.includes(keyword.toLowerCase()))) return false
  }
  return true
}

function buildDecision(rule: StreamClientRetryInterceptRule, event: ParsedSseEvent, outputSeen: boolean): StreamInterceptDecision {
  return {
    ruleId: rule.id,
    ruleName: rule.name,
    action: rule.action,
    triggerPhase: rule.triggerPhase,
    upstreamEventType: event.eventType || event.eventName || 'message',
    upstreamErrorCode: event.errorCode,
    upstreamErrorMessage: event.errorMessage,
    rewriteErrorCode: rule.clientRetry.rewriteErrorCode,
    rewriteMessage: rule.clientRetry.rewriteMessage,
    accountPolicy: rule.accountPolicy,
    cooldownMinutes: rule.cooldownMinutes,
    outputSeen
  }
}

function matchesEventTypes(eventTypes: StreamInterceptRule['match']['eventTypes'], event: ParsedSseEvent): boolean {
  if (eventTypes === 'all') return true
  return eventTypes.includes(event.eventType) || eventTypes.includes(event.eventName)
}

function matchesTriggerPhase(phase: StreamInterceptRuleTriggerPhase, outputSeen: boolean): boolean {
  if (phase === 'all') return true
  if (phase === 'before_output') return !outputSeen
  return outputSeen
}

function normalizeEndpoint(endpoint: string): string {
  const [path] = endpoint.split('?')
  const trimmed = path.trim()
  const methodPath = trimmed.match(/^[A-Z]+\s+(.+)$/)
  return methodPath?.[1] ?? trimmed
}

function findSseEventBoundary(buffer: Buffer): { endIndex: number } | undefined {
  const candidates = [
    boundaryCandidate(buffer, '\r\n\r\n'),
    boundaryCandidate(buffer, '\n\n'),
    boundaryCandidate(buffer, '\r\r')
  ].filter((item): item is { index: number; length: number } => Boolean(item))
  if (!candidates.length) return undefined
  const first = candidates.sort((left, right) => left.index - right.index || right.length - left.length)[0]
  return { endIndex: first.index + first.length }
}

function boundaryCandidate(buffer: Buffer, token: string): { index: number; length: number } | undefined {
  const tokenBuffer = Buffer.from(token, 'utf8')
  const index = buffer.indexOf(tokenBuffer)
  return index >= 0 ? { index, length: token.length } : undefined
}

function ensureSseEventBoundary(buffer: Buffer): Buffer {
  const text = buffer.toString('utf8')
  if (text.endsWith('\r\n\r\n') || text.endsWith('\n\n') || text.endsWith('\r\r')) {
    return buffer
  }
  return Buffer.concat([buffer, Buffer.from('\n\n', 'utf8')])
}

function parseSseEvent(rawText: string): ParsedSseEvent {
  let eventName = ''
  const dataLines: string[] = []
  for (const line of rawText.split(/\r?\n|\r/)) {
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  const dataText = dataLines.join('\n').trim()
  if (!dataText || dataText === '[DONE]') {
    return {
      rawText,
      eventName,
      dataText,
      dataParseError: false,
      eventType: dataText === '[DONE]' ? '[DONE]' : eventName
    }
  }
  try {
    const data = JSON.parse(dataText) as Record<string, unknown>
    const eventType = typeof data.type === 'string' ? data.type : eventName
    const error = extractEventError(data)
    return {
      rawText,
      eventName,
      dataText,
      data,
      dataParseError: false,
      eventType,
      errorCode: typeof error?.code === 'string' ? error.code : undefined,
      errorMessage: typeof error?.message === 'string' ? error.message : undefined
    }
  } catch {
    return {
      rawText,
      eventName,
      dataText,
      dataParseError: true,
      eventType: eventName
    }
  }
}

function extractEventError(data: Record<string, unknown>): Record<string, unknown> | undefined {
  const response = objectValue(data.response)
  const responseError = objectValue(response?.error)
  if (responseError) return responseError
  return objectValue(data.error)
}

function isVisibleOutputEvent(event: ParsedSseEvent): boolean {
  const data = event.data
  if (!data) return false
  if (event.eventType.endsWith('.delta') && hasMeaningfulDelta(data.delta)) return true
  if (event.eventType === 'response.output_item.added' || event.eventType === 'response.output_item.done') return true
  const choices = Array.isArray(data.choices) ? data.choices : []
  return choices.some((choice) => {
    if (typeof choice !== 'object' || choice === null) return false
    const row = choice as Record<string, unknown>
    if (hasNonEmptyString(row.text)) return true
    const delta = objectValue(row.delta)
    return Boolean(delta && hasMeaningfulDelta(delta))
  })
}

function hasMeaningfulDelta(value: unknown): boolean {
  if (hasNonEmptyString(value)) return true
  if (Array.isArray(value)) return value.some(hasMeaningfulDelta)
  if (typeof value !== 'object' || value === null) return false
  return Object.entries(value as Record<string, unknown>)
    .some(([key, child]) => key !== 'index' && key !== 'type' && key !== 'id' && hasMeaningfulDelta(child))
}

function hasNonEmptyString(value: unknown): boolean {
  return typeof value === 'string' && value.length > 0
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
