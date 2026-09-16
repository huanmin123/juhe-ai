package gatewayrouting

// w9e 覆盖率战役：补 gatewayrouting 的预算/协调预算/尝试追踪器/Redis 计数器/
// 模型过滤优先级/请求视图解析的未覆盖分支。Redis 走 miniredis，不触真实资源。

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func w9eInt64(v int64) *int64 { return &v }

type w9eObserver struct{ events []string }

func (o *w9eObserver) ObserveRouting(kind, outcome string, nowMs int64) {
	o.events = append(o.events, kind+"/"+outcome)
}

func TestW9EGatewayRequestWallBudgetBranches(t *testing.T) {
	now := int64(10_000)
	budget, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: now, BudgetMs: w9eInt64(5_000), Now: func() int64 { return now },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if budget.Now() != now {
		t.Fatalf("Now() = %d", budget.Now())
	}
	if budget.ElapsedMs(now+100) != 100 || budget.ElapsedMs(now-1) != 0 {
		t.Fatal("ElapsedMs 契约不符")
	}
	if got := budget.RemainingMs(now + 1_000); got != 4_000 {
		t.Fatalf("RemainingMs = %d", got)
	}
	if got := budget.RemainingMs(now + 9_000); got != 0 {
		t.Fatalf("耗尽 RemainingMs = %d", got)
	}
	// WithMinimumBudgetMs 分支。
	if _, err := budget.WithMinimumBudgetMs(0); err == nil {
		t.Fatal("min<=0 必须失败")
	}
	same, err := budget.WithMinimumBudgetMs(1_000)
	if err != nil || same != budget {
		t.Fatal("min<=budget 应原样返回")
	}
	raised, err := budget.WithMinimumBudgetMs(6_000)
	if err != nil || raised.BudgetMs != 6_000 {
		t.Fatalf("min>budget 应换新预算, got %d", raised.BudgetMs)
	}
	// WithoutLimit。
	unbounded := budget.WithoutLimit()
	if !unbounded.Unbounded || unbounded.RemainingMs(now) <= 0 {
		t.Fatal("WithoutLimit 应无界")
	}
	if again := unbounded.WithoutLimit(); again != unbounded {
		t.Fatal("无界 WithoutLimit 应原样返回")
	}
	// AvailableDecisionMs。
	// reserve 缺省按 Node 默认 2000ms。
	if got, err := budget.AvailableDecisionMs(GatewayRequestWallBudgetDecision{}); err != nil || got != 3_000 {
		t.Fatalf("AvailableDecisionMs = %d err=%v", got, err)
	}
	if got, err := budget.AvailableDecisionMs(GatewayRequestWallBudgetDecision{FinalResponseReserveMs: w9eInt64(-1)}); err == nil {
		t.Fatalf("负 reserve 应失败, got %d", got)
	}
	if got, err := budget.AvailableDecisionMs(GatewayRequestWallBudgetDecision{FinalResponseReserveMs: w9eInt64(2_000), NowMs: w9eInt64(now + 4_000)}); err != nil || got != 0 {
		t.Fatalf("耗尽后 reserve 超剩余应 0, got %d err=%v", got, err)
	}
	// HandoffRequired。
	required, err := budget.HandoffRequired(GatewayRequestWallBudgetDecision{MinimumMeaningfulAttemptMs: w9eInt64(6_000)})
	if err != nil || !required {
		t.Fatalf("剩余不足应要求 handoff, got %v err=%v", required, err)
	}
	if _, err := budget.HandoffRequired(GatewayRequestWallBudgetDecision{MinimumMeaningfulAttemptMs: w9eInt64(-1)}); err == nil {
		t.Fatal("负 minimum 必须失败")
	}
	// PrecommitRemainingMs。
	// reserve 缺省 2000：15000 - 10000 - 2000。
	precommit, err := budget.PrecommitRemainingMs(PrecommitBudgetInput{NowMs: w9eInt64(now)})
	if err != nil || precommit != 3_000 {
		t.Fatalf("PrecommitRemainingMs = %d err=%v", precommit, err)
	}
	if got, err := budget.PrecommitRemainingMs(PrecommitBudgetInput{NowMs: w9eInt64(now), RequestPrecommitDeadlineAtMs: w9eInt64(now + 1_000), FinalResponseReserveMs: w9eInt64(0)}); err != nil || got != 1_000 {
		t.Fatalf("提前 precommit deadline = %d err=%v", got, err)
	}
	// ClipFirstByteDeadlineMs。
	clip, err := budget.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{FirstByteDeadlineMs: 3_000, NowMs: w9eInt64(now)})
	if err != nil || clip != 3_000 {
		t.Fatalf("未超限 clip = %d err=%v", clip, err)
	}
	if _, err := budget.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{FirstByteDeadlineMs: -1}); err == nil {
		t.Fatal("负配置 deadline 必须失败")
	}
	if _, err := budget.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{FirstByteDeadlineMs: 1_000, FinalResponseReserveMs: w9eInt64(-2)}); err == nil {
		t.Fatal("负 reserve 必须失败")
	}
	// 观察者：precommit 裁剪时上报。
	observer := &w9eObserver{}
	observed, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: now, BudgetMs: w9eInt64(500), Now: func() int64 { return now },
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省 reserve 2000 使 precommit 剩余为 0：裁剪到 0 且上报观察事件。
	clipped, err := observed.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{FirstByteDeadlineMs: 10_000, NowMs: w9eInt64(now), FinalResponseReserveMs: w9eInt64(0)})
	if err != nil || clipped != 500 || len(observer.events) != 1 {
		t.Fatalf("precommit 裁剪 clip=%d events=%v err=%v", clipped, observer.events, err)
	}
	zero, err := observed.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{FirstByteDeadlineMs: 10_000, NowMs: w9eInt64(now)})
	if err != nil || zero != 0 {
		t.Fatalf("缺省 reserve 裁剪 = %d err=%v", zero, err)
	}
	// 无界预算各分支。
	unb, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{Unbounded: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := unb.AvailableDecisionMs(GatewayRequestWallBudgetDecision{}); got <= 0 {
		t.Fatal("无界应返回 MaxInt64")
	}
	if req, _ := unb.HandoffRequired(GatewayRequestWallBudgetDecision{MinimumMeaningfulAttemptMs: w9eInt64(1 << 40)}); req {
		t.Fatal("无界永不 handoff")
	}
	if got, _ := unb.PrecommitRemainingMs(PrecommitBudgetInput{}); got <= 0 {
		t.Fatal("无界 precommit 应 MaxInt64")
	}
	if got, err := unb.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{FirstByteDeadlineMs: 7_000}); err != nil || got != 7_000 {
		t.Fatalf("无界 clip = %d err=%v", got, err)
	}
	// 构造错误：非法预算值。
	if _, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{BudgetMs: w9eInt64(0)}, nil); err == nil {
		t.Fatal("budget=0 必须失败")
	}
}

