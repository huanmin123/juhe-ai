package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 引擎 helper 与上游调度支撑类型的单元测试（enginehelpers.go /
// upstreamdispatch.go 的类型与纯函数 / capacity.go / candfilters.go /
// circuitfacade.go）。所有时间断言基于注入时钟，等待类分支用 1ms 级可控延迟。

// ---------------------------------------------------------------------------
// UpstreamResponseModelSlot（原始上游模型归因）
// ---------------------------------------------------------------------------

func TestUpstreamResponseModelSlotNilSafety(t *testing.T) {
	var slot *UpstreamResponseModelSlot
	slot.Set("gpt-x") // 不应 panic
	if got := slot.Get(); got != "" {
		t.Fatalf("nil slot Get = %q", got)
	}
	slot.Bind(func(string) {}) // 不应 panic
}

func TestUpstreamResponseModelSlotBindAfterPublish(t *testing.T) {
	slot := &UpstreamResponseModelSlot{}
	slot.Set("gpt-5.6-terra")
	received := ""
	slot.Bind(func(model string) { received = model })
	if received != "gpt-5.6-terra" {
		t.Fatalf("绑定后补发模型 = %q", received)
	}
	if slot.Get() != "gpt-5.6-terra" {
		t.Fatalf("Get = %q", slot.Get())
	}
}

func TestUpstreamResponseModelSlotBindBeforePublish(t *testing.T) {
	slot := &UpstreamResponseModelSlot{}
	received := make(chan string, 1)
	slot.Bind(func(model string) { received <- model })
	slot.Set("gpt-5.6-sol")
	if got := <-received; got != "gpt-5.6-sol" {
		t.Fatalf("发布转发 = %q", got)
	}
	// nil 消费方不 panic，也不发布。
	(&UpstreamResponseModelSlot{}).Bind(nil)
}

// ---------------------------------------------------------------------------
// NormalRouteFirstByteAttemptCoordinator（速度优先切换预留协调器）
// ---------------------------------------------------------------------------

func TestFirstByteCoordinatorReservationLifecycle(t *testing.T) {
	// 行为存疑：NormalRouteFirstByteAttemptCoordinator 零值 state 为 ""，而
	// AttachReservation/CanCutover/TransferForCutover 只在 state=="active" 时
	// 生效；dispatchsingle.go 以 `&NormalRouteFirstByteAttemptCoordinator{}`
	// 构造（Node 对应构造即 active），因此当前生产路径上预留永远无法挂载。
	// 本测试按当前实际行为断言零值拒绝，并用白盒方式置 active 覆盖完整生命周期。
	coordinator := &NormalRouteFirstByteAttemptCoordinator{}
	if coordinator.CanCutover() {
		t.Fatal("零值协调器不可切换")
	}
	releasedZero := false
	if coordinator.AttachReservation(&SpeedFirstCutoverReservationView{
		ReleaseFunc: func() { releasedZero = true },
	}) {
		t.Fatal("零值协调器必须拒绝挂载")
	}
	if !releasedZero {
		t.Fatal("被拒绝的预留必须立即释放")
	}
	if coordinator.ReservedTargetAccountID() != "" {
		t.Fatal("无预留时目标账户应为空")
	}
	if coordinator.TransferForCutover() != nil {
		t.Fatal("无预留时转移应返回 nil")
	}

	// 白盒进入 active 状态（生产契约期望的构造态）。
	coordinator.state = "active"
	releasedActive := false
	attached := coordinator.AttachReservation(&SpeedFirstCutoverReservationView{
		TargetAccountIDValue: "a-1",
		ReleaseFunc:          func() { releasedActive = true },
	})
	if !attached || !coordinator.CanCutover() || coordinator.ReservedTargetAccountID() != "a-1" {
		t.Fatalf("挂载预留失败: attached=%v canCutover=%v target=%q", attached, coordinator.CanCutover(), coordinator.ReservedTargetAccountID())
	}
	if releasedActive {
		t.Fatal("未被替换的预留不应被释放")
	}
	// 再挂载时上一个预留（a-1）被释放。
	releasedA2 := false
	coordinator.AttachReservation(&SpeedFirstCutoverReservationView{
		TargetAccountIDValue: "a-2",
		ReleaseFunc:          func() { releasedA2 = true },
	})
	if !releasedActive {
		t.Fatal("替换预留时必须释放上一个预留（a-1）")
	}
	if coordinator.ReservedTargetAccountID() != "a-2" {
		t.Fatalf("目标账户 = %q", coordinator.ReservedTargetAccountID())
	}
	// 转移后预留移交调用方（a-2 不释放），协调器回到无预留状态。
	transferred := coordinator.TransferForCutover()
	if transferred == nil || transferred.TargetAccountIDValue != "a-2" {
		t.Fatalf("转移结果 = %#v", transferred)
	}
	if releasedA2 {
		t.Fatal("转移不释放移交出的预留")
	}
	if coordinator.CanCutover() {
		t.Fatal("转移后不可再切换")
	}
	// 释放移交出的预留。
	transferred.ReleaseFunc()
	if !releasedA2 {
		t.Fatal("调用方释放移交预留必须生效")
	}
	// superseded 状态下挂载的预留立即释放。
	coordinator.Supersede()
	releasedRejected := false
	accepted := coordinator.AttachReservation(&SpeedFirstCutoverReservationView{
		ReleaseFunc: func() { releasedRejected = true },
	})
	if accepted || !releasedRejected {
		t.Fatalf("superseded 后拒绝挂载并释放预留: accepted=%v released=%v", accepted, releasedRejected)
	}
	coordinator.Supersede() // 幂等
	coordinator.ReleaseReservation()
	if coordinator.TransferForCutover() != nil {
		t.Fatal("superseded 后转移必须返回 nil")
	}
}

func TestFirstByteCoordinatorReleaseReservation(t *testing.T) {
	coordinator := &NormalRouteFirstByteAttemptCoordinator{}
	coordinator.ReleaseReservation() // 无预留时不应 panic
	released := false
	coordinator.AttachReservation(&SpeedFirstCutoverReservationView{
		TargetAccountIDValue: "a-1",
		ReleaseFunc:          func() { released = true },
	})
	coordinator.ReleaseReservation()
	if !released {
		t.Fatal("释放预留必须调用 ReleaseFunc")
	}
	if coordinator.CanCutover() || coordinator.ReservedTargetAccountID() != "" {
		t.Fatal("释放后应回到无预留状态")
	}
	// 空 ReleaseFunc 预留不 panic。
	coordinator.AttachReservation(&SpeedFirstCutoverReservationView{TargetAccountIDValue: "a-2"})
	coordinator.ReleaseReservation()
}

// ---------------------------------------------------------------------------
// hotQualityAttemptHandle 生命周期
// ---------------------------------------------------------------------------

type recordingHotQualityLifecycle struct {
	firstByteCalls int
	terminalCalls  int
}

func (r *recordingHotQualityLifecycle) MarkFirstByte(*float64) { r.firstByteCalls++ }
func (r *recordingHotQualityLifecycle) RecordTerminal(context.Context, HotQualityTerminal) {
	r.terminalCalls++
}

