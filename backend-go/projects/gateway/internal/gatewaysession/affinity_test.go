package gatewaysession

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestClaimOpenAIAccountForSessionLocalSemantics(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	key := "aff_v1_session-a"
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-9", GroupID: "grp-1"}

	t.Run("first binder wins", func(t *testing.T) {
		owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-a", scope)
		if !ok || owner != "account-a" {
			t.Fatalf("claim = (%q, %v), want (account-a, true)", owner, ok)
		}
	})

	t.Run("second binder loses and observes the first owner", func(t *testing.T) {
		owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-b", scope)
		if !ok || owner != "account-a" {
			t.Fatalf("claim = (%q, %v), want (account-a, true)", owner, ok)
		}
	})

	t.Run("re-claim by the owner is a no-op", func(t *testing.T) {
		owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-a", scope)
		if !ok || owner != "account-a" {
			t.Fatalf("claim = (%q, %v)", owner, ok)
		}
	})

	t.Run("empty key claims nothing", func(t *testing.T) {
		if owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "", "account-a", scope); ok || owner != "" {
			t.Fatalf("empty key claim = (%q, %v)", owner, ok)
		}
	})

	t.Run("forget only with the bound account id", func(t *testing.T) {
		service.ForgetOpenAIAccountForSession(key, "account-b")
		if owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-b", scope); owner != "account-a" || !ok {
			t.Fatalf("forget with wrong id changed the binding: (%q, %v)", owner, ok)
		}
		service.ForgetOpenAIAccountForSession(key, "account-a")
		if owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-b", scope); owner != "account-b" || !ok {
			t.Fatalf("forget with owner id did not clear: (%q, %v)", owner, ok)
		}
	})

	t.Run("forget without account id clears", func(t *testing.T) {
		service.ForgetOpenAIAccountForSession(key, "")
		service.mu.Lock()
		binding := service.sessionAffinityCacheGetLocked(key)
		service.mu.Unlock()
		if binding != nil {
			t.Fatalf("binding survived: %+v", binding)
		}
	})
}

func TestSessionAffinityTTLExpiry(t *testing.T) {
	service, clock, _ := newTestAffinityService(t, nil)
	key := "aff_v1_ttl"
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-a", scope); !ok {
		t.Fatal("claim failed")
	}
	// Advance 59 minutes: still bound.
	clock.Advance(59 * time.Minute)
	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-b", scope); owner != "account-a" {
		t.Fatalf("binding expired early: owner = %q", owner)
	}
	// Advance past the 60 minute TTL: the binding is gone and b wins.
	clock.Advance(2 * time.Minute)
	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-b", scope); owner != "account-b" {
		t.Fatalf("binding survived TTL: owner = %q", owner)
	}
}

func TestSessionAffinityIndexMigrationLocal(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-9", GroupID: "grp-1"}
	for _, session := range []string{"s1", "s2"} {
		if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "aff_v1_"+session, "source", scope); !ok {
			t.Fatalf("claim for %s failed", session)
		}
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "aff_v1_other", "bystander", scope); !ok {
		t.Fatal("claim for bystander failed")
	}

	result := service.MigrateOpenAIAccountSessionAffinity("source", "target", scope, MigrationOptions{PreferMigratedSessions: true})
	if result.MigratedSessionCount != 2 {
		t.Fatalf("migrated = %d, want 2", result.MigratedSessionCount)
	}
	for _, session := range []string{"s1", "s2"} {
		owner, _ := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "aff_v1_"+session, "source", scope)
		if owner != "target" {
			t.Fatalf("session %s owner = %q, want target", session, owner)
		}
	}
	owner, _ := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "aff_v1_other", "source", scope)
	if owner != "bystander" {
		t.Fatalf("bystander owner = %q, want bystander", owner)
	}

	// Scope filter: only sys-2 sessions migrate.
	narrow := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-2"}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "aff_v1_s3", "source", &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-2", GroupID: "grp-9"}); !ok {
		t.Fatal("claim for sys-2 failed")
	}
	result = service.MigrateOpenAIAccountSessionAffinity("source", "target", narrow, MigrationOptions{})
	if result.MigratedSessionCount != 1 {
		t.Fatalf("scoped migrated = %d, want 1", result.MigratedSessionCount)
	}
}

