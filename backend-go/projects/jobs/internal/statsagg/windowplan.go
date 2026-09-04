package statsagg

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// 窗口计划常量对齐 usage-stats-window-helpers.ts。
const (
	HourMs               = 60 * 60 * 1000
	DayMs                = 24 * HourMs
	FixedRangeWindowDays = 31
)

// StatsRange mirrors domain/types.ts AccountUsageStatsRange。
type StatsRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

// RangeWindowKey mirrors rangeWindowKey。
func RangeWindowKey(rangeValue StatsRange) string {
	return rangeValue.StartDate + ":" + rangeValue.EndDate
}

var dateKeyPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// parseDateKeyStrict mirrors usage-stats-window-helpers parseDateKeyStrict：
// 在固定 UTC 日历上解析并验证日期有效（规避宿主时区）。
func parseDateKeyStrict(value string) (time.Time, bool) {
	match := dateKeyPattern.FindStringSubmatch(value)
	if match == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	candidate := time.Date(year, time.Month(month), day, 12, 0, 0, 0, time.UTC)
	if candidate.Year() != year || int(candidate.Month()) != month || candidate.Day() != day {
		return time.Time{}, false
	}
	return candidate, true
}

// addCalendarDays 在固定 UTC 日历上加减天数（对齐 Node addDays 的纯日历语义）。
func addCalendarDays(value time.Time, days int) time.Time {
	return value.AddDate(0, 0, days)
}

func formatCalendarDate(value time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", value.Year(), int(value.Month()), value.Day())
}

// startOfWeekMonday mirrors startOfWeekMonday：周一起始。
func startOfWeekMonday(value time.Time) time.Time {
	weekday := int(value.Weekday())
	offset := weekday - 1
	if weekday == 0 {
		offset = 6
	}
	return addCalendarDays(value, -offset)
}

// FixedUsageStatsDateKeys mirrors fixedUsageStatsDateKeys：以 todayKey 结尾的
// 连续 31 个日期键（含当天）。
func FixedUsageStatsDateKeys(todayKey string) []string {
	endDate, ok := parseDateKeyStrict(todayKey)
	if !ok {
		return []string{}
	}
	earliestDate := addCalendarDays(endDate, -(FixedRangeWindowDays - 1))
	dates := make([]string, 0, FixedRangeWindowDays)
	for index := 0; index < FixedRangeWindowDays; index++ {
		dates = append(dates, formatCalendarDate(addCalendarDays(earliestDate, index)))
	}
	return dates
}

// FixedUsageStatsRanges mirrors fixedUsageStatsRanges：31 天窗口的全部
// 连续子区间（start<=end），顺序与 Node 一致（外层 start 升序、内层 end 升序）。
func FixedUsageStatsRanges(todayKey string) []StatsRange {
	dates := FixedUsageStatsDateKeys(todayKey)
	ranges := make([]StatsRange, 0, len(dates)*(len(dates)+1)/2)
	for startIndex := 0; startIndex < len(dates); startIndex++ {
		for endIndex := startIndex; endIndex < len(dates); endIndex++ {
			ranges = append(ranges, StatsRange{
				StartDate: dates[startIndex],
				EndDate:   dates[endIndex],
				Days:      endIndex - startIndex + 1,
				MaxDays:   FixedRangeWindowDays,
			})
		}
	}
	return ranges
}

