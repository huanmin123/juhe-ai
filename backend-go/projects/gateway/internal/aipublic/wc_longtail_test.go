// 覆盖率长尾补测：各解析器的单行错误分支、停用目标用户的列表拒绝、
// api-key 严格键校验、DTO 计划投影与 capture body 的二次解码容错。
package aipublic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// TestWCFrozenTargetListRejected：停用目标用户在 list 家族同样拒绝。
func TestWCFrozenTargetListRejected(t *testing.T) {
	env := newWCLifeEnv(t)
	cases := []struct {
		name string
		path string
	}{
		{"group", Prefix + "/group/list?targetUsername=frozen"},
		{"strategy", Prefix + "/route-strategy/list?targetUsername=frozen"},
		{"api-key", Prefix + "/api-key/list?targetUsername=frozen"},
		{"account", Prefix + "/account/list?targetUsername=frozen"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload, _ := env.doAuth(http.MethodGet, testCase.path, "", wcLifeToken)
			if status != http.StatusBadRequest || payload["message"] != "目标用户已停用：frozen" {
				t.Fatalf("%s: %d %v", testCase.name, status, payload)
			}
		})
	}
}

// TestWCApiKeyStrictKeys：api-key update/del 的严格键与必填校验。
func TestWCApiKeyStrictKeys(t *testing.T) {
	env := newWCLifeEnv(t)
	status, _, _ := env.doAuth(http.MethodPost, Prefix+"/api-key/update", `{"apiKeyId":"k","bogus":1}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("update 未知键: %d", status)
	}
	status, _, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/del", `{"bogus":1}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("del 未知键: %d", status)
	}
	status, _, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/del", `{}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("del 缺 apiKeyId: %d", status)
	}
	status, _, _ = env.doAuth(http.MethodGet, Prefix+"/api-key/list?targetUsername=pusher&page=abc", "", wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("list page 非数: %d", status)
	}
}

// TestWCAccountUpdateNotesBranch：notes 合法值落库走 HasNotes 分支。
func TestWCAccountUpdateNotesBranch(t *testing.T) {
	env := newWCLifeEnv(t)
	accountID := wcAddAccount(t, env, wcAccountBase)
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","notes":"新备注"}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("notes update: %d %v", status, payload)
	}
}

// TestWCParseAccountUpdateBodyErrors：account update 解析器的单行错误分支。
func TestWCParseAccountUpdateBodyErrors(t *testing.T) {
	base := map[string]any{"accountId": "a"}
	cases := []struct {
		name  string
		field string
		value any
	}{
		{"targetUsername 过短", "targetUsername", "x"},
		{"targetGroupName 过短", "targetGroupName", " "},
		{"providerCode 过长", "providerCode", strings.Repeat("p", 61)},
		{"profileId 过长", "providerProtocolProfileId", strings.Repeat("p", 121)},
		{"name 非字符串", "name", 1.5},
		{"type 非字符串", "type", 1.5},
		{"baseUrl 非字符串", "baseUrl", 1.5},
		{"apiKey 非字符串", "apiKey", 1.5},
		{"status 非法", "status", "bogus"},
		{"notes 过长", "notes", strings.Repeat("n", 1001)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := map[string]any{}
			for key, value := range base {
				body[key] = value
			}
			body[testCase.field] = testCase.value
			if _, issue := parseAccountUpdateBody(body); issue == "" {
				t.Fatalf("%s 必须报错", testCase.field)
			}
		})
	}
	// supportedModels 顶层非数组。
	if _, issue := parseAccountUpdateBody(map[string]any{"accountId": "a", "supportedModels": "x"}); issue != zodInvalidType("array", "x") {
		t.Fatalf("模型非数组: %q", issue)
	}
	// 账号 501 个模型触发上限。
	many := make([]any, 501)
	for index := range many {
		many[index] = "m"
	}
	if _, issue := parseAccountUpdateBody(map[string]any{"accountId": "a", "supportedModels": many}); issue != "Array must contain at most 500 element(s)" {
		t.Fatalf("模型上限: %q", issue)
	}
}

