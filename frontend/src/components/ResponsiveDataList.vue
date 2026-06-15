<template>
  <div ref="listRootRef" class="responsive-data-list">
    <DeferredRender
      v-if="!isMobile"
      :active="pageActive"
      :deferred="deferTableMount"
      :delay-frames="tableMountDelayFrames"
      :min-height="tablePlaceholderMinHeight"
      reset-on-deactivate
    >
      <a-table
        :class="tableClassNames"
        :style="tableStyleVars"
        :size="size"
        :columns="tableColumns"
        :data-source="dataSource"
        :row-key="rowKey"
        :loading="loading"
        :pagination="tablePagination"
        :scroll="tableScroll"
        :table-layout="tableLayout"
        :row-selection="rowSelection"
        :expandable="expandable"
        :custom-row="tableCustomRow"
        @change="handleTableChange"
      >
        <template #emptyText>
          <slot name="emptyText">
            <a-empty class="page-empty-card" description="暂无数据" />
          </slot>
        </template>
        <template #bodyCell="slotProps">
          <slot name="bodyCell" v-bind="slotProps" />
        </template>
      </a-table>
      <template #placeholder>
        <div class="responsive-data-list-table-placeholder">
          <a-spin v-if="loading" size="small" />
        </div>
      </template>
    </DeferredRender>

    <div
      v-else
      ref="mobileListRef"
      :class="['responsive-data-list-cards', cardClass, { 'responsive-data-list-cards-virtualized': shouldVirtualizeMobileCards }]"
      @scroll="handleMobileScroll"
      @touchstart.passive="handleTouchStart"
      @touchmove.passive="handleTouchMove"
      @touchend="handleTouchEnd"
    >
      <div v-if="pullRefreshEnabled" class="responsive-data-list-pull" :class="{ active: pullDistance > 0 || pullRefreshing }">
        <a-spin v-if="pullRefreshing" size="small" />
        <span>{{ pullRefreshText }}</span>
      </div>
      <a-spin v-if="loading" class="responsive-data-list-loading" />
      <template v-else-if="mobileDataSource.length">
        <template v-if="shouldVirtualizeMobileCards">
          <div
            v-if="mobileVirtualWindow.topPadding > 0"
            class="responsive-data-list-virtual-spacer"
            :style="{ height: `${mobileVirtualWindow.topPadding}px` }"
          />
          <div
            v-for="virtualItem in mobileVirtualItems"
            :key="virtualItem.key"
            :ref="(element) => setMobileItemRef(element, virtualItem.heightKey)"
            class="responsive-data-list-item"
            :class="{ 'responsive-data-list-item-last': virtualItem.index === mobileDataSource.length - 1 }"
            :data-mobile-list-index="virtualItem.index"
          >
            <slot name="card" :record="virtualItem.record" :index="virtualItem.index" />
          </div>
          <div
            v-if="mobileVirtualWindow.bottomPadding > 0"
            class="responsive-data-list-virtual-spacer"
            :style="{ height: `${mobileVirtualWindow.bottomPadding}px` }"
          />
        </template>
        <template v-else>
          <template v-for="(record, index) in mobileDataSource" :key="resolveRowKey(record, index)">
            <slot name="card" :record="record" :index="index" />
          </template>
        </template>
        <div
          v-if="mobilePagination"
          class="responsive-data-list-footer"
          :class="{ 'responsive-data-list-footer-clickable': mobileFooterInteractive }"
          :role="mobileFooterInteractive ? 'button' : undefined"
          :tabindex="mobileFooterInteractive ? 0 : undefined"
          @click="handleMobileFooterClick"
          @keydown.enter="handleMobileFooterClick"
          @keydown.space.prevent="handleMobileFooterClick"
        >
          <a-spin v-if="loadingMore" size="small" />
          <span>{{ mobileFooterText }}</span>
        </div>
      </template>
      <slot v-else name="emptyText">
        <a-empty class="page-empty-card" description="暂无数据" />
      </slot>
    </div>
  </div>
