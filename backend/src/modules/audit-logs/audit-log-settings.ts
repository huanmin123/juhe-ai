import { getSettings } from '../../storage/repositories.js'
import { createAppCache } from '../../shared/cache.js'

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

const auditLogSettingsCache = createAppCache<string, AuditLogSettings>({
  name: 'audit-log:settings',
  max: 1,
  ttlMs: 1000
})

export function readAuditLogSettings(): AuditLogSettings {
  const cached = auditLogSettingsCache.get('current')
  if (cached) return cached
  const settings = auditLogSettingsFromRecord(getSettings())
  auditLogSettingsCache.set('current', settings)
  return settings
}

export function clearAuditLogSettingsCache(): void {
  auditLogSettingsCache.clear()
}

export function auditLogSettingsFromRecord(settings: Record<string, unknown>): AuditLogSettings {
  return {
    enabled: booleanSetting(settings.auditLogEnabled, true),
    successSampleRate: decimalSetting(settings.auditLogSuccessSampleRate, 0.1, 0, 1),
    flushIntervalSeconds: numberSetting(settings.auditLogFlushIntervalSeconds, 5, 1, 3600),
    batchSize: numberSetting(settings.auditLogBatchSize, 50, 1, 1000),
    queueMaxItems: numberSetting(settings.auditLogQueueMaxItems, 1000, 1, 100000),
    queueMaxBytes: numberSetting(settings.auditLogQueueMaxBytesMb, 256, 1, 10240) * 1024 * 1024,
    activeCaptureMaxBytes: numberSetting(settings.auditLogActiveCaptureMaxBytesMb, 64, 1, 10240) * 1024 * 1024,
    retentionDays: numberSetting(settings.auditLogRetentionDays, 7, 1, 3650)
  }
}

function booleanSetting(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function decimalSetting(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(number, min), max)
}

function numberSetting(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}
