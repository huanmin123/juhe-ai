package proberepo

// B1-4 回归：error/disabled 是 Key 的人工终态，失败写（RecordKeyFailure，
// 即 jobs 侧 RecordKeyScopedQuotaFailure 的落库入口）不得把 error 改写回
// rate_limited/temporary_unavailable；rate_limited 正常更新保持。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func TestB14RecordKeyFailurePreservesTerminalStatuses(t *testing.T) {
	h := openTestDBPostgres(t)
	key1, key2 := h.seedPoolAccount(t, "acc-b14")
	fp1 := h.store.FingerprintAPIKey(key1)
	fp2 := h.store.FingerprintAPIKey(key2)
	h.seedRuntimeState(t, "acc-b14", fp1, 0, "error", plusMillis(-1000))
	h.seedRuntimeState(t, "acc-b14", fp2, 1, "disabled", plusMillis(-1000))
	ctx := context.Background()

	assertNoop := func(t *testing.T, label, fingerprint, wantStatus string) {
		t.Helper()
		result, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
			AccountID: "acc-b14", KeyFingerprint: fingerprint, TrafficSource: "cooldown_retest",
			Status: "rate_limited", StatusCode: 429, QuotaRecoveryMode: "generic",
			ObservedAt: nowMillisText(),
		})
		if err != nil {
			t.Fatalf("%s: RecordKeyFailure 不应报错: %v", label, err)
		}
		if result.Changed {
			t.Fatalf("%s: 终态 Key 失败写必须 no-op: %+v", label, result)
		}
		var status string
		var failureCount int
		if err := h.db.QueryRow(`SELECT status, failure_count FROM juhe_business.account_api_key_runtime_states WHERE account_id=$1 AND key_fingerprint=$2`,
			"acc-b14", fingerprint).Scan(&status, &failureCount); err != nil {
			t.Fatalf("%s: 读取 runtime state: %v", label, err)
		}
		if status != wantStatus || failureCount != 0 {
			t.Fatalf("%s: 终态被改写 status=%s failure_count=%d", label, status, failureCount)
		}
	}
	assertNoop(t, "error", fp1, "error")
	assertNoop(t, "disabled", fp2, "disabled")

	// Expected.Status=error 与 success 写同口径短路。
	result, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-b14", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Status: "rate_limited", ObservedAt: nowMillisText(),
		Expected: accountquality.KeyMutationExpected{Status: "error"},
	})
	if err != nil || result.Changed {
		t.Fatalf("expected error 围栏必须短路: %+v err=%v", result, err)
	}

	// rate_limited 正常更新：状态保留为 rate_limited，失败计数 +1。
	h.exec(t, `UPDATE juhe_business.account_api_key_runtime_states SET status='rate_limited', last_attempt_at=$1 WHERE key_fingerprint=$2`,
		plusMillis(-1000), fp1)
	result, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-b14", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Status: "rate_limited", StatusCode: 429, ObservedAt: nowMillisText(),
	})
	if err != nil || !result.Changed {
		t.Fatalf("rate_limited 正常失败更新必须生效: %+v err=%v", result, err)
	}
	var status string
	var failureCount int
	if err := h.db.QueryRow(`SELECT status, failure_count FROM juhe_business.account_api_key_runtime_states WHERE key_fingerprint=$1`, fp1).
		Scan(&status, &failureCount); err != nil {
		t.Fatal(err)
	}
	if status != "rate_limited" || failureCount != 1 {
		t.Fatalf("rate_limited 更新后状态=%s failure_count=%d", status, failureCount)
	}
}
