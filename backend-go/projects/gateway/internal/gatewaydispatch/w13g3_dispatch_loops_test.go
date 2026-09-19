package gatewaydispatch

// w13g3：主循环补测——FetchFirstAvailableUpstream 的账户锁租约同账户重试
// （reserveSameAccountRetry / waitForAccountLockDelay）、容量排队、可恢复等待、
// 协调协处理器（first-byte coordinator / 模型 slot / hot-quality lifecycle）。
// 全部经 fake 端口驱动，无真实 PG/Redis；所有等待均有界（<=3s）并由信号超时兜底。
//
// 不可达语句归因登记（本文件范围的生产代码，均为防御分支，生产路径不可达）：
//   - upstreamdispatch.go:1178-1180 waitForAccountLockDelay 的
//     AvailableDecisionMs 错误：FinalResponseReserveMs 恒传
//     DefaultGatewayFinalResponseReserveMs（正值常量），仅负值报错。
//   - upstreamdispatch.go:1195-1197 BeginWait 错误及其传播点 650-652 / 710-712：
//     waitToken 版本快照与 BeginWait 之间无并发写者，version_conflict 不可达。
//   - upstreamdispatch.go:721-729 第二等待分支的 wall 结果：进入该分支要求
//     handoff=false（available > WaitMs），而 wall 要求 available < delayMs，矛盾。
//   - upstreamdispatch.go:1386-1388 accountCircuitEvidenceDigest 的 marshal
//     错误：入参均为 string 值 map，json.Marshal 恒成功。
//   - upstreamdispatch.go:974-976 与 dispatchsingle.go:109-111：
//     cycleRecoverableAccountIDs 合并循环体依赖 recoverableFailedAccountIDs，
//     该集合在当前 Go 引擎内没有写入点（Node 迁移保留），循环体不可达。
//   - dispatchsingle.go:507-509 gatewayAccountRuntimeKey 错误：能到达 attempt
//     循环的账户必然已通过 513-533 同键过滤，键错误不可能再现。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

var errW13g3 = errors.New("w13g3 注入错误")

// ---------------------------------------------------------------------------
// 可配置 fake：锁 / 排队 / 抑制 / 降级 / 并发 / 用量
// ---------------------------------------------------------------------------

type w13g3FakeLocks struct {
	fakeLocks
	mu              sync.Mutex
	acquireErr      error
	acquireErrAfter int // 0 = 每次都错；>0 = 第 N 次之后才报错
	acquireSeq      []LockLeaseAcquire
	consumeErr      error
	consumeOK       []bool
	releaseErr      error
	abandonErr      error
	recordErr       error
	state           *AccountLockStateView
	stateErr        error
	releases        []ReleaseRetryLeaseInput
	abandons        []AccountLockRetryLease
	failureCalls    int
	acquireCalls    int
	consumeCalls    int
}

func (f *w13g3FakeLocks) FindStateAsync(ctx context.Context, accountID string) (*AccountLockStateView, error) {
	if f.stateErr != nil {
		return nil, f.stateErr
	}
	return f.state, nil
}

func (f *w13g3FakeLocks) AcquireRetryLeaseAsync(ctx context.Context, accountID string, configuredDelayMs int64) (LockLeaseAcquire, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCalls++
	if f.acquireErr != nil && (f.acquireErrAfter == 0 || f.acquireCalls > f.acquireErrAfter) {
		return LockLeaseAcquire{}, f.acquireErr
	}
	if len(f.acquireSeq) == 0 {
		return LockLeaseAcquire{Allowed: true}, nil
	}
	index := f.acquireCalls - 1
	if index >= len(f.acquireSeq) {
		index = len(f.acquireSeq) - 1
	}
	return f.acquireSeq[index], nil
}

func (f *w13g3FakeLocks) ConsumeRetryLeaseAsync(ctx context.Context, accountID, leaseID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumeCalls++
	if f.consumeErr != nil {
		return false, f.consumeErr
	}
	if len(f.consumeOK) == 0 {
		return true, nil
	}
	index := f.consumeCalls - 1
	if index >= len(f.consumeOK) {
		index = len(f.consumeOK) - 1
	}
	return f.consumeOK[index], nil
}

func (f *w13g3FakeLocks) ReleaseRetryLeaseAsync(ctx context.Context, input ReleaseRetryLeaseInput) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, input)
	if f.releaseErr != nil {
		return false, f.releaseErr
	}
	return true, nil
}

func (f *w13g3FakeLocks) AbandonRetryReservationAsync(ctx context.Context, lease AccountLockRetryLease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abandons = append(f.abandons, lease)
	if f.abandonErr != nil {
		return f.abandonErr
	}
	return nil
}

func (f *w13g3FakeLocks) RecordFailureAsync(ctx context.Context, accountID, reason string, observation *AccountLockObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failureCalls++
	if f.recordErr != nil {
		return f.recordErr
	}
	return nil
}

type w13g3Queue struct {
	fakeQueue
	result    QueueWaitResult
	err       error
	sleepMs   int64
	waitCalls int
}

func (q *w13g3Queue) WaitForCapacity(ctx context.Context, input HighConcurrencyWaitInput) (QueueWaitResult, error) {
	q.waitCalls++
	if q.sleepMs > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(q.sleepMs) * time.Millisecond):
		}
	}
	if q.err != nil {
		return QueueWaitResult{}, q.err
	}
	return q.result, nil
}

