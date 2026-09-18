// w13g6_margin_test.go lifts the remaining statement coverage of the
// providers package past the 95% gate with a durable margin.
//
// 不可达语句归因登记（本文件登记，供覆盖率审计核对）：
//   - catalog.go:189 `!includeInactive && item.Status != "active"`：includeInactive=false 时
//     listBuiltInCatalogModels/listCustomModelRows 的 SQL 已带 status='active' 谓词，内存过滤恒为假（防御性双重过滤）。
//   - catalog.go:663 matchesModelToken 循环穷尽分支：position 每轮 +1，末轮 model[len:]="" 使 Index 返回 -1，
//     循环恒经 `found < 0` 退出，穷尽出口不可达。
//   - catalog.go:424/434 静态定价 CachedImageInputUsdPer1M / GenerationParameterCapabilities 注入臂：
//     internal/pricing 静态表当前无任何条目携带这两个字段（全包无字面量赋值）。
//   - catalog.go:999 选项合并排序的 provider-code 兜底：排序键是去重后的模型名，左右 Model 恒不同，
//     在 model 比较处提前返回，provider-code 比较不可达。
//   - custom_models.go:383/387/390 upsert 写失败/回读失败/saved==nil：upsert 先按 id 查重，写后回读同一行，
//     单测无法在不篡改驱动的情况下让 INSERT 失败或让刚写入的行消失。
//   - custom_models.go:433/446/479/487 RowsAffected/Commit 错误臂：modernc/sqlite 驱动不会对本地内存库
//     返回这两类错误，无注入点。
//   - custom_models.go:563/574 绑定第 3/4 段计数错误：mappingSource/mappingUpstream/union 查询同一
//     account_model_mappings 表，第 2 段失败后即返回，后两段无法独立失败。
//   - custom_models.go:588/593 countDistinctBoundAccounts 扫描/rows.Err 臂：外层恒为
//     COUNT(DISTINCT ...) 整数列，扫入 int64 不会失败。
//   - store.go:273/313 rows.Err 兜底、store.go:458/599/657/693 纯字符串列 Scan 错误臂：
//     SQLite 动态类型下 TEXT 扫入 *string 恒成功，无失败注入点。
//   - store.go:539 ListProviderOptions 扫描错误臂：查询带 WHERE enabled=1，非整数值在 SQL 比较中被排除，
//     通过筛选的行扫入 int 恒成功。
//   - catalog.go:842/892 modelOption 扫描错误臂：扫描目标全为 string/NullString，同理不可失败。
//   - write_handlers.go:101（conflict 需 UPDATE 0 行，但 expectedUpdatedAt 已在 67 行与库值核对）、
//     write_handlers.go:170（saved==nil 同理）、write_handlers.go:639（第二次 ListProviderModelsForRequest
//     与第一次同表，第一次成功则第二次必然成功）、write_handlers.go:54/505（非管理员的读谓词
//     scope='personal' AND owner=? 严格强于 canMutate 判定，403 臂不可达）。
//   - write_handlers.go:568（target==""）与 write_routes.go:609（owner==""）：
//     requestSystemAccountID 对认证主体恒返回非空 SystemAccountID（非管理员钉死调用者身份，
//     管理员回退自身身份），auth==nil 在 write 中间件之后不可达。
//   - write_routes.go:96 decodeModelBody re-marshal continue：json.Unmarshal 产物只有
//     map/slice/string/float64/bool/nil，重新 Marshal 恒成功。
//   - derived.go:75 capability() 未知参数兜底：全部调用点传入固定参数名，均在 generationParameterDefinitions 中。
package providers

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// w13g6DropTable 丢弃业务表（env 空闲时执行，事务外 DDL，单连接池安全）。
func w13g6DropTable(t *testing.T, env *testEnv, table string) {
	t.Helper()
	if _, err := env.db.Exec(`DROP TABLE ` + table); err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
}

