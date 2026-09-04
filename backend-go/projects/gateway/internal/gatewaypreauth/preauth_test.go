package gatewaypreauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// pre-auth.ts 的表驱动测试：鉴权拒绝矩阵、熔断短路、封禁与限数响应。

func TestResolveGatewayRuntimeAsync_MissingToken(t *testing.T) {
	circuits := &fakeCircuits{}
	service, obs, _ := newTestService(t, func(s *Service) { s.Circuits = circuits })
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Del("Authorization")

	runtime, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{CloseConnectionOnAuthFailure: true})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if runtime != nil {
		t.Fatal("缺 token 不应返回 runtime")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "缺少访问令牌" || errObject["type"] != "invalid_request_error" {
		t.Fatalf("payload = %v", errObject)
	}
	if connection := recorder.Header().Get("Connection"); connection != "close" {
		t.Fatalf("Connection = %q", connection)
	}
	if len(circuits.preAuthFailures) != 1 || circuits.preAuthFailures[0].Reason != PreAuthFailureMissingBearerToken {
		t.Fatalf("preAuthFailures = %+v", circuits.preAuthFailures)
	}
	if len(obs.eventsByName("gateway_auth_failed")) != 1 {
		t.Fatal("缺少 gateway_auth_failed 日志")
	}
	if req.AuthFailureErrorCode != "" {
		t.Fatalf("纯 401 路径不写 audit copy, got %q", req.AuthFailureErrorCode)
	}
}

func TestResolveGatewayRuntimeAsync_ExtractsAlternateCredentials(t *testing.T) {
	circuits := &fakeCircuits{}
	service, _, _ := newTestService(t, func(s *Service) { s.Circuits = circuits })

	t.Run("x-api-key 来源", func(t *testing.T) {
		req, _, _ := newTestRequest("POST", "/v1/chat/completions")
		req.HTTP.Header.Set("x-api-key", "sk-alt")
		source := service.GatewayPreAuthSource(req, req.Header("authorization"))
		if source != "x-api-key sk-alt" {
			t.Fatalf("source = %q", source)
		}
	})
	t.Run("gemini key 查询参数", func(t *testing.T) {
		req := newBareRequest("POST", "/v1beta/models/m:generateContent?key=g-key")
		key, ok := service.ExtractGatewayAPIKey(req, "")
		if !ok || key != "g-key" {
			t.Fatalf("key = %q %v", key, ok)
		}
		source := service.GatewayPreAuthSource(req, "")
		if source != "gemini-key g-key" {
			t.Fatalf("source = %q", source)
		}
	})
	t.Run("gemini x-goog-api-key 头", func(t *testing.T) {
		req := newBareRequest("POST", "/v1beta/models/m:generateContent")
		req.HTTP.Header.Set("x-goog-api-key", " goog-header ")
		key, ok := service.ExtractGatewayAPIKey(req, "")
		if !ok || key != "goog-header" {
			t.Fatalf("key = %q %v", key, ok)
		}
	})
	t.Run("openai 路径上的 gemini 头不算 gemini native", func(t *testing.T) {
		req := newBareRequest("POST", "/v1/chat/completions")
		req.HTTP.Header.Set("x-goog-api-key", "goog-header")
		if _, ok := service.ExtractGatewayAPIKey(req, ""); ok {
			t.Fatal("openai 路径不应读取 gemini key")
		}
	})
}

func TestResolveGatewayRuntimeAsync_InvalidAPIKey(t *testing.T) {
	circuits := &fakeCircuits{}
	service, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = circuits
		s.RuntimeCache = &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{}}
	})
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer bad-key")

	runtime, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
	if err != nil || runtime != nil {
		t.Fatalf("runtime = %v err = %v", runtime, err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "API Key 无效" {
		t.Fatalf("message = %v", errObject["message"])
	}
	if circuits.preAuthFailures[0].Reason != PreAuthFailureInvalidAPIKey {
		t.Fatalf("reason = %v", circuits.preAuthFailures[0].Reason)
	}
}

func TestResolveGatewayRuntimeAsync_SuccessAndReuse(t *testing.T) {
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{
			"sk-good": validRuntime(),
		}}
	})
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")

	runtime, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
	if err != nil || runtime == nil || runtime.APIKey == nil || runtime.APIKey.ID != "key_1" {
		t.Fatalf("runtime = %+v err = %v", runtime, err)
	}
	// 已解析的 runtime 直接复用（req.gatewayRuntime 语义）。
	req.Runtime = runtime
	cached, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
	if err != nil || cached != runtime {
		t.Fatalf("复用失败: %v %v", cached, err)
	}
}

