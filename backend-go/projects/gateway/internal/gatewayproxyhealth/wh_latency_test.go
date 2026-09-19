package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// whLatencyStore 在 MemoryRuntimeStateStore 之上注入确定性故障，用于驱动
// latencydegradation 的 CAS/回滚/锁错误路径（真实 Redis 故障的可回放替身）。
// 仅限单 goroutine 测试使用，不做并发保护。
type whLatencyStore struct {
	*MemoryRuntimeStateStore
	getJSONFail     func(key string) error
	getJSONManyFail func(keys []string) error
	setJSONFail     func(key string) error
	compareSetErr   func(key string) error // 命中即返回错误
	compareSetForce map[string]bool        // 键 → 强制 applied（不再委托真实存储）
	compareDelErr   func(key string) error // 命中即返回错误
	compareDelForce map[string]bool        // 键 → 强制 applied（不再委托真实存储）
	acquireLockFail func(key string) error
	deleteFail      func(key string) error
}

func newWhLatencyStore(clock *fakeClock) *whLatencyStore {
	return &whLatencyStore{
		MemoryRuntimeStateStore: NewMemoryRuntimeStateStore(clock.Now),
		compareSetForce:         map[string]bool{},
		compareDelForce:         map[string]bool{},
	}
}

func (s *whLatencyStore) GetJSON(ctx context.Context, key string) (json.RawMessage, error) {
	if s.getJSONFail != nil {
		if err := s.getJSONFail(key); err != nil {
			return nil, err
		}
	}
	return s.MemoryRuntimeStateStore.GetJSON(ctx, key)
}

func (s *whLatencyStore) GetJSONMany(ctx context.Context, keys []string) ([]json.RawMessage, error) {
	if s.getJSONManyFail != nil {
		if err := s.getJSONManyFail(keys); err != nil {
			return nil, err
		}
	}
	return s.MemoryRuntimeStateStore.GetJSONMany(ctx, keys)
}

func (s *whLatencyStore) SetJSON(ctx context.Context, key string, value any, ttlMs int64) error {
	if s.setJSONFail != nil {
		if err := s.setJSONFail(key); err != nil {
			return err
		}
	}
	return s.MemoryRuntimeStateStore.SetJSON(ctx, key, value, ttlMs)
}

func (s *whLatencyStore) CompareSetJSON(ctx context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error) {
	if s.compareSetErr != nil {
		if err := s.compareSetErr(key); err != nil {
			return false, err
		}
	}
	if forced, ok := s.compareSetForce[key]; ok {
		return forced, nil
	}
	return s.MemoryRuntimeStateStore.CompareSetJSON(ctx, key, expected, next, ttlMs)
}

func (s *whLatencyStore) CompareDeleteJSON(ctx context.Context, key string, expected json.RawMessage) (bool, error) {
	if s.compareDelErr != nil {
		if err := s.compareDelErr(key); err != nil {
			return false, err
		}
	}
	if forced, ok := s.compareDelForce[key]; ok {
		return forced, nil
	}
	return s.MemoryRuntimeStateStore.CompareDeleteJSON(ctx, key, expected)
}

func (s *whLatencyStore) AcquireLock(ctx context.Context, key string, ttlMs int64, token string) (bool, error) {
	if s.acquireLockFail != nil {
		if err := s.acquireLockFail(key); err != nil {
			return false, err
		}
	}
	return s.MemoryRuntimeStateStore.AcquireLock(ctx, key, ttlMs, token)
}

func (s *whLatencyStore) Delete(ctx context.Context, key string) error {
	if s.deleteFail != nil {
		if err := s.deleteFail(key); err != nil {
			return err
		}
	}
	return s.MemoryRuntimeStateStore.Delete(ctx, key)
}

func newWhLatencyServiceOnStore(clock *fakeClock, store *whLatencyStore) *LatencyDegradationService {
	return NewLatencyDegradationService(store, clock.Now, LatencyDegradationOptions{
		LockRetryDelay: func(int) {},
		Random:         func() float64 { return 0.5 },
	})
}

func whErr(message string) error { return &whTestError{message: message} }

type whTestError struct{ message string }

func (e *whTestError) Error() string { return e.message }

const whReadFailure = "wh 注入读取失败"

// whSeedRaw 直写存储 entry：用于投放 SetJSON 无法编码的非法 JSON，
// 以及 TTL 与降级截止时间不一致的 Node 侧残留载荷。
func whSeedRaw(store *MemoryRuntimeStateStore, clock *fakeClock, key string, raw json.RawMessage, ttlMs int64) {
	store.mu.Lock()
	store.entries[key] = memoryStateEntry{value: append(json.RawMessage(nil), raw...), expiresAt: clock.NowMs() + ttlMs}
	store.mu.Unlock()
}