// TestW13g6ClosedStoreErrorArms 覆盖 closed-DB 下各 Store 方法的错误臂。
func TestW13g6ClosedStoreErrorArms(t *testing.T) {
	store := w14dClosedStore(t, false)
	ctx := context.Background()

	if _, err := store.ListProviderModelsForRequest(ctx, "hybrid", "w13g6-u", false, false); err == nil {
		t.Fatal("hybrid models on closed DB must fail")
	}
	if _, err := store.ListProviderModelsForRequest(ctx, "gpt", "w13g6-u", false, false); err == nil {
		t.Fatal("provider models on closed DB must fail")
	}
	if _, err := store.ListProviderModelsForRequest(ctx, "openai", "w13g6-u", false, false); err == nil {
		t.Fatal("openai models on closed DB must fail")
	}
	if _, err := store.ModelCatalogSourceProviderCodes(ctx, "hybrid"); err == nil {
		t.Fatal("hybrid source codes on closed DB must fail")
	}
	if _, err := store.ModelCatalogSourceProviderCodes(ctx, "openai"); err == nil {
		t.Fatal("openai source codes on closed DB must fail")
	}
	if _, err := store.FindProviderModelCapabilities(ctx, "w13g6-ghost", "", "m"); err == nil {
		t.Fatal("capabilities on closed DB must fail")
	}
	if _, err := store.ListCatalogListItems(ctx); err == nil {
		t.Fatal("list items on closed DB must fail")
	}
	if _, err := store.ListProviderModelSelectionOptions(ctx, ModelOptionQuery{ProviderCode: "gpt"}); err == nil {
		t.Fatal("selection options on closed DB must fail")
	}
	if _, err := store.ProtocolProviderCodes(ctx, "openai", "v1"); err == nil {
		t.Fatal("protocol provider codes on closed DB must fail")
	}
	if err := store.UpsertSystemDefaultHealthCheckModel(ctx, "gpt", "m"); err == nil {
		t.Fatal("system default upsert on closed DB must fail")
	}
	if err := store.UpsertDefaultHealthCheckModelPreference(ctx, "w13g6-u", "gpt", "m"); err == nil {
		t.Fatal("preference upsert on closed DB must fail")
	}
	// 内置模型配置补丁：closed DB 下 BeginTx 失败。
	current := &ModelCatalogItem{ID: "w13g6-cat", ProviderCode: "gpt", Model: "m", Status: "active",
		UpdatedAt: "2026-01-01T00:00:00.000Z"}
	if _, err := store.patchBuiltInModelConfiguration(ctx, current, []builtinPatchField{
		{Name: "inputUsdPer1M", Value: 2.0},
	}, current.UpdatedAt, nil); err == nil {
		t.Fatal("builtin patch on closed DB must fail")
	}
	// pg 分支：table() 前缀。
	if got := (&Store{pg: true}).table("provider_model_catalog"); got != "juhe_business.provider_model_catalog" {
		t.Fatalf("pg table prefix: %s", got)
	}
}

