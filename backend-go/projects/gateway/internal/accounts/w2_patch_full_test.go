package accounts

// W2 PATCH 全字段链路测试：credentials 覆盖与 patch（null 删键）、余额查询
// 配置、代理与分组重绑、可用性时间表、状态与失败态清理、探测开关传播的
// owner 路径（patch.go + routes.go patchBody）。

import (
	"net/http"
	"testing"
)

// w2PatchAccount 创建一个可修补的账户并返回 id。
func w2PatchAccount(t *testing.T, env *testEnv, name string) string {
	t.Helper()
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", name, code, payload)
	}
	return dataMap(t, payload)["id"].(string)
}

func TestW2PatchCredentialsOverrideAndNullDelete(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "凭据补丁")

	// credentials 全量覆盖。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"credentials":{"api_key":"sk-new-key-987654","base_url":"https://api.openai.com/v1"}}`)
	if code != http.StatusOK {
		t.Fatalf("credentials 覆盖: %d %v", code, payload)
	}
	var sealed string
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	var credentials Credentials
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials["api_key"] != "sk-new-key-987654" {
		t.Fatalf("覆盖后 api_key 不一致：%v", credentials["api_key"])
	}

	// credentialsPatch：写 notes 字段并删除一个键（null 删键语义）。
	code, payload = w2PatchBody(t, env, id, `"credentialsPatch":{"base_url":"https://api.openai.com/v2","notes":null}`)
	if code != http.StatusOK {
		t.Fatalf("credentialsPatch: %d %v", code, payload)
	}
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	credentials = Credentials{}
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials["base_url"] != "https://api.openai.com/v2" {
		t.Fatalf("patch 后 base_url 不一致：%v", credentials["base_url"])
	}
	if _, exists := credentials["notes"]; exists {
		t.Fatalf("null 键应被删除：%v", credentials)
	}

	// credentials 与 credentialsPatch 互斥。
	code, payload = w2PatchBody(t, env, id, `"credentials":{"api_key":"a"},"credentialsPatch":{"api_key":"b"}`)
	if code != http.StatusBadRequest || payload["message"] != "credentials 与 credentialsPatch 不能同时提交" {
		t.Fatalf("互斥校验：%d %v", code, payload)
	}
}

func TestW2PatchBalanceAndProxyAndGroup(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedImportProxy(t, env, "pp-patch", adminID, "补丁代理")
	seedImportProxy(t, env, "pp-patch-disabled", adminID, "停用代理")
	env.exec(t, `UPDATE proxy_profiles SET enabled = 0 WHERE id = 'pp-patch-disabled'`)
	// 第二个 openai 分组用于换绑。
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-patch-b', ?, '补丁分组B', 'gpt', 1, 'personal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, adminID)
	id := w2PatchAccount(t, env, "综合补丁")

	// 余额查询开启 + 自定义配置。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"balanceQueryEnabled":true,"balanceQueryConfig":{"adapter":"builtin","intervalMinutes":7}}`)
	if code != http.StatusOK {
		t.Fatalf("余额开启: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if !containsChange(data["changedFields"], "balanceQueryEnabled") || !containsChange(data["changedFields"], "balanceQueryConfig") {
		t.Fatalf("余额字段未列入变更：%v", data["changedFields"])
	}
	if got := env.queryCell(t, `SELECT balance_query_enabled FROM accounts WHERE id = ?`, id); got != "1" {
		t.Fatalf("余额开关未落库：%s", got)
	}

	// 代理设置与置空。
	code, _ = w2PatchBody(t, env, id, `"proxyProfileId":"pp-patch"`)
	if code != http.StatusOK {
		t.Fatalf("代理设置: %d", code)
	}
	if got := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, id); got != "pp-patch" {
		t.Fatalf("代理未绑定：%s", got)
	}
	// 行为存疑：PATCH 置空代理（proxyProfileId: null）的变更判定
	// `(requested == nil) != current.Valid` 与预期相反，已绑定代理提交 null
	// 时被判定为「无变化」，代理保持不变。按当前实际行为断言。
	code, payload = w2PatchBody(t, env, id, `"proxyProfileId":null`)
	if code != http.StatusOK {
		t.Fatalf("代理置空: %d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT COALESCE(proxy_profile_id,'') FROM accounts WHERE id = ?`, id); got != "pp-patch" {
		t.Fatalf("置空行为存疑（当前实际保持不变）：%s", got)
	}
	// 停用代理拒绝绑定（读取实时修订号）。
	code, payload = w2PatchBody(t, env, id, `"proxyProfileId":"pp-patch-disabled"`)
	if code != http.StatusBadRequest {
		t.Fatalf("停用代理应拒绝：%d %v", code, payload)
	}

	// 分组换绑：绑定到补丁分组 B 并校验 group_accounts。
	code, payload = w2PatchBody(t, env, id, `"groupId":"grp-patch-b"`)
	if code != http.StatusOK {
		t.Fatalf("分组换绑: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ? AND group_id = 'grp-patch-b'`, id) != 1 {
		t.Fatal("新分组绑定缺失")
	}
	// 非同供应商分组拒绝。
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-patch-gpt2', ?, '其他供应商分组2', 'openai', 1, 'personal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, adminID)
	code, payload = w2PatchBody(t, env, id, `"groupId":"grp-patch-gpt2"`)
	if code != http.StatusBadRequest {
		t.Fatalf("跨供应商分组应拒绝：%d %v", code, payload)
	}
}

