import type { SystemAccountSummary } from '@/types/domain'

export const allSystemAccountsValue = 'all'

export interface SystemAccountScopedItem {
  systemAccountId?: string
  systemAccountName?: string
}

export interface SystemAccountOption {
  label: string
  value: string
}

export function buildSystemAccountOptions(accounts: SystemAccountSummary[]): SystemAccountOption[] {
  return [
    { label: '全部系统账户', value: allSystemAccountsValue },
    ...accounts.map((account) => ({
      label: systemAccountOptionLabel(account),
      value: account.id
    }))
  ]
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
  return item.systemAccountName || item.systemAccountId || '-'
}

function systemAccountOptionLabel(account: SystemAccountSummary): string {
  const displayName = account.displayName || account.username
  if (displayName === account.username) return displayName
  return `${displayName}（${account.username}）`
}
