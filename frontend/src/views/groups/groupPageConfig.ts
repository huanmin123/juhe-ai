import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { ProviderDefinition } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

export interface GroupsPagePaginationState {
  current: number
  pageSize: number
}

export type GroupsPageState = {
  pagination?: GroupsPagePaginationState
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

export const groupsPageSize = 50

export function defaultGroupsPageState(): GroupsPageState {
  return {
    pagination: { current: 1, pageSize: groupsPageSize },
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined
  }
}

export function isAllGroupsSystemAccountFilter(value: string): boolean {
  return value === allSystemAccountsValue
}

export function groupsActiveFilterCount(systemAccountFilter: string): number {
  return isAllGroupsSystemAccountFilter(systemAccountFilter) ? 0 : 1
}

export function groupsListParams(systemAccountId: string | undefined, pageState: GroupsPagePaginationState) {
  return {
    systemAccountId,
    page: pageState.current,
    pageSize: pageState.pageSize
  }
}

export function groupsProviderOptions(providers: ProviderDefinition[]): Array<{ label: string; value: string; disabled: boolean }> {
  return providers.map((provider) => ({
    label: provider.name,
    value: provider.code,
    disabled: !provider.enabled
  }))
}

export function groupsTableColumns(isManagementView: boolean): Array<Record<string, unknown>> {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '分组名称', dataIndex: 'name', key: 'name', width: 240, fixed: 'left', customHeaderCell: () => ({ class: 'group-name-header-cell' }) },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 120 },
    { title: '类型', dataIndex: 'groupType', key: 'groupType', width: 130 }
  ]
  if (isManagementView) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '账户数', key: 'accountCount', width: 130 },
    { title: '当前并发', key: 'concurrency', width: 100 },
    { title: '用量(日)', key: 'usage', width: 180 },
    { title: '状态', key: 'status', width: 100 },
    { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
    { title: '操作', key: 'actions', fixed: 'right' }
  )
  return baseColumns
}
