package main

// w1（chain_v1.go 补测批次）：覆盖此前零直接命中的纯逻辑投影与可构造分支。
// 范围（对照 chain_v1.go）：
//
//  1. resolveRouteAction / actionFallbackOptions / finalizeRouteAction 的
//     路由动作回退循环（routes.ts:373-487 等价面）；
//  2. switchToFallbackGroup 的纯分支（affinity 跳过、跳数上限、记录选择、
//     耗尽集移交、候选解析错误传播；完整 preflight 分支由
//     chain_v1_recovery_test.go 的端到端测试覆盖）；
//  3. 耗尽集单账号写入、耗尽 503 渲染（speed-first 变体）；
//  4. speed-first 投影与决策闭包（chainFirstByteConfigOf /
//     speedFirstDecisionsOf / speedFirstLatencyScopeOf /
//     speedFirstRouteEligibleDispatchAccounts / speedFirstSlotAcquirer /
//     speedFirstReservationViewOf / speedFirstReservationHandleOf /
//     onNormalRouteFirstByteDeadline / speedFirstDeadlineDecision /
//     observeSpeedFirstResponseOutcome / warnSpeedFirstDecisionFailure /
//     settleSpeedFirstCutoverError / 预留 attach 记录）；
//  5. 纯投影小函数（chainSpeedFirstMaxRetriesOf / chainAccountIDsOf /
//     chainSchedulingPolicyValueOf / chainRuntimeKeyOfCandidate /
//     chainConcurrencyAccountIDOf / chainCutoverTargetsOf /
//     streamServerRetryFallbackReason / classifyGatewayDispatchExhaustion /
//     routeStrategyIDOf / dbServiceUnavailableMessage / clientStrategyPreCommitFailureSignal /
//     clientStrategyViewOf / isEmptyPreflightResult / stringSetKeys）；
//  6. 请求级快照与适配器（usageRequestSnapshotOf / rawBodySnapshotOf /
//     requestClientIP / writableEndedOf / clientErrorProtocol /
//     newRequestBudgets / newAuditCapture / engineAuditCapture /
//     auditCaptureConcrete / responseAuditCaptureOf / chainResponseAuditCapture 各方法）；
//  7. settleDispatchError 的可同步错误分支与 handleOrchestratorError。
//
// 不可构造而跳过的部分：handleUpstreamResponse（依赖
// gatewaydispatch.GatewayUpstreamResponse 的未导出 status 字段，包外无法
// 构造，也无导出构造器）、switchToFallbackGroup / resolveRouteAction 中
// 「fallback preflight 返回真实上下文」的续环分支（需要完整 preflight 服务
// 装配，由 chain_v1_recovery_test.go 端到端覆盖）、
// NormalRouteFirstByteAttemptCoordinator 的 superseded attach 竞态（内部状态
// 不可注入）。全部测试为确定性同步驱动：固定时间注入、无真实网络、
// 无 goroutine/ticker 等待。

import (
	"context"
	"errors"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// 测试替身（w1v 前缀，全部同步、确定性）
// ---------------------------------------------------------------------------

// w1vCandidatePipelineStub 桩化 gatewaypreauth.CandidatePipeline：仅
// ResolveNextGroupFallbackCandidate 参与断言（捕获入参、可注入错误）。
type w1vCandidatePipelineStub struct {
	resolveErr    error
	found         bool
	captured      *gatewaypreauth.GroupFallbackCandidateInput
	resolveCalls  int
	filterCalled  bool
	prepareCalled bool
}

func (s *w1vCandidatePipelineStub) FilterCandidates(ctx context.Context, input gatewaypreauth.CandidateFilterInput) (gatewaypreauth.CandidateFilterResult, error) {
	s.filterCalled = true
	return gatewaypreauth.CandidateFilterResult{}, errors.New("w1v: FilterCandidates 不应被本测试触达")
}

func (s *w1vCandidatePipelineStub) PrepareDispatchAccounts(ctx context.Context, input gatewaypreauth.DispatchPreparationInput) (gatewaypreauth.DispatchPreparationResult, error) {
	s.prepareCalled = true
	return gatewaypreauth.DispatchPreparationResult{}, errors.New("w1v: PrepareDispatchAccounts 不应被本测试触达")
}

func (s *w1vCandidatePipelineStub) ResolveNextGroupFallbackCandidate(ctx context.Context, input gatewaypreauth.GroupFallbackCandidateInput) (gatewaypreauth.GroupFallbackCandidate, bool, error) {
	s.resolveCalls++
	copied := input
	s.captured = &copied
	if s.resolveErr != nil {
		return gatewaypreauth.GroupFallbackCandidate{}, false, s.resolveErr
	}
	return gatewaypreauth.GroupFallbackCandidate{}, s.found, nil
}

// w1vLocksStub 桩化 gatewaydispatch.AccountLocks（仅 FindStateAsync 参与断言，
// 其余方法为不可达兜底）。
type w1vLocksStub struct {
	state *gatewaydispatch.AccountLockStateView
	err   error
	calls int
}

func (s *w1vLocksStub) FindStateAsync(ctx context.Context, accountID string) (*gatewaydispatch.AccountLockStateView, error) {
	s.calls++
	return s.state, s.err
}

func (s *w1vLocksStub) AcquireRetryLeaseAsync(ctx context.Context, accountID string, configuredDelayMs int64) (gatewaydispatch.LockLeaseAcquire, error) {
	return gatewaydispatch.LockLeaseAcquire{}, errors.New("w1v: AcquireRetryLeaseAsync 不应被本测试触达")
}

func (s *w1vLocksStub) ConsumeRetryLeaseAsync(ctx context.Context, accountID, leaseID string) (bool, error) {
	return false, errors.New("w1v: ConsumeRetryLeaseAsync 不应被本测试触达")
}

func (s *w1vLocksStub) ReleaseRetryLeaseAsync(ctx context.Context, input gatewaydispatch.ReleaseRetryLeaseInput) (bool, error) {
	return false, errors.New("w1v: ReleaseRetryLeaseAsync 不应被本测试触达")
}

func (s *w1vLocksStub) AbandonRetryReservationAsync(ctx context.Context, lease gatewaydispatch.AccountLockRetryLease) error {
	return errors.New("w1v: AbandonRetryReservationAsync 不应被本测试触达")
}

func (s *w1vLocksStub) RecordFailureAsync(ctx context.Context, accountID, reason string, observation *gatewaydispatch.AccountLockObservation) error {
	return errors.New("w1v: RecordFailureAsync 不应被本测试触达")
}

func (s *w1vLocksStub) SettleDeadlineAsync(ctx context.Context, accountID string, nowMs int64, observation *gatewaydispatch.AccountLockObservation) error {
	return errors.New("w1v: SettleDeadlineAsync 不应被本测试触达")
}

func (s *w1vLocksStub) CompleteSuccessAsync(ctx context.Context, accountID, leaseID string, observation *gatewaydispatch.AccountLockObservation) error {
	return errors.New("w1v: CompleteSuccessAsync 不应被本测试触达")
}

func (s *w1vLocksStub) ListStatesAsync(ctx context.Context, accountIDs []string) (map[string]gatewaydispatch.AccountLockStateView, error) {
	return nil, errors.New("w1v: ListStatesAsync 不应被本测试触达")
}

// w1vConcurrencyStub 桩化 gatewaydispatch.AccountConcurrencyStore。
type w1vConcurrencyStub struct {
	acquireErr    error
	acquired      bool
	lastAccountID string
	lastOptions   *gatewaydispatch.AccountConcurrencyAcquireOptions
	releaseCount  int
}

func (s *w1vConcurrencyStub) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *w1vConcurrencyStub) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *w1vConcurrencyStub) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options gatewaydispatch.AccountConcurrencyAcquireOptions) (gatewaydispatch.ConcurrencySlot, error) {
	copied := options
	s.lastAccountID = accountID
	s.lastOptions = &copied
	if s.acquireErr != nil {
		return gatewaydispatch.ConcurrencySlot{}, s.acquireErr
	}
	if !s.acquired {
		return gatewaydispatch.ConcurrencySlot{Acquired: false}, nil
	}
	return gatewaydispatch.ConcurrencySlot{Acquired: true, Release: func() { s.releaseCount++ }}, nil
}

