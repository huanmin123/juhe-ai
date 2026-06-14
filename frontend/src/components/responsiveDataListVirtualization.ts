export interface MobileVirtualWindow {
  start: number
  end: number
  topPadding: number
  bottomPadding: number
}

export interface MobileVirtualItem<TRecord> {
  record: TRecord
  index: number
  key: string | number
  heightKey: string
}

export const defaultMobileItemHeight = 148
export const mobileVirtualOverscanItems = 10

export function normalizeMobileItemHeightKey(key: string | number): string {
  return `${typeof key}:${String(key)}`
}

export function buildMobileVirtualWindow<TRecord>(input: {
  records: TRecord[]
  shouldVirtualize: boolean
  scrollTop: number
  containerHeight: number
  listHeight: number
  estimatedItemHeight: number
  getItemHeight: (record: TRecord, index: number) => number
}): MobileVirtualWindow {
  const total = input.records.length
  if (!input.shouldVirtualize || total === 0) {
    return { start: 0, end: total, topPadding: 0, bottomPadding: 0 }
  }

  const viewportHeight = Math.max(input.containerHeight || input.listHeight || 600, 240)
  const estimatedItemHeight = Math.max(1, input.estimatedItemHeight)
  const overscanHeight = estimatedItemHeight * mobileVirtualOverscanItems
  const visibleStart = Math.max(0, input.scrollTop - overscanHeight)
  const visibleEnd = input.scrollTop + viewportHeight + overscanHeight

  let offset = 0
  let start = 0
  for (; start < total; start += 1) {
    const itemHeight = input.getItemHeight(input.records[start], start)
    if (offset + itemHeight >= visibleStart) break
    offset += itemHeight
  }

  const topPadding = offset
  let end = start
  for (; end < total; end += 1) {
    offset += input.getItemHeight(input.records[end], end)
    if (offset >= visibleEnd) {
      end += 1
      break
    }
  }
  end = Math.min(total, Math.max(start + 1, end))

  let totalHeight = offset
  for (let index = end; index < total; index += 1) {
    totalHeight += input.getItemHeight(input.records[index], index)
  }

  return {
    start,
    end,
    topPadding,
    bottomPadding: Math.max(0, totalHeight - offset)
  }
}

export function buildMobileVirtualItems<TRecord>(
  records: TRecord[],
  start: number,
  end: number,
  resolveKey: (record: TRecord, index: number) => string | number
): MobileVirtualItem<TRecord>[] {
  return records.slice(start, end).map((record, offset) => {
    const index = start + offset
    const key = resolveKey(record, index)
    return {
      record,
      index,
      key,
      heightKey: normalizeMobileItemHeightKey(key)
    }
  })
}

export function numberFromPagination(value: unknown): number | undefined {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue > 0 ? numberValue : undefined
}

export function adjustTableScrollY(value: number | string, offset: number): number | string {
  if (typeof value === 'number') return Math.max(0, value - offset)
  return `calc(${value} - ${offset}px)`
}
