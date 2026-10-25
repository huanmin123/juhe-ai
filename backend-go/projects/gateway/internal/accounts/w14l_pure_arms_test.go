package accounts

// w14l 覆盖率收尾（二）：纯函数臂、批量字段臂、授权调度消息臂、
// 失效回调错误臂与 retryQueue 状态臂。全部为包内直驱，不依赖网络。

import (
	"database/sql"
	"errors"
	"net"
	"testing"
	"time"
)

// --- 授权调度 body 解析臂 ---

func TestW14LAuthorizedDispatchBodyArms(t *testing.T) {
	if _, message := parseAuthorizedDispatchBody(map[string]any{"bogus": 1}); message == "" {
		t.Fatal("未知键应拒绝")
	}
	if _, message := parseAuthorizedDispatchBody(map[string]any{}); message == "" {
		t.Fatal("空 body 应拒绝")
	}
	if _, message := parseAuthorizedDispatchBody(map[string]any{"clearFailureState": false}); message == "" {
		t.Fatal("单独 clearFailureState=false 应拒绝")
	}
	invalidBodies := []map[string]any{
		{"priority": float64(1)},
		{"expectedConfigRevision": "x", "priority": float64(1)},
		{"expectedConfigRevision": float64(0.5), "priority": float64(1)},
		{"expectedConfigRevision": float64(0), "priority": float64(1)},
		{"expectedConfigRevision": float64(1), "status": 3},
		{"expectedConfigRevision": float64(1), "status": "bogus"},
		{"expectedConfigRevision": float64(1), "priority": "x"},
		{"expectedConfigRevision": float64(1), "priority": float64(1.5)},
		{"expectedConfigRevision": float64(1), "priority": float64(-1)},
		{"expectedConfigRevision": float64(1), "superPriorityEnabled": "yes"},
		{"expectedConfigRevision": float64(1), "fallbackEnabled": 1},
		{"expectedConfigRevision": float64(1), "clearFailureState": "true"},
	}
	for index, body := range invalidBodies {
		if _, message := parseAuthorizedDispatchBody(body); message == "" {
			t.Fatalf("非法 body #%d 应拒绝：%v", index, body)
		}
	}
	input, message := parseAuthorizedDispatchBody(map[string]any{
		"expectedConfigRevision": float64(2), "status": "active", "priority": float64(3),
		"superPriorityEnabled": true, "clearFailureState": true,
	})
	if message != "" || input.ExpectedConfigRevision != 2 || input.Status == nil || input.Priority == nil ||
		input.SuperPriorityEnabled == nil || !input.ClearFailureState {
		t.Fatalf("合法 body 应解析成功：%+v %q", input, message)
	}
}

// --- 授权调度事务补丁臂（直接驱动，真实事务末尾回滚） ---

// --- 时间计划解析臂 ---

