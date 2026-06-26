import type { Request } from 'express'

import {
  hasAnyUsageValue,
  type ParsedUsage
} from '../../usage/types.js'
import { extractOpenAIUsage } from './usage.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../../request/body.js'

export interface ParsedOpenAIStreamEvent {
  rawText?: string
  eventName: string
  dataText: string
  data?: Record<string, unknown>
  dataParseError: boolean
  eventType: string
  errorCode?: string
  errorMessage?: string
}

export interface OpenAIStreamEventClassification {
  eventType: string
  terminal: boolean
  failed: boolean
  visibleOutput: boolean
  imageOutput: boolean
  estimatedOutputTokens: number
  usage: ParsedUsage
  usageFound: boolean
  errorCode?: string
  errorMessage?: string
}

export function parseOpenAISseEventText(rawText: string): ParsedOpenAIStreamEvent {
  let eventName = ''
  const dataLines: string[] = []
  for (const line of rawText.split(/\r?\n|\r/)) {
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  return parseOpenAIStreamEventData(dataLines.join('\n').trim(), eventName, rawText)
}

export function parseOpenAIStreamEventData(
  dataText: string,
  eventName: string,
  rawText?: string
): ParsedOpenAIStreamEvent {
  if (!dataText || dataText === '[DONE]') {
    return {
      rawText,
      eventName,
      dataText,
      dataParseError: false,
      eventType: dataText === '[DONE]' ? '[DONE]' : eventName
    }
  }

  try {
    const data = JSON.parse(dataText) as Record<string, unknown>
    const eventType = typeof data.type === 'string' ? data.type : eventName
    const error = extractOpenAIStreamEventError(data)
    return {
      rawText,
      eventName,
      dataText,
      data,
      dataParseError: false,
      eventType,
      errorCode: typeof error?.code === 'string' ? error.code : undefined,
      errorMessage: typeof error?.message === 'string' ? error.message : undefined
    }
  } catch {
    return {
      rawText,
      eventName,
      dataText,
      dataParseError: true,
      eventType: eventName
    }
  }
}

export function classifyOpenAIStreamEvent(
  event: ParsedOpenAIStreamEvent,
  priorEstimatedOutputTokens = 0
): OpenAIStreamEventClassification {
  const data = event.data
  const estimatedOutputTokens = data
    ? estimateOpenAIStreamEventOutputTokens(data, event.eventType, priorEstimatedOutputTokens)
    : 0
  const imageOutput = Boolean(data && openAIStreamEventHasImageOutput(data, event.eventType))
  const visibleOutput = Boolean(data && (estimatedOutputTokens > 0 || openAIStreamEventHasVisibleOutput(data, event.eventType)))
  const terminal = event.eventType === '[DONE]'
    || event.eventType === 'response.completed'
    || event.eventType === 'response.done'
    || event.eventType === 'response.incomplete'
    || event.eventType === 'response.failed'
    || event.eventType === 'image_generation.completed'
    || event.eventType === 'image_generation.failed'
  const failed = event.eventType === 'response.failed'
    || event.eventType === 'image_generation.failed'
  const usage = data ? extractEventUsage(data) : {}
  const usageFound = hasAnyUsageValue(usage)

  return {
    eventType: event.eventType,
    terminal,
    failed,
    visibleOutput,
    imageOutput,
    estimatedOutputTokens,
    usage,
    usageFound,
    errorCode: failed ? event.errorCode : undefined,
    errorMessage: failed ? event.errorMessage : undefined
  }
}

export function extractOpenAIStreamEventError(data: Record<string, unknown>): Record<string, unknown> | undefined {
  if (data.type === 'response.mcp_call.failed') return undefined
  const response = objectValue(data.response)
  const responseError = objectValue(response?.error)
  if (responseError) return responseError
  const error = objectValue(data.error)
  if (error) return error
  if (typeof data.type === 'string' && data.type === 'error' && (typeof data.code === 'string' || typeof data.message === 'string')) {
    return data
  }
  return undefined
}

export function isOpenAIStreamFailureEvent(event: ParsedOpenAIStreamEvent): boolean {
  if (event.eventType === 'response.failed' || event.eventName === 'response.failed') return true
  if (event.eventType === 'error' || event.eventName === 'error') return true
  return Boolean(event.data && extractOpenAIStreamEventError(event.data))
}

export function isOpenAIStreamVisibleOutputEvent(event: ParsedOpenAIStreamEvent): boolean {
  return Boolean(event.data && openAIStreamEventHasVisibleOutput(event.data, event.eventType))
}

export function openAIStreamEventHasVisibleOutput(event: Record<string, unknown>, eventType: string): boolean {
  if (eventType.endsWith('.delta') && hasMeaningfulDelta(event.delta)) {
    return true
  }
  if (eventType === 'response.output_item.added' || eventType === 'response.output_item.done') {
    return responsesOutputItemRepresentsClientOutput(event.item)
      || estimateTokensFromOutputValue(event.item) > 0
  }
  if (eventType === 'response.completed' || eventType === 'response.done' || eventType === 'response.incomplete') {
    const output = (event.response as Record<string, unknown> | undefined)?.output
    return responsesOutputArrayRepresentsClientOutput(output)
      || estimateTokensFromOutputValue(output) > 0
  }
  if (isOpenAIImageStreamEventType(eventType)) {
    return true
  }
  const choices = Array.isArray(event.choices) ? event.choices : []
  return choices.some((choice) => {
    if (typeof choice !== 'object' || choice === null) return false
    const row = choice as Record<string, unknown>
    if (hasNonEmptyString(row.text)) return true
    const delta = objectValue(row.delta)
    return Boolean(delta && hasMeaningfulChoiceDelta(delta))
  })
}

export function openAIStreamEventHasImageOutput(event: Record<string, unknown>, eventType: string): boolean {
  if (isOpenAIImageStreamEventType(eventType)) {
    return true
  }
  const item = objectValue(event.item)
  if (item?.type === 'image_generation_call') {
    return true
  }
  const response = objectValue(event.response)
  const output = Array.isArray(response?.output) ? response.output : undefined
  return Boolean(output?.some((entry) => objectValue(entry)?.type === 'image_generation_call'))
}

export function isOpenAIImageStreamEventType(eventType: string): boolean {
  return eventType.startsWith('response.image_generation_call.')
    || eventType === 'image_generation.partial_image'
    || eventType === 'image_generation.completed'
    || eventType === 'image_generation.failed'
}

export function estimateOpenAIRequestInputTokens(req: Request): number | undefined {
  const bodyState = getGatewayRequestBodyState(req)
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  const bodyTokens = estimateTokensFromRequestValue(req.body)
  if (bodyTokens > 0) return bodyTokens

  if (!rawBody || rawBody.length === 0) return undefined
  if (bodyState?.isJson && bodyState.jsonParseStatus === 'parsed') return undefined
  return estimateTokenCountFromByteLength(rawBody.length)
}

function estimateOpenAIStreamEventOutputTokens(event: Record<string, unknown>, eventType: string, priorEstimatedOutputTokens = 0): number {
  let tokens = 0
  if (eventType.endsWith('.delta')) {
    tokens += estimateTokensFromOutputValue(event.delta)
  }

  const choices = Array.isArray(event.choices) ? event.choices : []
  for (const choice of choices) {
    if (typeof choice !== 'object' || choice === null) continue
    const row = choice as Record<string, unknown>
    tokens += estimateTokensFromOutputValue(row.text)
    tokens += estimateTokensFromOutputValue(row.delta)
  }

  if (tokens === 0 && priorEstimatedOutputTokens === 0) {
    if (eventType === 'response.output_item.done') {
      tokens += estimateTokensFromOutputValue(event.item)
    } else if (eventType === 'response.completed' || eventType === 'response.done' || eventType === 'response.incomplete') {
      tokens += estimateTokensFromOutputValue((event.response as Record<string, unknown> | undefined)?.output)
    }
  }

  return tokens
}

function hasMeaningfulChoiceDelta(delta: Record<string, unknown>): boolean {
  return hasMeaningfulDelta(delta.content)
    || hasMeaningfulDelta(delta.refusal)
    || hasMeaningfulDelta(delta.reasoning_content)
    || hasMeaningfulDelta(delta.audio)
    || hasMeaningfulDelta(delta.tool_calls)
    || hasMeaningfulDelta(delta.function_call)
}

function hasMeaningfulDelta(value: unknown): boolean {
  if (hasNonEmptyString(value)) return true
  if (Array.isArray(value)) return value.some(hasMeaningfulDelta)
  if (typeof value !== 'object' || value === null) return false
  const record = value as Record<string, unknown>
  for (const key in record) {
    if (!Object.prototype.hasOwnProperty.call(record, key)) continue
    if (key !== 'index' && key !== 'type' && key !== 'id' && hasMeaningfulDelta(record[key])) {
      return true
    }
  }
  return false
}

function responsesOutputArrayRepresentsClientOutput(value: unknown): boolean {
  return Array.isArray(value) && value.some(responsesOutputItemRepresentsClientOutput)
}

function responsesOutputItemRepresentsClientOutput(value: unknown): boolean {
  const item = objectValue(value)
  if (!item || typeof item.type !== 'string') return false
  return isResponsesCallableOutputItemType(item.type)
}

function isResponsesCallableOutputItemType(type: string): boolean {
  return type === 'function_call'
    || type === 'custom_tool_call'
    || type === 'computer_call'
    || type === 'web_search_call'
    || type === 'file_search_call'
    || type === 'mcp_call'
    || type === 'code_interpreter_call'
    || type === 'image_generation_call'
}

function estimateTokensFromRequestValue(value: unknown, key = ''): number {
  return estimateTokensFromValue(value, key, requestTokenEstimateSkippedKeys)
}

function estimateTokensFromOutputValue(value: unknown, key = ''): number {
  return estimateTokensFromValue(value, key, outputTokenEstimateSkippedKeys)
}

function estimateTokensFromValue(value: unknown, key: string, skippedKeys: Set<string>): number {
  return estimateTokensFromValueWithContext(value, key, skippedKeys, {
    seen: new WeakSet<object>(),
    nodes: 0
  }, 0)
}

function estimateTokensFromValueWithContext(
  value: unknown,
  key: string,
  skippedKeys: Set<string>,
  context: TokenEstimateContext,
  depth: number
): number {
  if (context.nodes >= tokenEstimateMaxNodes || depth > tokenEstimateMaxDepth) {
    return 0
  }
  context.nodes += 1
  if (typeof value === 'string') {
    return shouldSkipEstimatedString(value, key) ? 0 : estimateTokenCountFromText(value)
  }
  if (Array.isArray(value)) {
    let total = 0
    const length = Math.min(value.length, tokenEstimateMaxArrayItems)
    for (let index = 0; index < length; index += 1) {
      total += estimateTokensFromValueWithContext(value[index], key, skippedKeys, context, depth + 1)
      if (context.nodes >= tokenEstimateMaxNodes) break
    }
    return total
  }
  if (typeof value !== 'object' || value === null) {
    return 0
  }
  if (context.seen.has(value)) {
    return 0
  }
  context.seen.add(value)

  let total = 0
  let visitedKeys = 0
  const record = value as Record<string, unknown>
  for (const childKey in record) {
    if (!Object.prototype.hasOwnProperty.call(record, childKey)) continue
    if (visitedKeys >= tokenEstimateMaxObjectKeys) break
    visitedKeys += 1
    if (skippedKeys.has(childKey)) continue
    total += estimateTokensFromValueWithContext(record[childKey], childKey, skippedKeys, context, depth + 1)
    if (context.nodes >= tokenEstimateMaxNodes) break
  }
  return total
}

export function estimateTokenCountFromText(text: string): number {
  if (!text.trim()) return 0
  let asciiLikeChars = 0
  let cjkChars = 0
  let otherChars = 0
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0
    if (isCjkCodePoint(code)) {
      cjkChars += 1
    } else if (code <= 0x7f) {
      asciiLikeChars += 1
    } else {
      otherChars += 1
    }
  }
  return Math.max(1, Math.ceil(asciiLikeChars / 4) + cjkChars + Math.ceil(otherChars / 2))
}

