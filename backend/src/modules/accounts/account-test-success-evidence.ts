import type { AccountSupportedEndpointMode } from '../../domain/types.js'

export function hasAccountTestProtocolSuccessEvidence(
  mode: AccountSupportedEndpointMode,
  bodyText: string
): boolean {
  if (mode.endsWith('_sse')) {
    return hasStreamingSuccessEvidence(mode, bodyText)
  }
  const payload = parseJsonObject(bodyText)
  if (!payload) return false
  if (mode === 'chat_json') return hasCompletedChatPayload(payload)
  if (mode === 'responses_json') return hasCompletedResponsesPayload(payload)
  if (mode === 'messages_json') return hasCompletedMessagesPayload(payload)
  if (mode === 'generate_content_json') return hasCompletedGeminiPayload(payload)
  if (mode === 'interactions_json') return hasCompletedInteractionsPayload(payload)
  return false
}

export function hasAccountModelCatalogSuccessEvidence(model: string, bodyText: string): boolean {
  const target = model.trim()
  if (!target) return false
  const payload = parseJsonObject(bodyText)
  if (!payload || payload.object !== 'list' || !Array.isArray(payload.data)) return false
  return payload.data.some((item) => stringValue(objectValue(item)?.id) === target)
}

export function hasAccountImageGenerationSuccessEvidence(bodyText: string): boolean {
  const payload = parseJsonObject(bodyText)
  if (!payload || !Array.isArray(payload.data)) return false
  return payload.data.some((item) => {
    const image = objectValue(item)
    return Boolean(stringValue(image?.b64_json) || stringValue(image?.url))
  })
}

function hasStreamingSuccessEvidence(mode: AccountSupportedEndpointMode, bodyText: string): boolean {
  let hasChatContent = false
  for (const event of parseServerSentEvents(bodyText)) {
    if (event.data === '[DONE]') {
      return mode === 'chat_sse' && hasChatContent
    }
    const payload = parseJsonObject(event.data)
    if (!payload) continue
    const eventType = stringValue(payload.type) || event.event
    if (mode === 'chat_sse' && hasCompletedChatPayload(payload)) return true
    if (mode === 'chat_sse' && hasChatContentPayload(payload)) hasChatContent = true
    if (mode === 'responses_sse' && (
      eventType === 'response.completed'
      || hasCompletedResponsesPayload(objectValue(payload.response) ?? payload)
    )) return true
    if (mode === 'messages_sse' && (
      eventType === 'message_stop'
      || hasCompletedMessagesPayload(objectValue(payload.message) ?? payload)
    )) return true
    if (mode === 'generate_content_sse' && hasCompletedGeminiPayload(payload)) return true
    if (mode === 'interactions_sse' && (eventType === 'interaction.completed' || objectValue(payload.interaction)?.status === 'completed')) return true
  }
  return false
}

function hasCompletedChatPayload(payload: Record<string, unknown>): boolean {
  const choices = Array.isArray(payload.choices) ? payload.choices : []
  return choices.some((choice) => Boolean(stringValue(objectValue(choice)?.finish_reason)))
}

function hasChatContentPayload(payload: Record<string, unknown>): boolean {
  const choices = Array.isArray(payload.choices) ? payload.choices : []
  return choices.some((choice) => {
    const item = objectValue(choice)
    const delta = objectValue(item?.delta)
    const message = objectValue(item?.message)
    return Boolean(stringValue(delta?.content) || stringValue(message?.content))
  })
}

function hasCompletedResponsesPayload(payload: Record<string, unknown>): boolean {
  return payload.status === 'completed'
    && (payload.object === 'response' || Array.isArray(payload.output))
}

function hasCompletedMessagesPayload(payload: Record<string, unknown>): boolean {
  return payload.type === 'message' && Boolean(stringValue(payload.stop_reason))
}

function hasCompletedGeminiPayload(payload: Record<string, unknown>): boolean {
  const candidates = Array.isArray(payload.candidates) ? payload.candidates : []
  return candidates.some((candidate) => Boolean(stringValue(objectValue(candidate)?.finishReason)))
}

function hasCompletedInteractionsPayload(payload: Record<string, unknown>): boolean {
  return payload.status === 'completed'
    && (payload.object === 'interaction' || Array.isArray(payload.steps))
}

function parseServerSentEvents(bodyText: string): Array<{ event?: string; data: string }> {
  const output: Array<{ event?: string; data: string }> = []
  let event: string | undefined
  let dataLines: string[] = []
  const flush = () => {
    if (dataLines.length) output.push({ event, data: dataLines.join('\n') })
    event = undefined
    dataLines = []
  }
  for (const line of bodyText.split(/\r?\n/)) {
    if (!line.trim()) {
      flush()
    } else if (line.startsWith('event:')) {
      event = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      const data = line.slice(5).trim()
      if (data === '[DONE]') {
        flush()
        output.push({ event, data })
        event = undefined
      } else if (data) {
        dataLines.push(data)
      }
    }
  }
  flush()
  return output
}

function parseJsonObject(value: string): Record<string, unknown> | undefined {
  try {
    return objectValue(JSON.parse(value) as unknown)
  } catch {
    return undefined
  }
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
