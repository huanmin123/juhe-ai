package keymodelruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// wkFakeRecoveryStore 记录 runner 的调用序列，用于断言恢复循环语义。
type wkFakeRecoveryStore struct {
	mu sync.Mutex

	now      time.Time
	nowErr   error
	nowCalls int

	due    []State
	dueErr error

	acquireErr    error
	acquireStatus MutationStatus
	acquired      State
	acquiredLease string
	acquireCalls  int
	acquireCont   []bool
	acquireSrc    []bool
	acquirePhase  []Phase
	acquireSignal chan struct{}

	renewOK  bool
	renewErr error

	commitStatus MutationStatus
	commitErr    error
	committed    []string
	commitLeases chan string
}

func (s *wkFakeRecoveryStore) ServerNow(context.Context) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowCalls++
	if s.nowErr != nil {
		return time.Time{}, s.nowErr
	}
	return s.now, nil
}

func (s *wkFakeRecoveryStore) ListDue(context.Context, time.Time, int) ([]State, error) {
	return s.due, s.dueErr
}

func (s *wkFakeRecoveryStore) AcquireRecovery(_ context.Context, candidate State, leaseID string, continuation, sourceContinuation bool) (State, MutationStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls++
	s.acquiredLease = leaseID
	s.acquireCont = append(s.acquireCont, continuation)
	s.acquireSrc = append(s.acquireSrc, sourceContinuation)
	s.acquirePhase = append(s.acquirePhase, candidate.Phase)
	if s.acquireSignal != nil {
		s.acquireSignal <- struct{}{}
	}
	if s.acquireErr != nil {
		return State{}, "", s.acquireErr
	}
	if s.acquireStatus != StatusApplied {
		return candidate, s.acquireStatus, nil
	}
	return s.acquired, StatusApplied, nil
}

func (s *wkFakeRecoveryStore) RenewRecovery(context.Context, State, string) (bool, error) {
	return s.renewOK, s.renewErr
}

func (s *wkFakeRecoveryStore) CommitRecovery(_ context.Context, _, next State, leaseID string) (MutationStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.committed = append(s.committed, leaseID+":"+string(next.Phase)+":"+string(next.LastOutcome))
	if s.commitErr != nil {
		return "", s.commitErr
	}
	if s.commitLeases != nil {
		s.commitLeases <- leaseID
	}
	return s.commitStatus, nil
}

type wkFakeLoader struct {
	inputs []RecoveryInput
	err    error
}

func (l *wkFakeLoader) Load(context.Context, string) ([]RecoveryInput, error) {
	return l.inputs, l.err
}

func TestNewRunnerValidation(t *testing.T) {
	store := &wkFakeRecoveryStore{}
	if _, err := NewRunner(nil, &wkFakeLoader{}, nil); err == nil {
		t.Fatal("nil store 必须被拒绝")
	}
	if _, err := NewRunner(store, nil, nil); err == nil {
		t.Fatal("nil loader 必须被拒绝")
	}
	// probe 缺省时必须回退为 unknown 结果。
	runner, err := NewRunner(store, &wkFakeLoader{}, nil)
	if err != nil || runner == nil {
		t.Fatalf("默认 probe 构造: %v", err)
	}
}

