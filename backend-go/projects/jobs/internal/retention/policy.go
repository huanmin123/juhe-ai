// Package retention ports the J6 Node data-retention task family into the Go
// jobs project: data-retention-cleanup, chat-retention-cleanup,
// expired-deleted-account-cleanup, codex-context storage cleanup,
// record-maintenance job execution plus the api-key/account record cleanup
// retries.
//
// Node sources (read-only reference):
//   - modules/background/data-retention-cleanup.service.ts (+ .constants.ts)
//   - modules/background/maintenance-cleanup-jobs.ts
//   - modules/background/codex-context-storage-cleanup.service.ts
//   - modules/record-maintenance/record-maintenance-queue.service.ts (run-once semantics)
//
// The heavy per-domain stores (public API logs, usage shards, chat, accounts,
// stats writer, DB service) stay behind ports in ports.go so every domain has
// an independent owner and can be disabled or mocked on its own. In
// particular the publicApiLogs stage keeps the exact gateway
// projects/gateway/internal/publicapilogs/retention.go contract: cutoff is
// now minus publicApiLogRetentionDays, deletion is strictly created_at <
// cutoff (records equal to the cutoff survive), batches are 1000 rows, a run
// performs at most 20 batches and stops at the first non-full batch. The two
// modules never import each other; a composition root plugs an adapter with
// the same CleanupBefore(ctx, cutoffCreatedAt, limit) signature.
package retention

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Constants mirror data-retention-cleanup.constants.ts byte for byte.
const (
	CleanupIntervalMinutes   = 10
	CleanupBatchSize         = 1000
	CleanupMaxBatchesPerRun  = 20
	CleanupBatchPauseMillis  = 25
	CleanupBatchPause        = CleanupBatchPauseMillis * time.Millisecond
	retentionDayMillis       = int64(24 * 60 * 60 * 1000)
	retentionHourMillis      = int64(60 * 60 * 1000)
	retentionWeekMillis      = 7 * retentionDayMillis
	retentionMinuteMillis    = int64(60 * 1000)
	minimumUsageRecordAgeMs  = int64(24 * 60 * 60 * 1000)
	chatRetentionMaxDays     = 365
	chatRetentionDefaultDays = 3
)

// Retention upper bounds mirror the Node service constants.
const (
	usageRecordRetentionMaxDays        = 180
	accountQualityMinuteRetentionHours = 24
	statsMinuteRetentionMaxHours       = 24 * 14
	statsHourlyRetentionMaxDays        = 180
	statsDailyRetentionMaxDays         = 800
	statsWeeklyRetentionMaxWeeks       = 260
	statsMonthlyRetentionMaxMonths     = 60
	rankSnapshotRetentionMaxDays       = 365
	systemMetricsRawRetentionMaxDays   = 7
	statsRetentionMaxDays              = 30
	snapshotRetentionMaxDays           = 30
	publicApiLogRetentionMaxDays       = 365
)

// Settings keys read by the retention policy (Node settings repository keys).
const (
	SettingPublicApiLogRetentionDays        = "publicApiLogRetentionDays"
	SettingUsageRecordRetentionDays         = "usageRecordRetentionDays"
	SettingUsageStatsMinuteRetentionHours   = "usageStatsMinuteRetentionHours"
	SettingUsageStatsHourlyRetentionDays    = "usageStatsHourlyRetentionDays"
	SettingUsageStatsDailyRetentionDays     = "usageStatsDailyRetentionDays"
	SettingUsageStatsWeeklyRetentionWeeks   = "usageStatsWeeklyRetentionWeeks"
	SettingUsageStatsMonthlyRetentionMonths = "usageStatsMonthlyRetentionMonths"
	SettingUsageRankSnapshotRetentionDays   = "usageRankSnapshotRetentionDays"
	SettingSystemMetricsRetentionDays       = "systemMetricsRetentionDays"
	SettingSystemMetricsHourlyRetentionDays = "systemMetricsHourlyRetentionDays"
	SettingUsageStatsTimezone               = "usageStatsTimezone"
)

