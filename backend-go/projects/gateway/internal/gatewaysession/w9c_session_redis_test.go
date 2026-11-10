package gatewaysession

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

// ---------------------------------------------------------------------------
// lazyRedisClient full surface against a real miniredis.
// ---------------------------------------------------------------------------

func TestW9CLazyRedisClientSurface(t *testing.T) {
	server := miniredis.RunT(t)
	lazy := newLazyRedisClient("redis://" + server.Addr())
	ctx := context.Background()

	if value, err := lazy.Get(ctx, "missing"); err != nil || value != nil {
		t.Fatalf("lazy get = %v, %v", value, err)
	}
	if err := lazy.SetPX(ctx, "k", "v", 60_000); err != nil {
		t.Fatalf("lazy setpx: %v", err)
	}
	if value, err := lazy.Get(ctx, "k"); err != nil || value == nil || *value != "v" {
		t.Fatalf("lazy get after set = %v, %v", value, err)
	}
	result, err := lazy.Eval(ctx, "return 1", []string{})
	if err != nil || result == nil {
		t.Fatalf("lazy eval = %v, %v", result, err)
	}
	if pong, err := lazy.SendCommand(ctx, "PING"); err != nil || pong != "PONG" {
		t.Fatalf("lazy ping = %v, %v", pong, err)
	}
	if err := lazy.Del(ctx, "k"); err != nil {
		t.Fatalf("lazy del: %v", err)
	}
	if err := lazy.Close(); err != nil {
		t.Fatalf("lazy close: %v", err)
	}
	// A second dial happens transparently after Close.
	if err := lazy.SetPX(ctx, "k2", "v2", 1000); err != nil {
		t.Fatalf("lazy re-dial: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Redis driver affinity flows (binding lifecycle, migrate, traffic
// preference, async ordering).
// ---------------------------------------------------------------------------

func TestW9CRedisDriverRememberForgetAndMigrate(t *testing.T) {
	svc, clock, logger, mr := newRedisAffinityService(t)
	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// Sync remember under the redis driver is fire-and-forget: wait for the
	// goroutine to land the binding in miniredis.
	svc.RememberOpenAIAccountForSession("w9c-aff-1", "acc-1", scope)
	deadline := time.Now().Add(3 * time.Second)
	bound := ""
	for time.Now().Before(deadline) {
		if owner, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1", scope); ok && owner == "acc-1" {
			bound = owner
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if bound != "acc-1" {
		t.Fatalf("redis binding never landed: %q", bound)
	}
	// Re-claiming the same account refreshes (still owner).
	if owner, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1", scope); !ok || owner != "acc-1" {
		t.Fatalf("re-claim = %q %v", owner, ok)
	}
	// A different account cannot steal the binding: the claim reports the
	// current owner (first binder wins).
	if owner, _ := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-2", scope); owner != "acc-1" {
		t.Fatalf("steal must report the existing owner, got %q", owner)
	}

	// The sync forget under the redis driver is refused with a warn.
	svc.ForgetOpenAIAccountForSession("w9c-aff-1", "acc-1")
	if !logger.HasEvent("redis_openai_session_affinity_sync_forget_ignored") {
		t.Fatalf("sync forget must warn, events = %v", logger.Events())
	}
	// The async forget removes the binding.
	if err := svc.ForgetOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1"); err != nil {
		t.Fatalf("async forget: %v", err)
	}
	if _, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1", scope); !ok {
		t.Fatal("binding must be re-claimable after forget")
	}

	// Migration moves every binding of the source account to the target.
	svc.RememberOpenAIAccountForSessionAsync(ctx, "w9c-aff-m1", "acc-m", scope)
	svc.RememberOpenAIAccountForSessionAsync(ctx, "w9c-aff-m2", "acc-m", scope)
	result, err := svc.MigrateOpenAIAccountSessionAffinityAsync(ctx, "acc-m", "acc-mt", scope, MigrationOptions{PreferMigratedSessions: true})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.MigratedSessionCount != 2 {
		t.Fatalf("migrated = %d", result.MigratedSessionCount)
	}
	if owner, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-m1", "acc-mt", scope); !ok || owner != "acc-mt" {
		t.Fatalf("migrated binding owner = %q %v", owner, ok)
	}

	// TTL expiry drops the redis binding.
	clock.Advance(time.Duration(sessionAffinityTtlMs+redisSessionAffinityIndexTtlPaddingMs) * time.Millisecond)
	mr.FastForward(time.Duration(sessionAffinityTtlMs) * time.Millisecond)
	if _, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-m1", "acc-mt", scope); !ok {
		t.Fatal("binding must be re-claimable after ttl expiry")
	}
}

func TestW9CRedisDriverTrafficMigrationPreference(t *testing.T) {
	svc, clock, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	// Async write with ThrowOnRedisError succeeds against miniredis.
	if err := svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-a", "acc-b", scope, TrafficMigrationPreferenceWriteOptions{ThrowOnRedisError: true}); err != nil {
		t.Fatalf("redis preference write: %v", err)
	}
	preference := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-b", "acc-x"}, scope)
	if preference == nil || preference.TargetAccountID != "acc-b" {
		t.Fatalf("redis preference = %+v", preference)
	}
	// Source present consumes and deletes the redis preference.
	if consumed := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-a", "acc-b"}, scope); consumed != nil {
		t.Fatalf("consumed preference = %+v", consumed)
	}
	if again := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-b", "acc-x"}, scope); again != nil {
		t.Fatalf("preference must stay deleted: %+v", again)
	}

	// Corrupted payload degrades to nil (and is cleaned up).
	svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-a", "acc-b", scope, TrafficMigrationPreferenceWriteOptions{})
	key, keyErr := redisTrafficMigrationPreferenceKey(svc.cfg.RedisNamespace, "sys-1:*:grp-1")
	if keyErr != nil {
		t.Fatalf("preference key: %v", keyErr)
	}
	if err := mr.Set(key, "{corrupted"); err != nil {
		t.Fatalf("seed corruption: %v", err)
	}
	if corrupted := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-b", "acc-x"}, scope); corrupted != nil {
		t.Fatalf("corrupted preference = %+v", corrupted)
	}
	if raw, getErr := mr.Get(key); getErr == nil && raw != "" {
		t.Fatalf("corrupted preference must be deleted, got %q", raw)
	}

	// Redis outage surfaces through ThrowOnRedisError only.
	mr.Close()
	if err := svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-c", "acc-d", scope, TrafficMigrationPreferenceWriteOptions{ThrowOnRedisError: true}); err == nil {
		t.Fatal("outage must surface with ThrowOnRedisError")
	}
	// Without the flag the write failure only warns.
	if err := svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-c", "acc-d", scope, TrafficMigrationPreferenceWriteOptions{}); err != nil {
		t.Fatalf("outage without throw = %v", err)
	}
	_ = clock
}
