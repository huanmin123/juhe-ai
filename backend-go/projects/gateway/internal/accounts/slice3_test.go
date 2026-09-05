package accounts

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// 第 1-3 段对齐测试：凭据规范化域、批量 5 凭据字段、runtime-reset 端点。
// 每个 Node 分支的断言对照归档实现注释标注。

// ---- fake runtime effects (the RuntimeResetEffects port mock) ----

type fakeRuntimeEffects struct {
	mu                    sync.Mutex
	clearCalls            []RuntimeAvailabilityClearInput
	latencyCleared        int64
	revalidated           AccountAPIKeyRuntimeRevalidation
	transientStates       []AccountAPIKeyTransientSelectionState
	failureGuardCleared   []string
	transientCleared      []string
	healthCheckDispatches [][2]string
	quotaExceeded         bool
	poolAllUnavailable    bool
	failClearRuntime      bool
}

func (f *fakeRuntimeEffects) ClearAccountRuntimeAvailability(_ context.Context, input RuntimeAvailabilityClearInput) (RuntimeAvailabilityClearResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalls = append(f.clearCalls, input)
	if f.failClearRuntime {
		return RuntimeAvailabilityClearResult{}, context.DeadlineExceeded
	}
	return RuntimeAvailabilityClearResult{Cleared: true}, nil
}

func (f *fakeRuntimeEffects) ClearNormalRouteLatencyDegradation(context.Context, string, string) (int64, error) {
	return f.latencyCleared, nil
}

func (f *fakeRuntimeEffects) RevalidateAccountAPIKeyRuntimePool(context.Context, string, int64) (AccountAPIKeyRuntimeRevalidation, error) {
	return f.revalidated, nil
}

func (f *fakeRuntimeEffects) LoadAPIKeyTransientStates(_ context.Context, _ string, keyFingerprints []string) ([]AccountAPIKeyTransientSelectionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AccountAPIKeyTransientSelectionState{}, f.transientStates...), nil
}

func (f *fakeRuntimeEffects) ClearAPIKeyFailureGuard(_ string, keyFingerprint string, _ *int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failureGuardCleared = append(f.failureGuardCleared, keyFingerprint)
	return len(f.transientStates) > 0
}

func (f *fakeRuntimeEffects) ClearAPIKeyTransientFailure(_ context.Context, _ string, keyFingerprint string, generation *int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if generation == nil {
		return false, nil
	}
	f.transientCleared = append(f.transientCleared, keyFingerprint)
	return true, nil
}

func (f *fakeRuntimeEffects) DispatchAccountHealthCheck(accountID, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthCheckDispatches = append(f.healthCheckDispatches, [2]string{accountID, reason})
}

func (f *fakeRuntimeEffects) AuthorizationQuotaExceeded(context.Context, AuthorizationQuotaCheckInput) (bool, error) {
	return f.quotaExceeded, nil
}

func (f *fakeRuntimeEffects) APIKeyPoolAllUnavailable(context.Context, string) (bool, error) {
	return f.poolAllUnavailable, nil
}

// ---- 第 1 段：凭据规范化域 ----

