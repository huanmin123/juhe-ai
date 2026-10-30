// t82_capability_declaration_test.go pins the T82 explicit capability
// declaration chain（juhe_business.models 三列 supported_tools_by_protocol_json /
// input_modalities_json / output_modalities_json）：custom 目录行显式能力声明贯通
// 读写链——写侧是词表/校验唯一入口（POST/PATCH 严格解析、矩阵键 ⊆ 生效协议集），
// 读侧 lenient 解析（失败/越界 token 静默丢弃）；显式值优先（空矩阵=未声明，沿
// inheritCustomCatalogCapabilities 仅填空语义）；built_in fork 继续拒绝三键；
// 配置模板复制不携带三键。DB 往返用例经 pgtestkit 授权门（未设
// JUHE_AI_TEST_EXTERNAL=1 时默认 skip）。
package providers

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

func t82PostOptions() customModelParseOptions {
	return customModelParseOptions{
		requireModel:                true,
		allowScope:                  true,
		allowNotes:                  true,
		allowCapabilityDeclarations: true,
		statusValues:                []string{"draft", "active", "disabled"},
	}
}

func t82BuiltInOptions() customModelParseOptions {
	return customModelParseOptions{
		allowCatalogVisible:      true,
		requireExpectedUpdatedAt: true,
		statusValues:             []string{"active", "disabled"},
	}
}

func t82MustParse(t *testing.T, body string, options customModelParseOptions) *customModelParsedInput {
	t.Helper()
	parsed, ok := parseCustomModelBody(w14dDecodeBody(t, body), options)
	if !ok {
		t.Fatalf("parseCustomModelBody rejected %s", body)
	}
	return parsed
}

// --- 写侧解析（严格键集） --------------------------------------------------

func TestT82ParseCapabilityDeclarationsHappy(t *testing.T) {
	parsed := t82MustParse(t, `{"model":"a","supportedToolsByProtocol":{"chat_completions":["function_calling","web_search"],"responses":[]},
		"inputModalities":["text","image"],"outputModalities":["text"]}`, t82PostOptions())
	wantMatrix := map[string][]string{
		"chat_completions": {"function_calling", "web_search"},
		"responses":        {},
	}
	if !reflect.DeepEqual(parsed.values.SupportedToolsByProtocol, wantMatrix) {
		t.Fatalf("matrix: %#v", parsed.values.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(parsed.values.InputModalities, []string{"text", "image"}) {
		t.Fatalf("input modalities: %#v", parsed.values.InputModalities)
	}
	if !reflect.DeepEqual(parsed.values.OutputModalities, []string{"text"}) {
		t.Fatalf("output modalities: %#v", parsed.values.OutputModalities)
	}
	for _, key := range []string{"supportedToolsByProtocol", "inputModalities", "outputModalities"} {
		if !parsed.present[key] {
			t.Fatalf("present[%s] must be set", key)
		}
	}

	// 空 object / 空数组合法（=未声明/清空），且保持非 nil 形状。
	empty := t82MustParse(t, `{"model":"a","supportedToolsByProtocol":{},"inputModalities":[],"outputModalities":[]}`, t82PostOptions())
	if empty.values.SupportedToolsByProtocol == nil || len(empty.values.SupportedToolsByProtocol) != 0 {
		t.Fatalf("empty matrix: %#v", empty.values.SupportedToolsByProtocol)
	}
	if empty.values.InputModalities == nil || empty.values.OutputModalities == nil {
		t.Fatalf("empty modalities: %#v / %#v", empty.values.InputModalities, empty.values.OutputModalities)
	}
}

func TestT82ParseCapabilityDeclarationFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"matrix non-object", `{"model":"a","supportedToolsByProtocol":[]}`},
		{"matrix null", `{"model":"a","supportedToolsByProtocol":null}`},
		{"matrix key bad token", `{"model":"a","supportedToolsByProtocol":{"!!":[]}}`},
		{"matrix key uppercase", `{"model":"a","supportedToolsByProtocol":{"Chat":[]}}`},
		{"matrix value not array", `{"model":"a","supportedToolsByProtocol":{"chat_completions":"function_calling"}}`},
		{"matrix value entry non-string", `{"model":"a","supportedToolsByProtocol":{"chat_completions":[1]}}`},
		{"matrix token outside word list", `{"model":"a","supportedToolsByProtocol":{"chat_completions":["unknown_tool"]}}`},
		{"matrix token bad pattern", `{"model":"a","supportedToolsByProtocol":{"chat_completions":["Bad Token"]}}`},
		{"input modalities null", `{"model":"a","inputModalities":null}`},
		{"input modalities non-array", `{"model":"a","inputModalities":"text"}`},
		{"input modalities outside word list", `{"model":"a","inputModalities":["text2"]}`},
		{"input modalities entry non-string", `{"model":"a","inputModalities":[1]}`},
		{"output modalities null", `{"model":"a","outputModalities":null}`},
		{"output modalities outside word list", `{"model":"a","outputModalities":["smell"]}`},
	}
	for _, testCase := range cases {
		if parsed, ok := parseCustomModelBody(w14dDecodeBody(t, testCase.body), t82PostOptions()); ok {
			t.Fatalf("%s must fail, got %#v", testCase.name, parsed)
		}
	}
}

