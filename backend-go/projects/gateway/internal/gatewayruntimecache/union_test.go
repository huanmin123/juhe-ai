package gatewayruntimecache

// 分组模型并集缓存测试（权威契约：docs/functions/网关模型列表账户并集设计.md
// §6.5 第一行）。全部复用包内既有测试基建：manualClock / newTestService /
// fakeSharedFactory / whWarnLogger，另加可控的 union loader 替身与清理 spy。

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// test doubles
// ---------------------------------------------------------------------------

// fakeUnionLoader 是并集装载端口的可控替身：按调用次序返回 results；err 非
// nil 时恒失败；block 非 nil 时每次调用阻塞直至关闭（装载用独立有界 context，
// 等待者取消不传播）。
type fakeUnionLoader struct {
	mu      sync.Mutex
	calls   []GroupModelUnionListOptions
	results []GroupModelUnionEntry
	err     error
	block   chan struct{}
}

func (f *fakeUnionLoader) ListGroupModelUnion(ctx context.Context, input GroupModelUnionListOptions) (GroupModelUnionEntry, error) {
	f.mu.Lock()
	call := len(f.calls)
	f.calls = append(f.calls, GroupModelUnionListOptions{
		CallerSystemAccountID: input.CallerSystemAccountID,
		GroupIDs:              append([]string(nil), input.GroupIDs...),
	})
	block, err := f.block, f.err
	var result GroupModelUnionEntry
	if call < len(f.results) {
		result = f.results[call].Clone()
	}
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return GroupModelUnionEntry{}, ctx.Err()
		}
	}
	if err != nil {
		return GroupModelUnionEntry{}, err
	}
	return result, nil
}

func (f *fakeUnionLoader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// unionEntryAt 以手动时钟构造 validUntil = now + ttl 的装载结果。
func unionEntryAt(clock *manualClock, ttl time.Duration, models ...string) GroupModelUnionEntry {
	return GroupModelUnionEntry{Models: models, ValidUntil: clock.Now().Add(ttl)}
}

// waitUntil 轮询条件直至限时（后台装载/清理 goroutine 的确定性等待点）。
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func unionLoggerHasEvent(logger *whWarnLogger, event string) bool {
	for _, logged := range logger.snapshot() {
		if logged == event {
			return true
		}
	}
	return false
}

// unionSharedBucketOf 返回 svc 在指定代的共享桶名。
func unionSharedBucketOf(svc *Service, generation uint64) string {
	return groupModelUnionCacheName + ":" + svc.processStartToken + ":" + strconv.FormatUint(generation, 10)
}

func unionSharedBucketSize(shared *fakeSharedFactory, bucket string) int {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	return len(shared.store[bucket])
}

// unionCleanupSpyFactory 记录并按序通知 Clear 的桶名（旧代回收时序断言），
// failClear 时注入清理失败。
type unionCleanupSpyFactory struct {
	inner     SharedCacheFactory
	failClear bool
	notify    chan string
}

func newUnionCleanupSpyFactory(inner SharedCacheFactory, failClear bool) *unionCleanupSpyFactory {
	return &unionCleanupSpyFactory{inner: inner, failClear: failClear, notify: make(chan string, 16)}
}

func (f *unionCleanupSpyFactory) Cache(name string) SharedCache {
	return &unionCleanupSpyShared{factory: f, name: name, inner: f.inner.Cache(name)}
}

type unionCleanupSpyShared struct {
	factory *unionCleanupSpyFactory
	name    string
	inner   SharedCache
}

func (c *unionCleanupSpyShared) Get(ctx context.Context, key string, dst any) (bool, error) {
	return c.inner.Get(ctx, key, dst)
}

func (c *unionCleanupSpyShared) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	return c.inner.Set(ctx, key, value, ttl)
}

func (c *unionCleanupSpyShared) Clear(ctx context.Context) error {
	var err error
	if c.factory.failClear {
		err = errors.New("union 注入共享清理失败")
	} else {
		err = c.inner.Clear(ctx)
	}
	// 只通知 union 桶（工厂同时创建 settings 等其他共享桶，它们的清理与本测
	// 试无关）。
	if strings.HasPrefix(c.name, groupModelUnionCacheName+":") {
		c.factory.notify <- c.name
	}
	return err
}

// unionReadErrFactory 的共享缓存恒读取失败（读降级臂）。
type unionReadErrFactory struct{}

func (f unionReadErrFactory) Cache(string) SharedCache { return &whReadErrShared{} }

