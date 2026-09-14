package main

// w1: v1DispatchLoop 难测区直攻 —— speed-first cutover 错误结算、耗尽渲染、
// 首字截止决策门、orchestrator 错误出口。复用 recovery 测试的 stub 服务与
// 失败 sink 模式（newV1TestLoop / recordingFailureSink / chainStubCapture）。

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// w1FakeLocks 是 gatewaydispatch.AccountLocks 的可编程 stub。
type w1FakeLocks struct {
	view *gatewaydispatch.AccountLockStateView
	err  error
}

func (f *w1FakeLocks) FindStateAsync(context.Context, string) (*gatewaydispatch.AccountLockStateView, error) {
	return f.view, f.err
}
func (f *w1FakeLocks) AcquireRetryLeaseAsync(context.Context, string, int64) (gatewaydispatch.LockLeaseAcquire, error) {
	return gatewaydispatch.LockLeaseAcquire{}, nil
}
func (f *w1FakeLocks) ConsumeRetryLeaseAsync(context.Context, string, string) (bool, error) {
	return false, nil
}
func (f *w1FakeLocks) ReleaseRetryLeaseAsync(context.Context, gatewaydispatch.ReleaseRetryLeaseInput) (bool, error) {
	return false, nil
}
func (f *w1FakeLocks) AbandonRetryReservationAsync(context.Context, gatewaydispatch.AccountLockRetryLease) error {
	return nil
}
func (f *w1FakeLocks) RecordFailureAsync(context.Context, string, string, *gatewaydispatch.AccountLockObservation) error {
	return nil
}
func (f *w1FakeLocks) SettleDeadlineAsync(context.Context, string, int64, *gatewaydispatch.AccountLockObservation) error {
	return nil
}
func (f *w1FakeLocks) ListStatesAsync(context.Context, []string) (map[string]gatewaydispatch.AccountLockStateView, error) {
	return nil, nil
}
func (f *w1FakeLocks) CompleteSuccessAsync(context.Context, string, string, *gatewaydispatch.AccountLockObservation) error {
	return nil
}

func w1LoopWithEngine(t *testing.T, locks gatewaydispatch.AccountLocks) (*v1DispatchLoop, *recordingFailureSink) {
	t.Helper()
	loop := newV1TestLoop(t, &recordingFailureSink{})
	loop.c.engine = &gatewaydispatch.Engine{Locks: locks}
	return loop, loop.c.preauth.Responses.(*recordingFailureSink)
}

func TestW1RenderDispatchExhaustedWithMessage(t *testing.T) {
	loop, sink := w1LoopWithEngine(t, &w1FakeLocks{})
	loop.current.UsageContext.Endpoint = "/v1/chat/completions"
	loop.renderDispatchExhaustedWithMessage(context.Background(), "上游超时", "acc_slow", "慢账户")
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs = %d", len(sink.inputs))
	}
	input := sink.inputs[0]
	if input.StatusCode != 503 {
		t.Fatalf("status = %d", input.StatusCode)
	}
	if input.ResponsePayload.Error.Message != "上游暂时不可用，请重试" {
		t.Fatalf("message = %q", input.ResponsePayload.Error.Message)
	}
	if input.ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatalf("code = %q", input.ResponsePayload.Error.Code)
	}
	if input.Audit.ErrorPhase != "dispatch" || input.FailureScope != "upstream" {
		t.Fatalf("audit = %+v", input.Audit)
	}
	if input.RecordUsage == nil || *input.RecordUsage {
		t.Fatal("recordUsage 必须 false")
	}
}

func TestW1SettleSpeedFirstCutoverLockDenied(t *testing.T) {
	locks := &w1FakeLocks{view: &gatewaydispatch.AccountLockStateView{BlocksCrossAccount: true, Generation: 2}}
	loop, sink := w1LoopWithEngine(t, locks)
	released := false
	view := &gatewaydispatch.SpeedFirstCutoverReservationView{
		TargetAccountIDValue: "acc_t",
		ReleaseFunc:          func() { released = true },
	}
	loop.streamRetryExcludedAccounts = map[string]struct{}{"acc_slow": {}}
	cutover := &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "acc_slow", AccountName: "慢账户",
		Message: "首字超时", CutoverReservation: view,
		Deadline: gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured},
	}
	// 跨账户锁阻断：释放预留、解除排除、按耗尽结算、返回 true。
	if settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover); !settled {
		t.Fatal("锁定臂必须结算")
	}
	if !released {
		t.Fatal("预留必须被释放")
	}
	if _, blocked := loop.streamRetryExcludedAccounts["acc_slow"]; blocked {
		t.Fatal("慢账户必须解除排除")
	}
	if _, exhausted := loop.exhaustedAccounts["acc_slow"]; !exhausted {
		t.Fatal("慢账户必须入耗尽集")
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != 503 {
		t.Fatalf("sink = %+v", sink.inputs)
	}
	if loop.speedFirstRetryCandidateAccountIds != nil {
		t.Fatal("候选收窄必须清空")
	}
}

