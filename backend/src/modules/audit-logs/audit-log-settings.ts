import { runtimeConfig } from '../../config/runtime.js'

export interface AuditLogSettings {
  enabled: boolean
  successSampleRate: number
  flushIntervalSeconds: number
  batchSize: number
  queueMaxItems: number
  queueMaxBytes: number
  activeCaptureMaxBytes: number
  fullBodyCaptureEnabled: boolean
  successRetentionDays: number
  failureRetentionDays: number
  errorGroupRetentionDays: number
}

const auditLogMb = 1024 * 1024

// 原始审计日志是固定排障能力，不通过 system_settings 暴露配置。
export const fixedAuditLogSettings: AuditLogSettings = Object.freeze({
  enabled: true,
  successSampleRate: 0.1,
  flushIntervalSeconds: 5,
  batchSize: 200,
  queueMaxItems: 5000,
  queueMaxBytes: 128 * auditLogMb,
  activeCaptureMaxBytes: 64 * auditLogMb,
  fullBodyCaptureEnabled: false,
  successRetentionDays: 7,
  failureRetentionDays: 30,
  errorGroupRetentionDays: 30
})

export function readAuditLogSettings(): AuditLogSettings {
  return {
    ...fixedAuditLogSettings,
    fullBodyCaptureEnabled: runtimeConfig.audit.fullBodyCaptureEnabled
  }
}

export function setAuditLogFullBodyCaptureEnabled(enabled: boolean): AuditLogSettings {
  runtimeConfig.audit.fullBodyCaptureEnabled = enabled
  return readAuditLogSettings()
}
