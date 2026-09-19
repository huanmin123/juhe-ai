package gatewaycircuit

import (
	"testing"
)

// recordingLogger 捕获 Info/Warn 事件，用于断言抑制存储的审计输出。
type recordingLogger struct {
	infos []string
	warns []string
}

func (l *recordingLogger) Info(_ map[string]any, message string) { l.infos = append(l.infos, message) }
func (l *recordingLogger) Warn(_ map[string]any, message string) { l.warns = append(l.warns, message) }

func newSuppressionStore(t *testing.T, now func() int64) (*LocalSuppressionStore, *recordingLogger) {
	t.Helper()
	logger := &recordingLogger{}
	store := NewLocalSuppressionStore(LocalSuppressionStoreOptions{
		Now:                now,
		Logger:             logger,
		AccountConcurrency: func(string) int { return 0 },
	})
	return store, logger
}

func suppressible(key, id string) SuppressibleAccount {
	return SuppressibleAccount{SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: id}}
}

// 本地抑制阶梯契约：连续失败按 3s/5s/10s 阶梯避让；达到阶梯顶部后
// 观察窗口不足则继续短暂避让，观察窗口足够则要求事前确认。
func TestWBLocalSuppressionLadderAndPrecheck(t *testing.T) {
	now := int64(1_000_000)
	clock := &now
	store, logger := newSuppressionStore(t, func() int64 { return *clock })
	key := "acc1"
	first := store.SuppressForGatewayFailure(key, "acc1", "transport:connect failed", "")
	if first.Action != SuppressionActionSuppressed || first.LocalFailureCount != 1 || first.DelayMs != 3_000 || !first.HasDelayMs {
		t.Fatalf("first = %+v", first)
	}
	if len(logger.warns) == 0 {
		t.Fatal("屏蔽必须产生 Warn 审计")
	}
	// 屏蔽过期后的下一次失败推进到 5s。
	*clock += first.DelayMs + 1
	second := store.SuppressForGatewayFailure(key, "acc1", "transport:again", "")
	if second.LocalFailureCount != 2 || second.DelayMs != 5_000 {
		t.Fatalf("second = %+v", second)
	}
	// 再过期一次推进到 10s（阶梯顶部）。
	*clock += second.DelayMs + 1
	third := store.SuppressForGatewayFailure(key, "acc1", "transport:third", "")
	if third.LocalFailureCount != 3 || third.DelayMs != 10_000 {
		t.Fatalf("third = %+v", third)
	}
	// 阶梯顶部过期后再失败：观察窗口不足 60s 时继续短暂避让。
	*clock += third.DelayMs + 1
	fourth := store.SuppressForGatewayFailure(key, "acc1", "transport:fourth", "")
	if fourth.Action != SuppressionActionSuppressed || fourth.DelayMs != 10_000 || fourth.LocalFailureCount != 4 {
		t.Fatalf("fourth (insufficient observation) = %+v", fourth)
	}
	// 观察足够（>= 60s）后转为 precheck_required。
	store.AgeSuppressionSinceForTest(key, *clock-120_000)
	fifth := store.SuppressForGatewayFailure(key, "acc1", "transport:fifth", "")
	if fifth.Action != SuppressionActionPrecheckRequired || fifth.HasDelayMs {
		t.Fatalf("fifth = %+v", fifth)
	}
	// 事前确认流程自身会用 precheck_pending 状态写入屏蔽，
	// PrecheckRuntimeBlockingAt 只对该状态且未过期时阻断。
	store.Suppress(key, 60_000, "precheck in flight", AvailabilityStatusPrecheckPending, &suppressionMetadata{accountID: key})
	if !store.PrecheckRuntimeBlockingAt(key) {
		t.Fatal("precheck_pending 未过期必须阻断")
	}
	*clock += 60_001
	if store.PrecheckRuntimeBlockingAt(key) {
		t.Fatal("过期的 precheck 状态不得阻断")
	}
	if store.PrecheckRuntimeBlockingAt("acc-other") {
		t.Fatal("未知运行键不得阻断")
	}
}

// 可用性快照契约：可见屏蔽进入快照。降级可见性分支已随
// DegradeForGatewayFailure 写面退场删除（生产降级恒空）。
func TestWBSnapshotAvailabilityVisibility(t *testing.T) {
	now := int64(2_000_000)
	clock := &now
	store, _ := newSuppressionStore(t, func() int64 { return *clock })
	store.SuppressForGatewayFailure("acc-snap", "acc-snap", "transport:x", "")
	// SnapshotAvailability 持锁调用谓词，因此谓词必须无锁
	// （生产接线传入的是组合层的无锁闭包）。
	snapshot := store.SnapshotAvailability(func(string) bool { return false })
	availability, ok := snapshot["acc-snap"]
	if !ok || availability.Status != AvailabilityStatusLocalSuppressed || availability.Until == "" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if store.CountVisibleSuppressions(func(string) bool { return false }) < 1 {
		t.Fatal("可见屏蔽数量不足")
	}
}

