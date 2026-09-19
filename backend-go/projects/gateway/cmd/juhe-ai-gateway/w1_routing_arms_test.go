package main

// W1 routing-arms 单元覆盖：chain_routing.go 的纯投影、请求视图、rehydrate
// 回源与 chainRoutingCache 桥接，以及 chain_openaicompat.go 的错误构造、
// bearer 提取和 scope resolver。真 *gatewayruntimecache.Service 复用
// chain_test.go 的 newChainFixture（sqlite 临时库 + 种子业务行）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w1rBoolPtr 提升 bool 到可选指针（projectGroupAccessForRouting 保留三态布尔）。
func w1rBoolPtr(value bool) *bool { return &value }

// w1rGatewayBodyRequest 构造携带 raw body / 解析对象 / 状态的 body 管线请求。
func w1rGatewayBodyRequest(raw string, parsed map[string]any, state *gatewaybody.BodyState) *gatewaybody.Request {
	return &gatewaybody.Request{RawBody: []byte(raw), Body: parsed, State: state}
}

// ---------------------------------------------------------------------------
// 纯投影：group access / account（覆盖清单 1）
// ---------------------------------------------------------------------------

// TestW1RProjectGroupAccessForRouting 锁定指针字段坍缩为渲染值的投影行为：
// GroupType/授权字段 deref，SchedulingPolicy 保持原样 JSON 文本，三态布尔
// 指针原样透传。
func TestW1RProjectGroupAccessForRouting(t *testing.T) {
	policy := gatewayruntimecache.GroupSchedulingPolicy{"schedulingPreference": "quality"}
	meta := gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      "sys_owner",
		ProviderCode:                   "openai",
		GroupAccessType:                "owner",
		GroupType:                      strPtrOf("personal"),
		SchedulingPolicy:               &policy,
		GroupAuthorizationID:           strPtrOf("authz_1"),
		GroupAuthorizationExpiresAt:    strPtrOf("2999-01-01T00:00:00.000Z"),
		GroupAuthorizationQuotaLimited: w1rBoolPtr(true),
		GroupAuthorizationSourceType:   strPtrOf("team"),
		GroupAuthorizationSourceTeamID: strPtrOf("team_1"),
	}
	projected := projectGroupAccessForRouting(meta)
	if projected.GroupOwnerSystemAccountID != "sys_owner" {
		t.Errorf("GroupOwnerSystemAccountID = %q, want sys_owner", projected.GroupOwnerSystemAccountID)
	}
	if projected.ProviderCode != "openai" || projected.GroupAccessType != "owner" {
		t.Errorf("ProviderCode/GroupAccessType = %q/%q, want openai/owner", projected.ProviderCode, projected.GroupAccessType)
	}
	if projected.GroupType != "personal" {
		t.Errorf("GroupType = %q, want personal（指针已 deref）", projected.GroupType)
	}
	if projected.SchedulingPolicy != `{"schedulingPreference":"quality"}` {
		t.Errorf("SchedulingPolicy = %q, want 原样 JSON 文本", projected.SchedulingPolicy)
	}
	if projected.GroupAuthorizationID != "authz_1" || projected.GroupAuthorizationExpiresAt != "2999-01-01T00:00:00.000Z" {
		t.Errorf("授权字段 = %q/%q, want authz_1/2999-01-01T00:00:00.000Z",
			projected.GroupAuthorizationID, projected.GroupAuthorizationExpiresAt)
	}
	if projected.GroupAuthorizationQuotaLimited == nil || !*projected.GroupAuthorizationQuotaLimited {
		t.Errorf("GroupAuthorizationQuotaLimited = %v, want 保留 true 指针", projected.GroupAuthorizationQuotaLimited)
	}
	if projected.GroupAuthorizationSourceType != "team" || projected.GroupAuthorizationSourceTeamID != "team_1" {
		t.Errorf("来源字段 = %q/%q, want team/team_1",
			projected.GroupAuthorizationSourceType, projected.GroupAuthorizationSourceTeamID)
	}
	// 空元数据：nil 指针全部坍缩为零值字符串，布尔指针保留 nil。
	empty := projectGroupAccessForRouting(gatewayruntimecache.GroupUsageAccessMetadata{})
	if empty.GroupType != "" || empty.SchedulingPolicy != "" || empty.GroupAuthorizationID != "" {
		t.Errorf("空元数据投影 = %+v, want 全零值字符串", empty)
	}
	if empty.GroupAuthorizationQuotaLimited != nil {
		t.Errorf("空元数据 GroupAuthorizationQuotaLimited = %v, want nil", empty.GroupAuthorizationQuotaLimited)
	}
}

