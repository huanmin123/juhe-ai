package main

// w40 覆盖率补口（chain_catalog.go 纯函数簇 + 目录源扩展/过滤臂）：
// 2026-09 目录合并候选 / 供应商代码扩展语义（模型检测零配置自动认领 family）
// 新增的纯函数与 SQL 臂此前未覆盖。纯函数直接表驱动；扩展与过滤臂复用
// newChainFixture 既有 schema seed（providers / provider_protocol_profiles /
// provider_model_catalog / custom_provider_models）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
)

func TestChainCatalogScopePriority(t *testing.T) {
	if got := chainCatalogScopePriority("personal"); got != 3 {
		t.Fatalf("personal = %d", got)
	}
	if got := chainCatalogScopePriority("global"); got != 2 {
		t.Fatalf("global = %d", got)
	}
	if got := chainCatalogScopePriority("built_in"); got != 1 {
		t.Fatalf("built_in = %d", got)
	}
}

func TestChainCatalogTokenBoundary(t *testing.T) {
	if !chainCatalogTokenBoundary("-audio", 1) || !chainCatalogTokenBoundary("_audio", 1) || !chainCatalogTokenBoundary(".audio", 1) {
		t.Fatal("分隔符前驱必须视为边界")
	}
	if !chainCatalogTokenBoundary("audio", 0) {
		t.Fatal("起点必须视为边界")
	}
	if chainCatalogTokenBoundary("xaudio", 1) {
		t.Fatal("普通字符前驱不是边界")
	}
}

func TestChainCatalogTokenBoundaryEnd(t *testing.T) {
	if !chainCatalogTokenBoundaryEnd("audio", 5) {
		t.Fatal("串尾必须视为边界")
	}
	if !chainCatalogTokenBoundaryEnd("audio-x", 5) || !chainCatalogTokenBoundaryEnd("audio_x", 5) || !chainCatalogTokenBoundaryEnd("audio.x", 5) {
		t.Fatal("分隔符后继必须视为边界")
	}
	if chainCatalogTokenBoundaryEnd("audiox", 5) {
		t.Fatal("普通字符后继不是边界")
	}
}

func TestChainCatalogModelMatchesToken(t *testing.T) {
	cases := []struct {
		model, token string
		want         bool
	}{
		{"gpt-4.1", "audio", false},        // 无 token
		{"xaudiolab", "audio", false},      // 起点不齐（457 臂）
		{"audiox", "audio", false},         // 终点不齐（461 臂）
		{"audio", "audio", true},           // 整串
		{"whisper-turbo", "whisper", true}, // 起点边界
		{"gpt.realtime.v2", "realtime", true},
		{"chat_tts_small", "tts", true},
	}
	for _, tc := range cases {
		if got := chainCatalogModelMatchesToken(tc.model, tc.token); got != tc.want {
			t.Fatalf("matches(%q,%q) = %v, want %v", tc.model, tc.token, got, tc.want)
		}
	}
}

func TestChainCatalogTierPriceCount(t *testing.T) {
	if got := chainCatalogTierPriceCount(json.RawMessage(" ")); got != 0 {
		t.Fatalf("空白 = %d", got)
	}
	if got := chainCatalogTierPriceCount(json.RawMessage("null")); got != 0 {
		t.Fatalf("null = %d", got)
	}
	if got := chainCatalogTierPriceCount(json.RawMessage("[1,2]")); got != 0 {
		t.Fatalf("非对象 JSON = %d", got)
	}
	if got := chainCatalogTierPriceCount(json.RawMessage(`{"flex":{},"priority":{}}`)); got != 2 {
		t.Fatalf("两档 = %d", got)
	}
}

func TestChainHasDirectCatalogPriceTierFallback(t *testing.T) {
	item := gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ServiceTierPrices: json.RawMessage("null")}
	if chainHasDirectCatalogPrice(item) {
		t.Fatal("空定价与空档位必须判无价")
	}
	item.ServiceTierPrices = json.RawMessage(`{"flex":{}}`)
	if !chainHasDirectCatalogPrice(item) {
		t.Fatal("档位价存在必须判有价")
	}
}

func TestChainCatalogPlaceholders(t *testing.T) {
	if got := chainCatalogPlaceholders(0); got != "" {
		t.Fatalf("0 占位 = %q", got)
	}
	if got := chainCatalogPlaceholders(-1); got != "" {
		t.Fatalf("负数占位 = %q", got)
	}
	if got := chainCatalogPlaceholders(3); got != "?,?,?" {
		t.Fatalf("3 占位 = %q", got)
	}
}

