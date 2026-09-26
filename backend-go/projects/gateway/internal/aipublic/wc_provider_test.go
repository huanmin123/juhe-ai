// 公开面 provider 家族（GET /__aipublic__/provider/list、/provider/detail）
// 补测：enabled 过滤、内存分页公式、稳定顺序、strict query 校验、404/400
// 分支、basic/models 两个 tab 的投影、服务端模型分类、无 scope 403 与内置
// 测试 token 的确定性 mock。真实路径走 newAIPublicEnv 的 providers store。
package aipublic

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// wcProviderToken 签发带两个 provider 读 scope 的来源 token。
func wcProviderToken(t *testing.T, env *aipublicEnv, scopes []string) string {
	t.Helper()
	return env.seedSource("extsrc_prov", "exttok_prov", "juis_token_provvvvvvvvv",
		"active", "active", scopes, "[]", "", "")
}

func wcProviderScopesList() []string {
	return []string{scopeProviderListRead, scopeProviderDetailRead}
}

// seedProviderRow 直接插入 provider 定义行（name 可与 code 不同）。
func (e *aipublicEnv) seedProviderRow(code, name string, enabled bool) {
	e.t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	if _, err := e.db.Exec(`INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, '[]', ?, ?)`, "prov_"+code, code, name, enabledInt, now, now); err != nil {
		e.t.Fatal(err)
	}
}

// seedProviderCatalogRow 插入内置模型目录行；inputUsd 为 nil 表示无价。
func (e *aipublicEnv) seedProviderCatalogRow(id, providerCode, model, status string, inputUsd any) {
	e.t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(`INSERT INTO provider_model_catalog (id, provider_code, model, status,
		input_usd_per_1m, supported_api_protocols_json, supported_service_tiers_json,
		supported_reasoning_efforts_json, service_tier_prices_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '[]', '[]', '[]', '{}', ?, ?)`,
		id, providerCode, model, status, inputUsd, now, now); err != nil {
		e.t.Fatal(err)
	}
}

// seedProviderProfile 插入协议档案 + 一个启用的端点族绑定。
func (e *aipublicEnv) seedProviderProfile(id, providerCode string) {
	e.t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(`INSERT INTO provider_protocol_profiles
		(id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, 'openai', 'v1', 'https://profile.example/v1', 'hc-model', '["api_key"]', '["chat_completions"]', ?, ?)`,
		id, providerCode, "档案"+providerCode, now, now); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO protocol_endpoint_families
		(id, protocol_code, protocol_version, family_code, name, enabled, created_at, updated_at)
		VALUES ('fam_'||?, 'openai', 'v1', 'chat_completions', 'Chat Completions', 1, ?, ?)`,
		id, now, now); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO provider_protocol_profile_families (profile_id, family_code, enabled, created_at, updated_at)
		VALUES (?, 'chat_completions', 1, ?, ?)`, id, now, now); err != nil {
		e.t.Fatal(err)
	}
}

func wcData(t *testing.T, status int, payload map[string]any, wantStatus int) map[string]any {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status: %d %v", status, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("缺 data 信封: %v", payload)
	}
	if data["source"] != "stats" {
		t.Fatalf("真实路径信封必须为 stats: %v", data)
	}
	return data
}

