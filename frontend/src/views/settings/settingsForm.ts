import type { GlobalSettings, SystemSettings, SystemSettingsPatch } from '@/types/domain'
import { defaultAppBrand } from '@/composables/useAppBrand'

export interface GlobalForm {
  appName: string
  appIcon: string
}

export interface SystemForm {
  gatewayTextRawBodyLimitMegabytes: number
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamCircuitBreakerEnabled: boolean
  streamRequestTimeoutSeconds: number
  streamIdleTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  accountTestTaskConcurrency: number
  cooldownAccountRetestMaxBackoffHours: number
}

export const defaultGlobalSettings: GlobalForm = {
  appName: defaultAppBrand.appName,
  appIcon: defaultAppBrand.appIcon
}

export const defaultSystemSettings: SystemForm = {
  gatewayTextRawBodyLimitMegabytes: 8,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 180,
  streamIdleTimeoutSeconds: 60,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 10,
  accountTestTaskConcurrency: 100,
  cooldownAccountRetestMaxBackoffHours: 24
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
    defaultTemporaryUnschedulableMinutes: integerValue(settings.defaultTemporaryUnschedulableMinutes, '临时不可调用最大暂停时间', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: integerValue(settings.temporaryUnschedulableRetryIntervalSeconds, '临时状态重试间隔', 0, 3600),
    temporaryUnschedulableRetryAttempts: integerValue(settings.temporaryUnschedulableRetryAttempts, '临时状态重试次数', 0, 10),
    streamCircuitBreakerEnabled: booleanValue(settings.streamCircuitBreakerEnabled, '流式熔断开关'),
    streamRequestTimeoutSeconds: integerValue(settings.streamRequestTimeoutSeconds, '上游首包等待上限', 10, 3600),
    streamIdleTimeoutSeconds: integerValue(settings.streamIdleTimeoutSeconds, '输出停顿上限', 1, 3600),
    streamFailureThresholdCount: integerValue(settings.streamFailureThresholdCount, '失败触发次数', 1, 100),
    streamFailureThresholdWindowMinutes: integerValue(settings.streamFailureThresholdWindowMinutes, '失败统计窗口', 1, 1440),
    accountTestTaskConcurrency: integerValue(settings.accountTestTaskConcurrency, '账号测试后台并发上限', 1, 1000),
    cooldownAccountRetestMaxBackoffHours: integerValue(settings.cooldownAccountRetestMaxBackoffHours, '最长自动恢复观察', 1, 720)
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

function booleanValue(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(`${label}必须是布尔值`)
  }
  return value
}

function requiredStringValue(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}
