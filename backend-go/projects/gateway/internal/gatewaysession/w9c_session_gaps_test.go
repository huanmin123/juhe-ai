package gatewaysession

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// ttlcache.go gaps (Clear / Len / evictOverflow / dropStoreIfDisabled).
// ---------------------------------------------------------------------------

func TestW9CTTLCacheLifecycle(t *testing.T) {
	cleared := 0
	disposed := []string{}
	cache := newTTLCache[string](2, time.Minute, false)
	cache.onClear = func() { cleared++ }
	cache.dispose = func(key string, _ string) { disposed = append(disposed, key) }

	cache.Set("a", "1")
	cache.Set("b", "2")
	if cache.Len() != 2 {
		t.Fatalf("Len = %d", cache.Len())
	}
	cache.Clear()
	if cache.Len() != 0 || cleared != 1 {
		t.Fatalf("after clear: len=%d cleared=%d", cache.Len(), cleared)
	}

	// evictOverflow: capacity 2, third insert disposes the LRU entry.
	cache.Set("a", "1")
	cache.Set("b", "2")
	cache.Set("c", "3")
	if cache.Len() != 2 {
		t.Fatalf("overflow len = %d", cache.Len())
	}
	if _, ok := cache.Get("a"); ok {
		t.Fatal("oldest entry must be evicted")
	}
	if _, ok := cache.Get("c"); !ok {
		t.Fatal("newest entry must survive")
	}
	if len(disposed) == 0 || disposed[0] != "a" {
		t.Fatalf("disposed = %v", disposed)
	}

	// nowTime fallback without a wired clock.
	if cache.nowTime().IsZero() {
		t.Fatal("nowTime fallback must produce a time")
	}
}

func TestW9CTTLCacheDropStoreIfDisabled(t *testing.T) {
	readable := true
	resets := 0
	cache := newTTLCache[string](4, time.Minute, false)
	cache.readable = func() bool { return readable }
	cache.onReset = func() { resets++ }

	cache.Set("k", "v")
	if value, ok := cache.Get("k"); !ok || value != "v" {
		t.Fatal("readable cache must serve values")
	}

	// Disabled with entries: first access resets the store and fires onReset.
	readable = false
	if _, ok := cache.Get("k"); ok {
		t.Fatal("disabled cache must miss")
	}
	if resets != 1 || cache.Len() != 0 {
		t.Fatalf("resets=%d len=%d", resets, cache.Len())
	}
	// Disabled and empty: no further resets.
	if _, ok := cache.Get("k"); ok {
		t.Fatal("disabled cache must stay a miss")
	}
	if resets != 1 {
		t.Fatalf("empty disabled store must not reset again: %d", resets)
	}
	// Set while disabled parks the value; re-enabling serves it again.
	cache.Set("k2", "v2")
	readable = true
	if value, ok := cache.Get("k2"); !ok || value != "v2" {
		t.Fatal("re-enabled cache must serve the written value")
	}
}

// ---------------------------------------------------------------------------
// canonicalizer.go gaps (jsJSONString escape surface).
// ---------------------------------------------------------------------------

