// w14d_query_edges_test.go pins the remaining Store query error forks
// (closed-DB injection) and the previously uncovered pure helpers of the
// catalog merge/option/sort families plus the write-side capability
// normalizer branches.
package providers

import (
	"strings"
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestW14dNewStoreGuardsAndClock(t *testing.T) {
	if _, err := NewStore(nil, false, nil); err == nil {
		t.Fatal("nil db must be rejected")
	}
	store := &Store{db: nil, now: nil}
	if store.nowUTC().IsZero() {
		t.Fatal("default clock must be used")
	}
	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	pinned := &Store{db: nil, now: func() time.Time { return fixed }}
	if !pinned.nowUTC().Equal(fixed.UTC()) {
		t.Fatalf("injected clock: %v", pinned.nowUTC())
	}
}

func TestW14dClosedDBQueryForks(t *testing.T) {
	store := w14dClosedStore(t, false)
	ctx := context.Background()

	// Catalog read family.
	if _, err := store.ListProviderModelsForRequest(ctx, "gpt", "", false, true); err == nil {
		t.Fatal("closed DB list for request must fail")
	}
	if _, err := store.listProviderModelCatalog(ctx, "gpt", "", false, true); err == nil {
		t.Fatal("closed DB list catalog must fail")
	}
	if _, err := store.ModelCatalogSourceProviderCodes(ctx, "openai"); err == nil {
		t.Fatal("closed DB source codes must fail")
	}
	if _, err := store.listBuiltInCatalogModels(ctx, []string{"gpt"}, false); err == nil {
		t.Fatal("closed DB built-in catalog must fail")
	}
	if _, err := store.listCustomCatalogModels(ctx, []string{"gpt"}, "", false); err == nil {
		t.Fatal("closed DB custom catalog must fail")
	}
	if _, err := store.listCustomModelRows(ctx, []string{"gpt"}, "u1", false, "m"); err == nil {
		t.Fatal("closed DB custom rows must fail")
	}
	// Option read family.
	if _, err := store.ListProviderModelSelectionOptions(ctx, ModelOptionQuery{ProviderCode: "gpt", Limit: 5}); err == nil {
		t.Fatal("closed DB selection options must fail")
	}
	sourceCodes, builtInCodes, err := store.modelOptionSourceCodes(ctx, "", "openai")
	if err == nil {
		t.Fatal("closed DB option source codes must fail")
	}
	if sourceCodes != nil || builtInCodes != nil {
		t.Fatalf("error result must be nil: %v %v", sourceCodes, builtInCodes)
	}
	if _, _, err := store.modelOptionSourceCodes(ctx, "openai", ""); err == nil {
		t.Fatal("closed DB provider-scoped option sources must fail")
	}
	if _, err := store.listBuiltInModelOptions(ctx, []string{"gpt"}, ModelOptionQuery{Limit: 5}); err == nil {
		t.Fatal("closed DB built-in options must fail")
	}
	if _, err := store.listCustomModelOptions(ctx, []string{"gpt"}, ModelOptionQuery{Limit: 5}); err == nil {
		t.Fatal("closed DB custom options must fail")
	}
	if _, err := store.FindProviderModelCapabilities(ctx, "gpt", "", "m"); err == nil {
		t.Fatal("closed DB capabilities must fail")
	}
	if _, err := store.findBuiltInTestCatalogItems(ctx, []string{"gpt"}, "m"); err == nil {
		t.Fatal("closed DB test catalog items must fail")
	}
	// Provider definition/option family.
	if _, err := store.ListDefinitions(ctx); err == nil {
		t.Fatal("closed DB definitions must fail")
	}
	if _, err := store.FindDefinition(ctx, "gpt"); err == nil {
		t.Fatal("closed DB find definition must fail")
	}
	if _, err := store.ListProviderOptions(ctx); err == nil {
		t.Fatal("closed DB provider options must fail")
	}
	if _, err := store.FindProviderOption(ctx, "gpt"); err == nil {
		t.Fatal("closed DB find provider option must fail")
	}
	if _, err := store.ProtocolProviderCodes(ctx, "openai", "v1"); err == nil {
		t.Fatal("closed DB protocol provider codes must fail")
	}
	if _, err := store.EnabledNonHybridProviderCodes(ctx); err == nil {
		t.Fatal("closed DB enabled codes must fail")
	}
	if _, err := store.ListDefaultHealthCheckModelPreferences(ctx, "u1", []string{"gpt"}); err == nil {
		t.Fatal("closed DB preferences must fail")
	}
	if _, err := store.ListSystemDefaultHealthCheckModels(ctx, []string{"gpt"}); err == nil {
		t.Fatal("closed DB system defaults must fail")
	}
	if err := store.UpsertDefaultHealthCheckModelPreference(ctx, "u1", "gpt", "m"); err == nil {
		t.Fatal("closed DB preference upsert must fail")
	}
	if err := store.UpsertSystemDefaultHealthCheckModel(ctx, "gpt", "m"); err == nil {
		t.Fatal("closed DB system default upsert must fail")
	}
	if _, err := store.findBuiltInModelPatchState(ctx, "cat-1"); err == nil {
		t.Fatal("closed DB built-in patch state must fail")
	}
}

func TestW14dParseJSONArrayEdges(t *testing.T) {
	if got := parseJSONArray(sql.NullString{}); len(got) != 0 {
		t.Fatalf("null JSON array: %v", got)
	}
	if got := parseJSONArray(sql.NullString{String: "{broken", Valid: true}); len(got) != 0 {
		t.Fatalf("broken JSON array: %v", got)
	}
	if got := parseJSONArray(sql.NullString{String: `["a",7," ", "b"]`, Valid: true}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("filtered JSON array: %v", got)
	}
}

func TestW14dNormalizeServiceTierPricesEdges(t *testing.T) {
	if got := normalizeServiceTierPrices(sql.NullString{}); len(got) != 0 {
		t.Fatalf("null tier prices: %v", got)
	}
	if got := normalizeServiceTierPrices(sql.NullString{String: "{broken", Valid: true}); len(got) != 0 {
		t.Fatalf("broken tier prices: %v", got)
	}
	got := normalizeServiceTierPrices(sql.NullString{String: `{
		" p ": {"inputUsdPer1M":1},
		"default": {"inputUsdPer1M":9},
		"standard": {"inputUsdPer1M":9},
		"negative": {"outputUsdPer1M":-1},
		"broken": [1]
	}`, Valid: true})
	if len(got) != 1 || got["p"].InputUsdPer1M == nil || *got["p"].InputUsdPer1M != 1 {
		t.Fatalf("normalized tier prices: %#v", got)
	}
}

func TestW14dMergeProviderModelOptionRowsUnit(t *testing.T) {
	rows := []modelOptionRow{
		{ProviderCode: "gpt", Model: "m-b", Scope: "built_in", ReleaseDate: stringPtr("2025-01-01")},
		{ProviderCode: "gpt", Model: "m-b", Scope: "global", ReleaseDate: stringPtr("2024-01-01")},
		{ProviderCode: "gpt", Model: "m-c", Scope: "global", ReleaseDate: nil},
		{ProviderCode: "gpt", Model: "m-a", Scope: "global", ReleaseDate: stringPtr("2026-01-01")},
		{ProviderCode: " ", Model: "dropped"},
		{ProviderCode: "gpt", Model: "  "},
	}
	merged := mergeProviderModelOptionRows(rows, ModelOptionQuery{Limit: 2})
	if len(merged) != 2 {
		t.Fatalf("limit applied: %v", merged)
	}
	// Newest release first.
	if merged[0].ID != "m-a" {
		t.Fatalf("newest first: %v", merged)
	}
	// The global scope beats built_in at equal model.
	everything := mergeProviderModelOptionRows(rows, ModelOptionQuery{Limit: 10})
	if len(everything) != 3 {
		t.Fatalf("all models: %v", everything)
	}
	// Keyword filtering keeps selected rows even when they do not match.
	picked := mergeProviderModelOptionRows(rows, ModelOptionQuery{Limit: 10, Keyword: "zzz", SelectedIDs: []string{"m-c"}})
	if len(picked) != 1 || picked[0].ID != "m-c" {
		t.Fatalf("selected survives keyword filter: %v", picked)
	}
}

func TestW14dCatalogCompareAndMergeEdges(t *testing.T) {
	// compareProviderModelCatalogItems: shutdown vs missing release dates,
	// catalog order, model and id tie-breaks.
	early := "2020-01-01"
	late := "2025-01-01"
	order1 := int64(1)
	order2 := int64(2)
	if compareProviderModelCatalogItems(
		ModelCatalogItem{ReleaseDate: &early}, ModelCatalogItem{ReleaseDate: &late}) <= 0 {
		t.Fatal("newer release sorts first")
	}
	if compareProviderModelCatalogItems(
		ModelCatalogItem{ReleaseDate: &late}, ModelCatalogItem{ReleaseDate: &early}) >= 0 {
		t.Fatal("older release sorts last")
	}
	if compareProviderModelCatalogItems(
		ModelCatalogItem{ReleaseDate: &early}, ModelCatalogItem{}) >= 0 {
		t.Fatal("dated row sorts before undated")
	}
	if compareProviderModelCatalogItems(
		ModelCatalogItem{}, ModelCatalogItem{ReleaseDate: &early}) <= 0 {
		t.Fatal("undated row sorts after dated")
	}
	if compareProviderModelCatalogItems(
		ModelCatalogItem{CatalogOrder: &order2}, ModelCatalogItem{CatalogOrder: &order1}) <= 0 {
		t.Fatal("lower catalog order sorts first")
	}
	if compareProviderModelCatalogItems(
		ModelCatalogItem{Model: "a", ID: "z"}, ModelCatalogItem{Model: "b", ID: "a"}) >= 0 {
		t.Fatal("model name tie-break")
	}
	if compareProviderModelCatalogItems(
		ModelCatalogItem{Model: "a", ID: "a"}, ModelCatalogItem{Model: "a", ID: "b"}) >= 0 {
		t.Fatal("id tie-break")
	}

	// sortableCatalogReleaseDate.
	if got := sortableCatalogReleaseDate(nil); got != "" {
		t.Fatalf("nil release: %q", got)
	}
	blank := "  "
	if got := sortableCatalogReleaseDate(&blank); got != "" {
		t.Fatalf("blank release: %q", got)
	}

	// mergeModelCatalogItems: personal beats global beats built_in, and the
	// preserve-identity fork keeps hybrid rows separate per provider.
	merged := mergeModelCatalogItems([]ModelCatalogItem{
		{Model: "m", Scope: catalogScopeBuiltIn, ID: "b"},
		{Model: "m", Scope: catalogScopeGlobal, ID: "g"},
		{Model: "m", Scope: catalogScopePersonal, ID: "p"},
	}, false)
	if len(merged) != 1 || merged[0].ID != "p" {
		t.Fatalf("scope priority merge: %v", merged)
	}
	hybrid := mergeModelCatalogItems([]ModelCatalogItem{
		{Model: "m", Scope: catalogScopeGlobal, ProviderCode: "gpt", ID: "1"},
		{Model: "m", Scope: catalogScopeGlobal, ProviderCode: "anthropic", ID: "2"},
	}, true)
	if len(hybrid) != 2 {
		t.Fatalf("identity-preserving merge: %v", hybrid)
	}
	// Blank model names are dropped.
	if got := mergeModelCatalogItems([]ModelCatalogItem{{Model: "  ", ID: "x"}}, false); len(got) != 0 {
		t.Fatalf("blank model dropped: %v", got)
	}

	// catalogScopePriority ordering.
	if !(catalogScopePriority(catalogScopePersonal) > catalogScopePriority(catalogScopeGlobal) &&
		catalogScopePriority(catalogScopeGlobal) > catalogScopePriority(catalogScopeBuiltIn)) {
		t.Fatal("scope priority ordering")
	}
}

func TestW14dHasDirectPriceAndSupport(t *testing.T) {
	if hasDirectPrice(ModelCatalogItem{}) {
		t.Fatal("empty item carries no price")
	}
	if !hasDirectPrice(ModelCatalogItem{ServiceTierPrices: map[string]ModelPriceSet{
		"priority": {InputUsdPer1M: ptrFloat64(1)},
	}}) {
		t.Fatal("tier price counts as a direct price")
	}
	if !isSupportedCatalogModel(ModelCatalogItem{Model: "m", Status: "active"}) {
		t.Fatal("plain active model is supported")
	}
	audioMode := "audio"
	if isSupportedCatalogModel(ModelCatalogItem{Model: "m", Status: "active", Mode: &audioMode}) {
		t.Fatal("audio mode is not supported")
	}
	if isSupportedCatalogModel(ModelCatalogItem{Model: "m", Status: "active", SupportedAPIProtocols: []string{"realtime"}}) {
		t.Fatal("realtime protocol is not supported")
	}
	if isSupportedCatalogModel(ModelCatalogItem{Model: "whisper-1", Status: "active"}) {
		t.Fatal("whisper models are not supported")
	}
}

func TestW14dMatchesModelTokenEdges(t *testing.T) {
	if !matchesModelToken("gpt-4o-mini", "4o") {
		t.Fatal("infix token matches")
	}
	if matchesModelToken("gpt-4o-mini", "5o") {
		t.Fatal("mismatching token must not match")
	}
	if !matchesModelToken("4o", "4o") {
		t.Fatal("whole-name token matches")
	}
	if matchesModelToken("gpt-4o", "gpt-4o-mini") {
		t.Fatal("longer needle must not match")
	}
}

func TestW14dGenerationParameterEdges(t *testing.T) {
	// The max-output-tokens cap rides onto the capability list.
	caps := generationParameterCapabilitiesForModel("gpt", "gpt-4o", ptrInt64(4096))
	if len(caps) == 0 {
		t.Fatal("capabilities expected")
	}
	found := false
	for _, group := range caps {
		for _, capability := range group {
			if capability.Parameter != "maxOutputTokens" {
				continue
			}
			found = true
			if capability.Max != 4096 {
				t.Fatalf("capped max: %v", capability.Max)
			}
		}
	}
	if !found {
		t.Fatal("maxOutputTokens capability missing")
	}
	// Unknown providers carry no capability groups at all.
	if groups := generationParameterCapabilitiesForModel("nope", "no-model", nil); len(groups) != 0 {
		t.Fatalf("unknown provider must be empty: %v", groups)
	}
	// limitGenerationParameterMaxOutputTokens clamps and drops below-min items.
	limited := limitGenerationParameterMaxOutputTokens(map[string][]generationParameterCapability{
		"chat_completions": {
			{Parameter: "maxOutputTokens", Min: 1024, Max: 32768, DefaultValue: 8192},
			{Parameter: "other", Min: 5000, Max: 1000, DefaultValue: 6000},
		},
	}, ptrInt64(2048))
	if len(limited) == 0 {
		t.Fatal("limited list expected")
	}
	for _, group := range limited {
		for _, capability := range group {
			if capability.Parameter == "maxOutputTokens" && capability.Max != 2048 {
				t.Fatalf("clamped max: %v", capability.Max)
			}
		}
	}
}

func TestW14dFixedNumberEdges(t *testing.T) {
	if got := fixedNumber(-1.255, 2); got != "-1.26" && got != "-1.25" {
		t.Fatalf("negative rounding: %q", got)
	}
	if got := fixedNumber(1.5, 0); got != "2" {
		t.Fatalf("zero digits: %q", got)
	}
	if got := fixedNumber(2.5, 0); got != "3" {
		t.Fatalf("half-up: %q", got)
	}
	if got := fixedNumber(0.5, 3); got != "0.500" {
		t.Fatalf("padding: %q", got)
	}
}

func TestW14dNormalizeCustomModelCapabilitiesDirect(t *testing.T) {
	cases := []struct {
		name    string
		provider string
		input   customModelCapabilityInput
		wantErr string
	}{
		{"invalid mode", "gpt", customModelCapabilityInput{Mode: stringPtr("video")}, "当前只支持文本和图像自定义模型"},
		{"bad tier token", "gpt", customModelCapabilityInput{SupportedServiceTiers: []string{"!!"}}, "服务等级包含不支持的值"},
		{"bad effort token", "gpt", customModelCapabilityInput{SupportedReasoningEfforts: []string{"!"}}, "思考级别包含不支持的值"},
		{"gpt too many tiers", "gpt", customModelCapabilityInput{SupportedServiceTiers: []string{"a", "b", "c"}}, "自定义模型参数无效"},
		{"gpt too many efforts", "gpt", customModelCapabilityInput{SupportedReasoningEfforts: []string{"a", "b", "c", "d", "e", "f", "g", "h"}}, "自定义模型参数无效"},
		{"gpt foreign tier", "gpt", customModelCapabilityInput{SupportedServiceTiers: []string{"standard"}}, "自定义模型参数无效"},
		{"gpt foreign effort", "gpt", customModelCapabilityInput{SupportedReasoningEfforts: []string{"ultra"}}, "自定义模型参数无效"},
		{"image with tiers", "gpt", customModelCapabilityInput{Mode: stringPtr("image"), SupportedServiceTiers: []string{"priority"}}, "只有文本自定义模型支持服务等级和思考能力配置"},
		{"image with tier prices", "gpt", customModelCapabilityInput{Mode: stringPtr("image"), ServiceTierPrices: map[string]ModelPriceSet{
			"priority": {InputUsdPer1M: ptrFloat64(1)},
		}}, "只有文本自定义模型支持服务档位价格"},
		{"tier price outside tiers", "gpt", customModelCapabilityInput{ServiceTierPrices: map[string]ModelPriceSet{
			"flex": {InputUsdPer1M: ptrFloat64(1)},
		}}, "服务档位价格必须属于模型支持的服务等级"},
	}
	for _, testCase := range cases {
		if _, err := normalizeCustomModelCapabilities(testCase.provider, testCase.input); err == nil || !containsAll(err.Error(), testCase.wantErr) {
			t.Fatalf("%s: got %v want %q", testCase.name, err, testCase.wantErr)
		}
	}
	// The happy path keeps the normalized token arrays.
	ok, err := normalizeCustomModelCapabilities("gpt", customModelCapabilityInput{
		Mode:                      stringPtr(" text "),
		ServiceTierPrices:         map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(1)}, "standard": {}},
		SupportedServiceTiers:     []string{" priority "},
		SupportedReasoningEfforts: []string{"high", "high"},
	})
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if len(ok.supportedServiceTiers) != 1 || ok.supportedServiceTiers[0] != "priority" {
		t.Fatalf("normalized tiers: %v", ok.supportedServiceTiers)
	}
	if len(ok.supportedReasoningEfforts) != 1 || ok.supportedReasoningEfforts[0] != "high" {
		t.Fatalf("deduped efforts: %v", ok.supportedReasoningEfforts)
	}
}

