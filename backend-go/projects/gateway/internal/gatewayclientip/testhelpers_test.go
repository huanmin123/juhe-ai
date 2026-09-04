package gatewayclientip

import (
	"context"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// manual clock + scheduler: deterministic time for -race-safe table tests
// ---------------------------------------------------------------------------

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(start time.Time) *manualClock {
	return &manualClock{now: start}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type manualTimer struct {
	at       time.Time
	fn       func()
	cancelled bool
}

type manualScheduler struct {
	clock  *manualClock
	mu     sync.Mutex
	timers []*manualTimer
}

func (s *manualScheduler) AfterFunc(d time.Duration, fn func()) func() {
	s.mu.Lock()
	timer := &manualTimer{at: s.clock.Now().Add(d), fn: fn}
	s.timers = append(s.timers, timer)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		timer.cancelled = true
		s.mu.Unlock()
	}
}

// advance moves the clock and fires every due timer in schedule order; a
// timer that reschedules (the hit-buffer flush chain) keeps looping until
// nothing is due before the target instant.
func (s *manualScheduler) advance(d time.Duration) {
	target := s.clock.Now().Add(d)
	for {
		s.mu.Lock()
		var due *manualTimer
		for _, timer := range s.timers {
			if timer.cancelled || timer.at.After(target) {
				continue
			}
			if due == nil || timer.at.Before(due.at) {
				due = timer
			}
		}
		if due == nil {
			s.mu.Unlock()
			break
		}
		s.timers = removeTimer(s.timers, due)
		s.mu.Unlock()
		s.clock.advance(due.at.Sub(s.clock.Now()))
		due.fn()
	}
	s.clock.advance(target.Sub(s.clock.Now()))
}

func removeTimer(timers []*manualTimer, due *manualTimer) []*manualTimer {
	for i, timer := range timers {
		if timer == due {
			return append(timers[:i], timers[i+1:]...)
		}
	}
	return timers
}

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type fakePolicySource struct {
	mu        sync.Mutex
	policies  []ActiveClientIPPolicy
	hits      []PolicyHitInput
	err       error
	listCalls int
	findCalls int
	hitCalls  int
}

func (f *fakePolicySource) ListActiveClientIPPolicies(context.Context) ([]ActiveClientIPPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls += 1
	if f.err != nil {
		return nil, f.err
	}
	return append([]ActiveClientIPPolicy(nil), f.policies...), nil
}

func (f *fakePolicySource) FindActiveClientIPPolicyByHash(_ context.Context, ipHash string) (*ActiveClientIPPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls += 1
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.policies {
		if f.policies[i].IPHash == ipHash {
			policy := f.policies[i]
			return &policy, nil
		}
	}
	return nil, nil
}

func (f *fakePolicySource) RecordClientIPPolicyHits(_ context.Context, hits []PolicyHitInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hitCalls += 1
	if f.err != nil {
		return f.err
	}
	f.hits = append(f.hits, hits...)
	return nil
}

func (f *fakePolicySource) hitCountFor(ipHash, policyID string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, hit := range f.hits {
		if hit.IPHash == ipHash && hit.PolicyID == policyID {
			total += hit.HitCount
		}
	}
	return total
}

// fakeSharedCache is an in-memory gatewayruntimecache.SharedCache.
type fakeSharedCache struct {
	mu      sync.Mutex
	entries map[string][]byte
}

type fakeSharedCacheFactory struct {
	mu     sync.Mutex
	caches map[string]*fakeSharedCache
}

func newFakeSharedCacheFactory() *fakeSharedCacheFactory {
	return &fakeSharedCacheFactory{caches: map[string]*fakeSharedCache{}}
}

func (f *fakeSharedCacheFactory) Cache(name string) gatewayruntimecache.SharedCache {
	f.mu.Lock()
	defer f.mu.Unlock()
	cache, ok := f.caches[name]
	if !ok {
		cache = &fakeSharedCache{entries: map[string][]byte{}}
		f.caches[name] = cache
	}
	return cache
}

func (c *fakeSharedCache) Get(_ context.Context, key string, dst any) (bool, error) {
	c.mu.Lock()
	raw, ok := c.entries[key]
	c.mu.Unlock()
	if !ok {
		return false, nil
	}
	return decodeSharedJSON(raw, dst)
}

func (c *fakeSharedCache) Set(_ context.Context, key string, value any, _ time.Duration) error {
	encoded, err := encodeSharedJSON(value)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.entries[key] = encoded
	c.mu.Unlock()
	return nil
}

func (c *fakeSharedCache) Clear(_ context.Context) error {
	c.mu.Lock()
	c.entries = map[string][]byte{}
	c.mu.Unlock()
	return nil
}

// spyingLogger records warn events.
type spyingLogger struct {
	mu     sync.Mutex
	events []loggedEvent
}

type loggedEvent struct {
	event   string
	fields  map[string]any
	message string
}

func (l *spyingLogger) Warn(event string, fields map[string]any, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, loggedEvent{event: event, fields: fields, message: message})
}

