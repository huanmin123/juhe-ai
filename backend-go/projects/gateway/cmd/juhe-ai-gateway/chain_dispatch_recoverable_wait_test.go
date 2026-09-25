package main

// dispatch 引擎 RecoverableWait 薄适配器（chainDispatchRecoverableWait）的
// 行为测试：引擎等待分支（upstreamdispatch.go 消费契约）此前生产无赋值，
// 本适配器把 G11 PreAuthRecoverableWait 接到 engine.RecoverableWait。
// 引擎侧等待分支的既有单测（gatewaydispatch engineerrorpaths /
// enginetailpaths / w13g3 的 exhaustedWaiter / readyWaiter / hookWaiter mock）
// 已覆盖 exhausted→503、ready→继续派发、被唤醒→重排路径；这里覆盖适配层
// 本身：ready 立即返回、超时回传最后采样状态（不视为错误）、coordinator
// 唤醒后读到恢复状态、Refresh 错误透传。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

func w17WaitAdapter(t *testing.T, options gatewaycircuit.WaitEngineOptions) (*chainDispatchRecoverableWait, *gatewaycircuit.WaitCoordinator) {
	t.Helper()
	coordinator := gatewaycircuit.NewWaitCoordinator(gatewaycircuit.WaitCoordinatorOptions{})
	wait := gatewaycircuit.NewPreAuthRecoverableWait(coordinator, w17NopWaitLogger{})
	// PreAuthRecoverableWait.Options 直接挂在实例上（构造器不收 options）。
	wait.Options = options
	return &chainDispatchRecoverableWait{wait: wait}, coordinator
}

func w17WaitInput(
	scopeKey string,
	refresh func(context.Context) (gatewaydispatch.SuppressionFilterResult, error),
	ready func(gatewaydispatch.SuppressionFilterResult) bool,
	retryAfter func(gatewaydispatch.SuppressionFilterResult) *int64,
	maxWaitMs int64,
) gatewaydispatch.SuppressionWaitInput {
	// 与生产消费面一致（upstreamdispatch.go）：DeadlineAtMs 取
	// ServerRetryBudget.DeadlineAtMs（起点同 RequestStartedAtMs，即
	// started+预算），适配器把 0 也当真实截止透传（与 preauth 适配器同
	// 契约），测试必须显式给值。
	startedAtMs := time.Now().UnixMilli()
	return gatewaydispatch.SuppressionWaitInput{
		ScopeKey:           scopeKey,
		Reason:             "recoverable_later",
		Refresh:            refresh,
		IsReady:            ready,
		NextRetryAfterMs:   retryAfter,
		MaxWaitMs:          maxWaitMs,
		RequestStartedAtMs: startedAtMs,
		DeadlineAtMs:       startedAtMs + maxWaitMs,
	}
}

// TestChainDispatchRecoverableWaitReadyImmediately: 初始采样即 ready → 不进
// 等待循环，回传该状态。
func TestChainDispatchRecoverableWaitReadyImmediately(t *testing.T) {
	adapter, _ := w17WaitAdapter(t, gatewaycircuit.WaitEngineOptions{})
	readyState := gatewaydispatch.SuppressionFilterResult{Accounts: []gatewaydispatch.AccountCandidate{w17Account("acc-a")}}
	input := w17WaitInput("aff_w17_wait_ready",
		func(context.Context) (gatewaydispatch.SuppressionFilterResult, error) { return readyState, nil },
		func(state gatewaydispatch.SuppressionFilterResult) bool { return !state.AllSuppressed },
		func(gatewaydispatch.SuppressionFilterResult) *int64 { return nil },
		5_000)
	state, err := adapter.WaitForState(context.Background(), input)
	if err != nil {
		t.Fatalf("WaitForState: %v", err)
	}
	if state.AllSuppressed || len(state.Accounts) != 1 || state.Accounts[0].ID != "acc-a" {
		t.Fatalf("state = %#v", state)
	}
}

