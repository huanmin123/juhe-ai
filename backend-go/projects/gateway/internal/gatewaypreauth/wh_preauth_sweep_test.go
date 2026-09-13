package gatewaypreauth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 混合路由失败的文案映射表（reason → 客户端文案）。
func TestWhHybridRouteFailureMessages(t *testing.T) {
	cases := map[string]string{
		"no_scoring_account":                  "混合路由评分模型暂不可用：绑定分组池没有可用评分账户",
		"scoring_account_busy":                "混合路由评分模型暂不可用：评分账户并发已满",
		"hybrid_scoring_failed":               "混合路由评分模型调用失败",
		"hybrid_scoring_http_error":           "混合路由评分模型调用失败",
		"hybrid_level_route_missing":          "混合路由等级配置不可用",
		"hybrid_scoring_fallback_unavailable": "混合路由评分模型不可用，且低档兜底范围内没有可用目标模型",
		"hybrid_target_group_unavailable":     "混合路由目标分组暂不可用",
		"unknown_reason":                      "混合路由暂不可用",
	}
	for reason, want := range cases {
		if got := hybridRouteFailureMessage(reason); got != want {
			t.Fatalf("hybridRouteFailureMessage(%q) = %q, want %q", reason, got, want)
		}
	}
}

// 暂时阻塞的路由终态判定与协调结果。
func TestWhTemporarilyBlockedRouteFailure(t *testing.T) {
	if isTemporarilyBlockedRouteFailure(nil) {
		t.Fatal("nil 失败不算暂时阻塞")
	}
	retry := int64(1_000)
	if !isTemporarilyBlockedRouteFailure(&gatewayrouting.GatewayRouteFinalFailure{RetryAfterMs: &retry}) {
		t.Fatal("带 RetryAfter 的失败算暂时阻塞")
	}
	if !isTemporarilyBlockedRouteFailure(&gatewayrouting.GatewayRouteFinalFailure{FailureAttribution: "gateway_capacity"}) {
		t.Fatal("网关容量归因算暂时阻塞")
	}
	if !isTemporarilyBlockedRouteFailure(&gatewayrouting.GatewayRouteFinalFailure{StatusCode: 429}) {
		t.Fatal("429 算暂时阻塞")
	}
	if isTemporarilyBlockedRouteFailure(&gatewayrouting.GatewayRouteFinalFailure{StatusCode: 500}) {
		t.Fatal("普通 500 不算暂时阻塞")
	}
	// 设置覆盖逐字段生效且流熔断开关被钉住。
	override := &gatewayruntimecache.GatewaySettings{
		GatewayTextRawBodyLimitMegabytes:           9,
		AccountCircuitConfirmationFailuresRequired: 4,
		UsageStatsTimezone:                         "Asia/Shanghai",
		DefaultTemporaryUnschedulableMinutes:       5,
		TemporaryUnschedulableRetryIntervalSeconds: 11,
		TemporaryUnschedulableRetryAttempts:        3,
		TextFirstResponseTimeoutSeconds:            21,
		TextStreamIdleTimeoutSeconds:               22,
		TextUncommittedAttemptMaxLifetimeSeconds:   23,
		ImageFirstResponseTimeoutSeconds:           31,
		ImageStreamIdleTimeoutSeconds:              32,
		ImageUncommittedAttemptMaxLifetimeSeconds:  33,
		ImageRequestWallTimeoutSeconds:             34,
		NoAvailableAccountWaitTimeoutSeconds:       35,
		StreamFailureThresholdCount:                6,
		StreamFailureThresholdWindowMinutes:        7,
		GatewayUserRequestLimitPerMinute:           int64Ptr(60),
		GatewayUserRequestLimitPerDay:              int64Ptr(600),
		GatewayUserRequestLimitPerWeek:             int64Ptr(6_000),
		GatewayUserRequestLimitPerMonth:            int64Ptr(60_000),
	}
	merged, err := mergeGatewaySettings(gatewayruntimecache.GatewaySettings{NoAvailableAccountWaitTimeoutSeconds: 1}, override)
	if err != nil {
		t.Fatal(err)
	}
	if merged.GatewayTextRawBodyLimitMegabytes != 9 || merged.TextFirstResponseTimeoutSeconds != 21 ||
		merged.ImageRequestWallTimeoutSeconds != 34 || merged.NoAvailableAccountWaitTimeoutSeconds != 35 ||
		merged.UsageStatsTimezone != "Asia/Shanghai" || merged.StreamFailureThresholdCount != 6 {
		t.Fatalf("覆盖未生效: %+v", merged)
	}
	if merged.GatewayUserRequestLimitPerWeek == nil || *merged.GatewayUserRequestLimitPerWeek != 6_000 {
		t.Fatalf("周限额覆盖 = %+v", merged.GatewayUserRequestLimitPerWeek)
	}
	if !merged.StreamCircuitBreakerEnabled {
		t.Fatal("流熔断开关必须钉住为 true")
	}
}

