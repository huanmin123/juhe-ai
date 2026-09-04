package gatewayquota

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Modes mirrors the runtimeConfig axes the quota subsystem forks on
// (backend/src/config/runtime.ts). They are fixed at construction so every
// code path is explicit about which driver it touches.
type Modes struct {
	// PostgresDatabase mirrors databaseDriver === 'postgres'.
	PostgresDatabase bool
	// RedisCache mirrors cacheDriver === 'redis'.
	RedisCache bool
	// RedisRuntimeState mirrors runtimeStateDriver === 'redis'.
	RedisRuntimeState bool
	// ServerRole mirrors processRole === 'server' (gateway server that must
	// reach the DB through the DB service instead of local SQLite).
	ServerRole bool
}

// Decision mirrors GatewayQuotaDecision / ApiKeyQuotaDecision /
// AuthorizationQuotaDecision — the identical {allowed, message?} shape. The
// JSON tags keep the shared-cache and runtime-state payloads byte-compatible
// with Node (message omitted when empty).
type Decision struct {
	Allowed bool   `json:"allowed"`
	Message string `json:"message,omitempty"`
}

// AllowedDecision mirrors { allowed: true }.
func AllowedDecision() Decision { return Decision{Allowed: true} }

// DeniedDecision mirrors { allowed: false, message }.
func DeniedDecision(message string) Decision { return Decision{Allowed: false, Message: message} }

// CachedDecision mirrors the cache entries (decision + checkedAtMs).
type CachedDecision struct {
	Allowed     bool   `json:"allowed"`
	Message     string `json:"message,omitempty"`
	CheckedAtMs int64  `json:"checkedAtMs"`
}

func (c CachedDecision) decision() Decision {
	return Decision{Allowed: c.Allowed, Message: c.Message}
}

func newCachedDecision(d Decision, atMs int64) CachedDecision {
	return CachedDecision{Allowed: d.Allowed, Message: d.Message, CheckedAtMs: atMs}
}

// APIKeyRow is the GatewayApiKeyRow subset the quota subsystem reads.
type APIKeyRow struct {
	ID string
	// SystemAccountID mirrors system_account_id.
	SystemAccountID string
	// QuotaLimitsJSON mirrors quota_limits_json (empty for NULL).
	QuotaLimitsJSON string
}

// AccountRef mirrors the { accountId, accountAuthorizationId } batch items.
type AccountRef struct {
	AccountID              string
	AccountAuthorizationID string
}

// DBServiceClient is the db-service-ipc port (Node requestDbService) for the
// four read operations the quota services fall back to on the server role.
type DBServiceClient interface {
	// CheckAPIKeyQuota mirrors { type: 'check_api_key_quota', apiKey }.
	CheckAPIKeyQuota(ctx context.Context, apiKey APIKeyRow) (Decision, error)
	// ReadAPIKeyQuotaCosts mirrors { type: 'read_api_key_quota_costs', apiKey }.
	ReadAPIKeyQuotaCosts(ctx context.Context, apiKey APIKeyRow) (RequestQuotaCosts, error)
	// CheckAuthorizationQuota mirrors { type: 'check_authorization_quota',
	// groupAuthorizationId, accountAuthorizationId }.
	CheckAuthorizationQuota(ctx context.Context, groupAuthorizationID, accountAuthorizationID string) (Decision, error)
	// CheckAuthorizationQuotaBatch mirrors { type:
	// 'check_authorization_quota_batch', groupAuthorizationId, accounts }.
	// The returned slice is index-aligned with accounts; a missing entry
	// falls back to { allowed: true } exactly like Node.
	CheckAuthorizationQuotaBatch(ctx context.Context, groupAuthorizationID string, accounts []AccountRef) ([]Decision, error)
}

// InvalidationSyncer mirrors syncGatewayCacheInvalidationsFromRuntimeState.
// The composition root adapts *inval.Bus (SyncFromShared over the quota
// topics). Errors propagate to the caller exactly like Node's coordinator.
type InvalidationSyncer interface {
	SyncGatewayCacheInvalidations(ctx context.Context) error
}

