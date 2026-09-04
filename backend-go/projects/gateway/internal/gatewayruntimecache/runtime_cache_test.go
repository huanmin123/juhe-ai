package gatewayruntimecache

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// ---------------------------------------------------------------------------
// test doubles: manual clock + strict mock ReadModels
// ---------------------------------------------------------------------------

type manualClock struct {
	mu sync.Mutex
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeModels struct {
	mu sync.Mutex

	settingsCalls int
	settings      GatewaySettings
	settingsErr   error

	runtimeCalls int
	runtimes     map[string]GatewayRuntime
	runtimeErr   error
	onRuntime    func(call int, key string)
	blockRuntime chan struct{}

	groupAccess    map[string]*GroupUsageAccessMetadata
	groupCalls     map[string]int
	groupAccessErr error

	accounts    map[string]OpenAIAccountsForGroupResult
	accountErr  error

	policies    map[string][]ResponseInspectionPolicySummary
	policyCalls map[string]int

	catalog     map[string][]ProviderModelCatalogItem
	catalogCalls map[string]int
	catalogErr  error

	concurrency map[string]int
}

func newFakeModels() *fakeModels {
	return &fakeModels{
		runtimes:     map[string]GatewayRuntime{},
		groupCalls:   map[string]int{},
		groupAccess:  map[string]*GroupUsageAccessMetadata{},
		accounts:     map[string]OpenAIAccountsForGroupResult{},
		policies:     map[string][]ResponseInspectionPolicySummary{},
		policyCalls:  map[string]int{},
		catalog:      map[string][]ProviderModelCatalogItem{},
		catalogCalls: map[string]int{},
		concurrency:  map[string]int{},
		settings: GatewaySettings{
			GatewayTextRawBodyLimitMegabytes:          8,
			AccountCircuitConfirmationFailuresRequired: 3,
			UsageStatsTimezone:                        "UTC",
			DefaultTemporaryUnschedulableMinutes:      10,
			StreamCircuitBreakerEnabled:               true,
			TextFirstResponseTimeoutSeconds:           60,
			TextStreamIdleTimeoutSeconds:              30,
			TextUncommittedAttemptMaxLifetimeSeconds:  300,
			ImageFirstResponseTimeoutSeconds:          60,
			ImageStreamIdleTimeoutSeconds:             30,
			ImageUncommittedAttemptMaxLifetimeSeconds: 300,
			ImageRequestWallTimeoutSeconds:            600,
			NoAvailableAccountWaitTimeoutSeconds:      30,
			StreamFailureThresholdCount:               3,
			StreamFailureThresholdWindowMinutes:       5,
		},
	}
}

func (f *fakeModels) ReadGatewaySettings(ctx context.Context) (GatewaySettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settingsCalls++
	if f.settingsErr != nil {
		return GatewaySettings{}, f.settingsErr
	}
	return f.settings, nil
}

func (f *fakeModels) ReadGatewayRuntime(ctx context.Context, key string) (GatewayRuntime, error) {
	f.mu.Lock()
	f.runtimeCalls++
	call := f.runtimeCalls
	hook := f.onRuntime
	block := f.blockRuntime
	runtime, ok := f.runtimes[key]
	runtimeErr := f.runtimeErr
	f.mu.Unlock()
	if hook != nil {
		hook(call, key)
	}
	if block != nil {
		<-block
	}
	if runtimeErr != nil {
		return GatewayRuntime{}, runtimeErr
	}
	if !ok {
		return GatewayRuntime{Settings: f.settings, Accounts: []OpenAIAccountSecret{}}, nil
	}
	return runtime, nil
}

func (f *fakeModels) ResolveGroupUsageAccessMetadata(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupCalls[groupID+":"+systemAccountID]++
	if f.groupAccessErr != nil {
		return nil, f.groupAccessErr
	}
	value := f.groupAccess[groupID+":"+systemAccountID]
	return cloneGroupUsageAccessPtr(value), nil
}

func (f *fakeModels) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.accountErr != nil {
		return OpenAIAccountsForGroupResult{}, f.accountErr
	}
	result, ok := f.accounts[groupID]
	if !ok {
		return OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{}}, nil
	}
	return result, nil
}

func (f *fakeModels) ListActiveResponseInspectionPolicies(ctx context.Context, protocolCode string, providerCode string) ([]ResponseInspectionPolicySummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := protocolCode + ":" + providerCode
	f.policyCalls[key]++
	return CloneResponseInspectionPolicies(f.policies[key]), nil
}

func (f *fakeModels) ListProviderModelCatalog(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalogCalls[input.ProviderCode]++
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	return CloneProviderModelCatalogItems(f.catalog[input.ProviderCode]), nil
}

