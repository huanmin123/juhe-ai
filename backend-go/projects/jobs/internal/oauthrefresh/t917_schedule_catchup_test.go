package oauthrefresh

// 时间计划同步的闸门语义钉住测试（《时间计划闸门与账户状态解耦设计》§4.1）。
//
// 2026-11-03 契约变更后：
//   - 同步任务每轮按「当前时刻是否落在允许窗口内」重算 availability_schedule_gate
//     （NULL = 无计划 / 恒开，1 = 时段内，0 = 时段外），不再写 status；
//   - 错过边界不再需要事件键补偿：闸门每轮重算，下一轮自然纠正（T91-7 的
//     补偿分支已删除，catchUpScheduleEvent 不再存在）；
//   - *_schedule_status_events 只承担翻转审计（status 列读作开/关），不参与
//     「是否写闸门」的判定；因此本文件不再钉死边界事件键；
//   - 人工停用 / 启用在同步前后保持不变，闸门照常翻转。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func countScheduleEvents(t *testing.T, db *sql.DB, apiKeyID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.api_key_schedule_status_events WHERE event_key LIKE $1`,
		apiKeyID+":%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// 臂 1：错过开始边界（边界分钟那轮没跑）后，下一轮按当前窗口开闸，status 不变。
func TestT917MissedStartBoundaryOpensGateNextRound(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// 09:00 边界本应执行但被错过；10:30 的下一轮扫描开闸。
	seedApiKeyWithSchedule(t, db, "key-miss-start", availabilityScheduleJSON, "2026-09-07T09:00:00.000Z", "disabled")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Activated != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-miss-start" {
		t.Fatalf("错过开始边界的开闸结果不符: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-miss-start")
	if status != "disabled" {
		t.Fatalf("status=%q want disabled（同步不改写 status）", status)
	}
	requireGate(t, "key-miss-start", readApiKeyGate(t, db, "key-miss-start"), "1")
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q want 窗口结束边界", nextCheck)
	}
	// 审计事件按目标闸门 + 当轮分钟记录，只用于复核翻转事实。
	var eventStatus string
	if err := db.QueryRow(`SELECT status FROM juhe_business.api_key_schedule_status_events WHERE event_key LIKE 'key-miss-start:%'`).Scan(&eventStatus); err != nil {
		t.Fatal(err)
	}
	if eventStatus != "active" {
		t.Fatalf("审计事件 status=%q want active（读作闸门开）", eventStatus)
	}
}

// 臂 2：错过结束边界后，下一轮按窗口外关闸，status 不变。
func TestT917MissedEndBoundaryClosesGateNextRound(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-miss-end", availabilityScheduleJSON, "2026-09-07T18:00:00.000Z", "active")
	setScheduleGate(t, db, "api_keys", "key-miss-end", 1)

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 19, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-miss-end" {
		t.Fatalf("错过结束边界的关闸结果不符: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-miss-end")
	if status != "active" {
		t.Fatalf("status=%q want active（同步不改写 status）", status)
	}
	requireGate(t, "key-miss-end", readApiKeyGate(t, db, "key-miss-end"), "0")
	if nextCheck != "2026-09-08T09:00:00.000Z" {
		t.Fatalf("next check=%q want 次日开始边界", nextCheck)
	}
}