// w13g3Phase 是一次过滤的剧本：err 优先，其次 all，其次 ids，否则放行。
type w13g3Phase struct {
	ids           []string
	all           bool
	err           error
	sleep         time.Duration
	precheckIDs   []string // all=true 时附带的 precheck 抑制名单
	configuredIDs []string // all=true 时附带的策略抑制名单
}

// w13g3Suppression 按阶段消费剧本：账户级过滤（AcquireHalfOpenLease=true）
// 与周期后过滤分别计数，末位剧本重复，保证确定性。
type w13g3Suppression struct {
	filterCalls     int
	perAccountPlan  []w13g3Phase
	perAccountCalls int
	postCyclePlan   []w13g3Phase
	postCycleCalls  int
}

func (s *w13g3Suppression) nextPhase(plan []w13g3Phase, calls int) w13g3Phase {
	if len(plan) == 0 {
		return w13g3Phase{}
	}
	if calls >= len(plan) {
		return plan[len(plan)-1]
	}
	return plan[calls]
}

func (s *w13g3Suppression) apply(phase w13g3Phase, accounts []AccountCandidate) (SuppressionFilterResult, error) {
	if phase.sleep > 0 {
		time.Sleep(phase.sleep)
	}
	if phase.err != nil {
		return SuppressionFilterResult{}, phase.err
	}
	if phase.all {
		return SuppressionFilterResult{
			Accounts:                             nil,
			SuppressedCount:                      len(accounts),
			AllSuppressed:                        true,
			SuppressedAccountIDs:                 accountIDs(accounts),
			PrecheckSuppressedAccountIDs:         phase.precheckIDs,
			ConfiguredPolicySuppressedAccountIDs: phase.configuredIDs,
		}, nil
	}
	if len(phase.ids) > 0 {
		return SuppressionFilterResult{
			Accounts:             accounts,
			SuppressedCount:      len(phase.ids),
			SuppressedAccountIDs: phase.ids,
		}, nil
	}
	return SuppressionFilterResult{Accounts: accounts}, nil
}

func (s *w13g3Suppression) FilterAsync(ctx context.Context, accounts []AccountCandidate, options SuppressionFilterOptions) (SuppressionFilterResult, error) {
	s.filterCalls++
	if options.AcquireHalfOpenLease {
		phase := s.nextPhase(s.perAccountPlan, s.perAccountCalls)
		s.perAccountCalls++
		return s.apply(phase, accounts)
	}
	phase := s.nextPhase(s.postCyclePlan, s.postCycleCalls)
	s.postCycleCalls++
	return s.apply(phase, accounts)
}

func (s *w13g3Suppression) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

type w13g3Degradation struct {
	fakeDegradation
	errOnLaneCall int
	laneCalls     int
}

func (d *w13g3Degradation) OrderWithLaneAsync(ctx context.Context, accounts []AccountCandidate, requestLane string, policy *gatewayruntimecache.GroupSchedulingPolicy, priority *gatewayrouting.GatewayAccountModelPriority) (DegradationOrder, error) {
	d.laneCalls++
	if d.errOnLaneCall == d.laneCalls {
		return DegradationOrder{}, errW13g3
	}
	return d.fakeDegradation.OrderWithLaneAsync(ctx, accounts, requestLane, policy, priority)
}

type w13g3Usage struct {
	fakeUsage
	err error
}

func (u *w13g3Usage) RecordFailedUpstreamAttempt(ctx context.Context, req *gatewaypreauth.GatewayRequest, usageContext gatewaypreauth.GatewayFailureUsageContext, account AccountCandidate, record FailedAttemptRecord) error {
	u.fakeUsage.records = append(u.fakeUsage.records, record)
	if u.err != nil {
		return u.err
	}
	return nil
}

// w13g3Harness 组装引擎 + 覆盖常用 fake。
type w13g3Harness struct {
	t           *testing.T
	engine      *Engine
	driver      *fakeDriver
	dispatcher  *fakeFailureDispatcher
	locks       *w13g3FakeLocks
	queue       *w13g3Queue
	suppression *w13g3Suppression
	degradation *w13g3Degradation
	usage       *w13g3Usage
}

func newW13g3Harness(t *testing.T) *w13g3Harness {
	t.Helper()
	engine, driver, dispatcher := newTestEngine(t)
	h := &w13g3Harness{
		t:           t,
		engine:      engine,
		driver:      driver,
		dispatcher:  dispatcher,
		locks:       &w13g3FakeLocks{},
		queue:       &w13g3Queue{},
		suppression: &w13g3Suppression{},
		degradation: &w13g3Degradation{},
		usage:       &w13g3Usage{},
	}
	engine.Locks = h.locks
	engine.HighConcurrencyQueue = h.queue
	engine.Suppression = h.suppression
	engine.Degradation = h.degradation
	engine.Usage = h.usage
	return h
}

// w13g3WallBudget 构造剩余 remaining 的墙钟预算（60s 总预算回拨起点）。
func w13g3WallBudget(t *testing.T, remaining int64) *gatewayrouting.GatewayRequestWallBudget {
	t.Helper()
	budgetMs := int64(60_000)
	wallBudget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: gatewayupstream.NowMs() - (budgetMs - remaining),
		BudgetMs:            &budgetMs,
	}, nil)
	if err != nil {
		t.Fatalf("wall budget: %v", err)
	}
	return wallBudget
}

