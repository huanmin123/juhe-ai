// 内置测试 token（builtInTestSourceID/exttok_builtin_test）的 mock 家族补测：
// 16 条路由在 IsTestToken 会话下全部走 mock 载荷，不触碰资源表。断言锁定
// Node external-public-*.mock.ts 的默认值回退（用户名/分组名/供应商等）、
// 入参投影与 mock 信封（source=mock + generatedAt）。
package aipublic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newWCMockEnv 建一个带内置测试 token 会话的隔离环境。
func newWCMockEnv(t *testing.T) *aipublicEnv {
	t.Helper()
	env := newAIPublicEnv(t)
	// 内置测试来源：sourceID 命中 builtInTestSourceID 即 IsTestToken。
	env.seedSource(builtInTestSourceID, builtInTestTokenID, "juis_token_wcwcwcwcwcwcwcwc",
		"active", "active", allWCMockScopes(), "[]", "", "")
	return env
}

func allWCMockScopes() []string {
	return []string{
		scopeGroupListRead, scopeStrategyListRead, scopeApiKeyListRead, scopeAccountListRead,
		scopeGroupAddWrite, scopeGroupUpdateWrite, scopeGroupDeleteWrite,
		scopeStrategyAddWrite, scopeStrategyUpdateWrite, scopeStrategyDeleteWrite,
		scopeApiKeyAddWrite, scopeApiKeyUpdateWrite, scopeApiKeyDeleteWrite,
		scopeAccountAddWrite, scopeAccountUpdateWrite, scopeAccountDeleteWrite,
	}
}

func mustMockEnvelope(t *testing.T, status int, payload map[string]any, wantStatus int) map[string]any {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status: %d %v", status, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("缺 data 信封: %v", payload)
	}
	if data["source"] != "mock" {
		t.Fatalf("mock 信封 source 必须为 mock: %v", data)
	}
	if data["generatedAt"] == "" {
		t.Fatalf("mock 信封必须带 generatedAt: %v", data)
	}
	return data
}

// TestWCMockGroupFamily：group list/add/update/del 的 mock 分支与默认值回退。
func TestWCMockGroupFamily(t *testing.T) {
	env := newWCMockEnv(t)
	token := "juis_token_wcwcwcwcwcwcwcwc"

	// list：默认 keyword/providerCode 回退 + 显式入参投影 + 分页。
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=huanmin&keyword=Key&providerCode=gpt&page=2&pageSize=5", "", token)
	data := mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["page"] != float64(2) || data["pageSize"] != float64(5) || data["pageUpperBound"] != float64(1) || data["hasMore"] != false {
		t.Fatalf("mock 分页投影: %v", data)
	}
	items := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("mock 列表固定一条: %v", items)
	}
	item := items[0].(map[string]any)
	if item["name"] != "Key" || item["providerCode"] != "gpt" || item["id"] != "mock_group_public" || item["groupType"] != "personal" || item["enabled"] != true {
		t.Fatalf("mock 列表项: %v", item)
	}
	target := data["target"].(map[string]any)
	if target["username"] != "huanmin" || target["systemAccountId"] != mockSystemAccountID {
		t.Fatalf("mock target: %v", target)
	}

	// list：默认值回退（keyword=公开接口分组、providerCode=mock_provider）。
	// 注：schema 要求 targetUsername 最少 2 字符，空值在解析层即被拒。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=u1", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	item = data["items"].([]any)[0].(map[string]any)
	if item["name"] != "公开接口分组" || item["providerCode"] != "mock_provider" {
		t.Fatalf("mock 默认值回退: %v", item)
	}
	if data["target"].(map[string]any)["username"] != "u1" {
		t.Fatalf("mock 用户名投影: %v", data["target"])
	}

	// add：缺 providerCode 的回退分支在 schema 之后，HTTP 层到不了
	//（providerCode 为必填），直接调用 mock 处理器锁定该分支。
	recorder := httptest.NewRecorder()
	(&Deps{}).mockGroupAdd(recorder, map[string]any{"targetUsername": "u1", "name": "n1"})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "供应商编码不能为空") {
		t.Fatalf("mock add 缺供应商: %d %s", recorder.Code, recorder.Body.String())
	}

	// add：完整字段投影（description/enabled=false/groupType）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/add",
		`{"targetUsername":"u1","name":"组名","providerCode":"gpt","description":"描述","enabled":false,"groupType":"high_concurrency"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["action"] != "mock" {
		t.Fatalf("mock add action: %v", data)
	}
	group := data["group"].(map[string]any)
	if group["name"] != "组名" || group["description"] != "描述" || group["enabled"] != false || group["groupType"] != "high_concurrency" || group["isDefault"] != false {
		t.Fatalf("mock add group: %v", group)
	}

	// update：显式 groupId 投影（schema 必填，mock 不再有缺省回退）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/update", `{"targetUsername":"u1","groupId":"grp-u","name":"新组名"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	groupUpdated := data["group"].(map[string]any)
	if groupUpdated["id"] != "grp-u" || groupUpdated["name"] != "新组名" || groupUpdated["providerCode"] != "" {
		t.Fatalf("mock update group: %v", groupUpdated)
	}

	// del：显式 groupId 投影。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/del", `{"targetUsername":"u1","groupId":"grp-9"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["group"].(map[string]any)["id"] != "grp-9" || data["action"] != "mock" {
		t.Fatalf("mock del: %v", data)
	}
}

