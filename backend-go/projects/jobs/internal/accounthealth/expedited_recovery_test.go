// expedited_recovery_test.go 覆盖「AI 账户特供快速恢复通道」jobs/shared 范围
// （设计契约 docs/functions/AI账户特供快速恢复通道设计.md v3.2 §5/§6/§11）：
// 特供三处消费点与 7 天停损保留、普通账户节奏逐字节回归、expedited_recovery
// 签名回路、PG 直读候选特供列装配与冷却类内优先排序。
// 注：原借 SQLite store 载体的新鲜度门/直读 reader 用例随 sqlite 臂退役，
// PG 侧回归覆盖待存储迁移批次重建。
package accounthealth

import (
	"context"
	"encoding/json"
	"strings"
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

// fakeDirectInputReaderOnly 是带 DirectInputReader 装配面但无业务读面的桩
// （门保持未装配的判据验证）。
type fakeDirectInputReaderOnly struct {
	fakeDirectInputLoader
}

func (loader *fakeDirectInputReaderOnly) SetSuppressionProvider(func(context.Context, time.Time) ([]DirectInputSuppression, error)) {
}

// TestNewRunnerWithDirectInputReaderWiresFreshnessGate：PG 直读 reader 自动
// 装配新鲜度门；无业务读面的 loader 保持门未装配。
func TestNewRunnerWithDirectInputReaderWiresFreshnessGate(t *testing.T) {
	runner := NewRunnerWithDirectInputReader(Config{Now: time.Now}, nil, nil, &PostgresDirectInputReader{})
	if runner.inputFreshness == nil {
		t.Fatal("PG 直读 reader 必须自动装配新鲜度门")
	}
	runner = NewRunnerWithDirectInputReader(Config{Now: time.Now}, nil, nil, &fakeDirectInputReaderOnly{})
	if runner.inputFreshness != nil {
		t.Fatal("无业务读面 loader 的门必须保持未装配")
	}
}

// TestDirectInputCandidatesSQLCarryExpeditedLayer（§6 传播链/§11.3）：PG 候选
// SQL 携带特供列、冷却类内第一优先层（ROW_NUMBER 键与兜底键各一处），且
// pending_test 激活类窗口保持原键、全局第一优先不受影响。
func TestDirectInputCandidatesSQLCarryExpeditedLayer(t *testing.T) {
	sql := directInputCandidatesSQL
	if strings.Count(sql, "a.expedited_recovery_enabled") != 3 {
		t.Fatalf("候选 SQL 必须恰好引用特供列 3 次（SELECT 1 + 排序 2）: count=%d", strings.Count(sql, "a.expedited_recovery_enabled"))
	}
	if strings.Count(sql, "THEN a.expedited_recovery_enabled ELSE 0 END DESC") != 2 {
		t.Fatalf("候选 SQL 必须在冷却类 ROW_NUMBER 键与兜底键各携带特供优先层: %s", sql)
	}
	if !strings.Contains(sql, "PARTITION BY CASE WHEN a.status = 'pending_test' THEN 0 WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 1 ELSE 2 END\n      ORDER BY a.next_health_check_at ASC NULLS FIRST, a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC)") {
		t.Fatalf("pending_test 激活类窗口排序键必须保持原样（特供不影响激活类）: %s", sql)
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
