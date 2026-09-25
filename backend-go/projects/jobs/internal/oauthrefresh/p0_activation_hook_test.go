package oauthrefresh

// P0 修复回归：账户可用性排期同步的 ActivationHook 契约——只对“启用”翻转
// 调用、禁用翻转不调用；hook 收到同步事务本身（归档语义：激活副作用与状态
// 翻转同一事务，hook 内可观察未提交的翻转结果）。hook 自身报错仍然中止同步
// 事务（TestSyncActivationHookErrorAborts 钉住的接口契约不变；生产 hook 的
// best-effort 吞错在组合根装配层，见 cmd 的 worker_oauth_activation.go）。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const p0ClosedWindowSchedule = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"10:00"}]}`

func TestP0ActivationHookObservesUncommittedFlipInSyncTx(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "acc-tx", availabilityScheduleJSON, "", "disabled", false)
	seen := ""
	hook := ActivationHookFunc(func(_ context.Context, tx *sql.Tx, accountID, _ string) error {
		return tx.QueryRow(`SELECT status FROM accounts WHERE id = ?`, accountID).Scan(&seen)
	})
	if _, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, hook); err != nil {
		t.Fatal(err)
	}
	if seen != "active" {
		t.Fatalf("hook must observe the uncommitted flip inside the sync tx, got %q", seen)
	}
}

func TestP0DisableFlipDoesNotCallActivationHook(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// 窗口边界事件只在当前分钟恰好落在边界时触发（09:00-10:00 → 10:00 这一
	// 分钟到期事件是禁用翻转）。
	afterClose := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "acc-off", p0ClosedWindowSchedule, "", "active", false)
	calls := 0
	hook := ActivationHookFunc(func(context.Context, *sql.Tx, string, string) error {
		calls++
		return nil
	})
	result, err := store.SyncAccountScheduleStatuses(context.Background(), afterClose, 0, hook)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 || result.Activated != 0 {
		t.Fatalf("result=%+v", result)
	}
	if calls != 0 {
		t.Fatalf("activation hook called %d times on a disable flip", calls)
	}
	status, _, _ := readAccountStatus(t, db, "acc-off")
	if status != "disabled" {
		t.Fatalf("status=%q", status)
	}
}
