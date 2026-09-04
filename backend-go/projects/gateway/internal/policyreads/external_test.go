package policyreads

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestExternalScopesAndAPIDocs(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, scopes, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/scopes", "")
	options := dataSlice(t, scopes)
	if code != 200 || len(options) != len(externalIntegrationScopeOptions) {
		t.Fatalf("scopes: %d %v", code, scopes)
	}
	first := options[0].(map[string]any)
	if first["value"] != "juhe_ai_public:api_key_list:read" || first["label"] != "GET API Key 列表" {
		t.Fatalf("first scope: %v", first)
	}

	code, docs, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/api-docs", "")
	catalog := dataMap(t, docs)
	if code != 200 || catalog["basePath"] != "/__aipublic__" || catalog["authType"] != "Bearer" {
		t.Fatalf("catalog head: %v", catalog)
	}
	items, ok := catalog["items"].([]any)
	if !ok || len(items) != 16 {
		t.Fatalf("catalog items: %v", catalog)
	}
	for _, entry := range items {
		item := entry.(map[string]any)
		if item["id"] == "group-list" {
			if item["scope"] != "juhe_ai_public:group_list:read" {
				t.Fatalf("group-list scope: %v", item)
			}
			if fields, ok := item["responseFields"].([]any); !ok || len(fields) == 0 {
				t.Fatalf("group-list responseFields: %v", item)
			}
			if item["method"] != "GET" || item["path"] != "/__aipublic__/group/list" {
				t.Fatalf("group-list head: %v", item)
			}
		}
		if item["id"] == "api-key-add" {
			body, ok := item["requestBody"].(map[string]any)
			if !ok {
				t.Fatalf("api-key-add requestBody: %v", item)
			}
			if body["contentType"] != "application/json" {
				t.Fatalf("api-key-add contentType: %v", body)
			}
			example, ok := body["example"].(map[string]any)
			if !ok || example["targetUsername"] != "huanmin" {
				t.Fatalf("api-key-add example: %v", body)
			}
		}
		if item["id"] == "api-key-list" {
			if _, hasBody := item["requestBody"]; hasBody {
				t.Fatalf("GET items must omit requestBody: %v", item)
			}
		}
	}
}

