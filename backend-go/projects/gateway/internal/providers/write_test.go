// write_test.go pins the providers model-catalog write family contracts
// byte-for-byte against the Node routes (providers.routes.ts:184-606):
// create/edit/delete custom models, the built-in configuration patch and the
// default-health-check-model preference, including auth posture (session
// level with per-route admin forks), validation messages, optimistic
// concurrency (409), the AI-account binding guard, the default-reference
// cleanup and the configuration-template inheritance.
package providers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// seedWriteFixtures adds rows shared by the write tests: an image-mode
// global custom model (usable-for-test negative), an expired custom model
// (shutdown filter), a second user and the gpt-4o-personal preference seed.
func (env *testEnv) seedWriteFixtures(t *testing.T) (user1, user2 string) {
	t.Helper()
	user1 = env.requireAccount(t, "user1", "user-pass", "user")
	user2 = env.requireAccount(t, "user2", "user-pass", "user")
	const now = "2026-01-01T00:00:00.000Z"
	env.seedCustomModel(t, customModelSeed{ID: "cu-img", ProviderCode: "gpt", Model: "gpt-image-studio",
		Scope: "global", Protocols: `["images"]`})
	env.exec(t, `UPDATE custom_provider_models SET mode = 'image', image_input_usd_per_1m = 8 WHERE id = 'cu-img'`)
	env.seedCustomModel(t, customModelSeed{ID: "cu-expired", ProviderCode: "gpt", Model: "gpt-custom-expired",
		Scope: "global", ReleaseDate: "2024-01-01", Protocols: `["chat_completions"]`, InputUsd: ptrFloat64(1)})
	env.exec(t, `UPDATE custom_provider_models SET shutdown_date = '2020-01-01' WHERE id = 'cu-expired'`)
	_ = now
	return user1, user2
}

// updatedAtOf reads a custom model row's updated_at for the concurrency tests.
func (env *testEnv) updatedAtOf(t *testing.T, id string) string {
	t.Helper()
	var updatedAt string
	if err := env.db.QueryRow(`SELECT updated_at FROM custom_provider_models WHERE id = ?`, id).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt
}

func (env *testEnv) builtinUpdatedAtOf(t *testing.T, id string) string {
	t.Helper()
	var updatedAt string
	if err := env.db.QueryRow(`SELECT updated_at FROM provider_model_catalog WHERE id = ?`, id).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt
}

