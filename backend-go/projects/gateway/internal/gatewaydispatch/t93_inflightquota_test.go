package gatewaydispatch

// T93-1（C3-H1）+ T93-4（C3-H8）派发期在途预占钩子单元测试：
//   - 预占使用账户 vendor（account.VendorCode）与上游映射模型；
//   - 请求体字节与 max_output_tokens 取最终上游请求事实；
//   - 授权 scope 身份来自 usage context / 账户（分组授权 + 账户授权）；
//   - 拒绝返回 GatewayInflightQuotaExceededError（429 契约），
//   - 一次请求只预留一次（幂等），释放挂到请求 ctx。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

type fakeInflightQuotaPort struct {
	inputs  []InflightQuotaReserveInput
	outcome InflightQuotaReserveOutcome
	err     error
}

func (f *fakeInflightQuotaPort) ReserveUpstreamRequest(_ context.Context, input InflightQuotaReserveInput) (InflightQuotaReserveOutcome, error) {
	f.inputs = append(f.inputs, input)
	if f.err != nil {
		return InflightQuotaReserveOutcome{}, f.err
	}
	return f.outcome, nil
}

func t93Account() AccountCandidate {
	authorizationID := "aa1"
	return AccountCandidate{
		ID:                          "acc_1",
		VendorCode:                  "xai",
		AccountOwnerSystemAccountID: "owner_sys",
		AccountAuthorizationID:      &authorizationID,
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
			UpstreamModel:          "grok-upstream-model",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
		}},
	}
}

func t93Request(t *testing.T, raw string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("parse fixture body: %v", err)
	}
	req.Body = &gatewaybody.Request{RawBody: []byte(raw), Body: parsed}
	return req
}

func TestT93ReserveUsesUpstreamModelAndVendor(t *testing.T) {
	port := &fakeInflightQuotaPort{outcome: InflightQuotaReserveOutcome{Allowed: true}}
	engine := &Engine{InflightQuota: port}
	raw := `{"model":"client-model","max_tokens":77,"messages":[{"role":"user","content":"hi"}]}`
	req := t93Request(t, raw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := engine.reserveUpstreamInflightQuota(ctx, req, gatewaypreauth.GatewayFailureUsageContext{
		SystemAccountID:      "caller_sys",
		APIKeyID:             "key_1",
		GroupID:              "group_1",
		GroupAuthorizationID: "ga1",
	}, t93Account(), []byte(raw), "priority")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(port.inputs) != 1 {
		t.Fatalf("reserve calls = %d, want 1", len(port.inputs))
	}
	input := port.inputs[0]
	if input.ProviderCode != "xai" {
		t.Fatalf("providerCode = %q, want account vendor xai", input.ProviderCode)
	}
	if input.Model != "grok-upstream-model" {
		t.Fatalf("model = %q, want the mapped upstream model", input.Model)
	}
	if input.ServiceTier != "priority" {
		t.Fatalf("serviceTier = %q", input.ServiceTier)
	}
	if input.BodyBytes != int64(len(raw)) {
		t.Fatalf("bodyBytes = %d, want %d", input.BodyBytes, len(raw))
	}
	if !input.HasMaxOutputTokens || input.MaxOutputTokens != 77 {
		t.Fatalf("maxOutputTokens = %v/%d, want 77", input.HasMaxOutputTokens, input.MaxOutputTokens)
	}
	if input.GroupAuthorizationID != "ga1" || input.AccountAuthorizationID != "aa1" {
		t.Fatalf("authorization scopes = %q/%q", input.GroupAuthorizationID, input.AccountAuthorizationID)
	}
	// 账户所有者优先作为计价目录 scope（与全链 finalization 同源）。
	if input.SystemAccountID != "owner_sys" {
		t.Fatalf("systemAccountID = %q, want account owner", input.SystemAccountID)
	}
	if input.APIKeyID != "key_1" || input.GroupID != "group_1" || input.AccountID != "acc_1" {
		t.Fatalf("identity = %+v", input)
	}
}

func TestT93DenialReturnsQuotaError(t *testing.T) {
	port := &fakeInflightQuotaPort{outcome: InflightQuotaReserveOutcome{
		Allowed:     false,
		DeniedKind:  InflightDeniedAuthorization,
		EstimateUSD: 0.5,
		HasEstimate: true,
	}}
	engine := &Engine{InflightQuota: port}
	raw := `{"model":"client-model"}`
	req := t93Request(t, raw)

	err := engine.reserveUpstreamInflightQuota(context.Background(), req, gatewaypreauth.GatewayFailureUsageContext{APIKeyID: "key_1"}, t93Account(), []byte(raw), "default")
	var quotaErr *GatewayInflightQuotaExceededError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("err = %v, want GatewayInflightQuotaExceededError", err)
	}
	if quotaErr.DeniedKind != InflightDeniedAuthorization || !quotaErr.HasEstimate || quotaErr.EstimateUSD != 0.5 {
		t.Fatalf("quota error = %+v", quotaErr)
	}
	status, errorType, code, message := quotaErr.GatewayQuotaRejection()
	if status != http.StatusTooManyRequests || errorType != "rate_limit_exceeded" ||
		code != "rate_limit_exceeded" || message != gatewaypreauth.AuthorizationQuotaExceededMessage {
		t.Fatalf("authorization rejection = %d/%s/%s/%s", status, errorType, code, message)
	}
	if req.InflightQuotaReserved {
		t.Fatal("拒绝不得标记已预留")
	}
}