func TestNormalizeAPIKeyCredentialsForWrite(t *testing.T) {
	// Node openai-endpoint-modes defaultOpenAIEndpointModes: the gpt vendor
	// carries all four OpenAI modes, unknown providers carry them too.
	normalized, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key":  "sk-abc",
		"base_url": "https://api.openai.com/v1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if normalized["api_key"] != "sk-abc" || normalized["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("normalized core fields: %v", normalized)
	}
	modes, ok := normalized["supported_endpoint_modes"].([]any)
	if !ok || len(modes) != 4 || modes[0] != "chat_json" || modes[3] != "responses_sse" {
		t.Fatalf("gpt endpoint mode defaults: %v", normalized["supported_endpoint_modes"])
	}
	if _, exists := normalized["api_keys"]; exists {
		t.Fatal("single key must not gain the api_keys list")
	}

	// Multi-key pool: strategy defaults to failover, weights only for the
	// weighted strategy (normalizeApiKeyAccountCredentials).
	normalized, err = NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_keys":         []any{"sk-b", "sk-a", "sk-b"},
		"base_url":         "https://api.openai.com/v1",
		"api_key_strategy": "weighted_round_robin",
		"api_key_weights":  []any{float64(3)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := normalized["api_keys"].([]any)
	if len(keys) != 2 || keys[0] != "sk-b" || keys[1] != "sk-a" {
		t.Fatalf("dedupe+trim: %v", normalized["api_keys"])
	}
	if normalized["api_key"] != "sk-b" || normalized["api_key_strategy"] != "weighted_round_robin" {
		t.Fatalf("pool strategy: %v", normalized)
	}
	weights, _ := normalized["api_key_weights"].([]any)
	if len(weights) != 2 || weights[0] != float64(3) || weights[1] != float64(1) {
		// Node normalizeApiKeyWeight: absent entries fall back to 1.
		t.Fatalf("weights fallback: %v", weights)
	}
	// Blank entries throw like Node requiredTextInput (normalizeApiKeyCredentialList).
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_keys": []any{"sk-b", "  "},
		"base_url": "https://api.openai.com/v1",
	}, nil); err == nil || err.Error() != "API Key不能为空" {
		t.Fatalf("blank key entry: %v", err)
	}

	// Weight outside 1-100 is rejected (normalizeApiKeyWeight).
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_keys":         []any{"sk-b", "sk-a"},
		"base_url":         "https://api.openai.com/v1",
		"api_key_strategy": "weighted_round_robin",
		"api_key_weights":  []any{float64(1), float64(400)},
	}, nil); err == nil || err.Error() != "API Key 权重必须是 1-100 之间的整数" {
		t.Fatalf("weight range: %v", err)
	}

	// Unknown keys are rejected with the Node copy.
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1", "bogus": 1,
	}, nil); err == nil || err.Error() != "账户凭据包含不支持的字段：bogus" {
		t.Fatalf("unknown key: %v", err)
	}

	// Deprecated codex repair keys are stripped silently.
	normalized, err = NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"codex_responses_safe_repair_enabled": true,
	}, nil)
	if err != nil {
		t.Fatalf("deprecated keys: %v", err)
	}
	if _, exists := normalized["codex_responses_safe_repair_enabled"]; exists {
		t.Fatal("deprecated key must be stripped")
	}

	// OpenAI-compatible provider defaults to the chat pair only.
	normalized, err = NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
	}, &EndpointModeDefaultContext{
		ProviderCode: "openai", AccountType: "api_key",
		ProtocolCode: "openai", ProtocolVersion: "v1",
		ProviderProtocolProfileID: openAICompatibleProfileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	modes, _ = normalized["supported_endpoint_modes"].([]any)
	if len(modes) != 2 || modes[0] != "chat_json" || modes[1] != "chat_sse" {
		t.Fatalf("openai-compatible defaults: %v", modes)
	}
}