func (l *spyingLogger) count(event string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for _, logged := range l.events {
		if logged.event == event {
			total += 1
		}
	}
	return total
}

// stubStatsWriter mirrors the stats writer bridge.
type stubStatsWriter struct {
	mu         sync.Mutex
	operations []string
	payloads   []StatsWriterPayload
	err        error
	listResult []ActiveClientIPPolicy
	findResult *ActiveClientIPPolicy
}

func (s *stubStatsWriter) RequestStatsWriter(_ context.Context, operation string, payload StatsWriterPayload) (StatsWriterPayload, error) {
	s.mu.Lock()
	s.operations = append(s.operations, operation)
	s.payloads = append(s.payloads, payload)
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return StatsWriterPayload{}, err
	}
	switch operation {
	case StatsWriterOpListActiveClientIPPolicies:
		return StatsWriterPayload{Policies: s.listResult}, nil
	case StatsWriterOpFindActiveClientIPPolicyByHash:
		return StatsWriterPayload{Policy: s.findResult}, nil
	}
	return StatsWriterPayload{}, nil
}

// recordingConcurrency is a seam double capturing subscription calls.
type recordingConcurrency struct {
	mu         sync.Mutex
	released   []AccountConcurrencyReleaseEvent
	current    map[string]int
	lane       map[string]int
	listeners  []*listenerHandle
}

func newRecordingConcurrency() *recordingConcurrency {
	return &recordingConcurrency{current: map[string]int{}, lane: map[string]int{}}
}

func (r *recordingConcurrency) LoadAccountCurrentConcurrencyByID(_ context.Context, accountIDs []string) (map[string]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for _, id := range accountIDs {
		out[id] = r.current[id]
	}
	return out, nil
}

func (r *recordingConcurrency) LoadAccountCurrentConcurrencyByLane(_ context.Context, accountIDs []string, lane string) (map[string]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for _, id := range accountIDs {
		out[id] = r.lane[id+":"+lane]
	}
	return out, nil
}

func (r *recordingConcurrency) CurrentAccountConcurrency(accountID string, lane string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lane == "" {
		return r.current[accountID]
	}
	return r.lane[accountID+":"+lane]
}

func (r *recordingConcurrency) SubscribeAccountConcurrencyRelease(listener func(AccountConcurrencyReleaseEvent)) func() {
	r.mu.Lock()
	handle := &listenerHandle{fn: listener}
	r.listeners = append(r.listeners, handle)
	r.mu.Unlock()
	return func() {}
}

// setTotal sets the process-local total concurrency under the lock (test
// helper for simulating the shared/account-concurrency tracker state).
func (r *recordingConcurrency) setTotal(accountID string, value int) {
	r.mu.Lock()
	r.current[accountID] = value
	r.mu.Unlock()
}

// emit simulates an account concurrency release.
func (r *recordingConcurrency) emit(accountID string, lane string) {
	r.mu.Lock()
	if r.current[accountID] > 0 {
		r.current[accountID] -= 1
	}
	laneKey := accountID + ":" + lane
	if r.lane[laneKey] > 0 {
		r.lane[laneKey] -= 1
	}
	handles := append([]*listenerHandle(nil), r.listeners...)
	r.mu.Unlock()
	for _, handle := range handles {
		handle.fn(AccountConcurrencyReleaseEvent{AccountID: accountID, Lane: lane})
	}
}