func TestW9ERouteCoordinationBudget(t *testing.T) {
	now := int64(1_000)
	clock := func() int64 { return now }
	if _, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{RequestID: " "}); err == nil {
		t.Fatal("空 requestID 必须失败")
	}
	if _, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{RequestID: "req", BudgetMs: w9eInt64(-5)}); err == nil {
		t.Fatal("非法 budget 必须失败")
	}
	b, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{
		RequestID: "req", BudgetID: "custom", BudgetMs: w9eInt64(100), Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.BudgetID != "custom" {
		t.Fatalf("BudgetID = %q", b.BudgetID)
	}
	// 初始快照。
	snap := b.Snapshot(now)
	if snap.Version != 0 || snap.RemainingMs != 100 {
		t.Fatalf("初始快照 = %+v", snap)
	}
	// BeginWait：版本冲突 / 非法版本 / 应用。
	if res, err := b.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 5}); err != nil || res.Outcome != BudgetTransitionVersionConflict {
		t.Fatalf("版本冲突 = %+v err=%v", res, err)
	}
	if _, err := b.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: -1}); err == nil {
		t.Fatal("负版本必须失败")
	}
	applied, err := b.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 0, NowMs: w9eInt64(now + 10)})
	if err != nil || applied.Outcome != BudgetTransitionApplied || applied.Snapshot.Version != 1 || applied.Snapshot.ActiveSinceMs == nil {
		t.Fatalf("BeginWait = %+v err=%v", applied, err)
	}
	// 活跃期间再次 BeginWait → invalid（新 token）。
	invalid, err := b.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w2", ExpectedVersion: 1, NowMs: w9eInt64(now + 20)})
	if err != nil || invalid.Outcome != BudgetTransitionInvalid {
		t.Fatalf("活跃期间 BeginWait = %+v err=%v", invalid, err)
	}
	// 同 token 重放 → idempotent。
	replay, err := b.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 1, NowMs: w9eInt64(now + 20)})
	if err != nil || replay.Outcome != BudgetTransitionIdempotentReplay {
		t.Fatalf("重放 = %+v err=%v", replay, err)
	}
	if b.Exhausted(now + 20) {
		t.Fatal("剩余 90ms 不应耗尽")
	}
	// PauseWait：token 不符 → invalid；正常暂停后重放 → idempotent。
	if res, err := b.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w9", ExpectedVersion: 1}); err != nil || res.Outcome != BudgetTransitionInvalid {
		t.Fatalf("错误 token 暂停 = %+v err=%v", res, err)
	}
	paused, err := b.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 1, NowMs: w9eInt64(now + 30)})
	if err != nil || paused.Outcome != BudgetTransitionApplied || paused.Snapshot.RemainingMs != 80 {
		t.Fatalf("PauseWait = %+v err=%v", paused, err)
	}
	if res, err := b.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 2}); err != nil || res.Outcome != BudgetTransitionIdempotentReplay {
		t.Fatalf("暂停重放 = %+v err=%v", res, err)
	}
	// 暂停期间剩余不变；再等待重新激活。
	if got := b.RemainingMs(now + 100); got != 80 {
		t.Fatalf("暂停期间剩余 = %d", got)
	}
	if _, err := b.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w3", ExpectedVersion: 2, NowMs: w9eInt64(now + 200)}); err != nil {
		t.Fatal(err)
	}
	if !b.Exhausted(now + 300) {
		t.Fatal("等待 100ms 后应耗尽")
	}
}

