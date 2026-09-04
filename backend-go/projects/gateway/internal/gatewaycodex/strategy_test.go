package gatewaycodex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// ---------------------------------------------------------------------------
// source identity
// ---------------------------------------------------------------------------

type fakeSessionResolver struct {
	identity gatewaysession.GatewaySessionIdentity
	calls    int
	last     gatewaypreauth.SessionIdentityInput
}

func (f *fakeSessionResolver) ResolveSessionIdentity(_ *gatewaypreauth.GatewayRequest, input gatewaypreauth.SessionIdentityInput) gatewaysession.GatewaySessionIdentity {
	f.calls++
	f.last = input
	return f.identity
}

func newSourceResolver(session SessionIdentityResolver) *SourceIdentityResolver {
	return &SourceIdentityResolver{
		Secret:  "test-secret",
		Session: session,
	}
}

func TestResolveGatewayClientSourceIdentity(t *testing.T) {
	resolvedSession := gatewaysession.GatewaySessionIdentity{
		Status:            gatewaysession.IdentityStatusResolved,
		SessionID:         "sess-1",
		ConversationKey:   "conv-1",
		SemanticNamespace: "com.openai.codex.session",
	}
	invalidSession := gatewaysession.GatewaySessionIdentity{Status: gatewaysession.IdentityStatusInvalid}
	conflictSession := gatewaysession.GatewaySessionIdentity{Status: gatewaysession.IdentityStatusConflict}
	missingSession := gatewaysession.GatewaySessionIdentity{Status: gatewaysession.IdentityStatusMissing}

	baseInput := GatewayClientSourceIdentityInput{
		ClientProfile:       "codex",
		ClientProfileSource: "codex_turn_metadata",
		DownstreamProtocol:  "responses_sse",
		SystemAccountID:     "sys",
		APIKeyID:            "key",
		ClientIP:            "10.0.0.1",
	}

	tests := []struct {
		name          string
		input         GatewayClientSourceIdentityInput
		session       *gatewaysession.GatewaySessionIdentity
		wantStatus    string
		wantKind      string
		wantSourceKey bool
		wantAffinity  string
	}{
		{name: "missing system account", input: func() GatewayClientSourceIdentityInput {
			in := baseInput
			in.SystemAccountID = " "
			return in
		}(), wantStatus: SourceStatusMissing},
		{name: "official session", input: baseInput, session: &resolvedSession, wantStatus: SourceStatusResolved, wantKind: SourceKindOfficialSession, wantSourceKey: true, wantAffinity: "conv-1"},
		{name: "invalid session propagates", input: baseInput, session: &invalidSession, wantStatus: SourceStatusInvalid},
		{name: "conflict session propagates", input: baseInput, session: &conflictSession, wantStatus: SourceStatusConflict},
		{name: "ip fallback", input: baseInput, session: &missingSession, wantStatus: SourceStatusResolved, wantKind: SourceKindIPAPIKeyFallback, wantSourceKey: true},
		{name: "missing client ip", input: func() GatewayClientSourceIdentityInput {
			in := baseInput
			in.ClientIP = ""
			return in
		}(), session: &missingSession, wantStatus: SourceStatusMissing},
		{name: "codex without turn metadata skips session", input: func() GatewayClientSourceIdentityInput {
			in := baseInput
			in.ClientProfileSource = "explicit_header"
			return in
		}(), session: &resolvedSession, wantStatus: SourceStatusResolved, wantKind: SourceKindIPAPIKeyFallback, wantSourceKey: true},
		{name: "claude code signature may use session", input: func() GatewayClientSourceIdentityInput {
			in := baseInput
			in.ClientProfile = "claude_code"
			in.ClientProfileSource = "claude_code_request_signature"
			return in
		}(), session: &resolvedSession, wantStatus: SourceStatusResolved, wantKind: SourceKindOfficialSession, wantSourceKey: true, wantAffinity: "conv-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &fakeSessionResolver{}
			if tt.session != nil {
				session.identity = *tt.session
			}
			resolver := newSourceResolver(session)
			req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
			source := resolver.ResolveGatewayClientSourceIdentity(req, tt.input)
			if source.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", source.Status, tt.wantStatus)
			}
			if source.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", source.Kind, tt.wantKind)
			}
			if (source.SourceKey != "") != tt.wantSourceKey {
				t.Errorf("sourceKey presence = %v", source.SourceKey != "")
			}
			if source.SourceKey != "" && !strings.HasPrefix(source.SourceKey, "src_v1_") {
				t.Errorf("sourceKey = %q", source.SourceKey)
			}
			if source.AffinityKey != tt.wantAffinity {
				t.Errorf("affinityKey = %q, want %q", source.AffinityKey, tt.wantAffinity)
			}
		})
	}
}