// TestW1RProjectGroupAccessForRuntime 覆盖反向投影：字符串抬回指针、
// SchedulingPolicy 按 JSON 解析，非法 JSON 回退到
// {"schedulingPreference": <原文>}，空值保持 nil。
func TestW1RProjectGroupAccessForRuntime(t *testing.T) {
	base := gatewayrouting.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      "sys_owner",
		ProviderCode:                   "openai",
		GroupAccessType:                "authorized",
		GroupType:                      "high_concurrency",
		GroupAuthorizationID:           "authz_2",
		GroupAuthorizationSourceType:   "team",
		GroupAuthorizationSourceTeamID: "team_9",
	}
	out := projectGroupAccessForRuntime(base)
	if out.GroupOwnerSystemAccountID != "sys_owner" || out.ProviderCode != "openai" || out.GroupAccessType != "authorized" {
		t.Errorf("身份字段 = %+v, want 原样保留", out)
	}
	if out.GroupType == nil || *out.GroupType != "high_concurrency" {
		t.Errorf("GroupType = %v, want 指针 high_concurrency", out.GroupType)
	}
	if out.GroupAuthorizationID == nil || *out.GroupAuthorizationID != "authz_2" {
		t.Errorf("GroupAuthorizationID = %v, want 指针 authz_2", out.GroupAuthorizationID)
	}
	if out.GroupAuthorizationSourceType == nil || *out.GroupAuthorizationSourceType != "team" {
		t.Errorf("GroupAuthorizationSourceType = %v, want 指针 team", out.GroupAuthorizationSourceType)
	}
	if out.GroupAuthorizationSourceTeamID == nil || *out.GroupAuthorizationSourceTeamID != "team_9" {
		t.Errorf("GroupAuthorizationSourceTeamID = %v, want 指针 team_9", out.GroupAuthorizationSourceTeamID)
	}

	// SchedulingPolicy 合法 JSON：解析为策略对象。
	valid := base
	valid.SchedulingPolicy = `{"schedulingPreference":"quality","clientIpConcurrencyLimit":2}`
	out = projectGroupAccessForRuntime(valid)
	if out.SchedulingPolicy == nil {
		t.Fatal("合法 SchedulingPolicy 必须解析为策略指针")
	}
	if (*out.SchedulingPolicy)["schedulingPreference"] != "quality" || (*out.SchedulingPolicy)["clientIpConcurrencyLimit"] != float64(2) {
		t.Errorf("SchedulingPolicy 解析结果 = %+v, want quality/2", *out.SchedulingPolicy)
	}

	// SchedulingPolicy 非法 JSON：回退 {"schedulingPreference": 原文}。
	invalid := base
	invalid.SchedulingPolicy = `quality`
	out = projectGroupAccessForRuntime(invalid)
	if out.SchedulingPolicy == nil || (*out.SchedulingPolicy)["schedulingPreference"] != "quality" {
		t.Errorf("非法 SchedulingPolicy 回退 = %v, want {schedulingPreference: quality}", out.SchedulingPolicy)
	}

	// 空字段保持 nil 指针。
	blank := projectGroupAccessForRuntime(gatewayrouting.GroupUsageAccessMetadata{GroupType: "", SchedulingPolicy: ""})
	if blank.GroupType != nil || blank.SchedulingPolicy != nil {
		t.Errorf("空 GroupType/SchedulingPolicy = %v/%v, want nil/nil", blank.GroupType, blank.SchedulingPolicy)
	}
}

// TestW1RProjectAccountForRouting 覆盖账户投影：身份列原样、模型列表拷贝
// （修改投影不得影响源账户）、ModelMappings 有意留空。
func TestW1RProjectAccountForRouting(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "acc_1",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "prof_1",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		SupportedModels:           []string{"gpt-test"},
	}
	projected := projectAccountForRouting(account)
	if projected.ID != "acc_1" || projected.ProviderCode != "openai" || projected.ProviderProtocolProfileID != "prof_1" ||
		projected.ProtocolCode != "openai" || projected.ProtocolVersion != "v1" {
		t.Fatalf("投影身份列 = %+v, want acc_1/openai/prof_1/openai/v1", projected)
	}
	if len(projected.SupportedModels) != 1 || projected.SupportedModels[0] != "gpt-test" {
		t.Fatalf("SupportedModels = %v, want [gpt-test]", projected.SupportedModels)
	}
	projected.SupportedModels[0] = "mutated"
	if account.SupportedModels[0] != "gpt-test" {
		t.Fatalf("投影修改穿透到源账户: %v", account.SupportedModels)
	}
	if len(projected.ModelMappings) != 0 {
		t.Fatalf("ModelMappings = %v, want 空（路由层有意丢弃）", projected.ModelMappings)
	}
}

// ---------------------------------------------------------------------------
// 能力过滤器透传（覆盖清单 2）
// ---------------------------------------------------------------------------