// 空守卫：nil scope/config 的入口必须短路为 nil,nil（Node 同款行为）。
func TestWhLatencyNilGuards(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	config := speedFirstConfig()
	account := latencyAccount("a")

	if result, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, nil, &config, ""); result != nil || err != nil {
		t.Fatalf("nil scope slow: %+v err=%v", result, err)
	}
	if result, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, latencyScope(), nil, ""); result != nil || err != nil {
		t.Fatalf("nil config slow: %+v err=%v", result, err)
	}
	if result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, nil, &config, nil); result != nil || err != nil {
		t.Fatalf("nil scope success: %+v err=%v", result, err)
	}
	if result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, latencyScope(), &config, nil); result != nil || err != nil {
		t.Fatalf("nil firstByteMs success: %+v err=%v", result, err)
	}
	slow := int64(9_999)
	if result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, latencyScope(), &config, &slow); result != nil || err != nil {
		t.Fatalf("超时首字成功必须被忽略: %+v err=%v", result, err)
	}
	if degraded, err := service.IsNormalRouteAccountLatencyDegraded(contextBackground(), account, nil); degraded || err != nil {
		t.Fatalf("nil scope degraded: %v err=%v", degraded, err)
	}
	// 泛型排序入口的空守卫：nil scope/config 或空账户列表直接原样返回。
	if result, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(contextBackground(), service, nil, func(a SuppressibleGatewayAccount) SuppressibleGatewayAccount { return a }, nil, &config, nil); err != nil || result.Applied {
		t.Fatalf("nil scope order: %+v err=%v", result, err)
	}
	if result, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(contextBackground(), service, []SuppressibleGatewayAccount{account}, nil, latencyScope(), nil, nil); err != nil || result.Applied {
		t.Fatalf("nil config order: %+v err=%v", result, err)
	}
}

// 存储读取/写入故障必须原样传播，不得静默吞掉。
func TestWhLatencyStoreFailurePropagation(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	stateKey := accountLatencyStateKey(*scope, account)

	// 1. 读取 generation 失败（GetJSON 第一次调用发生在 loadLatencyStateGeneration）。
	store.getJSONFail = func(string) error { return whErr(whReadFailure) }
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil || err.Error() != whReadFailure {
		t.Fatalf("generation 读取故障必须传播, err=%v", err)
	}
	store.getJSONFail = nil

	// 2. 读取状态失败：generation 已存在后，GetJSON 命中状态 key 时失败。
	store.getJSONFail = func(key string) error {
		if key == stateKey {
			return whErr(whReadFailure)
		}
		return nil
	}
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil || err.Error() != whReadFailure {
		t.Fatalf("状态读取故障必须传播, err=%v", err)
	}
	store.getJSONFail = nil

	// 3. 写入状态失败：SetJSON 在 mutation lock 内失败。
	store.setJSONFail = func(key string) error {
		if key == stateKey {
			return whErr("wh 注入写入失败")
		}
		return nil
	}
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil {
		t.Fatal("状态写入故障必须传播")
	}
	store.setJSONFail = nil

	// 4. 授权账户缺少绑定上下文：keyChecked 直接报错（Node 抛出同款文案）。
	bad := SuppressibleGatewayAccount{ID: "x", AccessType: "authorized"}
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, bad, scope, &config, ""); err == nil || !strings.Contains(err.Error(), "授权账户运行态键缺少绑定上下文") {
		t.Fatalf("坏账户键必须报错, err=%v", err)
	}
	fast := int64(100)
	if _, err := service.RecordNormalRouteFirstByteSuccess(ctx, bad, scope, &config, &fast); err == nil {
		t.Fatal("success 入口同样必须报错")
	}

	// 5. AcquireLock 故障传播。
	store.acquireLockFail = func(string) error { return whErr("wh 注入锁故障") }
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil {
		t.Fatal("锁获取故障必须传播")
	}
	store.acquireLockFail = nil

	// 6. mutation 内 generation 续约 CAS 故障传播（renew 走 CompareSetJSON）。
	store.compareSetErr = func(key string) error {
		if key == latencyStateGenerationKey {
			return whErr("wh 注入续约故障")
		}
		return nil
	}
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil {
		t.Fatal("generation 续约故障必须传播")
	}
	store.compareSetErr = nil
}

// generation 事件是延迟降级状态的全局纪元：缺失时 CAS 初始化 initial，
// 非规范载荷通过 CAS 原位规范化，反复 CAS 失败按 Node 同款文案报错。
func TestWhLatencyGenerationLifecycle(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)

	// 1. 缺失 → CAS 初始化 initial。
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	generation, err := service.loadLatencyStateGeneration(ctx)
	if err != nil {
		t.Fatalf("initial generation: %v", err)
	}
	if generation != `[0,"initial"]` {
		t.Fatalf("initial generation token = %q", generation)
	}

	// 2. 非规范 publishedAt（带偏移）→ 读路径 CAS 规范化后返回。
	if err := store.SetJSON(ctx, latencyStateGenerationKey, map[string]any{"version": "gen-offset", "publishedAt": "2026-01-02T08:00:00+08:00"}, 60_000); err != nil {
		t.Fatal(err)
	}
	event, raw, err := service.loadLatencyGenerationEventRaw(ctx)
	if err != nil || event == nil {
		t.Fatalf("offset 规范化: %+v err=%v", event, err)
	}
	if event.PublishedAt != "2026-01-02T00:00:00.000Z" {
		t.Fatalf("规范化 publishedAt = %q", event.PublishedAt)
	}
	if string(raw) == "" {
		t.Fatal("规范化结果必须回写原始字节")
	}

	// 3. 非法 JSON → 明确的 RFC3339 错误（直写非法载荷，SetJSON 无法编码）。
	whSeedRaw(store.MemoryRuntimeStateStore, clock, latencyStateGenerationKey, json.RawMessage(`{oops`), 60_000)
	if _, _, err := service.loadLatencyGenerationEventRaw(ctx); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("非法 JSON 必须报 RFC3339 错误, err=%v", err)
	}

	// 4. JSON 合法但缺 version → 同款错误。
	if err := store.SetJSON(ctx, latencyStateGenerationKey, map[string]any{"publishedAt": "2026-01-02T00:00:00Z"}, 60_000); err != nil {
		t.Fatal(err)
	}
	if _, err := service.loadLatencyStateGeneration(ctx); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("缺 version 必须报错, err=%v", err)
	}

	// 5. 规范化 CAS 永不生效（强制 false，循环重试）→ 重试耗尽。
	store2 := newWhLatencyStore(clock)
	service2 := newWhLatencyServiceOnStore(clock, store2)
	if err := store2.SetJSON(ctx, latencyStateGenerationKey, map[string]any{"version": "gen-offset", "publishedAt": "2026-01-02T08:00:00+08:00"}, 60_000); err != nil {
		t.Fatal(err)
	}
	store2.compareSetForce[latencyStateGenerationKey] = false
	_, _, err = service2.loadLatencyGenerationEventRaw(ctx)
	if err == nil || !strings.Contains(err.Error(), "generation marker canonical CAS 重试耗尽") {
		t.Fatalf("规范化 CAS 耗尽必须报错, err=%v", err)
	}

	// 6. 初始化 CAS 永不生效 → 初始化重试耗尽。
	store3 := newWhLatencyStore(clock)
	service3 := newWhLatencyServiceOnStore(clock, store3)
	store3.compareSetForce[latencyStateGenerationKey] = false
	if _, err := service3.loadLatencyStateGeneration(ctx); err == nil || !strings.Contains(err.Error(), "generation marker CAS 初始化重试耗尽") {
		t.Fatalf("初始化 CAS 耗尽必须报错, err=%v", err)
	}
}