func TestW9CJsJSONString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", `"plain"`},
		{`q"uote`, `"q\"uote"`},
		{"back\\slash", `"back\\slash"`},
		{"nl\n", `"nl\n"`},
		{"cr\r", `"cr\r"`},
		{"tab\t", `"tab\t"`},
		{"bs\x08", `"bs\b"`},
		{"ff\x0c", `"ff\f"`},
		{"ctl\x01", "\"ctl\\u0001\""},
		{"keep\x7f", "\"keep\x7f\""},
	}
	for _, tc := range cases {
		if got := jsJSONString(tc.in); got != tc.want {
			t.Fatalf("jsJSONString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
	if got := jsJSONStringArray([]string{"a", `b"c`}); got != `["a","b\"c"]` {
		t.Fatalf("jsJSONStringArray = %s", got)
	}
	if got := jsJSONStringArray(nil); got != "[]" {
		t.Fatalf("empty array = %s", got)
	}
}

// ---------------------------------------------------------------------------
// redisdriver.go gaps (dial, lazy client, namespace helpers).
// ---------------------------------------------------------------------------

func TestW9CRedisNamespaceHelpers(t *testing.T) {
	if _, err := RedisNamespacedKey("dev", "  "); err == nil {
		t.Fatal("empty key must fail")
	}
	if _, err := RedisNamespacedKey(" ", "k"); err == nil {
		t.Fatal("empty namespace must fail")
	}
	got, err := RedisNamespacedKey("dev", "plain-key")
	if err != nil || got != "juhe-ai:dev:plain-key" {
		t.Fatalf("namespaced key = %s, %v", got, err)
	}
	got, err = RedisNamespacedKey("dev", "juhe-ai:other:key")
	if err != nil || got != "juhe-ai:dev:other:key" {
		t.Fatalf("root prefix rebind = %s, %v", got, err)
	}
	got, err = RedisNamespacedKey("dev", "juhe-ai:dev:keep")
	if err != nil || got != "juhe-ai:dev:keep" {
		t.Fatalf("already prefixed = %s, %v", got, err)
	}
	prefix, err := RedisNamespacePrefix("dev")
	if err != nil || prefix != "juhe-ai:dev:" {
		t.Fatalf("prefix = %s, %v", prefix, err)
	}
	if _, err := RedisNamespacePrefix("///"); err == nil {
		t.Fatal("blank sanitized namespace must fail")
	}
	if got, err := SanitizeRedisNamespacePart("a b/c"); err != nil || got != "a_b_c" {
		t.Fatalf("sanitize = %s, %v", got, err)
	}
	if got, err := SanitizeRedisNamespacePart(" _x_ "); err != nil || got != "x" {
		t.Fatalf("trim sanitize = %s, %v", got, err)
	}
}

func TestW9CGoRedisClientAgainstMiniredis(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := DialGoRedisClient("redis://" + server.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Client.Close() })
	ctx := context.Background()

	// Get miss decodes redis.Nil into (nil, nil).
	if value, err := client.Get(ctx, "missing"); err != nil || value != nil {
		t.Fatalf("missing get = %v, %v", value, err)
	}
	if err := client.SetPX(ctx, "k", "v", 60_000); err != nil {
		t.Fatalf("setpx: %v", err)
	}
	if value, err := client.Get(ctx, "k"); err != nil || value == nil || *value != "v" {
		t.Fatalf("get = %v, %v", value, err)
	}
	if err := client.Del(ctx); err != nil {
		t.Fatalf("del without keys must be a no-op: %v", err)
	}
	if err := client.Del(ctx, "k"); err != nil {
		t.Fatalf("del: %v", err)
	}
	result, err := client.Eval(ctx, "return 1", []string{})
	if err != nil || result == nil {
		t.Fatalf("eval = %v, %v", result, err)
	}
	ping, err := client.SendCommand(ctx, "PING")
	if err != nil || ping != "PONG" {
		t.Fatalf("send ping = %v, %v", ping, err)
	}

	// Bad URL fails at parse time.
	if _, err := DialGoRedisClient("://nope"); err == nil {
		t.Fatal("invalid url must fail")
	}
	// NewGoRedisClient wraps an existing client.
	wrapped := NewGoRedisClient(goredis.NewClient(&goredis.Options{Addr: server.Addr()}))
	t.Cleanup(func() { _ = wrapped.Client.Close() })
	if err := wrapped.SetPX(ctx, "w", "1", 1000); err != nil {
		t.Fatalf("wrapped setpx: %v", err)
	}
}

type w9cFailingRedis struct{}

func (w9cFailingRedis) Get(context.Context, string) (*string, error) {
	return nil, errors.New("get boom")
}
func (w9cFailingRedis) SetPX(context.Context, string, string, int64) error {
	return errors.New("set boom")
}
func (w9cFailingRedis) Del(context.Context, ...string) error { return errors.New("del boom") }
func (w9cFailingRedis) Eval(context.Context, string, []string, ...any) (any, error) {
	return nil, errors.New("eval boom")
}
func (w9cFailingRedis) SendCommand(context.Context, ...any) (any, error) {
	return nil, errors.New("send boom")
}

func TestW9CLazyRedisClient(t *testing.T) {
	// Empty URL fails on first use.
	lazy := newLazyRedisClient("")
	if _, err := lazy.client(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("empty url err = %v", err)
	}
	// Bad URL surfaces the dial error once and never caches it.
	badLazy := newLazyRedisClient("://nope")
	if _, err := badLazy.client(); err == nil {
		t.Fatal("bad url must fail")
	}
	// Valid URL dials once and caches the delegate.
	server := miniredis.RunT(t)
	goodLazy := newLazyRedisClient("redis://" + server.Addr())
	first, err := goodLazy.client()
	if err != nil {
		t.Fatalf("lazy dial: %v", err)
	}
	second, err := goodLazy.client()
	if err != nil || second != first {
		t.Fatal("lazy client must cache the delegate")
	}
}

// ---------------------------------------------------------------------------
// resolvers.go gaps (IDs, wrong-profile short-circuit, header collection).
// ---------------------------------------------------------------------------

type w9cFakeIdentityRequest struct {
	headers map[string][]string
}

func (r w9cFakeIdentityRequest) OriginalURL() string { return "/v1/responses" }
func (r w9cFakeIdentityRequest) Path() string        { return "/responses" }
func (r w9cFakeIdentityRequest) HeaderValues(name string) []string {
	return r.headers[strings.ToLower(name)]
}

func TestW9CSessionHeaderResolvers(t *testing.T) {
	if (CodexSessionHeaderResolver{}).ID() != CodexSessionHeaderResolverID {
		t.Fatal("codex resolver id mismatch")
	}
	if (ClaudeCodeSessionHeaderResolver{}).ID() != ClaudeCodeSessionHeaderResolverID {
		t.Fatal("claude resolver id mismatch")
	}
	codex := CodexSessionHeaderResolver{}
	// Wrong profile → no candidates.
	if got := codex.Collect(ResolverContext{ClientProfile: "claude_code", NormalizedPath: "/responses"}); got != nil {
		t.Fatal("wrong profile must skip")
	}
	// Wrong path → no candidates.
	if got := codex.Collect(ResolverContext{ClientProfile: "codex", NormalizedPath: "/chat/completions"}); got != nil {
		t.Fatal("wrong path must skip")
	}
	// /responses/compact is also a codex session path.
	got := codex.Collect(ResolverContext{
		Request:        w9cFakeIdentityRequest{headers: map[string][]string{"session-id": {"s1", "s2"}}},
		ClientProfile:  "codex",
		NormalizedPath: "/responses/compact",
	})
	if len(got) != 2 {
		t.Fatalf("codex candidates = %+v", got)
	}
	if got[0].SemanticNamespace != CodexSessionSemanticNamespace || got[0].Priority != codexSessionResolverPriority {
		t.Fatalf("candidate metadata = %+v", got[0])
	}

	claude := ClaudeCodeSessionHeaderResolver{}
	if got := claude.Collect(ResolverContext{ClientProfile: "codex", NormalizedPath: "/messages"}); got != nil {
		t.Fatal("wrong profile must skip")
	}
	if got := claude.Collect(ResolverContext{ClientProfile: "claude_code", NormalizedPath: "/responses"}); got != nil {
		t.Fatal("wrong path must skip")
	}
	got = claude.Collect(ResolverContext{
		Request:        w9cFakeIdentityRequest{headers: map[string][]string{"x-claude-code-session-id": {"c1"}}},
		ClientProfile:  "claude_code",
		NormalizedPath: "/messages",
	})
	if len(got) != 1 || got[0].SemanticNamespace != ClaudeCodeSessionSemanticNamespace {
		t.Fatalf("claude candidates = %+v", got)
	}
	// Header absent → nil.
	if got := claude.Collect(ResolverContext{
		Request:        w9cFakeIdentityRequest{headers: map[string][]string{}},
		ClientProfile:  "claude_code",
		NormalizedPath: "/messages",
	}); got != nil {
		t.Fatal("absent header must skip")
	}
}

// ---------------------------------------------------------------------------
// portgateway.go gaps (nil request views).
// ---------------------------------------------------------------------------

func TestW9CGatewayRequestIdentityViewNilGuards(t *testing.T) {
	var empty gatewayRequestIdentityView
	if empty.OriginalURL() != "" || empty.Path() != "" || empty.HeaderValues("session-id") != nil {
		t.Fatal("nil request view must be inert")
	}
}

// ---------------------------------------------------------------------------
// affinity.go gaps (remember/forget/migrate flows, scope helpers).
// ---------------------------------------------------------------------------

func TestW9CRememberAndClaimLocal(t *testing.T) {
	svc, clock, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// Sync remember (memory driver) then claim the same session.
	svc.RememberOpenAIAccountForSession("aff-key-1", "acc-1", scope)
	if owner := svc.claimOpenAIAccountForSessionLocal("aff-key-1", "acc-2", scope); owner != "acc-1" {
		t.Fatalf("first binder must win, owner = %s", owner)
	}
	// Async remember path.
	svc.RememberOpenAIAccountForSessionAsync(context.Background(), "aff-key-2", "acc-2", scope)
	svc.mu.Lock()
	binding := svc.sessionAffinityCacheGetLocked("aff-key-2")
	svc.mu.Unlock()
	if binding == nil || binding.AccountID != "acc-2" {
		t.Fatalf("async remember binding = %+v", binding)
	}
	// Local entry point.
	svc.RememberOpenAIAccountForSessionLocal("aff-key-3", "acc-3", nil)
	svc.mu.Lock()
	binding = svc.sessionAffinityCacheGetLocked("aff-key-3")
	svc.mu.Unlock()
	if binding == nil || binding.AccountID != "acc-3" {
		t.Fatalf("local remember binding = %+v", binding)
	}

	// Forget with a matching account id removes the binding.
	svc.ForgetOpenAIAccountForSession("aff-key-1", "acc-1")
	svc.mu.Lock()
	binding = svc.sessionAffinityCacheGetLocked("aff-key-1")
	svc.mu.Unlock()
	if binding != nil {
		t.Fatal("forget must drop the binding")
	}
	// Forget with a mismatching account id keeps it.
	svc.ForgetOpenAIAccountForSession("aff-key-2", "acc-other")
	svc.mu.Lock()
	binding = svc.sessionAffinityCacheGetLocked("aff-key-2")
	svc.mu.Unlock()
	if binding == nil {
		t.Fatal("mismatched forget must keep the binding")
	}
	// Blank keys are no-ops.
	svc.RememberOpenAIAccountForSession("", "acc-1", scope)
	svc.ForgetOpenAIAccountForSession("", "acc-1")
	_ = clock
}

func TestW9CTrafficMigrationPreferenceMemory(t *testing.T) {
	svc, clock, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	// Remember + read back through the ordering path: the preference only
	// applies while the source account is NOT among the candidates, and a
	// read that finds the source consumes (deletes) the preference.
	svc.RememberOpenAIAccountTrafficMigrationPreference("acc-a", "acc-b", scope)
	preference := svc.trafficMigrationPreferenceForAccounts([]string{"acc-b", "acc-x"}, scope)
	if preference == nil || preference.TargetAccountID != "acc-b" || preference.SourceAccountID != "acc-a" {
		t.Fatalf("preference = %+v", preference)
	}
	// Candidates containing the source consume the preference.
	if preference := svc.trafficMigrationPreferenceForAccounts([]string{"acc-a", "acc-b"}, scope); preference != nil {
		t.Fatalf("source in candidates must consume the preference: %+v", preference)
	}
	if again := svc.trafficMigrationPreferenceForAccounts([]string{"acc-b", "acc-x"}, scope); again != nil {
		t.Fatalf("consumed preference must stay deleted: %+v", again)
	}
	// Async memory path.
	if err := svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(context.Background(), "acc-c", "acc-d", scope, TrafficMigrationPreferenceWriteOptions{ThrowOnRedisError: true}); err != nil {
		t.Fatalf("memory async write = %v", err)
	}
	// Invalid inputs are ignored.
	svc.RememberOpenAIAccountTrafficMigrationPreference("", "acc-b", scope)
	svc.RememberOpenAIAccountTrafficMigrationPreference("acc-a", "acc-a", scope)
	svc.RememberOpenAIAccountTrafficMigrationPreference("acc-a", "acc-b", nil)
	svc.RememberOpenAIAccountTrafficMigrationPreference("acc-a", "acc-b", &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1"})
	// Scope key derivation arms.
	if key, ok := trafficMigrationPreferenceScopeKey(scope); !ok || key != "sys-1:*:grp-1" {
		t.Fatalf("group scope key = %s %v", key, ok)
	}
	withAPIKey := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	if key, ok := trafficMigrationPreferenceScopeKey(withAPIKey); !ok || key != "sys-1:key-1:grp-1" {
		t.Fatalf("api-key scope key = %s %v", key, ok)
	}
	keys := trafficMigrationPreferenceScopeKeys(withAPIKey)
	if len(keys) != 2 || keys[0] != "sys-1:key-1:grp-1" || keys[1] != "sys-1:*:grp-1" {
		t.Fatalf("scope keys = %v", keys)
	}
	if _, ok := trafficMigrationPreferenceScopeKey(nil); ok {
		t.Fatal("nil scope must not yield a key")
	}
	if _, ok := trafficMigrationPreferenceScopeKey(&OpenAIGatewaySessionAffinityScope{SystemAccountID: "s"}); ok {
		t.Fatal("scope without group must not yield a key")
	}
	// TTL expiry clears the preference.
	clock.Advance(time.Duration(trafficMigrationPreferenceTtlMs) * time.Millisecond)
	preference = svc.trafficMigrationPreferenceForAccounts([]string{"acc-b", "acc-x"}, scope)
	if preference != nil {
		t.Fatalf("expired preference = %+v", preference)
	}
}

func TestW9CSessionBindingMatchesScope(t *testing.T) {
	if sessionBindingMatchesScope(&SessionBinding{AccountID: "a"}, &OpenAIGatewaySessionAffinityScope{SystemAccountID: "s"}) {
		t.Fatal("nil binding scope never matches")
	}
	binding := &SessionBinding{AccountID: "a", Scope: &OpenAIGatewaySessionAffinityScope{SystemAccountID: "s1", APIKeyID: "k1"}}
	if !sessionBindingMatchesScope(binding, &OpenAIGatewaySessionAffinityScope{}) {
		t.Fatal("empty filter matches everything scoped")
	}
	if !sessionBindingMatchesScope(binding, &OpenAIGatewaySessionAffinityScope{SystemAccountID: "s1"}) {
		t.Fatal("matching system scope")
	}
	if sessionBindingMatchesScope(binding, &OpenAIGatewaySessionAffinityScope{SystemAccountID: "s2"}) {
		t.Fatal("mismatched system scope")
	}
	if sessionBindingMatchesScope(binding, &OpenAIGatewaySessionAffinityScope{APIKeyID: "k2"}) {
		t.Fatal("mismatched api key scope")
	}
	if !sessionBindingMatchesScope(binding, &OpenAIGatewaySessionAffinityScope{SystemAccountID: "s1", APIKeyID: "k1"}) {
		t.Fatal("full match")
	}
}

func TestW9CMigrationCandidateKeysLocked(t *testing.T) {
	svc, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	svc.mu.Lock()
	svc.setSessionAffinityBindingLocked("aff-m1", SessionBinding{AccountID: "acc-1", Scope: scope})
	svc.setSessionAffinityBindingLocked("aff-m2", SessionBinding{AccountID: "acc-1", Scope: &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1"}})
	svc.mu.Unlock()

	svc.mu.Lock()
	defer svc.mu.Unlock()
	apiKeyScoped := svc.sessionAffinityMigrationCandidateKeysLocked("acc-1", scope)
	if len(apiKeyScoped) != 1 || apiKeyScoped[0] != "aff-m1" {
		t.Fatalf("api-key scoped candidates = %v", apiKeyScoped)
	}
	systemScoped := svc.sessionAffinityMigrationCandidateKeysLocked("acc-1", &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1"})
	if len(systemScoped) != 2 {
		t.Fatalf("system scoped candidates = %v", systemScoped)
	}
	all := svc.sessionAffinityMigrationCandidateKeysLocked("acc-1", nil)
	if len(all) != 2 {
		t.Fatalf("unscoped candidates = %v", all)
	}
	if got := svc.sessionAffinityMigrationCandidateKeysLocked("acc-none", nil); len(got) != 0 {
		t.Fatalf("unknown account = %v", got)
	}
}

// ---------------------------------------------------------------------------
// scheduling.go gaps (policy validation arms, jsNumberValue, limits).
// ---------------------------------------------------------------------------

func TestW9CValidateGroupSchedulingPolicyArms(t *testing.T) {
	defaults := SchedulingDefaults{GlobalMax: 5000}
	// Unknown key.
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"nope": 1}, defaults); err == nil {
		t.Fatal("unknown key must fail")
	}
	// Non-high-concurrency group types skip validation entirely.
	policy, err := ResolveGroupSchedulingPolicy(GroupTypePersonal, map[string]any{"nope": 1}, defaults)
	if err != nil || policy != nil {
		t.Fatalf("personal policy = %+v, %v", policy, err)
	}
	// Numeric arms.
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"slowRequestThresholdMs": "x"}, defaults); err == nil {
		t.Fatal("non-number must fail")
	}
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"slowRequestThresholdMs": 1.5}, defaults); err == nil {
		t.Fatal("fraction must fail")
	}
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"slowRequestThresholdMs": 0}, defaults); err == nil {
		t.Fatal("below min must fail")
	}
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"maxQueueWaitMs": 3_600_001}, defaults); err == nil {
		t.Fatal("above max must fail")
	}
	// Boolean arm.
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"fastFirstEnabled": "yes"}, defaults); err == nil {
		t.Fatal("non-boolean must fail")
	}
	// Mode arm.
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"mode": "turbo"}, defaults); err == nil {
		t.Fatal("bad mode must fail")
	}
	// Overflow mode arm.
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"clientIpConcurrencyOverflowMode": "ignore"}, defaults); err == nil {
		t.Fatal("bad overflow mode must fail")
	}
	// perApiKeyQueueLimit above maxQueueSize fails; within passes.
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"maxQueueSize": 10, "perApiKeyQueueLimit": 11}, defaults); err == nil {
		t.Fatal("perApiKeyQueueLimit above maxQueueSize must fail")
	}
	policy, err = ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{
		"maxQueueSize": 10, "perApiKeyQueueLimit": float64(4), "fastFirstEnabled": false,
		"clientIpConcurrencyLimit": float64(2), "clientIpConcurrencyOverflowMode": "queue",
		"imageLaneMaxConcurrency": float64(3),
	}, defaults)
	if err != nil {
		t.Fatalf("valid policy = %v", err)
	}
	if policy.PerAPIKeyQueueLimit != 4 || policy.FastFirstEnabled || policy.ClientIPConcurrencyLimit != 2 ||
		policy.ClientIPConcurrencyOverflowMode != "queue" || policy.ImageLaneMaxConcurrency != 3 || policy.MaxQueueSize != 10 {
		t.Fatalf("policy = %+v", policy)
	}
	// jsNumberValue accepts the integral Go types too.
	for _, value := range []any{float64(1), float32(1), 1, int32(1), int64(1)} {
		if number, ok := jsNumberValue(value); !ok || number != 1 {
			t.Fatalf("jsNumberValue(%#v) = %v %v", value, number, ok)
		}
	}
	if _, ok := jsNumberValue("1"); ok {
		t.Fatal("string is not a js number")
	}
}