// Policy mirrors the Node DataRetentionPolicy / PostgresRetentionPolicy
// literal. AccountUsageSnapshotDays and FixedWindowDays are fixed constants
// in Node, so they are derived, never validated from settings.
type Policy struct {
	PublicApiLogDays         int64
	UsageRecordDays          int64
	StatsMinuteHours         int64
	StatsHourlyDays          int64
	StatsDailyDays           int64
	StatsWeeklyWeeks         int64
	StatsMonthlyMonths       int64
	RankSnapshotDays         int64
	SystemMetricsSampleDays  int64
	SystemMetricsHourlyDays  int64
	AccountUsageSnapshotDays int64
	FixedWindowDays          int64
}

// LoadPolicy mirrors building the Node policy literal: every configurable
// field is validated in the literal's declaration order and the first invalid
// setting fails the whole load (fail-closed, no silent default).
func LoadPolicy(settings map[string]any) (Policy, error) {
	var err error
	policy := Policy{}
	if policy.PublicApiLogDays, err = SettingNumber(settings, SettingPublicApiLogRetentionDays, 1, publicApiLogRetentionMaxDays); err != nil {
		return Policy{}, err
	}
	if policy.UsageRecordDays, err = SettingNumber(settings, SettingUsageRecordRetentionDays, 1, usageRecordRetentionMaxDays); err != nil {
		return Policy{}, err
	}
	if policy.StatsMinuteHours, err = SettingNumber(settings, SettingUsageStatsMinuteRetentionHours, 1, statsMinuteRetentionMaxHours); err != nil {
		return Policy{}, err
	}
	if policy.StatsHourlyDays, err = SettingNumber(settings, SettingUsageStatsHourlyRetentionDays, 1, statsHourlyRetentionMaxDays); err != nil {
		return Policy{}, err
	}
	if policy.StatsDailyDays, err = SettingNumber(settings, SettingUsageStatsDailyRetentionDays, 1, statsDailyRetentionMaxDays); err != nil {
		return Policy{}, err
	}
	if policy.StatsWeeklyWeeks, err = SettingNumber(settings, SettingUsageStatsWeeklyRetentionWeeks, 1, statsWeeklyRetentionMaxWeeks); err != nil {
		return Policy{}, err
	}
	if policy.StatsMonthlyMonths, err = SettingNumber(settings, SettingUsageStatsMonthlyRetentionMonths, 1, statsMonthlyRetentionMaxMonths); err != nil {
		return Policy{}, err
	}
	if policy.RankSnapshotDays, err = SettingNumber(settings, SettingUsageRankSnapshotRetentionDays, 1, rankSnapshotRetentionMaxDays); err != nil {
		return Policy{}, err
	}
	if policy.SystemMetricsSampleDays, err = SettingNumber(settings, SettingSystemMetricsRetentionDays, 1, systemMetricsRawRetentionMaxDays); err != nil {
		return Policy{}, err
	}
	if policy.SystemMetricsHourlyDays, err = SettingNumber(settings, SettingSystemMetricsHourlyRetentionDays, 1, statsRetentionMaxDays); err != nil {
		return Policy{}, err
	}
	policy.AccountUsageSnapshotDays = snapshotRetentionMaxDays
	policy.FixedWindowDays = statsRetentionMaxDays
	return policy, nil
}

// SettingNumber mirrors the Node settingNumber helper. A missing or
// non-integer value fails with 系统设置 %s 必须是整数, an out-of-range value
// with 系统设置 %s 必须在 %d 到 %d 之间. Integer-valued float64 / json.Number
// inputs are accepted, matching Number.isInteger on a JS number.
func SettingNumber(settings map[string]any, key string, min, max int64) (int64, error) {
	value, ok := settings[key]
	if !ok {
		return 0, fmt.Errorf("系统设置 %s 必须是整数", key)
	}
	parsed, err := settingIntegerValue(value)
	if err != nil {
		return 0, fmt.Errorf("系统设置 %s 必须是整数", key)
	}
	if parsed < min || parsed > max {
		return 0, fmt.Errorf("系统设置 %s 必须在 %d 到 %d 之间", key, min, max)
	}
	return parsed, nil
}

func settingIntegerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		return int64(typed), nil
	case float64:
		if typed != float64(int64(typed)) {
			return 0, errNotInteger
		}
		return int64(typed), nil
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return 0, errNotInteger
		}
		return parsed, nil
	}
	return 0, errNotInteger
}

type notIntegerError struct{}

var errNotInteger = notIntegerError{}

func (notIntegerError) Error() string { return "value is not an integer" }
