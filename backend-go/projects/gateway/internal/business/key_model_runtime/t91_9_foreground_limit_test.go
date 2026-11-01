package keymodelruntime

// T91-9 / B3-H31 钉住测试：J3b 的 key-model 前台准入恢复主链固定契约——
// 同一精确 CapabilityKey 最多 2 个未达到 precommit 的并发请求，第 3 个返回
// busy；不同 CapabilityKey 互不影响；Redis Lua 与 memory 实现同值。

import (
	"context"
	"testing"
	"time"
)

// TestT91ForegroundLimitMatchesMainChainContract 钉住固定契约值 2（与
// gatewayaccounteffects.KeyModelForegroundLimit 同源）。
func TestT91ForegroundLimitMatchesMainChainContract(t *testing.T) {
	if ForegroundLimit != 2 {
		t.Fatalf("ForegroundLimit = %d, want 2（主链 KeyModelForegroundLimit 固定契约）", ForegroundLimit)
	}
}

// TestT91MemoryForegroundLimitRejectsThirdConcurrent 同一 CapabilityKey 前两个
// 并发准入成功、第三个 busy；不同 Key 的 CapabilityKey 不受影响。
func TestT91MemoryForegroundLimitRejectsThirdConcurrent(t *testing.T) {
	store := NewMemoryStore()
	now := time.UnixMilli(1_000).UTC()
	capability := testCapability()
	for _, attemptID := range []string{"attempt-1", "attempt-2"} {
		decision, _, _, err := store.AdmitForeground(capability, attemptID, now)
		if err != nil || decision != ForegroundAdmitted {
			t.Fatalf("%s admit = %s err=%v", attemptID, decision, err)
		}
	}
	decision, _, _, err := store.AdmitForeground(capability, "attempt-3", now)
	if err != nil || decision != ForegroundBusy {
		t.Fatalf("第 3 个并发准入必须 busy = %s err=%v", decision, err)
	}
	// 不同 Key（KeyFingerprint 不同）是不同 CapabilityKey，不受 2 的约束。
	otherKey := testCapability()
	otherKey.KeyFingerprint = "key-other"
	decision, permit, _, err := store.AdmitForeground(otherKey, "attempt-other", now)
	if err != nil || decision != ForegroundAdmitted {
		t.Fatalf("不同 Key 必须放行 = %s err=%v", decision, err)
	}
	if permit.CapabilityHash == "" {
		t.Fatal("permit 必须带 capability hash")
	}
}

// TestT91RedisForegroundLimitRejectsThirdConcurrent Redis Lua 与 memory 同值：
// Lua 收到的 limit 参数来自 ForegroundLimit（redis.go AdmitForeground），第 3
// 个并发准入同样 busy。
func TestT91RedisForegroundLimitRejectsThirdConcurrent(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	capability := testCapability()
	for _, attemptID := range []string{"attempt-1", "attempt-2"} {
		decision, _, _, err := store.AdmitForeground(ctx, capability, attemptID)
		if err != nil || decision != ForegroundAdmitted {
			t.Fatalf("%s admit = %s err=%v", attemptID, decision, err)
		}
	}
	decision, _, _, err := store.AdmitForeground(ctx, capability, "attempt-3")
	if err != nil || decision != ForegroundBusy {
		t.Fatalf("Redis 第 3 个并发准入必须 busy = %s err=%v", decision, err)
	}
	// 不同 Key 不受影响。
	otherKey := testCapability()
	otherKey.KeyFingerprint = "key-other"
	decision, _, _, err = store.AdmitForeground(ctx, otherKey, "attempt-other")
	if err != nil || decision != ForegroundAdmitted {
		t.Fatalf("Redis 不同 Key 必须放行 = %s err=%v", decision, err)
	}
}
