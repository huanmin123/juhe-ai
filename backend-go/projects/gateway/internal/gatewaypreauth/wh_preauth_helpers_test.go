package gatewaypreauth

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func whBoolPtr(v bool) *bool { return &v }

// 端点族判定是跨协议转换与模型映射的输入契约：openai → anthropic → gemini。
func TestWhEndpointFamilyResolution(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		family string
	}{
		{name: "chat completions", method: "POST", target: "/v1/chat/completions", family: EndpointFamilyChatCompletions},
		{name: "responses", method: "POST", target: "/v1/responses?x=1", family: EndpointFamilyResponses},
		{name: "anthropic messages", method: "POST", target: "/v1/messages", family: EndpointFamilyMessages},
		{name: "anthropic messages 无版本前缀", method: "POST", target: "/messages", family: EndpointFamilyMessages},
		{name: "gemini generateContent", method: "POST", target: "/v1beta/models/gemini-pro:generateContent", family: "generate_content"},
		{name: "gemini models 不算端点族", method: "GET", target: "/v1beta/models", family: ""},
		{name: "未知路径", method: "POST", target: "/v1/unknown", family: ""},
		{name: "GET 不匹配 anthropic", method: "GET", target: "/v1/messages", family: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, nil)
			got := gatewayRequestEndpointFamily(&GatewayRequest{HTTP: req})
			if got != tt.family {
				t.Fatalf("family = %q, want %q", got, tt.family)
			}
		})
	}
	// 纯路径变体与 /v1 前缀剥离。
	if got := openAIEndpointFamilyFromPath(" /V1/Chat/Completions "); got != EndpointFamilyChatCompletions {
		t.Fatalf("openai path family = %q", got)
	}
	if got := stripV1Prefix("/v1"); got != "" {
		t.Fatalf("strip /v1 = %q", got)
	}
	if got := stripV1Prefix("/v1beta/models"); got != "/v1beta/models" {
		t.Fatalf("非 /v1 前缀保留 = %q", got)
	}
	// gemini 端点族经由 gatewaygemini 分类器映射。
	if got := geminiEndpointFamilyFromPath("/v1beta/models"); got != "models" {
		t.Fatalf("gemini models family = %q", got)
	}
	if got := geminiEndpointFamilyForPath("/v1beta/models/m:countTokens"); got != "count_tokens" {
		t.Fatalf("gemini countTokens family = %q", got)
	}
	if got := geminiEndpointFamilyForPath("/v1beta/interactions/i1"); got != "interactions" {
		t.Fatalf("gemini interactions family = %q", got)
	}
	if got := geminiEndpointFamilyForPath("junk"); got != "" {
		t.Fatalf("未知 gemini family = %q", got)
	}
}

