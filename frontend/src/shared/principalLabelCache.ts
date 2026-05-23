import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'

export type PrincipalKind = 'system_account' | 'team'

export interface PrincipalSelection {
  id: string
  name: string
  kind: PrincipalKind
}

const caches = new Map<PrincipalKind, Map<string, string>>([
  ['system_account', new Map()],
  ['team', new Map()]
])

export function rememberPrincipalLabel(kind: PrincipalKind, id: string | undefined, name: string | undefined): void {
  const normalizedId = id?.trim()
  const normalizedName = name?.trim()
  if (!normalizedId || !normalizedName) return
  caches.get(kind)?.set(normalizedId, normalizedName)
}

export function rememberPrincipalSelection(selection: PrincipalSelection | undefined): void {
  rememberPrincipalLabel(selection?.kind ?? 'system_account', selection?.id, selection?.name)
}

export function rememberPrincipalSelections(selections: Array<PrincipalSelection | undefined>): void {
  for (const selection of selections) {
    rememberPrincipalSelection(selection)
  }
}

export function rememberSystemAccountPrincipals(accounts: SystemAccountPrincipalSummary[]): void {
  for (const account of accounts) {
    rememberPrincipalLabel('system_account', account.id, systemAccountPrincipalName(account))
  }
}

export function rememberSystemTeamPrincipals(teams: SystemTeamPrincipalSummary[]): void {
  for (const team of teams) {
    rememberPrincipalLabel('team', team.id, team.name)
  }
}

export function principalLabelForId(kind: PrincipalKind, id: string | undefined): string | undefined {
  const normalizedId = id?.trim()
  return normalizedId ? caches.get(kind)?.get(normalizedId) : undefined
}

export function systemAccountPrincipalName(account: SystemAccountPrincipalSummary): string {
  return account.displayName
}
