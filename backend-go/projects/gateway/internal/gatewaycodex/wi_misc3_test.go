package gatewaycodex

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestWIIsReplayableCodexHistoryItemMatrix(t *testing.T) {
	tests := []struct {
		name string
		item any
		want bool
	}{
		{"非对象", "text", false},
		{"additional_tools 合法", map[string]any{"type": "additional_tools", "role": "r", "tools": []any{}}, true},
		{"additional_tools 缺 tools", map[string]any{"type": "additional_tools", "role": "r"}, false},
		{"message 合法", map[string]any{"type": "message", "role": "u", "content": []any{"x"}}, true},
		{"message 缺 content", map[string]any{"type": "message", "role": "u"}, false},
		{"agent_message 合法", map[string]any{"type": "agent_message", "author": "a", "recipient": "b", "content": []any{}}, true},
		{"reasoning 有摘要", map[string]any{"type": "reasoning", "summary": []any{"s"}}, true},
		{"reasoning 有加密内容", map[string]any{"type": "reasoning", "encrypted_content": "e"}, true},
		{"reasoning 全空", map[string]any{"type": "reasoning"}, false},
		{"local_shell_call 合法", map[string]any{"type": "local_shell_call", "action": map[string]any{}}, true},
		{"local_shell_call 缺 action", map[string]any{"type": "local_shell_call"}, false},
		{"function_call 合法", map[string]any{"type": "function_call", "name": "n", "arguments": "{}", "call_id": "c"}, true},
		{"function_call 缺参数", map[string]any{"type": "function_call", "name": "n", "call_id": "c"}, false},
		{"tool_search_call 合法", map[string]any{"type": "tool_search_call", "arguments": "{}", "execution": "e"}, true},
		{"function_call_output 合法", map[string]any{"type": "function_call_output", "call_id": "c", "output": "o"}, true},
		{"custom_tool_call_output 合法", map[string]any{"type": "custom_tool_call_output", "call_id": "c", "output": 1}, true},
		{"未知类型", map[string]any{"type": "weird"}, false},
	}
	for _, tc := range tests {
		if got := IsReplayableCodexHistoryItem(tc.item); got != tc.want {
			t.Fatalf("%s: got=%v want=%v", tc.name, got, tc.want)
		}
	}
}