func (env *testEnv) preferenceCount(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := env.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestProvidersCreateCustomModel covers POST /{code}/models: the 201 save
// response shape (no catalogDisplay), the merged-catalog pickup, scope
// ownership and the duplicate-model upsert semantics.
func TestProvidersCreateCustomModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	user1, _ := env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")

	body := `{"model":"my-model","supportedApiProtocols":["chat_completions"],"contextWindowTokens":32000,
		"inputUsdPer1M":1.5,"outputUsdPer1M":2,"supportedServiceTiers":["priority"],
		"serviceTierPrices":{"priority":{"inputUsdPer1M":2.5}}}`
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", body)
	if code != http.StatusCreated {
		t.Fatalf("create custom model: %d %v", code, created)
	}
	data := dataMap(t, created)
	if !strings.HasPrefix(data["id"].(string), "custom_model_") {
		t.Fatalf("custom id prefix: %v", data)
	}
	if data["providerCode"] != "gpt" || data["model"] != "my-model" || data["scope"] != "personal" ||
		data["status"] != "active" || data["source"] != "custom-personal" || data["systemAccountId"] != user1 {
		t.Fatalf("created row identity: %v", data)
	}
	if data["inputUsdPer1M"] != float64(1.5) || data["defaultReasoningEffort"] != nil {
		t.Fatalf("created row prices: %v", data)
	}
	if _, exists := data["catalogDisplay"]; exists {
		t.Fatalf("save response carries no catalogDisplay (toCustomCatalogItem): %v", data)
	}
	if _, exists := data["catalogVisible"]; exists {
		t.Fatalf("custom rows carry no catalogVisible: %v", data)
	}
	caps := data["generationParameterCapabilities"].(map[string]any)
	chat := caps["chat_completions"].([]any)
	responses := caps["responses"].([]any)
	if len(chat) != 6 || len(responses) != 3 {
		t.Fatalf("gpt generation capabilities: %v", caps)
	}
	tiers := data["serviceTierPrices"].(map[string]any)
	if len(tiers) != 1 || tiers["priority"].(map[string]any)["inputUsdPer1M"] != float64(2.5) {
		t.Fatalf("tier prices: %v", tiers)
	}

	// The merged catalog picks the row up with catalogDisplay present.
	code, models := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models", "")
	if code != http.StatusOK {
		t.Fatalf("models after create: %d %v", code, models)
	}
	var createdRow map[string]any
	for _, row := range dataArray(t, models) {
		entry := row.(map[string]any)
		if entry["model"] == "my-model" {
			createdRow = entry
		}
	}
	if createdRow == nil {
		t.Fatalf("created model missing from catalog: %v", models)
	}
	if _, exists := createdRow["catalogDisplay"]; !exists {
		t.Fatalf("catalog rows must carry catalogDisplay: %v", createdRow)
	}

	// Duplicate POST: same scope+model upserts onto the same row.
	code, again := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `{"model":"my-model","inputUsdPer1M":3}`)
	if code != http.StatusCreated {
		t.Fatalf("duplicate create: %d %v", code, again)
	}
	if dataMap(t, again)["id"] != data["id"] || dataMap(t, again)["inputUsdPer1M"] != float64(3) {
		t.Fatalf("duplicate create must upsert the scoped row: %v", again)
	}

	// Anonymous callers stay 401.
	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `{"model":"x"}`)
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous create: %d %v", code, anonymous)
	}
}