// TestChainDispatchRecoverableWaitTimeoutKeepsLastState: 恒不 ready → 超时
// skipped 不视为错误，回传最后一次采样状态（消费侧据此走耗尽退出）。
func TestChainDispatchRecoverableWaitTimeoutKeepsLastState(t *testing.T) {
	adapter, _ := w17WaitAdapter(t, gatewaycircuit.WaitEngineOptions{CheckIntervalMs: 20})
	retryAfter := int64(5)
	input := w17WaitInput("aff_w17_wait_timeout",
		func(context.Context) (gatewaydispatch.SuppressionFilterResult, error) {
			return gatewaydispatch.SuppressionFilterResult{AllSuppressed: true, NextRetryAfterMs: &retryAfter}, nil
		},
		func(state gatewaydispatch.SuppressionFilterResult) bool { return !state.AllSuppressed },
		func(state gatewaydispatch.SuppressionFilterResult) *int64 { return state.NextRetryAfterMs },
		80)
	state, err := adapter.WaitForState(context.Background(), input)
	if err != nil {
		t.Fatalf("超时不能作为错误返回: %v", err)
	}
	if !state.AllSuppressed || state.NextRetryAfterMs == nil || *state.NextRetryAfterMs != 5 {
		t.Fatalf("state = %#v", state)
	}
}

// TestChainDispatchRecoverableWaitWakesOnRecovery: 恢复后 coordinator 唤醒
// → refresh 读到 ready 状态并返回（等待恢复能力）。
func TestChainDispatchRecoverableWaitWakesOnRecovery(t *testing.T) {
	adapter, coordinator := w17WaitAdapter(t, gatewaycircuit.WaitEngineOptions{CheckIntervalMs: 5_000})
	var calls atomic.Int32
	scopeKey := "aff_w17_wait_wake"
	refresh := func(context.Context) (gatewaydispatch.SuppressionFilterResult, error) {
		if calls.Add(1) <= 2 {
			return gatewaydispatch.SuppressionFilterResult{AllSuppressed: true}, nil
		}
		return gatewaydispatch.SuppressionFilterResult{Accounts: []gatewaydispatch.AccountCandidate{w17Account("acc-a")}}, nil
	}
	input := w17WaitInput(scopeKey, refresh,
		func(state gatewaydispatch.SuppressionFilterResult) bool { return !state.AllSuppressed },
		func(gatewaydispatch.SuppressionFilterResult) *int64 { return nil },
		3_000)
	done := make(chan struct{})
	var (
		state gatewaydispatch.SuppressionFilterResult
		err   error
	)
	go func() {
		defer close(done)
		state, err = adapter.WaitForState(context.Background(), input)
	}()
	// 引擎每个等待轮次消费一次唤醒（settle 后 waiter 出队、重新注册），
	// 所以持续唤醒直到等待返回，而不是命中一次即停。
wakeLoop:
	for {
		select {
		case <-done:
			break wakeLoop
		default:
		}
		coordinator.NotifyOne(scopeKey, "recoverable_later")
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("等待未在唤醒后返回")
	}
	if err != nil {
		t.Fatalf("WaitForState: %v", err)
	}
	if state.AllSuppressed || len(state.Accounts) != 1 {
		t.Fatalf("唤醒后 state = %#v", state)
	}
	if calls.Load() < 3 {
		t.Fatalf("唤醒后必须重新采样, calls = %d", calls.Load())
	}
}

// TestChainDispatchRecoverableWaitRefreshError: Refresh 错误原样透传。
func TestChainDispatchRecoverableWaitRefreshError(t *testing.T) {
	adapter, _ := w17WaitAdapter(t, gatewaycircuit.WaitEngineOptions{})
	refreshErr := errors.New("w17: refresh refused")
	input := w17WaitInput("aff_w17_wait_error",
		func(context.Context) (gatewaydispatch.SuppressionFilterResult, error) {
			return gatewaydispatch.SuppressionFilterResult{}, refreshErr
		},
		func(gatewaydispatch.SuppressionFilterResult) bool { return true },
		func(gatewaydispatch.SuppressionFilterResult) *int64 { return nil },
		1_000)
	if _, err := adapter.WaitForState(context.Background(), input); !errors.Is(err, refreshErr) {
		t.Fatalf("err = %v; want %v", err, refreshErr)
	}
}
