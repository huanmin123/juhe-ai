package jobregistry

import "time"

// Schedule 是一个 scheduled job 的调度参数（对齐 Node background-jobs.ts 的
// scheduler.schedule 实参与 runWithPostgresScheduledLease 的 TTL）。
type Schedule struct {
	Interval          time.Duration
	InitialDelay      time.Duration
	StablePhaseWindow time.Duration
	PassiveJitter     bool
	// DeferFirstRun 为 true 表示首轮推迟一个完整间隔（Node runImmediately=false）。
	DeferFirstRun bool
	ScheduleMode  string // fixedRate（默认）| fixedDelay
	// OverlapCoalesce 为 true 表示 overlapPolicy=coalesceOne（默认 skip）。
	OverlapCoalesce bool
	Timeout         time.Duration
	Lane            string
	BackoffBase     time.Duration
	BackoffMax      time.Duration
	// LeaseTTL 是 runWithPostgresScheduledLease(jobName, ttlMs) 的租约时长；
	// 0 表示 Node 调度位置不包租约。
	LeaseTTL time.Duration
}

const (
	second = time.Second
	minute = time.Minute
	hour   = time.Hour
)

// SettingsInterval 允许组合根注入按系统设置解析的间隔（如
// statsAggregationIntervalSeconds）；nil 时用 Node 默认值。
type SettingsInterval func(jobName string) (time.Duration, bool)

// ScheduleFor 返回一个 job 的 Node 调度参数。
func ScheduleFor(jobName string) (Schedule, bool) {
	schedule, ok := schedules()[jobName]
	return schedule, ok
}

// ResolveSchedule 在 ScheduleFor 基础上应用设置解析覆盖。
func ResolveSchedule(jobName string, settings SettingsInterval) (Schedule, bool) {
	schedule, ok := ScheduleFor(jobName)
	if !ok {
		return Schedule{}, false
	}
	if settings != nil {
		if interval, matches := settings(jobName); matches && interval > 0 {
			schedule.Interval = interval
		}
	}
	return schedule, true
}

// SettingsIntervalJobNames 列出间隔来自系统设置的 job（供组合根解析）。
func SettingsIntervalJobNames() map[string]string {
	return map[string]string{
		"system-metrics-sample":             "systemMetricsSampleIntervalSeconds",
		"usage-stats-aggregation":           "statsAggregationIntervalSeconds",
		"client-ip-stats-aggregation":       "statsAggregationIntervalSeconds",
		"group-account-stats-refresh":       "groupAccountStatsRefreshIntervalSeconds",
		"usage-hot-window-refresh":          "usageHotWindowRefreshIntervalSeconds",
		"account-api-key-cooldown-retest":   "cooldownAccountRetestIntervalSeconds",
		"openai-oauth-access-token-refresh": "oauthAccessTokenRefreshIntervalSeconds",
		"account-quality-refresh":           "accountQualityRefreshIntervalSeconds",
	}
}

