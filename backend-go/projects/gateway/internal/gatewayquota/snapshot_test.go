package gatewayquota

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func snapshotFixture(generatedAt string, costComplete bool, authzComplete bool) GatewayQuotaSnapshot {
	hours := 3
	completeCost := costComplete
	completeAuthz := authzComplete
	return GatewayQuotaSnapshot{
		GeneratedAt: generatedAt,
		CostEntries: []QuotaCostSnapshotEntry{
			{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: &hours,
				Costs: RequestQuotaCosts{Hourly: 1, Daily: 2, Weekly: 3, Monthly: 4, Total: 5}},
		},
		AuthorizationEntries: []AuthorizationQuotaSnapshotEntry{
			{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga", Decision: DeniedDecision(AuthorizationQuotaExceededMessage)},
			{ScopeType: ScopeTypeAccountAuthorization, AuthorizationID: "aa", Decision: AllowedDecision()},
		},
		CostEntriesComplete:          &completeCost,
		AuthorizationEntriesComplete: &completeAuthz,
	}
}

func TestSnapshotCacheMemoryMode(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	cache, err := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	ctx := context.Background()

	generatedAt := "2026-09-04T00:00:00.000Z"
	if err := cache.ReplaceGatewayQuotaSnapshot(snapshotFixture(generatedAt, true, true)); err != nil {
		t.Fatalf("ReplaceGatewayQuotaSnapshot: %v", err)
	}
	if !cache.IsCostSnapshotComplete() || !cache.IsAuthorizationSnapshotComplete() {
		t.Fatal("complete snapshot must report complete")
	}
	if cache.IsCostSnapshotIncomplete() || cache.IsAuthorizationSnapshotIncomplete() {
		t.Fatal("complete snapshot must not report incomplete")
	}
	if got := cache.AuthorizationQuotaSnapshotVersion(); got != 1 {
		t.Fatalf("version = %d, want 1", got)
	}

	costs, ok := cache.ReadCostsSnapshot(QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: intPtr(3)})
	if !ok || costs.Total != 5 {
		t.Fatalf("ReadCostsSnapshot = (%+v, %v)", costs, ok)
	}
	decision, ok := cache.ReadAuthorizationSnapshot(ScopeTypeGroupAuthorization, "ga")
	if !ok || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("ReadAuthorizationSnapshot = (%+v, %v)", decision, ok)
	}
	if _, ok := cache.ReadAuthorizationSnapshot(ScopeTypeGroupAuthorization, ""); ok {
		t.Fatal("empty authorization id must miss")
	}

	// Mutating the returned clone must not affect the cache.
	costs.Total = 99
	again, _ := cache.ReadCostsSnapshot(QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: intPtr(3)})
	if again.Total != 5 {
		t.Fatalf("snapshot clone leaked mutation: %+v", again)
	}

	// Redis-mode-only readers stay inert in memory mode.
	if _, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: intPtr(3)}); err != nil || !ok {
		t.Fatalf("ReadCostsSnapshotAsync in memory mode must fall through to memory: ok=%v err=%v", ok, err)
	}
}

