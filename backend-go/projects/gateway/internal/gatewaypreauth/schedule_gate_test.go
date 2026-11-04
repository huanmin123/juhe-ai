package gatewaypreauth

// 时间计划闸门与账户状态解耦（docs/functions/时间计划闸门与账户状态解耦设计.md
// §5.2）：Key 有效但不在可用时段时返回 401 api_key_outside_schedule，与
// 「API Key 无效」区分，且不计入认证前熔断（时段外是合法密钥的短暂限制）。

import (
	"context"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestResolveGatewayRuntimeAsync_APIKeyOutsideSchedule(t *testing.T) {
	circuits := &fakeCircuits{}
	service, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = circuits
		s.RuntimeCache = &fakeRuntimeCache{readErr: gatewayruntimecache.ErrAPIKeyOutsideSchedule}
	})
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	req.HTTP.Header.Set("Authorization", "Bearer sk-outside-window")

	runtime, err := service.ResolveGatewayRuntimeAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
	if err != nil || runtime != nil {
		t.Fatalf("runtime = %v err = %v", runtime, err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "API Key 当前不在可用时段" || errObject["type"] != "invalid_request_error" ||
		errObject["code"] != "api_key_outside_schedule" {
		t.Fatalf("错误载荷不一致：%v", errObject)
	}
	// 时段外不得计入认证前熔断：合法密钥不应在窗口关闭期间被短期封禁。
	if len(circuits.preAuthFailures) != 0 {
		t.Fatalf("时段外不得记录认证失败：%v", circuits.preAuthFailures)
	}
	// 审计必须记录真实原因：否则 FinalizeGatewayAuthFailureAudit 在 Bearer 存在
	// 时会把失败文案回填成「API Key 无效」，排查日志再次误导。
	if req.AuthFailureErrorCode != "api_key_outside_schedule" || req.AuthFailureErrorMessage != "API Key 当前不在可用时段" {
		t.Fatalf("审计字段 = (%q, %q)，want (api_key_outside_schedule, API Key 当前不在可用时段)",
			req.AuthFailureErrorCode, req.AuthFailureErrorMessage)
	}
}

func TestResolveGatewayAPIKeyForModelsAsync_APIKeyOutsideSchedule(t *testing.T) {
	circuits := &fakeCircuits{}
	service, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = circuits
		s.APIKeyValidator = &fakeAPIKeyValidator{err: gatewayruntimecache.ErrAPIKeyOutsideSchedule}
	})
	req, recorder, writer := newTestRequest("GET", "/v1/models")
	req.HTTP.Header.Set("Authorization", "Bearer sk-outside-window")

	row, err := service.ResolveGatewayAPIKeyForModelsAsync(context.Background(), writer, req, ResolveGatewayRuntimeOptions{})
	if err != nil || row != nil {
		t.Fatalf("row = %v err = %v", row, err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["code"] != "api_key_outside_schedule" {
		t.Fatalf("错误码不一致：%v", errObject)
	}
	if len(circuits.preAuthFailures) != 0 {
		t.Fatalf("时段外不得记录认证失败：%v", circuits.preAuthFailures)
	}
}
