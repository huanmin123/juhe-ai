package taskruns

import (
	"strings"
	"testing"
	"time"
)

// w12b_units_test.go 纯函数分支收尾：LeaseFence.Fence、NormalizeReconcileLimit
// 上限截断、NormalizeStatus 未知态回退、requiredText 空值与超长、
// formatTimeBase36 零值。newID 的 rand.Read 失败 panic 属不可达（见
// w12b_failinject_test.go 头注释登记）。

func TestW12bLeaseFenceIdentityViews(t *testing.T) {
	fence := LeaseFence{LeaseKey: "w12b-k", OwnerID: "o", FencingToken: 7}
	if got := fence.Fence(); got != fence {
		t.Fatalf("LeaseFence.Fence 应返回自身: %+v", got)
	}
	identity := LeaseIdentity{LeaseKey: "w12b-k", OwnerID: "o", FencingToken: 7}
	if got := identity.Fence(); got != fence {
		t.Fatalf("LeaseIdentity.Fence 视图不符: %+v", got)
	}
}

func TestW12bNormalizeReconcileLimitBounds(t *testing.T) {
	if got := NormalizeReconcileLimit(0); got != ReconcileDefaultLimit {
		t.Fatalf("非正数应回落默认: %d", got)
	}
	if got := NormalizeReconcileLimit(-5); got != ReconcileDefaultLimit {
		t.Fatalf("负数应回落默认: %d", got)
	}
	if got := NormalizeReconcileLimit(ReconcileMaxLimit + 1); got != ReconcileMaxLimit {
		t.Fatalf("超上限应截断: %d", got)
	}
	if got := NormalizeReconcileLimit(42); got != 42 {
		t.Fatalf("合法值应透传: %d", got)
	}
}

func TestW12bNormalizeStatusFallback(t *testing.T) {
	for _, valid := range []TaskRunStatus{StatusQueued, StatusRunning, StatusCompleted, StatusFailed, StatusSkipped} {
		if got := NormalizeStatus(string(valid)); got != valid {
			t.Fatalf("合法状态应透传: %s -> %s", valid, got)
		}
	}
	if got := NormalizeStatus("w12b-unknown"); got != StatusFailed {
		t.Fatalf("未知状态应视为 failed: %s", got)
	}
}

func TestW12bRequiredTextValidation(t *testing.T) {
	if _, err := requiredText("   ", "leaseKey"); err == nil || !strings.Contains(err.Error(), "leaseKey") {
		t.Fatalf("空白应报错并带字段名: %v", err)
	}
	long := strings.Repeat("x", 513)
	if _, err := requiredText(long, "ownerId"); err == nil || !strings.Contains(err.Error(), "512") {
		t.Fatalf("超长应报错: %v", err)
	}
	trimmed, err := requiredText("  w12b-value  ", "shardKey")
	if err != nil || trimmed != "w12b-value" {
		t.Fatalf("合法值应去空白: %q %v", trimmed, err)
	}
	boundary := strings.Repeat("x", 512)
	if _, err := requiredText(boundary, "runId"); err != nil {
		t.Fatalf("512 边界应放行: %v", err)
	}
}

func TestW12bFormatTimeBase36Zero(t *testing.T) {
	if got := formatTimeBase36(time.UnixMilli(0)); got != "0" {
		t.Fatalf("零毫秒应为 \"0\": %q", got)
	}
	// 对照一个已知值：1000ms → base36 "rs"（27*36+28）。
	if got := formatTimeBase36(time.UnixMilli(1000)); got != "rs" {
		t.Fatalf("1000ms 应为 \"rs\": %q", got)
	}
}

func TestW12bTemporaryTaskLeaseKeyShape(t *testing.T) {
	got := TemporaryTaskLeaseKey("w12b-run-1")
	if got != TemporaryMaintenanceWorkerRole+":w12b-run-1" {
		t.Fatalf("lease key 形态不符: %s", got)
	}
	if got := ScheduledLeaseKey("j", "s"); got != "scheduled:j:s" {
		t.Fatalf("scheduled key 形态不符: %s", got)
	}
}