func TestW1SettleSpeedFirstCutoverReservationCarry(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	reservation := w1Reservation(t)
	view := speedFirstReservationViewOf(reservation)
	loop.recordAttachedSpeedFirstReservation(view, reservation)
	cutover := &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "acc_slow", Message: "首字超时",
		CutoverReservation: view,
		Deadline:           gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured},
	}
	// 有确认目标：预留接回循环携带状态，返回 false（继续派发）。
	if settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover); settled {
		t.Fatal("预留携带臂不得结算")
	}
	if loop.speedFirstByteRetryCount != 1 {
		t.Fatalf("retry count = %d", loop.speedFirstByteRetryCount)
	}
	if loop.speedFirstCutoverReservation != reservation {
		t.Fatal("预留必须接回为具体预留")
	}
	if _, narrowed := loop.speedFirstRetryCandidateAccountIds["acc_target"]; !narrowed {
		t.Fatalf("候选窗口 = %v", loop.speedFirstRetryCandidateAccountIds)
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_slow"]; !excluded {
		t.Fatal("慢账户必须被排除")
	}
}

func TestW1SettleSpeedFirstCutoverNoReservation(t *testing.T) {
	loop, sink := w1LoopWithEngine(t, &w1FakeLocks{})
	// 亲和绑定存在 → switchToFallbackGroup 直接 None → 走耗尽渲染。
	loop.current.InteractionResourceAffinity = &gatewaygemini.AffinityBinding{}
	cutover := &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "acc_slow", Message: "首字超时",
		Deadline: gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured},
	}
	if settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover); !settled {
		t.Fatal("无预留臂必须结算")
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != 503 {
		t.Fatalf("sink = %+v", sink.inputs)
	}
	// 无预留但带未知视图：确定性释放。
	fired := false
	orphan := &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "acc_slow", Message: "超时",
		CutoverReservation: &gatewaydispatch.SpeedFirstCutoverReservationView{ReleaseFunc: func() { fired = true }},
		Deadline:           gatewayrouting.NormalRouteAttemptFirstByteDeadline{},
	}
	loop.settleSpeedFirstCutoverError(context.Background(), orphan)
	if !fired {
		t.Fatal("孤儿视图必须确定性释放")
	}
}

func TestW1OnNormalRouteFirstByteDeadlineGates(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	closure := loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
	account := gatewaydispatch.AccountCandidate{ID: "acc_1"}
	// LaneTimeout / UncommittedAttempt → Continue。
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account,
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorLaneTimeout}, nil); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("lane timeout = %v", got)
	}
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account,
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorUncommittedAttempt}, nil); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("uncommitted = %v", got)
	}
	// WallPrecommit → Abort。
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account,
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorWallPrecommit}, nil); got != gatewaydispatch.FirstByteDeadlineActionAbort {
		t.Fatalf("precommit = %v", got)
	}
	// 无 speed-first 配置 → Continue。
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account,
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{}, nil); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("无配置 = %v", got)
	}
	// 有配置但决策面/范围缺失 → Continue。
	loop.current.NormalRouteSpeedFirstConfig = w4bSpeedFirstConfig(8_000, nil)
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account,
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured}, nil); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("无决策面 = %v", got)
	}
}

func TestW1SpeedFirstDeadlineDecisionLockAndError(t *testing.T) {
	// 锁阻断：Continue + 审计元数据。
	locks := &w1FakeLocks{view: &gatewaydispatch.AccountLockStateView{BlocksCrossAccount: true}}
	loop, _ := w1LoopWithEngine(t, locks)
	coordinator := &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}
	action, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current,
		gatewaydispatch.AccountCandidate{ID: "acc_1"},
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{EffectiveDeadlineMs: 900},
		coordinator, &w1LatencyFake{}, &gatewaydispatch.LatencyScopeInput{GroupID: "grp_1"},
		w4bSpeedFirstConfig(8_000, nil))
	if err != nil || action != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("锁阻断 = %v, %v", action, err)
	}
	// 决策错误（IsAccountLatencyDegradedAsync 报错）：错误透传，由闭包兜底。
	loop2, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop2.current.NormalRouteSpeedFirstConfig = w4bSpeedFirstConfig(8_000, nil)
	loop2.c.engine.Latency = &w1LatencyFake{err: errors.New("决策失败")}
	if _, err := loop2.speedFirstDeadlineDecision(context.Background(), loop2.current,
		gatewaydispatch.AccountCandidate{ID: "acc_1"},
		gatewayrouting.NormalRouteAttemptFirstByteDeadline{EffectiveDeadlineMs: 900},
		coordinator, loop2.speedFirstDecisionsOf(), &gatewaydispatch.LatencyScopeInput{GroupID: "grp_1"},
		w4bSpeedFirstConfig(8_000, nil)); err == nil {
		t.Fatal("决策错误必须透传")
	}
}

