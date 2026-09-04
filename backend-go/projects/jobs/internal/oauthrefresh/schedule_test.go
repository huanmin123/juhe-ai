package oauthrefresh

import (
	"strings"
	"testing"
	"time"
)

func TestParseScheduleJSON(t *testing.T) {
	valid := `{"enabled":true,"timezone":"Asia/Shanghai","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5],"start":"09:00","end":"18:00"}]}`
	schedule, err := ParseScheduleJSON(valid)
	if err != nil {
		t.Fatal(err)
	}
	if schedule == nil || !schedule.Enabled || len(schedule.Windows) != 1 {
		t.Fatalf("schedule=%+v", schedule)
	}
	if schedule.Windows[0].DaysOfWeek[0] != 1 || schedule.Windows[0].Start != "09:00" {
		t.Fatalf("window=%+v", schedule.Windows[0])
	}
	if empty, err := ParseScheduleJSON(""); err != nil || empty != nil {
		t.Fatalf("empty parse=%v %v", empty, err)
	}
}

func TestParseScheduleJSONValidationCopy(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		expect string
	}{
		{"not enabled", `{"enabled":false,"timezone":"UTC","mode":"allow_windows","windows":[]}`, "API Key 时间计划启用状态必须为 true"},
		{"bad mode", `{"enabled":true,"timezone":"UTC","mode":"deny_all","windows":[]}`, "API Key 时间计划模式必须为 allow_windows"},
		{"bad timezone", `{"enabled":true,"timezone":"Mars/Olympus","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"10:00"}]}`, "API Key 时间计划时区无效"},
		{"no windows", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[]}`, "API Key 时间计划至少需要一个允许时段"},
		{"same start end", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"09:00"}]}`, "API Key 时间计划开始时间和停止时间不能相同"},
		{"unknown field", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"10:00"}],"extra":1}`, "API Key 时间计划包含不支持字段：extra"},
		{"bad day", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[8],"start":"09:00","end":"10:00"}]}`, "API Key 时间计划重复日期无效"},
		{"bad exception action", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"10:00"}],"exceptions":[{"date":"2026-09-04","action":"pause"}]}`, "API Key 时间计划例外动作无效"},
		{"deny with windows", `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"10:00"}],"exceptions":[{"date":"2026-09-04","action":"deny","windows":[{"start":"01:00","end":"02:00"}]}]}`, "API Key 时间计划拒绝例外不能配置允许时段"},
	}
	for _, testCase := range cases {
		_, err := ParseScheduleJSON(testCase.raw)
		if err == nil || !strings.Contains(err.Error(), testCase.expect) {
			t.Fatalf("%s: got %v want %q", testCase.name, err, testCase.expect)
		}
	}
}

func TestScheduleStatusAndBoundaries(t *testing.T) {
	schedule := mustParseSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`)

	// 09:01 UTC on a Monday → inside the window.
	inside := time.Date(2026, 9, 7, 9, 1, 0, 0, time.UTC) // Monday
	if status, ok := ScheduleStatus(schedule, inside); !ok || status != "active" {
		t.Fatalf("inside status=%q ok=%v", status, ok)
	}
	// 08:59 → outside.
	outside := inside.Add(-2 * time.Minute)
	if status, ok := ScheduleStatus(schedule, outside); !ok || status != "disabled" {
		t.Fatalf("outside status=%q ok=%v", status, ok)
	}
	// Nil/disabled schedules report absent status (NULL column semantics).
	if status, ok := ScheduleStatus(nil, inside); ok || status != "" {
		t.Fatalf("nil schedule status=%q ok=%v", status, ok)
	}

	// Next check after 09:01 is 18:00 the same day.
	next, ok := NextScheduleCheckAt(schedule, inside)
	if !ok || next != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q ok=%v", next, ok)
	}
	// At 18:00 exactly the next check is tomorrow 09:00.
	atEnd := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	next, _ = NextScheduleCheckAt(schedule, atEnd)
	if next != "2026-09-08T09:00:00.000Z" {
		t.Fatalf("next check at end=%q", next)
	}
}

