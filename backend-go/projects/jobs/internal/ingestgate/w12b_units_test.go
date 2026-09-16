package ingestgate

// w12b 波次分支覆盖测试：safeCreatedBeforeForPendingBacklog 的 epoch 钳制臂，
// 以及经 Check 的端到端积压回退。
//
// 不可达语句登记（无法通过任何输入触达）：
//   - ingestgate.go Check 中 safeCreatedBeforeForPendingBacklog 的 err 臂：
//     oldest 串已在 oldestPendingUsageRecordCreatedAt/oldestIso 中经同一
//     normalizeIsoTime 校验，到达该调用时不可能再报错。

import (
	"context"
	"testing"
	"time"
)

func TestW12bEpochBacklogClampAndCheckEndToEnd(t *testing.T) {
	// epoch 0 的积压：oldestMs-1 为负，按 Node Math.max(0, oldest-1) 钳制为 0。
	got, err := safeCreatedBeforeForPendingBacklog("2026-09-17T00:00:00.000Z", "1970-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("epoch 积压回退: %v", err)
	}
	if got != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("应钳制为 epoch 0: %q", got)
	}

	// Check 端到端：无 flush 失败、存在 epoch 积压 → 放行并回退到 epoch。
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueOldestCreatedAt: "1970-01-01T00:00:00Z"}, nil
	}
	safety, err := Check(context.Background(), probe, time.Unix(1_789_000_000, 0).UTC())
	if err != nil {
		t.Fatalf("flush 失败但无积压应放行: %v", err)
	}
	if safety.SafeCreatedBefore != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("安全截止时间应回退到 epoch 前 1ms 的钳制值: %q", safety.SafeCreatedBefore)
	}
}