func TestDeriveGatewayClientSourceStateKeys(t *testing.T) {
	resolver := newSourceResolver(&fakeSessionResolver{})
	source := GatewayClientSourceIdentity{SourceKey: "src_v1_abc"}
	stateKey := resolver.DeriveGatewayClientSourceStateKey(source, struct {
		ClientProfile      string
		Endpoint           string
		DownstreamProtocol string
	}{ClientProfile: "codex", Endpoint: "/v1/responses", DownstreamProtocol: "responses_sse"})
	if stateKey == "" || !strings.HasPrefix(stateKey, "src_v1_") {
		t.Fatalf("stateKey = %q", stateKey)
	}
	again := resolver.DeriveGatewayClientSourceStateKey(source, struct {
		ClientProfile      string
		Endpoint           string
		DownstreamProtocol string
	}{ClientProfile: "codex", Endpoint: "/v1/responses", DownstreamProtocol: "responses_sse"})
	if again != stateKey {
		t.Error("state key not deterministic")
	}
	other := resolver.DeriveGatewayClientSourceStateKey(source, struct {
		ClientProfile      string
		Endpoint           string
		DownstreamProtocol string
	}{ClientProfile: "codex", Endpoint: "/v1/chat/completions", DownstreamProtocol: "json"})
	if other == stateKey {
		t.Error("endpoint not mixed into state key")
	}
	if empty := resolver.DeriveGatewayClientSourceStateKey(GatewayClientSourceIdentity{}, struct {
		ClientProfile      string
		Endpoint           string
		DownstreamProtocol string
	}{ClientProfile: "codex", Endpoint: "/v1/responses", DownstreamProtocol: "json"}); empty != "" {
		t.Errorf("empty source key derived %q", empty)
	}
	child := resolver.DeriveGatewayClientSourceChildStateKey(stateKey, "codex_turn", "turn-1")
	if child == "" || !strings.HasPrefix(child, "src_v1_") {
		t.Errorf("child key = %q", child)
	}
	if resolver.DeriveGatewayClientSourceChildStateKey("", "codex_turn", "turn-1") != "" {
		t.Error("empty parent derived child")
	}
}

// ---------------------------------------------------------------------------
// strategy
// ---------------------------------------------------------------------------

func newStrategyDeps() *ClientStrategyDeps {
	return &ClientStrategyDeps{
		CompactionExpected: func(req *gatewaypreauth.GatewayRequest) bool {
			return CodexCompactionExpectedForRequest(req)
		},
		Source: newSourceResolver(&fakeSessionResolver{}),
	}
}

func TestParseGatewayClientProfileHeader(t *testing.T) {
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	tests := []struct {
		header string
		want   string
	}{
		{"codex", ClientProfileCodex},
		{"Codex", ClientProfileCodex},
		{"CODEX", ClientProfileCodex},
		// Node: the openai strategy only upgrades the profile for codex;
		// claude-code / gemini-cli headers stay generic here (their native
		// resolvers own those profiles).
		{"claude code", ClientProfileGenericOpenAI},
		{"GeminiCLI", ClientProfileGenericOpenAI},
		{"generic_openai", ClientProfileGenericOpenAI},
		{"", ClientProfileGenericOpenAI},
	}
	for _, tt := range tests {
		if tt.header != "" {
			req.HTTP.Header.Set(GatewayClientProfileHeader, tt.header)
		} else {
			req.HTTP.Header.Del(GatewayClientProfileHeader)
		}
		deps := newStrategyDeps()
		strategy := deps.ResolveOpenAIGatewayClientStrategy(req, ClientStrategyIdentity{SystemAccountID: "sys", APIKeyID: "key", Endpoint: "/v1/responses"})
		if strategy.ClientProfile != tt.want {
			t.Errorf("header %q profile = %q, want %q", tt.header, strategy.ClientProfile, tt.want)
		}
		if tt.want == ClientProfileCodex && strategy.ClientProfileSource != ProfileSourceExplicitHeader {
			t.Errorf("header %q source = %q", tt.header, strategy.ClientProfileSource)
		}
	}
}

