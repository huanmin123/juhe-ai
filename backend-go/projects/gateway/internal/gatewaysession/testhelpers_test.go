package gatewaysession

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// fakeClock is a deterministic millisecond clock for TTL tests.
type fakeClock struct {
	mu    sync.Mutex
	nowMs int64
}

func newFakeClock(start int64) *fakeClock {
	return &fakeClock{nowMs: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.UnixMilli(c.nowMs)
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowMs += d.Milliseconds()
}

func (c *fakeClock) Millis() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nowMs
}

// capturedWarn records one warn call.
type capturedWarn struct {
	fields  map[string]any
	message string
}

// captureLogger records warns and is safe for concurrent use.
type captureLogger struct {
	mu    sync.Mutex
	warns []capturedWarn
}

func (l *captureLogger) Warn(fields map[string]any, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	copied := make(map[string]any, len(fields))
	for key, value := range fields {
		copied[key] = value
	}
	l.warns = append(l.warns, capturedWarn{fields: copied, message: message})
}

func (l *captureLogger) Events() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := make([]string, 0, len(l.warns))
	for _, warn := range l.warns {
		if event, ok := warn.fields["event"].(string); ok {
			events = append(events, event)
		}
	}
	return events
}

func (l *captureLogger) HasEvent(event string) bool {
	for _, candidate := range l.Events() {
		if candidate == event {
			return true
		}
	}
	return false
}

// mockConcurrency is a controllable ConcurrencySource.
type mockConcurrency struct {
	mu       sync.Mutex
	current  map[string]int
	lanes    map[string]map[string]int
	inFlight map[string]AccountInFlightStats
}

func newMockConcurrency() *mockConcurrency {
	return &mockConcurrency{
		current:  map[string]int{},
		lanes:    map[string]map[string]int{RequestLaneImage: {}},
		inFlight: map[string]AccountInFlightStats{},
	}
}

func (m *mockConcurrency) SetCurrent(accountID string, value int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current[accountID] = value
}

func (m *mockConcurrency) SetLane(lane string, accountID string, value int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lanes[lane] == nil {
		m.lanes[lane] = map[string]int{}
	}
	m.lanes[lane][accountID] = value
}

func (m *mockConcurrency) SetInFlight(accountID string, stats AccountInFlightStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight[accountID] = stats
}

func (m *mockConcurrency) GetAccountCurrentConcurrency(accountID string, lane string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lane == "" {
		return m.current[accountID]
	}
	return m.lanes[lane][accountID]
}

func (m *mockConcurrency) LoadAccountCurrentConcurrencyByIDsAsync(_ context.Context, accountIDs []string, lane string) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(accountIDs))
	for _, id := range accountIDs {
		if lane == "" {
			out[id] = m.current[id]
		} else {
			out[id] = m.lanes[lane][id]
		}
	}
	return out, nil
}

func (m *mockConcurrency) LoadAccountInFlightStatsByIDs(accountIDs []string, thresholds InFlightThresholds) map[string]AccountInFlightStats {
	return m.loadInFlight(accountIDs)
}

func (m *mockConcurrency) LoadAccountInFlightStatsByIDsAsync(_ context.Context, accountIDs []string, thresholds InFlightThresholds) (map[string]AccountInFlightStats, error) {
	return m.loadInFlight(accountIDs), nil
}

func (m *mockConcurrency) loadInFlight(accountIDs []string) map[string]AccountInFlightStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]AccountInFlightStats, len(accountIDs))
	for _, id := range accountIDs {
		if stats, ok := m.inFlight[id]; ok {
			out[id] = stats
		}
	}
	return out
}

var _ ConcurrencySource = (*mockConcurrency)(nil)

// newTestAffinityService builds a memory-driver service with deterministic
// clock and capture logger.
func newTestAffinityService(t *testing.T, mutate func(*AffinityConfig)) (*AffinityService, *fakeClock, *captureLogger) {
	t.Helper()
	clock := newFakeClock(1_700_000_000_000)
	logger := &captureLogger{}
	cfg := AffinityConfig{
		CacheDriver:        CacheDriverMemory,
		RuntimeStateDriver: RuntimeStateDriverMemory,
		Secret:             testHMACSecret,
		RedisNamespace:     "g14-tests",
		Clock:              clock.Now,
		Logger:             logger,
		Concurrency:        newMockConcurrency(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	service, err := NewAffinityService(cfg)
	if err != nil {
		t.Fatalf("NewAffinityService: %v", err)
	}
	return service, clock, logger
}

// testAccount builds an OpenAIAccountSecret with the given knobs.
func testAccount(id string, priority int, tweaks func(*gatewayruntimecache.OpenAIAccountSecret)) gatewayruntimecache.OpenAIAccountSecret {
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:               id,
		ConcurrencyLimit: 5,
		Priority:         priority,
	}
	if tweaks != nil {
		tweaks(&account)
	}
	return account
}

func ptrFloat(v float64) *float64 { return &v }

func ptrInt(v int) *int { return &v }
