package oauthrefresh

// T91-7 / B3-H12 钉住测试：API Key 时间计划同步不再只依赖精确边界分钟。
// 错过的开始 / 结束边界在下一轮按「当前是否在允许窗口内」补偿；已执行过的
// 边界事件（含两个边界之间的人工启停）由事件表去重，不被持续覆盖。
//
// 与会话裁决的对应关系：
//  1. 错过开始边界 → 下一轮激活；
//  2. 错过结束边界 → 下一轮停用；
//  3. 窗口内状态已正确 → 不反复写（无事件行、updated_at 不变）；
//  4. 人工停用（窗口内）/ 人工启用（窗口外）在边界事件已执行后保持，
//     直到下一次计划边界接管（BUG-0025 契约）。

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

func seedScheduleEvent(t *testing.T, db *sql.DB, apiKeyID, eventKey, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.api_key_schedule_status_events (event_key, api_key_id, status, executed_at)
		VALUES ($1, $2, $3, $4)`, eventKey, apiKeyID, status, isoMillis(defaultNow())); err != nil {
		t.Fatal(err)
	}
}

// T91-7 臂 1：错过开始边界（边界分钟那轮没跑）后，下一轮按当前窗口激活。
func TestT917CatchUpActivatesAfterMissedStartBoundary(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// 09:00 边界本应执行但被错过；10:30 的下一轮扫描恢复。
	missedStart := "2026-09-07T09:00:00.000Z"
	seedApiKeyWithSchedule(t, db, "key-miss-start", availabilityScheduleJSON, missedStart, "disabled")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Activated != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-miss-start" {
		t.Fatalf("补偿激活结果不符: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-miss-start")
	if status != "active" {
		t.Fatalf("status=%q want active", status)
	}
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q want 窗口结束边界", nextCheck)
	}
	// 补偿事件沿用被错过边界的标准键：未来同一分钟重放会被去重。
	var eventKey string
	if err := db.QueryRow(`SELECT event_key FROM juhe_business.api_key_schedule_status_events WHERE event_key LIKE 'key-miss-start:%'`).Scan(&eventKey); err != nil {
		t.Fatal(err)
	}
	if eventKey != "key-miss-start:2026-09-07:540:start:window:0:start:09:00" {
		t.Fatalf("补偿事件键=%q（必须是被错过开始边界的标准键）", eventKey)
	}
}

// T91-7 臂 2：错过结束边界后，下一轮按窗口外停用。
func TestT917CatchUpDisablesAfterMissedEndBoundary(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-miss-end", availabilityScheduleJSON, "2026-09-07T18:00:00.000Z", "active")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 19, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-miss-end" {
		t.Fatalf("补偿停用结果不符: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-miss-end")
	if status != "disabled" {
		t.Fatalf("status=%q want disabled", status)
	}
	if nextCheck != "2026-09-08T09:00:00.000Z" {
		t.Fatalf("next check=%q want 次日开始边界", nextCheck)
	}
}

// T91-7 臂 3：窗口内状态已正确时不反复写（不新增事件行、不更新 updated_at）。
func TestT917NoRepeatedWritesInsideWindow(t *testing.T) {
	store, db, _ := newSweepStore(t)
	inside := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-stable", availabilityScheduleJSON, "2026-09-07T18:00:00.000Z", "active")
	_, _, updatedBefore := readApiKey(t, db, "key-stable")

	// next_check_at 未到期：不扫描。
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 0 {
		t.Fatalf("未到期行不应扫描: %+v", result)
	}

	// 人为把 next_check 拉回过去，模拟本轮到期扫描：窗口内状态一致 → 只推进
	// 下次检查，不写 status / 事件。
	for round := 0; round < 2; round++ {
		if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_next_check_at = '2026-09-07T10:00:00.000Z' WHERE id = 'key-stable'`); err != nil {
			t.Fatal(err)
		}
		result, err = store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Unchanged != 1 || result.Activated != 0 || result.Disabled != 0 || result.Skipped != 0 {
			t.Fatalf("第 %d 轮窗口内不应改状态: %+v", round+1, result)
		}
		status, nextCheck, updatedAfter := readApiKey(t, db, "key-stable")
		if status != "active" || nextCheck != "2026-09-07T18:00:00.000Z" {
			t.Fatalf("第 %d 轮状态/下次检查被改写: status=%q next=%q", round+1, status, nextCheck)
		}
		if updatedAfter != updatedBefore {
			t.Fatalf("第 %d 轮 updated_at 被改写: %q -> %q", round+1, updatedBefore, updatedAfter)
		}
		if events := countScheduleEvents(t, db, "key-stable"); events != 0 {
			t.Fatalf("第 %d 轮不应产生计划事件行, count=%d", round+1, events)
		}
	}
}

