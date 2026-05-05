import type { GlobalSettings, SystemSettings } from '@/types/domain'
import { defaultAppBrand } from '@/composables/useAppBrand'

export interface GlobalForm {
  appName: string
  appIcon: string
}

export interface SystemForm {
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamCircuitBreakerEnabled: boolean
  streamRequestTimeoutSeconds: number
  streamIdleTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  auditLogEnabled: boolean
  auditLogSuccessSampleRate: number
  auditLogFlushIntervalSeconds: number
  auditLogBatchSize: number
  auditLogQueueMaxItems: number
  auditLogQueueMaxBytesMb: number
  auditLogActiveCaptureMaxBytesMb: number
  auditLogRetentionDays: number
}

export const defaultGlobalSettings: GlobalForm = {
  appName: defaultAppBrand.appName,
  appIcon: defaultAppBrand.appIcon
}

export const defaultSystemSettings: SystemForm = {
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 180,
  streamIdleTimeoutSeconds: 60,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 10,
  auditLogEnabled: true,
  auditLogSuccessSampleRate: 0.1,
  auditLogFlushIntervalSeconds: 5,
  auditLogBatchSize: 50,
  auditLogQueueMaxItems: 1000,
  auditLogQueueMaxBytesMb: 256,
  auditLogActiveCaptureMaxBytesMb: 64,
  auditLogRetentionDays: 7
}

export function normalizeGlobalSettings(settings: GlobalSettings | GlobalForm): GlobalForm {
  return {
    appName: stringValue(settings.appName, defaultGlobalSettings.appName),
    appIcon: stringValue(settings.appIcon, defaultGlobalSettings.appIcon)
  }
}

export function normalizeSystemSettings(settings: SystemSettings | SystemForm): SystemForm {
  return {
    defaultTemporaryUnschedulableMinutes: numberValue(settings.defaultTemporaryUnschedulableMinutes, defaultSystemSettings.defaultTemporaryUnschedulableMinutes, 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberValue(settings.temporaryUnschedulableRetryIntervalSeconds, defaultSystemSettings.temporaryUnschedulableRetryIntervalSeconds, 0, 3600),
    temporaryUnschedulableRetryAttempts: numberValue(settings.temporaryUnschedulableRetryAttempts, defaultSystemSettings.temporaryUnschedulableRetryAttempts, 0, 10),
    streamCircuitBreakerEnabled: booleanValue(settings.streamCircuitBreakerEnabled, defaultSystemSettings.streamCircuitBreakerEnabled),
    streamRequestTimeoutSeconds: numberValue(settings.streamRequestTimeoutSeconds, defaultSystemSettings.streamRequestTimeoutSeconds, 10, 3600),
    streamIdleTimeoutSeconds: numberValue(settings.streamIdleTimeoutSeconds, defaultSystemSettings.streamIdleTimeoutSeconds, 1, 3600),
    streamFailureThresholdCount: numberValue(settings.streamFailureThresholdCount, defaultSystemSettings.streamFailureThresholdCount, 1, 100),
    streamFailureThresholdWindowMinutes: numberValue(settings.streamFailureThresholdWindowMinutes, defaultSystemSettings.streamFailureThresholdWindowMinutes, 1, 1440),
    auditLogEnabled: booleanValue(settings.auditLogEnabled, defaultSystemSettings.auditLogEnabled),
    auditLogSuccessSampleRate: decimalValue(settings.auditLogSuccessSampleRate, defaultSystemSettings.auditLogSuccessSampleRate, 0, 1),
    auditLogFlushIntervalSeconds: numberValue(settings.auditLogFlushIntervalSeconds, defaultSystemSettings.auditLogFlushIntervalSeconds, 1, 3600),
    auditLogBatchSize: numberValue(settings.auditLogBatchSize, defaultSystemSettings.auditLogBatchSize, 1, 1000),
    auditLogQueueMaxItems: numberValue(settings.auditLogQueueMaxItems, defaultSystemSettings.auditLogQueueMaxItems, 1, 100000),
    auditLogQueueMaxBytesMb: numberValue(settings.auditLogQueueMaxBytesMb, defaultSystemSettings.auditLogQueueMaxBytesMb, 1, 10240),
    auditLogActiveCaptureMaxBytesMb: numberValue(settings.auditLogActiveCaptureMaxBytesMb, defaultSystemSettings.auditLogActiveCaptureMaxBytesMb, 1, 10240),
    auditLogRetentionDays: numberValue(settings.auditLogRetentionDays, defaultSystemSettings.auditLogRetentionDays, 1, 7)
  }
}

function decimalValue(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(number, min), max)
}

function numberValue(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

function booleanValue(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function stringValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}