// TestW1RChainCapabilityFilterPassesThrough 锁定透传语义：账户原样返回、
// 不跳过、无原因，避免投影损失静默清空分组。
func TestW1RChainCapabilityFilterPassesThrough(t *testing.T) {
	accounts := []gatewayrouting.UpstreamAccount{{ID: "acc_a"}, {ID: "acc_b"}}
	result := chainCapabilityFilter{}.FilterAccountsByRequestCapability(context.Background(), accounts,
		gatewayrouting.CapabilityFilterInput{RequestModel: "gpt-test", RequestClientCompatibility: "codex_responses"})
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "acc_a" || result.Accounts[1].ID != "acc_b" {
		t.Fatalf("过滤结果 = %+v, want 原样透传两个账户", result.Accounts)
	}
	if result.SkippedCount != 0 || result.Reason != "" {
		t.Fatalf("SkippedCount/Reason = %d/%q, want 0/空", result.SkippedCount, result.Reason)
	}
	empty := chainCapabilityFilter{}.FilterAccountsByRequestCapability(context.Background(), nil, gatewayrouting.CapabilityFilterInput{})
	if len(empty.Accounts) != 0 {
		t.Fatalf("空输入过滤 = %+v, want 空", empty.Accounts)
	}
}

// ---------------------------------------------------------------------------
// key row / config 投影（覆盖清单 5、6）
// ---------------------------------------------------------------------------

// TestW1RProjectAPIKeyRowForRouting 覆盖 nil 行、配置 JSON 指针坍缩与绑定行
// 数值列放宽（weight 恢复为指针联合）。
func TestW1RProjectAPIKeyRowForRouting(t *testing.T) {
	if row := projectAPIKeyRowForRouting(nil); row != nil {
		t.Fatalf("nil 行投影 = %+v, want nil", row)
	}
	configJSON := `{"k":1}`
	record := &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                      "key_1",
		SystemAccountID:         "sys_owner",
		RouteStrategyID:         "rs_1",
		RouteStrategyMode:       "normal",
		RouteStrategyConfigJSON: &configJSON,
		SelectedGroupID:         "group_main",
		Status:                  "active",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "b1", APIKeyID: "key_1", SystemAccountID: "sys_owner", GroupID: "g1", Priority: 3, Weight: 1, Status: "active", ProviderCode: "openai", GroupEnabled: 1},
			{ID: "b2", GroupID: "g2", Priority: 5, Weight: 2, Status: "disabled", ProviderCode: "openai", GroupEnabled: 0},
		},
	}
	row := projectAPIKeyRowForRouting(record)
	if row.ID != "key_1" || row.SystemAccountID != "sys_owner" || row.RouteStrategyID != "rs_1" ||
		row.RouteStrategyMode != "normal" || row.SelectedGroupID != "group_main" || row.Status != "active" {
		t.Fatalf("投影行 = %+v, want 身份列原样", row)
	}
	if row.RouteStrategyConfigJSON != `{"k":1}` {
		t.Errorf("RouteStrategyConfigJSON = %q, want 原样 JSON", row.RouteStrategyConfigJSON)
	}
	if len(row.GroupBindings) != 2 {
		t.Fatalf("绑定数 = %d, want 2", len(row.GroupBindings))
	}
	first := row.GroupBindings[0]
	if first.ID != "b1" || first.GroupID != "g1" || first.Status != "active" || first.ProviderCode != "openai" {
		t.Errorf("绑定一 = %+v, want 身份列原样", first)
	}
	if first.Priority != 3 || first.Weight == nil || *first.Weight != 1 || first.GroupEnabled != 1 {
		t.Errorf("绑定一数值列 = priority %d weight %v enabled %d, want 3/1/1",
			first.Priority, first.Weight, first.GroupEnabled)
	}
	second := row.GroupBindings[1]
	if second.Priority != 5 || second.Weight == nil || *second.Weight != 2 || second.GroupEnabled != 0 {
		t.Errorf("绑定二数值列 = priority %d weight %v enabled %d, want 5/2/0",
			second.Priority, second.Weight, second.GroupEnabled)
	}
	// 无配置指针：配置列坍缩为空字符串。
	noConfig := *record
	noConfig.RouteStrategyConfigJSON = nil
	noConfig.GroupBindings = nil
	row = projectAPIKeyRowForRouting(&noConfig)
	if row.RouteStrategyConfigJSON != "" || len(row.GroupBindings) != 0 {
		t.Errorf("无配置投影 = %q/%v, want 空/空", row.RouteStrategyConfigJSON, row.GroupBindings)
	}
}

