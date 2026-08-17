package accounthealth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeDirectInputLoader struct {
	due      []Input
	explicit map[string]Input
	loaded   []string
}

func (loader *fakeDirectInputLoader) LoadDue(_ context.Context, _ int) ([]Input, error) {
	return loader.due, nil
}

func (loader *fakeDirectInputLoader) LoadAccount(_ context.Context, accountID string) ([]Input, error) {
	loader.loaded = append(loader.loaded, accountID)
	input, found := loader.explicit[accountID]
	if !found {
		return nil, nil
	}
	return []Input{input}, nil
}

func TestRunnerPersistsDirectProbeOutcomeInJobsStore(t *testing.T) {
	secret := "scheduler-credential-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()
	root := t.TempDir()
	input := testInput(server.URL, "chat_json")
	input.APIKeys = []APIKeyInput{{Index: 0, Fingerprint: "key-1", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}}}
	input.KeySetFingerprint = "keyset-1"
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("scheduler-input-signing-key-123456")
	if err := os.WriteFile(filepath.Join(root, "account-1"+inputFileSuffix), signedEnvelope(t, "current", key, payload), 0o600); err != nil {
		t.Fatal(err)
	}
	explicitPayload, err := json.Marshal(ProbeRequest{RequestID: "request-1", AccountID: input.AccountID, Reason: "activation", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: time.Now().UTC().Add(time.Minute), MutateAccount: true})
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, "request-1"+requestFileSuffix)
	if err := os.WriteFile(requestPath, signedEnvelope(t, "current", key, explicitPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(root, "state.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "runner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t lease=%#v err=%v", acquired, lease, err)
	}
	runner := NewRunner(Config{InputDirectory: root, InputKeys: map[string][]byte{"current": key}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, Now: time.Now}, store, nil)
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.LoadCurrentState(context.Background(), input.AccountID)
	if err != nil || !found {
		t.Fatalf("found=%t state=%#v err=%v", found, state, err)
	}
	if state.Outcome != OutcomeSuccess || state.AccountStatus != "active" || state.NextDueAt == nil {
		t.Fatalf("unexpected current state: %#v", state)
	}
	if _, err := os.Stat(requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed request must be removed, stat err=%v", err)
	}
}

func TestRunnerCancellationReleasesLeaseForRestart(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "account-health.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	inputRoot := t.TempDir()
	runner := NewRunner(Config{
		InstanceID:       "runner-a",
		InputDirectory:   inputRoot,
		InputKeys:        map[string][]byte{"current": []byte("test-key")},
		CredentialSecret: "test-secret",
		ScanInterval:     time.Hour,
		OwnerLease:       15 * time.Second,
		ProbeTimeout:     time.Second,
		MaxResponseBytes: 1024,
		MaxConcurrency:   1,
		Now:              time.Now,
	}, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !runner.Ready() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !runner.Ready() {
		cancel()
		t.Fatal("runner did not acquire its lease")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runner shutdown error = %v, want context canceled", err)
	}
	if _, acquired, err := store.AcquireOwnerLease(context.Background(), "runner-b", time.Minute); err != nil || !acquired {
		t.Fatalf("replacement owner must acquire released lease: acquired=%t err=%v", acquired, err)
	}
}

func TestRunnerDirectPostgresInputLoadsExplicitRequestEvenWhenNotDue(t *testing.T) {
	secret := "direct-explicit-credential-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()

	root := t.TempDir()
	input := testInput(server.URL, "chat_json")
	input.APIKeys = []APIKeyInput{{Index: 0, Fingerprint: "key-1", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}}}
	input.KeySetFingerprint = "keyset-1"
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	key := []byte("scheduler-input-signing-key-123456")
	request := ProbeRequest{RequestID: "direct-request-1", AccountID: input.AccountID, Reason: "request_failure", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: time.Now().UTC().Add(time.Minute), MutateAccount: true}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, request.RequestID+requestFileSuffix)
	if err := os.WriteFile(requestPath, signedEnvelope(t, "current", key, payload), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(root, "state.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "runner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t lease=%#v err=%v", acquired, lease, err)
	}
	loader := &fakeDirectInputLoader{explicit: map[string]Input{input.AccountID: input}}
	runner := NewRunner(Config{InputDirectory: root, InputKeys: map[string][]byte{"current": key}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, DirectInputLimit: 8, Now: time.Now}, store, nil)
	runner.directInputReader = loader
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if len(loader.loaded) != 1 || loader.loaded[0] != input.AccountID {
		t.Fatalf("explicit direct load = %#v, want [%q]", loader.loaded, input.AccountID)
	}
	state, found, err := store.LoadCurrentState(context.Background(), input.AccountID)
	if err != nil || !found || state.Outcome != OutcomeSuccess {
		t.Fatalf("explicit direct request must persist successful outcome: found=%t state=%#v err=%v", found, state, err)
	}
	if _, err := os.Stat(requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed request must be removed, stat err=%v", err)
	}
}

func TestRunnerDirectPostgresInputDoesNotMergeSignedPeriodicSnapshots(t *testing.T) {
	root := t.TempDir()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	key := []byte("scheduler-input-signing-key-123456")
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, input.AccountID+inputFileSuffix), signedEnvelope(t, "current", key, payload), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(root, "state.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "runner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t lease=%#v err=%v", acquired, lease, err)
	}
	loader := &fakeDirectInputLoader{}
	runner := NewRunner(Config{InputDirectory: root, InputKeys: map[string][]byte{"current": key}, CredentialSecret: "unused", ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, DirectInputLimit: 8, Now: time.Now}, store, nil)
	runner.directInputReader = loader
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if len(loader.loaded) != 0 {
		t.Fatalf("no explicit request must not load an account: %#v", loader.loaded)
	}
	if _, found, err := store.LoadCurrentState(context.Background(), input.AccountID); err != nil || found {
		t.Fatalf("signed periodic snapshot must not run in PG direct mode: found=%t err=%v", found, err)
	}
}

