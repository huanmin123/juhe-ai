package gatewayaccounteffects

// w14g：账户副作用族覆盖率补强（错误/防御分支直驱）。
//
// 不可达语句登记（分析依据见各条；均不需要在本轮构造可达路径）：
//   - clock.go passiveScheduleJitterWindowMs 的 half<0 与 windowMs>half 分支
//     （197-202）：interval 先被钳到 ≥1，且各档 window 构造上不超过 interval/2。
//   - clock.go passiveScheduleDelayMs 的 result<1 分支（243-245）：delay ≥ 1
//     且 offset 被 window ≤ delay/2 约束，result 恒 ≥ 1。
//   - keymodelattempt.go/记忆存储中 CreateKeyModelOpenState 的 openErr 分支
//     （keymodelmemory.go 242/247-249/297/302-304、keymodelredis.go 171-173）：
//     前置 CapabilityHash 校验与 Normalize+Hash 复合校验同源同序，错误必在
//     前置分支先返回。
//   - keymodelmemory.go RecordFailure 的 CapabilityHash 重复校验（331-333）：
//     validateFailureIntent 已用同一输入校验过。
//   - apikeyguard.go rememberLocalAPISuppressionLocked 的 delayIndex<0 分支
//     （427-429）：failureCount = current+1 恒 ≥ 1。
//   - apikeyguard.go fences 淘汰循环的 oldestKey=="" break（483-484）：range
//     非空 map 时 key 恒非空。
//   - apikeyeffects.go 成功写入异步合并分支（176-187/301-317/350-357）：依赖
//     真实 timer 竞态，且已知并行负载 flake（TestWeAPIKeyEffects...），不新增
//     长等待异步测试。
//   - apikeytransient.go/keymodelredis.go 的 clientForUse ParseURL 失败分支
//     （apikeytransient.go 324-326/379-381/388-390）：构造函数已先行校验同一
//     URL，clientForUse 复析同串不可能新失败。
//   - apikeytransient.go mustJSON/newUUID 错误分支（639-649）与
//     keymodelruntime.go json.Marshal 纯结构分支（157-159/188-190/231-233）。
//   - keymodelrecovery.go Sweep 续租 ticker/超时分支（296-316）依赖 10s/30s
//     真实定时器、223-225 为负数钳制防御。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

var w14gErr = errors.New("w14g injected fault")

func w14gInvalidCapability() CapabilityKey {
	bad := testCapability()
	bad.ClientModel = ""
	return bad
}

// ---------------------------------------------------------------------------
// keymodelmemory.go
// ---------------------------------------------------------------------------