func TestTrafficMigrationPreferenceLocal(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	// Same source/target ignored.
	service.RememberOpenAIAccountTrafficMigrationPreference("a", "a", scope)
	if preference := service.trafficMigrationPreferenceForAccounts([]string{"a", "b"}, scope); preference != nil {
		t.Fatalf("preference for identical ids = %+v", preference)
	}

	service.RememberOpenAIAccountTrafficMigrationPreference("source", "target", scope)

	// Source still present: preference dropped.
	if preference := service.trafficMigrationPreferenceForAccounts([]string{"source", "target"}, scope); preference != nil {
		t.Fatalf("preference kept while source is dispatchable: %+v", preference)
	}
	// Preference cache entry was deleted by the read.
	if preference := service.trafficMigrationPreferenceForAccounts([]string{"target", "other"}, scope); preference != nil {
		t.Fatalf("preference survived the source-present read: %+v", preference)
	}

	// Recreate; source absent, target present: preference applies.
	service.RememberOpenAIAccountTrafficMigrationPreference("source", "target", scope)
	preference := service.trafficMigrationPreferenceForAccounts([]string{"target", "other"}, scope)
	if preference == nil || preference.TargetAccountID != "target" {
		t.Fatalf("preference = %+v, want target", preference)
	}

	// Neither source nor target present: no preference.
	service.RememberOpenAIAccountTrafficMigrationPreference("source", "target2", scope)
	if preference := service.trafficMigrationPreferenceForAccounts([]string{"other", "another"}, scope); preference != nil {
		t.Fatalf("preference without target present = %+v", preference)
	}
}

func TestClaimRaceSingleWinnerMemory(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	key := "aff_v1_race"
	const contenders = 50
	var wg sync.WaitGroup
	owners := make([]string, contenders)
	var mu sync.Mutex
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, fmtAccountID(i), nil)
			if !ok {
				owner = ""
			}
			mu.Lock()
			owners[i] = owner
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	winner := owners[0]
	for _, owner := range owners {
		if owner != winner {
			t.Fatalf("inconsistent owners: %q vs %q", owner, winner)
		}
	}
	service.mu.Lock()
	bindingCount := len(service.bindingByKey)
	service.mu.Unlock()
	if bindingCount != 1 {
		t.Fatalf("binding count = %d, want 1", bindingCount)
	}
}

func digitChar(code int) string {
	return string(rune(code))
}

func fmtAccountID(i int) string {
	return "account-" + digitChar(65+i%26) + string(rune('0'+i/26%10)) + string(rune('0'+i/260%10))
}

func TestSyncForgetUnderRedisDriverWarns(t *testing.T) {
	service, _, logger := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
	})
	key := "aff_v1_sync-forget"
	// canUseProcessLocalSessionAffinity is false under the redis driver, so
	// nothing is stored and the sync entry point warns.
	service.ForgetOpenAIAccountForSession(key, "account-a")
	if !logger.HasEvent("redis_openai_session_affinity_sync_forget_ignored") {
		t.Fatalf("events = %v", logger.Events())
	}
	if owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "account-a", nil); ok || owner != "" {
		t.Fatalf("local claim under redis driver = (%q, %v)", owner, ok)
	}
}

func TestCanUseProcessLocalClearsIndexesUnderRedisDriver(t *testing.T) {
	// Seed local indexes with a memory service first.
	memoryService, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	_, _ = memoryService.ClaimOpenAIAccountForSessionAsync(context.Background(), "aff_v1_seed", "account-a", scope)

	// A redis-driver service must keep no process-local facts.
	redisService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
	})
	redisService.mu.Lock()
	redisService.keysByAccountID["account-a"] = map[string]struct{}{"aff_v1_seed": {}}
	redisService.bindingByKey["aff_v1_seed"] = &SessionBinding{AccountID: "account-a"}
	redisService.mu.Unlock()
	if redisService.canUseProcessLocalSessionAffinity() {
		t.Fatal("canUseProcessLocalSessionAffinity = true under redis driver")
	}
	redisService.mu.Lock()
	left := len(redisService.bindingByKey) + len(redisService.keysByAccountID)
	redisService.mu.Unlock()
	if left != 0 {
		t.Fatalf("indexes not cleared: %d entries", left)
	}
	_ = scope
}