// 清理契约：过期屏蔽可清理；ClearForTest 清空全部状态。
// （ClearSuppression/ClearDegradation 已随写面退场删除。）
func TestWBSuppressionCleanup(t *testing.T) {
	now := int64(3_000_000)
	clock := &now
	store, _ := newSuppressionStore(t, func() int64 { return *clock })
	store.Suppress("acc-expire", 1_000, "transport:y", AvailabilityStatusLocalSuppressed, &suppressionMetadata{accountID: "acc-expire"})
	*clock += 1_000 + localSuppressionIdleRetentionMs + 1
	store.CleanupExpiredSuppressions(nil)
	if store.CountVisibleSuppressions(func(string) bool { return false }) != 0 {
		t.Fatal("过期屏蔽必须被清理")
	}
	store.ClearForTest()
	if store.CountVisibleSuppressions(store.PrecheckRuntimeBlockingAt) != 0 || store.CountDegradations() != 0 {
		t.Fatal("ClearForTest 之后必须全空")
	}
	// AgeDegradationForTest 对未知键必须是安全 no-op。
	store.AgeDegradationForTest("ghost", 1_000)
}

// Redis 托管模式契约：canUseProcessLocal=false 时所有本地状态操作退化为
// 清空本地缓存并返回 Redis 托管语义。
func TestWBSuppressionRedisManagedMode(t *testing.T) {
	calls := 0
	store := NewLocalSuppressionStore(LocalSuppressionStoreOptions{
		Now:                func() int64 { return 0 },
		CanUseProcessLocal: func() bool { calls++; return false },
	})
	result := store.SuppressForGatewayFailure("acc-r", "acc-r", "transport", "")
	if result.Action != SuppressionActionRedisManaged || result.LocalFailureCount != 0 {
		t.Fatalf("redis-managed result = %+v", result)
	}
	if got := store.SnapshotAvailability(nil); len(got) != 0 {
		t.Fatalf("redis-managed snapshot = %#v", got)
	}
	if lease := store.ReleaseHalfOpenLease("acc-r", "acc-r", "lease"); lease {
		t.Fatal("redis-managed 模式不得释放本地半开租约")
	}
	ordered := store.OrderDegradations([]SuppressibleAccount{suppressible("acc-r", "acc-r")}, nil)
	if len(ordered.Accounts) != 1 || ordered.Applied {
		t.Fatalf("redis-managed order = %+v", ordered)
	}
	filtered := store.FilterSuppressions([]SuppressibleAccount{suppressible("acc-r", "acc-r")}, nil, SuppressionFilterOptions{})
	if len(filtered.Accounts) != 1 {
		t.Fatalf("redis-managed filter = %+v", filtered)
	}
	store.AgeDegradationForTest("acc-r", 1)
	store.CleanupExpiredSuppressions(nil)
	store.Suppress("acc-r", 1_000, "r", AvailabilityStatusLocalSuppressed, nil)
	if store.CountVisibleSuppressions(nil) != 0 || store.CountDegradations() != 0 {
		t.Fatal("redis-managed 计数必须为 0")
	}
	if calls == 0 {
		t.Fatal("canUseProcessLocal 必须被调用")
	}
}

// 半开租约契约：屏蔽到期后放行一个探测请求；释放后回到本地屏蔽。
func TestWBHalfOpenLeaseAcquireAndRelease(t *testing.T) {
	now := int64(5_000_000)
	clock := &now
	store, _ := newSuppressionStore(t, func() int64 { return *clock })
	store.Suppress("acc-half", 5_000, "transport", AvailabilityStatusLocalSuppressed, &suppressionMetadata{accountID: "acc-half"})
	*clock += 6_000
	result := store.FilterSuppressions([]SuppressibleAccount{suppressible("acc-half", "acc-half")}, nil, SuppressionFilterOptions{AcquireHalfOpenLease: true})
	if len(result.Accounts) != 1 || len(result.AcquiredHalfOpenLeases) != 1 {
		t.Fatalf("filter = %+v", result)
	}
	lease := result.AcquiredHalfOpenLeases[0]
	if lease.LeaseID == "" || lease.Release == nil {
		t.Fatalf("lease = %+v", lease)
	}
	// 持租期间半开条目仍然屏蔽其它请求。
	blocked := store.FilterSuppressions([]SuppressibleAccount{suppressible("acc-half", "acc-half")}, nil, SuppressionFilterOptions{})
	if len(blocked.Accounts) != 0 || !blocked.AllSuppressed || blocked.SuppressedCount != 1 {
		t.Fatalf("持租期间必须继续屏蔽: %+v", blocked)
	}
	if !lease.Release() {
		t.Fatal("租约释放必须成功")
	}
	if lease.Release() {
		t.Fatal("重复释放必须返回 false")
	}
}