func TestDueScheduleEvent(t *testing.T) {
	schedule := mustParseSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`)

	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	event, ok := DueScheduleEvent(schedule, atStart)
	if !ok || event.Action != "start" || event.Status != "active" {
		t.Fatalf("start event=%+v ok=%v", event, ok)
	}
	if event.EventKey != "2026-09-07:540:start:window:0:start:09:00" {
		t.Fatalf("start eventKey=%q", event.EventKey)
	}

	atEnd := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	event, ok = DueScheduleEvent(schedule, atEnd)
	if !ok || event.Action != "end" || event.Status != "disabled" {
		t.Fatalf("end event=%+v ok=%v", event, ok)
	}
	if event.EventKey != "2026-09-07:1080:end:window:0:end:18:00" {
		t.Fatalf("end eventKey=%q", event.EventKey)
	}

	// Off-boundary minutes produce no event.
	offBoundary, ok := DueScheduleEvent(schedule, atStart.Add(90*time.Second))
	if ok || offBoundary.EventKey != "" {
		t.Fatalf("off boundary=%+v ok=%v", offBoundary, ok)
	}
}

func TestDueScheduleEventCrossMidnight(t *testing.T) {
	schedule := mustParseSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"23:00","end":"01:00"}]}`)
	// The end boundary lands on Tuesday 01:00 (the window spills).
	atEnd := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	event, ok := DueScheduleEvent(schedule, atEnd)
	if !ok || event.Action != "end" || event.Status != "disabled" {
		t.Fatalf("cross midnight end=%+v ok=%v", event, ok)
	}
	if !strings.Contains(event.EventKey, "end:window:0:end:01:00") {
		t.Fatalf("cross midnight key=%q", event.EventKey)
	}
}

func TestDueScheduleEventException(t *testing.T) {
	schedule := mustParseSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows",
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}],
		"exceptions":[{"date":"2026-09-07","action":"deny"}]}`)
	denied := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	if _, ok := DueScheduleEvent(schedule, denied); ok {
		t.Fatal("denied date must not produce events")
	}
	if status, ok := ScheduleStatus(schedule, denied); !ok || status != "disabled" {
		t.Fatalf("denied status=%q ok=%v", status, ok)
	}

	allow := mustParseSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows",
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}],
		"exceptions":[{"date":"2026-09-07","action":"allow","windows":[{"start":"10:00","end":"11:00"}]}]}`)
	atAllowStart := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	event, ok := DueScheduleEvent(allow, atAllowStart)
	if !ok || event.Action != "start" || event.Status != "active" {
		t.Fatalf("allow event=%+v ok=%v", event, ok)
	}
	if !strings.Contains(event.EventKey, "exception:2026-09-07:0:start:10:00") {
		t.Fatalf("allow key=%q", event.EventKey)
	}
	// The regular 09:00 window start on the allow-exception date must not
	// fire.
	atRegular := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	if _, ok := DueScheduleEvent(allow, atRegular); ok {
		t.Fatal("allow exception replaces regular windows on that date")
	}
}

func TestNextScheduleCheckFallback(t *testing.T) {
	// A schedule with no boundary in the horizon falls back to now + 7 days.
	schedule := mustParseSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"10:00"}],"dateRange":{"startDate":"2020-01-01","endDate":"2020-01-02"}}`)
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	next, ok := NextScheduleCheckAt(schedule, now)
	if !ok || next != "2026-09-14T09:00:00.000Z" {
		t.Fatalf("fallback next=%q ok=%v", next, ok)
	}
}

func mustParseSchedule(t *testing.T, raw string) *AvailabilitySchedule {
	t.Helper()
	schedule, err := ParseScheduleJSON(raw)
	if err != nil || schedule == nil {
		t.Fatalf("parse=%v err=%v", schedule, err)
	}
	return schedule
}
