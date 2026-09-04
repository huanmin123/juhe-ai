package gatewayproto

import (
	"net/http"
	"strings"
	"testing"
)

func TestRegistryDriverSelectionMirrorsNodeRegistry(t *testing.T) {
	registry := newTestRegistry()

	profile := ProtocolProfile{ID: "profile_openai_openai_v1", ProtocolCode: "openai", ProtocolVersion: "v1"}
	driver, ok := registry.DriverForProfile(profile)
	if !ok || driver.ID() != "openai-v1" {
		t.Fatalf("DriverForProfile = %v, %v; want openai-v1", driver, ok)
	}

	if _, ok := registry.DriverForProfile(ProtocolProfile{ID: "p", ProtocolCode: "anthropic", ProtocolVersion: "v1"}); ok {
		t.Fatal("anthropic profile must not resolve in the G01 slice registry")
	}

	if _, err := registry.RequireDriverForProfile(ProtocolProfile{ID: "profile_unknown"}); err == nil {
		t.Fatal("RequireDriverForProfile must fail for unknown profiles")
	}

	// Path-based selection mirrors isOpenAIProtocolRequestPath.
	for _, path := range []string{
		"/v1/chat/completions",
		"/v1/responses",
		"/openai/v1/chat/completions?api-version=1",
		"/v1/models",
		"/v1/embeddings",
		"/v1/images/generations",
		"/v1/audio/transcriptions",
	} {
		if _, ok := registry.DriverForRequest(RequestShape{Method: "POST", Path: path, OriginalPathAndQuery: path}); !ok {
			t.Fatalf("path %q must match a driver", path)
		}
	}
	for _, path := range []string{"/v1/messages", "/v1beta/models"} {
		if _, ok := registry.DriverForRequest(RequestShape{Method: "POST", Path: path, OriginalPathAndQuery: path}); ok {
			t.Fatalf("path %q must not match the openai driver", path)
		}
	}

	if _, err := registry.DriverForResponseProtocol("openai_v1"); err != nil {
		t.Fatalf("DriverForResponseProtocol(openai_v1) = %v", err)
	}
	if _, err := registry.DriverForResponseProtocol("anthropic_v1"); err == nil {
		t.Fatal("DriverForResponseProtocol must fail without an anthropic driver")
	}
}

