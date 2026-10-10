package main

// w1: chain_compose.go 客户端模型目录字典投影（scope 秩 / 条目投影）与 JSON
// 辅助投影直测。旧成员过滤（selectClientCatalogItems 及可见性/价格/运营排序）
// 已随 /v1/models 账户并集契约退役；字典构建行为见 chain_gatewaykeymodels_test.go。

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

func TestW1ClientCatalogScopeRank(t *testing.T) {
	if clientCatalogScopeRank(gatewayruntimecache.ProviderModelCatalogItem{Scope: "personal"}) != 3 {
		t.Fatal("personal rank 错误")
	}
	if clientCatalogScopeRank(gatewayruntimecache.ProviderModelCatalogItem{Scope: "global"}) != 2 {
		t.Fatal("global rank 错误")
	}
	if clientCatalogScopeRank(gatewayruntimecache.ProviderModelCatalogItem{}) != 1 {
		t.Fatal("default rank 错误")
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
