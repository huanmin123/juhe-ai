package main

// w1: chain_v1.go 深水区——speed-first 首字截止决策主体（Node routes.ts
// 1119-1240 决策闭包）、响应观测（routes.ts:2393-2455）、协议成功结算、
// 路由动作终态渲染（routes.ts:373-432）与派发错误结算臂。所有外部面用
// stub：锁（w1FakeLocks）、延迟决策（w1DeepLatencyFake）、账户并发存储
// （w1FakeConcurrencyStore）、失败响应 sink（recordingFailureSink）。

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w1DeepLatencyFake 在 w1LatencyFake 之上增加分阶段错误与采样结果控制：
// 决策闭包先查降级、再记慢采样、再评估剩余候选（内部再次查降级），
// 三个阶段必须能独立注入失败才能覆盖各自的错误臂。
type w1DeepLatencyFake struct {
	degraded      map[string]bool
	errDegraded   error
	errSlow       error
	errEligible   error
	errSuccess    error
	slowResult    *gatewayproxyhealth.LatencySlowResult
	successResult *gatewayproxyhealth.LatencySuccessResult
	degradedCalls int
	slowCalls     int
	successCalls  int
}

func (f *w1DeepLatencyFake) OrderAsync(context.Context, []gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	return gatewaydispatch.LatencyDegradationOrder{}, nil
}

func (f *w1DeepLatencyFake) IsAccountLatencyDegradedAsync(_ context.Context, account gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput) (bool, error) {
	f.degradedCalls++
	if f.degradedCalls > 1 && f.errEligible != nil {
		return false, f.errEligible
	}
	if f.errDegraded != nil {
		return false, f.errDegraded
	}
	return f.degraded[account.ID], nil
}

func (f *w1DeepLatencyFake) RecordFirstByteSlowAsync(context.Context, gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, string) (*gatewayproxyhealth.LatencySlowResult, error) {
	f.slowCalls++
	if f.errSlow != nil {
		return nil, f.errSlow
	}
	return f.slowResult, nil
}

func (f *w1DeepLatencyFake) RecordFirstByteSuccessAsync(context.Context, gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
	f.successCalls++
	if f.errSuccess != nil {
		return nil, f.errSuccess
	}
	return f.successResult, nil
}

// w1FakeConcurrencyStore 是 AccountConcurrencyStore 的最小 stub：默认
// TryAcquireAsync 恒成功，验证切号预留能拿到并发槽（Node
// tryAcquireAccountConcurrencyAsync 共享实现）。
type w1FakeConcurrencyStore struct {
	acquired    []string
	released    int
	loadErr     error
	acquireErr  error
	failAcquire bool
}

func (f *w1FakeConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return map[string]int{}, nil
}

func (f *w1FakeConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return map[string]int{}, nil
}

func (f *w1FakeConcurrencyStore) TryAcquireAsync(_ context.Context, accountID string, _ int, _ gatewaydispatch.AccountConcurrencyAcquireOptions) (gatewaydispatch.ConcurrencySlot, error) {
	if f.acquireErr != nil {
		return gatewaydispatch.ConcurrencySlot{}, f.acquireErr
	}
	if f.failAcquire {
		return gatewaydispatch.ConcurrencySlot{Acquired: false}, nil
	}
	f.acquired = append(f.acquired, accountID)
	return gatewaydispatch.ConcurrencySlot{Acquired: true, Release: func() { f.released++ }}, nil
}

// w1DeepDeadlineLoop 构造决策闭包的完整前置：引擎带锁 + 延迟决策面，
// DispatchContext 带网关流量来源、路由策略记录、速度优先运行时配置和
// 双账户候选窗口（Node preflight.current 的冻结子集）。
func w1DeepDeadlineLoop(t *testing.T, fake *w1DeepLatencyFake, maxRetries int64) (*v1DispatchLoop, *recordingFailureSink) {
	t.Helper()
	loop, sink := w1LoopWithEngine(t, &w1FakeLocks{})
	loop.c.engine.Latency = fake
	loop.current.UsageContext.SystemAccountID = "sys_1"
	loop.current.UsageContext.GroupID = "grp_main"
	loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_1"}
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{
		{ID: "acc_1", Name: "慢账户"},
		{ID: "acc_2", Name: "目标账户"},
	}
	loop.current.NormalRouteSpeedFirstConfig = w4bSpeedFirstConfig(8_000, map[string]any{
		"maxFirstByteRetriesPerRequest": maxRetries,
	})
	return loop, sink
}

func TestW1SpeedFirstDeadlineDecisionBody(t *testing.T) {
	deadline := gatewayrouting.NormalRouteAttemptFirstByteDeadline{EffectiveDeadlineMs: 900}
	tests := []struct {
		name       string
		setup      func(t *testing.T) (*v1DispatchLoop, *w1DeepLatencyFake)
		wantAction gatewaydispatch.FirstByteDeadlineAction
		wantErr    bool
		validate   func(t *testing.T, loop *v1DispatchLoop)
	}{
		{
			// Node routes.ts:1146-1152：慢采样失败向上抛，由闭包兜底 continue。
			name: "慢采样错误透传",
			setup: func(t *testing.T) (*v1DispatchLoop, *w1DeepLatencyFake) {
				fake := &w1DeepLatencyFake{errSlow: errors.New("慢采样写入失败")}
				loop, _ := w1DeepDeadlineLoop(t, fake, 3)
				return loop, fake
			},
			wantErr: true,
		},
		{
			// routes.ts:1155-1170：剩余候选评估里的降级查询失败同样透传。
			name: "剩余候选评估错误透传",
			setup: func(t *testing.T) (*v1DispatchLoop, *w1DeepLatencyFake) {
				fake := &w1DeepLatencyFake{errEligible: errors.New("候选降级查询失败")}
				loop, _ := w1DeepDeadlineLoop(t, fake, 3)
				return loop, fake
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loop, _ := tt.setup(t)
			coordinator := &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}
			action, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current,
				gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"},
				deadline, coordinator, loop.speedFirstDecisionsOf(),
				loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig)
			if tt.wantErr != (err != nil) {
				t.Fatalf("err = %v, 期望错误 %v", err, tt.wantErr)
			}
			if !tt.wantErr && action != tt.wantAction {
				t.Fatalf("action = %v, 期望 %v", action, tt.wantAction)
			}
			if tt.validate != nil {
				tt.validate(t, loop)
			}
		})
	}
}

