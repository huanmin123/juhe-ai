package accounthealth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// w12d_scheduler_units_test.go 补齐 w12d 波次调度决策的纯函数分支与
// applyOutcomeDecision / applyCooldownDecision / nextDue 的臂覆盖。

func w12dSchedule() Schedule {
	return Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 3, FailureRetryMS: int64(5 * time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 60, MaxRecoveryHours: 24}
}

// TestW12dValidateScheduledInputArms 覆盖 scheduled input 校验拒绝分支。
func TestW12dValidateScheduledInputArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	base := testScheduledAPIKeyInput(t, "http://127.0.0.1:1", "w12d-secret", "w12d-val")
	base.IssuedAt = now.Add(-time.Minute)
	base.ExpiresAt = now.Add(time.Hour)
	if err := validateScheduledInput(base, now); err != nil {
		t.Fatalf("valid: %v", err)
	}
	blank := base
	blank.AccountID = " "
	if err := validateScheduledInput(blank, now); err == nil || err.Error() != "input version 或账户 fence 无效" {
		t.Fatalf("blank account: %v", err)
	}
	zeroVersion := base
	zeroVersion.ConfigRevision = 0
	if err := validateScheduledInput(zeroVersion, now); err == nil {
		t.Fatal("zero revision must fail")
	}
	expired := base
	expired.ExpiresAt = now.Add(-time.Second)
	if err := validateScheduledInput(expired, now); err == nil || err.Error() != "input 已过期或缺少时间 fence" {
		t.Fatalf("expired: %v", err)
	}
	badSchedule := base
	badSchedule.Schedule.FailureThreshold = 0
	if err := validateScheduledInput(badSchedule, now); err == nil || err.Error() != "input schedule 无效" {
		t.Fatalf("bad schedule: %v", err)
	}
	jitterAboveInterval := base
	jitterAboveInterval.Schedule.HealthJitterMS = jitterAboveInterval.Schedule.HealthIntervalMS + 1
	if err := validateScheduledInput(jitterAboveInterval, now); err == nil {
		t.Fatal("jitter above interval must fail")
	}
	unbound := base
	unbound.Eligibility.BoundGroup = false
	if err := validateScheduledInput(unbound, now); err == nil || err.Error() != "input eligibility 缺少绑定或授权证据" {
		t.Fatalf("unbound: %v", err)
	}
	unknownStatus := base
	unknownStatus.Eligibility.AccountStatus = "error"
	if err := validateScheduledInput(unknownStatus, now); err == nil || err.Error() != "input account status 不可调度" {
		t.Fatalf("unknown status: %v", err)
	}
	cooldownMissingUntil := base
	cooldownMissingUntil.Eligibility.AccountStatus = "temporary_unavailable"
	if err := validateScheduledInput(cooldownMissingUntil, now); err == nil || err.Error() != "cooldown input 缺少 cooldown_until" {
		t.Fatalf("cooldown missing until: %v", err)
	}
	cooldownMissingFence := base
	cooldownMissingFence.Eligibility.AccountStatus = "rate_limited"
	cooldownMissingFence.Eligibility.CooldownUntil = ptrTime(now.Add(time.Hour))
	if err := validateScheduledInput(cooldownMissingFence, now); err == nil || err.Error() != "cooldown input 缺少或不匹配五元 fence" {
		t.Fatalf("cooldown missing fence: %v", err)
	}
	cooldownValid := base
	cooldownValid.Eligibility.AccountStatus = "temporary_unavailable"
	cooldownValid.Eligibility.CooldownUntil = ptrTime(now.Add(time.Hour))
	cooldownValid.Cooldown = &CooldownFence{ObservationStartedAt: now, Generation: "gen-w12d"}
	if err := validateScheduledInput(cooldownValid, now); err != nil {
		t.Fatalf("cooldown valid: %v", err)
	}
}

