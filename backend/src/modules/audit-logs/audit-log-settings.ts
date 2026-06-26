import { runtimeConfig } from '../../config/runtime.js'

export interface AuditLogSettings {
  enabled: boolean
  fullBodyCaptureEnabled: boolean
  successSampleRate: number
  flushIntervalSeconds: number
  batchSize: number
  queueMaxItems: number
  queueMaxBytes: number
  activeCaptureMaxBytes: number
  successHotRetentionHours: number
  successRetentionDays: number
  failureRetentionDays: number
  errorGroupRetentionDays: number
}

const auditLogMb = 1024 * 1024

// 原始审计日志是固定排障能力，不通过 system_settings 暴露配置。
export const fixedAuditLogSettings: AuditLogSettings = Object.freeze({
  enabled: true,
  fullBodyCaptureEnabled: true,
  successSampleRate: 0.1,
  flushIntervalSeconds: 5,
  batchSize: 500,
  queueMaxItems: 50000,
  queueMaxBytes: 256 * auditLogMb,
  activeCaptureMaxBytes: 64 * auditLogMb,
  successHotRetentionHours: 1,
  successRetentionDays: 7,
  failureRetentionDays: 30,
  errorGroupRetentionDays: 30
})

export function readAuditLogSettings(): AuditLogSettings {
  const baseSettings = {
    ...fixedAuditLogSettings,
    fullBodyCaptureEnabled: runtimeConfig.audit.fullBodyCaptureEnabled
  }
  if (runtimeConfig.runtimeMode === 'performance' || runtimeConfig.databaseDriver === 'postgres') {
    return {
      ...baseSettings,
      successSampleRate: 0.05,
      successHotRetentionHours: 0
    }
  }
  return baseSettings
}