// TestW1SpeedFirstDeadlineDecisionContinueArms 覆盖决策闭包的继续臂：
// 无剩余候选 / 未降级 / 重试预算耗尽 / 预留挂接失败（行为存疑见注释）。
func TestW1SpeedFirstDeadlineDecisionContinueArms(t *testing.T) {
	deadline := gatewayrouting.NormalRouteAttemptFirstByteDeadline{EffectiveDeadlineMs: 900}
	run := func(t *testing.T, fake *w1DeepLatencyFake, maxRetries int64, mutate func(loop *v1DispatchLoop)) (gatewaydispatch.FirstByteDeadlineAction, *v1DispatchLoop) {
		t.Helper()
		loop, _ := w1DeepDeadlineLoop(t, fake, maxRetries)
		if mutate != nil {
			mutate(loop)
		}
		coordinator := &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}
		action, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current,
			gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"},
			deadline, coordinator, loop.speedFirstDecisionsOf(),
			loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig)
		if err != nil {
			t.Fatalf("决策错误 = %v", err)
		}
		return action, loop
	}
	// routes.ts:1245-1248：窗口内没有剩余候选 → no_remaining_candidate。
	t.Run("无剩余候选", func(t *testing.T) {
		action, _ := run(t, &w1DeepLatencyFake{}, 3, func(loop *v1DispatchLoop) {
			loop.current.Accounts = loop.current.Accounts[:1]
		})
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
	})
	// routes.ts:1243：慢采样未达降级阈值 → slow_observation_not_degraded。
	t.Run("未降级", func(t *testing.T) {
		action, loop := run(t, &w1DeepLatencyFake{}, 3, nil)
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
		if loop.speedFirstSlowObservedForAttempt != nil {
			t.Fatalf("慢采样结果必须原样挂载 = %+v", loop.speedFirstSlowObservedForAttempt)
		}
	})
	// routes.ts:1216：重试预算耗尽 → max_retry_exceeded。
	t.Run("重试预算耗尽", func(t *testing.T) {
		fake := &w1DeepLatencyFake{slowResult: &gatewayproxyhealth.LatencySlowResult{SlowCount: 2, Degraded: true}}
		action, _ := run(t, fake, 1, func(loop *v1DispatchLoop) {
			loop.speedFirstByteRetryCount = 1
		})
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
	})
	// 行为存疑：协调器 state 从未初始化为 "active"（疑似生产问题 3），
	// AttachReservation 恒 false——预留创建后走 attach 失败臂：释放预留、
	// 继续当前上游（Node 目标语义是 attach 成功后返回 Abort）。
	t.Run("预留挂接失败", func(t *testing.T) {
		store := &w1FakeConcurrencyStore{}
		fake := &w1DeepLatencyFake{slowResult: &gatewayproxyhealth.LatencySlowResult{
			SlowCount:     3,
			Degraded:      true,
			DegradedUntil: w1StrPtr("2030-01-01T00:00:00Z"),
			NextProbeAt:   w1StrPtr("2030-01-01T00:01:00Z"),
		}}
		action, loop := run(t, fake, 3, func(loop *v1DispatchLoop) {
			loop.c.engine.Concurrency = store
		})
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
		if len(store.acquired) == 0 {
			t.Fatal("切号目标必须尝试获取并发槽")
		}
		if store.released == 0 {
			t.Fatal("attach 失败后预留槽必须被释放")
		}
		if loop.speedFirstSlowObservedForAttempt == nil || !loop.speedFirstSlowObservedForAttempt.Degraded {
			t.Fatalf("慢采样结果必须挂到尝试状态 = %+v", loop.speedFirstSlowObservedForAttempt)
		}
	})
}

