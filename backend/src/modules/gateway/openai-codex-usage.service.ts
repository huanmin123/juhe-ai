import { runtimeConfig } from '../../config/runtime.js'
import { enqueueRecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'

export interface OpenAICodexUsageSnapshot {
  primaryUsedPercent?: number
  primaryResetAfterSeconds?: number
  primaryWindowMinutes?: number
  secondaryUsedPercent?: number
  secondaryResetAfterSeconds?: number
  secondaryWindowMinutes?: number
  primaryOverSecondaryPercent?: number
  updatedAt: string
}

interface NormalizedCodexLimits {
  used5hPercent?: number
  reset5hSeconds?: number
  window5hMinutes?: number
  used7dPercent?: number
  reset7dSeconds?: number
  window7dMinutes?: number
}

type HeaderInput = Headers | Record<string, string | string[] | undefined>
type NormalizedWindowKey = '5h' | '7d'
interface CodexWindowCandidate {
  usedPercent?: number
  resetAfterSeconds?: number
  windowMinutes?: number
}

export function parseOpenAICodexUsageHeaders(headers?: HeaderInput): OpenAICodexUsageSnapshot | undefined {
  if (!headers) return undefined
  const snapshot: OpenAICodexUsageSnapshot = { updatedAt: new Date().toISOString() }
  let hasData = false

  const assignNumber = (key: string, apply: (value: number) => void) => {
    const value = numberHeader(headers, key)
    if (value === undefined) return
    apply(value)
    hasData = true
  }

  assignNumber('x-codex-primary-used-percent', (value) => { snapshot.primaryUsedPercent = value })
  assignNumber('x-codex-primary-reset-after-seconds', (value) => { snapshot.primaryResetAfterSeconds = Math.trunc(value) })
  assignNumber('x-codex-primary-window-minutes', (value) => { snapshot.primaryWindowMinutes = Math.trunc(value) })
  assignNumber('x-codex-secondary-used-percent', (value) => { snapshot.secondaryUsedPercent = value })
  assignNumber('x-codex-secondary-reset-after-seconds', (value) => { snapshot.secondaryResetAfterSeconds = Math.trunc(value) })
  assignNumber('x-codex-secondary-window-minutes', (value) => { snapshot.secondaryWindowMinutes = Math.trunc(value) })
  assignNumber('x-codex-primary-over-secondary-limit-percent', (value) => { snapshot.primaryOverSecondaryPercent = value })

  return hasData ? snapshot : undefined
}

export function persistOpenAICodexUsageHeaders(accountId: string, headers?: HeaderInput, source = 'gateway'): boolean {
  assertLocalGatewayDatabaseAccess('persistOpenAICodexUsageHeaders')
  const snapshot = parseOpenAICodexUsageHeaders(headers)
  if (!snapshot) return false
  const payload = buildOpenAICodexUsageSnapshotPayload(snapshot, new Date(), source)
  if (!hasOwnEnumerableKey(payload)) return false
  enqueueRecordMaintenanceJob({
    type: 'account_usage_snapshot_upsert',
    accountId,
    kind: 'openai_codex',
    source,
    snapshot: payload,
    updatedAt: String(payload.codex_usage_updated_at ?? snapshot.updatedAt)
  })
  return true
}

function hasOwnEnumerableKey(value: Record<string, unknown>): boolean {
  for (const key in value) {
    if (Object.prototype.hasOwnProperty.call(value, key)) return true
  }
  return false
}

function buildOpenAICodexUsageSnapshotPayload(snapshot: OpenAICodexUsageSnapshot, fallbackNow: Date, source?: string): Record<string, unknown> {
  const baseTime = parseIsoDate(snapshot.updatedAt) ?? fallbackNow
  const payload: Record<string, unknown> = {
    codex_usage_updated_at: baseTime.toISOString()
  }
  if (source) payload.source = source

  if (snapshot.primaryUsedPercent !== undefined) payload.codex_primary_used_percent = snapshot.primaryUsedPercent
  if (snapshot.primaryResetAfterSeconds !== undefined) payload.codex_primary_reset_after_seconds = snapshot.primaryResetAfterSeconds
  if (snapshot.primaryWindowMinutes !== undefined) payload.codex_primary_window_minutes = snapshot.primaryWindowMinutes
  if (snapshot.secondaryUsedPercent !== undefined) payload.codex_secondary_used_percent = snapshot.secondaryUsedPercent
  if (snapshot.secondaryResetAfterSeconds !== undefined) payload.codex_secondary_reset_after_seconds = snapshot.secondaryResetAfterSeconds
  if (snapshot.secondaryWindowMinutes !== undefined) payload.codex_secondary_window_minutes = snapshot.secondaryWindowMinutes
  if (snapshot.primaryOverSecondaryPercent !== undefined) payload.codex_primary_over_secondary_percent = snapshot.primaryOverSecondaryPercent

  const normalized = normalizeOpenAICodexUsageSnapshot(snapshot)
  if (!normalized) return payload

  if (normalized.used5hPercent !== undefined) payload.codex_5h_used_percent = normalized.used5hPercent
  if (normalized.reset5hSeconds !== undefined) payload.codex_5h_reset_after_seconds = normalized.reset5hSeconds
  if (normalized.window5hMinutes !== undefined) payload.codex_5h_window_minutes = normalized.window5hMinutes
  if (normalized.used7dPercent !== undefined) payload.codex_7d_used_percent = normalized.used7dPercent
  if (normalized.reset7dSeconds !== undefined) payload.codex_7d_reset_after_seconds = normalized.reset7dSeconds
  if (normalized.window7dMinutes !== undefined) payload.codex_7d_window_minutes = normalized.window7dMinutes

  const reset5hAt = resetAtFromSeconds(baseTime, normalized.reset5hSeconds)
  if (reset5hAt) payload.codex_5h_reset_at = reset5hAt
  const reset7dAt = resetAtFromSeconds(baseTime, normalized.reset7dSeconds)
  if (reset7dAt) payload.codex_7d_reset_at = reset7dAt

  return payload
}

function normalizeOpenAICodexUsageSnapshot(snapshot: OpenAICodexUsageSnapshot): NormalizedCodexLimits | undefined {
  const normalized: NormalizedCodexLimits = {}
  const primary = codexWindowCandidate('primary', snapshot)
  const secondary = codexWindowCandidate('secondary', snapshot)
  const primaryKey = windowKeyFromMinutes(primary.windowMinutes)
  const secondaryKey = windowKeyFromMinutes(secondary.windowMinutes)

  if (primaryKey) assignNormalizedWindow(normalized, primaryKey, primary)
  if (secondaryKey) assignNormalizedWindow(normalized, secondaryKey, secondary)

  return Object.values(normalized).some((value) => value !== undefined) ? normalized : undefined
}

function codexWindowCandidate(side: 'primary' | 'secondary', snapshot: OpenAICodexUsageSnapshot): CodexWindowCandidate {
  return side === 'primary'
    ? {
        usedPercent: snapshot.primaryUsedPercent,
        resetAfterSeconds: snapshot.primaryResetAfterSeconds,
        windowMinutes: snapshot.primaryWindowMinutes
      }
    : {
        usedPercent: snapshot.secondaryUsedPercent,
        resetAfterSeconds: snapshot.secondaryResetAfterSeconds,
        windowMinutes: snapshot.secondaryWindowMinutes
      }
}

function windowKeyFromMinutes(minutes?: number): NormalizedWindowKey | undefined {
  if (minutes === undefined || minutes <= 0) return undefined
  return minutes <= 360 ? '5h' : '7d'
}

function assignNormalizedWindow(normalized: NormalizedCodexLimits, key: NormalizedWindowKey, candidate: CodexWindowCandidate): void {
  if (candidate.windowMinutes !== undefined && candidate.windowMinutes <= 0) return
  if (candidate.usedPercent === undefined && candidate.resetAfterSeconds === undefined && candidate.windowMinutes === undefined) return
  if (key === '5h') {
    normalized.used5hPercent = candidate.usedPercent
    normalized.reset5hSeconds = candidate.resetAfterSeconds
    normalized.window5hMinutes = candidate.windowMinutes
    return
  }
  normalized.used7dPercent = candidate.usedPercent
  normalized.reset7dSeconds = candidate.resetAfterSeconds
  normalized.window7dMinutes = candidate.windowMinutes
}

function numberHeader(headers: HeaderInput, key: string): number | undefined {
  const value = headerValue(headers, key)
  if (!value) return undefined
  return numberValue(value)
}

function headerValue(headers: HeaderInput, key: string): string | undefined {
  if (headers instanceof Headers) return headers.get(key) ?? undefined
  const target = key.toLowerCase()
  for (const [name, value] of Object.entries(headers)) {
    if (name.toLowerCase() !== target) continue
    if (Array.isArray(value)) return value[0]
    return value
  }
  return undefined
}

function resetAtFromSeconds(baseTime: Date, seconds?: number): string | undefined {
  if (seconds === undefined) return undefined
  return new Date(baseTime.getTime() + Math.max(0, seconds) * 1000).toISOString()
}

function parseIsoDate(value: string): Date | undefined {
  const time = Date.parse(value)
  return Number.isFinite(time) ? new Date(time) : undefined
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步访问 SQLite：${operation} 必须通过 DB service`)
  }
}
