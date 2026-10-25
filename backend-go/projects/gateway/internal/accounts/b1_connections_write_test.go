package accounts

// B1 断点批（供应商与上游协议面重构设计 §2.1/§8）行为级测试：
//   - 携 connections + entryMappings + 健康检查三件套的创建全流程落库
//     （#B1 完成判定用例：account_connections / account_endpoint_modes /
//     account_entry_mappings / health 三字段事务闭环）；
//   - 不带 connections / 三件套的创建载荷显式 400；
//   - 连接行违规（registry 组合、重复 preset、mode 词表、base URL validator）400；
//   - validator 表驱动（§2.1 base_url 规则全集）；
//   - 克隆复制源连接行（id 重新生成、health 三字段指向新连接行）；
//   - 导入旧格式（无 connections）报错清单显式失败。

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// b1CreatePayload 构造携连接集合 + 入口映射 + 健康检查三件套的 gpt 创建载荷。
func b1CreatePayload(name string) string {
	return `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"` + name +
		`","type":"api_key","credentials":{"api_key":"sk-b1-secret-1234567890","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini","gpt-4.1"],"status":"active",` +
		`"connections":[{"connectionPresetCode":"gpt_openai_v1","protocolSurfaceCode":"openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],` +
		`"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini",` +
		`"modelMappings":[{"sourceModel":"client-model","sourceEndpointFamily":"responses","upstreamModel":"gpt-4o-mini","upstreamEndpointFamily":"chat_completions","targetConnectionPresetCode":"gpt_openai_v1","enabled":true}]}`
}

// TestB1CreateWritesConnectionRows 是 #B1 完成判定用例：携 connections +
// mappings + healthCheck 三件套的创建全流程 2xx，四张关系表行完整且相互解析
//（连接行 → mode 行 → 映射 target_connection_id → health_check_connection_id）。
func TestB1CreateWritesConnectionRows(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", b1CreatePayload("b1-full"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	// 连接行：确定性 ID、preset 自然键、registry surface、base_url、enabled。
	connectionID := env.queryCell(t, `SELECT id FROM account_connections WHERE account_id = ?`, id)
	if !strings.HasPrefix(connectionID, "conn-") {
		t.Fatalf("connection id 前缀：%q", connectionID)
	}
	if got := env.queryCell(t, `SELECT connection_preset_code FROM account_connections WHERE id = ?`, connectionID); got != "gpt_openai_v1" {
		t.Fatalf("connection preset：%q", got)
	}
	if got := env.queryCell(t, `SELECT protocol_surface_code FROM account_connections WHERE id = ?`, connectionID); got != "openai_v1" {
		t.Fatalf("connection surface：%q", got)
	}
	if got := env.queryCell(t, `SELECT base_url FROM account_connections WHERE id = ?`, connectionID); got != "https://api.openai.com/v1" {
		t.Fatalf("connection base_url：%q", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_connections WHERE account_id = ? AND enabled = 1`, id); got != 1 {
		t.Fatalf("enabled 连接行数：%d", got)
	}

	// endpoint mode 行：请求声明集原样落库。
	if got := env.count(t, `SELECT COUNT(*) FROM account_endpoint_modes WHERE connection_id = ?`, connectionID); got != 2 {
		t.Fatalf("endpoint mode 行数：%d", got)
	}

	// 入口映射：target_connection_id 解析到本事务写入的连接行（#B1 断点修复）。
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ? AND target_connection_id = ?
		AND source_model = 'client-model' AND source_endpoint_family = 'responses'
		AND target_endpoint_family = 'chat_completions' AND enabled = 1`, id, connectionID); got != 1 {
		t.Fatalf("入口映射未解析到新连接行：%d", got)
	}

	// health-check 三字段事务闭环：全部非 NULL 且指向 enabled 连接行。
	var healthConnection, healthMode, healthModel string
	if err := env.db.QueryRow(`SELECT health_check_connection_id, health_check_endpoint_mode, health_check_model
		FROM accounts WHERE id = $1`, id).Scan(&healthConnection, &healthMode, &healthModel); err != nil {
		t.Fatal(err)
	}
	if healthConnection != connectionID || healthMode != "chat_json" || healthModel != "gpt-4o-mini" {
		t.Fatalf("health 三字段不完整：%q %q %q", healthConnection, healthMode, healthModel)
	}
}

// TestB1CreateRequiresConnectionsAndHealthTrio：不带 connections / 三件套的
// 创建载荷显式 400，错误文案列明缺失字段（设计 §8）。
func TestB1CreateRequiresConnectionsAndHealthTrio(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"缺 connections", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"b1-missing-conn","type":"api_key",
			"credentials":{"api_key":"sk-b1-secret-1234567890","base_url":"https://api.openai.com/v1"},
			"supportedModels":["gpt-4o-mini"],"status":"active",
			"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"}`,
			"创建载荷缺少 connections"},
		{"缺 healthCheckConnectionPresetCode", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"b1-missing-preset","type":"api_key",
			"credentials":{"api_key":"sk-b1-secret-1234567890","base_url":"https://api.openai.com/v1"},
			"supportedModels":["gpt-4o-mini"],"status":"active",
			"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],
			"healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"}`,
			"创建载荷缺少 healthCheckConnectionPresetCode"},
		{"缺 healthCheckEndpointMode", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"b1-missing-mode","type":"api_key",
			"credentials":{"api_key":"sk-b1-secret-1234567890","base_url":"https://api.openai.com/v1"},
			"supportedModels":["gpt-4o-mini"],"status":"active",
			"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],
			"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckModel":"gpt-4o-mini"}`,
			"创建载荷缺少 healthCheckEndpointMode"},
		{"缺 healthCheckModel", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"b1-missing-model","type":"api_key",
			"credentials":{"api_key":"sk-b1-secret-1234567890","base_url":"https://api.openai.com/v1"},
			"supportedModels":["gpt-4o-mini"],"status":"active",
			"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],
			"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json"}`,
			"创建载荷缺少 healthCheckModel"},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", testCase.body)
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), testCase.message) {
			t.Fatalf("%s：%d %v（want 400 %s）", testCase.name, code, payload, testCase.message)
		}
	}
}

