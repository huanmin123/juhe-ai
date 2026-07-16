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
  problemRetentionDays: number
  successFullBodyLimitBytes: number
  problemFullBodyLimitBytes: number
}

const auditLogMb = 1024 * 1024

// 原始审计日志始终启用；runtimeConfig 负责环境变量合并和启动期校验。
export const fixedAuditLogSettings: AuditLogSettings = Object.freeze({
  enabled: true,
  fullBodyCaptureEnabled: true,
  successSampleRate: runtimeConfig.auditLog.successSampleRate,
  flushIntervalSeconds: 5,
  batchSize: 500,
  queueMaxItems: 50000,
  queueMaxBytes: 256 * auditLogMb,
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
