export type ExternalIntegrationSourceStatus = 'active' | 'disabled'
export type ExternalIntegrationSourceTokenStatus = 'active' | 'disabled' | 'revoked'

export interface ExternalIntegrationRateLimitRule {
  windowSeconds: number
  maxRequests: number
}

export interface ExternalIntegrationSourceInput {
  name: string
  status?: ExternalIntegrationSourceStatus
  scopes?: string[]
  rateLimits?: ExternalIntegrationRateLimitRule[]
  expiresAt?: string | null
  notes?: string | null
}

export interface ExternalIntegrationSourceUpdateInput {
  name?: string
  status?: ExternalIntegrationSourceStatus
  scopes?: string[]
  rateLimits?: ExternalIntegrationRateLimitRule[]
  expiresAt?: string | null
  notes?: string | null
}

export interface ExternalIntegrationSourceTokenInput {
  sourceRefId?: string
  name: string
  token?: string
  status?: ExternalIntegrationSourceTokenStatus
  scopes?: string[]
  expiresAt?: string | null
}

export interface ExternalIntegrationSourceTokenUpdateInput {
  name?: string
  status?: ExternalIntegrationSourceTokenStatus
  scopes?: string[]
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
  source: ExternalIntegrationSourceSummary
  token: CreatedExternalIntegrationSourceToken
}

export interface ExternalIntegrationSourceTokenSecret {
  token: string
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
  tokens?: ExternalIntegrationSourceTokenSummary[]
  primaryToken?: ExternalIntegrationSourceTokenSummary
  isBuiltIn: boolean
}

export interface ExternalIntegrationSourceListResult {
  items: ExternalIntegrationSourceSummary[]
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
}

export interface ExternalIntegrationSourceListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  status?: ExternalIntegrationSourceStatus | 'all'
}

export interface ExternalIntegrationSourceAuthContext {
  sourceRefId: string
  sourceName: string
  tokenId: string
  tokenName: string
  tokenPrefix: string
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  authenticatedAt: string
  isTestToken: boolean
}

export type ExternalIntegrationSourceAuthResult =
  | { ok: true; context: ExternalIntegrationSourceAuthContext }
  | { ok: false; statusCode: 401 | 403; code: string; message: string; context?: ExternalIntegrationSourceAuthContext }

export interface ExternalIntegrationSourceTokenRow {
  source_row_id: string
  source_name: string
  source_status: string
  source_scopes_json: string
  source_rate_limits_json: string | null
  source_expires_at: string | null
  source_last_used_at: string | null
  token_id: string
  token_name: string
  token_prefix: string
  token_suffix: string
  token_status: string
  token_scopes_json: string
  token_expires_at: string | null
  token_last_used_at: string | null
}

export interface ExternalIntegrationSourceRow {
  id: string
  name: string
  status: string
  scopes_json: string
  rate_limits_json: string | null
  expires_at: string | null
  notes: string | null
  last_used_at: string | null
  created_at: string
  updated_at: string
}

export interface ExternalIntegrationSourceListRow extends ExternalIntegrationSourceRow {
  token_count: number
  active_token_count: number
}

export interface ExternalIntegrationSourceTokenListRow {
  id: string
  source_ref_id: string
  name: string
  token_secret_encrypted?: string | null
  token_prefix: string
  token_suffix: string
  status: string
  scopes_json: string
  expires_at: string | null
  last_used_at: string | null
  created_at: string
  updated_at: string
  revoked_at: string | null
}
