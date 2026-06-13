export interface UpstreamAttempt {
  accountId: string
  accountName: string
  upstreamUrl: string
  status?: number
  message?: string
  responseHeaders?: Record<string, string>
  responseBodyText?: string
}
