import type { AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  diagnosticResponseContext,
  type DiagnosticResponseContext
} from '../gateway/diagnostics/diagnostic-response-context.js'

type AccountTestResponseInput = string | DiagnosticResponseContext

export function hasAccountTestProtocolSuccessEvidence(
  mode: AccountSupportedEndpointMode,
  input: AccountTestResponseInput
): boolean {
  const context = diagnosticResponseContext(input)
  if (mode.endsWith('_sse')) {
    return hasStreamingSuccessEvidence(mode, context)
  }
  const payload = context.record
  if (!payload) return false
  if (mode === 'chat_json') return hasCompletedChatPayload(payload)
  if (mode === 'responses_json') return hasCompletedResponsesPayload(payload)
  if (mode === 'messages_json') return hasCompletedMessagesPayload(payload)
  if (mode === 'generate_content_json') return hasCompletedGeminiPayload(payload)
  if (mode === 'interactions_json') return hasCompletedInteractionsPayload(payload)
  return false
}

export function hasAccountModelCatalogSuccessEvidence(model: string, input: AccountTestResponseInput): boolean {
  const target = model.trim()
  if (!target) return false
  return accountModelCatalogIds(input).includes(target)
}

export function hasAccountModelCatalogResponseEvidence(input: AccountTestResponseInput): boolean {
  const payload = diagnosticResponseContext(input).record
  return Boolean(Array.isArray(payload?.data) || Array.isArray(payload?.models))
}

export function accountModelCatalogIds(input: AccountTestResponseInput): string[] {
  const payload = diagnosticResponseContext(input).record
  return accountModelCatalogIdsFromPayload(payload)
}

export function accountModelCatalogIdsFromPayload(payload: unknown): string[] {
  const record = objectValue(payload)
  if (!record) return []
  const values = Array.isArray(record.data)
    ? record.data.map((item) => stringValue(objectValue(item)?.id))
    : Array.isArray(record.models)
      ? record.models.map((item) => geminiModelId(objectValue(item)?.name))
      : []
  return [...new Set(values.filter(Boolean))]
}

function hasStreamingSuccessEvidence(mode: AccountSupportedEndpointMode, context: DiagnosticResponseContext): boolean {
  let hasChatContent = false
  for (const event of context.events) {
    if (event.done) {
      return mode === 'chat_sse' && hasChatContent
    }
    const payload = event.json
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

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function geminiModelId(value: unknown): string {
  const name = stringValue(value)
  return name.startsWith('models/') ? name.slice('models/'.length) : name
}