// TestB1CreateRejectsInvalidConnections：连接行违规各 400。
func TestB1CreateRejectsInvalidConnections(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	base := `"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"NAME","type":"api_key",
		"credentials":{"api_key":"sk-b1-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active",
		"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"`
	cases := []struct {
		name        string
		connections string
		message     string
	}{
		{"registry 组合外 preset", `"connections":[{"connectionPresetCode":"anthropic_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json"],"enabled":true}]`,
			"connection_capability_unsupported"},
		{"同 preset 重复行", `"connections":[
			{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json"],"enabled":true},
			{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v2","endpointModes":["chat_sse"],"enabled":true}]`,
			"connection_capability_unsupported"},
		{"mode 越域", `"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["messages_json"],"enabled":true}]`,
			"connection_capability_unsupported"},
		{"mode 重复", `"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_json"],"enabled":true}]`,
			"connection_capability_unsupported"},
		{"operation 路径 base URL", `"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1/chat/completions","endpointModes":["chat_json"],"enabled":true}]`,
			"invalid_connection_base_url"},
		{"surface 回显不一致", `"connections":[{"connectionPresetCode":"gpt_openai_v1","protocolSurfaceCode":"anthropic_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json"],"enabled":true}]`,
			"connection_capability_unsupported"},
	}
	for _, testCase := range cases {
		body := "{" + strings.Replace(base, "NAME", testCase.name, 1) + "," + testCase.connections + "}"
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", body)
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), testCase.message) {
			t.Fatalf("%s：%d %v（want 400 %s）", testCase.name, code, payload, testCase.message)
		}
	}

	// 健康检查 preset 必须解析到最终连接集合（§8）：指向未提交的 xai preset
	// → health_check_connection_conflict（写链层断言；HTTP 面纯 ASCII 错误码
	// 会被 kernel 本地化为 400 默认文案，故此处直连 Store.Create）。
	preset := "xai_openai_v1"
	healthModel := "gpt-4o-mini"
	healthMode := "chat_json"
	_, err := env.store.Create(context.Background(), CreateInput{
		ProviderCode: "gpt", ProviderProtocolProfileID: "prof-gpt", Name: "b1-health-preset-out",
		AccountType:     "api_key",
		Credentials:     Credentials{"api_key": "sk-b1-secret-1234567890", "base_url": "https://api.openai.com/v1"},
		SupportedModels: []string{"gpt-4o-mini"},
		Status:          CreationStatus{Status: "active", SkipInitialHealthCheck: true, Schedulable: true},
		Connections: []ConnectionInput{{
			ConnectionPresetCode: "gpt_openai_v1",
			BaseURL:              "https://api.openai.com/v1",
			EndpointModes:        []string{"chat_json", "chat_sse"},
			Enabled:              true,
		}},
		HealthCheckConnectionPresetCode: &preset,
		HealthCheckEndpointMode:         &healthMode,
		HealthCheckModel:                &healthModel,
	}, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err == nil || !strings.Contains(err.Error(), "health_check_connection_conflict") {
		t.Fatalf("健康检查 preset 不在连接集合应拒绝：%v", err)
	}
}

