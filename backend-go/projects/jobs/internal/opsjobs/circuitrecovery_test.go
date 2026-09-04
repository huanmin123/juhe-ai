package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---- 测试辅助 ----

func testClock(t *testing.T) (func() int64, func(int64)) {
	t.Helper()
	current := int64(1_000_000)
	return func() int64 { return current }, func(next int64) { current = next }
}

func accountScope(accountID string) CircuitScope {
	return CircuitScope{Kind: CircuitScopeAccount, AccountRuntimeKey: accountID}
}

func protocolModelScope(accountID, profile, lane, bucket string) CircuitScope {
	return CircuitScope{
		Kind:              CircuitScopeProtocolModel,
		AccountRuntimeKey: accountID,
		ProtocolProfile:   profile,
		RequestLane:       lane,
		ModelBucket:       bucket,
	}
}

func suspectState(t *testing.T, scope CircuitScope, generation int64, dispatchRevision string, nowMS int64) CircuitState {
	t.Helper()
	scopeKey, err := AccountCircuitScopeKey(scope)
	if err != nil {
		t.Fatalf("构造 scopeKey 失败: %v", err)
	}
	required := 2
	count := 0
	retryAt := nowMS
	return CircuitState{
		ScopeKey:                     scopeKey,
		Scope:                        scope,
		Phase:                        CircuitPhaseSuspect,
		Generation:                   generation,
		DispatchRevision:             dispatchRevision,
		TransitionID:                 "seed-suspect",
		ConfirmationFailuresRequired: &required,
		ConfirmationFailureCount:     &count,
		RetryAtMS:                    &retryAt,
		UpdatedAtMS:                  nowMS,
	}
}

func openState(t *testing.T, scope CircuitScope, generation int64, dispatchRevision string, nowMS int64) CircuitState {
	t.Helper()
	state := suspectState(t, scope, generation, dispatchRevision, nowMS)
	state.Phase = CircuitPhaseOpen
	state.BackoffAttempt = 1
	return state
}

func framingCompleteOutcome() TransportProbeOutcome {
	status := 200
	return TransportProbeOutcome{Kind: ProbeOutcomeFramingComplete, StatusCode: &status}
}

func transportIncompleteOutcome(failureKind ProbeFailureKind, statusCode *int) TransportProbeOutcome {
	return TransportProbeOutcome{Kind: ProbeOutcomeTransportIncomplete, FailureKind: failureKind, StatusCode: statusCode}
}

func staticResolver(target CircuitRecoveryProbeTarget, found bool) CircuitRecoveryTargetResolver {
	return func(context.Context, CircuitState) (CircuitRecoveryProbeTarget, bool, error) {
		return target, found, nil
	}
}

func newTestRecoveryService(t *testing.T, store CircuitStore, nowMS func() int64, resolver CircuitRecoveryTargetResolver) *CircuitRecoveryService {
	t.Helper()
	counter := 0
	service, err := NewCircuitRecoveryService(store, resolver, CircuitRecoveryServiceOptions{
		BatchSize:       10,
		Concurrency:     2,
		LeaseDurationMS: 60_000,
		NowMS:           nowMS,
		CreateID: func() string {
			counter++
			return fmt.Sprintf("id-%d", counter)
		},
	})
	if err != nil {
		t.Fatalf("构造恢复服务失败: %v", err)
	}
	return service
}

// ---- 正常恢复：SUSPECT framing_complete → CLOSED ----

func TestCircuitRecoverySweepFramingCompleteClosesSuspect(t *testing.T) {
	nowMS, advance := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	seed := suspectState(t, accountScope("acc-1"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}
	mutations := 0
	service := newTestRecoveryService(t, store, nowMS, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true))
	service.onMutation = func(CircuitRecoveryMutation) { mutations++ }

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.DueCount != 1 || result.LeasedCount != 1 || result.FramingCompleteCount != 1 {
		t.Fatalf("计数不符: %+v", result)
	}
	state, err := store.Get(context.Background(), accountScope("acc-1"), nowMS())
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != CircuitPhaseClosed {
		t.Fatalf("SUSPECT 探针达标后应 CLOSED，got %s", state.Phase)
	}
	if mutations == 0 {
		t.Fatal("mutation 投影回调未被调用")
	}
	advance(nowMS() + 1)
}

