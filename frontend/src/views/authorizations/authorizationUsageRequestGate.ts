import type { AccountUsageStatsRange } from '@/types/domain'

export type AuthorizationUsageRequestChannel = 'rows' | 'summary'

export interface AuthorizationUsageRequestToken {
  channel: AuthorizationUsageRequestChannel
  generation: number
  signature: string
}

export function buildAuthorizationUsageSignature(input: {
  kind: 'team' | 'user'
  scope: 'admin' | 'self'
  authRevision: number
  viewerId?: string
  viewerRole?: string
  ownerSystemAccountId?: string
  startDate?: string
  endDate?: string
  resourceType?: string
  resourceId?: string
  teamId?: string
  granteeSystemAccountId?: string
}): string {
  return JSON.stringify([
    input.kind,
    input.scope,
    input.authRevision,
    input.viewerId ?? '',
    input.viewerRole ?? '',
    input.ownerSystemAccountId ?? '',
    input.startDate ?? 'default',
    input.endDate ?? 'default',
    input.resourceType ?? 'all',
    input.resourceId ?? '',
    input.teamId ?? '',
    input.granteeSystemAccountId ?? ''
  ])
}

export function createAuthorizationUsageRequestGate() {
  const generations: Record<AuthorizationUsageRequestChannel, number> = { rows: 0, summary: 0 }
  let active = true
  let resolvedSignature = ''
  let resolvedRangeKey = ''

  function beginBatch(signature: string): void {
    resolvedSignature = signature
    resolvedRangeKey = ''
  }

  function begin(channel: AuthorizationUsageRequestChannel, signature: string): AuthorizationUsageRequestToken {
    if (resolvedSignature !== signature) {
      resolvedSignature = signature
      resolvedRangeKey = ''
    }
    return { channel, generation: ++generations[channel], signature }
  }

  function isCurrent(token: AuthorizationUsageRequestToken, currentSignature: string): boolean {
    return active
      && token.signature === currentSignature
      && token.generation === generations[token.channel]
  }

  function acceptRange(token: AuthorizationUsageRequestToken, currentSignature: string, range: AccountUsageStatsRange): boolean {
    if (!isCurrent(token, currentSignature)) return false
    const rangeKey = `${range.startDate}:${range.endDate}`
    if (resolvedSignature !== token.signature) {
      resolvedSignature = token.signature
      resolvedRangeKey = rangeKey
      return true
    }
    if (!resolvedRangeKey) {
      resolvedRangeKey = rangeKey
      return true
    }
    return resolvedRangeKey === rangeKey
  }

  function deactivate(): void {
    active = false
    generations.rows += 1
    generations.summary += 1
  }

  function activate(): void {
    active = true
  }

  return { acceptRange, activate, begin, beginBatch, deactivate, isCurrent }
}
