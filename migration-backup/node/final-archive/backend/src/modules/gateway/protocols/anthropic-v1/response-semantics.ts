import {
  hasAnyUsageValue,
  type ParsedUsage
} from '../../usage/types.js'
import {
  parseOpenAISseEventText,
  type ParsedOpenAIStreamEvent
} from '../openai-v1/stream-events.js'
import type {
  ResponseSemanticFrame
} from '../openai-v1/response-semantics.js'
import {
  extractAnthropicUsage
} from './usage.js'

export type AnthropicResponseEndpointFamily = 'messages' | 'models' | 'message_token_counting'

export function anthropicResponseEndpointFamilyFromPath(pathAndQuery: string): AnthropicResponseEndpointFamily {
  const path = normalizedAnthropicPath(pathAndQuery)
  if (path === '/messages/count_tokens') return 'message_token_counting'
  if (path === '/models') return 'models'
  return 'messages'
}

export function extractAnthropicJsonSemanticFrames(
  value: unknown,
  endpointFamily: AnthropicResponseEndpointFamily = 'messages'
): ResponseSemanticFrame[] {
  const root = objectValue(value)
  if (!root) return []
  const frames: ResponseSemanticFrame[] = []
  const rootError = objectValue(root.error)
  if (rootError || root.type === 'error') {
    frames.push(errorFrame(rootError ?? root, endpointFamily, 'json', rootError ? ['error'] : []))
  }
  if (endpointFamily === 'messages') {
    frames.push(...extractMessageJsonFrames(root, endpointFamily))
  }
  const usage = extractAnthropicUsage(root.usage)
  if (hasAnyUsageValue(usage)) {
    frames.push({
      frameType: 'usage',
      protocol: 'anthropic_v1',
      endpointFamily,
      transport: 'json',
      usage,
      rawJsonPaths: ['usage']
    })
  }
  frames.push(rawJsonFrame(root, endpointFamily, 'json'))
  return attachRawJson(frames, root)
}

export function extractAnthropicSseSemanticFrames(
  event: ParsedOpenAIStreamEvent,
  endpointFamily: AnthropicResponseEndpointFamily = 'messages'
): ResponseSemanticFrame[] {
  const data = event.data
  const eventType = event.eventType || event.eventName || 'message'
  const rawText = event.rawText ?? event.dataText
  const frames: ResponseSemanticFrame[] = []
  if (!data) return frames

  const error = extractAnthropicStreamEventError(data, eventType, event.eventName)
  if (error) {
    frames.push(errorFrame(error, endpointFamily, 'sse', errorRawPaths(data), eventType, rawText))
  }

  if (endpointFamily === 'messages') {
    frames.push(...extractMessageSseFrames(data, endpointFamily, eventType, rawText))
  }

  const usage = extractAnthropicEventUsage(data)
  if (hasAnyUsageValue(usage)) {
    frames.push({
      frameType: 'usage',
      protocol: 'anthropic_v1',
      endpointFamily,
      transport: 'sse',
      usage,
      rawJsonPaths: usageRawPaths(data),
      rawText,
      eventType
    })
  }
  frames.push(rawJsonFrame(data, endpointFamily, 'sse', eventType, rawText))
  return attachRawJson(frames, data, rawText, eventType)
}

export function parseAnthropicSseEventText(rawText: string): ParsedOpenAIStreamEvent {
  return parseOpenAISseEventText(rawText)
}

export function extractAnthropicStreamEventError(
  data: Record<string, unknown>,
  eventType: string,
  eventName = ''
): Record<string, unknown> | undefined {
  if (eventType !== 'error' && eventName !== 'error' && data.type !== 'error') return undefined
  return objectValue(data.error) ?? data
}

function extractMessageJsonFrames(
  root: Record<string, unknown>,
  endpointFamily: AnthropicResponseEndpointFamily
): ResponseSemanticFrame[] {
  const frames: ResponseSemanticFrame[] = []
  const content = Array.isArray(root.content) ? root.content : []
  content.forEach((entry, contentIndex) => {
    const item = objectValue(entry)
    if (!item) return
    if (item.type === 'text' && typeof item.text === 'string' && item.text.length > 0) {
      frames.push({
        frameType: 'output_text_done',
        protocol: 'anthropic_v1',
        endpointFamily,
        transport: 'json',
        text: item.text,
        finishReason: stringValue(root.stop_reason),
        status: stringValue(root.stop_reason),
        rawJsonPaths: [`content.${contentIndex}.text`],
        contentIndex,
        visibleOutput: true
      })
    }
    if (item.type === 'thinking' && typeof item.thinking === 'string' && item.thinking.length > 0) {
      frames.push({
        frameType: 'output_text_done',
        protocol: 'anthropic_v1',
        endpointFamily,
        transport: 'json',
        text: item.thinking,
        finishReason: stringValue(root.stop_reason),
        status: stringValue(root.stop_reason),
        rawJsonPaths: [`content.${contentIndex}.thinking`],
        contentIndex,
        visibleOutput: false
      })
    }
    if (item.type === 'tool_use') {
      frames.push({
        frameType: 'raw_json_path',
        protocol: 'anthropic_v1',
        endpointFamily,
        transport: 'json',
        rawJsonPaths: [`content.${contentIndex}`],
        contentIndex,
        visibleOutput: false
      })
    }
  })
  const stopReason = stringValue(root.stop_reason)
  if (stopReason) {
    frames.push(completedFrame(endpointFamily, 'json', stopReason))
  }
  return frames
}