// TestWBOrderDegradationsReordersBehindHealthy 已随
// DegradeForGatewayFailure 写面退场删除（生产降级恒空，OrderDegradations
// 恒 passthrough；其 passthrough 分支仍由 redis-managed 用例覆盖）。

// 调度优先层契约：重排不得跨层提升账号；未知模型层级排最后。
func TestWBPreserveDispatchPriorityTiersKeepsLayerOrder(t *testing.T) {
	base := []SuppressibleAccount{
		{SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: "super"}, SuperPriorityEnabled: true},
		{SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: "normal"}, FallbackEnabled: false},
		{SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: "fallback"}, FallbackEnabled: true},
	}
	reordered := []SuppressibleAccount{base[2], base[0], base[1]}
	got := PreserveDispatchPriorityTiers(base, reordered, nil)
	if got[0].ID != "super" || got[len(got)-1].ID != "fallback" {
		t.Fatalf("tiers = %s,%s,%s", got[0].ID, got[1].ID, got[2].ID)
	}
	single := PreserveDispatchPriorityTiers(base[:1], base[:1], nil)
	if len(single) != 1 {
		t.Fatalf("single = %+v", single)
	}
	unknown := []SuppressibleAccount{base[0], {SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: "ghost"}}}
	withUnknown := PreserveDispatchPriorityTiers(base, unknown, nil)
	if withUnknown[len(withUnknown)-1].ID != "ghost" {
		t.Fatalf("unknown tier must sort last: %+v", withUnknown)
	}
	if tier := DispatchPriorityTier(base[0], map[string]int64{"super": 2}); tier == "" {
		t.Fatal("层级字符串不得为空")
	}
	if tier := DispatchPriorityTier(base[1], map[string]int64{"missing": 1}); tier == "" {
		t.Fatal("未知 rank 层级不得为空")
	}
}

// 抑制元数据契约：metadata 为 nil 或字段缺失时必须保留既有值。
func TestWBApplySuppressionMetadataPreservesExisting(t *testing.T) {
	target := LocalAccountSuppression{FailureCount: int64Ptr(1)}
	applySuppressionMetadata(&target, nil)
	if target.FailureCount == nil || *target.FailureCount != 1 {
		t.Fatalf("nil metadata must keep values: %+v", target)
	}
	count := int64(4)
	ipCount := int64(2)
	apiKeyCount := int64(3)
	precheckCount := int64(5)
	localCount := int64(6)
	applySuppressionMetadata(&target, &suppressionMetadata{
		failureCount: &count, distinctClientIPCount: &ipCount,
		distinctAPIKeyCount: &apiKeyCount, precheckAttemptCount: &precheckCount,
		localFailureCount: &localCount,
	})
	if *target.FailureCount != 4 || *target.DistinctClientIPCount != 2 || *target.DistinctAPIKeyCount != 3 || *target.PrecheckAttemptCount != 5 || *target.LocalFailureCount != 6 {
		t.Fatalf("metadata not applied: %+v", target)
	}
	if metadataLocalFailureCount(nil) != nil {
		t.Fatal("nil metadata 的本地失败计数必须为 nil")
	}
	if got := suppressionUntilMs(nil); got != nil {
		t.Fatalf("nil suppression until = %v", got)
	}
	if got := suppressionUntilMs(&LocalAccountSuppression{UntilMs: 42}); got == nil || *got != 42 {
		t.Fatalf("suppression until = %v", got)
	}
	if minRetryAtMs(nil, 10) == nil {
		t.Fatal("首个候选必须生成 retry 指针")
	}
	first := minRetryAtMs(nil, 20)
	if got := minRetryAtMs(first, 10); *got != 10 {
		t.Fatalf("min retry = %d", *got)
	}
	if isNaNInt64(1) {
		t.Fatal("int64 永远不是 NaN")
	}
}
