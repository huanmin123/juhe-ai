package accountprobe

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/platform/upstreamtransport"
	"juhe-ai/backend-go/internal/store/port"
)

func TestCooldownProbeExecutesAPIKeyCandidateEndToEnd(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	target, candidate := cooldownProbeFixtures(now)
	loader := &exactCandidateLoaderStub{candidate: candidate, found: true}
	current := &cooldownCandidateReaderStub{candidate: target, found: true}
	transport := &attemptTransportStub{result: completeResult(http.StatusOK, []byte(`{"choices":[{"finish_reason":"stop"}]}`))}
	probe := CooldownProbe{
		Loader: loader, Current: current, TransportFactory: candidateTransportFactoryStub{transport: transport},
		WorkingDirectory: `F:\work`, Now: func() time.Time { return now }, NewTraceID: func() string { return "trace" },
	}
	result, err := probe.Probe(t.Context(), target)
	if err != nil || result.Outcome != string(accounthealth.ProbeOutcomeCompleteSuccess) || result.StatusCode != http.StatusOK || result.TraceID != "trace" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if loader.calls != 2 || transport.request == nil || transport.request.Header.Get("Authorization") != "Bearer key" {
		t.Fatalf("loader calls=%d request=%+v", loader.calls, transport.request)
	}
}