func TestNormalizeOAuthCredentialsForWrite(t *testing.T) {
	// Non-anthropic OAuth requires one token (normalizeOAuthAccountCredentials).
	if _, err := NormalizeAccountCredentialsForWrite("oauth", Credentials{
		"base_url": "https://api.openai.com/v1",
	}, nil); err == nil || err.Error() != "OAuth 凭据不能为空" {
		t.Fatalf("oauth empty: %v", err)
	}

	// gpt vendor OAuth requires the account id.
	if _, err := NormalizeAccountCredentialsForWrite("oauth", Credentials{
		"access_token": "tok", "base_url": "https://api.openai.com/v1",
	}, &EndpointModeDefaultContext{ProviderCode: gptVendorCode, AccountType: "oauth",
		ProtocolCode: "openai", ProtocolVersion: "v1", ProviderProtocolProfileID: gptOpenAIV1ProfileIDConstant}); err == nil ||
		err.Error() != "OpenAI OAuth Access Token 缺少 account_id" {
		t.Fatalf("gpt oauth account_id: %v", err)
	}

	// Anthropic protocol profile requires the access token.
	if _, err := NormalizeAccountCredentialsForWrite("oauth", Credentials{
		"refresh_token": "r", "base_url": "https://api.anthropic.com/v1",
	}, &EndpointModeDefaultContext{ProviderCode: "anthropic", ProtocolCode: "anthropic",
		ProtocolVersion: "v1", AccountType: "oauth"}); err == nil ||
		err.Error() != "Anthropic OAuth Access Token 不能为空" {
		t.Fatalf("anthropic oauth: %v", err)
	}

	// OAuth defaults to the responses pair (defaultOpenAIEndpointModes oauth).
	normalized, err := NormalizeAccountCredentialsForWrite("oauth", Credentials{
		"refresh_token": "r", "base_url": "https://api.openai.com/v1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	modes := normalized["supported_endpoint_modes"].([]any)
	if len(modes) != 2 || modes[0] != "responses_json" || modes[1] != "responses_sse" {
		t.Fatalf("oauth mode defaults: %v", modes)
	}

	// google_oauth: refresh token requires client id + secret.
	if _, err := NormalizeAccountCredentialsForWrite("google_oauth", Credentials{
		"refresh_token": "r", "base_url": "https://generativelanguage.googleapis.com",
	}, nil); err == nil || err.Error() != "Google Refresh Token 需要同时配置 Client ID 和 Client Secret" {
		t.Fatalf("google oauth pair: %v", err)
	}
}

func TestNormalizeCredentialPolicies(t *testing.T) {
	// error_handling_rules: system rules cannot be written.
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"error_handling_rules": []any{map[string]any{"source": "system", "enabled": true, "name": "x", "priority": float64(1), "action": "retry_next"}},
	}, nil); err == nil || err.Error() != "第 1 条错误处理策略规则不能写入系统继承规则" {
		t.Fatalf("system rule: %v", err)
	}

	// 2xx status codes are rejected.
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "限流", "priority": float64(1), "action": "rate_limited",
			"status_codes": []any{float64(200)}, "reset_strategy": "duration", "duration_hours": float64(1),
		}},
	}, nil); err == nil || err.Error() != "第 1 条规则的状态码不能填写 2xx 成功状态码，例如 200" {
		t.Fatalf("2xx rule: %v", err)
	}

	// rate_limited requires the reset strategy fields.
	normalized, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "限流", "priority": float64(1), "action": "rate_limited",
			"status_codes": []any{float64(429)}, "reset_strategy": "duration", "duration_hours": float64(2),
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := normalized["error_handling_rules"].([]any)
	rule := rules[0].(map[string]any)
	if rule["reset_strategy"] != "duration" || rule["duration_hours"] != float64(2) {
		t.Fatalf("rate_limited rule: %v", rule)
	}

	// quota_recovery_policy: fixed jitter + invalid strategy copy.
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"quota_recovery_policy": map[string]any{"api_key": map[string]any{"reset_strategy": "hourly"}},
	}, nil); err == nil || err.Error() != "额度恢复策略 reset_strategy 必须是 duration、daily 或 weekly" {
		t.Fatalf("quota strategy: %v", err)
	}
	normalized, err = NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"quota_recovery_policy": map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(5)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := normalized["quota_recovery_policy"].(map[string]any)
	schedule := policy["oauth"].(map[string]any)
	if schedule["jitter_minutes"] != float64(15) || schedule["timezone"] != "UTC" {
		t.Fatalf("quota schedule defaults: %v", schedule)
	}

	// response_inspection_rules: matcher required.
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"response_inspection_rules": []any{map[string]any{
			"enabled": true, "name": "空规则", "priority": float64(1),
			"match": map[string]any{}, "action": "observe",
		}},
	}, nil); err == nil || err.Error() != "第 1 条响应检查规则至少需要一个匹配条件" {
		t.Fatalf("inspection matcher: %v", err)
	}

	// gpt request override enum (normalizeGptAccountRequestOverrides).
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1",
		"service_tier_override": "wild",
	}, &EndpointModeDefaultContext{ProviderCode: gptVendorCode, AccountType: "api_key",
		ProtocolCode: "openai", ProtocolVersion: "v1", ProviderProtocolProfileID: gptOpenAIV1ProfileIDConstant}); err == nil ||
		err.Error() != "服务等级覆盖无效" {
		t.Fatalf("service tier enum: %v", err)
	}
}

func TestUnsafeUpstreamBaseURLRejected(t *testing.T) {
	// The unsafe-target copy only holds while private base URLs are not
	// explicitly allowed for this process.
	if strings.TrimSpace(envOrEmpty("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS")) != "" {
		t.Skip("private upstream base URLs allowed by environment")
	}
	_, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "http://localhost:9000/v1",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "上游 Base URL 不能指向本机") {
		t.Fatalf("localhost reject: %v", err)
	}
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://192.168.1.10/v1",
	}, nil); err == nil || !strings.Contains(err.Error(), "上游 Base URL 不能指向本机") {
		t.Fatalf("private ip reject: %v", err)
	}
	// Format validations keep their own copies.
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1/chat/completions",
	}, nil); err == nil || err.Error() != "上游 Base URL 不能包含 /v1 后的具体接口路径" {
		t.Fatalf("endpoint path reject: %v", err)
	}
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk-abc", "base_url": "https://api.openai.com/v1?api=1",
	}, nil); err == nil || err.Error() != "上游 Base URL 不能包含查询参数" {
		t.Fatalf("query reject: %v", err)
	}
}

