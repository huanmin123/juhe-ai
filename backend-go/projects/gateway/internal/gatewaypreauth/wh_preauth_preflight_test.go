package gatewaypreauth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func whAuthorizedChatRequest() (*GatewayRequest, *httptest.ResponseRecorder, *TrackingWriter) {
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")
	return req, recorder, writer
}

func whRuntimeCacheFor(accounts []gatewayruntimecache.OpenAIAccountSecret) *fakeRuntimeCache {
	return &fakeRuntimeCache{
		runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": {
			APIKey: validRuntimeRow(),
			Settings: gatewayruntimecache.GatewaySettings{
				NoAvailableAccountWaitTimeoutSeconds: 30,
				ImageRequestWallTimeoutSeconds:       300,
			},
			GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai", GroupAccessType: "owner"},
			Accounts:    accounts,
		}},
		groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai", GroupAccessType: "owner"},
		accounts:    accounts,
	}
}

func whSuccessfulCandidates(account gatewayruntimecache.OpenAIAccountSecret) *fakeCandidates {
	return &fakeCandidates{
		filterResult: CandidateFilterResult{
			Outcome:  CandidateOutcomeAccounts,
			Accounts: []gatewayruntimecache.OpenAIAccountSecret{account},
		},
		preparation: DispatchPreparationResult{
			Outcome:                    CandidateOutcomeAccounts,
			Accounts:                   []gatewayruntimecache.OpenAIAccountSecret{account},
			ReleaseClientIPConcurrency: func() {},
		},
	}
}

// 运行态解析路径（Identity 为 nil）：从 API Key 解析到完整 DispatchContext。
func TestWhPreflightRuntimeResolvedHappyPath(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai", Status: "active"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = whSuccessfulCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
	})
	audit := &fakeAuditCapture{}
	req, recorder, writer := whAuthorizedChatRequest()

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1_700_000_000_000, TraceID: "trace_1",
		Endpoint:        "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{Method: "POST", Path: "/v1/chat/completions"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext == nil {
		t.Fatalf("应返回 DispatchContext: %+v", result)
	}
	context := result.DispatchContext
	if len(context.Accounts) != 1 || context.Accounts[0].ID != "acc_1" {
		t.Fatalf("accounts = %+v", context.Accounts)
	}
	if context.ActiveGatewaySettings.NoAvailableAccountWaitTimeoutSeconds != 30 {
		t.Fatalf("运行态设置未生效: %+v", context.ActiveGatewaySettings)
	}
	if context.UsageContext.APIKeyID != "key_1" || context.UsageContext.GroupID != "group_1" {
		t.Fatalf("usageContext = %+v", context.UsageContext)
	}
	if recorder.Code != 0 && recorder.Code >= 400 {
		t.Fatalf("成功路径不得写失败响应: %d", recorder.Code)
	}
}

// 运行态缺失：finalize 认证失败审计并在预检阶段结束。
func TestWhPreflightRuntimeMissing(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{}
	})
	audit := &fakeAuditCapture{}
	req, recorder, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if sink.authFailures != 1 {
		t.Fatalf("认证失败审计 = %d", sink.authFailures)
	}
	_ = recorder
}

// 模型列表快路径（Identity 为 nil）：认证后直接返回模型列表并完成请求。
func TestWhPreflightModelsFastPath(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.APIKeyValidator = &fakeAPIKeyValidator{row: validRuntimeRow()}
	})
	audit := &fakeAuditCapture{}
	req, recorder, writer := newTestRequest("GET", "/v1/models")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "GET /v1/models",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext != nil || result.RouteAction != nil {
		t.Fatalf("快路径必须就地完成: %+v", result)
	}
	if len(sink.modelSends) != 1 || sink.modelSends[0].Protocol != "openai" {
		t.Fatalf("模型响应 = %+v", sink.modelSends)
	}
	_ = recorder
}

// 本机部署 smoke：带标记的回环请求拒绝派发上游。
func TestWhPreflightDeploymentSmoke(t *testing.T) {
	service, _, sink := newTestService(t, nil)
	audit := &fakeAuditCapture{}
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("x-juhe-deployment-smoke", "no-upstream")
	req.RemoteAddr = "127.0.0.1:12345"
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options:   plainPreflightOptions(),
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != 400 || failure.Audit.ErrorCode != "deployment_smoke_no_upstream" {
		t.Fatalf("smoke 失败 = %+v", failure)
	}
	_ = recorder
}

// 普通模型路由命中：请求被改写到目标分组并重查客户端 IP 熔断。
func TestWhPreflightNormalRouteSelected(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_2", ProviderCode: "openai"}
	reroutedRow := validRuntimeRow()
	reroutedRow.SelectedGroupID = "g2"
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.RouteResolver = &fakeRouteResolver{normal: NormalRouteResult{
			Outcome: NormalRouteOutcomeSelected, GroupID: "g2", RouteSource: "model_binding",
			APIKeyRecord: reroutedRow, MatchedProviderCode: "openai",
			Accounts: []gatewayruntimecache.OpenAIAccountSecret{account},
		}}
		s.Candidates = whSuccessfulCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1_700_000_000_000, TraceID: "trace_1", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext == nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if result.DispatchContext.UsageContext.GroupID != "g2" {
		t.Fatalf("改写后分组 = %q", result.DispatchContext.UsageContext.GroupID)
	}
	// normal_model_route 审计元数据必须记录。
	found := false
	for _, call := range audit.metadata {
		if call.label == "normal_model_route" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少 normal_model_route 审计元数据")
	}
}

