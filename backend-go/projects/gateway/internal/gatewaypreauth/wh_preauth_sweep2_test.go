package gatewaypreauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 客户端错误负载的协议映射表（openai/anthropic/gemini 互操作文案）。
func TestWhGatewayErrorProtocolMappings(t *testing.T) {
	anthropicCases := map[string]string{
		"rate_limit_exceeded":   "rate_limit_error",
		"invalid_request_error": "invalid_request_error",
		"server_overloaded":     "overloaded_error",
		"authentication_error":  "authentication_error",
		"permission_error":      "permission_error",
		"not_found_error":       "not_found_error",
		"billing_error":         "billing_error",
		"weird_type":            "api_error",
	}
	for errorType, want := range anthropicCases {
		payload := GatewayErrorPayloadOf("m", errorType)
		if got := anthropicGatewayErrorType(payload); got != want {
			t.Fatalf("anthropic type(%q) = %q, want %q", errorType, got, want)
		}
	}
	if got := anthropicGatewayErrorType(GatewayErrorPayloadOf("m", "other", "server_overloaded")); got != "overloaded_error" {
		t.Fatalf("code 归因 overloaded = %q", got)
	}
	geminiCases := []struct {
		payload GatewayErrorPayload
		want    string
	}{
		{GatewayErrorPayloadOf("m", "rate_limit_exceeded"), "RESOURCE_EXHAUSTED"},
		{GatewayErrorPayloadOf("invalid api key", "invalid_request_error", "invalid_request_error"), "UNAUTHENTICATED"},
		{GatewayErrorPayloadOf("m", "invalid_request_error"), "INVALID_ARGUMENT"},
		{GatewayErrorPayloadOf("m", "authentication_error"), "UNAUTHENTICATED"},
		{GatewayErrorPayloadOf("m", "permission_error"), "PERMISSION_DENIED"},
		{GatewayErrorPayloadOf("m", "forbidden"), "PERMISSION_DENIED"},
		{GatewayErrorPayloadOf("m", "not_found_error"), "NOT_FOUND"},
		{GatewayErrorPayloadOf("m", "billing_error"), "RESOURCE_EXHAUSTED"},
		{GatewayErrorPayloadOf("m", "service_unavailable"), "UNAVAILABLE"},
		{GatewayErrorPayloadOf("m", "other", "gateway_timeout"), "DEADLINE_EXCEEDED"},
		{GatewayErrorPayloadOf("m", "other"), "INTERNAL"},
	}
	for _, c := range geminiCases {
		if got := geminiGatewayErrorStatus(c.payload); got != c.want {
			t.Fatalf("gemini status(%q/%q) = %q, want %q", c.payload.Error.Type, c.payload.Error.Code, got, c.want)
		}
	}
}

// GatewayRequest 的 nil 安全视图方法。
func TestWhGatewayRequestNilSafety(t *testing.T) {
	var req *GatewayRequest
	if req.PathAndQuery() != "/" || req.Path() != "/" || req.Header("x") != "" || req.MethodUpper() != "" {
		t.Fatal("nil 请求视图必须回退默认值")
	}
	if req.BodyState() != nil || req.ParsedJSONObjectBody() != nil {
		t.Fatal("nil 请求无 body 状态")
	}
	empty := &GatewayRequest{HTTP: &http.Request{Method: "GET", URL: &url.URL{}}}
	if got := empty.PathAndQuery(); got != "/" {
		t.Fatalf("空路径 = %q, want /", got)
	}
	if got := empty.Path(); got != "/" {
		t.Fatalf("空 Path = %q, want /", got)
	}
}

