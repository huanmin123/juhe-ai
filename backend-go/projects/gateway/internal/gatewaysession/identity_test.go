package gatewaysession

import (
	"testing"
)

// staticRequest is a pure-Go IdentityRequest for resolver tests.
type staticRequest struct {
	originalURL string
	path        string
	headers     map[string][]string
}

func (r staticRequest) OriginalURL() string { return r.originalURL }

func (r staticRequest) Path() string { return r.path }

func (r staticRequest) HeaderValues(name string) []string {
	return r.headers[name]
}

func TestNormalizedGatewaySessionRequestPath(t *testing.T) {
	tests := []struct {
		name        string
		originalURL string
		path        string
		want        string
	}{
		{name: "originalUrl wins", originalURL: "/a?x=1", path: "/b", want: "/a"},
		{name: "falls back to path", originalURL: "", path: "/b?x=1", want: "/b"},
		{name: "query stripped and lowercased", originalURL: "/v1/Responses?x=1", want: "/responses"},
		{name: "trailing slashes trimmed", originalURL: "/responses///", want: "/responses"},
		{name: "v1 stripped", originalURL: "/v1/responses", want: "/responses"},
		{name: "v1 bare", originalURL: "/v1", want: "/"},
		{name: "v1 trailing slash", originalURL: "/v1/", want: "/"},
		{name: "v1internal prefix", originalURL: "/v1internal:x", want: "/internal:x"},
		{name: "v1foo untouched", originalURL: "/v1foo", want: "/v1foo"},
		{name: "empty", originalURL: "", want: "/"},
		{name: "root", originalURL: "/", want: "/"},
		{name: "relative path gets slash", originalURL: "responses", want: "/responses"},
		{name: "whitespace trimmed", originalURL: "  /responses  ", want: "/responses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizedGatewaySessionRequestPath(staticRequest{originalURL: tt.originalURL, path: tt.path})
			if got != tt.want {
				t.Fatalf("NormalizedGatewaySessionRequestPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGatewaySessionHeaderValues(t *testing.T) {
	request := staticRequest{
		headers: map[string][]string{"session-id": {"a", "b"}},
	}
	if got := GatewaySessionHeaderValues(request, "session-id"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("GatewaySessionHeaderValues() = %v, want [a b]", got)
	}
	if got := GatewaySessionHeaderValues(request, "other"); got != nil {
		t.Fatalf("GatewaySessionHeaderValues(other) = %v, want empty", got)
	}
	var nilRequest IdentityRequest
	if got := GatewaySessionHeaderValues(nilRequest, "session-id"); got != nil {
		t.Fatalf("nil request values = %v, want empty", got)
	}
}

func TestDefaultResolversGating(t *testing.T) {
	newContext := func(clientProfile, normalizedPath string, headers map[string][]string) ResolverContext {
		return ResolverContext{
			Request:        staticRequest{headers: headers},
			ClientProfile:  clientProfile,
			NormalizedPath: normalizedPath,
		}
	}
	codexHeaders := map[string][]string{"session-id": {"abc"}, "x-claude-code-session-id": {"zzz"}}
	tests := []struct {
		name          string
		context       ResolverContext
		wantCount     int
		wantPriority  int
		wantNamespace string
	}{
		{
			name:          "codex /responses",
			context:       newContext("codex", "/responses", codexHeaders),
			wantCount:     1,
			wantPriority:  600,
			wantNamespace: "openai.codex.session",
		},
		{
			name:      "codex /responses/compact",
			context:   newContext("codex", "/responses/compact", codexHeaders),
			wantCount: 1,
		},
		{
			name:      "codex other path",
			context:   newContext("codex", "/chat/completions", codexHeaders),
			wantCount: 0,
		},
		{
			name:          "claude code /messages",
			context:       newContext("claude_code", "/messages", codexHeaders),
			wantCount:     1,
			wantNamespace: "anthropic.claude_code.session",
		},
		{
			name:      "claude code other path",
			context:   newContext("claude_code", "/responses", codexHeaders),
			wantCount: 0,
		},
		{
			name:      "other profile",
			context:   newContext("gemini", "/messages", codexHeaders),
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := CollectGatewaySessionIdentityCandidates(tt.context, nil)
			if len(candidates) != tt.wantCount {
				t.Fatalf("candidate count = %d, want %d", len(candidates), tt.wantCount)
			}
			if tt.wantCount == 0 {
				return
			}
			candidate := candidates[0]
			if candidate.Priority != 600 {
				t.Fatalf("priority = %d, want 600", candidate.Priority)
			}
			if candidate.Confidence != IdentityConfidenceAuthoritative {
				t.Fatalf("confidence = %q", candidate.Confidence)
			}
			if tt.wantNamespace != "" && candidate.SemanticNamespace != tt.wantNamespace {
				t.Fatalf("namespace = %q, want %q", candidate.SemanticNamespace, tt.wantNamespace)
			}
			if candidate.Source != (IdentityPhysicalSource{Location: IdentitySourceLocationHeader, Path: candidate.Source.Path}) {
				t.Fatalf("source = %+v", candidate.Source)
			}
		})
	}
	if resolvers := ListGatewaySessionIdentityResolvers(); len(resolvers) != 2 {
		t.Fatalf("default resolver count = %d, want 2", len(resolvers))
	}
}

func TestResolveGatewaySessionIdentityStates(t *testing.T) {
	service, err := NewIdentityService(testHMACSecret)
	if err != nil {
		t.Fatalf("NewIdentityService: %v", err)
	}
	scope := IdentityScope{ClientProfile: "codex", SystemAccountID: "sys-1", APIKeyID: "key-9"}

	t.Run("missing", func(t *testing.T) {
		identity, err := service.Resolve(staticRequest{originalURL: "/v1/responses", headers: map[string][]string{}}, scope, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if identity.Status != IdentityStatusMissing || identity.Resolution != IdentityResolutionMissing {
			t.Fatalf("status = %q resolution = %q", identity.Status, identity.Resolution)
		}
	})

	t.Run("invalid control character", func(t *testing.T) {
		identity, err := service.Resolve(staticRequest{
			originalURL: "/v1/responses",
			headers:     map[string][]string{"session-id": {"bad\x00value"}},
		}, scope, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if identity.Status != IdentityStatusInvalid || identity.Resolution != IdentityResolutionInvalid {
			t.Fatalf("status = %q resolution = %q", identity.Status, identity.Resolution)
		}
		if len(identity.Candidates) != 1 || identity.Candidates[0].Valid {
			t.Fatalf("candidates = %+v", identity.Candidates)
		}
		if identity.Candidates[0].InvalidReason != IdentityInvalidReasonControlCharacter {
			t.Fatalf("invalidReason = %q", identity.Candidates[0].InvalidReason)
		}
	})

	t.Run("resolved single candidate", func(t *testing.T) {
		identity, err := service.Resolve(staticRequest{
			originalURL: "/v1/responses",
			headers:     map[string][]string{"session-id": {"abc-session-123"}},
		}, scope, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if identity.Status != IdentityStatusResolved || identity.Resolution != IdentityResolutionOfficial {
			t.Fatalf("status = %q resolution = %q", identity.Status, identity.Resolution)
		}
		if identity.SessionID != "abc-session-123" {
			t.Fatalf("sessionId = %q", identity.SessionID)
		}
		wantConversationKey := "conv_v1_7DCcaz4omcf-A6GykddpbCv9_8Fu0sZv6V5I1BtZzsk"
		if identity.ConversationKey != wantConversationKey {
			t.Fatalf("conversationKey = %q, want %q", identity.ConversationKey, wantConversationKey)
		}
		if identity.SemanticNamespace != "openai.codex.session" {
			t.Fatalf("semanticNamespace = %q", identity.SemanticNamespace)
		}
		if len(identity.Candidates) != 1 || !identity.Candidates[0].Valid {
			t.Fatalf("candidates = %+v", identity.Candidates)
		}
		if len(identity.Conflicts) != 0 {
			t.Fatalf("conflicts = %+v", identity.Conflicts)
		}
		if identity.Source == nil || identity.Source.Path != "session-id" {
			t.Fatalf("source = %+v", identity.Source)
		}
	})

	t.Run("conflict between two values", func(t *testing.T) {
		identity, err := service.Resolve(staticRequest{
			originalURL: "/v1/responses",
			headers:     map[string][]string{"session-id": {"value-1", "value-2"}},
		}, scope, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if identity.Status != IdentityStatusConflict || identity.Resolution != IdentityResolutionConflict {
			t.Fatalf("status = %q resolution = %q", identity.Status, identity.Resolution)
		}
		if len(identity.Conflicts) != 1 {
			t.Fatalf("conflicts = %+v", identity.Conflicts)
		}
		conflict := identity.Conflicts[0]
		if conflict.Priority != 600 {
			t.Fatalf("conflict priority = %d", conflict.Priority)
		}
		if len(conflict.EvidenceKeys) != 2 {
			t.Fatalf("evidenceKeys = %+v", conflict.EvidenceKeys)
		}
		if conflict.Kind != IdentitySemanticKindSession {
			t.Fatalf("kind = %q", conflict.Kind)
		}
	})

	t.Run("same value across resolvers resolves and dedupes sources", func(t *testing.T) {
		request := staticRequest{
			originalURL: "/v1/responses",
			headers:     map[string][]string{"session-id": {"same"}},
		}
		// Both resolvers run but only one matches; emulate duplicate evidence
		// through a custom resolver list.
		duplicate := duplicateSessionResolver{}
		identity, err := service.Resolve(request, scope, []Resolver{duplicate})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if identity.Status != IdentityStatusResolved {
			t.Fatalf("status = %q", identity.Status)
		}
		if len(identity.Sources) != 2 {
			t.Fatalf("sources = %+v, want 2 deduped entries for distinct paths", identity.Sources)
		}
	})
}

type duplicateSessionResolver struct{}

func (duplicateSessionResolver) ID() string { return "duplicate_session" }

func (duplicateSessionResolver) Collect(context ResolverContext) []RawCandidate {
	values := GatewaySessionHeaderValues(context.Request, "session-id")
	candidates := make([]RawCandidate, 0, len(values)*2)
	for _, rawValue := range values {
		for _, path := range []string{"session-id", "x-session-id"} {
			candidates = append(candidates, RawCandidate{
				ResolverID:        "duplicate_session",
				SemanticKind:      IdentitySemanticKindSession,
				SemanticNamespace: "openai.codex.session",
				Source:            IdentityPhysicalSource{Location: IdentitySourceLocationHeader, Path: path},
				Confidence:        IdentityConfidenceAuthoritative,
				Priority:          600,
				RawValue:          rawValue,
			})
		}
	}
	return candidates
}

func TestIdentityServiceDeriveAffinityKeyFallback(t *testing.T) {
	service, err := NewIdentityService(testHMACSecret)
	if err != nil {
		t.Fatalf("NewIdentityService: %v", err)
	}
	got, err := service.DeriveGatewaySessionAffinityKey("conv-target", GatewaySessionAffinityKeyScope{
		SystemAccountID:           "sys-1",
		APIKeyID:                  "key-9",
		RouteStrategyID:           "rs-7",
		GroupID:                   "grp-3",
		ProviderProtocolProfileID: "ppp-5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "aff_v1_cxz8enf_7tT2pHY8Ynm7eLTRkYTcusiCnCvkTvL2SDM"
	if got != want {
		t.Fatalf("DeriveGatewaySessionAffinityKey() = %q, want %q", got, want)
	}
	if empty, err := service.DeriveGatewaySessionAffinityKey("", GatewaySessionAffinityKeyScope{}); empty != "" || err != nil {
		t.Fatalf("empty conversation key = %q, err %v", empty, err)
	}
	if _, err := NewIdentityService("   "); err == nil {
		t.Fatal("expected empty-secret construction error")
	}
}