func TestW14GMemoryStoreBranches(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewInMemoryKeyModelRuntimeStore(clock)
	ctx := context.Background()
	bad := w14gInvalidCapability()
	// Get / GetRecoveryTarget 的 hash 校验失败。
	if _, err := store.Get(ctx, bad); err == nil {
		t.Fatalf("非法 capability Get 必须失败")
	}
	if store.GetRecoveryTarget(bad) != nil {
		t.Fatalf("非法 capability GetRecoveryTarget 必须为 nil")
	}
	// RenewRecoveryLease 未知 hash。
	if store.RenewRecoveryLease(MemoryRecoveryRenewInput{CapabilityHash: "w14g-unknown", NowMs: 1000}) {
		t.Fatalf("未知 hash 续租必须失败")
	}
	// RecordFailure permit 与 capability 不匹配。
	_, err := store.RecordFailure(ctx, func() KeyModelFailureIntent {
		intent := memoryIntent(testCapability(), "w14g-permit-1", 1000)
		intent.Permit = &KeyModelForegroundPermit{CapabilityHash: "deadbeef", AttemptID: "a1"}
		return intent
	}())
	if err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("permit 不匹配必须失败: %v", err)
	}
	// 已有 CLOSED 状态上同修订的新失败 → 新 open 状态 generation+1。
	base := testCapability()
	seedHash, err := CapabilityHash(base)
	if err != nil {
		t.Fatal(err)
	}
	closedAt := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC).UnixMilli()
	store.states[seedHash] = KeyModelState{
		CapabilityKey: base, CapabilityHash: seedHash, Generation: 1,
		Phase: KeyModelPhaseClosed, LastObservedAtMs: closedAt,
	}
	result, err := store.RecordFailure(ctx, memoryIntent(base, "w14g-gen-2", 2000))
	if err != nil || result.State == nil || result.State.Generation != 2 {
		t.Fatalf("CLOSED 状态上的新失败 = %+v err=%v", result, err)
	}
	// AdmitForeground 非法 capability / 空 attemptId。
	if _, err := store.AdmitForeground(ctx, bad, "w14g-a"); err == nil {
		t.Fatalf("非法 capability AdmitForeground 必须失败")
	}
	if _, err := store.AdmitForeground(ctx, testCapability(), "   "); err == nil {
		t.Fatalf("空 attemptId 必须失败")
	}
	// 过期 MainProbe fence 清理。
	permit := KeyModelForegroundPermit{CapabilityHash: w14gCapabilityHash(t), AttemptID: "w14g-fence"}
	if err := store.RecordMainProbeFailure(ctx, testCapability(), permit); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Minute)
	if _, err := store.AdmitForeground(ctx, testCapability(), "w14g-fence-next"); err != nil {
		t.Fatal(err)
	}
	// 过期前台租约清理。
	clock.Set(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := store.AdmitForeground(ctx, testCapability(), "w14g-lease-1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Hour)
	if _, err := store.AdmitForeground(ctx, testCapability(), "w14g-lease-2"); err != nil {
		t.Fatal(err)
	}
	// ReleaseForeground：无活跃表 / 未知 attempt。
	fresh := NewInMemoryKeyModelRuntimeStore(clock)
	validHash := w14gCapabilityHash(t)
	ok, err := fresh.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: validHash, AttemptID: "w14g-x"})
	if err != nil || ok {
		t.Fatalf("空活跃表释放 = %v err=%v", ok, err)
	}
	if _, err := fresh.AdmitForeground(ctx, testCapability(), "w14g-held"); err != nil {
		t.Fatal(err)
	}
	ok, err = fresh.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: validHash, AttemptID: "w14g-other"})
	if err != nil || ok {
		t.Fatalf("未知 attempt 释放 = %v err=%v", ok, err)
	}
	// RenewForeground：无活跃表 / 未知 attempt。
	if renewed, err := fresh.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: validHash, AttemptID: "w14g-none"}); renewed != nil || err != nil {
		t.Fatalf("空活跃表续租 = %+v err=%v", renewed, err)
	}
	if _, err := fresh.AdmitForeground(ctx, testCapability(), "w14g-held2"); err != nil {
		t.Fatal(err)
	}
	if renewed, err := fresh.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: validHash, AttemptID: "w14g-missing"}); renewed != nil || err != nil {
		t.Fatalf("未知 attempt 续租 = %+v err=%v", renewed, err)
	}
	// RecordMainProbeFailure 非法 capability。
	if err := store.RecordMainProbeFailure(ctx, bad, permit); err == nil {
		t.Fatalf("非法 capability 主探针失败必须报错")
	}
	// Clear/Defer fence 非法 hash。
	if _, err := store.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: "bad", KeyFingerprint: "fp", OwnerID: "o"}, "fp"); err == nil {
		t.Fatalf("非法 hash 清理 fence 必须报错")
	}
	if _, err := store.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: "bad"}); err == nil {
		t.Fatalf("非法 hash 延后 fence 必须报错")
	}
	// normalizeRecoveryTarget 缺 systemAccountId。
	if _, err := normalizeRecoveryTarget(KeyModelRecoveryTarget{AccountID: "a", GroupID: "g"}); err == nil {
		t.Fatalf("缺 systemAccountId 必须失败")
	}
}

