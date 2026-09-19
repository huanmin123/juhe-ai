package chat

// w16c 覆盖收尾：参数能力交集/收敛、对象存储边界、图片工具与编排器错误臂。

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func w16cCap(parameter string, min, max float64) ChatGenerationParameterCapability {
	return ChatGenerationParameterCapability{Parameter: parameter, Min: min, Max: max, DefaultValue: min}
}

// TestW16CIntersectGenerationParameterCapabilitiesArms 覆盖交集的 min 抬升、
// max 收窄、参数缺失与 min>max 丢弃臂。
func TestW16CIntersectGenerationParameterCapabilitiesArms(t *testing.T) {
	// 第一项的 topP 在第二项缺失 → 丢弃；maxOutputTokens 反向缺失 → 丢弃；
	// temperature min 抬升到 0.5。
	out := intersectGenerationParameterCapabilities([]map[string][]ChatGenerationParameterCapability{
		{
			"chat_completions": {w16cCap("temperature", 0, 2), w16cCap("topP", 0, 1)},
			"responses":        {w16cCap("temperature", 0, 2)},
		},
		{
			"chat_completions": {w16cCap("temperature", 0.5, 2), w16cCap("maxOutputTokens", 1, 100)},
			"responses":        {w16cCap("temperature", 0.5, 2)},
		},
	})
	list := out["chat_completions"]
	if len(list) != 1 || list[0].Parameter != "temperature" || list[0].Min != 0.5 || list[0].Max != 2 {
		t.Fatalf("min 抬升交集 = %+v", list)
	}
	// temperature max 收窄。
	out = intersectGenerationParameterCapabilities([]map[string][]ChatGenerationParameterCapability{
		{
			"chat_completions": {w16cCap("temperature", 0, 2)},
			"responses":        {w16cCap("temperature", 0, 2)},
		},
		{
			"chat_completions": {w16cCap("temperature", 0, 1.5)},
			"responses":        {w16cCap("temperature", 0, 2)},
		},
	})
	list = out["chat_completions"]
	if len(list) != 1 || list[0].Max != 1.5 {
		t.Fatalf("max 收窄交集 = %+v", list)
	}
	// min>max 反转 → 丢弃。
	out = intersectGenerationParameterCapabilities([]map[string][]ChatGenerationParameterCapability{
		{
			"chat_completions": {w16cCap("temperature", 0, 2)},
			"responses":        {w16cCap("temperature", 0, 2)},
		},
		{
			"chat_completions": {w16cCap("temperature", 1.9, 1.4)},
			"responses":        {w16cCap("temperature", 0, 2)},
		},
	})
	if _, ok := out["chat_completions"]; ok {
		t.Fatalf("反转区间应丢弃: %+v", out)
	}
	if list := out["responses"]; len(list) != 1 {
		t.Fatalf("responses 交集 = %+v", list)
	}
}

// TestW16CFlattenGenerationParametersNarrowing 覆盖 flatten 的 min 抬升与 max 收窄臂。
func TestW16CFlattenGenerationParametersNarrowing(t *testing.T) {
	build := func(chatTemp, respTemp ChatGenerationParameterCapability) []ProviderModelCatalogItem {
		return []ProviderModelCatalogItem{
			{
				Model: "m1", SupportedAPIProtocols: []string{"chat_completions", "responses"},
				GenerationParameterCapabilities: map[string][]ChatGenerationParameterCapability{
					"chat_completions": {chatTemp, w16cCap("topP", 0, 1)},
					"responses":        {respTemp},
				},
			},
			{
				Model: "m2", SupportedAPIProtocols: []string{"chat_completions", "responses"},
				GenerationParameterCapabilities: map[string][]ChatGenerationParameterCapability{
					"chat_completions": {chatTemp},
					"responses":        {respTemp},
				},
			},
		}
	}
	// responses 侧 min 抬升。
	raised := flattenGenerationParameters(build(w16cCap("temperature", 0, 2), w16cCap("temperature", 0.5, 2)))
	if len(raised) == 0 {
		t.Fatal("flatten 结果为空")
	}
	// responses 侧 max 收窄。
	lowered := flattenGenerationParameters(build(w16cCap("temperature", 0, 2), w16cCap("temperature", 0, 1.5)))
	if len(lowered) == 0 {
		t.Fatal("flatten 结果为空")
	}
}

