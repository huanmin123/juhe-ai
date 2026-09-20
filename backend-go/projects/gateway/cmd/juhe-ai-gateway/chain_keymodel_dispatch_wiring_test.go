package main

// chain_keymodel_dispatch_wiring_test.go —— gatewayaccounteffects
// GatewayKeyModelAttempt.SetDispatcher 的生产接线断言（此前无生产调用）。
//
// 装配链：compose.go chainRuntimeDeps.KeyModelHealthDispatch
//（chainKeyModelHealthDispatcher{bridge: resetBridge}）
// → chain_compose.go engine.KeyModel = chainKeyModelAdmission{healthDispatch}
// → chain_wiring_w2c.go Prepare 在 admitted attempt 出口 SetDispatcher
// → attempt main-probe 失败臂 dispatchHealthCheck → adapter（丢弃 fence）
// → accountsRuntimeResetBridge.DispatchAccountHealthCheck → probe-request
// outbox 行（消费端 jobs J1 Runner，行契约由
// compose_accounts_reset_dispatch_test.go 覆盖）。
//
// 本文件断言两段接缝：
//   1. adapter 调用穿透到 runtime-reset bridge 的 outbox（fence 丢弃）；
//   2. admission 在 admitted attempt 上挂载 dispatcher 且派发真实到达
//      （真实 memory key-model store + main-probe 臂的确定性派发路径，
//      ManualScheduler 注入消除 renewal 定时器竞态）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w2xRecordingDispatcher 记录 adapter 的派发入参（并发安全：派发发生在
// attempt settle 调用栈上）。
type w2xRecordingDispatcher struct {
	mu        sync.Mutex
	accountID string
	reason    string
	fence     *gatewayaccounteffects.KeyModelFenceReference
}

func (d *w2xRecordingDispatcher) DispatchAccountHealthCheck(accountID string, reason string, fence *gatewayaccounteffects.KeyModelFenceReference) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accountID, d.reason, d.fence = accountID, reason, fence
}

func TestChainKeyModelHealthDispatcherForwardsToResetBridge(t *testing.T) {
	composed := newResetDispatchComposition(t)
	bridge, writer := newResetDispatchBridge(t, composed)
	adapter := chainKeyModelHealthDispatcher{bridge: bridge}
	adapter.DispatchAccountHealthCheck(resetDispatchTestAccountID, "request_failure",
		&gatewayaccounteffects.KeyModelFenceReference{OwnerID: "att-w2x"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := resetDispatchRows(t, writer)
		if len(rows) == 1 {
			if rows[0][0] != resetDispatchTestAccountID || rows[0][1] != "request_failure" {
				t.Fatalf("unexpected dispatched row: %+v", rows[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("adapter dispatch never landed in the outbox")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChainKeyModelAdmissionWiresAttemptDispatcher(t *testing.T) {
	stores := newChainKeyModelRuntimeStoreSelector("", "w2x-keymodel-dispatch")
	store, err := stores.Select("memory")
	if err != nil {
		t.Fatalf("select memory key-model store: %v", err)
	}
	request := newW2CTestRequest(t, `{"model":"gpt-4o","stream":true}`)
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "w2x-acc",
		DispatchRevision:          int64PtrOf(1),
		SelectedAPIKeyFingerprint: strPtr("fp-w2x"),
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
	}
	route := gatewaydispatch.ResolveGatewayKeyModelAttemptCapability(request, account)
	if route == nil {
		t.Fatal("完整路由必须解析出 capability")
	}
	// main-probe 臂：RecordMainProbeFailure 成功后无条件派发（不依赖
	// failure intent Applied / J1 confirmation 的 store 状态机细节）。
	route.AccountID = "w2x-acc"
	route.IsMainProbe = true

	dispatcher := &w2xRecordingDispatcher{}
	admission := chainKeyModelAdmission{healthDispatch: dispatcher}
	preparation, err := admission.Prepare(context.Background(), store,
		gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput{
			Route:         *route,
			RequestID:     "req-w2x-dispatch",
			AttemptID:     "att-w2x-dispatch",
			FailureBudget: gatewayaccounteffects.NewGatewayKeyModelFailureBudget(),
			// ManualScheduler 挂起 renewal 定时器，消除真实 renew 竞态。
			Scheduler: gatewayaccounteffects.NewManualScheduler(),
			Logger:    gatewayaccounteffects.NopLogger{},
		})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if preparation.Status != gatewayaccounteffects.AttemptPreparationAdmitted || preparation.Attempt == nil {
		t.Fatalf("preparation = %+v，want admitted + attempt", preparation)
	}
	if err := preparation.Attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatalf("ReportUpstreamNotComplete: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		dispatcher.mu.Lock()
		got := dispatcher.reason != ""
		accountID, reason, fence := dispatcher.accountID, dispatcher.reason, dispatcher.fence
		dispatcher.mu.Unlock()
		if got {
			if accountID != "w2x-acc" {
				t.Fatalf("dispatch accountID = %q，want w2x-acc", accountID)
			}
			if reason != "request_failure" {
				t.Fatalf("dispatch reason = %q，want request_failure", reason)
			}
			if fence == nil {
				t.Fatal("main-probe 臂派发必须携带 fence 引用")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SetDispatcher 接线后 attempt 派发未到达 adapter")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
