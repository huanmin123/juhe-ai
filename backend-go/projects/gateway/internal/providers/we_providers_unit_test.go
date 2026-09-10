package providers

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// derived.go：生成参数能力投影（与 Node chat-generation-parameters.ts 对齐）
// ---------------------------------------------------------------------------

func weCapabilityParams(capabilities map[string][]generationParameterCapability, protocol string) []string {
	out := []string{}
	for _, item := range capabilities[protocol] {
		out = append(out, item.Parameter)
	}
	return out
}

func TestWeGenerationParameterCapabilitiesByProvider(t *testing.T) {
	limit := int64(4096)
	tests := []struct {
		name          string
		provider      string
		model         string
		wantChat      []string
		wantResponses []string
	}{
		{"gpt 常规模型全参数", "GPT", "gpt-4o",
			[]string{"temperature", "topP", "frequencyPenalty", "presencePenalty", "maxOutputTokens", "seed"},
			[]string{"temperature", "topP", "maxOutputTokens"}},
		{"gpt-5 收缩为子集", "gpt", "gpt-5-turbo",
			[]string{"frequencyPenalty", "presencePenalty", "maxOutputTokens", "seed"},
			[]string{"maxOutputTokens"}},
		{"xai 普通模型", "xai", "grok-2",
			[]string{"temperature", "topP", "frequencyPenalty", "presencePenalty", "maxOutputTokens", "seed"},
			[]string{"temperature", "topP", "maxOutputTokens"}},
		{"xai reasoning 收缩", "xai", "grok-reasoning-1",
			[]string{"temperature", "topP", "maxOutputTokens", "seed"},
			[]string{"temperature", "topP", "maxOutputTokens"}},
		{"deepseek-chat 三参数", "deepseek", "deepseek-chat",
			[]string{"temperature", "topP", "maxOutputTokens"}, nil},
		{"deepseek 其他单参数", "DeepSeek", "deepseek-reasoner",
			[]string{"maxOutputTokens"}, nil},
		{"anthropic 新采样限制", "anthropic", "claude-sonnet-4.8",
			[]string{"maxOutputTokens"}, nil},
		{"anthropic 旧模型三参数", "anthropic", "claude-3-haiku",
			[]string{"temperature", "topP", "maxOutputTokens"}, nil},
		{"gemini-3 双协议单参数", "gemini", "gemini-3-pro",
			[]string{"maxOutputTokens"}, []string{"maxOutputTokens"}},
		{"gemini 常规采样", "Gemini", "gemini-2.5-pro",
			[]string{"temperature", "topP", "maxOutputTokens"},
			[]string{"temperature", "topP", "maxOutputTokens"}},
		{"glm 温度上限收紧", "glm", "glm-4",
			[]string{"temperature", "topP", "maxOutputTokens"}, nil},
		{"未知供应商空能力", "unknown-provider", "x", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capabilities := generationParameterCapabilitiesForModel(tt.provider, tt.model, &limit)
			if got := weCapabilityParams(capabilities, "chat_completions"); strings.Join(got, ",") != strings.Join(tt.wantChat, ",") {
				t.Fatalf("chat = %v, want %v", got, tt.wantChat)
			}
			gotResponses := weCapabilityParams(capabilities, "responses")
			wantResponses := tt.wantResponses
			if wantResponses == nil {
				wantResponses = []string{}
			}
			if strings.Join(gotResponses, ",") != strings.Join(wantResponses, ",") {
				t.Fatalf("responses = %v, want %v", gotResponses, wantResponses)
			}
		})
	}

	// glm 的温度/topP 边界覆盖。
	glmCapabilities := generationParameterCapabilitiesForModel("glm", "glm-4", nil)
	chatCapabilities := glmCapabilities["chat_completions"]
	if chatCapabilities[0].Max != 1 {
		t.Fatalf("glm temperature max = %v", chatCapabilities[0].Max)
	}
	if chatCapabilities[1].Min != 0.01 {
		t.Fatalf("glm topP min = %v", chatCapabilities[1].Min)
	}

	// maxOutputTokens 上限随模型配置收紧，默认值同步收紧。
	clamped := generationParameterCapabilitiesForModel("gpt", "gpt-4o", &limit)
	entries := clamped["responses"]
	for _, entry := range entries {
		if entry.Parameter == "maxOutputTokens" {
			if entry.Max != 4096 || entry.DefaultValue != 4096 {
				t.Fatalf("clamped = %+v", entry)
			}
		}
	}
}

