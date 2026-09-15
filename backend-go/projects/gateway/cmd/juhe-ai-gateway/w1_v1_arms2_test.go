package main

// chain_v1.go 第二部分：speed-first 响应观测与决策闭包、首字节截止时间、
// 切换结算与 settleDispatchError、finalizeRouteAction、请求快照与审计
// 捕获适配器。从 w1_v1_arms_test.go 按主题拆出
//（单文件不超 2000~3000 行法则）。

import (
	"context"
	"errors"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// speed-first 响应观测与决策闭包
// ---------------------------------------------------------------------------

// TestW1VObserveSpeedFirstResponseOutcome：响应完成后的慢采样/恢复采样观测。
func TestW1VObserveSpeedFirstResponseOutcome(t *testing.T) {
	newCase := func(t *testing.T) (*v1DispatchLoop, *recordingMetadataCapture, *w1vDecisionsStub) {
		sink := &recordingFailureSink{}
		capture := &recordingMetadataCapture{}
		stub := &w1vDecisionsStub{}
		loop, _ := w1vLoopWithDecisions(t, sink, capture, stub)
		return loop, capture, stub
	}

	// 配置缺席或首字耗时缺席：直接返回。
	loop, capture, stub := newCase(t)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{}, gatewayresponse.UpstreamResponseHandlingResult{})
	if capture.byLabel("normal_route_speed_first_slow_observed") != nil || stub.slowCalls+stub.successCalls != 0 {
		t.Fatal("无配置时不应观测")
	}
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	firstToken := int64(1500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{}, gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: nil})
	if stub.slowCalls+stub.successCalls != 0 {
		t.Fatal("无首字耗时不应观测")
	}

	// 决策面缺席（scope 在位）：不观测。
	loop, capture, _ = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	loop.c.engine = &gatewaydispatch.Engine{Latency: w1vLatencyPortOnly{}}
	firstToken = int64(1500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if capture.byLabel("normal_route_speed_first_slow_observed") != nil {
		t.Fatal("决策面缺席时不应观测")
	}

	// scope 缺席：不观测。
	loop, _, stub = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	loop.current.APIKeyRecord = nil
	loop.current.UsageContext.SystemAccountID = ""
	firstToken = int64(1500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if stub.slowCalls+stub.successCalls != 0 {
		t.Fatal("scope 缺席时不应观测")
	}

	// 首字超阈值：补记慢采样并写审计。
	loop, capture, stub = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 2, Degraded: true}
	firstToken = int64(1500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if stub.slowCalls != 1 {
		t.Fatalf("slowCalls=%d", stub.slowCalls)
	}
	metadata := capture.byLabel("normal_route_speed_first_slow_observed")
	if metadata == nil || metadata["observedAt"] != "response_completed" || metadata["firstTokenMs"] != int64(1500) || metadata["thresholdMs"] != int64(1000) {
		t.Fatalf("慢采样元数据 = %+v", metadata)
	}

	// 同尝试已观察过去重：不重复记录。
	loop, _, stub = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	loop.speedFirstSlowObservedForAttempt = &gatewayproxyhealth.LatencySlowResult{}
	firstToken = int64(1500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if stub.slowCalls != 0 {
		t.Fatalf("去重失败: slowCalls=%d", stub.slowCalls)
	}

	// 慢采样失败：warn + 审计，不改写响应。
	loop, capture, stub = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	stub.slowErr = errors.New("慢采样写入失败")
	firstToken = int64(1500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if capture.byLabel("normal_route_speed_first_local_decision_failed") == nil {
		t.Fatal("慢采样失败缺审计元数据")
	}

	// 达标：恢复采样 + 审计；结果为 nil 时不写。
	loop, capture, stub = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	stub.successResult = &gatewayproxyhealth.LatencySuccessResult{Cleared: true, RecoverySuccessCount: 2, RequiredRecoverySuccessCount: 3}
	firstToken = int64(400)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if stub.successCalls != 1 {
		t.Fatalf("successCalls=%d", stub.successCalls)
	}
	recovery := capture.byLabel("normal_route_speed_first_recovery_observed")
	if recovery == nil || recovery["cleared"] != true || recovery["recoverySuccessCount"] != int64(2) {
		t.Fatalf("恢复元数据 = %+v", recovery)
	}

	// 达标但恢复结果为空：不写审计。
	loop, capture, stub = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	firstToken = int64(400)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if capture.byLabel("normal_route_speed_first_recovery_observed") != nil {
		t.Fatal("空恢复结果不应写审计")
	}

	// 恢复采样失败：warn 路径。
	loop, capture, stub = newCase(t)
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(1000, 3)
	stub.successErr = errors.New("恢复采样写入失败")
	firstToken = int64(400)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if capture.byLabel("normal_route_speed_first_local_decision_failed") == nil {
		t.Fatal("恢复采样失败缺审计元数据")
	}
}

// TestW1VWarnSpeedFirstDecisionFailure：决策失败告警面写日志与审计元数据。
func TestW1VWarnSpeedFirstDecisionFailure(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop, _ := w1vLoopWithDecisions(t, sink, capture, &w1vDecisionsStub{})

	loop.warnSpeedFirstDecisionFailure(gatewaydispatch.AccountCandidate{ID: "acc_2"},
		loop.speedFirstLatencyScopeOf(loop.current), "response_observation", errors.New("决策端口故障"))
	metadata := capture.byLabel("normal_route_speed_first_local_decision_failed")
	if metadata == nil || metadata["stage"] != "response_observation" || metadata["accountId"] != "acc_2" {
		t.Fatalf("metadata = %+v", metadata)
	}
}

// TestW1VOnNormalRouteFirstByteDeadlineGates：决策闭包的前置门（limiting
// factor 门、配置门、决策面门）与错误兜底。
func TestW1VOnNormalRouteFirstByteDeadlineGates(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{ID: "acc_1"}
	deadline := gatewayrouting.NormalRouteAttemptFirstByteDeadline{
		EffectiveDeadlineMs: 800,
		LimitingFactor:      gatewayrouting.FirstByteLimitingFactorConfigured,
	}

	// limiting factor 门：lane 超时 / 未提交尝试 → 继续；墙钟预留 → 中止。
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop, _ := w1vLoopWithDecisions(t, sink, capture, &w1vDecisionsStub{})
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
	closure := loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
	gated := deadline
	gated.LimitingFactor = gatewayrouting.FirstByteLimitingFactorLaneTimeout
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, gated, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("lane 超时门 = %q", got)
	}
	gated.LimitingFactor = gatewayrouting.FirstByteLimitingFactorUncommittedAttempt
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, gated, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("未提交尝试门 = %q", got)
	}
	gated.LimitingFactor = gatewayrouting.FirstByteLimitingFactorWallPrecommit
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, gated, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionAbort {
		t.Fatalf("墙钟预留门 = %q", got)
	}

	// 配置缺席：继续。
	loop, _ = w1vLoopWithDecisions(t, sink, capture, &w1vDecisionsStub{})
	closure = loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, deadline, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("配置缺席 = %q", got)
	}

	// 决策面缺席：继续。
	loop, _ = w1vLoopWithDecisions(t, sink, capture, w1vLatencyPortOnly{})
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
	closure = loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, deadline, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("决策面缺席 = %q", got)
	}

	// scope 缺席（无 APIKeyRecord）：继续。
	loop, _ = w1vLoopWithDecisions(t, sink, capture, &w1vDecisionsStub{})
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
	loop.current.APIKeyRecord = nil
	closure = loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, deadline, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("scope 缺席 = %q", got)
	}

	// 决策错误：释放协调器预留、写审计、继续当前上游。
	loop, _ = w1vLoopWithDecisions(t, sink, capture, &w1vDecisionsStub{degradedErr: errors.New("降级查询失败")})
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
	closure = loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
	if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, deadline, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("决策错误兜底 = %q", got)
	}
	if capture.byLabel("normal_route_speed_first_local_decision_failed") == nil {
		t.Fatal("决策错误缺审计元数据")
	}
}