func (f *fakeModels) LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, id := range accountIDs {
		out[id] = f.concurrency[id]
	}
	return out, nil
}

func (f *fakeModels) runtimeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runtimeCalls
}

// newTestService builds a service with the manual clock.
func newTestService(t *testing.T, models ReadModels, clock *manualClock, mutate func(*Options)) *Service {
	t.Helper()
	opts := Options{Clock: clock}
	if mutate != nil {
		mutate(&opts)
	}
	service, err := New(models, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func testAPIKeyRow(id string, mode string, groupIDs ...string) *GatewayAPIKeyRow {
	row := &GatewayAPIKeyRow{
		ID:               id,
		SystemAccountID:  "sys_owner",
		RouteStrategyID:  "rs_" + id,
		RouteStrategyMode: mode,
		Status:           "active",
	}
	for _, groupID := range groupIDs {
		row.GroupBindings = append(row.GroupBindings, GatewayAPIKeyGroupBindingRow{
			ID: id + "-b-" + groupID, APIKeyID: id, SystemAccountID: "sys_owner",
			GroupID: groupID, Priority: len(row.GroupBindings) + 1, Weight: 1,
			Status: "active", ProviderCode: "gpt", GroupEnabled: 1,
		})
	}
	if len(row.GroupBindings) > 0 {
		row.SelectedGroupID = row.GroupBindings[0].GroupID
	}
	return row
}

func testAccount(id string, groupOwner string) OpenAIAccountSecret {
	return OpenAIAccountSecret{
		ID: id, ProviderCode: "gpt", ProviderProtocolProfileID: "pp1",
		ProtocolCode: "openai", ProtocolVersion: "v1",
		SystemAccountID: groupOwner, AccountOwnerSystemAccountID: groupOwner, GroupOwnerSystemAccountID: groupOwner,
		AccountAccessType: AccountAccessTypeOwner, GroupAccessType: GroupAccessTypeOwner,
		Name: "acc-" + id, Type: "api_key", Status: AccountStatusActive,
		ConcurrencyLimit: 5, Priority: 1,
		ClientCompatibility: "openai_standard", HealthCheckEndpointMode: "chat_json",
		BaseURL: "https://upstream.test/v1", APIKey: "upstream-secret",
		Credentials:            map[string]any{"apiKey": "upstream-secret"},
		SupportedModels:        []string{"gpt-test"},
		StreamFailureCount:     0,
	}
}

func staticRuntime(t *testing.T, models *fakeModels, apiKey *GatewayAPIKeyRow, groupAccess *GroupUsageAccessMetadata, accounts []OpenAIAccountSecret) GatewayRuntime {
	t.Helper()
	return GatewayRuntime{
		APIKey:      apiKey,
		Settings:    models.settings,
		GroupAccess: groupAccess,
		Accounts:    accounts,
	}
}

// ---------------------------------------------------------------------------
// settings cache
// ---------------------------------------------------------------------------

func TestSettingsFirstLoadThenHitThenReloadAfterClear(t *testing.T) {
	models := newFakeModels()
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	first, err := svc.ReadCachedGatewaySettings(ctx)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if models.settingsCalls != 1 {
		t.Fatalf("first load must call the loader once, got %d", models.settingsCalls)
	}
	second, err := svc.ReadCachedGatewaySettings(ctx)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if models.settingsCalls != 1 {
		t.Fatalf("cached read must not hit the loader, got %d calls", models.settingsCalls)
	}
	if first != second {
		t.Fatalf("cached settings must be identical: %+v vs %+v", first, second)
	}

	// 失效后重载
	svc.ClearGatewayRuntimeCache("settings_updated")
	if _, err := svc.ReadCachedGatewaySettings(ctx); err != nil {
		t.Fatalf("post-clear read: %v", err)
	}
	if models.settingsCalls != 2 {
		t.Fatalf("post-clear read must reload, got %d calls", models.settingsCalls)
	}
}

// ---------------------------------------------------------------------------
// group usage access: 首载 / 命中 / 空值缓存 / stale-while-revalidate
// ---------------------------------------------------------------------------

func TestGroupAccessNegativeCachingSync(t *testing.T) {
	models := newFakeModels()
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		value, err := svc.ResolveCachedGroupUsageAccessMetadata(ctx, "g_missing", "sys")
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if value != nil {
			t.Fatalf("read %d must be undefined", i)
		}
	}
	if models.groupCalls["g_missing:sys"] != 1 {
		t.Fatalf("negative entry must be cached, loader calls = %d", models.groupCalls["g_missing:sys"])
	}
}

func TestGroupAccessAsyncMatchesStandaloneSyncSemantics(t *testing.T) {
	// Node：非 db-service / 非 local-postgres 传输下，async 读取直接落入同步
	// 路径（cacheDriver != redis 时本地缓存为事实源）。stale-while-revalidate
	// 属于共享缓存（redis）模式，见 TestSharedModeGroupAccessStaleWhileRevalidate。
	models := newFakeModels()
	models.groupAccess["g1:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	if _, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatalf("first async read: %v", err)
	}
	if calls := models.groupCalls["g1:sys"]; calls != 1 {
		t.Fatalf("first async read loads once, got %d", calls)
	}
	clock.Advance(61 * time.Second)
	// standalone 语义：本地条目在 retain 窗口内直接命中（revalidate 窗口不驱动同步路径）。
	if _, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatalf("post-window read: %v", err)
	}
	if calls := models.groupCalls["g1:sys"]; calls != 1 {
		t.Fatalf("standalone async read must hit the local entry, got %d calls", calls)
	}
}