// w1vDecisionsStub 实现 LatencyDegradationPort + chainSpeedFirstDecisions：
// 决策闭包测试的可控决策面。degradedByAccount 缺省时回落 degraded。
type w1vDecisionsStub struct {
	degraded          bool
	degradedByAccount map[string]bool
	degradedErr       error
	slowResult        *gatewayproxyhealth.LatencySlowResult
	slowErr           error
	successResult     *gatewayproxyhealth.LatencySuccessResult
	successErr        error
	degradedCalls     int
	slowCalls         int
	successCalls      int
}

func (s *w1vDecisionsStub) degradedFor(account gatewaydispatch.AccountCandidate) bool {
	if value, ok := s.degradedByAccount[account.ID]; ok {
		return value
	}
	return s.degraded
}

func (s *w1vDecisionsStub) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, modelPriority *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	return gatewaydispatch.LatencyDegradationOrder{Accounts: accounts}, nil
}

func (s *w1vDecisionsStub) IsAccountLatencyDegradedAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput) (bool, error) {
	s.degradedCalls++
	if s.degradedErr != nil {
		return false, s.degradedErr
	}
	return s.degradedFor(account), nil
}

func (s *w1vDecisionsStub) RecordFirstByteSlowAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, reason string) (*gatewayproxyhealth.LatencySlowResult, error) {
	s.slowCalls++
	if s.slowErr != nil {
		return nil, s.slowErr
	}
	return s.slowResult, nil
}

func (s *w1vDecisionsStub) RecordFirstByteSuccessAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, firstByteMs int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
	s.successCalls++
	if s.successErr != nil {
		return nil, s.successErr
	}
	return s.successResult, nil
}

// w1vLatencyPortOnly 只实现 LatencyDegradationPort（不实现决策面），
// 用于 speedFirstDecisionsOf 的类型断言失败臂。
type w1vLatencyPortOnly struct{}

func (w1vLatencyPortOnly) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, modelPriority *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	return gatewaydispatch.LatencyDegradationOrder{Accounts: accounts}, nil
}

// w1vPlainResponseWriter 是非 TrackingWriter 的 GatewayResponseWriter 实现，
// 用于 writableEndedOf 的类型断言失败臂。
type w1vPlainResponseWriter struct {
	header http.Header
	status int
}

func (w *w1vPlainResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *w1vPlainResponseWriter) Write(body []byte) (int, error) { return len(body), nil }
func (w *w1vPlainResponseWriter) WriteHeader(status int)         { w.status = status }
func (w *w1vPlainResponseWriter) HeadersSent() bool              { return w.status != 0 }
func (w *w1vPlainResponseWriter) StatusCode() int                { return w.status }

// ---------------------------------------------------------------------------
// 构造辅助
// ---------------------------------------------------------------------------

// w1vSpeedFirstConfig 构造合法的速度优先运行态配置（deadline 必填，
// maxFirstByteRetriesPerRequest 显式覆写为 3 以区别缺省值 2）。
func w1vSpeedFirstConfig(deadlineMs int64, maxRetries int64) *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig {
	return &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		FirstByteDeadlineMs: &deadlineMs,
		Raw: map[string]any{
			"speedFirstConfig": map[string]any{"maxFirstByteRetriesPerRequest": float64(maxRetries)},
		},
	}
}

// w1vEnableLatencyScope 给 DispatchContext 补齐时延降级 scope 三元组
// （SystemAccountID + APIKeyRecord.RouteStrategyID + GroupID）。
func w1vEnableLatencyScope(loop *v1DispatchLoop) {
	loop.current.UsageContext.SystemAccountID = "sys_w1v"
	loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_w1v"}
}

// w1vLoopWithDecisions 在 newV1TestLoop 基础上装配引擎决策面与 scope。
func w1vLoopWithDecisions(t *testing.T, sink *recordingFailureSink, capture *recordingMetadataCapture, latency gatewaydispatch.LatencyDegradationPort) (*v1DispatchLoop, *w1vDecisionsStub) {
	t.Helper()
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	loop.c.engine = &gatewaydispatch.Engine{Latency: latency}
	w1vEnableLatencyScope(loop)
	stub, _ := latency.(*w1vDecisionsStub)
	return loop, stub
}

// w1vLoopBudgets 注入真实预算组（协调预算 / 墙钟预算 / 尝试追踪器）。
func w1vLoopBudgets(t *testing.T, loop *v1DispatchLoop) requestBudgets {
	t.Helper()
	budgets, err := newRequestBudgets("trace_w1v_budgets", loop.startedAt, gatewaypreauth.SystemClock{})
	if err != nil {
		t.Fatalf("构造请求预算: %v", err)
	}
	loop.budgets = budgets
	return budgets
}

