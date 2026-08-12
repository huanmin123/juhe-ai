import { normalizeListPage } from './query-utils.js'

export type AccountListSortField = 'priority' | 'superPriority' | 'fallback' | 'name' | 'type' | 'providerCode' | 'systemAccount' | 'concurrency' | 'status' | 'accountExpiresAt' | 'lastUsedAt'
export type AccountListSortDirection = 'asc' | 'desc'

export interface AccountListSort {
  field: AccountListSortField
  order: AccountListSortDirection
}

export type AccountListSchedulableFilter = 'all' | 'enabled' | 'disabled' | 'cooling'

export interface AccountListOptions {
  sorts?: AccountListSort[]
  ids?: string[]
  page?: number
  pageSize?: number
  keyword?: string
  providerCode?: string
  providerProtocolProfileId?: string
  groupId?: string
  tagIds?: string[]
  type?: string
  status?: string
  schedulable?: AccountListSchedulableFilter
}

export interface AccountOptionListOptions extends Omit<AccountListOptions, 'pageSize'> {
  limit?: number
}

export interface NormalizedAccountListOptions {
  sorts: AccountListSort[]
  ids: string[]
  page: number
  pageSize: number
  keyword?: string
  providerCode?: string
  providerProtocolProfileId?: string
  groupId?: string
  tagIds: string[]
  type?: string
  status?: string
  schedulable: AccountListSchedulableFilter
}

interface AccountListNormalizationOptions {
  maxPageSize?: number
}

const accountListSortColumns: Record<AccountListSortField, string> = {
  priority: "CASE WHEN account_rows.access_type = 'authorized' THEN COALESCE(group_bindings.local_priority, account_rows.priority) ELSE account_rows.priority END",
  superPriority: "CASE WHEN account_rows.access_type = 'authorized' THEN COALESCE(group_bindings.local_super_priority_enabled, account_rows.super_priority_enabled) ELSE account_rows.super_priority_enabled END",
  fallback: "CASE WHEN account_rows.access_type = 'authorized' THEN COALESCE(group_bindings.local_fallback_enabled, account_rows.fallback_enabled) ELSE account_rows.fallback_enabled END",
  name: 'account_rows.name',
  type: 'account_rows.type',
  providerCode: 'account_rows.provider_code',
  systemAccount: 'system_account_sort_name',
  concurrency: 'account_rows.concurrency_limit',
  status: `CASE
    WHEN account_rows.access_type = 'authorized' THEN
      CASE
        WHEN group_bindings.group_id IS NULL
          OR group_bindings.account_authorization_id IS NULL
          OR group_bindings.account_authorization_id <> account_rows.authorization_id
        THEN 'disabled'
        WHEN account_rows.authorization_status IS NULL
          OR account_rows.authorization_status <> 'active'
          OR (account_rows.authorization_expires_at IS NOT NULL AND account_rows.authorization_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
        THEN 'disabled'
        WHEN account_rows.source_status IS NULL THEN 'disabled'
        WHEN account_rows.source_last_error_code = 'account_expired'
          OR (account_rows.source_account_expires_at IS NOT NULL AND account_rows.source_account_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
        THEN 'disabled'
        WHEN account_rows.source_status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN account_rows.source_status
        WHEN account_rows.source_schedulable <> 1 THEN 'disabled'
        WHEN account_rows.source_cooldown_until IS NOT NULL AND account_rows.source_cooldown_until > strftime('%Y-%m-%dT%H:%M:%fZ', 'now') THEN 'temporary_unavailable'
        WHEN account_rows.account_expires_at IS NOT NULL AND account_rows.account_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now') THEN 'disabled'
        WHEN account_rows.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN account_rows.status
        WHEN account_rows.schedulable <> 1 THEN 'disabled'
        WHEN account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > strftime('%Y-%m-%dT%H:%M:%fZ', 'now') THEN 'temporary_unavailable'
        ELSE account_rows.status
      END
    ELSE account_rows.status
  END`,
  accountExpiresAt: 'COALESCE(account_rows.authorization_expires_at, account_rows.account_expires_at)',
  lastUsedAt: 'account_rows.last_used_at'
}

