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
// the dispatch capacity/busy check consumes. The total-lane read matches the
// G10 seam gatewayruntimecache.ConcurrencySource.
type AccountConcurrencySource interface {
	// LoadAccountCurrentConcurrencyByID mirrors
	// loadAccountCurrentConcurrencyByIdsAsync(ids) (lane "" = total).
	LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error)
	// LoadAccountCurrentConcurrencyByLane mirrors
	// loadAccountCurrentConcurrencyByIdsAsync(ids, lane).
	LoadAccountCurrentConcurrencyByLane(ctx context.Context, accountIDs []string, lane string) (map[string]int, error)
	// CurrentAccountConcurrency 返回单账户并发快照（同步读面：内存驱动直接
	// 读 map；Redis 驱动清理过期槽后 ZCARD，失败按 0 降级）。
	CurrentAccountConcurrency(accountID string, lane string) int
	// LoadAccountRPMByID 批量读取账户 RPM 窗口计数（AI 账户每分钟请求上限
	// 设计 §5.2：派发容量/忙判定消费）。
	LoadAccountRPMByID(ctx context.Context, accountIDs []string) (map[string]AccountRPMState, error)
	// CurrentAccountRPM 返回单账户 RPM 窗口快照（本地驱动的同步读取，与
	// CurrentAccountConcurrency 同层；Redis 驱动读独立 RPM HASH）。
	CurrentAccountRPM(accountID string) AccountRPMState
	// SubscribeAccountConcurrencyRelease mirrors
	// subscribeAccountConcurrencyRelease; the returned func unsubscribes.
	SubscribeAccountConcurrencyRelease(listener func(AccountConcurrencyReleaseEvent)) func()
}

// AccountRPMState 是账户 RPM 运行态（每分钟请求数计数）的单账户快照：
// Used 为本自然分钟窗口内已派发的请求次数，Limit 是账户配置上限（tracker
// 只承载计数事实，不携带配置，读取面恒为 0，由消费方按 accounts 投影补齐），
// ResetAtMs 是当前窗口结束时刻（Unix 毫秒）。
type AccountRPMState struct {
	Used      int
	Limit     int
	ResetAtMs int64
}

// MemoryAccountConcurrency is the in-process account concurrency tracker
// behind the seam: acquire/release slots with total + lane counters and
// release notifications (Node tryAcquire/releaseAccountConcurrency local
// behavior). RPM（AI 账户每分钟请求上限设计 §4）与并发共用同一互斥临界区：
// rpmBucket/rpmCount 按自然分钟固定窗口计数，只随成功占位或同一并发槽内的
// 后续真实出站消费自增，Release 不回退。
type MemoryAccountConcurrency struct {
	clock Clock

	mu        sync.Mutex
	total     map[string]int
	byLane    map[string]int
	rpmBucket map[string]int64
	rpmCount  map[string]int
	listeners []*listenerHandle
}

// NewMemoryAccountConcurrency builds the tracker.
func NewMemoryAccountConcurrency(clock Clock) *MemoryAccountConcurrency {
	if clock == nil {
		clock = systemClock()
	}
	return &MemoryAccountConcurrency{
		clock:     clock,
		total:     map[string]int{},
		byLane:    map[string]int{},
		rpmBucket: map[string]int64{},
		rpmCount:  map[string]int{},
	}
}

// AccountConcurrencyAcquireOutcome 是一次原子占位尝试的观测结果：Acquired
// 表示是否占位成功；Total 与 LaneCurrent 是本次尝试时刻的账户总量与归一化
// lane 计数（成功时为占位后的值，拒绝时保持原值、两个计数都不自增）。
// RPMCurrent/RPMLimit/RPMResetAtMs 是同一时刻的 RPM 窗口观测（设计 §4.3）：
// 供错误文案、终态 Retry-After 与审计使用，拒绝路径同样回带。
type AccountConcurrencyAcquireOutcome struct {
	Acquired     bool
	Total        int
	LaneCurrent  int
	RPMCurrent   int
	RPMLimit     int
	RPMResetAtMs int64
	// Token 是 Redis 驱动本次占位的唯一槽标识（release/refresh 凭它定位
	// ZSET 成员）；内存驱动为空。
	Token string
}

