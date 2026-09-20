package main

// worker_usage_pricing_catalog.go 的适配器测试：目录行映射/合并/过滤表驱动、
// ResolvePricingModel 解析链、BuildBreakdown 计费引擎表驱动（对照
// gateway/internal/pricing 语义）、freeze 集成与目录缺失降级、TTL 缓存。

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

func pricingCatalogFixtureSQL(t *testing.T, dir string) *usagePricingCatalog {
	t.Helper()
	db := openTestSQLite(t, filepath.Join(dir, "business-pricing.sqlite3"))
	mustExec(t, db,
		`CREATE TABLE IF NOT EXISTS provider_model_catalog (
          id TEXT PRIMARY KEY,
          provider_code TEXT NOT NULL,
          model TEXT NOT NULL,
          status TEXT NOT NULL DEFAULT 'active',
          mode TEXT,
          catalog_order INTEGER,
          shutdown_date TEXT,
          catalog_visible INTEGER NOT NULL DEFAULT 1,
          supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
          service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
          input_usd_per_1m REAL, output_usd_per_1m REAL,
          cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL,
          cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL,
          image_input_usd_per_1m REAL, image_output_usd_per_1m REAL,
          audio_input_usd_per_1m REAL, audio_output_usd_per_1m REAL,
          output_usd_per_image REAL,
          long_context_input_token_threshold INTEGER,
          long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
          long_context_input_cost_multiplier REAL,
          long_context_output_cost_multiplier REAL
        )`,
		// 列集按 maintenance 真实 DDL（sqlite_schema_business.go / pg_schema_business
		// tables.go 的 custom_provider_models）：无长上下文 4 列——该表在生产就
		// 没有这些列，伪造会让 loadCustom 每查必错还被测试掩盖。
		`CREATE TABLE IF NOT EXISTS custom_provider_models (
          id TEXT PRIMARY KEY,
          provider_code TEXT NOT NULL,
          model TEXT NOT NULL,
          scope TEXT NOT NULL DEFAULT 'personal',
          system_account_id TEXT,
          status TEXT NOT NULL DEFAULT 'active',
          mode TEXT,
          shutdown_date TEXT,
          supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
          service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
          input_usd_per_1m REAL, output_usd_per_1m REAL,
          cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL,
          cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL,
          image_input_usd_per_1m REAL, image_output_usd_per_1m REAL,
          audio_input_usd_per_1m REAL, audio_output_usd_per_1m REAL,
          output_usd_per_image REAL
        )`,
		// 内置目录：openai 计费主行 + 过滤臂（下线/隐藏/草稿）+ anthropic 行 +
		// tier 混合行 + gemini 长上下文行。
		`INSERT INTO provider_model_catalog (id, provider_code, model, status, mode, catalog_order,
          supported_service_tiers_json, service_tier_prices_json,
          input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m,
          cache_write_usd_per_1m, cache_write_1h_usd_per_1m,
          long_context_input_token_threshold, long_context_input_token_threshold_inclusive,
          long_context_input_cost_multiplier, long_context_output_cost_multiplier)
         VALUES ('b1', 'openai', 'gpt-test', 'active', 'chat', 1,
          '["priority"]', '{"priority":{"inputUsdPer1M":1.0,"outputUsdPer1M":2.0,"cachedInputUsdPer1M":0.2,"cacheWriteUsdPer1M":2.0,"cacheWrite1hUsdPer1M":3.0}}',
          0.5, 1.5, 0.125, 1.25, 2.5, 200000, 0, 2.0, 2.0)`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, status, shutdown_date) VALUES ('b2', 'openai', 'gpt-stale', 'active', '2020-01-01')`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, status, catalog_visible) VALUES ('b3', 'openai', 'gpt-hidden', 'active', 0)`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, status) VALUES ('b4', 'openai', 'gpt-draft', 'draft')`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, status, input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m) VALUES ('b5', 'anthropic', 'claude-test', 'active', 3.0, 15.0, 0.3)`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, status, supported_service_tiers_json, service_tier_prices_json, input_usd_per_1m, output_usd_per_1m)
         VALUES ('b6', 'openai', 'gpt-mixed', 'active', '["flex"]', '{"flex":{"inputUsdPer1M":1.0}}', 0.5, 1.5)`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, status, input_usd_per_1m, output_usd_per_1m, long_context_input_token_threshold, long_context_input_cost_multiplier, long_context_output_cost_multiplier)
         VALUES ('b7', 'gemini', 'gem-test', 'active', 0.5, 1.5, 200000, 2.0, 2.0)`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, status, input_usd_per_1m, output_usd_per_1m, long_context_input_token_threshold, long_context_input_cost_multiplier, long_context_output_cost_multiplier)
         VALUES ('b8', 'openai', 'gpt-dual', 'active', 0.7, 1.7, 200000, 2.0, 2.0)`,
		// 自定义目录：global 覆盖内置（gpt-dual，覆盖后长上下文定价被清空——
		// custom 表无这些列）；personal 命中本账户、不命中他账户。
		`INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status, input_usd_per_1m, output_usd_per_1m)
         VALUES ('c1', 'openai', 'gpt-dual', 'global', NULL, 'active', 2.0, 3.0)`,
		`INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status, output_usd_per_1m)
         VALUES ('c2', 'openai', 'gpt-personal', 'personal', 'sys_a', 'active', 9.9)`,
		`INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status, output_usd_per_1m)
         VALUES ('c3', 'openai', 'gpt-other-sys', 'personal', 'sys_b', 'active', 8.8)`,
		`INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status) VALUES ('c4', 'openai', 'gpt-disabled', 'global', NULL, 'disabled')`,
	)
	return newUsagePricingCatalog(db, false)
}

func assertCostClose(t *testing.T, label string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s 必须为 %.10f，实际 nil", label, want)
	}
	if math.Abs(*got-want) > 1e-12 {
		t.Fatalf("%s = %.12f want %.12f", label, *got, want)
	}
}

// TestUsagePricingCatalogRowMapping 锁定目录读取面：可见性/下线/状态过滤、
// global/personal scope 窗口与 built_in < global < personal 优先级合并。
func TestUsagePricingCatalogRowMapping(t *testing.T) {
	catalog := pricingCatalogFixtureSQL(t, t.TempDir())
	ctx := context.Background()

	rows, err := catalog.load(ctx, "openai", "sys_a")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, model := range []string{"gpt-test", "gpt-personal"} {
		if _, ok := rows[model]; !ok {
			t.Fatalf("模型 %s 必须在目录中（got %v）", model, mapKeys(rows))
		}
	}
	for _, model := range []string{"gpt-stale", "gpt-hidden", "gpt-draft", "gpt-other-sys", "gpt-disabled", "claude-test"} {
		if _, ok := rows[model]; ok {
			t.Fatalf("模型 %s 必须被过滤", model)
		}
	}
	// 优先级：global 自定义覆盖内置行。
	if rows["gpt-dual"].inputUsdPer1M == nil || *rows["gpt-dual"].inputUsdPer1M != 2.0 {
		t.Fatalf("global 自定义必须覆盖内置定价: %+v", rows["gpt-dual"])
	}
	if got := rows["gpt-dual"].scope; got != "global" {
		t.Fatalf("gpt-dual scope = %s want global", got)
	}
	// custom 行无长上下文定价：覆盖内置行后阈值字段必须为 nil（内置行的
	// 阈值字段保留）。
	if rows["gpt-dual"].longContextInputTokenThreshold != nil {
		t.Fatal("custom 覆盖后的行不得携带长上下文阈值")
	}
	if rows["gpt-test"].longContextInputTokenThreshold == nil {
		t.Fatal("内置行的长上下文阈值必须保留")
	}
	// personal scope 归属。
	if got := rows["gpt-personal"].scope; got != "personal" {
		t.Fatalf("gpt-personal scope = %s want personal", got)
	}
	// 无 system account：global + 内置，personal 全部缺席。
	rowsGlobal, err := catalog.load(ctx, "openai", "")
	if err != nil {
		t.Fatalf("load(global): %v", err)
	}
	if _, ok := rowsGlobal["gpt-personal"]; ok {
		t.Fatal("无 system account 时 personal 行必须缺席")
	}
	if _, ok := rowsGlobal["gpt-dual"]; !ok {
		t.Fatal("无 system account 时 global 行必须在")
	}
}

func mapKeys(rows map[string]usageCatalogPricingRow) []string {
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	return keys
}

// TestUsagePricingCatalogResolvePricingModel 表驱动解析链：upstream 优先、
// requested 回退、trim 相等、未解析空串。
func TestUsagePricingCatalogResolvePricingModel(t *testing.T) {
	catalog := pricingCatalogFixtureSQL(t, t.TempDir())
	cases := []struct {
		name      string
		upstream  string
		requested string
		want      string
	}{
		{"upstream命中", "gpt-test", "gpt-other", "gpt-test"},
		{"trim匹配", "  gpt-test  ", "", "gpt-test"},
		{"requested回退", "gpt-missing", "gpt-personal", "gpt-personal"},
		{"全部未命中", "gpt-missing", "gpt-also-missing", ""},
		{"空模型", "", "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := catalog.ResolvePricingModel(context.Background(), "openai", "sys_a", testCase.upstream, testCase.requested)
			if got != testCase.want {
				t.Fatalf("ResolvePricingModel = %q want %q", got, testCase.want)
			}
		})
	}
}

// TestUsagePricingCatalogBuildBreakdown 表驱动计费引擎：标准档、tier 专属/
// 混合、长上下文乘数（含 inclusive/exclusive 边界）、anthropic tier 拒绝、
// 未知 provider、无定价收敛、costUsd 覆盖、1h 缓存写拆分。
func TestUsagePricingCatalogBuildBreakdown(t *testing.T) {
	catalog := pricingCatalogFixtureSQL(t, t.TempDir())
	ctx := context.Background()
	intPtr := func(v int) *int { return &v }

	cases := []struct {
		name string
		row  string
		// 需要专用目录行时直接内联构造。
		providerCode string
		systemAcct   string
		serviceTier  string
		input        usagewriter.UsageRecordInput
		wantNil      bool
		verify       func(t *testing.T, breakdown *usagewriter.CostBreakdown)
	}{
		{
			name:         "openai标准档缓存读计入输入",
			providerCode: "openai", systemAcct: "sys_a",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "openai", Model: "gpt-test", UpstreamModel: "gpt-test",
				InputTokens: intPtr(10000), OutputTokens: intPtr(500), CacheReadTokens: intPtr(2000),
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				// uncached input = max(10000-2000,0)=8000 → 0.004；
				// output 500/1M*1.5 = 0.00075；cache read 2000/1M*0.125 = 0.00025。
				assertCostClose(t, "accountChargeUsd", breakdown.AccountChargeUsd, 0.005)
				assertCostClose(t, "inputCostUsd", breakdown.InputCostUsd, 0.004)
				assertCostClose(t, "cacheReadCostUsd", breakdown.CacheReadCostUsd, 0.00025)
				if breakdown.ServiceTierPricingSource != usagewriter.TierSourceDefault {
					t.Fatalf("tier source = %s want default", breakdown.ServiceTierPricingSource)
				}
				if breakdown.BillingPolicy != "openai" || breakdown.Currency != "USD" {
					t.Fatalf("policy/currency = %s/%s", breakdown.BillingPolicy, breakdown.Currency)
				}
			},
		},
		{
			name: "tier专属费率",
			row:  "openai-standard", providerCode: "openai",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "openai", UpstreamModel: "gpt-test",
				InputTokens: intPtr(10000), OutputTokens: intPtr(100),
				BilledServiceTier: "priority",
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				// tier 表 input 1.0 / output 2.0。
				assertCostClose(t, "inputCostUsd", breakdown.InputCostUsd, 0.01)
				assertCostClose(t, "outputCostUsd", breakdown.OutputCostUsd, 0.0002)
				if breakdown.ServiceTierPricingSource != usagewriter.TierSourceTierSpecific {
					t.Fatalf("tier source = %s want tier_specific", breakdown.ServiceTierPricingSource)
				}
			},
		},
		{
			name: "tier混合来源",
			row:  "openai-mixed", providerCode: "openai",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "openai", UpstreamModel: "gpt-mixed",
				InputTokens: intPtr(1000), BilledServiceTier: "flex",
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				assertCostClose(t, "inputCostUsd", breakdown.InputCostUsd, 0.001)
				if breakdown.ServiceTierPricingSource != usagewriter.TierSourceMixed {
					t.Fatalf("tier source = %s want mixed", breakdown.ServiceTierPricingSource)
				}
			},
		},
		{
			name: "长上下文乘数生效",
			row:  "gemini-longctx", providerCode: "gemini",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "gemini", UpstreamModel: "gem-test",
				InputTokens: intPtr(250000), OutputTokens: intPtr(100),
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				// 250000 > 200000（exclusive）→ 费率翻倍：0.25 + 0.0003。
				assertCostClose(t, "inputCostUsd", breakdown.InputCostUsd, 0.25)
				assertCostClose(t, "outputCostUsd", breakdown.OutputCostUsd, 0.0003)
			},
		},
		{
			name: "长上下文阈值边界不生效",
			row:  "gemini-longctx", providerCode: "gemini",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "gemini", UpstreamModel: "gem-test",
				InputTokens: intPtr(200000), OutputTokens: intPtr(100),
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				// 200000 不大于阈值（exclusive）→ 标准费率。
				assertCostClose(t, "inputCostUsd", breakdown.InputCostUsd, 0.1)
			},
		},
		{
			name: "custom覆盖清空长上下文定价",
			row:  "openai-custom-dual", providerCode: "openai",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "openai", UpstreamModel: "gpt-dual",
				InputTokens: intPtr(300000), OutputTokens: intPtr(100),
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				// custom 行（global 覆盖内置 b8）无长上下文列 → 300000 输入
				// 不得乘 2：input 300000/1M*2.0 = 0.6；output 100/1M*3.0 = 0.0003。
				assertCostClose(t, "inputCostUsd", breakdown.InputCostUsd, 0.6)
				assertCostClose(t, "outputCostUsd", breakdown.OutputCostUsd, 0.0003)
			},
		},
		{
			name: "anthropic不支持tier拒绝",
			row:  "anthropic-row", providerCode: "anthropic",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "anthropic", UpstreamModel: "claude-row",
				InputTokens: intPtr(1000), BilledServiceTier: "priority",
			},
			wantNil: true,
		},
		{
			name: "anthropic标准档缓存读费率",
			row:  "anthropic-row", providerCode: "anthropic",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "anthropic", UpstreamModel: "claude-test",
				InputTokens: intPtr(1000), CacheReadTokens: intPtr(500),
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				// anthropic 无 cacheReadFallbackToInput、缓存读不计入输入：
				// uncached input=1000 → 0.003；cacheRead 500/1M*0.30 = 0.00015。
				assertCostClose(t, "inputCostUsd", breakdown.InputCostUsd, 0.003)
				assertCostClose(t, "cacheReadCostUsd", breakdown.CacheReadCostUsd, 0.00015)
				if breakdown.BillingPolicy != "anthropic" {
					t.Fatalf("policy = %s want anthropic", breakdown.BillingPolicy)
				}
			},
		},
		{
			name: "1h缓存写拆分",
			row:  "openai-standard", providerCode: "openai",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "openai", UpstreamModel: "gpt-test",
				InputTokens: intPtr(100), CacheWriteTokens: intPtr(1000), CacheWrite1hTokens: intPtr(600),
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				// 标准 400@1.25 = 0.0005；1h 600@2.5 = 0.0015。
				assertCostClose(t, "cacheWriteCostUsd", breakdown.CacheWriteCostUsd, 0.0005)
				assertCostClose(t, "cacheWrite1hCostUsd", breakdown.CacheWrite1hCostUsd, 0.0015)
			},
		},
		{
			name: "costUsd覆盖账户费用",
			row:  "openai-standard", providerCode: "openai",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "openai", UpstreamModel: "gpt-test",
				InputTokens: intPtr(10000), OutputTokens: intPtr(500),
				CostUsd: floatPtr(0.42),
			},
			verify: func(t *testing.T, breakdown *usagewriter.CostBreakdown) {
				assertCostClose(t, "accountChargeUsd", breakdown.AccountChargeUsd, 0.42)
			},
		},
		{
			name:         "未知provider",
			providerCode: "unknown-provider",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "unknown-provider", UpstreamModel: "gpt-test",
				InputTokens: intPtr(100),
			},
			wantNil: true,
		},
		{
			name:         "模型不在目录",
			providerCode: "openai",
			input: usagewriter.UsageRecordInput{
				ProviderCode: "openai", UpstreamModel: "gpt-never",
				InputTokens: intPtr(100),
			},
			wantNil: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			breakdown := catalog.BuildBreakdown(ctx, testCase.providerCode, testCase.systemAcct, testCase.input.UpstreamModel, testCase.input.BilledServiceTier, testCase.input)
			if testCase.wantNil {
				if breakdown != nil {
					t.Fatalf("breakdown 必须为 nil: %+v", breakdown)
				}
				return
			}
			if breakdown == nil {
				t.Fatal("breakdown 不应为 nil（目录行存在）")
			}
			testCase.verify(t, breakdown)
		})
	}
}

func floatPtr(v float64) *float64 { return &v }

// TestUsagePricingCatalogFreezeWithFallback 集成冻结路径：目录命中产出
// 完整快照并补齐 costUsd；目录缺失保留确定性 fallback（不得删的降级面）。
func TestUsagePricingCatalogFreezeWithFallback(t *testing.T) {
	catalog := pricingCatalogFixtureSQL(t, t.TempDir())
	ctx := context.Background()
	inputTokens, outputTokens := 10000, 500

	frozen := usagewriter.FreezeUsageRecordPricingFacts(ctx, usagewriter.UsageRecordInput{
		ProviderCode: "openai", Model: "gpt-test", UpstreamModel: "gpt-test",
		SystemAccountID: "sys_a",
		InputTokens:     &inputTokens, OutputTokens: &outputTokens,
	}, catalog, true)
	if frozen.PricingSnapshot == nil {
		t.Fatal("目录命中必须产出定价快照")
	}
	snapshot := frozen.PricingSnapshot.(*usagewriter.CostBreakdown)
	// input 10000/1M*0.5 = 0.005 + output 500/1M*1.5 = 0.00075。
	assertCostClose(t, "frozen accountChargeUsd", snapshot.AccountChargeUsd, 0.00575)
	if frozen.CostUsd == nil {
		t.Fatal("enrich 必须补齐 costUsd")
	}
	if frozen.PricingModel != "gpt-test" {
		t.Fatalf("pricingModel = %s want gpt-test", frozen.PricingModel)
	}

	missing := usagewriter.FreezeUsageRecordPricingFacts(ctx, usagewriter.UsageRecordInput{
		ProviderCode: "openai", Model: "gpt-never", UpstreamModel: "gpt-never",
		SystemAccountID: "sys_a",
		InputTokens:     &inputTokens, OutputTokens: &outputTokens,
	}, catalog, true)
	if missing.PricingSnapshot == nil {
		t.Fatal("目录缺失必须保留确定性 fallback 快照")
	}
	fallback := missing.PricingSnapshot.(*usagewriter.CostBreakdown)
	if fallback.ServiceTierPricingSource != usagewriter.TierSourceUnknown || fallback.Multiplier != 1 {
		t.Fatalf("fallback 快照语义错误: %+v", fallback)
	}
	if missing.CostUsd != nil {
		t.Fatalf("fallback 不得凭空补 costUsd: %v", missing.CostUsd)
	}
}

// TestUsagePricingCatalogCacheTTL 锁定短 TTL 缓存语义：窗口内读缓存（改库
// 不生效），窗口过期重查。
func TestUsagePricingCatalogCacheTTL(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "ttl.sqlite3"))
	// 与适配器 SELECT 列集一致的完整表形状（列缺失会让查询失败并被负缓存）。
	mustExec(t, db,
		`CREATE TABLE provider_model_catalog (
          id TEXT PRIMARY KEY,
          provider_code TEXT NOT NULL,
          model TEXT NOT NULL,
          status TEXT NOT NULL DEFAULT 'active',
          mode TEXT,
          catalog_order INTEGER,
          shutdown_date TEXT,
          catalog_visible INTEGER NOT NULL DEFAULT 1,
          supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
          service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
          input_usd_per_1m REAL, output_usd_per_1m REAL,
          cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL,
          cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL,
          image_input_usd_per_1m REAL, image_output_usd_per_1m REAL,
          audio_input_usd_per_1m REAL, audio_output_usd_per_1m REAL,
          output_usd_per_image REAL,
          long_context_input_token_threshold INTEGER,
          long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
          long_context_input_cost_multiplier REAL,
          long_context_output_cost_multiplier REAL
        )`,
		`INSERT INTO provider_model_catalog (id, provider_code, model, input_usd_per_1m) VALUES ('x', 'openai', 'm1', 1.0)`,
		// load 同时读 custom_provider_models（maintenance 真实形状：无长上下文
		// 4 列，空表即可；列缺失/缺表会让整链报错并进负缓存）。
		`CREATE TABLE custom_provider_models (
          id TEXT PRIMARY KEY,
          provider_code TEXT NOT NULL,
          model TEXT NOT NULL,
          scope TEXT NOT NULL DEFAULT 'personal',
          system_account_id TEXT,
          status TEXT NOT NULL DEFAULT 'active',
          mode TEXT,
          shutdown_date TEXT,
          supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
          service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
          input_usd_per_1m REAL, output_usd_per_1m REAL,
          cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL,
          cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL,
          image_input_usd_per_1m REAL, image_output_usd_per_1m REAL,
          audio_input_usd_per_1m REAL, audio_output_usd_per_1m REAL,
          output_usd_per_image REAL
        )`)
	now := time.Unix(1_000_000, 0)
	catalog := newUsagePricingCatalog(db, false)
	catalog.now = func() time.Time { return now }
	ctx := context.Background()

	if got := catalog.ResolvePricingModel(ctx, "openai", "", "m1", ""); got != "m1" {
		t.Fatalf("首查必须命中: %q", got)
	}
	mustExec(t, db, `UPDATE provider_model_catalog SET model = 'm2' WHERE id = 'x'`)
	if got := catalog.ResolvePricingModel(ctx, "openai", "", "m2", ""); got != "" {
		t.Fatalf("TTL 窗口内必须读缓存: %q", got)
	}
	catalog.now = func() time.Time { return now.Add(usagePricingCatalogCacheTTL + time.Second) }
	if got := catalog.ResolvePricingModel(ctx, "openai", "", "m2", ""); got != "m2" {
		t.Fatalf("TTL 过期后必须重查: %q", got)
	}
}

// TestUsagePricingCatalogSQLiteFilterArms 收尾过滤臂：status/shutdown 的
// fail-closed 语义（禁用行绝不参与计价）。
func TestUsagePricingCatalogSQLiteFilterArms(t *testing.T) {
	catalog := pricingCatalogFixtureSQL(t, t.TempDir())
	rows, err := catalog.load(context.Background(), "openai", "sys_a")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, model := range []string{"gpt-stale", "gpt-disabled"} {
		if _, ok := rows[model]; ok {
			t.Fatalf("过滤臂失效: %s 不应出现在目录", model)
		}
	}
	if strings.Contains(mapKeysCompact(rows), "claude") {
		t.Fatal("跨 provider 行不得进入 openai 目录")
	}
}

func mapKeysCompact(rows map[string]usageCatalogPricingRow) string {
	return strings.Join(mapKeys(rows), ",")
}