// w13g3CoordinationBudget 构造小额协调预算。
func w13g3CoordinationBudget(t *testing.T, budgetMs int64) *gatewayrouting.RouteCoordinationBudget {
	t.Helper()
	budget, err := gatewayrouting.NewRouteCoordinationBudget(gatewayrouting.RouteCoordinationBudgetOptions{
		RequestID: "trace-test",
		BudgetMs:  &budgetMs,
	})
	if err != nil {
		t.Fatalf("coordination budget: %v", err)
	}
	return budget
}

// w13g3Args 在 fastDispatchArgs 基础上恢复 5s 重试预算。
func w13g3Args(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	args := fastDispatchArgs(t, req, accounts)
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{})
	return args
}

// w13g3TinyElapsedBudget 返回已耗尽的小额重试预算（handoff 恒真）。
func w13g3TinyElapsedBudget() *gatewaypreauth.ServerRetryBudget {
	budget := gatewaypreauth.NewServerRetryBudget(1, gatewaypreauth.SystemClock{})
	past := gatewayupstream.NowMs() - 10
	budget.BeginNoAvailableWait(&past)
	now := gatewayupstream.NowMs()
	budget.PauseNoAvailableWait(&now)
	return budget
}

// w13g3TransientServer 驱动「首次 500 → 同账户重试 → 成功」场景。
func w13g3TransientServer(t *testing.T, h *w13g3Harness) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	server := sequentialServer(t, 1, 500)
	t.Cleanup(server.Close)
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	return w13g3Args(t, req, testAccounts("a-1"))
}

// ---------------------------------------------------------------------------
// 协处理器单元
// ---------------------------------------------------------------------------

func TestW13g3FirstByteCoordinatorLifecycle(t *testing.T) {
	released := []string{}
	newReservation := func(id string) *SpeedFirstCutoverReservationView {
		return &SpeedFirstCutoverReservationView{TargetAccountIDValue: id, ReleaseFunc: func() { released = append(released, id) }}
	}
	coordinator := NewNormalRouteFirstByteAttemptCoordinator()
	if !coordinator.AttachReservation(newReservation("r1")) {
		t.Fatal("active coordinator must accept the reservation")
	}
	if !coordinator.CanCutover() || coordinator.ReservedTargetAccountID() != "r1" {
		t.Fatalf("state = %v / %q", coordinator.CanCutover(), coordinator.ReservedTargetAccountID())
	}
	// 二次挂载释放前一个 reservation。
	if !coordinator.AttachReservation(newReservation("r2")) {
		t.Fatal("re-attach must succeed while active")
	}
	if len(released) != 1 || released[0] != "r1" {
		t.Fatalf("previous reservation must be released: %v", released)
	}
	coordinator.ReleaseReservation()
	if len(released) != 2 || released[1] != "r2" {
		t.Fatalf("releaseReservation must release the current reservation: %v", released)
	}
	if coordinator.CanCutover() || coordinator.ReservedTargetAccountID() != "" {
		t.Fatal("no reservation after release")
	}
	coordinator.ReleaseReservation() // 幂等

	// 转移：active 时返回 reservation 并复位。
	second := NewNormalRouteFirstByteAttemptCoordinator()
	second.AttachReservation(newReservation("r3"))
	transfer := second.TransferForCutover()
	if transfer == nil || transfer.TargetAccountIDValue != "r3" {
		t.Fatalf("transfer = %+v", transfer)
	}
	if second.TransferForCutover() != nil || second.CanCutover() {
		t.Fatal("transferred coordinator must be terminal")
	}
	// supersede：释放挂着的 reservation 并拒绝后续挂载。
	third := NewNormalRouteFirstByteAttemptCoordinator()
	third.AttachReservation(newReservation("r4"))
	third.Supersede()
	if len(released) != 3 || released[2] != "r4" {
		t.Fatalf("supersede must release: %v", released)
	}
	third.Supersede() // 幂等
	if third.CanCutover() || third.ReservedTargetAccountID() != "" {
		t.Fatal("superseded coordinator must not cut over")
	}
	if third.AttachReservation(newReservation("r5")) {
		t.Fatal("attach must fail after supersede")
	}
	if len(released) != 4 || released[3] != "r5" {
		t.Fatalf("rejected attach must release the incoming reservation: %v", released)
	}
	if third.TransferForCutover() != nil {
		t.Fatal("transfer must fail after supersede")
	}
	// 零值协调器拒绝挂载（NewNormalRouteFirstByteAttemptCoordinator 注释契约）。
	var zero NormalRouteFirstByteAttemptCoordinator
	if zero.AttachReservation(newReservation("r6")) {
		t.Fatal("zero coordinator must reject reservations")
	}
	zero.Supersede()
	zero.ReleaseReservation()
	if zero.TransferForCutover() != nil {
		t.Fatal("zero coordinator transfer must be nil")
	}
}

