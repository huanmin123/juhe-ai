package chat

// w16c 覆盖收尾：SSE 收集器边界（64 KiB / 事件数 / 尾随缓冲）、transport
// 请求体字段与 runner 时间线图像投影臂。

import (
	"fmt"
	"strings"
	"testing"
)

// TestW16CCollectOpenAIChatSseLimits 覆盖 Chat SSE 事件数、单事件字节与
// 工具参数累计上限臂。
func TestW16CCollectOpenAIChatSseLimits(t *testing.T) {
	event := func(index int, payload string) string {
		return fmt.Sprintf("data: {\"choices\":[{\"index\":%d,\"delta\":%s}]}\n\n", index, payload)
	}
	// 事件数量超过 maxEvents。
	stream := strings.NewReader(event(0, `{"content":"a"}`) + event(0, `{"content":"b"}`))
	if _, err := CollectOpenAIChatSse(stream, 1<<20, nil, nil, 1); err == nil {
		t.Fatal("事件数超限应失败")
	}
	// 单事件超过 64 KiB。
	huge := strings.NewReader(event(0, `{"content":"`+strings.Repeat("x", sseMaxEventBytes)+`"}`))
	if _, err := CollectOpenAIChatSse(huge, 1<<20, nil, nil, 0); err == nil {
		t.Fatal("单事件超限应失败")
	}
	// 工具参数跨事件累计超过 64 KiB（单个事件保持在上限内）。
	toolStream := strings.NewReader(
		event(0, `{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"`+strings.Repeat("a", 40*1024)+`"}}]}`) +
			event(0, `{"tool_calls":[{"index":0,"function":{"arguments":"`+strings.Repeat("b", 40*1024)+`"}}]}`))
	if _, err := CollectOpenAIChatSse(toolStream, 1<<20, nil, nil, 0); err == nil {
		t.Fatal("工具参数超限应失败")
	}
	// 非法 JSON 事件 → consumeEvent 错误臂。
	if _, err := CollectOpenAIChatSse(strings.NewReader("data: {broken}\n\n"), 1<<20, nil, nil, 0); err == nil {
		t.Fatal("非法事件应失败")
	}
	// 尾随缓冲（无结尾空行）：事件数超限臂。
	trailing := strings.NewReader(event(0, `{"content":"a","finish_reason":"stop"}`) + `data: {broken}`)
	if _, err := CollectOpenAIChatSse(trailing, 1<<20, nil, nil, 1); err == nil {
		t.Fatal("尾随事件数超限应失败")
	}
	// 尾随缓冲：consumeEvent 错误传播臂。
	trailing2 := strings.NewReader(event(0, `{"content":"a","finish_reason":"stop"}`) + `data: {broken}`)
	if _, err := CollectOpenAIChatSse(trailing2, 1<<20, nil, nil, 0); err == nil {
		t.Fatal("尾随非法事件应失败")
	}
}

// TestW16CTransportAccountProtocolModes 覆盖 resolver 的上游端点模式校验：
// 映射 UpstreamEndpointFamily 决定账户必须声明的 SSE 模式，缺失即失败。
func TestW16CTransportAccountProtocolModes(t *testing.T) {
	cases := []struct {
		upstreamFamily string
		request        ChatTransportProtocol
		requiredMode   string
	}{
		{"messages", ProtocolResponses, "messages_sse"},
		{"generate_content", ProtocolChatCompletions, "generate_content_sse"},
	}
	for _, item := range cases {
		account := ChatTransportAccount{
			ID: "a1", Type: "api_key", ProviderCode: "claude",
			SupportedEndpointModes: []string{item.requiredMode},
			ModelMappings: []ChatTransportModelMapping{{
				SourceModel:            "m",
				UpstreamEndpointFamily: item.upstreamFamily,
			}},
		}
		route, err := resolveChatRequestProtocol(account, "m")
		if err != nil || route.RequestProtocol != item.request {
			t.Fatalf("映射到 %s 的账户应解析为 %s: %+v %v", item.requiredMode, item.request, route, err)
		}
		account.SupportedEndpointModes = []string{"chat_sse"}
		if _, err := resolveChatRequestProtocol(account, "m"); err == nil {
			t.Fatalf("缺少 %s 模式应失败", item.requiredMode)
		}
	}
}

func TestW16CTransportRequestPromptCacheKey(t *testing.T) {
	temperature := 0.5
	_, body := buildChatTransportRequest(ChatTransportRequestInput{
		Model:          "gpt-4o",
		CurrentContent: "问题",
		PromptCacheKey: "cache-key-1",
		GenerationParameters: &ChatGenerationParameters{
			Temperature: &temperature,
		},
	})
	if body["prompt_cache_key"] != "cache-key-1" {
		t.Fatalf("prompt_cache_key = %v", body["prompt_cache_key"])
	}
	if body["temperature"] != 0.5 {
		t.Fatalf("temperature = %v", body["temperature"])
	}
}

// TestW16CBuildChatModelOptionsDerivedInputTokens 覆盖输入上限的显式与推导来源。
func TestW16CBuildChatModelOptionsDerivedInputTokens(t *testing.T) {
	window := int64(100_000)
	output := int64(20_000)
	explicit := int64(64_000)
	options := buildChatModelOptions([]string{"m1", "m2"}, []ProviderModelCatalogItem{
		{
			Model:               "m1",
			ProviderCode:        "gpt",
			ContextWindowTokens: &window,
			MaxOutputTokens:     &output,
		},
		{
			Model:           "m2",
			ProviderCode:    "gpt",
			MaxInputTokens:  &explicit,
			MaxOutputTokens: &output,
		},
	})
	if len(options) != 2 {
		t.Fatalf("选项数 = %d", len(options))
	}
	byModel := map[string]*ChatModelOption{}
	for _, option := range options {
		byModel[option.ID] = option
	}
	if derived := byModel["m1"].MaxInputTokens; derived == nil || *derived != 80_000 {
		t.Fatalf("推导 MaxInputTokens = %v", byModel["m1"].MaxInputTokens)
	}
	if got := byModel["m2"].MaxInputTokens; got == nil || *got != 64_000 {
		t.Fatalf("显式 MaxInputTokens = %v", byModel["m2"].MaxInputTokens)
	}
}

func TestW16CRunnerAppendEmptyDeltas(t *testing.T) {
	runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		return ChatGenerationTerminalResult{}, nil
	})
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.appendTextLocked("")
	runner.appendReasoningLocked("")
	snapshot := runner.timeline.Snapshot()
	if len(snapshot.ContentBlocks) != 0 {
		t.Fatalf("空增量不应产生块: %+v", snapshot.ContentBlocks)
	}
}