// TestW13g6EmptyInputArms 覆盖空入参早退与纯函数分支。
func TestW13g6EmptyInputArms(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	ctx := context.Background()
	store := env.providersDeps.Store

	if got, err := store.ModelCatalogSourceProviderCodes(ctx, "   "); err != nil || len(got) != 0 {
		t.Fatalf("blank source codes: %v %v", got, err)
	}
	if got, err := store.ListProviderModelsForRequest(ctx, "  ", "", false, false); err != nil || len(got) != 0 {
		t.Fatalf("blank provider catalog: %v %v", got, err)
	}
	if got, err := store.FindProviderModelCapabilities(ctx, "gpt", "", "   "); err != nil || got != nil {
		t.Fatalf("blank model capabilities: %v %v", got, err)
	}
	if got, err := store.FindProviderModelCapabilities(ctx, "   ", "", "m"); err != nil || got != nil {
		t.Fatalf("blank provider capabilities: %v %v", got, err)
	}
	if got, err := store.FindDefinition(ctx, "  "); err != nil || got != nil {
		t.Fatalf("blank find definition: %v %v", got, err)
	}
	if got, err := store.FindProviderOption(ctx, "  "); err != nil || got != nil {
		t.Fatalf("blank find option: %v %v", got, err)
	}
	if got, err := store.ListDefaultHealthCheckModelPreferences(ctx, "  ", nil); err != nil || len(got) != 0 {
		t.Fatalf("blank preferences: %v %v", got, err)
	}
	if err := store.attachProtocolProfiles(ctx, nil); err != nil {
		t.Fatalf("empty attach: %v", err)
	}
	if got, err := store.listBuiltInCatalogModels(ctx, nil, false); err != nil || len(got) != 0 {
		t.Fatalf("empty builtin catalog: %v %v", got, err)
	}
	if got, err := store.findBuiltInTestCatalogItems(ctx, nil, "m"); err != nil || len(got) != 0 {
		t.Fatalf("empty test catalog: %v %v", got, err)
	}
	if got, err := store.listBuiltInModelOptions(ctx, nil, ModelOptionQuery{}); err != nil || len(got) != 0 {
		t.Fatalf("empty builtin options: %v %v", got, err)
	}
	if got, err := store.listCustomModelOptions(ctx, nil, ModelOptionQuery{}); err != nil || len(got) != 0 {
		t.Fatalf("empty custom options: %v %v", got, err)
	}
	// overlay 空列表早退。
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/list", nil)
	env.providersDeps.overlayListItems(request, []ProviderListItem{})
	env.providersDeps.overlayDefinitions(request, nil)

	// 纯函数分支。
	if optionScopePriority("personal") != 3 || optionScopePriority("global") != 2 || optionScopePriority("built_in") != 1 {
		t.Fatal("optionScopePriority arms")
	}
	date := func(value string) *string { return &value }
	if normalizedModelReleaseDate(nil) != "" ||
		normalizedModelReleaseDate(date("2024/01/01")) != "" ||
		normalizedModelReleaseDate(date("2024-1-1")) != "" ||
		normalizedModelReleaseDate(date("2024-01-0a")) != "" ||
		normalizedModelReleaseDate(date("2024-13-01")) != "" ||
		normalizedModelReleaseDate(date("2024-01-32")) != "" ||
		normalizedModelReleaseDate(date("2024-01-05T00:00:00Z")) != "2024-01-05" {
		t.Fatal("normalizedModelReleaseDate arms")
	}
	// compareProviderModelCatalogItems 的 catalogOrder 臂。
	order := func(v int64) *int64 { return &v }
	left := ModelCatalogItem{Model: "m", ReleaseDate: date("2024-01-01"), CatalogOrder: order(1)}
	right := ModelCatalogItem{Model: "m", ReleaseDate: date("2024-01-01"), CatalogOrder: order(2)}
	if compareProviderModelCatalogItems(left, right) >= 0 || compareProviderModelCatalogItems(right, left) <= 0 {
		t.Fatal("catalog order comparator arms")
	}
	// nullableIntegerJSON / mustJSON / parseCapabilityTokenArray。
	if nullableIntegerJSON(int64(-1)) != nil || nullableIntegerJSON(int64(7)) == nil ||
		nullableIntegerJSON("x") != nil {
		t.Fatal("nullableIntegerJSON int64/default arms")
	}
	if mustJSON(make(chan int)) != "null" {
		t.Fatal("mustJSON error arm")
	}
	if got := parseCapabilityTokenArray(sql.NullString{}); len(got) != 0 {
		t.Fatalf("null token array: %v", got)
	}
	// hasContentBeyondExpectedUpdatedAt 的真臂。
	parsed := &customModelParsedInput{status: "active"}
	if !parsed.hasContentBeyondExpectedUpdatedAt() {
		t.Fatal("status-only payload must carry content")
	}
	// cleanup 目标归一化的空码跳过臂。
	targets := normalizeCleanupTargets([]defaultReferenceCleanupTarget{
		{ProviderCode: "  "}, {ProviderCode: "gpt"},
	})
	if len(targets) != 1 || targets[0].ProviderCode != "gpt" {
		t.Fatalf("normalizeCleanupTargets blank skip: %+v", targets)
	}
	// 空模型早退。
	codes, err := clearUnavailableProviderModelDefaultReferences(ctx, nil, store, &defaultReferenceCleanupInput{Model: " "})
	if err != nil || len(codes) != 0 {
		t.Fatalf("blank cleanup model: %v %v", codes, err)
	}
	// pg 方言分支。
	pgSQL := availableModelExistsSQL(&Store{pg: true}, []string{"gpt"}, nil, "")
	if pgSQL.sql == "" || !strings.Contains(pgSQL.sql, "btrim") || !strings.Contains(pgSQL.sql, "TRUE") {
		t.Fatalf("pg availability sql: %s", pgSQL.sql)
	}
	// limitGenerationParameterMaxOutputTokens 的 min-violation 丢弃臂。
	caps := map[string][]generationParameterCapability{
		"chat_completions": {{Parameter: "maxOutputTokens", Min: 100, Max: 200, DefaultValue: 150}},
	}
	limit := int64(50)
	limited := limitGenerationParameterMaxOutputTokens(caps, &limit)
	if len(limited["chat_completions"]) != 0 {
		t.Fatalf("min-violating clamp must drop the entry: %+v", limited)
	}
}