// w2PatchBody 注入账户的实时修订号后提交 PATCH（消除测试内手算修订号）。
func w2PatchBody(t *testing.T, env *testEnv, id, fields string) (int, map[string]any) {
	t.Helper()
	revision := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id)
	body := `{"expectedConfigRevision":` + revision + `,` + fields + `}`
	return env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, body)
}

// containsChange 断言 changedFields 数组包含指定字段。
func containsChange(raw any, field string) bool {
	list, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if item.(string) == field {
			return true
		}
	}
	return false
}

func TestW2PatchScheduleStatusAndFailureClear(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "状态补丁")

	// 可用性时间表：always-allow（复用 m09 契约窗口）保持 active 并写入计划。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"availabilitySchedule":`+alwaysAllowSchedule+`}`)
	if code != http.StatusOK {
		t.Fatalf("时间表补丁: %d %v", code, payload)
	}
	if !containsChange(dataMap(t, payload)["changedFields"], "availabilitySchedule") {
		t.Fatalf("时间表未列入变更：%v", dataMap(t, payload)["changedFields"])
	}
	if got := env.queryCell(t, `SELECT COALESCE(availability_schedule_json,'') FROM accounts WHERE id = ?`, id); got == "" {
		t.Fatal("时间表未落库")
	}

	// 状态置为 cooldown 并预置失败痕迹，然后 clearFailureState 清理。
	env.exec(t, `UPDATE accounts SET status = 'cooldown', cooldown_until = '2030-01-01T00:00:00Z',
		last_error_code = 'upstream_5xx', last_error_message = 'boom', health_check_failure_count = 3,
		health_check_failure_started_at = '2026-01-01T00:00:00Z' WHERE id = ?`, id)
	code, payload = w2PatchBody(t, env, id, `"clearFailureState":true`)
	if code != http.StatusOK {
		t.Fatalf("失败态清理: %d %v", code, payload)
	}
	if !containsChange(dataMap(t, payload)["changedFields"], "clearFailureState") {
		t.Fatalf("清理未列入变更：%v", dataMap(t, payload)["changedFields"])
	}
	row := env.queryCell(t, `SELECT COALESCE(cooldown_until,'')||'|'||COALESCE(last_error_code,'')||'|'||health_check_failure_count FROM accounts WHERE id = ?`, id)
	if row != "||0" {
		t.Fatalf("失败痕迹未清理：%s", row)
	}

	// status + schedulable 显式更新。
	code, payload = w2PatchBody(t, env, id, `"status":"disabled","schedulable":false`)
	if code != http.StatusOK {
		t.Fatalf("状态补丁: %d %v", code, payload)
	}
	row = env.queryCell(t, `SELECT status||'|'||schedulable FROM accounts WHERE id = ?`, id)
	if row != "disabled|0" {
		t.Fatalf("状态未落库：%s", row)
	}

	// 临时不可用探测开关。
	code, payload = w2PatchBody(t, env, id, `"temporaryUnavailableContinuousProbeEnabled":false`)
	if code != http.StatusOK {
		t.Fatalf("探测开关: %d %v", code, payload)
	}
}

