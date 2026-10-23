package main

// T1 vendor 身份端到端用例（供应商与上游协议面重构设计 §1.1/§2.1）：真实 SQL
// 种子（accounts.vendor_code / 连接行 / models 目标形态支持模型）→ 候选投影
// （openAIAccountSecretFromRow 水合）→ secret.VendorCode →
// ApplyGptAccountRequestOverridesToUpstreamBody 按供应商身份改写出站 body。
//
// 覆盖形状：
//   - 新建目标形态：gpt 与 xai 两种 vendor（vendor_code + connection_driver_code，
//     不写 provider_code 旧列）；
//   - 存量形态等价臂：vendor_code 为 NULL——身份必须水合为空串（provider_code
//     旧列已随存储迁移退役，禁止任何旧列回落路径），覆盖保持惰性（空 vendor
//     跳过 gpt 枚举、不查目录、不改写 body）；
//   - gpt 专属 wire 枚举按供应商生效：同一配置值（reasoning_effort_override
//     = "ultra"）对 gpt 账户是请求级硬失败，对 xai 账户经目录能力门正常生效。
//
// 目录端口以测试替身经 GptRequestOverrideRuntime 显式传入（T15 收敛后无进程级
// Set*）：真实 runtimecache 目录适配器的投影已有
// TestChainGptRequestOverrideCatalogItemsOf / 接线冒烟覆盖，本用例的断言焦点
// 是 vendor 身份从 DB 行贯穿到覆盖运行时，不在目录缓存读本身。

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// vendorOverrideCatalogStub 是本用例的目录端口测试替身：按 providerCode 返回
// 声明能力行，并记录每次调用的 providerCode（断言 vendor 身份进入能力解析，
// 且空 vendor 不触发目录查询）。
type vendorOverrideCatalogStub struct {
	rows  map[string][]gatewaydispatch.GptRequestOverrideModelCatalogItem
	calls []string
}

func (m *vendorOverrideCatalogStub) ListGptRequestOverrideModelCatalog(
	_ context.Context, providerCode, _ string, _ bool,
) ([]gatewaydispatch.GptRequestOverrideModelCatalogItem, error) {
	m.calls = append(m.calls, providerCode)
	return m.rows[providerCode], nil
}