// TestW12dNextDueArms 覆盖 nextDue 的调度分类分支。
func TestW12dNextDueArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	input := testScheduledAPIKeyInput(t, "http://127.0.0.1:1", "w12d-secret", "w12d-next")
	input.IssuedAt = now.Add(-time.Minute)
	input.ExpiresAt = now.Add(time.Hour)
	input.Schedule = w12dSchedule()
	// 无状态 → health。
	kind, due, ok := nextDue(input, CurrentState{}, false, now)
	if !ok || kind != "health" || due != input.IssuedAt {
		t.Fatalf("no state: kind=%s due=%s ok=%t", kind, due, ok)
	}
	// 无状态 + cooldown 状态缺 fence → 拒绝。
	cooling := input
	cooling.Eligibility.AccountStatus = "temporary_unavailable"
	if _, _, ok := nextDue(cooling, CurrentState{}, false, now); ok {
		t.Fatal("cooldown without fence must not schedule")
	}
	// 状态版本过期 + cooldown 状态带 fence → cooldown_retest。
	cooling.Cooldown = &CooldownFence{ObservationStartedAt: now, Generation: "gen"}
	cooling.Eligibility.CooldownUntil = ptrTime(now.Add(time.Hour))
	kind, due, ok = nextDue(cooling, CurrentState{}, false, now)
	if !ok || kind != "cooldown_retest" || due != *cooling.Eligibility.CooldownUntil {
		t.Fatalf("cooldown no state: kind=%s due=%s ok=%t", kind, due, ok)
	}
	// 隔离恢复：空状态 + 有 retry due。
	quarantineState := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, NextDueAt: ptrTime(now.Add(time.Minute))}
	kind, due, ok = nextDue(cooling, quarantineState, true, now)
	if !ok || kind != "cooldown_retest" || due != now.Add(time.Minute) {
		t.Fatalf("quarantine resume: kind=%s due=%s ok=%t", kind, due, ok)
	}
	// split-brain：jobs 状态 active、业务行仍冷却 → reconciliation。
	stale := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}
	kind, _, ok = nextDue(cooling, stale, true, now)
	if !ok || kind != "cooldown_retest" {
		t.Fatalf("split-brain: kind=%s ok=%t", kind, ok)
	}
	// 业务行恢复 active、jobs 状态残留 error → 一次普通 health。
	recovered := input
	state := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "error"}
	kind, _, ok = nextDue(recovered, state, true, now)
	if !ok || kind != "health" {
		t.Fatalf("recovered: kind=%s ok=%t", kind, ok)
	}
	// 状态无 due → health(now)。
	state.AccountStatus = ""
	kind, due, ok = nextDue(recovered, state, true, now)
	if !ok || kind != "health" || due != now {
		t.Fatalf("no due: kind=%s due=%s ok=%t", kind, due, ok)
	}
	// 业务行为 pending_test、jobs 状态 error 且有 due → 不调度。
	recovered.Eligibility.AccountStatus = "pending_test"
	state.AccountStatus = "error"
	state.NextDueAt = ptrTime(now.Add(time.Minute))
	if _, _, ok := nextDue(recovered, state, true, now); ok {
		t.Fatal("error status must not schedule")
	}
	state.NextDueAt = nil
	// cooling 状态 fence 改变且冷却未过期 → reconciliation due。
	state.AccountStatus = "temporary_unavailable"
	state.CooldownFence = &CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "gen-old"}
	state.NextDueAt = ptrTime(now.Add(-time.Minute))
	cooling.Cooldown = &CooldownFence{ObservationStartedAt: now, Generation: "gen-new"}
	cooling.Eligibility.CooldownUntil = ptrTime(now.Add(-time.Second))
	kind, due, ok = nextDue(cooling, state, true, now)
	if !ok || kind != "cooldown_retest" {
		t.Fatalf("fence change: kind=%s ok=%t", kind, ok)
	}
	if due.Before(now) {
		t.Fatalf("reconciliation due must not stay in the past: %s", due)
	}
	// 相同 fence 的冷却 → 沿用 state due。
	state.CooldownFence = cooling.Cooldown
	kind, due, ok = nextDue(cooling, state, true, now)
	if !ok || kind != "cooldown_retest" || due != *state.NextDueAt {
		t.Fatalf("same fence: kind=%s due=%s ok=%t", kind, due, ok)
	}
	// 冷却 fence 无效 → 拒绝。
	cooling.Cooldown = nil
	if _, _, ok := nextDue(cooling, state, true, now); ok {
		t.Fatal("invalid fence must not schedule")
	}
}

