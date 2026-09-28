package main

// 2026-09-28 静态能力兜底回归（真实数据链）：数据库内置目录行没有
// supported_tools / input_modalities / output_modalities 列（fixture DDL 与
// 生产/Node 同形），chat 面目录读取链必须在装载（缓存写入前）按静态定价快照
// 补齐。管理面对照物是 providers.ApplyBuiltInStaticDerivedFields（同一解析器
// ResolveBuiltInStaticDerivedCapabilities），本文件同时断言两面 parity。
//
// 2026-09-28 custom 目录行能力继承回归（BUG-0229）：custom（global/personal）
// 目录行按 scope 优先级整行替换内置行后，三个空能力键以被覆盖内置行（经静态
// 兜底后）的值填充（仅填空）；全新自定义模型（内置无对应行）保持空，不退回
// 静态定价表别名/前缀匹配。管理面（providers.inheritCustomCatalogCapabilities）
// 与 chat 面（chainInheritCustomCatalogCapabilities）共用
// providers.InheritBuiltinCatalogCapabilities，两面同值。
//
// 分层说明：effectiveTools 的传输组装（body["tools"] 注入）由 internal/chat
// w10d_websearch_test 的 mock 级用例覆盖（保留未动）；本文件断言它的真实数据
// 入参——目录行能力集合、chat DTO 投影、toolCapabilities 判定与协议偏好。

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
)

// seedBuiltinStaticDerivedRows 种入无工具列的内置目录行（gpt-5.6-terra /
// gpt-5.6-sol）、一条 custom 全局行（custom-plain-model，无内置对应行）、一条
// personal custom 行整行覆盖内置 gpt-5.6-sol（cpm_override）与一条 personal
// 全新自定义模型行（cpm_new_personal）。fixture 的 provider_model_catalog 与
// 生产一致：没有 supported_tools / input_modalities / output_modalities 列，
// 也不插入这三个键；custom_provider_models 同样无这三列。
func seedBuiltinStaticDerivedRows(t *testing.T, fixture *chainFixture) {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	builtinInsert := `INSERT INTO provider_model_catalog (
			id, status, provider_code, model, catalog_order, release_date,
			supported_api_protocols_json, source, catalog_visible,
			max_output_tokens, input_usd_per_1m, output_usd_per_1m,
			created_at, updated_at)
		VALUES (?, 'active', 'gpt', ?, 1, '2026-06-26',
			'["chat_completions","responses"]', 'builtin', 1,
			?, 2.0, 12.0, ?, ?)`
	seed(builtinInsert, "cat_terra", "gpt-5.6-terra", 128000, now, now)
	seed(builtinInsert, "cat_sol", "gpt-5.6-sol", 64000, now, now)
	// custom 行：global 行无内置对应行（全新自定义模型），不继承任何能力。
	seed(`INSERT INTO custom_provider_models (
			id, provider_code, model, scope, system_account_id, status, catalog_visible,
			supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			input_usd_per_1m, service_tier_prices_json, created_by, created_at, updated_at)
		VALUES ('cpm_plain', 'gpt', 'custom-plain-model', 'global', NULL, 'active', 1,
			'[]', '[]', '[]', 1.0, '{}', ?, ?, ?)`, now, now, now)
	// custom 行：personal 行与内置行同 (provider, model)——scope 优先级整行替换
	// 内置行后，能力三键按契约从被覆盖内置行继承（仅填空）。
	seed(`INSERT INTO custom_provider_models (
			id, provider_code, model, scope, system_account_id, status, catalog_visible,
			supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			input_usd_per_1m, service_tier_prices_json, created_by, created_at, updated_at)
		VALUES ('cpm_override', 'gpt', 'gpt-5.6-sol', 'personal', ?, 'active', 1,
			'["chat_completions","responses"]', '[]', '[]', 1.5, '{}', ?, ?, ?)`,
		fixture.systemAccount, now, now, now)
	// custom 行：personal 全新自定义模型（内置无对应行），保持空能力。
	seed(`INSERT INTO custom_provider_models (
			id, provider_code, model, scope, system_account_id, status, catalog_visible,
			supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			input_usd_per_1m, service_tier_prices_json, created_by, created_at, updated_at)
		VALUES ('cpm_new_personal', 'gpt', 'brand-new-personal-model', 'personal', ?, 'active', 1,
			'["chat_completions"]', '[]', '[]', 1.0, '{}', ?, ?, ?)`,
		fixture.systemAccount, now, now, now)
}

