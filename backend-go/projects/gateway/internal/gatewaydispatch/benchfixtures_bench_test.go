package gatewaydispatch

// R5 性能基准专用夹具（bench-only）：与 fakes_test.go 的 fake 端口共用，
// 但装配函数不依赖 *testing.T，可在 Benchmark 中直接调用。所有 fake 与
// 既有测试同源（无真实 PG/Redis/上游），正确性 sanity check 由各 Benchmark
// 在循环外执行一次。

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// benchNopAuditContext 是无状态审计捕获上下文：既有 frozenAudit 会随迭代
// 追加 metadata 切片，基准长循环需要零增长的等价物。
type benchNopAuditContext struct{}

func (benchNopAuditContext) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (benchNopAuditContext) AddGatewayMetadata(string, map[string]any)      {}
func (benchNopAuditContext) Finalize(gatewaypreauth.AuditFinalizeInput)     {}

// benchNopAuditSink 是无状态 attempt 审计 sink：固定 attempt id、零增长。
type benchNopAuditSink struct{}

func (benchNopAuditSink) StartAttempt(StartAttemptInput) string                  { return "bench-attempt" }
func (benchNopAuditSink) CompleteAttempt(string, CompleteAttemptInput)           {}
func (benchNopAuditSink) RecordFailedDispatchAttempt(FailedDispatchAttemptInput) {}

// benchAuditCapture 是基准共享的无状态审计捕获组合。
var benchAuditCapture = AuditCapture{Context: benchNopAuditContext{}, Sink: benchNopAuditSink{}}

// benchRouteCoordinator 是零状态路由协调 owner：只覆盖 gatewaypreauth 管道
// 消费的两个方法，fallback 恒为“不再回退”。
type benchRouteCoordinator struct{}

func (benchRouteCoordinator) RequestFallback(context.Context, string) (gatewayrouting.GatewayRouteFallbackDecision, error) {
	return gatewayrouting.GatewayRouteFallbackDecision{Attempted: false}, nil
}

func (benchRouteCoordinator) CompleteFailure(_ context.Context, _ gatewayrouting.GatewayRouteFinalFailure) error {
	return nil
}

// benchNewEngine 装配与 newTestEngine 相同的 fake Engine（无 testing.T 依赖；
// 全部构造不可失败）。
func benchNewEngine() (*Engine, *fakeDriver, *fakeFailureDispatcher) {
	driver := &fakeDriver{}
	dispatcher := &fakeFailureDispatcher{
		failedResult: FailedUpstreamResponseResult{Action: FailedResponseActionSkipAccount, FailureKind: ""},
	}
	engine := NewEngine(driver, dispatcher)
	engine.Suppression = &fakeSuppression{}
	engine.Degradation = &fakeDegradation{}
	engine.Latency = &fakeLatency{}
	engine.ProxyHealth = &fakeProxyHealth{}
	engine.ClientIPAvoidance = &fakeAvoidance{}
	engine.ClientSourceAvoidance = &fakeClientSourceAvoidance{}
	engine.HotQuality = &fakeHotQuality{}
	engine.Affinity = &fakeAffinity{}
	engine.HighConcurrencyQueue = &fakeQueue{}
	engine.ClientIPConcurrency = &fakeClientIPConcurrency{}
	engine.Quota = &fakeQuota{}
	engine.Concurrency = &fakeConcurrencyStore{}
	engine.Cache = &fakeCache{}
	engine.Locks = &fakeLocks{}
	engine.Usage = &fakeUsage{}
	engine.KeyModelStore = gatewayaccounteffects.NewInMemoryKeyModelRuntimeStore(nil)
	return engine, driver, dispatcher
}

// benchNewRequest 构造 POST /v1/chat/completions 网关请求（newTestRequest 的
// 无 testing.T 变体；body 必须是合法 JSON 对象，非法时 Body 为空 map，
// 与 mustJSONObject 的容错一致）。
func benchNewRequest(body string) *gatewaypreauth.GatewayRequest {
	raw := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	raw.Header.Set("Content-Type", "application/json")
	request := gatewaypreauth.NewGatewayRequest(raw)
	object, _ := decodeJSONObject([]byte(body))
	if object == nil {
		object = map[string]any{}
	}
	request.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		Body:    object,
		State: &gatewaybody.BodyState{
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
		},
	}
	return request
}

// benchNewCoordination 构造 per-iteration 协调上下文。每次调度迭代的 attempt
// tracker 会拒绝重复 identity 注册（Node sameAccountRetry 语义），因此全链
// 基准必须每迭代重建；构造失败属夹具缺陷，直接 panic。
func benchNewCoordination() *RequestCoordinationContext {
	now := gatewayupstream.NowMs()
	wallBudget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: now,
		BudgetMs:            ptrInt64(60_000),
	}, nil)
	if err != nil {
		panic(err)
	}
	coordinationBudget, err := gatewayrouting.NewRouteCoordinationBudget(gatewayrouting.RouteCoordinationBudgetOptions{
		RequestID: "trace-bench",
	})
	if err != nil {
		panic(err)
	}
	tracker, err := gatewayrouting.NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		panic(err)
	}
	return &RequestCoordinationContext{
		Scope:                    CoordinationScopeGatewayRequest,
		ServerRetryBudget:        gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		GatewayRequestWallBudget: wallBudget,
		RouteCoordinationBudget:  coordinationBudget,
		RequestAttemptTracker:    tracker,
	}
}