func TestHotQualityAttemptHandleLifecycle(t *testing.T) {
	lifecycle := &recordingHotQualityLifecycle{}
	var createdInputs []HotQualityLifecycleInput
	handle := &hotQualityAttemptHandle{
		factory: func(input HotQualityLifecycleInput) HotQualityAttemptLifecycle {
			createdInputs = append(createdInputs, input)
			return lifecycle
		},
		input: HotQualityLifecycleInput{AttemptID: "hotq-1", AccountID: "a-1"},
	}
	handle.MarkFirstByte(nil)
	handle.RecordTerminal(context.Background(), HotQualityTerminal{OutcomeClass: HotQualityOutcomeTimeout})
	handle.MarkFirstByte(nil) // once 语义：工厂只调用一次
	if len(createdInputs) != 1 {
		t.Fatalf("工厂调用次数 = %d", len(createdInputs))
	}
	if lifecycle.firstByteCalls != 2 || lifecycle.terminalCalls != 1 {
		t.Fatalf("生命周期调用 = firstByte:%d terminal:%d", lifecycle.firstByteCalls, lifecycle.terminalCalls)
	}
}

func TestHotQualityAttemptHandleNilFactoryKeepsNoop(t *testing.T) {
	handle := &hotQualityAttemptHandle{}
	handle.MarkFirstByte(nil) // 不应 panic
	handle.RecordTerminal(context.Background(), HotQualityTerminal{})
	if isTransportQualityOutcome(HotQualityOutcomeUnknown) {
		t.Fatal("unknown 不是传输质量结果")
	}
	for _, outcome := range []string{HotQualityOutcomeTransportFailure, HotQualityOutcomeTimeout, HotQualityOutcomeReadInterruption, HotQualityOutcomeIncompleteResponse} {
		if !isTransportQualityOutcome(outcome) {
			t.Fatalf("outcome %q 应是传输质量结果", outcome)
		}
	}
	if isTransportQualityOutcome(HotQualityOutcomeUpstreamResponseFailure) {
		t.Fatal("upstream_response_failure 不是传输质量结果")
	}
}

// ---------------------------------------------------------------------------
// buildUpstreamAttemptFailureMessage / 小工具
// ---------------------------------------------------------------------------

func TestBuildUpstreamAttemptFailureMessage(t *testing.T) {
	if got := buildUpstreamAttemptFailureMessage(2, nil); got != "所有上游账户均失败" {
		t.Fatalf("message = %q", got)
	}
	if got := buildUpstreamAttemptFailureMessage(1, nil); got != "上游账户请求失败" {
		t.Fatalf("单账户 message = %q", got)
	}
	attempt := &UpstreamAttempt{AccountName: "账号 a-1", UpstreamURL: "https://u.example", Message: "上游 5xx"}
	if got := buildUpstreamAttemptFailureMessage(2, attempt); got != "所有上游账户均失败；最后一次尝试 账号 a-1 https://u.example 返回 上游 5xx" {
		t.Fatalf("message = %q", got)
	}
	// 无消息但有状态时回退状态码文本。
	attempt = &UpstreamAttempt{AccountName: "a", UpstreamURL: "u", HasStatus: true, Status: 502}
	if got := buildUpstreamAttemptFailureMessage(2, attempt); got != "所有上游账户均失败；最后一次尝试 a u 返回 502" {
		t.Fatalf("状态码回退 message = %q", got)
	}
	// 两者皆空回退「未知错误」。
	attempt = &UpstreamAttempt{AccountName: "a", UpstreamURL: "u"}
	if got := buildUpstreamAttemptFailureMessage(2, attempt); got != "所有上游账户均失败；最后一次尝试 a u 返回 未知错误" {
		t.Fatalf("未知错误回退 message = %q", got)
	}
}

func TestInt64ToStringAndFormatInt64(t *testing.T) {
	cases := map[int64]string{0: "0", 7: "7", -13: "-13", 123_456: "123456"}
	for value, want := range cases {
		if got := int64ToString(value); got != want {
			t.Fatalf("int64ToString(%d) = %q", value, got)
		}
		if got := formatInt64(value); got != want {
			t.Fatalf("formatInt64(%d) = %q", value, got)
		}
	}
}

func TestPtrHelpers(t *testing.T) {
	if ptrString("") != nil {
		t.Fatal("ptrString 空串应返回 nil")
	}
	if ptrString("x") == nil || *ptrString("x") != "x" {
		t.Fatal("ptrString 非空应返回指针")
	}
	if stringPtrOrNil("") != nil || *stringPtrOrNil("y") != "y" {
		t.Fatal("stringPtrOrNil 语义不符")
	}
	if derefIntPtr(nil) != 0 || derefIntPtr(newIntPtr(5)) != 5 {
		t.Fatal("derefIntPtr 语义不符")
	}
	if boolPtr(true) == nil || !*boolPtr(true) {
		t.Fatal("boolPtr 语义不符")
	}
	if accountRuntimeSourceId(AccountCandidate{ID: "a"}) != "a" {
		t.Fatal("无凭据来源时回落账户 ID")
	}
	source := "src"
	if got := accountRuntimeSourceId(AccountCandidate{ID: "a", CredentialSourceAccountID: &source}); got != "src" {
		t.Fatalf("凭据来源优先, got %q", got)
	}
}

func TestFirstAccountOrHelpers(t *testing.T) {
	accounts := testAccounts("a-1", "a-2")
	if firstAccountOr(nil).ID != "" {
		t.Fatal("空列表回退零值账户")
	}
	if firstAccountOr(accounts).ID != "a-1" {
		t.Fatal("应返回首个账户")
	}
	if firstAccountIDOr(nil, "fallback") != "fallback" {
		t.Fatal("空列表回退 fallback")
	}
	if firstAccountIDOr(accounts, "fallback") != "a-1" {
		t.Fatal("应返回首个账户 ID")
	}
	// 名称只在单账户时投影（Node firstAccountNameOr 语义）。
	if firstAccountNameOr(accounts) != "上游账户" {
		t.Fatalf("多账户 name = %q", firstAccountNameOr(accounts))
	}
	single := testAccounts("a-1")
	if firstAccountNameOr(single) != "账号 a-1" {
		t.Fatalf("单账户 name = %q", firstAccountNameOr(single))
	}
	if firstAccountNameOr(nil) != "上游账户" {
		t.Fatal("空列表回退「上游账户」")
	}
	blank := []AccountCandidate{{ID: "a-1"}}
	if firstAccountNameOr(blank) != "上游账户" {
		t.Fatal("空名称回退「上游账户」")
	}
}

func TestWaitReasonAndSetToSlice(t *testing.T) {
	if waitReason(true) != "precheck_half_open" {
		t.Fatalf("waitReason(true) = %q", waitReason(true))
	}
	if waitReason(false) != "local_account_suppression_dispatch" {
		t.Fatalf("waitReason(false) = %q", waitReason(false))
	}
	slice := setToSlice(map[string]struct{}{"a": {}, "b": {}})
	if len(slice) != 2 {
		t.Fatalf("setToSlice = %#v", slice)
	}
}

func TestRecoverableDispatchSuppressionScopeKey(t *testing.T) {
	if got := recoverableDispatchSuppressionScopeKey("sys", "key", "group", "gpt-test", "a@1,b@2"); got != "sys:key:group:gpt-test:a@1,b@2" {
		t.Fatalf("scope key = %q", got)
	}
}

func TestAccountPhysicalCredentialKey(t *testing.T) {
	if got := accountPhysicalCredentialKey(AccountCandidate{ID: "a-1"}); got != "a-1" {
		t.Fatalf("key = %q", got)
	}
	source := "  src-1  "
	if got := accountPhysicalCredentialKey(AccountCandidate{ID: "a-1", CredentialSourceAccountID: &source}); got != "src-1" {
		t.Fatalf("来源优先并去空白, got %q", got)
	}
	blank := "   "
	if got := accountPhysicalCredentialKey(AccountCandidate{ID: "a-1", CredentialSourceAccountID: &blank}); got != "a-1" {
		t.Fatalf("空白来源回落账户 ID, got %q", got)
	}
}

