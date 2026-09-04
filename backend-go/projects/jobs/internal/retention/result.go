package retention

// CleanupResult mirrors the Node DataRetentionCleanupResult object: every
// counter the retention family reports, in Node field order. Values uses the
// same order so logs keep the Node key sequence.
type CleanupResult struct {
	PublicApiLogs                      int64
	UsageRecords                       int64
	AccountQualityMinuteStats          int64
	AccountHealthHourly                int64
	UsageStatsMinute                   int64
	UsageModelMinute                   int64
	UsageErrorMinute                   int64
	UsageLatencyMinute                 int64
	UsageStatsDaily                    int64
	UsageModelDaily                    int64
	UsageErrorDaily                    int64
	UsageLatencyDaily                  int64
	UsageStatsHourly                   int64
	UsageModelHourly                   int64
	UsageErrorHourly                   int64
	UsageLatencyHourly                 int64
	UsageStatsWeekly                   int64
	UsageModelWeekly                   int64
	UsageErrorWeekly                   int64
	UsageLatencyWeekly                 int64
	UsageStatsMonthly                  int64
	UsageModelMonthly                  int64
	UsageErrorMonthly                  int64
	UsageLatencyMonthly                int64
	AuthorizationTeamUsageSummaryDaily int64
	AuthorizationTeamUsageRangeWindows int64
	AuthorizationUserUsageSummaryDaily int64
	AuthorizationUserUsageRangeWindows int64
	UsageRankSnapshots                 int64
	UsageOverviewSummaryWindows        int64
	UsageOverviewTrendWindows          int64
	UsageModelRankWindows              int64
	UsageErrorRankWindows              int64
	AIPerformanceSummaryWindows        int64
	UsageQuotaHourlyWindows            int64
	UsageScopeRangeWindows             int64
	ClientIpUsageRangeWindows          int64
	ClientIpRangeWindowDirtyIps        int64
	ClientIpAccountStatsDaily          int64
	ClientIpAccountUsageRangeWindows   int64
	ClientIpAccountRangeWindowDirtyIps int64
	AccountUsageSnapshots              int64
	SystemMetricsSamples               int64
	SystemMetricsHourly                int64
	SystemMetricsTrendWindows          int64
	ProcessEventLoopSamples            int64
	ProcessEventLoopHourly             int64
	ProcessEventLoopTrendWindows       int64
	SystemSessions                     int64
	CodexContextSessions               int64
	CodexContextResponses              int64
	CodexContextCompacts               int64
	CodexContextFiles                  int64
}

// EmptyCleanupResult mirrors emptyCleanupResult: every counter starts at 0.
func EmptyCleanupResult() CleanupResult { return CleanupResult{} }

