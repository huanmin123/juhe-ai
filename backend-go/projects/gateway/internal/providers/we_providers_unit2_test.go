package providers

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// catalog.go：模型 token 匹配与选项查询
// ---------------------------------------------------------------------------

func TestWeMatchesModelToken(t *testing.T) {
	tests := []struct {
		model string
		token string
		want  bool
	}{
		{"gpt-4o-mini", "mini", true},
		{"minimini", "mini", false},
		{"gpt.mini", "mini", true},
		{"gpt_mini", "mini", true},
		{"mini", "mini", true},
		{"gpt-minimum", "mini", false},
		{"", "mini", false},
	}
	for _, tt := range tests {
		if got := matchesModelToken(tt.model, tt.token); got != tt.want {
			t.Fatalf("matchesModelToken(%q, %q) = %v", tt.model, tt.token, got)
		}
	}
	if !modelTokenBoundary("x-mini", 2) || modelTokenBoundary("xmini", 1) || !modelTokenBoundary("mini", 0) {
		t.Fatal("modelTokenBoundary 语义错误")
	}
	if !modelTokenBoundaryEnd("mini-x", 4) || modelTokenBoundaryEnd("minix", 4) || !modelTokenBoundaryEnd("mini", 4) {
		t.Fatal("modelTokenBoundaryEnd 语义错误")
	}
	if minInt(1, 2) != 1 || minInt(2, 1) != 1 {
		t.Fatal("minInt 语义错误")
	}
}

func TestWeTodayTextAndProviderDialects(t *testing.T) {
	if (&Store{}).todayText() != "date('now')" {
		t.Fatal("sqlite todayText 错误")
	}
	if (&Store{pg: true}).todayText() != "CURRENT_DATE::text" {
		t.Fatal("pg todayText 错误")
	}
	if ensureCtx(nil) == nil {
		t.Fatal("ensureCtx(nil) 应回落 Background")
	}
	if ensureCtx(context.Background()) == nil {
		t.Fatal("非 nil ctx 应原样返回")
	}
}

func TestWeProviderModelUsableAsDefault(t *testing.T) {
	visible := true
	hidden := false
	future := "2099-01-01"
	past := "2000-01-01"
	textMode := "text"
	imageMode := "image"
	protocols := []string{"chat_completions"}

	if !providerModelIsUsableAsDefault("active", &visible, nil, nil, nil) {
		t.Fatal("active 可见模型应可用")
	}
	if providerModelIsUsableAsDefault("disabled", &visible, nil, nil, nil) {
		t.Fatal("disabled 不可用")
	}
	if providerModelIsUsableAsDefault("active", &hidden, nil, nil, nil) {
		t.Fatal("目录隐藏不可用")
	}
	if providerModelIsUsableAsDefault("active", &visible, &past, nil, nil) {
		t.Fatal("已停服不可用")
	}
	if !providerModelIsUsableAsDefault("active", &visible, &future, nil, nil) {
		t.Fatal("未来停服日期仍可用")
	}
	if providerModelIsUsableAsDefault("active", &visible, nil, &imageMode, nil) {
		t.Fatal("image 模式不可用")
	}
	if !providerModelIsUsableAsDefault("active", &visible, nil, &textMode, protocols) {
		t.Fatal("文本协议应可用")
	}
	if providerModelIsUsableAsDefault("active", &visible, nil, nil, []string{"images"}) {
		t.Fatal("无文本协议不可用")
	}
}

func TestWeServiceTierNormalizers(t *testing.T) {
	negative := -1.0
	prices := map[string]ModelPriceSet{
		"priority": {
			InputUsdPer1M: ptrFloat64(2), OutputUsdPer1M: &negative,
			CachedInputUsdPer1M: ptrFloat64(1), CacheWriteUsdPer1M: ptrFloat64(3),
			CacheWrite1hUsdPer1M: ptrFloat64(6), CacheStorageUsdPer1MPerHour: ptrFloat64(0.2),
			ImageInputUsdPer1M: ptrFloat64(4), ImageOutputUsdPer1M: ptrFloat64(16),
			AudioInputUsdPer1M: ptrFloat64(5), AudioOutputUsdPer1M: ptrFloat64(25),
			OutputUsdPerImage: ptrFloat64(0.06),
		},
		" default": {InputUsdPer1M: ptrFloat64(9)},
		"":         {InputUsdPer1M: ptrFloat64(9)},
	}
	normalized := normalizeServiceTierPricesValue(prices)
	// 契约：default/standard/空白 tier 丢弃；负价格字段丢弃。
	if len(normalized) != 1 {
		t.Fatalf("normalized = %+v", normalized)
	}
	if normalized["priority"].OutputUsdPer1M != nil {
		t.Fatal("负价格应被丢弃")
	}
	set := normalized["priority"]
	if set.InputUsdPer1M == nil || *set.InputUsdPer1M != 2 {
		t.Fatalf("input = %v", set.InputUsdPer1M)
	}
	for name, field := range map[string]*float64{
		"cachedInput": set.CachedInputUsdPer1M, "cacheWrite": set.CacheWriteUsdPer1M,
		"cacheWrite1h": set.CacheWrite1hUsdPer1M, "cacheStorage": set.CacheStorageUsdPer1MPerHour,
		"imageInput": set.ImageInputUsdPer1M, "imageOutput": set.ImageOutputUsdPer1M,
		"audioInput": set.AudioInputUsdPer1M, "audioOutput": set.AudioOutputUsdPer1M,
		"outputPerImage": set.OutputUsdPerImage,
	} {
		if field == nil {
			t.Fatalf("%s 未赋值", name)
		}
	}
	if !modelPriceSetDefined(set) {
		t.Fatal("modelPriceSetDefined 应为 true")
	}
	if modelPriceSetDefined(ModelPriceSet{}) {
		t.Fatal("空 set 应为 undefined")
	}
	keys := sortedTierKeys(map[string]ModelPriceSet{"b": {}, "a": {}, "c": {}})
	if strings.Join(keys, ",") != "a,b,c" {
		t.Fatalf("sortedTierKeys = %v", keys)
	}
	if _, err := normalizeCapabilityTokenArray([]string{" chat_completions ", "chat_completions"}, "能力"); err != nil {
		t.Fatalf("合法 token = %v", err)
	}
	if _, err := normalizeCapabilityTokenArray([]string{"bad token"}, "能力"); err == nil || !strings.Contains(err.Error(), "不支持的值") {
		t.Fatalf("非法 token = %v", err)
	}
	if _, err := normalizeCapabilityTokenArray([]string{""}, "能力"); err == nil {
		t.Fatal("空 token 应报错")
	}
}