// generation 不匹配时 renew 必须拒绝（状态被并发轮换后，旧纪元不得再写入）。
func TestWhLatencyRenewGenerationMismatch(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	if _, err := service.loadLatencyStateGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	// 空纪元：marker 仍缺失时 renew 直接 false。
	store2 := NewMemoryRuntimeStateStore(clock.Now)
	service2 := NewLatencyDegradationService(store2, clock.Now, LatencyDegradationOptions{LockRetryDelay: func(int) {}})
	if renewed, err := service2.renewLatencyStateGeneration(ctx, "bogus"); renewed || err != nil {
		t.Fatalf("缺失 marker renew = %v err=%v", renewed, err)
	}
	// 纪元已轮换：旧 token 必须被拒绝。
	if _, err := service.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "reset", PublishedAt: ISOStringMs(2_000_000)}); err != nil {
		t.Fatal(err)
	}
	if renewed, err := service.renewLatencyStateGeneration(ctx, `[0,"initial"]`); renewed || err != nil {
		t.Fatalf("旧纪元 renew = %v err=%v", renewed, err)
	}
	if renewed, err := service.renewLatencyStateGeneration(ctx, "totally-bogus"); renewed || err != nil {
		t.Fatalf("任意未知 token renew = %v err=%v", renewed, err)
	}
}

// ClearAll 的事件校验与 CAS 分支：老事件只续 TTL，新事件轮换纪元，CAS 永不生效返回 false。
func TestWhLatencyClearAllValidationAndCAS(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)

	if _, err := service.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "  ", PublishedAt: ISOStringMs(2_000_000)}); err == nil || !strings.Contains(err.Error(), "缺少 version") {
		t.Fatalf("空 version 必须报错, err=%v", err)
	}
	if _, err := service.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "v", PublishedAt: "bad"}); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("坏 publishedAt 必须报错, err=%v", err)
	}
	// 老事件（时间更早）：只刷新 marker TTL，返回 true。
	applied, err := service.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "aaaa", PublishedAt: "1970-01-01T00:00:00.000Z"})
	if err != nil || !applied {
		t.Fatalf("老事件刷新 = %v err=%v", applied, err)
	}
	// 同一事件再次发布：相等分支同样只续 TTL。
	applied, err = service.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "aaaa", PublishedAt: "1970-01-01T00:00:00.000Z"})
	if err != nil || !applied {
		t.Fatalf("相等事件刷新 = %v err=%v", applied, err)
	}
	// 新事件：轮换纪元。
	applied, err = service.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "reset-1", PublishedAt: ISOStringMs(3_000_000)})
	if err != nil || !applied {
		t.Fatalf("新事件轮换 = %v err=%v", applied, err)
	}
	// CAS 永不生效：重试耗尽后返回 false 而非报错（Node 契约）。
	store := newWhLatencyStore(clock)
	service2 := newWhLatencyServiceOnStore(clock, store)
	store.compareSetForce[latencyStateGenerationKey] = false
	applied, err = service2.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "reset-2", PublishedAt: ISOStringMs(4_000_000)})
	if err != nil || applied {
		t.Fatalf("CAS 耗尽必须返回 false,nil, got %v err=%v", applied, err)
	}
	// 新事件但 CAS 注入故障 → 错误传播。
	store.compareSetForce = map[string]bool{}
	store.compareSetErr = func(key string) error {
		if key == latencyStateGenerationKey {
			return whErr("wh 注入轮换故障")
		}
		return nil
	}
	if _, err := service2.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "reset-3", PublishedAt: ISOStringMs(5_000_000)}); err == nil {
		t.Fatal("轮换 CAS 故障必须传播")
	}
}

