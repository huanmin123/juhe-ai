import { runtimeConfig } from '../../config/runtime.js'

export interface AuditLogSettings {
  enabled: boolean
  fullBodyCaptureEnabled: boolean
  successSampleRate: number
  activeCaptureMaxBytes: number
  successHotRetentionHours: number
  successRetentionDays: number
  problemRetentionDays: number
  successFullBodyLimitBytes: number
  problemFullBodyLimitBytes: number
}

const auditLogMb = 1024 * 1024

// runtimeConfig 是审计总开关和环境变量合并的唯一事实源。
export const fixedAuditLogSettings: AuditLogSettings = Object.freeze({
  enabled: runtimeConfig.auditLog.enabled,
  fullBodyCaptureEnabled: true,
  successSampleRate: runtimeConfig.auditLog.successSampleRate,
  activeCaptureMaxBytes: 64 * auditLogMb,
  successHotRetentionHours: runtimeConfig.auditLog.successHotRetentionHours,
  successRetentionDays: runtimeConfig.auditLog.successRetentionDays,
  problemRetentionDays: runtimeConfig.auditLog.problemRetentionDays,
  successFullBodyLimitBytes: runtimeConfig.auditLog.successFullBodyLimitBytes,
  problemFullBodyLimitBytes: runtimeConfig.auditLog.problemFullBodyLimitBytes
})

export function readAuditLogSettings(): AuditLogSettings {
  return fixedAuditLogSettings
}