func TestChainCatalogItemID(t *testing.T) {
	if got := chainCatalogItemID(gatewayruntimecache.ProviderModelCatalogItem{}); got != "" {
		t.Fatalf("nil id = %q", got)
	}
	id := "cat_1"
	if got := chainCatalogItemID(gatewayruntimecache.ProviderModelCatalogItem{ID: &id}); got != "cat_1" {
		t.Fatalf("id = %q", got)
	}
}

func TestChainIsSupportedCatalogModel(t *testing.T) {
	audio := "audio"
	mode := func(m string) *string { return &m }
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Mode: &audio}) {
		t.Fatal("audio 模式必须判不支持")
	}
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Mode: mode("audio_speech")}) {
		t.Fatal("audio_speech 模式必须判不支持")
	}
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{SupportedAPIProtocols: []string{"realtime"}}) {
		t.Fatal("realtime 协议必须判不支持")
	}
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{SupportedAPIProtocols: []string{"audio"}}) {
		t.Fatal("纯 audio 协议必须判不支持")
	}
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Model: "whisper-1"}) {
		t.Fatal("whisper 命名必须判不支持")
	}
	if !chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Model: "gpt-5.6"}) {
		t.Fatal("普通模型必须判支持")
	}
}

func TestChainCompareCatalogModelsCaseTiebreak(t *testing.T) {
	if got := chainCompareCatalogModels("ABC", "abc"); got == 0 {
		t.Fatal("小写同序时必须回退原串比较")
	}
	if got := chainCompareCatalogModels("m1", "m1"); got != 0 {
		t.Fatalf("相同模型 = %d", got)
	}
}

func TestChainCompareCatalogItemsArms(t *testing.T) {
	newer, older := "2026-09-20", "2026-09-01"
	order := func(v int) *int { return &v }
	// 缺 release 一侧排后（527 臂）。
	if got := chainCompareCatalogItems(
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m"},
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ReleaseDate: &older},
	); got != 1 {
		t.Fatalf("缺 release 一侧 = %d, want 1", got)
	}
	// catalogOrder 左大右小 → 1（534 臂）。
	if got := chainCompareCatalogItems(
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", CatalogOrder: order(2)},
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", CatalogOrder: order(1)},
	); got != 1 {
		t.Fatalf("order 左大 = %d, want 1", got)
	}
	// catalogOrder 左小 → -1。
	if got := chainCompareCatalogItems(
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", CatalogOrder: order(1)},
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", CatalogOrder: order(2)},
	); got != -1 {
		t.Fatalf("order 左小 = %d, want -1", got)
	}
	// 同模型同序 → id 串比较决胜负（542 臂）。
	if got := chainCompareCatalogItems(
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ID: ptrString("b")},
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ID: ptrString("a")},
	); got <= 0 {
		t.Fatalf("id 决胜 = %d, want > 0", got)
	}
	// release 新者排前 / 旧者排后。
	if got := chainCompareCatalogItems(
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ReleaseDate: &newer},
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ReleaseDate: &older},
	); got != -1 {
		t.Fatalf("新 release = %d, want -1", got)
	}
	if got := chainCompareCatalogItems(
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ReleaseDate: &older},
		gatewayruntimecache.ProviderModelCatalogItem{Model: "m", ReleaseDate: &newer},
	); got != 1 {
		t.Fatalf("旧 release = %d, want 1", got)
	}
}

func ptrString(v string) *string { return &v }

