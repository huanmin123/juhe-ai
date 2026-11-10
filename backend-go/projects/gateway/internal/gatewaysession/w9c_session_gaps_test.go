package gatewaysession

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
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
	if err != nil || got != "juhe-ai:{dev}:plain-key" {
		t.Fatalf("namespaced key = %s, %v", got, err)
	}
	got, err = RedisNamespacedKey("dev", "juhe-ai:other:key")
	if err != nil || got != "juhe-ai:{dev}:other:key" {
		t.Fatalf("root prefix rebind = %s, %v", got, err)
	}
	got, err = RedisNamespacedKey("dev", "juhe-ai:{dev}:keep")
	if err != nil || got != "juhe-ai:{dev}:keep" {
		t.Fatalf("already prefixed = %s, %v", got, err)
	}
	prefix, err := RedisNamespacePrefix("dev")
	if err != nil || prefix != "juhe-ai:{dev}:" {
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
	if _, err := lazy.client(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_URL") {
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
	policy, err := ResolveGroupSchedulingPolicy(&gatewayscheduling.Policy{DefaultSoftConcurrency: 5, ClientIPConcurrencyLimit: 2})
	if err != nil {
		t.Fatalf("policy = %v", err)
	}
	if policy.DefaultSoftConcurrency != 5 || policy.ClientIPConcurrencyLimit != 2 {
		t.Fatalf("policy = %+v", *policy)
	}
	defaults, err := ResolveGroupSchedulingPolicy(nil)
	if err != nil || defaults.DefaultSoftConcurrency != -1 || defaults.ClientIPConcurrencyLimit != 0 {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	enabled := gatewayscheduling.Policy{DefaultSoftConcurrency: 5}
	if got := enabled.SoftLimit(10); got != 5 {
		t.Fatalf("soft limit = %d", got)
	}
	if got := enabled.SoftLimit(3); got != 3 {
		t.Fatalf("soft limit clamp = %d", got)
	}
	disabled := gatewayscheduling.Policy{DefaultSoftConcurrency: -1}
	if got := disabled.SoftLimit(10); got != 0 {
		t.Fatalf("disabled soft limit = %d", got)
	}
	if !enabled.SoftBusy(5, 10) || enabled.SoftBusy(4, 10) || disabled.SoftBusy(9, 10) {
		t.Fatal("soft busy arms drift")
	}
}
