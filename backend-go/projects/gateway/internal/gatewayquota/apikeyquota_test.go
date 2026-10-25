package gatewayquota

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// mockDBService records calls and returns canned results.
type mockDBService struct {
	mu sync.Mutex

	checkAPIKeyCalls int
	checkAPIKeyArgs  []APIKeyRow
	checkAPIKeyDec   Decision
	checkAPIKeyErr   error

	readCostsCalls int
	readCostsArgs  []APIKeyRow
	readCosts      RequestQuotaCosts
	readCostsErr   error

	checkAuthzCalls int
	checkAuthzGroup []string
	checkAuthzAcct  []string
	checkAuthzDec   Decision
	checkAuthzErr   error

	checkBatchCalls int
	checkBatchGroup []string
	checkBatchAccts [][]AccountRef
	checkBatchDecs  []Decision
	checkBatchErr   error
}

func (m *mockDBService) CheckAPIKeyQuota(_ context.Context, apiKey APIKeyRow) (Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkAPIKeyCalls++
	m.checkAPIKeyArgs = append(m.checkAPIKeyArgs, apiKey)
	return m.checkAPIKeyDec, m.checkAPIKeyErr
}

func (m *mockDBService) ReadAPIKeyQuotaCosts(_ context.Context, apiKey APIKeyRow) (RequestQuotaCosts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readCostsCalls++
	m.readCostsArgs = append(m.readCostsArgs, apiKey)
	return m.readCosts, m.readCostsErr
}

func (m *mockDBService) CheckAuthorizationQuota(_ context.Context, groupAuthorizationID, accountAuthorizationID string) (Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkAuthzCalls++
	m.checkAuthzGroup = append(m.checkAuthzGroup, groupAuthorizationID)
	m.checkAuthzAcct = append(m.checkAuthzAcct, accountAuthorizationID)
	return m.checkAuthzDec, m.checkAuthzErr
}

func (m *mockDBService) CheckAuthorizationQuotaBatch(_ context.Context, groupAuthorizationID string, accounts []AccountRef) ([]Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkBatchCalls++
	m.checkBatchGroup = append(m.checkBatchGroup, groupAuthorizationID)
	m.checkBatchAccts = append(m.checkBatchAccts, accounts)
	return m.checkBatchDecs, m.checkBatchErr
}

type logRecorder struct {
	mu    sync.Mutex
	items []string
}

func (r *logRecorder) hook(event string, _ map[string]any, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, event+"|"+message)
}

