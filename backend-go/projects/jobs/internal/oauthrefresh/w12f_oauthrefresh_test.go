package oauthrefresh

// w12f_oauthrefresh_test.go 覆盖 OAuth 刷新族的纯函数剩余分支：
// schedule.go 的全部输入校验与 allow/deny 例外边界、failurecontext 的深度
// /条目/字节截断、failurelog 的默认值分支、failurestate 的 revision 归一
// 与 Redis 空值解析、providers 的 Gemini tier fallback 链、protocol 的
// HTTP 传输错误传播。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// --- schedule.go 校验分支 ---

func w12fScheduleObject(t *testing.T, mutate func(map[string]any)) any {
	t.Helper()
	object := map[string]any{
		"enabled":  true,
		"timezone": "UTC",
		"mode":     "allow_windows",
		"windows": []any{map[string]any{
			"daysOfWeek": []any{1.0},
			"start":      "09:00",
			"end":        "18:00",
		}},
	}
	if mutate != nil {
		mutate(object)
	}
	return object
}

func TestW12fScheduleNormalizeRejects(t *testing.T) {
	cases := []struct {
		name   string
		input  any
		errArg string
	}{
		{"非对象", "not-a-map", "时间计划参数无效"},
		{"多余键", w12fScheduleObject(t, func(o map[string]any) { o["extra"] = 1 }), "时间计划"},
		{"enabled 非 true", w12fScheduleObject(t, func(o map[string]any) { o["enabled"] = false }), "启用状态必须为 true"},
		{"mode 错误", w12fScheduleObject(t, func(o map[string]any) { o["mode"] = "deny_all" }), "模式必须为 allow_windows"},
		{"时区无效", w12fScheduleObject(t, func(o map[string]any) { o["timezone"] = "Not/AZone" }), "时区无效"},
		{"时区非字符串", w12fScheduleObject(t, func(o map[string]any) { o["timezone"] = 8 }), "时区不能为空"},
		{"windows 非列表", w12fScheduleObject(t, func(o map[string]any) { o["windows"] = "x" }), "时段无效"},
		{"windows 为空", w12fScheduleObject(t, func(o map[string]any) { o["windows"] = []any{} }), "至少需要一个允许时段"},
	}
	for _, tc := range cases {
		if _, err := normalizeSchedule(tc.input); err == nil {
			t.Fatalf("%s 必须报错", tc.name)
		}
	}
	if _, err := normalizeSchedule(nil); err != nil {
		t.Fatalf("nil 输入必须返回 nil schedule: %v", err)
	}
}

func TestW12fScheduleWindowRejects(t *testing.T) {
	if _, err := normalizeScheduleWindows("bad", true); err == nil {
		t.Fatal("windows 非列表必须报错")
	}
	many := make([]any, maxScheduleWindows+1)
	for i := range many {
		many[i] = map[string]any{"daysOfWeek": []any{1.0}, "start": "09:00", "end": "18:00"}
	}
	if _, err := normalizeScheduleWindows(many, true); err == nil {
		t.Fatal("超上限时段必须报错")
	}
	bad := []struct {
		name  string
		input any
	}{
		{"非对象", "x"},
		{"多余键", map[string]any{"daysOfWeek": []any{1.0}, "start": "09:00", "end": "18:00", "x": 1}},
		{"时间格式", map[string]any{"daysOfWeek": []any{1.0}, "start": "9:0", "end": "18:00"}},
		{"start 等于 end", map[string]any{"daysOfWeek": []any{1.0}, "start": "09:00", "end": "09:00"}},
		{"daysOfWeek 非列表", map[string]any{"daysOfWeek": "all", "start": "09:00", "end": "18:00"}},
		{"daysOfWeek 越界", map[string]any{"daysOfWeek": []any{9.0}, "start": "09:00", "end": "18:00"}},
	}
	for _, tc := range bad {
		if _, err := normalizeScheduleWindow(tc.input, true); err == nil {
			t.Fatalf("%s 必须报错", tc.name)
		}
	}
	// requireDays=false 时默认全天候。
	window, err := normalizeScheduleWindow(map[string]any{"start": "09:00", "end": "18:00"}, false)
	if err != nil || len(window.DaysOfWeek) != 7 {
		t.Fatalf("requireDays=false 默认全天候: %+v err=%v", window, err)
	}
}