// rpmWindowResetAtMs 返回 RPM 窗口结束时刻（自然分钟固定窗口，设计 §4.2）。
// 账户 RPM 计数的资源 ID 与并发同键（账户级总量，跨 lane 与 API Key 统一）；
// 授权实例的资源 ID 归一（落来源账户 ID）由生产调用方 chainConcurrencyStore
// 完成。
func rpmWindowResetAtMs(bucket int64) int64 {
	return (bucket + 1) * 60_000
}

// TryAcquire 在单个互斥临界区内完成“账户总量 + lane + RPM”三重校验并占位，
// 恢复 Node tryAcquireAccountConcurrency 的拒绝语义并叠加 RPM 窗口判定（设计
// §4.3）：并发检查在前、RPM 在后，任一计数达到上限即拒绝且不自增，通过才
// 同时自增 total、lane 与 rpmCount 并返回成功。totalLimit/laneLimit/rpmLimit
// <= 0 视为不设限（与 dispatch 侧“上限>0 才拒绝”的既有语义一致）；lane 非
// image 归入 text。RPM 是默认不启用的可选限额：rpmLimit <= 0 时观测字段保持
// 0 值且不触碰 RPM map（未配置账户热路径零开销；消费方按 RPMLimit > 0 判定，
// 不受影响），仅 rpmLimit > 0 时按 bucket = floor(nowMs/60000) 就地惰性重置。
// 检查与占用同锁执行，消除“先读后判再 Acquire”两步逻辑的并发超卖窗口。
func (m *MemoryAccountConcurrency) TryAcquire(accountID string, totalLimit int, lane string, laneLimit int, rpmLimit int) AccountConcurrencyAcquireOutcome {
	if lane != AccountConcurrencyLaneImage {
		lane = AccountConcurrencyLaneText
	}
	var rpmBucket int64
	var rpmCurrent int
	var rpmResetAtMs int64
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.total[accountID]
	laneCurrent := m.byLane[accountID+":"+lane]
	if rpmLimit > 0 {
		rpmBucket = m.clock.Now().UnixMilli() / 60_000
		rpmCurrent = m.normalizeRPMWindowLocked(accountID, rpmBucket)
		rpmResetAtMs = rpmWindowResetAtMs(rpmBucket)
	}
	if totalLimit > 0 && total >= totalLimit {
		return AccountConcurrencyAcquireOutcome{Total: total, LaneCurrent: laneCurrent, RPMCurrent: rpmCurrent, RPMLimit: rpmLimit, RPMResetAtMs: rpmResetAtMs}
	}
	if laneLimit > 0 && laneCurrent >= laneLimit {
		return AccountConcurrencyAcquireOutcome{Total: total, LaneCurrent: laneCurrent, RPMCurrent: rpmCurrent, RPMLimit: rpmLimit, RPMResetAtMs: rpmResetAtMs}
	}
	if rpmLimit > 0 && rpmCurrent >= rpmLimit {
		return AccountConcurrencyAcquireOutcome{Total: total, LaneCurrent: laneCurrent, RPMCurrent: rpmCurrent, RPMLimit: rpmLimit, RPMResetAtMs: rpmResetAtMs}
	}
	m.total[accountID] = total + 1
	m.byLane[accountID+":"+lane] = laneCurrent + 1
	outcomeRPMCurrent := 0
	if rpmLimit > 0 {
		m.rpmCount[accountID] = rpmCurrent + 1
		m.rpmBucket[accountID] = rpmBucket
		outcomeRPMCurrent = rpmCurrent + 1
	}
	return AccountConcurrencyAcquireOutcome{Acquired: true, Total: total + 1, LaneCurrent: laneCurrent + 1, RPMCurrent: outcomeRPMCurrent, RPMLimit: rpmLimit, RPMResetAtMs: rpmResetAtMs}
}