// w1vReserveCutoverReservation 用确定性槽获取器预留一个真实切号预留。
func w1vReserveCutoverReservation(t *testing.T, targetID string) *gatewayhotquality.SpeedFirstCutoverReservation {
	t.Helper()
	reservation, err := gatewayhotquality.ReserveSpeedFirstCutoverTarget(context.Background(),
		gatewayhotquality.SpeedFirstCutoverReservationInput{
			SystemAccountID: "sys_w1v",
			RouteStrategyID: "rs_w1v",
			GroupID:         "group_w1v",
			SlowAccountID:   "acc_slow",
			Targets: []gatewayhotquality.GatewayAccountConcurrencyLimitIdentity{
				{ID: targetID, ConcurrencyLimit: 4},
			},
			Lane: gatewayhotquality.AccountConcurrencyLaneText,
			SlotAcquirer: func(ctx context.Context, accountID string, concurrencyLimit int, request gatewayhotquality.AccountConcurrencyAcquireRequest) (gatewayhotquality.AccountConcurrencySlot, bool, error) {
				return gatewayhotquality.AccountConcurrencySlot{
					Key:     accountID + ":" + request.Lane,
					Lane:    request.Lane,
					Release: func() {},
				}, true, nil
			},
		})
	if err != nil {
		t.Fatalf("预留切号目标: %v", err)
	}
	if reservation == nil {
		t.Fatal("切号预留不能为空")
	}
	t.Cleanup(reservation.Release)
	return reservation
}

// w1vNewObservability 构造丢弃输出的 observability（与既有 v1 测试一致）。
func w1vNewObservability() *slogObservability {
	return newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
}

// ---------------------------------------------------------------------------
// resolveRouteAction / actionFallbackOptions / finalizeRouteAction
// ---------------------------------------------------------------------------

// TestW1VResolveRouteActionPassthroughDispatchContext：初始结果不是路由动作时
// 直接透传 DispatchContext，不触碰任何服务端口。
func TestW1VResolveRouteActionPassthroughDispatchContext(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	dispatchContext := &gatewaypreauth.DispatchContext{UsageContext: loop.current.UsageContext}

	result, err := loop.resolveRouteAction(context.Background(), gatewaypreauth.PreflightResult{DispatchContext: dispatchContext})
	if err != nil {
		t.Fatalf("透传不应报错: %v", err)
	}
	if result != dispatchContext {
		t.Fatalf("透传结果指针不一致: %p != %p", result, dispatchContext)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("透传路径不应渲染响应: %+v", sink.inputs)
	}
}

// TestW1VActionFallbackOptionsCarriesBudgets：路由动作回退选项包携带共享预算
// 与分组运行态字段（routes.ts:449-459），TrafficSource 固定 gateway。
func TestW1VActionFallbackOptionsCarriesBudgets(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	budgets := w1vLoopBudgets(t, loop)
	commitState := &gatewaypreauth.DownstreamCommitState{}
	firstByteConfig := &gatewaypreauth.NormalRouteFirstByteRuntimeConfig{SchedulingPreference: "speed_first"}
	retryAfter := int64(500)
	action := &gatewaypreauth.RouteAction{
		RequestLane:                "image",
		ServerRetryBudget:          gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
		GatewayRequestWallBudget:   budgets.wall,
		RouteCoordinationBudget:    budgets.coordination,
		RequestAttemptTracker:      budgets.tracker,
		DownstreamCommitState:      commitState,
		NormalRouteFirstByteConfig: firstByteConfig,
		Failure:                    &gatewayrouting.GatewayRouteFinalFailure{RetryAfterMs: &retryAfter},
	}

	options := loop.actionFallbackOptions(action)
	if options.TrafficSource != gatewayTrafficSource {
		t.Fatalf("TrafficSource = %q", options.TrafficSource)
	}
	if options.RequestLane != action.RequestLane {
		t.Fatalf("RequestLane = %q", options.RequestLane)
	}
	if options.ServerRetryBudget != action.ServerRetryBudget {
		t.Fatal("ServerRetryBudget 未携带")
	}
	if options.GatewayRequestWallBudget != budgets.wall || options.RouteCoordinationBudget != budgets.coordination || options.RequestAttemptTracker != budgets.tracker {
		t.Fatal("共享预算未逐项携带")
	}
	if options.DownstreamCommitState != commitState {
		t.Fatal("DownstreamCommitState 未携带")
	}
	if options.NormalRouteFirstByteConfig != firstByteConfig {
		t.Fatal("NormalRouteFirstByteConfig 未携带")
	}
}

// TestW1VResolveRouteActionFinalizesFailureAction：候选回退不可用（零
// RoutePlanSnapshot → Attempted=false）时落到 finalizeRouteAction 的
// Failure 分支：Retry-After 头按毫秒向上取整为秒，payload 保留原始文案。
func TestW1VResolveRouteActionFinalizesFailureAction(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
	loop.res = res
	retryAfter := int64(1500) // → 2 秒
	action := &gatewaypreauth.RouteAction{
		UsageContext: loop.current.UsageContext,
		Failure: &gatewayrouting.GatewayRouteFinalFailure{
			StatusCode:   http.StatusTooManyRequests,
			Message:      "路由配额已用尽",
			ErrorType:    "quota_exceeded",
			ErrorCode:    "quota_exceeded",
			ErrorPhase:   "quota",
			RetryAfterMs: &retryAfter,
		},
	}

	result, err := loop.resolveRouteAction(context.Background(), gatewaypreauth.PreflightResult{RouteAction: action})
	if err != nil {
		t.Fatalf("路由动作收尾不应报错: %v", err)
	}
	if result != nil {
		t.Fatalf("路由动作收尾后应返回 nil: %+v", result)
	}
	if !loop.actionVisitedGroups[action.UsageContext.GroupID] {
		t.Fatalf("动作分组未标记已访问: %v", loop.actionVisitedGroups)
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	exit := sink.inputs[0]
	if exit.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", exit.StatusCode)
	}
	if exit.ResponsePayload.Error.Message != "路由配额已用尽" {
		t.Fatalf("payload = %+v", exit.ResponsePayload)
	}
	if exit.Audit.ErrorCode != "quota_exceeded" || exit.Audit.ErrorPhase != "quota" {
		t.Fatalf("audit = %+v", exit.Audit)
	}
	if got := res.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q want 2", got)
	}
}

// TestW1VResolveRouteActionVisitedGroupSkipsFallback：同分组重复路由动作不再
// 尝试回退，直接按 exhausted 文案收尾（temporarily_blocked 与缺省两条文案）。
func TestW1VResolveRouteActionVisitedGroupSkipsFallback(t *testing.T) {
	cases := []struct {
		name        string
		outcome     string
		wantMessage string
	}{
		{"temporarily_blocked", "temporarily_blocked", "当前路由暂时没有可派发账户，请稍后重试"},
		{"default_exhausted", "hard_exhausted", "当前路由没有可用的上游账户"},
	}
	for _, testCase := range cases {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		loop.actionVisitedGroups[loop.current.UsageContext.GroupID] = true
		action := &gatewaypreauth.RouteAction{
			Coordination: gatewaypreauth.RouteActionCoordination{Outcome: testCase.outcome},
			UsageContext: loop.current.UsageContext,
		}

		result, err := loop.resolveRouteAction(context.Background(), gatewaypreauth.PreflightResult{RouteAction: action})
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if result != nil {
			t.Fatalf("%s: 应返回 nil", testCase.name)
		}
		if len(sink.inputs) != 1 {
			t.Fatalf("%s: inputs=%d", testCase.name, len(sink.inputs))
		}
		if sink.inputs[0].ResponsePayload.Error.Message != testCase.wantMessage {
			t.Fatalf("%s: payload = %q", testCase.name, sink.inputs[0].ResponsePayload.Error.Message)
		}
		if sink.inputs[0].ResponsePayload.Error.Code != "upstream_retryable_error" {
			t.Fatalf("%s: code = %q", testCase.name, sink.inputs[0].ResponsePayload.Error.Code)
		}
	}
}