// 速度优先/首字截止配置从 API Key 行解码，字段缺失时逐层回退。
func TestWhNormalRouteConfigHelpers(t *testing.T) {
	speedRaw := []byte(`{"schedulingPreference":"speed_first","firstByteDeadlineMs":2500}`)
	speedRow := validRuntimeRow()
	speedRow.NormalRoutingConfig = &gatewayruntimecache.RouteStrategyNormalRoutingConfig{
		SchedulingPreference: "speed_first", Raw: speedRaw,
	}

	config := normalRouteFirstByteConfigForAPIKeyRecord(speedRow)
	if config == nil || config.SchedulingPreference != "speed_first" || config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs != 2500 {
		t.Fatalf("speed_first 配置 = %+v", config)
	}
	if got := normalRouteFirstByteConfigForAPIKeyRecord(nil); got != nil {
		t.Fatal("nil 记录必须返回 nil")
	}
	costRow := validRuntimeRow()
	if got := normalRouteFirstByteConfigForAPIKeyRecord(costRow); got != nil {
		t.Fatal("cost_first 必须返回 nil")
	}
	weightedRow := validRuntimeRow()
	weightedRow.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeWeighted
	weightedRow.NormalRoutingConfig = &gatewayruntimecache.RouteStrategyNormalRoutingConfig{SchedulingPreference: "speed_first", Raw: speedRaw}
	if got := normalRouteFirstByteConfigForAPIKeyRecord(weightedRow); got != nil {
		t.Fatal("非 normal 模式必须返回 nil")
	}
	// firstByteDeadlineFromConfig：nil/无 raw/坏 JSON/缺字段 四种回退。
	if got := firstByteDeadlineFromConfig(nil); got != nil {
		t.Fatal("nil config 必须返回 nil")
	}
	if got := firstByteDeadlineFromConfig(&gatewayruntimecache.RouteStrategyNormalRoutingConfig{}); got != nil {
		t.Fatal("无 raw 必须返回 nil")
	}
	if got := firstByteDeadlineFromConfig(&gatewayruntimecache.RouteStrategyNormalRoutingConfig{Raw: []byte(`{bad`)}); got != nil {
		t.Fatal("坏 JSON 必须返回 nil")
	}
	if got := firstByteDeadlineFromConfig(&gatewayruntimecache.RouteStrategyNormalRoutingConfig{Raw: []byte(`{"other":1}`)}); got != nil {
		t.Fatal("缺字段必须返回 nil")
	}

	// lane 门控：compaction 超时禁用或非适用 lane 直接 nil。
	service, _, _ := newTestService(t, nil)
	if got := service.normalRouteFirstByteConfigForAPIKey(speedRow, gatewayproto.LaneText, true, nil); got != nil {
		t.Fatal("compaction 禁用时必须返回 nil")
	}
	if got := service.normalRouteFirstByteConfigForAPIKey(speedRow, gatewayproto.LaneImage, false, nil); got != nil {
		t.Fatal("image lane 不适用首字截止")
	}
	override := &NormalRouteFirstByteRuntimeConfig{SchedulingPreference: "speed_first"}
	if got := service.normalRouteFirstByteConfigForAPIKey(speedRow, gatewayproto.LaneText, false, override); got != override {
		t.Fatal("override 必须原样返回")
	}
	// 速度优先运行时配置：raw 中的截止时间。
	speedFirst := service.normalRouteSpeedFirstConfigForAPIKey(speedRow, gatewayproto.LaneText, false)
	if speedFirst == nil || speedFirst.FirstByteDeadlineMs == nil || *speedFirst.FirstByteDeadlineMs != 2500 {
		t.Fatalf("速度优先配置 = %+v", speedFirst)
	}
	// 无 raw 的 speed_first 配置直接返回 nil（Opaque 载荷缺失）。
	noRawRow := validRuntimeRow()
	noRawRow.NormalRoutingConfig = &gatewayruntimecache.RouteStrategyNormalRoutingConfig{SchedulingPreference: "speed_first"}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(noRawRow, gatewayproto.LaneText, false); got != nil {
		t.Fatal("无 raw 必须返回 nil")
	}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow, gatewayproto.LaneImage, false); got != nil {
		t.Fatal("image lane 不适用速度优先")
	}
	badRawRow := validRuntimeRow()
	badRawRow.NormalRoutingConfig = &gatewayruntimecache.RouteStrategyNormalRoutingConfig{SchedulingPreference: "speed_first", Raw: []byte(`[1,2]`)}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(badRawRow, gatewayproto.LaneText, false); got != nil {
		t.Fatal("非对象 raw 必须返回 nil")
	}
	weightedSpeedRow := validRuntimeRow()
	weightedSpeedRow.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeWeighted
	if got := service.normalRouteSpeedFirstConfigForAPIKey(weightedSpeedRow, gatewayproto.LaneText, false); got != nil {
		t.Fatal("非 normal 模式必须返回 nil")
	}
}

// 分组回退可行性：快照游标优先，否则按激活绑定顺序判定。
func TestWhCanAttemptAPIKeyGroupFallback(t *testing.T) {
	bindings := []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{ID: "b1", GroupID: "g1", Status: "active"},
		{ID: "b2", GroupID: "g2", Status: "active"},
	}
	row := validRuntimeRow()
	row.GroupBindings = bindings
	if !canAttemptAPIKeyGroupFallback(row, "g1", nil) {
		t.Fatal("g1 后还有 g2 可回退")
	}
	if canAttemptAPIKeyGroupFallback(row, "g2", nil) {
		t.Fatal("最后一个绑定不可回退")
	}
	if canAttemptAPIKeyGroupFallback(row, "missing", nil) {
		t.Fatal("缺失分组不可回退")
	}
	single := validRuntimeRow()
	single.GroupBindings = bindings[:1]
	if canAttemptAPIKeyGroupFallback(single, "g1", nil) {
		t.Fatal("单绑定不可回退")
	}
	snapshot := &gatewayrouting.RoutePlanSnapshot[string]{Cursor: 0, OrderedAllowedTargets: []string{"g1", "g2"}}
	if !canAttemptAPIKeyGroupFallback(row, "g2", snapshot) {
		t.Fatal("快照路径按游标判定")
	}
	snapshot.Cursor = 1
	if canAttemptAPIKeyGroupFallback(row, "g2", snapshot) {
		t.Fatal("快照末位不可回退")
	}
}