func TestCooldownProbeFailsTaskWhenFinalFenceChanges(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	target, candidate := cooldownProbeFixtures(now)
	current := &cooldownCandidateReaderStub{candidate: target, found: true}
	current.candidate.Generation = "changed"
	transport := &attemptTransportStub{result: completeResult(http.StatusOK, []byte(`{"choices":[{"finish_reason":"stop"}]}`))}
	probe := CooldownProbe{
		Loader: &exactCandidateLoaderStub{candidate: candidate, found: true}, Current: current,
		TransportFactory: candidateTransportFactoryStub{transport: transport}, WorkingDirectory: `F:\work`, Now: func() time.Time { return now },
	}
	result, err := probe.Probe(t.Context(), target)
	if err != nil || result.Outcome != string(accounthealth.ProbeOutcomeTaskFailure) || result.ErrorCode != "probe_task_failure" || transport.request == nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestCooldownProbeRejectsOAuthBeforeTransport(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	target, candidate := cooldownProbeFixtures(now)
	candidate.Projection.Type = "oauth"
	probe := CooldownProbe{
		Loader: &exactCandidateLoaderStub{candidate: candidate, found: true}, Current: &cooldownCandidateReaderStub{candidate: target, found: true},
		TransportFactory: candidateTransportFactoryStub{}, WorkingDirectory: `F:\work`, Now: func() time.Time { return now },
	}
	result, err := probe.Probe(t.Context(), target)
	if err != nil || result.Outcome != string(accounthealth.ProbeOutcomeTaskFailure) || !strings.Contains(result.Message, "OAuth") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestCooldownProbeExecutesFreshOAuthWithFinalCredentialFence(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	target, candidate := cooldownProbeOAuthFixtures(now, "gpt")
	runtime := &oauthProbeSnapshotRuntimeStub{candidate: candidate, values: map[string]any{
		"access_token": "oauth-token", "expires_at": now.Add(time.Hour).Format(time.RFC3339),
	}}
	transport := &sequenceAttemptTransport{results: []upstreamtransport.Result{
		completeResult(http.StatusOK, []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"object\":\"response\"}}\n\n")),
	}}
	probe := CooldownProbe{
		Loader: &exactCandidateLoaderStub{candidate: candidate, found: true}, Current: &cooldownCandidateReaderStub{candidate: target, found: true},
		TransportFactory: candidateTransportFactoryStub{transport: transport}, OAuthSnapshots: runtime, OAuthCoordinator: OAuthCoordinator{},
		WorkingDirectory: `F:\work`, Now: func() time.Time { return now },
	}
	result, err := probe.Probe(t.Context(), target)
	if err != nil || result.Outcome != string(accounthealth.ProbeOutcomeCompleteSuccess) || len(transport.requests) != 1 {
		t.Fatalf("result=%+v requests=%d error=%v", result, len(transport.requests), err)
	}
	request := transport.requests[0]
	if request.URL.String() != "https://chatgpt.com/backend-api/codex/responses" || request.Header.Get("Authorization") != "Bearer oauth-token" || runtime.reloadCalls != 1 {
		t.Fatalf("url=%q authorization=%q reloads=%d", request.URL.String(), request.Header.Get("Authorization"), runtime.reloadCalls)
	}
}

func TestCooldownProbeDefersAfterOAuthRefreshCASRescheduleWithoutTransport(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	target, candidate := cooldownProbeOAuthFixtures(now, "gpt")
	runtime := &oauthProbeSnapshotRuntimeStub{candidate: candidate, values: map[string]any{"refresh_token": "refresh"}}
	transport := &sequenceAttemptTransport{}
	probe := CooldownProbe{
		Loader: &exactCandidateLoaderStub{candidate: candidate, found: true}, Current: &cooldownCandidateReaderStub{candidate: target, found: true},
		TransportFactory: candidateTransportFactoryStub{transport: transport}, OAuthSnapshots: runtime,
		OAuthCoordinator: oauthProbeCoordinatorStub{result: OAuthCoordinationResult{disposition: OAuthCoordinationReschedule}},
		WorkingDirectory: `F:\work`, Now: func() time.Time { return now },
	}
	result, err := probe.Probe(t.Context(), target)
	if err != nil || result.Outcome != string(accounthealth.ProbeOutcomeTaskFailure) || !strings.Contains(result.Message, "defer") || len(transport.requests) != 0 {
		t.Fatalf("result=%+v requests=%d error=%v", result, len(transport.requests), err)
	}
}

func TestCooldownProbeOAuthFinalFenceRejectsRotatedAccessToken(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	target, candidate := cooldownProbeOAuthFixtures(now, "gpt")
	runtime := &oauthProbeSnapshotRuntimeStub{
		candidate:    candidate,
		values:       map[string]any{"access_token": "old", "expires_at": now.Add(time.Hour).Format(time.RFC3339)},
		reloadValues: map[string]any{"access_token": "rotated", "expires_at": now.Add(time.Hour).Format(time.RFC3339)},
	}
	transport := &sequenceAttemptTransport{results: []upstreamtransport.Result{completeResult(http.StatusOK, []byte(`{"status":"completed","object":"response","output":[]}`))}}
	probe := CooldownProbe{
		Loader: &exactCandidateLoaderStub{candidate: candidate, found: true}, Current: &cooldownCandidateReaderStub{candidate: target, found: true},
		TransportFactory: candidateTransportFactoryStub{transport: transport}, OAuthSnapshots: runtime, OAuthCoordinator: OAuthCoordinator{},
		WorkingDirectory: `F:\work`, Now: func() time.Time { return now },
	}
	result, err := probe.Probe(t.Context(), target)
	if err != nil || result.Outcome != string(accounthealth.ProbeOutcomeTaskFailure) || !strings.Contains(result.Message, "credential changed") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestCooldownProbeXAIFallbackUsesSecondFencedAttemptOnlyOnProtocolSuccess(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		fallback    upstreamtransport.Result
		wantOutcome accounthealth.ProbeOutcome
		wantStatus  int
	}{
		{name: "success", fallback: completeResult(http.StatusOK, []byte(`{"status":"completed","object":"response","output":[]}`)), wantOutcome: accounthealth.ProbeOutcomeCompleteSuccess, wantStatus: http.StatusOK},
		{name: "rejected", fallback: completeResult(http.StatusForbidden, []byte(`{"error":"official rejected"}`)), wantOutcome: accounthealth.ProbeOutcomeFramingCompleteNeutral, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, candidate := cooldownProbeOAuthFixtures(now, "xai")
			candidate.DefaultBaseURL = "https://cli-chat-proxy.grok.com/v1"
			values := map[string]any{
				"access_token": "xai-token", "base_url": "https://cli-chat-proxy.grok.com/v1",
				"expires_at": now.Add(time.Hour).Format(time.RFC3339),
			}
			runtime := &oauthProbeSnapshotRuntimeStub{candidate: candidate, values: values}
			transport := &sequenceAttemptTransport{results: []upstreamtransport.Result{
				completeResult(http.StatusForbidden, []byte(`{"error":"Access denied"}`)), test.fallback,
			}}
			probe := CooldownProbe{
				Loader: &exactCandidateLoaderStub{candidate: candidate, found: true}, Current: &cooldownCandidateReaderStub{candidate: target, found: true},
				TransportFactory: candidateTransportFactoryStub{transport: transport}, OAuthSnapshots: runtime, OAuthCoordinator: OAuthCoordinator{},
				WorkingDirectory: `F:\work`, Now: func() time.Time { return now },
			}
			result, err := probe.Probe(t.Context(), target)
			if err != nil || result.Outcome != string(test.wantOutcome) || result.StatusCode != test.wantStatus || len(transport.requests) != 2 {
				t.Fatalf("result=%+v requests=%d error=%v", result, len(transport.requests), err)
			}
			if transport.requests[0].URL.Host != "cli-chat-proxy.grok.com" || transport.requests[1].URL.Host != "api.x.ai" || transport.requests[1].Header.Get("X-Xai-Token-Auth") != "" || runtime.reloadCalls != 2 {
				t.Fatalf("primary=%q fallback=%q headers=%v reloads=%d", transport.requests[0].URL, transport.requests[1].URL, transport.requests[1].Header, runtime.reloadCalls)
			}
		})
	}
}

