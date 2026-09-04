package gatewaypreauth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// authorization-preflight.ts 的表驱动测试：401/403/429 拒绝矩阵与配额顺序。

func TestRejectUnavailableGatewayAPIKey(t *testing.T) {
	t.Run("可用时跳过", func(t *testing.T) {
		service, _, sink := newTestService(t, nil)
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected := service.RejectUnavailableGatewayAPIKey(UnavailableAPIKeyInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			APIKeyUnavailable: false,
		})
		if rejected || len(sink.failureInputs) != 0 {
			t.Fatal("可用 key 不应拒绝")
		}
	})
	t.Run("不可用时 401", func(t *testing.T) {
		service, _, sink := newTestService(t, nil)
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		audit := &fakeAuditCapture{}
		rejected := service.RejectUnavailableGatewayAPIKey(UnavailableAPIKeyInput{
			Req: req, Res: writer, AuditCapture: audit,
			StartedAt: 1, APIKeyUnavailable: true,
		})
		if !rejected {
			t.Fatal("应拒绝")
		}
		input, ok := sink.lastFailure()
		if !ok || input.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failure = %+v", input)
		}
		if input.ResponsePayload.Error.Message != "API Key 不可用或已过期" || input.ResponsePayload.Error.Type != "invalid_api_key" {
			t.Fatalf("payload = %+v", input.ResponsePayload)
		}
		if input.Audit.ErrorPhase != "authorization" || input.Audit.ErrorCode != "invalid_api_key" {
			t.Fatalf("audit = %+v", input.Audit)
		}
	})
}

func TestRejectMissingGatewayGroupAccess(t *testing.T) {
	t.Run("有 groupAccess 时跳过", func(t *testing.T) {
		service, _, sink := newTestService(t, nil)
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected := service.RejectMissingGatewayGroupAccess(MissingGroupAccessInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		})
		if rejected || len(sink.failureInputs) != 0 {
			t.Fatal("有授权不应拒绝")
		}
	})
	t.Run("缺失时 403", func(t *testing.T) {
		service, _, sink := newTestService(t, nil)
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected := service.RejectMissingGatewayGroupAccess(MissingGroupAccessInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			StartedAt: 1, GroupAccess: nil,
		})
		if !rejected {
			t.Fatal("应拒绝")
		}
		input, _ := sink.lastFailure()
		if input.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d", input.StatusCode)
		}
		if input.ResponsePayload.Error.Message != "API Key 绑定的分组授权不可用" || input.ResponsePayload.Error.Type != "forbidden" {
			t.Fatalf("payload = %+v", input.ResponsePayload)
		}
		if input.Audit.ErrorMessage != "API Key 绑定的分组授权不可用" || input.Audit.ErrorCode != "forbidden" {
			t.Fatalf("audit = %+v", input.Audit)
		}
	})
}

func TestRejectGatewayAPIKeyQuotaIfExceeded(t *testing.T) {
	t.Run("额度已用完 429", func(t *testing.T) {
		quota := &fakeAPIKeyQuota{decision: gatewayquota.DeniedDecision("额度已用完，请联系管理员提升额度")}
		service, _, sink := newTestService(t, func(s *Service) { s.APIKeyQuota = quota })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected, err := service.RejectGatewayAPIKeyQuotaIfExceeded(context.Background(), APIKeyQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			StartedAt: 1, APIKeyRecord: validRuntimeRow(),
			UsageContext: GatewayFailureUsageContext{ProviderCode: "openai"},
		})
		if err != nil || !rejected {
			t.Fatalf("rejected = %v err = %v", rejected, err)
		}
		input, _ := sink.lastFailure()
		if input.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d", input.StatusCode)
		}
		if input.ResponsePayload.Error.Message != "额度已用完，请联系管理员提升额度" {
			t.Fatalf("message = %v", input.ResponsePayload.Error.Message)
		}
		if input.Audit.ErrorPhase != "quota" || input.Audit.ErrorCode != "rate_limit_exceeded" {
			t.Fatalf("audit = %+v", input.Audit)
		}
	})
	t.Run("在途额度超限 429", func(t *testing.T) {
		inflight := &fakeInflight{decision: gatewayquota.InflightDecision{Allowed: false, EstimatedCostUsd: 0.5, HasEstimatedCostUsd: true}}
		service, _, sink := newTestService(t, func(s *Service) { s.InflightQuota = inflight })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected, err := service.RejectGatewayAPIKeyQuotaIfExceeded(context.Background(), APIKeyQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			StartedAt: 1, APIKeyRecord: validRuntimeRow(),
			UsageContext: GatewayFailureUsageContext{ProviderCode: "openai"},
		})
		if err != nil || !rejected {
			t.Fatalf("rejected = %v err = %v", rejected, err)
		}
		input, _ := sink.lastFailure()
		if input.Audit.ErrorCode != "api_key_inflight_quota_exceeded" {
			t.Fatalf("errorCode = %q", input.Audit.ErrorCode)
		}
		if input.ResponsePayload.Error.Message != APIKeyQuotaExceededMessage {
			t.Fatalf("message = %v", input.ResponsePayload.Error.Message)
		}
	})
	t.Run("预留成功并登记完成回调", func(t *testing.T) {
		reservation := &gatewayquota.InflightReservation{}
		inflight := &fakeInflight{decision: gatewayquota.InflightDecision{Allowed: true, Reservation: reservation}}
		service, _, sink := newTestService(t, func(s *Service) { s.InflightQuota = inflight })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected, err := service.RejectGatewayAPIKeyQuotaIfExceeded(context.Background(), APIKeyQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			StartedAt: 1, APIKeyRecord: validRuntimeRow(),
			UsageContext: GatewayFailureUsageContext{ProviderCode: "openai"},
		})
		if err != nil || rejected {
			t.Fatalf("rejected = %v err = %v", rejected, err)
		}
		if !req.InflightQuotaReserved {
			t.Fatal("应标记已预留")
		}
		if len(sink.failureInputs) != 0 {
			t.Fatal("不应有失败响应")
		}
		// 二次调用幂等（WeakSet 语义）。
		rejected, err = service.RejectGatewayAPIKeyQuotaIfExceeded(context.Background(), APIKeyQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			StartedAt: 2, APIKeyRecord: validRuntimeRow(),
		})
		if err != nil || rejected {
			t.Fatalf("二次调用 rejected = %v err = %v", rejected, err)
		}
	})
	t.Run("无 apiKeyRecord 时跳过", func(t *testing.T) {
		quota := &fakeAPIKeyQuota{}
		service, _, sink := newTestService(t, func(s *Service) { s.APIKeyQuota = quota })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected, err := service.RejectGatewayAPIKeyQuotaIfExceeded(context.Background(), APIKeyQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, StartedAt: 1,
		})
		if err != nil || rejected || len(quota.rows) != 0 {
			t.Fatalf("rejected = %v err = %v rows = %d", rejected, err, len(quota.rows))
		}
		if len(sink.failureInputs) != 0 {
			t.Fatal("不应有失败响应")
		}
	})
	t.Run("检查错误向上传播", func(t *testing.T) {
		quota := &fakeAPIKeyQuota{err: errors.New("配额服务不可用")}
		service, _, _ := newTestService(t, func(s *Service) { s.APIKeyQuota = quota })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		if _, err := service.RejectGatewayAPIKeyQuotaIfExceeded(context.Background(), APIKeyQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			APIKeyRecord: validRuntimeRow(),
		}); err == nil {
			t.Fatal("错误应向上传播")
		}
	})
}