// TestW16CConstrainRouteCapabilitiesArms 覆盖路由账户收敛的 min/max/反转臂。
func TestW16CConstrainRouteCapabilitiesArms(t *testing.T) {
	mapping := func(upstream string) ChatTransportModelMapping {
		return ChatTransportModelMapping{SourceModel: "m", SourceEndpointFamily: "chat_completions", UpstreamModel: upstream}
	}
	accountGLM := ChatTransportAccount{ID: "a1", Type: "api_key", ProviderCode: "glm", ModelMappings: []ChatTransportModelMapping{mapping("glm-4.6")}}
	accountGPT := ChatTransportAccount{ID: "a2", Type: "api_key", ProviderCode: "gpt", ModelMappings: []ChatTransportModelMapping{mapping("gpt-4o")}}

	// gpt topP Min=0 在前，glm topP Min=0.01 抬升 min。
	items := constrainChatGenerationParametersForRoute([]ChatGenerationParameterCapability{w16cCap("topP", 0, 1)}, "m", ProtocolChatCompletions, []ChatTransportAccount{accountGPT, accountGLM})
	if len(items) != 1 || items[0].Min != 0.01 {
		t.Fatalf("topP 收敛 = %+v", items)
	}
	// glm temperature Max=1 → max 收窄。
	items = constrainChatGenerationParametersForRoute([]ChatGenerationParameterCapability{w16cCap("temperature", 0, 2)}, "m", ProtocolChatCompletions, []ChatTransportAccount{accountGPT, accountGLM})
	if len(items) != 1 || items[0].Max != 1 {
		t.Fatalf("temperature 收敛 = %+v", items)
	}
	// 无映射账户透传原始区间，与 glm 收窄区间反转 → 丢弃。
	passthrough := ChatTransportAccount{ID: "a3", Type: "api_key", ProviderCode: "deepseek"}
	items = constrainChatGenerationParametersForRoute([]ChatGenerationParameterCapability{w16cCap("temperature", 1.5, 1.6)}, "m", ProtocolChatCompletions, []ChatTransportAccount{passthrough, accountGLM})
	if len(items) != 0 {
		t.Fatalf("反转区间应被丢弃: %+v", items)
	}
}

