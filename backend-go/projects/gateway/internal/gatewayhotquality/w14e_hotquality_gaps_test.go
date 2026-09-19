package gatewayhotquality

// w14e 覆盖率补强：针对 w14e 波次 profile 定位的未覆盖语句块。
// 只使用包内既有 mock 模式（mockHotQualityStore / mockScriptRunner / miniredis），
// 不引入真实 Redis / PG 依赖。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// runtime.go：OrderGatewayAccountsByHotQuality 的错误臂与第二协议组路径。
// ---------------------------------------------------------------------------

// w14eExplorationStore 在既有 mockExplorationStore 基础上补充错误注入。
type w14eExplorationStore struct {
	t *testing.T

	accrueState *SameTierExplorationState
	accrueErr   error
	accrueNil   bool

	reserveResult *SameTierExplorationReserveResult
	reserveErr    error

	settleErr error
}

func (m *w14eExplorationStore) Get(ctx context.Context, input SameTierExplorationGetInput) (*SameTierExplorationState, error) {
	return &SameTierExplorationState{PoolKey: input.PoolKey}, nil
}

func (m *w14eExplorationStore) Accrue(ctx context.Context, input SameTierExplorationAccrueInput) (*SameTierExplorationState, error) {
	if m.accrueErr != nil {
		return nil, m.accrueErr
	}
	if m.accrueNil {
		return nil, nil
	}
	if m.accrueState == nil {
		return &SameTierExplorationState{PoolKey: input.PoolKey}, nil
	}
	return m.accrueState, nil
}

func (m *w14eExplorationStore) Reserve(ctx context.Context, input SameTierExplorationReserveInput) (*SameTierExplorationReserveResult, error) {
	if m.reserveErr != nil {
		return nil, m.reserveErr
	}
	if m.reserveResult == nil {
		return &SameTierExplorationReserveResult{Status: ExplorationReservationReserved, State: SameTierExplorationState{PoolKey: input.PoolKey}, Reservation: &SameTierExplorationReservation{
			ReservationID:     input.ReservationID,
			AccountRuntimeKey: input.AccountRuntimeKey,
			LeaseUntilMs:      input.LeaseUntilMs,
		}}, nil
	}
	return m.reserveResult, nil
}

func (m *w14eExplorationStore) Settle(ctx context.Context, input SameTierExplorationSettleInput) (*SameTierExplorationSettleResult, error) {
	if m.settleErr != nil {
		return nil, m.settleErr
	}
	return &SameTierExplorationSettleResult{Status: ExplorationSettlementApplied}, nil
}

func w14eOrderInput(accounts []GatewayHotQualityAccountView, mutate func(*GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView])) GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView] {
	input := GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts:        accounts,
		Base:            baseView,
		Mode:            HotQualityModeSpeedFirst,
		SystemAccountID: "sys",
		GroupID:         "g1",
		RequestLane:     "text",
		RequestID:       "w14e-req",
		NowMs:           int64Ptr(1_000_000),
	}
	if mutate != nil {
		mutate(&input)
	}
	return input
}

