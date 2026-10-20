package accounts

// w13g test_dispatch_routes.go 深度补测：树形子路由作用域、手工测试入口
// 错误臂、请求体解析矩阵、草稿快照准备链的拒绝分支与纯函数。
//
// 不可达登记（w13g）：
// - 1018-1019 ownerSystemAccountID=="" 的第一跳：groups.system_account_id
//   NOT NULL 且 fixture 恒非空，正常数据无法为空串（空串场景已通过直接
//   UPDATE 覆盖后续两跳，但 SQLite NOT NULL 允许 ''，该臂实际可达，见
//   TestW13GDraftOwnerFallback；此登记作废）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

func TestW13GTestTreeSegmentsAndScopes(t *testing.T) {
	// testTreeSegments 纯函数矩阵。
	exact := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts", nil)
	if segs, ok := testTreeSegments(exact, "/accounts"); !ok || segs != nil {
		t.Fatalf("精确前缀：%v %v", segs, ok)
	}
	outside := httptest.NewRequest(http.MethodGet, "/__aisys__/api/other/x", nil)
	if _, ok := testTreeSegments(outside, "/accounts"); ok {
		t.Fatal("前缀外应 false")
	}
	deep := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/a//b/", nil)
	segs, ok := testTreeSegments(deep, "/my-accounts")
	if !ok || len(segs) != 3 || segs[0] != "a" || segs[2] != "b" {
		t.Fatalf("多段裁剪：%v %v", segs, ok)
	}

	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	// 空白 systemAccountId 查询 → 400（GET 与 POST 两个树形入口）。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-tasks/x?systemAccountId=%20", ""); code != http.StatusBadRequest {
		t.Fatalf("GET 空白作用域应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions/s/heartbeat?systemAccountId=%20", ""); code != http.StatusBadRequest {
		t.Fatalf("POST 空白作用域应 400：%d %v", code, payload)
	}
	// my-accounts POST 树：未知形状 → 404。
	if code, _ := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/test-bogus/x/y", ""); code != http.StatusNotFound {
		t.Fatalf("自面 POST 未知形状应 404：%d", code)
	}
	// 自面 GET 空白作用域 → 400。
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/test-tasks/x?systemAccountId=%20", ""); code != http.StatusBadRequest {
		t.Fatalf("自面 GET 空白作用域应 400：%d", code)
	}
}

func TestW13GTestOptionsRouteArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-to", adminID, "w13g-to", "active")
	base := "/__aisys__/api/accounts"

	// 无效 limit → 400 文案。
	if code, payload := env.do(t, http.MethodGet, base+"/acc-w13g-to/test-options?limit=abc", ""); code != http.StatusBadRequest || payload["message"] != "limit 必须是 1 到 50 的整数" {
		t.Fatalf("limit 文案：%d %v", code, payload)
	}
	// 缺失账户 → 404。
	if code, _ := env.do(t, http.MethodGet, base+"/acc-w13g-none/test-options", ""); code != http.StatusNotFound {
		t.Fatal("缺失账户应 404")
	}
	// 空凭据行 → context nil → 404。
	env.seedAccount(t, "acc-w13g-to2", adminID, "w13g-to2", "active")
	env.exec(t, `UPDATE accounts SET credentials_encrypted = '' WHERE id = 'acc-w13g-to2'`)
	if code, _ := env.do(t, http.MethodGet, base+"/acc-w13g-to2/test-options", ""); code != http.StatusNotFound {
		t.Fatal("空凭据应 404")
	}
	// store 错误 → 500（B4 W2：读面已切 account_entry_mappings）。
	env.exec(t, `DROP TABLE account_entry_mappings`)
	if code, _ := env.do(t, http.MethodGet, base+"/acc-w13g-to/test-options", ""); code != http.StatusInternalServerError {
		t.Fatal("store 错误应 500")
	}
	// 模型能力路由：缺失账户 → 404；空凭据 → 404；store 错误 → 500。
	t.Run("model-route", func(t *testing.T) {
		env2 := newTestEnv(t)
		admin2 := env2.login(t, "root", "root-pass", "super_admin")
		env2.seedProviderAndDefaultGroup(t, admin2)
		if code, _ := env2.do(t, http.MethodGet, base+"/acc-w13g-none/test-options/models/m1", ""); code != http.StatusNotFound {
			t.Fatal("模型能力缺失账户应 404")
		}
		env2.seedAccount(t, "acc-w13g-to3", admin2, "w13g-to3", "active")
		env2.exec(t, `UPDATE accounts SET credentials_encrypted = '' WHERE id = 'acc-w13g-to3'`)
		if code, _ := env2.do(t, http.MethodGet, base+"/acc-w13g-to3/test-options/models/m1", ""); code != http.StatusNotFound {
			t.Fatal("模型能力空凭据应 404")
		}
		env2.seedAccount(t, "acc-w13g-to4", admin2, "w13g-to4", "active")
		env2.exec(t, `DROP TABLE account_entry_mappings`)
		if code, _ := env2.do(t, http.MethodGet, base+"/acc-w13g-to4/test-options/models/m1", ""); code != http.StatusInternalServerError {
			t.Fatal("模型能力 store 错误应 500")
		}
	})
}

