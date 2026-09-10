package accounts

// W2 手动测试 HTTP 链路测试：test-options / test-options/{model} / POST test /
// POST test-draft / 会话与任务树路由的参数校验与分支契约
// （test_dispatch_routes.go 的 handler 层）。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// w2DispatchEnv 准备目录 + 测试账户 + 接受投递的 fake worker。
func w2DispatchEnv(t *testing.T, accept bool) (*testEnv, string, string) {
	t.Helper()
	effects := &fakeTestEffects{accept: accept}
	env := newTestFamilyEnv(t, effects)
	adminID := env.login(t, "admin", "admin-password", "admin")
	accountID := seedTestAccount(t, env, adminID)
	return env, adminID, accountID
}

// w2DraftAccountBody 构造与 acc-test-1 一致的草稿账户配置。
func w2DraftAccountBody(groupID string) string {
	return `"account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt",` +
		`"name":"测试草稿","type":"api_key","groupId":"` + groupID + `",` +
		`"credentials":{"api_key":"sk-draft-123456","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini"],"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json"}`
}

func TestW2TestOptionsRoutes(t *testing.T) {
	env, _, accountID := w2DispatchEnv(t, true)

	t.Run("选项列表", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/test-options", "")
		if code != http.StatusOK {
			t.Fatalf("test-options 状态码：%d %v", code, payload)
		}
		options := dataArray(t, payload)
		if len(options) != 1 {
			t.Fatalf("选项数量不一致：%v", options)
		}
		option := options[0].(map[string]any)
		if option["id"] != "gpt-4o-mini" {
			t.Fatalf("选项模型不一致：%v", option)
		}
		modes := option["testEndpointModes"].([]any)
		// health_check_endpoint_mode=chat_json 优先，其后是 api_key 默认顺序。
		if len(modes) != 4 || modes[0] != "chat_json" || modes[1] != "chat_sse" ||
			modes[2] != "responses_sse" || modes[3] != "responses_json" {
			t.Fatalf("请求形态列表不一致：%v", modes)
		}
	})
	t.Run("keyword 与 limit", func(t *testing.T) {
		// 额外目录行：keyword 命中时返回两个模型。
		env.exec(t, `INSERT OR IGNORE INTO provider_model_catalog
			(id, provider_code, model, status, mode, release_date, supported_api_protocols_json, catalog_visible, created_at, updated_at)
			VALUES ('cat-2', 'gpt', 'gpt-4.1', 'active', NULL, '2026-02-01',
			'["chat_completions","responses"]', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/test-options?keyword=gpt-4", "")
		if code != http.StatusOK || len(dataArray(t, payload)) != 2 {
			t.Fatalf("keyword 命中数量不一致：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/test-options?keyword=nomatch", "")
		if code != http.StatusOK || len(dataArray(t, payload)) != 1 {
			t.Fatalf("keyword 不命中时只剩保底模型：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/test-options?limit=0", "")
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "limit 必须是 1 到 50 的整数") {
			t.Fatalf("limit 校验不一致：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/test-options?selectedIds=gpt-4o-mini&keyword=zzz", "")
		if code != http.StatusOK || len(dataArray(t, payload)) != 1 {
			t.Fatalf("selectedIds 应不受 keyword 限制：%d %v", code, payload)
		}
	})
	t.Run("模型能力", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/test-options/models/gpt-4o-mini", "")
		if code != http.StatusOK {
			t.Fatalf("模型能力状态码：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		if data["id"] != "gpt-4o-mini" {
			t.Fatalf("能力模型不一致：%v", data)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/test-options/models/unknown-model", "")
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "模型不在当前账户供应商可用目录中") {
			t.Fatalf("未知模型校验不一致：%d %v", code, payload)
		}
	})
	t.Run("账户不存在", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-missing/test-options", "")
		if code != http.StatusNotFound || payload["message"] != "账户不存在" {
			t.Fatalf("404 契约不一致：%d %v", code, payload)
		}
		code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-missing/test-options/models/gpt-4o-mini", "")
		if code != http.StatusNotFound {
			t.Fatalf("模型能力 404 不一致：%d", code)
		}
	})
}

func TestW2TestAccountDispatch(t *testing.T) {
	env, adminID, accountID := w2DispatchEnv(t, true)

	t.Run("显式模型投递", func(t *testing.T) {
		// 无草稿时不回退健康检查模型：缺 model 报「请选择测试模型」。
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test", `{}`)
		if code != http.StatusBadRequest || payload["message"] != "请选择测试模型" {
			t.Fatalf("缺 model 校验不一致：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test", `{"model":"gpt-4o-mini"}`)
		if code != http.StatusAccepted {
			t.Fatalf("投递状态码：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		if data["model"] != "gpt-4o-mini" || data["testEndpointMode"] != "chat_json" {
			t.Fatalf("默认选择不一致：%v", data)
		}
		taskID := data["id"].(string)
		if env.count(t, `SELECT COUNT(*) FROM account_test_tasks WHERE id = ? AND status = 'queued'`, taskID) != 1 {
			t.Fatal("任务行未落库或状态不一致")
		}
	})
	t.Run("worker 不可用", func(t *testing.T) {
		env2, _, account2 := w2DispatchEnv(t, false)
		code, payload := env2.do(t, http.MethodPost, "/__aisys__/api/accounts/"+account2+"/test", `{"model":"gpt-4o-mini"}`)
		if code != http.StatusServiceUnavailable || !strings.Contains(payload["message"].(string), "后台 worker 暂不可用") {
			t.Fatalf("503 契约不一致：%d %v", code, payload)
		}
		if env2.count(t, `SELECT COUNT(*) FROM account_test_tasks WHERE account_id = ? AND status = 'failed'`, account2) != 1 {
			t.Fatal("投递失败后任务应标记 failed")
		}
	})
	t.Run("请求体校验", func(t *testing.T) {
		cases := map[string]string{
			"未知键":          `{"bogus":1}`,
			"model 非字符串":   `{"model":3}`,
			"非法请求形态":       `{"testEndpointMode":"grpc"}`,
			"空会话":          `{"testSessionId":"  "}`,
			"prompt 非字符串":  `{"prompt":9}`,
			"account 非对象":  `{"account":"x"}`,
			"account 缺分组":  `{"account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"n","type":"api_key","healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json"}}`,
			"account 未知字段": `{"account":{"mystery":1}}`,
		}
		for name, body := range cases {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test", body)
			if code != http.StatusBadRequest || payload["message"] != "账户测试参数无效" {
				t.Fatalf("%s：期望 400 账户测试参数无效，实际 %d %v", name, code, payload)
			}
		}
	})
	t.Run("不支持协议", func(t *testing.T) {
		env.exec(t, `UPDATE accounts SET protocol_code = 'grpc', protocol_version = 'v9' WHERE id = ?`, accountID)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test", `{}`)
		if code != http.StatusBadRequest || payload["message"] != unsupportedGatewayProtocolTestMessage {
			t.Fatalf("协议 gate 不一致：%d %v", code, payload)
		}
		env.exec(t, `UPDATE accounts SET protocol_code = 'openai', protocol_version = 'v1' WHERE id = ?`, accountID)
	})
	t.Run("会话关联投递", func(t *testing.T) {
		code, sessionPayload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions", "")
		if code != http.StatusCreated {
			t.Fatalf("会话创建失败：%d", code)
		}
		sessionID := dataMap(t, sessionPayload)["id"].(string)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test",
			`{"model":"gpt-4o-mini","testSessionId":"`+sessionID+`"}`)
		if code != http.StatusAccepted {
			t.Fatalf("会话内投递失败：%d %v", code, payload)
		}
		if env.count(t, `SELECT COUNT(*) FROM account_test_session_tasks WHERE session_id = ?`, sessionID) != 1 {
			t.Fatal("会话任务关联缺失")
		}
	})

	// 草稿账户链：与保存账户一致时按草稿快照投递。
	groupID := "grp-default-" + adminID
	t.Run("一致草稿投递", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test",
			`{`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusAccepted {
			t.Fatalf("草稿投递状态码：%d %v", code, payload)
		}
		taskID := dataMap(t, payload)["id"].(string)
		var draft string
		if err := env.db.QueryRow(`SELECT draft_account_encrypted FROM account_test_tasks WHERE id = ?`, taskID).Scan(&draft); err != nil || draft == "" {
			t.Fatalf("草稿快照未落库：%v", err)
		}
	})
	t.Run("草稿与账户不一致", func(t *testing.T) {
		body := strings.Replace(w2DraftAccountBody(groupID), `"providerCode":"gpt"`, `"providerCode":"openai"`, 1)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test", `{`+body+`}`)
		if code != http.StatusBadRequest || payload["message"] != "账户测试草稿与当前账户不一致" {
			t.Fatalf("一致性 gate 不一致：%d %v", code, payload)
		}
		// 另插一个 gpt 档案：草稿准备成功后与保存账户的档案比较。
		now := "2026-01-01T00:00:00.000Z"
		env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
			protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
			capabilities_json, created_at, updated_at)
			VALUES ('profile_gpt_openai_v1_w2alt', 'gpt', 'OpenAI 备选', 1, 'openai', 'v1',
			'https://api.openai.com/v1', 'gpt-4o-mini', '["api_key"]', '[]', ?, ?)`, now, now)
		profileBody := strings.Replace(w2DraftAccountBody(groupID),
			`"providerProtocolProfileId":"prof-gpt"`, `"providerProtocolProfileId":"profile_gpt_openai_v1_w2alt"`, 1)
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test", `{`+profileBody+`}`)
		if code != http.StatusBadRequest || payload["message"] != "账户测试草稿与当前账户协议档案不一致" {
			t.Fatalf("协议档案 gate 不一致：%d %v", code, payload)
		}
	})
}

func TestW2TestDraftRoute(t *testing.T) {
	env, adminID, _ := w2DispatchEnv(t, true)
	groupID := "grp-default-" + adminID

	t.Run("缺 account", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft", `{}`)
		if code != http.StatusBadRequest || payload["message"] != "账户草稿测试参数无效" {
			t.Fatalf("缺 account 校验不一致：%d %v", code, payload)
		}
	})
	t.Run("完整草稿投递", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft",
			`{`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusAccepted {
			t.Fatalf("草稿投递状态码：%d %v", code, payload)
		}
	})
	t.Run("准备链校验", func(t *testing.T) {
		cases := []struct {
			name    string
			body    string
			message string
		}{
			{"分组无效", strings.Replace(w2DraftAccountBody(groupID), groupID, "grp-missing", 1), "账户分组无效"},
			{"类型不支持", strings.Replace(w2DraftAccountBody(groupID), `"type":"api_key"`, `"type":"vertex"`, 1), "供应商 gpt 不支持账户类型 vertex"},
			{"检查模型不在支持列表", strings.Replace(w2DraftAccountBody(groupID), `"supportedModels":["gpt-4o-mini"]`, `"supportedModels":["other-model"]`, 1), "账户检查模型必须属于账户支持模型"},
			{"时间计划无效", strings.Replace(w2DraftAccountBody(groupID), `"healthCheckEndpointMode":"chat_json"`,
				`"healthCheckEndpointMode":"chat_json","availabilitySchedule":{"enabled":true,"mode":"weird"}`, 1), "时间计划"},
			{"images 形态缺目录证据", strings.Replace(w2DraftAccountBody(groupID), `"healthCheckEndpointMode":"chat_json"`, `"healthCheckEndpointMode":"images_json"`, 1), "Images API"},
			{"映射上游不在支持列表", strings.Replace(w2DraftAccountBody(groupID), `"supportedModels":["gpt-4o-mini"]`,
				`"supportedModels":["gpt-4o-mini"],"modelMappings":[{"sourceModel":"gpt-4o-mini","sourceEndpointFamily":"chat_completions","upstreamModel":"secret-model","upstreamEndpointFamily":"responses"}]`, 1), "上游模型"},
		}
		for _, testCase := range cases {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft", `{`+testCase.body+`}`)
			if code != http.StatusBadRequest {
				t.Fatalf("%s：期望 400，实际 %d %v", testCase.name, code, payload)
			}
			if !strings.Contains(payload["message"].(string), testCase.message) {
				t.Fatalf("%s：消息缺少 %q：%v", testCase.name, testCase.message, payload["message"])
			}
		}
	})
	t.Run("停用供应商", func(t *testing.T) {
		now := "2026-01-01T00:00:00.000Z"
		env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
			VALUES ('prov-off-w2', 'offprovider', '停用', 0, '["m1"]', ?, ?)`, now, now)
		env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
			protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
			capabilities_json, created_at, updated_at)
			VALUES ('profile_off_w2', 'offprovider', '停用档案', 1, 'openai', 'v1', 'https://api.openai.com/v1',
			'm1', '["api_key"]', '[]', ?, ?)`, now, now)
		env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
			VALUES ('grp-off-w2', ?, '停用供应商分组', 'offprovider', 1, 'personal', ?, ?)`, adminID, now, now)
		body := strings.Replace(w2DraftAccountBody(groupID), `"providerCode":"gpt","providerProtocolProfileId":"prof-gpt"`,
			`"providerCode":"offprovider","providerProtocolProfileId":"profile_off_w2"`, 1)
		body = strings.Replace(body, groupID, "grp-off-w2", 1)
		body = strings.Replace(body, `"supportedModels":["gpt-4o-mini"]`, `"supportedModels":["m1"]`, 1)
		body = strings.Replace(body, `"healthCheckModel":"gpt-4o-mini"`, `"healthCheckModel":"m1"`, 1)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft", `{`+body+`}`)
		if code != http.StatusBadRequest || payload["message"] != "供应商已停用：offprovider" {
			t.Fatalf("停用供应商 gate 不一致：%d %v", code, payload)
		}
	})
	t.Run("用户侧分组不可见", func(t *testing.T) {
		env.login(t, "user2", "user2-pass", "user")
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/test-draft",
			`{`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusBadRequest || payload["message"] != "账户分组无效" {
			t.Fatalf("用户侧分组 gate 不一致：%d %v", code, payload)
		}
	})
	t.Run("worker 不可用", func(t *testing.T) {
		env2, admin2, _ := w2DispatchEnv(t, false)
		code, payload := env2.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft",
			`{`+w2DraftAccountBody("grp-default-"+admin2)+`}`)
		if code != http.StatusServiceUnavailable || !strings.Contains(payload["message"].(string), "账号草稿测试任务未能投递") {
			t.Fatalf("草稿 503 契约不一致：%d %v", code, payload)
		}
	})
}

