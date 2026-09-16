package keymodelrecovery

// w12b 波次 runner 分支覆盖测试：RunCycle 基础设施错误、RECOVERING 续跑标志、
// runCandidate 失败臂、Run 周期告警与 renewLease 失去租约路径。
//
// 不可达语句登记（无法通过任何输入触达）：
//   - runner.go newLeaseID 的 crypto/rand 失败臂：测试进程无法注入 rand.Read 失败。
//   - runner.go renewLease 失败分支的 select default 臂：lostLease 通道容量为 1
//     且该 goroutine 是唯一发送方，发送前通道必为空，default 不可能命中。
//   - state.go Hash 的 json.Marshal 错误臂：载荷仅含定长字符串与 int64，编码恒成功。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

// w12bArmsStore 是可注入错误的 Store，额外实现 CleanClosed 以覆盖清理分支。
type w12bArmsStore struct {
	now       time.Time
	candidate State

	nowErr        error
	settleNowErr  error
	settleNow     time.Time
	listErr       error
	cleanErr      error
	acquireErr    error
	acquireStatus MutationStatus
	renewOK       bool
	renewErr      error
	commitErr     error
	commitStatus  MutationStatus

	blockRelease chan struct{} // 非空时首次 ServerNow 阻塞直至关闭
	entered      chan struct{} // 首次 ServerNow 进入信号

	acquireSignal chan struct{}
	settleSignal  chan struct{}
	commitCalled  chan struct{}

	mu                    sync.Mutex
	nowCalls              int
	cleanCalls            int
	sawContinuation       bool
	sawSourceContinuation bool
}

func (s *w12bArmsStore) signal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *w12bArmsStore) ServerNow(context.Context) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowCalls++
	if s.nowCalls == 1 {
		if s.blockRelease != nil {
			s.signal(s.entered)
			release := s.blockRelease
			s.mu.Unlock()
			<-release
			s.mu.Lock()
		}
		if s.nowErr != nil {
			return time.Time{}, s.nowErr
		}
		return s.now, nil
	}
	s.signal(s.settleSignal)
	if s.settleNowErr != nil {
		return time.Time{}, s.settleNowErr
	}
	if !s.settleNow.IsZero() {
		return s.settleNow, nil
	}
	return s.now, nil
}

func (s *w12bArmsStore) ListDue(context.Context, time.Time, int64) ([]State, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return []State{s.candidate}, nil
}

func (s *w12bArmsStore) CleanClosed(context.Context, int64) (int64, error) {
	s.mu.Lock()
	s.cleanCalls++
	s.mu.Unlock()
	if s.cleanErr != nil {
		return 0, s.cleanErr
	}
	return 0, nil
}

func (s *w12bArmsStore) Acquire(_ context.Context, candidate State, leaseID string, continuationWaiting, sourceContinuationWaiting bool) (State, MutationStatus, error) {
	s.mu.Lock()
	s.sawContinuation = continuationWaiting
	s.sawSourceContinuation = sourceContinuationWaiting
	acquireErr, acquireStatus := s.acquireErr, s.acquireStatus
	s.mu.Unlock()
	if acquireErr != nil {
		s.signal(s.acquireSignal)
		return State{}, "", acquireErr
	}
	if acquireStatus != "" && acquireStatus != Applied {
		s.signal(s.acquireSignal)
		return candidate, acquireStatus, nil
	}
	next, status := Acquire(candidate, candidate.Generation, candidate.DispatchRevision, leaseID, s.now)
	return next, status, nil
}

func (s *w12bArmsStore) Renew(context.Context, State, string) (bool, error) {
	return s.renewOK, s.renewErr
}

func (s *w12bArmsStore) Commit(context.Context, State, State, string) (MutationStatus, error) {
	s.signal(s.commitCalled)
	if s.commitErr != nil {
		return "", s.commitErr
	}
	if s.commitStatus != "" {
		return s.commitStatus, nil
	}
	return Applied, nil
}

func (s *w12bArmsStore) cleanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanCalls
}

func (s *w12bArmsStore) continuationFlags() (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sawContinuation, s.sawSourceContinuation
}

func w12bWaitChannel(t *testing.T, ch chan struct{}, message string) {
	t.Helper()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("等待超时: %s", message)
	}
}

