// expedited_recovery_test.go 覆盖「AI 账户特供快速恢复通道」jobs/shared 范围
// （设计契约 docs/functions/AI账户特供快速恢复通道设计.md v3.2 §5/§6/§11）：
// 特供三处消费点与 7 天停损保留、普通账户节奏逐字节回归、执行前新鲜度门
// （门前拒绝零上游/零写回、门后竞态 fence 拒绝、失败关闭与诊断限频）、
// expedited_recovery 签名回路、双 reader 特供列装配与冷却类内优先排序。
package accounthealth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// newCooldownRetestInput 构造合法的冷却复测输入（五元 fence + cooldown_until）。
func newCooldownRetestInput(fence *exactkeyprobe.CooldownFence, cooldownUntil time.Time) exactkeyprobe.Input {
	input := testInput("https://api.example.com", "chat_json")
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = exactkeyprobe.Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 10, MaxRecoveryHours: 12}
	return input
}

// TestExpeditedCooldownNeutralCapIsSixtySeconds：特供中性封顶基准 60s
// （jitter 后 30–90s 且恒不等于基准，§5/§11.1）。
func TestExpeditedCooldownNeutralCapIsSixtySeconds(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "wexp-neutral-gen"}
	cooldownUntil := now.Add(-time.Second)
	input := newCooldownRetestInput(fence, cooldownUntil)
	input.Schedule.CooldownNeutralMaxMS = 60_000
	input.Eligibility.ExpeditedRecovery = true
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), time.Minute) {
		t.Fatalf("特供中性封顶基准必须为 60s（jitter [30s,90s] 且不等于基准）: %s", outcome.NextDueAt.Sub(now))
	}
	if outcome.AccountStatus != "temporary_unavailable" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
		t.Fatalf("特供中性顺延必须保持冷却态 defer: %#v", outcome)
	}
}

// TestNormalCooldownNeutralCapStaysFifteenMinutes：普通账户中性封顶 15 分钟
// 逐字节不变回归（jitter 后 14.5–15.5 分钟，§11.1）。
func TestNormalCooldownNeutralCapStaysFifteenMinutes(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "wnormal-neutral-gen"}
	cooldownUntil := now.Add(-time.Second)
	input := newCooldownRetestInput(fence, cooldownUntil)
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), 15*time.Minute) {
		t.Fatalf("普通中性封顶必须维持 15 分钟（jitter [14.5m,15.5m]）: %s", outcome.NextDueAt.Sub(now))
	}
}

// TestExpeditedSlowRetryLaneIsFifteenSeconds：特供连续失败慢速道基准 15s
// （jitter 后 7.5–22.5s）；普通账户保持 60s 基准不变（§5/§11.1）。
func TestExpeditedSlowRetryLaneIsFifteenSeconds(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "wexp-slow-gen"}
	cooldownUntil := now.Add(-time.Second)

	expedited := newCooldownRetestInput(fence, cooldownUntil)
	expedited.Eligibility.ExpeditedRecovery = true
	prior := CurrentState{InputVersion: expedited.InputVersion, ConfigRevision: expedited.ConfigRevision, DispatchRevision: expedited.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 6, CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, expedited, prior, true, "cooldown_retest")
	if outcome.ErrorCode != "" || outcome.AccountStatus != "temporary_unavailable" {
		t.Fatalf("特供慢速道不得终态化: %#v", outcome)
	}
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), 15*time.Second) {
		t.Fatalf("特供慢速道基准必须为 15s（jitter [7.5s,22.5s]）: %s", outcome.NextDueAt.Sub(now))
	}

	normal := newCooldownRetestInput(fence, cooldownUntil)
	normal.Eligibility.TemporaryUnavailableContinuousProbeEnabled = boolPointer(true)
	normalOutcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&normalOutcome, normal, prior, true, "cooldown_retest")
	if normalOutcome.NextDueAt == nil || !isJitteredWithin(normalOutcome.NextDueAt.Sub(now), time.Minute) {
		t.Fatalf("普通慢速道基准必须保持 60s（jitter [30s,90s]）: %s", normalOutcome.NextDueAt.Sub(now))
	}
}

