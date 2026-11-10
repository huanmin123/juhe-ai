package gatewaydispatch

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// fixedPollConcurrencyStore 前 succeedAt-1 次返回账户并发满载，之后放行。
type fixedPollConcurrencyStore struct {
	calls     int
	succeedAt int
}

func (s *fixedPollConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *fixedPollConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *fixedPollConcurrencyStore) LoadCurrentRPMAsync(context.Context, []string) (map[string]AccountRPMState, error) {
	return map[string]AccountRPMState{}, nil
}

func (s *fixedPollConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.calls++
	if s.calls < s.succeedAt {
		return ConcurrencySlot{Acquired: false, Current: 5, Limit: 5}, nil
	}
	return ConcurrencySlot{Acquired: true, Current: 1, Limit: 5, Release: func() {}, MarkFirstOutput: func() {}}, nil
}

// rpmPollConcurrencyStore 首次返回 RPM 满（resetAtMs 落在预算内/外），之后放行。
type rpmPollConcurrencyStore struct {
	calls        int
	resetAtMs    int64
	succeedAfter int
}

func (s *rpmPollConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *rpmPollConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *rpmPollConcurrencyStore) LoadCurrentRPMAsync(context.Context, []string) (map[string]AccountRPMState, error) {
	return map[string]AccountRPMState{}, nil
}

func (s *rpmPollConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.calls++
	if s.calls <= s.succeedAfter {
		return ConcurrencySlot{Acquired: false, Current: 0, Limit: 5, RPMCurrent: 2, RPMLimit: 2, RPMResetAtMs: s.resetAtMs}, nil
	}
	return ConcurrencySlot{Acquired: true, Current: 1, Limit: 5, Release: func() {}, MarkFirstOutput: func() {}}, nil
}

type delayedReleaseConcurrencyStore struct {
	start        time.Time
	unblockAfter time.Duration
}

func (s *delayedReleaseConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *delayedReleaseConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *delayedReleaseConcurrencyStore) LoadCurrentRPMAsync(context.Context, []string) (map[string]AccountRPMState, error) {
	return map[string]AccountRPMState{}, nil
}

func (s *delayedReleaseConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	if time.Since(s.start) < s.unblockAfter {
		return ConcurrencySlot{Acquired: false, Current: 5, Limit: 5}, nil
	}
	return ConcurrencySlot{Acquired: true, Current: 1, Limit: 5, Release: func() {}, MarkFirstOutput: func() {}}, nil
}

func TestDispatchOwnerWaitUsesSharedBudgetBeyondGeneric(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	store := &delayedReleaseConcurrencyStore{start: time.Now(), unblockAfter: 1400 * time.Millisecond}
	engine.Concurrency = store
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.SessionAffinityOwnerAccountID = "a-1"
	args.SessionAffinityWaitPlanned = true
	args.SessionAffinityWaitRemainingMs = 2000
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{})

	started := time.Now()
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s, want owner a-1", result.Account.ID)
	}
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("owner wait = %v, want beyond generic 1200ms budget", elapsed)
	}
}

func TestFetchSessionAffinityOwnerWaitBudgetPointerIsConsumed(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	store := &delayedReleaseConcurrencyStore{start: time.Now(), unblockAfter: 600 * time.Millisecond}
	engine.Concurrency = store
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.SessionAffinityOwnerAccountID = "a-1"
	args.SessionAffinityWaitPlanned = true
	args.SessionAffinityWaitRemainingMs = 400
	budget := int64(400)
	args.SessionAffinityOwnerWaitBudgetMs = &budget
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{})

	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), args); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if budget != 0 {
		t.Fatalf("owner wait budget = %d, want consumed to 0", budget)
	}
}

func TestSessionAffinityOwnerWaitBudgetMs(t *testing.T) {
	if got := sessionAffinityOwnerWaitBudgetMs(2000, true, 1400); got != 1400 {
		t.Fatalf("slot-remaining budget = %d, want 1400", got)
	}
	if got := sessionAffinityOwnerWaitBudgetMs(2000, false, 0); got != 2000 {
		t.Fatalf("unplanned budget = %d, want 2000", got)
	}
	if got := sessionAffinityOwnerWaitBudgetMs(0, true, 2000); got != 0 {
		t.Fatalf("disabled budget = %d, want 0", got)
	}
	budget := int64(2000)
	consumeSessionAffinityOwnerWaitBudget(&budget, 600)
	consumeSessionAffinityOwnerWaitBudget(&budget, 5000)
	if budget != 0 {
		t.Fatalf("remaining budget = %d, want 0", budget)
	}
	consumeSessionAffinityOwnerWaitBudget(nil, 100) // must not panic
}

func TestAcquireOwnerWaitFixedPolling(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &fixedPollConcurrencyStore{succeedAt: 2}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, remaining, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 5, 0, 600, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget, 200,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !slot.Acquired {
		t.Fatal("second probe must acquire the slot")
	}
	if waitedMs != 200 || remaining != 400 {
		t.Fatalf("waited=%d remaining=%d, want 200/400 (fixed 200ms poll)", waitedMs, remaining)
	}
	if store.calls != 2 {
		t.Fatalf("calls = %d, want 2", store.calls)
	}
}

func TestAcquireOwnerWaitBudgetShortensLastPoll(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &fixedPollConcurrencyStore{succeedAt: 99}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, remaining, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 5, 0, 250, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget, 200,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if slot.Acquired {
		t.Fatal("slot must stay exhausted")
	}
	if waitedMs != 250 || remaining != 0 {
		t.Fatalf("waited=%d remaining=%d, want 250/0", waitedMs, remaining)
	}
	if store.calls != 3 {
		t.Fatalf("calls = %d, want 3 (initial + 200ms + 50ms)", store.calls)
	}
}

func TestAcquireOwnerWaitWaitsForRPMResetWithinBudget(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &rpmPollConcurrencyStore{resetAtMs: gatewayupstream.NowMs() + 100, succeedAfter: 1}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, remaining, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 5, 2, 400, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget, 200,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !slot.Acquired || waitedMs != 200 || remaining != 200 {
		t.Fatalf("slot.acquired=%v waited=%d remaining=%d, want true/200/200", slot.Acquired, waitedMs, remaining)
	}
}

func TestAcquireOwnerWaitSkipsRPMResetBeyondBudget(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &rpmPollConcurrencyStore{resetAtMs: gatewayupstream.NowMs() + 10_000, succeedAfter: 99}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, remaining, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 5, 2, 400, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget, 200,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if slot.Acquired || waitedMs != 0 || remaining != 400 {
		t.Fatalf("slot.acquired=%v waited=%d remaining=%d, want false/0/400", slot.Acquired, waitedMs, remaining)
	}
	if store.calls != 1 {
		t.Fatalf("calls = %d, want 1 (reset outside budget must not wait)", store.calls)
	}
}
