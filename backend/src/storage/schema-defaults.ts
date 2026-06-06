export const DEFAULT_OPENAI_GROUP = {
  id: 'grp_default_openai_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 OpenAI 分组',
  providerCode: 'openai',
  description: ''
} as const

export const DEFAULT_GLOBAL_SETTINGS = [
  ['appName', '聚合 AI'],
  ['appIcon', '/__aisys__/brand-icon.svg']
] as const

export const OPENAI_PROVIDER_SEED = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  description: '当前内置供应商，支持 OAuth 与 API Key 两种账户接入方式',
  enabled: 1,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: 'gpt-5.5',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
} as const

export const DEFAULT_SYSTEM_SETTINGS = [
  ['defaultTemporaryUnschedulableMinutes', 5],
  ['temporaryUnschedulableRetryIntervalSeconds', 3],
  ['temporaryUnschedulableRetryAttempts', 3],
  ['streamCircuitBreakerEnabled', true],
  ['streamRequestTimeoutSeconds', 180],
  ['streamIdleTimeoutSeconds', 60],
  ['streamFailureThresholdCount', 3],
  ['streamFailureThresholdWindowMinutes', 10],
  ['operationLogEnabled', true],
  ['operationLogRetentionDays', 365],
  ['operationLogMaxChangesPerRecord', 100],
  ['statsAggregationIntervalSeconds', 60],
  ['statsAggregationBatchSize', 2000],
  ['statsAggregationMaxBatchesPerRun', 5],
  ['groupAccountStatsRefreshIntervalSeconds', 60],
  ['systemMetricsSampleIntervalSeconds', 30],
  ['tableMonitorMaxTablesPerRun', 4],
  ['accountQualityRefreshIntervalSeconds', 600],
  ['accountQualityWindowMinutes', 10],
  ['cooldownAccountRetestIntervalSeconds', 3],
  ['cooldownAccountRetestBatchSize', 10],
  ['cooldownAccountRetestMaxBackoffHours', 24],
  ['oauthAccessTokenRefreshIntervalSeconds', 60],
  ['oauthAccessTokenRefreshLeadSeconds', 300],
  ['oauthAccessTokenRefreshBatchSize', 20],
  ['oauthAccessTokenRefreshRetryBackoffSeconds', 300],
  ['modelCheckRetentionDays', 30],
  ['usageRecordRetentionDays', 7],
  ['usageStatsTimezone', Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'],
  ['usageStatsMinuteRetentionHours', 48],
  ['usageStatsHourlyRetentionDays', 60],
  ['usageStatsDailyRetentionDays', 400],
  ['usageStatsWeeklyRetentionWeeks', 104],
  ['usageStatsMonthlyRetentionMonths', 24],
  ['usageRankSnapshotRetentionDays', 30],
  ['systemMetricsRetentionDays', 7],
  ['systemMetricsHourlyRetentionDays', 30],
  ['dataRetentionCleanupBatchSize', 1000],
  ['dataRetentionCleanupMaxBatchesPerRun', 2]
] as const