func TestW13g3UpstreamResponseModelSlot(t *testing.T) {
	var nilSlot *UpstreamResponseModelSlot
	nilSlot.Set("m")         // nil 安全
	if nilSlot.Get() != "" { // nil 安全
		t.Fatal("nil slot get must be empty")
	}
	nilSlot.Bind(func(string) {}) // nil 安全

	slot := &UpstreamResponseModelSlot{}
	if slot.Get() != "" {
		t.Fatal("unset slot")
	}
	slot.Set("gpt-a")
	if slot.Get() != "gpt-a" {
		t.Fatalf("get = %q", slot.Get())
	}
	// 发布先于绑定：Bind 立即补发。
	replayed := ""
	slot.Bind(func(model string) { replayed = model })
	if replayed != "gpt-a" {
		t.Fatalf("bind replay = %q", replayed)
	}
	// 绑定后发布同步转发。
	forwarded := ""
	slot2 := &UpstreamResponseModelSlot{}
	slot2.Bind(func(model string) { forwarded = model })
	slot2.Set("gpt-b")
	if forwarded != "gpt-b" || slot2.Get() != "gpt-b" {
		t.Fatalf("forward = %q get = %q", forwarded, slot2.Get())
	}
	// nil consumer 无副作用。
	slot2.Bind(nil)
	slot2.Set("gpt-c")
}

type w13g3CustomLifecycle struct {
	firstByteCalls int
	terminalCalls  int
}

func (l *w13g3CustomLifecycle) MarkFirstByte(firstByteMs *float64) { l.firstByteCalls++ }
func (l *w13g3CustomLifecycle) RecordTerminal(ctx context.Context, terminal HotQualityTerminal) {
	l.terminalCalls++
}

func TestW13g3HotQualityHandleLifecycleVariants(t *testing.T) {
	custom := &w13g3CustomLifecycle{}
	cases := []struct {
		name    string
		factory HotQualityAttemptLifecycleFactory
	}{
		{"nil factory keeps no-op", nil},
		{"custom lifecycle adapter", func(HotQualityLifecycleInput) HotQualityAttemptLifecycle { return custom }},
		{"factory nil product keeps no-op", func(HotQualityLifecycleInput) HotQualityAttemptLifecycle { return nil }},
		{"concrete facade passthrough", func(HotQualityLifecycleInput) HotQualityAttemptLifecycle {
			return &attemptLifecycleFacade{
				MarkFirstByteFunc:  func(*float64) {},
				RecordTerminalFunc: func(context.Context, HotQualityTerminal) {},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handle := &hotQualityAttemptHandle{factory: tc.factory, input: HotQualityLifecycleInput{AttemptID: "a"}}
			handle.MarkFirstByte(nil)
			handle.RecordTerminal(context.Background(), HotQualityTerminal{OutcomeClass: HotQualityOutcomeUnknown})
			handle.MarkFirstByte(nil) // once 之后复用同一 lifecycle
			handle.RecordTerminal(context.Background(), HotQualityTerminal{})
		})
	}
	if custom.firstByteCalls != 2 || custom.terminalCalls != 2 {
		t.Fatalf("custom lifecycle calls: firstByte=%d terminal=%d", custom.firstByteCalls, custom.terminalCalls)
	}
}

func TestW13g3SessionIdentityAndEvidenceEdges(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	usage := testUsageContext()
	// SessionID 全空白 → 回落 unknown caller 桶（1368-1370）。
	engine.SessionIdentity = func(req *gatewaypreauth.GatewayRequest) SessionIdentityView {
		return SessionIdentityView{SessionID: "   ", SemanticNamespace: "ns"}
	}
	blank := engine.gatewayForegroundAccountCircuitFailureEvidenceKey(newTestRequest(t, `{}`), usage)
	// SemanticNamespace 为空时回落 gateway_session。
	engine.SessionIdentity = func(req *gatewaypreauth.GatewayRequest) SessionIdentityView {
		return SessionIdentityView{SessionID: " s1 "}
	}
	defaultNS := engine.gatewayForegroundAccountCircuitFailureEvidenceKey(newTestRequest(t, `{}`), usage)
	engine.SessionIdentity = func(req *gatewaypreauth.GatewayRequest) SessionIdentityView {
		return SessionIdentityView{SessionID: "s2", SemanticNamespace: "ns"}
	}
	named := engine.gatewayForegroundAccountCircuitFailureEvidenceKey(newTestRequest(t, `{}`), usage)
	if blank == defaultNS || defaultNS == named || blank == named {
		t.Fatalf("digests must differ: %s %s %s", blank, defaultNS, named)
	}
	if got := accountCircuitEvidenceDigest(map[string]any{"k": "v"}); len(got) != 64 {
		t.Fatalf("digest = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 主循环入口与过滤
// ---------------------------------------------------------------------------

func TestW13g3DefaultLaneAndSignalFallbacks(t *testing.T) {
	server := sequentialServer(t, 0, 500)
	defer server.Close()
	h := newW13g3Harness(t)
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1"))
	args.RequestLane = "" // 415-417 回落 text
	args.Signal = nil     // 419-421 回落 ctx
	started := 0
	args.RequestCoordination.OnUpstreamAttemptStarted = func(account AccountCandidate, upstreamURL string) { started++ }
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-1" || started != 1 {
		t.Fatalf("account=%s started=%d", result.Account.ID, started)
	}
}

func TestW13g3BlankAccountIDRejectedByTracker(t *testing.T) {
	h := newW13g3Harness(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1", ""))
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err == nil {
		t.Fatal("blank account runtime key must be rejected")
	}
}

func TestW13g3CircuitConfirmationRuntimeKeyError(t *testing.T) {
	h := newW13g3Harness(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	authorized := testAccounts("a-1")[0]
	authorized.AccountAccessType = "account_authorized" // 缺绑定上下文 → 运行态键错误
	args := w13g3Args(t, req, []AccountCandidate{authorized})
	args.AccountCircuitConfirmation = &gatewaycircuit.Confirmation{AccountRuntimeKey: "a-1"}
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "授权账户运行态键缺少绑定上下文") {
		t.Fatalf("expected runtime key error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 账户锁租约 × 同账户重试
// ---------------------------------------------------------------------------

func TestW13g3AccountLockLeaseConsumedForSameAccountRetry(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: true, LeaseID: "lease-w13g3-a"}}
	args := w13g3TransientServer(t, h)
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	if len(h.locks.abandons) != 0 {
		t.Fatalf("no abandonment expected: %v", h.locks.abandons)
	}
	// 成功结果携带的租约释放闭包消费活跃租约（587-601）。
	if result.ReleaseAccountLockRetryLease == nil {
		t.Fatal("ReleaseAccountLockRetryLease missing")
	}
	if !result.ReleaseAccountLockRetryLease(false) {
		t.Fatal("release must report success")
	}
	found := false
	for _, release := range h.locks.releases {
		if release.AccountID == "a-1" && release.LeaseID == "lease-w13g3-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("consumed lease must be released: %+v", h.locks.releases)
	}
}

func TestW13g3AccountLockLeaseWaitThenConsume(t *testing.T) {
	h := newW13g3Harness(t)
	// 首次获取被拒但给 30ms 等待 + 租约 → 等待完成后消费（649-689）。
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "lease-w13g3-b", WaitMs: 30}}
	args := w13g3TransientServer(t, h)
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	// 消费后的活跃租约挂在成功结果的释放闭包上（587-601）。
	if result.ReleaseAccountLockRetryLease == nil {
		t.Fatal("ReleaseAccountLockRetryLease missing")
	}
	if !result.ReleaseAccountLockRetryLease(false) {
		t.Fatal("release must report success")
	}
	found := false
	for _, release := range h.locks.releases {
		if release.AccountID == "a-1" && release.LeaseID == "lease-w13g3-b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("wait-completed lease must be released: %+v", h.locks.releases)
	}
}

func TestW13g3AccountLockLeaseWaitAborted(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "lease-w13g3-c", WaitMs: 2_000}}
	args := w13g3TransientServer(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	args.Signal = ctx
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err == nil {
		t.Fatal("expected failure after aborted same-account wait")
	}
}

func TestW13g3AccountLockLeaseWallBudgetAbortsRetry(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "lease-w13g3-d", WaitMs: 3_000}}
	args := w13g3TransientServer(t, h)
	args.RequestCoordination.GatewayRequestWallBudget = w13g3WallBudget(t, 2_050)
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) || budgetErr.BudgetKind != WallBudgetKindWall {
		t.Fatalf("expected wall budget error, got %v", err)
	}
}

