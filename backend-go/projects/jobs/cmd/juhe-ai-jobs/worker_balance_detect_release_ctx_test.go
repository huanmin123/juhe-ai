package main

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// TestBalanceRunWithLeaseReleasesAfterOuterCtxCancel（BUG-0223 防御收口）：
// run 取消外层 ctx（任务超时/停机传播的等价模拟）后，RunWithLease 的租约
// 释放不得因 ctx 已取消而失败——否则租约滞留至 30s TTL，同候选的下一轮
// 探测在 TTL 内全部 acquired=false。回归方式：第一次 RunWithLease 在 run
// 内 cancel 外层 ctx；若释放仍用已取消 ctx，第二次立即获取必然失败
// （LeaseBusy 直到 TTL 过期）。
func TestBalanceRunWithLeaseReleasesAfterOuterCtxCancel(t *testing.T) {
	runtime, _, _ := wgNewBalanceRuntime(t, "", true)
	candidate := opsjobs.BalanceDetectionCandidate{ID: "acc-release-ctx", SystemAccountID: "sys-1"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acquired, err := runtime.RunWithLease(ctx, candidate, func(context.Context) error {
		cancel()
		return nil
	})
	if err != nil || !acquired {
		t.Fatalf("首次租约必须获取并执行: acquired=%v err=%v", acquired, err)
	}
	reacquired, err := runtime.RunWithLease(context.Background(), candidate, func(context.Context) error { return nil })
	if err != nil || !reacquired {
		t.Fatalf("run 取消外层 ctx 后租约必须已释放、立即重取成功: acquired=%v err=%v", reacquired, err)
	}
}