func cooldownProbeFixtures(now time.Time) (port.CooldownAccountRetestCandidate, gatewaycandidatewindow.Candidate) {
	observation := now.Add(-time.Minute)
	target := port.CooldownAccountRetestCandidate{
		ID: "account", ConfigRevision: 3, DispatchRevision: 4, ObservationStartedAt: &observation, Generation: "generation",
		SystemAccountID: "system", GroupID: "group", HealthCheckModel: "model", HealthCheckEndpointMode: string(ModeChatJSON),
	}
	candidate := apiKeyCandidate("openai", "gpt", "profile", "https://api.example", map[string]any{"api_key": "key"})
	candidate.Projection.AccountID = target.ID
	candidate.Projection.SystemAccountID = target.SystemAccountID
	candidate.Projection.GroupID = target.GroupID
	candidate.Projection.ConfigRevision = target.ConfigRevision
	candidate.Projection.DispatchRevision = int64(target.DispatchRevision)
	candidate.Projection.Status = "temporary_unavailable"
	candidate.Projection.Schedulable = true
	return target, candidate
}

func cooldownProbeOAuthFixtures(now time.Time, provider string) (port.CooldownAccountRetestCandidate, gatewaycandidatewindow.Candidate) {
	target, candidate := cooldownProbeFixtures(now)
	target.HealthCheckEndpointMode = string(ModeResponsesJSON)
	candidate.Projection.Type = "oauth"
	candidate.Projection.ProviderCode = provider
	return target, candidate
}

type candidateTransportFactoryStub struct {
	transport AttemptTransport
	err       error
}

func (s candidateTransportFactoryStub) New(gatewaycandidatewindow.Candidate) (AttemptTransport, error) {
	return s.transport, s.err
}

var _ CandidateTransportFactory = candidateTransportFactoryStub{}

type oauthProbeSnapshotRuntimeStub struct {
	candidate    gatewaycandidatewindow.Candidate
	values       map[string]any
	reloadValues map[string]any
	reloadCalls  int
}

func (s *oauthProbeSnapshotRuntimeStub) Snapshot(candidate gatewaycandidatewindow.Candidate) (OAuthProbeCandidateSnapshot, error) {
	s.candidate = candidate
	return NewOAuthProbeCandidateSnapshot(candidate, s.values)
}

func (s *oauthProbeSnapshotRuntimeStub) ReloadOAuthProbeCandidate(context.Context, LoadInput) (OAuthProbeCandidateSnapshot, bool, error) {
	s.reloadCalls++
	values := s.reloadValues
	if values == nil {
		values = s.values
	}
	snapshot, err := NewOAuthProbeCandidateSnapshot(s.candidate, values)
	return snapshot, err == nil, err
}

type oauthProbeCoordinatorStub struct{ result OAuthCoordinationResult }

func (s oauthProbeCoordinatorStub) Coordinate(context.Context, OAuthCoordinationInput) OAuthCoordinationResult {
	return s.result
}

type sequenceAttemptTransport struct {
	results  []upstreamtransport.Result
	errs     []error
	requests []*http.Request
}

func (s *sequenceAttemptTransport) ExecuteWithFence(ctx context.Context, request *http.Request, fence func(context.Context) error) (upstreamtransport.Result, error) {
	s.requests = append(s.requests, request)
	if fence != nil {
		if err := fence(ctx); err != nil {
			return upstreamtransport.Result{}, err
		}
	}
	index := len(s.requests) - 1
	var result upstreamtransport.Result
	if index < len(s.results) {
		result = s.results[index]
	}
	var err error
	if index < len(s.errs) {
		err = s.errs[index]
	}
	return result, err
}

var _ OAuthProbeSnapshotRuntime = (*oauthProbeSnapshotRuntimeStub)(nil)
var _ OAuthProbeCoordinator = oauthProbeCoordinatorStub{}
var _ AttemptTransport = (*sequenceAttemptTransport)(nil)
