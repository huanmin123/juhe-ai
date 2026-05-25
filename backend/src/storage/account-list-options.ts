export type AccountListSortField = 'priority' | 'superPriority' | 'fallback' | 'qualityScore' | 'name' | 'type' | 'providerCode' | 'systemAccount' | 'concurrency' | 'status' | 'accountExpiresAt' | 'lastUsedAt'
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
  limit?: number
  keyword?: string
  providerCode?: string
  groupId?: string
  type?: string
  status?: string
  schedulable?: AccountListSchedulableFilter
}

export interface NormalizedAccountListOptions {
  sorts: AccountListSort[]
  ids: string[]
  page: number
  pageSize: number
  keyword?: string
  providerCode?: string
  groupId?: string
  type?: string
  status?: string
  schedulable: AccountListSchedulableFilter
}

interface AccountListNormalizationOptions {
  maxPageSize?: number
}

const accountListSortColumns: Record<AccountListSortField, string> = {
  priority: "CASE WHEN account_rows.access_type = 'authorized' THEN 0 ELSE account_rows.priority END",
  superPriority: "CASE WHEN account_rows.access_type = 'authorized' THEN 0 ELSE account_rows.super_priority_enabled END",
  fallback: "CASE WHEN account_rows.access_type = 'authorized' THEN 0 ELSE account_rows.fallback_enabled END",
  qualityScore: 'quality_score',
  name: 'account_rows.name COLLATE NOCASE',
  type: 'account_rows.type COLLATE NOCASE',
  providerCode: 'account_rows.provider_code COLLATE NOCASE',
  systemAccount: 'system_account_sort_name COLLATE NOCASE',
  concurrency: 'account_rows.concurrency_limit',
  status: `CASE
    WHEN account_rows.access_type = 'authorized' THEN
      CASE
        WHEN account_rows.authorization_status <> 'active'
          OR (account_rows.authorization_expires_at IS NOT NULL AND account_rows.authorization_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
        THEN 'disabled'
        WHEN account_rows.account_expires_at IS NOT NULL AND account_rows.account_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now') THEN 'disabled'
        WHEN account_rows.status IN ('disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN account_rows.status
        WHEN account_rows.schedulable <> 1 THEN 'disabled'
        WHEN account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > strftime('%Y-%m-%dT%H:%M:%fZ', 'now') THEN 'temporary_unavailable'
        WHEN group_bindings.local_status = 'temporary_unavailable'
          AND group_bindings.local_cooldown_until IS NOT NULL
          AND group_bindings.local_cooldown_until <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
        THEN 'active'
        ELSE COALESCE(group_bindings.local_status, 'active')
      END
    ELSE account_rows.status
  END COLLATE NOCASE`,
  accountExpiresAt: 'COALESCE(account_rows.authorization_expires_at, account_rows.account_expires_at)',
  lastUsedAt: 'account_rows.last_used_at'
}

const defaultAccountListSorts: AccountListSort[] = [{ field: 'priority', order: 'asc' }]
const defaultAccountListPageSize = 50
const maxAccountListPageSize = 200
const maxAccountOptionPageSize = 50

export function normalizeAccountListOptions(options?: AccountListOptions, normalizationOptions: AccountListNormalizationOptions = {}): NormalizedAccountListOptions {
  const seenFields = new Set<AccountListSortField>()
  const sorts = (options?.sorts ?? [])
    .filter((sort): sort is AccountListSort => isAccountListSortField(sort.field) && isAccountListSortDirection(sort.order))
    .filter((sort) => {
      if (seenFields.has(sort.field)) return false
      seenFields.add(sort.field)
      return true
    })
  const rawPage = options?.page
  const rawPageSize = options?.pageSize ?? options?.limit
  const maxPageSize = normalizationOptions.maxPageSize ?? maxAccountListPageSize
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxPageSize, Math.max(1, rawPageSize))
    : defaultAccountListPageSize
  return {
    sorts: sorts.length ? sorts : defaultAccountListSorts,
    ids: normalizeTextList(options?.ids),
    page,
    pageSize,
    keyword: normalizeTextFilter(options?.keyword),
    providerCode: normalizeTextFilter(options?.providerCode),
    groupId: normalizeTextFilter(options?.groupId),
    type: normalizeTextFilter(options?.type),
    status: normalizeTextFilter(options?.status),
    schedulable: isSchedulableFilter(options?.schedulable) ? options.schedulable : 'all'
  }
}

export function normalizeAccountOptionListOptions(options?: AccountListOptions): NormalizedAccountListOptions {
  return normalizeAccountListOptions({ ...options, sorts: [] }, { maxPageSize: maxAccountOptionPageSize })
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
    if (sort.field === 'qualityScore') {
      return `CASE WHEN quality_score IS NULL THEN 1 ELSE 0 END ASC, quality_score ${direction}`
    }
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
