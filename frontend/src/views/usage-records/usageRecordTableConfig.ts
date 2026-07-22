import type { UsageRecordSortField, UsageRecordTableSortOrder } from './usageRecordPageState'

export function usageRecordTableColumns(input: {
  isManagementView: boolean
  columnSortOrder: (field: UsageRecordSortField) => UsageRecordTableSortOrder
}): Array<Record<string, unknown>> {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: 'AI账户名称', dataIndex: 'accountName', key: 'account', width: 170, fixed: 'left' }
  ]
  if (input.isManagementView) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '模型', dataIndex: 'model', key: 'model', width: 240 },
    { title: '类型', key: 'stream', width: 90 },
    { title: '状态', key: 'status', width: 150 },
    { title: 'Token 用量', key: 'tokens', width: 150 },
    { title: '成本', key: 'cost', width: 110 },
    { title: '延迟', key: 'latency', width: 150 },
    { title: '时间', dataIndex: 'createdAt', key: 'createdAt', width: 180, sorter: true, sortOrder: input.columnSortOrder('createdAt') },
    { title: '接口', dataIndex: 'endpoint', key: 'endpoint', width: 150 },
    { title: '请求来源', key: 'trafficSource', width: 110 },
    { title: 'API Key', dataIndex: 'apiKeyName', key: 'apiKey', width: 170 },
    { title: '分组', dataIndex: 'groupName', key: 'group', width: 150 },
    { title: 'IP', dataIndex: 'clientIp', key: 'clientIp', width: 130 },
    { title: 'traceId', dataIndex: 'traceId', key: 'traceId', width: 300 }
  )
  return baseColumns
}

export function usageRecordColumnStorageKey(isManagementView: boolean): string {
  return isManagementView ? 'usage-records:management' : 'usage-records:self'
}

export function normalizeUsageRecordTableSorter(sorter: unknown): { field: UsageRecordSortField; order: UsageRecordTableSortOrder } | undefined {
  const item = Array.isArray(sorter) ? sorter[0] : sorter
  if (!item || typeof item !== 'object') return undefined
  const record = item as Record<string, unknown>
  const field = usageRecordSortFieldFromColumn(record.columnKey ?? record.field)
  const order = record.order === 'ascend' || record.order === 'descend' ? record.order : null
  return field && order ? { field, order } : undefined
}

export function usageRecordSortFieldFromColumn(value: unknown): UsageRecordSortField | undefined {
  if (value === 'createdAt') return value
  return undefined
}

export function usageRecordPaginationFromTable(
  paginationInfo: unknown,
  fallbackPageSize: number
): { current: number; pageSize: number } | undefined {
  if (!paginationInfo || typeof paginationInfo !== 'object') return undefined
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  return {
    current: Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1,
    pageSize: Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : fallbackPageSize
  }
}