func TestResolveGatewayRuntimeAsync_PreAuthCircuitBlocked(t *testing.T) {
	retryAfter := int64(30)
	service, obs, _ := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{inspectDecision: CircuitDecision{Blocked: true, Reason: "missing_bearer_token", RetryAfterSeconds: &retryAfter, FailureCount: int64Ptr(40)}}
	})
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Del("Authorization")

	runtime, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
	if err != nil || runtime != nil {
		t.Fatalf("runtime = %v err = %v", runtime, err)
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "当前来源短时间认证失败过多，请稍后重试" {
		t.Fatalf("message = %v", errObject["message"])
	}
	if errObject["code"] != "client_ip_pre_auth_circuit_open" {
		t.Fatalf("code = %v", errObject["code"])
	}
	if recorder.Header().Get("Retry-After") != "30" {
		t.Fatalf("Retry-After = %q", recorder.Header().Get("Retry-After"))
	}
	if len(obs.eventsByName("gateway_pre_auth_error_circuit_blocked")) != 1 {
		t.Fatal("缺少短路日志")
	}
	// 缺 token 分支不应再执行
	if len(obs.eventsByName("gateway_auth_failed")) != 0 {
		t.Fatal("短路后不应继续鉴权失败路径")
	}
}

func TestResolveGatewayRuntimeAsync_RecordFailureOpensCircuit(t *testing.T) {
	retryAfter := int64(10)
	circuits := &fakeCircuits{recordDecision: CircuitDecision{Blocked: true, Reason: "invalid_api_key", RetryAfterSeconds: &retryAfter}}
	service, _, _ := newTestService(t, func(s *Service) { s.Circuits = circuits })
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer bad")

	runtime, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
	if err != nil || runtime != nil {
		t.Fatalf("runtime = %v err = %v", runtime, err)
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "当前来源短时间认证失败过多，请稍后重试" {
		t.Fatalf("message = %v", errObject["message"])
	}
}

func TestRejectCachedClientIPBlacklist(t *testing.T) {
	policy := &fakeIPPolicy{decision: ClientIPPolicyDecision{
		Blocked:         true,
		NormalizedIP:    &NormalizedClientIP{ClientIP: "203.0.113.9", AggregateIPKey: "203.0.113.0/24"},
		BlacklistPolicy: &BlacklistPolicy{ID: "policy_1", IPHash: "hash", Reason: "", ClientIP: "203.0.113.9"},
	}}
	service, obs, _ := newTestService(t, func(s *Service) { s.IPPolicy = policy })
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")

	blocked := service.rejectCachedClientIPBlacklist(context.Background(), writer, req, "203.0.113.9", ResolveGatewayRuntimeOptions{}, true)
	if !blocked {
		t.Fatal("应命中封禁")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	// reason 为空且 aggregateIpKey != clientIp：`当前来源 IP x（封禁范围：r）已被管理员封禁`
	if errObject["message"] != "当前来源 IP 203.0.113.9（封禁范围：203.0.113.0/24）已被管理员封禁" {
		t.Fatalf("message = %v", errObject["message"])
	}
	if len(policy.hits) != 1 || policy.hits[0].ID != "policy_1" {
		t.Fatalf("hits = %+v", policy.hits)
	}
	if len(obs.eventsByName("gateway_client_ip_blacklist_blocked")) != 1 {
		t.Fatal("缺少封禁日志")
	}
}

func TestPreResolveGatewayRuntime_UserRequestLimitAndImageDisabled(t *testing.T) {
	t.Run("用户请求数超限", func(t *testing.T) {
		limit := int64(100)
		retryAfter := int64(45)
		limits := &fakeUserLimits{decision: UserRequestLimitDecision{
			Allowed: false, Window: UserRequestLimitPerMinute, Limit: &limit, RetryAfterSeconds: &retryAfter,
		}}
		service, _, _ := newTestService(t, func(s *Service) {
			s.UserLimits = limits
			s.RuntimeCache = &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": validRuntime()}}
		})
		req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
		req.HTTP.Header.Set("Authorization", "Bearer sk-good")
		called := false
		err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() { called = true })
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if called {
			t.Fatal("超限后不应调用 next")
		}
		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d", recorder.Code)
		}
		errObject := errorBody(t, recorder)
		want := "你的每分钟请求数已达到 100 次，请联系管理员提升额度。"
		if errObject["message"] != want {
			t.Fatalf("message = %v, want %q", errObject["message"], want)
		}
		if recorder.Header().Get("Retry-After") != "45" {
			t.Fatalf("Retry-After = %q", recorder.Header().Get("Retry-After"))
		}
		if req.AuthFailureErrorCode != "user_request_limit_exceeded" {
			t.Fatalf("audit code = %q", req.AuthFailureErrorCode)
		}
	})
	t.Run("窗口中文标签", func(t *testing.T) {
		cases := map[UserRequestLimitWindow]string{
			UserRequestLimitPerMinute: "每分钟",
			UserRequestLimitPerDay:    "每日",
			UserRequestLimitPerWeek:   "每周",
			UserRequestLimitPerMonth:  "每月",
			"other":                   "每月",
		}
		for window, label := range cases {
			if got := userRequestLimitWindowLabel(window); got != label {
				t.Fatalf("window %v label = %q, want %q", window, got, label)
			}
		}
	})
	t.Run("图像生成被禁用", func(t *testing.T) {
		row := validRuntimeRow()
		row.SystemAccountImageGenerationEnabled = 0
		runtime := validRuntime()
		runtime.APIKey = row
		service, _, _ := newTestService(t, func(s *Service) {
			s.RuntimeCache = &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": runtime}}
		})
		req, recorder, writer := newTestRequest("POST", "/v1/images/generations")
		req.HTTP.Header.Set("Authorization", "Bearer sk-good")
		called := false
		err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() { called = true })
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if called {
			t.Fatal("禁用后不应调用 next")
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d", recorder.Code)
		}
		errObject := errorBody(t, recorder)
		if errObject["message"] != ImageGenerationDisabledMessage || errObject["code"] != ImageGenerationDisabledCode {
			t.Fatalf("payload = %v", errObject)
		}
	})
	t.Run("图像启用时放行", func(t *testing.T) {
		service, _, _ := newTestService(t, func(s *Service) {
			s.RuntimeCache = &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": validRuntime()}}
		})
		req, _, writer := newTestRequest("POST", "/v1/images/generations")
		req.HTTP.Header.Set("Authorization", "Bearer sk-good")
		called := false
		if err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() { called = true }); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !called || req.Runtime == nil {
			t.Fatal("应调用 next 并写入 runtime")
		}
	})
	t.Run("models 请求跳过鉴权", func(t *testing.T) {
		service, _, _ := newTestService(t, nil)
		req, _, writer := newTestRequest("GET", "/v1/models")
		called := false
		if err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() { called = true }); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !called || req.Runtime != nil {
			t.Fatal("models 快路只调用 next")
		}
	})
	t.Run("runtime 未解析记录早失败审计", func(t *testing.T) {
		service, _, _ := newTestService(t, func(s *Service) {
			s.RuntimeCache = &fakeRuntimeCache{}
		})
		req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
		req.HTTP.Header.Del("Authorization")
		if err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() {}); err != nil {
			t.Fatalf("err = %v", err)
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", recorder.Code)
		}
		// recordEarlyGatewayAuthFailure 只在 headersSent 后生效；此处尚未发送，故无 audit 派发。
	})
}

