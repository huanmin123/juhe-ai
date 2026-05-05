export interface ApiKeySummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  keyPrefix: string
  key: string
  status: 'active' | 'disabled'
  groupId: string
  groupAuthorizationId?: string
  expiresAt?: string
}

export interface CreatedApiKey extends ApiKeySummary {}

export interface OpenAIAuthURLResult {
  authUrl: string
  sessionId: string
}

export interface ProxyProfileSummary {
  id: string
  name: string
  description?: string
  type: 'http' | 'https' | 'socks5' | string
  host: string
  port: number
  username?: string
  enabled: boolean
  testStatus: string
  lastTestedAt?: string
}
