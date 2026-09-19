package gatewaycircuit

// w14m 覆盖率补强：本地抑制 store 的降级计数保持臂、快照可见性过滤、
// precheck 阻断过滤、元数据 sinceMs 注入、mustRuntimeKey/MustScopeKey 防御
// panic、被动调度抖动的大代次偏移臂与十六进制解析。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

func TestW14MNopLoggerAndMustHelpers(t *testing.T) {
	// nopLogger 的空实现臂。
	NopLogger.Info(map[string]any{"event": "w14m"}, "w14m-info")
	NopLogger.Warn(nil, "w14m-warn")

	// MustScopeKey 对未验证 scope 直接 panic。
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("MustScopeKey must panic on invalid scope")
			}
		}()
		MustScopeKey(Scope{})
	}()

	// mustRuntimeKey 对缺少绑定上下文的授权账户直接 panic。
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("mustRuntimeKey must panic on invalid account")
			}
		}()
		_ = mustRuntimeKey(SuppressibleGatewayAccount{ID: "w14m-auth", AccountAccessType: "account_authorized"})
	}()
}

// TestW14MDegradeKeepsCountAndStaysActive 已随 DegradeForGatewayFailure
// 写面退场删除（生产写面退场，见 suppression.go 顶部注记）。

func TestW14MSuppressMetadataSinceMsOnFreshKey(t *testing.T) {
	now := int64(5_000)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	since := int64(1_234)
	store.Suppress("w14m-since", now+10_000, "transport:boom", AvailabilityStatusLocalSuppressed,
		&suppressionMetadata{sinceMs: &since})
	suppression := store.suppressions["w14m-since"]
	if suppression == nil || suppression.SinceMs != 1_234 {
		t.Fatalf("suppression sinceMs = %+v", suppression)
	}
}

func TestW14MSnapshotAvailabilityVisibilityArms(t *testing.T) {
	now := int64(10_000)
	clock := &now
	store := newTestSuppressionStore(func() int64 { return *clock }, nil, false)

	// 键 A：已过期但未超闲置保留期的 local_suppressed，谓词 false → 不可见被 continue。
	store.Suppress("w14m-invisible", 9_000, "transport:boom", AvailabilityStatusLocalSuppressed, nil)

	// 键 B：谓词 true → cleanup 跳过且快照可见。
	store.Suppress("w14m-blocked", 9_000, "precheck", AvailabilityStatusPrecheckPending, nil)

	// （原键 B 的 active 降级 continue 分支已随 DegradeForGatewayFailure
	// 写面退场删除；生产降级恒空。原 setup 中的时钟推进保留在此处，
	// 使键 A 处于“已过期但未超闲置保留期”的可见性臂。）
	*clock = 71_000

	snapshot := store.SnapshotAvailability(func(runtimeKey string) bool {
		return runtimeKey == "w14m-blocked"
	})
	if _, ok := snapshot["w14m-invisible"]; ok {
		t.Fatal("invisible suppression must be skipped")
	}
	if _, ok := snapshot["w14m-blocked"]; !ok {
		t.Fatal("predicate-blocked suppression must stay visible")
	}
}

func TestW14MFilterSuppressionsPrecheckBlockingArm(t *testing.T) {
	now := int64(1_000)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	store.Suppress("w14m-pre", now+10_000, "precheck", AvailabilityStatusPrecheckPending, nil)
	unknown := "w14m-pre-unknown"
	store.Suppress(unknown, now+10_000, "precheck", AvailabilityStatusPrecheckFailed, nil)

	result := store.FilterSuppressions(
		[]SuppressibleAccount{suppressibleAccount("w14m-pre"), suppressibleAccount(unknown)},
		func(string) bool { return true },
		SuppressionFilterOptions{},
	)
	if len(result.Accounts) != 0 || !result.AllSuppressed {
		t.Fatalf("filter result = %+v", result)
	}
	found := map[string]PrecheckSuppressedRuntimeScope{}
	for _, scope := range result.PrecheckSuppressedRuntimeScopes {
		found[scope.RuntimeKey] = scope
	}
	if scope, ok := found["w14m-pre"]; !ok || scope.Generation != now {
		t.Fatalf("precheck runtime scopes = %+v", result.PrecheckSuppressedRuntimeScopes)
	}
	if _, ok := found[unknown]; !ok {
		t.Fatalf("precheck runtime scopes = %+v", result.PrecheckSuppressedRuntimeScopes)
	}
	if result.NextRetryAtMs == nil {
		t.Fatalf("nextRetryAtMs must be set: %+v", result)
	}
	t.Logf("nextRetryAtMs = %d", *result.NextRetryAtMs)
}

