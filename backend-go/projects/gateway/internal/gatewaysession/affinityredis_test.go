package gatewaysession

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	goredis "github.com/redis/go-redis/v9"
)

// newRedisAffinityService starts a miniredis-backed service.
func newRedisAffinityService(t *testing.T) (*AffinityService, *fakeClock, *captureLogger, *miniredis.Miniredis) {
	t.Helper()
	goredis.SetLogger(quietRedisLogger{})
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), MaxRetries: 2,
		MinRetryBackoff: 5 * time.Millisecond, MaxRetryBackoff: 25 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	redisClient := NewGoRedisClient(client)
	var service *AffinityService
	service, clock, logger := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.Redis = redisClient
		cfg.Concurrency = newMockConcurrency()
	})
	return service, clock, logger, mr
}

func TestRedisSessionAffinityBindingLifecycle(t *testing.T) {
	service, clock, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_v1_redis-lifecycle"
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-9", GroupID: "grp-1"}

	// Claim: first binder wins and the binding lands in Redis with the TTL
	// and the three index ZSETs.
	owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-a", scope)
	if !ok || owner != "account-a" {
		t.Fatalf("claim = (%q, %v)", owner, ok)
	}
	bindingRedisKey, err := redisSessionAffinityBindingKey("g14-tests", key)
	if err != nil {
		t.Fatalf("binding key: %v", err)
	}
	stored, err := mr.Get(bindingRedisKey)
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	wantValue := `{"accountId":"account-a","scope":{"systemAccountId":"sys-1","apiKeyId":"key-9","groupId":"grp-1"}}`
	if stored != wantValue {
		t.Fatalf("stored binding = %q, want %q", stored, wantValue)
	}
	ttl := mr.TTL(bindingRedisKey)
	if ttl <= time.Duration(sessionAffinityTtlMs-1000)*time.Millisecond || ttl > time.Duration(sessionAffinityTtlMs)*time.Millisecond {
		t.Fatalf("binding ttl = %v, want ~%dms", ttl, sessionAffinityTtlMs)
	}
	for _, indexKey := range []string{
		mustRedisSessionAffinityAccountIndexKey("g14-tests", "account-a"),
		mustRedisSessionAffinityAccountSystemIndexKey("g14-tests", "account-a", "sys-1"),
		mustRedisSessionAffinityAccountSystemAPIKeyIndexKey("g14-tests", "account-a", "sys-1", "key-9"),
	} {
		members, err := mr.ZMembers(indexKey)
		if err != nil || len(members) != 1 || members[0] != key {
			t.Fatalf("index %s members = %v, err %v", indexKey, members, err)
		}
	}

	// Re-claim by the owner refreshes TTL (PEXPIRE path).
	clock.Advance(30 * time.Minute)
	if _, _ = service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-a", scope); mr.TTL(bindingRedisKey) <= time.Duration(sessionAffinityTtlMs-5000)*time.Millisecond {
		t.Fatalf("ttl not refreshed: %v", mr.TTL(bindingRedisKey))
	}

	// Pre-emption attempt: the loser observes the incumbent.
	if owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-b", scope); !ok || owner != "account-a" {
		t.Fatalf("pre-emption claim = (%q, %v)", owner, ok)
	}
	if stored, _ := mr.Get(bindingRedisKey); stored != wantValue {
		t.Fatalf("binding changed after pre-emption attempt: %q", stored)
	}

	// Async forget with the wrong account is a no-op.
	if err := service.ForgetOpenAIAccountForSessionAsync(ctx, key, "account-b"); err != nil {
		t.Fatalf("forget error: %v", err)
	}
	if exists := mr.Exists(bindingRedisKey); !exists {
		t.Fatal("binding deleted by wrong-account forget")
	}
	// Async forget with the owner clears the binding and the indexes.
	if err := service.ForgetOpenAIAccountForSessionAsync(ctx, key, "account-a"); err != nil {
		t.Fatalf("forget error: %v", err)
	}
	if exists := mr.Exists(bindingRedisKey); exists {
		t.Fatal("binding survived owner forget")
	}
	for _, indexKey := range []string{
		mustRedisSessionAffinityAccountIndexKey("g14-tests", "account-a"),
		mustRedisSessionAffinityAccountSystemIndexKey("g14-tests", "account-a", "sys-1"),
	} {
		if members, _ := mr.ZMembers(indexKey); len(members) != 0 {
			t.Fatalf("index %s not cleaned: %v", indexKey, members)
		}
	}
}