// generation 事件的比较与 token 契约。
func TestWhLatencyGenerationCompareAndToken(t *testing.T) {
	older := LatencyGenerationEvent{Version: "a", PublishedAt: "1970-01-01T00:00:00.000Z"}
	newer := LatencyGenerationEvent{Version: "b", PublishedAt: "2026-01-02T00:00:00.000Z"}
	if compareLatencyGenerationEvents(older, newer) != -1 {
		t.Fatal("更早时间必须比较为 -1")
	}
	if compareLatencyGenerationEvents(newer, older) != 1 {
		t.Fatal("更晚时间必须比较为 1")
	}
	sameTimeLeft := LatencyGenerationEvent{Version: "b", PublishedAt: "2026-01-02T00:00:00.000Z"}
	if compareLatencyGenerationEvents(sameTimeLeft, newer) != 0 {
		t.Fatal("同时间同 version 必须为 0")
	}
	if compareLatencyGenerationEvents(older, LatencyGenerationEvent{Version: "a", PublishedAt: "2026-01-02T00:00:00.000Z"}) != -1 {
		t.Fatal("同时间按 version 字典序比较")
	}
	if latencyGenerationToken(nil) != "initial" {
		t.Fatal("nil 事件 token 必须是 initial")
	}
	// 规范化失败分支。
	if _, err := normalizeLatencyGenerationEvent(LatencyGenerationEvent{Version: "", PublishedAt: ISOStringMs(1)}); err == nil {
		t.Fatal("空 version 规范化必须失败")
	}
	if _, err := normalizeLatencyGenerationEvent(LatencyGenerationEvent{Version: "v", PublishedAt: "nope"}); err == nil {
		t.Fatal("坏时间规范化必须失败")
	}
}

// mutation lock 被占时入口静默放弃（Node 返回 undefined 同款），不得写状态。
func TestWhLatencyMutationLockContention(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	stateKey := accountLatencyStateKey(*scope, account)
	lockKey := latencyStateMutationLockKey(stateKey)

	// 外部持有者先拿到锁。
	acquired, err := store.AcquireLock(ctx, lockKey, 60_000, "other-holder")
	if err != nil || !acquired {
		t.Fatalf("预占锁失败: %v err=%v", acquired, err)
	}
	result, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
	if result != nil || err != nil {
		t.Fatalf("锁被占必须静默放弃: %+v err=%v", result, err)
	}
	if _, err := service.RecordNormalRouteFirstByteSuccess(ctx, account, scope, &config, nil); result != nil || err != nil {
		t.Fatalf("success 入口锁被占同样静默: %+v err=%v", result, err)
	}
	// 释放后恢复写入：首个慢样本只累计不降级。
	if err := store.ReleaseLock(ctx, lockKey, "other-holder"); err != nil {
		t.Fatal(err)
	}
	result, err = service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
	if err != nil || result == nil || result.SlowCount != 1 || result.Degraded {
		t.Fatalf("锁释放后必须可写入: %+v err=%v", result, err)
	}
}

