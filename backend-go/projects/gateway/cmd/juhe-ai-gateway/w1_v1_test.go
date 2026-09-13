package main

// w1: chain_v1.go 的纯函数与投影直测（speed-first 提取器、耗尽分类、
// db-service 不可用识别、审计捕获桥）。编排循环状态不在此覆盖。

import (
	contextLib "context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW1ChainSpeedFirstProjectionHelpers(t *testing.T) {
	// chainSpeedFirstMaxRetriesOf：nil 配置 = 0。
	if got := chainSpeedFirstMaxRetriesOf(&gatewaypreauth.DispatchContext{}); got != 0 {
		t.Fatalf("nil config retries = %d", got)
	}
	configured := chainSpeedFirstMaxRetriesOf(&gatewaypreauth.DispatchContext{
		NormalRouteSpeedFirstConfig: w4bSpeedFirstConfig(8_000, map[string]any{"maxFirstByteRetriesPerRequest": 3}),
	})
	if configured != 3 {
		t.Fatalf("configured retries = %d", configured)
	}
	// chainAccountIDsOf。
	ids := chainAccountIDsOf([]gatewaydispatch.AccountCandidate{{ID: "a"}, {ID: "b"}})
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v", ids)
	}
	// chainFirstByteConfigOf。
	if chainFirstByteConfigOf(nil) != nil {
		t.Fatal("nil first-byte config 必须投影 nil")
	}
	deadline := int64(900)
	projected := chainFirstByteConfigOf(&gatewaypreauth.NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: "speed_first", FirstByteDeadlineMs: &deadline,
	})
	if projected == nil || projected.FirstByteDeadlineMs != 900 || projected.SchedulingPreference != "speed_first" {
		t.Fatalf("projected = %+v", projected)
	}
	// chainSchedulingPolicyValueOf。
	if chainSchedulingPolicyValueOf(nil) != nil {
		t.Fatal("nil policy 必须 nil")
	}
	policy := gatewayruntimecache.GroupSchedulingPolicy{"schedulingPreference": "speed_first"}
	if chainSchedulingPolicyValueOf(&policy)["schedulingPreference"] != "speed_first" {
		t.Fatal("policy 值丢失")
	}
	// chainRuntimeKeyOfCandidate。
	if got := chainRuntimeKeyOfCandidate(gatewaydispatch.AccountCandidate{ID: "acc_1"}); got != "acc_1" {
		t.Fatalf("runtime key = %q", got)
	}
	bindingID, boundGroup, boundOwner := "authz_1", "grp_1", "sys_1"
	authorized := chainRuntimeKeyOfCandidate(gatewaydispatch.AccountCandidate{
		ID: "acc_2", AccountAccessType: "account_authorized",
		BindingSystemAccountID: &boundOwner, BoundGroupID: &boundGroup, AccountAuthorizationID: &bindingID,
	})
	if !strings.HasPrefix(authorized, "acc_2:authorized:sys_1:grp_1:authz_1") {
		t.Fatalf("authorized key = %q", authorized)
	}
	// 指针缺失时回落普通 ID。
	if got := chainRuntimeKeyOfCandidate(gatewaydispatch.AccountCandidate{ID: "acc_3", AccountAccessType: "account_authorized"}); got != "acc_3" {
		t.Fatalf("不完整授权 key = %q", got)
	}
	// chainConcurrencyAccountIDOf。
	source := "  phys_1  "
	if got := chainConcurrencyAccountIDOf(gatewaydispatch.AccountCandidate{ID: "acc_4", CredentialSourceAccountID: &source}); got != "phys_1" {
		t.Fatalf("credential source = %q", got)
	}
	if got := chainConcurrencyAccountIDOf(gatewaydispatch.AccountCandidate{ID: "acc_5"}); got != "acc_5" {
		t.Fatalf("fallback id = %q", got)
	}
	// chainCutoverTargetsOf。
	targets := chainCutoverTargetsOf([]gatewaydispatch.AccountCandidate{
		{ID: "acc_6", ConcurrencyLimit: 4},
		{ID: "acc_7", ConcurrencyLimit: 2, CredentialSourceAccountID: &source},
	})
	if len(targets) != 2 || targets[0].ConcurrencyLimit != 4 || targets[1].CredentialSourceAccountID != "phys_1" {
		t.Fatalf("targets = %+v", targets)
	}
}

func w1Reservation(t *testing.T) *gatewayhotquality.SpeedFirstCutoverReservation {
	t.Helper()
	reservation, err := gatewayhotquality.ReserveSpeedFirstCutoverTarget(nil, gatewayhotquality.SpeedFirstCutoverReservationInput{
		SystemAccountID: "sys_1", GroupID: "grp_1", SlowAccountID: "acc_slow",
		Targets: []gatewayhotquality.GatewayAccountConcurrencyLimitIdentity{{
			ID: "acc_target", CredentialSourceAccountID: "acc_target", ConcurrencyLimit: 2,
		}},
		SlotAcquirer: func(_ contextLib.Context, _ string, _ int, _ gatewayhotquality.AccountConcurrencyAcquireRequest) (gatewayhotquality.AccountConcurrencySlot, bool, error) {
			return gatewayhotquality.AccountConcurrencySlot{Key: "acc_target", Release: func() {}}, true, nil
		},
	})
	if err != nil || reservation == nil {
		t.Fatalf("reserve = %v, %v", reservation, err)
	}
	return reservation
}