</template>

<script setup lang="ts" generic="T extends Record<string, any>">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import DeferredRender from './DeferredRender.vue'
import { changeResponsiveDataListBodyScrollLock } from './responsiveDataListBodyScrollLock'
import { normalizeResponsiveTableColumns } from './responsiveDataListColumns'
import {
  measureResponsiveDataListActionColumnWidth,
  measureResponsiveDataListScrollbarPlaceholder
} from './responsiveDataListDomMeasurement'
import { normalizeResponsiveTableSorter, type ResponsiveDataListSort } from './responsiveDataListSorting'
import {
  buildResponsiveDataListTablePagination,
  buildResponsiveDataListTableScroll,
  hasResponsiveDataListTablePagination,
  resolveResponsiveDataListMobileFooterText,
  resolveResponsiveDataListTablePlaceholderMinHeight,
  resolveResponsiveDataListTableScrollY,
  type ResponsiveDataListTablePagination
} from './responsiveDataListTableLayout'
import {
  buildMobileVirtualItems,
  buildMobileVirtualWindow,
  defaultMobileItemHeight,
  normalizeMobileItemHeightKey,
  type MobileVirtualItem
} from './responsiveDataListVirtualization'
import {
  normalizeResponsiveDataListPullDistance,
  resolveResponsiveDataListPullRefreshText,
  shouldTriggerResponsiveDataListPullRefresh
} from './responsiveDataListPullRefresh'

type RowKey = string | ((record: T) => string | number)
type TablePagination = ResponsiveDataListTablePagination

const props = withDefaults(defineProps<{
  columns: Array<Record<string, any>>
  dataSource: T[]
  rowKey?: RowKey
  loading?: boolean
  scrollX?: number | string
  tableScrollY?: number | string
  tableScrollEnabled?: boolean
  pagination?: TablePagination
  paginationSummary?: boolean
  rowSelection?: Record<string, any>
  expandable?: Record<string, any>
  mobileDataSource?: T[]
  mobilePagination?: boolean
  mobileHasMore?: boolean
  loadingMore?: boolean
  refreshing?: boolean
  pullRefreshEnabled?: boolean
  mobileBreakpoint?: number
  tableClass?: string
  cardClass?: string
  size?: 'small' | 'middle' | 'large'
  lockBodyScroll?: boolean
  adaptiveColumnWidth?: boolean
  rowClickable?: boolean
  deferTableMount?: boolean
  tableMountDelayFrames?: number
  mobileVirtualized?: boolean
  mobileVirtualizeThreshold?: number
}>(), {
  rowKey: 'id',
  loading: false,
  tableScrollY: 'calc(100dvh - 286px)',
  tableScrollEnabled: true,
  pagination: undefined,
  paginationSummary: true,
  mobileBreakpoint: 900,
  tableClass: '',
  cardClass: '',
  size: 'middle',
  lockBodyScroll: true,
  mobilePagination: false,
  mobileHasMore: false,
  loadingMore: false,
  refreshing: false,
  pullRefreshEnabled: false,
  adaptiveColumnWidth: true,
  rowClickable: false,
  deferTableMount: true,
  tableMountDelayFrames: 1,
  mobileVirtualized: true,
  mobileVirtualizeThreshold: 60
})

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
  (event: 'sort-change', sorts: ResponsiveDataListSort[]): void
  (event: 'row-click', record: T, index?: number): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()

defineSlots<{
  emptyText?: () => any
  bodyCell?: (props: { column: Record<string, any>; record: T; index?: number; text?: any; value?: any }) => any
  card?: (props: { record: T; index: number }) => any
}>()