// TestW1VSpeedFirstDeadlineDecisionArms：决策主体（routes.ts:1119-1240）的
// 锁检查、降级门、重试上限、预留获取与切换中止。
func TestW1VSpeedFirstDeadlineDecisionArms(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"}
	deadline := gatewayrouting.NormalRouteAttemptFirstByteDeadline{EffectiveDeadlineMs: 800, LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured}

	newCase := func(t *testing.T) (*v1DispatchLoop, *recordingMetadataCapture, *w1vDecisionsStub) {
		sink := &recordingFailureSink{}
		capture := &recordingMetadataCapture{}
		loop, stub := w1vLoopWithDecisions(t, sink, capture, &w1vDecisionsStub{})
		loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
		loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
		scope := loop.speedFirstLatencyScopeOf(loop.current)
		_ = scope
		return loop, capture, stub
	}
	scopeOf := func(loop *v1DispatchLoop) *gatewaydispatch.LatencyScopeInput {
		return loop.speedFirstLatencyScopeOf(loop.current)
	}

	// 1. 账户锁查询失败：错误上抛。
	loop, capture, stub := newCase(t)
	loop.current.UsageContext.TrafficSource = gatewayTrafficSource
	loop.c.engine.Locks = &w1vLocksStub{err: errors.New("锁查询失败")}
	if _, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig); err == nil {
		t.Fatal("锁查询失败必须上抛")
	}

	// 2. 跨账户锁拒绝切换：审计 + 继续当前账户。
	loop, capture, stub = newCase(t)
	loop.current.UsageContext.TrafficSource = gatewayTrafficSource
	loop.c.engine.Locks = &w1vLocksStub{state: &gatewaydispatch.AccountLockStateView{BlocksCrossAccount: true}}
	action, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig)
	if err != nil {
		t.Fatalf("锁拒绝分支不应报错: %v", err)
	}
	if action != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("锁拒绝 = %q", action)
	}
	if capture.byLabel("account_lock_speed_first_cutover_denied") == nil {
		t.Fatal("锁拒绝缺审计元数据")
	}

	// 3. 降级查询失败：错误上抛（非 gateway 流量跳过锁检查）。
	loop, _, stub = newCase(t)
	stub.degradedErr = errors.New("降级查询失败")
	if _, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig); err == nil {
		t.Fatal("降级查询失败必须上抛")
	}

	// 4. 慢采样失败：错误上抛。
	loop, _, stub = newCase(t)
	stub.slowErr = errors.New("慢采样失败")
	if _, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig); err == nil {
		t.Fatal("慢采样失败必须上抛")
	}

	// 5. 慢采样未降级：继续当前账户，阻塞原因 slow_observation_not_degraded。
	loop, capture, stub = newCase(t)
	stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 1}
	action, err = loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig)
	if err != nil || action != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("未降级: action=%q err=%v", action, err)
	}
	if metadata := capture.byLabel("normal_route_speed_first_slow_observed"); metadata == nil || metadata["retryBlockedReason"] != "slow_observation_not_degraded" {
		t.Fatalf("未降级元数据 = %+v", metadata)
	}

	// 6. 剩余候选为空：阻塞原因 no_remaining_candidate。
	loop, capture, stub = newCase(t)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}}
	stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 1, Degraded: true}
	action, err = loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig)
	if err != nil || action != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("无剩余候选: action=%q err=%v", action, err)
	}
	if metadata := capture.byLabel("normal_route_speed_first_slow_observed"); metadata == nil || metadata["retryBlockedReason"] != "no_remaining_candidate" {
		t.Fatalf("无候选元数据 = %+v", metadata)
	}

	// 7. 重试次数达到上限：阻塞原因 max_retry_exceeded。
	loop, capture, stub = newCase(t)
	stub.degradedByAccount = map[string]bool{"acc_1": true}
	stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 2, Degraded: true}
	loop.speedFirstByteRetryCount = 3
	action, err = loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig)
	if err != nil || action != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("重试上限: action=%q err=%v", action, err)
	}
	if metadata := capture.byLabel("normal_route_speed_first_slow_observed"); metadata == nil || metadata["retryBlockedReason"] != "max_retry_exceeded" {
		t.Fatalf("重试上限元数据 = %+v", metadata)
	}

	// 8. 预留槽不可用：阻塞原因 target_slot_or_cutover_budget_unavailable。
	loop, capture, stub = newCase(t)
	loop.current.UsageContext.TrafficSource = gatewayTrafficSource
	stub.degradedByAccount = map[string]bool{"acc_1": true}
	stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 2, Degraded: true}
	action, err = loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig)
	if err != nil || action != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("槽不可用: action=%q err=%v", action, err)
	}
	if metadata := capture.byLabel("normal_route_speed_first_slow_observed"); metadata == nil || metadata["retryBlockedReason"] != "target_slot_or_cutover_budget_unavailable" {
		t.Fatalf("槽不可用元数据 = %+v", metadata)
	}

	// 9. 切换预留：预留经循环的槽获取器获取目标并发槽；attach 失败（零值
	// 协调器不可进入 active 态，AttachReservation 对外包不可满足）时确定性
	// 释放预留并继续当前上游。
	loop, capture, stub = newCase(t)
	loop.current.UsageContext.TrafficSource = gatewayTrafficSource
	store := &w1vConcurrencyStub{acquired: true}
	loop.c.engine.Concurrency = store
	stub.degradedByAccount = map[string]bool{"acc_1": true}
	stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 2, Degraded: true}
	action, err = loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
		&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, scopeOf(loop), loop.current.NormalRouteSpeedFirstConfig)
	if err != nil || action != gatewaydispatch.FirstByteDeadlineActionContinue {
		t.Fatalf("attach 失败: action=%q err=%v", action, err)
	}
	if store.lastAccountID != "acc_2" || store.lastOptions == nil {
		t.Fatalf("循环槽获取器未被使用: id=%q", store.lastAccountID)
	}
	// attach 失败臂释放预留：底层槽的释放闭包被确定性触发一次。
	if store.releaseCount != 1 {
		t.Fatalf("预留释放次数=%d want 1", store.releaseCount)
	}
	if metadata := capture.byLabel("normal_route_speed_first_slow_observed"); metadata == nil || metadata["thresholdMs"] != int64(800) {
		t.Fatalf("观测元数据 = %+v", metadata)
	}
}

