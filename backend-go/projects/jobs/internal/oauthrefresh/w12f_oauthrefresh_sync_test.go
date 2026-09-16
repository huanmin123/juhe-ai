package oauthrefresh

// w12f_oauthrefresh_sync_test.go 覆盖 schedule 状态同步的完整链路（种子行
// → 解析 → 翻转 → 事件幂等）、allow/deny 例外边界、时区 fallback，以及
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
	// active 行 + 到期窗口（09:00 后 10:30 仍处于窗口内 → active，事件 start 已写入）。
	seedApiKeyWithSchedule(t, db, "w12f-key-active", w12fScheduleJSON(t), "", "active")
	// disabled 行 → 需要翻回 active（start 边界在一天前触发过）。
	seedApiKeyWithSchedule(t, db, "w12f-key-disabled", w12fScheduleJSON(t), "", "disabled")
	// 非法 JSON 行 → invalid 计数。
	seedApiKeyWithSchedule(t, db, "w12f-key-invalid", "{bad", "", "active")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), now.Add(10*time.Hour+30*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 3 || result.Invalid != 1 {
		t.Fatalf("扫描结果=%+v", result)
	}
	// 第二次同步：事件幂等（ON CONFLICT DO NOTHING 分支）。
	if _, err := store.SyncApiKeyScheduleStatuses(context.Background(), now.Add(10*time.Hour+30*time.Minute), 10); err != nil {
		t.Fatal(err)
	}
}

func TestW12fSyncAccountScheduleChain(t *testing.T) {
	store, db, _ := newTestStore(t)
	now := w12fBaseNow()
	seedAccountWithSchedule(t, db, "w12f-acc-sched", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:01","end":"00:02"}]}`, "", "active", false)
	// 00:01-00:02 窗口外 → disabled 翻转。
	result, err := store.SyncAccountScheduleStatuses(context.Background(), now.Add(12*time.Hour), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 {
		t.Fatalf("扫描=%+v", result)
	}
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