func TestWeLimitGenerationParameterMaxOutputTokens(t *testing.T) {
	capabilities := generationParameterCapabilitiesForModel("gpt", "gpt-4o", nil)
	// nil 上限原样返回。
	if got := limitGenerationParameterMaxOutputTokens(capabilities, nil); len(got) == 0 {
		t.Fatal("nil 上限应原样返回")
	}
	small := int64(2048)
	limited := limitGenerationParameterMaxOutputTokens(capabilities, &small)
	for _, items := range limited {
		for _, entry := range items {
			if entry.Parameter == "maxOutputTokens" {
				if entry.Max != 2048 {
					t.Fatalf("max = %v", entry.Max)
				}
				if entry.DefaultValue > 2048 {
					t.Fatalf("default 未收紧: %v", entry.DefaultValue)
				}
			}
		}
	}
	if minFloat64(1, 2) != 1 || minFloat64(2, 1) != 1 {
		t.Fatal("minFloat64 语义错误")
	}
}

// ---------------------------------------------------------------------------
// derived.go：目录展示投影（六家供应商策略）
// ---------------------------------------------------------------------------

func weRichCatalogItem(providerCode string) *ModelCatalogItem {
	visible := true
	inclusive := true
	tierPrices := map[string]ModelPriceSet{
		"priority": {InputUsdPer1M: ptrFloat64(10), OutputUsdPer1M: ptrFloat64(20)},
	}
	threshold := int64(200000)
	inputMultiplier := 2.0
	outputMultiplier := 3.0
	return &ModelCatalogItem{
		ID: "item-1", ProviderCode: providerCode, Model: "model-x", Status: "active",
		CatalogVisible:                          &visible,
		Mode:                                    ptr("text"),
		SupportedReasoningEfforts:               []string{"low", "medium", "none", ""},
		DefaultReasoningEffort:                  ptr("medium"),
		ContextWindowTokens:                     ptrInt64(128000),
		MaxInputTokens:                          ptrInt64(100000),
		MaxOutputTokens:                         ptrInt64(32000),
		InputUsdPer1M:                           ptrFloat64(5),
		OutputUsdPer1M:                          ptrFloat64(15),
		CachedInputUsdPer1M:                     ptrFloat64(2.5),
		CacheWriteUsdPer1M:                      ptrFloat64(6.25),
		CacheWrite1hUsdPer1M:                    ptrFloat64(12),
		CacheStorageUsdPer1MPerHour:             ptrFloat64(0.25),
		ServiceTierPrices:                       tierPrices,
		SupportedServiceTiers:                   []string{"priority", "missing-tier"},
		LongContextInputTokenThreshold:          &threshold,
		LongContextInputTokenThresholdInclusive: &inclusive,
		LongContextInputCostMultiplier:          &inputMultiplier,
		LongContextOutputCostMultiplier:         &outputMultiplier,
		ImageInputUsdPer1M:                      ptrFloat64(8),
		ImageOutputUsdPer1M:                     ptrFloat64(32),
		AudioInputUsdPer1M:                      ptrFloat64(6),
		AudioOutputUsdPer1M:                     ptrFloat64(24),
		OutputUsdPerImage:                       ptrFloat64(0.08),
		SourcePricingCurrency:                   "CNY",
		SourceExchangeRateToUsd:                 ptrFloat64(7.25),
		SourceExchangeRateDate:                  "2026-01-01",
		SourcePricingNote:                       "官方价",
	}
}

func ptr[T any](value T) *T { return &value }

