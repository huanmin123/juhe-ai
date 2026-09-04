package gatewaysession

// ListGatewaySessionIdentityResolvers mirrors listGatewaySessionIdentityResolvers.
func ListGatewaySessionIdentityResolvers() []Resolver {
	return DefaultGatewaySessionIdentityResolvers
}

// CollectGatewaySessionIdentityCandidates mirrors
// collectGatewaySessionIdentityCandidates: resolvers == nil selects the
// default resolver set; candidate order is resolver order, then collection
// order (the Node flatMap order).
func CollectGatewaySessionIdentityCandidates(context ResolverContext, resolvers []Resolver) []RawCandidate {
	if resolvers == nil {
		resolvers = DefaultGatewaySessionIdentityResolvers
	}
	var candidates []RawCandidate
	for _, resolver := range resolvers {
		candidates = append(candidates, resolver.Collect(context)...)
	}
	return candidates
}
