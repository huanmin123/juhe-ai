package main

// w1: chain_routing.go 的路由投影与 rehydrate 适配层单测。localEndpointFamily
// 的全家族分支、请求视图投影、hybrid 请求体桥与 key 行投影全部进程内断言。

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func TestW1ProjectGroupAccessForRoutingAndRuntime(t *testing.T) {
	groupType := "high_concurrency"
	authzID := "authz_1"
	meta := gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      "sys_owner",
		ProviderCode:                   "openai",
		GroupAccessType:                "shared",
		GroupType:                      &groupType,
		SchedulingPolicy:               &gatewayruntimecache.GroupSchedulingPolicy{"schedulingPreference": "speed_first"},
		GroupAuthorizationID:           &authzID,
		GroupAuthorizationQuotaLimited: boolPtr(true),
	}
	projected := projectGroupAccessForRouting(meta)
	if projected.GroupOwnerSystemAccountID != "sys_owner" || projected.ProviderCode != "openai" || projected.GroupType != "high_concurrency" {
		t.Fatalf("projection = %+v", projected)
	}
	if !strings.Contains(projected.SchedulingPolicy, "speed_first") {
		t.Fatalf("scheduling policy = %q", projected.SchedulingPolicy)
	}
	if projected.GroupAuthorizationID != "authz_1" {
		t.Fatalf("authz id = %q", projected.GroupAuthorizationID)
	}
	// 反向投影：identity 字段、空值 nil 指针、policy JSON 往返。
	back := projectGroupAccessForRuntime(projected)
	if back.GroupType == nil || *back.GroupType != "high_concurrency" {
		t.Fatalf("runtime group type = %v", back.GroupType)
	}
	if back.SchedulingPolicy == nil {
		t.Fatal("runtime scheduling policy 缺失")
	}
	rendered := renderSchedulingPolicy(back.SchedulingPolicy)
	if !strings.Contains(rendered, "speed_first") {
		t.Fatalf("policy 往返 = %q", rendered)
	}
	// 非法 policy JSON 回落为 schedulingPreference 单键。
	broken := projected
	broken.SchedulingPolicy = "{not-json"
	back = projectGroupAccessForRuntime(broken)
	if back.SchedulingPolicy == nil {
		t.Fatal("非法 policy 必须回落单键")
	}
	if (*back.SchedulingPolicy)["schedulingPreference"] != "{not-json" {
		t.Fatalf("回落 policy = %+v", *back.SchedulingPolicy)
	}
	// 空 scheduling policy：nil。
	empty := projectGroupAccessForRuntime(gatewayrouting.GroupUsageAccessMetadata{GroupType: ""})
	if empty.SchedulingPolicy != nil || empty.GroupType != nil {
		t.Fatalf("空投影 = %+v", empty)
	}
	// nil 指针的 deref 投影。
	bare := projectGroupAccessForRouting(gatewayruntimecache.GroupUsageAccessMetadata{})
	if bare.GroupType != "" || bare.SchedulingPolicy != "" || bare.GroupAuthorizationID != "" {
		t.Fatalf("nil 指针投影 = %+v", bare)
	}
}