// 臂 3：窗口内闸门已开时重复扫描不重复写（不新增审计行、不更新 updated_at）。
func TestT917NoRepeatedWritesInsideWindow(t *testing.T) {
	store, db, _ := newSweepStore(t)
	inside := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-stable", availabilityScheduleJSON, "2026-09-07T18:00:00.000Z", "active")
	setScheduleGate(t, db, "api_keys", "key-stable", 1)
	_, _, updatedBefore := readApiKey(t, db, "key-stable")

	// next_check_at 未到期：不扫描。
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 0 {
		t.Fatalf("未到期行不应扫描: %+v", result)
	}

	// 人为把 next_check 拉回过去，模拟本轮到期扫描：闸门一致 → 只推进下次
	// 检查，不写 status / 闸门 / 审计事件。
	for round := 0; round < 2; round++ {
		if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_next_check_at = '2026-09-07T10:00:00.000Z' WHERE id = 'key-stable'`); err != nil {
			t.Fatal(err)
		}
		result, err = store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Unchanged != 1 || result.Activated != 0 || result.Disabled != 0 || len(result.ChangedIDs) != 0 {
			t.Fatalf("第 %d 轮窗口内不应改闸门: %+v", round+1, result)
		}
		status, nextCheck, updatedAfter := readApiKey(t, db, "key-stable")
		if status != "active" || nextCheck != "2026-09-07T18:00:00.000Z" {
			t.Fatalf("第 %d 轮状态/下次检查被改写: status=%q next=%q", round+1, status, nextCheck)
		}
		requireGate(t, "key-stable", readApiKeyGate(t, db, "key-stable"), "1")
		if updatedAfter != updatedBefore {
			t.Fatalf("第 %d 轮 updated_at 被改写: %q -> %q", round+1, updatedBefore, updatedAfter)
		}
		if events := countScheduleEvents(t, db, "key-stable"); events != 0 {
			t.Fatalf("第 %d 轮不应产生审计事件行, count=%d", round+1, events)
		}
	}
}

// 臂 4a：窗口内人工停用后，同步只维护闸门，status 全程保持 disabled——闸门
// 照常随窗口翻转，但不会再复活人工停用。
func TestT917ManualDisableInsideWindowKeepsStatus(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-manual-disable", availabilityScheduleJSON, "2026-09-07T10:00:00.000Z", "disabled")
	setScheduleGate(t, db, "api_keys", "key-manual-disable", 1)

	// 窗口内：闸门已开，只有 next_check 推进。
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged != 1 || result.Activated != 0 || result.Disabled != 0 {
		t.Fatalf("窗口内人工停用必须只推进检查: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-manual-disable")
	if status != "disabled" {
		t.Fatalf("status=%q want disabled（人工停用不得被同步改写）", status)
	}
	requireGate(t, "key-manual-disable", readApiKeyGate(t, db, "key-manual-disable"), "1")
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q want 结束边界", nextCheck)
	}

	// 窗口外：闸门关，status 仍是 disabled。
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 19, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 {
		t.Fatalf("窗口外必须关闸: %+v", result)
	}
	status, nextCheck, _ = readApiKey(t, db, "key-manual-disable")
	if status != "disabled" {
		t.Fatalf("window end status=%q want disabled", status)
	}
	requireGate(t, "key-manual-disable", readApiKeyGate(t, db, "key-manual-disable"), "0")
	if nextCheck != "2026-09-08T09:00:00.000Z" {
		t.Fatalf("next check=%q want 次日开始边界", nextCheck)
	}
}

// 臂 4b：窗口外人工启用后，同步只维护闸门，status 全程保持 active——下个窗口
// 开始只开闸，不再通过 status 表达。
func TestT917ManualEnableOutsideWindowKeepsStatus(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-manual-enable", availabilityScheduleJSON, "2026-09-07T19:00:00.000Z", "active")
	setScheduleGate(t, db, "api_keys", "key-manual-enable", 0)

	// 窗口外：闸门已关，只有 next_check 推进。
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 19, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged != 1 || result.Activated != 0 || result.Disabled != 0 {
		t.Fatalf("窗口外人工启用必须只推进检查: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-manual-enable")
	if status != "active" {
		t.Fatalf("status=%q want active（人工启用不得被同步改写）", status)
	}
	requireGate(t, "key-manual-enable", readApiKeyGate(t, db, "key-manual-enable"), "0")
	if nextCheck != "2026-09-08T09:00:00.000Z" {
		t.Fatalf("next check=%q want 次日开始边界", nextCheck)
	}

	// 次日窗口开始：闸门打开，status 仍保持 active。
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("次日开始必须开闸: %+v", result)
	}
	status, _, _ = readApiKey(t, db, "key-manual-enable")
	if status != "active" {
		t.Fatalf("window start status=%q want active", status)
	}
	requireGate(t, "key-manual-enable", readApiKeyGate(t, db, "key-manual-enable"), "1")
}

// 臂 5：跨午夜窗口（22:00 / 次日 02:00）的闸门重算。
func TestT917CrossMidnightWindowGate(t *testing.T) {
	const crossMidnight = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"22:00","end":"02:00"}]}`
	store, db, _ := newSweepStore(t)

	seedApiKeyWithSchedule(t, db, "key-cross-start", crossMidnight, "2026-09-07T22:00:00.000Z", "disabled")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 23, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("跨午夜窗口错过开始边界的开闸结果不符: %+v", result)
	}
	if status, _, _ := readApiKey(t, db, "key-cross-start"); status != "disabled" {
		t.Fatalf("cross start status=%q want disabled", status)
	}
	requireGate(t, "key-cross-start", readApiKeyGate(t, db, "key-cross-start"), "1")
	// 第一轮已经把 key-cross-start 的 next_check 推进到 09-08 02:00；显式固定
	// 该值，让第二轮只观察到 key-cross-end 的关闸。
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_next_check_at = '2026-09-08T22:00:00.000Z' WHERE id = 'key-cross-start'`); err != nil {
		t.Fatal(err)
	}

	seedApiKeyWithSchedule(t, db, "key-cross-end", crossMidnight, "2026-09-08T02:00:00.000Z", "active")
	setScheduleGate(t, db, "api_keys", "key-cross-end", 1)
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-cross-end" {
		t.Fatalf("跨午夜窗口错过结束边界的关闸结果不符: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-cross-end")
	if status != "active" {
		t.Fatalf("cross end status=%q want active", status)
	}
	requireGate(t, "key-cross-end", readApiKeyGate(t, db, "key-cross-end"), "0")
	if nextCheck != "2026-09-08T22:00:00.000Z" {
		t.Fatalf("cross end next=%q", nextCheck)
	}
}

// 臂 6：多窗口计划按当前所在窗口重算闸门（不依赖「最近一个边界」）。
func TestT917MultipleWindowsGate(t *testing.T) {
	const twoWindows = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[
		{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"12:00"},
		{"daysOfWeek":[1,2,3,4,5,6,7],"start":"14:00","end":"18:00"}]}`
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-two-windows", twoWindows, "2026-09-07T14:00:00.000Z", "disabled")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 15, 0, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("多窗口第二个窗口内的开闸结果不符: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-two-windows")
	if status != "disabled" {
		t.Fatalf("status=%q want disabled", status)
	}
	requireGate(t, "key-two-windows", readApiKeyGate(t, db, "key-two-windows"), "1")
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q want 第二个窗口结束", nextCheck)
	}
}