// TestWCProviderList：enabled 过滤、稳定顺序、分页四件套与 strict query。
func TestWCProviderList(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProviderRow("b-provider", "Beta", true)
	env.seedProviderRow("a-provider", "alpha", true)
	env.seedProviderRow("c-provider", "Gamma", false)
	env.seedProviderRow("d-provider", "Delta", true)
	token := wcProviderToken(t, env, wcProviderScopesList())

	// 默认分页：仅 enabled，store 的 ORDER BY name ASC 为二进制序（大写在前）：
	// "Beta" < "Delta" < "alpha"。
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/provider/list", "", token)
	data := wcData(t, status, payload, http.StatusOK)
	if data["page"] != float64(1) || data["pageSize"] != float64(20) {
		t.Fatalf("默认分页: %v", data)
	}
	items := data["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("enabled 过滤: %v", items)
	}
	wantOrder := []string{"b-provider", "d-provider", "a-provider"}
	for index, item := range items {
		if got := item.(map[string]any)["code"]; got != wantOrder[index] {
			t.Fatalf("稳定顺序 %d: %v != %s", index, got, wantOrder[index])
		}
	}
	first := items[0].(map[string]any)
	if first["name"] != "Beta" || first["enabled"] != true {
		t.Fatalf("列表项投影: %v", first)
	}
	if _, hasParent := first["parentCode"]; hasParent {
		t.Fatalf("空 parentCode 必须省略: %v", first)
	}

	// pageSize=1：hasMore/pageUpperBound 公式。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/list?page=1&pageSize=1", "", token)
	data = wcData(t, status, payload, http.StatusOK)
	if data["pageUpperBound"] != float64(2) || data["hasMore"] != true {
		t.Fatalf("第 1 页分页: %v", data)
	}
	if items := data["items"].([]any); len(items) != 1 || items[0].(map[string]any)["code"] != "b-provider" {
		t.Fatalf("第 1 页条目: %v", data["items"])
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/list?page=3&pageSize=1", "", token)
	data = wcData(t, status, payload, http.StatusOK)
	if data["pageUpperBound"] != float64(3) || data["hasMore"] != false {
		t.Fatalf("第 3 页分页: %v", data)
	}
	// 越界页：空条目，上界按契约公式 (page-1)*pageSize+本页条数+hasMore。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/list?page=9&pageSize=1", "", token)
	data = wcData(t, status, payload, http.StatusOK)
	if data["pageUpperBound"] != float64(8) || data["hasMore"] != false || len(data["items"].([]any)) != 0 {
		t.Fatalf("越界页: %v", data)
	}

	// strict：未知键（HTTP 层 400 文案走 kernel 本地化，zod 原文在 parse 层锁定）。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/list?targetUsername=u1", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("未知键: %d %v", status, payload)
	}
	// pageSize 越界。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/list?pageSize=101", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("pageSize>100: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/list?page=0", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("page<1: %d %v", status, payload)
	}
}

// TestWCProviderDetailBasic：basic tab 投影、404 与 query 校验。
func TestWCProviderDetailBasic(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProviderRow("gpt", "GPT", true)
	env.seedProviderRow("dead", "停用", false)
	env.seedProviderProfile("profile_gpt_openai_v1", "gpt")
	token := wcProviderToken(t, env, wcProviderScopesList())

	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=basic", "", token)
	data := wcData(t, status, payload, http.StatusOK)
	provider := data["provider"].(map[string]any)
	if provider["code"] != "gpt" || provider["name"] != "GPT" || provider["enabled"] != true {
		t.Fatalf("provider 投影: %v", provider)
	}
	if _, hasPage := data["page"]; hasPage {
		t.Fatalf("basic tab 不得带分页字段: %v", data)
	}
	profiles := data["protocolProfiles"].([]any)
	if len(profiles) != 1 {
		t.Fatalf("protocolProfiles: %v", profiles)
	}
	profile := profiles[0].(map[string]any)
	if profile["id"] != "profile_gpt_openai_v1" || profile["baseUrl"] != "https://profile.example/v1" ||
		profile["protocolCode"] != "openai" || profile["protocolVersion"] != "v1" ||
		profile["defaultHealthCheckModel"] != "hc-model" || profile["enabled"] != true {
		t.Fatalf("profile 投影: %v", profile)
	}
	if accountTypes := profile["accountTypes"].([]any); len(accountTypes) != 1 || accountTypes[0] != "api_key" {
		t.Fatalf("accountTypes: %v", profile["accountTypes"])
	}
	if capabilities := profile["capabilities"].([]any); len(capabilities) != 1 || capabilities[0] != "chat_completions" {
		t.Fatalf("capabilities: %v", profile["capabilities"])
	}
	families := profile["endpointFamilies"].([]any)
	if len(families) != 1 {
		t.Fatalf("endpointFamilies: %v", families)
	}
	family := families[0].(map[string]any)
	if family["code"] != "chat_completions" || family["name"] != "Chat Completions" {
		t.Fatalf("endpointFamily 投影: %v", family)
	}

	// 无 profile 供应商：空数组而非 null，且不报错。
	env.seedProviderRow("bare", "裸供应商", true)
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=bare&tab=basic", "", token)
	data = wcData(t, status, payload, http.StatusOK)
	if profiles := data["protocolProfiles"].([]any); len(profiles) != 0 {
		t.Fatalf("无 profile 必须为空数组: %v", data["protocolProfiles"])
	}

	// 404：未知 code / 停用 code。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=ghost&tab=basic", "", token)
	if status != http.StatusNotFound || payload["message"] != "供应商不存在或已停用" {
		t.Fatalf("未知供应商: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=dead&tab=basic", "", token)
	if status != http.StatusNotFound || payload["message"] != "供应商不存在或已停用" {
		t.Fatalf("停用供应商: %d %v", status, payload)
	}

	// 400：缺 code / 缺 tab / 非法 tab / basic 带 models 专属键（HTTP 层文案
	// 走 kernel 本地化，zod 原文在 parse 层锁定）。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?tab=basic", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("缺 code: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("缺 tab: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=pricing", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("非法 tab: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=basic&category=text", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("basic 带 category: %d %v", status, payload)
	}
}