// TestW1VResolveRouteActionFallbackErrorPropagates：候选分组解析错误向上传播
// （Node routes.ts:518-520：resolveRouteAction 的错误走 express 错误中间件）。
func TestW1VResolveRouteActionFallbackErrorPropagates(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	stub := &w1vCandidatePipelineStub{resolveErr: errors.New("候选分组解析失败")}
	loop.c.preauth.Candidates = stub
	loop.current.RoutePlanSnapshot = gatewayrouting.RoutePlanSnapshot[string]{
		OrderedAllowedTargets: []string{"group_main", "group_fb"},
		Cursor:                0,
	}
	action := &gatewaypreauth.RouteAction{
		UsageContext:      loop.current.UsageContext,
		RoutePlanSnapshot: loop.current.RoutePlanSnapshot,
	}

	result, err := loop.resolveRouteAction(context.Background(), gatewaypreauth.PreflightResult{RouteAction: action})
	if err == nil {
		t.Fatal("候选解析错误必须向上传播")
	}
	if err.Error() != "候选分组解析失败" {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("出错时结果应为 nil: %+v", result)
	}
	if stub.resolveCalls != 1 {
		t.Fatalf("resolveCalls=%d", stub.resolveCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("错误路径不应渲染响应: %+v", sink.inputs)
	}
}

// TestW1VResolveRouteActionClientHandoffSkipsFallback：client handoff 结果不
// 尝试分组回退，直接渲染 handoff 契约。
func TestW1VResolveRouteActionClientHandoffSkipsFallback(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	stub := &w1vCandidatePipelineStub{}
	loop.c.preauth.Candidates = stub
	action := &gatewaypreauth.RouteAction{
		Coordination: gatewaypreauth.RouteActionCoordination{
			Outcome: gatewaypreauth.RouteOutcomeClientHandoff,
			Reason:  "route_coordination_budget_exhausted",
		},
		UsageContext: loop.current.UsageContext,
	}

	result, err := loop.resolveRouteAction(context.Background(), gatewaypreauth.PreflightResult{RouteAction: action})
	if err != nil {
		t.Fatalf("client handoff 收尾不应报错: %v", err)
	}
	if result != nil {
		t.Fatalf("应返回 nil: %+v", result)
	}
	if stub.resolveCalls != 0 {
		t.Fatalf("client handoff 不应触发候选解析: %d", stub.resolveCalls)
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	if sink.inputs[0].Audit.ErrorMessage != "当前路由暂时无法继续派发，请客户端重试并重新选择可用账户" {
		t.Fatalf("audit = %+v", sink.inputs[0].Audit)
	}
}

// ---------------------------------------------------------------------------
// switchToFallbackGroup 的可构造分支
// ---------------------------------------------------------------------------

// TestW1VSwitchToFallbackGroupAffinitySkips：交互资源亲和上下文不尝试分组回退。
func TestW1VSwitchToFallbackGroupAffinitySkips(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	stub := &w1vCandidatePipelineStub{}
	loop.c.preauth.Candidates = stub
	loop.current.InteractionResourceAffinity = &gatewaygemini.AffinityBinding{}

	fallback, err := loop.switchToFallbackGroup(context.Background(), "upstream_accounts_exhausted")
	if err != nil {
		t.Fatalf("affinity 跳过不应报错: %v", err)
	}
	if fallback != v1FallbackNone {
		t.Fatalf("fallback = %q", fallback)
	}
	if stub.resolveCalls != 0 || len(sink.inputs) != 0 {
		t.Fatalf("affinity 路径不应触达候选解析或渲染: calls=%d inputs=%d", stub.resolveCalls, len(sink.inputs))
	}
}

// TestW1VSwitchToFallbackGroupHopLimit：回退跳数达到分组绑定数时跳过并记录
// api_key_group_route_fallback_skipped（fallback_hop_limit）。
func TestW1VSwitchToFallbackGroupHopLimit(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	loop.c.preauth.Candidates = &w1vCandidatePipelineStub{}
	loop.fallbackSwitches = 1
	loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{GroupID: "group_main"}},
	}

	fallback, err := loop.switchToFallbackGroup(context.Background(), "upstream_accounts_exhausted")
	if err != nil {
		t.Fatalf("跳数上限不应报错: %v", err)
	}
	if fallback != v1FallbackNone {
		t.Fatalf("fallback = %q", fallback)
	}
	metadata := capture.byLabel("api_key_group_route_fallback_skipped")
	if metadata == nil {
		t.Fatal("api_key_group_route_fallback_skipped 元数据缺失")
	}
	if metadata["skippedReason"] != "fallback_hop_limit" || metadata["groupBindingCount"] != 1 || metadata["fallbackSwitchCount"] != 1 {
		t.Fatalf("metadata = %+v", metadata)
	}
}

// TestW1VSwitchToFallbackGroupSelectsFallbackRecordAndHandsExhaustedSet：
// agent guidance 原因把回退记录升级为 GroupFallbackAPIKeyRecord；请求级耗尽
// 集原样移交给候选解析入参（routes.ts:576-578/624-625）。
func TestW1VSwitchToFallbackGroupSelectsFallbackRecordAndHandsExhaustedSet(t *testing.T) {
	cases := []struct {
		name           string
		reason         string
		wantMainRecord bool
	}{
		{"agent_guidance_uses_group_fallback_record", "account_scoped_agent_guidance_exhausted", false},
		{"plain_reason_keeps_main_record", "upstream_accounts_exhausted", true},
	}
	for _, testCase := range cases {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		stub := &w1vCandidatePipelineStub{}
		loop.c.preauth.Candidates = stub
		recordMain := &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_main"}
		recordFallback := &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_fb"}
		loop.current.APIKeyRecord = recordMain
		loop.current.GroupFallbackAPIKeyRecord = recordFallback
		loop.current.RoutePlanSnapshot = gatewayrouting.RoutePlanSnapshot[string]{
			OrderedAllowedTargets: []string{"group_main", "group_fb"},
			Cursor:                0,
		}
		loop.exhaustedAccounts = map[string]struct{}{"acc_sentinel": {}}

		fallback, err := loop.switchToFallbackGroup(context.Background(), testCase.reason)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if fallback != v1FallbackNone {
			t.Fatalf("%s: fallback = %q", testCase.name, fallback)
		}
		if stub.captured == nil {
			t.Fatalf("%s: 候选解析未被调用", testCase.name)
		}
		wantRecord := recordFallback
		if testCase.wantMainRecord {
			wantRecord = recordMain
		}
		if stub.captured.APIKeyRecord != wantRecord {
			t.Fatalf("%s: APIKeyRecord 选择错误", testCase.name)
		}
		if _, ok := stub.captured.ExcludedAccountIDs["acc_sentinel"]; !ok {
			t.Fatalf("%s: 请求级耗尽集未移交: %v", testCase.name, stub.captured.ExcludedAccountIDs)
		}
		if stub.captured.Reason != testCase.reason {
			t.Fatalf("%s: reason = %q", testCase.name, stub.captured.Reason)
		}
	}
}