func TestExternalSourceLifecycle(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	// Create → 201 with item + one-shot token.
	body := `{"name":"来源A","scopes":["juhe_ai_public:group_list:read","juhe_ai_public:group_list:read"],` +
		`"rateLimits":[{"windowSeconds":60,"maxRequests":10}],"notes":"备注"}`
	code, created, headers := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", body)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("secret headers: %v", headers)
	}
	createdData := dataMap(t, created)
	item := createdData["item"].(map[string]any)
	token := createdData["token"].(map[string]any)
	sourceID := item["id"].(string)
	if !strings.HasPrefix(sourceID, "extsrc_") || item["name"] != "来源A" || item["isBuiltIn"] != false {
		t.Fatalf("created item: %v", item)
	}
	scopes := item["scopes"].([]any)
	if len(scopes) != 1 || scopes[0] != "juhe_ai_public:group_list:read" {
		t.Fatalf("scopes must dedupe: %v", item)
	}
	tokenValue := token["token"].(string)
	if !strings.HasPrefix(tokenValue, "juis_") || len(tokenValue) < 40 {
		t.Fatalf("token: %v", token)
	}
	primaryToken := item["primaryToken"].(map[string]any)
	if primaryToken["id"] != token["id"] || primaryToken["tokenPrefix"] != token["tokenPrefix"] {
		t.Fatalf("primary token: %v %v", item, token)
	}
	if token["tokenPrefix"] != tokenValue[:8] {
		t.Fatalf("token prefix: %v", token)
	}

	// Duplicate name (case-insensitive), distinct payload to pass the guard.
	code, duplicate, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources",
		`{"name":"来源a","notes":"different"}`)
	if code != http.StatusBadRequest || duplicate["message"] != "来源系统名称已存在" {
		t.Fatalf("duplicate name: %d %v", code, duplicate)
	}

	// Guarded identical duplicate → 409.
	code, _, _ = env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", body)
	if code != http.StatusConflict {
		t.Fatalf("guarded duplicate: %d", code)
	}

	// Validation errors mirror the repository messages.
	code, badScope, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources",
		`{"name":"来源B","scopes":["not:a:scope"]}`)
	if code != http.StatusBadRequest || badScope["message"] != "来源系统 scope 不受支持：not:a:scope" {
		t.Fatalf("bad scope: %d %v", code, badScope)
	}
	code, badWindow, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources",
		`{"name":"来源B","rateLimits":[{"windowSeconds":0,"maxRequests":10}]}`)
	if code != http.StatusBadRequest || badWindow["message"] != "限频窗口不能小于 1 秒" {
		t.Fatalf("bad window: %d %v", code, badWindow)
	}
	code, badExpires, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources",
		`{"name":"来源B","expiresAt":"2026-01-01 00:00:00"}`)
	if code != http.StatusBadRequest || badExpires["message"] != "过期时间无效" {
		t.Fatalf("bad expires: %d %v", code, badExpires)
	}

	// Detail with token summaries.
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	summary := dataMap(t, detail)
	if code != 200 || summary["tokenCount"] != float64(1) || summary["activeTokenCount"] != float64(1) {
		t.Fatalf("detail: %d %v", code, detail)
	}
	tokens := summary["tokens"].([]any)
	if len(tokens) != 1 || tokens[0].(map[string]any)["name"] != "来源A 生产 Token" {
		t.Fatalf("token summaries: %v", summary)
	}
	sourceUpdatedAt := summary["updatedAt"].(string)

	// List with keyword/status filters.
	code, list, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?keyword=来源", "")
	items := dataMap(t, list)["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}
	code, miss, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?keyword=不存在", "")
	if code != 200 || len(dataMap(t, miss)["items"].([]any)) != 0 {
		t.Fatalf("keyword miss: %d %v", code, miss)
	}
	code, disabledList, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?status=disabled", "")
	if code != 200 || len(dataMap(t, disabledList)["items"].([]any)) != 0 {
		t.Fatalf("status filter: %d %v", code, disabledList)
	}

	// Patch: rename + disable, tokens sync to disabled.
	code, patched, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+sourceUpdatedAt+`","name":"来源A2","status":"disabled"}`)
	if code != 200 {
		t.Fatalf("patch: %d %v", code, patched)
	}
	mutation := dataMap(t, patched)
	if mutation["id"] != sourceID || mutation["updatedAt"].(string) == sourceUpdatedAt {
		t.Fatalf("mutation: %v", mutation)
	}
	code, after, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	afterSummary := dataMap(t, after)
	if afterSummary["status"] != "disabled" || afterSummary["activeTokenCount"] != float64(0) {
		t.Fatalf("after disable: %v", afterSummary)
	}
	if afterSummary["tokens"].([]any)[0].(map[string]any)["status"] != "disabled" {
		t.Fatalf("token status must sync: %v", afterSummary)
	}

	// Stale expectedUpdatedAt → 409.
	code, stale, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+sourceUpdatedAt+`","status":"active"}`)
	if code != http.StatusConflict || stale["message"] != externalConflictMessage {
		t.Fatalf("stale patch: %d %v", code, stale)
	}
	// No-op patch is rejected by the refine.
	code, noop, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+mutation["updatedAt"].(string)+`"}`)
	if code != http.StatusBadRequest || noop["message"] != "请提供要修改的来源配置字段" {
		t.Fatalf("noop patch: %d %v", code, noop)
	}
	// Unknown id → 404.
	code, unknown, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/extsrc_nope",
		`{"expectedUpdatedAt":"`+mutation["updatedAt"].(string)+`","status":"active"}`)
	if code != http.StatusNotFound || unknown["message"] != "来源系统不存在" {
		t.Fatalf("unknown patch: %d %v", code, unknown)
	}

	// Re-enable syncs tokens back to active.
	code, reEnabled, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+mutation["updatedAt"].(string)+`","status":"active"}`)
	if code != 200 {
		t.Fatalf("re-enable: %d %v", code, reEnabled)
	}
	sourceUpdatedAt = dataMap(t, reEnabled)["updatedAt"].(string)

	// Token lifecycle.
	code, newToken, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources/"+sourceID+"/tokens",
		`{"name":"T2","expiresAt":"2030-01-01T00:00:00.000Z"}`)
	if code != http.StatusCreated {
		t.Fatalf("token create: %d %v", code, newToken)
	}
	tokenData := dataMap(t, newToken)["token"].(map[string]any)
	tokenID := tokenData["id"].(string)
	if !strings.HasPrefix(tokenID, "exttok_") || tokenData["expiresAt"] != "2030-01-01T00:00:00.000Z" {
		t.Fatalf("new token: %v", tokenData)
	}

	// Secret reveal returns the same plaintext with no-store headers.
	code, secret, headers := env.do(t, http.MethodGet,
		"/__aisys__/api/external-integration-sources/"+sourceID+"/tokens/"+tokenID+"/secret", "")
	if code != 200 || dataMap(t, secret)["token"] != tokenData["token"] {
		t.Fatalf("secret: %d %v", code, secret)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("secret headers: %v", headers)
	}
	code, missingSecret, _ := env.do(t, http.MethodGet,
		"/__aisys__/api/external-integration-sources/"+sourceID+"/tokens/exttok_missing/secret", "")
	if code != http.StatusNotFound || missingSecret["message"] != "Token 不存在" {
		t.Fatalf("missing secret: %d %v", code, missingSecret)
	}

	// Token patch with status change to revoked.
	detailCode, tokenDetail, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	tokenUpdatedAt := dataMap(t, tokenDetail)["tokens"].([]any)[0].(map[string]any)["updatedAt"].(string)
	if detailCode != 200 {
		t.Fatalf("token detail: %d %v", detailCode, tokenDetail)
	}
	// Find T2's updatedAt (tokens ordered created DESC → T2 first).
	code, revoked, _ := env.do(t, http.MethodPatch,
		"/__aisys__/api/external-integration-sources/"+sourceID+"/tokens/"+tokenID,
		`{"expectedUpdatedAt":"`+tokenUpdatedAt+`","status":"revoked"}`)
	if code != 200 {
		t.Fatalf("token revoke: %d %v", code, revoked)
	}
	code, afterRevoke, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	var t2 map[string]any
	for _, entry := range dataMap(t, afterRevoke)["tokens"].([]any) {
		candidate := entry.(map[string]any)
		if candidate["id"] == tokenID {
			t2 = candidate
		}
	}
	if t2 == nil || t2["status"] != "revoked" || t2["revokedAt"] == "" {
		t.Fatalf("revoked token: %v", t2)
	}

	// Delete the source (Node DELETE carries expectedUpdatedAt in the body);
	// tokens cascade.
	code, deleted, _ := env.do(t, http.MethodDelete, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+sourceUpdatedAt+`"}`)
	if code != http.StatusNoContent || deleted != nil {
		t.Fatalf("delete: %d %v", code, deleted)
	}
	code, gone, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	if code != http.StatusNotFound || gone["message"] != "来源系统不存在" {
		t.Fatalf("after delete: %d %v", code, gone)
	}
	if env.count(t, `SELECT COUNT(*) FROM external_integration_source_tokens WHERE source_ref_id = ?`, sourceID) != 0 {
		t.Fatal("tokens must cascade")
	}
	if !env.sink.has("external_integration_sources.create") ||
		!env.sink.has("external_integration_sources.update") ||
		!env.sink.has("external_integration_sources.create_token") ||
		!env.sink.has("external_integration_sources.update_token") ||
		!env.sink.has("external_integration_sources.delete") {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	_ = store
}