// TestExpeditedCooldownSkipsLongTermChannel：超过最大恢复观察窗后特供不降频
// （无 long_term 错误码、继续常规失败节奏）；普通账户 1 小时长期道仍生效
// （§5/§11.1）。
func TestExpeditedCooldownSkipsLongTermChannel(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-2 * time.Hour), Generation: "wexp-long-gen"}
	cooldownUntil := now.Add(-time.Second)

	expedited := newCooldownRetestInput(fence, cooldownUntil)
	expedited.Schedule.MaxRecoveryHours = 1
	expedited.Eligibility.ExpeditedRecovery = true
	prior := CurrentState{InputVersion: expedited.InputVersion, ConfigRevision: expedited.ConfigRevision, DispatchRevision: expedited.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 0, CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, expedited, prior, true, "cooldown_retest")
	if outcome.ErrorCode != "" {
		t.Fatalf("特供超过观察窗不得进入长期降频道: %#v", outcome)
	}
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), 3*time.Second) {
		t.Fatalf("特供跳过长期道后必须保持初始退避 3s 节奏: %s", outcome.NextDueAt.Sub(now))
	}

	normal := newCooldownRetestInput(fence, cooldownUntil)
	normal.Schedule.MaxRecoveryHours = 1
	normal.Eligibility.TemporaryUnavailableContinuousProbeEnabled = boolPointer(true)
	normalOutcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&normalOutcome, normal, prior, true, "cooldown_retest")
	if normalOutcome.ErrorCode != "cooldown_retest_long_term_unavailable" || normalOutcome.NextDueAt == nil || !isJitteredWithin(normalOutcome.NextDueAt.Sub(now), time.Hour) {
		t.Fatalf("普通账户长期降频道（1 小时/轮）必须仍生效: %#v", normalOutcome)
	}
}

// TestExpeditedBypassesBoundedTenMinuteTerminal：特供视同持续探针，绕开有界
// 10 分钟 error 终态；普通未开持续探针账户同一形态仍写
// cooldown_retest_limited_probe_timeout（§5/§11.1）。
func TestExpeditedBypassesBoundedTenMinuteTerminal(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownLimitedProbeTimeout - time.Minute), Generation: "wexp-bounded-gen"}
	cooldownUntil := now.Add(-time.Second)
	prior := CurrentState{InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, AccountStatus: "temporary_unavailable", FailureCount: 1, CooldownFence: fence}

	expedited := newCooldownRetestInput(fence, cooldownUntil)
	expedited.Eligibility.ExpeditedRecovery = true
	expedited.Eligibility.TemporaryUnavailableContinuousProbeEnabled = boolPointer(false)
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, expedited, prior, true, "cooldown_retest")
	if outcome.AccountStatus == "error" || outcome.ErrorCode != "" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_failure" {
		t.Fatalf("特供必须绕开有界 10 分钟 error 终态: %#v", outcome)
	}

	normal := newCooldownRetestInput(fence, cooldownUntil)
	normal.Eligibility.TemporaryUnavailableContinuousProbeEnabled = boolPointer(false)
	normalOutcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&normalOutcome, normal, prior, true, "cooldown_retest")
	if normalOutcome.AccountStatus != "error" || normalOutcome.ErrorCode != "cooldown_retest_limited_probe_timeout" {
		t.Fatalf("普通未开持续探针账户必须保留 10 分钟有界终态: %#v", normalOutcome)
	}
}

// TestExpeditedCooldownObservationTimeoutStillApplies：7 天观察超时对特供保留
// （即使已绕开 10 分钟有界终态，§5/§11.2）。
func TestExpeditedCooldownObservationTimeoutStillApplies(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownObservationTimeout - time.Hour), Generation: "wexp-7d-gen"}
	cooldownUntil := now.Add(-time.Second)
	input := newCooldownRetestInput(fence, cooldownUntil)
	input.Eligibility.ExpeditedRecovery = true
	prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 9, CooldownFence: fence}
	outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	if outcome.AccountStatus != "error" || outcome.ErrorCode != "cooldown_retest_observation_timeout" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_error" {
		t.Fatalf("特供 7 天观察超时停损必须保留: %#v", outcome)
	}
}

// fakeFreshnessReader 是新鲜度门的最小注入桩：可配置命中 revision、账户不
// 存在与读取失败三类判定。
type fakeFreshnessReader struct {
	mu        sync.Mutex
	revisions map[string]int64
	missing   map[string]bool
	fail      map[string]error
	calls     []string
}

func (f *fakeFreshnessReader) LoadAccountConfigRevision(_ context.Context, accountID string) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, accountID)
	if err, ok := f.fail[accountID]; ok {
		return 0, false, err
	}
	if f.missing[accountID] {
		return 0, false, nil
	}
	revision, ok := f.revisions[accountID]
	if !ok {
		return 0, false, nil
	}
	return revision, true, nil
}

