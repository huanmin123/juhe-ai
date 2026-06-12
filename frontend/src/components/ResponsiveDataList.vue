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
import { normalizeResponsiveTableSorter, type ResponsiveDataListSort } from './responsiveDataListSorting'
import { rowActionColumnWidth } from './rowActions'
import { tableColumnManualWidthMarker } from './tableColumnSettings'

type RowKey = string | ((record: T) => string | number)
type TablePagination = false | Record<string, any>

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
  (event: 'column-resize', payload: { key: string; width: number }): void
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

const scrollLockClassName = 'responsive-data-list-scroll-lock'
const scrollLockCountKey = '__responsiveDataListScrollLockCount'
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
const pullThreshold = 64
const defaultPageSize = 20
const tableHeaderHeight = 47
const tablePaginationHeight = 56
const minTableBodyHeight = 160
const defaultMobileItemHeight = 148
const mobileVirtualOverscanItems = 10
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

type MobileVirtualItem<TRecord> = {
  record: TRecord
  index: number
  key: string | number
  heightKey: string
}

const mobileDataSource = computed(() => props.mobileDataSource ?? props.dataSource)
const mobileScrollTop = ref(0)
const mobileContainerHeight = ref(0)
const mobileEstimatedItemHeight = ref(defaultMobileItemHeight)
const mobileItemHeightVersion = ref(0)
const mobileItemHeights = new Map<string, number>()
const mobileItemElements = new Map<string, HTMLElement>()
const measuredActionColumnWidth = ref(0)
const tableColumns = computed(() => normalizeTableColumns(props.columns))
const tableClassNames = computed(() => [
  'responsive-data-list-table',
  props.tableClass,
  { 'responsive-data-list-table-natural': !props.tableScrollEnabled },
  { 'responsive-data-list-table-overlay-scrollbar': hasOverlayScrollbarPlaceholder.value }
])
const tableStyleVars = computed(() => ({
  '--responsive-data-list-scrollbar-placeholder-width': `${scrollbarPlaceholderWidth.value}px`
}))
const tablePagination = computed<TablePagination>(() => {
  if (props.pagination === false) return false
  const mergedPagination: Record<string, any> = {
    pageSize: defaultPageSize,
    hideOnSinglePage: true,
    showSizeChanger: false,
    showTotal: (total: number) => `共 ${total} 条`,
    ...(props.pagination ?? {})
  }
  if (!props.paginationSummary || mergedPagination.showTotal === false) {
    const paginationWithoutTotal = { ...mergedPagination }
    delete paginationWithoutTotal.showTotal
    return paginationWithoutTotal
  }
  return mergedPagination
})

const hasTablePagination = computed(() => {
  const pagination = tablePagination.value
  if (pagination === false) return false
  const pageSize = numberFromPagination(pagination.pageSize) ?? defaultPageSize
  const total = numberFromPagination(pagination.total) ?? props.dataSource.length
  return pagination.hideOnSinglePage === false || total > pageSize
})

const tableScroll = computed(() => {
  if (!props.tableScrollEnabled) {
    return props.scrollX ? { x: props.adaptiveColumnWidth ? 'max-content' : props.scrollX } : undefined
  }
  const scroll: Record<string, number | string> = { y: tableScrollY.value }
  if (props.scrollX) scroll.x = props.adaptiveColumnWidth ? 'max-content' : props.scrollX
  return scroll
})

const tableLayout = computed(() => props.adaptiveColumnWidth ? 'auto' : undefined)
const tablePlaceholderMinHeight = computed(() => {
  const height = tableScrollY.value
  return typeof height === 'number' ? height + tableHeaderHeight : 220
})

const tableScrollY = computed(() => {
  if (listHeight.value > 0) {
    const paginationHeight = hasTablePagination.value ? tablePaginationHeight : 0
    return Math.max(minTableBodyHeight, listHeight.value - tableHeaderHeight - paginationHeight)
  }
  return adjustTableScrollY(props.tableScrollY, tableHeaderHeight + (hasTablePagination.value ? tablePaginationHeight : 0))
})

const pullRefreshText = computed(() => {
  if (pullRefreshing.value) return '正在刷新...'
  if (pullDistance.value >= pullThreshold) return '松开刷新'
  if (pullDistance.value > 0) return '下拉刷新'
  return '下拉刷新'
})

