import type { AccountListSortField } from '@/api/client'
import type { TableSortOrder } from '@/components/responsiveDataListSorting'
import type { AccountSummary } from '@/types/domain'

export type AccountTableSortOrderResolver = (field: AccountListSortField) => TableSortOrder

export function buildAccountTableColumns(isAdmin: boolean, sortOrder: AccountTableSortOrderResolver): Array<Record<string, unknown>> {
  const baseColumns: Array<Record<string, unknown>> = [
    sortableColumn({ title: '名称', dataIndex: 'name', key: 'name', width: 230 }, 'name', sortOrder),
    sortableColumn({ title: '账户类型', dataIndex: 'type', key: 'type', width: 120 }, 'type', sortOrder),
    sortableColumn({ title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 }, 'providerCode', sortOrder)
  ]
  if (isAdmin) {
    baseColumns.push(sortableColumn({ title: '系统账户', key: 'systemAccount', width: 180 }, 'systemAccount', sortOrder))
  }
  baseColumns.push(
    sortableColumn({ title: '并发数', key: 'concurrency', width: 100, align: 'center' }, 'concurrency', sortOrder),
    sortableColumn({ title: '状态', key: 'status', width: 190 }, 'status', sortOrder),
    { title: '用量(日)', key: 'usage', width: 380 },
    { title: '归属分组', key: 'group', width: 240, className: 'account-group-column' },
    sortableColumn({ title: '优先级', dataIndex: 'priority', key: 'priority', width: 90 }, 'priority', sortOrder),
    sortableColumn({ title: '账户到期时间', key: 'accountExpiresAt', width: 180 }, 'accountExpiresAt', sortOrder),
    sortableColumn({ title: '最近使用时间', key: 'lastUsedAt', width: 180 }, 'lastUsedAt', sortOrder),
    sortableColumn({ title: '说明', dataIndex: 'notes', key: 'notes', width: 200 }, 'notes', sortOrder),
    { title: '操作', key: 'actions', width: 160, fixed: 'right' }
  )
  return baseColumns
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

export function tableColumnKey(column: { key?: unknown; dataIndex?: unknown }): string {
  return String(column.key ?? column.dataIndex ?? '')
}

export function accountTableScrollX(isAdmin: boolean): number {
  return isAdmin ? 2320 : 2140
}

export function accountTableScrollY(): string {
  return 'calc(100dvh - 286px)'
}

export type AccountTableRecord = AccountSummary
