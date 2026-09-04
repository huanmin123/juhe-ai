package gatewayhotquality

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Speed-first body admission mirroring
// backend/src/modules/gateway/runtime/speed-first-body-admission.service.ts.
// Node's module-global `states` map becomes a registry; a package-level
// default registry keeps the module-level function surface. AbortSignal is a
// context.Context here; timers stay real so waitedMs uses the injected clock.

// SpeedFirstBodyAdmissionRejectReason mirrors SpeedFirstBodyAdmissionRejectReason.
type SpeedFirstBodyAdmissionRejectReason = string

// Rejection reasons (mirror the Node union verbatim).
const (
	BodyAdmissionRejectQueueDisabled   SpeedFirstBodyAdmissionRejectReason = "queue_disabled"
	BodyAdmissionRejectQueueFull       SpeedFirstBodyAdmissionRejectReason = "queue_full"
	BodyAdmissionRejectAPIKeyQueueFull SpeedFirstBodyAdmissionRejectReason = "api_key_queue_full"
	BodyAdmissionRejectTimeout         SpeedFirstBodyAdmissionRejectReason = "timeout"
	BodyAdmissionRejectAborted         SpeedFirstBodyAdmissionRejectReason = "aborted"
)

// SpeedFirstBodyAdmissionDecision mirrors SpeedFirstBodyAdmissionDecision:
// Acquired=true carries a Release func, Acquired=false carries Reason.
type SpeedFirstBodyAdmissionDecision struct {
	Acquired bool
	WaitedMs int64
	Reason   SpeedFirstBodyAdmissionRejectReason
	Release  func()
}

// SpeedFirstBodyAdmissionInput mirrors SpeedFirstBodyAdmissionInput.
type SpeedFirstBodyAdmissionInput struct {
	SystemAccountID     string
	RouteStrategyID     string
	GroupID             string
	APIKeyID            string
	Capacity            int
	MaxQueueWaitMs      int64
	MaxQueueSize        int
	PerAPIKeyQueueLimit int
}

// SpeedFirstBodyAdmissionSnapshotEntry mirrors the snapshot entry shape.
type SpeedFirstBodyAdmissionSnapshotEntry struct {
	Key      string
	Capacity int
	Active   int
	Queued   int
}

type bodyAdmissionState struct {
	key             string
	capacity        int
	active          int
	queue           []*bodyAdmissionQueueItem
	perAPIKeyQueued map[string]int
}

type bodyAdmissionQueueItem struct {
	apiKeyID     string
	enqueuedAtMs int64
	removed      bool
	timer        *time.Timer
	done         chan SpeedFirstBodyAdmissionDecision
	finished     chan struct{}
	resolveOnce  sync.Once
}

func (item *bodyAdmissionQueueItem) resolve(decision SpeedFirstBodyAdmissionDecision) {
	item.resolveOnce.Do(func() {
		item.done <- decision
		close(item.finished)
	})
}

// SpeedFirstBodyAdmissionRegistry owns the admission states for one process.
type SpeedFirstBodyAdmissionRegistry struct {
	mu     sync.Mutex
	states map[string]*bodyAdmissionState
	now    func() time.Time
}

// NewSpeedFirstBodyAdmissionRegistry builds a registry with the wall clock.
func NewSpeedFirstBodyAdmissionRegistry() *SpeedFirstBodyAdmissionRegistry {
	return &SpeedFirstBodyAdmissionRegistry{
		states: make(map[string]*bodyAdmissionState),
		now:    time.Now,
	}
}

// SetClock overrides the clock (tests inject a fake).
func (registry *SpeedFirstBodyAdmissionRegistry) SetClock(now func() time.Time) {
	if now != nil {
		registry.now = now
	}
}

var defaultBodyAdmissionRegistry = NewSpeedFirstBodyAdmissionRegistry()

// AcquireSpeedFirstBodyAdmission mirrors the module-level
// acquireSpeedFirstBodyAdmission (process-global registry).
func AcquireSpeedFirstBodyAdmission(ctx context.Context, input SpeedFirstBodyAdmissionInput) SpeedFirstBodyAdmissionDecision {
	return defaultBodyAdmissionRegistry.Acquire(ctx, input)
}

// SpeedFirstBodyAdmissionSnapshot mirrors speedFirstBodyAdmissionSnapshot
// (sorted by key for deterministic output; Node relies on insertion order).
func SpeedFirstBodyAdmissionSnapshot() []SpeedFirstBodyAdmissionSnapshotEntry {
	return defaultBodyAdmissionRegistry.Snapshot()
}

