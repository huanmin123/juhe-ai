package gatewayruntimecache

// accounts 读取路径消费 revalidateAtMs（stale-while-revalidate）的回归测试：
// 本地缓存模式下 revalidate 窗口（≤60s）过期触发后台回源，last-good 立即返回；
// 账号到期收紧与共享（redis）模式行为保持不变。

import (
	"context"
	"testing"
	"time"
)

// TestAccountsStaleWhileRevalidate revalidate 过期触发后台回源、未过期直用缓存、
// 刷新完成后新凭据（jobs 侧轮换）被下一个 revalidate 窗口消费。
func TestAccountsStaleWhileRevalidate(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	svc := newTestService(t, counter, clock, nil)

	// 未命中 → loader 装载。
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("首载 loader = %d", counter.accountCalls)
	}
	// revalidate 窗口内（60s）命中直接用缓存，不触发回源。
	clock.Advance(30 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("fresh 命中不应回源，loader = %d", counter.accountCalls)
	}
	// 越过 revalidate 窗口：陈旧快照立即返回（last-good），后台刷新被触发。
	clock.Advance(31 * time.Second)
	stale, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(stale) != 1 || stale[0].ID != "a1" {
		t.Fatalf("stale last-good = %v err=%v", stale, err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("revalidate 过期必须回源，loader = %d", counter.accountCalls)
	}

	// 模拟 jobs 后台轮换 access_token（只写 DB/loader 侧，无跨进程失效通知）：
	// 等待在途刷新结束后更新 loader 数据，下一个窗口的回源必须拿到新凭据。
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys"), testAccount("a2", "sys")}}
	clock.Advance(61 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 3 {
		t.Fatalf("二次回源 loader = %d", counter.accountCalls)
	}
	refreshed, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(refreshed) != 2 {
		t.Fatalf("轮换后快照 = %v err=%v", refreshed, err)
	}
}

// w11dFailingAccounts 复用：failAfter 之后的 loader 调用失败。
func TestAccountsStaleRefreshFailureKeepsSnapshot(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	failing := &w11dFailingAccounts{fakeModels: models, failAfter: 1}
	clock := newManualClock()
	svc := newTestService(t, failing, clock, nil)

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	// revalidate 过期 + 回源失败：last-good 快照保留，读不失败。
	clock.Advance(61 * time.Second)
	stale, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(stale) != 1 || stale[0].ID != "a1" {
		t.Fatalf("回源失败必须保留 last-good = %v err=%v", stale, err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	if failing.calls < 2 {
		t.Fatalf("后台刷新必须已尝试，loader = %d", failing.calls)
	}
	// 缓存仍是旧快照（再次读取仍命中旧值）。
	again, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(again) != 1 || again[0].ID != "a1" {
		t.Fatalf("失败后旧快照 = %v err=%v", again, err)
	}
}

// TestAccountsExpiryTightenedRevalidate 账号到期收紧 revalidateAtMs 的逻辑不变：
// 到期前 30s 条目即视为陈旧并回源，同时运行时不可用账号仍被并发覆盖层剔除。
func TestAccountsExpiryTightenedRevalidate(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	expiresAt := newManualClock().Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	account := testAccount("a_expiring", "sys")
	account.ExpiresAt = &expiresAt
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{account}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	svc := newTestService(t, counter, clock, nil)

	// 收紧后的条目 revalidateAtMs = 到期时刻，而非 now+60s。
	entry, entryErr := newOpenAIAccountsCacheEntry(cloneStaticOpenAIAccounts(models.accounts["g1"].Accounts), clock.Now().UnixMilli())
	if entryErr != nil {
		t.Fatal(entryErr)
	}
	if want := clock.Now().Add(30 * time.Second).UnixMilli(); entry.revalidateAtMs != want {
		t.Fatalf("收紧 revalidateAtMs = %d, want %d", entry.revalidateAtMs, want)
	}
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	// 窗口内（<30s）fresh 直用。
	clock.Advance(10 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("收紧窗口内 fresh 命中不应回源，loader = %d", counter.accountCalls)
	}
	// 越过收紧窗口（>30s）：陈旧回源被触发。
	clock.Advance(21 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("收紧窗口过期必须回源，loader = %d", counter.accountCalls)
	}
}

// TestAccountsSharedModeUnaffectedByRevalidate 共享（redis）模式下进程缓存禁用：
// revalidate 分支不参与，每次读取都落 loader（快照不进共享层的既有契约不变）。
func TestAccountsSharedModeUnaffectedByRevalidate(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	svc := newTestService(t, counter, clock, func(o *Options) { o.Shared = shared })

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(31 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 3 {
		t.Fatalf("共享模式每次读取都必须落 loader，loader = %d", counter.accountCalls)
	}
	if n := svc.pendingRuntimeLoadCount(); n != 0 {
		t.Fatalf("pending = %d", n)
	}
}