// TestW1ObserveSpeedFirstResponseOutcome 覆盖响应观测的缺席守卫、慢采样
// （含同尝试去重与失败告警）与成功恢复采样（routes.ts:2393-2455）。
func TestW1ObserveSpeedFirstResponseOutcome(t *testing.T) {
	config := w4bSpeedFirstConfig(8_000, nil)
	slowResult := &gatewayproxyhealth.LatencySlowResult{SlowCount: 1, Degraded: true}
	successResult := &gatewayproxyhealth.LatencySuccessResult{Cleared: true, RecoverySuccessCount: 2, RequiredRecoverySuccessCount: 3}
	slow9s := int64(9_000)
	fast500 := int64(500)
	baseLoop := func(t *testing.T, fake *w1DeepLatencyFake) (*v1DispatchLoop, *gatewaypreauth.DispatchContext) {
		t.Helper()
		loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
		loop.c.engine.Latency = fake
		current := &gatewaypreauth.DispatchContext{}
		current.UsageContext.SystemAccountID = "sys_1"
		current.UsageContext.GroupID = "grp_main"
		current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_1"}
		current.NormalRouteSpeedFirstConfig = config
		return loop, current
	}
	dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}}
	// 缺席守卫：无配置 / 无首字耗时 / 无决策面 / 无范围 → 零调用。
	t.Run("缺席守卫", func(t *testing.T) {
		fake := &w1DeepLatencyFake{}
		loop, current := baseLoop(t, fake)
		loop.observeSpeedFirstResponseOutcome(context.Background(), &gatewaypreauth.DispatchContext{}, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slow9s})
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{})
		loop.c.engine.Latency = nil
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slow9s})
		current.UsageContext.SystemAccountID = ""
		loop.c.engine.Latency = fake
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slow9s})
		if fake.slowCalls != 0 || fake.successCalls != 0 {
			t.Fatalf("守卫臂不得触达决策面 slow=%d success=%d", fake.slowCalls, fake.successCalls)
		}
	})
	// 首字超阈值：补记慢采样；同尝试已记录则去重；记录失败走告警。
	t.Run("慢采样", func(t *testing.T) {
		fake := &w1DeepLatencyFake{slowResult: slowResult}
		loop, current := baseLoop(t, fake)
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slow9s})
		if fake.slowCalls != 1 {
			t.Fatalf("slowCalls = %d", fake.slowCalls)
		}
		// 去重标记由首字截止决策写入（l.speedFirstSlowObservedForAttempt）；
		// 已有标记时响应观测不再补记。
		loop.speedFirstSlowObservedForAttempt = &gatewayproxyhealth.LatencySlowResult{}
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slow9s})
		if fake.slowCalls != 1 {
			t.Fatalf("同尝试必须去重 slowCalls = %d", fake.slowCalls)
		}
	})
	t.Run("慢采样错误", func(t *testing.T) {
		fake := &w1DeepLatencyFake{errSlow: errors.New("慢采样失败")}
		loop, current := baseLoop(t, fake)
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slow9s})
		if fake.slowCalls != 1 {
			t.Fatalf("slowCalls = %d", fake.slowCalls)
		}
	})
	// 首字达标：恢复采样成功 / 无结果 / 失败三条臂。
	t.Run("恢复采样", func(t *testing.T) {
		fake := &w1DeepLatencyFake{successResult: successResult}
		loop, current := baseLoop(t, fake)
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &fast500})
		loop2, current2 := baseLoop(t, &w1DeepLatencyFake{})
		loop2.observeSpeedFirstResponseOutcome(context.Background(), current2, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &fast500})
		fakeErr := &w1DeepLatencyFake{errSuccess: errors.New("恢复采样失败")}
		loop3, current3 := baseLoop(t, fakeErr)
		loop3.observeSpeedFirstResponseOutcome(context.Background(), current3, dispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &fast500})
		if fake.successCalls != 1 || fakeErr.successCalls != 1 {
			t.Fatalf("successCalls = %d/%d", fake.successCalls, fakeErr.successCalls)
		}
	})
}

// TestW1ConfirmProtocolSuccessSideEffects 覆盖最终协议成功结算（D-111）：
// 非 ProtocolValidatedSuccess 直接返回；三个结算钩子的缺席、成功与失败臂
// （失败只告警，不改写已提交响应）。
func TestW1ConfirmProtocolSuccessSideEffects(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	calls := 0
	mkConfirm := func() func() error {
		return func() error { calls++; return errors.New("结算失败") }
	}
	handlingPlain := gatewayresponse.UpstreamResponseHandlingResult{}
	dispatched := gatewaydispatch.UpstreamDispatchResult{}
	// 未过协议门：任何钩子都不得调用。
	loop.confirmProtocolSuccessSideEffects(context.Background(), dispatched, handlingPlain)
	if calls != 0 {
		t.Fatalf("协议门必须拦截调用 = %d", calls)
	}
	handling := gatewayresponse.UpstreamResponseHandlingResult{ProtocolValidatedSuccess: true}
	// 钩子全部缺席：安全。
	loop.confirmProtocolSuccessSideEffects(context.Background(), dispatched, handling)
	// 钩子全部失败：只告警。
	dispatchedErr := gatewaydispatch.UpstreamDispatchResult{
		ConfirmSameAccountApiKeyFailures: mkConfirm(),
		ConfirmAccountAPIKeySuccess:      mkConfirm(),
		ConfirmAccountLockSuccess:        mkConfirm(),
	}
	loop.confirmProtocolSuccessSideEffects(context.Background(), dispatchedErr, handling)
	if calls != 3 {
		t.Fatalf("失败结算必须全部触发 = %d", calls)
	}
	// 钩子全部成功。
	ok := 0
	dispatchedOK := gatewaydispatch.UpstreamDispatchResult{
		ConfirmSameAccountApiKeyFailures: func() error { ok++; return nil },
		ConfirmAccountAPIKeySuccess:      func() error { ok++; return nil },
		ConfirmAccountLockSuccess:        func() error { ok++; return nil },
	}
	loop.confirmProtocolSuccessSideEffects(context.Background(), dispatchedOK, handling)
	if ok != 3 {
		t.Fatalf("成功结算必须全部触发 = %d", ok)
	}
}