// TestW1RProjectBindingsForRuntime 覆盖等长拷贝、按 ID 回查与未知 ID 合成。
func TestW1RProjectBindingsForRuntime(t *testing.T) {
	original := []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{ID: "b1", APIKeyID: "key_1", SystemAccountID: "sys_owner", GroupID: "g1", Priority: 3, Weight: 1, Status: "active", ProviderCode: "openai", GroupEnabled: 1},
		{ID: "b2", GroupID: "g2", Priority: 5, Weight: 2, Status: "disabled", GroupEnabled: 0},
	}
	// 等长：原样返回运行时行（选择器未过滤）。
	sameLength := projectBindingsForRuntime(projectAPIKeyRowForRouting(&gatewayruntimecache.GatewayAPIKeyRow{GroupBindings: original}).GroupBindings, original)
	if !reflect.DeepEqual(sameLength, original) {
		t.Fatalf("等长投影 = %+v, want 原行拷贝", sameLength)
	}
	// 过滤后：按 ID 回查命中原始行。
	filtered := projectBindingsForRuntime(
		[]gatewayrouting.GroupBindingRow{
			{ID: "b2", APIKeyID: "key_1", SystemAccountID: "sys_owner", GroupID: "g2", Priority: 5, Weight: strPtrOf_int64(2), Status: "disabled", ProviderCode: "openai", GroupEnabled: 0},
		}, original)
	if len(filtered) != 1 || !reflect.DeepEqual(filtered[0], original[1]) {
		t.Fatalf("过滤投影 = %+v, want 原始 b2 行", filtered)
	}
	// 未知 ID：从投影列合成运行时行（weight 指针不保留）。
	synthesized := projectBindingsForRuntime(
		[]gatewayrouting.GroupBindingRow{
			{ID: "bx", APIKeyID: "key_x", SystemAccountID: "sys_x", GroupID: "gx", Priority: 7, Weight: strPtrOf_int64(9), Status: "active", ProviderCode: "openai", GroupEnabled: 1},
		}, original)
	if len(synthesized) != 1 {
		t.Fatalf("合成投影长度 = %d, want 1", len(synthesized))
	}
	row := synthesized[0]
	if row.ID != "bx" || row.APIKeyID != "key_x" || row.SystemAccountID != "sys_x" || row.GroupID != "gx" ||
		row.Priority != 7 || row.Status != "active" || row.ProviderCode != "openai" || row.GroupEnabled != 1 {
		t.Fatalf("合成行 = %+v, want 投影列回填", row)
	}
	if row.Weight != 0 {
		t.Errorf("合成行 Weight = %d, want 0（合成路径不携带 weight）", row.Weight)
	}
	// 空投影：返回空切片。
	empty := projectBindingsForRuntime(nil, original)
	if len(empty) != 0 {
		t.Fatalf("空投影 = %+v, want 空", empty)
	}
}

// strPtrOf_int64 是 GroupBindingRow.Weight 的指针构造（w1r 前缀别名）。
func strPtrOf_int64(value int64) *int64 { return &value }

// ---------------------------------------------------------------------------
// 账户回填 / 小工具（覆盖清单 7、8、9）
// ---------------------------------------------------------------------------

// TestW1RAccountFromRoutingProjection 覆盖最小可调度凭据重建：身份列回填、
// 模型列表拷贝隔离、凭据列为空。
func TestW1RAccountFromRoutingProjection(t *testing.T) {
	projection := gatewayrouting.UpstreamAccount{
		ID:                        "acc_missing",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "prof_2",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		SupportedModels:           []string{"gpt-pro"},
	}
	rebuilt := accountFromRoutingProjection(projection)
	if rebuilt.ID != "acc_missing" || rebuilt.ProviderCode != "openai" || rebuilt.ProviderProtocolProfileID != "prof_2" ||
		rebuilt.ProtocolCode != "openai" || rebuilt.ProtocolVersion != "v1" {
		t.Fatalf("重建账户 = %+v, want 身份列回填", rebuilt)
	}
	if len(rebuilt.SupportedModels) != 1 || rebuilt.SupportedModels[0] != "gpt-pro" {
		t.Fatalf("SupportedModels = %v, want [gpt-pro]", rebuilt.SupportedModels)
	}
	rebuilt.SupportedModels[0] = "mutated"
	if projection.SupportedModels[0] != "gpt-pro" {
		t.Fatalf("重建修改穿透到投影: %v", projection.SupportedModels)
	}
	if rebuilt.APIKey != "" || rebuilt.Credentials != nil {
		t.Errorf("重建账户凭据 = %q/%v, want 空（凭据只来自缓存回源）", rebuilt.APIKey, rebuilt.Credentials)
	}
}

// TestW1RDerefStringPtrRenderSchedulingPolicy 覆盖 deref / stringPtr /
// renderSchedulingPolicy 的 nil、空值、非法值与正常路径。
func TestW1RDerefStringPtrRenderSchedulingPolicy(t *testing.T) {
	if got := deref(nil); got != "" {
		t.Errorf("deref(nil) = %q, want 空字符串", got)
	}
	if got := deref(strPtrOf("personal")); got != "personal" {
		t.Errorf("deref(指针) = %q, want personal", got)
	}
	if got := stringPtr(""); got != nil {
		t.Errorf("stringPtr(\"\") = %v, want nil", got)
	}
	if got := stringPtr("   "); got != nil {
		t.Errorf("stringPtr(空白) = %v, want nil", got)
	}
	if got := stringPtr("team_1"); got == nil || *got != "team_1" {
		t.Errorf("stringPtr(team_1) = %v, want 指针 team_1", got)
	}
	if got := renderSchedulingPolicy(nil); got != "" {
		t.Errorf("renderSchedulingPolicy(nil) = %q, want 空字符串", got)
	}
	policy := gatewayruntimecache.GroupSchedulingPolicy{"schedulingPreference": "quality"}
	if got := renderSchedulingPolicy(&policy); got != `{"schedulingPreference":"quality"}` {
		t.Errorf("renderSchedulingPolicy = %q, want 原样 JSON", got)
	}
	// 序列化失败路径：不可 JSON 化的值渲染为空字符串。
	unserializable := gatewayruntimecache.GroupSchedulingPolicy{"bad": func() {}}
	if got := renderSchedulingPolicy(&unserializable); got != "" {
		t.Errorf("不可序列化策略 = %q, want 空字符串", got)
	}
}