func TestW14EOrderGatewayAccountsErrorArms(t *testing.T) {
	valid := testAccount("acc-a", 0)

	t.Run("negative nowMs", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		input := w14eOrderInput([]GatewayHotQualityAccountView{valid}, func(i *GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]) {
			i.NowMs = int64Ptr(-1)
		})
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "当前时间必须是非负安全整数" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("broken runtime key", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		broken := testAccount("acc-broken", 0)
		broken.AccountAccessType = "account_authorized"
		input := w14eOrderInput([]GatewayHotQualityAccountView{broken}, nil)
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "授权账户运行态键缺少绑定上下文" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("blank protocol profile", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		bare := GatewayHotQualityAccountView{ID: "acc-bare", ProviderProtocolProfileID: " "}
		input := w14eOrderInput([]GatewayHotQualityAccountView{bare}, nil)
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "protocolProfile不能为空" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("missing system account", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		input := w14eOrderInput([]GatewayHotQualityAccountView{valid}, func(i *GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]) {
			i.SystemAccountID = " "
		})
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "systemAccountId不能为空" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("snapshot get error", func(t *testing.T) {
		store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
			return nil, errors.New("w14e snapshot boom")
		}}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		input := w14eOrderInput([]GatewayHotQualityAccountView{valid}, nil)
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "w14e snapshot boom" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("missing requestId", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		input := w14eOrderInput([]GatewayHotQualityAccountView{valid}, func(i *GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]) {
			i.RequestID = " "
		})
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "requestId不能为空" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("accrue error", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t, accrueErr: errors.New("w14e accrue boom")})
		input := w14eOrderInput([]GatewayHotQualityAccountView{valid}, nil)
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "w14e accrue boom" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalid mode", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		input := w14eOrderInput([]GatewayHotQualityAccountView{valid}, func(i *GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]) {
			i.Mode = "bogus-mode"
		})
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || !strings.Contains(err.Error(), "热质量路由模式无效") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("scope key direct errors", func(t *testing.T) {
		if _, err := GatewayHotQualityRouteScopeKey(gatewayHotQualityRouteScopeKeyInput{SystemAccountID: "sys", GroupID: " "}); err == nil || err.Error() != "groupId不能为空" {
			t.Fatalf("group err = %v", err)
		}
		if _, err := GatewayHotQualityRouteScopeKey(gatewayHotQualityRouteScopeKeyInput{SystemAccountID: "sys", GroupID: "g", ProtocolProfile: " "}); err == nil || err.Error() != "protocolProfile不能为空" {
			t.Fatalf("protocol err = %v", err)
		}
		if _, err := SameTierExplorationPoolKey("scope", " "); err == nil || err.Error() != "configurationTierKey不能为空" {
			t.Fatalf("tier err = %v", err)
		}
	})

	t.Run("accrue nil state", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t, accrueNil: true})
		input := w14eOrderInput([]GatewayHotQualityAccountView{valid}, nil)
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "同层探索状态缺失" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reserve error", func(t *testing.T) {
		accA := testAccount("acc-a", 0)
		accB := testAccount("acc-b", 0)
		store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
			if scope.AccountRuntimeKey == "acc-a" {
				return snapshotFor(10, 0), nil
			}
			return snapshotFor(0, 10), nil
		}}
		exploration := &w14eExplorationStore{t: t, accrueState: &SameTierExplorationState{PoolKey: "pool", Credit: 1, Cursor: 2}, reserveErr: errors.New("w14e reserve boom")}
		runtime, _, _ := newTestRuntime(store, exploration)
		input := w14eOrderInput([]GatewayHotQualityAccountView{accA, accB}, func(i *GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]) {
			i.EligibleFirstPrimaryDispatch = true
		})
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || err.Error() != "w14e reserve boom" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("settle closure error", func(t *testing.T) {
		accA := testAccount("acc-a", 0)
		accB := testAccount("acc-b", 0)
		store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
			if scope.AccountRuntimeKey == "acc-a" {
				return snapshotFor(10, 0), nil
			}
			return snapshotFor(0, 10), nil
		}}
		exploration := &w14eExplorationStore{t: t, accrueState: &SameTierExplorationState{PoolKey: "pool", Credit: 1, Cursor: 2}, settleErr: errors.New("w14e settle boom")}
		runtime, _, _ := newTestRuntime(store, exploration)
		input := w14eOrderInput([]GatewayHotQualityAccountView{accA, accB}, func(i *GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]) {
			i.EligibleFirstPrimaryDispatch = true
		})
		result, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.SettleExplorationAfterDispatch == nil {
			t.Fatal("reserved path must carry a settle closure")
		}
		if err := result.SettleExplorationAfterDispatch(context.Background(), "dispatched"); err == nil || err.Error() != "w14e settle boom" {
			t.Fatalf("settle err = %v", err)
		}
	})

	t.Run("tier key overflow priority", func(t *testing.T) {
		store := &mockHotQualityStore{t: t}
		runtime, _, _ := newTestRuntime(store, &w14eExplorationStore{t: t})
		huge := testAccount("acc-huge", math.MaxInt64)
		input := w14eOrderInput([]GatewayHotQualityAccountView{huge}, nil)
		if _, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input); err == nil || !strings.Contains(err.Error(), "priority") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestW14EOrderGatewayAccountsSecondProtocolGroup(t *testing.T) {
	accA := testAccount("acc-a", 0)
	accA2 := testAccount("acc-a2", 0)
	accB := testAccount("acc-b", 3)
	accB.ProviderProtocolProfileID = "openai:2025"
	accB.ProtocolCode = "openai"
	accB.ProtocolVersion = "2025"
	store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
		if scope.AccountRuntimeKey == "acc-a" {
			return snapshotFor(10, 0), nil
		}
		return snapshotFor(0, 10), nil
	}}
	exploration := &w14eExplorationStore{t: t, accrueState: &SameTierExplorationState{PoolKey: "pool", Credit: 1, Cursor: 2}}
	runtime, observer, _ := newTestRuntime(store, exploration)
	input := w14eOrderInput([]GatewayHotQualityAccountView{accA, accA2, accB}, func(i *GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]) {
		i.EligibleFirstPrimaryDispatch = true
	})
	result, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 首协议组探索预留胜出；第二协议组仅追加排序结果。
	if result.DispatchIntent != "same_tier_exploration" || result.ExplorationStatus != "reserved" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Accounts) != 3 {
		t.Fatalf("accounts = %+v", result.Accounts)
	}
	found := false
	for _, event := range observer.snapshot() {
		if event.Kind == "exploration" && event.Outcome == "reserved" {
			found = true
		}
	}
	if !found {
		t.Fatalf("exploration observation missing: %+v", observer.snapshot())
	}
}

// ---------------------------------------------------------------------------
// runtime.go：GetGatewayHotQualityRuntime 的 Redis 客户端错误臂。
// ---------------------------------------------------------------------------

func TestW14EGetGatewayHotQualityRuntimeRedisClientError(t *testing.T) {
	t.Cleanup(ResetGatewayHotQualityRuntimeForTest)
	ResetGatewayHotQualityRuntimeForTest()
	if _, err := GetGatewayHotQualityRuntime(context.Background(), RuntimeDriverConfig{
		RuntimeMode:        "performance",
		RuntimeStateDriver: "redis",
		RedisStateURL:      "://w14e-not-a-url",
		RedisNamespace:     "w14e-ns",
	}); err == nil {
		t.Fatal("invalid redis url must fail the runtime build")
	}
	// 空命名空间：热质量 Redis store 构造必须拒绝。
	server := miniredis.RunT(t)
	if _, err := GetGatewayHotQualityRuntime(context.Background(), RuntimeDriverConfig{
		RuntimeMode:        "performance",
		RuntimeStateDriver: "redis",
		RedisStateURL:      "redis://" + server.Addr(),
		RedisNamespace:     " ",
	}); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("empty namespace err = %v", err)
	}
	ResetGatewayHotQualityRuntimeForTest()
}

// ---------------------------------------------------------------------------
// redis_store.go：构造参数校验、输入校验、mock runner 错误臂。
// ---------------------------------------------------------------------------