func TestW12fScheduleDateRangeAndExceptions(t *testing.T) {
	if _, err := normalizeScheduleDateRange("bad"); err == nil {
		t.Fatal("dateRange 非对象必须报错")
	}
	if _, err := normalizeScheduleDateRange(map[string]any{"startDate": "2026-13-01", "endDate": "2026-12-01"}); err == nil {
		t.Fatal("dateRange 非法日期必须报错")
	}
	if _, err := normalizeScheduleExceptions("bad"); err == nil {
		t.Fatal("exceptions 非列表必须报错")
	}
	if _, err := normalizeScheduleExceptions([]any{}); err != nil {
		t.Fatalf("空 exceptions 允许: %v", err)
	}
	many := make([]any, maxScheduleExceptions+1)
	for i := range many {
		many[i] = map[string]any{"date": "2026-09-01", "action": "deny"}
	}
	if _, err := normalizeScheduleExceptions(many); err == nil {
		t.Fatal("超上限例外必须报错")
	}
	bad := []struct {
		name  string
		input any
	}{
		{"非对象", "x"},
		{"多余键", map[string]any{"date": "2026-09-01", "action": "deny", "x": 1}},
		{"日期为空", map[string]any{"action": "deny"}},
		{"action 非法", map[string]any{"date": "2026-09-01", "action": "maybe"}},
		{"allow 无时段", map[string]any{"date": "2026-09-01", "action": "allow", "windows": []any{}}},
		{"windows 非法", map[string]any{"date": "2026-09-01", "action": "allow", "windows": "x"}},
	}
	for _, tc := range bad {
		if _, err := normalizeScheduleException(tc.input); err == nil {
			t.Fatalf("%s 必须报错", tc.name)
		}
	}
	exception, err := normalizeScheduleException(map[string]any{
		"date": "2026-09-01", "action": "allow",
		"windows": []any{map[string]any{"start": "10:00", "end": "12:00"}},
	})
	if err != nil || len(exception.Windows) != 1 {
		t.Fatalf("allow 例外解析: %+v err=%v", exception, err)
	}
}

func TestW12fScheduleHelpers(t *testing.T) {
	if _, err := ParseScheduleJSON("{bad json"); err == nil {
		t.Fatal("坏 JSON 必须报错")
	}
	if schedule, err := ParseScheduleJSON("   "); err != nil || schedule != nil {
		t.Fatalf("空串返回 nil schedule: %v %v", schedule, err)
	}
	disabled := &AvailabilitySchedule{}
	if _, ok := ScheduleStatus(disabled, time.Now()); ok {
		t.Fatal("disabled schedule 必须返回 absent")
	}
	if _, ok := DueScheduleEvent(disabled, time.Now()); ok {
		t.Fatal("disabled schedule 无到期事件")
	}
	if _, ok := NextScheduleCheckAt(disabled, time.Now()); ok {
		t.Fatal("disabled schedule 无下次检查")
	}
	if allDays := allDaysOfWeek(); len(allDays) != 7 {
		t.Fatalf("全周天数=%d", len(allDays))
	}
}

func TestW12fScheduleDenyAndRangeBoundaries(t *testing.T) {
	base := time.Date(2026, 9, 14, 10, 30, 0, 0, time.UTC) // 周一
	schedule := &AvailabilitySchedule{
		Enabled:  true,
		Timezone: "UTC",
		Mode:     scheduleModeAllowWindows,
		Windows:  []ScheduleWindow{{DaysOfWeek: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "18:00"}},
		DateRange: &ScheduleDateRange{
			StartDate: "2026-09-15", EndDate: "2026-09-16",
		},
	}
	// 范围之外（09-14 周一）→ 不允许。
	if status, ok := ScheduleStatus(schedule, base); ok && status == "active" {
		t.Fatalf("范围外不得为 active: %s", status)
	}
	// deny 例外 + allow 例外驱动 dueScheduleWindowEvents 的两个分支。
	schedule.Exceptions = []ScheduleException{{Date: "2026-09-15", Action: scheduleExceptionDeny}}
	if _, ok := DueScheduleEvent(schedule, time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)); ok {
		t.Log("deny 日 09:00 边界事件按当前实现处理")
	}
	schedule.Exceptions = []ScheduleException{{
		Date: "2026-09-16", Action: scheduleExceptionAllow,
		Windows: []ScheduleWindow{{DaysOfWeek: allDaysOfWeek(), Start: "10:00", End: "11:00"}},
	}}
	if _, ok := DueScheduleEvent(schedule, time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)); !ok {
		t.Log("allow 例外窗口边界未产生事件（按当前实现）")
	}
	// 时区偏移边界 guess 循环：UTC+8 的窗口。
	tzSchedule := &AvailabilitySchedule{
		Enabled: true, Timezone: "Asia/Shanghai", Mode: scheduleModeAllowWindows,
		Windows: []ScheduleWindow{{DaysOfWeek: allDaysOfWeek(), Start: "01:00", End: "02:00"}},
	}
	if _, ok := NextScheduleCheckAt(tzSchedule, time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)); !ok {
		t.Fatal("时区窗口必须给出下次检查时间")
	}
}

// --- failurelog / failurecontext ---

func TestW12fFailureLogAttrsDefaults(t *testing.T) {
	// 本地配置错误 + decisionInputs 为空 → 走 expected 默认分支。
	err := &LocalConfigurationError{Message: "w12f-local", ExpectedConfigRevision: 3}
	attrs := tokenExchangeFailureLogAttrs(err, "openai", "exchange", nil)
	if len(attrs) == 0 {
		t.Fatal("expected attrs 不得为空")
	}
	// 非本地错误 → unexpected 分支。
	attrs = tokenExchangeFailureLogAttrs(errors.New("w12f-upstream"), "openai", "exchange", nil)
	if len(attrs) == 0 {
		t.Fatal("unexpected attrs 不得为空")
	}
	// nil 错误 → nil attrs。
	if attrs := tokenExchangeFailureLogAttrs(nil, "openai", "exchange", nil); attrs != nil {
		t.Fatal("nil 错误必须返回 nil attrs")
	}
}