// whCompletingCandidates 在过滤完成前写入路由终态失败（模拟协调器回写）。
type whCompletingCandidates struct {
	fakeCandidates
	failure gatewayrouting.GatewayRouteFinalFailure
}

func (f *whCompletingCandidates) FilterCandidates(ctx context.Context, input CandidateFilterInput) (CandidateFilterResult, error) {
	_ = input.RouteCoordinator.CompleteFailure(ctx, f.failure)
	return CandidateFilterResult{Outcome: CandidateOutcomeCompleted, Reason: "coordinator_completed"}, nil
}

// 协调器写入暂时阻塞失败后，Completed 结果必须折算为 RouteAction（temporarily_blocked）。
func TestWhPreflightCoordinatorFailureProducesRouteAction(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &whCompletingCandidates{
			fakeCandidates: fakeCandidates{},
			failure: gatewayrouting.GatewayRouteFinalFailure{
				StatusCode: 429, ErrorCode: "rate_limited_by_route",
				RetryAfterMs: int64Ptr(2_000),
			},
		}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1_700_000_000_000, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsRouteAction() {
		t.Fatalf("应折算为 RouteAction: %+v", result)
	}
	action := result.RouteAction
	if action.Coordination.Outcome != "temporarily_blocked" || action.Coordination.Reason != "rate_limited_by_route" {
		t.Fatalf("coordination = %+v", action.Coordination)
	}
	if action.Coordination.EarliestRetryAtMs == nil || *action.Coordination.EarliestRetryAtMs != 1_700_000_002_000 {
		t.Fatalf("最早重试 = %+v", action.Coordination.EarliestRetryAtMs)
	}
	if len(sink.failureInputs) != 0 {
		t.Fatal("RouteAction 不直接写响应")
	}
	// 硬耗尽终态（非暂时阻塞）同样折算为 RouteAction。
	service2, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &whCompletingCandidates{
			fakeCandidates: fakeCandidates{},
			failure:        gatewayrouting.GatewayRouteFinalFailure{StatusCode: 503, ErrorCode: "hard_stop"},
		}
	})
	req2, _, writer2 := whAuthorizedChatRequest()
	result2, err := service2.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req2, Res: writer2, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result2.IsRouteAction() || result2.RouteAction.Coordination.Outcome != "hard_exhausted" {
		t.Fatalf("硬耗尽 = %+v", result2.RouteAction)
	}
}

// codex compaction 请求：超时预算解除限制并记录审计元数据。
func TestWhPreflightCompactionTimeoutsDisabled(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = whSuccessfulCandidates(account)
		s.Codex = &fakeCodex{
			compactionExpected: true,
			compactResult:      CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}},
		}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext == nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	found := false
	for _, call := range audit.metadata {
		if call.label == "codex_compaction_timeouts_disabled" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少 compaction 超时禁用审计元数据")
	}
}

// codex 上下文状态预检就地完成请求。
func TestWhPreflightCodexStateCompleted(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = whSuccessfulCandidates(account)
		s.Codex = &fakeCodex{stateCompleted: true}
	})
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("codex 完成后必须就地返回: %+v err=%v", result, err)
	}
}