func TestW13g3AccountLockLeaseCoordinationBudgetAbortsRetry(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "lease-w13g3-e", WaitMs: 300}}
	args := w13g3TransientServer(t, h)
	args.RequestCoordination.RouteCoordinationBudget = w13g3CoordinationBudget(t, 50)
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) || budgetErr.BudgetKind != WallBudgetKindCoordination {
		t.Fatalf("expected coordination budget error, got %v", err)
	}
}

func TestW13g3AccountLockLeaseConsumeOutcomes(t *testing.T) {
	t.Run("consume false gives up retry", func(t *testing.T) {
		h := newW13g3Harness(t)
		h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "lease-w13g3-f", WaitMs: 20}}
		h.locks.consumeOK = []bool{false}
		args := w13g3TransientServer(t, h)
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("expected attempt error, got %v", err)
		}
	})
	t.Run("first consume error surfaces", func(t *testing.T) {
		h := newW13g3Harness(t)
		h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "lease-w13g3-g", WaitMs: 20}}
		h.locks.consumeErr = errW13g3
		args := w13g3TransientServer(t, h)
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected consume error, got %v", err)
		}
	})
	t.Run("second consume error surfaces", func(t *testing.T) {
		h := newW13g3Harness(t)
		h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: true, LeaseID: "lease-w13g3-h"}}
		h.locks.consumeErr = errW13g3
		args := w13g3TransientServer(t, h)
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected consume error, got %v", err)
		}
	})
}

func TestW13g3AccountLockLeaseAcquireError(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireErr = errW13g3
	args := w13g3TransientServer(t, h)
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if !errors.Is(err, errW13g3) {
		t.Fatalf("expected acquire error, got %v", err)
	}
}

func TestW13g3AccountLockLeaseReleaseError(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.releaseErr = errW13g3
	args := w13g3TransientServer(t, h)
	args.RequestCoordination.AccountLockRetryLease = &AccountLockRetryLease{AccountID: "a-1", LeaseID: "lease-w13g3-i"}
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if !errors.Is(err, errW13g3) {
		t.Fatalf("expected release error, got %v", err)
	}
}