// TestProvidersCreateCustomModelValidation covers the schema and pricing
// validation forks of POST /{code}/models.
func TestProvidersCreateCustomModelValidation(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.login(t, "user1", "user-pass", "user")

	cases := []struct {
		name    string
		login   string
		body    string
		status  int
		message string
	}{
		{"unknown key", "user1", `{"model":"a","bogus":1}`, 400, "自定义模型参数无效"},
		{"missing model", "user1", `{"inputUsdPer1M":1}`, 400, "自定义模型参数无效"},
		{"empty model", "user1", `{"model":"  "}`, 400, "自定义模型参数无效"},
		{"bad protocol", "user1", `{"model":"a","supportedApiProtocols":["nope"]}`, 400, "自定义模型参数无效"},
		{"bad tier token", "user1", `{"model":"a","supportedServiceTiers":["!"]}`, 400, "自定义模型参数无效"},
		{"active without price", "user1", `{"model":"a","status":"active"}`, 400, "启用的自定义模型必须配置完整当前价格"},
		{"gpt foreign tier", "user1", `{"model":"a","inputUsdPer1M":1,"supportedServiceTiers":["fast"]}`, 400, "自定义模型参数无效"},
		{"image with tiers", "root", `{"model":"a","mode":"image","imageInputUsdPer1M":1,"supportedServiceTiers":["priority"]}`, 400, "只有文本自定义模型支持服务等级和思考能力配置"},
		{"tier price not supported", "user1", `{"model":"a","inputUsdPer1M":1,"serviceTierPrices":{"flex":{"inputUsdPer1M":1}}}`, 400, "服务档位价格必须属于模型支持的服务等级"},
		{"strict tier prices", "user1", `{"model":"a","inputUsdPer1M":1,"serviceTierPrices":{"p":{"nope":1}}}`, 400, "自定义模型参数无效"},
		{"global as user", "user1", `{"model":"a","scope":"global","inputUsdPer1M":1}`, 403, "只有管理员可以创建全局模型"},
		// An admin without ?systemAccountId targets their own account, so the
		// personal-model owner fork stays satisfied and pricing validation
		// answers first (Node providerModelRequestSystemAccountId fallback).
		{"admin personal without account", "root", `{"model":"a"}`, 400, "启用的自定义模型必须配置完整当前价格"},
	}
	for _, testCase := range cases {
		password := map[string]string{"root": "root-pass", "user1": "user-pass"}[testCase.login]
		env.login(t, testCase.login, password, map[string]string{"root": "super_admin", "user1": "user"}[testCase.login])
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", testCase.body)
		if code != testCase.status || payload["message"] != testCase.message {
			t.Fatalf("%s: %d %v (want %d %s)", testCase.name, code, payload, testCase.status, testCase.message)
		}
	}

	// Unknown provider.
	env.login(t, "root", "root-pass", "super_admin")
	code, unknownProvider := env.do(t, http.MethodPost, "/__aisys__/api/providers/nope/models", `{"model":"a","inputUsdPer1M":1}`)
	if code != http.StatusNotFound || unknownProvider["message"] != "供应商不存在" {
		t.Fatalf("unknown provider: %d %v", code, unknownProvider)
	}

	// Draft status skips the price completeness rule.
	env.login(t, "user1", "user-pass", "user")
	code, draft := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `{"model":"draft-model","status":"draft"}`)
	if code != http.StatusCreated || dataMap(t, draft)["status"] != "draft" {
		t.Fatalf("draft create: %d %v", code, draft)
	}

	// Admin global create works and carries no systemAccountId.
	env.login(t, "root", "root-pass", "super_admin")
	code, global := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `{"model":"global-model","scope":"global","inputUsdPer1M":1}`)
	if code != http.StatusCreated {
		t.Fatalf("global create: %d %v", code, global)
	}
	globalData := dataMap(t, global)
	if globalData["scope"] != "global" || globalData["source"] != "custom-global" {
		t.Fatalf("global create row: %v", globalData)
	}
	if _, exists := globalData["systemAccountId"]; exists {
		t.Fatalf("global rows carry no systemAccountId: %v", globalData)
	}
}

