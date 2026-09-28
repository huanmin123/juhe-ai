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

// TestInheritBuiltInCatalogCapabilities: 能力继承原语的「仅填空」契约——空键以
// 内置行值填充，非空键不覆盖；全新自定义模型（无内置对应行，由调用侧体现）
// 不经过本函数改写。
func TestInheritBuiltInCatalogCapabilities(t *testing.T) {
	builtin := CustomCatalogCapabilityKeys{
		SupportedTools:   []string{"web_search", "function_calling"},
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}
	filled := InheritBuiltinCatalogCapabilities(CustomCatalogCapabilityKeys{}, builtin)
	if !reflect.DeepEqual(filled, builtin) {
		t.Fatalf("empty keys must inherit the builtin values: %#v", filled)
	}
	partial := InheritBuiltinCatalogCapabilities(CustomCatalogCapabilityKeys{
		SupportedTools: []string{"custom_tool"},
	}, builtin)
	if !reflect.DeepEqual(partial.SupportedTools, []string{"custom_tool"}) {
		t.Fatalf("non-empty supportedTools must stay: %v", partial.SupportedTools)
	}
	if !reflect.DeepEqual(partial.InputModalities, builtin.InputModalities) ||
		!reflect.DeepEqual(partial.OutputModalities, builtin.OutputModalities) {
		t.Fatalf("empty keys must inherit: in=%v out=%v", partial.InputModalities, partial.OutputModalities)
	}
}

// TestInheritCustomCatalogCapabilitiesMergeKeys: 管理面回填 map 键与
// mergeModelCatalogItems 的合并键一致——非 hybrid 用裸 model（跨供应商覆盖也
// 继承），hybrid（preserveProviderIdentity）用 (provider, model)（不同供应商
// 同名模型不串能力）；built_in 行不受影响。
func TestInheritCustomCatalogCapabilitiesMergeKeys(t *testing.T) {
	builtinRow := ModelCatalogItem{
		Scope:            catalogScopeBuiltIn,
		ProviderCode:     "openai",
		Model:            "gpt-6-sol",
		SupportedTools:   []string{"web_search"},
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}
	customRow := func(provider string) ModelCatalogItem {
		return ModelCatalogItem{
			Scope:        catalogScopePersonal,
			ProviderCode: provider,
			Model:        "gpt-6-sol",
			// scanCustomCatalogItem 的空能力形状是空切片（非 nil）。
			InputModalities:  []string{},
			OutputModalities: []string{},
		}
	}
	merged := []ModelCatalogItem{customRow("my-chat")}
	inheritCustomCatalogCapabilities(merged, []ModelCatalogItem{builtinRow}, false)
	if !containsStringItem(merged[0].SupportedTools, "web_search") ||
		!containsStringItem(merged[0].InputModalities, "image") ||
		len(merged[0].OutputModalities) == 0 {
		t.Fatalf("bare-model key must inherit across providers: tools=%v in=%v out=%v",
			merged[0].SupportedTools, merged[0].InputModalities, merged[0].OutputModalities)
	}
	merged = []ModelCatalogItem{customRow("my-chat")}
	inheritCustomCatalogCapabilities(merged, []ModelCatalogItem{builtinRow}, true)
	if len(merged[0].SupportedTools) != 0 || len(merged[0].InputModalities) != 0 || len(merged[0].OutputModalities) != 0 {
		t.Fatalf("hybrid identity must not inherit across providers: tools=%v in=%v out=%v",
			merged[0].SupportedTools, merged[0].InputModalities, merged[0].OutputModalities)
	}
	merged = []ModelCatalogItem{customRow("openai")}
	inheritCustomCatalogCapabilities(merged, []ModelCatalogItem{builtinRow}, true)
	if !containsStringItem(merged[0].SupportedTools, "web_search") {
		t.Fatalf("same (provider, model) must inherit: %v", merged[0].SupportedTools)
	}
	builtinOnly := []ModelCatalogItem{builtinRow}
	inheritCustomCatalogCapabilities(builtinOnly, nil, false)
	if !containsStringItem(builtinOnly[0].SupportedTools, "web_search") {
		t.Fatalf("builtin row must stay untouched: %v", builtinOnly[0].SupportedTools)
	}
}