// TestW13g6PatchAssignmentsMergeClosures 覆盖 patch 合并时剩余字段闭包。
func TestW13g6PatchAssignmentsMergeClosures(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "w13g6-closures", SystemAccountID: "w13g6-u1",
		DefaultReasoningEffort: stringPtr("low"), MaxInputTokens: ptrInt64(10), MaxOutputTokens: ptrInt64(10),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	next := customProviderModelUpsertInput{
		ActorSystemAccountID:   "w13g6-u1",
		DefaultReasoningEffort: stringPtr("high"),
		MaxInputTokens:         ptrInt64(20),
		MaxOutputTokens:        ptrInt64(20),
	}
	_, _, merged, err := store.customProviderModelPatchAssignments(current, next,
		[]string{"defaultReasoningEffort", "maxInputTokens", "maxOutputTokens"})
	if err != nil {
		t.Fatalf("assignments: %v", err)
	}
	// defaultReasoningEffort 的合并闭包是清空语义：capabilities 归一化恒产生 nil，
	// 与 current 的非空值不同即触发闭包并把合并结果置空。
	if merged.DefaultReasoningEffort != nil ||
		merged.MaxInputTokens == nil || *merged.MaxInputTokens != 20 ||
		merged.MaxOutputTokens == nil || *merged.MaxOutputTokens != 20 {
		t.Fatalf("merge closures: %+v", merged)
	}
}

// TestW13g6BrokenScanRows 覆盖类型不兼容列导致的扫描错误臂。
func TestW13g6BrokenScanRows(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('w13g6-broken', 'w13g6-broken', 'broken', 'abc', '[]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.login(t, "root", "root-pass", "super_admin")

	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/list", ""); code != http.StatusInternalServerError {
		t.Fatalf("broken list row: %d", code)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers?viewScope=admin", ""); code != http.StatusInternalServerError {
		t.Fatalf("broken definitions row: %d", code)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/w13g6-broken?viewScope=admin", ""); code != http.StatusInternalServerError {
		t.Fatalf("broken detail row: %d", code)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=w13g6-broken", ""); code != http.StatusInternalServerError {
		t.Fatalf("broken option lookup: %d", code)
	}
}

// TestW13g6BrokenProfileScan 覆盖 profile 扫描错误臂。
func TestW13g6BrokenProfileScan(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('w13g6-prof-broken', 'gpt', 'broken', 'abc', 'openai', 'v1', 'https://x', 'm', '[]', '[]',
		'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.login(t, "root", "root-pass", "super_admin")
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers?viewScope=admin", ""); code != http.StatusInternalServerError {
		t.Fatalf("broken profile scan: %d", code)
	}
}