func TestW14LScheduleNormalizeArms(t *testing.T) {
	baseSchedule := func(extra map[string]any) map[string]any {
		object := map[string]any{
			"enabled": true, "timezone": "UTC", "mode": "allow_windows",
			"windows": []any{map[string]any{"daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)}, "start": "00:00", "end": "23:59"}},
		}
		for key, value := range extra {
			object[key] = value
		}
		return object
	}
	// 例外日期：非法动作 / deny 带窗口 / allow 无窗口 / 缺日期 / 多余键 / 非对象 / 非列表 / 非法日期。
	badSchedules := []map[string]any{
		baseSchedule(map[string]any{"exceptions": "not-a-list"}),
		baseSchedule(map[string]any{"exceptions": []any{"not-an-object"}}),
		baseSchedule(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "allow", "windows": []any{}, "extra": 1}}}),
		baseSchedule(map[string]any{"exceptions": []any{map[string]any{"date": "", "action": "allow", "windows": []any{map[string]any{"start": "01:00", "end": "02:00"}}}}}),
		baseSchedule(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "toggle"}}}),
		baseSchedule(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "deny", "windows": []any{map[string]any{"start": "01:00", "end": "02:00"}}}}}),
		baseSchedule(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "allow", "windows": []any{}}}}),
		baseSchedule(map[string]any{"exceptions": []any{map[string]any{"date": "2026-13-01", "action": "deny"}}}),
	}
	for index, object := range badSchedules {
		if _, err := NormalizeSchedule(object); err == nil {
			t.Fatalf("非法时间计划 #%d 应拒绝", index)
		}
	}
	// 合法：允许例外 + 日期范围。
	schedule, err := NormalizeSchedule(baseSchedule(map[string]any{
		"dateRange":  map[string]any{"startDate": "2026-01-01", "endDate": "2026-12-31"},
		"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "deny"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if schedule.DateRange == nil || len(schedule.Exceptions) != 1 {
		t.Fatalf("例外与日期范围应保留：%+v", schedule)
	}
	// 日期范围倒置与时段错误。
	if _, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows":   []any{map[string]any{"daysOfWeek": []any{float64(1)}, "start": "01:00", "end": "02:00"}},
		"dateRange": map[string]any{"startDate": "2026-12-31", "endDate": "2026-01-01"},
	}); err == nil {
		t.Fatal("日期范围倒置应拒绝")
	}
	if _, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows": []any{map[string]any{"daysOfWeek": []any{float64(1)}, "start": "01:00", "end": "01:00"}},
	}); err == nil {
		t.Fatal("开始等于停止应拒绝")
	}
	if _, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "Bogus/Zone", "mode": "allow_windows",
		"windows": []any{map[string]any{"daysOfWeek": []any{float64(1)}, "start": "01:00", "end": "02:00"}},
	}); err == nil {
		t.Fatal("非法时区应拒绝")
	}
	// 过期日期范围：范围外一律 denied 覆盖，且未来两周无边界（回退 7 天）。
	expired, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows":   []any{map[string]any{"daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)}, "start": "00:00", "end": "23:59"}},
		"dateRange": map[string]any{"startDate": "2020-01-01", "endDate": "2020-01-02"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	if override, ok := ScheduleStatus(expired, now); !ok || override != "disabled" {
		t.Fatalf("范围外应覆盖为 disabled：%q %v", override, ok)
	}
	if _, ok := NextScheduleCheckAt(expired, now); !ok {
		t.Fatal("范围外应回退到 7 天后的检查点")
	}
	// JSON 往返与解析失败。
	raw, ok := ScheduleJSON(expired)
	if !ok || raw == "" {
		t.Fatal("计划应可序列化")
	}
	parsed, err := ParseScheduleJSON(raw)
	if err != nil || parsed == nil {
		t.Fatalf("JSON 应可解析：%v", err)
	}
	if _, err := ParseScheduleJSON("{bogus"); err == nil {
		t.Fatal("非法 JSON 应拒绝")
	}
	// 日期键进阶（跨天窗口边界）。
	if nextDateKey("2026-12-31") != "2027-01-01" {
		t.Fatalf("日期进阶不符：%s", nextDateKey("2026-12-31"))
	}
}

// --- 测试选项纯函数臂 ---

func TestW14LTestOptionPureArms(t *testing.T) {
	badJSON := sql.NullString{String: "not-json", Valid: true}
	if got := testParseJSONArray(badJSON); len(got) != 0 {
		t.Fatalf("非法 JSON 应为空：%v", got)
	}
	mixed := sql.NullString{String: `["a", 2, "b"]`, Valid: true}
	if got := testParseJSONArray(mixed); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("非字符串成员应跳过：%v", got)
	}
	if got, err := endpointModeProtocolFamily("images_json"); got != "" || err == nil {
		t.Fatal("images_json 应报错")
	}
	if family, err := endpointModeProtocolFamily("mystery_mode"); err != nil || family != "generate_content" {
		t.Fatalf("未列模式应走 default 臂：%q %v", family, err)
	}
	if normalizedOptionReleaseDate("2026-01-XY") != "" {
		t.Fatal("非法日期字符应为空")
	}
	if normalizedOptionReleaseDate("2026-13-01") != "" {
		t.Fatal("非法月份应为空")
	}
	if normalizedOptionReleaseDate("2026-03-01T10:00:00Z") != "2026-03-01" {
		t.Fatal("时间戳应截断到日期")
	}
	rows := []testOptionRow{
		{Provider: "  ", Model: "m1"},
		{Provider: "p1", Model: ""},
		{Provider: "p1", Model: "gpt-4o", Scope: catalogScopeBuiltIn, ReleaseDate: "2026-01-01"},
		{Provider: "p2", Model: "gpt-4o", Scope: catalogScopePersonal, ReleaseDate: "2026-02-01T00:00:00Z"},
		{Provider: "p3", Model: "zzz", Scope: catalogScopeGlobal},
	}
	merged := mergeTestOptionRows(rows, ManualTestOptionsQuery{Keyword: "gpt", SelectedIDs: []string{"zzz"}, Limit: 5})
	if len(merged) != 2 || merged[0].Model != "gpt-4o" || merged[1].Model != "zzz" {
		t.Fatalf("合并结果不符：%+v", merged)
	}
	limited := mergeTestOptionRows(rows, ManualTestOptionsQuery{Limit: 0})
	if len(limited) != 0 {
		t.Fatalf("limit=0 应全部排除：%+v", limited)
	}
}