func findStaticDerivedItem(items []gatewayruntimecache.ProviderModelCatalogItem, model string) *gatewayruntimecache.ProviderModelCatalogItem {
	for index := range items {
		if items[index].Model == model {
			return &items[index]
		}
	}
	return nil
}

func staticDerivedContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func staticDerivedFloatPtrEqual(left, right *float64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func staticDerivedStringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// staticDerivedCapabilityEqual 比较能力键切片，nil 与空切片视为等价：chat 面
// custom 行缺键时解码为 nil，管理面 scanCustomCatalogItem 显式置空切片。
func staticDerivedCapabilityEqual(left, right []string) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	return reflect.DeepEqual(left, right)
}

// staticDerivedCapabilityMapEqual 比较二维「协议 × 工具」矩阵，nil 与空矩阵
// 视为等价（chat 面 custom 行缺键解码为 nil，管理面显式置空矩阵）。
func staticDerivedCapabilityMapEqual(left, right map[string][]string) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	for protocol, tools := range left {
		if !staticDerivedCapabilityEqual(right[protocol], tools) {
			return false
		}
	}
	return true
}

// TestChainCatalogBuiltinStaticDerivedCapabilitiesRealDataChain: 无工具列的
// builtin 行经真实 SQLite 读取链解析出 web_search / function_calling /
// image 输入模态；custom 全新自定义模型（无内置对应行）不继承任何能力
// （保持空）。
func TestChainCatalogBuiltinStaticDerivedCapabilitiesRealDataChain(t *testing.T) {
	fixture := newChainFixture(t)
	seedBuiltinStaticDerivedRows(t, fixture)
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	items, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt"})
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	terra := findStaticDerivedItem(items, "gpt-5.6-terra")
	if terra == nil {
		t.Fatalf("catalog missing gpt-5.6-terra: %#v", items)
	}
	if !staticDerivedContains(terra.SupportedToolsByProtocol["responses"], "web_search") {
		t.Fatalf("gpt-5.6-terra supportedTools missing web_search: %v", terra.SupportedToolsByProtocol["responses"])
	}
	if !staticDerivedContains(terra.SupportedToolsByProtocol["responses"], "function_calling") {
		t.Fatalf("gpt-5.6-terra supportedTools missing function_calling: %v", terra.SupportedToolsByProtocol["responses"])
	}
	// 二维矩阵：hosted 工具（web_search 等）只归 responses；chat_completions
	// 恒 [function_calling]；一维 supportedTools 为矩阵并集。
	if !staticDerivedContains(terra.SupportedToolsByProtocol["responses"], "web_search") {
		t.Fatalf("gpt-5.6-terra responses tools missing web_search: %v", terra.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(terra.SupportedToolsByProtocol["chat_completions"], []string{"function_calling"}) {
		t.Fatalf("gpt-5.6-terra chat_completions tools must be [function_calling]: %v", terra.SupportedToolsByProtocol)
	}
	if !staticDerivedContains(terra.InputModalities, "image") {
		t.Fatalf("gpt-5.6-terra inputModalities missing image: %v", terra.InputModalities)
	}
	if len(terra.OutputModalities) == 0 {
		t.Fatalf("gpt-5.6-terra outputModalities empty")
	}
	if len(terra.GenerationParameterCapabilities) == 0 {
		t.Fatalf("gpt-5.6-terra generationParameterCapabilities empty")
	}
	custom := findStaticDerivedItem(items, "custom-plain-model")
	if custom == nil {
		t.Fatalf("catalog missing custom-plain-model: %#v", items)
	}
	if len(custom.SupportedToolsByProtocol) != 0 || len(custom.InputModalities) != 0 || len(custom.OutputModalities) != 0 {
		t.Fatalf("brand-new custom model must not gain inherited capabilities: matrix=%v in=%v out=%v",
			custom.SupportedToolsByProtocol, custom.InputModalities, custom.OutputModalities)
	}
	if len(custom.GenerationParameterCapabilities) != 0 {
		t.Fatalf("custom row generationParameterCapabilities must stay absent: %s", custom.GenerationParameterCapabilities)
	}
}

// TestChainCatalogBuiltinStaticDerivedCapabilitiesSurviveCacheHit: 兜底发生在
// 缓存写入前，第二次读取（内存缓存命中路径，且此时已删除底层目录行以证明
// 未回源）仍携带同一能力集合。
func TestChainCatalogBuiltinStaticDerivedCapabilitiesSurviveCacheHit(t *testing.T) {
	fixture := newChainFixture(t)
	seedBuiltinStaticDerivedRows(t, fixture)
	options := gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt"}
	first, err := fixture.cache.ListCachedProviderModelCatalogAsync(context.Background(), options)
	if err != nil {
		t.Fatalf("first catalog read: %v", err)
	}
	terra := findStaticDerivedItem(first, "gpt-5.6-terra")
	if terra == nil || !staticDerivedContains(terra.SupportedToolsByProtocol["responses"], "web_search") {
		t.Fatalf("first read missing web_search: %#v", terra)
	}
	// 删除底层行：第二次读取若回源将拿不到该模型，能取到即为缓存命中。
	if _, err := fixture.db.Exec(`DELETE FROM provider_model_catalog`); err != nil {
		t.Fatalf("delete catalog rows: %v", err)
	}
	second, err := fixture.cache.ListCachedProviderModelCatalogAsync(context.Background(), options)
	if err != nil {
		t.Fatalf("second catalog read: %v", err)
	}
	cached := findStaticDerivedItem(second, "gpt-5.6-terra")
	if cached == nil {
		t.Fatalf("cache hit lost the builtin row entirely: %#v", second)
	}
	if !staticDerivedContains(cached.SupportedToolsByProtocol["responses"], "web_search") ||
		!staticDerivedContains(cached.SupportedToolsByProtocol["responses"], "function_calling") ||
		!staticDerivedContains(cached.InputModalities, "image") {
		t.Fatalf("cache hit lost static capabilities: tools=%v in=%v",
			cached.SupportedToolsByProtocol["responses"], cached.InputModalities)
	}
	if !staticDerivedContains(cached.SupportedToolsByProtocol["responses"], "web_search") ||
		!reflect.DeepEqual(cached.SupportedToolsByProtocol["chat_completions"], []string{"function_calling"}) {
		t.Fatalf("cache hit lost the protocol x tools matrix: %v", cached.SupportedToolsByProtocol)
	}
}

// TestChainCatalogBuiltinStaticDerivedParityWithAdminFace: 同一组样本模型
// （至少含 gpt-5.6-terra）在 chat 面（真实读取链）与管理面
// （providers.ApplyBuiltInStaticDerivedFields）解析出完全相同的能力集合。
func TestChainCatalogBuiltinStaticDerivedParityWithAdminFace(t *testing.T) {
	fixture := newChainFixture(t)
	seedBuiltinStaticDerivedRows(t, fixture)
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	items, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt"})
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	samples := []struct {
		model          string
		maxOutputToken int64
	}{
		{model: "gpt-5.6-terra", maxOutputToken: 128000},
		{model: "gpt-5.6-sol", maxOutputToken: 64000},
	}
	for _, sample := range samples {
		chatItem := findStaticDerivedItem(items, sample.model)
		if chatItem == nil {
			t.Fatalf("catalog missing %s: %#v", sample.model, items)
		}
		maxTokens := sample.maxOutputToken
		admin := providers.ModelCatalogItem{
			ProviderCode:    "gpt",
			Model:           sample.model,
			MaxOutputTokens: &maxTokens,
			Source:          "builtin",
		}
		providers.ApplyBuiltInStaticDerivedFields(&admin)
		if !reflect.DeepEqual(chatItem.InputModalities, admin.InputModalities) {
			t.Fatalf("%s inputModalities chat=%v admin=%v", sample.model, chatItem.InputModalities, admin.InputModalities)
		}
		if !reflect.DeepEqual(chatItem.OutputModalities, admin.OutputModalities) {
			t.Fatalf("%s outputModalities chat=%v admin=%v", sample.model, chatItem.OutputModalities, admin.OutputModalities)
		}
		if !staticDerivedCapabilityMapEqual(chatItem.SupportedToolsByProtocol, admin.SupportedToolsByProtocol) {
			t.Fatalf("%s supportedToolsByProtocol chat=%v admin=%v", sample.model, chatItem.SupportedToolsByProtocol, admin.SupportedToolsByProtocol)
		}
		if !staticDerivedFloatPtrEqual(chatItem.CachedImageInputUsdPer1M, admin.CachedImageInputUsdPer1M) {
			t.Fatalf("%s cachedImageInputUsdPer1M chat=%v admin=%v", sample.model, chatItem.CachedImageInputUsdPer1M, admin.CachedImageInputUsdPer1M)
		}
		if !staticDerivedFloatPtrEqual(chatItem.SourceExchangeRateToUsd, admin.SourceExchangeRateToUsd) {
			t.Fatalf("%s sourceExchangeRateToUsd chat=%v admin=%v", sample.model, chatItem.SourceExchangeRateToUsd, admin.SourceExchangeRateToUsd)
		}
		if staticDerivedStringPtrValue(chatItem.SourcePricingCurrency) != admin.SourcePricingCurrency ||
			staticDerivedStringPtrValue(chatItem.SourceExchangeRateDate) != admin.SourceExchangeRateDate ||
			staticDerivedStringPtrValue(chatItem.SourcePricingNote) != admin.SourcePricingNote {
			t.Fatalf("%s source pricing provenance chat=(%v,%v,%v) admin=(%s,%s,%s)",
				sample.model,
				chatItem.SourcePricingCurrency, chatItem.SourceExchangeRateDate, chatItem.SourcePricingNote,
				admin.SourcePricingCurrency, admin.SourceExchangeRateDate, admin.SourcePricingNote)
		}
		// 生成参数能力：管理面 map 与 chat 面 RawMessage 序列化为同一 JSON
		// （两侧同源解析器，map 键序由编码器归一）。
		adminRaw, err := json.Marshal(admin.GenerationParameterCapabilities)
		if err != nil {
			t.Fatalf("marshal admin capabilities: %v", err)
		}
		if string(chatItem.GenerationParameterCapabilities) != string(adminRaw) {
			t.Fatalf("%s generationParameterCapabilities chat=%s admin=%s",
				sample.model, chatItem.GenerationParameterCapabilities, adminRaw)
		}
	}
}

// TestChainCatalogCustomRowInheritsBuiltinCapabilities: personal custom 行
// 整行覆盖内置行（同 (provider, model)）后，合并胜出行是 custom 行本身
// （scope/source/id 证明），三个空能力键以被覆盖内置行经静态兜底后的值填充
// （与 providers.ApplyBuiltInStaticDerivedFields 同键样本完全同值，含
// web_search）；personal 全新自定义模型（内置无对应行）保持空。
func TestChainCatalogCustomRowInheritsBuiltinCapabilities(t *testing.T) {
	fixture := newChainFixture(t)
	seedBuiltinStaticDerivedRows(t, fixture)
	source, err := newChainCatalogSource(fixture.db, false)
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
	overridden := findStaticDerivedItem(items, "gpt-5.6-sol")
	if overridden == nil {
		t.Fatalf("catalog missing gpt-5.6-sol: %#v", items)
	}
	// 胜出行是 custom personal 行（BUG 场景还原：scope 优先级整行替换）。
	if overridden.Scope != "personal" || overridden.Source != "custom-personal" ||
		staticDerivedStringPtrValue(overridden.ID) != "cpm_override" {
		t.Fatalf("gpt-5.6-sol must be won by the personal custom row: scope=%s source=%s id=%s",
			overridden.Scope, overridden.Source, staticDerivedStringPtrValue(overridden.ID))
	}
	if !staticDerivedContains(overridden.SupportedToolsByProtocol["responses"], "web_search") {
		t.Fatalf("overridden custom row supportedTools missing web_search: %v", overridden.SupportedToolsByProtocol)
	}
	if !staticDerivedContains(overridden.SupportedToolsByProtocol["responses"], "function_calling") {
		t.Fatalf("overridden custom row supportedTools missing function_calling: %v", overridden.SupportedToolsByProtocol)
	}
	if !staticDerivedContains(overridden.SupportedToolsByProtocol["responses"], "web_search") ||
		!reflect.DeepEqual(overridden.SupportedToolsByProtocol["chat_completions"], []string{"function_calling"}) {
		t.Fatalf("overridden custom row matrix missing web_search under responses: %v", overridden.SupportedToolsByProtocol)
	}
	if !staticDerivedContains(overridden.InputModalities, "image") {
		t.Fatalf("overridden custom row inputModalities missing image: %v", overridden.InputModalities)
	}
	if len(overridden.OutputModalities) == 0 {
		t.Fatalf("overridden custom row outputModalities empty")
	}
	// 继承值与被覆盖内置行（经静态兜底）完全同值：同键样本直接对管理面解析器。
	maxTokens := int64(64000)
	builtinPeer := providers.ModelCatalogItem{
		ProviderCode:    "gpt",
		Model:           "gpt-5.6-sol",
		MaxOutputTokens: &maxTokens,
		Source:          "builtin",
	}
	providers.ApplyBuiltInStaticDerivedFields(&builtinPeer)
	if !staticDerivedCapabilityMapEqual(overridden.SupportedToolsByProtocol, builtinPeer.SupportedToolsByProtocol) ||
		!reflect.DeepEqual(overridden.InputModalities, builtinPeer.InputModalities) ||
		!reflect.DeepEqual(overridden.OutputModalities, builtinPeer.OutputModalities) {
		t.Fatalf("inherited capabilities must equal the static-derived builtin row: matrix=%v in=%v out=%v",
			overridden.SupportedToolsByProtocol, overridden.InputModalities, overridden.OutputModalities)
	}
	// personal 全新自定义模型（内置无对应行）不继承。
	brandNew := findStaticDerivedItem(items, "brand-new-personal-model")
	if brandNew == nil {
		t.Fatalf("catalog missing brand-new-personal-model: %#v", items)
	}
	if len(brandNew.SupportedToolsByProtocol) != 0 ||
		len(brandNew.InputModalities) != 0 || len(brandNew.OutputModalities) != 0 {
		t.Fatalf("brand-new personal custom model must stay empty: matrix=%v in=%v out=%v",
			brandNew.SupportedToolsByProtocol, brandNew.InputModalities, brandNew.OutputModalities)
	}
}

// TestChainCatalogCustomCapabilityInheritAdminFaceParity: custom 覆盖内置行
// 场景，chat 面真实读取链与管理面真实读取链（providers.Store
// ListProviderModelsForRequest，同一 fixture 库）解析出完全相同的能力三键，
// 全新自定义模型两侧同样保持空——两面同源不漂移（BUG-0210 parity 原则）。
func TestChainCatalogCustomCapabilityInheritAdminFaceParity(t *testing.T) {
	fixture := newChainFixture(t)
	seedBuiltinStaticDerivedRows(t, fixture)
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	chatItems, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    "gpt",
		SystemAccountID: fixture.systemAccount,
	})
	if err != nil {
		t.Fatalf("list chat catalog: %v", err)
	}
	adminStore, err := providers.NewStore(fixture.db, false, time.Now)
	if err != nil {
		t.Fatalf("create admin store: %v", err)
	}
	adminItems, err := adminStore.ListProviderModelsForRequest(context.Background(), "gpt", fixture.systemAccount, false, false)
	if err != nil {
		t.Fatalf("list admin catalog: %v", err)
	}
	findAdminItem := func(model string) *providers.ModelCatalogItem {
		for index := range adminItems {
			if adminItems[index].Model == model {
				return &adminItems[index]
			}
		}
		return nil
	}
	for _, model := range []string{"gpt-5.6-sol", "brand-new-personal-model", "custom-plain-model"} {
		chatItem := findStaticDerivedItem(chatItems, model)
		if chatItem == nil {
			t.Fatalf("chat catalog missing %s: %#v", model, chatItems)
		}
		adminItem := findAdminItem(model)
		if adminItem == nil {
			t.Fatalf("admin catalog missing %s: %#v", model, adminItems)
		}
		if !staticDerivedCapabilityMapEqual(chatItem.SupportedToolsByProtocol, adminItem.SupportedToolsByProtocol) {
			t.Fatalf("%s supportedToolsByProtocol chat=%v admin=%v", model, chatItem.SupportedToolsByProtocol, adminItem.SupportedToolsByProtocol)
		}
		if !staticDerivedCapabilityEqual(chatItem.InputModalities, adminItem.InputModalities) {
			t.Fatalf("%s inputModalities chat=%v admin=%v", model, chatItem.InputModalities, adminItem.InputModalities)
		}
		if !staticDerivedCapabilityEqual(chatItem.OutputModalities, adminItem.OutputModalities) {
			t.Fatalf("%s outputModalities chat=%v admin=%v", model, chatItem.OutputModalities, adminItem.OutputModalities)
		}
	}
	overridden := findAdminItem("gpt-5.6-sol")
	if overridden.Scope != "personal" || overridden.Source != "custom-personal" {
		t.Fatalf("admin gpt-5.6-sol must be won by the personal custom row: scope=%s source=%s",
			overridden.Scope, overridden.Source)
	}
}