// ConsumeRPM 在同一账户 RPM 窗口内原子消费 1 次（T161）：首次占位已由
// TryAcquire 计数，同一并发槽内的后续真实上游尝试在出站前调用本方法。窗口
// 判定/惰性重置与 TryAcquire 共用 normalizeRPMWindowLocked；达到上限返回
// allowed=false 并回带当前计数与窗口结束时刻，不修改任何计数。
func (m *MemoryAccountConcurrency) ConsumeRPM(_ context.Context, accountID string, rpmLimit int) (bool, int, int64, error) {
	if rpmLimit <= 0 {
		return true, 0, 0, nil
	}
	bucket := m.clock.Now().UnixMilli() / 60_000
	resetAtMs := rpmWindowResetAtMs(bucket)
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.normalizeRPMWindowLocked(accountID, bucket)
	if current >= rpmLimit {
		return false, current, resetAtMs, nil
	}
	m.rpmCount[accountID] = current + 1
	m.rpmBucket[accountID] = bucket
	return true, current + 1, resetAtMs, nil
}

// normalizeRPMWindowLocked 是判定路径的就地惰性重置（设计 §4.2：判定时发现
// rpmBucket != 当前 bucket，视为 count = 0 并就地重置；仅在 rpmLimit > 0 时
// 进入）。持锁调用。
func (m *MemoryAccountConcurrency) normalizeRPMWindowLocked(accountID string, bucket int64) int {
	if m.rpmBucket[accountID] != bucket {
		m.rpmBucket[accountID] = bucket
		m.rpmCount[accountID] = 0
	}
	return m.rpmCount[accountID]
}

// peekRPMWindowLocked 是读取面的窗口观察（设计 §4.2 读取惰性重置的清理变体）：
// 无条目直接返回 0 且不写入（未配置账户零开销、map 不随读取增长）；有条目且
// bucket 过期时删除两个条目并返回 0（惰性清理，避免 map 无限留存）。持锁调用。
func (m *MemoryAccountConcurrency) peekRPMWindowLocked(accountID string, bucket int64) int {
	storedBucket, hasEntry := m.rpmBucket[accountID]
	if !hasEntry {
		return 0
	}
	if storedBucket != bucket {
		delete(m.rpmBucket, accountID)
		delete(m.rpmCount, accountID)
		return 0
	}
	return m.rpmCount[accountID]
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

// LoadAccountRPMByID 按账户资源 ID 批量读取 RPM 窗口计数（派发容量判定的
// 异步 ready 判定与列表运行态 overlay 消费）。无条目按 0 返回且不写入；窗口
// 已滚动的账户按 0 返回并删除两个条目（读取面惰性清理，设计 §4.2）。
func (m *MemoryAccountConcurrency) LoadAccountRPMByID(_ context.Context, accountIDs []string) (map[string]AccountRPMState, error) {
	bucket := m.clock.Now().UnixMilli() / 60_000
	resetAtMs := rpmWindowResetAtMs(bucket)
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]AccountRPMState, len(accountIDs))
	seen := map[string]bool{}
	for _, accountID := range accountIDs {
		if accountID == "" || seen[accountID] {
			continue
		}
		seen[accountID] = true
		result[accountID] = AccountRPMState{Used: m.peekRPMWindowLocked(accountID, bucket), ResetAtMs: resetAtMs}
	}
	return result, nil
}

// CurrentAccountRPM 返回单账户 RPM 窗口快照（本地驱动的同步读取，容量判定
// 的 immediate/wake 判定消费）。无条目按 0 返回且不写入；窗口已滚动时按 0
// 返回并删除两个条目（惰性清理）。
func (m *MemoryAccountConcurrency) CurrentAccountRPM(accountID string) AccountRPMState {
	bucket := m.clock.Now().UnixMilli() / 60_000
	m.mu.Lock()
	defer m.mu.Unlock()
	return AccountRPMState{Used: m.peekRPMWindowLocked(accountID, bucket), ResetAtMs: rpmWindowResetAtMs(bucket)}
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
