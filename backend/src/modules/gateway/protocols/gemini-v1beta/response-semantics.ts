import {
  hasAnyUsageValue,
  type ParsedUsage
} from '../../usage/types.js'
import type {
  ParsedOpenAIStreamEvent
} from '../openai-v1/stream-events.js'
import type {
  ResponseSemanticFrame
} from '../openai-v1/response-semantics.js'
import { geminiEndpointFamilyFromPath, type GeminiEndpointFamily } from '../../../../domain/gemini-endpoint-modes.js'
import {
  GEMINI_GENERATE_CONTENT_FAMILY
} from '../../../../domain/provider-protocol.js'
import {
  extractGeminiUsage
} from './usage.js'

export type GeminiResponseEndpointFamily = GeminiEndpointFamily

export function geminiResponseEndpointFamilyFromPath(pathAndQuery: string): GeminiResponseEndpointFamily {
  return geminiEndpointFamilyFromPath(pathAndQuery) ?? GEMINI_GENERATE_CONTENT_FAMILY
}

export function extractGeminiJsonSemanticFrames(
  value: unknown,
  endpointFamily: GeminiResponseEndpointFamily = GEMINI_GENERATE_CONTENT_FAMILY
): ResponseSemanticFrame[] {
  const root = objectValue(value)
  if (!root) return []
  const frames: ResponseSemanticFrame[] = []
  const rootError = objectValue(root.error)
  if (rootError) {
    frames.push(errorFrame(rootError, endpointFamily, 'json', ['error']))
  }
  frames.push(...extractGenerateContentFrames(root, endpointFamily, 'json'))
  const usage = extractGeminiUsage(root.usageMetadata)
  if (hasAnyUsageValue(usage)) {
    frames.push(usageFrame(usage, endpointFamily, 'json', ['usageMetadata']))
  }
  frames.push(rawJsonFrame(root, endpointFamily, 'json'))
  return attachRawJson(frames, root)
}

export function extractGeminiSseSemanticFrames(
  event: ParsedOpenAIStreamEvent,
  endpointFamily: GeminiResponseEndpointFamily = GEMINI_GENERATE_CONTENT_FAMILY
): ResponseSemanticFrame[] {
  const data = event.data
  const eventType = event.eventType || event.eventName || 'message'
  const rawText = event.rawText ?? event.dataText
  const frames: ResponseSemanticFrame[] = []
  if (!data) return frames
  const error = extractGeminiStreamEventError(data)
  if (error) {
    frames.push(errorFrame(error, endpointFamily, 'sse', ['error'], eventType, rawText))
  }
  frames.push(...extractGenerateContentFrames(data, endpointFamily, 'sse', eventType, rawText))
  const usage = extractGeminiUsage(data.usageMetadata)
  if (hasAnyUsageValue(usage)) {
    frames.push(usageFrame(usage, endpointFamily, 'sse', ['usageMetadata'], eventType, rawText))
  }
  frames.push(rawJsonFrame(data, endpointFamily, 'sse', eventType, rawText))
  return attachRawJson(frames, data, rawText, eventType)
}

export function extractGeminiStreamEventError(data: Record<string, unknown>): Record<string, unknown> | undefined {
  const error = objectValue(data.error)
  if (error) return error
  return undefined
}

function extractGenerateContentFrames(
  root: Record<string, unknown>,
  endpointFamily: GeminiResponseEndpointFamily,
  transport: 'json' | 'sse',
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame[] {
  const candidates = Array.isArray(root.candidates) ? root.candidates : []
  const frames: ResponseSemanticFrame[] = []
  candidates.forEach((candidate, choiceIndex) => {
    const row = objectValue(candidate)
    if (!row) return
    const content = objectValue(row.content)
    const parts = Array.isArray(content?.parts) ? content.parts : []
    parts.forEach((part, contentIndex) => {
      const item = objectValue(part)
      if (!item) return
      const text = stringValue(item.text)
      if (text) {
        frames.push({
          frameType: transport === 'sse' ? 'output_text_delta' : 'output_text_done',
          protocol: 'gemini_v1beta',
          endpointFamily,
          transport,
          text,
          finishReason: stringValue(row.finishReason),
          status: stringValue(row.finishReason),
          rawJsonPaths: [`candidates.${choiceIndex}.content.parts.${contentIndex}.text`],
          rawText,
          eventType,
          choiceIndex,
          contentIndex,
          visibleOutput: item.thought === true ? false : true
        })
      }
      if (objectValue(item.functionCall) || objectValue(item.inlineData) || objectValue(item.fileData) || objectValue(item.executableCode)) {
        frames.push({
          frameType: 'raw_json_path',
          protocol: 'gemini_v1beta',
          endpointFamily,
          transport,
          rawJsonPaths: [`candidates.${choiceIndex}.content.parts.${contentIndex}`],
          rawText,
          eventType,
          choiceIndex,
          contentIndex,
          visibleOutput: false
        })
      }
    })
    const finishReason = stringValue(row.finishReason)
    if (finishReason) {
      frames.push(completedFrame(endpointFamily, transport, finishReason, eventType, rawText, choiceIndex))
    }
  })
  return frames
}

function rawJsonFrame(
  rawJson: Record<string, unknown>,
  endpointFamily: GeminiResponseEndpointFamily,
  transport: 'json' | 'sse',
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'raw_json_path',
    protocol: 'gemini_v1beta',
    endpointFamily,
    transport,
    rawJson,
    rawText,
    eventType
  }
}

function usageFrame(
  usage: ParsedUsage,
  endpointFamily: GeminiResponseEndpointFamily,
  transport: 'json' | 'sse',
  rawJsonPaths: string[],
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'usage',
    protocol: 'gemini_v1beta',
    endpointFamily,
    transport,
    usage,
    rawJsonPaths,
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
  endpointFamily: GeminiResponseEndpointFamily,
  transport: 'json' | 'sse',
  rawJsonPaths: string[],
  eventType?: string,
  rawText?: string
): ResponseSemanticFrame {
  return {
    frameType: 'error',
    protocol: 'gemini_v1beta',
    endpointFamily,
    transport,
    errorCode: stringValue(error.code) ?? stringValue(error.status),
    errorType: stringValue(error.status),
    errorMessage: stringValue(error.message),
    rawJsonPaths,
    rawText,
    eventType
  }
}

function completedFrame(
  endpointFamily: GeminiResponseEndpointFamily,
  transport: 'json' | 'sse',
  finishReason: string,
  eventType?: string,
  rawText?: string,
  choiceIndex?: number
): ResponseSemanticFrame {
  return {
    frameType: 'completed',
    protocol: 'gemini_v1beta',
    endpointFamily,
    transport,
    finishReason,
    status: finishReason,
    rawText,
    eventType,
    choiceIndex
  }
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string | undefined {
  if (typeof value === 'string') return value.length > 0 ? value : undefined
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return undefined
}