// seedWCModelCatalog 造一个含 text/image/无价/停用四行的 gpt 目录。
func seedWCModelCatalog(t *testing.T, env *aipublicEnv) {
	t.Helper()
	env.seedProviderRow("gpt", "GPT", true)
	env.seedProviderCatalogRow("cat-text", "gpt", "gpt-4o", "active", 2.5)
	env.seedProviderCatalogRow("cat-image", "gpt", "gpt-image-1", "active", 5.0)
	env.seedProviderCatalogRow("cat-unpriced", "gpt", "gpt-unpriced", "active", nil)
	env.seedProviderCatalogRow("cat-disabled", "gpt", "gpt-off", "disabled", 1.0)
}

// TestWCProviderDetailModels：服务端分类、active+priced 过滤、category 过滤
// 与分页。
func TestWCProviderDetailModels(t *testing.T) {
	env := newAIPublicEnv(t)
	seedWCModelCatalog(t, env)
	token := wcProviderToken(t, env, wcProviderScopesList())

	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models", "", token)
	data := wcData(t, status, payload, http.StatusOK)
	provider := data["provider"].(map[string]any)
	if provider["code"] != "gpt" || provider["name"] != "GPT" {
		t.Fatalf("models provider 投影: %v", provider)
	}
	if _, hasCategory := data["category"]; hasCategory {
		t.Fatalf("未给 category 不得回显: %v", data)
	}
	items := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("active+priced 过滤: %v", items)
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if first["model"] != "gpt-4o" || first["category"] != "text" || first["status"] != "active" {
		t.Fatalf("text 模型: %v", first)
	}
	if first["inputUsdPer1M"] != float64(2.5) {
		t.Fatalf("模型价格投影: %v", first)
	}
	if second["model"] != "gpt-image-1" || second["category"] != "image" {
		t.Fatalf("image 分类: %v", second)
	}

	// category 过滤 + 回显。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models&category=image", "", token)
	data = wcData(t, status, payload, http.StatusOK)
	if data["category"] != "image" {
		t.Fatalf("category 回显: %v", data)
	}
	items = data["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["model"] != "gpt-image-1" {
		t.Fatalf("category=image 过滤: %v", items)
	}

	// models 分页：pageSize=1。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models&page=1&pageSize=1", "", token)
	data = wcData(t, status, payload, http.StatusOK)
	if data["pageUpperBound"] != float64(2) || data["hasMore"] != true {
		t.Fatalf("models 分页: %v", data)
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models&page=2&pageSize=1", "", token)
	data = wcData(t, status, payload, http.StatusOK)
	if data["pageUpperBound"] != float64(2) || data["hasMore"] != false {
		t.Fatalf("models 第 2 页: %v", data)
	}

	// strict：models tab 的非法 category（HTTP 层文案本地化）。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models&category=audio", "", token)
	if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("非法 category: %d %v", status, payload)
	}
}