// TestB1ValidateConnectionBaseURLTable 是 §2.1 base_url validator 的表驱动
// 用例（合法 http/https + suffix、非法 scheme/userinfo/query/fragment/dot
// segment/坏 percent/编码斜杠/operation 前缀）。
func TestB1ValidateConnectionBaseURLTable(t *testing.T) {
	// gpt_openai_v1：suffix ["", "/v1"]，openai operation template 全集。
	suffixes, templates, ok := connectionURLContract("gpt", "api_key", "", "gpt_openai_v1", "chat")
	if !ok {
		t.Fatal("registry URL 契约缺失")
	}
	valid := []string{
		"https://api.openai.com",
		"https://api.openai.com/",
		"https://api.openai.com/v1",
		"https://api.openai.com/v1/",
		"http://api.openai.com/v1",
		"https://proxy.example.com:8443/custom-prefix/v1",
	}
	for _, raw := range valid {
		if err := validateConnectionBaseURL(suffixes, templates, raw); err != nil {
			t.Fatalf("合法 base URL 被拒绝 %q：%v", raw, err)
		}
	}
	invalid := []string{
		"",                        // 空
		"ftp://api.openai.com/v1", // 非 http/https
		"https://user:pass@api.openai.com/v1", // userinfo
		"https://api.openai.com/v1?x=1",       // query
		"https://api.openai.com/v1#frag",      // fragment
		"https://api.openai.com/../v1",        // dot segment
		"https://api.openai.com/%zz/v1",       // 坏 percent escape
		"https://api.openai.com/%2F/v1",       // percent-decoded slash
		"https://api.openai.com/%5C/v1",       // percent-decoded backslash
		"https://api.openai.com/v1/chat/completions", // operation 完整路径
		"https://api.openai.com/v1/chat",             // operation 中间前缀
		"https://api.openai.com/v1/chat/comp",        // 字面段非空前缀
		"https://api.openai.com/audio",               // 非法 suffix
		"https://api.openai.com/v1/chat/completions/extra", // suffix 未命中 + operation 前缀
	}
	for _, raw := range invalid {
		if err := validateConnectionBaseURL(suffixes, templates, raw); err == nil {
			t.Fatalf("非法 base URL 被放行 %q", raw)
		} else if !strings.Contains(err.Error(), "invalid_connection_base_url") {
			t.Fatalf("错误码不符合 §8 %q：%v", raw, err)
		}
	}
	// gemini native 模板的嵌入式占位符（{model}:generateContent）与文档示例
	// /v1beta/models/foo。
	geminiSuffixes, geminiTemplates, ok := connectionURLContract("gemini", "api_key", "", "gemini_native_v1beta", "chat")
	if !ok {
		t.Fatal("gemini registry URL 契约缺失")
	}
	if err := validateConnectionBaseURL(geminiSuffixes, geminiTemplates, "https://generativelanguage.googleapis.com/v1beta/models/foo"); err == nil {
		t.Fatal("嵌入式占位符中间前缀 /v1beta/models/foo 应拒绝")
	}
	if err := validateConnectionBaseURL(geminiSuffixes, geminiTemplates, "https://generativelanguage.googleapis.com/v1beta"); err != nil {
		t.Fatalf("gemini suffix /v1beta 应放行：%v", err)
	}
}

