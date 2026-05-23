export const allSystemAccountsValue = 'all'

export interface SystemAccountScopedItem {
  systemAccountId?: string
  systemAccountName?: string
}

export function selectedSystemAccountId(filterValue: string, isAdmin: boolean): string | undefined {
  const value = filterValue.trim()
  if (!isAdmin || !value || value === allSystemAccountsValue) return undefined
  return value
}

export function matchesSystemAccountFilter(item: SystemAccountScopedItem, filterValue: string, isAdmin: boolean): boolean {
  const systemAccountId = selectedSystemAccountId(filterValue, isAdmin)
  return !systemAccountId || item.systemAccountId === systemAccountId
}

export function systemAccountDisplayText(item: SystemAccountScopedItem): string {
  return item.systemAccountName || '-'
}
