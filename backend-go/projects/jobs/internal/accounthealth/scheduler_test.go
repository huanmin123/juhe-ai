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

func TestScheduleMillisecondsAreBoundedWithoutDurationOverflow(t *testing.T) {
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	input := Input{
		AccountID: "bounded", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Eligibility: Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true},
		Schedule:    Schedule{HealthIntervalMS: maxScheduleMilliseconds + 1, FailureThreshold: 1, FailureRetryMS: 3_000},
	}
	if err := validateScheduledInput(input, now); err == nil {
		t.Fatal("schedule duration over the configured upper bound must be rejected")
	}
	if got := durationMS(1<<63-1, time.Second); got != maxScheduleDuration {
		t.Fatalf("durationMS overflow guard=%s, want %s", got, maxScheduleDuration)
	}
	if got := stableJitter("bounded", 1<<63-1); got < 0 || got > maxScheduleDuration {
		t.Fatalf("stableJitter must remain in the bounded duration range: %s", got)
	}
	if got := stableCooldownDefer("bounded", "bounded-generation", 1000, maxScheduleDuration, maxScheduleDuration); got < 0 || got > maxScheduleDuration {
		t.Fatalf("stable cooldown defer overflowed: %s", got)
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

func TestCooldownNeutralDeferUsesObservationGenerationWindow(t *testing.T) {
	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	input := testInput("https://api.example.com", "chat_json")
	fence := &CooldownFence{ObservationStartedAt: start, Generation: "neutral-generation"}
	cooldownUntil := start
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: fence}

	first := Outcome{Outcome: OutcomeNeutral, ObservedAt: start}
	applyOutcomeDecision(&first, input, prior, true, "cooldown_retest")
	if first.NextDueAt == nil {
		t.Fatal("first neutral defer missing due time")
	}
	firstDelay := first.NextDueAt.Sub(start)
	prior.FailureCount = 99
	repeat := Outcome{Outcome: OutcomeTaskFailed, ObservedAt: start}
	applyOutcomeDecision(&repeat, input, prior, true, "cooldown_retest")
	if repeat.NextDueAt == nil || repeat.NextDueAt.Sub(start) != firstDelay || repeat.FailureCount != 99 {
		t.Fatalf("same generation/stage defer must be stable and not change failure count: %#v", repeat)
	}

	later := Outcome{Outcome: OutcomeNeutral, ObservedAt: start.Add(30 * time.Second)}
	applyOutcomeDecision(&later, input, prior, true, "cooldown_retest")
	if later.NextDueAt == nil || later.NextDueAt.Sub(later.ObservedAt) <= firstDelay {
		t.Fatalf("next observation window must increase neutral defer: first=%s later=%#v", firstDelay, later)
	}
	if capped := stableCooldownDefer(input.AccountID, fence.Generation, 100, 30*time.Second, 15*time.Minute); capped > 15*time.Minute || capped < 3*time.Second {
		t.Fatalf("neutral defer must remain within [3s,15m]: %s", capped)
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

func TestActiveHealthFailureWaitsForConfiguredThreshold(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active", FailureCount: 1}
	outcome := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "health")
	if outcome.AccountStatus != "active" || outcome.FailureCount != 2 || outcome.NextDueAt == nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "health_failure" || outcome.CooldownFence != nil {
		t.Fatalf("below threshold health failure must keep active: %#v", outcome)
	}
	prior.FailureCount = 2
	outcome = Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "health")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.FailureCount != 0 || outcome.Projection == nil || outcome.Projection.TransitionKind != "temporary_unavailable" || outcome.CooldownFence == nil || outcome.Projection.Values["health_check_failure_count"] != 3 {
		t.Fatalf("threshold health failure must enter cooldown: %#v", outcome)
	}
}

func TestScheduledAndSourceHealthFailuresBypassGenericThreshold(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}
	for _, result := range []string{OutcomeUpstreamFailed, OutcomeNeutral} {
		outcome := Outcome{Outcome: result, ObservedAt: now}
		applyOutcomeDecision(&outcome, input, prior, true, "scheduled_health")
		if outcome.AccountStatus != "temporary_unavailable" || outcome.CooldownFence == nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "temporary_unavailable" {
			t.Fatalf("scheduled complete diagnostic failure must immediately enter cooldown: %#v", outcome)
		}
	}
	source := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&source, input, prior, true, "source_health")
	if source.AccountStatus != "temporary_unavailable" || source.CooldownFence == nil || source.Projection == nil || source.Projection.TransitionKind != "temporary_unavailable" {
		t.Fatalf("source request confirmation must use threshold one: %#v", source)
	}
}