// fakeSharedCache / fakeSharedFactory 是 Redis 共享缓存的内存替身。
type fakeSharedFactory struct {
	mu    sync.Mutex
	store map[string]map[string][]byte
}

func newFakeSharedFactory() *fakeSharedFactory {
	return &fakeSharedFactory{store: map[string]map[string][]byte{}}
}

func (f *fakeSharedFactory) Cache(name string) SharedCache {
	return &fakeSharedCache{factory: f, name: name}
}

type fakeSharedCache struct {
	factory *fakeSharedFactory
	name    string
}

func (c *fakeSharedCache) bucketLocked() map[string][]byte {
	f := c.factory
	if f.store[c.name] == nil {
		f.store[c.name] = map[string][]byte{}
	}
	return f.store[c.name]
}

func (c *fakeSharedCache) Get(ctx context.Context, key string, dst any) (bool, error) {
	c.factory.mu.Lock()
	raw := append([]byte(nil), c.bucketLocked()[key]...)
	c.factory.mu.Unlock()
	if raw == nil {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

func (c *fakeSharedCache) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.factory.mu.Lock()
	c.bucketLocked()[key] = encoded
	c.factory.mu.Unlock()
	return nil
}

func (c *fakeSharedCache) Clear(ctx context.Context) error {
	c.factory.mu.Lock()
	delete(c.factory.store, c.name)
	c.factory.mu.Unlock()
	return nil
}

func TestSharedModeGroupAccessStaleWhileRevalidate(t *testing.T) {
	models := newFakeModels()
	models.groupAccess["g1:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	svc := newTestService(t, models, clock, func(o *Options) { o.Shared = shared })
	ctx := context.Background()

	if _, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatalf("shared-mode first read: %v", err)
	}
	if calls := models.groupCalls["g1:sys"]; calls != 1 {
		t.Fatalf("shared-mode first read loads once, got %d", calls)
	}
	// 共享命中（新进程同语义）：不触发 loader。
	svc2, err := New(models, Options{Clock: clock, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	if _, err := svc2.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatalf("second process read: %v", err)
	}
	if calls := models.groupCalls["g1:sys"]; calls != 1 {
		t.Fatalf("shared entry must satisfy the cold process, got %d calls", calls)
	}
	// 超过 revalidate 窗口：stale serve + 后台刷新。
	clock.Advance(61 * time.Second)
	stale, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys")
	if err != nil {
		t.Fatalf("stale read: %v", err)
	}
	if stale == nil {
		t.Fatal("stale value must be served as last-good")
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatalf("await: %v", err)
	}
	if calls := models.groupCalls["g1:sys"]; calls != 2 {
		t.Fatalf("background refresh must reload once, got %d calls", calls)
	}
}

// ---------------------------------------------------------------------------
// runtime: 首载 / 命中 / 失效后重载 / 快照一致性 / 异常路径不缓存毒化
// ---------------------------------------------------------------------------

func TestRuntimeFirstLoadFreshHitAndStaleRefresh(t *testing.T) {
	models := newFakeModels()
	models.runtimes["sk-test"] = staticRuntime(t, models, testAPIKeyRow("key_1", "normal", "g1"),
		&GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner},
		[]OpenAIAccountSecret{testAccount("a1", "sys_owner")})
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-test")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if runtime.APIKey == nil || runtime.APIKey.ID != "key_1" {
		t.Fatalf("first read must return the loaded key, got %+v", runtime.APIKey)
	}
	if len(runtime.Accounts) != 1 {
		t.Fatalf("dispatch clone must keep usable accounts, got %d", len(runtime.Accounts))
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("first read must load once, got %d", models.runtimeCallCount())
	}

	// 命中：fresh window 内不触发 loader。
	again, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-test")
	if err != nil {
		t.Fatalf("fresh read: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("fresh read must hit, got %d calls", models.runtimeCallCount())
	}
	if again.APIKey.ID != "key_1" {
		t.Fatalf("fresh read key mismatch")
	}

	// 快照一致性：改写返回值不得污染缓存。
	again.Accounts[0].Name = "mutated"
	again.Settings.GatewayTextRawBodyLimitMegabytes = 999
	third, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-test")
	if err != nil {
		t.Fatalf("post-mutation read: %v", err)
	}
	if third.Accounts[0].Name == "mutated" {
		t.Fatal("returned snapshot must be a clone; mutation leaked into the cache")
	}
	if third.Settings.GatewayTextRawBodyLimitMegabytes == 999 {
		t.Fatal("settings mutation leaked into the cache")
	}

	// 失效后重载：超过 60s revalidate 窗口 → 旧值 + 后台刷新。
	clock.Advance(61 * time.Second)
	stale, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-test")
	if err != nil {
		t.Fatalf("stale read: %v", err)
	}
	if stale.APIKey == nil || stale.APIKey.ID != "key_1" {
		t.Fatal("stale read must serve the last-good snapshot")
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatalf("await background: %v", err)
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("background refresh must reload once, got %d calls", models.runtimeCallCount())
	}
}

func TestRuntimeLoadFailureDoesNotPoisonCache(t *testing.T) {
	models := newFakeModels()
	models.runtimeErr = errors.New("db down")
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-broken"); err == nil {
		t.Fatal("loader failure must surface")
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("one attempt recorded, got %d", models.runtimeCallCount())
	}
	// 恢复后立即成功：失败结果未被缓存。
	models.mu.Lock()
	models.runtimeErr = nil
	models.runtimes["sk-broken"] = staticRuntime(t, models, testAPIKeyRow("key_ok", "normal", "g1"), nil, nil)
	models.mu.Unlock()
	runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-broken")
	if err != nil {
		t.Fatalf("post-recovery read: %v", err)
	}
	if runtime.APIKey == nil || runtime.APIKey.ID != "key_ok" {
		t.Fatalf("post-recovery runtime mismatch: %+v", runtime.APIKey)
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("failure must not be cached, got %d calls", models.runtimeCallCount())
	}
}

