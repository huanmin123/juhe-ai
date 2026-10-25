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
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
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
	// M1 裁决（音频设计 §2）：mode=audio（含别名）且协议为音频协议的模型
	// 收录；无音频协议的 audio 行、realtime 协议、旧词表单 audio 协议继续
	// 排除；名字 token 排除只对未标注 audio 的模型生效。
	audio := "audio"
	mode := func(m string) *string { return &m }
	if !chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Model: "whisper-1", Mode: &audio, SupportedAPIProtocols: []string{"audio_transcription"}}) {
		t.Fatal("显式 audio 分类的模型必须判支持")
	}
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Mode: mode("audio_speech")}) {
		t.Fatal("无音频协议的 audio_speech 模式必须判不支持")
	}
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Mode: &audio, SupportedAPIProtocols: []string{"chat_completions"}}) {
		t.Fatal("audio 模式 + 对话协议必须判不支持")
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
	// M2 裁决（媒体设计 §2）：mode=video 且协议含 video 的模型收录；无
	// video 协议的 video 行维持排除。
	video := "video"
	if !chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Model: "sora-2", Mode: &video, SupportedAPIProtocols: []string{"video"}}) {
		t.Fatal("video 模式 + video 协议必须判支持")
	}
	if chainIsSupportedCatalogModel(gatewayruntimecache.ProviderModelCatalogItem{Mode: &video, SupportedAPIProtocols: []string{"chat_completions"}}) {
		t.Fatal("video 模式无 video 协议必须判不支持")
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

// --- DB-backed：sourceVendorCodes 恒等维度 / 错误臂 / 列表过滤臂 ---
// 供应商解耦（设计 §3/§8）：providers/provider_protocol_profiles 协议子供应
// 商源扩展随两表退役拆除，目录维度归一为 vendor_code 恒等源；原扩展臂与
// closed-DB 扩展错误臂一并退役。

func TestChainCatalogSourceVendorCodesIdentity(t *testing.T) {
	// 归一化后为空 → 空集。
	if codes := sourceVendorCodes("   "); len(codes) != 0 {
		t.Fatalf("空白 provider = %#v", codes)
	}
	// 任意非空入参（含 hybrid）→ 归一化单值恒等源。
	for input, want := range map[string]string{"hybrid": "hybrid", "OpenAI": "openai", " gpt ": "gpt"} {
		codes := sourceVendorCodes(input)
		if len(codes) != 1 || codes[0] != want {
			t.Fatalf("sourceVendorCodes(%q) = %#v, want [%q]", input, codes, want)
		}
	}
	// openai 兼容目标在内置目录源中剔除自身（openai vendor 无自身内置目录）。
	if codes := chainCatalogBuiltInSourceProviderCodes("openai", []string{"openai"}); len(codes) != 0 {
		t.Fatalf("openai builtin 源剔除后应为空: %#v", codes)
	}
	if codes := chainCatalogBuiltInSourceProviderCodes("gpt", []string{"gpt"}); len(codes) != 1 || codes[0] != "gpt" {
		t.Fatalf("gpt builtin 源保持恒等: %#v", codes)
	}
}

func TestChainCatalogSourceClosedDBErrorArms(t *testing.T) {
	db, err := sql.Open("sqlite", "file:w40-closed-catalog?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	source, err := newChainCatalogSource(db)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx := context.Background()
	if _, err := source.ListProviderModelCatalog(ctx, gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt"}); err == nil {
		t.Fatal("关闭连接上目录读取必须报错（148 臂）")
	}
}

func TestChainListProviderModelCatalogFilterArms(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seedModel := func(id, model, scope string, price any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO models (
				id, vendor_code, public_model, upstream_model, scope, status, catalog_visible,
				supported_endpoint_modes_json, input_usd_per_1m, source, created_at, updated_at)
			VALUES (?, 'catdrop', ?, ?, ?, 'active', TRUE, '["chat_json","chat_sse"]', ?, 'builtin', ?, ?)`),
			id, model, model, scope, price, now, now); err != nil {
			t.Fatalf("seed model row %s: %v", id, err)
		}
	}
	// 名词排除行（whisper token）：active + 有价，SQL 放行 → isSupported 臂
	// 剔除（旧 audio-mode+chat-protocol 行在 mode/协议同源自 models 端点集后
	// 不可再表达，剔除面由名词 token 臂等价覆盖）。
	seedModel("cat_w40_whisper", "catdrop-whisper-x", "builtin", 1.0)
	// 无价自定义行：active、可支持 → unpriced 臂剔除（192）。
	seedModel("cpm_w40_free", "catdrop-free-model", "global", nil)
	source, err := newChainCatalogSource(fixture.db)
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
	if models["catdrop-whisper-x"] {
		t.Fatalf("whisper 名词行必须被过滤: %#v", items)
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
		if item.Model == "catdrop-whisper-x" {
			t.Fatalf("whisper 名词行不得因 IncludeUnpriced 放开而回归: %#v", withUnpriced)
		}
	}
	if !found {
		t.Fatalf("IncludeUnpriced=true 必须放行 free 行: %#v", withUnpriced)
	}
}

// B4 ⑥：TestAccountModelCatalogReaderAdapterTails 随 accountModelCatalogReaderAdapter
// 装配拆除一并删除——断言链已切 models 表身份解析（accounts/model_catalog_validation.go），
// 端口行为由 accounts 包的目录断言测试（model_catalog_validation_test.go）钉住。
