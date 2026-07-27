import {
  emptyUsage,
  type ParsedUsage
} from '../../usage/types.js'
import { normalizeOptionalUsageServiceTier } from '../../usage/service-tier.js'

export function parseOpenAIUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  const text = responseBody.toString('utf8')
  try {
    const root = JSON.parse(text) as Record<string, unknown>
    return extractOpenAIUsage(root.usage, normalizeServiceTier(root.service_tier))
  } catch {
    return parseOpenAIUsageFromJsonTextFragment(text)
  }
}

export function parseOpenAIUsageFromJsonValue(value: unknown): ParsedUsage {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return emptyUsage()
  const root = value as Record<string, unknown>
  return extractOpenAIUsage(root.usage, normalizeServiceTier(root.service_tier))
}

export function parseOpenAIUsageFromJsonTextFragment(text?: string): ParsedUsage {
  if (!text) return emptyUsage()
  const usageText = extractJsonObjectPropertyFromTextFragment(text, 'usage')
  const serviceTier = normalizeServiceTier(extractJsonStringPropertyFromTextFragment(text, 'service_tier'))
  if (!usageText) return serviceTier ? { serviceTier } : emptyUsage()
  try {
    return extractOpenAIUsage(JSON.parse(usageText), serviceTier)
  } catch {
    return emptyUsage()
  }
}

export function extractOpenAIUsage(value: unknown, serviceTier?: ParsedUsage['serviceTier']): ParsedUsage {
  if (typeof value !== 'object' || value === null) return emptyUsage()
  const usage = value as Record<string, unknown>
  const responsesInputDetails = objectValue(usage.input_tokens_details)
  const chatInputDetails = objectValue(usage.prompt_tokens_details)
  const inputTokens = numberValue(usage.input_tokens) ?? numberValue(usage.prompt_tokens)
  const outputTokens = numberValue(usage.output_tokens) ?? numberValue(usage.completion_tokens)
  const cacheReadTokens = numberValue(responsesInputDetails?.cached_tokens)
    ?? numberValue(chatInputDetails?.cached_tokens)
    ?? numberValue(usage.prompt_cache_hit_tokens)
  const responsesCacheCreation = objectValue(responsesInputDetails?.cache_creation)
  const chatCacheCreation = objectValue(chatInputDetails?.cache_creation)
  const rootCacheCreation = objectValue(usage.cache_creation)
  const cacheWrite5mTokens = firstNumberValue(
    responsesInputDetails?.cache_write_5m_tokens,
    responsesInputDetails?.cache_write_5m_input_tokens,
    responsesInputDetails?.cache_creation_5m_tokens,
    responsesInputDetails?.cache_creation_5m_input_tokens,
    responsesCacheCreation?.ephemeral_5m_input_tokens,
    chatInputDetails?.cache_write_5m_tokens,
    chatInputDetails?.cache_write_5m_input_tokens,
    chatInputDetails?.cache_creation_5m_tokens,
    chatInputDetails?.cache_creation_5m_input_tokens,
    chatCacheCreation?.ephemeral_5m_input_tokens,
    usage.cache_write_5m_tokens,
    usage.cache_write_5m_input_tokens,
    usage.cache_creation_5m_tokens,
    usage.cache_creation_5m_input_tokens,
    usage.cache_creation_5_m_tokens,
    usage.claude_cache_creation_5m_tokens,
    usage.claude_cache_creation_5_m_tokens,
    rootCacheCreation?.ephemeral_5m_input_tokens
  )
  const cacheWrite1hTokens = firstNumberValue(
    responsesInputDetails?.cache_write_1h_tokens,
    responsesInputDetails?.cache_write_1h_input_tokens,
    responsesInputDetails?.cache_creation_1h_tokens,
    responsesInputDetails?.cache_creation_1h_input_tokens,
    responsesCacheCreation?.ephemeral_1h_input_tokens,
    chatInputDetails?.cache_write_1h_tokens,
    chatInputDetails?.cache_write_1h_input_tokens,
    chatInputDetails?.cache_creation_1h_tokens,
    chatInputDetails?.cache_creation_1h_input_tokens,
    chatCacheCreation?.ephemeral_1h_input_tokens,
    usage.cache_write_1h_tokens,
    usage.cache_write_1h_input_tokens,
    usage.cache_creation_1h_tokens,
    usage.cache_creation_1h_input_tokens,
    usage.cache_creation_1_h_tokens,
    usage.claude_cache_creation_1h_tokens,
    usage.claude_cache_creation_1_h_tokens,
    rootCacheCreation?.ephemeral_1h_input_tokens
  )
  const cacheWriteDetailTokens = sumDefined(cacheWrite5mTokens, cacheWrite1hTokens)
  const cacheWriteTokens = firstNumberValue(
    responsesInputDetails?.cache_write_tokens,
    responsesInputDetails?.cache_write_input_tokens,
    responsesInputDetails?.cache_creation_tokens,
    responsesInputDetails?.cache_creation_input_tokens,
    chatInputDetails?.cache_write_tokens,
    chatInputDetails?.cache_write_input_tokens,
    chatInputDetails?.cache_creation_tokens,
    chatInputDetails?.cache_creation_input_tokens,
    usage.cache_write_tokens,
    usage.cache_write_input_tokens,
    usage.cache_creation_tokens,
    usage.cache_creation_input_tokens,
    cacheWriteDetailTokens
  )
  const outputDetails = objectValue(usage.output_tokens_details) ?? objectValue(usage.completion_tokens_details)
  const inputImageTokens = numberValue(responsesInputDetails?.image_tokens)
    ?? numberValue(chatInputDetails?.image_tokens)
  const outputImageTokens = numberValue(outputDetails?.image_tokens)
  const inputAudioTokens = numberValue(responsesInputDetails?.audio_tokens)
    ?? numberValue(chatInputDetails?.audio_tokens)
  const outputAudioTokens = numberValue(outputDetails?.audio_tokens)
  const thinkingTokens = numberValue(outputDetails?.reasoning_tokens)
  const outputImageCount = outputImageCountValue(usage)
  return { serviceTier, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, cacheWrite1hTokens, inputImageTokens, outputImageTokens, inputAudioTokens, outputAudioTokens, thinkingTokens, outputImageCount }
}

