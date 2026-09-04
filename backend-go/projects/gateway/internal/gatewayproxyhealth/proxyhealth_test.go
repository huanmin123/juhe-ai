package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestGatewayUpstreamBucketKeys(t *testing.T) {
	proxyProfile := "profile-1"
	tests := []struct {
		name     string
		fixture  accountFixture
		scope    GatewayUpstreamBucketScope
		expected []string
	}{
		{
			name: "oauth openai pins the codex base url",
			fixture: accountFixture{
				id: "acc1", systemAccountID: "sys1", providerCode: "openai",
				protocolCode: "openai", protocolVersion: "v1", baseURL: "https://api.openai.com", accountType: "oauth",
			},
			expected: []string{
				"baseUrl:https://chatgpt.com/backend-api/codex:owner:sys1",
				"provider:openai:owner:sys1",
			},
		},
		{
			name: "proxy profile and upstream keys with owner scope",
			fixture: accountFixture{
				id: "acc1", systemAccountID: "sys1", ownerAccountID: "owner1", providerCode: "openai",
				proxyProfileID: &proxyProfile, baseURL: "https://API.Example.com:8443/sub/",
			},
			expected: []string{
				"proxy:profile:profile-1:owner:owner1",
				"baseUrl:https://api.example.com:8443/sub/v1:owner:owner1",
				"provider:openai:owner:owner1",
			},
		},
		{
			name: "proxy scope only returns the proxy key",
			fixture: accountFixture{
				id: "acc1", systemAccountID: "sys1", providerCode: "openai",
				proxyProfileID: &proxyProfile, baseURL: "https://api.example.com",
			},
			scope:    BucketScopeProxy,
			expected: []string{"proxy:profile:profile-1:owner:sys1"},
		},
		{
			name: "upstream scope drops the proxy key",
			fixture: accountFixture{
				id: "acc1", systemAccountID: "sys1", providerCode: "openai",
				proxyProfileID: &proxyProfile, baseURL: "https://api.example.com",
			},
			scope: BucketScopeUpstream,
			expected: []string{
				"baseUrl:https://api.example.com/v1:owner:sys1",
				"provider:openai:owner:sys1",
			},
		},
		{
			name: "owner scope falls back to account id",
			fixture: accountFixture{
				id: "acc1", providerCode: "openai", baseURL: "https://api.example.com",
			},
			expected: []string{
				"baseUrl:https://api.example.com/v1:owner:acc1",
				"provider:openai:owner:acc1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GatewayUpstreamBucketKeys(tt.fixture.build(), tt.scope)
			if len(got) != len(tt.expected) {
				t.Fatalf("keys = %v, want %v", got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Fatalf("keys[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestNormalizeOpenAIBaseURLForBucket(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "appends /v1", input: "https://api.example.com", expected: "https://api.example.com/v1"},
		{name: "keeps existing /v1", input: "https://api.example.com/v1", expected: "https://api.example.com/v1"},
		{name: "strips trailing slashes and query", input: "https://api.example.com/v1/?x=1", expected: "https://api.example.com/v1"},
		{name: "strips userinfo", input: "https://user:pass@api.example.com/v1", expected: "https://api.example.com/v1"},
		{name: "lowercases host and protocol only", input: "https://API.Example.COM/V1", expected: "https://api.example.com/V1/v1"},
		{name: "empty input", input: "   ", expected: ""},
		{name: "scheme-less falls back to lowercase text", input: "api.example.com/v1", expected: "api.example.com/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeOpenAIBaseURLForBucket(tt.input)
			if got != tt.expected {
				t.Fatalf("normalize(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGatewayProxyKey(t *testing.T) {
	profile := "p1"
	if key, ok := GatewayProxyKey(accountFixture{proxyProfileID: &profile}.build()); !ok || key != "proxy:profile:p1" {
		t.Fatalf("profile key = %q ok=%v", key, ok)
	}
	if key, ok := GatewayProxyKey(accountFixture{proxyURL: stringPtrValue("https://proxy:9")}.build()); !ok || !strings.HasPrefix(key, "proxy:url:") {
		t.Fatalf("url key = %q ok=%v", key, ok)
	}
	if _, ok := GatewayProxyKey(accountFixture{}.build()); ok {
		t.Fatal("no proxy must return ok=false")
	}
}

func TestOrderGatewayAccountsAvoidsSpecificBucket(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "openai", baseURL: "https://api.example.com"}.build()
	b := accountFixture{id: "b", systemAccountID: "sys", providerCode: "openai", baseURL: "https://api.example.com"}.build()

	result := service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	if result.Applied {
		t.Fatal("fresh accounts must not reorder")
	}

	decision := service.RecordGatewayUpstreamBucketFailure(a, "连接失败", FailureRecordOptions{})
	if !decision.Recorded || decision.Suspected {
		t.Fatalf("single account must not be suspected: %+v", decision)
	}
	if decision.DistinctAccountCount == nil || *decision.DistinctAccountCount != 1 {
		t.Fatalf("distinct count = %v", decision.DistinctAccountCount)
	}

	result = service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	if result.Applied {
		t.Fatal("one suspect alone must not reorder (second account is also in the bucket)")
	}

	// A distinct second failure crosses the threshold of 2 and避让 applies.
	service.RecordGatewayUpstreamBucketFailure(b, "连接失败", FailureRecordOptions{})
	c := accountFixture{id: "c", systemAccountID: "sys", providerCode: "other", baseURL: "https://other.example.com"}.build()
	result = service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{a, b, c}, nil)
	if !result.Applied {
		t.Fatalf("suspected bucket must reorder: %+v", result)
	}
	if result.Accounts[0].ID != "c" {
		t.Fatalf("fresh account must come first, got %s", result.Accounts[0].ID)
	}
	if len(result.AvoidedAccountIDs) != 2 || len(result.AvoidedBucketKeys) == 0 {
		t.Fatalf("avoided ids/keys = %v / %v", result.AvoidedAccountIDs, result.AvoidedBucketKeys)
	}
}

func TestProxyHealthDistinctThresholdBoundary(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	// Same account twice: evidence id dedupes, count stays 1, still below threshold 2.
	service.RecordGatewayUpstreamBucketFailure(a, "err", FailureRecordOptions{})
	service.RecordGatewayUpstreamBucketFailure(a, "err", FailureRecordOptions{})
	if entry, ok := service.getMemoryEntry("baseUrl:https://x.example.com/v1:owner:sys"); ok && entry.AvoidUntilMs != nil {
		t.Fatal("below-threshold failures must not open avoidance")
	}
	// credentialSourceAccountId overrides the evidence identity.
	b := accountFixture{id: "b", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com", credentialSourceID: stringPtrValue("root-a")}.build()
	decision := service.RecordGatewayUpstreamBucketFailure(b, "err", FailureRecordOptions{})
	if !decision.Suspected {
		t.Fatalf("two distinct evidence ids must open avoidance: %+v", decision)
	}
}

func TestProxyHealthHalfOpenLifecycle(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	// Shared baseUrl, distinct provider buckets: after the avoidance TTL only
	// one account can claim the half-open probe on the shared bucket.
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	b := accountFixture{id: "b", systemAccountID: "sys", providerCode: "q", baseURL: "https://x.example.com"}.build()
	service.RecordGatewayUpstreamBucketFailure(a, "err", FailureRecordOptions{})
	service.RecordGatewayUpstreamBucketFailure(b, "err", FailureRecordOptions{})

	// Expire the avoidance window.
	clock.Advance(61_000)
	result := service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	if !result.Applied {
		t.Fatalf("probe winner must reorder: %+v", result)
	}
	if len(result.HalfOpenAccountIDs) != 1 {
		t.Fatalf("half-open ids = %v", result.HalfOpenAccountIDs)
	}
	probe := result.HalfOpenAccountIDs[0]
	loser := "b"
	if probe == "b" {
		loser = "a"
	}
	if result.Accounts[0].ID != probe {
		t.Fatalf("probe account must be first, got %v", idsFixture(result.Accounts))
	}
	_ = loser

	// A probe failure while half-open re-opens the bucket immediately.
	clock.Advance(1_000)
	probeAccount := a
	if probe == "b" {
		probeAccount = b
	}
	decision := service.RecordGatewayUpstreamBucketFailure(probeAccount, "半开探测失败", FailureRecordOptions{})
	if !decision.Suspected {
		t.Fatalf("half-open probe failure must be suspected: %+v", decision)
	}
	entry, ok := service.getMemoryEntry("baseUrl:https://x.example.com/v1:owner:sys")
	if !ok || entry.AvoidUntilMs == nil || *entry.AvoidUntilMs <= clock.NowMs() {
		t.Fatal("probe failure must re-open avoidance")
	}

	// Success clears the entries; proxy-scope success only touches proxy keys.
	if cleared := service.RecordGatewayProxySuccess(probeAccount); cleared {
		t.Fatal("proxy success with proxy-scope filter must not clear upstream bucket")
	}
	if !service.RecordGatewayUpstreamBucketSuccess(probeAccount, FailureRecordOptions{}) {
		t.Fatal("success must report the previously existing entry")
	}
	if _, exists := service.getMemoryEntry("baseUrl:https://x.example.com/v1:owner:sys"); exists {
		t.Fatal("success must clear the bucket entry")
	}
}

func TestProxyHealthSharedBucketHalfOpenSingleProbe(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	shared := accountFixture{systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}
	a := shared.build()
	a.ID = "a"
	b := shared.build()
	b.ID = "b"
	service.RecordGatewayUpstreamBucketFailure(a, "err", FailureRecordOptions{})
	service.RecordGatewayUpstreamBucketFailure(b, "err", FailureRecordOptions{})
	clock.Advance(61_000)

	result := service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	// One shared bucket: exactly one account wins the probe lease and is
	// ordered with the fresh group; the other stays avoided.
	if !result.Applied {
		t.Fatalf("half-open winner must reorder: %+v", result)
	}
	if len(result.HalfOpenAccountIDs) != 1 {
		t.Fatalf("half-open ids = %v", result.HalfOpenAccountIDs)
	}
	if result.Accounts[0].ID != result.HalfOpenAccountIDs[0] {
		t.Fatalf("probe account must be first, got %v / %v", idsFixture(result.Accounts), result.HalfOpenAccountIDs)
	}
}

func idsFixture(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	output := make([]string, 0, len(accounts))
	for _, account := range accounts {
		output = append(output, account.ID)
	}
	return output
}

func TestSuppressGatewayUpstreamBucketLocallyForSeconds(t *testing.T) {
	clock := newFakeClock(1_000_000)
	log := &recordingLog{}
	store := NewMemoryRuntimeStateStore(clock.Now)
	service := NewProxyHealthService(clock.Now, store, ProxyHealthOptions{}, log.record)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", proxyURL: stringPtrValue("http://user:pass@proxy:9")}.build()

	decision := service.SuppressGatewayUpstreamBucketLocallyForSeconds(a, 30, "维护避让", FailureRecordOptions{})
	if !decision.Recorded || !decision.Suspected || decision.DistinctAccountCount == nil || *decision.DistinctAccountCount != 1 {
		t.Fatalf("suppress decision = %+v", decision)
	}
	if decision.ProxyKey == nil || !strings.HasPrefix(*decision.ProxyKey, "proxy:url:") {
		t.Fatalf("proxyKey = %v", decision.ProxyKey)
	}
	entry, ok := service.getMemoryEntry(*decisionProxyKeyOf(decision))
	if !ok || entry.AvoidUntilMs == nil || *entry.AvoidUntilMs != clock.NowMs()+30_000 {
		t.Fatalf("avoidUntil = %v", entry.AvoidUntilMs)
	}
	if log.count() != 1 || log.events()[0] != "gateway_upstream_bucket_locally_suppressed" {
		t.Fatalf("suppress log = %v", log.events())
	}
}

func decisionProxyKeyOf(decision GatewayProxyFailureDecision) *string {
	return decision.ProxyKey
}

func TestProxyHealthRedisDriverPaths(t *testing.T) {
	clock := newFakeClock(1_000_000)
	store := NewMemoryRuntimeStateStore(clock.Now)
	service := NewProxyHealthService(clock.Now, store, ProxyHealthOptions{CASMaxAttempts: 4}, nil)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	b := accountFixture{id: "b", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()

	if decision, err := service.RecordGatewayUpstreamBucketFailureAsync(contextBackground(), a, "err", FailureRecordOptions{}); err != nil || decision.Suspected {
		t.Fatalf("async single failure = %+v err=%v", decision, err)
	}
	decision, err := service.RecordGatewayUpstreamBucketFailureAsync(contextBackground(), b, "err", FailureRecordOptions{})
	if err != nil || !decision.Suspected {
		t.Fatalf("async threshold failure = %+v err=%v", decision, err)
	}
	if _, ok := store.entries[redisBucketStateKey("baseUrl:https://x.example.com/v1:owner:sys")]; !ok {
		t.Fatal("redis driver must persist under the bucket: prefix key")
	}

	clock.Advance(61_000)
	result, err := service.OrderGatewayAccountsByUpstreamBucketHealthAsync(contextBackground(), []gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	if err != nil {
		t.Fatalf("async order error: %v", err)
	}
	// One shared bucket, one probe winner.
	if !result.Applied || len(result.HalfOpenAccountIDs) != 1 {
		t.Fatalf("async order = %+v", result)
	}
	winner := result.HalfOpenAccountIDs[0]
	winnerAccount := a
	if winner == "b" {
		winnerAccount = b
	}
	cleared, err := service.RecordGatewayUpstreamBucketSuccessAsync(contextBackground(), winnerAccount, FailureRecordOptions{})
	if err != nil || !cleared {
		t.Fatalf("async success cleared=%v err=%v", cleared, err)
	}
	if _, exists := store.entries[redisBucketStateKey("baseUrl:https://x.example.com/v1:owner:sys")]; exists {
		t.Fatal("winner success must clear the shared bucket entry")
	}
}

// flakyRuntimeStateStore deterministically fails the first N compare-set
// calls to drive the CAS retry loop to exhaustion.
type flakyRuntimeStateStore struct {
	*MemoryRuntimeStateStore
	mu        sync.Mutex
	failFirst int
}

func (s *flakyRuntimeStateStore) CompareSetJSON(ctx context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error) {
	s.mu.Lock()
	fail := s.failFirst > 0
	if fail {
		s.failFirst--
	}
	s.mu.Unlock()
	if fail {
		return false, nil
	}
	return s.MemoryRuntimeStateStore.CompareSetJSON(ctx, key, expected, next, ttlMs)
}

func TestProxyHealthCASExhausted(t *testing.T) {
	clock := newFakeClock(1_000_000)
	store := &flakyRuntimeStateStore{MemoryRuntimeStateStore: NewMemoryRuntimeStateStore(clock.Now)}
	service := NewProxyHealthService(clock.Now, store, ProxyHealthOptions{CASMaxAttempts: 2}, nil)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	b := accountFixture{id: "b", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()

	// Open the bucket first (flaky disabled), then arm the failure counter so
	// the half-open claim burns through its two attempts deterministically.
	service.RecordGatewayUpstreamBucketFailureAsync(contextBackground(), a, "err", FailureRecordOptions{})
	service.RecordGatewayUpstreamBucketFailureAsync(contextBackground(), b, "err", FailureRecordOptions{})
	clock.Advance(61_000)
	store.mu.Lock()
	store.failFirst = 8
	store.mu.Unlock()

	_, err := service.OrderGatewayAccountsByUpstreamBucketHealthAsync(contextBackground(), []gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	if err == nil || !strings.Contains(err.Error(), "上游桶 Redis CAS 重试耗尽（2 次）") {
		t.Fatalf("CAS exhaustion error = %v", err)
	}
}

func TestProxyHealthConcurrentFailureRecording(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.RecordGatewayUpstreamBucketFailure(a, "err", FailureRecordOptions{})
			service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{a}, nil)
		}()
	}
	wg.Wait()
}

func TestProxyHealthPriorityTiersPreserved(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	shared := accountFixture{systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}
	low := shared.build()
	low.ID = "low"
	low.Priority = 1
	high := shared.build()
	high.ID = "high"
	high.Priority = 9
	service.RecordGatewayUpstreamBucketFailure(low, "err", FailureRecordOptions{})
	service.RecordGatewayUpstreamBucketFailure(high, "err", FailureRecordOptions{})
	clock.Advance(61_000)

	ranks := map[string]int{"high": 0, "low": 1}
	result := service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{low, high}, &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: ranks})
	if !result.Applied {
		t.Fatalf("expected applied order: %+v", result)
	}
	// Base order [low, high] defines the tier sequence; the reorder must not
	// invert it even though the probe winner is high.
	if result.Accounts[0].ID != "low" || result.Accounts[1].ID != "high" {
		t.Fatalf("base priority tier order must survive, got %v", idsFixture(result.Accounts))
	}
}
