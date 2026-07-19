import { http, unwrap } from '../http'

export const pageDataDomains = [
  'accounts.static',
  'accounts.runtime',
  'accounts.options',
  'usage.records',
  'announcements.public',
  'providers.catalog',
  'groups.static',
  'systemAccounts.options',
  'teams.options',
  'routeStrategies.options',
  'stats.overview',
  'stats.accountUsage',
  'stats.aiPerformance'
] as const

export type PageDataDomain = typeof pageDataDomains[number]
export type PageDataViewScope = 'self' | 'admin'
export type PageDataConfirmAction = 'unchanged' | 'delta' | 'reload' | 'reset'

export interface PageDataRevisionToken {
  protocolVersion: number
  epoch: string
  scope: string
  domain: PageDataDomain
  sequence: number
  resetSequence: number
}

export interface PageDataChangeProjection {
  entityId?: string
  operation: 'upsert' | 'delete' | 'append' | 'range_reset' | 'window_replace'
  fieldMask: string[]
  membershipChanged: boolean
  orderChanged: boolean
  filterChanged: boolean
  pageChanged: boolean
}

export interface PageDataConfirmDomainResult {
  action: PageDataConfirmAction
  token: PageDataRevisionToken
  changes?: PageDataChangeProjection[]
}

export interface PageDataConfirmResult {
  serverTime: string
  domains: Partial<Record<PageDataDomain, PageDataConfirmDomainResult>>
}

export interface PageDataConfirmRequest {
  viewScope: PageDataViewScope
  targetSystemAccountId?: string
  domains: Partial<Record<PageDataDomain, PageDataRevisionToken | null>>
}

export const pageDataApi = {
  confirm: (payload: PageDataConfirmRequest) => unwrap<PageDataConfirmResult>(http.post('/data-changes/confirm', payload))
}