// ---------------------------------------------------------------------------
// settleSpeedFirstCutoverError / settleDispatchError
// ---------------------------------------------------------------------------

// TestW1VSettleSpeedFirstCutoverErrorLockBlocked：跨账户锁拒绝切换 → 释放预留、
// 解除慢账户排除、请求级耗尽并按固定文案收尾。
func TestW1VSettleSpeedFirstCutoverErrorLockBlocked(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	loop.current.UsageContext.TrafficSource = gatewayTrafficSource
	loop.c.engine = &gatewaydispatch.Engine{Locks: &w1vLocksStub{state: &gatewaydispatch.AccountLockStateView{BlocksCrossAccount: true}}}
	released := false
	view := &gatewaydispatch.SpeedFirstCutoverReservationView{ReleaseFunc: func() { released = true }}
	loop.speedFirstRetryCandidateAccountIds = map[string]struct{}{"acc_target": {}}
	loop.streamRetryExcludedAccounts = map[string]struct{}{"acc_slow": {}}
	cutover := &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID:          "acc_slow",
		AccountName:        "慢账户",
		Message:            "首字截止已到且锁拒绝切换",
		CutoverReservation: view,
	}

	settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover)
	if !settled {
		t.Fatal("锁拒绝必须结算请求")
	}
	if !released {
		t.Fatal("预留视图未被释放")
	}
	if loop.speedFirstRetryCandidateAccountIds != nil {
		t.Fatalf("保留窗口未清空: %v", loop.speedFirstRetryCandidateAccountIds)
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_slow"]; excluded {
		t.Fatalf("慢账户未解除排除: %v", loop.streamRetryExcludedAccounts)
	}
	if _, exhausted := loop.exhaustedAccounts["acc_slow"]; !exhausted {
		t.Fatalf("慢账户未入请求级耗尽集: %v", loop.exhaustedAccounts)
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("inputs=%+v", sink.inputs)
	}
}