// TestProvidersCreateCustomModelTemplate covers the configuration-template
// inheritance and its 400 fork.
func TestProvidersCreateCustomModelTemplate(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"from-template","configurationTemplateId":"cat-2"}`)
	if code != http.StatusCreated {
		t.Fatalf("template create: %d %v", code, created)
	}
	data := dataMap(t, created)
	if data["inputUsdPer1M"] != float64(0) || data["contextWindowTokens"] != float64(128000) ||
		data["releaseDate"] != "2024-07-18" || data["defaultReasoningEffort"] != nil {
		t.Fatalf("inherited prices: %v", data)
	}
	if protocols := data["supportedApiProtocols"].([]any); len(protocols) != 2 || protocols[1] != "responses" {
		t.Fatalf("inherited protocols: %v", data)
	}

	// A disabled template is unusable.
	code, disabled := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"from-disabled","configurationTemplateId":"cat-3"}`)
	if code != http.StatusBadRequest || disabled["message"] != "配置模板不可用" {
		t.Fatalf("disabled template: %d %v", code, disabled)
	}
	// Unknown template id.
	code, unknown := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"from-nowhere","configurationTemplateId":"cat-nope"}`)
	if code != http.StatusBadRequest || unknown["message"] != "配置模板不可用" {
		t.Fatalf("unknown template: %d %v", code, unknown)
	}
}

// TestProvidersPatchCustomModel covers the custom PATCH forks: ownership,
// optimistic concurrency, the empty-patch refine and the pricing validation.
func TestProvidersPatchCustomModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.requireAccount(t, "user2", "user-pass", "user")
	env.login(t, "user1", "user-pass", "user")

	updatedAt := env.updatedAtOf(t, "cu-1")
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+updatedAt+`","outputUsdPer1M":4,"capabilityNotes":"changed"}`)
	if code != http.StatusOK {
		t.Fatalf("patch own model: %d %v", code, patched)
	}
	result := dataMap(t, patched)
	if result["id"] != "cu-1" || result["status"] != "active" || result["updatedAt"] == updatedAt {
		t.Fatalf("patch result: %v (previous %s)", result, updatedAt)
	}
	if _, exists := result["defaultHealthCheckModelCleared"]; exists {
		t.Fatalf("no cleanup expected: %v", result)
	}
	if env.updatedAtOf(t, "cu-1") == updatedAt {
		t.Fatalf("updated_at did not advance")
	}

	// Stale expectedUpdatedAt -> 409.
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+updatedAt+`","outputUsdPer1M":5}`)
	if code != http.StatusConflict || conflict["message"] != "模型已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale patch: %d %v", code, conflict)
	}

	// Missing fields -> the fixed parse message (the route never surfaces the
	// zod refine text).
	current := env.updatedAtOf(t, "cu-1")
	code, empty := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+current+`"}`)
	if code != http.StatusBadRequest || empty["message"] != "自定义模型参数无效" {
		t.Fatalf("empty patch: %d %v", code, empty)
	}

	// Another user cannot see (404) or mutate someone else's personal model.
	env.login(t, "user2", "user-pass", "user")
	code, missing := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+current+`","outputUsdPer1M":9}`)
	if code != http.StatusNotFound || missing["message"] != "自定义模型不存在" {
		t.Fatalf("other user patch: %d %v", code, missing)
	}
	// A global custom model is invisible to non-admins (the owner predicate
	// yields the same 404 as Node's patch-state lookup).
	code, forbidden := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-2",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-2")+`","outputUsdPer1M":9}`)
	if code != http.StatusNotFound || forbidden["message"] != "自定义模型不存在" {
		t.Fatalf("global patch as user: %d %v", code, forbidden)
	}

	// Wrong provider code -> 404.
	env.login(t, "user1", "user-pass", "user")
	code, wrongProvider := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gemini/models/cu-1",
		`{"expectedUpdatedAt":"`+current+`","outputUsdPer1M":9}`)
	if code != http.StatusNotFound || wrongProvider["message"] != "自定义模型不存在" {
		t.Fatalf("wrong provider patch: %d %v", code, wrongProvider)
	}

	// Clearing every price while staying active fails the completeness rule.
	code, unpriced := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-1")+`","inputUsdPer1M":null,"outputUsdPer1M":null}`)
	if code != http.StatusBadRequest || unpriced["message"] != "启用的自定义模型必须配置完整当前价格" {
		t.Fatalf("unpriced patch: %d %v", code, unpriced)
	}

	// Admin patches the personal model without the owner predicate.
	env.login(t, "root", "root-pass", "super_admin")
	code, adminPatched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-1")+`","notes":"admin note"}`)
	if code != http.StatusOK || dataMap(t, adminPatched)["id"] != "cu-1" {
		t.Fatalf("admin patch: %d %v", code, adminPatched)
	}

	// custom_model_-prefixed unknown ids stay on the custom path.
	code, unknown := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_missing",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","notes":"x"}`)
	if code != http.StatusNotFound || unknown["message"] != "自定义模型不存在" {
		t.Fatalf("unknown custom id: %d %v", code, unknown)
	}
}

// TestProvidersPatchCustomModelDefaultCleanup covers the
// default-health-check-model reference cleanup on patch transitions.
func TestProvidersPatchCustomModelDefaultCleanup(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	user1 := env.requireAccount(t, "user1", "user-pass", "user")
	const now = "2026-01-01T00:00:00.000Z"
	// seedCatalog already wrote user1's gpt preference; replace it with the
	// row under test.
	env.exec(t, `INSERT INTO provider_default_health_check_models (system_account_id, provider_code, model, created_at, updated_at)
		VALUES (?, 'gpt', 'gpt-4o-personal', ?, ?)
		ON CONFLICT(system_account_id, provider_code) DO UPDATE SET model = excluded.model, updated_at = excluded.updated_at`, user1, now, now)
	env.exec(t, `INSERT INTO provider_system_default_health_check_models (provider_code, model, created_at, updated_at)
		VALUES ('gpt', 'gpt-4o-personal', ?, ?)`, now, now)

	env.login(t, "user1", "user-pass", "user")
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-1")+`","status":"disabled"}`)
	if code != http.StatusOK {
		t.Fatalf("disable own model: %d %v", code, patched)
	}
	if dataMap(t, patched)["defaultHealthCheckModelCleared"] != true {
		t.Fatalf("cleared flag missing: %v", patched)
	}
	if got := env.preferenceCount(t, `SELECT COUNT(*) FROM provider_default_health_check_models WHERE system_account_id = ?`, user1); got != 0 {
		t.Fatalf("personal preference must be cleared: %d", got)
	}
	// Personal transitions never clear the system default.
	if got := env.preferenceCount(t, `SELECT COUNT(*) FROM provider_system_default_health_check_models WHERE provider_code = 'gpt' AND model = 'gpt-4o-personal'`); got != 1 {
		t.Fatalf("system default must survive a personal patch: %d", got)
	}

	// A transition shadowed by the built-in gpt-4o cleans nothing.
	env.login(t, "root", "root-pass", "super_admin")
	env.exec(t, `INSERT INTO provider_system_default_health_check_models (provider_code, model, created_at, updated_at)
		VALUES ('gpt', 'gpt-4o', ?, ?)
		ON CONFLICT(provider_code) DO UPDATE SET model = excluded.model, updated_at = excluded.updated_at`, now, now)
	code, global := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-2",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-2")+`","status":"disabled"}`)
	if code != http.StatusOK {
		t.Fatalf("disable global model: %d %v", code, global)
	}
	if _, exists := dataMap(t, global)["defaultHealthCheckModelCleared"]; exists {
		t.Fatalf("built-in shadow must keep the reference: %v", global)
	}
	if got := env.preferenceCount(t, `SELECT COUNT(*) FROM provider_system_default_health_check_models WHERE provider_code = 'gpt' AND model = 'gpt-4o'`); got != 1 {
		t.Fatalf("shadowed system default must survive: %d", got)
	}
}

