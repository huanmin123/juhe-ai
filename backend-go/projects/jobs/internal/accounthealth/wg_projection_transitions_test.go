package accounthealth

import (
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"testing"
	"time"
)

// wgTransitionOutcome 构造指定 transition kind 的投影 outcome（fence 形状
// 与归档一致：expected/输出共用同一不可变 fence）。
func wgTransitionOutcome(kind string, observedAt time.Time, withOutputFence bool, expectedStatus string) Outcome {
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: observedAt.Add(-time.Hour), Generation: "gen-" + kind, SourceConfigRevision: nil}
	projection := &Projection{
		TargetAccountID:       "acct-1",
		TransitionKind:        kind,
		InputVersion:          1,
		ConfigRevision:        5,
		DispatchRevision:      7,
		ExpectedAccountStatus: expectedStatus,
		Values: map[string]any{
			"last_health_check_at":          observedAt.Format(time.RFC3339Nano),
			"last_health_check_status_code": 503,
		},
	}
	if kind == "cooldown_defer" || kind == "cooldown_failure" || kind == "temporary_unavailable" || kind == "cooldown_error" {
		projection.CooldownFence = fence
		projection.ExpectedCooldownFence = fence
	}
	// outcome 与 transition 的匹配真值表见 outcomeMatchesTransition：
	// 成功族需要 Success；defer 需要 Neutral；failure/error 需要 UpstreamFailed。
	outcomeKind := exactkeyprobe.OutcomeUpstreamFailed
	switch kind {
	case "cooldown_defer":
		outcomeKind = exactkeyprobe.OutcomeNeutral
	case "health_success":
		outcomeKind = exactkeyprobe.OutcomeSuccess
	}
	outcome := Outcome{
		OutcomeID:        "outcome-" + kind,
		RequestID:        "request-" + kind,
		AccountID:        "acct-1",
		Outcome:          outcomeKind,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
		StatusCode:       503,
		ErrorCode:        "upstream_5xx",
		ErrorMessage:     "上游失败",
		NextDueAt:        ptrTime(observedAt.Add(30 * time.Minute)),
		FailureCount:     2,
		FailureStartedAt: ptrTime(observedAt.Add(-2 * time.Hour)),
		Projection:       projection,
	}
	if withOutputFence {
		outcome.CooldownFence = fence
	}
	return outcome
}

func TestCooldownDeferBounds(t *testing.T) {
	if got := cooldownDefer(-1, 10*time.Second, time.Hour); got < 3*time.Second {
		t.Fatalf("负步长必须钳到最小推迟: %v", got)
	}
	if got := cooldownDefer(0, time.Second, time.Hour); got != 3*time.Second {
		t.Fatalf("过小 base 必须钳到 3s: %v", got)
	}
	if got := cooldownDefer(0, 48*time.Hour, time.Hour); got != time.Hour {
		t.Fatalf("base 超过 maximum 必须钳到 maximum: %v", got)
	}
}

// TestSignedPayloadVerificationBranches 覆盖签名载荷校验的损坏分支。
func TestSignedPayloadVerificationBranches(t *testing.T) {
	keys := map[string][]byte{"runtime-v1": []byte("0123456789abcdef0123456789abcdef")}
	if _, err := VerifySignedPayload([]byte("not-json"), keys); err == nil {
		t.Fatal("非法 envelope 必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"","payload":"","signature":""}`), keys); err == nil {
		t.Fatal("缺 key ID 必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"runtime-v1","payload":"!!!","signature":""}`), keys); err == nil {
		t.Fatal("非法 payload 编码必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"runtime-v1","payload":"e30","signature":"!!!"}`), keys); err == nil {
		t.Fatal("非法 signature 编码必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"rs256","key_id":"runtime-v1","payload":"e30","signature":"AA"}`), keys); err == nil {
		t.Fatal("不支持算法必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"missing","payload":"e30","signature":"AA"}`), keys); err == nil {
		t.Fatal("未知 key 必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"runtime-v1","payload":"e30","signature":"AA"}`), keys); err == nil {
		t.Fatal("签名不匹配必须报错")
	}
}
