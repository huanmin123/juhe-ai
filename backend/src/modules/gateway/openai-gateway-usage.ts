import type { IncomingHttpHeaders } from 'node:http'
import type { Request } from 'express'

export interface UpstreamAttempt {
  accountId: string
  accountName: string
  upstreamUrl: string
  status?: number
  message?: string
  responseHeaders?: Record<string, string>
  responseBodyText?: string
}

export interface UsageRequestSnapshot {
  method: string
  path: string
  originalUrl: string
  clientIp?: string
  traceId: string
  headers: Record<string, string | string[]>
  body?: unknown
}

export interface UsageResponseSnapshot {
  upstreamUrl?: string
  statusCode?: number
  headers?: Record<string, string>
  bodyText?: string
  errorMessage?: string
  generatedBy?: 'gateway'
  lastUpstreamAttempt?: {
    accountId: string
    accountName: string
    upstreamUrl: string
    statusCode?: number
    headers?: Record<string, string>
    bodyText?: string
    errorMessage?: string
  }
}

export interface ParsedUsage {
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
}

export interface OpenAIStreamInspection {
  terminalReceived: boolean
  failedReceived: boolean
  outputReceived: boolean
  outputEventCount: number
  skipped: boolean
  skipReason?: string
  errorCode?: string
  errorMessage?: string
  usage: ParsedUsage
}

export function extractBearerToken(authorization?: string): string | undefined {
  if (!authorization) return undefined
  const match = authorization.match(/^Bearer\s+(.+)$/i)
  return match?.[1]?.trim()
}

export function extractClientIp(req: Request): string | undefined {
  const forwarded = firstHeaderValue(req.header('x-forwarded-for'))
  const realIp = firstHeaderValue(req.header('x-real-ip'))
  const cfIp = firstHeaderValue(req.header('cf-connecting-ip'))
  return normalizeClientIp(forwarded ?? realIp ?? cfIp ?? req.ip ?? req.socket.remoteAddress)
}

export function requestModel(req: Request): string | undefined {
  return typeof req.body?.model === 'string' ? req.body.model : undefined
}

export function requestEndpoint(req: Request): string {
  return `${req.method.toUpperCase()} ${req.originalUrl.split('?')[0] || req.path}`
}

export function buildUsageRequestSnapshot(req: Request, traceId: string, clientIp?: string): UsageRequestSnapshot {
  const snapshot: UsageRequestSnapshot = {
    method: req.method,
    path: req.path,
    originalUrl: req.originalUrl,
    clientIp,
    traceId,
    headers: sanitizeRequestHeaders(req.headers)
  }
  if (req.body !== undefined) {
    snapshot.body = req.body
  }
  return snapshot
}

export function sanitizeRequestHeaders(headers: IncomingHttpHeaders): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  const hidden = new Set(['authorization', 'proxy-authorization', 'cookie', 'set-cookie', 'x-api-key'])
  for (const [name, value] of Object.entries(headers)) {
    if (value === undefined) continue
    output[name] = hidden.has(name.toLowerCase()) ? '[redacted]' : value
  }
  return output
}

export function buildUsageResponseSnapshot(input: {
  upstreamUrl?: string
  statusCode?: number
  headers?: Headers | Record<string, string>
  bodyText?: string
  errorMessage?: string
  generatedBy?: 'gateway'
}): UsageResponseSnapshot {
  return {
    upstreamUrl: input.upstreamUrl,
    statusCode: input.statusCode,
    headers: input.headers instanceof Headers ? headersToObject(input.headers) : input.headers,
    bodyText: input.bodyText,
    errorMessage: input.errorMessage,
    generatedBy: input.generatedBy
  }
}

export function buildGatewayErrorResponseSnapshot(
  statusCode: number,
  payload: Record<string, unknown>,
  lastAttempt?: UpstreamAttempt
): UsageResponseSnapshot {
  const snapshot = buildUsageResponseSnapshot({
    statusCode,
    headers: { 'content-type': 'application/json; charset=utf-8' },
    bodyText: JSON.stringify(payload),
    errorMessage: typeof payload.error === 'object' && payload.error !== null
      ? String((payload.error as Record<string, unknown>).message ?? '')
      : undefined,
    generatedBy: 'gateway'
  })

  if (lastAttempt) {
    snapshot.lastUpstreamAttempt = {
      accountId: lastAttempt.accountId,
      accountName: lastAttempt.accountName,
      upstreamUrl: lastAttempt.upstreamUrl,
      statusCode: lastAttempt.status,
      headers: lastAttempt.responseHeaders,
      bodyText: lastAttempt.responseBodyText,
      errorMessage: lastAttempt.message
    }
  }

  return snapshot
}