function extractMessageSseFrames(
  data: Record<string, unknown>,
  endpointFamily: AnthropicResponseEndpointFamily,
  eventType: string,
  rawText?: string
): ResponseSemanticFrame[] {
  const frames: ResponseSemanticFrame[] = []
  if (eventType === 'content_block_start') {
    const block = objectValue(data.content_block)
    if (block?.type === 'tool_use') {
      frames.push({
        frameType: 'raw_json_path',
        protocol: 'anthropic_v1',
        endpointFamily,
        transport: 'sse',
        rawJsonPaths: ['content_block'],
        rawText,
        eventType,
        contentIndex: numberValue(data.index),
        visibleOutput: false
      })
    }
  }
  if (eventType === 'content_block_delta') {
    const delta = objectValue(data.delta)
    const text = anthropicDeltaText(delta)
    if (text) {
      frames.push({
        frameType: 'output_text_delta',
        protocol: 'anthropic_v1',
        endpointFamily,
        transport: 'sse',
        text,
        rawJsonPaths: [`delta.${delta?.type === 'input_json_delta' ? 'partial_json' : delta?.type === 'thinking_delta' ? 'thinking' : 'text'}`],
        rawText,
        eventType,
        contentIndex: numberValue(data.index),
        visibleOutput: delta?.type !== 'thinking_delta'
      })
    }
  }
  if (eventType === 'message_delta') {
    const delta = objectValue(data.delta)
    const stopReason = stringValue(delta?.stop_reason)
    if (stopReason) {
      frames.push(completedFrame(endpointFamily, 'sse', stopReason, eventType, rawText))
    }
  }
  if (eventType === 'message_stop') {
    frames.push(completedFrame(endpointFamily, 'sse', 'message_stop', eventType, rawText))
  }
  return frames
}

function anthropicDeltaText(delta: Record<string, unknown> | undefined): string | undefined {
  if (!delta) return undefined
  if (delta.type === 'text_delta' && typeof delta.text === 'string' && delta.text.length > 0) return delta.text
  if (delta.type === 'input_json_delta' && typeof delta.partial_json === 'string' && delta.partial_json.length > 0) return delta.partial_json
  if (delta.type === 'thinking_delta' && typeof delta.thinking === 'string' && delta.thinking.length > 0) return delta.thinking
  return undefined
}

function extractAnthropicEventUsage(data: Record<string, unknown>): ParsedUsage {
  const usage = objectValue(data.usage)
  if (usage) return extractAnthropicUsage(usage)
  const message = objectValue(data.message)
  const messageUsage = objectValue(message?.usage)
  return messageUsage ? extractAnthropicUsage(messageUsage) : {}
}

function rawJsonFrame(
  rawJson: Record<string, unknown>,
  endpointFamily: AnthropicResponseEndpointFamily,
  transport: 'json' | 'sse',
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'raw_json_path',
    protocol: 'anthropic_v1',
    endpointFamily,
    transport,
    rawJson,
    rawText,
    eventType
  }
}

function attachRawJson(
  frames: ResponseSemanticFrame[],
  rawJson: Record<string, unknown>,
  rawText?: string,
  eventType?: string
): ResponseSemanticFrame[] {
  return frames.map((frame) => ({
    ...frame,
    rawJson: frame.rawJson ?? rawJson,
    rawText: frame.rawText ?? rawText,
    eventType: frame.eventType ?? eventType
  }))
}

function errorFrame(
  error: Record<string, unknown>,
  endpointFamily: AnthropicResponseEndpointFamily,
  transport: 'json' | 'sse',
  rawJsonPaths: string[],
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'error',
    protocol: 'anthropic_v1',
    endpointFamily,
    transport,
    errorCode: stringValue(error.code) ?? stringValue(error.type),
    errorType: stringValue(error.type),
    errorMessage: stringValue(error.message),
    rawJsonPaths,
    rawText,
    eventType
  }
}

function completedFrame(
  endpointFamily: AnthropicResponseEndpointFamily,
  transport: 'json' | 'sse',
  finishReason: string,
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'completed',
    protocol: 'anthropic_v1',
    endpointFamily,
    transport,
    finishReason,
    status: finishReason,
    rawText,
    eventType
  }
}

function errorRawPaths(data: Record<string, unknown>): string[] {
  if (objectValue(data.error)) return ['error']
  return data.type === 'error' ? [] : []
}

function usageRawPaths(data: Record<string, unknown>): string[] {
  if (objectValue(data.usage)) return ['usage']
  if (objectValue(objectValue(data.message)?.usage)) return ['message.usage']
  return []
}

function normalizedAnthropicPath(pathAndQuery: string): string {
  const path = pathAndQuery.split('?', 1)[0] || ''
  const requestPath = path.startsWith('/') ? path : `/${path}`
  return requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}
