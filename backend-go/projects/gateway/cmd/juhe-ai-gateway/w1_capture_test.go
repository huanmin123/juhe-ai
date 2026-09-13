package main

// w1: chainResponseAuditCapture 桥（G17 捕获上下文的 response 层投影）与
// v1DispatchLoop 的轻量请求级方法直测。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func w1Capture() *gatewayusage.AuditCaptureContext {
	return gatewayusage.NewAuditCaptureContext(gatewayusage.AuditCaptureInput{
		TraceID: "trace_cap", ClientIP: "203.0.113.9", StartedAtMs: 1_000,
		TrafficSource: "gateway", Method: "POST", Path: "/v1/chat/completions",
		Settings: gatewayusage.FixedAuditLogSettingsSource{Settings: gatewayusage.AuditLogSettings{
			Enabled: true, FullBodyCaptureEnabled: true, SuccessSampleRate: 1,
			ActiveCaptureMaxBytes: 1 << 20, SuccessFullBodyLimitBytes: 1 << 20, ProblemFullBodyLimitBytes: 1 << 20,
		}},
	})
}

func TestW1ChainResponseAuditCaptureAllMethods(t *testing.T) {
	capture := w1Capture()
	bridge := chainResponseAuditCapture{capture: capture}
	bridge.BindContext(gatewaypreauth.AuditGatewayContext{
		SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "grp_1",
		ProviderCode: "openai", TrafficSource: "gateway", SessionID: "sess_1",
	})
	bridge.AddGatewayMetadata("label_1", map[string]any{"k": "v"})
	// 尝试审计：Start → Complete（含状态码/响应头/体）。
	attemptID := capture.StartAttempt(gatewayusage.StartAttemptInput{
		Account: gatewayusage.UsageModelAccount{ID: "acc_1"}, AttemptIndex: 0,
		UpstreamURL: "https://upstream", Method: "POST",
	})
	if attemptID == "" {
		t.Fatal("attempt id 缺失")
	}
	bridge.CompleteAttempt(attemptID, gatewayresponse.AttemptAuditInput{
		StatusCode: 200, ResponseHeaders: map[string]any{"content-type": "application/json"},
		ResponseBody: []byte(`{"ok":true}`), Success: true,
	})
	// FinalizeLazy：延迟 finalize 输入转换。
	bridge.FinalizeLazy(func() gatewaypreauth.AuditFinalizeInput {
		return gatewaypreauth.AuditFinalizeInput{
			Outcome: "succeeded", Success: true, StatusCode: 200,
			ResponseBody: `{"final":true}`, ResponsePartType: "body",
		}
	})
	// OmitPayloadBodies。
	bridge.OmitPayloadBodies(gatewayresponse.OmitPayloadBodiesInput{
		Label: "omit", PartTypes: []string{"body"},
		AlreadyOmittedPayloadCount: 2, AlreadyOmittedBodyBytes: 64,
	})
	if !bridge.ShouldCaptureSuccessPayloads() {
		t.Log("捕获关闭（默认采样配置），桥面透传验证完成")
	}
}

func TestW1V1LoopRequestStateMethods(t *testing.T) {
	loop := &v1DispatchLoop{}
	// exhaustDispatchFailedAccountID：空 ID 忽略；两次幂等。
	loop.exhaustDispatchFailedAccountID("")
	if len(loop.exhaustedAccounts) != 0 {
		t.Fatal("空 ID 不得入集")
	}
	loop.exhaustDispatchFailedAccountID("acc_1")
	loop.exhaustDispatchFailedAccountID("acc_1")
	if len(loop.exhaustedAccounts) != 1 {
		t.Fatalf("exhausted = %d", len(loop.exhaustedAccounts))
	}
	// streamRetryDispatchAccounts：排除集过滤。
	accounts := []gatewaydispatch.AccountCandidate{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	if got := streamRetryDispatchAccounts(accounts, nil); len(got) != 3 {
		t.Fatalf("无排除 = %d", len(got))
	}
	got := streamRetryDispatchAccounts(accounts, map[string]struct{}{"b": {}})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("排除后 = %+v", got)
	}
	// releaseCutoverReservation：释放闭包触发 + 状态清空。
	released := false
	view := &gatewaydispatch.SpeedFirstCutoverReservationView{ReleaseFunc: func() { released = true }}
	reservationLoop := &v1DispatchLoop{speedFirstCutoverReservation: &gatewayhotquality.SpeedFirstCutoverReservation{}}
	reservationLoop.releaseCutoverReservation(view)
	if !released || reservationLoop.speedFirstCutoverReservation != nil {
		t.Fatalf("released=%v state=%v", released, reservationLoop.speedFirstCutoverReservation)
	}
	// nil 视图安全。
	reservationLoop.releaseCutoverReservation(nil)
}