// success 入口的清理分支：无状态短路与过期降级立即清除。
func TestWhLatencySuccessClearingBranches(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	fast := int64(500)

	// 无状态：成功直接短路为 nil,nil。
	if result, err := service.RecordNormalRouteFirstByteSuccess(ctx, account, scope, &config, &fast); result != nil || err != nil {
		t.Fatalf("无状态成功: %+v err=%v", result, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	// 直接播种一个降级截止已过但存储 TTL 未到期的状态（模拟 Node 侧残留：
	// 真实 Redis PX 与业务截止时间不严格重合）。success 必须立即清除并返回 Cleared。
	whSeedRaw(store, clock, accountLatencyStateKey(*scope, account), mustMarshalJSON(latencyState{
		Generation:                     `[0,"initial"]`,
		AccountID:                      account.ID,
		RuntimeKey:                     account.ID,
		Scope:                          *scope,
		Config:                         config,
		FirstSlowAtMs:                  clock.NowMs() - 200_000,
		LastSlowAtMs:                   clock.NowMs() - 200_000,
		SlowCount:                      3,
		DegradedUntilMs:                int64Ptr(clock.NowMs() - 1_000),
		SuccessCount:                   0,
		RecoveryProbeRoundAttemptCount: int64Ptr(0),
		RecoveryProbeRoundSuccessCount: int64Ptr(0),
		Reason:                         "历史降级",
	}), 60_000)
	result, err := service.RecordNormalRouteFirstByteSuccess(ctx, account, scope, &config, &fast)
	if err != nil || result == nil || !result.Cleared || result.RecoverySuccessCount != 0 {
		t.Fatalf("过期降级必须立即清除: %+v err=%v", result, err)
	}
	if degraded, err := service.IsNormalRouteAccountLatencyDegraded(ctx, account, scope); degraded || err != nil {
		t.Fatalf("清除后不再降级: %v err=%v", degraded, err)
	}
}

// 探针成功入口的守卫：首字缺失/超时与账户身份不匹配都不得触碰状态。
func TestWhLatencyRecoveryProbeGuards(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(5_002)
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(ctx, nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %v err=%v", candidates, err)
	}
	candidate := candidates[0]
	fast := int64(500)

	if result, err := service.RecordNormalRouteRecoveryProbeSuccess(ctx, account, candidate, nil); result != nil || err != nil {
		t.Fatalf("nil 首字: %+v err=%v", result, err)
	}
	slow := int64(9_999)
	if result, err := service.RecordNormalRouteRecoveryProbeSuccess(ctx, account, candidate, &slow); result != nil || err != nil {
		t.Fatalf("超时首字: %+v err=%v", result, err)
	}
	other := latencyAccount("other")
	if result, err := service.RecordNormalRouteRecoveryProbeSuccess(ctx, other, candidate, &fast); result != nil || err != nil {
		t.Fatalf("账户不匹配: %+v err=%v", result, err)
	}
	// 同 ID 但 runtimeKey 不同（授权键缺绑定上下文 → runtimeKey 报错）。
	keyless := SuppressibleGatewayAccount{ID: "a", AccessType: "authorized", BindingSystemAccountID: "sys"}
	if _, err := service.RecordNormalRouteRecoveryProbeSuccess(ctx, keyless, candidate, &fast); err == nil || !strings.Contains(err.Error(), "授权账户运行态键缺少绑定上下文") {
		t.Fatalf("runtimeKey 错误必须传播, err=%v", err)
	}
	// 探针失败入口：缺失状态（无当前纪元状态）时静默。
	if result, err := service.RecordNormalRouteProbeFailure(ctx, LatencyProbeCandidate{StateKey: "v1:missing", Generation: "[]"}, ""); result != nil || err != nil {
		t.Fatalf("缺失状态探针失败: %+v err=%v", result, err)
	}
}

// 探针轮次：失败→成功构成混合对被丢弃后，成功落在轮次第二拍会触发轮次重置（0,0）。
func TestWhLatencyProbeRoundResetAfterMixedPair(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	stateKey := accountLatencyStateKey(*scope, account)
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	fast := int64(500)

	// 第一拍：失败（attempt=1, success=0）。
	clock.Advance(5_002)
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(ctx, nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %v err=%v", candidates, err)
	}
	if _, err := service.RecordNormalRouteProbeFailure(ctx, candidates[0], "探针失败"); err != nil {
		t.Fatal(err)
	}
	// 第二拍：成功（attempt=2, success=1）→ 轮次完成但非双成 → 计数清零重开。
	clock.Advance(5_002)
	candidates, err = service.ListNormalRouteLatencyProbeCandidates(ctx, nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("第二拍 candidates = %v err=%v", candidates, err)
	}
	result, err := service.RecordNormalRouteRecoveryProbeSuccess(ctx, account, candidates[0], &fast)
	if err != nil || result == nil || result.Cleared {
		t.Fatalf("混合对后的成功不得清除: %+v err=%v", result, err)
	}
	if result.RecoverySuccessCount != 1 || result.RequiredRecoverySuccessCount != 2 {
		t.Fatalf("混合对后的成功计数 = %+v", result)
	}
	// 状态内的轮次计数必须重置为 0/0。
	raw, err := store.GetJSON(ctx, stateKey)
	if err != nil || raw == nil {
		t.Fatalf("状态必须存在: %v err=%v", raw, err)
	}
	var state latencyState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.RecoveryProbeRoundAttemptCount == nil || *state.RecoveryProbeRoundAttemptCount != 0 ||
		state.RecoveryProbeRoundSuccessCount == nil || *state.RecoveryProbeRoundSuccessCount != 0 {
		t.Fatalf("轮次计数未重置: %+v", state)
	}
	// Defer/Discard 在状态缺失或纪元不匹配时静默成功。
	deferred, err := service.DeferNormalRouteLatencyProbeCandidate(ctx, LatencyProbeCandidate{StateKey: "v1:missing", Generation: "[]"})
	if err != nil || deferred {
		t.Fatalf("缺失状态 defer = %v err=%v", deferred, err)
	}
	if err := service.DiscardNormalRouteLatencyProbeCandidate(ctx, LatencyProbeCandidate{StateKey: "v1:missing", Generation: "[]"}); err != nil {
		t.Fatalf("缺失状态 discard: %v", err)
	}
}

// 探针候选列表的过滤契约：limit 归一化、过期降级排除、空索引排空。
func TestWhLatencyProbeCandidateFiltering(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	// limit=0 归一化为下限 1，仍能取到唯一候选。
	clock.Advance(5_002)
	limited := 0
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(ctx, &limited, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("limit=0 candidates = %v err=%v", candidates, err)
	}
	// 推进到降级截止之后：过期降级状态从候选中排除。
	clock.Set(1_000_000 + 120_000 + 1)
	empty, err := service.ListNormalRouteLatencyProbeCandidates(ctx, nil, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("过期降级候选 = %v err=%v", empty, err)
	}
	// 索引为空的干净服务直接返回空切片。
	fresh, _ := newMemoryLatencyService(clock)
	none, err := fresh.ListNormalRouteLatencyProbeCandidates(ctx, nil, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("空索引候选 = %v err=%v", none, err)
	}
}

// 运行态查询的过滤与排序：系统账户、策略路由、纪元与降级窗口逐层过滤。
func TestWhLatencyDegradedRuntimeFilters(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	config := speedFirstConfig()
	scopeA := &LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs1", GroupID: "g1"}
	scopeB := &LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs1", GroupID: "g2"}
	a := latencyAccount("a")
	b := latencyAccount("b")
	for _, item := range []struct {
		account SuppressibleGatewayAccount
		scope   *LatencyDegradationScope
	}{{a, scopeA}, {a, scopeB}, {b, scopeA}, {b, scopeB}} {
		for i := 0; i < 3; i++ {
			if _, err := service.RecordNormalRouteFirstByteSlow(ctx, item.account, item.scope, &config, ""); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 空 strategy 列表 → 空结果。
	if items, err := service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{}); err != nil || len(items) != 0 {
		t.Fatalf("空 strategy 查询 = %v err=%v", items, err)
	}
	// 系统账户不匹配 → 排除。
	items, err := service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{
		SystemAccountID: stringPtr("other"), RouteStrategyIDs: []string{"rs1"},
	})
	if err != nil || len(items) != 0 {
		t.Fatalf("系统账户过滤 = %v err=%v", items, err)
	}
	// 正常查询：同一降级到期时间下按 account id 稳定排序。
	items, err = service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{RouteStrategyIDs: []string{" rs1 "}})
	if err != nil || len(items) != 4 {
		t.Fatalf("rs1 查询 = %d err=%v", len(items), err)
	}
	if items[0].AccountID != "a" || items[3].AccountID != "b" {
		t.Fatalf("排序错误: %+v", items)
	}
	// Now 覆盖到过期之后 → 全部被过滤。
	future := clock.NowMs() + 120_000 + 1
	items, err = service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{
		RouteStrategyIDs: []string{"rs1"}, Now: &future,
	})
	if err != nil || len(items) != 0 {
		t.Fatalf("过期过滤 = %v err=%v", items, err)
	}
	// 纪元不匹配（旧纪元残留）不外露：手动写入一个旧 generation 的状态。
	raw, err := store.GetJSON(ctx, accountLatencyStateKey(*scopeA, a))
	if err != nil || raw == nil {
		t.Fatalf("状态应存在: %v err=%v", raw, err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["generation"] = "stale-generation"
	if err := store.SetJSON(ctx, accountLatencyStateKey(*scopeA, a), state, 60_000); err != nil {
		t.Fatal(err)
	}
	items, err = service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{RouteStrategyIDs: []string{"rs1"}})
	if err != nil || len(items) != 3 {
		t.Fatalf("旧纪元必须被过滤: %d err=%v", len(items), err)
	}
	// 非法 JSON 状态同样被跳过（直写非法载荷）。
	whSeedRaw(store, clock, accountLatencyStateKey(*scopeB, b), json.RawMessage(`{bad`), 60_000)
	if items, err := service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{RouteStrategyIDs: []string{"rs1"}}); err != nil || len(items) != 2 {
		t.Fatalf("非法状态必须被过滤: %d err=%v", len(items), err)
	}
}