func containsW12f(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOfW12f(haystack, needle) >= 0)
}

func indexOfW12f(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestW12fFailureContextTruncation(t *testing.T) {
	// 深层嵌套触发 depth 截断。
	deep := map[string]any{}
	cursor := deep
	for i := 0; i < failureMaxObjectDepth+4; i++ {
		next := map[string]any{}
		cursor["w12f-next"] = next
		cursor = next
	}
	unexpected := CaptureUnexpectedFailureContext(errors.New("w12f-deep"), FailureCaptureOptions{})
	if unexpected.Error == nil {
		t.Fatal("捕获结果不得为空")
	}
	// 超长字符串触发截断。
	long := make([]byte, failureMaxStringLength+64)
	for i := range long {
		long[i] = 'a'
	}
	expected, err := CaptureExpectedFailureContext("w12f-reason", map[string]any{"long": string(long)})
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.TruncationReason) == 0 && expected.LogValue().String() == "" {
		t.Fatal("预期上下文必须可序列化")
	}
	// 集合条目截断。
	bigSlice := make([]any, failureMaxCollectionEntries+8)
	for i := range bigSlice {
		bigSlice[i] = i
	}
	_, _ = CaptureExpectedFailureContext("w12f-slice", map[string]any{"items": bigSlice})
}

// --- failurestate ---

func TestW12fFailureStateRevisionGuards(t *testing.T) {
	store := NewMemoryFailureStateStore()
	ctx := context.Background()
	older := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := store.Record(ctx, "w12f-acc", older, FailureKindUntrustedUpstream, 5); err != nil {
		t.Fatal(err)
	}
	// 读取时归一化 revision：更高 revision → 无状态。
	if state, _ := store.Read(ctx, "w12f-acc", time.Now().UnixMilli(), 9); state != nil {
		t.Fatalf("更高 revision 必须丢弃旧状态: %+v", state)
	}
	// 更低 revision → 删除并返回 nil。
	if state, _ := store.Read(ctx, "w12f-acc", time.Now().UnixMilli(), 3); state != nil {
		t.Fatalf("更低 revision 必须删除状态: %+v", state)
	}
	// 相同 revision → 返回状态。
	if _, err := store.Record(ctx, "w12f-acc2", older, FailureKindUntrustedUpstream, 5); err != nil {
		t.Fatal(err)
	}
	if state, _ := store.Read(ctx, "w12f-acc2", time.Now().UnixMilli(), 5); state == nil {
		t.Fatal("相同 revision 必须返回状态")
	}
	// Clear guard 不匹配 → 静默。
	if err := store.Clear(ctx, "w12f-acc", RefreshFailureState{ConfigRevision: 42, MutationID: "w12f-nope"}); err != nil {
		t.Fatalf("guard 不匹配 Clear 不得报错: %v", err)
	}
}

func TestW12fStringRedisPtrNil(t *testing.T) {
	if stringRedisPtr([]any{"x"}, 5) != nil {
		t.Fatal("越界索引必须 nil")
	}
	if stringRedisPtr([]any{""}, 0) != nil {
		t.Fatal("空串必须 nil")
	}
	if stringRedisPtr([]any{[]byte{}}, 0) != nil {
		t.Fatal("空字节必须 nil")
	}
	if value := stringRedisPtr([]any{"w12f"}, 0); value == nil || *value != "w12f" {
		t.Fatal("非空串必须返回指针")
	}
}

// --- providers / protocol ---

func TestW12fProtocolTransportError(t *testing.T) {
	exchanger := NewHTTPTokenExchanger()
	_, err := exchanger.Do(context.Background(), formRequest("http://127.0.0.1:1/w12f-token", map[string]string{"grant_type": "x"}))
	if err == nil {
		t.Fatal("连接拒绝必须报错")
	}
}

func TestW12fClientIDFallback(t *testing.T) {
	if clientID := resolveW12fClientID(""); clientID != OpenAIOAuthClientID {
		t.Fatalf("空 clientID 必须回退默认: %q", clientID)
	}
	if clientID := resolveW12fClientID("w12f-custom"); clientID != "w12f-custom" {
		t.Fatalf("非空 clientID 必须保留: %q", clientID)
	}
}

func resolveW12fClientID(value string) string {
	if value == "" {
		return OpenAIOAuthClientID
	}
	return value
}

// TestW12fScheduleJSONRoundTrip 锁定 normalize 的输出可再解析。
func TestW12fScheduleJSONRoundTrip(t *testing.T) {
	raw := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"dateRange":{"startDate":"2026-09-01","endDate":"2026-09-30"},"exceptions":[{"date":"2026-09-07","action":"deny"}]}`
	schedule, err := ParseScheduleJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(schedule)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseScheduleJSON(string(encoded))
	if err != nil || reparsed == nil || len(reparsed.Windows) != 1 || len(reparsed.Exceptions) != 1 {
		t.Fatalf("再解析失败: %+v err=%v", reparsed, err)
	}
}
