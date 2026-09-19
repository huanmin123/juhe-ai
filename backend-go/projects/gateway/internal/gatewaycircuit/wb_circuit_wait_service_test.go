package gatewaycircuit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// manualTimerHub 用可控通道替代真实计时器：每次排程把协调器时钟推进
// 计时延迟，使"计时器触发后轮到等待者"在固定测试时钟下可重放。
type manualTimerHub struct {
	timers []chan struct{}
	clock  int64
}

func (h *manualTimerHub) newTimer(delay time.Duration) (<-chan struct{}, func()) {
	h.clock += delay.Milliseconds()
	done := make(chan struct{})
	h.timers = append(h.timers, done)
	return done, func() {}
}

func (h *manualTimerHub) fireAll() {
	for _, done := range h.timers {
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

func newManualCoordinator(t *testing.T, perScope, global int) (*WaitCoordinator, *manualTimerHub) {
	t.Helper()
	hub := &manualTimerHub{clock: 1_000}
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{
		MaxWaitersPerScope: perScope,
		MaxWaitersGlobal:   global,
		NewTimer:           hub.newTimer,
		Now:                func() int64 { return hub.clock },
	})
	return coordinator, hub
}

// 协调器契约：NotifyOne 唤醒队首等待者；无等待者时返回 false。
func TestWBWaitCoordinatorNotifyOneSettlesHead(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 4, 4)
	if coordinator.NotifyOne("scope", "reason") {
		t.Fatal("无等待者必须返回 false")
	}
	done := make(chan string, 1)
	go func() {
		done <- coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "scope", Reason: "reason", DelayMs: 60_000, DeadlineAtMs: 2_000_000})
	}()
	deadline := time.After(2 * time.Second)
	for {
		if coordinator.Snapshot().WaiterCount == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("等待者未注册")
		default:
		}
	}
	snapshot := coordinator.Snapshot()
	if snapshot.ScopeCount != 1 || snapshot.TimerCount != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !coordinator.NotifyOne("scope", "reason") {
		t.Fatal("NotifyOne 必须命中队首等待者")
	}
	if turn := <-done; turn != TurnReady {
		t.Fatalf("turn = %s", turn)
	}
	hub.fireAll()
}

// NotifyOneForRuntimeKey 契约：只唤醒登记了该运行键的作用域队首。
func TestWBWaitCoordinatorNotifyOneForRuntimeKey(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 4, 4)
	if coordinator.NotifyOneForRuntimeKey("rk") {
		t.Fatal("无等待者必须返回 false")
	}
	done := make(chan string, 1)
	go func() {
		done <- coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "s", Reason: "r", DelayMs: 60_000, DeadlineAtMs: 2_000_000, RuntimeKeys: []string{"rk"}})
	}()
	deadline := time.After(2 * time.Second)
	for coordinator.Snapshot().WaiterCount < 1 {
		select {
		case <-deadline:
			t.Fatal("等待者未注册")
		default:
		}
	}
	if !coordinator.NotifyOneForRuntimeKey("rk") {
		t.Fatal("运行键命中的等待者必须被唤醒")
	}
	if turn := <-done; turn != TurnReady {
		t.Fatalf("turn = %s", turn)
	}
	hub.fireAll()
}

