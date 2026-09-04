package gatewaypreauth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// preflight.ts 的表驱动测试：models 快路、部署 smoke、熔断/配额拒绝顺序、
// 路由失败与 RouteAction 组装。

func TestPreflight_ModelsFastPath(t *testing.T) {
	service, _, sink := newTestService(t, nil)
	req, recorder, writer := newTestRequest("GET", "/v1/models")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")
	audit := &fakeAuditCapture{}

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: &PreflightOptions{}, StartedAt: 1, TraceID: "trace",
		ClientIP: "203.0.113.9", Endpoint: "GET /v1/models",
		RequestSnapshot: UsageRequestSnapshot{Method: "GET", Path: "/v1/models"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext != nil || result.RouteAction != nil {
		t.Fatal("models 快路应返回空结果")
	}
	if len(sink.modelSends) != 1 {
		t.Fatalf("modelSends = %d", len(sink.modelSends))
	}
	if recorder.Code != http.StatusOK && len(sink.failureInputs) != 0 {
		t.Fatalf("不应有失败响应: %d", recorder.Code)
	}
}

func TestPreflight_DeploymentSmoke(t *testing.T) {
	service, _, sink := newTestService(t, nil)
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("x-juhe-deployment-smoke", "no-upstream")
	req.RemoteAddr = "127.0.0.1:5555"
	req.ClientIP = "127.0.0.1"
	audit := &fakeAuditCapture{}

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: &PreflightOptions{}, StartedAt: 1, TraceID: "trace",
		Endpoint:        "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext != nil {
		t.Fatal("smoke 不应派发")
	}
	input, ok := sink.lastFailure()
	if !ok || input.StatusCode != http.StatusBadRequest {
		t.Fatalf("failure = %+v", input)
	}
	if input.ResponsePayload.Error.Message != "部署 smoke 已在网关本地完成，未派发上游" {
		t.Fatalf("message = %v", input.ResponsePayload.Error.Message)
	}
	if input.Audit.ErrorCode != "deployment_smoke_no_upstream" || input.RecordUsage == nil || *input.RecordUsage != false {
		t.Fatalf("audit = %+v recordUsage = %v", input.Audit, input.RecordUsage)
	}
}

func TestPreflight_AuthFailureFinalizesAudit(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{}
	})
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Del("Authorization")
	audit := &fakeAuditCapture{}

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: &PreflightOptions{}, StartedAt: 1, TraceID: "trace",
		Endpoint:        "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext != nil {
		t.Fatal("鉴权失败不应派发")
	}
	if sink.authFailures != 1 {
		t.Fatalf("authFailures = %d", sink.authFailures)
	}
}

func TestPreflight_ClientIPErrorCircuitBlocked(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{clientIPDecision: CircuitDecision{Blocked: true, Reason: "invalid_json", RetryAfterSeconds: int64Ptr(20)}}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: plainPreflightOptions(), StartedAt: 1, TraceID: "trace",
		Endpoint:        "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext != nil {
		t.Fatal("熔断短路不应派发")
	}
	input, _ := sink.lastFailure()
	if input.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", input.StatusCode)
	}
	if input.ResponsePayload.Error.Message != "当前来源短时间错误过多，请稍后重试" {
		t.Fatalf("message = %v", input.ResponsePayload.Error.Message)
	}
	if input.Audit.ErrorPhase != "security" || input.Audit.ErrorCode != "client_ip_error_circuit_open" {
		t.Fatalf("audit = %+v", input.Audit)
	}
	found := false
	for _, call := range audit.metadata {
		if call.label == "client_ip_error_circuit" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少 client_ip_error_circuit 审计元数据")
	}
}

