import {
  emptyUsage,
  type ParsedUsage
} from '../../usage/types.js'
import { normalizeOptionalUsageServiceTier } from '../../usage/service-tier.js'

export function parseAnthropicUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  return parseAnthropicUsageFromJsonTextFragment(responseBody.toString('utf8'))
}

export function parseAnthropicUsageFromJsonTextFragment(text?: string): ParsedUsage {
  if (!text) return emptyUsage()
  const usageText = extractJsonObjectPropertyFromTextFragment(text, 'usage')
  if (!usageText) return emptyUsage()
  try {
    return extractAnthropicUsage(JSON.parse(usageText))
  } catch {
    return emptyUsage()
  }
}

export function extractAnthropicUsage(value: unknown): ParsedUsage {
  if (typeof value !== 'object' || value === null) return emptyUsage()
  const usage = value as Record<string, unknown>
  const cacheCreation = objectValue(usage.cache_creation)
  const cacheWrite5mTokens = numberValue(cacheCreation?.ephemeral_5m_input_tokens)
  const cacheWrite1hTokens = numberValue(cacheCreation?.ephemeral_1h_input_tokens)
  const cacheWriteDetailTokens = sumDefined(cacheWrite5mTokens, cacheWrite1hTokens)
  const cacheWriteTokens = numberValue(usage.cache_creation_input_tokens) ?? cacheWriteDetailTokens
  const outputTokenDetails = objectValue(usage.output_tokens_details)
  return {
    serviceTier: normalizeOptionalUsageServiceTier(usage.speed),
    inputTokens: numberValue(usage.input_tokens),
    outputTokens: numberValue(usage.output_tokens),
    cacheReadTokens: numberValue(usage.cache_read_input_tokens),
    cacheWriteTokens,
    cacheWrite1hTokens,
    thinkingTokens: numberValue(outputTokenDetails?.thinking_tokens)
  }
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

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : undefined
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function sumDefined(...values: Array<number | undefined>): number | undefined {
  const defined = values.filter((value): value is number => value !== undefined)
  if (!defined.length) return undefined
  return defined.reduce((sum, value) => sum + value, 0)
}
