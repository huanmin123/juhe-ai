package accountquality

import (
	"fmt"
	"time"
)

// MinuteKey 是 Node usage-stats-helpers minuteKey 的移植：
// `YYYY-MM-DDTHH:mm`（指定 IANA 时区的本地时间）。usageStatsTimezone 来自
// 系统设置 `usageStatsTimezone`（由宿主注入），Go 侧解析为 *time.Location。
func MinuteKey(t time.Time, loc *time.Location) string {
	local := t.In(loc)
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d", local.Year(), int(local.Month()), local.Day(), local.Hour(), local.Minute())
}

// ResolveTimezone 将 IANA 名称解析为 *time.Location；空名称回落 UTC。
// 与 Node normalizeUsageStatsTimezone 的失败语义一致：无效名称返回错误。
func ResolveTimezone(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效：%s", name)
	}
	return loc, nil
}
