import type { AccountUsageStatsRange } from '@/types/domain'

export type AiPerformanceRequestChannel = 'base' | 'series'

export interface AiPerformanceRequestToken {
  channel: AiPerformanceRequestChannel
  generation: number
  signature: string
}

export interface AiPerformanceRequestSignatureInput {
  channel: AiPerformanceRequestChannel
  scope: 'admin' | 'self'
  authRevision: number
  viewerId?: string
  viewerRole?: string
  ownerSystemAccountId?: string
  startDate?: string
  endDate?: string
  accountIds?: string[]
}

export function buildAiPerformanceRequestSignature(input: AiPerformanceRequestSignatureInput): string {
  return JSON.stringify([
    input.channel,
    input.scope,
    input.authRevision,
    input.viewerId ?? 'anonymous',
    input.viewerRole ?? 'anonymous',
    input.ownerSystemAccountId ?? '',
    input.startDate ?? 'default',
    input.endDate ?? 'default',
    [...new Set(input.accountIds ?? [])].sort()
  ])
}

export function createAiPerformanceRequestGate() {
  const generations: Record<AiPerformanceRequestChannel, number> = { base: 0, series: 0 }
  let active = true

  function begin(channel: AiPerformanceRequestChannel, signature: string): AiPerformanceRequestToken {
    return { channel, generation: ++generations[channel], signature }
  }

  function isCurrent(token: AiPerformanceRequestToken, currentSignature: string): boolean {
    return active
      && token.generation === generations[token.channel]
      && token.signature === currentSignature
  }

  function acceptsRange(
    token: AiPerformanceRequestToken,
    currentSignature: string,
    range: AccountUsageStatsRange,
    expectedRange: { startDate?: string; endDate?: string }
  ): boolean {
    if (!isCurrent(token, currentSignature)) return false
    if (expectedRange.startDate === undefined && expectedRange.endDate === undefined) {
      return token.channel === 'base'
    }
    return expectedRange.startDate === range.startDate && expectedRange.endDate === range.endDate
  }

  function invalidate(channel?: AiPerformanceRequestChannel): void {
    if (channel) {
      generations[channel] += 1
      return
    }
    generations.base += 1
    generations.series += 1
  }

  function deactivate(): void {
    active = false
    invalidate()
  }

  function activate(): void {
    active = true
  }

  return { acceptsRange, activate, begin, deactivate, invalidate, isCurrent }
}
