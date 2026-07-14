import {
  extractAnthropicResponseOutputText,
  parseAnthropicStreamFailureMessage,
  parseAnthropicUpstreamMessage
} from '../gateway/protocols/anthropic-v1/response-parsing.js'
import type { ModelCheckProbeProtocol } from './model-checks.profiles.js'
import {
  extractOpenAIResponseOutputText,
  modelFromSse,
  parseJsonRecord,
  parseOpenAIStreamFailureMessage,
  parseUpstreamMessage,
  recordValue,
  textValue,
  usageFromSse
} from './model-checks-parsing.js'

export interface ParsedModelCheckProbeResponse {
  json?: Record<string, unknown>
  outputText?: string
  model?: string
  usage?: Record<string, unknown>
  systemFingerprint?: string
  errorMessage?: string
  streamFailureMessage?: string
}

export function parseModelCheckProbeResponse(input: {
  bodyText: string
  protocol: ModelCheckProbeProtocol
  path: string
}): ParsedModelCheckProbeResponse {
  if (input.protocol === 'openai_responses') {
    return parseOpenAIResponsesProbeResponse(input.bodyText)
  }
  if (input.protocol === 'openai_chat') {
    return parseOpenAIChatProbeResponse(input.bodyText)
  }
  if (input.protocol === 'anthropic_messages') {
    return parseAnthropicMessagesProbeResponse(input.bodyText)
  }
  return parseGeminiNativeProbeResponse(input.bodyText)
}

function parseOpenAIResponsesProbeResponse(bodyText: string): ParsedModelCheckProbeResponse {
  const json = parseJsonRecord(bodyText)
  return {
    json,
    outputText: extractOpenAIResponseOutputText(bodyText),
    model: textValue(json?.model) ?? modelFromSse(bodyText),
    usage: recordValue(json?.usage) ?? usageFromSse(bodyText),
    systemFingerprint: textValue(json?.system_fingerprint),
    errorMessage: parseUpstreamMessage(bodyText),
    streamFailureMessage: parseOpenAIStreamFailureMessage(bodyText)
  }
}

function parseOpenAIChatProbeResponse(bodyText: string): ParsedModelCheckProbeResponse {
  const json = parseJsonRecord(bodyText)
  const events = parseSseJsonEvents(bodyText)
  return {
    json,
    outputText: extractOpenAIChatOutputText(json, events),
    model: textValue(json?.model) ?? firstText(events.map((event) => event.json?.model)),
    usage: recordValue(json?.usage) ?? firstRecord(events.map((event) => event.json?.usage)),
    errorMessage: parseUpstreamMessage(bodyText),
    streamFailureMessage: parseGenericStreamFailureMessage(events)
  }
}

function parseAnthropicMessagesProbeResponse(bodyText: string): ParsedModelCheckProbeResponse {
  const json = parseJsonRecord(bodyText)
  const events = parseSseJsonEvents(bodyText)
  return {
    json,
    outputText: extractAnthropicResponseOutputText(bodyText),
    model: textValue(json?.model) ?? firstText(events.map((event) => recordValue(event.json?.message)?.model)),
    usage: recordValue(json?.usage)
      ?? firstRecord(events.map((event) => event.json?.usage))
      ?? firstRecord(events.map((event) => recordValue(event.json?.message)?.usage)),
    errorMessage: parseAnthropicUpstreamMessage(bodyText) ?? parseUpstreamMessage(bodyText),
    streamFailureMessage: parseAnthropicStreamFailureMessage(bodyText)
  }
}

function parseGeminiNativeProbeResponse(bodyText: string): ParsedModelCheckProbeResponse {
  const json = parseJsonRecord(bodyText)
  const events = parseSseJsonEvents(bodyText)
  return {
    json,
    outputText: extractGeminiOutputText(json, events),
    model: textValue(json?.model) ?? textValue(json?.modelVersion) ?? firstText(events.map((event) => event.json?.modelVersion)),
    usage: recordValue(json?.usageMetadata) ?? firstRecord(events.map((event) => event.json?.usageMetadata)),
    errorMessage: parseGeminiUpstreamMessage(bodyText) ?? parseUpstreamMessage(bodyText),
    streamFailureMessage: parseGenericStreamFailureMessage(events)
  }
}