// TestWCProviderDetailEmptyCatalog：目录为空时 models tab 返回空数组不报错。
func TestWCProviderDetailEmptyCatalog(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProviderRow("empty", "空目录", true)
	token := wcProviderToken(t, env, wcProviderScopesList())
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=empty&tab=models", "", token)
	data := wcData(t, status, payload, http.StatusOK)
	if items := data["items"].([]any); len(items) != 0 {
		t.Fatalf("空目录必须为空数组: %v", data["items"])
	}
	if data["pageUpperBound"] != float64(0) || data["hasMore"] != false {
		t.Fatalf("空目录分页: %v", data)
	}
}

// TestWCProviderScopeForbidden：无 provider scope 的 token 403。
func TestWCProviderScopeForbidden(t *testing.T) {
	env := newAIPublicEnv(t)
	token := wcProviderToken(t, env, []string{scopeGroupListRead})
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/provider/list", "", token)
	if status != http.StatusForbidden || payload["code"] != "external_source_scope_forbidden" {
		t.Fatalf("list 无 scope: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=basic", "", token)
	if status != http.StatusForbidden || payload["code"] != "external_source_scope_forbidden" {
		t.Fatalf("detail 无 scope: %d %v", status, payload)
	}
}

// TestWCProviderMockFamily：内置测试 token 的确定性 mock（list + detail 两
// tab），重复调用锁定载荷内容一致。
func TestWCProviderMockFamily(t *testing.T) {
	env := newWCMockEnv(t)
	token := "juis_token_wcwcwcwcwcwcwcwc"

	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/provider/list?page=3&pageSize=7", "", token)
	data := mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["page"] != float64(3) || data["pageSize"] != float64(7) || data["pageUpperBound"] != float64(1) || data["hasMore"] != false {
		t.Fatalf("mock list 分页投影: %v", data)
	}
	items := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("mock list 固定一条: %v", items)
	}
	item := items[0].(map[string]any)
	if item["code"] != "gpt" || item["name"] != "GPT" || item["enabled"] != true {
		t.Fatalf("mock list 条目: %v", item)
	}

	// detail basic：profile 字段与默认档案。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=basic", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	provider := data["provider"].(map[string]any)
	if provider["code"] != "gpt" || provider["name"] != "GPT" || provider["enabled"] != true {
		t.Fatalf("mock basic provider: %v", provider)
	}
	profiles := data["protocolProfiles"].([]any)
	if len(profiles) != 1 {
		t.Fatalf("mock basic profiles: %v", profiles)
	}
	profile := profiles[0].(map[string]any)
	if profile["name"] != "默认 OpenAI 协议档案" || profile["protocolCode"] != "openai" ||
		profile["protocolVersion"] != "v1" || profile["baseUrl"] != "https://api.openai.com/v1" ||
		profile["enabled"] != true {
		t.Fatalf("mock basic profile: %v", profile)
	}
	if accountTypes := profile["accountTypes"].([]any); len(accountTypes) != 1 || accountTypes[0] != "api_key" {
		t.Fatalf("mock basic accountTypes: %v", profile["accountTypes"])
	}
	if families := profile["endpointFamilies"].([]any); len(families) != 1 {
		t.Fatalf("mock basic endpointFamilies: %v", profile["endpointFamilies"])
	}
	if models := data["defaultSupportedModels"].([]any); len(models) == 0 {
		t.Fatalf("mock basic defaultSupportedModels: %v", data["defaultSupportedModels"])
	}

	// detail models：两条 + 分类。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["pageUpperBound"] != float64(1) || data["hasMore"] != false {
		t.Fatalf("mock models 分页: %v", data)
	}
	models := data["items"].([]any)
	if len(models) != 2 {
		t.Fatalf("mock models 条数: %v", models)
	}
	if models[0].(map[string]any)["model"] != "gpt-4o" || models[0].(map[string]any)["category"] != "text" {
		t.Fatalf("mock models text: %v", models[0])
	}
	if models[1].(map[string]any)["model"] != "gpt-image-1" || models[1].(map[string]any)["category"] != "image" {
		t.Fatalf("mock models image: %v", models[1])
	}

	// mock models 的 category 过滤同样生效。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models&category=image", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["category"] != "image" {
		t.Fatalf("mock category 回显: %v", data)
	}
	if models := data["items"].([]any); len(models) != 1 || models[0].(map[string]any)["model"] != "gpt-image-1" {
		t.Fatalf("mock category 过滤: %v", models)
	}

	// 确定性：去掉 generatedAt 后两次调用载荷逐字一致。
	firstPayload, secondPayload := wcMockPayload(t, env, token), wcMockPayload(t, env, token)
	if string(firstPayload) != string(secondPayload) {
		t.Fatalf("mock 必须确定性:\n%s\n%s", firstPayload, secondPayload)
	}
}

