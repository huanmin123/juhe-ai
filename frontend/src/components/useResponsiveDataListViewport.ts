import { ref } from 'vue'
import { changeResponsiveDataListBodyScrollLock } from './responsiveDataListBodyScrollLock'

export interface ResponsiveDataListViewportOptions {
  getMobileBreakpoint: () => number
  shouldLockBodyScroll: () => boolean
  onListHeightUpdated?: () => void
  onViewportResize?: () => void
}

export function useResponsiveDataListViewport(options: ResponsiveDataListViewportOptions) {
  const listRootRef = ref<HTMLElement>()
  const mobileListRef = ref<HTMLElement>()
  const isMobile = ref(initialMobileState())
  const listHeight = ref(0)
  const mobileScrollTop = ref(0)
  const mobileContainerHeight = ref(0)
  let listResizeObserver: ResizeObserver | undefined
  let bodyScrollLocked = false
  let viewportListenersAttached = false

  function updateViewportState(): void {
    if (typeof window === 'undefined') return
    isMobile.value = window.innerWidth <= options.getMobileBreakpoint()
  }

  function updateListHeight(): void {
    listHeight.value = listRootRef.value?.clientHeight ?? 0
    updateMobileViewportMetrics()
    options.onListHeightUpdated?.()
  }

  function updateMobileViewportMetrics(): void {
    const list = mobileListRef.value
    if (!list) return
    mobileContainerHeight.value = list.clientHeight
    mobileScrollTop.value = list.scrollTop
  }

  function lockBodyScroll(): void {
    if (!options.shouldLockBodyScroll() || bodyScrollLocked) return
    bodyScrollLocked = true
    changeResponsiveDataListBodyScrollLock(1)
  }

  function unlockBodyScroll(): void {
    if (!bodyScrollLocked) return
    bodyScrollLocked = false
    changeResponsiveDataListBodyScrollLock(-1)
  }

  function observeListResize(): void {
    if (listResizeObserver || typeof ResizeObserver === 'undefined' || !listRootRef.value) return
    listResizeObserver = new ResizeObserver(() => {
      updateListHeight()
      options.onViewportResize?.()
    })
    listResizeObserver.observe(listRootRef.value)
  }

  function disconnectListResize(): void {
    listResizeObserver?.disconnect()
    listResizeObserver = undefined
  }

  function addViewportListeners(): void {
    if (viewportListenersAttached || typeof window === 'undefined') return
    viewportListenersAttached = true
    window.addEventListener('resize', updateViewportState, { passive: true })
    window.addEventListener('resize', updateListHeight, { passive: true })
    window.addEventListener('resize', handleViewportResizeSideEffects, { passive: true })
  }

  function removeViewportListeners(): void {
    if (!viewportListenersAttached || typeof window === 'undefined') return
    viewportListenersAttached = false
    window.removeEventListener('resize', updateViewportState)
    window.removeEventListener('resize', updateListHeight)
    window.removeEventListener('resize', handleViewportResizeSideEffects)
  }

  function updateMobileScrollMetrics(event: Event): HTMLElement {
    const target = event.currentTarget as HTMLElement
    mobileScrollTop.value = target.scrollTop
    mobileContainerHeight.value = target.clientHeight
    return target
  }

  function handleViewportResizeSideEffects(): void {
    options.onViewportResize?.()
  }

  function initialMobileState(): boolean {
    return typeof window !== 'undefined' && window.innerWidth <= options.getMobileBreakpoint()
  }

  return {
    listRootRef,
    mobileListRef,
    isMobile,
    listHeight,
    mobileScrollTop,
    mobileContainerHeight,
    updateViewportState,
    updateListHeight,
    updateMobileViewportMetrics,
    lockBodyScroll,
    unlockBodyScroll,
    observeListResize,
    disconnectListResize,
    addViewportListeners,
    removeViewportListeners,
    updateMobileScrollMetrics
  }
}