func TestResolveOpenAIGatewayClientStrategyCodexTurn(t *testing.T) {
	deps := newStrategyDeps()
	tests := []struct {
		name               string
		method             string
		target             string
		headers            map[string]string
		body               string
		wantProfile        string
		wantProfileSource  string
		wantCompatibility  string
		wantCodexTurn      bool
		wantCompactionFlag bool
	}{
		{
			name:   "codex turn metadata on streaming responses",
			method: "POST", target: "/v1/responses",
			headers: map[string]string{
				"Accept":                "text/event-stream",
				"X-Codex-Turn-Metadata": `{"turn_id":"turn-1","session_id":"s1","thread_id":"t1"}`,
			},
			wantProfile: ClientProfileCodex, wantProfileSource: ProfileSourceCodexTurnMetadata,
			wantCompatibility: CompatibilityCodexResponses, wantCodexTurn: true,
		},
		{
			name:   "codex turn metadata on compact post",
			method: "POST", target: "/v1/responses/compact",
			headers:     map[string]string{"X-Codex-Turn-Metadata": `{"turn_id":"turn-2"}`},
			wantProfile: ClientProfileCodex, wantProfileSource: ProfileSourceCodexTurnMetadata,
			wantCompatibility: CompatibilityCodexResponses, wantCodexTurn: true, wantCompactionFlag: true,
		},
		{
			name:   "turn metadata without stream or compact stays generic",
			method: "POST", target: "/v1/responses",
			headers:     map[string]string{"X-Codex-Turn-Metadata": `{"turn_id":"turn-3"}`},
			wantProfile: ClientProfileGenericOpenAI, wantProfileSource: ProfileSourceDefault,
			wantCompatibility: CompatibilityOpenAIStandard,
		},
		{
			name:   "invalid turn metadata json",
			method: "POST", target: "/v1/responses",
			headers: map[string]string{
				"Accept":                "text/event-stream",
				"X-Codex-Turn-Metadata": `not-json`,
			},
			wantProfile: ClientProfileGenericOpenAI, wantProfileSource: ProfileSourceDefault,
			wantCompatibility: CompatibilityOpenAIStandard,
		},
		{
			name:   "missing turn id",
			method: "POST", target: "/v1/responses",
			headers: map[string]string{
				"Accept":                "text/event-stream",
				"X-Codex-Turn-Metadata": `{"session_id":"s1"}`,
			},
			wantProfile: ClientProfileGenericOpenAI, wantProfileSource: ProfileSourceDefault,
			wantCompatibility: CompatibilityOpenAIStandard,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTestRequest(t, tt.method, tt.target, nil, tt.headers)
			strategy := deps.ResolveOpenAIGatewayClientStrategy(req, ClientStrategyIdentity{SystemAccountID: "sys", APIKeyID: "key", Endpoint: tt.target, ClientIP: "10.0.0.9"})
			if strategy.ClientProfile != tt.wantProfile {
				t.Errorf("profile = %q, want %q", strategy.ClientProfile, tt.wantProfile)
			}
			if strategy.ClientProfileSource != tt.wantProfileSource {
				t.Errorf("profileSource = %q, want %q", strategy.ClientProfileSource, tt.wantProfileSource)
			}
			if strategy.RequestClientCompatibility != tt.wantCompatibility {
				t.Errorf("compatibility = %q, want %q", strategy.RequestClientCompatibility, tt.wantCompatibility)
			}
			if (strategy.CodexTurn != nil) != tt.wantCodexTurn {
				t.Fatalf("codexTurn presence = %v", strategy.CodexTurn != nil)
			}
			if strategy.CodexTurn != nil {
				if strategy.CodexTurn.TurnID == "" {
					t.Error("turnId empty")
				}
				if strategy.ClientSourceAvoidanceStateKey == "" {
					t.Error("avoidance state key missing with codex turn")
				}
				if !strategy.AllowCodexTurnAccountAvoidance || !strategy.AllowClientSourceAccountAvoidance {
					t.Error("avoidance flags not enabled")
				}
			}
			if strategy.CodexCompactionExpected != tt.wantCompactionFlag {
				t.Errorf("compactionExpected = %v, want %v", strategy.CodexCompactionExpected, tt.wantCompactionFlag)
			}
			if strategy.UpstreamAdapter != UpstreamAdapterOpenAIMixed {
				t.Errorf("adapter = %q", strategy.UpstreamAdapter)
			}
		})
	}
}