func w9eBinding(id, groupID string, priority int64, weight *int64) GroupBindingRow {
	return GroupBindingRow{
		ID: id, APIKeyID: "key1", GroupID: groupID,
		Priority: priority, Weight: weight, Status: RowStatusActive,
		ProviderCode: "openai", GroupEnabled: 1,
	}
}

func TestW9EAttemptTrackerFlows(t *testing.T) {
	// 初始快照含非法值 → 错误。
	if _, err := NewGatewayRequestAttemptTracker(&GatewayRequestAttemptSnapshot{AttemptedAccountRuntimeKeys: []string{" "}}); err == nil {
		t.Fatal("非法初始 key 必须失败")
	}
	tracker, err := NewGatewayRequestAttemptTracker(&GatewayRequestAttemptSnapshot{
		AttemptedAccountRuntimeKeys:     []string{"acct-a"},
		AttemptedPhysicalCredentialKeys: []string{"cred-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := tracker.HasAccountRuntimeKey("acct-a"); !ok {
		t.Fatal("初始 runtime key 未登记")
	}
	if ok, _ := tracker.HasPhysicalCredentialKey("cred-1"); !ok {
		t.Fatal("初始 credential key 未登记")
	}
	if ok, _ := tracker.HasKeyFingerprint("fp-x"); ok {
		t.Fatal("未登记 fingerprint 不应存在")
	}
	// CanAttemptAccount：物理凭据已被其它 runtime 使用。
	decision, err := tracker.CanAttemptAccount(CanAttemptAccountInput{AccountRuntimeKey: "acct-b", PhysicalCredentialKey: "cred-1"})
	if err != nil || decision.Allowed || decision.Reason != RejectPhysicalCredentialAlreadyTried {
		t.Fatalf("跨 runtime 复用凭据 = %+v err=%v", decision, err)
	}
	// 直接重复账号。
	decision, err = tracker.CanAttemptAccount(CanAttemptAccountInput{AccountRuntimeKey: "acct-a", PhysicalCredentialKey: "cred-2"})
	if err != nil || decision.Allowed || decision.Reason != RejectAccountRuntimeAlreadyAttempted {
		t.Fatalf("重复账号 = %+v err=%v", decision, err)
	}
	// 新组合允许。
	decision, err = tracker.CanAttemptAccount(CanAttemptAccountInput{AccountRuntimeKey: "acct-b", PhysicalCredentialKey: "cred-2"})
	if err != nil || !decision.Allowed {
		t.Fatalf("新组合应允许 = %+v err=%v", decision, err)
	}
	// 空键校验。
	if _, err := tracker.CanAttemptAccount(CanAttemptAccountInput{AccountRuntimeKey: " ", PhysicalCredentialKey: "c"}); err == nil {
		t.Fatal("空 runtime key 必须失败")
	}
	// TryRecordDispatchAttempt 常规流：协议模型与 fingerprint 去重。
	identity := GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-1", AccountRuntimeKey: "acct-c", PhysicalCredentialKey: "cred-3", KeyFingerprint: "fp-1"}
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: identity}); err != nil || !res.Allowed {
		t.Fatalf("首次记录 = %+v err=%v", res, err)
	}
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{
		ProtocolModelKey: "pm-1", AccountRuntimeKey: "acct-fresh", PhysicalCredentialKey: "cred-fresh",
	}}); err != nil || res.Allowed || res.Reason != RejectProtocolModelAlreadyAttempted {
		t.Fatalf("协议模型重复 = %+v err=%v", res, err)
	}
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{
		ProtocolModelKey: "pm-2", AccountRuntimeKey: "acct-d", PhysicalCredentialKey: "cred-4", KeyFingerprint: "fp-1",
	}}); err != nil || res.Allowed || res.Reason != RejectKeyFingerprintAlreadyAttempted {
		t.Fatalf("fingerprint 重复 = %+v err=%v", res, err)
	}
	// confirmation 冲突 + key rotation。
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-c", AccountRuntimeKey: "acct-e", PhysicalCredentialKey: "cred-5"},
		MatchingConfirmation:           true,
	}); err != nil || !res.Allowed {
		t.Fatalf("首次 confirmation = %+v err=%v", res, err)
	}
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-c", AccountRuntimeKey: "acct-e", PhysicalCredentialKey: "cred-6"},
		MatchingConfirmation:           true,
	}); err != nil || res.Allowed || res.Reason != RejectPhysicalCredentialAlreadyTried {
		t.Fatalf("confirmation 换凭据 = %+v err=%v", res, err)
	}
	// rotation：条件不足 → not applicable。
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-r", AccountRuntimeKey: "acct-z", PhysicalCredentialKey: "cred-z"},
		AllowKeyRotation:               true,
	}); err != nil || res.Allowed || res.Reason != RejectKeyRotationNotApplicable {
		t.Fatalf("rotation 条件不足 = %+v err=%v", res, err)
	}
	// rotation：已尝试的账号+凭据+新 fingerprint → 允许轮换。
	rotated, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-1", AccountRuntimeKey: "acct-c", PhysicalCredentialKey: "cred-3", KeyFingerprint: "fp-new"},
		AllowKeyRotation:               true,
	})
	if err != nil || !rotated.Allowed {
		t.Fatalf("rotation 应允许 = %+v err=%v", rotated, err)
	}
	// semantic retry。
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-s", AccountRuntimeKey: "acct-f", PhysicalCredentialKey: "cred-7"},
		SemanticRetryID:                "sem-1",
	}); err != nil || !res.Allowed {
		t.Fatalf("semantic 首次 = %+v err=%v", res, err)
	}
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-s", AccountRuntimeKey: "acct-f", PhysicalCredentialKey: "cred-7"},
		SemanticRetryID:                "sem-1",
	}); err != nil || res.Allowed || res.Reason != RejectSemanticRetryAlreadyAttempted {
		t.Fatalf("semantic 重复 = %+v err=%v", res, err)
	}
	// same-account retry 保留与消费。
	reservation, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
		// rotation 后登记的身份带 fp-new，保留必须与之全等。
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-1", AccountRuntimeKey: "acct-c", PhysicalCredentialKey: "cred-3", KeyFingerprint: "fp-new"},
		MaxRetries:                     2,
	})
	if err != nil || !reservation.Reserved || reservation.Remaining != 1 {
		t.Fatalf("保留 = %+v err=%v", reservation, err)
	}
	if _, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{MaxRetries: 11}); err == nil {
		t.Fatal("max retries 超界必须失败")
	}
	if res, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-x", AccountRuntimeKey: "ghost", PhysicalCredentialKey: "ghost-cred"},
		MaxRetries:                     1,
	}); err != nil || res.Reserved || res.Reason != SameAccountRetryNotApplicable {
		t.Fatalf("未注册身份 = %+v err=%v", res, err)
	}
	// 模式冲突。
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity, SameAccountRetryID: reservation.RetryID, MatchingConfirmation: true,
	}); err != nil || res.Allowed || res.Reason != RejectSameAccountRetryModeConflict {
		t.Fatalf("模式冲突 = %+v err=%v", res, err)
	}
	// 未注册 retry id。
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity, SameAccountRetryID: "nope",
	}); err != nil || res.Allowed || res.Reason != RejectSameAccountRetryNotRegistered {
		t.Fatalf("未注册 retry = %+v err=%v", res, err)
	}
	// 身份不符。
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-1", AccountRuntimeKey: "acct-other", PhysicalCredentialKey: "cred-3"},
		SameAccountRetryID:             reservation.RetryID,
	}); err != nil || res.Allowed || res.Reason != RejectSameAccountRetryIdentityMismatch {
		t.Fatalf("身份不符 = %+v err=%v", res, err)
	}
	// 正常消费 + 二次消费拒绝（消费身份必须与保留身份全等，含 fp-new）。
	retryIdentity := GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm-1", AccountRuntimeKey: "acct-c", PhysicalCredentialKey: "cred-3", KeyFingerprint: "fp-new"}
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: retryIdentity, SameAccountRetryID: reservation.RetryID,
	}); err != nil || !res.Allowed {
		t.Fatalf("消费保留 = %+v err=%v", res, err)
	}
	if res, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: retryIdentity, SameAccountRetryID: reservation.RetryID,
	}); err != nil || res.Allowed || res.Reason != RejectSameAccountRetryAlreadyAttempted {
		t.Fatalf("重复消费 = %+v err=%v", res, err)
	}
	// 快照包含已尝试集合。
	snap := tracker.Snapshot()
	found := false
	for _, k := range snap.AttemptedAccountRuntimeKeys {
		if k == "acct-c" {
			found = true
		}
	}
	if !found {
		t.Fatalf("快照缺少 acct-c: %v", snap.AttemptedAccountRuntimeKeys)
	}
}

