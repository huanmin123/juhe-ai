// Package statsagg 迁移 Node 后台 12 个统计 job 中归 J-Cab 工作包的 9 个：
// usage 聚合 5 个（usage-stats-aggregation、usage-hot-window-refresh、
// usage-rank-snapshots-refresh、usage-overview-windows-refresh、
// usage-scope-range-windows-refresh）与窗口 4 个（system-metrics-sample、
// system-metrics-trend-windows-refresh、ai-performance-summary-windows-refresh、
// authorization-usage-range-windows-refresh）。
//
// Node 参考（只读对齐源）：
//   - backend/src/modules/background/background-jobs.ts（job 调度层）
//   - backend/src/modules/background/background-stats-writer.ts（stats-writer 分发）
//   - backend/src/storage/usage-stats.repository.ts（usage-stats-aggregation / rank 快照编排）
//   - backend/src/storage/usage-stats-aggregation.ts（scope 扇出 + 行过滤）
//   - backend/src/storage/usage-stats-helpers.ts（dateKey/hourKey/weekKey/monthKey）
//   - backend/src/storage/usage-stats-window-helpers.ts（31 天固定窗口 / 热窗口）
//   - backend/src/storage/usage-stats-window-aggregates.ts（窗口聚合数学）
//   - backend/src/storage/usage-stats-latency-writer.ts（延迟直方图桶）
//   - backend/src/storage/usage-overview-windows.repository.ts（overview 汇总/趋势窗口）
//   - backend/src/storage/usage-range-windows.repository.ts（scope/authorization 范围窗口）
//   - backend/src/storage/usage-stats-snapshot-helpers.ts（排行快照）
//   - backend/src/storage/system-metrics.repository.ts（系统指标采样与趋势窗口）
//
// 时间与时区语义对齐 backend-go/projects/gateway/internal/gatewayquota/statwindow.go
// 的已迁移算法（纯日历运算固定在 UTC 正午计算，规避宿主时区与 DST 漂移），
// jobs 侧独立实现并保持一致。
package statsagg

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rfc3339InstantPattern 对齐 backend/src/shared/rfc3339.ts 的
// rfc3339InstantPattern：offset 必需，裸日期时间不按本地时区猜测。
var rfc3339InstantPattern = regexp.MustCompile(
	`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// ParseRFC3339Instant mirrors parseRfc3339Instant: 解析绝对时间输入，
// 非法输入返回 false 而不是报错。
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
		// JavaScript Date 只保留毫秒精度；Node new Date(text) 在第 10 位
		// 及之后的分数位上按毫秒截断（round-half-even 由 V8 决定，工程输入
		// 统一来自 toISOString 的 3 位），这里对齐为毫秒截断语义：
		// 超出 3 位的部分仅用于解析兼容，不参与精度。
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

// CanonicalizeRFC3339Instant mirrors canonicalizeRfc3339Instant：规范化为
// toISOString 形式（毫秒精度 + Z）。
func CanonicalizeRFC3339Instant(value string) (string, bool) {
	parsed, ok := ParseRFC3339Instant(value)
	if !ok {
		return "", false
	}
	return FormatRFC3339Millis(parsed), true
}

// RequiredRFC3339Instant mirrors requiredRfc3339Instant：非法输入抛错。
func RequiredRFC3339Instant(value, label string) (string, error) {
	normalized, ok := CanonicalizeRFC3339Instant(value)
	if !ok {
		return "", fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return normalized, nil
}

// RFC3339Milliseconds mirrors rfc3339InstantMilliseconds.
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

// CompareUsageStatsTimestamp mirrors usage-stats.repository.ts
// compareUsageStatsTimestamp：按毫秒比较，非法输入报错。
func CompareUsageStatsTimestamp(left, right string) (int, error) {
	leftMilliseconds, okLeft := RFC3339Milliseconds(left)
	rightMilliseconds, okRight := RFC3339Milliseconds(right)
	if !okLeft || !okRight {
		return 0, errors.New("用量统计聚合时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	switch {
	case leftMilliseconds == rightMilliseconds:
		return 0, nil
	case leftMilliseconds > rightMilliseconds:
		return 1, nil
	default:
		return -1, nil
	}
}

// MaxOptionalISO mirrors maxOptionalIso：两者都缺省时返回空；否则取较大者。
func MaxOptionalISO(left, right string) (string, error) {
	if left == "" {
		if right == "" {
			return "", nil
		}
		return RequiredRFC3339Instant(right, "用量统计聚合时间")
	}
	if right == "" {
		return RequiredRFC3339Instant(left, "用量统计聚合时间")
	}
	comparison, err := CompareUsageStatsTimestamp(left, right)
	if err != nil {
		return "", err
	}
	if comparison >= 0 {
		return RequiredRFC3339Instant(left, "用量统计聚合时间")
	}
	return RequiredRFC3339Instant(right, "用量统计聚合时间")
}
