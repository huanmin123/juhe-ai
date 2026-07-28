import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import {
  diagnosticResponseContext,
  type DiagnosticResponseContext
} from '../gateway/diagnostics/diagnostic-response-context.js'

export type AccountTestDiagnosticProtocol = 'openai' | 'anthropic' | 'gemini'

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

export function parseAccountTestUpstreamErrorCode(input: string | DiagnosticResponseContext): string | undefined {
  const context = diagnosticResponseContext(input)
  for (const payload of context.payloads) {
    const code = upstreamErrorCodeFromPayload(payload)
    if (code) return code
  }
  return undefined
}

export function parseAccountTestUpstreamMessage(
  input: string | DiagnosticResponseContext,
  protocol: AccountTestDiagnosticProtocol,
  options: { rawFallback?: boolean } = {}
): string | undefined {
  const context = diagnosticResponseContext(input)
  for (const payload of context.payloads) {
    const message = protocolMessage(payload, protocol)
    if (message) return message
  }
  const streamFailure = parseAccountTestStreamFailureMessage(context, protocol)
  if (streamFailure) return streamFailure
  return options.rawFallback && context.bodyText ? context.bodyText.slice(0, 240) : undefined
}

export function parseAccountTestStreamFailureMessage(
  input: string | DiagnosticResponseContext,
  protocol: AccountTestDiagnosticProtocol
): string | undefined {
  const context = diagnosticResponseContext(input)
  if (protocol === 'anthropic') {
    const errorEvent = context.events.find((event) => event.event === 'error')
    return errorEvent ? protocolMessage(errorEvent.json, protocol) ?? 'Anthropic 流式响应失败' : undefined
  }
  if (protocol === 'gemini') {
    return context.events.length
      ? firstPayloadMessage(context.payloads, protocol)
      : undefined
  }
  for (const event of context.events) {
    const type = stringValue(event.json?.type) || event.event
    if (type !== 'response.failed' && type !== 'response.incomplete' && type !== 'error') continue
    return openAIErrorMessage(event.json?.error)
      || openAIErrorMessage(objectValue(event.json?.response)?.error)
      || openAIErrorMessage(event.json)
      || type
  }
  return undefined
}

export function extractAccountTestResponseOutputText(
  input: string | DiagnosticResponseContext,
  protocol: AccountTestDiagnosticProtocol
): string | undefined {
  const context = diagnosticResponseContext(input)
  if (protocol === 'anthropic') return extractAnthropicOutputText(context)
  if (protocol === 'gemini') return joinedText(context.payloads.flatMap(geminiCandidateTexts))
  return extractOpenAIOutputText(context)
}

function upstreamErrorCodeFromPayload(payload: Record<string, unknown>): string | undefined {
  const response = objectValue(payload.response)
  const error = objectValue(payload.error) ?? objectValue(response?.error) ?? payload
  return stringValue(error.code) || stringValue(error.type) || undefined
}

function protocolMessage(payload: Record<string, unknown> | undefined, protocol: AccountTestDiagnosticProtocol): string | undefined {
  if (!payload) return undefined
  if (protocol === 'anthropic' || protocol === 'gemini') {
    const error = objectValue(payload.error)
    return stringValue(error?.message) || stringValue(error?.status) || stringValue(payload.message) || undefined
  }
  const error = objectValue(payload.error) ?? objectValue(objectValue(payload.response)?.error)
  return openAIErrorMessage(error) || stringValue(payload.message) || undefined
}

function firstPayloadMessage(payloads: readonly Record<string, unknown>[], protocol: AccountTestDiagnosticProtocol): string | undefined {
  for (const payload of payloads) {
    const message = protocolMessage(payload, protocol)
    if (message) return message
  }
  return undefined
}

function extractOpenAIOutputText(context: DiagnosticResponseContext): string | undefined {
  const direct = extractOpenAIResponsePayloadText(context.record) || extractOpenAIChatPayloadText(context.record, 'message')
  if (direct) return direct
  const chunks: string[] = []
  for (const event of context.events) {
    const payload = event.json
    const type = stringValue(payload?.type) || event.event
    if (type === 'response.output_text.delta' || type === 'response.refusal.delta') {
      const delta = stringValue(payload?.delta)
      if (delta) chunks.push(delta)
    }
    if (type === 'response.output_text.done') {
      const text = stringValue(payload?.text)
      if (text) return text
    }
    if (type === 'response.completed' || type === 'response.done') {
      const text = extractOpenAIResponsePayloadText(objectValue(payload?.response))
      if (text) return text
    }
    const chatText = extractOpenAIChatPayloadText(payload, 'delta')
    if (chatText) chunks.push(chatText)
  }
  return joinedText(chunks)
}

function extractOpenAIResponsePayloadText(payload: Record<string, unknown> | undefined): string | undefined {
  const direct = stringValue(payload?.output_text)
  if (direct) return direct
  const output = Array.isArray(payload?.output) ? payload.output : []
  return joinedText(output.flatMap((item) => {
    const content = objectValue(item)?.content
    return Array.isArray(content)
      ? content.map((entry) => stringValue(objectValue(entry)?.text)).filter(Boolean)
      : []
  }))
}

function extractOpenAIChatPayloadText(payload: Record<string, unknown> | undefined, field: 'message' | 'delta'): string | undefined {
  const choices = Array.isArray(payload?.choices) ? payload.choices : []
  return joinedText(choices.map((choice) => {
    const container = objectValue(objectValue(choice)?.[field])
    return stringValue(container?.content) || stringValue(container?.reasoning_content) || stringValue(container?.refusal)
  }))
}

function extractAnthropicOutputText(context: DiagnosticResponseContext): string | undefined {
  const content = Array.isArray(context.record?.content) ? context.record.content : []
  const direct = joinedText(content.map((item) => stringValue(objectValue(item)?.text)))
  if (direct) return direct
  return joinedText(context.payloads.map((payload) => stringValue(objectValue(payload.delta)?.text)))
}

function geminiCandidateTexts(payload: Record<string, unknown>): string[] {
  const candidates = Array.isArray(payload.candidates) ? payload.candidates : []
  return candidates.flatMap((candidate) => {
    const parts = objectValue(objectValue(candidate)?.content)?.parts
    return Array.isArray(parts)
      ? parts.map((part) => stringValue(objectValue(part)?.text)).filter(Boolean)
      : []
  })
}

function openAIErrorMessage(value: unknown): string | undefined {
  if (typeof value === 'string') return value.trim() || undefined
  const record = objectValue(value)
  return stringValue(record?.message) || stringValue(record?.code) || stringValue(record?.type) || undefined
}

function joinedText(parts: string[]): string | undefined {
  const text = parts.filter(Boolean).join('').trim()
  return text || undefined
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