// built_in fork 不感知三键：共享解析器在 builtin options 下对新键走既有
// default fail（fork 必须继续拒绝这些键）。
func TestT82BuiltInOptionsRejectCapabilityKeys(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"matrix", `{"supportedToolsByProtocol":{"chat_completions":["function_calling"]},"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`},
		{"input modalities", `{"inputModalities":["text"],"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`},
		{"output modalities", `{"outputModalities":["text"],"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`},
	}
	for _, testCase := range cases {
		if parsed, ok := parseCustomModelBody(w14dDecodeBody(t, testCase.body), t82BuiltInOptions()); ok {
			t.Fatalf("builtin options must reject %s, got %#v", testCase.name, parsed)
		}
	}
}

// --- 校验（矩阵键 ⊆ 生效协议集） ------------------------------------------

func TestT82ValidateCapabilityMatrixProtocolSubset(t *testing.T) {
	base := customProviderModelUpsertInput{
		ProviderCode:          "glm",
		SupportedAPIProtocols: []string{"chat_completions"},
	}
	ok := base
	ok.SupportedToolsByProtocol = map[string][]string{"chat_completions": {"function_calling"}}
	if message := validateCustomModelCapabilities("glm", ok); message != "" {
		t.Fatalf("in-protocol matrix must pass: %s", message)
	}
	empty := base
	empty.SupportedToolsByProtocol = map[string][]string{}
	if message := validateCustomModelCapabilities("glm", empty); message != "" {
		t.Fatalf("empty matrix must pass: %s", message)
	}
	outOfScope := base
	outOfScope.SupportedToolsByProtocol = map[string][]string{"responses": {"structured_outputs"}}
	if message := validateCustomModelCapabilities("glm", outOfScope); message != "自定义模型参数无效" {
		t.Fatalf("out-of-protocol matrix key: %q", message)
	}
}

// --- PATCH assignments / merged input --------------------------------------

func t82CurrentRecord() *customProviderModelRecord {
	return &customProviderModelRecord{
		ID:                        "custom_model_t82",
		ProviderCode:              "glm",
		Model:                     "t82-model",
		Scope:                     catalogScopeGlobal,
		Status:                    "active",
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  map[string][]string{"chat_completions": {"function_calling"}},
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedServiceTiers:     []string{},
		SupportedReasoningEfforts: []string{},
	}
}