// 路由计划快照：目标去重、加权令牌、混合评分决策与墙钟预算指针。
func TestWhRoutePlanSnapshot(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	bindings := []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{ID: "binding-9", GroupID: "group_1", Status: "active"},
		{ID: "binding-2", GroupID: "g2", Status: "active"},
	}
	row := validRuntimeRow()
	row.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeWeighted
	row.GroupBindings = bindings
	budget := &gatewayrouting.GatewayRequestWallBudget{BudgetMs: 5_000, DeadlineAtMs: 1_234}
	firstByte := int64(2_500)
	snapshot, err := service.createOpenAIGatewayRoutePlanSnapshot(routePlanInput{
		traceID:                    "trace_1",
		startedAt:                  1_000,
		groupId:                    "group_1",
		apiKeyRecord:               row,
		gatewayRequestWallBudget:   budget,
		normalRouteFirstByteConfig: &NormalRouteFirstByteRuntimeConfig{FirstByteDeadlineMs: &firstByte},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Cursor != 0 {
		t.Fatalf("游标 = %d", snapshot.Cursor)
	}
	if len(snapshot.OrderedAllowedTargets) < 1 || snapshot.OrderedAllowedTargets[0] != "group_1" {
		t.Fatalf("目标顺序 = %v", snapshot.OrderedAllowedTargets)
	}
	if snapshot.GatewayRequestWallBudgetMs != 5_000 {
		t.Fatalf("墙钟预算 = %d", snapshot.GatewayRequestWallBudgetMs)
	}
	if snapshot.FirstByteDeadlineMs == nil || *snapshot.FirstByteDeadlineMs != 2_500 {
		t.Fatalf("首字截止 = %+v", snapshot.FirstByteDeadlineMs)
	}
	if snapshot.WeightedDecisionToken != "binding-9" {
		t.Fatalf("加权令牌 = %q", snapshot.WeightedDecisionToken)
	}
	// hybridRouteField/hybridRouteBool 的 nil 安全读取。
	if hybridRouteField(nil, "level") != nil || hybridRouteField(map[string]any{}, "missing") != nil {
		t.Fatal("缺失字段必须返回 nil")
	}
	if !hybridRouteBool(map[string]any{"defaulted": true}, "defaulted") || hybridRouteBool(map[string]any{"defaulted": "x"}, "defaulted") {
		t.Fatal("hybridRouteBool 只接受布尔")
	}
}

// 用量上下文装配：默认 tier、分组元数据透传。
func TestWhBuildGatewayUsageContext(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	context := service.BuildGatewayUsageContext(usageContextInput{
		traceID: "t1", clientIP: "1.2.3.4", trafficSource: TrafficSourceGateway,
		identity: OpenAIGatewayRequestIdentity{SystemAccountID: "sys", APIKeyID: "key", GroupID: "g"},
		endpoint: "POST /v1/chat/completions",
		requestSnapshot: UsageRequestSnapshot{RequestedReasoningEffort: "high"},
	})
	if context.TraceID != "t1" || context.RequestedServiceTier != "default" || context.EffectiveServiceTier != "default" {
		t.Fatalf("默认 tier 上下文 = %+v", context)
	}
	if context.RequestedReasoningEffort != "high" || context.EffectiveReasoningEffort != "high" {
		t.Fatalf("推理档位 = %+v", context)
	}
	withTier := service.BuildGatewayUsageContext(usageContextInput{
		requestSnapshot: UsageRequestSnapshot{RequestedServiceTier: "flex"},
		groupUsageFields: &GroupUsageMetadataFields{
			ProviderCode: "openai", GroupOwnerSystemAccountID: "owner", GroupAccessType: "owner",
		},
	})
	if withTier.RequestedServiceTier != "flex" || withTier.ProviderCode != "openai" || withTier.GroupOwnerSystemAccountID != "owner" {
		t.Fatalf("分组元数据上下文 = %+v", withTier)
	}
}

// 可恢复账户重试时间：取最小剩余冷却，坏时间戳必须显式失败。
func TestWhNextRecoverableAccountRetryAfterMs(t *testing.T) {
	if _, ok := nextRecoverableAccountRetryAfterMs(nil, 1_000); ok {
		t.Fatal("空列表必须失败")
	}
	if _, ok := nextRecoverableAccountRetryAfterMs([]gatewayruntimecache.OpenAIAccountSecret{}, 1_000); ok {
		t.Fatal("无冷却账户必须失败")
	}
	now := int64(1_700_000_000_000)
	iso := func(ms int64) *string {
		value := time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
		return &value
	}
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		{CooldownUntil: iso(now + 5_000)},
		{CooldownUntil: iso(now + 3_000)},
		{CooldownUntil: iso(now - 9_000)},
	}
	// 已过期的冷却（剩余为负）按 0 参与，并成为最小值。
	retryAfter, ok := nextRecoverableAccountRetryAfterMs(accounts, now)
	if !ok || retryAfter != 0 {
		t.Fatalf("最小剩余 = %d ok=%v", retryAfter, ok)
	}
	broken := []gatewayruntimecache.OpenAIAccountSecret{{CooldownUntil: whStringPtr("not-a-time")}}
	if _, ok := nextRecoverableAccountRetryAfterMs(broken, now); ok {
		t.Fatal("坏时间戳必须失败")
	}
	// 共享 RFC3339 解析器（要求显式 offset）。
	if ms, ok := gatewayruntimecacheRFC3339Millis("2026-01-02T03:04:05Z"); !ok || ms != 1_767_323_045_000 {
		t.Fatalf("Z 解析 = %d ok=%v (want 1767323045000)", ms, ok)
	}
	if _, ok := gatewayruntimecacheRFC3339Millis("2026-01-02T03:04:05"); ok {
		t.Fatal("缺 offset 必须失败")
	}
	if _, err := timeParseRFC3339Instant("garbage"); err == nil {
		t.Fatal("垃圾输入必须报错")
	}
	if _, err := gatewaybodyDecodeJSON([]byte(`{bad`)); err == nil {
		t.Fatal("坏 JSON 必须报错")
	}
	if decoded, err := gatewaybodyDecodeJSON([]byte(`{"a":1}`)); err != nil || decoded["a"].(float64) != 1 {
		t.Fatalf("正常解码 = %v err=%v", decoded, err)
	}
}