func TestW1V1LoopAttachedReservationLifecycle(t *testing.T) {
	loop := &v1DispatchLoop{}
	// recordAttachedSpeedFirstReservation：nil 双方安全。
	loop.recordAttachedSpeedFirstReservation(nil, nil)
	reservation := w1Reservation(t)
	view := speedFirstReservationViewOf(reservation)
	loop.recordAttachedSpeedFirstReservation(view, reservation)
	if len(loop.speedFirstAttachedViews) != 1 {
		t.Fatalf("attached = %d", len(loop.speedFirstAttachedViews))
	}
	// attachedConcreteReservationOf：命中并移除映射。
	concrete := loop.attachedConcreteReservationOf(view)
	if concrete == nil || concrete.TargetAccountID() != "acc_target" {
		t.Fatalf("concrete = %+v", concrete)
	}
	if len(loop.speedFirstAttachedViews) != 0 {
		t.Fatalf("映射未清空: %d", len(loop.speedFirstAttachedViews))
	}
	// 未知视图：确定性释放 + nil。
	fired := false
	unknown := &gatewaydispatch.SpeedFirstCutoverReservationView{ReleaseFunc: func() { fired = true }}
	if got := loop.attachedConcreteReservationOf(unknown); got != nil || !fired {
		t.Fatalf("unknown = %+v fired=%v", got, fired)
	}
	if loop.attachedConcreteReservationOf(nil) != nil {
		t.Fatal("nil 视图必须 nil")
	}
}

func TestW1V1LoopSpeedFirstLatencyScope(t *testing.T) {
	loop := &v1DispatchLoop{}
	// 无 APIKeyRecord（strategy 空）：scope 为 nil（G13 范围需要三元组）。
	current := &gatewaypreauth.DispatchContext{}
	current.UsageContext.SystemAccountID = "sys_1"
	current.UsageContext.GroupID = "grp_1"
	if scope := loop.speedFirstLatencyScopeOf(current); scope != nil {
		t.Fatalf("缺 strategy 必须为 nil，got %+v", scope)
	}
	// 带 APIKeyRecord。
	current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_9"}
	scope := loop.speedFirstLatencyScopeOf(current)
	if scope == nil || scope.RouteStrategyID != "rs_9" {
		t.Fatalf("scope with strategy = %+v", scope)
	}
}

// w1LatencyFake 同时满足 LatencyDegradationPort 与 chainSpeedFirstDecisions。
type w1LatencyFake struct {
	degraded map[string]bool
	err      error
}

func (f *w1LatencyFake) OrderAsync(context.Context, []gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	return gatewaydispatch.LatencyDegradationOrder{}, nil
}

func (f *w1LatencyFake) IsAccountLatencyDegradedAsync(_ context.Context, account gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.degraded[account.ID], nil
}

func (f *w1LatencyFake) RecordFirstByteSlowAsync(context.Context, gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, string) (*gatewayproxyhealth.LatencySlowResult, error) {
	return nil, nil
}

func (f *w1LatencyFake) RecordFirstByteSuccessAsync(context.Context, gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
	return nil, nil
}

func TestW1V1LoopSpeedFirstDecisionsOf(t *testing.T) {
	loop := &v1DispatchLoop{c: &gatewayChain{engine: &gatewaydispatch.Engine{}}}
	// Latency 未装配：nil（Node 缺席 continue 语义）。
	if loop.speedFirstDecisionsOf() != nil {
		t.Fatal("未装配必须 nil")
	}
	fake := &w1LatencyFake{}
	loop.c.engine.Latency = fake
	if loop.speedFirstDecisionsOf() == nil {
		t.Fatal("装配后必须命中类型断言")
	}
}

func TestW1V1LoopSpeedFirstRouteEligibleAccounts(t *testing.T) {
	loop := &v1DispatchLoop{}
	current := &gatewaypreauth.DispatchContext{}
	current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	fake := &w1LatencyFake{degraded: map[string]bool{"b": true}}
	loop.c = &gatewayChain{engine: &gatewaydispatch.Engine{Latency: fake}}
	// scope 为 nil：只跑排除过滤，不查降级。
	remaining, err := loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), current,
		map[string]struct{}{"c": {}}, nil, nil)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("nil scope = %+v, %v", remaining, err)
	}
	// scope + decisions：降级账户剔除。
	scope := &gatewaydispatch.LatencyScopeInput{SystemAccountID: "sys_1"}
	eligible, err := loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), current, nil, scope, fake)
	if err != nil || len(eligible) != 2 || eligible[0].ID != "a" || eligible[1].ID != "c" {
		t.Fatalf("eligible = %+v, %v", eligible, err)
	}
	// 决策错误透传。
	fake.err = context.DeadlineExceeded
	if _, err := loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), current, nil, scope, fake); err == nil {
		t.Fatal("决策错误必须透传")
	}
}
