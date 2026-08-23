package proxylatency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadRuntimeConfigDisabledIsFailClosed(t *testing.T) {
	cfg, err := LoadRuntimeConfig(func(string) string { return "" })
	if err != nil || cfg.Enabled {
		t.Fatalf("disabled config=%+v err=%v", cfg, err)
	}
}

func TestLoadRuntimeConfigEnabledRequiresCompletePostgresConfig(t *testing.T) {
	env := map[string]string{"JUHE_AI_PROXY_LATENCY_ENABLED": "true", "JUHE_AI_PROXY_LATENCY_JOBS_OWNER": "go"}
	cfg, err := LoadRuntimeConfig(func(name string) string { return env[name] })
	if err == nil || cfg.Enabled || !strings.Contains(err.Error(), "INSTANCE_ID") {
		t.Fatalf("expected fail-closed config, cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadRuntimeConfigUsesNodeBatchAndCandidatePoolDefaults(t *testing.T) {
	env := map[string]string{
		"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "jobs-test",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        "postgres://jobs",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  "postgres://business",
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": "postgres://business-writer",
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "credential-secret",
	}
	cfg, err := LoadRuntimeConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InputLimit != 80 || cfg.BatchSize != 20 || cfg.CandidatePoolFactor != 4 || cfg.WorkerConcurrency != 4 {
		t.Fatalf("unexpected Node-aligned scheduler defaults: %+v", cfg)
	}
}

func TestRunnerBatchAndCandidatePoolLimits(t *testing.T) {
	runner := &Runner{cfg: RuntimeConfig{InputLimit: 80, BatchSize: 20, CandidatePoolFactor: 4, WorkerConcurrency: 4}}
	if got := runner.batchSize(); got != 20 {
		t.Fatalf("batch size=%d, want 20", got)
	}
	if got := runner.candidatePoolLimit(); got != 80 {
		t.Fatalf("candidate pool=%d, want 80", got)
	}
	if got := runner.workerConcurrency(); got != 4 {
		t.Fatalf("worker concurrency=%d, want 4", got)
	}
}

type fakeInputReader struct {
	drafts []InputDraft
	err    error
}

func (f fakeInputReader) LoadDue(context.Context, int) ([]InputDraft, error) { return f.drafts, f.err }

func testRuntimeConfig(t *testing.T) RuntimeConfig {
	t.Helper()
	return RuntimeConfig{Enabled: true, InstanceID: "test-owner", Store: StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "jobs.sqlite3")}, InputLimit: 10, InputTTL: time.Minute, Interval: time.Hour, OwnerLease: time.Minute, ProxyLease: 30 * time.Second, ProbeTimeout: time.Second, CredentialSecret: "test-secret", Now: time.Now}
}

func testDraft(proxyID string) InputDraft {
	now := time.Now().UTC()
	return InputDraft{ProxyID: proxyID, ConfigRevision: now.Add(-time.Second).Format(time.RFC3339Nano), Trigger: TriggerPeriodic, IssuedAt: now, ExpiresAt: now.Add(time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion, ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 1, Targets: []Target{{Provider: "openai", ProfileID: "profile", URL: "http://provider.invalid/"}}}
}

func TestRunnerCycleOrdersLeasesAndIsolatesProxyFailures(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reader := fakeInputReader{drafts: []InputDraft{{ProxyID: "bad", ProxyType: "bad"}, testDraft("good")}}
	runner := NewRunner(cfg, store, reader, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease acquired=%v err=%v", acquired, err)
	}
	runner.setOwnerHeld(true)
	if err := runner.runCycle(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	status := runner.Status()
	if status.Inputs != 1 || status.ProxyFailures != 1 || status.Executed != 1 || runner.Ready() || status.LastSuccess.IsZero() == false || status.LastError == "" {
		t.Fatalf("unexpected status=%+v ready=%v", status, runner.Ready())
	}
	if status.Selected != 2 || status.Target != 2 || status.Started != 2 || status.Processed != 1 || status.Deferred != 0 || !status.Partial {
		t.Fatalf("scheduler counters lost failure/partial semantics: %+v", status)
	}
}

func TestRunnerStopsCycleWhenProxyLeaseAcquisitionLosesOwner(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{drafts: []InputDraft{testDraft("owner-lost-during-proxy-acquire")}}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease acquired=%v err=%v", acquired, err)
	}
	runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
		return ProxyLease{}, false, ErrOwnerLeaseLost
	}
	err = runner.runCycle(context.Background(), owner)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("owner loss during proxy acquire must stop cycle: %v", err)
	}
	if status := runner.Status(); !strings.Contains(status.LastError, ErrOwnerLeaseLost.Error()) {
		t.Fatalf("owner loss was not visible in cycle status: %+v", status)
	}
}