// T91-7 臂 4：窗口内人工停用，开始边界事件已执行 → 不补偿、保持停用，直到
// 结束边界（BUG-0025 契约）。
func TestT917ManualDisableInsideWindowIsNotOverridden(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-manual-disable", availabilityScheduleJSON, "2026-09-07T10:00:00.000Z", "disabled")
	seedScheduleEvent(t, db, "key-manual-disable", "key-manual-disable:2026-09-07:540:start:window:0:start:09:00", "active")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 0 || result.Skipped != 1 {
		t.Fatalf("已执行开始边界后的人工停用必须保持: %+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-manual-disable")
	if status != "disabled" {
		t.Fatalf("status=%q want disabled（人工停用不得被补偿覆盖）", status)
	}
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q want 结束边界", nextCheck)
	}
}

// T91-7 臂 5：窗口外人工启用，结束边界事件已执行 → 不补偿停用，直到下一个
// 开始边界接管（BUG-0025 契约）。
func TestT917ManualEnableOutsideWindowIsNotOverridden(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-manual-enable", availabilityScheduleJSON, "2026-09-07T19:00:00.000Z", "active")
	seedScheduleEvent(t, db, "key-manual-enable", "key-manual-enable:2026-09-07:1080:end:window:0:end:18:00", "disabled")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 19, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 0 || result.Skipped != 1 {
		t.Fatalf("已执行结束边界后的人工启用必须保持: %+v", result)
	}
	status, _, _ := readApiKey(t, db, "key-manual-enable")
	if status != "active" {
		t.Fatalf("status=%q want active（人工启用不得被补偿覆盖）", status)
	}
}

// T91-7 臂 6：跨午夜窗口的错过边界补偿（开始 22:00 / 结束次日 02:00）。
func TestT917CatchUpCrossMidnightWindow(t *testing.T) {
	const crossMidnight = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"22:00","end":"02:00"}]}`
	store, db, _ := newSweepStore(t)

	seedApiKeyWithSchedule(t, db, "key-cross-start", crossMidnight, "2026-09-07T22:00:00.000Z", "disabled")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 23, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("跨午夜窗口错过开始边界未激活: %+v", result)
	}
	if status, _, _ := readApiKey(t, db, "key-cross-start"); status != "active" {
		t.Fatalf("cross start status=%q", status)
	}
	// 第一轮已经把 key-cross-start 的 next_check 推进到 09-08 22:00；显式固定
	// 该值，让第二轮只观察到 key-cross-end 的补偿。
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_next_check_at = '2026-09-08T22:00:00.000Z' WHERE id = 'key-cross-start'`); err != nil {
		t.Fatal(err)
	}

	seedApiKeyWithSchedule(t, db, "key-cross-end", crossMidnight, "2026-09-08T02:00:00.000Z", "active")
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-cross-end" {
		t.Fatalf("跨午夜窗口错过结束边界未停用: %+v", result)
	}
	if status, nextCheck, _ := readApiKey(t, db, "key-cross-end"); status != "disabled" || nextCheck != "2026-09-08T22:00:00.000Z" {
		t.Fatalf("cross end status=%q next=%q", status, nextCheck)
	}
}

// T91-7 臂 7：多窗口计划只补偿最近一个本应执行的边界（14:00 开始）。
func TestT917CatchUpMultipleWindows(t *testing.T) {
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
		t.Fatalf("多窗口错过第二个开始边界未激活: %+v", result)
	}
	var eventKey string
	if err := db.QueryRow(`SELECT event_key FROM juhe_business.api_key_schedule_status_events WHERE event_key LIKE 'key-two-windows:%'`).Scan(&eventKey); err != nil {
		t.Fatal(err)
	}
	if eventKey != "key-two-windows:2026-09-07:840:start:window:1:start:14:00" {
		t.Fatalf("补偿事件键=%q want 第二个窗口开始边界", eventKey)
	}
}