// TestFreshnessGateRejectsStaleScheduledInputBeforeDispatch（§11.4/§11.11）：
// 补丁在 freshness 判定前提交 → 旧 revision 输入在门前被拒，零上游请求、
// 零账户状态写回、零失败计数；同轮新鲜账户不受阻断。
func TestFreshnessGateRejectsStaleScheduledInputBeforeDispatch(t *testing.T) {
	secret := "freshness-gate-scheduled-secret"
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()
	store, lease := openSQLiteStoreWithLease(t)
	fresh := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-gate-fresh")
	stale := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-gate-stale")
	loader := &fakeDirectInputLoader{due: []exactkeyprobe.Input{fresh, stale}}
	runner := NewRunner(Config{InputDirectory: t.TempDir(), InputKeys: map[string][]byte{"current": []byte("test-key")}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 2, DirectInputLimit: 2, Now: time.Now}, store, nil)
	runner.directInputReader = loader
	runner.inputFreshness = &fakeFreshnessReader{revisions: map[string]int64{
		"wexp-gate-fresh": fresh.ConfigRevision,
		"wexp-gate-stale": stale.ConfigRevision + 1,
	}}
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatalf("freshness 拒绝不得作为错误阻断本轮: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("门前提交补丁必须零上游请求（仅新鲜账户发出 1 次）: hits=%d", got)
	}
	if _, found, err := store.LoadCurrentState(context.Background(), stale.AccountID); err != nil || found {
		t.Fatalf("stale 输入不得写回账户状态: found=%t err=%v", found, err)
	}
	var staleOutcomes int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM account_health_outcomes WHERE account_id=?`, stale.AccountID).Scan(&staleOutcomes); err != nil || staleOutcomes != 0 {
		t.Fatalf("stale 输入不得落任何 outcome（不改失败计数）: count=%d err=%v", staleOutcomes, err)
	}
	state, found, err := store.LoadCurrentState(context.Background(), fresh.AccountID)
	if err != nil || !found || state.Outcome != exactkeyprobe.OutcomeSuccess {
		t.Fatalf("新鲜账户必须照常执行: found=%t state=%#v err=%v", found, state, err)
	}
}

// TestFreshnessGateFailsClosedOnReadFailureOrMissingAccount（§6.3/§11.11）：
// DB 读错或账户不存在按失败关闭——零上游请求、零写回、零失败计数；诊断
// input_freshness_unavailable 按账户限频。
func TestFreshnessGateFailsClosedOnReadFailureOrMissingAccount(t *testing.T) {
	secret := "freshness-gate-failclosed-secret"
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	store, lease := openSQLiteStoreWithLease(t)
	// 时钟从真实当前时刻出发（与 testInput 的有效期同基），仅限频断言段推进。
	clock := time.Now().UTC().Round(0)
	runner := NewRunner(Config{InputDirectory: t.TempDir(), InputKeys: map[string][]byte{"current": []byte("test-key")}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, Now: func() time.Time { return clock }}, store, nil)
	stub := &fakeFreshnessReader{
		fail:    map[string]error{"wexp-db-down": errors.New("business db unavailable")},
		missing: map[string]bool{"wexp-gone": true},
	}
	runner.inputFreshness = stub
	ctx := context.Background()

	dbDown := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-db-down")
	if err := runner.runInput(ctx, lease, dbDown, clock); err != nil {
		t.Fatalf("freshness 失败关闭是已处理的 skip，不得返回错误: %v", err)
	}
	gone := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-gone")
	if err := runner.runInput(ctx, lease, gone, clock); err != nil {
		t.Fatalf("账户不存在按失败关闭同样不得返回错误: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("失败关闭必须零上游请求: hits=%d", got)
	}
	for _, accountID := range []string{"wexp-db-down", "wexp-gone"} {
		if _, found, err := store.LoadCurrentState(ctx, accountID); err != nil || found {
			t.Fatalf("失败关闭不得写回账户状态 %s: found=%t err=%v", accountID, found, err)
		}
		var outcomes int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_health_outcomes WHERE account_id=?`, accountID).Scan(&outcomes); err != nil || outcomes != 0 {
			t.Fatalf("失败关闭不得计失败 %s: count=%d err=%v", accountID, outcomes, err)
		}
	}

	// 诊断限频：同一账户窗口内第二次告警不刷新时间戳；跨窗口后允许更新。
	runner.freshnessMu.Lock()
	firstWarned, recorded := runner.freshnessWarnedAt["wexp-db-down"]
	runner.freshnessMu.Unlock()
	if !recorded {
		t.Fatal("freshness 不可用必须记录限频诊断")
	}
	input := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-db-down")
	if verdict, _ := runner.checkInputFreshness(ctx, input); verdict != freshnessUnavailable {
		t.Fatal("DB 读错必须持续失败关闭")
	}
	runner.freshnessMu.Lock()
	secondWarned, stillRecorded := runner.freshnessWarnedAt["wexp-db-down"]
	runner.freshnessMu.Unlock()
	if !stillRecorded || !secondWarned.Equal(firstWarned) {
		t.Fatalf("限频窗口内不得重复告警: first=%v second=%v", firstWarned, secondWarned)
	}
	clock = clock.Add(inputFreshnessDiagnosticWindow + time.Minute)
	if verdict, _ := runner.checkInputFreshness(ctx, input); verdict != freshnessUnavailable {
		t.Fatal("跨窗口后仍必须失败关闭")
	}
	runner.freshnessMu.Lock()
	thirdWarned := runner.freshnessWarnedAt["wexp-db-down"]
	runner.freshnessMu.Unlock()
	if !thirdWarned.After(secondWarned) {
		t.Fatalf("跨窗口后必须允许新告警: second=%v third=%v", secondWarned, thirdWarned)
	}
}

