import type { Request } from 'express'

import {
  extractOpenAIUsage as extractUsage
} from './usage.js'
import {
  hasAnyUsageValue,
  type ParsedUsage
} from '../../usage/types.js'
import {
  extractOpenAIStreamEventError,
  type ParsedOpenAIStreamEvent
} from './stream-events.js'

export type OpenAIResponseEndpointFamily = 'chat_completions' | 'responses' | 'unknown'
export type ResponseEndpointFamily = OpenAIResponseEndpointFamily | 'messages' | 'models' | 'message_token_counting'
export type OpenAIResponseTransport = 'json' | 'sse'
export type ResponseProtocolCode = 'openai_v1' | 'anthropic_v1'
export type ResponseSemanticFrameType = 'output_text_delta' | 'output_text_done' | 'error' | 'completed' | 'usage' | 'raw_json_path'

export interface ResponseSemanticFrame {
  frameType: ResponseSemanticFrameType
  protocol: ResponseProtocolCode
  endpointFamily: ResponseEndpointFamily
  transport: OpenAIResponseTransport
  text?: string
  errorCode?: string
  errorType?: string
  errorMessage?: string
  finishReason?: string
  status?: string
  usage?: ParsedUsage
  rawJson?: unknown
  rawJsonPaths?: string[]
  rawText?: string
  eventType?: string
  choiceIndex?: number
  outputIndex?: number
  contentIndex?: number
  visibleOutput?: boolean
}

export function openAIResponseEndpointFamilyFromRequest(req: Request): OpenAIResponseEndpointFamily {
  const path = req.path || req.originalUrl.split('?', 1)[0] || ''
  if (path.endsWith('/chat/completions') || path.includes('/chat/completions')) return 'chat_completions'
  if (path.endsWith('/responses') || path.includes('/responses')) return 'responses'
  return 'unknown'
}

export function extractOpenAIJsonSemanticFrames(
  value: unknown,
  endpointFamily: OpenAIResponseEndpointFamily
): ResponseSemanticFrame[] {
  const root = objectValue(value)
  if (!root) return []
  const frames: ResponseSemanticFrame[] = []
  const rootError = objectValue(root.error)
  if (rootError) {
    frames.push(errorFrame(rootError, endpointFamily, 'json', ['error']))
  }
  const response = objectValue(root.response)
  const responseError = objectValue(response?.error)
  if (responseError) {
    frames.push(errorFrame(responseError, endpointFamily, 'json', ['response.error']))
  }
  if (endpointFamily === 'chat_completions') {
    frames.push(...extractChatJsonFrames(root))
  } else if (endpointFamily === 'responses') {
    frames.push(...extractResponsesJsonFrames(root))
  } else {
    frames.push(...extractChatJsonFrames(root))
    frames.push(...extractResponsesJsonFrames(root))
  }
  const usage = extractUsage(root.usage)
  if (hasAnyUsageValue(usage)) {
    frames.push({
      frameType: 'usage',
      protocol: 'openai_v1',
      endpointFamily,
      transport: 'json',
      usage,
      rawJsonPaths: ['usage']
    })
  }
  frames.push(rawJsonFrame(root, endpointFamily, 'json'))
  return attachRawJson(frames, root)
}

export function extractOpenAISseSemanticFrames(
  event: ParsedOpenAIStreamEvent,
  endpointFamily: OpenAIResponseEndpointFamily
): ResponseSemanticFrame[] {
  const data = event.data
  const eventType = event.eventType || event.eventName || 'message'
  const rawText = event.rawText ?? event.dataText
  const frames: ResponseSemanticFrame[] = []
  if (!data) {
    if (eventType === '[DONE]') {
      frames.push(completedFrame(endpointFamily, 'sse', '[DONE]', eventType, rawText))
    }
    return frames
  }
  const error = extractOpenAIStreamEventError(data)
  if (error) {
    frames.push(errorFrame(error, endpointFamily, 'sse', errorRawPaths(data), eventType, rawText))
  }
  if (endpointFamily === 'chat_completions') {
    frames.push(...extractChatSseFrames(data, endpointFamily, eventType, rawText))
  } else if (endpointFamily === 'responses') {
    frames.push(...extractResponsesSseFrames(data, endpointFamily, eventType, rawText))
  } else {
    frames.push(...extractChatSseFrames(data, endpointFamily, eventType, rawText))
    frames.push(...extractResponsesSseFrames(data, endpointFamily, eventType, rawText))
  }
  const usage = extractUsage(data.usage ?? objectValue(data.response)?.usage)
  if (hasAnyUsageValue(usage)) {
    frames.push({
      frameType: 'usage',
      protocol: 'openai_v1',
      endpointFamily,
      transport: 'sse',
      usage,
      rawJsonPaths: data.usage ? ['usage'] : ['response.usage'],
      rawText,
      eventType
    })
  }
  frames.push(rawJsonFrame(data, endpointFamily, 'sse', eventType, rawText))
  return attachRawJson(frames, data, rawText, eventType)
}

