export function sqlPlaceholders(count: number): string {
  return Array.from({ length: Math.max(1, count) }, () => '?').join(',')
}

export function chunkValues<T>(values: T[], chunkSize: number): T[][] {
  const chunks: T[][] = []
  const size = Math.max(1, Math.trunc(chunkSize))
  for (let index = 0; index < values.length; index += size) {
    chunks.push(values.slice(index, index + size))
  }
  return chunks
}

export function takePageRows<T>(rows: T[], pageSize: number): { rows: T[]; hasMore: boolean } {
  const size = Math.max(0, Math.trunc(pageSize))
  const hasMore = rows.length > size
  return {
    rows: hasMore ? rows.slice(0, size) : rows,
    hasMore
  }
}

export function pagedTotalUpperBound(page: number, pageSize: number, itemCount: number, hasMore: boolean): number {
  const safePage = Math.max(1, Math.trunc(page))
  const safePageSize = Math.max(0, Math.trunc(pageSize))
  const safeItemCount = Math.max(0, Math.trunc(itemCount))
  return (safePage - 1) * safePageSize + safeItemCount + (hasMore ? 1 : 0)
}

export function textPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index]?.codePointAt(0)
    if (codePoint !== undefined && codePoint < 0x10ffff) {
      return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
    }
  }
  return `${value}\u{10ffff}`
}

export const defaultListWindowRows = 1001

export function pageUpperBoundForWindow(pageSize: number, windowRows = defaultListWindowRows): number {
  return Math.max(1, Math.floor((Math.max(1, Math.trunc(windowRows)) - 1) / Math.max(1, Math.trunc(pageSize))))
}

export function normalizeListPage(value: unknown, pageSize: number, windowRows = defaultListWindowRows): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(pageUpperBoundForWindow(pageSize, windowRows), Math.max(1, value))
    : 1
}

export function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}