// TestWCMockStrategyFamily：策略路由 mock 的绑定投影与默认配置。
func TestWCMockStrategyFamily(t *testing.T) {
	env := newWCMockEnv(t)
	token := "juis_token_wcwcwcwcwcwcwcwc"

	// add：绑定数组投影（groupId 缺省、priority/weight/status 覆盖）。
	body := `{"targetUsername":"u1","name":"策略名","mode":"weighted","status":"disabled",` +
		`"groupBindings":[{"groupId":"g1","priority":7,"weight":30,"status":"disabled"},{"groupId":"g2","weight":55}]}`
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add", body, token)
	data := mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["action"] != "mock" {
		t.Fatalf("mock add action: %v", data)
	}
	strategy := data["routeStrategy"].(map[string]any)
	if strategy["id"] != "mock_route_strategy_public" || strategy["mode"] != "weighted" || strategy["status"] != "disabled" {
		t.Fatalf("mock strategy summary: %v", strategy)
	}
	bindings := strategy["groupBindings"].([]any)
	if len(bindings) != 2 {
		t.Fatalf("mock bindings: %v", bindings)
	}
	first := bindings[0].(map[string]any)
	if first["groupId"] != "g1" || first["priority"] != float64(7) || first["weight"] != float64(30) || first["status"] != "disabled" || first["groupEnabled"] != true {
		t.Fatalf("mock binding 0: %v", first)
	}
	second := bindings[1].(map[string]any)
	if second["groupId"] != "g2" || second["priority"] != float64(2) || second["weight"] != float64(55) || second["status"] != "active" {
		t.Fatalf("mock binding 1 默认回退: %v", second)
	}

	// add：normal 模式缺省路由配置注入 cost_first。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/add", `{"targetUsername":"u1","name":"默认策略","groupBindings":[{"groupId":"g9"}]}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	strategy = data["routeStrategy"].(map[string]any)
	config := strategy["normalRoutingConfig"].(map[string]any)
	if config["schedulingPreference"] != "cost_first" {
		t.Fatalf("mock normal 默认配置: %v", config)
	}
	// 非 normal 模式不渲染 normalRoutingConfig。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/add", `{"targetUsername":"u1","name":"加权策略","mode":"weighted","groupBindings":[{"groupId":"g9"}]}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["routeStrategy"].(map[string]any)["normalRoutingConfig"] != nil {
		t.Fatalf("非 normal 不得带 normalRoutingConfig: %v", data["routeStrategy"])
	}

	// list：mode/status 缺省与 "all" 回退 normal/active。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/route-strategy/list?targetUsername=u1&mode=all&status=all", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	item := data["items"].([]any)[0].(map[string]any)
	if item["mode"] != "normal" || item["status"] != "active" || item["name"] != "公开接口策略路由" {
		t.Fatalf("mock strategy list 默认: %v", item)
	}

	// update：显式 routeStrategyId 投影；mock 摘要不渲染 hybridRoutingConfig
	//（mockStrategySummary 只落 normalRoutingConfig）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/update",
		`{"targetUsername":"u1","routeStrategyId":"rs-1","hybridRoutingConfig":{"unavailableThreshold":3}}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	strategy = data["routeStrategy"].(map[string]any)
	if strategy["id"] != "rs-1" || strategy["hybridRoutingConfig"] != nil {
		t.Fatalf("mock strategy update: %v", strategy)
	}

	// del：固定 disabled 状态摘要。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/del", `{"targetUsername":"u1","routeStrategyId":"rs-2"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	strategy = data["routeStrategy"].(map[string]any)
	if strategy["id"] != "rs-2" || strategy["status"] != "disabled" {
		t.Fatalf("mock strategy del: %v", strategy)
	}
}

