package gatewaypreauth

// w14h 覆盖波次（第三部分）：PreResolveGatewayRuntime 阶段日志臂、models
// 校验错误臂、黑名单响应的空值回退臂与 sendClientIPBlacklistResponse 聚合键臂。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// w14hValidatorOverride 支持校验错误注入。
type w14hValidatorOverride struct {
	fakeAPIKeyValidator
	err error
}

func (f *w14hValidatorOverride) Validate(ctx context.Context, key string) (*gatewayruntimecache.GatewayAPIKeyRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.fakeAPIKeyValidator.Validate(ctx, key)
}

func TestW14HPreResolveStageArms(t *testing.T) {
	ctx := context.Background()
	// 解析失败 → unexpected_failure 日志分支。
	inspectErr, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &w14hCircuitsOverride{inspectErr: w14hBoomErr}
	})
	failed, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	nextCalled := false
	if err := inspectErr.PreResolveGatewayRuntime(ctx, writer, failed, func() { nextCalled = true }); err == nil {
		t.Fatal("解析错误必须透传")
	}
	if nextCalled {
		t.Fatal("失败后不应调用 next")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("不应写响应=%d", recorder.Code)
	}
	// 未解析 → expected_failure 分支 + recordEarlyGatewayAuthFailure。
	unresolved, _, _ := newTestService(t, nil)
	missing, recorder2, writer2 := newTestRequest("POST", "/v1/chat/completions")
	if err := unresolved.PreResolveGatewayRuntime(ctx, writer2, missing, func() { nextCalled = true }); err != nil {
		t.Fatal(err)
	}
	if recorder2.Code != http.StatusUnauthorized {
		t.Fatalf("缺失令牌=%d", recorder2.Code)
	}
}

func TestW14HModelsValidationArms(t *testing.T) {
	ctx := context.Background()
	// Validate 错误 → 透传。
	validateErr, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{}
		s.APIKeyValidator = &w14hValidatorOverride{err: w14hBoomErr}
	})
	request, _, writer := newTestRequest("GET", "/v1/models")
	request.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := validateErr.ResolveGatewayAPIKeyForModelsAsync(ctx, writer, request, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("校验错误必须透传")
	}
	// 无效 key 且失败记录出错 → rejectMissingOrInvalidGatewayCredential 错误臂。
	invalidRecordErr, _, _ := newTestService(t, func(s *Service) {
		s.APIKeyValidator = &fakeAPIKeyValidator{}
		s.Circuits = &fakeCircuits{recordErr: w14hBoomErr}
	})
	invalid, _, writer2 := newTestRequest("GET", "/v1/models")
	invalid.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := invalidRecordErr.ResolveGatewayAPIKeyForModelsAsync(ctx, writer2, invalid, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("无效 key + 记录错误必须透传")
	}
	// 无效 key 且失败记录触发熔断 → 静默短路。
	invalidBlocked, _, _ := newTestService(t, func(s *Service) {
		s.APIKeyValidator = &fakeAPIKeyValidator{}
		s.Circuits = &fakeCircuits{recordDecision: CircuitDecision{Blocked: true, Reason: "w14h"}}
	})
	invalid2, recorder3, writer3 := newTestRequest("GET", "/v1/models")
	invalid2.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := invalidBlocked.ResolveGatewayAPIKeyForModelsAsync(ctx, writer3, invalid2, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder3.Code != http.StatusTooManyRequests {
		t.Fatalf("熔断短路响应=%d", recorder3.Code)
	}
	// 解析成功后再查黑名单 → 命中 403（runtime 与 models 两条路径）。
	blockedPolicy := ClientIPPolicyDecision{
		Blocked:         true,
		BlacklistPolicy: &BlacklistPolicy{ID: "w14h-policy", Reason: "abuse"},
		NormalizedIP:    &NormalizedClientIP{ClientIP: "203.0.113.9", AggregateIPKey: "agg-w14h"},
	}
	runtimeAfter, _, _ := newTestService(t, func(s *Service) {
		s.IPPolicy = &fakeIPPolicy{decision: blockedPolicy}
	})
	runtimeRequest, recorder4, writer4 := newTestRequest("POST", "/v1/chat/completions")
	runtimeRequest.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := runtimeAfter.ResolveGatewayRuntimeAsync(ctx, writer4, runtimeRequest, ResolveGatewayRuntimeOptions{InspectClientIPPolicyAfterRuntime: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if recorder4.Code != http.StatusForbidden {
		t.Fatalf("解析后封禁=%d", recorder4.Code)
	}
	modelsAfter, _, _ := newTestService(t, func(s *Service) {
		s.IPPolicy = &fakeIPPolicy{decision: blockedPolicy}
	})
	modelsRequest, recorder5, writer5 := newTestRequest("GET", "/v1/models")
	modelsRequest.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := modelsAfter.ResolveGatewayAPIKeyForModelsAsync(ctx, writer5, modelsRequest, ResolveGatewayRuntimeOptions{InspectClientIPPolicyAfterRuntime: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if recorder5.Code != http.StatusForbidden {
		t.Fatalf("models 解析后封禁=%d", recorder5.Code)
	}
	// InspectPreAuthCircuit 错误（models 路径）。
	modelsInspectErr, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &w14hCircuitsOverride{inspectErr: w14hBoomErr}
	})
	modelsCircuit, _, writer6 := newTestRequest("GET", "/v1/models")
	if _, err := modelsInspectErr.ResolveGatewayAPIKeyForModelsAsync(ctx, writer6, modelsCircuit, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("models 熔断检查错误必须透传")
	}
}

func TestW14HBlacklistFallbackArms(t *testing.T) {
	// NormalizedIP 缺失且策略字段部分为空 → 回退到策略字段与 unknown 聚合键。
	policy := ClientIPPolicyDecision{
		Blocked:         true,
		BlacklistPolicy: &BlacklistPolicy{ID: "w14h-policy", Reason: "abuse", AggregateIPKey: "agg-only"},
	}
	service, _, _ := newTestService(t, func(s *Service) {
		s.IPPolicy = &fakeIPPolicy{decision: policy}
	})
	request, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	if _, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, request, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("封禁=%d", recorder.Code)
	}
	// 标记转发已由 marker writer 覆盖；此处补 kernel 包标记的响应写出。
	errRecorder := httptest.NewRecorder()
	errWriter := NewTrackingWriter(errRecorder)
	kernel.MarkUpstreamError(errWriter)
	if errRecorder.Code != http.StatusOK {
		t.Fatalf("标记不应写响应=%d", errRecorder.Code)
	}
}
