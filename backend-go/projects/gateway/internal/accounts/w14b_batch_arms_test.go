package accounts

// w14b batch.go 臂补齐：LoadBatchEditContext 校验/查询/解密臂、
// BatchUpdate 的凭据覆盖（错误处理规则/响应检查规则/端点形态/服务等级）、
// 支持模型与映射替换、代理解析、过期时间、时间计划、备注清空、状态覆盖、
// super/fallback 互斥让位、批次解析 strict 校验等剩余分支。

import (
	"context"
	"strings"
	"testing"
)

// w14bBatchSetup 建一对可批量编辑的 openai api_key 账户（与 w13a 类似的
// 最小种子，带启用的代理解析行）。
func w14bBatchSetup(t *testing.T, env *testEnv, suffix string) (string, []BatchUpdateTarget) {
	t.Helper()
	adminID := env.login(t, "root", "root-pass", "super_admin")
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT OR IGNORE INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-openai-compat', 'openai', 'OpenAI 兼容', 1, '["gpt-4o-mini"]', ?, ?)`, now, now)
	env.exec(t, `INSERT OR IGNORE INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_openai_openai_v1', 'openai', 'OpenAI 兼容协议', 1, 'openai', 'v1',
		'https://api.openai.com/v1', 'gpt-4o-mini', '["api_key","oauth"]', '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('proxy-w14b`+suffix+`', ?, 'w14b-proxy', 'http', '127.0.0.1', 7890, 1, ?, ?)`, adminID, now, now)
	for _, name := range []string{"w14b-x1" + suffix, "w14b-x2" + suffix} {
		sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w14b-" + name, "base_url": "https://api.openai.com/v1"})
		if err != nil {
			t.Fatal(err)
		}
		env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
			VALUES (?, ?, 'openai', 'profile_openai_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', ?, ?, ?)`,
			"acc-"+name, adminID, name, sealed, now, now)
	}
	revision := func(id string) int64 {
		var revision int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, id).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	targets := []BatchUpdateTarget{
		{AccountID: "acc-w14b-x1" + suffix, ConfigRevision: revision("acc-w14b-x1" + suffix)},
		{AccountID: "acc-w14b-x2" + suffix, ConfigRevision: revision("acc-w14b-x2" + suffix)},
	}
	return adminID, targets
}

func w14bRefreshTargets(t *testing.T, env *testEnv, targets []BatchUpdateTarget) {
	t.Helper()
	for index := range targets {
		var revision int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, targets[index].AccountID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		targets[index].ConfigRevision = revision
	}
}

func TestW14BBatchEditContextArms(t *testing.T) {
	env := newTestEnv(t)
	adminID, targets := w14bBatchSetup(t, env, "")
	ctx := context.Background()
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ids := []string{targets[0].AccountID, targets[1].AccountID}

	// 目标数量/去重校验。
	if _, err := env.store.LoadBatchEditContext(ctx, ids[:1], nil, scope); err == nil || !strings.Contains(err.Error(), "2-100") {
		t.Fatalf("单目标应拒绝：%v", err)
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = "acc-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := env.store.LoadBatchEditContext(ctx, many, nil, scope); err == nil {
		t.Fatal("101 目标应拒绝")
	}
	if _, err := env.store.LoadBatchEditContext(ctx, []string{" acc-a ", "acc-a"}, nil, scope); err == nil {
		t.Fatal("trim 后重复应拒绝")
	}
	if _, err := env.store.LoadBatchEditContext(ctx, ids, []string{"bogus"}, scope); err == nil || !strings.Contains(err.Error(), "不支持的批量编辑上下文字段") {
		t.Fatalf("未知字段应拒绝：%v", err)
	}
	if _, err := env.store.LoadBatchEditContext(ctx, ids, nil, AccessScope{}); err == nil {
		t.Fatal("空 scope 应拒绝")
	}
	// 全字段 happy path：models/mappings/modes 三类。
	env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'openai', 'gpt-4o-mini', '2026-09-17T00:00:00.000Z')`, ids[0])
	env.exec(t, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, created_at, updated_at)
		VALUES (?, 'openai', 'gpt-4o-mini', 'chat_completions', 'up-mini', 'chat_completions', '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z')`, ids[0])
	items, err := env.store.LoadBatchEditContext(ctx, ids, []string{"supportedModels", "modelMappings", "supportedEndpointModes"}, scope)
	if err != nil {
		t.Fatalf("批量上下文应成功：%v", err)
	}
	if len(items) != 2 {
		t.Fatalf("上下文条目数不符：%+v", items)
	}
	if items[0].ID == ids[0] {
		if items[0].SupportedModels == nil || len(*items[0].SupportedModels) != 1 {
			t.Fatalf("支持模型应渲染：%+v", items[0])
		}
		if items[0].ModelMappings == nil || len(*items[0].ModelMappings) != 1 {
			t.Fatalf("模型映射应渲染：%+v", items[0])
		}
	} else if items[1].SupportedModels == nil || len(*items[1].SupportedModels) != 0 {
		t.Fatalf("未配置账户支持模型应为空数组：%+v", items[1])
	}
	// 损坏的凭据密文触发解密失败臂。
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'not-sealed' WHERE id = ?`, ids[0])
	if _, err := env.store.LoadBatchEditContext(ctx, ids, []string{"supportedEndpointModes"}, scope); err == nil {
		t.Fatal("损坏密文应报错")
	}
	// 非 owner 视角走 scoped 子句臂。
	userID := env.login(t, "alice-w14b-ctx", "alice-pass", "user")
	if _, err := env.store.LoadBatchEditContext(ctx, ids, nil, AccessScope{ViewerID: userID}); err == nil || !strings.Contains(err.Error(), "不属于同一作用域") {
		t.Fatalf("跨 owner 视角应拒绝：%v", err)
	}
	// strict 上下文体解析。
	if _, _, prompt := batchEditContextBody(map[string]any{"unknown": 1}); prompt == "" {
		t.Fatal("未知顶层键应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": "x"}); prompt == "" {
		t.Fatal("accountIds 非数组应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": []any{"a"}}); prompt == "" {
		t.Fatal("单目标应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": []any{"", "b"}}); prompt == "" {
		t.Fatal("空 id 应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": []any{"a", "a"}}); prompt == "" {
		t.Fatal("重复 id 应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": []any{"a", "b"}}); prompt == "" {
		t.Fatal("缺 fields 应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": []any{"a", "b"}, "fields": "x"}); prompt == "" {
		t.Fatal("fields 非数组应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": []any{"a", "b"}, "fields": []any{4}}); prompt == "" {
		t.Fatal("fields 项非字符串应拒绝")
	}
	if _, _, prompt := batchEditContextBody(map[string]any{"accountIds": []any{"a", "b"}, "fields": []any{"supportedModels", "supportedModels"}}); prompt == "" {
		t.Fatal("fields 重复应拒绝")
	}
	gotIDs, gotFields, prompt := batchEditContextBody(map[string]any{
		"accountIds": []any{" a ", "b"}, "fields": []any{"supportedModels", "modelMappings"}})
	if prompt != "" || len(gotIDs) != 2 || len(gotFields) != 2 || gotIDs[0] != "a" {
		t.Fatalf("合法上下文体应通过：%v %v %q", gotIDs, gotFields, prompt)
	}
}