// 提交键整值替换、等值折叠、缺键保持、空矩阵清空。
func TestT82PatchAssignmentsCapabilityKeys(t *testing.T) {
	store := &Store{}

	current := t82CurrentRecord()
	same := customProviderModelUpsertInput{
		SupportedToolsByProtocol: map[string][]string{"chat_completions": {"function_calling"}},
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	}
	assignments, params, _, err := store.customProviderModelPatchAssignments(current, same,
		[]string{"supportedToolsByProtocol", "inputModalities", "outputModalities"})
	if err != nil {
		t.Fatalf("equal patch: %v", err)
	}
	if len(assignments) != 0 {
		t.Fatalf("equal values must collapse to no assignments: %v", assignments)
	}

	replaced := customProviderModelUpsertInput{
		SupportedToolsByProtocol: map[string][]string{"chat_completions": {"mcp"}},
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text", "audio"},
	}
	assignments, params, merged, err := store.customProviderModelPatchAssignments(current, replaced,
		[]string{"supportedToolsByProtocol", "inputModalities", "outputModalities"})
	if err != nil {
		t.Fatalf("replace patch: %v", err)
	}
	wantAssignments := []string{
		"supported_tools_by_protocol_json = ?",
		"input_modalities_json = ?",
		"output_modalities_json = ?",
	}
	if !reflect.DeepEqual(assignments, wantAssignments) {
		t.Fatalf("assignments: %v", assignments)
	}
	wantParams := []any{
		`{"chat_completions":["mcp"]}`,
		`["text","image"]`,
		`["text","audio"]`,
	}
	if !reflect.DeepEqual(params, wantParams) {
		t.Fatalf("params: %#v", params)
	}
	if !reflect.DeepEqual(merged.SupportedToolsByProtocol, replaced.SupportedToolsByProtocol) {
		t.Fatalf("merged matrix: %#v", merged.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(merged.InputModalities, replaced.InputModalities) {
		t.Fatalf("merged input modalities: %#v", merged.InputModalities)
	}

	// 缺键保持：fields 不含 supportedToolsByProtocol 时矩阵列不进 assignments，
	// merged 保持现值。
	keepCurrent := customProviderModelUpsertInput{InputModalities: []string{"text", "file"}}
	assignments, _, merged, err = store.customProviderModelPatchAssignments(current, keepCurrent, []string{"inputModalities"})
	if err != nil {
		t.Fatalf("keep patch: %v", err)
	}
	if !reflect.DeepEqual(assignments, []string{"input_modalities_json = ?"}) {
		t.Fatalf("keep assignments: %v", assignments)
	}
	if !reflect.DeepEqual(merged.SupportedToolsByProtocol, current.SupportedToolsByProtocol) {
		t.Fatalf("absent matrix key must keep current: %#v", merged.SupportedToolsByProtocol)
	}

	// 空 object/array 提交 = 清空（=未声明）。
	clear := customProviderModelUpsertInput{
		SupportedToolsByProtocol: map[string][]string{},
		InputModalities:          []string{},
		OutputModalities:         []string{},
	}
	assignments, params, merged, err = store.customProviderModelPatchAssignments(current, clear,
		[]string{"supportedToolsByProtocol", "inputModalities", "outputModalities"})
	if err != nil {
		t.Fatalf("clear patch: %v", err)
	}
	if !reflect.DeepEqual(params, []any{`{}`, `[]`, `[]`}) {
		t.Fatalf("clear params: %#v", params)
	}
	if len(merged.SupportedToolsByProtocol) != 0 || len(merged.InputModalities) != 0 || len(merged.OutputModalities) != 0 {
		t.Fatalf("clear merged: %#v", merged)
	}
}

// mergedCustomModelUpsertInput：缺键保持现值、提交键整值替换。
func TestT82MergedInputCapabilityKeys(t *testing.T) {
	current := t82CurrentRecord()
	kept := mergedCustomModelUpsertInput(current, &customModelParsedInput{present: map[string]bool{}})
	if !reflect.DeepEqual(kept.SupportedToolsByProtocol, current.SupportedToolsByProtocol) {
		t.Fatalf("kept matrix: %#v", kept.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(kept.InputModalities, current.InputModalities) ||
		!reflect.DeepEqual(kept.OutputModalities, current.OutputModalities) {
		t.Fatalf("kept modalities: %#v / %#v", kept.InputModalities, kept.OutputModalities)
	}
	parsed := &customModelParsedInput{
		present: map[string]bool{"supportedToolsByProtocol": true, "inputModalities": true, "outputModalities": true},
		values: customProviderModelUpsertInput{
			SupportedToolsByProtocol: map[string][]string{"chat_completions": {"url_context"}},
			InputModalities:          []string{"file"},
			OutputModalities:         []string{"video"},
		},
	}
	replaced := mergedCustomModelUpsertInput(current, parsed)
	if !reflect.DeepEqual(replaced.SupportedToolsByProtocol, parsed.values.SupportedToolsByProtocol) {
		t.Fatalf("replaced matrix: %#v", replaced.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(replaced.InputModalities, parsed.values.InputModalities) ||
		!reflect.DeepEqual(replaced.OutputModalities, parsed.values.OutputModalities) {
		t.Fatalf("replaced modalities: %#v / %#v", replaced.InputModalities, replaced.OutputModalities)
	}
}

// 配置模板复制不携带三键（显式声明是行级所有权，不经模板通道继承）。
func TestT82TemplateCopyOmitsCapabilityDeclarations(t *testing.T) {
	template := &ModelCatalogItem{
		SupportedToolsByProtocol: map[string][]string{"chat_completions": {"function_calling"}},
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
	}
	input := customModelInputFromConfigurationTemplate(template)
	if input.SupportedToolsByProtocol != nil {
		t.Fatalf("template copy must not carry matrix: %#v", input.SupportedToolsByProtocol)
	}
	if input.InputModalities != nil || input.OutputModalities != nil {
		t.Fatalf("template copy must not carry modalities: %#v / %#v", input.InputModalities, input.OutputModalities)
	}
}

// --- 读侧 lenient 解析 ------------------------------------------------------

func TestT82LenientReadParsers(t *testing.T) {
	matrix := parseCapabilityTokenMatrix(sqlNullString(`{"chat_completions":["function_calling","b@d"],"bad!!key":[],"empty":[],"nested":{"y":1}}`))
	wantMatrix := map[string][]string{
		"chat_completions": {"function_calling"},
		"empty":            {},
	}
	if !reflect.DeepEqual(matrix, wantMatrix) {
		t.Fatalf("lenient matrix: %#v", matrix)
	}
	if got := parseCapabilityTokenMatrix(sqlNullString(`not-json`)); got == nil || len(got) != 0 {
		t.Fatalf("invalid JSON matrix: %#v", got)
	}
	if got := parseCapabilityTokenMatrix(sqlNullString(`[]`)); got == nil || len(got) != 0 {
		t.Fatalf("non-object matrix: %#v", got)
	}
	if got := parseCapabilityTokenMatrix(sqlNullString(``)); got == nil || len(got) != 0 {
		t.Fatalf("empty matrix: %#v", got)
	}

	modalities := parseCapabilityModalityArray(sqlNullString(`["text","image","b@d"]`))
	if !reflect.DeepEqual(modalities, []string{"text", "image"}) {
		t.Fatalf("lenient modalities: %#v", modalities)
	}
	if got := parseCapabilityModalityArray(sqlNullString(`"text"`)); got == nil || len(got) != 0 {
		t.Fatalf("non-array modalities: %#v", got)
	}
	if got := parseCapabilityModalityArray(sqlNullString(``)); got == nil || len(got) != 0 {
		t.Fatalf("empty modalities: %#v", got)
	}
}

func sqlNullString(value string) (null sql.NullString) {
	null.String = value
	null.Valid = true
	return null
}

// --- DB 往返（pgtestkit 授权门：JUHE_AI_TEST_EXTERNAL=1 才运行） ------------

// custom 行写入三键后经 findCustomProviderModelByID / ListProviderModelsForRequest
// 读回；capabilities 路由（listCustomModelRows modelFilter 臂）同 SELECT 可读。
func TestT82CapabilityDeclarationStoreRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	saved, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode:          "gpt",
		Model:                 "t82-capability-model",
		Scope:                 catalogScopeGlobal,
		Status:                "active",
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: map[string][]string{
			"chat_completions": {"function_calling", "web_search"},
			"responses":        {"structured_outputs"},
		},
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
		InputUsdPer1M:    ptrFloat64(1),
		OutputUsdPer1M:   ptrFloat64(2),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	record, err := store.findCustomProviderModelByID(ctx, saved.ID, "")
	if err != nil || record == nil {
		t.Fatalf("find by id: %v", err)
	}
	if !reflect.DeepEqual(record.SupportedToolsByProtocol, map[string][]string{
		"chat_completions": {"function_calling", "web_search"},
		"responses":        {"structured_outputs"},
	}) {
		t.Fatalf("record matrix: %#v", record.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(record.InputModalities, []string{"text", "image"}) ||
		!reflect.DeepEqual(record.OutputModalities, []string{"text"}) {
		t.Fatalf("record modalities: %#v / %#v", record.InputModalities, record.OutputModalities)
	}

	items, err := store.ListProviderModelsForRequest(ctx, "gpt", "", false, false)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var item *ModelCatalogItem
	for index := range items {
		if items[index].Model == "t82-capability-model" {
			item = &items[index]
			break
		}
	}
	if item == nil {
		t.Fatalf("custom row missing from catalog: %d items", len(items))
	}
	if !reflect.DeepEqual(item.SupportedToolsByProtocol, record.SupportedToolsByProtocol) {
		t.Fatalf("catalog matrix: %#v", item.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(item.InputModalities, []string{"text", "image"}) ||
		!reflect.DeepEqual(item.OutputModalities, []string{"text"}) {
		t.Fatalf("catalog modalities: %#v / %#v", item.InputModalities, item.OutputModalities)
	}

	// capabilities 路由臂（modelFilter SELECT）带新列可读。
	capability, err := store.FindProviderModelCapabilities(ctx, "gpt", "", "t82-capability-model")
	if err != nil || capability == nil {
		t.Fatalf("capabilities route: %v", err)
	}
}

// 显式矩阵覆盖同名内置行继承（不再继承）：custom 行显式声明原样透传。
func TestT82ExplicitDeclarationOverridesBuiltinInheritance(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	// gpt-4.1 在静态表携带非空工具矩阵（继承源的 sanity 前置）。
	expectedBuiltin := ResolveBuiltInStaticDerivedCapabilities("gpt", "gpt-4.1", nil, "")
	if len(expectedBuiltin.SupportedToolsByProtocol) == 0 {
		t.Fatalf("static table lost gpt-4.1 tools; pick another model for this test")
	}
	// 内置行以 disabled 落库让同名著 custom 行过写路径冲突门；继承查找不限可见
	// 性，includeInactive 读回时 builtin 行仍在继承源。
	env.seedBuiltinModel(t, builtinModelSeed{ID: "cat-t82-inherit", ProviderCode: "gpt", Model: "gpt-4.1",
		Status: "disabled", ReleaseDate: "2025-01-01", Protocols: `["chat_completions","responses"]`, InputUsd: ptrFloat64(1)})
	saved, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode:             "gpt",
		Model:                    "gpt-4.1",
		Scope:                    catalogScopeGlobal,
		Status:                   "active",
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: map[string][]string{"chat_completions": {"function_calling", "web_search"}},
		InputModalities:          []string{"text", "file"},
		OutputModalities:         []string{"text"},
		InputUsdPer1M:            ptrFloat64(3),
		OutputUsdPer1M:           ptrFloat64(4),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	items, err := store.ListProviderModelsForRequest(ctx, "gpt", "", true, true)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var item *ModelCatalogItem
	for index := range items {
		if items[index].Model == "gpt-4.1" {
			item = &items[index]
			break
		}
	}
	if item == nil {
		t.Fatalf("merged row missing: %d items", len(items))
	}
	if item.ID != saved.ID || item.Scope != catalogScopeGlobal {
		t.Fatalf("custom row must win merge: id=%s scope=%s", item.ID, item.Scope)
	}
	wantMatrix := map[string][]string{"chat_completions": {"function_calling", "web_search"}}
	if !reflect.DeepEqual(item.SupportedToolsByProtocol, wantMatrix) {
		t.Fatalf("explicit matrix must override inheritance: %#v", item.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(item.InputModalities, []string{"text", "file"}) ||
		!reflect.DeepEqual(item.OutputModalities, []string{"text"}) {
		t.Fatalf("explicit modalities must override inheritance: %#v / %#v", item.InputModalities, item.OutputModalities)
	}
}

// 空矩阵仍继承：custom 行未声明三键时沿 inheritCustomCatalogCapabilities 仅填
// 空语义拿内置静态兜底值。
func TestT82EmptyDeclarationStillInheritsBuiltin(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	// builtin 行以 disabled 落库；custom 行由 seedCustomModel 直插（三列取
	// DEFAULT 空矩阵/空数组=未声明）。
	env.seedBuiltinModel(t, builtinModelSeed{ID: "cat-t82-empty", ProviderCode: "gpt", Model: "gpt-4.1",
		Status: "disabled", ReleaseDate: "2025-01-01", Protocols: `["chat_completions","responses"]`, InputUsd: ptrFloat64(1)})
	env.seedCustomModel(t, customModelSeed{ID: "cu-t82-empty", ProviderCode: "gpt", Model: "gpt-4.1",
		Scope: "global", ReleaseDate: "2025-01-01", Protocols: `["chat_completions"]`, InputUsd: ptrFloat64(3)})

	expected := ResolveBuiltInStaticDerivedCapabilities("gpt", "gpt-4.1", nil, "")
	items, err := store.ListProviderModelsForRequest(ctx, "gpt", "", true, true)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var item *ModelCatalogItem
	for index := range items {
		if items[index].Model == "gpt-4.1" {
			item = &items[index]
			break
		}
	}
	if item == nil {
		t.Fatalf("merged row missing: %d items", len(items))
	}
	if item.Scope != catalogScopeGlobal {
		t.Fatalf("custom row must win merge: scope=%s", item.Scope)
	}
	if !reflect.DeepEqual(item.SupportedToolsByProtocol, expected.SupportedToolsByProtocol) {
		t.Fatalf("empty matrix must inherit builtin static tools: %#v, want %#v",
			item.SupportedToolsByProtocol, expected.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(item.InputModalities, expected.InputModalities) ||
		!reflect.DeepEqual(item.OutputModalities, expected.OutputModalities) {
		t.Fatalf("empty modalities must inherit builtin static modalities: %#v / %#v, want %#v / %#v",
			item.InputModalities, item.OutputModalities, expected.InputModalities, expected.OutputModalities)
	}
}

// PATCH 往返：提交键整值替换、缺键保持（DB 面）。
func TestT82PatchCapabilityDeclarationRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "t82-patch-model", Scope: catalogScopeGlobal,
		SupportedAPIProtocols: []string{"chat_completions"},
		InputUsdPer1M:         ptrFloat64(1), OutputUsdPer1M: ptrFloat64(2),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	outcome, err := store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID:     "",
		SupportedToolsByProtocol: map[string][]string{"chat_completions": {"mcp", "skills"}},
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
	}, []string{"supportedToolsByProtocol", "inputModalities", "outputModalities"}, current.UpdatedAt, "", nil)
	if err != nil || outcome.Kind != "updated" {
		t.Fatalf("patch: %v %+v", err, outcome)
	}
	patched, err := store.findCustomProviderModelByID(ctx, current.ID, "")
	if err != nil || patched == nil {
		t.Fatalf("find: %v", err)
	}
	if !reflect.DeepEqual(patched.SupportedToolsByProtocol, map[string][]string{"chat_completions": {"mcp", "skills"}}) {
		t.Fatalf("patched matrix: %#v", patched.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(patched.InputModalities, []string{"text", "image"}) {
		t.Fatalf("patched input modalities: %#v", patched.InputModalities)
	}
	// 缺键保持：只提交 inputModalities，矩阵列保持现值。
	outcome, err = store.patchCustomProviderModel(ctx, patched, customProviderModelUpsertInput{
		ActorSystemAccountID: "",
		InputModalities:      []string{"file"},
	}, []string{"inputModalities"}, patched.UpdatedAt, "", nil)
	if err != nil || outcome.Kind != "updated" {
		t.Fatalf("second patch: %v %+v", err, outcome)
	}
	kept, err := store.findCustomProviderModelByID(ctx, current.ID, "")
	if err != nil || kept == nil {
		t.Fatalf("find after second patch: %v", err)
	}
	if !reflect.DeepEqual(kept.SupportedToolsByProtocol, map[string][]string{"chat_completions": {"mcp", "skills"}}) {
		t.Fatalf("absent matrix key must keep value: %#v", kept.SupportedToolsByProtocol)
	}
	if !reflect.DeepEqual(kept.InputModalities, []string{"file"}) {
		t.Fatalf("replaced modalities: %#v", kept.InputModalities)
	}
}
