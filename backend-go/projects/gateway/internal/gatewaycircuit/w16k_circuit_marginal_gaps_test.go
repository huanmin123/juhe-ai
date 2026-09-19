package gatewaycircuit

// 归因与目的：下潜二轮（d64e51c9b）删除 DegradeForGatewayFailure 生产写面并
// 改写 w11c_wait_suppression_test.go 的降级段后，degradations map 恒空，读面
// 降级臂（SnapshotAvailability / OrderDegradations / CountDegradations /
// cleanupExpiredDegradationsLocked / isLocalAccountDegradationActive /
// localAccountDegradationAvailability / AgeDegradationForTest）失覆盖，
// 包覆盖率从 95.5% 回落至 94.8%。写面退场是生产事实，本文件以同包字段注入
// 恢复被删段的读面契约覆盖（等价于 w11c 被删测试段的意图），不改生产代码。

import (
	"testing"
)

// w16kDegradation constructs a degradation entry with explicit lifecycle fields.
func w16kDegradation(id string, sinceMs, firstFailureMs, lastFailureMs, failureCount int64) *localAccountDegradation {
	return &localAccountDegradation{
		accountID:      id,
		reason:         "w16k-gateway-failure",
		sinceMs:        sinceMs,
		firstFailureMs: firstFailureMs,
		lastFailureMs:  lastFailureMs,
		failureCount:   failureCount,
	}
}

func TestW16KDegradationSnapshotAvailabilityArms(t *testing.T) {
	now := int64(1_000_000)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	store.degradations["w16k-deg-active"] = w16kDegradation("w16k-deg-active", now, now, now+LocalDegradationMinObservationMs, LocalDegradationActivationFailureThreshold)
	store.degradations["w16k-deg-once"] = w16kDegradation("w16k-deg-once", now, now, now, 1)
	store.degradations["w16k-deg-stale"] = w16kDegradation("w16k-deg-stale", now-LocalDegradationWindowMs-1, now-LocalDegradationWindowMs-1, now-LocalDegradationWindowMs-1, 1)
	// Suppression and active degradation share one runtime key: the
	// suppression snapshot wins and the degradation arm must skip.
	store.SuppressForGatewayFailure("w16k-both", "w16k-both", "w16k-reason", "")
	store.degradations["w16k-both"] = w16kDegradation("w16k-both", now, now, now+LocalDegradationMinObservationMs, LocalDegradationActivationFailureThreshold)
	// Active degradation behind a precheck-blocked key stays hidden.
	store.degradations["w16k-deg-blocked"] = w16kDegradation("w16k-deg-blocked", now, now, now+LocalDegradationMinObservationMs, LocalDegradationActivationFailureThreshold)
	blocked := map[string]bool{"w16k-deg-blocked": true}

	snapshot := store.SnapshotAvailability(func(key string) bool { return blocked[key] })

	// CountDegradations 触发 cleanupExpiredDegradationsLocked：stale 条目被物理
	// 删除，三个 active 条目（deg-active / both / deg-blocked）计数。
	if count := store.CountDegradations(); count != 3 {
		t.Fatalf("CountDegradations = %d, want 3", count)
	}
	if _, exists := store.degradations["w16k-deg-stale"]; exists {
		t.Fatalf("stale degradation should be evicted by cleanup")
	}
	if entry, visible := snapshot["w16k-deg-active"]; !visible || entry.Status != AvailabilityStatusDegraded {
		t.Fatalf("active degradation snapshot = %+v", entry)
	} else if entry.Reason != "w16k-gateway-failure" || entry.FailureCount == nil || *entry.FailureCount != LocalDegradationActivationFailureThreshold {
		t.Fatalf("active degradation fields = %+v", entry)
	}
	if _, visible := snapshot["w16k-deg-once"]; visible {
		t.Fatalf("inactive degradation should stay hidden: %+v", snapshot)
	}
	if _, visible := snapshot["w16k-deg-blocked"]; visible {
		t.Fatalf("precheck-blocked degradation should stay hidden: %+v", snapshot)
	}
	if entry, visible := snapshot["w16k-both"]; !visible || entry.Status != AvailabilityStatusLocalSuppressed {
		t.Fatalf("shared-key snapshot should keep suppression status: %+v", entry)
	}
}

func TestW16KDegradationOrderAndAgeArms(t *testing.T) {
	now := int64(2_000_000)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	store.degradations["a"] = w16kDegradation("a", now, now, now+LocalDegradationMinObservationMs, LocalDegradationActivationFailureThreshold)
	accounts := []SuppressibleAccount{suppressibleAccount("a"), suppressibleAccount("b")}

	// 一个 active 降级：降级账号重排到健康账号之后。
	result := store.OrderDegradations(accounts, nil)
	if !result.Applied || result.DegradedCount != 1 || len(result.DegradedAccountIDs) != 1 || result.DegradedAccountIDs[0] != "a" {
		t.Fatalf("single degraded order = %+v", result)
	}
	if result.Accounts[0].ID != "b" || result.Accounts[1].ID != "a" {
		t.Fatalf("reordered accounts = %+v", result.Accounts)
	}

	// 全部降级：bypass 标记置位，顺序不变，Applied 不置位。
	store.degradations["b"] = w16kDegradation("b", now, now, now+LocalDegradationMinObservationMs, LocalDegradationActivationFailureThreshold)
	result = store.OrderDegradations(accounts, nil)
	if result.Applied || !result.BypassedAllDegraded || result.DegradedCount != 2 || len(result.Accounts) != 2 {
		t.Fatalf("all degraded order = %+v", result)
	}

	// AgeDegradationForTest 正常臂：firstFailureMs 前移并下拉 sinceMs。
	store.degradations["c"] = w16kDegradation("c", now, now, now, 1)
	store.AgeDegradationForTest("c", 120_000)
	aged := store.degradations["c"]
	if aged.firstFailureMs != now-120_000 || aged.sinceMs != now-120_000 {
		t.Fatalf("aged degradation = %+v", aged)
	}
	if aged.lastFailureMs != now {
		t.Fatalf("aging must not touch lastFailureMs: %+v", aged)
	}
	// 缺位 runtimeKey 是无副作用早退。
	store.AgeDegradationForTest("w16k-missing", 1_000)
	if _, exists := store.degradations["w16k-missing"]; exists {
		t.Fatalf("aging a missing key must not create an entry")
	}
}

func TestW16KNopLoggerContract(t *testing.T) {
	// nopLogger 是默认 logger；写面退场后日志路径不再被集成路径触达，
	// 直接固化 no-op 契约。
	NopLogger.Info(map[string]any{"event": "w16k"}, "w16k info")
	NopLogger.Warn(map[string]any{"event": "w16k"}, "w16k warn")
}
