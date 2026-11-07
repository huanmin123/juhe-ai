package main

// 深挖复审落点复核回归修复批（DD-RC-C3）：compat 三族入口的认证前熔断服务
// err 与主链同口径 fail-closed（handleOrchestratorError 渲染），不再静默放行。
// 2026-11-07 跨协议桥退役：原 DD-RC-C1 桥接流 usage 快照钉住测试随桥删除
// （桥状态机与其快照槽已不存在）。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ddRC3ErroringCircuits 在认证前熔断检查阶段返回存储 err。
type ddRC3ErroringCircuits struct{}

func (ddRC3ErroringCircuits) InspectPreAuthCircuit(context.Context, gatewaypreauth.PreAuthCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, errors.New("熔断存储不可用（测试注入）")
}

func (ddRC3ErroringCircuits) RecordPreAuthFailure(context.Context, gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (ddRC3ErroringCircuits) InspectClientIPErrorCircuit(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (ddRC3ErroringCircuits) RecordClientIPErrorCircuitSuccess(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) error {
	return nil
}

func (ddRC3ErroringCircuits) RecordClientIPErrorCircuitSample(context.Context, gatewaypreauth.ClientIPErrorCircuitSampleInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

// ddRC3Chain 组装带 compat mux 的最小链（与原 ddA2Chain 同构，归宿本文件）。
func ddRC3Chain(t *testing.T, circuits gatewaypreauth.PreAuthCircuits, policy gatewaypreauth.ClientIPPolicy) *gatewayChain {
	t.Helper()
	inner := newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
	service := &gatewaypreauth.Service{
		Responses:     &recordingFailureSink{},
		Observability: inner,
		Clock:         gatewaypreauth.SystemClock{},
		Circuits:      circuits,
		IPPolicy:      policy,
	}
	mux := &chainCompatMux{mux: http.NewServeMux()}
	mux.Register("/v1/files", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	return &gatewayChain{
		preauth:       service,
		observability: inner,
		clock:         gatewaypreauth.SystemClock{},
		compat:        &chainCompatDispatcher{mux: mux},
	}
}

// TestDDRC3CompatEntryCircuitServiceErrorFailClosed 钉住熔断服务 err 时 compat
// 入口与主链同口径 fail-closed：同一 err 在主链（ResolveGatewayRuntimeAsync
// 上抛 → handleOrchestratorError）渲染 500 {"message":"服务器内部错误"}；
// compat 入口复用同一渲染出口，不得静默放行。
func TestDDRC3CompatEntryCircuitServiceErrorFailClosed(t *testing.T) {
	chain := ddRC3Chain(t, ddRC3ErroringCircuits{}, traceTestNoopIPPolicy{})
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("熔断服务 err /v1/files 状态码 = %d, want 500（与主链 handleOrchestratorError 同口径 fail-closed）", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "服务器内部错误") {
		t.Fatalf("响应缺主链 500 契约文案: %s", body)
	}
	if strings.Contains(body, `{"ok":true}`) {
		t.Fatal("熔断服务 err 时不得放行到 compat 路由")
	}
}

// TestDDRC3CompatEntryCircuitServiceErrorFailClosedStopsBeforeCompat 复核短路
// 返回值：熔断 err 渲染 5xx 后必须短路（返回 true），不得继续进 compat mux。
func TestDDRC3CompatEntryCircuitServiceErrorFailClosedStopsBeforeCompat(t *testing.T) {
	chain := ddRC3Chain(t, ddRC3ErroringCircuits{}, traceTestNoopIPPolicy{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	req := gatewaypreauth.NewGatewayRequest(request)
	if shortCircuited := chain.rejectCompatEntryBySourceGuards(recorder, req); !shortCircuited {
		t.Fatal("熔断服务 err 必须短路（返回 true），不得继续进入 compat 路由")
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("直调守卫状态码 = %d, want 500", recorder.Code)
	}
}