func TestGatewayAccountRuntimeKeyAuthorizedRequiresBinding(t *testing.T) {
	bare := AccountCandidate{ID: "a-1"}
	key, err := gatewayAccountRuntimeKey(bare)
	if err != nil {
		t.Fatalf("bare account key: %v", err)
	}
	if key == "" {
		t.Fatal("bare key 不能为空")
	}
	authorized := AccountCandidate{ID: "a-1", AccountAccessType: "account_authorized"}
	if _, err := gatewayAccountRuntimeKey(authorized); err == nil {
		t.Fatal("授权账户缺少绑定上下文必须报错")
	}
	bindingSystem := "system-1"
	boundGroup := "group-1"
	authz := "authz-1"
	bound := AccountCandidate{ID: "a-1", AccountAccessType: "account_authorized", BindingSystemAccountID: &bindingSystem, BoundGroupID: &boundGroup, AccountAuthorizationID: &authz}
	boundKey, err := gatewayAccountRuntimeKey(bound)
	if err != nil {
		t.Fatalf("bound key: %v", err)
	}
	if boundKey == key {
		t.Fatal("授权绑定键必须与 bare 键不同")
	}
}

func TestLastCapacityLimitFailure(t *testing.T) {
	if lastCapacityLimitFailure(nil) != nil {
		t.Fatal("空列表返回 nil")
	}
	failures := []AccountCapacityLimitFailure{{message: "first"}, {message: "last"}}
	last := lastCapacityLimitFailure(failures)
	if last == nil || last.message != "last" {
		t.Fatalf("last = %#v", last)
	}
}

func TestRecordAccountCapacityLimitFailure(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	usage := engine.Usage.(*fakeUsage)
	sink := &fakeAuditSink{}
	capture := AuditCapture{Context: &frozenAudit{sink: sink}, Sink: sink}
	err := engine.recordAccountCapacityLimitFailure(context.Background(), testUsageContext(), testAccounts("a-1")[0], "账户并发已达到上限 4/4", capture, 3)
	if err != nil {
		t.Fatalf("recordAccountCapacityLimitFailure: %v", err)
	}
	if len(usage.records) != 1 || usage.records[0].UpstreamURL != "concurrency:limit" || usage.records[0].FailureAttribution != "gateway_capacity" {
		t.Fatalf("usage 记录 = %#v", usage.records)
	}
	if sink.failed != 1 {
		t.Fatalf("审计失败记录数 = %d", sink.failed)
	}
}

func TestAccountCircuitEvidenceDigestStable(t *testing.T) {
	digest := accountCircuitEvidenceDigest(map[string]any{"a": 1})
	if len(digest) != 64 {
		t.Fatalf("digest 长度 = %d", len(digest))
	}
	if digest != accountCircuitEvidenceDigest(map[string]any{"a": 1}) {
		t.Fatal("同输入 digest 必须稳定")
	}
}

// ---------------------------------------------------------------------------
// waitForAccountLockDelay：墙钟 / 协调 / 完成分支
// ---------------------------------------------------------------------------

func TestWaitForAccountLockDelayBranches(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	coordination := newTestCoordination(t)
	wallBudget := coordination.GatewayRequestWallBudget
	routeBudget := coordination.RouteCoordinationBudget

	// delayMs<=0 直接完成。
	outcome, err := engine.waitForAccountLockDelay(context.Background(), "a-1", "lease-1", 0, wallBudget, routeBudget)
	if err != nil || outcome != accountLockWaitCompleted {
		t.Fatalf("零延迟应完成: outcome=%q err=%v", outcome, err)
	}
	outcome, err = engine.waitForAccountLockDelay(context.Background(), "a-1", "lease-1", -5, wallBudget, routeBudget)
	if err != nil || outcome != accountLockWaitCompleted {
		t.Fatalf("负延迟应完成: outcome=%q err=%v", outcome, err)
	}

	// delayMs 超出墙钟剩余 → wall。
	outcome, err = engine.waitForAccountLockDelay(context.Background(), "a-1", "lease-1", 10*60_000, wallBudget, routeBudget)
	if err != nil || outcome != accountLockWaitWall {
		t.Fatalf("超墙钟应返回 wall: outcome=%q err=%v", outcome, err)
	}

	// delayMs 在墙钟内但超出协调预算（默认 3s）→ coordination。
	outcome, err = engine.waitForAccountLockDelay(context.Background(), "a-1", "lease-1", 5_000, wallBudget, routeBudget)
	if err != nil || outcome != accountLockWaitCoordination {
		t.Fatalf("超协调预算应返回 coordination: outcome=%q err=%v", outcome, err)
	}

	// 1ms 可控延迟 → completed（真实 BeginWait/PauseWait 转换）。
	// 注意：wait token 在同一预算上是一次性的（幂等重放语义），每个分支用独立 leaseID。
	outcome, err = engine.waitForAccountLockDelay(context.Background(), "a-1", "lease-completed", 1, wallBudget, routeBudget)
	if err != nil || outcome != accountLockWaitCompleted {
		t.Fatalf("1ms 延迟应完成: outcome=%q err=%v", outcome, err)
	}

	// 信号取消 → aborted。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, err = engine.waitForAccountLockDelay(canceled, "a-1", "lease-cancelled", 1, wallBudget, routeBudget)
	if err != nil || outcome != accountLockWaitAborted {
		t.Fatalf("取消信号应返回 aborted: outcome=%q err=%v", outcome, err)
	}
}

// ---------------------------------------------------------------------------
// acquireAccountConcurrencyWithShortRetry / 延迟策略
// ---------------------------------------------------------------------------

func TestNextConcurrencyRetryDelayMsExponential(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 默认 initial=120ms, max=480ms。
	cases := map[int64]int64{
		1: 120, 2: 240, 3: 480, 4: 480, 10: 480,
	}
	for attempt, want := range cases {
		if got := engine.nextConcurrencyRetryDelayMs(attempt, 60_000); got != want {
			t.Fatalf("attempt=%d delay=%d 期望 %d", attempt, got, want)
		}
	}
	if got := engine.nextConcurrencyRetryDelayMs(1, 50); got != 50 {
		t.Fatalf("剩余预算应截断延迟, got %d", got)
	}
}

func TestAccountConcurrencyLaneAcquireOptions(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	options := engine.accountConcurrencyLaneAcquireOptions(4, gatewayprotoLane("text"), nil)
	if options.Lane != "text" || options.LaneLimit != nil {
		t.Fatalf("text lane options = %#v", options)
	}
	options = engine.accountConcurrencyLaneAcquireOptions(4, gatewayprotoLane("image"), nil)
	if options.Lane != "image" || options.LaneLimit == nil || *options.LaneLimit < 1 {
		t.Fatalf("image lane options = %#v", options)
	}
}

func TestAcquireAccountConcurrencyWithShortRetryExhaustion(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &countingBusyConcurrencyStore{}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 4, 10, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if slot.Acquired {
		t.Fatal("耗尽的存储不应获取槽位")
	}
	if waitedMs <= 0 {
		t.Fatalf("短等后 waitedMs 应大于 0, got %d", waitedMs)
	}
	if store.acquireCalls.Load() < 2 {
		t.Fatalf("预算内应存在多次重试获取, got %d", store.acquireCalls.Load())
	}
}

// countingBusyConcurrencyStore 恒拒绝并统计获取调用次数。
type countingBusyConcurrencyStore struct {
	acquireCalls atomic.Int64
}

