export {
  createGatewayConversationKey,
  gatewaySessionIdentityMaxBytes,
  validateGatewaySessionIdentityCandidate
} from './canonicalizer.js'
export {
  collectGatewaySessionIdentityCandidates,
  listGatewaySessionIdentityResolvers
} from './registry.js'
export {
  deriveGatewaySessionAffinityKey,
  getGatewaySessionIdentity,
  resolveGatewaySessionIdentity
} from './service.js'
export type {
  GatewaySessionAffinityKeyScope,
  GatewaySessionIdentity,
  GatewaySessionIdentityCandidate,
  GatewaySessionIdentityConfidence,
  GatewaySessionIdentityConflict,
  GatewaySessionIdentityInvalidReason,
  GatewaySessionIdentityPhysicalSource,
  GatewaySessionIdentityRequest,
  GatewaySessionIdentityResolution,
  GatewaySessionIdentityResolver,
  GatewaySessionIdentityResolverContext,
  GatewaySessionIdentityScope,
  GatewaySessionIdentityStatus,
  ResolvedGatewaySessionIdentityScope
} from './types.js'
