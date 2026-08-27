package modelcheckhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
)

func TestJSONRunDrivesGoRuntimeAndRejectsTrailingOrUnknownJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	service, _, now := newHTTPTestService(t, server.URL)
	handler := newTestHandler(service)
	body := `{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol","profile":"quick"}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"data"`) || !strings.Contains(recorder.Body.String(), `"status":"completed"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	_ = now
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol","unexpected":true}`)))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	trailing := httptest.NewRecorder()
	handler.ServeHTTP(trailing, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body+` {}`)))
	if trailing.Code != http.StatusBadRequest {
		t.Fatalf("trailing status=%d body=%s", trailing.Code, trailing.Body.String())
	}
}

func TestJSONRunDefaultsProfileAndRejectsIncompleteTrustedComparison(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	service, _, _ := newHTTPTestService(t, server.URL)
	handler := newTestHandler(service)
	var received Command
	original := handler.BuildRequest
	handler.BuildRequest = func(ctx context.Context, scope Scope, command Command) (modelcheckruntime.RunRequest, error) {
		received = command
		return original(ctx, scope, command)
	}
	defaultProfile := httptest.NewRecorder()
	handler.ServeHTTP(defaultProfile, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`)))
	if defaultProfile.Code != http.StatusOK || received.Profile != modelcheckprofile.DefaultProfile {
		t.Fatalf("default profile status=%d command=%#v body=%s", defaultProfile.Code, received, defaultProfile.Body.String())
	}
	trustedWithoutAccount := httptest.NewRecorder()
	handler.ServeHTTP(trustedWithoutAccount, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol","trustedComparison":true}`)))
	if trustedWithoutAccount.Code != http.StatusBadRequest {
		t.Fatalf("trusted comparison status=%d body=%s", trustedWithoutAccount.Code, trustedWithoutAccount.Body.String())
	}
}

func TestSSEEmitsConnectedHeartbeatAndComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	service, _, _ := newHTTPTestService(t, server.URL)
	handler := newTestHandler(service)
	handler.Heartbeat = 5 * time.Millisecond
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol","profile":"quick"}`)))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, ": connected") || !strings.Contains(body, ": heartbeat") || !strings.Contains(body, "event: progress") || !strings.Contains(body, `"type":"run_started"`) || !strings.Contains(body, "event: complete") {
		t.Fatalf("status=%d body=%s", recorder.Code, body)
	}
}

func TestActiveAndStopEndpointsUseGoRegistry(t *testing.T) {
	service, _, _ := newHTTPTestService(t, "http://unused.invalid")
	registry := modelcheckactive.NewRegistry()
	service.Active = registry
	_, acquired, _ := registry.TryStart(context.Background(), "system-account:system-account", modelcheckactive.Summary{RunID: "run-1", TargetID: "target-account"})
	if !acquired {
		t.Fatal("failed to seed active run")
	}
	handler := newTestHandler(service)
	active := httptest.NewRecorder()
	handler.ServeHTTP(active, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if active.Code != http.StatusOK || !strings.Contains(active.Body.String(), `"data"`) || !strings.Contains(active.Body.String(), `"runId":"run-1"`) {
		t.Fatalf("active status=%d body=%s", active.Code, active.Body.String())
	}
	stop := httptest.NewRecorder()
	handler.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/run/stop", nil))
	if stop.Code != http.StatusOK || !strings.Contains(stop.Body.String(), `"stopped":true`) {
		t.Fatalf("stop status=%d body=%s", stop.Code, stop.Body.String())
	}
}

func TestRunConflictIsReportedBeforeSSEOrJSONSuccess(t *testing.T) {
	service, _, _ := newHTTPTestService(t, "http://unused.invalid")
	registry := modelcheckactive.NewRegistry()
	service.Active = registry
	_, acquired, _ := registry.TryStart(context.Background(), "system-account:system-account", modelcheckactive.Summary{RunID: "existing-run"})
	if !acquired {
		t.Fatal("failed to seed active run")
	}
	handler := newTestHandler(service)
	for _, path := range []string{"/run", "/run/stream"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`)))
		if recorder.Code != http.StatusConflict || recorder.Header().Get("Retry-After") != "1" || !strings.Contains(recorder.Body.String(), `"active"`) || strings.Contains(recorder.Body.String(), ": connected") {
			t.Fatalf("path=%s status=%d retry=%q body=%s", path, recorder.Code, recorder.Header().Get("Retry-After"), recorder.Body.String())
		}
	}
}

func newTestHandler(service *modelcheckruntime.Service) *Handler {
	return &Handler{
		Service: service,
		Active:  service.Active,
		Authorize: func(context.Context, *http.Request) (Scope, error) {
			return Scope{SystemAccountID: "system-account"}, nil
		},
		BuildRequest: func(_ context.Context, scope Scope, command Command) (modelcheckruntime.RunRequest, error) {
			if scope.SystemAccountID == "" {
				return modelcheckruntime.RunRequest{}, errors.New("scope missing")
			}
			now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
			request := modelcheckruntime.RunRequest{SystemAccountID: scope.SystemAccountID, ActorSystemAccountID: "actor-account", Target: modelcheckinput.AccountSnapshot{ID: command.TargetID, ConfigRevision: "config-revision-1", ProviderCode: "openai", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", EndpointFingerprint: "endpoint-hmac-1", MappedUpstreamModel: command.Model, CredentialEnvelopeRef: "credential-alias-1", ProxyConfigurationVersion: "proxy-revision-1"}, Model: command.Model, Profile: command.Profile, Trigger: modelcheckinput.TriggerManual, ProbeSetVersion: "probe-v1", Policy: testPolicySnapshot(), StartedAt: now, DeadlineAt: now.Add(time.Minute), ProviderCode: "openai", TargetType: "account", TargetName: "Target"}
			if command.TrustedComparison {
				comparison := request.Target
				comparison.ID = command.TrustedComparisonID
				request.Comparison = &comparison
				request.TrustedComparison = true
			}
			return request, nil
		},
	}
}

func testPolicySnapshot() modelcheckinput.PolicySnapshot {
	policy, err := modelcheckinput.NewPolicySnapshot("policy-revision-1", "quick", true, 70, "fallback", 10)
	if err != nil {
		panic(err)
	}
	return policy
}

func newHTTPTestService(t *testing.T, endpoint string) (*modelcheckruntime.Service, string, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	durablePath := filepath.Join(t.TempDir(), "durable.sqlite3")
	durable, err := modelcheckdurable.OpenSQLite(durablePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := durable.EnsureSchema(context.Background()); err != nil {
		durable.Close()
		t.Fatal(err)
	}
	datasetPath := filepath.Join(t.TempDir(), "dataset.sqlite3")
	dataset, err := modelcheckstore.OpenSQLite(datasetPath)
	if err != nil {
		durable.Close()
		t.Fatal(err)
	}
	if err := dataset.EnsureSchema(context.Background()); err != nil {
		durable.Close()
		dataset.Close()
		t.Fatal(err)
	}
	service := &modelcheckruntime.Service{Durable: durable, Dataset: dataset, Active: modelcheckactive.NewRegistry(), Resolver: func(context.Context, modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
		return modelcheckexecutor.ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: endpoint, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, Retry: modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }}, Now: func() time.Time { return now }, NewID: func(prefix string) string { return prefix + "-http" }}
	t.Cleanup(func() { _ = durable.Close(); _ = dataset.Close() })
	return service, datasetPath, now
}