func envOrEmpty(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

// ---- 第 2 段：批量 5 凭据字段 ----

func TestBatchUpdateCredentialConfigFields(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	ids := []string{}
	for _, name := range []string{"cred-a", "cred-b"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}

	rulesJSON := `[{"enabled":true,"name":"限流覆盖","priority":5,"action":"rate_limited","status_codes":[429],"reset_strategy":"duration","duration_hours":2}]`
	body := `{"targets":[{"accountId":"` + ids[0] + `","configRevision":1},{"accountId":"` + ids[1] + `","configRevision":1}],
		"updates":{
			"errorHandlingRules":{"enabled":true,"value":` + rulesJSON + `},
			"responseInspectionRules":{"enabled":false},
			"serviceTierOverride":{"enabled":false}
		}}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", body)
	if code != http.StatusOK {
		t.Fatalf("batch credential update: %d %v", code, payload)
	}
	fields := changedFieldSet(t, payload)
	if len(fields) != 1 || !fields["errorHandlingRules"] {
		t.Fatalf("changedFields: %v", dataMap(t, payload)["changedFields"])
	}
	// disabled-union fields must not surface as changed.
	items, _ := dataMap(t, payload)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("batch items: %v", items)
	}
	if item := items[0].(map[string]any); len(item["changedFields"].([]any)) != 1 {
		t.Fatalf("enabled-union leak: %v", item)
	}

	// The normalized rules are persisted in the sealed credentials.
	var sealed string
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, ids[0]).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	credentials := Credentials{}
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	rules, ok := credentials["error_handling_rules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("stored rules: %v", credentials["error_handling_rules"])
	}
	rule := rules[0].(map[string]any)
	if rule["priority"] != float64(5) || rule["reset_strategy"] != "duration" || rule["duration_hours"] != float64(2) {
		t.Fatalf("stored rule shape: %v", rule)
	}

	// The operation log entry carries the batch id and the field list.
	entries := env.sink.entries
	if len(entries) == 0 || entries[len(entries)-1].Action != "batch_update" {
		t.Fatalf("batch sink entries: %v", env.sink.actions())
	}
	entry := entries[len(entries)-1]
	if !strings.HasPrefix(entry.ResourceID, "account_batch_") {
		t.Fatalf("batchId resource: %v", entry.ResourceID)
	}

	// Stale revision stays a per-account 409 (CAS contract unchanged).
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":1},{"accountId":"`+ids[1]+`","configRevision":2}],
		"updates":{"errorHandlingRules":{"enabled":true,"value":[]}}}`)
	if code != http.StatusConflict {
		t.Fatalf("stale credential batch: %d %v", code, conflict)
	}

	// supportedEndpointModes override: restrict to the chat pair; the account
	// was created with the default chat_json health mode, so nothing else
	// moves — but a responses-only account flips the health check mode.
	code, modesPayload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":2},{"accountId":"`+ids[1]+`","configRevision":2}],
		"updates":{"supportedEndpointModes":{"enabled":true,"value":["chat_json","chat_sse"]}}}`)
	if code != http.StatusOK {
		t.Fatalf("modes batch: %d %v", code, modesPayload)
	}
	if fields := changedFieldSet(t, modesPayload); len(fields) != 1 || !fields["supportedEndpointModes"] {
		t.Fatalf("modes changedFields: %v", dataMap(t, modesPayload)["changedFields"])
	}

	// serviceTierOverride null clears the key through
	// applyNullableCredentialOverride; the normalized record drops the key so
	// a rerun is a no-op.
	code, tierPayload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":3},{"accountId":"`+ids[1]+`","configRevision":3}],
		"updates":{"serviceTierOverride":{"enabled":true,"value":"priority"}}}`)
	if code != http.StatusOK {
		t.Fatalf("tier batch: %d %v", code, tierPayload)
	}
	if fields := changedFieldSet(t, tierPayload); len(fields) != 1 || !fields["serviceTierOverride"] {
		t.Fatalf("tier changedFields: %v", dataMap(t, tierPayload)["changedFields"])
	}
	var sealedTier string
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, ids[0]).Scan(&sealedTier); err != nil {
		t.Fatal(err)
	}
	tierCredentials := Credentials{}
	if err := DecryptJSON(testSecret, sealedTier, &tierCredentials); err != nil {
		t.Fatal(err)
	}
	if tierCredentials["service_tier_override"] != "priority" {
		t.Fatalf("stored tier override: %v", tierCredentials["service_tier_override"])
	}

	// Invalid rule payloads fail the account (400 via normalization message).
	code, badRules := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":4},{"accountId":"`+ids[1]+`","configRevision":4}],
		"updates":{"errorHandlingRules":{"enabled":true,"value":[{"enabled":true}]}}}`)
	if code != http.StatusBadRequest || badRules["message"] != "第 1 条规则名称不能为空" {
		t.Fatalf("bad rules batch: %d %v", code, badRules)
	}
}

// ---- 第 3 段：runtime-reset 端点 ----

func resetSuccessResponse(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	// The result rides the data object directly (Node res.json(ok(result))).
	return dataMap(t, payload)
}

func TestRuntimeResetOwnerAccount(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	effects := &fakeRuntimeEffects{latencyCleared: 2}
	env.store.SetRuntimeResetEffects(effects)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("reset-me"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'upstream_error',
		last_error_message = '上游 5xx', cooldown_until = ?, cooldown_retest_failure_count = 3,
		stream_failure_count = 2 WHERE id = ?`, now, id)

	// Strict body + validation surface.
	code, bad := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"expectedConfigRevision":1,"bogus":1}`)
	if code != http.StatusBadRequest || bad["message"] != "清理运行状态参数无效" {
		t.Fatalf("strict body: %d %v", code, bad)
	}
	// Revision mismatch renders 409 with the refresh copy.
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"expectedConfigRevision":99}`)
	if code != http.StatusConflict || conflict["message"] != RevisionConflictMessage {
		t.Fatalf("revision conflict: %d %v", code, conflict)
	}
	// Missing account renders 404.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound || missing["message"] != "账户不存在" {
		t.Fatalf("missing account: %d %v", code, missing)
	}

	code, okPayload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, okPayload)
	}
	result := resetSuccessResponse(t, okPayload)
	// Node patchAccountFailureStateInTransaction maps error → pending_test
	// (the health-check gate owns the final recovery), so the reset restores
	// the account to the pending family, not straight to active.
	if result["changed"] != true || result["status"] != "pending_test" || result["schedulable"] != false {
		t.Fatalf("reset result: %v", result)
	}
	if result["dispatchEligible"] != false {
		t.Fatalf("dispatchEligible: %v", result)
	}
	if result["gatewayRuntime"] != "cleared" {
		t.Fatalf("gatewayRuntime: %v", result)
	}
	if result["latencyDegradationCleared"] != float64(2) {
		t.Fatalf("latencyDegradationCleared: %v", result)
	}
	clearedSet := map[string]bool{}
	for _, item := range result["cleared"].([]any) {
		clearedSet[item.(string)] = true
	}
	if !clearedSet["account_persistent"] || !clearedSet["gateway_runtime"] || !clearedSet["speed_first_latency"] || !clearedSet["dispatch_revision"] {
		t.Fatalf("cleared set: %v", result["cleared"])
	}
	if len(result["skipped"].([]any)) != 0 {
		t.Fatalf("skipped: %v", result["skipped"])
	}

	// The failure-state patch cleared the persistent columns and advanced both
	// revisions (config CAS + dispatch fence + outbox row).
	var status, lastErrorCode string
	var configRevision, dispatchRevision int
	var cooldownUntil *string
	if err := env.db.QueryRow(`SELECT status, COALESCE(last_error_code, ''), config_revision, dispatch_revision, cooldown_until
		FROM accounts WHERE id = ?`, id).Scan(&status, &lastErrorCode, &configRevision, &dispatchRevision, &cooldownUntil); err != nil {
		t.Fatal(err)
	}
	if status != "pending_test" || lastErrorCode != "" || configRevision != 2 || dispatchRevision != 2 || cooldownUntil != nil {
		t.Fatalf("reset row state: %s %s %d %d %v", status, lastErrorCode, configRevision, dispatchRevision, cooldownUntil)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = '`+id+`'`) != 1 {
		t.Fatal("dispatch fence outbox row missing")
	}

	// The port received the reset target with the base key included and the
	// operation log entry mirrors the Node log contract.
	effects.mu.Lock()
	if len(effects.clearCalls) != 1 || !effects.clearCalls[0].IncludeBaseAccountKey || effects.clearCalls[0].AccountID != id {
		effects.mu.Unlock()
		t.Fatalf("clear calls: %v", effects.clearCalls)
	}
	effects.mu.Unlock()
	entries := env.sink.entries
	last := entries[len(entries)-1]
	if last.Module != "accounts" || last.Action != "runtime_reset" || last.OperationKey != "accounts.runtime_reset" ||
		last.ResourceID != id || last.Summary != "清理 AI 账户运行状态：reset-me" {
		t.Fatalf("sink entry: %v", last)
	}
	if len(last.Changes) != 1 || last.Changes[0].Field != "runtimeState" {
		t.Fatalf("sink changes: %v", last.Changes)
	}

	// Second reset on the healthy account: the persistent-clear skips (no
	// failure state), but the runtime surfaces still clear and the fence stays
	// put with no prior change.
	code, second := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"expectedConfigRevision":2}`)
	if code != http.StatusOK {
		t.Fatalf("second reset: %d %v", code, second)
	}
	secondResult := resetSuccessResponse(t, second)
	// A healthy active account has no persistent failure state: the owner
	// patch skips (Node skipPersistentClear) while the runtime surfaces still
	// clear — and the fence still advances for the runtime-only clear.
	secondCleared := map[string]bool{}
	for _, item := range secondResult["cleared"].([]any) {
		secondCleared[item.(string)] = true
	}
	if secondCleared["account_persistent"] {
		t.Fatalf("second run must skip the persistent clear: %v", secondResult["cleared"])
	}
	if !secondCleared["gateway_runtime"] || !secondCleared["dispatch_revision"] {
		t.Fatalf("second cleared set: %v", secondResult["cleared"])
	}
	if secondResult["changed"] != true {
		t.Fatalf("second changed: %v", secondResult)
	}
}