func TestRedisSessionAffinityMigrationViaIndexes(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-9", GroupID: "grp-1"}
	for _, session := range []string{"m1", "m2", "m3"} {
		if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, "aff_v1_"+session, "source", scope); !ok {
			t.Fatalf("claim %s failed", session)
		}
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, "aff_v1_other", "bystander", scope); !ok {
		t.Fatal("bystander claim failed")
	}
	result, err := service.MigrateOpenAIAccountSessionAffinityAsync(ctx, "source", "target", scope, MigrationOptions{PreferMigratedSessions: true})
	if err != nil {
		t.Fatalf("migrate error: %v", err)
	}
	if result.MigratedSessionCount != 3 {
		t.Fatalf("migrated = %d, want 3", result.MigratedSessionCount)
	}
	for _, session := range []string{"m1", "m2", "m3"} {
		record, err := service.getRedisSessionAffinityRecord(ctx, "aff_v1_"+session, false)
		if err != nil || record == nil {
			t.Fatalf("record %s missing: %v", session, err)
		}
		if record.binding.AccountID != "target" || !record.binding.TrafficMigrationPreferred {
			t.Fatalf("record %s = %+v, want target with migration flag", session, record.binding)
		}
	}
}

func TestRedisTrafficMigrationPreferenceLifecycle(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	if err := service.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "source", "target", scope, TrafficMigrationPreferenceWriteOptions{}); err != nil {
		t.Fatalf("remember error: %v", err)
	}
	scopeKey, _ := trafficMigrationPreferenceScopeKey(scope)
	preferenceKey, err := redisTrafficMigrationPreferenceKey("g14-tests", scopeKey)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	stored, err := mr.Get(preferenceKey)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	wantValue := `{"sourceAccountId":"source","targetAccountId":"target"}`
	if stored != wantValue {
		t.Fatalf("stored preference = %q, want %q", stored, wantValue)
	}
	if ttl := mr.TTL(preferenceKey); ttl <= 0 {
		t.Fatalf("preference ttl = %v", ttl)
	}

	// Ordering consumes the preference when the source account is not
	// dispatchable any more.
	ordered, err := service.OrderOpenAIAccountsBySessionAffinityAsync(ctx, []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("target", 5, nil),
		testAccount("x", 5, nil),
	}, "", DispatchOrderingOptions{GroupType: GroupTypePersonal, TrafficMigrationScope: scope})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if ids := idsOf(ordered); !equalIDs(ids, []string{"target", "x"}) {
		t.Fatalf("ordered = %v, want target first", ids)
	}
}

