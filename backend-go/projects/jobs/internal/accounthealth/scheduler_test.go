package accounthealth

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
)

type fakeDirectInputLoader struct {
	due      []exactkeyprobe.Input
	failures []DirectInputFailure
	explicit map[string]exactkeyprobe.Input
	loaded   []string
	mu       sync.Mutex
	dueCalls int
}

func (loader *fakeDirectInputLoader) LoadDueWithFailures(_ context.Context, _ int) (DirectInputLoadResult, error) {
	loader.mu.Lock()
	loader.dueCalls++
	loader.mu.Unlock()
	return DirectInputLoadResult{Inputs: loader.due, Failures: loader.failures}, nil
}

func (loader *fakeDirectInputLoader) LoadDue(_ context.Context, _ int) ([]exactkeyprobe.Input, error) {
	loader.mu.Lock()
	loader.dueCalls++
	loader.mu.Unlock()
	return loader.due, nil
}

func (loader *fakeDirectInputLoader) DueCalls() int {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	return loader.dueCalls
}

func (loader *fakeDirectInputLoader) LoadAccount(_ context.Context, accountID string) ([]exactkeyprobe.Input, error) {
	loader.loaded = append(loader.loaded, accountID)
	input, found := loader.explicit[accountID]
	if !found {
		return nil, nil
	}
	return []exactkeyprobe.Input{input}, nil
}

func TestInvalidInputRequestIDIsStableAcrossObservedTimes(t *testing.T) {
	input := exactkeyprobe.Input{AccountID: "account-invalid", InputVersion: 7, ConfigRevision: 11, DispatchRevision: 13}
	first := invalidInputRequestID(input)
	second := invalidInputRequestID(input)
	if first == "" || first != second {
		t.Fatalf("invalid input request ID must be stable: first=%q second=%q", first, second)
	}
	if first == scheduledRequestID(input, "invalid_input", time.Now().UTC()) {
		t.Fatal("invalid input request ID must not depend on the observed clock")
	}
}

func testScheduledAPIKeyInput(t *testing.T, baseURL, secret, accountID string) exactkeyprobe.Input {
	t.Helper()
	input := testInput(baseURL, "chat_json")
	input.AccountID = accountID
	input.IssuedAt = time.Now().UTC().Add(-time.Minute)
	input.ExpiresAt = time.Now().UTC().Add(time.Hour)
	input.APIKeys = []exactkeyprobe.APIKeyInput{{Index: 0, Fingerprint: "key-1", Credential: exactkeyprobe.CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}}}
	input.KeySetFingerprint = "keyset-" + accountID
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	return input
}

func TestScheduleMillisecondsAreBoundedWithoutDurationOverflow(t *testing.T) {
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	input := exactkeyprobe.Input{
		AccountID: "bounded", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Eligibility: exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true},
		Schedule:    exactkeyprobe.Schedule{HealthIntervalMS: maxScheduleMilliseconds + 1, FailureThreshold: 1, FailureRetryMS: 3_000},
	}
	if err := validateScheduledInput(input, now); err == nil {
		t.Fatal("schedule duration over the configured upper bound must be rejected")
	}
	if got := durationMS(1<<63-1, time.Second); got != maxScheduleDuration {
		t.Fatalf("durationMS overflow guard=%s, want %s", got, maxScheduleDuration)
	}
	if got := cooldownDefer(1000, maxScheduleDuration, maxScheduleDuration); got < 0 || got > maxScheduleDuration {
		t.Fatalf("cooldown defer overflowed: %s", got)
	}
}

func TestDirectInputFailureRequestIDIsStableAcrossRetryRounds(t *testing.T) {
	failure := DirectInputFailure{AccountID: "invalid-direct-candidate", InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6}
	first := time.Date(2030, 8, 16, 12, 0, 0, 0, time.UTC)
	second := first.Add(2 * directInputFailureRetryBackoff)
	if got, want := directInputFailureRequestID(failure, first), directInputFailureRequestID(failure, second); got != want {
		t.Fatalf("invalid input request ID must be stable across retry rounds: first=%q second=%q", got, want)
	}
}