// 手动清理入口的空守卫与陈旧索引清理。
func TestWhLatencyClearGuardsAndStaleIndex(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	config := speedFirstConfig()
	scope := latencyScope()
	account := latencyAccount("a")

	// 空输入守卫。
	cleared, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "  ")
	if err != nil || cleared != 0 {
		t.Fatalf("空策略清理 = %d err=%v", cleared, err)
	}
	cleared, err = service.ClearNormalRouteLatencyDegradationForAccount(ctx, ClearNormalRouteLatencyDegradationForAccountInput{})
	if err != nil || cleared != 0 {
		t.Fatalf("空账户清理 = %d err=%v", cleared, err)
	}
	cleared, err = service.ClearNormalRouteLatencyDegradationForAccountBinding(ctx, ClearNormalRouteLatencyDegradationForAccountBindingInput{
		SystemAccountID: "sys", AccountID: "a", GroupIDs: []*string{nil, stringPtr("  ")},
	})
	if err != nil || cleared != 0 {
		t.Fatalf("空绑定清理 = %d err=%v", cleared, err)
	}
	// 干净索引直接返回 0。
	cleared, err = service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1")
	if err != nil || cleared != 0 {
		t.Fatalf("空索引清理 = %d err=%v", cleared, err)
	}

	// 状态已被直删但索引仍登记：清理走 raw==nil 分支并回收索引。
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Delete(ctx, accountLatencyStateKey(*scope, account)); err != nil {
		t.Fatal(err)
	}
	cleared, err = service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1")
	if err != nil || cleared != 0 {
		t.Fatalf("陈旧索引清理 = %d err=%v", cleared, err)
	}
	keys, err := service.loadLatencyStateIndexKeys(ctx, latencyStateAllIndexKey)
	if err != nil || len(keys) != 0 {
		t.Fatalf("陈旧索引必须被回收: %v err=%v", keys, err)
	}

	// 非法状态载荷：decode 失败 → 不清理不计数。
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetJSON(ctx, accountLatencyStateKey(*scope, account), map[string]any{"junk": true}, 60_000); err != nil {
		t.Fatal(err)
	}
	cleared, err = service.ClearNormalRouteLatencyDegradationForAccount(ctx, ClearNormalRouteLatencyDegradationForAccountInput{SystemAccountID: "sys", AccountID: "a"})
	if err != nil || cleared != 0 {
		t.Fatalf("非法载荷清理 = %d err=%v", cleared, err)
	}

	// 纪元轮换后旧状态不再属于当前 generation：按策略清理计 0。
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ClearAllNormalRouteLatencyDegradation(ctx, LatencyGenerationEvent{Version: "rotate", PublishedAt: ISOStringMs(clock.NowMs() + 1)}); err != nil {
		t.Fatal(err)
	}
	cleared, err = service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1")
	if err != nil || cleared != 0 {
		t.Fatalf("纪元外清理 = %d err=%v", cleared, err)
	}
}