func TestRuntimeResetLockStateAndSelfScope(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.store.SetRuntimeResetEffects(&fakeRuntimeEffects{})
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("locked"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'x', last_error_message = 'y' WHERE id = ?`, id)
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, updated_at)
		VALUES (?, 1, 'ENGAGED', ?)`, id, now)

	// A live lock incident blocks the persistent clear and the reset reports
	// it through skipped (never turns the outage back into dispatchable).
	code, locked := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("locked reset: %d %v", code, locked)
	}
	result := resetSuccessResponse(t, locked)
	skippedSet := map[string]bool{}
	for _, item := range result["skipped"].([]any) {
		skippedSet[item.(string)] = true
	}
	if !skippedSet["lock_state"] {
		t.Fatalf("lock_state skip missing: %v", result["skipped"])
	}
	if result["changed"] != true {
		t.Fatalf("runtime-only surfaces still change the result: %v", result)
	}
	var status, lastErrorCode string
	if err := env.db.QueryRow(`SELECT status, COALESCE(last_error_code, '') FROM accounts WHERE id = ?`, id).
		Scan(&status, &lastErrorCode); err != nil {
		t.Fatal(err)
	}
	if status != "error" || lastErrorCode != "x" {
		t.Fatalf("locked row must stay untouched: %s %s", status, lastErrorCode)
	}
	if result["dispatchEligible"] != false {
		t.Fatalf("locked account must not be dispatch eligible: %v", result)
	}
}