func TestW1LocalEndpointFamily(t *testing.T) {
	cases := []struct {
		name   string
		method string
		url    string
		want   string
	}{
		{"chat", "POST", "/v1/chat/completions", gatewayrouting.EndpointFamilyChatCompletions},
		{"chat 带 query", "POST", "/v1/chat/completions?x=1", gatewayrouting.EndpointFamilyChatCompletions},
		{"responses", "POST", "/v1/responses", gatewayrouting.EndpointFamilyResponses},
		{"messages", "POST", "/v1/messages", gatewayrouting.EndpointFamilyMessages},
		{"generateContent", "POST", "/v1beta/models/gemini:generateContent", gatewayrouting.EndpointFamilyGenerateContent},
		{"streamGenerateContent", "POST", "/v1beta/models/gemini:streamGenerateContent", gatewayrouting.EndpointFamilyStreamGenerate},
		{"countTokens", "POST", "/v1beta/models/gemini:countTokens", gatewayrouting.EndpointFamilyCountTokens},
		{"embedContent", "POST", "/v1beta/models/gemini:embedContent", gatewayrouting.EndpointFamilyEmbedContent},
		{"gemini models 列表", "GET", "/v1beta/models", ""},
		{"未识别", "POST", "/v1/other", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			view := gatewayrouting.RequestView{Method: testCase.method, OriginalURL: testCase.url}
			if got := localEndpointFamily(view); got != testCase.want {
				t.Fatalf("family = %q，want %q", got, testCase.want)
			}
		})
	}
	// OriginalURL 为空时回落 Path。
	view := gatewayrouting.RequestView{Method: "POST", Path: "/v1/chat/completions"}
	if got := localEndpointFamily(view); got != gatewayrouting.EndpointFamilyChatCompletions {
		t.Fatalf("path 回落 = %q", got)
	}
	// 非 POST 的 messages 不识别。
	view = gatewayrouting.RequestView{Method: "GET", OriginalURL: "/v1/messages"}
	if got := localEndpointFamily(view); got != "" {
		t.Fatalf("GET messages = %q", got)
	}
	// 非斜杠前缀输入补齐。
	view = gatewayrouting.RequestView{Method: "POST", OriginalURL: "v1/chat/completions"}
	if got := localEndpointFamily(view); got != gatewayrouting.EndpointFamilyChatCompletions {
		t.Fatalf("无斜杠 = %q", got)
	}
}

func TestW1HybridRequestBodyBridge(t *testing.T) {
	// nil request / nil body：全部安全返回。
	nilBridge := hybridRequestBody{}
	if nilBridge.ReplaceModel("m") || nilBridge.HasRawBody() {
		t.Fatal("nil bridge 必须返回 false")
	}
	if _, err := nilBridge.ParseRawBody(context.Background()); err == nil {
		t.Fatal("空 body 解析必须报错")
	}
	if nilBridge.ReplaceModelWithParsed("m", nil) {
		t.Fatal("nil parsed 必须返回 false")
	}
	// 有 body：替换 model 并保持 raw body 一致。
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a","messages":[]}`)))
	request.Body = &gatewaybody.Request{RawBody: []byte(`{"model":"a","messages":[]}`), Body: map[string]any{"model": "a", "messages": []any{}}, ContentTypeHeader: "application/json"}
	bridge := hybridRequestBody{request: request}
	if !bridge.HasRawBody() {
		t.Fatal("HasRawBody 必须为真")
	}
	if !bridge.ReplaceModel("b") {
		t.Fatal("ReplaceModel 必须成功")
	}
	if !strings.Contains(string(request.Body.RawBody), `"b"`) {
		t.Fatalf("替换后 raw body = %s", request.Body.RawBody)
	}
	parsedAny, err := bridge.ParseRawBody(context.Background())
	if err != nil {
		t.Fatalf("ParseRawBody: %v", err)
	}
	parsed, ok := parsedAny.(*gatewayhybrid.OrderedJSON)
	if !ok {
		t.Fatalf("ParseRawBody 类型 = %T", parsedAny)
	}
	// ReplaceModelWithParsed 走 ordered map 序列化路径。
	if !bridge.ReplaceModelWithParsed("c", parsed) {
		t.Fatal("ReplaceModelWithParsed 必须成功")
	}
	if !strings.Contains(string(request.Body.RawBody), `"c"`) {
		t.Fatalf("ordered 替换后 = %s", request.Body.RawBody)
	}
	// orderedJSONObjectMap / orderedValueToPlain：嵌套对象 + 数组 + 标量。
	nested := gatewayhybrid.NewOrderedJSON()
	nested.Set("model", "a")
	child := gatewayhybrid.NewOrderedJSON()
	child.Set("k", "v")
	nested.Set("child", child)
	nested.Set("list", []any{int64(1), "x", child})
	plain := orderedJSONObjectMap(nested)
	if plain["model"] != "a" {
		t.Fatalf("plain model = %v", plain["model"])
	}
	childMap, ok := plain["child"].(map[string]any)
	if !ok || childMap["k"] != "v" {
		t.Fatalf("child = %#v", plain["child"])
	}
	list, ok := plain["list"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("list = %#v", plain["list"])
	}
	// nil ordered object → 空 map。
	if got := orderedJSONObjectMap(nil); len(got) != 0 {
		t.Fatalf("nil object = %v", got)
	}
	orderedValueToPlain([]any{"deep"})
}