// TestW1RLocalEndpointFamily 表驱动覆盖端点族推导：OpenAI / Anthropic /
// Gemini 各族、查询串剥离、v1 前缀剥离、无斜杠补齐与方法守卫。
func TestW1RLocalEndpointFamily(t *testing.T) {
	cases := []struct {
		name string
		view gatewayrouting.RequestView
		want string
	}{
		{"chat completions 带查询串", gatewayrouting.RequestView{OriginalURL: "/v1/chat/completions?api=1"}, "chat_completions"},
		{"responses 走 Path 兜底", gatewayrouting.RequestView{Path: "/v1/responses"}, "responses"},
		{"chat completions 大小写不敏感", gatewayrouting.RequestView{OriginalURL: "/V1/Chat/Completions"}, "chat_completions"},
		{"anthropic messages POST", gatewayrouting.RequestView{Method: "POST", Path: "/v1/messages"}, "messages"},
		{"anthropic messages 方法小写", gatewayrouting.RequestView{Method: "post", OriginalURL: "/v1/messages"}, "messages"},
		{"anthropic messages GET 拒绝", gatewayrouting.RequestView{Method: "GET", Path: "/v1/messages"}, ""},
		{"gemini generateContent", gatewayrouting.RequestView{Method: "POST", OriginalURL: "/v1beta/models/gemini-pro:generateContent?x=1"}, "generate_content"},
		{"gemini streamGenerateContent", gatewayrouting.RequestView{Method: "POST", Path: "/v1beta/models/m:streamGenerateContent"}, "stream_generate_content"},
		{"gemini countTokens", gatewayrouting.RequestView{Method: "POST", Path: "/v1beta/models/m:countTokens"}, "count_tokens"},
		{"gemini embedContent", gatewayrouting.RequestView{Method: "POST", Path: "/v1beta/models/m:embedContent"}, "embed_content"},
		{"gemini 裸 models 路径拒绝", gatewayrouting.RequestView{Method: "POST", Path: "/v1beta/models"}, ""},
		{"无前导斜杠补齐", gatewayrouting.RequestView{OriginalURL: "chat/completions"}, "chat_completions"},
		{"非 v1 前缀的 messages 拒绝", gatewayrouting.RequestView{Method: "POST", Path: "/v2/messages"}, ""},
		{"未知路径为空", gatewayrouting.RequestView{Method: "POST", Path: "/v1/whatever"}, ""},
	}
	for _, item := range cases {
		if got := localEndpointFamily(item.view); got != item.want {
			t.Errorf("%s: localEndpointFamily = %q, want %q", item.name, got, item.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 请求视图投影（覆盖清单 10）
// ---------------------------------------------------------------------------

// TestW1RRoutingRequestView 覆盖 nil 请求与 method/originalUrl/path/body
// model 的投影。
func TestW1RRoutingRequestView(t *testing.T) {
	view := routingRequestView(nil, nil)
	if view.Method != "" || view.OriginalURL != "" || view.Path != "" || view.BodyModel != "" {
		t.Fatalf("nil 请求视图 = %+v, want 零值", view)
	}
	rawRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?api=1", nil)
	request := gatewaypreauth.NewGatewayRequest(rawRequest)
	request.Body = w1rGatewayBodyRequest(`{"model":"gpt-test"}`, nil,
		&gatewaybody.BodyState{Model: strPtrOf("gpt-test"), ContentType: "application/json"})
	view = routingRequestView(request, &gatewayruntimecache.GatewayAPIKeyRow{ID: "key_1"})
	if view.Method != "POST" {
		t.Errorf("Method = %q, want POST", view.Method)
	}
	if view.OriginalURL != "/v1/chat/completions?api=1" {
		t.Errorf("OriginalURL = %q, want /v1/chat/completions?api=1", view.OriginalURL)
	}
	if view.Path != "/v1/chat/completions" {
		t.Errorf("Path = %q, want /v1/chat/completions", view.Path)
	}
	if view.BodyModel != "gpt-test" {
		t.Errorf("BodyModel = %q, want gpt-test", view.BodyModel)
	}
	// 无 body 状态：BodyModel 为空。
	bare := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	view = routingRequestView(bare, nil)
	if view.Method != "GET" || view.BodyModel != "" || view.Path != "/v1/models" {
		t.Errorf("无 body 视图 = %+v, want GET /v1/models 空模型", view)
	}
}

// ---------------------------------------------------------------------------
// chainRoutingCache 桥接 + rehydrate 回源（覆盖清单 11、12）
// ---------------------------------------------------------------------------

// TestW1RChainRoutingCacheBridgesRuntimeCache 用真 Service 覆盖三个桥接
// 方法：分组访问投影、分组账户投影与供应商模型路由转译。
func TestW1RChainRoutingCacheBridgesRuntimeCache(t *testing.T) {
	fixture := newChainFixture(t)
	bridge := chainRoutingCache{cache: fixture.cache}
	ctx := context.Background()

	meta, found, err := bridge.ResolveCachedGroupUsageAccessMetadataAsync(ctx, fixture.groupID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("分组访问读取: %v", err)
	}
	if !found {
		t.Fatal("fixture 分组必须命中")
	}
	if meta.GroupOwnerSystemAccountID != fixture.systemAccount {
		t.Errorf("GroupOwnerSystemAccountID = %q, want %q", meta.GroupOwnerSystemAccountID, fixture.systemAccount)
	}
	if meta.GroupType != "personal" {
		t.Errorf("GroupType = %q, want personal（指针坍缩）", meta.GroupType)
	}
	if meta.ProviderCode != "openai" || meta.GroupAccessType != "owner" {
		t.Errorf("ProviderCode/GroupAccessType = %q/%q, want openai/owner", meta.ProviderCode, meta.GroupAccessType)
	}
	if meta.SchedulingPolicy != "" {
		t.Errorf("SchedulingPolicy = %q, want 空（fixture 无策略）", meta.SchedulingPolicy)
	}

	missingMeta, missingFound, err := bridge.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "group_missing", fixture.systemAccount)
	if err != nil {
		t.Fatalf("缺失分组访问读取: %v", err)
	}
	if missingFound || missingMeta != (gatewayrouting.GroupUsageAccessMetadata{}) {
		t.Errorf("缺失分组 = found %v meta %+v, want false 零值", missingFound, missingMeta)
	}

	accounts, err := bridge.ListCachedOpenAIAccountsForGroupAsync(ctx, fixture.groupID, fixture.systemAccount,
		gatewayrouting.CachedAccountsForGroupOptions{RequestedModel: "gpt-test"})
	if err != nil {
		t.Fatalf("分组账户读取: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("分组账户数 = %d, want 1", len(accounts))
	}
	if accounts[0].ID != fixture.accountID || accounts[0].ProviderCode != "openai" || accounts[0].ProtocolCode != "openai" {
		t.Errorf("投影账户 = %+v, want acc_1/openai/openai", accounts[0])
	}
	if len(accounts[0].SupportedModels) != 1 || accounts[0].SupportedModels[0] != "gpt-test" {
		t.Errorf("投影模型 = %v, want [gpt-test]", accounts[0].SupportedModels)
	}

	route, err := bridge.ResolveCachedProviderModelRouteAsync(ctx, gatewayrouting.ProviderModelRouteInput{
		Model: "gpt-test", ProviderCodes: []string{"openai"}, SystemAccountID: fixture.systemAccount, IncludeUnpriced: true,
	})
	if err != nil {
		t.Fatalf("模型路由读取: %v", err)
	}
	if route.Outcome != "matched" || route.ProviderCode != "openai" || route.ModelKey != "gpt-test" {
		t.Errorf("模型路由 = %+v, want matched openai gpt-test", route)
	}
	if len(route.MatchedProviderCodes) != 1 || route.MatchedProviderCodes[0] != "openai" {
		t.Errorf("MatchedProviderCodes = %v, want [openai]", route.MatchedProviderCodes)
	}
	missingRoute, err := bridge.ResolveCachedProviderModelRouteAsync(ctx, gatewayrouting.ProviderModelRouteInput{
		Model: "unknown-model", ProviderCodes: []string{"openai"}, SystemAccountID: fixture.systemAccount, IncludeUnpriced: true,
	})
	if err != nil {
		t.Fatalf("缺失模型路由读取: %v", err)
	}
	if missingRoute.Outcome != "missing" || missingRoute.ProviderCode != "" {
		t.Errorf("缺失模型路由 = %+v, want missing 无 provider", missingRoute)
	}
}

// TestW1RRehydrateGroupAccess 覆盖三分支：空 groupID 直接投影 fallback、
// 命中缓存返回原始元数据、缓存未命中回退投影。
func TestW1RRehydrateGroupAccess(t *testing.T) {
	fixture := newChainFixture(t)
	resolver := &chainRouteResolver{cache: fixture.cache}
	ctx := context.Background()
	fallback := gatewayrouting.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: "sys_fb",
		ProviderCode:              "openai",
		GroupAccessType:           "owner",
		SchedulingPolicy:          `{"schedulingPreference":"quality"}`,
		GroupAuthorizationID:      "authz_fb",
	}

	// 空 groupID：不查缓存，直接投影 fallback。
	projected := resolver.rehydrateGroupAccess(ctx, "", fixture.systemAccount, fallback)
	if projected.GroupOwnerSystemAccountID != "sys_fb" || projected.GroupAccessType != "owner" {
		t.Errorf("空 groupID 投影 = %+v, want sys_fb/owner", projected)
	}
	if projected.GroupAuthorizationID == nil || *projected.GroupAuthorizationID != "authz_fb" {
		t.Errorf("空 groupID 授权 = %v, want 指针 authz_fb", projected.GroupAuthorizationID)
	}
	if projected.SchedulingPolicy == nil || (*projected.SchedulingPolicy)["schedulingPreference"] != "quality" {
		t.Errorf("空 groupID 策略 = %v, want quality", projected.SchedulingPolicy)
	}

	// 命中缓存：返回缓存原始元数据（指针形态，含 personal 类型）。
	cached := resolver.rehydrateGroupAccess(ctx, fixture.groupID, fixture.systemAccount, fallback)
	if cached.GroupOwnerSystemAccountID != fixture.systemAccount {
		t.Errorf("缓存元数据 owner = %q, want %q", cached.GroupOwnerSystemAccountID, fixture.systemAccount)
	}
	if cached.GroupType == nil || *cached.GroupType != "personal" {
		t.Errorf("缓存 GroupType = %v, want personal", cached.GroupType)
	}
	if cached.GroupAuthorizationID != nil {
		t.Errorf("缓存授权 ID = %v, want nil（fixture 无授权）", cached.GroupAuthorizationID)
	}

	// 缓存未命中：回退 fallback 投影。
	missed := resolver.rehydrateGroupAccess(ctx, "group_missing", fixture.systemAccount, fallback)
	if missed.GroupOwnerSystemAccountID != "sys_fb" || missed.SchedulingPolicy == nil {
		t.Errorf("未命中回退 = %+v, want fallback 投影", missed)
	}
}

// TestW1RRehydrateAccounts 覆盖全量回源：命中账户带凭据、未命中账户回退
// 投影、nil cache 全部投影回退，routeSource/matchedProviderCode 原样透传。
func TestW1RRehydrateAccounts(t *testing.T) {
	fixture := newChainFixture(t)
	resolver := &chainRouteResolver{cache: fixture.cache}
	ctx := context.Background()
	projected := []gatewayrouting.UpstreamAccount{
		{ID: fixture.accountID, ProviderCode: "openai", ProtocolCode: "openai"},
		{ID: "acc_unknown", ProviderCode: "openai", ProtocolCode: "openai", SupportedModels: []string{"gpt-pro"}},
	}
	accounts, routeSource, matched := resolver.rehydrateAccounts(ctx, fixture.groupID, fixture.systemAccount,
		projected, "gpt-test", "chat_completions", "catalog_provider", "openai")
	if routeSource != "catalog_provider" || matched != "openai" {
		t.Fatalf("routeSource/matched = %q/%q, want catalog_provider/openai", routeSource, matched)
	}
	if len(accounts) != 2 {
		t.Fatalf("账户数 = %d, want 2", len(accounts))
	}
	if accounts[0].ID != fixture.accountID {
		t.Errorf("账户一 ID = %q, want %q", accounts[0].ID, fixture.accountID)
	}
	if accounts[0].APIKey != "sk-upstream-account-key" {
		t.Errorf("账户一凭据 = %q, want 缓存回源完整凭据", accounts[0].APIKey)
	}
	if accounts[1].ID != "acc_unknown" || accounts[1].APIKey != "" || accounts[1].ProviderCode != "openai" {
		t.Errorf("账户二 = %+v, want 投影回退（无凭据）", accounts[1])
	}

	// nil cache：所有投影走 accountFromRoutingProjection。
	bare := &chainRouteResolver{}
	accounts, routeSource, matched = bare.rehydrateAccounts(ctx, fixture.groupID, fixture.systemAccount,
		projected[:1], "gpt-test", "", "account_mapping", "openai")
	if len(accounts) != 1 || accounts[0].ID != fixture.accountID || accounts[0].APIKey != "" {
		t.Errorf("nil cache 回源 = %+v, want 投影回退", accounts)
	}
	if routeSource != "account_mapping" || matched != "openai" {
		t.Errorf("nil cache 透传 = %q/%q, want account_mapping/openai", routeSource, matched)
	}
}

// TestW1RListFullAccountsGuards 覆盖 listFullAccounts 的守卫分支：空
// groupID 与 nil cache 返回 nil，缓存未命中的缺失分组返回空结果。
func TestW1RListFullAccountsGuards(t *testing.T) {
	fixture := newChainFixture(t)
	resolver := &chainRouteResolver{cache: fixture.cache}
	ctx := context.Background()
	if got := resolver.listFullAccounts(ctx, "", fixture.systemAccount, "gpt-test", ""); got != nil {
		t.Errorf("空 groupID = %v, want nil", got)
	}
	bare := &chainRouteResolver{}
	if got := bare.listFullAccounts(ctx, fixture.groupID, fixture.systemAccount, "gpt-test", ""); got != nil {
		t.Errorf("nil cache = %v, want nil", got)
	}
	// 缺失分组不报错：选择器返回空切片（无可调度账户）。
	if got := resolver.listFullAccounts(ctx, "group_missing", fixture.systemAccount, "gpt-test", ""); len(got) != 0 {
		t.Errorf("缺失分组 = %v, want 空", got)
	}
	full := resolver.listFullAccounts(ctx, fixture.groupID, fixture.systemAccount, "gpt-test", "chat_completions")
	if len(full) != 1 || full[0].ID != fixture.accountID || full[0].APIKey != "sk-upstream-account-key" {
		t.Errorf("命中分组 = %+v, want acc_1 完整凭据", full)
	}
}

// ---------------------------------------------------------------------------
// chain_openaicompat（覆盖清单 13）
// ---------------------------------------------------------------------------

// TestW1RErrChainCompat 覆盖错误构造：消息原文与专用错误类型。
func TestW1RErrChainCompat(t *testing.T) {
	err := errChainCompat("openai-compatible 组合缺少业务数据库句柄")
	if err.Error() != "openai-compatible 组合缺少业务数据库句柄" {
		t.Errorf("err = %q, want 消息原文", err.Error())
	}
	if _, ok := err.(*chainCompatError); !ok {
		t.Errorf("错误类型 = %T, want *chainCompatError", err)
	}
}

// TestW1RBearerTokenOf 表驱动覆盖 Authorization bearer 提取：大小写、
// 空白容错与非 bearer 方案拒绝。
func TestW1RBearerTokenOf(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"标准 Bearer", "Bearer sk-abc", "sk-abc"},
		{"小写 bearer", "bearer sk-abc", "sk-abc"},
		{"大写 BEARER", "BEARER sk-abc", "sk-abc"},
		{"值带空白被修剪", "Bearer   sk-abc  ", "sk-abc"},
		{"头空白被修剪", " Bearer sk-abc", "sk-abc"},
		{"空头", "", ""},
		{"非 bearer 方案", "Token sk-abc", ""},
		{"无空格分隔", "Bearersk-abc", ""},
		{"仅方案", "Bearer ", ""},
	}
	for _, item := range cases {
		request := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
		if item.header != "" {
			request.Header.Set("Authorization", item.header)
		}
		if got := bearerTokenOf(request); got != item.want {
			t.Errorf("%s: bearerTokenOf = %q, want %q", item.name, got, item.want)
		}
	}
}

// TestW1RChainCompatScopeResolver 用真 fixture Service 覆盖 scope 解析：
// nil cache/nil 请求守卫、无凭据/非 bearer/未知 key 返回 nil、合法 key 返回
// 网关范围。
func TestW1RChainCompatScopeResolver(t *testing.T) {
	fixture := newChainFixture(t)
	resolve := chainCompatScopeResolver(fixture.cache)

	if resolve(nil) != nil {
		t.Error("nil 请求必须返回 nil scope")
	}
	if chainCompatScopeResolver(nil)(httptest.NewRequest(http.MethodPost, "/v1/files", nil)) != nil {
		t.Error("nil cache 必须返回 nil scope")
	}

	noHeader := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	if resolve(noHeader) != nil {
		t.Error("无 Authorization 必须返回 nil scope")
	}
	nonBearer := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	nonBearer.Header.Set("Authorization", "Token sk-abc")
	if resolve(nonBearer) != nil {
		t.Error("非 bearer 方案必须返回 nil scope")
	}
	unknown := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	unknown.Header.Set("Authorization", "Bearer sk-unknown-key")
	if resolve(unknown) != nil {
		t.Error("未知 key 必须返回 nil scope")
	}

	valid := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	valid.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	scope := resolve(valid)
	if scope == nil {
		t.Fatal("合法 key 必须返回 scope")
	}
	if scope.SystemAccountID != fixture.systemAccount {
		t.Errorf("SystemAccountID = %q, want %q", scope.SystemAccountID, fixture.systemAccount)
	}
	if scope.APIKeyID != "key_1" {
		t.Errorf("APIKeyID = %q, want key_1", scope.APIKeyID)
	}
}

// w1rAuditMetadataEntry 记录一次 AddGatewayMetadata 调用。
type w1rAuditMetadataEntry struct {
	label    string
	metadata map[string]any
}

// w1rAuditCapture 是 gatewaypreauth.AuditCaptureContext 的记录用 fake。
type w1rAuditCapture struct {
	boundContext gatewaypreauth.AuditGatewayContext
	entries      []w1rAuditMetadataEntry
	finalized    int
}

func (c *w1rAuditCapture) BindContext(ctx gatewaypreauth.AuditGatewayContext) { c.boundContext = ctx }

func (c *w1rAuditCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	c.entries = append(c.entries, w1rAuditMetadataEntry{label: label, metadata: metadata})
}

func (c *w1rAuditCapture) Finalize(input gatewaypreauth.AuditFinalizeInput) { c.finalized++ }