func TestW9EGatewayAttemptProtocolModelKey(t *testing.T) {
	key, err := GatewayAttemptProtocolModelKey("acct", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "unknown_protocol") || !strings.Contains(key, "unknown_version") || !strings.Contains(key, "unknown_model") {
		t.Fatalf("默认 key = %q", key)
	}
	if _, err := GatewayAttemptProtocolModelKey(" ", "p", "v", "m"); err == nil {
		t.Fatal("空账号必须失败")
	}
	if _, err := GatewayAttemptProtocolModelKey("acct", "p", "v", "m"); err != nil {
		t.Fatalf("显式 key 失败: %v", err)
	}
}

func TestW9ECreateGatewayRoutePlanSnapshot(t *testing.T) {
	if _, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{}); err == nil {
		t.Fatal("空 route plan id 必须失败")
	}
	if _, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{RoutePlanID: "rp"}); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("空目标必须失败, got %v", err)
	}
	badCursor := 99
	if _, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID: "rp", OrderedAllowedTargets: []string{"a"}, GatewayRequestWallBudgetMs: w9eInt64(1_000), Cursor: &badCursor,
	}); err == nil {
		t.Fatal("游标越界必须失败")
	}
	negativeFirstByte := int64(-3)
	snap, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID: "rp", OrderedAllowedTargets: []string{"a", "b"},
		RequestAcceptedAtMs:          1_000,
		GatewayRequestWallBudgetMs:   w9eInt64(5_000),
		RequestPrecommitDeadlineAtMs: w9eInt64(2_000),
		FinalResponseReserveMs:       w9eInt64(100),
		FirstByteDeadlineMs:          &negativeFirstByte,
		Mode:                         "normal",
	})
	if err == nil {
		t.Fatal("负 first byte 必须失败")
	}
	valid := int64(3_000)
	snap, err = CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID: "rp", OrderedAllowedTargets: []string{"a", "b"},
		RequestAcceptedAtMs:            1_000,
		GatewayRequestWallBudgetMs:     w9eInt64(5_000),
		RequestPrecommitDeadlineAtMs:   w9eInt64(99_000),
		FinalResponseReserveMs:         w9eInt64(100),
		FirstByteDeadlineMs:            &valid,
		UncommittedAttemptDeadlineAtMs: w9eInt64(2_500),
		WeightedDecisionToken:          "tok",
		Mode:                           "normal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.GatewayRequestWallDeadlineAtMs != 6_000 || snap.RequestPrecommitDeadlineAtMs != 6_000 {
		t.Fatalf("快照 deadline = %+v", snap)
	}
	if snap.FirstByteDeadlineMs == nil || *snap.FirstByteDeadlineMs != 3_000 {
		t.Fatalf("FirstByteDeadlineMs = %v", snap.FirstByteDeadlineMs)
	}
}