const isMobile = ref(initialMobileState())
const listRootRef = ref<HTMLElement>()
const listHeight = ref(0)
const mobileListRef = ref<HTMLElement>()
const pageActive = ref(false)
const hasOverlayScrollbarPlaceholder = ref(false)
const scrollbarPlaceholderWidth = ref(0)
const pullDistance = ref(0)
const pullRefreshRequested = ref(false)
const touchStartY = ref(0)
const touchStartedAtTop = ref(false)
let listResizeObserver: ResizeObserver | undefined
let tableMutationObserver: MutationObserver | undefined
let mobileItemResizeObserver: ResizeObserver | undefined
let tableScrollbarPlaceholderFrame = 0
let actionColumnMeasureFrame = 0
let mobileMeasurementFrame = 0
let tableScrollbarPlaceholderTimers: number[] = []
let tableScrollbarPlaceholderUpdateQueued = false
let bodyScrollLocked = false
let viewportListenersAttached = false

const mobileDataSource = computed(() => props.mobileDataSource ?? props.dataSource)
const mobileScrollTop = ref(0)
const mobileContainerHeight = ref(0)
const mobileEstimatedItemHeight = ref(defaultMobileItemHeight)
const mobileItemHeightVersion = ref(0)
const mobileItemHeights = new Map<string, number>()
const mobileItemElements = new Map<string, HTMLElement>()
const measuredActionColumnWidth = ref(0)
const tableColumns = computed(() => normalizeResponsiveTableColumns(props.columns, {
  adaptiveColumnWidth: props.adaptiveColumnWidth,
  measuredActionColumnWidth: measuredActionColumnWidth.value
}))
const tableClassNames = computed(() => [
  'responsive-data-list-table',
  props.tableClass,
  { 'responsive-data-list-table-natural': !props.tableScrollEnabled },
  { 'responsive-data-list-table-overlay-scrollbar': hasOverlayScrollbarPlaceholder.value }
])
const tableStyleVars = computed(() => ({
  '--responsive-data-list-scrollbar-placeholder-width': `${scrollbarPlaceholderWidth.value}px`
}))
const tablePagination = computed<TablePagination>(() => buildResponsiveDataListTablePagination(
  props.pagination,
  props.paginationSummary
))

const hasTablePagination = computed(() => hasResponsiveDataListTablePagination(
  tablePagination.value,
  props.dataSource.length
))

const tableScroll = computed(() => buildResponsiveDataListTableScroll({
  tableScrollEnabled: props.tableScrollEnabled,
  scrollX: props.scrollX,
  adaptiveColumnWidth: props.adaptiveColumnWidth,
  tableScrollY: tableScrollY.value
}))

const tableLayout = computed(() => props.adaptiveColumnWidth ? 'auto' : undefined)
const tablePlaceholderMinHeight = computed(() => resolveResponsiveDataListTablePlaceholderMinHeight(tableScrollY.value))

const tableScrollY = computed(() => resolveResponsiveDataListTableScrollY({
  listHeight: listHeight.value,
  hasPagination: hasTablePagination.value,
  tableScrollY: props.tableScrollY
}))

const pullRefreshing = computed(() => props.refreshing && pullRefreshRequested.value)
const pullRefreshText = computed(() => resolveResponsiveDataListPullRefreshText(
  pullDistance.value,
  pullRefreshing.value
))

const mobileFooterText = computed(() => resolveResponsiveDataListMobileFooterText({
  loadingMore: props.loadingMore,
  mobileHasMore: props.mobileHasMore
}))

const mobileFooterInteractive = computed(() => props.mobilePagination && props.mobileHasMore && !props.loadingMore && !props.refreshing)

const shouldVirtualizeMobileCards = computed(() => (
  props.mobileVirtualized &&
  isMobile.value &&
  mobileDataSource.value.length > props.mobileVirtualizeThreshold
))