func TestRuntimeSingleflightConcurrentLoads(t *testing.T) {
	models := newFakeModels()
	models.runtimes["sk-hot"] = staticRuntime(t, models, testAPIKeyRow("key_hot", "normal", "g1"), nil, nil)
	blocked := make(chan struct{})
	models.blockRuntime = blocked
	var entered atomic.Int32
	models.onRuntime = func(call int, key string) {
		entered.Add(1)
	}
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	const readers = 8
	var wg sync.WaitGroup
	results := make([]GatewayRuntime, readers)
	errs := make([]error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			results[slot], errs[slot] = svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-hot")
		}(i)
	}
	// 等待第一个加载进入 loader。
	waitFor(t, 2*time.Second, func() bool { return entered.Load() >= 1 })
	close(blocked)
	wg.Wait()
	if entered.Load() != 1 {
		t.Fatalf("singleflight must collapse concurrent loads, loader entered %d times", entered.Load())
	}
	for i := 0; i < readers; i++ {
		if errs[i] != nil {
			t.Fatalf("reader %d: %v", i, errs[i])
		}
		if results[i].APIKey == nil || results[i].APIKey.ID != "key_hot" {
			t.Fatalf("reader %d got runtime %+v", i, results[i].APIKey)
		}
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("loader must run once, ran %d", models.runtimeCallCount())
	}
}

