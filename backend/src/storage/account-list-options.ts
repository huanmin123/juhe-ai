export type AccountListSortField = 'priority' | 'superPriority' | 'fallback' | 'qualityScore' | 'name' | 'type' | 'providerCode' | 'systemAccount' | 'concurrency' | 'status' | 'accountExpiresAt' | 'lastUsedAt' | 'notes'
export type AccountListSortDirection = 'asc' | 'desc'

export interface AccountListSort {
  field: AccountListSortField
  order: AccountListSortDirection
}

export type AccountListSchedulableFilter = 'all' | 'enabled' | 'disabled' | 'cooling'

export interface AccountListOptions {
  sorts?: AccountListSort[]
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
  type?: string
  status?: string
  schedulable?: AccountListSchedulableFilter
}

export interface NormalizedAccountListOptions {
  sorts: AccountListSort[]
  page: number
  pageSize: number
  keyword?: string
  type?: string
  status?: string
  schedulable: AccountListSchedulableFilter
}

const accountListSortColumns: Record<AccountListSortField, string> = {
  priority: 'account_rows.priority',
  superPriority: 'account_rows.super_priority_enabled',
  fallback: 'account_rows.fallback_enabled',
  qualityScore: 'account_quality.quality_score',
  name: 'account_rows.name COLLATE NOCASE',
  type: 'account_rows.type COLLATE NOCASE',
  providerCode: 'account_rows.provider_code COLLATE NOCASE',
  systemAccount: "COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) COLLATE NOCASE",
  concurrency: 'account_rows.concurrency_limit',
  status: 'account_rows.status COLLATE NOCASE',
  accountExpiresAt: 'account_rows.account_expires_at',
  lastUsedAt: "COALESCE(CASE WHEN account_rows.access_type = 'authorized' AND account_rows.authorization_id IS NOT NULL THEN authorization_usage.last_used_at ELSE account_rows.last_used_at END, account_usage.last_used_at)",
  notes: 'account_rows.notes COLLATE NOCASE'
}

const defaultAccountListSorts: AccountListSort[] = [{ field: 'priority', order: 'asc' }]
const defaultAccountListPageSize = 50
const maxAccountListPageSize = 200

export function normalizeAccountListOptions(options?: AccountListOptions): NormalizedAccountListOptions {
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
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxAccountListPageSize, Math.max(1, rawPageSize))
    : defaultAccountListPageSize
  return {
    sorts: sorts.length ? sorts : defaultAccountListSorts,
    page,
    pageSize,
    keyword: normalizeTextFilter(options?.keyword),
    type: normalizeTextFilter(options?.type),
    status: normalizeTextFilter(options?.status),
    schedulable: isSchedulableFilter(options?.schedulable) ? options.schedulable : 'all'
  }
}

export function buildAccountListOrderClause(options: Pick<NormalizedAccountListOptions, 'sorts'>): string {
  const orderParts = options.sorts.map((sort) => {
    const direction = sort.order === 'desc' ? 'DESC' : 'ASC'
    if (sort.field === 'qualityScore') {
      return `CASE WHEN account_quality.quality_score IS NULL THEN 1 ELSE 0 END ASC, account_quality.quality_score ${direction}`
    }
    return `${accountListSortColumns[sort.field]} ${direction}`
  })
  return `ORDER BY ${[...orderParts, 'account_rows.created_at ASC', 'account_rows.id ASC'].join(', ')}`
}

function normalizeTextFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
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
