package main

// w1: chain_compose.go 客户端模型目录投影（selectClientCatalogItems 系列）
// 与 JSON 辅助投影直测。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func w1CatalogItem(model, scope string) gatewayruntimecache.ProviderModelCatalogItem {
	input := 10.0
	visible := true
	release := "2026-01-01"
	return gatewayruntimecache.ProviderModelCatalogItem{
		Model: model, Scope: scope, Status: "active",
		CatalogVisible: &visible, InputUsdPer1M: &input, ReleaseDate: &release,
	}
}

func TestW1SortedUniqueProviderCodes(t *testing.T) {
	got := sortedUniqueProviderCodes([]string{" openai ", "OpenAI", "", "gemini", "openai"})
	if len(got) != 2 || got[0] != "gemini" || got[1] != "openai" {
		t.Fatalf("codes = %v", got)
	}
	if got := sortedUniqueProviderCodes(nil); len(got) != 0 {
		t.Fatalf("nil = %v", got)
	}
}

func TestW1SelectClientCatalogItems(t *testing.T) {
	personal := w1CatalogItem("gpt-x", "personal")
	global := w1CatalogItem("gpt-x", "global")
	builtin := w1CatalogItem("gpt-x", "built_in")
	hidden := builtin
	hiddenValue := false
	hidden.CatalogVisible = &hiddenValue
	unpriced := gatewayruntimecache.ProviderModelCatalogItem{Model: "gpt-free", Scope: "built_in", Status: "active"}
	inactive := w1CatalogItem("gpt-old", "built_in")
	inactive.Status = "disabled"
	newer := w1CatalogItem("gpt-new", "built_in")
	newerRelease := "2026-06-01"
	newer.ReleaseDate = &newerRelease
	selected := selectClientCatalogItems([]gatewayruntimecache.ProviderModelCatalogItem{
		builtin, global, personal, hidden, unpriced, inactive, newer,
	})
	models := []string{}
	for _, item := range selected {
		models = append(models, item.Model+"@"+item.Scope)
	}
	// personal/global 同模型去重保留 best-scope（personal），隐藏与未定价被剔除。
	if len(selected) != 2 {
		t.Fatalf("selected = %v", models)
	}
	if selected[0].Model != "gpt-new" {
		t.Fatalf("新模型未按发布日期排序: %v", models)
	}
	if selected[1].Scope != "personal" {
		t.Fatalf("best scope = %q", selected[1].Scope)
	}
	// 空 model 剔除。
	blank := w1CatalogItem("  ", "built_in")
	if got := selectClientCatalogItems([]gatewayruntimecache.ProviderModelCatalogItem{blank}); len(got) != 0 {
		t.Fatalf("空模型 = %v", got)
	}
}

func TestW1ClientCatalogCompareHelpers(t *testing.T) {
	if clientCatalogScopeRank(gatewayruntimecache.ProviderModelCatalogItem{Scope: "personal"}) != 3 {
		t.Fatal("personal rank 错误")
	}
	if clientCatalogScopeRank(gatewayruntimecache.ProviderModelCatalogItem{Scope: "global"}) != 2 {
		t.Fatal("global rank 错误")
	}
	if clientCatalogScopeRank(gatewayruntimecache.ProviderModelCatalogItem{}) != 1 {
		t.Fatal("default rank 错误")
	}
	older := w1CatalogItem("a", "built_in")
	newer := w1CatalogItem("b", "built_in")
	newerRelease := "2026-06-01"
	newer.ReleaseDate = &newerRelease
	if !clientCatalogCompareItems(newer, older) {
		t.Fatal("新日期应排在前面")
	}
	if clientCatalogCompareItems(older, newer) {
		t.Fatal("旧日期不应排在前面")
	}
	if got := clientCatalogReleaseDate(gatewayruntimecache.ProviderModelCatalogItem{}); got != "" {
		t.Fatalf("nil date = %q", got)
	}
	priceBearing := gatewayruntimecache.ProviderModelCatalogItem{}
	tierPrice := json.RawMessage(`{"priority":{}}`)
	priceBearing.ServiceTierPrices = tierPrice
	if !clientCatalogHasVisiblePrice(priceBearing) {
		t.Fatal("tier 价格应视为可见定价")
	}
	if clientCatalogHasVisiblePrice(gatewayruntimecache.ProviderModelCatalogItem{}) {
		t.Fatal("无定价不应可见")
	}
}

func TestW1ClientCatalogEntryOf(t *testing.T) {
	item := w1CatalogItem("gpt-test", "personal")
	contextWindow := int64(128000)
	item.ContextWindowTokens = &contextWindow
	item.CodexSupportedReasoningLevels = json.RawMessage(`["low","high"]`)
	notes := "备注"
	item.CapabilityNotes = &notes
	levels := json.RawMessage(`"high"`)
	item.CodexDefaultReasoningLevel = levels
	entry := clientCatalogEntryOf(item)
	if entry.Model != "gpt-test" || entry.Scope != "personal" || entry.ContextWindowTokens != 128000 {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.CapabilityNotes != "备注" {
		t.Fatalf("notes = %q", entry.CapabilityNotes)
	}
	if len(entry.CodexSupportedReasoningLevels) != 2 || entry.CodexDefaultReasoningLevel != "high" {
		t.Fatalf("codex levels = %v %q", entry.CodexSupportedReasoningLevels, entry.CodexDefaultReasoningLevel)
	}
	// nil JSON 投影。
	empty := clientCatalogEntryOf(w1CatalogItem("m", "global"))
	if empty.CodexSupportedReasoningLevels != nil || empty.CodexDefaultReasoningLevel != "" {
		t.Fatalf("空投影 = %+v", empty)
	}
	if nilString(nil) != "" || nilInt(nil) != 0 {
		t.Fatal("nil 辅助错误")
	}
	if got := rawMessageStringList(json.RawMessage("not-json")); got != nil {
		t.Fatalf("非法 JSON list = %v", got)
	}
	if got := rawMessageString(json.RawMessage("not-json")); got != "" {
		t.Fatalf("非法 JSON string = %q", got)
	}
	if !strings.Contains(entry.Model, "gpt") {
		t.Fatal("断言占位")
	}
}