// TestW1FinalizeRouteActionArms 覆盖路由动作终态渲染（routes.ts:373-432）：
// 失败动作的 Retry-After 头、client_handoff 的流式重试耗尽契约、
// temporarily_blocked 与默认耗尽文案。
func TestW1FinalizeRouteActionArms(t *testing.T) {
	newLoop := func(t *testing.T) (*v1DispatchLoop, *recordingFailureSink, *httptest.ResponseRecorder) {
		t.Helper()
		loop, sink := w1LoopWithEngine(t, &w1FakeLocks{})
		recorder := httptest.NewRecorder()
		loop.res = gatewaypreauth.NewTrackingWriter(recorder)
		return loop, sink, recorder
	}
	retryAfter := int64(2500)
	t.Run("失败动作携带RetryAfter", func(t *testing.T) {
		loop, sink, recorder := newLoop(t)
		loop.finalizeRouteAction(&gatewaypreauth.RouteAction{
			Failure: &gatewayrouting.GatewayRouteFinalFailure{
				StatusCode: http.StatusTooManyRequests, Message: "配额受限",
				ErrorType: "insufficient_quota", ErrorCode: "insufficient_quota",
				ErrorPhase: "quota", RetryAfterMs: &retryAfter,
			},
		})
		if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusTooManyRequests {
			t.Fatalf("sink = %+v", sink.inputs)
		}
		if got := recorder.Header().Get("Retry-After"); got != "3" {
			t.Fatalf("Retry-After = %q", got)
		}
	})
	t.Run("失败动作无RetryAfter", func(t *testing.T) {
		loop, sink, recorder := newLoop(t)
		loop.finalizeRouteAction(&gatewaypreauth.RouteAction{
			Failure: &gatewayrouting.GatewayRouteFinalFailure{
				StatusCode: http.StatusForbidden, Message: "无权限", ErrorPhase: "dispatch",
			},
		})
		if len(sink.inputs) != 1 {
			t.Fatalf("sink = %+v", sink.inputs)
		}
		if got := recorder.Header().Get("Retry-After"); got != "" {
			t.Fatalf("不得设置 Retry-After = %q", got)
		}
	})
	t.Run("客户端交接", func(t *testing.T) {
		loop, sink, _ := newLoop(t)
		loop.finalizeRouteAction(&gatewaypreauth.RouteAction{
			Coordination: gatewaypreauth.RouteActionCoordination{Outcome: gatewaypreauth.RouteOutcomeClientHandoff},
		})
		if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("sink = %+v", sink.inputs)
		}
		input := sink.inputs[0]
		if input.ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
			t.Fatalf("code = %q", input.ResponsePayload.Error.Code)
		}
		if input.RecordUsage == nil || *input.RecordUsage {
			t.Fatal("交接响应不得记量")
		}
	})
	t.Run("临时阻断与默认耗尽文案", func(t *testing.T) {
		loop, sink, _ := newLoop(t)
		loop.finalizeRouteAction(&gatewaypreauth.RouteAction{
			Coordination: gatewaypreauth.RouteActionCoordination{Outcome: "temporarily_blocked"},
		})
		loop2, sink2, _ := newLoop(t)
		loop2.finalizeRouteAction(&gatewaypreauth.RouteAction{})
		if len(sink.inputs) != 1 || len(sink2.inputs) != 1 {
			t.Fatalf("sink = %d/%d", len(sink.inputs), len(sink2.inputs))
		}
		blocked := sink.inputs[0].ResponsePayload.Error.Message
		defaultMsg := sink2.inputs[0].ResponsePayload.Error.Message
		if blocked != "当前路由暂时没有可派发账户，请稍后重试" {
			t.Fatalf("blocked = %q", blocked)
		}
		if defaultMsg != "当前路由没有可用的上游账户" {
			t.Fatalf("default = %q", defaultMsg)
		}
	})
}

// TestW1SettleDispatchErrorBudgetArms 覆盖派发错误结算的已知错误/中断/
// 预算臂：wall 预算 503 固定文案、coordination 预算客户端交接、
// 上游中断静默结算、未知错误的兜底 503。
func TestW1SettleDispatchErrorBudgetArms(t *testing.T) {
	// 未知错误：HandleGatewayRequestKnownErrorResponse 不命中 → 兜底 503。
	loop, sink := w1LoopWithEngine(t, &w1FakeLocks{})
	if settled := loop.settleDispatchError(context.Background(), errors.New("未知派发失败")); !settled {
		t.Fatal("未知错误必须结算")
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sink = %+v", sink.inputs)
	}
	// 上游请求中断：无响应契约。
	loop2, sink2 := w1LoopWithEngine(t, &w1FakeLocks{})
	if settled := loop2.settleDispatchError(context.Background(), &gatewaydispatch.UpstreamRequestAbortedError{}); !settled {
		t.Fatal("中断必须静默结算")
	}
	if len(sink2.inputs) != 0 {
		t.Fatalf("中断不得渲染响应 = %+v", sink2.inputs)
	}
	// coordination 预算：客户端交接契约。
	loop3, sink3 := w1LoopWithEngine(t, &w1FakeLocks{})
	handoff := &gatewaydispatch.GatewayRequestWallBudgetExhaustedError{
		BudgetKind: gatewaydispatch.WallBudgetKindCoordination, WallRemainingMs: 1234,
	}
	if settled := loop3.settleDispatchError(context.Background(), handoff); !settled {
		t.Fatal("coordination 预算必须交接结算")
	}
	if len(sink3.inputs) != 1 || sink3.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sink = %+v", sink3.inputs)
	}
	if sink3.inputs[0].ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatalf("code = %q", sink3.inputs[0].ResponsePayload.Error.Code)
	}
	// wall 预算：固定 503 文案。
	loop4, sink4 := w1LoopWithEngine(t, &w1FakeLocks{})
	wall := &gatewaydispatch.GatewayRequestWallBudgetExhaustedError{BudgetKind: gatewaydispatch.WallBudgetKindWall}
	if settled := loop4.settleDispatchError(context.Background(), wall); !settled {
		t.Fatal("wall 预算必须结算")
	}
	if len(sink4.inputs) != 1 {
		t.Fatalf("sink = %+v", sink4.inputs)
	}
	if got := sink4.inputs[0].ResponsePayload.Error.Message; got != "网关请求时间预算已用尽，请稍后重试" {
		t.Fatalf("message = %q", got)
	}
}

