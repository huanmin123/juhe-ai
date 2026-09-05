import { defaultGatewaySessionIdentityResolvers } from './resolvers.js'
import type {
  GatewaySessionIdentityRawCandidate,
  GatewaySessionIdentityResolver,
  GatewaySessionIdentityResolverContext
} from './types.js'

export function listGatewaySessionIdentityResolvers(): readonly GatewaySessionIdentityResolver[] {
  return defaultGatewaySessionIdentityResolvers
}

export function collectGatewaySessionIdentityCandidates(
  context: GatewaySessionIdentityResolverContext,
  resolvers: readonly GatewaySessionIdentityResolver[] = defaultGatewaySessionIdentityResolvers
): GatewaySessionIdentityRawCandidate[] {
  return resolvers.flatMap((resolver) => resolver.collect(context))
}