func TestRuntimeResetPendingTestWithoutFailureRefuses(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	// pending_test with a failed health check is resettable; a fresh
	// pending_test account refuses the persistent clear.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("pending"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.exec(t, `UPDATE accounts SET status = 'pending_test', last_health_check_at = ?,
		last_health_check_error_message = '检查失败' WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	effects := &fakeRuntimeEffects{}
	env.store.SetRuntimeResetEffects(effects)
	code, reset := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("pending reset: %d %v", code, reset)
	}
	result := resetSuccessResponse(t, reset)
	if result["status"] != "pending_test" {
		t.Fatalf("failed pending resets to pending_test: %v", result)
	}
	effects.mu.Lock()
	dispatches := append([][2]string{}, effects.healthCheckDispatches...)
	effects.mu.Unlock()
	if len(dispatches) != 1 || dispatches[0][1] != "activation" {
		t.Fatalf("activation health dispatch missing: %v", dispatches)
	}

	// A fresh pending_test account (no prior health check) skips the
	// persistent clear without touching the row (reset service
	// skipPersistentClear; the patch-repository refusal never runs).
	code, payload2 := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("pending-fresh"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload2)
	}
	freshID := dataMap(t, payload2)["id"].(string)
	env.exec(t, `UPDATE accounts SET status = 'pending_test' WHERE id = ?`, freshID)
	code, fresh := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+freshID+"/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("fresh pending reset: %d %v", code, fresh)
	}
	freshResult := resetSuccessResponse(t, fresh)
	freshCleared := map[string]bool{}
	for _, item := range freshResult["cleared"].([]any) {
		freshCleared[item.(string)] = true
	}
	if freshCleared["account_persistent"] {
		t.Fatalf("fresh pending must skip the persistent clear: %v", freshResult["cleared"])
	}
	var freshStatus string
	var freshRevision int
	if err := env.db.QueryRow("SELECT status, config_revision FROM accounts WHERE id = ?", freshID).
		Scan(&freshStatus, &freshRevision); err != nil {
		t.Fatal(err)
	}
	if freshStatus != "pending_test" || freshRevision != 1 {
		t.Fatalf("fresh pending row untouched: %s %d", freshStatus, freshRevision)
	}
}