// 恢复候选范围键与字符串助手。
func TestWhRecoverableScopeAndStringHelpers(t *testing.T) {
	if got := recoverableCandidateScopeKey("sys", "key", "g", "model", "chat_completions"); got != "sys:key:g:model:chat_completions" {
		t.Fatalf("scope key = %q", got)
	}
	if !containsString([]string{"a", "b"}, "b") || containsString(nil, "a") {
		t.Fatal("containsString 语义错误")
	}
	if indexOfString([]string{"a", "b"}, "b") != 1 || indexOfString([]string{"a"}, "z") != -1 {
		t.Fatal("indexOfString 语义错误")
	}
	if !boolListContains([]string{"image"}, "image") {
		t.Fatal("boolListContains 语义错误")
	}
	if normalizeProviderToken(" Anthropic ") != "anthropic" || normalizeProviderToken("") != "" {
		t.Fatal("normalizeProviderToken 语义错误")
	}
	anthropic := gatewayruntimecache.OpenAIAccountSecret{ProtocolCode: "Anthropic", ProtocolVersion: "V1"}
	if !isAnthropicProtocolAccount(anthropic) {
		t.Fatal("anthropic v1 账户必须匹配")
	}
	if binding, ok := bindingForGroup(*validRuntimeRow(), "group_1"); !ok || binding.ID != "binding_1" {
		t.Fatalf("绑定查找 = %+v ok=%v", binding, ok)
	}
	if _, ok := bindingForGroup(*validRuntimeRow(), "other"); ok {
		t.Fatal("缺失绑定必须失败")
	}
}

