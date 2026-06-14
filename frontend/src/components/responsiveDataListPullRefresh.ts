export const responsiveDataListPullRefreshThreshold = 64
export const responsiveDataListPullRefreshMaxDistance = 96

export function normalizeResponsiveDataListPullDistance(distance: number): number {
  if (!Number.isFinite(distance) || distance <= 0) return 0
  return Math.min(distance, responsiveDataListPullRefreshMaxDistance)
}

export function shouldTriggerResponsiveDataListPullRefresh(
  pullDistance: number,
  refreshing: boolean,
  loadingMore: boolean
): boolean {
  return pullDistance >= responsiveDataListPullRefreshThreshold && !refreshing && !loadingMore
}

export function resolveResponsiveDataListPullRefreshText(
  pullDistance: number,
  refreshing: boolean
): string {
  if (refreshing) return '正在刷新...'
  if (pullDistance >= responsiveDataListPullRefreshThreshold) return '松开刷新'
  return '下拉刷新'
}
