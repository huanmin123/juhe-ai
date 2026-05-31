import type { ApiKeyListOptions } from './api-key.repository.js'

type ApiKeyFilterValue = string | number

export type NormalizedApiKeyListOptions = Required<Pick<ApiKeyListOptions, 'page' | 'pageSize'>> & Pick<ApiKeyListOptions, 'keyword' | 'status' | 'groupId'>

export interface ApiKeyFilterResult {
  clause: string
  params: ApiKeyFilterValue[]
}

const defaultApiKeyListPageSize = 50
const maxApiKeyListPageSize = 200

export function normalizeApiKeyListOptions(options?: ApiKeyListOptions): NormalizedApiKeyListOptions {
  const rawPage = options?.page
  const rawPageSize = options?.pageSize ?? options?.limit
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxApiKeyListPageSize, Math.max(1, rawPageSize))
    : defaultApiKeyListPageSize
  return {
    page,
    pageSize,
    keyword: textFilter(options?.keyword),
    status: options?.status === 'active' || options?.status === 'disabled' ? options.status : undefined,
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
    clauses.push('api_keys.status = ?')
    params.push(options.status)
  }
  if (options.groupId) {
    clauses.push(`EXISTS (
        SELECT 1
        FROM api_key_group_bindings
        WHERE api_key_group_bindings.api_key_id = api_keys.id
          AND api_key_group_bindings.group_id = ?
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