// TestW12dApplyCooldownDecisionArms 覆盖冷却决策的失败/无效臂。
func TestW12dApplyCooldownDecisionArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	input := testScheduledAPIKeyInput(t, "http://127.0.0.1:1", "w12d-secret", "w12d-cool")
	input.IssuedAt = now.Add(-time.Minute)
	input.ExpiresAt = now.Add(time.Hour)
	input.Schedule = w12dSchedule()
	// (a) fence 无效 → TaskFailed。
	outcome := Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyCooldownDecision(&outcome, input, CurrentState{}, false, "temporary_unavailable", now)
	if outcome.Outcome != OutcomeTaskFailed || outcome.ErrorCode != "cooldown_fence_invalid" {
		t.Fatalf("invalid fence: %+v", outcome)
	}
	// (b) neutral → cooldown_defer 投影。
	input.Cooldown = &CooldownFence{ObservationStartedAt: now, Generation: "gen-w12d-defer"}
	outcome = Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyCooldownDecision(&outcome, input, CurrentState{}, false, "temporary_unavailable", now)
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" || outcome.CooldownFence == nil {
		t.Fatalf("defer: %+v", outcome)
	}
	// (c) upstream failed → cooldown_failure 投影 + 指数退避。
	outcome = Outcome{Outcome: OutcomeUpstreamFailed, ObservedAt: now, StatusCode: 500}
	applyCooldownDecision(&outcome, input, CurrentState{}, false, "temporary_unavailable", now)
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_failure" || outcome.FailureCount != 1 {
		t.Fatalf("failure: %+v", outcome)
	}
	// (d) success → cooldown_success + active。
	outcome = Outcome{Outcome: OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyCooldownDecision(&outcome, input, CurrentState{}, false, "temporary_unavailable", now)
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_success" || outcome.AccountStatus != "active" {
		t.Fatalf("success: %+v", outcome)
	}
	// (e) 未知 outcome → 保留 prior 状态。
	outcome = Outcome{Outcome: OutcomeStale, ObservedAt: now}
	applyCooldownDecision(&outcome, input, CurrentState{AccountStatus: "rate_limited"}, true, "rate_limited", now)
	if outcome.AccountStatus != "rate_limited" {
		t.Fatalf("unknown outcome: %+v", outcome)
	}
	// (f) prior fence 与 input fence 一致 → 采用 prior fence。
	priorFence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "gen-prior"}
	input.Cooldown = &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "gen-prior"}
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, CooldownFence: priorFence}
	outcome = Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyCooldownDecision(&outcome, input, prior, true, "temporary_unavailable", now)
	if outcome.CooldownFence == nil || outcome.CooldownFence.Generation != "gen-prior" {
		t.Fatalf("prior fence: %+v", outcome.CooldownFence)
	}
}