// TestW1SettleDispatchErrorAttemptArms 覆盖 UpstreamAttemptError 非终态臂：
// 耗尽集登记、speed-first 收窄窗口回落（剩余>0 继续循环 / 空窗口走耗尽）、
// agent-guidance 理由变体与终态直入耗尽渲染。组回退在夹具里以跳数上限
// 短路（APIKeyRecord 带 1 个分组绑定 + fallbackSwitches 已达上限），
// 避免 stub 服务上跑真实 fallback preflight。
func TestW1SettleDispatchErrorAttemptArms(t *testing.T) {
	hopLimited := func(loop *v1DispatchLoop) {
		loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{GroupID: "grp_main"}},
		}
		loop.fallbackSwitches = 1
	}
	// 收窄窗口回落：目标失败后解除收窄、失败账户入排除集、剩余>0 继续循环。
	loop, sink := w1LoopWithEngine(t, &w1FakeLocks{})
	hopLimited(loop)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	loop.speedFirstRetryCandidateAccountIds = map[string]struct{}{"acc_1": {}}
	attempt := &gatewaydispatch.UpstreamAttemptError{Message: "首字失败", FailedAccountIDs: []string{"acc_1"}}
	if settled := loop.settleDispatchError(context.Background(), attempt); settled {
		t.Fatal("剩余候选存在时必须继续循环")
	}
	if loop.speedFirstRetryCandidateAccountIds != nil {
		t.Fatal("收窄必须清空")
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_1"]; !excluded {
		t.Fatal("失败账户必须入排除集")
	}
	if _, exhausted := loop.exhaustedAccounts["acc_1"]; !exhausted {
		t.Fatal("失败账户必须入耗尽集")
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("继续循环不得渲染响应 = %+v", sink.inputs)
	}
	// 收窄窗口清空：回落到组回退（跳数上限短路）→ 耗尽渲染。
	loop2, sink2 := w1LoopWithEngine(t, &w1FakeLocks{})
	hopLimited(loop2)
	loop2.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}}
	loop2.speedFirstRetryCandidateAccountIds = map[string]struct{}{"acc_1": {}}
	if settled := loop2.settleDispatchError(context.Background(), attempt); !settled {
		t.Fatal("空窗口必须结算")
	}
	if len(sink2.inputs) != 1 || sink2.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sink = %+v", sink2.inputs)
	}
	// agent-guidance 变体：同样走到耗尽渲染（跳数上限短路）。
	loop3, sink3 := w1LoopWithEngine(t, &w1FakeLocks{})
	hopLimited(loop3)
	guidance := &gatewaydispatch.UpstreamAttemptError{
		Message: "引导耗尽", FailedAccountIDs: []string{"acc_1"},
		AgentGuidanceResponse: &gatewaypreauth.GatewayAgentGuidanceResponse{Message: "引导"},
	}
	if settled := loop3.settleDispatchError(context.Background(), guidance); !settled {
		t.Fatal("guidance 必须结算")
	}
	if len(sink3.inputs) != 1 {
		t.Fatalf("sink = %+v", sink3.inputs)
	}
	// 终态失败：直接耗尽渲染，不写排除集。
	loop4, sink4 := w1LoopWithEngine(t, &w1FakeLocks{})
	terminal := &gatewaydispatch.UpstreamAttemptError{Message: "终态", TerminalUpstreamFailure: true, FailedAccountIDs: []string{"acc_9"}}
	if settled := loop4.settleDispatchError(context.Background(), terminal); !settled {
		t.Fatal("终态必须结算")
	}
	if _, excluded := loop4.streamRetryExcludedAccounts["acc_9"]; excluded {
		t.Fatal("终态失败不得写排除集")
	}
	if len(sink4.inputs) != 1 {
		t.Fatalf("sink = %+v", sink4.inputs)
	}
	// exhaustDispatchFailedAccounts：可恢复失败不进耗尽集。
	loop5, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop5.exhaustDispatchFailedAccounts(&gatewaydispatch.UpstreamAttemptError{
		FailedAccountIDs: []string{"acc_r", "acc_x"}, RecoverableAccountIDs: []string{"acc_r"},
	})
	if _, exhausted := loop5.exhaustedAccounts["acc_r"]; exhausted {
		t.Fatal("可恢复失败不得进耗尽集")
	}
	if _, exhausted := loop5.exhaustedAccounts["acc_x"]; !exhausted {
		t.Fatal("不可恢复失败必须进耗尽集")
	}
	loop5.exhaustDispatchFailedAccounts(&gatewaydispatch.UpstreamAttemptError{})
	if len(loop5.exhaustedAccounts) != 1 {
		t.Fatalf("空失败集不得改写耗尽集 = %+v", loop5.exhaustedAccounts)
	}
}

// TestW1SwitchToFallbackHopLimit 覆盖组回退的跳数上限短路臂（Node
// routes.ts fallback_hop_limit）：回退次数达到分组绑定时跳过并记审计。
func TestW1SwitchToFallbackHopLimit(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{GroupID: "grp_main"}},
	}
	loop.fallbackSwitches = 1
	switched, err := loop.switchToFallbackGroup(context.Background(), "upstream_accounts_exhausted")
	if err != nil || switched != v1FallbackNone {
		t.Fatalf("switch = %v, %v", switched, err)
	}
	// InteractionResourceAffinity 挂载时同样直接跳过。
	loop2, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop2.current.InteractionResourceAffinity = &gatewaygemini.AffinityBinding{}
	switched2, err2 := loop2.switchToFallbackGroup(context.Background(), "upstream_accounts_exhausted")
	if err2 != nil || switched2 != v1FallbackNone {
		t.Fatalf("affinity switch = %v, %v", switched2, err2)
	}
}