const pullRefreshing = computed(() => props.refreshing && pullRefreshRequested.value)

const mobileFooterText = computed(() => {
  if (props.loadingMore) return '正在加载更多...'
  return props.mobileHasMore ? '上拉或点击加载更多' : '没有更多了'
})

const mobileFooterInteractive = computed(() => props.mobilePagination && props.mobileHasMore && !props.loadingMore && !props.refreshing)

const shouldVirtualizeMobileCards = computed(() => (
  props.mobileVirtualized &&
  isMobile.value &&
  mobileDataSource.value.length > props.mobileVirtualizeThreshold
))

const mobileVirtualWindow = computed(() => {
  const records = mobileDataSource.value
  const total = records.length
  if (!shouldVirtualizeMobileCards.value || total === 0) {
    return { start: 0, end: total, topPadding: 0, bottomPadding: 0 }
  }

  void mobileItemHeightVersion.value
  const viewportHeight = Math.max(mobileContainerHeight.value || listHeight.value || 600, 240)
  const estimatedItemHeight = Math.max(1, mobileEstimatedItemHeight.value)
  const overscanHeight = estimatedItemHeight * mobileVirtualOverscanItems
  const visibleStart = Math.max(0, mobileScrollTop.value - overscanHeight)
  const visibleEnd = mobileScrollTop.value + viewportHeight + overscanHeight

  let offset = 0
  let start = 0
  for (; start < total; start += 1) {
    const itemHeight = getMobileItemHeight(records[start], start)
    if (offset + itemHeight >= visibleStart) break
    offset += itemHeight
  }

  const topPadding = offset
  let end = start
  for (; end < total; end += 1) {
    offset += getMobileItemHeight(records[end], end)
    if (offset >= visibleEnd) {
      end += 1
      break
    }
  }
  end = Math.min(total, Math.max(start + 1, end))

  let totalHeight = offset
  for (let index = end; index < total; index += 1) {
    totalHeight += getMobileItemHeight(records[index], index)
  }

  return {
    start,
    end,
    topPadding,
    bottomPadding: Math.max(0, totalHeight - offset)
  }
})