// HotUsageStatsRanges mirrors hotUsageStatsRanges：今日、昨日、近 7 日、
// 31 天固定窗、本月（截到 31 天窗）五个热窗口（去重保序）。
func HotUsageStatsRanges(todayKey string) []StatsRange {
	endDate, ok := parseDateKeyStrict(todayKey)
	if !ok {
		return []StatsRange{}
	}
	fixedStartDate := addCalendarDays(endDate, -(FixedRangeWindowDays - 1))
	monthStartDate := time.Date(endDate.Year(), endDate.Month(), 1, 12, 0, 0, 0, time.UTC)
	candidates := []struct{ start, end time.Time }{
		{endDate, endDate},
		{addCalendarDays(endDate, -1), addCalendarDays(endDate, -1)},
		{addCalendarDays(endDate, -6), endDate},
		{fixedStartDate, endDate},
		{maxCalendarDate(monthStartDate, fixedStartDate), endDate},
	}
	unique := map[string]struct{}{}
	ranges := make([]StatsRange, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.end.Before(fixedStartDate) || candidate.start.After(endDate) {
			continue
		}
		startDate := candidate.start
		if startDate.Before(fixedStartDate) {
			startDate = fixedStartDate
		}
		normalizedStart := formatCalendarDate(startDate)
		normalizedEnd := formatCalendarDate(candidate.end)
		// days 对齐 Math.round((end-start)/DAY_MS)+1：UTC 正午日历差恒为整天。
		days := int(candidate.end.Sub(startDate).Hours()/24+0.5) + 1
		key := normalizedStart + ":" + normalizedEnd
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		ranges = append(ranges, StatsRange{
			StartDate: normalizedStart,
			EndDate:   normalizedEnd,
			Days:      int(days),
			MaxDays:   FixedRangeWindowDays,
		})
	}
	return ranges
}