func (m *countingBusyConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (m *countingBusyConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (m *countingBusyConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	m.acquireCalls.Add(1)
	return ConcurrencySlot{Acquired: false, Current: 4, Limit: 4, Lane: "text"}, nil
}

func TestAcquireAccountConcurrencyWithShortRetryAbort(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &fakeConcurrencyStore{exhausted: true, limit: 4}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := engine.acquireAccountConcurrencyWithShortRetry(
		canceled, canceled, "a-1", 4, 10_000, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget,
	)
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) {
		t.Fatalf("取消应返回 aborted 错误, got %v", err)
	}
}

type errorConcurrencyStore struct{}

func (errorConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return nil, nil
}

func (errorConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return nil, nil
}

func (errorConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{}, errors.New("store boom")
}

func TestAcquireAccountConcurrencyStoreError(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	engine.Concurrency = errorConcurrencyStore{}
	coordination := newTestCoordination(t)
	_, _, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 4, 10, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget,
	)
	if err == nil || err.Error() != "store boom" {
		t.Fatalf("存储错误应透传, got %v", err)
	}
}

func TestAcquireAccountConcurrencyImmediateSuccess(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &fakeConcurrencyStore{}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 4, 1_200, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget,
	)
	if err != nil || !slot.Acquired || waitedMs != 0 {
		t.Fatalf("首取成功: slot=%+v waited=%d err=%v", slot, waitedMs, err)
	}
}

// ---------------------------------------------------------------------------
// takeReservedSlot / onceFunc / halfOpenLeaseUnclaimed
// ---------------------------------------------------------------------------

func TestTakeReservedSlot(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	if engine.takeReservedSlot(nil, testAccounts("a-1")[0]) != nil {
		t.Fatal("nil handle 返回 nil")
	}
	if engine.takeReservedSlot(&SpeedFirstCutoverReservationHandle{}, testAccounts("a-1")[0]) != nil {
		t.Fatal("无 TakeForAccount 的 handle 返回 nil")
	}
	called := false
	handle := &SpeedFirstCutoverReservationHandle{
		TakeForAccount: func(account AccountCandidate) (ConcurrencySlot, bool) {
			called = true
			if account.ID != "a-1" {
				t.Fatalf("takeForAccount 收到 %s", account.ID)
			}
			return ConcurrencySlot{Acquired: true}, true
		},
	}
	slot := engine.takeReservedSlot(handle, testAccounts("a-1")[0])
	if !called || slot == nil || !slot.Acquired {
		t.Fatalf("slot = %#v", slot)
	}
	missed := engine.takeReservedSlot(&SpeedFirstCutoverReservationHandle{
		TakeForAccount: func(AccountCandidate) (ConcurrencySlot, bool) { return ConcurrencySlot{}, false },
	}, testAccounts("a-1")[0])
	if missed != nil {
		t.Fatal("未命中预留应返回 nil")
	}
}

