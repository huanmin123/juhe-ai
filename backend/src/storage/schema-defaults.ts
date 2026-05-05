export const DEFAULT_OPENAI_GROUP = {
  id: 'grp_default_openai_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 OpenAI 分组',
  providerCode: 'openai',
  description: '第一期默认分组'
} as const

export const DEFAULT_GLOBAL_SETTINGS = [
  ['appName', '聚合 AI'],
  ['appIcon', '/brand-icon.svg']
] as const

export const OPENAI_PROVIDER_SEED = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  description: '第一阶段内置供应商，支持 OAuth 与 API Key 两种账户接入方式',
  enabled: 1,
  baseUrl: 'https://api.openai.com/v1',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
} as const

export const DEFAULT_SYSTEM_SETTINGS = [
  ['appName', '聚合 AI'],
  ['appIcon', '/brand-icon.svg'],
  ['defaultTemporaryUnschedulableMinutes', 5],
  ['temporaryUnschedulableRetryIntervalSeconds', 3],
  ['temporaryUnschedulableRetryAttempts', 3],
  ['streamCircuitBreakerEnabled', true],
  ['streamRequestTimeoutSeconds', 180],
  ['streamIdleTimeoutSeconds', 60],
  ['streamFailureThresholdCount', 3],
  ['streamFailureThresholdWindowMinutes', 10],
  ['auditLogEnabled', true],
  ['auditLogSuccessSampleRate', 0.1],
  ['auditLogFlushIntervalSeconds', 5],
  ['auditLogBatchSize', 50],
  ['auditLogQueueMaxItems', 1000],
  ['auditLogQueueMaxBytesMb', 256],
  ['auditLogActiveCaptureMaxBytesMb', 64],
  ['auditLogRetentionDays', 7],
  ['usageRecordRetentionDays', 7],
  ['usageStatsDailyRetentionDays', 30],
  ['usageStatsHourlyRetentionDays', 30],
  ['systemMetricsRetentionDays', 7],
  ['systemMetricsHourlyRetentionDays', 30],
  ['dataRetentionCleanupBatchSize', 10000],
  ['dataRetentionCleanupMaxBatchesPerRun', 10]
] as const