func w14gCapabilityHash(t *testing.T) string {
	t.Helper()
	hash, err := CapabilityHash(testCapability())
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// ---------------------------------------------------------------------------
// keymodelattempt.go / keymodelrecovery.go
// ---------------------------------------------------------------------------

func TestW14GBudgetAndPrepareDefaults(t *testing.T) {
	// 零值 budget 的 nil map 初始化。
	budget := &GatewayKeyModelFailureBudget{}
	if !budget.Claim("w14g-hash") {
		t.Fatalf("零值 budget 首次 Claim 必须成功")
	}
	// Prepare 的 Scheduler/Logger 缺省分支。
	store := NewInMemoryKeyModelRuntimeStore(NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	prep, err := PrepareGatewayKeyModelAttempt(context.Background(), store, PrepareGatewayKeyModelAttemptInput{
		Route: GatewayKeyModelCapability{AccountID: "acc", Capability: testCapability()},
		RequestID: "req", AttemptID: "att", FailureBudget: budget,
		Clock: NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	})
	if err != nil || prep.Status != AttemptPreparationAdmitted {
		t.Fatalf("prepare = %+v err=%v", prep, err)
	}
}

func TestW14GRecoveryRunnerDefaultsAndLostCtx(t *testing.T) {
	// 全缺省构造：Probe/Now 缺省分支。
	runner := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{})
	if runner.probe == nil || runner.now == nil {
		t.Fatalf("缺省 probe/now 必须被填充")
	}
	// 父 ctx 已取消时 runProbeWithLease 返回 unknown。
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	runner2 := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{
		Probe: func(input KeyModelRecoveryProbeInput) KeyModelOutcome {
			close(probeStarted)
			<-releaseProbe
			return KeyModelOutcomeCompleteSuccess
		},
		Now: func() int64 { return 1000 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	go func() {
		<-probeStarted
		// ctx 已取消，runProbeWithLease 应在 probe 返回前以 unknown 结束。
	}()
	outcome := runner2.runProbeWithLease(ctx, KeyModelState{}, KeyModelRecoveryTarget{AccountID: "a", GroupID: "g", SystemAccountID: "s"}, "w14g-lease")
	close(releaseProbe)
	if outcome != KeyModelOutcomeUnknown {
		t.Fatalf("取消后的 outcome = %s", outcome)
	}
}

// ---------------------------------------------------------------------------
// keymodelredis.go：EvalRunner 注入损坏返回值
// ---------------------------------------------------------------------------

func w14gRedisStoreWithRunner(t *testing.T, runner func(script string, keys []string, args []string) (any, error)) *RedisKeyModelRuntimeStore {
	t.Helper()
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL: "redis://127.0.0.1:1/0", Namespace: "w14g-ns", EvalRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestW14GRedisStoreEvalCorruptionBranches(t *testing.T) {
	ctx := context.Background()
	fence := KeyModelFenceReference{CapabilityHash: w14gCapabilityHash(t), KeyFingerprint: "fp", OwnerID: "owner", DispatchRevision: 1}
	steps := []struct {
		name   string
		runner func(script string, keys []string, args []string) (any, error)
		act    func(store *RedisKeyModelRuntimeStore) error
	}{
		{"clear eval 失败", func(string, []string, []string) (any, error) { return nil, w14gErr }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.ClearMainProbeFence(ctx, fence, "fp")
			return err
		}},
		{"clear 非数组", func(string, []string, []string) (any, error) { return "nope", nil }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.ClearMainProbeFence(ctx, fence, "fp")
			return err
		}},
		{"clear 非法整数", func(string, []string, []string) (any, error) { return []any{"x"}, nil }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.ClearMainProbeFence(ctx, fence, "fp")
			return err
		}},
		{"defer eval 失败", func(string, []string, []string) (any, error) { return nil, w14gErr }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.DeferMainProbeFence(ctx, fence)
			return err
		}},
		{"defer 非数组", func(string, []string, []string) (any, error) { return 7, nil }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.DeferMainProbeFence(ctx, fence)
			return err
		}},
		{"defer 非法整数", func(string, []string, []string) (any, error) { return []any{nil}, nil }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.DeferMainProbeFence(ctx, fence)
			return err
		}},
		{"j1 eval 失败", func(string, []string, []string) (any, error) { return nil, w14gErr }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.ClaimJ1Confirmation(ctx, "src", 1)
			return err
		}},
		{"j1 非数组", func(string, []string, []string) (any, error) { return map[string]any{}, nil }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.ClaimJ1Confirmation(ctx, "src", 1)
			return err
		}},
		{"j1 非字符串", func(string, []string, []string) (any, error) { return []any{123}, nil }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.ClaimJ1Confirmation(ctx, "src", 1)
			return err
		}},
		{"release 非数组", func(string, []string, []string) (any, error) { return "bad", nil }, func(store *RedisKeyModelRuntimeStore) error {
			_, err := store.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: w14gCapabilityHash(t), AttemptID: "a"})
			return err
		}},
	}
	for _, step := range steps {
		store := w14gRedisStoreWithRunner(t, step.runner)
		if err := step.act(store); err == nil {
			t.Fatalf("%s 必须失败", step.name)
		}
	}
}

