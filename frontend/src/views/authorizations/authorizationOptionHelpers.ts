import { accountSelectionForId, type AccountSelection } from '@/shared/accountLabelCache'
import { systemAccountPrincipalName, type PrincipalSelection } from '@/shared/principalLabelCache'
import { type AccountOptionSummary, type GroupOptionSummary, type SystemAccountPrincipalSummary, type SystemTeamPrincipalSummary } from '@/types/domain'
import { type GroupSelection } from '@/shared/groupLabelCache'

export function selectedGroupFromOptions(
  id: string | undefined,
  nextGroups: GroupOptionSummary[],
  fallback?: GroupSelection
): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = nextGroups.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  return fallback?.id === normalizedId ? fallback : undefined
}

export function selectedAccountFromOptions(
  id: string | undefined,
  nextAccounts: AccountOptionSummary[],
  fallback?: AccountSelection
): AccountSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  return accountSelectionForId(normalizedId, nextAccounts) ?? (fallback?.id === normalizedId ? fallback : undefined)
}

export function selectedTeamFromOptions(
  id: string | undefined,
  nextTeams: SystemTeamPrincipalSummary[],
  fallback?: PrincipalSelection
): PrincipalSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const team = nextTeams.find((item) => item.id === normalizedId)
  if (team) return { id: team.id, name: team.name, kind: 'team' }
  return fallback?.kind === 'team' && fallback.id === normalizedId ? fallback : undefined
}

export function selectedUserFromOptions(
  id: string | undefined,
  nextUsers: SystemAccountPrincipalSummary[],
  fallback?: PrincipalSelection
): PrincipalSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const user = nextUsers.find((item) => item.id === normalizedId)
  const userName = user ? systemAccountPrincipalName(user).trim() : ''
  if (user && userName) return { id: user.id, name: userName, kind: 'system_account' }
  return fallback?.kind === 'system_account' && fallback.id === normalizedId ? fallback : undefined
}

export function normalizeSearchKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
}

export function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}