// TestW1VSettleSpeedFirstCutoverErrorNonGatewaySkipsLock：非 gateway 流量不做
// 锁检查（诊断/手动测试流量不受账户锁切换限制）。
func TestW1VSettleSpeedFirstCutoverErrorNonGatewaySkipsLock(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	locks := &w1vLocksStub{}
	loop.current.UsageContext.TrafficSource = "manual_account_test"
	loop.c.engine = &gatewaydispatch.Engine{Locks: locks}

	settled := loop.settleSpeedFirstCutoverError(context.Background(), &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "acc_slow",
		Message:   "首字截止已到",
	})
	if !settled {
		t.Fatal("无预留且无 fallback 时应按耗尽收尾")
	}
	if locks.calls != 0 {
		t.Fatalf("非 gateway 流量不应查询锁: %d", locks.calls)
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
}

// TestW1VSettleSpeedFirstCutoverErrorCarriesReservation：确认切换 → 慢账户进入
// 排除集、携带预留、窗口收窄到目标并继续循环。
func TestW1VSettleSpeedFirstCutoverErrorCarriesReservation(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	loop.current.UsageContext.TrafficSource = gatewayTrafficSource
	loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
	loop.c.engine = &gatewaydispatch.Engine{Locks: &w1vLocksStub{}}
	reservation := w1vReserveCutoverReservation(t, "acc_target")
	view := speedFirstReservationViewOf(reservation)
	loop.recordAttachedSpeedFirstReservation(view, reservation)
	cutover := &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID:          "acc_slow",
		Deadline:           gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured, EffectiveDeadlineMs: 800},
		Message:            "首字截止 800ms 已到",
		CutoverReservation: view,
	}

	settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover)
	if settled {
		t.Fatal("确认切换应继续循环")
	}
	if loop.speedFirstByteRetryCount != 1 {
		t.Fatalf("retryCount=%d", loop.speedFirstByteRetryCount)
	}
	if loop.speedFirstCutoverReservation != reservation {
		t.Fatalf("预留未携带: %v", loop.speedFirstCutoverReservation)
	}
	targets := loop.speedFirstRetryCandidateAccountIds
	if len(targets) != 1 {
		t.Fatalf("收窄窗口 = %v", targets)
	}
	if _, reserved := targets["acc_target"]; !reserved {
		t.Fatalf("窗口应收窄到目标: %v", targets)
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_slow"]; !excluded {
		t.Fatalf("慢账户未排除: %v", loop.streamRetryExcludedAccounts)
	}
	metadata := capture.byLabel("normal_route_speed_first_retry_dispatch")
	if metadata == nil || metadata["retryAllowed"] != true || metadata["targetAccountId"] != "acc_target" || metadata["maxRetries"] != int64(3) {
		t.Fatalf("切换元数据 = %+v", metadata)
	}
	loop.releasePendingSpeedFirstReservation()
}

