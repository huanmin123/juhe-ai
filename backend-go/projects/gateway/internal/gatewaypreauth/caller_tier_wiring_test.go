package gatewaypreauth

// 调用方分级影子观测信号接线测试（调用方分级调度设计 §3/§8 M1）：preflight
// 在本地模型过滤失败按调用方责任（4xx 请求校验态 invalid_request_error）渲
// 染终态时，向 CallerTier 口透传恰一次 model_filter_failed 信号；503 容量侧
// 与 429 额度侧在设计排除清单内不透传；口未装配（nil，newTestService 默认）
// 时全部既有用例即其零改动证明。信号是影子观测：终态响应语义逐字段不变。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycallertier"
)

// newCallerTierTestRecorder 构造接真 MemoryStore 的影子记录器（设计 §6.1
// memory 部署形态；测试内不触网），返回记录器与可快照的存储。
func newCallerTierTestRecorder(t *testing.T) (*gatewaycallertier.Recorder, *gatewaycallertier.MemoryStore) {
	t.Helper()
	store := gatewaycallertier.NewMemoryStore(time.Now)
	recorder, err := gatewaycallertier.NewRecorder(store, gatewaycallertier.Metrics{}, nil)
	if err != nil {
		t.Fatalf("构造 caller tier recorder: %v", err)
	}
	return recorder, store
}

// TestWhPreflightModelFilterFailureRecordsCallerTierSignal：4xx 本地模型过滤
// 失败（结构对齐 wh_preauth_preflight_test.go 的 model_not_allowed 404 形态）
// 透传一次 model_filter_failed，且失败响应语义不变。
func TestWhPreflightModelFilterFailureRecordsCallerTierSignal(t *testing.T) {
	recorder, store := newCallerTierTestRecorder(t)
	service, _, sink := newTestService(t, func(s *Service) {
		s.CallerTier = recorder
		s.RuntimeCache = whRuntimeCacheFor(nil)
		s.RouteResolver = &fakeRouteResolver{normal: NormalRouteResult{
			Outcome: NormalRouteOutcomeFailed, StatusCode: 404, Type: "invalid_request_error",
			Code: "model_not_allowed", Message: "模型不在任何绑定分组中",
		}}
	})
	req, _, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
		StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	// 影子边界：失败响应语义逐字段不变（404 + model_not_allowed + 请求校验态）。
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != 404 || failure.Audit.ErrorCode != "model_not_allowed" {
		t.Fatalf("失败 = %+v", failure)
	}
	if failure.Audit.ErrorPhase != "request_validation" {
		t.Fatalf("错误阶段 = %q", failure.Audit.ErrorPhase)
	}
	// 信号恰一次，落在已认证 Key（key_1）上；分母随同一次 Mutate 计入。
	snapshot := store.Snapshot()
	state, ok := snapshot["key_1"]
	if !ok {
		t.Fatalf("缺少 key_1 状态: %v", snapshot)
	}
	if state.DaySignals != 1 || state.DayRequests != 1 {
		t.Fatalf("信号=%d 分母=%d，期望各 1", state.DaySignals, state.DayRequests)
	}
}

// TestWhPreflightCapacityAndQuotaFailuresSkipCallerTierSignal：503 分组无可用
// 账号（容量侧）与 429 授权配额（额度侧）都不透传信号（设计 §3 排除清单）。
func TestWhPreflightCapacityAndQuotaFailuresSkipCallerTierSignal(t *testing.T) {
	cases := []struct {
		name   string
		result NormalRouteResult
	}{
		{
			name: "503 容量侧不透传",
			result: NormalRouteResult{
				Outcome: NormalRouteOutcomeFailed, StatusCode: 503, Type: "service_unavailable",
				Code: "model_target_group_unavailable", Message: "请求模型对应的供应商分组当前没有可用账号：gpt-test",
			},
		},
		{
			name: "429 额度侧不透传",
			result: NormalRouteResult{
				Outcome: NormalRouteOutcomeFailed, StatusCode: 429, Type: "rate_limit_exceeded",
				Code: "rate_limit_exceeded", Message: "授权配额已用尽",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder, store := newCallerTierTestRecorder(t)
			service, _, sink := newTestService(t, func(s *Service) {
				s.CallerTier = recorder
				s.RuntimeCache = whRuntimeCacheFor(nil)
				s.RouteResolver = &fakeRouteResolver{normal: tc.result}
			})
			req, _, writer := whAuthorizedChatRequest()
			if _, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
				Req: req, Res: writer, AuditCapture: &fakeAuditCapture{}, Options: &PreflightOptions{},
				StartedAt: 1, TraceID: "trace", Endpoint: "POST /v1/chat/completions",
			}); err != nil {
				t.Fatal(err)
			}
			if failure, ok := sink.lastFailure(); !ok || failure.StatusCode != tc.result.StatusCode {
				t.Fatalf("失败 = %+v，期望 %d", failure, tc.result.StatusCode)
			}
			if snapshot := store.Snapshot(); len(snapshot) != 0 {
				t.Fatalf("排除清单失败不得计信号: %v", snapshot)
			}
		})
	}
}
