package instantmigration

import (
	"regexp"
	"testing"
)

func TestRFC3339TextPatternRanges(t *testing.T) {
	pattern := regexp.MustCompile(rfc3339TextPattern)
	valid := []string{
		"2026-01-01T00:00:00Z",
		"2026-01-01T00:00:00.123Z",
		"2026-01-01T00:00:00.123456789+08:00",
		"2026-12-31T23:59:59-05:30",
		"2026-01-01 00:00:00+08:00",
		"2026-11-11 22:01:43.35+08",
	}
	for _, value := range valid {
		if !pattern.MatchString(value) {
			t.Fatalf("合法值未通过形状校验: %q", value)
		}
	}
	invalid := []string{
		"",
		"2026-13-01T00:00:00Z",
		"2026-00-01T00:00:00Z",
		"2026-01-32T00:00:00Z",
		"2026-01-01T24:00:00Z",
		"2026-01-01T00:60:00Z",
		"2026-01-01T00:00:60Z",
		"2026-01-01T00:00:00",
		" 2026-01-01T00:00:00Z",
		"2026-01-01T00:00:00z",
	}
	for _, value := range invalid {
		if pattern.MatchString(value) {
			t.Fatalf("非法值通过了形状校验: %q", value)
		}
	}
	// 02-30 这类月日联动由 pg_input_is_valid 在数据库侧拦截；形状正则只
	// 负责挡明显越界的字段，避免把 PostgreSQL 的完整日历规则复制到 Go。
	if !pattern.MatchString("2026-02-30T00:00:00Z") {
		t.Fatal("形状正则不应承担月日联动校验")
	}
}