// TestFreshnessGateExplicitRequestConsumesRequestWithoutStateWrite（§6.3/§11.11）：
// 显式请求路径门前拒绝：按请求终态落库消费该请求（stale /
// input_stale_before_dispatch），零上游请求、零账户健康状态写回。
func TestFreshnessGateExplicitRequestConsumesRequestWithoutStateWrite(t *testing.T) {
	secret := "freshness-gate-explicit-secret"
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	root := t.TempDir()
	input := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-explicit-stale")
	key := []byte("scheduler-input-signing-key-123456")
	request := ProbeRequest{RequestID: "wexp-explicit-request-1", AccountID: input.AccountID, Reason: "request_failure", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: time.Now().UTC().Add(time.Minute), MutateAccount: true}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, request.RequestID+requestFileSuffix)
	if err := os.WriteFile(requestPath, signedEnvelope(t, "current", key, payload), 0o600); err != nil {
		t.Fatal(err)
	}
	store, lease := openSQLiteStoreWithLease(t)
	loader := &fakeDirectInputLoader{explicit: map[string]exactkeyprobe.Input{input.AccountID: input}}
	runner := NewRunner(Config{InputDirectory: root, InputKeys: map[string][]byte{"current": key}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, DirectInputLimit: 8, Now: time.Now}, store, nil)
	runner.directInputReader = loader
	runner.inputFreshness = &fakeFreshnessReader{revisions: map[string]int64{input.AccountID: input.ConfigRevision + 1}}
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("显式请求门前拒绝必须零上游请求: hits=%d", got)
	}
	if _, found, err := store.LoadCurrentState(context.Background(), input.AccountID); err != nil || found {
		t.Fatalf("显式请求拒绝不得写回账户健康状态: found=%t err=%v", found, err)
	}
	var outcome, code string
	if err := store.db.QueryRowContext(context.Background(), `SELECT outcome,error_code FROM account_health_outcomes WHERE request_id=?`, request.RequestID).Scan(&outcome, &code); err != nil {
		t.Fatalf("拒绝必须按请求终态落库以消费该请求: %v", err)
	}
	if outcome != OutcomeStale || code != "input_stale_before_dispatch" {
		t.Fatalf("显式请求终态必须为 stale/input_stale_before_dispatch: outcome=%q code=%q", outcome, code)
	}
	if _, statErr := os.Stat(requestPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("已消费请求文件必须清理: %v", statErr)
	}
}

// TestFreshnessGateExplicitRequestFailsClosedWithoutStateWrite：显式请求路径
// freshness 不可用同样失败关闭，仅落自身 task_failed 终态消费请求。
func TestFreshnessGateExplicitRequestFailsClosedWithoutStateWrite(t *testing.T) {
	secret := "freshness-gate-explicit-failclosed-secret"
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	root := t.TempDir()
	input := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-explicit-dbdown")
	key := []byte("scheduler-input-signing-key-123456")
	request := ProbeRequest{RequestID: "wexp-explicit-request-2", AccountID: input.AccountID, Reason: "request_failure", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: time.Now().UTC().Add(time.Minute), MutateAccount: true}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, request.RequestID+requestFileSuffix), signedEnvelope(t, "current", key, payload), 0o600); err != nil {
		t.Fatal(err)
	}
	store, lease := openSQLiteStoreWithLease(t)
	loader := &fakeDirectInputLoader{explicit: map[string]exactkeyprobe.Input{input.AccountID: input}}
	runner := NewRunner(Config{InputDirectory: root, InputKeys: map[string][]byte{"current": key}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, DirectInputLimit: 8, Now: time.Now}, store, nil)
	runner.directInputReader = loader
	runner.inputFreshness = &fakeFreshnessReader{fail: map[string]error{input.AccountID: errors.New("business db unavailable")}}
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("freshness 不可用必须零上游请求: hits=%d", got)
	}
	if _, found, err := store.LoadCurrentState(context.Background(), input.AccountID); err != nil || found {
		t.Fatalf("失败关闭不得写回账户健康状态: found=%t err=%v", found, err)
	}
	var outcome, code string
	if err := store.db.QueryRowContext(context.Background(), `SELECT outcome,error_code FROM account_health_outcomes WHERE request_id=?`, request.RequestID).Scan(&outcome, &code); err != nil {
		t.Fatal(err)
	}
	if outcome != exactkeyprobe.OutcomeTaskFailed || code != "input_freshness_unavailable" {
		t.Fatalf("显式请求失败关闭终态必须为 task_failed/input_freshness_unavailable: outcome=%q code=%q", outcome, code)
	}
}