func TestResolveDownstreamProtocols(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(*gatewaypreauth.GatewayRequest) string
		method  string
		target  string
		headers map[string]string
		want    string
	}{
		{name: "responses sse", resolve: ResolveOpenAIGatewayDownstreamProtocol, method: "POST", target: "/v1/responses", headers: map[string]string{"Accept": "text/event-stream"}, want: DownstreamResponsesSSE},
		{name: "responses json", resolve: ResolveOpenAIGatewayDownstreamProtocol, method: "POST", target: "/v1/responses", want: DownstreamJSON},
		{name: "chat sse", resolve: ResolveOpenAIGatewayDownstreamProtocol, method: "POST", target: "/v1/chat/completions", headers: map[string]string{"Accept": "text/event-stream"}, want: DownstreamChatCompletionsSSE},
		{name: "unknown stream", resolve: ResolveOpenAIGatewayDownstreamProtocol, method: "POST", target: "/v1/other", headers: map[string]string{"Accept": "text/event-stream"}, want: DownstreamUnknownStream},
		{name: "messages sse", resolve: ResolveAnthropicGatewayDownstreamProtocol, method: "POST", target: "/v1/messages", headers: map[string]string{"Accept": "text/event-stream"}, want: DownstreamMessagesSSE},
		{name: "gemini stream generate", resolve: ResolveGeminiGatewayDownstreamProtocol, method: "POST", target: "/v1beta/models/gemini:streamgeneratecontent", want: DownstreamGeminiStreamGenerateSSE},
		{name: "gemini generate with alt sse", resolve: ResolveGeminiGatewayDownstreamProtocol, method: "POST", target: "/v1beta/models/gemini:generatecontent?alt=sse", want: DownstreamGeminiStreamGenerateSSE},
		{name: "gemini interactions json", resolve: ResolveGeminiGatewayDownstreamProtocol, method: "POST", target: "/v1beta/interactions", want: DownstreamJSON},
		{name: "gemini interactions get", resolve: ResolveGeminiGatewayDownstreamProtocol, method: "GET", target: "/v1beta/interactions/abc", want: DownstreamJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTestRequest(t, tt.method, tt.target, nil, tt.headers)
			if got := tt.resolve(req); got != tt.want {
				t.Errorf("protocol = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClaudeCodeSignatureDetection(t *testing.T) {
	deps := newStrategyDeps()
	build := func(headers map[string]string, target string) *gatewaypreauth.GatewayRequest {
		return newTestRequest(t, "POST", target, nil, headers)
	}
	two := build(map[string]string{"User-Agent": "claude-cli/1.0", "Anthropic-Beta": "claude-code-2025"}, "/v1/messages")
	strategy := deps.ResolveAnthropicGatewayClientStrategy(two, &ClientStrategyIdentity{SystemAccountID: "sys", APIKeyID: "key", Endpoint: "/v1/messages"})
	if strategy.ClientProfile != ClientProfileClaudeCode || strategy.ClientProfileSource != ProfileSourceClaudeCodeRequestSignature {
		t.Fatalf("claude code strategy = %+v", strategy)
	}
	if strategy.RequestClientCompatibility != CompatibilityClaudeCode || strategy.UpstreamAdapter != UpstreamAdapterAnthropicAPIKey {
		t.Errorf("compat/adapter = %q/%q", strategy.RequestClientCompatibility, strategy.UpstreamAdapter)
	}
	one := build(map[string]string{"User-Agent": "claude-cli/1.0"}, "/v1/messages")
	strategy = deps.ResolveAnthropicGatewayClientStrategy(one, &ClientStrategyIdentity{})
	if strategy.ClientProfile != ClientProfileGenericAnthropic {
		t.Errorf("one signal profile = %q", strategy.ClientProfile)
	}
	query := build(map[string]string{"User-Agent": "claude-cli/1.0", "X-Claude-Code-Session-Id": "s"}, "/v1/messages?beta=true")
	strategy = deps.ResolveAnthropicGatewayClientStrategy(query, &ClientStrategyIdentity{})
	if strategy.ClientProfile != ClientProfileClaudeCode {
		t.Errorf("query signal profile = %q", strategy.ClientProfile)
	}
}

func TestGeminiCLISignatureDetection(t *testing.T) {
	deps := newStrategyDeps()
	req := newTestRequest(t, "POST", "/v1beta/models/gemini:generatecontent", nil, map[string]string{
		"User-Agent":     "GeminiCLI/v1 (linux)",
		"X-Goog-Api-Key": "k",
	})
	strategy := deps.ResolveGeminiGatewayClientStrategy(req, &ClientStrategyIdentity{})
	if strategy.ClientProfile != ClientProfileGeminiCLI || strategy.ClientProfileSource != ProfileSourceGeminiCLIRequestSignature {
		t.Fatalf("gemini cli strategy = %+v", strategy)
	}
	if strategy.RequestClientCompatibility != CompatibilityOpenAIStandard || strategy.UpstreamAdapter != UpstreamAdapterGeminiAPIKey {
		t.Errorf("compat/adapter = %q/%q", strategy.RequestClientCompatibility, strategy.UpstreamAdapter)
	}
	plain := newTestRequest(t, "POST", "/v1beta/models/gemini:generatecontent", nil, nil)
	strategy = deps.ResolveGeminiGatewayClientStrategy(plain, &ClientStrategyIdentity{})
	if strategy.ClientProfile != ClientProfileGenericGemini {
		t.Errorf("plain profile = %q", strategy.ClientProfile)
	}
}

func TestResolveGatewayClientRetryCoordination(t *testing.T) {
	tests := []struct {
		profile  string
		protocol string
		pre      string
		commited string
	}{
		{ClientProfileCodex, DownstreamResponsesSSE, FailureSignalProtocolErrorEvent, FailureSignalProtocolErrorEvent},
		{ClientProfileCodex, DownstreamJSON, FailureSignalHTTPError, FailureSignalDisconnect},
		{ClientProfileClaudeCode, DownstreamMessagesSSE, FailureSignalProtocolErrorEvent, FailureSignalProtocolErrorEvent},
		{ClientProfileGeminiCLI, DownstreamGeminiInteractionsSSE, FailureSignalProtocolErrorEvent, FailureSignalProtocolErrorEvent},
		{ClientProfileGenericOpenAI, DownstreamResponsesSSE, FailureSignalHTTPError, FailureSignalDisconnect},
	}
	for _, tt := range tests {
		coordination := ResolveGatewayClientRetryCoordination(tt.profile, tt.protocol)
		if coordination.PreCommitFailureSignal != tt.pre || coordination.CommittedFailureSignal != tt.commited {
			t.Errorf("coordination(%q,%q) = %+v", tt.profile, tt.protocol, coordination)
		}
	}
}

func TestOpenAIGatewayClientStrategyAuditMetadata(t *testing.T) {
	strategy := OpenAIGatewayClientStrategyContext{
		ClientProfile:              ClientProfileCodex,
		RequestClientCompatibility: CompatibilityCodexResponses,
		DownstreamProtocol:         DownstreamResponsesSSE,
		UpstreamAdapter:            UpstreamAdapterOpenAIMixed,
		CodexCompactionExpected:    true,
		CodexTurn: &OpenAIGatewayCodexTurnContext{
			TurnID: "turn", SessionID: "sess", ThreadID: "thread", StateKey: "src_v1_key", SourceKind: SourceKindOfficialSession,
		},
		ClientSource:                      &GatewayClientSourceIdentity{Status: SourceStatusResolved, Kind: SourceKindOfficialSession, SemanticNamespace: "ns", SourceKey: "src_v1_s"},
		ClientSourceAvoidanceStateKey:     "src_v1_avoid",
		ClientProfileSource:               ProfileSourceCodexTurnMetadata,
		RetryCoordination:                 ResolveGatewayClientRetryCoordination(ClientProfileCodex, DownstreamResponsesSSE),
		AllowClientSourceAccountAvoidance: true,
		AllowCodexTurnAccountAvoidance:    true,
	}
	metadata := OpenAIGatewayClientStrategyAuditMetadata(strategy)
	if metadata["clientProfile"] != ClientProfileCodex || metadata["codexCompactionExpected"] != true {
		t.Errorf("metadata = %+v", metadata)
	}
	if metadata["codexTurnStateKey"] != "src_v1_key" || metadata["clientSourceKind"] != SourceKindOfficialSession {
		t.Errorf("metadata keys = %+v", metadata)
	}
	if metadata["codexTurnIdPresent"] != true || metadata["clientSourceKeyPresent"] != true {
		t.Errorf("presence flags = %+v", metadata)
	}
}

// ---------------------------------------------------------------------------
// turn retry
// ---------------------------------------------------------------------------

func newTurnRetryService(t *testing.T) *TurnRetryService {
	t.Helper()
	return &TurnRetryService{
		Secret:   "turn-secret",
		Clock:    newFakeClock(time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)),
		CreateID: fixedIDGenerator("fence"),
	}
}

func avoidanceStrategy(stateKey string) OpenAIGatewayClientStrategyContext {
	return OpenAIGatewayClientStrategyContext{
		ClientProfile:                     ClientProfileCodex,
		ClientSourceAvoidanceStateKey:     stateKey,
		AllowClientSourceAccountAvoidance: stateKey != "",
		AllowCodexTurnAccountAvoidance:    stateKey != "",
	}
}

func TestRememberCodexTurnStreamFailureActivation(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("state-a")
	first := service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ErrorCode: "e1"})
	if first == nil || first.FailureCount != 1 || first.DuplicateObservation {
		t.Fatalf("first = %+v", first)
	}
	if first.Activation != nil {
		t.Fatalf("premature activation: %+v", first.Activation)
	}
	second := service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ErrorCode: "e2"})
	if second == nil || second.Activation == nil {
		t.Fatalf("activation missing: %+v", second)
	}
	// The exposed generation is the per-account tombstone generation
	// (Node applyCodexTurnAvoidanceGeneration with no explicit generation).
	if second.Activation.SourceGeneration != 1 || second.Activation.AccountID != "acc-1" || second.Activation.SourceFenceID == "" {
		t.Errorf("activation = %+v", second.Activation)
	}

	// Duplicate observation with the same id is a no-op.
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ObservationID: "obs-1"})
	dup := service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ObservationID: "obs-1"})
	if dup == nil || !dup.DuplicateObservation {
		t.Fatalf("duplicate = %+v", dup)
	}

	// Committed retry signal activates immediately.
	committed := service.RememberCodexTurnStreamFailure(avoidanceStrategy("state-b"), "acc-2", CodexTurnFailureInput{Evidence: EvidenceCommittedRetrySignal})
	if committed == nil || committed.Activation == nil {
		t.Fatalf("committed activation = %+v", committed)
	}

	// Incomplete downstream abort needs two within the window.
	weakStrategy := avoidanceStrategy("state-c")
	weakFirst := service.RememberCodexTurnStreamFailure(weakStrategy, "acc-3", CodexTurnFailureInput{Evidence: EvidenceIncompleteDownstreamAbort})
	if weakFirst == nil || weakFirst.Activation != nil {
		t.Fatalf("first incomplete = %+v", weakFirst)
	}
	weakSecond := service.RememberCodexTurnStreamFailure(weakStrategy, "acc-3", CodexTurnFailureInput{Evidence: EvidenceIncompleteDownstreamAbort})
	if weakSecond == nil || weakSecond.Activation == nil {
		t.Fatalf("second incomplete = %+v", weakSecond)
	}
	// Outside the window the count resets.
	service.Clock.(*fakeClock).Advance(2 * time.Minute)
	weakThird := service.RememberCodexTurnStreamFailure(weakStrategy, "acc-3", CodexTurnFailureInput{Evidence: EvidenceIncompleteDownstreamAbort})
	if weakThird == nil || weakThird.Activation != nil {
		t.Fatalf("window expired count reset failed: %+v", weakThird)
	}

	// Without an avoidance state key nothing is recorded.
	if result := service.RememberCodexTurnStreamFailure(OpenAIGatewayClientStrategyContext{}, "acc-4", CodexTurnFailureInput{}); result != nil {
		t.Fatalf("no state key result = %+v", result)
	}
}