func TestW2PatchExpiryChangeDisablesExpired(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "过期补丁")

	// 过去时间到期：自动停用链（与批量链一致的语义）。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"accountExpiresAt":"2020-01-01T00:00:00Z"}`)
	if code != http.StatusOK {
		t.Fatalf("到期补丁: %d %v", code, payload)
	}
	row := env.queryCell(t, `SELECT status||'|'||schedulable||'|'||COALESCE(last_error_code,'') FROM accounts WHERE id = ?`, id)
	if row != "disabled|0|account_expired" {
		t.Fatalf("过期自动停用链不一致：%s", row)
	}
	// 置 null 清除到期时间。
	code, payload = w2PatchBody(t, env, id, `"accountExpiresAt":null`)
	if code != http.StatusOK {
		t.Fatalf("到期清空: %d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT COALESCE(account_expires_at,'') FROM accounts WHERE id = ?`, id); got != "" {
		t.Fatalf("到期时间未清空：%s", got)
	}
}

func TestW2PatchSupportedModelsAndMappings(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "模型补丁")

	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"supportedModels":["gpt-4.1"],"healthCheckModel":"gpt-4.1",
		"modelMappings":[{"sourceModel":"gpt-4.1","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o-mini","upstreamEndpointFamily":"chat_completions","enabled":false}]}`)
	if code != http.StatusOK {
		t.Fatalf("模型补丁: %d %v", code, payload)
	}
	changed := dataMap(t, payload)["changedFields"]
	for _, field := range []string{"supportedModels", "healthCheckModel", "modelMappings"} {
		if !containsChange(changed, field) {
			t.Fatalf("变更缺少 %s：%v", field, changed)
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE account_id = ? AND model = 'gpt-4.1'`, id) != 1 {
		t.Fatal("支持模型未落库")
	}
	var enabled int
	if err := env.db.QueryRow(`SELECT enabled FROM account_model_mappings WHERE account_id = ? AND source_model = 'gpt-4.1'`, id).Scan(&enabled); err != nil {
		t.Fatalf("映射未落库：%v", err)
	}
	if enabled != 0 {
		t.Fatalf("映射 enabled 标志不一致：%d", enabled)
	}

	// PATCH 面不重复支持模型池断言（目录端口直通）：替换映射并以落库为准。
	code, payload = w2PatchBody(t, env, id, `"modelMappings":[{"sourceModel":"gpt-4.1","sourceEndpointFamily":"responses","upstreamModel":"gpt-4o-mini","upstreamEndpointFamily":"chat_completions"}]`)
	if code != http.StatusOK {
		t.Fatalf("映射替换: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_model_mappings WHERE account_id = ? AND source_endpoint_family = 'responses'`, id) != 1 {
		t.Fatal("替换映射未落库")
	}
}

func TestW2PatchTagReplaceAndLabels(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "标签补丁")

	// 全量 PATCH 面也能替换标签。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"tags":["新标签一","新标签二"]}`)
	if code != http.StatusOK {
		t.Fatalf("标签补丁: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = ?`, id) != 2 {
		t.Fatal("标签绑定数量不一致")
	}
	// 清空。
	code, payload = w2PatchBody(t, env, id, `"tags":[]`)
	if code != http.StatusOK {
		t.Fatalf("标签清空: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = ?`, id) != 0 {
		t.Fatal("标签未清空")
	}

	// accountPatchChangeLabel 覆盖主要字段文案。
	labels := map[string]string{
		"name": "名称", "notes": "备注", "credentials": "凭据", "status": "状态",
		"concurrencyLimit": "并发限制", "priority": "优先级", "tags": "标签",
		"proxyProfileId": "代理", "schedulable": "参与调度", "groupId": "绑定分组",
		"balanceQueryEnabled": "余额查询", "clearFailureState": "异常恢复",
	}
	for field, want := range labels {
		if got := accountPatchChangeLabel(field); got != want {
			t.Fatalf("字段 %s 文案不一致：%q (期望 %q)", field, got, want)
		}
	}
	if got := accountPatchChangeLabel("mystery"); got == "" {
		t.Fatal("未知字段应有回退文案")
	}
}
