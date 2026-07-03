export interface PagePaginationState {
  current: number
  pageSize: number
}

export function positiveIntegerOrFallback(value: unknown, fallback: number, max = Number.MAX_SAFE_INTEGER): number {
  const numeric = Number(value)
  return Number.isInteger(numeric) && numeric > 0 && numeric <= max ? numeric : fallback
}

export function sanitizePaginationState(value: unknown, fallback: PagePaginationState, maxPageSize = 200): PagePaginationState {
  const source = value && typeof value === 'object' ? value as Partial<PagePaginationState> : {}
  return {
    current: positiveIntegerOrFallback(source.current, fallback.current),
    pageSize: positiveIntegerOrFallback(source.pageSize, fallback.pageSize, maxPageSize)
  }
}

export function stringOrFallback(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback
}

export function stringUnionOrFallback<T extends string>(value: unknown, allowed: readonly T[], fallback: T): T {
  return typeof value === 'string' && (allowed as readonly string[]).includes(value) ? value as T : fallback
}