func TestW9CEffectiveLimits(t *testing.T) {
	defaults := SchedulingDefaults{GlobalMax: 5000}
	soft, err := EffectiveSoftConcurrencyLimit(100, nil, defaults)
	if err != nil || soft != 100 {
		t.Fatalf("soft = %d, %v", soft, err)
	}
	// Policy soft cap below the account limit clamps.
	soft, err = EffectiveSoftConcurrencyLimit(100, map[string]any{"defaultSoftConcurrency": 42}, defaults)
	if err != nil || soft != 42 {
		t.Fatalf("clamped soft = %d, %v", soft, err)
	}
	if _, err := EffectiveSoftConcurrencyLimit(100, map[string]any{"defaultSoftConcurrency": "x"}, defaults); err == nil {
		t.Fatal("invalid policy must fail")
	}
	image, err := EffectiveImageLaneConcurrencyLimit(100, nil, defaults)
	if err != nil || image != 100 {
		t.Fatalf("image lane default = %d, %v", image, err)
	}
	image, err = EffectiveImageLaneConcurrencyLimit(100, map[string]any{"imageLaneMaxConcurrency": 7}, defaults)
	if err != nil || image != 7 {
		t.Fatalf("image lane configured = %d, %v", image, err)
	}
	// resolvePerAPIKeyQueueLimit nil falls back to maxQueueSize.
	fallback, err := resolvePerAPIKeyQueueLimit(nil, 33)
	if err != nil || fallback != 33 {
		t.Fatalf("queue fallback = %d, %v", fallback, err)
	}
	if positiveIntegerBounded(0, 5, 100) != 5 {
		t.Fatal("invalid positive integer falls back")
	}
	if positiveIntegerBounded(9, 5, 100) != 9 {
		t.Fatal("valid positive integer passes")
	}
	// resolvePolicyOrDefault with an invalid map errors; empty map resolves defaults.
	svc, _, _ := newTestAffinityService(t, nil)
	if _, err := resolvePolicyOrDefault(map[string]any{"mode": "bad"}, svc.schedulingDefaults); err == nil {
		t.Fatal("invalid policy must error")
	}
	resolved, err := resolvePolicyOrDefault(nil, svc.schedulingDefaults)
	if err != nil || !resolved.FastFirstEnabled {
		t.Fatalf("default policy = %+v, %v", resolved, err)
	}
}