function extractOpenAIChatOutputText(json: Record<string, unknown> | undefined, events: SseJsonEvent[]): string | undefined {
  const direct = extractOpenAIChatChoicesText(json, 'message')
  if (direct) return direct
  const chunks = events
    .map((event) => extractOpenAIChatChoicesText(event.json, 'delta'))
    .filter((value): value is string => Boolean(value))
  return joinedText(chunks)
}

function extractOpenAIChatChoicesText(payload: Record<string, unknown> | undefined, field: 'message' | 'delta'): string | undefined {
  const choices = Array.isArray(payload?.choices) ? payload.choices : []
  const parts: string[] = []
  for (const choice of choices) {
    const container = recordValue(recordValue(choice)?.[field])
    const content = openAITextValue(container?.content) ?? openAITextValue(container?.reasoning_content) ?? openAITextValue(container?.refusal)
    if (content) parts.push(content)
  }
  return joinedText(parts)
}

function extractGeminiOutputText(json: Record<string, unknown> | undefined, events: SseJsonEvent[]): string | undefined {
  const direct = extractGeminiCandidateText(json)
  if (direct) return direct
  return joinedText(events.map((event) => extractGeminiCandidateText(event.json)).filter((value): value is string => Boolean(value)))
}

function extractGeminiCandidateText(payload: Record<string, unknown> | undefined): string | undefined {
  const candidates = Array.isArray(payload?.candidates) ? payload.candidates : []
  const parts: string[] = []
  for (const candidate of candidates) {
    const content = recordValue(recordValue(candidate)?.content)
    const contentParts = Array.isArray(content?.parts) ? content.parts : []
    for (const part of contentParts) {
      const text = textValue(recordValue(part)?.text)
      if (text) parts.push(text)
    }
  }
  return joinedText(parts)
}

function parseGeminiUpstreamMessage(bodyText: string): string | undefined {
  const json = parseJsonRecord(bodyText)
  const error = recordValue(json?.error)
  return textValue(error?.message) || textValue(error?.status) || textValue(json?.message)
}

function parseGenericStreamFailureMessage(events: SseJsonEvent[]): string | undefined {
  for (const event of events) {
    const error = recordValue(event.json?.error)
    const message = textValue(error?.message) || textValue(error?.code) || textValue(event.json?.message)
    if (message) return message
  }
  return undefined
}

interface SseJsonEvent {
  event: string
  json?: Record<string, unknown>
}

function parseSseJsonEvents(text: string): SseJsonEvent[] {
  const events: SseJsonEvent[] = []
  const blocks = text.split(/\r?\n\r?\n/)
  for (const block of blocks) {
    const lines = block.split(/\r?\n/)
    let event = ''
    const dataLines: string[] = []
    for (const line of lines) {
      if (line.startsWith('event:')) {
        event = line.slice(6).trim()
      } else if (line.startsWith('data:')) {
        dataLines.push(line.slice(5).trimStart())
      }
    }
    const data = dataLines.join('\n').trim()
    if (!data || data === '[DONE]') continue
    const parsed = parseJsonTextRecord(data)
    events.push({ event, json: parsed })
  }
  return events
}

function parseJsonTextRecord(text: string): Record<string, unknown> | undefined {
  try {
    return recordValue(JSON.parse(text) as unknown)
  } catch {
    return undefined
  }
}

function openAITextValue(value: unknown): string | undefined {
  if (typeof value === 'string') return value.trim() || undefined
  if (!Array.isArray(value)) return undefined
  const parts = value
    .map((item) => textValue(recordValue(item)?.text))
    .filter((item): item is string => Boolean(item))
  return joinedText(parts)
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