func TestRuntimeGenerationRetryAndExhaustion(t *testing.T) {
	t.Run("invalidation during load retries once", func(t *testing.T) {
		models := newFakeModels()
		models.runtimes["sk-gen"] = staticRuntime(t, models, testAPIKeyRow("key_gen", "normal", "g1"), nil, nil)
		clock := newManualClock()
		svc := newTestService(t, models, clock, nil)
		ctx := context.Background()
		models.onRuntime = func(call int, key string) {
			if call == 1 {
				svc.InvalidateGatewayRuntimeCacheByAPIKeyID("key_gen", nil)
			}
		}
		runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-gen")
		if err != nil {
			t.Fatalf("read after retry: %v", err)
		}
		if runtime.APIKey == nil || runtime.APIKey.ID != "key_gen" {
			t.Fatalf("unexpected runtime %+v", runtime.APIKey)
		}
		if models.runtimeCallCount() != 2 {
			t.Fatalf("stale-generation load must retry, got %d calls", models.runtimeCallCount())
		}
	})

	t.Run("continuous invalidation exhausts attempts", func(t *testing.T) {
		models := newFakeModels()
		models.runtimes["sk-loop"] = staticRuntime(t, models, testAPIKeyRow("key_loop", "normal", "g1"), nil, nil)
		clock := newManualClock()
		svc := newTestService(t, models, clock, nil)
		ctx := context.Background()
		models.onRuntime = func(call int, key string) {
			svc.InvalidateGatewayRuntimeCacheByAPIKeyID("key_loop", nil)
		}
		_, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-loop")
		if err == nil {
			t.Fatal("expected exhaustion error")
		}
		if !strings.Contains(err.Error(), "连续失效") {
			t.Fatalf("exhaustion error mismatch: %v", err)
		}
		if models.runtimeCallCount() != gatewayRuntimeLoadAttemptLimit {
			t.Fatalf("must stop after %d attempts, got %d", gatewayRuntimeLoadAttemptLimit, models.runtimeCallCount())
		}
	})
}

func TestRuntimeTargetedInvalidationByAPIKeyID(t *testing.T) {
	models := newFakeModels()
	models.runtimes["sk-one"] = staticRuntime(t, models, testAPIKeyRow("key_one", "normal", "g1"), nil, nil)
	models.runtimes["sk-two"] = staticRuntime(t, models, testAPIKeyRow("key_two", "normal", "g1"), nil, nil)
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	for _, key := range []string{"sk-one", "sk-two"} {
		if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, key); err != nil {
			t.Fatalf("warm %s: %v", key, err)
		}
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("warm-up must load twice, got %d", models.runtimeCallCount())
	}
	svc.InvalidateGatewayRuntimeCacheByAPIKeyID("key_one", nil)
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-one"); err != nil {
		t.Fatalf("re-read sk-one: %v", err)
	}
	if models.runtimeCallCount() != 3 {
		t.Fatalf("targeted invalidation must drop key_one only, got %d calls", models.runtimeCallCount())
	}
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-two"); err != nil {
		t.Fatalf("re-read sk-two: %v", err)
	}
	if models.runtimeCallCount() != 3 {
		t.Fatalf("key_two must stay cached, got %d calls", models.runtimeCallCount())
	}
}

func TestRuntimeInvalidKeyNegativeCacheWindow(t *testing.T) {
	models := newFakeModels()
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-unknown")
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if runtime.APIKey != nil || len(runtime.Accounts) != 0 {
			t.Fatalf("unknown key must return the empty runtime, got %+v", runtime)
		}
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("invalid runtime must be negatively cached, got %d calls", models.runtimeCallCount())
	}
	// 10s 后负缓存过期：stale 读返回旧值并触发后台重载。
	clock.Advance(11 * time.Second)
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-unknown"); err != nil {
		t.Fatalf("post-window read: %v", err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatalf("await background: %v", err)
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("invalid runtime revalidate window must reload, got %d calls", models.runtimeCallCount())
	}
}

// ---------------------------------------------------------------------------
// inval bus wiring
// ---------------------------------------------------------------------------

func TestBusRuntimeTopicInvalidationClearsCaches(t *testing.T) {
	models := newFakeModels()
	models.runtimes["sk-bus"] = staticRuntime(t, models, testAPIKeyRow("key_bus", "normal", "g1"), nil, nil)
	clock := newManualClock()
	bus := inval.New(clock.Now)
	svc := newTestService(t, models, clock, func(o *Options) { o.Bus = bus })
	ctx := context.Background()

	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-bus"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	bus.Invalidate(inval.TopicGatewayRuntime, "group_updated")
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-bus"); err != nil {
		t.Fatalf("post-invalidate: %v", err)
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("bus invalidation must clear the runtime cache, got %d calls", models.runtimeCallCount())
	}
}