// ---------------------------------------------------------------------------
// concurrencyidentity.go gaps.
// ---------------------------------------------------------------------------

func TestW9CConcurrencyIdentityHelpers(t *testing.T) {
	source := "cred-1"
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", CredentialSourceAccountID: &source, ConcurrencyLimit: 0}
	if got := GatewayAccountConcurrencyAccountID(GatewayAccountConcurrencyIdentityOf(account)); got != "cred-1" {
		t.Fatalf("credential source id = %s", got)
	}
	noSource := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-2"}
	if got := GatewayAccountConcurrencyAccountID(GatewayAccountConcurrencyIdentityOf(noSource)); got != "acc-2" {
		t.Fatalf("plain id = %s", got)
	}
	ids := GatewayAccountConcurrencyAccountIDs(GatewayAccountConcurrencyIdentities([]gatewayruntimecache.OpenAIAccountSecret{account, noSource, account}))
	if len(ids) != 2 {
		t.Fatalf("deduped ids = %v", ids)
	}
	// Hard limit clamps to 1.
	if accountHardConcurrencyLimit(account) != 1 {
		t.Fatal("limit <= 1 must clamp to 1")
	}
	if accountHardConcurrencyLimit(gatewayruntimecache.OpenAIAccountSecret{ID: "x", ConcurrencyLimit: 7}) != 7 {
		t.Fatal("higher limit must pass through")
	}
	// Quality rank comparisons.
	scored := gatewayruntimecache.OpenAIAccountSecret{ID: "s", QualityScore: ptrFloat(0.5)}
	unscored := gatewayruntimecache.OpenAIAccountSecret{ID: "u"}
	if delta := compareAccountQualityRank(scored, unscored); delta != -1 {
		t.Fatalf("scored vs unscored = %d", delta)
	}
	if delta := compareAccountQualityRank(unscored, scored); delta != 1 {
		t.Fatalf("unscored vs scored = %d", delta)
	}
	if delta := compareAccountQualityRank(unscored, gatewayruntimecache.OpenAIAccountSecret{ID: "u2"}); delta != 0 {
		t.Fatalf("two unscored = %d", delta)
	}
	if accountQualityRank(unscored) <= 0 {
		t.Fatal("missing quality must rank +Inf")
	}
	if accountFallbackRank(gatewayruntimecache.OpenAIAccountSecret{ID: "f", FallbackEnabled: true}) != 1 {
		t.Fatal("fallback rank must be 1")
	}
}