const mobileVirtualWindow = computed(() => {
  const records = mobileDataSource.value
  void mobileItemHeightVersion.value
  return buildMobileVirtualWindow({
    records,
    shouldVirtualize: shouldVirtualizeMobileCards.value,
    scrollTop: mobileScrollTop.value,
    containerHeight: mobileContainerHeight.value,
    listHeight: listHeight.value,
    estimatedItemHeight: mobileEstimatedItemHeight.value,
    getItemHeight: getMobileItemHeight
  })
})

const mobileVirtualItems = computed<MobileVirtualItem<T>[]>(() => {
  const { start, end } = mobileVirtualWindow.value
  return buildMobileVirtualItems(mobileDataSource.value, start, end, resolveRowKey)
})

function updateViewportState() {
  if (typeof window === 'undefined') return
  isMobile.value = window.innerWidth <= props.mobileBreakpoint
}

function updateListHeight() {
  listHeight.value = listRootRef.value?.clientHeight ?? 0
  updateMobileViewportMetrics()
  queueTableScrollbarPlaceholderUpdate()
}

function initialMobileState() {
  return typeof window !== 'undefined' && window.innerWidth <= props.mobileBreakpoint
}

function resolveRowKey(record: T, index: number): string | number {
  if (typeof props.rowKey === 'function') return props.rowKey(record)
  return record[props.rowKey] ?? index
}

function getMobileItemHeight(record: T, index: number): number {
  return mobileItemHeights.get(mobileItemHeightKey(record, index)) ?? mobileEstimatedItemHeight.value
}

function mobileItemHeightKey(record: T, index: number): string {
  return normalizeMobileItemHeightKey(resolveRowKey(record, index))
}

function updateMobileViewportMetrics() {
  const list = mobileListRef.value
  if (!list) return
  mobileContainerHeight.value = list.clientHeight
  mobileScrollTop.value = list.scrollTop
}

function lockBodyScroll() {
  if (!props.lockBodyScroll || bodyScrollLocked) return
  bodyScrollLocked = true
  changeResponsiveDataListBodyScrollLock(1)
}

function unlockBodyScroll() {
  if (!bodyScrollLocked) return
  bodyScrollLocked = false
  changeResponsiveDataListBodyScrollLock(-1)
}

function queueTableScrollbarPlaceholderUpdate() {
  if (typeof window === 'undefined') return
  if (tableScrollbarPlaceholderUpdateQueued) return
  tableScrollbarPlaceholderUpdateQueued = true
  tableScrollbarPlaceholderFrame = window.requestAnimationFrame(() => {
    tableScrollbarPlaceholderFrame = 0
    updateTableScrollbarPlaceholderState()
  })
  tableScrollbarPlaceholderTimers = [80, 240, 600].map((delay) => window.setTimeout(() => {
    updateTableScrollbarPlaceholderState()
    if (delay === 600) {
      tableScrollbarPlaceholderUpdateQueued = false
      tableScrollbarPlaceholderTimers = []
    }
  }, delay))
}

function queueActionColumnMeasure() {
  if (typeof window === 'undefined') return
  window.cancelAnimationFrame(actionColumnMeasureFrame)
  actionColumnMeasureFrame = window.requestAnimationFrame(() => {
    actionColumnMeasureFrame = 0
    measureActionColumnSlots()
  })
}

function cancelActionColumnMeasure() {
  if (typeof window === 'undefined') return
  window.cancelAnimationFrame(actionColumnMeasureFrame)
  actionColumnMeasureFrame = 0
}

function measureActionColumnSlots() {
  const root = listRootRef.value
  if (!root) return
  const nextWidth = measureResponsiveDataListActionColumnWidth(root, isMobile.value)
  if (nextWidth > 0 && nextWidth !== measuredActionColumnWidth.value) {
    measuredActionColumnWidth.value = nextWidth
    nextTick(queueTableScrollbarPlaceholderUpdate)
  }
}