// T91-7 臂 8：next_check 为空（导入 / 存量行）不触发补偿——没有「错过边界」
// 的事实，按既有「人工状态不被持续纠偏」保持。
func TestT917NullNextCheckDoesNotCatchUp(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-null-next", availabilityScheduleJSON, "", "disabled")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged != 1 || result.Activated != 0 {
		t.Fatalf("next_check 为空不应补偿: %+v", result)
	}
	if status, _, _ := readApiKey(t, db, "key-null-next"); status != "disabled" {
		t.Fatalf("status=%q want disabled", status)
	}
}

// T91-7 臂 9：账户计划同步保持既有语义（本轮裁决只覆盖 API Key）。
func TestT917AccountSyncKeepsBoundaryOnlySemantics(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedAccountWithSchedule(t, db, "acc-missed-start", availabilityScheduleJSON, "2026-09-07T09:00:00.000Z", "disabled", false)
	result, err := store.SyncAccountScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 0 || result.Unchanged != 1 {
		t.Fatalf("账户同步不应获得 API Key 补偿语义: %+v", result)
	}
	if status, _, _ := readAccountStatus(t, db, "acc-missed-start"); status != "disabled" {
		t.Fatalf("account status=%q want disabled", status)
	}
}

// T91-7 单元臂：补偿事件与「边界分钟当轮执行」的事件逐字段一致（action /
// status / 去重键），这是人工干预不被覆盖的前提。
func TestT917CatchUpEventMatchesBoundaryEvent(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		now     time.Time
		at      time.Time
		matches bool
	}{
		{
			name:    "窗口内回看开始边界",
			raw:     availabilityScheduleJSON,
			now:     time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC),
			at:      time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "窗口外回看结束边界",
			raw:     availabilityScheduleJSON,
			now:     time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC),
			at:      time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC),
			matches: true,
		},
		{
			name: "跨午夜回看次日结束边界",
			raw:  `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"22:00","end":"02:00"}]}`,
			now:  time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC),
			at:   time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC),
			matches: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			schedule, err := ParseScheduleJSON(testCase.raw)
			if err != nil {
				t.Fatal(err)
			}
			catchUp, ok := CatchUpScheduleEvent(schedule, testCase.now)
			if !ok {
				t.Fatal("补偿事件缺失")
			}
			boundary, ok := DueScheduleEvent(schedule, testCase.at)
			if !ok {
				t.Fatal("边界事件缺失")
			}
			if catchUp != boundary {
				t.Fatalf("补偿事件=%+v 与边界事件=%+v 不一致", catchUp, boundary)
			}
		})
	}
	// 停用计划（nil / enabled=false）没有补偿事件。
	if _, ok := CatchUpScheduleEvent(nil, time.Now()); ok {
		t.Fatal("nil 计划不应产生补偿事件")
	}
	if _, ok := CatchUpScheduleEvent(&AvailabilitySchedule{Enabled: false}, time.Now()); ok {
		t.Fatal("停用计划不应产生补偿事件")
	}
}

// T91-7 臂 10：dateRange 已结束、最后边界在十几天前的长停摆仍能定位到该
// 结束边界（回看窗口覆盖月度级积压）。
func TestT917CatchUpFindsLastBoundaryAfterDateRangeEnded(t *testing.T) {
	const bounded = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","dateRange":{"startDate":"2026-08-01","endDate":"2026-08-20"},
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`
	schedule, err := ParseScheduleJSON(bounded)
	if err != nil {
		t.Fatal(err)
	}
	event, ok := CatchUpScheduleEvent(schedule, time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("dateRange 结束边界必须仍可定位")
	}
	want := ScheduleDueEvent{
		Action:   "end",
		Status:   "disabled",
		EventKey: "2026-08-20:1080:end:window:0:end:18:00",
	}
	if event != want {
		t.Fatalf("补偿事件=%+v want %+v", event, want)
	}
}
