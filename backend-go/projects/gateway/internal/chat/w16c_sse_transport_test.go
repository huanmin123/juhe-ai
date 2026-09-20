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

// TestW16CParseImageBlockArms 覆盖图像块解析的 failed / completed 无结果臂。
func TestW16CParseImageBlockArms(t *testing.T) {
	failed := parseImageBlock("image_generation.failed", `{"status":"failed"}`)
	if failed.event == nil || failed.event.Type != "image_failed" {
		t.Fatalf("failed 事件 = %+v", failed.event)
	}
	noResult := parseImageBlock("response.output_item.completed", `{"type":"image_generation_call"}`)
	if noResult.event == nil || noResult.event.Type != "image_failed" {
		t.Fatalf("completed 无结果 = %+v", noResult.event)
	}
	started := parseImageBlock("response.output_item.added", `{"type":"image_generation_call"}`)
	if started.event == nil || started.event.Type != "image_started" {
		t.Fatalf("added 事件 = %+v", started.event)
	}
	// eventName 为空时从 data 的 type 字段推导。
	fromData := parseImageBlock("", `{"type":"image_generation.failed"}`)
	if fromData.event == nil || fromData.event.Type != "image_failed" {
		t.Fatalf("type 推导 failed = %+v", fromData.event)
	}
}

// TestW16CParseResponsesBlockImageWithoutCallID 覆盖输出项图像缺 callId 的分支。
func TestW16CParseResponsesBlockImageWithoutCallID(t *testing.T) {
	block := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"image_generation_call\"}}\n\n"
	parsed := parseResponsesBlock(block)
	if parsed.event == nil || parsed.event.Type != "image_started" {
		t.Fatalf("无 callId 图像项 = %+v", parsed.event)
	}
	if _, ok := parsed.event.Item["callId"]; ok {
		t.Fatalf("不应合并 callId: %+v", parsed.event.Item)
	}
}

// TestW16CCompletedResponseImagesMissingCallID 覆盖终态图像缺 callId 的跳过臂。
func TestW16CCompletedResponseImagesMissingCallID(t *testing.T) {
	images := completedResponseImages(map[string]any{"output": []any{
		map[string]any{"type": "image_generation_call", "result": "raw"},
		map[string]any{"type": "reasoning"},
	}})
	if len(images) != 0 {
		t.Fatalf("缺 callId 应跳过: %+v", images)
	}
}

// TestW16CStripImageResultStringsScannerArms 覆盖扫描器空白与无名冒号臂。
func TestW16CStripImageResultStringsScannerArms(t *testing.T) {
	// ':' 前无字符串 token。
	stripped, values, err := stripImageResultStrings(`{ : 1, "result": "abc" }`, "result")
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(values) != 1 || values[0] != "abc" {
		t.Fatalf("result 值 = %v", values)
	}
	if strings.Contains(stripped, "abc") || !strings.Contains(stripped, `"result"`) {
		t.Fatalf("结果值应被剔除且保留键名: %q", stripped)
	}
}

// TestW16CTransportAccountProtocolModes 覆盖上游协议映射到 messages/generate_content 的判定臂。
func TestW16CTransportAccountProtocolModes(t *testing.T) {
	cases := []struct {
		upstreamFamily string
		requiredMode   string
	}{
		{"messages", "messages_sse"},
		{"generate_content", "generate_content_sse"},
	}
	for _, item := range cases {
		account := ChatTransportAccount{
			ID: "a1", Type: "api_key", ProviderCode: "claude",
			SupportedEndpointModes: []string{item.requiredMode},
			ModelMappings: []ChatTransportModelMapping{{
				SourceModel:            "m",
				SourceEndpointFamily:   string(ProtocolChatCompletions),
				UpstreamEndpointFamily: item.upstreamFamily,
			}},
		}
		if !chatTransportAccountSupportsProtocol(account, "m", ProtocolChatCompletions) {
			t.Fatalf("映射到 %s 的账户应支持 chat 协议入口", item.requiredMode)
		}
		account.SupportedEndpointModes = []string{"chat_sse"}
		if chatTransportAccountSupportsProtocol(account, "m", ProtocolChatCompletions) {
			t.Fatalf("缺少 %s 模式应不支持", item.requiredMode)
		}
	}
}

// TestW16CTransportRequestPromptCacheKey 覆盖 chat 请求体 prompt_cache_key 与生成参数字段。
func TestW16CTransportRequestPromptCacheKey(t *testing.T) {
	temperature := 0.5
	_, body := buildChatTransportRequest(ChatTransportRequestInput{
		Protocol:       ProtocolChatCompletions,
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

// TestW16CRunnerImageProjectionArms 覆盖 runner 图像投影的空资产、无变化与完成臂。
func TestW16CRunnerImageProjectionArms(t *testing.T) {
	runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		return ChatGenerationTerminalResult{}, nil
	})
	runner.mu.Lock()
	defer runner.mu.Unlock()
	// 空资产 ID → UpdateImage 返回 nil → 直接返回。
	runner.updateImageLocked(&assistantBlock{Type: "output_image", AssetID: " ", Status: asstStarted}, asstCompleted, map[string]any{})
	// 建立图像块后：无变化 → 不发事件。
	width := int64(8)
	runner.timeline.StartImage(StartImageInput{AssetID: "a1", MimeType: "image/webp", Width: &width, Height: &width})
	existing := &assistantBlock{Type: "output_image", AssetID: "a1", Status: asstStarted, MimeType: "image/webp", Width: &width, Height: &width}
	before := len(runner.subscribers)
	runner.updateImageLocked(existing, asstStarted, map[string]any{"mimeType": "image/webp", "width": float64(8), "height": float64(8)})
	// 完成并携带 patch 字段 → completed 事件。
	runner.updateImageLocked(existing, asstCompleted, map[string]any{"mimeType": "image/png", "width": float64(16), "height": float64(16), "revisedPrompt": "更好"})
	_ = before
}

// TestW16CRunnerAppendEmptyDeltas 覆盖空增量直接返回臂。
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
