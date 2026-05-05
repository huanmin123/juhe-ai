export type TableSortOrder = 'ascend' | 'descend' | null

export interface ResponsiveDataListSort {
  columnKey: string
  order: Exclude<TableSortOrder, null>
  priority: number
}

interface RawSorterRecord {
  columnKey?: unknown
  field?: unknown
  order?: unknown
  column?: unknown
}

export function normalizeResponsiveTableSorter(sorter: unknown): ResponsiveDataListSort[] {
  const items = Array.isArray(sorter) ? sorter : sorter ? [sorter] : []
  return items
    .map(sorterRecord)
    .map((record, index) => normalizeSorterRecord(record, index))
    .filter((item): item is ResponsiveDataListSort => Boolean(item))
}

function sorterRecord(value: unknown): RawSorterRecord {
  return value && typeof value === 'object' ? value as RawSorterRecord : {}
}

function normalizeSorterRecord(record: RawSorterRecord, index: number): ResponsiveDataListSort | undefined {
  if (record.order !== 'ascend' && record.order !== 'descend') return undefined
  const columnKey = stringValue(record.columnKey ?? record.field)
  if (!columnKey) return undefined
  return {
    columnKey,
    order: record.order,
    priority: sorterPriority(record.column, index)
  }
}

function sorterPriority(column: unknown, index: number): number {
  if (column && typeof column === 'object') {
    const sorter = (column as { sorter?: unknown }).sorter
    if (sorter && typeof sorter === 'object') {
      const multiple = (sorter as { multiple?: unknown }).multiple
      if (typeof multiple === 'number' && Number.isFinite(multiple)) return multiple
    }
  }
  return Number.MAX_SAFE_INTEGER - index
}

function stringValue(value: unknown): string | undefined {
  if (typeof value !== 'string' && typeof value !== 'number') return undefined
  const text = String(value).trim()
  return text || undefined
}
