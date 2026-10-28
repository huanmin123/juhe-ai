// plaza_test.go 钉住模型广场聚合读接口（GET /providers/models/plaza，模型广
// 场页与供应商目录退役设计 §3）的窗口与聚合契约：用户面 pin 自己、仅 enabled
// vendor、catalogVisible=false 的 builtin 行不出现；管理面窗口含 disabled
// vendor 行、草稿行与隐藏 builtin 行、可按 systemAccountId 过滤（all 视空）；
// 跨供应商同名模型不合并（价格独立）；providers 摘要与 items 的 vendor 集合
// 一致；每行挂 catalogDisplay。夹具沿用 providers 包 testEnv/seedCatalog
// （providers_test.go），广场专用补充夹具见 seedPlazaFixture。
package providers

import (
	"net/http"
	"strings"
	"testing"
)

// seedPlazaFixture 在 seedCatalog 之上补齐广场专用夹具：glm 启用 vendor、
// gpt 与 glm 各一行同名内置模型（跨供应商同名不合并断言，异价证独立）、root
// 名下一条 gemini personal 行（归属过滤与 pin 断言）。
func seedPlazaFixture(t *testing.T, env *testEnv) {
	t.Helper()
	const now = "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO vendors (id, code, name, description, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-glm', 'glm', 'Glm', NULL, 1, '[]', ?, ?)`, now, now)
	env.seedBuiltinModel(t, builtinModelSeed{ID: "cat-gpt-shared", ProviderCode: "gpt", Model: "plaza-shared-model", Status: "active",
		ReleaseDate: "2026-03-01", Protocols: `["chat_completions"]`, InputUsd: ptrFloat64(5.5)})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "cat-glm-shared", ProviderCode: "glm", Model: "plaza-shared-model", Status: "active",
		ReleaseDate: "2026-03-01", Protocols: `["chat_completions"]`, InputUsd: ptrFloat64(4.5)})
	root := env.requireAccount(t, "root", "root-pass", "super_admin")
	env.seedCustomModel(t, customModelSeed{ID: "cu-root-personal", ProviderCode: "gemini", Model: "root-personal-model", Scope: "personal",
		SystemAccountID: &root, ReleaseDate: "2026-02-01", Protocols: `["generate_content"]`, InputUsd: ptrFloat64(2.0)})
}

// plazaKeys 把 items 投影成 "providerCode/model" 键序列（行序断言）。
func plazaKeys(t *testing.T, payload map[string]any) []string {
	t.Helper()
	items := dataMap(t, payload)["items"].([]any)
	keys := make([]string, 0, len(items))
	for _, raw := range items {
		row := raw.(map[string]any)
		keys = append(keys, row["providerCode"].(string)+"/"+row["model"].(string))
	}
	return keys
}

func plazaProviderRows(t *testing.T, payload map[string]any) []any {
	t.Helper()
	return dataMap(t, payload)["providers"].([]any)
}

// plazaProviderCodes 投影 providers 摘要的 code 序列（vendor 集合断言）。
func plazaProviderCodes(t *testing.T, payload map[string]any) []string {
	t.Helper()
	rows := plazaProviderRows(t, payload)
	codes := make([]string, 0, len(rows))
	for _, raw := range rows {
		codes = append(codes, raw.(map[string]any)["code"].(string))
	}
	return codes
}

// assertPlazaProviderItemVendorCover 钉住 providers 摘要与 items 的 vendor
// 集合一致：items 不引用 providers 之外的 vendor（管理面窗口 providers 可含
// 无行 vendor，故只断言单向覆盖）。
func assertPlazaProviderItemVendorCover(t *testing.T, payload map[string]any) {
	t.Helper()
	covered := map[string]bool{}
	for _, code := range plazaProviderCodes(t, payload) {
		covered[code] = true
	}
	for _, key := range plazaKeys(t, payload) {
		code := strings.SplitN(key, "/", 2)[0]
		if !covered[code] {
			t.Fatalf("item vendor %q outside providers summary %v", code, covered)
		}
	}
}

