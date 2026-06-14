import { adjustTableScrollY, numberFromPagination } from './responsiveDataListVirtualization'

export type ResponsiveDataListTablePagination = false | Record<string, any>

export const defaultResponsiveDataListPageSize = 20
export const responsiveDataListTableHeaderHeight = 47
export const responsiveDataListTablePaginationHeight = 56
export const responsiveDataListMinTableBodyHeight = 160
export const responsiveDataListTablePlaceholderMinHeight = 220

export function buildResponsiveDataListTablePagination(
  pagination: ResponsiveDataListTablePagination | undefined,
  paginationSummary: boolean
): ResponsiveDataListTablePagination {
  if (pagination === false) return false

  const mergedPagination: Record<string, any> = {
    pageSize: defaultResponsiveDataListPageSize,
    hideOnSinglePage: true,
    showSizeChanger: false,
    showTotal: (total: number) => `共 ${total} 条`,
    ...(pagination ?? {})
  }

  if (!paginationSummary || mergedPagination.showTotal === false) {
    const paginationWithoutTotal = { ...mergedPagination }
    delete paginationWithoutTotal.showTotal
    return paginationWithoutTotal
  }

  return mergedPagination
}

export function hasResponsiveDataListTablePagination(
  pagination: ResponsiveDataListTablePagination,
  dataSourceLength: number
): boolean {
  if (pagination === false) return false

  const pageSize = numberFromPagination(pagination.pageSize) ?? defaultResponsiveDataListPageSize
  const total = numberFromPagination(pagination.total) ?? dataSourceLength
  return pagination.hideOnSinglePage === false || total > pageSize
}

export function buildResponsiveDataListTableScroll(input: {
  tableScrollEnabled: boolean
  scrollX?: number | string
  adaptiveColumnWidth: boolean
  tableScrollY: number | string
}): Record<string, number | string> | undefined {
  const scrollX = input.scrollX && input.adaptiveColumnWidth ? 'max-content' : input.scrollX
  if (!input.tableScrollEnabled) return scrollX ? { x: scrollX } : undefined

  return scrollX ? { x: scrollX, y: input.tableScrollY } : { y: input.tableScrollY }
}

export function resolveResponsiveDataListTablePlaceholderMinHeight(tableScrollY: number | string): number {
  return typeof tableScrollY === 'number'
    ? tableScrollY + responsiveDataListTableHeaderHeight
    : responsiveDataListTablePlaceholderMinHeight
}

export function resolveResponsiveDataListTableScrollY(input: {
  listHeight: number
  hasPagination: boolean
  tableScrollY: number | string
}): number | string {
  const paginationHeight = input.hasPagination ? responsiveDataListTablePaginationHeight : 0
  const verticalOffset = responsiveDataListTableHeaderHeight + paginationHeight
  if (input.listHeight > 0) {
    return Math.max(responsiveDataListMinTableBodyHeight, input.listHeight - verticalOffset)
  }
  return adjustTableScrollY(input.tableScrollY, verticalOffset)
}

export function resolveResponsiveDataListMobileFooterText(input: {
  loadingMore: boolean
  mobileHasMore: boolean
}): string {
  if (input.loadingMore) return '正在加载更多...'
  return input.mobileHasMore ? '上拉或点击加载更多' : '没有更多了'
}
