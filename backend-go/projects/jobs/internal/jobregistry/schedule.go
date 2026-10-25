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
// usageOverviewWindowRefreshIntervalMs=5min。原 coldUsageRangeWindowRefreshIntervalMs=
// 6h 随 usage-scope-range-windows-refresh 的 sqlite-only 注册退役）。
func schedules() map[string]Schedule {
	rankInterval := 30 * minute
	return map[string]Schedule{
		// stats-worker（背景任务对账）
		"background-task-run-reconcile": {
			Interval: 5 * minute, InitialDelay: 2 * second, PassiveJitter: true,
			ScheduleMode: "fixedDelay", Timeout: 2 * minute,
			BackoffBase: 5 * second, BackoffMax: 5 * minute, LeaseTTL: 2 * minute,
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
		"usage-overview-windows-refresh": {
			Interval: 5 * minute, InitialDelay: 4*minute + 10*second, StablePhaseWindow: 30 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 10 * minute,
			BackoffBase: 30 * second, BackoffMax: 10 * minute, LeaseTTL: 15 * minute,
		},
		// usage-scope-range-windows-refresh（原 sqlite-only 冷历史范围窗口重刷，
		// 6h 档）已随 sqlite 存储形态退役删除（D13：sqlite-only 任务注册清理）；
		// scope 范围窗口写面由 usage-hot-window-refresh（热窗口阶段）承载。
		// authorization-usage-range-windows-refresh 已按设计消灭，无调度参数：
		// 授权消耗明细读端按日范围直读 authorization_*_usage_summary_daily，
		// 范围窗口表不再刷新。
		// 配额小时窗刷新（BUG-0175 D-48 族）：归档未保留 background-jobs.ts
		// 的原始 scheduler.schedule 实参，参数对齐窗口刷新族惯例放宽：
		// interval=5min（对齐 usage-overview-windows-refresh 的热窗口节拍，
		// 满足最小 1h 配额窗口的执法新鲜度）、hasMore 续跑在任务内完成
		//（timeout 10min 覆盖脏积压排空）。
		"usage-quota-hourly-windows-refresh": {
			Interval: 5 * minute, InitialDelay: 50 * second, StablePhaseWindow: 10 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "stats-heavy", Timeout: 10 * minute,
			BackoffBase: 30 * second, BackoffMax: 5 * minute, LeaseTTL: 15 * minute,
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
		// media-jobs-retention：Go 新增任务（媒体设计 §8.2，M2；归档无对应
		// scheduled job 可对照）。TTL 7 天的任务行清理量级小，interval=1h
		// 足够收敛；fixedDelay + storage-maintenance lane 对齐 retention 邻居
		//（data/chat-retention 10min/450s/270s），initial delay 9min 与两者
		// 错峰；timeout 2min/LeaseTTL 5min 对齐 chat-retention-cleanup。
		"media-jobs-retention": {
			Interval: hour, InitialDelay: 9 * minute, StablePhaseWindow: 30 * second,
			PassiveJitter: true, ScheduleMode: "fixedDelay", Lane: "storage-maintenance", Timeout: 2 * minute,
			BackoffBase: 30 * second, BackoffMax: 10 * minute, LeaseTTL: 5 * minute,
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
		// xai-grok-usage-refresh：Go 新增任务（AI 账户 Grok 用量快照设计 §4，
		// 归档 Node 无对应 scheduled job 可对照）。每轮全量扫描 provider_code='xai'
		// 且 type='oauth' 的账户（数量少，无租约/游标），调上游 billing/settings
		// 落 stats 库 account_usage_snapshots kind='xai_grok' 快照；interval
		// 10 分钟（用量百分比变化粒度低）、timeout 60s、OverlapCoalesce 同
		// account-balance-refresh 惯例，lane 对齐 external-account-maintenance；
		// initial delay 30s 与余额族（20s/25s）错峰，LeaseTTL 2 分钟同 OAuth
		// 家族（PG 调度租约 + 运行历史）。
		"xai-grok-usage-refresh": {
			Interval: 10 * minute, InitialDelay: 30 * second, StablePhaseWindow: 5 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "external-account-maintenance", Timeout: 60 * second,
			BackoffBase: 10 * second, BackoffMax: 5 * minute, LeaseTTL: 2 * minute,
		},
		// upstream-client-version-refresh：Go 新增任务（客户端版本自动跟版设计
		// §6）。日频串行拉五族官方发布源，幂等 upsert 自动键；InitialDelay 37min
		// 避让现有 31min 档内时隙；PassiveJitter 走 DayWindow ±1h。
		"upstream-client-version-refresh": {
			Interval: 24 * hour, InitialDelay: 37 * minute, PassiveJitter: true,
			OverlapCoalesce: true, Lane: "external-account-maintenance", Timeout: 3 * minute,
			BackoffBase: 30 * minute, BackoffMax: 6 * hour, LeaseTTL: 2 * minute,
		},
		// account-balance-stats-projection：Go 新增任务（归档 Node
		// account-balance-jobs-projector 的 Go 等价，归档无 scheduler.schedule
		// 实参可对照，按注册表约定取保守值并注明来源）。每轮全量投影
		// juhe_jobs.account_balance_snapshots → juhe_stats relay_balance（98 行
		// 级别，无游标）；interval 1 分钟对齐 J2 快照的秒级新鲜度足够传递；
		// 独立 lane=balance-projection，不与 external-account-maintenance 的
		// 余额族互相争抢 lane 并发槽；initial delay 47s 与余额族
		// （20s/25s/30s/35s/40s）错峰；timeout 45s、OverlapCoalesce 同余额族
		// 惯例；LeaseTTL 2 分钟同 OAuth/xai 家族（PG 调度租约 + 运行历史）。
		"account-balance-stats-projection": {
			Interval: minute, InitialDelay: 47 * second, StablePhaseWindow: 5 * second,
			PassiveJitter: true, OverlapCoalesce: true, Lane: "balance-projection", Timeout: 45 * second,
			BackoffBase: 10 * second, BackoffMax: 5 * minute, LeaseTTL: 2 * minute,
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
		// oauth-keepalive-token-refresh：归档未保留 Node keepalive 周期（该任务
		// 族语义源自 dispatch-preparation 换发窗口，无既有 scheduler.schedule
		// 实参可对照），按注册表约定取保守值并注明来源：interval 对齐相邻
		// OAuth 条目 openai-oauth-access-token-refresh 的 60s 节拍
		// （OAuthTokenRefreshInterval，覆盖最小 60s keepalive lead 窗口）；
		// 候选查询批量受限（默认 20/族）且到点前不产生写，上游无额外压力；
		// initial delay 40s 与 openai 刷新（35s）错峰。
		"oauth-keepalive-token-refresh": {
			Interval: OAuthTokenRefreshInterval, InitialDelay: 40 * second, StablePhaseWindow: 5 * second,
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