export function headersToObject(headers: Headers): Record<string, string> {
  const output: Record<string, string> = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
}

export function emptyUsage(): ParsedUsage {
  return {}
}

export function parseOpenAIUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  try {
    const payload = JSON.parse(responseBody.toString('utf8')) as Record<string, unknown>
    return extractUsage(payload.usage)
  } catch {
    return emptyUsage()
  }
}

export function parseOpenAIUsageFromJsonTextFragment(text?: string): ParsedUsage {
  if (!text) return emptyUsage()
  const usageText = extractJsonObjectPropertyFromTextFragment(text, 'usage')
  if (!usageText) return emptyUsage()
  try {
    return extractUsage(JSON.parse(usageText))
  } catch {
    return emptyUsage()
  }
}

export function parseOpenAIUsageFromSseText(text: string): ParsedUsage {
  return inspectOpenAIStreamText(text).usage
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
    skipped: false,
    usage: emptyUsage()
  }
  private eventName = ''
  private dataLines: string[] = []
  private dataBytes = 0
  private pendingLine = ''

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
      usage: { ...this.inspection.usage }
    }
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
    const data = this.dataLines.join('\n').trim()
    this.eventName = ''
    this.dataLines = []
    this.dataBytes = 0
    if (!data) return
    if (data === '[DONE]') {
      this.inspection.terminalReceived = true
      return
    }
    try {
      const event = JSON.parse(data) as Record<string, unknown>
      const eventType = typeof event.type === 'string' ? event.type : currentEventName
      if (openAIStreamEventHasOutput(event, eventType)) {
        this.inspection.outputReceived = true
        this.inspection.outputEventCount += 1
      }
      if (eventType === 'response.completed' || eventType === 'response.done') {
        this.inspection.terminalReceived = true
      } else if (eventType === 'response.failed') {
        this.inspection.terminalReceived = true
        this.inspection.failedReceived = true
        const error = typeof event.response === 'object' && event.response !== null
          && typeof (event.response as Record<string, unknown>).error === 'object'
          && (event.response as Record<string, unknown>).error !== null
          ? (event.response as Record<string, unknown>).error as Record<string, unknown>
          : typeof event.error === 'object' && event.error !== null
            ? event.error as Record<string, unknown>
            : undefined
        this.inspection.errorCode = typeof error?.code === 'string' ? error.code : this.inspection.errorCode
        this.inspection.errorMessage = typeof error?.message === 'string' ? error.message : this.inspection.errorMessage
      }
      const nextUsage = extractEventUsage(event)
      if (hasAnyUsageValue(nextUsage)) {
        this.inspection.usage = mergeUsage(this.inspection.usage, nextUsage)
      }
    } catch {
    }
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
}

const streamInspectorMaxLineBytes = 256 * 1024
const streamInspectorMaxEventBytes = 512 * 1024

export function isOpenAIStreamServerOverloadedErrorCode(code?: string): boolean {
  return code === 'server_is_overloaded' || code === 'slow_down'
}

function openAIStreamEventHasOutput(event: Record<string, unknown>, eventType: string): boolean {
  if (eventType.endsWith('.delta') && hasMeaningfulDelta(event.delta)) {
    return true
  }
  const choices = Array.isArray(event.choices) ? event.choices : []
  return choices.some((choice) => {
    if (typeof choice !== 'object' || choice === null) return false
    const row = choice as Record<string, unknown>
    if (hasNonEmptyString(row.text)) return true
    const delta = typeof row.delta === 'object' && row.delta !== null ? row.delta as Record<string, unknown> : undefined
    return Boolean(delta && hasMeaningfulChoiceDelta(delta))
  })
}

function hasMeaningfulChoiceDelta(delta: Record<string, unknown>): boolean {
  return hasMeaningfulDelta(delta.content)
    || hasMeaningfulDelta(delta.refusal)
    || hasMeaningfulDelta(delta.reasoning_content)
    || hasMeaningfulDelta(delta.audio)
    || hasMeaningfulDelta(delta.tool_calls)
    || hasMeaningfulDelta(delta.function_call)
}

function hasMeaningfulDelta(value: unknown): boolean {
  if (hasNonEmptyString(value)) return true
  if (Array.isArray(value)) return value.some(hasMeaningfulDelta)
  if (typeof value !== 'object' || value === null) return false
  return Object.entries(value as Record<string, unknown>)
    .some(([key, child]) => key !== 'index' && key !== 'type' && key !== 'id' && hasMeaningfulDelta(child))
}