func TestOnceFuncIdempotent(t *testing.T) {
	calls := 0
	once := onceFunc(func() { calls++ })
	once()
	once()
	once()
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestHalfOpenLeaseUnclaimed(t *testing.T) {
	if !halfOpenLeaseUnclaimed(nil) {
		t.Fatal("无租约视为未声明")
	}
	if !halfOpenLeaseUnclaimed(fakeHalfOpenLease{}) {
		t.Fatal("generation=nil 视为未声明")
	}
	generation := int64(3)
	if halfOpenLeaseUnclaimed(fakeHalfOpenLease{generation: &generation}) {
		t.Fatal("有 generation 的租约视为已声明")
	}
}

type fakeHalfOpenLease struct {
	generation *int64
	released   *bool
}

func (f fakeHalfOpenLease) RuntimeKey() string { return "rk:test" }

func (f fakeHalfOpenLease) Generation() *int64 { return f.generation }

func (f fakeHalfOpenLease) Release() (bool, error) {
	if f.released != nil {
		*f.released = true
	}
	return true, nil
}

func (f fakeHalfOpenLease) CompleteSuccess() (bool, error) { return true, nil }

// ---------------------------------------------------------------------------
// 账户尝试构造器
// ---------------------------------------------------------------------------

func TestAccountAttemptBuilders(t *testing.T) {
	account := AccountCandidate{
		ID: "a-1", Name: "账号 a-1", ProviderCode: "openai",
		ProviderProtocolProfileID: "profile", ProtocolCode: "openai", ProtocolVersion: "v1",
	}
	attempt := accountApiKeyPoolUnavailableAttempt(account)
	if attempt.UpstreamURL != "account:api_key_pool_unavailable" || attempt.Message != "账户 API Key 池暂无可用 Key" {
		t.Fatalf("pool unavailable attempt = %#v", attempt)
	}
	attempt = accountApiKeyRetryBudgetExhaustedAttempt(account, "上限")
	if attempt.UpstreamURL != "account:api_key_request_retry_budget_exhausted" || attempt.Message != "上限" {
		t.Fatalf("budget exhausted attempt = %#v", attempt)
	}
	attempt = accountCircuitBlockedAttempt(account, "half_open")
	if attempt.UpstreamURL != "account:circuit_blocked" || attempt.Message != "账户短电路处于 half_open" {
		t.Fatalf("circuit blocked attempt = %#v", attempt)
	}
	attempt = accountCapacityLimitAttempt(account, "并发上限")
	if attempt.UpstreamURL != "concurrency:limit" || attempt.Message != "并发上限" {
		t.Fatalf("capacity attempt = %#v", attempt)
	}
	guidance := &gatewaypreauth.GatewayAgentGuidanceResponse{Message: "仅本账户可用"}
	attempt = accountScopedGuidanceAttempt(account, guidance)
	if attempt.UpstreamURL != "gateway:agent_guidance" || attempt.Message != "仅本账户可用" {
		t.Fatalf("guidance attempt = %#v", attempt)
	}
	attempt = requestDeduplicatedAttempt(account, "duplicate")
	if attempt.UpstreamURL != "account:request_deduplicated" || attempt.Message != "请求内候选已尝试：duplicate" {
		t.Fatalf("dedup attempt = %#v", attempt)
	}
	attempt = keyModelUnavailableAttempt(account, "busy")
	if attempt.UpstreamURL != "account:key_model_unavailable" || attempt.Message != "精确 Key-model 候选暂不可选：busy" {
		t.Fatalf("keymodel attempt = %#v", attempt)
	}
	// 身份字段全量投影。
	if attempt.AccountID != "a-1" || attempt.ProviderCode != "openai" || attempt.ProtocolVersion != "v1" {
		t.Fatalf("身份投影缺失: %#v", attempt)
	}
}

func TestLocallySuppressedAttemptMessage(t *testing.T) {
	attempt := locallySuppressedAttempt(testAccounts("a-1")[0], nil)
	if attempt.Message != "账号处于本地短期屏蔽" {
		t.Fatalf("message = %q", attempt.Message)
	}
	next := int64(2_500)
	attempt = locallySuppressedAttempt(testAccounts("a-1")[0], &next)
	if attempt.Message != "账号处于本地短期屏蔽，预计 3 秒后释放" {
		t.Fatalf("带重试时间 message = %q", attempt.Message)
	}
	zero := int64(0)
	attempt = locallySuppressedAttempt(testAccounts("a-1")[0], &zero)
	if attempt.Message != "账号处于本地短期屏蔽，预计 1 秒后释放" {
		t.Fatalf("零重试时间下限 message = %q", attempt.Message)
	}
}

func TestAccountConcurrencyLimitMessage(t *testing.T) {
	if got := accountConcurrencyLimitMessage(ConcurrencySlot{Current: 4, Limit: 4, Lane: "text"}, 0); got != "账户并发已达到上限 4/4" {
		t.Fatalf("message = %q", got)
	}
	if got := accountConcurrencyLimitMessage(ConcurrencySlot{Current: 4, Limit: 4, Lane: "text"}, 240); got != "账户并发已达到上限 4/4（短等 240ms 后仍未释放）" {
		t.Fatalf("带短等 message = %q", got)
	}
	if got := accountConcurrencyLimitMessage(ConcurrencySlot{Lane: "image", LaneCurrent: 3, LaneLimit: 3, Current: 2, Limit: 4}, 0); got != "账户图像通道并发已达到上限 3/3，已为文本通道保留并发槽" {
		t.Fatalf("image lane message = %q", got)
	}
}

// ---------------------------------------------------------------------------
// API Key 轮换重试判定
// ---------------------------------------------------------------------------

func accountWithKeys(id string, keys ...string) AccountCandidate {
	account := testAccounts(id)[0]
	account.APIKeys = keys
	return account
}

func TestShouldRetryAnotherAccountApiKeyBranches(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	capture := AuditCapture{Context: &frozenAudit{sink: &fakeAuditSink{}}, Sink: &fakeAuditSink{}}
	fingerprint := "fp-1"
	account := accountWithKeys("a-1", "k1", "k2")
	account.SelectedAPIKeyFingerprint = &fingerprint

	if engine.shouldRetryAnotherAccountApiKey(account, false, 0, 0, capture) {
		t.Fatal("非 Key 级失败不应轮换")
	}
	if !engine.shouldRetryAnotherAccountApiKey(account, true, 0, 0, capture) {
		t.Fatal("Key 级失败且有剩余 Key 应轮换")
	}
	if engine.shouldRetryAnotherAccountApiKey(accountWithKeys("a-1", "k1"), true, 0, 0, capture) {
		t.Fatal("无 SelectedAPIKeyFingerprint 不应轮换")
	}
	disabledAccount := accountWithKeys("a-1", "k1", "k2")
	disabledAccount.SelectedAPIKeyFingerprint = &fingerprint
	disabledAccount.APIKeyRuntimeStateDisabled = true
	if engine.shouldRetryAnotherAccountApiKey(disabledAccount, true, 0, 0, capture) {
		t.Fatal("运行态禁用不应轮换")
	}
	if engine.shouldRetryAnotherAccountApiKey(account, true, 2, 0, capture) {
		t.Fatal("Key 池穷尽不应轮换")
	}
	// 请求级安全上限耗尽。
	engine.Config.AccountApiKeyRequestAttemptSafetyLimit = 1
	if engine.shouldRetryAnotherAccountApiKey(account, true, 0, 1, capture) {
		t.Fatal("请求级安全上限耗尽不应轮换")
	}
}

func TestShouldTryAnotherAccountApiKeyForRequest(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	fingerprint := "fp"
	account := accountWithKeys("a-1", "k1", "k2")
	account.SelectedAPIKeyFingerprint = &fingerprint
	if !engine.shouldTryAnotherAccountApiKeyForRequest(account, 1, 0) {
		t.Fatal("未达上限且有剩余 Key 应轮换")
	}
	if engine.shouldTryAnotherAccountApiKeyForRequest(account, 2, 0) {
		t.Fatal("Key 池穷尽不应轮换")
	}
	if engine.shouldTryAnotherAccountApiKeyForRequest(accountWithKeys("a-1", "k1"), 0, 0) {
		t.Fatal("无选中指纹不应轮换")
	}
	engine.Config.AccountApiKeyRequestAttemptSafetyLimit = 3
	if engine.shouldTryAnotherAccountApiKeyForRequest(account, 0, 3) {
		t.Fatal("达到请求级安全上限不应轮换")
	}
}

func TestAssertGatewayRequestWallBudgetAvailableForAttempt(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	coordination := newTestCoordination(t)
	sink := &fakeAuditSink{}
	capture := AuditCapture{Context: &frozenAudit{sink: sink}, Sink: sink}
	if err := engine.assertGatewayRequestWallBudgetAvailableForAttempt(coordination.GatewayRequestWallBudget, coordination.RequestAttemptTracker, capture, 2_000); err != nil {
		t.Fatalf("预算充足不应报错: %v", err)
	}
	exhausted, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: NowMs() - 60_000,
		BudgetMs:            ptrInt64(60_000),
	}, nil)
	if err != nil {
		t.Fatalf("wall budget: %v", err)
	}
	err = engine.assertGatewayRequestWallBudgetAvailableForAttempt(exhausted, coordination.RequestAttemptTracker, capture, 2_000)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) {
		t.Fatalf("预算耗尽应报错, got %v", err)
	}
}

