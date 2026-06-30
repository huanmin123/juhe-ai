import type {
  ModelCheckItemSummary,
  ModelCheckRunStatus
} from '../../domain/types.js'
import {
  extractOpenAIResponseOutputText,
  openAIResponseModelFromSse,
  openAIResponseUsageFromSse,
  parseOpenAIErrorMessage,
  parseOpenAIJsonRecord,
  parseOpenAIStreamFailureMessage,
  parseOpenAIUpstreamMessage
} from '../gateway/protocols/openai-v1/response-parsing.js'
import { normalizeModelCheckModel, type SupportedModel } from './model-checks.profiles.js'

export {
  extractOpenAIResponseOutputText,
  parseOpenAIStreamFailureMessage
}

export const parseJsonRecord = parseOpenAIJsonRecord
export const parseUpstreamMessage = parseOpenAIUpstreamMessage
export const parseErrorMessage = parseOpenAIErrorMessage
export const usageFromSse = openAIResponseUsageFromSse
export const modelFromSse = openAIResponseModelFromSse

export function parseFirstJsonObject(text?: string): Record<string, unknown> | undefined {
  if (!text) return undefined
  const trimmed = text.trim()
  try {
    return recordValue(JSON.parse(trimmed))
  } catch {
    const match = trimmed.match(/\{[\s\S]*\}/)
    if (!match) return undefined
    try {
      return recordValue(JSON.parse(match[0]))
    } catch {
      return undefined
    }
  }
}

export function hasFunctionCall(payload: Record<string, unknown> | undefined, name: string): boolean {
  const output = Array.isArray(payload?.output) ? payload.output : []
  if (output.some((item) => {
    const record = recordValue(item)
    return record?.type === 'function_call' && record.name === name
  })) {
    return true
  }
  const choices = Array.isArray(payload?.choices) ? payload.choices : []
  if (choices.some((choice) => {
    const message = recordValue(recordValue(choice)?.message)
    const toolCalls = Array.isArray(message?.tool_calls) ? message.tool_calls : []
    return toolCalls.some((toolCall) => {
      const record = recordValue(toolCall)
      const fn = recordValue(record?.function)
      return textValue(fn?.name) === name
    })
  })) {
    return true
  }
  const content = Array.isArray(payload?.content) ? payload.content : []
  if (content.some((item) => {
    const record = recordValue(item)
    return record?.type === 'tool_use' && textValue(record.name) === name
  })) {
    return true
  }
  const candidates = Array.isArray(payload?.candidates) ? payload.candidates : []
  return candidates.some((candidate) => {
    const parts = Array.isArray(recordValue(recordValue(candidate)?.content)?.parts)
      ? recordValue(recordValue(candidate)?.content)?.parts as unknown[]
      : []
    return parts.some((part) => textValue(recordValue(recordValue(part)?.functionCall)?.name) === name)
  })
}

export function buildModelMatchEvidence(actual: unknown, expected: string): {
  expectedModel: string
  responseModel?: string
  matchedModel: boolean
  modelMismatch: boolean
} {
  const text = textValue(actual)
  const matchedModel = modelMatches(text, expected)
  return {
    expectedModel: expected,
    responseModel: text,
    matchedModel,
    modelMismatch: Boolean(text && !matchedModel)
  }
}

export function describeModelMismatch(evidence: { expectedModel: string; responseModel?: string; modelMismatch: boolean }): string | undefined {
  return evidence.modelMismatch && evidence.responseModel
    ? `上游返回模型 ${evidence.responseModel}，与请求模型 ${evidence.expectedModel} 不一致`
    : undefined
}

export function hasModelMismatchEvidence(item: ModelCheckItemSummary): boolean {
  const evidence = recordValue(item.evidenceSummary)
  if (evidence?.modelMismatch !== true) return false
  return item.itemKey.startsWith('target.') && item.itemType !== 'cross_model'
}

export function modelMatches(actual: unknown, expected: string): boolean {
  const text = textValue(actual)
  if (!text) return false
  if (text === expected) return true
  if (!text.startsWith(`${expected}-`)) return false
  const suffix = text.slice(expected.length + 1)
  return /^\d{4}-\d{2}-\d{2}(?:$|[._-])/.test(suffix)
}

