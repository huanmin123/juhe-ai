// 请求解析器与投影杂项补测：group/route-strategy/api-key/account 各 schema
// 解析器的全分支表驱动用例（HTTP 层无法经济覆盖的缺省/错误分支），以及
// 操作日志记录器、mock 处理器缺省回退、capture holder 的直接单测。
package aipublic

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/publicapilogs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// mustParseQuery 构造 url.Values。
func mustParseQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return values
}

// TestWCBodyString：body 字段读取的存在性/类型分支（zod.go 的单值形态）。
func TestWCBodyString(t *testing.T) {
	if _, ok := bodyString(nil); ok {
		t.Fatalf("nil 必须 ok=false")
	}
	if _, ok := bodyString(1.5); ok {
		t.Fatalf("非字符串必须 ok=false")
	}
	if text, ok := bodyString("v"); !ok || text != "v" {
		t.Fatalf("字符串: %q %v", text, ok)
	}
}

// TestWCParseGroupListQuery：group 列表 query 的错误与缺省分支。
func TestWCParseGroupListQuery(t *testing.T) {
	if _, issue := parseGroupListQuery(mustParseQuery(t, "x=1")); issue != "Unrecognized key(s) in object: x" {
		t.Fatalf("未知键: %q", issue)
	}
	if _, issue := parseGroupListQuery(mustParseQuery(t, "targetUsername=a")); issue != zodStringMin(2) {
		t.Fatalf("用户名过短: %q", issue)
	}
	if _, issue := parseGroupListQuery(mustParseQuery(t, "targetUsername=ab&providerCode=")); issue != zodStringMin(1) {
		t.Fatalf("providerCode 空白: %q", issue)
	}
	if _, issue := parseGroupListQuery(mustParseQuery(t, "targetUsername=ab&keyword="+strings.Repeat("k", 81))); issue != zodStringMax(80) {
		t.Fatalf("keyword 过长: %q", issue)
	}
	query, issue := parseGroupListQuery(mustParseQuery(t, "targetUsername=ab&providerCode=gpt&keyword=k&page=2&pageSize=5"))
	if issue != "" || !query.HasProvider || !query.HasKeyword || query.Page != 2 || query.PageSize != 5 {
		t.Fatalf("全量解析: %+v %q", query, issue)
	}
	if empty, issue := parseGroupListQuery(mustParseQuery(t, "targetUsername=ab")); issue != "" || empty.HasProvider || empty.HasKeyword || empty.HasPage || empty.HasPageSize {
		t.Fatalf("缺省分支: %+v %q", empty, issue)
	}
}

// TestWCParseGroupMutationBodies：group add/update body 的分支。
func TestWCParseGroupMutationBodies(t *testing.T) {
	add, issue := parseGroupAddBody(map[string]any{
		"targetUsername": " ab ", "targetDisplayName": " 展示 ", "name": " 组 ",
		"providerCode": " gpt ", "enabled": false, "groupType": "high_concurrency",
	})
	if issue != "" || add.TargetUsername != "ab" || add.TargetDisplayName == nil || *add.TargetDisplayName != "展示" ||
		add.Name != "组" || add.ProviderCode != "gpt" || add.Description != nil || !add.HasEnabled ||
		add.Enabled == nil || *add.Enabled || add.GroupType != "high_concurrency" {
		t.Fatalf("add 全字段: %+v %q", add, issue)
	}
	if _, issue := parseGroupAddBody(map[string]any{"targetUsername": "ab", "name": "n", "providerCode": "gpt", "enabled": "x"}); issue != zodInvalidType("boolean", "x") {
		t.Fatalf("enabled 非布尔: %q", issue)
	}
	if _, issue := parseGroupAddBody(map[string]any{"targetUsername": "ab", "name": "n", "providerCode": "gpt", "targetDisplayName": ""}); issue != zodStringMin(1) {
		t.Fatalf("displayName 空白: %q", issue)
	}

	update, issue := parseGroupUpdateBody(map[string]any{
		"targetUsername": "ab", "groupId": " g1 ", "name": " n ", "providerCode": " p ",
		"description": nil, "enabled": true, "groupType": "personal",
	})
	if issue != "" || !update.HasTarget || update.GroupID != "g1" || update.Name == nil || *update.Name != "n" ||
		update.ProviderCode == nil || !update.HasDescription || update.Enabled == nil || !update.HasGroupType {
		t.Fatalf("update 全字段: %+v %q", update, issue)
	}
	if _, issue := parseGroupUpdateBody(map[string]any{"groupId": "g", "enabled": 1.5}); issue != zodInvalidType("boolean", 1.5) {
		t.Fatalf("enabled 非布尔: %q", issue)
	}
	if _, issue := parseGroupUpdateBody(map[string]any{"groupId": "g", "groupType": "bogus"}); issue != zodEnumMessage([]string{"personal", "high_concurrency"}, "bogus") {
		t.Fatalf("groupType 非法: %q", issue)
	}
	if _, issue := parseGroupUpdateBody(map[string]any{"groupId": "g", "name": ""}); issue != zodStringMin(1) {
		t.Fatalf("name 空白: %q", issue)
	}
	// 仅 targetUsername+groupId 不满足可变字段。
	if _, issue := parseGroupUpdateBody(map[string]any{"targetUsername": "ab", "groupId": "g"}); issue != "分组修改至少提供一个要修改的字段" {
		t.Fatalf("无可变字段: %q", issue)
	}
}