// ---------------------------------------------------------------------------
// 键归一与克隆
// ---------------------------------------------------------------------------

func TestUnionCacheKeyNormalizationAndClone(t *testing.T) {
	ids := normalizeGroupModelUnionGroupIDs([]string{"g3", "g1", "g2", "g1"})
	if strings.Join(ids, ",") != "g1,g2,g3" {
		t.Fatalf("去重排序 = %v", ids)
	}
	key1 := groupModelUnionCacheKey("c1", normalizeGroupModelUnionGroupIDs([]string{"g1", "g2"}))
	if key1 != groupModelUnionCacheKey("c1", normalizeGroupModelUnionGroupIDs([]string{"g2", "g1"})) {
		t.Fatal("分组集顺序不得影响业务键")
	}
	if key1 == groupModelUnionCacheKey("c2", []string{"g1", "g2"}) {
		t.Fatal("调用方必须参与业务键")
	}
	if key1 == groupModelUnionCacheKey("c1", []string{"g1", "g3"}) {
		t.Fatal("分组集不同必须不同键")
	}
	// Clone 深拷贝。
	clock := newManualClock()
	entry := unionEntryAt(clock, time.Hour, "m1", "m2")
	cloned := entry.Clone()
	cloned.Models[0] = "mutated"
	if entry.Models[0] != "m1" {
		t.Fatal("Clone 必须深拷贝 Models")
	}
	var nilEntry GroupModelUnionEntry
	if nilEntry.Clone().Models != nil {
		t.Fatal("nil Models 克隆必须保持 nil")
	}
}

// ---------------------------------------------------------------------------
// memory 模式：首载/命中/克隆隔离/调用方与分组集键隔离
// ---------------------------------------------------------------------------

func TestUnionMemoryModeLoadHitMissAndKeyIsolation(t *testing.T) {
	clock := newManualClock()
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{
		unionEntryAt(clock, time.Hour, "m1", "m2"),
		unionEntryAt(clock, time.Hour, "m3"),
		unionEntryAt(clock, time.Hour, "m4"),
	}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	ctx := context.Background()

	first, err := svc.ListCachedGroupModelUnionAsync(ctx, GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g2", "g1"}})
	if err != nil || strings.Join(first.Models, ",") != "m1,m2" {
		t.Fatalf("首读 = %+v err=%v", first, err)
	}
	if loader.callCount() != 1 {
		t.Fatalf("首读 loader = %d", loader.callCount())
	}
	// 返回克隆：改动返回值不得毒化缓存。
	first.Models[0] = "mutated"
	second, err := svc.ListCachedGroupModelUnionAsync(ctx, GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1", "g2"}})
	if err != nil || strings.Join(second.Models, ",") != "m1,m2" {
		t.Fatalf("命中读 = %+v err=%v", second, err)
	}
	if loader.callCount() != 1 {
		t.Fatalf("命中读不得触发 loader = %d", loader.callCount())
	}
	// 调用方隔离。
	other, err := svc.ListCachedGroupModelUnionAsync(ctx, GroupModelUnionListOptions{CallerSystemAccountID: "c2", GroupIDs: []string{"g1", "g2"}})
	if err != nil || strings.Join(other.Models, ",") != "m3" {
		t.Fatalf("跨调用方读 = %+v err=%v", other, err)
	}
	if loader.callCount() != 2 {
		t.Fatalf("跨调用方必须分别装载 = %d", loader.callCount())
	}
	// 分组集隔离。
	otherSet, err := svc.ListCachedGroupModelUnionAsync(ctx, GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1", "g3"}})
	if err != nil || strings.Join(otherSet.Models, ",") != "m4" {
		t.Fatalf("跨分组集读 = %+v err=%v", otherSet, err)
	}
	if loader.callCount() != 3 {
		t.Fatalf("跨分组集必须分别装载 = %d", loader.callCount())
	}
	// 顺序不敏感：同键再次命中。
	again, err := svc.ListCachedGroupModelUnionAsync(ctx, GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g2", "g1"}})
	if err != nil || strings.Join(again.Models, ",") != "m1,m2" {
		t.Fatalf("同键再读 = %+v err=%v", again, err)
	}
	if loader.callCount() != 3 {
		t.Fatalf("同键不得重复装载 = %d", loader.callCount())
	}
}

// nil loader：读取返回明确错误，不 panic。
func TestUnionNilLoaderReturnsError(t *testing.T) {
	svc := newTestService(t, newFakeModels(), newManualClock(), nil)
	_, err := svc.ListCachedGroupModelUnionAsync(context.Background(), GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}})
	if !errors.Is(err, ErrGroupModelUnionLoaderUnavailable) {
		t.Fatalf("nil loader 必须返回哨兵错误: %v", err)
	}
}

// ---------------------------------------------------------------------------
// singleflight：同代并发未命中只装载一次；等待者取消不取消共享装载
// ---------------------------------------------------------------------------

func TestUnionSingleflightMergesConcurrentMisses(t *testing.T) {
	clock := newManualClock()
	loader := &fakeUnionLoader{
		results: []GroupModelUnionEntry{unionEntryAt(clock, time.Hour, "m1")},
		block:   make(chan struct{}),
	}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}

	const n = 8
	entries := make([]GroupModelUnionEntry, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entries[i], errs[i] = svc.ListCachedGroupModelUnionAsync(context.Background(), input)
		}(i)
	}
	waitUntil(t, func() bool { return loader.callCount() == 1 && svc.pendingUnionLoadCount() == 1 })
	close(loader.block)
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil || strings.Join(entries[i].Models, ",") != "m1" {
			t.Fatalf("等待者 %d = %+v err=%v", i, entries[i], errs[i])
		}
	}
	if loader.callCount() != 1 {
		t.Fatalf("同代并发未命中必须只装载一次 = %d", loader.callCount())
	}
}