func TestW9COrderOpenAIAccountsByModelPriority(t *testing.T) {
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("unsupported", 1, nil),
		testAccount("mapping", 2, nil),
		testAccount("direct", 3, nil),
	}
	priority := &GatewayAccountModelPriority{
		RequestedModel: "m",
		RankByAccountID: map[string]int{
			"direct":  ModelPriorityRankDirect,
			"mapping": ModelPriorityRankMapping,
		},
	}
	ordered := orderOpenAIAccountsByModelPriority(accounts, priority)
	if ordered[0].ID != "direct" || ordered[1].ID != "mapping" || ordered[2].ID != "unsupported" {
		t.Fatalf("ordered = %v", accountsIDs(ordered))
	}
	// Nil priority keeps input order; short lists pass through.
	if got := orderOpenAIAccountsByModelPriority(accounts, nil); got[0].ID != "unsupported" {
		t.Fatalf("nil priority order = %v", accountsIDs(got))
	}
	if len(orderOpenAIAccountsByModelPriority(accounts[:1], priority)) != 1 {
		t.Fatal("single account passes through")
	}
	if CompareGatewayAccountModelPriority("direct", "mapping", priority) >= 0 {
		t.Fatal("direct must outrank mapping")
	}
	if GatewayAccountModelPriorityRank("unknown", priority) != ModelPriorityRankUnsupported {
		t.Fatal("unknown account must rank unsupported")
	}
	if GatewayAccountModelPriorityRank("any", nil) != ModelPriorityRankDirect {
		t.Fatal("nil priority ranks everything direct")
	}
}