func TestThresholdCooldownStartsIndependentRetrySequence(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 10, MaxRecoveryHours: 12}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active", FailureCount: 2}
	threshold := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&threshold, input, prior, true, "health")
	if threshold.CooldownFence == nil || threshold.FailureCount != 0 || threshold.NextDueAt == nil || !threshold.NextDueAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("threshold transition must reset cooldown retry count: %#v", threshold)
	}
	firstCooldown := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	prior = CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: threshold.FailureCount, CooldownFence: threshold.CooldownFence}
	applyOutcomeDecision(&firstCooldown, input, prior, true, "cooldown_retest")
	if firstCooldown.FailureCount != 1 || firstCooldown.NextDueAt == nil || !firstCooldown.NextDueAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("first cooldown retry must use initial backoff: %#v", firstCooldown)
	}
	secondCooldown := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	prior.FailureCount = firstCooldown.FailureCount
	applyOutcomeDecision(&secondCooldown, input, prior, true, "cooldown_retest")
	if secondCooldown.FailureCount != 2 || secondCooldown.NextDueAt == nil || !secondCooldown.NextDueAt.Equal(now.Add(6*time.Second)) {
		t.Fatalf("second cooldown retry must double initial backoff: %#v", secondCooldown)
	}
}

func TestThresholdCooldownUsesThreeSecondDefaultBackoff(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(5 * time.Minute / time.Millisecond), CooldownFailureBackoffMS: 0}
	outcome := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{AccountStatus: "active"}, true, "health")
	if outcome.NextDueAt == nil || !outcome.NextDueAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("threshold cooldown default backoff must be 3s: %#v", outcome)
	}
}

func TestCooldownFailureTransitionsLongTermThenTerminal(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "generation-1"}
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, TemporaryUnavailableContinuousProbeEnabled: boolPointer(true)}
	input.Cooldown = fence
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 10, MaxRecoveryHours: 1}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 2, CooldownFence: fence}
	outcome := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.NextDueAt == nil || !outcome.NextDueAt.Equal(now.Add(time.Hour)) || outcome.ErrorCode != "cooldown_retest_long_term_unavailable" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_failure" {
		t.Fatalf("expired max recovery must use hourly long-term retest: %#v", outcome)
	}
	fence.ObservationStartedAt = now.Add(-7 * 24 * time.Hour)
	outcome = Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "error" || outcome.NextDueAt != nil || outcome.ErrorCode != "cooldown_retest_observation_timeout" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_error" || outcome.Projection.ExpectedCooldownFence == nil || outcome.CooldownFence == nil || outcome.Projection.CooldownFence == nil || !sameCooldownFence(outcome.CooldownFence, fence) || !sameCooldownFence(outcome.Projection.CooldownFence, fence) {
		t.Fatalf("seven-day cooldown observation must become projected terminal: %#v", outcome)
	}
}

func TestCooldownFailureUsesDeterministicSlowBackoffAfterFastRetries(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, TemporaryUnavailableContinuousProbeEnabled: boolPointer(true)}
	input.Cooldown = fence
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 1, MaxRecoveryHours: 12}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 10, CooldownFence: fence}
	outcome := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.NextDueAt == nil || outcome.AccountStatus != "temporary_unavailable" || outcome.ErrorCode != "" {
		t.Fatalf("slow recovery outcome invalid: %#v", outcome)
	}
	delay := outcome.NextDueAt.Sub(now)
	if delay < time.Minute || delay > 5*time.Minute || delay != cooldownFailureDelay(input.AccountID, fence.Generation, 3*time.Second, 11) {
		t.Fatalf("slow recovery delay must be stable in [1m,5m]: %s", delay)
	}
}

func TestBoundedTemporaryUnavailableCapsThenTerminatesOnRealFailure(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	fence := &CooldownFence{ObservationStartedAt: now.Add(-cooldownLimitedProbeTimeout + time.Second), Generation: "generation-1"}
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, TemporaryUnavailableContinuousProbeEnabled: boolPointer(false)}
	input.Cooldown = fence
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 10, MaxRecoveryHours: 12}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 1, CooldownFence: fence}
	neutral := Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&neutral, input, prior, true, "cooldown_retest")
	if neutral.NextDueAt == nil || !neutral.NextDueAt.Equal(now.Add(time.Second)) || neutral.AccountStatus != "temporary_unavailable" {
		t.Fatalf("bounded neutral deferral must stop at ten-minute deadline: %#v", neutral)
	}
	beforeDeadline := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&beforeDeadline, input, prior, true, "cooldown_retest")
	if beforeDeadline.NextDueAt == nil || !beforeDeadline.NextDueAt.Equal(now.Add(time.Second)) || beforeDeadline.AccountStatus != "temporary_unavailable" {
		t.Fatalf("bounded retry must stop at ten-minute deadline: %#v", beforeDeadline)
	}
	fence.ObservationStartedAt = now.Add(-cooldownLimitedProbeTimeout)
	atDeadline := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&atDeadline, input, prior, true, "cooldown_retest")
	if atDeadline.AccountStatus != "error" || atDeadline.ErrorCode != "cooldown_retest_limited_probe_timeout" || atDeadline.Projection == nil || atDeadline.Projection.TransitionKind != "cooldown_error" || atDeadline.CooldownFence == nil || atDeadline.Projection.CooldownFence == nil || !sameCooldownFence(atDeadline.CooldownFence, fence) {
		t.Fatalf("bounded real failure at deadline must become limited terminal: %#v", atDeadline)
	}

	input.Eligibility.AccountStatus = "rate_limited"
	input.Schedule.MaxRecoveryHours = 1
	fence.ObservationStartedAt = now.Add(-time.Hour)
	rateLimited := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now}
	prior.AccountStatus = "rate_limited"
	applyOutcomeDecision(&rateLimited, input, prior, true, "cooldown_retest")
	if rateLimited.AccountStatus != "rate_limited" || rateLimited.ErrorCode != "cooldown_retest_long_term_unavailable" {
		t.Fatalf("rate-limited cooldown must not use temporary bounded terminal: %#v", rateLimited)
	}
}

