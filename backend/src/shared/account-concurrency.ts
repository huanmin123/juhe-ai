const currentConcurrencyByAccountId = new Map<string, number>()

export interface AccountConcurrencySlot {
  acquired: boolean
  current: number
  limit: number
  release: () => void
}

export function tryAcquireAccountConcurrency(accountId: string, concurrencyLimit: number): AccountConcurrencySlot {
  const limit = normalizeConcurrencyLimit(concurrencyLimit)
  const current = getAccountCurrentConcurrency(accountId)
  if (current >= limit) {
    return {
      acquired: false,
      current,
      limit,
      release: noop
    }
  }

  currentConcurrencyByAccountId.set(accountId, current + 1)
  let released = false
  return {
    acquired: true,
    current: current + 1,
    limit,
    release: () => {
      if (released) return
      released = true
      releaseAccountConcurrency(accountId)
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

export function sumAccountCurrentConcurrency(accountIds: string[], concurrencyByAccount = loadAccountCurrentConcurrencyByIds(accountIds)): number {
  let total = 0
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    total += concurrencyByAccount.get(accountId) ?? 0
  }
  return total
}

export function clearAccountConcurrency(): void {
  currentConcurrencyByAccountId.clear()
}

function releaseAccountConcurrency(accountId: string): void {
  const current = getAccountCurrentConcurrency(accountId)
  if (current <= 1) {
    currentConcurrencyByAccountId.delete(accountId)
    return
  }
  currentConcurrencyByAccountId.set(accountId, current - 1)
}

function normalizeConcurrencyLimit(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}

function noop(): void {}