func wcMockPayload(t *testing.T, env *aipublicEnv, token string) []byte {
	t.Helper()
	_, payload, _ := env.doAuth(http.MethodGet, Prefix+"/provider/detail?code=gpt&tab=models", "", token)
	data := payload["data"].(map[string]any)
	delete(data, "generatedAt")
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestWCProviderCategoryRules：分类规则纯函数的边界（前缀命中与大小写归一）。
func TestWCProviderCategoryRules(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"gpt-image-1", "image"},
		{"gpt-image", "image"},
		{"dall-e-3", "image"},
		{" DALL-E-2 ", "image"},
		{"gpt-4o", "text"},
		{"claude-sonnet", "text"},
		{"", "text"},
	}
	for _, testCase := range cases {
		if got := publicModelCategory(testCase.model); got != testCase.want {
			t.Fatalf("publicModelCategory(%q) = %q, want %q", testCase.model, got, testCase.want)
		}
	}
}

// TestWCProviderQueryParsers：parse 层的 zod issue 原文（HTTP 层经 kernel
// 本地化后只渲染「请求参数无效」，原文在这里锁定）。
func TestWCProviderQueryParsers(t *testing.T) {
	if _, issue := parseProviderListQuery(mustParseQuery(t, "targetUsername=u1")); issue != zodUnrecognizedKeys("targetUsername") {
		t.Fatalf("list 未知键 issue: %q", issue)
	}
	if _, issue := parseProviderListQuery(mustParseQuery(t, "pageSize=101")); issue != zodNumberMax(100) {
		t.Fatalf("pageSize>100 issue: %q", issue)
	}
	if _, issue := parseProviderListQuery(mustParseQuery(t, "page=0")); issue != zodNumberMin(1) {
		t.Fatalf("page<1 issue: %q", issue)
	}
	parsed, issue := parseProviderListQuery(mustParseQuery(t, ""))
	if issue != "" || parsed == nil {
		t.Fatalf("空 query 必须通过: %v %q", parsed, issue)
	}

	if _, issue := parseProviderDetailQuery(mustParseQuery(t, "tab=basic")); issue != zodRequired {
		t.Fatalf("缺 code issue: %q", issue)
	}
	if _, issue := parseProviderDetailQuery(mustParseQuery(t, "code=gpt")); issue != zodRequired {
		t.Fatalf("缺 tab issue: %q", issue)
	}
	if _, issue := parseProviderDetailQuery(mustParseQuery(t, "code=gpt&tab=pricing")); issue != zodEnumMessage(providerDetailTabs, "pricing") {
		t.Fatalf("非法 tab issue: %q", issue)
	}
	if _, issue := parseProviderDetailQuery(mustParseQuery(t, "code=gpt&tab=basic&category=text")); issue != zodUnrecognizedKeys("category") {
		t.Fatalf("basic 带 category issue: %q", issue)
	}
	if _, issue := parseProviderDetailQuery(mustParseQuery(t, "code=gpt&tab=models&category=audio")); issue != zodEnumMessage(providerModelCategories, "audio") {
		t.Fatalf("非法 category issue: %q", issue)
	}
	modelsParsed, issue := parseProviderDetailQuery(mustParseQuery(t, "code=gpt&tab=models&category=image&page=2&pageSize=5"))
	if issue != "" || modelsParsed == nil {
		t.Fatalf("models query 必须通过: %v %q", modelsParsed, issue)
	}
	if modelsParsed.Category != "image" || !modelsParsed.HasCategory || modelsParsed.Page != 2 || modelsParsed.PageSize != 5 {
		t.Fatalf("models query 解析: %+v", modelsParsed)
	}
}
