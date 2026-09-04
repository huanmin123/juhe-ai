package gatewayhybrid

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func TestApplyAffinityDecisionBranches(t *testing.T) {
	lowRoute := routestrategies.HybridLevelRoute{MinLevel: 1, MaxLevel: 5, TargetModel: "gpt-5-mini", Enabled: true}
	highRoute := routestrategies.HybridLevelRoute{MinLevel: 6, MaxLevel: 10, TargetModel: "gpt-5", Enabled: true}
	tests := []struct {
		name string
	 previous routestrategies.HybridLevelRoute
		level int
		route routestrategies.HybridLevelRoute
		lowCount int
		lastLevel int
		delta int
		downgradeCount int
		wantApplied bool
		wantReason string
		wantRoute routestrategies.HybridLevelRoute
		wantLowCount int
		wantLowCountSet bool
	}{
		{
			name: "same model does not stick",
			previous: lowRoute, level: 3, route: lowRoute, delta: 2, downgradeCount: 2,
			wantApplied: false, wantRoute: lowRoute, wantLowCount: 0, wantLowCountSet: true,
		},
		{
			name: "downgrade waits for confirmation",
			previous: highRoute, lastLevel: 9, level: 3, route: lowRoute, lowCount: 0, delta: 2, downgradeCount: 2,
			wantApplied: true, wantReason: "downgrade_requires_consecutive_low_scores",
			wantRoute: highRoute, wantLowCount: 1, wantLowCountSet: true,
		},
		{
			name: "downgrade confirmed after threshold",
			previous: highRoute, lastLevel: 9, level: 3, route: lowRoute, lowCount: 1, delta: 2, downgradeCount: 2,
			wantApplied: false, wantRoute: lowRoute, wantLowCount: 0, wantLowCountSet: true,
		},
		{
			name: "upgrade sticks below delta threshold",
			previous: lowRoute, lastLevel: 3, level: 4, route: highRoute, delta: 2, downgradeCount: 2,
			wantApplied: true, wantReason: "level_delta_below_threshold",
			wantRoute: lowRoute, wantLowCount: 0, wantLowCountSet: true,
		},
		{
			name: "upgrade at exact delta threshold switches",
			previous: lowRoute, lastLevel: 3, level: 5, route: highRoute, delta: 2, downgradeCount: 2,
			wantApplied: false, wantRoute: highRoute, wantLowCount: 0, wantLowCountSet: true,
		},
		{
			name: "upward move keeps previous lowCount",
			previous: lowRoute, lastLevel: 2, level: 3, route: highRoute, lowCount: 0, delta: 2, downgradeCount: 2,
			wantApplied: true, wantReason: "level_delta_below_threshold",
			wantRoute: lowRoute, wantLowCount: 0, wantLowCountSet: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			config := hybridConfig()
			config.SwitchMinLevelDelta = testCase.delta
			config.DowngradeConsecutiveLowCount = testCase.downgradeCount
			decision := ApplyAffinityDecision(ApplyAffinityDecisionInput{
				Previous: HybridRouteAffinityBinding{Route: testCase.previous, LastLevel: testCase.lastLevel, LowCount: testCase.lowCount},
				Level:    testCase.level,
				Route:    testCase.route,
				Config:   config,
			})
			if decision.Applied != testCase.wantApplied || decision.Reason != testCase.wantReason {
				t.Fatalf("decision = %+v, want applied=%v reason=%q", decision, testCase.wantApplied, testCase.wantReason)
			}
			if decision.Route.TargetModel != testCase.wantRoute.TargetModel {
				t.Fatalf("route = %s, want %s", decision.Route.TargetModel, testCase.wantRoute.TargetModel)
			}
			if testCase.wantLowCountSet {
				if decision.LowCount == nil || *decision.LowCount != testCase.wantLowCount {
					t.Fatalf("lowCount = %v, want %d", decision.LowCount, testCase.wantLowCount)
				}
			}
		})
	}
}