// TestW1VSettleSpeedFirstCutoverErrorWithoutReservationTarget：预留无目标或
// 缺预留 → 慢账户耗尽、无 fallback 时按固定 503 收尾。
func TestW1VSettleSpeedFirstCutoverErrorWithoutReservationTarget(t *testing.T) {
	cases := []struct {
		name        string
		reservation *gatewaydispatch.SpeedFirstCutoverReservationView
	}{
		{"empty_target_reservation", &gatewaydispatch.SpeedFirstCutoverReservationView{ReleaseFunc: func() {}}},
		{"no_reservation", nil},
	}
	for _, testCase := range cases {
		sink := &recordingFailureSink{}
		capture := &recordingMetadataCapture{}
		loop := newV1TestLoop(t, sink)
		loop.auditCapture = capture
		loop.current.UsageContext.TrafficSource = gatewayTrafficSource
		loop.c.engine = &gatewaydispatch.Engine{Locks: &w1vLocksStub{}}
		cutover := &gatewaydispatch.NormalRouteFirstByteCutoverError{
			AccountID:          "acc_slow",
			AccountName:        "慢账户",
			Message:            "首字截止已到",
			CutoverReservation: testCase.reservation,
		}

		settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover)
		if !settled {
			t.Fatalf("%s: 应按耗尽收尾", testCase.name)
		}
		if _, exhausted := loop.exhaustedAccounts["acc_slow"]; !exhausted {
			t.Fatalf("%s: 慢账户未耗尽: %v", testCase.name, loop.exhaustedAccounts)
		}
		if len(sink.inputs) != 1 || sink.inputs[0].ResponsePayload.Error.Message != "上游暂时不可用，请重试" {
			t.Fatalf("%s: inputs=%+v", testCase.name, sink.inputs)
		}
	}
}

// TestW1VSettleDispatchErrorCutoverPassthrough：cutover 错误经 settleDispatchError
// 委托到 speed-first 收尾。
func TestW1VSettleDispatchErrorCutoverPassthrough(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.current.UsageContext.TrafficSource = gatewayTrafficSource
	loop.c.engine = &gatewaydispatch.Engine{}

	settled := loop.settleDispatchError(context.Background(), &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "acc_slow",
		Message:   "首字截止已到",
	})
	if !settled {
		t.Fatal("cutover 耗尽必须结算请求")
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
}

// TestW1VSettleDispatchErrorAbortedViaKnownError：preauth 已知错误面不识别
// 引擎的 UpstreamRequestAbortedError（其检测面是 UpstreamAbortedError 接口），
// settleDispatchError 的 errors.As 分支按「下游已关闭，无响应契约」直接结算。
func TestW1VSettleDispatchErrorAbortedViaKnownError(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	recorder := httptest.NewRecorder()
	res := gatewaypreauth.NewTrackingWriter(recorder)
	loop.res = res

	settled := loop.settleDispatchError(context.Background(), &gatewaydispatch.UpstreamRequestAbortedError{Message: "下游连接已关闭"})
	if !settled {
		t.Fatal("中止错误必须结算请求")
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("中止路径不应渲染失败响应: %+v", sink.inputs)
	}
	if res.HeadersSent() {
		t.Fatalf("中止路径不应写出响应: status=%d body=%q", res.StatusCode(), recorder.Body.String())
	}
}

// TestW1VSettleDispatchErrorTerminalAttempt：终端上游失败直接渲染耗尽出口，
// 不入耗尽集、不尝试 fallback。
func TestW1VSettleDispatchErrorTerminalAttempt(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	settled := loop.settleDispatchError(context.Background(), &gatewaydispatch.UpstreamAttemptError{
		Message:                 "所有上游账户均失败",
		TerminalUpstreamFailure: true,
		FailedAccountIDs:        []string{"acc_1"},
		LastAttempt:             &gatewaydispatch.UpstreamAttempt{AccountID: "acc_1"},
	})
	if !settled {
		t.Fatal("终端失败必须结算请求")
	}
	if len(loop.exhaustedAccounts) != 0 {
		t.Fatalf("终端失败不入耗尽集: %v", loop.exhaustedAccounts)
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("inputs=%+v", sink.inputs)
	}
}