// TestWCParseAccountDeleteBodyErrors：account delete 解析器的可选字段分支。
func TestWCParseAccountDeleteBodyErrors(t *testing.T) {
	if _, issue := parseAccountDeleteBody(map[string]any{"accountId": "a", "targetUsername": "x"}); issue != zodStringMin(2) {
		t.Fatalf("targetUsername 过短: %q", issue)
	}
	if _, issue := parseAccountDeleteBody(map[string]any{"accountId": "a", "providerCode": 1.5}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("providerCode 非字符串: %q", issue)
	}
	parsed, issue := parseAccountDeleteBody(map[string]any{"accountId": " a "})
	if issue != "" || parsed.AccountID != "a" || parsed.HasTarget {
		t.Fatalf("最小载荷: %+v %q", parsed, issue)
	}
}

// TestWCParseAccountAddLimits：account add 的并发/优先级边界。
func TestWCParseAccountAddLimits(t *testing.T) {
	base := map[string]any{"targetUsername": "ab", "targetGroupName": "g", "providerCode": "p",
		"providerProtocolProfileId": "pf", "name": "n", "type": "api_key", "baseUrl": "u", "apiKey": "k"}
	if _, issue := parseAccountAddBody(mergeBody(base, "priority", -1.0)); issue != zodNumberMin(0) {
		t.Fatalf("priority 下限: %q", issue)
	}
	if _, issue := parseAccountAddBody(mergeBody(base, "concurrencyLimit", 100001.0)); issue != zodNumberMax(100000) {
		t.Fatalf("并发上限: %q", issue)
	}
	if _, issue := parseAccountAddBody(mergeBody(base, "status", "pending_test")); issue != zodEnumMessage([]string{"active", "disabled"}, "pending_test") {
		t.Fatalf("status 枚举: %q", issue)
	}
	// concurrencyLimit/priority 为 null 时视为未提供（add 语义允许）。
	parsed, issue := parseAccountAddBody(mergeBody(base, "concurrencyLimit", nil))
	if issue != "" || parsed.ConcurrencyLimit != nil {
		t.Fatalf("null 并发: %+v %q", parsed, issue)
	}
}

func mergeBody(base map[string]any, key string, value any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	out[key] = value
	return out
}

// TestWCParseApiKeyUpdateBodyErrors：api-key update 的单行错误分支。
func TestWCParseApiKeyUpdateBodyErrors(t *testing.T) {
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k", "name": 1.5}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("name 非字符串: %q", issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k", "description": 1.5}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("description 非字符串: %q", issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k", "expiresAt": 1.5}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("expiresAt 非字符串: %q", issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k", "targetUsername": "x"}); issue != zodStringMin(2) {
		t.Fatalf("targetUsername 过短: %q", issue)
	}
	if _, issue := parseApiKeyAddBody(map[string]any{"targetUsername": "ab", "name": "n", "routeStrategyId": "s", "availabilitySchedule": 1.5}); issue != "" {
		// add 阶段 schedule 透传不限型，确认不报错。
		t.Fatalf("add schedule 透传: %q", issue)
	}
}

// TestWCParseGroupStrategySingleLines：group/strategy 解析器残余单行分支。
func TestWCParseGroupStrategySingleLines(t *testing.T) {
	if _, issue := parseGroupUpdateBody(map[string]any{"groupId": "g", "name": 1.5}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("group name 非字符串: %q", issue)
	}
	if _, issue := parseGroupUpdateBody(map[string]any{"groupId": "g", "providerCode": ""}); issue != zodStringMin(1) {
		t.Fatalf("providerCode 空白: %q", issue)
	}
	if _, issue := parseGroupUpdateBody(map[string]any{"groupId": "g", "targetUsername": " "}); issue != zodStringMin(2) {
		t.Fatalf("targetUsername 空白: %q", issue)
	}
	if _, issue := parseStrategyUpdateBody(map[string]any{"routeStrategyId": "s", "description": 1.5}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("description 非字符串: %q", issue)
	}
	if _, issue := parseStrategyUpdateBody(map[string]any{"routeStrategyId": "s", "targetUsername": " "}); issue != zodStringMin(2) {
		t.Fatalf("targetUsername 空白: %q", issue)
	}
	if _, issue := parseStrategyUpdateBody(map[string]any{"routeStrategyId": "s", "name": ""}); issue != zodStringMin(1) {
		t.Fatalf("name 空白: %q", issue)
	}
	// 绑定 status 非字符串。
	if _, issue := parseStrategyBindings(map[string]any{"groupBindings": []any{map[string]any{"groupId": "g", "status": 1.5}}}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("绑定 status 非字符串: %q", issue)
	}
	// 绑定 priority 非整数。
	if _, issue := parseStrategyBindings(map[string]any{"groupBindings": []any{map[string]any{"groupId": "g", "priority": 1.5}}}); issue != zodNumberMin(1) {
		t.Fatalf("绑定 priority 浮点: %q", issue)
	}
}