func TestW14dApplyParsedFieldsAllKeys(t *testing.T) {
	mode := "text"
	price := 2.5
	window := int64(99)
	effort := "high"
	parsed := &customModelParsedInput{
		present: map[string]bool{},
		values: customProviderModelUpsertInput{
			Mode: &mode, SupportedAPIProtocols: []string{"responses"},
			SupportedServiceTiers: []string{"flex"}, SupportedReasoningEfforts: []string{"low"},
			DefaultReasoningEffort: &effort, ReleaseDate: stringPtr("2024-01-01"),
			ShutdownDate: stringPtr("2999-01-01"), ContextWindowTokens: &window,
			MaxInputTokens: &window, MaxOutputTokens: &window,
			InputUsdPer1M: &price, OutputUsdPer1M: &price, CachedInputUsdPer1M: &price,
			CacheWriteUsdPer1M: &price, CacheWrite1hUsdPer1M: &price,
			CacheStorageUsdPer1MPerHour: &price,
			ServiceTierPrices:           map[string]ModelPriceSet{"flex": {InputUsdPer1M: &price}},
			ImageInputUsdPer1M:          &price, ImageOutputUsdPer1M: &price,
			AudioInputUsdPer1M: &price, AudioOutputUsdPer1M: &price,
			OutputUsdPerImage: &price, PricingNotes: stringPtr("p"),
			CapabilityNotes: stringPtr("c"), Notes: stringPtr("n"),
		},
		status: "draft",
	}
	for _, key := range []string{"mode", "supportedApiProtocols", "supportedServiceTiers",
		"supportedReasoningEfforts", "defaultReasoningEffort", "releaseDate", "shutdownDate",
		"contextWindowTokens", "maxInputTokens", "maxOutputTokens", "inputUsdPer1M",
		"outputUsdPer1M", "cachedInputUsdPer1M", "cacheWriteUsdPer1M", "cacheWrite1hUsdPer1M",
		"cacheStorageUsdPer1MPerHour", "serviceTierPrices", "imageInputUsdPer1M",
		"imageOutputUsdPer1M", "audioInputUsdPer1M", "audioOutputUsdPer1M", "outputUsdPerImage",
		"pricingNotes", "capabilityNotes", "notes", "status"} {
		parsed.present[key] = true
	}
	target := &customProviderModelUpsertInput{}
	applyParsedCustomModelFields(target, parsed)
	if target.Mode == nil || *target.Mode != "text" || target.ContextWindowTokens == nil ||
		target.InputUsdPer1M == nil || target.Status != "draft" ||
		len(target.SupportedAPIProtocols) != 1 || target.ServiceTierPrices["flex"].InputUsdPer1M == nil ||
		target.PricingNotes == nil || target.Notes == nil {
		t.Fatalf("applied target: %+v", target)
	}
}

