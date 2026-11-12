// Package statsagg 迁移 Node 后台 12 个统计 job 中归 J-Cab 工作包的 9 个：
// usage 聚合 5 个（usage-stats-aggregation、usage-hot-window-refresh、
// usage-rank-snapshots-refresh、usage-overview-windows-refresh、
// usage-scope-range-windows-refresh）与窗口 4 个（system-metrics-sample、
// system-metrics-trend-windows-refresh、ai-performance-summary-windows-refresh、
// authorization-usage-range-windows-refresh；其中 authorization-usage-range-
// windows-refresh 已随授权消耗明细改为日摘要表现场聚合而停用，不再装配调度）。
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
// jobs 侧独立实现并保持一致。RFC3339 解析/格式化自 2026-11-11 起委托
// shared/platform/timefmt，移除本包第四份拷贝。
package statsagg

import (
	"errors"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/timefmt"
)

// ParseRFC3339Instant mirrors parseRfc3339Instant: 解析绝对时间输入，
// 非法输入返回 false 而不是报错。
func ParseRFC3339Instant(value string) (time.Time, bool) {
	return timefmt.ParseRFC3339Instant(value)
}

// CanonicalizeRFC3339Instant mirrors canonicalizeRfc3339Instant：规范化为
// toISOString 形式（毫秒精度 + Z）。
func CanonicalizeRFC3339Instant(value string) (string, bool) {
	return timefmt.CanonicalizeRFC3339Instant(value)
}

// RequiredRFC3339Instant mirrors requiredRfc3339Instant：非法输入抛错。
func RequiredRFC3339Instant(value, label string) (string, error) {
	return timefmt.RequiredRFC3339Instant(value, label)
}

// RFC3339Milliseconds mirrors rfc3339InstantMilliseconds.
func RFC3339Milliseconds(value string) (int64, bool) {
	return timefmt.RFC3339Milliseconds(value)
}

// FormatRFC3339Millis 以 Node Date.prototype.toISOString 形式输出
// （YYYY-MM-DDTHH:MM:SS.sssZ，毫秒精度）。
func FormatRFC3339Millis(t time.Time) string {
	return timefmt.FormatRFC3339Millis(t)
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
