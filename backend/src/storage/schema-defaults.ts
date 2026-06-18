import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_MODELS_FAMILY,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  ANTHROPIC_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY
} from '../domain/provider-protocol.js'

export const DEFAULT_GPT_GROUP = {
  id: 'grp_default_gpt_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 GPT 分组',
  providerCode: GPT_VENDOR_CODE,
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  description: ''
} as const

export const DEFAULT_OPENAI_COMPATIBLE_GROUP = {
  id: 'grp_default_openai_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 OpenAI 兼容分组',
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  description: ''
} as const

export const DEFAULT_ANTHROPIC_GROUP = {
  id: 'grp_default_anthropic_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 Anthropic 分组',
  providerCode: ANTHROPIC_PROVIDER_CODE,
  providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  description: ''
} as const

export const DEFAULT_BUILT_IN_GROUPS = [
  DEFAULT_OPENAI_COMPATIBLE_GROUP,
  DEFAULT_GPT_GROUP,
  DEFAULT_ANTHROPIC_GROUP
] as const

export const DEFAULT_GLOBAL_SETTINGS = [
  ['appName', '聚合 AI'],
  ['appIcon', '/__aisys__/brand-icon.svg']
] as const

export const GPT_PROVIDER_SEED = {
  id: GPT_VENDOR_CODE,
  code: GPT_VENDOR_CODE,
  name: 'GPT',
  parentCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  description: 'GPT 官方供应商，继承通用 OpenAI-compatible 能力，并启用 OAuth、Codex Responses 等 GPT 专属能力',
  enabled: 1
} as const

export const OPENAI_COMPATIBLE_PROVIDER_SEED = {
  id: OPENAI_COMPATIBLE_PROVIDER_CODE,
  code: OPENAI_COMPATIBLE_PROVIDER_CODE,
  name: 'OpenAI 兼容',
  parentCode: null,
  description: '通用 OpenAI-compatible 供应商，用于接入兼容 OpenAI v1 协议的上游服务，默认只提供 API Key 透传能力',
  enabled: 1
} as const

export const ANTHROPIC_PROVIDER_SEED = {
  id: ANTHROPIC_PROVIDER_CODE,
  code: ANTHROPIC_PROVIDER_CODE,
  name: 'Anthropic',
  parentCode: null,
  description: 'Anthropic 官方供应商，当前支持官方 API Key 与 Anthropic Messages 原生协议直连',
  enabled: 1
} as const

export const OPENAI_PROTOCOL_SEED = {
  id: `${OPENAI_PROTOCOL_CODE}_${OPENAI_PROTOCOL_VERSION}`,
  code: OPENAI_PROTOCOL_CODE,
  version: OPENAI_PROTOCOL_VERSION,
  name: 'OpenAI v1',
  description: 'OpenAI-compatible v1 协议；接口族包含 Chat Completions 与 Responses',
  enabled: 1
} as const

export const ANTHROPIC_PROTOCOL_SEED = {
  id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}`,
  code: ANTHROPIC_PROTOCOL_CODE,
  version: ANTHROPIC_PROTOCOL_VERSION,
  name: 'Anthropic v1',
  description: 'Anthropic 官方 v1 协议；接口族包含 Messages、Models 与 Message Token Counting',
  enabled: 1
} as const

export const OPENAI_PROTOCOL_ENDPOINT_FAMILY_SEEDS = [
  {
    id: `${OPENAI_PROTOCOL_CODE}_${OPENAI_PROTOCOL_VERSION}_${OPENAI_CHAT_COMPLETIONS_FAMILY}`,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    code: OPENAI_CHAT_COMPLETIONS_FAMILY,
    name: 'Chat Completions',
    description: 'OpenAI v1 /chat/completions 接口族',
    enabled: 1
  },
  {
    id: `${OPENAI_PROTOCOL_CODE}_${OPENAI_PROTOCOL_VERSION}_${OPENAI_RESPONSES_FAMILY}`,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    code: OPENAI_RESPONSES_FAMILY,
    name: 'Responses',
    description: 'OpenAI v1 /responses 接口族',
    enabled: 1
  }
] as const

export const ANTHROPIC_PROTOCOL_ENDPOINT_FAMILY_SEEDS = [
  {
    id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}_${ANTHROPIC_MESSAGES_FAMILY}`,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    code: ANTHROPIC_MESSAGES_FAMILY,
    name: 'Messages',
    description: 'Anthropic v1 /messages 接口族',
    enabled: 1
  },
  {
    id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}_${ANTHROPIC_MODELS_FAMILY}`,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    code: ANTHROPIC_MODELS_FAMILY,
    name: 'Models',
    description: 'Anthropic v1 /models 接口族',
    enabled: 1
  },
  {
    id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}_${ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY}`,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    code: ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
    name: 'Message Token Counting',
    description: 'Anthropic v1 /messages/count_tokens 接口族',
    enabled: 1
  }
] as const

export const GPT_OPENAI_V1_PROFILE_SEED = {
  id: GPT_OPENAI_V1_PROFILE_ID,
  providerCode: GPT_VENDOR_CODE,
  name: 'GPT / OpenAI v1',
  description: 'GPT 供应商的 OpenAI v1 协议档案，支持 OAuth 与 API Key 两种账户接入方式',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: 'gpt-5.5',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['responses', 'chat'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_RESPONSES_FAMILY]
} as const

export const OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_SEED = {
  id: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  name: 'OpenAI 兼容 / OpenAI v1',
  description: '通用 OpenAI-compatible 供应商的 OpenAI v1 协议档案，仅承载 API Key 透传、模型目录和通用协议策略',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: 'gpt-5.5',
  accountTypes: ['api_key'],
  capabilities: ['responses', 'chat', 'passthrough'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_RESPONSES_FAMILY]
} as const

export const ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED = {
  id: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  providerCode: ANTHROPIC_PROVIDER_CODE,
  name: 'Anthropic / Anthropic v1',
  description: 'Anthropic 官方 API Key 协议档案，仅承载 x-api-key、anthropic-version 与 Messages 原生协议',
  enabled: 1,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  baseUrl: 'https://api.anthropic.com/v1',
  defaultTestModel: 'claude-haiku-4-5',
  accountTypes: ['api_key'],
  capabilities: ['messages', 'models', 'count_tokens', 'passthrough'],
  endpointFamilies: [ANTHROPIC_MESSAGES_FAMILY, ANTHROPIC_MODELS_FAMILY, ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY]
} as const

export const DEFAULT_SYSTEM_SETTINGS = [
  ['gatewayTextRawBodyLimitMegabytes', 8],
  ['systemApiRateLimitEnabled', true],
  ['systemApiRateLimitIpReadPerMinute', 600],
  ['systemApiRateLimitIpReadBurstPer10Seconds', 120],
  ['systemApiRateLimitIpWritePerMinute', 180],
  ['systemApiRateLimitIpWriteBurstPer10Seconds', 40],
  ['systemApiRateLimitUserReadPerMinute', 300],
  ['systemApiRateLimitUserWritePerMinute', 120],
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
  ['accountTestTaskConcurrency', 100],
  ['accountHealthCheckIntervalHours', 12],
  ['accountHealthCheckJitterMinutes', 120],
  ['accountHealthCheckBatchSize', 20],
  ['accountHealthCheckFailureThreshold', 3],
  ['cooldownAccountRetestIntervalSeconds', 3],
  ['cooldownAccountRetestBatchSize', 10],
  ['cooldownAccountRetestMaxBackoffHours', 12],
  ['cooldownAccountRetestLongTermIntervalHours', 24],
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
