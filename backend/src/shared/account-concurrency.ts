const currentConcurrencyByAccountId = new Map<string, number>()
const inFlightSlotsByAccountId = new Map<string, Map<number, AccountInFlightSlot>>()
const releaseListeners = new Set<(event: AccountConcurrencyReleaseEvent) => void>()
let nextSlotId = 1

export type AccountConcurrencyLane = 'text' | 'image'

export interface AccountConcurrencySlot {
  acquired: boolean
  current: number
  limit: number
  lane: AccountConcurrencyLane
  laneCurrent: number
  laneLimit: number
  release: () => void
  markFirstOutput: () => void
}

interface AccountInFlightSlot {
  slotId: number
  startedAtMs: number
  firstOutputAtMs?: number
  lane: AccountConcurrencyLane
}

export interface AccountInFlightStats {
  currentConcurrency: number
  slowInFlightCount: number
  firstOutputSlowCount: number
  oldestInFlightMs: number
}

export interface AccountConcurrencyAcquireOptions {
  lane?: AccountConcurrencyLane
  laneLimit?: number
}

export interface AccountConcurrencyReleaseEvent {
  accountId: string
  lane: AccountConcurrencyLane
}

export function tryAcquireAccountConcurrency(
  accountId: string,
  concurrencyLimit: number,
  options: AccountConcurrencyAcquireOptions = {}
): AccountConcurrencySlot {
  const limit = normalizeConcurrencyLimit(concurrencyLimit)
  const lane = normalizeConcurrencyLane(options.lane)
  const laneLimit = normalizeLaneLimit(options.laneLimit, limit, lane)
  const current = getAccountCurrentConcurrency(accountId)
  const laneCurrent = getAccountCurrentConcurrency(accountId, lane)
  if (current >= limit || laneCurrent >= laneLimit) {
    return {
      acquired: false,
      current,
      limit,
      lane,
      laneCurrent,
      laneLimit,
      release: noop,
      markFirstOutput: noop
    }
  }

  currentConcurrencyByAccountId.set(accountId, current + 1)
  const slotId = nextSlotId
  nextSlotId += 1
  const accountSlots = inFlightSlotsByAccountId.get(accountId) ?? new Map<number, AccountInFlightSlot>()
  accountSlots.set(slotId, { slotId, startedAtMs: Date.now(), lane })
  inFlightSlotsByAccountId.set(accountId, accountSlots)
  let released = false
  return {
    acquired: true,
    current: current + 1,
    limit,
    lane,
    laneCurrent: laneCurrent + 1,
    laneLimit,
    markFirstOutput: () => markAccountConcurrencyFirstOutput(accountId, slotId),
    release: () => {
      if (released) return
      released = true
      releaseAccountConcurrency(accountId, slotId)
    }
  }
}

export function getAccountCurrentConcurrency(accountId: string, lane?: AccountConcurrencyLane): number {
  if (lane) {
    const slots = inFlightSlotsByAccountId.get(accountId)
    if (!slots) return 0
    let count = 0
    for (const slot of slots.values()) {
      if (slot.lane === lane) {
        count += 1
      }
    }
    return count
  }
  return Math.max(0, Math.trunc(currentConcurrencyByAccountId.get(accountId) ?? 0))
}

export function loadAccountCurrentConcurrencyByIds(accountIds: string[], lane?: AccountConcurrencyLane): Map<string, number> {
  const result = new Map<string, number>()
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    result.set(accountId, getAccountCurrentConcurrency(accountId, lane))
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

export function subscribeAccountConcurrencyRelease(listener: (event: AccountConcurrencyReleaseEvent) => void): () => void {
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
  let releasedLane: AccountConcurrencyLane = 'text'
  if (slots) {
    releasedLane = slots.get(slotId)?.lane ?? releasedLane
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
  notifyAccountConcurrencyReleased({ accountId, lane: releasedLane })
}

function notifyAccountConcurrencyReleased(event: AccountConcurrencyReleaseEvent): void {
  for (const listener of releaseListeners) {
    try {
      listener(event)
    } catch {
      // Release must never fail because a scheduler observer failed.
    }
  }
}

function normalizeConcurrencyLimit(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}

function normalizeConcurrencyLane(value: unknown): AccountConcurrencyLane {
  return value === 'image' ? 'image' : 'text'
}

function normalizeLaneLimit(value: unknown, concurrencyLimit: number, lane: AccountConcurrencyLane): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (Number.isFinite(numeric) && numeric > 0) {
    return Math.min(concurrencyLimit, Math.max(1, Math.trunc(numeric)))
  }
  if (lane === 'image' && concurrencyLimit > 1) {
    return concurrencyLimit - 1
  }
  return concurrencyLimit
}

function normalizePositiveDuration(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : fallback
}

function noop(): void {}