// TestW12dApplyOutcomeDecisionArms 覆盖健康决策的转换分支。
func TestW12dApplyOutcomeDecisionArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	input := testScheduledAPIKeyInput(t, "http://127.0.0.1:1", "w12d-secret", "w12d-health")
	input.IssuedAt = now.Add(-time.Minute)
	input.ExpiresAt = now.Add(time.Hour)
	input.Schedule = w12dSchedule()
	// success + prior active → health_success。
	outcome := Outcome{Outcome: OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyOutcomeDecision(&outcome, input, CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}, true, "scheduled_health")
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "health_success" || outcome.FailureCount != 0 {
		t.Fatalf("health success: %+v", outcome)
	}
	// success + prior pending_test → activation_success（priorStatus 要求与
	// input eligibility 状态一致才采信 prior）。
	activationInput := input
	activationInput.Eligibility.AccountStatus = "pending_test"
	outcome = Outcome{Outcome: OutcomeSuccess, ObservedAt: now, StatusCode: 200}
	applyOutcomeDecision(&outcome, activationInput, CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "pending_test"}, true, "health")
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "activation_success" {
		t.Fatalf("activation success: %+v", outcome.Projection)
	}
	// neutral + active + scheduled_health → 立即冷却（阈值旁路）。
	outcome = Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}, true, "scheduled_health")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.CooldownFence == nil {
		t.Fatalf("immediate cooldown: %+v", outcome)
	}
	// neutral + active + 阈值内显式 health kind → health_failure。
	outcome = Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active"}, true, "health")
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "health_failure" || outcome.FailureCount != 1 {
		t.Fatalf("health failure: %+v", outcome)
	}
	// neutral + active 达到阈值 → temporary_unavailable + 输出 fence。
	outcome = Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "active", FailureCount: 2}, true, "scheduled_health")
	if outcome.AccountStatus != "temporary_unavailable" || outcome.CooldownFence == nil || outcome.FailureCount != 0 {
		t.Fatalf("threshold cooldown: %+v", outcome)
	}
	// pending_test 失败超过 24h → activation_error。
	lateStarted := now.Add(-25 * time.Hour)
	outcome = Outcome{Outcome: OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, activationInput, CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "pending_test", FailureCount: 1, FailureStartedAt: &lateStarted}, true, "scheduled_health")
	if outcome.AccountStatus != "error" || outcome.ErrorCode != "account_activation_check_timeout" || outcome.Projection.TransitionKind != "activation_error" {
		t.Fatalf("activation timeout: %+v", outcome)
	}
	// 未知 outcome → 保留状态不投影。
	outcome = Outcome{Outcome: OutcomeStale, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, CurrentState{AccountStatus: "active", FailureCount: 1}, true, "scheduled_health")
	if outcome.AccountStatus != "active" || outcome.Projection != nil {
		t.Fatalf("unknown outcome: %+v", outcome)
	}
}

// TestW12dSchedulerPureHelpers 覆盖调度剩余纯函数。
func TestW12dSchedulerPureHelpers(t *testing.T) {
	if cooldownMaxPause(Schedule{}) != defaultCooldownMaxPauseMinutes*time.Minute {
		t.Fatal("max pause default")
	}
	if cooldownMaxRecovery(Schedule{}) != defaultCooldownMaxRecoveryHours*time.Hour {
		t.Fatal("max recovery default")
	}
	if cooldownFailureDelay("a", "g", 0, 0) != 3*time.Second {
		t.Fatal("failure delay floor")
	}
	if cooldownFailureDelay("a", "g", time.Second, 6) != 60*time.Second {
		t.Fatal("failure delay slow path")
	}
	if cooldownFailureDelay("a", "g", time.Second, 3) != 4*time.Second {
		t.Fatal("failure delay doubling")
	}
	// boundedCooldownRemaining：仅 temporary_unavailable 且未开启持续探活。
	disabled := false
	enabled := true
	fence := &CooldownFence{ObservationStartedAt: time.Now().UTC().Add(-time.Minute)}
	if _, bounded := boundedCooldownRemaining(Input{}, "active", fence, time.Now()); bounded {
		t.Fatal("wrong status must not bound")
	}
	if _, bounded := boundedCooldownRemaining(Input{Eligibility: Eligibility{TemporaryUnavailableContinuousProbeEnabled: &enabled}}, "temporary_unavailable", fence, time.Now()); bounded {
		t.Fatal("continuous probe must not bound")
	}
	remaining, bounded := boundedCooldownRemaining(Input{Eligibility: Eligibility{TemporaryUnavailableContinuousProbeEnabled: &disabled}}, "temporary_unavailable", fence, time.Now())
	if !bounded || remaining <= 0 {
		t.Fatalf("bounded remaining=%s bounded=%t", remaining, bounded)
	}
	// validCooldownFence 五元检查。
	if validCooldownFence(nil, Input{}) {
		t.Fatal("nil fence invalid")
	}
	if validCooldownFence(&CooldownFence{Generation: "g"}, Input{}) {
		t.Fatal("zero observation invalid")
	}
	source := int64(3)
	if !validCooldownFence(&CooldownFence{ObservationStartedAt: time.Now(), Generation: "g", SourceConfigRevision: &source}, Input{Eligibility: Eligibility{SourceConfigRevision: &source}}) {
		t.Fatal("matching source revision must pass")
	}
	if validCooldownFence(&CooldownFence{ObservationStartedAt: time.Now(), Generation: "g"}, Input{Eligibility: Eligibility{SourceConfigRevision: &source}}) {
		t.Fatal("missing source revision must fail")
	}
	// inputEligible。
	pending := Input{Eligibility: Eligibility{AccountStatus: "pending_test", BoundGroup: true, AuthorizationEligible: true}}
	if !inputEligible(pending) {
		t.Fatal("pending_test must be eligible without schedulable")
	}
	if inputEligible(Input{Eligibility: Eligibility{AccountStatus: "active", BoundGroup: true, AuthorizationEligible: true}}) {
		t.Fatal("unschedulable active must not be eligible")
	}
	// reconciliationDue 与 waitContext。
	if !reconciliationDue(time.Now().Add(time.Hour), time.Now()).After(time.Now()) {
		t.Fatal("future cooldown must win")
	}
	if err := waitContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("wait: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	if minDuration(time.Second, time.Minute) != time.Second || minDuration(time.Minute, time.Second) != time.Second {
		t.Fatal("min duration")
	}
	// passiveDelayBefore。
	if passiveDelayBefore(0) != time.Millisecond {
		t.Fatal("zero deadline floor")
	}
	if delay := passiveDelayBefore(time.Hour); delay <= 0 || delay >= time.Hour {
		t.Fatalf("passive delay=%s", delay)
	}
	// Runner Status/Ready。
	runner := &Runner{}
	if runner.Ready() {
		t.Fatal("nil runner must not be ready")
	}
	if _, _, err := (&Store{}).AcquireOwnerLease(context.Background(), "", time.Minute); err == nil {
		t.Fatal("blank owner must fail")
	}
}

