import { computed, ref, watch } from 'vue'
import {
  normalizeResponsiveDataListPullDistance,
  resolveResponsiveDataListPullRefreshText,
  shouldTriggerResponsiveDataListPullRefresh
} from './responsiveDataListPullRefresh'

export interface ResponsiveDataListPullRefreshOptions {
  isEnabled: () => boolean
  isRefreshing: () => boolean
  isLoadingMore: () => boolean
  getScrollTop: () => number
  onRefresh: () => void
}

export function useResponsiveDataListPullRefresh(options: ResponsiveDataListPullRefreshOptions) {
  const pullDistance = ref(0)
  const pullRefreshRequested = ref(false)
  const touchStartY = ref(0)
  const touchStartedAtTop = ref(false)
  const pullRefreshing = computed(() => options.isRefreshing() && pullRefreshRequested.value)
  const pullRefreshText = computed(() => resolveResponsiveDataListPullRefreshText(
    pullDistance.value,
    pullRefreshing.value
  ))

  function handleTouchStart(event: TouchEvent): void {
    if (!options.isEnabled() || options.isRefreshing() || options.isLoadingMore()) return
    touchStartY.value = event.touches[0]?.clientY ?? 0
    touchStartedAtTop.value = options.getScrollTop() <= 0
  }

  function handleTouchMove(event: TouchEvent): void {
    if (!options.isEnabled() || !touchStartedAtTop.value || options.isRefreshing() || options.isLoadingMore()) return
    const currentY = event.touches[0]?.clientY ?? 0
    const distance = currentY - touchStartY.value
    pullDistance.value = normalizeResponsiveDataListPullDistance(distance)
  }

  function handleTouchEnd(): void {
    if (!options.isEnabled()) return
    if (shouldTriggerResponsiveDataListPullRefresh(
      pullDistance.value,
      options.isRefreshing(),
      options.isLoadingMore()
    )) {
      pullRefreshRequested.value = true
      options.onRefresh()
    }
    pullDistance.value = 0
    touchStartedAtTop.value = false
  }

  watch(options.isRefreshing, (refreshing) => {
    if (!refreshing) {
      pullRefreshRequested.value = false
    }
  })

  return {
    pullDistance,
    pullRefreshing,
    pullRefreshText,
    handleTouchStart,
    handleTouchMove,
    handleTouchEnd
  }
}
