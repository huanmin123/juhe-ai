package gatewaysession

import (
	"container/list"
	"time"
)

// ttlCache is the local projection of shared/cache.ts createAppCache for the
// affinity caches: TTL from write, optional age-refresh on get, LRU eviction
// with dispose, clear with onClear, and the dropStoreIfDisabled semantics
// (when the process-local cache stops being a fact source — Redis cache
// driver — the first access resets the store, fires onReset, and reads stay
// misses while readable() is false).
//
// The cache is NOT thread-safe: the owning AffinityService serializes every
// access with its state mutex, mirroring the single-threaded Node module.
// dispose / onClear / onReset run synchronously inside the owning call.
type ttlCache[T any] struct {
	max            int
	ttl            time.Duration
	updateAgeOnGet bool

	order   *list.List // front = most recent; values are *ttlCacheEntry[T]
	entries map[string]*list.Element

	readable func() bool
	onReset  func()
	dispose  func(key string, value T)
	onClear  func()
	now      func() time.Time
}

type ttlCacheEntry[T any] struct {
	key       string
	value     T
	expiresAt time.Time
}

func newTTLCache[T any](max int, ttl time.Duration, updateAgeOnGet bool) *ttlCache[T] {
	return &ttlCache[T]{
		max:            max,
		ttl:            ttl,
		updateAgeOnGet: updateAgeOnGet,
		order:          list.New(),
		entries:        make(map[string]*list.Element, max),
	}
}

func (c *ttlCache[T]) Get(key string) (T, bool) {
	var zero T
	c.dropStoreIfDisabled()
	if c.readable != nil && !c.readable() {
		return zero, false
	}
	element, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	entry := element.Value.(*ttlCacheEntry[T])
	now := c.nowTime()
	if !now.Before(entry.expiresAt) {
		c.removeElement(element)
		if c.dispose != nil {
			c.dispose(key, entry.value)
		}
		return zero, false
	}
	if c.updateAgeOnGet {
		c.order.MoveToFront(element)
	}
	return entry.value, true
}

func (c *ttlCache[T]) Set(key string, value T) {
	c.dropStoreIfDisabled()
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*ttlCacheEntry[T])
		oldValue := entry.value
		entry.value = value
		entry.expiresAt = c.nowTime().Add(c.ttl)
		c.order.MoveToFront(element)
		if c.dispose != nil {
			// lru-cache calls dispose for replaced values; the affinity index
			// removal guards on binding identity so this stays consistent.
			c.dispose(key, oldValue)
		}
		return
	}
	element := c.order.PushFront(&ttlCacheEntry[T]{key: key, value: value, expiresAt: c.nowTime().Add(c.ttl)})
	c.entries[key] = element
	c.evictOverflow()
}

// nowTime falls back to time.Now when no clock is wired.
func (c *ttlCache[T]) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *ttlCache[T]) Delete(key string) {
	element, ok := c.entries[key]
	if !ok {
		return
	}
	entry := element.Value.(*ttlCacheEntry[T])
	c.removeElement(element)
	if c.dispose != nil {
		c.dispose(key, entry.value)
	}
}

func (c *ttlCache[T]) Clear() {
	c.order.Init()
	c.entries = make(map[string]*list.Element, c.max)
	if c.onClear != nil {
		c.onClear()
	}
}

func (c *ttlCache[T]) Len() int {
	return len(c.entries)
}

func (c *ttlCache[T]) evictOverflow() {
	for len(c.entries) > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		entry := oldest.Value.(*ttlCacheEntry[T])
		c.removeElement(oldest)
		if c.dispose != nil {
			c.dispose(entry.key, entry.value)
		}
	}
}

func (c *ttlCache[T]) removeElement(element *list.Element) {
	entry := element.Value.(*ttlCacheEntry[T])
	delete(c.entries, entry.key)
	c.order.Remove(element)
}

func (c *ttlCache[T]) dropStoreIfDisabled() {
	// Node dropStoreIfDisabled: skip while the process-local store is a fact
	// source; reset once when it stopped being one and holds entries.
	if c.readable == nil || c.readable() {
		return
	}
	if len(c.entries) == 0 {
		return
	}
	c.order.Init()
	c.entries = make(map[string]*list.Element, c.max)
	if c.onReset != nil {
		// Node calls options.onClear() for the store reset; lru-cache clear()
		// does not run per-entry dispose, and neither do we — onReset covers
		// the index reset.
		c.onReset()
	}
}
