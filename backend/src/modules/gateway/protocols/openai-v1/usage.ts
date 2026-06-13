import {
  emptyUsage,
  type ParsedUsage
} from '../../usage/types.js'

export function parseOpenAIUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  return parseOpenAIUsageFromJsonTextFragment(responseBody.toString('utf8'))
}

export function parseOpenAIUsageFromJsonTextFragment(text?: string): ParsedUsage {
  if (!text) return emptyUsage()
  const usageText = extractJsonObjectPropertyFromTextFragment(text, 'usage')
  if (!usageText) return emptyUsage()
  try {
    return extractOpenAIUsage(JSON.parse(usageText))
  } catch {
    return emptyUsage()
  }
}

export function extractOpenAIUsage(value: unknown): ParsedUsage {
  if (typeof value !== 'object' || value === null) return emptyUsage()
  const usage = value as Record<string, unknown>
  const responsesInputDetails = objectValue(usage.input_tokens_details)
  const chatInputDetails = objectValue(usage.prompt_tokens_details)
  const inputTokens = numberValue(usage.input_tokens) ?? numberValue(usage.prompt_tokens)
  const outputTokens = numberValue(usage.output_tokens) ?? numberValue(usage.completion_tokens)
  const cacheReadTokens = numberValue(responsesInputDetails?.cached_tokens)
    ?? numberValue(chatInputDetails?.cached_tokens)
  const outputDetails = objectValue(usage.output_tokens_details) ?? objectValue(usage.completion_tokens_details)
  const inputImageTokens = numberValue(responsesInputDetails?.image_tokens)
    ?? numberValue(chatInputDetails?.image_tokens)
  const outputImageTokens = numberValue(outputDetails?.image_tokens)
  const inputAudioTokens = numberValue(responsesInputDetails?.audio_tokens)
    ?? numberValue(chatInputDetails?.audio_tokens)
  const outputAudioTokens = numberValue(outputDetails?.audio_tokens)
  const outputImageCount = outputImageCountValue(usage)
  return { inputTokens, outputTokens, cacheReadTokens, inputImageTokens, outputImageTokens, inputAudioTokens, outputAudioTokens, outputImageCount }
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
