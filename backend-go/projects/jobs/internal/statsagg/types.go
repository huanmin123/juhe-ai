package statsagg

// GlobalStatsSystemAccountID 与 GlobalStatsScopeID mirrors
// usage-stats-types.ts GLOBAL_STATS_SYSTEM_ACCOUNT_ID / GLOBAL_STATS_SCOPE_ID。
const (
	GlobalStatsSystemAccountID = "global"
	GlobalStatsScopeID         = "global"
)

// UsageStatsRecordRow mirrors usage-stats-types.ts UsageStatsRecordRow。
// 数值列以 float64 承载（对齐 Node Number 语义），nullable 列用指针。
type UsageStatsRecordRow struct {
	ID                               string
	SystemAccountID                  string
	TraceID                          string
	TrafficSource                    string
	ClientIP                         *string
	APIKeyID                         *string
	GroupID                          *string
	AccountID                        *string
	Endpoint                         *string
	ProviderCode                     *string
	ProviderProtocolProfileID        *string
	Model                            *string
	StatusCode                       *float64
	Success                          float64
	FailureAttribution               *string
	FirstTokenMs                     *float64
	DurationMs                       *float64
	InputTokens                      *float64
	OutputTokens                     *float64
	CacheReadTokens                  *float64
	CacheReadCostUsd                 *float64
	CacheWriteTokens                 *float64
	CacheWrite1hTokens               *float64
	CacheWriteCostUsd                *float64
	ThinkingTokens                   *float64
	InputImageTokens                 *float64
	OutputImageTokens                *float64
	CostUsd                          *float64
	ErrorCode                        *string
	ErrorMessage                     *string
	AccountOwnerSystemAccountID      *string
	GroupOwnerSystemAccountID        *string
	AccountAccessType                *string
	GroupAccessType                  *string
	AccountAuthorizationID           *string
	AccountAuthorizationSourceType   *string
	AccountAuthorizationSourceTeamID *string
	GroupAuthorizationID             *string
	GroupAuthorizationSourceType     *string
	GroupAuthorizationSourceTeamID   *string
	CreatedAt                        string

	// SourceShardKey 由聚合游标写入（SQLite 分片模式），PG 单库为空。
	SourceShardKey string
}

// UsageStatsAccumulator mirrors usage-stats-types.ts UsageStatsAccumulator。
type UsageStatsAccumulator struct {
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
	LastUsedAt         string // '' 表示 Node undefined
	LastErrorAt        string // '' 表示 Node undefined
}

// UsageStatsEntry mirrors usage-stats-types.ts UsageStatsEntry。
type UsageStatsEntry struct {
	SystemAccountID string
	ScopeType       string
	ScopeID         string
	Accumulator     UsageStatsAccumulator
}

// UsageStatsTimeKeys mirrors usage-stats-time-buckets.ts UsageStatsTimeKeys。
type UsageStatsTimeKeys struct {
	StatMinute string
	StatHour   string
	StatDate   string
	StatWeek   string
	StatMonth  string
}

// timeValue selects UsageStatsTimeKeys 按 bucket valueKey 取值，
// 对齐 Node `timeKeys[bucket.valueKey]`。
func (k UsageStatsTimeKeys) timeValue(valueKey string) string {
	switch valueKey {
	case "statMinute":
		return k.StatMinute
	case "statHour":
		return k.StatHour
	case "statDate":
		return k.StatDate
	case "statWeek":
		return k.StatWeek
	case "statMonth":
		return k.StatMonth
	}
	return ""
}

// TimeBucketDefinition mirrors usage-stats-time-buckets.ts
// UsageStatsTimeBucketDefinition。
type TimeBucketDefinition struct {
	TableName  string
	ColumnName string
	ValueKey   string
}

// usageStatsTimeBuckets / usageModelTimeBuckets / usageErrorTimeBuckets /
// usageLatencyTimeBuckets 逐项对齐 usage-stats-time-buckets.ts 的四个数组。
var (
	usageStatsTimeBuckets = []TimeBucketDefinition{
		{"usage_stats_minute", "stat_minute", "statMinute"},
		{"usage_stats_hourly", "stat_hour", "statHour"},
		{"usage_stats_daily", "stat_date", "statDate"},
		{"usage_stats_weekly", "stat_week", "statWeek"},
		{"usage_stats_monthly", "stat_month", "statMonth"},
	}
	usageModelTimeBuckets = []TimeBucketDefinition{
		{"usage_model_minute", "stat_minute", "statMinute"},
		{"usage_model_hourly", "stat_hour", "statHour"},
		{"usage_model_daily", "stat_date", "statDate"},
		{"usage_model_weekly", "stat_week", "statWeek"},
		{"usage_model_monthly", "stat_month", "statMonth"},
	}
	usageErrorTimeBuckets = []TimeBucketDefinition{
		{"usage_error_minute", "stat_minute", "statMinute"},
		{"usage_error_hourly", "stat_hour", "statHour"},
		{"usage_error_daily", "stat_date", "statDate"},
		{"usage_error_weekly", "stat_week", "statWeek"},
		{"usage_error_monthly", "stat_month", "statMonth"},
	}
	usageLatencyTimeBuckets = []TimeBucketDefinition{
		{"usage_latency_minute", "stat_minute", "statMinute"},
		{"usage_latency_hourly", "stat_hour", "statHour"},
		{"usage_latency_daily", "stat_date", "statDate"},
		{"usage_latency_weekly", "stat_week", "statWeek"},
		{"usage_latency_monthly", "stat_month", "statMonth"},
	}
)

// UsageStatsCursorSafetyDelaySeconds mirrors usage-stats.repository.ts
// usageStatsCursorSafetyDelaySeconds = 15。
const UsageStatsCursorSafetyDelaySeconds = 15

// EmptySourceWatermark mirrors USAGE_RANK_SNAPSHOT_EMPTY_SOURCE_WATERMARK。
const EmptySourceWatermark = "0001-01-01T00:00:00.000Z"

// LegacyEmptySourceWatermark mirrors USAGE_RANK_SNAPSHOT_LEGACY_EMPTY_SOURCE_WATERMARK。
const LegacyEmptySourceWatermark = "0000-00-00T00:00:00.000Z"

// RankSnapshotJobStateScopeType/ID 与 SourceVersionScopeType/ID 对齐
// usage-stats.repository.ts 的常量。
const (
	RankSnapshotJobStateScopeType      = "global"
	RankSnapshotJobStateScopeID        = ""
	RankSnapshotSourceVersionScopeType = "usage_rank_snapshot_source_version"
	RankSnapshotSourceVersionScopeID   = ""
)