func TestSnapshotCacheMemoryIncompleteAndInvalidation(t *testing.T) {
	var logs []string
	clock := newFakeClock(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	cache, err := NewSnapshotCache(Modes{}, nil, clock.Now, func(event string, fields map[string]any, message string) {
		logs = append(logs, event)
	})
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	if err := cache.ReplaceGatewayQuotaSnapshot(snapshotFixture("2026-09-04T00:00:00.000Z", false, false)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !cache.IsCostSnapshotIncomplete() || !cache.IsAuthorizationSnapshotIncomplete() {
		t.Fatal("incomplete flags must be observable")
	}
	if cache.IsCostSnapshotComplete() || cache.IsAuthorizationSnapshotComplete() {
		t.Fatal("incomplete snapshot must not be complete")
	}
	if len(logs) != 1 || logs[0] != "gateway_quota_snapshot_incomplete" {
		t.Fatalf("incomplete warn log = %v", logs)
	}

	// Invalidate with publishedAt newer than generatedAt -> unusable.
	newer := "2026-09-04T01:00:00.000Z"
	if err := cache.InvalidateAuthorizationQuotaSnapshot(&newer); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, ok := cache.ReadAuthorizationSnapshot(ScopeTypeAccountAuthorization, "aa"); ok {
		t.Fatal("invalidated snapshot must not serve authorization decisions")
	}
	if !cache.IsAuthorizationSnapshotIncomplete() || cache.IsAuthorizationSnapshotComplete() {
		t.Fatal("post-invalidation completeness flags wrong")
	}
	if got := cache.AuthorizationQuotaSnapshotVersion(); got != 2 {
		t.Fatalf("version after invalidate = %d, want 2", got)
	}

	// Older publishedAt must not push the watermark back (monotonic max).
	older := "2026-09-03T23:00:00.000Z"
	if err := cache.InvalidateAuthorizationQuotaSnapshot(&older); err != nil {
		t.Fatalf("invalidate older: %v", err)
	}

	if err := cache.InvalidateAuthorizationQuotaSnapshot(nil); err != nil {
		t.Fatalf("invalidate nil: %v", err)
	}

	cache.ClearGatewayQuotaSnapshot()
	if _, ok := cache.ReadCostsSnapshot(QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak"}); ok {
		t.Fatal("cleared snapshot must miss")
	}
	if cache.IsCostSnapshotIncomplete() {
		t.Fatal("cleared snapshot without generatedAt is not incomplete")
	}
}

func TestSnapshotCacheInvalidGeneratedAt(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	cache, _ := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
	snapshot := snapshotFixture("2026-09-04 00:00:00", true, true)
	err := cache.ReplaceGatewayQuotaSnapshot(snapshot)
	if err == nil || err.Error() != "网关额度快照 generatedAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("invalid generatedAt error = %v", err)
	}
	bad := "2026-09-04T01:00:00.000Z"
	err = cache.InvalidateAuthorizationQuotaSnapshot(&bad)
	_ = err // valid, ignored
	reallyBad := "nope"
	if err := cache.InvalidateAuthorizationQuotaSnapshot(&reallyBad); err == nil ||
		err.Error() != "网关额度快照授权失效 publishedAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("invalid publishedAt error = %v", err)
	}
}

func newSnapshotRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client, *RedisRuntimeState) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisRuntimeStateStore(client, "dev", GatewayQuotaSnapshotRuntimeStateStoreName)
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	return server, client, store
}

