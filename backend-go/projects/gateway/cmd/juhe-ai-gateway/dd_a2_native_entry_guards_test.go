package main

// DD-A2 原生入口守卫钉测（2026-11-07 从 dd_a2c6_fix_test.go 拆分保留）：
// openai-compatible 三族协议门前置入口的来源保护——缓存态客户端 IP 黑名单
// 403 / 认证前熔断 429 / 正常请求不拦。DD-C6 桥 usage 测试随 Chat bridge
// 退役删除，本文件只保留与桥无关的原生入口保护覆盖。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ddA2BlockingIPPolicy 是命中黑名单的 IP 策略 stub。
type ddA2BlockingIPPolicy struct{}

func (ddA2BlockingIPPolicy) InspectClientIPPolicy(context.Context, string, bool) (gatewaypreauth.ClientIPPolicyDecision, error) {
	return gatewaypreauth.ClientIPPolicyDecision{
		Blocked:         true,
		NormalizedIP:    &gatewaypreauth.NormalizedClientIP{ClientIP: "203.0.113.9", AggregateIPKey: "203.0.113.0/24"},
		BlacklistPolicy: &gatewaypreauth.BlacklistPolicy{ID: "p1", IPHash: "hash", Reason: "测试封禁", ClientIP: "203.0.113.9", AggregateIPKey: "203.0.113.0/24"},
	}, nil
}

func (ddA2BlockingIPPolicy) RecordClientIPPolicyHit(gatewaypreauth.BlacklistPolicy) {}

// ddA2BlockingCircuits 是熔断中的 Circuits stub。
type ddA2BlockingCircuits struct{}

func (ddA2BlockingCircuits) InspectPreAuthCircuit(context.Context, gatewaypreauth.PreAuthCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	retryAfter := int64(42)
	return gatewaypreauth.CircuitDecision{
		Blocked:           true,
		Reason:            "pre_auth_failures",
		RetryAfterSeconds: &retryAfter,
	}, nil
}

func (ddA2BlockingCircuits) RecordPreAuthFailure(context.Context, gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (ddA2BlockingCircuits) InspectClientIPErrorCircuit(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (ddA2BlockingCircuits) RecordClientIPErrorCircuitSuccess(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) error {
	return nil
}

func (ddA2BlockingCircuits) RecordClientIPErrorCircuitSample(context.Context, gatewaypreauth.ClientIPErrorCircuitSampleInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func ddA2Chain(t *testing.T, circuits gatewaypreauth.PreAuthCircuits, policy gatewaypreauth.ClientIPPolicy) *gatewayChain {
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

func TestDDA2CompatEntryCircuitBlocked(t *testing.T) {
	chain := ddA2Chain(t, ddA2BlockingCircuits{}, traceTestNoopIPPolicy{})
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("熔断中 /v1/files 状态码 = %d, want 429（熔断口径）", recorder.Code)
	}
	if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "42" {
		t.Fatalf("Retry-After = %q, want 42", retryAfter)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "client_ip_pre_auth_circuit_open") {
		t.Fatalf("响应缺熔断错误码: %s", body)
	}
	if !strings.Contains(body, "当前来源短时间认证失败过多") {
		t.Fatalf("响应缺熔断文案: %s", body)
	}
}

func TestDDA2CompatEntryIPBlacklisted(t *testing.T) {
	chain := ddA2Chain(t, traceTestNoopCircuits{}, ddA2BlockingIPPolicy{})
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("黑名单 /v1/files 状态码 = %d, want 403", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "client_ip_blacklisted") {
		t.Fatalf("响应缺黑名单错误码: %s", body)
	}
	if !strings.Contains(body, "已被管理员封禁") {
		t.Fatalf("响应缺黑名单文案: %s", body)
	}
}

func TestDDA2CompatEntryGuardsPassThroughNormalRequest(t *testing.T) {
	// 不拦截的 stub：正常请求应穿过入口保护到达 compat 路由（200 回归）。
	chain := ddA2Chain(t, traceTestNoopCircuits{}, traceTestNoopIPPolicy{})
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("正常 /v1/files 状态码 = %d, want 200（入口保护不得拦截正常请求）", recorder.Code)
	}
	if body := recorder.Body.String(); body != `{"ok":true}` {
		t.Fatalf("正常 /v1/files 响应体 = %q", body)
	}
}