func TestW13g3AccountLockRecordFailureTransportPath(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recordErr error
		wantErr   bool
	}{
		{"record failure ok", nil, false},
		{"record failure error", errW13g3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newW13g3Harness(t)
			h.locks.recordErr = tc.recordErr
			// 拒绝连接的传输错误按已开始传输失败分类（transport.go client.Do 错误
			// 恒标 started）→ 走 skip_account + 同账户重试路径。
			h.driver.urlByAccount = map[string][]string{"a-1": {"https://127.0.0.1:9/v1/chat/completions"}}
			req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
			args := w13g3Args(t, req, testAccounts("a-1"))
			_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
			if tc.wantErr {
				if !errors.Is(err, errW13g3) {
					t.Fatalf("expected record failure error, got %v", err)
				}
				return
			}
			var attemptErr *UpstreamAttemptError
			if !errorsAs(err, &attemptErr) {
				t.Fatalf("expected attempt error, got %v", err)
			}
			if h.locks.failureCalls == 0 {
				t.Fatal("transport failure must be recorded on the account lock")
			}
		})
	}
}

func TestW13g3AccountLockReacquireOutcomes(t *testing.T) {
	t.Run("reacquire rejected gives up", func(t *testing.T) {
		h := newW13g3Harness(t)
		h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "", WaitMs: 20}}
		args := w13g3TransientServer(t, h)
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("expected attempt error, got %v", err)
		}
	})
	t.Run("reacquire error surfaces", func(t *testing.T) {
		h := newW13g3Harness(t)
		h.locks.acquireErr = errW13g3
		h.locks.acquireErrAfter = 1
		h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: false, LeaseID: "", WaitMs: 20}}
		args := w13g3TransientServer(t, h)
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected reacquire error, got %v", err)
		}
	})
}

func TestW13g3AccountLockHandoffAbandonsReservation(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: true, LeaseID: "lease-w13g3-j", WaitMs: 60_000}}
	args := w13g3TransientServer(t, h)
	// available = 50ms，WaitMs 60s → handoff → 放弃预留并退出重试。
	args.RequestCoordination.GatewayRequestWallBudget = w13g3WallBudget(t, 2_050)
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected attempt error, got %v", err)
	}
	if len(h.locks.abandons) != 1 || h.locks.abandons[0].LeaseID != "lease-w13g3-j" {
		t.Fatalf("reservations abandoned: %+v", h.locks.abandons)
	}
}

func TestW13g3AccountLockSecondWaitCoordinationAborts(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: true, LeaseID: "lease-w13g3-k", WaitMs: 300}}
	args := w13g3TransientServer(t, h)
	args.RequestCoordination.RouteCoordinationBudget = w13g3CoordinationBudget(t, 50)
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) || budgetErr.BudgetKind != WallBudgetKindCoordination {
		t.Fatalf("expected coordination error, got %v", err)
	}
}

func TestW13g3AccountLockSecondWaitAbortedAbandons(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: true, LeaseID: "lease-w13g3-l", WaitMs: 2_000}}
	args := w13g3TransientServer(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	args.Signal = ctx
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err == nil {
		t.Fatal("expected failure after aborted same-account wait")
	}
	if len(h.locks.abandons) == 0 {
		t.Fatalf("aborted wait must abandon the lease: %+v", h.locks.abandons)
	}
}

func TestW13g3AccountLockHandoffDecisionError(t *testing.T) {
	h := newW13g3Harness(t)
	h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: true, LeaseID: "lease-w13g3-m", WaitMs: -5}}
	args := w13g3TransientServer(t, h)
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if err == nil || errorsAs(err, &attemptErr) {
		t.Fatalf("expected handoff decision error, got %v", err)
	}
}