// TestProvidersPatchBuiltInModel covers the built-in fork: the admin gate,
// the source transition, completeness validation and the operation log.
func TestProvidersPatchBuiltInModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")
	logs := &captureSink{}
	env.providersDeps.Sink = logs

	updatedAt := env.builtinUpdatedAtOf(t, "cat-1")
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+updatedAt+`","catalogVisible":false}`)
	if code != http.StatusOK {
		t.Fatalf("hide built-in: %d %v", code, patched)
	}
	result := dataMap(t, patched)
	if result["id"] != "cat-1" || result["status"] != "active" || result["updatedAt"] == updatedAt {
		t.Fatalf("built-in patch result: %v", result)
	}
	var source string
	if err := env.db.QueryRow(`SELECT source FROM provider_model_catalog WHERE id = 'cat-1'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "manual-visibility-override" {
		t.Fatalf("visibility-only patch source: %s", source)
	}
	if len(logs.entries) != 1 || logs.entries[0].Action != "update_model_configuration" ||
		logs.entries[0].Module != "providers" || logs.entries[0].VisibilityScope != "admin_only" {
		t.Fatalf("operation log: %v", logs.entries)
	}

	// A configuration field flips the source to manual-override.
	code, repriced := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-1")+`","inputUsdPer1M":7.5}`)
	if code != http.StatusOK {
		t.Fatalf("reprice built-in: %d %v", code, repriced)
	}
	if err := env.db.QueryRow(`SELECT source FROM provider_model_catalog WHERE id = 'cat-1'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "manual-override" {
		t.Fatalf("configuration patch source: %s", source)
	}

	// No-op patches answer with the unchanged record.
	current := env.builtinUpdatedAtOf(t, "cat-1")
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+current+`","inputUsdPer1M":7.5}`)
	if code != http.StatusOK || dataMap(t, noop)["updatedAt"] != current {
		t.Fatalf("no-op patch: %d %v", code, noop)
	}

	// Stale guard.
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","inputUsdPer1M":1}`)
	if code != http.StatusConflict || conflict["message"] != "模型已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale built-in patch: %d %v", code, conflict)
	}

	// Non-admin gate and schema forks.
	env.login(t, "user1", "user-pass", "user")
	code, forbidden := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+current+`","catalogVisible":false}`)
	if code != http.StatusForbidden || forbidden["message"] != "只有管理员可以维护内置模型配置" {
		t.Fatalf("user built-in patch: %d %v", code, forbidden)
	}
	env.login(t, "root", "root-pass", "super_admin")
	code, notes := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+current+`","notes":"x"}`)
	if code != http.StatusBadRequest || notes["message"] != "内置模型配置参数无效" {
		t.Fatalf("notes not patchable: %d %v", code, notes)
	}
	code, status := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-3",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-3")+`","status":"active"}`)
	if code != http.StatusBadRequest || status["message"] != "内置模型必须配置接口协议" {
		t.Fatalf("completeness validation: %d %v", code, status)
	}
	code, wrongProvider := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gemini/models/cat-1",
		`{"expectedUpdatedAt":"`+current+`","catalogVisible":false}`)
	if code != http.StatusNotFound || wrongProvider["message"] != "模型不存在" {
		t.Fatalf("wrong provider built-in: %d %v", code, wrongProvider)
	}

	// An unknown non-custom id falls through to the custom path (404 with the
	// custom message).
	code, unknown := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-missing",
		`{"expectedUpdatedAt":"`+current+`","status":"active"}`)
	if code != http.StatusNotFound || unknown["message"] != "自定义模型不存在" {
		t.Fatalf("unknown built-in id: %d %v", code, unknown)
	}
	if len(logs.entries) != 2 {
		t.Fatalf("operation log count: %d", len(logs.entries))
	}
}

