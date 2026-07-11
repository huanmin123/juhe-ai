import type { GlobalSettings, SystemSettings, SystemSettingsPatch } from '@/types/domain'
import { defaultAppBrand } from '@/composables/useAppBrand'

export interface GlobalForm {
  appName: string
  appIcon: string
}

export interface SystemForm {
  gatewayTextRawBodyLimitMegabytes: number
  gptPriorityPriceMultiplier: number
  gptFlexPriceMultiplier: number
  systemApiRateLimitIpReadPerMinute: number
  systemApiRateLimitIpReadBurstPer10Seconds: number
  systemApiRateLimitIpWritePerMinute: number
  systemApiRateLimitIpWriteBurstPer10Seconds: number
  systemApiRateLimitUserReadPerMinute: number
  systemApiRateLimitUserWritePerMinute: number
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamRequestTimeoutSeconds: number
  streamIdleTimeoutSeconds: number
  streamClientTotalWaitTimeoutSeconds: number
  streamMaxLifetimeSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  accountTestTaskConcurrency: number
  accountHealthCheckIntervalHours: number
  accountHealthCheckJitterMinutes: number
  accountHealthCheckBatchSize: number
  accountHealthCheckFailureThreshold: number
  runtimeLogIndexRetentionDays: number
  publicApiLogRetentionDays: number
  usageRecordRetentionDays: number
  cooldownAccountRetestMaxBackoffHours: number
  cooldownAccountRetestLongTermIntervalHours: number
}

export const defaultGlobalSettings: GlobalForm = {
  appName: defaultAppBrand.appName,
  appIcon: defaultAppBrand.appIcon
}

export const defaultSystemSettings: SystemForm = {
  gatewayTextRawBodyLimitMegabytes: 16,
  gptPriorityPriceMultiplier: 2,
  gptFlexPriceMultiplier: 0.5,
  systemApiRateLimitIpReadPerMinute: 600,
  systemApiRateLimitIpReadBurstPer10Seconds: 120,
  systemApiRateLimitIpWritePerMinute: 180,
  systemApiRateLimitIpWriteBurstPer10Seconds: 40,
  systemApiRateLimitUserReadPerMinute: 300,
  systemApiRateLimitUserWritePerMinute: 120,
  defaultTemporaryUnschedulableMinutes: 2,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamRequestTimeoutSeconds: 120,
  streamIdleTimeoutSeconds: 30,
  streamClientTotalWaitTimeoutSeconds: 270,
  streamMaxLifetimeSeconds: 1800,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5,
  accountTestTaskConcurrency: 100,
  accountHealthCheckIntervalHours: 12,
  accountHealthCheckJitterMinutes: 120,
  accountHealthCheckBatchSize: 20,
  accountHealthCheckFailureThreshold: 3,
  runtimeLogIndexRetentionDays: 14,
  publicApiLogRetentionDays: 30,
  usageRecordRetentionDays: 30,
  cooldownAccountRetestMaxBackoffHours: 12,
  cooldownAccountRetestLongTermIntervalHours: 1
}

export function normalizeGlobalSettings(settings: GlobalSettings | GlobalForm): GlobalForm {
  return {
    appName: requiredStringValue(settings.appName, '系统名称'),
    appIcon: requiredStringValue(settings.appIcon, '系统图标路径')
  }
}

