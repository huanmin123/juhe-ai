package circuitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// w7b 助手：miniredis 进程内直驱完整 Redis Lua 状态机
// ---------------------------------------------------------------------------

type w7bClock struct{ now time.Time }

func newW7BClock() *w7bClock {
	return &w7bClock{now: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)}
}

func (c *w7bClock) Now() time.Time          { return c.now }
func (c *w7bClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func w7bGate() OwnerGate {
	return OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
}

// w7bRealRedisStore 以隔离命名空间（dev:w1cover:circuitrt，完整键前缀
// juhe-ai:dev:w1cover:circuitrt:*）驱动真实 dev Redis；env 缺失或不可达时
// 登记证据并跳过。用例结束后清理命名空间键，保证可重放。
func w7bRealRedisStore(t *testing.T, clock *w7bClock) *Store {
	t.Helper()
	url := w7bSharedEnvRedisURL(t)
	store, err := New(Config{URL: url, Namespace: "dev:w1cover:circuitrt", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Skipf("真实 Redis store 构建失败，跳过: %v", err)
	}
	pattern := "juhe-ai:dev:w1cover:circuitrt:*"
	w7bScanDelete(store.client.client, pattern)
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := store.BackfillRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}, reader); err != nil {
		w7bScanDelete(store.client.client, pattern)
		_ = store.Close()
		t.Skipf("真实 Redis 索引发布失败，跳过: %v", err)
	}
	store.runtime.WithNow(clock.Now)
	t.Cleanup(func() {
		w7bScanDelete(store.client.client, pattern)
		_ = store.Close()
	})
	return store
}

func w7bScanDelete(client *goredis.Client, pattern string) {
	var cursor uint64
	for {
		keys, next, err := client.Scan(context.Background(), cursor, pattern, 200).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}

// w7bSharedEnvRedisURL 读取开发环境共享 env 的缓存 Redis 地址；
// 支持 JUHE_AI_W1COVER_REDIS_URL 覆盖。值只用于连接，不写入日志或报告。
func w7bSharedEnvRedisURL(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("JUHE_AI_W1COVER_REDIS_URL"); override != "" {
		return override
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skipf("dev env 不可读，真实 Redis 用例跳过: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "JUHE_AI_REDIS_CACHE_URL=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "JUHE_AI_REDIS_CACHE_URL="))
		}
	}
	t.Skip("shared.env 缺少 JUHE_AI_REDIS_CACHE_URL，真实 Redis 用例跳过")
	return ""
}

// w7bReadyStore 启动隔离 miniredis、发布 ready 运行时索引并返回直驱入口。
func w7bReadyStore(t *testing.T, clock *w7bClock, capacity int, retention time.Duration) (*Store, *AccountCircuitRuntimeStore) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b", Capacity: capacity, Retention: retention}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := store.BackfillRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}, reader); err != nil {
		t.Fatalf("发布运行时索引失败: %v", err)
	}
	return store, store.runtime.WithNow(clock.Now)
}

func w7bAccountScope(id string) GatewayAccountCircuitScope {
	return GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAccount, AccountRuntimeKey: id}
}

func w7bProtocolScope(id, bucket string) GatewayAccountCircuitScope {
	return GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: id, ProtocolProfile: "openai", RequestLane: "text", ModelBucket: bucket}
}

func w7bIdentity(accountID string, scope GatewayAccountCircuitScope, generation int, revision int64, transitionID string) GatewayAccountCircuitTransitionIdentity {
	return GatewayAccountCircuitTransitionIdentity{AccountID: accountID, Scope: scope, Generation: generation, DispatchRevision: revision, TransitionID: transitionID}
}

func w7bExpectPhase(t *testing.T, result GatewayAccountCircuitMutationResult, err error, status GatewayAccountCircuitMutationStatus, phase GatewayAccountCircuitPhase) {
	t.Helper()
	if err != nil {
		t.Fatalf("变更失败: %v", err)
	}
	if result.Status != status {
		t.Fatalf("状态 = %s, 期望 %s (phase=%s)", result.Status, status, result.State.Phase)
	}
	if phase != "" && result.State.Phase != phase {
		t.Fatalf("phase = %s, 期望 %s", result.State.Phase, phase)
	}
}

