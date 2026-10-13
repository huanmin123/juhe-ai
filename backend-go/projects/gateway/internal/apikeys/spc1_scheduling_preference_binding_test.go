// 调度偏好独立实体 P1——API Key 绑定五写入口矩阵测试（接口契约「API Key
// 绑定契约（五写入口矩阵）」；v2 用户裁决修订 2026-10-13 后无停用态）。
// 覆盖：管理/个人创建（缺省/null/显式/非法/跨 owner/不存在/空字符串）、
// 管理/个人 PATCH（缺失保持/显式 null 回默认/显式 ID/同值 no-op/chat 拒绝/
// 默认 Key 可换偏好）、公开面 add/update 的 body 解析同规则。运行态六态解析
// 与 preauth 投影在 gatewayruntimecache / gatewaypreauth 各自包内覆盖。
package apikeys

import (
	"net/http"
	"testing"
	"time"
)

// seedSchedulingPreference 插入一条调度偏好行（built_in 可控；enabled 已随
// v2 用户裁决修订第 4 条退役，行不再携带该列）。
func seedSchedulingPreference(t *testing.T, env *testEnv, id, ownerID, name, chatMode, configJSON string, builtIn int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, ownerID, name, chatMode, configJSON, builtIn, now, now)
}

// seededSpeedFirstConfig 是启用速度偏好的典型配置（合法范围值）。
const seededSpeedFirstConfig = `{"slowTriggerCount":4,"slowWindowSeconds":180,"recoverySuccessCount":3,"probeIntervalSeconds":30,"degradedTtlSeconds":300,"maxFirstByteRetriesPerRequest":2,"totalTimeDeadlineSeconds":150,"compactionTotalTimeDeadlineSeconds":300}`

func patchPreferenceBody(revision string, preferenceValue string) string {
	field := ""
	if preferenceValue != "" {
		field = `"schedulingPreferenceId":` + preferenceValue + `,`
	}
	return `{"expectedRevision":"` + revision + `",` + field + `"description":"patched"}`
}