// Add merges another full result (Node addCleanupResult over a partial).
func (r *CleanupResult) Add(other CleanupResult) {
	r.PublicApiLogs += other.PublicApiLogs
	r.UsageRecords += other.UsageRecords
	r.AccountQualityMinuteStats += other.AccountQualityMinuteStats
	r.AccountHealthHourly += other.AccountHealthHourly
	r.UsageStatsMinute += other.UsageStatsMinute
	r.UsageModelMinute += other.UsageModelMinute
	r.UsageErrorMinute += other.UsageErrorMinute
	r.UsageLatencyMinute += other.UsageLatencyMinute
	r.UsageStatsDaily += other.UsageStatsDaily
	r.UsageModelDaily += other.UsageModelDaily
	r.UsageErrorDaily += other.UsageErrorDaily
	r.UsageLatencyDaily += other.UsageLatencyDaily
	r.UsageStatsHourly += other.UsageStatsHourly
	r.UsageModelHourly += other.UsageModelHourly
	r.UsageErrorHourly += other.UsageErrorHourly
	r.UsageLatencyHourly += other.UsageLatencyHourly
	r.UsageStatsWeekly += other.UsageStatsWeekly
	r.UsageModelWeekly += other.UsageModelWeekly
	r.UsageErrorWeekly += other.UsageErrorWeekly
	r.UsageLatencyWeekly += other.UsageLatencyWeekly
	r.UsageStatsMonthly += other.UsageStatsMonthly
	r.UsageModelMonthly += other.UsageModelMonthly
	r.UsageErrorMonthly += other.UsageErrorMonthly
	r.UsageLatencyMonthly += other.UsageLatencyMonthly
	r.AuthorizationTeamUsageSummaryDaily += other.AuthorizationTeamUsageSummaryDaily
	r.AuthorizationTeamUsageRangeWindows += other.AuthorizationTeamUsageRangeWindows
	r.AuthorizationUserUsageSummaryDaily += other.AuthorizationUserUsageSummaryDaily
	r.AuthorizationUserUsageRangeWindows += other.AuthorizationUserUsageRangeWindows
	r.UsageRankSnapshots += other.UsageRankSnapshots
	r.UsageOverviewSummaryWindows += other.UsageOverviewSummaryWindows
	r.UsageOverviewTrendWindows += other.UsageOverviewTrendWindows
	r.UsageModelRankWindows += other.UsageModelRankWindows
	r.UsageErrorRankWindows += other.UsageErrorRankWindows
	r.AIPerformanceSummaryWindows += other.AIPerformanceSummaryWindows
	r.UsageQuotaHourlyWindows += other.UsageQuotaHourlyWindows
	r.UsageScopeRangeWindows += other.UsageScopeRangeWindows
	r.ClientIpUsageRangeWindows += other.ClientIpUsageRangeWindows
	r.ClientIpRangeWindowDirtyIps += other.ClientIpRangeWindowDirtyIps
	r.ClientIpAccountStatsDaily += other.ClientIpAccountStatsDaily
	r.ClientIpAccountUsageRangeWindows += other.ClientIpAccountUsageRangeWindows
	r.ClientIpAccountRangeWindowDirtyIps += other.ClientIpAccountRangeWindowDirtyIps
	r.AccountUsageSnapshots += other.AccountUsageSnapshots
	r.SystemMetricsSamples += other.SystemMetricsSamples
	r.SystemMetricsHourly += other.SystemMetricsHourly
	r.SystemMetricsTrendWindows += other.SystemMetricsTrendWindows
	r.ProcessEventLoopSamples += other.ProcessEventLoopSamples
	r.ProcessEventLoopHourly += other.ProcessEventLoopHourly
	r.ProcessEventLoopTrendWindows += other.ProcessEventLoopTrendWindows
	r.SystemSessions += other.SystemSessions
	r.CodexContextSessions += other.CodexContextSessions
	r.CodexContextResponses += other.CodexContextResponses
	r.CodexContextCompacts += other.CodexContextCompacts
	r.CodexContextFiles += other.CodexContextFiles
}

// AddUsageStats mirrors addCleanupResult over a stats-writer
// cleanup_usage_stats_retention result.
func (r *CleanupResult) AddUsageStats(counts UsageStatsRetentionCounts) {
	r.AccountQualityMinuteStats += counts.AccountQualityMinuteStats
	r.AccountHealthHourly += counts.AccountHealthHourly
	r.UsageStatsMinute += counts.UsageStatsMinute
	r.UsageModelMinute += counts.UsageModelMinute
	r.UsageErrorMinute += counts.UsageErrorMinute
	r.UsageLatencyMinute += counts.UsageLatencyMinute
	r.UsageStatsDaily += counts.UsageStatsDaily
	r.UsageModelDaily += counts.UsageModelDaily
	r.UsageErrorDaily += counts.UsageErrorDaily
	r.UsageLatencyDaily += counts.UsageLatencyDaily
	r.UsageStatsHourly += counts.UsageStatsHourly
	r.UsageModelHourly += counts.UsageModelHourly
	r.UsageErrorHourly += counts.UsageErrorHourly
	r.UsageLatencyHourly += counts.UsageLatencyHourly
	r.UsageStatsWeekly += counts.UsageStatsWeekly
	r.UsageModelWeekly += counts.UsageModelWeekly
	r.UsageErrorWeekly += counts.UsageErrorWeekly
	r.UsageLatencyWeekly += counts.UsageLatencyWeekly
	r.UsageStatsMonthly += counts.UsageStatsMonthly
	r.UsageModelMonthly += counts.UsageModelMonthly
	r.UsageErrorMonthly += counts.UsageErrorMonthly
	r.UsageLatencyMonthly += counts.UsageLatencyMonthly
	r.AuthorizationTeamUsageSummaryDaily += counts.AuthorizationTeamUsageSummaryDaily
	r.AuthorizationTeamUsageRangeWindows += counts.AuthorizationTeamUsageRangeWindows
	r.AuthorizationUserUsageSummaryDaily += counts.AuthorizationUserUsageSummaryDaily
	r.AuthorizationUserUsageRangeWindows += counts.AuthorizationUserUsageRangeWindows
	r.UsageRankSnapshots += counts.UsageRankSnapshots
	r.UsageOverviewSummaryWindows += counts.UsageOverviewSummaryWindows
	r.UsageOverviewTrendWindows += counts.UsageOverviewTrendWindows
	r.UsageModelRankWindows += counts.UsageModelRankWindows
	r.UsageErrorRankWindows += counts.UsageErrorRankWindows
	r.AIPerformanceSummaryWindows += counts.AIPerformanceSummaryWindows
	r.UsageQuotaHourlyWindows += counts.UsageQuotaHourlyWindows
	r.UsageScopeRangeWindows += counts.UsageScopeRangeWindows
	r.ClientIpUsageRangeWindows += counts.ClientIpUsageRangeWindows
	r.ClientIpRangeWindowDirtyIps += counts.ClientIpRangeWindowDirtyIps
	r.ClientIpAccountStatsDaily += counts.ClientIpAccountStatsDaily
	r.ClientIpAccountUsageRangeWindows += counts.ClientIpAccountUsageRangeWindows
	r.ClientIpAccountRangeWindowDirtyIps += counts.ClientIpAccountRangeWindowDirtyIps
	r.AccountUsageSnapshots += counts.AccountUsageSnapshots
}

