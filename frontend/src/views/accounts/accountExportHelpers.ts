import type { AccountExportFilters, AccountListSortParam } from '@/api/client'
import type { AccountListItem } from '@/types/domain'

export const ACCOUNT_EXPORT_MAX_ACCOUNTS = 500

export interface AccountExportFilterState {
  keyword: string
  providerCode: string
  type: string
  groupId: string
  tagIds: string[]
  status: string[]
}

export interface AccountExportTargetOption {
  id: string
  displayName?: string
}

export interface AccountExportTargetState {
  selectedSystemAccountId?: string
  selectedSystemAccountName?: string
  systemAccounts: AccountExportTargetOption[]
}

export function accountExportFiltersFromState(filters: AccountExportFilterState, sorts: AccountListSortParam[]): AccountExportFilters {
  return {
    sorts,
    keyword: filters.keyword.trim() || undefined,
    providerCode: filters.providerCode && filters.providerCode !== 'all' ? filters.providerCode : undefined,
    type: filters.type && filters.type !== 'all' ? filters.type : undefined,
    groupId: filters.groupId || undefined,
    tagIds: filters.tagIds.length ? filters.tagIds : undefined,
    status: filters.status.length ? filters.status : undefined
  }
}

export function accountExportPayloadByIds(accounts: AccountListItem[]): { accountIds: string[] } {
  return { accountIds: accounts.map((account) => account.id) }
}

export function accountExportFilename(accountCount: number, targetState: AccountExportTargetState): string {
  const target = accountExportTargetSystemAccountLabel(targetState)
  const safeTarget = target.replace(/[<>:"/\\|?*\u0000-\u001f]+/g, '-').replace(/\s+/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
  const timestamp = new Date().toISOString().replace(/[:.]/g, '-')
  return `${safeTarget || 'AI账户'}-${accountCount}个账户-${timestamp}.json`
}

export function downloadJsonFile(filename: string, data: unknown): void {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

function accountExportTargetSystemAccountLabel(targetState: AccountExportTargetState): string {
  const { selectedSystemAccountId, selectedSystemAccountName, systemAccounts } = targetState
  if (!selectedSystemAccountId) return 'AI账户'
  const account = systemAccounts.find((item) => item.id === selectedSystemAccountId)
  return account?.displayName || selectedSystemAccountName || 'AI账户'
}
