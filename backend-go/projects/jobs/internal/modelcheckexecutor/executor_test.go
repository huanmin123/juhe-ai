package modelcheckexecutor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestExecuteInputRunsGoProbeAndCommitsDurableOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "executor.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	draft := validDraft(now)
	issued, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(context.Context, string, string) (ResolvedTarget, error) {
		return ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil || payload.Item.Status != "passed" || payload.InputVersion != 1 {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
}

func TestExecuteInputRejectsRevisionDriftBeforeNetwork(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "executor-stale.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(context.Context, string, string) (ResolvedTarget, error) {
		return ResolvedTarget{ConfigRevision: "config-revision-drifted", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "stale") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func validDraft(issuedAt time.Time) modelcheckinput.Draft {
	return modelcheckinput.Draft{InputID: "executor-input", SystemAccountID: "system-account", ActorSystemAccountID: "actor-account", Target: modelcheckinput.AccountSnapshot{ID: "target-account", ConfigRevision: "config-revision-1", ProviderCode: "openai", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", EndpointFingerprint: "endpoint-hmac-1", MappedUpstreamModel: "gpt-5.6-sol", CredentialEnvelopeRef: "credential-alias-1", ProxyConfigurationVersion: "proxy-revision-1"}, Model: "gpt-5.6-sol", Profile: "quick", Trigger: modelcheckinput.TriggerManual, ProbeSetVersion: "probe-v1", Policy: modelcheckinput.PolicySnapshot{Revision: "policy-revision-1", Digest: "policy-digest-1"}, IssuedAt: issuedAt, DeadlineAt: issuedAt.Add(time.Minute)}
}