// ClearSpeedFirstBodyAdmissionsForTest mirrors clearSpeedFirstBodyAdmissionsForTest.
func ClearSpeedFirstBodyAdmissionsForTest() {
	defaultBodyAdmissionRegistry.ClearForTest()
}

// Acquire mirrors acquireSpeedFirstBodyAdmission.
func (registry *SpeedFirstBodyAdmissionRegistry) Acquire(ctx context.Context, input SpeedFirstBodyAdmissionInput) SpeedFirstBodyAdmissionDecision {
	key := bodyAdmissionKey(input)
	registry.mu.Lock()
	state, ok := registry.states[key]
	if !ok {
		state = createState(key, input.Capacity)
		registry.states[key] = state
	}
	state.capacity = bodyAdmissionPositiveInteger(input.Capacity, 1)
	registry.mu.Unlock()
	startedAtMs := registry.now().UnixMilli()
	if ctx != nil && ctx.Err() != nil {
		return SpeedFirstBodyAdmissionDecision{Acquired: false, Reason: BodyAdmissionRejectAborted, WaitedMs: 0}
	}

	registry.mu.Lock()
	if len(state.queue) == 0 && state.active < state.capacity {
		state.active++
		decision := registry.acquiredDecision(state, 0)
		registry.mu.Unlock()
		return decision
	}
	registry.mu.Unlock()

	maxQueueWaitMs := bodyAdmissionNonNegativeInteger(input.MaxQueueWaitMs, 0)
	if maxQueueWaitMs == 0 {
		return SpeedFirstBodyAdmissionDecision{Acquired: false, Reason: BodyAdmissionRejectQueueDisabled, WaitedMs: 0}
	}
	registry.mu.Lock()
	if len(state.queue) >= bodyAdmissionPositiveInteger(input.MaxQueueSize, 1) {
		registry.mu.Unlock()
		return SpeedFirstBodyAdmissionDecision{Acquired: false, Reason: BodyAdmissionRejectQueueFull, WaitedMs: 0}
	}
	apiKeyQueued := state.perAPIKeyQueued[input.APIKeyID]
	if apiKeyQueued >= bodyAdmissionPositiveInteger(input.PerAPIKeyQueueLimit, 1) {
		registry.mu.Unlock()
		return SpeedFirstBodyAdmissionDecision{Acquired: false, Reason: BodyAdmissionRejectAPIKeyQueueFull, WaitedMs: 0}
	}
	item := &bodyAdmissionQueueItem{
		apiKeyID:     input.APIKeyID,
		enqueuedAtMs: startedAtMs,
		done:         make(chan SpeedFirstBodyAdmissionDecision, 1),
		finished:     make(chan struct{}),
	}
	item.timer = time.AfterFunc(time.Duration(maxQueueWaitMs)*time.Millisecond, func() {
		registry.completeQueuedItem(state, item, SpeedFirstBodyAdmissionDecision{
			Acquired: false,
			Reason:   BodyAdmissionRejectTimeout,
			WaitedMs: registry.now().UnixMilli() - item.enqueuedAtMs,
		})
	})
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				registry.completeQueuedItem(state, item, SpeedFirstBodyAdmissionDecision{
					Acquired: false,
					Reason:   BodyAdmissionRejectAborted,
					WaitedMs: registry.now().UnixMilli() - item.enqueuedAtMs,
				})
			case <-item.finished:
			}
		}()
	}
	state.queue = append(state.queue, item)
	state.perAPIKeyQueued[input.APIKeyID] = apiKeyQueued + 1
	registry.mu.Unlock()

	decision := <-item.done
	return decision
}

// Snapshot mirrors speedFirstBodyAdmissionSnapshot.
func (registry *SpeedFirstBodyAdmissionRegistry) Snapshot() []SpeedFirstBodyAdmissionSnapshotEntry {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entries := make([]SpeedFirstBodyAdmissionSnapshotEntry, 0, len(registry.states))
	keys := make([]string, 0, len(registry.states))
	for key := range registry.states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		state := registry.states[key]
		entries = append(entries, SpeedFirstBodyAdmissionSnapshotEntry{
			Key:      state.key,
			Capacity: state.capacity,
			Active:   state.active,
			Queued:   len(state.queue),
		})
	}
	return entries
}

// ClearForTest mirrors clearSpeedFirstBodyAdmissionsForTest.
func (registry *SpeedFirstBodyAdmissionRegistry) ClearForTest() {
	registry.mu.Lock()
	for _, state := range registry.states {
		for _, item := range append([]*bodyAdmissionQueueItem{}, state.queue...) {
			registry.completeQueuedItemLocked(state, item, SpeedFirstBodyAdmissionDecision{
				Acquired: false,
				Reason:   BodyAdmissionRejectAborted,
				WaitedMs: registry.now().UnixMilli() - item.enqueuedAtMs,
			})
		}
	}
	registry.states = make(map[string]*bodyAdmissionState)
	registry.mu.Unlock()
}