// AddSystemMetrics mirrors addCleanupResult over a stats-writer
// cleanup_system_metrics_retention result.
func (r *CleanupResult) AddSystemMetrics(counts SystemMetricsRetentionCounts) {
	r.SystemMetricsSamples += counts.SystemMetricsSamples
	r.SystemMetricsHourly += counts.SystemMetricsHourly
	r.SystemMetricsTrendWindows += counts.SystemMetricsTrendWindows
	r.ProcessEventLoopSamples += counts.ProcessEventLoopSamples
	r.ProcessEventLoopHourly += counts.ProcessEventLoopHourly
	r.ProcessEventLoopTrendWindows += counts.ProcessEventLoopTrendWindows
}

// Sum mirrors sumDeleted: the total number of deleted entities across all
// counters. Batch loops compare it against zero to decide whether to stop.
func (r CleanupResult) Sum() int64 {
	var sum int64
	sum += r.PublicApiLogs
	sum += r.UsageRecords
	sum += r.AccountQualityMinuteStats
	sum += r.AccountHealthHourly
	sum += r.UsageStatsMinute
	sum += r.UsageModelMinute
	sum += r.UsageErrorMinute
	sum += r.UsageLatencyMinute
	sum += r.UsageStatsDaily
	sum += r.UsageModelDaily
	sum += r.UsageErrorDaily
	sum += r.UsageLatencyDaily
	sum += r.UsageStatsHourly
	sum += r.UsageModelHourly
	sum += r.UsageErrorHourly
	sum += r.UsageLatencyHourly
	sum += r.UsageStatsWeekly
	sum += r.UsageModelWeekly
	sum += r.UsageErrorWeekly
	sum += r.UsageLatencyWeekly
	sum += r.UsageStatsMonthly
	sum += r.UsageModelMonthly
	sum += r.UsageErrorMonthly
	sum += r.UsageLatencyMonthly
	sum += r.AuthorizationTeamUsageSummaryDaily
	sum += r.AuthorizationTeamUsageRangeWindows
	sum += r.AuthorizationUserUsageSummaryDaily
	sum += r.AuthorizationUserUsageRangeWindows
	sum += r.UsageRankSnapshots
	sum += r.UsageOverviewSummaryWindows
	sum += r.UsageOverviewTrendWindows
	sum += r.UsageModelRankWindows
	sum += r.UsageErrorRankWindows
	sum += r.AIPerformanceSummaryWindows
	sum += r.UsageQuotaHourlyWindows
	sum += r.UsageScopeRangeWindows
	sum += r.ClientIpUsageRangeWindows
	sum += r.ClientIpRangeWindowDirtyIps
	sum += r.ClientIpAccountStatsDaily
	sum += r.ClientIpAccountUsageRangeWindows
	sum += r.ClientIpAccountRangeWindowDirtyIps
	sum += r.AccountUsageSnapshots
	sum += r.SystemMetricsSamples
	sum += r.SystemMetricsHourly
	sum += r.SystemMetricsTrendWindows
	sum += r.ProcessEventLoopSamples
	sum += r.ProcessEventLoopHourly
	sum += r.ProcessEventLoopTrendWindows
	sum += r.SystemSessions
	sum += r.CodexContextSessions
	sum += r.CodexContextResponses
	sum += r.CodexContextCompacts
	sum += r.CodexContextFiles
	return sum
}