func TestRegistryEndpointModeForRequest(t *testing.T) {
	registry := newTestRegistry()
	cases := []struct {
		path   string
		stream bool
		want   EndpointMode
	}{
		{"/v1/chat/completions", false, EndpointModeChatJSON},
		{"/v1/chat/completions", true, EndpointModeChatSSE},
		{"/v1/responses", false, EndpointModeResponsesJSON},
		{"/v1/responses", true, EndpointModeResponsesSSE},
		{"/v1/models", false, ""},
	}
	for _, tc := range cases {
		got, ok := registry.EndpointModeForRequest(RequestShape{Method: "POST", Path: tc.path, Stream: tc.stream})
		if tc.want == "" {
			if ok {
				t.Fatalf("path %q: unexpected mode %q", tc.path, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Fatalf("path %q stream=%v: mode = %q, %v; want %q", tc.path, tc.stream, got, ok, tc.want)
		}
	}
}

func TestClassifyOutcomeFrozenMapping(t *testing.T) {
	cases := []struct {
		name     string
		evidence AttemptEvidence
		want     Outcome
	}{
		{"framing+semantic", AttemptEvidence{StatusCode: 200, SemanticSuccess: true}, OutcomeCompleteSuccess},
		{"framing w/o semantic (400)", AttemptEvidence{StatusCode: 400}, OutcomeFramingCompleteNeutral},
		{"framing w/o semantic (502)", AttemptEvidence{StatusCode: 502}, OutcomeFramingCompleteNeutral},
		{"timeout", AttemptEvidence{TransportFailure: TransportFailureTimeout}, OutcomeUpstreamFailure},
		{"connection", AttemptEvidence{TransportFailure: TransportFailureConnection}, OutcomeUpstreamFailure},
		{"read", AttemptEvidence{TransportFailure: TransportFailureRead, StatusCode: 200}, OutcomeUpstreamFailure},
		{"canceled", AttemptEvidence{TransportFailure: TransportFailureCanceled}, OutcomeProbeTaskFailure},
		{"task", AttemptEvidence{TransportFailure: TransportFailureTask}, OutcomeProbeTaskFailure},
		{"no evidence", AttemptEvidence{}, OutcomeProbeTaskFailure},
	}
	for _, tc := range cases {
		if got := ClassifyOutcome(tc.evidence); got != tc.want {
			t.Fatalf("%s: outcome = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParsedUsageMergeAndHasAny(t *testing.T) {
	empty := EmptyUsage()
	if HasAnyUsageValue(empty) {
		t.Fatal("empty usage must carry no values")
	}
	current := ParsedUsage{InputTokens: IntToken(10)}
	next := ParsedUsage{OutputTokens: IntToken(8), InputTokens: IntToken(12)}
	merged := MergeUsage(current, next)
	if Token(merged.InputTokens) != 12 || Token(merged.OutputTokens) != 8 {
		t.Fatalf("merge = %+v", merged)
	}
	if !HasAnyUsageValue(merged) {
		t.Fatal("merged usage must carry values")
	}
	// next-wins only when present
	fallback := MergeUsage(ParsedUsage{ServiceTier: "default", CacheReadTokens: IntToken(3)}, ParsedUsage{ServiceTier: "priority"})
	if fallback.ServiceTier != "priority" || Token(fallback.CacheReadTokens) != 3 {
		t.Fatalf("merge fallback = %+v", fallback)
	}
}

func TestHasPendingSseProtocolEvent(t *testing.T) {
	cases := []struct {
		name  string
		state SsePendingEventState
		want  bool
	}{
		{"clean", SsePendingEventState{}, false},
		{"skipped", SsePendingEventState{Skipped: true, DataBytes: 10}, false},
		{"oversized", SsePendingEventState{OversizedEvent: true}, true},
		{"event name", SsePendingEventState{EventName: "response.created"}, true},
		{"data lines", SsePendingEventState{DataLineCount: 1}, true},
		{"data bytes", SsePendingEventState{DataBytes: 5}, true},
		{"pending event line", SsePendingEventState{PendingLine: "event: response.created"}, true},
		{"pending data line", SsePendingEventState{PendingLine: "data: {\"a\""}, true},
		{"pending data line cr", SsePendingEventState{PendingLine: "data: x\r"}, true},
		{"plain text line", SsePendingEventState{PendingLine: ": comment"}, false},
		{"trailing cr only strip then plain", SsePendingEventState{PendingLine: "x\r"}, false},
	}
	for _, tc := range cases {
		if got := HasPendingSseProtocolEvent(tc.state); got != tc.want {
			t.Fatalf("%s: pending = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func newTestRegistry() *Registry {
	return NewRegistry(&fakeOpenAIDriver{})
}

// fakeOpenAIDriver exercises the Registry without importing the concrete
// G02 package (the dependency direction is G02 -> G01).
type fakeOpenAIDriver struct{}

func (f *fakeOpenAIDriver) ID() string                   { return "openai-v1" }
func (f *fakeOpenAIDriver) ProtocolCode() string         { return "openai" }
func (f *fakeOpenAIDriver) ProtocolVersion() string      { return "v1" }
func (f *fakeOpenAIDriver) ResponseProtocol() string     { return "openai_v1" }
func (f *fakeOpenAIDriver) ClientErrorProtocol() string  { return "openai" }
func (f *fakeOpenAIDriver) DefaultClientProfile() string { return "generic_openai" }
func (f *fakeOpenAIDriver) SupportsProfile(p ProtocolProfile) bool {
	return NormalizeProtocolToken(p.ProtocolCode) == "openai" && NormalizeProtocolToken(p.ProtocolVersion) == "v1"
}
func (f *fakeOpenAIDriver) MatchPath(shape RequestShape) bool {
	endpoint := shape.OriginalPathAndQuery
	if endpoint == "" {
		endpoint = shape.Path
	}
	return IsOpenAITestPath(endpoint)
}
func (f *fakeOpenAIDriver) EndpointModeForRequestShape(shape RequestShape) (EndpointMode, bool) {
	path := shape.Path
	switch {
	case strings.Contains(path, "/chat/completions"):
		if shape.Stream {
			return EndpointModeChatSSE, true
		}
		return EndpointModeChatJSON, true
	case strings.Contains(path, "/responses"):
		if shape.Stream {
			return EndpointModeResponsesSSE, true
		}
		return EndpointModeResponsesJSON, true
	}
	return "", false
}
func (f *fakeOpenAIDriver) BuildUpstreamRequest(BuildUpstreamRequestInput) (*BuildUpstreamRequestResult, error) {
	return nil, nil
}
func (f *fakeOpenAIDriver) NewStreamInspector() StreamInspector { return nil }
func (f *fakeOpenAIDriver) InspectResponse(InspectResponseInput) ResponseInspection {
	return ResponseInspection{}
}
func (f *fakeOpenAIDriver) ExtractUsageFromJSONBuffer([]byte) ParsedUsage       { return EmptyUsage() }
func (f *fakeOpenAIDriver) ExtractUsageFromJSONValue(any) ParsedUsage           { return EmptyUsage() }
func (f *fakeOpenAIDriver) ExtractUsageFromJSONTextFragment(string) ParsedUsage { return EmptyUsage() }
func (f *fakeOpenAIDriver) ParseErrorPayload(string, http.Header) ErrorPayload {
	return ErrorPayload{}
}

// IsOpenAITestPath mirrors the openai path surface for the fake driver.
func IsOpenAITestPath(endpoint string) bool {
	if strings.Contains(endpoint, "/chat/completions") || strings.Contains(endpoint, "/responses") {
		return true
	}
	path := endpoint
	if idx := strings.Index(endpoint, "?"); idx >= 0 {
		path = endpoint[:idx]
	}
	normalized := path
	switch {
	case normalized == "/v1":
		normalized = "/"
	case strings.HasPrefix(normalized, "/v1/"):
		normalized = normalized[len("/v1"):]
	}
	return normalized == "/models" ||
		normalized == "/embeddings" ||
		normalized == "/images" || strings.HasPrefix(normalized, "/images/") ||
		normalized == "/audio" || strings.HasPrefix(normalized, "/audio/")
}
