export interface ParsedUsageTokens {
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  thinkingTokens?: number
  imageTokens?: number
  audioTokens?: number
}

export interface UsageSemantic {
  id: string
  normalizeForStorage(usage: ParsedUsageTokens): ParsedUsageTokens
  cacheReadRateDenominator(usage: Pick<ParsedUsageTokens, 'inputTokens' | 'cacheReadTokens' | 'cacheWriteTokens'>): number
}
