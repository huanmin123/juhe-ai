package main

// w1: chain_dispatch.go 适配器收割——显式降级 stub（时延/桶健康/热度/来源
// 规避排序直通）、进程内账户并发存储（获取/释放/泳道）、client-IP 避让
// 适配器与键投影纯函数。

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestW1DegradedPortStubs(t *testing.T) {
	accounts := []gatewaydispatch.AccountCandidate{{ID: "a"}, {ID: "b"}}
	// 时延降级缺席：顺序原样。
	latencyOrder, err := (&degradedLatency{}).OrderAsync(context.Background(), accounts, nil, nil, nil)
	if err != nil || len(latencyOrder.Accounts) != 2 || latencyOrder.Accounts[0].ID != "a" {
		t.Fatalf("latency = %+v, %v", latencyOrder, err)
	}
	// 桶健康缺席：顺序原样 + 失败记录恒 nil。
	proxyOrder, err := (&degradedProxyHealth{}).OrderAsync(context.Background(), accounts, nil)
	if err != nil || len(proxyOrder.Accounts) != 2 {
		t.Fatalf("proxy = %+v, %v", proxyOrder, err)
	}
	if err := (&degradedProxyHealth{}).RecordFailureAsync(context.Background(), accounts[0], "text"); err != nil {
		t.Fatalf("record = %v", err)
	}
	// 热度质量缺席：输入账户原样返回。
	hotOrder, err := (&degradedHotQuality{}).OrderAsync(context.Background(), gatewaydispatch.HotQualityOrderInput{Accounts: accounts})
	if err != nil || len(hotOrder.Accounts) != 2 {
		t.Fatalf("hot = %+v, %v", hotOrder, err)
	}
	// 客户端来源规避缺席：直通。
	avoidOrder, err := (&degradedClientSourceAvoidance{}).OrderAsync(context.Background(), accounts, gatewaypreauth.ClientStrategyContext{}, nil)
	if err != nil || len(avoidOrder.Accounts) != 2 {
		t.Fatalf("avoid = %+v, %v", avoidOrder, err)
	}
}

func TestW1ChainConcurrencyStoreLifecycle(t *testing.T) {
	store := newChainConcurrencyStore(gatewayclientip.NewMemoryAccountConcurrency(nil))
	ctx := context.Background()
	// 初始并发为 0。
	current, err := store.LoadCurrentAsync(ctx, []string{"acc_1"})
	if err != nil || current["acc_1"] != 0 {
		t.Fatalf("load = %v, %v", current, err)
	}
	laneCurrent, err := store.LoadCurrentByLaneAsync(ctx, []string{"acc_1"}, "image")
	if err != nil || laneCurrent["acc_1"] != 0 {
		t.Fatalf("lane load = %v, %v", laneCurrent, err)
	}
	// 获取：低于限额成功，当前数 +1，释放闭包回滚。
	slot, err := store.TryAcquireAsync(ctx, "acc_1", 2, gatewaydispatch.AccountConcurrencyAcquireOptions{})
	if err != nil || !slot.Acquired || slot.Current != 1 || slot.Limit != 2 {
		t.Fatalf("slot = %+v, %v", slot, err)
	}
	// 达到限额：不再获取（Acquired=false），槽位字段仍带观测值。
	_, _ = store.TryAcquireAsync(ctx, "acc_1", 2, gatewaydispatch.AccountConcurrencyAcquireOptions{})
	full, err := store.TryAcquireAsync(ctx, "acc_1", 2, gatewaydispatch.AccountConcurrencyAcquireOptions{})
	if err != nil || full.Acquired {
		t.Fatalf("full slot = %+v, %v", full, err)
	}
	if full.LaneLimit != 0 && full.LaneLimit != 2 {
		t.Fatalf("laneLimit = %d", full.LaneLimit)
	}
	// 释放后可再次获取。
	slot.Release()
	retry, err := store.TryAcquireAsync(ctx, "acc_1", 2, gatewaydispatch.AccountConcurrencyAcquireOptions{})
	if err != nil || !retry.Acquired {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	// 泳道限额：laneLimit 收紧时按泳道判断。
	laneSlot, err := store.TryAcquireAsync(ctx, "acc_2", 5, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "image"})
	if err != nil || !laneSlot.Acquired || laneSlot.Lane != "image" {
		t.Fatalf("lane slot = %+v, %v", laneSlot, err)
	}
}