func TestWeCatalogDisplayPerProvider(t *testing.T) {
	// gpt：含模态价格前缀、长上下文、层级、思考、容量与美元换算。
	gptDisplay := buildProviderCatalogDisplay(weRichCatalogItem("gpt"))
	sections := map[string]bool{}
	for _, section := range gptDisplay {
		sections[section.Key] = true
	}
	for _, expected := range []string{"token_pricing", "image_generation", "tier_priority", "long_context", "reasoning", "capacity", "currency_conversion"} {
		if !sections[expected] {
			t.Fatalf("gpt 展示缺少 %s: %+v", expected, gptDisplay)
		}
	}

	// anthropic：1h 缓存写入条目。
	anthropicDisplay := buildProviderCatalogDisplay(weRichCatalogItem("anthropic"))
	anthropicKeys := map[string]bool{}
	for _, section := range anthropicDisplay {
		anthropicKeys[section.Key] = true
	}
	if !anthropicKeys["token_pricing"] || !anthropicKeys["capacity"] {
		t.Fatalf("anthropic 展示: %+v", anthropicDisplay)
	}

	// gemini：长上下文倍率条目（含 >= 前缀）。
	geminiDisplay := buildProviderCatalogDisplay(weRichCatalogItem("gemini"))
	var geminiPricing *catalogDisplaySection
	for index := range geminiDisplay {
		if geminiDisplay[index].Key == "token_pricing" {
			geminiPricing = &geminiDisplay[index]
		}
	}
	if geminiPricing == nil {
		t.Fatal("gemini 缺少 token_pricing")
	}
	foundLongContext := false
	for _, item := range geminiPricing.Items {
		if strings.HasPrefix(item.Key, "long_context_input") && strings.Contains(item.Label, ">= 200K") {
			foundLongContext = true
		}
	}
	if !foundLongContext {
		t.Fatalf("gemini 长上下文条目: %+v", geminiPricing.Items)
	}

	// xai：image 模式切换标题。
	xaiItem := weRichCatalogItem("xai")
	xaiItem.Mode = ptr("image")
	xaiDisplay := buildProviderCatalogDisplay(xaiItem)
	if xaiDisplay[0].Label != "图像输入" {
		t.Fatalf("xai image 标题 = %s", xaiDisplay[0].Label)
	}

	// deepseek 与 glm。
	if display := buildProviderCatalogDisplay(weRichCatalogItem("deepseek")); len(display) == 0 {
		t.Fatal("deepseek 展示为空")
	}
	glmItem := weRichCatalogItem("glm")
	glmDisplay := buildProviderCatalogDisplay(glmItem)
	if glmDisplay[0].Key != "token_pricing" || len(glmDisplay[0].Items) < 3 {
		t.Fatalf("glm 展示: %+v", glmDisplay[0])
	}
	// glm 单一 token 价：输入=输出且无缓存读。
	glmItem.InputUsdPer1M = ptrFloat64(2)
	glmItem.OutputUsdPer1M = ptrFloat64(2)
	glmItem.CachedInputUsdPer1M = nil
	glmSingle := buildProviderCatalogDisplay(glmItem)
	if glmSingle[0].Items[0].Key != "token" {
		t.Fatalf("glm 单价合并: %+v", glmSingle[0].Items)
	}

	// 未知供应商 → Node [] 默认。
	if empty := buildProviderCatalogDisplay(weRichCatalogItem("unknown")); len(empty) != 0 {
		t.Fatalf("未知供应商应返回空: %+v", empty)
	}
}

func TestWeCatalogDisplayFormattingHelpers(t *testing.T) {
	if got := formatTokenThreshold(200000); got != "200K" {
		t.Fatalf("formatTokenThreshold = %s", got)
	}
	if got := formatTokenThreshold(123456); got != "123456" {
		t.Fatalf("非整千应原样: %s", got)
	}
	if itoaInt64(-42) != "-42" || itoaInt64(0) != "0" || itoaInt64(7) != "7" {
		t.Fatal("itoaInt64 语义错误")
	}
	if tierLabel("priority") != "Priority" || tierLabel("flex") != "Flex" || tierLabel("batch") != "Batch API" || tierLabel("scale") != "scale" {
		t.Fatal("tierLabel 语义错误")
	}
	if got := formatUsdRate(7.25); got != "$7.25" {
		t.Fatalf("formatUsdRate = %s", got)
	}
	if got := fixedNumber(0.5, 0); got != "1" {
		t.Fatalf("fixedNumber 0 位 = %s", got)
	}
	if trimTrailingZeros("7.25000000") != "7.25" || trimTrailingZeros("8") != "8" || trimTrailingZeros("2.0000") != "2" {
		t.Fatal("trimTrailingZeros 语义错误")
	}
	// multipliedPrice：缺失价格/缺失倍率/非正倍率。
	price := ptrFloat64(2)
	if multipliedPrice(nil, nil) != nil {
		t.Fatal("缺失价格应返回 nil")
	}
	if got := multipliedPrice(price, nil); *got != 2 {
		t.Fatalf("缺倍率按 1: %v", *got)
	}
	factor := 3.0
	if got := multipliedPrice(price, &factor); *got != 6 {
		t.Fatalf("倍率应生效: %v", *got)
	}
	zero := 0.0
	if got := multipliedPrice(price, &zero); *got != 2 {
		t.Fatalf("非正倍率按 1: %v", *got)
	}
	// 长上下文条目在无阈值时为空。
	item := weRichCatalogItem("gpt")
	item.LongContextInputTokenThreshold = nil
	if entries := longContextTokenEntries(item, "缓存读"); entries != nil {
		t.Fatal("无阈值应返回 nil")
	}
	// 空价格 section 折叠为 nil。
	if section := priceSection("k", "v", nil); section != nil {
		t.Fatal("空 section 应为 nil")
	}
	// 纯空字符串文本条目被丢弃。
	empty := textSection("k", "v", []catalogDisplayItem{{Key: "a", Value: ""}})
	if empty != nil {
		t.Fatal("空字符串条目应被丢弃")
	}
}

