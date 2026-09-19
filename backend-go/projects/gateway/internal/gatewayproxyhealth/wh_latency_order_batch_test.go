package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// 排序入口批量读改造（E2）的等价性验证：
// 1. 同一组状态，批量 GetJSONMany 与改造前逐 key GetJSON 给出完全一致的排序结果；
// 2. 排序入口只发生一次批量往返（N+1 消除）；
// 3. 批量读整体失败时错误原样传播。

// perKeyGetStore 默认把 GetJSONMany 退化为逐 key GetJSON 循环（改造前的数据
// 获取语义，含错误传播），并记录两类调用次数。manyPassthrough 为 true 时改
// 为只计数并委托原生批量实现，用于批量往返次数断言。
type perKeyGetStore struct {
	*MemoryRuntimeStateStore
	manyPassthrough  bool
	getJSONCalls     int
	getJSONManyCalls int
}

func (s *perKeyGetStore) GetJSON(ctx context.Context, key string) (json.RawMessage, error) {
	s.getJSONCalls++
	return s.MemoryRuntimeStateStore.GetJSON(ctx, key)
}

func (s *perKeyGetStore) GetJSONMany(ctx context.Context, keys []string) ([]json.RawMessage, error) {
	s.getJSONManyCalls++
	if s.manyPassthrough {
		return s.MemoryRuntimeStateStore.GetJSONMany(ctx, keys)
	}
	output := make([]json.RawMessage, len(keys))
	for i, key := range keys {
		raw, err := s.GetJSON(ctx, key)
		if err != nil {
			return nil, err
		}
		output[i] = raw
	}
	return output, nil
}

func orderLatencyOptions() LatencyDegradationOptions {
	return LatencyDegradationOptions{
		LockRetryDelay: func(int) {},
		Random:         func() float64 { return 0.5 },
	}
}

func orderAccountIDs(result LatencyDegradationOrderResult[SuppressibleGatewayAccount]) []string {
	ids := make([]string, len(result.Accounts))
	for i, account := range result.Accounts {
		ids[i] = account.ID
	}
	return ids
}

func assertOrderResultEqual(t *testing.T, label string, got, want LatencyDegradationOrderResult[SuppressibleGatewayAccount]) {
	t.Helper()
	if got.Applied != want.Applied {
		t.Fatalf("%s: Applied = %v, want %v", label, got.Applied, want.Applied)
	}
	if got.BypassedAllDegraded != want.BypassedAllDegraded {
		t.Fatalf("%s: BypassedAllDegraded = %v, want %v", label, got.BypassedAllDegraded, want.BypassedAllDegraded)
	}
	if !reflect.DeepEqual(orderAccountIDs(got), orderAccountIDs(want)) {
		t.Fatalf("%s: 账户顺序 = %v, want %v", label, orderAccountIDs(got), orderAccountIDs(want))
	}
	if !reflect.DeepEqual(got.DegradedAccountIDs, want.DegradedAccountIDs) {
		t.Fatalf("%s: DegradedAccountIDs = %v, want %v", label, got.DegradedAccountIDs, want.DegradedAccountIDs)
	}
}

// seedOrderState 直写一个当前纪元的 latency 状态（Decode 校验可通过）。
func seedOrderState(t *testing.T, store *MemoryRuntimeStateStore, key string, state latencyState) {
	t.Helper()
	if err := store.SetJSON(contextBackground(), key, state, 120_000); err != nil {
		t.Fatal(err)
	}
}

