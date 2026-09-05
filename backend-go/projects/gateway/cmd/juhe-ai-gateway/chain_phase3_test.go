package main

// G20 phase-3 composition tests: hybrid Redis collaborator interop
// (miniredis), the pricing estimate vectors and the openai-compatible route
// probe.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// ---------------------------------------------------------------------------
// item 3: hybrid Redis collaborators + G14 identity wiring
// ---------------------------------------------------------------------------

// TestChainHybridRedisCollaboratorsInterop: the chain-assembled hybrid Redis
// stores round-trip through miniredis with the Node-compatible key layout
// (state:gateway-hybrid-route-affinity / cache:gateway:hybrid-scoring-result).
func TestChainHybridRedisCollaboratorsInterop(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	state, err := gatewayhybrid.NewRedisRuntimeStateStore(client, "juhe-ai:dev")
	if err != nil {
		t.Fatalf("create hybrid runtime state: %v", err)
	}
	ctx := context.Background()
	affinity := map[string]any{"accountId": "acc_1", "groupId": "group_main"}
	if err := state.SetJSON(ctx, "key-1", affinity, 60_000); err != nil {
		t.Fatalf("set affinity: %v", err)
	}
	var loaded map[string]any
	ok, err := state.GetJSON(ctx, "key-1", &loaded)
	if err != nil || !ok {
		t.Fatalf("get affinity: ok=%v err=%v", ok, err)
	}
	if loaded["accountId"] != "acc_1" {
		t.Fatalf("affinity = %v", loaded)
	}
	// Node-compatible key layout: juhe-ai:<ns>:state:gateway-hybrid-route-affinity:<key>.
	if _, err := server.Get("juhe-ai:dev:state:gateway-hybrid-route-affinity:key-1"); err != nil {
		t.Fatalf("state key missing in redis: %v", err)
	}

	scoring, err := gatewayhybrid.NewRedisSharedJSONCache(client, "dev")
	if err != nil {
		t.Fatalf("create hybrid scoring cache: %v", err)
	}
	entry := gatewayhybrid.HybridScoringCacheEntry{Level: 2, Reason: strPtrCompat("healthy")}
	if err := scoring.Set(ctx, "cache-key", entry, 60_000); err != nil {
		t.Fatalf("set scoring entry: %v", err)
	}
	loadedEntry, err := scoring.Get(ctx, "cache-key")
	if err != nil || loadedEntry == nil {
		t.Fatalf("get scoring entry: %v %v", loadedEntry, err)
	}
	if loadedEntry.Level != 2 || loadedEntry.Reason == nil || *loadedEntry.Reason != "healthy" {
		t.Fatalf("scoring entry = %+v", loadedEntry)
	}
	if _, err := server.Get("juhe-ai:dev:cache:gateway:hybrid-scoring-result:cache-key"); err != nil {
		t.Fatalf("scoring key missing in redis: %v", err)
	}
	if err := scoring.Clear(ctx); err != nil {
		t.Fatalf("clear scoring cache: %v", err)
	}
	if cleared, _ := scoring.Get(ctx, "cache-key"); cleared != nil {
		t.Fatalf("scoring entry survived clear: %+v", cleared)
	}
}