func TestOrderOpenAIAccountsByCodexTurnAvoidance(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("state-order")
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		{ID: "acc-1", Priority: 10, FallbackEnabled: true, SuperPriorityEnabled: true},
		{ID: "acc-2", Priority: 10, FallbackEnabled: true, SuperPriorityEnabled: true},
		{ID: "acc-3", Priority: 20},
	}
	priority := &gatewayrouting.GatewayAccountModelPriority{RequestedModel: "m", RankByAccountID: map[string]int{"acc-1": 1, "acc-2": 1, "acc-3": 1}}

	// No failures -> untouched.
	result := service.OrderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy, priority)
	if result.Applied || result.ThresholdReached || len(result.AvoidedAccountIDs) != 0 {
		t.Fatalf("clean result = %+v", result)
	}

	// Activate avoidance on acc-1.
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
	result = service.OrderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy, priority)
	if !result.Applied || !result.ThresholdReached {
		t.Fatalf("applied result = %+v", result)
	}
	// acc-1 and acc-2 share a tier; the tier preservation keeps acc-1
	// behind acc-2 with acc-3 in its own tier.
	if len(result.Accounts) != 3 ||
		result.Accounts[0].ID != "acc-2" || result.Accounts[1].ID != "acc-1" || result.Accounts[2].ID != "acc-3" {
		t.Errorf("ordering = %+v", accountIDs(result.Accounts))
	}
	if len(result.AvoidedAccountIDs) != 1 || result.AvoidedAccountIDs[0] != "acc-1" {
		t.Errorf("avoided = %+v", result.AvoidedAccountIDs)
	}

	// All accounts avoided -> bypass.
	for _, id := range []string{"acc-2", "acc-3"} {
		service.RememberCodexTurnStreamFailure(strategy, id, CodexTurnFailureInput{})
		service.RememberCodexTurnStreamFailure(strategy, id, CodexTurnFailureInput{})
	}
	result = service.OrderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy, priority)
	if result.Applied || !result.ThresholdReached || !result.BypassedAllAvoided {
		t.Fatalf("bypass result = %+v", result)
	}

	// Without the avoidance flag nothing happens.
	result = service.OrderOpenAIAccountsByCodexTurnAvoidance(accounts, OpenAIGatewayClientStrategyContext{}, priority)
	if result.Applied || len(result.Accounts) != 3 {
		t.Fatalf("disabled result = %+v", result)
	}
}