func TestChainMergeCatalogItemsIdentityAndEmpty(t *testing.T) {
	items := []gatewayruntimecache.ProviderModelCatalogItem{
		{ProviderCode: "a", Model: "  ", Scope: "global"},   // 空 model 直接跳过（388 臂）
		{ProviderCode: "A", Model: "m1", Scope: "global"},   // 身份键 a/m1
		{ProviderCode: "b", Model: "m1", Scope: "global"},   // 身份键 b/m1（hybrid 身份保真，392 臂）
		{ProviderCode: "a", Model: "m1", Scope: "personal"}, // 同键高优先级替换
	}
	merged := chainMergeCatalogItems(items, true)
	if len(merged) != 2 {
		t.Fatalf("身份保真合并 = %#v", merged)
	}
	byProvider := map[string]gatewayruntimecache.ProviderModelCatalogItem{}
	for _, item := range merged {
		byProvider[item.ProviderCode] = item
	}
	// 同一归一化键 a/m1：personal 后到且优先级更高 → 替换原行（原行 ProviderCode "A" 被覆盖）。
	if got, ok := byProvider["a"]; !ok || got.Scope != "personal" {
		t.Fatalf("高优先级必须替换同键行: %#v", byProvider)
	}
	if _, ok := byProvider["b"]; !ok {
		t.Fatalf("身份保真必须保留 b/m1: %#v", merged)
	}
	// 非 hybrid：同 model 直接合并，后者胜。
	plain := chainMergeCatalogItems([]gatewayruntimecache.ProviderModelCatalogItem{
		{ProviderCode: "a", Model: "m2", Scope: "global"},
		{ProviderCode: "b", Model: "m2", Scope: "global"},
	}, false)
	if len(plain) != 1 || plain[0].ProviderCode != "b" {
		t.Fatalf("非身份合并 = %#v", plain)
	}
	// 低优先级后到不得替换（personal 先、global 后）。
	skip := chainMergeCatalogItems([]gatewayruntimecache.ProviderModelCatalogItem{
		{ProviderCode: "a", Model: "m3", Scope: "personal"},
		{ProviderCode: "b", Model: "m3", Scope: "global"},
	}, false)
	if len(skip) != 1 || skip[0].Scope != "personal" {
		t.Fatalf("低优先级不得替换 = %#v", skip)
	}
}

// --- DB-backed：sourceProviderCodes 扩展臂 / 错误臂 / 列表过滤臂 ---

func TestChainCatalogSourceProviderCodesExpansion(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	for _, row := range []struct{ id, code, protocol, version string }{
		{"prov_w40_gpt", "gpt", "openai", "v1"},
		{"prov_w40_ant", "ant", "anthropic", "v1"},
		{"prov_w40_gem", "gem", "gemini", "v1beta"},
	} {
		if _, err := fixture.db.Exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)`, row.id, row.code, strings.ToUpper(row.code), now, now); err != nil {
			t.Fatalf("seed provider %s: %v", row.code, err)
		}
		if _, err := fixture.db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, 'https://w40.invalid', 'm', '[]', '{}', ?, ?)`,
			"prof_"+row.id, row.code, row.code+" profile", row.protocol, row.version, now, now); err != nil {
			t.Fatalf("seed profile %s: %v", row.code, err)
		}
	}
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	ctx := context.Background()

	// 归一化后为空 → 空集（224 臂）。
	if codes, err := source.sourceProviderCodes(ctx, "   "); err != nil || len(codes) != 0 {
		t.Fatalf("空白 provider = %#v err=%v", codes, err)
	}

	// hybrid → 三协议子供应商展开（227-242 臂）。
	hybrid, err := source.sourceProviderCodes(ctx, "hybrid")
	if err != nil {
		t.Fatalf("hybrid expansion: %v", err)
	}
	for _, want := range []string{"gpt", "ant", "gem"} {
		found := false
		for _, code := range hybrid {
			if code == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("hybrid 展开缺 %s: %#v", want, hybrid)
		}
	}
	for _, code := range hybrid {
		if code == "hybrid" {
			t.Fatalf("hybrid 展开不得包含自身: %#v", hybrid)
		}
	}

	// openai 兼容目标 → openai 协议子供应商 + 自身。
	openai, err := source.sourceProviderCodes(ctx, "openai")
	if err != nil {
		t.Fatalf("openai expansion: %v", err)
	}
	if len(openai) < 2 {
		t.Fatalf("openai 展开至少含子供应商与自身: %#v", openai)
	}
}

