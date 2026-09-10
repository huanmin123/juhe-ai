package policyreads

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 存量数据守卫与仓库层错误投影
// ---------------------------------------------------------------------------

func TestWeExternalTokenExpiresAtChangeAndGuards(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", `{"name":"到期变更来源"}`)
	sourceID := dataMap(t, created)["item"].(map[string]any)["id"].(string)
	code, tokenCreated, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources/"+sourceID+"/tokens",
		`{"name":"T1","expiresAt":"2030-01-01T00:00:00.000Z"}`)
	if code != http.StatusCreated {
		t.Fatalf("token create: %d %v", code, tokenCreated)
	}
	tokenID := dataMap(t, tokenCreated)["token"].(map[string]any)["id"].(string)
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	tokenUpdatedAt := dataMap(t, detail)["tokens"].([]any)[0].(map[string]any)["updatedAt"].(string)

	// expiresAt 合法值变更：从 2030 改到 2031（覆盖解析与变更投影分支）。
	code, changed, _ := env.do(t, http.MethodPatch,
		"/__aisys__/api/external-integration-sources/"+sourceID+"/tokens/"+tokenID,
		`{"expectedUpdatedAt":"`+tokenUpdatedAt+`","expiresAt":"2031-06-01T00:00:00.000Z"}`)
	if code != 200 {
		t.Fatalf("expiresAt 变更: %d %v", code, changed)
	}

	// token body scopes 项非字符串（解析层分支）。
	code, badScopes, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources/"+sourceID+"/tokens",
		`{"name":"T2","scopes":[1]}`)
	if code != http.StatusBadRequest || badScopes["message"] != localizedBadRequest {
		t.Fatalf("scopes 项非字符串: %d %v", code, badScopes)
	}

	// 删除未知来源（receipt 为 nil → 404）。
	code, missing, _ := env.do(t, http.MethodDelete, "/__aisys__/api/external-integration-sources/extsrc_missing",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`)
	if code != http.StatusNotFound || missing["message"] != "来源系统不存在" {
		t.Fatalf("删除未知来源: %d %v", code, missing)
	}
}

func TestWeExternalTokenSecretMissingCiphertextSurfaces500(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	_, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", `{"name":"无密文来源"}`)
	sourceID := dataMap(t, created)["item"].(map[string]any)["id"].(string)
	env.exec(t, `INSERT INTO external_integration_source_tokens (id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at)
		VALUES ('exttok_nosecret2', ?, '无密文', 'hash', NULL, 'prefix123', 'suffix456', 'active', '[]', NULL, NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', NULL)`, sourceID)

	// 仓库层把缺密文映射为校验错误；handler 对一切错误返回 500。
	deps := &ExternalDeps{Store: store, Auth: env.deps, Sink: env.sink}
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request.SetPathValue("id", sourceID)
	request.SetPathValue("tokenId", "exttok_nosecret2")
	recorder := httptest.NewRecorder()
	deps.tokenSecret(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("缺密文应 500, got %d", recorder.Code)
	}
}

func TestWeExternalResetBuiltinErrorBranch(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 未播种内置 Token 时重置：仓库校验错误 → 400。
	deps := &ExternalDeps{Store: store, Auth: env.deps, Sink: env.sink}
	request := httptest.NewRequest(http.MethodPost, "/x", nil)
	recorder := httptest.NewRecorder()
	deps.guardedResetBuiltInTestToken().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "内置测试 Token 不存在") {
		t.Fatalf("重置未播种内置 Token = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWeInspectionPatchProviderCodeNullAndCreateMatchNull(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	// match 显式 null：归一为空匹配后由 hasMatcher 拒绝。
	code, matchNull, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"空匹配","scopeType":"protocol","protocolCode":"openai","match":null,"action":"observe"}`)
	if code != http.StatusBadRequest || matchNull["message"] != "至少需要填写一个匹配条件" {
		t.Fatalf("match null: %d %v", code, matchNull)
	}

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"置空供应商","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`)
	policyID := dataMap(t, created)["id"].(string)
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	updatedAt := dataMap(t, detail)["updatedAt"].(string)

	// providerCode 显式 null：解析层接受（nullable optional）；协议层策略的
	// providerCode 本就是空，null 覆盖是无变化提交（200，不落库不记日志）。
	code, providerNull, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","providerCode":null}`)
	if code != http.StatusOK {
		t.Fatalf("providerCode null: %d %v", code, providerNull)
	}
}