func TestApplyCooldownNeutralKeepsTemporaryUnavailableAndDefers(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{AccountStatus: "temporary_unavailable", FailureCount: 2, CooldownFence: fence}, true, "cooldown_retest")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.NextDueAt == nil || !outcome.NextDueAt.After(now) || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" || outcome.CooldownFence == nil || outcome.CooldownFence.Generation != fence.Generation || outcome.Projection.ExpectedAccountStatus != "temporary_unavailable" || outcome.Projection.ExpectedCooldownFence == nil || outcome.Projection.ExpectedCooldownFence.Generation != fence.Generation {
		t.Fatalf("unexpected cooldown defer: %#v", outcome)
	}
}

func TestCooldownNeutralTerminalizesAfterSevenDays(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownObservationTimeout), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, StatusCode: 403, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "error" || outcome.NextDueAt != nil || outcome.ErrorCode != "cooldown_retest_neutral_timeout" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_error" || outcome.CooldownFence == nil || !sameCooldownFence(outcome.CooldownFence, fence) || !sameCooldownFence(outcome.Projection.CooldownFence, fence) {
		t.Fatalf("seven-day neutral observation must become projected terminal: %#v", outcome)
	}
	if !strings.Contains(outcome.ErrorMessage, "HTTP 403") || !strings.Contains(outcome.ErrorMessage, "7 天") {
		t.Fatalf("neutral terminal message must carry observation window and last status: %q", outcome.ErrorMessage)
	}
}

func TestCooldownNeutralBeforeSevenDaysStillDefers(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownObservationTimeout + time.Minute), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, StatusCode: 403, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.NextDueAt == nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" || outcome.ErrorCode != "" {
		t.Fatalf("neutral before the seven-day window must keep deferring: %#v", outcome)
	}
}

func TestCooldownTaskFailureNeverTerminalizes(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownObservationTimeout - time.Hour), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeTaskFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus == "error" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
		t.Fatalf("local task failure must not terminalize an account: %#v", outcome)
	}
}

func TestCooldownNeutralTerminalizesRateLimitedAfterSevenDays(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownObservationTimeout), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "rate_limited", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "rate_limited", CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, StatusCode: 429, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "error" || outcome.ErrorCode != "cooldown_retest_neutral_timeout" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_error" {
		t.Fatalf("seven-day neutral rate-limited observation must become projected terminal: %#v", outcome)
	}
}

func TestCooldownRetestUsesInputFenceWhenCurrentStateFenceChanged(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "current-generation"}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-2 * time.Minute), Generation: "stale-generation"}}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: now}
	applyCooldownDecision(&outcome, input, prior, true, "temporary_unavailable", now)
	if outcome.Projection == nil || !sameCooldownFence(outcome.Projection.ExpectedCooldownFence, input.Cooldown) {
		t.Fatalf("cooldown retest must use the current input fence: projection=%#v", outcome.Projection)
	}
}

func TestNextDueUsesFreshReconciliationEpochWhenCurrentStateFenceChanged(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "current-generation"}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond)}
	state := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", NextDueAt: &cooldownUntil, CooldownFence: &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-2 * time.Minute), Generation: "stale-generation"}}
	kind, due, ok := nextDue(input, state, true, now)
	if !ok || kind != "cooldown_retest" || !due.Equal(now) {
		t.Fatalf("nextDue must create a fresh reconciliation request: kind=%q due=%s ok=%t", kind, due, ok)
	}
	if scheduledRequestID(input, kind, due) == scheduledRequestID(input, kind, cooldownUntil) {
		t.Fatal("reconciliation request must not reuse the already-settled business cooldown request ID")
	}
}

func TestNextDueBusinessCooldownWinsOverActiveJobsState(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	cooldownUntil := now.Add(-time.Second)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "business-generation"}
	input := testInput("https://api.example.com", "chat_json")
	input.InputVersion = 1
	input.ConfigRevision = 5
	input.DispatchRevision = 7
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000}
	state := CurrentState{InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, AccountStatus: "active", NextDueAt: ptrTime(now.Add(time.Hour))}

	kind, due, ok := nextDue(input, state, true, now)
	if !ok || kind != "cooldown_retest" || !due.Equal(now) {
		t.Fatalf("business cooldown must override stale active jobs state: kind=%q due=%s ok=%t", kind, due, ok)
	}

	outcome := Outcome{Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyOutcomeDecision(&outcome, input, state, true, kind)
	if outcome.AccountStatus != "active" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_success" || outcome.Projection.ExpectedAccountStatus != "temporary_unavailable" || outcome.Projection.ExpectedCooldownFence == nil || !sameCooldownFence(outcome.Projection.ExpectedCooldownFence, fence) {
		t.Fatalf("cooldown recovery must carry the business fence: %#v", outcome)
	}
}

