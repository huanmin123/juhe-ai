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
import { GEMINI_INTERACTIONS_FAMILY, geminiEndpointFamilyFromPath, type GeminiEndpointFamily } from '../../../../domain/gemini-endpoint-modes.js'
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
  if (endpointFamily === GEMINI_INTERACTIONS_FAMILY) {
    frames.push(...extractInteractionsJsonFrames(root, endpointFamily))
  } else {
    frames.push(...extractGenerateContentFrames(root, endpointFamily, 'json'))
  }
  const usage = extractGeminiUsage(root)
  if (hasAnyUsageValue(usage)) {
    frames.push(usageFrame(usage, endpointFamily, 'json', geminiUsageRawJsonPaths(endpointFamily)))
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
  const error = extractGeminiStreamEventError(data, eventType, event.eventName)
  if (error) {
    frames.push(errorFrame(error, endpointFamily, 'sse', ['error'], eventType, rawText))
  }
  if (endpointFamily === GEMINI_INTERACTIONS_FAMILY) {
    frames.push(...extractInteractionsSseFrames(data, endpointFamily, eventType, rawText))
  } else {
    frames.push(...extractGenerateContentFrames(data, endpointFamily, 'sse', eventType, rawText))
  }
  const usage = extractGeminiUsage(data)
  if (hasAnyUsageValue(usage)) {
    frames.push(usageFrame(usage, endpointFamily, 'sse', geminiUsageRawJsonPaths(endpointFamily), eventType, rawText))
  }
  frames.push(rawJsonFrame(data, endpointFamily, 'sse', eventType, rawText))
  return attachRawJson(frames, data, rawText, eventType)
}

function extractInteractionsJsonFrames(
  root: Record<string, unknown>,
  endpointFamily: GeminiResponseEndpointFamily
): ResponseSemanticFrame[] {
  const frames: ResponseSemanticFrame[] = []
  const steps = Array.isArray(root.steps) ? root.steps : []
  steps.forEach((step, stepIndex) => {
    const row = objectValue(step)
    if (!row) return
    const content = Array.isArray(row.content) ? row.content : []
    content.forEach((item, contentIndex) => {
      const part = objectValue(item)
      const text = stringValue(part?.text)
      if (!text) return
      const visibleOutput = row.type !== 'thought' && row.type !== 'thought_summary'
      frames.push({
        frameType: 'output_text_done',
        protocol: 'gemini_v1beta',
        endpointFamily,
        transport: 'json',
        text,
        status: stringValue(root.status),
        rawJsonPaths: [`steps.${stepIndex}.content.${contentIndex}.text`],
        stepIndex,
        contentIndex,
        visibleOutput
      })
    })
  })
  const status = stringValue(root.status)
  if (status) frames.push(completedFrame(endpointFamily, 'json', status))
  return frames
}

function extractInteractionsSseFrames(
  data: Record<string, unknown>,
  endpointFamily: GeminiResponseEndpointFamily,
  eventType: string,
  rawText?: string
): ResponseSemanticFrame[] {
  const frames: ResponseSemanticFrame[] = []
  if (eventType === 'step.delta') {
    const delta = objectValue(data.delta)
    const text = stringValue(delta?.text)
    if (text) {
      frames.push({
        frameType: 'output_text_delta',
        protocol: 'gemini_v1beta',
        endpointFamily,
        transport: 'sse',
        text,
        rawJsonPaths: ['delta.text'],
        rawText,
        eventType,
        visibleOutput: delta?.type === 'text'
      })
    }
  }
  if (eventType === 'interaction.completed' || eventType === 'interaction.failed') {
    const interaction = objectValue(data.interaction)
    const status = stringValue(interaction?.status) ?? eventType.replace(/^interaction\./, '')
    if (eventType === 'interaction.failed') {
      const error = objectValue(interaction?.error) ?? objectValue(data.error)
      if (error) {
        frames.push(errorFrame(error, endpointFamily, 'sse', ['interaction.error'], eventType, rawText))
      }
    }
    if (status) frames.push(completedFrame(endpointFamily, 'sse', status, eventType, rawText))
  }
  return frames
}

export function extractGeminiStreamEventError(
  data: Record<string, unknown>,
  eventType: string,
  eventName = ''
): Record<string, unknown> | undefined {
  const explicitFailure = eventType === 'error'
    || eventName === 'error'
    || data.type === 'error'
    || eventType === 'interaction.failed'
    || eventName === 'interaction.failed'
  if (!explicitFailure) return undefined
  const interaction = objectValue(data.interaction)
  return objectValue(interaction?.error) ?? objectValue(data.error) ?? interaction ?? data
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

function geminiUsageRawJsonPaths(endpointFamily: GeminiResponseEndpointFamily): string[] {
  return endpointFamily === GEMINI_INTERACTIONS_FAMILY
    ? ['metadata.total_usage']
    : ['usageMetadata']
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
