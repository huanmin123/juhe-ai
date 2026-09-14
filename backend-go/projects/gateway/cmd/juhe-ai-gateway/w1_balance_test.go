package main

// w1: compose_account_balance_refresh.go 纯函数面收割——模型协议画像匹配、
// 目录增量挑选、推荐健康检查模型（归档 account-model-catalog-refresh
// service 的移植）与快照/凭据投影。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
)

func TestW1ModelProtocolProfileArms(t *testing.T) {
	// 无协议画像：全放行。
	if !providerModelSupportsProtocolProfile(nil, "openai", "openai") {
		t.Fatal("空画像必须放行")
	}
	// gpt 供应商：恒放行。
	if !providerModelSupportsProtocolProfile([]string{"messages"}, "GPT", "openai") {
		t.Fatal("gpt 供应商必须放行")
	}
	// 未知协议码：无默认族 → 拒绝。
	if providerModelSupportsProtocolProfile([]string{"chat_completions"}, "openai", "unknown") {
		t.Fatal("未知协议码必须拒绝")
	}
	// 命中默认族。
	if !providerModelSupportsProtocolProfile([]string{"responses", "messages"}, "openai", "openai") {
		t.Fatal("responses 必须命中 openai 族")
	}
	if !providerModelSupportsProtocolProfile([]string{"messages"}, "anthropic", "anthropic") {
		t.Fatal("messages 必须命中 anthropic 族")
	}
	if !providerModelSupportsProtocolProfile([]string{"count_tokens"}, "gemini", "gemini") {
		t.Fatal("count_tokens 必须命中 gemini 族")
	}
	if providerModelSupportsProtocolProfile([]string{"messages"}, "gemini", "gemini") {
		t.Fatal("跨族必须拒绝")
	}
	// 默认族集合。
	families := defaultProtocolFamilies("OpenAI ")
	if !families["chat_completions"] || !families["responses"] || len(families) != 2 {
		t.Fatalf("openai families = %v", families)
	}
	if len(defaultProtocolFamilies("bogus")) != 0 {
		t.Fatal("未知协议码必须空集合")
	}
}

func TestW1AccountModelCatalogAdditions(t *testing.T) {
	upstreamIDs := map[string]bool{"gpt-5": true, "gpt-5-mini": true, "claude-x": true}
	local := []providers.ModelCatalogItem{
		{Model: " gpt-5 ", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "gpt-5-mini", SupportedAPIProtocols: []string{"messages"}},
		{Model: "claude-x", SupportedAPIProtocols: nil},
		{Model: "  ", SupportedAPIProtocols: nil},
		{Model: "dup", SupportedAPIProtocols: nil},
		{Model: "dup", SupportedAPIProtocols: nil},
	}
	// 空协议画像的本地模型直接放行（len==0 短路），未知协议码只拦有画像者。
	additions := accountModelCatalogAdditions([]string{"gpt-5"}, upstreamIDs, local, "openai", "unknown")
	if len(additions) != 1 || additions[0] != "claude-x" {
		t.Fatalf("unknown-code additions = %v", additions)
	}
	// openai 协议码：gpt-5 已选（跳过）、gpt-5-mini 协议不符（跳过）、
	// claude-x 不在已选列表且上游存在 → 加入。
	additions = accountModelCatalogAdditions([]string{"gpt-5"}, upstreamIDs, local, "openai", "openai")
	if len(additions) != 1 || additions[0] != "claude-x" {
		t.Fatalf("additions = %v", additions)
	}
	// 上游不存在：不加入。
	additions = accountModelCatalogAdditions([]string{}, map[string]bool{}, local, "openai", "openai")
	if len(additions) != 0 {
		t.Fatalf("no upstream = %v", additions)
	}
}

func TestW1RecommendedHealthCheckModel(t *testing.T) {
	upstreamIDs := map[string]bool{"gpt-5": true, "gpt-5-mini": true}
	models := []providers.ModelCatalogItem{
		{Model: "gpt-5", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "gpt-5-mini", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "not-upstream", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "gpt-5", SupportedAPIProtocols: []string{"chat_completions"}},
	}
	// 已配置模型在候选中：保持不变。
	if got := recommendedAccountHealthCheckModel(" gpt-5-mini ", upstreamIDs, models, "openai", "openai"); got != "gpt-5-mini" {
		t.Fatalf("configured = %q", got)
	}
	// 未配置或配置不在候选：取第一个候选。
	if got := recommendedAccountHealthCheckModel("missing", upstreamIDs, models, "openai", "openai"); got != "gpt-5" {
		t.Fatalf("fallback = %q", got)
	}
	// 无候选：空。
	if got := recommendedAccountHealthCheckModel("", nil, models, "openai", "openai"); got != "" {
		t.Fatalf("empty = %q", got)
	}
}