func TestDispatchDroppedAuditCapture(t *testing.T) {
	dispatcher := &fakeAuditDispatcher{}
	service, _, _ := newTestService(t, func(s *Service) {
		s.AuditDispatch = dispatcher
	})
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions?apiKey=secret")
	// 模拟响应已写出（headersSent）。
	recorder.WriteHeader(http.StatusUnauthorized)
	writer.WriteHeader(http.StatusUnauthorized)
	req.AuthFailureErrorMessage = "API Key 无效"
	service.recordEarlyGatewayAuthFailure(writer, req)
	if len(dispatcher.dispatched) != 1 {
		t.Fatalf("dispatched = %d", len(dispatcher.dispatched))
	}
	capture := dispatcher.dispatched[0]
	if capture.AuditOutcome != "gateway_failed" || capture.ErrorCode != "invalid_request_error" {
		t.Fatalf("capture = %+v", capture)
	}
	if capture.Path != "/v1/chat/completions" {
		t.Fatalf("path = %q", capture.Path)
	}
	if capture.QueryString != "apiKey=secret" {
		t.Fatalf("queryString = %q", capture.QueryString)
	}
	if capture.ErrorMessage != "API Key 无效" || capture.ErrorPhase != "auth" {
		t.Fatalf("error copy = %+v", capture)
	}
}