func TestSnapshotCacheRedisMode(t *testing.T) {
	server, client, store := newSnapshotRedis(t)
	clock := newFakeClock(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	modes := Modes{RedisCache: true, RedisRuntimeState: true}
	cache, err := NewSnapshotCache(modes, store, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	ctx := context.Background()

	writeSnapshot := func(t *testing.T, snapshot GatewayQuotaSnapshot) {
		t.Helper()
		if err := store.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, snapshot, time.Hour); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
	}

	writeSnapshot(t, snapshotFixture("2026-09-04T00:00:00.000Z", true, true))
	// Memory-mode replace clears everything in redis mode (mirrors Node).
	if err := cache.ReplaceGatewayQuotaSnapshot(snapshotFixture("2026-09-04T00:00:00.000Z", true, true)); err != nil {
		t.Fatalf("replace in redis mode: %v", err)
	}

	costs, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: intPtr(3)})
	if err != nil || !ok || costs.Total != 5 {
		t.Fatalf("redis ReadCostsSnapshotAsync = (%+v, %v, %v)", costs, ok, err)
	}
	decision, ok, err := cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga")
	if err != nil || !ok || decision.Allowed {
		t.Fatalf("redis ReadAuthorizationSnapshotAsync = (%+v, %v, %v)", decision, ok, err)
	}
	if complete, err := cache.IsCostSnapshotCompleteAsync(ctx); err != nil || !complete {
		t.Fatalf("IsCostSnapshotCompleteAsync = (%v, %v)", complete, err)
	}
	info := cache.SnapshotRuntime()
	if info.CostEntryCount != 1 || info.AuthorizationEntryCount != 2 || !info.CostEntriesComplete {
		t.Fatalf("SnapshotRuntime = %+v", info)
	}

	// 1s memo: mutate the stored document; the cached read persists.
	snapshot := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	snapshot.CostEntries[0].Costs.Total = 42
	writeSnapshot(t, snapshot)
	clock.Advance(500 * time.Millisecond)
	cached, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: intPtr(3)})
	if err != nil || !ok || cached.Total != 5 {
		t.Fatalf("memo must serve the stale read: (%+v, %v, %v)", cached, ok, err)
	}
	clock.Advance(600 * time.Millisecond)
	refreshed, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: intPtr(3)})
	if err != nil || !ok || refreshed.Total != 42 {
		t.Fatalf("memo expiry must re-read: (%+v, %v, %v)", refreshed, ok, err)
	}

	// Authorization invalidation: watermark newer than generatedAt makes the
	// shared snapshot unusable for authorization scopes and flips the
	// incomplete flag; the cost scopes keep working.
	publishedAt := "2026-09-04T02:00:00.000Z"
	if err := cache.InvalidateAuthorizationQuotaSnapshot(&publishedAt); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, ok, err := cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga"); err != nil || ok {
		t.Fatalf("invalidated authorization scope must miss: (%v, %v)", ok, err)
	}
	if incomplete, err := cache.IsAuthorizationSnapshotIncompleteAsync(ctx); err != nil || !incomplete {
		t.Fatalf("IsAuthorizationSnapshotIncompleteAsync after invalidate = (%v, %v)", incomplete, err)
	}
	if incomplete, err := cache.IsAuthorizationSnapshotCompleteAsync(ctx); err != nil || incomplete {
		t.Fatalf("IsAuthorizationSnapshotCompleteAsync after invalidate = (%v, %v)", incomplete, err)
	}
	if _, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: intPtr(3)}); err != nil || !ok {
		t.Fatalf("costs must survive authorization invalidation: (%v, %v)", ok, err)
	}

	// A snapshot generated after the watermark becomes usable again and the
	// invalidation clears (version bump observable).
	versionBefore := cache.AuthorizationQuotaSnapshotVersion()
	snapshot = snapshotFixture("2026-09-04T03:00:00.000Z", true, true)
	writeSnapshot(t, snapshot)
	clock.Advance(2 * time.Second)
	decision, ok, err = cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga")
	if err != nil || !ok {
		t.Fatalf("newer snapshot must be usable: (%v, %v)", ok, err)
	}
	// The fixture denies the group authorization scope.
	if decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("replaced snapshot decision mismatch: %+v", decision)
	}
	if cache.AuthorizationQuotaSnapshotVersion() <= versionBefore {
		t.Fatalf("version must bump on usable shared snapshot: %d -> %d", versionBefore, cache.AuthorizationQuotaSnapshotVersion())
	}

	// Missing document -> degraded to no snapshot (no error).
	if err := client.Del(ctx, mustRedisKey(t, store, GatewayQuotaSnapshotRuntimeStateKey)).Err(); err != nil {
		t.Fatalf("del: %v", err)
	}
	clock.Advance(2 * time.Second)
	if ok, err := cache.HasGatewayQuotaSnapshotAsync(ctx); err != nil || ok {
		t.Fatalf("HasGatewayQuotaSnapshotAsync without document = (%v, %v)", ok, err)
	}

	// Transport failure degrades to no snapshot without error.
	clock.Advance(2 * time.Second)
	server.Close()
	if _, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak"}); err != nil || ok {
		t.Fatalf("transport failure must degrade: (%v, %v)", ok, err)
	}
}

func mustRedisKey(t *testing.T, store *RedisRuntimeState, key string) string {
	t.Helper()
	location, err := store.key(key)
	if err != nil {
		t.Fatalf("redis key: %v", err)
	}
	return location
}

func TestSnapshotCacheRedisInvalidGeneratedAt(t *testing.T) {
	_, _, store := newSnapshotRedis(t)
	clock := newFakeClock(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	cache, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, store, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	ctx := context.Background()
	if err := store.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey,
		snapshotFixture("not-a-timestamp", true, true), time.Hour); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err = cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak"})
	if err == nil || err.Error() != "Redis runtime state 网关额度快照 generatedAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("invalid redis generatedAt error = %v", err)
	}
}

func intPtr(v int) *int { return &v }
