import type { AccountListSortField, AccountListSortParam } from '@/api/client'
import type { ResponsiveDataListSort, TableSortOrder } from '@/components/responsiveDataListSorting'
import type { AccountSummary } from '@/types/domain'

export type AccountTableSortOrderResolver = (field: AccountListSortField) => TableSortOrder

export function buildAccountTableColumns(isManagementView: boolean, sortOrder: AccountTableSortOrderResolver): Array<Record<string, unknown>> {
  const baseColumns: Array<Record<string, unknown>> = [
    sortableColumn({ title: '名称', dataIndex: 'name', key: 'name', width: 230 }, 'name', sortOrder),
    sortableColumn({ title: '账户类型', dataIndex: 'type', key: 'type', width: 120 }, 'type', sortOrder),
    sortableColumn({ title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 }, 'providerCode', sortOrder)
  ]
  if (isManagementView) {
    baseColumns.push(sortableColumn({ title: '系统账户', key: 'systemAccount', width: 180 }, 'systemAccount', sortOrder))
  }
  baseColumns.push(
    sortableColumn({ title: '并发数', key: 'concurrency', width: 100, align: 'center' }, 'concurrency', sortOrder),
    sortableColumn({ title: '状态', key: 'status', width: 190 }, 'status', sortOrder),
    sortableColumn({ title: '优先级', dataIndex: 'priority', key: 'priority', width: 90 }, 'priority', sortOrder),
    { title: '用量(日)', key: 'usage', width: 180 },
    { title: '代理', key: 'proxy', width: 180 },
    { title: '归属分组', key: 'group', width: 240, className: 'account-group-column' },
    sortableColumn({ title: '账户到期时间', key: 'accountExpiresAt', width: 180 }, 'accountExpiresAt', sortOrder),
    sortableColumn({ title: '最近使用时间', key: 'lastUsedAt', width: 180 }, 'lastUsedAt', sortOrder),
    sortableColumn({ title: '说明', dataIndex: 'notes', key: 'notes', width: 200 }, 'notes', sortOrder),
    { title: '操作', key: 'actions', width: 120, fixed: 'right' }
  )
  return baseColumns
}

export function accountColumnSortOrder(sorts: AccountListSortParam[], field: AccountListSortField): TableSortOrder {
  const sort = sorts.find((item) => item.field === field)
  if (!sort) return null
  return sort.order === 'asc' ? 'ascend' : 'descend'
}

export function normalizeAccountTableSorts(sorts: ResponsiveDataListSort[]): AccountListSortParam[] {
  const mappedSorts = sorts
    .map((sort) => {
      const field = accountSortFieldFromColumn(sort.columnKey)
      if (!field) return undefined
      return {
        field,
        order: sort.order === 'ascend' ? 'asc' : 'desc',
        priority: sort.priority
      }
    })
    .filter((sort): sort is AccountListSortParam & { priority: number } => Boolean(sort))
    .sort((left, right) => right.priority - left.priority)
  return mappedSorts.length
    ? mappedSorts.map(({ field, order }) => ({ field, order }))
    : [{ field: 'priority', order: 'asc' }]
}

function accountSortFieldFromColumn(columnKey: string): AccountListSortField | undefined {
  if (accountSortFields.includes(columnKey as AccountListSortField)) return columnKey as AccountListSortField
  return undefined
}

function sortableColumn(column: Record<string, unknown>, field: AccountListSortField, sortOrder: AccountTableSortOrderResolver): Record<string, unknown> {
  return {
    ...column,
    sorter: { multiple: accountSortMultiple(field) },
    sortOrder: sortOrder(field)
  }
}

function accountSortMultiple(field: AccountListSortField): number {
  const index = accountSortFields.indexOf(field)
  return index >= 0 ? accountSortFields.length - index : 1
}

const accountSortFields: AccountListSortField[] = [
  'priority',
  'superPriority',
  'qualityScore',
  'name',
  'type',
  'providerCode',
  'systemAccount',
  'concurrency',
  'status',
  'accountExpiresAt',
  'lastUsedAt',
  'notes'
]

export const accountSelectionColumnWidth = 32

export function tableColumnKey(column: { key?: unknown; dataIndex?: unknown }): string {
  return String(column.key ?? column.dataIndex ?? '')
}

export function accountTableScrollX(isManagementView: boolean): number {
  return (isManagementView ? 2340 : 2160) + accountSelectionColumnWidth
}

export function accountTableScrollY(): string {
  return 'calc(100dvh - 286px)'
}

export type AccountTableRecord = AccountSummary
