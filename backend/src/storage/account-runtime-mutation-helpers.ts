import { randomUUID } from 'node:crypto'

import type { AccountStatus } from '../domain/types.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { isCoolingAccountStatus } from './account-status.js'
import { getSettings, getSettingsAsync } from './settings.repository.js'

const temporaryUnavailableInitialBackoffSeconds = 3

export function isAccountExpired(accountExpiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!accountExpiresAt) return false
  const timestamp = Date.parse(accountExpiresAt)
  return Number.isFinite(timestamp) && timestamp <= now
}

export function defaultTemporaryUnschedulableMinutes(): number {
  return normalizeDefaultTemporaryUnschedulableMinutes(getSettings().defaultTemporaryUnschedulableMinutes)
}

export async function defaultTemporaryUnschedulableMinutesAsync(): Promise<number> {
  const settings = await getSettingsAsync()
  return normalizeDefaultTemporaryUnschedulableMinutes(settings.defaultTemporaryUnschedulableMinutes)
}

function normalizeDefaultTemporaryUnschedulableMinutes(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须是整数')
  }
  if (value < 1 || value > 1440) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须在 1 到 1440 之间')
  }
  return value
}

export function temporaryUnavailableRuntimeState(nowMs = Date.now()): { cooldownUntil: string; observationStartedAt: string } {
  return {
    cooldownUntil: new Date(nowMs + temporaryUnavailableInitialBackoffSeconds * 1000).toISOString(),
    observationStartedAt: new Date(nowMs).toISOString()
  }
}

export function newCooldownRetestGeneration(): string {
  return `cooldown:${randomUUID()}`
}

export function initialCooldownUntilForStatus(status: AccountStatus, nowMs = Date.now()): string | undefined {
  if (status === 'temporary_unavailable') {
    return temporaryUnavailableRuntimeState(nowMs).cooldownUntil
  }
  if (status === 'rate_limited') {
    return new Date(nowMs + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
  }
  return undefined
}

export async function initialCooldownUntilForStatusAsync(status: AccountStatus, nowMs = Date.now()): Promise<string | undefined> {
  if (status === 'temporary_unavailable') {
    return temporaryUnavailableRuntimeState(nowMs).cooldownUntil
  }
  if (status === 'rate_limited') {
    const minutes = await defaultTemporaryUnschedulableMinutesAsync()
    return new Date(nowMs + minutes * 60_000).toISOString()
  }
  return undefined
}

export function cooldownRetestObservationStartedAtForStatus(status: AccountStatus, nowMs = Date.now()): string | undefined {
  return isCoolingAccountStatus(status) ? new Date(nowMs).toISOString() : undefined
}

export function invalidateGatewayRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
}