func maxCalendarDate(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

// TrendBucketHours mirrors trendBucketHours。
func TrendBucketHours(days int) int {
	if days <= 1 {
		return 1
	}
	if days <= 3 {
		return 6
	}
	return 24
}

// TrendBucketKey mirrors trendBucketKey：把 stat_hour 归入趋势桶。
func TrendBucketKey(statHour string, bucketHours int) string {
	if bucketHours >= 24 {
		if len(statHour) >= 10 {
			return statHour[:10]
		}
		return statHour
	}
	if bucketHours <= 1 {
		return statHour
	}
	if len(statHour) < 13 {
		return statHour
	}
	hour, err := strconv.Atoi(statHour[11:13])
	if err != nil {
		return statHour
	}
	bucketHour := hour / bucketHours * bucketHours
	return statHour[:11] + fmt.Sprintf("%02d", bucketHour)
}

// CompareText mirrors compareText。
func CompareText(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

// NextCalendarDateKey mirrors usage-stats-helpers nextCalendarDateKey。
func NextCalendarDateKey(value string) string {
	parsed, ok := parseDateKeyStrict(value)
	if !ok {
		return value
	}
	return formatCalendarDate(addCalendarDays(parsed, 1))
}

// DateKeysInRange mirrors dateKeysInRange（上限 31 天）。
func DateKeysInRange(startDate, endDate string) []string {
	start, okStart := parseDateKeyStrict(startDate)
	end, okEnd := parseDateKeyStrict(endDate)
	if !okStart || !okEnd || start.After(end) {
		return []string{}
	}
	days := int(end.Sub(start).Hours()/24) + 1
	if days > FixedRangeWindowDays {
		days = FixedRangeWindowDays
	}
	keys := make([]string, 0, days)
	for index := 0; index < days; index++ {
		keys = append(keys, formatCalendarDate(addCalendarDays(start, index)))
	}
	return keys
}

// DailyWindowRow mirrors usage-stats-window-aggregates.ts
// UsageStatsDailyWindowRow（Go 侧读取层使用 float64/指针承载数值）。
type DailyWindowRow struct {
	StatDate           string
	RequestCount       float64
	SuccessCount       float64
	ErrorCount         float64
	InputTokens        float64
	OutputTokens       float64
	CacheReadTokens    float64
	CacheReadCostUsd   float64
	CacheWriteTokens   float64
	CacheWrite1hTokens float64
	CacheWriteCostUsd  float64
	ThinkingTokens     float64
	InputImageTokens   float64
	OutputImageTokens  float64
	TotalCostUsd       float64
	DurationMsSum      float64
	DurationMsCount    float64
	DurationMsMax      float64
	FirstTokenMsSum    float64
	FirstTokenMsCount  float64
	FirstTokenMsMax    float64
	LastUsedAt         string // '' 表示 NULL
}

// HourlyWindowRow mirrors UsageOverviewHourlyWindowRow。
type HourlyWindowRow struct {
	StatHour           string
	RequestCount       float64
	ErrorCount         float64
	InputTokens        float64
	OutputTokens       float64
	CacheReadTokens    float64
	CacheReadCostUsd   float64
	CacheWriteTokens   float64
	CacheWrite1hTokens float64
	CacheWriteCostUsd  float64
	ThinkingTokens     float64
	InputImageTokens   float64
	OutputImageTokens  float64
	TotalCostUsd       float64
	DurationMsSum      float64
	DurationMsCount    float64
}

// UsageWindowAggregate mirrors usage-stats-window-aggregates.ts
// UsageWindowAggregate。
type UsageWindowAggregate struct {
	RequestCount       float64
	SuccessCount       float64
	ErrorCount         float64
	InputTokens        float64
	OutputTokens       float64
	CacheReadTokens    float64
	CacheReadCostUsd   float64
	CacheWriteTokens   float64
	CacheWrite1hTokens float64
	CacheWriteCostUsd  float64
	ThinkingTokens     float64
	InputImageTokens   float64
	OutputImageTokens  float64
	TotalCostUsd       float64
	DurationMsSum      float64
	DurationMsCount    float64
	DurationMsMax      float64
	FirstTokenMsSum    float64
	FirstTokenMsCount  float64
	FirstTokenMsMax    float64
	LastUsedAt         string
}

// RowsByStatDate mirrors rowsByStatDate。
func RowsByStatDate[T any](rows []T, statDate func(T) string) map[string][]T {
	result := map[string][]T{}
	for _, row := range rows {
		key := statDate(row)
		result[key] = append(result[key], row)
	}
	return result
}

// RowsByStatHourDate mirrors rowsByStatHourDate：stat_hour 前 10 位为日期。
func RowsByStatHourDate[T any](rows []T, statHour func(T) string) map[string][]T {
	result := map[string][]T{}
	for _, row := range rows {
		hour := statHour(row)
		if len(hour) < 10 {
			continue
		}
		statDate := hour[:10]
		result[statDate] = append(result[statDate], row)
	}
	return result
}

// RowsForDateRange mirrors rowsForDateRange。
func RowsForDateRange[T any](rowsByDate map[string][]T, startDate, endDate string) []T {
	var rows []T
	for _, statDateKey := range DateKeysInRange(startDate, endDate) {
		rows = append(rows, rowsByDate[statDateKey]...)
	}
	return rows
}

// AggregateUsageRowsForRange mirrors aggregateUsageRowsForRange。
func AggregateUsageRowsForRange(rowsByDate map[string][]DailyWindowRow, rangeValue StatsRange) UsageWindowAggregate {
	aggregate := UsageWindowAggregate{}
	for _, row := range RowsForDateRange(rowsByDate, rangeValue.StartDate, rangeValue.EndDate) {
		addUsageWindowAggregate(&aggregate, dailyWindowAggregable(row))
	}
	return aggregate
}

type aggregableWindowRow struct {
	RequestCount       float64
	SuccessCount       float64
	ErrorCount         float64
	InputTokens        float64
	OutputTokens       float64
	CacheReadTokens    float64
	CacheReadCostUsd   float64
	CacheWriteTokens   float64
	CacheWrite1hTokens float64
	CacheWriteCostUsd  float64
	ThinkingTokens     float64
	InputImageTokens   float64
	OutputImageTokens  float64
	TotalCostUsd       float64
	DurationMsSum      float64
	DurationMsCount    float64
	DurationMsMax      float64
	FirstTokenMsSum    float64
	FirstTokenMsCount  float64
	FirstTokenMsMax    float64
	LastUsedAt         string
}

func dailyWindowAggregable(row DailyWindowRow) aggregableWindowRow {
	return aggregableWindowRow{
		RequestCount: row.RequestCount, SuccessCount: row.SuccessCount, ErrorCount: row.ErrorCount,
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		CacheReadTokens: row.CacheReadTokens, CacheReadCostUsd: row.CacheReadCostUsd,
		CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens,
		CacheWriteCostUsd: row.CacheWriteCostUsd, ThinkingTokens: row.ThinkingTokens,
		InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens,
		TotalCostUsd:  row.TotalCostUsd,
		DurationMsSum: row.DurationMsSum, DurationMsCount: row.DurationMsCount, DurationMsMax: row.DurationMsMax,
		FirstTokenMsSum: row.FirstTokenMsSum, FirstTokenMsCount: row.FirstTokenMsCount, FirstTokenMsMax: row.FirstTokenMsMax,
		LastUsedAt: row.LastUsedAt,
	}
}

// addUsageWindowAggregate mirrors addUsageWindowAggregate（NaN/undefined 按 0）。
func addUsageWindowAggregate(target *UsageWindowAggregate, row aggregableWindowRow) {
	target.RequestCount += row.RequestCount
	target.SuccessCount += row.SuccessCount
	target.ErrorCount += row.ErrorCount
	target.InputTokens += row.InputTokens
	target.OutputTokens += row.OutputTokens
	target.CacheReadTokens += row.CacheReadTokens
	target.CacheReadCostUsd += row.CacheReadCostUsd
	target.CacheWriteTokens += row.CacheWriteTokens
	target.CacheWrite1hTokens += row.CacheWrite1hTokens
	target.CacheWriteCostUsd += row.CacheWriteCostUsd
	target.ThinkingTokens += row.ThinkingTokens
	target.InputImageTokens += row.InputImageTokens
	target.OutputImageTokens += row.OutputImageTokens
	target.TotalCostUsd += row.TotalCostUsd
	target.DurationMsSum += row.DurationMsSum
	target.DurationMsCount += row.DurationMsCount
	if row.DurationMsMax > target.DurationMsMax {
		target.DurationMsMax = row.DurationMsMax
	}
	target.FirstTokenMsSum += row.FirstTokenMsSum
	target.FirstTokenMsCount += row.FirstTokenMsCount
	if row.FirstTokenMsMax > target.FirstTokenMsMax {
		target.FirstTokenMsMax = row.FirstTokenMsMax
	}
	// latestText：right 为空保持 left；right > left 时取 right
	if row.LastUsedAt != "" {
		if target.LastUsedAt == "" || row.LastUsedAt > target.LastUsedAt {
			target.LastUsedAt = row.LastUsedAt
		}
	}
}

// AggregateUsageTrendBuckets mirrors aggregateUsageTrendBuckets。Node 侧先
// rowsByStatHourDate(rows) 再按 range 取行，这里等价为直接对行流做日期过滤。
func AggregateUsageTrendBuckets(rows []HourlyWindowRow, rangeValue StatsRange) map[string]UsageWindowAggregate {
	buckets := map[string]UsageWindowAggregate{}
	bucketHours := TrendBucketHours(rangeValue.Days)
	rowsByDate := RowsByStatHourDate(rows, func(r HourlyWindowRow) string { return r.StatHour })
	for _, row := range RowsForDateRange(rowsByDate, rangeValue.StartDate, rangeValue.EndDate) {
		bucketKey := TrendBucketKey(row.StatHour, bucketHours)
		bucket := buckets[bucketKey]
		bucket.RequestCount += row.RequestCount
		bucket.ErrorCount += row.ErrorCount
		bucket.InputTokens += row.InputTokens
		bucket.OutputTokens += row.OutputTokens
		bucket.CacheReadTokens += row.CacheReadTokens
		bucket.CacheReadCostUsd += row.CacheReadCostUsd
		bucket.CacheWriteTokens += row.CacheWriteTokens
		bucket.CacheWrite1hTokens += row.CacheWrite1hTokens
		bucket.CacheWriteCostUsd += row.CacheWriteCostUsd
		bucket.ThinkingTokens += row.ThinkingTokens
		bucket.InputImageTokens += row.InputImageTokens
		bucket.OutputImageTokens += row.OutputImageTokens
		bucket.TotalCostUsd += row.TotalCostUsd
		bucket.DurationMsSum += row.DurationMsSum
		bucket.DurationMsCount += row.DurationMsCount
		buckets[bucketKey] = bucket
	}
	return buckets
}

// SortedMapKeys 返回按键字典序排序的键（对齐 sortedMapEntries 的输出顺序）。
func SortedMapKeys[V any](buckets map[string]V) []string {
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
