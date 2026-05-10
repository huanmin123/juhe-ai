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
  streamFailureThresholdWindowMinutes: 10
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
    streamFailureThresholdWindowMinutes: numberValue(settings.streamFailureThresholdWindowMinutes, defaultSystemSettings.streamFailureThresholdWindowMinutes, 1, 1440)
  }
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