func TestT93APIKeyDenialUsesAPIKeyContract(t *testing.T) {
	status, errorType, code, message := (&GatewayInflightQuotaExceededError{DeniedKind: InflightDeniedAPIKey}).GatewayQuotaRejection()
	if status != http.StatusTooManyRequests || errorType != "rate_limit_exceeded" ||
		code != "api_key_inflight_quota_exceeded" || message != gatewaypreauth.APIKeyQuotaExceededMessage {
		t.Fatalf("api key rejection = %d/%s/%s/%s", status, errorType, code, message)
	}
}

func TestT93ReservationIsOncePerRequestAndReleasesOnContext(t *testing.T) {
	released := make(chan struct{}, 1)
	port := &fakeInflightQuotaPort{outcome: InflightQuotaReserveOutcome{
		Allowed:    true,
		OnComplete: func() { released <- struct{}{} },
	}}
	engine := &Engine{InflightQuota: port}
	raw := `{"model":"client-model"}`
	req := t93Request(t, raw)
	ctx, cancel := context.WithCancel(context.Background())
	usageContext := gatewaypreauth.GatewayFailureUsageContext{APIKeyID: "key_1"}

	if err := engine.reserveUpstreamInflightQuota(ctx, req, usageContext, t93Account(), []byte(raw), "default"); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if !req.InflightQuotaReserved {
		t.Fatal("预留成功必须标记请求级幂等位")
	}
	if err := engine.reserveUpstreamInflightQuota(ctx, req, usageContext, t93Account(), []byte(raw), "default"); err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if len(port.inputs) != 1 {
		t.Fatalf("reserve calls = %d, want 1 (one reservation per request)", len(port.inputs))
	}

	cancel()
	<-released
}

func TestT93ReserveSkippedWithoutPortOrModel(t *testing.T) {
	raw := `{"model":"client-model"}`
	// 端口缺席：不预留（preflight 只保留已结算额度拒绝）。
	if err := (&Engine{}).reserveUpstreamInflightQuota(context.Background(), t93Request(t, raw), gatewaypreauth.GatewayFailureUsageContext{}, t93Account(), []byte(raw), "default"); err != nil {
		t.Fatalf("nil port: %v", err)
	}
	// 无模型：不预留。
	port := &fakeInflightQuotaPort{outcome: InflightQuotaReserveOutcome{Allowed: true}}
	engine := &Engine{InflightQuota: port}
	req := t93Request(t, `{"messages":[]}`)
	if err := engine.reserveUpstreamInflightQuota(context.Background(), req, gatewaypreauth.GatewayFailureUsageContext{}, t93Account(), []byte(`{"messages":[]}`), "default"); err != nil {
		t.Fatalf("no model: %v", err)
	}
	if len(port.inputs) != 0 {
		t.Fatalf("无模型不得预留: %+v", port.inputs)
	}
}

func TestT93ReservePropagatesPortError(t *testing.T) {
	port := &fakeInflightQuotaPort{err: errors.New("quota store down")}
	engine := &Engine{InflightQuota: port}
	raw := `{"model":"client-model"}`
	err := engine.reserveUpstreamInflightQuota(context.Background(), t93Request(t, raw), gatewaypreauth.GatewayFailureUsageContext{}, t93Account(), []byte(raw), "default")
	if err == nil || err.Error() != "quota store down" {
		t.Fatalf("err = %v, want the original port error", err)
	}
}
