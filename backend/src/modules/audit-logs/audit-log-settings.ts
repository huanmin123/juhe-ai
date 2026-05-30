import { runtimeConfig, type AuditFullBodyCaptureRuntimeConfig, type AuditFullBodyCaptureScope } from '../../config/runtime.js'

export type AuditFullBodyCaptureConfig = AuditFullBodyCaptureRuntimeConfig

export interface AuditFullBodyCaptureConfigInput {
  enabled: boolean
  scope?: AuditFullBodyCaptureScope
  accountId?: string
  includeSuccess?: boolean
  expiresAt?: string
  durationMinutes?: number
}

export interface AuditLogSettings {
  enabled: boolean
  successSampleRate: number
  flushIntervalSeconds: number
  batchSize: number
  queueMaxItems: number
  queueMaxBytes: number
  activeCaptureMaxBytes: number
  fullBodyCaptureEnabled: boolean
  fullBodyCapture: AuditFullBodyCaptureConfig
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
  fullBodyCapture: Object.freeze({
    enabled: false,
    scope: 'global',
    includeSuccess: false
  }),
  successRetentionDays: 7,
  failureRetentionDays: 30,
  errorGroupRetentionDays: 30
})

export function readAuditLogSettings(): AuditLogSettings {
  const fullBodyCapture = readAuditFullBodyCaptureConfig()
  return {
    ...fixedAuditLogSettings,
    fullBodyCaptureEnabled: fullBodyCapture.enabled,
    fullBodyCapture
  }
}

export function setAuditLogFullBodyCaptureEnabled(enabled: boolean): AuditLogSettings {
  runtimeConfig.audit.fullBodyCaptureEnabled = enabled
  runtimeConfig.audit.fullBodyCapture = {
    enabled,
    scope: 'global',
    includeSuccess: false,
    updatedAt: new Date().toISOString()
  }
  return readAuditLogSettings()
}

export function setAuditLogFullBodyCaptureConfig(input: AuditFullBodyCaptureConfigInput): AuditLogSettings {
  const nextConfig = normalizeAuditFullBodyCaptureConfig(input)
  runtimeConfig.audit.fullBodyCapture = nextConfig
  runtimeConfig.audit.fullBodyCaptureEnabled = nextConfig.enabled
  return readAuditLogSettings()
}

export function readAuditFullBodyCaptureConfig(nowMs = Date.now()): AuditFullBodyCaptureConfig {
  if (runtimeConfig.audit.fullBodyCaptureEnabled !== runtimeConfig.audit.fullBodyCapture.enabled) {
    runtimeConfig.audit.fullBodyCapture = {
      enabled: runtimeConfig.audit.fullBodyCaptureEnabled,
      scope: 'global',
      includeSuccess: false,
      updatedAt: new Date(nowMs).toISOString()
    }
  }

  const normalized = normalizeAuditFullBodyCaptureConfig(runtimeConfig.audit.fullBodyCapture, nowMs)
  if (!sameAuditFullBodyCaptureConfig(runtimeConfig.audit.fullBodyCapture, normalized)) {
    runtimeConfig.audit.fullBodyCapture = normalized
    runtimeConfig.audit.fullBodyCaptureEnabled = normalized.enabled
  }
  return normalized
}

export function normalizeAuditFullBodyCaptureConfig(input: AuditFullBodyCaptureConfigInput, nowMs = Date.now()): AuditFullBodyCaptureConfig {
  const enabled = input.enabled === true
  const scope = input.scope === 'account' ? 'account' : 'global'
  const accountId = typeof input.accountId === 'string' ? input.accountId.trim() : ''
  const includeSuccess = input.includeSuccess === true
  const updatedAt = new Date(nowMs).toISOString()
  const expiresAt = normalizeAuditFullBodyCaptureExpiresAt(input, nowMs)

  if (!enabled) {
    return {
      enabled: false,
      scope,
      accountId: scope === 'account' && accountId ? accountId : undefined,
      includeSuccess,
      updatedAt
    }
  }
  if (expiresAt && Date.parse(expiresAt) <= nowMs) {
    return {
      enabled: false,
      scope,
      accountId: scope === 'account' && accountId ? accountId : undefined,
      includeSuccess,
      expiresAt,
      updatedAt
    }
  }
  return {
    enabled: true,
    scope,
    accountId: scope === 'account' ? accountId : undefined,
    includeSuccess,
    expiresAt,
    updatedAt
  }
}

function normalizeAuditFullBodyCaptureExpiresAt(input: AuditFullBodyCaptureConfigInput, nowMs: number): string | undefined {
  if (typeof input.durationMinutes === 'number' && Number.isFinite(input.durationMinutes)) {
    const durationMinutes = Math.min(Math.max(Math.trunc(input.durationMinutes), 1), 24 * 60)
    return new Date(nowMs + durationMinutes * 60 * 1000).toISOString()
  }
  if (typeof input.expiresAt !== 'string') {
    return undefined
  }
  const timestamp = Date.parse(input.expiresAt)
  if (!Number.isFinite(timestamp)) {
    return undefined
  }
  return new Date(timestamp).toISOString()
}

function sameAuditFullBodyCaptureConfig(a: AuditFullBodyCaptureConfig, b: AuditFullBodyCaptureConfig): boolean {
  return a.enabled === b.enabled
    && a.scope === b.scope
    && a.accountId === b.accountId
    && a.includeSuccess === b.includeSuccess
    && a.expiresAt === b.expiresAt
}