func TestW14ERedisHotQualityStoreConstructorArms(t *testing.T) {
	_, client := newTestRedis(t)
	zeroInt := 0
	zeroTTL := int64(0)
	cases := []struct {
		name    string
		options RedisHotQualityStoreOptions
	}{
		{"keyCapacity zero", RedisHotQualityStoreOptions{Namespace: "w14e", KeyCapacity: &zeroInt}},
		{"attemptCapacity zero", RedisHotQualityStoreOptions{Namespace: "w14e", AttemptCapacity: &zeroInt}},
		{"keyTtl zero", RedisHotQualityStoreOptions{Namespace: "w14e", KeyTtlMs: &zeroTTL}},
		{"terminalTtl zero", RedisHotQualityStoreOptions{Namespace: "w14e", TerminalTtlMs: &zeroTTL}},
	}
	for _, testCase := range cases {
		if _, err := NewRedisHotQualityStore(NewRedisScriptRunner(client), testCase.options); err == nil {
			t.Fatalf("%s: expected error", testCase.name)
		}
	}
	// 缺省 Now 必须落到 wall clock 分支。
	store, err := NewRedisHotQualityStore(NewRedisScriptRunner(client), RedisHotQualityStoreOptions{Namespace: "w14e"})
	if err != nil || store.now == nil || store.now() <= 0 {
		t.Fatalf("default now missing: %v", err)
	}
	// 空白 name 触发 safeRedisName 的回退名。
	fallbackStore, err := NewRedisHotQualityStore(NewRedisScriptRunner(client), RedisHotQualityStoreOptions{Namespace: "w14e", Name: "  "})
	if err != nil || fallbackStore.Keys().Prefix != "juhe-ai:w14e:hot-quality:gateway-hot-quality" {
		t.Fatalf("fallback name prefix = %s, %v", fallbackStore.Keys().Prefix, err)
	}
}

func TestW14ERedisHotQualityStoreInputValidationArms(t *testing.T) {
	store, _ := newTestRedisHotQualityStore(t, "w14e")
	ctx := context.Background()
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "w14e-a", Scope: HotQualityScope{}}); err == nil {
		t.Fatal("invalid scope must fail")
	}
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: " ", Scope: testScope("acc-1")}); err == nil {
		t.Fatal("empty attemptId must fail")
	}
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "w14e-a", Scope: testScope("acc-1"), NowMs: int64Ptr(-1)}); err == nil {
		t.Fatal("negative nowMs must fail")
	}
}

func TestW14ERedisHotQualityStoreRunnerErrorArms(t *testing.T) {
	ctx := context.Background()

	t.Run("get input validation", func(t *testing.T) {
		store, err := NewRedisHotQualityStore(newMockScriptRunner(t, nil), RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := store.Get(ctx, HotQualityScope{}, nil); err == nil {
			t.Fatal("invalid scope must fail")
		}
		if _, err := store.Get(ctx, testScope("acc-1"), int64Ptr(-1)); err == nil {
			t.Fatal("negative nowMs must fail")
		}
	})

	t.Run("record terminal eval error", func(t *testing.T) {
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local requested_entry_key", err: errors.New("w14e redis down")},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
			AttemptID: "w14e-a", Scope: testScope("acc-1"), TerminalOutcomeID: "w14e-t",
			OutcomeClass: TerminalOutcomeTimeout, FailureScope: FailureScopeNone, Source: TerminalSourceRequestLifecycle,
		}); err == nil || err.Error() != "w14e redis down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("get eval error", func(t *testing.T) {
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local entry = cjson.decode", err: errors.New("w14e redis down")},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := store.Get(ctx, testScope("acc-1"), nil); err == nil || err.Error() != "w14e redis down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("get nil reply", func(t *testing.T) {
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local entry = cjson.decode", reply: nil},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		snapshot, err := store.Get(ctx, testScope("acc-1"), nil)
		if err != nil || snapshot != nil {
			t.Fatalf("nil reply must map to nil snapshot: %+v, %v", snapshot, err)
		}
	})

	t.Run("get garbage json", func(t *testing.T) {
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local entry = cjson.decode", reply: "{not-json"},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := store.Get(ctx, testScope("acc-1"), nil); err == nil || !strings.Contains(err.Error(), "entry 结构无效") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("get non numeric bucket keys skipped", func(t *testing.T) {
		scopeText := scopeJSON(testScope("acc-1"))
		entryJSON := fmt.Sprintf(`{"scopeKey":"sk","scope":%s,"buckets":{"junk":{"minuteStartedAtMs":960000}},"expiresAtMs":3400000}`, scopeText)
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local entry = cjson.decode", reply: entryJSON},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		snapshot, err := store.Get(ctx, testScope("acc-1"), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snapshot == nil || len(snapshot.MinuteBuckets) != 0 {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	})

	t.Run("stats invalid numeric values", func(t *testing.T) {
		negative := `{"keyCount":1,"attemptIdentityCount":2,"terminalIdentityCount":3,"keyCreationRefusals":0,"highCardinalityDegradations":-1}`
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "ZREMRANGEBYSCORE", reply: negative},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := store.Stats(ctx, nil); err == nil || !strings.Contains(err.Error(), "highCardinalityDegradations") {
			t.Fatalf("err = %v", err)
		}

		negative2 := `{"keyCount":1,"attemptIdentityCount":2,"terminalIdentityCount":3,"keyCreationRefusals":0,"attemptCapacityRefusals":-2}`
		runner2 := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "ZREMRANGEBYSCORE", reply: negative2},
		})
		store2, err := NewRedisHotQualityStore(runner2, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := store2.Stats(ctx, nil); err == nil || !strings.Contains(err.Error(), "attemptCapacityRefusals") {
			t.Fatalf("err = %v", err)
		}

		negative3 := `{"keyCount":1,"attemptIdentityCount":2,"terminalIdentityCount":3,"terminalQualityKeyMisses":-3}`
		runner3 := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "ZREMRANGEBYSCORE", reply: negative3},
		})
		store3, err := NewRedisHotQualityStore(runner3, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := store3.Stats(ctx, nil); err == nil || !strings.Contains(err.Error(), "terminalQualityKeyMisses") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("mutate invalid scope and reply", func(t *testing.T) {
		store, _ := newTestRedisHotQualityStore(t, "w14e")
		if _, err := store.mutate(ctx, "record_attempt", &redisHotQualityMutationPayload{Operation: "record_attempt", AttemptID: "w14e-a", NowMs: 1_000_000}); err == nil {
			t.Fatal("mutate with empty scope must fail")
		}

		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local requested_entry_key", reply: "not-json-at-all"},
		})
		broken, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := broken.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "w14e-a", Scope: testScope("acc-1")}); err == nil || !strings.Contains(err.Error(), "mutation 返回值无效") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// redis_store.go：GetRedisClient 全分支。