// TestProviderModelPlazaUserWindow 钉用户面窗口：pin 自己（传入他人
// systemAccountId 被忽略）、仅 enabled vendor、catalogVisible=false 的
// builtin 行不出现、草稿/无价行按恒 true 窗口语义返回、跨供应商同名模型两行
// 都在且价格独立、每行挂 catalogDisplay。
func TestProviderModelPlazaUserWindow(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	seedPlazaFixture(t, env)
	user1 := env.requireAccount(t, "user1", "user-pass", "user")
	env.login(t, "user1", "user-pass", "user")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza", "")
	if code != http.StatusOK {
		t.Fatalf("user plaza: %d %v", code, payload)
	}
	// providers：仅 enabled vendor（gemini/glm/gpt），无 disabled anthropic。
	codes := plazaProviderCodes(t, payload)
	if strings.Join(codes, ",") != "gemini,glm,gpt" {
		t.Fatalf("user providers: %v", codes)
	}
	// items：vendor 注册表顺序拼接（gemini → glm → gpt），vendor 内 release
	// desc 排序；无隐藏行、无他人 personal 行；草稿行与无价行在窗口内。
	want := []string{
		"gemini/gemini-2.5-pro",
		"glm/plaza-shared-model",
		"gpt/plaza-shared-model",
		"gpt/gpt-4o-personal",
		"gpt/gpt-4o-realtime",
		"gpt/gpt-4o-mini",
		"gpt/gpt-4o",
		"gpt/gpt-4o-expired",
		"gpt/gpt-4-secret",
		"gpt/gpt-4o-unpriced",
	}
	got := plazaKeys(t, payload)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("user items: %v (want %v)", got, want)
	}
	assertPlazaProviderItemVendorCover(t, payload)

	// 跨供应商同名模型两行独立：价格各自保留（gpt 5.5 / glm 4.5）。
	prices := map[string]float64{}
	for _, raw := range dataMap(t, payload)["items"].([]any) {
		row := raw.(map[string]any)
		if row["model"] != "plaza-shared-model" {
			continue
		}
		if row["scope"] != "built_in" {
			t.Fatalf("shared row scope: %v", row)
		}
		prices[row["providerCode"].(string)] = row["inputUsdPer1M"].(float64)
	}
	if len(prices) != 2 || prices["gpt"] != 5.5 || prices["glm"] != 4.5 {
		t.Fatalf("cross-vendor same-name rows must stay independent: %v", prices)
	}

	// 个人行 pin 自己：scope/systemAccountId 归属 user1。
	personalSeen := false
	for _, raw := range dataMap(t, payload)["items"].([]any) {
		row := raw.(map[string]any)
		if row["model"] == "gpt-4o-personal" {
			personalSeen = true
			if row["scope"] != "personal" || row["systemAccountId"] != user1 {
				t.Fatalf("personal row ownership: %v", row)
			}
		}
		if row["model"] == "root-personal-model" {
			t.Fatalf("foreign personal row leaked to user window: %v", row)
		}
		if row["model"] == "gpt-4o-hidden" {
			t.Fatalf("catalogVisible=false builtin row leaked to user window: %v", row)
		}
	}
	if !personalSeen {
		t.Fatalf("own personal row missing: %v", got)
	}

	// 每行挂 catalogDisplay 键（照常走 buildProviderCatalogDisplay，与
	// /{code}/models 同源：无可渲染 section 的行序列化为 null）；有价行的具名
	// vendor 投影非空，共享行带 token_pricing section。
	sharedDisplay := map[string]bool{}
	for _, raw := range dataMap(t, payload)["items"].([]any) {
		row := raw.(map[string]any)
		display, hasKey := row["catalogDisplay"]
		if !hasKey {
			t.Fatalf("catalogDisplay key missing: %v", row)
		}
		sections, _ := display.([]any)
		if row["model"] != "plaza-shared-model" {
			// 无任何价格/规格的行（gpt-4o-unpriced）与现单供应商目录同样出
			// null；带价行必须渲染非空 section。
			if len(sections) == 0 && row["model"] != "gpt-4o-unpriced" {
				t.Fatalf("priced row must render sections: %v", row)
			}
			continue
		}
		if len(sections) == 0 {
			t.Fatalf("shared row display empty: %v", row)
		}
		for _, section := range sections {
			if section.(map[string]any)["key"] == "token_pricing" {
				sharedDisplay[row["providerCode"].(string)] = true
			}
		}
	}
	if !sharedDisplay["gpt"] || !sharedDisplay["glm"] {
		t.Fatalf("named-vendor display sections missing: %v", sharedDisplay)
	}

	// providers 摘要键集即设计 §3.2 四键；glm 摘要由内置目录派生。
	for _, raw := range plazaProviderRows(t, payload) {
		row := raw.(map[string]any)
		if len(row) != 4 {
			t.Fatalf("provider summary key set: %v", row)
		}
		if row["code"] == "glm" && (row["name"] != "Glm" || row["enabled"] != true || row["defaultHealthCheckModel"] != "plaza-shared-model") {
			t.Fatalf("glm summary: %v", row)
		}
	}

	// 非 admin 传 systemAccountId 被忽略（恒 pin 自己），窗口不变。
	code, pinned := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza?systemAccountId="+env.requireAccount(t, "root", "root-pass", "super_admin"), "")
	if code != http.StatusOK || strings.Join(plazaKeys(t, pinned), ",") != strings.Join(want, ",") {
		t.Fatalf("user with foreign systemAccountId must stay pinned: %d %v", code, plazaKeys(t, pinned))
	}

	// 非 admin 传 viewScope=admin 不抬窗（角色判定不通过）：仍仅 enabled
	// vendor、无隐藏行。
	code, escalated := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza?viewScope=admin", "")
	if code != http.StatusOK || strings.Join(plazaKeys(t, escalated), ",") != strings.Join(want, ",") {
		t.Fatalf("user viewScope=admin must not widen window: %d %v", code, plazaKeys(t, escalated))
	}
	if codes := plazaProviderCodes(t, escalated); strings.Join(codes, ",") != "gemini,glm,gpt" {
		t.Fatalf("user viewScope=admin providers: %v", codes)
	}

	// 匿名调用被 read 中间件族拒绝。
	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous plaza: %d %v", code, anonymous)
	}
}