// 容量与截止契约：作用域/全局容量上限立即拒绝；过期截止立即超时；已取消信号立即中止。
func TestWBWaitCoordinatorLimitsDeadlineAbort(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 1, 4)
	// 全局容量上限用独立的单容量协调器验证。
	globalCoordinator, globalHub := newManualCoordinator(t, 4, 1)
	globalOccupant := make(chan string, 1)
	go func() {
		globalOccupant <- globalCoordinator.WaitForTurn(WaitTurnInput{ScopeKey: "g", Reason: "r", DelayMs: 60_000, DeadlineAtMs: 2_000_000})
	}()
	globalDeadline := time.After(2 * time.Second)
	for globalCoordinator.Snapshot().WaiterCount < 1 {
		select {
		case <-globalDeadline:
			t.Fatal("全局占用者未注册")
		default:
		}
	}
	if turn := globalCoordinator.WaitForTurn(WaitTurnInput{ScopeKey: "g2", Reason: "r", DeadlineAtMs: 2_000_000}); turn != TurnGlobalLimit {
		t.Fatalf("全局容量 = %s", turn)
	}
	// 计时器到期只会重新排队（尚未到达 notBefore），必须显式唤醒。
	if !globalCoordinator.NotifyOne("g", "r") {
		t.Fatal("全局占用者必须可被唤醒")
	}
	if turn := <-globalOccupant; turn != TurnReady {
		t.Fatalf("全局占用者 = %s", turn)
	}
	_ = globalHub
	if turn := coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "s", Reason: "r", DeadlineAtMs: 500}); turn != TurnDeadlineExceeded {
		t.Fatalf("过期截止 = %s", turn)
	}
	first := make(chan string, 1)
	go func() {
		first <- coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "s", Reason: "r", DelayMs: 60_000, DeadlineAtMs: 2_000_000})
	}()
	deadline := time.After(2 * time.Second)
	for coordinator.Snapshot().WaiterCount < 1 {
		select {
		case <-deadline:
			t.Fatal("第一个等待者未注册")
		default:
		}
	}
	if turn := coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "s", Reason: "r", DelayMs: 0, DeadlineAtMs: 2_000_000}); turn != TurnScopeLimit {
		t.Fatalf("作用域容量 = %s", turn)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if turn := coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "s2", Reason: "r", DeadlineAtMs: 2_000_000, Signal: cancelled}); turn != TurnAborted {
		t.Fatalf("已取消信号 = %s", turn)
	}
	// 等待中的信号被取消：观察者必须把等待者结算为 aborted。
	waiting := make(chan string, 1)
	waitCtx, waitCancel := context.WithCancel(context.Background())
	go func() {
		waiting <- coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "s3", Reason: "r", DelayMs: 60_000, DeadlineAtMs: 2_000_000, Signal: waitCtx})
	}()
	for coordinator.Snapshot().WaiterCount != 2 {
		select {
		case <-deadline:
			t.Fatal("第二个等待者未注册")
		default:
		}
	}
	waitCancel()
	if turn := <-waiting; turn != TurnAborted {
		t.Fatalf("取消等待 = %s", turn)
	}
	if !coordinator.NotifyOne("s", "r") {
		t.Fatal("NotifyOne 必须结算首个等待者")
	}
	if turn := <-first; turn != TurnReady {
		t.Fatalf("首个等待者 = %s", turn)
	}
	hub.fireAll()
}

// 恢复等待引擎适配器契约：立即可就绪时零等待返回；跳过原因经
// RecoverableWaitOutcome 错误值透出。
func TestWBPreAuthRecoverableWaitImmediatePaths(t *testing.T) {
	coordinator, _ := newManualCoordinator(t, 4, 4)
	waiter := NewPreAuthRecoverableWait(coordinator, NopLogger)
	if err := waiter.WaitForRecoverableUnavailableState(context.Background(), gatewayPreauthInput(t, true, nil)); err != nil {
		t.Fatalf("立即可就绪必须无错误: %v", err)
	}
	err := waiter.WaitForRecoverableUnavailableState(context.Background(), gatewayPreauthInput(t, false, &RecoverableWaitOutcome{SkippedReason: "deadline_exceeded"}))
	var outcome *RecoverableWaitOutcome
	if !errors.As(err, &outcome) || outcome.Error() != "deadline_exceeded" {
		t.Fatalf("跳过原因必须以错误值透出: %v", err)
	}
}

func gatewayPreauthInput(t *testing.T, ready bool, _ *RecoverableWaitOutcome) gatewaypreauth.RecoverableWaitInput {
	t.Helper()
	return gatewaypreauth.RecoverableWaitInput{
		ScopeKey: "scope", Reason: "recoverable_unavailable", MaxWaitMs: 5,
		IsReady:          func(context.Context) bool { return ready },
		NextRetryAfterMs: func(context.Context) (int64, bool) { return 0, false },
	}
}

// WaitForStateLoop 契约：首采样就绪直接返回；刷新错误向上传播；
// 选项覆盖生效；时钟推进后的下一轮刷新返回就绪。
func TestWBDispatchWaitStateLoop(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 4, 4)
	waiter := &PreAuthRecoverableWait{Coordinator: coordinator, Logger: NopLogger, Options: WaitEngineOptions{MaxWaitMs: 60_000, CheckIntervalMs: 20}}
	waited, skipped, err := waiter.WaitForStateLoop(context.Background(), StateWaitInput{
		// 引擎按墙钟计算本地截止时间，必须提供当前请求起点。
		RequestStartedAtMs: time.Now().UnixMilli(),
		ScopeKey:           "scope", Reason: "suppression", MaxWaitMs: 5,
		Refresh: func(context.Context) (bool, bool, int64, error) { return true, false, 0, nil },
	})
	if err != nil || skipped != "" || waited < 0 {
		t.Fatalf("ready-first loop = (%d, %q, %v)", waited, skipped, err)
	}
	if _, _, err := waiter.WaitForStateLoop(context.Background(), StateWaitInput{
		ScopeKey: "scope", Reason: "suppression", MaxWaitMs: 5,
		Refresh: func(context.Context) (bool, bool, int64, error) {
			return false, false, 0, errors.New("状态读取失败")
		},
	}); err == nil || !strings.Contains(err.Error(), "状态读取失败") {
		t.Fatalf("刷新错误必须传播: %v", err)
	}
	// 首采样未就绪：等待者注册后触发计时器，下一轮刷新样本变为就绪。
	samples := 0
	done := make(chan struct{})
	var waitedMs int64
	var skip string
	go func() {
		waitedMs, skip, err = waiter.WaitForStateLoop(context.Background(), StateWaitInput{
			RequestStartedAtMs: time.Now().UnixMilli(),
			// DeadlineAtMs=0 会被引擎视为已过期，必须提供重试预算截止。
			DeadlineAtMs: time.Now().UnixMilli() + 60_000,
			ScopeKey:     "scope", Reason: "suppression", MaxWaitMs: 10_000,
			Refresh: func(context.Context) (bool, bool, int64, error) {
				samples++
				return samples > 1, false, 0, nil
			},
		})
		close(done)
	}()
	registered := time.After(2 * time.Second)
	for coordinator.Snapshot().WaiterCount < 1 {
		select {
		case <-registered:
			t.Fatal("等待者未注册")
		default:
		}
	}
	hub.fireAll()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("状态循环未在计时器触发后返回")
	}
	if err != nil || skip != "" || samples != 2 {
		t.Fatalf("second-sample loop = (%d, %q, samples=%d, err=%v)", waitedMs, skip, samples, err)
	}
}