// ---------------------------------------------------------------------------

func TestW14EGetRedisClientArms(t *testing.T) {
	ctx := context.Background()
	if _, err := GetRedisClient(ctx, "   "); err == nil || err.Error() != "Redis 连接串不能为空" {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := GetRedisClient(ctx, "://w14e-bad"); err == nil {
		t.Fatal("parse failure expected")
	}
	// 无监听端口：Ping 失败必须关闭并返回错误。
	if _, err := GetRedisClient(ctx, "redis://127.0.0.1:1"); err == nil {
		t.Fatal("ping failure expected")
	}
	server := miniredis.RunT(t)
	client, err := GetRedisClient(ctx, "redis://"+server.Addr()+"/0")
	if err != nil || client == nil {
		t.Fatalf("healthy url err = %v", err)
	}
	cached, err := GetRedisClient(ctx, "redis://"+server.Addr()+"/0")
	if err != nil || cached != client {
		t.Fatal("same url must hit the client cache")
	}
}

// ---------------------------------------------------------------------------
// exploration_redis_store.go：构造、输入与 wire 解码错误臂。
// ---------------------------------------------------------------------------

func TestW14ERedisExplorationStoreConstructorArms(t *testing.T) {
	_, client := newTestRedis(t)
	zeroTTL := int64(0)
	zeroInt := 0
	if _, err := NewRedisSameTierExplorationStore(NewRedisScriptRunner(client), RedisSameTierExplorationStoreOptions{Namespace: " "}); err == nil {
		t.Fatal("empty namespace must fail")
	}
	if _, err := NewRedisSameTierExplorationStore(NewRedisScriptRunner(client), RedisSameTierExplorationStoreOptions{Namespace: "w14e", StateTtlMs: &zeroTTL}); err == nil {
		t.Fatal("zero ttl must fail")
	}
	if _, err := NewRedisSameTierExplorationStore(NewRedisScriptRunner(client), RedisSameTierExplorationStoreOptions{Namespace: "w14e", PoolCapacity: &zeroInt}); err == nil {
		t.Fatal("zero pool capacity must fail")
	}
	store, err := NewRedisSameTierExplorationStore(NewRedisScriptRunner(client), RedisSameTierExplorationStoreOptions{Namespace: "w14e"})
	if err != nil || store.now == nil || store.now() <= 0 {
		t.Fatalf("default now missing: %v", err)
	}
}

func w14eRedisExplorationStore(t *testing.T, reply interface{}, evalErr error) *RedisSameTierExplorationStore {
	t.Helper()
	runner := newMockScriptRunner(t, []mockScriptCall{
		{scriptMarker: "local operation = ARGV[5]", reply: reply, err: evalErr},
	})
	store, err := NewRedisSameTierExplorationStore(runner, RedisSameTierExplorationStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return store
}

func TestW14ERedisExplorationStoreWireArms(t *testing.T) {
	ctx := context.Background()

	if _, err := w14eRedisExplorationStore(t, nil, errors.New("w14e eval boom")).Get(ctx, SameTierExplorationGetInput{PoolKey: "p"}); err == nil || err.Error() != "w14e eval boom" {
		t.Fatalf("eval error lost: %v", err)
	}

	// 非 JSON 返回值。
	if _, err := w14eRedisExplorationStore(t, "not-json", nil).Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "p", AccrualToken: "t"}); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("garbage reply err = %v", err)
	}

	// state 缺失。
	if _, err := w14eRedisExplorationStore(t, `{"status":"read"}`, nil).Get(ctx, SameTierExplorationGetInput{PoolKey: "p"}); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("nil state err = %v", err)
	}

	// wire 数组字段解码失败（cooldown / reservations / accruedTokens / settled）。
	brokenCooldown := `{"status":"read","state":{"poolKey":"p","cooldownUntilMsByRuntimeKey":"junk"}}`
	if _, err := w14eRedisExplorationStore(t, brokenCooldown, nil).Get(ctx, SameTierExplorationGetInput{PoolKey: "p"}); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("cooldown err = %v", err)
	}
	brokenReservations := `{"status":"read","state":{"poolKey":"p","reservations":"junk"}}`
	if _, err := w14eRedisExplorationStore(t, brokenReservations, nil).Get(ctx, SameTierExplorationGetInput{PoolKey: "p"}); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("reservations err = %v", err)
	}
	brokenAccrued := `{"status":"read","state":{"poolKey":"p","accruedTokens":"junk"}}`
	if _, err := w14eRedisExplorationStore(t, brokenAccrued, nil).Get(ctx, SameTierExplorationGetInput{PoolKey: "p"}); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("accrued err = %v", err)
	}
	brokenSettled := `{"status":"read","state":{"poolKey":"p","settledReservationIds":"junk"}}`
	if _, err := w14eRedisExplorationStore(t, brokenSettled, nil).Get(ctx, SameTierExplorationGetInput{PoolKey: "p"}); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("settled err = %v", err)
	}

	// wire 状态通过解码但 normalize 失败（credit 超出上限）。
	overCredit := `{"status":"read","state":{"poolKey":"p","credit":5,"expiresAtMs":2000000}}`
	if _, err := w14eRedisExplorationStore(t, overCredit, nil).Get(ctx, SameTierExplorationGetInput{PoolKey: "p"}); err == nil || !strings.Contains(err.Error(), "credit 超出范围") {
		t.Fatalf("credit err = %v", err)
	}

	// mutate 输入校验：空 poolKey。
	store, err := NewRedisSameTierExplorationStore(newMockScriptRunner(t, nil), RedisSameTierExplorationStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: " "}); err == nil {
		t.Fatal("empty poolKey must fail")
	}
	if _, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "p", NowMs: int64Ptr(-1)}); err == nil {
		t.Fatal("negative nowMs must fail")
	}
}

