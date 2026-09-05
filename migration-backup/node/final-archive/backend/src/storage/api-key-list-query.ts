import type { ApiKeyListOptions } from './api-key.repository.js'
import { normalizeListPage } from './query-utils.js'

type ApiKeyFilterValue = string | number

export type NormalizedApiKeyListOptions = Required<Pick<ApiKeyListOptions, 'page' | 'pageSize'>> & Pick<ApiKeyListOptions, 'keyword' | 'status' | 'routeStrategyId'>

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
    routeStrategyId: textFilter(options?.routeStrategyId)
  }
}

export function buildApiKeyFilters(scope: { clause: string; params: string[] }, options: NormalizedApiKeyListOptions): ApiKeyFilterResult {
  const clauses: string[] = []
  const params: ApiKeyFilterValue[] = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^\s*WHERE\s+/i, ''))
    params.push(...scope.params)
  }
  if (options.keyword) {
    clauses.push('(api_keys.name >= ? AND api_keys.name < ?)')
    params.push(options.keyword, textPrefixUpperBound(options.keyword))
  }
  if (options.status) {
    clauses.push('api_keys.status = ?')
    params.push(options.status)
  }
  if (options.routeStrategyId) {
    clauses.push('api_keys.route_strategy_id = ?')
    params.push(options.routeStrategyId)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function textFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function textPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index]?.codePointAt(0)
    if (codePoint !== undefined && codePoint < 0x10ffff) {
      return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
    }
  }
  return `${value}\uffff`
}