func TestRunnerPreservesPriorReleaseErrorWhenOwnerIsLostOnNextDraft(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	drafts := []InputDraft{testDraft("release-before-owner-loss"), testDraft("owner-loss-next-draft")}
	runner := NewRunner(cfg, store, fakeInputReader{drafts: drafts}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease acquired=%v err=%v", acquired, err)
	}
	var executeCount int
	runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
		executeCount++
		if executeCount == 1 {
			if err := store.ReleaseOwnerLease(context.Background(), owner); err != nil {
				t.Fatalf("release owner in test hook: %v", err)
			}
		}
		return Outcome{}, false, nil
	}
	releaseErr := errors.New("release before owner loss")
	runner.releaseProxyLease = func(context.Context, ProxyLease) error { return releaseErr }
	err = runner.runCycle(context.Background(), owner)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("owner loss on next draft must stop cycle: %v", err)
	}
	status := runner.Status()
	if !strings.Contains(status.LastError, ErrOwnerLeaseLost.Error()) || !strings.Contains(status.LastError, releaseErr.Error()) {
		t.Fatalf("prior release error was lost after owner loss: status=%+v", status)
	}
}

func TestRenewOwnerLeaseKeepsFenceAndRejectsLostOwner(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("acquire owner=%v err=%v", acquired, err)
	}
	if err := store.RenewOwnerLease(context.Background(), owner, cfg.OwnerLease); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseOwnerLease(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewOwnerLease(context.Background(), owner, cfg.OwnerLease); err == nil {
		t.Fatal("renew after release must fail")
	}
}

func TestExecutionWindowCannotOutliveProxyLeaseOrInputExpiry(t *testing.T) {
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	if got := executionWindow(now, now.Add(10*time.Second), 3*time.Second); got != 3*time.Second {
		t.Fatalf("proxy lease must cap execution window: %s", got)
	}
	if got := executionWindow(now, now.Add(2*time.Second), 3*time.Second); got != 2*time.Second {
		t.Fatalf("input expiry must cap execution window: %s", got)
	}
	if got := executionWindow(now, now, time.Second); got != 0 {
		t.Fatalf("expired input must have no execution window: %s", got)
	}
}

func TestExecutionWindowUsesPersistedLeaseDeadline(t *testing.T) {
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	if got := executionWindowUntil(now, now.Add(10*time.Second), now.Add(1500*time.Millisecond)); got != 1500*time.Millisecond {
		t.Fatalf("persisted proxy lease must cap execution window: %s", got)
	}
	if got := executionWindowUntil(now, now.Add(10*time.Second), now.Add(-time.Millisecond)); got != 0 {
		t.Fatalf("expired persisted proxy lease must stop execution: %s", got)
	}
}

func TestRunnerStopsImmediatelyWhenOwnerRenewalFails(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.OwnerLease = 30 * time.Millisecond
	cfg.Interval = time.Hour
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease acquired=%v err=%v", acquired, err)
	}
	runner.setOwnerHeld(true)
	runner.renewOwnerLease = func(context.Context, OwnerLease, time.Duration) error { return ErrOwnerLeaseLost }
	start := time.Now()
	err = runner.runOwned(context.Background(), owner)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("renewal loss err=%v", err)
	}
	if elapsed := time.Since(start); elapsed >= cfg.Interval/2 {
		t.Fatalf("renewal loss waited for interval: %s", elapsed)
	}
}

