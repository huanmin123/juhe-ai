package gatewayproxyhealth

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestAuthenticatedModelsRateLimitFixedWindowBoundary(t *testing.T) {
	clock := newFakeClock(1_000_000_000)
	service := NewAuthenticatedModelsRateLimitService(clock.Now, NewPenaltyWindowRateLimiter(clock.Now, false, nil, "juhe-ai"), nil)

	// api_key scope: 100 requests / 10s. Distinct IPs keep the api_key_ip
	// scope out of the way.
	for i := 0; i < 100; i++ {
		ip := ipForIndex(i)
		decision, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", ip), nil)
		if err != nil || !decision.Allowed {
			t.Fatalf("request %d: %+v err=%v", i, decision, err)
		}
	}
	blocked, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", ipForIndex(999)), nil)
	if err != nil || blocked.Allowed || blocked.Scope != "api_key" {
		t.Fatalf("blocked = %+v err=%v", blocked, err)
	}
	if blocked.Limit == nil || *blocked.Limit != 100 {
		t.Fatalf("limit = %v", blocked.Limit)
	}
	if blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds != 10 {
		t.Fatalf("fixed-window retryAfter = %v", blocked.RetryAfterSeconds)
	}

	// Different API keys stay isolated.
	allowed, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-b", ipForIndex(998)), nil)
	if err != nil || !allowed.Allowed {
		t.Fatalf("key-b must stay allowed: %+v err=%v", allowed, err)
	}

	// The 10s window rolls: allowed again, then the 60s/300 rule takes over.
	clock.Set(1_000_000_000 + 10_000)
	for i := 0; i < 200; i++ {
		if _, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", ipForIndex(i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	blocked60, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", ipForIndex(997)), nil)
	if err != nil || blocked60.Allowed {
		t.Fatalf("60s rule must block: %+v err=%v", blocked60, err)
	}
	// The 60s window started 10s ago, so the fixed-window retry-after is 10.
	if blocked60.RetryAfterSeconds == nil || *blocked60.RetryAfterSeconds != 10 {
		t.Fatalf("60s retryAfter = %v", blocked60.RetryAfterSeconds)
	}
}

func TestAuthenticatedModelsRateLimitAPIKeyIPScope(t *testing.T) {
	clock := newFakeClock(1_000_000_000)
	service := NewAuthenticatedModelsRateLimitService(clock.Now, NewPenaltyWindowRateLimiter(clock.Now, false, nil, "juhe-ai"), nil)

	// api_key_ip scope: 20 requests / 10s.
	for i := 0; i < 20; i++ {
		decision, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", "198.51.100.7"), nil)
		if err != nil || !decision.Allowed {
			t.Fatalf("request %d: %+v err=%v", i, decision, err)
		}
	}
	blocked, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", "198.51.100.7"), nil)
	if err != nil || blocked.Allowed || blocked.Scope != "api_key_ip" {
		t.Fatalf("ip-scope block = %+v err=%v", blocked, err)
	}
	if blocked.Limit == nil || *blocked.Limit != 20 {
		t.Fatalf("ip limit = %v", blocked.Limit)
	}
	// A different IP for the same key keeps its own bucket.
	otherIP, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", "198.51.100.8"), nil)
	if err != nil || !otherIP.Allowed {
		t.Fatalf("other IP must stay allowed: %+v err=%v", otherIP, err)
	}
	// Missing IP normalizes to 'unknown'.
	for i := 0; i < 20; i++ {
		if _, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-c", "   "), nil); err != nil {
			t.Fatal(err)
		}
	}
	unknown, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-c", ""), nil)
	if err != nil || unknown.Allowed || unknown.Scope != "api_key_ip" {
		t.Fatalf("unknown-ip block = %+v err=%v", unknown, err)
	}
}

func TestAuthenticatedModelsRateLimitFailClosedOnRedisError(t *testing.T) {
	clock := newFakeClock(1_000_000_000)
	log := &recordingLog{}
	// redisDriver=true with no client mirrors the missing stateUrl failure.
	service := NewAuthenticatedModelsRateLimitService(clock.Now, NewPenaltyWindowRateLimiter(clock.Now, true, nil, "juhe-ai"), log.record)
	decision, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("key-a", "203.0.113.1"), nil)
	if err != nil {
		t.Fatalf("fail-closed must not surface the error: %v", err)
	}
	if decision.Allowed || !decision.Unavailable || decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds != 5 {
		t.Fatalf("fail-closed decision = %+v", decision)
	}
	if log.count() != 1 || log.events()[0] != "authenticated_models_rate_limit_unavailable" {
		t.Fatalf("log = %v", log.events())
	}
}