// ---------------------------------------------------------------------------
// apikeytransient.go
// ---------------------------------------------------------------------------

func TestW14GTransientStoreBranches(t *testing.T) {
	store, server := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	// 携带 KeyIndex 的目标（走 keyIndex 格式化分支）后关闭 Redis。
	keyIndex := 2
	target := AccountApiKeyTransientTarget{AccountID: "acc", KeyFingerprint: "fp", KeyIndex: &keyIndex}
	server.Close()
	if _, err := store.mutate(ctx, transientMutationArgs{operation: "record_failure", target: target}); err == nil {
		t.Fatalf("关闭 Redis 后 mutate 必须失败")
	}
	// 关闭 Redis 后 LoadMany eval 失败。
	if _, err := store.LoadMany(ctx, "acc", []string{"fp"}); err == nil {
		t.Fatalf("关闭 Redis 后 LoadMany 必须失败")
	}
	// 缺失 keys 的 LoadMany 正常返回（脚本会合成默认状态或空态）。
	live, _ := newTransientStoreForTest(t, 0)
	if _, err := live.LoadMany(ctx, "acc", []string{"w14g-missing"}); err != nil {
		t.Fatalf("缺失状态 LoadMany 失败: %v", err)
	}
}

// ---------------------------------------------------------------------------
// apikeyguard.go
// ---------------------------------------------------------------------------

func w14gGuardAccount() gatewayruntimecache.OpenAIAccountSecret {
	return guardTestAccount("w14g-acc", "fp-1")
}

