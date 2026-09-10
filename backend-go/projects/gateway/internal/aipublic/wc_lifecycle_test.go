// /__aipublic__ 资源族（group / route-strategy / api-key / account）的真实
// stats 路径补测：zod 校验失败（400）、归属/存在性（404）、供应商预检、
// 目标分组解析与列表过滤投影。锁定 Node external-integrations 路由层的
// 错误语义与响应包络。
package aipublic

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const wcLifeToken = "juis_token_lifelifelifelife"

// wcLifeScopes 是生命周期测试用的全量 scope。
func wcLifeScopes() []string {
	return []string{
		scopeGroupListRead, scopeStrategyListRead, scopeApiKeyListRead, scopeAccountListRead,
		scopeGroupAddWrite, scopeGroupUpdateWrite, scopeGroupDeleteWrite,
		scopeStrategyAddWrite, scopeStrategyUpdateWrite, scopeStrategyDeleteWrite,
		scopeApiKeyAddWrite, scopeApiKeyUpdateWrite, scopeApiKeyDeleteWrite,
		scopeAccountAddWrite, scopeAccountUpdateWrite, scopeAccountDeleteWrite,
	}
}

// newWCLifeEnv：真实路径环境——启用的 gpt 供应商 + 协议档案 + 活跃/停用目标用户。
func newWCLifeEnv(t *testing.T) *aipublicEnv {
	t.Helper()
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	env.seedProvider("anthropic", false)
	env.seedTargetUser("user_pusher", "pusher", "active")
	env.seedTargetUser("user_frozen", "frozen", "disabled")
	env.seedSource("extsrc_life", "exttok_life", wcLifeToken, "active", "active", wcLifeScopes(), "[]", "", "")
	wcSeedProfile(t, env, "profile_gpt_openai_v1", "gpt", true, `["api_key","oauth"]`)
	return env
}

// wcSeedProfile 插入一条 provider_protocol_profiles。
func wcSeedProfile(t *testing.T, env *aipublicEnv, id, providerCode string, enabled bool, accountTypes string) {
	t.Helper()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := env.db.Exec(`INSERT INTO provider_protocol_profiles
		(id, provider_code, name, enabled, protocol_code, protocol_version, base_url,
		 default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'openai', 'v1', 'https://example.invalid', 'm1', ?, '[]', ?, ?)`,
		id, providerCode, id, enabledInt, accountTypes, now, now); err != nil {
		t.Fatal(err)
	}
}

// wcAddGroup 快捷建组并返回组 ID（已存在时复用）。
func wcAddGroup(t *testing.T, env *aipublicEnv, username, name string) string {
	t.Helper()
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/group/add",
		`{"targetUsername":"`+username+`","name":"`+name+`","providerCode":"gpt"}`, wcLifeToken)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("建组失败: %d %v", status, payload)
	}
	return payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)
}

// wcAddStrategy 快捷建策略并返回 ID。
func wcAddStrategy(t *testing.T, env *aipublicEnv, username, name, groupID string) string {
	t.Helper()
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"`+username+`","name":"`+name+`","groupBindings":[{"groupId":"`+groupID+`"}]}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("建策略失败: %d %v", status, payload)
	}
	return payload["data"].(map[string]any)["routeStrategy"].(map[string]any)["id"].(string)
}

// wcAddAccount 快捷建账号并返回 ID（补齐支持模型）。
func wcAddAccount(t *testing.T, env *aipublicEnv, body string) string {
	t.Helper()
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/add", body, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("建账号失败: %d %v", status, payload)
	}
	return payload["data"].(map[string]any)["account"].(map[string]any)["id"].(string)
}

// wcAccountBase 是公开账号新增的最小合法载荷（%s 供名称/密钥插值）。
const wcAccountBase = `{"targetUsername":"pusher","targetGroupName":"福利","providerCode":"gpt",` +
	`"providerProtocolProfileId":"profile_gpt_openai_v1","name":"%s","type":"api_key",` +
	`"baseUrl":"https://a.example","apiKey":"sk-%s","supportedModels":["gpt-4o-mini"]}`

// wcAccountBody 在 base 上追加尾部字段（替换最后的 "]}")。
func wcAccountBody(extra string) string {
	return strings.Replace(wcAccountBase, `]}`, `],`+extra+`}`, 1)
}

