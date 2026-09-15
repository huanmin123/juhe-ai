package main

// w1_resolve_tail_test.go：chain_routing.go / chain_ports.go / chain_dispatch.go
// / chain_preflight.go / runtime.go 既有 w1* 测试未覆盖的剩余函数单测。
// 全部新包级标识符使用 w1k 前缀；断言只依赖 stdlib 与确定性 fake。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// ---------------------------------------------------------------------------
// 公共 fake 与构造 helper
// ---------------------------------------------------------------------------

// w1kFixedClock 是 gatewayaccounteffects.Clock 的固定时钟实现（避让状态
// UntilMs 的写入侧确定性）。
type w1kFixedClock struct{ now time.Time }

func (c w1kFixedClock) Now() time.Time { return c.now }

// w1kBodyRequest 构造带已捕获 body 状态的网关请求；model 为空时产出无 body
// 的请求（httptest 原始请求 + 空 BodyState）。
func w1kBodyRequest(t *testing.T, method, path, model string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	rawBody := ""
	if model != "" {
		rawBody = `{"model":"` + model + `","messages":[]}`
	}
	httpReq := httptest.NewRequest(method, path, strings.NewReader(rawBody))
	httpReq.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	if rawBody != "" {
		modelCopy := model
		req.Body = &gatewaybody.Request{
			RawBody:           []byte(rawBody),
			Body:              map[string]any{"model": model, "messages": []any{}},
			ContentTypeHeader: "application/json",
			State: &gatewaybody.BodyState{
				ContentType:     "application/json",
				JSONParseStatus: gatewaybody.JSONParseStatusParsed,
				Model:           &modelCopy,
			},
		}
	}
	return req
}

// w1kNormalRouteKeyRow 构造绑定到 fixture 分组的 normal 策略 Key 行，每个
// provider 一条 active 绑定（同一分组多供应商才不会被 single_provider 跳过）。
func w1kNormalRouteKeyRow(fixture *chainFixture, mode string, providers ...string) *gatewayruntimecache.GatewayAPIKeyRow {
	row := &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                "key_w1k",
		SystemAccountID:   fixture.systemAccount,
		RouteStrategyID:   "rs_w1k",
		RouteStrategyMode: mode,
		Status:            "active",
	}
	for index, provider := range providers {
		row.GroupBindings = append(row.GroupBindings, gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			ID:              fmt.Sprintf("binding_w1k_%d", index),
			APIKeyID:        "key_w1k",
			SystemAccountID: fixture.systemAccount,
			GroupID:         fixture.groupID,
			Priority:        index,
			Weight:          1,
			Status:          "active",
			ProviderCode:    provider,
			GroupEnabled:    1,
		})
	}
	return row
}

// w1kNormalResolver 装配带真实 normal 路由核心的 chainRouteResolver。
func w1kNormalResolver(fixture *chainFixture) *chainRouteResolver {
	return &chainRouteResolver{
		cache:  fixture.cache,
		normal: gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{}),
	}
}

// w1kHybridConfigJSON 是最小可用的 hybrid_smart 路由配置：单条 level 路由
// 命中 fixture 分组的 gpt-test，scoring 回落窗口覆盖全部 level。
const w1kHybridConfigJSON = `{` +
	`"scoringModel":"gpt-test",` +
	`"scoringContextMode":"conversation",` +
	`"qualityPreference":"quality_first",` +
	`"scoringTimeoutMs":3000,` +
	`"scoringFallbackMaxLevel":100,` +
	`"scoringCacheEnabled":false,` +
	`"scoringCacheTtlSeconds":300,` +
	`"cacheAffinityEnabled":false,` +
	`"affinityTtlSeconds":3600,` +
	`"switchMinLevelDelta":10,` +
	`"downgradeConsecutiveLowCount":3,` +
	`"levelRoutes":[{"minLevel":0,"maxLevel":100,"targetModel":"gpt-test","enabled":true}]` +
	`}`

// w1kHybridKeyRow 构造 hybrid 策略 Key 行（SelectedGroupID 指向 fixture 分组，
// hybridTargetGroups.SelectTargetGroup 依赖它读组访问与账户）。
func w1kHybridKeyRow(fixture *chainFixture, mode, configJSON string) *gatewayruntimecache.GatewayAPIKeyRow {
	row := &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                "key_w1k_hybrid",
		SystemAccountID:   fixture.systemAccount,
		RouteStrategyID:   "rs_w1k_hybrid",
		RouteStrategyMode: mode,
		SelectedGroupID:   fixture.groupID,
		Status:            "active",
	}
	if configJSON != "" {
		row.HybridRoutingConfig = &gatewayruntimecache.ApiKeyHybridRoutingConfig{Raw: json.RawMessage(configJSON)}
	}
	return row
}

// w1kFixedTime 返回固定 hybrid Clock（func() time.Time 形状）。
func w1kFixedTime() time.Time {
	return time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
}

// w1kFailingAuxDispatcher 是 gatewayhybrid.AuxiliaryDispatcher 的失败 fake：
// 辅助打分请求必然失败，驱动 hybrid Resolve 走 scoring 回落分支。
type w1kFailingAuxDispatcher struct{}

func (w1kFailingAuxDispatcher) DispatchHybridAuxiliaryChatCompletion(_ context.Context, _ gatewayhybrid.AuxiliaryDispatchInput) (gatewayhybrid.AuxiliaryDispatchSuccess, *gatewayhybrid.AuxiliaryDispatchFailure) {
	return gatewayhybrid.AuxiliaryDispatchSuccess{}, &gatewayhybrid.AuxiliaryDispatchFailure{
		ErrorCode:    "w1k_dispatch_failed",
		ErrorMessage: "测试注入的辅助派发失败",
	}
}

// w1kHybridResolver 装配真实 hybrid 路由核心：selector 走 fixture 运行时缓存
// 桥，identity 用降级端口，scoring 用必然失败的 fake dispatcher。
func w1kHybridResolver(fixture *chainFixture) *chainRouteResolver {
	affinity := gatewayhybrid.NewAffinityService(w1kFixedTime, hybridSessionIdentityPort{}, nil)
	scoring := gatewayhybrid.NewScoringService(w1kFixedTime, w1kFailingAuxDispatcher{}, nil, nil, nil)
	return &chainRouteResolver{
		cache:   fixture.cache,
		hybrid:  gatewayhybrid.NewRouteService(affinity, hybridTargetGroups{cache: fixture.cache}, hybridSessionIdentityPort{}, nil),
		scoring: scoring,
	}
}

// w1kUsageService 组装真实 gatewayusage.Service + 内存记录器（收尾队列异步
// 落记录，测试用 WaitForIdle 有界等待）。
func w1kUsageService() (*gatewayusage.Service, *gatewayusage.MemoryUsageRecorder, *gatewayusage.FinalizationDispatch) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, nil, 64, 1)
	return gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{}), recorder, dispatch
}

// ---------------------------------------------------------------------------
// A1. chainRouteResolver.ResolveNormalGatewayModelRoute
// ---------------------------------------------------------------------------