// TestW1VSwitchToFallbackGroupCandidateError：候选解析错误原样上抛
// （调用方渲染 unexpected failure；见 settleSpeedFirstCutoverError 测试）。
func TestW1VSwitchToFallbackGroupCandidateError(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.c.preauth.Candidates = &w1vCandidatePipelineStub{resolveErr: errors.New("候选存储不可用")}
	loop.current.RoutePlanSnapshot = gatewayrouting.RoutePlanSnapshot[string]{
		OrderedAllowedTargets: []string{"group_main", "group_fb"},
	}

	fallback, err := loop.switchToFallbackGroup(context.Background(), "upstream_accounts_exhausted")
	if err == nil {
		t.Fatal("候选解析错误必须上抛")
	}
	if err.Error() != "候选存储不可用" {
		t.Fatalf("err = %v", err)
	}
	if fallback != "" {
		t.Fatalf("出错时 fallback = %q", fallback)
	}
}

// ---------------------------------------------------------------------------
// 耗尽集与耗尽渲染
// ---------------------------------------------------------------------------

// TestW1VExhaustDispatchFailedAccountID：空 id 不入集；非空 id 入集并初始化
// 集合（settleSpeedFirstCutoverError 的锁定/无预留臂依赖）。
func TestW1VExhaustDispatchFailedAccountID(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	loop.exhaustDispatchFailedAccountID("")
	if loop.exhaustedAccounts != nil {
		t.Fatalf("空 id 不应初始化集合: %v", loop.exhaustedAccounts)
	}
	loop.exhaustDispatchFailedAccountID("acc_7")
	if _, exhausted := loop.exhaustedAccounts["acc_7"]; !exhausted {
		t.Fatalf("acc_7 必须入集: %v", loop.exhaustedAccounts)
	}
	loop.exhaustDispatchFailedAccountID("acc_7")
	if len(loop.exhaustedAccounts) != 1 {
		t.Fatalf("重复写入不应产生重复成员: %v", loop.exhaustedAccounts)
	}
}

// TestW1VRenderDispatchExhaustedWithMessage：speed-first 耗尽的固定 503 契约
// ——客户端固定文案、审计携带原始消息、recordUsage=false。
func TestW1VRenderDispatchExhaustedWithMessage(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture

	loop.renderDispatchExhaustedWithMessage(context.Background(), "首字截止 800ms 已到：acc_slow", "acc_slow", "慢账户")
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	exit := sink.inputs[0]
	if exit.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", exit.StatusCode)
	}
	if exit.ResponsePayload.Error.Message != "上游暂时不可用，请重试" {
		t.Fatalf("client payload = %q", exit.ResponsePayload.Error.Message)
	}
	if exit.ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatalf("code = %q", exit.ResponsePayload.Error.Code)
	}
	if exit.Audit.ErrorMessage != "首字截止 800ms 已到：acc_slow" {
		t.Fatalf("audit message = %q", exit.Audit.ErrorMessage)
	}
	if exit.Audit.Outcome != gatewaypreauth.AuditOutcomeUpstreamFailed || exit.Audit.ErrorPhase != "dispatch" {
		t.Fatalf("audit shape = %s/%s", exit.Audit.Outcome, exit.Audit.ErrorPhase)
	}
	if exit.RecordUsage == nil || *exit.RecordUsage {
		t.Fatal("speed-first 耗尽必须跳过用量记录")
	}
}

// ---------------------------------------------------------------------------
// 纯投影小函数
// ---------------------------------------------------------------------------