// TestProviderModelPlazaAdminWindow 钉管理面窗口：viewScope=admin 可见
// disabled vendor 行与草稿/隐藏 builtin 行、可按 systemAccountId 过滤（all
// 视空）、per-vendor 合并语义保留（global custom 压过同名 builtin）。
func TestProviderModelPlazaAdminWindow(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	seedPlazaFixture(t, env)
	root := env.requireAccount(t, "root", "root-pass", "super_admin")
	user1 := env.requireAccount(t, "user1", "user-pass", "user")
	env.login(t, "root", "root-pass", "super_admin")

	// 管理面默认（pin root 自己）：providers 含 disabled anthropic；items 含
	// root personal 行、隐藏 builtin 行、草稿行。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza?viewScope=admin", "")
	if code != http.StatusOK {
		t.Fatalf("admin plaza: %d %v", code, payload)
	}
	if codes := plazaProviderCodes(t, payload); strings.Join(codes, ",") != "anthropic,gemini,glm,gpt" {
		t.Fatalf("admin providers: %v", codes)
	}
	for _, raw := range plazaProviderRows(t, payload) {
		row := raw.(map[string]any)
		if row["code"] == "anthropic" && (row["enabled"] != false || row["defaultHealthCheckModel"] != "") {
			t.Fatalf("disabled vendor summary: %v", row)
		}
	}
	want := []string{
		"gemini/root-personal-model",
		"gemini/gemini-2.5-pro",
		"glm/plaza-shared-model",
		"gpt/plaza-shared-model",
		"gpt/gpt-4o-realtime",
		"gpt/gpt-4o-mini",
		"gpt/gpt-4o",
		"gpt/gpt-4o-expired",
		"gpt/gpt-4-secret",
		"gpt/gpt-4o-hidden",
		"gpt/gpt-4o-unpriced",
	}
	got := plazaKeys(t, payload)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("admin items: %v (want %v)", got, want)
	}
	assertPlazaProviderItemVendorCover(t, payload)

	// per-vendor 合并语义与弹窗一致：gpt-4o 由 global custom 行胜出，价格独立
	// 于内置行；vendor 内排序与单供应商目录同口径。
	for _, raw := range dataMap(t, payload)["items"].([]any) {
		row := raw.(map[string]any)
		if row["providerCode"] == "gpt" && row["model"] == "gpt-4o" {
			if row["scope"] != "global" || row["source"] != "custom-global" || row["inputUsdPer1M"] != float64(6) {
				t.Fatalf("per-vendor merge winner: %v", row)
			}
		}
	}

	// admin 按 systemAccountId 过滤：切到 user1 归属后 root personal 行消失、
	// user1 personal 行出现。
	code, filtered := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza?viewScope=admin&systemAccountId="+user1, "")
	if code != http.StatusOK {
		t.Fatalf("admin filtered plaza: %d %v", code, filtered)
	}
	filteredWant := []string{
		"gemini/gemini-2.5-pro",
		"glm/plaza-shared-model",
		"gpt/plaza-shared-model",
		"gpt/gpt-4o-personal",
		"gpt/gpt-4o-realtime",
		"gpt/gpt-4o-mini",
		"gpt/gpt-4o",
		"gpt/gpt-4o-expired",
		"gpt/gpt-4-secret",
		"gpt/gpt-4o-hidden",
		"gpt/gpt-4o-unpriced",
	}
	if got := plazaKeys(t, filtered); strings.Join(got, ",") != strings.Join(filteredWant, ",") {
		t.Fatalf("admin filtered items: %v (want %v)", got, filteredWant)
	}

	// systemAccountId=all 视空：沿 requestSystemAccountID 现语义回落 pin
	// admin 自己（与不传该参数完全一致），而非 global-only。
	code, all := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza?viewScope=admin&systemAccountId=all", "")
	if code != http.StatusOK {
		t.Fatalf("admin all plaza: %d %v", code, all)
	}
	if got := plazaKeys(t, all); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("admin all items must equal default pin-self window: %v (want %v)", got, want)
	}
	for _, raw := range dataMap(t, all)["items"].([]any) {
		row := raw.(map[string]any)
		if row["model"] == "gpt-4o-personal" {
			t.Fatalf("systemAccountId=all must not surface other owners' rows: %v", row)
		}
	}

	// 非 admin 角色登录后 viewScope=admin 不生效（同用户面窗口）。
	env.login(t, "user1", "user-pass", "user")
	code, escalated := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/plaza?viewScope=admin&systemAccountId="+root, "")
	if code != http.StatusOK {
		t.Fatalf("non-admin admin-scope plaza: %d %v", code, escalated)
	}
	for _, key := range plazaKeys(t, escalated) {
		if key == "anthropic/anything" || strings.HasSuffix(key, "/gpt-4o-hidden") || strings.HasSuffix(key, "/root-personal-model") {
			t.Fatalf("non-admin must not widen via viewScope/systemAccountId: %v", plazaKeys(t, escalated))
		}
	}
	if codes := plazaProviderCodes(t, escalated); strings.Join(codes, ",") != "gemini,glm,gpt" {
		t.Fatalf("non-admin admin-scope providers: %v", codes)
	}
}