func TestNextDueBusinessCooldownWaitsUntilFutureDue(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	cooldownUntil := now.Add(time.Minute)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "business-generation"}
	input := testInput("https://api.example.com", "chat_json")
	input.InputVersion = 1
	input.ConfigRevision = 5
	input.DispatchRevision = 7
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	state := CurrentState{InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, AccountStatus: "active", NextDueAt: ptrTime(now.Add(time.Hour))}

	kind, due, ok := nextDue(input, state, true, now)
	if !ok || kind != "cooldown_retest" || !due.Equal(cooldownUntil) {
		t.Fatalf("business cooldown must retain an unexpired due time: kind=%q due=%s ok=%t", kind, due, ok)
	}
}

func TestNextDueBusinessActiveReconcilesStaleCooldownState(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	input := testInput("https://api.example.com", "chat_json")
	input.InputVersion = 1
	input.ConfigRevision = 5
	input.DispatchRevision = 7
	input.IssuedAt = now
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureRetryMS: int64(time.Minute / time.Millisecond)}
	state := CurrentState{InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, AccountStatus: "temporary_unavailable", NextDueAt: ptrTime(now.Add(time.Hour))}

	kind, due, ok := nextDue(input, state, true, now)
	if !ok || kind != "health" || !due.Equal(now) {
		t.Fatalf("business active must reconcile stale jobs cooldown state: kind=%q due=%s ok=%t", kind, due, ok)
	}

	outcome := Outcome{Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyOutcomeDecision(&outcome, input, state, true, kind)
	if outcome.AccountStatus != "active" || outcome.Projection == nil || outcome.Projection.TransitionKind != "health_success" || outcome.Projection.ExpectedAccountStatus != "active" {
		t.Fatalf("stale cooldown state must produce a normal active health projection: %#v", outcome)
	}
}

func TestCooldownNeutralDeferUsesObservationGenerationWindow(t *testing.T) {
	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	input := testInput("https://api.example.com", "chat_json")
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: start, Generation: "neutral-generation"}
	cooldownUntil := start
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: fence}

	first := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: start}
	applyOutcomeDecision(&first, input, prior, true, "cooldown_retest")
	if first.NextDueAt == nil {
		t.Fatal("first neutral defer missing due time")
	}
	firstDelay := first.NextDueAt.Sub(start)
	prior.FailureCount = 99
	repeat := Outcome{Outcome: exactkeyprobe.OutcomeTaskFailed, ObservedAt: start}
	applyOutcomeDecision(&repeat, input, prior, true, "cooldown_retest")
	if repeat.NextDueAt == nil || repeat.NextDueAt.Sub(start) <= 0 || repeat.FailureCount != 99 {
		t.Fatalf("same generation/stage defer must remain positive and not change failure count: %#v", repeat)
	}

	later := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: start.Add(30 * time.Second)}
	applyOutcomeDecision(&later, input, prior, true, "cooldown_retest")
	if later.NextDueAt == nil || later.NextDueAt.Sub(later.ObservedAt) <= 0 {
		t.Fatalf("next observation window must retain a positive neutral defer: first=%s later=%#v", firstDelay, later)
	}
	if capped := cooldownDefer(100, 30*time.Second, 15*time.Minute); capped > 15*time.Minute || capped < 3*time.Second {
		t.Fatalf("neutral defer must remain within [3s,15m]: %s", capped)
	}
}

func TestApplyHealthTransitionCarriesExpectedAccountStatus(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "pending_test", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyOutcomeDecision(&outcome, input, CurrentState{}, false, "health")
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "activation_success" || outcome.Projection.ExpectedAccountStatus != "pending_test" || outcome.Projection.ExpectedCooldownFence != nil {
		t.Fatalf("health projection must carry exact expected status: %#v", outcome.Projection)
	}
}

