import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { ApiKeyStatusFilter } from './apiKeyTableConfig'

export interface ApiKeysPageState {
  groupFilter?: GroupSelection
  keywordFilter: string
  pagination: { current: number; pageSize: number }
  statusFilter: ApiKeyStatusFilter
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

export function defaultApiKeysPageState(pageSize: number): ApiKeysPageState {
  return {
    groupFilter: undefined,
    keywordFilter: '',
    pagination: { current: 1, pageSize },
    statusFilter: 'all',
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined
  }
}