function cancelTableScrollbarPlaceholderUpdate() {
  if (typeof window === 'undefined') return
  window.cancelAnimationFrame(tableScrollbarPlaceholderFrame)
  tableScrollbarPlaceholderFrame = 0
  tableScrollbarPlaceholderTimers.forEach((timer) => window.clearTimeout(timer))
  tableScrollbarPlaceholderTimers = []
  tableScrollbarPlaceholderUpdateQueued = false
}

function updateTableScrollbarPlaceholderState() {
  const state = measureResponsiveDataListScrollbarPlaceholder({
    tableScrollEnabled: props.tableScrollEnabled,
    isMobile: isMobile.value,
    root: listRootRef.value,
    currentPlaceholderWidth: scrollbarPlaceholderWidth.value
  })
  hasOverlayScrollbarPlaceholder.value = state.hasOverlayScrollbarPlaceholder
  scrollbarPlaceholderWidth.value = state.scrollbarPlaceholderWidth
}

function observeTableMutations() {
  if (typeof MutationObserver === 'undefined' || !listRootRef.value) return
  tableMutationObserver?.disconnect()
  tableMutationObserver = new MutationObserver(() => {
    queueTableScrollbarPlaceholderUpdate()
    queueActionColumnMeasure()
  })
  tableMutationObserver.observe(listRootRef.value, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ['class', 'style']
  })
}

function observeListResize() {
  if (listResizeObserver || typeof ResizeObserver === 'undefined' || !listRootRef.value) return
  listResizeObserver = new ResizeObserver(() => {
    updateListHeight()
    queueTableScrollbarPlaceholderUpdate()
  })
  listResizeObserver.observe(listRootRef.value)
}

function disconnectListResize() {
  listResizeObserver?.disconnect()
  listResizeObserver = undefined
}

function ensureMobileItemResizeObserver() {
  if (mobileItemResizeObserver || typeof ResizeObserver === 'undefined') return
  mobileItemResizeObserver = new ResizeObserver((entries) => {
    let updated = false
    entries.forEach((entry) => {
      if (entry.target instanceof HTMLElement) {
        updated = updateMobileItemHeight(entry.target) || updated
      }
    })
    if (updated) updateMobileEstimatedItemHeight()
  })
}

function observeRenderedMobileItems() {
  ensureMobileItemResizeObserver()
  if (!mobileItemResizeObserver) return
  mobileItemElements.forEach((element) => mobileItemResizeObserver?.observe(element))
}

function disconnectMobileItemResizeObserver() {
  mobileItemResizeObserver?.disconnect()
  mobileItemResizeObserver = undefined
  cancelMobileItemMeasurement()
}

function setMobileItemRef(element: unknown, heightKey: string) {
  const resolvedElement = resolveElementRef(element)
  const existingElement = mobileItemElements.get(heightKey)
  if (existingElement && existingElement !== resolvedElement) {
    mobileItemResizeObserver?.unobserve(existingElement)
    mobileItemElements.delete(heightKey)
  }
  if (!resolvedElement) return
  mobileItemElements.set(heightKey, resolvedElement)
  ensureMobileItemResizeObserver()
  mobileItemResizeObserver?.observe(resolvedElement)
  queueMobileItemMeasurement()
}

function resolveElementRef(element: unknown): HTMLElement | undefined {
  if (typeof HTMLElement === 'undefined') return undefined
  if (element instanceof HTMLElement) return element
  const possibleElement = element && typeof element === 'object' ? (element as { $el?: unknown }).$el : undefined
  return possibleElement instanceof HTMLElement ? possibleElement : undefined
}

function updateMobileItemHeight(element: HTMLElement): boolean {
  const index = Number(element.dataset.mobileListIndex)
  const record = mobileDataSource.value[index]
  if (!record) return false
  const height = Math.ceil(element.getBoundingClientRect().height)
  if (!Number.isFinite(height) || height <= 0) return false
  const heightKey = mobileItemHeightKey(record, index)
  const previousHeight = mobileItemHeights.get(heightKey)
  if (previousHeight !== undefined && Math.abs(previousHeight - height) <= 1) return false
  mobileItemHeights.set(heightKey, height)
  mobileItemHeightVersion.value += 1
  return true
}