func TestW1HybridAuditMetadataAndViews(t *testing.T) {
	// nil capture / nil metadata 安全。
	(hybridAuditMetadata{}).AddGatewayMetadata("label", nil)
	// 有 capture：metadata 落盘。
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	_ = request
	if hybridTargetModelHint(nil) != "" {
		t.Fatal("nil req 的 model hint 必须为空")
	}
	view := hybridRequestView(nil)
	if view.Method != "" || view.Path != "" {
		t.Fatalf("nil req view = %+v", view)
	}
	ids := hybridAccountIDs([]gatewayhybrid.OpenAIAccountSecret{{ID: "a"}, {ID: "b"}})
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v", ids)
	}
	if boolPtr(true) == nil || !*boolPtr(true) {
		t.Fatal("boolPtr 错误")
	}
}

func TestW1ProjectAPIKeyRowProjections(t *testing.T) {
	if projectAPIKeyRowForRouting(nil) != nil {
		t.Fatal("nil record 必须投影 nil")
	}
	configJSON := `{"priority":1}`
	record := &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                      "key_1",
		SystemAccountID:         "sys_1",
		RouteStrategyID:         "rs_1",
		RouteStrategyMode:       "normal",
		RouteStrategyConfigJSON: &configJSON,
		SelectedGroupID:         "grp_1",
		Status:                  "active",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "rsg_1", APIKeyID: "key_1", SystemAccountID: "sys_1", GroupID: "grp_1",
				Priority: 2, Weight: 3, Status: "active", ProviderCode: "openai", GroupEnabled: 1},
			{ID: "rsg_2", APIKeyID: "key_1", SystemAccountID: "sys_1", GroupID: "grp_2",
				Priority: 4, Weight: 1, Status: "active", ProviderCode: "openai", GroupEnabled: 1},
		},
	}
	projected := projectAPIKeyRowForRouting(record)
	if projected.ID != "key_1" || projected.RouteStrategyConfigJSON != configJSON || len(projected.GroupBindings) != 2 {
		t.Fatalf("projected = %+v", projected)
	}
	if projected.GroupBindings[0].Weight == nil || *projected.GroupBindings[0].Weight != 3 {
		t.Fatalf("weight = %v", projected.GroupBindings[0].Weight)
	}
	// 等长路径：原样返回副本。
	same := projectBindingsForRuntime(projected.GroupBindings, record.GroupBindings)
	if len(same) != 2 || same[0] != record.GroupBindings[0] {
		t.Fatalf("等长恢复 = %+v", same)
	}
	// 短投影按 ID 恢复原始行（第一条保留原值，未知 ID 构造 runtime 行）。
	extra := []gatewayrouting.GroupBindingRow{
		{ID: "rsg_new", APIKeyID: "key_1", SystemAccountID: "sys_1", GroupID: "grp_9",
			Priority: 5, Status: "active", ProviderCode: "openai", GroupEnabled: 0},
	}
	rebuilt := projectBindingsForRuntime(extra, record.GroupBindings)
	if len(rebuilt) != 1 {
		t.Fatalf("重建长度 = %d", len(rebuilt))
	}
	if rebuilt[0].ID != "rsg_new" || rebuilt[0].Priority != 5 || rebuilt[0].Weight != 0 {
		t.Fatalf("未知 ID 重建 = %+v", rebuilt[0])
	}
	matched := projectBindingsForRuntime([]gatewayrouting.GroupBindingRow{{ID: "rsg_1", Priority: 9}}, record.GroupBindings)
	if len(matched) != 1 || matched[0] != record.GroupBindings[0] {
		t.Fatalf("按 ID 恢复 = %+v", matched)
	}
}

