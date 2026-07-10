package ratelimit

import (
	"context"
	"testing"
	"time"

	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

type fakePenaltyWindowClient struct {
	limits   []redisplatform.PenaltyWindowLimit
	decision redisplatform.PenaltyWindowDecision
	err      error
	calls    int
}

func (f *fakePenaltyWindowClient) AllowPenaltyWindow(_ context.Context, limits []redisplatform.PenaltyWindowLimit) (redisplatform.PenaltyWindowDecision, error) {
	f.calls++
	f.limits = append([]redisplatform.PenaltyWindowLimit(nil), limits...)
	return f.decision, f.err
}

func TestNewLimiterRequiresClient(t *testing.T) {
	if _, err := NewLimiter(Options{}); err == nil {
		t.Fatal("NewLimiter() error = nil, want client error")
	}
}

func TestLimiterAllowsWhenNoActiveRules(t *testing.T) {
	client := &fakePenaltyWindowClient{}
	limiter, err := NewLimiter(Options{Client: client})
	if err != nil {
		t.Fatalf("NewLimiter() error = %v", err)
	}

	decision, err := limiter.Allow(context.Background(), publicapiauth.AuthContext{
		SourceRefID: "source",
		TokenID:     "token",
		TokenPrefix: "juis_abc",
		RateLimits: []port.PublicAPIRateLimitRule{
			{WindowSeconds: 0, MaxRequests: 10},
			{WindowSeconds: 60, MaxRequests: 0},
		},
	})
	if err != nil {
		t.Fatalf("Allow() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("decision = %+v, want allowed", decision)
	}
	if client.calls != 0 {
		t.Fatalf("client calls = %d, want 0", client.calls)
	}
}

func TestLimiterMapsAuthContextToRedisPenaltyWindow(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	client := &fakePenaltyWindowClient{
		decision: redisplatform.PenaltyWindowDecision{
			Allowed:            false,
			RetryAfterSeconds:  7,
			BlockedWindowIndex: 2,
		},
	}
	limiter, err := NewLimiter(Options{
		Client:     client,
		Now:        func() time.Time { return now },
		MaxPenalty: 10 * time.Minute,
		MaxIdle:    time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLimiter() error = %v", err)
	}

	decision, err := limiter.Allow(context.Background(), publicapiauth.AuthContext{
		SourceRefID: "source_1",
		TokenID:     "token_1",
		TokenPrefix: "juis_abcd",
		RateLimits: []port.PublicAPIRateLimitRule{
			{WindowSeconds: 60, MaxRequests: 10},
			{WindowSeconds: 300, MaxRequests: 30},
		},
	})
	if err != nil {
		t.Fatalf("Allow() error = %v", err)
	}

	if decision.Allowed {
		t.Fatalf("decision = %+v, want blocked", decision)
	}
	if decision.RetryAfterSeconds != 7 {
		t.Fatalf("retry after = %d, want 7", decision.RetryAfterSeconds)
	}
	if decision.Rule.WindowSeconds != 300 || decision.Rule.MaxRequests != 30 {
		t.Fatalf("blocked rule = %+v, want second rule", decision.Rule)
	}
	if got, want := len(client.limits), 2; got != want {
		t.Fatalf("limits length = %d, want %d", got, want)
	}
	first := client.limits[0]
	if first.StoreName != DefaultStoreName {
		t.Fatalf("store name = %q, want %q", first.StoreName, DefaultStoreName)
	}
	if first.ScopeKey != "source_1:token_1:juis_abcd" {
		t.Fatalf("scope key = %q", first.ScopeKey)
	}
	if first.Window != time.Minute || first.Limit != 10 {
		t.Fatalf("first limit = %+v", first)
	}
	if !first.Now.Equal(now) || first.MaxPenalty != 10*time.Minute || first.MaxIdle != time.Hour {
		t.Fatalf("first timing = %+v", first)
	}
}
