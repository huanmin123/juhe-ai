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
  type MobileVirtualItem
} from './responsiveDataListVirtualization'
import { useResponsiveDataListMobileItemMeasurement } from './useResponsiveDataListMobileItemMeasurement'
import { useResponsiveDataListPullRefresh } from './useResponsiveDataListPullRefresh'
import { useResponsiveDataListViewport } from './useResponsiveDataListViewport'

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

const pageActive = ref(false)
const hasOverlayScrollbarPlaceholder = ref(false)
const scrollbarPlaceholderWidth = ref(0)
let tableMutationObserver: MutationObserver | undefined
let tableScrollbarPlaceholderFrame = 0
let actionColumnMeasureFrame = 0
let tableScrollbarPlaceholderTimers: number[] = []
let tableScrollbarPlaceholderUpdateQueued = false

const mobileDataSource = computed(() => props.mobileDataSource ?? props.dataSource)
const {
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
} = useResponsiveDataListViewport({
  getMobileBreakpoint: () => props.mobileBreakpoint,
  shouldLockBodyScroll: () => props.lockBodyScroll,
  onListHeightUpdated: () => queueTableScrollbarPlaceholderUpdate(),
  onViewportResize: () => queueTableScrollbarPlaceholderUpdate()
})
const {
  estimatedItemHeight: mobileEstimatedItemHeight,
  itemHeightVersion: mobileItemHeightVersion,
  getItemHeight: getMobileItemHeight,
  setItemRef: setMobileItemRef,
  observeRenderedItems: observeRenderedMobileItems,
  disconnectItemResizeObserver: disconnectMobileItemResizeObserver,
  queueMeasurement: queueMobileItemMeasurement,
  pruneItemHeights: pruneMobileItemHeights,
  clear: clearMobileItemMeasurement
} = useResponsiveDataListMobileItemMeasurement<T>({
  records: mobileDataSource,
  resolveRowKey,
  getPruneThreshold: () => props.mobileVirtualizeThreshold
})
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

const {
  pullDistance,
  pullRefreshing,
  pullRefreshText,
  handleTouchStart,
  handleTouchMove,
  handleTouchEnd
} = useResponsiveDataListPullRefresh({
  isEnabled: () => props.pullRefreshEnabled,
  isRefreshing: () => props.refreshing,
  isLoadingMore: () => props.loadingMore,
  getScrollTop: () => mobileListRef.value?.scrollTop ?? 0,
  onRefresh: () => emit('mobile-refresh')
})

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

function resolveRowKey(record: T, index: number): string | number {
  if (typeof props.rowKey === 'function') return props.rowKey(record)
  return record[props.rowKey] ?? index
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

function handleMobileScroll(event: Event) {
  const target = updateMobileScrollMetrics(event)
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
  clearMobileItemMeasurement()
})
</script>

<style scoped src="./ResponsiveDataList.css"></style>