// 交互亲和失败响应：状态码分界与审计载荷透传。
func TestWhSendInteractionAffinityFailure(t *testing.T) {
	service, _, sink := newTestService(t, nil)
	req, _, _ := newTestRequest("POST", "/v1/chat/completions")
	service.sendInteractionAffinityFailure(nil, interactionFailureInput{
		req: req, startedAt: 1, statusCode: 400,
		code: "invalid_model", message: "模型不可用",
	})
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != 400 || failure.Audit.ErrorCode != "invalid_model" {
		t.Fatalf("失败输入 = %+v", failure)
	}
	if failure.Audit.ErrorPhase != "request_validation" {
		t.Fatalf("错误阶段 = %q", failure.Audit.ErrorPhase)
	}
	// >=500 归类 dispatch 阶段。
	service.sendInteractionAffinityFailure(nil, interactionFailureInput{statusCode: 503, code: "c", message: "m"})
	failure, _ = sink.lastFailure()
	if failure.StatusCode != 503 || failure.Audit.ErrorPhase != "dispatch" {
		t.Fatalf("dispatch 阶段 = %+v", failure)
	}
}

// 混合路由失败元数据与状态码映射。
func TestWhHybridFailureMetadata(t *testing.T) {
	metadata := hybridFailedMetadata(nil, HybridRouteResult{
		Reason: "hybrid_scoring_failed", TargetModel: "m1",
		Scoring: map[string]any{"failed": true, "defaulted": true, "errorCode": "E", "errorMessage": "bad"},
	})
	if metadata["level"] != nil || metadata["scoringDefaulted"] != true || metadata["scoringErrorCode"] != "E" || metadata["scoringErrorMessage"] != "bad" {
		t.Fatalf("失败元数据 = %v", metadata)
	}
	plain := hybridFailedMetadata(nil, HybridRouteResult{Reason: "hybrid_target_group_unavailable", Scoring: map[string]any{"level": 3}})
	if plain["level"] != 3 {
		t.Fatalf("level 透传 = %v", plain["level"])
	}
	if got := hybridRouteFailureStatusCode("hybrid_scoring_failed"); got != 502 {
		t.Fatalf("评分失败状态码 = %d", got)
	}
	if got := hybridRouteFailureStatusCode("hybrid_scoring_http_error"); got != 502 {
		t.Fatalf("评分 HTTP 状态码 = %d", got)
	}
	if got := hybridRouteFailureStatusCode("other"); got != 503 {
		t.Fatalf("其他状态码 = %d", got)
	}
}