// TestWCGroupValidation：group 家族的 400 校验分支（经 kernel 本地化后
// 英文 zod 消息统一呈现为 请求参数无效）。
func TestWCGroupValidation(t *testing.T) {
	env := newWCLifeEnv(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list 未知 query 键", http.MethodGet, Prefix + "/group/list?targetUsername=pusher&bogus=1", ""},
		{"list 缺 targetUsername", http.MethodGet, Prefix + "/group/list", ""},
		{"list page=0", http.MethodGet, Prefix + "/group/list?targetUsername=pusher&page=0", ""},
		{"list pageSize=101", http.MethodGet, Prefix + "/group/list?targetUsername=pusher&pageSize=101", ""},
		{"add 未知 body 键", http.MethodPost, Prefix + "/group/add", `{"targetUsername":"pusher","name":"n","providerCode":"gpt","x":1}`},
		{"add 缺 name", http.MethodPost, Prefix + "/group/add", `{"targetUsername":"pusher","providerCode":"gpt"}`},
		{"add 用户名过短", http.MethodPost, Prefix + "/group/add", `{"targetUsername":"p","name":"n","providerCode":"gpt"}`},
		{"add groupType 非法", http.MethodPost, Prefix + "/group/add", `{"targetUsername":"pusher","name":"n","providerCode":"gpt","groupType":"bogus"}`},
		{"del 未知键", http.MethodPost, Prefix + "/group/del", `{"groupId":"g","x":1}`},
		{"del 缺 groupId", http.MethodPost, Prefix + "/group/del", `{}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload, _ := env.doAuth(testCase.method, testCase.path, testCase.body, wcLifeToken)
			if status != http.StatusBadRequest {
				t.Fatalf("status: %d %v", status, payload)
			}
			if payload["message"] == nil || payload["code"] != nil {
				t.Fatalf("400 包络必须只有 message: %v", payload)
			}
		})
	}
	// update 无可变字段 → 中文文案原样保留。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/group/update", `{"targetUsername":"pusher","groupId":"g1"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "分组修改至少提供一个要修改的字段" {
		t.Fatalf("update 无可变字段: %d %v", status, payload)
	}
}

// TestWCGroupListFilters：group 列表的 keyword 前缀与分页投影。
func TestWCGroupListFilters(t *testing.T) {
	env := newWCLifeEnv(t)
	groupA := wcAddGroup(t, env, "pusher", "Alpha组")
	groupB := wcAddGroup(t, env, "pusher", "Beta站")

	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=pusher&keyword=Alpha", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("list: %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	items := data["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != groupA {
		t.Fatalf("keyword 过滤: %v", items)
	}
	if data["page"] != float64(1) || data["pageSize"] != float64(20) || data["hasMore"] != false {
		t.Fatalf("默认分页: %v", data)
	}

	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=pusher&page=1&pageSize=1", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("list 分页: %d %v", status, payload)
	}
	data = payload["data"].(map[string]any)
	if len(data["items"].([]any)) != 1 || data["pageUpperBound"] != float64(2) || data["hasMore"] != true {
		t.Fatalf("分页上界/hasMore: %v", data)
	}
	if groupB == "" {
		t.Fatalf("组 B 必须存在")
	}
}

// TestWCGroupUpdateDeleteBranches：update/del 的归属与状态分支。
func TestWCGroupUpdateDeleteBranches(t *testing.T) {
	env := newWCLifeEnv(t)
	groupID := wcAddGroup(t, env, "pusher", "原组名")

	// 未知组 → 404 分组不存在。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/group/update", `{"groupId":"grp_none","name":"x"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "分组不存在" {
		t.Fatalf("未知组 update: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/del", `{"groupId":"grp_none"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "分组不存在" {
		t.Fatalf("未知组 del: %d %v", status, payload)
	}

	// 停用属主本人 → 400（resolveOwnedTarget 按属主 ID 匹配后才检查状态）。
	if _, err := env.db.Exec(`UPDATE system_accounts SET status = 'disabled' WHERE username = 'pusher'`); err != nil {
		t.Fatal(err)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/update",
		`{"targetUsername":"pusher","groupId":"`+groupID+`","name":"x"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "目标用户已停用：pusher" {
		t.Fatalf("停用属主 update: %d %v", status, payload)
	}
	if _, err := env.db.Exec(`UPDATE system_accounts SET status = 'active' WHERE username = 'pusher'`); err != nil {
		t.Fatal(err)
	}

	// 停用的 providerCode → 400 供应商已停用。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/update",
		`{"groupId":"`+groupID+`","providerCode":"anthropic"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "供应商已停用：anthropic" {
		t.Fatalf("停用供应商 update: %d %v", status, payload)
	}

	// description 显式 null + enabled=false + groupType 更新。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/update",
		`{"groupId":"`+groupID+`","description":null,"enabled":false,"groupType":"high_concurrency"}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("update: %d %v", status, payload)
	}
	group := payload["data"].(map[string]any)["group"].(map[string]any)
	if group["description"] != nil || group["enabled"] != false || group["groupType"] != "high_concurrency" {
		t.Fatalf("update 投影: %v", group)
	}
	// DB 侧确认 description 为 NULL。
	var description any
	if err := env.db.QueryRow(`SELECT description FROM groups WHERE id = ?`, groupID).Scan(&description); err != nil {
		t.Fatal(err)
	}
	if description != nil {
		t.Fatalf("description 必须写 NULL: %v", description)
	}

	// del：显式 targetUsername 匹配属主后删除。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/del",
		`{"targetUsername":"pusher","groupId":"`+groupID+`"}`, wcLifeToken)
	if status != http.StatusOK || payload["data"].(map[string]any)["action"] != "deleted" {
		t.Fatalf("del: %d %v", status, payload)
	}
}

// TestWCStrategyValidation：route-strategy 家族的校验分支。
func TestWCStrategyValidation(t *testing.T) {
	env := newWCLifeEnv(t)
	groupID := wcAddGroup(t, env, "pusher", "策略组")
	tooMany := make([]string, 21)
	for index := range tooMany {
		tooMany[index] = `{"groupId":"g"}`
	}
	cases := []struct {
		name string
		body string
	}{
		{"add 绑定空数组", `{"targetUsername":"pusher","name":"s","groupBindings":[]}`},
		{"add 绑定超 20", `{"targetUsername":"pusher","name":"s","groupBindings":[` + strings.Join(tooMany, ",") + `]}`},
		{"add 绑定缺 groupId", `{"targetUsername":"pusher","name":"s","groupBindings":[{}]}`},
		{"add 绑定 priority 0", `{"targetUsername":"pusher","name":"s","groupBindings":[{"groupId":"g","priority":0}]}`},
		{"add 绑定 weight 101", `{"targetUsername":"pusher","name":"s","groupBindings":[{"groupId":"g","weight":101}]}`},
		{"add 绑定 weight 0", `{"targetUsername":"pusher","name":"s","groupBindings":[{"groupId":"g","weight":0}]}`},
		{"add 绑定 status 非法", `{"targetUsername":"pusher","name":"s","groupBindings":[{"groupId":"g","status":"bogus"}]}`},
		{"add 绑定未知键", `{"targetUsername":"pusher","name":"s","groupBindings":[{"groupId":"g","x":1}]}`},
		{"add 绑定非对象", `{"targetUsername":"pusher","name":"s","groupBindings":["x"]}`},
		{"add 绑定非数组", `{"targetUsername":"pusher","name":"s","groupBindings":"x"}`},
		{"add mode 非法", `{"targetUsername":"pusher","name":"s","mode":"bogus","groupBindings":[{"groupId":"g"}]}`},
		{"add normalRoutingConfig 非对象", `{"targetUsername":"pusher","name":"s","normalRoutingConfig":1,"groupBindings":[{"groupId":"g"}]}`},
		{"add hybridRoutingConfig 非对象", `{"targetUsername":"pusher","name":"s","hybridRoutingConfig":1,"groupBindings":[{"groupId":"g"}]}`},
		{"update 无可变字段", `{"targetUsername":"pusher","routeStrategyId":"rs_x"}`},
		{"list mode 非法", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			method, path, body := http.MethodPost, Prefix+"/route-strategy/add", testCase.body
			if testCase.body == "" {
				method, path = http.MethodGet, Prefix+"/route-strategy/list?targetUsername=pusher&mode=bogus"
			}
			status, payload, _ := env.doAuth(method, path, body, wcLifeToken)
			if status != http.StatusBadRequest {
				t.Fatalf("status: %d %v", status, payload)
			}
		})
	}

	// add：description null + status disabled + weighted 模式（hybrid 配置需
	// scoringModel，是 M06 store 的独立契约，不在此展开）。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"pusher","name":"加权策略","description":null,"mode":"weighted","status":"disabled",`+
			`"groupBindings":[{"groupId":"`+groupID+`","priority":2,"weight":60}]}`,
		wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("加权策略 add: %d %v", status, payload)
	}
	strategy := payload["data"].(map[string]any)["routeStrategy"].(map[string]any)
	if strategy["mode"] != "weighted" || strategy["status"] != "disabled" || strategy["description"] != nil {
		t.Fatalf("加权策略投影: %v", strategy)
	}
	if strategy["normalRoutingConfig"] != nil {
		t.Fatalf("非 normal 模式不得渲染 normal 配置: %v", strategy["normalRoutingConfig"])
	}
	strategyID := strategy["id"].(string)

	// update：绑定整体替换 + 名称。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/update",
		`{"routeStrategyId":"`+strategyID+`","name":"改名","groupBindings":[{"groupId":"`+groupID+`","weight":80}]}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("策略 update: %d %v", status, payload)
	}
	updated := payload["data"].(map[string]any)["routeStrategy"].(map[string]any)
	if updated["name"] != "改名" {
		t.Fatalf("update 名称: %v", updated)
	}
	binding := updated["groupBindings"].([]any)[0].(map[string]any)
	if binding["weight"] != float64(80) || binding["status"] != "active" {
		t.Fatalf("update 绑定: %v", binding)
	}

	// update/del 未知策略 → 404。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/update", `{"routeStrategyId":"rs_none","name":"x"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "路由策略不存在" {
		t.Fatalf("未知策略 update: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/del", `{"routeStrategyId":"rs_none"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "路由策略不存在" {
		t.Fatalf("未知策略 del: %d %v", status, payload)
	}

	// list：status 过滤生效（加权策略是 disabled，过滤 active 后为空）。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/route-strategy/list?targetUsername=pusher&status=active", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("list: %d %v", status, payload)
	}
	if items := payload["data"].(map[string]any)["items"].([]any); len(items) != 0 {
		t.Fatalf("status 过滤: %v", items)
	}
}

// TestWCApiKeyFamilyBranches：api-key 家族的过滤、更新与 404 分支。
func TestWCApiKeyFamilyBranches(t *testing.T) {
	env := newWCLifeEnv(t)
	groupID := wcAddGroup(t, env, "pusher", "Key组")
	strategyID := wcAddStrategy(t, env, "pusher", "Key策略", groupID)

	// add：status + expiresAt + description。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/api-key/add",
		`{"targetUsername":"pusher","name":"KeyA","routeStrategyId":"`+strategyID+`","status":"active",`+
			`"expiresAt":"2030-01-01T00:00:00Z","description":"备注"}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("api-key add: %d %v", status, payload)
	}
	created := payload["data"].(map[string]any)["apiKey"].(map[string]any)
	if created["expiresAt"] != "2030-01-01T00:00:00.000Z" || created["status"] != "active" {
		t.Fatalf("api-key add 投影: %v", created)
	}
	apiKeyID := created["id"].(string)

	// add 校验：未知键 / 缺 routeStrategyId / 非法 status。
	status, _, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/add", `{"targetUsername":"pusher","name":"x","routeStrategyId":"s","bogus":1}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("未知键: %d", status)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/add", `{"targetUsername":"pusher","name":"x"}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("缺 routeStrategyId: %d %v", status, payload)
	}
	status, _, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/add", `{"targetUsername":"pusher","name":"x","routeStrategyId":"s","status":"bogus"}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("非法 status: %d", status)
	}

	// quotaLimits 不支持的字段 → store 层 400（业务契约文案）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/add",
		`{"targetUsername":"pusher","name":"KeyQ","routeStrategyId":"`+strategyID+`","quotaLimits":{"dailyQuota":1}}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "请求额度限制包含不支持字段：dailyQuota" {
		t.Fatalf("quotaLimits 校验: %d %v", status, payload)
	}

	// 第二把 disabled key：驱动 status 过滤。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/add",
		`{"targetUsername":"pusher","name":"KeyB","routeStrategyId":"`+strategyID+`","status":"disabled"}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("KeyB add: %d %v", status, payload)
	}
	disabledID := payload["data"].(map[string]any)["apiKey"].(map[string]any)["id"].(string)

	// list：status=disabled 过滤。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/api-key/list?targetUsername=pusher&status=disabled", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("list: %d %v", status, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != disabledID {
		t.Fatalf("status 过滤: %v", items)
	}

	// list 校验：非法 status 枚举。
	status, _, _ = env.doAuth(http.MethodGet, Prefix+"/api-key/list?targetUsername=pusher&status=bogus", "", wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("list 非法 status: %d", status)
	}

	// update：name + description null + expiresAt null + 状态回切。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/update",
		`{"apiKeyId":"`+disabledID+`","name":"KeyB2","description":null,"expiresAt":null,"status":"active"}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("update: %d %v", status, payload)
	}
	updated := payload["data"].(map[string]any)["apiKey"].(map[string]any)
	if updated["name"] != "KeyB2" || updated["status"] != "active" || updated["description"] != nil || updated["expiresAt"] != nil {
		t.Fatalf("update 投影: %v", updated)
	}

	// update 校验：无可变字段 → 中文文案。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/update", `{"apiKeyId":"`+apiKeyID+`"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "API Key 修改至少提供一个要修改的字段" {
		t.Fatalf("update 无可变字段: %d %v", status, payload)
	}

	// update/del 未知 key：del 保持 200 not_found（Node 对齐），update 404。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/update", `{"apiKeyId":"key_none","name":"x"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "API Key 不存在" {
		t.Fatalf("未知 key update: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/del", `{"apiKeyId":"key_none"}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("未知 key del: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "not_found" {
		t.Fatalf("del not_found: %v", payload["data"])
	}

	// del：显式 targetUsername 匹配属主后删除。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/del",
		`{"targetUsername":"pusher","apiKeyId":"`+disabledID+`"}`, wcLifeToken)
	if status != http.StatusOK || payload["data"].(map[string]any)["action"] != "deleted" {
		t.Fatalf("del: %d %v", status, payload)
	}
}

// TestWCAccountValidation：account 家族的校验与供应商预检分支。
func TestWCAccountValidation(t *testing.T) {
	env := newWCLifeEnv(t)
	cases := []struct {
		name      string
		body      string
		message   string
		localized bool // 英文 zod 消息经 kernel 本地化为 请求参数无效
	}{
		{"缺 providerCode", strings.Replace(wcAccountBase, `"providerCode":"gpt",`, ``, 1), "", true},
		{"未知供应商", strings.Replace(wcAccountBase, `"providerCode":"gpt"`, `"providerCode":"claude"`, 1), "不支持的供应商：claude", false},
		{"停用供应商", strings.Replace(wcAccountBase, `"providerCode":"gpt"`, `"providerCode":"anthropic"`, 1), "供应商已停用：anthropic", false},
		{"缺 profile", strings.Replace(wcAccountBase, `"providerProtocolProfileId":"profile_gpt_openai_v1",`, ``, 1), "", true},
		{"profile 不存在", strings.Replace(wcAccountBase, `"providerProtocolProfileId":"profile_gpt_openai_v1"`, `"providerProtocolProfileId":"profile_missing"`, 1), "供应商未配置协议档案：gpt", false},
		{"缺 type", strings.Replace(wcAccountBase, `"type":"api_key",`, ``, 1), "账号类型不能为空", false},
		{"type 非 api_key", strings.Replace(wcAccountBase, `"type":"api_key"`, `"type":"oauth"`, 1), "公开账号接口仅支持 API Key 账户", false},
		{"模型为空", strings.Replace(wcAccountBase, `"supportedModels":["gpt-4o-mini"]`, `"supportedModels":[]`, 1), "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型", false},
		{"模型非数组", strings.Replace(wcAccountBase, `"supportedModels":["gpt-4o-mini"]`, `"supportedModels":"m"`, 1), "", true},
		{"并发下限", wcAccountBody(`"concurrencyLimit":0`), "", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/add", testCase.body, wcLifeToken)
			if status != http.StatusBadRequest {
				t.Fatalf("status: %d %v", status, payload)
			}
			expected := testCase.message
			if testCase.localized {
				expected = "请求参数无效"
			}
			if payload["message"] != expected {
				t.Fatalf("message: %v，期望 %v", payload["message"], expected)
			}
		})
	}

	// 停用目标用户在供应商预检之后。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/add",
		strings.Replace(wcAccountBase, `"targetUsername":"pusher"`, `"targetUsername":"frozen"`, 1), wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "目标用户已停用：frozen" {
		t.Fatalf("停用用户: %d %v", status, payload)
	}
}

// TestWCAccountListFilters：账号列表的 profile/group 解析与过滤分支。
func TestWCAccountListFilters(t *testing.T) {
	env := newWCLifeEnv(t)
	accountID := wcAddAccount(t, env, wcAccountBody(`"concurrencyLimit":9,"priority":2,"notes":"备注","status":"active"`))

	// profileId 过滤缺 providerCode → 400。
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&providerProtocolProfileId=profile_gpt_openai_v1", "", wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "按协议档案查询时必须提供 providerCode" {
		t.Fatalf("profile 过滤缺供应商: %d %v", status, payload)
	}

	// groupName 过滤缺 providerCode → 400。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&targetGroupName=福利", "", wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "按目标分组名称查询账号时必须提供 providerCode" {
		t.Fatalf("分组过滤缺供应商: %d %v", status, payload)
	}

	// groupName+providerCode 未命中分组 → 空列表（sentinel 过滤）。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&targetGroupName=不存在&providerCode=gpt", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("未知分组过滤: %d %v", status, payload)
	}
	if items := payload["data"].(map[string]any)["items"].([]any); len(items) != 0 {
		t.Fatalf("未知分组必须空列表: %v", items)
	}

	// groupName+providerCode 命中 → 返回账号。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&targetGroupName=福利&providerCode=gpt", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("分组过滤: %d %v", status, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != accountID {
		t.Fatalf("分组过滤结果: %v", items)
	}

	// type/status/schedulable 过滤：active 账号在 pending_test 之下不被
	// status=active 命中？行为存疑见报告；这里只锁定 schedulable=disabled 过滤。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&type=api_key&status=active&schedulable=disabled", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("过滤: %d %v", status, payload)
	}
	if items := payload["data"].(map[string]any)["items"].([]any); len(items) != 0 {
		t.Fatalf("schedulable=disabled 必须过滤掉活跃账号: %v", items)
	}

	// 非法 schedulable 枚举。
	status, _, _ = env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&schedulable=bogus", "", wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("非法 schedulable: %d", status)
	}
}

// TestWCAccountUpdateDeleteBranches：账号 update/del 的归属、字段与 404 分支。
func TestWCAccountUpdateDeleteBranches(t *testing.T) {
	env := newWCLifeEnv(t)
	accountID := wcAddAccount(t, env, wcAccountBase)

	// 未知账号：update 404 / del 200 not_found。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/update", `{"accountId":"acc_none","name":"x"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "账号不存在" {
		t.Fatalf("未知账号 update: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/del", `{"accountId":"acc_none"}`, wcLifeToken)
	if status != http.StatusOK || payload["data"].(map[string]any)["action"] != "not_found" {
		t.Fatalf("未知账号 del: %d %v", status, payload)
	}

	// update：providerCode 与账号不符 → 400 账号不存在（需带可变字段过
	// hasAnyField 门）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","providerCode":"claude","name":"改名"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "账号不存在" {
		t.Fatalf("供应商不符 update: %d %v", status, payload)
	}

	// update：未知档案 ID → 400 供应商未配置协议档案（档案解析先于归属
	// 比对，行为存疑点见报告）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","providerProtocolProfileId":"profile_missing","name":"改名"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "供应商未配置协议档案：gpt" {
		t.Fatalf("未知档案 update: %d %v", status, payload)
	}

	// update：targetGroupName 与实际绑定不符 → 400 账号不存在。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","targetGroupName":"别的组","name":"改名"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "账号不存在" {
		t.Fatalf("分组不符 update: %d %v", status, payload)
	}

	// update：type 显式给出且非 api_key。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","type":"oauth"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "公开账号接口仅支持 API Key 账户" {
		t.Fatalf("type 校验: %d %v", status, payload)
	}

	// update：并发下限。
	status, _, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","concurrencyLimit":0}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("并发下限: %d", status)
	}

	// update：改密钥 + BaseURL + 名称 + 支持模型 + 状态停用。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","name":"站点A2","baseUrl":"https://b.example","apiKey":"sk-new",`+
			`"supportedModels":["gpt-4o"],"status":"disabled"}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("update: %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	if data["action"] != "updated" {
		t.Fatalf("update 包络: %v", data)
	}
	account := data["account"].(map[string]any)
	if account["name"] != "站点A2" || account["status"] != "disabled" || account["schedulable"] != false {
		t.Fatalf("update 投影: %v", account)
	}
	if models := account["supportedModels"].([]any); len(models) != 1 || models[0] != "gpt-4o" {
		t.Fatalf("update 模型: %v", models)
	}
	if account["boundGroupName"] != "福利" {
		t.Fatalf("update 分组投影: %v", account)
	}

	// del：携带匹配的供应商/档案/分组字段成功删除。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/del",
		`{"accountId":"`+accountID+`","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","targetGroupName":"福利"}`, wcLifeToken)
	if status != http.StatusOK || payload["data"].(map[string]any)["action"] != "deleted" {
		t.Fatalf("del: %d %v", status, payload)
	}
	if group := payload["data"].(map[string]any)["target"].(map[string]any); group["groupName"] != "福利" {
		t.Fatalf("del 分组投影: %v", group)
	}
}

// TestWCAccountRejectsNonAPIKeyRow：oauth 类型存量账号在公开修改/删除面
// 被拒（公开接口仅支持 API Key 账户维护）。
func TestWCAccountRejectsNonAPIKeyRow(t *testing.T) {
	env := newWCLifeEnv(t)
	accountID := wcAddAccount(t, env, wcAccountBase)
	// 直接改行类型模拟存量 oauth 账号。
	if _, err := env.db.Exec(`UPDATE accounts SET type = 'oauth' WHERE id = ?`, accountID); err != nil {
		t.Fatal(err)
	}
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/update", `{"accountId":"`+accountID+`","name":"x"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "公开账号修改仅支持 API Key 账户" {
		t.Fatalf("update oauth 行: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/del", `{"accountId":"`+accountID+`"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "公开账号删除仅支持 API Key 账户" {
		t.Fatalf("del oauth 行: %d %v", status, payload)
	}
}

// TestWCGroupAddDuplicateConflict：同名同供应商重复推送走 existing 复用分支
// （group 语义是复用而非 409）。
func TestWCGroupAddDuplicateConflict(t *testing.T) {
	env := newWCLifeEnv(t)
	body := `{"targetUsername":"pusher","name":"冲突组","providerCode":"gpt"}`
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/group/add", body, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("首次 add: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/add", body, wcLifeToken)
	if status != http.StatusOK || payload["data"].(map[string]any)["action"] != "existing" {
		t.Fatalf("重复 add: %d %v", status, payload)
	}
	if target := payload["data"].(map[string]any)["target"].(map[string]any); target["created"] != false {
		t.Fatalf("existing target: %v", target)
	}
}

// TestWCAccountAddDisabledStatus：status=disabled 新增保持停用且不可调度；
// 公开投影不携带 notes。
func TestWCAccountAddDisabledStatus(t *testing.T) {
	env := newWCLifeEnv(t)
	body := strings.Replace(wcAccountBase, `"supportedModels":["gpt-4o-mini"]`,
		`"supportedModels":["gpt-4o-mini"],"status":"disabled","notes":"站点备注"`, 1)
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/add", body, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("add: %d %v", status, payload)
	}
	account := payload["data"].(map[string]any)["account"].(map[string]any)
	if account["status"] != "disabled" || account["schedulable"] != false {
		t.Fatalf("disabled 投影: %v", account)
	}
	if _, hasNotes := account["notes"]; hasNotes {
		t.Fatalf("公开投影不得携带 notes: %v", account)
	}
}