func TestW14ERedisExplorationStoreReserveValidation(t *testing.T) {
	_, client := newTestRedis(t)
	store, err := NewRedisSameTierExplorationStore(NewRedisScriptRunner(client), RedisSameTierExplorationStoreOptions{Namespace: "w14e", Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.Reserve(context.Background(), SameTierExplorationReserveInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: -1}); err == nil {
		t.Fatal("negative lease must fail")
	}
	if _, err := store.Reserve(context.Background(), SameTierExplorationReserveInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 3_400_001}); err == nil {
		t.Fatal("lease beyond ttl must fail")
	}
}

func TestW14EDecodeJSONHelpersInvalid(t *testing.T) {
	if _, err := decodeJSONArray[string](nil); err != nil {
		t.Fatalf("nil raw must decode empty: %v", err)
	}
	if _, err := decodeJSONArray[string]([]byte("{bad")); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("err = %v", err)
	}
	if _, err := decodeJSONInt64Map([]byte("{bad")); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// exploration_memory_store.go：错误臂、容量淘汰与 settle 计数细节。
// ---------------------------------------------------------------------------

func TestW14EMemoryExplorationStoreValidationArms(t *testing.T) {
	store, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := context.Background()
	if _, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: " "}); err == nil {
		t.Fatal("empty poolKey must fail")
	}
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "p", AccrualToken: "t", NowMs: int64Ptr(-1)}); err == nil {
		t.Fatal("negative nowMs must fail")
	}
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: " ", AccrualToken: "t"}); err == nil {
		t.Fatal("empty poolKey accrue must fail")
	}
	if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 1_010_000, NowMs: int64Ptr(-1)}); err == nil {
		t.Fatal("negative nowMs reserve must fail")
	}
	if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: -5, NowMs: int64Ptr(1_000_000)}); err == nil {
		t.Fatal("negative lease must fail")
	}
	if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: " ", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 1_010_000}); err == nil {
		t.Fatal("empty poolKey reserve must fail")
	}
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: "a", NowMs: int64Ptr(-1)}); err == nil {
		t.Fatal("negative nowMs settle must fail")
	}
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: " "}); err == nil {
		t.Fatal("empty runtime key settle must fail")
	}
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: " ", ReservationID: "r", AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("empty poolKey settle must fail")
	}
}

