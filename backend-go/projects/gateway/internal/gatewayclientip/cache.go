package gatewayclientip

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Clock and Logger reuse the shared runtime-cache contracts.
type (
	// Clock injects time; tests use a manual clock.
	Clock = gatewayruntimecache.Clock
	// Logger receives warn-path diagnostics.
	Logger = gatewayruntimecache.Logger
)

// systemClock is the wall-clock default (gatewayruntimecache.SystemClock).
func systemClock() Clock { return gatewayruntimecache.SystemClock() }

// entryTTLCache mirrors the lru-cache-backed createAppCache behavior this
// family uses: per-entry TTL, LRU recency on get, max-size eviction. Node is
// single threaded; Go serializes access with a mutex.
type entryTTLCache[V any] struct {
	clock  Clock
	max    int
	mu     sync.Mutex
	entries map[string]entryTTLValue[V]
	// order holds insert order for eviction (lru-cache evicts by recency;
	// get refreshes recency, so track order by last access).
	order []string
}

type entryTTLValue[V any] struct {
	value     V
	expiresAt time.Time
}

func newEntryTTLCache[V any](clock Clock, max int) *entryTTLCache[V] {
	return &entryTTLCache[V]{clock: clock, max: max, entries: map[string]entryTTLValue[V]{}}
}

func (c *entryTTLCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	if !entry.expiresAt.After(c.clock.Now()) {
		c.removeLocked(key)
		var zero V
		return zero, false
	}
	c.touchLocked(key)
	return entry.value, true
}

func (c *entryTTLCache[V]) set(key string, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
	} else {
		c.touchLocked(key)
	}
	c.entries[key] = entryTTLValue[V]{value: value, expiresAt: c.clock.Now().Add(ttl)}
	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *entryTTLCache[V]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]entryTTLValue[V]{}
	c.order = nil
}

func (c *entryTTLCache[V]) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *entryTTLCache[V]) touchLocked(key string) {
	for i, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *entryTTLCache[V]) removeLocked(key string) {
	delete(c.entries, key)
	for i, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// memorySharedCache mirrors MemorySharedJsonCache (shared/cache.ts) for the
// non-Redis cache driver: a bounded LRU with a default TTL per entry.
type memorySharedCache struct {
	clock Clock
	max   int
	ttl   time.Duration
	cache *entryTTLCache[[]byte]
}

func newMemorySharedCache(clock Clock, max int, ttl time.Duration) *memorySharedCache {
	return &memorySharedCache{clock: clock, max: max, ttl: ttl, cache: newEntryTTLCache[[]byte](clock, max)}
}

// Get implements the shared JSON cache read.
func (c *memorySharedCache) Get(_ context.Context, key string, dst any) (bool, error) {
	raw, ok := c.cache.get(key)
	if !ok {
		return false, nil
	}
	return decodeSharedJSON(raw, dst)
}

// Set implements the shared JSON cache write.
func (c *memorySharedCache) Set(_ context.Context, key string, value any, ttl time.Duration) error {
	encoded, err := encodeSharedJSON(value)
	if err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = c.ttl
	}
	c.cache.set(key, encoded, ttl)
	return nil
}

// Clear implements the whole-cache clear.
func (c *memorySharedCache) Clear(_ context.Context) error {
	c.cache.clear()
	return nil
}

// orderedExpiryMap mirrors the Node Map<string, {value, expiresAt}> memory
// stores shared by the circuit and avoidance memories: expiry reads delete,
// insertion order drives eviction and snapshot iteration.
type orderedExpiryMap[V any] struct {
	clock   Clock
	max     int
	mu      sync.Mutex
	entries map[string]orderedExpiryEntry[V]
	order   []string
}

type orderedExpiryEntry[V any] struct {
	value     V
	expiresAt int64
}

func newOrderedExpiryMap[V any](clock Clock, max int) *orderedExpiryMap[V] {
	return &orderedExpiryMap[V]{
		clock:   clock,
		max:     max,
		entries: map[string]orderedExpiryEntry[V]{},
	}
}

// Get mirrors getMemory*Entry: expired entries delete and read as missing.
func (m *orderedExpiryMap[V]) Get(key string) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	if entry.expiresAt <= m.clock.Now().UnixMilli() {
		m.removeLocked(key)
		var zero V
		return zero, false
	}
	return entry.value, true
}

// Set mirrors setMemory*Entry: expiresAt = now + max(1, ttlMs), then evict
// the oldest inserted entries while over max.
func (m *orderedExpiryMap[V]) Set(key string, value V, ttlMs int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[key]; !exists {
		m.order = append(m.order, key)
	}
	m.entries[key] = orderedExpiryEntry[V]{
		value:     value,
		expiresAt: m.clock.Now().UnixMilli() + maxInt64(1, ttlMs),
	}
	for len(m.entries) > m.max {
		if len(m.order) == 0 {
			return
		}
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.entries, oldest)
	}
}

func (m *orderedExpiryMap[V]) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(key)
}

func (m *orderedExpiryMap[V]) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// Values mirrors memoryCircuitValues / memoryClientIpAccountAvoidanceValues:
// prune expired entries then return the live values in insertion order.
func (m *orderedExpiryMap[V]) Values() []V {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now().UnixMilli()
	values := make([]V, 0, len(m.entries))
	kept := m.order[:0]
	for _, key := range m.order {
		entry, ok := m.entries[key]
		if !ok {
			continue
		}
		if entry.expiresAt <= now {
			delete(m.entries, key)
			continue
		}
		kept = append(kept, key)
		values = append(values, entry.value)
	}
	m.order = kept
	return values
}

func (m *orderedExpiryMap[V]) removeLocked(key string) {
	delete(m.entries, key)
	for i, candidate := range m.order {
		if candidate == key {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

// encodeSharedJSON / decodeSharedJSON mirror the JSON round-trip the Node
// shared caches apply to every value.
func encodeSharedJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func decodeSharedJSON(raw []byte, dst any) (bool, error) {
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

// sharedSnapshotEntry mirrors ClientIpPolicySnapshotCacheEntry.
type sharedSnapshotEntry struct {
	LoadedAt string                 `json:"loadedAt"`
	Policies []ActiveClientIPPolicy `json:"policies"`
}

// sharedByIPEntry mirrors ClientIpPolicyByIpCacheEntry; Policy nil mirrors
// the Node `policy?: ActiveClientIpPolicy` absence.
type sharedByIPEntry struct {
	LoadedAt string                 `json:"loadedAt"`
	Policy   *ActiveClientIPPolicy  `json:"policy,omitempty"`
}