// TestB1CloneCopiesConnectionRows：克隆复制源账户连接行（preset/base_url/
// modes/enabled 保留、id 重新生成），health 三字段指向新账户自己的连接行。
func TestB1CloneCopiesConnectionRows(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", b1CreatePayload("b1-clone-source"))
	if code != http.StatusCreated {
		t.Fatalf("source create: %d %v", code, payload)
	}
	sourceID := dataMap(t, payload)["id"].(string)
	sourceConnectionID := env.queryCell(t, `SELECT id FROM account_connections WHERE account_id = ?`, sourceID)

	cloneBody := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"b1-clone-copy","type":"api_key",
		"capabilityDomain":"chat","credentials":{"api_key":"sk-b1-clone-secret-123456","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","cloneSourceId":"` + sourceID + `"}`
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", cloneBody)
	if code != http.StatusCreated {
		t.Fatalf("clone create: %d %v", code, payload)
	}
	cloneID := dataMap(t, payload)["id"].(string)

	// 连接行按 preset 原样重建，行 ID 重新生成且与源不同。
	cloneConnectionID := env.queryCell(t, `SELECT id FROM account_connections WHERE account_id = ?`, cloneID)
	if cloneConnectionID == "" || cloneConnectionID == sourceConnectionID {
		t.Fatalf("克隆连接行 ID 未重新生成：%q vs %q", cloneConnectionID, sourceConnectionID)
	}
	if got := env.queryCell(t, `SELECT connection_preset_code || '|' || base_url || '|' || enabled FROM account_connections WHERE id = ?`, cloneConnectionID); got != "gpt_openai_v1|https://api.openai.com/v1|1" {
		t.Fatalf("克隆连接行事实不一致：%q", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_endpoint_modes WHERE connection_id = ?`, cloneConnectionID); got != 2 {
		t.Fatalf("克隆 endpoint mode 行数：%d", got)
	}

	// health 三字段指向克隆体自己的连接行。
	var healthConnection, healthMode, healthModel string
	if err := env.db.QueryRow(`SELECT health_check_connection_id, health_check_endpoint_mode, health_check_model
		FROM accounts WHERE id = $1`, cloneID).Scan(&healthConnection, &healthMode, &healthModel); err != nil {
		t.Fatal(err)
	}
	if healthConnection != cloneConnectionID || healthMode != "chat_json" || healthModel != "gpt-4o-mini" {
		t.Fatalf("克隆 health 三字段未指向新连接行：%q %q %q", healthConnection, healthMode, healthModel)
	}
}

// TestB1ImportLegacyFormatFailsExplicitly：旧格式导入包（无 connections）在
// 计划层按 per-account message 显式失败（CanImport=false），不造兼容层。
func TestB1ImportLegacyFormatFailsExplicitly(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	legacy := `{"data":{"type":"juhe-ai-account-import","version":1,"proxies":[],
		"accounts":[{"ref":"legacy-1","name":"旧格式账户","providerCode":"openai",
		"providerProtocolProfileId":"` + openAICompatibleProfileID + `","type":"api_key","status":"active",
		"groupName":"旧格式分组","supportedModels":["gpt-4o-mini"],
		"credentials":{"api_key":"sk-legacy-1234567890","base_url":"https://api.openai.com/v1"}}]}}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", legacy)
	if code != http.StatusOK {
		t.Fatalf("preview: %d %v", code, payload)
	}
	plan := dataMap(t, payload)
	if plan["canImport"] != false {
		t.Fatalf("旧格式导入应不可导入：%v", plan["summary"])
	}
	item := plan["accounts"].([]any)[0].(map[string]any)
	if item["action"] != "failed" {
		t.Fatalf("旧格式条目应 failed：%v", item)
	}
	messages := ""
	for _, message := range item["messages"].([]any) {
		messages += message.(string) + "|"
	}
	if !strings.Contains(messages, "connections 不能为空") || !strings.Contains(messages, "healthCheckConnectionPresetCode 不能为空") {
		t.Fatalf("旧格式报错清单不完整：%s", messages)
	}
	// 显式失败：不落任何账户行。
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = '旧格式账户'`); got != 0 {
		t.Fatalf("旧格式导入不应落库：%d", got)
	}
}