// ---------------------------------------------------------------------------
// affinityorder.go gaps (sign helpers, traffic migration ordering,
// hard-busy-last, busy lanes).
// ---------------------------------------------------------------------------

func TestW9CSignHelpers(t *testing.T) {
	for _, value := range []int{-7, 0, 9} {
		want := 0
		if value < 0 {
			want = -1
		} else if value > 0 {
			want = 1
		}
		if got := sign(value); got != want {
			t.Fatalf("sign(%d) = %d", value, got)
		}
	}
	if signInt64(-9) != -1 || signInt64(9) != 1 || signInt64(0) != 0 {
		t.Fatal("signInt64 broken")
	}
	if ptrIntFromMap(map[string]int{"a": 1}, "a") == nil {
		t.Fatal("present key must return a pointer")
	}
	if ptrIntFromMap(map[string]int{}, "a") != nil {
		t.Fatal("missing key must return nil")
	}
}

func TestW9COrderOpenAIAccountsByTrafficMigrationPreference(t *testing.T) {
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 1, nil),
		testAccount("b", 1, nil),
		testAccount("target", 1, nil),
	}
	ordered := orderOpenAIAccountsByTrafficMigrationPreference(accounts, "target", nil)
	if ordered[0].ID != "target" {
		t.Fatalf("target must be promoted, got %v", accountsIDs(ordered))
	}
	// Target already first: unchanged.
	if got := orderOpenAIAccountsByTrafficMigrationPreference(ordered, "target", nil); accountsIDs(got)[0] != "target" {
		t.Fatal("first-position target must stay")
	}
	// Empty target: unchanged.
	if got := orderOpenAIAccountsByTrafficMigrationPreference(accounts, "", nil); accountsIDs(got)[0] != "a" {
		t.Fatal("empty target must keep the order")
	}
	// Single account: unchanged.
	if got := orderOpenAIAccountsByTrafficMigrationPreference(accounts[:1], "a", nil); len(got) != 1 {
		t.Fatal("single account must pass through")
	}
	// Model priority blocks the promotion past a higher-ranked peer.
	priority := &GatewayAccountModelPriority{RankByAccountID: map[string]int{
		"a": ModelPriorityRankDirect, "target": ModelPriorityRankMapping,
	}}
	ordered = orderOpenAIAccountsByTrafficMigrationPreference(accounts, "target", priority)
	if ordered[0].ID != "a" {
		t.Fatalf("model priority must block promotion, got %v", accountsIDs(ordered))
	}
}

