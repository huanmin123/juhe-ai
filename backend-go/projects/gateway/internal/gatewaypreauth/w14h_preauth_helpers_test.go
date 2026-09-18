package gatewaypreauth

// w14h 覆盖波次（第二部分）：preflighthelpers 的路由配置分支、可恢复账户
// 等待与 API Key 分组回退准备的错误臂。全部进程内 fake，无真实网络。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW14HRouteConfigArms(t *testing.T) {
	service := &Service{}
	lane := gatewayProtoLane(gatewayproto.LaneText)
	speedRow := func(raw string, preference string) *gatewayruntimecache.GatewayAPIKeyRow {
		return &gatewayruntimecache.GatewayAPIKeyRow{
			RouteStrategyMode: gatewayruntimecache.RouteStrategyModeNormal,
			NormalRoutingConfig: &gatewayruntimecache.RouteStrategyNormalRoutingConfig{
				SchedulingPreference: preference,
				Raw:                  json.RawMessage(raw),
			},
		}
	}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`{}`, "speed_first"), lane, true); got != nil {
		t.Fatal("禁用超时必须返回 nil")
	}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`{}`, "speed_first"), "image", false); got != nil {
		t.Fatal("非 text 通道必须返回 nil")
	}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(nil, lane, false); got != nil {
		t.Fatal("nil 记录必须返回 nil")
	}
	wrongMode := speedRow(`{}`, "speed_first")
	wrongMode.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeWeighted
	if got := service.normalRouteSpeedFirstConfigForAPIKey(wrongMode, lane, false); got != nil {
		t.Fatal("非 normal 模式必须返回 nil")
	}
	noConfig := speedRow(`{}`, "speed_first")
	noConfig.NormalRoutingConfig = nil
	if got := service.normalRouteSpeedFirstConfigForAPIKey(noConfig, lane, false); got != nil {
		t.Fatal("缺 normal 配置必须返回 nil")
	}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`{}`, "cost_first"), lane, false); got != nil {
		t.Fatal("非 speed_first 必须返回 nil")
	}
	noRaw := speedRow(`{}`, "speed_first")
	noRaw.NormalRoutingConfig.Raw = nil
	if got := service.normalRouteSpeedFirstConfigForAPIKey(noRaw, lane, false); got != nil {
		t.Fatal("缺 Raw 必须返回 nil")
	}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`w14h-bad`, "speed_first"), lane, false); got != nil {
		t.Fatal("非法 Raw 必须返回 nil")
	}
	withDeadline := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`{"firstByteDeadlineMs": 2500}`, "speed_first"), lane, false)
	if withDeadline == nil || withDeadline.FirstByteDeadlineMs == nil || *withDeadline.FirstByteDeadlineMs != 2500 {
		t.Fatalf("含 deadline 配置=%v", withDeadline)
	}
	withoutDeadline := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`{"other": 1}`, "speed_first"), lane, false)
	if withoutDeadline == nil || withoutDeadline.FirstByteDeadlineMs != nil {
		t.Fatalf("无 deadline 配置=%v", withoutDeadline)
	}
	if got := service.normalRouteFirstByteConfigForAPIKey(speedRow(`{}`, "speed_first"), lane, true, nil); got != nil {
		t.Fatal("first-byte 禁用必须返回 nil")
	}
	if got := service.normalRouteFirstByteConfigForAPIKey(speedRow(`{}`, "speed_first"), "image", false, nil); got != nil {
		t.Fatal("first-byte 非 text 通道必须返回 nil")
	}
	override := &NormalRouteFirstByteRuntimeConfig{SchedulingPreference: "speed_first"}
	if got := service.normalRouteFirstByteConfigForAPIKey(nil, lane, false, override); got != override {
		t.Fatal("override 必须直通")
	}
	if got := service.normalRouteFirstByteConfigForAPIKey(nil, lane, false, nil); got != nil {
		t.Fatal("first-byte 缺记录必须返回 nil")
	}
	withConfig := service.normalRouteFirstByteConfigForAPIKey(speedRow(`{"firstByteDeadlineMs": 900}`, "speed_first"), lane, false, nil)
	if withConfig == nil || withConfig.FirstByteDeadlineMs == nil {
		t.Fatalf("first-byte 配置=%v", withConfig)
	}
	if got := openAIEndpointFamilyFromPath(" "); got != "" {
		t.Fatalf("空路径端点族=%q", got)
	}
	if got := anthropicMessagesRequestEndpointFamily(NewGatewayRequest(httptest.NewRequest("GET", "/v1/messages", nil))); got != "" {
		t.Fatalf("非 POST=%q", got)
	}
	if got := anthropicMessagesRequestEndpointFamily(NewGatewayRequest(httptest.NewRequest("POST", "/v1/other", nil))); got != "" {
		t.Fatalf("非 messages=%q", got)
	}
}