// TestW13g6PreferredProfileAndTokenSkips 覆盖 glm 首选 profile 与空/hybrid 供应商 token 跳过臂。
func TestW13g6PreferredProfileAndTokenSkips(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	const now = "2026-01-01T00:00:00.000Z"
	// glm 首选 profile 分支。
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('w13g6-glm', 'glm', 'GLM', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_glm_coding_openai_v1', 'glm', 'GLM Coding', 1, 'openai', 'v1', 'https://glm.example/v1',
		'glm-4', '[]', '[]', ?, ?)`, now, now)
	// 空码供应商（归一化为空 token）与 hybrid 码供应商的 openai profile（hybrid provider 已由 seedCatalog 提供）。
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('w13g6-blank', '', 'blank', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('w13g6-prof-blank', '', 'blank profile', 1, 'openai', 'v1', 'https://b', 'm', '[]', '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('w13g6-prof-hybrid', 'hybrid', 'hybrid profile', 1, 'openai', 'v1', 'https://h', 'm', '[]', '[]', ?, ?)`, now, now)
	env.login(t, "root", "root-pass", "super_admin")

	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers?viewScope=admin", ""); code != http.StatusOK {
		t.Fatalf("preferred profile definitions: %d", code)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options", ""); code != http.StatusOK {
		t.Fatalf("protocol-less options: %d", code)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?protocol=openai", ""); code != http.StatusOK {
		t.Fatalf("openai protocol options with blank token: %d", code)
	}
	ctx := context.Background()
	if _, err := env.providersDeps.Store.ModelCatalogSourceProviderCodes(ctx, "hybrid"); err != nil {
		t.Fatalf("hybrid source expansion: %v", err)
	}
	if _, err := env.providersDeps.Store.ModelCatalogSourceProviderCodes(ctx, "openai"); err != nil {
		t.Fatalf("openai source expansion: %v", err)
	}
}

