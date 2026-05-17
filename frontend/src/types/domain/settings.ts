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
  [key: string]: unknown
}

export interface GlobalSettings {
  appName?: string
  appIcon?: string
  [key: string]: unknown
}

export interface UpstreamErrorFeatureRuleCatalogItem {
  id: string
  enabled: boolean
  name: string
  description?: string
  rationale?: string
  source?: string
  provider: string
  endpoint: string
  action: string
  accountPolicy: string
  rule: Record<string, unknown>
}