// schedules 返回全部 scheduled job 的调度参数表（值取自
// background-jobs.ts；usageRankSnapshotRefreshIntervalMs=30min、
// usageOverviewWindowRefreshIntervalMs=5min、coldUsageRangeWindowRefreshIntervalMs=6h）。
func schedules() map[string]Schedule {
	rankInterval := 30 * minute
	coldRangeInterval := 6 * hour
	return map[string]Schedule{
		// stats-worker（背景任务对账）
		"background-task-run-reconcile": {
			Interval: 5 * minute, InitialDelay: 2 * second, PassiveJitter: true,
			ScheduleMode: "fixedDelay", Timeout: 2 * minute,
			BackoffBase: 5 * second, BackoffMax: 5 * minute, LeaseTTL: 2 * minute,
		},
		"system-metrics-sample": {
			Interval: SystemMetricsSampleInterval, InitialDelay: 4 * second, PassiveJitter: true,
			OverlapCoalesce: true, Timeout: 20 * second, LeaseTTL: 15 * second,
		},
		"usage-stats-aggregation": {
			Interval: StatsAggregationInterval, InitialDelay: 3 * second, StablePhaseWindow: 2 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-online", Timeout: 20 * second,
			BackoffBase: second, BackoffMax: minute, LeaseTTL: minute,
		},
		"client-ip-stats-aggregation": {
			Interval: StatsAggregationInterval, InitialDelay: 8 * second, StablePhaseWindow: 2 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-online", Timeout: 20 * second,
			BackoffBase: second, BackoffMax: minute, LeaseTTL: minute,
		},
		"group-account-stats-refresh": {
			Interval: GroupAccountStatsInterval, InitialDelay: 16 * second, PassiveJitter: true,
			OverlapCoalesce: true, Lane: "stats-online", Timeout: 30 * second,
			BackoffBase: second, BackoffMax: minute, LeaseTTL: 2 * minute,
		},
		"usage-hot-window-refresh": {
			Interval: UsageHotWindowInterval, InitialDelay: 25 * second, StablePhaseWindow: 10 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 35 * second,
			BackoffBase: 10 * second, BackoffMax: 5 * minute,
		},
		"usage-rank-snapshots-refresh": {
			Interval: rankInterval, InitialDelay: 2*minute + 30*second, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 10 * minute,
			BackoffBase: 30 * second, BackoffMax: 10 * minute, LeaseTTL: 15 * minute,
		},
		"ai-performance-summary-windows-refresh": {
			Interval: 5 * minute, InitialDelay: 3 * minute, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: minute,
			BackoffBase: 15 * second, BackoffMax: 5 * minute, LeaseTTL: 5 * minute,
		},
		"system-metrics-trend-windows-refresh": {
			Interval: rankInterval, InitialDelay: 3*minute + 20*second, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 10 * minute,
			BackoffBase: 30 * second, BackoffMax: 10 * minute, LeaseTTL: 15 * minute,
		},
		"usage-overview-windows-refresh": {
			Interval: 5 * minute, InitialDelay: 4*minute + 10*second, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 10 * minute,
			BackoffBase: 30 * second, BackoffMax: 10 * minute, LeaseTTL: 15 * minute,
		},
		"usage-scope-range-windows-refresh": {
			Interval: coldRangeInterval, InitialDelay: 31 * minute, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 10 * minute,
			BackoffBase: minute, BackoffMax: 30 * minute, LeaseTTL: 15 * minute,
		},
		"authorization-usage-range-windows-refresh": {
			Interval: coldRangeInterval, InitialDelay: 43 * minute, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 10 * minute,
			BackoffBase: minute, BackoffMax: 30 * minute, LeaseTTL: 15 * minute,
		},
		"usage-stats-consistency-check": {
			Interval: 60 * minute, InitialDelay: 11 * minute, PassiveJitter: true, LeaseTTL: 5 * minute,
		},

		// ingest-worker（清理重试/保留）
		"api-key-record-cleanup-retry": {
			Interval: minute, InitialDelay: 24 * second, PassiveJitter: true, LeaseTTL: 2 * minute,
		},
		"account-record-cleanup-retry": {
			Interval: minute, InitialDelay: 42 * second, PassiveJitter: true, LeaseTTL: 2 * minute,
		},
		"data-retention-cleanup": {
			Interval: 10 * minute, InitialDelay: 450 * second, StablePhaseWindow: minute,
			PassiveJitter: true, ScheduleMode: "fixedDelay", Lane: "storage-maintenance", Timeout: 5 * minute,
			BackoffBase: minute, BackoffMax: 30 * minute, LeaseTTL: 10 * minute,
		},

		// ops-worker（可用性/授权/保留）
		"chat-retention-cleanup": {
			Interval: 10 * minute, InitialDelay: 270 * second, StablePhaseWindow: 30 * second,
			PassiveJitter: true, ScheduleMode: "fixedDelay", Lane: "storage-maintenance", Timeout: 2 * minute,
			BackoffBase: 30 * second, BackoffMax: 10 * minute, LeaseTTL: 5 * minute,
		},
		"api-key-availability-schedule-status-sync": {
			Interval: 10 * second, InitialDelay: second, PassiveJitter: true, LeaseTTL: 30 * second,
		},
		"account-availability-schedule-status-sync": {
			Interval: 10 * second, InitialDelay: 2 * second, PassiveJitter: true, LeaseTTL: 30 * second,
		},
		"resource-authorization-expiry-sweep": {
			Interval: minute, InitialDelay: 54 * second, PassiveJitter: true, LeaseTTL: 2 * minute,
		},
		"expired-deleted-account-cleanup": {
			Interval: 24 * hour, InitialDelay: 14 * minute, PassiveJitter: true, LeaseTTL: 10 * minute,
		},

		// ops-worker（探针与恢复）
		"account-balance-refresh": {
			Interval: minute, InitialDelay: 20 * second, StablePhaseWindow: 5 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "external-account-maintenance", Timeout: 60 * second,
			BackoffBase: 10 * second, BackoffMax: 5 * minute,
		},
		"account-balance-auto-detect-recovery": {
			Interval: minute, InitialDelay: 25 * second, StablePhaseWindow: 5 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "external-account-maintenance", Timeout: 45 * second,
			BackoffBase: 10 * second, BackoffMax: 5 * minute,
		},
		"account-api-key-cooldown-retest": {
			Interval: CooldownRetestInterval, InitialDelay: 60 * second, PassiveJitter: true,
		},
		"normal-route-speed-first-recovery-probe": {
			Interval: 5 * second, InitialDelay: 75 * second, PassiveJitter: true,
		},
		"account-circuit-control-plane-maintenance": {
			Interval: 5 * second, InitialDelay: second, PassiveJitter: true,
		},
		"account-list-availability-projection-maintenance": {
			Interval: second, InitialDelay: second, StablePhaseWindow: second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "account-list-projection", Timeout: 60 * second,
			BackoffBase: second, BackoffMax: minute, LeaseTTL: 2 * minute,
		},
		"account-circuit-recovery": {
			Interval: 5 * second, InitialDelay: 5 * second, PassiveJitter: true,
		},
		"key-model-memory-recovery": {
			Interval: second, InitialDelay: second,
			OverlapCoalesce: true, Lane: "external-account-maintenance", Timeout: 45 * second,
			BackoffBase: second, BackoffMax: 5 * second,
		},
		"openai-oauth-access-token-refresh": {
			Interval: OAuthTokenRefreshInterval, InitialDelay: 35 * second, StablePhaseWindow: 5 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "external-account-maintenance", Timeout: 90 * second,
			BackoffBase: 10 * second, BackoffMax: 5 * minute, LeaseTTL: 2 * minute,
		},

		// stats-worker（账户质量）
		"account-quality-refresh": {
			Interval: AccountQualityRefreshDefault, InitialDelay: 75 * second, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-online", Timeout: minute,
			BackoffBase: 5 * second, BackoffMax: 5 * minute, LeaseTTL: 5 * minute,
		},
	}
}