function extractJsonStringPropertyFromTextFragment(text: string, propertyName: string): string | undefined {
  const pattern = new RegExp(`"${propertyName}"\\s*:\\s*"([^"\\\\]*(?:\\\\.[^"\\\\]*)*)"`, 'g')
  let value: string | undefined
  for (const match of text.matchAll(pattern)) value = match[1]
  return value
}

function normalizeServiceTier(value: unknown): ParsedUsage['serviceTier'] {
  return normalizeOptionalUsageServiceTier(value)
}

function extractJsonObjectPropertyFromTextFragment(text: string, propertyName: string): string | undefined {
  const token = `"${propertyName}"`
  let searchFrom = text.length
  while (searchFrom > 0) {
    const tokenIndex = text.lastIndexOf(token, searchFrom - 1)
    if (tokenIndex < 0) {
      return undefined
    }
    let cursor = tokenIndex + token.length
    cursor = skipJsonWhitespace(text, cursor)
    if (text[cursor] !== ':') {
      searchFrom = tokenIndex
      continue
    }
    cursor = skipJsonWhitespace(text, cursor + 1)
    if (text[cursor] !== '{') {
      searchFrom = tokenIndex
      continue
    }
    const objectText = extractJsonObjectAt(text, cursor)
    if (objectText) {
      return objectText
    }
    searchFrom = tokenIndex
  }
  return undefined
}

function extractJsonObjectAt(text: string, startIndex: number): string | undefined {
  let depth = 0
  let inString = false
  let escaping = false
  for (let index = startIndex; index < text.length; index += 1) {
    const char = text[index]
    if (inString) {
      if (escaping) {
        escaping = false
      } else if (char === '\\') {
        escaping = true
      } else if (char === '"') {
        inString = false
      }
      continue
    }

    if (char === '"') {
      inString = true
    } else if (char === '{') {
      depth += 1
    } else if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return text.slice(startIndex, index + 1)
      }
    }
  }
  return undefined
}

function skipJsonWhitespace(text: string, startIndex: number): number {
  let index = startIndex
  while (index < text.length && /\s/.test(text[index])) {
    index += 1
  }
  return index
}

function outputImageCountValue(usage: Record<string, unknown>): number | undefined {
  const value = numberValue(usage.output_image_count)
    ?? numberValue(usage.output_images)
    ?? numberValue(usage.image_count)
  return value && value > 0 ? value : undefined
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null ? value as Record<string, unknown> : undefined
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : undefined
}

function firstNumberValue(...values: unknown[]): number | undefined {
  for (const value of values) {
    const number = numberValue(value)
    if (number !== undefined) return number
  }
  return undefined
}

function sumDefined(...values: Array<number | undefined>): number | undefined {
  const defined = values.filter((value): value is number => value !== undefined)
  if (!defined.length) return undefined
  return defined.reduce((sum, value) => sum + value, 0)
}
