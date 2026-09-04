package gatewayhybrid

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func TestRedisRuntimeStateStoreRoundTrip(t *testing.T) {
	server, client := newTestRedis(t)
	store, err := NewRedisRuntimeStateStore(client, "juhe-ai:test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := context.Background()
	binding := HybridRouteAffinityBinding{
		Route: routestrategies.HybridLevelRoute{MinLevel: 6, MaxLevel: 10, TargetModel: "gpt-5", Enabled: true},
		LastLevel: 7,
		LowCount:  1,
	}
	if err := store.SetJSON(ctx, "session:abc", binding, 60_000); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Key layout mirrors redisNamespacedKey('juhe-ai:state:...').
	wantKey := "juhe-ai:test:state:gateway-hybrid-route-affinity:session:abc"
	if _, err := server.Get(wantKey); err != nil {
		t.Fatalf("redis key missing: %v", err)
	}
	if ttl := server.TTL(wantKey); ttl != time.Minute {
		t.Fatalf("ttl = %v", ttl)
	}
	var decoded HybridRouteAffinityBinding
	found, err := store.GetJSON(ctx, "session:abc", &decoded)
	if err != nil || !found {
		t.Fatalf("found = %v err = %v", found, err)
	}
	if decoded.LastLevel != 7 || decoded.LowCount != 1 || decoded.Route.TargetModel != "gpt-5" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if missing, err := store.GetJSON(ctx, "session:missing", &decoded); err != nil || missing {
		t.Fatalf("missing = %v err = %v", missing, err)
	}
	// Corrupt payloads read as absent and are deleted.
	_ = server.Set(wantKey, "{broken")
	if corrupted, err := store.GetJSON(ctx, "session:abc", &decoded); err != nil || corrupted {
		t.Fatalf("corrupted = %v err = %v", corrupted, err)
	}
	if _, err := server.Get(wantKey); err == nil {
		t.Fatal("corrupted payload must be deleted")
	}
	// Short namespaces get the juhe-ai prefix once.
	shortStore, err := NewRedisRuntimeStateStore(client, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := shortStore.SetJSON(ctx, "k", binding, 1000); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := server.Get("juhe-ai:test:state:gateway-hybrid-route-affinity:k"); err != nil {
		t.Fatalf("short namespace key missing: %v", err)
	}
}

func TestRedisSharedJSONCacheRoundTripAndClear(t *testing.T) {
	server, client := newTestRedis(t)
	cache, err := NewRedisSharedJSONCache(client, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := context.Background()
	entry := HybridScoringCacheEntry{Level: 8, Confidence: floatPtr(0.4), Factors: []string{"多步推理"}, Reason: strPtr("reason")}
	key := "abc123"
	if err := cache.Set(ctx, key, entry, 30_000); err != nil {
		t.Fatalf("set: %v", err)
	}
	wantKey := "juhe-ai:dev:cache:gateway:hybrid-scoring-result:abc123"
	if _, err := server.Get(wantKey); err != nil {
		t.Fatalf("redis key missing: %v", err)
	}
	decoded, err := cache.Get(ctx, key)
	if err != nil || decoded == nil {
		t.Fatalf("get = %v err = %v", decoded, err)
	}
	if decoded.Level != 8 || decoded.Confidence == nil || *decoded.Confidence != 0.4 || len(decoded.Factors) != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if missing, err := cache.Get(ctx, "nope"); err != nil || missing != nil {
		t.Fatalf("missing = %v err = %v", missing, err)
	}
	if err := cache.Clear(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared, err := cache.Get(ctx, key); err != nil || cleared != nil {
		t.Fatalf("after clear = %v err = %v", cleared, err)
	}
}

func TestRedisAdaptersRejectMissingWiring(t *testing.T) {
	if _, err := NewRedisRuntimeStateStore(nil, "dev"); err == nil {
		t.Fatal("nil client must be rejected")
	}
	if _, err := NewRedisSharedJSONCache(nil, "dev"); err == nil {
		t.Fatal("nil client must be rejected")
	}
}

func TestAuxiliaryHelpers(t *testing.T) {
	if got := ComposeAuxiliaryEndpoint("/v1/chat/completions", AuxiliaryTrafficSourceHybridQualityScoring); got != "/v1/chat/completions#hybrid-quality-scoring" {
		t.Fatalf("endpoint = %s", got)
	}
	if got := ComposeAuxiliaryEndpoint("/v1/chat/completions", AuxiliaryTrafficSourceHybridScoring); got != "/v1/chat/completions#hybrid-scoring" {
		t.Fatalf("endpoint = %s", got)
	}

	body, usage := ParseHybridAuxiliaryResponse(`{"usage":{"prompt_tokens":9},"x":1}`, "application/json")
	if body.Status != "valid" || usage.InputTokens == nil || gatewayproto.Token(usage.InputTokens) != 9 {
		t.Fatalf("parsed = %+v usage = %+v", body.Status, usage)
	}
	fragmentBody, fragmentUsage := ParseHybridAuxiliaryResponse(`prefix "usage":{"prompt_tokens":5} suffix`, "text/plain")
	if fragmentBody.Status != "not_json" {
		t.Fatalf("status = %s", fragmentBody.Status)
	}
	if fragmentUsage.InputTokens == nil || gatewayproto.Token(fragmentUsage.InputTokens) != 5 {
		t.Fatalf("fragment usage = %+v", fragmentUsage)
	}
	if gatewayproto.HasAnyUsageValue(EmptyHybridAuxiliaryUsage()) {
		t.Fatal("empty usage must not carry evidence")
	}

	t.Run("upstream failure normalization", func(t *testing.T) {
		payload, _ := AuxiliaryUpstreamFailure(AuxiliaryUpstreamFailureInput{
			BodyText:          `{"error":{"code":"insufficient_quota","message":"配额不足"}}`,
			ContentType:       "application/json",
			StatusCode:        429,
			FallbackErrorCode: "hybrid_scoring_failed",
		})
		if payload != "insufficient_quota" {
			t.Fatalf("code = %s", payload)
		}
		code, message := AuxiliaryUpstreamFailure(AuxiliaryUpstreamFailureInput{
			BodyText:          `{"error":{"code":"insufficient_quota","message":"配额不足"}}`,
			ContentType:       "application/json",
			StatusCode:        429,
			FallbackErrorCode: "hybrid_scoring_failed",
		})
		if code != "insufficient_quota" || message != "配额不足" {
			t.Fatalf("code = %s message = %s", code, message)
		}
		_, plain := AuxiliaryUpstreamFailure(AuxiliaryUpstreamFailureInput{
			BodyText:          "  upstream blew up  ",
			ContentType:       "text/html",
			StatusCode:        502,
			FallbackErrorCode: "hybrid_scoring_failed",
		})
		if plain != "upstream blew up" {
			t.Fatalf("message = %s", plain)
		}
		_, httpLine := AuxiliaryUpstreamFailure(AuxiliaryUpstreamFailureInput{
			BodyText:          "",
			ContentType:       "",
			StatusCode:        503,
			FallbackErrorCode: "hybrid_scoring_failed",
		})
		if httpLine != "上游返回 HTTP 503" {
			t.Fatalf("http line = %s", httpLine)
		}
	})
}