// TestW1VPureProjections：覆盖 chain_v1.go 的零散纯投影函数。
func TestW1VPureProjections(t *testing.T) {
	// chainSpeedFirstMaxRetriesOf：缺配置 = 0；合法配置读取 Raw 覆写值。
	if got := chainSpeedFirstMaxRetriesOf(&gatewaypreauth.DispatchContext{}); got != 0 {
		t.Fatalf("缺配置 maxRetries = %d want 0", got)
	}
	deadline := int64(900)
	withRaw := &gatewaypreauth.DispatchContext{
		NormalRouteSpeedFirstConfig: &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
			FirstByteDeadlineMs: &deadline,
			Raw:                 map[string]any{"speedFirstConfig": map[string]any{"maxFirstByteRetriesPerRequest": float64(3)}},
		},
	}
	if got := chainSpeedFirstMaxRetriesOf(withRaw); got != 3 {
		t.Fatalf("Raw 覆写 maxRetries = %d want 3", got)
	}

	// chainAccountIDsOf：按入参顺序投影 id。
	ids := chainAccountIDsOf([]gatewaydispatch.AccountCandidate{{ID: "acc_b"}, {ID: "acc_a"}})
	if len(ids) != 2 || ids[0] != "acc_b" || ids[1] != "acc_a" {
		t.Fatalf("chainAccountIDsOf = %v", ids)
	}
	if got := chainAccountIDsOf(nil); len(got) != 0 {
		t.Fatalf("chainAccountIDsOf(nil) = %v", got)
	}

	// chainSchedulingPolicyValueOf：nil 解引用为 nil，非 nil 返回同一值。
	policy := gatewayruntimecache.GroupSchedulingPolicy{"imageLaneMaxConcurrency": float64(4)}
	if got := chainSchedulingPolicyValueOf(nil); got != nil {
		t.Fatalf("nil policy = %v", got)
	}
	if got := chainSchedulingPolicyValueOf(&policy); got == nil || got["imageLaneMaxConcurrency"] != float64(4) {
		t.Fatalf("policy 解引用错误: %v", got)
	}

	// chainRuntimeKeyOfCandidate：owner 即 ID；授权账户带绑定上下文；缺指针回落 ID。
	authorized := gatewaydispatch.AccountCandidate{
		ID:                     "acc_auth",
		AccountAccessType:      "account_authorized",
		BindingSystemAccountID: w1vStrPtr("sys_owner"),
		BoundGroupID:           w1vStrPtr("group_bound"),
		AccountAuthorizationID: w1vStrPtr("authz_1"),
	}
	if got := chainRuntimeKeyOfCandidate(authorized); got != "acc_auth:authorized:sys_owner:group_bound:authz_1" {
		t.Fatalf("authorized runtime key = %q", got)
	}
	missingAuthz := authorized
	missingAuthz.AccountAuthorizationID = nil
	if got := chainRuntimeKeyOfCandidate(missingAuthz); got != "acc_auth" {
		t.Fatalf("缺授权 id 的 runtime key = %q", got)
	}
	if got := chainRuntimeKeyOfCandidate(gatewaydispatch.AccountCandidate{ID: "acc_owner"}); got != "acc_owner" {
		t.Fatalf("owner runtime key = %q", got)
	}

	// chainConcurrencyAccountIDOf：凭据源账户优先并规范化去空白。
	credential := " cred_9 "
	withCredential := gatewaydispatch.AccountCandidate{ID: "acc_1", CredentialSourceAccountID: &credential}
	if got := chainConcurrencyAccountIDOf(withCredential); got != "cred_9" {
		t.Fatalf("credential id = %q", got)
	}
	blank := "   "
	blankCredential := gatewaydispatch.AccountCandidate{ID: "acc_1", CredentialSourceAccountID: &blank}
	if got := chainConcurrencyAccountIDOf(blankCredential); got != "acc_1" {
		t.Fatalf("空凭据源应回落 id: %q", got)
	}
	if got := chainConcurrencyAccountIDOf(gatewaydispatch.AccountCandidate{ID: "acc_1"}); got != "acc_1" {
		t.Fatalf("无凭据源应回落 id: %q", got)
	}

	// chainCutoverTargetsOf：投影切号目标三元组。
	targets := chainCutoverTargetsOf([]gatewaydispatch.AccountCandidate{
		{ID: "acc_t", CredentialSourceAccountID: &credential, ConcurrencyLimit: 7},
	})
	if len(targets) != 1 || targets[0].ID != "acc_t" || targets[0].CredentialSourceAccountID != "cred_9" || targets[0].ConcurrencyLimit != 7 {
		t.Fatalf("targets = %+v", targets)
	}

	// chainFirstByteConfigOf：nil 透传；字段逐项投影；deadline 空指针解引用为 0。
	if got := chainFirstByteConfigOf(nil); got != nil {
		t.Fatalf("nil config = %v", got)
	}
	deadlineValue := int64(1200)
	projected := chainFirstByteConfigOf(&gatewaypreauth.NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: "speed_first",
		FirstByteDeadlineMs:  &deadlineValue,
	})
	if projected == nil || projected.SchedulingPreference != "speed_first" || projected.FirstByteDeadlineMs != 1200 {
		t.Fatalf("projected = %+v", projected)
	}
	emptyDeadline := chainFirstByteConfigOf(&gatewaypreauth.NormalRouteFirstByteRuntimeConfig{SchedulingPreference: "quality_first"})
	if emptyDeadline == nil || emptyDeadline.FirstByteDeadlineMs != 0 {
		t.Fatalf("空 deadline 投影 = %+v", emptyDeadline)
	}

	// routeStrategyIDOf：nil 记录返回空串。
	if got := routeStrategyIDOf(nil); got != "" {
		t.Fatalf("nil record strategy = %q", got)
	}
	if got := routeStrategyIDOf(&gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_9"}); got != "rs_9" {
		t.Fatalf("record strategy = %q", got)
	}

	// isEmptyPreflightResult：双空即空。
	if !isEmptyPreflightResult(gatewaypreauth.PreflightResult{}) {
		t.Fatal("零值 PreflightResult 应为空")
	}
	if isEmptyPreflightResult(gatewaypreauth.PreflightResult{DispatchContext: &gatewaypreauth.DispatchContext{}}) {
		t.Fatal("带 DispatchContext 不为空")
	}

	// stringSetKeys：确定性排序输出。
	keys := stringSetKeys(map[string]struct{}{"b": {}, "a": {}, "c": {}})
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Fatalf("stringSetKeys = %v", keys)
	}
	if got := stringSetKeys(nil); len(got) != 0 {
		t.Fatalf("stringSetKeys(nil) = %v", got)
	}
}

func w1vStrPtr(value string) *string { return &value }

// TestW1VStreamServerRetryFallbackReason：三种重试原因的 fallback reason 映射。
func TestW1VStreamServerRetryFallbackReason(t *testing.T) {
	cases := []struct {
		retryReason string
		want        string
	}{
		{gatewayresponse.StreamServerRetryResponseInspection, "response_inspection_server_retry_exhausted"},
		{gatewayresponse.StreamServerRetryUpstreamProtocolFailure, "upstream_protocol_server_retry_exhausted"},
		{"unknown_reason", "stream_server_retry_exhausted"},
		{"", "stream_server_retry_exhausted"},
	}
	for _, testCase := range cases {
		if got := streamServerRetryFallbackReason(testCase.retryReason); got != testCase.want {
			t.Fatalf("%q → %q want %q", testCase.retryReason, got, testCase.want)
		}
	}
}

// TestW1VClassifyGatewayDispatchExhaustion：耗尽分类的五条分支。
func TestW1VClassifyGatewayDispatchExhaustion(t *testing.T) {
	cases := []struct {
		name         string
		lastAttempt  *gatewaydispatch.UpstreamAttempt
		wantReason   string
		wantUpstream any
	}{
		{"no_attempt", nil, "no_available_account", nil},
		{"key_pool", &gatewaydispatch.UpstreamAttempt{UpstreamURL: "account:api_key_pool_unavailable"}, "api_key_pool_unavailable", nil},
		{"suppressed", &gatewaydispatch.UpstreamAttempt{UpstreamURL: "account:locally_suppressed"}, "all_accounts_locally_suppressed", nil},
		{"concurrency", &gatewaydispatch.UpstreamAttempt{UpstreamURL: "concurrency:limit"}, "account_concurrency_exhausted", nil},
		{"http_error", &gatewaydispatch.UpstreamAttempt{UpstreamURL: "https://upstream", HasStatus: true, Status: 502}, "upstream_http_error", 502},
		{"transport", &gatewaydispatch.UpstreamAttempt{UpstreamURL: "https://upstream"}, "upstream_transport_error", nil},
		{"status_zero", &gatewaydispatch.UpstreamAttempt{UpstreamURL: "https://upstream", HasStatus: true}, "upstream_transport_error", nil},
	}
	for _, testCase := range cases {
		reason, upstream := classifyGatewayDispatchExhaustion(testCase.lastAttempt)
		if reason != testCase.wantReason {
			t.Fatalf("%s: reason = %q want %q", testCase.name, reason, testCase.wantReason)
		}
		if upstream != testCase.wantUpstream {
			t.Fatalf("%s: upstream = %v want %v", testCase.name, upstream, testCase.wantUpstream)
		}
	}
}

