package accounts

// PATCH 调度标记互斥回归：超级优先与降级备用的互斥按"终值"判定（请求未
// 携带的字段并入行内现值后再判，m11_authorized_dispatch 同款语义）。历史
// 实现只比双输入同真，行内已开 A、请求单开 B 的单字段 PATCH（列表菜单
// "超级优先/降级备用"通道）会漏判并落库双开。

import (
	"context"
	"strings"
	"testing"
)

// mutexSeed 建 owner 账户并把行内两个调度标记置为给定值（绕过创建入参，
// 模拟历史双开残留或既有单开）。
func mutexSeed(t *testing.T, env *testEnv, accountID string, super, fallback int) {
	t.Helper()
	env.seedAccount(t, accountID, "owner-1", "互斥-"+accountID, "active")
	env.seedProviderAndDefaultGroup(t, "owner-1")
	if super != 0 {
		env.exec(t, `UPDATE accounts SET super_priority_enabled = 1 WHERE id = ?`, accountID)
	}
	if fallback != 0 {
		env.exec(t, `UPDATE accounts SET fallback_enabled = 1 WHERE id = ?`, accountID)
	}
}

func mutexFlags(t *testing.T, env *testEnv, accountID string) (super, fallback string) {
	t.Helper()
	super = env.queryCell(t, `SELECT super_priority_enabled FROM accounts WHERE id = ?`, accountID)
	fallback = env.queryCell(t, `SELECT fallback_enabled FROM accounts WHERE id = ?`, accountID)
	return
}

func TestPatchSuperPriorityRejectedWhenFallbackAlreadyOn(t *testing.T) {
	env := newTestEnv(t)
	mutexSeed(t, env, "acc-mtx-1", 0, 1)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	_, err := env.store.Patch(ctx, "acc-mtx-1", PatchInput{
		ExpectedConfigRevision: 1,
		SuperPriorityEnabled:   boolPtr(true),
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "超级优先和降级备用不能同时开启") {
		t.Fatalf("期望互斥校验拒绝，got %v", err)
	}
	if super, fallback := mutexFlags(t, env, "acc-mtx-1"); super != "0" || fallback != "1" {
		t.Fatalf("校验失败不得改写行内标记，got super=%s fallback=%s", super, fallback)
	}
}

func TestPatchFallbackRejectedWhenSuperPriorityAlreadyOn(t *testing.T) {
	env := newTestEnv(t)
	mutexSeed(t, env, "acc-mtx-2", 1, 0)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	_, err := env.store.Patch(ctx, "acc-mtx-2", PatchInput{
		ExpectedConfigRevision: 1,
		FallbackEnabled:        boolPtr(true),
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "超级优先和降级备用不能同时开启") {
		t.Fatalf("期望互斥校验拒绝，got %v", err)
	}
	if super, fallback := mutexFlags(t, env, "acc-mtx-2"); super != "1" || fallback != "0" {
		t.Fatalf("校验失败不得改写行内标记，got super=%s fallback=%s", super, fallback)
	}
}

func TestPatchBothFlagsExplicitTrueRejected(t *testing.T) {
	env := newTestEnv(t)
	mutexSeed(t, env, "acc-mtx-3", 0, 0)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	_, err := env.store.Patch(ctx, "acc-mtx-3", PatchInput{
		ExpectedConfigRevision: 1,
		SuperPriorityEnabled:   boolPtr(true),
		FallbackEnabled:        boolPtr(true),
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "超级优先和降级备用不能同时开启") {
		t.Fatalf("期望双显式真被拒绝，got %v", err)
	}
}

// 历史双开残留的自愈通道：取消其一必须放行（取消后终值单真）。
func TestPatchDirtyBothOnRowCanClearOneFlag(t *testing.T) {
	env := newTestEnv(t)
	mutexSeed(t, env, "acc-mtx-4", 1, 1)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	if _, err := env.store.Patch(ctx, "acc-mtx-4", PatchInput{
		ExpectedConfigRevision: 1,
		FallbackEnabled:        boolPtr(false),
	}, scope); err != nil {
		t.Fatalf("取消降级备用应放行，got %v", err)
	}
	if super, fallback := mutexFlags(t, env, "acc-mtx-4"); super != "1" || fallback != "0" {
		t.Fatalf("取消后应只剩超级优先，got super=%s fallback=%s", super, fallback)
	}
}
