package statsagg

import (
	"errors"
	"fmt"
	"strings"
	"time"

	// tzdata 内嵌 IANA 时区库，保证 time.LoadLocation 在任意宿主（含
	// Windows 开发机与 distroless 容器）上与 Node Intl.DateTimeFormat 解析
	// 同样的时区名。与 gateway/internal/gatewayquota/statwindow.go 相同处理。
	_ "time/tzdata"
)

// dateKey mirrors usage-stats-helpers dateKey：时区内 YYYY-MM-DD。
// 算法与 gatewayquota/statwindow.go dateKey 一致（jobs 侧独立实现）。
func dateKey(t time.Time, location *time.Location) string {
	year, month, day := t.In(location).Date()
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

// hourKey mirrors usage-stats-helpers hourKey：YYYY-MM-DDTHH。
func hourKey(t time.Time, location *time.Location) string {
	year, month, day := t.In(location).Date()
	return fmt.Sprintf("%04d-%02d-%02dT%02d", year, int(month), day, t.In(location).Hour())
}

// minuteKey mirrors usage-stats-helpers minuteKey：YYYY-MM-DDTHH:MM。
func minuteKey(t time.Time, location *time.Location) string {
	year, month, day := t.In(location).Date()
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d", year, int(month), day, t.In(location).Hour(), t.In(location).Minute())
}

// monthKey mirrors usage-stats-helpers monthKey：YYYY-MM。
func monthKey(t time.Time, location *time.Location) string {
	year, month, _ := t.In(location).Date()
	return fmt.Sprintf("%04d-%02d", year, int(month))
}

// weekKey mirrors usage-stats-helpers weekKey：时区日历日的周一起始周键。
// Node 实现取 zonedDateParts 后在宿主本地时区做 startOfWeekMonday；日历
// 周运算与宿主时区无关，这里对齐 statwindow.go weekKey 的 UTC 正午纯日历
// 算法，避免 DST 与宿主时区漂移。
func weekKey(t time.Time, location *time.Location) string {
	year, month, day := t.In(location).Date()
	weekday := time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Weekday()
	daysSinceMonday := (int(weekday) + 6) % 7 // Monday=0 ... Sunday=6
	weekStart := time.Date(year, month, day, 12, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	wYear, wMonth, wDay := weekStart.Date()
	return fmt.Sprintf("%04d-%02d-%02d", wYear, int(wMonth), wDay)
}

// LoadStatsTimezone mirrors normalizeUsageStatsTimezone + time.LoadLocation：
// 校验非空并确认时区存在。
func LoadStatsTimezone(value string) (*time.Location, error) {
	if len(value) == 0 || len(strings.TrimSpace(value)) == 0 {
		return nil, errors.New("统计时区必须是非空字符串")
	}
	timezone := strings.TrimSpace(value)
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("统计时区不存在：%s", timezone)
	}
	return location, nil
}

// UsageStatsTimeKeysFor mirrors usageStatsRecordCreatedAt + usageStatsTimeKeys：
// 解析 created_at 并按时区生成 5 个统计桶键。
func UsageStatsTimeKeysFor(createdAt string, location *time.Location) (UsageStatsTimeKeys, error) {
	parsed, ok := ParseRFC3339Instant(createdAt)
	if !ok {
		return UsageStatsTimeKeys{}, errors.New("使用记录 created_at必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return UsageStatsTimeKeys{
		StatMinute: minuteKey(parsed, location),
		StatHour:   hourKey(parsed, location),
		StatDate:   dateKey(parsed, location),
		StatWeek:   weekKey(parsed, location),
		StatMonth:  monthKey(parsed, location),
	}, nil
}