func TestW14EMemoryExplorationStoreReserveCooldownCapacity(t *testing.T) {
	store, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	poolKey := "w14e-cooldown-pool"
	// 直接构造满员 cooldown 表（Mock 内部状态，避免 2048 次循环写入）。
	cooldown := make(map[string]int64, SameTierExplorationIdentityCapacity)
	for index := 0; index < SameTierExplorationIdentityCapacity; index++ {
		cooldown[fmt.Sprintf("acc-%d", index)] = 2_000_000
	}
	store.states[poolKey] = &SameTierExplorationState{
		PoolKey:                     poolKey,
		Credit:                      1,
		ExpiresAtMs:                 2_000_000,
		CooldownUntilMsByRuntimeKey: cooldown,
	}
	result, err := store.Reserve(context.Background(), SameTierExplorationReserveInput{
		PoolKey: poolKey, ReservationID: "w14e-r1", AccountRuntimeKey: "acc-new", LeaseUntilMs: 1_010_000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ExplorationReservationTargetCooldown {
		t.Fatalf("status = %s", result.Status)
	}
}

func TestW14EMemoryExplorationStoreSettleAccounting(t *testing.T) {
	store, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	poolKey := "w14e-settle-pool"
	// 两个在途预留 + 已有 cooldown + 满额 settled 窗口 + credit 为 0 + cursor 溢出。
	settled := make([]string, 0, SameTierExplorationIdentityCapacity+1)
	for index := 0; index < SameTierExplorationIdentityCapacity+1; index++ {
		settled = append(settled, fmt.Sprintf("old-%d", index))
	}
	store.states[poolKey] = &SameTierExplorationState{
		PoolKey: poolKey,
		Cursor:  maxSafeInteger,
		Reservations: []SameTierExplorationReservation{
			{ReservationID: "w14e-keep", AccountRuntimeKey: "acc-keep", LeaseUntilMs: 2_000_000},
			{ReservationID: "w14e-settle", AccountRuntimeKey: "acc-settle", LeaseUntilMs: 2_000_000},
		},
		CooldownUntilMsByRuntimeKey: map[string]int64{"acc-keep": 2_000_000},
		SettledReservationIDs:       settled,
		ExpiresAtMs:                 2_000_000,
	}
	result, err := store.Settle(context.Background(), SameTierExplorationSettleInput{
		PoolKey: poolKey, ReservationID: "w14e-settle", AccountRuntimeKey: "acc-settle", Outcome: "dispatched",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ExplorationSettlementApplied {
		t.Fatalf("status = %s", result.Status)
	}
	state := result.State
	if len(state.Reservations) != 1 || state.Reservations[0].ReservationID != "w14e-keep" {
		t.Fatalf("reservations = %+v", state.Reservations)
	}
	// credit 0 下 dispatched 结算被钳制到 0，cursor 溢出回绕为 0。
	if state.Credit != 0 || state.Cursor != 0 {
		t.Fatalf("credit = %v cursor = %v", state.Credit, state.Cursor)
	}
	if state.CooldownUntilMsByRuntimeKey["acc-settle"] != 1_000_000+SameTierExplorationTargetCooldownMS {
		t.Fatalf("cooldown = %v", state.CooldownUntilMsByRuntimeKey)
	}
	// settled 窗口被截断到容量上限且包含刚结算的 id。
	if len(state.SettledReservationIDs) != SameTierExplorationIdentityCapacity {
		t.Fatalf("settled len = %d", len(state.SettledReservationIDs))
	}
	if state.SettledReservationIDs[len(state.SettledReservationIDs)-1] != "w14e-settle" {
		t.Fatalf("settled tail = %v", state.SettledReservationIDs[len(state.SettledReservationIDs)-1])
	}
}

func TestW14EMemoryExplorationStoreLoadNormalizeErrorAndEviction(t *testing.T) {
	store, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{Now: func() int64 { return 1_000_000 }, PoolCapacity: intPtr(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := context.Background()
	// 非法内部状态：credit 超上限，load 归一化必须报错。
	store.states["w14e-bad"] = &SameTierExplorationState{PoolKey: "w14e-bad", Credit: 5, ExpiresAtMs: 2_000_000}
	if _, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "w14e-bad"}); err == nil || !strings.Contains(err.Error(), "credit 超出范围") {
		t.Fatalf("normalize err = %v", err)
	}
	delete(store.states, "w14e-bad")
	// 容量淘汰：过期池被清掉后新池可写入。
	store.states["w14e-expired"] = &SameTierExplorationState{PoolKey: "w14e-expired", ExpiresAtMs: 500_000}
	store.states["w14e-live"] = &SameTierExplorationState{PoolKey: "w14e-live", ExpiresAtMs: 2_000_000}
	if _, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "w14e-third"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := store.states["w14e-expired"]; exists {
		t.Fatal("expired pool must be evicted")
	}
	if _, exists := store.states["w14e-third"]; !exists {
		t.Fatal("third pool must be stored after eviction")
	}
	// 容量满且全部有效：返回未持久化的空状态。
	if _, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "w14e-fourth"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := store.states["w14e-fourth"]; exists {
		t.Fatal("fourth pool must not be stored at capacity")
	}
}

// ---------------------------------------------------------------------------
// exploration.go：归一化错误臂。
// ---------------------------------------------------------------------------

func TestW14EExplorationNormalizeArms(t *testing.T) {
	if _, err := EmptySameTierExplorationState(" ", 1_000); err == nil || err.Error() != "poolKey 必须是 1 到 512 字符" {
		t.Fatalf("empty pool err = %v", err)
	}

	base := SameTierExplorationState{PoolKey: "p", Credit: 0.5, Cursor: 1, ExpiresAtMs: 2_000}

	// 零值预留被跳过。
	state, err := NormalizeSameTierExplorationState(SameTierExplorationState{
		PoolKey: base.PoolKey, Credit: base.Credit, Cursor: base.Cursor, ExpiresAtMs: base.ExpiresAtMs,
		Reservations: []SameTierExplorationReservation{{}},
	}, 1_500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.Reservations) != 0 {
		t.Fatalf("zero reservation must be dropped: %+v", state.Reservations)
	}

	errorCases := []struct {
		name   string
		mutate func(*SameTierExplorationState)
	}{
		{"reservation empty id", func(s *SameTierExplorationState) {
			s.Reservations = []SameTierExplorationReservation{{ReservationID: " ", AccountRuntimeKey: "a", LeaseUntilMs: 2_000}}
		}},
		{"reservation empty runtime key", func(s *SameTierExplorationState) {
			s.Reservations = []SameTierExplorationReservation{{ReservationID: "r", AccountRuntimeKey: " ", LeaseUntilMs: 2_000}}
		}},
		{"reservation negative lease", func(s *SameTierExplorationState) {
			s.Reservations = []SameTierExplorationReservation{{ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: -1}}
		}},
		{"cooldown empty key", func(s *SameTierExplorationState) { s.CooldownUntilMsByRuntimeKey = map[string]int64{" ": 2_000} }},

		{"accrued empty token", func(s *SameTierExplorationState) { s.AccruedTokens = []string{"ok", ""} }},
		{"settled empty id", func(s *SameTierExplorationState) { s.SettledReservationIDs = []string{""} }},
		{"negative expiresAtMs", func(s *SameTierExplorationState) { s.ExpiresAtMs = -1 }},
		{"negative cursor", func(s *SameTierExplorationState) { s.Cursor = -3 }},
	}
	for _, errorCase := range errorCases {
		state := base
		state.Reservations = nil
		state.CooldownUntilMsByRuntimeKey = nil
		state.AccruedTokens = nil
		state.SettledReservationIDs = nil
		errorCase.mutate(&state)
		if _, err := NormalizeSameTierExplorationState(state, 1_500); err == nil {
			t.Fatalf("%s: expected error", errorCase.name)
		}
	}

	if _, err := uniqueBoundedKeys([]string{" "}, 4); err == nil {
		t.Fatal("blank identity must fail")
	}
	// 负 nowMs 下未过期的负 cooldown 值才会走到数值校验分支。
	negativeNow := base
	negativeNow.CooldownUntilMsByRuntimeKey = map[string]int64{"a": -3}
	if _, err := NormalizeSameTierExplorationState(negativeNow, -5); err == nil {
		t.Fatal("negative cooldown until must fail under negative nowMs")
	}
	// limit 截断：从最新端扫描去重，保留最近 limit 个 identity。
	keys, err := uniqueBoundedKeys([]string{"a", "b", "c", "a"}, 2)
	if err != nil || len(keys) != 2 || keys[0] != "c" || keys[1] != "a" {
		t.Fatalf("keys = %v, %v", keys, err)
	}
}

// ---------------------------------------------------------------------------
// attempt_lifecycle.go：缺失 protocolProfile 与 MarkFirstByte(nil)。
// ---------------------------------------------------------------------------

func TestW14ELifecycleMissingProtocolAndNilFirstByte(t *testing.T) {
	runtime, _, _ := newTestRuntime(&mockHotQualityStore{t: t}, nil)
	if _, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{
		Runtime: runtime, AttemptID: "w14e-at",
		Account: GatewayHotQualityAccountView{ID: "acc", ProviderProtocolProfileID: " "},
	}); err == nil || err.Error() != "热质量 protocolProfile 不能为空" {
		t.Fatalf("err = %v", err)
	}

	// MarkFirstByte(nil)：显式 nil 必须被忽略且不 panic。
	lifecycle, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{
		Runtime: runtime, AttemptID: "w14e-at2",
		Account: GatewayHotQualityAccountView{ID: "acc", ProviderProtocolProfileID: "openai:2024"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lifecycle.MarkFirstByte(nil)
	invalid := -1.5
	lifecycle.MarkFirstByte(&invalid)
	lifecycle.RecordTerminal(context.Background(), GatewayHotQualityTerminalInput{OutcomeClass: TerminalOutcomeCompletedResponse})
	if len(lifecycle.runtime.HotQualityStore.(*mockHotQualityStore).recordedTerminals()) != 1 {
		t.Fatal("terminal must be recorded once")
	}
	terminal := lifecycle.runtime.HotQualityStore.(*mockHotQualityStore).recordedTerminals()[0]
	if terminal.FirstByteMs != nil {
		t.Fatalf("nil/invalid first byte must stay nil: %v", *terminal.FirstByteMs)
	}
}

// ---------------------------------------------------------------------------
// snapshot.go / store.go：窗口跳过、时间戳合并、scope/首字错误臂。
// ---------------------------------------------------------------------------

func TestW14ESnapshotWindowSkipAndTimestampMerge(t *testing.T) {
	currentMinute := int64(100)
	// -9 分钟的桶对 5m 窗口必须被跳过（仍通过 30 桶过滤器与 10m 窗口）。
	oldBucketMinute := currentMinute - 9
	snapshot := CreateHotQualitySnapshot(HotQualitySnapshotState{
		ScopeKey: "w14e-snap",
		Buckets: []HotQualityBucketState{
			{MinuteStartedAtMs: oldBucketMinute * 60_000, HotQualityCounters: HotQualityCounters{CompletedResponses: 7, LastCompletedAtMs: int64Ptr(5)}},
			{MinuteStartedAtMs: currentMinute * 60_000, HotQualityCounters: HotQualityCounters{CompletedResponses: 1, LastCompletedAtMs: int64Ptr(9)}},
		},
		ExpiresAtMs: 200_000,
	}, currentMinute*60_000)
	if snapshot.Window5m.CompletedResponses != 1 {
		t.Fatalf("5m window must skip the stale bucket: %+v", snapshot.Window5m.HotQualityCounters)
	}
	if snapshot.Window10m.CompletedResponses != 8 {
		t.Fatalf("10m window must merge both buckets: %d", snapshot.Window10m.CompletedResponses)
	}
	if snapshot.Window5m.LastCompletedAtMs == nil || *snapshot.Window5m.LastCompletedAtMs != 9 {
		t.Fatalf("5m last completed = %v", snapshot.Window5m.LastCompletedAtMs)
	}

	// maximumInt64Ptr 双非nil：left > right 取 left；equal 取 left。
	if got := maximumInt64Ptr(int64Ptr(7), int64Ptr(3)); *got != 7 {
		t.Fatalf("left-dominant = %v", *got)
	}
	if got := maximumInt64Ptr(int64Ptr(4), int64Ptr(4)); *got != 4 {
		t.Fatalf("equal = %v", *got)
	}
}

func TestW14EStoreScopeArms(t *testing.T) {
	if _, err := HotQualityScopeKey(HotQualityScope{}); err == nil {
		t.Fatal("empty scope must fail")
	}
	if _, err := ProtocolHotQualityScope(HotQualityScope{}); err == nil {
		t.Fatal("empty scope must fail protocol fallback")
	}
	if got, err := NormalizedFirstByteMs(float64(maxSafeInteger) * 2); err != nil || got != maxSafeInteger {
		t.Fatalf("clamp = %v, %v", got, err)
	}
	if _, err := NormalizedFirstByteMs(math.NaN()); err == nil {
		t.Fatal("NaN must fail")
	}
}

// ---------------------------------------------------------------------------
// speedfirst_body_admission.go：ClearForTest 唤醒、陈旧 item、nil item。
// ---------------------------------------------------------------------------

func TestW14EBodyAdmissionClearWithQueuedItem(t *testing.T) {
	registry := NewSpeedFirstBodyAdmissionRegistry()
	baseClock := int64(1_000)
	registry.SetClock(func() time.Time { return time.UnixMilli(baseClock) })

	ctx := context.Background()
	first := registry.Acquire(ctx, SpeedFirstBodyAdmissionInput{SystemAccountID: "s", RouteStrategyID: "r", GroupID: "g", APIKeyID: "key-1", Capacity: 1, MaxQueueWaitMs: 60_000, MaxQueueSize: 4, PerAPIKeyQueueLimit: 4})
	if !first.Acquired || first.Release == nil {
		t.Fatalf("first acquire = %+v", first)
	}

	queuedDone := make(chan SpeedFirstBodyAdmissionDecision, 1)
	go func() {
		queuedDone <- registry.Acquire(ctx, SpeedFirstBodyAdmissionInput{SystemAccountID: "s", RouteStrategyID: "r", GroupID: "g", APIKeyID: "key-2", Capacity: 1, MaxQueueWaitMs: 60_000, MaxQueueSize: 4, PerAPIKeyQueueLimit: 4})
	}()
	deadline := time.Now().Add(time.Second)
	for len(registry.states) == 0 || len(registry.states["s:r:g"].queue) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("queued item never appeared")
		}
		time.Sleep(time.Millisecond)
	}

	baseClock = 1_500
	registry.ClearForTest()
	select {
	case decision := <-queuedDone:
		if decision.Acquired || decision.Reason != BodyAdmissionRejectAborted {
			t.Fatalf("clear decision = %+v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("clear must resolve queued items")
	}
	first.Release()
	if entries := registry.Snapshot(); len(entries) != 0 {
		t.Fatalf("clear must drop states: %+v", entries)
	}
}

func TestW14EBodyAdmissionStaleAndNilQueueItems(t *testing.T) {
	registry := NewSpeedFirstBodyAdmissionRegistry()
	state := createState("w14e:key", 2)
	stale := &bodyAdmissionQueueItem{apiKeyID: "k", done: make(chan SpeedFirstBodyAdmissionDecision, 1), finished: make(chan struct{})}
	// 陈旧 item 不在队列中：complete 必须是 no-op，contains 必须为 false。
	registry.completeQueuedItem(state, stale, SpeedFirstBodyAdmissionDecision{Acquired: false, Reason: BodyAdmissionRejectAborted})
	if bodyAdmissionQueueContains(state.queue, stale) {
		t.Fatal("stale item must not be queued")
	}
	select {
	case <-stale.done:
		t.Fatal("stale item must not be resolved")
	default:
	}

	// 唤醒循环遇到 nil item 必须安全返回。
	nilState := createState("w14e:nil", 2)
	nilState.queue = []*bodyAdmissionQueueItem{nil}
	registry.states[nilState.key] = nilState
	registry.mu.Lock()
	registry.wakeQueuedItemsLocked(nilState)
	registry.mu.Unlock()
}

// ---------------------------------------------------------------------------
// speedfirst_cutover_reservation.go：policy 数值回退。
// ---------------------------------------------------------------------------

func TestW14ECutoverNumericPolicyFallback(t *testing.T) {
	policy := gatewayruntimecache.GroupSchedulingPolicy{"imageLaneMaxConcurrency": "not-a-number"}
	if got := EffectiveImageLaneConcurrencyLimit(5, policy); got != 5 {
		t.Fatalf("non numeric policy must keep hard limit: %d", got)
	}
	int64Policy := gatewayruntimecache.GroupSchedulingPolicy{"imageLaneMaxConcurrency": int64(3)}
	if got := EffectiveImageLaneConcurrencyLimit(9, int64Policy); got != 3 {
		t.Fatalf("int64 policy = %d", got)
	}
}

// ---------------------------------------------------------------------------
// memory_store.go：构造、输入校验与终态 owner 过期清理。
// ---------------------------------------------------------------------------

func TestW14EMemoryHotQualityStoreConstructorArms(t *testing.T) {
	zeroInt := 0
	zeroTTL := int64(0)
	if _, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{KeyTtlMs: &zeroTTL}); err == nil {
		t.Fatal("zero key ttl must fail")
	}
	if _, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{TerminalTtlMs: &zeroTTL}); err == nil {
		t.Fatal("zero terminal ttl must fail")
	}
	if _, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{KeyCapacity: &zeroInt}); err == nil {
		t.Fatal("zero key capacity must fail")
	}
	store, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.now == nil || store.now() <= 0 {
		t.Fatalf("default now missing")
	}
	if store.attemptCapacity != 100_000 || store.keyCapacity != 10_000 {
		t.Fatalf("defaults = %+v", store)
	}
	zeroInt2 := 0
	if _, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{AttemptCapacity: &zeroInt2}); err == nil {
		t.Fatal("zero attempt capacity must fail")
	}
}

func TestW14EMemoryHotQualityStoreValidationArms(t *testing.T) {
	store := newTestMemoryStore(t, nil)
	ctx := context.Background()
	scope := testScope("acc-1")
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: " ", Scope: scope}); err == nil {
		t.Fatal("empty attemptId must fail")
	}
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "w14e-a", Scope: HotQualityScope{}}); err == nil {
		t.Fatal("invalid scope must fail")
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{AttemptID: "w14e-a", Scope: scope, NowMs: int64Ptr(-1)}); err == nil {
		t.Fatal("negative nowMs must fail")
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{AttemptID: " ", Scope: scope}); err == nil {
		t.Fatal("empty attemptId terminal must fail")
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{AttemptID: "w14e-a", Scope: HotQualityScope{}}); err == nil {
		t.Fatal("invalid scope terminal must fail")
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{AttemptID: "w14e-a", Scope: scope, TerminalOutcomeID: " "}); err == nil {
		t.Fatal("empty terminalOutcomeId must fail")
	}
	if _, err := store.Get(ctx, HotQualityScope{}, int64Ptr(-1)); err == nil {
		t.Fatal("negative nowMs get must fail")
	}
	if _, err := store.Get(ctx, HotQualityScope{}, nil); err == nil {
		t.Fatal("invalid scope get must fail")
	}
	if _, err := store.GetTerminal(ctx, " ", nil); err == nil {
		t.Fatal("empty attemptId getTerminal must fail")
	}
	if _, err := store.GetTerminal(ctx, "w14e-a", int64Ptr(-1)); err == nil {
		t.Fatal("negative nowMs getTerminal must fail")
	}
	if _, err := store.Stats(ctx, int64Ptr(-1)); err == nil {
		t.Fatal("negative nowMs stats must fail")
	}
}