// 已认证模型列表：identity 场景下按协议分支发送模型响应。
func TestWhPreflightModelsProtocolBranches(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		headers  map[string]string
		protocol string
	}{
		{name: "openai", target: "/v1/models", protocol: "openai"},
		{name: "gemini", target: "/v1beta/models", protocol: "gemini"},
		{name: "anthropic", target: "/v1/models", headers: map[string]string{"anthropic-version": "2023-06-01"}, protocol: "anthropic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _, sink := newTestService(t, func(s *Service) {
				s.RuntimeCache = whRuntimeCacheFor(nil)
			})
			req, _, writer := newTestRequest("GET", tt.target)
			for key, value := range tt.headers {
				req.HTTP.Header.Set(key, value)
			}
			result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
				Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
				Options:   plainPreflightOptions(),
				StartedAt: 1, TraceID: "trace", Endpoint: "GET " + tt.target,
			})
			if err != nil || result.DispatchContext != nil {
				t.Fatalf("result = %+v err=%v", result, err)
			}
			// 分支选择只体现为不同的 Send 方法；共同记录一条模型响应。
			if len(sink.modelSends) != 1 || len(sink.modelSends[0].ProviderCodes) != 1 {
				t.Fatalf("模型响应 = %+v", sink.modelSends)
			}
			_ = tt.protocol
		})
	}
}

// 会话亲和键来源与禁用开关。
func TestWhPreflightSessionAffinity(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	build := func(affinity *fakeSessionAffinity, disable bool) (*Service, *fakeAuditCapture) {
		audit := &fakeAuditCapture{}
		service, _, _ := newTestService(t, func(s *Service) {
			s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
			s.Candidates = whSuccessfulCandidates(account)
			s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
			s.SessionAffinity = affinity
		})
		return service, audit
	}
	// client source 优先。
	service, audit := build(&fakeSessionAffinity{fromClientSource: "src-key", fromIdentity: "id-key"}, false)
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext == nil || result.DispatchContext.SessionAffinityKey != "src-key" {
		t.Fatalf("client source 亲和键 = %+v err=%v", result.DispatchContext, err)
	}
	// identity 回退。
	service, audit = build(&fakeSessionAffinity{fromIdentity: "id-key"}, false)
	req, _, writer = whAuthorizedChatRequest()
	result, err = service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext == nil || result.DispatchContext.SessionAffinityKey != "id-key" {
		t.Fatalf("identity 亲和键 = %+v err=%v", result.DispatchContext, err)
	}
	// 禁用开关清空亲和键。
	service, audit = build(&fakeSessionAffinity{fromIdentity: "id-key"}, true)
	req, _, writer = whAuthorizedChatRequest()
	result, err = service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options:   &PreflightOptions{DisableSessionAffinity: true},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext == nil || result.DispatchContext.SessionAffinityKey != "" {
		t.Fatalf("禁用亲和 = %+v err=%v", result.DispatchContext, err)
	}
}