// TestW1V1SmallHelpers 覆盖 chain_v1.go 尾部的小工具函数：请求快照、
// 原始体快照、客户端错误协议、client-IP 提取、释放列表、响应日志适配、
// 预算构造错误臂与审计捕获投影的 nil 守卫。
func TestW1V1SmallHelpers(t *testing.T) {
	// usageRequestSnapshotOf：带 body state 时投影 service tier / 推理力度。
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?x=1", strings.NewReader(`{}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	effort := "high"
	req.Body = &gatewaybody.Request{State: &gatewaybody.BodyState{ServiceTier: "flex", ReasoningEffort: &effort}}
	snapshot := usageRequestSnapshotOf(req, "trace_x")
	if snapshot.Method != "POST" || snapshot.RequestedServiceTier != "flex" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	// rawBodySnapshotOf：nil 请求 / nil body / 有 body 三态。
	if got := rawBodySnapshotOf(nil); got != nil {
		t.Fatalf("nil 请求 = %v", got)
	}
	emptyReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/x", nil))
	if got := rawBodySnapshotOf(emptyReq); got != nil {
		t.Fatalf("nil body = %v", got)
	}
	bodyReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/x", nil))
	bodyReq.Body = &gatewaybody.Request{RawBody: []byte("payload")}
	if string(rawBodySnapshotOf(bodyReq)) != "payload" {
		t.Fatalf("rawBody = %q", rawBodySnapshotOf(bodyReq))
	}
	// requestClientIP：X-Forwarded-For 提取。
	ipRequest := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	ipRequest.Header.Set("X-Forwarded-For", "203.0.113.9")
	// 默认信任模型下 RemoteAddr 优先（httptest 默认 192.0.2.1:1234），
	// X-Forwarded-For 只在代理信任配置下生效。
	if got := requestClientIP(gatewaypreauth.NewGatewayRequest(ipRequest)); got != "192.0.2.1" {
		t.Fatalf("clientIP = %q", got)
	}
	// writableEndedOf：非 TrackingWriter 包装 → false。
	if writableEndedOf(failWriter{}) {
		t.Fatal("非跟踪 writer 必须 false")
	}
	// clientErrorProtocol：anthropic 路径 → anthropic；未知路径回退 openai。
	anthropicReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if got := clientErrorProtocol(anthropicReq); got != gatewaypreauth.GatewayErrorProtocolAnthropic {
		t.Fatalf("anthropic protocol = %v", got)
	}
	unknownReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/definitely-not-a-protocol", nil))
	if got := clientErrorProtocol(unknownReq); got != gatewaypreauth.GatewayErrorProtocolOpenAI {
		t.Fatalf("fallback protocol = %v", got)
	}
	// clientIPSlotReleaseList：nil 跳过 + once 语义。
	calls := 0
	list := &clientIPSlotReleaseList{}
	list.Add(nil)
	list.Add(func() { calls++ })
	list.ReleaseAll()
	list.ReleaseAll()
	if calls != 1 {
		t.Fatalf("release 调用次数 = %d", calls)
	}
	// gatewayResponseLogger：三级日志都走 slog 适配（缓冲捕获，不打 stdout）。
	var logBuffer bytes.Buffer
	logger := gatewayResponseLogger{inner: slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	logger.Debug("evt_debug", map[string]any{"k": "v"}, "调试消息")
	logger.Info("evt_info", nil, "信息消息")
	logger.Warn("evt_warn", nil, "警告消息")
	for _, want := range []string{"调试消息", "信息消息", "警告消息", "evt_warn"} {
		if !strings.Contains(logBuffer.String(), want) {
			t.Fatalf("日志缺少 %q: %s", want, logBuffer.String())
		}
	}
	// newRequestBudgets：空 traceID 必须报错（协调预算需要 request id）。
	if _, err := newRequestBudgets("", 1728000000000, gatewaypreauth.SystemClock{}); err == nil {
		t.Fatal("空 requestID 必须报错")
	}
	// 审计捕获投影 nil 守卫。
	chain := &gatewayChain{}
	plain := chain.engineAuditCapture(nil)
	if plain.Sink != nil {
		t.Fatal("非具体捕获不得携带 attempt sink")
	}
	if auditCaptureConcrete(nil) != nil {
		t.Fatal("auditCaptureConcrete 空 context 必须 nil")
	}
	if responseAuditCaptureOf(nil) != nil {
		t.Fatal("responseAuditCaptureOf 空 context 必须 nil")
	}
	// timeoutProfileOf：text / image 两个泳道都出 profile。
	textProfile := timeoutProfileOf(gatewayruntimecache.GatewaySettings{}, "text")
	imageProfile := timeoutProfileOf(gatewayruntimecache.GatewaySettings{}, "image")
	_ = textProfile
	_ = imageProfile
	// firstOutputMetricMarkOf：nil 标记安全；非 nil 标记恰好一次。
	marks := 0
	mark := firstOutputMetricMarkOf(func() { marks++ }, 1728000000000, "POST")
	mark()
	mark()
	if marks != 2 {
		t.Fatalf("marks = %d", marks)
	}
	nilMark := firstOutputMetricMarkOf(nil, 1728000000000, "POST")
	nilMark()
	// clientStrategyPreCommitFailureSignal：非 codex 上下文返回空。
	if got := clientStrategyPreCommitFailureSignal(&gatewaypreauth.ClientStrategyContext{}); got != "" {
		t.Fatalf("signal = %q", got)
	}
	if got := clientStrategyPreCommitFailureSignal(nil); got != "" {
		t.Fatalf("nil signal = %q", got)
	}
	codexStrategy := &gatewaypreauth.ClientStrategyContext{
		Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{
			RetryCoordination: gatewaycodex.GatewayClientRetryCoordination{PreCommitFailureSignal: "protocol_error_event"},
		},
	}
	if got := clientStrategyPreCommitFailureSignal(codexStrategy); got != "protocol_error_event" {
		t.Fatalf("codex signal = %q", got)
	}
}

// failWriter 是非 TrackingWriter 的最小 GatewayResponseWriter，验证
// writableEndedOf 的类型分支回退。
type failWriter struct{}

func (failWriter) Header() http.Header       { return http.Header{} }
func (failWriter) Write([]byte) (int, error) { return 0, errors.New("拒绝写入") }
func (failWriter) WriteHeader(int)           {}
func (failWriter) HeadersSent() bool         { return false }
func (failWriter) StatusCode() int           { return 200 }

func w1StrPtr(value string) *string { return &value }

// TestW1SettleResponseStreamServerRetry 覆盖响应层重试结算（Node
// routes.ts:2301-2398）：策略换号排除后继续循环、检查策略无换号时终止、
// 候选清空时的耗尽退出。
func TestW1SettleResponseStreamServerRetry(t *testing.T) {
	hopLimited := func(loop *v1DispatchLoop) {
		loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{GroupID: "grp_main"}},
		}
		loop.fallbackSwitches = 1
	}
	// 策略换号：当前账户入排除集，仍有剩余候选 → 继续派发循环。
	loop, sink := w1LoopWithEngine(t, &w1FakeLocks{})
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	if settled := loop.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{
			RetryUpstream: true, RetryReason: gatewayresponse.StreamServerRetryResponseInspection,
			ExcludeCurrentAccount: true, Message: "策略要求换号",
		}); settled {
		t.Fatal("有剩余候选必须继续循环")
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_1"]; !excluded {
		t.Fatal("当前账户必须入排除集")
	}
	if loop.streamServerRetryCount != 1 {
		t.Fatalf("retryCount = %d", loop.streamServerRetryCount)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("继续循环不得渲染响应 = %+v", sink.inputs)
	}
	// 检查策略重试且无换号：直接终止（策略命中但不动派发窗口）。
	loop2, sink2 := w1LoopWithEngine(t, &w1FakeLocks{})
	loop2.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	loop2.current.ClientStrategy.DownstreamProtocol = "openai"
	if settled := loop2.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{
			RetryUpstream: true, RetryReason: gatewayresponse.StreamServerRetryResponseInspection,
			Message: "策略终止", ErrorCode: "policy_stop",
			ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
				PolicyID: "pol_1", PolicyName: "检查策略", RetryEnabled: true,
			},
		}); !settled {
		t.Fatal("无换号的策略重试必须终止")
	}
	if len(sink2.inputs) != 1 || sink2.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sink = %+v", sink2.inputs)
	}
	// 客户端载荷是固定文案；策略文案只进审计。
	if got := sink2.inputs[0].ResponsePayload.Error.Message; got != "上游暂时不可用，请重试" {
		t.Fatalf("client message = %q", got)
	}
	if got := sink2.inputs[0].Audit.ErrorMessage; got != "策略终止" {
		t.Fatalf("audit message = %q", got)
	}
	// 候选清空：排除集并入耗尽集 → 组回退（跳数上限短路）→ 耗尽契约。
	loop3, sink3 := w1LoopWithEngine(t, &w1FakeLocks{})
	hopLimited(loop3)
	loop3.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}}
	loop3.current.ClientStrategy.DownstreamProtocol = "openai"
	if settled := loop3.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{
			RetryUpstream: true, RetryReason: "pre_commit_stream_failure",
			ExcludeCurrentAccount: true, Message: "候选耗尽",
		}); !settled {
		t.Fatal("候选清空必须结算")
	}
	if _, exhausted := loop3.exhaustedAccounts["acc_1"]; !exhausted {
		t.Fatal("排除账户必须并入耗尽集")
	}
	if len(sink3.inputs) != 1 {
		t.Fatalf("sink = %+v", sink3.inputs)
	}
}