func TestWIJsJSONArrayEscapes(t *testing.T) {
	// JSON.stringify 字符串数组语义：双引号与反斜杠转义 + 短转义 + 原样 UTF-8。
	tests := []struct {
		in   []string
		want string
	}{
		{nil, "[]"},
		{[]string{}, "[]"},
		{[]string{"a"}, `["a"]`},
		{[]string{"a", `b"c`}, `["a","b\"c"]`},
		{[]string{`x\y`}, `["x\\y"]`},
		{[]string{"nl\n"}, `["nl\n"]`},
		{[]string{"cr\r"}, `["cr\r"]`},
		{[]string{"tab\t"}, `["tab\t"]`},
		{[]string{"中文"}, `["中文"]`},
	}
	for _, tc := range tests {
		if got := jsJSONArray(tc.in); got != tc.want {
			t.Fatalf("jsJSONArray(%v)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestWIGatewayProtocolResponseProtocol(t *testing.T) {
	// OpenAI 兼容路径优先；随后按原生协议识别 anthropic / gemini。
	openAI := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	if proto, ok := gatewayProtocolResponseProtocol(openAI); !ok || proto != "openai_v1" {
		t.Fatalf("openai=%q ok=%v", proto, ok)
	}
	anthropic := newTestRequest(t, "POST", "/v1/messages", nil, map[string]string{
		"User-Agent": "claude-cli/1", "Anthropic-Beta": "claude-code-1", "X-Claude-Code-Session-Id": "s",
	})
	if proto, ok := gatewayProtocolResponseProtocol(anthropic); !ok || proto != "anthropic_v1" {
		t.Fatalf("anthropic=%q ok=%v", proto, ok)
	}
	gemini := newTestRequest(t, "POST", "/models/gemini:generatecontent", nil, map[string]string{
		"User-Agent": "GeminiCLI/v1", "X-Goog-Api-Key": "k",
	})
	if proto, ok := gatewayProtocolResponseProtocol(gemini); !ok || proto != "gemini_v1beta" {
		t.Fatalf("gemini=%q ok=%v", proto, ok)
	}
	plain := newTestRequest(t, "GET", "/healthz", nil, nil)
	if _, ok := gatewayProtocolResponseProtocol(plain); ok {
		t.Fatal("普通请求不得识别协议")
	}
	if _, ok := gatewayProtocolResponseProtocol(nil); ok {
		t.Fatal("nil req 不得识别协议")
	}
}

func TestWISegmentStoreGuards(t *testing.T) {
	if _, err := NewSegmentStore(SegmentStoreConfig{Root: " "}); err == nil {
		t.Fatal("空 root 必须报错")
	}
	store, err := NewSegmentStore(SegmentStoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	ctx := context.Background()
	reference, err := store.WriteSegmentPayload(ctx, "session", map[string]any{"k": "v"}, time.UnixMilli(1_000))
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	raw, err := store.ReadSegmentPayload(reference)
	if err != nil || !containsJSONKey(raw, "k") {
		t.Fatalf("读回=%s err=%v", raw, err)
	}
	// 超过读取上限必须拒绝。
	oversized := reference
	oversized.CompressedSizeBytes = maxStoredPayloadBytes + 1
	if _, err := store.ReadSegmentPayload(oversized); err == nil {
		t.Fatal("超限引用必须报错")
	}
	// 未知 storage key → 打开失败。
	missing := reference
	missing.StorageKey = "no-such-key"
	if _, err := store.ReadSegmentPayload(missing); err == nil {
		t.Fatal("未知 key 必须报错")
	}
	// digest 校验失败 → 报错。
	tampered := reference
	tampered.SHA256 = stringsRepeat("0", 64)
	if _, err := store.ReadSegmentPayload(tampered); err == nil {
		t.Fatal("digest 不匹配必须报错")
	}
}

func containsJSONKey(raw []byte, key string) bool {
	return len(raw) > 0 && string(raw) != "null" && indexOf(string(raw), key) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func stringsRepeat(ch string, n int) string {
	out := make([]byte, 0, n*len(ch))
	for i := 0; i < n; i++ {
		out = append(out, ch...)
	}
	return string(out)
}

func TestWIProbeSettleFailureBranches(t *testing.T) {
	retry := newTurnRetryService(t)
	coordinator := &fakeProbeCoordinator{
		acquireResult: gatewaycircuit.ProbeAcquireResult{
			Disposition: gatewaycircuit.ProbeDispositionOwner, RuntimeKey: "rk", Generation: 2, OwnerToken: "tok",
		},
		releaseOK: false,
	}
	probe := &TurnAvoidanceProbeService{
		Coordinator: coordinator,
		TurnRetry:   retry,
		Logger:      &recordingLogger{},
		Clock:       SystemClock{},
	}
	input := CodexTurnAvoidanceProbeInput{
		Account:  gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1"},
		Strategy: avoidanceStrategy("wi-settle"),
		Activation: CodexTurnFailureActivation{
			AccountID: "acc-1", SourceGeneration: 3, SourceFenceID: "00000000-0000-4000-8000-00000000000f",
		},
		Dispatch: func(string, string, string, *SourceProbeFence) HealthCheckDispatchOutcome {
			return HealthCheckDispatchOutcome{Outcome: HealthDispatchRejected}
		},
	}
	// release 失败（released=false）→ 通过 owner token settle unknown。
	result, err := probe.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), input)
	if err != nil {
		t.Fatalf("探活失败: %v", err)
	}
	if result.Outcome != ProbeOutcomeUnknown || len(coordinator.settled) == 0 {
		t.Fatalf("result=%+v settled=%v", result, coordinator.settled)
	}
	// 替换 fence 结算成功 → 清理旧来源避让分支。
	coordinator2 := &fakeProbeCoordinator{
		acquireResult: gatewaycircuit.ProbeAcquireResult{
			Disposition: gatewaycircuit.ProbeDispositionOwner, RuntimeKey: "rk2", Generation: 3, OwnerToken: "tok2",
			ReplacedFenceSettlement: &gatewaycircuit.ReplacedProbeFenceSettlement{
				Outcome: ProbeOutcomeSuccess,
				SourceFences: []gatewaycircuit.ProbeSourceFence{{
					StateKey: "wi-settle", AccountID: "acc-1", SourceGeneration: 1, SourceFenceID: "00000000-0000-4000-8000-00000000000e",
				}},
			},
		},
		releaseOK: true,
	}
	probe.Coordinator = coordinator2
	retry.RememberCodexTurnStreamFailure(avoidanceStrategy("wi-settle"), "acc-1", CodexTurnFailureInput{})
	retry.RememberCodexTurnStreamFailure(avoidanceStrategy("wi-settle"), "acc-1", CodexTurnFailureInput{})
	input.Dispatch = func(string, string, string, *SourceProbeFence) HealthCheckDispatchOutcome {
		return HealthCheckDispatchOutcome{Outcome: HealthDispatchQueued}
	}
	result, err = probe.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), input)
	if err != nil {
		t.Fatalf("第二次探活失败: %v", err)
	}
	if result.Outcome != "" {
		t.Fatalf("queued 派发后 outcome 应为空: %+v", result)
	}
	// settleOwnerProbeFailure 直接调用：fence 分支。
	if _, err := probe.settleOwnerProbeFailure(context.Background(), input, coordinator2.acquireResult,
		&SourceProbeFence{StateKey: "s", AccountID: "a", SourceFenceID: "f"}, true, errFake()); err != nil {
		t.Fatalf("fence 失败结算出错: %v", err)
	}
	// owner token 分支。
	if _, err := probe.settleOwnerProbeFailure(context.Background(), input, coordinator2.acquireResult, nil, false, errFake()); err != nil {
		t.Fatalf("owner 失败结算出错: %v", err)
	}
}

func errFake() error { return &wiProbeError{} }

type wiProbeError struct{}

func (e *wiProbeError) Error() string { return "探活失败" }