// 模型列表响应协议判定：显式客户端画像决定 anthropic/gemini，否则回退 openai。
func TestWhModelsResponseProtocol(t *testing.T) {
	geminiPath := httptest.NewRequest("GET", "/v1beta/models", nil)
	if protocol, ok := ResolveGatewayModelsResponseProtocol(&GatewayRequest{HTTP: geminiPath}); !ok || protocol != ResponseProtocolGeminiV {
		t.Fatalf("gemini path protocol = %v ok=%v", protocol, ok)
	}
	geminiUA := httptest.NewRequest("GET", "/models", nil)
	geminiUA.Header.Set("User-Agent", "GeminiCLI/v1.0")
	if protocol, ok := ResolveGatewayModelsResponseProtocol(&GatewayRequest{HTTP: geminiUA}); !ok || protocol != ResponseProtocolGeminiV {
		t.Fatalf("gemini UA protocol = %v ok=%v", protocol, ok)
	}
	geminiKey := httptest.NewRequest("GET", "/models?key=abc", nil)
	if !isExplicitGeminiModelsClient(&GatewayRequest{HTTP: geminiKey}) {
		t.Fatal("key 查询参数必须是显式 gemini 客户端")
	}
	googAPIKey := httptest.NewRequest("GET", "/models", nil)
	googAPIKey.Header.Set("x-goog-api-key", "k")
	if !isExplicitGeminiModelsClient(&GatewayRequest{HTTP: googAPIKey}) {
		t.Fatal("x-goog-api-key 必须是显式 gemini 客户端")
	}
	profile := httptest.NewRequest("GET", "/models", nil)
	profile.Header.Set(GatewayClientProfileHeader, "generic gemini")
	if !isExplicitGeminiModelsClient(&GatewayRequest{HTTP: profile}) {
		t.Fatal("画像头分隔符必须折叠")
	}
	anthropicReq := httptest.NewRequest("GET", "/v1/models", nil)
	anthropicReq.Header.Set("anthropic-version", "2023-06-01")
	if protocol, ok := ResolveGatewayModelsResponseProtocol(&GatewayRequest{HTTP: anthropicReq}); !ok || protocol != ResponseProtocolAnthropicV {
		t.Fatalf("anthropic protocol = %v ok=%v", protocol, ok)
	}
	claudeUA := httptest.NewRequest("GET", "/v1/models", nil)
	claudeUA.Header.Set("User-Agent", "claude-cli/1.2 (cli)")
	if !isExplicitAnthropicModelsClient(&GatewayRequest{HTTP: claudeUA}) {
		t.Fatal("claude-cli UA 必须是显式 anthropic 客户端")
	}
	// 无显式画像：openai 兜底。
	plain := httptest.NewRequest("GET", "/v1/models", nil)
	if protocol, ok := ResolveGatewayModelsResponseProtocol(&GatewayRequest{HTTP: plain}); !ok || protocol != ResponseProtocolOpenAI {
		t.Fatalf("openai 兜底 = %v ok=%v", protocol, ok)
	}
	nonModels := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if _, ok := ResolveGatewayModelsResponseProtocol(&GatewayRequest{HTTP: nonModels}); ok {
		t.Fatal("非模型列表请求不解析协议")
	}
	if modelsResponseKind(ResponseProtocolAnthropicV) != "anthropic" || modelsResponseKind(ResponseProtocolGeminiV) != "gemini" || modelsResponseKind(ResponseProtocolOpenAI) != "openai" {
		t.Fatal("modelsResponseKind 映射错误")
	}
	if collapseSeparators("a--b  c") != "a_b_c" {
		t.Fatalf("分隔符折叠 = %q", collapseSeparators("a--b  c"))
	}
}

// 图像权限与 lane 判定：端点/模型短路、body 提示与禁用开关。
func TestWhImagePermissionAndLane(t *testing.T) {
	row := validRuntimeRow()
	imageLaneRow := validRuntimeRow()
	imageLaneRow.SystemAccountImageGenerationEnabled = 0
	if !IsImageGenerationDisabledForAPIKey(imageLaneRow, gatewayproto.LaneImage) {
		t.Fatal("未开启图像生成的 image lane 必须被拒")
	}
	if IsImageGenerationDisabledForAPIKey(row, gatewayproto.LaneImage) {
		t.Fatal("已开启的 image lane 必须放行")
	}
	if IsImageGenerationDisabledForAPIKey(row, gatewayproto.LaneText) {
		t.Fatal("text lane 不受图像开关约束")
	}
	if IsImageGenerationDisabledForAPIKey(nil, gatewayproto.LaneImage) {
		t.Fatal("nil 记录不禁用（由 401 处理）")
	}
	// 请求 lane：body 的 imageGeneration 捕获标志短路。
	imageReq := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	flagged := &GatewayRequest{HTTP: imageReq, Body: &gatewaybody.Request{State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{ImageGeneration: whBoolPtr(true)})}}
	if lane := ResolveOpenAIGatewayRequestLane(flagged); lane != gatewayproto.LaneImage {
		t.Fatalf("捕获标志 lane = %v", lane)
	}
	// 模型提示：raw body model 优先。
	bodyReq := &GatewayRequest{HTTP: imageReq, Body: &gatewaybody.Request{Body: map[string]any{"model": "gpt-image-1"}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{"model": "gpt-image-1"}})}}
	if hint := requestModelHint(bodyReq); hint != "gpt-image-1" {
		t.Fatalf("模型提示 = %q", hint)
	}
	if lane := ResolveOpenAIGatewayRequestLane(bodyReq); lane != gatewayproto.LaneImage {
		t.Fatalf("图像模型 lane = %v", lane)
	}
	textBody := &GatewayRequest{HTTP: imageReq, Body: &gatewaybody.Request{Body: map[string]any{"model": "gpt-4o"}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{"model": "gpt-4o"}})}}
	if hint := requestModelHint(textBody); hint != "gpt-4o" {
		t.Fatalf("文本模型提示 = %q", hint)
	}
	if hint := requestModelHint(&GatewayRequest{HTTP: imageReq}); hint != "" {
		t.Fatalf("空 body 提示 = %q", hint)
	}
	// 图像端点判定。
	imagesPath := httptest.NewRequest("POST", "/v1/images/generations", nil)
	if !IsOpenAIGatewayImageEndpointOrModelRequest(&GatewayRequest{HTTP: imagesPath}) {
		t.Fatal("/v1/images 前缀必须命中")
	}
	imagesBare := httptest.NewRequest("POST", "/images", nil)
	if !IsOpenAIGatewayImageEndpointOrModelRequest(&GatewayRequest{HTTP: imagesBare}) {
		t.Fatal("/images 精确路径必须命中")
	}
	if !IsOpenAIGatewayImageGenerationModel("dall-e-3") || IsOpenAIGatewayImageGenerationModel("gpt-4o") {
		t.Fatal("图像模型判定语义错误")
	}
	if !IsOpenAIGatewayImageGenerationModel("imagen-3") || !IsOpenAIGatewayImageGenerationModel("nano-banana-pro") {
		t.Fatal("扩展图像模型前缀必须命中")
	}
}