// Values returns flat key/value pairs in Node field order for structured
// logging (the Node log object key sequence).
func (r CleanupResult) Values() []any {
	return []any{
		"publicApiLogs", r.PublicApiLogs,
		"usageRecords", r.UsageRecords,
		"accountQualityMinuteStats", r.AccountQualityMinuteStats,
		"accountHealthHourly", r.AccountHealthHourly,
		"usageStatsMinute", r.UsageStatsMinute,
		"usageModelMinute", r.UsageModelMinute,
		"usageErrorMinute", r.UsageErrorMinute,
		"usageLatencyMinute", r.UsageLatencyMinute,
		"usageStatsDaily", r.UsageStatsDaily,
		"usageModelDaily", r.UsageModelDaily,
		"usageErrorDaily", r.UsageErrorDaily,
		"usageLatencyDaily", r.UsageLatencyDaily,
		"usageStatsHourly", r.UsageStatsHourly,
		"usageModelHourly", r.UsageModelHourly,
		"usageErrorHourly", r.UsageErrorHourly,
		"usageLatencyHourly", r.UsageLatencyHourly,
		"usageStatsWeekly", r.UsageStatsWeekly,
		"usageModelWeekly", r.UsageModelWeekly,
		"usageErrorWeekly", r.UsageErrorWeekly,
		"usageLatencyWeekly", r.UsageLatencyWeekly,
		"usageStatsMonthly", r.UsageStatsMonthly,
		"usageModelMonthly", r.UsageModelMonthly,
		"usageErrorMonthly", r.UsageErrorMonthly,
		"usageLatencyMonthly", r.UsageLatencyMonthly,
		"authorizationTeamUsageSummaryDaily", r.AuthorizationTeamUsageSummaryDaily,
		"authorizationTeamUsageRangeWindows", r.AuthorizationTeamUsageRangeWindows,
		"authorizationUserUsageSummaryDaily", r.AuthorizationUserUsageSummaryDaily,
		"authorizationUserUsageRangeWindows", r.AuthorizationUserUsageRangeWindows,
		"usageRankSnapshots", r.UsageRankSnapshots,
		"usageOverviewSummaryWindows", r.UsageOverviewSummaryWindows,
		"usageOverviewTrendWindows", r.UsageOverviewTrendWindows,
		"usageModelRankWindows", r.UsageModelRankWindows,
		"usageErrorRankWindows", r.UsageErrorRankWindows,
		"aiPerformanceSummaryWindows", r.AIPerformanceSummaryWindows,
		"usageQuotaHourlyWindows", r.UsageQuotaHourlyWindows,
		"usageScopeRangeWindows", r.UsageScopeRangeWindows,
		"clientIpUsageRangeWindows", r.ClientIpUsageRangeWindows,
		"clientIpRangeWindowDirtyIps", r.ClientIpRangeWindowDirtyIps,
		"clientIpAccountStatsDaily", r.ClientIpAccountStatsDaily,
		"clientIpAccountUsageRangeWindows", r.ClientIpAccountUsageRangeWindows,
		"clientIpAccountRangeWindowDirtyIps", r.ClientIpAccountRangeWindowDirtyIps,
		"accountUsageSnapshots", r.AccountUsageSnapshots,
		"systemMetricsSamples", r.SystemMetricsSamples,
		"systemMetricsHourly", r.SystemMetricsHourly,
		"systemMetricsTrendWindows", r.SystemMetricsTrendWindows,
		"processEventLoopSamples", r.ProcessEventLoopSamples,
		"processEventLoopHourly", r.ProcessEventLoopHourly,
		"processEventLoopTrendWindows", r.ProcessEventLoopTrendWindows,
		"systemSessions", r.SystemSessions,
		"codexContextSessions", r.CodexContextSessions,
		"codexContextResponses", r.CodexContextResponses,
		"codexContextCompacts", r.CodexContextCompacts,
		"codexContextFiles", r.CodexContextFiles,
	}
}

// Map returns the counters keyed by their Node field names, matching the
// retainedCleanup object logged by the Postgres dispatch path.
func (r CleanupResult) Map() map[string]int64 {
	values := r.Values()
	result := make(map[string]int64, len(values)/2)
	for index := 0; index+1 < len(values); index += 2 {
		result[values[index].(string)] = values[index+1].(int64)
	}
	return result
}

