import type { GlobalSettings, SystemSettings, SystemSettingsPatch } from '@/types/domain'
import { defaultAppBrand } from '@/composables/useAppBrand'

export interface GlobalForm {
  appName: string
  appIcon: string
}

export interface SystemForm {
  gatewayTextRawBodyLimitMegabytes: number
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
  chatImageGenerationTotalTimeoutSeconds: number
  noAvailableAccountWaitTimeoutSeconds: number
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
}

export const defaultGlobalSettings: GlobalForm = {
  appName: defaultAppBrand.appName,
  appIcon: defaultAppBrand.appIcon
}

export const defaultSystemSettings: SystemForm = {
  gatewayTextRawBodyLimitMegabytes: 16,
  systemApiRateLimitIpReadPerMinute: 600,
  systemApiRateLimitIpReadBurstPer10Seconds: 120,
  systemApiRateLimitIpWritePerMinute: 180,
  systemApiRateLimitIpWriteBurstPer10Seconds: 40,
  systemApiRateLimitUserReadPerMinute: 300,
  systemApiRateLimitUserWritePerMinute: 120,
  defaultTemporaryUnschedulableMinutes: 2,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  textFirstResponseTimeoutSeconds: 120,
  textStreamIdleTimeoutSeconds: 30,
  textUncommittedAttemptMaxLifetimeSeconds: 1800,
  imageFirstResponseTimeoutSeconds: 600,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 3600,
  chatImageGenerationTotalTimeoutSeconds: 900,
  noAvailableAccountWaitTimeoutSeconds: 270,
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
  cooldownAccountRetestMaxBackoffHours: 12
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
    systemApiRateLimitIpReadPerMinute: integerValue(settings.systemApiRateLimitIpReadPerMinute, 'IP 读请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitIpReadBurstPer10Seconds: integerValue(settings.systemApiRateLimitIpReadBurstPer10Seconds, 'IP 读请求突发上限', 0, 1_000_000),
    systemApiRateLimitIpWritePerMinute: integerValue(settings.systemApiRateLimitIpWritePerMinute, 'IP 写请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitIpWriteBurstPer10Seconds: integerValue(settings.systemApiRateLimitIpWriteBurstPer10Seconds, 'IP 写请求突发上限', 0, 1_000_000),
    systemApiRateLimitUserReadPerMinute: integerValue(settings.systemApiRateLimitUserReadPerMinute, '登录用户读请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitUserWritePerMinute: integerValue(settings.systemApiRateLimitUserWritePerMinute, '登录用户写请求每分钟上限', 0, 1_000_000),
    defaultTemporaryUnschedulableMinutes: integerValue(settings.defaultTemporaryUnschedulableMinutes, '临时不可调用最大暂停时间', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: integerValue(settings.temporaryUnschedulableRetryIntervalSeconds, '临时状态重试间隔', 0, 3600),
    temporaryUnschedulableRetryAttempts: integerValue(settings.temporaryUnschedulableRetryAttempts, '临时状态重试次数', 0, 10),
    textFirstResponseTimeoutSeconds: integerValue(settings.textFirstResponseTimeoutSeconds, '文本首响应等待上限', 10, 3600),
    textStreamIdleTimeoutSeconds: integerValue(settings.textStreamIdleTimeoutSeconds, '文本流式停顿上限', 1, 3600),
    textUncommittedAttemptMaxLifetimeSeconds: integerValue(settings.textUncommittedAttemptMaxLifetimeSeconds, '文本未提交尝试最大存活时间', 60, 86400),
    imageFirstResponseTimeoutSeconds: integerValue(settings.imageFirstResponseTimeoutSeconds, '图像首响应等待上限', 10, 3600),
    imageStreamIdleTimeoutSeconds: integerValue(settings.imageStreamIdleTimeoutSeconds, '图像流式停顿上限', 1, 3600),
    imageUncommittedAttemptMaxLifetimeSeconds: integerValue(settings.imageUncommittedAttemptMaxLifetimeSeconds, '图像未提交尝试最大存活时间', 60, 86400),
    chatImageGenerationTotalTimeoutSeconds: integerValue(settings.chatImageGenerationTotalTimeoutSeconds, 'AI 对话生图总超时', 60, 86400),
    noAvailableAccountWaitTimeoutSeconds: integerValue(settings.noAvailableAccountWaitTimeoutSeconds, '无可用账号等待上限', 10, 3600),
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
    cooldownAccountRetestMaxBackoffHours: integerValue(settings.cooldownAccountRetestMaxBackoffHours, '长期不可用观察阈值', 1, 720)
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

function requiredStringValue(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}