func TestW14BBatchUpdateCredentialAndProxyArms(t *testing.T) {
	env := newTestEnv(t)
	adminID, targets := w14bBatchSetup(t, env, "")
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		gptCatalogFact(),
		{Model: "gpt-4o-mini-2", SupportedAPIProtocols: []string{"chat_completions", "responses"}},
	}})
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	field := func(name string, value any) BatchUpdateField {
		return BatchUpdateField{Enabled: true, Value: value}
	}

	// 空 scope 拒绝（L417 臂）。
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{Targets: targets}, AccessScope{}); err == nil {
		t.Fatal("空 scope 批量应拒绝")
	}

	// 种子健康检查模型与支持模型：模型配置变更会断言检查模型属于支持集合。
	env.exec(t, `UPDATE accounts SET health_check_model = 'gpt-4o-mini'`)
	// 种子支持模型：端点形态/覆盖字段参与会断言非空支持模型集合。
	for _, target := range targets {
		env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
			VALUES (?, 'openai', 'gpt-4o-mini', '2026-09-17T00:00:00.000Z')`, target.AccountID)
	}
	// 凭据覆盖：错误处理规则 + 响应检查规则 + 端点形态 + 服务等级清除。
	w14bRefreshTargets(t, env, targets)
	result, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"errorHandlingRules": field("errorHandlingRules", []any{map[string]any{
				"enabled": true, "name": "限流", "priority": float64(1), "action": "rate_limited",
				"reset_strategy": "duration", "duration_hours": float64(2), "keywords": []any{"quota"},
			}}),
			"responseInspectionRules": field("responseInspectionRules", []any{map[string]any{
				"enabled": true, "name": "r", "priority": float64(1), "action": "observe",
				"match": map[string]any{"outputTextIncludes": []any{"err"}},
			}}),
			"supportedEndpointModes": field("supportedEndpointModes", []any{"chat_json"}),
			"serviceTierOverride":    field("serviceTierOverride", nil),
			"notes":                  field("notes", nil),
			"accountExpiresAt":       field("accountExpiresAt", "2027-01-01T00:00:00Z"),
			"proxyProfileId":         field("proxyProfileId", "proxy-w14b"),
		},
	}, scope)
	if err != nil {
		t.Fatalf("凭据覆盖批量应成功：%v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("批量结果数不符：%+v", result.Items)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE proxy_profile_id = 'proxy-w14b'`); got != 2 {
		t.Fatalf("代理解析应落库：%d", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE account_expires_at IS NOT NULL`); got != 2 {
		t.Fatalf("过期时间应落库：%d", got)
	}
	for _, id := range []string{targets[0].AccountID, targets[1].AccountID} {
		var sealed string
		if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, id).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		var credentials Credentials
		if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
			t.Fatalf("凭据应可解密：%v", err)
		}
		rules, ok := credentials["error_handling_rules"].([]any)
		if !ok || len(rules) != 1 {
			t.Fatalf("错误处理规则应写入凭据：%+v", credentials)
		}
		if _, ok := credentials["response_inspection_rules"].([]any); !ok {
			t.Fatalf("响应检查规则应写入凭据：%+v", credentials)
		}
		if modes, _ := credentials["supported_endpoint_modes"].([]any); len(modes) != 1 || modes[0] != "chat_json" {
			t.Fatalf("端点形态应写入凭据：%+v", credentials)
		}
	}

	// 代理解析清空 + 无效代理（不存在/停用）臂。
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"proxyProfileId": field("proxyProfileId", nil)},
	}, scope); err != nil {
		t.Fatalf("清空代理应成功：%v", err)
	}
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"proxyProfileId": field("proxyProfileId", "proxy-missing")},
	}, scope); err == nil || !strings.Contains(err.Error(), "代理不存在或已停用") {
		t.Fatalf("缺失代理应拒绝：%v", err)
	}
	env.exec(t, `UPDATE proxy_profiles SET enabled = 0 WHERE id = 'proxy-w14b'`)
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"proxyProfileId": field("proxyProfileId", "proxy-w14b")},
	}, scope); err == nil || !strings.Contains(err.Error(), "代理不存在或已停用") {
		t.Fatalf("停用代理应拒绝：%v", err)
	}
	env.exec(t, `UPDATE proxy_profiles SET enabled = 1 WHERE id = 'proxy-w14b'`)

	// 凭据覆盖字段传空白/非法值臂。
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"supportedEndpointModes": field("supportedEndpointModes", []any{"chat_json", "bogus_mode"})},
	}, scope); err == nil {
		t.Fatal("非法端点形态应拒绝")
	}
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"errorHandlingRules": field("errorHandlingRules", "bad")},
	}, scope); err == nil {
		t.Fatal("非法错误处理规则应拒绝")
	}

	// 支持模型变化：目录校验 + 替换落库（L482 臂）。
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"supportedModels": field("supportedModels", []any{"gpt-4o-mini", "gpt-4.1-mini"})},
	}, scope); err == nil {
		t.Fatal("目录外模型应拒绝")
	}
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"supportedModels": field("supportedModels", []any{"gpt-4o-mini", "gpt-4o-mini-2"})},
	}, scope); err != nil {
		t.Fatalf("目录内模型应成功：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE model = 'gpt-4o-mini'`); got != 2 {
		t.Fatalf("支持模型应批量替换：%d", got)
	}

	// 模型映射更新 + tags 替换（L487/L492 臂）。
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"modelMappings": field("modelMappings", []any{map[string]any{
				"sourceModel": "gpt-4o-mini", "sourceEndpointFamily": "chat_completions",
				"upstreamModel": "gpt-4o-mini-2", "upstreamEndpointFamily": "chat_completions", "enabled": true,
			}}),
			"tags": field("tags", []any{"w14b-tag-a", "w14b-tag-b"}),
		},
	}, scope); err != nil {
		t.Fatalf("映射与标签批量应成功：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_model_mappings WHERE upstream_model = 'gpt-4o-mini-2'`); got != 2 {
		t.Fatalf("映射应批量落库：%d", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_tag_bindings`); got != 4 {
		t.Fatalf("标签绑定应批量落库：%d", got)
	}

	// 健康检查模型空值拒绝 + 非法形态拒绝。
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"healthCheckModel": field("healthCheckModel", "  ")},
	}, scope); err == nil || !strings.Contains(err.Error(), "账户检查模型不能为空") {
		t.Fatalf("空检查模型应拒绝：%v", err)
	}
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"healthCheckEndpointMode": field("healthCheckEndpointMode", "images_json")},
	}, scope); err == nil {
		t.Fatal("未启用的检查形态应拒绝")
	}

	// 时间计划清除 + 优先级/超级优先让位。
	w14bRefreshTargets(t, env, targets)
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"availabilitySchedule": field("availabilitySchedule", nil),
			"priority":             field("priority", float64(2)),
			"superPriorityEnabled": field("superPriorityEnabled", true),
		},
	}, scope); err != nil {
		t.Fatalf("计划清除与超级优先应成功：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE super_priority_enabled = 1`); got != 2 {
		t.Fatalf("超级优先应落库：%d", got)
	}

	// 设置已过期的时间计划：计划态覆盖强制 disabled + 不可调度。
	w14bRefreshTargets(t, env, targets)
	expiredSchedule := map[string]any{
		"enabled": true, "mode": "allow_windows",
		"windows":   []any{map[string]any{"start": "09:00", "end": "10:00", "daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)}}},
		"dateRange": map[string]any{"startDate": "2026-01-01", "endDate": "2026-01-02"},
	}
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{"availabilitySchedule": field("availabilitySchedule", expiredSchedule)},
	}, scope); err != nil {
		t.Fatalf("过期时间计划应成功：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE status = 'disabled'`); got != 2 {
		t.Fatalf("过期计划应强制停用：%d", got)
	}
}

func TestW14BBatchUpdateParseArms(t *testing.T) {
	body := func(targets, updates any) map[string]any {
		return map[string]any{"targets": targets, "updates": updates}
	}
	if _, prompt := batchUpdateBody(map[string]any{"bogus": 1}); prompt == "" {
		t.Fatal("未知顶层键应拒绝")
	}
	if _, prompt := batchUpdateBody(body("x", map[string]any{})); prompt == "" {
		t.Fatal("targets 非数组应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{1}, map[string]any{})); prompt == "" {
		t.Fatal("target 非对象应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{map[string]any{"accountId": "a", "bogus": 1}}, map[string]any{})); prompt == "" {
		t.Fatal("target 未知键应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{map[string]any{"accountId": "  "}}, map[string]any{})); prompt == "" {
		t.Fatal("空 accountId 应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{map[string]any{"accountId": "a", "configRevision": 0.5}}, map[string]any{})); prompt == "" {
		t.Fatal("非整数版本应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "a", "configRevision": float64(1)},
	}, map[string]any{})); prompt == "" {
		t.Fatal("重复目标应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, "x")); prompt == "" {
		t.Fatal("updates 非对象应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"bogusField": map[string]any{"enabled": true}})); prompt == "" {
		t.Fatal("未知更新字段应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"notes": "x"})); prompt == "" {
		t.Fatal("更新值非 {enabled,value} 应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"notes": map[string]any{"enabled": "yes"}})); prompt == "" {
		t.Fatal("enabled 非布尔应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"notes": map[string]any{"enabled": true, "value": 3, "extra": 1}})); prompt == "" {
		t.Fatal("value 外多余键应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"healthCheckModel": map[string]any{"enabled": true, "value": "  "}})); prompt == "" {
		t.Fatal("空检查模型应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"modelMappings": map[string]any{"enabled": true, "value": "x"}})); prompt == "" {
		t.Fatal("映射非数组应拒绝")
	}
	if _, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"supportedEndpointModes": map[string]any{"enabled": true, "value": []any{}}})); prompt == "" {
		t.Fatal("空端点形态数组应拒绝")
	}
	// 合法体 + validateBatchUpdateValue 各字段臂。
	input, prompt := batchUpdateBody(body([]any{
		map[string]any{"accountId": "a", "configRevision": float64(1)},
		map[string]any{"accountId": "b", "configRevision": float64(1)},
	}, map[string]any{"notes": map[string]any{"enabled": true, "value": "ok"}}))
	if prompt != "" || len(input.Targets) != 2 || input.Updates["notes"].Value != "ok" {
		t.Fatalf("合法批量体应通过：%+v %q", input, prompt)
	}
	for field, value := range map[string]any{
		"tags": 3, "healthCheckModel": " ", "healthCheckEndpointMode": "bogus",
		"modelMappings": 3, "supportedEndpointModes": []any{3}, "serviceTierOverride": 3,
		"reasoningEffortOverride": 3,
	} {
		if validateBatchUpdateValue(field, value) == "" {
			t.Fatalf("字段 %s 非法值应拒绝", field)
		}
	}
	if validateBatchUpdateValue("serviceTierOverride", nil) != "" {
		t.Fatal("serviceTierOverride nil 应放行")
	}
	if validateBatchUpdateValue("serviceTierOverride", "bogus") == "" {
		t.Fatal("serviceTierOverride 非法值应拒绝")
	}
	if validateBatchUpdateValue("reasoningEffortOverride", "low") != "" {
		t.Fatal("reasoningEffortOverride 合法值应放行")
	}
	if validateBatchUpdateValue("notes", "ok") != "" {
		t.Fatal("notes 合法值应放行")
	}
	if validateBatchUpdateValue("tags", []any{"a"}) != "" {
		t.Fatal("tags 合法值应放行")
	}
}
