package gatewayproxyhealth

import (
	"context"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// fakeClock is the injectable clock shared by the tests.
type fakeClock struct {
	mu sync.Mutex
	ms int64
}

func newFakeClock(ms int64) *fakeClock { return &fakeClock{ms: ms} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.UnixMilli(c.ms)
}

func (c *fakeClock) NowMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ms
}

func (c *fakeClock) Advance(ms int64) {
	c.mu.Lock()
	c.ms += ms
	c.mu.Unlock()
}

func (c *fakeClock) Set(ms int64) {
	c.mu.Lock()
	c.ms = ms
	c.mu.Unlock()
}

// accountFixture builds gatewayruntimecache.OpenAIAccountSecret values with
// optional per-field overrides for the proxy-health tests.
type accountFixture struct {
	id                 string
	systemAccountID    string
	ownerAccountID     string
	providerCode       string
	protocolCode       string
	protocolVersion    string
	baseURL            string
	accountType        string
	proxyProfileID     *string
	proxyURL           *string
	credentialSourceID *string
	priority           int
	superPriority      bool
	fallbackEnabled    bool
}

func (f accountFixture) build() gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                          f.id,
		SystemAccountID:             f.systemAccountID,
		AccountOwnerSystemAccountID: f.ownerAccountID,
		ProviderCode:                f.providerCode,
		ProtocolCode:                f.protocolCode,
		ProtocolVersion:             f.protocolVersion,
		BaseURL:                     f.baseURL,
		Type:                        f.accountType,
		ProxyProfileID:              f.proxyProfileID,
		ProxyURL:                    f.proxyURL,
		CredentialSourceAccountID:   f.credentialSourceID,
		Priority:                    f.priority,
		SuperPriorityEnabled:        f.superPriority,
		FallbackEnabled:             f.fallbackEnabled,
	}
}

func stringPtrValue(v string) *string { return &v }

func int64PtrValue(v int64) *int64 { return &v }

// recordingLog captures log payloads for assertions.
type recordingLog struct {
	mu       sync.Mutex
	messages []string
	fields   []map[string]any
}

func (r *recordingLog) record(fields map[string]any, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
	r.fields = append(r.fields, fields)
}

func (r *recordingLog) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

func (r *recordingLog) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]string, 0, len(r.fields))
	for _, field := range r.fields {
		if event, ok := field["event"].(string); ok {
			events = append(events, event)
		}
	}
	return events
}

func newMemoryProxyHealth(clock *fakeClock) (*ProxyHealthService, *MemoryRuntimeStateStore) {
	store := NewMemoryRuntimeStateStore(clock.Now)
	return NewProxyHealthService(clock.Now, store, ProxyHealthOptions{}, nil), store
}

func newMemoryLatencyService(clock *fakeClock) (*LatencyDegradationService, *MemoryRuntimeStateStore) {
	store := NewMemoryRuntimeStateStore(clock.Now)
	return NewLatencyDegradationService(store, clock.Now, LatencyDegradationOptions{
		LockRetryDelay: func(int) {},
		Random:         func() float64 { return 0.5 },
	}), store
}

func contextBackground() context.Context { return context.Background() }