// TestExplicitActivationProbeOnActiveAccountClassifiedAsActivation：直接以
// active 落地创建的单 Key 账户，创建事务以 reason="activation" 派发首探；该
// 显式请求成功必须分类为 activation_success（余额探测意图的唯一写入口，
// 《AI账户上游余额查询设计》§5）。周期健康检查与其他 reason 的显式请求保持
// health_success——旧账户与普通编辑不创建新意图。
func TestExplicitActivationProbeOnActiveAccountClassifiedAsActivation(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}

	explicit := Outcome{Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyExplicitRequestDecision(&explicit, input, ProbeRequest{Reason: "activation", MutateAccount: true}, prior, true, "health")
	if explicit.Projection == nil || explicit.Projection.TransitionKind != "activation_success" || explicit.Projection.ExpectedAccountStatus != "active" {
		t.Fatalf("显式激活首探成功必须分类为 activation_success: %#v", explicit.Projection)
	}

	scheduled := Outcome{Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyOutcomeDecision(&scheduled, input, prior, true, "scheduled_health")
	if scheduled.Projection == nil || scheduled.Projection.TransitionKind != "health_success" {
		t.Fatalf("周期健康成功必须保持 health_success: %#v", scheduled.Projection)
	}

	configuration := Outcome{Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyExplicitRequestDecision(&configuration, input, ProbeRequest{Reason: "configuration", MutateAccount: true}, prior, true, "health")
	if configuration.Projection == nil || configuration.Projection.TransitionKind != "health_success" {
		t.Fatalf("配置探活成功必须保持 health_success: %#v", configuration.Projection)
	}
}

func TestActiveHealthFailureWaitsForConfiguredThreshold(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active", FailureCount: 1}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "health")
	if outcome.AccountStatus != "active" || outcome.FailureCount != 2 || outcome.NextDueAt == nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "health_failure" || outcome.CooldownFence != nil {
		t.Fatalf("below threshold health failure must keep active: %#v", outcome)
	}
	prior.FailureCount = 2
	outcome = Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "health")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.FailureCount != 0 || outcome.Projection == nil || outcome.Projection.TransitionKind != "temporary_unavailable" || outcome.CooldownFence == nil || outcome.Projection.Values["health_check_failure_count"] != 3 {
		t.Fatalf("threshold health failure must enter cooldown: %#v", outcome)
	}
}

func TestScheduledAndSourceHealthFailuresBypassGenericThreshold(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}
	for _, result := range []string{exactkeyprobe.OutcomeUpstreamFailed, exactkeyprobe.OutcomeNeutral} {
		outcome := Outcome{Outcome: result, ObservedAt: now}
		applyOutcomeDecision(&outcome, input, prior, true, "scheduled_health")
		if outcome.AccountStatus != "temporary_unavailable" || outcome.CooldownFence == nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "temporary_unavailable" {
			t.Fatalf("scheduled complete diagnostic failure must immediately enter cooldown: %#v", outcome)
		}
	}
	source := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&source, input, prior, true, "source_health")
	if source.AccountStatus != "temporary_unavailable" || source.CooldownFence == nil || source.Projection == nil || source.Projection.TransitionKind != "temporary_unavailable" {
		t.Fatalf("source request confirmation must use threshold one: %#v", source)
	}
}

func TestRequestFailureHealthConfirmationBypassesGenericThreshold(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}
	confirmed := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyExplicitRequestDecision(&confirmed, input, ProbeRequest{Reason: "request_failure", MutateAccount: true}, prior, true, "health")
	if confirmed.AccountStatus != "temporary_unavailable" || confirmed.CooldownFence == nil || confirmed.Projection == nil || confirmed.Projection.TransitionKind != "temporary_unavailable" {
		t.Fatalf("request failure confirmation must enter cooldown at threshold one: %#v", confirmed)
	}
	generic := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyExplicitRequestDecision(&generic, input, ProbeRequest{Reason: "configuration_changed", MutateAccount: true}, prior, true, "health")
	if generic.AccountStatus != "active" || generic.Projection == nil || generic.Projection.TransitionKind != "health_failure" {
		t.Fatalf("non-request explicit health failure must retain generic threshold: %#v", generic)
	}
}

