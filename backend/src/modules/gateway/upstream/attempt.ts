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
