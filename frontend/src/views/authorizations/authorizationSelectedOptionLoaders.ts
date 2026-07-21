import { api } from '@/api/client'
import type { AccountOptionSummary, AuthorizationGranteeGroupOptionSummary, GroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
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

export async function ensureSelectedAuthorizationGranteeGroupOption(
  options: AuthorizationGranteeGroupOptionSummary[],
  selectedId: string | undefined,
  granteeSystemAccountId: string,
  providerCode: string,
  isManagementView: boolean
): Promise<AuthorizationGranteeGroupOptionSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView
      ? await api.authorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, ids: [id], limit: 1, preferDefault: true })
      : await api.myAuthorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, ids: [id], limit: 1, preferDefault: true })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

export async function ensureSelectedSystemAccountPrincipal(
  options: SystemAccountPrincipalSummary[],
  selectedId: string | undefined,
  isManagementView: boolean
): Promise<SystemAccountPrincipalSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView
      ? await api.authorizationOptions.granteeAccounts({ ids: [id], limit: 1 })
      : await api.myAuthorizationOptions.granteeAccounts({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

export async function ensureSelectedTeamOption(
  options: SystemTeamPrincipalSummary[],
  selectedId: string | undefined,
  isManagementView: boolean
): Promise<SystemTeamPrincipalSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView
      ? await api.authorizationOptions.granteeTeams({ ids: [id], limit: 1 })
      : await api.myAuthorizationOptions.granteeTeams({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}
