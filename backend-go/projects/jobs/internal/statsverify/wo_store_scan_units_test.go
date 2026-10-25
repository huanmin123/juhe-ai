package statsverify

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// ---- OpenStore 配置校验与 Postgres 懒打开 ----

func TestSQLTimeVariants(t *testing.T) {
	expected := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, err := sqlTime(expected); err != nil || !got.Equal(expected) {
		t.Fatalf("time.Time 应原样 UTC 化: %v %v", got, err)
	}
	if got, err := sqlTime(sql.NullTime{Time: expected, Valid: true}); err != nil || !got.Equal(expected) {
		t.Fatalf("NullTime 有效值应转换: %v %v", got, err)
	}
	if _, err := sqlTime(sql.NullTime{}); err == nil {
		t.Fatalf("NullTime 无效值应报错")
	}
	if got, err := sqlTime("2026-01-02T03:04:05Z"); err != nil || !got.Equal(expected) {
		t.Fatalf("RFC3339 文本应可解析: %v %v", got, err)
	}
	if _, err := sqlTime(""); err == nil {
		t.Fatalf("空文本应报错")
	}
	if _, err := sqlTime("not-a-time"); err == nil {
		t.Fatalf("非 RFC3339 文本应报错")
	}
	if got, err := sqlTime([]byte("2026-01-02T03:04:05Z")); err != nil || !got.Equal(expected) {
		t.Fatalf("字节数本文本应可解析: %v %v", got, err)
	}
	if _, err := sqlTime([]byte("bad")); err == nil {
		t.Fatalf("字节数组非法文本应报错")
	}
	if _, err := sqlTime(42); err == nil {
		t.Fatalf("整型时间单元格应报错")
	}
}

func TestSQLTextVariants(t *testing.T) {
	if got, err := sqlText("a"); err != nil || got != "a" {
		t.Fatalf("字符串应原样: %v %v", got, err)
	}
	if got, err := sqlText([]byte("b")); err != nil || got != "b" {
		t.Fatalf("字节应转字符串: %v %v", got, err)
	}
	moment := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, err := sqlText(moment); err != nil || got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("时间应按 Node nowIso 格式化: %v %v", got, err)
	}
	if got, err := sqlText(sql.NullString{String: "c", Valid: true}); err != nil || got != "c" {
		t.Fatalf("NullString 有效值应转换: %v %v", got, err)
	}
	if got, err := sqlText(sql.NullString{}); err != nil || got != "" {
		t.Fatalf("NullString 无效值应为空串: %v %v", got, err)
	}
	if got, err := sqlText(nil); err != nil || got != "" {
		t.Fatalf("nil 应为空串: %v %v", got, err)
	}
	if _, err := sqlText(3.14); err == nil {
		t.Fatalf("浮点文本单元格应报错")
	}
}

func TestSQLIntVariants(t *testing.T) {
	cases := []struct {
		value any
		want  int64
	}{
		{int64(7), 7}, {int32(8), 8}, {int(9), 9}, {float64(10.9), 10}, {float32(11), 11},
		{"12", 12}, {[]byte("13"), 13}, {sql.NullInt64{Int64: 14, Valid: true}, 14}, {sql.NullInt64{}, 0}, {nil, 0},
	}
	for _, tc := range cases {
		got, err := sqlInt(tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("sqlInt(%#v)=%d err=%v，期望 %d", tc.value, got, err, tc.want)
		}
	}
	if _, err := sqlInt("abc"); err == nil {
		t.Fatalf("非数字文本应报错")
	}
	if _, err := sqlInt(true); err == nil {
		t.Fatalf("布尔整数单元格应报错")
	}
}

func TestSQLFloatVariants(t *testing.T) {
	cases := []struct {
		value any
		want  float64
	}{
		{float64(1.5), 1.5}, {float32(2.5), 2.5}, {int64(3), 3}, {int32(4), 4},
		{"5.5", 5.5}, {nil, 0},
	}
	for _, tc := range cases {
		got, err := sqlFloat(tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("sqlFloat(%#v)=%v err=%v，期望 %v", tc.value, got, err, tc.want)
		}
	}
	if _, err := sqlFloat("nan-text"); err == nil {
		t.Fatalf("非数字文本应报错")
	}
}