func TestW14GGuardBranches(t *testing.T) {
	ctx := context.Background()
	// memory 驱动：非法账号捕获观测为 nil。
	memoryGuard, clock := newGuardForTest(t, "memory")
	if memoryGuard.CaptureFailureObservation(gatewayruntimecache.OpenAIAccountSecret{ID: "x"}) != nil {
		t.Fatalf("非法账号捕获观测必须为 nil")
	}
	// redis 驱动（无 store 工厂）：
	redisGuard, _ := newGuardForTest(t, "redis")
	if ok, err := redisGuard.RecordTransientFailure(ctx, w14gGuardAccount(), APIKeyStatusError); ok || err != nil {
		t.Fatalf("非 redis 驱动语义混用应短路: %v %v", ok, err)
	}
	_ = clock
	// LoadTransientStatesForDispatch 非 redis 驱动 → 本地状态。
	if states, err := memoryGuard.LoadTransientStatesForDispatch(ctx, "w14g-acc", []string{"fp-1"}); err != nil || states == nil {
		t.Fatalf("非 redis 驱动必须回退本地状态: %+v %v", states, err)
	}
	// 空账号 / 空 fingerprints。
	if states, err := redisGuard.LoadTransientStatesForDispatch(ctx, "  ", []string{"fp"}); err != nil || len(states) != 0 {
		t.Fatalf("空账号必须返回空")
	}
	if states, err := redisGuard.LoadTransientStatesForDispatch(ctx, "w14g-acc", nil); err != nil || len(states) != 0 {
		t.Fatalf("空指纹列表必须返回空")
	}
	// ClearTransientFailure：合法 target（带 generation）+ 缺 store 工厂 → 错误。
	generation := "gen-1"
	account := w14gGuardAccount()
	account.SelectedAPIKeyTransientGeneration = &generation
	if _, err := redisGuard.ClearTransientFailure(ctx, account); err == nil {
		t.Fatalf("缺 store 工厂必须失败")
	}
	// RecordSuccessGuard 非法账号 → false。
	if redisGuard.RecordSuccessGuard(gatewayruntimecache.OpenAIAccountSecret{ID: "x"}) {
		t.Fatalf("非法账号成功守卫必须为 false")
	}
	// SnapshotForTest redis 驱动 → 空。
	if snapshot := redisGuard.SnapshotForTest(); len(snapshot) != 0 {
		t.Fatalf("redis 驱动快照必须为空")
	}
	// 过期抑制项在快照中被跳过。
	target := AccountApiKeyRuntimeTarget{AccountID: "w14g-acc", KeyFingerprint: "fp-1"}
	memoryGuard.rememberLocalAPISuppressionLocked(target, APIKeyStatusError, nil)
	clock.Advance(time.Hour)
	if snapshot := memoryGuard.SnapshotForTest(); len(snapshot) != 0 {
		t.Fatalf("过期抑制不应出现在快照: %+v", snapshot)
	}
	// redis 驱动 rememberLocalAPISuppressionLocked 直接短路。
	redisGuard.rememberLocalAPISuppressionLocked(target, APIKeyStatusError, nil)
	if len(redisGuard.SnapshotForTest()) != 0 {
		t.Fatalf("redis 驱动不应保留本地抑制")
	}
	// 连续失败达到延迟档位上限（count 8 → delayIndex 7 被钳到末档）。
	for i := 0; i < 8; i++ {
		memoryGuard.rememberLocalAPISuppressionLocked(target, APIKeyStatusError, nil)
	}
	memoryGuard.mu.Lock()
	suppression := memoryGuard.suppressions["w14g-acc:fp-1"]
	memoryGuard.mu.Unlock()
	if suppression == nil || suppression.FailureCount != 8 {
		t.Fatalf("抑制计数 = %+v", suppression)
	}
	// transientStateStore 缺工厂错误。
	if _, err := redisGuard.transientStateStore(); err == nil {
		t.Fatalf("缺 store 工厂必须失败")
	}
	// 携带 index + generation 的 target 解析。
	index := 3
	full := w14gGuardAccount()
	full.SelectedAPIKeyIndex = &index
	full.SelectedAPIKeyTransientGeneration = &generation
	parsed, ok := accountAPIKeyRuntimeTarget(full)
	if !ok || parsed.KeyIndex == nil || *parsed.KeyIndex != 3 || parsed.TransientGeneration != generation {
		t.Fatalf("target 解析 = %+v ok=%v", parsed, ok)
	}
}

// ---------------------------------------------------------------------------
// sideeffects.go / policyavoidance.go / sideeffectqueue.go
// ---------------------------------------------------------------------------

