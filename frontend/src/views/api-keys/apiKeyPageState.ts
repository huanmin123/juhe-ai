import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { RouteStrategyMode, RouteStrategyStatus } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { ApiKeyStatusFilter } from './apiKeyTableConfig'

export interface ApiKeyRouteStrategyFilterSelection {
  id: string
  name: string
  mode: RouteStrategyMode
  status?: RouteStrategyStatus
  isDefault?: boolean
  systemAccountName?: string
}

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
