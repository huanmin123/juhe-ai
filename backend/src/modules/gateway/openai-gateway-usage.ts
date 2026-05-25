import type { IncomingHttpHeaders } from 'node:http'
import type { Request } from 'express'

import { sanitizeUrlForLog } from '../../shared/request-context.js'
import {
  buildGatewayRequestBodySummary,
  getGatewayRequestBodyState
} from './openai-gateway-request-body.js'

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
  bodyOmission?: unknown
}

export interface UsageResponseSnapshot {
  upstreamUrl?: string
  statusCode?: number
  headers?: Record<string, string>
  bodyText?: string
  bodyOmission?: unknown
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
  const bodyState = getGatewayRequestBodyState(req)
  return bodyState?.model ?? (typeof req.body?.model === 'string' ? req.body.model : undefined)
}

export function requestStream(req: Request): boolean {
  const bodyState = getGatewayRequestBodyState(req)
  return bodyState?.stream ?? req.body?.stream === true
}

export function requestEndpoint(req: Request): string {
  return `${req.method.toUpperCase()} ${req.originalUrl.split('?')[0] || req.path}`
}

export function buildUsageRequestSnapshot(req: Request, traceId: string, clientIp?: string): UsageRequestSnapshot {
  const snapshot: UsageRequestSnapshot = {
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    clientIp,
    traceId,
    headers: sanitizeRequestHeaders(req.headers)
  }
  const bodySummary = buildGatewayRequestBodySummary(req)
  if (bodySummary) {
    snapshot.body = bodySummary
  } else if (req.body !== undefined) {
    snapshot.body = req.body
  }
  return snapshot
}

export function sanitizeRequestHeaders(headers: IncomingHttpHeaders): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  for (const [name, value] of Object.entries(headers)) {
    if (value === undefined) continue
    output[name] = sanitizeHeaderValue(name, value)
  }
  return output
}

export function buildUsageResponseSnapshot(input: {
  upstreamUrl?: string
  statusCode?: number
  headers?: Headers | Record<string, string>
  bodyText?: string
  bodyOmission?: unknown
  errorMessage?: string
  generatedBy?: 'gateway'
}): UsageResponseSnapshot {
  return {
    upstreamUrl: input.upstreamUrl,
    statusCode: input.statusCode,
    headers: input.headers instanceof Headers ? headersToSafeObject(input.headers) : input.headers ? sanitizeStringHeaderRecord(input.headers) : undefined,
    bodyText: input.bodyText,
    bodyOmission: input.bodyOmission,
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

export function headersToSafeObject(headers: Headers): Record<string, string> {
  const output: Record<string, string> = {}
  headers.forEach((value, name) => {
    output[name] = String(sanitizeHeaderValue(name, value))
  })
  return output
}

export function sanitizeHeaderRecord(headers: Record<string, string | string[]>): Record<string, string | string[]> {
  return Object.fromEntries(Object.entries(headers).map(([name, value]) => [name, sanitizeHeaderValue(name, value)]))
}

function sanitizeStringHeaderRecord(headers: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(headers).map(([name, value]) => [name, String(sanitizeHeaderValue(name, value))]))
}

export function sanitizeHeaderValue(name: string, value: string | string[]): string | string[] {
  if (!isSensitiveHeaderName(name)) {
    return value
  }
  return Array.isArray(value) ? value.map(() => '[redacted]') : '[redacted]'
}

export function isSensitiveHeaderName(name: string): boolean {
  const normalized = name.trim().toLowerCase()
  return sensitiveHeaderNames.has(normalized)
}

const sensitiveHeaderNames = new Set([
  'authorization',
  'proxy-authorization',
  'cookie',
  'set-cookie',
  'x-api-key',
  'api-key',
  'openai-api-key'
])

export function emptyUsage(): ParsedUsage {
  return {}
}

export function parseOpenAIUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  return parseOpenAIUsageFromJsonTextFragment(responseBody.toString('utf8'))
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

export function extractUsage(value: unknown): ParsedUsage {
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

export function mergeUsage(current: ParsedUsage, next: ParsedUsage): ParsedUsage {
  return {
    inputTokens: next.inputTokens ?? current.inputTokens,
    outputTokens: next.outputTokens ?? current.outputTokens,
    cacheReadTokens: next.cacheReadTokens ?? current.cacheReadTokens,
    inputImageTokens: next.inputImageTokens ?? current.inputImageTokens,
    outputImageTokens: next.outputImageTokens ?? current.outputImageTokens
  }
}

export function hasAnyUsageValue(value: ParsedUsage): boolean {
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