func TestShouldRetainTransportFailureForRecovery(t *testing.T) {
	if !shouldRetainTransportFailureForRecovery("https://u.example/v1", context.Background()) {
		t.Fatal("https URL 且未取消应保留")
	}
	if !shouldRetainTransportFailureForRecovery("http://u.example/v1", nil) {
		t.Fatal("nil signal 视为未取消")
	}
	if shouldRetainTransportFailureForRecovery("account:preparation", context.Background()) {
		t.Fatal("非 http(s) URL 不保留")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldRetainTransportFailureForRecovery("https://u.example", canceled) {
		t.Fatal("已取消不保留")
	}
}

func TestIsLocalRequestFailure(t *testing.T) {
	if isLocalRequestFailure(errors.New("x")) {
		t.Fatal("普通错误不是本地请求失败")
	}
	if !isLocalRequestFailure(&gatewaypreauth.GatewayAgentGuidanceResponse{}) {
		t.Fatal("guidance 响应是本地请求失败")
	}
	if !isLocalRequestFailure(&gatewaypreauth.GatewayLocalProtocolResponse{}) {
		t.Fatal("local protocol 响应是本地请求失败")
	}
	if !isLocalRequestFailure(&gatewaypreauth.GatewayRequestValidationError{}) {
		t.Fatal("validation 错误是本地请求失败")
	}
	if !isLocalRequestFailure(NewOpenAIOAuthCodexAdapterError("x")) {
		t.Fatal("adapter 错误是本地请求失败")
	}
}

func TestLastStatusMessageAndDeadlinePtr(t *testing.T) {
	if lastStatusOf(nil) != 0 {
		t.Fatal("nil attempt 状态为 0")
	}
	if lastMessageOf(nil) != "" {
		t.Fatal("nil attempt 消息为空")
	}
	if got := lastStatusOf(&UpstreamAttempt{Status: 502}); got != 502 {
		t.Fatalf("status = %d", got)
	}
	if got := lastMessageOf(&UpstreamAttempt{Message: "x"}); got != "x" {
		t.Fatalf("message = %q", got)
	}
	if deadlineMsPtr(nil) != nil {
		t.Fatal("nil deadline 返回 nil")
	}
	value := deadlineMsPtr(&gatewayrouting.NormalRouteAttemptFirstByteDeadline{EffectiveDeadlineMs: 1234})
	if value == nil || *value != 1234 {
		t.Fatalf("deadline ptr = %#v", value)
	}
}

// ---------------------------------------------------------------------------
// recordConfirmedSameAccountApiKeyFailures / recordAccountAPIKeySuccess
// ---------------------------------------------------------------------------

type recordingAPIKeyEffects struct {
	failures        []RecordAPIKeyFailureInput
	successes       []RecordAPIKeySuccessInput
	failAccounts    []AccountCandidate
	successAccounts []AccountCandidate
}

func (r *recordingAPIKeyEffects) CaptureFailureObservation(account AccountCandidate) string {
	return "obs-" + account.ID
}

func (r *recordingAPIKeyEffects) RecordFailure(ctx context.Context, account AccountCandidate, input RecordAPIKeyFailureInput) error {
	r.failures = append(r.failures, input)
	r.failAccounts = append(r.failAccounts, account)
	return nil
}

func (r *recordingAPIKeyEffects) RecordSuccess(ctx context.Context, account AccountCandidate, input RecordAPIKeySuccessInput) error {
	r.successes = append(r.successes, input)
	r.successAccounts = append(r.successAccounts, account)
	return nil
}

func TestRecordConfirmedSameAccountApiKeyFailures(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	effects := &recordingAPIKeyEffects{}
	engine.APIKeyEffects = effects
	usage := testUsageContext()
	successFingerprint := "fp-k1"
	success := accountWithKeys("a-1", "k1", "k2")
	success.SelectedAPIKeyFingerprint = &successFingerprint
	otherFingerprint := "fp-k2"
	failureAccount := accountWithKeys("a-1", "k1", "k2")
	failureAccount.SelectedAPIKeyFingerprint = &otherFingerprint
	// 同账户、不同指纹的失败才被确认（两条同名失败都确认）。
	err := engine.recordConfirmedSameAccountApiKeyFailures(context.Background(), []PendingAccountApiKeyFailure{
		{Account: failureAccount, Status: "temporary_unavailable"},
		{Account: failureAccount, Status: "cooldown"},
	}, success, &usage)
	if err != nil {
		t.Fatalf("recordConfirmedSameAccountApiKeyFailures: %v", err)
	}
	if len(effects.failures) != 2 {
		t.Fatalf("确认失败数 = %d", len(effects.failures))
	}
	if effects.failures[0].Source != "same_account_api_key_rotation_confirmed" {
		t.Fatalf("source = %q", effects.failures[0].Source)
	}
	// 空失败列表 / 成功账户无指纹 / 未装配端口全部中性跳过。
	if err := engine.recordConfirmedSameAccountApiKeyFailures(context.Background(), nil, success, &usage); err != nil {
		t.Fatalf("空失败列表: %v", err)
	}
	noFingerprint := accountWithKeys("a-1", "k1")
	if err := engine.recordConfirmedSameAccountApiKeyFailures(context.Background(), []PendingAccountApiKeyFailure{{Account: failureAccount}}, noFingerprint, &usage); err != nil {
		t.Fatalf("成功账户无指纹: %v", err)
	}
	engine.APIKeyEffects = nil
	if err := engine.recordConfirmedSameAccountApiKeyFailures(context.Background(), []PendingAccountApiKeyFailure{{Account: failureAccount}}, success, &usage); err != nil {
		t.Fatalf("未装配端口: %v", err)
	}
	if len(effects.failures) != 2 {
		t.Fatal("中性路径不应新增失败记录")
	}
}

func TestRecordConfirmedSameAccountApiKeyFailuresSkipsOthersAndSameFingerprint(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	effects := &recordingAPIKeyEffects{}
	engine.APIKeyEffects = effects
	usage := testUsageContext()
	fp1 := "fp-1"
	success := accountWithKeys("a-1", "k1", "k2")
	success.SelectedAPIKeyFingerprint = &fp1
	// 其他账户的失败不确认。
	other := accountWithKeys("a-2", "k1")
	otherFp := "fp-other"
	other.SelectedAPIKeyFingerprint = &otherFp
	// 相同指纹的失败不确认（成功 Key 自身的失败）。
	sameFp := accountWithKeys("a-1", "k1", "k2")
	sameFp.SelectedAPIKeyFingerprint = &fp1
	if err := engine.recordConfirmedSameAccountApiKeyFailures(context.Background(), []PendingAccountApiKeyFailure{
		{Account: other},
		{Account: sameFp},
	}, success, &usage); err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(effects.failures) != 0 {
		t.Fatalf("不应确认任何失败, got %d", len(effects.failures))
	}
}

func TestRecordAccountAPIKeySuccess(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	effects := &recordingAPIKeyEffects{}
	engine.APIKeyEffects = effects
	usage := testUsageContext()
	fp := "fp-1"
	success := accountWithKeys("a-1", "k1")
	success.SelectedAPIKeyFingerprint = &fp
	if err := engine.recordAccountAPIKeySuccess(context.Background(), success, &usage); err != nil {
		t.Fatalf("recordAccountAPIKeySuccess: %v", err)
	}
	if len(effects.successes) != 1 || effects.successes[0].Source != "upstream_dispatch_success" {
		t.Fatalf("success 记录 = %#v", effects.successes)
	}
	noFingerprint := accountWithKeys("a-1", "k1")
	if err := engine.recordAccountAPIKeySuccess(context.Background(), noFingerprint, &usage); err != nil {
		t.Fatalf("无指纹: %v", err)
	}
	engine.APIKeyEffects = nil
	if err := engine.recordAccountAPIKeySuccess(context.Background(), success, &usage); err != nil {
		t.Fatalf("未装配端口: %v", err)
	}
	if len(effects.successes) != 1 {
		t.Fatal("中性跳过不应新增成功记录")
	}
}

// ---------------------------------------------------------------------------
// capacity.go 纯函数与并发快照
// ---------------------------------------------------------------------------

func TestGatewayAccountConcurrencyLimitsByAccountID(t *testing.T) {
	source := "src-1"
	accounts := []AccountCandidate{
		{ID: "a", ConcurrencyLimit: 4, CredentialSourceAccountID: &source},
		{ID: "b", ConcurrencyLimit: 2, CredentialSourceAccountID: &source},
		{ID: "c", ConcurrencyLimit: 0},
	}
	limits := GatewayAccountConcurrencyLimitsByAccountID(accounts)
	// 同一凭据来源取较小限制；无来源用账户 ID；limit<1 提升为 1。
	if limits["src-1"] != 2 || limits["c"] != 1 {
		t.Fatalf("limits = %#v", limits)
	}
}

// errorLoadingConcurrencyStore 的 LoadCurrentAsync 恒失败（快照错误透传面）。
type errorLoadingConcurrencyStore struct{}

func (errorLoadingConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return nil, errors.New("load boom")
}

func (errorLoadingConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return nil, nil
}

func (errorLoadingConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{Acquired: true}, nil
}

func TestRefreshGatewayAccountCurrentConcurrencyAsync(t *testing.T) {
	store := &mapBackedConcurrencyStore{current: map[string]int{"a-1": 3}}
	accounts, err := RefreshGatewayAccountCurrentConcurrencyAsync(context.Background(), store, testAccounts("a-1", "a-2"))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if accounts[0].CurrentConcurrency == nil || *accounts[0].CurrentConcurrency != 3 {
		t.Fatalf("a-1 并发 = %#v", accounts[0].CurrentConcurrency)
	}
	if accounts[1].CurrentConcurrency == nil || *accounts[1].CurrentConcurrency != 0 {
		t.Fatalf("a-2 并发应补 0: %#v", accounts[1].CurrentConcurrency)
	}
	if _, err := RefreshGatewayAccountCurrentConcurrencyAsync(context.Background(), errorLoadingConcurrencyStore{}, nil); err == nil {
		t.Fatal("存储错误应透传")
	}
}

type mapBackedConcurrencyStore struct {
	current map[string]int
}

func (m *mapBackedConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return m.current, nil
}

func (m *mapBackedConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (m *mapBackedConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{Acquired: true}, nil
}

func TestOrderAccountsByLaneCapacityBusyState(t *testing.T) {
	// a-1 满载（4/4），a-2 空闲：空闲账户应排前。
	store := &mapBackedConcurrencyStore{current: map[string]int{"a-1": 4, "a-2": 0}}
	accounts := testAccounts("a-1", "a-2")
	ordered, err := OrderGatewayAccountsByLaneCapacityAvailabilityAsync(context.Background(), store, accounts, "text", nil, nil)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if ordered[0].ID != "a-2" {
		t.Fatalf("顺序 = %#v", accountIDs(ordered))
	}
	// image 通道：lane 独立并发饱和的账户被判忙。
	imageStore := &laneBackedConcurrencyStore{current: map[string]int{"a-1": 1}, image: map[string]int{"a-1": 4}}
	ordered, err = OrderGatewayAccountsByLaneCapacityAvailabilityAsync(context.Background(), imageStore, accounts, "image", nil, nil)
	if err != nil {
		t.Fatalf("image order: %v", err)
	}
	if ordered[0].ID != "a-2" {
		t.Fatalf("image 顺序 = %#v", accountIDs(ordered))
	}
	busy, err := AreGatewayAccountsCapacityBusyForLaneAsync(context.Background(), imageStore, accounts, "image", nil)
	if err != nil || !busy {
		t.Fatalf("image busy = %v err=%v", busy, err)
	}
	// 单账户不重排。
	single := testAccounts("a-1")
	ordered, err = OrderGatewayAccountsByLaneCapacityAvailabilityAsync(context.Background(), store, single, "text", nil, nil)
	if err != nil || ordered[0].ID != "a-1" {
		t.Fatalf("单账户透传: %#v %v", ordered, err)
	}
}

type laneBackedConcurrencyStore struct {
	current map[string]int
	image   map[string]int
}

func (m *laneBackedConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return m.current, nil
}

func (m *laneBackedConcurrencyStore) LoadCurrentByLaneAsync(_ context.Context, _ []string, lane string) (map[string]int, error) {
	if lane == "image" {
		return m.image, nil
	}
	return map[string]int{}, nil
}

func (m *laneBackedConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{Acquired: true}, nil
}

func TestDerefPolicyAndModelPriorityRankMap(t *testing.T) {
	if derefPolicy(nil) != nil {
		t.Fatal("nil policy 解引用应为 nil")
	}
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	if derefPolicy(&policy) == nil {
		t.Fatal("非 nil policy 应解引用")
	}
	if modelPriorityRankMap(nil) != nil {
		t.Fatal("nil priority 返回 nil")
	}
	mapped := modelPriorityRankMap(&gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{"a": 1}})
	if mapped["a"] != 1 {
		t.Fatalf("rank map = %#v", mapped)
	}
}

// ---------------------------------------------------------------------------
// candfilters.go 模型覆盖请求视图与端点族
// ---------------------------------------------------------------------------

func TestGatewayRequestWithModelOverride(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	cloned := gatewayRequestWithModelOverride(req, "  mapped-model  ")
	if cloned == req {
		t.Fatal("有覆盖时必须克隆请求")
	}
	if cloned.Body.Body.(map[string]any)["model"] != "mapped-model" {
		t.Fatalf("覆盖模型 = %#v", cloned.Body.Body)
	}
	if cloned.Body.State.Model == nil || *cloned.Body.State.Model != "mapped-model" {
		t.Fatalf("state model = %#v", cloned.Body.State.Model)
	}
	// 原请求不被修改。
	if req.Body.Body.(map[string]any)["model"] != "gpt-test" {
		t.Fatal("原请求 model 被污染")
	}
	// 空覆盖透传原请求。
	if gatewayRequestWithModelOverride(req, "   ") != req {
		t.Fatal("空覆盖应透传")
	}
}

func TestGatewayBodyJSONObject(t *testing.T) {
	if _, ok := gatewaybodyJSONObject(nil); ok {
		t.Fatal("nil 请求无对象")
	}
	if _, ok := gatewaybodyJSONObject(&gatewaypreauth.GatewayRequest{}); ok {
		t.Fatal("无 body 无对象")
	}
	req := newTestRequest(t, `{"model":"gpt-test"}`)
	object, ok := gatewaybodyJSONObject(req)
	if !ok || object["model"] != "gpt-test" {
		t.Fatalf("object = %#v ok=%v", object, ok)
	}
}

func TestBypassGatewayModelFilter(t *testing.T) {
	result := BypassGatewayModelFilter([]AccountCandidate{
		{ID: "limited", SupportedModels: []string{"m"}},
		{ID: "unlimited"},
	}, "chat_completions")
	if result.DirectMatchedCount != 2 || result.LimitedAccountCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.ModelPriority.RankByAccountID["limited"] != ModelPriorityRankDirect {
		t.Fatal("bypass 全部直连 rank")
	}
	if result.ModelPriority.RankByAccountID["unlimited"] != ModelPriorityRankDirect {
		t.Fatal("无约束账户同为直连 rank")
	}
	if result.SourceEndpointFamily != "chat_completions" {
		t.Fatalf("family = %q", result.SourceEndpointFamily)
	}
}

func TestGatewayAccountModelPriorityFor(t *testing.T) {
	if GatewayAccountModelPriorityFor("a", nil) != ModelPriorityRankDirect {
		t.Fatal("nil priority 回落 direct")
	}
	priority := &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{"a": ModelPriorityRankMapping}}
	if GatewayAccountModelPriorityFor("a", priority) != ModelPriorityRankMapping {
		t.Fatal("已知账户返回其 rank")
	}
	if GatewayAccountModelPriorityFor("b", priority) != ModelPriorityRankUnsupported {
		t.Fatal("未知账户回落 unsupported")
	}
}

