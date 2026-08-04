export interface SystemSettings {
  gatewayTextRawBodyLimitMegabytes: number
  accountCircuitConfirmationFailuresRequired: number
  gatewayUserRequestLimitPerMinute: number
  gatewayUserRequestLimitPerDay: number
  gatewayUserRequestLimitPerWeek: number
  gatewayUserRequestLimitPerMonth: number
  systemApiRateLimitIpReadPerMinute: number
  systemApiRateLimitIpReadBurstPer10Seconds: number
  systemApiRateLimitIpWritePerMinute: number
  systemApiRateLimitIpWriteBurstPer10Seconds: number
  systemApiRateLimitUserReadPerMinute: number
  systemApiRateLimitUserWritePerMinute: number
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  textFirstResponseTimeoutSeconds: number
  textStreamIdleTimeoutSeconds: number
  textUncommittedAttemptMaxLifetimeSeconds: number
  imageFirstResponseTimeoutSeconds: number
  imageStreamIdleTimeoutSeconds: number
  imageUncommittedAttemptMaxLifetimeSeconds: number
  imageRequestWallTimeoutSeconds: number
  chatImageGenerationTotalTimeoutSeconds: number
  noAvailableAccountWaitTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  operationLogRetentionDays: number
  operationLogMaxChangesPerRecord: number
  statsAggregationIntervalSeconds: number
  statsAggregationBatchSize: number
  statsAggregationMaxBatchesPerRun: number
  usageHotWindowRefreshIntervalSeconds: number
  groupAccountStatsRefreshIntervalSeconds: number
  systemMetricsSampleIntervalSeconds: number
  tableMonitorMaxTablesPerRun: number
  accountQualityRefreshIntervalSeconds: number
  accountQualityWindowMinutes: number
  accountHealthCheckIntervalHours: number
  accountHealthCheckJitterMinutes: number
  accountHealthCheckFailureThreshold: number
  cooldownAccountRetestIntervalSeconds: number
  cooldownAccountRetestMaxBackoffHours: number
  oauthAccessTokenRefreshIntervalSeconds: number
  oauthAccessTokenRefreshLeadSeconds: number
  oauthAccessTokenRefreshBatchSize: number
  oauthAccessTokenRefreshRetryBackoffSeconds: number
  modelCheckRetentionDays: number
  runtimeLogIndexRetentionDays: number
  publicApiLogRetentionDays: number
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
}

export type SystemSettingsPatch = Partial<SystemSettings>

export interface GlobalSettings {
  appName: string
  appIcon: string
}