func TestW2TestTreeRoutes(t *testing.T) {
	env, _, accountID := w2DispatchEnv(t, true)

	// 建 session + 投递一个任务，作为树路由的读取对象。
	code, sessionPayload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions", "")
	sessionID := dataMap(t, sessionPayload)["id"].(string)
	code, taskPayload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test",
		`{"model":"gpt-4o-mini","testSessionId":"`+sessionID+`"}`)
	if code != http.StatusAccepted {
		t.Fatalf("任务投递失败：%d %v", code, taskPayload)
	}
	taskID := dataMap(t, taskPayload)["id"].(string)

	t.Run("状态读取", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-tasks/"+taskID, "")
		if code != http.StatusOK || dataMap(t, payload)["id"] != taskID {
			t.Fatalf("任务读取不一致：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-sessions/"+sessionID, "")
		if code != http.StatusOK {
			t.Fatalf("会话读取不一致：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-sessions/"+sessionID+"/tasks", "")
		if code != http.StatusOK {
			t.Fatalf("会话任务列表不一致：%d %v", code, payload)
		}
		if len(dataArray(t, payload)) != 1 {
			t.Fatalf("会话任务数量不一致：%v", payload)
		}
		// ids 批量列表。
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-tasks?ids="+taskID, "")
		if code != http.StatusOK || len(dataArray(t, payload)) != 1 {
			t.Fatalf("任务批量列表不一致：%d %v", code, payload)
		}
	})
	t.Run("404 契约", func(t *testing.T) {
		cases := []struct {
			name    string
			method  string
			path    string
			message string
		}{
			{"任务不存在", http.MethodGet, "/__aisys__/api/accounts/test-tasks/task-missing", "账户测试任务不存在"},
			{"会话不存在", http.MethodGet, "/__aisys__/api/accounts/test-sessions/sess-missing", "账户测试会话不存在"},
			{"会话任务不存在", http.MethodGet, "/__aisys__/api/accounts/test-sessions/sess-missing/tasks", "账户测试会话不存在"},
			{"取消任务不存在", http.MethodPost, "/__aisys__/api/accounts/test-tasks/task-missing/cancel", "账户测试任务不存在"},
			{"取消会话不存在", http.MethodPost, "/__aisys__/api/accounts/test-sessions/sess-missing/cancel", "账户测试会话不存在"},
			{"心跳会话不存在", http.MethodPost, "/__aisys__/api/accounts/test-sessions/sess-missing/heartbeat", "账户测试会话不存在"},
			{"未知树形状", http.MethodGet, "/__aisys__/api/accounts/test-sessions/x/bogus", "资源不存在"},
			{"未知树形状 POST", http.MethodPost, "/__aisys__/api/accounts/test-sessions/x/bogus", "资源不存在"},
		}
		for _, testCase := range cases {
			code, payload := env.do(t, testCase.method, testCase.path, "")
			if code != http.StatusNotFound {
				t.Fatalf("%s：期望 404，实际 %d %v", testCase.name, code, payload)
			}
			if testCase.message != "" && payload["message"] != testCase.message {
				t.Fatalf("%s：消息不一致：%v", testCase.name, payload["message"])
			}
		}
	})
	t.Run("范围查询校验", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-tasks?systemAccountId=", "")
		if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
			t.Fatalf("空 systemAccountId 校验不一致：%d %v", code, payload)
		}
	})
	t.Run("取消任务", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-tasks/"+taskID+"/cancel", "")
		if code != http.StatusOK {
			t.Fatalf("任务取消失败：%d %v", code, payload)
		}
		if dataMap(t, payload)["cancelRequested"] != true {
			t.Fatalf("取消标记缺失：%v", payload)
		}
	})
	t.Run("取消会话", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions/"+sessionID+"/cancel", "")
		if code != http.StatusOK {
			t.Fatalf("会话取消失败：%d %v", code, payload)
		}
		if dataMap(t, payload)["status"] != "canceled" {
			t.Fatalf("会话状态不一致：%v", payload)
		}
	})
	t.Run("用户侧不可见他人任务", func(t *testing.T) {
		env.login(t, "user3", "user3-pass", "user")
		code, _ := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/test-tasks/"+taskID, "")
		if code != http.StatusNotFound {
			t.Fatalf("他人任务应不可见：%d", code)
		}
	})
}

func TestW2TestDraftAvailabilityGate(t *testing.T) {
	// 已保存账户的可用性 gate：disabled 账户在 owner 模式仍可测（gate 只作用于
	// authorized 实例），owner 直接投递成功。
	env, _, accountID := w2DispatchEnv(t, true)
	env.exec(t, `UPDATE accounts SET status = 'disabled' WHERE id = ?`, accountID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test", `{"model":"gpt-4o-mini"}`)
	if code != http.StatusAccepted {
		t.Fatalf("disabled 账户 owner 测试应放行：%d %v", code, payload)
	}
	_ = fmt.Sprint()
}
