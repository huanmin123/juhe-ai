package gatewaysession

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestPortSessionIdentityResolver(t *testing.T) {
	service, err := NewIdentityService(testHMACSecret)
	if err != nil {
		t.Fatalf("NewIdentityService: %v", err)
	}
	var port gatewaypreauth.SessionIdentityResolver = service

	raw := httptest.NewRequest("POST", "/v1/responses?x=1", nil)
	raw.Header.Set("Session-Id", "abc-session-123")
	req := gatewaypreauth.NewGatewayRequest(raw)

	identity := port.ResolveGatewaySessionIdentity(req, gatewaypreauth.SessionIdentityInput{
		ClientProfile:   "codex",
		SystemAccountID: "sys-1",
		APIKeyID:        "key-9",
	})
	if identity.SessionID != "abc-session-123" {
		t.Fatalf("sessionId = %q", identity.SessionID)
	}
	want := "conv_v1_7DCcaz4omcf-A6GykddpbCv9_8Fu0sZv6V5I1BtZzsk"
	if identity.ConversationKey != want {
		t.Fatalf("conversationKey = %q, want %q", identity.ConversationKey, want)
	}

	// Missing header: empty projection.
	empty := port.ResolveGatewaySessionIdentity(gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil)),
		gatewaypreauth.SessionIdentityInput{ClientProfile: "codex", SystemAccountID: "sys-1"})
	if empty.SessionID != "" || empty.ConversationKey != "" {
		t.Fatalf("missing identity = %+v", empty)
	}
}

func TestPortSessionAffinity(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	var port gatewaypreauth.SessionAffinity = service

	// ResolveKey: the Node resolveOpenAIGatewaySessionAffinityKey contract.
	key, ok := port.ResolveKey(gatewaypreauth.SessionIdentity{ConversationKey: "conv-target"}, gatewaypreauth.SessionAffinityScope{
		SystemAccountID:           "sys-1",
		APIKeyID:                  "key-9",
		GroupID:                   "grp-3",
		RouteStrategyID:           "rs-7",
		ProviderProtocolProfileID: "ppp-5",
	})
	if !ok || key != "aff_v1_cxz8enf_7tT2pHY8Ynm7eLTRkYTcusiCnCvkTvL2SDM" {
		t.Fatalf("ResolveKey = (%q, %v)", key, ok)
	}
	if key, ok := port.ResolveKey(gatewaypreauth.SessionIdentity{}, gatewaypreauth.SessionAffinityScope{GroupID: "g"}); ok || key != "" {
		t.Fatalf("ResolveKey without conversation key = (%q, %v)", key, ok)
	}

	// ResolveKeyFromClientSource: trims, blank never creates affinity.
	clientSource := &gatewaypreauth.ClientSource{SessionIdentity: &gatewaypreauth.SessionIdentity{ConversationKey: "  conv-target  "}}
	key, ok = port.ResolveKeyFromClientSource(clientSource, gatewaypreauth.SessionAffinityScope{
		SystemAccountID:           "sys-1",
		APIKeyID:                  "key-9",
		GroupID:                   "grp-3",
		RouteStrategyID:           "rs-7",
		ProviderProtocolProfileID: "ppp-5",
	})
	if !ok || key != "aff_v1_cxz8enf_7tT2pHY8Ynm7eLTRkYTcusiCnCvkTvL2SDM" {
		t.Fatalf("ResolveKeyFromClientSource = (%q, %v)", key, ok)
	}
	if _, ok := port.ResolveKeyFromClientSource(&gatewaypreauth.ClientSource{SessionIdentity: &gatewaypreauth.SessionIdentity{ConversationKey: "   "}}, gatewaypreauth.SessionAffinityScope{GroupID: "g"}); ok {
		t.Fatal("blank affinity key must not create affinity")
	}
	if _, ok := port.ResolveKeyFromClientSource(nil, gatewaypreauth.SessionAffinityScope{GroupID: "g"}); ok {
		t.Fatal("nil client source must not create affinity")
	}
}

func TestPortScopeDefaultsMatchNodeFallbacks(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	// Missing apiKeyId / routeStrategyId / providerProtocolProfileId map to
	// the Node 'internal' / 'default' placeholders.
	key, ok := service.ResolveOpenAIGatewaySessionAffinityKey("conv-target", GatewaySessionAffinityKeyScope{
		SystemAccountID: "sys-1",
		APIKeyID:        "internal",
		GroupID:         "grp-3",
		RouteStrategyID: "default",
	})
	if !ok {
		t.Fatal("expected key")
	}
	portKey, _ := service.ResolveKey(gatewaypreauth.SessionIdentity{ConversationKey: "conv-target"}, gatewaypreauth.SessionAffinityScope{
		SystemAccountID: "sys-1",
		GroupID:         "grp-3",
	})
	if portKey != key {
		t.Fatalf("port fallback = %q, want %q", portKey, key)
	}
	_ = context.Background
}