func TestGatewayRequestEndpointFamilyVariants(t *testing.T) {
	// OpenAI chat completions。
	chatReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if family := GatewayRequestEndpointFamily(chatReq); family != gatewayrouting.EndpointFamilyChatCompletions {
		t.Fatalf("chat family = %q", family)
	}
	// OpenAI responses。
	responsesReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if family := GatewayRequestEndpointFamily(responsesReq); family != gatewayrouting.EndpointFamilyResponses {
		t.Fatalf("responses family = %q", family)
	}
	// Anthropic messages（POST，/v1 前缀剥离）。
	messagesReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if family := GatewayRequestEndpointFamily(messagesReq); family != gatewayrouting.EndpointFamilyMessages {
		t.Fatalf("messages family = %q", family)
	}
	// GET 不属于 anthropic/gemini 族。
	getReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodGet, "/v1/messages", nil))
	if family := GatewayRequestEndpointFamily(getReq); family != "" {
		t.Fatalf("GET family = %q", family)
	}
	// 未知路径无族。
	unknownReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/other", nil))
	if family := GatewayRequestEndpointFamily(unknownReq); family != "" {
		t.Fatalf("unknown family = %q", family)
	}
	// 带 query 的路径仍可识别。
	queryReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions?beta=true", nil))
	if family := GatewayRequestEndpointFamily(queryReq); family != gatewayrouting.EndpointFamilyChatCompletions {
		t.Fatalf("query family = %q", family)
	}
}

func TestStripV1Prefix(t *testing.T) {
	cases := map[string]string{
		"/v1/messages": "/messages",
		"/v1":          "/",
		"/v1x":         "/v1x",
		"/messages":    "/messages",
		"":             "/",
	}
	for path, want := range cases {
		if got := stripV1Prefix(path); got != want {
			t.Fatalf("stripV1Prefix(%q) = %q, 期望 %q", path, got, want)
		}
	}
}