func TestRunCycleReportsStoreErrors(t *testing.T) {
	store := &wkFakeRecoveryStore{nowErr: errors.New("clock unavailable")}
	runner, err := NewRunner(store, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.RunCycle(context.Background()); err == nil || !strings.Contains(err.Error(), "read key-model Redis time") {
		t.Fatalf("now 错误未被包裹: %v", err)
	}
	store.nowErr = nil
	store.dueErr = errors.New("zrange failed")
	if err := runner.RunCycle(context.Background()); err == nil || !strings.Contains(err.Error(), "list key-model due state") {
		t.Fatalf("due 错误未被包裹: %v", err)
	}
}

func TestRunCycleContinuationFlagsAndLeaseIdentity(t *testing.T) {
	// 契约：RECOVERING 候选必须触发 continuation 标记，且同源候选携带源级续跑标记。
	now := time.UnixMilli(5000).UTC()
	openState, err := Open(testCapability(), now)
	if err != nil {
		t.Fatal(err)
	}
	otherCapability := testCapability()
	otherCapability.KeyFingerprint = "key-2"
	otherCapability.CredentialSourceAccountID = "source-2"
	recoveringState, err := Open(otherCapability, now)
	if err != nil {
		t.Fatal(err)
	}
	recoveringState.Phase = PhaseRecovering
	recoveringState.RecoverySuccessCount = 1
	store := &wkFakeRecoveryStore{now: now, due: []State{openState, recoveringState}, acquireStatus: StatusNotDue, acquireSignal: make(chan struct{}, 2)}
	loader := &wkFakeLoader{}
	runner, err := NewRunner(store, loader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 等待两个候选的 acquire 都已发生（goroutine 同步点）。
	<-store.acquireSignal
	<-store.acquireSignal
	if store.acquireCalls != 2 {
		t.Fatalf("acquire calls=%d", store.acquireCalls)
	}
	if store.acquiredLease == "" || !strings.HasPrefix(store.acquiredLease, "gkmr-") {
		t.Fatalf("lease id 前缀不符: %q", store.acquiredLease)
	}
	// continuation 是批级标记：批内任一 RECOVERING 即整批续跑；
	// goroutine 完成顺序不定，按相位配对断言。
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.acquirePhase) != 2 {
		t.Fatalf("acquire 次数=%d", len(store.acquirePhase))
	}
	for i, phase := range store.acquirePhase {
		if !store.acquireCont[i] {
			t.Fatalf("批内含 RECOVERING 时必须整批 continuation: index=%d", i)
		}
		if phase == PhaseRecovering && !store.acquireSrc[i] {
			t.Fatalf("RECOVERING 候选必须触发 source continuation: index=%d", i)
		}
		if phase == PhaseOpen && store.acquireSrc[i] {
			t.Fatalf("OPEN 候选不应触发 source continuation: index=%d", i)
		}
	}
}

func TestRunCandidateSettlementPaths(t *testing.T) {
	now := time.UnixMilli(20_000).UTC()
	state, err := Open(testCapability(), now)
	if err != nil {
		t.Fatal(err)
	}
	halfOpen := state
	halfOpen.Phase = PhaseHalfOpen
	halfOpen.ProbeLease = &Lease{ID: "lease-1", Until: now.Add(time.Minute)}

	newStore := func() *wkFakeRecoveryStore {
		return &wkFakeRecoveryStore{now: now, acquireStatus: StatusApplied, acquired: halfOpen}
	}

	t.Run("loader error settles unknown", func(t *testing.T) {
		store := newStore()
		store.commitLeases = make(chan string, 1)
		runner, err := NewRunner(store, &wkFakeLoader{err: errors.New("decrypt failed")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		runner.runCandidate(context.Background(), state, "lease-1", false, false)
		committed := <-store.commitLeases
		if committed != "lease-1" {
			t.Fatalf("commit lease=%q", committed)
		}
		if got := store.committed[0]; !strings.Contains(got, string(OutcomeUnknown)) {
			t.Fatalf("loader 失败必须按 unknown 结算: %q", got)
		}
	})

	t.Run("missing input settles unknown", func(t *testing.T) {
		store := newStore()
		store.commitLeases = make(chan string, 1)
		loader := &wkFakeLoader{inputs: []RecoveryInput{{AccountID: "other", DispatchRevision: 1}}}
		runner, err := NewRunner(store, loader, nil)
		if err != nil {
			t.Fatal(err)
		}
		runner.runCandidate(context.Background(), state, "lease-1", false, false)
		if committed := <-store.commitLeases; committed != "lease-1" {
			t.Fatalf("commit lease=%q", committed)
		}
	})

	t.Run("probe success commits outcome", func(t *testing.T) {
		store := newStore()
		store.commitLeases = make(chan string, 1)
		loader := &wkFakeLoader{inputs: []RecoveryInput{{AccountID: testCapability().CredentialSourceAccountID, DispatchRevision: testCapability().DispatchRevision}}}
		probe := func(context.Context, State, RecoveryInput) Outcome { return OutcomeCompleteSuccess }
		runner, err := NewRunner(store, loader, probe)
		if err != nil {
			t.Fatal(err)
		}
		runner.runCandidate(context.Background(), state, "lease-1", false, false)
		if committed := <-store.commitLeases; committed != "lease-1" {
			t.Fatalf("commit lease=%q", committed)
		}
		if got := store.committed[0]; !strings.Contains(got, string(OutcomeCompleteSuccess)) {
			t.Fatalf("成功结果未被提交: %q", got)
		}
	})

	t.Run("acquire failure skips probe", func(t *testing.T) {
		store := &wkFakeRecoveryStore{now: now, acquireErr: errors.New("redis down")}
		loader := &wkFakeLoader{inputs: []RecoveryInput{{AccountID: "source-1", DispatchRevision: 7}}}
		runner, err := NewRunner(store, loader, nil)
		if err != nil {
			t.Fatal(err)
		}
		runner.runCandidate(context.Background(), state, "lease-x", false, false)
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.committed) != 0 {
			t.Fatalf("acquire 失败不应提交: %v", store.committed)
		}
	})

	t.Run("lease mismatch skips commit", func(t *testing.T) {
		mismatched := halfOpen
		mismatched.ProbeLease = &Lease{ID: "someone-else", Until: now.Add(time.Minute)}
		store := &wkFakeRecoveryStore{now: now, acquireStatus: StatusApplied, acquired: mismatched}
		loader := &wkFakeLoader{inputs: []RecoveryInput{{AccountID: "source-1", DispatchRevision: 7}}}
		runner, err := NewRunner(store, loader, nil)
		if err != nil {
			t.Fatal(err)
		}
		runner.runCandidate(context.Background(), state, "lease-1", false, false)
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.committed) != 0 {
			t.Fatalf("租约不符不应提交: %v", store.committed)
		}
	})

	t.Run("observe clock failure skips commit", func(t *testing.T) {
		store := newStore()
		store.nowCalls = 1 // 第一次（RunCycle 语义上的取时）成功，结算取时失败
		store.nowErr = errors.New("clock gone")
		loader := &wkFakeLoader{inputs: []RecoveryInput{{AccountID: "source-1", DispatchRevision: 7}}}
		probe := func(context.Context, State, RecoveryInput) Outcome { return OutcomeCompleteSuccess }
		runner, err := NewRunner(store, loader, probe)
		if err != nil {
			t.Fatal(err)
		}
		runner.runCandidate(context.Background(), state, "lease-1", false, false)
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.committed) != 0 {
			t.Fatalf("取时失败不应提交: %v", store.committed)
		}
	})
}

