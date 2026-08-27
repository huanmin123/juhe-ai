package modelcheckexecutor

import (
	"context"
	"errors"
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
	payload, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		if request.Input.SystemAccountID != "system-account" || request.Input.Model != "gpt-5.6-sol" || request.Account.ID != "target-account" {
			t.Fatalf("resolver request=%#v", request)
		}
		return ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil || payload.Item.Status != "passed" || len(payload.Items) != 4 || payload.InputVersion != 1 {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
}

func TestExecuteInputWithOptionsEmitsEachCommittedItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "executor-progress.sqlite3"))
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
	var observed []string
	payload, err := ExecuteInputWithOptions(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(context.Context, ResolutionRequest) (ResolvedTarget, error) {
		return ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }}, ExecuteOptions{OnItem: func(item modelcheckprobe.EvaluationItem) {
		observed = append(observed, item.ItemKey)
	}})
	if err != nil || len(observed) != len(payload.Items) {
		t.Fatalf("payload=%#v observed=%#v err=%v", payload, observed, err)
	}
	for index, item := range payload.Items {
		if observed[index] != item.ItemKey {
			t.Fatalf("index=%d observed=%q item=%q", index, observed[index], item.ItemKey)
		}
	}
}

func TestExecuteInputRunsTrustedComparisonInGo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "executor-trusted.sqlite3"))
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
	comparison := draft.Target
	comparison.ID = "comparison-account"
	draft.TrustedComparison = true
	draft.Comparison = &comparison
	issued, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		snapshot := request.Account
		accountID, revision := snapshot.ID, snapshot.ConfigRevision
		if accountID != "target-account" && accountID != "comparison-account" {
			t.Fatalf("unexpected account=%q", accountID)
		}
		return ResolvedTarget{ConfigRevision: revision, ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if findPayloadItem(payload.Items, "trusted_comparison.responses_basic") == nil || findPayloadItem(payload.Items, "trusted_comparison.comparison") == nil || payload.Summary.Level == "unavailable" {
		t.Fatalf("trusted payload=%#v", payload)
	}
}

func TestExecuteInputRunsTargetOnlyExtensionsBeforeTrustedComparison(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "executor-full-trusted.sqlite3"))
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
	draft.Profile = "full"
	comparison := draft.Target
	comparison.ID = "comparison-account"
	draft.TrustedComparison = true
	draft.Comparison = &comparison
	issued, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		snapshot := request.Account
		revision := snapshot.ConfigRevision
		return ResolvedTarget{ConfigRevision: revision, ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if findPayloadItem(payload.Items, "target.token_integrity") == nil || findPayloadItem(payload.Items, "target.identity_observation") == nil {
		t.Fatalf("target-only extensions missing: %#v", payload.Items)
	}
	if findPayloadItem(payload.Items, "trusted_comparison.token_integrity") != nil || findPayloadItem(payload.Items, "trusted_comparison.identity_observation") != nil {
		t.Fatalf("comparison incorrectly ran target-only extensions: %#v", payload.Items)
	}
	if itemIndex(payload.Items, "target.identity_observation") >= itemIndex(payload.Items, "trusted_comparison.responses_basic") {
		t.Fatalf("target extensions must complete before comparison suite: %#v", payload.Items)
	}
}

func TestExecuteInputStopsTrustedComparisonAfterTargetTransportFailure(t *testing.T) {
	comparisonCalls := 0
	comparisonServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		comparisonCalls++
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer comparisonServer.Close()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "executor-terminal-transport.sqlite3"))
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
	comparison := draft.Target
	comparison.ID = "comparison-account"
	draft.TrustedComparison = true
	draft.Comparison = &comparison
	issued, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		snapshot := request.Account
		target := ResolvedTarget{ConfigRevision: snapshot.ConfigRevision, ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}
		if snapshot.ID == "comparison-account" {
			target.Endpoint = comparisonServer.URL
			return target, nil
		}
		target.Endpoint = "https://target.invalid"
		target.Client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("forced transport failure")
		})}
		return target, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if findPayloadItem(payload.Items, "trusted_comparison.responses_basic") != nil || comparisonCalls != 0 {
		t.Fatalf("comparison probe ran after terminal target transport failure: items=%#v calls=%d", payload.Items, comparisonCalls)
	}
	if len(payload.Items) != 1 || payload.Items[0].Evidence["requestFailure"] != true {
		t.Fatalf("terminal target outcome=%#v", payload.Items)
	}
}

func findPayloadItem(items []modelcheckprobe.EvaluationItem, key string) *modelcheckprobe.EvaluationItem {
	for index := range items {
		if items[index].ItemKey == key {
			return &items[index]
		}
	}
	return nil
}

func itemIndex(items []modelcheckprobe.EvaluationItem, key string) int {
	for index, item := range items {
		if item.ItemKey == key {
			return index
		}
	}
	return len(items)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
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
	_, err = ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(context.Context, ResolutionRequest) (ResolvedTarget, error) {
		return ResolvedTarget{ConfigRevision: "config-revision-drifted", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "stale") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func validDraft(issuedAt time.Time) modelcheckinput.Draft {
	return modelcheckinput.Draft{InputID: "executor-input", SystemAccountID: "system-account", ActorSystemAccountID: "actor-account", Target: modelcheckinput.AccountSnapshot{ID: "target-account", ConfigRevision: "config-revision-1", ProviderCode: "openai", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", EndpointFingerprint: "endpoint-hmac-1", MappedUpstreamModel: "gpt-5.6-sol", CredentialEnvelopeRef: "credential-alias-1", ProxyConfigurationVersion: "proxy-revision-1"}, Model: "gpt-5.6-sol", Profile: "quick", Trigger: modelcheckinput.TriggerManual, ProbeSetVersion: "probe-v1", Policy: testPolicySnapshot(), IssuedAt: issuedAt, DeadlineAt: issuedAt.Add(time.Minute)}
}

func testPolicySnapshot() modelcheckinput.PolicySnapshot {
	policy, err := modelcheckinput.NewPolicySnapshot("policy-revision-1", "quick", true, 70, "fallback", 10)
	if err != nil {
		panic(err)
	}
	return policy
}