func TestRequestCapabilityMismatchMessage(t *testing.T) {
	anthropicMessage := "当前 API Key 绑定的是 Anthropic 原生分组，不兼容 Codex / OpenAI 请求路径；请改用 Anthropic /v1/messages 客户端，或绑定支持 OpenAI Responses / Chat Completions 的分组"
	if got := requestCapabilityMismatchMessage("anthropic_native_group_openai_compatible_request"); got != anthropicMessage {
		t.Fatalf("anthropic 原生分组 message = %q", got)
	}
	if got := requestCapabilityMismatchMessage("other"); got != "当前分组无账户支持请求路径或客户端协议" {
		t.Fatalf("default message = %q", got)
	}
}

func TestShouldReloadModelAwareCandidates(t *testing.T) {
	loader := func(context.Context, string, string) ([]AccountCandidate, error) { return nil, nil }
	if shouldReloadModelAwareCandidates("", ModelFilterResult{}, loader) {
		t.Fatal("无模型不重载")
	}
	if shouldReloadModelAwareCandidates("m", ModelFilterResult{DirectMatchedCount: 1}, nil) {
		t.Fatal("有直连命中不重载")
	}
	if !shouldReloadModelAwareCandidates("m", ModelFilterResult{}, loader) {
		t.Fatal("无命中且有 loader 应重载")
	}
}

// ---------------------------------------------------------------------------
// circuitfacade.go 压缩触发识别
// ---------------------------------------------------------------------------

func TestCodexCompactionExpectedForRequest(t *testing.T) {
	if CodexCompactionExpectedForRequest(nil) {
		t.Fatal("nil 请求不触发")
	}
	getReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodGet, "/v1/responses/compact", nil))
	if CodexCompactionExpectedForRequest(getReq) {
		t.Fatal("GET 不触发")
	}
	compactReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil))
	if !CodexCompactionExpectedForRequest(compactReq) {
		t.Fatal("POST /responses/compact 必须触发")
	}
	// /responses 路径靠 body 中的 compaction_trigger 识别。
	responsesTrigger := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	responsesTrigger.Body = newTestRequestBody(t, `{"model":"gpt-test","input":[{"type":"compaction_trigger"}]}`)
	if !CodexCompactionExpectedForRequest(responsesTrigger) {
		t.Fatal("body 含 compaction_trigger 必须命中")
	}
	responsesPlain := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	responsesPlain.Body = newTestRequestBody(t, `{"model":"gpt-test"}`)
	if CodexCompactionExpectedForRequest(responsesPlain) {
		t.Fatal("普通 body 不应命中")
	}
}

func TestRequestBodyHasCompactionTriggerEdgeScan(t *testing.T) {
	// >64KiB 的 body 只扫描头尾窗口：触发词放在头部窗口内命中。
	pad := make([]byte, 70*1024)
	head := `{"input":[{"type":"compaction_trigger"}],"pad":"` + string(pad) + `"}`
	large := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	large.Body = newTestRequestBody(t, head)
	if !requestBodyHasCompactionTrigger(large) {
		t.Fatal("头部窗口内触发词必须命中")
	}
	// 触发词只在中段（头尾窗口外）时不命中。
	middle := `{"a":"` + string(pad) + `","type":"compaction_trigger","b":"` + string(pad) + `"}`
	midReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	midReq.Body = newTestRequestBody(t, middle)
	if requestBodyHasCompactionTrigger(midReq) {
		t.Fatal("窗口外触发词不应命中")
	}
	// 空 body / nil body 不命中。
	if requestBodyHasCompactionTrigger(&gatewaypreauth.GatewayRequest{}) {
		t.Fatal("nil body 不命中")
	}
}

// newTestRequestBody 构造带解析对象与状态的最小请求 body。
func newTestRequestBody(t *testing.T, body string) *gatewaybody.Request {
	t.Helper()
	return &gatewaybody.Request{
		RawBody: []byte(body),
		Body:    mustJSONObject(t, body),
		State:   &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusParsed},
	}
}

// ---------------------------------------------------------------------------
// Engine / AuditCapture 面
// ---------------------------------------------------------------------------

func TestCandidatePipelineOf(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	if engine.CandidatePipelineOf() == nil {
		t.Fatal("CandidatePipelineOf 应返回管道")
	}
}

// plainAuditCaptureContext 只实现 gatewaypreauth.AuditCaptureContext（不实现
// gatewaydispatch 的 AttemptAuditSink），用于验证工厂回退分支。
type plainAuditCaptureContext struct{}

func (plainAuditCaptureContext) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (plainAuditCaptureContext) AddGatewayMetadata(string, map[string]any)      {}
func (plainAuditCaptureContext) Finalize(gatewaypreauth.AuditFinalizeInput)     {}

func TestAuditCaptureOfFactoryFallback(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	sink := &fakeAuditSink{}
	// 仅实现 preauth 捕获面且未设置工厂时无 sink。
	capture := engine.auditCaptureOf(plainAuditCaptureContext{})
	if capture.Sink != nil {
		t.Fatal("未实现 attempt sink 且无工厂时 sink 为空")
	}
	engine.AttemptAuditSinkFactory = func() AttemptAuditSink { return sink }
	withFactory := engine.auditCaptureOf(plainAuditCaptureContext{})
	if withFactory.Sink == nil {
		t.Fatal("未实现 attempt sink 的 context 走工厂")
	}
}

func TestAuditCaptureSurface(t *testing.T) {
	if !(AuditCapture{}).Nil() {
		t.Fatal("全空 capture 视为 nil")
	}
	sink := &fakeAuditSink{}
	capture := AuditCapture{Context: &frozenAudit{sink: sink}, Sink: sink}
	if capture.Nil() {
		t.Fatal("带 sink 的 capture 不为 nil")
	}
	capture.BindContext(gatewaypreauth.AuditGatewayContext{})
	capture.AddGatewayMetadata("label", map[string]any{"k": "v"})
	if got := capture.StartAttempt(StartAttemptInput{}); got == "" {
		t.Fatal("StartAttempt 应返回 attempt id")
	}
	capture.CompleteAttempt("attempt-1", CompleteAttemptInput{})
	capture.RecordFailedDispatchAttempt(FailedDispatchAttemptInput{})
	if sink.started != 1 || sink.completed != 1 || sink.failed != 1 {
		t.Fatalf("sink 调用 = started:%d completed:%d failed:%d", sink.started, sink.completed, sink.failed)
	}
	capture.Finalize(gatewaypreauth.AuditFinalizeInput{})
}

func TestAccountLockBlocksCrossAccount(t *testing.T) {
	if accountLockBlocksCrossAccount(AccountLockStateView{}) {
		t.Fatal("空状态不阻断跨账户")
	}
	if !accountLockBlocksCrossAccount(AccountLockStateView{BlocksCrossAccount: true}) {
		t.Fatal("BlocksCrossAccount 状态阻断跨账户")
	}
}

func TestKeyModelCapabilityMergePermitLostSignal(t *testing.T) {
	lost := make(chan struct{})
	merged := MergePermitLostSignal(context.Background(), lost)
	select {
	case <-merged.Done():
		t.Fatal("未触发时不应取消")
	default:
	}
	close(lost)
	select {
	case <-merged.Done():
	case <-time.After(time.Second):
		t.Fatal("permit 丢失后合并信号必须取消")
	}
}