// TestW1VSettleDispatchErrorSpeedFirstWindowFallback：收窄窗口的目标失败 →
// 目标进排除集、窗口重置、剩余候选继续循环。
func TestW1VSettleDispatchErrorSpeedFirstWindowFallback(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	loop.speedFirstRetryCandidateAccountIds = map[string]struct{}{"acc_1": {}}

	settled := loop.settleDispatchError(context.Background(), &gatewaydispatch.UpstreamAttemptError{
		Message:          "目标账户失败",
		FailedAccountIDs: []string{"acc_1"},
	})
	if settled {
		t.Fatal("剩余候选存在时应继续循环")
	}
	if loop.speedFirstRetryCandidateAccountIds != nil {
		t.Fatalf("收窄窗口未重置: %v", loop.speedFirstRetryCandidateAccountIds)
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_1"]; !excluded {
		t.Fatalf("失败目标未排除: %v", loop.streamRetryExcludedAccounts)
	}
	if capture.byLabel("normal_route_speed_first_reserved_target_exhausted") == nil {
		t.Fatal("缺 reserved_target_exhausted 元数据")
	}
}

// TestW1VSettleDispatchErrorUnknownError：未知调度错误保持 503 上游契约。
func TestW1VSettleDispatchErrorUnknownError(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	if settled := loop.settleDispatchError(context.Background(), errors.New("意外崩溃")); !settled {
		t.Fatal("未知错误必须结算请求")
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("inputs=%+v", sink.inputs)
	}
}

// ---------------------------------------------------------------------------
// finalizeRouteAction / handleOrchestratorError / newRequestBudgets
// ---------------------------------------------------------------------------

// TestW1VFinalizeRouteActionGuards：可写流已结束直接返回；Retry-After 头仅在
// 响应头未发送时设置。
func TestW1VFinalizeRouteActionGuards(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	retryAfter := int64(500)
	action := &gatewaypreauth.RouteAction{
		UsageContext: loop.current.UsageContext,
		Failure:      &gatewayrouting.GatewayRouteFinalFailure{StatusCode: 429, Message: "限流", ErrorType: "rate_limited", ErrorCode: "rate_limited", RetryAfterMs: &retryAfter},
	}

	// 已结束：不渲染。
	loop.res.End()
	loop.finalizeRouteAction(action)
	if len(sink.inputs) != 0 {
		t.Fatalf("已结束的流不应渲染: %+v", sink.inputs)
	}

	// 头已发送（未结束）：不设置 Retry-After，但失败响应照常进 sink。
	recorder := httptest.NewRecorder()
	loop.res = gatewaypreauth.NewTrackingWriter(recorder)
	loop.res.WriteHeader(http.StatusOK)
	loop.finalizeRouteAction(action)
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	if got := loop.res.Header().Get("Retry-After"); got != "" {
		t.Fatalf("头已发送时不应设置 Retry-After: %q", got)
	}

	// 头未发送：设置向上取整的 Retry-After。
	recorder = httptest.NewRecorder()
	loop.res = gatewaypreauth.NewTrackingWriter(recorder)
	loop.finalizeRouteAction(action)
	if got := loop.res.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q want 1", got)
	}
}

// TestW1VHandleOrchestratorError：db 不可用 503 原文、其余 500 固定文案、
// 头已发送时保持沉默。
func TestW1VHandleOrchestratorError(t *testing.T) {
	observability := w1vNewObservability()
	service := &gatewaypreauth.Service{Responses: &recordingFailureSink{}, Observability: observability, Clock: gatewaypreauth.SystemClock{}}
	chain := &gatewayChain{preauth: service, observability: observability}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req := gatewaypreauth.NewGatewayRequest(request)

	// db 不可用：503 + 原文消息。
	recorder := httptest.NewRecorder()
	res := gatewaypreauth.NewTrackingWriter(recorder)
	chain.handleOrchestratorError(errors.New("本地数据库服务暂时不可用：连接超时"), req, res, 1728000000000, "chat_completions")
	if res.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", res.StatusCode())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "本地数据库服务暂时不可用：连接超时") || !strings.Contains(body, "service_unavailable") {
		t.Fatalf("body = %q", body)
	}

	// 其他错误：500 固定文案。
	recorder = httptest.NewRecorder()
	res = gatewaypreauth.NewTrackingWriter(recorder)
	chain.handleOrchestratorError(errors.New("空指针解引用"), req, res, 1728000000000, "chat_completions")
	if res.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", res.StatusCode())
	}
	if body = recorder.Body.String(); !strings.Contains(body, "服务器内部错误") {
		t.Fatalf("body = %q", body)
	}

	// 头已发送：不再写响应。
	recorder = httptest.NewRecorder()
	res = gatewaypreauth.NewTrackingWriter(recorder)
	res.WriteHeader(http.StatusOK)
	chain.handleOrchestratorError(errors.New("任意错误"), req, res, 1728000000000, "chat_completions")
	if body = recorder.Body.String(); body != "" {
		t.Fatalf("头已发送时应沉默: %q", body)
	}
}

