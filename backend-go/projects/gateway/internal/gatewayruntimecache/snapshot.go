package gatewayruntimecache

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// runtime snapshot service (Node runtime/runtime-snapshot.service.ts port):
// the db-service / management-plane account concurrency + runtime availability
// snapshot caches behind the account and group list projections.
// ---------------------------------------------------------------------------

// Snapshot timing constants mirror runtime-snapshot.service.ts.
const (
	serverRuntimeSnapshotCacheTTL        = 300 * time.Millisecond
	serverRuntimeSnapshotMaxStale        = 5 * time.Second
	serverRuntimeSnapshotRefreshInterval = 100 * time.Millisecond
	snapshotAvailabilityKeyLimit         = 100
)

// AccountRuntimeAvailabilitySnapshot mirrors
// AccountRuntimeAvailabilitySnapshot: keyed by account id or the authorized
// projection key; payloads pass through to the account list slice.
type AccountRuntimeAvailabilitySnapshot = map[string]json.RawMessage

// AccountConcurrencySnapshot mirrors AccountConcurrencySnapshot.
type AccountConcurrencySnapshot = map[string]int

// AccountRuntimeSnapshotStatus mirrors AccountRuntimeSnapshotStatus.
type AccountRuntimeSnapshotStatus struct {
	AccountConcurrencyAvailable         bool `json:"accountConcurrencyAvailable"`
	AccountRuntimeAvailabilityAvailable bool `json:"accountRuntimeAvailabilityAvailable"`
	AccountCircuitSummaryAvailable      bool `json:"accountCircuitSummaryAvailable"`
}

// AllRuntimeSnapshotAvailable is the "nothing required" status.
func AllRuntimeSnapshotAvailable() AccountRuntimeSnapshotStatus {
	return AccountRuntimeSnapshotStatus{true, true, true}
}

// RuntimeAvailabilityLoader mirrors the distributed availability read
// (loadDistributedGatewayAccountRuntimeAvailability). ok=false reports the
// dependency as unavailable (Node catch → {available:false, values:{}}).
type RuntimeAvailabilityLoader func(ctx context.Context, runtimeKeys []string) (values AccountRuntimeAvailabilitySnapshot, ok bool, err error)

// SnapshotConcurrencyLoader mirrors loadAccountCurrentConcurrencyByIdsAsync
// for the redis runtime-state path. ok=false reports the store as unavailable.
type SnapshotConcurrencyLoader func(ctx context.Context, accountIDs []string) (values AccountConcurrencySnapshot, ok bool, err error)

// ServerSnapshotLoader mirrors requestServerAccountRuntimeSnapshot: one read
// producing the paired concurrency + availability snapshot. It feeds the
// cached server-snapshot path (Node non-redis driver).
type ServerSnapshotLoader func(ctx context.Context) (*AccountRuntimeSnapshot, error)

// runtimeSnapshotCache mirrors RuntimeSnapshotCache<T>.
type runtimeSnapshotCache[T any] struct {
	mu                 sync.Mutex
	value              T
	hasValue           bool
	updatedAtMs        int64
	refreshStartedAtMs int64
	refresh            chan struct{}
}

// RuntimeSnapshotService owns the snapshot caches. The db-service-only gate
// (runtimeConfig.processRole !== 'db-service') is a composition decision: wire
// the service only where Node would serve it.
type RuntimeSnapshotService struct {
	clock Clock
	// redisAvailability / redisConcurrency mirror the redis runtime-state
	// driver reads (direct, uncached).
	redisAvailability RuntimeAvailabilityLoader
	redisConcurrency  SnapshotConcurrencyLoader
	// serverSnapshot mirrors the cached requestServerAccountRuntimeSnapshot
	// path.
	serverSnapshot ServerSnapshotLoader

	runtimeCache runtimeSnapshotCache[AccountRuntimeSnapshot]
}

// AccountRuntimeSnapshot mirrors the internal AccountRuntimeSnapshot pair.
type AccountRuntimeSnapshot struct {
	AccountConcurrency         AccountConcurrencySnapshot
	AccountRuntimeAvailability AccountRuntimeAvailabilitySnapshot
}

// NewRuntimeSnapshotService builds the service. The redis loaders take
// precedence over the cached server snapshot exactly like the Node driver
// branches; all may be nil (reads answer available=false).
func NewRuntimeSnapshotService(clock Clock, redisAvailability RuntimeAvailabilityLoader, redisConcurrency SnapshotConcurrencyLoader, serverSnapshot ServerSnapshotLoader) *RuntimeSnapshotService {
	if clock == nil {
		clock = SystemClock()
	}
	return &RuntimeSnapshotService{
		clock:             clock,
		redisAvailability: redisAvailability,
		redisConcurrency:  redisConcurrency,
		serverSnapshot:    serverSnapshot,
	}
}

// ProbeAccountRuntimeState mirrors probeAccountRuntimeState: synthetic keys,
// availability-only answer.
func (s *RuntimeSnapshotService) ProbeAccountRuntimeState(ctx context.Context) (concurrencyAvailable, availabilityAvailable bool) {
	_, availabilityOK := s.LoadAccountRuntimeAvailabilityByKeys(ctx, []string{"__account_list_projection_runtime_probe__"})
	_, concurrencyOK := s.LoadAccountConcurrencyByIDs(ctx, []string{"__account_list_projection_concurrency_probe__"})
	return concurrencyOK, availabilityOK
}

