export interface GatewayDispatchPriorityAccount {
  id: string
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
}

export function preserveGatewayAccountDispatchPriorityTiers<T extends GatewayDispatchPriorityAccount>(
  baseAccounts: readonly T[],
  reorderedAccounts: readonly T[]
): T[] {
  if (baseAccounts.length < 2 || reorderedAccounts.length < 2) {
    return [...reorderedAccounts]
  }

  const baseTierOrder: string[] = []
  const seenBaseTiers = new Set<string>()
  for (const account of baseAccounts) {
    const tier = gatewayAccountDispatchPriorityTier(account)
    if (seenBaseTiers.has(tier)) {
      continue
    }
    seenBaseTiers.add(tier)
    baseTierOrder.push(tier)
  }

  const reorderedByTier = new Map<string, T[]>()
  const unknownTierAccounts: T[] = []
  for (const account of reorderedAccounts) {
    const tier = gatewayAccountDispatchPriorityTier(account)
    if (!seenBaseTiers.has(tier)) {
      unknownTierAccounts.push(account)
      continue
    }
    const bucket = reorderedByTier.get(tier)
    if (bucket) {
      bucket.push(account)
    } else {
      reorderedByTier.set(tier, [account])
    }
  }

  const output: T[] = []
  for (const tier of baseTierOrder) {
    const bucket = reorderedByTier.get(tier)
    if (bucket) {
      output.push(...bucket)
    }
  }
  output.push(...unknownTierAccounts)
  return output
}

function gatewayAccountDispatchPriorityTier(account: GatewayDispatchPriorityAccount): string {
  const fallbackRank = account.fallbackEnabled === true ? 1 : 0
  const superRank = account.superPriorityEnabled === true ? 0 : 1
  const priority = typeof account.priority === 'number' && Number.isFinite(account.priority)
    ? Math.trunc(account.priority)
    : 0
  return `${fallbackRank}:${superRank}:${priority}`
}