func TestW9ERedisRouteStateCounterWithMiniredis(t *testing.T) {
	mr := miniredis.RunT(t)
	counter := NewRedisRouteStateCounter("redis://" + mr.Addr() + "/0")
	ctx := context.Background()
	first, err := counter.NextRouteCounterIndex(ctx, "k", 3)
	if err != nil || first != 0 {
		t.Fatalf("first = %d err=%v", first, err)
	}
	second, err := counter.NextRouteCounterIndex(ctx, "k", 3)
	if err != nil || second != 1 {
		t.Fatalf("second = %d err=%v", second, err)
	}
	// 非法 URL。
	bad := NewRedisRouteStateCounter("://bad-url")
	if _, err := bad.NextRouteCounterIndex(ctx, "k", 3); err == nil {
		t.Fatal("非法 URL 必须失败")
	}
	// selector 侧：modulo<=0 短路、缺 URL 报错、计数器错误透传。
	selector := NewAPIKeyGroupRouteSelector("redis", counter, "redis://"+mr.Addr()+"/0")
	if got, err := selector.nextRedisRouteCounterIndex(ctx, "k", 0); err != nil || got != 0 {
		t.Fatalf("modulo<=0 = %d err=%v", got, err)
	}
	noURL := NewAPIKeyGroupRouteSelector("redis", nil, "")
	if _, err := noURL.nextRedisRouteCounterIndex(ctx, "k", 3); err == nil || !strings.Contains(err.Error(), "STATE_URL") {
		t.Fatalf("缺 state URL = %v", err)
	}
	failing := NewAPIKeyGroupRouteSelector("redis", nil, "://bad")
	if _, err := failing.nextRedisRouteCounterIndex(ctx, "k", 3); err == nil {
		t.Fatal("计数器错误必须透传")
	}
	// redis 驱动下的动态排序端到端。
	apiKey := &APIKeyRow{
		ID: "k1", RouteStrategyID: "s1", RouteStrategyMode: RouteStrategyModeRoundRobin,
		GroupBindings: []GroupBindingRow{
			w9eBinding("b1", "g1", 1, w9eInt64(1)),
			w9eBinding("b2", "g2", 2, w9eInt64(1)),
		},
	}
	ordered, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil || len(ordered) != 2 {
		t.Fatalf("async round robin = %v err=%v", ordered, err)
	}
	apiKey.RouteStrategyMode = RouteStrategyModeWeighted
	weighted, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil || len(weighted) != 2 {
		t.Fatalf("async weighted = %v err=%v", weighted, err)
	}
	// 单绑定直接返回。
	single := &APIKeyRow{ID: "k2", GroupBindings: []GroupBindingRow{w9eBinding("only", "g1", 1, nil)}}
	if got, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, single); err != nil || len(got) != 1 {
		t.Fatalf("single = %v err=%v", got, err)
	}
}