// 派发候选窗口诊断元数据进入审计（runtime 提供诊断时）。
func TestWhPreflightDispatchDiagnosticsMetadata(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	runtime := gatewayruntimecache.GatewayRuntime{
		APIKey:      validRuntimeRow(),
		Settings:    gatewayruntimecache.GatewaySettings{NoAvailableAccountWaitTimeoutSeconds: 30},
		GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		AccountDispatchDiagnostics: &gatewayruntimecache.OpenAIAccountsForGroupDiagnostics{
			ScanLimit: 500, FinalLimit: 200, CandidateRowCount: 10, ScannedRowCount: 9,
			EligibleRowCount: 3, HydrationBatchCount: 1, HydratedAccountCount: 2,
			HydrationDroppedCount: 0, FinalAccountCount: 1, ScanLimitReached: false,
		},
	}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{
			runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": runtime},
			groupAccess:  runtime.GroupAccess,
			accounts:     []gatewayruntimecache.OpenAIAccountSecret{account},
		}
		s.Candidates = whFullDispatchCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
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
		if call.label == "account_dispatch_candidate_window" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少候选窗口诊断元数据")
	}
}

// 候选过滤/派发准备的错误与完成分支。
func TestWhPreflightCandidateOutcomeBranches(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	// 过滤错误传播。
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &fakeCandidates{filterErr: errors.New("过滤失败")}
	})
	req, _, writer := whAuthorizedChatRequest()
	if _, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	}); err == nil {
		t.Fatal("过滤错误必须传播")
	}
	// 过滤完成（无路由失败）→ 就地返回。
	service2, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &fakeCandidates{filterResult: CandidateFilterResult{Outcome: CandidateOutcomeCompleted}}
	})
	req2, _, writer2 := whAuthorizedChatRequest()
	result2, err := service2.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req2, Res: writer2, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result2.DispatchContext != nil || result2.RouteAction != nil {
		t.Fatalf("过滤完成 = %+v err=%v", result2, err)
	}
	// 派发准备错误传播。
	service3, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{Outcome: CandidateOutcomeAccounts, Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}},
			prepareErr:   errors.New("准备失败"),
		}
	})
	req3, _, writer3 := whAuthorizedChatRequest()
	if _, err := service3.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req3, Res: writer3, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	}); err == nil {
		t.Fatal("准备错误必须传播")
	}
	// 设置读取错误传播：带 Identity 的入口从缓存读设置。
	service4, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &whSettingsErrCache{fakeRuntimeCache: whRuntimeCacheFor(nil)}
	})
	req4, _, writer4 := newTestRequest("POST", "/v1/chat/completions")
	if _, err := service4.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req4, Res: writer4, AuditCapture: &fakeAuditCapture{}, Options: plainPreflightOptions(),
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	}); err == nil {
		t.Fatal("设置读取错误必须传播")
	}
}

// whSettingsErrCache 注入设置读取错误。
type whSettingsErrCache struct {
	*fakeRuntimeCache
}

func (c *whSettingsErrCache) ReadCachedGatewaySettingsAsync(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, errors.New("wh 注入设置读取失败")
}

// whPreparingCandidates 在派发准备完成前写入路由终态失败。
type whPreparingCandidates struct {
	fakeCandidates
	failure gatewayroutingFailure
}

type gatewayroutingFailure = gatewayrouting.GatewayRouteFinalFailure

func (f *whPreparingCandidates) PrepareDispatchAccounts(ctx context.Context, input DispatchPreparationInput) (DispatchPreparationResult, error) {
	_ = input.RouteCoordinator.CompleteFailure(ctx, f.failure)
	return DispatchPreparationResult{Outcome: CandidateOutcomeCompleted}, nil
}

// 派发准备阶段协调器终态：Completed + 失败 → RouteAction。
func TestWhPreflightPreparationCoordinatorFailure(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &whPreparingCandidates{
			fakeCandidates: fakeCandidates{
				filterResult: CandidateFilterResult{Outcome: CandidateOutcomeAccounts, Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}},
			},
			failure: gatewayroutingFailure{StatusCode: 429, ErrorCode: "capacity"},
		}
	})
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsRouteAction() || result.RouteAction.Coordination.Outcome != "temporarily_blocked" {
		t.Fatalf("RouteAction = %+v", result.RouteAction)
	}
}

// whCodexCompact 触发派发账号回调并按配置就地完成。
type whCodexCompact struct {
	fakeCodex
	complete bool
	account  gatewayAccountFixtureAlias
	sawCall  *bool
}

type gatewayAccountFixtureAlias = gatewayruntimecache.OpenAIAccountSecret