func TestRejectGatewayAuthorizationQuotaIfExceeded(t *testing.T) {
	t.Run("通过", func(t *testing.T) {
		authz := &fakeAuthorizationQuota{decision: gatewayquota.AllowedDecision()}
		service, _, sink := newTestService(t, func(s *Service) { s.AuthorizationQuota = authz })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected, err := service.RejectGatewayAuthorizationQuotaIfExceeded(context.Background(), AuthorizationQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, StartedAt: 1,
			GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		})
		if err != nil || rejected {
			t.Fatalf("rejected = %v err = %v", rejected, err)
		}
		if len(sink.failureInputs) != 0 {
			t.Fatal("不应有失败响应")
		}
	})
	t.Run("授权额度超限 429", func(t *testing.T) {
		authz := &fakeAuthorizationQuota{decision: gatewayquota.DeniedDecision("")}
		service, _, sink := newTestService(t, func(s *Service) { s.AuthorizationQuota = authz })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected, err := service.RejectGatewayAuthorizationQuotaIfExceeded(context.Background(), AuthorizationQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, StartedAt: 1,
			GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{
				ProviderCode:                   "openai",
				GroupAuthorizationID:           strPtr("authz_1"),
				GroupAuthorizationQuotaLimited: boolRef(true),
			},
		})
		if err != nil || !rejected {
			t.Fatalf("rejected = %v err = %v", rejected, err)
		}
		input, _ := sink.lastFailure()
		if input.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d", input.StatusCode)
		}
		if input.ResponsePayload.Error.Message != AuthorizationQuotaExceededMessage {
			t.Fatalf("message = %v", input.ResponsePayload.Error.Message)
		}
		if len(authz.calls) != 1 || authz.calls[0].GroupAuthorizationID != "authz_1" || !authz.calls[0].GroupAuthorizationQuotaLimited {
			t.Fatalf("calls = %+v", authz.calls)
		}
	})
	t.Run("自定义拒绝消息", func(t *testing.T) {
		authz := &fakeAuthorizationQuota{decision: gatewayquota.DeniedDecision("自定义额度文案")}
		service, _, sink := newTestService(t, func(s *Service) { s.AuthorizationQuota = authz })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		rejected, err := service.RejectGatewayAuthorizationQuotaIfExceeded(context.Background(), AuthorizationQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{},
		})
		if err != nil || !rejected {
			t.Fatalf("rejected = %v err = %v", rejected, err)
		}
		input, _ := sink.lastFailure()
		if input.ResponsePayload.Error.Message != "自定义额度文案" {
			t.Fatalf("message = %v", input.ResponsePayload.Error.Message)
		}
	})
	t.Run("检查错误向上传播", func(t *testing.T) {
		authz := &fakeAuthorizationQuota{err: errors.New("授权配额查询失败")}
		service, _, _ := newTestService(t, func(s *Service) { s.AuthorizationQuota = authz })
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		if _, err := service.RejectGatewayAuthorizationQuotaIfExceeded(context.Background(), AuthorizationQuotaInput{
			Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
			GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{},
		}); err == nil {
			t.Fatal("错误应向上传播")
		}
	})
}

func strPtr(value string) *string { return &value }

func boolRef(value bool) *bool { return &value }