func TestAuthenticatedModelsRateLimitEmptyKeyAllowed(t *testing.T) {
	clock := newFakeClock(1_000_000_000)
	service := NewAuthenticatedModelsRateLimitService(clock.Now, NewPenaltyWindowRateLimiter(clock.Now, false, nil, "juhe-ai"), nil)
	decision, err := service.ConsumeAuthenticatedModelsRateLimit(contextBackground(), gatewayModelsInput("   ", "203.0.113.1"), nil)
	if err != nil || !decision.Allowed {
		t.Fatalf("empty key must be allowed: %+v err=%v", decision, err)
	}
}

func ipForIndex(i int) string {
	return "203.0.113." + itoaForTest(int64(i%200+1))
}

func gatewayModelsInput(apiKeyID, clientIP string) gatewaypreauth.AuthenticatedModelsRateLimitInput {
	return gatewaypreauth.AuthenticatedModelsRateLimitInput{APIKeyID: apiKeyID, ClientIP: clientIP}
}

func TestPublicModelsRateLimit(t *testing.T) {
	clock := newFakeClock(5_000_000_000)
	service := NewPublicModelsRateLimitService(clock.Now, NewPenaltyWindowRateLimiter(clock.Now, false, nil, "juhe-ai"))

	decision, err := service.ConsumePublicModelsRateLimit(contextBackground(), "10.0.0.1")
	if err != nil || !decision.Allowed || decision.Limit != 60 || decision.Remaining != 60 {
		t.Fatalf("allowed decision = %+v err=%v", decision, err)
	}
	// Node quirk: resetAt on the allowed path is now + windowSeconds.
	if decision.ResetAtMs != 5_000_000_000+60_000 {
		t.Fatalf("resetAtMs = %d", decision.ResetAtMs)
	}

	// Missing IP falls into the ip:unknown bucket.
	if _, err := service.ConsumePublicModelsRateLimit(contextBackground(), "  "); err != nil {
		t.Fatal(err)
	}

	// Requests 2..60 pass, the 61st hits the exact boundary: the exponential
	// mode opens a penalty equal to the window (60s).
	for i := 0; i < 60; i++ {
		if _, err := service.ConsumePublicModelsRateLimit(contextBackground(), "10.0.0.2"); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := service.ConsumePublicModelsRateLimit(contextBackground(), "10.0.0.2")
	if err != nil || blocked.Allowed || blocked.Remaining != 0 {
		t.Fatalf("blocked decision = %+v err=%v", blocked, err)
	}
	if blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds != 60 {
		t.Fatalf("blocked retryAfter = %v", blocked.RetryAfterSeconds)
	}
	if blocked.ResetAtMs != 5_000_000_000+60_000 {
		t.Fatalf("blocked resetAtMs = %d", blocked.ResetAtMs)
	}
}

func TestPublicModelsExponentialPenaltyEscalation(t *testing.T) {
	clock := newFakeClock(5_000_000_000)
	service := NewPublicModelsRateLimitService(clock.Now, NewPenaltyWindowRateLimiter(clock.Now, false, nil, "juhe-ai"))
	rule := PenaltyWindowRateLimitRule{WindowSeconds: 10, MaxRequests: 1}

	// First over-limit: penalty = window (10s).
	decision := service.store.consumeMemory("p:1", []PenaltyWindowRateLimitRule{rule}, nil)
	if !decision.Allowed {
		t.Fatal("first request must pass")
	}
	blocked := service.store.consumeMemory("p:1", []PenaltyWindowRateLimitRule{rule}, nil)
	if blocked.Allowed || blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds != 10 {
		t.Fatalf("first block retryAfter = %+v", blocked)
	}
	// Retry inside the penalty: the penalty doubles to 20s and the retry-after
	// covers the fresh full penalty from now (Node: blockedUntil - nowMs).
	clock.Advance(5_000)
	blocked = service.store.consumeMemory("p:1", []PenaltyWindowRateLimitRule{rule}, nil)
	if blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds != 20 {
		t.Fatalf("doubled penalty retryAfter = %+v", blocked)
	}
	// Keep retrying inside the penalty so it doubles up to the 15min cap.
	for i := 0; i < 20; i++ {
		clock.Advance(1_000)
		blocked = service.store.consumeMemory("p:1", []PenaltyWindowRateLimitRule{rule}, nil)
		if blocked.Allowed {
			t.Fatalf("retry %d must stay blocked", i)
		}
	}
	if blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds != 900 {
		t.Fatalf("penalty must cap at 15min: %+v", blocked)
	}
}

func TestPenaltyWindowMemoryConsumeForbiddenInRedisDriver(t *testing.T) {
	clock := newFakeClock(1_000)
	store := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{Name: "s", RedisDriver: true})
	if _, err := store.ConsumeMemory("k", []PenaltyWindowRateLimitRule{{WindowSeconds: 60, MaxRequests: 5}}, nil); err == nil {
		t.Fatal("redis driver must refuse the memory consume entry")
	}
}