func TestExternalBuiltInTestToken(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	// Seed the built-in source/token rows the way deployment provisioning does.
	env.exec(t, `INSERT INTO external_integration_sources (id, name, status, scopes_json, rate_limits_json, expires_at, notes, last_used_at, created_at, updated_at)
		VALUES ('extsrc_builtin_test', '内置测试来源', 'active', '["juhe_ai_public:group_list:read"]', '[{"windowSeconds":60,"maxRequests":10}]', NULL, NULL, NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO external_integration_source_tokens (id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at)
		VALUES ('exttok_builtin_test', 'extsrc_builtin_test', '内置测试 Token', 'hash', NULL, 'juis_builtin', 'suffix', 'active', '["juhe_ai_public:group_list:read"]', NULL, NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', NULL)`)

	// Built-in edit restrictions.
	code, restricted, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/extsrc_builtin_test",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","name":"改名"}`)
	if code != http.StatusBadRequest || restricted["message"] != "内置测试 Token 只支持启用或停用，不支持编辑名称、授权范围、限频、到期时间或备注" {
		t.Fatalf("built-in rename: %d %v", code, restricted)
	}
	code, deleted, _ := env.do(t, http.MethodDelete, "/__aisys__/api/external-integration-sources/extsrc_builtin_test",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`)
	if code != http.StatusBadRequest || deleted["message"] != "内置测试 Token 不支持删除" {
		t.Fatalf("built-in delete: %d %v", code, deleted)
	}
	code, tokenEdit, _ := env.do(t, http.MethodPatch,
		"/__aisys__/api/external-integration-sources/extsrc_builtin_test/tokens/exttok_builtin_test",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","name":"改名"}`)
	if code != http.StatusBadRequest || tokenEdit["message"] != "内置测试 Token 不支持编辑" {
		t.Fatalf("built-in token edit: %d %v", code, tokenEdit)
	}
	code, newToken, _ := env.do(t, http.MethodPost,
		"/__aisys__/api/external-integration-sources/extsrc_builtin_test/tokens", `{"name":"T"}`)
	if code != http.StatusBadRequest || newToken["message"] != "内置测试 Token 不支持新增 Token" {
		t.Fatalf("built-in token create: %d %v", code, newToken)
	}

	// Status-only patch is allowed.
	code, disabled, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/extsrc_builtin_test",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","status":"disabled"}`)
	if code != 200 {
		t.Fatalf("built-in disable: %d %v", code, disabled)
	}
	code, enabled, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/extsrc_builtin_test",
		`{"expectedUpdatedAt":"`+dataMap(t, disabled)["updatedAt"].(string)+`","status":"active"}`)
	if code != 200 {
		t.Fatalf("built-in enable: %d %v", code, enabled)
	}

	// Reset mints a fresh active token and reveals it.
	code, reset, headers := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources/built-in-test-token/reset", "")
	if code != 200 {
		t.Fatalf("reset: %d %v", code, reset)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("reset headers: %v", headers)
	}
	token := dataMap(t, reset)["token"].(map[string]any)
	if token["id"] != "exttok_builtin_test" || token["status"] != nil {
		t.Fatalf("reset token: %v", token)
	}
	if _, ok := token["token"].(string); !ok || !strings.HasPrefix(token["token"].(string), "juis_") {
		t.Fatalf("reset token value: %v", token)
	}
	// Reset twice yields different tokens (guard passes different result? identical payload → dedupe).
	// Use a distinct second reset through the store to avoid the guard.
	store, err := NewExternalStore(env.db, false, nil, nil, nil, "test-crypto-secret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ResetBuiltInTestToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Token == token["token"].(string) {
		t.Fatal("reset must mint a new token")
	}
	// The secret endpoint decrypts the latest reset token.
	code, secret, _ := env.do(t, http.MethodGet,
		"/__aisys__/api/external-integration-sources/extsrc_builtin_test/tokens/exttok_builtin_test/secret", "")
	if code != 200 || dataMap(t, secret)["token"] != second.Token {
		t.Fatalf("builtin secret: %d %v", code, secret)
	}
	if !env.sink.has("external_integration_sources.reset_builtin_test_token") {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
}