// ---- 时钟与日期纯函数 ----

func TestFixedClockAdvanceAndNilSafety(t *testing.T) {
	var nilClock *FixedClock
	if !nilClock.Now().IsZero() {
		t.Fatalf("nil 时钟应返回零值")
	}
	nilClock.Sleep(time.Minute) // 不应 panic
	clock := NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if got := clock.Now(); !got.Equal(clock.Current) {
		t.Fatalf("Now 应返回当前值")
	}
	clock.Sleep(time.Second)
	if got := clock.Current.Sub(time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)); got != 0 {
		t.Fatalf("Sleep 应推进时钟，实际偏移: %v", got)
	}
}

func TestSystemClockNowIsCurrent(t *testing.T) {
	clock := SystemClock{}
	if delta := clock.Now().Sub(time.Now()).Abs(); delta > time.Minute {
		t.Fatalf("SystemClock 应接近当前时间，偏移: %v", delta)
	}
}

func TestAddCalendarDaysInBoundaries(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		value string
		days  int
		want  string
	}{
		{"2026-01-31", 1, "2026-02-01"},
		{"2026-03-01", -1, "2026-02-28"},
		{"2024-02-28", 1, "2024-02-29"}, // 闰年
		{"2023-02-28", 1, "2023-03-01"}, // 平年
		{"2026-12-31", 1, "2027-01-01"}, // 跨年
	}
	for _, tc := range cases {
		if got := AddCalendarDaysIn(tc.value, tc.days, loc); got != tc.want {
			t.Fatalf("AddCalendarDaysIn(%s,%d)=%s，期望 %s", tc.value, tc.days, got, tc.want)
		}
	}
	// 非日期键原样返回。
	if got := AddCalendarDaysIn("bad-key", 3, loc); got != "bad-key" {
		t.Fatalf("非法日期键应原样返回，实际: %s", got)
	}
}

func TestParseDateKeyPartsStrictness(t *testing.T) {
	if parts, ok := parseDateKeyParts("2026-02-28"); !ok || parts.year != 2026 || parts.month != 2 || parts.day != 28 {
		t.Fatalf("合法日期应解析成功: %#v %v", parts, ok)
	}
	for _, bad := range []string{"2026-2-28", "2026/02/28", "20260228", "2026-13-01", "2026-02-30", "2026-00-10", ""} {
		if _, ok := parseDateKeyParts(bad); ok {
			t.Fatalf("非法日期键 %q 不应解析成功", bad)
		}
	}
}

func TestMinIntTable(t *testing.T) {
	cases := []struct{ left, right, want int }{
		{1, 2, 1}, {2, 1, 1}, {3, 3, 3}, {-1, 0, -1},
	}
	for _, tc := range cases {
		if got := minInt(tc.left, tc.right); got != tc.want {
			t.Fatalf("minInt(%d,%d)=%d，期望 %d", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestSelectKeyColumnsAfterAndChunkStrings(t *testing.T) {
	if got := selectKeyColumnsAfter("account_id"); got != "account_id, " {
		t.Fatalf("有账号列时应返回前缀: %q", got)
	}
	if got := selectKeyColumnsAfter(""); got != "" {
		t.Fatalf("无账号列时应返回空串: %q", got)
	}
	chunks := chunkStrings([]string{"a", "b", "c", "d", "e"}, 2)
	if len(chunks) != 3 || strings.Join(chunks[0], "") != "ab" || strings.Join(chunks[2], "") != "e" {
		t.Fatalf("分块结果不符: %#v", chunks)
	}
	if chunks := chunkStrings([]string{"a"}, 0); len(chunks) != 1 || chunks[0][0] != "a" {
		t.Fatalf("size<1 应钳制为 1: %#v", chunks)
	}
}

// ---- 时区装载与脏标记 ----