func TestRedisCorruptedBindingTreatedAsMissing(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_v1_corrupted"
	bindingRedisKey, _ := redisSessionAffinityBindingKey("g14-tests", key)
	if err := mr.Set(bindingRedisKey, "{not json"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-a", nil)
	if !ok || owner != "account-a" {
		t.Fatalf("claim over corrupted value = (%q, %v)", owner, ok)
	}
	if stored, _ := mr.Get(bindingRedisKey); !strings.Contains(stored, "account-a") {
		t.Fatalf("binding not rewritten: %q", stored)
	}
}

func TestRedisMalformedScopeDropsOnlyScope(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_v1_malformed-scope"
	bindingRedisKey, _ := redisSessionAffinityBindingKey("g14-tests", key)
	// A non-object scope must drop the scope but keep the binding, exactly
	// like the Node per-field typeof checks.
	if err := mr.Set(bindingRedisKey, `{"accountId":"a","scope":[1,2]}`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	record, err := service.getRedisSessionAffinityRecord(ctx, key, false)
	if err != nil || record == nil {
		t.Fatalf("record = %+v, err %v", record, err)
	}
	if record.binding.AccountID != "a" || record.binding.Scope != nil {
		t.Fatalf("binding = %+v", record.binding)
	}
}

func TestRedisClaimRaceFaithfulOutcome(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_v1_redis-race"
	bindingRedisKey, _ := redisSessionAffinityBindingKey("g14-tests", key)
	const contenders = 24
	owners := make([]string, contenders)
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(contenders)
	for i := 0; i < contenders; i++ {
		go func(i int) {
			defer wg.Done()
			owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, fmt.Sprintf("account-%02d", i), nil)
			if !ok {
				owner = ""
			}
			mu.Lock()
			owners[i] = owner
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// Node HEAD contract: the claim re-reads the incumbent right before the
	// Lua CAS, so under a tight race a contender whose fresh read observed a
	// binding may legitimately overwrite it. The invariants that must hold
	// are: every reported owner is a contender (or empty on exhausted
	// attempts), Redis ends with exactly one binding owned by a contender,
	// and the store is stable afterwards.
	valid := map[string]bool{"": true}
	for i := 0; i < contenders; i++ {
		valid[fmt.Sprintf("account-%02d", i)] = true
	}
	for _, owner := range owners {
		if !valid[owner] {
			t.Fatalf("unexpected owner %q", owner)
		}
	}
	stored, err := mr.Get(bindingRedisKey)
	if err != nil || stored == "" {
		t.Fatalf("final binding missing: %q, %v", stored, err)
	}
	if !strings.Contains(stored, "account-") {
		t.Fatalf("final binding = %q", stored)
	}
	// After the storm the binding is stable: every late claimer observes the
	// same owner and no new binding appears.
	finalOwner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-late", nil)
	if !ok {
		t.Fatal("late claim not ok")
	}
	if !strings.Contains(stored, finalOwner) {
		t.Fatalf("late claim owner = %q, binding = %q", finalOwner, stored)
	}
}

func TestRedisDegradeToNoAffinityOnRedisFailure(t *testing.T) {
	service, _, logger, mr := newRedisAffinityService(t)
	ctx := context.Background()
	// Kill the server: every redis path must degrade instead of crashing.
	mr.Close()

	owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, "aff_v1_down", "account-a", nil)
	if ok || owner != "" {
		t.Fatalf("claim over down redis = (%q, %v)", owner, ok)
	}
	if !logger.HasEvent("redis_openai_session_affinity_remember_failed") {
		t.Fatalf("events = %v", logger.Events())
	}
	if err := service.ForgetOpenAIAccountForSessionAsync(ctx, "aff_v1_down", "account-a"); err != nil {
		t.Fatalf("forget over down redis must not error: %v", err)
	}
	if !logger.HasEvent("redis_openai_session_affinity_forget_failed") {
		t.Fatalf("forget events = %v", logger.Events())
	}
	// Ordering degrades to input order with a read-failure warn.
	ordered, err := service.OrderOpenAIAccountsBySessionAffinityAsync(ctx, []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 2, nil),
		testAccount("b", 1, nil),
	}, "aff_v1_down", DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil {
		t.Fatalf("order over down redis must not error: %v", err)
	}
	if ids := idsOf(ordered); !equalIDs(ids, []string{"a", "b"}) {
		t.Fatalf("ordered = %v, want unchanged", ids)
	}
	if !logger.HasEvent("redis_openai_session_affinity_read_failed") {
		t.Fatalf("order events = %v", logger.Events())
	}
}

func TestRedisMissingCacheURLConfigError(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	// Replace the driver with one that has no client and no URL.
	service.redis = nil
	_, err := service.redisSessionAffinityClient(context.Background())
	if err == nil || err.Error() != "JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置" {
		t.Fatalf("err = %v", err)
	}
}

func TestDualDriverConsistency(t *testing.T) {
	runScenario := func(service *AffinityService) ([]string, string) {
		ctx := context.Background()
		key := "aff_v1_dual"
		scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
		owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "b", scope)
		preempted, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", scope)
		accounts := []gatewayruntimecache.OpenAIAccountSecret{
			testAccount("a", 10, nil),
			testAccount("b", 10, nil),
		}
		ordered, err := service.OrderOpenAIAccountsBySessionAffinityAsync(ctx, accounts, key, DispatchOrderingOptions{GroupType: GroupTypePersonal})
		if err != nil {
			return []string{err.Error()}, preempted
		}
		return idsOf(ordered), owner + "/" + preempted
	}

	memoryService, _, _ := newTestAffinityService(t, nil)
	memoryOrder, memoryOwners := runScenario(memoryService)

	redisService, _, _, _ := newRedisAffinityService(t)
	redisOrder, redisOwners := runScenario(redisService)

	if !equalIDs(memoryOrder, redisOrder) {
		t.Fatalf("ordering mismatch: memory %v vs redis %v", memoryOrder, redisOrder)
	}
	if memoryOwners != redisOwners {
		t.Fatalf("claim owners mismatch: memory %q vs redis %q", memoryOwners, redisOwners)
	}
}

// quietRedisLogger silences the go-redis pool retry logs the degradation
// tests deliberately trigger.
type quietRedisLogger struct{}

func (quietRedisLogger) Printf(_ context.Context, _ string, _ ...any) {}
