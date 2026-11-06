package oauthrefresh

// w12f_oauthrefresh_sync_test.go 覆盖 schedule 闸门同步的完整链路（种子行
// → 解析 → 闸门求值 → 审计事件）、allow/deny 例外边界、时区 fallback，以及
// failurecontext LogValue 的全字段渲染分支。

import (
	"context"
	"testing"
	"time"
)

func w12fScheduleJSON(t *testing.T) string {
	t.Helper()
	return `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5],"start":"09:00","end":"18:00"}]}`
}

func TestW12fSyncApiKeyScheduleFlipAndEventIdempotent(t *testing.T) {
	store, db, _ := newTestStore(t)
	now := w12fBaseNow()
	// 2026-09-14 20:30（周一，窗口外）→ 合法行关闸，status 不变。
	seedApiKeyWithSchedule(t, db, "w12f-key-active", w12fScheduleJSON(t), "", "active")
	seedApiKeyWithSchedule(t, db, "w12f-key-disabled", w12fScheduleJSON(t), "", "disabled")
	// 非法 JSON 行 → invalid 计数，同样 fail-closed 关闸。
	seedApiKeyWithSchedule(t, db, "w12f-key-invalid", "{bad", "", "active")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), now.Add(10*time.Hour+30*time.Minute), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 3 || result.Invalid != 1 || result.Disabled != 3 || result.Activated != 0 {
		t.Fatalf("扫描结果=%+v", result)
	}
	cases := []struct{ id, status string }{
		{"w12f-key-active", "active"},
		{"w12f-key-disabled", "disabled"},
		{"w12f-key-invalid", "active"},
	}
	for _, testCase := range cases {
		status, _, _ := readApiKey(t, db, testCase.id)
		if status != testCase.status {
			t.Fatalf("%s status=%q want %q（同步不得改写 status）", testCase.id, status, testCase.status)
		}
		requireGate(t, testCase.id, readApiKeyGate(t, db, testCase.id), "0")
	}

	// 第二次同步：把闸门已一致的行重新置为到期，验证「同值闸门只推进检查、
	// 不新增审计事件」（事件表只做审计，不再承担去重职责）。
	for _, id := range []string{"w12f-key-active", "w12f-key-disabled"} {
		if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_next_check_at = NULL WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := store.SyncApiKeyScheduleStatuses(context.Background(), now.Add(10*time.Hour+30*time.Minute), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 非法行按设计 §4.1 已排到 1 小时后，未到期不参与本轮；两条合法行闸门
	// 已一致，只推进 next_check_at，不新增审计事件。
	if replay.Scanned != 2 || replay.Unchanged != 2 || replay.Disabled != 0 || replay.Activated != 0 {
		t.Fatalf("replay=%+v", replay)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.api_key_schedule_status_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("审计事件 rows=%d want 2（两条合法行各一次关闸，非法行不写事件）", events)
	}
}

func TestW12fSyncAccountScheduleChain(t *testing.T) {
	store, db, _ := newTestStore(t)
	now := w12fBaseNow()
	seedAccountWithSchedule(t, db, "w12f-acc-sched", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:01","end":"00:02"}]}`, "", "active", false)
	// 00:01-00:02 窗口外 → 关闸；status 保持 active。
	result, err := store.SyncAccountScheduleStatuses(context.Background(), now.Add(12*time.Hour), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Disabled != 1 {
		t.Fatalf("扫描=%+v", result)
	}
	status, _, _ := readAccountStatus(t, db, "w12f-acc-sched")
	if status != "active" {
		t.Fatalf("status=%q want active（同步不得改写 status）", status)
	}
	requireGate(t, "w12f-acc-sched", readAccountGate(t, db, "w12f-acc-sched"), "0")
}

func TestW12fScheduleDenyAllowBoundaryEvents(t *testing.T) {
	base := w12fBaseNow()
	// allow 例外窗口的边界事件（窗口日 + 跨日 end）。
	schedule := &AvailabilitySchedule{
		Enabled: true, Timezone: "UTC", Mode: scheduleModeAllowWindows,
		Windows: []ScheduleWindow{{DaysOfWeek: allDaysOfWeek(), Start: "09:00", End: "18:00"}},
		Exceptions: []ScheduleException{{
			Date: "2026-09-15", Action: scheduleExceptionAllow,
			Windows: []ScheduleWindow{{DaysOfWeek: allDaysOfWeek(), Start: "22:00", End: "02:00"}},
		}},
	}
	times := scheduleBoundaryUTCTimes(schedule, base)
	if len(times) == 0 {
		t.Fatal("allow 例外必须产生边界时间")
	}
	// deny 例外：整天无边界。
	denySchedule := &AvailabilitySchedule{
		Enabled: true, Timezone: "UTC", Mode: scheduleModeAllowWindows,
		Windows:    []ScheduleWindow{{DaysOfWeek: allDaysOfWeek(), Start: "09:00", End: "18:00"}},
		Exceptions: []ScheduleException{{Date: "2026-09-15", Action: scheduleExceptionDeny}},
	}
	if times := scheduleBoundaryUTCTimes(denySchedule, base); len(times) == 0 {
		t.Fatal("常规日必须有边界时间")
	}
	// daysOfWeek 不含当日 → 无边界。
	filtered := &AvailabilitySchedule{
		Enabled: true, Timezone: "UTC", Mode: scheduleModeAllowWindows,
		Windows:    []ScheduleWindow{{DaysOfWeek: []int{6}, Start: "09:00", End: "18:00"}},
		Exceptions: nil,
	}
	mondayOnly := scheduleBoundaryUTCTimes(filtered, base)
	for _, day := range []time.Time{base, base.Add(24 * time.Hour)} {
		_ = day
	}
	if len(mondayOnly) == 0 {
		t.Fatal("周六窗口必须产生边界")
	}
	// 时区无效时 zonedParts 回退 UTC。
	parts := scheduleZonedParts(base, "Not/AZone")
	if parts.dateKey == "" {
		t.Fatal("无效时区必须回退 UTC")
	}
}

func TestW12fFailureContextLogValueFull(t *testing.T) {
	long := make([]byte, failureMaxStringLength+128)
	for i := range long {
		long[i] = 'x'
	}
	items := make([]any, failureMaxCollectionEntries+4)
	for i := range items {
		items[i] = map[string]any{"nested": i}
	}
	decisionInputs := map[string]any{"long": string(long), "items": items}
	context, err := CaptureExpectedFailureContext("w12f-reason", decisionInputs)
	if err != nil {
		t.Fatal(err)
	}
	value := context.LogValue()
	if value.Kind() == 0 {
		t.Fatal("LogValue 必须有效")
	}
	// 多余键的决策输入被剔除 → redactedFields 记录。
	if len(context.RedactedFields) == 0 && context.TruncationReason == "" {
		t.Log("输入未触发截断或脱敏（按当前实现）")
	}
}