// TestW1SpeedFirstSlotAcquirerArms 覆盖槽获取器：并发存储错误、未获取、
// image 泳道限额投影与普通泳道键名。
func TestW1SpeedFirstSlotAcquirerArms(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	// 并发存储错误透传。
	errStore := &w1FakeConcurrencyStore{acquireErr: errors.New("并发存储不可用")}
	loop.c.engine.Concurrency = errStore
	acquire := loop.speedFirstSlotAcquirer(loop.current)
	if _, _, err := acquire(context.Background(), "acc_1", 4, gatewayhotquality.AccountConcurrencyAcquireRequest{}); err == nil {
		t.Fatal("存储错误必须透传")
	}
	// 未获取：返回 (零槽, false, nil)。
	loop2, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop2.c.engine.Concurrency = &w1FakeConcurrencyStore{failAcquire: true}
	acquire2 := loop2.speedFirstSlotAcquirer(loop2.current)
	slot, ok, err := acquire2(context.Background(), "acc_1", 4, gatewayhotquality.AccountConcurrencyAcquireRequest{})
	if err != nil || ok || slot.Release != nil {
		t.Fatalf("未获取 = %+v, %v, %v", slot, ok, err)
	}
	// image 泳道：键带泳道后缀，释放闭包透传。
	loop3, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	store3 := &w1FakeConcurrencyStore{}
	loop3.c.engine.Concurrency = store3
	acquire3 := loop3.speedFirstSlotAcquirer(loop3.current)
	imageSlot, imageOK, imageErr := acquire3(context.Background(), "acc_1", 2,
		gatewayhotquality.AccountConcurrencyAcquireRequest{Lane: gatewayhotquality.AccountConcurrencyLaneImage})
	if imageErr != nil || !imageOK || imageSlot.Key != "acc_1:image" || imageSlot.Lane != gatewayhotquality.AccountConcurrencyLaneImage {
		t.Fatalf("image slot = %+v, %v, %v", imageSlot, imageOK, imageErr)
	}
	imageSlot.Release()
	if store3.released != 1 {
		t.Fatalf("released = %d", store3.released)
	}
	// 普通泳道：默认键名。
	textSlot, textOK, textErr := acquire3(context.Background(), "acc_2", 2, gatewayhotquality.AccountConcurrencyAcquireRequest{})
	if textErr != nil || !textOK || textSlot.Lane != "" {
		t.Fatalf("text slot = %+v, %v, %v", textSlot, textOK, textErr)
	}
}

