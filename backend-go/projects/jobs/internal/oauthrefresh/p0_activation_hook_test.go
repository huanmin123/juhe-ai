package oauthrefresh

// P0 修复回归：账户可用性排期同步的 ActivationHook 契约——只对「闸门由关到开」
// （gate 0/NULL → 1）调用、关闸不调用；hook 收到同步事务本身（激活副作用与
// 闸门写入同一事务，hook 内可观察未提交的闸门值）。hook 自身报错仍然中止同步
// 事务（TestSyncActivationHookErrorAborts 钉住的接口契约不变；生产 hook 的
// best-effort 吞错在组合根装配层，见 cmd 的 worker_oauth_activation.go）。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const p0ClosedWindowSchedule = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"10:00"}]}`

func TestP0ActivationHookObservesUncommittedGateInSyncTx(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "acc-tx", availabilityScheduleJSON, "", "disabled", false)
	var seenGate sql.NullInt64
	hook := ActivationHookFunc(func(_ context.Context, tx *sql.Tx, accountID, _ string) error {
		return tx.QueryRow(`SELECT availability_schedule_gate FROM juhe_business.accounts WHERE id = $1`, accountID).Scan(&seenGate)
	})
	if _, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, hook); err != nil {
		t.Fatal(err)
	}
	if !seenGate.Valid || seenGate.Int64 != 1 {
		t.Fatalf("hook must observe the uncommitted gate=1 inside the sync tx, got %+v", seenGate)
	}
	// status 不参与激活：同步后仍是人工停用的 disabled。
	if status, _, _ := readAccountStatus(t, db, "acc-tx"); status != "disabled" {
		t.Fatalf("status=%q want disabled（同步不得改写 status）", status)
	}
	requireGate(t, "acc-tx", readAccountGate(t, db, "acc-tx"), "1")
}

func TestP0DisableFlipDoesNotCallActivationHook(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// 10:00 是 09:00-10:00 窗口的结束分钟：闸门由 1 变 0。
	afterClose := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "acc-off", p0ClosedWindowSchedule, "", "active", false)
	setScheduleGate(t, db, "accounts", "acc-off", 1)
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
		t.Fatalf("activation hook called %d times on a gate close", calls)
	}
	status, _, _ := readAccountStatus(t, db, "acc-off")
	if status != "active" {
		t.Fatalf("status=%q want active（同步不得改写 status）", status)
	}
	requireGate(t, "acc-off", readAccountGate(t, db, "acc-off"), "0")
}