// transport_incomplete：确认失败证据（evidence key）按契约写入并回落 SUSPECT。
func TestCircuitRecoveryTransportIncompleteRecordsEvidence(t *testing.T) {
	nowMS, advance := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	seed := suspectState(t, accountScope("acc-2"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}
	status := 502
	service := newTestRecoveryService(t, store, nowMS, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return transportIncompleteOutcome(ProbeFailureTimeout, &status), nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.TransportIncompleteCount != 1 {
		t.Fatalf("transportIncompleteCount = %d", result.TransportIncompleteCount)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-2"), nowMS())
	if state.Phase != CircuitPhaseSuspect {
		t.Fatalf("确认失败(1/2)应保持 SUSPECT，got %s", state.Phase)
	}
	if state.ConfirmationFailureCount == nil || *state.ConfirmationFailureCount != 1 {
		t.Fatalf("confirmationFailureCount 应为 1: %+v", state.ConfirmationFailureCount)
	}
	if len(state.FailureEvidenceKeys) != 1 {
		t.Fatalf("应写入 1 条失败证据: %v", state.FailureEvidenceKeys)
	}
	wantReason := "background_probe:timeout:http_502"
	if state.FailureReason != wantReason {
		t.Fatalf("failureReason = %q, want %q", state.FailureReason, wantReason)
	}
	// evidence key = sha256(background_confirmation:scopeKey:generation:leaseId)
	// 首个 createID 消耗在 leaseId（id-1），第二个在 transitionId（id-2）。
	leaseID := "id-1"
	wantKey := BackgroundConfirmationEvidenceKey(seed, leaseID)
	if state.FailureEvidenceKeys[0] != wantKey {
		t.Fatalf("failureEvidenceKey = %s, want %s", state.FailureEvidenceKeys[0], wantKey)
	}
	advance(nowMS() + 1)
}

// dispatch revision 漂移：围栏释放（fenced）并 replace revision。
func TestCircuitRecoveryDispatchRevisionDriftFences(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	seed := suspectState(t, accountScope("acc-3"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}
	service := newTestRecoveryService(t, store, nowMS, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "9",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			t.Fatal("revision 漂移时不应执行探针")
			return TransportProbeOutcome{}, nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.FencedCount != 1 {
		t.Fatalf("FencedCount = %d, want 1: %+v", result.FencedCount, result)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-3"), nowMS())
	if state.DispatchRevision != "9" {
		t.Fatalf("dispatchRevision 应已被替换为 9: %s", state.DispatchRevision)
	}
}

// 目标缺失：保守释放半开租约（unknown），租约被清除。
func TestCircuitRecoveryMissingTargetReleasesLease(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	seed := suspectState(t, accountScope("acc-4"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}
	service := newTestRecoveryService(t, store, nowMS, staticResolver(CircuitRecoveryProbeTarget{}, false))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.UnknownCount != 1 {
		t.Fatalf("UnknownCount = %d", result.UnknownCount)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-4"), nowMS())
	if state.Lease != nil {
		t.Fatal("unknown 释放后不应残留租约")
	}
}

// HALF_OPEN/其他到期前的阶段不应被本轮 ListDue 选中（skip 计数路径）。
func TestCircuitRecoverySkipsNonEligiblePhase(t *testing.T) {
	nowMS, advance := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	// HALF_OPEN 且租约未到期 → 不在 ListDue 结果中。
	seed := openState(t, accountScope("acc-5"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}
	leaseUntil := nowMS() + 60_000
	leased := seed
	leased.Phase = CircuitPhaseHalfOpen
	leased.TransitionID = "seed-half-open"
	leased.UpdatedAtMS = nowMS() + 1
	leased.HalfOpenOrigin = string(CircuitPhaseOpen)
	leased.Lease = &CircuitLease{Kind: CircuitLeaseHalfOpen, LeaseID: "held", LeaseUntilMS: leaseUntil}
	if _, err := store.Restore(context.Background(), leased, nowMS()+1); err != nil {
		t.Fatal(err)
	}
	// CLOSED 行为永不 due，直接注入 skip 场景：用一个 OPEN 尚未到期状态。
	notDue := suspectState(t, accountScope("acc-6"), 1, "5", nowMS())
	futureRetry := nowMS() + 120_000
	notDue.RetryAtMS = &futureRetry
	if _, err := store.Restore(context.Background(), notDue, nowMS()+2); err != nil {
		t.Fatal(err)
	}
	advance(nowMS() + 30_000)

	result, err := newTestRecoveryService(t, store, nowMS, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true)).Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.DueCount != 0 || result.SkippedCount != 0 {
		t.Fatalf("未到期状态不应被选中: %+v", result)
	}
}

// 探针超过租约 deadline：unknown/task_failure，不进入失败证据。
func TestCircuitRecoveryProbeLeaseDeadlineYieldsUnknown(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	seed := suspectState(t, accountScope("acc-7"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}
	service, err := NewCircuitRecoveryService(store, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(ctx context.Context) (TransportProbeOutcome, error) {
			<-ctx.Done()
			return TransportProbeOutcome{}, ctx.Err()
		},
	}, true), CircuitRecoveryServiceOptions{
		BatchSize:       10,
		LeaseDurationMS: 20,
		NowMS:           nowMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.UnknownCount != 1 {
		t.Fatalf("租约超时应计为 unknown: %+v", result)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-7"), nowMS())
	if state.FailureReason != "" {
		t.Fatalf("租约超时不得写入失败原因: %q", state.FailureReason)
	}
}

// kill-restart 硬门禁：任务中断后租约仍在持久 store；重启（新服务实例）
// 后，租约到期即重新入列并完成续跑。
func TestCircuitRecoveryResumesAfterKillRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	nowMS, advance := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	seed := suspectState(t, accountScope("acc-8"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}

	probeStarted := make(chan struct{})
	firstService := newTestRecoveryService(t, store, nowMS, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			close(probeStarted)
			<-ctx.Done()
			return TransportProbeOutcome{}, ctx.Err()
		},
	}, true))
	sweepDone := make(chan error, 1)
	go func() {
		_, err := firstService.Sweep(ctx)
		sweepDone <- err
	}()
	<-probeStarted
	cancel()
	if err := <-sweepDone; err == nil {
		t.Fatal("被 kill 的 sweep 必须返回错误")
	}

	// 进程重启：时间推进跨过租约 TTL，新实例从持久租约状态续跑。
	// 重启实例使用独立 ID 前缀（与被 kill 进程的幂等键空间隔离）。
	advance(nowMS() + 120_000)
	restartCounter := 0
	secondService, err := NewCircuitRecoveryService(store, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true), CircuitRecoveryServiceOptions{
		BatchSize:       10,
		LeaseDurationMS: 60_000,
		NowMS:           nowMS,
		CreateID: func() string {
			restartCounter++
			return fmt.Sprintf("restart-%d", restartCounter)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := secondService.Sweep(context.Background())
	if err != nil {
		t.Fatalf("重启后 Sweep 失败: %v", err)
	}
	if result.DueCount != 1 || result.FramingCompleteCount != 1 {
		t.Fatalf("重启后续跑计数不符: %+v", result)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-8"), nowMS())
	if state.Phase != CircuitPhaseClosed {
		t.Fatalf("重启续跑后应 CLOSED，got %s", state.Phase)
	}
}

// ---- 纯函数契约 ----

func TestCircuitOutcomeAndFailureReasonMatrix(t *testing.T) {
	status := 503
	cases := []struct {
		name        string
		outcome     TransportProbeOutcome
		wantVerdict CircuitProbeVerdict
		wantReason  string
	}{
		{"framing", framingCompleteOutcome(), CircuitVerdictFramingComplete, ""},
		{"framing semantic 失败", func() TransportProbeOutcome {
			outcome := framingCompleteOutcome()
			failed := false
			outcome.SemanticSuccess = &failed
			return outcome
		}(), CircuitVerdictUnknown, ""},
		{"transport timeout", transportIncompleteOutcome(ProbeFailureTimeout, &status), CircuitVerdictTransportFailure, "background_probe:timeout:http_503"},
		{"transport read 无状态码", transportIncompleteOutcome(ProbeFailureRead, nil), CircuitVerdictTransportFailure, "background_probe:read"},
		{"unknown canceled", TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureCanceled}, CircuitVerdictUnknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CircuitOutcome(tc.outcome); got != tc.wantVerdict {
				t.Fatalf("CircuitOutcome = %s, want %s", got, tc.wantVerdict)
			}
			if got := CircuitFailureReason(tc.outcome); got != tc.wantReason {
				t.Fatalf("CircuitFailureReason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

func TestAccountCircuitScopeKeyEncoding(t *testing.T) {
	cases := []struct {
		name    string
		scope   CircuitScope
		want    string
		wantErr bool
	}{
		{"account", accountScope("acc"), "7:account|3:acc", false},
		{"key", CircuitScope{Kind: CircuitScopeKey, AccountRuntimeKey: "acc", KeyFingerprint: "fp"}, "3:key|3:acc|2:fp", false},
		{"protocol_model", protocolModelScope("acc", "prof", "text", "gpt"), "14:protocol_model|3:acc|4:prof|4:text|3:gpt", false},
		{"非法 lane", protocolModelScope("acc", "prof", "ws", "gpt"), "", true},
		{"空 runtime key", CircuitScope{Kind: CircuitScopeAccount}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AccountCircuitScopeKey(tc.scope)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("scopeKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRecoveryRuntimeIdentity(t *testing.T) {
	cases := []struct {
		runtimeKey string
		wantKind   string
		wantID     string
		ok         bool
	}{
		{"acc-1", "owner", "acc-1", true},
		{"acc-1:authorized:sys-1:grp-1:auth-1", "authorized", "acc-1", true},
		{"acc-1:authorized:sys::auth", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		identity, ok := ParseRecoveryRuntimeIdentity(tc.runtimeKey)
		if ok != tc.ok {
			t.Fatalf("%q ok=%v want %v", tc.runtimeKey, ok, tc.ok)
		}
		if ok && (identity.Kind != tc.wantKind || identity.AccountID != tc.wantID) {
			t.Fatalf("%q = %+v", tc.runtimeKey, identity)
		}
	}
}

func TestCurrentDispatchRevision(t *testing.T) {
	if _, ok := CurrentDispatchRevision(0); ok {
		t.Fatal("0 不应产生 revision")
	}
	if got, ok := CurrentDispatchRevision(42); !ok || got != "42" {
		t.Fatalf("got %q %v", got, ok)
	}
}

// 并发 sweep 在共享内存 store 上 -race 安全。
func TestCircuitRecoveryConcurrentSweepRace(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(50, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		id := "acc-race-" + string(rune('a'+i))
		seed := suspectState(t, accountScope(id), 1, "5", nowMS())
		if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewCircuitRecoveryService(store, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true), CircuitRecoveryServiceOptions{BatchSize: 8, Concurrency: 4, LeaseDurationMS: 60_000, NowMS: nowMS})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.FramingCompleteCount != 8 {
		t.Fatalf("并发恢复计数不符: %+v", result)
	}
}

// resolve 抛错：释放 unknown 并聚合错误。
func TestCircuitRecoveryResolveErrorReleasesLease(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	seed := suspectState(t, accountScope("acc-err"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, nowMS()); err != nil {
		t.Fatal(err)
	}
	resolver := func(context.Context, CircuitState) (CircuitRecoveryProbeTarget, bool, error) {
		return CircuitRecoveryProbeTarget{}, false, errors.New("db down")
	}
	service := newTestRecoveryService(t, store, nowMS, resolver)
	_, sweepErr := service.Sweep(context.Background())
	if sweepErr == nil || !strings.Contains(sweepErr.Error(), "db down") {
		t.Fatalf("应聚合 resolver 错误: %v", sweepErr)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-err"), nowMS())
	if state.Lease != nil {
		t.Fatal("resolver 失败后应释放租约")
	}
	// 给定时间推进避免 unused 提示。
}
