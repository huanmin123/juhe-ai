export interface AuditLogSettings {
  enabled: boolean
  successSampleRate: number
  flushIntervalSeconds: number
  batchSize: number
  queueMaxItems: number
  queueMaxBytes: number
  activeCaptureMaxBytes: number
  retentionDays: number
}

const auditLogMb = 1024 * 1024

// 原始审计日志是固定排障能力，不通过 system_settings 暴露配置。
export const fixedAuditLogSettings: AuditLogSettings = Object.freeze({
  enabled: true,
  successSampleRate: 0.1,
  flushIntervalSeconds: 5,
  batchSize: 50,
  queueMaxItems: 1000,
  queueMaxBytes: 256 * auditLogMb,
  activeCaptureMaxBytes: 64 * auditLogMb,
  retentionDays: 7
})

export function readAuditLogSettings(): AuditLogSettings {
  return fixedAuditLogSettings
}