func TestSPC1CreateDefaultsToBuiltinPreference(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	// 内置行由 maintenance seed/ensure-schema 恢复式补种、gateway 启动三臂校
	// 验兜底；测试库按同一权威值补种，供绑定投影 join 回显。
	seedSchedulingPreference(t, env, BuiltinSchedulingPreferenceID, "", BuiltinSchedulingPreferenceName, "cost_first", "", 1)

	// 缺省 → 内置固定 ID。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"absent"}`)
	if code != http.StatusCreated {
		t.Fatalf("create absent: %d %v", code, created)
	}
	absentID := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = ?`, absentID); got != BuiltinSchedulingPreferenceID {
		t.Fatalf("absent preference: got %q want builtin %q", got, BuiltinSchedulingPreferenceID)
	}

	// 显式 null → 内置固定 ID。
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"explicit-null","schedulingPreferenceId":null}`)
	if code != http.StatusCreated {
		t.Fatalf("create null: %d %v", code, created)
	}
	nullID := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = ?`, nullID); got != BuiltinSchedulingPreferenceID {
		t.Fatalf("null preference: got %q want builtin", got)
	}

	// 详情投影：三字段回显内置行。
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+nullID, "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, detail)
	}
	data := dataMap(t, detail)
	if data["schedulingPreferenceId"] != BuiltinSchedulingPreferenceID ||
		data["schedulingPreferenceName"] != BuiltinSchedulingPreferenceName ||
		data["schedulingPreferenceBuiltIn"] != true {
		t.Fatalf("detail projection: %v", data)
	}
}

func TestSPC1CreateExplicitPreferenceArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedSchedulingPreference(t, env, "sp-own-speed", adminID, "自有速度", "speed_first", seededSpeedFirstConfig, 0)
	seedSchedulingPreference(t, env, "sp-foreign", "other-owner", "他人偏好", "speed_first", seededSpeedFirstConfig, 0)

	// 同 owner 启用自定义偏好 → 持久化。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"speed","schedulingPreferenceId":"sp-own-speed"}`)
	if code != http.StatusCreated {
		t.Fatalf("create custom: %d %v", code, created)
	}
	speedID := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = ?`, speedID); got != "sp-own-speed" {
		t.Fatalf("custom preference: got %q", got)
	}

	for name, body := range map[string]string{
		"empty-string": `{"name":"bad-empty","schedulingPreferenceId":"  "}`,
		"number":       `{"name":"bad-number","schedulingPreferenceId":42}`,
		"missing-row":  `{"name":"bad-missing","schedulingPreferenceId":"sp-nope"}`,
		"cross-owner":  `{"name":"bad-foreign","schedulingPreferenceId":"sp-foreign"}`,
	} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %v", name, code, payload)
		}
		if message, _ := payload["message"].(string); message != SchedulingPreferenceUnavailableMessage {
			t.Fatalf("%s: message %q want %q", name, message, SchedulingPreferenceUnavailableMessage)
		}
	}
}

func TestSPC1PatchPreferenceArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedSchedulingPreference(t, env, "sp-patch-speed", adminID, "补丁速度", "speed_first", seededSpeedFirstConfig, 0)

	// 历史 NULL Key：PATCH 缺失字段 → 保持 NULL（显式 null → 内置）。
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, created_at, updated_at)
		VALUES ('key-hist', ?, 'rs-default', '历史', 'hash-hist', 'prefix', 'suffix', 'sealed', 'active', 0, 'general', '2019-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`, adminID)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist", `{"expectedRevision":"2020-01-01T00:00:00.000Z","description":"no-pref"}`)
	if code != http.StatusOK {
		t.Fatalf("patch missing field: %d %v", code, patched)
	}
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = 'key-hist'`); got != "" {
		t.Fatalf("missing field must keep NULL, got %q", got)
	}
	// 缺失字段臂同样推进了 revision，后续臂读最新值（CAS 契约沿用）。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist",
		`{"expectedRevision":"`+dataMap(t, patched)["revision"].(string)+`","schedulingPreferenceId":null}`)
	if code != http.StatusOK {
		t.Fatalf("patch null: %d %v", code, patched)
	}
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = 'key-hist'`); got != BuiltinSchedulingPreferenceID {
		t.Fatalf("explicit null must persist builtin, got %q", got)
	}
	// rowPatch 三字段（rowPatchOf 断言在 lifecycle 测试里有型；这里直接断键）。
	rowPatch := patched["data"].(map[string]any)["rowPatch"].(map[string]any)
	if rowPatch["schedulingPreferenceId"] != BuiltinSchedulingPreferenceID ||
		rowPatch["schedulingPreferenceName"] != BuiltinSchedulingPreferenceName ||
		rowPatch["schedulingPreferenceBuiltIn"] != true {
		t.Fatalf("rowPatch: %v", rowPatch)
	}

	// 显式 ID 变更：changedFields + rowPatch + 操作日志标签 + validation 失效。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist", `{"expectedRevision":"`+dataMap(t, patched)["revision"].(string)+`","schedulingPreferenceId":"sp-patch-speed"}`)
	if code != http.StatusOK {
		t.Fatalf("patch custom: %d %v", code, patched)
	}
	fields := changedFieldsOf(t, patched)
	if len(fields) != 1 || !hasField(fields, "schedulingPreferenceId") {
		t.Fatalf("changedFields: %v", fields)
	}
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = 'key-hist'`); got != "sp-patch-speed" {
		t.Fatalf("custom bind: got %q", got)
	}
	if !env.inval.has(ReasonAPIKeyUpdated) {
		t.Fatalf("validation invalidation missing: %v", env.inval)
	}
	logged := false
	for _, entry := range env.sink.entries {
		for _, change := range entry.Changes {
			if change.Field == "schedulingPreferenceId" && change.Label == "调度偏好" {
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
		`{"expectedRevision":"`+revision+`","schedulingPreferenceId":"sp-patch-speed"}`)
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

	// 非法目标统一 400（停用目标分支已随 v2 第 4 条退役）。
	for name, value := range map[string]string{
		"missing":   `"sp-nope"`,
		"empty":     `" "`,
		"nonstring": `7`,
	} {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-hist", patchPreferenceBody(revision, value))
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %v", name, code, payload)
		}
		if message, _ := payload["message"].(string); message != SchedulingPreferenceUnavailableMessage {
			t.Fatalf("%s: message %q", name, message)
		}
	}
}

func TestSPC1PatchChatKeyRejectsPreferenceAndDefaultKeyAllowsIt(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedSchedulingPreference(t, env, "sp-chat-target", adminID, "聊天目标", "speed_first", seededSpeedFirstConfig, 0)

	seedGuardKey(t, env, "key-chat", adminID, "AI 对话", "chat", 0)
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-chat", patchPreferenceBody("2020-01-01T00:00:00.000Z", `"sp-chat-target"`))
	if code != http.StatusBadRequest {
		t.Fatalf("chat patch: %d %v", code, payload)
	}
	if message, _ := payload["message"].(string); message != SchedulingPreferenceUnavailableMessage {
		t.Fatalf("chat patch message: %q", message)
	}
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = 'key-chat'`); got != "" {
		t.Fatalf("chat key column must stay NULL after ID rejection, got %q", got)
	}

	// 显式 null 一并拒绝：chat Key 列恒 NULL 强不变量，不走普通 Key 的
	// 「null 回默认内置 ID」矩阵，统一同型 400 且列保持 NULL。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-chat", patchPreferenceBody("2020-01-01T00:00:00.000Z", `null`))
	if code != http.StatusBadRequest {
		t.Fatalf("chat patch null: %d %v", code, payload)
	}
	if message, _ := payload["message"].(string); message != SchedulingPreferenceUnavailableMessage {
		t.Fatalf("chat patch null message: %q", message)
	}
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = 'key-chat'`); got != "" {
		t.Fatalf("chat key column must stay NULL after null rejection, got %q", got)
	}
	// 两次拒绝都不推进 revision（无任何写入），同版本仍可用。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/api-keys/key-chat", "")
	if code != http.StatusOK {
		t.Fatalf("chat detail: %d %v", code, payload)
	}
	if revision := dataMap(t, payload)["revision"].(string); revision != "2020-01-01T00:00:00.000Z" {
		t.Fatalf("rejected chat patches must not bump revision, got %q", revision)
	}

	// 默认 Key「不允许更换策略路由」不扩展到偏好字段（契约明示）：换偏好放
	// 行、换到其他策略仍拒绝。
	seedGuardKey(t, env, "key-default", adminID, "默认", "general", 1)
	seedOtherStrategy(t, env, adminID, "rs-alternate", "备用策略")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-default", patchPreferenceBody("2020-01-01T00:00:00.000Z", `"sp-chat-target"`))
	if code != http.StatusOK {
		t.Fatalf("default-key preference patch: %d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = 'key-default'`); got != "sp-chat-target" {
		t.Fatalf("default-key bind: got %q", got)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-default",
		`{"expectedRevision":"`+dataMap(t, payload)["revision"].(string)+`","routeStrategyId":"rs-alternate"}`)
	if code == http.StatusOK {
		t.Fatalf("default-key strategy change must stay rejected")
	}
}