func TestW1ProjectAPIKeyRowForHybridAndMaps(t *testing.T) {
	nilRecord, err := projectAPIKeyRowForHybrid(nil)
	if err != nil || nilRecord == nil || nilRecord.RouteStrategyMode != "" {
		t.Fatalf("nil record = %+v, %v", nilRecord, err)
	}
	plain, err := projectAPIKeyRowForHybrid(&gatewayruntimecache.GatewayAPIKeyRow{ID: "key_1", RouteStrategyMode: "hybrid"})
	if err != nil || plain == nil || plain.HybridRoutingConfig != nil {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	badRecord := &gatewayruntimecache.GatewayAPIKeyRow{ID: "key_2", HybridRoutingConfig: &gatewayruntimecache.ApiKeyHybridRoutingConfig{Raw: []byte("{not-json")}}
	if _, err := projectAPIKeyRowForHybrid(badRecord); err == nil {
		t.Fatal("非法 config 必须报错")
	}
	if got := hybridConfigToMap(nil); got != nil {
		t.Fatalf("nil config map = %v", got)
	}
	if hybridConfigToMap(&routestrategies.HybridRoutingConfig{}) == nil {
		t.Fatal("空 config map 不应为 nil")
	}
	if hybridScoringToMap(gatewayhybrid.HybridScoringResult{}) == nil {
		t.Fatal("scoring map 不应为 nil")
	}
	if hybridRouteToMap(routestrategies.HybridLevelRoute{}) == nil {
		t.Fatal("route map 不应为 nil")
	}
	account := accountFromRoutingProjection(gatewayrouting.UpstreamAccount{
		ID: "acc_1", ProviderCode: "openai", ProtocolCode: "openai",
		ProviderProtocolProfileID: "prof_1", ProtocolVersion: "v1", SupportedModels: []string{"gpt-test"},
	})
	if account.ID != "acc_1" || len(account.SupportedModels) != 1 {
		t.Fatalf("account = %+v", account)
	}
	if stringPtr("  ") != nil {
		t.Fatal("空白 stringPtr 必须 nil")
	}
	value := "x"
	if deref(nil) != "" || deref(&value) != "x" {
		t.Fatal("deref 错误")
	}
	if renderSchedulingPolicy(nil) != "" {
		t.Fatal("nil policy 渲染必须为空")
	}
	if !strings.Contains(renderSchedulingPolicy(&gatewayruntimecache.GroupSchedulingPolicy{"schedulingPreference": "latency"}), "latency") {
		t.Fatal("policy 渲染丢失")
	}
	result := (chainCapabilityFilter{}).FilterAccountsByRequestCapability(context.Background(),
		[]gatewayrouting.UpstreamAccount{{ID: "a"}}, gatewayrouting.CapabilityFilterInput{})
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "a" {
		t.Fatalf("capability filter = %+v", result)
	}
	full := projectAccountForRouting(gatewayruntimecache.OpenAIAccountSecret{ID: "acc_2", ProviderCode: "openai", SupportedModels: []string{"m"}})
	if full.ID != "acc_2" || len(full.SupportedModels) != 1 {
		t.Fatalf("full = %+v", full)
	}
}

func TestW1ChainRouteResolverGuards(t *testing.T) {
	resolver := &chainRouteResolver{}
	if _, err := resolver.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{}); err == nil {
		t.Fatal("nil normal 必须报错")
	}
	result, err := resolver.ResolveHybridGatewayRoute(context.Background(), gatewaypreauth.HybridRouteInput{})
	if err != nil || result.Outcome != gatewaypreauth.HybridRouteOutcomeSkipped {
		t.Fatalf("nil hybrid = %+v, %v", result, err)
	}
	empty := resolver.rehydrateGroupAccess(context.Background(), "", "sys_1", gatewayrouting.GroupUsageAccessMetadata{ProviderCode: "openai", GroupType: "personal"})
	if empty == nil || empty.ProviderCode != "openai" {
		t.Fatalf("empty group access = %+v", empty)
	}
	if got := resolver.listFullAccounts(context.Background(), "", "sys_1", "m", ""); got != nil {
		t.Fatalf("空 group 列表 = %v", got)
	}
	resolver.cache = nil
	accounts, routeSource, matched := resolver.rehydrateAccounts(context.Background(), "", "sys_1",
		[]gatewayrouting.UpstreamAccount{{ID: "acc_x", ProviderCode: "openai"}}, "m", "", "source", "matched")
	if len(accounts) != 1 || accounts[0].ID != "acc_x" || routeSource != "source" || matched != "matched" {
		t.Fatalf("rehydrate = %+v %q %q", accounts, routeSource, matched)
	}
	byID := resolver.rehydrateAccountsByID(context.Background(), "", "sys_1", []string{"acc_y"}, nil)
	if len(byID) != 1 || byID[0].ID != "acc_y" {
		t.Fatalf("rehydrateByID = %+v", byID)
	}
}

