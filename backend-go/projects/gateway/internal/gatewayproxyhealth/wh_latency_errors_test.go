package gatewayproxyhealth

import (
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// whLatencyErrorSweepError 统一注入错误标识。
var errWhInject = errors.New("wh 注入故障")

// 排序与 success 入口的存储故障传播：generation/状态读取、锁获取与竞争放弃。
func TestWhLatencyOrderAndSuccessErrorSweep(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	stateKey := accountLatencyStateKey(*scope, account)
	accounts := []SuppressibleGatewayAccount{account}
	view := func(a SuppressibleGatewayAccount) SuppressibleGatewayAccount { return a }

	// 泛型排序：generation 读取故障。
	store.getJSONFail = func(string) error { return errWhInject }
	if _, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(ctx, service, accounts, view, scope, &config, nil); err == nil {
		t.Fatal("排序 generation 故障必须传播")
	}
	store.getJSONFail = nil
	// 泛型排序：状态读取故障（批量 GetJSONMany 整体失败，generation 键放行）。
	store.getJSONManyFail = func([]string) error { return errWhInject }
	if _, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(ctx, service, accounts, view, scope, &config, nil); err == nil {
		t.Fatal("排序状态故障必须传播")
	}
	store.getJSONManyFail = nil

	fast := int64(100)
	// success 入口：锁获取故障。
	store.acquireLockFail = func(string) error { return errWhInject }
	if _, err := service.RecordNormalRouteFirstByteSuccess(ctx, account, scope, &config, &fast); err == nil {
		t.Fatal("success 锁故障必须传播")
	}
	store.acquireLockFail = nil
	// success 入口：mutation 锁被外部持有 → 静默放弃。
	acquired, err := store.AcquireLock(ctx, latencyStateMutationLockKey(stateKey), 60_000, "holder")
	if err != nil || !acquired {
		t.Fatalf("预占锁失败: %v err=%v", acquired, err)
	}
	if result, err := service.RecordNormalRouteFirstByteSuccess(ctx, account, scope, &config, &fast); result != nil || err != nil {
		t.Fatalf("success 锁竞争必须静默: %+v err=%v", result, err)
	}
	if err := store.ReleaseLock(ctx, latencyStateMutationLockKey(stateKey), "holder"); err != nil {
		t.Fatal(err)
	}
	// success 入口：generation 续约 CAS 故障。
	store.compareSetErr = func(key string) error {
		if key == latencyStateGenerationKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.RecordNormalRouteFirstByteSuccess(ctx, account, scope, &config, &fast); err == nil {
		t.Fatal("success 续约故障必须传播")
	}
	store.compareSetErr = nil
}

// 探针失败/推迟/丢弃入口的故障与短路面。
func TestWhLatencyProbeMutationErrorSweep(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	key := accountLatencyStateKey(*scope, account)
	if _, err := service.loadLatencyStateGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	now := clock.NowMs()

	seed := func(degradedUntilMs, nextProbeAtMs int64) LatencyProbeCandidate {
		state := latencyState{
			Generation: `[0,"initial"]`, AccountID: account.ID, RuntimeKey: account.ID,
			Scope: *scope, Config: config,
			FirstSlowAtMs: now - 300_000, LastSlowAtMs: now, SlowCount: 3,
			DegradedUntilMs: int64Ptr(degradedUntilMs), NextProbeAtMs: int64Ptr(nextProbeAtMs),
			RecoveryProbeRoundAttemptCount: int64Ptr(0), RecoveryProbeRoundSuccessCount: int64Ptr(0),
			Reason: "播种",
		}
		whSeedRaw(store.MemoryRuntimeStateStore, clock, key, mustMarshalJSON(state), 120_000)
		candidate, ok := probeCandidateFromState(key, state)
		if !ok {
			t.Fatal("播种状态必须产出候选")
		}
		return candidate
	}

	// 探针失败：mutation 锁被持有 → 静默。
	live := seed(now+100_000, now-1)
	lockKey := latencyStateMutationLockKey(key)
	if acquired, err := store.AcquireLock(ctx, lockKey, 60_000, "holder"); err != nil || !acquired {
		t.Fatalf("预占锁失败: %v err=%v", acquired, err)
	}
	if result, err := service.RecordNormalRouteProbeFailure(ctx, live, ""); result != nil || err != nil {
		t.Fatalf("探针失败锁竞争必须静默: %+v err=%v", result, err)
	}
	if err := store.ReleaseLock(ctx, lockKey, "holder"); err != nil {
		t.Fatal(err)
	}
	// 探针失败：状态读取故障。
	store.getJSONFail = func(k string) error {
		if k == key {
			return errWhInject
		}
		return nil
	}
	if _, err := service.RecordNormalRouteProbeFailure(ctx, live, ""); err == nil {
		t.Fatal("探针失败读取故障必须传播")
	}
	store.getJSONFail = nil
	// 探针失败：候选过期（降级截止与实际状态不一致）→ 静默。
	stale := live
	stale.DegradedUntil = ISOStringMs(now + 999_000)
	if result, err := service.RecordNormalRouteProbeFailure(ctx, stale, ""); result != nil || err != nil {
		t.Fatalf("过期候选必须静默: %+v err=%v", result, err)
	}
	// 探针失败：降级已到期 → 删除状态并静默。
	expired := seed(now-1_000, now-1)
	if result, err := service.RecordNormalRouteProbeFailure(ctx, expired, ""); result != nil || err != nil {
		t.Fatalf("到期候选必须删除后静默: %+v err=%v", result, err)
	}
	if raw, err := store.GetJSON(ctx, key); err != nil || raw != nil {
		t.Fatalf("到期状态必须被删除: %v err=%v", raw, err)
	}
	// 探针失败：删除故障传播。
	expired = seed(now-1_000, now-1)
	store.deleteFail = func(k string) error {
		if k == key {
			return errWhInject
		}
		return nil
	}
	if _, err := service.RecordNormalRouteProbeFailure(ctx, expired, ""); err == nil {
		t.Fatal("删除故障必须传播")
	}
	store.deleteFail = nil

	// 推迟：读取故障传播。
	live = seed(now+100_000, now-1)
	store.getJSONFail = func(k string) error {
		if k == key {
			return errWhInject
		}
		return nil
	}
	if _, err := service.DeferNormalRouteLatencyProbeCandidate(ctx, live); err == nil {
		t.Fatal("推迟读取故障必须传播")
	}
	store.getJSONFail = nil
	// 推迟：候选过期 → 静默不推迟。
	stale = live
	stale.DegradedUntil = ISOStringMs(now + 999_000)
	if deferred, err := service.DeferNormalRouteLatencyProbeCandidate(ctx, stale); err != nil || deferred {
		t.Fatalf("过期候选推迟 = %v err=%v", deferred, err)
	}
	// 推迟：降级到期 → 删除并静默。
	expired = seed(now-1_000, now-1)
	if deferred, err := service.DeferNormalRouteLatencyProbeCandidate(ctx, expired); err != nil || deferred {
		t.Fatalf("到期候选推迟 = %v err=%v", deferred, err)
	}
	// 推迟：写入故障传播（索引写失败 → 回滚旧状态）。
	live = seed(now+100_000, now-1)
	store.compareSetErr = func(k string) error {
		if k == latencyStateAllIndexKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.DeferNormalRouteLatencyProbeCandidate(ctx, live); err == nil {
		t.Fatal("推迟写入故障必须传播")
	}
	store.compareSetErr = nil
	// 推迟：锁被持有 → false, nil。
	if acquired, err := store.AcquireLock(ctx, lockKey, 60_000, "holder"); err != nil || !acquired {
		t.Fatal("预占锁失败")
	}
	if deferred, err := service.DeferNormalRouteLatencyProbeCandidate(ctx, live); deferred || err != nil {
		t.Fatalf("推迟锁竞争 = %v err=%v", deferred, err)
	}
	if err := store.ReleaseLock(ctx, lockKey, "holder"); err != nil {
		t.Fatal(err)
	}

	// 丢弃：读取故障传播。
	store.getJSONFail = func(k string) error {
		if k == key {
			return errWhInject
		}
		return nil
	}
	if err := service.DiscardNormalRouteLatencyProbeCandidate(ctx, live); err == nil {
		t.Fatal("丢弃读取故障必须传播")
	}
	store.getJSONFail = nil
	// 丢弃：删除故障传播。
	store.deleteFail = func(k string) error {
		if k == key {
			return errWhInject
		}
		return nil
	}
	if err := service.DiscardNormalRouteLatencyProbeCandidate(ctx, live); err == nil {
		t.Fatal("丢弃删除故障必须传播")
	}
	store.deleteFail = nil
	// 丢弃：候选过期 → 静默。
	stale = live
	stale.DegradedUntil = ISOStringMs(now + 999_000)
	if err := service.DiscardNormalRouteLatencyProbeCandidate(ctx, stale); err != nil {
		t.Fatalf("过期候选丢弃: %v", err)
	}
}

// 清理入口的索引/纪元读取故障传播。
func TestWhLatencyClearErrorSweep(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	key := accountLatencyStateKey(*scope, account)
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}

	// 索引读取故障（三个清理入口 + 账户绑定入口）。
	store.getJSONFail = func(k string) error {
		if k == latencyStateAllIndexKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1"); err == nil {
		t.Fatal("策略清理索引故障必须传播")
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForAccount(ctx, ClearNormalRouteLatencyDegradationForAccountInput{SystemAccountID: "sys", AccountID: "a"}); err == nil {
		t.Fatal("账户清理索引故障必须传播")
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForAccountBinding(ctx, ClearNormalRouteLatencyDegradationForAccountBindingInput{SystemAccountID: "sys", AccountID: "a", GroupIDs: []*string{stringPtr("g1")}}); err == nil {
		t.Fatal("绑定清理索引故障必须传播")
	}
	// 纪元读取故障（索引放行）。
	store.getJSONFail = func(k string) error {
		if k == latencyStateGenerationKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1"); err == nil {
		t.Fatal("策略清理纪元故障必须传播")
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForAccount(ctx, ClearNormalRouteLatencyDegradationForAccountInput{SystemAccountID: "sys", AccountID: "a"}); err == nil {
		t.Fatal("账户清理纪元故障必须传播")
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForAccountBinding(ctx, ClearNormalRouteLatencyDegradationForAccountBindingInput{SystemAccountID: "sys", AccountID: "a", GroupIDs: []*string{stringPtr("g1")}}); err == nil {
		t.Fatal("绑定清理纪元故障必须传播")
	}
	store.getJSONFail = nil

	// 逐 key 清理：状态读取故障聚合。
	store.getJSONFail = func(k string) error {
		if k == key {
			return errWhInject
		}
		return nil
	}
	_, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1")
	if err == nil || !strings.Contains(err.Error(), "逐 key 精确清理存在 1 个失败") {
		t.Fatalf("状态读取聚合错误 = %v", err)
	}
	store.getJSONFail = nil
	// 逐 key 清理：删除故障聚合。
	store.deleteFail = func(k string) error {
		if k == key {
			return errWhInject
		}
		return nil
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1"); err == nil {
		t.Fatal("删除故障必须聚合")
	}
	store.deleteFail = nil
	// 逐 key 清理：mutation 锁获取故障聚合。
	store.acquireLockFail = func(k string) error {
		if strings.HasPrefix(k, latencyStateVersion+":mutation-lock:") {
			return errWhInject
		}
		return nil
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1"); err == nil {
		t.Fatal("锁故障必须聚合")
	}
	store.acquireLockFail = nil
	// 逐 key 清理：raw 缺失且索引回收故障聚合。
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	store.compareSetErr = func(k string) error {
		if k == latencyStateProbeIndexKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1"); err == nil {
		t.Fatal("陈旧索引回收故障必须聚合")
	}
	store.compareSetErr = nil
}

// ClearAll 刷新分支与 renew 的 CAS 错误面。
func TestWhLatencyClearAllRefreshAndRenewErrors(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	older := LatencyGenerationEvent{Version: "aaaa", PublishedAt: "1970-01-01T00:00:00.000Z"}

	// 初始化纪元后：老事件刷新走 CompareSet，注入故障必须传播。
	if _, err := service.loadLatencyStateGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	store.compareSetErr = func(k string) error {
		if k == latencyStateGenerationKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.ClearAllNormalRouteLatencyDegradation(ctx, older); err == nil {
		t.Fatal("刷新 CAS 故障必须传播")
	}
	store.compareSetErr = nil
	// 刷新 CAS 永不生效 → 重试耗尽后返回 false。
	store.compareSetForce[latencyStateGenerationKey] = false
	applied, err := service.ClearAllNormalRouteLatencyDegradation(ctx, older)
	if err != nil || applied {
		t.Fatalf("刷新 CAS 耗尽 = %v err=%v", applied, err)
	}
	store.compareSetForce = map[string]bool{}
	// renew：纪元读取故障。
	store.getJSONFail = func(k string) error {
		if k == latencyStateGenerationKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.renewLatencyStateGeneration(ctx, `[0,"initial"]`); err == nil {
		t.Fatal("renew 读取故障必须传播")
	}
	store.getJSONFail = nil
	// renew：刷新 CAS 故障。
	store.compareSetErr = func(k string) error {
		if k == latencyStateGenerationKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.renewLatencyStateGeneration(ctx, `[0,"initial"]`); err == nil {
		t.Fatal("renew CAS 故障必须传播")
	}
	store.compareSetErr = nil
}

// 索引快照读取故障与降级写入（探针索引失败）的回滚路径。
func TestWhLatencyIndexSnapshotAndProbeIndexErrors(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	key := accountLatencyStateKey(*scope, account)

	// 索引快照读取故障：状态写入成功后补索引失败 → 回滚删除。
	store.getJSONFail = func(k string) error {
		if k == latencyStateGenerationKey {
			return nil
		}
		if k == latencyStateAllIndexKey {
			return errWhInject
		}
		return nil
	}
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil {
		t.Fatal("索引快照故障必须传播")
	}
	if raw, err := store.GetJSON(ctx, key); err != nil || raw != nil {
		t.Fatalf("回滚后状态必须删除: %v err=%v", raw, err)
	}
	store.getJSONFail = nil

	// 降级写入触发探针索引：探针索引写失败 → 回滚并回收 all-index。
	for i := 0; i < 3; i++ {
		if i < 2 {
			if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
				t.Fatal(err)
			}
			continue
		}
		// 第三次慢样本触发降级 → 需要写 probe-index，注入故障。
		store.compareSetErr = func(k string) error {
			if k == latencyStateProbeIndexKey {
				return errWhInject
			}
			return nil
		}
		_, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
		store.compareSetErr = nil
		if err == nil {
			t.Fatal("探针索引故障必须传播")
		}
	}
	// 注意：降级写入回滚后，第三次慢样本未生效；重放后应可正常降级。
	result, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
	if err != nil || result == nil || !result.Degraded {
		t.Fatalf("回滚重放必须可降级: %+v err=%v", result, err)
	}
}

// 纯助手补充：轮次空指针、账户键回退、配置校验与空索引移除。
func TestWhLatencyHelperEdges(t *testing.T) {
	empty := latencyState{}
	if recoveryProbeRoundAttempts(empty) != 0 || recoveryProbeRoundSuccesses(empty) != 0 {
		t.Fatal("nil 轮次计数必须视为 0")
	}
	// 授权账户 runtimeKey 失败时，普通键推导回退到 account.ID。
	broken := SuppressibleGatewayAccount{ID: "fallback", AccessType: "authorized"}
	if key := accountLatencyStateKey(LatencyDegradationScope{SystemAccountID: "s", RouteStrategyID: "r", GroupID: "g"}, broken); !strings.HasPrefix(key, "v1:s:r:g:fallback") {
		t.Fatalf("回退键 = %q", key)
	}
	// scope 合法但 config 非法的状态必须被拒绝。
	invalidConfig := latencyState{
		Generation: "g", AccountID: "a", RuntimeKey: "a",
		Scope: LatencyDegradationScope{SystemAccountID: "s", RouteStrategyID: "r", GroupID: "g"},
	}
	if _, ok := decodeLatencyState(mustMarshalJSON(invalidConfig)); ok {
		t.Fatal("缺 config 字段必须校验失败")
	}
	// 空移除列表直接成功。
	service := &LatencyDegradationService{}
	if err := service.removeLatencyStateIndexKeysStrict(contextBackground(), nil); err != nil {
		t.Fatalf("空移除必须成功: %v", err)
	}
}

// 上游桶异步排序的内存回退、全员避让与故障传播。
func TestWhProxyHealthAsyncOrderSweep(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	// 1. 无状态存储 → 异步入口回退内存路径。
	memoryOnly := NewProxyHealthService(clock.Now, nil, ProxyHealthOptions{}, nil)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	result, err := memoryOnly.OrderGatewayAccountsByUpstreamBucketHealthAsync(ctx, []gatewayruntimecache.OpenAIAccountSecret{a}, nil)
	if err != nil || len(result.Accounts) != 1 {
		t.Fatalf("内存回退 = %+v err=%v", result, err)
	}
	// 2. 内存回退 + 空列表 → 空结果。
	if result, err := memoryOnly.OrderGatewayAccountsByUpstreamBucketHealthAsync(ctx, nil, nil); err != nil || len(result.Accounts) != 0 {
		t.Fatalf("空列表 = %+v err=%v", result, err)
	}
	// 3. Redis 语义路径：全员避让 → WithEntries 的 bypassed 汇总。
	store := newWhLatencyStore(clock)
	service := NewProxyHealthService(clock.Now, store, ProxyHealthOptions{CASMaxAttempts: 4}, nil)
	b := accountFixture{id: "b", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	if _, err := service.RecordGatewayUpstreamBucketFailureAsync(ctx, a, "err", FailureRecordOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordGatewayUpstreamBucketFailureAsync(ctx, b, "err", FailureRecordOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err = service.OrderGatewayAccountsByUpstreamBucketHealthAsync(ctx, []gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.BypassedAllAvoided || result.Applied {
		t.Fatalf("全员避让必须 bypassed: %+v", result)
	}
	// 4. 半开 CAS 注入故障 → 异步排序报错。
	clock.Advance(61_000)
	store.compareSetErr = func(k string) error {
		if strings.HasPrefix(k, "bucket:") {
			return errWhInject
		}
		return nil
	}
	if _, err := service.OrderGatewayAccountsByUpstreamBucketHealthAsync(ctx, []gatewayruntimecache.OpenAIAccountSecret{a, b}, nil); err == nil {
		t.Fatal("半开 CAS 故障必须传播")
	}
	store.compareSetErr = nil
	// 5. 桶条目读取故障传播。
	store.getJSONFail = func(k string) error {
		if strings.HasPrefix(k, "bucket:") {
			return errWhInject
		}
		return nil
	}
	if _, err := service.OrderGatewayAccountsByUpstreamBucketHealthAsync(ctx, []gatewayruntimecache.OpenAIAccountSecret{a}, nil); err == nil {
		t.Fatal("桶读取故障必须传播")
	}
	store.getJSONFail = nil
	// 6. 空账户列表的同步排序。
	if result := service.OrderGatewayAccountsByUpstreamBucketHealth(nil, nil); len(result.Accounts) != 0 {
		t.Fatalf("空列表同步排序 = %+v", result)
	}
	// 7. URL 解析失败回退小写文本（Node URL 构造器抛错路径）。
	if got := normalizeOpenAIBaseURLForBucket("http://[::1"); got != "http://[::1/v1" {
		t.Fatalf("解析失败回退 = %q", got)
	}
}

func gatewayAccountSecretOf(accounts ...gatewayruntimecache.OpenAIAccountSecret) []gatewayruntimecache.OpenAIAccountSecret {
	return accounts
}