func TestW1ChainClientIPAvoidanceOrder(t *testing.T) {
	ctx := context.Background()
	accounts := []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	// 回避存储缺席：原样返回。
	if order, err := newChainClientIPAvoidance(nil).OrderAsync(ctx, accounts, gatewaydispatch.ClientIPAvoidanceScope{}, nil); err != nil || len(order.Accounts) != 2 {
		t.Fatalf("nil avoidance = %+v, %v", order, err)
	}
	if order, err := newChainClientIPAvoidance(nil).OrderAsync(ctx, nil, gatewaydispatch.ClientIPAvoidanceScope{}, nil); err != nil || len(order.Accounts) != 0 {
		t.Fatalf("empty accounts = %+v, %v", order, err)
	}
	// 内存回避：无回避记录时保持调度顺序。
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{})
	if err != nil {
		t.Fatalf("NewAvoidance = %v", err)
	}
	defer avoidance.Close()
	order, err := newChainClientIPAvoidance(avoidance).OrderAsync(ctx, accounts, gatewaydispatch.ClientIPAvoidanceScope{
		SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "grp_1", ClientIP: "203.0.113.5",
	}, nil)
	if err != nil || len(order.Accounts) != 2 || order.Accounts[0].ID != "acc_1" {
		t.Fatalf("order = %+v, %v", order, err)
	}
}

func TestW1DispatchKeyProjectionHelpers(t *testing.T) {
	// 持久化变更上下文：空 map → nil；非法 JSON → nil；合法 → 解码。
	if chainMutationContextOf(nil) != nil || chainMutationContextOf(map[string]any{}) != nil {
		t.Fatal("空 map 必须 nil")
	}
	if chainMutationContextOf(map[string]any{"quotaRecoveryMode": 1}) != nil {
		t.Fatal("非法载荷必须 nil")
	}
	context := chainMutationContextOf(map[string]any{"quotaRecoveryMode": "daily_reset"})
	if context == nil || context.QuotaRecoveryMode != "daily_reset" {
		t.Fatalf("context = %+v", context)
	}
	if got := chainQuotaRecoveryModeOf(map[string]any{}); got != "" {
		t.Fatalf("empty mode = %q", got)
	}
	if got := chainQuotaRecoveryModeOf(map[string]any{"quotaRecoveryMode": "duration"}); got != "duration" {
		t.Fatalf("mode = %q", got)
	}
	// 观察纪元：空 / 非法 / 合法。
	if chainObservationEpochPtrOf("") != nil || chainObservationEpochPtrOf("abc") != nil {
		t.Fatal("空与非法必须 nil")
	}
	if got := chainObservationEpochPtrOf(" 42 "); got == nil || *got != 42 {
		t.Fatalf("epoch = %v", got)
	}
	// 空串指针助手。
	if nilStringFrom("") != nil || nilStringFrom("  ") != nil || nilStringPtr("") != nil {
		t.Fatal("空白必须 nil")
	}
	if got := nilStringFrom("x"); got == nil || *got != "x" {
		t.Fatalf("nilStringFrom = %v", got)
	}
	// 解引用助手。
	if derefStringValue(nil) != "" || derefInt64Ptr2(nil) != 0 {
		t.Fatal("nil 解引用必须零值")
	}
	value := "v"
	if derefStringValue(&value) != "v" {
		t.Fatal("解引用必须取值")
	}
	number := int64(9)
	if derefInt64Ptr2(&number) != 9 {
		t.Fatal("解引用必须取值")
	}
	// 避让候选投影：字段逐一映射。
	binding := "owner_1"
	projected := chainAvoidanceAccountsOf([]gatewaydispatch.AccountCandidate{{
		ID: "acc_1", AccountAccessType: "account_authorized", BindingSystemAccountID: &binding,
	}})
	if len(projected) != 1 || projected[0].ID != "acc_1" || projected[0].BindingSystemAccountID != "owner_1" {
		t.Fatalf("projected = %+v", projected)
	}
	// once 告警：重复调用只记一次（内部 map 去重），断言仅要求不 panic。
	slogOnceWarn("w1-test-port", "测试效果")
	slogOnceWarn("w1-test-port", "测试效果")
	_ = errors.New
}
