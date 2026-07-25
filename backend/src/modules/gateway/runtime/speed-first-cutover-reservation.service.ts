import { effectiveImageLaneConcurrencyLimit } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../../domain/types.js'
import {
  tryAcquireAccountConcurrencyAsync,
  type AccountConcurrencyLane,
  type AccountConcurrencySlot
} from '../../../shared/account-concurrency.js'
import {
  gatewayAccountConcurrencyAccountId,
  type GatewayAccountConcurrencyLimitIdentity
} from '../dispatch/account-concurrency-identity.js'

const maxConcurrentCutoversPerScope = 2
const activeBudgetByScope = new Map<string, number>()
type SpeedFirstCutoverSlotAcquirer = typeof tryAcquireAccountConcurrencyAsync
let speedFirstCutoverSlotAcquirerForTest: SpeedFirstCutoverSlotAcquirer | undefined

export interface SpeedFirstCutoverReservation {
  readonly targetAccountId: string
  readonly consumed: boolean
  takeForAccount: (account: GatewayAccountConcurrencyLimitIdentity) => AccountConcurrencySlot | undefined
  release: () => void
}

export async function reserveSpeedFirstCutoverTarget(input: {
  systemAccountId: string
  routeStrategyId: string
  groupId: string
  slowAccountId: string
  targets: GatewayAccountConcurrencyLimitIdentity[]
  lane: AccountConcurrencyLane
  groupSchedulingPolicy?: GroupSchedulingPolicy
}): Promise<SpeedFirstCutoverReservation | undefined> {
  const budgetKey = `${input.systemAccountId}:${input.routeStrategyId}:${input.groupId}:${input.slowAccountId}`
  if (!tryAcquireBudget(budgetKey)) return undefined

  let acquiredSlot: AccountConcurrencySlot | undefined
  let ownershipTransferred = false
  try {
    for (const target of input.targets) {
      const slotAcquirer = speedFirstCutoverSlotAcquirerForTest ?? tryAcquireAccountConcurrencyAsync
      const slot = await slotAcquirer(
        gatewayAccountConcurrencyAccountId(target),
        target.concurrencyLimit,
        input.lane === 'image'
          ? {
              lane: 'image',
              laneLimit: effectiveImageLaneConcurrencyLimit({
                accountConcurrencyLimit: target.concurrencyLimit,
                policy: input.groupSchedulingPolicy
              })
            }
          : { lane: 'text' }
      )
      if (!slot.acquired) continue
      acquiredSlot = slot
      const reservation = createReservation(target, slot, () => releaseBudget(budgetKey))
      ownershipTransferred = true
      return reservation
    }
    return undefined
  } finally {
    if (!ownershipTransferred) {
      try {
        acquiredSlot?.release()
      } finally {
        releaseBudget(budgetKey)
      }
    }
  }
}

export function speedFirstCutoverBudgetSnapshot(): Array<{ key: string; active: number }> {
  return [...activeBudgetByScope.entries()].map(([key, active]) => ({ key, active }))
}

export function clearSpeedFirstCutoverReservationsForTest(): void {
  activeBudgetByScope.clear()
  speedFirstCutoverSlotAcquirerForTest = undefined
}

export function setSpeedFirstCutoverSlotAcquirerForTest(acquirer?: SpeedFirstCutoverSlotAcquirer): void {
  speedFirstCutoverSlotAcquirerForTest = acquirer
}

function createReservation(
  target: GatewayAccountConcurrencyLimitIdentity,
  slot: AccountConcurrencySlot,
  releaseBudgetLease: () => void
): SpeedFirstCutoverReservation {
  let consumed = false
  let released = false
  const release = () => {
    if (released) return
    released = true
    try {
      slot.release()
    } finally {
      releaseBudgetLease()
    }
  }
  const reservedSlot: AccountConcurrencySlot = {
    ...slot,
    release
  }
  return {
    targetAccountId: target.id,
    get consumed() {
      return consumed
    },
    takeForAccount: (account) => {
      if (consumed || released || account.id !== target.id) return undefined
      consumed = true
      return reservedSlot
    },
    release
  }
}

function tryAcquireBudget(key: string): boolean {
  const current = activeBudgetByScope.get(key) ?? 0
  if (current >= maxConcurrentCutoversPerScope) return false
  activeBudgetByScope.set(key, current + 1)
  return true
}

function releaseBudget(key: string): void {
  const current = activeBudgetByScope.get(key) ?? 0
  if (current <= 1) activeBudgetByScope.delete(key)
  else activeBudgetByScope.set(key, current - 1)
}