// TestWCZodAbsentBranches：可选字段缺省分支的直接覆盖。
func TestWCZodAbsentBranches(t *testing.T) {
	if _, present, issue := bodyOptionalString("x", false); present || issue != "" {
		t.Fatalf("bodyOptionalString 缺省: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalBool(true, false); present || issue != "" {
		t.Fatalf("bodyOptionalBool 缺省: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalEnum("a", false, []string{"a"}); present || issue != "" {
		t.Fatalf("bodyOptionalEnum 缺省: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalInt(1.0, false, 1, 0); present || issue != "" {
		t.Fatalf("bodyOptionalInt 缺省: %v %q", present, issue)
	}
	if text, issue := trimmedBodyString("x", false, 1, 3); text != nil || issue != "" {
		t.Fatalf("trimmedBodyString 缺省: %v %q", text, issue)
	}
	if text, issue := nullableTrimmedBodyString("x", false, 3); text != nil || issue != "" {
		t.Fatalf("nullableTrimmedBodyString 缺省: %v %q", text, issue)
	}
}

// TestWCValidateTokenBlank：空 token 直接 401，不查库。
func TestWCValidateTokenBlank(t *testing.T) {
	deps := &Deps{}
	_, authErr := deps.ValidateToken(t.Context(), "   ", scopeGroupListRead)
	if authErr == nil || authErr.StatusCode != http.StatusUnauthorized || authErr.Code != "external_source_token_missing" {
		t.Fatalf("空 token: %#v", authErr)
	}
}

// TestWCSanitizeAccountSchedule：账号投影的时间计划非 nil 透传。
func TestWCSanitizeAccountSchedule(t *testing.T) {
	schedule := &accounts.AvailabilitySchedule{}
	item := sanitizeAccountItem(&accounts.ListItem{ID: "a", AvailabilitySchedule: schedule}, nil)
	if item.AvailabilitySchedule == nil {
		t.Fatalf("时间计划必须透传")
	}
}

// TestWCDecodeCaptureBodyInvalidJSON：未标记失败但 JSON 损坏的兜底。
func TestWCDecodeCaptureBodyInvalidJSON(t *testing.T) {
	if decodeCaptureBody(&captureRequestBody{raw: []byte("not json")}) != nil {
		t.Fatalf("损坏 JSON 必须为 nil")
	}
}

// TestWCMockGroupEnabledDefaults：mock group 的 enabled/groupType 缺省分支。
func TestWCMockGroupEnabledDefaults(t *testing.T) {
	deps := &Deps{}
	recorder := httptest.NewRecorder()
	deps.mockGroupAdd(recorder, map[string]any{"providerCode": "gpt", "enabled": "not-bool"})
	if !strings.Contains(recorder.Body.String(), `"enabled":true`) {
		t.Fatalf("非布尔 enabled 回退 true: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockGroupUpdate(recorder, map[string]any{"enabled": false})
	if !strings.Contains(recorder.Body.String(), `"enabled":false`) {
		t.Fatalf("显式 false 生效: %s", recorder.Body.String())
	}
}

// TestWCUpdateMappingLongTail：api-key/策略 update 的字段映射长尾
// （quotaLimits/availabilitySchedule/routeStrategyId/expiresAt 透传与
// 策略绑定/normal 配置替换）。
func TestWCUpdateMappingLongTail(t *testing.T) {
	env := newWCLifeEnv(t)
	groupID := wcAddGroup(t, env, "pusher", "长尾组")
	secondGroup := wcAddGroup(t, env, "pusher", "第二组")
	strategyID := wcAddStrategy(t, env, "pusher", "长尾策略", groupID)
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/api-key/add",
		`{"targetUsername":"pusher","name":"长尾Key","routeStrategyId":"`+strategyID+`"}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("建 key: %d %v", status, payload)
	}
	keyID := payload["data"].(map[string]any)["apiKey"].(map[string]any)["id"].(string)
	accountID := wcAddAccount(t, env, wcAccountBase)

	// api-key：四类可变字段一起提交；quotaLimits 非法字段由 store 拒绝，
	// 但字段映射分支全部执行。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/api-key/update",
		`{"apiKeyId":"`+keyID+`","routeStrategyId":"`+strategyID+`","expiresAt":"2030-01-01T00:00:00.000Z",`+
			`"quotaLimits":{"bogus":1},"availabilitySchedule":{"enabled":true}}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "请求额度限制包含不支持字段：bogus" {
		t.Fatalf("quotaLimits 校验: %d %v", status, payload)
	}

	// 策略：绑定替换 + normal 配置 + 描述。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/update",
		`{"routeStrategyId":"`+strategyID+`","mode":"normal","groupBindings":[{"groupId":"`+secondGroup+`","weight":70}],`+
			`"normalRoutingConfig":{"schedulingPreference":"cost_first"},"description":"新描述"}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("策略 update: %d %v", status, payload)
	}
	updated := payload["data"].(map[string]any)["routeStrategy"].(map[string]any)
	if updated["description"] != "新描述" {
		t.Fatalf("策略描述: %v", updated)
	}
	if binding := updated["groupBindings"].([]any)[0].(map[string]any); binding["groupId"] != secondGroup {
		t.Fatalf("策略绑定替换: %v", binding)
	}

	// 账号：targetGroupName 指向存在但未绑定的分组 → 400 账号不存在
	//（命中成员校验分支）。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","targetGroupName":"第二组","name":"改名"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "账号不存在" {
		t.Fatalf("未绑定分组: %d %v", status, payload)
	}
}

// TestWCDeleteFilterMismatch：账号删除携带不匹配的分组过滤 → 400 账号不存在；
// 策略 update/del 携带非属主用户名 → 404。
func TestWCDeleteFilterMismatch(t *testing.T) {
	env := newWCLifeEnv(t)
	accountID := wcAddAccount(t, env, wcAccountBase)
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/del",
		`{"accountId":"`+accountID+`","targetGroupName":"不存在组","providerCode":"gpt"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "账号不存在" {
		t.Fatalf("删除分组过滤不符: %d %v", status, payload)
	}

	groupID := wcAddGroup(t, env, "pusher", "归属组")
	strategyID := wcAddStrategy(t, env, "pusher", "归属策略", groupID)
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/update",
		`{"routeStrategyId":"`+strategyID+`","targetUsername":"ghost","name":"x"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "路由策略不存在" {
		t.Fatalf("策略非属主 update: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/del",
		`{"routeStrategyId":"`+strategyID+`","targetUsername":"ghost"}`, wcLifeToken)
	if status != http.StatusNotFound || payload["message"] != "路由策略不存在" {
		t.Fatalf("策略非属主 del: %d %v", status, payload)
	}
}

// TestWCBrokenJSONEverywhere：12 条 POST 路由的 JSON 解析失败统一走
// 400 请求体无效（kernel.DecodeJSON 契约）。
func TestWCBrokenJSONEverywhere(t *testing.T) {
	env := newWCLifeEnv(t)
	routes := []string{
		"/group/add", "/group/update", "/group/del",
		"/route-strategy/add", "/route-strategy/update", "/route-strategy/del",
		"/api-key/add", "/api-key/update", "/api-key/del",
		"/account/add", "/account/update", "/account/del",
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			status, payload, _ := env.doAuth(http.MethodPost, Prefix+route, `{"broken`, wcLifeToken)
			if status != http.StatusBadRequest || payload["message"] != "请求体无效" {
				t.Fatalf("%s: %d %v", route, status, payload)
			}
		})
	}
	// del 家族的 targetUsername 过短分支。
	status, _, _ := env.doAuth(http.MethodPost, Prefix+"/api-key/del", `{"targetUsername":"x","apiKeyId":"k"}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("api-key del 用户名过短: %d", status)
	}
	status, _, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/del", `{"targetUsername":"x","routeStrategyId":"s"}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("strategy del 用户名过短: %d", status)
	}
	status, _, _ = env.doAuth(http.MethodPost, Prefix+"/group/del", `{"targetUsername":"x","groupId":"g"}`, wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("group del 用户名过短: %d", status)
	}
}
