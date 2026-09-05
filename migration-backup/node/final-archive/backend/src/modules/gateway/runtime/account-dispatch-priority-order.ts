export interface GatewayDispatchPriorityAccount {
  id: string
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
}

export interface GatewayDispatchPriorityOrderOptions {
  modelRankByAccountId?: ReadonlyMap<string, number>
}

const unknownGatewayDispatchModelRank = 3

export function preserveGatewayAccountDispatchPriorityTiers<T extends GatewayDispatchPriorityAccount>(
  baseAccounts: readonly T[],
  reorderedAccounts: readonly T[],
  options: GatewayDispatchPriorityOrderOptions = {}
): T[] {
  if (baseAccounts.length < 2 || reorderedAccounts.length < 2) {
    return [...reorderedAccounts]
  }

  const baseTierOrder: string[] = []
  const seenBaseTiers = new Set<string>()
  for (const account of baseAccounts) {
    const tier = gatewayAccountDispatchPriorityTier(account, options)
    if (seenBaseTiers.has(tier)) {
      continue
    }
    seenBaseTiers.add(tier)
    baseTierOrder.push(tier)
  }

  const reorderedByTier = new Map<string, T[]>()
  const unknownTierAccounts: T[] = []
  for (const account of reorderedAccounts) {
    const tier = gatewayAccountDispatchPriorityTier(account, options)
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

export function gatewayAccountDispatchPriorityTier(
  account: GatewayDispatchPriorityAccount,
  options: GatewayDispatchPriorityOrderOptions
): string {
  const modelRank = gatewayAccountDispatchModelRank(account, options)
  const fallbackRank = account.fallbackEnabled === true ? 1 : 0
  const superRank = account.superPriorityEnabled === true ? 0 : 1
  const priority = typeof account.priority === 'number' && Number.isFinite(account.priority)
    ? Math.trunc(account.priority)
    : 0
  return `${modelRank}:${fallbackRank}:${superRank}:${priority}`
}

function gatewayAccountDispatchModelRank(
  account: GatewayDispatchPriorityAccount,
  options: GatewayDispatchPriorityOrderOptions
): number {
  if (!options.modelRankByAccountId) {
    return 0
  }
  const rank = options.modelRankByAccountId.get(account.id)
  return typeof rank === 'number' && Number.isFinite(rank)
    ? Math.max(0, Math.trunc(rank))
    : unknownGatewayDispatchModelRank
}
