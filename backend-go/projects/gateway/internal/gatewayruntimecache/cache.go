package gatewayruntimecache

import (
	"container/list"
	"sync"
	"time"
)

// Clock separates wall time from cache logic so every TTL/revalidate path is
// testable with a fake clock (Node tests inject Date.now the same way).
type Clock interface {
	Now() time.Time
}

// realClock uses the system time.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// SystemClock returns the default clock.
func SystemClock() Clock { return realClock{} }

// entryCache is the bounded process-local TTL cache mirroring the Node
// createAppCache / createProcessLocalResourceCache primitive (lru-cache with
// max + per-entry ttl + updateAgeOnGet + dispose). enabled=false mirrors the
// Node appCache behaviour when cacheDriver === 'redis': reads and writes are
// no-ops because the Redis shared cache becomes the fact source.
type entryCache[K comparable, V any] struct {
	name          string
	max           int
	ttl           time.Duration
	updateAgeOnGet bool
	enabled       bool
	clock         Clock

	mu      sync.Mutex
	items   map[K]*list.Element
	order   *list.List // front = most recent
	onClear func()
	dispose func(key K, value V)
}

type entryValue[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
}

func newEntryCache[K comparable, V any](name string, max int, ttl time.Duration, updateAgeOnGet, enabled bool, clock Clock, dispose func(K, V), onClear func()) *entryCache[K, V] {
	if max < 1 {
		max = 1
	}
	if clock == nil {
		clock = SystemClock()
	}
	return &entryCache[K, V]{
		name:           name,
		max:            max,
		ttl:            ttl,
		updateAgeOnGet: updateAgeOnGet,
		enabled:        enabled,
		clock:          clock,
		items:          make(map[K]*list.Element),
		order:          list.New(),
		dispose:        dispose,
		onClear:        onClear,
	}
}

func (c *entryCache[K, V]) now() time.Time { return c.clock.Now() }

// get returns the live value; expired entries are dropped with dispose like
// lru-cache.
func (c *entryCache[K, V]) get(key K) (V, bool) {
	if !c.enabled {
		var zero V
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	entry := element.Value.(*entryValue[K, V])
	now := c.now()
	if !entry.expiresAt.IsZero() && !entry.expiresAt.After(now) {
		c.removeElementLocked(element)
		var zero V
		return zero, false
	}
	if c.updateAgeOnGet {
		c.order.MoveToFront(element)
	}
	return entry.value, true
}

// set stores the value with the cache default TTL unless ttl override given;
// zero/negative ttl keeps the entry until eviction (lru-cache ttl: 0). In Go a
// zero override maps to the default; callers pass retainTTL explicitly.
func (c *entryCache[K, V]) set(key K, value V, ttl time.Duration) {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		entry := element.Value.(*entryValue[K, V])
		entry.value = value
		entry.expiresAt = c.expiresAt(ttl)
		c.order.MoveToFront(element)
		c.evictOverflowLocked()
		return
	}
	element := c.order.PushFront(&entryValue[K, V]{key: key, value: value, expiresAt: c.expiresAt(ttl)})
	c.items[key] = element
	c.evictOverflowLocked()
}

func (c *entryCache[K, V]) expiresAt(ttl time.Duration) time.Time {
	if ttl <= 0 {
		ttl = c.ttl
	}
	return c.now().Add(ttl)
}

func (c *entryCache[K, V]) evictOverflowLocked() {
	for len(c.items) > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.removeElementLocked(oldest)
	}
}

func (c *entryCache[K, V]) removeElementLocked(element *list.Element) {
	entry := element.Value.(*entryValue[K, V])
	c.order.Remove(element)
	delete(c.items, entry.key)
	if c.dispose != nil {
		c.dispose(entry.key, entry.value)
	}
}

func (c *entryCache[K, V]) delete(key K) {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.removeElementLocked(element)
	}
}

// peek returns the entry even when stale (used for the bounded stale
// fallback window; Node reads the raw entry then checks revalidateAtMs).
func (c *entryCache[K, V]) peek(key K) (V, bool) {
	if !c.enabled {
		var zero V
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	entry := element.Value.(*entryValue[K, V])
	if c.updateAgeOnGet {
		c.order.MoveToFront(element)
	}
	return entry.value, true
}

func (c *entryCache[K, V]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[K]*list.Element)
	c.order.Init()
	if c.onClear != nil {
		c.onClear()
	}
}

func (c *entryCache[K, V]) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