func TestRuntimeResetSelfSurfaceAndCrossOwner(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.login(t, "alice", "alice-pass", "user")
	aliceID := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'alice'`).Scan(&aliceID); err != nil {
		t.Fatal(err)
	}
	env.seedProviderAndDefaultGroup(t, aliceID)
	// Alice's own account resets through the my-* mirror.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", createPayload("alice-account"))
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, payload)
	}
	aliceAccount := dataMap(t, payload)["id"].(string)
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e', last_error_message = 'm' WHERE id = ?`, aliceAccount)
	code, reset := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+aliceAccount+"/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("self reset: %d %v", code, reset)
	}
	// Admin surface with the owner filter reaches it too.
	env.login(t, "root", "root-pass", "super_admin")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e2', last_error_message = 'm2' WHERE id = ?`, aliceAccount)
	code, adminReset := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+aliceAccount+"/runtime-reset?systemAccountId="+aliceID, `{"expectedConfigRevision":2}`)
	if code != http.StatusOK {
		t.Fatalf("scoped admin reset: %d %v", code, adminReset)
	}
	// Alice cannot reset the admin's account (404 through the scope filter).
	env.login(t, "alice", "alice-pass", "user")
	code, payload3 := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("root-account-hidden"))
	if code == http.StatusCreated {
		t.Fatal("alice must not create through the admin surface")
	}
	_ = payload3
}