const mobileVirtualItems = computed<MobileVirtualItem<T>[]>(() => {
  const { start, end } = mobileVirtualWindow.value
  return mobileDataSource.value.slice(start, end).map((record, offset) => {
    const index = start + offset
    const key = resolveRowKey(record, index)
    return {
      record,
      index,
      key,
      heightKey: normalizeMobileItemHeightKey(key)
    }
  })
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

function normalizeMobileItemHeightKey(key: string | number): string {
  return `${typeof key}:${String(key)}`
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

function numberFromPagination(value: unknown): number | undefined {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue > 0 ? numberValue : undefined
}

function adjustTableScrollY(value: number | string, offset: number): number | string {
  if (typeof value === 'number') return Math.max(0, value - offset)
  return `calc(${value} - ${offset}px)`
}

function normalizeTableColumns(columns: Array<Record<string, any>>): Array<Record<string, any>> {
  const flexColumnIndex = findFlexColumnIndex(columns)
  return columns.map((column, index) => normalizeTableColumn(column, index === flexColumnIndex))
}

function normalizeTableColumn(column: Record<string, any>, isFlexColumn: boolean): Record<string, any> {
  if (Array.isArray(column.children)) {
    return {
      ...column,
      children: normalizeTableColumns(column.children)
    }
  }
  if (isActionColumn(column)) {
    const width = resolveActionColumnWidth(column.width, column.actionCount)
    const { actionCount: _actionCount, [tableColumnManualWidthMarker]: _manualWidth, ...restColumn } = column
    return withColumnResizeHeaderProps(withCellProps({
      ...restColumn,
      width,
      className: mergeClassName(column.className, 'responsive-data-list-actions-column')
    }, {
      class: 'responsive-data-list-actions-column'
    }))
  }
  if (props.adaptiveColumnWidth && !column.fixed) {
    const minWidth = resolveColumnMinWidth(column)
    const manualWidth = isManualColumnWidth(column) ? resolveColumnWidth(column) : undefined
    const { width: _width, [tableColumnManualWidthMarker]: _manualWidth, ...restColumn } = column
    return withColumnResizeHeaderProps(withCellProps({
      ...restColumn,
      ...(manualWidth === undefined ? {} : { width: manualWidth }),
      minWidth,
      className: mergeClassName(column.className, 'responsive-data-list-auto-column', isFlexColumn ? 'responsive-data-list-flex-column' : undefined)
    }, {
      class: mergeClassName('responsive-data-list-auto-column', isFlexColumn ? 'responsive-data-list-flex-column' : undefined),
      style: { minWidth: `${minWidth}px` }
    }))
  }
  return withColumnResizeHeaderProps(stripInternalColumnProps(column))
}

function findFlexColumnIndex(columns: Array<Record<string, any>>): number {
  let bestIndex = -1
  let bestScore = -1
  columns.forEach((column, index) => {
    if (Array.isArray(column.children) || isActionColumn(column) || column.fixed || column.responsiveFlex === false) return
    const score = column.responsiveFlex === true ? 1000 : flexColumnScore(column, index)
    if (score > bestScore) {
      bestScore = score
      bestIndex = index
    }
  })
  return bestIndex
}

function flexColumnScore(column: Record<string, any>, index: number): number {
  const key = String(column.key ?? column.dataIndex ?? '')
  const title = String(column.title ?? '')
  if (key === 'description' || title.includes('说明') || title.includes('备注')) return 900
  if (key === 'notes' || key === 'remark') return 880
  if (key === 'usage' || key === 'usageTotal' || title.includes('用量')) return 760
  if (key === 'capabilities' || title.includes('能力')) return 740
  if (key === 'baseUrl' || key === 'host' || key === 'key') return 720
  if (key === 'resource' || key === 'group') return 700
  if (key === 'displayName') return 680
  if (key === 'name' || title.includes('名称')) return 660
  if (key === 'systemAccount') return 620
  return Math.max(1, 200 - index)
}

function isActionColumn(column: Record<string, any>): boolean {
  return column.key === 'actions' || column.dataIndex === 'actions' || column.title === '操作'
}

function resolveActionColumnWidth(value: unknown, actionCount: unknown): number | string {
  if (measuredActionColumnWidth.value > 0) return measuredActionColumnWidth.value
  if (typeof value === 'number' && Number.isFinite(value) && value > 0) return value
  if (typeof value === 'string') {
    const trimmedValue = value.trim()
    const numericWidth = Number(trimmedValue)
    if (Number.isFinite(numericWidth) && numericWidth > 0) return numericWidth
    if (Number.isFinite(Number.parseFloat(trimmedValue)) && Number.parseFloat(trimmedValue) > 0) return trimmedValue
  }
  const numericActionCount = typeof actionCount === 'number' ? actionCount : Number.parseFloat(String(actionCount ?? ''))
  return rowActionColumnWidth(Number.isFinite(numericActionCount) ? numericActionCount : undefined)
}

function resolveColumnMinWidth(column: Record<string, any>): number {
  const minWidth = typeof column.minWidth === 'number' ? column.minWidth : Number.parseFloat(String(column.minWidth ?? ''))
  if (Number.isFinite(minWidth) && minWidth > 0) return minWidth
  const width = typeof column.width === 'number' ? column.width : Number.parseFloat(String(column.width ?? ''))
  if (Number.isFinite(width) && width > 0) return width
  return 160
}

function withColumnResizeHeaderProps(column: Record<string, any>): Record<string, any> {
  if (!isResizableColumn(column)) return column
  const columnKey = tableColumnKey(column)
  const width = resolveColumnWidth(column) ?? resolveColumnMinWidth(column)
  return {
    ...column,
    customHeaderCell: (...args: any[]) => mergeCellProps(column.customHeaderCell?.(...args), {
      class: 'responsive-data-list-resizable-header',
      style: { width: `${width}px` },
      onMousedown: (event: MouseEvent) => handleColumnResizePointerDown(event, columnKey, width)
    })
  }
}

function isResizableColumn(column: Record<string, any>): boolean {
  return column.resizable !== false && !Array.isArray(column.children) && tableColumnKey(column) !== ''
}

function isManualColumnWidth(column: Record<string, any>): boolean {
  return column[tableColumnManualWidthMarker] === true
}

function stripInternalColumnProps(column: Record<string, any>): Record<string, any> {
  if (!(tableColumnManualWidthMarker in column)) return column
  const { [tableColumnManualWidthMarker]: _manualWidth, ...restColumn } = column
  return restColumn
}

function handleColumnResizePointerDown(event: MouseEvent, key: string, startWidth: number): void {
  const target = event.currentTarget
  if (!(target instanceof HTMLElement) || !isColumnResizeEdge(event, target)) return
  event.preventDefault()
  event.stopPropagation()
  const startX = event.clientX
  const minWidth = 72
  const maxWidth = 720
  const move = (moveEvent: MouseEvent) => {
    moveEvent.preventDefault()
    emit('column-resize', {
      key,
      width: Math.max(minWidth, Math.min(maxWidth, Math.round(startWidth + moveEvent.clientX - startX)))
    })
  }
  const up = () => {
    document.removeEventListener('mousemove', move)
    document.removeEventListener('mouseup', up)
    document.body.classList.remove('responsive-data-list-column-resizing')
  }
  document.body.classList.add('responsive-data-list-column-resizing')
  document.addEventListener('mousemove', move)
  document.addEventListener('mouseup', up, { once: true })
}

function isColumnResizeEdge(event: MouseEvent, target: HTMLElement): boolean {
  const rect = target.getBoundingClientRect()
  return event.clientX >= rect.right - 8 && event.clientX <= rect.right + 4
}

function tableColumnKey(column: Record<string, any>): string {
  const value = column.key ?? column.dataIndex
  if (Array.isArray(value)) return value.map(String).join('.')
  if (value !== undefined && value !== null && String(value).trim()) return String(value)
  return ''
}

function resolveColumnWidth(column: Record<string, any>): number | undefined {
  const width = typeof column.width === 'number' ? column.width : Number.parseFloat(String(column.width ?? ''))
  return Number.isFinite(width) && width > 0 ? width : undefined
}

function withCellProps(column: Record<string, any>, propsToMerge: Record<string, any>): Record<string, any> {
  return {
    ...column,
    customHeaderCell: (...args: any[]) => mergeCellProps(column.customHeaderCell?.(...args), propsToMerge),
    customCell: (...args: any[]) => mergeCellProps(column.customCell?.(...args), propsToMerge)
  }
}

function mergeCellProps(baseProps: Record<string, any> | undefined, propsToMerge: Record<string, any>): Record<string, any> {
  const base = baseProps ?? {}
  return {
    ...base,
    class: mergeClassName(base.class, propsToMerge.class),
    style: mergeStyle(base.style, propsToMerge.style),
    onMousedown: mergeEventHandlers(base.onMousedown, propsToMerge.onMousedown)
  }
}

function mergeClassName(...values: unknown[]): string {
  return values.filter(Boolean).map(String).join(' ')
}

function mergeStyle(baseStyle: unknown, styleToMerge?: Record<string, string>): unknown {
  if (!styleToMerge) return baseStyle
  if (!baseStyle) return styleToMerge
  if (typeof baseStyle === 'string') {
    return `${baseStyle};${Object.entries(styleToMerge).map(([key, value]) => `${toKebabCase(key)}:${value}`).join(';')}`
  }
  if (Array.isArray(baseStyle)) return [...baseStyle, styleToMerge]
  if (typeof baseStyle === 'object') return { ...(baseStyle as Record<string, unknown>), ...styleToMerge }
  return styleToMerge
}

function mergeEventHandlers(first: unknown, second: unknown): unknown {
  if (typeof first !== 'function') return second
  if (typeof second !== 'function') return first
  return (...args: unknown[]) => {
    first(...args)
    second(...args)
  }
}

function toKebabCase(value: string): string {
  return value.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)
}

function changeBodyScrollLock(delta: number) {
  if (typeof document === 'undefined') return
  const body = document.body as HTMLBodyElement & Record<string, any>
  const nextCount = Math.max(0, Number(body[scrollLockCountKey] ?? 0) + delta)
  body[scrollLockCountKey] = nextCount
  body.classList.toggle(scrollLockClassName, nextCount > 0)
}

function lockBodyScroll() {
  if (!props.lockBodyScroll || bodyScrollLocked) return
  bodyScrollLocked = true
  changeBodyScrollLock(1)
}

function unlockBodyScroll() {
  if (!bodyScrollLocked) return
  bodyScrollLocked = false
  changeBodyScrollLock(-1)
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
  if (isMobile.value) return
  const root = listRootRef.value
  if (!root) return
  const actionRoots = root.querySelectorAll<HTMLElement>('.responsive-data-list-actions-column .row-actions[data-row-action-slots]')
  let nextSlotCount = 0
  actionRoots.forEach((element) => {
    const slotCount = Number.parseInt(element.dataset.rowActionSlots ?? '', 10)
    if (Number.isFinite(slotCount) && slotCount > nextSlotCount) {
      nextSlotCount = slotCount
    }
  })
  const nextWidth = Math.max(
    nextSlotCount > 0 ? rowActionColumnWidth(nextSlotCount) : 0,
    measureActionColumnContentWidth(root)
  )
  if (nextWidth > 0 && nextWidth !== measuredActionColumnWidth.value) {
    measuredActionColumnWidth.value = nextWidth
    nextTick(queueTableScrollbarPlaceholderUpdate)
  }
}

function measureActionColumnContentWidth(root: HTMLElement): number {
  const cells = root.querySelectorAll<HTMLElement>('.ant-table-tbody .responsive-data-list-actions-column')
  let maxContentWidth = 0
  cells.forEach((cell) => {
    const rects = Array.from(cell.children)
      .map((child) => child.getBoundingClientRect())
      .filter((rect) => rect.width > 0)
    if (!rects.length) return
    const left = Math.min(...rects.map((rect) => rect.left))
    const right = Math.max(...rects.map((rect) => rect.right))
    maxContentWidth = Math.max(maxContentWidth, Math.ceil(right - left) + 16)
  })
  return maxContentWidth
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
  if (!props.tableScrollEnabled) {
    hasOverlayScrollbarPlaceholder.value = false
    scrollbarPlaceholderWidth.value = 0
    return
  }
  if (isMobile.value) {
    hasOverlayScrollbarPlaceholder.value = false
    scrollbarPlaceholderWidth.value = 0
    return
  }
  const root = listRootRef.value
  const body = root?.querySelector<HTMLElement>('.ant-table-body')
  const scrollbarCell = root?.querySelector<HTMLElement>('.ant-table-cell-scrollbar')
  if (!body || !scrollbarCell) {
    hasOverlayScrollbarPlaceholder.value = false
    scrollbarPlaceholderWidth.value = 0
    return
  }

  const actualScrollbarWidth = Math.max(0, body.offsetWidth - body.clientWidth)
  const measuredPlaceholderWidth = Math.round(scrollbarCell.getBoundingClientRect().width)
  const placeholderWidth = measuredPlaceholderWidth > 0 ? measuredPlaceholderWidth : scrollbarPlaceholderWidth.value
  hasOverlayScrollbarPlaceholder.value = placeholderWidth > 0 && actualScrollbarWidth <= 1
  scrollbarPlaceholderWidth.value = hasOverlayScrollbarPlaceholder.value ? placeholderWidth : 0
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
  pullDistance.value = distance > 0 ? Math.min(distance, 96) : 0
}

function handleTouchEnd() {
  if (!props.pullRefreshEnabled) return
  if (pullDistance.value >= pullThreshold && !props.refreshing && !props.loadingMore) {
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

:global(body.responsive-data-list-column-resizing) {
  cursor: col-resize !important;
  user-select: none;
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

.responsive-data-list-table :deep(.responsive-data-list-resizable-header) {
  position: relative;
}

.responsive-data-list-table :deep(.responsive-data-list-resizable-header::after) {
  position: absolute;
  top: 20%;
  right: 0;
  width: 8px;
  height: 60%;
  border-right: 2px solid transparent;
  cursor: col-resize;
  content: "";
  transition: border-color 0.16s ease;
}

.responsive-data-list-table :deep(.responsive-data-list-resizable-header:hover::after) {
  border-right-color: #94a3b8;
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