func TestCooldownFailureDelayStartsAtInitialBackoff(t *testing.T) {
	initial := 3 * time.Second
	if got := cooldownFailureDelay("account-1", "generation-1", initial, 1); got != initial {
		t.Fatalf("first cooldown failure delay = %s, want %s", got, initial)
	}
	if got := cooldownFailureDelay("account-1", "generation-1", initial, 2); got != 6*time.Second {
		t.Fatalf("second cooldown failure delay = %s, want 6s", got)
	}
	if got := cooldownFailureDelay("account-1", "generation-1", initial, 6); got < time.Minute || got > 5*time.Minute {
		t.Fatalf("slow cooldown failure delay = %s, want [1m,5m]", got)
	}
}

func TestSourceFenceUpstreamFailurePreservesCooldownState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	nextDue := now.Add(time.Minute)
	failureStarted := now.Add(-2 * time.Minute)
	prior := CurrentState{
		InputVersion:     input.InputVersion,
		ConfigRevision:   input.ConfigRevision,
		DispatchRevision: input.DispatchRevision,
		NextDueAt:        &nextDue,
		FailureCount:     3,
		FailureStartedAt: &failureStarted,
		AccountStatus:    "temporary_unavailable",
		CooldownFence:    fence,
	}
	outcome := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now, SourceFence: &SourceFence{StateKey: "state-1"}}
	applyExplicitRequestDecision(&outcome, input, ProbeRequest{SourceFence: outcome.SourceFence}, prior, true, "")
	if outcome.NextDueAt == nil || !outcome.NextDueAt.Equal(nextDue) || outcome.FailureCount != prior.FailureCount || outcome.FailureStartedAt == nil || !outcome.FailureStartedAt.Equal(failureStarted) || outcome.AccountStatus != prior.AccountStatus || outcome.CooldownFence != fence || outcome.Projection != nil {
		t.Fatalf("source-fenced cooldown state must be preserved: %#v", outcome)
	}
}

func TestSourceFenceUpstreamFailureMutatesActiveState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}
	outcome := Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now, SourceFence: &SourceFence{StateKey: "state-1"}}
	applyExplicitRequestDecision(&outcome, input, ProbeRequest{SourceFence: outcome.SourceFence}, prior, true, "")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.NextDueAt == nil || outcome.CooldownFence == nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "temporary_unavailable" {
		t.Fatalf("source-fenced active upstream failure must mutate health state: %#v", outcome)
	}
}

func TestSourceOnlyMissingStateFailsClosedBeforeProbe(t *testing.T) {
	secret := "source-state-credential-secret"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	input := testInput(server.URL, "chat_json")
	input.APIKeys = []APIKeyInput{{Index: 0, Fingerprint: "key-1", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}}}
	input.KeySetFingerprint = "keyset-1"
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: 3_000}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "state.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "runner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t err=%v", acquired, err)
	}
	runner := NewRunner(Config{CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, Now: time.Now}, store, nil)
	request := ProbeRequest{RequestID: "source-missing-state", AccountID: input.AccountID, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: time.Now().UTC().Add(time.Minute), SourceFence: &SourceFence{StateKey: "source-1"}}
	if err := runner.runExplicitRequest(context.Background(), lease, input, request, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("source-only request without state must not probe upstream, calls=%d", calls)
	}
	state, found, err := store.LoadCurrentState(context.Background(), input.AccountID)
	if err != nil || found || state != (CurrentState{}) {
		t.Fatalf("source-only stale outcome must remain outcome-only: found=%t state=%#v err=%v", found, state, err)
	}
}

func TestExplicitMutationUsesCooldownOrRejectsTerminalState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Cooldown = &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	cooling := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: input.Cooldown}
	kind, allowed := explicitMutationKind(input, cooling, true)
	if !allowed || kind != "cooldown_retest" {
		t.Fatalf("cooling explicit mutation must use cooldown state machine: kind=%q allowed=%t", kind, allowed)
	}
	terminal := cooling
	terminal.AccountStatus = "error"
	kind, allowed = explicitMutationKind(input, terminal, true)
	if allowed || kind != "" {
		t.Fatalf("terminal explicit mutation must be rejected: kind=%q allowed=%t", kind, allowed)
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
