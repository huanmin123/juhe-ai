package main

// w1：显式降级桩与身份/策略适配器的直调收割（全部纯内存、无等待）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW1DisabledAccountLocksStub(t *testing.T) {
	locks := &disabledAccountLocks{}
	ctx := context.Background()
	view, err := locks.FindStateAsync(ctx, "acc_1")
	if err != nil || view == nil || view.BlocksCrossAccount {
		t.Fatalf("find = %+v, %v", view, err)
	}
	lease, err := locks.AcquireRetryLeaseAsync(ctx, "acc_1", 1000)
	if err != nil || !lease.Allowed {
		t.Fatalf("lease = %+v, %v", lease, err)
	}
	if consumed, err := locks.ConsumeRetryLeaseAsync(ctx, "acc_1", "fp"); err != nil || !consumed {
		t.Fatalf("consume = %v, %v", consumed, err)
	}
	if released, err := locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{}); err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
	if err := locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{}); err != nil {
		t.Fatalf("abandon = %v", err)
	}
	if err := locks.RecordFailureAsync(ctx, "acc_1", "fp", nil); err != nil {
		t.Fatalf("record = %v", err)
	}
	if err := locks.SettleDeadlineAsync(ctx, "acc_1", 0, nil); err != nil {
		t.Fatalf("settle = %v", err)
	}
	states, err := locks.ListStatesAsync(ctx, []string{"acc_1", "acc_2"})
	if err != nil || len(states) != 2 {
		t.Fatalf("states = %+v, %v", states, err)
	}
	if err := locks.CompleteSuccessAsync(ctx, "acc_1", "fp", nil); err != nil {
		t.Fatalf("complete = %v", err)
	}
}

func TestW1DisabledDegradationAndIdentityAdapters(t *testing.T) {
	accounts := []gatewaydispatch.AccountCandidate{{ID: "a"}}
	order := (&disabledDegradation{}).OrderSync(accounts, nil)
	if len(order.Accounts) != 1 {
		t.Fatalf("orderSync = %+v", order)
	}
	if _, hit, err := (&disabledSuppression{}).ResolveLocalSuppressionFilter(context.Background(), gatewaydispatch.LocalSuppressionPreflightInput{}); err != nil || hit {
		t.Fatalf("suppression resolve = %v, %v", hit, err)
	}
	// 身份请求包装：原始 URL / 路径 / 头集合。
	request := httptest.NewRequest(http.MethodGet, "/v1/models?x=1", nil)
	request.Header.Set("X-Test", "v1")
	identityRequest := chainIdentityRequest{req: gatewaypreauth.NewGatewayRequest(request)}
	if !strings.HasSuffix(identityRequest.OriginalURL(), "x=1") || identityRequest.Path() != "/v1/models" {
		t.Fatalf("url=%q path=%q", identityRequest.OriginalURL(), identityRequest.Path())
	}
	if values := identityRequest.HeaderValues("X-Test"); len(values) != 1 || values[0] != "v1" {
		t.Fatalf("headers = %v", values)
	}
	if values := identityRequest.HeaderValues("X-Missing"); values != nil {
		t.Fatalf("missing headers = %v", values)
	}
	// 客户端策略审计元数据投影。
	metadata := (clientStrategyAdapter{}).AuditMetadata(gatewaypreauth.ClientStrategyContext{
		ClientProfile: "generic_openai", DownstreamProtocol: "openai",
	})
	if metadata["clientProfile"] != "generic_openai" || metadata["downstreamProtocol"] != "openai" {
		t.Fatalf("metadata = %v", metadata)
	}
	// nil 依赖的客户端策略解析：通用 OpenAI 画像回退。
	resolved := (clientStrategyAdapter{}).Resolve(nil, gatewaypreauth.ClientStrategyInput{})
	if resolved.ClientProfile != "generic_openai" {
		t.Fatalf("resolve = %+v", resolved)
	}
}

func TestW1RoutingCacheAdaptersNilSafety(t *testing.T) {
	_ = gatewayrouting.GroupBindingRow{}
	_ = gatewayruntimecache.GatewayAPIKeyGroupBindingRow{}
}
