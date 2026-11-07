package jobleader

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

type fakeLeaseStore struct {
	mu       sync.Mutex
	clock    *fakeClock
	heldBy   string
	until    time.Time
	renewOK  bool
	renewErr error
	released int
	acquireN int
}

func newFakeLeaseStore(clock *fakeClock) *fakeLeaseStore {
	return &fakeLeaseStore{clock: clock, renewOK: true}
}

func (s *fakeLeaseStore) AcquireLease(_ context.Context, input taskruns.LeaseAcquireInput) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireN++
	now := s.clock.Now().UTC()
	if input.Now != nil {
		now = input.Now.UTC()
	}
	if s.heldBy != "" && now.Before(s.until) {
		return false, nil
	}
	s.heldBy = input.OwnerID
	s.until = input.LeaseUntil
	return true, nil
}

func (s *fakeLeaseStore) RenewLease(_ context.Context, leaseKey, ownerID string, leaseUntil time.Time, now *time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heldBy != ownerID {
		return false, nil
	}
	if s.renewErr != nil {
		return false, s.renewErr
	}
	if !s.renewOK {
		return false, nil
	}
	_ = leaseKey
	_ = now
	s.until = leaseUntil
	return true, nil
}

func (s *fakeLeaseStore) ReleaseLease(_ context.Context, _ string, ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heldBy == ownerID {
		s.heldBy = ""
		s.until = time.Time{}
		s.released++
	}
	return nil
}

func (s *fakeLeaseStore) owner() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldBy
}

func (s *fakeLeaseStore) setRenewOK(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewOK = ok
}

func newTestGate(t *testing.T, store LeaseStore, ownerID string, now func() time.Time) *Gate {
	t.Helper()
	gate, err := New(Options{
		Store:         store,
		OwnerID:       ownerID,
		TTL:           2 * time.Second,
		RetryInterval: 10 * time.Millisecond,
		RenewInterval: 20 * time.Millisecond,
		Now:           now,
	})
	if err != nil {
		t.Fatalf("New gate: %v", err)
	}
	return gate
}

func TestGateAcquireWaitsThenTakeover(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000, 0)}
	store := newFakeLeaseStore(clock)
	gateA := newTestGate(t, store, "owner-a", clock.Now)
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	gateA.Start(ctxA)
	if err := gateA.Wait(ctxA); err != nil {
		t.Fatalf("A wait: %v", err)
	}
	if !gateA.IsLeader() || store.owner() != "owner-a" {
		t.Fatalf("A must be leader: leader=%v owner=%q", gateA.IsLeader(), store.owner())
	}

	gateB := newTestGate(t, store, "owner-b", clock.Now)
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	gateB.Start(ctxB)
	waitCtx, waitCancel := context.WithTimeout(ctxB, 80*time.Millisecond)
	defer waitCancel()
	if err := gateB.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("B must stay standby while A holds lease: %v", err)
	}

	cancelA()
	select {
	case <-gateA.Done():
	case <-time.After(time.Second):
		t.Fatal("A gate did not stop after cancel")
	}
	waitCtx2, waitCancel2 := context.WithTimeout(ctxB, 2*time.Second)
	defer waitCancel2()
	if err := gateB.Wait(waitCtx2); err != nil {
		t.Fatalf("B takeover: %v", err)
	}
	if store.owner() != "owner-b" {
		t.Fatalf("owner after takeover = %q, want owner-b", store.owner())
	}
}

func TestGateLostCancelsComponent(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000, 0)}
	store := newFakeLeaseStore(clock)
	gate := newTestGate(t, store, "owner-a", clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate.Start(ctx)
	if err := gate.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	started := make(chan struct{})
	componentDone := make(chan error, 1)
	go func() {
		componentDone <- gate.RunComponent(ctx, func(componentCtx context.Context) error {
			close(started)
			<-componentCtx.Done()
			return componentCtx.Err()
		})
	}()
	<-started
	store.setRenewOK(false)

	select {
	case err := <-componentDone:
		if !errors.Is(err, ErrLeaderLost) {
			t.Fatalf("component error = %v, want ErrLeaderLost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("component was not canceled after lease loss")
	}
	if gate.IsLeader() {
		t.Fatal("gate must not report leader after loss")
	}
}

func TestGateReleaseOnShutdown(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000, 0)}
	store := newFakeLeaseStore(clock)
	gate := newTestGate(t, store, "owner-a", clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	gate.Start(ctx)
	if err := gate.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	cancel()
	select {
	case <-gate.Done():
	case <-time.After(time.Second):
		t.Fatal("gate did not stop")
	}
	if store.owner() != "" {
		t.Fatalf("lease owner after shutdown = %q, want released", store.owner())
	}
}

func TestUniqueOwnerIDIsProcessUnique(t *testing.T) {
	first, err := UniqueOwnerID("juhe-ai-jobs")
	if err != nil {
		t.Fatal(err)
	}
	second, err := UniqueOwnerID("juhe-ai-jobs")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "juhe-ai-jobs#") {
		t.Fatalf("owner IDs = %q / %q", first, second)
	}
	if _, err := UniqueOwnerID("  "); err == nil {
		t.Fatal("empty instance ID must fail")
	}
}
