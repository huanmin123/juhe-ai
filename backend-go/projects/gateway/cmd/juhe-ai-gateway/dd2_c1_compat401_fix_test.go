package main

// 深挖复审 R2 验证后回归修复批（DD2-C1）钉住测试：compat 三族路由内的 401
// 认证失败接入认证前熔断计数——修复前该族 401 出口（scope resolver 解析失败
// → openaicompatstorage 的 invalid_api_key 401）不计数，401 枚举零成本。
// 口径与主链同型：reason 按凭据形态分类（bearer 缺失 → missing_bearer_token，
// 非空无效 → invalid_api_key）、Authorization 经 GatewayPreAuthSource 同源合
// 成；计数失败不阻断已写出的 401（fire-and-forget）；达阈值后下一请求由入口
// 守卫 InspectPreAuthCircuit 短路 429。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ddC1RecordingCircuits 记录 RecordPreAuthFailure 调用并按累计失败数模拟熔断
// 阈值（failThreshold 次后 Inspect 返回 Blocked）。
type ddC1RecordingCircuits struct {
	mu            sync.Mutex
	records       []gatewaypreauth.PreAuthFailureInput
	failThreshold int
}

func (c *ddC1RecordingCircuits) InspectPreAuthCircuit(_ context.Context, _ gatewaypreauth.PreAuthCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failThreshold > 0 && len(c.records) >= c.failThreshold {
		retryAfter := int64(42)
		return gatewaypreauth.CircuitDecision{
			Blocked:           true,
			Reason:            "pre_auth_failures",
			RetryAfterSeconds: &retryAfter,
		}, nil
	}
	return gatewaypreauth.CircuitDecision{}, nil
}

func (c *ddC1RecordingCircuits) RecordPreAuthFailure(_ context.Context, input gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, input)
	return gatewaypreauth.CircuitDecision{}, nil
}

func (c *ddC1RecordingCircuits) InspectClientIPErrorCircuit(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

func (c *ddC1RecordingCircuits) RecordClientIPErrorCircuitSuccess(context.Context, gatewaypreauth.ClientIPErrorCircuitInput) error {
	return nil
}

func (c *ddC1RecordingCircuits) RecordClientIPErrorCircuitSample(context.Context, gatewaypreauth.ClientIPErrorCircuitSampleInput) (gatewaypreauth.CircuitDecision, error) {
	return gatewaypreauth.CircuitDecision{}, nil
}

// ddC1Chain 构造带 401 mux（模拟 scope resolver 解析失败的 invalid_api_key
// 出口）的 compat chain。
func ddC1Chain(t *testing.T, circuits gatewaypreauth.PreAuthCircuits) *gatewayChain {
	t.Helper()
	inner := newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
	service := &gatewaypreauth.Service{
		Responses:     &recordingFailureSink{},
		Observability: inner,
		Clock:         gatewaypreauth.SystemClock{},
		Circuits:      circuits,
		IPPolicy:      traceTestNoopIPPolicy{},
	}
	mux := &chainCompatMux{mux: http.NewServeMux()}
	mux.Register("/v1/files", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"缺少或无效的 API Key","error":{"code":"invalid_api_key"}}`))
	}))
	return &gatewayChain{
		preauth:       service,
		observability: inner,
		clock:         gatewaypreauth.SystemClock{},
		compat:        &chainCompatDispatcher{mux: mux},
	}
}

// TestDDC1CompatUnauthorized401RecordsPreAuthFailure 钉住：compat 401 计入熔
// 断（invalid_api_key reason、GatewayPreAuthSource 同源合成的 Bearer 原文）。
func TestDDC1CompatUnauthorized401RecordsPreAuthFailure(t *testing.T) {
	circuits := &ddC1RecordingCircuits{}
	chain := ddC1Chain(t, circuits)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, withBearer("bad-key"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401（计数不得阻断/改写 401 响应）", recorder.Code)
	}
	if len(circuits.records) != 1 {
		t.Fatalf("RecordPreAuthFailure 调用次数 = %d, want 1（compat 401 必须计入熔断）", len(circuits.records))
	}
	record := circuits.records[0]
	if record.Reason != gatewaypreauth.PreAuthFailureInvalidAPIKey {
		t.Fatalf("reason = %q, want invalid_api_key", record.Reason)
	}
	if record.Authorization != "Bearer bad-key" {
		t.Fatalf("authorization = %q, want 同源合成的 Bearer 原文", record.Authorization)
	}
	if record.ClientIP == "" {
		t.Fatal("clientIP 缺失（与主链/入口守卫同源要求）")
	}
}

// TestDDC1CompatMissingBearerRecordsMissingReason 钉住凭据缺失的分类：无
// Authorization 头记 missing_bearer_token（与主链缺凭据面同款口径）。
func TestDDC1CompatMissingBearerRecordsMissingReason(t *testing.T) {
	circuits := &ddC1RecordingCircuits{}
	chain := ddC1Chain(t, circuits)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401", recorder.Code)
	}
	if len(circuits.records) != 1 {
		t.Fatalf("RecordPreAuthFailure 调用次数 = %d, want 1", len(circuits.records))
	}
	if circuits.records[0].Reason != gatewaypreauth.PreAuthFailureMissingBearerToken {
		t.Fatalf("reason = %q, want missing_bearer_token", circuits.records[0].Reason)
	}
}

// TestDDC1CompatSuccessResponseNotRecorded 钉住：非 401 响应不计数（正常鉴
// 权流量零额外计数）。
func TestDDC1CompatSuccessResponseNotRecorded(t *testing.T) {
	circuits := &ddC1RecordingCircuits{}
	chain := ddC1Chain(t, circuits)
	// mux 换成 200 出口。
	chain.compat.mux.mux.Handle("/v1/vector_stores", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/vector_stores", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", recorder.Code)
	}
	if len(circuits.records) != 0 {
		t.Fatalf("非 401 响应不得计数，records = %d", len(circuits.records))
	}
}

// TestDDC1CompatCircuitOpensAfterThreshold 钉住串联行为：401 失败逐次累计，
// 达阈值后下一请求由入口守卫 InspectPreAuthCircuit 短路 429（且该请求不再进
// mux、不再计数）。
func TestDDC1CompatCircuitOpensAfterThreshold(t *testing.T) {
	circuits := &ddC1RecordingCircuits{failThreshold: 3}
	chain := ddC1Chain(t, circuits)
	for i := 0; i < 3; i++ {
		recorder := httptest.NewRecorder()
		chain.ServeHTTP(recorder, withBearer("bad-key"))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次请求状态码 = %d, want 401（未达阈值）", i+1, recorder.Code)
		}
	}
	if len(circuits.records) != 3 {
		t.Fatalf("累计计数 = %d, want 3", len(circuits.records))
	}
	blocked := httptest.NewRecorder()
	chain.ServeHTTP(blocked, withBearer("bad-key"))
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("达阈值后状态码 = %d, want 429（入口熔断短路）", blocked.Code)
	}
	if body := blocked.Body.String(); !strings.Contains(body, "client_ip_pre_auth_circuit_open") {
		t.Fatalf("响应缺熔断错误码: %s", body)
	}
	if len(circuits.records) != 3 {
		t.Fatalf("熔断短路请求不得再计数，records = %d", len(circuits.records))
	}
}

func withBearer(token string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}
