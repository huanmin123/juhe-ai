import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { parseOpenAISseEvents } from '../gateway/protocols/openai-v1/response-parsing.js'

export function resolveAccountTestResponseDiagnostics(input: {
  downstreamResponseText: string
  downstreamResponseHeaders: Record<string, string | string[]>
  downstreamResponseTruncated: boolean
  upstreamAttempt?: UpstreamAttempt
}): {
  responseText: string
  responseHeaders: Record<string, string | string[]>
  responseTruncated: boolean
} {
  const upstreamResponseText = input.upstreamAttempt?.responseBodyText ?? ''
  if (!upstreamResponseText.trim()) {
    return {
      responseText: input.downstreamResponseText,
      responseHeaders: input.downstreamResponseHeaders,
      responseTruncated: input.downstreamResponseTruncated
    }
  }
  return {
    responseText: upstreamResponseText,
    responseHeaders: input.upstreamAttempt?.responseHeaders ?? input.downstreamResponseHeaders,
    responseTruncated: upstreamResponseText.endsWith('\n[truncated]')
  }
}

export function redactAccountTestImageResponse(bodyText: string): unknown {
  try {
    return redactImageFields(JSON.parse(bodyText) as unknown)
  } catch {
    return { response: '已省略' }
  }
}

function redactImageFields(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redactImageFields)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([key, item]) => [
    key,
    isImageResponseField(key) ? '已省略' : redactImageFields(item)
  ]))
}

function isImageResponseField(key: string): boolean {
  const normalized = key.toLowerCase().replace(/[^a-z0-9]/g, '')
  return normalized === 'b64json' || normalized === 'url' || normalized === 'imageurl'
}

export function parseAccountTestUpstreamErrorCode(bodyText: string): string | undefined {
  if (!bodyText) return undefined
  try {
    return upstreamErrorCodeFromPayload(JSON.parse(bodyText) as Record<string, unknown>)
  } catch {
  }
  for (const payload of parseOpenAISseEvents(bodyText)) {
    const code = upstreamErrorCodeFromPayload(payload)
    if (code) return code
  }
  return undefined
}

function upstreamErrorCodeFromPayload(payload: Record<string, unknown>): string | undefined {
  const response = objectValue(payload.response)
  const error = objectValue(payload.error) ?? objectValue(response?.error) ?? payload
  return stringValue(error.code) || stringValue(error.type) || undefined
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
