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
      const reservation = createReservation(target, slot)
      ownershipTransferred = true
      return reservation
    }
    return undefined
  } finally {
    if (!ownershipTransferred) {
      acquiredSlot?.release()
    }
  }
}

export function clearSpeedFirstCutoverReservationsForTest(): void {
  speedFirstCutoverSlotAcquirerForTest = undefined
}

// Kept for regression consumers that assert no process-local cutover gate remains.
export function speedFirstCutoverBudgetSnapshot(): Array<{ key: string; active: number }> {
  return []
}

export function setSpeedFirstCutoverSlotAcquirerForTest(acquirer?: SpeedFirstCutoverSlotAcquirer): void {
  speedFirstCutoverSlotAcquirerForTest = acquirer
}

function createReservation(
  target: GatewayAccountConcurrencyLimitIdentity,
  slot: AccountConcurrencySlot
): SpeedFirstCutoverReservation {
  let consumed = false
  let released = false
  const release = () => {
    if (released) return
    released = true
    slot.release()
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