// TestW13g6CatalogScanArms 覆盖内置/自定义目录扫描错误与 defaultEffort 注入臂。
func TestW13g6CatalogScanArms(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	// 非数值价格列让内置目录行扫描失败。
	env.exec(t, `INSERT INTO provider_model_catalog (id, provider_code, model, status, supported_api_protocols_json,
		input_usd_per_1m, created_at, updated_at)
		VALUES ('w13g6-cat-broken', 'gpt', 'w13g6-broken-model', 'active', '[]', 'abc',
		'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.login(t, "root", "root-pass", "super_admin")
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models", ""); code != http.StatusInternalServerError {
		t.Fatalf("broken builtin catalog scan: %d", code)
	}

	// 自定义模型行：全局 gpt-4o 价格列坏行（按 scope 查找命中）。
	env.exec(t, `UPDATE custom_provider_models SET input_usd_per_1m = 'abc' WHERE id = 'cu-2'`)
	env.exec(t, `UPDATE custom_provider_models SET default_reasoning_effort = 'medium' WHERE id = 'cu-3'`)
	ctx := context.Background()
	store := env.providersDeps.Store
	if _, err := store.listCustomModelRows(ctx, []string{"gpt"}, "", true, ""); err == nil {
		t.Fatal("broken custom catalog scan must fail")
	}
	if _, err := store.findCustomProviderModelByID(ctx, "cu-2", ""); err == nil {
		t.Fatal("broken custom find by id must fail")
	}
	if _, err := store.findCustomProviderModelByScope(ctx, "gpt", "global", "", "gpt-4o"); err == nil {
		t.Fatal("broken custom find by scope must fail")
	}
	// 正常行走通 defaultEffort 注入臂。
	items, err := store.listCustomModelRows(ctx, []string{"gpt"}, "", true, "gpt-whisper-box")
	if err != nil {
		t.Fatalf("custom rows with default effort: %v", err)
	}
	if len(items) == 0 || items[0].DefaultReasoningEffort == nil || *items[0].DefaultReasoningEffort != "medium" {
		t.Fatalf("default effort projection: %+v", items)
	}
}

// TestW13g6OverlayErrorFallbacks 覆盖偏好/系统默认查询失败的 overlay 兜底臂。
func TestW13g6OverlayErrorFallbacks(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	w13g6DropTable(t, env, "provider_default_health_check_models")
	w13g6DropTable(t, env, "provider_system_default_health_check_models")
	env.login(t, "root", "root-pass", "super_admin")

	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers/list?viewScope=admin", ""); code != http.StatusOK {
		t.Fatalf("list with dropped preference tables: %d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers?viewScope=admin", ""); code != http.StatusOK {
		t.Fatalf("definitions with dropped preference tables: %d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers/definitions", ""); code != http.StatusOK {
		t.Fatalf("enabled definitions with dropped preference tables: %d %v", code, payload)
	}
}

// TestW13g6AttachProfileErrorArms 覆盖 profile/family 查询失败的传播臂。
func TestW13g6AttachProfileErrorArms(t *testing.T) {
	t.Run("families-dropped", func(t *testing.T) {
		env := newTestEnv(t)
		env.seedCatalog(t)
		w13g6DropTable(t, env, "provider_protocol_profile_families")
		env.login(t, "root", "root-pass", "super_admin")
		if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers?viewScope=admin", ""); code != http.StatusInternalServerError {
			t.Fatalf("dropped families table: %d", code)
		}
		if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt?viewScope=admin", ""); code != http.StatusInternalServerError {
			t.Fatalf("dropped families table on detail: %d", code)
		}
	})
	t.Run("profiles-dropped", func(t *testing.T) {
		env := newTestEnv(t)
		env.seedCatalog(t)
		w13g6DropTable(t, env, "provider_protocol_profiles")
		env.login(t, "root", "root-pass", "super_admin")
		if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers?viewScope=admin", ""); code != http.StatusInternalServerError {
			t.Fatalf("dropped profiles table: %d", code)
		}
		// cleanup targets 的逐码 source 展开失败臂（providers 在、profiles 表缺）。
		if _, err := env.providersDeps.Store.defaultReferenceCleanupTargets(context.Background(), "hybrid", "", "m", true); err == nil {
			t.Fatal("cleanup targets source expansion must fail")
		}
	})
	t.Run("providers-dropped", func(t *testing.T) {
		env := newTestEnv(t)
		env.seedCatalog(t)
		w13g6DropTable(t, env, "providers")
		env.login(t, "root", "root-pass", "super_admin")
		if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers?viewScope=admin", ""); code != http.StatusInternalServerError {
			t.Fatalf("dropped providers table on definitions: %d", code)
		}
		if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt", ""); code != http.StatusInternalServerError {
			t.Fatalf("dropped providers table on option lookup: %d", code)
		}
		if _, err := env.providersDeps.Store.defaultReferenceCleanupTargets(context.Background(), "hybrid", "", "m", true); err == nil {
			t.Fatal("cleanup targets on dropped providers must fail")
		}
	})
}

// TestW13g6SelectionOptionQueryErrors 覆盖模型选项自定义查询错误臂与默认检查模型校验错误臂。
func TestW13g6SelectionOptionQueryErrors(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	w13g6DropTable(t, env, "custom_provider_models")
	env.login(t, "root", "root-pass", "super_admin")
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt", ""); code != http.StatusInternalServerError {
		t.Fatalf("dropped custom table on options: %d", code)
	}
	// 默认检查模型校验：目录展开中途失败 -> 500。
	if code, _ := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?viewScope=admin",
		`{"model":"gpt-4o"}`); code != http.StatusInternalServerError {
		t.Fatalf("validate with dropped custom table: %d", code)
	}
}

// TestW13g6PatchCustomModelErrorArms 覆盖自定义 PATCH 的错误传播臂。
func TestW13g6PatchCustomModelErrorArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "w13g6-patch-target", SystemAccountID: "w13g6-u1", InputUsdPer1M: ptrFloat64(1),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	env.login(t, "root", "root-pass", "super_admin")

	// patch 事务内 cleanup 失败：偏好表缺失 -> WriteBadRequest(err.Error())。
	w13g6DropTable(t, env, "provider_default_health_check_models")
	if code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/"+current.ID,
		`{"expectedUpdatedAt":"`+current.UpdatedAt+`","status":"disabled"}`); code != http.StatusBadRequest {
		t.Fatalf("patch tx cleanup error must surface as 400: %d", code)
	}

	// cleanup targets 失败：providers 表缺失。
	w13g6DropTable(t, env, "providers")
	if code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/"+current.ID,
		`{"expectedUpdatedAt":"`+current.UpdatedAt+`","status":"disabled"}`); code != http.StatusInternalServerError {
		t.Fatalf("patch cleanup targets error: %d", code)
	}

	// 自定义模型表缺失：findCustomProviderModelByID 错误臂。
	w13g6DropTable(t, env, "custom_provider_models")
	if code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_w13g6-nope",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","status":"disabled"}`); code != http.StatusInternalServerError {
		t.Fatalf("patch on dropped custom table: %d", code)
	}
}

