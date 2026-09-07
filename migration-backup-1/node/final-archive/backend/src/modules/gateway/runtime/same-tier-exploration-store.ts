export const SAME_TIER_EXPLORATION_STATE_TTL_MS = 40 * 60_000
export const SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS = 60_000
export const SAME_TIER_EXPLORATION_CREDIT_INCREMENT = 0.05
export const SAME_TIER_EXPLORATION_CREDIT_COST = 1
export const SAME_TIER_EXPLORATION_CREDIT_CAP = 1
export const SAME_TIER_EXPLORATION_POOL_CAPACITY = 10_000
export const SAME_TIER_EXPLORATION_IDENTITY_CAPACITY = 2_048

export interface SameTierExplorationReservation {
  reservationId: string
  accountRuntimeKey: string
  leaseUntilMs: number
}

export interface SameTierExplorationState {
  poolKey: string
  credit: number
  cursor: number
  reservations: readonly SameTierExplorationReservation[]
  cooldownUntilMsByRuntimeKey: Readonly<Record<string, number>>
  accruedTokens: readonly string[]
  settledReservationIds: readonly string[]
  expiresAtMs: number
}

export type SameTierExplorationReservationStatus =
  | 'reserved'
  | 'credit_unavailable'
  | 'pool_busy'
  | 'target_cooldown'
  | 'reservation_conflict'

export type SameTierExplorationSettlementStatus =
  | 'applied'
  | 'idempotent'
  | 'reservation_not_found'
  | 'reservation_conflict'

export interface SameTierExplorationStore {
  get(input: { poolKey: string; nowMs?: number }): Promise<SameTierExplorationState>
  accrue(input: {
    poolKey: string
    accrualToken: string
    eligible: boolean
    nowMs?: number
  }): Promise<SameTierExplorationState>
  reserve(input: {
    poolKey: string
    reservationId: string
    accountRuntimeKey: string
    leaseUntilMs: number
    nowMs?: number
  }): Promise<{
    status: SameTierExplorationReservationStatus
    state: SameTierExplorationState
    reservation?: SameTierExplorationReservation
  }>
  settle(input: {
    poolKey: string
    reservationId: string
    accountRuntimeKey: string
    outcome: 'dispatched' | 'not_dispatched'
    nowMs?: number
  }): Promise<{
    status: SameTierExplorationSettlementStatus
    state: SameTierExplorationState
  }>
}

export function emptySameTierExplorationState(poolKey: string, nowMs: number): SameTierExplorationState {
  return {
    poolKey: requiredKey(poolKey, 'poolKey'),
    credit: 0,
    cursor: 0,
    reservations: [],
    cooldownUntilMsByRuntimeKey: {},
    accruedTokens: [],
    settledReservationIds: [],
    expiresAtMs: nowMs + SAME_TIER_EXPLORATION_STATE_TTL_MS
  }
}

export function normalizeSameTierExplorationState(
  input: SameTierExplorationState,
  nowMs: number
): SameTierExplorationState {
  const poolKey = requiredKey(input.poolKey, 'poolKey')
  const credit = finiteRange(input.credit, 0, SAME_TIER_EXPLORATION_CREDIT_CAP, 'credit')
  const cursor = nonNegativeInteger(input.cursor, 'cursor')
  const normalizedReservations = input.reservations.filter(Boolean).map((reservation) => ({
      reservationId: requiredKey(reservation.reservationId, 'reservationId'),
      accountRuntimeKey: requiredKey(reservation.accountRuntimeKey, 'accountRuntimeKey'),
      leaseUntilMs: nonNegativeInteger(reservation.leaseUntilMs, 'leaseUntilMs')
    }))
  const reservations = normalizedReservations.filter((reservation) => reservation.leaseUntilMs > nowMs)
  const expiredReservationIds = normalizedReservations
    .filter((reservation) => reservation.leaseUntilMs <= nowMs)
    .map((reservation) => reservation.reservationId)
  const cooldownUntilMsByRuntimeKey: Record<string, number> = {}
  for (const [runtimeKey, untilMs] of Object.entries(input.cooldownUntilMsByRuntimeKey)) {
    if (untilMs <= nowMs) continue
    cooldownUntilMsByRuntimeKey[requiredKey(runtimeKey, 'cooldown accountRuntimeKey')] = nonNegativeInteger(untilMs, 'cooldownUntilMs')
  }
  return {
    poolKey,
    credit,
    cursor,
    reservations,
    cooldownUntilMsByRuntimeKey,
    accruedTokens: uniqueBoundedKeys(input.accruedTokens, SAME_TIER_EXPLORATION_IDENTITY_CAPACITY),
    // Expired IDs are retained as fencing tombstones so a late owner cannot
    // reuse its reservation ID and settle a newer lease.
    settledReservationIds: uniqueBoundedKeys(
      [...input.settledReservationIds, ...expiredReservationIds],
      SAME_TIER_EXPLORATION_IDENTITY_CAPACITY
    ),
    expiresAtMs: Math.max(nowMs + 1, nonNegativeInteger(input.expiresAtMs, 'expiresAtMs'))
  }
}

export function cloneSameTierExplorationState(input: SameTierExplorationState): SameTierExplorationState {
  return {
    poolKey: input.poolKey,
    credit: input.credit,
    cursor: input.cursor,
    reservations: input.reservations.map((reservation) => ({ ...reservation })),
    cooldownUntilMsByRuntimeKey: { ...input.cooldownUntilMsByRuntimeKey },
    accruedTokens: [...input.accruedTokens],
    settledReservationIds: [...input.settledReservationIds],
    expiresAtMs: input.expiresAtMs
  }
}

function uniqueBoundedKeys(values: readonly string[], limit: number): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  for (const value of [...values].reverse()) {
    const normalized = requiredKey(value, '状态 identity')
    if (seen.has(normalized)) continue
    seen.add(normalized)
    result.unshift(normalized)
    if (result.length >= limit) break
  }
  return result
}

function requiredKey(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized || normalized.length > 512) throw new RangeError(`${name} 必须是 1 到 512 字符`)
  return normalized
}

function finiteRange(value: number, minimum: number, maximum: number, name: string): number {
  if (!Number.isFinite(value) || value < minimum || value > maximum) throw new RangeError(`${name} 超出范围`)
  return Math.round(value * 1_000_000) / 1_000_000
}

function nonNegativeInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new RangeError(`${name} 必须是非负安全整数`)
  return value
}