func TestRunnerDirectInputRefreshesDueFenceAfterRead(t *testing.T) {
	secret := "direct-due-fence-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()
	start := time.Now().UTC().Truncate(time.Millisecond)
	input := testInput(server.URL, "chat_json")
	input.IssuedAt = start.Add(time.Millisecond)
	input.ExpiresAt = start.Add(time.Hour)
	input.APIKeys = []APIKeyInput{{Index: 0, Fingerprint: "key-1", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}}}
	input.KeySetFingerprint = "keyset-1"
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "state.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "runner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t lease=%#v err=%v", acquired, lease, err)
	}
	clockCalls := 0
	now := func() time.Time {
		clockCalls++
		return start.Add(time.Duration(clockCalls-1) * 2 * time.Millisecond)
	}
	loader := &fakeDirectInputLoader{due: []Input{input}}
	runner := NewRunner(Config{InputDirectory: t.TempDir(), InputKeys: map[string][]byte{"current": []byte("test-key")}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, DirectInputLimit: 8, Now: now}, store, nil)
	runner.directInputReader = loader
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.LoadCurrentState(context.Background(), input.AccountID)
	if err != nil || !found || state.Outcome != OutcomeSuccess {
		t.Fatalf("direct input issued during the read must run in the same cycle: found=%t state=%#v err=%v", found, state, err)
	}
}

func TestApplyCooldownNeutralKeepsTemporaryUnavailableAndDefers(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	outcome := Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{AccountStatus: "temporary_unavailable", FailureCount: 2, CooldownFence: fence}, true, "cooldown_retest")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.NextDueAt == nil || !outcome.NextDueAt.After(now) || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" || outcome.CooldownFence == nil || outcome.CooldownFence.Generation != fence.Generation || outcome.Projection.ExpectedAccountStatus != "temporary_unavailable" || outcome.Projection.ExpectedCooldownFence == nil || outcome.Projection.ExpectedCooldownFence.Generation != fence.Generation {
		t.Fatalf("unexpected cooldown defer: %#v", outcome)
	}
}

func TestApplyHealthTransitionCarriesExpectedAccountStatus(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "pending_test", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	outcome := Outcome{Outcome: OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyOutcomeDecision(&outcome, input, CurrentState{}, false, "health")
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "activation_success" || outcome.Projection.ExpectedAccountStatus != "pending_test" || outcome.Projection.ExpectedCooldownFence != nil {
		t.Fatalf("health projection must carry exact expected status: %#v", outcome.Projection)
	}
}

func TestPendingTestInputEligibleBeforeActivation(t *testing.T) {
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "pending_test", Schedulable: false, BoundGroup: true, AuthorizationEligible: true}
	if !inputEligible(input) {
		t.Fatal("pending_test must remain probeable before activation sets schedulable")
	}
}

func TestNextDueBootstrapsSignedCooldownState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	kind, due, ok := nextDue(input, CurrentState{}, false, now)
	if !ok || kind != "cooldown_retest" || !due.Equal(cooldownUntil) {
		t.Fatalf("unexpected bootstrap cooldown due: kind=%q due=%s ok=%t", kind, due, ok)
	}
}