func TestIsImageGenerationDisabledForAPIKey(t *testing.T) {
	row := validRuntimeRow()
	if IsImageGenerationDisabledForAPIKey(row, gatewayproto.LaneText) {
		t.Fatal("text lane 不禁用")
	}
	row.SystemAccountImageGenerationEnabled = 1
	if IsImageGenerationDisabledForAPIKey(row, gatewayproto.LaneImage) {
		t.Fatal("显式启用不应禁用")
	}
	row.SystemAccountImageGenerationEnabled = 0
	if !IsImageGenerationDisabledForAPIKey(row, gatewayproto.LaneImage) {
		t.Fatal("未启用应禁用")
	}
	if IsImageGenerationDisabledForAPIKey(nil, gatewayproto.LaneImage) {
		t.Fatal("nil key 不禁用（由 401 分支处理）")
	}
}

func TestResolveGatewayRuntimeAsync_PropagatesCacheError(t *testing.T) {
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{readErr: errors.New("缓存读取失败")}
	})
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")
	if _, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("缓存读取错误应向上传播")
	}
}

func TestResolveGatewayAPIKeyForModelsAsync(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		row := validRuntimeRow()
		service, _, _ := newTestService(t, func(s *Service) {
			s.APIKeyValidator = &fakeAPIKeyValidator{row: row}
		})
		req, _, writer := newTestRequest("GET", "/v1/models")
		req.HTTP.Header.Set("Authorization", "Bearer sk-good")
		apiKey, err := service.ResolveGatewayAPIKeyForModelsAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
		if err != nil || apiKey != row {
			t.Fatalf("apiKey = %v err = %v", apiKey, err)
		}
	})
	t.Run("无效 key", func(t *testing.T) {
		service, _, _ := newTestService(t, func(s *Service) {
			s.APIKeyValidator = &fakeAPIKeyValidator{row: nil}
		})
		req, recorder, writer := newTestRequest("GET", "/v1/models")
		req.HTTP.Header.Set("Authorization", "Bearer bad")
		apiKey, err := service.ResolveGatewayAPIKeyForModelsAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
		if err != nil || apiKey != nil {
			t.Fatalf("apiKey = %v err = %v", apiKey, err)
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", recorder.Code)
		}
		errObject := errorBody(t, recorder)
		if errObject["message"] != "API Key 无效" {
			t.Fatalf("message = %v", errObject["message"])
		}
	})
	t.Run("缺 token", func(t *testing.T) {
		service, _, _ := newTestService(t, nil)
		req, recorder, writer := newTestRequest("GET", "/v1/models")
		req.HTTP.Header.Del("Authorization")
		apiKey, err := service.ResolveGatewayAPIKeyForModelsAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
		if err != nil || apiKey != nil {
			t.Fatalf("apiKey = %v err = %v", apiKey, err)
		}
		errObject := errorBody(t, recorder)
		if errObject["message"] != "缺少访问令牌" {
			t.Fatalf("message = %v", errObject["message"])
		}
	})
}

func TestPreResolveGatewayRuntime_ConcurrentRequests(t *testing.T) {
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": validRuntime()}}
	})
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _, writer := newTestRequest("POST", "/v1/chat/completions")
			req.HTTP.Header.Set("Authorization", "Bearer sk-good")
			called := false
			if err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() { called = true }); err != nil {
				errCh <- err
				return
			}
			if !called {
				errCh <- errors.New("next 未调用")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发请求失败: %v", err)
	}
}

func TestUserRequestLimitConsumeReceivesRuntimeSettings(t *testing.T) {
	limits := &fakeUserLimits{decision: UserRequestLimitDecision{Allowed: true}}
	service, _, _ := newTestService(t, func(s *Service) {
		s.UserLimits = limits
		s.RuntimeCache = &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": validRuntime()}}
	})
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")
	if err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() {}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(limits.inputs) != 1 {
		t.Fatalf("consume 调用 = %d", len(limits.inputs))
	}
	input := limits.inputs[0]
	if input.SystemAccountID != "sys_1" {
		t.Fatalf("systemAccountId = %q", input.SystemAccountID)
	}
	if input.Settings.NoAvailableAccountWaitTimeoutSeconds != 30 {
		t.Fatalf("settings 未透传: %+v", input.Settings)
	}
	if input.Overrides == nil || input.Overrides.PerMinute == nil {
		t.Fatalf("overrides 未透传: %+v", input.Overrides)
	}
	if !strings.Contains(input.SystemAccountID, "sys") {
		t.Fatal("unreachable")
	}
	_ = gatewayruntimecache.RouteStrategyModeNormal
}
