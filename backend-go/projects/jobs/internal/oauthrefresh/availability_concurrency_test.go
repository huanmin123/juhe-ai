package oauthrefresh

// 时间计划闸门并发栅栏测试（《时间计划闸门与账户状态解耦设计》§4.1）：
// 求值在事务外读行、写回在事务内，期间计划可能被 PATCH/批量改写。写回必须带
// 计划原文 CAS，否则用旧计划算出的 gate / next_check 会覆盖新计划的结果，把
// 下次扫描推到旧计划的远端边界，新窗口整个不会被扫描到。

import (
	"context"
	"testing"
	"time"
)

// TestApplyScheduleGateSkipsWhenScheduleChangedAfterRead 模拟「求值后、写回前」
// 计划被改写：申请写回的 update 携带旧计划原文，落库时 CAS 失配 → 不写 gate，
// 也不推进 next_check，闸门与边界保持 PATCH 写入的新值。
func TestApplyScheduleGateSkipsWhenScheduleChangedAfterRead(t *testing.T) {
	store, db, _ := newTestStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-race", availabilityScheduleJSON, "", "active")
	setScheduleGate(t, db, "api_keys", "key-race", 0)

	stale := availabilityScheduleJSON
	fresh := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"12:00","end":"13:00"}]}`
	// 模拟 PATCH 在求值之后改写计划并把闸门关回 0、边界改成新窗口起点。
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_json = $1, availability_schedule_gate = 0, availability_schedule_next_check_at = '2026-09-07T12:00:00.000Z' WHERE id = 'key-race'`, fresh); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	gateOpen := 1
	update := scheduleSyncUpdate{
		id:           "key-race",
		gate:         &gateOpen,
		nextCheckAt:  "2026-09-07T18:00:00.000Z", // 旧计划算出的边界
		scheduleJSON: stale,                       // 旧计划原文 → CAS 失配
	}
	changed, err := store.applyScheduleGate(context.Background(), tx, store.table("api_keys"), false, update, isoMillis(atStart))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("计划已被改写时必须放弃写回（CAS 失配）")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	gate := readApiKeyGate(t, db, "key-race")
	requireGate(t, "并发改写后", gate, "0")
	_, nextCheck, _ := readApiKey(t, db, "key-race")
	if nextCheck != "2026-09-07T12:00:00.000Z" {
		t.Fatalf("next_check 必须保持新计划的值，实得 %q", nextCheck)
	}
}

// TestUpdateScheduleNextCheckAtSkipsWhenScheduleChangedAfterRead 同一栅栏覆盖
// 「只推进下次检查」分支：旧计划的边界不得覆盖新计划的边界。
func TestUpdateScheduleNextCheckAtSkipsWhenScheduleChangedAfterRead(t *testing.T) {
	store, db, _ := newTestStore(t)
	seedApiKeyWithSchedule(t, db, "key-race-2", availabilityScheduleJSON, "", "active")
	fresh := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"12:00","end":"13:00"}]}`
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_json = $1, availability_schedule_gate = 1, availability_schedule_next_check_at = '2026-09-07T13:00:00.000Z' WHERE id = 'key-race-2'`, fresh); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.updateScheduleNextCheckAt(context.Background(), tx, store.table("api_keys"), "key-race-2", "2026-09-07T18:00:00.000Z", availabilityScheduleJSON); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, nextCheck, _ := readApiKey(t, db, "key-race-2")
	if nextCheck != "2026-09-07T13:00:00.000Z" {
		t.Fatalf("next_check 必须保持新计划的值，实得 %q", nextCheck)
	}
}

// TestSyncApiKeyScheduleGateFlipKeepsRevisionStable 钉住 Key 侧 updated_at 契约：
// 闸门翻转不得推进 api_keys.updated_at（该列是对外 revision 与 PATCH CAS 基准），
// 否则窗口边界会在用户编辑期间制造 409。账户侧不受此约束。
func TestSyncApiKeyScheduleGateFlipKeepsRevisionStable(t *testing.T) {
	store, db, _ := newTestStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-rev", availabilityScheduleJSON, "", "active")
	setScheduleGate(t, db, "api_keys", "key-rev", 0)
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET updated_at = '2026-09-01T00:00:00.000Z' WHERE id = 'key-rev'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, nil); err != nil {
		t.Fatal(err)
	}
	requireGate(t, "开闸", readApiKeyGate(t, db, "key-rev"), "1")
	_, _, updatedAt := readApiKey(t, db, "key-rev")
	if updatedAt != "2026-09-01T00:00:00.000Z" {
		t.Fatalf("Key 闸门翻转不得推进 updated_at，实得 %q", updatedAt)
	}
}

// TestSyncAccountScheduleGateFlipAdvancesUpdatedAt 账户侧相反：updated_at 可推进
// （账户乐观锁用 config_revision）。
func TestSyncAccountScheduleGateFlipAdvancesUpdatedAt(t *testing.T) {
	store, db, _ := newTestStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "acc-rev", availabilityScheduleJSON, "", "active", false)
	setScheduleGate(t, db, "accounts", "acc-rev", 0)
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET updated_at = '2026-09-01T00:00:00.000Z' WHERE id = 'acc-rev'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, nil); err != nil {
		t.Fatal(err)
	}
	requireGate(t, "账户开闸", readAccountGate(t, db, "acc-rev"), "1")
	_, _, _ = readAccountStatus(t, db, "acc-rev")
}