// TestWCParseStrategyBodies：strategy 解析器的分支矩阵。
func TestWCParseStrategyBodies(t *testing.T) {
	if _, issue := parseStrategyListQuery(mustParseQuery(t, "targetUsername=ab&keyword="+strings.Repeat("k", 121))); issue != zodStringMax(120) {
		t.Fatalf("keyword 过长: %q", issue)
	}
	query, issue := parseStrategyListQuery(mustParseQuery(t, "targetUsername=ab&keyword=k&mode=merge&status=disabled&page=3&pageSize=7"))
	if issue != "" || query.Mode != "merge" || query.Status != "disabled" || query.Page != 3 {
		t.Fatalf("list 全量: %+v %q", query, issue)
	}

	add, issue := parseStrategyAddBody(map[string]any{
		"targetUsername": "ab", "name": "n", "description": nil, "mode": "normal", "status": "active",
		"groupBindings":       []any{map[string]any{"groupId": "g", "priority": 2.0, "weight": 30.0, "status": "disabled"}},
		"normalRoutingConfig": map[string]any{},
	})
	if issue != "" || add.Description != nil || add.Mode != "normal" || add.Status != "active" ||
		!add.HasBindings || add.Bindings[0].Priority == nil || add.Bindings[0].Weight == nil ||
		add.Bindings[0].Status != "disabled" || !add.HasNormalConfig {
		t.Fatalf("add 全字段: %+v %q", add, issue)
	}
	// mode 枚举先于绑定校验（字段序即 zod 定义序）。
	if _, issue := parseStrategyAddBody(map[string]any{"targetUsername": "ab", "name": "n", "groupBindings": []any{}, "mode": "bogus"}); issue != zodEnumMessage(strategyMutationModes, "bogus") {
		t.Fatalf("mode 枚举先报: %q", issue)
	}
	if _, issue := parseStrategyAddBody(map[string]any{"targetUsername": "ab", "name": "n", "groupBindings": []any{}}); issue != "Array must contain at least 1 element(s)" {
		t.Fatalf("空绑定: %q", issue)
	}
	if _, issue := parseStrategyAddBody(map[string]any{"targetUsername": "ab", "name": "n", "groupBindings": []any{1.5}}); issue != zodInvalidType("object", 1.5) {
		t.Fatalf("绑定非对象: %q", issue)
	}
	if _, issue := parseStrategyAddBody(map[string]any{"targetUsername": "ab", "name": "n", "groupBindings": []any{}, "status": "bogus"}); issue == "" {
		t.Fatalf("status 非法必须报错")
	}

	update, issue := parseStrategyUpdateBody(map[string]any{
		"targetUsername": "ab", "routeStrategyId": " s1 ", "name": " n2 ", "description": " d ",
		"mode": "weighted", "status": "disabled",
		"groupBindings":       []any{map[string]any{"groupId": "g"}},
		"normalRoutingConfig": nil,
	})
	if issue != "" || !update.HasTarget || update.RouteStrategyID != "s1" || !update.HasName ||
		!update.HasDescription || update.Description == nil || *update.Description != "d" ||
		update.Mode != "weighted" || update.Status != "disabled" || !update.HasBindings || !update.HasNormalConfig {
		t.Fatalf("update 全字段: %+v %q", update, issue)
	}
	// 21 个绑定触发上限。
	many := make([]any, 21)
	for index := range many {
		many[index] = map[string]any{"groupId": "g"}
	}
	if _, issue := parseStrategyUpdateBody(map[string]any{"routeStrategyId": "s", "groupBindings": many}); issue != "Array must contain at most 20 element(s)" {
		t.Fatalf("绑定上限: %q", issue)
	}
	if _, issue := parseStrategyUpdateBody(map[string]any{"routeStrategyId": "s"}); issue != "路由策略修改至少提供一个要修改的字段" {
		t.Fatalf("无可变字段: %q", issue)
	}
}

