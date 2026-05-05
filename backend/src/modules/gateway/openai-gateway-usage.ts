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
  requestId: string
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
}

interface OpenAIStreamInspection {
  terminalReceived: boolean
  failedReceived: boolean
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

export function buildUsageRequestSnapshot(req: Request, requestId: string, clientIp?: string): UsageRequestSnapshot {
  const snapshot: UsageRequestSnapshot = {
    method: req.method,
    path: req.path,
    originalUrl: req.originalUrl,
    clientIp,
    requestId,
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

export function parseOpenAIUsageFromSseText(text: string): ParsedUsage {
  return inspectOpenAIStreamText(text).usage
}

export function inspectOpenAIStreamText(text: string): OpenAIStreamInspection {
  const inspection: OpenAIStreamInspection = {
    terminalReceived: false,
    failedReceived: false,
    usage: emptyUsage()
  }
  let eventName = ''
  let dataLines: string[] = []

  const flushEvent = () => {
    if (dataLines.length === 0) {
      eventName = ''
      return
    }
    const currentEventName = eventName
    const data = dataLines.join('\n').trim()
    eventName = ''
    dataLines = []
    if (!data) return
    if (data === '[DONE]') {
      inspection.terminalReceived = true
      return
    }
    try {
      const event = JSON.parse(data) as Record<string, unknown>
      const eventType = typeof event.type === 'string' ? event.type : currentEventName
      if (eventType === 'response.completed' || eventType === 'response.done') {
        inspection.terminalReceived = true
      } else if (eventType === 'response.failed') {
        inspection.terminalReceived = true
        inspection.failedReceived = true
        const error = typeof event.response === 'object' && event.response !== null
          && typeof (event.response as Record<string, unknown>).error === 'object'
          && (event.response as Record<string, unknown>).error !== null
          ? (event.response as Record<string, unknown>).error as Record<string, unknown>
          : typeof event.error === 'object' && event.error !== null
            ? event.error as Record<string, unknown>
            : undefined
        inspection.errorCode = typeof error?.code === 'string' ? error.code : inspection.errorCode
        inspection.errorMessage = typeof error?.message === 'string' ? error.message : inspection.errorMessage
      }
      if (eventType === 'response.completed' || eventType === 'response.done' || eventType === 'response.failed') {
        const nextUsage = extractUsage(typeof event.response === 'object' && event.response !== null ? (event.response as Record<string, unknown>).usage : event.usage)
        if (nextUsage.inputTokens !== undefined || nextUsage.outputTokens !== undefined || nextUsage.cacheReadTokens !== undefined) {
          inspection.usage = nextUsage
        }
      }
    } catch {
    }
  }

  for (const line of text.split(/\r?\n/)) {
    if (line === '') {
      flushEvent()
      continue
    }
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  flushEvent()

  return inspection
}

function firstHeaderValue(value?: string): string | undefined {
  return value?.split(',').map((item) => item.trim()).find(Boolean)
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
  const details = typeof usage.input_tokens_details === 'object' && usage.input_tokens_details !== null
    ? usage.input_tokens_details as Record<string, unknown>
    : {}
  const inputTokens = numberValue(usage.input_tokens)
  const outputTokens = numberValue(usage.output_tokens)
  const cacheReadTokens = numberValue(details.cached_tokens)
  return { inputTokens, outputTokens, cacheReadTokens }
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : undefined
}
