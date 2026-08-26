import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { RouteStrategySelection } from '@/shared/routeStrategyLabelCache'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { ApiKeyStatusFilter } from './apiKeyTableConfig'

export type ApiKeyRouteStrategyFilterSelection = RouteStrategySelection

export interface ApiKeysPageState {
  keywordFilter: string
  pagination: { current: number; pageSize: number }
  routeStrategyFilter?: ApiKeyRouteStrategyFilterSelection
  statusFilter: ApiKeyStatusFilter
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

export function defaultApiKeysPageState(pageSize: number): ApiKeysPageState {
  return {
    keywordFilter: '',
    pagination: { current: 1, pageSize },
    routeStrategyFilter: undefined,
    statusFilter: 'all',
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined
  }
}