func (f *whCodexCompact) ApplyChatBridgeCompactPreflight(ctx context.Context, input CodexCompactPreflightInput) (CodexCompactPreflightResult, error) {
	if input.OnDispatchedAccount != nil && len(input.DispatchAccounts) > 0 {
		input.OnDispatchedAccount(input.DispatchAccounts[0])
	}
	if f.complete {
		return CodexCompactPreflightResult{Completed: true, Accounts: input.DispatchAccounts}, nil
	}
	return f.fakeCodex.ApplyChatBridgeCompactPreflight(ctx, input)
}

// whPoliciesErrCache 注入策略列表错误。
type whPoliciesErrCache struct {
	*fakeRuntimeCache
}

func (c *whPoliciesErrCache) ListCachedActiveResponseInspectionPoliciesForAccountsAsync(context.Context, []gatewayruntimecache.OpenAIAccountSecret) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return nil, errors.New("wh 注入策略读取失败")
}

// 热门质量探索结算与压缩预检完成/策略读取错误的清理路径。
func TestWhPreflightSettleAndCleanupPaths(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	settled := []string{}
	// 1. 派发账号命中探索预留 → 结算 dispatched。
	called := false
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{Outcome: CandidateOutcomeAccounts, Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}},
			preparation: DispatchPreparationResult{
				Outcome:                    CandidateOutcomeAccounts,
				Accounts:                   []gatewayruntimecache.OpenAIAccountSecret{account},
				ReleaseClientIPConcurrency: func() {},
				SettleHotQualityExplorationAfterDispatch: func(outcome string) error {
					settled = append(settled, outcome)
					return nil
				},
				HotQualityExplorationReservation: &HotQualityExplorationReservation{AccountRuntimeKey: "acc_1"},
			},
		}
		s.Codex = &whCodexCompact{
			account: account,
			sawCall: &called,
		}
	})
	req, _, writer := whAuthorizedChatRequest()
	if _, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	}); err != nil {
		t.Fatal(err)
	}
	if len(settled) != 1 || settled[0] != "dispatched" {
		t.Fatalf("结算 = %v", settled)
	}

	// 2. 压缩预检就地完成 → 结算 not_dispatched 并释放并发占位。
	released := false
	settled2 := []string{}
	service2, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{Outcome: CandidateOutcomeAccounts, Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}},
			preparation: DispatchPreparationResult{
				Outcome:                    CandidateOutcomeAccounts,
				Accounts:                   []gatewayruntimecache.OpenAIAccountSecret{account},
				ReleaseClientIPConcurrency: func() { released = true },
				SettleHotQualityExplorationAfterDispatch: func(outcome string) error {
					settled2 = append(settled2, outcome)
					return nil
				},
			},
		}
		s.Codex = &whCodexCompact{complete: true}
	})
	req2, _, writer2 := whAuthorizedChatRequest()
	result2, err := service2.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req2, Res: writer2, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result2.DispatchContext != nil {
		t.Fatalf("压缩完成 = %+v err=%v", result2, err)
	}
	if !released || len(settled2) != 1 || settled2[0] != "not_dispatched" {
		t.Fatalf("清理路径 released=%v settled=%v", released, settled2)
	}

	// 3. 策略读取错误 → 结算并传播错误。
	service3, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &whPoliciesErrCache{fakeRuntimeCache: whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})}
		s.Candidates = whFullDispatchCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
	})
	req3, _, writer3 := whAuthorizedChatRequest()
	if _, err := service3.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req3, Res: writer3, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	}); err == nil {
		t.Fatal("策略读取错误必须传播")
	}
}

// codex/claude_code/gemini_cli 画像写入 client_strategy 审计元数据。
func TestWhPreflightClientStrategyAuditMetadata(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1", ProviderCode: "openai"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = whFullDispatchCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
		s.ClientStrategy = &fakeClientStrategy{strategy: ClientStrategyContext{ClientProfile: "codex"}}
	})
	audit := &fakeAuditCapture{}
	req, _, writer := whAuthorizedChatRequest()
	if _, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range audit.metadata {
		if call.label == "client_strategy" {
			found = true
		}
	}
	if !found {
		t.Fatal("codex 画像缺少审计元数据")
	}
}

func httptestNewRequest(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}
