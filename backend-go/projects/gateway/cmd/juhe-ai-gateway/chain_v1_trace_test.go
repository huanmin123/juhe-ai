package main

// W1a 链级 traceID 统一测试：/v1 编排器的 traceID 改用 kernel
// RequestContextMiddleware 已解析的 TraceID（Traceparent/X-Trace-Id 头优先，
// 否则 UUID），使 request.accepted 等阶段日志、审计 capture、usage 与客户端
// 拿到的响应头 X-Trace-Id 同源可查。这里用 kernel.RequestContextMiddleware
// 包住轻量 gatewayChain 复刻生产装配（kernel.go Handler 的包裹顺序），
// models 端点在 preauth 直接放行（preauth.go IsGatewayModelsRequest 分支），
// 无需 runtime 依赖即可走到 request.accepted 日志观测链级 traceID。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

type capturedRequestStage struct {
	stage  string
	fields map[string]any
}

// traceCapturingObservability 只捕获 LogRequestStage，其余方法委托内层实现。
type traceCapturingObservability struct {
	gatewaypreauth.Observability
	mu     sync.Mutex
	stages []capturedRequestStage
}

func (o *traceCapturingObservability) LogRequestStage(stage string, fields map[string]any, outcome string, startedAt time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stages = append(o.stages, capturedRequestStage{stage: stage, fields: fields})
}

func (o *traceCapturingObservability) captured(stage string) []capturedRequestStage {
	o.mu.Lock()
	defer o.mu.Unlock()
	matched := []capturedRequestStage{}
	for _, item := range o.stages {
		if item.stage == stage {
			matched = append(matched, item)
		}
	}
	return matched
}

func newTraceTestChain(t *testing.T) (*gatewayChain, *traceCapturingObservability) {
	t.Helper()
	inner := newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
	capturing := &traceCapturingObservability{Observability: inner}
	service := &gatewaypreauth.Service{
		Responses:     &recordingFailureSink{},
		Observability: capturing,
		Clock:         gatewaypreauth.SystemClock{},
		Circuits:      traceTestNoopCircuits{},
		IPPolicy:      traceTestNoopIPPolicy{},
	}
	return &gatewayChain{preauth: service, observability: capturing, clock: gatewaypreauth.SystemClock{}}, capturing
}

// traceTestNoopIPPolicy/traceTestNoopCircuits 是 preauth 的最小 no-op 依赖：
// IP 策略不拦截、熔断不拦截。轻量链无需真实 runtime cache；无 bearer 的
// models 请求经缺失凭据的预期失败路径正常退出（preflight.rejected）。
type traceTestNoopIPPolicy struct{}

func (traceTestNoopIPPolicy) InspectClientIPPolicy(context.Context, string, bool) (gatewaypreauth.ClientIPPolicyDecision, error) {
	return gatewaypreauth.ClientIPPolicyDecision{}, nil
}

func (traceTestNoopIPPolicy) RecordClientIPPolicyHit(gatewaypreauth.BlacklistPolicy) {}

type traceTestNoopCircuits struct{}

func (traceTestNoopCircuits) InspectPreAuthCircuit(context.Context, gatewaypreauth.PreAuthCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (traceTestNoopCircuits) RecordPreAuthFailure(context.Context, gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (traceTestNoopCircuits) InspectClientIPErrorCircuit(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (traceTestNoopCircuits) RecordClientIPErrorCircuitSuccess(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) error {
	return nil
}

func (traceTestNoopCircuits) RecordClientIPErrorCircuitSample(context.Context, gatewaypreauth.ClientIPErrorCircuitSampleInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

// (a) 带 X-Trace-Id 头：链级 traceID（request.accepted 阶段日志）与响应头
// X-Trace-Id 都等于该头值。
func TestV1ChainTraceIDFollowsRequestHeader(t *testing.T) {
	chain, capturing := newTraceTestChain(t)
	handler := kernel.RequestContextMiddleware(0)(chain)

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("X-Trace-Id", "w1a-trace-header")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	accepted := capturing.captured("request.accepted")
	if len(accepted) != 1 {
		t.Fatalf("request.accepted logs = %d, want 1（轻量链应走到 accepted）", len(accepted))
	}
	if got, _ := accepted[0].fields["traceId"].(string); got != "w1a-trace-header" {
		t.Fatalf("accepted traceId = %q, want 头值 w1a-trace-header", got)
	}
	if got := recorder.Header().Get("X-Trace-Id"); got != "w1a-trace-header" {
		t.Fatalf("响应头 X-Trace-Id = %q, want 头值 w1a-trace-header", got)
	}
}

// (b) 无头请求：链级 traceID 是 kernel 兜底 UUID（非 trace_ 前缀），且与
// 响应头 X-Trace-Id 一致。
func TestV1ChainTraceIDFallsBackToKernelUUID(t *testing.T) {
	chain, capturing := newTraceTestChain(t)
	handler := kernel.RequestContextMiddleware(0)(chain)

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	accepted := capturing.captured("request.accepted")
	if len(accepted) != 1 {
		t.Fatalf("request.accepted logs = %d, want 1", len(accepted))
	}
	traceID, _ := accepted[0].fields["traceId"].(string)
	if len(traceID) != 36 || traceID[8] != '-' || traceID[13] != '-' || traceID[18] != '-' || traceID[23] != '-' {
		t.Fatalf("accepted traceId = %q, want kernel UUID 形态（8-4-4-4-12）", traceID)
	}
	if got := recorder.Header().Get("X-Trace-Id"); got != traceID {
		t.Fatalf("响应头 X-Trace-Id = %q, want 与链级 %q 一致", got, traceID)
	}
}
