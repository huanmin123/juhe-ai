import {
  SAME_TIER_EXPLORATION_CREDIT_CAP,
  SAME_TIER_EXPLORATION_CREDIT_COST,
  SAME_TIER_EXPLORATION_CREDIT_INCREMENT,
  SAME_TIER_EXPLORATION_STATE_TTL_MS,
  SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS,
  cloneSameTierExplorationState,
  emptySameTierExplorationState,
  normalizeSameTierExplorationState,
  type SameTierExplorationState,
  type SameTierExplorationStore
} from './same-tier-exploration-store.js'

export interface MemorySameTierExplorationStoreOptions {
  now?: () => number
  stateTtlMs?: number
}

export class MemorySameTierExplorationStore implements SameTierExplorationStore {
  private readonly states = new Map<string, SameTierExplorationState>()
  private readonly now: () => number
  private readonly stateTtlMs: number

  constructor(options: MemorySameTierExplorationStoreOptions = {}) {
    this.now = options.now ?? Date.now
    this.stateTtlMs = positiveInteger(options.stateTtlMs ?? SAME_TIER_EXPLORATION_STATE_TTL_MS, 'stateTtlMs')
  }

  async get(input: { poolKey: string; nowMs?: number }): Promise<SameTierExplorationState> {
    const nowMs = normalizedNow(input.nowMs ?? this.now())
    const state = this.load(input.poolKey, nowMs)
    return cloneSameTierExplorationState(state)
  }

  async accrue(input: { poolKey: string; accrualToken: string; eligible: boolean; nowMs?: number }): Promise<SameTierExplorationState> {
    const nowMs = normalizedNow(input.nowMs ?? this.now())
    const state = this.load(input.poolKey, nowMs)
    const token = requiredKey(input.accrualToken, 'accrualToken')
    if (input.eligible && !state.accruedTokens.includes(token)) {
      state.credit = Math.min(SAME_TIER_EXPLORATION_CREDIT_CAP, state.credit + SAME_TIER_EXPLORATION_CREDIT_INCREMENT)
      state.accruedTokens = [...state.accruedTokens, token].slice(-2048)
    }
    state.expiresAtMs = nowMs + this.stateTtlMs
    this.states.set(state.poolKey, state)
    return cloneSameTierExplorationState(state)
  }

  async reserve(input: {
    poolKey: string
    reservationId: string
    accountRuntimeKey: string
    leaseUntilMs: number
    nowMs?: number
  }): Promise<{
    status: 'reserved' | 'credit_unavailable' | 'target_busy' | 'target_cooldown' | 'reservation_conflict'
    state: SameTierExplorationState
    reservation?: { reservationId: string; accountRuntimeKey: string; leaseUntilMs: number }
  }> {
    const nowMs = normalizedNow(input.nowMs ?? this.now())
    const state = this.load(input.poolKey, nowMs)
    const reservationId = requiredKey(input.reservationId, 'reservationId')
    const accountRuntimeKey = requiredKey(input.accountRuntimeKey, 'accountRuntimeKey')
    const leaseUntilMs = normalizedNow(input.leaseUntilMs)
    const existing = state.reservations.find((reservation) => reservation.reservationId === reservationId)
    if (existing) {
      const status = existing.accountRuntimeKey === accountRuntimeKey ? 'reserved' : 'reservation_conflict'
      return { status, state: cloneSameTierExplorationState(state), reservation: status === 'reserved' ? { ...existing } : undefined }
    }
    if (state.credit < SAME_TIER_EXPLORATION_CREDIT_COST) {
      return { status: 'credit_unavailable', state: cloneSameTierExplorationState(state) }
    }
    if (state.reservations.some((reservation) => reservation.accountRuntimeKey === accountRuntimeKey)) {
      return { status: 'target_busy', state: cloneSameTierExplorationState(state) }
    }
    if ((state.cooldownUntilMsByRuntimeKey[accountRuntimeKey] ?? 0) > nowMs) {
      return { status: 'target_cooldown', state: cloneSameTierExplorationState(state) }
    }
    const reservation = { reservationId, accountRuntimeKey, leaseUntilMs }
    state.reservations = [...state.reservations, reservation]
    state.expiresAtMs = nowMs + this.stateTtlMs
    this.states.set(state.poolKey, state)
    return { status: 'reserved', state: cloneSameTierExplorationState(state), reservation: { ...reservation } }
  }

  async settle(input: {
    poolKey: string
    reservationId: string
    accountRuntimeKey: string
    outcome: 'dispatched' | 'not_dispatched'
    nowMs?: number
  }): Promise<{ status: 'applied' | 'idempotent' | 'reservation_not_found' | 'reservation_conflict'; state: SameTierExplorationState }> {
    const nowMs = normalizedNow(input.nowMs ?? this.now())
    const state = this.load(input.poolKey, nowMs)
    const reservationId = requiredKey(input.reservationId, 'reservationId')
    const accountRuntimeKey = requiredKey(input.accountRuntimeKey, 'accountRuntimeKey')
    if (state.settledReservationIds.includes(reservationId)) {
      return { status: 'idempotent', state: cloneSameTierExplorationState(state) }
    }
    const reservation = state.reservations.find((item) => item.reservationId === reservationId)
    if (!reservation) return { status: 'reservation_not_found', state: cloneSameTierExplorationState(state) }
    if (reservation.accountRuntimeKey !== accountRuntimeKey) {
      return { status: 'reservation_conflict', state: cloneSameTierExplorationState(state) }
    }
    state.reservations = state.reservations.filter((item) => item.reservationId !== reservationId)
    state.settledReservationIds = [...state.settledReservationIds, reservationId].slice(-2048)
    state.cooldownUntilMsByRuntimeKey = {
      ...state.cooldownUntilMsByRuntimeKey,
      [accountRuntimeKey]: nowMs + SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS
    }
    if (input.outcome === 'dispatched') {
      state.credit = Math.max(0, state.credit - SAME_TIER_EXPLORATION_CREDIT_COST)
      state.cursor = state.cursor === Number.MAX_SAFE_INTEGER ? 0 : state.cursor + 1
    }
    state.expiresAtMs = nowMs + this.stateTtlMs
    this.states.set(state.poolKey, state)
    return { status: 'applied', state: cloneSameTierExplorationState(state) }
  }

  private load(poolKey: string, nowMs: number): SameTierExplorationState {
    const normalizedPoolKey = requiredKey(poolKey, 'poolKey')
    const current = this.states.get(normalizedPoolKey)
    if (!current || current.expiresAtMs <= nowMs) {
      const empty = emptySameTierExplorationState(normalizedPoolKey, nowMs)
      empty.expiresAtMs = nowMs + this.stateTtlMs
      this.states.set(normalizedPoolKey, empty)
      return empty
    }
    const normalized = normalizeSameTierExplorationState(current, nowMs)
    this.states.set(normalizedPoolKey, normalized)
    return normalized
  }
}

function requiredKey(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized || normalized.length > 512) throw new RangeError(`${name} 必须是 1 到 512 字符`)
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new RangeError('时间必须是非负安全整数')
  return value
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new RangeError(`${name} 必须是正整数`)
  return value
}
