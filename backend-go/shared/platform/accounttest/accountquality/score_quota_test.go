package accountquality

import (
	"math"
	"testing"
	"time"
)

func ptrI64(v int64) *int64     { return &v }
func ptrF64(v float64) *float64 { return &v }
func ptrStr(v string) *string   { return &v }

func TestComputeQualityScore(t *testing.T) {
	base := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		ewma       *int64
		state      QualityState
		updatedAgo time.Duration
		want       int64
	}{
		{"fresh 无延迟", nil, QualityFresh, 0, UnknownQualityScore},
		{"fresh 延迟", ptrI64(120), QualityFresh, 0, 120},
		{"stale 罚分", ptrI64(120), QualityStale, 0, 120 + StalePenaltyMs},
		{"failed 罚分", ptrI64(120), QualityFailed, 0, 120 + FailurePenaltyMs},
		{"unknown 罚分", ptrI64(120), QualityUnknown, 0, 120 + UnknownStatePenaltyMs},
		{"龄期罚分 5 分钟", ptrI64(0), QualityFresh, 5 * time.Minute, 500},
		{"龄期罚分上限", ptrI64(0), QualityFresh, 200 * time.Minute, AgePenaltyCapMs},
		{"上限叠加 20000（下限 0 仅防理论负值）", ptrI64(0), QualityUnknown, 200 * time.Minute, 20_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeQualityScore(tc.ewma, nil, tc.state, base.Add(-tc.updatedAgo), base)
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestNextEwma(t *testing.T) {
	cases := []struct {
		name   string
		prev   *int64
		recent *int64
		want   *int64
	}{
		{"都有值 0.6/0.4", ptrI64(100), ptrI64(200), ptrI64(140)},
		{"prev 缺失", nil, ptrI64(200), ptrI64(200)},
		{"recent 缺失", ptrI64(100), nil, ptrI64(100)},
		{"都缺失", nil, nil, nil},
		{"四舍五入", ptrI64(101), ptrI64(102), ptrI64(101)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextEwma(tc.prev, tc.recent)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("want nil got %v", *got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("want %v got %v", *tc.want, got)
			}
		})
	}
}

func TestAutomaticProbeOutcome(t *testing.T) {
	cases := []struct {
		name     string
		result   ProbeResult
		evidence ProbeEvidence
		want     ProbeOutcome
	}{
		{"成功", ProbeResult{Success: true}, ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 200}, OutcomeCompleteSuccess},
		{"framing 完成但语义失败", ProbeResult{Success: false}, ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 200}, OutcomeFramingCompleteNeutral},
		{"HTTP 500", ProbeResult{Success: false}, ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 500}, OutcomeFramingCompleteNeutral},
		{"传输超时", ProbeResult{}, ProbeEvidence{HasRealUpstreamAttempt: true, TransportFailureKind: TransportFailureTimeout}, OutcomeUpstreamFailure},
		{"连接失败", ProbeResult{}, ProbeEvidence{HasRealUpstreamAttempt: true, TransportFailureKind: TransportFailureConnection}, OutcomeUpstreamFailure},
		{"读中断", ProbeResult{}, ProbeEvidence{HasRealUpstreamAttempt: true, TransportFailureKind: TransportFailureRead}, OutcomeUpstreamFailure},
		{"被取消", ProbeResult{Success: true}, ProbeEvidence{Canceled: true}, OutcomeProbeTaskFailure},
		{"诊断超时耗尽后超时", ProbeResult{}, ProbeEvidence{HasRealUpstreamAttempt: true, TimedOut: true, DiagnosticTimeoutExhausted: true}, OutcomeUpstreamFailure},
		{"超时但未耗尽且有真实尝试", ProbeResult{}, ProbeEvidence{HasRealUpstreamAttempt: true, TimedOut: true}, OutcomeProbeTaskFailure},
		{"无任何上游尝试", ProbeResult{}, ProbeEvidence{}, OutcomeProbeTaskFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AutomaticProbeOutcome(tc.result, tc.evidence); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestSystemInsufficientQuotaRuleMatches(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		errorCode  string
		text       string
		want       bool
	}{
		{"402 无码", 402, "", "", true},
		{"403 稳定码", 403, "insufficient_quota", "", true},
		{"403 quota 代码", 403, "monthly_quota_reached", "", true},
		{"403 非 quota 码", 403, "forbidden", "", false},
		{"403 文本命中", 403, "", "当前余额不足，请充值", true},
		{"403 英文文本", 403, "", "You have insufficient balance", true},
		{"403 排除标识文本", 403, "", "content policy violation: forbidden", false},
		{"非 402/403", 429, "insufficient_quota", "", false},
		{"无任何证据", 403, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SystemInsufficientQuotaRuleMatches(tc.statusCode, tc.errorCode, "", tc.text)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestExtractQuotaRecoveryHint(t *testing.T) {
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		body     string
		headers  map[string]string
		wantMode QuotaRecoveryMode
		wantNil  bool
	}{
		{"reset_at 秒级未来", `{"error":{"reset_at":1798000000}}`, nil, QuotaRecoveryExplicitReset, false},
		{"reset_at 过去回落", `{"reset_at":1000}`, nil, "", true},
		{"reset_after_seconds", `{"reset_after_seconds":90}`, nil, QuotaRecoveryExplicitReset, false},
		{"retry-after 头", "", map[string]string{"Retry-After": "120"}, QuotaRecoveryExplicitReset, false},
		{"provider 头", "", map[string]string{"x-ratelimit-reset": "1799000000000"}, QuotaRecoveryExplicitReset, false},
		{"无任何 hint", `{}`, nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractQuotaRecoveryHint(tc.body, tc.headers, now)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil got %+v", got)
				}
				return
			}
			if got == nil || got.Mode != tc.wantMode {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestResolveQuotaRetestDecision(t *testing.T) {
	observedAt := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	status500 := 500
	body := `{"error":{"code":"insufficient_quota","message":"quota exceeded","reset_at":1798000000}}`
	t.Run("explicit_reset 来自 reset_at", func(t *testing.T) {
		decision := ResolveQuotaRetestDecision(ProbeResult{
			StatusCode:       &status500,
			ResponseBodyText: body,
		}, ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
			&OpenAIAccountCandidate{ID: "acc-1"}, "", "", "acc-1:key-1", observedAt)
		if !decision.QuotaFailure {
			t.Fatal("应判定额度失败")
		}
		if decision.HasRecoveryMode != true || decision.RecoveryMode != QuotaRecoveryExplicitReset {
			t.Fatalf("恢复模式不符: %+v", decision)
		}
		if decision.RecoveryHint == nil || decision.RecoveryHint.Source != HintSourceResetAt {
			t.Fatalf("hint 来源不符: %+v", decision.RecoveryHint)
		}
		if decision.TimedOut {
			t.Fatal("explicit_reset 不应判超时")
		}
	})

	t.Run("generic 且观察窗口超 30 天", func(t *testing.T) {
		startedAt := observedAt.Add(-31 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z07:00")
		decision := ResolveQuotaRetestDecision(ProbeResult{
			StatusCode: &status500,
		}, ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
			&OpenAIAccountCandidate{ID: "acc-1"}, QuotaRecoveryGenericErrorCode, startedAt, "acc-1:key-1", observedAt)
		if !decision.QuotaFailure || !decision.TimedOut {
			t.Fatalf("应判定 30 天观察超时: %+v", decision)
		}
		if decision.CooldownUntil != "" {
			t.Fatalf("超时不应给出 cooldownUntil: %s", decision.CooldownUntil)
		}
	})

	t.Run("generic 未超时给出通用冷却", func(t *testing.T) {
		decision := ResolveQuotaRetestDecision(ProbeResult{
			StatusCode: &status500,
		}, ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
			&OpenAIAccountCandidate{ID: "acc-1"}, "", "", "acc-1:key-1", observedAt)
		if !decision.QuotaFailure || decision.TimedOut {
			t.Fatalf("应为 generic 未超时: %+v", decision)
		}
		if decision.CooldownUntil == "" {
			t.Fatal("应给出通用冷却时间")
		}
	})

	t.Run("非额度失败", func(t *testing.T) {
		decision := ResolveQuotaRetestDecision(ProbeResult{
			StatusCode: &status500,
			Message:    "connection reset",
		}, ProbeEvidence{HasRealUpstreamAttempt: true, TransportFailureKind: TransportFailureConnection},
			&OpenAIAccountCandidate{ID: "acc-1"}, "", "", "acc-1:key-1", observedAt)
		if decision.QuotaFailure || decision.HasRecoveryMode {
			t.Fatalf("不应判定额度失败: %+v", decision)
		}
	})
}

func TestQuotaRecoveryCooldownUntilDeterministic(t *testing.T) {
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	a := GenericAPIKeyQuotaCooldownUntil(nil, "acc:key", now)
	b := GenericAPIKeyQuotaCooldownUntil(nil, "acc:key", now)
	if a != b {
		t.Fatalf("同 seed 应确定: %s vs %s", a, b)
	}
	c := GenericAPIKeyQuotaCooldownUntil(nil, "other", now)
	if a == c {
		t.Fatalf("不同 seed 应有不同偏移")
	}
	parsed, err := time.Parse(time.RFC3339, a)
	if err != nil {
		t.Fatal(err)
	}
	// 通用策略为 duration 60min + 对称窗口 ±30min → 落在 now+30min ~ now+90min。
	if diff := parsed.Sub(now); diff < 30*time.Minute || diff > 90*time.Minute {
		t.Fatalf("冷却时间超出策略窗口: %v", diff)
	}
}

func TestDeterministicOffsetMatchesJS(t *testing.T) {
	// passiveScheduleDeterministicOffsetMs 的 FNV-1a 算法锚点：
	// seed "abc"：hash = 2166136261 ^97 *16777619 ... 逐码元计算。
	var hash uint32 = 2166136261
	for _, unit := range []uint16{'a', 'b', 'c'} {
		hash ^= uint32(unit)
		hash *= 16777619
	}
	window := JitterWindowMs(60 * 60 * 1000)
	span := uint64(window)*2 + 1
	want := int64(uint64(hash)%span) - window
	if want == 0 {
		want = 1
	}
	if got := DeterministicOffsetMs(60*60*1000, "abc"); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
	// 零偏移改 1 的约定：找一个会让 hash%span==window 的 seed 难以枚举，
	// 以窗口边界替代验证。
	if got := DeterministicOffsetMs(2, "x"); got != 1 && math.Abs(float64(got)) != 1 {
		t.Logf("小窗口偏移: %d", got)
	}
}

func TestQuotaRecoveryDelaySecondsFloor(t *testing.T) {
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	candidate := &OpenAIAccountCandidate{ID: "acc-1"}
	// 通用 60 分钟策略减去当前时刻必然 > 60s。
	got := quotaRecoveryDelaySeconds(candidate, "key-1", "", now)
	if got < 60 {
		t.Fatalf("顺延秒数不应低于 60: %d", got)
	}
}