func TestW14MPreserveDispatchPriorityTiersUnknownTier(t *testing.T) {
	base := []SuppressibleAccount{
		suppressibleAccount("w14m-a1"),
		suppressibleAccount("w14m-a2"),
	}
	odd := suppressibleAccount("w14m-a3")
	odd.Priority = 9
	reordered := []SuppressibleAccount{odd, base[1], base[0]}
	output := PreserveDispatchPriorityTiers(base, reordered, nil)
	if len(output) != 3 || output[2].ID != "w14m-a3" {
		t.Fatalf("unknown-tier account must be appended last: %+v", output)
	}
}

func TestW14MJitterDeterministicArms(t *testing.T) {
	// 负间隔与窗口大于半间隔的收敛臂。
	if got := passiveScheduleJitterWindowMs(-1_000); got != 0 {
		t.Fatalf("negative interval window = %d", got)
	}
	// 2 分钟间隔 → 分钟窗口 30s < 半间隔 60s → 返回窗口本身。
	if got := passiveScheduleJitterWindowMs(120_000); got != int64(schedulejitter.MinuteWindow/time.Millisecond) {
		t.Fatalf("minute window = %d", got)
	}
	// 大写十六进制前缀解析。
	if got := hexPrefixSample("ABCDEF01"); got != 0xABCDEF01 {
		t.Fatalf("uppercase sample = %d", got)
	}

	// backoff 第 5 级（index>=4）带 seed：枚举 seed 命中 offset==0 的臂。
	settings := DefaultSettings()
	base := settings.AccountCircuitBackoffMs[4]
	windowMs := passiveScheduleJitterWindowMs(base)
	var zeroSeed string
	for i := 0; i < 1_000_000; i++ {
		seed := fmt.Sprintf("w14m-jitter-%d", i)
		sample := hexPrefixSample(sha1Hex(seed))
		if int64(sample%uint64(windowMs*2+1)) == windowMs {
			zeroSeed = seed
			break
		}
	}
	if zeroSeed == "" {
		t.Fatal("failed to locate a zero-offset jitter seed")
	}
	delay := settings.accountCircuitBackoffDelayMs(5, zeroSeed, nil)
	if delay != base+1 {
		t.Fatalf("zero-offset delay = %d, want %d", delay, base+1)
	}
}

func TestW14MMemoryStoreMutationNotFoundArms(t *testing.T) {
	now := int64(1_000)
	store := w14eMemoryStore(t, func(options *MemoryStoreOptions) {
		options.Now = func() int64 { return now }
	})
	ctx := context.Background()
	scope := w14mAccountScope("missing")

	// AcquireConfirmationLease：作用域不存在 → NotFound。
	result, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "rev-1", TransitionID: "w14m-t1",
		LeaseID: "w14m-lease", LeaseUntilMs: now + 5_000,
	})
	if err != nil || result.Status != MutationNotFound {
		t.Fatalf("acquire on missing scope = (%s, %v)", result.Status, err)
	}

	// AcquireCanaryLease：作用域不存在 → NotFound。
	canary, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "rev-1", TransitionID: "w14m-t2",
		LeaseID: "w14m-canary", LeaseUntilMs: now + 5_000,
	})
	if err != nil || canary.Status != MutationNotFound {
		t.Fatalf("canary on missing scope = (%s, %v)", canary.Status, err)
	}

	// CompleteCanary：作用域不存在 → NotFound。
	completed, err := store.CompleteCanary(ctx, CompleteCanaryInput{
		Scope: scope, Generation: 1, DispatchRevision: "rev-1", TransitionID: "w14m-t3",
		LeaseID: "w14m-canary", Outcome: OutcomeUnknown,
	})
	if err != nil || completed.Status != MutationNotFound {
		t.Fatalf("complete canary on missing scope = (%s, %v)", completed.Status, err)
	}
}
