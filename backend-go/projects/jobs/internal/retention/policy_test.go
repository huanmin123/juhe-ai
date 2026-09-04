package retention

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func validPolicySettings() map[string]any {
	return map[string]any{
		SettingPublicApiLogRetentionDays:        30,
		SettingUsageRecordRetentionDays:         14,
		SettingUsageStatsMinuteRetentionHours:   48,
		SettingUsageStatsHourlyRetentionDays:    60,
		SettingUsageStatsDailyRetentionDays:     90,
		SettingUsageStatsWeeklyRetentionWeeks:   52,
		SettingUsageStatsMonthlyRetentionMonths: 24,
		SettingUsageRankSnapshotRetentionDays:   180,
		SettingSystemMetricsRetentionDays:       3,
		SettingSystemMetricsHourlyRetentionDays: 15,
	}
}

func TestSettingNumber(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		key      string
		min      int64
		max      int64
		want     int64
		wantErr  string
	}{
		{name: "missing key fails closed", settings: map[string]any{}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须是整数"},
		{name: "string value fails closed", settings: map[string]any{"k": "5"}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须是整数"},
		{name: "boolean value fails closed", settings: map[string]any{"k": true}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须是整数"},
		{name: "nil value fails closed", settings: map[string]any{"k": nil}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须是整数"},
		{name: "non-integer float fails closed", settings: map[string]any{"k": 5.5}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须是整数"},
		{name: "integer float accepted", settings: map[string]any{"k": 5.0}, key: "k", min: 1, max: 10, want: 5},
		{name: "int accepted", settings: map[string]any{"k": 5}, key: "k", min: 1, max: 10, want: 5},
		{name: "json number accepted", settings: map[string]any{"k": json.Number("7")}, key: "k", min: 1, max: 10, want: 7},
		{name: "json number float rejected", settings: map[string]any{"k": json.Number("7.5")}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须是整数"},
		{name: "below min rejected", settings: map[string]any{"k": 0}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须在 1 到 10 之间"},
		{name: "above max rejected", settings: map[string]any{"k": 11}, key: "k", min: 1, max: 10, wantErr: "系统设置 k 必须在 1 到 10 之间"},
		{name: "min boundary accepted", settings: map[string]any{"k": 1}, key: "k", min: 1, max: 10, want: 1},
		{name: "max boundary accepted", settings: map[string]any{"k": 10}, key: "k", min: 1, max: 10, want: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SettingNumber(tt.settings, tt.key, tt.min, tt.max)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("SettingNumber() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SettingNumber() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("SettingNumber() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLoadPolicyFixedFields(t *testing.T) {
	policy, err := LoadPolicy(validPolicySettings())
	if err != nil {
		t.Fatalf("LoadPolicy() unexpected error: %v", err)
	}
	if policy.AccountUsageSnapshotDays != snapshotRetentionMaxDays || policy.FixedWindowDays != statsRetentionMaxDays {
		t.Fatalf("fixed fields must mirror Node constants: %+v", policy)
	}
	if policy.PublicApiLogDays != 30 || policy.UsageRecordDays != 14 || policy.StatsMinuteHours != 48 ||
		policy.StatsHourlyDays != 60 || policy.StatsDailyDays != 90 || policy.StatsWeeklyWeeks != 52 ||
		policy.StatsMonthlyMonths != 24 || policy.RankSnapshotDays != 180 ||
		policy.SystemMetricsSampleDays != 3 || policy.SystemMetricsHourlyDays != 15 {
		t.Fatalf("policy fields not loaded as configured: %+v", policy)
	}
}

// TestLoadPolicyBounds probes every configurable key against its Node upper
// bound (accepted at the bound, rejected one past it).
func TestLoadPolicyBounds(t *testing.T) {
	tests := []struct {
		key string
		max int64
	}{
		{SettingPublicApiLogRetentionDays, publicApiLogRetentionMaxDays},
		{SettingUsageRecordRetentionDays, usageRecordRetentionMaxDays},
		{SettingUsageStatsMinuteRetentionHours, statsMinuteRetentionMaxHours},
		{SettingUsageStatsHourlyRetentionDays, statsHourlyRetentionMaxDays},
		{SettingUsageStatsDailyRetentionDays, statsDailyRetentionMaxDays},
		{SettingUsageStatsWeeklyRetentionWeeks, statsWeeklyRetentionMaxWeeks},
		{SettingUsageStatsMonthlyRetentionMonths, statsMonthlyRetentionMaxMonths},
		{SettingUsageRankSnapshotRetentionDays, rankSnapshotRetentionMaxDays},
		{SettingSystemMetricsRetentionDays, systemMetricsRawRetentionMaxDays},
		{SettingSystemMetricsHourlyRetentionDays, statsRetentionMaxDays},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			settings := validPolicySettings()
			settings[tt.key] = tt.max
			if _, err := LoadPolicy(settings); err != nil {
				t.Fatalf("LoadPolicy(max=%d) unexpected error: %v", tt.max, err)
			}
			settings[tt.key] = tt.max + 1
			_, err := LoadPolicy(settings)
			want := "系统设置 " + tt.key + " 必须在 1 到 " + itoa(tt.max) + " 之间"
			if err == nil || err.Error() != want {
				t.Fatalf("LoadPolicy(max+1) error = %v, want %q", err, want)
			}
		})
	}
}

// TestLoadPolicyFailClosedOrder mirrors the Node literal evaluation order:
// the first invalid key in declaration order fails the load.
func TestLoadPolicyFailClosedOrder(t *testing.T) {
	_, err := LoadPolicy(map[string]any{})
	want := "系统设置 publicApiLogRetentionDays 必须是整数"
	if err == nil || err.Error() != want {
		t.Fatalf("LoadPolicy(empty) error = %v, want %q", err, want)
	}
	settings := validPolicySettings()
	settings[SettingUsageStatsDailyRetentionDays] = "x"
	_, err = LoadPolicy(settings)
	want = "系统设置 usageStatsDailyRetentionDays 必须是整数"
	if err == nil || err.Error() != want {
		t.Fatalf("LoadPolicy(bad daily) error = %v, want %q", err, want)
	}
}

func TestISOString(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{name: "epoch", in: time.UnixMilli(0), want: "1970-01-01T00:00:00.000Z"},
		{name: "millis precision", in: time.Date(2026, 8, 24, 23, 14, 56, 789_000_000, time.UTC), want: "2026-08-24T23:14:56.789Z"},
		{name: "nanos truncated to millis", in: time.Date(2026, 8, 24, 23, 14, 56, 789_999_999, time.UTC), want: "2026-08-24T23:14:56.789Z"},
		{name: "offset converted to UTC", in: time.Date(2026, 9, 4, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)), want: "2026-09-04T04:00:00.000Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ISOString(tt.in); got != tt.want {
				t.Fatalf("ISOString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCutoffKeys(t *testing.T) {
	// 2026-09-04T20:30:00Z; in UTC+8 this wall clock is 2026-09-05T04:30.
	nowMillis := time.Date(2026, 9, 4, 20, 30, 0, 0, time.UTC).UnixMilli()
	zone := time.FixedZone("UTC+8", 8*60*60)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "cutoff iso", got: cutoffISO(nowMillis, 3), want: "2026-09-01T20:30:00.000Z"},
		{name: "cutoff iso hours", got: cutoffMinuteKey(nowMillis, 24, time.UTC), want: "2026-09-03T20:30"},
		{name: "date key business tz crosses day", got: cutoffDateKey(nowMillis, 0, zone), want: "2026-09-05"},
		{name: "hour key business tz", got: cutoffHourKey(nowMillis, 0, zone), want: "2026-09-05T04"},
		{name: "minute key business tz", got: cutoffMinuteKey(nowMillis, 0, zone), want: "2026-09-05T04:30"},
		{name: "week key friday snaps back over month boundary", got: cutoffWeekKey(nowMillis, 0, time.UTC), want: "2026-08-31"},
		{name: "week key one week earlier", got: cutoffWeekKey(nowMillis, 1, time.UTC), want: "2026-08-24"},
		{name: "date key utc", got: cutoffDateKey(nowMillis, 30, time.UTC), want: "2026-08-05"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestCutoffMonthKeyHost(t *testing.T) {
	tests := []struct {
		name   string
		now    time.Time
		months int64
		host   *time.Location
		biz    *time.Location
		want   string
	}{
		// Node: new Date(now).setMonth(...) overflows Feb 31 into Mar 3.
		{name: "month overflow normalizes", now: time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC), months: 1, host: time.UTC, biz: time.UTC, want: "2026-03"},
		{name: "plain previous month", now: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC), months: 1, host: time.UTC, biz: time.UTC, want: "2026-02"},
		{name: "previous year", now: time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC), months: 1, host: time.UTC, biz: time.UTC, want: "2025-12"},
		// Host mutation then business-zone read: 2026-01-31T18:00Z mutated in
		// UTC stays 2025-12-31T18:00Z, which is 2026-01-01 in UTC+8.
		{name: "host mutation business read", now: time.Date(2026, 1, 31, 18, 0, 0, 0, time.UTC), months: 1, host: time.UTC, biz: time.FixedZone("UTC+8", 8*60*60), want: "2026-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cutoffMonthKeyHost(tt.now, tt.months, tt.host, tt.biz); got != tt.want {
				t.Fatalf("cutoffMonthKeyHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadUsageStatsTimezone(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "empty rejected", value: "  ", wantErr: "统计时区必须是非空字符串"},
		{name: "unknown zone rejected", value: "Mars/Olympus", wantErr: "统计时区不存在：Mars/Olympus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadUsageStatsTimezone(tt.value)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("LoadUsageStatsTimezone(%q) error = %v, want %q", tt.value, err, tt.wantErr)
			}
		})
	}
	t.Run("valid zone loads", func(t *testing.T) {
		location, err := LoadUsageStatsTimezone("Asia/Shanghai")
		if err != nil {
			t.Fatalf("LoadUsageStatsTimezone(Asia/Shanghai) unexpected error: %v", err)
		}
		if got := dateKey(time.Date(2026, 9, 4, 16, 0, 0, 0, time.UTC), location); got != "2026-09-05" {
			t.Fatalf("dateKey in Asia/Shanghai = %q, want 2026-09-05", got)
		}
	})
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
