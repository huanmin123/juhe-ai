export type OAuthClientType = 'public' | 'confidential'
export type OAuthClientStatus = 'active' | 'disabled'

export interface OAuthClientSummary {
  id: string
  clientId: string
  displayName: string
  clientType: OAuthClientType
  redirectUris: string[]
  allowedScopes: string[]
  status: OAuthClientStatus
  createdAt: string
  updatedAt: string
}

export interface OAuthClientCreatePayload {
  displayName: string
  clientType: OAuthClientType
  redirectUris: string[]
  allowedScopes: string[]
}

export interface CreatedOAuthClient extends OAuthClientSummary {
  // 机密 Client 的密钥只在创建响应中出现一次，后续列表不包含该字段。
  clientSecret?: string
}

export interface OAuthSigningKeyRotationResult {
  kid: string
  status: 'active'
  createdAt: string
}

export type OAuthConnectedApplicationStatus = 'active' | 'disabled' | 'expired' | 'revoked' | 'invalid'

export interface OAuthConnectedApplicationSummary {
  clientId: string
  displayName: string
  clientType?: OAuthClientType
  iconUrl?: string
  websiteUrl?: string
  systemAccountId?: string
  systemAccountName?: string
  scopes: string[]
  status: OAuthConnectedApplicationStatus
  statusReason?: string
  grantedAt?: string
  authorizedAt?: string
  expiresAt?: string
  lastTokenRenewedAt?: string
  lastTokenRotatedAt?: string
  lastUsedAt?: string
  createdAt?: string
  updatedAt?: string
}

export interface OAuthConnectedApplicationListResult {
  items: OAuthConnectedApplicationSummary[]
}