// TestW1VNewRequestBudgets：合法 trace 产出三个预算；空 trace 报协调预算错误。
func TestW1VNewRequestBudgets(t *testing.T) {
	budgets, err := newRequestBudgets("trace_w1v_new", 1728000000000, gatewaypreauth.SystemClock{})
	if err != nil {
		t.Fatalf("构造预算: %v", err)
	}
	if budgets.wall == nil || budgets.coordination == nil || budgets.tracker == nil {
		t.Fatalf("budgets = %+v", budgets)
	}
	if _, err = newRequestBudgets("", 1728000000000, gatewaypreauth.SystemClock{}); err == nil || !strings.Contains(err.Error(), "create route coordination budget") {
		t.Fatalf("空 trace err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 请求级快照与协议辅助
// ---------------------------------------------------------------------------

// TestW1VRequestSnapshots：usageRequestSnapshotOf / rawBodySnapshotOf /
// requestClientIP / writableEndedOf / clientErrorProtocol。
func TestW1VRequestSnapshots(t *testing.T) {
	// 快照：方法/路径/原始 URL/client IP/trace，以及 body state 的 service tier
	// 与 reasoning effort。
	reasoningEffort := "high"
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?beta=x", strings.NewReader(`{}`))
	request.RemoteAddr = "10.1.2.3:8080"
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{
		RawBody: []byte(`{}`),
		State:   &gatewaybody.BodyState{ServiceTier: "priority", ReasoningEffort: &reasoningEffort},
	}
	snapshot := usageRequestSnapshotOf(req, "trace_w1v_snap")
	if snapshot.Method != "POST" || snapshot.Path != "/v1/chat/completions" || snapshot.OriginalURL != "/v1/chat/completions?beta=x" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.ClientIP != "10.1.2.3" || snapshot.TraceID != "trace_w1v_snap" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.RequestedServiceTier != "priority" || snapshot.RequestedReasoningEffort != "high" {
		t.Fatalf("body state 未投影: %+v", snapshot)
	}

	// 无 body state：字段留空。
	plain := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	plainSnapshot := usageRequestSnapshotOf(plain, "trace_w1v_plain")
	if plainSnapshot.RequestedServiceTier != "" || plainSnapshot.RequestedReasoningEffort != "" {
		t.Fatalf("plain snapshot = %+v", plainSnapshot)
	}

	// rawBodySnapshotOf：nil 请求 / nil body / 携带 raw body。
	if got := rawBodySnapshotOf(nil); got != nil {
		t.Fatalf("nil req = %v", got)
	}
	if got := rawBodySnapshotOf(plain); got != nil {
		t.Fatalf("nil body = %v", got)
	}
	if got := rawBodySnapshotOf(req); string(got) != "{}" {
		t.Fatalf("raw body = %q", got)
	}

	// requestClientIP：显式 IP 优先，其次 RemoteAddr 解析，失败返回空。
	withIP := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	withIP.ClientIP = "203.0.113.7"
	if got := requestClientIP(withIP); got != "203.0.113.7" {
		t.Fatalf("explicit ip = %q", got)
	}
	if got := requestClientIP(req); got != "10.1.2.3" {
		t.Fatalf("remote addr ip = %q", got)
	}
	invalid := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	invalid.ClientIP = "not-an-ip"
	invalid.RemoteAddr = ""
	if got := requestClientIP(invalid); got != "" {
		t.Fatalf("invalid ip = %q", got)
	}

	// writableEndedOf：TrackingWriter 透传，其余实现返回 false。
	tracking := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
	if writableEndedOf(tracking) {
		t.Fatal("未结束的 TrackingWriter 不应视为已结束")
	}
	tracking.End()
	if !writableEndedOf(tracking) {
		t.Fatal("End 后应视为已结束")
	}
	if writableEndedOf(&w1vPlainResponseWriter{}) {
		t.Fatal("非 TrackingWriter 应返回 false")
	}

	// clientErrorProtocol：openai 路径 → openai；anthropic 原生请求 → anthropic；
	// 未知请求回落 openai。
	openaiReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if got := clientErrorProtocol(openaiReq); got != gatewaypreauth.GatewayErrorProtocolOpenAI {
		t.Fatalf("openai protocol = %q", got)
	}
	anthropicReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/messages", nil))
	if got := clientErrorProtocol(anthropicReq); got != gatewaypreauth.GatewayErrorProtocolAnthropic {
		t.Fatalf("anthropic protocol = %q", got)
	}
	unknownReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodGet, "/no/such/protocol", nil))
	if got := clientErrorProtocol(unknownReq); got != gatewaypreauth.GatewayErrorProtocolOpenAI {
		t.Fatalf("unknown fallback = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 审计捕获适配器
// ---------------------------------------------------------------------------

// TestW1VAuditCaptureAdapters：newAuditCapture / engineAuditCapture /
// auditCaptureConcrete / responseAuditCaptureOf / chainResponseAuditCapture。
func TestW1VAuditCaptureAdapters(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("User-Agent", "w1v-agent")
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(`{"model":"gpt-test"}`)}

	chain := &gatewayChain{}
	capture := chain.newAuditCapture(req, "trace_w1v_audit", 1728000000000)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(capture) })

	// 具体 G17 capture 在适配器内部构建且默认停用（无 settings 装配）。
	concrete := auditCaptureConcrete(capture)
	if concrete == nil {
		t.Fatal("preauthAuditCapture 应还原具体 capture")
	}
	if concrete.IsEnabled() {
		t.Fatal("未装配 settings 时 capture 应停用")
	}
	if concrete.AuditLogID() == "" {
		t.Fatal("capture 应携带审计 id")
	}
	if got := auditCaptureConcrete(chainStubCapture{}); got != nil {
		t.Fatalf("stub capture = %v", got)
	}

	// engine 适配：具体 capture → 带 sink；否则空 sink。
	engineCapture := chain.engineAuditCapture(capture)
	if engineCapture.Sink == nil {
		t.Fatal("具体 capture 应携带 attempt sink")
	}
	if sink, ok := engineCapture.Sink.(chainAttemptAuditSink); !ok || sink.capture != concrete {
		t.Fatalf("sink = %#v", engineCapture.Sink)
	}
	emptyEngineCapture := chain.engineAuditCapture(chainStubCapture{})
	if emptyEngineCapture.Sink != nil {
		t.Fatalf("stub 的 engine sink = %v", emptyEngineCapture.Sink)
	}

	// 响应层适配与方法面（停用 capture 下全链路无 panic、结构一致）。
	responseCapture := responseAuditCaptureOf(capture)
	if responseCapture == nil {
		t.Fatal("具体 capture 应适配响应层")
	}
	if responseAuditCaptureOf(chainStubCapture{}) != nil {
		t.Fatal("stub 不应适配响应层")
	}
	typed, ok := responseCapture.(chainResponseAuditCapture)
	if !ok || typed.capture != concrete {
		t.Fatalf("response capture = %#v", responseCapture)
	}
	typed.BindContext(gatewaypreauth.AuditGatewayContext{GroupID: "group_w1v", APIKeyID: "key_w1v", TrafficSource: "gateway"})
	typed.AddGatewayMetadata("w1v_adapter_label", map[string]any{"k": "v"})
	attemptID := concrete.StartAttempt(gatewayusage.StartAttemptInput{
		Account:      gatewayusage.UsageModelAccount{ID: "acc_w1v", ProviderCode: "openai"},
		AttemptIndex: 1,
		UpstreamURL:  "https://upstream-w1v",
	})
	typed.CompleteAttempt(attemptID, gatewayresponse.AttemptAuditInput{
		StatusCode:      http.StatusOK,
		ResponseHeaders: map[string]any{"content-type": "application/json"},
		ResponseBody:    []byte("ok"),
		Success:         true,
	})
	typed.FinalizeLazy(func() gatewaypreauth.AuditFinalizeInput {
		return gatewaypreauth.AuditFinalizeInput{
			Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
			StatusCode:   http.StatusServiceUnavailable,
			ResponseBody: "failure body",
			ErrorPhase:   "dispatch",
			ErrorCode:    "upstream_retryable_error",
			ErrorMessage: "上游失败",
		}
	})
	typed.OmitPayloadBodies(gatewayresponse.OmitPayloadBodiesInput{
		Label:                      "w1v_omit",
		PartTypes:                  []string{"request", "upstream_response"},
		AlreadyOmittedPayloadCount: 2,
		AlreadyOmittedBodyBytes:    128,
	})
	if typed.ShouldCaptureSuccessPayloads() {
		t.Fatal("停用 capture 不应捕获成功 payload")
	}
	typed.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
		StatusCode:   http.StatusServiceUnavailable,
		ErrorPhase:   "dispatch",
		ErrorCode:    "upstream_retryable_error",
		ErrorMessage: "上游失败",
	})

	// preauth 适配器的 Finalize 输入联合转换（停用 capture 下仅验证无 panic 与
	// Cancel 幂等）。
	preauth := preauthAuditCapture{inner: concrete}
	preauth.Cancel()
	preauth.Cancel()
}
