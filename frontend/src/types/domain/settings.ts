export interface SystemSettings {
  appName?: string
  appIcon?: string
  defaultTemporaryUnschedulableMinutes?: number
  temporaryUnschedulableRetryIntervalSeconds?: number
  temporaryUnschedulableRetryAttempts?: number
  streamCircuitBreakerEnabled?: boolean
  streamRequestTimeoutSeconds?: number
  streamIdleTimeoutSeconds?: number
  streamFailureThresholdCount?: number
  streamFailureThresholdWindowMinutes?: number
  auditLogEnabled?: boolean
  auditLogSuccessSampleRate?: number
  auditLogFlushIntervalSeconds?: number
  auditLogBatchSize?: number
  auditLogQueueMaxItems?: number
  auditLogQueueMaxBytesMb?: number
  auditLogActiveCaptureMaxBytesMb?: number
  auditLogRetentionDays?: number
  [key: string]: unknown
}

export interface GlobalSettings {
  appName?: string
  appIcon?: string
  [key: string]: unknown
}