func TestW1RoutingRequestView(t *testing.T) {
	empty := routingRequestView(nil, nil)
	if empty.Method != "" || empty.Path != "" {
		t.Fatalf("nil view = %+v", empty)
	}
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions?q=1", nil))
	view := routingRequestView(request, nil)
	if view.Method != "POST" || view.OriginalURL != "/v1/chat/completions?q=1" || view.Path != "/v1/chat/completions" {
		t.Fatalf("view = %+v", view)
	}
	model := "gpt-test"
	stream := true
	request2 := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	request2.HTTP.Header.Set("content-type", "application/json")
	request2.HTTP.Header.Set("x-conversation-key", "conv_1")
	request2.Body = &gatewaybody.Request{
		RawBody: []byte(`{"model":"gpt-test"}`),
		Body:    map[string]any{"model": "gpt-test"},
		State: &gatewaybody.BodyState{
			Model: &model, Stream: &stream, ContentType: "application/json",
			ImageGeneration: true, ImageGenerationForced: true, StrictOutputRequirement: true,
		},
	}
	hybridView := hybridRequestView(request2)
	if hybridView.Method != "POST" || hybridView.ContentType != "application/json" || !hybridView.BodyAvailable {
		t.Fatalf("hybrid view = %+v", hybridView)
	}
	if hybridView.OriginalModel != "gpt-test" || !hybridView.OriginalModelPresent {
		t.Fatalf("model = %q present=%v", hybridView.OriginalModel, hybridView.OriginalModelPresent)
	}
	if hybridView.ConversationKey != "conv_1" {
		t.Fatalf("conversation key = %q", hybridView.ConversationKey)
	}
	if hybridView.BodyState == nil || hybridView.BodyState.Stream == nil || !*hybridView.BodyState.ImageGeneration {
		t.Fatalf("body state = %+v", hybridView.BodyState)
	}
	request3 := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	request3.Body = &gatewaybody.Request{RawBody: []byte("{}")}
	view3 := hybridRequestView(request3)
	if view3.BodyState != nil || view3.OriginalModelPresent {
		t.Fatalf("无 state view = %+v", view3)
	}
	if got := hybridTargetModelHint(request2); got != "gpt-test" {
		t.Fatalf("model hint = %q", got)
	}
}

func TestW1ChainRoutingCacheBridge(t *testing.T) {
	fixture := newChainFixture(t)
	bridge := chainRoutingCache{cache: fixture.cache}
	ctx := context.Background()
	meta, ok, err := bridge.ResolveCachedGroupUsageAccessMetadataAsync(ctx, fixture.groupID, fixture.systemAccount)
	if err != nil || !ok || meta.ProviderCode != "openai" {
		t.Fatalf("group access = %+v, %v, %v", meta, ok, err)
	}
	accounts, err := bridge.ListCachedOpenAIAccountsForGroupAsync(ctx, fixture.groupID, fixture.systemAccount, gatewayrouting.CachedAccountsForGroupOptions{})
	if err != nil || len(accounts) != 1 || accounts[0].ID != fixture.accountID {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	resolution, err := bridge.ResolveCachedProviderModelRouteAsync(ctx, gatewayrouting.ProviderModelRouteInput{Model: "gpt-test", ProviderCodes: []string{"openai"}})
	if err != nil {
		t.Fatalf("model route: %v", err)
	}
	if resolution.ModelKey == "" {
		t.Fatalf("resolution = %+v", resolution)
	}
	if _, ok, err := bridge.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "grp_missing", fixture.systemAccount); err != nil || ok {
		t.Fatalf("缺失组 = %v, %v", ok, err)
	}
	resolver := &chainRouteResolver{cache: fixture.cache}
	hydrated := resolver.rehydrateGroupAccess(ctx, fixture.groupID, fixture.systemAccount, gatewayrouting.GroupUsageAccessMetadata{ProviderCode: "fallback"})
	if hydrated == nil || hydrated.ProviderCode != "openai" {
		t.Fatalf("hydrated = %+v", hydrated)
	}
	full, _, _ := resolver.rehydrateAccounts(ctx, fixture.groupID, fixture.systemAccount,
		[]gatewayrouting.UpstreamAccount{{ID: fixture.accountID}}, "", "", "", "")
	if len(full) != 1 || full[0].ID != fixture.accountID || full[0].APIKey != "sk-upstream-account-key" {
		t.Fatalf("rehydrated = %+v", full)
	}
}
