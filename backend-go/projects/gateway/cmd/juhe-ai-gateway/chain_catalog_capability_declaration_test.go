package main

// 2026-10-30 T82 models 能力声明三列列投影回归（真实数据链）：custom
// （global/personal）目录行写入显式 supported_tools_by_protocol_json /
// input_modalities_json / output_modalities_json 后，chat 面目录读取链
// （ListProviderModelCatalog / ListAccountSupportedModelCatalog）必须原样读回
// 非空矩阵与模态（列投影直接带出，buildChatModelOptions 的
// supportsTool("function_calling") 与搜索候选判定由此获得真实入参）。
// 同时锁住两条不变量：
//   - builtin 行三列取默认空值，静态兜底（decorateBuiltinStaticDerivedCapabilities）
//     无条件覆盖，矩阵仍来自静态快照；
//   - custom 行显式声明按 BUG-0229「仅填空」契约不被内置行（静态兜底值）覆盖，
//     未声明（默认空值）行为不变（仅填空继承）。
//
// 授权门：经 newChainFixture 走 pgtestkit 隔离 PG，未设 JUHE_AI_TEST_EXTERNAL=1
// 时自动 skip（真实 PG 用例由授权运行串行执行）。

import (
	"context"
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// seedCapabilityDeclarationRows 在 seedBuiltinStaticDerivedRows（builtin 静态
// 兜底基线 + 无声明 custom 行）之上追加两条带显式能力声明的 models 行：
//   - mdl_decl_global：global 行，显式矩阵
//     {"chat_completions":["function_calling"]} 与模态 ["text","image"] / ["text"]；
//   - mdl_decl_override：personal 行整行覆盖内置 gpt-5.6-sol，显式矩阵
//     {"chat_completions":["custom_tool_explicit"]} 与模态 ["text"] / ["text"]。
func seedCapabilityDeclarationRows(t *testing.T, fixture *chainFixture) {
	t.Helper()
	seedBuiltinStaticDerivedRows(t, fixture)
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, status, catalog_visible,
			supported_endpoint_modes_json, supported_tools_by_protocol_json,
			input_modalities_json, output_modalities_json,
			input_usd_per_1m, created_at, updated_at)
		VALUES ('mdl_decl_global', 'gpt', 'decl-global-model', 'decl-global-model', 'global', 'active', TRUE,
			'["chat_json","chat_sse"]', '{"chat_completions":["function_calling"]}',
			'["text","image"]', '["text"]', 1.0, ?, ?)`, now, now)
	seed(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, system_account_id, status, catalog_visible,
			supported_endpoint_modes_json, supported_tools_by_protocol_json,
			input_modalities_json, output_modalities_json,
			input_usd_per_1m, created_at, updated_at)
		VALUES ('mdl_decl_override', 'gpt', 'gpt-5.6-sol', 'gpt-5.6-sol', 'personal', ?, 'active', TRUE,
			'["chat_json","chat_sse","responses_json","responses_sse"]', '{"chat_completions":["custom_tool_explicit"]}',
			'["text"]', '["text"]', 1.5, ?, ?)`,
		fixture.systemAccount, now, now)
}