// LoadAccountRuntimeAvailabilityByKeys mirrors
// loadAccountRuntimeAvailabilityByKeys: dedupe + filter + 100-key slice, with
// the Redis loader taking precedence over the server snapshot cache.
func (s *RuntimeSnapshotService) LoadAccountRuntimeAvailabilityByKeys(ctx context.Context, runtimeKeys []string) (AccountRuntimeAvailabilitySnapshot, bool) {
	keys := normalizeSnapshotKeys(runtimeKeys)
	if len(keys) > snapshotAvailabilityKeyLimit {
		keys = keys[:snapshotAvailabilityKeyLimit]
	}
	if s.redisAvailability != nil {
		values, ok, err := s.redisAvailability(ctx, keys)
		if err == nil && ok {
			return filterAvailabilityKeys(values, keys), true
		}
		return AccountRuntimeAvailabilitySnapshot{}, false
	}
	runtime, ok := s.loadServerAccountRuntimeSnapshot(ctx)
	if !ok || runtime.AccountRuntimeAvailability == nil {
		return AccountRuntimeAvailabilitySnapshot{}, false
	}
	return filterAvailabilityKeys(runtime.AccountRuntimeAvailability, keys), true
}

// LoadAccountConcurrencyByIDs mirrors loadAccountConcurrencyByIds.
func (s *RuntimeSnapshotService) LoadAccountConcurrencyByIDs(ctx context.Context, accountIDs []string) (AccountConcurrencySnapshot, bool) {
	ids := normalizeSnapshotKeys(accountIDs)
	if s.redisConcurrency != nil {
		values, ok, err := s.redisConcurrency(ctx, ids)
		if err == nil && ok {
			return values, true
		}
		return AccountConcurrencySnapshot{}, false
	}
	runtime, ok := s.loadServerAccountRuntimeSnapshot(ctx)
	if !ok || runtime.AccountConcurrency == nil {
		return AccountConcurrencySnapshot{}, false
	}
	out := AccountConcurrencySnapshot{}
	for _, id := range ids {
		out[id] = numberValue(runtime.AccountConcurrency[id])
	}
	return out, true
}

// LoadServerAccountRuntimeAvailabilitySnapshot mirrors
// loadServerAccountRuntimeAvailabilitySnapshot.
func (s *RuntimeSnapshotService) LoadServerAccountRuntimeAvailabilitySnapshot(ctx context.Context) (AccountRuntimeAvailabilitySnapshot, bool) {
	runtime, ok := s.loadServerAccountRuntimeSnapshot(ctx)
	if !ok || runtime.AccountRuntimeAvailability == nil {
		return nil, false
	}
	return runtime.AccountRuntimeAvailability, true
}

// PeekServerAccountRuntimeAvailabilitySnapshot mirrors
// peekServerAccountRuntimeAvailabilitySnapshot: schedule a refresh when stale,
// answer only within the max-stale window.
func (s *RuntimeSnapshotService) PeekServerAccountRuntimeAvailabilitySnapshot(ctx context.Context) (AccountRuntimeAvailabilitySnapshot, bool) {
	snapshot, ok := s.peekServerAccountRuntimeSnapshot(ctx)
	if !ok || snapshot.AccountRuntimeAvailability == nil {
		return nil, false
	}
	return snapshot.AccountRuntimeAvailability, true
}

// loadServerAccountRuntimeSnapshot mirrors loadCachedServerRuntimeSnapshot for
// the runtime pair: fresh within 300ms, stale-tolerant to 5s with a scheduled
// refresh, singleflight beyond.
func (s *RuntimeSnapshotService) loadServerAccountRuntimeSnapshot(ctx context.Context) (AccountRuntimeSnapshot, bool) {
	cache := &s.runtimeCache
	now := s.clock.Now().UnixMilli()
	cache.mu.Lock()
	if cache.hasValue && now-cache.updatedAtMs <= serverRuntimeSnapshotCacheTTL.Milliseconds() {
		value := cache.value
		cache.mu.Unlock()
		return value, true
	}
	if cache.hasValue && now-cache.updatedAtMs <= serverRuntimeSnapshotMaxStale.Milliseconds() {
		s.scheduleSnapshotRefreshLocked(cache, now)
		value := cache.value
		cache.mu.Unlock()
		return value, true
	}
	if cache.refresh != nil {
		refresh := cache.refresh
		cache.mu.Unlock()
		awaitRefresh(ctx, refresh)
		cache.mu.Lock()
		value, has := cache.value, cache.hasValue
		cache.mu.Unlock()
		return value, has
	}
	refresh := make(chan struct{})
	cache.refresh = refresh
	cache.refreshStartedAtMs = now
	cache.mu.Unlock()

	value, ok := s.refreshSnapshot(ctx, cache, refresh)
	return value, ok
}