// 普通模型路由失败：按失败状态码渲染终态响应。
func TestWhPreflightNormalRouteFailed(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor(nil)
		s.RouteResolver = &fakeRouteResolver{normal: NormalRouteResult{
			Outcome: NormalRouteOutcomeFailed, StatusCode: 404, Type: "invalid_request_error",
			Code: "model_not_allowed", Message: "模型不在任何绑定分组中",
			MatchedProviderCodes: []string{"openai"},
		}}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Options: &PreflightOptions{},
		Req:     req, Res: writer, AuditCapture: audit,
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != 404 || failure.Audit.ErrorCode != "model_not_allowed" {
		t.Fatalf("失败 = %+v", failure)
	}
	if failure.Audit.ErrorPhase != "request_validation" {
		t.Fatalf("错误阶段 = %q", failure.Audit.ErrorPhase)
	}
	// >=500 走 dispatch 阶段。
	service2, _, sink2 := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor(nil)
		s.RouteResolver = &fakeRouteResolver{normal: NormalRouteResult{
			Outcome: NormalRouteOutcomeFailed, StatusCode: 503, Type: "service_unavailable",
			Code: "route_unavailable", Message: "暂无可用分组",
		}}
	})
	req2, _, writer2 := whAuthorizedChatRequest()
	if _, err := service2.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Options: &PreflightOptions{},
		Req:     req2, Res: writer2, AuditCapture: &fakeAuditCapture{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	}); err != nil {
		t.Fatal(err)
	}
	if failure2, ok := sink2.lastFailure(); !ok || failure2.Audit.ErrorPhase != "dispatch" {
		t.Fatalf("500+ 阶段 = %+v", failure2)
	}
}

// 图像 lane 升级：目录命中图像后更新墙钟预算与路由计划。
func TestWhPreflightImageLaneUpgrade(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_img", ProviderCode: "openai"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{
			runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": {
				APIKey: validRuntimeRow(),
				Settings: gatewayruntimecache.GatewaySettings{
					NoAvailableAccountWaitTimeoutSeconds: 30,
					ImageRequestWallTimeoutSeconds:       120,
				},
				GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
			}},
			groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
			catalog: []gatewayruntimecache.ProviderModelCatalogItem{{
				ProviderCode: "openai", Model: "catalog-model",
				SupportedAPIProtocols: []string{"images"},
			}},
		}
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{
				Outcome:  CandidateOutcomeAccounts,
				Accounts: []gatewayruntimecache.OpenAIAccountSecret{account},
			},
			preparation: DispatchPreparationResult{
				Outcome:                    CandidateOutcomeAccounts,
				Accounts:                   []gatewayruntimecache.OpenAIAccountSecret{account},
				ReleaseClientIPConcurrency: func() {},
			},
		}
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
		s.Images = &fakeImages{result: ImagePermissionPreflightResult{RequestLane: "image"}}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := whAuthorizedChatRequest()
	// 请求模型命中目录里的图像模型。
	req.Body = whBodyFor(map[string]any{"model": "catalog-model"})
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1_700_000_000_000, TraceID: "trace_1", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext == nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if result.DispatchContext.RequestLane != "image" {
		t.Fatalf("lane = %q", result.DispatchContext.RequestLane)
	}
}

// 派发准备回退：候选通过但派发准备失败时返回 RouteAction。
func TestWhPreflightDispatchPreparationFallback(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{
				Outcome:  CandidateOutcomeAccounts,
				Accounts: []gatewayruntimecache.OpenAIAccountSecret{account},
			},
			preparation: DispatchPreparationResult{Outcome: CandidateOutcomeFallback, Reason: "all_accounts_cooldown"},
		}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Options: &PreflightOptions{},
		Req:     req, Res: writer, AuditCapture: audit,
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsRouteAction() || result.RouteAction.Coordination.Reason != "all_accounts_cooldown" {
		t.Fatalf("RouteAction = %+v", result.RouteAction)
	}
	if len(sink.failureInputs) != 0 {
		t.Fatal("RouteAction 不直接写响应")
	}
}