func w12bDueCandidate(t *testing.T, now time.Time) State {
	t.Helper()
	state, err := NewOpen(key(), now)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func w12bMatchingLoader(t *testing.T, state State) runnerLoader {
	t.Helper()
	return runnerLoader{input: accounthealth.Input{AccountID: state.CredentialSourceAccountID, DispatchRevision: state.DispatchRevision}}
}

func TestW12bRunCycleInfrastructureErrors(t *testing.T) {
	ctx := context.Background()
	state := w12bDueCandidate(t, time.Unix(10_000, 0).UTC())

	store := &w12bArmsStore{now: state.RetryAt, candidate: state, nowErr: errors.New("w12b redis time down")}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err == nil {
		t.Fatal("ServerNow 失败应返回错误")
	}

	store = &w12bArmsStore{now: state.RetryAt, candidate: state, cleanErr: errors.New("w12b clean down")}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err == nil {
		t.Fatal("CleanClosed 失败应返回错误")
	}

	store = &w12bArmsStore{now: state.RetryAt, candidate: state, listErr: errors.New("w12b list down")}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err == nil {
		t.Fatal("ListDue 失败应返回错误")
	}
}

func TestW12bRunCycleRecoveringContinuationFlags(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0).UTC()
	state := w12bDueCandidate(t, now)
	state.Phase = Recovering
	store := &w12bArmsStore{now: state.RetryAt, candidate: state, settleSignal: make(chan struct{}, 4), commitCalled: make(chan struct{}, 4)}
	runner := NewRunner(store, w12bMatchingLoader(t, state), nil)
	runner.probe = func(_ context.Context, candidate State, _ accounthealth.Input) Outcome {
		if candidate.CapabilityHash != state.CapabilityHash {
			t.Errorf("probe 收到意外候选: %s", candidate.CapabilityHash)
		}
		return CompleteSuccess
	}
	if err := runner.RunCycle(ctx); err != nil {
		t.Fatalf("第一轮 RunCycle: %v", err)
	}
	w12bWaitChannel(t, store.commitCalled, "第一轮 commit")
	waiting, sourceWaiting := store.continuationFlags()
	if !waiting || !sourceWaiting {
		t.Fatalf("RECOVERING 候选应置续跑标志: waiting=%v sourceWaiting=%v", waiting, sourceWaiting)
	}
	if err := runner.RunCycle(ctx); err != nil {
		t.Fatalf("第二轮 RunCycle: %v", err)
	}
	w12bWaitChannel(t, store.commitCalled, "第二轮 commit")
	if got := store.cleanCount(); got != 1 {
		t.Fatalf("五分钟窗口内 CleanClosed 只应执行一次: %d", got)
	}
}

func TestW12bRunCandidateFailureArms(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0).UTC()
	state := w12bDueCandidate(t, now)

	// acquire 错误臂。
	store := &w12bArmsStore{now: state.RetryAt, candidate: state, acquireErr: errors.New("w12b acquire down"), acquireSignal: make(chan struct{}, 1)}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err != nil {
		t.Fatalf("acquire 错误不应冒泡: %v", err)
	}
	w12bWaitChannel(t, store.acquireSignal, "acquire 错误")

	// acquire 状态非 applied 臂。
	store = &w12bArmsStore{now: state.RetryAt, candidate: state, acquireStatus: Stale, acquireSignal: make(chan struct{}, 1)}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err != nil {
		t.Fatalf("acquire stale 不应冒泡: %v", err)
	}
	w12bWaitChannel(t, store.acquireSignal, "acquire stale")

	// 结算时间读取失败臂。
	store = &w12bArmsStore{now: state.RetryAt, candidate: state, settleNowErr: errors.New("w12b settle time down"), settleSignal: make(chan struct{}, 1)}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err != nil {
		t.Fatalf("结算时间错误不应冒泡: %v", err)
	}
	w12bWaitChannel(t, store.settleSignal, "结算时间失败")

	// 结算 CAS 过期臂：观测时间晚于租约到期。
	store = &w12bArmsStore{now: state.RetryAt, candidate: state, settleNow: now.Add(2 * time.Minute), settleSignal: make(chan struct{}, 1)}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err != nil {
		t.Fatalf("结算过期不应冒泡: %v", err)
	}
	w12bWaitChannel(t, store.settleSignal, "结算过期")

	// commit 错误臂。
	store = &w12bArmsStore{now: state.RetryAt, candidate: state, commitErr: errors.New("w12b commit down"), commitCalled: make(chan struct{}, 1)}
	if err := NewRunner(store, w12bMatchingLoader(t, state), nil).RunCycle(ctx); err != nil {
		t.Fatalf("commit 错误不应冒泡: %v", err)
	}
	w12bWaitChannel(t, store.commitCalled, "commit 错误")
	time.Sleep(150 * time.Millisecond)
}

// w12bCaptureHandler 捕获 Warn 级别日志消息。
type w12bCaptureHandler struct {
	mu    sync.Mutex
	warns chan string
}

func (h *w12bCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *w12bCaptureHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Level == slog.LevelWarn {
		h.mu.Lock()
		warns := h.warns
		h.mu.Unlock()
		select {
		case warns <- record.Message:
		default:
		}
	}
	return nil
}
func (h *w12bCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *w12bCaptureHandler) WithGroup(string) slog.Handler      { return h }