const defaultAccountListSorts: AccountListSort[] = [{ field: 'priority', order: 'asc' }]
const defaultAccountListPageSize = 50
const maxAccountListPageSize = 200
const maxAccountOptionPageSize = 50

export function normalizeAccountListOptions(options?: AccountListOptions, normalizationOptions: AccountListNormalizationOptions = {}): NormalizedAccountListOptions {
  const seenFields = new Set<AccountListSortField>()
  const inputSorts = (options?.sorts ?? [])
    .filter((sort): sort is AccountListSort => isAccountListSortField(sort.field) && isAccountListSortDirection(sort.order))
    .filter((sort) => {
      if (seenFields.has(sort.field)) return false
      seenFields.add(sort.field)
      return true
    })
  const prioritySort = inputSorts.find((sort) => sort.field === 'priority') ?? defaultAccountListSorts[0]!
  const statusSort = inputSorts.find((sort) => sort.field === 'status')
  const sorts = [
    prioritySort,
    ...(statusSort ? [statusSort] : []),
    ...inputSorts.filter((sort) => sort.field !== 'priority' && sort.field !== 'status')
  ]
  const rawPage = options?.page
  const rawPageSize = options?.pageSize
  const maxPageSize = normalizationOptions.maxPageSize ?? maxAccountListPageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxPageSize, Math.max(1, rawPageSize))
    : defaultAccountListPageSize
  const page = normalizeListPage(rawPage, pageSize)
  return {
    sorts,
    ids: normalizeTextList(options?.ids),
    page,
    pageSize,
    keyword: normalizeTextFilter(options?.keyword),
    providerCode: normalizeTextFilter(options?.providerCode),
    providerProtocolProfileId: normalizeTextFilter(options?.providerProtocolProfileId),
    groupId: normalizeTextFilter(options?.groupId),
    tagIds: normalizeTextList(options?.tagIds),
    type: normalizeTextFilter(options?.type),
    status: normalizeTextFilter(options?.status),
    schedulable: isSchedulableFilter(options?.schedulable) ? options.schedulable : 'all'
  }
}

export function normalizeAccountOptionListOptions(options?: AccountOptionListOptions): NormalizedAccountListOptions {
  return normalizeAccountListOptions({ ...options, pageSize: options?.limit, sorts: [] }, { maxPageSize: maxAccountOptionPageSize })
}

export function accountStatusFilterValues(status?: string): string[] {
  if (!status) return []
  const values = status
    .split(',')
    .map((item) => normalizeTextFilter(item))
    .filter((item): item is string => Boolean(item) && item !== 'all')
  return [...new Set(values)]
}

export function buildAccountListOrderClause(options: Pick<NormalizedAccountListOptions, 'sorts'>): string {
  const orderParts = options.sorts.map((sort) => {
    const direction = sort.order === 'desc' ? 'DESC' : 'ASC'
    return `${accountListSortColumns[sort.field]} ${direction}`
  })
  return `ORDER BY ${[...orderParts, 'account_rows.created_at ASC', 'account_rows.id ASC'].join(', ')}`
}

function normalizeTextFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizeTextList(values?: string[]): string[] {
  if (!values?.length) return []
  return [...new Set(values.map((value) => normalizeTextFilter(value)).filter((value): value is string => Boolean(value)))]
    .sort()
    .slice(0, 500)
}

function isSchedulableFilter(value: unknown): value is AccountListSchedulableFilter {
  return value === 'all' || value === 'enabled' || value === 'disabled' || value === 'cooling'
}

function isAccountListSortField(value: unknown): value is AccountListSortField {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(accountListSortColumns, value)
}

function isAccountListSortDirection(value: unknown): value is AccountListSortDirection {
  return value === 'asc' || value === 'desc'
}