func TestW14dNullableHelpersEdges(t *testing.T) {
	bad := "not-a-date"
	if nullableDatePtr(&bad) != nil {
		t.Fatal("malformed date collapses to nil")
	}
	if normalizedDateCopy(&bad) != nil {
		t.Fatal("malformed date copy collapses to nil")
	}
	if normalizedDateCopy(nil) != nil {
		t.Fatal("nil date copy stays nil")
	}
	negative := int64(-5)
	if nullableInt64Ptr(&negative) != nil {
		t.Fatal("negative integer collapses to nil")
	}
	negativePrice := -1.5
	if nullableFloat64Ptr(&negativePrice) != nil {
		t.Fatal("negative price collapses to nil")
	}
	if nullableTextValue("  x  ") != "x" || nullableTextValue("   ") != nil {
		t.Fatal("nullable text semantics")
	}
	if got := stringPtr("v"); *got != "v" {
		t.Fatalf("string pointer: %v", got)
	}
	// patchValuesEqual uses JSON equality with nil as "null".
	if !patchValuesEqual(nil, nil) || !patchValuesEqual("a", "a") || patchValuesEqual("a", "b") {
		t.Fatal("patch value equality")
	}
	if !patchValuesEqual((*string)(nil), nil) {
		t.Fatal("typed nil equals bare nil via JSON encoding")
	}
	// serviceTierPriceKeysWithPrices sorts and drops blank tiers.
	keys := serviceTierPriceKeysWithPrices(map[string]ModelPriceSet{
		"z": {InputUsdPer1M: ptrFloat64(1)},
		"a": {InputUsdPer1M: ptrFloat64(1)},
		"m": {InputUsdPer1M: ptrFloat64(1)},
		"e": {},
	})
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "m" || keys[2] != "z" {
		t.Fatalf("sorted keys: %v", keys)
	}
}

// containsAll reports whether the message carries every fragment (the
// label-with-value error texts embed the offending token).
func containsAll(message string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if !strings.Contains(message, prefix) {
			return false
		}
	}
	return true
}