func TestW1KResolveNormalGatewayModelRouteArms(t *testing.T) {
	fixture := newChainFixture(t)
	ctx := context.Background()

	t.Run("缺少路由核心必须报错", func(t *testing.T) {
		resolver := &chainRouteResolver{}
		_, err := resolver.ResolveNormalGatewayModelRoute(ctx, gatewaypreauth.NormalRouteInput{
			Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "gpt-test"),
			APIKeyRecord: w1kNormalRouteKeyRow(fixture, gatewayruntimecache.RouteStrategyModeNormal, "openai"),
		})
		if err == nil || !strings.Contains(err.Error(), "NormalModelRouteService") {
			t.Fatalf("缺少 normal 路由核心必须报错: %v", err)
		}
	})

	skipCases := []struct {
		name       string
		mode       string
		model      string
		providers  []string
		wantReason string
	}{
		{"hybrid 策略跳过", gatewayruntimecache.RouteStrategyModeHybridSmart, "gpt-test", []string{"openai", "anthropic"}, "route_strategy_is_hybrid_smart"},
		{"缺少请求模型跳过", gatewayruntimecache.RouteStrategyModeNormal, "", []string{"openai", "anthropic"}, "missing_requested_model"},
		{"无绑定跳过", gatewayruntimecache.RouteStrategyModeNormal, "gpt-test", nil, "empty_binding"},
		{"单供应商跳过", gatewayruntimecache.RouteStrategyModeNormal, "gpt-test", []string{"openai"}, "single_provider"},
	}
	for _, testCase := range skipCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := w1kNormalResolver(fixture).ResolveNormalGatewayModelRoute(ctx, gatewaypreauth.NormalRouteInput{
				Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", testCase.model),
				APIKeyRecord: w1kNormalRouteKeyRow(fixture, testCase.mode, testCase.providers...),
			})
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if result.Outcome != gatewaypreauth.NormalRouteOutcomeSkipped || result.Reason != testCase.wantReason {
				t.Fatalf("结果 = %s/%s, want skipped/%s", result.Outcome, result.Reason, testCase.wantReason)
			}
			if result.APIKeyRecord == nil || result.APIKeyRecord.ID != "key_w1k" {
				t.Fatalf("跳过分支必须原样携带 Key 行: %+v", result.APIKeyRecord)
			}
		})
	}

	t.Run("命中选择并回填完整账户", func(t *testing.T) {
		result, err := w1kNormalResolver(fixture).ResolveNormalGatewayModelRoute(ctx, gatewaypreauth.NormalRouteInput{
			Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "gpt-test"),
			APIKeyRecord: w1kNormalRouteKeyRow(fixture, gatewayruntimecache.RouteStrategyModeNormal, "openai", "anthropic"),
		})
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if result.Outcome != gatewaypreauth.NormalRouteOutcomeSelected {
			t.Fatalf("结果 = %s/%s, want selected", result.Outcome, result.Reason)
		}
		if result.GroupID != fixture.groupID {
			t.Fatalf("GroupID = %q, want %q", result.GroupID, fixture.groupID)
		}
		if result.APIKeyRecord == nil || result.APIKeyRecord.SelectedGroupID != fixture.groupID {
			t.Fatalf("选择分支必须回写 SelectedGroupID: %+v", result.APIKeyRecord)
		}
		if result.GroupAccess == nil || result.GroupAccess.ProviderCode != "openai" {
			t.Fatalf("GroupAccess 回填错误: %+v", result.GroupAccess)
		}
		if len(result.Accounts) != 1 || result.Accounts[0].ID != fixture.accountID {
			t.Fatalf("回填账户 = %+v, want [%s]", result.Accounts, fixture.accountID)
		}
		if result.Accounts[0].APIKey != "sk-upstream-account-key" {
			t.Fatalf("回填账户必须携带凭据: %q", result.Accounts[0].APIKey)
		}
		if result.RouteSource != "catalog_provider" {
			t.Fatalf("RouteSource = %q, want catalog_provider", result.RouteSource)
		}
		if result.MatchedProviderCode != "openai" {
			t.Fatalf("MatchedProviderCode = %q, want openai", result.MatchedProviderCode)
		}
		if result.RequestedModel != "gpt-test" {
			t.Fatalf("RequestedModel = %q", result.RequestedModel)
		}
	})

	t.Run("模型不可路由失败", func(t *testing.T) {
		result, err := w1kNormalResolver(fixture).ResolveNormalGatewayModelRoute(ctx, gatewaypreauth.NormalRouteInput{
			Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "no-such-model"),
			APIKeyRecord: w1kNormalRouteKeyRow(fixture, gatewayruntimecache.RouteStrategyModeNormal, "openai", "anthropic"),
		})
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if result.Outcome != gatewaypreauth.NormalRouteOutcomeFailed {
			t.Fatalf("结果 = %s, want failed", result.Outcome)
		}
		if result.StatusCode != 400 || result.Code != "model_not_routable_for_api_key" {
			t.Fatalf("失败契约 = %d/%s, want 400/model_not_routable_for_api_key", result.StatusCode, result.Code)
		}
		if result.RequestedModel != "no-such-model" {
			t.Fatalf("RequestedModel = %q", result.RequestedModel)
		}
	})
}

// ---------------------------------------------------------------------------
// A1. chainRouteResolver.ResolveHybridGatewayRoute
// ---------------------------------------------------------------------------

func TestW1KResolveHybridGatewayRouteArms(t *testing.T) {
	fixture := newChainFixture(t)
	ctx := context.Background()

	t.Run("未装配 hybrid 核心按跳过返回", func(t *testing.T) {
		result, err := (&chainRouteResolver{}).ResolveHybridGatewayRoute(ctx, gatewaypreauth.HybridRouteInput{
			Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "gpt-test"),
			APIKeyRecord: w1kHybridKeyRow(fixture, gatewayruntimecache.RouteStrategyModeHybridSmart, w1kHybridConfigJSON),
		})
		if err != nil {
			t.Fatalf("未装配核心必须静默跳过: %v", err)
		}
		if result.Outcome != gatewaypreauth.HybridRouteOutcomeSkipped || result.Reason != "not_hybrid_route_strategy" {
			t.Fatalf("结果 = %s/%s, want skipped/not_hybrid_route_strategy", result.Outcome, result.Reason)
		}
	})

	t.Run("混合路由配置解析失败", func(t *testing.T) {
		resolver := w1kHybridResolver(fixture)
		_, err := resolver.ResolveHybridGatewayRoute(ctx, gatewaypreauth.HybridRouteInput{
			Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "gpt-test"),
			APIKeyRecord: w1kHybridKeyRow(fixture, gatewayruntimecache.RouteStrategyModeHybridSmart, "{not-json"),
		})
		if err == nil || !strings.Contains(err.Error(), "解析混合路由配置失败") {
			t.Fatalf("非法配置必须报解析错误: %v", err)
		}
	})

	t.Run("非 hybrid 策略跳过", func(t *testing.T) {
		result, err := w1kHybridResolver(fixture).ResolveHybridGatewayRoute(ctx, gatewaypreauth.HybridRouteInput{
			Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "gpt-test"),
			APIKeyRecord: w1kHybridKeyRow(fixture, gatewayruntimecache.RouteStrategyModeNormal, w1kHybridConfigJSON),
		})
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if result.Outcome != gatewaypreauth.HybridRouteOutcomeSkipped || result.Reason != "not_hybrid_route_strategy" {
			t.Fatalf("结果 = %s/%s, want skipped/not_hybrid_route_strategy", result.Outcome, result.Reason)
		}
		if result.APIKeyRecord == nil || result.APIKeyRecord.ID != "key_w1k_hybrid" {
			t.Fatalf("跳过分支必须原样携带 Key 行: %+v", result.APIKeyRecord)
		}
	})

	t.Run("非 JSON POST 跳过", func(t *testing.T) {
		result, err := w1kHybridResolver(fixture).ResolveHybridGatewayRoute(ctx, gatewaypreauth.HybridRouteInput{
			Req:          w1kBodyRequest(t, http.MethodGet, "/v1/chat/completions", ""),
			APIKeyRecord: w1kHybridKeyRow(fixture, gatewayruntimecache.RouteStrategyModeHybridSmart, w1kHybridConfigJSON),
		})
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if result.Outcome != gatewaypreauth.HybridRouteOutcomeSkipped || result.Reason != "not_json_post_request" {
			t.Fatalf("结果 = %s/%s, want skipped/not_json_post_request", result.Outcome, result.Reason)
		}
	})

	t.Run("打分失败回落选中并回填账户", func(t *testing.T) {
		result, err := w1kHybridResolver(fixture).ResolveHybridGatewayRoute(ctx, gatewaypreauth.HybridRouteInput{
			Req:          w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "client-model"),
			APIKeyRecord: w1kHybridKeyRow(fixture, gatewayruntimecache.RouteStrategyModeHybridSmart, w1kHybridConfigJSON),
			TraceID:      "trace_w1k_hybrid",
		})
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if result.Outcome != gatewaypreauth.HybridRouteOutcomeSelected {
			t.Fatalf("结果 = %s/%s, want selected", result.Outcome, result.Reason)
		}
		if result.TargetModel != "gpt-test" || result.GroupID != fixture.groupID {
			t.Fatalf("目标 = %s/%s, want gpt-test/%s", result.TargetModel, result.GroupID, fixture.groupID)
		}
		if !result.ScoringFallbackApplied || result.AffinityApplied {
			t.Fatalf("回落标记 = %v/%v, want true/false", result.ScoringFallbackApplied, result.AffinityApplied)
		}
		if result.APIKeyRecord == nil || result.APIKeyRecord.SelectedGroupID != fixture.groupID {
			t.Fatalf("选中分支必须回写 SelectedGroupID: %+v", result.APIKeyRecord)
		}
		if len(result.Accounts) != 1 || result.Accounts[0].ID != fixture.accountID {
			t.Fatalf("回填账户 = %+v, want [%s]", result.Accounts, fixture.accountID)
		}
		if result.Accounts[0].APIKey != "sk-upstream-account-key" {
			t.Fatalf("回填账户必须携带凭据: %q", result.Accounts[0].APIKey)
		}
		if result.Config == nil || result.Scoring == nil || result.Route == nil {
			t.Fatalf("Config/Scoring/Route 映射必须非空: %+v", result)
		}
		if result.Route["targetModel"] != "gpt-test" {
			t.Fatalf("Route.targetModel = %v, want gpt-test", result.Route["targetModel"])
		}
	})
}