func TestBusAPIKeyValidationTopicScopedInvalidation(t *testing.T) {
	models := newFakeModels()
	models.runtimes["sk-scope1"] = staticRuntime(t, models, testAPIKeyRow("key_scope1", "normal", "g1"), nil, nil)
	models.runtimes["sk-scope2"] = staticRuntime(t, models, testAPIKeyRow("key_scope2", "normal", "g1"), nil, nil)
	clock := newManualClock()
	bus := inval.New(clock.Now)
	svc := newTestService(t, models, clock, func(o *Options) { o.Bus = bus })
	ctx := context.Background()

	for _, key := range []string{"sk-scope1", "sk-scope2"} {
		if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, key); err != nil {
			t.Fatalf("warm %s: %v", key, err)
		}
	}
	// K5 BusInvalidator reason 形如 "<reason> <apiKeyID>"。
	bus.Invalidate(inval.TopicGatewayAPIKeyValidation, "api_key_secret_refreshed key_scope1")
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-scope1"); err != nil {
		t.Fatalf("re-read scoped key: %v", err)
	}
	if models.runtimeCallCount() != 3 {
		t.Fatalf("scoped invalidation must reload key_scope1, got %d calls", models.runtimeCallCount())
	}
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-scope2"); err != nil {
		t.Fatalf("re-read untouched key: %v", err)
	}
	if models.runtimeCallCount() != 3 {
		t.Fatalf("key_scope2 must survive the scoped invalidation, got %d calls", models.runtimeCallCount())
	}
}