// 单个等待者取消只退出自己：返回原始取消错误，共享装载继续并服务其他等待者。
func TestUnionWaiterCancelDoesNotAbortSharedLoad(t *testing.T) {
	clock := newManualClock()
	loader := &fakeUnionLoader{
		results: []GroupModelUnionEntry{unionEntryAt(clock, time.Hour, "m1")},
		block:   make(chan struct{}),
	}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	results := make(chan GroupModelUnionEntry, 1)
	readErrs := make(chan error, 1)
	go func() {
		entry, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
		results <- entry
		readErrs <- err
	}()
	waitUntil(t, func() bool { return svc.pendingUnionLoadCount() == 1 })

	waiterCtx, cancel := context.WithCancel(ctx)
	cancel()
	canceled := make(chan error, 1)
	go func() {
		_, err := svc.ListCachedGroupModelUnionAsync(waiterCtx, input)
		canceled <- err
	}()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消等待者必须返回原始取消错误: %v", err)
	}
	// 共享装载未受影响：解除阻塞后首个读取者拿到结果。
	close(loader.block)
	if err := <-readErrs; err != nil {
		t.Fatalf("共享装载不得被等待者取消: %v", err)
	}
	entry := <-results
	if strings.Join(entry.Models, ",") != "m1" {
		t.Fatalf("共享装载结果 = %+v", entry)
	}
	if loader.callCount() != 1 {
		t.Fatalf("loader = %d", loader.callCount())
	}
}

// ---------------------------------------------------------------------------
// GET/旧 loader 与失效交错：旧结果不返回、不回填，触发新代重算
// ---------------------------------------------------------------------------

func TestUnionStaleLoadDiscardedAfterInvalidation(t *testing.T) {
	clock := newManualClock()
	loader := &fakeUnionLoader{
		results: []GroupModelUnionEntry{
			unionEntryAt(clock, time.Hour, "m1-old"),
			unionEntryAt(clock, time.Hour, "m1-new"),
		},
		block: make(chan struct{}),
	}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	cacheKey := groupModelUnionCacheKey("c1", []string{"g1"})
	ctx := context.Background()

	type readResult struct {
		entry GroupModelUnionEntry
		err   error
	}
	results := make(chan readResult, 1)
	go func() {
		entry, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
		results <- readResult{entry, err}
	}()
	waitUntil(t, func() bool { return loader.callCount() == 1 && svc.pendingUnionLoadCount() == 1 })

	// 失效介入：代号推进 + pending 清空；随后旧装载完成。
	svc.ClearGatewayRuntimeCache("account_deleted")
	close(loader.block)

	res := <-results
	if res.err != nil {
		t.Fatalf("重算后必须成功: %v", res.err)
	}
	if strings.Join(res.entry.Models, ",") != "m1-new" {
		t.Fatalf("旧代结果不得返回等待者: %+v", res.entry)
	}
	if loader.callCount() != 2 {
		t.Fatalf("旧代作废后必须重算, loader = %d", loader.callCount())
	}
	// 新代结果已回填本地：再次读取不触发装载。
	again, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
	if err != nil || strings.Join(again.Models, ",") != "m1-new" {
		t.Fatalf("再读 = %+v err=%v", again, err)
	}
	if loader.callCount() != 2 {
		t.Fatalf("新代命中不得触发装载 = %d", loader.callCount())
	}
	_, generation := svc.captureUnionVersion()
	local, ok := svc.getUnionLocalEntry(generation, cacheKey)
	if !ok || strings.Join(local.Models, ",") != "m1-new" {
		t.Fatalf("本地必须只回填新代数据 = %+v ok=%v", local, ok)
	}
}