// --- 上游 URL/IP 纯函数臂 ---

func TestW14LUpstreamIPArms(t *testing.T) {
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("非法 IPv6 网段应 panic")
			}
		}()
		mustIPv6Network("not-an-ip")
	}()
	if !isBlockedIPv6(nil) {
		t.Fatal("nil IP 应视为被阻断")
	}
	if isBlockedIPv6(net.ParseIP("2001:4860:4860::8888")) {
		t.Fatal("公网 IPv6 不应被阻断")
	}
	if !isBlockedIPv6(net.ParseIP("fc00::1")) {
		t.Fatal("ULA IPv6 应被阻断")
	}
	if !isBlockedIPv4(net.ParseIP("10.1.2.3").To4()) || isBlockedIPv4(net.ParseIP("8.8.8.8").To4()) {
		t.Fatal("IPv4 私网判定不符")
	}
	// ipv4MatchesPrefix 边界：前缀 0 立即通过，前缀 32 全匹配，中途失配。
	if !ipv4MatchesPrefix([4]byte{1, 2, 3, 4}, [4]byte{9, 9, 9, 9}, 0) {
		t.Fatal("前缀 0 应匹配")
	}
	if !ipv4MatchesPrefix([4]byte{1, 2, 3, 4}, [4]byte{1, 2, 3, 4}, 32) {
		t.Fatal("前缀 32 全等应匹配")
	}
	if ipv4MatchesPrefix([4]byte{1, 2, 3, 4}, [4]byte{1, 2, 9, 9}, 24) {
		t.Fatal("第三字节失配不应匹配")
	}
}

// --- 批量字段臂（经真实 BatchUpdate 驱动） ---

// --- 失效回调错误臂 ---

type w14lErrorInvalidator struct{}

func (w14lErrorInvalidator) InvalidateAccountLookup(string) error {
	return errors.New("w14l lookup 失效失败")
}
func (w14lErrorInvalidator) InvalidateGatewayRuntime(string) error {
	return errors.New("w14l runtime 失效失败")
}
func (w14lErrorInvalidator) InvalidateGroupAccountIds() error {
	return errors.New("w14l 分组失效失败")
}
func (w14lErrorInvalidator) ClearResourceAuthorizationLookupCaches() error {
	return errors.New("w14l 授权清理失败")
}
func (w14lErrorInvalidator) InvalidateAuthorizationQuota(string) error {
	return errors.New("w14l 额度失效失败")
}

// --- retryQueue 状态臂 ---

func TestW14LRetryQueueArms(t *testing.T) {
	exhausted := 0
	queue := newRetryQueue[string]("w14l-queue", []int64{1, 1}, 2,
		func(item string, attemptIndex int) error { return errors.New("w14l 恒败") },
		retryQueueCallbacks[string]{OnExhausted: func(retryQueueEvent[string]) { exhausted++ }},
	)
	options := retryQueueEnqueueOptions{DelayMs: -5}
	if !queue.enqueue("a", "a", options) {
		t.Fatal("入队应成功")
	}
	queue.waitRunning()
	deadline := time.Now().Add(2 * time.Second)
	for exhausted == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if exhausted == 0 {
		t.Fatal("重试耗尽应触发回调")
	}
	// 停止后入队被拒；重复 stop 与 clear 安全。
	queue.stop()
	if queue.enqueue("b", "b", retryQueueEnqueueOptions{}) {
		t.Fatal("停止后入队应拒绝")
	}
	queue.stop()
	queue.clear()
}