// TestWCMockApiKeyFamily：API Key mock 的策略绑定变体与状态归一。
func TestWCMockApiKeyFamily(t *testing.T) {
	env := newWCMockEnv(t)
	token := "juis_token_wcwcwcwcwcwcwcwc"

	// add：缺省 status 归一为 active + 明文 key 固定值。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/api-key/add",
		`{"targetUsername":"u1","name":"KeyA","routeStrategyId":"rs-1","expiresAt":"2030-01-01T00:00:00Z"}`, token)
	data := mustMockEnvelope(t, status, payload, http.StatusOK)
	apiKey := data["apiKey"].(map[string]any)
	if apiKey["status"] != "active" || apiKey["key"] != "juis_mock_public_api_key" || apiKey["expiresAt"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("mock api key add: %v", apiKey)
	}

	// add：status=disabled 保持。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/add", `{"targetUsername":"u1","name":"KeyB","routeStrategyId":"rs-1","status":"disabled"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["apiKey"].(map[string]any)["status"] != "disabled" {
		t.Fatalf("mock api key add disabled: %v", data["apiKey"])
	}

	// list：无 routeStrategyId → 公开策略回退名。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/api-key/list?targetUsername=u1", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	item := data["items"].([]any)[0].(map[string]any)
	if item["routeStrategyName"] != "公开接口策略路由" || item["id"] != "mock_api_key_public" || item["key"] != nil {
		t.Fatalf("mock api key list 默认: %v", item)
	}

	// list：带 routeStrategyId → 指定策略名 + bound id。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/api-key/list?targetUsername=u1&routeStrategyId=rs-9&status=disabled&keyword=KeyK", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	item = data["items"].([]any)[0].(map[string]any)
	if item["routeStrategyName"] != "公开接口指定策略路由" || item["id"] != "mock_api_key_public_bound" || item["status"] != "disabled" || item["name"] != "KeyK" {
		t.Fatalf("mock api key list 绑定: %v", item)
	}

	// update：id 缺省回退（schema 要求至少一个可变字段，name 满足）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/update", `{"targetUsername":"u1","apiKeyId":"key-c","name":"KeyC"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["apiKey"].(map[string]any)["id"] != "key-c" || data["apiKey"].(map[string]any)["name"] != "KeyC" {
		t.Fatalf("mock api key update: %v", data["apiKey"])
	}

	// del。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/del", `{"targetUsername":"u1","apiKeyId":"key-1"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	apiKey = data["apiKey"].(map[string]any)
	if apiKey["id"] != "key-1" || apiKey["status"] != "disabled" || apiKey["routeStrategyMode"] != "normal" {
		t.Fatalf("mock api key del: %v", apiKey)
	}
}

