import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { parseOpenAISseEvents } from '../gateway/protocols/openai-v1/response-parsing.js'

export function resolveAccountTestResponseDiagnostics(input: {
  downstreamResponseText: string
  downstreamResponseHeaders: Record<string, string | string[]>
  upstreamAttempt?: UpstreamAttempt
}): {
  responseText: string
  responseHeaders: Record<string, string | string[]>
} {
  const upstreamResponseText = input.upstreamAttempt?.responseBodyText ?? ''
  if (!upstreamResponseText.trim()) {
    return {
      responseText: input.downstreamResponseText,
      responseHeaders: input.downstreamResponseHeaders
    }
  }
  return {
    responseText: upstreamResponseText,
    responseHeaders: input.upstreamAttempt?.responseHeaders ?? input.downstreamResponseHeaders
  }
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