// ---------------------------------------------------------------------------
// builtin_patch.go：值归一化与差异计算
// ---------------------------------------------------------------------------

func TestWeBuiltinPatchValueHelpers(t *testing.T) {
	if got := nullableText("  值  "); got != "值" {
		t.Fatalf("nullableText = %v", got)
	}
	if got := nullableText("   "); got != nil {
		t.Fatalf("空白应归 nil: %v", got)
	}
	if got := nullableText(42); got != nil {
		t.Fatalf("非字符串应归 nil: %v", got)
	}
	if got := stringListJSON([]any{"a", 1, "b"}); len(got) != 2 {
		t.Fatalf("stringListJSON 过滤非字符串: %v", got)
	}
	if got := stringListJSON("x"); len(got) != 0 {
		t.Fatalf("非数组应返回空: %v", got)
	}
	if got := nullableIntegerJSON(float64(7)); got != int64(7) {
		t.Fatalf("整数 = %v", got)
	}
	for _, bad := range []any{"x", 1.5, -1.0} {
		if got := nullableIntegerJSON(bad); got != nil {
			t.Fatalf("非法整数 %v 应归 nil, got %v", bad, got)
		}
	}
	if got := nullablePriceJSON(float64(1.5)); got != 1.5 {
		t.Fatalf("价格 = %v", got)
	}
	if got := nullablePriceJSON(-0.5); got != nil {
		t.Fatalf("负价格应归 nil: %v", got)
	}
	if got := anySlice([]string{"a", "b"}); len(got) != 2 {
		t.Fatalf("anySlice = %v", got)
	}
	prices := serviceTierPricesFromAny(map[string]any{
		"priority": map[string]any{"inputUsdPer1M": float64(10), "outputUsdPer1M": float64(20), "bogus": float64(1)},
		"broken":   "not-a-map",
	})
	if len(prices) != 1 || prices["priority"].InputUsdPer1M == nil || *prices["priority"].InputUsdPer1M != 10 {
		t.Fatalf("serviceTierPricesFromAny = %+v", prices)
	}
	if empty := serviceTierPricesFromAny("x"); len(empty) != 0 {
		t.Fatalf("非对象应返回空: %v", empty)
	}
	toAny := serviceTierPricesToAny(prices)
	if len(toAny) != 1 {
		t.Fatalf("serviceTierPricesToAny = %v", toAny)
	}
	if normalizedDateString(42) != nil {
		t.Fatal("非字符串日期应归 nil")
	}
	if normalizedDateString("  ") != nil {
		t.Fatal("空白日期应归 nil")
	}
	if normalizedDateString("2026/01/01") != nil {
		t.Fatal("非法格式日期应归 nil")
	}
	if got := normalizedDateString(" 2026-01-01 "); got == nil || *got != "2026-01-01" {
		t.Fatalf("normalizedDateString = %v", got)
	}
	stamp := nextProviderModelUpdatedAt("2026-01-01T00:00:00.000Z", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if stamp != "2026-01-01T00:00:00.001Z" {
		t.Fatalf("nextProviderModelUpdatedAt = %s", stamp)
	}
}

func TestWeConfigurationChangesAndCurrentValue(t *testing.T) {
	visible := true
	current := &ModelCatalogItem{
		ID: "cat-x", ProviderCode: "gpt", Model: "gpt-x", Status: "active",
		CatalogVisible:              &visible,
		Mode:                        ptr("text"),
		SupportedAPIProtocols:       []string{"chat_completions"},
		SupportedServiceTiers:       []string{"priority"},
		SupportedReasoningEfforts:   []string{"low"},
		DefaultReasoningEffort:      ptr("low"),
		ReleaseDate:                 ptr("2024-01-01"),
		ShutdownDate:                ptr("2030-01-01"),
		ContextWindowTokens:         ptrInt64(1000),
		MaxInputTokens:              ptrInt64(900),
		MaxOutputTokens:             ptrInt64(800),
		InputUsdPer1M:               ptrFloat64(1),
		OutputUsdPer1M:              ptrFloat64(2),
		CachedInputUsdPer1M:         ptrFloat64(0.5),
		CacheWriteUsdPer1M:          ptrFloat64(1.25),
		CacheWrite1hUsdPer1M:        ptrFloat64(2.5),
		CacheStorageUsdPer1MPerHour: ptrFloat64(0.1),
		ServiceTierPrices:           map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(3)}},
		ImageInputUsdPer1M:          ptrFloat64(4),
		ImageOutputUsdPer1M:         ptrFloat64(5),
		AudioInputUsdPer1M:          ptrFloat64(6),
		AudioOutputUsdPer1M:         ptrFloat64(7),
		OutputUsdPerImage:           ptrFloat64(0.1),
	}

	// 相同值不产生变更；不同值保留（含 nil vs 值、未知键）。
	samePatch := []builtinPatchField{
		{Name: "status", Value: "active"},
		{Name: "catalogVisible", Value: true},
		{Name: "mode", Value: "text"},
		{Name: "supportedApiProtocols", Value: anySlice(current.SupportedAPIProtocols)},
		{Name: "serviceTierPrices", Value: map[string]any{"priority": map[string]any{"inputUsdPer1M": float64(3)}}},
		{Name: "unknownField", Value: "x"},
	}
	if changes := configurationChanges(current, samePatch); len(changes) != 1 || changes[0].Name != "unknownField" {
		t.Fatalf("changes = %+v", changes)
	}
	// nil 指针字段的当前值投影为 nil。
	empty := &ModelCatalogItem{ID: "cat-y", Status: "active"}
	if got := builtInCurrentValue(empty, "catalogVisible"); got != nil {
		t.Fatalf("nil catalogVisible = %v", got)
	}
	if got := builtInCurrentValue(empty, "inputUsdPer1M"); got != nil {
		t.Fatalf("nil 价格 = %v", got)
	}
	// builtInCurrentValue 覆盖全部字段名。
	for _, field := range []string{
		"status", "catalogVisible", "mode", "supportedApiProtocols", "supportedServiceTiers",
		"supportedReasoningEfforts", "defaultReasoningEffort", "releaseDate", "shutdownDate",
		"contextWindowTokens", "maxInputTokens", "maxOutputTokens", "inputUsdPer1M", "outputUsdPer1M",
		"cachedInputUsdPer1M", "cacheWriteUsdPer1M", "cacheWrite1hUsdPer1M", "cacheStorageUsdPer1MPerHour",
		"serviceTierPrices", "imageInputUsdPer1M", "imageOutputUsdPer1M", "audioInputUsdPer1M",
		"audioOutputUsdPer1M", "outputUsdPerImage", "nope",
	} {
		if builtInCurrentValue(current, field) == nil && field != "nope" {
			t.Fatalf("字段 %s 的当前值不应为 nil", field)
		}
	}
}