// TestW16CCapabilitiesForModelClamp 覆盖 maxOutputTokens 上限钳制臂。
func TestW16CCapabilitiesForModelClamp(t *testing.T) {
	limit := int64(1_000)
	table := generationParameterCapabilitiesForModel("GPT", "  GPT-4o  ", &limit)
	list := table["chat_completions"]
	found := false
	for _, item := range list {
		if item.Parameter == "maxOutputTokens" {
			found = true
			if item.Max != 1_000 || item.DefaultValue != 1_000 {
				t.Fatalf("maxOutputTokens 未钳制: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("缺少 maxOutputTokens 能力")
	}
}

// TestW16CLocalObjectStoreBoundaries 覆盖 Write/Open/path 的边界错误臂。
func TestW16CLocalObjectStoreBoundaries(t *testing.T) {
	store, err := NewLocalObjectStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("payload")
	// maxBytes<=0 回退默认上限（成功路径）。
	if err := store.Write("ok/asset", data, 0, ""); err != nil {
		t.Fatalf("默认上限写入失败: %v", err)
	}
	// 超限。
	if err := store.Write("big", make([]byte, 64), 10, ""); err == nil {
		t.Fatal("超限写入应失败")
	}
	// 空数据。
	if err := store.Write("empty", nil, 10, ""); err == nil {
		t.Fatal("空数据应失败")
	}
	// 哈希不一致。
	if err := store.Write("hash", data, 10, strings.Repeat("0", 64)); err == nil {
		t.Fatal("哈希校验应失败")
	}
	// 越出受限目录。
	if err := store.Write("../escape", data, 10, ""); err == nil {
		t.Fatal("越界键应失败")
	}
	// Open：maxBytes<=0 回退默认；缺失文件报错。
	if _, _, err := store.Open("ok/asset", 0); err != nil {
		t.Fatalf("默认上限读取失败: %v", err)
	}
	if _, _, err := store.Open("missing", 10); err == nil {
		t.Fatal("缺失文件应失败")
	}
}

// TestW16CLoadImageEditReferencesValidation 覆盖引用数量纯校验臂。
func TestW16CLoadImageEditReferencesValidation(t *testing.T) {
	store := &Store{}
	if _, err := loadImageEditReferences(store, nil, nil, "owner", "conv", "2026-03-10T08:00:00.000Z"); err == nil {
		t.Fatal("空引用应失败")
	}
	ids := make([]string, chatImageEditMaxReferenceImages+1)
	for index := range ids {
		ids[index] = "chat_asset_0000000000000000000000000000000" + string(rune('a'+index))
	}
	if _, err := loadImageEditReferences(store, nil, ids, "owner", "conv", "2026-03-10T08:00:00.000Z"); err == nil {
		t.Fatal("超量引用应失败")
	}
}

func w16cAssetID(seed byte) string {
	return "chat_asset_" + strings.Repeat("0", 31) + string(seed)
}

// TestW16CExecuteGenerateImageToolArms 覆盖图片工具执行的动作、加载器与适配器错误臂。
func TestW16CExecuteGenerateImageToolArms(t *testing.T) {
	validRefs := func() []any { return []any{w16cAssetID('a'), w16cAssetID('b')} }
	generationOK := func(ChatImageGenerationRequest) (ChatImageGenerationToolResult, error) {
		return ChatImageGenerationToolResult{Data: []byte("img"), Bytes: 3, MimeType: "image/webp", Width: 8, Height: 8}, nil
	}
	sink := &w16cFakeSink{}
	wired := &chatToolExecutionContext{ImageGeneration: generationOK, ArtifactSink: sink}
	// action 非字符串 → fmt.Sprint → 非法。
	if _, err := executeGenerateImageTool(map[string]any{"prompt": "p", "action": 7}, wired); err == nil {
		t.Fatal("非法 action 应失败")
	}
	// DefaultImageModel 为空 → 回退默认模型，并因生成适配器缺失报错。
	if _, err := executeGenerateImageTool(map[string]any{"prompt": "p"}, &chatToolExecutionContext{}); err == nil {
		t.Fatal("缺少运行时应失败")
	}
	// edit 缺少引用装载器。
	editContext := &chatToolExecutionContext{ImageGeneration: generationOK, ArtifactSink: sink}
	if _, err := executeGenerateImageTool(map[string]any{"prompt": "p", "action": "edit", "reference_asset_ids": validRefs()}, editContext); err == nil {
		t.Fatal("缺少引用装载器应失败")
	}
	// 引用装载器失败。
	failingLoader := func([]string) ([]ChatImageEditReference, error) { return nil, errors.New("装载失败") }
	editContext.LoadImageEditReferences = failingLoader
	if _, err := executeGenerateImageTool(map[string]any{"prompt": "p", "action": "edit", "reference_asset_ids": validRefs()}, editContext); err == nil {
		t.Fatal("装载失败应传播")
	}
	// 生成适配器失败。
	generationFail := func(ChatImageGenerationRequest) (ChatImageGenerationToolResult, error) {
		return ChatImageGenerationToolResult{}, errors.New("生成失败")
	}
	if _, err := executeGenerateImageTool(map[string]any{"prompt": "p"}, &chatToolExecutionContext{ImageGeneration: generationFail, ArtifactSink: sink}); err == nil {
		t.Fatal("生成失败应传播")
	}
	// 生成成功但提交失败。
	failingSink := &w16cFailingSink{}
	if _, err := executeGenerateImageTool(map[string]any{"prompt": "p"}, &chatToolExecutionContext{ImageGeneration: generationOK, ArtifactSink: failingSink}); err == nil {
		t.Fatal("提交失败应传播")
	}
	// 引用数量超限。
	refs := make([]any, 6)
	for index := range refs {
		refs[index] = w16cAssetID(byte('a' + index))
	}
	if _, err := normalizeReferenceAssetIDs(refs); err == nil {
		t.Fatal("引用超限应失败")
	}
}

// TestW16CNormalizeChatImageSizeSwap 覆盖尺寸宽高交换臂。
func TestW16CNormalizeChatImageSizeSwap(t *testing.T) {
	got, err := normalizeChatImageSize("512x1536")
	if err != nil || got != "512x1536" {
		t.Fatalf("竖版尺寸 = %q/%v", got, err)
	}
}

type w16cFakeSink struct{}

func (s *w16cFakeSink) CommitGeneratedImage(input GeneratedImageCommitInput) (GeneratedImageCommitResult, error) {
	return GeneratedImageCommitResult{AssetID: "chat_asset_" + strings.Repeat("f", 32), MimeType: input.Result.MimeType}, nil
}

type w16cFailingSink struct{}

func (s *w16cFailingSink) CommitGeneratedImage(input GeneratedImageCommitInput) (GeneratedImageCommitResult, error) {
	return GeneratedImageCommitResult{}, errors.New("提交失败")
}

// TestW16COrchestratorAbortAndLimits 覆盖编排器取消与调用次数上限臂。
func TestW16COrchestratorAbortAndLimits(t *testing.T) {
	registry := newChatInternalToolRegistry("development", true, true)
	publish := func(ChatToolExecutionEvent) {}
	aborted := &chatToolExecutionContext{Aborted: func() bool { return true }}
	orchestrator := newChatInternalToolOrchestrator(registry, nil, aborted, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 2, MaxImageCalls: 2}, publish)
	if _, err := orchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		t.Fatal("取消后不应发起模型调用")
		return ChatToolModelTurn{}, nil
	}); err == nil {
		t.Fatal("取消应失败")
	}
	// 工具调用次数上限。
	limitContext := &chatToolExecutionContext{Aborted: func() bool { return false }}
	limited := newChatInternalToolOrchestrator(registry, nil, limitContext, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 0, MaxImageCalls: 2}, publish)
	if _, _, err := limited.executeCall(ChatToolCall{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: "{}"}); err == nil {
		t.Fatal("工具调用超限应失败")
	}
	// 图片调用次数上限。
	imageLimited := newChatInternalToolOrchestrator(registry, nil, limitContext, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 4, MaxImageCalls: 0}, publish)
	if _, _, err := imageLimited.executeCall(ChatToolCall{CallID: "c2", ToolName: "generate_image", ArgumentsJSON: `{"prompt":"p"}`}); err == nil {
		t.Fatal("图片调用超限应失败")
	}
	// 模型轮次超限（未取消、轮次上限为 0）。
	roundLimited := newChatInternalToolOrchestrator(registry, nil, limitContext, ChatOrchestratorLimits{MaxModelRounds: 0, MaxToolCalls: 2, MaxImageCalls: 2}, publish)
	if _, err := roundLimited.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		t.Fatal("轮次超限后不应发起模型调用")
		return ChatToolModelTurn{}, nil
	}); err == nil {
		t.Fatal("轮次超限应失败")
	}
	// 参数 JSON 非法。
	badArgs := newChatInternalToolOrchestrator(registry, nil, limitContext, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 4, MaxImageCalls: 2}, publish)
	if _, _, err := badArgs.executeCall(ChatToolCall{CallID: "c3", ToolName: "diagnostic_echo", ArgumentsJSON: "{broken}"}); err == nil {
		t.Fatal("非法参数应失败")
	}
}