func TestChainCatalogSourceClosedDBErrorArms(t *testing.T) {
	db, err := sql.Open("sqlite", "file:w40-closed-catalog?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	source, err := newChainCatalogSource(db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx := context.Background()
	if _, err := source.protocolProviderCodes(ctx, "openai", "v1"); err == nil {
		t.Fatal("关闭连接上 protocolProviderCodes 必须报错")
	}
	if _, err := source.sourceProviderCodes(ctx, "openai"); err == nil {
		t.Fatal("关闭连接上 openai 扩展必须报错（248 臂）")
	}
	if _, err := source.sourceProviderCodes(ctx, "hybrid"); err == nil {
		t.Fatal("关闭连接上 hybrid 扩展必须报错（231 臂）")
	}
	if _, err := source.ListProviderModelCatalog(ctx, gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt"}); err == nil {
		t.Fatal("关闭连接上目录读取必须报错（148 臂）")
	}
}

func TestChainListProviderModelCatalogFilterArms(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	// 目录目标 passthrough 供应商（非 openai 兼容 / 非 hybrid）。
	if _, err := fixture.db.Exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov_w40_drop', 'catdrop', 'CatDrop', 1, ?, ?)`, now, now); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof_w40_drop', 'catdrop', 'CatDrop profile', 1, 'openai', 'v1', 'https://catdrop.invalid', 'm', '[]', '{}', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	// audio 模式行：active + 有价，SQL 放行 → isSupported 臂剔除（186）。
	if _, err := fixture.db.Exec(`INSERT INTO provider_model_catalog (
			id, status, provider_code, model, mode, supported_api_protocols_json, source, catalog_visible, supports_prompt_caching, input_usd_per_1m, created_at, updated_at)
		VALUES ('cat_w40_audio', 'active', 'catdrop', 'catdrop-tts-audio', 'audio', '["chat_completions"]', 'builtin', 1, 0, 1.0, ?, ?)`, now, now); err != nil {
		t.Fatalf("seed audio row: %v", err)
	}
	// 无价自定义行：active、可支持 → unpriced 臂剔除（192）。
	if _, err := fixture.db.Exec(`INSERT INTO custom_provider_models (
			id, provider_code, model, scope, system_account_id, status, catalog_visible,
			supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			service_tier_prices_json, created_by, created_at, updated_at)
		VALUES ('cpm_w40_free', 'catdrop', 'catdrop-free-model', 'global', NULL, 'active', 1,
			'[]', '[]', '[]', '[]', 'sys_owner', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed unpriced row: %v", err)
	}
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	// 空白 provider → 空目录（151/153 臂）。
	empty, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "   "})
	if err != nil || len(empty) != 0 {
		t.Fatalf("空白 provider 目录 = %#v err=%v", empty, err)
	}

	items, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "catdrop"})
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	models := map[string]bool{}
	for _, item := range items {
		models[item.Model] = true
	}
	if models["catdrop-tts-audio"] {
		t.Fatalf("audio 模式行必须被过滤: %#v", items)
	}
	if models["catdrop-free-model"] {
		t.Fatalf("无价行必须被过滤（IncludeUnpriced=false）: %#v", items)
	}
	// 交叉验证：放开 IncludeUnpriced 后 free 行必须可见——证明上面的剔除
	// 走的是 unpriced 臂而非 SQL 可见性过滤。
	withUnpriced, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "catdrop", IncludeUnpriced: true})
	if err != nil {
		t.Fatalf("list catalog with unpriced: %v", err)
	}
	found := false
	for _, item := range withUnpriced {
		if item.Model == "catdrop-free-model" {
			found = true
		}
		if item.Model == "catdrop-tts-audio" {
			t.Fatalf("audio 模式行不得因 IncludeUnpriced 放开而回归: %#v", withUnpriced)
		}
	}
	if !found {
		t.Fatalf("IncludeUnpriced=true 必须放行 free 行: %#v", withUnpriced)
	}
}

func TestAccountModelCatalogReaderAdapterTails(t *testing.T) {
	// 错误臂：无 schema 库 → 查询失败原样返回（1763 错误臂）。
	bare, err := sql.Open("sqlite", "file:w40-adapter-bare?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	t.Cleanup(func() { _ = bare.Close() })
	bareStore, err := providers.NewStore(bare, false, time.Now)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := (accountModelCatalogReaderAdapter{store: bareStore}).ListAccountModelCatalog(context.Background(), "gpt", "sys", true); err == nil {
		t.Fatal("无 schema 库必须报错")
	}

	// 成功臂：fixture 库 → 目录行投影为 facts（1768 成功路径）。
	fixture := newChainFixture(t)
	store, err := providers.NewStore(fixture.db, false, time.Now)
	if err != nil {
		t.Fatalf("new fixture store: %v", err)
	}
	facts, err := (accountModelCatalogReaderAdapter{store: store}).ListAccountModelCatalog(context.Background(), "openai", fixture.systemAccount, false)
	if err != nil {
		t.Fatalf("adapter list: %v", err)
	}
	for _, fact := range facts {
		if fact.Model == "" {
			t.Fatalf("fact 缺 model: %#v", facts)
		}
	}
}
