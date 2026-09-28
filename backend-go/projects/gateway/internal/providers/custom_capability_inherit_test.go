package providers

import (
	"reflect"
	"testing"
)

func containsStringItem(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func containsProtocolTool(toolsByProtocol map[string][]string, protocol, candidate string) bool {
	for _, value := range toolsByProtocol[protocol] {
		if value == candidate {
			return true
		}
	}
	return false
}

// TestInheritBuiltInCatalogCapabilities: 二维矩阵「仅填空」契约——空键以
// 内置行值填充（工具键为整张矩阵继承），非空键不覆盖；全新自定义模型（无内置
// 对应行，由调用侧体现）不经过本函数改写。
func TestInheritBuiltInCatalogCapabilities(t *testing.T) {
	builtin := CustomCatalogCapabilityKeys{
		SupportedToolsByProtocol: map[string][]string{
			"responses":        {"web_search", "function_calling"},
			"chat_completions": {"function_calling"},
		},
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}
	filled := InheritBuiltinCatalogCapabilities(CustomCatalogCapabilityKeys{}, builtin)
	if !reflect.DeepEqual(filled, builtin) {
		t.Fatalf("empty keys must inherit the builtin values: %#v", filled)
	}
	partial := InheritBuiltinCatalogCapabilities(CustomCatalogCapabilityKeys{
		SupportedToolsByProtocol: map[string][]string{
			"chat_completions": {"custom_tool"},
		},
	}, builtin)
	if !reflect.DeepEqual(partial.SupportedToolsByProtocol, map[string][]string{
		"chat_completions": {"custom_tool"},
	}) {
		t.Fatalf("non-empty matrix must stay: %v", partial.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(partial.InputModalities, builtin.InputModalities) ||
		!reflect.DeepEqual(partial.OutputModalities, builtin.OutputModalities) {
		t.Fatalf("empty keys must inherit: in=%v out=%v", partial.InputModalities, partial.OutputModalities)
	}
}

// TestInheritCustomCatalogCapabilitiesMergeKeys: 管理面回填 map 键与
// mergeModelCatalogItems 的合并键一致——非 hybrid 用裸 model（跨供应商覆盖也
// 继承），hybrid（preserveProviderIdentity）用 (provider, model)（不同供应商
// 同名模型不串能力）；built_in 行不受影响。二维矩阵继承后，一维 supportedTools
// 过渡投影按矩阵并集回填。
func TestInheritCustomCatalogCapabilitiesMergeKeys(t *testing.T) {
	builtinRow := ModelCatalogItem{
		Scope:        catalogScopeBuiltIn,
		ProviderCode: "openai",
		Model:        "gpt-6-sol",
		SupportedToolsByProtocol: map[string][]string{
			"responses":        {"web_search", "function_calling"},
			"chat_completions": {"function_calling"},
		},
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}
	customRow := func(provider string) ModelCatalogItem {
		return ModelCatalogItem{
			Scope:        catalogScopePersonal,
			ProviderCode: provider,
			Model:        "gpt-6-sol",
			// scanCustomCatalogItem 的空能力形状是空矩阵/空切片（非 nil）。
			SupportedToolsByProtocol: map[string][]string{},
			InputModalities:          []string{},
			OutputModalities:         []string{},
		}
	}
	merged := []ModelCatalogItem{customRow("my-chat")}
	inheritCustomCatalogCapabilities(merged, []ModelCatalogItem{builtinRow}, false)
	if !containsProtocolTool(merged[0].SupportedToolsByProtocol, "responses", "web_search") ||
		!containsStringItem(merged[0].SupportedTools, "web_search") ||
		!containsStringItem(merged[0].InputModalities, "image") ||
		len(merged[0].OutputModalities) == 0 {
		t.Fatalf("bare-model key must inherit across providers: matrix=%v tools=%v in=%v out=%v",
			merged[0].SupportedToolsByProtocol, merged[0].SupportedTools, merged[0].InputModalities, merged[0].OutputModalities)
	}
	merged = []ModelCatalogItem{customRow("my-chat")}
	inheritCustomCatalogCapabilities(merged, []ModelCatalogItem{builtinRow}, true)
	if len(merged[0].SupportedToolsByProtocol) != 0 || len(merged[0].SupportedTools) != 0 ||
		len(merged[0].InputModalities) != 0 || len(merged[0].OutputModalities) != 0 {
		t.Fatalf("hybrid identity must not inherit across providers: matrix=%v tools=%v in=%v out=%v",
			merged[0].SupportedToolsByProtocol, merged[0].SupportedTools, merged[0].InputModalities, merged[0].OutputModalities)
	}
	merged = []ModelCatalogItem{customRow("openai")}
	inheritCustomCatalogCapabilities(merged, []ModelCatalogItem{builtinRow}, true)
	if !containsProtocolTool(merged[0].SupportedToolsByProtocol, "responses", "web_search") {
		t.Fatalf("same (provider, model) must inherit: %v", merged[0].SupportedToolsByProtocol)
	}
	builtinOnly := []ModelCatalogItem{builtinRow}
	inheritCustomCatalogCapabilities(builtinOnly, nil, false)
	if !containsProtocolTool(builtinOnly[0].SupportedToolsByProtocol, "responses", "web_search") {
		t.Fatalf("builtin row must stay untouched: %v", builtinOnly[0].SupportedToolsByProtocol)
	}
}