// ---------------------------------------------------------------------------
// 失效：任意 reason 推进 generation 并强制重算
// ---------------------------------------------------------------------------

func TestUnionAnyReasonClearAdvancesGeneration(t *testing.T) {
	clock := newManualClock()
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{
		unionEntryAt(clock, time.Hour, "m1"),
		unionEntryAt(clock, time.Hour, "m2"),
		unionEntryAt(clock, time.Hour, "m3"),
		unionEntryAt(clock, time.Hour, "m4"),
	}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	svc.ClearGatewayRuntimeCache("account_deleted")
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	svc.ClearGatewayRuntimeCache("")
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	svc.ClearGatewayRuntimeCacheLocal(ClearOptions{ClearModelCatalog: true})
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	if loader.callCount() != 4 {
		t.Fatalf("每次失效后必须重算, loader = %d", loader.callCount())
	}
	_, generation := svc.captureUnionVersion()
	if generation != 3 {
		t.Fatalf("generation = %d, want 3", generation)
	}
}

// ---------------------------------------------------------------------------
// redis 模式：共享优先读 + 命中回填本地
// ---------------------------------------------------------------------------

func TestUnionSharedPriorityReadAndLocalBackfill(t *testing.T) {
	clock := newManualClock()
	shared := newFakeSharedFactory()
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{unionEntryAt(clock, time.Hour, "m1", "m2")}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) {
		o.Shared = shared
		o.Union = loader
	})
	input := GroupModelUnionListOptions{CallerSystemAccountID: "caller", GroupIDs: []string{"g1", "g2"}}
	cacheKey := groupModelUnionCacheKey("caller", []string{"g1", "g2"})
	ctx := context.Background()

	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	if loader.callCount() != 1 {
		t.Fatalf("首读 loader = %d", loader.callCount())
	}
	// 覆写共享桶为另一份新数据：共享优先于本地（本地仍是首载值）。
	bucket := unionSharedBucketOf(svc, 0)
	overridden, err := json.Marshal(sharedGroupModelUnionEntry{Models: []string{"m-shared"}, ValidUntil: clock.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	shared.mu.Lock()
	shared.store[bucket] = map[string][]byte{cacheKey: overridden}
	shared.mu.Unlock()
	sharedHit, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
	if err != nil || strings.Join(sharedHit.Models, ",") != "m-shared" {
		t.Fatalf("共享命中必须优先于本地 = %+v err=%v", sharedHit, err)
	}
	if loader.callCount() != 1 {
		t.Fatalf("共享命中不得触发 loader = %d", loader.callCount())
	}
	// 共享命中已回填本地：清空共享桶后再读仍命中。
	shared.mu.Lock()
	delete(shared.store, bucket)
	shared.mu.Unlock()
	localHit, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
	if err != nil || strings.Join(localHit.Models, ",") != "m-shared" {
		t.Fatalf("回填本地必须可命中 = %+v err=%v", localHit, err)
	}
	if loader.callCount() != 1 {
		t.Fatalf("回填命中不得触发 loader = %d", loader.callCount())
	}
}

// 重启（新启动 token）后旧 Redis 键不可达：必须重新装载。
func TestUnionRestartTokenIsolatesOldSharedKeys(t *testing.T) {
	clock := newManualClock()
	shared := newFakeSharedFactory()
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{
		unionEntryAt(clock, time.Hour, "m1-old"),
		unionEntryAt(clock, time.Hour, "m1-new"),
	}}
	models := newFakeModels()
	svc1 := newTestService(t, models, clock, func(o *Options) {
		o.Shared = shared
		o.Union = loader
	})
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	first, err := svc1.ListCachedGroupModelUnionAsync(ctx, input)
	if err != nil || strings.Join(first.Models, ",") != "m1-old" {
		t.Fatalf("首进程读 = %+v err=%v", first, err)
	}
	// “重启”：新进程同工厂共享缓存，但启动 token 不同 → 不同桶名。
	svc2, err := New(models, Options{Clock: clock, Shared: shared, Union: loader})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	if svc2.processStartToken == svc1.processStartToken {
		t.Fatal("重启后的启动 token 必须不同")
	}
	second, err := svc2.ListCachedGroupModelUnionAsync(ctx, input)
	if err != nil || strings.Join(second.Models, ",") != "m1-new" {
		t.Fatalf("重启后不得命中旧键 = %+v err=%v", second, err)
	}
	if loader.callCount() != 2 {
		t.Fatalf("重启后必须重新装载 = %d", loader.callCount())
	}
	// 旧进程的桶仍持有数据（TTL 前残留属预期），但不在新进程读取路径上。
	if unionSharedBucketSize(shared, unionSharedBucketOf(svc1, 0)) != 1 {
		t.Fatal("旧代桶数据应保留至回收")
	}
}

