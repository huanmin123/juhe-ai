import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { UserRequestLimits, UserRequestLimitWindow } from '../../../domain/types.js'

const windowOrder = ['perMinute', 'perDay', 'perWeek', 'perMonth'] as const satisfies readonly UserRequestLimitWindow[]
const defaultMaxCounterEntries = 500_000
const defaultCleanupStride = 4096
const defaultCleanupBatchSize = 64
const minuteMs = 60_000
const dayMs = 24 * 60 * minuteMs

interface CounterEntry {
  key: string
  systemAccountId: string
  window: UserRequestLimitWindow
  bucket: string
  localCount: number
  syncedLocalCount: number
  remoteTotal: number
  redisTtlMs: number
  expiresAtMs: number
  dirty: boolean
}

interface BucketDefinition {
  bucket: string
  windowEndsAtMs: number
  expiresAtMs: number
  redisTtlMs: number
}

interface BucketSnapshot {
  epochMinute: number
  perMinute: BucketDefinition
  perDay: BucketDefinition
  perWeek: BucketDefinition
  perMonth: BucketDefinition
}

export interface UserRequestLimitConsumeInput {
  systemAccountId: string
  settings: Pick<GatewaySettings,
    | 'gatewayUserRequestLimitPerMinute'
    | 'gatewayUserRequestLimitPerDay'
    | 'gatewayUserRequestLimitPerWeek'
    | 'gatewayUserRequestLimitPerMonth'
    | 'usageStatsTimezone'>
  overrides?: UserRequestLimits
  nowMs?: number
}

export interface UserRequestLimitDecision {
  allowed: boolean
  window?: UserRequestLimitWindow
  limit?: number
  retryAfterSeconds?: number
}

export interface UserRequestLimitDirtySnapshot {
  entryKey: string
  systemAccountId: string
  window: UserRequestLimitWindow
  bucket: string
  localCount: number
  redisTtlMs: number
}

export interface UserRequestLimitSyncResult {
  entryKey: string
  sentLocalCount: number
  remoteTotal: number
}

export interface UserRequestLimitCounterOptions {
  maxEntries?: number
  cleanupStride?: number
  cleanupBatchSize?: number
}

export interface UserRequestLimitCounterStats {
  entries: number
  dirtyEntries: number
  capacityEvictions: number
}

export class UserRequestLimitCounter {
  private readonly entries = new Map<string, CounterEntry>()
  private readonly dirtyKeys = new Set<string>()
  private readonly bucketSnapshots = new Map<string, BucketSnapshot>()
  private readonly timezoneFormatters = new Map<string, Intl.DateTimeFormat>()
  private readonly maxEntries: number
  private readonly cleanupStride: number
  private readonly cleanupBatchSize: number
  private consumeCount = 0
  private capacityEvictions = 0
  private cleanupIterator?: IterableIterator<[string, CounterEntry]>

  constructor(options: UserRequestLimitCounterOptions = {}) {
    this.maxEntries = positiveInteger(options.maxEntries, defaultMaxCounterEntries)
    this.cleanupStride = positiveInteger(options.cleanupStride, defaultCleanupStride)
    this.cleanupBatchSize = positiveInteger(options.cleanupBatchSize, defaultCleanupBatchSize)
  }

  consume(input: UserRequestLimitConsumeInput): UserRequestLimitDecision {
    const nowMs = input.nowMs ?? Date.now()
    const timezone = input.settings.usageStatsTimezone || 'UTC'
    let buckets: BucketSnapshot | undefined
    const activeOverrides = input.overrides?.expiresOn
      ? ((buckets = this.currentBuckets(timezone, nowMs)).perDay.bucket <= input.overrides.expiresOn ? input.overrides : undefined)
      : input.overrides
    const limits = effectiveLimits(input.settings, activeOverrides)
    if (limits.perMinute === 0 && limits.perDay === 0 && limits.perWeek === 0 && limits.perMonth === 0) {
      return { allowed: true }
    }

    buckets ??= this.currentBuckets(timezone, nowMs)
    const pending: Array<{ entry: CounterEntry; limit: number }> = []
    let blockedDecision: UserRequestLimitDecision | undefined
    for (const window of windowOrder) {
      const limit = limits[window]
      if (limit === 0) continue
      const bucket = buckets[window]
      const entry = this.entry(input.systemAccountId, window, bucket, nowMs)
      if (!entry) continue
      const unsyncedDelta = Math.max(0, entry.localCount - entry.syncedLocalCount)
      const estimatedTotal = Math.max(entry.localCount, entry.remoteTotal + unsyncedDelta)
      if (!blockedDecision && estimatedTotal + 1 > limit) {
        blockedDecision = {
          allowed: false,
          window,
          limit,
          ...(window === 'perMinute'
            ? { retryAfterSeconds: Math.max(1, Math.ceil((bucket.windowEndsAtMs - nowMs) / 1000)) }
            : {})
        }
      }
      pending.push({ entry, limit })
    }

    for (const { entry, limit } of pending) {
      const nextLocalCount = blockedDecision
        ? Math.min(entry.localCount + 1, limit + 1)
        : entry.localCount + 1
      if (nextLocalCount > entry.localCount) {
        entry.localCount = nextLocalCount
        entry.dirty = true
        this.dirtyKeys.add(entry.key)
      }
    }
    this.maybeCleanup(nowMs)
    return blockedDecision ?? { allowed: true }
  }