func TestChainVendorIdentityFromSeedToOverrideBody(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	encrypt := func(credentials map[string]any) string {
		t.Helper()
		encrypted, err := accounts.EncryptJSON("chain-test-secret", credentials)
		if err != nil {
			t.Fatalf("encrypt credentials: %v", err)
		}
		return encrypted
	}

	// 目标形态 models 单表行：vendor 维度声明能力（supported_*_json 是目录
	// 能力门的数据契约；测试替身按 vendor 返回同值，DB 行留作形态证据）。
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
			supported_endpoint_modes_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			input_usd_per_1m, created_at, updated_at)
		VALUES ('mdl_v_gpt', 'gpt', 'gpt-test', 'gpt-test', 'global', 'active',
			'["chat_json","chat_sse","responses_json","responses_sse"]', '["priority"]', '["high"]', 1.0, ?, ?),
		       ('mdl_v_xai', 'xai', 'grok-4', 'grok-4', 'global', 'active',
			'["chat_json","chat_sse"]', '["priority"]', '["ultra"]', 1.0, ?, ?)`,
		now, now, now, now)
	// B4 目标形态：account_supported_models 读取收敛为 (account_id, model_id)
	// JOIN models；夹具近似 DDL 保留 NOT NULL 旧 model 列，按值回填保持行合法
	//（装载器只消费 model_id JOIN，见 loadSupportedModelsByAccountIds）。
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES ('acc_v_gpt', 'gpt-test', 'mdl_v_gpt', ?), ('acc_v_xai', 'openai', 'grok-4', 'mdl_v_xai', ?)`, now, now)

	// 四个账户行（目标形态不写 provider_code 旧列；列已随存储迁移退役，
	// vendor_code NULL 即身份空串——回落禁用由读面 COALESCE 保证）：
	//   acc_v_gpt      gpt 形态，两覆盖配置值均在 gpt wire 枚举内；
	//   acc_v_xai      xai 形态，reasoning_effort_override="ultra" 不在 gpt 枚举内；
	//   acc_v_gpt_bad  gpt 形态，reasoning_effort_override="ultra"（枚举硬失败臂）；
	//   acc_v_legacy   存量形态等价臂：vendor_code NULL → 身份水合为空串。
	seedAccounts := map[string]struct {
		vendorCode  any
		credentials map[string]any
	}{
		"acc_v_gpt":     {vendorCode: "gpt", credentials: map[string]any{"api_key": "sk-v-gpt", "service_tier_override": "priority", "reasoning_effort_override": "high"}},
		"acc_v_xai":     {vendorCode: "xai", credentials: map[string]any{"api_key": "sk-v-xai", "service_tier_override": "priority", "reasoning_effort_override": "ultra"}},
		"acc_v_gpt_bad": {vendorCode: "gpt", credentials: map[string]any{"api_key": "sk-v-gpt-bad", "reasoning_effort_override": "ultra"}},
		"acc_v_legacy":  {vendorCode: nil, credentials: map[string]any{"api_key": "sk-v-legacy", "reasoning_effort_override": "ultra"}},
	}
	for id, spec := range seedAccounts {
		var vendorColumn any
		if spec.vendorCode != nil {
			vendorColumn = spec.vendorCode
		}
		seed(`INSERT INTO accounts (
				id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
				name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
			) VALUES (?, ?, ?, ?, 'openai', 'v1', ?, 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
			id, fixture.systemAccount, vendorColumn, spec.vendorCode,
			"账户 "+id, encrypt(spec.credentials))
		seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES (?, ?, ?, 1, ?)`, fixture.groupID, fixture.systemAccount, id, now)
		// 单连接事实源：enabled 连接行 + 显式端点模式 + health-check 回填。
		chainSeedAccountConnection(t, db, id, "https://"+strings.ReplaceAll(id, "_", "-")+".example.invalid/v1",
			"chat_json", "chat_sse")
	}

	// ① → ② 真实 SQL 候选投影：runtime cache selector 读种子行并水合 secret。
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(),
		fixture.groupID, fixture.systemAccount, gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	secrets := map[string]*gatewayruntimecache.OpenAIAccountSecret{}
	for index := range result.Accounts {
		secrets[result.Accounts[index].ID] = &result.Accounts[index]
	}

	// ③ secret.VendorCode 水合断言（gpt / xai 两种厂商形状）。
	gptSecret := secrets["acc_v_gpt"]
	if gptSecret == nil {
		t.Fatalf("acc_v_gpt 未进候选窗口：accounts=%v", result.Accounts)
	}
	if gptSecret.VendorCode != "gpt" || gptSecret.ConnectionDriverCode != "gpt" {
		t.Fatalf("acc_v_gpt vendor 水合 = %q/%q, want gpt/gpt", gptSecret.VendorCode, gptSecret.ConnectionDriverCode)
	}
	if gptSecret.BaseURL != "https://acc-v-gpt.example.invalid/v1" {
		t.Fatalf("acc_v_gpt baseURL = %q（连接行事实源）", gptSecret.BaseURL)
	}
	if gptSecret.Credentials["service_tier_override"] != "priority" || gptSecret.Credentials["reasoning_effort_override"] != "high" {
		t.Fatalf("acc_v_gpt credentials 覆盖键 = %v", gptSecret.Credentials)
	}
	if !reflect.DeepEqual(gptSecret.SupportedModels, []string{"gpt-test"}) {
		t.Fatalf("acc_v_gpt supportedModels = %v（models 目标形态 JOIN）", gptSecret.SupportedModels)
	}

	xaiSecret := secrets["acc_v_xai"]
	if xaiSecret == nil {
		t.Fatalf("acc_v_xai 未进候选窗口：accounts=%v", result.Accounts)
	}
	if xaiSecret.VendorCode != "xai" || xaiSecret.ConnectionDriverCode != "xai" {
		t.Fatalf("acc_v_xai vendor 水合 = %q/%q, want xai/xai", xaiSecret.VendorCode, xaiSecret.ConnectionDriverCode)
	}

	// ⑤ 存量形态等价臂：vendor_code NULL → 身份必须为空串
	// （provider_code 旧列已退役，投影层 COALESCE(vendor_code,'') 无回落来源）。
	legacySecret := secrets["acc_v_legacy"]
	if legacySecret == nil {
		t.Fatalf("acc_v_legacy 未进候选窗口：accounts=%v", result.Accounts)
	}
	if legacySecret.VendorCode != "" {
		t.Fatalf("存量行 vendor 水合 = %q, want 空串（无 vendor 身份来源）", legacySecret.VendorCode)
	}

	// ④ 覆盖改写出站 body：目录替身经 GptRequestOverrideRuntime 显式传入
	// （T15 收敛：进程级 Set* 已删除，runtime 随调用显式携带，不污染其他用例）。
	catalog := &vendorOverrideCatalogStub{rows: map[string][]gatewaydispatch.GptRequestOverrideModelCatalogItem{
		"gpt": {{Model: "gpt-test", SupportedServiceTiers: []string{"priority"}, SupportedReasoningEfforts: []string{"high"}}},
		"xai": {{Model: "grok-4", SupportedServiceTiers: []string{"priority"}, SupportedReasoningEfforts: []string{"ultra"}}},
	}}
	overrideRuntime := gatewaydispatch.GptRequestOverrideRuntime{Catalog: catalog}

	// gpt 形状（responses 族）：service_tier 顶层写入 + reasoning.effort 嵌套。
	gptBody := []byte(`{"model":"gpt-test","input":"hi"}`)
	gptOut, err := gatewaydispatch.ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), gptBody, nil, *gptSecret, "responses", false, "gpt-test", "", overrideRuntime)
	if err != nil {
		t.Fatalf("gpt 覆盖应用失败: %v", err)
	}
	gptParsed := map[string]any{}
	if err := json.Unmarshal(gptOut, &gptParsed); err != nil {
		t.Fatalf("gpt 输出非 JSON: %v", err)
	}
	if gptParsed["service_tier"] != "priority" {
		t.Fatalf("gpt service_tier = %v, want priority（凭据改写生效）", gptParsed["service_tier"])
	}
	gptReasoning, _ := gptParsed["reasoning"].(map[string]any)
	if gptReasoning == nil || gptReasoning["effort"] != "high" {
		t.Fatalf("gpt reasoning = %v, want effort=high（responses 族 wire 形态）", gptParsed["reasoning"])
	}
	if _, exists := gptParsed["reasoning_effort"]; exists {
		t.Fatalf("gpt responses 族不得写 reasoning_effort 顶层键: %v", gptParsed)
	}

	// xai 形状（chat_completions 族）：同一 "ultra" 值在 gpt 枚举下是硬失败，
	// xai 账户跳过 gpt 枚举、经目录能力门正常改写（供应商感知判据本体）。
	xaiBody := []byte(`{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`)
	xaiOut, err := gatewaydispatch.ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), xaiBody, nil, *xaiSecret, "chat_completions", false, "grok-4", "", overrideRuntime)
	if err != nil {
		t.Fatalf("xai 覆盖应用失败（gpt 枚举不得外溢到其它 vendor）: %v", err)
	}
	xaiParsed := map[string]any{}
	if err := json.Unmarshal(xaiOut, &xaiParsed); err != nil {
		t.Fatalf("xai 输出非 JSON: %v", err)
	}
	if xaiParsed["reasoning_effort"] != "ultra" {
		t.Fatalf("xai reasoning_effort = %v, want ultra（目录能力门放行）", xaiParsed["reasoning_effort"])
	}
	if xaiParsed["service_tier"] != "priority" {
		t.Fatalf("xai service_tier = %v, want priority", xaiParsed["service_tier"])
	}
	if _, exists := xaiParsed["reasoning"]; exists {
		t.Fatalf("xai chat_completions 族不得写 reasoning 嵌套键: %v", xaiParsed)
	}

	// gpt 专属枚举臂： hydrated gpt 账户配 "ultra" 必须请求级硬失败。
	badSecret := secrets["acc_v_gpt_bad"]
	if badSecret == nil {
		t.Fatalf("acc_v_gpt_bad 未进候选窗口：accounts=%v", result.Accounts)
	}
	badBody := []byte(`{"model":"gpt-test","input":"hi"}`)
	if _, err := gatewaydispatch.ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), badBody, nil, *badSecret, "responses", false, "gpt-test", "", overrideRuntime); err == nil {
		t.Fatal("gpt 账户 reasoning_effort_override=ultra 必须被专属枚举拒绝")
	} else if !strings.Contains(err.Error(), "reasoning_effort_override 无效") {
		t.Fatalf("gpt 枚举错误文案 = %v", err)
	}

	// 存量臂：空 vendor 身份 → 覆盖整段惰性（不改写、不报错）。
	legacyBody := []byte(`{"model":"gpt-test","input":"hi"}`)
	legacyOut, err := gatewaydispatch.ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), legacyBody, nil, *legacySecret, "responses", false, "gpt-test", "", overrideRuntime)
	if err != nil {
		t.Fatalf("存量行覆盖必须惰性而非硬失败: %v", err)
	}
	if string(legacyOut) != string(legacyBody) {
		t.Fatalf("存量行 body 被改写: %s", legacyOut)
	}

	// vendor 身份进入能力解析的旁证：目录查询只收到 gpt/xai（存量行空 vendor
	// 不触发目录查询——能力解析在 providerCode 为空时短路）。
	for _, provider := range catalog.calls {
		if provider != "gpt" && provider != "xai" {
			t.Fatalf("目录端口收到意外 providerCode = %q（calls=%v）", provider, catalog.calls)
		}
	}
	if len(catalog.calls) < 2 {
		t.Fatalf("目录端口调用不足（gpt 与 xai 各至少一次）：calls=%v", catalog.calls)
	}
}
