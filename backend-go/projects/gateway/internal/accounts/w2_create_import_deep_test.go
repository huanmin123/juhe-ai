package accounts

// W2 创建与导入深字段批次：POST /accounts 的可选字段全量成功链（余额查询
// 配置、时间表、调度字段、代理与分组显式绑定）与导入管道的剩余校验分支
// （检查模型归属、映射上游池、导入执行器输入映射）。

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestW2CreateFullFieldSuccess(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedImportProxy(t, env, "pp-create", adminID, "创建代理")

	body := `{
		"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"全字段账户",
		"type":"api_key","credentials":{"api_key":"sk-full-field-123456","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini","gpt-4.1"],"healthCheckModel":"gpt-4.1","healthCheckEndpointMode":"chat_json",
		"modelMappings":[{"sourceModel":"gpt-4.1","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o-mini","upstreamEndpointFamily":"chat_completions","enabled":false}],
		"tags":["全字段","测试"],"status":"active","skipInitialHealthCheck":true,
		"concurrencyLimit":42,"priority":9,"superPriorityEnabled":true,"fallbackEnabled":false,
		"proxyProfileId":"pp-create","groupId":"grp-default-` + adminID + `",
		"accountExpiresAt":"2031-06-30T00:00:00Z",
		"availabilitySchedule":` + alwaysAllowSchedule + `,
		"balanceQueryEnabled":true,"balanceQueryConfig":{"adapter":"builtin","intervalMinutes":10},
		"temporaryUnavailableContinuousProbeEnabled":false,"notes":"全字段备注"
	}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", body)
	if code != http.StatusCreated {
		t.Fatalf("全字段创建失败：%d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	// 调度与代理。
	var concurrency, priority int
	var super, schedulable bool
	if err := env.db.QueryRow(`SELECT concurrency_limit, priority, super_priority_enabled, schedulable
		FROM accounts WHERE id = ?`, id).Scan(&concurrency, &priority, &super, &schedulable); err != nil {
		t.Fatal(err)
	}
	// skipInitialHealthCheck 让 active 账户跳过 pending_test 直接可调度。
	if concurrency != 42 || priority != 9 || !super || !schedulable {
		t.Fatalf("调度字段不一致：%d %d %v %v", concurrency, priority, super, schedulable)
	}
	if got := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, id); got != "pp-create" {
		t.Fatalf("代理绑定不一致：%s", got)
	}
	// 分组绑定使用显式分组。
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ? AND group_id = ?`, id, "grp-default-"+adminID) != 1 {
		t.Fatal("显式分组绑定缺失")
	}
	// 检查模型与备注。
	if got := env.queryCell(t, `SELECT health_check_model || '|' || COALESCE(notes,'') FROM accounts WHERE id = ?`, id); got != "gpt-4.1|全字段备注" {
		t.Fatalf("检查模型/备注不一致：%s", got)
	}
	// 余额查询配置落库。
	if got := env.queryCell(t, `SELECT balance_query_enabled FROM accounts WHERE id = ?`, id); got != "1" {
		t.Fatalf("余额查询未开启：%s", got)
	}
	if config := env.queryCell(t, `SELECT balance_query_config_json FROM accounts WHERE id = ?`, id); !strings.Contains(config, "builtin") {
		t.Fatalf("余额配置未落库：%s", config)
	}
	// 到期时间。
	if got := env.queryCell(t, `SELECT COALESCE(account_expires_at,'') FROM accounts WHERE id = ?`, id); got == "" {
		t.Fatal("到期时间未落库")
	}
	// 时间表。
	if got := env.queryCell(t, `SELECT COALESCE(availability_schedule_json,'') FROM accounts WHERE id = ?`, id); got == "" {
		t.Fatal("时间表未落库")
	}
	// 禁用映射。
	var enabled int
	if err := env.db.QueryRow(`SELECT enabled FROM account_model_mappings WHERE account_id = ?`, id).Scan(&enabled); err != nil {
		t.Fatalf("映射未落库：%v", err)
	}
	if enabled != 0 {
		t.Fatalf("映射 enabled 标志不一致：%d", enabled)
	}

	// 余额查询能力边界：oauth 账户开启余额查询被拒（上游能力不支持）。
	oauthBody := strings.Replace(body, "全字段账户", "oauth 余额", 1)
	oauthBody = strings.Replace(oauthBody, `"type":"api_key","credentials":{"api_key":"sk-full-field-123456","base_url":"https://api.openai.com/v1"}`,
		`"type":"oauth","credentials":{"refresh_token":"rt-w2","account_id":"acc-w2"}`, 1)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", oauthBody)
	if code != http.StatusBadRequest {
		t.Fatalf("oauth 余额查询应被能力边界拒绝：%d %v", code, payload)
	}
	// 开启余额但缺配置。
	missing := strings.Replace(body, `"balanceQueryConfig":{"adapter":"builtin","intervalMinutes":10},`, "", 1)
	missing = strings.Replace(missing, "全字段账户", "缺配置账户", 1)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", missing)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "必须选择查询类型") {
		t.Fatalf("缺配置应拒绝：%d %v", code, payload)
	}
	// 非法配置字段。
	bad := strings.Replace(body, `"intervalMinutes":10`, `"mystery":1`, 1)
	bad = strings.Replace(bad, "全字段账户", "坏配置账户", 1)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", bad)
	if code != http.StatusBadRequest {
		t.Fatalf("坏配置应拒绝：%d %v", code, payload)
	}
}