func (r *logRecorder) has(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.items {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

const testQuotaLimits = `{"daily":{"enabled":true,"limit":10}}`

func TestCheckAPIKeyQuotaBoundaries(t *testing.T) {
	db := newTestDB(t, "apikeyquota")
	statsSchema(t, db)
	stats, _ := NewStatsStore(db)
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	tz, _ := NewStaticTimezoneSource(time.UTC)
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{Modes: Modes{}, Stats: stats, Timezone: tz, Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	now := clock.Now()

	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-09-04", 5})

	decision, err := service.CheckAPIKeyQuota(ctx, apiKey, now)
	if err != nil || !decision.Allowed {
		t.Fatalf("within quota must allow: (%+v, %v)", decision, err)
	}

	// Exactly at the limit denies (>=).
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak2", "2026-09-04", 10})
	exactKey := APIKeyRow{ID: "ak2", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	decision, err = service.CheckAPIKeyQuota(ctx, exactKey, now)
	if err != nil || decision.Allowed || decision.Message != APIKeyQuotaExceededMessage {
		t.Fatalf("exactly at limit must deny with message: (%+v, %v)", decision, err)
	}
	if decision.Message != "额度已用完，请联系管理员提升额度" {
		t.Fatalf("429 copy mismatch: %q", decision.Message)
	}

	// Over the limit denies.
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak3", "2026-09-04", 11})
	decision, err = service.CheckAPIKeyQuota(ctx, APIKeyRow{ID: "ak3", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}, now)
	if err != nil || decision.Allowed {
		t.Fatalf("over limit must deny: (%+v, %v)", decision, err)
	}

	// No enabled limits always allows without touching the stats tables.
	decision, err = service.CheckAPIKeyQuota(ctx, APIKeyRow{ID: "free", SystemAccountID: "sys", QuotaLimitsJSON: ""}, now)
	if err != nil || !decision.Allowed || decision.Message != "" {
		t.Fatalf("no limits must allow: (%+v, %v)", decision, err)
	}
}

func TestCheckAPIKeyQuotaInvalidConfig(t *testing.T) {
	db := newTestDB(t, "apikey-invalid")
	statsSchema(t, db)
	stats, _ := NewStatsStore(db)
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, _ := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{}, Stats: stats, Timezone: mustTZ(t, time.UTC), Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	decision, err := service.CheckAPIKeyQuota(context.Background(), APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: `{"daily":{"enabled":true,"limit":-1}}`}, clock.Now())
	if err == nil || !strings.Contains(err.Error(), "日额度金额必须是大于 0 的数字") {
		t.Fatalf("invalid config must surface normalization error, got (%+v, %v)", decision, err)
	}
}

func TestCheckAPIKeyQuotaCacheAndInvalidation(t *testing.T) {
	db := newTestDB(t, "apikey-cache")
	statsSchema(t, db)
	stats, _ := NewStatsStore(db)
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{}, Stats: stats, Timezone: mustTZ(t, time.UTC), Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}

	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-09-04", 1})
	if decision, err := service.CheckAPIKeyQuota(ctx, apiKey, clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("first check: (%+v, %v)", decision, err)
	}

	// Push the scope over the limit; the 5s runtime cache must still allow.
	if _, err := db.Exec(`UPDATE juhe_stats.usage_stats_daily SET success_cost_usd = 51 WHERE system_account_id = 'sys' AND scope_type = 'api_key' AND scope_id = 'ak'`); err != nil {
		t.Fatalf("update cost: %v", err)
	}
	if decision, err := service.CheckAPIKeyQuota(ctx, apiKey, clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("cached decision must persist: (%+v, %v)", decision, err)
	}
	// ReadOnly bypasses the cache.
	if decision, err := service.CheckAPIKeyQuotaReadOnly(ctx, apiKey, clock.Now()); err != nil || decision.Allowed {
		t.Fatalf("read-only check must be fresh: (%+v, %v)", decision, err)
	}

	// Window reset: after the daily key rolls over the fresh load allows.
	clock.Advance(6 * time.Second)
	// TTL expiry forces a reload; the old window still exceeds (cost 51).
	if decision, err := service.CheckAPIKeyQuota(ctx, apiKey, clock.Now()); err != nil || decision.Allowed {
		t.Fatalf("TTL expiry must reload fresh (still exceeded): (%+v, %v)", decision, err)
	}
	clock.Advance(20 * time.Hour)
	if decision, err := service.CheckAPIKeyQuota(ctx, apiKey, clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("window reset must allow: (%+v, %v)", decision, err)
	}

	// Back to the old window: the cached entry was evicted; reload denies.
	clock.Advance(-20 * time.Hour)
	if decision, err := service.CheckAPIKeyQuota(ctx, apiKey, clock.Now()); err != nil || decision.Allowed {
		t.Fatalf("reload in exceeded window must deny: (%+v, %v)", decision, err)
	}

	// InvalidateByID clears just this key's entries.
	if decision, err := service.CheckAPIKeyQuota(ctx, apiKey, clock.Now()); err != nil || decision.Allowed {
		t.Fatalf("pre-invalidation deny: (%+v, %v)", decision, err)
	}
	service.InvalidateByID(ctx, "ak")
	// Still denied after invalidation (fresh load), proving the cache cleared.
	if decision, err := service.CheckAPIKeyQuota(ctx, apiKey, clock.Now()); err != nil || decision.Allowed {
		t.Fatalf("post-invalidation reload: (%+v, %v)", decision, err)
	}
	service.ClearCache(ctx)
	other := APIKeyRow{ID: "other", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	if decision, err := service.CheckAPIKeyQuota(ctx, other, clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("unlimited other key: (%+v, %v)", decision, err)
	}
}

func newRedisQuotaStack(t *testing.T, clock *fakeClock) (*miniredis.Miniredis, *RedisSharedCache, *SnapshotCache, *RedisRuntimeState) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	shared, err := NewRedisSharedCache(client, "dev", "gateway:api-key-quota")
	if err != nil {
		t.Fatalf("NewRedisSharedCache: %v", err)
	}
	runtimeState, err := NewRedisRuntimeStateStore(client, "dev", GatewayQuotaSnapshotRuntimeStateStoreName)
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	modes := Modes{RedisCache: true, RedisRuntimeState: true}
	snapshot, err := NewSnapshotCache(modes, runtimeState, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	return server, shared, snapshot, runtimeState
}

func TestCheckAPIKeyQuotaAsyncSharedCacheHit(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	_, shared, snapshot, _ := newRedisQuotaStack(t, clock)
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true},
		Stats:    nil,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Shared:   shared,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	cacheKey := mustAPIKeyCacheKey(t, service, apiKey, clock.Now())
	denied := newCachedDecision(DeniedDecision(APIKeyQuotaExceededMessage), clock.Now().UnixMilli())
	if err := shared.Set(ctx, sharedCacheKey(cacheKey), denied, apiKeyQuotaCacheTTL); err != nil {
		t.Fatalf("seed shared: %v", err)
	}
	decision, err := service.CheckAPIKeyQuotaAsync(ctx, apiKey)
	if err != nil || decision.Allowed || decision.Message != APIKeyQuotaExceededMessage {
		t.Fatalf("shared hit must return the cached denial: (%+v, %v)", decision, err)
	}
}

func mustTZ(t *testing.T, loc *time.Location) StatsTimezoneProvider {
	t.Helper()
	provider, err := NewStaticTimezoneSource(loc)
	if err != nil {
		t.Fatalf("NewStaticTimezoneSource: %v", err)
	}
	return provider
}

func mustSnapshotCache(t *testing.T, modes Modes, clock *fakeClock) *SnapshotCache {
	t.Helper()
	if modes.RedisCache || modes.RedisRuntimeState {
		t.Fatal("use newRedisQuotaStack for redis-mode snapshot caches")
	}
	cache, err := NewSnapshotCache(modes, nil, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	return cache
}

func mustAPIKeyCacheKey(t *testing.T, service *APIKeyQuotaService, apiKey APIKeyRow, now time.Time) string {
	t.Helper()
	key, err := service.apiKeyQuotaCacheKey(context.Background(), apiKey, now, 0, false)
	if err != nil {
		t.Fatalf("apiKeyQuotaCacheKey: %v", err)
	}
	return key
}
