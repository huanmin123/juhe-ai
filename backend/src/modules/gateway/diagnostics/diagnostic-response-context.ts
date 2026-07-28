import type { GatewayNonStreamJsonBody } from '../response/non-stream-json-body.js'
import type { ParsedOpenAIStreamEvent } from '../protocols/openai-v1/stream-events.js'

export interface DiagnosticSseEvent {
  event?: string
  data: string
  json?: Record<string, unknown>
  done: boolean
}

export interface DiagnosticResponseContext {
  bodyText: string
  json?: unknown
  record?: Record<string, unknown>
  events: readonly DiagnosticSseEvent[]
  payloads: readonly Record<string, unknown>[]
}

export interface DiagnosticResponseParseOptions {
  onJsonParseAttempt?: (text: string) => void
}

export function parseDiagnosticResponseContext(
  bodyText: string,
  options: DiagnosticResponseParseOptions = {}
): DiagnosticResponseContext {
  const normalizedBodyText = bodyText.startsWith('\uFEFF') ? bodyText.slice(1) : bodyText
  const trimmed = normalizedBodyText.trim()
  if (!trimmed) return emptyDiagnosticResponseContext(bodyText)

  if (!looksLikeServerSentEvents(trimmed)) {
    const json = parseJson(trimmed, options)
    if (json !== undefined) {
      const record = recordValue(json)
      return {
        bodyText,
        json,
        record,
        events: [],
        payloads: record ? [record] : []
      }
    }
  }

  const events = parseDiagnosticSseEvents(normalizedBodyText, options)
  return {
    bodyText,
    events,
    payloads: events.flatMap((event) => event.json ? [event.json] : [])
  }
}

export function diagnosticResponseContext(
  input: string | DiagnosticResponseContext
): DiagnosticResponseContext {
  return typeof input === 'string' ? parseDiagnosticResponseContext(input) : input
}

export function diagnosticResponseContextFromGatewayNonStream(
  bodyText: string,
  parsedBody: GatewayNonStreamJsonBody | undefined,
  options: DiagnosticResponseParseOptions = {}
): DiagnosticResponseContext {
  if (!parsedBody) return parseDiagnosticResponseContext(bodyText, options)
  if (parsedBody.status !== 'valid') return emptyDiagnosticResponseContext(bodyText)
  const record = recordValue(parsedBody.value)
  return {
    bodyText,
    json: parsedBody.value,
    record,
    events: [],
    payloads: record ? [record] : []
  }
}

export function diagnosticResponseContextFromGatewayResponse(
  bodyText: string,
  parsedBody: GatewayNonStreamJsonBody | undefined,
  parsedStreamEvents: readonly ParsedOpenAIStreamEvent[] | undefined,
  options: DiagnosticResponseParseOptions = {}
): DiagnosticResponseContext {
  if (parsedBody) {
    return diagnosticResponseContextFromGatewayNonStream(bodyText, parsedBody, options)
  }
  if (!parsedStreamEvents?.length) {
    return parseDiagnosticResponseContext(bodyText, options)
  }
  const events = parsedStreamEvents.map((event): DiagnosticSseEvent => ({
    event: event.eventName || undefined,
    data: event.dataText,
    json: event.data,
    done: event.dataText.trim() === '[DONE]'
  }))
  return {
    bodyText,
    events,
    payloads: events.flatMap((event) => event.json ? [event.json] : [])
  }
}

function parseDiagnosticSseEvents(
  bodyText: string,
  options: DiagnosticResponseParseOptions
): DiagnosticSseEvent[] {
  const events: DiagnosticSseEvent[] = []
  let event: string | undefined
  let dataLines: string[] = []

  const flush = () => {
    if (!dataLines.length) {
      event = undefined
      return
    }
    const data = dataLines.join('\n')
    const normalizedData = data.trim()
    const done = normalizedData === '[DONE]'
    events.push({
      event,
      data,
      json: done || !normalizedData ? undefined : recordValue(parseJson(data, options)),
      done
    })
    event = undefined
    dataLines = []
  }

  for (const line of bodyText.split(/\r\n|\r|\n/)) {
    if (line === '') {
      flush()
      continue
    }
    if (line.startsWith(':')) continue
    const separatorIndex = line.indexOf(':')
    const field = separatorIndex < 0 ? line : line.slice(0, separatorIndex)
    let value = separatorIndex < 0 ? '' : line.slice(separatorIndex + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') {
      event = value
    } else if (field === 'data') {
      dataLines.push(value)
    }
  }
  flush()
  return events
}

function parseJson(text: string, options: DiagnosticResponseParseOptions): unknown {
  options.onJsonParseAttempt?.(text)
  try {
    return JSON.parse(text) as unknown
  } catch {
    return undefined
  }
}

function looksLikeServerSentEvents(text: string): boolean {
  return /(?:^|\r\n|\r|\n)(?::|(?:event|data|id|retry)(?::|$))/.test(text)
}

function emptyDiagnosticResponseContext(bodyText: string): DiagnosticResponseContext {
  return {
    bodyText,
    events: [],
    payloads: []
  }
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