func TestW2ImportDeepValidationBranches(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	now := "2026-01-01T00:00:00.000Z"
	// 带默认模型目录的窄档案（只有 m2）。
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('profile_openai_narrow_w2', 'openai', '窄目录', 1, 'openai', 'v1',
		'https://api.openai.com/v1', 'm2', '["api_key"]', '[]', ?, ?)`, now, now)

	account := func(mutations map[string]any) map[string]any {
		base := w2APIKeyAccount("深校验", "深分组")
		for key, value := range mutations {
			base[key] = value
		}
		return base
	}

	t.Run("检查模型必须属于支持模型", func(t *testing.T) {
		doc := w2NativeDoc([]map[string]any{account(map[string]any{
			"supportedModels": []any{"gpt-4o-mini"}, "healthCheckModel": "secret-model",
		})}, nil)
		result := w2Preview(t, env, doc, "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "healthCheckModel 必须属于 supportedModels") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("映射上游必须落在支持模型池", func(t *testing.T) {
		doc := w2NativeDoc([]map[string]any{account(map[string]any{
			"supportedModels": []any{"gpt-4o-mini"},
			"modelMappings":   []any{map[string]any{"sourceModel": "gpt-4o-mini", "sourceEndpointFamily": "chat_completions", "upstreamModel": "ghost", "upstreamEndpointFamily": "responses"}},
		})}, nil)
		result := w2Preview(t, env, doc, "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "上游模型") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("status 枚举归一", func(t *testing.T) {
		doc := w2NativeDoc([]map[string]any{account(map[string]any{"status": "pending_test"})}, nil)
		result := w2Preview(t, env, doc, "", ImportOptions{}, adminID)
		if w2Item(t, result, 0).Action != importActionCreate {
			t.Fatalf("pending_test 应合法：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("执行输入映射（执行器字段透传）", func(t *testing.T) {
		doc := w2NativeDoc([]map[string]any{account(map[string]any{
			"proxyRef":         "p9",
			"concurrencyLimit": float64(33), "priority": float64(4),
			"superPriorityEnabled": true, "fallbackEnabled": false,
			"healthCheckEndpointMode": "chat_sse", "notes": "导入备注",
			"accountExpiresAt": "2031-01-01T00:00:00Z",
		})}, []map[string]any{w2Proxy("p9", "执行代理")})
		result, err := env.store.ExecuteImport(context.Background(), doc, "", ImportOptions{},
			AccessScope{ViewerID: adminID, IsAdmin: true})
		if err != nil {
			t.Fatalf("执行错误：%v", err)
		}
		if !result.Imported || result.Summary.Accounts.Create != 1 {
			t.Fatalf("执行汇总不一致：%+v", result.Summary)
		}
		id := *result.Accounts[0].AccountID
		row := env.queryCell(t, `SELECT concurrency_limit || '|' || priority || '|' || super_priority_enabled || '|' || health_check_endpoint_mode || '|' || COALESCE(notes,'') FROM accounts WHERE id = ?`, id)
		if row != "33|4|1|chat_sse|导入备注" {
			t.Fatalf("执行字段透传不一致：%s", row)
		}
		if got := env.queryCell(t, `SELECT COALESCE(account_expires_at,'') FROM accounts WHERE id = ?`, id); got == "" {
			t.Fatal("到期时间未透传")
		}
		if got := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, id); got == "" {
			t.Fatal("代理引用未透传")
		}
		// active 导入重映射为 pending_test。
		if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = ?`, id); got != "pending_test" {
			t.Fatalf("状态重映射不一致：%s", got)
		}
	})
}
