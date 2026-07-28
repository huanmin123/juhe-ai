import {
  diagnosticResponseContextFromGatewayResponse,
  type DiagnosticResponseContext,
  type DiagnosticResponseParseOptions,
  type DiagnosticSseEvent
} from '../gateway/diagnostics/diagnostic-response-context.js'
import type { GatewayNonStreamJsonBody } from '../gateway/response/non-stream-json-body.js'
import type { ParsedOpenAIStreamEvent } from '../gateway/protocols/openai-v1/stream-events.js'
import type { ModelCheckProbeProtocol } from './model-checks.profiles.js'
import { recordValue, textValue } from './model-checks-parsing.js'

export interface ParsedModelCheckProbeResponse {
  json?: Record<string, unknown>
  outputText?: string
  model?: string
  usage?: Record<string, unknown>
  systemFingerprint?: string
  errorMessage?: string
  streamFailureMessage?: string
}

export function isSuccessfulModelCheckProbeResponse(
  statusCode: number,
  parsed: Pick<ParsedModelCheckProbeResponse, 'errorMessage' | 'streamFailureMessage'>
): boolean {
  return statusCode >= 200
    && statusCode < 300
    && !parsed.errorMessage
    && !parsed.streamFailureMessage
}

export function parseModelCheckProbeResponse(input: {
  bodyText: string
  protocol: ModelCheckProbeProtocol
  path: string
  parseOptions?: DiagnosticResponseParseOptions
  parsedNonStreamJsonBody?: GatewayNonStreamJsonBody
  parsedStreamEvents?: readonly ParsedOpenAIStreamEvent[]
}): ParsedModelCheckProbeResponse {
  const context = diagnosticResponseContextFromGatewayResponse(
    input.bodyText,
    input.parsedNonStreamJsonBody,
    input.parsedStreamEvents,
    input.parseOptions
  )
  if (input.protocol === 'openai_responses') return parseOpenAIResponsesProbeResponse(context)
  if (input.protocol === 'openai_chat') return parseOpenAIChatProbeResponse(context)
  if (input.protocol === 'anthropic_messages') return parseAnthropicMessagesProbeResponse(context)
  return parseGeminiNativeProbeResponse(context)
}

function parseOpenAIResponsesProbeResponse(context: DiagnosticResponseContext): ParsedModelCheckProbeResponse {
  const streamFailureMessage = parseOpenAIStreamFailureMessage(context)
  return {
    json: context.record,
    outputText: extractOpenAIResponsesOutputText(context),
    model: textValue(context.record?.model) ?? firstText(context.payloads.map((payload) => recordValue(payload.response)?.model)),
    usage: recordValue(context.record?.usage) ?? firstRecord(context.payloads.map((payload) => recordValue(payload.response)?.usage)),
    systemFingerprint: textValue(context.record?.system_fingerprint),
    errorMessage: parseUpstreamMessage(context) ?? streamFailureMessage,
    streamFailureMessage
  }
}

function parseOpenAIChatProbeResponse(context: DiagnosticResponseContext): ParsedModelCheckProbeResponse {
  const streamFailureMessage = parseGenericStreamFailureMessage(context.events)
  return {
    json: context.record,
    outputText: extractOpenAIChatOutputText(context),
    model: textValue(context.record?.model) ?? firstText(context.payloads.map((payload) => payload.model)),
    usage: recordValue(context.record?.usage) ?? firstRecord(context.payloads.map((payload) => payload.usage)),
    errorMessage: parseUpstreamMessage(context) ?? streamFailureMessage,
    streamFailureMessage
  }
}

function parseAnthropicMessagesProbeResponse(context: DiagnosticResponseContext): ParsedModelCheckProbeResponse {
  const streamFailureMessage = parseAnthropicStreamFailureMessage(context)
  return {
    json: context.record,
    outputText: extractAnthropicOutputText(context),
    model: textValue(context.record?.model)
      ?? firstText(context.payloads.map((payload) => recordValue(payload.message)?.model)),
    usage: recordValue(context.record?.usage)
      ?? firstRecord(context.payloads.map((payload) => payload.usage))
      ?? firstRecord(context.payloads.map((payload) => recordValue(payload.message)?.usage)),
    errorMessage: parseAnthropicMessage(context) ?? parseUpstreamMessage(context) ?? streamFailureMessage,
    streamFailureMessage
  }
}

function parseGeminiNativeProbeResponse(context: DiagnosticResponseContext): ParsedModelCheckProbeResponse {
  const streamFailureMessage = parseGenericStreamFailureMessage(context.events)
  return {
    json: context.record,
    outputText: extractGeminiOutputText(context),
    model: textValue(context.record?.model)
      ?? textValue(context.record?.modelVersion)
      ?? firstText(context.payloads.map((payload) => payload.modelVersion)),
    usage: recordValue(context.record?.usageMetadata)
      ?? firstRecord(context.payloads.map((payload) => payload.usageMetadata)),
    errorMessage: parseGeminiMessage(context) ?? parseUpstreamMessage(context) ?? streamFailureMessage,
    streamFailureMessage
  }
}

function extractOpenAIResponsesOutputText(context: DiagnosticResponseContext): string | undefined {
  const direct = extractOpenAIResponsePayloadText(context.record)
  if (direct) return direct
  const chunks: string[] = []
  for (const event of context.events) {
    const payload = event.json
    const type = textValue(payload?.type) ?? event.event
    if (type === 'response.output_text.delta' || type === 'response.refusal.delta') {
      const delta = textValue(payload?.delta)
      if (delta) chunks.push(delta)
    }
    if (type === 'response.output_text.done') {
      const text = textValue(payload?.text)
      if (text) return text
    }
    if (type === 'response.completed' || type === 'response.done') {
      const text = extractOpenAIResponsePayloadText(recordValue(payload?.response))
      if (text) return text
    }
  }
  return joinedText(chunks)
}

