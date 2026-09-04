package gatewaysession

import (
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// G05 port adapters. The preflight consumes exactly these seams
// (gatewaypreauth/ports.go):
//
//   - SessionIdentityResolver — session-identity/index.ts (resolve);
//   - SessionAffinity — resolveOpenAIGatewaySessionAffinityKey{,FromClientSource}.
//
// The compile-time assertions pin the contract match.

var (
	_ gatewaypreauth.SessionIdentityResolver = (*IdentityService)(nil)
	_ gatewaypreauth.SessionAffinity         = (*AffinityService)(nil)
)

// ResolveGatewaySessionIdentity mirrors resolveGatewaySessionIdentity through
// the G05 port shape. The full identity (candidates, conflicts, sources) is
// available via Resolve; the port freezes the sessionId / conversationKey
// projection. Errors cannot occur after construction-time secret validation.
func (s *IdentityService) ResolveGatewaySessionIdentity(req *gatewaypreauth.GatewayRequest, input gatewaypreauth.SessionIdentityInput) gatewaypreauth.SessionIdentity {
	identity, err := s.Resolve(gatewayRequestIdentityView{req: req}, IdentityScope{
		ClientProfile:   input.ClientProfile,
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
	}, nil)
	if err != nil {
		return gatewaypreauth.SessionIdentity{}
	}
	return gatewaypreauth.SessionIdentity{
		SessionID:       identity.SessionID,
		ConversationKey: identity.ConversationKey,
	}
}

// ResolveKey mirrors resolveOpenAIGatewaySessionAffinityKey through the G05
// port shape.
func (s *AffinityService) ResolveKey(identity gatewaypreauth.SessionIdentity, scope gatewaypreauth.SessionAffinityScope) (string, bool) {
	return s.ResolveOpenAIGatewaySessionAffinityKey(identity.ConversationKey, GatewaySessionAffinityKeyScope{
		SystemAccountID:           scope.SystemAccountID,
		APIKeyID:                  scope.APIKeyID,
		GroupID:                   scope.GroupID,
		RouteStrategyID:           scope.RouteStrategyID,
		ProviderProtocolProfileID: scope.ProviderProtocolProfileID,
	})
}

// ResolveKeyFromClientSource mirrors
// resolveOpenAIGatewaySessionAffinityKeyFromClientSource through the G05 port
// shape. The port freezes the client source to its session identity; the
// ConversationKey carries the Node affinityKey projection (the official
// conversation key or the protocol-resource key).
func (s *AffinityService) ResolveKeyFromClientSource(clientSource *gatewaypreauth.ClientSource, scope gatewaypreauth.SessionAffinityScope) (string, bool) {
	affinityKey := ""
	if clientSource != nil && clientSource.SessionIdentity != nil {
		affinityKey = clientSource.SessionIdentity.ConversationKey
	}
	return s.ResolveOpenAIGatewaySessionAffinityKeyFromClientSource(affinityKey, GatewaySessionAffinityKeyScope{
		SystemAccountID:           scope.SystemAccountID,
		APIKeyID:                  scope.APIKeyID,
		GroupID:                   scope.GroupID,
		RouteStrategyID:           scope.RouteStrategyID,
		ProviderProtocolProfileID: scope.ProviderProtocolProfileID,
	})
}

// gatewayRequestIdentityView adapts gatewaypreauth.GatewayRequest to the
// IdentityRequest projection: originalUrl/path plus multi-value header access
// (the headersDistinct projection).
type gatewayRequestIdentityView struct {
	req *gatewaypreauth.GatewayRequest
}

// OriginalURL mirrors request.originalUrl.
func (v gatewayRequestIdentityView) OriginalURL() string {
	if v.req == nil {
		return ""
	}
	return v.req.PathAndQuery()
}

// Path mirrors request.path.
func (v gatewayRequestIdentityView) Path() string {
	if v.req == nil {
		return ""
	}
	return v.req.Path()
}

// HeaderValues mirrors request.headersDistinct[name]: every value sent for
// the (case-insensitive) header name, in arrival order.
func (v gatewayRequestIdentityView) HeaderValues(name string) []string {
	if v.req == nil || v.req.HTTP == nil || v.req.HTTP.Header == nil {
		return nil
	}
	return http.Header(v.req.HTTP.Header).Values(name)
}