// TestChainCatalogCapabilityDeclarationProjectionRealDataChain: custom 行显式
// 矩阵/模态经真实读取链原样读回；显式声明的 personal 覆盖行不被内置行
// （静态兜底值）替换；builtin 行矩阵仍来自静态兜底；未声明的全新 custom 行
// 保持空（不退静态表）。
func TestChainCatalogCapabilityDeclarationProjectionRealDataChain(t *testing.T) {
	fixture := newChainFixture(t)
	seedCapabilityDeclarationRows(t, fixture)
	source, err := newChainCatalogSource(fixture.db)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	items, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    "gpt",
		SystemAccountID: fixture.systemAccount,
	})
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	// builtin 行：新列为默认空值，静态兜底照常覆盖（行为与列投影加入前一致）。
	terra := findStaticDerivedItem(items, "gpt-5.6-terra")
	if terra == nil {
		t.Fatalf("catalog missing gpt-5.6-terra: %#v", items)
	}
	if !staticDerivedContains(terra.SupportedToolsByProtocol["responses"], "web_search") ||
		!reflect.DeepEqual(terra.SupportedToolsByProtocol["chat_completions"], []string{"function_calling"}) {
		t.Fatalf("builtin static fallback must stay: %v", terra.SupportedToolsByProtocol)
	}
	// custom global 行：显式声明经列投影原样读回（非空矩阵与模态）。
	declared := findStaticDerivedItem(items, "decl-global-model")
	if declared == nil {
		t.Fatalf("catalog missing decl-global-model: %#v", items)
	}
	if !reflect.DeepEqual(declared.SupportedToolsByProtocol, map[string][]string{"chat_completions": {"function_calling"}}) {
		t.Fatalf("declared matrix must round-trip: %v", declared.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(declared.InputModalities, []string{"text", "image"}) ||
		!reflect.DeepEqual(declared.OutputModalities, []string{"text"}) {
		t.Fatalf("declared modalities must round-trip: in=%v out=%v", declared.InputModalities, declared.OutputModalities)
	}
	// custom personal 覆盖行：显式矩阵/模态不被内置行（静态兜底值）覆盖。
	override := findStaticDerivedItem(items, "gpt-5.6-sol")
	if override == nil {
		t.Fatalf("catalog missing gpt-5.6-sol: %#v", items)
	}
	if override.Scope != "personal" || staticDerivedStringPtrValue(override.ID) != "mdl_decl_override" {
		t.Fatalf("gpt-5.6-sol must be won by the declared personal row: scope=%s id=%s",
			override.Scope, staticDerivedStringPtrValue(override.ID))
	}
	if !reflect.DeepEqual(override.SupportedToolsByProtocol, map[string][]string{"chat_completions": {"custom_tool_explicit"}}) {
		t.Fatalf("explicit matrix must not be overridden by the builtin row: %v", override.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(override.InputModalities, []string{"text"}) ||
		!reflect.DeepEqual(override.OutputModalities, []string{"text"}) {
		t.Fatalf("explicit modalities must not be overridden: in=%v out=%v", override.InputModalities, override.OutputModalities)
	}
	// 未声明的全新 custom 行（无内置对应行）保持空：空值语义=未声明，
	// 不退静态定价表兜底。
	plain := findStaticDerivedItem(items, "custom-plain-model")
	if plain == nil {
		t.Fatalf("catalog missing custom-plain-model: %#v", items)
	}
	if len(plain.SupportedToolsByProtocol) != 0 || len(plain.InputModalities) != 0 || len(plain.OutputModalities) != 0 {
		t.Fatalf("undeclared custom row must stay empty: matrix=%v in=%v out=%v",
			plain.SupportedToolsByProtocol, plain.InputModalities, plain.OutputModalities)
	}
}

// TestChainCatalogCapabilityDeclarationAccountCatalog: 账户作用域目录
// （ListAccountSupportedModelCatalog，account_supported_models 显式绑定）同样
// 读回 custom 行显式矩阵与模态（accountModelCatalogColumns 由
// chainCatalogColumns append 派生，自动携带三列）。
func TestChainCatalogCapabilityDeclarationAccountCatalog(t *testing.T) {
	fixture := newChainFixture(t)
	seedCapabilityDeclarationRows(t, fixture)
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'decl-global-model', 'mdl_decl_global', '2026-09-04T00:00:00.000Z')`), fixture.accountID); err != nil {
		t.Fatalf("bind account supported model: %v", err)
	}
	source, err := newChainCatalogSource(fixture.db)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	items, err := source.ListAccountSupportedModelCatalog(context.Background(), fixture.accountID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("list account catalog: %v", err)
	}
	declared := findStaticDerivedItem(items, "decl-global-model")
	if declared == nil {
		t.Fatalf("account catalog missing decl-global-model: %#v", items)
	}
	if !reflect.DeepEqual(declared.SupportedToolsByProtocol, map[string][]string{"chat_completions": {"function_calling"}}) {
		t.Fatalf("account catalog declared matrix must round-trip: %v", declared.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(declared.InputModalities, []string{"text", "image"}) ||
		!reflect.DeepEqual(declared.OutputModalities, []string{"text"}) {
		t.Fatalf("account catalog declared modalities must round-trip: in=%v out=%v",
			declared.InputModalities, declared.OutputModalities)
	}
}