// 臂 7：next_check 为空（导入 / 存量行）仍恒到期，闸门按当前窗口写入。
func TestT917NullNextCheckScannedAndGateOpens(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-null-next", availabilityScheduleJSON, "", "disabled")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Activated != 1 {
		t.Fatalf("next_check 为空必须到期并开闸: %+v", result)
	}
	if status, _, _ := readApiKey(t, db, "key-null-next"); status != "disabled" {
		t.Fatalf("status=%q want disabled", status)
	}
	requireGate(t, "key-null-next", readApiKeyGate(t, db, "key-null-next"), "1")
}

// 臂 8：账户计划同步与 Key 同口径——错过的开始边界由下一轮重算开闸，status
// 保持不变（B3-H11 的「账户只按边界事件」旧语义已删除）。
func TestT917AccountSyncGateCatchesUpMissedBoundary(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedAccountWithSchedule(t, db, "acc-missed-start", availabilityScheduleJSON, "2026-09-07T09:00:00.000Z", "disabled", false)
	result, err := store.SyncAccountScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Activated != 1 || result.Unchanged != 0 {
		t.Fatalf("账户错过开始边界必须开闸: %+v", result)
	}
	if status, _, _ := readAccountStatus(t, db, "acc-missed-start"); status != "disabled" {
		t.Fatalf("account status=%q want disabled（同步不改写 status）", status)
	}
	requireGate(t, "acc-missed-start", readAccountGate(t, db, "acc-missed-start"), "1")
}