func TestW13g3PlainSameAccountDelayAborted(t *testing.T) {
	h := newW13g3Harness(t)
	h.engine.Config.AccountConcurrencyRetryBudgetMs = 0
	args := w13g3TransientServer(t, h)
	// 租约获取成功但没有 leaseID → 走配置延迟（interval=1s）→ 信号 200ms 中止。
	args.Settings.TemporaryUnschedulableRetryIntervalSeconds = 1
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	args.Signal = ctx
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) {
		t.Fatalf("expected abort during same-account delay, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 容量排队
// ---------------------------------------------------------------------------

type w13g3Concurrency struct {
	fakeConcurrencyStore
	failFirst int
	calls     int
}

func (c *w13g3Concurrency) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	c.calls++
	if c.calls <= c.failFirst {
		return ConcurrencySlot{Acquired: false, Current: concurrencyLimit, Limit: concurrencyLimit, Lane: options.Lane}, nil
	}
	return c.fakeConcurrencyStore.TryAcquireAsync(ctx, accountID, concurrencyLimit, options)
}

func TestW13g3CapacityQueueWaitPaths(t *testing.T) {
	policy := gatewayruntimecache.GroupSchedulingPolicy{"mode": "queue"}
	newCapacityEngine := func(t *testing.T, failFirst int) (*w13g3Harness, *w13g3Concurrency) {
		t.Helper()
		h := newW13g3Harness(t)
		h.engine.Config.AccountConcurrencyRetryBudgetMs = 0
		concurrency := &w13g3Concurrency{failFirst: failFirst}
		h.engine.Concurrency = concurrency
		return h, concurrency
	}

	t.Run("queue error surfaces", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		h.queue.err = errW13g3
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.GroupSchedulingPolicy = &policy
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected queue error, got %v", err)
		}
	})

	t.Run("ready reorders and dispatches", func(t *testing.T) {
		server := sequentialServer(t, 0, 500)
		defer server.Close()
		h, concurrency := newCapacityEngine(t, 1)
		h.queue.result = QueueWaitResult{Ready: true, WaitedMs: 5, QueueSize: 1}
		h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.GroupSchedulingPolicy = &policy
		result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if result.Account.ID != "a-1" || concurrency.calls < 2 {
			t.Fatalf("account=%s acquireCalls=%d", result.Account.ID, concurrency.calls)
		}
	})

	t.Run("ready order error surfaces", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		h.queue.result = QueueWaitResult{Ready: true}
		h.degradation.errOnLaneCall = 2 // 第二次（ready 重排）失败
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.GroupSchedulingPolicy = &policy
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected order error, got %v", err)
		}
	})

	t.Run("aborted after queue wait", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		h.queue.result = QueueWaitResult{Ready: true}
		h.queue.sleepMs = 300
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.GroupSchedulingPolicy = &policy
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		args.Signal = ctx
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort after queue wait, got %v", err)
		}
	})

	t.Run("retry delay deadline with policy", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		h.queue.result = QueueWaitResult{Ready: false}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.GroupSchedulingPolicy = &policy
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		args.Signal = ctx
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline during capacity retry delay, got %v", err)
		}
	})

	t.Run("retry delay canceled with policy", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		h.queue.result = QueueWaitResult{Ready: false}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.GroupSchedulingPolicy = &policy
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(150*time.Millisecond, cancel)
		defer timer.Stop()
		defer cancel()
		args.Signal = ctx
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort during capacity retry delay, got %v", err)
		}
	})

	t.Run("handoff records capacity failure", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		h.queue.result = QueueWaitResult{Ready: false, Reason: "timeout"}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.GroupSchedulingPolicy = &policy
		args.RequestCoordination.ServerRetryBudget = w13g3TinyElapsedBudget()
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("expected attempt error, got %v", err)
		}
		if len(h.usage.records) == 0 {
			t.Fatal("capacity limit failure must be recorded on usage")
		}
	})

	t.Run("no policy handoff records capacity failure", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.RequestCoordination.ServerRetryBudget = w13g3TinyElapsedBudget()
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("expected attempt error, got %v", err)
		}
		if len(h.usage.records) == 0 {
			t.Fatal("capacity limit failure must be recorded on usage")
		}
	})

	t.Run("no policy retry delay deadline", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		defer cancel()
		args.Signal = ctx
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline during capacity retry delay, got %v", err)
		}
	})

	t.Run("no policy retry delay canceled", func(t *testing.T) {
		h, _ := newCapacityEngine(t, 99)
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(120*time.Millisecond, cancel)
		defer timer.Stop()
		defer cancel()
		args.Signal = ctx
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort during capacity retry delay, got %v", err)
		}
	})
}

func TestW13g3RecordCapacityLimitFailureUnit(t *testing.T) {
	t.Run("usage error surfaces", func(t *testing.T) {
		engine, _, _ := newTestEngine(t)
		engine.Usage = &w13g3Usage{err: errW13g3}
		err := engine.recordAccountCapacityLimitFailure(
			context.Background(), testUsageContext(), testAccounts("a-1")[0], "并发已满",
			AuditCapture{Sink: &fakeAuditSink{}}, 1)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected usage error, got %v", err)
		}
	})
	t.Run("ok path records audit failure", func(t *testing.T) {
		engine, _, _ := newTestEngine(t)
		usage := &w13g3Usage{}
		engine.Usage = usage
		sink := &fakeAuditSink{}
		err := engine.recordAccountCapacityLimitFailure(
			context.Background(), testUsageContext(), testAccounts("a-1")[0], "并发已满",
			AuditCapture{Sink: sink}, 2)
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		if len(usage.records) != 1 || sink.failed != 1 {
			t.Fatalf("records=%d failed=%d", len(usage.records), sink.failed)
		}
	})
}

// ---------------------------------------------------------------------------
// 可恢复等待
// ---------------------------------------------------------------------------

type w13g3Waiter struct {
	mu        sync.Mutex
	result    SuppressionFilterResult
	err       error
	blockOn   context.Context
	useHooks  bool
	hookCalls int
}

func (w *w13g3Waiter) WaitForState(ctx context.Context, input SuppressionWaitInput) (SuppressionFilterResult, error) {
	if w.useHooks {
		w.mu.Lock()
		w.hookCalls++
		w.mu.Unlock()
		if input.IsReady != nil {
			_ = input.IsReady(w.result)
		}
		if input.NextRetryAfterMs != nil {
			_ = input.NextRetryAfterMs(w.result)
		}
	}
	if w.blockOn != nil {
		<-w.blockOn.Done()
		return SuppressionFilterResult{}, context.Canceled
	}
	if w.err != nil {
		return SuppressionFilterResult{}, w.err
	}
	return w.result, nil
}