function firstHeaderValue(value?: string): string | undefined {
  return value?.split(',').map((item) => item.trim()).find(Boolean)
}

function extractJsonObjectPropertyFromTextFragment(text: string, propertyName: string): string | undefined {
  const token = `"${propertyName}"`
  let searchFrom = text.length
  while (searchFrom > 0) {
    const tokenIndex = text.lastIndexOf(token, searchFrom - 1)
    if (tokenIndex < 0) {
      return undefined
    }
    let cursor = tokenIndex + token.length
    cursor = skipJsonWhitespace(text, cursor)
    if (text[cursor] !== ':') {
      searchFrom = tokenIndex
      continue
    }
    cursor = skipJsonWhitespace(text, cursor + 1)
    if (text[cursor] !== '{') {
      searchFrom = tokenIndex
      continue
    }
    const objectText = extractJsonObjectAt(text, cursor)
    if (objectText) {
      return objectText
    }
    searchFrom = tokenIndex
  }
  return undefined
}

function extractJsonObjectAt(text: string, startIndex: number): string | undefined {
  let depth = 0
  let inString = false
  let escaping = false
  for (let index = startIndex; index < text.length; index += 1) {
    const char = text[index]
    if (inString) {
      if (escaping) {
        escaping = false
      } else if (char === '\\') {
        escaping = true
      } else if (char === '"') {
        inString = false
      }
      continue
    }

    if (char === '"') {
      inString = true
    } else if (char === '{') {
      depth += 1
    } else if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return text.slice(startIndex, index + 1)
      }
    }
  }
  return undefined
}

function skipJsonWhitespace(text: string, startIndex: number): number {
  let index = startIndex
  while (index < text.length && /\s/.test(text[index])) {
    index += 1
  }
  return index
}

function normalizeClientIp(value?: string): string | undefined {
  if (!value) return undefined
  let ip = value.trim()
  if (!ip) return undefined
  if (ip.startsWith('[')) {
    const end = ip.indexOf(']')
    if (end > 0) ip = ip.slice(1, end)
  }
  if (/^\d{1,3}(?:\.\d{1,3}){3}:\d+$/.test(ip)) {
    ip = ip.replace(/:\d+$/, '')
  }
  if (ip.startsWith('::ffff:')) {
    ip = ip.slice('::ffff:'.length)
  }
  return ip === '::1' ? '127.0.0.1' : ip
}

function extractUsage(value: unknown): ParsedUsage {
  if (typeof value !== 'object' || value === null) return emptyUsage()
  const usage = value as Record<string, unknown>
  const responsesInputDetails = objectValue(usage.input_tokens_details)
  const chatInputDetails = objectValue(usage.prompt_tokens_details)
  const inputTokens = numberValue(usage.input_tokens) ?? numberValue(usage.prompt_tokens)
  const outputTokens = numberValue(usage.output_tokens) ?? numberValue(usage.completion_tokens)
  const cacheReadTokens = numberValue(responsesInputDetails?.cached_tokens)
    ?? numberValue(chatInputDetails?.cached_tokens)
  const outputDetails = objectValue(usage.output_tokens_details) ?? objectValue(usage.completion_tokens_details)
  const inputImageTokens = numberValue(responsesInputDetails?.image_tokens)
    ?? numberValue(chatInputDetails?.image_tokens)
  const outputImageTokens = numberValue(outputDetails?.image_tokens)
  return { inputTokens, outputTokens, cacheReadTokens, inputImageTokens, outputImageTokens }
}

function extractEventUsage(event: Record<string, unknown>): ParsedUsage {
  const response = objectValue(event.response)
  return extractUsage(response?.usage ?? event.usage)
}

function mergeUsage(current: ParsedUsage, next: ParsedUsage): ParsedUsage {
  return {
    inputTokens: next.inputTokens ?? current.inputTokens,
    outputTokens: next.outputTokens ?? current.outputTokens,
    cacheReadTokens: next.cacheReadTokens ?? current.cacheReadTokens,
    inputImageTokens: next.inputImageTokens ?? current.inputImageTokens,
    outputImageTokens: next.outputImageTokens ?? current.outputImageTokens
  }
}

function hasAnyUsageValue(value: ParsedUsage): boolean {
  return value.inputTokens !== undefined
    || value.outputTokens !== undefined
    || value.cacheReadTokens !== undefined
    || value.inputImageTokens !== undefined
    || value.outputImageTokens !== undefined
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null ? value as Record<string, unknown> : undefined
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : undefined
}

function hasNonEmptyString(value: unknown): boolean {
  return typeof value === 'string' && value.length > 0
}
