export type GatewaySessionIdentityStatus = 'resolved' | 'missing' | 'conflict' | 'invalid'

export type GatewaySessionIdentityResolution = 'official' | 'missing' | 'conflict' | 'invalid'

export type GatewaySessionIdentityConfidence = 'authoritative'

export type GatewaySessionIdentityInvalidReason = 'empty' | 'control_character' | 'too_long' | 'invalid_shape'

export interface GatewaySessionIdentityPhysicalSource {
  location: 'header'
  path: string
}

export interface GatewaySessionIdentityCandidate {
  resolverId: string
  semanticKind: 'session'
  semanticNamespace: string
  source: GatewaySessionIdentityPhysicalSource
  confidence: GatewaySessionIdentityConfidence
  priority: number
  valid: boolean
  evidenceKey?: string
  invalidReason?: GatewaySessionIdentityInvalidReason
}

export interface GatewaySessionIdentityConflict {
  kind: 'session'
  priority: number
  sources: GatewaySessionIdentityPhysicalSource[]
  evidenceKeys: string[]
}

export interface GatewaySessionIdentity {
  status: GatewaySessionIdentityStatus
  resolution: GatewaySessionIdentityResolution
  sessionId?: string
  conversationKey?: string
  semanticNamespace?: string
  source?: GatewaySessionIdentityPhysicalSource
  sources: GatewaySessionIdentityPhysicalSource[]
  confidence?: GatewaySessionIdentityConfidence
  candidates: GatewaySessionIdentityCandidate[]
  conflicts: GatewaySessionIdentityConflict[]
}

export interface GatewaySessionIdentityRequest {
  method: string
  originalUrl?: string
  path?: string
  headers?: Record<string, string | readonly string[] | undefined>
  headersDistinct?: Record<string, readonly string[] | undefined>
  rawHeaders?: readonly string[]
  header?(name: string): string | undefined
}

export interface GatewaySessionIdentityScope {
  clientProfile: string
  systemAccountId: string
  apiKeyId?: string
  hmacSecret?: string
}

export interface ResolvedGatewaySessionIdentityScope extends GatewaySessionIdentityScope {
  hmacSecret: string
}

export interface GatewaySessionIdentityResolverContext {
  request: GatewaySessionIdentityRequest
  clientProfile: string
  normalizedPath: string
}

export interface GatewaySessionIdentityResolver {
  id: string
  collect(context: GatewaySessionIdentityResolverContext): GatewaySessionIdentityRawCandidate[]
}

export interface GatewaySessionIdentityRawCandidate {
  resolverId: string
  semanticKind: 'session'
  semanticNamespace: string
  source: GatewaySessionIdentityPhysicalSource
  confidence: GatewaySessionIdentityConfidence
  priority: number
  rawValue: unknown
  invalidShape?: boolean
}

export interface GatewaySessionAffinityKeyScope {
  hmacSecret?: string
  systemAccountId: string
  apiKeyId?: string
  routeStrategyId?: string
  groupId: string
  providerProtocolProfileId?: string
}

export interface ValidatedGatewaySessionIdentityCandidate extends GatewaySessionIdentityRawCandidate {
  rawValue: string
  evidenceKey: string
}
