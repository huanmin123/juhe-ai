import type { AuthorizationListParams } from '@/api/contracts'

import type {
  AuthorizationDirectionFilter,
  AuthorizationFilterResourceType,
  AuthorizationSourceFilter,
  AuthorizationStatusFilter
} from './authorizationTableColumns'

export interface AuthorizationListFilterValues {
  direction: AuthorizationDirectionFilter
  sourceType: AuthorizationSourceFilter
  status: AuthorizationStatusFilter
  resourceType: AuthorizationFilterResourceType
  resourceOwnerSystemAccountId: string
  resourceId?: string
  teamId?: string
  granteeSystemAccountId?: string
}

export interface AuthorizationListFilterContext {
  filters: AuthorizationListFilterValues
  keyword: string
  isManagementView: boolean
  filterResourceDisabled: boolean
  allSystemAccountsValue: string
}

export interface AuthorizationListParamsContext extends AuthorizationListFilterContext {
  selectedResourceOwnerSystemAccountId?: string
  pageState: { current: number; pageSize: number }
}

export function activeAuthorizationFilterCount(context: AuthorizationListFilterContext): number {
  const { filters, keyword, isManagementView, filterResourceDisabled, allSystemAccountsValue } = context
  let count = 0
  if (keyword.trim()) count += 1
  if (!isManagementView && filters.direction !== 'outbound') count += 1
  if (!isManagementView && filters.sourceType !== 'all') count += 1
  if (filters.status !== 'all') count += 1
  if (isManagementView && filters.resourceOwnerSystemAccountId !== allSystemAccountsValue) count += 1
  if (filters.resourceType !== 'all') count += 1
  if (!filterResourceDisabled && filters.resourceId) count += 1
  if (isManagementView && filters.teamId) count += 1
  if (isManagementView && filters.granteeSystemAccountId) count += 1
  return count
}

export function advancedAuthorizationFilterCount(context: AuthorizationListFilterContext): number {
  const { filters, isManagementView, filterResourceDisabled, allSystemAccountsValue } = context
  let count = 0
  if (!isManagementView && filters.sourceType !== 'all') count += 1
  if (filters.status !== 'all') count += 1
  if (isManagementView && filters.resourceType !== 'all') count += 1
  if (isManagementView && filters.resourceOwnerSystemAccountId !== allSystemAccountsValue) count += 1
  if (!filterResourceDisabled && filters.resourceId) count += 1
  if (isManagementView && filters.teamId) count += 1
  if (isManagementView && filters.granteeSystemAccountId) count += 1
  return count
}

export function authorizationEmptyDescription(context: {
  filters: Pick<AuthorizationListFilterValues, 'direction'>
  isManagementView: boolean
  activeFilterCount: number
}): string {
  const { filters, isManagementView, activeFilterCount } = context
  if (isManagementView) {
    return activeFilterCount > 0 ? '没有符合当前筛选条件的授权记录。' : '暂无授权记录。'
  }
  if (filters.direction === 'inbound') {
    return '暂无授权给我的记录；获得授权后的资源会在对应使用菜单中显示。'
  }
  return activeFilterCount > 0 ? '没有符合当前筛选条件的授权记录。' : '暂无我授权出去的记录，可新增授权给其他用户或团队。'
}

export function authorizationListParams(context: AuthorizationListParamsContext): AuthorizationListParams {
  const { filters, keyword, isManagementView, filterResourceDisabled, selectedResourceOwnerSystemAccountId, pageState } = context
  return {
    keyword: keyword.trim() || undefined,
    resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
    resourceId: filters.resourceType === 'all' || filterResourceDisabled ? undefined : filters.resourceId,
    resourceOwnerSystemAccountId: isManagementView ? selectedResourceOwnerSystemAccountId : undefined,
    teamId: isManagementView ? filters.teamId : undefined,
    granteeSystemAccountId: isManagementView ? filters.granteeSystemAccountId : undefined,
    status: filters.status === 'all' ? undefined : filters.status,
    direction: isManagementView ? undefined : filters.direction,
    sourceType: !isManagementView && filters.sourceType !== 'all' ? filters.sourceType : undefined,
    page: pageState.current,
    pageSize: pageState.pageSize
  }
}