func TestClearCodexTurnAccountAvoidanceByFence(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("state-fence")
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
	remembered := service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
	fence := remembered.Activation

	// Wrong fence id rejected.
	wrong, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(context.Background(), ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "state-fence", AccountID: "acc-1", SourceGeneration: fence.SourceGeneration, SourceFenceID: "not-a-uuid",
	})
	if err != nil || wrong {
		t.Fatalf("invalid fence cleared: %v %v", wrong, err)
	}
	// Wrong generation rejected.
	wrong, err = service.ClearCodexTurnAccountAvoidanceByFenceAsync(context.Background(), ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "state-fence", AccountID: "acc-1", SourceGeneration: fence.SourceGeneration + 10, SourceFenceID: fence.SourceFenceID,
	})
	if err != nil || wrong {
		t.Fatalf("wrong generation cleared: %v %v", wrong, err)
	}
	// Exact fence clears.
	cleared, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(context.Background(), ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "state-fence", AccountID: "acc-1", SourceGeneration: fence.SourceGeneration, SourceFenceID: fence.SourceFenceID,
	})
	if err != nil || !cleared {
		t.Fatalf("clear failed: %v %v", cleared, err)
	}
	// One-shot: a second clear finds nothing.
	cleared, err = service.ClearCodexTurnAccountAvoidanceByFenceAsync(context.Background(), ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "state-fence", AccountID: "acc-1", SourceGeneration: fence.SourceGeneration, SourceFenceID: fence.SourceFenceID,
	})
	if err != nil || cleared {
		t.Fatalf("second clear succeeded: %v %v", cleared, err)
	}
}

type fakeRedisStore struct {
	mu      sync.Mutex
	values  map[string][]byte
	failCAS bool
}

func (s *fakeRedisStore) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.values[key]; ok {
		return append(json.RawMessage(nil), value...), nil
	}
	return nil, nil
}

func (s *fakeRedisStore) CompareSetJSON(_ context.Context, key string, _ json.RawMessage, next any, _ int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCAS {
		return false, nil
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	s.values[key] = encoded
	return true, nil
}

func (s *fakeRedisStore) Incr(_ context.Context, key string, _ int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return 7, nil
}

func TestRememberCodexTurnStreamFailureAsyncRedis(t *testing.T) {
	store := &fakeRedisStore{values: map[string][]byte{}}
	service := newTurnRetryService(t)
	service.Store = store
	strategy := avoidanceStrategy("state-redis")
	result, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || result == nil || result.FailureCount != 1 {
		t.Fatalf("first async = %+v err %v", result, err)
	}
	second, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || second == nil || second.Activation == nil {
		t.Fatalf("second async = %+v err %v", second, err)
	}
	if second.Activation.SourceGeneration != 7 {
		t.Errorf("redis generation = %d, want 7 (store incr)", second.Activation.SourceGeneration)
	}
	// CAS exhaustion fails open with a warn.
	store.failCAS = true
	exhausted, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), avoidanceStrategy("state-race"), "acc-9", CodexTurnFailureInput{})
	if err != nil || exhausted != nil {
		t.Fatalf("exhausted = %+v err %v", exhausted, err)
	}
}