func TestInputRevisionMismatchCannotReuseFailureCounters(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond)}
	for _, prior := range []CurrentState{
		{InputVersion: input.InputVersion - 1, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active", FailureCount: 99},
		{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision + 1, DispatchRevision: input.DispatchRevision, AccountStatus: "active", FailureCount: 99},
		{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision + 1, AccountStatus: "active", FailureCount: 99},
	} {
		outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
		applyOutcomeDecision(&outcome, input, prior, true, "health")
		if outcome.AccountStatus != "active" || outcome.FailureCount != 1 || outcome.Projection == nil || outcome.Projection.TransitionKind != "health_failure" {
			t.Fatalf("revision-mismatched current state must start a clean health window: prior=%#v outcome=%#v", prior, outcome)
		}
	}
}

func TestThresholdCooldownStartsIndependentRetrySequence(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 10, MaxRecoveryHours: 12}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active", FailureCount: 2}
	threshold := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&threshold, input, prior, true, "health")
	if threshold.CooldownFence == nil || threshold.FailureCount != 0 || threshold.NextDueAt == nil || !isJitteredWithin(threshold.NextDueAt.Sub(now), 3*time.Second) {
		t.Fatalf("threshold transition must reset cooldown retry count: %#v", threshold)
	}
	firstCooldown := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	prior = CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: threshold.FailureCount, CooldownFence: threshold.CooldownFence}
	applyOutcomeDecision(&firstCooldown, input, prior, true, "cooldown_retest")
	if firstCooldown.FailureCount != 1 || firstCooldown.NextDueAt == nil || !isJitteredWithin(firstCooldown.NextDueAt.Sub(now), 3*time.Second) {
		t.Fatalf("first cooldown retry must use initial backoff: %#v", firstCooldown)
	}
	secondCooldown := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	prior.FailureCount = firstCooldown.FailureCount
	applyOutcomeDecision(&secondCooldown, input, prior, true, "cooldown_retest")
	if secondCooldown.FailureCount != 2 || secondCooldown.NextDueAt == nil || !isJitteredWithin(secondCooldown.NextDueAt.Sub(now), 6*time.Second) {
		t.Fatalf("second cooldown retry must double initial backoff: %#v", secondCooldown)
	}
}

func TestThresholdCooldownUsesThreeSecondDefaultBackoff(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(5 * time.Minute / time.Millisecond), CooldownFailureBackoffMS: 0}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{AccountStatus: "active"}, true, "health")
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), 3*time.Second) {
		t.Fatalf("threshold cooldown default backoff must be 3s: %#v", outcome)
	}
}

func TestCooldownFailureTransitionsLongTermThenTerminal(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, TemporaryUnavailableContinuousProbeEnabled: boolPointer(true)}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 10, MaxRecoveryHours: 1}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 2, CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), time.Hour) || outcome.ErrorCode != "cooldown_retest_long_term_unavailable" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_failure" {
		t.Fatalf("expired max recovery must use hourly long-term retest: %#v", outcome)
	}
	fence.ObservationStartedAt = now.Add(-7 * 24 * time.Hour)
	outcome = Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "error" || outcome.NextDueAt != nil || outcome.ErrorCode != "cooldown_retest_observation_timeout" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_error" || outcome.Projection.ExpectedCooldownFence == nil || outcome.CooldownFence == nil || outcome.Projection.CooldownFence == nil || !sameCooldownFence(outcome.CooldownFence, fence) || !sameCooldownFence(outcome.Projection.CooldownFence, fence) {
		t.Fatalf("seven-day cooldown observation must become projected terminal: %#v", outcome)
	}
}