// 可恢复候选等待：活跃账户直返、无可恢复返回空、等待后取最终快照。
func TestWhWaitForRecoverableCandidates(t *testing.T) {
	cooled := gatewayruntimecache.OpenAIAccountSecret{ID: "cool", ProviderCode: "openai"}
	buildInput := func(service *Service) recoveryInput {
		return recoveryInput{
			req: whNewRequest("POST", "/v1/chat/completions"), auditCapture: &fakeAuditCapture{},
			systemAccountID: "sys_1", apiKeyID: "key_1", groupID: "group_1",
			serverRetryBudget: NewServerRetryBudget(5_000, newFakeClock(1_000)),
		}
	}

	// 1. 活跃账户存在 → 直接返回。
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{cooled})
	})
	accounts, err := service.waitForRecoverableOpenAIGatewayCandidateAccounts(buildInput(service))
	if err != nil || len(accounts) != 1 {
		t.Fatalf("活跃账户 = %v err=%v", accounts, err)
	}

	// 2. 无活跃且无可恢复 → 空切片。
	service2, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor(nil)
	})
	accounts, err = service2.waitForRecoverableOpenAIGatewayCandidateAccounts(buildInput(service2))
	if err != nil || len(accounts) != 0 {
		t.Fatalf("空结果 = %v err=%v", accounts, err)
	}

	// 3. 有可恢复 → 等待（fake 立即返回）后取最终活跃快照。
	service3, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &whRuntimeCacheWithRecoverable{
			fakeRuntimeCache: whRuntimeCacheFor(nil),
			recoverable:      []gatewayruntimecache.OpenAIAccountSecret{cooled},
		}
	})
	recoverable := &fakeRecoverable{}
	service3.Recoverable = recoverable
	accounts, err = service3.waitForRecoverableOpenAIGatewayCandidateAccounts(buildInput(service3))
	if err != nil || len(accounts) != 0 {
		t.Fatalf("等待后快照 = %v err=%v", accounts, err)
	}
	if len(recoverable.waited) != 1 || !strings.Contains(recoverable.waited[0], "group_1") {
		t.Fatalf("等待范围 = %v", recoverable.waited)
	}
}

// 候选加载闭包：按模型/端点族转发缓存查询。
func TestWhCandidateAndRecoverableLoaders(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "a"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
	})
	// options.CandidateAccounts 非空 → 不构造闭包。
	if candidateLoader(service, &PreflightOptions{CandidateAccounts: []gatewayruntimecache.OpenAIAccountSecret{account}}, nil, nil, "g", "sys") != nil {
		t.Fatal("自带候选必须返回 nil 闭包")
	}
	loader := candidateLoader(service, &PreflightOptions{}, nil, nil, "group_1", "sys_1")
	if loader == nil {
		t.Fatal("应返回加载闭包")
	}
	accounts, err := loader("m1", "chat_completions")
	if err != nil || len(accounts) != 1 {
		t.Fatalf("加载 = %v err=%v", accounts, err)
	}
	if recoverableLoader(service, &PreflightOptions{CandidateAccounts: []gatewayruntimecache.OpenAIAccountSecret{account}}, nil, nil, recoveryInput{}) != nil {
		t.Fatal("自带候选必须返回 nil 可恢复闭包")
	}
	if recoverableLoader(service, &PreflightOptions{}, nil, nil, recoveryInput{}) == nil {
		t.Fatal("应返回可恢复闭包")
	}
	// 亲和池标识：按 profile/provider 去重排序，空集合折叠为 empty。
	if got := gatewayAffinityProviderProfilePool([]gatewayruntimecache.OpenAIAccountSecret{
		{ProviderProtocolProfileID: "p2"}, {ProviderProtocolProfileID: "p1"}, {ProviderCode: "openai"},
	}); got != "pool:openai,p1,p2" {
		t.Fatalf("亲和池 = %q", got)
	}
	if got := gatewayAffinityProviderProfilePool(nil); got != "pool:empty" {
		t.Fatalf("空亲和池 = %q", got)
	}
}

// 不可用 API Key 的早期拒绝。
func TestWhPreflightUnavailableAPIKey(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor(nil)
	})
	audit := &fakeAuditCapture{}
	// Identity 携带 key 但记录缺失 → apiKeyUnavailable。
	_, _, writer := newTestRequest("POST", "/v1/chat/completions")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: whNewRequest("POST", "/v1/chat/completions"), Res: writer,
		AuditCapture: audit,
		Options: &PreflightOptions{Identity: &OpenAIGatewayRequestIdentity{
			SystemAccountID: "sys_1", APIKeyID: "key_missing", GroupID: "group_1",
		}},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = result
	failure, ok := sink.lastFailure()
	if !ok || failure.Audit.ErrorCode != "invalid_api_key" {
		t.Fatalf("不可用 key 失败 = %+v", failure)
	}
}

// whBodyFor 构造携带解析 JSON 的 body 请求视图。
func whBodyFor(body map[string]any) *gatewaybodyRequest {
	return &gatewaybodyRequest{Body: body, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{
		JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: body,
	})}
}

type gatewaybodyRequest = gatewaybody.Request

// whRuntimeCacheWithRecoverable 在既有 fake 之上注入可恢复账户列表。
type whRuntimeCacheWithRecoverable struct {
	*fakeRuntimeCache
	recoverable []gatewayruntimecache.OpenAIAccountSecret
}

func (c *whRuntimeCacheWithRecoverable) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(_ context.Context, _, _ string, _ gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, _ *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return c.recoverable, nil
}
