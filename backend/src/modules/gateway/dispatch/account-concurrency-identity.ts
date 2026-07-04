export interface GatewayAccountConcurrencyIdentity {
  id: string
  credentialSourceAccountId?: string
}

export interface GatewayAccountConcurrencyLimitIdentity extends GatewayAccountConcurrencyIdentity {
  concurrencyLimit: number
}

export function gatewayAccountConcurrencyAccountId(account: GatewayAccountConcurrencyIdentity): string {
  return normalizedAccountId(account.credentialSourceAccountId) ?? account.id
}

export function gatewayAccountConcurrencyAccountIds(accounts: GatewayAccountConcurrencyIdentity[]): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  for (const account of accounts) {
    const accountId = gatewayAccountConcurrencyAccountId(account)
    if (!accountId || seen.has(accountId)) {
      continue
    }
    seen.add(accountId)
    result.push(accountId)
  }
  return result
}

export function gatewayAccountConcurrencyLimitsByAccountId(accounts: GatewayAccountConcurrencyLimitIdentity[]): Record<string, number> {
  const result: Record<string, number> = {}
  for (const account of accounts) {
    const accountId = gatewayAccountConcurrencyAccountId(account)
    if (!accountId) {
      continue
    }
    const limit = normalizedPositiveInteger(account.concurrencyLimit)
    result[accountId] = result[accountId] === undefined
      ? limit
      : Math.min(result[accountId], limit)
  }
  return result
}

function normalizedAccountId(value: string | undefined): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedPositiveInteger(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}