// TestW13g6PatchStoreTxErrorArms 覆盖 patch/delete 事务内 UPDATE/DELETE 错误臂。
func TestW13g6PatchStoreTxErrorArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current := &customProviderModelRecord{ID: "w13g6-tx", ProviderCode: "gpt", Model: "m", Status: "active",
		UpdatedAt: "2026-01-01T00:00:00.000Z"}
	w13g6DropTable(t, env, "custom_provider_models")
	if _, err := store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "w13g6-u1", Status: "disabled",
	}, []string{"status"}, current.UpdatedAt, "", nil); err == nil {
		t.Fatal("patch tx on dropped table must fail")
	}
	if _, err := store.deleteCustomProviderModel(ctx, "w13g6-tx", "", nil); err == nil {
		t.Fatal("delete tx on dropped table must fail")
	}
}

// TestW13g6BuiltinPatchErrorArms 覆盖内置 PATCH 的 cleanup / 配置写错误臂。
func TestW13g6BuiltinPatchErrorArms(t *testing.T) {
	t.Run("cleanup-targets-error", func(t *testing.T) {
		env := newTestEnv(t)
		env.seedCatalog(t)
		w13g6DropTable(t, env, "providers")
		env.login(t, "root", "root-pass", "super_admin")
		if code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
			`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-1")+`","catalogVisible":false}`); code != http.StatusInternalServerError {
			t.Fatalf("builtin patch cleanup targets error: %d", code)
		}
	})
	t.Run("tx-cleanup-error", func(t *testing.T) {
		env := newTestEnv(t)
		env.seedCatalog(t)
		w13g6DropTable(t, env, "provider_system_default_health_check_models")
		env.login(t, "root", "root-pass", "super_admin")
		if code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-2",
			`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-2")+`","catalogVisible":false}`); code != http.StatusInternalServerError {
			t.Fatalf("builtin patch tx cleanup error: %d", code)
		}
	})
	t.Run("store-noop-and-stale", func(t *testing.T) {
		env := newTestEnv(t)
		env.seedCatalog(t)
		ctx := context.Background()
		store := env.providersDeps.Store
		current, err := store.findBuiltInModelPatchState(ctx, "cat-1")
		if err != nil || current == nil {
			t.Fatalf("patch state: %v %v", current, err)
		}
		record, err := store.patchBuiltInModelConfiguration(ctx, current, nil, current.UpdatedAt, nil)
		if err != nil || record == nil || record.ID != "cat-1" {
			t.Fatalf("empty patch no-op: %v %v", record, err)
		}
		if saved, err := store.patchBuiltInModelConfiguration(ctx, current, []builtinPatchField{
			{Name: "inputUsdPer1M", Value: 9.0},
		}, "2020-01-01T00:00:00.000Z", nil); err != nil || saved != nil {
			t.Fatalf("stale builtin patch must return nil: %v %v", saved, err)
		}
	})
}

// TestW13g6DeleteBindingErrorArm 覆盖 DELETE 的 bindings 错误臂（accounts 表缺失）。
func TestW13g6DeleteBindingErrorArm(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	created, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "w13g6-del-bindings", SystemAccountID: "w13g6-u1", InputUsdPer1M: ptrFloat64(1),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	env.login(t, "root", "root-pass", "super_admin")
	w13g6DropTable(t, env, "accounts")
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+created.ID, ""); code != http.StatusInternalServerError {
		t.Fatalf("delete bindings error: %d", code)
	}
}