// TestWCMockAccountFamily：账号 mock 的状态/可调度/分组回退矩阵。
func TestWCMockAccountFamily(t *testing.T) {
	env := newWCMockEnv(t)
	token := "juis_token_wcwcwcwcwcwcwcwc"

	// add：默认回退 + status=active 可调度 + supportedModels 投影。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/add",
		`{"targetUsername":"u1","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"p1","name":"站点M","baseUrl":"https://x","apiKey":"sk","supportedModels":[" m1 ","m1"]}`, token)
	data := mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["action"] != "mock" {
		t.Fatalf("mock add action: %v", data)
	}
	account := data["account"].(map[string]any)
	if account["status"] != "active" || account["schedulable"] != true || account["providerCode"] != "gpt" || account["type"] != "api_key" {
		t.Fatalf("mock account add 默认: %v", account)
	}
	if models := account["supportedModels"].([]any); len(models) != 1 || models[0] != "m1" {
		t.Fatalf("mock supportedModels 规范化: %v", account["supportedModels"])
	}
	target := data["target"].(map[string]any)
	if target["groupName"] != "福利" || target["groupId"] != "mock_group_welfare" {
		t.Fatalf("mock account target 默认: %v", target)
	}

	// add：status=disabled 不可调度 + 显式展示名。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/add",
		`{"targetUsername":"u1","targetDisplayName":"展示名","status":"disabled","targetGroupName":"G1","providerCode":"anthropic","providerProtocolProfileId":"p9","name":"站点B","baseUrl":"https://y","apiKey":"sk-b"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	account = data["account"].(map[string]any)
	if account["status"] != "disabled" || account["schedulable"] != false || account["name"] != "站点B" || account["providerCode"] != "anthropic" {
		t.Fatalf("mock account add 显式: %v", account)
	}
	if data["target"].(map[string]any)["displayName"] != "展示名" {
		t.Fatalf("mock targetDisplayName: %v", data["target"])
	}

	// list：groupId/keyword/groupName 投影 + 并发/优先级固定值。
	status, payload, _ = env.doAuth(http.MethodGet,
		Prefix+"/account/list?targetUsername=u1&groupId=g-7&keyword=Key&targetGroupName=GN&providerCode=anthropic&providerProtocolProfileId=p8", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	item := data["items"].([]any)[0].(map[string]any)
	if item["boundGroupId"] != "g-7" || item["boundGroupName"] != "GN" || item["concurrencyLimit"] != float64(20) || item["priority"] != float64(0) || item["name"] != "Key" {
		t.Fatalf("mock account list: %v", item)
	}
	if item["supportedModels"].([]any)[0] != "gpt-5.5" {
		t.Fatalf("mock account list 模型: %v", item["supportedModels"])
	}

	// update：test token 走 mockAccountPush 信封。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update", `{"targetUsername":"u1","accountId":"a-1","status":"active"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	if data["action"] != "mock" || data["account"].(map[string]any)["status"] != "active" {
		t.Fatalf("mock account update: %v", data)
	}

	// del：显式 accountId 投影 + 固定 disabled。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/del", `{"targetUsername":"u1","accountId":"a-2","targetGroupName":"GN2"}`, token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	account = data["account"].(map[string]any)
	if account["id"] != "a-2" || account["status"] != "disabled" || account["schedulable"] != false {
		t.Fatalf("mock account del: %v", account)
	}
	if data["target"].(map[string]any)["groupName"] != "GN2" {
		t.Fatalf("mock account del target: %v", data["target"])
	}
}

// TestWCMockTokenIgnoresAuthTables：内置测试 token 在来源被禁用时仍走 mock？
// 行为契约：IsTestToken 由来源/token ID 决定，但状态门在 mock 分支之前，
// 禁用来源依旧 403——这里锁定该顺序不被回归。
func TestWCMockTokenIgnoresAuthTables(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedSource(builtInTestSourceID, "exttok_wc_off", "juis_token_offoffoffoff",
		"disabled", "active", allWCMockScopes(), "[]", "", "")
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=x", "", "juis_token_offoffoffoff")
	if status != http.StatusForbidden || payload["code"] != "external_source_disabled" {
		t.Fatalf("禁用来源必须先于 mock 分支拒绝: %d %v", status, payload)
	}
	// mock 路径不写库：所有资源表保持为空。
	var rows int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("mock 路径不得写 groups: %d", rows)
	}
	if !strings.Contains(mockSystemAccountID, "huanmin") {
		t.Fatalf("mock 账号 ID 约定: %q", mockSystemAccountID)
	}
}