// TestW1VClientStrategyPreCommitFailureSignal：nil 策略、非 codex Opaque、
// codex Opaque 三种取值路径。
func TestW1VClientStrategyPreCommitFailureSignal(t *testing.T) {
	if got := clientStrategyPreCommitFailureSignal(nil); got != "" {
		t.Fatalf("nil strategy = %q", got)
	}
	nonCodex := &gatewaypreauth.ClientStrategyContext{Opaque: "not-a-strategy"}
	if got := clientStrategyPreCommitFailureSignal(nonCodex); got != "" {
		t.Fatalf("non-codex opaque = %q", got)
	}
	codex := &gatewaypreauth.ClientStrategyContext{
		Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{
			RetryCoordination: gatewaycodex.GatewayClientRetryCoordination{
				PreCommitFailureSignal: gatewaycodex.FailureSignalProtocolErrorEvent,
			},
		},
	}
	if got := clientStrategyPreCommitFailureSignal(codex); got != gatewaycodex.FailureSignalProtocolErrorEvent {
		t.Fatalf("codex signal = %q", got)
	}
}

// TestW1VClientStrategyViewOf：最终化视图投影（G18 冻结子集）。
func TestW1VClientStrategyViewOf(t *testing.T) {
	context := &gatewaypreauth.DispatchContext{
		ClientStrategy: gatewaypreauth.ClientStrategyContext{
			ClientProfile:      "codex_cli",
			DownstreamProtocol: "responses_sse",
		},
	}
	view := clientStrategyViewOf(context)
	if view == nil || view.ClientProfile != "codex_cli" || view.DownstreamProtocol != "responses_sse" || !view.InterpretSemantics {
		t.Fatalf("view = %+v", view)
	}
	emptyProfile := clientStrategyViewOf(&gatewaypreauth.DispatchContext{})
	if emptyProfile == nil || emptyProfile.ClientProfile != "" || !emptyProfile.InterpretSemantics {
		t.Fatalf("empty view = %+v", emptyProfile)
	}
}

// TestW1VDbServiceUnavailableMessage：四个前缀命中与其余不命中。
func TestW1VDbServiceUnavailableMessage(t *testing.T) {
	positive := []string{
		"本地数据库服务暂时不可用：连接失败",
		"本地数据库服务未就绪",
		"本地数据库服务请求超时（3000ms）",
		"本地数据库服务已退出",
	}
	for _, message := range positive {
		if !dbServiceUnavailableMessage(message) {
			t.Fatalf("%q 应命中 db 不可用前缀", message)
		}
	}
	negative := []string{"", "数据库连接失败", "本地数据库服务", "网关请求体读取失败"}
	for _, message := range negative {
		if dbServiceUnavailableMessage(message) {
			t.Fatalf("%q 不应命中 db 不可用前缀", message)
		}
	}
}

// ---------------------------------------------------------------------------
// speed-first 决策面：decisions / scope / 候选窗口 / 槽获取器 / 预留视图
// ---------------------------------------------------------------------------

// TestW1VSpeedFirstDecisionsOfAndLatencyScope：决策面的接口断言与 scope 三元组投影。
func TestW1VSpeedFirstDecisionsOfAndLatencyScope(t *testing.T) {
	sink := &recordingFailureSink{}
	// 决策面缺席：只实现排序端口的 Latency 不会通过断言。
	loopPortOnly := newV1TestLoop(t, sink)
	loopPortOnly.c.engine = &gatewaydispatch.Engine{Latency: w1vLatencyPortOnly{}}
	if decisions := loopPortOnly.speedFirstDecisionsOf(); decisions != nil {
		t.Fatalf("只实现排序端口时 decisions = %v", decisions)
	}
	// 决策面在位：chainLatencyDegradationPort 实现完整接口。
	loopWithPort := newV1TestLoop(t, sink)
	loopWithPort.c.engine = &gatewaydispatch.Engine{Latency: chainLatencyDegradationPort{}}
	if decisions := loopWithPort.speedFirstDecisionsOf(); decisions == nil {
		t.Fatal("chainLatencyDegradationPort 必须满足 chainSpeedFirstDecisions")
	}

	// scope：三元组齐全才有 scope；缺任一为 nil。
	loop := newV1TestLoop(t, sink)
	if scope := loop.speedFirstLatencyScopeOf(loop.current); scope != nil {
		t.Fatalf("缺三元组时 scope = %+v", scope)
	}
	w1vEnableLatencyScope(loop)
	scope := loop.speedFirstLatencyScopeOf(loop.current)
	if scope == nil || scope.SystemAccountID != "sys_w1v" || scope.RouteStrategyID != "rs_w1v" || scope.GroupID != "group_main" {
		t.Fatalf("scope = %+v", scope)
	}
}

// TestW1VSpeedFirstRouteEligibleDispatchAccounts：候选窗口三重过滤——排除集、
// 尝试预算、已降级账户。
func TestW1VSpeedFirstRouteEligibleDispatchAccounts(t *testing.T) {
	// 排除集过滤；tracker 与 scope 缺席时到此为止。
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	stub := &w1vDecisionsStub{}
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	remaining, err := loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), loop.current,
		map[string]struct{}{"acc_2": {}}, nil, stub)
	if err != nil {
		t.Fatalf("排除集过滤不应报错: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "acc_1" {
		t.Fatalf("remaining = %v", chainAccountIDsOf(remaining))
	}

	// tracker 在位：尝试预算把空 runtime key 的候选挡下。
	loop = newV1TestLoop(t, sink)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_good"}, {ID: ""}}
	w1vLoopBudgets(t, loop)
	remaining, err = loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), loop.current, nil, nil, stub)
	if err != nil {
		t.Fatalf("尝试预算过滤不应报错: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "acc_good" {
		t.Fatalf("tracker 过滤后 remaining = %v", chainAccountIDsOf(remaining))
	}

	// scope + 决策面：已降级账户被剔除。
	loop = newV1TestLoop(t, sink)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	stub = &w1vDecisionsStub{degradedByAccount: map[string]bool{"acc_2": true}}
	w1vEnableLatencyScope(loop)
	scope := loop.speedFirstLatencyScopeOf(loop.current)
	remaining, err = loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), loop.current, nil, scope, stub)
	if err != nil {
		t.Fatalf("降级过滤不应报错: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "acc_1" {
		t.Fatalf("降级过滤后 remaining = %v", chainAccountIDsOf(remaining))
	}

	// 决策面报错：向上传播。
	loop = newV1TestLoop(t, sink)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}}
	stub = &w1vDecisionsStub{degradedErr: errors.New("降级查询失败")}
	remaining, err = loop.speedFirstRouteEligibleDispatchAccounts(context.Background(), loop.current, nil, scope, stub)
	if err == nil || err.Error() != "降级查询失败" {
		t.Fatalf("err = %v", err)
	}
	if remaining != nil {
		t.Fatalf("出错时 remaining 应为 nil: %v", remaining)
	}
}