func TestW14HRoutePlanSnapshotArms(t *testing.T) {
	service := &Service{}
	weighted := &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyMode: gatewayruntimecache.RouteStrategyModeWeighted}
	snapshot, err := service.createOpenAIGatewayRoutePlanSnapshot(routePlanInput{
		traceID:      "w14h",
		startedAt:    1,
		groupId:      "g2",
		apiKeyRecord: weighted,
		hybridRoute: &HybridRuntimeRoute{
			TargetModel: "gem-2.5",
			Scoring:     map[string]any{"level": "high", "defaulted": true},
			Route:       map[string]any{"minLevel": "low", "maxLevel": "high"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WeightedDecisionToken != "g2" {
		t.Fatalf("加权 token=%q", snapshot.WeightedDecisionToken)
	}
	if snapshot.HybridScoreDecision == nil {
		t.Fatal("混合决策不能为空")
	}
}

func TestW14HNextRecoverableRetryAfterArms(t *testing.T) {
	future := "2099-01-01T00:00:00Z"
	past := "2020-01-01T00:00:00Z"
	if _, ok := nextRecoverableAccountRetryAfterMs(nil, 1); ok {
		t.Fatal("空账户必须失败")
	}
	if _, ok := nextRecoverableAccountRetryAfterMs([]gatewayruntimecache.OpenAIAccountSecret{{}}, 1); ok {
		t.Fatal("无 cooldown 必须失败")
	}
	if _, ok := nextRecoverableAccountRetryAfterMs([]gatewayruntimecache.OpenAIAccountSecret{{CooldownUntil: w14hStrPtr("w14h-bad")}}, 1); ok {
		t.Fatal("非法 cooldown 必须失败")
	}
	if got, ok := nextRecoverableAccountRetryAfterMs([]gatewayruntimecache.OpenAIAccountSecret{{CooldownUntil: w14hStrPtr(past)}}, 4102444800000); !ok || got != 0 {
		t.Fatalf("过期 cooldown=%d/%v", got, ok)
	}
	got, ok := nextRecoverableAccountRetryAfterMs([]gatewayruntimecache.OpenAIAccountSecret{
		{CooldownUntil: w14hStrPtr(future)},
		{CooldownUntil: w14hStrPtr("2098-01-01T00:00:00Z")},
	}, 1)
	if !ok || got <= 0 {
		t.Fatalf("最近 cooldown=%d/%v", got, ok)
	}
}

func w14hStrPtr(value string) *string { return &value }

func w14hIntPtr(value int) *int { return &value }

func w14hMustSnapshot(targets ...string) gatewayrouting.RoutePlanSnapshot[string] {
	snapshot, err := gatewayrouting.CreateGatewayRoutePlanSnapshot(gatewayrouting.CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID:           "w14h",
		OrderedAllowedTargets: targets,
		Cursor:                w14hIntPtr(0),
	})
	if err != nil {
		panic(err)
	}
	return snapshot
}

// w14hRuntimeOverride 在 fakeRuntimeCache 上叠加 List 错误注入。
type w14hRuntimeOverride struct {
	fakeRuntimeCache
	freshErr       error
	recoverableErr error
}

func (c *w14hRuntimeOverride) ListFreshOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if c.freshErr != nil {
		return nil, c.freshErr
	}
	return c.fakeRuntimeCache.accounts, nil
}

func (c *w14hRuntimeOverride) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if c.recoverableErr != nil {
		return nil, c.recoverableErr
	}
	return c.fakeRuntimeCache.accounts, nil
}

