package gatewayruntimecache

// T91-4 / B2-H3 钉住测试：运行缓存 last-good retain 窗口从 Node 的 10 分钟
// 收紧到 2 分钟。60 秒 revalidate 窗口不变——revalidate 过期后仍以 last-good
// 立即返回并后台刷新；但条目超过 2 分钟后整体丢弃，读路径必须同步回源，不能
// 继续用陈旧快照调度已翻转状态的账户。accounts / group-access / runtime /
// response-inspection 四族 retain 常量同源一致。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestT91RetainTTLConstantsAligned 钉住四族 retain 常量 = 2 分钟，且 revalidate
// 窗口仍为 60 秒（未随 retain 缩短）。
func TestT91RetainTTLConstantsAligned(t *testing.T) {
	retain := map[string]time.Duration{
		"gatewayRuntimeRetainTTL":           gatewayRuntimeRetainTTL,
		"groupUsageAccessRetainTTL":         groupUsageAccessRetainTTL,
		"openAIAccountsRetainTTL":           openAIAccountsRetainTTL,
		"responseInspectionPolicyRetainTTL": responseInspectionPolicyRetainTTL,
	}
	for name, ttl := range retain {
		if ttl != 2*time.Minute {
			t.Fatalf("%s = %v, want 2m", name, ttl)
		}
	}
	revalidate := map[string]time.Duration{
		"gatewayRuntimeTTL":   gatewayRuntimeTTL,
		"groupUsageAccessTTL": groupUsageAccessTTL,
		"openAIAccountsTTL":   openAIAccountsTTL,
	}
	for name, ttl := range revalidate {
		if ttl != 60*time.Second {
			t.Fatalf("%s = %v, want 60s", name, ttl)
		}
	}
}

// TestT91AccountsStaleRetainedThenDropped revalidate 过期后（61s）仍命中
// last-good；越过 2 分钟 retain（121s）后条目被丢弃，读路径同步回源，loader
// 失败直接上抛——不再有 last-good 兜底。
func TestT91AccountsStaleRetainedThenDropped(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	failing := &w11dFailingAccounts{fakeModels: models, failAfter: 1}
	clock := newManualClock()
	svc := newTestService(t, failing, clock, nil)

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatalf("首载: %v", err)
	}
	if failing.calls != 1 {
		t.Fatalf("首载 loader = %d", failing.calls)
	}

	// 61s：revalidate 已过期但仍在 2min retain 窗口内 → last-good 立即返回。
	clock.Advance(61 * time.Second)
	stale, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(stale) != 1 || stale[0].ID != "a1" {
		t.Fatalf("2min 内陈旧条目必须命中 last-good = %v err=%v", stale, err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatalf("await: %v", err)
	}
	if failing.calls != 2 {
		t.Fatalf("revalidate 过期必须后台回源（失败不回填），loader = %d", failing.calls)
	}

	// 121s：越过 2min retain → 条目丢弃，同步回源；loader 失败必须上抛，
	// 不得再返回 last-good。
	clock.Advance(60 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("越过 retain 窗口后不得命中 last-good（同步回源失败必须上抛）")
	}
	if failing.calls != 3 {
		t.Fatalf("越过 retain 必须同步回源一次，loader = %d", failing.calls)
	}
}

// TestT91RuntimeStaleRetainedThenDropped 与账户缓存同口径：runtime last-good
// 只在 revalidate 过期后、2min retain 内可用；越过 retain 后同步回源。
func TestT91RuntimeStaleRetainedThenDropped(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.runtimes["sk-t91"] = staticRuntime(t, models, testAPIKeyRow("key_1", "normal", "g1"),
		&GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", GroupAccessType: GroupAccessTypeOwner},
		[]OpenAIAccountSecret{testAccount("a1", "sys_owner")})
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)

	runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-t91")
	if err != nil {
		t.Fatalf("首读: %v", err)
	}
	if runtime.APIKey == nil || runtime.APIKey.ID != "key_1" {
		t.Fatalf("首读 key = %+v", runtime.APIKey)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("首读 loader = %d", models.runtimeCallCount())
	}

	// 61s：revalidate 过期、retain 未到期 → last-good + 后台刷新。
	clock.Advance(61 * time.Second)
	stale, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-t91")
	if err != nil || stale.APIKey == nil || stale.APIKey.ID != "key_1" {
		t.Fatalf("2min 内陈旧 runtime 必须命中 last-good = %+v err=%v", stale.APIKey, err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatalf("await: %v", err)
	}
	// 后台刷新成功会重写条目（新的 2min 窗口），因此这里重新构造一个只读首载
	// 的条目来验证「超过 retain」的丢弃语义。
	models.mu.Lock()
	models.runtimeErr = nil
	models.mu.Unlock()
	svc2 := newTestService(t, models, clock, nil)
	before := models.runtimeCallCount()
	if _, err := svc2.ReadCachedGatewayRuntimeAsync(ctx, "sk-t91"); err != nil {
		t.Fatalf("svc2 首读: %v", err)
	}
	// 越过 retain 窗口（>2min）：同步回源，loader 失败直接上抛。
	models.mu.Lock()
	models.runtimeErr = errors.New("t91 runtime loader down")
	models.mu.Unlock()
	clock.Advance(121 * time.Second)
	if _, err := svc2.ReadCachedGatewayRuntimeAsync(ctx, "sk-t91"); err == nil {
		t.Fatal("越过 retain 窗口后不得命中 last-good（同步回源失败必须上抛）")
	}
	if got := models.runtimeCallCount() - before; got != 2 {
		t.Fatalf("svc2 首读 + 越窗回源 loader = %d, want 2", got)
	}
}
