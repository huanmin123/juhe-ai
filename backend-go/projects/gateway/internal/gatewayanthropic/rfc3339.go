package gatewayanthropic

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// YYYYMMDDPattern 校验 YYYY-MM-DD 日期。
var YYYYMMDDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// rfc3339InstantPattern 对齐 shared/rfc3339.ts：秒级 + 可选小数 + 必须带
// Z 或数值 offset。
var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

var rfc3339GoLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05Z07:00",
}

// ParseRFC3339Instant 对齐 parseRfc3339Instant：offset 必需，日期分量按
// 真实日历校验，不按本地时区猜测。
func ParseRFC3339Instant(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	match := rfc3339InstantPattern.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}, false
	}
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	hour, _ := strconv.Atoi(match[4])
	minute, _ := strconv.Atoi(match[5])
	second, _ := strconv.Atoi(match[6])
	offset := match[8]
	if month < 1 || month > 12 || day < 1 || day > daysInMonth(match[1], month) ||
		hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	if offset != "Z" {
		offsetHour, _ := strconv.Atoi(offset[1:3])
		offsetMinute, _ := strconv.Atoi(offset[4:6])
		if offsetHour > 23 || offsetMinute > 59 {
			return time.Time{}, false
		}
	}
	for _, layout := range rfc3339GoLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// CanonicalizeRFC3339Instant 对齐 canonicalizeRfc3339Instant：规范化为 UTC
// ISO（毫秒精度 + Z 后缀）。
func CanonicalizeRFC3339Instant(value string) (string, bool) {
	parsed, ok := ParseRFC3339Instant(value)
	if !ok {
		return "", false
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z"), true
}

func daysInMonth(yearText string, month int) int {
	year, _ := strconv.Atoi(yearText)
	// 对齐 new Date(Date.UTC(year, month, 0)).getUTCDate()：month 从 1 起算，
	// 取下个月第 0 天即当月最后一天。
	lastDay := time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC)
	return lastDay.Day()
}