func TestW13GTestAccountAuthAndDraftGates(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-ta", adminID, "w13g-ta", "active")
	base := "/__aisys__/api/accounts"

	// 未登录 → 401（test 与 test-draft 两个入口）。
	for _, path := range []string{base + "/acc-w13g-ta/test", base + "/test-draft"} {
		request, err := http.NewRequest(http.MethodPost, env.server.URL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401：%d", path, response.StatusCode)
		}
	}

	// 非网关协议供应商 → 400 协议文案。
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-w13g-x', 'wx', 'X', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof-w13g-x', 'wx', 'X 协议', 1, 'wxp', 'v1', 'https://x.example/v1', 'm-x', '["api_key"]', '[]',
		'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	env.seedAccount(t, "acc-w13g-tx", adminID, "w13g-tx", "active")
	env.exec(t, `UPDATE accounts SET vendor_code = NULL, connection_driver_code = 'generic_http',
		protocol_code = 'wxp' WHERE id = 'acc-w13g-tx'`)
	if code, payload := env.do(t, http.MethodPost, base+"/acc-w13g-tx/test", `{}`); code != http.StatusBadRequest || payload["message"] != unsupportedGatewayProtocolTestMessage {
		t.Fatalf("协议拒绝：%d %v", code, payload)
	}

	// 草稿保存路径(saved form)：授权实例走授权环境（m10 端口装配）。
	// 详见 TestW13GTestAccountAuthorizedGates。

	// owner 账户草稿与当前账户不一致（provider/type）→ 400。
	env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w13g-tb", adminID, "w13g-tb", "active")
	if code, payload := env.do(t, http.MethodPost, base+"/acc-w13g-tb/test", `{"account":{
		"providerCode":"other","providerProtocolProfileId":"prof-gpt","name":"x","type":"api_key",
		"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json","groupId":"grp-default-`+adminID+`"}}`); code != http.StatusBadRequest || payload["message"] != "账户测试草稿与当前账户不一致" {
		t.Fatalf("provider 不一致：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPost, base+"/acc-w13g-tb/test", `{"account":{
		"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"x","type":"oauth",
		"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json","groupId":"grp-default-`+adminID+`"}}`); code != http.StatusBadRequest || payload["message"] != "账户测试草稿与当前账户不一致" {
		t.Fatalf("type 不一致：%d %v", code, payload)
	}
	// profile 行已随目标 schema 退役（§9.3）：入参 providerProtocolProfileId
	// 忽略（协议上下文由 vendor 语义推导供给），「账户测试草稿与当前账户协议
	// 档案不一致」pin 比对门随 profile 维度一并拆除——saved 草稿携带任意档案
	// ID 不再 400，一致性契约由 providerCode/type 相等断言承载（上方两臂）。
	access := AccessScope{ViewerID: adminID, IsAdmin: true}
	view, err := env.store.FindAccountForTestView(context.Background(), access, "acc-w13g-tb")
	if err != nil || view == nil {
		t.Fatalf("测试视图加载：%v %v", view, err)
	}
	draftInput, message := parseTestDraftAccountInput(map[string]any{
		"providerCode": "gpt", "providerProtocolProfileId": "prof-w13g-bogus", "name": "x", "type": "api_key",
		"healthCheckModel": "gpt-4o-mini", "healthCheckEndpointMode": "chat_json",
		"groupId":         "grp-default-" + adminID,
		"supportedModels": []any{"gpt-4o-mini"},
		"credentials":     map[string]any{"api_key": "sk-x", "base_url": "https://api.openai.com/v1"},
	})
	if message != "" {
		t.Fatalf("草稿解析：%q", message)
	}
	draft, err := env.store.savedAccountDraftTestSnapshot(context.Background(), view, draftInput, access)
	if err != nil {
		t.Fatalf("异档案 ID 的 saved 草稿应通过（pin 已忽略）：%v", err)
	}
	if draft.StateTargetAccountID == nil || *draft.StateTargetAccountID != "acc-w13g-tb" {
		t.Fatalf("state target 未落：%+v", draft.StateTargetAccountID)
	}
	// FindAccountForTestView 的 ListPage 错误 → 500。
	env.exec(t, `DROP TABLE groups`)
	if code, _ := env.do(t, http.MethodPost, base+"/acc-w13g-tb/test", `{}`); code != http.StatusInternalServerError {
		t.Fatal("ListPage 错误应 500")
	}
}

func TestW13GTestAccountAuthorizedGates(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ownerID := env.login(t, "owner-w13g", "owner-pass", "user")
	memberID := env.login(t, "member-w13g", "member-pass", "user")
	env.seedProviderAndDefaultGroup(t, ownerID)
	env.seedAccount(t, "acc-w13g-src", ownerID, "w13g-源账户", "active")
	env.seedProviderAndDefaultGroup(t, memberID)
	env.seedTeamMember(t, "team-w13g", ownerID, memberID)
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-w13g-src",
		GranteeType: "team", GranteeID: "team-w13g",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-w13g-src'`, memberID)
	if runtimeID == "" {
		t.Fatal("team runtime row missing")
	}
	env.seedAuthorizationInstance(t, "acc-w13g-inst", memberID, runtimeID, "acc-w13g-src")
	env.login(t, "member-w13g", "member-pass", "user")

	// 授权实例 error 态：availability 不可用 → 400 带理由文案。
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e', last_error_message = '授权实例异常' WHERE id = 'acc-w13g-inst'`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-w13g-inst/test", `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("授权实例不可用应 400：%d %v", code, payload)
	}

	// 恢复 active：带 saved account 表单 → 授权账户拒绝草稿表单（diagnostics
	// 走 limited 臂）。
	env.exec(t, `UPDATE accounts SET status = 'active', last_error_code = NULL, last_error_message = NULL WHERE id = 'acc-w13g-inst'`)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-w13g-inst/test", `{"account":{
		"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"x","type":"api_key",
		"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json","groupId":"grp-default-`+ownerID+`"}}`)
	if code != http.StatusBadRequest || payload["message"] != "授权账户测试不支持使用未保存表单配置" {
		t.Fatalf("授权草稿拒绝：%d %v", code, payload)
	}
}

func TestW13GTestSessionRouteErrors(t *testing.T) {
	// 建会话 store 错 → 500（test 家族表用 newTestFamilyEnv 的扩展 schema）。
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	env.login(t, "root", "root-pass", "super_admin")
	base := "/__aisys__/api/accounts"
	env.exec(t, `DROP TABLE account_test_sessions`)
	if code, _ := env.do(t, http.MethodPost, base+"/test-sessions", ""); code != http.StatusInternalServerError {
		t.Fatal("建会话错误应 500")
	}

	// 会话/任务读取错误 → 500（heartbeat/complete/cancel/get/detail/list）。
	env2 := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	env2.login(t, "root", "root-pass", "super_admin")
	env2.exec(t, `DROP TABLE account_test_sessions`)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, base + "/test-sessions/s1/heartbeat"},
		{http.MethodPost, base + "/test-sessions/s1/complete"},
		{http.MethodPost, base + "/test-sessions/s1/cancel"},
		{http.MethodGet, base + "/test-sessions/s1"},
		{http.MethodGet, base + "/test-sessions/s1/tasks"},
	} {
		if code, _ := env2.do(t, route.method, route.path, ""); code != http.StatusInternalServerError {
			t.Fatalf("%s 会话 store 错误应 500：%d", route.path, code)
		}
	}
	env3 := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	env3.login(t, "root", "root-pass", "super_admin")
	env3.exec(t, `DROP TABLE account_test_tasks`)
	if code, _ := env3.do(t, http.MethodGet, base+"/test-tasks?ids=t1", ""); code != http.StatusInternalServerError {
		t.Fatal("任务列表错误应 500")
	}
	if code, _ := env3.do(t, http.MethodGet, base+"/test-tasks/t1", ""); code != http.StatusInternalServerError {
		t.Fatal("任务查询错误应 500")
	}
	if code, _ := env3.do(t, http.MethodPost, base+"/test-tasks/t1/cancel", ""); code != http.StatusInternalServerError {
		t.Fatal("任务取消错误应 500")
	}
}

func TestW13GParseTestBodies(t *testing.T) {
	// 未知顶层键、类型错误与端点模式枚举矩阵。
	bad := []map[string]any{
		{"bogus": 1},
		{"model": 1},
		{"testEndpointMode": 1},
		{"testEndpointMode": "bogus_mode"},
		{"prompt": 1},
		{"testSessionId": 1},
		{"testSessionId": "   "},
		{"account": 1},
	}
	for _, body := range bad {
		if _, message := parseTestRequestBody(body); message == "" {
			t.Fatalf("应拒绝：%v", body)
		}
	}
	// 合法负载。
	parsed, message := parseTestRequestBody(map[string]any{
		"model": " gpt-4o-mini ", "testEndpointMode": " chat_sse", "prompt": "p",
		"testSessionId": " sess-1 ", "account": map[string]any{
			"providerCode": "gpt", "providerProtocolProfileId": "prof-gpt", "name": "n", "type": "api_key",
			"healthCheckModel": "m", "healthCheckEndpointMode": "chat_json", "groupId": "g1",
		}})
	if message != "" || parsed.Model != "gpt-4o-mini" || parsed.TestEndpointMode != "chat_sse" ||
		parsed.TestSessionID != "sess-1" || parsed.Account == nil || parsed.Account.GroupID != "g1" {
		t.Fatalf("合法负载：%+v %q", parsed, message)
	}
	// providerProtocolProfileId 不再必填（§9.3 profile 退役，推导供给；
	// 携带时保持接受但忽略）。
	withoutProfile := map[string]any{
		"providerCode": "gpt", "name": "n", "type": "api_key",
		"healthCheckModel": "m", "healthCheckEndpointMode": "chat_json", "groupId": "g1",
	}
	if omitted, message := parseTestDraftAccountInput(withoutProfile); message != "" || omitted == nil {
		t.Fatalf("无档案 ID 草稿应解析通过：%+v %q", omitted, message)
	}

	// 草稿账户字段矩阵。
	accountBad := []map[string]any{
		{"bogus": 1},
		{"providerCode": 1},
		{"healthCheckEndpointMode": "bogus"},
		{"healthCheckEndpointMode": 1},
		{"providerCode": "", "groupId": "g"},
		{"groupId": 1},
		{"groupId": "  "},
		{"credentials": 1},
		{"supportedModels": 1},
		{"supportedModels": []any{}},
		{"supportedModels": []any{1}},
		{"supportedModels": []any{" "}},
		{"modelMappings": 1},
		{"modelMappings": []any{1}},
		{"modelMappings": []any{map[string]any{"bogus": 1}}},
		{"concurrencyLimit": 0.5},
		{"concurrencyLimit": float64(0)},
		{"priority": -1.0},
		{"priority": 0.5},
		{"superPriorityEnabled": 1},
		{"fallbackEnabled": 1},
		{"proxyProfileId": 1},
		{"accountExpiresAt": 1},
		{"availabilitySchedule": 1},
		{"notes": 1},
	}
	required := map[string]any{
		"providerCode": "gpt", "providerProtocolProfileId": "prof-gpt", "name": "n", "type": "api_key",
		"healthCheckModel": "m", "healthCheckEndpointMode": "chat_json", "groupId": "g1",
	}
	for _, override := range accountBad {
		record := map[string]any{}
		for key, value := range required {
			record[key] = value
		}
		for key, value := range override {
			record[key] = value
		}
		if _, message := parseTestDraftAccountInput(record); message == "" {
			t.Fatalf("草稿字段应拒绝：%v", override)
		}
	}
	// 可选字段合法值与空值清空。
	full := map[string]any{
		"providerCode": "gpt", "providerProtocolProfileId": "prof-gpt", "name": " n ", "type": "api_key",
		"healthCheckModel": "m", "healthCheckEndpointMode": "chat_json", "groupId": " g1 ",
		"credentials":      map[string]any{"api_key": "sk"},
		"supportedModels":  []any{" a ", "a"},
		"modelMappings":    []any{},
		"concurrencyLimit": float64(5), "priority": float64(2),
		"superPriorityEnabled": true, "fallbackEnabled": false,
		"proxyProfileId": " pp ", "accountExpiresAt": " 2026-02-02 ",
		"availabilitySchedule": map[string]any{"type": "always"}, "notes": "note",
	}
	input, message := parseTestDraftAccountInput(full)
	if message != "" || input == nil {
		t.Fatalf("完整草稿：%q", message)
	}
	if input.Name != "n" || input.GroupID != "g1" || len(input.SupportedModels) != 2 ||
		input.ConcurrencyLimit == nil || *input.ConcurrencyLimit != 5 ||
		input.Priority == nil || *input.Priority != 2 ||
		input.SuperPriorityEnabled == nil || !*input.SuperPriorityEnabled ||
		input.FallbackEnabled == nil || *input.FallbackEnabled ||
		input.ProxyProfileID == nil || *input.ProxyProfileID != "pp" ||
		input.AccountExpiresAt == nil || *input.AccountExpiresAt != "2026-02-02" ||
		input.Notes == nil || *input.Notes != "note" || input.AvailabilitySchedule == nil {
		t.Fatalf("草稿规范化：%+v", input)
	}
	// null 形态清空可选字段。
	clearable := map[string]any{
		"providerCode": "gpt", "providerProtocolProfileId": "prof-gpt", "name": "n", "type": "api_key",
		"healthCheckModel": "m", "healthCheckEndpointMode": "chat_json", "groupId": "g1",
		"proxyProfileId": nil, "accountExpiresAt": nil, "notes": nil, "availabilitySchedule": nil,
		"credentials": nil, "supportedModels": nil, "modelMappings": nil,
	}
	input, message = parseTestDraftAccountInput(clearable)
	if message != "" || input == nil || input.ProxyProfileID != nil || input.AccountExpiresAt != nil ||
		input.Notes != nil || input.AvailabilitySchedule != nil || input.Credentials != nil ||
		input.SupportedModels != nil || input.ModelMappings != nil {
		t.Fatalf("null 清空：%+v %q", input, message)
	}
	// firstNonEmptyTextValue 全空。
	if firstNonEmptyTextValue("", " ") != "" {
		t.Fatal("全空应返回空串")
	}
}

func TestW13GDraftSnapshotPreparationArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	groupID := "grp-default-" + adminID
	access := AccessScope{ViewerID: adminID, IsAdmin: true}
	build := func(mutate func(*TestDraftAccountInput)) *TestDraftAccountInput {
		input := &TestDraftAccountInput{
			// providerProtocolProfileId 已不是入参契约（§9.3 推导供给）：
			// 恒空提交，协议上下文由 vendor 语义推导。
			ProviderCode: "gpt", Name: "d1", Type: "api_key",
			HealthCheckModel: "gpt-4o-mini", HealthCheckEndpointMode: "chat_json", GroupID: groupID,
			Credentials:     Credentials{"api_key": "sk-w13g-d", "base_url": "https://api.openai.com/v1"},
			SupportedModels: []string{"gpt-4o-mini"},
		}
		if mutate != nil {
			mutate(input)
		}
		return input
	}

	cases := []struct {
		name   string
		mutate func(*TestDraftAccountInput)
		want   string
	}{
		{"不存在的分组", func(in *TestDraftAccountInput) { in.GroupID = "grp-w13g-none" }, "账户分组无效"},
		{"坏凭据", func(in *TestDraftAccountInput) {
			in.Credentials = Credentials{"api_key": " ", "base_url": "https://api.openai.com/v1"}
		}, "API Key不能为空"},
		{"坏调度", func(in *TestDraftAccountInput) {
			in.AvailabilitySchedule = map[string]any{"enabled": true, "mode": "bogus"}
		}, "API Key 时间计划"},
		{"检查模型空", func(in *TestDraftAccountInput) { in.HealthCheckModel = " " }, "账户检查模型不能为空"},
		{"检查模型不属于支持集", func(in *TestDraftAccountInput) {
			in.SupportedModels = []string{"m2"}
		}, "账户检查模型必须属于账户支持模型"},
		{"映射上游不允许", func(in *TestDraftAccountInput) {
			in.ModelMappings = []ModelMapping{{SourceModel: "gpt-4o-mini", SourceEndpointFamily: "chat_completions",
				UpstreamModel: "nope", UpstreamEndpointFamily: "chat_completions"}}
		}, ""},
		{"空模型集", func(in *TestDraftAccountInput) { in.SupportedModels = nil }, "账户支持模型不能为空"},
	}
	for _, c := range cases {
		_, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), build(c.mutate), access, "")
		if c.want != "" {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s：%v", c.name, err)
			}
		} else if err == nil {
			t.Fatalf("%s：应报错", c.name)
		}
	}

	// 正常链路：默认模型回落、数值覆盖、调度序列化。
	validSchedule := map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows": []any{map[string]any{"daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)}, "start": "00:00", "end": "23:59"}},
	}
	rich := build(func(in *TestDraftAccountInput) {
		limit, priority := 9, 3
		super, fallback := true, true
		expires, notes := "2026-03-03T00:00:00Z", "n"
		in.ConcurrencyLimit = &limit
		in.Priority = &priority
		in.SuperPriorityEnabled = &super
		in.FallbackEnabled = &fallback
		in.AccountExpiresAt = &expires
		in.Notes = &notes
		in.AvailabilitySchedule = validSchedule
	})
	draft, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), rich, access, "draft-acc-w13g")
	if err != nil {
		t.Fatal(err)
	}
	if draft.ID != "draft-acc-w13g" || draft.ConcurrencyLimit != 9 || draft.Priority != 3 ||
		!draft.SuperPriorityEnabled || !draft.FallbackEnabled || draft.Notes == nil ||
		draft.AvailabilitySchedule == nil || draft.AvailabilityScheduleJSON == nil ||
		draft.ProviderProtocolProfileID == nil || *draft.ProviderProtocolProfileID != accountscore.GptOpenAIV1ProfileID ||
		draft.ProtocolCode == nil || draft.ProtocolVersion == nil {
		t.Fatalf("草稿快照：%+v", draft)
	}

	// images_json 检查模式：目录读取失败（B4 ⑧ 后目录源是 models 单表，DROP
	// 制造读错误）→ 传播。
	env.exec(t, `DROP TABLE models`)
	images := build(func(in *TestDraftAccountInput) { in.HealthCheckEndpointMode = "images_json" })
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), images, access, ""); err == nil {
		t.Fatal("images 目录读错误应报错")
	}

	// 分组边界：enabled=0、非 owner、空 ID、读错误。
	ownerless := build(nil)
	env.exec(t, `UPDATE groups SET system_account_id = '' WHERE id = ?`, groupID)
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), ownerless, access, ""); err != nil {
		t.Fatalf("空 owner 应回落到 ViewerID：%v", err)
	}
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), ownerless, AccessScope{}, ""); err == nil ||
		!strings.Contains(err.Error(), "账户分组缺少归属用户") {
		t.Fatalf("无 owner 草稿：%v", err)
	}
	env.exec(t, `UPDATE groups SET system_account_id = ? WHERE id = ?`, adminID, groupID)
	env.exec(t, `UPDATE groups SET enabled = 0 WHERE id = ?`, groupID)
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), ownerless, access, ""); err == nil ||
		!strings.Contains(err.Error(), "账户分组无效") {
		t.Fatalf("停用分组：%v", err)
	}
	env.exec(t, `UPDATE groups SET enabled = 1 WHERE id = ?`, groupID)
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), ownerless,
		AccessScope{ViewerID: "someone-else"}, ""); err == nil || !strings.Contains(err.Error(), "账户分组无效") {
		t.Fatalf("非 owner：%v", err)
	}
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), build(func(in *TestDraftAccountInput) {
		in.GroupID = " "
	}), access, ""); err == nil {
		t.Fatal("空分组 ID 应报错")
	}
	env.exec(t, `DROP TABLE groups`)
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), ownerless, access, ""); err == nil {
		t.Fatal("分组读错误应报错")
	}
}

func TestW13GDraftVendorGateArms(t *testing.T) {
	// draftProviderProfile（providers/profiles JOIN 门）已随 profile 维度拆除，
	// 供应商归属校验收敛为 assertVendorEnabled（vendors 表，§2.1）。
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	groupID := "grp-default-" + adminID
	access := AccessScope{ViewerID: adminID, IsAdmin: true}
	build := func(providerCode string) *TestDraftAccountInput {
		return &TestDraftAccountInput{
			ProviderCode: providerCode, Name: "d1", Type: "api_key",
			HealthCheckModel: "gpt-4o-mini", HealthCheckEndpointMode: "chat_json", GroupID: groupID,
			Credentials:     Credentials{"api_key": "sk-w13g-v", "base_url": "https://api.openai.com/v1"},
			SupportedModels: []string{"gpt-4o-mini"},
		}
	}
	// 真实厂商缺 vendors 行 → 不支持的供应商（夹具只种 gpt）。
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), build("xai"), access, ""); err == nil ||
		!strings.Contains(err.Error(), "不支持的供应商：xai") {
		t.Fatalf("缺 vendors 行：%v", err)
	}
	// 真实厂商 vendors 停用 → 供应商已停用。
	env.exec(t, `UPDATE vendors SET enabled = 0 WHERE code = 'gpt'`)
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), build("gpt"), access, ""); err == nil ||
		!strings.Contains(err.Error(), "供应商已停用：gpt") {
		t.Fatalf("供应商停用：%v", err)
	}
	env.exec(t, `UPDATE vendors SET enabled = 1 WHERE code = 'gpt'`)
	// generic 语义（openai，无 vendors 行）跳过 vendor 门，推导 openai 族通过。
	draft, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), build("openai"), access, "")
	if err != nil {
		t.Fatalf("generic 语义应通过：%v", err)
	}
	if draft.ProviderProtocolProfileID == nil || *draft.ProviderProtocolProfileID != "" ||
		draft.ProtocolCode == nil || *draft.ProtocolCode != "openai" {
		t.Fatalf("generic 推导：%+v %v", draft.ProviderProtocolProfileID, draft.ProtocolCode)
	}
	// vendors 读错误 → 传播。
	env.exec(t, `DROP TABLE vendors`)
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), build("gpt"), access, ""); err == nil {
		t.Fatal("vendors 读错误应报错")
	}
}

func TestW13GDraftOAuthAndHelpers(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	groupID := "grp-default-" + adminID
	access := AccessScope{ViewerID: adminID, IsAdmin: true}

	// oauth 草稿无 base_url → 合并 vendor 默认 preset 的 registry 根（§1.1）。
	// gpt → OpenAI 官方根。
	record := map[string]any{
		"providerCode": "gpt", "providerProtocolProfileId": "prof-gpt", "name": "o1", "type": "oauth",
		"healthCheckModel": "gpt-4o-mini", "healthCheckEndpointMode": "responses_json", "groupId": groupID,
		"credentials":     map[string]any{"refresh_token": "r"},
		"supportedModels": []any{"gpt-4o-mini"},
	}
	input, message := parseTestDraftAccountInput(record)
	if message != "" {
		t.Fatalf("oauth 构造：%q", message)
	}
	draft, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), input, access, "")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Credentials["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("oauth base_url 合并：%v", draft.Credentials["base_url"])
	}

	// 问题 B：anthropic / xai OAuth 草稿缺省 base_url 得到各自上游根（不再是
	// 恒 OpenAI 官方根）。两例均不带 providerProtocolProfileId（入参已非契约）。
	env.exec(t, `INSERT OR IGNORE INTO vendors (id, code, name, enabled, created_at, updated_at)
		VALUES ('ven-w13g-anthropic', 'anthropic', 'Anthropic', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	env.exec(t, `INSERT OR IGNORE INTO vendors (id, code, name, enabled, created_at, updated_at)
		VALUES ('ven-w13g-xai', 'xai', 'xAI', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	vendorCases := []struct {
		providerCode, name, model, endpointMode, wantBaseURL string
	}{
		{"anthropic", "o-anthropic", "claude-fixture", "messages_json", "https://api.anthropic.com/v1"},
		{"xai", "o-xai", "grok-fixture", "responses_json", "https://api.x.ai/v1"},
	}
	for _, vendorCase := range vendorCases {
		credentials := map[string]any{"refresh_token": "r"}
		if vendorCase.providerCode == "anthropic" {
			// anthropic 协议 oauth 凭据校验要求 access_token（credentials_normalize.go）。
			credentials["access_token"] = "t"
		}
		vendorRecord := map[string]any{
			"providerCode": vendorCase.providerCode, "name": vendorCase.name, "type": "oauth",
			"healthCheckModel": vendorCase.model, "healthCheckEndpointMode": vendorCase.endpointMode,
			"groupId":         groupID,
			"credentials":     credentials,
			"supportedModels": []any{vendorCase.model},
		}
		vendorInput, message := parseTestDraftAccountInput(vendorRecord)
		if message != "" {
			t.Fatalf("%s oauth 构造：%q", vendorCase.providerCode, message)
		}
		vendorDraft, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), vendorInput, access, "")
		if err != nil {
			t.Fatalf("%s oauth 草稿：%v", vendorCase.providerCode, err)
		}
		if vendorDraft.Credentials["base_url"] != vendorCase.wantBaseURL {
			t.Fatalf("%s oauth 缺省 base_url = %v, want %v", vendorCase.providerCode,
				vendorDraft.Credentials["base_url"], vendorCase.wantBaseURL)
		}
	}

	// 纯函数：scheduleToMap / scheduleToJSONString / normalizeDraftTextList /
	// credentialTextPresent。
	schedule := &AvailabilitySchedule{Enabled: true, Timezone: "UTC", Mode: scheduleModeAllowWindows,
		Windows: []ScheduleWindow{{DaysOfWeek: []int{1, 2, 3, 4, 5, 6, 7}, Start: "00:00", End: "23:59"}}}
	if scheduleToMap(nil) != nil {
		t.Fatal("nil schedule map")
	}
	if out := scheduleToMap(schedule); out == nil || out["mode"] != scheduleModeAllowWindows {
		t.Fatalf("schedule map：%v", out)
	}
	if scheduleToJSONString(nil) != nil {
		t.Fatal("nil schedule json")
	}
	if scheduleToJSONString("not-a-schedule") != nil {
		t.Fatal("非 schedule json")
	}
	if text := scheduleToJSONString(schedule); text == nil || !strings.Contains(*text, scheduleModeAllowWindows) {
		t.Fatalf("schedule json：%v", text)
	}
	if list := normalizeDraftTextList([]string{" a ", "a", "", "b"}); len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Fatalf("draft 文本列表：%v", list)
	}
	if credentialTextPresent(1) || credentialTextPresent("  ") || !credentialTextPresent(" x ") {
		t.Fatal("credentialTextPresent")
	}
}