// SharedJSONCache is the createSharedJsonCache subset the quota caches use
// (get/set/clear). Only consulted when Modes.RedisCache is on.
type SharedJSONCache interface {
	Get(ctx context.Context, key string, target any) (bool, error)
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Clear(ctx context.Context) error
}

// RuntimeStateStore is the createRuntimeStateStore subset the quota snapshot
// reads (getJson). Only consulted when Modes.RedisCache && Modes.RedisRuntimeState.
type RuntimeStateStore interface {
	GetJSON(ctx context.Context, storeName, key string, target any) (bool, error)
}

// LogHook receives the Node logger.warn events (best-effort diagnostics;
// never alters the decision). fields mirrors the Node log fields.
type LogHook func(event string, fields map[string]any, message string)

func noopLog(event string, fields map[string]any, message string) {}

// errServerLocalSQLite mirrors assertLocalGatewayDatabaseAccess.
func errServerLocalSQLite(operation string) error {
	return errors.New("server 角色禁止直接同步读取 SQLite：" + operation + " 必须通过 DB service")
}

// splitNul splits on the \x0000 separator the Node cache keys use.
func splitNul(value string) []string {
	return strings.Split(value, "\x00")
}

func segment(cacheKey string, index int) string {
	parts := splitNul(cacheKey)
	if index < len(parts) {
		return parts[index]
	}
	return ""
}

// apiKeyIDFromQuotaCacheKey mirrors apiKeyIdFromQuotaCacheKey (segment 1).
func apiKeyIDFromQuotaCacheKey(cacheKey string) string {
	return segment(cacheKey, 1)
}

// lruCache is the createAppCache subset (max + ttl, recency touched on
// get/set, dispose callback on explicit delete and LRU eviction — matching
// lru-cache's dispose lifecycle that the api-key quota index relies on).
type lruCache[V any] struct {
	max     int
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*lruEntry[V]
	order   *recencyList
	onEvict func(key string)
}

type lruEntry[V any] struct {
	value     V
	expiresAt time.Time
}

func newLRUCache[V any](max int, ttl time.Duration, now func() time.Time, onEvict func(string)) *lruCache[V] {
	if max < 1 {
		max = 1
	}
	if now == nil {
		now = time.Now
	}
	return &lruCache[V]{max: max, ttl: ttl, now: now, entries: map[string]*lruEntry[V]{}, order: newRecencyList(), onEvict: onEvict}
}

func (c *lruCache[V]) get(key string) (V, bool) {
	entry, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	if c.now().After(entry.expiresAt) {
		var zero V
		return zero, false
	}
	c.order.moveToFront(key)
	return entry.value, true
}

func (c *lruCache[V]) set(key string, value V) {
	now := c.now()
	if entry, ok := c.entries[key]; ok {
		entry.value = value
		entry.expiresAt = now.Add(c.ttl)
		c.order.moveToFront(key)
		return
	}
	c.entries[key] = &lruEntry[V]{value: value, expiresAt: now.Add(c.ttl)}
	c.order.pushFront(key)
	for len(c.entries) > c.max {
		oldest, ok := c.order.back()
		if !ok {
			break
		}
		c.delete(oldest)
	}
}

func (c *lruCache[V]) delete(key string) {
	if _, ok := c.entries[key]; !ok {
		return
	}
	c.order.remove(key)
	delete(c.entries, key)
	if c.onEvict != nil {
		c.onEvict(key)
	}
}

func (c *lruCache[V]) clear() {
	c.entries = map[string]*lruEntry[V]{}
	c.order = newRecencyList()
}

func (c *lruCache[V]) len() int { return len(c.entries) }

// recencyList is a doubly linked list over recency-ordered keys.
type recencyList struct {
	head, tail *recencyNode
}

type recencyNode struct {
	key  string
	prev *recencyNode
	next *recencyNode
}

func newRecencyList() *recencyList { return &recencyList{} }