// TestW1VSpeedFirstSlotAcquirer：并发槽获取器的桥接语义。
func TestW1VSpeedFirstSlotAcquirer(t *testing.T) {
	// 引擎并发存储缺席：不获取也不报错。
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.c.engine = &gatewaydispatch.Engine{}
	acquirer := loop.speedFirstSlotAcquirer(loop.current)
	slot, acquired, err := acquirer(context.Background(), "acc_1", 5, gatewayhotquality.AccountConcurrencyAcquireRequest{Lane: gatewayhotquality.AccountConcurrencyLaneText})
	if err != nil || acquired {
		t.Fatalf("无并发存储: acquired=%v err=%v", acquired, err)
	}

	// 获取成功：槽键为 accountID:lane，释放闭包透传。
	store := &w1vConcurrencyStub{acquired: true}
	loop.c.engine = &gatewaydispatch.Engine{Concurrency: store}
	slot, acquired, err = acquirer(context.Background(), "acc_1", 5, gatewayhotquality.AccountConcurrencyAcquireRequest{Lane: gatewayhotquality.AccountConcurrencyLaneText})
	if err != nil || !acquired {
		t.Fatalf("获取失败: acquired=%v err=%v", acquired, err)
	}
	if slot.Key != "acc_1:text" || slot.Lane != gatewayhotquality.AccountConcurrencyLaneText || slot.Release == nil {
		t.Fatalf("slot = %+v", slot)
	}
	if store.lastAccountID != "acc_1" || store.lastOptions == nil || store.lastOptions.Lane != "text" || store.lastOptions.LaneLimit != nil {
		t.Fatalf("store 侧入参 = id:%q options:%+v", store.lastAccountID, store.lastOptions)
	}
	slot.Release()
	if store.releaseCount != 1 {
		t.Fatalf("释放闭包未透传: %d", store.releaseCount)
	}

	// image lane：注入 image lane 上限。
	_, acquired, err = acquirer(context.Background(), "acc_1", 5, gatewayhotquality.AccountConcurrencyAcquireRequest{Lane: gatewayhotquality.AccountConcurrencyLaneImage})
	if err != nil || !acquired {
		t.Fatalf("image lane 获取失败: %v %v", acquired, err)
	}
	if store.lastOptions.LaneLimit == nil || *store.lastOptions.LaneLimit <= 0 {
		t.Fatalf("image lane 上限未注入: %+v", store.lastOptions)
	}

	// 槽满与错误路径。
	store = &w1vConcurrencyStub{acquired: false}
	loop.c.engine = &gatewaydispatch.Engine{Concurrency: store}
	_, acquired, err = acquirer(context.Background(), "acc_1", 5, gatewayhotquality.AccountConcurrencyAcquireRequest{Lane: "text"})
	if err != nil || acquired {
		t.Fatalf("槽满应返回未获取: acquired=%v err=%v", acquired, err)
	}
	store = &w1vConcurrencyStub{acquireErr: errors.New("并发存储故障")}
	loop.c.engine = &gatewaydispatch.Engine{Concurrency: store}
	if _, _, err = acquirer(context.Background(), "acc_1", 5, gatewayhotquality.AccountConcurrencyAcquireRequest{Lane: "text"}); err == nil {
		t.Fatal("并发存储错误必须上抛")
	}
}

// TestW1VSpeedFirstReservationViewAndHandle：预留视图与预占句柄投影。
func TestW1VSpeedFirstReservationViewAndHandle(t *testing.T) {
	if view := speedFirstReservationViewOf(nil); view != nil {
		t.Fatalf("nil 预留视图 = %v", view)
	}
	if handle := speedFirstReservationHandleOf(nil); handle != nil {
		t.Fatalf("nil 预留句柄 = %v", handle)
	}

	reservation := w1vReserveCutoverReservation(t, "acc_target")
	view := speedFirstReservationViewOf(reservation)
	if view == nil || view.TargetAccountIDValue != "acc_target" || view.ReleaseFunc == nil {
		t.Fatalf("view = %+v", view)
	}

	handle := speedFirstReservationHandleOf(reservation)
	slot, ok := handle.TakeForAccount(gatewaydispatch.AccountCandidate{ID: "acc_target", ConcurrencyLimit: 4})
	if !ok || !slot.Acquired || slot.Release == nil {
		t.Fatalf("目标账户取槽失败: ok=%v slot=%+v", ok, slot)
	}
	// 预留一次性：消费后对任何账户（含目标）都不再可用。
	if _, ok = handle.TakeForAccount(gatewaydispatch.AccountCandidate{ID: "acc_target"}); ok {
		t.Fatal("消费后的预留不应再次取槽")
	}
	if _, ok = handle.TakeForAccount(gatewaydispatch.AccountCandidate{ID: "acc_other"}); ok {
		t.Fatal("非目标账户不应取槽")
	}
	slot.Release()
	reservation.Release()
}

// TestW1VAttachedSpeedFirstReservationRoundTrip：视图 → 具体预留映射的记录、
// 取回与未知视图的确定性释放。
func TestW1VAttachedSpeedFirstReservationRoundTrip(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	// nil 入参不建立映射。
	loop.recordAttachedSpeedFirstReservation(nil, nil)
	if view := loop.attachedConcreteReservationOf(nil); view != nil {
		t.Fatalf("nil 视图应返回 nil: %v", view)
	}

	reservation := w1vReserveCutoverReservation(t, "acc_target")
	view := speedFirstReservationViewOf(reservation)
	loop.recordAttachedSpeedFirstReservation(view, reservation)
	loop.recordAttachedSpeedFirstReservation(nil, reservation)
	loop.recordAttachedSpeedFirstReservation(view, nil)

	concrete := loop.attachedConcreteReservationOf(view)
	if concrete != reservation {
		t.Fatalf("取回的预留不一致: %v", concrete)
	}
	// 取回即删除：同一视图第二次取回走未知视图臂，触发一次确定性释放。
	released := false
	unknown := &gatewaydispatch.SpeedFirstCutoverReservationView{ReleaseFunc: func() { released = true }}
	if got := loop.attachedConcreteReservationOf(unknown); got != nil {
		t.Fatalf("未知视图应返回 nil: %v", got)
	}
	if !released {
		t.Fatal("未知视图的释放闭包未被调用")
	}
	reservation.Release()
}
