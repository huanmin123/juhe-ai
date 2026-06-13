export interface ParsedUsage {
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
  inputAudioTokens?: number
  outputAudioTokens?: number
  outputImageCount?: number
}

export function emptyUsage(): ParsedUsage {
  return {}
}

export function mergeUsage(current: ParsedUsage, next: ParsedUsage): ParsedUsage {
  return {
    inputTokens: next.inputTokens ?? current.inputTokens,
    outputTokens: next.outputTokens ?? current.outputTokens,
    cacheReadTokens: next.cacheReadTokens ?? current.cacheReadTokens,
    inputImageTokens: next.inputImageTokens ?? current.inputImageTokens,
    outputImageTokens: next.outputImageTokens ?? current.outputImageTokens,
    inputAudioTokens: next.inputAudioTokens ?? current.inputAudioTokens,
    outputAudioTokens: next.outputAudioTokens ?? current.outputAudioTokens,
    outputImageCount: next.outputImageCount ?? current.outputImageCount
  }
}

export function hasAnyUsageValue(value: ParsedUsage): boolean {
  return value.inputTokens !== undefined
    || value.outputTokens !== undefined
    || value.cacheReadTokens !== undefined
    || value.inputImageTokens !== undefined
    || value.outputImageTokens !== undefined
    || value.inputAudioTokens !== undefined
    || value.outputAudioTokens !== undefined
    || value.outputImageCount !== undefined
}