func TestWeBuiltinPatchAssignmentsAndRecords(t *testing.T) {
	store := &Store{}
	patch := []builtinPatchField{
		{Name: "status", Value: "disabled"},
		{Name: "catalogVisible", Value: false},
		{Name: "catalogVisible", Value: "not-bool"},
		{Name: "mode", Value: nil},
		{Name: "supportedApiProtocols", Value: []any{"chat_completions", 3}},
		{Name: "supportedServiceTiers", Value: []any{"priority"}},
		{Name: "supportedReasoningEfforts", Value: []any{}},
		{Name: "defaultReasoningEffort", Value: "low"},
		{Name: "releaseDate", Value: "2024-01-01"},
		{Name: "shutdownDate", Value: nil},
		{Name: "contextWindowTokens", Value: float64(1000)},
		{Name: "maxInputTokens", Value: float64(-5)},
		{Name: "inputUsdPer1M", Value: float64(1.5)},
		{Name: "outputUsdPer1M", Value: nil},
		{Name: "serviceTierPrices", Value: map[string]any{"priority": map[string]any{"inputUsdPer1M": float64(9)}}},
		{Name: "outputUsdPerImage", Value: float64(0.2)},
		{Name: "unknown", Value: 1},
	}
	assignments, params := builtinPatchAssignments(store, patch)
	if len(assignments) == 0 {
		t.Fatal("应产生赋值列")
	}
	joined := strings.Join(assignments, ",")
	for _, expected := range []string{"status", "catalog_visible", "mode", "supported_api_protocols_json",
		"supported_service_tiers_json", "supported_reasoning_efforts_json", "default_reasoning_effort",
		"release_date", "shutdown_date", "context_window_tokens", "input_usd_per_1m", "output_usd_per_1m",
		"service_tier_prices_json", "output_usd_per_image"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("缺少列 %s: %s", expected, joined)
		}
	}
	if len(params) != len(assignments) {
		t.Fatalf("参数与列数不一致: %d vs %d", len(params), len(assignments))
	}

	visible := true
	current := &ModelCatalogItem{ID: "cat-z", ProviderCode: "gpt", Model: "m", Status: "active",
		CatalogVisible: &visible, ShutdownDate: ptr("2030-01-01")}
	record := builtinMutationRecord(current)
	if record.ID != "cat-z" || !record.CatalogVisible {
		t.Fatalf("record = %+v", record)
	}
	visible = false
	updated := builtinMutationRecordOf(current, []builtinPatchField{
		{Name: "status", Value: "disabled"},
		{Name: "catalogVisible", Value: visible},
		{Name: "shutdownDate", Value: nil},
		{Name: "status", Value: 42},
	}, "2026-02-02T00:00:00.000Z")
	if updated.Status != "disabled" || updated.CatalogVisible {
		t.Fatalf("recordOf = %+v", updated)
	}
	if updated.UpdatedAt != "2026-02-02T00:00:00.000Z" {
		t.Fatalf("updatedAt = %s", updated.UpdatedAt)
	}
}