  dirtySnapshot(limit = 512): UserRequestLimitDirtySnapshot[] {
    const output: UserRequestLimitDirtySnapshot[] = []
    for (const entryKey of this.dirtyKeys) {
      const entry = this.entries.get(entryKey)
      if (!entry?.dirty) {
        this.dirtyKeys.delete(entryKey)
        continue
      }
      output.push({
        entryKey: entry.key,
        systemAccountId: entry.systemAccountId,
        window: entry.window,
        bucket: entry.bucket,
        localCount: entry.localCount,
        redisTtlMs: entry.redisTtlMs
      })
      // Rotate selected keys so continuously hot buckets cannot starve later dirty entries.
      this.dirtyKeys.delete(entryKey)
      this.dirtyKeys.add(entryKey)
      if (output.length >= limit) break
    }
    return output
  }

  applySyncResults(results: readonly UserRequestLimitSyncResult[]): void {
    for (const result of results) {
      const entry = this.entries.get(result.entryKey)
      if (!entry) continue
      entry.remoteTotal = Math.max(entry.remoteTotal, result.remoteTotal)
      entry.syncedLocalCount = Math.max(entry.syncedLocalCount, result.sentLocalCount)
      entry.dirty = entry.localCount > entry.syncedLocalCount
      if (!entry.dirty) this.dirtyKeys.delete(entry.key)
    }
  }

  cleanupExpired(nowMs = Date.now(), limit = 2_048): number {
    let removed = 0
    let inspected = 0
    this.cleanupIterator ??= this.entries.entries()
    while (inspected < limit) {
      const current = this.cleanupIterator.next()
      if (current.done) {
        this.cleanupIterator = undefined
        break
      }
      const [key, entry] = current.value
      if (entry.expiresAtMs <= nowMs) {
        this.entries.delete(key)
        this.dirtyKeys.delete(key)
        removed += 1
      }
      inspected += 1
    }
    return removed
  }

  size(): number {
    return this.entries.size
  }

  stats(): UserRequestLimitCounterStats {
    return {
      entries: this.entries.size,
      dirtyEntries: this.dirtyKeys.size,
      capacityEvictions: this.capacityEvictions
    }
  }

  reset(): void {
    this.entries.clear()
    this.dirtyKeys.clear()
    this.bucketSnapshots.clear()
    this.consumeCount = 0
    this.capacityEvictions = 0
    this.cleanupIterator = undefined
  }

  private entry(systemAccountId: string, window: UserRequestLimitWindow, bucket: BucketDefinition, nowMs: number): CounterEntry | undefined {
    const key = `${systemAccountId}\u001f${window}\u001f${bucket.bucket}`
    const existing = this.entries.get(key)
    if (existing && existing.expiresAtMs > nowMs) return existing
    if (this.entries.size >= this.maxEntries) {
      this.cleanupExpired(nowMs, this.cleanupBatchSize)
    }
    if (this.entries.size >= this.maxEntries) {
      const oldestKey = this.entries.keys().next().value as string | undefined
      if (oldestKey) {
        this.entries.delete(oldestKey)
        this.dirtyKeys.delete(oldestKey)
        this.capacityEvictions += 1
      }
    }
    const entry: CounterEntry = {
      key,
      systemAccountId,
      window,
      bucket: bucket.bucket,
      localCount: 0,
      syncedLocalCount: 0,
      remoteTotal: 0,
      redisTtlMs: bucket.redisTtlMs,
      expiresAtMs: bucket.expiresAtMs,
      dirty: false
    }
    this.entries.set(key, entry)
    return entry
  }

