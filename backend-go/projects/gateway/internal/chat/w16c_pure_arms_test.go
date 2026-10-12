package chat

// w16c 覆盖收尾：参数能力交集/收敛、对象存储边界、图片工具与编排器错误臂。

import (
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
