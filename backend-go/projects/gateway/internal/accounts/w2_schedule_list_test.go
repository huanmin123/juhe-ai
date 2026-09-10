package accounts

// W2 时间表深函数与列表排序测试：可用性时间表的日期键运算、异常日、日期
// 范围、下一次检查边界（schedule.go）与列表排序解析（list.go 的查询分支）。

import (
	"net/http"
	"testing"
	"time"
)

func TestW2ScheduleDateKeyMath(t *testing.T) {
	t.Run("日期键运算", func(t *testing.T) {
		if got := previousDateKey("2026-03-01"); got != "2026-02-28" {
			t.Fatalf("前一天跨月不一致：%q", got)
		}
		if got := nextDateKey("2026-02-28"); got != "2026-03-01" {
			t.Fatalf("后一天跨月不一致：%q", got)
		}
		// 2026-09-11 是周五（ISO 第 5 天；周日映射为 7）。
		if got := dayOfWeekForDateKey("2026-09-11"); got != 5 {
			t.Fatalf("星期映射不一致：%d", got)
		}
		if got := dayOfWeekForDateKey("2026-09-13"); got != 7 {
			t.Fatalf("周日应映射为 7：%d", got)
		}
		if got := minuteOfDay("09:05"); got != 545 {
			t.Fatalf("分钟换算不一致：%d", got)
		}
		if got := minuteOfDay("bad"); got != 0 {
			t.Fatalf("非法时间应回退 0：%d", got)
		}
		if dateKeyTime("2026-09-11").IsZero() {
			t.Fatal("日期键应可解析")
		}
	})
	t.Run("跨午夜窗口结束键", func(t *testing.T) {
		if got := windowEndDateKey("2026-09-11", "23:00", "01:00"); got != "2026-09-12" {
			t.Fatalf("跨午夜应推到次日：%q", got)
		}
		if got := windowEndDateKey("2026-09-11", "01:00", "23:00"); got != "2026-09-11" {
			t.Fatalf("同日窗口应保持当日：%q", got)
		}
	})
	t.Run("日期范围与异常日", func(t *testing.T) {
		schedule := &AvailabilitySchedule{}
		if !dateInScheduleRange("2026-09-11", schedule) {
			t.Fatal("无范围应全放行")
		}
		schedule.DateRange = &ScheduleDateRange{StartDate: "2026-09-10", EndDate: "2026-09-20"}
		if dateInScheduleRange("2026-09-09", schedule) || dateInScheduleRange("2026-09-21", schedule) {
			t.Fatal("范围外应拒绝")
		}
		if !dateInScheduleRange("2026-09-15", schedule) {
			t.Fatal("范围内应放行")
		}
		schedule.Exceptions = []ScheduleException{{Date: "2026-09-15", Action: "deny"}}
		if findScheduleException(schedule, "2026-09-15") == nil {
			t.Fatal("异常日应命中")
		}
		if findScheduleException(schedule, "2026-09-16") != nil {
			t.Fatal("非异常日不应命中")
		}
	})
	t.Run("时区感知的边界计算", func(t *testing.T) {
		// 上海 UTC+8：窗口 08:00-20:00（本地），对应 UTC 00:00-12:00。
		schedule, err := NormalizeSchedule(map[string]any{
			"enabled": true, "timezone": "Asia/Shanghai", "mode": "allow_windows",
			"windows": []any{map[string]any{"daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)}, "start": "08:00", "end": "20:00"}},
		})
		if err != nil {
			t.Fatalf("时间表归一失败：%v", err)
		}
		now := time.Date(2026, 9, 11, 2, 0, 0, 0, time.UTC) // 上海 10:00，窗口内
		if !m11ScheduleAllowed(mustScheduleJSON(t, schedule), now) {
			t.Fatal("上海窗口内应放行")
		}
		now = time.Date(2026, 9, 11, 16, 30, 0, 0, time.UTC) // 上海 00:30，窗口外
		if m11ScheduleAllowed(mustScheduleJSON(t, schedule), now) {
			t.Fatal("上海 00:30 应在窗口外拒绝")
		}
		next, ok := NextScheduleCheckAt(schedule, now)
		if !ok || next == "" {
			t.Fatal("应给出下一次检查边界")
		}
	})
	t.Run("deny 异常日的边界跳过", func(t *testing.T) {
		schedule, err := NormalizeSchedule(map[string]any{
			"enabled": true, "timezone": "UTC", "mode": "allow_windows",
			"windows": []any{map[string]any{"daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)}, "start": "00:00", "end": "23:59"}},
			"exceptions": []any{map[string]any{
				"date": "2026-09-12", "action": "deny",
			}},
		})
		if err != nil {
			t.Fatalf("含异常日时间表归一失败：%v", err)
		}
		// now 落在 deny 异常日（09-12）内：当日无边界，下一次边界是
		// 09-13 窗口开始（00:00 UTC）。
		now := time.Date(2026, 9, 12, 0, 30, 0, 0, time.UTC)
		next, ok := NextScheduleCheckAt(schedule, now)
		if !ok {
			t.Fatal("应给出边界")
		}
		expected, _ := zonedLocalMinuteToUTC("2026-09-13", 0, "UTC")
		got := parseISOToMillis(t, next)
		if got != expected {
			t.Fatalf("deny 异常日后的边界不一致：%d (期望 %d)", got, expected)
		}
	})
	t.Run("禁用计划不给出检查时间", func(t *testing.T) {
		if _, ok := NextScheduleCheckAt(&AvailabilitySchedule{Enabled: false}, time.Now()); ok {
			t.Fatal("禁用计划应返回 false")
		}
		if _, ok := NextScheduleCheckAt(nil, time.Now()); ok {
			t.Fatal("nil 计划应返回 false")
		}
	})
	t.Run("异常日 allow 覆写", func(t *testing.T) {
		// 周五（2026-09-11）不在常规窗口，但异常日 allow 放开全天。
		schedule, err := NormalizeSchedule(map[string]any{
			"enabled": true, "timezone": "UTC", "mode": "allow_windows",
			"windows": []any{map[string]any{"daysOfWeek": []any{float64(1)}, "start": "00:00", "end": "01:00"}},
			"exceptions": []any{map[string]any{
				"date": "2026-09-11", "action": "allow",
				"windows": []any{map[string]any{"start": "00:00", "end": "23:59"}},
			}},
		})
		if err != nil {
			t.Fatalf("异常 allow 归一失败：%v", err)
		}
		now := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)
		if status, override := ScheduleStatus(schedule, now); !override || status != "active" {
			t.Fatalf("异常 allow 应放行：%q %v", status, override)
		}
	})
}

// mustScheduleJSON 归一结果的 JSON 文本（供 m11ScheduleAllowed 消费）。
func mustScheduleJSON(t *testing.T, schedule *AvailabilitySchedule) string {
	t.Helper()
	raw, ok := ScheduleJSON(schedule)
	if !ok {
		t.Fatal("计划序列化失败")
	}
	return raw
}

// parseISOToMillis 解析 ISO 毫秒文本为 Unix 毫秒。
func parseISOToMillis(t *testing.T, raw string) int64 {
	t.Helper()
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", raw)
	if err != nil {
		t.Fatalf("ISO 解析失败：%v", err)
	}
	return parsed.UnixMilli()
}

func TestW2ListSortColumns(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	for _, name := range []string{"排序甲", "排序乙"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
	}

	// 各排序字段均返回 200（非法字段被丢弃后回退默认排序）。
	for _, sort := range []string{"name:asc", "name:desc", "priority:asc", "concurrencyLimit:desc",
		"providerCode:asc", "status:asc", "type:desc", "createdAt:asc", "updatedAt:desc",
		"lastUsedAt:asc", "healthCheckModel:asc", "bogusField:asc", "name:weird"} {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?sorts="+sort, "")
		if code != http.StatusOK {
			t.Fatalf("sorts=%s 状态码：%d %v", sort, code, payload)
		}
		if len(listItems(t, payload)) != 2 {
			t.Fatalf("sorts=%s 结果数量不一致：%v", sort, payload)
		}
	}
	// 名称升序的确定性顺序。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?sorts=name:asc", "")
	if code != http.StatusOK {
		t.Fatalf("排序请求失败：%d", code)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("列表信封缺失：%v", payload)
	}
	rawItems, ok := data["items"].([]any)
	if !ok || len(rawItems) != 2 {
		t.Fatalf("列表条目不一致：%v", data)
	}
	first := rawItems[0].(map[string]any)["name"].(string)
	second := rawItems[1].(map[string]any)["name"].(string)
	if first != "排序乙" || second != "排序甲" {
		t.Fatalf("升序顺序不一致：%s / %s", first, second)
	}
}