// 认证前凭据提取链：Bearer → x-api-key → gemini 原生 key。
func TestWhExtractGatewayAPIKey(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	bearer := whNewRequest("POST", "/v1/chat/completions")
	bearer.HTTP.Header.Set("Authorization", "Bearer sk-bearer")
	if key, ok := service.ExtractGatewayAPIKey(bearer, "Bearer sk-bearer"); !ok || key != "sk-bearer" {
		t.Fatalf("Bearer = %q ok=%v", key, ok)
	}
	xAPIKey := whNewRequest("POST", "/v1/chat/completions")
	xAPIKey.HTTP.Header.Set("x-api-key", " sk-header ")
	if key, ok := service.ExtractGatewayAPIKey(xAPIKey, ""); !ok || key != "sk-header" {
		t.Fatalf("x-api-key = %q ok=%v", key, ok)
	}
	geminiKey := whNewRequest("POST", "/v1beta/models/m:generateContent?key=sk-gemini")
	if key, ok := service.ExtractGatewayAPIKey(geminiKey, ""); !ok || key != "sk-gemini" {
		t.Fatalf("gemini key = %q ok=%v", key, ok)
	}
	googHeader := whNewRequest("POST", "/v1beta/models/m:generateContent")
	googHeader.HTTP.Header.Set("x-goog-api-key", "sk-goog")
	if key, ok := service.ExtractGatewayAPIKey(googHeader, ""); !ok || key != "sk-goog" {
		t.Fatalf("x-goog-api-key = %q ok=%v", key, ok)
	}
	if _, ok := service.ExtractGatewayAPIKey(whNewRequest("POST", "/v1/chat/completions"), ""); ok {
		t.Fatal("无凭据必须失败")
	}
	// PreAuthSource 合成来源。
	if source := service.GatewayPreAuthSource(bearer, "Bearer sk-bearer"); source != "Bearer sk-bearer" {
		t.Fatalf("Bearer 来源 = %q", source)
	}
	if source := service.GatewayPreAuthSource(xAPIKey, ""); source != "x-api-key sk-header" {
		t.Fatalf("x-api-key 来源 = %q", source)
	}
	if source := service.GatewayPreAuthSource(geminiKey, ""); source != "gemini-key sk-gemini" {
		t.Fatalf("gemini 来源 = %q", source)
	}
	if IsOpenAIStreamRequest(&GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil)}) {
		t.Fatal("无流标志不得开启流")
	}
	// 客户端错误协议回退。
	if got := service.clientErrorProtocolOrOpenAI(whNewRequest("POST", "/v1/chat/completions")); got != GatewayErrorProtocolOpenAI {
		t.Fatalf("openai 协议 = %v", got)
	}
	if got := service.clientErrorProtocolOrOpenAI(whNewRequest("POST", "/unknown")); got != GatewayErrorProtocolOpenAI {
		t.Fatalf("未知路径回退 = %v", got)
	}
	// 路径与追踪视图。
	WithPath := whNewRequest("GET", "/v1/models?x=1")
	if got := requestPathOnly(WithPath); got != "/v1/models" {
		t.Fatalf("路径 = %q", got)
	}
	if trace := service.observedTraceID(WithPath); !strings.HasPrefix(trace, "trace_fixed") && trace == "" {
		t.Fatalf("追踪 ID = %q", trace)
	}
}

// 响应写包装器的上游标记与协议化 JSON 错误。
func TestWhSendGatewayJSONError(t *testing.T) {
	// anthropic 协议负载。
	_, recorder, writer := newTestRequest("POST", "/v1/messages")
	SendGatewayJSONError(writer, 429, GatewayErrorPayloadOf("过多", "rate_limit_exceeded", "code-x"), SendGatewayErrorOptions{
		Protocol: GatewayErrorProtocolAnthropic,
	})
	assertContains(t, recorder.Body.String(), `"type":"error"`, "rate_limit_error")
	// gemini 协议负载 + 认证信号。
	_, recorder2, writer2 := newTestRequest("POST", "/v1/chat/completions")
	SendGatewayJSONError(writer2, 401, GatewayErrorPayloadOf("api key 无效", "invalid_request_error", "invalid_request_error"), SendGatewayErrorOptions{
		Protocol: GatewayErrorProtocolGemini,
	})
	assertContains(t, recorder2.Body.String(), "UNAUTHENTICATED")
	// 保留上游错误文案（不做本地化改写）。
	_, recorder3, writer3 := newTestRequest("POST", "/v1/chat/completions")
	SendGatewayJSONError(writer3, 502, GatewayErrorPayloadOf("上游原文", "bad_gateway", ""), SendGatewayErrorOptions{
		Protocol:                     GatewayErrorProtocolOpenAI,
		PreserveUpstreamErrorMessage: true,
	})
	assertContains(t, recorder3.Body.String(), "上游原文")
	// 多值响应头归一化为数组。
	_, _, writer4 := newTestRequest("POST", "/v1/chat/completions")
	writer4.Header().Add("Set-Cookie", "a=1")
	writer4.Header().Add("Set-Cookie", "b=2")
	writer4.Header().Set("X-Single", "one")
	headers := responseHeadersToObject(writer4)
	if list, ok := headers["Set-Cookie"].([]any); !ok || len(list) != 2 {
		t.Fatalf("多值头 = %+v", headers["Set-Cookie"])
	}
	if headers["X-Single"] != "one" {
		t.Fatalf("单值头 = %+v", headers["X-Single"])
	}
}