func TestW9EModelPriorityHelpers(t *testing.T) {
	left := UpstreamAccount{ID: "a"}
	right := UpstreamAccount{ID: "b"}
	if got := GatewayAccountModelPriorityFor(left, nil); got != gatewayAccountModelPriorityRankDirect {
		t.Fatalf("nil priority rank = %d", got)
	}
	priority := &GatewayAccountModelPriority{RankByAccountID: map[string]int{"a": 1}}
	if got := GatewayAccountModelPriorityFor(left, priority); got != 1 {
		t.Fatalf("ranked = %d", got)
	}
	if got := GatewayAccountModelPriorityFor(right, priority); got != gatewayAccountModelPriorityRankUnsupported {
		t.Fatalf("unranked = %d", got)
	}
	if CompareGatewayAccountModelPriority(left, right, priority) >= 0 {
		t.Fatal("a 应排在 b 前")
	}
	if !isMappingAllowedBySupportedModels("m", []string{"m", "n"}) || isMappingAllowedBySupportedModels("x", []string{"m"}) || isMappingAllowedBySupportedModels("m", nil) {
		t.Fatal("isMappingAllowedBySupportedModels 契约不符")
	}
	if resolveGatewayAccountModelMatch("m", []string{"m"}) != true || resolveGatewayAccountModelMatch("", []string{"m"}) || resolveGatewayAccountModelMatch("z", []string{"m"}) {
		t.Fatal("resolveGatewayAccountModelMatch 契约不符")
	}
	var nilAccount *UpstreamAccount
	if nilAccount.runtimeAccount() != nil {
		t.Fatal("nil 账户投影应为 nil")
	}
	enabled := true
	withMapping := &UpstreamAccount{ID: "a", ProviderCode: "openai", ModelMappings: []gatewayAccountModelMapping{{SourceModel: "s", UpstreamModel: "u", Enabled: &enabled}}}
	projected := withMapping.runtimeAccount()
	if projected == nil || len(projected.ModelMappings) != 1 || projected.ProviderCode != "openai" {
		t.Fatalf("投影 = %+v", projected)
	}
}

