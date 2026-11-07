// 安全策略批A——API Key 绑定五写入口矩阵测试（API Key 安全策略与出站内容脱
// 敏设计 §4.2，镜像 spc1_scheduling_preference_binding_test.go 形态）。
// 覆盖：管理面创建（缺省/null/显式/内置行/非法/跨 owner/不存在/空字符串）、
// 管理 PATCH（缺失保持/显式 null 回默认/显式 ID/同值 no-op/非法目标/投影三
// 字段/操作日志标签/validation 失效）、无 purpose 豁免（chat Key 可绑定，与
// 调度偏好相反）。公开面 add/update 的 body 解析同规则在 aipublic 包
// parser 测试覆盖；运行态六态解析属后续批次。
package apikeys

import (
	"net/http"
	"testing"
	"time"
)

// seedSecurityPolicy 插入一条安全策略行（built_in 可控）。
func seedSecurityPolicy(t *testing.T, env *testEnv, id, ownerID, name, rulesJSON string, builtIn int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO security_policies (id, system_account_id, name, description, rules_json, built_in, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, ?, ?, ?)`, id, ownerID, name, rulesJSON, builtIn, now, now)
}

func patchPolicyBody(revision string, policyValue string) string {
	field := ""
	if policyValue != "" {
		field = `"securityPolicyId":` + policyValue + `,`
	}
	return `{"expectedRevision":"` + revision + `",` + field + `"description":"patched"}`
}

func TestSecPol1CreateDefaultsToBuiltinPolicy(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	// 内置行由 maintenance seed/ensure-schema 恢复式补种；测试库按同一权威值
	// 补种，供绑定投影 join 回显。
	seedSecurityPolicy(t, env, BuiltinSecurityPolicyID, "", BuiltinSecurityPolicyName, `{"rules":[]}`, 1)

	// 缺省 → 内置固定 ID。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"absent"}`)
	if code != http.StatusCreated {
		t.Fatalf("create absent: %d %v", code, created)
	}
	absentID := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = ?`, absentID); got != BuiltinSecurityPolicyID {
		t.Fatalf("absent policy: got %q want builtin %q", got, BuiltinSecurityPolicyID)
	}

	// 显式 null → 内置固定 ID。
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"explicit-null","securityPolicyId":null}`)
	if code != http.StatusCreated {
		t.Fatalf("create null: %d %v", code, created)
	}
	nullID := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = ?`, nullID); got != BuiltinSecurityPolicyID {
		t.Fatalf("null policy: got %q want builtin", got)
	}

	// 详情投影：三字段回显内置行。
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+nullID, "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, detail)
	}
	data := dataMap(t, detail)
	if data["securityPolicyId"] != BuiltinSecurityPolicyID ||
		data["securityPolicyName"] != BuiltinSecurityPolicyName ||
		data["securityPolicyBuiltIn"] != true {
		t.Fatalf("detail projection: %v", data)
	}
}

func TestSecPol1CreateExplicitPolicyArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedSecurityPolicy(t, env, BuiltinSecurityPolicyID, "", BuiltinSecurityPolicyName, `{"rules":[]}`, 1)
	seedSecurityPolicy(t, env, "secpol-own", adminID, "自有策略", `{"rules":[{"id":"r1","type":"phone","action":"mask"}]}`, 0)
	seedSecurityPolicy(t, env, "secpol-foreign", "other-owner", "他人策略", `{"rules":[]}`, 0)

	// 同 owner 自定义策略 → 持久化；显式绑定内置行 → 持久化内置 ID。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"custom","securityPolicyId":"secpol-own"}`)
	if code != http.StatusCreated {
		t.Fatalf("create custom: %d %v", code, created)
	}
	customID := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = ?`, customID); got != "secpol-own" {
		t.Fatalf("custom policy: got %q", got)
	}
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"builtin-bind","securityPolicyId":"`+BuiltinSecurityPolicyID+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("create builtin-bind: %d %v", code, created)
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = ?`, dataMap(t, created)["id"].(string)); got != BuiltinSecurityPolicyID {
		t.Fatalf("builtin bind: got %q", got)
	}

	for name, body := range map[string]string{
		"empty-string": `{"name":"bad-empty","securityPolicyId":"  "}`,
		"number":       `{"name":"bad-number","securityPolicyId":42}`,
		"missing-row":  `{"name":"bad-missing","securityPolicyId":"secpol-nope"}`,
		"cross-owner":  `{"name":"bad-foreign","securityPolicyId":"secpol-foreign"}`,
	} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %v", name, code, payload)
		}
		if message, _ := payload["message"].(string); message != SecurityPolicyUnavailableMessage {
			t.Fatalf("%s: message %q want %q", name, message, SecurityPolicyUnavailableMessage)
		}
	}
}

func TestSecPol1PatchPolicyArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedSecurityPolicy(t, env, "secpol-patch", adminID, "补丁策略", `{"rules":[]}`, 0)

	// 历史 NULL Key：PATCH 缺失字段 → 保持 NULL（显式 null → 内置）。
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, created_at, updated_at)
		VALUES ('key-hist', ?, 'rs-default', '历史', 'hash-hist', 'prefix', 'suffix', 'sealed', 'active', 0, 'general', '2019-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`, adminID)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist", `{"expectedRevision":"2020-01-01T00:00:00.000Z","description":"no-pol"}`)
	if code != http.StatusOK {
		t.Fatalf("patch missing field: %d %v", code, patched)
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = 'key-hist'`); got != "" {
		t.Fatalf("missing field must keep NULL, got %q", got)
	}
	// 缺失字段臂同样推进了 revision，后续臂读最新值（CAS 契约沿用）。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist",
		`{"expectedRevision":"`+dataMap(t, patched)["revision"].(string)+`","securityPolicyId":null}`)
	if code != http.StatusOK {
		t.Fatalf("patch null: %d %v", code, patched)
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = 'key-hist'`); got != BuiltinSecurityPolicyID {
		t.Fatalf("explicit null must persist builtin, got %q", got)
	}
	rowPatch := patched["data"].(map[string]any)["rowPatch"].(map[string]any)
	if rowPatch["securityPolicyId"] != BuiltinSecurityPolicyID ||
		rowPatch["securityPolicyName"] != BuiltinSecurityPolicyName ||
		rowPatch["securityPolicyBuiltIn"] != true {
		t.Fatalf("rowPatch: %v", rowPatch)
	}

	// 显式 ID 变更：changedFields + rowPatch + 操作日志标签 + validation 失效。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist", `{"expectedRevision":"`+dataMap(t, patched)["revision"].(string)+`","securityPolicyId":"secpol-patch"}`)
	if code != http.StatusOK {
		t.Fatalf("patch custom: %d %v", code, patched)
	}
	fields := changedFieldsOf(t, patched)
	if len(fields) != 1 || !hasField(fields, "securityPolicyId") {
		t.Fatalf("changedFields: %v", fields)
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = 'key-hist'`); got != "secpol-patch" {
		t.Fatalf("custom bind: got %q", got)
	}
	if !env.inval.has(ReasonAPIKeyUpdated) {
		t.Fatalf("validation invalidation missing: %v", env.inval)
	}
	logged := false
	for _, entry := range env.sink.entries {
		for _, change := range entry.Changes {
			if change.Field == "securityPolicyId" && change.Label == "安全策略" {
				logged = true
			}
		}
	}
	if !logged {
		t.Fatalf("operation log label missing")
	}

	// 同值 no-op：不 bump、changedFields 空、无新操作日志、无新失效。
	env.sink.entries = nil
	env.inval.mu.Lock()
	env.inval.reasons = nil
	env.inval.mu.Unlock()
	revision := dataMap(t, patched)["revision"].(string)
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist",
		`{"expectedRevision":"`+revision+`","securityPolicyId":"secpol-patch"}`)
	if code != http.StatusOK {
		t.Fatalf("no-op patch: %d %v", code, patched)
	}
	if got := dataMap(t, patched)["revision"].(string); got != revision {
		t.Fatalf("same-value patch bumped revision: %s -> %s", revision, got)
	}
	if fields := changedFieldsOf(t, patched); len(fields) != 0 {
		t.Fatalf("same-value changedFields: %v", fields)
	}
	if len(env.sink.entries) != 0 {
		t.Fatalf("same-value patch wrote operation logs: %v", env.sink.actions())
	}
	if env.inval.has(ReasonAPIKeyUpdated) {
		t.Fatalf("same-value patch invalidated validation cache")
	}

	// 非法目标统一 400（无停用态；非法类型 / 空字符串 / 不存在同型）。
	for name, value := range map[string]string{
		"missing":   `"secpol-nope"`,
		"empty":     `" "`,
		"nonstring": `7`,
	} {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist", patchPolicyBody(revision, value))
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %v", name, code, payload)
		}
		if message, _ := payload["message"].(string); message != SecurityPolicyUnavailableMessage {
			t.Fatalf("%s: message %q", name, message)
		}
	}
	// 跨 owner 同型 400。
	seedSecurityPolicy(t, env, "secpol-foreign", "other-owner", "他人策略", `{"rules":[]}`, 0)
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist", patchPolicyBody(revision, `"secpol-foreign"`))
	if code != http.StatusBadRequest {
		t.Fatalf("cross-owner patch: %d %v", code, payload)
	}
	if message, _ := payload["message"].(string); message != SecurityPolicyUnavailableMessage {
		t.Fatalf("cross-owner message: %q", message)
	}
}

// TestSecPol1NoPurposeExemption 锁定 §4.2 与调度偏好的差异：安全策略绑定无
// purpose 豁免——chat Key 可正常绑定/解绑（显式 null 回内置 ID）。
func TestSecPol1NoPurposeExemption(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedSecurityPolicy(t, env, "secpol-chat-target", adminID, "对话策略", `{"rules":[{"id":"r1","type":"phone","action":"placeholder"}]}`, 0)

	seedGuardKey(t, env, "key-chat", adminID, "AI 对话", "chat", 0)
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-chat", patchPolicyBody("2020-01-01T00:00:00.000Z", `"secpol-chat-target"`))
	if code != http.StatusOK {
		t.Fatalf("chat key policy patch must succeed (no purpose exemption): %d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = 'key-chat'`); got != "secpol-chat-target" {
		t.Fatalf("chat key bind: got %q", got)
	}
	// 显式 null 回默认。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-chat",
		`{"expectedRevision":"`+dataMap(t, payload)["revision"].(string)+`","securityPolicyId":null}`)
	if code != http.StatusOK {
		t.Fatalf("chat key null patch: %d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = 'key-chat'`); got != BuiltinSecurityPolicyID {
		t.Fatalf("chat key null reset: got %q", got)
	}
}
