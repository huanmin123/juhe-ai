import { api } from '@/api/client'
import type { AccountOptionSummary, GroupOptionSummary } from '@/types/domain'
import { mergeOptionsById } from './authorizationOptionHelpers'

export async function ensureSelectedAccountOption(
  options: AccountOptionSummary[],
  selectedId: string | undefined,
  systemAccountId: string | undefined,
  isManagementView: boolean
): Promise<AccountOptionSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView
      ? await api.accounts.options({ systemAccountId, ids: [id], limit: 1 })
      : await api.myAccounts.options({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

export async function ensureSelectedGroupOption(
  options: GroupOptionSummary[],
  selectedId: string | undefined,
  systemAccountId: string | undefined,
  isManagementView: boolean
): Promise<GroupOptionSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView
      ? await api.groups.authorizationOptions({ systemAccountId, ids: [id], limit: 1 })
      : await api.myGroups.authorizationOptions({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}