func TestWeInspectionFindDetailEmptyID(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountInspection(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 空 id 直接返回未命中（白盒直调仓库层）。
	if detail, err := store.FindDetail(nil, "   "); err != nil || detail != nil {
		t.Fatalf("空 id = %v err = %v", detail, err)
	}
	deps := &InspectionDeps{Store: store, Auth: env.deps, Sink: env.sink}
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request.SetPathValue("id", "")
	recorder := httptest.NewRecorder()
	deps.detail(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("空 id 详情应 404, got %d", recorder.Code)
	}
}

// TestWeHandlersSurfaceErrorsWhenDatabaseClosed 用关闭数据库统一驱动各读路径
// 的内部错误分支（500 投影），避免为每个分支单独构造故障注入。
func TestWeHandlersSurfaceErrorsWhenDatabaseClosed(t *testing.T) {
	env := newPolicyTestEnv(t)
	inspectionStore := env.mountInspection(t)
	externalStore := env.mountExternal(t)
	oauthStore := env.mountOAuth(t, true, "https://id.example.com")
	env.login(t, "root", "root-pass", "super_admin")

	// 先关闭底层库：此后所有仓库调用立即失败。
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}

	inspectionDeps := &InspectionDeps{Store: inspectionStore, Auth: env.deps, Sink: env.sink}
	externalDeps := &ExternalDeps{Store: externalStore, Auth: env.deps, Sink: env.sink}
	oauthDeps := &OAuthDeps{Store: oauthStore, Auth: env.deps, OIDCEnabled: true, OIDCIssuer: "https://id.example.com"}

	get := func(handler http.Handler, pathValues map[string]string) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/x", nil)
		for key, value := range pathValues {
			request.SetPathValue(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	// 检查策略：列表 / 供应商选项 / 详情 的错误分支。
	if code := get(http.HandlerFunc(inspectionDeps.list), nil); code != http.StatusInternalServerError {
		t.Fatalf("inspection list = %d", code)
	}
	optionsRequest := httptest.NewRequest(http.MethodGet, "/x?protocolCode=openai&scopeType=provider", nil)
	optionsRecorder := httptest.NewRecorder()
	inspectionDeps.providerOptions(optionsRecorder, optionsRequest)
	if optionsRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("provider options = %d", optionsRecorder.Code)
	}
	if code := get(http.HandlerFunc(inspectionDeps.detail), map[string]string{"id": "rip_x"}); code != http.StatusInternalServerError {
		t.Fatalf("inspection detail = %d", code)
	}
	// 删除：仓库错误分支。
	if code := get(inspectionDeps.guardedDelete(), map[string]string{"id": "rip_x"}); code != http.StatusInternalServerError {
		t.Fatalf("inspection delete = %d", code)
	}

	// 外部来源：列表 / 详情 / Token 密文 / 客户端列表。
	if code := get(http.HandlerFunc(externalDeps.list), nil); code != http.StatusInternalServerError {
		t.Fatalf("external list = %d", code)
	}
	if code := get(http.HandlerFunc(externalDeps.detail), map[string]string{"id": "extsrc_x"}); code != http.StatusInternalServerError {
		t.Fatalf("external detail = %d", code)
	}
	if code := get(http.HandlerFunc(externalDeps.tokenSecret), map[string]string{"id": "extsrc_x", "tokenId": "exttok_x"}); code != http.StatusInternalServerError {
		t.Fatalf("token secret = %d", code)
	}

	// OAuth：列表 / 对接文档（含 FindClient 与 FindClientSecret 错误分支）。
	if code := get(http.HandlerFunc(oauthDeps.listClients), nil); code != http.StatusInternalServerError {
		t.Fatalf("oauth list = %d", code)
	}
	if code := get(http.HandlerFunc(oauthDeps.integrationPackage), map[string]string{"clientId": "juhe_x"}); code != http.StatusInternalServerError {
		t.Fatalf("oauth package = %d", code)
	}
}
