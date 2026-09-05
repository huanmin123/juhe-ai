import type { UsageServiceTier } from './service-tier.js'

export interface ParsedUsage {
  upstreamResponseModel?: string
  serviceTier?: UsageServiceTier
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  cacheWrite1hTokens?: number
  thinkingTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
  inputAudioTokens?: number
  outputAudioTokens?: number
  outputImageCount?: number
}

export function emptyUsage(): ParsedUsage {
  return {}
}

export function usageWithObservedUpstreamResponseModel(
  usage: ParsedUsage,
  observedUpstreamResponseModel?: string
): ParsedUsage {
  return observedUpstreamResponseModel
    ? { ...usage, upstreamResponseModel: observedUpstreamResponseModel }
    : usage
}

export function mergeUsage(current: ParsedUsage, next: ParsedUsage): ParsedUsage {
  return {
    upstreamResponseModel: next.upstreamResponseModel ?? current.upstreamResponseModel,
    serviceTier: next.serviceTier ?? current.serviceTier,
    inputTokens: next.inputTokens ?? current.inputTokens,
    outputTokens: next.outputTokens ?? current.outputTokens,
    cacheReadTokens: next.cacheReadTokens ?? current.cacheReadTokens,
    cacheWriteTokens: next.cacheWriteTokens ?? current.cacheWriteTokens,
    cacheWrite1hTokens: next.cacheWrite1hTokens ?? current.cacheWrite1hTokens,
    thinkingTokens: next.thinkingTokens ?? current.thinkingTokens,
    inputImageTokens: next.inputImageTokens ?? current.inputImageTokens,
    outputImageTokens: next.outputImageTokens ?? current.outputImageTokens,
    inputAudioTokens: next.inputAudioTokens ?? current.inputAudioTokens,
    outputAudioTokens: next.outputAudioTokens ?? current.outputAudioTokens,
    outputImageCount: next.outputImageCount ?? current.outputImageCount
  }
}

export function hasUpstreamResponseModelMismatch(
  upstreamModel?: string,
  upstreamResponseModel?: string
): boolean {
  const sentModel = upstreamModel?.trim()
  const responseModel = upstreamResponseModel?.trim()
  return Boolean(sentModel && responseModel && sentModel !== responseModel)
}

export function hasAnyUsageValue(value: ParsedUsage): boolean {
  return value.serviceTier !== undefined
    || value.inputTokens !== undefined
    || value.outputTokens !== undefined
    || value.cacheReadTokens !== undefined
    || value.cacheWriteTokens !== undefined
    || value.cacheWrite1hTokens !== undefined
    || value.thinkingTokens !== undefined
    || value.inputImageTokens !== undefined
    || value.outputImageTokens !== undefined
    || value.inputAudioTokens !== undefined
    || value.outputAudioTokens !== undefined
    || value.outputImageCount !== undefined
}