func TestW14EMemoryHotQualityStoreTerminalOwnerExpiry(t *testing.T) {
	now := int64(1_000_000)
	clock := &now
	store := newTestMemoryStore(t, func(options *MemoryHotQualityStoreOptions) { options.Now = func() int64 { return *clock } })
	ctx := context.Background()
	scope := testScope("acc-1")

	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "w14e-owner-a", Scope: scope, NowMs: clock}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID: "w14e-owner-a", Scope: scope, TerminalOutcomeID: "w14e-owner-t",
		OutcomeClass: TerminalOutcomeTimeout, FailureScope: FailureScopeNone, Source: TerminalSourceRequestLifecycle, NowMs: clock,
	})
	if err != nil || result.Status != TerminalMutationApplied {
		t.Fatalf("terminal result = %+v, %v", result, err)
	}

	// 推进时钟到原 owner 即将过期前：刷新 cleanup 门限。
	*clock += HotQualityTerminalTTLMS - 1_000
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "w14e-owner-b", Scope: scope, NowMs: clock}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 再推进 1.1s：owner A 已过期，但 cleanup 门限未到，owner 清理分支必须接管。
	*clock += 1_100
	result, err = store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID: "w14e-owner-b", Scope: scope, TerminalOutcomeID: "w14e-owner-t",
		OutcomeClass: TerminalOutcomeTimeout, FailureScope: FailureScopeNone, Source: TerminalSourceRequestLifecycle, NowMs: clock,
	})
	if err != nil || result.Status != TerminalMutationApplied {
		t.Fatalf("expired owner must free the outcome id: %+v, %v", result, err)
	}
}
