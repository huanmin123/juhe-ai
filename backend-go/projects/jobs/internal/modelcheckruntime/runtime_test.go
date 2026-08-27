package modelcheckruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
	_ "modernc.org/sqlite"
)

func TestRunProjectsGoProbeIntoDatasetAndDurableOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()

	durable, dataset, datasetPath, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()
	service := newRuntimeService(durable, dataset, server.URL, now)
	result, err := service.Run(context.Background(), runtimeRequest(now))
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if result.RunStatus != modelcheckstore.RunCompleted || result.InputID == "" || result.OutcomeID == "" || len(result.Items) != 4 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Summary.Level == "" || result.Summary.MaxScore <= 0 {
		t.Fatalf("summary=%#v", result.Summary)
	}

	status, count := readRunStatusAndItemCount(t, datasetPath, result.RunID)
	if status != string(modelcheckstore.RunCompleted) || count != len(result.Items) {
		t.Fatalf("projected status=%s itemCount=%d resultItems=%d", status, count, len(result.Items))
	}
	if decision := readRunQualityDecision(t, datasetPath, result.RunID); !strings.Contains(decision, `"quality_evidence_not_formed"`) || !strings.Contains(decision, `"result":"not_triggered"`) {
		t.Fatalf("quality decision=%s", decision)
	}
	outcomes, err := durable.ListCommittedOutcomes(context.Background(), modelcheckdurable.OutcomeCursor{}, 10)
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome.OutcomeID != result.OutcomeID {
		t.Fatalf("durable outcomes=%#v err=%v", outcomes, err)
	}
}

func TestRunProjectsCanceledFailureEvenWhenCallerContextIsCanceled(t *testing.T) {
	durable, dataset, datasetPath, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()
	service := newRuntimeService(durable, dataset, "http://unused.invalid", now)
	ctx, cancel := context.WithCancel(context.Background())
	service.Resolver = func(context.Context, modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
		cancel()
		return modelcheckexecutor.ResolvedTarget{}, context.Canceled
	}
	result, err := service.Run(ctx, runtimeRequest(now))
	if !errors.Is(err, context.Canceled) || result.RunStatus != modelcheckstore.RunCanceled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	status, count := readRunStatusAndItemCount(t, datasetPath, result.RunID)
	if status != string(modelcheckstore.RunCanceled) || count != 1 {
		t.Fatalf("projected canceled status=%s itemCount=%d", status, count)
	}
}

func TestRunUsesActiveRegistryForStopAndExclusion(t *testing.T) {
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()
	service := newRuntimeService(durable, dataset, "http://unused.invalid", now)
	service.Active = modelcheckactive.NewRegistry()
	started := make(chan struct{})
	service.Resolver = func(ctx context.Context, _ modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return modelcheckexecutor.ResolvedTarget{}, ctx.Err()
	}
	resultCh := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := service.Run(context.Background(), runtimeRequest(now))
		resultCh <- struct {
			result Result
			err    error
		}{result, err}
	}()
	<-started
	if _, err := service.Run(context.Background(), runtimeRequest(now)); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("second run error=%v", err)
	}
	if _, ok := service.Active.Stop("system-account:" + runtimeRequest(now).SystemAccountID); !ok {
		t.Fatal("active run stop was not accepted")
	}
	completed := <-resultCh
	if !errors.Is(completed.err, context.Canceled) || completed.result.RunStatus != modelcheckstore.RunCanceled {
		t.Fatalf("stopped result=%#v err=%v", completed.result, completed.err)
	}
}

func openRuntimeStores(t *testing.T) (*modelcheckdurable.Store, *modelcheckstore.Store, string, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	durable, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "durable.sqlite3"))
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
	return durable, dataset, datasetPath, now
}

func newRuntimeService(durable *modelcheckdurable.Store, dataset *modelcheckstore.Store, endpoint string, now time.Time) *Service {
	return &Service{
		Durable: durable,
		Dataset: dataset,
		Resolver: func(_ context.Context, request modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
			snapshot := request.Account
			if request.Input.SystemAccountID != "system-account" || request.Input.Model != "gpt-5.6-sol" {
				return modelcheckexecutor.ResolvedTarget{}, fmt.Errorf("unexpected resolver input: %#v", request.Input)
			}
			if snapshot.ID != "target-account" || snapshot.ConfigRevision != "config-revision-1" || snapshot.MappedUpstreamModel != "gpt-5.6-sol" {
				return modelcheckexecutor.ResolvedTarget{}, fmt.Errorf("unexpected resolver snapshot: %#v", snapshot)
			}
			return modelcheckexecutor.ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: endpoint, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
		},
		Retry: modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }},
		Now:   nowFunc(now),
		NewID: func(prefix string) string { return prefix + "-fixed" },
	}
}

func runtimeRequest(now time.Time) RunRequest {
	return RunRequest{SystemAccountID: "system-account", ActorSystemAccountID: "actor-account", Target: modelcheckinput.AccountSnapshot{ID: "target-account", ConfigRevision: "config-revision-1", ProviderCode: "openai", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", EndpointFingerprint: "endpoint-hmac-1", MappedUpstreamModel: "gpt-5.6-sol", CredentialEnvelopeRef: "credential-alias-1", ProxyConfigurationVersion: "proxy-revision-1"}, Model: "gpt-5.6-sol", Profile: "quick", Trigger: modelcheckinput.TriggerManual, ProbeSetVersion: "probe-v1", Policy: testPolicySnapshot(), StartedAt: now, DeadlineAt: now.Add(time.Minute), TargetName: "Target", ProviderCode: "openai", TargetType: "account"}
}

func testPolicySnapshot() modelcheckinput.PolicySnapshot {
	policy, err := modelcheckinput.NewPolicySnapshot("policy-revision-1", "quick", true, 70, "fallback", 10)
	if err != nil {
		panic(err)
	}
	return policy
}

func nowFunc(now time.Time) func() time.Time { return func() time.Time { return now } }

func openReadOnlySQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func readRunStatusAndItemCount(t *testing.T, path, runID string) (string, int) {
	t.Helper()
	if path == "" {
		t.Fatalf("dataset path is required")
	}
	db := openReadOnlySQLite(t, path)
	defer db.Close()
	var status string
	if err := db.QueryRow("SELECT status FROM model_check_runs WHERE id=?", runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM model_check_items WHERE run_id=?", runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return status, count
}

func readRunQualityDecision(t *testing.T, path, runID string) string {
	t.Helper()
	db := openReadOnlySQLite(t, path)
	defer db.Close()
	var decision string
	if err := db.QueryRow("SELECT quality_decision_json FROM model_check_runs WHERE id=?", runID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	return decision
}