// 中止归因：超时/取消来源判别与上游中止错误识别。
func TestWhAbortAttribution(t *testing.T) {
	MarkGatewayRequestAbortSource(nil, AbortSourceServerDiagnosticTimeout) // nil 安全
	req := &GatewayRequest{}
	if _, ok := GatewayRequestAbortSourceOf(req); ok {
		t.Fatal("未标记请求不得有来源")
	}
	MarkGatewayRequestAbortSource(req, AbortSourceServerDiagnosticCancel)
	if source, ok := GatewayRequestAbortSourceOf(req); !ok || source != AbortSourceServerDiagnosticCancel {
		t.Fatalf("来源 = %v ok=%v", source, ok)
	}
	if GatewayDiagnosticAbortSourceFromSignal("context deadline exceeded", nil) != AbortSourceServerDiagnosticTimeout {
		t.Fatal("deadline 原因必须识别为超时")
	}
	if GatewayDiagnosticAbortSourceFromSignal("canceled", nil) != AbortSourceServerDiagnosticCancel {
		t.Fatal("普通原因必须识别为取消")
	}
	if GatewayDiagnosticAbortSourceFromSignal("", &whTimeoutError{}) != AbortSourceServerDiagnosticTimeout {
		t.Fatal("Timeout() 错误必须识别为超时")
	}
	if GatewayDiagnosticAbortSourceFromSignal("", errors.New("operation timeout")) != AbortSourceServerDiagnosticTimeout {
		t.Fatal("消息匹配必须识别为超时")
	}
	if GatewayDiagnosticAbortSourceFromSignal("", errors.New("boom")) != AbortSourceServerDiagnosticCancel {
		t.Fatal("其他错误识别为取消")
	}
	if IsUpstreamRequestAbortedError(nil) {
		t.Fatal("nil 不算上游中止")
	}
	if !IsUpstreamRequestAbortedError(&whUpstreamAborted{}) {
		t.Fatal("UpstreamAbortedError 实现必须命中")
	}
	if !IsUpstreamRequestAbortedError(errors.New("请求已取消")) {
		t.Fatal("取消文案必须命中")
	}
	if IsUpstreamRequestAbortedError(errors.New("other")) {
		t.Fatal("其他错误不算上游中止")
	}
	if guidanceCreatedSeconds(time.UnixMilli(1_700_000_000_123)) != 1_700_000_000 {
		t.Fatalf("guidance 秒 = %d", guidanceCreatedSeconds(time.UnixMilli(1_700_000_000_123)))
	}
}

type whTimeoutError struct{}

func (e *whTimeoutError) Error() string { return "wh timeout" }
func (e *whTimeoutError) Timeout() bool { return true }

type whUpstreamAborted struct{}

func (e *whUpstreamAborted) Error() string                { return "aborted" }
func (e *whUpstreamAborted) UpstreamRequestAborted() bool { return true }

