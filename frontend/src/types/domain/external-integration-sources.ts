export type ExternalIntegrationSourceStatus = 'active' | 'disabled'
export type ExternalIntegrationSourceTokenStatus = 'active' | 'disabled' | 'revoked'

export interface ExternalIntegrationRateLimitRule {
  windowSeconds: number
  maxRequests: number
}

export interface ExternalIntegrationScopeOption {
  value: string
  label: string
}

export interface ExternalIntegrationSourceTokenSummary {
  id: string
  name: string
  tokenPrefix: string
  tokenSuffix: string
  status: ExternalIntegrationSourceTokenStatus
  scopes: string[]
  expiresAt?: string
  lastUsedAt?: string
  createdAt: string
  updatedAt: string
  revokedAt?: string
  isBuiltIn: boolean
}

export interface ExternalIntegrationSourceSummary {
  id: string
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  expiresAt?: string
  notes?: string
  lastUsedAt?: string
  createdAt: string
  updatedAt: string
  tokenCount: number
  activeTokenCount: number
  tokens: ExternalIntegrationSourceTokenSummary[]
  isBuiltIn: boolean
}

export interface ExternalIntegrationSourcePrimaryTokenSummary {
  id: string
  tokenPrefix: string
  tokenSuffix: string
}

export interface ExternalIntegrationSourceListItem {
  id: string
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  expiresAt?: string
  notes?: string
  lastUsedAt?: string
  primaryToken?: ExternalIntegrationSourcePrimaryTokenSummary
  isBuiltIn: boolean
}

export interface ExternalIntegrationSourceListResult {
  items: ExternalIntegrationSourceListItem[]
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
}

export interface ExternalIntegrationSourcePayload {
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  expiresAt?: string | null
  notes?: string | null
}

export interface ExternalIntegrationSourceTokenPayload {
  name: string
  status?: ExternalIntegrationSourceTokenStatus
  scopes: string[]
  expiresAt?: string | null
}

export interface CreatedExternalIntegrationSourceToken {
  id: string
  name: string
  token: string
  tokenPrefix: string
  tokenSuffix: string
  scopes: string[]
  expiresAt?: string
}

export interface CreatedExternalIntegrationSourceAuthorization {
  token: CreatedExternalIntegrationSourceToken
}

export interface ExternalIntegrationSourceTokenSecretResult {
  token: string
}

export type ExternalPublicApiMethod = 'GET' | 'POST' | 'PATCH' | 'DELETE'
export type ExternalPublicApiStatus = 'available' | 'mock'

export interface ExternalPublicApiField {
  name: string
  type: string
  required: boolean
  description: string
  example?: unknown
}

export interface ExternalPublicApiHeader {
  name: string
  required: boolean
  description: string
  example: string
}

export interface ExternalPublicApiBody {
  contentType: string
  fields: ExternalPublicApiField[]
  example: unknown
}

export interface ExternalPublicApiDocItem {
  id: string
  name: string
  summary: string
  status: ExternalPublicApiStatus
  method: ExternalPublicApiMethod
  path: string
  scope?: string
  headers: ExternalPublicApiHeader[]
  query: ExternalPublicApiField[]
  requestBody?: ExternalPublicApiBody
  responseFields: ExternalPublicApiField[]
  responseExample: unknown
}

export interface ExternalPublicApiCatalog {
  basePath: string
  authType: 'Bearer'
  items: ExternalPublicApiDocItem[]
}
