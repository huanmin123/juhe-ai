package accountkeystates

// B1-4 回归：Key 级 error 是人工恢复终态，被动失败写（RecordFailure，含
// chainErrorPolicyEffects.RecordKeyScopedQuotaFailure 的委托路径）不得把
// error 改写成 rate_limited/temporary_unavailable；disabled 同样不可写；
// rate_limited 的正常失败更新不受影响。

import (
	"context"
	"testing"
)

func TestB14RecordFailurePreservesKeyTerminalStates(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "b14-acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	first := w9dPoolAccount("b14-acc", fps)
	second := first
	second.SelectedAPIKeyFingerprint = fps[1]
	second.SelectedAPIKeyIndex = 1
	second.APIKey = "sk-test-b14-acc-b"
	ctx := context.Background()

	h.seedState(t, "b14-acc", fps[0], map[string]any{
		"status": "error", "last_attempt_at": plusMillis(-1000), "updated_at": plusMillis(-1000),
	})
	h.seedState(t, "b14-acc", fps[1], map[string]any{
		"status": "disabled", "last_attempt_at": plusMillis(-1000), "updated_at": plusMillis(-1000),
	})
	assertTerminal := func(t *testing.T, label, fingerprint, wantStatus string, account TargetInput) {
		t.Helper()
		result, err := h.store.RecordFailure(ctx, FailureInput{
			Account: account, Status: "rate_limited", StatusCode: 429,
			ErrorCode: QuotaRecoveryGenericErrorCode, QuotaRecoveryMode: "generic",
		})
		if err != nil {
			t.Fatalf("%s: RecordFailure 不应报错: %v", label, err)
		}
		if result.Changed {
			t.Fatalf("%s: 终态 Key 失败写必须 no-op: %+v", label, result)
		}
		var status string
		var failureCount int
		if err := h.db.QueryRow(`SELECT status, failure_count FROM account_api_key_runtime_states WHERE account_id=? AND key_fingerprint=?`,
			"b14-acc", fingerprint).Scan(&status, &failureCount); err != nil {
			t.Fatalf("%s: 读取 runtime state: %v", label, err)
		}
		if status != wantStatus || failureCount != 0 {
			t.Fatalf("%s: 终态被改写 status=%s failure_count=%d", label, status, failureCount)
		}
	}
	assertTerminal(t, "error", fps[0], "error", first)
	assertTerminal(t, "disabled", fps[1], "disabled", second)

	// 期望围栏声明 error 时也必须短路为 manual_restore_required（与成功写同口径）。
	result, err := h.store.RecordFailure(ctx, FailureInput{
		Account: first, Status: "rate_limited",
		Expected: ExpectedProbeState{Status: "error"},
	})
	if err != nil || result.Changed || result.SkippedReason != "manual_restore_required" {
		t.Fatalf("expected error 围栏必须 manual_restore_required: %+v err=%v", result, err)
	}

	// rate_limited 是普通可写状态：正常失败更新仍然生效。
	h.exec(t, `UPDATE account_api_key_runtime_states SET status='rate_limited' WHERE key_fingerprint=?`, fps[0])
	h.advance(1)
	result, err = h.store.RecordFailure(ctx, FailureInput{
		Account: first, Status: "rate_limited", StatusCode: 429,
	})
	if err != nil || !result.Changed {
		t.Fatalf("rate_limited 正常失败更新必须生效: %+v err=%v", result, err)
	}
	var status string
	var failureCount int
	if err := h.db.QueryRow(`SELECT status, failure_count FROM account_api_key_runtime_states WHERE key_fingerprint=?`, fps[0]).
		Scan(&status, &failureCount); err != nil {
		t.Fatal(err)
	}
	if status != "rate_limited" || failureCount != 1 {
		t.Fatalf("rate_limited 更新后状态=%s failure_count=%d", status, failureCount)
	}
}

// TestB14RecordFailureSkipsWhenExpectedError 覆盖 gateway 侧 RecordFailure 在
// Expected.Status=error 时的早退（RecordKeyScopedQuotaFailure 复用同一入口）。
func TestB14RecordFailureSkipsWhenExpectedError(t *testing.T) {
	h := openTestDB(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "b14-short", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	result, err := h.store.RecordFailure(context.Background(), FailureInput{
		Account: w9dPoolAccount("b14-short", fps), Status: "rate_limited",
		Expected: ExpectedProbeState{Status: "error"},
	})
	if err != nil || result.Changed || result.SkippedReason != "manual_restore_required" {
		t.Fatalf("error 期望围栏必须短路: %+v err=%v", result, err)
	}
}