function rawJsonFrame(
  rawJson: Record<string, unknown>,
  endpointFamily: OpenAIResponseEndpointFamily,
  transport: OpenAIResponseTransport,
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'raw_json_path',
    protocol: 'openai_v1',
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

function extractChatJsonFrames(root: Record<string, unknown>): ResponseSemanticFrame[] {
  const choices = Array.isArray(root.choices) ? root.choices : []
  const frames: ResponseSemanticFrame[] = []
  choices.forEach((choice, choiceIndex) => {
    const row = objectValue(choice)
    if (!row) return
    const message = objectValue(row.message)
    const content = textFromOpenAITextValue(message?.content)
    const finishReason = typeof row.finish_reason === 'string' ? row.finish_reason : undefined
    if (content) {
      frames.push({
        frameType: 'output_text_done',
        protocol: 'openai_v1',
        endpointFamily: 'chat_completions',
        transport: 'json',
        text: content,
        finishReason,
        status: finishReason,
        rawJsonPaths: [`choices.${choiceIndex}.message.content`],
        choiceIndex,
        visibleOutput: true
      })
    }
    if (finishReason) {
      frames.push(completedFrame('chat_completions', 'json', finishReason, undefined, undefined, choiceIndex))
    }
  })
  return frames
}

function extractResponsesJsonFrames(root: Record<string, unknown>): ResponseSemanticFrame[] {
  const frames: ResponseSemanticFrame[] = []
  const status = typeof root.status === 'string' ? root.status : undefined
  if (typeof root.output_text === 'string' && root.output_text.length > 0) {
    frames.push({
      frameType: 'output_text_done',
      protocol: 'openai_v1',
      endpointFamily: 'responses',
      transport: 'json',
      text: root.output_text,
      finishReason: status,
      status,
      rawJsonPaths: ['output_text'],
      visibleOutput: true
    })
  }
  const output = Array.isArray(root.output) ? root.output : []
  output.forEach((item, outputIndex) => {
    const outputItem = objectValue(item)
    const content = Array.isArray(outputItem?.content) ? outputItem.content : []
    content.forEach((entry, contentIndex) => {
      const contentItem = objectValue(entry)
      const text = textFromOpenAITextValue(contentItem?.text)
      if (!text) return
      frames.push({
        frameType: 'output_text_done',
        protocol: 'openai_v1',
        endpointFamily: 'responses',
        transport: 'json',
        text,
        finishReason: status,
        status,
        rawJsonPaths: [`output.${outputIndex}.content.${contentIndex}.text`],
        outputIndex,
        contentIndex,
        visibleOutput: true
      })
    })
  })
  if (status) {
    frames.push(completedFrame('responses', 'json', status))
  }
  return frames
}

function extractChatSseFrames(
  data: Record<string, unknown>,
  endpointFamily: OpenAIResponseEndpointFamily,
  eventType: string,
  rawText?: string
): ResponseSemanticFrame[] {
  const choices = Array.isArray(data.choices) ? data.choices : []
  const frames: ResponseSemanticFrame[] = []
  choices.forEach((choice, choiceIndex) => {
    const row = objectValue(choice)
    if (!row) return
    const delta = objectValue(row.delta)
    const content = textFromOpenAITextValue(delta?.content)
    if (content) {
      frames.push({
        frameType: 'output_text_delta',
        protocol: 'openai_v1',
        endpointFamily,
        transport: 'sse',
        text: content,
        rawJsonPaths: [`choices.${choiceIndex}.delta.content`],
        rawText,
        eventType,
        choiceIndex,
        visibleOutput: true
      })
    }
    const refusal = textFromOpenAITextValue(delta?.refusal)
    if (refusal) {
      frames.push({
        frameType: 'output_text_delta',
        protocol: 'openai_v1',
        endpointFamily,
        transport: 'sse',
        text: refusal,
        rawJsonPaths: [`choices.${choiceIndex}.delta.refusal`],
        rawText,
        eventType,
        choiceIndex,
        visibleOutput: true
      })
    }
    if (typeof row.finish_reason === 'string') {
      frames.push(completedFrame(endpointFamily, 'sse', row.finish_reason, eventType, rawText, choiceIndex))
    }
  })
  return frames
}

function extractResponsesSseFrames(
  data: Record<string, unknown>,
  endpointFamily: OpenAIResponseEndpointFamily,
  eventType: string,
  rawText?: string
): ResponseSemanticFrame[] {
  const frames: ResponseSemanticFrame[] = []
  if (eventType === 'response.output_text.delta') {
    const text = textFromOpenAITextValue(data.delta)
    if (text) {
      frames.push({
        frameType: 'output_text_delta',
        protocol: 'openai_v1',
        endpointFamily,
        transport: 'sse',
        text,
        rawJsonPaths: ['delta'],
        rawText,
        eventType,
        visibleOutput: true
      })
    }
  }
  if (eventType === 'response.output_text.done') {
    const text = textFromOpenAITextValue(data.text)
    if (text) {
      frames.push({
        frameType: 'output_text_done',
        protocol: 'openai_v1',
        endpointFamily,
        transport: 'sse',
        text,
        rawJsonPaths: ['text'],
        rawText,
        eventType,
        visibleOutput: true
      })
    }
  }
  const response = objectValue(data.response)
  if (response && (eventType === 'response.completed' || eventType === 'response.done' || eventType === 'response.incomplete')) {
    frames.push(...extractResponsesJsonFrames(response).map((frame) => ({
      ...frame,
      transport: 'sse' as const,
      rawText,
      eventType
    })))
  }
  if (eventType === 'response.completed' || eventType === 'response.done' || eventType === 'response.incomplete' || eventType === 'response.failed') {
    const status = typeof response?.status === 'string'
      ? response.status
      : eventType === 'response.failed' ? 'failed' : eventType.replace(/^response\./, '')
    frames.push(completedFrame(endpointFamily, 'sse', status, eventType, rawText))
  }
  return frames
}

function errorFrame(
  error: Record<string, unknown>,
  endpointFamily: OpenAIResponseEndpointFamily,
  transport: OpenAIResponseTransport,
  rawJsonPaths: string[],
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'error',
    protocol: 'openai_v1',
    endpointFamily,
    transport,
    errorCode: typeof error.code === 'string' ? error.code : undefined,
    errorType: typeof error.type === 'string' ? error.type : undefined,
    errorMessage: typeof error.message === 'string' ? error.message : undefined,
    rawJsonPaths,
    rawText,
    eventType
  }
}