// Attempt 非确认路径契约：ReportUnknown 无确认时返回 nil；
// 前台怀疑后的 framing 完成走 key-rotation 关闭路径。
func TestWBAttemptForegroundFramingAndUnknown(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil || result.Outcome != PrepareDispatchable {
		t.Fatalf("prepare = (%s, %v)", result.Outcome, err)
	}
	plain := result.Attempt
	if unknown, err := plain.ReportUnknown(context.Background()); unknown != nil || err != nil {
		t.Fatalf("无确认 ReportUnknown = (%v, %v)", unknown, err)
	}
	if plain.DeferConfirmationTransportFailureForKeyRotation() {
		t.Fatal("无确认不得延迟键轮换失败")
	}
	if _, err := plain.ReportTransportFailure(context.Background(), TransportFailure{Kind: TransportFailureKindTransport, Reason: "connect refused"}); err != nil {
		t.Fatalf("transport failure: %v", err)
	}
	framing, err := plain.ReportFramingComplete(context.Background())
	if err != nil || framing == nil || framing.Status != MutationApplied || framing.State.Phase != PhaseClosed {
		t.Fatalf("key-rotation framing = (%+v, %v)", framing, err)
	}
}

// 观察者 framing 契约：观察者尝试的 framing 完成走 CloseSuspectFromObserver。
func TestWBAttemptObserverFramingCompletesSuspect(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 1000
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil || !result.Attempt.IsObserver {
		t.Fatalf("observer prepare = %+v err=%v", result, err)
	}
	framing, err := result.Attempt.ReportFramingComplete(context.Background())
	if err != nil || framing == nil || framing.State.Phase != PhaseClosed {
		t.Fatalf("observer framing = (%+v, %v)", framing, err)
	}
}

// ReportUnknown 与键轮换延迟仅在持有确认租约时生效。
func TestWBAttemptConfirmationReportUnknownAndDeferral(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil || !result.Attempt.IsConfirmation() {
		t.Fatalf("confirmation prepare = %+v err=%v", result, err)
	}
	attempt := result.Attempt
	if !attempt.DeferConfirmationTransportFailureForKeyRotation() {
		t.Fatal("持有确认时必须允许延迟键轮换失败")
	}
	// 结算前重复观察允许再次登记（幂等置位）。
	if !attempt.DeferConfirmationTransportFailureForKeyRotation() {
		t.Fatal("结算前重复延迟必须仍然成立")
	}
	unknown, err := attempt.ReportUnknown(context.Background())
	if err != nil || unknown == nil || unknown.Status != MutationApplied {
		t.Fatalf("ReportUnknown = (%+v, %v)", unknown, err)
	}
	// 结算后再次延迟必须拒绝。
	if attempt.DeferConfirmationTransportFailureForKeyRotation() {
		t.Fatal("结算后不得再延迟")
	}
}

// CompleteRequestFramingAfterKeyRotation 契约：缺证据返回 state_mismatch，
// 携带证据时执行键轮换关闭。
func TestWBCompleteRequestFramingAfterKeyRotation(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	scope := protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o"))
	if _, err := service.CompleteRequestFramingAfterKeyRotation(context.Background(), keyRotationFramingInput{scope: scope, generation: 1}); err != nil {
		t.Fatalf("nil-evidence framing 必须返回 state_mismatch 而非错误: %v", err)
	}
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
		failureEvidenceKey:           strPtr(strings.Repeat("a", 64)),
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	completed, err := service.CompleteRequestFramingAfterKeyRotation(context.Background(), keyRotationFramingInput{
		scope: scope, generation: 1, dispatchRevision: revisionOf(t, testAccount()),
		failureEvidenceKey: strPtr(strings.Repeat("a", 64)),
	})
	if err != nil || completed.Status != MutationApplied || completed.State.Phase != PhaseClosed {
		t.Fatalf("key-rotation close = (%+v, %v)", completed, err)
	}
}

