package gatewayclientip

import (
	"context"
	"sync"
)

// AccountConcurrencyLane values mirror AccountConcurrencyLane.
const (
	AccountConcurrencyLaneText  = "text"
	AccountConcurrencyLaneImage = "image"
)

// AccountConcurrencyReleaseEvent mirrors AccountConcurrencyReleaseEvent.
type AccountConcurrencyReleaseEvent struct {
	AccountID string
	Lane      string
}

// AccountConcurrencySource mirrors the shared/account-concurrency.ts surface
// the high-concurrency group queue consumes. The total-lane read matches the
// G10 seam gatewayruntimecache.ConcurrencySource.
type AccountConcurrencySource interface {
	// LoadAccountCurrentConcurrencyByID mirrors
	// loadAccountCurrentConcurrencyByIdsAsync(ids) (lane "" = total).
	LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error)
	// LoadAccountCurrentConcurrencyByLane mirrors
	// loadAccountCurrentConcurrencyByIdsAsync(ids, lane).
	LoadAccountCurrentConcurrencyByLane(ctx context.Context, accountIDs []string, lane string) (map[string]int, error)
	// CurrentAccountConcurrency mirrors getAccountCurrentConcurrency — the
	// process-local synchronous read (memory driver only).
	CurrentAccountConcurrency(accountID string, lane string) int
	// SubscribeAccountConcurrencyRelease mirrors
	// subscribeAccountConcurrencyRelease; the returned func unsubscribes.
	SubscribeAccountConcurrencyRelease(listener func(AccountConcurrencyReleaseEvent)) func()
}

// MemoryAccountConcurrency is the in-process account concurrency tracker
// behind the seam: acquire/release slots with total + lane counters and
// release notifications (Node tryAcquire/releaseAccountConcurrency local
// behavior).
type MemoryAccountConcurrency struct {
	clock Clock

	mu        sync.Mutex
	total     map[string]int
	byLane    map[string]int
	listeners []*listenerHandle
}

// NewMemoryAccountConcurrency builds the tracker.
func NewMemoryAccountConcurrency(clock Clock) *MemoryAccountConcurrency {
	if clock == nil {
		clock = systemClock()
	}
	return &MemoryAccountConcurrency{
		clock:  clock,
		total:  map[string]int{},
		byLane: map[string]int{},
	}
}

// Acquire mirrors tryAcquireAccountConcurrency's successful local path.
func (m *MemoryAccountConcurrency) Acquire(accountID string, lane string) bool {
	if lane != AccountConcurrencyLaneImage {
		lane = AccountConcurrencyLaneText
	}
	m.mu.Lock()
	m.total[accountID] += 1
	m.byLane[accountID+":"+lane] += 1
	m.mu.Unlock()
	return true
}

// Release mirrors releaseAccountConcurrency: decrement both counters and
// notify the release listeners (listener failures are swallowed).
func (m *MemoryAccountConcurrency) Release(accountID string, lane string) {
	if lane != AccountConcurrencyLaneImage {
		lane = AccountConcurrencyLaneText
	}
	m.mu.Lock()
	if m.total[accountID] <= 1 {
		delete(m.total, accountID)
	} else {
		m.total[accountID] -= 1
	}
	laneKey := accountID + ":" + lane
	if m.byLane[laneKey] <= 1 {
		delete(m.byLane, laneKey)
	} else {
		m.byLane[laneKey] -= 1
	}
	listeners := append([]*listenerHandle(nil), m.listeners...)
	m.mu.Unlock()
	for _, listener := range listeners {
		func() {
			defer func() { _ = recover() }()
			listener.fn(AccountConcurrencyReleaseEvent{AccountID: accountID, Lane: lane})
		}()
	}
}

// LoadAccountCurrentConcurrencyByID implements AccountConcurrencySource.
func (m *MemoryAccountConcurrency) LoadAccountCurrentConcurrencyByID(_ context.Context, accountIDs []string) (map[string]int, error) {
	return m.loadBy(accountIDs, ""), nil
}

// LoadAccountCurrentConcurrencyByLane implements AccountConcurrencySource.
func (m *MemoryAccountConcurrency) LoadAccountCurrentConcurrencyByLane(_ context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return m.loadBy(accountIDs, lane), nil
}

func (m *MemoryAccountConcurrency) loadBy(accountIDs []string, lane string) map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]int, len(accountIDs))
	seen := map[string]bool{}
	for _, accountID := range accountIDs {
		if accountID == "" || seen[accountID] {
			continue
		}
		seen[accountID] = true
		if lane == "" {
			result[accountID] = maxInt(0, m.total[accountID])
		} else {
			result[accountID] = maxInt(0, m.byLane[accountID+":"+lane])
		}
	}
	return result
}

// CurrentAccountConcurrency implements AccountConcurrencySource.
func (m *MemoryAccountConcurrency) CurrentAccountConcurrency(accountID string, lane string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lane == "" {
		return maxInt(0, m.total[accountID])
	}
	return maxInt(0, m.byLane[accountID+":"+lane])
}

// listenerHandle wraps one subscription so unsubscribe can compare identity.
type listenerHandle struct {
	fn func(AccountConcurrencyReleaseEvent)
}

// SubscribeAccountConcurrencyRelease implements AccountConcurrencySource.
func (m *MemoryAccountConcurrency) SubscribeAccountConcurrencyRelease(listener func(AccountConcurrencyReleaseEvent)) func() {
	handle := &listenerHandle{fn: listener}
	m.mu.Lock()
	m.listeners = append(m.listeners, handle)
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		for i, candidate := range m.listeners {
			if candidate == handle {
				m.listeners = append(m.listeners[:i], m.listeners[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
	}
}
