package gatewayproxyhealth

// W43-A（后台自动探测总开关 W3）：速度优先降级的请求级/后台探针边界。
// 契约源 docs/functions/AI账户后台自动探测总开关设计.md §5：手动账户
// latency_degraded 仍可由真实流量建立（排序保留、TTL 恢复），但不写
// nextProbeAt / probe-index；flag=1 默认路径逐字节不变。

import (
	"context"
	"testing"
)

func w43aServiceWithOptions(clock *fakeClock, store *MemoryRuntimeStateStore, disabled func(ctx context.Context, account SuppressibleGatewayAccount) (bool, error)) *LatencyDegradationService {
	return NewLatencyDegradationService(store, clock.Now, LatencyDegradationOptions{
		BackgroundClearConcurrency: 4,
		LockRetryDelay:             func(attempt int) {},
		Random:                     func() float64 { return 0.5 },
		BackgroundProbeDisabled:    disabled,
	})
}

func w43aProbeIndexKeys(t *testing.T, service *LatencyDegradationService) []string {
	t.Helper()
	keys, err := service.loadLatencyStateIndexKeys(context.Background(), latencyStateKeyspaceV1.probeIndexKey())
	if err != nil {
		t.Fatalf("读取 probe-index 失败: %v", err)
	}
	return keys
}

func w43aAllIndexKeys(t *testing.T, service *LatencyDegradationService) []string {
	t.Helper()
	keys, err := service.loadLatencyStateIndexKeys(context.Background(), latencyStateKeyspaceV1.allIndexKey())
	if err != nil {
		t.Fatalf("读取 all-index 失败: %v", err)
	}
	return keys
}

// 手动账户慢样本触发降级：DegradedUntilMs 照常写入（排序保留、TTL 恢复），
// NextProbeAtMs 不写、probe-index 不收录（all-index 仍维护）。
func TestW43AManualAccountDegradationWithoutProbeScheduling(t *testing.T) {
	clock := newFakeClock(1_000_000)
	store := NewMemoryRuntimeStateStore(clock.Now)
	manualAccount := latencyAccount("manual")
	enabledAccount := latencyAccount("enabled")
	disabledIDs := map[string]bool{"manual": true}
	service := w43aServiceWithOptions(clock, store, func(ctx context.Context, account SuppressibleGatewayAccount) (bool, error) {
		return disabledIDs[account.ID], nil
	})
	scope := latencyScope()
	config := speedFirstConfig()

	for i := 0; i < 3; i++ {
		result, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), manualAccount, scope, &config, "")
		if err != nil || result == nil {
			t.Fatalf("慢样本 %d: result=%v err=%v", i, result, err)
		}
	}
	state := wtdLoadState(t, store, scope, manualAccount)
	if state.DegradedUntilMs == nil || *state.DegradedUntilMs <= clock.NowMs() {
		t.Fatalf("手动账户降级必须照常建立（排序保留）: degradedUntilMs=%v", state.DegradedUntilMs)
	}
	if state.NextProbeAtMs != nil {
		t.Fatalf("手动账户不写 nextProbeAt: %+v", state.NextProbeAtMs)
	}
	probeKeys := w43aProbeIndexKeys(t, service)
	for _, key := range probeKeys {
		if key == accountLatencyStateKey(*scope, manualAccount) {
			t.Fatalf("手动账户键不得进入 probe-index: %v", probeKeys)
		}
	}
	allKeys := w43aAllIndexKeys(t, service)
	foundAll := false
	for _, key := range allKeys {
		if key == accountLatencyStateKey(*scope, manualAccount) {
			foundAll = true
		}
	}
	if !foundAll {
		t.Fatalf("手动账户键应进入 all-index（降级排序读取按直键不受影响）: %v", allKeys)
	}
	// probe-index 无候选：手动降级状态不产生恢复探针候选。
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.AccountID == manualAccount.ID {
			t.Fatalf("手动账户不得出现在恢复探针候选中: %+v", candidates)
		}
	}

	// 对照组：flag=1 默认路径行为不变（nextProbeAt 写入 + probe-index 收录）。
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), enabledAccount, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	enabledState := wtdLoadState(t, store, scope, enabledAccount)
	if enabledState.NextProbeAtMs == nil {
		t.Fatalf("默认路径必须写 nextProbeAt（flag=1 行为不变）")
	}
	foundProbe := false
	for _, key := range w43aProbeIndexKeys(t, service) {
		if key == accountLatencyStateKey(*scope, enabledAccount) {
			foundProbe = true
		}
	}
	if !foundProbe {
		t.Fatalf("默认路径键应进入 probe-index（flag=1 行为不变）")
	}
}

// 手动账户真实流量达标恢复保留：SuccessCount 照常推进并清除降级，但不在
// 恢复观察期间写 nextProbeAt。
func TestW43AManualAccountRealTrafficSuccessRecoversWithoutProbeScheduling(t *testing.T) {
	clock := newFakeClock(1_000_000)
	store := NewMemoryRuntimeStateStore(clock.Now)
	account := latencyAccount("manual")
	service := w43aServiceWithOptions(clock, store, func(ctx context.Context, got SuppressibleGatewayAccount) (bool, error) {
		return true, nil
	})
	scope := latencyScope()
	config := speedFirstConfig()

	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	if state := wtdLoadState(t, store, scope, account); state.DegradedUntilMs == nil {
		t.Fatalf("前置失败：账户未降级")
	}
	// 真实流量达标样本推进恢复计数（RecoverySuccessCount=3 内不清除）。
	clock.ms += 1_000
	result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, scope, &config, ptrInt64W43a(1_000))
	if err != nil || result == nil {
		t.Fatalf("达标样本: result=%v err=%v", result, err)
	}
	if result.Cleared {
		t.Fatalf("首次达标不应清除降级（恢复阈值=3）: %+v", result)
	}
	state := wtdLoadState(t, store, scope, account)
	if state.SuccessCount != 1 {
		t.Fatalf("真实流量恢复计数必须照常推进: successCount=%d", state.SuccessCount)
	}
	if state.NextProbeAtMs != nil {
		t.Fatalf("手动账户恢复观察期不写 nextProbeAt: %+v", state.NextProbeAtMs)
	}
	// 达标样本累计到阈值：降级清除（真实流量恢复路径保留）。
	for i := 0; i < 2; i++ {
		clock.ms += 1_000
		result, err = service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, scope, &config, ptrInt64W43a(1_000))
		if err != nil || result == nil {
			t.Fatalf("达标样本 %d: result=%v err=%v", i, result, err)
		}
	}
	if !result.Cleared {
		t.Fatalf("真实流量达标达到阈值后应清除降级: %+v", result)
	}
}

func ptrInt64W43a(value int64) *int64 { return &value }