func TestPreflight_InvalidJSONBody(t *testing.T) {
	service, _, sink := newTestService(t, nil)
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	invalid := gatewaybodyInvalidJSON()
	bodyReq := bodyRequestForState(invalid)
	req.Body = &bodyReq

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: plainPreflightOptions(), StartedAt: 1, TraceID: "trace",
		Endpoint:        "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext != nil {
		t.Fatal("invalid_json 不应派发")
	}
	if len(sink.failureInputs) != 1 {
		t.Fatalf("failureInputs = %d", len(sink.failureInputs))
	}
	input := sink.failureInputs[0]
	if input.StatusCode != http.StatusBadRequest || input.ResponsePayload.Error.Message != "请求体不是合法 JSON" {
		t.Fatalf("failure = %+v", input)
	}
	if input.Audit.ErrorCode != "invalid_json" {
		t.Fatalf("audit = %+v", input.Audit)
	}
}

func TestPreflight_QuotaRejectionOrdering(t *testing.T) {
	t.Run("API Key 配额先于授权配额", func(t *testing.T) {
		quota := &fakeAPIKeyQuota{decision: gatewayquota.DeniedDecision("额度已用完，请联系管理员提升额度")}
		authz := &fakeAuthorizationQuota{decision: gatewayquota.DeniedDecision("x")}
		service, _, sink := newTestService(t, func(s *Service) {
			s.APIKeyQuota = quota
			s.AuthorizationQuota = authz
			s.RuntimeCache = &fakeRuntimeCache{groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}}
		})
		audit := &fakeAuditCapture{}
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
			Req: req, Res: writer, AuditCapture: audit,
			Options: plainPreflightOptions(), StartedAt: 1, TraceID: "trace",
			Endpoint: "POST /v1/chat/completions", RequestSnapshot: UsageRequestSnapshot{},
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if result.DispatchContext != nil {
			t.Fatal("配额拒绝不应派发")
		}
		if len(quota.rows) != 1 || len(authz.calls) != 0 {
			t.Fatalf("api 配额调用 = %d, 授权配额调用 = %d", len(quota.rows), len(authz.calls))
		}
		input, _ := sink.lastFailure()
		if input.Audit.ErrorPhase != "quota" {
			t.Fatalf("audit = %+v", input.Audit)
		}
	})
	t.Run("授权配额其次", func(t *testing.T) {
		authz := &fakeAuthorizationQuota{decision: gatewayquota.DeniedDecision("授权已超额")}
		service, _, sink := newTestService(t, func(s *Service) {
			s.AuthorizationQuota = authz
			s.RuntimeCache = &fakeRuntimeCache{groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}}
		})
		audit := &fakeAuditCapture{}
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
			Req: req, Res: writer, AuditCapture: audit,
			Options: plainPreflightOptions(), StartedAt: 1, TraceID: "trace",
			Endpoint: "POST /v1/chat/completions", RequestSnapshot: UsageRequestSnapshot{},
		})
		if err != nil || result.DispatchContext != nil {
			t.Fatalf("result = %+v err = %v", result, err)
		}
		input, _ := sink.lastFailure()
		if input.StatusCode != http.StatusTooManyRequests || input.ResponsePayload.Error.Message != "授权已超额" {
			t.Fatalf("failure = %+v", input)
		}
	})
}

func TestPreflight_MissingGroupAccess(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{groupAccess: nil}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: plainPreflightOptions(), StartedAt: 1, TraceID: "trace",
		Endpoint: "POST /v1/chat/completions", RequestSnapshot: UsageRequestSnapshot{},
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	input, _ := sink.lastFailure()
	if input.StatusCode != http.StatusForbidden || input.ResponsePayload.Error.Message != "API Key 绑定的分组授权不可用" {
		t.Fatalf("failure = %+v", input)
	}
}

func TestPreflight_NormalRouteFailed(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.RouteResolver = &fakeRouteResolver{normal: NormalRouteResult{
			Outcome:    NormalRouteOutcomeFailed,
			StatusCode: 404, Type: "invalid_request_error", Code: "model_not_routable_for_api_key",
			Message: "当前 API Key 无法路由该模型", RequestedModel: "gpt-x",
			MatchedProviderCodes: []string{"openai"},
		}}
		s.RuntimeCache = &fakeRuntimeCache{
			runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": validRuntime()},
			groupAccess:  &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")
	body := map[string]any{"model": "gpt-x"}
	bodyReq := bodyRequestForBody(body)
	req.Body = &bodyReq

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: &PreflightOptions{}, StartedAt: 1, TraceID: "trace",
		ClientIP: "203.0.113.9", Endpoint: "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{},
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	input, _ := sink.lastFailure()
	if input.StatusCode != 404 || input.Audit.ErrorPhase != "request_validation" {
		t.Fatalf("failure = %+v", input)
	}
	found := false
	for _, call := range audit.metadata {
		if call.label == "normal_model_route_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少 normal_model_route_failed 元数据")
	}
}