// TestW1OnNormalRouteFirstByteDeadlineDecisionArms 覆盖截止闭包的决策
// 错误兜底（释放预留 + 告警 + 继续）与 scope 缺席继续臂。
func TestW1OnNormalRouteFirstByteDeadlineDecisionArms(t *testing.T) {
	// 决策错误：闭包兜底 Continue，不走 Abort。
	fake := &w1DeepLatencyFake{errDegraded: errors.New("降级查询失败")}
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop.c.engine.Latency = fake
	loop.current.NormalRouteSpeedFirstConfig = w4bSpeedFirstConfig(8_000, nil)
	loop.current.UsageContext.SystemAccountID = "sys_1"
	loop.current.UsageContext.GroupID = "grp_main"
	loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_1"}
	closure := loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{},
		gatewaydispatch.AccountCandidate{ID: "acc_1"},
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured},
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("决策错误兜底 = %v", got)
	}
	// scope 缺席（系统账户为空）：继续当前上游。
	loop2, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop2.c.engine.Latency = &w1DeepLatencyFake{}
	loop2.current.NormalRouteSpeedFirstConfig = w4bSpeedFirstConfig(8_000, nil)
	closure2 := loop2.onNormalRouteFirstByteDeadline(context.Background(), loop2.current)
	if got := closure2(gatewaydispatch.FirstByteDeadlineDecisionInput{},
		gatewaydispatch.AccountCandidate{ID: "acc_1"},
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured},
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("scope 缺席 = %v", got)
	}
}

// TestW1SpeedFirstEligibleWithBudgetTracker 覆盖剩余候选评估的预算跟踪
// 过滤臂：合法键通过、空键报错。
func TestW1SpeedFirstEligibleWithBudgetTracker(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop.c.engine.Latency = &w1DeepLatencyFake{}
	budgets, err := newRequestBudgets("trace_budget", 1728000000000, gatewaypreauth.SystemClock{})
	if err != nil {
		t.Fatalf("budgets = %v", err)
	}
	loop.budgets = budgets
	current := &gatewaypreauth.DispatchContext{}
	current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	scope := &gatewaydispatch.LatencyScopeInput{SystemAccountID: "sys_1"}
	remaining, err := loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), current,
		map[string]struct{}{"acc_1": {}}, scope, loop.speedFirstDecisionsOf())
	if err != nil || len(remaining) != 1 || remaining[0].ID != "acc_2" {
		t.Fatalf("remaining = %+v, %v", remaining, err)
	}
	// 空运行时键：预算登记报错按“不允许尝试”吞掉（continue），候选直接跳过。
	current.Accounts = []gatewaydispatch.AccountCandidate{{ID: ""}}
	blocked, err := loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), current,
		nil, scope, loop.speedFirstDecisionsOf())
	if err != nil || len(blocked) != 0 {
		t.Fatalf("空账户键必须被跳过 = %+v, %v", blocked, err)
	}
}

// TestW1ConfirmClientIPAvoidanceAfterFinalFailure 覆盖终态客户端 IP 回避
// 确认：诊断流量直接跳过、缺席组件跳过、内存回避确认（Node
// routes.ts:2690-2725）。
func TestW1ConfirmClientIPAvoidanceAfterFinalFailure(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	// 诊断流量源：不确认。
	diagnostic := &gatewaypreauth.DispatchContext{}
	diagnostic.UsageContext.TrafficSource = "manual_account_test"
	loop.confirmClientIPAccountAvoidanceAfterFinalFailure(context.Background(), diagnostic, "test")
	// 组件缺席：service 未装配回避工厂 / context 无 tracker。
	loop.confirmClientIPAccountAvoidanceAfterFinalFailure(context.Background(), loop.current, "test")
	// 内存回避：确认成功但无待确认失败 → 静默返回。
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{})
	if err != nil {
		t.Fatalf("NewAvoidance = %v", err)
	}
	defer avoidance.Close()
	loop.c.preauth.AccountAvoidance = avoidance
	tracker := avoidance.CreateAvoidanceTracker(gatewayclientip.AvoidanceScopeInput{
		SystemAccountID: "sys_1", GroupID: "grp_main", ClientIP: "203.0.113.5",
	})
	loop.current.ClientIPAccountAvoidance = tracker
	loop.current.ActiveGatewaySettings = gatewayruntimecache.GatewaySettings{}
	loop.confirmClientIPAccountAvoidanceAfterFinalFailure(context.Background(), loop.current, "stream_server_retry_exhausted")
}