// TestComposeChainRuntimeServicesWiresRedisCollaborators: with redis drivers
// the runtime services assemble non-nil hybrid collaborators and the G14
// identity services (no silent nil).
func TestComposeChainRuntimeServicesWiresRedisCollaborators(t *testing.T) {
	server := miniredis.RunT(t)
	cfg := composeTestConfig(t)
	cfg.RedisNamespace = "compose-test"
	cfg.CacheDriver = "redis"
	cfg.RuntimeStateDriver = "redis"
	cfg.RedisCacheURL = "redis://" + server.Addr()
	cfg.RedisStateURL = "redis://" + server.Addr()
	composed := &composition{db: nil, pgDialect: false}
	// composeChainRuntimeServices requires the composed handles: seed the
	// minimal business + stats schema for the runtime cache read models.
	// The cheap path: reuse the chain fixture database (same schema).
	fixture := newChainFixture(t)
	composed.db = fixture.db
	composed.statsDB = fixture.statsDB
	composed.Bus = nil
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("compose chain runtime services: %v", err)
	}
	t.Cleanup(services.Close)
	if services.HybridScoringCache == nil {
		t.Fatal("HybridScoringCache must assemble under cacheDriver=redis")
	}
	if services.HybridRuntimeState == nil {
		t.Fatal("HybridRuntimeState must assemble under runtimeStateDriver=redis")
	}
	if services.Identity == nil || services.Identity.Identity == nil || services.Identity.Affinity == nil {
		t.Fatal("G14 identity services must assemble")
	}
	// The assembled collaborators interop with the same redis keys.
	ctx := context.Background()
	if err := services.HybridRuntimeState.SetJSON(ctx, "interop", map[string]any{"ok": true}, 60_000); err != nil {
		t.Fatalf("set via assembled state store: %v", err)
	}
	if _, err := server.Get("juhe-ai:compose-test:state:gateway-hybrid-route-affinity:interop"); err != nil {
		t.Fatalf("assembled state key missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// item 4: pricing estimate vectors
// ---------------------------------------------------------------------------

// TestChainCostEstimatorEstimateVectors pins the estimate math against the
// catalog pricing rows (input/output tokens, service tier, long-context
// multipliers).
func TestChainCostEstimatorEstimateVectors(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed catalog row: %v: %v", query, err)
		}
	}
	tiers, _ := json.Marshal(map[string]any{
		"priority": map[string]any{"inputUsdPer1M": 4.0, "outputUsdPer1M": 16.0},
	})
	seed(`INSERT INTO provider_model_catalog (
			id, status, provider_code, model, mode, source, catalog_visible,
			input_usd_per_1m, output_usd_per_1m,
			long_context_input_token_threshold, long_context_input_token_threshold_inclusive,
			long_context_input_cost_multiplier, long_context_output_cost_multiplier,
			supported_service_tiers_json, service_tier_prices_json,
			created_at, updated_at)
		VALUES ('cat_est', 'active', 'openai', 'gpt-est', 'chat', 'builtin', 1,
			2.5, 10.0,
			100000, 0,
			2.0, 1.5,
			?, ?, ?, ?)`, `["priority"]`, string(tiers), now, now)
	// The cache catalog cache must pick the new row up: build a fresh cache.
	cache := fixture.cache

	estimator := newChainCostEstimator(cache)
	ctx := context.Background()

	cases := []struct {
		name          string
		model         string
		tier          string
		inputTokens   int64
		outputTokens  int64
		want          float64
		wantEstimated bool
	}{
		{"standard-rate", "gpt-est", "default", 10_000, 10_000, 0.125, true},
		{"priority-tier", "gpt-est", "priority", 10_000, 10_000, 0.2, true},
		{"unknown-tier", "gpt-est", "flex", 10_000, 10_000, 0, false},
		{"long-context", "gpt-est", "default", 200_000, 200_000, 2*2.5*0.2 + 1.5*10*0.2, true},
		{"below-threshold", "gpt-est", "default", 100_000, 100_000, 1.25, true},
		{"unknown-model", "gpt-missing", "default", 1_000, 1_000, 0, false},
		{"unpriced-input", "gpt-est", "default", 0, 1_000_000, 10.0, true},
	}
	for _, testCase := range cases {
		got, ok := estimator.EstimateCatalogCostUSD(ctx, gatewayquota.CatalogCostInput{
			ProviderCode:    "openai",
			SystemAccountID: fixture.systemAccount,
			Model:           testCase.model,
			ServiceTier:     testCase.tier,
			InputTokens:     testCase.inputTokens,
			OutputTokens:    testCase.outputTokens,
		})
		if ok != testCase.wantEstimated {
			t.Fatalf("%s: ok=%v want %v (got %v)", testCase.name, ok, testCase.wantEstimated, got)
		}
		if ok && absFloat64(got-testCase.want) > 1e-9 {
			t.Fatalf("%s: estimate=%v want %v", testCase.name, got, testCase.want)
		}
	}
}

