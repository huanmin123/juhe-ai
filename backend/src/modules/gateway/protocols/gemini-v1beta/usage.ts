import {
  emptyUsage,
  type ParsedUsage
} from '../../usage/types.js'
import { normalizeOptionalUsageServiceTier } from '../../usage/service-tier.js'

export function parseGeminiUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  return parseGeminiUsageFromJsonTextFragment(responseBody.toString('utf8'))
}

export function parseGeminiUsageFromJsonTextFragment(text?: string): ParsedUsage {
  if (!text) return emptyUsage()
  try {
    return extractGeminiUsage(JSON.parse(text))
  } catch {
    // Large-response inspection can provide a bounded JSON fragment instead of a complete document.
  }
  const serviceTier = normalizeOptionalUsageServiceTier(extractJsonStringPropertyFromTextFragment(text, 'service_tier'))
  for (const propertyName of ['total_usage', 'usageMetadata', 'usage']) {
    const usageText = extractJsonObjectPropertyFromTextFragment(text, propertyName)
    if (!usageText) continue
    try {
      const usage = extractGeminiUsage(JSON.parse(usageText))
      if (Object.values(usage).some((value) => value !== undefined)) {
        return serviceTier ? { ...usage, serviceTier } : usage
      }
    } catch {
      continue
    }
  }
  return serviceTier ? { serviceTier } : emptyUsage()
}

export function extractGeminiUsage(value: unknown): ParsedUsage {
  if (typeof value !== 'object' || value === null) return emptyUsage()
  const usage = value as Record<string, unknown>
  const interaction = objectValue(usage.interaction)
  const serviceTier = normalizeOptionalUsageServiceTier(usage.service_tier ?? interaction?.service_tier)
  const nested = objectValue(objectValue(usage.metadata)?.total_usage)
    ?? objectValue(usage.total_usage)
    ?? objectValue(usage.usageMetadata)
    ?? objectValue(usage.usage)
    ?? objectValue(interaction?.usage)
  if (nested) {
    const nestedUsage = extractGeminiUsage(nested)
    return serviceTier ? { ...nestedUsage, serviceTier } : nestedUsage
  }
  const candidateTokens = numberValue(usage.candidatesTokenCount)
    ?? numberValue(usage.totalOutputTokens)
    ?? numberValue(usage.total_output_tokens)
    ?? numberValue(usage.outputTokens)
    ?? numberValue(usage.output_tokens)
  const thinkingTokens = numberValue(usage.thoughtsTokenCount)
    ?? numberValue(usage.totalThoughtTokens)
    ?? numberValue(usage.total_thought_tokens)
    ?? numberValue(usage.thoughtTokens)
    ?? numberValue(usage.thought_tokens)
  return {
    ...(serviceTier ? { serviceTier } : {}),
    inputTokens: numberValue(usage.promptTokenCount)
      ?? numberValue(usage.totalInputTokens)
      ?? numberValue(usage.total_input_tokens)
      ?? numberValue(usage.inputTokens)
      ?? numberValue(usage.input_tokens),
    outputTokens: sumDefined(candidateTokens, thinkingTokens),
    cacheReadTokens: numberValue(usage.cachedContentTokenCount)
      ?? numberValue(usage.totalCachedTokens)
      ?? numberValue(usage.total_cached_tokens)
      ?? numberValue(usage.cachedTokens)
      ?? numberValue(usage.cached_tokens),
    thinkingTokens
  }
}

function extractJsonStringPropertyFromTextFragment(text: string, propertyName: string): string | undefined {
  const pattern = new RegExp(`"${propertyName}"\\s*:\\s*"([^"\\\\]*(?:\\\\.[^"\\\\]*)*)"`, 'g')
  let value: string | undefined
  for (const match of text.matchAll(pattern)) value = match[1]
  return value
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
  return defined.length ? defined.reduce((sum, value) => sum + value, 0) : undefined
}