func TestPreflight_CandidateFallbackProducesRouteAction(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{Outcome: CandidateOutcomeFallback, Reason: "no_candidate_accounts"},
		}
		s.RuntimeCache = &fakeRuntimeCache{groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: plainPreflightOptions(), StartedAt: 1, TraceID: "trace",
		Endpoint: "POST /v1/chat/completions", RequestSnapshot: UsageRequestSnapshot{},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.IsRouteAction() || result.RouteAction == nil {
		t.Fatal("应返回 RouteAction")
	}
	action := result.RouteAction
	if action.Coordination.Outcome != "hard_exhausted" || action.Coordination.Reason != "no_candidate_accounts" {
		t.Fatalf("coordination = %+v", action.Coordination)
	}
	if action.ServerRetryBudget == nil || action.GatewayRequestWallBudget == nil || action.RouteCoordinationBudget == nil || action.RequestAttemptTracker == nil {
		t.Fatal("RouteAction 预算器缺失")
	}
	if len(action.RoutePlanSnapshot.OrderedAllowedTargets) == 0 || action.RoutePlanSnapshot.OrderedAllowedTargets[0] != "group_1" {
		t.Fatalf("route plan = %+v", action.RoutePlanSnapshot)
	}
	if len(sink.failureInputs) != 0 {
		t.Fatal("RouteAction 不直接写响应（HTTP 路由循环负责终态）")
	}
}