func TestW14HWaitRecoverableArms(t *testing.T) {
	build := func(runtimeCache *w14hRuntimeOverride) *Service {
		service, _, _ := newTestService(t, func(s *Service) {
			s.RuntimeCache = runtimeCache
		})
		return service
	}
	newInput := func() recoveryInput {
		return recoveryInput{
			req:               NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil)),
			auditCapture:      w14hCapture{onMetadata: func(string, map[string]any) {}},
			systemAccountID:   "sys",
			apiKeyID:          "key",
			groupID:           "group",
			serverRetryBudget: NewServerRetryBudget(1000, nil),
			signal:            context.Background(),
		}
	}
	freshFail := &w14hRuntimeOverride{freshErr: w14hBoomErr}
	if _, err := build(freshFail).waitForRecoverableOpenAIGatewayCandidateAccounts(newInput()); err == nil {
		t.Fatal("活跃账户读取失败必须透传")
	}
	recoverableFail := &w14hRuntimeOverride{recoverableErr: w14hBoomErr}
	if _, err := build(recoverableFail).waitForRecoverableOpenAIGatewayCandidateAccounts(newInput()); err == nil {
		t.Fatal("可恢复账户读取失败必须透传")
	}
	empty := &w14hRuntimeOverride{}
	accounts, err := build(empty).waitForRecoverableOpenAIGatewayCandidateAccounts(newInput())
	if err != nil || len(accounts) != 0 {
		t.Fatalf("双空=%v/%v", accounts, err)
	}
	recoverable := &w14hRuntimeOverride{}
	recoverable.accounts = []gatewayruntimecache.OpenAIAccountSecret{{ID: "w14h-recoverable"}}
	if _, err := build(recoverable).waitForRecoverableOpenAIGatewayCandidateAccounts(newInput()); err != nil {
		t.Fatalf("可恢复等待=%v", err)
	}
}

func TestW14HPrepareFallbackArms(t *testing.T) {
	ctx := context.Background()
	snapshot, err := gatewayrouting.CreateGatewayRoutePlanSnapshot(gatewayrouting.CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID:           "w14h",
		OrderedAllowedTargets: []string{"g1", "g2"},
		Cursor:                w14hIntPtr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := APIKeyGroupFallbackDispatchInput{
		Reason:            "w14h",
		APIKeyRecord:      &gatewayruntimecache.GatewayAPIKeyRow{ID: "key"},
		GroupID:           "g1",
		RoutePlanSnapshot: snapshot,
		AuditCapture:      w14hCapture{onMetadata: func(string, map[string]any) {}},
	}
	errService, _, _ := newTestService(t, func(s *Service) {
		s.Candidates = &fakeCandidates{fallbackCandidateErr: w14hBoomErr}
	})
	if _, err := errService.PrepareAPIKeyGroupFallbackDispatchContext(ctx, input); err == nil {
		t.Fatal("候选解析失败必须透传")
	}
	notFound, _, _ := newTestService(t, func(s *Service) {
		s.Candidates = &fakeCandidates{}
	})
	result, err := notFound.PrepareAPIKeyGroupFallbackDispatchContext(ctx, input)
	if err != nil || result.Attempted {
		t.Fatalf("无候选=%+v/%v", result, err)
	}
	attempted, _, _ := newTestService(t, nil)
	exhausted := APIKeyGroupFallbackDispatchInput{
		Reason: "w14h", APIKeyRecord: &gatewayruntimecache.GatewayAPIKeyRow{ID: "key"}, GroupID: "g1",
		RoutePlanSnapshot: w14hMustSnapshot("g1"),
		AuditCapture: w14hCapture{onMetadata: func(string, map[string]any) {}},
	}
	result, err = attempted.PrepareAPIKeyGroupFallbackDispatchContext(ctx, exhausted)
	if err != nil || result.Attempted {
		t.Fatalf("无后备=%+v/%v", result, err)
	}
}
