export interface UpstreamAttempt {
  accountId: string
  accountName: string
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  upstreamUrl: string
  status?: number
  message?: string
  responseHeaders?: Record<string, string>
  responseBodyText?: string
}

export function isRealUpstreamAttempt(attempt: Pick<UpstreamAttempt, 'upstreamUrl'>): boolean {
  try {
    const url = new URL(attempt.upstreamUrl)
    return url.protocol === 'http:' || url.protocol === 'https:'
  } catch {
    return false
  }
}

export function isCompletedRealUpstreamAttempt(
  attempt: Pick<UpstreamAttempt, 'upstreamUrl' | 'status'>
): boolean {
  return isRealUpstreamAttempt(attempt)
    && typeof attempt.status === 'number'
    && Number.isFinite(attempt.status)
}
