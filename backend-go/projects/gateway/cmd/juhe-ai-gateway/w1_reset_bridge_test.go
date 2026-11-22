package main

// w1: accountsRuntimeResetBridge 的纯投影直测（fixture 内存库）。
//（authorizationLimitsJSON / teamGrantLimitsJSON / authorizationInstanceIDs
// DB 读取面随授权额度域退役删除，额度读取用例一并摘除。）

import (
	"context"
	"testing"
)

func newW1ResetBridge(t *testing.T) *accountsRuntimeResetBridge {
	t.Helper()
	fixture := newChainFixture(t)
	//（桥已无 now 字段：额度门时钟随授权额度域退役删除。）
	return &accountsRuntimeResetBridge{db: fixture.db}
}

func TestW1ResetBridgeGenerationsAndGuards(t *testing.T) {
	bridge := newW1ResetBridge(t)
	// 未记住：空串。
	if got := bridge.rememberedTransientGeneration("acc_1", "fp_1"); got != "" {
		t.Fatalf("未记住 = %q", got)
	}
	bridge.rememberTransientGeneration("acc_1", "fp_1", "gen-1")
	if got := bridge.rememberedTransientGeneration("acc_1", "fp_1"); got != "gen-1" {
		t.Fatalf("记住 = %q", got)
	}
	// guardAccount：指纹 + 代际指针。
	account := bridge.guardAccount("acc_2", "fp_2", "")
	if account.ID != "acc_2" || account.SelectedAPIKeyFingerprint == nil || *account.SelectedAPIKeyFingerprint != "fp_2" {
		t.Fatalf("guard account = %+v", account)
	}
	if account.SelectedAPIKeyTransientGeneration != nil {
		t.Fatal("无代际不得有指针")
	}
	withGen := bridge.guardAccount("acc_3", "fp_3", "gen-9")
	if withGen.SelectedAPIKeyTransientGeneration == nil || *withGen.SelectedAPIKeyTransientGeneration != "gen-9" {
		t.Fatalf("代际 = %v", withGen.SelectedAPIKeyTransientGeneration)
	}
	// 表限定与占位符（PG 单形态）。
	if bridge.table("accounts") != "juhe_business.accounts" {
		t.Fatal("表前缀缺失")
	}
	if got := bridge.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("bind = %q", got)
	}
	// ClearAPIKeyTransientFailure：nil 代际 false；未记住 false。
	ctx := context.Background()
	ok, err := bridge.ClearAPIKeyTransientFailure(ctx, "acc_x", "fp_x", nil)
	if err != nil || ok {
		t.Fatalf("nil 代际 = %v, %v", ok, err)
	}
	generation := int64(7)
	if ok, err := bridge.ClearAPIKeyTransientFailure(ctx, "acc_y", "fp_y", &generation); err != nil || ok {
		t.Fatalf("未记住 = %v, %v", ok, err)
	}
}