function estimateTokenCountFromByteLength(bytes: number): number | undefined {
  return bytes > 0 ? Math.max(1, Math.ceil(bytes / 4)) : undefined
}

function shouldSkipEstimatedString(value: string, key: string): boolean {
  const trimmed = value.trim()
  if (!trimmed) return true
  if (key === 'url' && trimmed.startsWith('data:')) return true
  return /^data:[^,]+;base64,/i.test(trimmed) || looksLikeLargeBase64Payload(trimmed, key)
}

function looksLikeLargeBase64Payload(value: string, key: string): boolean {
  if (!binaryPayloadEstimateSkippedKeys.has(key)) return false
  if (value.length < 512 || /\s/.test(value)) return false
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  return /^[A-Za-z0-9+/]+={0,2}$/.test(normalized) && normalized.length % 4 === 0
}

function isCjkCodePoint(code: number): boolean {
  return (code >= 0x3400 && code <= 0x9fff)
    || (code >= 0xf900 && code <= 0xfaff)
    || (code >= 0x20000 && code <= 0x2ebef)
}

const requestTokenEstimateSkippedKeys = new Set([
  'model',
  'stream',
  'stream_options',
  'metadata',
  'user'
])

const binaryPayloadEstimateSkippedKeys = new Set([
  'data',
  'b64_json',
  'partial_image_b64',
  'result',
  'file_data',
  'audio',
  'image'
])

const outputTokenEstimateSkippedKeys = new Set([
  'object',
  'model',
  'status',
  'created',
  'created_at',
  'sequence_number',
  'output_index',
  'content_index',
  'item_id',
  'id',
  'index',
  'type',
  'role',
  'finish_reason',
  'logprobs',
  'usage',
  'error'
])

interface TokenEstimateContext {
  seen: WeakSet<object>
  nodes: number
}

const tokenEstimateMaxDepth = 8
const tokenEstimateMaxNodes = 5000
const tokenEstimateMaxArrayItems = 200
const tokenEstimateMaxObjectKeys = 120

function extractEventUsage(event: Record<string, unknown>): ParsedUsage {
  const response = objectValue(event.response)
  return extractOpenAIUsage(response?.usage ?? event.usage)
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function hasNonEmptyString(value: unknown): boolean {
  return typeof value === 'string' && value.length > 0
}