func TestW14GSideEffectsServiceDefaults(t *testing.T) {
	service, err := NewSideEffectsService(SideEffectsConfig{}, SideEffectDeps{
		Writer: WriterFunc(func(context.Context, AccountSideEffectOperation) (AccountErrorHandlingResult, error) {
			return AccountErrorHandlingResult{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.clock == nil || service.random == nil {
		t.Fatalf("缺省 clock/random 必须被填充")
	}
	if delay := service.sideEffectRetryDelayMs(0); delay <= 0 {
		t.Fatalf("retry 0 的延迟 = %d", delay)
	}
}

func TestW14GPolicyAvoidanceBranches(t *testing.T) {
	ctx := context.Background()
	store := newFakePolicyAvoidanceStore()
	service := NewConfiguredPolicyAvoidanceService(store, nil, nil, NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	// authorized 访问缺 binding → runtime key 构造失败。
	if err := service.SuppressGatewayAccountLocallyForSeconds(ctx, SuppressibleGatewayAccount{ID: "w14g-a", AccountAccessType: "account_authorized"}, nil, "w14g"); err == nil {
		t.Fatalf("缺 binding 的授权账号必须失败")
	}
	// 列表读取失败。
	store.getErr = w14gErr
	if _, err := service.LoadConfiguredPolicyAvoidanceStates(ctx, []string{"w14g-key"}); !errors.Is(err, w14gErr) {
		t.Fatalf("读取失败必须透传: %v", err)
	}
	store.getErr = nil
	// 缺失键回读有效 JSON → 解码成功分支。
	store.values["w14g-key"] = []byte(`{"runtimeKey":"w14g-key","accountId":"a","reason":"r","startedAtMs":1,"untilMs":2}`)
	if _, err := service.LoadConfiguredPolicyAvoidanceStates(ctx, []string{"w14g-key"}); err != nil {
		t.Fatalf("有效 JSON 回读失败: %v", err)
	}
	// 缓存容量淘汰。
	for index := 0; index <= configuredPolicyAvoidanceCacheMaxEntries+1; index++ {
		service.rememberConfiguredPolicyAvoidanceState("w14g-key-"+string(rune('a'+index%26))+string(rune(index)), nil, 1000)
	}
	service.mu.Lock()
	oversized := len(service.cache) > configuredPolicyAvoidanceCacheMaxEntries
	service.mu.Unlock()
	if oversized {
		t.Fatalf("缓存容量未被淘汰")
	}
}

func TestW14GSideEffectQueueBranches(t *testing.T) {
	// parseRfc3339Instant：匹配格式但日期非法。
	if _, _, ok := parseRfc3339Instant("2026-13-99T99:99:99Z"); ok {
		t.Fatalf("非法日期必须解析失败")
	}
	// registry：现存条目 observedAt 损坏。
	registry, err := NewAccountSideEffectEpochRegistry(0)
	if err != nil {
		t.Fatal(err)
	}
	registry.byKey["w14g-bad"] = registry.current.PushBack(&registryEntry{key: "w14g-bad", epoch: AccountSideEffectEpoch{ObservedAt: "not-a-time"}})
	if _, err := registry.Observe("w14g-bad", EpochObservation{ObservedAt: "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatalf("损坏 observedAt 必须失败")
	}
	// compareSideEffectFailureAge：NextAttemptAtMs 排序分支。
	left := &QueuedAccountSideEffect{NextAttemptAtMs: 10}
	right := &QueuedAccountSideEffect{NextAttemptAtMs: 5}
	if compareSideEffectFailureAge(left, right) != 1 {
		t.Fatalf("较晚 next attempt 应返回 1")
	}
	// FindIndexByRuntimeKey / RemoveRuntimeKey / RemoveOldestFailure 的脏映射防御。
	queue := NewAccountSideEffectQueue()
	queue.itemsByRuntimeKey["w14g-stale"] = map[*QueuedAccountSideEffect]struct{}{{}: {}}
	if queue.FindIndexByRuntimeKey("w14g-stale") != -1 {
		t.Fatalf("脏映射必须返回 -1")
	}
	if removed := queue.RemoveRuntimeKey("w14g-stale"); len(removed) != 0 {
		t.Fatalf("脏映射必须移除为空")
	}
	orphan := &QueuedAccountSideEffect{NextAttemptAtMs: 1}
	queue.failuresByAge = append(queue.failuresByAge, orphan)
	if queue.RemoveOldestFailure() != nil {
		t.Fatalf("脏堆必须返回 nil")
	}
	queue.removeFailureByAge(&QueuedAccountSideEffect{})
	// rebalanceFailureAgeAt：上滤与下滤分支。
	queue2 := NewAccountSideEffectQueue()
	first := &QueuedAccountSideEffect{NextAttemptAtMs: 100}
	second := &QueuedAccountSideEffect{NextAttemptAtMs: 50}
	third := &QueuedAccountSideEffect{NextAttemptAtMs: 70}
	queue2.failuresByAge = []*QueuedAccountSideEffect{first, second, third}
	queue2.failureAgeIndexByItem = map[*QueuedAccountSideEffect]int{first: 0, second: 1, third: 2}
	queue2.rebalanceFailureAgeAt(2)
	if queue2.failuresByAge[0] == first {
		t.Fatalf("rebalance 应修复堆序")
	}
}
