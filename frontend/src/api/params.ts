import type { ModelCheckRunListParams } from '@/types/domain'
import type {
  AccountListParams,
  AccountOptionParams,
  AccountUsageStatsParams,
  AiPerformanceAccountOptionsParams,
  AiPerformanceParams,
  AuthSessionListParams,
  AuthorizationGranteeGroupOptionsParams,
  AuthorizationPrincipalOptionsParams,
  GroupListParams,
  GroupOptionParams,
  OperationLogListParams,
  SystemAccountListParams,
  SystemAccountOptionsParams,
  TeamListParams
} from './contracts'

export function stripSystemAccountParam<T extends object>(params?: T): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output = { ...params } as Record<string, unknown>
  delete output.systemAccountId
  return Object.keys(output).length ? output : undefined
}

export function boundedAuthorizationListParams<T extends object>(params?: T): Record<string, unknown> {
  return {
    ...(params as Record<string, unknown> | undefined),
    page: (params as { page?: unknown } | undefined)?.page ?? 1,
    pageSize: (params as { pageSize?: unknown } | undefined)?.pageSize ?? 500
  }
}

export function stripAdminOperationLogParams(params?: OperationLogListParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output = { ...params } as Record<string, unknown>
  delete output.actorSystemAccountId
  delete output.affectedSystemAccountId
  delete output.operationScopeSystemAccountId
  return Object.keys(output).length ? output : undefined
}

export function accountListParams(params?: AccountListParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.keyword) output.keyword = params.keyword
  if (params.providerCode && params.providerCode !== 'all') output.providerCode = params.providerCode
  if (params.groupId) output.groupId = params.groupId
  const tagIds = joinedListParam(params.tagIds)
  if (tagIds) output.tagIds = tagIds
  if (params.type && params.type !== 'all') output.type = params.type
  const status = joinedListParam(params.status)
  if (status) output.status = status
  if (params.schedulable && params.schedulable !== 'all') output.schedulable = params.schedulable
  if (params.sorts?.length) {
    output.sorts = params.sorts.map((sort) => `${sort.field}:${sort.order}`).join(',')
  }
  return output
}

export function accountOptionsParams(params?: AccountOptionParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.limit) output.limit = params.limit
  if (params.ids?.length) output.ids = params.ids.join(',')
  if (params.keyword) output.keyword = params.keyword
  if (params.providerCode && params.providerCode !== 'all') output.providerCode = params.providerCode
  if (params.groupId) output.groupId = params.groupId
  const tagIds = joinedListParam(params.tagIds)
  if (tagIds) output.tagIds = tagIds
  if (params.type && params.type !== 'all') output.type = params.type
  const status = joinedListParam(params.status)
  if (status) output.status = status
  if (params.schedulable && params.schedulable !== 'all') output.schedulable = params.schedulable
  return Object.keys(output).length ? output : undefined
}

function joinedListParam(value?: string | string[]): string | undefined {
  const values = Array.isArray(value) ? value : value ? [value] : []
  const normalizedValues = values
    .flatMap((item) => item.split(','))
    .map((item) => item.trim())
    .filter((item) => item && item !== 'all')
  return normalizedValues.length ? [...new Set(normalizedValues)].join(',') : undefined
}

export function groupListParams(params?: GroupListParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  return Object.keys(output).length ? output : undefined
}

export function groupOptionParams(params?: GroupOptionParams | Pick<GroupOptionParams, 'ids' | 'keyword' | 'providerCode' | 'limit' | 'manageableOnly' | 'preferDefault'>, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && 'systemAccountId' in params && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if ('ids' in params && params.ids?.length) output.ids = params.ids.join(',')
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.providerCode?.trim()) output.providerCode = params.providerCode.trim()
  if (params.limit) output.limit = params.limit
  if (typeof params.manageableOnly === 'boolean') output.manageableOnly = params.manageableOnly
  if (typeof params.preferDefault === 'boolean') output.preferDefault = params.preferDefault
  return Object.keys(output).length ? output : undefined
}

export function scopedListParams<T extends object>(params?: T, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output = { ...(params as Record<string, unknown>) }
  if (!includeSystemAccount) {
    delete output.systemAccountId
  }
  for (const [key, value] of Object.entries(output)) {
    if (value === undefined || value === null || value === '' || value === 'all') {
      delete output[key]
    }
  }
  return Object.keys(output).length ? output : undefined
}

export function teamListParams(params?: TeamListParams | Omit<TeamListParams, 'systemAccountId'>, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && 'systemAccountId' in params && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  return Object.keys(output).length ? output : undefined
}

export function authorizationPrincipalOptionsParams(params?: AuthorizationPrincipalOptionsParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.ids?.length) output.ids = params.ids.join(',')
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}

export function authorizationGranteeGroupOptionsParams(params: AuthorizationGranteeGroupOptionsParams): Record<string, unknown> {
  const output = authorizationPrincipalOptionsParams(params) ?? {}
  output.granteeSystemAccountId = params.granteeSystemAccountId
  if (params.providerCode?.trim()) output.providerCode = params.providerCode.trim()
  if (typeof params.preferDefault === 'boolean') output.preferDefault = params.preferDefault
  return output
}

export function systemAccountOptionsParams(params?: SystemAccountOptionsParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.ids?.length) output.ids = params.ids.join(',')
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}

export function systemAccountListParams(params?: SystemAccountListParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  return Object.keys(output).length ? output : undefined
}

export function authSessionListParams(params?: AuthSessionListParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  return Object.keys(output).length ? output : undefined
}

export function accountUsageStatsParams(params?: AccountUsageStatsParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.startDate) output.startDate = params.startDate
  if (params.endDate) output.endDate = params.endDate
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  if (params.schedulable && params.schedulable !== 'all') output.schedulable = params.schedulable
  return Object.keys(output).length ? output : undefined
}

export function aiPerformanceParams(params?: AiPerformanceParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.startDate) output.startDate = params.startDate
  if (params.endDate) output.endDate = params.endDate
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  return Object.keys(output).length ? output : undefined
}

export function aiPerformanceAccountOptionsParams(params?: AiPerformanceAccountOptionsParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}

export function modelCheckRunListParams(params?: ModelCheckRunListParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.systemAccountId?.trim()) output.systemAccountId = params.systemAccountId.trim()
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.targetType) output.targetType = params.targetType
  if (params.targetId?.trim()) output.targetId = params.targetId.trim()
  if (params.model) output.model = params.model
  if (params.level) output.level = params.level
  if (params.status) output.status = params.status
  if (params.startAt?.trim()) output.startAt = params.startAt.trim()
  if (params.endAt?.trim()) output.endAt = params.endAt.trim()
  return Object.keys(output).length ? output : undefined
}