func TestAffinityMemoryDriverHitMissAndExpiry(t *testing.T) {
	now := time.Now()
	service := NewAffinityService(testClock(&now), &mockIdentity{}, nil)
	config := hybridConfig()
	view := &GatewayRequestView{ConversationKey: "conv-1"}
	first, err := service.ApplyAsync(context.Background(), AffinityInput{
		View: view, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 7, Route: config.LevelRoutes[1],
	})
	if err != nil || first.Applied {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	// A different scoring route with small delta sticks to the previous route.
	second, err := service.ApplyAsync(context.Background(), AffinityInput{
		View: view, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 7, Route: config.LevelRoutes[0],
	})
	if err != nil || !second.Applied || second.Route.TargetModel != "gpt-5" {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	// Different conversation key misses.
	other, err := service.ApplyAsync(context.Background(), AffinityInput{
		View: &GatewayRequestView{ConversationKey: "conv-2"}, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 3, Route: config.LevelRoutes[0],
	})
	if err != nil || other.Applied {
		t.Fatalf("other = %+v err=%v", other, err)
	}
	// Advance beyond the affinity TTL (900s) — binding expires.
	advanceClock(&now, 901*time.Second)
	expired, err := service.ApplyAsync(context.Background(), AffinityInput{
		View: view, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 7, Route: config.LevelRoutes[0],
	})
	if err != nil || expired.Applied {
		t.Fatalf("expired = %+v err=%v", expired, err)
	}
}

func TestAffinityDisabledOrNoIdentitySkips(t *testing.T) {
	now := time.Now()
	store := newMockStateStore()
	service := NewAffinityService(testClock(&now), &mockIdentity{}, store)
	config := hybridConfig()
	config.CacheAffinityEnabled = false
	decision, err := service.ApplyAsync(context.Background(), AffinityInput{
		View: &GatewayRequestView{ConversationKey: "conv"}, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 3, Route: config.LevelRoutes[0],
	})
	if err != nil || decision.Applied {
		t.Fatalf("disabled affinity = %+v err=%v", decision, err)
	}
	if len(store.values) != 0 {
		t.Fatalf("disabled affinity must not write state, got %d", len(store.values))
	}
	config = hybridConfig()
	config.AffinityTTLSeconds = 0
	decision, err = service.ApplyAsync(context.Background(), AffinityInput{
		View: &GatewayRequestView{ConversationKey: "conv"}, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 3, Route: config.LevelRoutes[0],
	})
	if err != nil || decision.Applied {
		t.Fatalf("zero ttl = %+v err=%v", decision, err)
	}
	// No conversation key → no key, no state write.
	config = hybridConfig()
	decision, err = service.ApplyAsync(context.Background(), AffinityInput{
		View: &GatewayRequestView{}, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 3, Route: config.LevelRoutes[0],
	})
	if err != nil || decision.Applied {
		t.Fatalf("no identity = %+v err=%v", decision, err)
	}
	if len(store.values) != 0 {
		t.Fatalf("no identity must not write state")
	}
}

func TestAffinityRedisDriverStoresBindingUnderSessionKey(t *testing.T) {
	now := time.Now()
	store := newMockStateStore()
	service := NewAffinityService(testClock(&now), &mockIdentity{}, store)
	config := hybridConfig()
	view := &GatewayRequestView{ConversationKey: "conv-1"}
	if _, err := service.ApplyAsync(context.Background(), AffinityInput{
		View: view, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 7, Route: config.LevelRoutes[1],
	}); err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	found := false
	for key := range store.values {
		if key == "session:aff:conv-1:sys:key:hybrid_smart:"+HybridRoutePoolScope(config) {
			found = true
			if store.ttls[key] != 900_000 {
				t.Fatalf("ttl = %d, want 900000", store.ttls[key])
			}
		}
	}
	if !found {
		t.Fatalf("state key missing, have %v", store.values)
	}
	// A second apply reads the stored binding; scoring the low route is a
	// downgrade, so it sticks to the previous (high) route pending
	// consecutive-low confirmation.
	decision, err := service.ApplyAsync(context.Background(), AffinityInput{
		View: view, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 7, Route: config.LevelRoutes[0],
	})
	if err != nil || !decision.Applied || decision.Reason != "downgrade_requires_consecutive_low_scores" {
		t.Fatalf("decision = %+v err=%v", decision, err)
	}
}

func TestAffinityMemoryEvictionAtMaxEntries(t *testing.T) {
	now := time.Now()
	service := NewAffinityService(testClock(&now), &mockIdentity{}, nil)
	config := hybridConfig()
	for index := 0; index < HybridRouteAffinityMaxEntries+5; index++ {
		view := &GatewayRequestView{ConversationKey: "conv-" + strconv.Itoa(index)}
		service.Apply(AffinityInput{
			View: view, SystemAccountID: "sys", APIKeyID: "key", Config: config, Level: 3, Route: config.LevelRoutes[0],
		})
	}
	service.memory.mu.Lock()
	size := len(service.memory.entries)
	service.memory.mu.Unlock()
	if size != HybridRouteAffinityMaxEntries {
		t.Fatalf("entries = %d, want %d", size, HybridRouteAffinityMaxEntries)
	}
}
