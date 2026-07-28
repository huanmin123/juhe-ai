package accountprobe

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
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

type candidateTransportFactoryStub struct {
	transport AttemptTransport
	err       error
}

func (s candidateTransportFactoryStub) New(gatewaycandidatewindow.Candidate) (AttemptTransport, error) {
	return s.transport, s.err
}

var _ CandidateTransportFactory = candidateTransportFactoryStub{}