export function normalizeSystemSettings(settings: SystemSettings | SystemForm): SystemForm {
  return {
    gatewayTextRawBodyLimitMegabytes: integerValue(settings.gatewayTextRawBodyLimitMegabytes, '文本请求体上限', 1, 64),
    gptPriorityPriceMultiplier: decimalValue(settings.gptPriorityPriceMultiplier, 'Priority 通用计价倍率', 0.01, 100),
    gptFlexPriceMultiplier: decimalValue(settings.gptFlexPriceMultiplier, 'Flex 通用计价倍率', 0.01, 100),
    systemApiRateLimitIpReadPerMinute: integerValue(settings.systemApiRateLimitIpReadPerMinute, 'IP 读请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitIpReadBurstPer10Seconds: integerValue(settings.systemApiRateLimitIpReadBurstPer10Seconds, 'IP 读请求突发上限', 0, 1_000_000),
    systemApiRateLimitIpWritePerMinute: integerValue(settings.systemApiRateLimitIpWritePerMinute, 'IP 写请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitIpWriteBurstPer10Seconds: integerValue(settings.systemApiRateLimitIpWriteBurstPer10Seconds, 'IP 写请求突发上限', 0, 1_000_000),
    systemApiRateLimitUserReadPerMinute: integerValue(settings.systemApiRateLimitUserReadPerMinute, '登录用户读请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitUserWritePerMinute: integerValue(settings.systemApiRateLimitUserWritePerMinute, '登录用户写请求每分钟上限', 0, 1_000_000),
    defaultTemporaryUnschedulableMinutes: integerValue(settings.defaultTemporaryUnschedulableMinutes, '临时不可调用最大暂停时间', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: integerValue(settings.temporaryUnschedulableRetryIntervalSeconds, '临时状态重试间隔', 0, 3600),
    temporaryUnschedulableRetryAttempts: integerValue(settings.temporaryUnschedulableRetryAttempts, '临时状态重试次数', 0, 10),
    streamRequestTimeoutSeconds: integerValue(settings.streamRequestTimeoutSeconds, '上游首包等待上限', 10, 3600),
    streamIdleTimeoutSeconds: integerValue(settings.streamIdleTimeoutSeconds, '输出停顿上限', 1, 3600),
    streamClientTotalWaitTimeoutSeconds: integerValue(settings.streamClientTotalWaitTimeoutSeconds, '客户端总等待时长', 10, 3600),
    streamMaxLifetimeSeconds: integerValue(settings.streamMaxLifetimeSeconds, '单条流最大存活时间', 60, 86400),
    streamFailureThresholdCount: integerValue(settings.streamFailureThresholdCount, '流失败诊断计数', 1, 100),
    streamFailureThresholdWindowMinutes: integerValue(settings.streamFailureThresholdWindowMinutes, '流失败诊断窗口', 1, 1440),
    accountTestTaskConcurrency: integerValue(settings.accountTestTaskConcurrency, '账号测试后台并发上限', 1, 1000),
    accountHealthCheckIntervalHours: integerValue(settings.accountHealthCheckIntervalHours, '正常账号健康检测间隔', 1, 168),
    accountHealthCheckJitterMinutes: integerValue(settings.accountHealthCheckJitterMinutes, '健康检测错峰窗口', 0, 1440),
    accountHealthCheckBatchSize: integerValue(settings.accountHealthCheckBatchSize, '健康检测单轮账号数', 1, 100),
    accountHealthCheckFailureThreshold: integerValue(settings.accountHealthCheckFailureThreshold, '健康检测连续失败阈值', 1, 10),
    runtimeLogIndexRetentionDays: integerValue(settings.runtimeLogIndexRetentionDays, '运行日志索引保留天数', 1, 90),
    publicApiLogRetentionDays: integerValue(settings.publicApiLogRetentionDays, '公开接口日志保留天数', 1, 365),
    usageRecordRetentionDays: integerValue(settings.usageRecordRetentionDays, '使用记录保留天数', 1, 180),
    cooldownAccountRetestMaxBackoffHours: integerValue(settings.cooldownAccountRetestMaxBackoffHours, '长期不可用观察阈值', 1, 720),
    cooldownAccountRetestLongTermIntervalHours: integerValue(settings.cooldownAccountRetestLongTermIntervalHours, '长期不可用复测间隔', 1, 720)
  }
}

export function buildGlobalSettingsPayload(form: GlobalForm): GlobalSettings {
  return normalizeGlobalSettings(form)
}

export function buildSystemSettingsPayload(form: SystemForm): SystemSettingsPatch {
  return normalizeSystemSettings(form)
}

function integerValue(value: unknown, label: string, min: number, max: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`${label}必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`${label}必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

function decimalValue(value: unknown, label: string, min: number, max: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    throw new Error(`${label}必须是数字`)
  }
  if (value < min || value > max) {
    throw new Error(`${label}必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

function requiredStringValue(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}