// ---------------------------------------------------------------------------
// catalog.go：测试目录比较器；custom_models / write_handlers 小工具
// ---------------------------------------------------------------------------

func TestWeTestCatalogComparator(t *testing.T) {
	if testCatalogPriority("personal") != 3 || testCatalogPriority("global") != 2 || testCatalogPriority("other") != 1 {
		t.Fatal("testCatalogPriority 语义错误")
	}
	base := func(id, model string) testCatalogItem {
		return testCatalogItem{ID: id, ProviderCode: "gpt", Model: model}
	}
	newer := base("a", "m")
	newer.ReleaseDate = ptr("2025-01-01")
	older := base("b", "m")
	older.ReleaseDate = ptr("2024-01-01")
	if compareProviderModelTestCatalogItems(newer, older) >= 0 {
		t.Fatal("更新发布日期应排前")
	}
	if compareProviderModelTestCatalogItems(older, newer) <= 0 {
		t.Fatal("比较应反对称")
	}
	noDate := base("c", "m")
	if compareProviderModelTestCatalogItems(newer, noDate) >= 0 {
		t.Fatal("有日期应排在无日期之前")
	}
	order1 := base("d", "m")
	order := int64(1)
	order1.CatalogOrder = &order
	order2 := base("e", "m")
	orderBigger := int64(2)
	order2.CatalogOrder = &orderBigger
	if compareProviderModelTestCatalogItems(order1, order2) >= 0 {
		t.Fatal("catalogOrder 小者应排前")
	}
	if cmp := compareProviderModelTestCatalogItems(base("a", "alpha"), base("a", "beta")); cmp >= 0 {
		t.Fatal("模型名排序")
	}
	if cmp := compareProviderModelTestCatalogItems(base("a", "m"), base("b", "m")); cmp >= 0 {
		t.Fatal("ID 排序")
	}
	if textValue(nil) != "" || textValue(ptr("v")) != "v" {
		t.Fatal("textValue 语义错误")
	}
}