// TestW12dRunCycleArms 覆盖 runCycle 的输入/请求装载错误臂（SQLite store）。
func TestW12dRunCycleArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-cycle.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w12d-cycle", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease: %v %t", err, acquired)
	}
	// 缺失 input 目录 → LoadSignedInputFiles 报错。
	runner := NewRunner(Config{InputDirectory: filepath.Join(t.TempDir(), "absent"), InputKeys: map[string][]byte{"current": []byte("k")}, Now: time.Now}, store, nil)
	if err := runner.runCycle(context.Background(), lease); err == nil {
		t.Fatal("missing input directory must fail")
	}
	// directInputReader.LoadDue 错误臂。
	failing := &failingDirectReader{}
	runner = NewRunner(Config{InputDirectory: t.TempDir(), InputKeys: map[string][]byte{"current": []byte("k")}, Now: time.Now}, store, nil)
	runner.directInputReader = failing
	if err := runner.runCycle(context.Background(), lease); err == nil || err.Error() != "load due failed" {
		t.Fatalf("reader failure: %v", err)
	}
	// 显式请求装载错误臂。
	failing.failExplicit = true
	if err := runner.runCycle(context.Background(), lease); err == nil || err.Error() != "load explicit failed" {
		t.Fatalf("explicit failure: %v", err)
	}
}

type failingDirectReader struct {
	failExplicit bool
}

func (r *failingDirectReader) LoadDueWithFailures(ctx context.Context, limit int) (DirectInputLoadResult, error) {
	if r.failExplicit {
		return DirectInputLoadResult{}, errors.New("load explicit failed")
	}
	return DirectInputLoadResult{}, errors.New("load due failed")
}

func (r *failingDirectReader) LoadDue(ctx context.Context, limit int) ([]Input, error) {
	return nil, errors.New("load due failed")
}

func (r *failingDirectReader) LoadAccount(ctx context.Context, accountID string) ([]Input, error) {
	return nil, errors.New("load explicit failed")
}
