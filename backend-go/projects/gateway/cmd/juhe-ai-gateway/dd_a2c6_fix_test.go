package main

// 深挖复审修复批（DD-A2 / DD-C6）钉住测试：
//   - DD-A2：openai-compatible 三族协议门前置入口的来源保护（缓存态客户端
//     IP 黑名单 403 / 认证前熔断 429 / 正常请求不拦）；
//   - DD-C6：跨协议桥接流 usage 结算传递（桥泵完成的快照 = 桥内计算值）。

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
)

func ddIntPtr(value int) *int { return &value }

// ---- DD-A2：compat 三族入口保护 ----

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

// ---- DD-C6：桥接流 usage 快照 ----

func ddC6BridgeInput(t *testing.T) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	body := `{"model":"client-model","stream":true}`
	request := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body)}
	return gatewaydispatch.UpstreamResponseTransformInput{Req: req}
}

// ddC6AnthropicSSE 是 anthropic messages 上游流（input/cache_read 在
// message_start、output 在 message_delta）。
const ddC6AnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":30}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":50}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// ddC6ChatSSE 是 chat completions 上游流（usage 携带 cached/reasoning 明细）。
const ddC6ChatSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":5},\"completion_tokens_details\":{\"reasoning_tokens\":7}}}\n\n" +
	"data: [DONE]\n\n"

// TestDDC6AnthropicToChatPumpUsageSnapshot 钉住 anthropic messages 上游 → chat
// 客户端方向：桥泵完成后快照 = 桥内计算值（input/output/cache 三族）。
func TestDDC6AnthropicToChatPumpUsageSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"},
		openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if pump == nil {
		t.Fatal("anthropic→chat pump 缺失")
	}
	if err := pump(strings.NewReader(ddC6AnthropicSSE), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("桥泵完成后快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 100 {
		t.Fatalf("InputTokens = %d, want 100（上游 input_tokens 经桥转 chat 形）", gatewayproto.Token(usage.InputTokens))
	}
	if gatewayproto.Token(usage.OutputTokens) != 50 {
		t.Fatalf("OutputTokens = %d, want 50（上游 output_tokens）", gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 30 {
		t.Fatalf("CacheReadTokens = %d, want 30（上游 cache_read_input_tokens）", gatewayproto.Token(usage.CacheReadTokens))
	}
}

// TestDDC6ChatToGeminiPumpUsageSnapshot 钉住 chat completions 上游 → gemini
// 客户端方向：快照口径同 gemini 原生提取器（OutputTokens = candidates +
// thoughts，CacheRead = cachedContentTokenCount，Thinking 单列）。
func TestDDC6ChatToGeminiPumpUsageSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "gemini_generate_content"},
		openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyChatCompletions, "m", slot)
	if pump == nil {
		t.Fatal("chat→gemini pump 缺失")
	}
	if err := pump(strings.NewReader(ddC6ChatSSE), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("桥泵完成后快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 10 {
		t.Fatalf("InputTokens = %d, want 10", gatewayproto.Token(usage.InputTokens))
	}
	if gatewayproto.Token(usage.OutputTokens) != 27 {
		t.Fatalf("OutputTokens = %d, want 27（candidates 20 + thoughts 7，gemini 原生口径）", gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 5 {
		t.Fatalf("CacheReadTokens = %d, want 5", gatewayproto.Token(usage.CacheReadTokens))
	}
	if gatewayproto.Token(usage.ThinkingTokens) != 7 {
		t.Fatalf("ThinkingTokens = %d, want 7", gatewayproto.Token(usage.ThinkingTokens))
	}
}

// TestDDC6BridgePumpWithoutSlotKeepsBehavior 钉住未传槽（既有调用形态）时
// pump 行为不变（正常出字节、无快照）。
func TestDDC6BridgePumpWithoutSlotKeepsBehavior(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"},
		openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyAnthropicMessages, "m")
	if pump == nil {
		t.Fatal("anthropic→chat pump 缺失")
	}
	out := &bytes.Buffer{}
	if err := pump(strings.NewReader(ddC6AnthropicSSE), out); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("无槽形态输出为空")
	}
}

// TestDDC6CarrierAttemptIsolation 钉住载体换代语义：新 attempt 换入新槽后
// supplier 读不到旧 attempt 快照；非桥接 attempt 换入 nil 后供给关闭。
func TestDDC6CarrierAttemptIsolation(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/messages", nil)
	carrier := chainBridgeUsageCarrierOf(chainBridgeUsageCarrierInstall(request))
	if carrier == nil {
		t.Fatal("载体未安装")
	}
	first := &chainBridgeUsageSlot{}
	carrier.replaceSlot(first)
	first.storeUsage(gatewayproto.ParsedUsage{InputTokens: ddIntPtr(11)})
	supplier := carrier.supplier()
	if usage, ok := supplier(); !ok || gatewayproto.Token(usage.InputTokens) != 11 {
		t.Fatalf("首 attempt 快照读取异常: ok=%v usage=%+v", ok, usage)
	}
	// 换号重试：第二个桥接 attempt 换入新槽，旧槽（含迟到的旧 goroutine 写入）
	// 不再可见。
	second := &chainBridgeUsageSlot{}
	carrier.replaceSlot(second)
	first.storeUsage(gatewayproto.ParsedUsage{InputTokens: ddIntPtr(99)})
	if usage, ok := supplier(); ok {
		t.Fatalf("换代后读到旧槽快照: %+v", usage)
	}
	second.storeUsage(gatewayproto.ParsedUsage{InputTokens: ddIntPtr(22)})
	if usage, ok := supplier(); !ok || gatewayproto.Token(usage.InputTokens) != 22 {
		t.Fatalf("新槽快照读取异常: ok=%v usage=%+v", ok, usage)
	}
	// 非桥接 attempt：换入 nil，供给关闭。
	carrier.replaceSlot(nil)
	if _, ok := supplier(); ok {
		t.Fatal("nil 槽不应有供给")
	}
}

// TestDDC6ChatUsageMapConversion 钉住 chat 形 map → ParsedUsage 的解析口径与
// gatewayopenai 提取器同源（cached 走 prompt_tokens_details）。
func TestDDC6ChatUsageMapConversion(t *testing.T) {
	usage := map[string]any{
		"prompt_tokens":         float64(100),
		"completion_tokens":     float64(50),
		"prompt_tokens_details": map[string]any{"cached_tokens": float64(30)},
	}
	parsed := chainBridgeChatUsageMapToParsedUsage(usage)
	if gatewayproto.Token(parsed.InputTokens) != 100 || gatewayproto.Token(parsed.OutputTokens) != 50 ||
		gatewayproto.Token(parsed.CacheReadTokens) != 30 {
		t.Fatalf("chat 形解析 = %+v", parsed)
	}
	if nilUsage := chainBridgeChatUsageMapToParsedUsage(nil); nilUsage.InputTokens != nil {
		t.Fatalf("nil usage 应为无证据: %+v", nilUsage)
	}
}
