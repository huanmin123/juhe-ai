import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import type {
  AccountListOptions,
  AccountListSchedulableFilter,
  AccountListSortDirection,
  AccountListSortField,
  AccountOptionListOptions
} from '../../storage/repositories.js'

export const accountListSortFieldValues = [
  'priority',
  'superPriority',
  'fallback',
  'qualityScore',
  'name',
  'type',
  'providerCode',
  'systemAccount',
  'concurrency',
  'status',
  'accountExpiresAt',
  'lastUsedAt'
] as const

const accountListSortFields = new Set<AccountListSortField>(accountListSortFieldValues)

export function parseAccountOptionsQuery(query: Record<string, unknown>): AccountOptionListOptions {
  return {
    ids: queryTextList(query.ids, 50),
    page: integerQueryValue(query.page),
    limit: optionLimitValue(integerQueryValue(query.limit)),
    keyword: optionalQueryText(query.keyword),
    providerCode: optionalQueryText(query.providerCode),
    groupId: optionalQueryText(query.groupId),
    tagIds: queryTextList(query.tagIds, 100),
    type: optionalQueryText(query.type),
    status: statusQueryValue(query.status),
    schedulable: schedulableQueryValue(query.schedulable)
  }
}

export function parseAccountListOptions(query: Record<string, unknown>): AccountListOptions {
  const sorts = stringValues(query.sorts)
    .flatMap((value) => value.split(','))
    .map((value) => value.trim())
    .filter(Boolean)
    .map(parseAccountListSort)
    .filter((sort): sort is NonNullable<ReturnType<typeof parseAccountListSort>> => Boolean(sort))
  return {
    sorts,
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword),
    providerCode: optionalQueryText(query.providerCode),
    groupId: optionalQueryText(query.groupId),
    tagIds: queryTextList(query.tagIds, 100),
    type: optionalQueryText(query.type),
    status: statusQueryValue(query.status),
    schedulable: schedulableQueryValue(query.schedulable)
  }
}

export function statusQueryValue(value: unknown): string | undefined {
  const statuses = stringValues(value)
    .flatMap((item) => item.split(','))
    .map((item) => item.trim())
    .filter((item) => item && item !== 'all')
  return statuses.length ? [...new Set(statuses)].join(',') : undefined
}

export function schedulableQueryValue(value: unknown): AccountListSchedulableFilter | undefined {
  const text = optionalQueryText(value)
  return text === 'all' || text === 'enabled' || text === 'disabled' || text === 'cooling' ? text : undefined
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(50, Math.max(1, value)) : 50
}

function parseAccountListSort(value: string): { field: AccountListSortField; order: AccountListSortDirection } | undefined {
  const [field, order] = value.split(':').map((item) => item.trim())
  if (!accountListSortFields.has(field as AccountListSortField)) return undefined
  if (order !== 'asc' && order !== 'desc') return undefined
  return { field: field as AccountListSortField, order }
}

function stringValues(value: unknown): string[] {
  if (typeof value === 'string') return [value]
  if (Array.isArray(value)) return value.filter((item): item is string => typeof item === 'string')
  return []
}
