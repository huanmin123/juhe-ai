// Package timefmt 提供 RFC3339 毫秒精度时间的统一解析/格式化。
// 语义对齐 Node backend/src/shared/rfc3339.ts：offset 必需，裸日期时间
// 不按本地时区猜测；小数超出 3 位按毫秒截断（JavaScript Date 精度对齐）。
// 统一锚点为 jobs/internal/statsagg/rfc3339.go 的已迁移实现（评估文档
// R1 取证确认 4 份完整实现行为等价，本包为 shared 收敛落点）。
package timefmt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rfc3339InstantPattern 对齐 Node rfc3339InstantPattern。
var rfc3339InstantPattern = regexp.MustCompile(
	`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// ParseRFC3339Instant 解析绝对时间输入，非法输入返回 false 而不是报错。
func ParseRFC3339Instant(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	match := rfc3339InstantPattern.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	hour, _ := strconv.Atoi(match[4])
	minute, _ := strconv.Atoi(match[5])
	second, _ := strconv.Atoi(match[6])
	fraction := match[7]
	offset := match[8]
	if month < 1 || month > 12 {
		return time.Time{}, false
	}
	if day < 1 || day > daysInMonth(year, month) {
		return time.Time{}, false
	}
	if hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	nanoseconds := 0
	if fraction != "" {
		// 小数超出 3 位按毫秒截断（JavaScript Date 精度对齐）；仅用于
		// 解析兼容，不参与精度。
		trimmed := fraction
		if len(trimmed) > 3 {
			trimmed = trimmed[:3]
		}
		for len(trimmed) < 3 {
			trimmed += "0"
		}
		ms, err := strconv.Atoi(trimmed)
		if err != nil {
			return time.Time{}, false
		}
		nanoseconds = ms * 1_000_000
	}
	location := time.UTC
	if offset != "Z" {
		offsetHour, err1 := strconv.Atoi(offset[1:3])
		offsetMinute, err2 := strconv.Atoi(offset[4:6])
		if err1 != nil || err2 != nil || offsetHour > 23 || offsetMinute > 59 {
			return time.Time{}, false
		}
		location = time.FixedZone("", sign(offset)*((offsetHour*60+offsetMinute)*60))
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, nanoseconds, location), true
}

func sign(offset string) int {
	if offset[0] == '-' {
		return -1
	}
	return 1
}

func daysInMonth(year, month int) int {
	return time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC).Day()
}

// CanonicalizeRFC3339Instant 规范化为 toISOString 形式（毫秒精度 + Z）。
func CanonicalizeRFC3339Instant(value string) (string, bool) {
	parsed, ok := ParseRFC3339Instant(value)
	if !ok {
		return "", false
	}
	return FormatRFC3339Millis(parsed), true
}

// RequiredRFC3339Instant 非法输入抛错；错误文案由 label 参数定位
// （调用方保留自己的文案前缀语义）。
func RequiredRFC3339Instant(value, label string) (string, error) {
	normalized, ok := CanonicalizeRFC3339Instant(value)
	if !ok {
		return "", fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return normalized, nil
}

// RFC3339Milliseconds 返回绝对时间的 Unix 毫秒；非法输入返回 false。
func RFC3339Milliseconds(value string) (int64, bool) {
	parsed, ok := ParseRFC3339Instant(value)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// FormatRFC3339Millis 以 Node Date.prototype.toISOString 形式输出
// （YYYY-MM-DDTHH:MM:SS.sssZ，毫秒精度）。
func FormatRFC3339Millis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