func TestRunnerRunClearsOwnerAfterRenewalFailure(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.OwnerLease = 30 * time.Millisecond
	cfg.Interval = time.Hour
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.renewOwnerLease = func(context.Context, OwnerLease, time.Duration) error { return ErrOwnerLeaseLost }
	// This hook only runs after runOwned returns. If the renewal cancellation
	// is not observed by the interval loop, the test remains blocked until the
	// timeout instead of reaching this hook.
	runner.releaseOwnerLease = func(context.Context, OwnerLease) error {
		cancel()
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runner returned unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runner did not relinquish owner after renewal failure")
	}
	if runner.Status().OwnerHeld {
		t.Fatalf("owner status remained held after renewal failure: %+v", runner.Status())
	}
}

func TestRunnerKeepsProxyReleaseErrorVisible(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{drafts: []InputDraft{testDraft("release-error")}}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease acquired=%v err=%v", acquired, err)
	}
	releaseErr := errors.New("proxy release test failure")
	runner.releaseProxyLease = func(context.Context, ProxyLease) error { return releaseErr }
	_ = runner.runCycle(context.Background(), owner)
	status := runner.Status()
	if !strings.Contains(status.LastError, releaseErr.Error()) || runner.Ready() {
		t.Fatalf("release error was not retained: status=%+v ready=%v", status, runner.Ready())
	}
}

func TestRunnerPreservesReleaseErrorWithExecutionError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.CredentialSecret = "wrong-secret"
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testDraft("release-and-exec-error")
	draft.ProxyUsername = "proxy-user"
	draft.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: testProxyPasswordEnvelope(t, "correct-secret", "proxy-password")}
	runner := NewRunner(cfg, store, fakeInputReader{drafts: []InputDraft{draft}}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease acquired=%v err=%v", acquired, err)
	}
	releaseErr := errors.New("combined proxy release failure")
	runner.releaseProxyLease = func(context.Context, ProxyLease) error { return releaseErr }
	_ = runner.runCycle(context.Background(), owner)
	status := runner.Status()
	if !strings.Contains(status.LastError, releaseErr.Error()) || !strings.Contains(status.LastError, "凭据不可用") {
		t.Fatalf("combined execution/release errors were not retained: status=%+v", status)
	}
}

func TestRunnerPreservesReleaseErrorWithFatalExecutionError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{drafts: []InputDraft{testDraft("fatal-release-and-exec-error")}}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease acquired=%v err=%v", acquired, err)
	}
	executionErr := ErrProxyLeaseLost
	releaseErr := errors.New("fatal proxy release failure")
	runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
		return Outcome{}, false, executionErr
	}
	runner.releaseProxyLease = func(context.Context, ProxyLease) error { return releaseErr }
	err = runner.runCycle(context.Background(), owner)
	if !errors.Is(err, executionErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("fatal execution/release errors were not joined: %v", err)
	}
	status := runner.Status()
	if !strings.Contains(status.LastError, executionErr.Error()) || !strings.Contains(status.LastError, releaseErr.Error()) {
		t.Fatalf("fatal execution/release errors were not visible: status=%+v", status)
	}
}

func TestRunnerCycleCancellationStopsBeforeIssue(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := NewRunner(cfg, store, fakeInputReader{drafts: []InputDraft{testDraft("cancelled")}}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatal(err)
	}
	if err := runner.runCycle(ctx, owner); err == nil {
		t.Fatal("cancelled cycle must fail")
	}
	if status := runner.Status(); status.Inputs != 0 {
		t.Fatalf("cancelled cycle issued input: %+v", status)
	}
}

func TestRunnerHealthReadinessPayload(t *testing.T) {
	runner := NewRunner(RuntimeConfig{Enabled: true}, nil, nil, nil)
	record := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/health", nil)
	runner.HealthHandler().ServeHTTP(record, request)
	if record.Code != 503 {
		t.Fatalf("initial health status=%d", record.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil || payload["j3aEnabled"] != true || payload["ready"] != false {
		t.Fatalf("health payload=%s err=%v", record.Body.String(), err)
	}
}