func TestW13g3RecoverableWaiterRecoversAndDispatches(t *testing.T) {
	h := newW13g3Harness(t)
	server := sequentialServer(t, 1, 500)
	defer server.Close()
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1"))
	args.WaitForRecoverableFailures = true
	// 周期 1：账户级与周期后过滤都全抑制 → 等待器恢复 → 周期 2 放行并成功。
	h.suppression.perAccountPlan = []w13g3Phase{{all: true}, {}}
	h.suppression.postCyclePlan = []w13g3Phase{{all: true}, {}}
	waiter := &w13g3Waiter{
		useHooks: true,
		result:   SuppressionFilterResult{Accounts: testAccounts("a-1")},
	}
	h.engine.RecoverableWait = waiter
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	if waiter.hookCalls != 1 {
		t.Fatalf("waiter hooks = %d", waiter.hookCalls)
	}
}

func TestW13g3RecoverableWaiterErrorAndCancel(t *testing.T) {
	t.Run("waiter error surfaces", func(t *testing.T) {
		h := newW13g3Harness(t)
		server := sequentialServer(t, 99, 503)
		defer server.Close()
		h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.WaitForRecoverableFailures = true
		h.suppression.perAccountPlan = []w13g3Phase{{}}
		h.suppression.postCyclePlan = []w13g3Phase{{all: true}}
		h.engine.RecoverableWait = &w13g3Waiter{err: errW13g3}
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected waiter error, got %v", err)
		}
	})
	t.Run("waiter cancel aborts", func(t *testing.T) {
		h := newW13g3Harness(t)
		server := sequentialServer(t, 99, 503)
		defer server.Close()
		h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.WaitForRecoverableFailures = true
		h.suppression.perAccountPlan = []w13g3Phase{{}}
		h.suppression.postCyclePlan = []w13g3Phase{{all: true}}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		defer cancel()
		args.Signal = ctx
		h.engine.RecoverableWait = &w13g3Waiter{blockOn: ctx}
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort from canceled waiter, got %v", err)
		}
	})
}

func TestW13g3RecoverableSubsetFilterErrorAndHandoff(t *testing.T) {
	t.Run("subset filter error", func(t *testing.T) {
		h := newW13g3Harness(t)
		server := sequentialServer(t, 99, 503)
		defer server.Close()
		h.driver.urlByAccount = map[string][]string{
			"a-1": {server.URL + "/v1/chat/completions"},
			"a-2": {server.URL + "/v1/chat/completions"},
		}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1", "a-2"))
		args.WaitForRecoverableFailures = true
		// 周期后过滤标记 a-1 可恢复（账户级放行），子集过滤报错。
		h.suppression.perAccountPlan = []w13g3Phase{{}}
		h.suppression.postCyclePlan = []w13g3Phase{{ids: []string{"a-1"}}, {err: errW13g3}}
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected subset filter error, got %v", err)
		}
	})
	t.Run("handoff breaks before wait", func(t *testing.T) {
		h := newW13g3Harness(t)
		server := sequentialServer(t, 99, 503)
		defer server.Close()
		h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.WaitForRecoverableFailures = true
		args.RequestCoordination.ServerRetryBudget = w13g3TinyElapsedBudget()
		h.suppression.perAccountPlan = []w13g3Phase{{}}
		h.suppression.postCyclePlan = []w13g3Phase{{ids: []string{"a-1"}}}
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("expected attempt error, got %v", err)
		}
	})
}

func TestW13g3RecoverableWaitContinuesAfterDelay(t *testing.T) {
	h := newW13g3Harness(t)
	server := sequentialServer(t, 99, 503)
	defer server.Close()
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1"))
	args.WaitForRecoverableFailures = true
	// 周期 1 标记 a-1 可恢复 → 记录等待并延迟（3s 上限）→ 重排继续一个周期。
	h.suppression.perAccountPlan = []w13g3Phase{{}}
	h.suppression.postCyclePlan = []w13g3Phase{{ids: []string{"a-1"}}, {}}
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected attempt error, got %v", err)
	}
	if h.suppression.postCycleCalls < 2 {
		t.Fatalf("post-cycle filter calls = %d", h.suppression.postCycleCalls)
	}
}

func TestW13g3AllSuppressedExhaustedPaths(t *testing.T) {
	t.Run("no waiter ends suppressed", func(t *testing.T) {
		h := newW13g3Harness(t)
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.WaitForRecoverableFailures = true
		h.suppression.perAccountPlan = []w13g3Phase{{all: true}}
		h.suppression.postCyclePlan = []w13g3Phase{{all: true}}
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("expected attempt error, got %v", err)
		}
		if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "account:locally_suppressed" {
			t.Fatalf("lastAttempt = %+v", attemptErr.LastAttempt)
		}
	})
	t.Run("abort checked at exhaustion", func(t *testing.T) {
		h := newW13g3Harness(t)
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.WaitForRecoverableFailures = true
		h.suppression.perAccountPlan = []w13g3Phase{{all: true}}
		h.suppression.postCyclePlan = []w13g3Phase{{all: true, sleep: 200 * time.Millisecond}}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		args.Signal = ctx
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort at suppression exhaustion, got %v", err)
		}
	})
}