func TestClearReasonDiscrimination(t *testing.T) {
	models := newFakeModels()
	models.groupAccess["g1:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	clock := newManualClock()
	clears := 0
	svc := newTestService(t, models, clock, func(o *Options) {
		o.ClearSettingsCache = func() { clears++ }
	})
	ctx := context.Background()

	if _, err := svc.ResolveCachedGroupUsageAccessMetadata(ctx, "g1", "sys"); err != nil {
		t.Fatalf("warm group access: %v", err)
	}
	// 非 settings 原因不清设置仓库缓存，但组缓存仍清空。
	svc.ClearGatewayRuntimeCache("group_updated")
	if clears != 0 {
		t.Fatalf("group_updated must not clear settings cache, got %d clears", clears)
	}
	if _, err := svc.ResolveCachedGroupUsageAccessMetadata(ctx, "g1", "sys"); err != nil {
		t.Fatalf("reload group access: %v", err)
	}
	if models.groupCalls["g1:sys"] != 2 {
		t.Fatalf("clear must drop group cache, got %d calls", models.groupCalls["g1:sys"])
	}
	// settings_updated 触发 ClearSettingsCache。
	svc.ClearGatewayRuntimeCache("settings_updated")
	if clears != 1 {
		t.Fatalf("settings_updated must clear settings cache once, got %d", clears)
	}
	// 模型目录原因触发 catalog 世代推进。
	models.catalog["gpt"] = []ProviderModelCatalogItem{{ProviderCode: "gpt", Model: "gpt-test", SupportedAPIProtocols: []string{"responses"}, SupportedTools: []string{}, InputModalities: []string{"text"}, OutputModalities: []string{"text"}, SupportedServiceTiers: []string{}, SupportedReasoningEfforts: []string{}, Source: "built_in", Scope: "built_in", Status: "active"}}
	if _, err := svc.ListCachedProviderModelCatalogAsync(ctx, ModelCatalogListOptions{ProviderCode: "gpt"}); err != nil {
		t.Fatalf("warm catalog: %v", err)
	}
	svc.ClearGatewayRuntimeCache("custom_provider_model_saved")
	if _, err := svc.ListCachedProviderModelCatalogAsync(ctx, ModelCatalogListOptions{ProviderCode: "gpt"}); err != nil {
		t.Fatalf("post-catalog-clear: %v", err)
	}
	if models.catalogCalls["gpt"] != 2 {
		t.Fatalf("model-catalog reason must invalidate the catalog cache, loader calls = %d", models.catalogCalls["gpt"])
	}
}

// ---------------------------------------------------------------------------
// dynamic route re-selection + concurrency overlay
// ---------------------------------------------------------------------------

func TestDynamicRouteReselectionUsesCachedGroupData(t *testing.T) {
	models := newFakeModels()
	models.groupAccess["g1:sys_owner"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	models.groupAccess["g2:sys_owner"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys_owner")}}
	models.accounts["g2"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a2", "sys_owner")}}
	models.concurrency["a2"] = 3
	// 运行时读取（skipDynamicRouteSelection）返回静态形态：无组访问、无账户。
	models.runtimes["sk-dyn"] = staticRuntime(t, models, testAPIKeyRow("key_dyn", RouteStrategyModeRoundRobin, "g1", "g2"), nil, nil)
	clock := newManualClock()
	svc := newTestService(t, models, clock, func(o *Options) {
		o.Orderer = orderedFakeOrderer{[][]string{{"g2", "g1"}}}
	})
	ctx := context.Background()

	runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-dyn")
	if err != nil {
		t.Fatalf("dynamic read: %v", err)
	}
	if runtime.APIKey == nil || runtime.APIKey.SelectedGroupID != "g2" {
		t.Fatalf("dynamic route must select g2, got %+v", runtime.APIKey)
	}
	if len(runtime.Accounts) != 1 || runtime.Accounts[0].ID != "a2" {
		t.Fatalf("dynamic route must carry g2 accounts, got %+v", runtime.Accounts)
	}
	if runtime.GroupAccess == nil {
		t.Fatal("dynamic route must carry the group access metadata")
	}
	if runtime.Accounts[0].CurrentConcurrency == nil || *runtime.Accounts[0].CurrentConcurrency != 3 {
		t.Fatalf("concurrency overlay must stamp 3, got %+v", runtime.Accounts[0].CurrentConcurrency)
	}
	if len(runtime.ResponseInspectionPolicies) != 0 {
		t.Fatal("dynamic rebuild resets response inspection policies")
	}
}

type orderedFakeOrderer struct{ orders [][]string }

func (o orderedFakeOrderer) OrderAPIKeyGroupBindings(ctx context.Context, apiKey GatewayAPIKeyRow) ([]GatewayAPIKeyGroupBindingRow, error) {
	order := o.orders[0]
	rank := map[string]int{}
	for i, groupID := range order {
		rank[groupID] = i
	}
	bindings := append([]GatewayAPIKeyGroupBindingRow(nil), apiKey.GroupBindings...)
	sort.SliceStable(bindings, func(i, j int) bool { return rank[bindings[i].GroupID] < rank[bindings[j].GroupID] })
	return bindings, nil
}

func TestDynamicRouteOrdererFailureFallsBackOrThrows(t *testing.T) {
	models := newFakeModels()
	// skipDynamic 快照没有 groupAccess / accounts → orderer 失败必须上抛。
	models.runtimes["sk-dyn-fail"] = staticRuntime(t, models, testAPIKeyRow("key_dyn_fail", RouteStrategyModeWeighted, "g1"), nil, nil)
	clock := newManualClock()
	svc := newTestService(t, models, clock, func(o *Options) {
		o.Orderer = failingOrderer{}
	})
	ctx := context.Background()
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-dyn-fail"); err == nil {
		t.Fatal("orderer failure without last-good data must throw")
	}
}

type failingOrderer struct{}

func (failingOrderer) OrderAPIKeyGroupBindings(ctx context.Context, apiKey GatewayAPIKeyRow) ([]GatewayAPIKeyGroupBindingRow, error) {
	return nil, errors.New("redis route state unavailable")
}

// ---------------------------------------------------------------------------
// recoverable unavailable accounts
// ---------------------------------------------------------------------------

func TestRecoverableUnavailableWindowFilter(t *testing.T) {
	models := newFakeModels()
	nowISO := "2026-09-01T08:00:30.000Z"
	inWindowActive := testAccount("a_cool", "sys")
	inWindowActive.CooldownUntil = &nowISO
	inWindowRateLimited := testAccount("a_rate", "sys")
	rateISO := "2026-09-01T08:00:10.000Z"
	inWindowRateLimited.CooldownUntil = &rateISO
	inWindowRateLimited.Status = AccountStatusRateLimited
	outOfWindow := testAccount("a_far", "sys")
	farISO := "2026-09-01T09:00:00.000Z"
	outOfWindow.CooldownUntil = &farISO
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{inWindowActive, inWindowRateLimited, outOfWindow}}
	clock := newManualClock() // now = 08:00:00Z, window default 30s
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	accounts, err := svc.ListRecoverableUnavailableOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}, nil)
	if err != nil {
		t.Fatalf("recoverable read: %v", err)
	}
	ids := map[string]bool{}
	for _, account := range accounts {
		ids[account.ID] = true
	}
	// Node 语义：active 且冷却截止落在窗口内（晚于 now、不晚于 now+window）的
	// 账号可恢复必须保留；rate_limited 同窗口保留；超出窗口丢弃。
	if !ids["a_cool"] || !ids["a_rate"] || ids["a_far"] {
		t.Fatalf("window filter mismatch: got %v", ids)
	}
}

// ---------------------------------------------------------------------------
// provider model catalog + route index
// ---------------------------------------------------------------------------

func catalogItem(provider, model string) ProviderModelCatalogItem {
	return ProviderModelCatalogItem{
		ProviderCode: provider, Model: model, Scope: "built_in", Status: "active",
		Source: "built_in", SupportedAPIProtocols: []string{"responses"},
		InputModalities: []string{"text"}, OutputModalities: []string{"text"},
		SupportedTools: []string{}, SupportedServiceTiers: []string{}, SupportedReasoningEfforts: []string{},
	}
}