// ---------------------------------------------------------------------------
// 共享桶回收：只删旧代桶、不删当前代；清理失败不破坏正确性
// ---------------------------------------------------------------------------

func TestUnionCleanupTargetsOnlyOldGenerationBuckets(t *testing.T) {
	clock := newManualClock()
	shared := newFakeSharedFactory()
	spy := newUnionCleanupSpyFactory(shared, false)
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{
		unionEntryAt(clock, time.Hour, "m1"),
		unionEntryAt(clock, time.Hour, "m2"),
		unionEntryAt(clock, time.Hour, "m3"),
	}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) {
		o.Shared = spy
		o.Union = loader
	})
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	// 第一次失效：回收 :0 桶，:1 不受影响。
	svc.ClearGatewayRuntimeCache("account_deleted")
	if cleared := <-spy.notify; cleared != unionSharedBucketOf(svc, 0) {
		t.Fatalf("清理目标 = %q", cleared)
	}
	if size := unionSharedBucketSize(shared, unionSharedBucketOf(svc, 0)); size != 0 {
		t.Fatalf("旧代桶必须被回收 = %d", size)
	}
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	if size := unionSharedBucketSize(shared, unionSharedBucketOf(svc, 1)); size != 1 {
		t.Fatalf("当前代桶必须持有数据 = %d", size)
	}
	// 第二次失效：回收 :1，随后的 :2 在回收时点尚不存在、不被波及。
	svc.ClearGatewayRuntimeCache("group_updated")
	if cleared := <-spy.notify; cleared != unionSharedBucketOf(svc, 1) {
		t.Fatalf("清理目标 = %q", cleared)
	}
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	if size := unionSharedBucketSize(shared, unionSharedBucketOf(svc, 2)); size != 1 {
		t.Fatalf("新代桶必须存活 = %d", size)
	}
	if loader.callCount() != 3 {
		t.Fatalf("loader = %d", loader.callCount())
	}
}

// 清理（DEL）失败：记 WARN、不影响正确性——版本键隔离使下一次读取仍重算新集合。
func TestUnionCleanupFailureWarnsAndVersionKeyStillIsolates(t *testing.T) {
	clock := newManualClock()
	shared := newFakeSharedFactory()
	spy := newUnionCleanupSpyFactory(shared, true)
	logger := &whWarnLogger{}
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{
		unionEntryAt(clock, time.Hour, "m1-old"),
		unionEntryAt(clock, time.Hour, "m1-new"),
	}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) {
		o.Shared = spy
		o.Union = loader
		o.Logger = logger
	})
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	svc.ClearGatewayRuntimeCache("account_deleted")
	if cleared := <-spy.notify; cleared != unionSharedBucketOf(svc, 0) {
		t.Fatalf("清理目标 = %q", cleared)
	}
	waitUntil(t, func() bool {
		return unionLoggerHasEvent(logger, "gateway_group_model_union_shared_cache_cleanup_failed")
	})
	// 清理失败后 GET 恢复：版本键隔离使旧数据不可达，重算新集合。
	fresh, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
	if err != nil || strings.Join(fresh.Models, ",") != "m1-new" {
		t.Fatalf("清理失败后必须重算新集合 = %+v err=%v", fresh, err)
	}
	if loader.callCount() != 2 {
		t.Fatalf("loader = %d", loader.callCount())
	}
}

// ---------------------------------------------------------------------------
// TTL 与有效期
// ---------------------------------------------------------------------------