function updateMobileEstimatedItemHeight() {
  if (mobileItemHeights.size === 0) {
    mobileEstimatedItemHeight.value = defaultMobileItemHeight
    return
  }
  const heights = Array.from(mobileItemHeights.values())
  const averageHeight = heights.reduce((total, height) => total + height, 0) / heights.length
  mobileEstimatedItemHeight.value = Math.max(96, Math.min(640, Math.round(averageHeight)))
}

function measureRenderedMobileItems() {
  let updated = false
  mobileItemElements.forEach((element) => {
    updated = updateMobileItemHeight(element) || updated
  })
  if (updated) updateMobileEstimatedItemHeight()
}

function queueMobileItemMeasurement() {
  if (typeof window === 'undefined' || mobileMeasurementFrame) return
  mobileMeasurementFrame = window.requestAnimationFrame(() => {
    mobileMeasurementFrame = 0
    measureRenderedMobileItems()
  })
}

function cancelMobileItemMeasurement() {
  if (typeof window === 'undefined' || !mobileMeasurementFrame) return
  window.cancelAnimationFrame(mobileMeasurementFrame)
  mobileMeasurementFrame = 0
}

function pruneMobileItemHeights() {
  if (mobileItemHeights.size <= mobileDataSource.value.length + props.mobileVirtualizeThreshold) return
  const currentKeys = new Set(mobileDataSource.value.map((record, index) => mobileItemHeightKey(record, index)))
  mobileItemHeights.forEach((_, key) => {
    if (!currentKeys.has(key)) mobileItemHeights.delete(key)
  })
  updateMobileEstimatedItemHeight()
  mobileItemHeightVersion.value += 1
}

function addViewportListeners() {
  if (viewportListenersAttached || typeof window === 'undefined') return
  viewportListenersAttached = true
  window.addEventListener('resize', updateViewportState, { passive: true })
  window.addEventListener('resize', updateListHeight, { passive: true })
  window.addEventListener('resize', queueTableScrollbarPlaceholderUpdate, { passive: true })
}

function removeViewportListeners() {
  if (!viewportListenersAttached || typeof window === 'undefined') return
  viewportListenersAttached = false
  window.removeEventListener('resize', updateViewportState)
  window.removeEventListener('resize', updateListHeight)
  window.removeEventListener('resize', queueTableScrollbarPlaceholderUpdate)
}

function handleMobileScroll(event: Event) {
  const target = event.currentTarget as HTMLElement
  mobileScrollTop.value = target.scrollTop
  mobileContainerHeight.value = target.clientHeight
  if (!props.mobilePagination || props.loadingMore || props.refreshing || !props.mobileHasMore) return
  const distanceToBottom = target.scrollHeight - target.scrollTop - target.clientHeight
  if (distanceToBottom <= 80) emit('mobile-load-more')
}

function handleMobileFooterClick() {
  if (!mobileFooterInteractive.value) return
  emit('mobile-load-more')
}

function handleTableChange(...args: unknown[]) {
  emit('change', ...args)
  if (tableChangeAction(args[3]) === 'sort') {
    emit('sort-change', normalizeResponsiveTableSorter(args[2]))
  }
}

function tableChangeAction(value: unknown): string | undefined {
  return value && typeof value === 'object' && typeof (value as { action?: unknown }).action === 'string'
    ? (value as { action: string }).action
    : undefined
}

function tableCustomRow(record: T, index?: number): Record<string, unknown> {
  if (!props.rowClickable) return {}
  return {
    class: 'responsive-data-list-clickable-row',
    onClick: (event: MouseEvent) => {
      if (isInteractiveElementClick(event)) return
      emit('row-click', record, index)
    }
  }
}