// UsageStatsRetentionCounts mirrors UsageStatsRetentionCleanupResult, the
// stats-writer result of cleanup_usage_stats_retention.
type UsageStatsRetentionCounts struct {
	AccountQualityMinuteStats          int64
	AccountHealthHourly                int64
	UsageStatsMinute                   int64
	UsageModelMinute                   int64
	UsageErrorMinute                   int64
	UsageLatencyMinute                 int64
	UsageStatsDaily                    int64
	UsageModelDaily                    int64
	UsageErrorDaily                    int64
	UsageLatencyDaily                  int64
	UsageStatsHourly                   int64
	UsageModelHourly                   int64
	UsageErrorHourly                   int64
	UsageLatencyHourly                 int64
	UsageStatsWeekly                   int64
	UsageModelWeekly                   int64
	UsageErrorWeekly                   int64
	UsageLatencyWeekly                 int64
	UsageStatsMonthly                  int64
	UsageModelMonthly                  int64
	UsageErrorMonthly                  int64
	UsageLatencyMonthly                int64
	AuthorizationTeamUsageSummaryDaily int64
	AuthorizationTeamUsageRangeWindows int64
	AuthorizationUserUsageSummaryDaily int64
	AuthorizationUserUsageRangeWindows int64
	UsageRankSnapshots                 int64
	UsageOverviewSummaryWindows        int64
	UsageOverviewTrendWindows          int64
	UsageModelRankWindows              int64
	UsageErrorRankWindows              int64
	AIPerformanceSummaryWindows        int64
	UsageQuotaHourlyWindows            int64
	UsageScopeRangeWindows             int64
	ClientIpUsageRangeWindows          int64
	ClientIpRangeWindowDirtyIps        int64
	ClientIpAccountStatsDaily          int64
	ClientIpAccountUsageRangeWindows   int64
	ClientIpAccountRangeWindowDirtyIps int64
	AccountUsageSnapshots              int64
}

// Sum mirrors sumNumbers over the stats-writer result; the Postgres
// dispatch loop compares it against the full-batch threshold of 1.
func (c UsageStatsRetentionCounts) Sum() int64 {
	var sum int64
	sum += c.AccountQualityMinuteStats
	sum += c.AccountHealthHourly
	sum += c.UsageStatsMinute
	sum += c.UsageModelMinute
	sum += c.UsageErrorMinute
	sum += c.UsageLatencyMinute
	sum += c.UsageStatsDaily
	sum += c.UsageModelDaily
	sum += c.UsageErrorDaily
	sum += c.UsageLatencyDaily
	sum += c.UsageStatsHourly
	sum += c.UsageModelHourly
	sum += c.UsageErrorHourly
	sum += c.UsageLatencyHourly
	sum += c.UsageStatsWeekly
	sum += c.UsageModelWeekly
	sum += c.UsageErrorWeekly
	sum += c.UsageLatencyWeekly
	sum += c.UsageStatsMonthly
	sum += c.UsageModelMonthly
	sum += c.UsageErrorMonthly
	sum += c.UsageLatencyMonthly
	sum += c.AuthorizationTeamUsageSummaryDaily
	sum += c.AuthorizationTeamUsageRangeWindows
	sum += c.AuthorizationUserUsageSummaryDaily
	sum += c.AuthorizationUserUsageRangeWindows
	sum += c.UsageRankSnapshots
	sum += c.UsageOverviewSummaryWindows
	sum += c.UsageOverviewTrendWindows
	sum += c.UsageModelRankWindows
	sum += c.UsageErrorRankWindows
	sum += c.AIPerformanceSummaryWindows
	sum += c.UsageQuotaHourlyWindows
	sum += c.UsageScopeRangeWindows
	sum += c.ClientIpUsageRangeWindows
	sum += c.ClientIpRangeWindowDirtyIps
	sum += c.ClientIpAccountStatsDaily
	sum += c.ClientIpAccountUsageRangeWindows
	sum += c.ClientIpAccountRangeWindowDirtyIps
	sum += c.AccountUsageSnapshots
	return sum
}

// SystemMetricsRetentionCounts mirrors SystemMetricsRetentionCleanupResult,
// the stats-writer result of cleanup_system_metrics_retention.
type SystemMetricsRetentionCounts struct {
	SystemMetricsSamples         int64
	SystemMetricsHourly          int64
	SystemMetricsTrendWindows    int64
	ProcessEventLoopSamples      int64
	ProcessEventLoopHourly       int64
	ProcessEventLoopTrendWindows int64
}

// Sum mirrors sumNumbers over the system-metrics result.
func (c SystemMetricsRetentionCounts) Sum() int64 {
	var sum int64
	sum += c.SystemMetricsSamples
	sum += c.SystemMetricsHourly
	sum += c.SystemMetricsTrendWindows
	sum += c.ProcessEventLoopSamples
	sum += c.ProcessEventLoopHourly
	sum += c.ProcessEventLoopTrendWindows
	return sum
}