func TestWeBooleanQueryValue(t *testing.T) {
	request := httptest.NewRequest("GET", "/x?flag=1", nil)
	if value := booleanQueryValue(request, "flag"); value == nil || !*value {
		t.Fatal("1 应解析为 true")
	}
	request = httptest.NewRequest("GET", "/x?flag=no", nil)
	if value := booleanQueryValue(request, "flag"); value == nil || *value {
		t.Fatal("no 应解析为 false")
	}
	request = httptest.NewRequest("GET", "/x?flag=bogus", nil)
	if value := booleanQueryValue(request, "flag"); value != nil {
		t.Fatal("非法值应返回 nil")
	}
	request = httptest.NewRequest("GET", "/x", nil)
	if value := booleanQueryValue(request, "flag"); value != nil {
		t.Fatal("缺参应返回 nil")
	}
}

// TestWeEnabledNonHybridProviderCodes 锁定模型选项兜底来源的过滤契约：
// 仅启用且非 hybrid 的供应商进入候选。
func TestWeEnabledNonHybridProviderCodes(t *testing.T) {
	env := newTestEnv(t)
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('p1', 'gpt', 'OpenAI', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('p2', 'anthropic', 'Anthropic', 0, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('p3', 'hybrid', 'Hybrid', 1, '[]', ?, ?)`, now, now)

	store, err := NewStore(env.db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := store.EnabledNonHybridProviderCodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(codes, ",") != "gpt" {
		t.Fatalf("codes = %v", codes)
	}
}

func TestWeSupportedCatalogModelAndScopePriority(t *testing.T) {
	if catalogScopePriority("personal") != 3 || catalogScopePriority("global") != 2 || catalogScopePriority("built_in") != 1 {
		t.Fatal("catalogScopePriority 语义错误")
	}
	audioMode := " Audio_Speech "
	supported := ModelCatalogItem{SupportedAPIProtocols: []string{"chat_completions"}, Model: "gpt-x"}
	if !isSupportedCatalogModel(supported) {
		t.Fatal("文本模型应受支持")
	}
	if isSupportedCatalogModel(ModelCatalogItem{Mode: &audioMode, Model: "m"}) {
		t.Fatal("audio 模式不受支持")
	}
	realtime := ModelCatalogItem{SupportedAPIProtocols: []string{"realtime"}, Model: "m"}
	if isSupportedCatalogModel(realtime) {
		t.Fatal("realtime 协议不受支持")
	}
	audioOnly := ModelCatalogItem{SupportedAPIProtocols: []string{"audio"}, Model: "m"}
	if isSupportedCatalogModel(audioOnly) {
		t.Fatal("audio 单协议不受支持")
	}
	if isSupportedCatalogModel(ModelCatalogItem{Model: "gpt-whisper-box"}) {
		t.Fatal("whisper 命名不受支持")
	}
	if isSupportedCatalogModel(ModelCatalogItem{Model: "model_tts"}) {
		t.Fatal("tts 命名不受支持")
	}

	// hasDirectPrice：直接价格或层级价格任一存在即视为有价。
	if hasDirectPrice(ModelCatalogItem{}) {
		t.Fatal("无价格应为 false")
	}
	if !hasDirectPrice(ModelCatalogItem{OutputUsdPerImage: ptrFloat64(0.1)}) {
		t.Fatal("每图价格应视为有价")
	}
	if !hasDirectPrice(ModelCatalogItem{ServiceTierPrices: map[string]ModelPriceSet{"priority": {}}}) {
		t.Fatal("层级价格应视为有价")
	}
}