function isInteractiveElementClick(event: MouseEvent): boolean {
  const target = event.target
  if (!(target instanceof HTMLElement)) return false
  return Boolean(target.closest([
    'a',
    'button',
    'input',
    'textarea',
    'select',
    '[role="button"]',
    '.ant-checkbox-wrapper',
    '.ant-radio-wrapper',
    '.ant-dropdown-trigger'
  ].join(',')))
}

function handleTouchStart(event: TouchEvent) {
  if (!props.pullRefreshEnabled || props.refreshing || props.loadingMore) return
  touchStartY.value = event.touches[0]?.clientY ?? 0
  touchStartedAtTop.value = (mobileListRef.value?.scrollTop ?? 0) <= 0
}

function handleTouchMove(event: TouchEvent) {
  if (!props.pullRefreshEnabled || !touchStartedAtTop.value || props.refreshing || props.loadingMore) return
  const currentY = event.touches[0]?.clientY ?? 0
  const distance = currentY - touchStartY.value
  pullDistance.value = normalizeResponsiveDataListPullDistance(distance)
}

function handleTouchEnd() {
  if (!props.pullRefreshEnabled) return
  if (shouldTriggerResponsiveDataListPullRefresh(pullDistance.value, props.refreshing, props.loadingMore)) {
    pullRefreshRequested.value = true
    emit('mobile-refresh')
  }
  pullDistance.value = 0
  touchStartedAtTop.value = false
}

watch(() => props.refreshing, (refreshing) => {
  if (!refreshing) {
    pullRefreshRequested.value = false
  }
})

watch(() => props.lockBodyScroll, (enabled) => {
  if (enabled && pageActive.value) {
    lockBodyScroll()
    return
  }
  unlockBodyScroll()
}, { immediate: true })

watch([
  isMobile,
  tableColumns,
  tableScrollY,
  () => props.loading,
  () => props.dataSource.length
], () => {
  nextTick(() => {
    queueTableScrollbarPlaceholderUpdate()
    queueActionColumnMeasure()
  })
}, { flush: 'post' })

watch([
  () => props.loading,
  () => mobileDataSource.value.length,
  shouldVirtualizeMobileCards
], () => {
  nextTick(() => {
    updateMobileViewportMetrics()
    pruneMobileItemHeights()
    queueMobileItemMeasurement()
  })
}, { flush: 'post' })

onMounted(() => {
  pageActive.value = true
  updateViewportState()
  updateListHeight()
  nextTick(() => {
    queueTableScrollbarPlaceholderUpdate()
    queueActionColumnMeasure()
    observeRenderedMobileItems()
    queueMobileItemMeasurement()
  })
  lockBodyScroll()
  observeListResize()
  observeTableMutations()
  addViewportListeners()
})

onActivated(() => {
  pageActive.value = true
  lockBodyScroll()
  observeListResize()
  observeTableMutations()
  addViewportListeners()
  nextTick(() => {
    updateViewportState()
    updateListHeight()
    queueTableScrollbarPlaceholderUpdate()
    queueActionColumnMeasure()
    observeRenderedMobileItems()
    queueMobileItemMeasurement()
  })
})

onDeactivated(() => {
  pageActive.value = false
  unlockBodyScroll()
  tableMutationObserver?.disconnect()
  tableMutationObserver = undefined
  disconnectListResize()
  removeViewportListeners()
  cancelTableScrollbarPlaceholderUpdate()
  cancelActionColumnMeasure()
  disconnectMobileItemResizeObserver()
})

onBeforeUnmount(() => {
  pageActive.value = false
  unlockBodyScroll()
  cancelTableScrollbarPlaceholderUpdate()
  cancelActionColumnMeasure()
  disconnectMobileItemResizeObserver()
  disconnectListResize()
  tableMutationObserver?.disconnect()
  removeViewportListeners()
  mobileItemElements.clear()
  mobileItemHeights.clear()
})
</script>

