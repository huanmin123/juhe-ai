package systemsettings

import "sort"

const UsageStatsTimezoneKey = "usageStatsTimezone"

type ValueKind string

const (
	ValueKindInteger  ValueKind = "integer"
	ValueKindDecimal  ValueKind = "decimal"
	ValueKindTimezone ValueKind = "timezone"
)

type Definition struct {
	Key            string
	Kind           ValueKind
	Minimum        int
	Maximum        int
	DecimalMinimum float64
	DecimalMaximum float64
}

var definitions = []Definition{
	{Key: "gatewayTextRawBodyLimitMegabytes", Kind: ValueKindInteger, Minimum: 1, Maximum: 64},
	{Key: "systemApiRateLimitIpReadPerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitIpReadBurstPer10Seconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitIpWritePerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitIpWriteBurstPer10Seconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitUserReadPerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitUserWritePerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "defaultTemporaryUnschedulableMinutes", Kind: ValueKindInteger, Minimum: 1, Maximum: 1440},
	{Key: "temporaryUnschedulableRetryIntervalSeconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 3600},
	{Key: "temporaryUnschedulableRetryAttempts", Kind: ValueKindInteger, Minimum: 0, Maximum: 10},
	{Key: "textFirstResponseTimeoutSeconds", Kind: ValueKindInteger, Minimum: 10, Maximum: 3600},
	{Key: "textStreamIdleTimeoutSeconds", Kind: ValueKindInteger, Minimum: 1, Maximum: 3600},
	{Key: "textUncommittedAttemptMaxLifetimeSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 86400},
	{Key: "imageFirstResponseTimeoutSeconds", Kind: ValueKindInteger, Minimum: 10, Maximum: 3600},
	{Key: "imageStreamIdleTimeoutSeconds", Kind: ValueKindInteger, Minimum: 1, Maximum: 3600},
	{Key: "imageUncommittedAttemptMaxLifetimeSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 86400},
	{Key: "chatImageGenerationTotalTimeoutSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 86400},
	{Key: "noAvailableAccountWaitTimeoutSeconds", Kind: ValueKindInteger, Minimum: 10, Maximum: 3600},
	{Key: "streamFailureThresholdCount", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "streamFailureThresholdWindowMinutes", Kind: ValueKindInteger, Minimum: 1, Maximum: 1440},
	{Key: "operationLogRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 3650},
	{Key: "operationLogMaxChangesPerRecord", Kind: ValueKindInteger, Minimum: 1, Maximum: 500},
	{Key: "statsAggregationIntervalSeconds", Kind: ValueKindInteger, Minimum: 5, Maximum: 3600},
	{Key: "statsAggregationBatchSize", Kind: ValueKindInteger, Minimum: 100, Maximum: 10000},
	{Key: "statsAggregationMaxBatchesPerRun", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "usageHotWindowRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 3600},
	{Key: "groupAccountStatsRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 5, Maximum: 3600},
	{Key: "systemMetricsSampleIntervalSeconds", Kind: ValueKindInteger, Minimum: 5, Maximum: 3600},
	{Key: "tableMonitorMaxTablesPerRun", Kind: ValueKindInteger, Minimum: 0, Maximum: 100},
	{Key: "accountQualityRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 3600},
	{Key: "accountQualityWindowMinutes", Kind: ValueKindInteger, Minimum: 1, Maximum: 60},
	{Key: "accountTestTaskConcurrency", Kind: ValueKindInteger, Minimum: 1, Maximum: 1000},
	{Key: "accountHealthCheckIntervalHours", Kind: ValueKindInteger, Minimum: 1, Maximum: 168},
	{Key: "accountHealthCheckJitterMinutes", Kind: ValueKindInteger, Minimum: 0, Maximum: 1440},
	{Key: "accountHealthCheckBatchSize", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "accountHealthCheckFailureThreshold", Kind: ValueKindInteger, Minimum: 1, Maximum: 10},
	{Key: "cooldownAccountRetestIntervalSeconds", Kind: ValueKindInteger, Minimum: 1, Maximum: 3600},
	{Key: "cooldownAccountRetestBatchSize", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "cooldownAccountRetestMaxBackoffHours", Kind: ValueKindInteger, Minimum: 1, Maximum: 720},
	{Key: "oauthAccessTokenRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 10, Maximum: 3600},
	{Key: "oauthAccessTokenRefreshLeadSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 86400},
	{Key: "oauthAccessTokenRefreshBatchSize", Kind: ValueKindInteger, Minimum: 1, Maximum: 200},
	{Key: "oauthAccessTokenRefreshRetryBackoffSeconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 86400},
	{Key: "modelCheckRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 365},
	{Key: "runtimeLogIndexRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 90},
	{Key: "publicApiLogRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 365},
	{Key: "usageRecordRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 180},
	{Key: UsageStatsTimezoneKey, Kind: ValueKindTimezone},
	{Key: "usageStatsMinuteRetentionHours", Kind: ValueKindInteger, Minimum: 1, Maximum: 336},
	{Key: "usageStatsHourlyRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 180},
	{Key: "usageStatsDailyRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 800},
	{Key: "usageStatsWeeklyRetentionWeeks", Kind: ValueKindInteger, Minimum: 1, Maximum: 260},
	{Key: "usageStatsMonthlyRetentionMonths", Kind: ValueKindInteger, Minimum: 1, Maximum: 60},
	{Key: "usageRankSnapshotRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 365},
	{Key: "systemMetricsRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 7},
	{Key: "systemMetricsHourlyRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 30},
}

var (
	definitionsByKey = buildDefinitionsByKey()
	sortedKeys       = buildSortedKeys()
)

func Definitions() []Definition {
	return append([]Definition(nil), definitions...)
}

func Keys() []string {
	keys := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		keys = append(keys, definition.Key)
	}
	return keys
}

func SortedKeys() []string {
	return append([]string(nil), sortedKeys...)
}

func DefinitionFor(key string) (Definition, bool) {
	definition, ok := definitionsByKey[key]
	return definition, ok
}

func IsKey(key string) bool {
	_, ok := definitionsByKey[key]
	return ok
}

func buildDefinitionsByKey() map[string]Definition {
	output := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		if _, exists := output[definition.Key]; exists {
			panic("duplicate system setting definition: " + definition.Key)
		}
		output[definition.Key] = definition
	}
	return output
}

func buildSortedKeys() []string {
	keys := Keys()
	sort.Strings(keys)
	return keys
}