// 服务器重试预算：累计等待、观察者切换与交接判定。
func TestWhServerRetryBudget(t *testing.T) {
	clock := newFakeClock(1_000_000)
	budget := NewServerRetryBudget(10_000, clock)
	if budget.WaitBudgetMs != 10_000 {
		t.Fatalf("预算 = %d", budget.WaitBudgetMs)
	}
	if NewServerRetryBudget(0, clock).WaitBudgetMs != 1 {
		t.Fatal("预算下限 1ms")
	}
	if budget.NowMs() != 1_000_000 {
		t.Fatalf("NowMs = %d", budget.NowMs())
	}
	started := 0
	paused := 0
	budget.SetWaitObserver(&ServerRetryBudgetWaitObserver{OnWaitStarted: func() { started++ }, OnWaitPaused: func() { paused++ }})
	budget.BeginNoAvailableWait(nil)
	budget.BeginNoAvailableWait(nil) // 重复开始是 no-op
	if started != 1 {
		t.Fatalf("开始次数 = %d", started)
	}
	clock.Advance(3_000)
	if budget.ElapsedMs(nil) != 3_000 || budget.RemainingMs(nil) != 7_000 {
		t.Fatalf("等待中 elapsed/remaining = %d/%d", budget.ElapsedMs(nil), budget.RemainingMs(nil))
	}
	// DeadlineAtMs：开始等待并返回剩余预算的截止点。
	before := clock.nowMsValue()
	deadline := budget.DeadlineAtMs(nil)
	if deadline != before+7_000 {
		t.Fatalf("截止 = %d, want %d", deadline, before+7_000)
	}
	budget.PauseNoAvailableWait(nil)
	budget.PauseNoAvailableWait(nil) // 重复暂停是 no-op
	if paused != 1 {
		t.Fatalf("暂停次数 = %d", paused)
	}
	if budget.ElapsedMs(nil) != 3_000 {
		t.Fatalf("累计等待 = %d", budget.ElapsedMs(nil))
	}
	// 累计封顶在预算值。
	clock.Advance(20_000)
	budget.BeginNoAvailableWait(nil)
	clock.Advance(30_000)
	budget.PauseNoAvailableWait(nil)
	if budget.ElapsedMs(nil) != 10_000 || budget.RemainingMs(nil) != 0 {
		t.Fatalf("封顶 elapsed/remaining = %d/%d", budget.ElapsedMs(nil), budget.RemainingMs(nil))
	}
	// 交接判定。
	if budget.HandoffRequired(AvailabilityDispatchableNow, nil) {
		t.Fatal("可派发不交接")
	}
	if !budget.HandoffRequired(AvailabilityHardExhausted, nil) {
		t.Fatal("硬耗尽必须交接")
	}
	if !budget.HandoffRequired(AvailabilityRecoverableLater, nil) {
		t.Fatal("等待预算耗尽必须交接")
	}
	if ShouldHandoffClient(AvailabilityRecoverableLater, 1, 10_000) {
		t.Fatal("预算未耗尽不交接")
	}
	if ShouldHandoffClient(AvailabilityRecoverableLater, -5, -5) {
		t.Fatal("负值归一化后 0 < 1 不交接")
	}
	// 观察者替换：等待中切换会先暂停旧观察者并开始新观察者。
	other := &ServerRetryBudgetWaitObserver{OnWaitStarted: func() { started++ }, OnWaitPaused: func() { paused++ }}
	budget.BeginNoAvailableWait(nil)
	budget.SetWaitObserver(other)
	// 旧观察者在等待中被暂停（paused+1），新观察者立即收到开始（started+1）。
	if paused != 3 || started != 4 {
		t.Fatalf("切换观察者 started/paused = %d/%d", started, paused)
	}
	// nil 观察者切换安全。
	budget.SetWaitObserver(nil)
	// 指定 nowMs 的读取路径。
	if got := budget.ElapsedMs(int64Ptr(5)); got != 10_000 {
		t.Fatalf("指定 now 的 elapsed = %d", got)
	}
	_ = strings.TrimSpace
	_ = gatewaybody.JSONParseStatusParsed
}

func whStringPtr(v string) *string { return &v }