func TestW1LoopStateResetsAndBudgets(t *testing.T) {
	loop := &v1DispatchLoop{}
	// resetSpeedFirstState：全部清零并释放预留。
	loop.speedFirstCutoverReservation = w1Reservation(t)
	loop.speedFirstByteRetryCount = 3
	loop.speedFirstRetryCandidateAccountIds = map[string]struct{}{"a": {}}
	loop.speedFirstSlowObservedForAttempt = &gatewayproxyhealth.LatencySlowResult{}
	loop.resetSpeedFirstState()
	if loop.speedFirstByteRetryCount != 0 || loop.speedFirstRetryCandidateAccountIds != nil || loop.speedFirstSlowObservedForAttempt != nil {
		t.Fatalf("reset 后 = %+v", loop)
	}
	if loop.speedFirstCutoverReservation != nil {
		t.Fatal("reset 必须释放预留")
	}
	// releasePending：nil 安全 + 释放清空。
	loop.releasePendingSpeedFirstReservation()
	loop.speedFirstCutoverReservation = w1Reservation(t)
	loop.releasePendingSpeedFirstReservation()
	if loop.speedFirstCutoverReservation != nil {
		t.Fatal("pending 必须释放")
	}
	// actionFallbackOptions：字段投影。
	action := &gatewaypreauth.RouteAction{RequestLane: "text"}
	options := loop.actionFallbackOptions(action)
	if options.TrafficSource != "gateway" || options.RequestLane != "text" {
		t.Fatalf("options = %+v", options)
	}
	// speedFirstSlotAcquirer：Concurrency 缺席 → 未获取。
	loop2, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	acquirer := loop2.speedFirstSlotAcquirer(loop2.current)
	if _, ok, err := acquirer(context.Background(), "acc_1", 4, gatewayhotquality.AccountConcurrencyAcquireRequest{}); err != nil || ok {
		t.Fatalf("nil concurrency = %v, %v", ok, err)
	}
}

func TestW1HandleOrchestratorErrorContracts(t *testing.T) {
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	// db-service 不可用：503 + 原文透传。
	recorder503 := httptest.NewRecorder()
	loop.res = gatewaypreauth.NewTrackingWriter(recorder503)
	loop.c.handleOrchestratorError(errors.New("本地数据库服务暂时不可用：健康检查超时"),
		loop.req, loop.res, loop.startedAt, "/v1/chat/completions")
	if recorder503.Code != 503 {
		t.Fatalf("503 契约 = %d body=%s", recorder503.Code, recorder503.Body.String())
	}
	if !bytes.Contains(recorder503.Body.Bytes(), []byte("本地数据库服务暂时不可用")) {
		t.Fatalf("body = %s", recorder503.Body.String())
	}
	// 普通错误：Express 兜底 500 固定文案（无网关错误包络）。
	recorder500 := httptest.NewRecorder()
	loop.res = gatewaypreauth.NewTrackingWriter(recorder500)
	loop.c.handleOrchestratorError(errors.New("上游炸了"), loop.req, loop.res, loop.startedAt, "/v1/chat/completions")
	if recorder500.Code != 500 || recorder500.Body.String() != `{"message":"服务器内部错误"}` {
		t.Fatalf("500 契约 = %d %s", recorder500.Code, recorder500.Body.String())
	}
	// 头已发出：不再渲染。
	recorderSent := httptest.NewRecorder()
	writer := gatewaypreauth.NewTrackingWriter(recorderSent)
	_, _ = writer.Write([]byte("partial"))
	loop.res = writer
	loop.c.handleOrchestratorError(errors.New("普通错误"), loop.req, loop.res, loop.startedAt, "/v1/chat/completions")
	if recorderSent.Code == 500 && recorderSent.Body.String() == `{"message":"服务器内部错误"}` {
		t.Fatal("头已发出不得重复渲染")
	}
}
