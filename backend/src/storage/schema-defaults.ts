export const DEFAULT_OPENAI_GROUP = {
  id: 'grp_default_openai_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 OpenAI 分组',
  providerCode: 'openai',
  description: ''
} as const

export const DEFAULT_GLOBAL_SETTINGS = [
  ['appName', '聚合 AI'],
  ['appIcon', '/brand-icon.svg']
] as const

export const OPENAI_PROVIDER_SEED = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  description: '当前内置供应商，支持 OAuth 与 API Key 两种账户接入方式',
  enabled: 1,
  baseUrl: 'https://api.openai.com/v1',
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
  ['accountQualityRefreshIntervalSeconds', 600],
  ['accountQualityWindowMinutes', 10],
  ['cooldownAccountRetestEnabled', true],
  ['cooldownAccountRetestIntervalSeconds', 60],
  ['cooldownAccountRetestBatchSize', 10],
  ['cooldownAccountRetestModel', 'gpt-5.5'],
  ['oauthAccessTokenRefreshIntervalSeconds', 60],
  ['oauthAccessTokenRefreshLeadSeconds', 300],
  ['oauthAccessTokenRefreshBatchSize', 20],
  ['oauthAccessTokenRefreshRetryBackoffSeconds', 300],
  ['usageRecordRetentionDays', 7],
  ['usageStatsDailyRetentionDays', 30],
  ['usageStatsHourlyRetentionDays', 30],
  ['systemMetricsRetentionDays', 7],
  ['systemMetricsHourlyRetentionDays', 30],
  ['dataRetentionCleanupBatchSize', 10000],
  ['dataRetentionCleanupMaxBatchesPerRun', 10]
] as const
