export type AccountListSortField = 'priority' | 'name' | 'type' | 'providerCode' | 'systemAccount' | 'concurrency' | 'status' | 'accountExpiresAt' | 'lastUsedAt' | 'notes'
export type AccountListSortDirection = 'asc' | 'desc'

export interface AccountListSort {
  field: AccountListSortField
  order: AccountListSortDirection
}

export interface AccountListOptions {
  sorts?: AccountListSort[]
}

const accountListSortColumns: Record<AccountListSortField, string> = {
  priority: 'account_rows.priority',
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

export function normalizeAccountListOptions(options?: AccountListOptions): Required<AccountListOptions> {
  const seenFields = new Set<AccountListSortField>()
  const sorts = (options?.sorts ?? [])
    .filter((sort): sort is AccountListSort => isAccountListSortField(sort.field) && isAccountListSortDirection(sort.order))
    .filter((sort) => {
      if (seenFields.has(sort.field)) return false
      seenFields.add(sort.field)
      return true
    })
  return {
    sorts: sorts.length ? sorts : defaultAccountListSorts
  }
}

export function buildAccountListOrderClause(options: Required<AccountListOptions>): string {
  const orderParts = options.sorts.map((sort) => {
    const direction = sort.order === 'desc' ? 'DESC' : 'ASC'
    return `${accountListSortColumns[sort.field]} ${direction}`
  })
  return `ORDER BY ${orderParts.join(', ')}`
}

function isAccountListSortField(value: unknown): value is AccountListSortField {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(accountListSortColumns, value)
}

function isAccountListSortDirection(value: unknown): value is AccountListSortDirection {
  return value === 'asc' || value === 'desc'
}
