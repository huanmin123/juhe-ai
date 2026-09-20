package main

// D-151 P0 组合根接线单测：runtimecache 目录行 → 端口期望形态的映射表驱动
// 用例 + 进程级端口 Set 的组合冒烟（经测试间接层观察，不直接污染
// gatewaydispatch 全局状态）。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestChainGptRequestOverrideCatalogItemsOf(t *testing.T) {
	items := []gatewayruntimecache.ProviderModelCatalogItem{
		{
			Model:                     "gpt-5.4",
			Status:                    "active",
			SupportedServiceTiers:     []string{"default", "priority", "flex"},
			SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		},
		{
			// inactive 行不得进入能力解析。
			Model:  "gpt-5.4",
			Status: "inactive",
		},
		{
			// active 但无能力字段：保留（能力列表为空即不支持对应覆盖）。
			Model:  "gpt-5.3-codex",
			Status: "active",
		},
	}
	out := chainGptRequestOverrideCatalogItemsOf(items)
	if len(out) != 2 {
		t.Fatalf("期望 2 行（过滤 inactive），实际 %d：%+v", len(out), out)
	}
	if out[0].Model != "gpt-5.4" ||
		len(out[0].SupportedServiceTiers) != 3 ||
		len(out[0].SupportedReasoningEfforts) != 4 {
		t.Fatalf("第一行能力字段投影不符：%+v", out[0])
	}
	if out[1].Model != "gpt-5.3-codex" || out[1].SupportedServiceTiers != nil || out[1].SupportedReasoningEfforts != nil {
		t.Fatalf("第二行应为零能力字段的原样投影：%+v", out[1])
	}
}

func TestChainGptRequestOverrideModelCandidates(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		expect   []string
	}{
		{
			name:     "gpt 日期后缀剥离",
			provider: "gpt",
			model:    "gpt-5.4-2026-03-05",
			expect:   []string{"gpt-5.4-2026-03-05", "gpt-5.4"},
		},
		{
			name:     "gpt 紧凑日期后缀剥离",
			provider: "gpt",
			model:    "gpt-5.5-20260423",
			expect:   []string{"gpt-5.5-20260423", "gpt-5.5"},
		},
		{
			name:     "gpt 家族前缀回退",
			provider: "gpt",
			model:    "gpt-5.4-mini-2026-03-17",
			// Node 规则按前缀逐条命中：mini 基名与 gpt-5.4 家族基名都加入。
			expect: []string{"gpt-5.4-mini-2026-03-17", "gpt-5.4-mini", "gpt-5.4"},
		},
		{
			name:     "gpt 无后缀恒等",
			provider: "gpt",
			model:    "gpt-5.3-codex",
			expect:   []string{"gpt-5.3-codex"},
		},
		{
			name:     "openai 供应商码同规则",
			provider: "openai",
			model:    "gpt-4.1-nano-2025-04-14",
			expect:   []string{"gpt-4.1-nano-2025-04-14", "gpt-4.1-nano", "gpt-4.1"},
		},
		{
			name:     "非 openai 系回落默认语义",
			provider: "glm",
			model:    " glm-5 ",
			expect:   []string{" glm-5 ", "glm-5"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chainGptRequestOverrideModelCandidates(tc.provider, tc.model)
			if len(got) != len(tc.expect) {
				t.Fatalf("候选数不符：期望 %v，实际 %v", tc.expect, got)
			}
			for index := range tc.expect {
				if got[index] != tc.expect[index] {
					t.Fatalf("候选[%d] 不符：期望 %v，实际 %v", index, tc.expect, got)
				}
			}
		})
	}
}

// 组合冒烟：newChainProviderDriverWithCache 必须把目录适配器与候选扩展器
// Set 到 gatewayoauthcodex 的进程级端口（否则能力解析恒 nil，管理面配置的
// service_tier / reasoning_effort 覆盖在运行面整段惰性——P0 缺口本体）。
func TestNewChainProviderDriverWithCacheRegistersOverridePorts(t *testing.T) {
	var registeredCatalog gatewaydispatch.GptRequestOverrideModelCatalog
	var registeredCandidates func(providerCode, model string) []string
	originalCatalog := registerGptRequestOverrideCatalogPort
	originalCandidates := registerGptRequestOverrideModelCandidates
	t.Cleanup(func() {
		registerGptRequestOverrideCatalogPort = originalCatalog
		registerGptRequestOverrideModelCandidates = originalCandidates
	})
	registerGptRequestOverrideCatalogPort = func(catalog gatewaydispatch.GptRequestOverrideModelCatalog) {
		registeredCatalog = catalog
	}
	registerGptRequestOverrideModelCandidates = func(expander func(providerCode, model string) []string) {
		registeredCandidates = expander
	}

	newChainProviderDriverWithCache(nil, nil)
	if registeredCatalog != nil || registeredCandidates != nil {
		t.Fatalf("nil cache 不得注册端口（保持能力解析缺席语义）")
	}

	newChainProviderDriverWithCache(&gatewayruntimecache.Service{}, nil)
	if registeredCatalog == nil {
		t.Fatalf("cache 非空时必须注册目录端口")
	}
	if registeredCandidates == nil {
		t.Fatalf("cache 非空时必须注册候选扩展器")
	}
	if _, ok := registeredCatalog.(chainGptRequestOverrideModelCatalog); !ok {
		t.Fatalf("注册的必须是 runtimecache 目录适配器，实际 %T", registeredCatalog)
	}
}