// peekServerAccountRuntimeSnapshot mirrors peekCachedServerRuntimeSnapshot.
func (s *RuntimeSnapshotService) peekServerAccountRuntimeSnapshot(ctx context.Context) (AccountRuntimeSnapshot, bool) {
	cache := &s.runtimeCache
	now := s.clock.Now().UnixMilli()
	cache.mu.Lock()
	ageMs := now - cache.updatedAtMs
	if !cache.hasValue || ageMs > serverRuntimeSnapshotCacheTTL.Milliseconds() {
		s.scheduleSnapshotRefreshLocked(cache, now)
	}
	value, has := cache.value, cache.hasValue
	cache.mu.Unlock()
	if has && ageMs <= serverRuntimeSnapshotMaxStale.Milliseconds() {
		return value, true
	}
	return AccountRuntimeSnapshot{}, false
}

// scheduleSnapshotRefreshLocked mirrors scheduleServerRuntimeSnapshotRefresh;
// caller holds cache.mu.
func (s *RuntimeSnapshotService) scheduleSnapshotRefreshLocked(cache *runtimeSnapshotCache[AccountRuntimeSnapshot], now int64) {
	if cache.refresh != nil || now-cache.refreshStartedAtMs < serverRuntimeSnapshotRefreshInterval.Milliseconds() {
		return
	}
	refresh := make(chan struct{})
	cache.refresh = refresh
	cache.refreshStartedAtMs = now
	go func() {
		defer close(refresh)
		ctx, cancel := context.WithTimeout(context.Background(), serverRuntimeSnapshotMaxStale*4)
		defer cancel()
		s.refreshSnapshot(ctx, cache, refresh)
	}()
}

// refreshSnapshot mirrors refreshServerRuntimeSnapshotCache: only a successful
// loader result replaces the cached value; the old value answers meanwhile.
func (s *RuntimeSnapshotService) refreshSnapshot(ctx context.Context, cache *runtimeSnapshotCache[AccountRuntimeSnapshot], refresh chan struct{}) (AccountRuntimeSnapshot, bool) {
	defer func() {
		cache.mu.Lock()
		cache.refresh = nil
		cache.mu.Unlock()
	}()
	var value *AccountRuntimeSnapshot
	var err error
	if s.serverSnapshot != nil {
		value, err = s.serverSnapshot(ctx)
	} else {
		err = errSnapshotUnavailable
	}
	if err == nil && value != nil {
		cache.mu.Lock()
		cache.value = *value
		cache.hasValue = true
		cache.updatedAtMs = s.clock.Now().UnixMilli()
		snapshot := cache.value
		cache.mu.Unlock()
		return snapshot, true
	}
	cache.mu.Lock()
	cached, has := cache.value, cache.hasValue
	cache.mu.Unlock()
	return cached, has
}

var errSnapshotUnavailable = errSnapshotUnavailableError{}

type errSnapshotUnavailableError struct{}

func (errSnapshotUnavailableError) Error() string { return "运行时快照依赖不可用" }

// awaitRefresh waits for an in-flight refresh best-effort.
func awaitRefresh(ctx context.Context, refresh chan struct{}) {
	select {
	case <-refresh:
	case <-ctx.Done():
	}
}

// normalizeSnapshotKeys mirrors [...new Set(keys.filter(Boolean))] ordering.
func normalizeSnapshotKeys(keys []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func filterAvailabilityKeys(values AccountRuntimeAvailabilitySnapshot, keys []string) AccountRuntimeAvailabilitySnapshot {
	out := AccountRuntimeAvailabilitySnapshot{}
	for _, key := range keys {
		if value, ok := values[key]; ok {
			out[key] = value
		}
	}
	return out
}

// numberValue mirrors numberValue: strings and non-finite floats collapse to 0.
func numberValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		if isNaN(typed) || isInf(typed) {
			return 0
		}
		return int(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil || isNaN(parsed) || isInf(parsed) {
			return 0
		}
		return int(parsed)
	case string:
		parsed, err := json.Number(typed).Float64()
		if err != nil || isNaN(parsed) || isInf(parsed) {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}

func isNaN(value float64) bool { return value != value }

func isInf(value float64) bool { return value > 1.7976931348623157e308 || value < -1.7976931348623157e308 }

// AccountRuntimeAvailabilityKey mirrors accountRuntimeAvailabilityKey: the
// authorized-projection key format shared with the account list slice.
func AccountRuntimeAvailabilityKey(accountID, accessType, accountAuthorizationID, boundGroupID, bindingSystemAccountID, systemAccountID, ownerSystemAccountID string) string {
	if accessType == "authorized" && accountAuthorizationID != "" && boundGroupID != "" {
		systemAccount := bindingSystemAccountID
		if systemAccount == "" {
			systemAccount = systemAccountID
		}
		if systemAccount == "" {
			systemAccount = ownerSystemAccountID
		}
		if systemAccount != "" {
			return accountID + ":authorized:" + systemAccount + ":" + boundGroupID + ":" + accountAuthorizationID
		}
	}
	return accountID
}

// SortSnapshotKeys keeps deterministic iteration for tests/diagnostics.
func SortSnapshotKeys(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}
