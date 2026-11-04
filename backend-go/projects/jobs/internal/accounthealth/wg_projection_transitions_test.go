package accounthealth

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
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
	// 成功族需要 Success；defer 需要 HTTP 失败；failure/error 需要 UpstreamFailed。
	outcomeKind := exactkeyprobe.OutcomeUpstreamFailed
	switch kind {
	case "cooldown_defer":
		outcomeKind = exactkeyprobe.OutcomeHTTPFailure
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

// TestLegacyFramingCompleteNeutralNormalizesToHTTPFailure 锁定历史存量兼容：
// 2026-11-04 前写入的 payload（outcome=framing_complete_neutral，
// transition_kind=cooldown_defer）经 payload 解码与 normalizeStoredOutcome 后
// 被视为 upstream_http_failure，并通过新的 transition 真值表校验；非 0
// StatusCode 保留，供诊断区分 HTTP 拒绝与 transport 未完成。
func TestLegacyFramingCompleteNeutralNormalizesToHTTPFailure(t *testing.T) {
	payload := []byte(`{
		"outcome_id": "legacy-neutral-outcome",
		"request_id": "legacy-neutral-request",
		"account_id": "acct-legacy",
		"outcome": "framing_complete_neutral",
		"observed_at": "2026-11-01T00:00:00Z",
		"input_version": 1,
		"config_revision": 5,
		"dispatch_revision": 7,
		"status_code": 503,
		"projection": {
			"target_account_id": "acct-legacy",
			"transition_kind": "cooldown_defer",
			"input_version": 1,
			"config_revision": 5,
			"dispatch_revision": 7,
			"expected_account_status": "temporary_unavailable"
		}
	}`)
	var outcome Outcome
	if err := json.Unmarshal(payload, &outcome); err != nil {
		t.Fatalf("解码历史 outcome payload 失败: %v", err)
	}
	if outcome.Outcome != legacyOutcomeFramingCompleteNeutral {
		t.Fatalf("测试夹具必须保留历史结果名: %q", outcome.Outcome)
	}
	if outcomeMatchesTransition(outcome, "cooldown_defer") {
		t.Fatal("未归一的历史值不得直接匹配新真值表")
	}
	outcome.Outcome = normalizeStoredOutcome(outcome.Outcome)
	if outcome.Outcome != exactkeyprobe.OutcomeHTTPFailure {
		t.Fatalf("历史值必须归一为 %q: %q", exactkeyprobe.OutcomeHTTPFailure, outcome.Outcome)
	}
	if outcome.StatusCode != 503 {
		t.Fatalf("历史 HTTP 失败的 StatusCode 必须保留: %d", outcome.StatusCode)
	}
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
		t.Fatalf("历史行必须保留 cooldown_defer 投影: %#v", outcome.Projection)
	}
	if !outcomeMatchesTransition(outcome, outcome.Projection.TransitionKind) {
		t.Fatalf("归一后的历史行必须通过过渡真值表校验: %#v", outcome)
	}
}

// TestCooldownErrorHTTPFailureTerminalPassesProjectionValidation 锁定 7 天
// HTTP 失败终态的投影真值表：新结果 upstream_http_failure 与归一后的历史
// framing_complete_neutral 都必须通过 cooldown_error 校验（B07 的 fence
// 约束仍由构造夹具满足）。否则 jobs 会把新终态当成 outcome_transition_mismatch
// 拒绝，账户永远停在冷却态。
func TestCooldownErrorHTTPFailureTerminalPassesProjectionValidation(t *testing.T) {
	observed := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for _, rawOutcome := range []string{exactkeyprobe.OutcomeHTTPFailure, legacyOutcomeFramingCompleteNeutral} {
		outcome := wgTransitionOutcome("cooldown_error", observed, true, "temporary_unavailable")
		outcome.Outcome = normalizeStoredOutcome(rawOutcome)
		outcome.ErrorCode = "cooldown_retest_http_failure_timeout"
		outcome.NextDueAt = nil
		validation := validateProjection(outcome)
		if validation.terminal {
			t.Fatalf("HTTP 失败 7 天终态必须通过投影校验（raw=%q）: disposition=%q reason=%q", rawOutcome, validation.disposition, validation.reason)
		}
	}
}
