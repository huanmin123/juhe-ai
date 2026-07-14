-- name: LockManagementGlobalSettings :many
SELECT key, value_json
FROM juhe_business.global_settings
WHERE key IN ('appName', 'appIcon')
ORDER BY key ASC
FOR UPDATE;

-- name: UpdateManagementGlobalSetting :one
UPDATE juhe_business.global_settings
SET
  value_json = sqlc.arg(value_json)::text,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE key = sqlc.arg(key)::text
RETURNING key, value_json;

-- name: ListManagementSystemSettings :many
SELECT key, value_json
FROM juhe_business.system_settings
WHERE system_account_id = 'sys_admin'
  AND key IN (
    'gatewayTextRawBodyLimitMegabytes',
    'systemApiRateLimitIpReadPerMinute',
    'systemApiRateLimitIpReadBurstPer10Seconds',
    'systemApiRateLimitIpWritePerMinute',
    'systemApiRateLimitIpWriteBurstPer10Seconds',
    'systemApiRateLimitUserReadPerMinute',
    'systemApiRateLimitUserWritePerMinute',
    'defaultTemporaryUnschedulableMinutes',
    'temporaryUnschedulableRetryIntervalSeconds',
    'temporaryUnschedulableRetryAttempts',
    'streamRequestTimeoutSeconds',
    'streamIdleTimeoutSeconds',
    'streamClientTotalWaitTimeoutSeconds',
    'streamMaxLifetimeSeconds',
    'streamFailureThresholdCount',
    'streamFailureThresholdWindowMinutes',
    'operationLogRetentionDays',
    'operationLogMaxChangesPerRecord',
    'statsAggregationIntervalSeconds',
    'statsAggregationBatchSize',
    'statsAggregationMaxBatchesPerRun',
    'usageHotWindowRefreshIntervalSeconds',
    'groupAccountStatsRefreshIntervalSeconds',
    'systemMetricsSampleIntervalSeconds',
    'tableMonitorMaxTablesPerRun',
    'accountQualityRefreshIntervalSeconds',
    'accountQualityWindowMinutes',
    'accountTestTaskConcurrency',
    'accountHealthCheckIntervalHours',
    'accountHealthCheckJitterMinutes',
    'accountHealthCheckBatchSize',
    'accountHealthCheckFailureThreshold',
    'cooldownAccountRetestIntervalSeconds',
    'cooldownAccountRetestBatchSize',
    'cooldownAccountRetestMaxBackoffHours',
    'oauthAccessTokenRefreshIntervalSeconds',
    'oauthAccessTokenRefreshLeadSeconds',
    'oauthAccessTokenRefreshBatchSize',
    'oauthAccessTokenRefreshRetryBackoffSeconds',
    'modelCheckRetentionDays',
    'runtimeLogIndexRetentionDays',
    'publicApiLogRetentionDays',
    'usageRecordRetentionDays',
    'usageStatsTimezone',
    'usageStatsMinuteRetentionHours',
    'usageStatsHourlyRetentionDays',
    'usageStatsDailyRetentionDays',
    'usageStatsWeeklyRetentionWeeks',
    'usageStatsMonthlyRetentionMonths',
    'usageRankSnapshotRetentionDays',
    'systemMetricsRetentionDays',
    'systemMetricsHourlyRetentionDays'
  )
ORDER BY key ASC;

-- name: LockManagementSystemSettings :many
SELECT key, value_json
FROM juhe_business.system_settings
WHERE system_account_id = 'sys_admin'
  AND key IN (
    'gatewayTextRawBodyLimitMegabytes',
    'systemApiRateLimitIpReadPerMinute',
    'systemApiRateLimitIpReadBurstPer10Seconds',
    'systemApiRateLimitIpWritePerMinute',
    'systemApiRateLimitIpWriteBurstPer10Seconds',
    'systemApiRateLimitUserReadPerMinute',
    'systemApiRateLimitUserWritePerMinute',
    'defaultTemporaryUnschedulableMinutes',
    'temporaryUnschedulableRetryIntervalSeconds',
    'temporaryUnschedulableRetryAttempts',
    'streamRequestTimeoutSeconds',
    'streamIdleTimeoutSeconds',
    'streamClientTotalWaitTimeoutSeconds',
    'streamMaxLifetimeSeconds',
    'streamFailureThresholdCount',
    'streamFailureThresholdWindowMinutes',
    'operationLogRetentionDays',
    'operationLogMaxChangesPerRecord',
    'statsAggregationIntervalSeconds',
    'statsAggregationBatchSize',
    'statsAggregationMaxBatchesPerRun',
    'usageHotWindowRefreshIntervalSeconds',
    'groupAccountStatsRefreshIntervalSeconds',
    'systemMetricsSampleIntervalSeconds',
    'tableMonitorMaxTablesPerRun',
    'accountQualityRefreshIntervalSeconds',
    'accountQualityWindowMinutes',
    'accountTestTaskConcurrency',
    'accountHealthCheckIntervalHours',
    'accountHealthCheckJitterMinutes',
    'accountHealthCheckBatchSize',
    'accountHealthCheckFailureThreshold',
    'cooldownAccountRetestIntervalSeconds',
    'cooldownAccountRetestBatchSize',
    'cooldownAccountRetestMaxBackoffHours',
    'oauthAccessTokenRefreshIntervalSeconds',
    'oauthAccessTokenRefreshLeadSeconds',
    'oauthAccessTokenRefreshBatchSize',
    'oauthAccessTokenRefreshRetryBackoffSeconds',
    'modelCheckRetentionDays',
    'runtimeLogIndexRetentionDays',
    'publicApiLogRetentionDays',
    'usageRecordRetentionDays',
    'usageStatsTimezone',
    'usageStatsMinuteRetentionHours',
    'usageStatsHourlyRetentionDays',
    'usageStatsDailyRetentionDays',
    'usageStatsWeeklyRetentionWeeks',
    'usageStatsMonthlyRetentionMonths',
    'usageRankSnapshotRetentionDays',
    'systemMetricsRetentionDays',
    'systemMetricsHourlyRetentionDays'
  )
ORDER BY key ASC
FOR UPDATE;

-- name: UpdateManagementSystemSetting :one
UPDATE juhe_business.system_settings
SET
  value_json = sqlc.arg(value_json)::text,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE system_account_id = 'sys_admin'
  AND key = sqlc.arg(key)::text
RETURNING key, value_json;
