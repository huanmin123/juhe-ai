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
  // 机密 Client 的当前密钥不会出现在列表中；管理员可通过专属对接文档重新获取。
  clientSecret?: string
}

export interface OAuthClientIntegrationPackage {
  client: OAuthClientSummary
  clientSecret?: string
}

export interface OAuthIntegrationInfo {
  issuer: string
  discoveryUrl: string
  jwksUrl: string
  authorizationEndpoint: string
  tokenEndpoint: string
  userinfoEndpoint: string
  deviceAuthorizationEndpoint: string
  revocationEndpoint: string
  tokenRenewalEndpoint: string
  idTokenSigningAlgorithm: 'RS256'
}