func TestRunCycleSkipsWhenGlobalLimitReached(t *testing.T) {
	now := time.UnixMilli(1).UTC()
	state, err := Open(testCapability(), now)
	if err != nil {
		t.Fatal(err)
	}
	store := &wkFakeRecoveryStore{now: now, due: []State{state}, acquireStatus: StatusNotDue}
	runner, err := NewRunner(store, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.running = make(map[string]struct{}, RecoveryGlobalLimit)
	for i := 0; i < RecoveryGlobalLimit; i++ {
		runner.running[strconvItoa(i)] = struct{}{}
	}
	runner.mu.Unlock()
	if err := runner.RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.acquireCalls != 0 {
		t.Fatalf("全局限额必须阻断新的候选: %d", store.acquireCalls)
	}
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func TestRenewLeaseReturnsWhenDoneClosed(t *testing.T) {
	runner, err := NewRunner(&wkFakeRecoveryStore{}, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	// done 已关闭：renewLease 必须立即返回，不触碰 renew 存储。
	runner.renewLease(context.Background(), State{}, "lease", func() {}, make(chan struct{}, 1), done)
}

func TestRunStopsOnCanceledContext(t *testing.T) {
	store := &wkFakeRecoveryStore{now: time.UnixMilli(1).UTC()}
	runner, err := NewRunner(store, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后的 Run 必须返回 ctx.Err(): %v", err)
	}
}

func TestNewLeaseIDPrefix(t *testing.T) {
	id := newLeaseID()
	if !strings.HasPrefix(id, "gkmr-") {
		t.Fatalf("lease id 前缀不符: %q", id)
	}
}

func TestRunReturnsCycleErrorWhenContextAlive(t *testing.T) {
	store := &wkFakeRecoveryStore{nowErr: errors.New("clock gone")}
	runner, err := NewRunner(store, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runner.Run(ctx); err == nil || !strings.Contains(err.Error(), "read key-model Redis time") {
		t.Fatalf("Run 必须透出周期错误: %v", err)
	}
}

func TestRenewLeaseReturnsOnCanceledContext(t *testing.T) {
	runner, err := NewRunner(&wkFakeRecoveryStore{}, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	// ctx 已取消：renewLease 必须立即返回，不触发任何续期。
	runner.renewLease(ctx, State{}, "lease", func() {}, make(chan struct{}, 1), done)
}

func TestSettleUnknownSkipsOnClockFailure(t *testing.T) {
	store := &wkFakeRecoveryStore{nowErr: errors.New("clock gone")}
	runner, err := NewRunner(store, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.settleUnknown(context.Background(), State{}, "lease-1")
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.committed) != 0 {
		t.Fatalf("取时失败不得提交: %v", store.committed)
	}
}

func TestRunCycleSkipsRunningCandidates(t *testing.T) {
	now := time.UnixMilli(1).UTC()
	state, err := Open(testCapability(), now)
	if err != nil {
		t.Fatal(err)
	}
	store := &wkFakeRecoveryStore{now: now, due: []State{state}, acquireStatus: StatusNotDue, acquireSignal: make(chan struct{}, 1)}
	runner, err := NewRunner(store, &wkFakeLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 预占同 hash：RunCycle 必须跳过进行中的候选。
	runner.mu.Lock()
	runner.running[state.CapabilityHash] = struct{}{}
	runner.mu.Unlock()
	if err := runner.RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.acquireSignal:
		t.Fatal("进行中的候选不应被重复 acquire")
	default:
	}
}