// TestProvidersDeleteCustomModel covers DELETE: ownership, the binding guard
// messages and the default-reference cleanup.
func TestProvidersDeleteCustomModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.login(t, "user1", "user-pass", "user")

	// Owner deletes the unbound personal model.
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-1", "")
	if code != http.StatusOK || dataMap(t, deleted)["deleted"] != true {
		t.Fatalf("delete own model: %d %v", code, deleted)
	}
	code, again := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-1", "")
	if code != http.StatusNotFound || again["message"] != "自定义模型不存在" {
		t.Fatalf("double delete: %d %v", code, again)
	}

	// Another user's personal model is invisible.
	env.login(t, "user1", "user-pass", "user")
	second := `{"model":"user1-private","inputUsdPer1M":1}`
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", second)
	if code != http.StatusCreated {
		t.Fatalf("second personal model: %d %v", code, created)
	}
	privateID := dataMap(t, created)["id"].(string)
	env.login(t, "user2", "user-pass", "user")
	code, invisible := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+privateID, "")
	if code != http.StatusNotFound || invisible["message"] != "自定义模型不存在" {
		t.Fatalf("other user delete: %d %v", code, invisible)
	}

	// Binding guard: supported-model bindings block the delete with the
	// verbatim message.
	env.login(t, "root", "root-pass", "super_admin")
	env.exec(t, `INSERT INTO accounts (id, system_account_id, name) VALUES ('acc-1', 'sys-1', 'A1')`)
	env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model) VALUES ('acc-1', 'gpt', 'gpt-4o')`)
	code, bound := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-2", "")
	if code != http.StatusConflict {
		t.Fatalf("bound delete: %d %v", code, bound)
	}
	if bound["message"] != "模型已绑定 AI 账户，不能删除；请先从1 个账户支持模型中移除后再删除" {
		t.Fatalf("binding message: %v", bound)
	}
	// Mapping bindings extend the message.
	env.exec(t, `INSERT INTO account_model_mappings (account_id, source_model, upstream_model) VALUES ('acc-1', 'gpt-4o', 'up')`)
	env.exec(t, `INSERT INTO account_model_mappings (account_id, source_model, upstream_model) VALUES ('acc-1', 'src', 'gpt-4o')`)
	code, bound = env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-2", "")
	if code != http.StatusConflict || bound["message"] != "模型已绑定 AI 账户，不能删除；请先从1 个账户支持模型、1 个账户映射下游模型、1 个账户映射上游模型中移除后再删除" {
		t.Fatalf("mapping binding message: %v", bound)
	}

	// Global delete clears a shadowed system default; gpt-4o is shadowed by
	// the built-in row so a fresh model is used for the cleanup assertion.
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"delete-me","scope":"global","inputUsdPer1M":2}`)
	if code != http.StatusCreated {
		t.Fatalf("delete-me create: %d %v", code, created)
	}
	const now = "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO provider_system_default_health_check_models (provider_code, model, created_at, updated_at)
		VALUES ('gpt', 'delete-me', ?, ?)`, now, now)
	deleteID := dataMap(t, created)["id"].(string)
	code, deleted = env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+deleteID, "")
	if code != http.StatusOK || dataMap(t, deleted)["deleted"] != true {
		t.Fatalf("delete-me delete: %d %v", code, deleted)
	}
	if got := env.preferenceCount(t, `SELECT COUNT(*) FROM provider_system_default_health_check_models WHERE provider_code = 'gpt' AND model = 'delete-me'`); got != 0 {
		t.Fatalf("system default must be cleared on delete: %d", got)
	}

	// Unknown id.
	code, unknown := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/custom_model_nope", "")
	if code != http.StatusNotFound || unknown["message"] != "自定义模型不存在" {
		t.Fatalf("unknown delete: %d %v", code, unknown)
	}
	// Anonymous.
	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-2", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous delete: %d %v", code, anonymous)
	}
}

// TestProvidersPutDefaultHealthCheckModel covers the preference endpoint:
// validation messages, the personal/system fork and the overlay pickup.
func TestProvidersPutDefaultHealthCheckModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")

	code, saved := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"gpt-4o"}`)
	if code != http.StatusOK {
		t.Fatalf("save preference: %d %v", code, saved)
	}
	data := dataMap(t, saved)
	if data["providerCode"] != "gpt" || data["defaultHealthCheckModel"] != "gpt-4o" {
		t.Fatalf("save payload: %v", data)
	}
	// The overlay picks the new preference up.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt", "")
	if code != http.StatusOK || dataMap(t, detail)["defaultHealthCheckModel"] != "gpt-4o" {
		t.Fatalf("overlay after save: %d %v", code, detail)
	}

	// Invalid selections.
	code, unknown := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"nope"}`)
	if code != http.StatusBadRequest || unknown["message"] != "模型不在当前用户可见目录中：nope" {
		t.Fatalf("unknown model: %d %v", code, unknown)
	}
	code, image := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"gpt-image-studio"}`)
	if code != http.StatusBadRequest || image["message"] != "默认检查模型只能选择文本生成模型" {
		t.Fatalf("image model: %d %v", code, image)
	}
	// The expired custom model is visible in the inactive diagnosis pass but
	// still unavailable, so the answer is the availability message.
	code, expired := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"gpt-custom-expired"}`)
	if code != http.StatusBadRequest || expired["message"] != "只能把当前可用的模型设置为默认检查模型" {
		t.Fatalf("expired model: %d %v", code, expired)
	}
	// A disabled built-in model exists in the inactive diagnosis.
	code, disabledModel := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"gpt-4-secret"}`)
	if code != http.StatusBadRequest || disabledModel["message"] != "只能把当前可用的模型设置为默认检查模型" {
		t.Fatalf("disabled model: %d %v", code, disabledModel)
	}

	// The admin management fork saves the system default.
	env.login(t, "root", "root-pass", "super_admin")
	code, system := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?viewScope=admin", `{"model":"gpt-4o-mini"}`)
	if code != http.StatusOK || dataMap(t, system)["defaultHealthCheckModel"] != "gpt-4o-mini" {
		t.Fatalf("system default save: %d %v", code, system)
	}
	if got := env.preferenceCount(t, `SELECT COUNT(*) FROM provider_system_default_health_check_models WHERE provider_code = 'gpt' AND model = 'gpt-4o-mini'`); got != 1 {
		t.Fatalf("system default row: %d", got)
	}
	// The user fork ignores viewScope=admin and writes a personal row.
	env.login(t, "user1", "user-pass", "user")
	code, userFork := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?viewScope=admin", `{"model":"gpt-4o-mini"}`)
	if code != http.StatusOK || dataMap(t, userFork)["defaultHealthCheckModel"] != "gpt-4o-mini" {
		t.Fatalf("user viewScope fork: %d %v", code, userFork)
	}

	// Schema and lookup forks.
	code, badBody := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":" "}`)
	if code != http.StatusBadRequest || badBody["message"] != "默认检查模型参数无效" {
		t.Fatalf("bad body: %d %v", code, badBody)
	}
	code, strict := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"gpt-4o","extra":1}`)
	if code != http.StatusBadRequest || strict["message"] != "默认检查模型参数无效" {
		t.Fatalf("strict body: %d %v", code, strict)
	}
	code, missingProvider := env.do(t, http.MethodPut, "/__aisys__/api/providers/nope/default-health-check-model", `{"model":"gpt-4o"}`)
	if code != http.StatusNotFound || missingProvider["message"] != "供应商不存在" {
		t.Fatalf("unknown provider: %d %v", code, missingProvider)
	}
	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"gpt-4o"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous put: %d %v", code, anonymous)
	}
}

// TestProvidersCustomShutdownFilter closes the T5 review item on
// /{code}/models: the active / visible / shutdown filters hold for custom
// rows too (no catalog_visible clause; shutdown/expiry hides by default and
// includeInactive lifts it).
func TestProvidersCustomShutdownFilter(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, models := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models", "")
	if code != http.StatusOK {
		t.Fatalf("models: %d %v", code, models)
	}
	seen := map[string]bool{}
	for _, row := range dataArray(t, models) {
		seen[row.(map[string]any)["model"].(string)] = true
	}
	if seen["gpt-custom-expired"] {
		t.Fatalf("shutdown custom model leaked: %v", seen)
	}
	if !seen["gpt-image-studio"] {
		t.Fatalf("priced image custom model must stay in the catalog: %v", seen)
	}
	code, inactive := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models?includeInactive=true", "")
	if code != http.StatusOK {
		t.Fatalf("includeInactive: %d %v", code, inactive)
	}
	seen = map[string]bool{}
	for _, row := range dataArray(t, inactive) {
		seen[row.(map[string]any)["model"].(string)] = true
	}
	if !seen["gpt-custom-expired"] {
		t.Fatalf("includeInactive must lift the shutdown filter: %v", seen)
	}
}

// captureSink records operation log entries for assertions.
type captureSink struct {
	entries []authsys.OperationLogEntry
}

func (c *captureSink) Record(entry authsys.OperationLogEntry, r *http.Request) {
	c.entries = append(c.entries, entry)
}
