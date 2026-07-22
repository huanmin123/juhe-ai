import { registerGatewayRuntimeCacheInvalidator } from '../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../shared/logger.js'
import type { AccountHealthCheckSettings } from './account-health-check.repository.js'
import { getSettingsAsync } from './settings.repository.js'

const usageHealthCheckSettingsSnapshotTtlMs = 60_000
const usageHealthCheckSettingsFailureRetryMs = 5_000

type SettingsLoader = () => Promise<Record<string, unknown>>

export class UsageHealthCheckSettingsSnapshotCache {
  private snapshot?: AccountHealthCheckSettings
  private loadedAt = 0
  private retryAfter = 0
  private pending?: Promise<AccountHealthCheckSettings>

  constructor(
    private readonly load: SettingsLoader,
    private readonly options: {
      ttlMs?: number
      failureRetryMs?: number
      onError?: (error: unknown) => void
    } = {}
  ) {}

  read(now = Date.now()): Promise<AccountHealthCheckSettings> {
    const ttlMs = this.options.ttlMs ?? usageHealthCheckSettingsSnapshotTtlMs
    if (this.snapshot && now - this.loadedAt < ttlMs) {
      return Promise.resolve({ ...this.snapshot })
    }
    if (now < this.retryAfter) {
      return Promise.resolve({ ...(this.snapshot ?? defaultHealthCheckSettings()) })
    }
    if (!this.pending) {
      this.pending = this.refresh(now).finally(() => {
        this.pending = undefined
      })
    }
    return this.pending.then((settings) => ({ ...settings }))
  }

  invalidate(): void {
    this.snapshot = undefined
    this.loadedAt = 0
    this.retryAfter = 0
  }

  private async refresh(now: number): Promise<AccountHealthCheckSettings> {
    try {
      const loaded = healthCheckSettingsFromSystemSettings(await this.load())
      this.snapshot = loaded
      this.loadedAt = now
      this.retryAfter = 0
      return loaded
    } catch (error) {
      this.retryAfter = now + (this.options.failureRetryMs ?? usageHealthCheckSettingsFailureRetryMs)
      try {
        this.options.onError?.(error)
      } catch {
        // Diagnostics must not turn a settings fallback into a usage write failure.
      }
      return this.snapshot ?? defaultHealthCheckSettings()
    }
  }
}

const usageHealthCheckSettingsSnapshot = new UsageHealthCheckSettingsSnapshotCache(getSettingsAsync, {
  onError: (error) => {
    logger.warn(errorLogFields(error, {
      event: 'usage_health_check_settings_snapshot_refresh_failed'
    }), '使用记录健康信号设置快照刷新失败，将临时使用最近快照或默认值')
  }
})

registerGatewayRuntimeCacheInvalidator((reason) => {
  if (!reason || reason === 'settings_updated') {
    usageHealthCheckSettingsSnapshot.invalidate()
  }
})

export function readUsageHealthCheckSettingsSnapshot(): Promise<AccountHealthCheckSettings> {
  return usageHealthCheckSettingsSnapshot.read()
}

function healthCheckSettingsFromSystemSettings(settings: Record<string, unknown>): AccountHealthCheckSettings {
  return {
    intervalHours: boundedInteger(settings.accountHealthCheckIntervalHours, 12, 1, 168),
    jitterMinutes: boundedInteger(settings.accountHealthCheckJitterMinutes, 120, 0, 1440),
    failureThreshold: boundedInteger(settings.accountHealthCheckFailureThreshold, 3, 1, 10)
  }
}

function defaultHealthCheckSettings(): AccountHealthCheckSettings {
  return { intervalHours: 12, jitterMinutes: 120, failureThreshold: 3 }
}

function boundedInteger(value: unknown, fallback: number, minimum: number, maximum: number): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(minimum, Math.min(maximum, Math.trunc(parsed)))
}
