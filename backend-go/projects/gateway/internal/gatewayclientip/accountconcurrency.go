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

// AccountConcurrencyAcquireOutcome 是一次原子占位尝试的观测结果：Acquired
// 表示是否占位成功；Total 与 LaneCurrent 是本次尝试时刻的账户总量与归一化
// lane 计数（成功时为占位后的值，拒绝时保持原值、两个计数都不自增）。
type AccountConcurrencyAcquireOutcome struct {
	Acquired    bool
	Total       int
	LaneCurrent int
}

// TryAcquire 在单个互斥临界区内完成“账户总量 + lane”双重校验并占位，恢复
// Node tryAcquireAccountConcurrency 的拒绝语义（current >= limit ||
// laneCurrent >= laneLimit 即拒绝）：任一计数达到上限即拒绝且不自增，通过才
// 同时自增 total 与 lane 并返回成功。totalLimit/laneLimit <= 0 视为不设限
// （与 dispatch 侧“上限>0 才拒绝”的既有语义一致）；lane 非 image 归入 text。
// 检查与占用同锁执行，消除“先读后判再 Acquire”两步逻辑的并发超卖窗口。
func (m *MemoryAccountConcurrency) TryAcquire(accountID string, totalLimit int, lane string, laneLimit int) AccountConcurrencyAcquireOutcome {
	if lane != AccountConcurrencyLaneImage {
		lane = AccountConcurrencyLaneText
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.total[accountID]
	laneCurrent := m.byLane[accountID+":"+lane]
	if totalLimit > 0 && total >= totalLimit {
		return AccountConcurrencyAcquireOutcome{Total: total, LaneCurrent: laneCurrent}
	}
	if laneLimit > 0 && laneCurrent >= laneLimit {
		return AccountConcurrencyAcquireOutcome{Total: total, LaneCurrent: laneCurrent}
	}
	m.total[accountID] = total + 1
	m.byLane[accountID+":"+lane] = laneCurrent + 1
	return AccountConcurrencyAcquireOutcome{Acquired: true, Total: total + 1, LaneCurrent: laneCurrent + 1}
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
