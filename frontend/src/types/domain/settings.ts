export interface SystemSettings {
  gatewayTextRawBodyLimitMegabytes: number
  systemApiRateLimitEnabled: boolean
  systemApiRateLimitIpReadPerMinute: number
  systemApiRateLimitIpReadBurstPer10Seconds: number
  systemApiRateLimitIpWritePerMinute: number
  systemApiRateLimitIpWriteBurstPer10Seconds: number
  systemApiRateLimitUserReadPerMinute: number
  systemApiRateLimitUserWritePerMinute: number
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamCircuitBreakerEnabled: boolean
  streamRequestTimeoutSeconds: number
  streamIdleTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  operationLogEnabled: boolean
  operationLogRetentionDays: number
  operationLogMaxChangesPerRecord: number
  statsAggregationIntervalSeconds: number
  statsAggregationBatchSize: number
  statsAggregationMaxBatchesPerRun: number
  groupAccountStatsRefreshIntervalSeconds: number
  systemMetricsSampleIntervalSeconds: number
  tableMonitorMaxTablesPerRun: number
  accountQualityRefreshIntervalSeconds: number
  accountQualityWindowMinutes: number
  accountTestTaskConcurrency: number
  accountHealthCheckIntervalHours: number
  accountHealthCheckJitterMinutes: number
  accountHealthCheckBatchSize: number
  accountHealthCheckFailureThreshold: number
  cooldownAccountRetestIntervalSeconds: number
  cooldownAccountRetestBatchSize: number
  cooldownAccountRetestMaxBackoffHours: number
  cooldownAccountRetestLongTermIntervalHours: number
  oauthAccessTokenRefreshIntervalSeconds: number
  oauthAccessTokenRefreshLeadSeconds: number
  oauthAccessTokenRefreshBatchSize: number
  oauthAccessTokenRefreshRetryBackoffSeconds: number
  modelCheckRetentionDays: number
  usageRecordRetentionDays: number
  usageStatsTimezone: string
  usageStatsMinuteRetentionHours: number
  usageStatsHourlyRetentionDays: number
  usageStatsDailyRetentionDays: number
  usageStatsWeeklyRetentionWeeks: number
  usageStatsMonthlyRetentionMonths: number
  usageRankSnapshotRetentionDays: number
  systemMetricsRetentionDays: number
  systemMetricsHourlyRetentionDays: number
  dataRetentionCleanupIntervalMinutes: number
  dataRetentionCleanupBatchSize: number
  dataRetentionCleanupMaxBatchesPerRun: number
}

export type SystemSettingsPatch = Partial<SystemSettings>

export interface GlobalSettings {
  appName: string
  appIcon: string
}