<style scoped>
:global(body.responsive-data-list-scroll-lock) {
  overflow: hidden;
}

.responsive-data-list {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}

.responsive-data-list-table {
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
}

.responsive-data-list-table-natural {
  display: block;
  flex: initial;
  overflow: visible;
}

.responsive-data-list-table :deep(.ant-spin-nested-loading),
.responsive-data-list-table :deep(.ant-spin-container) {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}

.responsive-data-list-table-natural :deep(.ant-spin-nested-loading),
.responsive-data-list-table-natural :deep(.ant-spin-container) {
  display: block;
  flex: initial;
}

.responsive-data-list-table :deep(.ant-table) {
  min-height: 0;
  flex: 1 1 auto;
}

.responsive-data-list-table-natural :deep(.ant-table) {
  flex: initial;
}

.responsive-data-list-table :deep(.ant-table-body) {
  min-height: 0;
  overscroll-behavior: contain;
}

.responsive-data-list-table :deep(.responsive-data-list-clickable-row) {
  cursor: pointer;
}

.responsive-data-list-table-overlay-scrollbar :deep(.ant-table-cell-scrollbar) {
  display: none;
}

.responsive-data-list-table-overlay-scrollbar :deep(.ant-table-header table) {
  width: calc(100% - var(--responsive-data-list-scrollbar-placeholder-width, 0px)) !important;
}

.responsive-data-list-table-overlay-scrollbar :deep(.responsive-data-list-actions-column.ant-table-cell-fix-right) {
  right: 0 !important;
}

.responsive-data-list-table :deep(.responsive-data-list-actions-column) {
  padding-inline: 8px !important;
  white-space: nowrap;
}

.responsive-data-list-table :deep(.responsive-data-list-actions-column.ant-table-cell-fix-right-first::after),
.responsive-data-list-table :deep(.responsive-data-list-actions-column.ant-table-cell-fix-right-last::after) {
  box-shadow: none !important;
}

.responsive-data-list-table :deep(.responsive-data-list-actions-column.ant-table-cell-fix-right-first),
.responsive-data-list-table :deep(.responsive-data-list-actions-column.ant-table-cell-fix-right-last) {
  border-left: 1px solid #edf1f7;
}

.responsive-data-list-table :deep(.responsive-data-list-actions-column .ant-space) {
  column-gap: 6px !important;
  row-gap: 6px !important;
}

.responsive-data-list-table :deep(.responsive-data-list-actions-column .ant-btn-link) {
  padding-inline: 0 !important;
}

.responsive-data-list-table :deep(.responsive-data-list-flex-column) {
  min-width: 0;
}

.responsive-data-list-cards {
  display: grid;
  min-height: 0;
  flex: 1 1 auto;
  gap: 12px;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding-right: 2px;
}

.responsive-data-list-cards-virtualized {
  gap: 0;
}

.responsive-data-list-item {
  min-width: 0;
  box-sizing: border-box;
  padding-bottom: 12px;
}

.responsive-data-list-item-last {
  padding-bottom: 0;
}

.responsive-data-list-virtual-spacer {
  min-height: 0;
  pointer-events: none;
}

.responsive-data-list-pull,
.responsive-data-list-footer {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 34px;
  color: #64748b;
  font-size: 12px;
}

.responsive-data-list-footer-clickable {
  cursor: pointer;
}

.responsive-data-list-footer-clickable:hover {
  color: #1677ff;
}

.responsive-data-list-pull {
  min-height: 0;
  height: 0;
  overflow: hidden;
  transition: height 0.18s ease;
}

.responsive-data-list-pull.active {
  height: 34px;
}

.responsive-data-list-loading {
  display: flex;
  justify-content: center;
  padding: 36px 0;
}

.responsive-data-list-table-placeholder {
  display: flex;
  min-height: 220px;
  align-items: center;
  justify-content: center;
}
</style>
