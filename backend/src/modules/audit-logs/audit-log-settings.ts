import { runtimeConfig, type AuditFullBodyCaptureRuntimeConfig, type AuditFullBodyCaptureScope } from '../../config/runtime.js'
import { optionalServerDateTimeIso } from '../../storage/value-utils.js'

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
  queueMaxBytes: 1024 * auditLogMb,
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
  const normalized = normalizeAuditFullBodyCaptureConfig(runtimeConfig.audit.fullBodyCapture, nowMs)
  if (!sameAuditFullBodyCaptureConfig(runtimeConfig.audit.fullBodyCapture, normalized)) {
    runtimeConfig.audit.fullBodyCapture = normalized
    runtimeConfig.audit.fullBodyCaptureEnabled = normalized.enabled
  }
  return normalized
}

export function normalizeAuditFullBodyCaptureConfig(input: AuditFullBodyCaptureConfigInput, nowMs = Date.now()): AuditFullBodyCaptureConfig {
  if (typeof input.enabled !== 'boolean') {
    throw new Error('临时全量捕获 enabled 必须是布尔值')
  }
  const enabled = input.enabled
  const scope = normalizeAuditFullBodyCaptureScope(input.scope)
  const accountId = normalizeAuditFullBodyCaptureAccountId(input.accountId, scope)
  const includeSuccess = normalizeAuditFullBodyCaptureIncludeSuccess(input.includeSuccess)
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
  if (input.durationMinutes !== undefined) {
    if (typeof input.durationMinutes !== 'number' || !Number.isInteger(input.durationMinutes) || input.durationMinutes < 1 || input.durationMinutes > 24 * 60) {
      throw new Error('临时全量捕获 durationMinutes 必须是 1 到 1440 的整数')
    }
    const durationMinutes = input.durationMinutes
    return new Date(nowMs + durationMinutes * 60 * 1000).toISOString()
  }
  if (input.expiresAt === undefined) {
    return undefined
  }
  if (typeof input.expiresAt !== 'string' || !input.expiresAt.trim()) {
    throw new Error('临时全量捕获过期时间无效')
  }
  const normalized = optionalServerDateTimeIso(input.expiresAt)
  if (!normalized) {
    throw new Error('临时全量捕获过期时间无效')
  }
  return normalized
}

function normalizeAuditFullBodyCaptureScope(value: unknown): AuditFullBodyCaptureScope {
  if (value === undefined || value === 'global') return 'global'
  if (value === 'account') return 'account'
  throw new Error('临时全量捕获 scope 无效')
}

function normalizeAuditFullBodyCaptureAccountId(value: unknown, scope: AuditFullBodyCaptureScope): string {
  if (value === undefined) return ''
  if (typeof value !== 'string') {
    throw new Error('临时全量捕获 accountId 必须是字符串')
  }
  const accountId = value.trim()
  if (scope === 'account' && !accountId) {
    throw new Error('请选择要定向捕获的 AI 账户')
  }
  return accountId
}

function normalizeAuditFullBodyCaptureIncludeSuccess(value: unknown): boolean {
  if (value === undefined) return false
  if (typeof value === 'boolean') return value
  throw new Error('临时全量捕获 includeSuccess 必须是布尔值')
}

function sameAuditFullBodyCaptureConfig(a: AuditFullBodyCaptureConfig, b: AuditFullBodyCaptureConfig): boolean {
  return a.enabled === b.enabled
    && a.scope === b.scope
    && a.accountId === b.accountId
    && a.includeSuccess === b.includeSuccess
    && a.expiresAt === b.expiresAt
}