  private currentBuckets(timezone: string, nowMs: number): BucketSnapshot {
    const epochMinute = Math.floor(nowMs / minuteMs)
    const cached = this.bucketSnapshots.get(timezone)
    if (cached?.epochMinute === epochMinute) return cached

    const parts = localDateParts(this.timezoneFormatter(timezone), nowMs)
    const localDayEpoch = Date.UTC(parts.year, parts.month - 1, parts.day)
    const mondayEpoch = localDayEpoch - ((new Date(localDayEpoch).getUTCDay() + 6) % 7) * dayMs
    const monday = new Date(mondayEpoch)
    const snapshot: BucketSnapshot = {
      epochMinute,
      perMinute: {
        bucket: String(epochMinute),
        windowEndsAtMs: (epochMinute + 1) * minuteMs,
        expiresAtMs: nowMs + 2 * minuteMs,
        redisTtlMs: 2 * minuteMs
      },
      perDay: {
        bucket: `${parts.year}-${pad(parts.month)}-${pad(parts.day)}`,
        windowEndsAtMs: nowMs + dayMs,
        expiresAtMs: nowMs + 2 * dayMs,
        redisTtlMs: 2 * dayMs
      },
      perWeek: {
        bucket: `${monday.getUTCFullYear()}-${pad(monday.getUTCMonth() + 1)}-${pad(monday.getUTCDate())}`,
        windowEndsAtMs: nowMs + 7 * dayMs,
        expiresAtMs: nowMs + 9 * dayMs,
        redisTtlMs: 9 * dayMs
      },
      perMonth: {
        bucket: `${parts.year}-${pad(parts.month)}`,
        windowEndsAtMs: nowMs + 31 * dayMs,
        expiresAtMs: nowMs + 35 * dayMs,
        redisTtlMs: 35 * dayMs
      }
    }
    this.bucketSnapshots.set(timezone, snapshot)
    if (this.bucketSnapshots.size > 32) {
      const oldestTimezone = this.bucketSnapshots.keys().next().value as string | undefined
      if (oldestTimezone) this.bucketSnapshots.delete(oldestTimezone)
    }
    return snapshot
  }

  private timezoneFormatter(timezone: string): Intl.DateTimeFormat {
    const cached = this.timezoneFormatters.get(timezone)
    if (cached) return cached
    const formatter = new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hourCycle: 'h23'
    })
    this.timezoneFormatters.set(timezone, formatter)
    return formatter
  }

  private maybeCleanup(nowMs: number): void {
    this.consumeCount += 1
    if (this.consumeCount % this.cleanupStride !== 0) return
    let inspected = 0
    for (const [key, entry] of this.entries) {
      if (entry.expiresAtMs <= nowMs) {
        this.entries.delete(key)
        this.dirtyKeys.delete(key)
      }
      inspected += 1
      if (inspected >= this.cleanupBatchSize) break
    }
  }
}

export const userRequestLimitCounter = new UserRequestLimitCounter()

function effectiveLimits(settings: UserRequestLimitConsumeInput['settings'], overrides: UserRequestLimits | undefined) {
  return {
    perMinute: overrides?.perMinute ?? settings.gatewayUserRequestLimitPerMinute ?? 0,
    perDay: overrides?.perDay ?? settings.gatewayUserRequestLimitPerDay ?? 0,
    perWeek: overrides?.perWeek ?? settings.gatewayUserRequestLimitPerWeek ?? 0,
    perMonth: overrides?.perMonth ?? settings.gatewayUserRequestLimitPerMonth ?? 0
  }
}

function localDateParts(formatter: Intl.DateTimeFormat, nowMs: number): { year: number; month: number; day: number } {
  let year = 0
  let month = 0
  let day = 0
  for (const part of formatter.formatToParts(nowMs)) {
    if (part.type === 'year') year = Number(part.value)
    else if (part.type === 'month') month = Number(part.value)
    else if (part.type === 'day') day = Number(part.value)
  }
  return { year, month, day }
}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

function positiveInteger(value: number | undefined, fallback: number): number {
  return Number.isInteger(value) && Number(value) > 0 ? Number(value) : fallback
}
