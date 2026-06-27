import type { ApiKeyListOptions } from './api-key.repository.js'
import { normalizeListPage } from './query-utils.js'

type ApiKeyFilterValue = string | number

export type NormalizedApiKeyListOptions = Required<Pick<ApiKeyListOptions, 'page' | 'pageSize'>> & Pick<ApiKeyListOptions, 'keyword' | 'status' | 'routeStrategyId' | 'groupId'>

export interface ApiKeyFilterResult {
  clause: string
  params: ApiKeyFilterValue[]
}

const defaultApiKeyListPageSize = 50
const maxApiKeyListPageSize = 200

export function normalizeApiKeyListOptions(options?: ApiKeyListOptions): NormalizedApiKeyListOptions {
  const rawPage = options?.page
  const rawPageSize = options?.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxApiKeyListPageSize, Math.max(1, rawPageSize))
    : defaultApiKeyListPageSize
  const page = normalizeListPage(rawPage, pageSize)
  return {
    page,
    pageSize,
    keyword: textFilter(options?.keyword),
    status: options?.status === 'active' || options?.status === 'disabled' ? options.status : undefined,
    routeStrategyId: textFilter(options?.routeStrategyId),
    groupId: textFilter(options?.groupId)
  }
}

export function buildApiKeyFilters(scope: { clause: string; params: string[] }, options: NormalizedApiKeyListOptions): ApiKeyFilterResult {
  const clauses: string[] = []
  const params: ApiKeyFilterValue[] = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ WHERE /, ''))
    params.push(...scope.params)
  }
  if (options.keyword) {
    const keywordPrefix = `${escapeLikePrefix(options.keyword)}%`
    clauses.push("(api_keys.name COLLATE NOCASE = ? OR api_keys.name LIKE ? ESCAPE '\\')")
    params.push(options.keyword, keywordPrefix)
  }
  if (options.status) {
    if (options.status === 'active') {
      clauses.push("api_keys.status = 'active' AND api_keys.availability_schedule_active = 1")
    } else {
      clauses.push("(api_keys.status = 'disabled' OR api_keys.availability_schedule_active <> 1)")
    }
  }
  if (options.routeStrategyId) {
    clauses.push('api_keys.route_strategy_id = ?')
    params.push(options.routeStrategyId)
  }
  if (options.groupId) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM route_strategy_groups
      WHERE route_strategy_groups.route_strategy_id = api_keys.route_strategy_id
        AND route_strategy_groups.system_account_id = api_keys.system_account_id
        AND route_strategy_groups.group_id = ?
    )`)
    params.push(options.groupId)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function textFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}
