package gatewayruntimecache

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakePrewarmer 组合 fakeModels 并实现 GatewayAPIKeyPrewarmer：固定 hash 清单
// 与按 hash 的运行时快照，onRead 钩子用于在预热中段注入失效。
type fakePrewarmer struct {
	*fakeModels
	hashes    []string
	runtimes  map[string]GatewayRuntime
	errs      map[string]error
	onRead    func(hash string)
	listCalls int
	readCalls []string
}

func (f *fakePrewarmer) ListActiveGatewayAPIKeyHashes(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	f.listCalls++
	f.mu.Unlock()
	return f.hashes, nil
}

func (f *fakePrewarmer) ReadGatewayRuntimeByKeyHash(ctx context.Context, keyHash string) (GatewayRuntime, error) {
	f.mu.Lock()
	f.readCalls = append(f.readCalls, keyHash)
	hook := f.onRead
	runtime, ok := f.runtimes[keyHash]
	readErr := f.errs[keyHash]
	f.mu.Unlock()
	if hook != nil {
		hook(keyHash)
	}
	if readErr != nil {
		return GatewayRuntime{}, readErr
	}
	if !ok {
		return GatewayRuntime{Settings: f.settings, Accounts: []OpenAIAccountSecret{}}, nil
	}
	return runtime, nil
}

func TestPrewarmGatewayAPIKeyValidationCachePopulates(t *testing.T) {
	models := newFakeModels()
	rawKey := "sk-prewarm-warmable"
	warmable := testAPIKeyRow("ak-1", RouteStrategyModeNormal, "grp-1")
	fake := &fakePrewarmer{
		fakeModels: models,
		hashes:     []string{HashSecret(rawKey), "hash-invalid"},
		runtimes: map[string]GatewayRuntime{
			HashSecret(rawKey): {APIKey: warmable, Settings: models.settings},
			"hash-invalid":     {APIKey: nil, Settings: models.settings},
		},
	}
	clock := newManualClock()
	svc := newTestService(t, fake, clock, nil)
	ctx := context.Background()

	warmed, err := svc.PrewarmGatewayAPIKeyValidationCache(ctx)
	if err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	// 无绑定的 Key（APIKey == nil）不预热（Node `group_bindings.length` 分支）。
	if warmed != 1 {
		t.Fatalf("warmed = %d, want 1", warmed)
	}
	if len(fake.readCalls) != 2 {
		t.Fatalf("both hashes must be attempted: %v", fake.readCalls)
	}

	// 预热条目与自然读条目同形：后续 ReadCachedGatewayRuntimeAsync 直接命中，
	// 不触发 ReadGatewayRuntime 惰性加载。
	runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, rawKey)
	if err != nil {
		t.Fatalf("post-prewarm read: %v", err)
	}
	if runtime.APIKey == nil || runtime.APIKey.ID != "ak-1" {
		t.Fatalf("prewarmed runtime identity: %+v", runtime.APIKey)
	}
	if got := models.runtimeCallCount(); got != 0 {
		t.Fatalf("prewarmed read must not hit the loader, loader calls = %d", got)
	}

	// 预热计数 + 未命中 Key 走惰性加载。
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-never-warmed"); err != nil {
		t.Fatalf("lazy read: %v", err)
	}
	if got := models.runtimeCallCount(); got != 1 {
		t.Fatalf("unwarmed key must lazy-load once, loader calls = %d", got)
	}
}

func TestPrewarmStopsOnGenerationInvalidation(t *testing.T) {
	models := newFakeModels()
	svcRef := newTestService(t, &fakePrewarmer{fakeModels: models}, newManualClock(), nil)
	// 重建：需要持有 Service 引用的 onRead 钩子，先组装再注入。
	svcRef.Close()
	fake := &fakePrewarmer{
		fakeModels: models,
		hashes:     []string{"hash-a", "hash-b"},
		runtimes: map[string]GatewayRuntime{
			"hash-a": {APIKey: testAPIKeyRow("ak-a", RouteStrategyModeNormal, "grp-a"), Settings: models.settings},
			"hash-b": {APIKey: testAPIKeyRow("ak-b", RouteStrategyModeNormal, "grp-b"), Settings: models.settings},
		},
	}
	svc := newTestService(t, fake, newManualClock(), nil)
	fake.onRead = func(hash string) {
		// 预热中段落地一次代次失效（Node isGatewayApiKeyValidationGenerationCurrent
		// 中止分支）。
		svc.InvalidateGatewayRuntimeCacheByAPIKeyID("ak-other", nil)
	}

	warmed, err := svc.PrewarmGatewayAPIKeyValidationCache(context.Background())
	if err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	if warmed != 0 {
		t.Fatalf("stale generation must abort with nothing warmed, got %d", warmed)
	}
	if len(fake.readCalls) != 1 {
		t.Fatalf("the loop must stop at the first stale read: %v", fake.readCalls)
	}
}

func TestPrewarmReturnsLoaderError(t *testing.T) {
	models := newFakeModels()
	fake := &fakePrewarmer{
		fakeModels: models,
		hashes:     []string{"hash-err"},
		errs:       map[string]error{"hash-err": errors.New("db down")},
	}
	svc := newTestService(t, fake, newManualClock(), nil)
	warmed, err := svc.PrewarmGatewayAPIKeyValidationCache(context.Background())
	if err == nil || err.Error() != "db down" {
		t.Fatalf("loader error must surface: %v", err)
	}
	if warmed != 0 {
		t.Fatalf("no key warms on error, got %d", warmed)
	}
}

func TestPrewarmNoPrewarmerDegradesToNoop(t *testing.T) {
	models := newFakeModels()
	svc := newTestService(t, models, newManualClock(), nil)
	warmed, err := svc.PrewarmGatewayAPIKeyValidationCache(context.Background())
	if err != nil || warmed != 0 {
		t.Fatalf("models without the prewarm seam must no-op: %d %v", warmed, err)
	}
}

// TestPrewarmConcurrentWithInvalidationNoRace 驱动预热与失效并发（-race 门）：
// 预热读 generation 必须走锁内读取（currentAPIKeyRuntimeGeneration），
// 与 ClearGatewayRuntimeCacheLocal 的代次递增并发时不产生数据竞争；
// 结束后预热条目要么完整要么被失效代次拒绝（无半份条目）。
func TestPrewarmConcurrentWithInvalidationNoRace(t *testing.T) {
	models := newFakeModels()
	hashes := make([]string, 0, 64)
	for index := 0; index < 64; index++ {
		hashes = append(hashes, "hash-race-"+strings.Repeat("x", index%8)+"-"+itoaPrewarm(index))
	}
	fake := &fakePrewarmer{fakeModels: models, hashes: hashes}
	svc := newTestService(t, fake, newManualClock(), nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 200; index++ {
			svc.ClearGatewayRuntimeCacheLocal(ClearOptions{})
		}
	}()
	warmed, err := svc.PrewarmGatewayAPIKeyValidationCache(context.Background())
	<-done
	if err != nil {
		t.Fatalf("prewarm under invalidation: %v", err)
	}
	if warmed < 0 || warmed > len(hashes) {
		t.Fatalf("warmed out of range: %d", warmed)
	}
}

func itoaPrewarm(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
