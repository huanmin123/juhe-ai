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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
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
	if !staticDerivedContains(terra.SupportedTools, "web_search") {
		t.Fatalf("gpt-5.6-terra supportedTools missing web_search: %v", terra.SupportedTools)
	}
	if !staticDerivedContains(terra.SupportedTools, "function_calling") {
		t.Fatalf("gpt-5.6-terra supportedTools missing function_calling: %v", terra.SupportedTools)
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
	if len(custom.SupportedTools) != 0 || len(custom.InputModalities) != 0 || len(custom.OutputModalities) != 0 {
		t.Fatalf("brand-new custom model must not gain inherited capabilities: tools=%v in=%v out=%v",
			custom.SupportedTools, custom.InputModalities, custom.OutputModalities)
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
	if terra == nil || !staticDerivedContains(terra.SupportedTools, "web_search") {
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
	if !staticDerivedContains(cached.SupportedTools, "web_search") ||
		!staticDerivedContains(cached.SupportedTools, "function_calling") ||
		!staticDerivedContains(cached.InputModalities, "image") {
		t.Fatalf("cache hit lost static capabilities: tools=%v in=%v",
			cached.SupportedTools, cached.InputModalities)
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
		if !reflect.DeepEqual(chatItem.SupportedTools, admin.SupportedTools) {
			t.Fatalf("%s supportedTools chat=%v admin=%v", sample.model, chatItem.SupportedTools, admin.SupportedTools)
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
	if !staticDerivedContains(overridden.SupportedTools, "web_search") {
		t.Fatalf("overridden custom row supportedTools missing web_search: %v", overridden.SupportedTools)
	}
	if !staticDerivedContains(overridden.SupportedTools, "function_calling") {
		t.Fatalf("overridden custom row supportedTools missing function_calling: %v", overridden.SupportedTools)
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
	if !reflect.DeepEqual(overridden.SupportedTools, builtinPeer.SupportedTools) ||
		!reflect.DeepEqual(overridden.InputModalities, builtinPeer.InputModalities) ||
		!reflect.DeepEqual(overridden.OutputModalities, builtinPeer.OutputModalities) {
		t.Fatalf("inherited capabilities must equal the static-derived builtin row: tools=%v in=%v out=%v",
			overridden.SupportedTools, overridden.InputModalities, overridden.OutputModalities)
	}
	// personal 全新自定义模型（内置无对应行）不继承。
	brandNew := findStaticDerivedItem(items, "brand-new-personal-model")
	if brandNew == nil {
		t.Fatalf("catalog missing brand-new-personal-model: %#v", items)
	}
	if len(brandNew.SupportedTools) != 0 || len(brandNew.InputModalities) != 0 || len(brandNew.OutputModalities) != 0 {
		t.Fatalf("brand-new personal custom model must stay empty: tools=%v in=%v out=%v",
			brandNew.SupportedTools, brandNew.InputModalities, brandNew.OutputModalities)
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
		if !staticDerivedCapabilityEqual(chatItem.SupportedTools, adminItem.SupportedTools) {
			t.Fatalf("%s supportedTools chat=%v admin=%v", model, chatItem.SupportedTools, adminItem.SupportedTools)
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
// 同名模型不串能力）；仅填空不覆盖非空值；built_in 行不受影响。
func TestChainInheritCustomCatalogCapabilitiesMergeKeys(t *testing.T) {
	builtinRow := gatewayruntimecache.ProviderModelCatalogItem{
		Scope:            "built_in",
		ProviderCode:     "openai",
		Model:            "gpt-6-sol",
		SupportedTools:   []string{"web_search", "function_calling"},
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
	if !staticDerivedContains(merged[0].SupportedTools, "web_search") ||
		!staticDerivedContains(merged[0].InputModalities, "image") ||
		len(merged[0].OutputModalities) == 0 {
		t.Fatalf("bare-model key must inherit across providers: tools=%v in=%v out=%v",
			merged[0].SupportedTools, merged[0].InputModalities, merged[0].OutputModalities)
	}
	// hybrid：不同供应商同名模型不继承。
	merged = []gatewayruntimecache.ProviderModelCatalogItem{customRow("my-chat")}
	chainInheritCustomCatalogCapabilities(merged, []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}, true)
	if len(merged[0].SupportedTools) != 0 || len(merged[0].InputModalities) != 0 || len(merged[0].OutputModalities) != 0 {
		t.Fatalf("hybrid identity must not inherit across providers: tools=%v in=%v out=%v",
			merged[0].SupportedTools, merged[0].InputModalities, merged[0].OutputModalities)
	}
	// hybrid：同供应商 (provider, model) 同键才继承；非空键不被覆盖。
	sameProvider := customRow("openai")
	sameProvider.SupportedTools = []string{"custom_tool"}
	merged = []gatewayruntimecache.ProviderModelCatalogItem{sameProvider}
	chainInheritCustomCatalogCapabilities(merged, []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}, true)
	if len(merged[0].SupportedTools) != 1 || merged[0].SupportedTools[0] != "custom_tool" {
		t.Fatalf("non-empty supportedTools must stay: %v", merged[0].SupportedTools)
	}
	if !staticDerivedContains(merged[0].InputModalities, "image") || len(merged[0].OutputModalities) == 0 {
		t.Fatalf("empty keys must inherit under (provider, model): in=%v out=%v",
			merged[0].InputModalities, merged[0].OutputModalities)
	}
	// built_in 行不参与回填（无内置扫描行时保持原状）。
	builtinOnly := []gatewayruntimecache.ProviderModelCatalogItem{builtinRow}
	chainInheritCustomCatalogCapabilities(builtinOnly, nil, false)
	if !staticDerivedContains(builtinOnly[0].SupportedTools, "web_search") {
		t.Fatalf("builtin row must stay untouched: %v", builtinOnly[0].SupportedTools)
	}
}

// toolCapsRealCatalogChain 是混合目录桩：ListProviderCatalog 走真实
// runtime cache（SQLite 装载 + 缓存命中），ListAccountsForGroup 用桩账户
// （被测对象是目录链，不是账户选择链）。
type toolCapsRealCatalogChain struct {
	chatModelCatalog
	accounts map[string][]chat.ChatTransportAccount
}

func (c toolCapsRealCatalogChain) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []chat.ChatTransportAccount {
	return c.accounts[groupID+"|"+requestedModel+"|"+endpointFamily]
}

// TestChatToolCapabilitiesWebSearchAvailableFromRealCatalogChain: 真实目录链
// 之上，Responses 协议账户下 toolCapabilities 判定 web_search 可用，协议
// 偏好（preferResponses）选中 responses；chat DTO 投影（模型选项的组装入参）
// 保留能力集合。
func TestChatToolCapabilitiesWebSearchAvailableFromRealCatalogChain(t *testing.T) {
	fixture := newChainFixture(t)
	seedBuiltinStaticDerivedRows(t, fixture)
	if _, err := fixture.cache.ListCachedProviderModelCatalogAsync(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt"}); err != nil {
		t.Fatalf("warm catalog cache: %v", err)
	}
	responseAccount := chat.ChatTransportAccount{
		ID:                     "acct-responses",
		Type:                   "oauth",
		ProviderCode:           "gpt",
		SupportedModels:        []string{"gpt-5.6-terra"},
		SupportedEndpointModes: []string{"responses_sse"},
	}
	catalog := toolCapsRealCatalogChain{
		chatModelCatalog: chatModelCatalog{cache: fixture.cache},
		accounts: map[string][]chat.ChatTransportAccount{
			fixture.groupID + "|gpt-5.6-terra|":          {responseAccount},
			fixture.groupID + "|gpt-5.6-terra|responses": {responseAccount},
		},
	}
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: &chat.GatewayKeyView{
			GroupBindings: []chat.GatewayGroupBinding{{GroupID: fixture.groupID, Status: "active", GroupEnabled: true}},
		}},
		catalog)
	payload := resolveChatToolCapabilities(deps, toolCapsConversation("gpt-5.6-terra"), "owner-1")
	expectTool(t, payload, "web_search", true, "")

	// 协议偏好：真实链解析出的协议集合含 responses，preferResponses 生效。
	protocols := chatToolSupportedProtocols(deps, []string{fixture.groupID}, "", "gpt-5.6-terra")
	hasResponses := false
	for _, protocol := range protocols {
		if protocol == chat.ProtocolResponses {
			hasResponses = true
		}
	}
	if !hasResponses {
		t.Fatalf("supported protocols missing responses: %v", protocols)
	}
	if selected := chatToolSelectTransport(protocols, true); selected != chat.ProtocolResponses {
		t.Fatalf("preferResponses selected %q, want responses", selected)
	}

	// chat DTO 投影（buildChatModelOptions 的直接入参）：JSON 往返不丢能力。
	projected := catalog.ListProviderCatalog("gpt", "")
	var projectedTerra *chat.ProviderModelCatalogItem
	for index := range projected {
		if projected[index].Model == "gpt-5.6-terra" {
			projectedTerra = &projected[index]
			break
		}
	}
	if projectedTerra == nil {
		t.Fatalf("projected catalog missing gpt-5.6-terra: %#v", projected)
	}
	if !staticDerivedContains(projectedTerra.SupportedTools, "web_search") ||
		!staticDerivedContains(projectedTerra.SupportedTools, "function_calling") {
		t.Fatalf("projected supportedTools lost capabilities: %v", projectedTerra.SupportedTools)
	}
	if !staticDerivedContains(projectedTerra.InputModalities, "image") {
		t.Fatalf("projected inputModalities lost image: %v", projectedTerra.InputModalities)
	}
	if len(projectedTerra.GenerationParameterCapabilities) == 0 {
		t.Fatalf("projected generationParameterCapabilities empty")
	}
}