func TestCooldownFailureUsesDeterministicSlowBackoffAfterFastRetries(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, TemporaryUnavailableContinuousProbeEnabled: boolPointer(true)}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 1, MaxRecoveryHours: 12}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 10, CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.NextDueAt == nil || outcome.AccountStatus != "temporary_unavailable" || outcome.ErrorCode != "" {
		t.Fatalf("slow recovery outcome invalid: %#v", outcome)
	}
	delay := outcome.NextDueAt.Sub(now)
	if delay < 30*time.Second || delay > 5*time.Minute+30*time.Second || delay == cooldownFailureDelay(input.AccountID, fence.Generation, 3*time.Second, 11, false) {
		t.Fatalf("slow recovery delay must apply a fresh global offset in [30s,5m30s]: %s", delay)
	}
}

func TestBoundedTemporaryUnavailableCapsThenTerminatesOnRealFailure(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownLimitedProbeTimeout + time.Second), Generation: "generation-1"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, TemporaryUnavailableContinuousProbeEnabled: boolPointer(false)}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 10, MaxRecoveryHours: 12}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 1, CooldownFence: fence}
	neutral := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&neutral, input, prior, true, "cooldown_retest")
	if neutral.NextDueAt == nil || neutral.NextDueAt.Sub(now) < 3*time.Second || neutral.AccountStatus != "temporary_unavailable" {
		t.Fatalf("bounded neutral deferral must retain the 3s minimum without terminalizing: %#v", neutral)
	}
	beforeDeadline := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&beforeDeadline, input, prior, true, "cooldown_retest")
	if beforeDeadline.NextDueAt == nil || beforeDeadline.NextDueAt.Sub(now) <= 0 || !beforeDeadline.NextDueAt.Before(now.Add(time.Second)) || beforeDeadline.AccountStatus != "temporary_unavailable" {
		t.Fatalf("bounded retry must stop at ten-minute deadline: %#v", beforeDeadline)
	}
	fence.ObservationStartedAt = now.Add(-cooldownLimitedProbeTimeout)
	atDeadlineTaskFailed := Outcome{Outcome: exactkeyprobe.OutcomeTaskFailed, ObservedAt: now}
	applyOutcomeDecision(&atDeadlineTaskFailed, input, prior, true, "cooldown_retest")
	if atDeadlineTaskFailed.NextDueAt == nil || atDeadlineTaskFailed.NextDueAt.Sub(now) < 3*time.Second || atDeadlineTaskFailed.AccountStatus != "temporary_unavailable" {
		t.Fatalf("bounded task failure must retain the 3s minimum without terminalizing: %#v", atDeadlineTaskFailed)
	}
	atDeadline := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&atDeadline, input, prior, true, "cooldown_retest")
	if atDeadline.AccountStatus != "error" || atDeadline.ErrorCode != "cooldown_retest_limited_probe_timeout" || atDeadline.Projection == nil || atDeadline.Projection.TransitionKind != "cooldown_error" || atDeadline.CooldownFence == nil || atDeadline.Projection.CooldownFence == nil || !sameCooldownFence(atDeadline.CooldownFence, fence) {
		t.Fatalf("bounded real failure at deadline must become limited terminal: %#v", atDeadline)
	}

	input.Eligibility.AccountStatus = "rate_limited"
	input.Schedule.MaxRecoveryHours = 1
	fence.ObservationStartedAt = now.Add(-time.Hour)
	rateLimited := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	prior.AccountStatus = "rate_limited"
	applyOutcomeDecision(&rateLimited, input, prior, true, "cooldown_retest")
	if rateLimited.AccountStatus != "rate_limited" || rateLimited.ErrorCode != "cooldown_retest_long_term_unavailable" {
		t.Fatalf("rate-limited cooldown must not use temporary bounded terminal: %#v", rateLimited)
	}
}

func isJitteredWithin(delay, interval time.Duration) bool {
	window := schedulejitter.Window(interval)
	return delay >= interval-window && delay <= interval+window && delay != interval
}

func TestCooldownFailureDelayStartsAtInitialBackoff(t *testing.T) {
	initial := 3 * time.Second
	if got := cooldownFailureDelay("account-1", "generation-1", initial, 1, false); got != initial {
		t.Fatalf("first cooldown failure delay = %s, want %s", got, initial)
	}
	if got := cooldownFailureDelay("account-1", "generation-1", initial, 2, false); got != 6*time.Second {
		t.Fatalf("second cooldown failure delay = %s, want 6s", got)
	}
	if got := cooldownFailureDelay("account-1", "generation-1", initial, 6, false); got < time.Minute || got > 5*time.Minute {
		t.Fatalf("slow cooldown failure delay = %s, want [1m,5m]", got)
	}
}