func TestW9ERequestViewParsers(t *testing.T) {
	v := RequestView{OriginalURL: "/v1/chat/completions?x=1"}
	if got := v.requestEndpointPath(); got != "/v1/chat/completions" {
		t.Fatalf("endpoint path = %q", got)
	}
	if got := (RequestView{Path: "/p"}).requestEndpointPath(); got != "/p" {
		t.Fatalf("path fallback = %q", got)
	}
	if got := requestModelFromGeminiPath("/v1beta/models/gem-2.5:generateContent", ""); got != "gem-2.5" {
		t.Fatalf("gemini model = %q", got)
	}
	if got := requestModelFromGeminiPath("", "/models/a%20b:countTokens"); got != "a b" {
		t.Fatalf("unescape model = %q", got)
	}
	if got := requestModelFromGeminiPath("/no-match", ""); got != "" {
		t.Fatalf("no match = %q", got)
	}
	if got := anthropicMessagesRequestEndpointFamily("GET", "/messages"); got != "" {
		t.Fatalf("GET messages = %q", got)
	}
	if got := anthropicMessagesRequestEndpointFamily("POST", "messages"); got != EndpointFamilyMessages {
		t.Fatalf("POST messages = %q", got)
	}
	if got := normalizedGeminiPath("v1beta/models?key=1"); got != "/models" {
		t.Fatalf("normalized gemini = %q", got)
	}
	if got := normalizedGeminiPath("/v1beta"); got != "/" {
		t.Fatalf("empty after strip = %q", got)
	}
}