func (l *recencyList) pushFront(key string) {
	node := &recencyNode{key: key, next: l.head}
	if l.head != nil {
		l.head.prev = node
	}
	l.head = node
	if l.tail == nil {
		l.tail = node
	}
}

func (l *recencyList) unlink(node *recencyNode) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		l.head = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		l.tail = node.prev
	}
}

func (l *recencyList) remove(key string) {
	for node := l.head; node != nil; node = node.next {
		if node.key == key {
			l.unlink(node)
			return
		}
	}
}

func (l *recencyList) moveToFront(key string) {
	if l.head != nil && l.head.key == key {
		return
	}
	l.remove(key)
	l.pushFront(key)
}

func (l *recencyList) back() (string, bool) {
	if l.tail == nil {
		return "", false
	}
	return l.tail.key, true
}

// quotaMemoryCache keeps the api-key quota runtime LRU plus its per-id index
// behind one mutex (Node apiKeyQuotaCache + apiKeyQuotaCacheKeysById).
type quotaMemoryCache struct {
	mu    sync.Mutex
	lru   *lruCache[CachedDecision]
	index map[string]map[string]struct{}
}

func newQuotaMemoryCache(now func() time.Time, ttl time.Duration, max int) *quotaMemoryCache {
	cache := &quotaMemoryCache{index: map[string]map[string]struct{}{}}
	cache.lru = newLRUCache[CachedDecision](max, ttl, now, func(cacheKey string) {
		// dispose callback: keep the per-id index in sync.
		cache.removeIndexLocked(apiKeyIDFromQuotaCacheKey(cacheKey), cacheKey)
	})
	return cache
}

func (c *quotaMemoryCache) get(cacheKey string) (CachedDecision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.get(cacheKey)
}

func (c *quotaMemoryCache) set(apiKeyID, cacheKey string, entry CachedDecision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.lru.entries[cacheKey]; exists {
		// Node setApiKeyQuotaCacheEntry: re-index under the id parsed from
		// the cache key before overwriting.
		c.removeIndexLocked(apiKeyIDFromQuotaCacheKey(cacheKey), cacheKey)
	}
	c.lru.set(cacheKey, entry)
	c.addIndexLocked(apiKeyID, cacheKey)
}

func (c *quotaMemoryCache) addIndexLocked(apiKeyID, cacheKey string) {
	set, ok := c.index[apiKeyID]
	if !ok {
		set = map[string]struct{}{}
		c.index[apiKeyID] = set
	}
	set[cacheKey] = struct{}{}
}

func (c *quotaMemoryCache) removeIndexLocked(apiKeyID, cacheKey string) {
	set, ok := c.index[apiKeyID]
	if !ok {
		return
	}
	delete(set, cacheKey)
	if len(set) == 0 {
		delete(c.index, apiKeyID)
	}
}

// removeByID mirrors invalidateApiKeyQuotaCacheById's memory branch: delete
// every indexed cache key of the api key, then drop the index entry.
func (c *quotaMemoryCache) removeByID(apiKeyID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.index[apiKeyID]))
	for cacheKey := range c.index[apiKeyID] {
		keys = append(keys, cacheKey)
	}
	for _, cacheKey := range keys {
		c.lru.delete(cacheKey)
	}
	delete(c.index, apiKeyID)
}

func (c *quotaMemoryCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.clear()
	c.index = map[string]map[string]struct{}{}
}

// simpleMemoryCache is the plain decision LRU (authorization quota runtime
// cache, no index).
type simpleMemoryCache struct {
	mu  sync.Mutex
	lru *lruCache[CachedDecision]
}

func newSimpleMemoryCache(now func() time.Time, ttl time.Duration, max int) *simpleMemoryCache {
	return &simpleMemoryCache{lru: newLRUCache[CachedDecision](max, ttl, now, nil)}
}

func (c *simpleMemoryCache) get(cacheKey string) (CachedDecision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.get(cacheKey)
}

func (c *simpleMemoryCache) set(cacheKey string, entry CachedDecision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.set(cacheKey, entry)
}

func (c *simpleMemoryCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.clear()
}
