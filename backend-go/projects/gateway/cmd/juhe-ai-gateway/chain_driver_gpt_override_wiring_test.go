package main

// D-151 P0 组合根接线单测：runtimecache 目录行 → 端口期望形态的映射表驱动
// 用例 + 进程级端口 Set 的组合冒烟（经测试间接层观察，不直接污染
// gatewaydispatch 全局状态）。

import (
	"testing"

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
			// gpt 家族前缀回退
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
			// 非 gpt vendor 回落默认语义：openai/hybrid 已随 vendors 表退役
			//（供应商与上游协议面重构设计 §1.1/§10），不再命中 openai 系候选
			// 规则。
			name:     "非 gpt vendor 回落默认语义（openai 已退役）",
			provider: "openai",
			model:    "gpt-4.1-nano-2025-04-14",
			expect:   []string{"gpt-4.1-nano-2025-04-14"},
		},
		{
			name:     "非 gpt vendor 回落默认语义（含空白归一）",
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

// 组合冒烟（T15 显式传参收敛后）：newChainProviderDriverWithCache 必须把目录
// 适配器与候选扩展器装进 driver 的 GptRequestOverrideRuntime 字段（否则能力
// 解析恒 nil，管理面配置的 service_tier / reasoning_effort 覆盖在运行面整段
// 惰性——P0 缺口本体）；进程级全局端口与 Set* 已删除，不存在其他注入面。
func TestNewChainProviderDriverWithCacheRegistersOverridePorts(t *testing.T) {
	nilCacheDriver := newChainProviderDriverWithCache(nil, nil)
	if nilCacheDriver.gptRequestOverrideRuntime.Catalog != nil {
		t.Fatalf("nil cache 不得装配端口（保持能力解析缺席语义）")
	}
	if nilCacheDriver.gptRequestOverrideRuntime.ModelCandidates != nil {
		t.Fatalf("nil cache 不得装配候选扩展器")
	}

	cache := &gatewayruntimecache.Service{}
	driver := newChainProviderDriverWithCache(cache, nil)
	if driver.gptRequestOverrideRuntime.Catalog == nil {
		t.Fatalf("cache 非空时必须装配目录端口")
	}
	if driver.gptRequestOverrideRuntime.ModelCandidates == nil {
		t.Fatalf("cache 非空时必须装配候选扩展器")
	}
	catalog, ok := driver.gptRequestOverrideRuntime.Catalog.(chainGptRequestOverrideModelCatalog)
	if !ok {
		t.Fatalf("装配的必须是 runtimecache 目录适配器，实际 %T", driver.gptRequestOverrideRuntime.Catalog)
	}
	if catalog.cache != cache {
		t.Fatalf("目录适配器必须携带注入的 runtime cache")
	}

	// 无 cache 构造器保持零值 runtime（能力解析缺席 = 覆盖惰性）。
	bare := newChainProviderDriver()
	if bare.gptRequestOverrideRuntime.Catalog != nil || bare.gptRequestOverrideRuntime.ModelCandidates != nil {
		t.Fatalf("newChainProviderDriver 必须保持零值 runtime: %+v", bare.gptRequestOverrideRuntime)
	}
}