function extractOpenAIResponsePayloadText(payload: Record<string, unknown> | undefined): string | undefined {
  const direct = textValue(payload?.output_text)
  if (direct) return direct
  const output = Array.isArray(payload?.output) ? payload.output : []
  return joinedText(output.flatMap((item) => {
    const content = recordValue(item)?.content
    return Array.isArray(content)
      ? content.map((entry) => textValue(recordValue(entry)?.text))
      : []
  }))
}

function extractOpenAIChatOutputText(context: DiagnosticResponseContext): string | undefined {
  const direct = extractOpenAIChatChoicesText(context.record, 'message')
  if (direct) return direct
  return joinedText(context.payloads.map((payload) => extractOpenAIChatChoicesText(payload, 'delta')))
}

function extractOpenAIChatChoicesText(payload: Record<string, unknown> | undefined, field: 'message' | 'delta'): string | undefined {
  const choices = Array.isArray(payload?.choices) ? payload.choices : []
  return joinedText(choices.map((choice) => {
    const container = recordValue(recordValue(choice)?.[field])
    return openAITextValue(container?.content)
      ?? openAITextValue(container?.reasoning_content)
      ?? openAITextValue(container?.refusal)
  }))
}

function extractAnthropicOutputText(context: DiagnosticResponseContext): string | undefined {
  const content = Array.isArray(context.record?.content) ? context.record.content : []
  const direct = joinedText(content.map((item) => textValue(recordValue(item)?.text)))
  if (direct) return direct
  return joinedText(context.payloads.map((payload) => textValue(recordValue(payload.delta)?.text)))
}

function extractGeminiOutputText(context: DiagnosticResponseContext): string | undefined {
  return joinedText(context.payloads.map(extractGeminiCandidateText))
}

function extractGeminiCandidateText(payload: Record<string, unknown>): string | undefined {
  const candidates = Array.isArray(payload.candidates) ? payload.candidates : []
  const parts: Array<string | undefined> = []
  for (const candidate of candidates) {
    const content = recordValue(recordValue(candidate)?.content)
    const contentParts = Array.isArray(content?.parts) ? content.parts : []
    for (const part of contentParts) parts.push(textValue(recordValue(part)?.text))
  }
  return joinedText(parts)
}

function parseUpstreamMessage(context: DiagnosticResponseContext): string | undefined {
  for (const payload of context.payloads) {
    const error = recordValue(payload.error) ?? recordValue(recordValue(payload.response)?.error)
    const message = parseErrorMessage(error) ?? textValue(payload.message)
    if (message) return message
  }
  return undefined
}

function parseAnthropicMessage(context: DiagnosticResponseContext): string | undefined {
  for (const payload of context.payloads) {
    const message = parseAnthropicPayloadMessage(payload)
    if (message) return message
  }
  return undefined
}

function parseGeminiMessage(context: DiagnosticResponseContext): string | undefined {
  for (const payload of context.payloads) {
    const error = recordValue(payload.error)
    const message = textValue(error?.message) ?? textValue(error?.status) ?? textValue(payload.message)
    if (message) return message
  }
  return undefined
}

function parseOpenAIStreamFailureMessage(context: DiagnosticResponseContext): string | undefined {
  for (const event of context.events) {
    const type = textValue(event.json?.type) ?? event.event
    if (type !== 'response.failed' && type !== 'response.incomplete' && type !== 'error') continue
    return parseErrorMessage(event.json?.error)
      ?? parseErrorMessage(recordValue(event.json?.response)?.error)
      ?? parseErrorMessage(event.json)
      ?? type
  }
  return undefined
}

function parseAnthropicStreamFailureMessage(context: DiagnosticResponseContext): string | undefined {
  const event = context.events.find((item) => item.event === 'error')
  return event ? parseAnthropicPayloadMessage(event.json) ?? 'Anthropic 流式响应失败' : undefined
}

function parseAnthropicPayloadMessage(payload: Record<string, unknown> | undefined): string | undefined {
  return textValue(recordValue(payload?.error)?.message) ?? textValue(payload?.message)
}

function parseGenericStreamFailureMessage(events: readonly DiagnosticSseEvent[]): string | undefined {
  for (const event of events) {
    const error = recordValue(event.json?.error)
    const message = textValue(error?.message) ?? textValue(error?.code) ?? textValue(event.json?.message)
    if (message) return message
  }
  return undefined
}

function parseErrorMessage(value: unknown): string | undefined {
  if (typeof value === 'string') return value.trim() || undefined
  const record = recordValue(value)
  return textValue(record?.message) ?? textValue(record?.code) ?? textValue(record?.type)
}

function openAITextValue(value: unknown): string | undefined {
  if (typeof value === 'string') return value.trim() || undefined
  if (!Array.isArray(value)) return undefined
  return joinedText(value.map((item) => textValue(recordValue(item)?.text)))
}

function firstText(values: unknown[]): string | undefined {
  for (const value of values) {
    const text = textValue(value)
    if (text) return text
  }
  return undefined
}

function firstRecord(values: unknown[]): Record<string, unknown> | undefined {
  for (const value of values) {
    const record = recordValue(value)
    if (record) return record
  }
  return undefined
}

function joinedText(parts: Array<string | undefined>): string | undefined {
  const text = parts.filter((item): item is string => Boolean(item)).join('').trim()
  return text || undefined
}