func (registry *SpeedFirstBodyAdmissionRegistry) acquiredDecision(state *bodyAdmissionState, waitedMs int64) SpeedFirstBodyAdmissionDecision {
	registryRef := registry
	released := false
	return SpeedFirstBodyAdmissionDecision{
		Acquired: true,
		WaitedMs: waitedMs,
		Release: func() {
			registryRef.mu.Lock()
			defer registryRef.mu.Unlock()
			if released {
				return
			}
			released = true
			state.active = bodyAdmissionMax(0, state.active-1)
			registryRef.wakeQueuedItemsLocked(state)
			registryRef.cleanupStateLocked(state)
		},
	}
}

// wakeQueuedItemsLocked mirrors wakeQueuedItems; caller holds the lock.
func (registry *SpeedFirstBodyAdmissionRegistry) wakeQueuedItemsLocked(state *bodyAdmissionState) {
	for state.active < state.capacity && len(state.queue) > 0 {
		item := state.queue[0]
		if item == nil {
			return
		}
		registry.removeQueuedItemLocked(state, item)
		state.active++
		waitedMs := registry.now().UnixMilli() - item.enqueuedAtMs
		item.resolve(registry.acquiredDecision(state, waitedMs))
	}
}

// completeQueuedItem mirrors completeQueuedItem.
func (registry *SpeedFirstBodyAdmissionRegistry) completeQueuedItem(state *bodyAdmissionState, item *bodyAdmissionQueueItem, decision SpeedFirstBodyAdmissionDecision) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.completeQueuedItemLocked(state, item, decision)
}

// completeQueuedItemLocked completes with membership check; caller holds the
// lock.
func (registry *SpeedFirstBodyAdmissionRegistry) completeQueuedItemLocked(state *bodyAdmissionState, item *bodyAdmissionQueueItem, decision SpeedFirstBodyAdmissionDecision) {
	if !bodyAdmissionQueueContains(state.queue, item) {
		return
	}
	registry.removeQueuedItemLocked(state, item)
	item.resolve(decision)
	registry.cleanupStateLocked(state)
}

// removeQueuedItemLocked mirrors removeQueuedItem; caller holds the lock.
func (registry *SpeedFirstBodyAdmissionRegistry) removeQueuedItemLocked(state *bodyAdmissionState, item *bodyAdmissionQueueItem) {
	for index, candidate := range state.queue {
		if candidate == item {
			state.queue = append(state.queue[:index], state.queue[index+1:]...)
			break
		}
	}
	if item.timer != nil {
		item.timer.Stop()
	}
	item.removed = true
	count := state.perAPIKeyQueued[item.apiKeyID]
	if count <= 1 {
		delete(state.perAPIKeyQueued, item.apiKeyID)
	} else {
		state.perAPIKeyQueued[item.apiKeyID] = count - 1
	}
}

// cleanupStateLocked mirrors cleanupState; caller holds the lock.
func (registry *SpeedFirstBodyAdmissionRegistry) cleanupStateLocked(state *bodyAdmissionState) {
	if state.active == 0 && len(state.queue) == 0 {
		delete(registry.states, state.key)
	}
}

func bodyAdmissionQueueContains(queue []*bodyAdmissionQueueItem, item *bodyAdmissionQueueItem) bool {
	for _, candidate := range queue {
		if candidate == item {
			return true
		}
	}
	return false
}

func createState(key string, capacity int) *bodyAdmissionState {
	return &bodyAdmissionState{
		key:             key,
		capacity:        bodyAdmissionPositiveInteger(capacity, 1),
		perAPIKeyQueued: make(map[string]int),
	}
}

func bodyAdmissionKey(input SpeedFirstBodyAdmissionInput) string {
	return input.SystemAccountID + ":" + input.RouteStrategyID + ":" + input.GroupID
}

// bodyAdmissionPositiveInteger mirrors the Node positiveInteger helper
// (max(1, trunc(value)); int inputs are always finite so the fallback never
// applies).
func bodyAdmissionPositiveInteger(value int, fallback int) int {
	return bodyAdmissionMax(1, value)
}

// bodyAdmissionNonNegativeInteger mirrors the Node nonNegativeInteger helper.
func bodyAdmissionNonNegativeInteger(value int64, fallback int64) int64 {
	return bodyAdmissionMax64(0, value)
}

func bodyAdmissionMax(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func bodyAdmissionMax64(left int64, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