// ---------------------------------------------------------------------------
// turn availability probe
// ---------------------------------------------------------------------------

type fakeProbeCoordinator struct {
	acquireResult gatewaycircuit.ProbeAcquireResult
	acquireErr    error
	state         *gatewaycircuit.ProbeState
	released      bool
	settled       []gatewaycircuit.SettleProbeInput
	settledFence  []gatewaycircuit.SettleDispatchedProbeInput
	releaseOK     bool
}

func (c *fakeProbeCoordinator) Acquire(_ context.Context, _ gatewaycircuit.ProbeAcquireInput) (gatewaycircuit.ProbeAcquireResult, error) {
	return c.acquireResult, c.acquireErr
}

func (c *fakeProbeCoordinator) ReleaseForExecution(_ context.Context, _ gatewaycircuit.ReleaseProbeInput) (bool, error) {
	c.released = true
	return c.releaseOK, nil
}

func (c *fakeProbeCoordinator) Settle(_ context.Context, input gatewaycircuit.SettleProbeInput) (bool, error) {
	c.settled = append(c.settled, input)
	return true, nil
}

func (c *fakeProbeCoordinator) SettleDispatchedBySourceFence(_ context.Context, input gatewaycircuit.SettleDispatchedProbeInput) (bool, error) {
	c.settledFence = append(c.settledFence, input)
	return true, nil
}

func (c *fakeProbeCoordinator) GetState(_ context.Context, _ string) (*gatewaycircuit.ProbeState, error) {
	return c.state, nil
}

func newProbeService(coordinator *fakeProbeCoordinator, retry *TurnRetryService) *TurnAvoidanceProbeService {
	return &TurnAvoidanceProbeService{
		Coordinator: coordinator,
		TurnRetry:   retry,
		Logger:      &recordingLogger{},
		Clock:       SystemClock{},
	}
}

func probeInput(stateKey string) CodexTurnAvoidanceProbeInput {
	return CodexTurnAvoidanceProbeInput{
		Account:  gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", ConfigRevision: nil},
		Strategy: avoidanceStrategy(stateKey),
		Activation: CodexTurnFailureActivation{
			AccountID:        "acc-1",
			SourceGeneration: 3,
			SourceFenceID:    "fence-1",
		},
		Dispatch: func(accountID string, reason string, traceID string, sourceFence *SourceProbeFence) HealthCheckDispatchOutcome {
			return HealthCheckDispatchOutcome{Outcome: HealthDispatchQueued, DecisionCode: "queued", TargetRole: "go-jobs"}
		},
	}
}