func TestW12bRunLogsCycleWarningThenExits(t *testing.T) {
	state := w12bDueCandidate(t, time.Unix(10_000, 0).UTC())
	store := &w12bArmsStore{now: state.RetryAt, candidate: state, nowErr: errors.New("w12b first tick down"), blockRelease: make(chan struct{}), entered: make(chan struct{}, 1), commitCalled: make(chan struct{}, 2)}
	handler := &w12bCaptureHandler{warns: make(chan string, 4)}
	runner := NewRunner(store, w12bMatchingLoader(t, state), slog.New(handler))
	runner.probe = func(context.Context, State, accounthealth.Input) Outcome { return CompleteSuccess }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()

	w12bWaitChannel(t, store.entered, "首次 ServerNow 进入")
	close(store.blockRelease)

	select {
	case message := <-handler.warns:
		if message != "model-recovery scan failed" {
			t.Fatalf("意外告警: %q", message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunCycle 失败应记录周期告警")
	}

	// 第二个周期由 ticker 驱动并完整走一遍 happy path。
	w12bWaitChannel(t, store.commitCalled, "ticker 周期 commit")
	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后 Run 应返回 ctx.Err: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Run 应退出")
	}
}

func TestW12bRenewLeaseLostLeaseSkipsSettlement(t *testing.T) {
	if testing.Short() {
		t.Skip("w12b 慢速用例：依赖真实 10s 续约 ticker")
	}
	ctx := context.Background()
	now := time.Unix(10_000, 0).UTC()
	state := w12bDueCandidate(t, now)
	store := &w12bArmsStore{now: state.RetryAt, candidate: state, renewOK: false, commitCalled: make(chan struct{}, 1)}
	runner := NewRunner(store, w12bMatchingLoader(t, state), nil)
	probeEntered := make(chan struct{}, 1)
	probeReturned := make(chan struct{}, 1)
	runner.probe = func(probeCtx context.Context, _ State, _ accounthealth.Input) Outcome {
		probeEntered <- struct{}{}
		<-probeCtx.Done() // 续约失败触发 cancel 后返回，避免整测超时。
		probeReturned <- struct{}{}
		return Unknown
	}
	if err := runner.RunCycle(ctx); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	w12bWaitChannel(t, probeEntered, "探针进入")
	// renewLease 的 ticker 是真实的 10s ProbeLeaseRenew，等待上限需覆盖一个周期。
	select {
	case <-probeReturned:
	case <-time.After(15 * time.Second):
		t.Fatal("续约失败后探针应随 cancel 返回")
	}
	select {
	case <-store.commitCalled:
		t.Fatal("失去租约后不应结算")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestW12bStateAndHelperArms(t *testing.T) {
	// UnmarshalJSON 错误臂。
	var decoded State
	if err := json.Unmarshal([]byte(`{"generation":"NaN"}`), &decoded); err == nil {
		t.Fatal("非法 state JSON 应报错")
	}

	// PrioritizeDue 同为非续跑时的 RetryAt 排序比较臂。
	now := time.Unix(20_000, 0).UTC()
	early := w12bDueCandidate(t, now)
	later := w12bDueCandidate(t, now)
	later.RetryAt = early.RetryAt.Add(time.Second)
	ordered := PrioritizeDue([]Due{{State: early, SourceID: early.CredentialSourceAccountID}, {State: later, SourceID: later.CredentialSourceAccountID}}, now.Add(time.Minute))
	if len(ordered) != 2 || ordered[0].State.CapabilityHash != early.CapabilityHash {
		t.Fatalf("READY 候选应按 RetryAt 升序: %#v", ordered)
	}

	// SelectDue 单来源上限臂：第三个同源候选因 source limit 被跳过。
	third := w12bDueCandidate(t, now)
	items := []Due{
		{State: early, SourceID: early.CredentialSourceAccountID},
		{State: later, SourceID: later.CredentialSourceAccountID},
		{State: third, SourceID: third.CredentialSourceAccountID},
	}
	selected := SelectDue(items, nil, now.Add(time.Minute))
	if len(selected) != 2 {
		t.Fatalf("同源候选应受 source limit 限制: %d", len(selected))
	}

	// runningSlice 非空臂。
	running := runningSlice(map[string]Running{"lease-1": {SourceID: "src"}})
	if len(running) != 1 || running[0].SourceID != "src" {
		t.Fatalf("runningSlice 应展开非空映射: %#v", running)
	}

	// boolArg 两臂。
	if boolArg(true) != "true" || boolArg(false) != "false" {
		t.Fatal("boolArg 应输出字面 true/false")
	}
}
