package gatewaysession

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func latchScope() *OpenAIGatewaySessionAffinityScope {
	return &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
}

func latchFingerprint() string {
	return strings.Repeat("a", 64)
}

func TestAffinityLeaseClaimAndRebindMemory(t *testing.T) {
	service, clock, _ := newTestAffinityService(t, nil)
	ctx := context.Background()
	key := "aff_v1_latch-lease-memory"
	scope := latchScope()

	if owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", scope); !ok || owner != "a" {
		t.Fatalf("initial claim = %q %v", owner, ok)
	}
	if ok := service.RebindOpenAIAccountForSessionAsync(ctx, key, "a", "a", scope); !ok {
		t.Fatal("same-owner success must refresh lease")
	}
	service.mu.Lock()
	binding := service.sessionAffinityCacheGetLocked(key)
	service.mu.Unlock()
	if binding == nil || binding.LeaseUntilMs <= clock.Millis() {
		t.Fatalf("lease not written: %+v", binding)
	}
	if binding.LeaseUntilMs != clock.Millis()+sessionAffinityLeaseTtlMs {
		t.Fatalf("leaseUntil = %d, want %d", binding.LeaseUntilMs, clock.Millis()+sessionAffinityLeaseTtlMs)
	}
	// Active lease keeps first-binder-wins.
	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "b", scope); owner != "a" {
		t.Fatalf("active lease owner = %q, want a", owner)
	}
	// Expired lease allows the proposed account to rewrite.
	clock.Advance(31 * time.Minute)
	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "b", scope); owner != "b" {
		t.Fatalf("expired lease owner = %q, want b", owner)
	}
}

func TestAffinityRebindCASMemory(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	ctx := context.Background()
	key := "aff_v1_latch-rebind-memory"
	scope := latchScope()

	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", scope); owner != "a" {
		t.Fatalf("claim = %q", owner)
	}
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "a", latchFingerprint())
	// Wrong expected owner must not steal the binding.
	if ok := service.RebindOpenAIAccountForSessionAsync(ctx, key, "c", "b", scope); ok {
		t.Fatal("rebind with wrong expected owner must be a no-op")
	}
	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "b", scope); owner != "a" {
		t.Fatalf("binding changed by wrong expected owner: %q", owner)
	}
	// Correct expected owner rebinds and clears the old owner's Key fingerprint.
	if ok := service.RebindOpenAIAccountForSessionAsync(ctx, key, "a", "b", scope); !ok {
		t.Fatal("rebind a->b failed")
	}
	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", scope); owner != "b" {
		t.Fatalf("rebind owner = %q, want b", owner)
	}
	if fingerprint := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "b"); fingerprint != "" {
		t.Fatalf("old owner fingerprint leaked to new owner: %q", fingerprint)
	}
}

func TestAffinityRebindCASConcurrentSingleWinner(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	ctx := context.Background()
	key := "aff_v1_latch-rebind-concurrent"
	scope := latchScope()

	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", scope); owner != "a" {
		t.Fatalf("claim = %q", owner)
	}
	candidates := []string{"b", "c", "d", "e", "f", "g", "h", "i"}
	var wg sync.WaitGroup
	var applied atomic.Int32
	for _, candidate := range candidates {
		wg.Add(1)
		go func(newOwner string) {
			defer wg.Done()
			if service.RebindOpenAIAccountForSessionAsync(ctx, key, "a", newOwner, scope) {
				applied.Add(1)
			}
		}(candidate)
	}
	wg.Wait()
	if got := applied.Load(); got != 1 {
		t.Fatalf("concurrent expected-owner rebinds applied = %d, want 1", got)
	}
	// Whatever candidate won, the binding is stable and no later claim flips it.
	owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "z", scope)
	if !ok || owner == "a" || owner == "z" {
		t.Fatalf("post-race claim = %q %v", owner, ok)
	}
	if next, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "z", scope); next != owner {
		t.Fatalf("binding flipped after race: %q -> %q", owner, next)
	}
}

func TestAffinityRebindRedis(t *testing.T) {
	service, clock, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_v1_latch-rebind-redis"
	scope := latchScope()

	if owner, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", scope); !ok || owner != "a" {
		t.Fatalf("initial claim = %q %v", owner, ok)
	}
	if ok := service.RebindOpenAIAccountForSessionAsync(ctx, key, "a", "b", scope); !ok {
		t.Fatal("redis rebind a->b failed")
	}
	if owner, _ := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", scope); owner != "b" {
		t.Fatalf("redis rebind owner = %q, want b", owner)
	}
	record, err := service.getRedisSessionAffinityRecord(ctx, key, false)
	if err != nil || record == nil {
		t.Fatalf("read redis record = %+v err=%v", record, err)
	}
	if record.binding.LeaseUntilMs != clock.Millis()+sessionAffinityLeaseTtlMs {
		t.Fatalf("redis leaseUntil = %d, want %d", record.binding.LeaseUntilMs, clock.Millis()+sessionAffinityLeaseTtlMs)
	}
	if !strings.Contains(record.rawValue, `"leaseUntilMs"`) {
		t.Fatalf("redis raw value missing leaseUntilMs: %s", record.rawValue)
	}
	// Same-owner success refreshes the lease without changing the owner.
	clock.Advance(time.Minute)
	if ok := service.RebindOpenAIAccountForSessionAsync(ctx, key, "b", "b", scope); !ok {
		t.Fatal("redis same-owner lease refresh failed")
	}
	record, err = service.getRedisSessionAffinityRecord(ctx, key, false)
	if err != nil || record == nil || record.binding.AccountID != "b" {
		t.Fatalf("redis same-owner record = %+v err=%v", record, err)
	}
	if record.binding.LeaseUntilMs != clock.Millis()+sessionAffinityLeaseTtlMs {
		t.Fatalf("redis refreshed lease = %d, want %d", record.binding.LeaseUntilMs, clock.Millis()+sessionAffinityLeaseTtlMs)
	}
}

func TestAffinityOrderSkipsExpiredLease(t *testing.T) {
	service, clock, _ := newTestAffinityService(t, nil)
	ctx := context.Background()
	key := "aff_v1_latch-order-lease"
	scope := latchScope()
	accounts := []gatewayruntimecache.OpenAIAccountSecret{testAccount("a", 5, nil), testAccount("b", 5, nil)}

	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "b", scope); !ok {
		t.Fatal("claim failed")
	}
	// Legacy lease=0 is active: bound b promotes over equal-tier a.
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, key, DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil {
		t.Fatalf("order legacy: %v", err)
	}
	if len(ordered) != 2 || ordered[0].ID != "b" {
		t.Fatalf("legacy order = %v, want b first", idsOf(ordered))
	}
	// Write a lease, then expire it: the binding no longer promotes.
	if ok := service.RebindOpenAIAccountForSessionAsync(ctx, key, "b", "b", scope); !ok {
		t.Fatal("lease refresh failed")
	}
	clock.Advance(31 * time.Minute)
	ordered, err = service.OrderOpenAIAccountsBySessionAffinity(accounts, key, DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil {
		t.Fatalf("order expired: %v", err)
	}
	if len(ordered) != 2 || ordered[0].ID != "a" {
		t.Fatalf("expired order = %v, want a first", idsOf(ordered))
	}
}
