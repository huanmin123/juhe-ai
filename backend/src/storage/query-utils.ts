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