func TestProviderModelRouteResolutionOutcomes(t *testing.T) {
	models := newFakeModels()
	models.catalog["gpt"] = []ProviderModelCatalogItem{catalogItem("gpt", "gpt-x"), catalogItem("gpt", "shared")}
	models.catalog["deepseek"] = []ProviderModelCatalogItem{catalogItem("deepseek", "shared")}
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	matched, err := svc.ResolveCachedProviderModelRouteAsync(ctx, " gpt-x ", []string{"gpt", "gpt"}, "", false)
	if err != nil {
		t.Fatalf("matched resolve: %v", err)
	}
	if matched.Outcome != ProviderModelRouteMatched || matched.ProviderCode != "gpt" {
		t.Fatalf("matched outcome mismatch: %+v", matched)
	}
	ambiguous, err := svc.ResolveCachedProviderModelRouteAsync(ctx, "shared", []string{"deepseek", "gpt"}, "", false)
	if err != nil {
		t.Fatalf("ambiguous resolve: %v", err)
	}
	if ambiguous.Outcome != ProviderModelRouteAmbiguous || len(ambiguous.MatchedProviderCodes) != 2 {
		t.Fatalf("ambiguous outcome mismatch: %+v", ambiguous)
	}
	missing, err := svc.ResolveCachedProviderModelRouteAsync(ctx, "unknown", []string{"gpt"}, "", false)
	if err != nil {
		t.Fatalf("missing resolve: %v", err)
	}
	if missing.Outcome != ProviderModelRouteMissing {
		t.Fatalf("missing outcome mismatch: %+v", missing)
	}
	empty, err := svc.ResolveCachedProviderModelRouteAsync(ctx, "gpt-x", nil, "", false)
	if err != nil {
		t.Fatalf("empty providers resolve: %v", err)
	}
	if empty.Outcome != ProviderModelRouteMissing || empty.ModelKey != "gpt-x" {
		t.Fatalf("empty providers must short-circuit to missing: %+v", empty)
	}
}

func TestCatalogSingleflight(t *testing.T) {
	models := newFakeModels()
	models.catalog["gpt"] = []ProviderModelCatalogItem{catalogItem("gpt", "gpt-x")}
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	const readers = 6
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.ListCachedProviderModelCatalogAsync(ctx, ModelCatalogListOptions{ProviderCode: "gpt"}); err != nil {
				t.Errorf("catalog read: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls := models.catalogCalls["gpt"]; calls != 1 {
		t.Fatalf("concurrent catalog reads must singleflight, loader calls = %d", calls)
	}
}

// ---------------------------------------------------------------------------
// inspection policies
// ---------------------------------------------------------------------------

func TestInspectionPoliciesLoadCacheAndForAccountsMerge(t *testing.T) {
	models := newFakeModels()
	models.policies["openai:"] = []ResponseInspectionPolicySummary{{
		ID: "p1", DefaultRule: false, Editable: true, Name: "stored", Enabled: true, Priority: 1,
		ScopeType: "protocol", ProtocolCode: "openai", Action: "observe",
		Match: ResponseInspectionPolicyMatch{JSONPathsExists: []string{"error"}},
	}}
	models.policies["openai:gpt"] = []ResponseInspectionPolicySummary{{
		ID: "p2", DefaultRule: false, Editable: true, Name: "provider", Enabled: true, Priority: 2,
		ScopeType: "provider", ProtocolCode: "openai", ProviderCode: strPtrIfSet("gpt"), Action: "observe",
		Match: ResponseInspectionPolicyMatch{ErrorCodes: []string{"cyber_policy"}},
	}}
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	first, err := svc.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "")
	if err != nil {
		t.Fatalf("first inspection read: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("stored policies only (defaults filtered by scope), got %d", len(first))
	}
	if models.policyCalls["openai:"] != 1 {
		t.Fatalf("first read must load once, got %d", models.policyCalls["openai:"])
	}
	// stale 窗口内命中。
	if _, err := svc.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", ""); err != nil {
		t.Fatalf("hit read: %v", err)
	}
	if models.policyCalls["openai:"] != 1 {
		t.Fatalf("hit read must not reload, got %d", models.policyCalls["openai:"])
	}
	// ForAccounts：账户作用域合并去重（acc1 协议层作用域、acc2 供应商层作用域）。
	acc1 := testAccount("a1", "sys")
	acc1.ProviderCode = ""
	acc2 := testAccount("a2", "sys")
	merged, err := svc.ListCachedActiveResponseInspectionPoliciesForAccountsAsync(ctx, []OpenAIAccountSecret{acc1, acc2, acc1})
	if err != nil {
		t.Fatalf("for-accounts read: %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("merged policies must dedupe by id, got %d", len(merged))
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not reached before timeout")
}