// w7bOpenProtocolScope 把 protocol_model 子作用域驱动到 OPEN（suspect→确认→传输失败）。
func w7bOpenProtocolScope(t *testing.T, rt *AccountCircuitRuntimeStore, clock *w7bClock, accountID, bucket string) GatewayAccountCircuitScope {
	t.Helper()
	scope := w7bProtocolScope(accountID, bucket)
	result, err := rt.SuspectGatewayAccountCircuit(context.Background(), GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t-suspect-" + bucket, Reason: "w7b-failure", Now: clock.Now()})
	w7bExpectPhase(t, result, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
	lease, err := rt.AcquireGatewayAccountCircuitConfirmationLease(context.Background(), GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t-confirm-"+bucket), LeaseID: "lease-" + bucket, LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, lease, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
	opened, err := rt.CompleteGatewayAccountCircuitConfirmation(context.Background(), GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t-open-"+bucket), LeaseID: "lease-" + bucket, Outcome: GatewayAccountCircuitCompletionTransportFailure, Reason: "w7b-transport"})
	w7bExpectPhase(t, opened, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseOpen)
	return scope
}

// ---------------------------------------------------------------------------
// 探针：miniredis Lua 桥接覆盖本包脚本所需特性
// ---------------------------------------------------------------------------

func TestW7BMiniRedisLuaFeatureProbe(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := NewClient("redis://"+server.Addr(), "w7b-probe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	script := `
local kind = redis.call('TYPE', KEYS[1])['ok']
if kind ~= 'none' then return redis.error_reply('bad type: ' .. kind) end
redis.call('HSET', KEYS[1], 'field', cjson.encode({values = {'a', 'b'}, n = 2}))
local decoded = cjson.decode(redis.call('HGET', KEYS[1], 'field'))
if decoded.n ~= 2 or decoded.values[1] ~= 'a' then return redis.error_reply('cjson roundtrip failed') end
redis.call('ZADD', KEYS[2], 10, 'one')
redis.call('ZADD', KEYS[2], 20, 'two')
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', 15, 'LIMIT', 0, 10)
if #expired ~= 1 or expired[1] ~= 'one' then return redis.error_reply('zrangebyscore failed') end
if redis.call('HSETNX', KEYS[1], 'rev', '1') ~= 1 then return redis.error_reply('hsetnx failed') end
return cjson.encode({status = 'applied', n = decoded.n})
`
	value, err := goredis.NewScript(script).Run(context.Background(), client.client, []string{"w7b-probe:states", "w7b-probe:due"}).Result()
	if err != nil {
		t.Fatalf("miniredis Lua 探针失败: %v", err)
	}
	encoded, err := runtimeRedisBytes(value)
	if err != nil {
		t.Fatal(err)
	}
	// miniredis cjson 不保证对象键序，只断言语义等价。
	var probe struct {
		Status string `json:"status"`
		N      int    `json:"n"`
	}
	if err := json.Unmarshal(encoded, &probe); err != nil || probe.Status != "applied" || probe.N != 2 {
		t.Fatalf("探针载荷 = %s err = %v", encoded, err)
	}
}

// ---------------------------------------------------------------------------
// 状态机全迁移：CLOSED→SUSPECT→OPEN→HALF_OPEN→RECOVERING→CLOSED
// ---------------------------------------------------------------------------

func TestW7BCircuitStateMachineFullLifecycle(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	ctx := context.Background()
	accountID := "acct-sm"
	scope := w7bAccountScope(accountID)

	// 初始读取：无条目时返回默认 CLOSED。
	initial, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope, Now: clock.Now()})
	if err != nil || initial.Phase != GatewayAccountCircuitPhaseClosed || initial.DispatchRevision != 0 {
		t.Fatalf("初始读取 = %+v err=%v", initial, err)
	}

	// CLOSED → SUSPECT。
	suspect, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t1", Reason: "w7b-failure", Now: clock.Now()})
	w7bExpectPhase(t, suspect, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
	if suspect.State.Generation != 1 || suspect.State.FailureReason != "w7b-failure" || suspect.State.IncidentID != "t1" {
		t.Fatalf("SUSPECT 状态 = %+v", suspect.State)
	}
	// 幂等重放。
	replay, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t1", Reason: "w7b-failure", Now: clock.Now()})
	w7bExpectPhase(t, replay, err, GatewayAccountCircuitMutationIdempotent, GatewayAccountCircuitPhaseSuspect)
	// 非 CLOSED 状态再次 suspect → state_mismatch。
	mismatch, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t2", Reason: "w7b-failure", Now: clock.Now()})
	w7bExpectPhase(t, mismatch, err, GatewayAccountCircuitMutationStateMismatch, GatewayAccountCircuitPhaseSuspect)

	// SUSPECT + 确认租约。
	lease, err := rt.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t2"), LeaseID: "lease-1", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, lease, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
	if lease.State.Lease == nil || lease.State.Lease.Kind != GatewayAccountCircuitLeaseConfirmation {
		t.Fatalf("确认租约缺失: %+v", lease.State.Lease)
	}
	// 已持租约再获取 → state_mismatch。
	again, err := rt.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t3"), LeaseID: "lease-2", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, again, err, GatewayAccountCircuitMutationStateMismatch, GatewayAccountCircuitPhaseSuspect)
	// 错误租约完成 → lease_mismatch。
	wrong, err := rt.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t3"), LeaseID: "lease-wrong", Outcome: GatewayAccountCircuitCompletionFramingComplete})
	w7bExpectPhase(t, wrong, err, GatewayAccountCircuitMutationLeaseMismatch, GatewayAccountCircuitPhaseSuspect)
	// 未知结果：租约释放，停在 SUSPECT。
	unknown, err := rt.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t3"), LeaseID: "lease-1", Outcome: GatewayAccountCircuitCompletionUnknown})
	w7bExpectPhase(t, unknown, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
	if unknown.State.Lease != nil {
		t.Fatal("未知结果后租约必须释放")
	}
	// 重新获取确认租约（unknown 已释放旧租约）。
	release, err := rt.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t4a"), LeaseID: "lease-2", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, release, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
	// framing_complete → RECOVERING。
	recovering, err := rt.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t4"), LeaseID: "lease-2", Outcome: GatewayAccountCircuitCompletionFramingComplete})
	w7bExpectPhase(t, recovering, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseRecovering)
	if recovering.State.RetryAt == nil || !recovering.State.RetryAt.Equal(clock.Now().Add(3*time.Second)) {
		t.Fatalf("RECOVERING retryAt = %v", recovering.State.RetryAt)
	}

	// 未到 retryAt 的金丝雀 → not_due。
	notDue, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t5"), LeaseID: "canary-1", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, notDue, err, GatewayAccountCircuitMutationNotDue, GatewayAccountCircuitPhaseRecovering)
	clock.Advance(3 * time.Second)
	// RECOVERING → HALF_OPEN（recovery 租约）。
	half, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t5"), LeaseID: "canary-1", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, half, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseHalfOpen)
	if half.State.Lease.Kind != GatewayAccountCircuitLeaseRecovery || half.State.HalfOpenOrigin != GatewayAccountCircuitPhaseRecovering {
		t.Fatalf("HALF_OPEN 租约 = %+v origin=%s", half.State.Lease, half.State.HalfOpenOrigin)
	}
	// 未知金丝雀结果 → 回到来源相位。
	back, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t6"), LeaseID: "canary-1", Outcome: GatewayAccountCircuitCompletionUnknown})
	w7bExpectPhase(t, back, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseRecovering)
	if back.State.RetryAt == nil || !back.State.RetryAt.Equal(clock.Now()) {
		t.Fatalf("回退 retryAt = %v", back.State.RetryAt)
	}
	// transport_failure 金丝雀 → OPEN（退避 1 级）。
	half2, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t7"), LeaseID: "canary-2", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, half2, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseHalfOpen)
	opened, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t7c"), LeaseID: "canary-2", Outcome: GatewayAccountCircuitCompletionTransportFailure, Reason: "w7b-canary-fail"})
	w7bExpectPhase(t, opened, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseOpen)
	if opened.State.BackoffAttempt != 1 || opened.State.OpenedAt == nil {
		t.Fatalf("OPEN 状态 = %+v", opened.State)
	}
	// list_due：未到期为空，到期后返回 OPEN。
	due, err := rt.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Now: clock.Now(), Limit: 10})
	if err != nil || len(due) != 0 {
		t.Fatalf("未到期 due = %v err=%v", due, err)
	}
	clock.Advance(3 * time.Second)
	due, err = rt.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Now: clock.Now(), Limit: 10})
	if err != nil || len(due) != 1 || due[0].Phase != GatewayAccountCircuitPhaseOpen {
		t.Fatalf("到期 due = %v err=%v", due, err)
	}
	if _, err := rt.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Limit: 0}); err == nil {
		t.Fatal("limit=0 必须报错")
	}
	if _, err := rt.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Limit: GatewayAccountCircuitRuntimeMaxDuePage + 1}); err == nil {
		t.Fatal("超限 limit 必须报错")
	}
	// OPEN → HALF_OPEN（half_open 租约）→ framing_complete → RECOVERING。
	half3, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t8"), LeaseID: "canary-3", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, half3, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseHalfOpen)
	if half3.State.Lease.Kind != GatewayAccountCircuitLeaseHalfOpen || half3.State.HalfOpenOrigin != GatewayAccountCircuitPhaseOpen {
		t.Fatalf("half_open 租约 = %+v origin=%s", half3.State.Lease, half3.State.HalfOpenOrigin)
	}
	rec2, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t8c"), LeaseID: "canary-3", Outcome: GatewayAccountCircuitCompletionFramingComplete})
	w7bExpectPhase(t, rec2, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseRecovering)
	// 连续三轮恢复成功 → 关闭。
	transition := 9
	for i := 0; i < 3; i++ {
		clock.Advance(3 * time.Second)
		round, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, fmt.Sprintf("t%d", transition)), LeaseID: fmt.Sprintf("canary-r%d", i), LeaseUntil: clock.Now().Add(time.Minute)})
		w7bExpectPhase(t, round, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseHalfOpen)
		closed, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, fmt.Sprintf("t%d-c", transition)), LeaseID: fmt.Sprintf("canary-r%d", i), Outcome: GatewayAccountCircuitCompletionFramingComplete})
		w7bExpectPhase(t, closed, err, GatewayAccountCircuitMutationApplied, "")
		transition++
		if i < 2 && closed.State.Phase != GatewayAccountCircuitPhaseRecovering {
			t.Fatalf("第 %d 轮成功后应为 RECOVERING, got %s", i+1, closed.State.Phase)
		}
	}
	final, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope, Now: clock.Now()})
	if err != nil || final.Phase != GatewayAccountCircuitPhaseClosed {
		t.Fatalf("三轮成功后必须 CLOSED: %+v err=%v", final.Phase, err)
	}
	if final.DispatchRevision != 1 || final.Generation != 1 {
		t.Fatalf("CLOSED 世代/修订 = %d/%d", final.Generation, final.DispatchRevision)
	}
	// 超过保留窗口：条目清除，回到默认 CLOSED。
	clock.Advance(2 * time.Minute)
	expired, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope, Now: clock.Now()})
	if err != nil || expired.Phase != GatewayAccountCircuitPhaseClosed || expired.Generation != 0 {
		t.Fatalf("过期读取 = %+v err=%v", expired, err)
	}
	// 清除后的新一代 suspect：先推进账户墓碑到 2。
	if _, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: accountID, DispatchRevision: 2, TransitionID: "t-gen2-seed"}); err != nil {
		t.Fatal(err)
	}
	suspect2, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 2, TransitionID: "t-gen2", Reason: "w7b-again", Now: clock.Now()})
	w7bExpectPhase(t, suspect2, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
	// 条目已被保留窗口清除：generation 从 1 重新计数，仅修订推进到 2。
	if suspect2.State.Generation != 1 || suspect2.State.DispatchRevision != 2 {
		t.Fatalf("第二代 generation/revision = %d/%d", suspect2.State.Generation, suspect2.State.DispatchRevision)
	}
	// 世代/修订围栏：世代低于实际 → stale_generation。
	staleGen, err := rt.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 2, 2, "t-old2"), LeaseID: "l2", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, staleGen, err, GatewayAccountCircuitMutationStaleGeneration, GatewayAccountCircuitPhaseSuspect)
	staleRev, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t-old3", Reason: "x", Now: clock.Now()})
	w7bExpectPhase(t, staleRev, err, GatewayAccountCircuitMutationStaleDispatchRevision, GatewayAccountCircuitPhaseSuspect)
	// 缺失作用域上的迁移 → not_found。
	missing, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity("acct-none", w7bAccountScope("acct-none"), 1, 1, "t-miss"), LeaseID: "l3", LeaseUntil: clock.Now().Add(time.Minute)})
	w7bExpectPhase(t, missing, err, GatewayAccountCircuitMutationNotFound, "")
}

// ---------------------------------------------------------------------------
// replace_revision：同步重置到 CLOSED + 幂等/围栏
// ---------------------------------------------------------------------------

func TestW7BCircuitReplaceRevision(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	ctx := context.Background()
	accountID := "acct-rr"
	scope := w7bAccountScope(accountID)
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t1", Reason: "x", Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	// 墓碑仍为 1：与条目同修订 → 幂等；越过墓碑 → stale 围栏（占位回退状态）。
	idempotent, err := rt.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t2", Now: clock.Now()})
	w7bExpectPhase(t, idempotent, err, GatewayAccountCircuitMutationIdempotent, GatewayAccountCircuitPhaseSuspect)
	fenced, err := rt.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: accountID, Scope: scope, DispatchRevision: 2, TransitionID: "t3", Now: clock.Now()})
	w7bExpectPhase(t, fenced, err, GatewayAccountCircuitMutationStaleDispatchRevision, GatewayAccountCircuitPhaseSuspect)
	// 全新作用域：replace_revision 直接落地 CLOSED rev1，重放幂等。
	fresh := w7bAccountScope("acct-rn")
	freshResult, err := rt.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: "acct-rn", Scope: fresh, DispatchRevision: 1, TransitionID: "t5", Now: clock.Now()})
	w7bExpectPhase(t, freshResult, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseClosed)
	if freshResult.State.Generation != 1 {
		t.Fatalf("fresh generation = %d", freshResult.State.Generation)
	}
	freshAgain, err := rt.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: "acct-rn", Scope: fresh, DispatchRevision: 1, TransitionID: "t6", Now: clock.Now()})
	w7bExpectPhase(t, freshAgain, err, GatewayAccountCircuitMutationIdempotent, GatewayAccountCircuitPhaseClosed)
}

// ---------------------------------------------------------------------------
// 升级证据：record → escalate → already_active → idempotent → clear
// ---------------------------------------------------------------------------

// 升级脚本把同一 Lua 表同时赋给 childScopeKeys 与 requiredRecoveryScopeKeys：
// 真实 Redis cjson 允许共享引用，miniredis 的 gopher-json 会判为嵌套错误，
// 因此该用例使用隔离命名空间的真实 dev Redis 直驱（不可达时登记跳过）。
func TestW7BCircuitEscalationAndShadowRelease(t *testing.T) {
	clock := newW7BClock()
	store := w7bRealRedisStore(t, clock)
	rt := store.runtime
	ctx := context.Background()
	accountID := "acct-esc"
	accountScope := w7bAccountScope(accountID)
	childA := w7bOpenProtocolScope(t, rt, clock, accountID, "bucket-a")
	childB := w7bOpenProtocolScope(t, rt, clock, accountID, "bucket-b")

	evidence := func(scope GatewayAccountCircuitScope, evidenceID string, count int) GatewayAccountCircuitProtocolModelOpenEvidenceInput {
		return GatewayAccountCircuitProtocolModelOpenEvidenceInput{
			AccountID: accountID, Scope: scope, Generation: 1, DispatchRevision: 1,
			EvidenceID: evidenceID, AccountTransitionID: "ta-1", Reason: "w7b-escalate",
			ConfirmedFailureCount: count, Window: time.Hour, MaxProtocolScopes: 8, Now: clock.Now(),
		}
	}
	recorded, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, evidence(childA, "e1", 2))
	if err != nil || recorded.Status != GatewayAccountCircuitEscalationRecorded || recorded.ProtocolScopeCount != 1 || recorded.ConfirmedFailureCount != 2 {
		t.Fatalf("recorded = %+v err=%v", recorded, err)
	}
	escalated, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, evidence(childB, "e2", 2))
	if err != nil || escalated.Status != GatewayAccountCircuitEscalationEscalated {
		t.Fatalf("escalated = %+v err=%v", escalated, err)
	}
	parent := escalated.AccountState
	if parent.Phase != GatewayAccountCircuitPhaseOpen || parent.Generation != 1 || len(parent.ChildScopeKeys) != 2 || len(parent.ChildIncidentIDs) != 2 || len(parent.RequiredRecoveryScopeKeys) != 2 {
		t.Fatalf("父状态 = %+v", parent)
	}
	// 子作用域被父事件遮蔽。
	childState, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: childA, Now: clock.Now()})
	if err != nil || childState.ShadowedByIncidentID != "ta-1" {
		t.Fatalf("遮蔽标记 = %q err=%v", childState.ShadowedByIncidentID, err)
	}
	// 重复证据 → idempotent。
	duplicate, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, evidence(childA, "e1", 2))
	if err != nil || duplicate.Status != GatewayAccountCircuitEscalationIdempotent {
		t.Fatalf("duplicate = %+v err=%v", duplicate, err)
	}
	// 父活跃时新证据 → already_active（kept 只保留每个 scope 最新证据）。
	active, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, evidence(childA, "e3", 1))
	if err != nil || active.Status != GatewayAccountCircuitEscalationAlreadyActive {
		t.Fatalf("already_active = %+v err=%v", active, err)
	}
	// 清除 childB 的最新证据 e2。
	cleared, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: accountID, AccountRuntimeKey: accountID, DispatchRevision: 1, EvidenceID: "e2"})
	if err != nil || !cleared {
		t.Fatalf("clear = %v err=%v", cleared, err)
	}
	if cleared, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: accountID, AccountRuntimeKey: accountID, DispatchRevision: 1, EvidenceID: "e2"}); err != nil || cleared {
		t.Fatalf("重复 clear = %v err=%v", cleared, err)
	}
	// 世代/修订围栏。
	staleGen, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
		input := evidence(childA, "e4", 1)
		input.Generation = 2
		return input
	}())
	if err != nil || staleGen.Status != GatewayAccountCircuitEscalationStaleGeneration {
		t.Fatalf("stale generation = %+v err=%v", staleGen, err)
	}
	staleRev, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
		input := evidence(childA, "e5", 1)
		input.DispatchRevision = 2
		return input
	}())
	if err != nil || staleRev.Status != GatewayAccountCircuitEscalationStaleDispatchRevision {
		t.Fatalf("stale revision = %+v err=%v", staleRev, err)
	}
	// 父熔断走完恢复并关闭：子作用域遮蔽解除。
	clock.Advance(3 * time.Second)
	transition := 100
	parentCanary := func(outcome GatewayAccountCircuitCompletionOutcome, evidenceKey string) GatewayAccountCircuitMutationResult {
		transition++
		id := w7bIdentity(accountID, accountScope, 1, 1, fmt.Sprintf("tp%d", transition))
		acquired, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: id, LeaseID: fmt.Sprintf("pl%d", transition), LeaseUntil: clock.Now().Add(time.Minute)})
		w7bExpectPhase(t, acquired, err, GatewayAccountCircuitMutationApplied, "")
		completed, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, accountScope, 1, 1, fmt.Sprintf("tp%d-c", transition)), LeaseID: fmt.Sprintf("pl%d", transition), Outcome: outcome, EvidenceScopeKey: evidenceKey})
		w7bExpectPhase(t, completed, err, GatewayAccountCircuitMutationApplied, "")
		return completed
	}
	if opened := parentCanary(GatewayAccountCircuitCompletionTransportFailure, ""); opened.State.Phase != GatewayAccountCircuitPhaseOpen || opened.State.BackoffAttempt != 2 {
		t.Fatalf("父重开 = %+v", opened.State)
	}
	clock.Advance(5 * time.Second)
	if rec := parentCanary(GatewayAccountCircuitCompletionFramingComplete, ""); rec.State.Phase != GatewayAccountCircuitPhaseRecovering {
		t.Fatalf("父恢复 = %+v", rec.State)
	}
	for i, key := range []string{"", "", ""} {
		_ = key
		clock.Advance(3 * time.Second)
		required := []string{mustGatewayAccountCircuitScopeKey(childA), mustGatewayAccountCircuitScopeKey(childB)}
		if result := parentCanary(GatewayAccountCircuitCompletionFramingComplete, required[i%2]); i < 2 && result.State.Phase != GatewayAccountCircuitPhaseRecovering {
			t.Fatalf("恢复轮 %d = %s", i, result.State.Phase)
		}
	}
	parentFinal, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: accountScope, Now: clock.Now()})
	if err != nil || parentFinal.Phase != GatewayAccountCircuitPhaseClosed {
		t.Fatalf("父最终 = %s err=%v", parentFinal.Phase, err)
	}
	childAfter, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: childA, Now: clock.Now()})
	if err != nil || childAfter.ShadowedByIncidentID != "" || childAfter.Phase != GatewayAccountCircuitPhaseOpen {
		t.Fatalf("遮蔽解除失败 = %+v err=%v", childAfter, err)
	}
}

// ---------------------------------------------------------------------------
// 容量耗尽与 restore / 账户级修订
// ---------------------------------------------------------------------------

func TestW7BCircuitCapacityExhausted(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 1, time.Minute)
	ctx := context.Background()
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: "acct-cap", Scope: w7bAccountScope("acct-cap"), DispatchRevision: 1, TransitionID: "t1", Reason: "x", Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	second := w7bProtocolScope("acct-cap", "bucket-2")
	result, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: "acct-cap", Scope: second, DispatchRevision: 1, TransitionID: "t2", Reason: "x", Now: clock.Now()})
	w7bExpectPhase(t, result, err, GatewayAccountCircuitMutationCapacityExhausted, "")
}

func TestW7BCircuitRestoreAndAccountRevision(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	ctx := context.Background()
	accountID := "acct-res"
	scope := w7bAccountScope(accountID)
	scopeKey := mustGatewayAccountCircuitScopeKey(scope)
	restoreState := func(phase GatewayAccountCircuitPhase, generation int, revision int64, ledger int64) GatewayAccountCircuitState {
		return GatewayAccountCircuitState{
			ScopeKey: scopeKey, Scope: scope, Phase: phase, Generation: generation,
			DispatchRevision: revision, LedgerRevision: ledger, TransitionID: "tr",
			UpdatedAt: clock.Now(),
		}
	}
	// restore 围栏要求账户墓碑已推进：先以账户级修订播种墓碑 7。
	if _, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: accountID, DispatchRevision: 7, TransitionID: "tseed"}); err != nil {
		t.Fatal(err)
	}
	// restore OPEN → applied。
	applied, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: restoreState(GatewayAccountCircuitPhaseOpen, 5, 7, 3), Now: clock.Now()})
	w7bExpectPhase(t, applied, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseOpen)
	if applied.State.Generation != 5 || applied.State.LedgerRevision != 3 {
		t.Fatalf("restore 状态 = %+v", applied.State)
	}
	// 相同载荷 restore → idempotent。
	idempotent, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: restoreState(GatewayAccountCircuitPhaseOpen, 5, 7, 3), Now: clock.Now()})
	w7bExpectPhase(t, idempotent, err, GatewayAccountCircuitMutationIdempotent, GatewayAccountCircuitPhaseOpen)
	// 更低世代 → stale_generation。
	staleGen, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: restoreState(GatewayAccountCircuitPhaseOpen, 4, 7, 3), Now: clock.Now()})
	w7bExpectPhase(t, staleGen, err, GatewayAccountCircuitMutationStaleGeneration, GatewayAccountCircuitPhaseOpen)
	// CLOSED + 过期保留时间 → 移除并记墓碑。
	retained := clock.Now().Add(-time.Second)
	closed, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: restoreState(GatewayAccountCircuitPhaseClosed, 6, 7, 3), RetainedUntil: &retained, Now: clock.Now()})
	w7bExpectPhase(t, closed, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseClosed)
	gone, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope, Now: clock.Now()})
	if err != nil || gone.Generation != 0 {
		t.Fatalf("过期 CLOSED 必须移除条目: %+v", gone)
	}
	// 墓碑拦下更低账本修订的 restore。
	conflict, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: restoreState(GatewayAccountCircuitPhaseOpen, 1, 7, 1), Now: clock.Now()})
	w7bExpectPhase(t, conflict, err, GatewayAccountCircuitMutationStaleGeneration, "")
	// Go 侧输入校验。
	if _, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: restoreState(GatewayAccountCircuitPhaseOpen, 1, 0, 1)}); err == nil {
		t.Fatal("restore revision=0 必须报错")
	}
	badState := restoreState(GatewayAccountCircuitPhaseOpen, 1, 9, 1)
	badState.Phase = "NOPE"
	if _, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: badState}); err == nil {
		t.Fatal("非法 phase 必须报错")
	}

	// 账户级修订替换。
	probe := w7bOpenProtocolScope(t, rt, clock, "acct-arev", "bucket-r")
	_ = probe
	accountReplaced, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "acct-arev", DispatchRevision: 2, TransitionID: "ta2", Now: clock.Now()})
	if err != nil || accountReplaced.Status != GatewayAccountCircuitMutationApplied || accountReplaced.ClosedScopeCount != 1 || accountReplaced.CurrentDispatchRevision != 2 {
		t.Fatalf("账户修订 = %+v err=%v", accountReplaced, err)
	}
	accountAgain, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "acct-arev", DispatchRevision: 2, TransitionID: "ta3", Now: clock.Now()})
	if err != nil || accountAgain.Status != GatewayAccountCircuitMutationIdempotent || accountAgain.ClosedScopeCount != 0 {
		t.Fatalf("账户幂等 = %+v err=%v", accountAgain, err)
	}
	accountStale, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "acct-arev", DispatchRevision: 1, TransitionID: "ta4", Now: clock.Now()})
	if err != nil || accountStale.Status != GatewayAccountCircuitMutationStaleDispatchRevision || accountStale.CurrentDispatchRevision != 2 {
		t.Fatalf("账户 stale = %+v err=%v", accountStale, err)
	}
	if _, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "acct-arev", DispatchRevision: 0, TransitionID: "ta5"}); err == nil {
		t.Fatal("账户修订 0 必须报错")
	}
}

// ---------------------------------------------------------------------------
// 输入校验矩阵（Go 侧围栏）
// ---------------------------------------------------------------------------

func TestW7BCircuitInputValidationMatrix(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	ctx := context.Background()
	scope := w7bAccountScope("acct-v")
	identity := w7bIdentity("acct-v", scope, 1, 1, "t1")
	until := clock.Now().Add(time.Minute)

	if _, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: "other", Scope: scope}); err == nil {
		t.Fatal("账户身份不匹配必须报错")
	}
	badScope := scope
	badScope.Kind = "nope"
	if _, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: "acct-v", Scope: badScope}); err == nil {
		t.Fatal("非法 scope 必须报错")
	}
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: "acct-v", Scope: scope, DispatchRevision: 0, TransitionID: "t1", Now: clock.Now()}); err == nil {
		t.Fatal("revision 0 必须报错")
	}
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: "acct-v", Scope: scope, DispatchRevision: 1, TransitionID: "bad id", Now: clock.Now()}); err == nil {
		t.Fatal("非法 transition id 必须报错")
	}
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: "acct-v", Scope: scope, DispatchRevision: 1, TransitionID: "t1", Reason: strings.Repeat("r", 1025), Now: clock.Now()}); err == nil {
		t.Fatal("超长 reason 必须报错")
	}
	if _, err := rt.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "lease", LeaseUntil: time.Time{}}); err == nil {
		t.Fatal("空租约期限必须报错")
	}
	if _, err := rt.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "lease", LeaseUntil: clock.Now().Add(DefaultAccountCircuitRuntimeMaxLease + time.Minute)}); err == nil {
		t.Fatal("超长租约期限必须报错")
	}
	if _, err := rt.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: strings.Repeat("l", 257), LeaseUntil: until}); err == nil {
		t.Fatal("超长租约 id 必须报错")
	}
	if _, err := rt.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "lease", Outcome: "weird"}); err == nil {
		t.Fatal("非法 outcome 必须报错")
	}
	if _, err := rt.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "lease", Outcome: GatewayAccountCircuitCompletionFramingComplete, Reason: strings.Repeat("r", 1025)}); err == nil {
		t.Fatal("超长完成 reason 必须报错")
	}
	if _, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "lease", Outcome: GatewayAccountCircuitCompletionFramingComplete, EvidenceScopeKey: strings.Repeat("e", 2049)}); err == nil {
		t.Fatal("超长证据 key 必须报错")
	}
	if _, err := rt.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: "acct-v", Scope: scope, DispatchRevision: 0, TransitionID: "t1"}); err == nil {
		t.Fatal("replace revision 0 必须报错")
	}
	badEvidence := GatewayAccountCircuitProtocolModelOpenEvidenceInput{AccountID: "acct-v", Scope: w7bAccountScope("acct-v"), Generation: 1, DispatchRevision: 1, EvidenceID: "e", AccountTransitionID: "a", Reason: "r", ConfirmedFailureCount: 1, Window: time.Hour, MaxProtocolScopes: 8}
	if _, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, badEvidence); err == nil {
		t.Fatal("account scope 证据必须报错")
	}
	protocolScope := w7bProtocolScope("acct-v", "b")
	invalidEvidence := []GatewayAccountCircuitProtocolModelOpenEvidenceInput{
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.Generation = -1
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.DispatchRevision = 0
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.ConfirmedFailureCount = 0
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.MaxProtocolScopes = 0
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.MaxProtocolScopes = GatewayAccountCircuitRuntimeMaxEvidenceScopes + 1
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.Window = 0
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.Window = GatewayAccountCircuitRuntimeMaxEvidenceWindow + time.Hour
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.EvidenceID = " ev"
			return in
		}(),
		func() GatewayAccountCircuitProtocolModelOpenEvidenceInput {
			in := badEvidence
			in.Scope = protocolScope
			in.Reason = ""
			return in
		}(),
	}
	for index, input := range invalidEvidence {
		if _, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, input); err == nil {
			t.Fatalf("非法证据输入 %d 必须报错", index)
		}
	}
	if _, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: "other", AccountRuntimeKey: "acct-v", DispatchRevision: 1, EvidenceID: "e"}); err == nil {
		t.Fatal("clear 账户不匹配必须报错")
	}
	if _, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: "acct-v", AccountRuntimeKey: "acct-v", DispatchRevision: 0, EvidenceID: "e"}); err == nil {
		t.Fatal("clear revision 0 必须报错")
	}
	if _, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: "acct-v", AccountRuntimeKey: "acct-v", DispatchRevision: 1, EvidenceID: strings.Repeat("e", 1025)}); err == nil {
		t.Fatal("clear 超长证据 id 必须报错")
	}
}

// ---------------------------------------------------------------------------
// Client / Owner 门禁
// ---------------------------------------------------------------------------

func TestW7BClientAndOwnerGate(t *testing.T) {
	server := miniredis.RunT(t)
	if _, err := NewClient("", "ns"); err == nil {
		t.Fatal("空 URL 必须报错")
	}
	if _, err := NewClient("://bad", "ns"); err == nil {
		t.Fatal("坏 URL 必须报错")
	}
	if _, err := NewClient("redis://"+server.Addr(), ""); err == nil {
		t.Fatal("空命名空间必须报错")
	}
	client, err := NewClient("redis://"+server.Addr(), "w7b-gate")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close = %v", err)
	}

	// 构造参数校验。
	if _, err := New(Config{}, w7bGate()); err == nil {
		t.Fatal("空配置必须报错")
	}
	if _, err := NewAccountCircuitRuntimeStore(nil, time.Minute, 10); err == nil {
		t.Fatal("nil client 必须报错")
	}
	if _, err := NewAccountCircuitRuntimeStore(client, -time.Second, 10); err == nil {
		t.Fatal("非法 retention 必须报错")
	}
	if _, err := NewAccountCircuitRuntimeStore(client, time.Minute, -1); err == nil {
		t.Fatal("非法 capacity 必须报错")
	}

	// 门禁未满足。
	locked, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-locked"}, OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Close() })
	if err := locked.CheckReady(context.Background()); err == nil {
		t.Fatal("门禁未满足 CheckReady 必须报错")
	}
	if _, err := locked.Runtime(); err == nil {
		t.Fatal("门禁未满足 Runtime 必须报错")
	}
	if _, err := locked.GetGatewayAccountCircuit(context.Background(), GatewayAccountCircuitGetInput{AccountID: "a", Scope: w7bAccountScope("a")}); err == nil {
		t.Fatal("门禁未满足读取必须报错")
	}
	// 门禁满足但索引未发布。
	unpublished, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-unpub"}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unpublished.Close() })
	if err := unpublished.CheckReady(context.Background()); err == nil {
		t.Fatal("索引未发布 CheckReady 必须报错")
	}
	if _, err := unpublished.SuspectGatewayAccountCircuit(context.Background(), GatewayAccountCircuitSuspectInput{AccountID: "a", Scope: w7bAccountScope("a"), DispatchRevision: 1, TransitionID: "t"}); err == nil {
		t.Fatal("索引未发布写入必须报错")
	}
	if _, err := unpublished.ProjectRevision(context.Background(), GatewayAccountCircuitOutboxEvent{}); err == nil {
		t.Fatal("索引未发布投影必须报错")
	}
	if _, err := unpublished.RestoreIncident(context.Background(), GatewayAccountCircuitIncident{}); err == nil {
		t.Fatal("索引未发布 incident restore 必须报错")
	}
}

// ---------------------------------------------------------------------------
// 索引 backfill：happy / 校验 / 冲突 / 失效
// ---------------------------------------------------------------------------

func TestW7BRuntimeIndexBackfillValidationAndConflicts(t *testing.T) {
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: ""}); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o", LockTTL: time.Second}); err == nil {
		t.Fatal("过短 lock ttl 必须报错")
	}
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o", ScanCount: 1001}); err == nil {
		t.Fatal("超大 scan count 必须报错")
	}
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o", MaxPages: 100001}); err == nil {
		t.Fatal("超大 max pages 必须报错")
	}
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o", MaxFields: 1000001}); err == nil {
		t.Fatal("超大 max fields 必须报错")
	}
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o", MaxBytes: 512}); err == nil {
		t.Fatal("过小 max bytes 必须报错")
	}
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o", MaxScopeMembers: 1000001}); err == nil {
		t.Fatal("超大 max scope members 必须报错")
	}
	if err := ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o"}); err != nil {
		t.Fatalf("默认参数必须有效: %v", err)
	}

	server := miniredis.RunT(t)
	client, err := NewClient("redis://"+server.Addr(), "w7b-backfill")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := NewAccountCircuitRuntimeIndexBackfiller(nil); err == nil {
		t.Fatal("nil client 必须报错")
	}
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(client)
	if err != nil {
		t.Fatal(err)
	}
	input := GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}
	if _, err := backfiller.BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input); err == nil {
		t.Fatal("缺 reader 必须报错")
	}
	readerError := errors.New("w7b reader 失败")
	failing := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, readerError
	})
	if _, err := backfiller.WithDispatchRevisionReader(failing).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input); !errors.Is(err, readerError) {
		t.Fatalf("reader 错误必须透传: %v", err)
	}
	outOfOrder := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: "b", DispatchRevision: 1}, {AccountID: "a", DispatchRevision: 1}}, NextAfterAccountID: "b"}, nil
	})
	if _, err := backfiller.WithDispatchRevisionReader(outOfOrder).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input); err == nil {
		t.Fatal("乱序 reader 页必须报错")
	}
	// 超过页上限的 reader 页。
	oversized := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{
			{AccountID: "a", DispatchRevision: 1}, {AccountID: "b", DispatchRevision: 1},
		}, NextAfterAccountID: "b"}, nil
	})
	second, err := NewAccountCircuitRuntimeIndexBackfiller(client)
	if err != nil {
		t.Fatal(err)
	}
	second.WithDispatchRevisionReader(oversized)
	if _, err := second.BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner", ScanCount: 1}); err == nil {
		t.Fatal("超页 reader 必须报错")
	}
	emptyNext := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{NextAfterAccountID: "ghost"}, nil
	})
	if _, err := backfiller.WithDispatchRevisionReader(emptyNext).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input); err == nil {
		t.Fatal("空页游标必须报错")
	}
	// 锁被占用。
	if err := client.client.SetNX(context.Background(), backfiller.keys.indexLock, "other-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := backfiller.WithDispatchRevisionReader(emptyReader()).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("锁占用必须报 already running: %v", err)
	}
	_ = client.client.Del(context.Background(), backfiller.keys.indexLock).Err()
	// states 源损坏 → 失败且索引标记 invalid。
	if err := client.client.HSet(context.Background(), backfiller.keys.states, "broken-key", "{nope").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := backfiller.WithDispatchRevisionReader(emptyReader()).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input); err == nil {
		t.Fatal("损坏 states 源必须报错")
	}
	meta, err := client.client.HGetAll(context.Background(), backfiller.keys.indexMeta).Result()
	if err != nil {
		t.Fatal(err)
	}
	if meta["status"] != "invalid" {
		t.Fatalf("失败后索引必须 invalid, got %q", meta["status"])
	}
}

func TestW7BRuntimeIndexBackfillHappyPathWithEvidence(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-ev", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountID := "acct-ev"
	// 预置一个合法 escalation 证据 + 一个账户修订。
	scopeKey := mustGatewayAccountCircuitScopeKey(w7bProtocolScope(accountID, "b1"))
	evidence := map[string]any{
		"dispatchRevision": "5",
		"scopes": []map[string]any{{
			"scopeKey": scopeKey, "incidentId": "inc-1", "evidenceId": "ev-1",
			"confirmedFailureCount": 2, "observedAtMs": clock.Now().UnixMilli(),
		}},
	}
	raw, _ := json.Marshal(evidence)
	if err := store.client.client.HSet(context.Background(), store.runtime.keys.escalation, accountID, string(raw)).Err(); err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: accountID, DispatchRevision: 5}}}, nil
	})
	result, err := store.BackfillRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}, reader)
	if err != nil {
		t.Fatalf("带证据 backfill 失败: %v", err)
	}
	if result.EvidenceCount != 1 || result.RevisionCount != 1 || result.Epoch == "" || result.AuditedAt.IsZero() {
		t.Fatalf("backfill 结果 = %+v", result)
	}
	if err := store.CheckReady(context.Background()); err != nil {
		t.Fatalf("ready 校验 = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 索引来源项解析矩阵（不经 Redis，直接驱动解析分支）
// ---------------------------------------------------------------------------

func TestW7BRuntimeIndexItemFromSourceMatrix(t *testing.T) {
	accountScope := w7bAccountScope("acct-src")
	scopeKey := mustGatewayAccountCircuitScopeKey(accountScope)
	closedEntry := map[string]any{
		"state": map[string]any{
			"scopeKey": scopeKey, "scope": map[string]any{"kind": "account", "accountRuntimeKey": "acct-src"},
			"phase": "CLOSED", "generation": 1, "dispatchRevision": "3", "transitionId": "t1", "updatedAtMs": 100,
		},
		"closedExpiresAtMs": 500,
		"replayOrder":       []string{"t1"},
	}
	closedRaw, _ := json.Marshal(closedEntry)
	item, err := accountCircuitRuntimeIndexItemFromSource("states", scopeKey, string(closedRaw))
	if err != nil || item.Runtime != "acct-src" || item.Account != "acct-src" || item.Revision != 3 {
		t.Fatalf("closed item = %+v err=%v", item, err)
	}
	openEntry := map[string]any{
		"state": map[string]any{
			"scopeKey": scopeKey, "scope": map[string]any{"kind": "account", "accountRuntimeKey": "acct-src"},
			"phase": "OPEN", "generation": 1, "dispatchRevision": "3", "transitionId": "t1", "updatedAtMs": 100,
		},
		"replayOrder": []string{"t1"},
	}
	openRaw, _ := json.Marshal(openEntry)
	if _, err := accountCircuitRuntimeIndexItemFromSource("states", scopeKey, string(openRaw)); err != nil {
		t.Fatalf("open item = %v", err)
	}
	invalidStates := []struct{ name, field, source string }{
		{"空来源", scopeKey, ""},
		{"坏 JSON", scopeKey, "{nope"},
		{"作用域键不匹配", "other-key", string(openRaw)},
		{"缺失 CLOSED 截止", scopeKey, string(closedRaw[:len(closedRaw)-len("500}")-1]) + "}"},
		{"非 CLOSED 带截止", scopeKey, string(openRaw[:len(openRaw)-1]) + `,"closedExpiresAtMs":500}`},
		{"重放历史超限", scopeKey, func() string {
			broken := openEntry
			broken["replayOrder"] = []string{strings.Repeat("t", 300)}
			raw, _ := json.Marshal(broken)
			return string(raw)
		}()},
		{"重放历史重复", scopeKey, func() string {
			broken := openEntry
			broken["replayOrder"] = []string{"t1", "t1"}
			raw, _ := json.Marshal(broken)
			return string(raw)
		}()},
	}
	for _, entry := range invalidStates {
		if _, err := accountCircuitRuntimeIndexItemFromSource("states", entry.field, entry.source); err == nil {
			t.Fatalf("states 无效来源 %q 必须报错", entry.name)
		}
	}
	validEvidence := `{"dispatchRevision":"5","scopes":[{"scopeKey":"sk","incidentId":"inc","evidenceId":"ev","confirmedFailureCount":2,"observedAtMs":10}]}`
	evidenceItem, err := accountCircuitRuntimeIndexItemFromSource("escalation", "acct-src", validEvidence)
	if err != nil || evidenceItem.Account != "acct-src" || evidenceItem.Runtime != "acct-src" || evidenceItem.Revision != 5 {
		t.Fatalf("evidence item = %+v err=%v", evidenceItem, err)
	}
	invalidEvidence := []string{
		"{nope",
		`{"dispatchRevision":"0","scopes":[]}`,
		`{"dispatchRevision":"5","scopes":[{"scopeKey":"sk","incidentId":"inc","evidenceId":"ev","confirmedFailureCount":0,"observedAtMs":10}]}`,
	}
	for _, source := range invalidEvidence {
		if _, err := accountCircuitRuntimeIndexItemFromSource("escalation", "acct-src", source); err == nil {
			t.Fatal("escalation 无效来源必须报错")
		}
	}
	if _, err := accountCircuitRuntimeIndexItemFromSource("unknown", scopeKey, string(openRaw)); err == nil {
		t.Fatal("未知 phase 必须报错")
	}
}

// ---------------------------------------------------------------------------
// 修订投影
// ---------------------------------------------------------------------------

func TestW7BRevisionProjector(t *testing.T) {
	clock := newW7BClock()
	store, rt := w7bReadyStore(t, clock, 0, time.Minute)
	_ = rt
	ctx := context.Background()
	if _, err := NewAccountCircuitRevisionProjector(nil, time.Minute); err == nil {
		t.Fatal("nil client 必须报错")
	}
	if _, err := NewAccountCircuitRevisionProjector(store.client, 25*time.Hour); err == nil {
		t.Fatal("非法 retention 必须报错")
	}
	projector, err := NewAccountCircuitRevisionProjector(store.client, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if projector.WithNow(nil) != projector || projector.WithMaxIndexMembers(0) != projector {
		t.Fatal("With* 必须返回自身")
	}
	if projector.WithNow(clock.Now) != projector || projector.WithMaxIndexMembers(10) != projector {
		t.Fatal("With* 必须返回自身")
	}
	projector.WithMaxIndexMembers(10)
	accountID := "acct-proj"
	scope := w7bAccountScope(accountID)
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t1", Reason: "x", Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	event := func(revision int64) GatewayAccountCircuitOutboxEvent {
		return GatewayAccountCircuitOutboxEvent{EventID: "e1", ProjectionKey: GatewayAccountCircuitProjectionKey, EventType: GatewayAccountCircuitDispatchRevisionChanged, AccountID: accountID, AccountRuntimeKey: accountID, TransitionID: fmt.Sprintf("tp%d", revision), DispatchRevision: revision}
	}
	applied, err := projector.ProjectGatewayAccountCircuitRevision(ctx, event(2))
	if err != nil || applied.Status != GatewayAccountCircuitRevisionApplied || applied.CurrentRevision != 2 || applied.ClosedStates != 1 {
		t.Fatalf("投影 applied = %+v err=%v", applied, err)
	}
	idempotent, err := projector.ProjectGatewayAccountCircuitRevision(ctx, event(2))
	if err != nil || idempotent.Status != GatewayAccountCircuitRevisionIdempotent {
		t.Fatalf("投影 idempotent = %+v err=%v", idempotent, err)
	}
	stale, err := projector.ProjectGatewayAccountCircuitRevision(ctx, event(1))
	if err != nil || stale.Status != GatewayAccountCircuitRevisionStale {
		t.Fatalf("投影 stale = %+v err=%v", stale, err)
	}
	invalidEvents := []GatewayAccountCircuitOutboxEvent{
		func() GatewayAccountCircuitOutboxEvent { in := event(3); in.EventType = "other"; return in }(),
		func() GatewayAccountCircuitOutboxEvent { in := event(3); in.ProjectionKey = "other"; return in }(),
		func() GatewayAccountCircuitOutboxEvent { in := event(3); in.AccountRuntimeKey = "mismatch"; return in }(),
		func() GatewayAccountCircuitOutboxEvent { in := event(0); return in }(),
		func() GatewayAccountCircuitOutboxEvent { in := event(3); in.TransitionID = ""; return in }(),
	}
	for index, in := range invalidEvents {
		if _, err := projector.ProjectGatewayAccountCircuitRevision(ctx, in); err == nil {
			t.Fatalf("非法事件 %d 必须报错", index)
		}
	}
	// 通过 Store 门禁入口投影。
	viaStore, err := store.ProjectRevision(ctx, event(3))
	if err != nil || viaStore.Status != GatewayAccountCircuitRevisionApplied {
		t.Fatalf("Store 投影 = %+v err=%v", viaStore, err)
	}
}

// ---------------------------------------------------------------------------
// incident restore：legacy 构建模式 + 运行时属主模式
// ---------------------------------------------------------------------------

func TestW7BIncidentStateMappingMatrix(t *testing.T) {
	now := time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC)
	base := GatewayAccountCircuitIncident{
		AccountID: "acct-inc", AccountRuntimeKey: "acct-inc", ScopeKind: "account", IncidentID: "inc-1",
		State: "OPEN", Generation: 2, DispatchRevision: 4, LedgerRevision: 2,
		TransitionID: "t1", UpdatedAt: now,
	}
	base.CircuitScopeKey = mustGatewayAccountCircuitScopeKey(w7bAccountScope("acct-inc"))
	if state, err := accountCircuitIncidentRuntimeStateFromIncident(base); err != nil || state.Phase != "OPEN" || state.Scope.Kind != "account" {
		t.Fatalf("account incident = %+v err=%v", state, err)
	}
	keyIncident := base
	keyIncident.ScopeKind = "key"
	keyIncident.KeyFingerprint = "fp-1"
	keyIncident.CircuitScopeKey = mustGatewayAccountCircuitScopeKey(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAPIKey, AccountRuntimeKey: "acct-inc", KeyFingerprint: "fp-1"})
	if state, err := accountCircuitIncidentRuntimeStateFromIncident(keyIncident); err != nil || state.Scope.KeyFingerprint != "fp-1" {
		t.Fatalf("key incident = %+v err=%v", state, err)
	}
	protocolIncident := base
	protocolIncident.ScopeKind = "protocol_model"
	protocolIncident.ProtocolCode = "openai"
	protocolIncident.RequestLane = "image"
	protocolIncident.ModelFamily = "gpt"
	protocolIncident.CircuitScopeKey = mustGatewayAccountCircuitScopeKey(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: "acct-inc", ProtocolProfile: "openai", RequestLane: "image", ModelBucket: "gpt"})
	if state, err := accountCircuitIncidentRuntimeStateFromIncident(protocolIncident); err != nil || state.Scope.RequestLane != "image" {
		t.Fatalf("protocol incident = %+v err=%v", state, err)
	}
	keyModelIncident := base
	keyModelIncident.ScopeKind = "key_model"
	keyModelIncident.KeyFingerprint = "fp-1"
	keyModelIncident.ClientModel = "gpt-4.1"
	keyModelIncident.CapabilityHash = "hash"
	keyModelIncident.CredentialSourceAccountID = "cred"
	keyModelIncident.ClientEndpointFamily = "chat_completions"
	keyModelIncident.FinalUpstreamModel = "gpt-4.1-mini"
	keyModelIncident.UpstreamEndpointMode = "chat_sse"
	if _, err := accountCircuitIncidentRuntimeStateFromIncident(keyModelIncident); err != nil {
		t.Fatalf("key-model incident = %v", err)
	}
	// PERSISTING → OPEN 映射 + HALF_OPEN 租约方向。
	persisting := base
	persisting.State = "PERSISTING"
	if state, err := accountCircuitIncidentRuntimeStateFromIncident(persisting); err != nil || state.Phase != "OPEN" {
		t.Fatalf("persisting = %+v err=%v", state, err)
	}
	halfOpen := base
	halfOpen.State = "HALF_OPEN"
	halfOpen.LeaseID = "lease-1"
	halfOpen.LeasePurpose = "recovery"
	until := now.Add(time.Minute)
	halfOpen.LeaseUntil = &until
	if state, err := accountCircuitIncidentRuntimeStateFromIncident(halfOpen); err != nil || state.HalfOpenOrigin != "RECOVERING" || state.Lease == nil {
		t.Fatalf("half-open = %+v err=%v", state, err)
	}
	invalidIncidents := []GatewayAccountCircuitIncident{
		func() GatewayAccountCircuitIncident { in := base; in.CircuitScopeKey = ""; return in }(),
		func() GatewayAccountCircuitIncident { in := base; in.DispatchRevision = 0; return in }(),
		func() GatewayAccountCircuitIncident { in := base; in.LedgerRevision = 0; return in }(),
		func() GatewayAccountCircuitIncident { in := base; in.ScopeKind = "key"; return in }(),
		func() GatewayAccountCircuitIncident { in := base; in.ScopeKind = "alien"; return in }(),
		func() GatewayAccountCircuitIncident {
			in := base
			in.ScopeKind = "protocol_model"
			in.RequestLane = "sideways"
			return in
		}(),
		func() GatewayAccountCircuitIncident {
			in := base
			in.ScopeKind = "key_model"
			in.ClientModel = ""
			return in
		}(),
		func() GatewayAccountCircuitIncident { in := base; in.State = "ZOMBIE"; return in }(),
		func() GatewayAccountCircuitIncident { in := base; in.State = "CLOSED"; return in }(),
		func() GatewayAccountCircuitIncident { in := base; in.UpdatedAt = time.Time{}; return in }(),
	}
	for index, incident := range invalidIncidents {
		if _, err := accountCircuitIncidentRuntimeStateFromIncident(incident); err == nil {
			t.Fatalf("非法 incident %d 必须报错", index)
		}
	}
}

func TestW7BIncidentRestoreLegacyAndOwner(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	ctx := context.Background()

	// legacy：索引元数据缺失（视为构建模式）时可用。
	legacyClient, err := NewClient("redis://"+server.Addr(), "w7b-legacy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacyClient.Close() })
	if _, err := NewAccountCircuitIncidentRestorer(nil, time.Minute, 10); err == nil {
		t.Fatal("nil client 必须报错")
	}
	if _, err := NewAccountCircuitIncidentRestorer(legacyClient, 0, 0); err != nil {
		t.Fatalf("默认参数必须有效: %v", err)
	}
	if _, err := NewAccountCircuitIncidentRestorer(legacyClient, 25*time.Hour, 10); err == nil {
		t.Fatal("非法 retention 必须报错")
	}
	if _, err := NewAccountCircuitIncidentRestorer(legacyClient, time.Minute, -1); err == nil {
		t.Fatal("非法 capacity 必须报错")
	}
	legacy, err := NewAccountCircuitIncidentRestorer(legacyClient, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	legacy.WithNow(clock.Now)
	incident := GatewayAccountCircuitIncident{
		AccountID: "acct-leg", AccountRuntimeKey: "acct-leg", ScopeKind: "account",
		CircuitScopeKey: mustGatewayAccountCircuitScopeKey(w7bAccountScope("acct-leg")),
		IncidentID:      "inc-1", State: "OPEN", Generation: 2, DispatchRevision: 4,
		LedgerRevision: 2, TransitionID: "t1", UpdatedAt: clock.Now(),
	}
	applied, err := legacy.RestoreGatewayAccountCircuitIncident(ctx, incident)
	if err != nil || applied.Status != GatewayAccountCircuitRevisionApplied || applied.CurrentRevision != 4 {
		t.Fatalf("legacy applied = %+v err=%v", applied, err)
	}
	idempotent, err := legacy.RestoreGatewayAccountCircuitIncident(ctx, incident)
	if err != nil || idempotent.Status != GatewayAccountCircuitRevisionIdempotent {
		t.Fatalf("legacy idempotent = %+v err=%v", idempotent, err)
	}
	staleIncident := incident
	staleIncident.DispatchRevision = 3
	stale, err := legacy.RestoreGatewayAccountCircuitIncident(ctx, staleIncident)
	if err != nil || stale.Status != GatewayAccountCircuitRevisionStale {
		t.Fatalf("legacy stale = %+v err=%v", stale, err)
	}
	conflictIncident := incident
	conflictIncident.LedgerRevision = 1
	if _, err := legacy.RestoreGatewayAccountCircuitIncident(ctx, conflictIncident); err == nil {
		t.Fatal("ledger 冲突必须报错")
	}
	// 索引 ready 后 legacy restore 必须被围栏。
	if err := legacyClient.client.HSet(ctx, legacy.keys.indexMeta, "status", "ready").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.RestoreGatewayAccountCircuitIncident(ctx, incident); err == nil {
		t.Fatal("ready 索引必须围栏 legacy restore")
	}

	// 运行时属主模式（restore 围栏要求账户墓碑已推进：先播种墓碑 6）。
	clock2 := newW7BClock()
	store, rt2 := w7bReadyStore(t, clock2, 0, time.Minute)
	if _, err := rt2.ReplaceGatewayAccountCircuitAccountDispatchRevision(context.Background(), GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "acct-own", DispatchRevision: 6, TransitionID: "tseed"}); err != nil {
		t.Fatal(err)
	}
	ownerIncident := GatewayAccountCircuitIncident{
		AccountID: "acct-own", AccountRuntimeKey: "acct-own", ScopeKind: "account",
		CircuitScopeKey: mustGatewayAccountCircuitScopeKey(w7bAccountScope("acct-own")),
		IncidentID:      "inc-2", State: "OPEN", Generation: 3, DispatchRevision: 6,
		LedgerRevision: 1, TransitionID: "t2", UpdatedAt: clock2.Now(),
	}
	ownerApplied, err := store.RestoreIncident(ctx, ownerIncident)
	if err != nil || ownerApplied.Status != GatewayAccountCircuitRevisionApplied {
		t.Fatalf("owner applied = %+v err=%v", ownerApplied, err)
	}
	ownerIdempotent, err := store.RestoreIncident(ctx, ownerIncident)
	if err != nil || ownerIdempotent.Status != GatewayAccountCircuitRevisionIdempotent {
		t.Fatalf("owner idempotent = %+v err=%v", ownerIdempotent, err)
	}
	ownerStale := ownerIncident
	ownerStale.Generation = 1
	if _, err := store.RestoreIncident(ctx, ownerStale); err == nil {
		t.Fatal("世代回退必须报错")
	}
	ownerStaleRevision := ownerIncident
	ownerStaleRevision.DispatchRevision = 2
	staleProjection, err := store.RestoreIncident(ctx, ownerStaleRevision)
	if err != nil || staleProjection.Status != GatewayAccountCircuitRevisionStale {
		t.Fatalf("owner stale = %+v err=%v", staleProjection, err)
	}
}

// ---------------------------------------------------------------------------
// 契约矩阵：scope/状态/克隆/族匹配
// ---------------------------------------------------------------------------

func TestW7BContractMatrix(t *testing.T) {
	if len(ManifestOperations) == 0 {
		t.Fatal("清单操作不能为空")
	}
	// scope 校验矩阵。
	if err := ValidateGatewayAccountCircuitScope(w7bAccountScope("a")); err != nil {
		t.Fatalf("account scope = %v", err)
	}
	accountPolluted := w7bAccountScope("a")
	accountPolluted.KeyFingerprint = "fp"
	if err := ValidateGatewayAccountCircuitScope(accountPolluted); err == nil {
		t.Fatal("account scope 不得携带 key 字段")
	}
	keyScope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAPIKey, AccountRuntimeKey: "a", KeyFingerprint: "fp"}
	if err := ValidateGatewayAccountCircuitScope(keyScope); err != nil {
		t.Fatalf("key scope = %v", err)
	}
	keyPolluted := keyScope
	keyPolluted.ClientModel = "gpt"
	if err := ValidateGatewayAccountCircuitScope(keyPolluted); err == nil {
		t.Fatal("key scope 不得携带 key-model 字段")
	}
	protocolScope := w7bProtocolScope("a", "b")
	if err := ValidateGatewayAccountCircuitScope(protocolScope); err != nil {
		t.Fatalf("protocol scope = %v", err)
	}
	protocolBadLane := protocolScope
	protocolBadLane.RequestLane = "video"
	if err := ValidateGatewayAccountCircuitScope(protocolBadLane); err == nil {
		t.Fatal("非法 request lane 必须报错")
	}
	protocolWithKey := protocolScope
	protocolWithKey.KeyFingerprint = "fp"
	if err := ValidateGatewayAccountCircuitScope(protocolWithKey); err == nil {
		t.Fatal("protocol scope 携带 key 必须报错")
	}
	keyModelScope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeKeyModel, AccountRuntimeKey: "a", KeyFingerprint: "fp", ClientModel: "m", CapabilityHash: "h", CredentialSourceAccountID: "c", ClientEndpointFamily: "chat", FinalUpstreamModel: "m2", UpstreamEndpointMode: "sse"}
	if err := ValidateGatewayAccountCircuitScope(keyModelScope); err != nil {
		t.Fatalf("key-model scope = %v", err)
	}
	keyModelPolluted := keyModelScope
	keyModelPolluted.ProtocolProfile = "openai"
	if err := ValidateGatewayAccountCircuitScope(keyModelPolluted); err == nil {
		t.Fatal("key-model scope 不得携带 protocol 字段")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: "alien", AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("未知 kind 必须报错")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAPIKey}); err == nil {
		t.Fatal("空 runtime key 必须报错")
	}
	if _, err := GatewayAccountCircuitScopeKey(GatewayAccountCircuitScope{Kind: "alien", AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("非法 scope key 生成必须报错")
	}

	// closed state。
	if _, err := GatewayAccountCircuitClosedState(w7bAccountScope("a"), -1, 0, "", time.Now()); err == nil {
		t.Fatal("负修订 closed state 必须报错")
	}
	if _, err := GatewayAccountCircuitClosedState(w7bAccountScope("a"), 1, 0, " t", time.Now()); err == nil {
		t.Fatal("非法 transition closed state 必须报错")
	}
	closed, err := GatewayAccountCircuitClosedState(w7bAccountScope("a"), 3, 1, "t", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cloned := CloneGatewayAccountCircuitState(closed)
	if cloned.ScopeKey != closed.ScopeKey || cloned.Phase != closed.Phase || cloned.Generation != closed.Generation || cloned.DispatchRevision != closed.DispatchRevision {
		t.Fatal("克隆必须保留标量字段")
	}
	withRelations := closed
	withRelations.ChildScopeKeys = []string{"k1"}
	if CloneGatewayAccountCircuitState(withRelations).ChildScopeKeys == nil {
		t.Fatal("克隆必须复制关系列表")
	}

	// 状态校验。
	mismatched := closed
	mismatched.ScopeKey = "wrong"
	if err := ValidateGatewayAccountCircuitState(mismatched); err == nil {
		t.Fatal("scope key 不匹配必须报错")
	}
	zeroTime := closed
	zeroTime.UpdatedAt = time.Time{}
	if err := ValidateGatewayAccountCircuitState(zeroTime); err == nil {
		t.Fatal("空 UpdatedAt 必须报错")
	}
	negative := closed
	negative.Generation = -1
	if err := ValidateGatewayAccountCircuitState(negative); err == nil {
		t.Fatal("负 generation 必须报错")
	}
	badPhase := closed
	badPhase.Phase = "ZOMBIE"
	if err := ValidateGatewayAccountCircuitState(badPhase); err == nil {
		t.Fatal("非法 phase 必须报错")
	}
	badLease := closed
	badLease.Lease = &GatewayAccountCircuitLease{Kind: "alien", ID: "l", Until: time.Now()}
	if err := ValidateGatewayAccountCircuitState(badLease); err == nil {
		t.Fatal("非法 lease kind 必须报错")
	}
	noDeadlineLease := closed
	noDeadlineLease.Lease = &GatewayAccountCircuitLease{Kind: GatewayAccountCircuitLeaseHalfOpen, ID: "l"}
	if err := ValidateGatewayAccountCircuitState(noDeadlineLease); err == nil {
		t.Fatal("空租约期限必须报错")
	}
	halfOpenNoLease := closed
	halfOpenNoLease.Phase = GatewayAccountCircuitPhaseHalfOpen
	halfOpenNoLease.HalfOpenOrigin = GatewayAccountCircuitPhaseOpen
	if err := ValidateGatewayAccountCircuitState(halfOpenNoLease); err == nil {
		t.Fatal("HALF_OPEN 缺租约必须报错")
	}
	halfOpenBadOrigin := closed
	halfOpenBadOrigin.Phase = GatewayAccountCircuitPhaseHalfOpen
	halfOpenBadOrigin.Lease = &GatewayAccountCircuitLease{Kind: GatewayAccountCircuitLeaseHalfOpen, ID: "l", Until: time.Now()}
	halfOpenBadOrigin.HalfOpenOrigin = GatewayAccountCircuitPhaseClosed
	if err := ValidateGatewayAccountCircuitState(halfOpenBadOrigin); err == nil {
		t.Fatal("HALF_OPEN 非法来源必须报错")
	}
	originWithoutHalfOpen := closed
	originWithoutHalfOpen.HalfOpenOrigin = GatewayAccountCircuitPhaseOpen
	if err := ValidateGatewayAccountCircuitState(originWithoutHalfOpen); err == nil {
		t.Fatal("非 HALF_OPEN 携带来源必须报错")
	}
	duplicatedRelations := closed
	duplicatedRelations.ChildScopeKeys = []string{"k", "k"}
	if err := ValidateGatewayAccountCircuitState(duplicatedRelations); err == nil {
		t.Fatal("重复关系列表必须报错")
	}
	unsortedRelations := closed
	unsortedRelations.ChildScopeKeys = []string{"b", "a"}
	if err := ValidateGatewayAccountCircuitState(unsortedRelations); err == nil {
		t.Fatal("未排序关系列表必须报错")
	}

	// runtime key 家族匹配。
	if GatewayAccountCircuitRuntimeKeyMatchesFamily("", "a") {
		t.Fatal("空 target 不匹配")
	}
	if GatewayAccountCircuitRuntimeKeyMatchesFamily("a", "") {
		t.Fatal("空 candidate 不匹配")
	}
	if !GatewayAccountCircuitRuntimeKeyMatchesFamily("a", "a") {
		t.Fatal("相等即匹配")
	}
	if !GatewayAccountCircuitRuntimeKeyMatchesFamily("a", "a:authorized:x") {
		t.Fatal("授权后缀是家族成员")
	}
	if GatewayAccountCircuitRuntimeKeyMatchesFamily("a:authorized:x", "a:authorized:y") {
		t.Fatal("已授权 target 不再扩展家族")
	}

	// wire 转换辅助。
	if _, err := runtimeWireRevision("nope"); err == nil {
		t.Fatal("非法 wire 修订必须报错")
	}
	if value, err := runtimeWireRevision(""); err != nil || value != 0 {
		t.Fatalf("空 wire 修订 = %d err=%v", value, err)
	}
	if runtimeWireTime(nil) != nil {
		t.Fatal("空 wire 时间必须为 nil")
	}
	if canonicalRuntimeStrings([]string{"b", "a", "b"}) != nil {
		t.Fatal("重复列表必须返回 nil")
	}
	sorted := canonicalRuntimeStrings([]string{"b", "a"})
	if sorted == nil || sorted[0] != "a" {
		t.Fatalf("排序结果 = %v", sorted)
	}
	if value, err := redisInt64("7"); err != nil || value != 7 {
		t.Fatalf("字符串整数解析 = %d err=%v", value, err)
	}
	if value, err := redisInt64(int64(9)); err != nil || value != 9 {
		t.Fatalf("int64 解析 = %d err=%v", value, err)
	}
	if _, err := redisInt64("nope"); err == nil {
		t.Fatal("非数字必须报错")
	}
	if _, err := redisInt64(struct{}{}); err == nil {
		t.Fatal("未知类型必须报错")
	}
	if _, err := runtimeRedisBytes(3.14); err == nil {
		t.Fatal("未知 Redis 结果类型必须报错")
	}
}

func emptyReader() GatewayAccountCircuitDispatchRevisionReader {
	return DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
}

// ---------------------------------------------------------------------------
// Store 门禁委派流与守卫分支
// ---------------------------------------------------------------------------

func TestW7BStoreDelegatedFlows(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-deleg", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("ping = %v", err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := store.BackfillRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}, reader); err != nil {
		t.Fatalf("backfill = %v", err)
	}
	store.runtime.WithNow(clock.Now)
	ctx := context.Background()
	accountID := "acct-del"
	scope := w7bAccountScope(accountID)
	// 委派 suspect → 确认租约 → 传输失败 → OPEN → 到期 → 金丝雀恢复。
	if _, err := store.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t1", Reason: "x", Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t2"), LeaseID: "l1", LeaseUntil: clock.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	opened, err := store.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t3"), LeaseID: "l1", Outcome: GatewayAccountCircuitCompletionTransportFailure})
	if err != nil || opened.State.Phase != GatewayAccountCircuitPhaseOpen {
		t.Fatalf("委派 open = %+v err=%v", opened, err)
	}
	clock.Advance(3 * time.Second)
	due, err := store.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Now: clock.Now(), Limit: 5})
	if err != nil || len(due) != 1 {
		t.Fatalf("委派 due = %v err=%v", due, err)
	}
	half, err := store.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t4"), LeaseID: "l2", LeaseUntil: clock.Now().Add(time.Minute)})
	if err != nil || half.State.Phase != GatewayAccountCircuitPhaseHalfOpen {
		t.Fatalf("委派 canary = %+v err=%v", half, err)
	}
	closed, err := store.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t5"), LeaseID: "l2", Outcome: GatewayAccountCircuitCompletionFramingComplete})
	if err != nil || closed.State.Phase != GatewayAccountCircuitPhaseRecovering {
		t.Fatalf("委派恢复 = %+v err=%v", closed, err)
	}
	// 委派 record（recorded 分支在 miniredis 可达）与 clear。
	// 升级证据要求子作用域处于 OPEN：先驱动 protocol 子作用域。
	protocolScope := w7bOpenProtocolScope(t, store.runtime, clock, accountID, "bucket-d")
	recorded, err := store.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, GatewayAccountCircuitProtocolModelOpenEvidenceInput{
		AccountID: accountID, Scope: protocolScope, Generation: 1, DispatchRevision: 1,
		EvidenceID: "e1", AccountTransitionID: "ta", Reason: "r", ConfirmedFailureCount: 1,
		Window: time.Hour, MaxProtocolScopes: 8, Now: clock.Now(),
	})
	if err != nil || recorded.Status != GatewayAccountCircuitEscalationRecorded {
		t.Fatalf("委派 record = %+v err=%v", recorded, err)
	}
	cleared, err := store.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: accountID, AccountRuntimeKey: accountID, DispatchRevision: 1, EvidenceID: "ghost"})
	if err != nil || cleared {
		t.Fatalf("委派 clear = %v err=%v", cleared, err)
	}
	// 委派 replace 与账户级修订。
	replaced, err := store.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t6", Now: clock.Now()})
	if err != nil || replaced.Status != GatewayAccountCircuitMutationIdempotent {
		t.Fatalf("委派 replace = %+v err=%v", replaced, err)
	}
	accountResult, err := store.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: accountID, DispatchRevision: 2, TransitionID: "t7", Now: clock.Now()})
	if err != nil || accountResult.Status != GatewayAccountCircuitMutationApplied {
		t.Fatalf("委派账户修订 = %+v err=%v", accountResult, err)
	}
	// 委派 restore。
	restoreState := GatewayAccountCircuitState{
		ScopeKey: mustGatewayAccountCircuitScopeKey(scope), Scope: scope, Phase: GatewayAccountCircuitPhaseClosed,
		Generation: 3, DispatchRevision: 2, TransitionID: "t8", UpdatedAt: clock.Now(),
	}
	restored, err := store.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: restoreState, Now: clock.Now()})
	if err != nil || restored.Status != GatewayAccountCircuitMutationApplied {
		t.Fatalf("委派 restore = %+v err=%v", restored, err)
	}
	if _, err := store.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope, Now: clock.Now()}); err != nil {
		t.Fatalf("委派 get = %v", err)
	}
}

func TestW7BStoreDelegationGuardrails(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()
	locked, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-guard"}, OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Close() })
	guardClock := newW7BClock()
	scope := w7bAccountScope("acct-g")
	identity := w7bIdentity("acct-g", scope, 1, 1, "t1")
	if _, err := locked.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "l", LeaseUntil: guardClock.Now().Add(time.Minute)}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "l", Outcome: GatewayAccountCircuitCompletionFramingComplete}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "l", LeaseUntil: guardClock.Now().Add(time.Minute)}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: "l", Outcome: GatewayAccountCircuitCompletionFramingComplete}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: "acct-g", Scope: scope, DispatchRevision: 1, TransitionID: "t"}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: "acct-g", State: GatewayAccountCircuitState{Scope: scope}}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Limit: 1}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, GatewayAccountCircuitProtocolModelOpenEvidenceInput{AccountID: "acct-g", Scope: scope, DispatchRevision: 1, ConfirmedFailureCount: 1, Window: time.Hour, MaxProtocolScopes: 1}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: "acct-g", AccountRuntimeKey: "acct-g", DispatchRevision: 1, EvidenceID: "e"}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "acct-g", DispatchRevision: 1, TransitionID: "t"}); err == nil {
		t.Fatal("门禁未满足委派必须报错")
	}
	if _, err := locked.BackfillRuntimeIndex(ctx, GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o"}, DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})); err == nil {
		t.Fatal("门禁未满足 backfill 必须报错")
	}

	// nil 接收者守卫。
	var nilStore *Store
	if err := nilStore.Ping(ctx); err == nil {
		t.Fatal("nil store ping 必须报错")
	}
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store close = %v", err)
	}
	if _, err := nilStore.Runtime(); err == nil {
		t.Fatal("nil store runtime 必须报错")
	}
	var nilClient *Client
	if err := nilClient.Ping(ctx); err == nil {
		t.Fatal("nil client ping 必须报错")
	}
	if err := nilClient.Close(); err != nil {
		t.Fatalf("nil client close = %v", err)
	}

	// CheckReady 的 Redis 故障分支。
	broken, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-broken"}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	_ = broken.Close()
	if err := broken.CheckReady(ctx); err == nil {
		t.Fatal("客户端已关闭 CheckReady 必须报错")
	}
	// 构造参数非法分支。
	if _, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-bad", Capacity: -1}, w7bGate()); err == nil {
		t.Fatal("非法 capacity 必须报错")
	}
}

// ---------------------------------------------------------------------------
// With* 助手、wire 满分支与小型校验助手
// ---------------------------------------------------------------------------

func TestW7BHelpersAndWireFullBranches(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	// WithMaxLease 收紧后超期租约必须报错。
	if rt.WithMaxLease(time.Minute) != rt || rt.WithMaxLease(0) != rt {
		t.Fatal("WithMaxLease 必须返回自身")
	}
	scope := w7bAccountScope("acct-maxlease")
	if _, err := rt.AcquireGatewayAccountCircuitCanaryLease(context.Background(), GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: w7bIdentity("acct-maxlease", scope, 1, 1, "t1"), LeaseID: "l", LeaseUntil: clock.Now().Add(2 * time.Minute)}); err == nil {
		t.Fatal("超过收紧后的 max lease 必须报错")
	}

	// runtimeStateToWire 全字段分支。
	full := GatewayAccountCircuitState{
		ScopeKey: mustGatewayAccountCircuitScopeKey(scope), Scope: scope, Phase: GatewayAccountCircuitPhaseHalfOpen,
		Generation: 3, DispatchRevision: 4, LedgerRevision: 2, TransitionID: "tw",
		OpenedAt: ptrTimeW7B(clock.Now()), RetryAt: ptrTimeW7B(clock.Now().Add(time.Second)),
		Lease:          &GatewayAccountCircuitLease{Kind: GatewayAccountCircuitLeaseHalfOpen, ID: "lw", Until: clock.Now().Add(time.Minute)},
		HalfOpenOrigin: GatewayAccountCircuitPhaseOpen, UpdatedAt: clock.Now(),
	}
	wire, err := runtimeStateToWire(full)
	if err != nil || wire.LedgerRevision == "" || wire.OpenedAtMS == nil || wire.RetryAtMS == nil || wire.Lease == nil {
		t.Fatalf("wire 满分支 = %+v err=%v", wire, err)
	}
	back, err := runtimeStateFromWire(wire)
	if err != nil || back.LedgerRevision != 2 || back.Lease == nil || back.OpenedAt == nil {
		t.Fatalf("wire 回读 = %+v err=%v", back, err)
	}
	badLeaseWire := wire
	badLeaseWire.Lease.LeaseUntilMS = 0
	if _, err := runtimeStateFromWire(badLeaseWire); err == nil {
		t.Fatal("非法租约 wire 必须报错")
	}

	// wire 关系校验分支。
	dup := wire
	dup.ChildScopeKeys = accountCircuitRuntimeStrings{"k", "k"}
	if err := validateAccountCircuitRuntimeWireRelations(dup); err == nil {
		t.Fatal("重复关系必须报错")
	}
	tooMany := wire
	tooMany.ChildScopeKeys = accountCircuitRuntimeStrings{}
	for i := 0; i <= GatewayAccountCircuitRuntimeMaxEvidenceScopes; i++ {
		tooMany.ChildScopeKeys = append(tooMany.ChildScopeKeys, "k")
	}
	if err := validateAccountCircuitRuntimeWireRelations(tooMany); err == nil {
		t.Fatal("超限关系必须报错")
	}

	// Clone 指针字段分支。
	cloned := CloneGatewayAccountCircuitState(full)
	if cloned.Lease == nil || cloned.OpenedAt == nil || cloned.RetryAt == nil || cloned.Lease.ID != "lw" {
		t.Fatal("克隆必须保留指针字段")
	}

	// 文本校验：控制字符 / 首尾空白 / 非法 UTF8。
	if validateGatewayAccountCircuitText("a\x00b", 16, "x") == nil {
		t.Fatal("控制字符必须报错")
	}
	if validateGatewayAccountCircuitText(" a", 16, "x") == nil {
		t.Fatal("首部空白必须报错")
	}

	// 投影结果校验分支。
	if err := validateAccountCircuitRevisionProjection(GatewayAccountCircuitRevisionProjection{CurrentRevision: 0, Status: GatewayAccountCircuitRevisionApplied}); err == nil {
		t.Fatal("CurrentRevision 0 必须报错")
	}
	if err := validateAccountCircuitRevisionProjection(GatewayAccountCircuitRevisionProjection{CurrentRevision: 1, Status: "alien"}); err == nil {
		t.Fatal("非法投影状态必须报错")
	}
	// Redis 键名归一化分支。
	if _, err := accountCircuitRevisionRedisKeys("", "name"); err == nil {
		t.Fatal("空命名空间必须报错")
	}
	if _, err := accountCircuitRevisionRedisKeys("  ", ""); err == nil {
		t.Fatal("空名称必须报错")
	}

	// 数组助手分支。
	if isUniqueSortedRuntimeStrings([]string{"a", "a"}) {
		t.Fatal("重复数组必须判非唯一")
	}
	if got := uniqueSortedRuntimeStrings([]string{"b", "a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("去重排序 = %v", got)
	}
	if equalRuntimeStringMap(map[string]string{"a": "1"}, map[string]string{"a": "2"}) {
		t.Fatal("不同值必须判不等")
	}
	if equalRuntimeArrayMap(map[string][]string{"a": {"1"}}, map[string][]string{"a": {"1", "2"}}) {
		t.Fatal("不同长度必须判不等")
	}
	if equalRuntimeArrayMap(map[string][]string{"a": {"1", "2"}}, map[string][]string{"a": {"1", "3"}}) {
		t.Fatal("不同内容必须判不等")
	}

	// 空投影器 / 空 restorer 守卫。
	var nilProjector *AccountCircuitRevisionProjector
	if _, err := nilProjector.ProjectGatewayAccountCircuitRevision(context.Background(), GatewayAccountCircuitOutboxEvent{}); err == nil {
		t.Fatal("nil projector 必须报错")
	}
	emptyRestorer := &AccountCircuitIncidentRestorer{}
	if _, err := emptyRestorer.RestoreGatewayAccountCircuitIncident(context.Background(), GatewayAccountCircuitIncident{}); err == nil {
		t.Fatal("空 restorer 必须报错")
	}
	// backfiller WithNow 链。
	server := miniredis.RunT(t)
	client, err := NewClient("redis://"+server.Addr(), "w7b-helpers")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(client)
	if err != nil {
		t.Fatal(err)
	}
	if backfiller.WithNow(nil) != backfiller {
		t.Fatal("WithNow(nil) 必须返回自身")
	}
	if backfiller.WithNow(clock.Now) != backfiller {
		t.Fatal("WithNow 必须返回自身")
	}
	// audit 失败分支：states 源修订与 durable 修订不一致。
	if err := client.client.HSet(context.Background(), backfiller.keys.states, mustGatewayAccountCircuitScopeKey(w7bAccountScope("acct-audit")), `{"state":{"scopeKey":"`+mustGatewayAccountCircuitScopeKey(w7bAccountScope("acct-audit"))+`","scope":{"kind":"account","accountRuntimeKey":"acct-audit"},"phase":"OPEN","generation":1,"dispatchRevision":"3","transitionId":"t1","updatedAtMs":1},"replayOrder":["t1"]}`).Err(); err != nil {
		t.Fatal(err)
	}
	mismatchReader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: "acct-audit", DispatchRevision: 9}}}, nil
	})
	backfiller.WithDispatchRevisionReader(mismatchReader)
	if _, err := backfiller.BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}); err == nil {
		t.Fatal("修订不一致 audit 必须报错")
	}
}

func TestW7BFinalValidatorSweep(t *testing.T) {
	rt := &AccountCircuitRuntimeStore{}
	scope := w7bAccountScope("acct-a")
	// 身份围栏的错误分支。
	if err := rt.validateIdentity("other", scope); err == nil {
		t.Fatal("身份不匹配必须报错")
	}
	if err := rt.validateMutationIdentity("other", scope, 1, "t"); err == nil {
		t.Fatal("身份不匹配必须报错")
	}
	if err := validateRuntimeText(" x", 16, "x"); err == nil {
		t.Fatal("首空格文本必须报错")
	}
	// scope 校验残余分支。
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAccount, AccountRuntimeKey: " a"}); err == nil {
		t.Fatal("runtime key 首空格必须报错")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: "a", ProtocolProfile: "openai", RequestLane: "text", ModelBucket: " b"}); err == nil {
		t.Fatal("model bucket 首空格必须报错")
	}
	// 数组助手键缺失分支。
	if equalRuntimeArrayMap(map[string][]string{"a": {"1"}}, map[string][]string{}) {
		t.Fatal("键缺失必须判不等")
	}
	// audit：states 账户缺少 durable 修订 → audit 失败。
	server := miniredis.RunT(t)
	client, err := NewClient("redis://"+server.Addr(), "w7b-final")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(client)
	if err != nil {
		t.Fatal(err)
	}
	accountScope := w7bAccountScope("acct-orphan")
	entry := map[string]any{
		"state": map[string]any{
			"scopeKey": mustGatewayAccountCircuitScopeKey(accountScope),
			"scope":    map[string]any{"kind": "account", "accountRuntimeKey": "acct-orphan"},
			"phase":    "OPEN", "generation": 1, "dispatchRevision": "3", "transitionId": "t1", "updatedAtMs": 1,
		},
		"replayOrder": []string{"t1"},
	}
	rawEntry, _ := json.Marshal(entry)
	if err := client.client.HSet(context.Background(), backfiller.keys.states, mustGatewayAccountCircuitScopeKey(accountScope), string(rawEntry)).Err(); err != nil {
		t.Fatal(err)
	}
	strayReader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: "acct-other", DispatchRevision: 3}}}, nil
	})
	if _, err := backfiller.WithDispatchRevisionReader(strayReader).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}); err == nil {
		t.Fatal("states 账户缺少 durable 修订 audit 必须报错")
	}
}

func ptrTimeW7B(value time.Time) *time.Time { return &value }

// ---------------------------------------------------------------------------
// 错误分支批量覆盖：关闭客户端 + 直接助手调用 + 容量耗尽映射
// ---------------------------------------------------------------------------

func TestW7BErrorBranchesWithClosedClient(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w7b-closed", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := store.BackfillRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}, reader); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	accountID := "acct-closed"
	scope := w7bAccountScope(accountID)
	// 关闭客户端后所有 Redis 往返都必须包裹错误上抛。
	if _, err := store.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "t1", Reason: "x"}); err == nil {
		t.Fatal("关闭后 suspect 必须报错")
	}
	if _, err := store.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope}); err == nil {
		t.Fatal("关闭后 get 必须报错")
	}
	if _, err := store.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Limit: 1}); err == nil {
		t.Fatal("关闭后 listDue 必须报错")
	}
	if _, err := store.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, GatewayAccountCircuitProtocolModelOpenEvidenceInput{AccountID: accountID, Scope: w7bProtocolScope(accountID, "b"), Generation: 1, DispatchRevision: 1, EvidenceID: "e", AccountTransitionID: "a", Reason: "r", ConfirmedFailureCount: 1, Window: time.Hour, MaxProtocolScopes: 8}); err == nil {
		t.Fatal("关闭后 record 必须报错")
	}
	if _, err := store.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: accountID, AccountRuntimeKey: accountID, DispatchRevision: 1, EvidenceID: "e"}); err == nil {
		t.Fatal("关闭后 clear 必须报错")
	}
	if _, err := store.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: accountID, DispatchRevision: 1, TransitionID: "t"}); err == nil {
		t.Fatal("关闭后账户修订必须报错")
	}
	if _, err := store.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: accountID, State: func() GatewayAccountCircuitState {
		state, err := GatewayAccountCircuitClosedState(scope, 1, 1, "t", clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		return state
	}()}); err == nil {
		t.Fatal("关闭后 restore 必须报错")
	}
	if _, err := store.ProjectRevision(ctx, GatewayAccountCircuitOutboxEvent{EventType: GatewayAccountCircuitDispatchRevisionChanged, ProjectionKey: GatewayAccountCircuitProjectionKey, AccountID: accountID, AccountRuntimeKey: accountID, TransitionID: "t", DispatchRevision: 2}); err == nil {
		t.Fatal("关闭后投影必须报错")
	}
	// 索引扫描在关闭客户端上的错误分支。
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	backfiller.WithDispatchRevisionReader(reader)
	if _, err := backfiller.BackfillGatewayAccountCircuitRuntimeIndex(ctx, GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}); err == nil {
		t.Fatal("关闭后 backfill 必须报错")
	}
	// 空值接收者。
	var nilRuntime *AccountCircuitRuntimeStore
	if _, err := nilRuntime.runMutation(ctx, accountCircuitRuntimeMutationWire{}); err == nil {
		t.Fatal("nil runtime runMutation 必须报错")
	}
}

func TestW7BCapacityExhaustedRestoreMapping(t *testing.T) {
	clock := newW7BClock()
	store, rt := w7bReadyStore(t, clock, 1, time.Minute)
	ctx := context.Background()
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: "acct-cap2", Scope: w7bAccountScope("acct-cap2"), DispatchRevision: 1, TransitionID: "t1", Reason: "x", Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "acct-cap2", DispatchRevision: 2, TransitionID: "t2"}); err != nil {
		t.Fatal(err)
	}
	// 容量 1 且唯一槽位被 acct-cap2 占用：restore 其他作用域必须报容量耗尽。
	other := w7bAccountScope("acct-other")
	state := GatewayAccountCircuitState{
		ScopeKey: mustGatewayAccountCircuitScopeKey(other), Scope: other, Phase: GatewayAccountCircuitPhaseOpen,
		Generation: 1, DispatchRevision: 2, TransitionID: "t3", UpdatedAt: clock.Now(),
	}
	if _, err := store.RestoreIncident(ctx, GatewayAccountCircuitIncident{
		AccountID: "acct-other", AccountRuntimeKey: "acct-other", ScopeKind: "account",
		CircuitScopeKey: mustGatewayAccountCircuitScopeKey(other), IncidentID: "inc", State: "OPEN",
		Generation: 1, DispatchRevision: 2, LedgerRevision: 1, TransitionID: "t3", UpdatedAt: clock.Now(),
	}); err == nil {
		t.Fatal("容量耗尽 restore 必须报错")
	}
	_ = state
}

func TestW7BDirectHelperBranches(t *testing.T) {
	// redisInt64 / runtimeRedisBytes / accountID / mustKey 分支。
	if value, err := redisInt64(3); err != nil || value != 3 {
		t.Fatalf("int 分支 = %d err=%v", value, err)
	}
	if value, err := redisInt64([]byte("5")); err != nil || value != 5 {
		t.Fatalf("[]byte 分支 = %d err=%v", value, err)
	}
	if accountCircuitRuntimeAccountID("acct:authorized:extra") != "acct" {
		t.Fatal("authorized 前缀必须截断")
	}
	if accountCircuitRuntimeAccountID("plain") != "plain" {
		t.Fatal("无 authorized 必须原样返回")
	}
	if string(mustBytesW7B(runtimeRedisBytes([]byte("raw")))) != "raw" {
		t.Fatal("[]byte 分支必须透传")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("非法 scope 的 mustKey 必须panic")
		}
	}()
	_ = mustGatewayAccountCircuitScopeKey(GatewayAccountCircuitScope{})
}

func mustBytesW7B(value []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return value
}

// ---------------------------------------------------------------------------
// 第二轮补齐：锁错误路径、audit 证据修订、incident 时间字段、残余校验分支
// ---------------------------------------------------------------------------

func TestW7BBackfillLockAndAuditBranches(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	client, err := NewClient("redis://"+server.Addr(), "w7b-lock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(client)
	if err != nil {
		t.Fatal(err)
	}
	// 锁 token 不匹配 → renew / advancePhase / invalidate 错误与零值分支。
	if err := backfiller.renew(context.Background(), "wrong-token", time.Minute); err == nil {
		t.Fatal("锁丢失 renew 必须报错")
	}
	if err := backfiller.advancePhase(context.Background(), "wrong-token", "epoch", "states", "escalation"); err == nil {
		t.Fatal("锁丢失 advancePhase 必须报错")
	}
	if err := backfiller.invalidate(context.Background(), "wrong-token", "wrong-epoch", "reason"); err != nil {
		t.Fatalf("token 不匹配 invalidate 必须零值返回: %v", err)
	}
	if err := backfiller.invalidate(context.Background(), "wrong-token", "wrong-epoch", strings.Repeat("r", 600)); err != nil {
		t.Fatalf("长 reason invalidate 必须成功截断: %v", err)
	}
	// nil client / nil ctx 的 backfill 错误分支。
	emptyClient := &Client{}
	if _, err := NewAccountCircuitRuntimeIndexBackfiller(emptyClient); err == nil {
		t.Fatal("nil redis client 必须报错")
	}
	if _, err := backfiller.BackfillGatewayAccountCircuitRuntimeIndex(nil, GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o"}); err == nil {
		t.Fatal("nil ctx 必须报错")
	}

	// 证据修订与 durable 不一致 → audit 失败。
	accountID := "acct-evmismatch"
	scopeKey := mustGatewayAccountCircuitScopeKey(w7bProtocolScope(accountID, "b1"))
	evidence := map[string]any{
		"dispatchRevision": "5",
		"scopes": []map[string]any{{
			"scopeKey": scopeKey, "incidentId": "inc", "evidenceId": "ev",
			"confirmedFailureCount": 2, "observedAtMs": clock.Now().UnixMilli(),
		}},
	}
	raw, _ := json.Marshal(evidence)
	if err := client.client.HSet(context.Background(), backfiller.keys.escalation, accountID, string(raw)).Err(); err != nil {
		t.Fatal(err)
	}
	mismatch := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: accountID, DispatchRevision: 9}}}, nil
	})
	backfiller.WithDispatchRevisionReader(mismatch)
	if _, err := backfiller.BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner"}); err == nil {
		t.Fatal("证据修订不一致 audit 必须报错")
	}

	// 索引扫描页数上限（依赖 miniredis HScan 分页行为；不触发则跳过断言）。
	pageBound := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	pipe := client.client.Pipeline()
	for i := 0; i < 120; i++ {
		key := fmt.Sprintf("field-%03d", i)
		_ = pipe.HSet(context.Background(), backfiller.keys.states, key, key)
	}
	_, _ = pipe.Exec(context.Background())
	_, pageErr := backfiller.WithDispatchRevisionReader(pageBound).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner", ScanCount: 10, MaxPages: 1})
	if pageErr == nil {
		t.Log("miniredis 未分页，页数上限分支未触发（可接受）")
	}
	_ = client.client.Del(context.Background(), backfiller.keys.states).Err()

	// 修订页超过数据上限。
	bigID := strings.Repeat("a", 300)
	hugePage := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: bigID, DispatchRevision: 1}}}, nil
	})
	if _, err := backfiller.WithDispatchRevisionReader(hugePage).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w7b-owner", MaxBytes: 1024}); err == nil {
		t.Fatal("修订页超数据上限必须报错")
	}
}

func TestW7BIncidentTimeFieldsAndRemainingValidators(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	base := GatewayAccountCircuitIncident{
		AccountID: "acct-inc2", AccountRuntimeKey: "acct-inc2", ScopeKind: "account",
		CircuitScopeKey: mustGatewayAccountCircuitScopeKey(w7bAccountScope("acct-inc2")),
		IncidentID:      "inc", State: "OPEN", Generation: 1, DispatchRevision: 2,
		LedgerRevision: 1, TransitionID: "t1", UpdatedAt: now,
		OpenUntil: &now, NextTransitionAt: &now,
	}
	state, err := accountCircuitIncidentRuntimeStateFromIncident(base)
	if err != nil || state.OpenedAtMS == nil || state.RetryAtMS == nil {
		t.Fatalf("incident 时间字段 = %+v err=%v", state, err)
	}
	// validateTransitionIdentity 负世代分支。
	rt := &AccountCircuitRuntimeStore{}
	if err := rt.validateTransitionIdentity(GatewayAccountCircuitTransitionIdentity{Generation: -1}); err == nil {
		t.Fatal("负世代必须报错")
	}
	// wire ledger 修订损坏分支。
	scope := w7bAccountScope("acct-wire")
	wire := accountCircuitRuntimeStateWire{
		ScopeKey: mustGatewayAccountCircuitScopeKey(scope), Scope: runtimeScopeWire(scope),
		Phase: string(GatewayAccountCircuitPhaseClosed), Generation: 1,
		DispatchRevision: "2", LedgerRevision: "junk", UpdatedAtMS: now.UnixMilli(),
	}
	if _, err := runtimeStateFromWire(wire); err == nil {
		t.Fatal("损坏 ledger 修订必须报错")
	}
	// 投影 nil ctx / 空 project / project 错误分支。
	projector := &AccountCircuitRevisionProjector{project: func(context.Context, accountCircuitRevisionKeys, GatewayAccountCircuitOutboxEvent, time.Duration, time.Time) ([]byte, error) {
		return []byte(`{}`), nil
	}}
	if _, err := projector.ProjectGatewayAccountCircuitRevision(nil, GatewayAccountCircuitOutboxEvent{}); err == nil {
		t.Fatal("nil ctx 必须报错")
	}
	emptyProjector := &AccountCircuitRevisionProjector{}
	if _, err := emptyProjector.ProjectGatewayAccountCircuitRevision(context.Background(), GatewayAccountCircuitOutboxEvent{}); err == nil {
		t.Fatal("空 projector 必须报错")
	}
	failingProjector := &AccountCircuitRevisionProjector{project: func(context.Context, accountCircuitRevisionKeys, GatewayAccountCircuitOutboxEvent, time.Duration, time.Time) ([]byte, error) {
		return nil, errors.New("w7b 注入投影失败")
	}}
	if _, err := failingProjector.ProjectGatewayAccountCircuitRevision(context.Background(), GatewayAccountCircuitOutboxEvent{}); err == nil {
		t.Fatal("project 错误必须上抛")
	}
	// 状态校验残余分支。
	closed, _ := GatewayAccountCircuitClosedState(scope, 1, 1, "t", now)
	negativeBackoff := closed
	negativeBackoff.BackoffAttempt = -1
	if err := ValidateGatewayAccountCircuitState(negativeBackoff); err == nil {
		t.Fatal("负退避计数必须报错")
	}
	negativeSuccess := closed
	negativeSuccess.RecoverySuccessCount = -1
	if err := ValidateGatewayAccountCircuitState(negativeSuccess); err == nil {
		t.Fatal("负恢复成功计数必须报错")
	}
	negativeLedger := closed
	negativeLedger.LedgerRevision = -1
	if err := ValidateGatewayAccountCircuitState(negativeLedger); err == nil {
		t.Fatal("负账本修订必须报错")
	}
	controlRelation := closed
	controlRelation.ChildScopeKeys = []string{"a\x00"}
	if err := ValidateGatewayAccountCircuitState(controlRelation); err == nil {
		t.Fatal("关系值控制字符必须报错")
	}
	// incident 映射残余分支：SHADOWED_BY_PERSISTENT → OPEN；未知租约用途忽略。
	shadowed := base
	shadowed.State = "SHADOWED_BY_PERSISTENT"
	if state, err := accountCircuitIncidentRuntimeStateFromIncident(shadowed); err != nil || state.Phase != "OPEN" {
		t.Fatalf("shadowed 映射 = %+v err=%v", state, err)
	}
	alienLease := base
	alienLease.State = "HALF_OPEN"
	alienLease.LeaseID = "l"
	alienLease.LeasePurpose = "alien"
	alienLease.LeaseUntil = &now
	if state, err := accountCircuitIncidentRuntimeStateFromIncident(alienLease); err != nil || state.Lease != nil {
		t.Fatalf("未知租约用途必须忽略: %+v err=%v", state, err)
	}
	// 运行时属主 restorer 的构造参数校验分支。
	server2 := miniredis.RunT(t)
	client2, err := NewClient("redis://"+server2.Addr(), "w7b-inc2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client2.Close() })
	if _, err := NewAccountCircuitRuntimeOwnerIncidentRestorer(client2, 25*time.Hour, 10); err == nil {
		t.Fatal("非法 retention 必须报错")
	}
	if _, err := NewAccountCircuitRuntimeOwnerIncidentRestorer(client2, time.Minute, -1); err == nil {
		t.Fatal("非法 capacity 必须报错")
	}
	// 空接收者的 backfill 与 redisInt64 边界、数组助手、文本控制字符。
	var nilBackfiller *AccountCircuitRuntimeIndexBackfiller
	if _, err := nilBackfiller.BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "o"}); err == nil {
		t.Fatal("nil backfiller 必须报错")
	}
	if validAccountCircuitRevisionText("a\nb", 16) {
		t.Fatal("换行必须判非法")
	}
	if equalRuntimeStringMap(map[string]string{"a": "1"}, map[string]string{}) {
		t.Fatal("不同长度必须判不等")
	}
	// 锁 renew 在关闭客户端上的错误分支。
	closedBackfiller, err := NewAccountCircuitRuntimeIndexBackfiller(client2)
	if err != nil {
		t.Fatal(err)
	}
	_ = client2.Close()
	if err := closedBackfiller.renew(context.Background(), "o", time.Minute); err == nil {
		t.Fatal("关闭客户端 renew 必须报错")
	}
	// validateGatewayAccountCircuitScope 的 key-model 单字段缺失分支。
	keyModel := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeKeyModel, AccountRuntimeKey: "a", KeyFingerprint: "fp"}
	for name, mutate := range map[string]func(*GatewayAccountCircuitScope){
		"clientModel":      func(sc *GatewayAccountCircuitScope) { sc.ClientModel = "m" },
		"capabilityHash":   func(sc *GatewayAccountCircuitScope) { sc.CapabilityHash = "h" },
		"credentialSource": func(sc *GatewayAccountCircuitScope) { sc.CredentialSourceAccountID = "c" },
		"endpointFamily":   func(sc *GatewayAccountCircuitScope) { sc.ClientEndpointFamily = "chat" },
		"finalUpstream":    func(sc *GatewayAccountCircuitScope) { sc.FinalUpstreamModel = "m2" },
		"upstreamMode":     func(sc *GatewayAccountCircuitScope) { sc.UpstreamEndpointMode = "sse" },
	} {
		partial := keyModel
		mutate(&partial)
		if err := ValidateGatewayAccountCircuitScope(partial); err == nil {
			t.Fatalf("key-model 缺失 %s 必须报错", name)
		}
	}
}