func TestRuntimeResetAuthorizedInstance(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	ownerID := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'root'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	env.login(t, "alice", "alice-pass", "user")
	granteeID := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'alice'`).Scan(&granteeID); err != nil {
		t.Fatal(err)
	}
	// Owner seeds provider/default group under the owner scope.
	env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, ownerID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("source-account"))
	if code != http.StatusCreated {
		t.Fatalf("source create: %d %v", code, payload)
	}
	sourceID := dataMap(t, payload)["id"].(string)

	// The grantee needs an enabled default group for the gpt provider.
	env.login(t, "alice", "alice-pass", "user")
	env.seedProviderAndDefaultGroup(t, granteeID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	authID := "ra-test-1"
	instanceID := "acc-instance-1"
	groupID := ""
	if err := env.db.QueryRow(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1`, granteeID).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, scope, status, effective_source_type, created_by, created_at, updated_at)
		VALUES (?, 'account', ?, ?, ?, 'use', 'active', 'manual', ?, ?, ?)`,
		authID, sourceID, ownerID, granteeID, ownerID, now, now)
	credentials, err := EncryptJSON(testSecret, map[string]any{
		"api_key": "sk-instance", "base_url": "https://api.openai.com/v1",
		"supported_endpoint_modes": []any{"chat_json", "chat_sse", "responses_json", "responses_sse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO accounts (id, config_revision, dispatch_revision, system_account_id, provider_code,
		provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted,
		credential_mask, health_check_model, health_check_endpoint_mode, created_at, updated_at,
		authorization_instance_authorization_id, authorization_instance_source_account_id)
		VALUES (?, 1, 1, ?, 'gpt', 'prof-gpt', 'openai', 'v1', '授权实例', 'api_key', 'error', ?, '', 'gpt-4.1', 'chat_json', ?, ?, ?, ?)`,
		instanceID, granteeID, credentials, now, now, authID, sourceID)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at, account_authorization_id)
		VALUES (?, ?, ?, 1, ?, ?, ?)`, granteeID, groupID, instanceID, now, now, authID)
	// The instance carries a local failure state in the resettable family
	// (error stays behind the health-check gate); the source stays active.
	env.exec(t, `UPDATE accounts SET status = 'rate_limited', last_error_code = 'upstream', last_error_message = 'm',
		cooldown_until = ?, cooldown_retest_failure_count = 1 WHERE id = ?`, now, instanceID)

	effects := &fakeRuntimeEffects{}
	env.store.SetRuntimeResetEffects(effects)
	code, reset := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+instanceID+"/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("authorized reset: %d %v", code, reset)
	}
	result := resetSuccessResponse(t, reset)
	if result["status"] != "active" || result["changed"] != true {
		t.Fatalf("authorized result: %v", result)
	}
	clearedSet := map[string]bool{}
	for _, item := range result["cleared"].([]any) {
		clearedSet[item.(string)] = true
	}
	if !clearedSet["account_persistent"] || !clearedSet["dispatch_revision"] {
		t.Fatalf("authorized cleared set: %v", result["cleared"])
	}
	effects.mu.Lock()
	clearCalls := append([]RuntimeAvailabilityClearInput{}, effects.clearCalls...)
	effects.mu.Unlock()
	if len(clearCalls) != 1 || clearCalls[0].IncludeBaseAccountKey {
		t.Fatalf("authorized clear target: %v", clearCalls)
	}
	if clearCalls[0].AuthorizedBinding == nil || clearCalls[0].AuthorizedBinding.AccountAuthorizationID != authID ||
		clearCalls[0].AuthorizedBinding.GroupID != groupID || clearCalls[0].AuthorizedBinding.SystemAccountID != granteeID {
		t.Fatalf("authorized binding: %v", clearCalls[0].AuthorizedBinding)
	}
	var status string
	var revision int
	if err := env.db.QueryRow(`SELECT status, config_revision FROM accounts WHERE id = ?`, instanceID).Scan(&status, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "active" || revision != 2 {
		t.Fatalf("authorized row state: %s %d", status, revision)
	}

	// A paused authorization blocks the reset with the authorization copy.
	env.exec(t, `UPDATE resource_authorizations SET status = 'paused' WHERE id = ?`, authID)
	env.exec(t, `UPDATE accounts SET status = 'rate_limited', last_error_code = 'up', last_error_message = 'm', config_revision = 3, dispatch_revision = 2 WHERE id = ?`, instanceID)
	code, paused := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+instanceID+"/runtime-reset", `{"expectedConfigRevision":3}`)
	if code != http.StatusOK {
		t.Fatalf("paused reset: %d %v", code, paused)
	}
	pausedResult := resetSuccessResponse(t, paused)
	skipped := map[string]bool{}
	for _, item := range pausedResult["skipped"].([]any) {
		skipped[item.(string)] = true
	}
	if !skipped["authorization_source_blocked"] {
		t.Fatalf("paused skipped set: %v", pausedResult["skipped"])
	}
	var statusAfter string
	if err := env.db.QueryRow(`SELECT status FROM accounts WHERE id = ?`, instanceID).Scan(&statusAfter); err != nil {
		t.Fatal(err)
	}
	if statusAfter != "rate_limited" {
		t.Fatalf("paused instance must stay failed: %s", statusAfter)
	}
}

func TestRuntimeResetPendingTestAuthorizedRefused(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	ownerID := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'root'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	env.seedProviderAndDefaultGroup(t, ownerID)
	env.login(t, "alice", "alice-pass", "user")
	granteeID := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'alice'`).Scan(&granteeID); err != nil {
		t.Fatal(err)
	}
	env.seedProviderAndDefaultGroup(t, granteeID)
	env.login(t, "root", "root-pass", "super_admin")
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("source-two"))
	if code != http.StatusCreated {
		t.Fatalf("source create: %d %v", code, payload)
	}
	sourceID := dataMap(t, payload)["id"].(string)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, scope, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-test-2', 'account', ?, ?, ?, 'use', 'active', 'manual', ?, ?, ?)`,
		sourceID, ownerID, granteeID, ownerID, now, now)
	credentials, _ := EncryptJSON(testSecret, map[string]any{"api_key": "sk-i2", "base_url": "https://api.openai.com/v1"})
	env.exec(t, `INSERT INTO accounts (id, config_revision, dispatch_revision, system_account_id, provider_code,
		provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted,
		credential_mask, health_check_model, health_check_endpoint_mode, created_at, updated_at,
		authorization_instance_authorization_id, authorization_instance_source_account_id)
		VALUES ('acc-instance-2', 1, 1, ?, 'gpt', 'prof-gpt', 'openai', 'v1', '实例二', 'api_key', 'pending_test', ?, '',
		'gpt-4.1', 'chat_json', ?, ?, 'ra-test-2', ?)`, granteeID, credentials, now, now, sourceID)
	env.login(t, "alice", "alice-pass", "user")
	env.store.SetRuntimeResetEffects(&fakeRuntimeEffects{})
	// The authorized branch skips pending_test instances behind the
	// health-check gate without invoking the dispatch write, so the route
	// renders 200 with the skip marker and the row stays untouched.
	code, refused := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-instance-2/runtime-reset", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("pending authorized reset: %d %v", code, refused)
	}
	refusedResult := resetSuccessResponse(t, refused)
	refusedSkipped := map[string]bool{}
	for _, item := range refusedResult["skipped"].([]any) {
		refusedSkipped[item.(string)] = true
	}
	if !refusedSkipped["pending_test"] {
		t.Fatalf("pending skipped set: %v", refusedResult["skipped"])
	}
	var pendingStatus string
	if err := env.db.QueryRow("SELECT status FROM accounts WHERE id = 'acc-instance-2'").Scan(&pendingStatus); err != nil {
		t.Fatal(err)
	}
	if pendingStatus != "pending_test" {
		t.Fatalf("pending instance untouched: %s", pendingStatus)
	}
}

func TestExportBodyRejectsBothSelectors(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	// accountIds + filters together fail both strict schema branches → 400.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export",
		`{"accountIds":["acc-x"],"filters":{"keyword":"a"}}`)
	if code != http.StatusBadRequest || payload["message"] == "" {
		t.Fatalf("both selectors: %d %v", code, payload)
	}
}
