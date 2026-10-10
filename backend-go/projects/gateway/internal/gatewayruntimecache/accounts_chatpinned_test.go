package gatewayruntimecache

// chat-pinned 直取读（AI 问答设计 §5.2 2026-10-10 修订）的 Service 层覆盖：
// fresh 直读 loader、按显式 ChatPinnedAccountID 选项分流、非 active 账户不被
// 可用性剔除但仍注入实时并发；对照常规缓存读同账户被
// isOpenAIAccountRuntimeUsableAt 剔除——/v1 常规窗口语义不变的回归锚点。

import (
	"context"
	"sync"
	"testing"
)

// pinnedCaptureModels 包装 fakeModels，记录最近一次 loader 选项（断言
// ChatPinnedAccountID 显式透传；其余方法经嵌入提升委托）。
type pinnedCaptureModels struct {
	*fakeModels

	mu       sync.Mutex
	lastOpts OpenAIAccountsForGroupOptions
}

func (m *pinnedCaptureModels) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	m.mu.Lock()
	m.lastOpts = opts
	m.mu.Unlock()
	return m.fakeModels.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, opts)
}

func (m *pinnedCaptureModels) snapshotLastOpts() OpenAIAccountsForGroupOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastOpts
}

func TestListFreshOpenAIAccountsForChatPinnedAsyncKeepsUnavailable(t *testing.T) {
	models := newFakeModels()
	disabled := testAccount("acc_pin", "sys")
	disabled.Status = "disabled"
	active := testAccount("acc_active", "sys")
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{disabled, active}}
	models.concurrency["acc_pin"] = 3
	capture := &pinnedCaptureModels{fakeModels: models}
	svc := newTestService(t, capture, newManualClock(), nil)
	ctx := context.Background()

	pinned, err := svc.ListFreshOpenAIAccountsForChatPinnedAsync(ctx, "g1", "sys", "acc_pin")
	if err != nil {
		t.Fatal(err)
	}
	if opts := capture.snapshotLastOpts(); opts.ChatPinnedAccountID != "acc_pin" {
		t.Fatalf("loader 选项未携带 ChatPinnedAccountID: %+v", opts)
	}
	if len(pinned) != 2 {
		t.Fatalf("pinned 直取 = %d 账户, want 2（含禁用账户）", len(pinned))
	}
	var pinnedDisabled *OpenAIAccountSecret
	for i := range pinned {
		if pinned[i].ID == "acc_pin" {
			pinnedDisabled = &pinned[i]
		}
	}
	if pinnedDisabled == nil {
		t.Fatalf("pinned 直取缺少禁用账户: %v", pinned)
	}
	if pinnedDisabled.CurrentConcurrency == nil || *pinnedDisabled.CurrentConcurrency != 3 {
		t.Fatalf("pinned 禁用账户并发注入 = %+v, want 3", pinnedDisabled.CurrentConcurrency)
	}

	// 对照：常规缓存读同数据仍按可用性窗口剔除禁用账户（/v1 语义不变的回归
	// 锚点），active 账户照常返回。
	cached, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 1 || cached[0].ID != "acc_active" {
		t.Fatalf("常规缓存读 = %v, want 仅 active 账户（禁用账户被可用性窗口剔除）", cached)
	}
}

func TestListFreshOpenAIAccountsForChatPinnedAsyncEmptyGroup(t *testing.T) {
	models := newFakeModels()
	svc := newTestService(t, models, newManualClock(), nil)
	pinned, err := svc.ListFreshOpenAIAccountsForChatPinnedAsync(context.Background(), "g_missing", "sys", "acc_pin")
	if err != nil || len(pinned) != 0 {
		t.Fatalf("缺失分组直取 = %v err=%v, want 空切片 nil 错误", pinned, err)
	}
}