// ---------------------------------------------------------------------------
// A2. hybridSessionIdentityPort.HybridRouteAffinityKey + hybridTargetGroups.SelectTargetGroup
// ---------------------------------------------------------------------------

func TestW1KHybridSessionIdentityAndTargetGroups(t *testing.T) {
	fixture := newChainFixture(t)
	ctx := context.Background()

	if key := (hybridSessionIdentityPort{}).HybridRouteAffinityKey(&gatewayhybrid.GatewayRequestView{Method: "POST"}, gatewayhybrid.AffinityKeyScope{SystemAccountID: fixture.systemAccount}); key != "" {
		t.Fatalf("降级身份端口必须返回空亲和键: %q", key)
	}

	if selection, err := (hybridTargetGroups{}).SelectTargetGroup(ctx, gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{SelectedGroupID: fixture.groupID},
	}); err != nil || selection != nil {
		t.Fatalf("缓存缺失必须返回空选择: %+v, %v", selection, err)
	}

	selector := hybridTargetGroups{cache: fixture.cache}
	if selection, err := selector.SelectTargetGroup(ctx, gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{SystemAccountID: fixture.systemAccount},
	}); err != nil || selection != nil {
		t.Fatalf("未选中分组必须返回空选择: %+v, %v", selection, err)
	}
	if selection, err := selector.SelectTargetGroup(ctx, gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{SystemAccountID: fixture.systemAccount, SelectedGroupID: "group_missing"},
	}); err != nil || selection != nil {
		t.Fatalf("分组缺失必须返回空选择: %+v, %v", selection, err)
	}

	selection, err := selector.SelectTargetGroup(ctx, gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{SystemAccountID: fixture.systemAccount, SelectedGroupID: fixture.groupID},
		TargetModel:  "gpt-test",
	})
	if err != nil {
		t.Fatalf("正常选择失败: %v", err)
	}
	if selection == nil || selection.GroupID != fixture.groupID || selection.GroupAccess.ProviderCode != "openai" {
		t.Fatalf("选择 = %+v, want 分组 %s", selection, fixture.groupID)
	}
	if len(selection.Accounts) != 1 || selection.Accounts[0].ID != fixture.accountID {
		t.Fatalf("选择账户 = %+v, want [%s]", selection.Accounts, fixture.accountID)
	}
	if selection.ResponseInspectionPolicies == nil {
		t.Fatal("ResponseInspectionPolicies 必须初始化为空切片")
	}

	// 分组存在但无账户（model 过滤后为空）必须返回空选择。
	if _, err := fixture.db.Exec(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('group_w1k_empty', ?, 'openai', 1, 'personal')`, fixture.systemAccount); err != nil {
		t.Fatalf("seed 空分组: %v", err)
	}
	if selection, err := selector.SelectTargetGroup(ctx, gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{SystemAccountID: fixture.systemAccount, SelectedGroupID: "group_w1k_empty"},
		TargetModel:  "gpt-test",
	}); err != nil || selection != nil {
		t.Fatalf("无可用账户必须返回空选择: %+v, %v", selection, err)
	}
}

// ---------------------------------------------------------------------------
// A3. usageAttemptRecorderAdapter + attemptStatusCodeOf / usageContextOf / usageFailureContextOf
// ---------------------------------------------------------------------------

func TestW1KUsageAttemptRecorderAdapterArms(t *testing.T) {
	ctx := context.Background()
	usageContext := gatewaypreauth.GatewayFailureUsageContext{
		TraceID:                   "trace_w1k_attempt",
		TrafficSource:             "gateway",
		ClientIP:                  "203.0.113.9",
		SystemAccountID:           "sys_w1k",
		APIKeyID:                  "key_w1k",
		GroupID:                   "group_w1k",
		Endpoint:                  "/v1/chat/completions",
		GroupOwnerSystemAccountID: "sys_w1k",
		GroupAccessType:           "owner",
	}

	// service 缺失时静默直通。
	if err := (usageAttemptRecorderAdapter{}).RecordFailedUpstreamAttempt(ctx, nil, usageContext, gatewaydispatch.AccountCandidate{}, gatewaydispatch.FailedAttemptRecord{}); err != nil {
		t.Fatalf("缺失用量服务必须直通: %v", err)
	}

	service, recorder, dispatch := w1kUsageService()
	adapter := usageAttemptRecorderAdapter{service: service}
	statusCode := 502
	err := adapter.RecordFailedUpstreamAttempt(ctx,
		w1kBodyRequest(t, http.MethodPost, "/v1/chat/completions", "gpt-test"),
		usageContext,
		gatewaydispatch.AccountCandidate{
			ID:                          "acc_w1k",
			ProviderCode:                "openai",
			AccountOwnerSystemAccountID: "sys_w1k_acc",
			GroupOwnerSystemAccountID:   "sys_w1k",
			AccountAccessType:           "owner",
			GroupAccessType:             "owner",
		},
		gatewaydispatch.FailedAttemptRecord{
			UpstreamURL:        "https://upstream.example/v1/chat/completions",
			StartedAt:          1_000,
			StatusCode:         statusCode,
			HasStatusCode:      true,
			BodyText:           `{"error":{"message":"boom"}}`,
			ErrorMessage:       "上游返回 502",
			FailureAttribution: "account_upstream",
		})
	if err != nil {
		t.Fatalf("失败尝试记录失败: %v", err)
	}
	if !dispatch.WaitForIdle(2000) {
		t.Fatal("用量收尾未在超时内完成")
	}
	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("记录数 = %d, want 1", len(records))
	}
	record := records[0]
	if record.TraceID != "trace_w1k_attempt" || record.ClientIP != "203.0.113.9" {
		t.Fatalf("身份字段错误: %+v", record)
	}
	if record.SystemAccountID != "sys_w1k" || record.APIKeyID != "key_w1k" || record.GroupID != "group_w1k" {
		t.Fatalf("归属字段错误: %+v", record)
	}
	if record.Endpoint != "/v1/chat/completions" {
		t.Fatalf("Endpoint = %q", record.Endpoint)
	}
	if record.Model != "gpt-test" {
		t.Fatalf("Model = %q, want 请求体模型提示 gpt-test", record.Model)
	}
	if record.StatusCode == nil || *record.StatusCode != 502 {
		t.Fatalf("StatusCode = %v, want 502", record.StatusCode)
	}
	if record.ErrorMessage != "上游返回 502" || record.FailureAttribution != "account_upstream" {
		t.Fatalf("失败描述错误: %q/%q", record.ErrorMessage, record.FailureAttribution)
	}
	if record.AccountID != "acc_w1k" {
		t.Fatalf("AccountID = %q, want acc_w1k", record.AccountID)
	}
	if record.Success {
		t.Fatal("失败尝试 Success 必须为 false")
	}
}

func TestW1KUsageContextProjections(t *testing.T) {
	if got := attemptStatusCodeOf(gatewaydispatch.FailedAttemptRecord{StatusCode: 7}); got != nil {
		t.Fatalf("无状态码标记必须投影 nil: %v", *got)
	}
	if got := attemptStatusCodeOf(gatewaydispatch.FailedAttemptRecord{StatusCode: 7, HasStatusCode: true}); got == nil || *got != 7 {
		t.Fatalf("状态码投影错误: %v", got)
	}

	source := gatewaypreauth.GatewayFailureUsageContext{
		TraceID:                        "trace_1",
		TrafficSource:                  "gateway",
		ClientIP:                       "10.0.0.1",
		SystemAccountID:                "sys",
		APIKeyID:                       "key",
		GroupID:                        "group",
		Endpoint:                       "/v1/chat/completions",
		RequestedServiceTier:           "auto",
		EffectiveServiceTier:           "default",
		RequestedReasoningEffort:       "low",
		EffectiveReasoningEffort:       "medium",
		ProviderCode:                   "openai",
		ProviderProtocolProfileID:      "prof_1",
		ProtocolCode:                   "openai",
		ProtocolVersion:                "v1",
		GroupOwnerSystemAccountID:      "owner",
		GroupAccessType:                "group",
		GroupAuthorizationID:           "authz_1",
		GroupAuthorizationSourceType:   "team",
		GroupAuthorizationSourceTeamID: "team_1",
	}
	projected := usageContextOf(source)
	if projected.TraceID != source.TraceID || string(projected.TrafficSource) != "gateway" ||
		projected.ClientIP != source.ClientIP || projected.SystemAccountID != source.SystemAccountID ||
		projected.APIKeyID != source.APIKeyID || projected.GroupID != source.GroupID ||
		projected.Endpoint != source.Endpoint {
		t.Fatalf("用量上下文投影错误: %+v", projected)
	}
	if projected.RequestedServiceTier != "auto" || projected.EffectiveServiceTier != "default" ||
		projected.RequestedReasoningEffort != "low" || projected.EffectiveReasoningEffort != "medium" {
		t.Fatalf("服务档位/推理力度投影错误: %+v", projected)
	}

	failure := usageFailureContextOf(source)
	if failure.TraceID != source.TraceID || failure.ProviderCode != "openai" ||
		failure.ProviderProtocolProfileID != "prof_1" || failure.ProtocolCode != "openai" ||
		failure.ProtocolVersion != "v1" {
		t.Fatalf("失败上下文供应商字段投影错误: %+v", failure)
	}
	if failure.GroupOwnerSystemAccountID != "owner" || failure.GroupAccessType != "group" ||
		failure.GroupAuthorizationID != "authz_1" || failure.GroupAuthorizationSourceType != "team" ||
		failure.GroupAuthorizationSourceTeamID != "team_1" {
		t.Fatalf("失败上下文分组字段投影错误: %+v", failure)
	}
	if failure.GatewayUsageContext.APIKeyID != source.APIKeyID {
		t.Fatalf("失败上下文必须内嵌用量上下文: %+v", failure)
	}
}

// ---------------------------------------------------------------------------
// A4. usageDispatchAdapter.DispatchUsageRecord / RecordGatewayFailure
// ---------------------------------------------------------------------------

func TestW1KUsageDispatchAdapterArms(t *testing.T) {
	usageContext := gatewaypreauth.GatewayFailureUsageContext{
		TraceID:                   "trace_w1k_dispatch",
		TrafficSource:             "gateway",
		ClientIP:                  "10.0.0.2",
		SystemAccountID:           "sys_w1k_d",
		APIKeyID:                  "key_w1k_d",
		GroupID:                   "group_w1k_d",
		Endpoint:                  "/v1/chat/completions",
		ProviderCode:              "anthropic",
		GroupOwnerSystemAccountID: "sys_w1k_d",
		GroupAccessType:           "owner",
	}

	// 录制器/服务缺失时静默直通。
	empty := usageDispatchAdapter{}
	empty.DispatchUsageRecord(gatewayresponse.ModelsUsageDispatchInput{UsageContext: usageContext})
	empty.RecordGatewayFailure(gatewayresponse.FailureUsageRecordInput{UsageContext: usageContext})

	direct := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	service, spooled, dispatch := w1kUsageService()
	adapter := usageDispatchAdapter{service: service, recorder: direct}

	adapter.DispatchUsageRecord(gatewayresponse.ModelsUsageDispatchInput{
		UsageContext:  usageContext,
		ProviderCode:  "anthropic",
		UsageSemantic: "chat",
		Stream:        true,
		StatusCode:    200,
		Success:       true,
		FirstTokenMs:  120,
		DurationMs:    800,
	})
	records := direct.Records()
	if len(records) != 1 {
		t.Fatalf("直投记录数 = %d, want 1", len(records))
	}
	record := records[0]
	if record.TraceID != "trace_w1k_dispatch" || string(record.TrafficSource) != "gateway" ||
		record.ClientIP != "10.0.0.2" || record.SystemAccountID != "sys_w1k_d" ||
		record.APIKeyID != "key_w1k_d" {
		t.Fatalf("直投身份字段错误: %+v", record)
	}
	if record.ProviderCode != "anthropic" || record.Endpoint != "/v1/chat/completions" || record.UsageSemantic != "chat" {
		t.Fatalf("直投业务字段错误: %+v", record)
	}
	if record.GroupID != "" {
		// 直投记录不携带分组归属元数据，用量范围完整性归一化会丢弃 GroupID
		//（normalizeUsageRecordInput 的 group scope integrity 规则）。
		t.Fatalf("直投 GroupID 应被完整性归一化丢弃: %q", record.GroupID)
	}
	if !record.Success {
		t.Fatal("直投 Success 必须透传 true")
	}
	if record.Stream == nil || !*record.Stream {
		t.Fatalf("Stream = %v, want true", record.Stream)
	}
	if record.StatusCode == nil || *record.StatusCode != 200 {
		t.Fatalf("StatusCode = %v, want 200", record.StatusCode)
	}
	if record.FirstTokenMs == nil || *record.FirstTokenMs != 120 {
		t.Fatalf("FirstTokenMs = %v, want 120", record.FirstTokenMs)
	}
	if record.DurationMs == nil || *record.DurationMs != 800 {
		t.Fatalf("DurationMs = %v, want 800", record.DurationMs)
	}
	if record.CreatedAt == "" {
		t.Fatal("CreatedAt 必须写入时间戳")
	}

	adapter.RecordGatewayFailure(gatewayresponse.FailureUsageRecordInput{
		UsageContext:  usageContext,
		StatusCode:    503,
		StartedAtMs:   1_000,
		CompletedAtMs: 1_500,
		ErrorMessage:  "上游 503",
		ResponsePayload: gatewayresponse.GatewayErrorPayloadCarrier{
			Error: map[string]any{"message": "上游过载"},
			Extra: map[string]any{"upstream": "edge-1"},
		},
	})
	if !dispatch.WaitForIdle(2000) {
		t.Fatal("失败用量收尾未在超时内完成")
	}
	failures := spooled.Records()
	if len(failures) != 1 {
		t.Fatalf("失败记录数 = %d, want 1", len(failures))
	}
	failed := failures[0]
	if failed.Success {
		t.Fatal("失败记录 Success 必须为 false")
	}
	if failed.TraceID != "trace_w1k_dispatch" || failed.ProviderCode != "anthropic" || failed.Endpoint != "/v1/chat/completions" {
		t.Fatalf("失败记录身份字段错误: %+v", failed)
	}
	if failed.StatusCode == nil || *failed.StatusCode != 503 {
		t.Fatalf("失败记录 StatusCode = %v, want 503", failed.StatusCode)
	}
	if failed.ErrorMessage != "上游 503" {
		t.Fatalf("失败记录 ErrorMessage = %q, want 上游 503", failed.ErrorMessage)
	}
	if failed.GroupID != "group_w1k_d" {
		t.Fatalf("带分组归属快照时 GroupID 必须保留: %q", failed.GroupID)
	}
}

// ---------------------------------------------------------------------------
// B5. chainClientSourceAvoidance.OrderAsync
// ---------------------------------------------------------------------------

func TestW1KClientSourceAvoidanceOrderAsync(t *testing.T) {
	ctx := context.Background()
	turnRetry := &gatewaycodex.TurnRetryService{Secret: "w1k-source-secret"}
	adapter := &chainClientSourceAvoidance{turnRetry: turnRetry}
	strategy := gatewaycodex.OpenAIGatewayClientStrategyContext{
		ClientSourceAvoidanceStateKey:     "w1k-source-state",
		AllowClientSourceAccountAvoidance: true,
	}
	clientStrategy := gatewaypreauth.ClientStrategyContext{Opaque: strategy}
	accounts := []gatewaydispatch.AccountCandidate{
		{ID: "acc-1", Priority: 10},
		{ID: "acc-2", Priority: 10},
		{ID: "acc-3", Priority: 10},
	}

	ordered, err := adapter.OrderAsync(ctx, accounts, clientStrategy, nil)
	if err != nil {
		t.Fatalf("无失败记录时排序失败: %v", err)
	}
	if ordered.Applied || ordered.ThresholdReached || len(ordered.AvoidedAccountIDs) != 0 {
		t.Fatalf("无失败记录必须保持直通: %+v", ordered)
	}
	if len(ordered.Accounts) != 3 || ordered.Accounts[0].ID != "acc-1" || ordered.Accounts[2].ID != "acc-3" {
		t.Fatalf("直通顺序错误: %v", ordered.Accounts)
	}

	// 两次失败达到激活阈值，同层内被避让账户后置。
	for i := 0; i < 2; i++ {
		if _, err := turnRetry.RememberGatewayClientSourceFailureAsync(ctx, strategy, "acc-2", gatewaycodex.CodexTurnFailureInput{ErrorCode: "timeout"}); err != nil {
			t.Fatalf("记录来源失败 %d: %v", i+1, err)
		}
	}
	ordered, err = adapter.OrderAsync(ctx, accounts, clientStrategy, nil)
	if err != nil {
		t.Fatalf("避让排序失败: %v", err)
	}
	if !ordered.Applied || !ordered.ThresholdReached {
		t.Fatalf("避让必须生效: %+v", ordered)
	}
	if len(ordered.AvoidedAccountIDs) != 1 || ordered.AvoidedAccountIDs[0] != "acc-2" {
		t.Fatalf("被避让账户 = %v, want [acc-2]", ordered.AvoidedAccountIDs)
	}
	if len(ordered.Accounts) != 3 || ordered.Accounts[0].ID != "acc-1" || ordered.Accounts[1].ID != "acc-3" || ordered.Accounts[2].ID != "acc-2" {
		t.Fatalf("避让顺序错误: %v", ordered.Accounts)
	}

	// 策略未开启来源避让时保持直通。
	disabled := gatewaypreauth.ClientStrategyContext{Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{}}
	ordered, err = adapter.OrderAsync(ctx, accounts, disabled, nil)
	if err != nil {
		t.Fatalf("未启用策略排序失败: %v", err)
	}
	if ordered.Applied || ordered.Accounts[0].ID != "acc-1" {
		t.Fatalf("未启用策略必须直通: %+v", ordered)
	}
}

// ---------------------------------------------------------------------------
// B5. chainClientIPAvoidance.OrderAsync
// ---------------------------------------------------------------------------

func w1kIPAvoidanceScope() gatewaydispatch.ClientIPAvoidanceScope {
	return gatewaydispatch.ClientIPAvoidanceScope{
		SystemAccountID: "sys_w1k_ip",
		APIKeyID:        "key_w1k_ip",
		GroupID:         "group_w1k_ip",
		ClientIP:        "10.0.0.31",
	}
}

func TestW1KClientIPAvoidanceOrderAsync(t *testing.T) {
	ctx := context.Background()
	scope := w1kIPAvoidanceScope()

	// avoidance 缺失时直通。
	passthrough := newChainClientIPAvoidance(nil)
	ordered, err := passthrough.OrderAsync(ctx, []gatewaydispatch.AccountCandidate{{ID: "acc-only"}}, scope, nil)
	if err != nil {
		t.Fatalf("缺失避让服务必须直通: %v", err)
	}
	if ordered.Applied || len(ordered.Accounts) != 1 || ordered.Accounts[0].ID != "acc-only" {
		t.Fatalf("缺失避让服务直通结果错误: %+v", ordered)
	}

	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{Clock: gatewaypreauth.SystemClock{}})
	if err != nil {
		t.Fatalf("创建避让服务: %v", err)
	}
	t.Cleanup(avoidance.Close)
	adapter := newChainClientIPAvoidance(avoidance)

	// 空账户列表直通。
	ordered, err = adapter.OrderAsync(ctx, nil, scope, nil)
	if err != nil || ordered.Applied || len(ordered.Accounts) != 0 {
		t.Fatalf("空账户列表必须直通: %+v, %v", ordered, err)
	}

	confirmFailure := func() {
		t.Helper()
		tracker := avoidance.CreateAvoidanceTracker(gatewayclientip.AvoidanceScopeInput{
			SystemAccountID: scope.SystemAccountID,
			APIKeyID:        scope.APIKeyID,
			GroupID:         scope.GroupID,
			ClientIP:        scope.ClientIP,
		})
		avoidance.RememberPendingFailure(tracker, "acc-bad", "Bad", gatewayclientip.AccountFailure{ErrorPhase: "upstream_request"})
		avoidance.ConfirmAfterFinalFailure(tracker, nil)
	}
	accounts := []gatewaydispatch.AccountCandidate{
		{ID: "acc-bad", Priority: 5},
		{ID: "acc-good", Priority: 5},
	}

	// 阈值（2 次）之前不避让。
	confirmFailure()
	ordered, err = adapter.OrderAsync(ctx, accounts, scope, nil)
	if err != nil {
		t.Fatalf("阈值前排序失败: %v", err)
	}
	if ordered.Applied || len(ordered.AvoidedAccountIDs) != 0 {
		t.Fatalf("阈值前不得避让: %+v", ordered)
	}

	// 达到阈值后同层内被避让账户后置。
	confirmFailure()
	ordered, err = adapter.OrderAsync(ctx, accounts, scope, nil)
	if err != nil {
		t.Fatalf("避让排序失败: %v", err)
	}
	if !ordered.Applied {
		t.Fatalf("避让必须生效: %+v", ordered)
	}
	if len(ordered.AvoidedAccountIDs) != 1 || ordered.AvoidedAccountIDs[0] != "acc-bad" {
		t.Fatalf("被避让账户 = %v, want [acc-bad]", ordered.AvoidedAccountIDs)
	}
	if len(ordered.Accounts) != 2 || ordered.Accounts[0].ID != "acc-good" || ordered.Accounts[1].ID != "acc-bad" {
		t.Fatalf("避让顺序错误: %v", ordered.Accounts)
	}
}

// ---------------------------------------------------------------------------
// B6. chainDispatchQuota.CheckBatchAsync
// ---------------------------------------------------------------------------

func TestW1KDispatchQuotaCheckBatchAsync(t *testing.T) {
	fixture := newChainFixture(t)
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool-w1k-quota"))
	quota := newChainDispatchQuota(deps.AuthzQuota)
	ctx := context.Background()

	decisions, err := quota.CheckBatchAsync(ctx, gatewayruntimecache.GroupUsageAccessMetadata{}, []gatewaydispatch.AccountCandidate{
		{ID: fixture.accountID},
		{ID: "acc_w1k_quota"},
	})
	if err != nil {
		t.Fatalf("批量配额检查失败: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("决策数 = %d, want 2: %+v", len(decisions), decisions)
	}
	for _, accountID := range []string{fixture.accountID, "acc_w1k_quota"} {
		decision, ok := decisions[accountID]
		if !ok || !decision.Allowed {
			t.Fatalf("账户 %s 决策 = %+v, want 放行", accountID, decision)
		}
	}

	// 未绑定授权 ID 的候选投影为空授权摘要（无分组配额时不进入检查面）。
	limited := true
	decisions, err = quota.CheckBatchAsync(ctx, gatewayruntimecache.GroupUsageAccessMetadata{}, []gatewaydispatch.AccountCandidate{
		{ID: "acc_w1k_limited", AccountAuthorizationQuotaLimited: &limited},
	})
	if err != nil || len(decisions) != 1 || !decisions["acc_w1k_limited"].Allowed {
		t.Fatalf("配额受限标记候选决策错误: %+v, %v", decisions, err)
	}
}

// ---------------------------------------------------------------------------
// B7. filterConfiguredPolicyAvoidances / FilterAsync
// ---------------------------------------------------------------------------

// w1kInnerSuppression 是 SuppressionPort 的内层 fake：回显幸存者并附加固定
// 内层屏蔽名单（可注入错误）。
type w1kInnerSuppression struct {
	calls        int
	lastAccounts []gatewaydispatch.AccountCandidate
	suppressed   []string
	err          error
}

func (s *w1kInnerSuppression) FilterAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ gatewaydispatch.SuppressionFilterOptions) (gatewaydispatch.SuppressionFilterResult, error) {
	s.calls++
	s.lastAccounts = append([]gatewaydispatch.AccountCandidate(nil), accounts...)
	if s.err != nil {
		return gatewaydispatch.SuppressionFilterResult{}, s.err
	}
	return gatewaydispatch.SuppressionFilterResult{
		Accounts:             accounts,
		SuppressedCount:      len(s.suppressed),
		SuppressedAccountIDs: s.suppressed,
	}, nil
}

func (s *w1kInnerSuppression) ResolveLocalSuppressionFilter(_ context.Context, input gatewaydispatch.LocalSuppressionPreflightInput) (*gatewaydispatch.SuppressionFilterResult, bool, error) {
	result := gatewaydispatch.SuppressionFilterResult{Accounts: input.Accounts}
	return &result, false, nil
}

func TestW1KFilterConfiguredPolicyAvoidances(t *testing.T) {
	base := time.Now().UTC()
	service := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, w1kFixedClock{now: base})
	inner := &w1kInnerSuppression{}
	suppression := &chainConfiguredPolicyAvoidanceSuppression{inner: inner, avoidance: service}
	ctx := context.Background()
	accounts := []gatewaydispatch.AccountCandidate{
		{ID: "acc-a", AccountAccessType: "owner"},
		{ID: "acc-b", AccountAccessType: "owner"},
		// 授权账户缺少绑定上下文 → 运行态键推导失败回退账户 ID。
		{ID: "acc-authz", AccountAccessType: "account_authorized"},
	}

	result, err := suppression.filterConfiguredPolicyAvoidances(ctx, accounts)
	if err != nil {
		t.Fatalf("未避让过滤失败: %v", err)
	}
	if len(result.Accounts) != 3 || result.SuppressedCount != 0 || result.AllSuppressed || result.NextRetryAfterMs != nil {
		t.Fatalf("未避让结果错误: %+v", result)
	}

	// 24h 窗口避让 acc-b（wall-clock 断言保留 ±1h 余量）。
	long := int64(24 * 3600)
	if err := service.SuppressGatewayAccountLocallyForSeconds(ctx, gatewayaccounteffects.SuppressibleGatewayAccount{ID: "acc-b", AccountAccessType: "owner"}, &long, "w1k 测试避让"); err != nil {
		t.Fatalf("写入避让状态失败: %v", err)
	}
	result, err = suppression.filterConfiguredPolicyAvoidances(ctx, accounts)
	if err != nil {
		t.Fatalf("避让过滤失败: %v", err)
	}
	if len(result.Accounts) != 2 || result.SuppressedCount != 1 {
		t.Fatalf("避让后可见面错误: %+v", result)
	}
	if result.Accounts[0].ID != "acc-a" || result.Accounts[1].ID != "acc-authz" {
		t.Fatalf("可见账户 = %v, want [acc-a acc-authz]", []string{result.Accounts[0].ID, result.Accounts[1].ID})
	}
	if len(result.SuppressedAccountIDs) != 1 || result.SuppressedAccountIDs[0] != "acc-b" {
		t.Fatalf("被避让名单 = %v, want [acc-b]", result.SuppressedAccountIDs)
	}
	if len(result.ConfiguredPolicySuppressedAccountIDs) != 1 || result.ConfiguredPolicySuppressedAccountIDs[0] != "acc-b" {
		t.Fatalf("配置策略避让名单 = %v", result.ConfiguredPolicySuppressedAccountIDs)
	}
	if result.NextRetryAfterMs == nil || *result.NextRetryAfterMs <= 23*3600*1000 || *result.NextRetryAfterMs > 24*3600*1000 {
		t.Fatalf("最早重试时间 = %v, 超出 24h 窗口余量", result.NextRetryAfterMs)
	}
	if result.AllSuppressed {
		t.Fatal("仍有幸存者时 AllSuppressed 必须为 false")
	}

	t.Run("FilterAsync 合并内层屏蔽", func(t *testing.T) {
		base := time.Now().UTC()
		service := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, w1kFixedClock{now: base})
		inner := &w1kInnerSuppression{suppressed: []string{"acc-a"}}
		suppression := &chainConfiguredPolicyAvoidanceSuppression{inner: inner, avoidance: service}
		ctx := context.Background()
		accounts := []gatewaydispatch.AccountCandidate{
			{ID: "acc-a", AccountAccessType: "owner"},
			{ID: "acc-b", AccountAccessType: "owner"},
		}
		long := int64(24 * 3600)
		if err := service.SuppressGatewayAccountLocallyForSeconds(ctx, gatewayaccounteffects.SuppressibleGatewayAccount{ID: "acc-b", AccountAccessType: "owner"}, &long, "w1k 合并避让"); err != nil {
			t.Fatalf("写入避让状态失败: %v", err)
		}

		merged, err := suppression.FilterAsync(ctx, accounts, gatewaydispatch.SuppressionFilterOptions{})
		if err != nil {
			t.Fatalf("FilterAsync 失败: %v", err)
		}
		if inner.calls != 1 {
			t.Fatalf("内层过滤调用数 = %d, want 1", inner.calls)
		}
		if len(inner.lastAccounts) != 1 || inner.lastAccounts[0].ID != "acc-a" {
			t.Fatalf("内层只应收到幸存者: %v", inner.lastAccounts)
		}
		if merged.SuppressedCount != 2 {
			t.Fatalf("合并屏蔽计数 = %d, want 2", merged.SuppressedCount)
		}
		if len(merged.SuppressedAccountIDs) != 2 {
			t.Fatalf("合并屏蔽名单 = %v, want acc-a + acc-b", merged.SuppressedAccountIDs)
		}
		if len(merged.ConfiguredPolicySuppressedAccountIDs) != 1 || merged.ConfiguredPolicySuppressedAccountIDs[0] != "acc-b" {
			t.Fatalf("配置策略名单必须只保留配置策略来源: %v", merged.ConfiguredPolicySuppressedAccountIDs)
		}
		if merged.AllSuppressed {
			t.Fatal("仍有幸存者时 AllSuppressed 必须为 false")
		}
		if merged.NextRetryAfterMs == nil {
			t.Fatal("配置策略重试时间必须保留")
		}
	})

	t.Run("全部被配置策略避让时短路内层", func(t *testing.T) {
		base := time.Now().UTC()
		service := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, w1kFixedClock{now: base})
		inner := &w1kInnerSuppression{}
		suppression := &chainConfiguredPolicyAvoidanceSuppression{inner: inner, avoidance: service}
		ctx := context.Background()
		accounts := []gatewaydispatch.AccountCandidate{
			{ID: "acc-a", AccountAccessType: "owner"},
			{ID: "acc-b", AccountAccessType: "owner"},
		}
		long := int64(24 * 3600)
		for _, accountID := range []string{"acc-a", "acc-b"} {
			if err := service.SuppressGatewayAccountLocallyForSeconds(ctx, gatewayaccounteffects.SuppressibleGatewayAccount{ID: accountID, AccountAccessType: "owner"}, &long, "w1k 全量避让"); err != nil {
				t.Fatalf("写入 %s 避让状态失败: %v", accountID, err)
			}
		}

		result, err := suppression.FilterAsync(ctx, accounts, gatewaydispatch.SuppressionFilterOptions{})
		if err != nil {
			t.Fatalf("FilterAsync 失败: %v", err)
		}
		if inner.calls != 0 {
			t.Fatalf("全量避让时内层不得被调用, 实际 %d 次", inner.calls)
		}
		if !result.AllSuppressed || len(result.Accounts) != 0 || result.SuppressedCount != 2 {
			t.Fatalf("全量避让结果错误: %+v", result)
		}
		if result.NextRetryAfterMs == nil || *result.NextRetryAfterMs <= 0 {
			t.Fatalf("全量避让必须给出最早重试时间: %v", result.NextRetryAfterMs)
		}
	})

	t.Run("内层错误向上传播", func(t *testing.T) {
		base := time.Now().UTC()
		service := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, w1kFixedClock{now: base})
		inner := &w1kInnerSuppression{err: errors.New("内层屏蔽读取失败")}
		suppression := &chainConfiguredPolicyAvoidanceSuppression{inner: inner, avoidance: service}
		_, err := suppression.FilterAsync(context.Background(),
			[]gatewaydispatch.AccountCandidate{{ID: "acc-clean", AccountAccessType: "owner"}},
			gatewaydispatch.SuppressionFilterOptions{})
		if err == nil || !strings.Contains(err.Error(), "内层屏蔽读取失败") {
			t.Fatalf("内层错误必须向上传播: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// B8. chainResponseAccountEffects.ApplyInspectionPolicySideEffects
// ---------------------------------------------------------------------------

// w1kPlainAccountView 是 gatewayresponse.AccountView 的非 OpenAI 实现（视图
// 还原回落分支覆盖）。
type w1kPlainAccountView struct{ id string }

func (v w1kPlainAccountView) GetID() string                        { return v.id }
func (v w1kPlainAccountView) GetName() string                      { return "" }
func (v w1kPlainAccountView) GetProviderCode() string              { return "" }
func (v w1kPlainAccountView) GetProviderProtocolProfileID() string { return "" }
func (v w1kPlainAccountView) GetProtocolCode() string              { return "" }
func (v w1kPlainAccountView) GetProtocolVersion() string           { return "" }
func (v w1kPlainAccountView) GetClientCompatibility() string       { return "" }

func TestW1KResponseAccountEffectsInspectionPolicyArms(t *testing.T) {
	fixture := newChainFixture(t)
	ctx := context.Background()
	avoidance := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, nil)
	effects := &chainResponseAccountEffects{cache: fixture.cache, avoidance: avoidance}
	var view gatewayresponse.AccountView = gatewayresponse.OpenAIAccountView{
		Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-w1k-policy", AccountAccessType: "owner"},
	}
	configured := &gatewayresponse.ResponseInspectionDecision{
		Reason: "configured_response_policy",
		Action: "replace_with_failure",
	}

	// 守卫分支：决策为空 / 状态变更关闭 / 非配置策略 / dry_run / 未知视图。
	guards := []struct {
		name     string
		decision *gatewayresponse.ResponseInspectionDecision
		mutate   bool
		target   gatewayresponse.AccountView
	}{
		{"决策为空", nil, true, view},
		{"账户状态变更关闭", configured, false, view},
		{"非配置策略原因", &gatewayresponse.ResponseInspectionDecision{Reason: "before_downstream_write_response_failure", Action: "replace_with_failure"}, true, view},
		{"dry_run", &gatewayresponse.ResponseInspectionDecision{Reason: "configured_response_policy", Action: "dry_run"}, true, view},
		{"未知账户视图", configured, true, w1kPlainAccountView{id: "acc-plain"}},
	}
	for _, guard := range guards {
		if err := effects.ApplyInspectionPolicySideEffects(guard.decision, guard.target, guard.mutate); err != nil {
			t.Fatalf("%s 必须直接跳过: %v", guard.name, err)
		}
	}
	states, err := avoidance.LoadConfiguredPolicyAvoidanceStates(ctx, []string{"acc-w1k-policy"})
	if err != nil || len(states) != 1 || states[0] != nil {
		t.Fatalf("守卫分支不得写入避让状态: %+v, %v", states, err)
	}

	// runtime_avoidance 决策写入配置策略避让（TTL 来自系统设置，断言未过期）。
	decision := &gatewayresponse.ResponseInspectionDecision{
		Reason:       "configured_response_policy",
		Action:       "replace_with_failure",
		AccountState: "runtime_avoidance",
		PolicyName:   "w1k 避让策略",
	}
	if err := effects.ApplyInspectionPolicySideEffects(decision, view, true); err != nil {
		t.Fatalf("runtime_avoidance 副作用失败: %v", err)
	}
	states, err = avoidance.LoadConfiguredPolicyAvoidanceStates(ctx, []string{"acc-w1k-policy"})
	if err != nil || len(states) != 1 {
		t.Fatalf("读取避让状态失败: %+v, %v", states, err)
	}
	if states[0] == nil || states[0].AccountID != "acc-w1k-policy" || states[0].UntilMs <= time.Now().UnixMilli() {
		t.Fatalf("避让状态错误: %+v", states[0])
	}
	if states[0].Reason == "" || !strings.Contains(states[0].Reason, "w1k 避让策略") {
		t.Fatalf("避让原因必须携带策略名: %q", states[0].Reason)
	}

	// avoid_upstream_bucket_ttl 决策写入上游桶避让（memory 驱动）。
	bucketEffects := &chainResponseAccountEffects{
		cache:       fixture.cache,
		avoidance:   gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, nil),
		proxyHealth: gatewayproxyhealth.NewProxyHealthService(func() time.Time { return time.Now() }, nil, gatewayproxyhealth.ProxyHealthOptions{}, nil),
	}
	bucketDecision := &gatewayresponse.ResponseInspectionDecision{
		Reason:        "configured_response_policy",
		Action:        "replace_with_failure",
		AccountSwitch: "avoid_upstream_bucket_ttl",
		PolicyID:      "pol-w1k",
	}
	if err := bucketEffects.ApplyInspectionPolicySideEffects(bucketDecision, view, true); err != nil {
		t.Fatalf("avoid_upstream_bucket_ttl 副作用失败: %v", err)
	}
}

// ---------------------------------------------------------------------------
// D10. runtime.go：businessOwnerGate / normalizeAllowedOrigin / corsPolicy /
// upstreamURLSecurityConfig（trustProxyConfig 与
// temporaryAccessIPAllowlistConfig 已由 runtime_w4g_test.go 覆盖，不再重复）
// ---------------------------------------------------------------------------

func TestW1KBusinessOwnerGateArms(t *testing.T) {
	valid := runtimeConfig{
		SystemAPIEnabled:            true,
		BusinessOwner:               "gateway",
		BusinessHandoffConfirmed:    true,
		BusinessNodeWriterStopped:   true,
		BusinessSchemaReady:         true,
		BusinessOwnerEpoch:          "epoch-w1k",
		BusinessCutoverEvidencePath: "evidence-w1k.json",
		DatabaseDriver:              "sqlite",
		BusinessDatabasePath:        "data/business.sqlite3",
	}

	disabled := runtimeConfig{}
	if err := disabled.businessOwnerGate(); err != nil {
		t.Fatalf("未启用系统 API 必须直通: %v", err)
	}
	if err := valid.businessOwnerGate(); err != nil {
		t.Fatalf("完整 sqlite 交接配置必须通过: %v", err)
	}
	postgres := valid
	postgres.DatabaseDriver = "postgres"
	postgres.BusinessPostgresURL = "postgres://user:pass@127.0.0.1:5432/juhe"
	if err := postgres.businessOwnerGate(); err != nil {
		t.Fatalf("完整 postgres 交接配置必须通过: %v", err)
	}

	fails := []struct {
		name   string
		mutate func(*runtimeConfig)
		want   string
	}{
		{"owner 非 gateway", func(c *runtimeConfig) { c.BusinessOwner = "jobs" }, "JUHE_AI_BUSINESS_OWNER"},
		{"交接未确认", func(c *runtimeConfig) { c.BusinessHandoffConfirmed = false }, "JUHE_AI_BUSINESS_HANDOFF_CONFIRMED"},
		{"Node writer 未停止", func(c *runtimeConfig) { c.BusinessNodeWriterStopped = false }, "JUHE_AI_BUSINESS_NODE_WRITER_STOPPED"},
		{"schema 未就绪", func(c *runtimeConfig) { c.BusinessSchemaReady = false }, "JUHE_AI_BUSINESS_SCHEMA_READY"},
		{"缺少 epoch", func(c *runtimeConfig) { c.BusinessOwnerEpoch = "" }, "JUHE_AI_BUSINESS_OWNER_EPOCH"},
		{"缺少交接证据", func(c *runtimeConfig) { c.BusinessCutoverEvidencePath = "" }, "JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH"},
		{"postgres 缺少 URL", func(c *runtimeConfig) { c.DatabaseDriver = "postgres" }, "JUHE_AI_BUSINESS_POSTGRES_URL"},
		{"sqlite 缺少路径", func(c *runtimeConfig) { c.BusinessDatabasePath = "" }, "JUHE_AI_BUSINESS_DATABASE_PATH"},
	}
	for _, fail := range fails {
		candidate := valid
		fail.mutate(&candidate)
		err := candidate.businessOwnerGate()
		if err == nil || !strings.Contains(err.Error(), fail.want) {
			t.Fatalf("%s 必须失败并包含 %s: %v", fail.name, fail.want, err)
		}
	}
}

func TestW1KNormalizeAllowedOriginArms(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    string
		wantErr string
	}{
		{"大写协议主机默认端口与尾斜杠", "HTTPS://Admin.Example.com:443/", "https://admin.example.com", ""},
		{"http 默认端口剥离", "http://LocalHost:80", "http://localhost", ""},
		{"自定义端口保留", "https://ops.Example.com:8443", "https://ops.example.com:8443", ""},
		{"根路径等价无路径", "https://admin.example.com/", "https://admin.example.com", ""},
		{"非 http 协议", "ftp://admin.example.com", "", "只允许 http 或 https"},
		{"带路径", "https://admin.example.com/app", "", "只能填写 Origin"},
		{"带查询", "https://admin.example.com?x=1", "", "只能填写 Origin"},
		{"带用户信息", "https://user:pass@admin.example.com", "", "只能填写 Origin"},
		{"带片段", "https://admin.example.com/#section", "", "只能填写 Origin"},
		{"非 Origin 字符串", "not-an-origin", "", "包含无效 Origin"},
		{"通配符", "*", "", "包含无效 Origin"},
		{"空值", "", "", "包含无效 Origin"},
	}
	for _, testCase := range cases {
		got, err := normalizeAllowedOrigin("JUHE_AI_ALLOWED_ORIGINS", testCase.value)
		if testCase.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("%s(%q) 必须失败并包含 %q: %v", testCase.name, testCase.value, testCase.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s(%q) 失败: %v", testCase.name, testCase.value, err)
		}
		if got != testCase.want {
			t.Fatalf("%s(%q) = %q, want %q", testCase.name, testCase.value, got, testCase.want)
		}
	}
}

func TestW1KCorsPolicyProjection(t *testing.T) {
	cfg := runtimeConfig{
		CORSAllowAnyOrigin: true,
		CORSAllowedOrigins: []string{"https://admin.example.com", "https://ops.example.com:8443"},
	}
	policy := cfg.corsPolicy()
	if !policy.AllowAnyOrigin {
		t.Fatal("AllowAnyOrigin 必须透传 true")
	}
	if len(policy.AllowedOrigins) != 2 || policy.AllowedOrigins[0] != "https://admin.example.com" || policy.AllowedOrigins[1] != "https://ops.example.com:8443" {
		t.Fatalf("AllowedOrigins 投影错误: %v", policy.AllowedOrigins)
	}
}

func TestW1KUpstreamURLSecurityConfigArms(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}

	config, err := upstreamURLSecurityConfig(false, env(nil))
	if err != nil {
		t.Fatalf("缺省配置必须成功: %v", err)
	}
	if config.AllowPrivateBaseUrls || len(config.PrivateOriginAllowlist) != 0 {
		t.Fatalf("缺省配置必须全部关闭: %+v", config)
	}

	if _, err := upstreamURLSecurityConfig(false, env(map[string]string{
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS": "bogus",
	})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS") {
		t.Fatalf("非法布尔值必须报错: %v", err)
	}

	if _, err := upstreamURLSecurityConfig(true, env(map[string]string{
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS": "true",
	})); err == nil || !strings.Contains(err.Error(), "生产环境不能启用") {
		t.Fatalf("生产信号下必须拒绝私有上游开关: %v", err)
	}

	config, err = upstreamURLSecurityConfig(false, env(map[string]string{
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS":    "true",
		"JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST": "http://10.0.0.5:8080 , https://192.168.1.5 , , ",
	}))
	if err != nil {
		t.Fatalf("开发配置必须成功: %v", err)
	}
	if !config.AllowPrivateBaseUrls {
		t.Fatal("开发环境必须允许开启私有上游")
	}
	if !config.PrivateOriginAllowlist["http://10.0.0.5:8080"] || !config.PrivateOriginAllowlist["https://192.168.1.5:443"] {
		t.Fatalf("白名单键规范化错误: %v", config.PrivateOriginAllowlist)
	}

	if _, err := upstreamURLSecurityConfig(false, env(map[string]string{
		"JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST": "http://internal.example.com",
	})); err == nil {
		t.Fatal("域名白名单条目必须拒绝")
	}
}