// 索引锁竞争与索引 CAS 重试耗尽的错误面。
func TestWhLatencyIndexLockAndCASErrors(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")

	// 1. all-index 锁被外部持有 → 写入状态后补索引失败 → 整个记录报错。
	acquired, err := store.AcquireLock(ctx, latencyStateAllIndexLockKey, 60_000, "blocker")
	if err != nil || !acquired {
		t.Fatalf("预占索引锁失败: %v err=%v", acquired, err)
	}
	_, err = service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
	if err == nil || !strings.Contains(err.Error(), "索引锁获取失败") {
		t.Fatalf("索引锁竞争必须报错, err=%v", err)
	}
	if err := store.ReleaseLock(ctx, latencyStateAllIndexLockKey, "blocker"); err != nil {
		t.Fatal(err)
	}

	// 2. 索引 CAS 永不生效（强制 false，不委托真实存储）→ 重试耗尽报错。
	store.compareSetForce[latencyStateAllIndexKey] = false
	_, err = service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
	if err == nil || !strings.Contains(err.Error(), "索引 CAS 重试耗尽") {
		t.Fatalf("索引 CAS 耗尽必须报错, err=%v", err)
	}
}

// 写入回滚契约：索引写失败后状态必须回滚到旧值（或删除），回滚失败要聚合报错。
func TestWhLatencyWriteRollback(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	stateKey := accountLatencyStateKey(*scope, account)
	failIndexWrite := func(s *whLatencyStore) {
		s.compareSetErr = func(key string) error {
			if key == latencyStateAllIndexKey {
				return whErr("wh 注入索引故障")
			}
			return nil
		}
	}

	// 1. 新键（previous=nil）：all-index 写失败 → CompareDeleteJSON 回滚 → 状态删除，错误即索引错误。
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	failIndexWrite(store)
	if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil || err.Error() != "wh 注入索引故障" {
		t.Fatalf("新键回滚后必须返回索引错误, err=%v", err)
	}
	if raw, err := store.GetJSON(ctx, stateKey); err != nil || raw != nil {
		t.Fatalf("回滚后状态必须删除, raw=%v err=%v", raw, err)
	}

	// 2. 旧键（previous!=nil）：回滚恢复旧状态（slowCount=1）。
	store2 := newWhLatencyStore(clock)
	service2 := newWhLatencyServiceOnStore(clock, store2)
	if _, err := service2.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	failIndexWrite(store2)
	if _, err := service2.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil {
		t.Fatal("第二次写入必须失败")
	}
	raw, err := store2.GetJSON(ctx, stateKey)
	if err != nil || raw == nil {
		t.Fatalf("回滚后旧状态必须保留: %v err=%v", raw, err)
	}
	var rolledBack latencyState
	if err := json.Unmarshal(raw, &rolledBack); err != nil {
		t.Fatal(err)
	}
	if rolledBack.SlowCount != 1 {
		t.Fatalf("回滚状态 slowCount = %d, want 1", rolledBack.SlowCount)
	}

	// 3. 新键回滚 CAS 未生效（强制 false）→ 聚合错误包含回滚失败。
	store3 := newWhLatencyStore(clock)
	service3 := newWhLatencyServiceOnStore(clock, store3)
	store3.compareDelForce[stateKey] = false
	failIndexWrite(store3)
	_, err = service3.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
	if err == nil || !strings.Contains(err.Error(), "回滚存在 1 个错误") || !strings.Contains(err.Error(), "state rollback CAS 失败") {
		t.Fatalf("回滚 CAS 失败必须聚合报错, err=%v", err)
	}

	// 4. 回滚操作本身故障 → 聚合错误携带原始错误。
	store4 := newWhLatencyStore(clock)
	service4 := newWhLatencyServiceOnStore(clock, store4)
	store4.compareDelErr = func(key string) error {
		if key == stateKey {
			return whErr("wh 注入回滚故障")
		}
		return nil
	}
	failIndexWrite(store4)
	_, err = service4.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, "")
	if err == nil || !strings.Contains(err.Error(), "回滚存在 1 个错误") || !strings.Contains(err.Error(), "wh 注入回滚故障") {
		t.Fatalf("回滚故障必须聚合报错, err=%v", err)
	}

	// 5. 起始 SetJSON 故障直接传播（无回滚路径）。
	store5 := newWhLatencyStore(clock)
	service5 := newWhLatencyServiceOnStore(clock, store5)
	store5.setJSONFail = func(key string) error {
		if key == stateKey {
			return whErr("wh 注入初始写入故障")
		}
		return nil
	}
	if _, err := service5.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err == nil || err.Error() != "wh 注入初始写入故障" {
		t.Fatalf("初始写入故障必须直接传播, err=%v", err)
	}
}

// 逐 key 精确清理在单 key 锁竞争时聚合失败数量。
func TestWhLatencyClearAggregatedFailure(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	lockKey := latencyStateMutationLockKey(accountLatencyStateKey(*scope, account))
	if acquired, err := store.AcquireLock(ctx, lockKey, 60_000, "blocker"); err != nil || !acquired {
		t.Fatalf("预占 mutation 锁失败: %v err=%v", acquired, err)
	}
	_, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, "rs1")
	if err == nil || !strings.Contains(err.Error(), "逐 key 精确清理存在 1 个失败") {
		t.Fatalf("聚合清理错误 = %v", err)
	}
}