func TestW9CHighConcurrencyHardBusyLast(t *testing.T) {
	svc, _, _ := newTestAffinityService(t, nil)
	busy := testAccount("busy", 1, func(secret *gatewayruntimecache.OpenAIAccountSecret) {
		secret.ConcurrencyLimit = 1
		secret.CurrentConcurrency = ptrInt(1)
	})
	free := testAccount("free", 1, func(secret *gatewayruntimecache.OpenAIAccountSecret) {
		secret.ConcurrencyLimit = 5
		secret.CurrentConcurrency = ptrInt(1)
	})
	ordered, err := svc.orderOpenAIHighConcurrencyHardBusyLast([]gatewayruntimecache.OpenAIAccountSecret{busy, free})
	if err != nil || ordered[0].ID != "free" {
		t.Fatalf("hard busy must sort last: %v, %v", accountsIDs(ordered), err)
	}
	// Async memory path delegates to the sync implementation.
	orderedAsync, err := svc.orderOpenAIHighConcurrencyHardBusyLastAsync(context.Background(), []gatewayruntimecache.OpenAIAccountSecret{busy, free})
	if err != nil || orderedAsync[0].ID != "free" {
		t.Fatalf("async hard busy = %v, %v", accountsIDs(orderedAsync), err)
	}
	// All busy or all free keep the input order.
	ordered, err = svc.orderOpenAIHighConcurrencyHardBusyLast([]gatewayruntimecache.OpenAIAccountSecret{free, free})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("all free = %v, %v", accountsIDs(ordered), err)
	}
	// Short lists pass through.
	if ordered, err = svc.orderOpenAIHighConcurrencyHardBusyLast([]gatewayruntimecache.OpenAIAccountSecret{busy}); err != nil || len(ordered) != 1 {
		t.Fatalf("short list = %v, %v", accountsIDs(ordered), err)
	}
}

func TestW9CAreHighConcurrencyAccountsBusyArms(t *testing.T) {
	svc, _, _ := newTestAffinityService(t, nil)
	personal := DispatchOrderingOptions{GroupType: GroupTypePersonal}
	hardBusy := testAccount("busy", 1, func(secret *gatewayruntimecache.OpenAIAccountSecret) {
		secret.ConcurrencyLimit = 1
		secret.CurrentConcurrency = ptrInt(1)
	})
	healthy := testAccount("ok", 1, func(secret *gatewayruntimecache.OpenAIAccountSecret) {
		secret.ConcurrencyLimit = 5
		secret.CurrentConcurrency = ptrInt(1)
	})

	// Non-high-concurrency groups are never busy.
	if svc.AreOpenAIHighConcurrencyAccountsHardBusy([]gatewayruntimecache.OpenAIAccountSecret{hardBusy}, personal) {
		t.Fatal("personal groups are never hard busy")
	}
	if svc.AreOpenAIHighConcurrencyAccountsHardBusy(nil, DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}) {
		t.Fatal("empty accounts are never hard busy")
	}
	if !svc.AreOpenAIHighConcurrencyAccountsHardBusy([]gatewayruntimecache.OpenAIAccountSecret{hardBusy, hardBusy}, DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}) {
		t.Fatal("all-hard-busy accounts must report busy")
	}
	if svc.AreOpenAIHighConcurrencyAccountsHardBusy([]gatewayruntimecache.OpenAIAccountSecret{hardBusy, healthy}, DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}) {
		t.Fatal("one healthy account keeps the group not hard busy")
	}

	// Busy-for-lane sync arms.
	busyOpts := BusyLaneOptions{DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}, ""}
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLane([]gatewayruntimecache.OpenAIAccountSecret{hardBusy}, busyOpts); err != nil || !busy {
		t.Fatalf("hard busy lane = %v, %v", busy, err)
	}
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLane([]gatewayruntimecache.OpenAIAccountSecret{healthy}, busyOpts); err != nil || busy {
		t.Fatalf("healthy lane = %v, %v", busy, err)
	}
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLane(nil, busyOpts); err != nil || busy {
		t.Fatalf("empty lane = %v, %v", busy, err)
	}
	noConcurrency := BusyLaneOptions{DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}, ""}
	bare, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) { cfg.Concurrency = nil })
	if busy, err := bare.AreOpenAIHighConcurrencyAccountsBusyForLane([]gatewayruntimecache.OpenAIAccountSecret{healthy}, noConcurrency); err != nil || busy {
		t.Fatalf("no concurrency source = %v, %v", busy, err)
	}

	// Image lane limit (the concurrency identity of `healthy` is its ID).
	imageOpts := BusyLaneOptions{DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}, RequestLaneImage}
	svc.cfg.Concurrency.(*mockConcurrency).SetLane(RequestLaneImage, "ok", 99)
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLane([]gatewayruntimecache.OpenAIAccountSecret{healthy}, imageOpts); err != nil || !busy {
		t.Fatalf("image lane busy = %v, %v", busy, err)
	}

	// Async variants on the memory runtime driver delegate to the sync path.
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(context.Background(), []gatewayruntimecache.OpenAIAccountSecret{hardBusy}, busyOpts); err != nil || !busy {
		t.Fatalf("async delegate = %v, %v", busy, err)
	}
}