func TestW9EBindingsAndRotationHelpers(t *testing.T) {
	a := w9eBinding("a", "g1", 1, nil)
	b := w9eBinding("b", "g2", 2, nil)
	if compareBindingOrderByPriority(a, b) >= 0 {
		t.Fatal("低优先级应排前")
	}
	// rotateBindings 边界。
	bindings := []GroupBindingRow{a, b, w9eBinding("c", "g3", 3, nil)}
	if got := rotateBindings(bindings, -5); got[0].ID != "a" {
		t.Fatalf("负起点应钳 0, got %s", got[0].ID)
	}
	if got := rotateBindings(bindings, 99); got[0].ID != "c" {
		t.Fatalf("超界起点应钳末尾, got %s", got[0].ID)
	}
	// registry 溢出裁剪 trimLocked。
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	apiKey := &APIKeyRow{ID: "overflow", RouteStrategyMode: RouteStrategyModeRoundRobin, GroupBindings: []GroupBindingRow{a, b}}
	for i := 0; i < apiKeyGroupRouteStateMaxEntries+5; i++ {
		key := &APIKeyRow{ID: fmt.Sprintf("overflow-%d", i), RouteStrategyMode: RouteStrategyModeRoundRobin, GroupBindings: apiKey.GroupBindings}
		if _, err := selector.OrderAPIKeyGroupBindingsForDispatch(key); err != nil {
			t.Fatalf("rotation 迭代 %d 失败: %v", i, err)
		}
	}
	selector.states.mu.Lock()
	roundRobinSize := len(selector.states.roundRobin)
	weightedSize := len(selector.states.weighted)
	selector.states.mu.Unlock()
	if roundRobinSize > apiKeyGroupRouteStateMaxEntries {
		t.Fatalf("round robin 状态泄漏: %d", roundRobinSize)
	}
	if weightedSize > apiKeyGroupRouteStateMaxEntries {
		t.Fatalf("weighted 状态泄漏: %d", weightedSize)
	}
}

func TestW9ECleanupWeightedState(t *testing.T) {
	state := map[string]int64{"gone": 1, "kept": 2}
	cleanupWeightedState(state, []GroupBindingRow{w9eBinding("kept", "g", 1, nil)})
	if _, ok := state["gone"]; ok {
		t.Fatal("失效绑定权重应清理")
	}
	if state["kept"] != 2 {
		t.Fatal("活跃绑定权重应保留")
	}
}

func TestW9EFirstByteDeadlineErrorPropagation(t *testing.T) {
	budget, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: 1_000, BudgetMs: w9eInt64(1_000), Now: func() int64 { return 1_000 },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 用非法 reserve 触发 ClipFirstByteDeadlineMs 的错误臂。
	_, err = ResolveNormalRouteAttemptFirstByteDeadline(NormalRouteAttemptFirstByteDeadlineInput{
		GatewayRequestWallBudget: budget,
		AttemptStartedAtMs:       1_000,
		FinalResponseReserveMs:   w9eInt64(-9),
	})
	if err == nil {
		t.Fatal("应透传 ClipFirstByteDeadlineMs 错误")
	}
	// 正常解析：lane timeout 生效。
	resolved, err := ResolveNormalRouteAttemptFirstByteDeadline(NormalRouteAttemptFirstByteDeadlineInput{
		Config:                          NormalRouteFirstByteRuntimeConfig{FirstByteDeadlineMs: 30_000},
		GatewayRequestWallBudget:        budget,
		AttemptStartedAtMs:              1_000,
		LaneFirstByteTimeoutMs:          300,
		UncommittedAttemptMaxLifetimeMs: 60_000,
		FinalResponseReserveMs:          w9eInt64(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.EffectiveDeadlineMs != 300 || resolved.LimitingFactor != FirstByteLimitingFactorLaneTimeout {
		t.Fatalf("resolved = %+v", resolved)
	}
}
