const currentConcurrencyByAccountId = new Map<string, number>()
const inFlightSlotsByAccountId = new Map<string, Map<number, AccountInFlightSlot>>()
const releaseListeners = new Set<(accountId: string) => void>()
let nextSlotId = 1

export interface AccountConcurrencySlot {
  acquired: boolean
  current: number
  limit: number
  release: () => void
  markFirstOutput: () => void
}

interface AccountInFlightSlot {
  slotId: number
  startedAtMs: number
  firstOutputAtMs?: number
}

export interface AccountInFlightStats {
  currentConcurrency: number
  slowInFlightCount: number
  firstOutputSlowCount: number
  oldestInFlightMs: number
}

export function tryAcquireAccountConcurrency(accountId: string, concurrencyLimit: number): AccountConcurrencySlot {
  const limit = normalizeConcurrencyLimit(concurrencyLimit)
  const current = getAccountCurrentConcurrency(accountId)
  if (current >= limit) {
    return {
      acquired: false,
      current,
      limit,
      release: noop,
      markFirstOutput: noop
    }
  }

  currentConcurrencyByAccountId.set(accountId, current + 1)
  const slotId = nextSlotId
  nextSlotId += 1
  const accountSlots = inFlightSlotsByAccountId.get(accountId) ?? new Map<number, AccountInFlightSlot>()
  accountSlots.set(slotId, { slotId, startedAtMs: Date.now() })
  inFlightSlotsByAccountId.set(accountId, accountSlots)
  let released = false
  return {
    acquired: true,
    current: current + 1,
    limit,
    markFirstOutput: () => markAccountConcurrencyFirstOutput(accountId, slotId),
    release: () => {
      if (released) return
      released = true
      releaseAccountConcurrency(accountId, slotId)
    }
  }
}

export function getAccountCurrentConcurrency(accountId: string): number {
  return Math.max(0, Math.trunc(currentConcurrencyByAccountId.get(accountId) ?? 0))
}

export function loadAccountCurrentConcurrencyByIds(accountIds: string[]): Map<string, number> {
  const result = new Map<string, number>()
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    result.set(accountId, getAccountCurrentConcurrency(accountId))
  }
  return result
}

export function loadAccountInFlightStatsByIds(accountIds: string[], input: {
  slowRequestThresholdMs: number
  firstOutputSlowThresholdMs: number
}): Map<string, AccountInFlightStats> {
  const result = new Map<string, AccountInFlightStats>()
  const now = Date.now()
  const slowRequestThresholdMs = normalizePositiveDuration(input.slowRequestThresholdMs, 30_000)
  const firstOutputSlowThresholdMs = normalizePositiveDuration(input.firstOutputSlowThresholdMs, 15_000)
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    const slots = inFlightSlotsByAccountId.get(accountId)
    let slowInFlightCount = 0
    let firstOutputSlowCount = 0
    let oldestInFlightMs = 0
    if (slots) {
      for (const slot of slots.values()) {
        const ageMs = Math.max(0, now - slot.startedAtMs)
        oldestInFlightMs = Math.max(oldestInFlightMs, ageMs)
        if (ageMs >= slowRequestThresholdMs) {
          slowInFlightCount += 1
        }
        if (slot.firstOutputAtMs === undefined && ageMs >= firstOutputSlowThresholdMs) {
          firstOutputSlowCount += 1
        }
      }
    }
    result.set(accountId, {
      currentConcurrency: getAccountCurrentConcurrency(accountId),
      slowInFlightCount,
      firstOutputSlowCount,
      oldestInFlightMs
    })
  }
  return result
}

export function subscribeAccountConcurrencyRelease(listener: (accountId: string) => void): () => void {
  releaseListeners.add(listener)
  return () => {
    releaseListeners.delete(listener)
  }
}

export function snapshotAccountConcurrency(): Record<string, number> {
  const snapshot: Record<string, number> = {}
  for (const [accountId, current] of currentConcurrencyByAccountId.entries()) {
    const normalized = Math.max(0, Math.trunc(current))
    if (accountId && normalized > 0) {
      snapshot[accountId] = normalized
    }
  }
  return snapshot
}

export function sumAccountCurrentConcurrency(accountIds: string[], concurrencyByAccount = loadAccountCurrentConcurrencyByIds(accountIds)): number {
  let total = 0
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    total += concurrencyByAccount.get(accountId) ?? 0
  }
  return total
}

export function clearAccountConcurrency(): void {
  currentConcurrencyByAccountId.clear()
  inFlightSlotsByAccountId.clear()
}

function markAccountConcurrencyFirstOutput(accountId: string, slotId: number): void {
  const slot = inFlightSlotsByAccountId.get(accountId)?.get(slotId)
  if (!slot || slot.firstOutputAtMs !== undefined) {
    return
  }
  slot.firstOutputAtMs = Date.now()
}

function releaseAccountConcurrency(accountId: string, slotId: number): void {
  const slots = inFlightSlotsByAccountId.get(accountId)
  if (slots) {
    slots.delete(slotId)
    if (slots.size === 0) {
      inFlightSlotsByAccountId.delete(accountId)
    }
  }
  const current = getAccountCurrentConcurrency(accountId)
  if (current <= 1) {
    currentConcurrencyByAccountId.delete(accountId)
  } else {
    currentConcurrencyByAccountId.set(accountId, current - 1)
  }
  notifyAccountConcurrencyReleased(accountId)
}

function notifyAccountConcurrencyReleased(accountId: string): void {
  for (const listener of releaseListeners) {
    try {
      listener(accountId)
    } catch {
      // Release must never fail because a scheduler observer failed.
    }
  }
}

function normalizeConcurrencyLimit(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}

function normalizePositiveDuration(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : fallback
}

function noop(): void {}
