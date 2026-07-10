-- +goose Up
-- +goose ENVSUB ON
INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES
  ('sys_admin', 'gatewayTextRawBodyLimitMegabytes', '16', now()),
  ('sys_admin', 'systemApiRateLimitIpReadPerMinute', '600', now()),
  ('sys_admin', 'systemApiRateLimitIpReadBurstPer10Seconds', '120', now()),
  ('sys_admin', 'systemApiRateLimitIpWritePerMinute', '180', now()),
  ('sys_admin', 'systemApiRateLimitIpWriteBurstPer10Seconds', '40', now()),
  ('sys_admin', 'systemApiRateLimitUserReadPerMinute', '300', now()),
  ('sys_admin', 'systemApiRateLimitUserWritePerMinute', '120', now()),
  ('sys_admin', 'defaultTemporaryUnschedulableMinutes', '2', now()),
  ('sys_admin', 'temporaryUnschedulableRetryIntervalSeconds', '3', now()),
  ('sys_admin', 'temporaryUnschedulableRetryAttempts', '3', now()),
  ('sys_admin', 'streamRequestTimeoutSeconds', '120', now()),
  ('sys_admin', 'streamIdleTimeoutSeconds', '30', now()),
  ('sys_admin', 'streamClientTotalWaitTimeoutSeconds', '270', now()),
  ('sys_admin', 'streamMaxLifetimeSeconds', '1800', now()),
  ('sys_admin', 'streamFailureThresholdCount', '3', now()),
  ('sys_admin', 'streamFailureThresholdWindowMinutes', '5', now()),
  ('sys_admin', 'operationLogRetentionDays', '365', now()),
  ('sys_admin', 'operationLogMaxChangesPerRecord', '100', now()),
  ('sys_admin', 'statsAggregationIntervalSeconds', '60', now()),
  ('sys_admin', 'statsAggregationBatchSize', '2000', now()),
  ('sys_admin', 'statsAggregationMaxBatchesPerRun', '5', now()),
  ('sys_admin', 'usageHotWindowRefreshIntervalSeconds', '600', now()),
  ('sys_admin', 'groupAccountStatsRefreshIntervalSeconds', '60', now()),
  ('sys_admin', 'systemMetricsSampleIntervalSeconds', '30', now()),
  ('sys_admin', 'tableMonitorMaxTablesPerRun', '4', now()),
  ('sys_admin', 'accountQualityRefreshIntervalSeconds', '600', now()),
  ('sys_admin', 'accountQualityWindowMinutes', '10', now()),
  ('sys_admin', 'accountTestTaskConcurrency', '100', now()),
  ('sys_admin', 'accountHealthCheckIntervalHours', '12', now()),
  ('sys_admin', 'accountHealthCheckJitterMinutes', '120', now()),
  ('sys_admin', 'accountHealthCheckBatchSize', '20', now()),
  ('sys_admin', 'accountHealthCheckFailureThreshold', '3', now()),
  ('sys_admin', 'cooldownAccountRetestIntervalSeconds', '3', now()),
  ('sys_admin', 'cooldownAccountRetestBatchSize', '10', now()),
  ('sys_admin', 'cooldownAccountRetestMaxBackoffHours', '12', now()),
  ('sys_admin', 'cooldownAccountRetestLongTermIntervalHours', '1', now()),
  ('sys_admin', 'oauthAccessTokenRefreshIntervalSeconds', '60', now()),
  ('sys_admin', 'oauthAccessTokenRefreshLeadSeconds', '300', now()),
  ('sys_admin', 'oauthAccessTokenRefreshBatchSize', '20', now()),
  ('sys_admin', 'oauthAccessTokenRefreshRetryBackoffSeconds', '300', now()),
  ('sys_admin', 'modelCheckRetentionDays', '30', now()),
  ('sys_admin', 'runtimeLogIndexRetentionDays', '14', now()),
  ('sys_admin', 'publicApiLogRetentionDays', '30', now()),
  ('sys_admin', 'usageRecordRetentionDays', '30', now()),
  (
    'sys_admin',
    'usageStatsTimezone',
    to_jsonb(
      COALESCE(
        NULLIF('${JUHE_AI_USAGE_STATS_TIMEZONE:-}', ''),
        NULLIF(current_setting('TimeZone', true), ''),
        'UTC'
      )
    )::text,
    now()
  ),
  ('sys_admin', 'usageStatsMinuteRetentionHours', '48', now()),
  ('sys_admin', 'usageStatsHourlyRetentionDays', '60', now()),
  ('sys_admin', 'usageStatsDailyRetentionDays', '400', now()),
  ('sys_admin', 'usageStatsWeeklyRetentionWeeks', '104', now()),
  ('sys_admin', 'usageStatsMonthlyRetentionMonths', '24', now()),
  ('sys_admin', 'usageRankSnapshotRetentionDays', '30', now()),
  ('sys_admin', 'systemMetricsRetentionDays', '7', now()),
  ('sys_admin', 'systemMetricsHourlyRetentionDays', '30', now())
ON CONFLICT (system_account_id, key) DO NOTHING;
-- +goose ENVSUB OFF

-- +goose Down
-- no-op: system settings are part of the current management settings contract.
