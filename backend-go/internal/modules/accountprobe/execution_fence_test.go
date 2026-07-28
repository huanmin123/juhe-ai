package accountprobe

import (
	"context"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

func TestAPIKeyExecutionFenceRechecksVersionIdentityProxyAndExactKey(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	observation := now.Add(-time.Minute)
	candidate := apiKeyCandidate("openai", "gpt", "profile", "https://api.example", map[string]any{"api_key": "key"})
	candidate.Projection.AccountID = "account"
	candidate.Projection.SystemAccountID = "system"
	candidate.Projection.GroupID = "group"
	candidate.Projection.ConfigRevision = 3
	candidate.Projection.DispatchRevision = 4
	candidate.Projection.AccountAuthorizationID = "binding"
	candidate.Proxy = &gatewaycandidatewindow.ProxyRuntime{
		ID: "proxy", Type: "http", Host: "proxy.example", Port: 8080, Username: "user",
		Credentials: gatewaycandidatewindow.NewCredentialSet(map[string]any{"password": "password"}), Enabled: true, Available: true,
	}
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeResponsesJSON, Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := PrepareAPIKeyAttempt(candidate, prepared, now)
	if err != nil {
		t.Fatal(err)
	}
	expected := port.CooldownAccountRetestCandidate{
		ID: "account", SystemAccountID: "system", GroupID: "group", ConfigRevision: 3, DispatchRevision: 4,
		ObservationStartedAt: &observation, Generation: "generation", HealthCheckModel: "model", HealthCheckEndpointMode: string(ModeResponsesJSON),
	}
	loader := &exactCandidateLoaderStub{candidate: candidate, found: true}
	current := &cooldownCandidateReaderStub{candidate: expected, found: true}
	fence := APIKeyExecutionFence{
		Loader: loader, Current: current, LoadInput: LoadInput{AccountID: "account", GroupID: "group", SystemAccountID: "system", RequestedModel: "model", EndpointFamily: "responses"},
		Expected: expected, Candidate: candidate, Prepared: prepared, Attempt: attempt, Now: func() time.Time { return now },
	}
	if err := fence.Recheck(t.Context()); err != nil {
		t.Fatalf("Recheck() error = %v", err)
	}
	if !loader.input.Now.Equal(now) {
		t.Fatalf("reload time = %v", loader.input.Now)
	}
	current.candidate.Generation = "new-generation"
	if err := fence.Recheck(t.Context()); err == nil || !strings.Contains(err.Error(), "cooldown generation") {
		t.Fatalf("generation error = %v", err)
	}
	current.candidate = expected

	tests := []struct {
		name   string
		mutate func(*gatewaycandidatewindow.Candidate)
		match  string
	}{
		{name: "target revision", mutate: func(value *gatewaycandidatewindow.Candidate) { value.Projection.ConfigRevision++ }, match: "revision"},
		{name: "authorization binding", mutate: func(value *gatewaycandidatewindow.Candidate) { value.Projection.AccountAuthorizationID = "new-binding" }, match: "candidate changed"},
		{name: "proxy password", mutate: func(value *gatewaycandidatewindow.Candidate) {
			value.Proxy.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"password": "new"})
		}, match: "candidate changed"},
		{name: "key fingerprint", mutate: func(value *gatewaycandidatewindow.Candidate) { value.APIKeyRuntime[0].KeyFingerprint = "new" }, match: "credential changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneExecutionCandidate(candidate)
			test.mutate(&changed)
			loader.candidate = changed
			if err := fence.Recheck(t.Context()); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Recheck() error = %v", err)
			}
		})
	}
}

func TestAPIKeyExecutionFenceRequiresMatchingAuthorizedSourceRevision(t *testing.T) {
	candidate := apiKeyCandidate("openai", "binding", "binding-profile", "https://api.example", map[string]any{"api_key": "key"})
	candidate.Projection.AccountID = "binding"
	candidate.Projection.SystemAccountID = "system"
	candidate.Projection.GroupID = "group"
	candidate.Projection.ConfigRevision = 3
	candidate.Projection.DispatchRevision = 4
	candidate.Projection.ResourceAccountID = "source"
	candidate.Projection.ResourceProviderCode = "gpt"
	candidate.Projection.ResourceProviderProtocolProfileID = "profile"
	candidate.Projection.ResourceProtocolCode = "openai"
	candidate.Projection.ResourceProtocolVersion = "v1"
	candidate.Projection.ResourceType = "api_key"
	candidate.Projection.ResourceConfigRevision = 7
	candidate.Projection.ResourceDispatchRevision = 8
	sourceRevision := 7
	expected := port.CooldownAccountRetestCandidate{ID: "binding", SystemAccountID: "system", GroupID: "group", ConfigRevision: 3, DispatchRevision: 4, SourceConfigRevision: &sourceRevision}
	if err := verifyCooldownCandidateVersion(expected, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Projection.ResourceConfigRevision++
	if err := verifyCooldownCandidateVersion(expected, candidate); err == nil || !strings.Contains(err.Error(), "source revision") {
		t.Fatalf("source revision error = %v", err)
	}
}

func cloneExecutionCandidate(value gatewaycandidatewindow.Candidate) gatewaycandidatewindow.Candidate {
	clone := value
	clone.APIKeyRuntime = append([]gatewaycandidatewindow.APIKeyRuntime(nil), value.APIKeyRuntime...)
	if value.Proxy != nil {
		proxy := *value.Proxy
		clone.Proxy = &proxy
	}
	return clone
}

type exactCandidateLoaderStub struct {
	candidate gatewaycandidatewindow.Candidate
	found     bool
	err       error
	input     LoadInput
	calls     int
}

func (s *exactCandidateLoaderStub) Load(_ context.Context, input LoadInput) (gatewaycandidatewindow.Candidate, bool, error) {
	s.input = input
	s.calls++
	return s.candidate, s.found, s.err
}

type cooldownCandidateReaderStub struct {
	candidate port.CooldownAccountRetestCandidate
	found     bool
	err       error
}

func (s *cooldownCandidateReaderStub) FindDueCooldownAccountRetest(context.Context, string, time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	return s.candidate, s.found, s.err
}