// ClearAccountEscalationEvidenceAfterFramingComplete 契约：空修订失败关闭，
// 合法输入把清理请求透传给存储。
func TestWBClearAccountEscalationEvidence(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	scope := accountScope("acc")
	if err := service.ClearAccountEscalationEvidenceAfterFramingComplete(context.Background(), scope, " "); err == nil {
		t.Fatal("空修订必须报错")
	}
	if err := service.ClearAccountEscalationEvidenceAfterFramingComplete(context.Background(), scope, "7"); err != nil {
		t.Fatalf("合法清理请求不得报错: %v", err)
	}
}

// 纯辅助函数契约：确认克隆、升级状态映射、证据归属与运行键。
func TestWBCircuitPureHelpers(t *testing.T) {
	if cloneConfirmation(nil) != nil {
		t.Fatal("nil 确认克隆必须为 nil")
	}
	value := &Confirmation{LeaseID: "lease"}
	if cloned := cloneConfirmation(value); cloned == value || cloned.LeaseID != "lease" {
		t.Fatal("确认克隆必须是独立副本")
	}
	if escalationMutationStatus(EscalationEscalated) != MutationApplied || escalationMutationStatus(EscalationAlreadyActive) != MutationApplied {
		t.Fatal("升级态必须映射 applied")
	}
	if escalationMutationStatus(EscalationRecorded) != MutationIdempotent {
		t.Fatal("记录态必须映射 idempotent")
	}
	if escalationMutationStatus("other") != "other" {
		t.Fatal("未知状态必须原样返回")
	}
	if anySlice(nil) != nil {
		t.Fatal("nil 切片必须保持 nil")
	}
	if got := anySlice([]string{"a"}); len(got.([]any)) != 1 {
		t.Fatalf("anySlice = %#v", got)
	}
	if got := accountCircuitCredentialOwnerIdentity(nil); len(got) != 0 {
		t.Fatalf("nil 凭据身份 = %#v", got)
	}
	identity := accountCircuitCredentialOwnerIdentity(map[string]any{"api_key": "k", "junk": "x", "base_url": "https://u"})
	if len(identity) != 2 || identity["api_key"] != "k" {
		t.Fatalf("凭据身份 = %#v", identity)
	}
	account := testAccount()
	revision := int64(42)
	account.DispatchRevision = &revision
	if got, err := AccountCircuitDispatchRevision(account); err != nil || got != "42" {
		t.Fatalf("显式修订 = %q err=%v", got, err)
	}
	if GatewayAccountRuntimeKeyString("key") != "key" {
		t.Fatal("字符串运行键必须直通")
	}
	if RuntimeAccountIDFromKey("acc:authorized:s:g:a") != "acc" || RuntimeAccountIDFromKey("plain") != "plain" {
		t.Fatal("运行键账户前缀提取不正确")
	}
	if GatewayAccountConcurrencyAccountID("acc", "src") != "src" || GatewayAccountConcurrencyAccountID("acc", " ") != "acc" {
		t.Fatal("并发身份必须优先凭证源账户")
	}
	if _, err := GatewayAccountProtocolModelScope(gatewayruntimecache.OpenAIAccountSecret{ID: "a", AccountAccessType: "account_authorized"}, LaneText, nil); err == nil {
		t.Fatal("授权账户缺绑定上下文必须报错")
	}
	if failureReasonWithEvidence("transport:x", nil) != "transport:x" {
		t.Fatal("无证据的原因必须保持原样")
	}
	if !strings.Contains(failureReasonWithEvidence("transport:x", strPtr(strings.Repeat("a", 64))), gatewayAccountCircuitFailureEvidenceMarker) {
		t.Fatal("带证据的原因必须带标记")
	}
	list := stringList{"a"}
	if stringListEqual(list, stringList{"a"}) != true || stringListEqual(list, stringList{"b"}) || stringListEqual(list, nil) {
		t.Fatal("stringListEqual 语义不正确")
	}
	if got := sortedCopy([]string{"b", "a"}); got[0] != "a" || got[1] != "b" {
		t.Fatalf("sortedCopy = %v", got)
	}
	empty := MutationResult{}.RelatedStatesSlice()
	if empty != nil {
		t.Fatalf("nil 相关状态必须保持 nil: %#v", empty)
	}
	states := EscalationResult{RelatedStates: stateList{{ScopeKey: "s"}}}.RelatedStatesSlice()
	if len(states) != 1 || states[0].ScopeKey != "s" {
		t.Fatalf("相关状态切片 = %#v", states)
	}
}