func TestPreflight_SuccessfulDispatchContext(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai", Status: "active"}
	service, _, _ := newTestService(t, func(s *Service) {
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
		s.RuntimeCache = &fakeRuntimeCache{
			groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai", GroupAccessType: "owner"},
			accounts:    []gatewayruntimecache.OpenAIAccountSecret{account},
		}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")

	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: plainPreflightOptions(), StartedAt: 1_700_000_000_000, TraceID: "trace_1",
		Endpoint:        "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{Method: "POST", Path: "/v1/chat/completions"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext == nil {
		t.Fatal("应返回 DispatchContext")
	}
	context := result.DispatchContext
	if len(context.Accounts) != 1 || context.Accounts[0].ID != "acc_1" {
		t.Fatalf("accounts = %+v", context.Accounts)
	}
	if context.UsageContext.GroupID != "group_1" || context.UsageContext.SystemAccountID != "sys_1" {
		t.Fatalf("usageContext = %+v", context.UsageContext)
	}
	if context.UsageContext.RequestedServiceTier != "default" || context.UsageContext.EffectiveServiceTier != "default" {
		t.Fatalf("serviceTier 默认值错误: %+v", context.UsageContext)
	}
	if context.SessionIdentity.SessionID != "session_1" {
		t.Fatalf("sessionIdentity = %+v", context.SessionIdentity)
	}
	if context.GroupSchedulingPolicy != nil {
		t.Fatalf("schedulingPolicy = %v", context.GroupSchedulingPolicy)
	}
	if len(context.ResponseInspectionPolicies) != 0 {
		t.Fatalf("policies = %v", context.ResponseInspectionPolicies)
	}
}

func TestPreflight_ServerRetryBudgetObserverWiring(t *testing.T) {
	budget := NewServerRetryBudget(5_000, newFakeClock(1_000))
	if budget.WaitBudgetMs != 5_000 {
		t.Fatalf("budget = %d", budget.WaitBudgetMs)
	}
	budget.BeginNoAvailableWait(nil)
	fake := newFakeClock(1_500)
	budget2 := NewServerRetryBudget(5_000, fake)
	budget2.BeginNoAvailableWait(nil)
	fake.Advance(1_000)
	if got := budget2.ElapsedMs(nil); got != 1_000 {
		t.Fatalf("elapsed = %d", got)
	}
	if got := budget2.RemainingMs(nil); got != 4_000 {
		t.Fatalf("remaining = %d", got)
	}
	deadline := budget2.DeadlineAtMs(nil)
	if deadline != 6_500 {
		t.Fatalf("deadline = %d", deadline)
	}
	budget2.PauseNoAvailableWait(nil)
	if got := budget2.ElapsedMs(nil); got != 1_000 {
		t.Fatalf("暂停后 elapsed = %d", got)
	}
	if !ShouldHandoffClient(AvailabilityHardExhausted, 0, 1_000) {
		t.Fatal("hard_exhausted 必须移交")
	}
	if ShouldHandoffClient(AvailabilityDispatchableNow, 9_999, 1_000) {
		t.Fatal("dispatchable 不移交")
	}
}

func TestRouteCoordinatorOwnerContract(t *testing.T) {
	service, _, _ := newTestService(t, func(s *Service) {
		s.Candidates = &fakeCandidates{fallbackCandidate: &GroupFallbackCandidate{GroupID: "group_2"}, fallbackFound: true}
		s.RuntimeCache = &fakeRuntimeCache{groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}}
	})
	snapshotValue := gatewayrouting.RoutePlanSnapshot[string]{OrderedAllowedTargets: []string{"group_1", "group_2"}, Cursor: 0}
	snapshot := &snapshotValue
	pendingReason := ""
	var pendingFailure *gatewayrouting.GatewayRouteFinalFailure
	apiKeyRecord := validRuntimeRow()
	groupID := "group_1"
	requestLane := gatewayProtoLane("text")
	compat := ""
	coordinator := newPreflightRouteCoordinator(service, context.Background(), &preflightCoordinatorState{
		apiKeyRecord: &apiKeyRecord, groupID: &groupID, requestLane: &requestLane,
		requestClientCompatibility: &compat, routePlanSnapshot: &snapshot,
		pendingRouteReason: &pendingReason, pendingRouteFailure: &pendingFailure,
	})
	decision, err := coordinator.RequestFallback(context.Background(), "capacity_wait_timeout")
	if err != nil || !decision.Attempted || pendingReason != "capacity_wait_timeout" {
		t.Fatalf("decision = %+v err = %v reason = %q", decision, err, pendingReason)
	}
	if err := coordinator.CompleteFailure(context.Background(), gatewayrouting.GatewayRouteFinalFailure{
		StatusCode: 429, ErrorCode: "gateway_capacity_exhausted", RetryAfterMs: int64Ptr(120),
	}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if pendingFailure == nil || !isTemporarilyBlockedRouteFailure(pendingFailure) {
		t.Fatalf("pendingFailure = %+v", pendingFailure)
	}
}

func TestMergeGatewaySettingsOverride(t *testing.T) {
	base := gatewayruntimecache.GatewaySettings{
		GatewayTextRawBodyLimitMegabytes:     100,
		NoAvailableAccountWaitTimeoutSeconds: 30,
		StreamFailureThresholdCount:          3,
	}
	merged, err := mergeGatewaySettings(base, &gatewayruntimecache.GatewaySettings{
		NoAvailableAccountWaitTimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if merged.GatewayTextRawBodyLimitMegabytes != 100 {
		t.Fatalf("未覆盖字段应保留 base: %+v", merged)
	}
	if merged.NoAvailableAccountWaitTimeoutSeconds != 60 {
		t.Fatalf("覆盖字段应生效: %+v", merged)
	}
	if !merged.StreamCircuitBreakerEnabled {
		t.Fatal("streamCircuitBreakerEnabled 固定为 true")
	}
	if _, err := mergeGatewaySettings(base, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestUniqueActiveRouteGroupIds(t *testing.T) {
	row := &gatewayruntimecache.GatewayAPIKeyRow{GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{GroupID: "g1", Status: "active", GroupEnabled: 1},
		{GroupID: "g2", Status: "active", GroupEnabled: 0},
		{GroupID: "g3", Status: "disabled", GroupEnabled: 1},
		{GroupID: "g1", Status: "active", GroupEnabled: 1},
		{GroupID: "", Status: "active", GroupEnabled: 1},
	}}
	got := uniqueActiveRouteGroupIds(row)
	want := []string{"g1"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got = %v, want %v", got, want)
	}
}

func TestHybridRouteFailureCopy(t *testing.T) {
	cases := map[string]struct {
		message string
		status  int
	}{
		"no_scoring_account":                  {"混合路由评分模型暂不可用：绑定分组池没有可用评分账户", 503},
		"scoring_account_busy":                {"混合路由评分模型暂不可用：评分账户并发已满", 503},
		"hybrid_scoring_failed":               {"混合路由评分模型调用失败", 502},
		"hybrid_scoring_http_error":           {"混合路由评分模型调用失败", 502},
		"hybrid_level_route_missing":          {"混合路由等级配置不可用", 503},
		"hybrid_scoring_fallback_unavailable": {"混合路由评分模型不可用，且低档兜底范围内没有可用目标模型", 503},
		"hybrid_target_group_unavailable":     {"混合路由目标分组暂不可用", 503},
		"unknown":                             {"混合路由暂不可用", 503},
	}
	for reason, want := range cases {
		if got := hybridRouteFailureMessage(reason); got != want.message {
			t.Fatalf("%s message = %q", reason, got)
		}
		if got := hybridRouteFailureStatusCode(reason); got != want.status {
			t.Fatalf("%s status = %d", reason, got)
		}
	}
}

func TestGatewayModelsProviderCodes(t *testing.T) {
	row := &gatewayruntimecache.GatewayAPIKeyRow{GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{ProviderCode: "openai", Status: "active"},
		{ProviderCode: " openai ", Status: "active"},
		{ProviderCode: "anthropic", Status: "active"},
		{ProviderCode: "gemini", Status: "disabled"},
		{ProviderCode: "", Status: "active"},
	}}
	codes := gatewayModelsProviderCodes(row)
	if len(codes) != 2 || codes[0] != "openai" || codes[1] != "anthropic" {
		t.Fatalf("codes = %v", codes)
	}
}

func TestPreflightErrorPropagation(t *testing.T) {
	service, _, _ := newTestService(t, func(s *Service) {
		s.Codex = &fakeCodex{compactErr: errors.New("codex 预检失败")}
		s.RuntimeCache = &fakeRuntimeCache{groupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}}
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{
				Outcome:  CandidateOutcomeAccounts,
				Accounts: []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc_1", ProviderCode: "openai"}},
			},
			preparation: DispatchPreparationResult{
				Outcome:  CandidateOutcomeAccounts,
				Accounts: []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc_1", ProviderCode: "openai"}},
			},
		}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	if _, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options: plainPreflightOptions(), StartedAt: time.Now().UnixMilli(), TraceID: "trace",
		Endpoint: "POST /v1/chat/completions", RequestSnapshot: UsageRequestSnapshot{},
	}); err == nil {
		t.Fatal("codex 预检错误应向上传播")
	}
}

func TestPreflightResultIsRouteAction(t *testing.T) {
	if (PreflightResult{}).IsRouteAction() {
		t.Fatal("空结果不是 RouteAction")
	}
	if !(PreflightResult{RouteAction: &RouteAction{}}).IsRouteAction() {
		t.Fatal("RouteAction 判定失败")
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := New(Service{}); err == nil {
		t.Fatal("缺 RuntimeCache 应报错")
	}
	service := Service{RuntimeCache: &fakeRuntimeCache{}}
	if _, err := New(service); err == nil {
		t.Fatal("缺 Observability 应报错")
	}
	service.Observability = &fakeObservability{}
	built, err := New(service)
	if err != nil || built.Clock == nil {
		t.Fatalf("built = %+v err = %v", built, err)
	}
}
