export interface OpenAIUpstreamMessageOptions {
  rawFallback?: boolean
}

export function parseOpenAIJsonBody(bodyText: string): unknown {
  if (!bodyText.trim() || bodyText.trimStart().startsWith('event:')) return undefined
  try {
    return JSON.parse(bodyText) as unknown
  } catch {
    return undefined
  }
}

export function parseOpenAIJsonRecord(bodyText: string): Record<string, unknown> | undefined {
  return recordValue(parseOpenAIJsonBody(bodyText))
}

export function extractOpenAIResponseOutputText(bodyText: string): string | undefined {
  const direct = extractTextFromOpenAIResponsePayload(parseOpenAIJsonBody(bodyText))
  if (direct) return direct
  const parts: string[] = []
  for (const event of parseOpenAISseEvents(bodyText)) {
    if (event.type === 'response.output_text.delta' || event.type === 'response.refusal.delta') {
      const delta = textValue(event.delta)
      if (delta) parts.push(delta)
    }
    if (event.type === 'response.output_text.done') {
      const text = textValue(event.text)
      if (text) return text
    }
    if (event.type === 'response.completed' || event.type === 'response.done') {
      const text = extractTextFromOpenAIResponsePayload(event.response)
      if (text) return text
    }
  }
  const text = parts.join('').trim()
  return text || undefined
}

export function extractTextFromOpenAIResponsePayload(payload: unknown): string | undefined {
  const record = recordValue(payload)
  const direct = textValue(record?.output_text)
  if (direct) return direct
  const output = Array.isArray(record?.output) ? record.output : []
  const parts: string[] = []
  for (const item of output) {
    const content = recordValue(item)?.content
    if (!Array.isArray(content)) continue
    for (const contentItem of content) {
      const text = textValue(recordValue(contentItem)?.text)
      if (text) parts.push(text)
    }
  }
  const text = parts.join('').trim()
  return text || undefined
}

export function parseOpenAIStreamFailureMessage(bodyText: string): string | undefined {
  if (!bodyText.includes('response.failed') && !bodyText.includes('response.incomplete') && !bodyText.includes('error')) {
    return undefined
  }
  for (const event of parseOpenAISseEvents(bodyText)) {
    const type = textValue(event.type)
    if (type !== 'response.failed' && type !== 'response.incomplete' && type !== 'error') continue
    const error = event.error ?? recordValue(event.response)?.error
    return parseOpenAIErrorMessage(error) || parseOpenAIErrorMessage(event) || type
  }
  return undefined
}

export function parseOpenAISseEvents(bodyText: string): Array<Record<string, unknown>> {
  const events: Array<Record<string, unknown>> = []
  let eventName = ''
  let dataLines: string[] = []
  const flush = () => {
    const data = dataLines.join('\n').trim()
    const type = eventName
    eventName = ''
    dataLines = []
    if (!data || data === '[DONE]') return
    try {
      const payload = JSON.parse(data) as Record<string, unknown>
      if (type && typeof payload.type !== 'string') payload.type = type
      events.push(payload)
    } catch {
    }
  }
  for (const line of bodyText.split(/\r?\n/)) {
    if (!line) {
      flush()
    } else if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  flush()
  return events
}

export function parseOpenAIUpstreamMessage(bodyText: string, options: OpenAIUpstreamMessageOptions = {}): string | undefined {
  const json = parseOpenAIJsonRecord(bodyText)
  const error = recordValue(json?.error)
  const message = textValue(error?.message)
    || textValue(error?.code)
    || textValue(json?.message)
    || parseOpenAIStreamFailureMessage(bodyText)
  if (message) return message
  return options.rawFallback && bodyText ? bodyText.slice(0, 240) : undefined
}

export function parseOpenAIErrorMessage(value: unknown): string | undefined {
  if (typeof value === 'string' && value.trim()) return value.trim()
  const record = recordValue(value)
  return textValue(record?.message) || textValue(record?.code) || textValue(record?.type)
}

export function openAIResponseUsageFromSse(bodyText: string): Record<string, unknown> | undefined {
  for (const event of parseOpenAISseEvents(bodyText)) {
    const response = recordValue(event.response)
    const usage = recordValue(response?.usage)
    if (usage) return usage
  }
  return undefined
}

export function openAIResponseModelFromSse(bodyText: string): string | undefined {
  for (const event of parseOpenAISseEvents(bodyText)) {
    const model = textValue(recordValue(event.response)?.model)
    if (model) return model
  }
  return undefined
}

function textValue(value: unknown): string | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}