// 批量读与逐 key 读在完整状态矩阵下必须给出一致排序：
// 无状态、缺失键、降级中、降级已过期、纪元过期、非法 JSON、校验失败、
// 两个账户 sanitize 后同 key（dup.1 / dup_1 → dup_1）。
func TestOrderGatewayAccountsBatchReadEquivalence(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	now := clock.NowMs()
	base := NewMemoryRuntimeStateStore(clock.Now)
	scope := latencyScope()
	config := speedFirstConfig()

	serviceBatch := NewLatencyDegradationService(base, clock.Now, orderLatencyOptions())
	// 逐 key 替身与批量路径共享同一底层存储，预置状态天然一致。
	perKey := &perKeyGetStore{MemoryRuntimeStateStore: base}
	servicePerKey := NewLatencyDegradationService(perKey, clock.Now, orderLatencyOptions())

	// 降级中：dg1 / dg2 / dup_1 通过官方入口触发降级（SlowTriggerCount=3）。
	for _, id := range []string{"dg1", "dup_1", "dg2"} {
		account := latencyAccount(id)
		for i := 0; i < 3; i++ {
			if _, err := serviceBatch.RecordNormalRouteFirstByteSlow(ctx, account, scope, &config, ""); err != nil {
				t.Fatalf("%s 慢样本 %d: %v", id, i, err)
			}
		}
	}

	// 降级已过期：纪元一致但 DegradedUntilMs 早于 now → 视为正常。
	expired := latencyState{
		Generation: `[0,"initial"]`, AccountID: "expired", RuntimeKey: "expired",
		Scope: *scope, Config: config,
		FirstSlowAtMs: now - 300_000, LastSlowAtMs: now - 10_000, SlowCount: 3,
		DegradedUntilMs: int64Ptr(now - 1_000), NextProbeAtMs: int64Ptr(now - 2_000),
		RecoveryProbeRoundAttemptCount: int64Ptr(0), RecoveryProbeRoundSuccessCount: int64Ptr(0),
		Reason: "播种",
	}
	seedOrderState(t, base, accountLatencyStateKey(*scope, latencyAccount("expired")), expired)

	// 纪元过期：状态本身合法但 Generation 不匹配 → 视为无状态。
	staleGen := expired
	staleGen.AccountID = "stalegen"
	staleGen.RuntimeKey = "stalegen"
	staleGen.Generation = `[99,"stale"]`
	staleGen.DegradedUntilMs = int64Ptr(now + 100_000)
	seedOrderState(t, base, accountLatencyStateKey(*scope, latencyAccount("stalegen")), staleGen)

	// 校验失败：缺 config 全零字段 → decodeLatencyState 拒绝 → 视为无状态。
	invalid := latencyState{
		Generation: `[0,"initial"]`, AccountID: "invalid", RuntimeKey: "invalid",
		Scope:         *scope,
		FirstSlowAtMs: now - 300_000, LastSlowAtMs: now, SlowCount: 3,
		DegradedUntilMs: int64Ptr(now + 100_000),
		Reason:          "播种",
	}
	seedOrderState(t, base, accountLatencyStateKey(*scope, latencyAccount("invalid")), invalid)

	// 非法 JSON：decode 失败 → 视为无状态。
	whSeedRaw(base, clock, accountLatencyStateKey(*scope, latencyAccount("badjson")), json.RawMessage(`{not-json`), 120_000)

	accounts := []SuppressibleGatewayAccount{
		latencyAccount("n1"),       // 正常（无状态）
		latencyAccount("dg1"),      // 降级中
		latencyAccount("missing"),  // 键缺失
		latencyAccount("expired"),  // 降级已过期 → 正常
		latencyAccount("stalegen"), // 纪元过期 → 无状态
		latencyAccount("badjson"),  // 非法 JSON → 无状态
		latencyAccount("invalid"),  // 校验失败 → 无状态
		latencyAccount("dup.1"),    // 与 dup_1 同 key，读到降级状态
		latencyAccount("dup_1"),    // 降级中
		latencyAccount("dg2"),      // 降级中
	}
	view := func(a SuppressibleGatewayAccount) SuppressibleGatewayAccount { return a }

	gotBatch, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(ctx, serviceBatch, accounts, view, scope, &config, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotPerKey, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(ctx, servicePerKey, accounts, view, scope, &config, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := LatencyDegradationOrderResult[SuppressibleGatewayAccount]{
		Accounts: []SuppressibleGatewayAccount{
			latencyAccount("n1"), latencyAccount("missing"), latencyAccount("expired"),
			latencyAccount("stalegen"), latencyAccount("badjson"), latencyAccount("invalid"),
			latencyAccount("dg1"), latencyAccount("dup.1"), latencyAccount("dup_1"), latencyAccount("dg2"),
		},
		Applied:             true,
		DegradedAccountIDs:  []string{"dg1", "dup.1", "dup_1", "dg2"},
		BypassedAllDegraded: false,
	}
	assertOrderResultEqual(t, "批量读", gotBatch, want)
	// 逐 key 读（改造前语义）与批量读结果完全一致。
	assertOrderResultEqual(t, "逐 key 读", gotPerKey, want)
	assertOrderResultEqual(t, "批量 vs 逐 key", gotBatch, gotPerKey)
}

// 排序入口必须只发生一次批量往返：GetJSONMany 恰好 1 次，
// GetJSON 仅剩 generation 读取的 1 次（改造前为 N+1 次 GetJSON）。
func TestOrderGatewayAccountsSingleBatchRoundTrip(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	base := NewMemoryRuntimeStateStore(clock.Now)
	scope := latencyScope()
	config := speedFirstConfig()

	seeder := NewLatencyDegradationService(base, clock.Now, orderLatencyOptions())
	if _, err := seeder.RecordNormalRouteFirstByteSlow(ctx, latencyAccount("dg"), scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := seeder.RecordNormalRouteFirstByteSlow(ctx, latencyAccount("dg"), scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := seeder.RecordNormalRouteFirstByteSlow(ctx, latencyAccount("dg"), scope, &config, ""); err != nil {
		t.Fatal(err)
	}

	perKey := &perKeyGetStore{MemoryRuntimeStateStore: base, manyPassthrough: true}
	service := NewLatencyDegradationService(perKey, clock.Now, orderLatencyOptions())
	accounts := []SuppressibleGatewayAccount{latencyAccount("a"), latencyAccount("dg"), latencyAccount("c")}
	view := func(a SuppressibleGatewayAccount) SuppressibleGatewayAccount { return a }
	if _, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(ctx, service, accounts, view, scope, &config, nil); err != nil {
		t.Fatal(err)
	}
	if perKey.getJSONManyCalls != 1 {
		t.Fatalf("排序状态读取必须恰好 1 次批量往返, got %d", perKey.getJSONManyCalls)
	}
	if perKey.getJSONCalls != 1 {
		t.Fatalf("单 key GetJSON 仅允许 generation 读取 1 次, got %d", perKey.getJSONCalls)
	}
}

// 批量读整体失败必须原样传播，不得静默回退为无状态排序。
func TestOrderGatewayAccountsBatchFailurePropagation(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	store := newWhLatencyStore(clock)
	service := newWhLatencyServiceOnStore(clock, store)
	scope := latencyScope()
	config := speedFirstConfig()
	accounts := []SuppressibleGatewayAccount{latencyAccount("a"), latencyAccount("b")}
	view := func(a SuppressibleGatewayAccount) SuppressibleGatewayAccount { return a }

	injected := errors.New("批量读取注入故障")
	store.getJSONManyFail = func([]string) error { return injected }
	if _, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(ctx, service, accounts, view, scope, &config, nil); !errors.Is(err, injected) {
		t.Fatalf("批量读取故障必须原样传播, err=%v", err)
	}
	store.getJSONManyFail = nil
}