func TestW9CCanSessionAffinityPromoteOver(t *testing.T) {
	base := testAccount("bound", 5, nil)
	same := testAccount("peer", 5, nil)
	if !canSessionAffinityPromoteOver(base, same, nil) {
		t.Fatal("identical accounts must promote (quality rank tie)")
	}
	// Super priority mismatch never promotes.
	super := testAccount("super", 5, func(secret *gatewayruntimecache.OpenAIAccountSecret) {
		secret.SuperPriorityEnabled = true
	})
	if canSessionAffinityPromoteOver(super, same, nil) {
		t.Fatal("super priority bound account must not promote over a normal peer")
	}
	// Fallback mismatch never promotes.
	fallback := testAccount("fb", 5, func(secret *gatewayruntimecache.OpenAIAccountSecret) {
		secret.FallbackEnabled = true
	})
	if canSessionAffinityPromoteOver(fallback, same, nil) {
		t.Fatal("fallback mismatch must not promote")
	}
	// Priority mismatch never promotes.
	bigger := testAccount("big", 9, nil)
	if canSessionAffinityPromoteOver(bigger, same, nil) {
		t.Fatal("priority mismatch must not promote")
	}
	// Model priority decides first.
	priority := &GatewayAccountModelPriority{RankByAccountID: map[string]int{
		"direct": ModelPriorityRankDirect, "mapping": ModelPriorityRankMapping,
	}}
	direct := testAccount("direct", 1, nil)
	mapping := testAccount("mapping", 1, nil)
	if !canSessionAffinityPromoteOver(direct, mapping, priority) {
		t.Fatal("better model rank must promote")
	}
	if canSessionAffinityPromoteOver(mapping, direct, priority) {
		t.Fatal("worse model rank must not promote")
	}
	// Same-tier rotation works both ways (both peers share the direct rank).
	sameTier := &GatewayAccountModelPriority{RankByAccountID: map[string]int{
		"direct": ModelPriorityRankDirect, "direct-2": ModelPriorityRankDirect,
	}}
	if !canSessionAffinityRotateWithinSameTier(direct, testAccount("direct-2", 1, nil), sameTier) {
		t.Fatal("same model rank with identical knobs must rotate")
	}
}

func TestW9CHasPrimarySoftAvailableAndCompare(t *testing.T) {
	policy := DefaultHighConcurrencyGroupSchedulingPolicy(SchedulingDefaults{GlobalMax: 100})
	healthy := highConcurrencyCandidate{
		account: testAccount("primary", 1, nil), softLimit: 100, hardLimit: 100,
	}
	fallbackBusy := highConcurrencyCandidate{
		account:  testAccount("fb", 1, func(secret *gatewayruntimecache.OpenAIAccountSecret) { secret.FallbackEnabled = true }),
		softBusy: true, softLimit: 100, hardLimit: 100,
	}
	if !hasPrimarySoftAvailable([]highConcurrencyCandidate{fallbackBusy, healthy}) {
		t.Fatal("healthy non-fallback candidate marks primary soft available")
	}
	if hasPrimarySoftAvailable([]highConcurrencyCandidate{fallbackBusy}) {
		t.Fatal("fallback soft-busy only must not mark primary available")
	}
	// compare: hard busy loses; traffic migration preference wins.
	busy := healthy
	busy.hardBusy = true
	if compareHighConcurrencyCandidates(busy, healthy, &policy, true, nil) <= 0 {
		t.Fatal("hard busy must lose")
	}
	preferred := healthy
	preferred.trafficMigrationPreferred = true
	if compareHighConcurrencyCandidates(preferred, healthy, &policy, true, nil) >= 0 {
		t.Fatal("traffic migration preference must win")
	}
}

func TestW9COrderOpenAIHighConcurrencyAccountsAsyncArms(t *testing.T) {
	svc, _, _ := newTestAffinityService(t, nil)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{testAccount("a", 1, nil), testAccount("b", 2, nil)}
	// Memory runtime driver delegates to the sync ordering.
	ordered, err := svc.orderOpenAIHighConcurrencyAccountsAsync(context.Background(), accounts, "", nil, "", nil, nil)
	if err != nil || len(ordered) != 2 {
		t.Fatalf("async ordering = %v, %v", accountsIDs(ordered), err)
	}
	// Short list passes through even under the redis runtime driver.
	redisSvc, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = w9cFailingRedis{}
	})
	short, err := redisSvc.orderOpenAIHighConcurrencyAccountsAsync(context.Background(), accounts[:1], "k", nil, "", nil, nil)
	if err != nil || len(short) != 1 {
		t.Fatalf("short async = %v, %v", accountsIDs(short), err)
	}
	// Invalid scheduling policy surfaces the error.
	if _, err := redisSvc.orderOpenAIHighConcurrencyAccounts(accounts, "k", map[string]any{"mode": "bad"}, "", nil); err == nil {
		t.Fatal("invalid policy must error")
	}
	// Fast-first disabled with a traffic migration target uses hard-busy-last.
	ordered, err = svc.orderOpenAIHighConcurrencyAccounts(accounts, "k", map[string]any{"fastFirstEnabled": false}, "b", nil)
	if err != nil || len(ordered) != 2 {
		t.Fatalf("hard busy last ordering = %v, %v", accountsIDs(ordered), err)
	}
	// OrderOpenAIAccountsBySessionAffinityAsync memory fallback path.
	ordered, err = svc.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "aff", DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("async personal ordering = %v, %v", accountsIDs(ordered), err)
	}
	// High concurrency group type routes through the candidate sorter.
	ordered, err = svc.OrderOpenAIAccountsBySessionAffinity(accounts, "aff", DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("sync high concurrency ordering = %v, %v", accountsIDs(ordered), err)
	}
	// Async personal ordering under the redis cache driver reads bindings via
	// the (failing) redis client and still returns a full ordering.
	ordered, err = redisSvc.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "aff", DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("redis async personal ordering = %v, %v", accountsIDs(ordered), err)
	}
}