// TestW16CProjectToolEventArms 覆盖工具事件投影的 id 生成与状态分支。
func TestW16CProjectToolEventArms(t *testing.T) {
	blocks := &[]*assistantBlock{}
	projectToolEvent(blocks, "tool_started", map[string]any{})
	if len(*blocks) != 1 || (*blocks)[0].CallID != "tool_1" {
		t.Fatalf("生成 id = %+v", *blocks)
	}
	existing := &[]*assistantBlock{{Type: "tool_call", CallID: "t1", ToolType: "web_search", Status: asstStarted}}
	projectToolEvent(existing, "tool_updated", map[string]any{"id": "t1"})
	if (*existing)[0].Status != "updated" {
		t.Fatalf("updated 状态 = %+v", (*existing)[0])
	}
	projectToolEvent(existing, "tool_completed", map[string]any{"id": "t1"})
	if (*existing)[0].Status != "completed" {
		t.Fatalf("completed 状态 = %+v", (*existing)[0])
	}
	// 投影事件的 tool_updated / tool_completed 状态分支。
	for _, eventType := range []string{"tool_updated", "tool_completed"} {
		if event := chatGenerationToolEventProjection(eventType, nil); event.Status == "started" {
			t.Fatalf("%s 状态应变化: %+v", eventType, event)
		}
	}
}