// 后台清理槽遵循 ctx 取消：槽被占满且 ctx 已取消时任务不得执行。
func TestWhLatencyBackgroundSlotCancelled(t *testing.T) {
	clock := newFakeClock(1_000_000)
	store := NewMemoryRuntimeStateStore(clock.Now)
	// 并发度 1：占满唯一的槽后，发送必然阻塞，select 确定性走 ctx.Done 分支。
	service := NewLatencyDegradationService(store, clock.Now, LatencyDegradationOptions{
		BackgroundClearConcurrency: 1,
		LockRetryDelay:             func(int) {},
	})
	service.clearSlots <- struct{}{}
	ctx, cancel := context.WithCancel(contextBackground())
	cancel()
	executed := false
	_, err := service.runWithBackgroundSlot(ctx, func() (int64, error) {
		executed = true
		return 0, nil
	})
	if err == nil || executed {
		t.Fatalf("取消后任务不得执行: err=%v executed=%v", err, executed)
	}
}

// 纯助手契约：键净化/指针相等/索引归一化等迁移互操作语义。
func TestWhLatencyPureHelpers(t *testing.T) {
	if sanitizeKeyPart("  ") != "_" {
		t.Fatalf("空净化结果必须是下划线, got %q", sanitizeKeyPart("  "))
	}
	if sanitizeKeyPart("a/b:c-d_e") != "a_b:c-d_e" {
		t.Fatalf("非法字符逐字替换, got %q", sanitizeKeyPart("a/b:c-d_e"))
	}
	if !stringPtrEqual(nil, nil) || stringPtrEqual(nil, stringPtr("x")) || stringPtrEqual(stringPtr("x"), nil) || !stringPtrEqual(stringPtr("x"), stringPtr("x")) || stringPtrEqual(stringPtr("x"), stringPtr("y")) {
		t.Fatal("stringPtrEqual 四分支语义错误")
	}
	if isoPtr(nil) != nil {
		t.Fatal("nil 毫秒必须输出 nil ISO")
	}
	if optionalAccountName(SuppressibleGatewayAccount{ID: "a", Name: "   "}) != nil {
		t.Fatal("纯空白账户名必须输出 nil")
	}
	if got := latencyStateRemainingTTLMs(nil, 100); got != 1 {
		t.Fatalf("nil 降级截止的剩余 TTL = %d", got)
	}
	if got := latencyStateRemainingTTLMs(int64Ptr(50), 100); got != 1 {
		t.Fatalf("已过期的剩余 TTL 下限 = %d", got)
	}
	// 索引归一化：去空白、去重、保序、尾部截断到上限。
	keys := normalizeLatencyStateIndexKeys([]string{" b ", "b", "a", ""})
	if len(keys) != 2 || keys[0] != "b" || keys[1] != "a" {
		t.Fatalf("索引归一化 = %v", keys)
	}
	if got := normalizeLatencyStateIndexKeys(nil); got == nil || len(got) != 0 {
		t.Fatal("nil 索引必须归一化为空切片")
	}
	overflow := make([]string, 0, latencyStateIndexMaxKeys+2)
	for i := 0; i < latencyStateIndexMaxKeys+2; i++ {
		overflow = append(overflow, "k"+itoaForTest(int64(i)))
	}
	trimmed := normalizeLatencyStateIndexKeys(overflow)
	if len(trimmed) != latencyStateIndexMaxKeys || trimmed[0] != "k2" {
		t.Fatalf("超限索引必须保留尾部, len=%d first=%v", len(trimmed), trimmed[0])
	}
	// 策略 ID 归一化：去空白、去重。
	ids := normalizedRouteStrategyIDs([]string{" rs1 ", "rs1", "", "rs2"})
	if len(ids) != 2 || ids[0] != "rs1" || ids[1] != "rs2" {
		t.Fatalf("策略归一化 = %v", ids)
	}
	// 探针候选：缺降级截止时间 → 不可用。
	if _, ok := probeCandidateFromState("k", latencyState{}); ok {
		t.Fatal("缺 degradedUntil 的状态不得产出候选")
	}
	// 解码校验：非法 JSON / 负轮次计数 / 缺 scope。
	if _, ok := decodeLatencyState(json.RawMessage(`{bad`)); ok {
		t.Fatal("非法 JSON 必须解码失败")
	}
	negativeRound := int64(-1)
	if _, ok := decodeLatencyState(mustMarshalJSON(latencyState{
		Generation: "g", AccountID: "a", RuntimeKey: "a",
		Scope: LatencyDegradationScope{SystemAccountID: "s", RouteStrategyID: "r", GroupID: "g"},
		Config: SpeedFirstRuntimeConfig{FirstByteDeadlineMs: 1, SlowTriggerCount: 1, SlowWindowSeconds: 1,
			RecoverySuccessCount: 1, ProbeIntervalSeconds: 1, DegradedTTLSeconds: 1, MaxFirstByteRetriesPerRequest: 1},
		RecoveryProbeRoundAttemptCount: &negativeRound,
	})); ok {
		t.Fatal("负轮次计数必须校验失败")
	}
	if _, ok := decodeLatencyState(mustMarshalJSON(latencyState{Generation: "g", AccountID: "a", RuntimeKey: "a"})); ok {
		t.Fatal("缺 scope/config 必须校验失败")
	}
	// TTL 契约：降级用 DegradedTTLSeconds，非降级用 SlowWindowSeconds，秒下限 1。
	cfg := SpeedFirstRuntimeConfig{SlowWindowSeconds: 0, DegradedTTLSeconds: 0}
	if latencyStateTTLMs(cfg, false) != 1_000 || latencyStateTTLMs(cfg, true) != 1_000 {
		t.Fatal("秒数下限必须折叠为 1s")
	}
}
