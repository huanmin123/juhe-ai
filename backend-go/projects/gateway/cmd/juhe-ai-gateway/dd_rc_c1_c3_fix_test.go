package main

// 深挖复审落点复核回归修复批（DD-RC-C1 / DD-RC-C3）钉住测试：
//   - DD-RC-C1：跨协议桥接流上游未发 usage 时不得写零值快照否决估算兜底——
//     桥状态 Usage 为 nil map（上游确实没发 usage）→ 不写快照，finalization
//     回落既有估算兜底；非 nil（含「发了 usage 但全 0」的合法上游行为）→
//     快照生效，计 0 是上游事实；
//   - DD-RC-C3：compat 三族入口的认证前熔断服务 err 与主链同口径 fail-closed
//     （handleOrchestratorError 渲染），不再静默放行。

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
)

// ---- DD-RC-C1：上游未发 usage 的桥接流不写快照 ----

// ddRC1AnthropicSSENoUsage 是不带 usage 的 anthropic messages 上游流
// （message_start / message_delta 均无 usage 对象 → 桥状态 Usage 保持 nil）。
const ddRC1AnthropicSSENoUsage = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"up-model\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// ddRC1ChatSSENoUsage 是不带 usage 的 chat completions 上游流。
const ddRC1ChatSSENoUsage = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
	"data: [DONE]\n\n"

// ddRC1ChatSSEZeroUsage 是上游发了 usage 但全 0 的 chat completions 流（合法
// 上游行为：计 0 保留，不得回落估算）。
const ddRC1ChatSSEZeroUsage = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n" +
	"data: [DONE]\n\n"

// ddRC1AnthropicSSEZeroUsage 是上游 message_start 带全 0 usage 的 anthropic
// messages 流（anthropicUsageToChatUsage 渲染出非 nil 键形 → 快照生效计 0）。
const ddRC1AnthropicSSEZeroUsage = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"up-model\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// TestDDRC1AnthropicToChatPumpNilUsageNoSnapshot 钉住 anthropic messages 上游
// → chat 客户端方向：上游未发 usage（桥状态 Usage nil）时泵完成后槽上无快照，
// supplier 对本次 attempt 返回 ok=false，finalization 走既有估算兜底（修复前
// nil 经转换得零值快照 ok=true，否决估算 → 计费 0 的回归）。
func TestDDRC1AnthropicToChatPumpNilUsageNoSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"},
		openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if pump == nil {
		t.Fatal("anthropic→chat pump 缺失")
	}
	if err := pump(strings.NewReader(ddRC1AnthropicSSENoUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游未发 usage 时槽上不应有快照（ok=false 才能回落估算兜底）")
	}
}

// TestDDRC1ChatToGeminiPumpNilUsageNoSnapshot 钉住 chat completions 上游 →
// gemini 客户端方向：上游未发 usage 时同样不写快照。
func TestDDRC1ChatToGeminiPumpNilUsageNoSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "gemini_generate_content"},
		openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyChatCompletions, "m", slot)
	if pump == nil {
		t.Fatal("chat→gemini pump 缺失")
	}
	if err := pump(strings.NewReader(ddRC1ChatSSENoUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游未发 usage 时槽上不应有快照（ok=false 才能回落估算兜底）")
	}
}

// TestDDRC1ChatToGeminiPumpZeroUsageStillSnapshotted 钉住「发了 usage 但全 0」
// 的合法上游行为：快照生效（ok=true，值全 0），计 0 是上游事实，不回落估算。
func TestDDRC1ChatToGeminiPumpZeroUsageStillSnapshotted(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "gemini_generate_content"},
		openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyChatCompletions, "m", slot)
	if err := pump(strings.NewReader(ddRC1ChatSSEZeroUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("上游发了 usage（全 0）时快照应生效（ok=true 计 0）")
	}
	if gatewayproto.Token(usage.InputTokens) != 0 || gatewayproto.Token(usage.OutputTokens) != 0 {
		t.Fatalf("全 0 usage 快照 = %+v, want 0/0", usage)
	}
}

// TestDDRC1AnthropicToChatPumpZeroUsageStillSnapshotted 同上，anthropic 上游
// → chat 客户端方向（message_start 携带全 0 usage 对象）。
func TestDDRC1AnthropicToChatPumpZeroUsageStillSnapshotted(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"},
		openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if err := pump(strings.NewReader(ddRC1AnthropicSSEZeroUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("上游发了 usage（全 0）时快照应生效（ok=true 计 0）")
	}
	if gatewayproto.Token(usage.InputTokens) != 0 || gatewayproto.Token(usage.OutputTokens) != 0 {
		t.Fatalf("全 0 usage 快照 = %+v, want 0/0", usage)
	}
}

// ---- DD-RC-C3：compat 入口熔断服务 err fail-closed ----

// ddRC3ErroringCircuits 是 InspectPreAuthCircuit 返回 err（熔断存储不可用）的
// Circuits stub。
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

// TestDDRC3CompatEntryCircuitServiceErrorFailClosed 钉住熔断服务 err 时 compat
// 入口与主链同口径 fail-closed：同一 err 在主链（ResolveGatewayRuntimeAsync
// 上抛 → handleOrchestratorError）渲染 500 {"message":"服务器内部错误"}；
// compat 入口复用同一渲染出口，不得静默放行。
func TestDDRC3CompatEntryCircuitServiceErrorFailClosed(t *testing.T) {
	chain := ddA2Chain(t, ddRC3ErroringCircuits{}, traceTestNoopIPPolicy{})
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
	chain := ddA2Chain(t, ddRC3ErroringCircuits{}, traceTestNoopIPPolicy{})
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
