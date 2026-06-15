import { ref, type ComputedRef } from 'vue'
import { defaultMobileItemHeight, normalizeMobileItemHeightKey } from './responsiveDataListVirtualization'

export interface ResponsiveDataListMobileItemMeasurementOptions<TRecord> {
  records: ComputedRef<TRecord[]>
  resolveRowKey: (record: TRecord, index: number) => string | number
  getPruneThreshold: () => number
  minEstimatedItemHeight?: number
  maxEstimatedItemHeight?: number
}

export function useResponsiveDataListMobileItemMeasurement<TRecord>(
  options: ResponsiveDataListMobileItemMeasurementOptions<TRecord>
) {
  let itemResizeObserver: ResizeObserver | undefined
  let measurementFrame = 0
  const estimatedItemHeight = ref(defaultMobileItemHeight)
  const itemHeightVersion = ref(0)
  const itemHeights = new Map<string, number>()
  const itemElements = new Map<string, HTMLElement>()
  const minEstimatedItemHeight = options.minEstimatedItemHeight ?? 96
  const maxEstimatedItemHeight = options.maxEstimatedItemHeight ?? 640

  function getItemHeight(record: TRecord, index: number): number {
    return itemHeights.get(itemHeightKey(record, index)) ?? estimatedItemHeight.value
  }

  function setItemRef(element: unknown, heightKey: string): void {
    const resolvedElement = resolveElementRef(element)
    const existingElement = itemElements.get(heightKey)
    if (existingElement && existingElement !== resolvedElement) {
      itemResizeObserver?.unobserve(existingElement)
      itemElements.delete(heightKey)
    }
    if (!resolvedElement) return
    itemElements.set(heightKey, resolvedElement)
    ensureItemResizeObserver()
    itemResizeObserver?.observe(resolvedElement)
    queueMeasurement()
  }

  function observeRenderedItems(): void {
    ensureItemResizeObserver()
    if (!itemResizeObserver) return
    itemElements.forEach((element) => itemResizeObserver?.observe(element))
  }

  function disconnectItemResizeObserver(): void {
    itemResizeObserver?.disconnect()
    itemResizeObserver = undefined
    cancelMeasurement()
  }

  function queueMeasurement(): void {
    if (typeof window === 'undefined' || measurementFrame) return
    measurementFrame = window.requestAnimationFrame(() => {
      measurementFrame = 0
      measureRenderedItems()
    })
  }

  function pruneItemHeights(): void {
    if (itemHeights.size <= options.records.value.length + options.getPruneThreshold()) return
    const currentKeys = new Set(options.records.value.map((record, index) => itemHeightKey(record, index)))
    itemHeights.forEach((_, key) => {
      if (!currentKeys.has(key)) itemHeights.delete(key)
    })
    updateEstimatedItemHeight()
    itemHeightVersion.value += 1
  }

  function clear(): void {
    itemElements.clear()
    itemHeights.clear()
  }

  function itemHeightKey(record: TRecord, index: number): string {
    return normalizeMobileItemHeightKey(options.resolveRowKey(record, index))
  }

  function ensureItemResizeObserver(): void {
    if (itemResizeObserver || typeof ResizeObserver === 'undefined') return
    itemResizeObserver = new ResizeObserver((entries) => {
      let updated = false
      entries.forEach((entry) => {
        if (typeof HTMLElement !== 'undefined' && entry.target instanceof HTMLElement) {
          updated = updateItemHeight(entry.target) || updated
        }
      })
      if (updated) updateEstimatedItemHeight()
    })
  }

  function resolveElementRef(element: unknown): HTMLElement | undefined {
    if (typeof HTMLElement === 'undefined') return undefined
    if (element instanceof HTMLElement) return element
    const possibleElement = element && typeof element === 'object' ? (element as { $el?: unknown }).$el : undefined
    return possibleElement instanceof HTMLElement ? possibleElement : undefined
  }

  function updateItemHeight(element: HTMLElement): boolean {
    const index = Number(element.dataset.mobileListIndex)
    const record = options.records.value[index]
    if (!record) return false
    const height = Math.ceil(element.getBoundingClientRect().height)
    if (!Number.isFinite(height) || height <= 0) return false
    const heightKey = itemHeightKey(record, index)
    const previousHeight = itemHeights.get(heightKey)
    if (previousHeight !== undefined && Math.abs(previousHeight - height) <= 1) return false
    itemHeights.set(heightKey, height)
    itemHeightVersion.value += 1
    return true
  }

  function updateEstimatedItemHeight(): void {
    if (itemHeights.size === 0) {
      estimatedItemHeight.value = defaultMobileItemHeight
      return
    }
    const heights = Array.from(itemHeights.values())
    const averageHeight = heights.reduce((total, height) => total + height, 0) / heights.length
    estimatedItemHeight.value = Math.max(
      minEstimatedItemHeight,
      Math.min(maxEstimatedItemHeight, Math.round(averageHeight))
    )
  }

  function measureRenderedItems(): void {
    let updated = false
    itemElements.forEach((element) => {
      updated = updateItemHeight(element) || updated
    })
    if (updated) updateEstimatedItemHeight()
  }

  function cancelMeasurement(): void {
    if (typeof window === 'undefined' || !measurementFrame) return
    window.cancelAnimationFrame(measurementFrame)
    measurementFrame = 0
  }

  return {
    estimatedItemHeight,
    itemHeightVersion,
    getItemHeight,
    setItemRef,
    observeRenderedItems,
    disconnectItemResizeObserver,
    queueMeasurement,
    pruneItemHeights,
    clear
  }
}