func TestW1SpeedFirstReservationProjections(t *testing.T) {
	if speedFirstReservationViewOf(nil) != nil {
		t.Fatal("nil reservation 视图必须 nil")
	}
	if speedFirstReservationHandleOf(nil) != nil {
		t.Fatal("nil reservation 句柄必须 nil")
	}
	reservation := w1Reservation(t)
	view := speedFirstReservationViewOf(reservation)
	if view == nil || view.TargetAccountIDValue != "acc_target" || view.ReleaseFunc == nil {
		t.Fatalf("view = %+v", view)
	}
	handle := speedFirstReservationHandleOf(reservation)
	if handle == nil {
		t.Fatal("handle 缺失")
	}
	// 非 target 账户取不到槽。
	if _, ok := handle.TakeForAccount(gatewaydispatch.AccountCandidate{ID: "acc_other"}); ok {
		t.Fatal("非 target 账户不得取得预占槽")
	}
	// target 账户一次性取槽。
	slot, ok := handle.TakeForAccount(gatewaydispatch.AccountCandidate{ID: "acc_target"})
	if !ok || !slot.Acquired || slot.Release == nil {
		t.Fatalf("slot = %+v ok=%v", slot, ok)
	}
	// 消耗后再次取：失败。
	if _, ok := handle.TakeForAccount(gatewaydispatch.AccountCandidate{ID: "acc_target"}); ok {
		t.Fatal("已消耗预留不得重复取槽")
	}
	slot.Release()
	// 释放后视图 ReleaseFunc 幂等。
	view.ReleaseFunc()
}

func TestW1DispatchExhaustionClassification(t *testing.T) {
	kind, detail := classifyGatewayDispatchExhaustion(nil)
	if kind != "no_available_account" || detail != nil {
		t.Fatalf("nil = %q %v", kind, detail)
	}
	cases := []struct {
		url  string
		kind string
	}{
		{"account:api_key_pool_unavailable", "api_key_pool_unavailable"},
		{"account:locally_suppressed", "all_accounts_locally_suppressed"},
		{"concurrency:limit", "account_concurrency_exhausted"},
	}
	for _, testCase := range cases {
		kind, detail = classifyGatewayDispatchExhaustion(&gatewaydispatch.UpstreamAttempt{UpstreamURL: testCase.url})
		if kind != testCase.kind || detail != nil {
			t.Fatalf("%s = %q %v", testCase.url, kind, detail)
		}
	}
	kind, detail = classifyGatewayDispatchExhaustion(&gatewaydispatch.UpstreamAttempt{HasStatus: true, Status: 502})
	if kind != "upstream_http_error" || detail != 502 {
		t.Fatalf("http = %q %v", kind, detail)
	}
	kind, detail = classifyGatewayDispatchExhaustion(&gatewaydispatch.UpstreamAttempt{UpstreamURL: "https://x", HasStatus: false})
	if kind != "upstream_transport_error" || detail != nil {
		t.Fatalf("transport = %q %v", kind, detail)
	}
}

func TestW1StreamRetryAndMiscProjections(t *testing.T) {
	if got := streamServerRetryFallbackReason(gatewayresponse.StreamServerRetryResponseInspection); got != "response_inspection_server_retry_exhausted" {
		t.Fatalf("inspection = %q", got)
	}
	if got := streamServerRetryFallbackReason(gatewayresponse.StreamServerRetryUpstreamProtocolFailure); got != "upstream_protocol_server_retry_exhausted" {
		t.Fatalf("protocol = %q", got)
	}
	if got := streamServerRetryFallbackReason("other"); got != "stream_server_retry_exhausted" {
		t.Fatalf("default = %q", got)
	}
	keys := stringSetKeys(map[string]struct{}{"b": {}, "a": {}, "c": {}})
	if len(keys) != 3 || keys[0] != "a" || keys[2] != "c" {
		t.Fatalf("keys = %v", keys)
	}
	if !isEmptyPreflightResult(gatewaypreauth.PreflightResult{}) {
		t.Fatal("空 preflight 必须为真")
	}
	if isEmptyPreflightResult(gatewaypreauth.PreflightResult{RouteAction: &gatewaypreauth.RouteAction{}}) {
		t.Fatal("有 route action 不为空")
	}
	if routeStrategyIDOf(nil) != "" {
		t.Fatal("nil record 必须空策略")
	}
	if got := routeStrategyIDOf(&gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_1"}); got != "rs_1" {
		t.Fatalf("strategy = %q", got)
	}
}

func TestW1DbServiceUnavailableMessage(t *testing.T) {
	for _, message := range []string{
		"本地数据库服务暂时不可用",
		"本地数据库服务未就绪",
		"本地数据库服务请求超时",
		"本地数据库服务已退出",
	} {
		if !dbServiceUnavailableMessage(message) {
			t.Fatalf("%q 必须识别为 db 不可用", message)
		}
	}
	if dbServiceUnavailableMessage("其他错误") {
		t.Fatal("普通错误不得识别")
	}
}

func TestW1NewRequestBudgets(t *testing.T) {
	budgets, err := newRequestBudgets("trace_w1", 1000, gatewaypreauth.SystemClock{})
	if err != nil {
		t.Fatalf("budgets: %v", err)
	}
	if budgets.coordination == nil {
		t.Fatal("coordination budget 缺失")
	}
}