// TestChainInheritCustomCatalogCapabilitiesMergeKeys: 回填 map 键必须与
// chainMergeCatalogItems 的合并键一致——非 hybrid 用裸 model（跨供应商覆盖也
// 继承），hybrid（preserveProviderIdentity）用 (provider, model)（不同供应商
// 同名模型不串能力）；仅填空不覆盖非空值（二维矩阵与一维过渡投影同规则）；
// built_in 行不受影响。
func TestChainInheritCustomCatalogCapabilitiesMergeKeys(t *testing.T) {
	builtinRow := gatewayruntimecache.ProviderModelCatalogItem{
		Scope:        "built_in",
		ProviderCode: "openai",
		Model:        "gpt-6-sol",
		SupportedToolsByProtocol: map[string][]string{
			"responses":        {"web_search", "function_calling"},
			"chat_completions": {"function_calling"},
		},
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}
	customRow := func(provider string) gatewayruntimecache.ProviderModelCatalogItem {
		return gatewayruntimecache.ProviderModelCatalogItem{
			Scope:        "personal",
			ProviderCode: provider,
			Model:        "gpt-6-sol",
		}
	}
	// 非 hybrid：custom 行在另一供应商上，仍按裸 model 键继承被覆盖内置行。
	merged := []gatewayruntimecache.ProviderModelCatalogItem{customRow("my-chat")}
	chainInheritCustomCatalogCapabilities(merged, []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}, false)
	if !staticDerivedContains(merged[0].SupportedToolsByProtocol["responses"], "web_search") ||
		!staticDerivedContains(merged[0].InputModalities, "image") ||
		len(merged[0].OutputModalities) == 0 {
		t.Fatalf("bare-model key must inherit across providers: matrix=%v in=%v out=%v",
			merged[0].SupportedToolsByProtocol, merged[0].InputModalities, merged[0].OutputModalities)
	}
	// hybrid：不同供应商同名模型不继承。
	merged = []gatewayruntimecache.ProviderModelCatalogItem{customRow("my-chat")}
	chainInheritCustomCatalogCapabilities(merged, []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}, true)
	if len(merged[0].SupportedToolsByProtocol) != 0 ||
		len(merged[0].InputModalities) != 0 || len(merged[0].OutputModalities) != 0 {
		t.Fatalf("hybrid identity must not inherit across providers: matrix=%v in=%v out=%v",
			merged[0].SupportedToolsByProtocol, merged[0].InputModalities, merged[0].OutputModalities)
	}
	// hybrid：同供应商 (provider, model) 同键才继承；非空键不被覆盖。
	sameProvider := customRow("openai")
	sameProvider.SupportedToolsByProtocol = map[string][]string{"responses": {"custom_tool"}}
	merged = []gatewayruntimecache.ProviderModelCatalogItem{sameProvider}
	chainInheritCustomCatalogCapabilities(merged, []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}, true)
	if !reflect.DeepEqual(merged[0].SupportedToolsByProtocol["responses"], []string{"custom_tool"}) {
		t.Fatalf("non-empty matrix must stay: %v", merged[0].SupportedToolsByProtocol)
	}
	// 空**矩阵**在同 (provider, model) 键下整体继承。
	emptyMatrix := customRow("openai")
	mergedEmpty := []gatewayruntimecache.ProviderModelCatalogItem{emptyMatrix}
	chainInheritCustomCatalogCapabilities(mergedEmpty, []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}, true)
	if !staticDerivedContains(mergedEmpty[0].SupportedToolsByProtocol["responses"], "web_search") {
		t.Fatalf("empty matrix must inherit under (provider, model): %v", mergedEmpty[0].SupportedToolsByProtocol)
	}
	if !staticDerivedContains(merged[0].InputModalities, "image") || len(merged[0].OutputModalities) == 0 {
		t.Fatalf("empty keys must inherit under (provider, model): in=%v out=%v",
			merged[0].InputModalities, merged[0].OutputModalities)
	}
	// built_in 行不参与回填（无内置扫描行时保持原状）。
	builtinOnly := []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}
	chainInheritCustomCatalogCapabilities(builtinOnly, nil, false)
	if !staticDerivedContains(builtinOnly[0].SupportedToolsByProtocol["responses"], "web_search") {
		t.Fatalf("builtin row must stay untouched: %v", builtinOnly[0].SupportedToolsByProtocol)
	}
}

// toolCapsRealCatalogChain 是混合目录桩：ListProviderCatalog 走真实
// runtime cache（SQLite 装载 + 缓存命中），ListAccountsForGroup 用桩账户
// （被测对象是目录链，不是账户选择链）。
// TestChatToolCapabilitiesWebSearchAvailableFromRealCatalogChain 与 toolCapsRealCatalogChain
// 已随 cmd 侧 toolCapabilities resolver 退场删除（绑定状态聚合移入 internal/chat，
// 契约 AI问答工具体系设计 §8；能力链断言由本文件 RealDataChain/SurviveCacheHit/parity 覆盖）。