function completedFrame(
  endpointFamily: OpenAIResponseEndpointFamily,
  transport: OpenAIResponseTransport,
  finishReason: string,
  eventType?: string,
  rawText?: string,
  choiceIndex?: number
): ResponseSemanticFrame {
  return {
    frameType: 'completed',
    protocol: 'openai_v1',
    endpointFamily,
    transport,
    finishReason,
    status: finishReason,
    rawText,
    eventType,
    choiceIndex
  }
}

function errorRawPaths(data: Record<string, unknown>): string[] {
  const paths: string[] = []
  if (objectValue(data.error)) paths.push('error')
  if (objectValue(objectValue(data.response)?.error)) paths.push('response.error')
  if (paths.length === 0 && (typeof data.code === 'string' || typeof data.message === 'string')) {
    paths.push('error')
  }
  return paths
}

function textFromOpenAITextValue(value: unknown): string | undefined {
  if (typeof value === 'string') return value.length > 0 ? value : undefined
  if (!Array.isArray(value)) return undefined
  const parts: string[] = []
  for (const item of value) {
    const entry = objectValue(item)
    if (typeof entry?.text === 'string' && entry.text.length > 0) {
      parts.push(entry.text)
    }
  }
  return parts.length > 0 ? parts.join('') : undefined
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