func TestSourceFenceUpstreamFailurePreservesCooldownState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
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
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now, SourceFence: &SourceFence{StateKey: "state-1"}}
	applyExplicitRequestDecision(&outcome, input, ProbeRequest{SourceFence: outcome.SourceFence}, prior, true, "")
	if outcome.NextDueAt == nil || !outcome.NextDueAt.Equal(nextDue) || outcome.FailureCount != prior.FailureCount || outcome.FailureStartedAt == nil || !outcome.FailureStartedAt.Equal(failureStarted) || outcome.AccountStatus != prior.AccountStatus || outcome.CooldownFence != fence || outcome.Projection != nil {
		t.Fatalf("source-fenced cooldown state must be preserved: %#v", outcome)
	}
}

func TestSourceFenceUpstreamFailureMutatesActiveState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now, SourceFence: &SourceFence{StateKey: "state-1"}}
	applyExplicitRequestDecision(&outcome, input, ProbeRequest{SourceFence: outcome.SourceFence}, prior, true, "")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.NextDueAt == nil || outcome.CooldownFence == nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "temporary_unavailable" {
		t.Fatalf("source-fenced active upstream failure must mutate health state: %#v", outcome)
	}
}

func TestExplicitMutationUsesCooldownOrRejectsTerminalState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Cooldown = &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
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
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "pending_test", Schedulable: false, BoundGroup: true, AuthorizationEligible: true}
	if !inputEligible(input) {
		t.Fatal("pending_test must remain probeable before activation sets schedulable")
	}
}

func TestCooldownInputEligibleWhenLegacySchedulableBitIsFalse(t *testing.T) {
	for _, status := range []string{"temporary_unavailable", "rate_limited"} {
		input := testInput("https://api.example.com", "chat_json")
		input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: status, Schedulable: false, BoundGroup: true, AuthorizationEligible: true}
		if !inputEligible(input) {
			t.Fatalf("%s cooldown input must remain probeable when legacy schedulable=false", status)
		}
	}
}

func TestCooldownNextDueRecoversFromDirectInputFailureState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Minute)
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: false, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "repaired-generation"}
	retryAt := now.Add(-time.Second)
	state := CurrentState{
		InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision,
		NextDueAt: &retryAt, ErrorCode: "direct_input_invalid",
	}

	kind, due, ok := nextDue(input, state, true, now)
	if !ok || kind != "cooldown_retest" || !due.Equal(retryAt) {
		t.Fatalf("repaired cooldown candidate must resume as cooldown retest: kind=%q due=%s ok=%t", kind, due, ok)
	}
}

func TestPendingTestTimeoutCarriesFrozenTerminalError(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "pending_test", Schedulable: false, BoundGroup: true, AuthorizationEligible: true}
	started := now.Add(-24 * time.Hour)
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "pending_test", FailureCount: 2, FailureStartedAt: &started}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now, ErrorCode: "upstream_error", ErrorMessage: "upstream unavailable"}
	applyOutcomeDecision(&outcome, input, prior, true, "scheduled_health")
	if outcome.AccountStatus != "error" || outcome.ErrorCode != "account_activation_check_timeout" || outcome.ErrorMessage == "" || outcome.NextDueAt != nil || outcome.Projection == nil || outcome.Projection.TransitionKind != "activation_error" {
		t.Fatalf("pending timeout must be a terminal, explicit activation error: %#v", outcome)
	}
}

func TestNextDueBootstrapsSignedCooldownState(t *testing.T) {
	now := time.Now().UTC()
	input := testInput("https://api.example.com", "chat_json")
	cooldownUntil := now.Add(-time.Second)
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "generation-1"}
	kind, due, ok := nextDue(input, CurrentState{}, false, now)
	if !ok || kind != "cooldown_retest" || !due.Equal(cooldownUntil) {
		t.Fatalf("unexpected bootstrap cooldown due: kind=%q due=%s ok=%t", kind, due, ok)
	}
}