// TestWCParseApiKeyBodies：api-key 解析器的分支矩阵。
func TestWCParseApiKeyBodies(t *testing.T) {
	if _, issue := parseApiKeyListQuery(mustParseQuery(t, "targetUsername=ab&routeStrategyId="+strings.Repeat("s", 121))); issue != zodStringMax(120) {
		t.Fatalf("strategyId 过长: %q", issue)
	}
	query, issue := parseApiKeyListQuery(mustParseQuery(t, "targetUsername=ab&routeStrategyId=s&keyword=k&status=disabled&page=2&pageSize=9"))
	if issue != "" || !query.HasStrategy || query.Status != "disabled" || query.PageSize != 9 {
		t.Fatalf("list 全量: %+v %q", query, issue)
	}

	add, issue := parseApiKeyAddBody(map[string]any{
		"targetUsername": "ab", "name": "n", "description": nil, "routeStrategyId": " s ",
		"status": "disabled", "expiresAt": " e ", "quotaLimits": map[string]any{}, "availabilitySchedule": map[string]any{},
	})
	if issue != "" || add.Description != nil || add.RouteStrategyID != "s" || add.Status != "disabled" ||
		add.ExpiresAt != "e" || !add.HasExpiresAt || !add.HasQuotaLimits || !add.HasSchedule {
		t.Fatalf("add 全字段: %+v %q", add, issue)
	}
	if parsed, issue := parseApiKeyAddBody(map[string]any{"targetUsername": "ab", "name": "n", "routeStrategyId": "s", "expiresAt": " "}); issue != "" || !parsed.HasExpiresAt || parsed.ExpiresAt != "" {
		t.Fatalf("expiresAt 空白合法（min 0）: %+v %q", parsed, issue)
	}

	update, issue := parseApiKeyUpdateBody(map[string]any{
		"targetUsername": "ab", "apiKeyId": " k1 ", "name": " n ", "description": nil,
		"routeStrategyId": " s2 ", "status": "active", "expiresAt": nil,
		"quotaLimits": map[string]any{}, "availabilitySchedule": map[string]any{},
	})
	if issue != "" || !update.HasTarget || update.ApiKeyID != "k1" || !update.HasName || update.Name == nil ||
		update.Description != nil || update.RouteStrategyID != "s2" || update.Status != "active" ||
		!update.HasExpiresAt || update.ExpiresAt != nil || !update.HasQuotaLimits || !update.HasSchedule {
		t.Fatalf("update 全字段: %+v %q", update, issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k", "routeStrategyId": ""}); issue == "" {
		t.Fatalf("空 routeStrategyId 必须报错")
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k"}); issue != "API Key 修改至少提供一个要修改的字段" {
		t.Fatalf("无可变字段: %q", issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k", "status": "bogus"}); issue != zodEnumMessage([]string{"active", "disabled"}, "bogus") {
		t.Fatalf("status 非法: %q", issue)
	}
}

// TestWCParseAccountListQuery：account 列表 query 的全字段分支。
func TestWCParseAccountListQuery(t *testing.T) {
	long := strings.Repeat("x", 81)
	if _, issue := parseAccountListQuery(mustParseQuery(t, "targetUsername=ab&targetGroupName="+long)); issue != zodStringMax(80) {
		t.Fatalf("groupName 过长: %q", issue)
	}
	if _, issue := parseAccountListQuery(mustParseQuery(t, "targetUsername=ab&providerCode=")); issue != zodStringMin(1) {
		t.Fatalf("providerCode 空白: %q", issue)
	}
	if _, issue := parseAccountListQuery(mustParseQuery(t, "targetUsername=ab&groupId=")); issue != zodStringMin(1) {
		t.Fatalf("groupId 空白: %q", issue)
	}
	if _, issue := parseAccountListQuery(mustParseQuery(t, "targetUsername=ab&status="+strings.Repeat("s", 201))); issue != zodStringMax(200) {
		t.Fatalf("status 过长: %q", issue)
	}
	if _, issue := parseAccountListQuery(mustParseQuery(t, "targetUsername=ab&schedulable=bogus")); issue != zodEnumMessage(accountSchedulableOptions, "bogus") {
		t.Fatalf("schedulable 非法: %q", issue)
	}
	query, issue := parseAccountListQuery(mustParseQuery(t,
		"targetUsername=ab&targetGroupName=福利&providerCode=gpt&providerProtocolProfileId=p&groupId=g&keyword=k&type=api_key&status=active&schedulable=cooling&page=4&pageSize=8"))
	if issue != "" || !query.HasGroupName || !query.HasProvider || !query.HasProfileID || !query.HasGroupID ||
		!query.HasKeyword || !query.HasType || !query.HasStatus || !query.HasSchedulable ||
		query.Page != 4 || query.PageSize != 8 || query.Schedulable != "cooling" {
		t.Fatalf("list 全量: %+v %q", query, issue)
	}
}

// TestWCParseAccountMutationBodies：account add/update/delete 解析器分支。
func TestWCParseAccountMutationBodies(t *testing.T) {
	add, issue := parseAccountAddBody(map[string]any{
		"targetUsername": "ab", "targetGroupName": " g ", "providerCode": " p ",
		"providerProtocolProfileId": " pf ", "name": " n ", "type": "api_key", "baseUrl": " u ", "apiKey": " k ",
		"supportedModels": []any{" a "}, "status": "disabled", "concurrencyLimit": 5.0, "priority": 0.0,
		"availabilitySchedule": map[string]any{}, "notes": " x ",
	})
	if issue != "" || add.TargetDisplayName != nil || add.TargetGroupName != "g" || add.AccountType != "api_key" ||
		add.SupportedModels == nil || len(*add.SupportedModels) != 1 || (*add.SupportedModels)[0] != "a" ||
		add.Status != "disabled" || add.ConcurrencyLimit == nil || add.Priority == nil || !add.HasSchedule || add.Notes == nil {
		t.Fatalf("add 全字段: %+v %q", add, issue)
	}
	if _, issue := parseAccountAddBody(map[string]any{"targetUsername": "ab", "targetGroupName": "g", "providerCode": "p",
		"providerProtocolProfileId": "pf", "name": "n", "type": nil}); issue != "公开账号接口仅支持 API Key 账户" {
		t.Fatalf("type null: %q", issue)
	}
	if _, issue := parseAccountAddBody(map[string]any{"targetUsername": "ab", "targetGroupName": "g", "providerCode": "p", "providerProtocolProfileId": "pf", "name": "n", "type": "api_key", "baseUrl": "u", "apiKey": "k", "supportedModels": []any{1.5}}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("模型非字符串: %q", issue)
	}
	if _, issue := parseAccountAddBody(map[string]any{"targetUsername": "ab", "targetGroupName": "g", "providerCode": "p", "providerProtocolProfileId": "pf", "name": "n", "type": "api_key", "baseUrl": "u", "apiKey": "k", "supportedModels": []any{strings.Repeat("m", 121)}}); issue != zodStringMax(120) {
		t.Fatalf("模型过长: %q", issue)
	}
	if _, issue := parseAccountAddBody(map[string]any{"targetUsername": "ab", "targetGroupName": "g", "providerCode": "p", "providerProtocolProfileId": "pf", "name": "n", "type": "api_key", "baseUrl": "u", "apiKey": "k", "supportedModels": []any{""}}); issue != zodStringMin(1) {
		t.Fatalf("模型空白: %q", issue)
	}
	if _, issue := parseAccountAddBody(map[string]any{"targetUsername": "ab", "targetGroupName": "g", "providerCode": "p", "providerProtocolProfileId": "pf", "name": "n", "type": "api_key", "baseUrl": "u", "apiKey": "k", "notes": ""}); issue != "" {
		t.Fatalf("notes 空白合法（min 0）: %q", issue)
	}
	if _, issue := parseAccountAddBody(map[string]any{"targetUsername": "ab", "targetGroupName": "g", "providerCode": "p", "providerProtocolProfileId": "pf", "name": "n", "type": "api_key", "baseUrl": "u", "apiKey": "k", "notes": strings.Repeat("n", 1001)}); issue != zodStringMax(1000) {
		t.Fatalf("notes 过长: %q", issue)
	}

	update, issue := parseAccountUpdateBody(map[string]any{
		"accountId": " a1 ", "targetUsername": "ab", "targetGroupName": " g ", "providerCode": " p ",
		"providerProtocolProfileId": " pf ", "name": " n ", "type": "api_key", "baseUrl": " u ", "apiKey": " k ",
		"supportedModels": []any{" m "}, "status": "active", "concurrencyLimit": 5.0, "priority": 1.0,
		"availabilitySchedule": map[string]any{}, "notes": " x ",
	})
	if issue != "" || update.AccountID != "a1" || !update.HasGroupName || !update.HasProvider ||
		!update.HasProfileID || !update.HasName || !update.HasType || !update.HasBaseURL || !update.HasAPIKey ||
		!update.HasSupportedModels || update.Status != "active" || !update.HasConcurrencyLimit ||
		update.Priority == nil || !update.HasSchedule || !update.HasNotes {
		t.Fatalf("update 全字段: %+v %q", update, issue)
	}
	if _, issue := parseAccountUpdateBody(map[string]any{"accountId": "a", "concurrencyLimit": nil}); issue != zodInvalidType("number", nil) {
		t.Fatalf("concurrencyLimit null: %q", issue)
	}
	if _, issue := parseAccountUpdateBody(map[string]any{"accountId": "a", "priority": nil}); issue != zodInvalidType("number", nil) {
		t.Fatalf("priority null: %q", issue)
	}
	if _, issue := parseAccountUpdateBody(map[string]any{"accountId": "a", "notes": ""}); issue != "" {
		t.Fatalf("notes 空白: %q", issue)
	}
	if _, issue := parseAccountUpdateBody(map[string]any{"accountId": "a"}); issue != "账号修改至少提供一个要修改的字段" {
		t.Fatalf("无可变字段: %q", issue)
	}

	del, issue := parseAccountDeleteBody(map[string]any{
		"accountId": " a2 ", "targetUsername": "ab", "targetGroupName": " g ", "providerCode": " p ", "providerProtocolProfileId": " pf ",
	})
	if issue != "" || del.AccountID != "a2" || !del.HasTarget || del.TargetGroupName != "g" ||
		del.ProviderCode != "p" || del.ProfileID != "pf" {
		t.Fatalf("delete 全字段: %+v %q", del, issue)
	}
	if _, issue := parseAccountDeleteBody(map[string]any{"accountId": "a", "targetGroupName": ""}); issue != zodStringMin(1) {
		t.Fatalf("groupName 空白: %q", issue)
	}
	if _, issue := parseAccountDeleteBody(map[string]any{"accountId": "a", "providerProtocolProfileId": ""}); issue != zodStringMin(1) {
		t.Fatalf("profileId 空白: %q", issue)
	}
	if _, issue := parseAccountDeleteBody(map[string]any{}); issue != zodRequired {
		t.Fatalf("缺 accountId: %q", issue)
	}
}

// TestWCTargetDirectBranches：目标解析的入参守卫分支（不经 DB）。
func TestWCTargetDirectBranches(t *testing.T) {
	deps := &Deps{}
	if _, err := deps.requirePublicTarget(t.Context(), ""); err == nil || err.Error() != "目标用户不能为空" {
		t.Fatalf("requirePublicTarget 空用户名: %v", err)
	}
	if _, err := deps.ensureTargetSystemAccount(t.Context(), "", nil); err == nil || err.Error() != "目标用户不能为空" {
		t.Fatalf("ensureTargetSystemAccount 空用户名: %v", err)
	}
	if _, _, err := deps.ensureTargetGroup(t.Context(), "owner", "gpt", ""); err == nil || err.Error() != "目标分组不能为空" {
		t.Fatalf("ensureTargetGroup 空分组: %v", err)
	}
}

// TestWCDepsPlumbing：table/PG 方言、recordCapture nil、限流回退警告。
func TestWCDepsPlumbing(t *testing.T) {
	pg := &Deps{PGDialect: true}
	if got := pg.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("PG 表名: %q", got)
	}
	sqlite := &Deps{}
	if got := sqlite.table("accounts"); got != "accounts" {
		t.Fatalf("SQLite 表名: %q", got)
	}
	// recordCapture 在 Capture 缺省时静默。
	(&Deps{}).recordCapture(publicapilogs.CaptureSpec{})
	// Warn 缺省回退标准日志（不参与断言，只覆盖分支）。
	(&Deps{}).warnPenaltyFallback(assertError("redis down"))
	if (&Deps{}).clock().IsZero() {
		t.Fatalf("clock 缺省必须非零")
	}
}

// TestWCCaptureHolderNilSafe：holder 的 nil 接收者容错。
func TestWCCaptureHolderNilSafe(t *testing.T) {
	var holder *captureSourceHolder
	holder.set(&AuthContext{}) // nil 接收者必须安全
	if holder.get() != nil {
		t.Fatalf("nil holder get 必须为 nil")
	}
	real := &captureSourceHolder{}
	if real.get() != nil {
		t.Fatalf("空 holder get 必须为 nil")
	}
	expected := &AuthContext{SourceRefID: "s"}
	real.set(expected)
	if real.get() != expected {
		t.Fatalf("holder 必须原样取回")
	}
}

// TestWCDTOValueHelpers：时间计划的非 nil 投影。
func TestWCDTOValueHelpers(t *testing.T) {
	if availabilityScheduleValue(nil) != nil {
		t.Fatalf("nil 计划必须返回 nil")
	}
	schedule := &apikeys.AvailabilitySchedule{}
	if availabilityScheduleValue(schedule) == nil {
		t.Fatalf("非 nil 计划必须透传")
	}
	if normalRoutingConfigValue(nil) != nil {
		t.Fatalf("nil 配置必须返回 nil")
	}
	normal := &routestrategies.NormalRoutingConfig{}
	if normalRoutingConfigValue(normal) == nil {
		t.Fatalf("非 nil 配置必须透传")
	}
}

// TestWCOperationLogRecorders：账号写/删日志的 nil 守卫与记录分支。
func TestWCOperationLogRecorders(t *testing.T) {
	sink := &recordingAIPublicSink{}
	deps := &Deps{Sink: sink}
	request := httptest.NewRequest(http.MethodPost, Prefix+"/account/add", nil)
	target := &ResolvedPublicTarget{Public: PublicTarget{Username: "u", SystemAccountID: "own"}, SystemAccountID: "own"}
	context := &AuthContext{SourceRefID: "src", SourceName: "来源"}

	// 写日志：action 归一 + 标量投影。
	deps.recordAccountWriteLog(request, context, "account_add", "op_key", "新增",
		"acc1", "站点", target, true, map[string]any{"status": "active", "schedulable": true})
	// 删日志。
	deps.recordAccountDeleteLog(request, context, "acc1", "站点", target,
		&accountDeleteBody{TargetGroupName: "福利"})
	// nil context / 空 accountID 的守卫分支。
	deps.recordAccountWriteLog(request, nil, "account_update", "op", "修改", "acc1", "站点", target, false, nil)
	deps.recordAccountWriteLog(request, context, "account_update", "op", "修改", "", "站点", target, false, nil)

	if len(sink.Entries) != 2 {
		t.Fatalf("必须记录两条日志: %d", len(sink.Entries))
	}
	created := sink.Entries[0]
	if created.Action != "account_add" || created.ActorSystemAccountID != "external:src" ||
		created.Summary != "来源 新增账号：站点" || len(created.Changes) != 5 {
		t.Fatalf("写日志: %+v", created)
	}
	if created.Changes[0].After != "created" || created.Changes[4].After != "true" {
		t.Fatalf("写日志 changes: %+v", created.Changes)
	}
	deleted := sink.Entries[1]
	if deleted.Action != "account_delete" || deleted.Summary != "来源 删除账号：站点" || len(deleted.Changes) != 1 {
		t.Fatalf("删日志: %+v", deleted)
	}
	if deleted.Summary == "" || deleted.Changes[0].After != "true" {
		t.Fatalf("删日志变更: %+v", deleted.Changes)
	}
}

// TestWCWriteAccountNotFoundDelete：not_found 删除包络的投影。
func TestWCWriteAccountNotFoundDelete(t *testing.T) {
	deps := &Deps{Now: func() time.Time { return time.Unix(0, 0) }}
	recorder := httptest.NewRecorder()
	deps.writeAccountNotFoundDelete(recorder, &accountDeleteBody{TargetUsername: " ghost ", TargetGroupName: " G "})
	if recorder.Code != http.StatusOK {
		t.Fatalf("not_found 删除必须 200: %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"action":"not_found"`) {
		t.Fatalf("包络: %s", recorder.Body.String())
	}
}

// TestWCWriteApiKeyActionNilTarget：api-key 动作包络的空目标回退。
func TestWCWriteApiKeyActionNilTarget(t *testing.T) {
	deps := &Deps{}
	recorder := httptest.NewRecorder()
	deps.writeApiKeyAction(recorder, "not_found", nil, nil)
	if !strings.Contains(recorder.Body.String(), `"target":{"username":"","displayName":"","systemAccountId":"","created":false}`) {
		t.Fatalf("空目标投影: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"apiKey":null`) {
		t.Fatalf("空摘要: %s", recorder.Body.String())
	}
}

// TestWCMockHandlersDirectDefaults：mock 处理器的缺省回退分支（HTTP 层被
// schema 必填约束挡住的路径）。
func TestWCMockHandlersDirectDefaults(t *testing.T) {
	deps := &Deps{}
	recorder := httptest.NewRecorder()
	deps.mockGroupUpdate(recorder, map[string]any{})
	if !strings.Contains(recorder.Body.String(), `"name":"公开接口分组"`) {
		t.Fatalf("group update 默认名: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockGroupDelete(recorder, map[string]any{})
	if !strings.Contains(recorder.Body.String(), `"id":"mock_group_public"`) {
		t.Fatalf("group del 默认 id: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockStrategyDelete(recorder, map[string]any{})
	if !strings.Contains(recorder.Body.String(), `"id":"mock_route_strategy_public"`) {
		t.Fatalf("strategy del 默认 id: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockApiKeyDelete(recorder, map[string]any{})
	if !strings.Contains(recorder.Body.String(), `"id":"mock_api_key_public"`) {
		t.Fatalf("api key del 默认 id: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockAccountList(recorder, map[string]string{}, 1, 20)
	if !strings.Contains(recorder.Body.String(), `公益站测试账号`) {
		t.Fatalf("account list 默认 keyword: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockAccountDelete(recorder, map[string]any{})
	if !strings.Contains(recorder.Body.String(), `"id":"mock_account_public_welfare"`) {
		t.Fatalf("account del 默认 id: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockStrategyAdd(recorder, map[string]any{"groupBindings": []any{map[string]any{"groupId": "g"}}})
	if !strings.Contains(recorder.Body.String(), `公开接口策略路由`) {
		t.Fatalf("strategy add 默认名: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockStrategyUpdate(recorder, map[string]any{})
	if !strings.Contains(recorder.Body.String(), `"status":"active"`) {
		t.Fatalf("strategy update 默认 status: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockApiKeyUpdate(recorder, map[string]any{"expiresAt": "2030-01-01T00:00:00Z"})
	if !strings.Contains(recorder.Body.String(), `"expiresAt":"2030-01-01T00:00:00Z"`) {
		t.Fatalf("api key update expiresAt: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockApiKeyAdd(recorder, map[string]any{"expiresAt": " e "})
	if !strings.Contains(recorder.Body.String(), `"expiresAt":"e"`) {
		t.Fatalf("api key add expiresAt 规范化: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.mockAccountPush(recorder, map[string]any{"targetDisplayName": "  "})
	// 空白展示名不覆盖默认 displayName（username 回退）。
	if !strings.Contains(recorder.Body.String(), `"displayName":"huanmin"`) {
		t.Fatalf("空白展示名回退: %s", recorder.Body.String())
	}
}

// TestWCOperationLogSinkGuard：Sink 缺省时记录器静默。
func TestWCOperationLogSinkGuard(t *testing.T) {
	deps := &Deps{}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	deps.recordAccountWriteLog(request, &AuthContext{}, "account_add", "k", "新增", "a", "n",
		&ResolvedPublicTarget{}, false, nil)
	deps.recordAccountDeleteLog(request, &AuthContext{}, "a", "n", &ResolvedPublicTarget{}, &accountDeleteBody{})
	var _ authsys.OperationLogSink = sinkOf(&recordingAIPublicSink{})
}

func sinkOf(sink *recordingAIPublicSink) authsys.OperationLogSink { return sink }
