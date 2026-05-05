import type { AccountSummary } from '@/types/domain'
import {
  compareAccountConcurrency,
  compareAccountExpiresAt,
  compareAccountLastUsedAt
} from './accountFormatters'

export function buildAccountTableColumns(isAdmin: boolean): Array<Record<string, unknown>> {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', dataIndex: 'name', key: 'name', width: 230 },
    { title: '账户类型', dataIndex: 'type', key: 'type', width: 120 },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 }
  ]
  if (isAdmin) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '并发数', key: 'concurrency', width: 100, align: 'center', sorter: compareAccountConcurrency },
    { title: '状态', key: 'status', width: 190 },
    { title: '用量(日)', key: 'usage', width: 380 },
    { title: '归属分组', key: 'group', width: 240, className: 'account-group-column' },
    { title: '优先级', dataIndex: 'priority', key: 'priority', width: 90 },
    { title: '账户到期时间', key: 'accountExpiresAt', width: 180, sorter: compareAccountExpiresAt },
    { title: '最近使用时间', key: 'lastUsedAt', width: 180, sorter: compareAccountLastUsedAt },
    { title: '说明', dataIndex: 'notes', key: 'notes', width: 200 },
    { title: '操作', key: 'actions', width: 160, fixed: 'right' }
  )
  return baseColumns
}

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