func TestRunCodexTurnAvoidanceAvailabilityProbe(t *testing.T) {
	success := ProbeOutcomeSuccess

	t.Run("joined consumes settled success and clears fence", func(t *testing.T) {
		retry := newTurnRetryService(t)
		strategy := avoidanceStrategy("state-joined")
		retry.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
		remembered := retry.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
		coordinator := &fakeProbeCoordinator{
			acquireResult: gatewaycircuit.ProbeAcquireResult{Disposition: gatewaycircuit.ProbeDispositionJoined, RuntimeKey: "rk", Generation: 5},
			state:         &gatewaycircuit.ProbeState{Outcome: &success},
		}
		service := newProbeService(coordinator, retry)
		input := probeInput("state-joined")
		input.Activation = *remembered.Activation
		result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), input)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if result.Disposition != "joined" || result.Generation != 5 || result.Outcome != "" {
			t.Fatalf("result = %+v", result)
		}
		cleared, _ := retry.ClearCodexTurnAccountAvoidanceByFenceAsync(context.Background(), ClearCodexTurnAccountAvoidanceByFenceInput{
			StateKey: "state-joined", AccountID: "acc-1", SourceGeneration: remembered.Activation.SourceGeneration, SourceFenceID: remembered.Activation.SourceFenceID,
		})
		if cleared {
			t.Error("fence should already be cleared by the probe")
		}
	})

	t.Run("owner queued hands off to worker", func(t *testing.T) {
		retry := newTurnRetryService(t)
		coordinator := &fakeProbeCoordinator{
			acquireResult: gatewaycircuit.ProbeAcquireResult{Disposition: gatewaycircuit.ProbeDispositionOwner, RuntimeKey: "rk", Generation: 2, OwnerToken: "tok"},
			releaseOK:     true,
		}
		service := newProbeService(coordinator, retry)
		result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), probeInput("state-owner"))
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if result.Disposition != "owner" || result.Generation != 2 || result.Outcome != "" {
			t.Fatalf("result = %+v", result)
		}
		if !coordinator.released || len(coordinator.settledFence) != 0 || len(coordinator.settled) != 0 {
			t.Errorf("coordinator = %+v", coordinator)
		}
	})

	t.Run("release failure settles unknown", func(t *testing.T) {
		retry := newTurnRetryService(t)
		coordinator := &fakeProbeCoordinator{
			acquireResult: gatewaycircuit.ProbeAcquireResult{Disposition: gatewaycircuit.ProbeDispositionOwner, RuntimeKey: "rk", Generation: 2, OwnerToken: "tok"},
		}
		service := newProbeService(coordinator, retry)
		result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), probeInput("state-release"))
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if result.Outcome != ProbeOutcomeUnknown {
			t.Fatalf("outcome = %q", result.Outcome)
		}
		if len(coordinator.settled) != 1 || coordinator.settled[0].Outcome != ProbeOutcomeUnknown {
			t.Errorf("settled = %+v", coordinator.settled)
		}
	})

	t.Run("rejected dispatch settles fence one-shot", func(t *testing.T) {
		retry := newTurnRetryService(t)
		coordinator := &fakeProbeCoordinator{
			acquireResult: gatewaycircuit.ProbeAcquireResult{Disposition: gatewaycircuit.ProbeDispositionOwner, RuntimeKey: "rk", Generation: 2, OwnerToken: "tok"},
			releaseOK:     true,
		}
		service := newProbeService(coordinator, retry)
		input := probeInput("state-rejected")
		input.Dispatch = func(accountID string, reason string, traceID string, sourceFence *SourceProbeFence) HealthCheckDispatchOutcome {
			return HealthCheckDispatchOutcome{Outcome: HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
		}
		result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), input)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if result.Outcome != ProbeOutcomeProbeTaskFailure {
			t.Fatalf("outcome = %q", result.Outcome)
		}
		if len(coordinator.settledFence) != 1 || coordinator.settledFence[0].Outcome != ProbeOutcomeProbeTaskFailure {
			t.Fatalf("settledFence = %+v", coordinator.settledFence)
		}
	})

	t.Run("missing state key joined unknown", func(t *testing.T) {
		retry := newTurnRetryService(t)
		coordinator := &fakeProbeCoordinator{}
		service := newProbeService(coordinator, retry)
		input := probeInput("")
		result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), input)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if result.Disposition != "joined" || result.Outcome != ProbeOutcomeUnknown {
			t.Fatalf("result = %+v", result)
		}
	})
}

// ---------------------------------------------------------------------------
// G05 ports
// ---------------------------------------------------------------------------

func TestClientStrategyPortAdapter(t *testing.T) {
	deps := newStrategyDeps()
	port := NewClientStrategyPort(deps)
	var _ gatewaypreauth.ClientStrategy = port

	req := newTestRequest(t, "POST", "/v1/responses", nil, map[string]string{
		"Accept":                "text/event-stream",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-9"}`,
	})
	context := port.Resolve(req, gatewaypreauth.ClientStrategyInput{
		SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
		Endpoint: "/v1/responses", ClientIP: "10.1.1.1",
	})
	if context.ClientProfile != ClientProfileCodex || context.DownstreamProtocol != DownstreamResponsesSSE {
		t.Fatalf("context = %+v", context)
	}
	if context.RequestClientCompatibility != CompatibilityCodexResponses {
		t.Errorf("compatibility = %q", context.RequestClientCompatibility)
	}
	if context.ClientSource == nil {
		t.Fatal("client source missing")
	}
	full, ok := context.Opaque.(OpenAIGatewayClientStrategyContext)
	if !ok || full.CodexTurn == nil {
		t.Fatalf("opaque = %+v", context.Opaque)
	}
	metadata := port.AuditMetadata(context)
	if metadata["clientProfile"] != ClientProfileCodex {
		t.Errorf("metadata = %+v", metadata)
	}
}

func TestBridgePreflightPortAdapter(t *testing.T) {
	bridge, registry, _, _, _ := newBridgeService(t)
	compact := &CompactPreflightService{Bridge: bridge, Registry: registry, Clock: SystemClock{}, Sink: &recordedSink{}}
	port := NewBridgePreflightPort(bridge, compact, registry)
	var _ gatewaypreauth.CodexBridgePreflight = port

	if !port.CompactionExpectedForRequest(newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)) {
		t.Error("compaction expected on compact path")
	}
	completed, err := port.ApplyContextStatePreflight(context.Background(), gatewaypreauth.CodexContextStateInput{
		Req:          newTestRequest(t, "POST", "/v1/chat/completions", nil, nil),
		AuditCapture: &recordedAudit{},
	})
	if err != nil || completed {
		t.Errorf("chat request completed=%v err=%v", completed, err)
	}
	compactResult, err := port.ApplyChatBridgeCompactPreflight(context.Background(), gatewaypreauth.CodexCompactPreflightInput{
		Req:              newTestRequest(t, "POST", "/v1/responses", nil, nil),
		AuditCapture:     &recordedAudit{},
		DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
	})
	if err != nil {
		t.Fatalf("compact port: %v", err)
	}
	if compactResult.Completed || len(compactResult.Accounts) != 1 {
		t.Errorf("compact result = %+v", compactResult)
	}
}

// silence unused-import guards for types only used in a few builds
var (
	_ = http.MethodPost
	_ = errors.New
)