func TestWeCustomModelSmallHelpers(t *testing.T) {
	if capabilityTokenPtr(sql.NullString{}) != nil {
		t.Fatal("无效 NullString 应返回 nil")
	}
	if capabilityTokenPtr(sql.NullString{String: "  ", Valid: true}) != nil {
		t.Fatal("空白应返回 nil")
	}
	if capabilityTokenPtr(sql.NullString{String: "bad token!", Valid: true}) != nil {
		t.Fatal("非法 token 应返回 nil")
	}
	if got := capabilityTokenPtr(sql.NullString{String: "chat_completions", Valid: true}); got == nil || *got != "chat_completions" {
		t.Fatalf("capabilityTokenPtr = %v", got)
	}
	if normalizedDateCopy(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}
	if normalizedDateCopy(ptr("nope")) != nil {
		t.Fatal("非法日期应返回 nil")
	}
	if got := normalizedDateCopy(ptr(" 2026-05-01 ")); got == nil || *got != "2026-05-01" {
		t.Fatalf("normalizedDateCopy = %v", got)
	}
	if got := stringSliceFromJSON([]any{"a", 1, "b"}); len(got) != 2 {
		t.Fatalf("stringSliceFromJSON = %v", got)
	}
	if got := stringSliceFromJSON("x"); len(got) != 0 {
		t.Fatalf("非数组应返回空: %v", got)
	}
	// mergedBuiltInItem 把补丁字段并到当前行。
	current := &ModelCatalogItem{ID: "m-1", Status: "active", SupportedAPIProtocols: []string{"chat_completions"}}
	merged := mergedBuiltInItem(current, []builtinPatchField{
		{Name: "status", Value: "disabled"},
		{Name: "catalogVisible", Value: true},
		{Name: "mode", Value: "text"},
		{Name: "supportedApiProtocols", Value: anySlice([]string{"responses"})},
		{Name: "defaultReasoningEffort", Value: "low"},
		{Name: "contextWindowTokens", Value: int64(5)},
		{Name: "maxInputTokens", Value: "bad"},
		{Name: "inputUsdPer1M", Value: 1.5},
		{Name: "outputUsdPer1M", Value: nil},
		{Name: "serviceTierPrices", Value: map[string]any{}},
		{Name: "outputUsdPerImage", Value: 0.3},
		{Name: "unknown", Value: 1},
	})
	if merged.Status != "disabled" || merged.ContextWindowTokens == nil || *merged.ContextWindowTokens != 5 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged.CatalogVisible == nil || !*merged.CatalogVisible {
		t.Fatal("可见性未合并")
	}
	if merged.MaxInputTokens != nil {
		t.Fatal("类型不匹配应归 nil")
	}
	if merged.InputUsdPer1M == nil || *merged.InputUsdPer1M != 1.5 {
		t.Fatalf("价格未合并: %v", merged.InputUsdPer1M)
	}
	if strings.Join(merged.SupportedAPIProtocols, ",") != "responses" {
		t.Fatalf("协议未合并: %v", merged.SupportedAPIProtocols)
	}
}

func TestWeStoreBindAndRequestAccountID(t *testing.T) {
	sqliteStore := &Store{}
	if got := sqliteStore.bind("a = ? AND b = ?"); got != "a = ? AND b = ?" {
		t.Fatalf("sqlite bind = %s", got)
	}
	// PostgreSQL 方言：? 占位符改写为 $n（双模式部署契约）。
	pgStore := &Store{pg: true}
	if got := pgStore.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind = %s", got)
	}
	// boolValue：SQLite 用 1/0，PG 用布尔。
	if sqliteStore.boolValue(true) != 1 || sqliteStore.boolValue(false) != 0 {
		t.Fatal("sqlite boolValue 语义错误")
	}
	if pgStore.boolValue(true) != true {
		t.Fatal("pg boolValue 语义错误")
	}
	if requestSystemAccountID(httptest.NewRequest("GET", "/x", nil)) != "" {
		t.Fatal("匿名请求应返回空账户")
	}
	if got := todayUTCString(); !strings.HasPrefix(got, "20") {
		t.Fatalf("todayUTCString = %s", got)
	}
}