// TestFreshnessGateRaceAfterCheckAllowsRequestButFenceRejectsWriteBack
// （§6.3/§11.4）：补丁在门判定之后提交允许请求竞态（探针照常发出），但迟到
// 结果的写回由既有 epoch fence 拒绝——jobs current_state 保持补丁后新代次。
func TestFreshnessGateRaceAfterCheckAllowsRequestButFenceRejectsWriteBack(t *testing.T) {
	secret := "freshness-gate-race-secret"
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	store, lease := openSQLiteStoreWithLease(t)
	now := time.Now().UTC().Round(0)
	input := testScheduledAPIKeyInput(t, server.URL, secret, "wexp-race")
	input.IssuedAt = now.Add(-time.Minute)
	input.ExpiresAt = now.Add(time.Hour)
	cooldownUntil := now.Add(-time.Second)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-2 * time.Minute), Generation: "wexp-race-input-gen"}
	input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	// 补丁后的新代次（ConfigRevision 6）已先落到 jobs current_state。
	patchedFence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "wexp-race-patched-gen"}
	patchedDue := now.Add(30 * time.Minute)
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "wexp-race-seed", RequestID: "wexp-race-seed-request", AccountID: input.AccountID, Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: now.Add(-time.Minute), InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision + 1, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 2, NextDueAt: &patchedDue, CooldownFence: patchedFence, Projection: &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_failure", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision + 1, DispatchRevision: input.DispatchRevision, ExpectedAccountStatus: "temporary_unavailable", ExpectedCooldownFence: patchedFence, CooldownFence: patchedFence}})
	runner := NewRunner(Config{InputDirectory: t.TempDir(), InputKeys: map[string][]byte{"current": []byte("test-key")}, CredentialSecret: secret, ProbeTimeout: time.Second, MaxResponseBytes: 1024, MaxConcurrency: 1, Now: func() time.Time { return now }}, store, nil)
	// 门判定时业务行仍与输入同代次（revision 1）→ 门通过；其后的补丁竞态由
	// 既有 fence 拒绝写回。
	runner.inputFreshness = &fakeFreshnessReader{revisions: map[string]int64{input.AccountID: input.ConfigRevision}}
	if err := runner.runInput(context.Background(), lease, input, now); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("门后竞态允许请求已发出: hits=%d", got)
	}
	state, found, err := store.LoadCurrentState(context.Background(), input.AccountID)
	if err != nil || !found {
		t.Fatalf("补丁后的 current_state 必须仍在: found=%t err=%v", found, err)
	}
	if state.ConfigRevision != input.ConfigRevision+1 || state.OutcomeID != "wexp-race-seed" || state.FailureCount != 2 || state.NextDueAt == nil || !state.NextDueAt.Equal(patchedDue) || !sameCooldownFence(state.CooldownFence, patchedFence) {
		t.Fatalf("迟到结果必须被既有 epoch fence 拒绝写回: %#v", state)
	}
	var outcomes int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM account_health_outcomes WHERE account_id=?`, input.AccountID).Scan(&outcomes); err != nil || outcomes < 2 {
		t.Fatalf("迟到结果保留为 audit outcome 行: count=%d err=%v", outcomes, err)
	}
}

// TestExpeditedRecoverySurvivesSignedInputRoundTrip（§11.5）：expedited_recovery
// 随 Eligibility 序列化 → VerifySignedInput 反序列化无损；false 时 omitempty
// 不落线载键。
func TestExpeditedRecoverySurvivesSignedInputRoundTrip(t *testing.T) {
	key := []byte("expedited-signing-key-1234567890ab")
	keys := map[string][]byte{"current": key}
	now := time.Now().UTC().Round(0)
	build := func(expedited bool) exactkeyprobe.Input {
		input := testInput("https://api.example.com", "chat_json")
		input.IssuedAt = now
		input.ExpiresAt = now.Add(time.Hour)
		input.Eligibility = exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, ExpeditedRecovery: expedited}
		return input
	}
	expeditedPayload, err := json.Marshal(build(true))
	if err != nil {
		t.Fatal(err)
	}
	expedited, err := VerifySignedInput(signedEnvelope(t, "current", key, expeditedPayload), keys)
	if err != nil {
		t.Fatal(err)
	}
	if !expedited.Eligibility.ExpeditedRecovery {
		t.Fatalf("expedited_recovery 必须随签名输入无损往返: %#v", expedited.Eligibility)
	}
	normalPayload, err := json.Marshal(build(false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(normalPayload), "expedited_recovery") {
		t.Fatal("omitempty 契约：未标记账户的线载 JSON 不得包含 expedited_recovery 键")
	}
	normal, err := VerifySignedInput(signedEnvelope(t, "current", key, normalPayload), keys)
	if err != nil {
		t.Fatal(err)
	}
	if normal.Eligibility.ExpeditedRecovery {
		t.Fatal("未标记账户反序列化后必须保持 false")
	}
}

// fakeDirectInputReaderOnly 是带 DirectInputReader 装配面但无业务读面的桩
// （模拟 files 后备形态：门保持未装配）。
type fakeDirectInputReaderOnly struct {
	fakeDirectInputLoader
}

func (loader *fakeDirectInputReaderOnly) SetSuppressionProvider(func(context.Context, time.Time) ([]DirectInputSuppression, error)) {
}

// TestNewRunnerWithDirectInputReaderWiresFreshnessGate：PG/SQLite 直读 reader
// 自动装配新鲜度门；无业务读面的 loader（files 形态）保持门未装配。
func TestNewRunnerWithDirectInputReaderWiresFreshnessGate(t *testing.T) {
	store, _ := openSQLiteStoreWithLease(t)
	runner := NewRunnerWithDirectInputReader(Config{Now: time.Now}, store, nil, &SQLiteDirectInputReader{})
	if runner.inputFreshness == nil {
		t.Fatal("SQLite 直读 reader 必须自动装配新鲜度门")
	}
	runner = NewRunnerWithDirectInputReader(Config{Now: time.Now}, store, nil, &PostgresDirectInputReader{})
	if runner.inputFreshness == nil {
		t.Fatal("PG 直读 reader 必须自动装配新鲜度门")
	}
	runner = NewRunnerWithDirectInputReader(Config{Now: time.Now}, store, nil, &fakeDirectInputReaderOnly{})
	if runner.inputFreshness != nil {
		t.Fatal("files 形态 loader 无业务读面，门必须保持未装配")
	}
}

// TestDirectInputCandidatesSQLCarryExpeditedLayer（§6 传播链/§11.3）：PG 与
// SQLite 候选 SQL 同步携带特供列、冷却类内第一优先层（ROW_NUMBER 键与兜底键
// 各一处），且 pending_test 激活类窗口保持原键、全局第一优先不受影响。
func TestDirectInputCandidatesSQLCarryExpeditedLayer(t *testing.T) {
	for name, sql := range map[string]string{"PG": directInputCandidatesSQL, "SQLite": sqliteDirectInputCandidatesSQL} {
		if strings.Count(sql, "a.expedited_recovery_enabled") != 3 {
			t.Fatalf("%s 候选 SQL 必须恰好引用特供列 3 次（SELECT 1 + 排序 2）: count=%d", name, strings.Count(sql, "a.expedited_recovery_enabled"))
		}
		if strings.Count(sql, "THEN a.expedited_recovery_enabled ELSE 0 END DESC") != 2 {
			t.Fatalf("%s 候选 SQL 必须在冷却类 ROW_NUMBER 键与兜底键各携带特供优先层: %s", name, sql)
		}
		if !strings.Contains(sql, "PARTITION BY CASE WHEN a.status = 'pending_test' THEN 0 WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 1 ELSE 2 END\n      ORDER BY a.next_health_check_at ASC NULLS FIRST, a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC)") {
			t.Fatalf("%s pending_test 激活类窗口排序键必须保持原样（特供不影响激活类）: %s", name, sql)
		}
	}
	pgOrder := directInputCandidatesSQL[strings.Index(directInputCandidatesSQL, "ORDER BY CASE WHEN a.status = 'pending_test'"):strings.Index(directInputCandidatesSQL, "LIMIT $2")]
	sqliteOrder := sqliteDirectInputCandidatesSQL[strings.Index(sqliteDirectInputCandidatesSQL, "ORDER BY CASE WHEN a.status = 'pending_test'"):strings.Index(sqliteDirectInputCandidatesSQL, "LIMIT $2")]
	if pgOrder != sqliteOrder {
		t.Fatalf("PG 与 SQLite 的 ORDER BY 必须逐字同构:\nPG=%q\nSQLite=%q", pgOrder, sqliteOrder)
	}
}

// seedExpeditedCooldownCandidate 以冷却形态种子一个候选；expedited 传 1/0。
func seedExpeditedCooldownCandidate(t *testing.T, fixture *sqliteDirectFixture, id string, expedited int, cooldownUntilOffset, observationOffset time.Duration) {
	t.Helper()
	seed := newSQLiteDirectCandidateSeed(id)
	seed.status = "temporary_unavailable"
	seed.extraColumns["expedited_recovery_enabled"] = expedited
	seed.extraColumns["cooldown_until"] = sqliteDirectFixtureTimeText(cooldownUntilOffset)
	seed.extraColumns["cooldown_retest_observation_started_at"] = sqliteDirectFixtureTimeText(observationOffset)
	seed.extraColumns["cooldown_retest_generation"] = "wexp-gen-" + id
	fixture.seedCandidate(t, seed)
}

// TestSQLiteDirectInputReaderExpeditedAssemblyAndOrdering（§11.3）：新列读取与
// 装配正确（Eligibility.ExpeditedRecovery / CooldownNeutralMaxMS 60s vs 900s），
// 冷却类内特供优先于更早到期的普通账户，pending_test 激活类仍全局第一优先。
func TestSQLiteDirectInputReaderExpeditedAssemblyAndOrdering(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	pending := newSQLiteDirectCandidateSeed("wexp-rd-pending")
	fixture.seedCandidate(t, pending)
	// 普通账户更早到期（无特供层时本应排前）；特供更晚到期仍必须领先。
	seedExpeditedCooldownCandidate(t, fixture, "wexp-rd-normal", 0, -2*time.Hour, -3*time.Hour)
	seedExpeditedCooldownCandidate(t, fixture, "wexp-rd-expedited", 1, -time.Hour, -90*time.Minute)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Inputs) != 3 {
		t.Fatalf("候选数必须为 3: %+v", result)
	}
	if result.Inputs[0].AccountID != "wexp-rd-pending" {
		t.Fatalf("pending_test 激活类必须保持全局第一优先: %+v", result.Inputs)
	}
	if result.Inputs[1].AccountID != "wexp-rd-expedited" || result.Inputs[2].AccountID != "wexp-rd-normal" {
		t.Fatalf("冷却类内特供必须构成第一优先层（层内仍按 cooldown_until ASC）: %+v", result.Inputs)
	}
	expedited := result.Inputs[1]
	if !expedited.Eligibility.ExpeditedRecovery {
		t.Fatalf("特供账户必须装配 ExpeditedRecovery=true: %#v", expedited.Eligibility)
	}
	if expedited.Schedule.CooldownNeutralMaxMS != 60_000 {
		t.Fatalf("特供中性封顶必须按账户覆盖为 60s: %d", expedited.Schedule.CooldownNeutralMaxMS)
	}
	normal := result.Inputs[2]
	if normal.Eligibility.ExpeditedRecovery {
		t.Fatalf("普通账户必须保持未标记: %#v", normal.Eligibility)
	}
	if normal.Schedule.CooldownNeutralMaxMS != 900_000 {
		t.Fatalf("普通中性封顶必须维持 settings 全局值 900s: %d", normal.Schedule.CooldownNeutralMaxMS)
	}
	if result.Inputs[0].Eligibility.ExpeditedRecovery || result.Inputs[0].Schedule.CooldownNeutralMaxMS != 900_000 {
		t.Fatalf("激活类输入不得携带特供档位: %#v", result.Inputs[0])
	}
}

// TestSQLiteDirectInputReaderExpeditedRebuildAfterConfigChange（§11.4 reader
// 半边）：补丁后下一轮扫描重建的输入携带新档位与新的账户 ConfigRevision。
func TestSQLiteDirectInputReaderExpeditedRebuildAfterConfigChange(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	seedExpeditedCooldownCandidate(t, fixture, "wexp-rd-rebuild", 1, -time.Hour, -2*time.Hour)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	first, err := reader.LoadDueWithFailures(context.Background(), 4)
	if err != nil || len(first.Inputs) != 1 {
		t.Fatalf("首次装配: %+v err=%v", first, err)
	}
	if !first.Inputs[0].Eligibility.ExpeditedRecovery || first.Inputs[0].Schedule.CooldownNeutralMaxMS != 60_000 || first.Inputs[0].ConfigRevision != 5 {
		t.Fatalf("补丁前输入必须携带特供档位与旧 revision: %#v", first.Inputs[0])
	}
	if _, err := fixture.business.Exec(`UPDATE accounts SET expedited_recovery_enabled = 0, config_revision = 6 WHERE id = 'wexp-rd-rebuild'`); err != nil {
		t.Fatal(err)
	}
	second, err := reader.LoadDueWithFailures(context.Background(), 4)
	if err != nil || len(second.Inputs) != 1 {
		t.Fatalf("重建装配: %+v err=%v", second, err)
	}
	if second.Inputs[0].Eligibility.ExpeditedRecovery || second.Inputs[0].Schedule.CooldownNeutralMaxMS != 900_000 || second.Inputs[0].ConfigRevision != 6 {
		t.Fatalf("补丁后重建输入必须携带普通档位与新 revision: %#v", second.Inputs[0])
	}
	revision, found, err := reader.LoadAccountConfigRevision(context.Background(), "wexp-rd-rebuild")
	if err != nil || !found || revision != 6 {
		t.Fatalf("新鲜度门必须读到补丁后 revision: revision=%d found=%t err=%v", revision, found, err)
	}
}

// TestSQLiteDirectInputReaderLoadAccountConfigRevision：新鲜度门最小查询的
// 存在/缺失/软删三臂（缺失与软删均视为不存在，由调用方失败关闭）。
func TestSQLiteDirectInputReaderLoadAccountConfigRevision(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	seed := newSQLiteDirectCandidateSeed("wexp-rd-fresh")
	fixture.seedCandidate(t, seed)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	ctx := context.Background()
	revision, found, err := reader.LoadAccountConfigRevision(ctx, "wexp-rd-fresh")
	if err != nil || !found || revision != 5 {
		t.Fatalf("存在账户必须读到 revision: revision=%d found=%t err=%v", revision, found, err)
	}
	if _, found, err := reader.LoadAccountConfigRevision(ctx, "wexp-rd-absent"); err != nil || found {
		t.Fatalf("缺失账户必须返回 found=false: found=%t err=%v", found, err)
	}
	if _, err := fixture.business.Exec(`UPDATE accounts SET deleted_at = ? WHERE id = 'wexp-rd-fresh'`, sqliteDirectFixtureTimeText(0)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.LoadAccountConfigRevision(ctx, "wexp-rd-fresh"); err != nil || found {
		t.Fatalf("软删账户必须视为不存在: found=%t err=%v", found, err)
	}
}

// TestExpeditedRecoveryDiscoveryLatencyComposite：§11.9 决策层组合对比——
// 同一条持续失败流（含越过最大恢复观察窗的长期道窗口）中，特供每轮复测
// 间隔必须逐轮小于普通账户（两档间隔的 jitter 区间逐轮不相交，断言确定
// 性成立）；成功轮两者都即时恢复 active。普通账户逐字节回归由其余用例锁定。
func TestExpeditedRecoveryDiscoveryLatencyComposite(t *testing.T) {
	now := time.Now().UTC()
	runStream := func(expedited bool, fenceOffset time.Duration, rounds int) ([]time.Duration, int) {
		observed := now
		failures := 0
		delays := make([]time.Duration, 0, rounds)
		longTerms := 0
		for i := 0; i < rounds; i++ {
			fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(fenceOffset), Generation: "wexp-composite-gen"}
			input := newCooldownRetestInput(fence, now.Add(-time.Second))
			if expedited {
				input.Eligibility.ExpeditedRecovery = true
			} else {
				input.Eligibility.TemporaryUnavailableContinuousProbeEnabled = boolPointer(true)
			}
			prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: failures, CooldownFence: fence}
			outcome := Outcome{Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: observed}
			applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
			if outcome.NextDueAt == nil || outcome.AccountStatus != "temporary_unavailable" || outcome.ErrorCode == "cooldown_retest_observation_timeout" {
				t.Fatalf("失败轮不得终态化（round=%d expedited=%t）: %#v", i+1, expedited, outcome)
			}
			if outcome.ErrorCode == "cooldown_retest_long_term_unavailable" {
				longTerms++
			}
			delays = append(delays, outcome.NextDueAt.Sub(observed))
			failures = outcome.FailureCount
			observed = *outcome.NextDueAt
		}
		return delays, longTerms
	}
	// 场景一：观察窗 12 小时内的持续失败（fence 1h 前）。普通第 6 轮起进
	// 60s 慢速道 [30s,90s]，特供 15s [7.5s,22.5s]——第 6 轮起区间不相交，
	// 特供逐轮严格更短；前 5 轮两者同为 3s 倍增。
	expDelays, expLong := runStream(true, -time.Hour, 10)
	normDelays, normLong := runStream(false, -time.Hour, 10)
	if expLong != 0 || normLong != 0 {
		t.Fatalf("1 小时窗口内不得进入长期道: expedited=%d normal=%d", expLong, normLong)
	}
	for i := 5; i < 10; i++ {
		if expDelays[i] >= normDelays[i] {
			t.Fatalf("慢速道窗口特供间隔必须逐轮更短（round=%d expedited=%s normal=%s）", i+1, expDelays[i], normDelays[i])
		}
	}
	// 场景二：已越过最大恢复观察窗（fence 13h 前，默认 MaxRecovery=12h）。
	// 普通每轮落 1 小时长期道 [30m,90m] 且带 long_term 错误码，特供不降频
	// 保持 15s [7.5s,22.5s]——区间不相交，逐轮严格更短。
	expDelays2, expLong2 := runStream(true, -13*time.Hour, 5)
	normDelays2, normLong2 := runStream(false, -13*time.Hour, 5)
	if expLong2 != 0 {
		t.Fatalf("特供不得进入长期道: %d", expLong2)
	}
	if normLong2 != 5 {
		t.Fatalf("普通账户越过观察窗后必须全部落长期道: %d", normLong2)
	}
	for i := 0; i < 5; i++ {
		if expDelays2[i] >= normDelays2[i] {
			t.Fatalf("长期道窗口特供间隔必须逐轮更短（round=%d expedited=%s normal=%s）", i+1, expDelays2[i], normDelays2[i])
		}
	}
	// 成功轮：两者都即时恢复 active 且失败计数清零（复活证据标准不变）。
	for _, expedited := range []bool{true, false} {
		fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-13 * time.Hour), Generation: "wexp-composite-success"}
		input := newCooldownRetestInput(fence, now.Add(-time.Second))
		if expedited {
			input.Eligibility.ExpeditedRecovery = true
		} else {
			input.Eligibility.TemporaryUnavailableContinuousProbeEnabled = boolPointer(true)
		}
		prior := CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: 9, CooldownFence: fence}
		outcome := Outcome{Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: now, StatusCode: 200}
		applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
		if outcome.AccountStatus != "active" || outcome.FailureCount != 0 || outcome.NextDueAt == nil {
			t.Fatalf("成功轮必须即时恢复 active（expedited=%t）: %#v", expedited, outcome)
		}
	}
}