// 本地 TTL 按剩余有效期设置：ValidUntil 临近时 TTL < 1h，过期即重算。
func TestUnionTTLFollowsRemainingValidity(t *testing.T) {
	clock := newManualClock()
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{
		unionEntryAt(clock, 10*time.Minute, "m1"),
		unionEntryAt(clock, time.Hour, "m1"),
	}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	clock.Advance(9 * time.Minute)
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	if loader.callCount() != 1 {
		t.Fatalf("剩余有效期内必须命中 = %d", loader.callCount())
	}
	// 越过 ValidUntil（未到 1h 基础 TTL）：本地条目必须已过期。
	clock.Advance(2 * time.Minute)
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	if loader.callCount() != 2 {
		t.Fatalf("越过 ValidUntil 必须重算 = %d", loader.callCount())
	}
}

// loader 返回已到期结果：不发布（本地与共享都不写），按未命中重算。
func TestUnionExpiredAtPublishRecomputes(t *testing.T) {
	clock := newManualClock()
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{
		unionEntryAt(clock, -time.Second, "stale"),
		unionEntryAt(clock, time.Hour, "fresh"),
	}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	entry, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
	if err != nil || strings.Join(entry.Models, ",") != "fresh" {
		t.Fatalf("到期结果必须重算 = %+v err=%v", entry, err)
	}
	if loader.callCount() != 2 {
		t.Fatalf("loader = %d", loader.callCount())
	}
	// 有效结果已发布：再次读取命中。
	if _, err := svc.ListCachedGroupModelUnionAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	if loader.callCount() != 2 {
		t.Fatalf("有效发布必须可命中 = %d", loader.callCount())
	}
}

// ---------------------------------------------------------------------------
// 缓存面故障降级与 loader 错误语义
// ---------------------------------------------------------------------------

// Redis 读失败按未命中降级，请求成功并记 WARN。
func TestUnionSharedReadFailureDegradesToMiss(t *testing.T) {
	clock := newManualClock()
	logger := &whWarnLogger{}
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{unionEntryAt(clock, time.Hour, "m1")}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) {
		o.Shared = unionReadErrFactory{}
		o.Union = loader
		o.Logger = logger
	})
	entry, err := svc.ListCachedGroupModelUnionAsync(context.Background(), GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}})
	if err != nil || strings.Join(entry.Models, ",") != "m1" {
		t.Fatalf("读失败必须降级未命中 = %+v err=%v", entry, err)
	}
	if loader.callCount() != 1 {
		t.Fatalf("loader = %d", loader.callCount())
	}
	if !unionLoggerHasEvent(logger, "gateway_group_model_union_shared_cache_read_failed") {
		t.Fatalf("读失败告警缺失: %v", logger.snapshot())
	}
}

// Redis 写失败记 WARN，不失败请求。
func TestUnionSharedWriteFailureWarnsButServes(t *testing.T) {
	clock := newManualClock()
	logger := &whWarnLogger{}
	loader := &fakeUnionLoader{results: []GroupModelUnionEntry{unionEntryAt(clock, time.Hour, "m1")}}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) {
		o.Shared = &whFailingFactory{}
		o.Union = loader
		o.Logger = logger
	})
	entry, err := svc.ListCachedGroupModelUnionAsync(context.Background(), GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}})
	if err != nil || strings.Join(entry.Models, ",") != "m1" {
		t.Fatalf("写失败不得失败请求 = %+v err=%v", entry, err)
	}
	if !unionLoggerHasEvent(logger, "gateway_group_model_union_shared_cache_write_failed") {
		t.Fatalf("写失败告警缺失: %v", logger.snapshot())
	}
}

// loader 错误原样返回，不被缓存面失败掩盖；失败结果不缓存。
func TestUnionLoaderErrorSurfacedAndNotCached(t *testing.T) {
	clock := newManualClock()
	boom := errors.New("union loader boom")
	loader := &fakeUnionLoader{err: boom}
	svc := newTestService(t, newFakeModels(), clock, func(o *Options) { o.Union = loader })
	input := GroupModelUnionListOptions{CallerSystemAccountID: "c1", GroupIDs: []string{"g1"}}
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_, err := svc.ListCachedGroupModelUnionAsync(ctx, input)
		if !errors.Is(err, boom) {
			t.Fatalf("第 %d 读必须原样上抛 loader 错误: %v", i+1, err)
		}
	}
	if loader.callCount() != 2 {
		t.Fatalf("失败结果不得缓存, loader = %d", loader.callCount())
	}
}