func absFloat64(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// ---------------------------------------------------------------------------
// item 4: openai-compatible route probe
// ---------------------------------------------------------------------------

// TestComposeSystemAPIServesOpenAICompatFamilies: /v1/files answers with the
// openaicompat 401 contract without a key, /v1/vector_stores with a valid
// gateway key reaches the route surface, and a non-family /v1 path keeps the
// Node 404 JSON.
func TestComposeSystemAPIServesOpenAICompatFamilies(t *testing.T) {
	cfg := composeTestConfig(t)
	cfg.ChainEnabled = true
	store := openComposeOperationStore(t)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store))
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)

	// Seed one gateway API key over the seeded default route strategy
	// (bootstrap seedDefaults creates the sys_admin default strategy).
	secret := "sk-compose-compat"
	sealed, sealErr := apikeys.EncryptJSON("compose-test-secret", map[string]string{"key": secret})
	if sealErr != nil {
		t.Fatalf("seal compat key: %v", sealErr)
	}
	var strategyID string
	if err := composed.DB.QueryRow(
		`SELECT id FROM route_strategies WHERE system_account_id = 'sys_admin' AND status = 'active' ORDER BY created_at ASC LIMIT 1`,
	).Scan(&strategyID); err != nil {
		t.Fatalf("query default strategy: %v", err)
	}
	if _, err := composed.DB.Exec(`INSERT INTO api_keys (
			id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
			key_secret_encrypted, status, is_default, created_at, updated_at
		) VALUES ('key_compat', 'sys_admin', ?, '兼容探测', ?, 'sk-compose', 'compat', ?, 'active', 0, ?, ?)`,
		strategyID, gatewayruntimecache.HashSecret(secret), sealed, "2026-09-04T00:00:00.000Z", "2026-09-04T00:00:00.000Z"); err != nil {
		t.Fatalf("seed compat api key: %v", err)
	}

	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}

	// /v1/files without a key: the openaicompat 401 contract.
	response, err := client.Get(server.URL + "/v1/files")
	if err != nil {
		t.Fatalf("GET /v1/files: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/v1/files status=%d body=%s", response.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "invalid_api_key") {
		t.Fatalf("/v1/files body=%s", string(body))
	}

	// /v1/vector_stores with a valid gateway key: the route surface answers
	// (the list route renders an empty page).
	list, err := client.Get(server.URL + "/v1/vector_stores")
	if err != nil {
		t.Fatalf("GET /v1/vector_stores: %v", err)
	}
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if list.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/v1/vector_stores unauthenticated status=%d body=%s", list.StatusCode, string(listBody))
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/vector_stores", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	authed, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET /v1/vector_stores with key: %v", err)
	}
	authedBody, _ := io.ReadAll(authed.Body)
	authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("/v1/vector_stores status=%d body=%s", authed.StatusCode, string(authedBody))
	}

	// Non-family /v1 paths keep the Node 404 JSON contract.
	unknown, err := client.Get(server.URL + "/v1/definitely-not-a-protocol-path")
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	unknownBody, _ := io.ReadAll(unknown.Body)
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound || !strings.Contains(string(unknownBody), "资源不存在") {
		t.Fatalf("unknown path status=%d body=%s", unknown.StatusCode, string(unknownBody))
	}

	// The wrong-method probe on a family path falls through to the 404 JSON
	// (express routers do not match unmatched methods).
	wrongMethod, err := client.Post(server.URL+"/v1/vector_stores/not-a-route", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST family miss: %v", err)
	}
	wrongMethodBody, _ := io.ReadAll(wrongMethod.Body)
	wrongMethod.Body.Close()
	if wrongMethod.StatusCode != http.StatusNotFound {
		t.Fatalf("family miss status=%d body=%s", wrongMethod.StatusCode, string(wrongMethodBody))
	}
}

func strPtrCompat(value string) *string { return &value }