export function normalizeModel(value: unknown): SupportedModel | undefined {
  return normalizeModelCheckModel(value)
}

export function modelCheckLevelValue(value: unknown): 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable' | undefined {
  return value === 'high_confidence' || value === 'likely' || value === 'uncertain' || value === 'suspicious' || value === 'unavailable' ? value : undefined
}

export function modelCheckStatusValue(value: unknown): ModelCheckRunStatus | undefined {
  return value === 'running' || value === 'completed' || value === 'failed' || value === 'canceled' ? value : undefined
}

export function integerValue(value: unknown): number | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  const numeric = typeof raw === 'number' ? raw : typeof raw === 'string' ? Number.parseInt(raw, 10) : NaN
  return Number.isFinite(numeric) ? Math.trunc(numeric) : undefined
}

export function textValue(value: unknown): string | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined
}

export function recordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

export function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

export function totalTokens(usage: Record<string, unknown> | undefined): number | undefined {
  return numberValue(usage?.total_tokens)
    ?? numberValue(usage?.totalTokens)
    ?? numberValue(usage?.totalTokenCount)
    ?? sumDefined([
      numberValue(usage?.input_tokens),
      numberValue(usage?.output_tokens)
    ])
    ?? sumDefined([
      numberValue(usage?.prompt_tokens),
      numberValue(usage?.completion_tokens)
    ])
    ?? sumDefined([
      numberValue(usage?.promptTokenCount),
      numberValue(usage?.candidatesTokenCount)
    ])
}

export function sumDefined(values: Array<number | undefined>): number | undefined {
  const numbers = values.filter((value): value is number => value !== undefined)
  return numbers.length ? numbers.reduce((sum, value) => sum + value, 0) : undefined
}

export function average(values: number[]): number {
  const numbers = values.filter((value) => Number.isFinite(value))
  return numbers.length ? numbers.reduce((sum, value) => sum + value, 0) / numbers.length : 0
}

export function ratio(part: number, total: number): number {
  return total > 0 ? part / total : 0
}

export function boundedRatio(left: number, right: number): number {
  if (left <= 0 || right <= 0) return 0
  return Math.min(left, right) / Math.max(left, right)
}

export function roundMetric(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.round(value * 1000) / 1000
}

export function textSimilarity(left: string, right: string): number {
  const normalizedLeft = normalizeComparableText(left)
  const normalizedRight = normalizeComparableText(right)
  if (!normalizedLeft || !normalizedRight) return 0
  if (normalizedLeft === normalizedRight) return 1
  const leftTokens = comparableTokens(normalizedLeft)
  const rightTokens = comparableTokens(normalizedRight)
  if (!leftTokens.size || !rightTokens.size) return 0
  let intersection = 0
  for (const token of leftTokens) {
    if (rightTokens.has(token)) intersection += 1
  }
  const union = leftTokens.size + rightTokens.size - intersection
  const tokenSimilarity = union > 0 ? intersection / union : 0
  const lengthSimilarity = boundedRatio(normalizedLeft.length, normalizedRight.length)
  return (tokenSimilarity * 0.75) + (lengthSimilarity * 0.25)
}

export function normalizeComparableText(value: string): string {
  return value
    .toLowerCase()
    .replace(/\s+/g, '')
    .replace(/[，。！？；：,.!?;:"'`~\-—_[\](){}<>]/g, '')
    .trim()
}

export function comparableTokens(value: string): Set<string> {
  if (value.length <= 2) return new Set(value ? [value] : [])
  const tokens = new Set<string>()
  for (let index = 0; index < value.length - 1; index += 1) {
    tokens.add(value.slice(index, index + 2))
  }
  return tokens
}

export function bounded(value?: string): string | undefined {
  if (!value) return undefined
  const text = value.trim()
  return text.length > 160 ? `${text.slice(0, 160)}...` : text
}

export function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new Error('模型检测已取消')
  }
}