// TestW13g6DeleteCleanupTargetErrorArm 覆盖 DELETE 的 cleanup targets 错误臂。
func TestW13g6DeleteCleanupTargetErrorArm(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	created, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "w13g6-del-targets", SystemAccountID: "w13g6-u1", InputUsdPer1M: ptrFloat64(1),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	env.login(t, "root", "root-pass", "super_admin")
	w13g6DropTable(t, env, "providers")
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+created.ID, ""); code != http.StatusInternalServerError {
		t.Fatalf("delete cleanup targets error: %d", code)
	}
}

// TestW13g6DeleteTxCleanupErrorArm 覆盖 DELETE 事务内 cleanup 失败臂。
func TestW13g6DeleteTxCleanupErrorArm(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	created, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "w13g6-del-tx", SystemAccountID: "w13g6-u1", InputUsdPer1M: ptrFloat64(1),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	env.login(t, "root", "root-pass", "super_admin")
	w13g6DropTable(t, env, "provider_default_health_check_models")
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+created.ID, ""); code != http.StatusInternalServerError {
		t.Fatalf("delete tx cleanup error: %d", code)
	}
}

// TestW13g6BindingCountErrorArm 覆盖绑定计数的第二段错误臂。
func TestW13g6BindingCountErrorArm(t *testing.T) {
	env := newTestEnv(t)
	w13g6DropTable(t, env, "account_model_mappings")
	if _, err := env.providersDeps.Store.customProviderModelBindings(context.Background(),
		"gpt", "w13g6-m", "global", ""); err == nil {
		t.Fatal("mapping count on dropped table must fail")
	}
}

// TestW13g6PutDefaultHealthCheckArms 覆盖 PUT 默认检查模型的写入错误臂。
func TestW13g6PutDefaultHealthCheckArms(t *testing.T) {
	t.Run("system-default-error", func(t *testing.T) {
		// 管理面写系统默认失败：系统默认表缺失。
		env := newTestEnv(t)
		env.seedCatalog(t)
		w13g6DropTable(t, env, "provider_system_default_health_check_models")
		env.login(t, "root", "root-pass", "super_admin")
		if code, _ := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?viewScope=admin",
			`{"model":"gpt-4o"}`); code != http.StatusInternalServerError {
			t.Fatalf("system default upsert error: %d", code)
		}
	})
	t.Run("preference-error", func(t *testing.T) {
		// 个人偏好写失败：偏好表缺失。
		env := newTestEnv(t)
		env.seedCatalog(t)
		w13g6DropTable(t, env, "provider_default_health_check_models")
		env.login(t, "user1", "user-pass", "user")
		if code, _ := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model",
			`{"model":"gpt-4o"}`); code != http.StatusInternalServerError {
			t.Fatalf("preference upsert error: %d", code)
		}
	})
}

// TestW13g6BlankPreferenceRows 覆盖偏好/系统默认行的空码跳过臂。
func TestW13g6BlankPreferenceRows(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	user1 := env.requireAccount(t, "user1", "user-pass", "user")
	env.exec(t, `INSERT INTO provider_default_health_check_models (system_account_id, provider_code, model, created_at, updated_at)
		VALUES (?, '', 'm', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, user1)
	env.exec(t, `INSERT INTO provider_system_default_health_check_models (provider_code, model, created_at, updated_at)
		VALUES ('', 'm', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	ctx := context.Background()
	store := env.providersDeps.Store
	preferences, err := store.ListDefaultHealthCheckModelPreferences(ctx, user1, nil)
	if err != nil {
		t.Fatalf("preferences: %v", err)
	}
	if _, exists := preferences[""]; exists {
		t.Fatalf("blank provider code must be skipped: %v", preferences)
	}
	systemDefaults, err := store.ListSystemDefaultHealthCheckModels(ctx, nil)
	if err != nil {
		t.Fatalf("system defaults: %v", err)
	}
	if _, exists := systemDefaults[""]; exists {
		t.Fatalf("blank system default code must be skipped: %v", systemDefaults)
	}
}