// TestWCMockBodyHelpers：mock 层缺省回退分支（schema 之后的 mock 兜底逻辑
// 无法经 HTTP 触达缺省值，这里直接锁定纯函数契约）。
func TestWCMockBodyHelpers(t *testing.T) {
	if got := mockUsername("  "); got != "huanmin" {
		t.Fatalf("mockUsername 空值回退: %q", got)
	}
	if got := mockUsername(" u "); got != "u" {
		t.Fatalf("mockUsername 去空白: %q", got)
	}
	if textFrom(map[string]any{"k": " v "}, "k") != "v" {
		t.Fatalf("textFrom 去空白")
	}
	if textFrom(map[string]any{}, "k") != "" || textFrom(map[string]any{"k": 1.5}, "k") != "" {
		t.Fatalf("textFrom 缺失/非字符串为空")
	}
	if optionalTextPointer(map[string]any{"d": " x "}, "d") == nil {
		t.Fatalf("optionalTextPointer 有值")
	}
	if optionalTextPointer(map[string]any{"d": "  "}, "d") != nil {
		t.Fatalf("optionalTextPointer 空白为 nil")
	}
	if optionalObject(map[string]any{"o": nil}, "o") != nil || optionalObject(map[string]any{}, "o") != nil {
		t.Fatalf("optionalObject 缺失/null 为 nil")
	}
	if optionalObject(map[string]any{"o": "x"}, "o") != nil {
		t.Fatalf("optionalObject 非对象为 nil")
	}
	if _, ok := optionalObject(map[string]any{"o": map[string]any{"a": 1}}, "o").(map[string]any); !ok {
		t.Fatalf("optionalObject 对象透传")
	}
	// 绑定缺省：groupId 缺省 mock_group_public、缺省 status/priority/weight。
	bindings := bindingsFromBody(map[string]any{"groupBindings": []any{map[string]any{}, "not-object"}})
	if len(bindings) != 1 {
		t.Fatalf("非对象项必须跳过: %v", bindings)
	}
	binding := bindings[0]
	if binding.GroupID != "mock_group_public" || binding.ID != "mock_route_strategy_group_1" || binding.Priority != 1 || binding.Weight != 100 || binding.Status != "active" || !binding.GroupEnabled {
		t.Fatalf("绑定缺省回退: %+v", binding)
	}
	if bindingsFromBody(map[string]any{}) != nil || bindingsFromBody(map[string]any{"groupBindings": nil}) != nil ||
		bindingsFromBody(map[string]any{"groupBindings": []any{}}) != nil ||
		bindingsFromBody(map[string]any{"groupBindings": "x"}) != nil {
		t.Fatalf("缺省/非法绑定必须为 nil")
	}
	if mockSupportedModels(map[string]any{"supportedModels": []any{" a ", "a", " b "}}) == nil {
		t.Fatalf("模型规范化保留去重结果")
	}
	if mockSupportedModels(map[string]any{"supportedModels": []any{"  "}}) != nil {
		t.Fatalf("全空白模型必须为 nil")
	}
	if mockSupportedModels(map[string]any{"supportedModels": "x"}) != nil ||
		mockSupportedModels(map[string]any{}) != nil {
		t.Fatalf("缺失/非法模型必须为 nil")
	}
	// 摘要缺省：无绑定时注入一条默认绑定；normal 缺省 cost_first。
	summary := mockStrategySummary("id", "n", "normal", "active", nil, nil)
	if len(summary.GroupBindings) != 1 || summary.GroupBindings[0].GroupID != "mock_group_public" {
		t.Fatalf("摘要默认绑定: %+v", summary.GroupBindings)
	}
	if summary.NormalRoutingConfig.(map[string]any)["schedulingPreference"] != "cost_first" {
		t.Fatalf("摘要默认路由配置: %v", summary.NormalRoutingConfig)
	}
	hybrid := mockStrategySummary("id", "n", "hybrid_smart", "active", nil, map[string]any{"k": 1})
	if hybrid.NormalRoutingConfig != nil || hybrid.HybridRoutingConfig != nil {
		t.Fatalf("非 normal 摘要不落任何路由配置: %+v", hybrid)
	}
}
