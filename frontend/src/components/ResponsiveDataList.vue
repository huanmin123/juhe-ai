<template>
  <div ref="listRootRef" class="responsive-data-list">
    <a-table
      v-if="!isMobile"
      :class="['responsive-data-list-table', tableClass]"
      :size="size"
      :columns="tableColumns"
      :data-source="dataSource"
      :row-key="rowKey"
      :loading="loading"
      :pagination="tablePagination"
      :scroll="tableScroll"
      :row-selection="rowSelection"
      @change="(...args: unknown[]) => emit('change', ...args)"
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

    <div
      v-else
      ref="mobileListRef"
      :class="['responsive-data-list-cards', cardClass]"
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
        <template v-for="(record, index) in mobileDataSource" :key="resolveRowKey(record, index)">
          <slot name="card" :record="record" :index="index" />
        </template>
        <div v-if="mobilePagination" class="responsive-data-list-footer">
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
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

type RowKey = string | ((record: T) => string | number)
type TablePagination = false | Record<string, any>

const props = withDefaults(defineProps<{
  columns: Array<Record<string, any>>
  dataSource: T[]
  rowKey?: RowKey
  loading?: boolean
  scrollX?: number | string
  tableScrollY?: number | string
  pagination?: TablePagination
  rowSelection?: Record<string, any>
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
}>(), {
  rowKey: 'id',
  loading: false,
  tableScrollY: 'calc(100dvh - 286px)',
  pagination: undefined,
  mobileBreakpoint: 900,
  tableClass: '',
  cardClass: '',
  size: 'middle',
  lockBodyScroll: true,
  mobilePagination: false,
  mobileHasMore: false,
  loadingMore: false,
  refreshing: false,
  pullRefreshEnabled: false
})

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
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
const pullDistance = ref(0)
const pullRefreshRequested = ref(false)
const touchStartY = ref(0)
const touchStartedAtTop = ref(false)
const pullThreshold = 64
const defaultPageSize = 20
const tableHeaderHeight = 47
const tablePaginationHeight = 56
const minTableBodyHeight = 160
let listResizeObserver: ResizeObserver | undefined

const mobileDataSource = computed(() => props.mobileDataSource ?? props.dataSource)
const tableColumns = computed(() => normalizeTableColumns(props.columns))
const tablePagination = computed<TablePagination>(() => {
  if (props.pagination === false) return false
  return {
    pageSize: defaultPageSize,
    hideOnSinglePage: true,
    showSizeChanger: false,
    showTotal: (total: number) => `共 ${total} 条`,
    ...(props.pagination ?? {})
  }
})

const hasTablePagination = computed(() => {
  const pagination = tablePagination.value
  if (pagination === false) return false
  const pageSize = numberFromPagination(pagination.pageSize) ?? defaultPageSize
  const total = numberFromPagination(pagination.total) ?? props.dataSource.length
  return pagination.hideOnSinglePage === false || total > pageSize
})

const tableScroll = computed(() => {
  const scroll: Record<string, number | string> = { y: tableScrollY.value }
  if (props.scrollX) scroll.x = props.scrollX
  return scroll
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
  return props.mobileHasMore ? '上拉加载更多' : '没有更多了'
})

function updateViewportState() {
  if (typeof window === 'undefined') return
  isMobile.value = window.innerWidth <= props.mobileBreakpoint
}

function updateListHeight() {
  listHeight.value = listRootRef.value?.clientHeight ?? 0
}

function initialMobileState() {
  return typeof window !== 'undefined' && window.innerWidth <= props.mobileBreakpoint
}

function resolveRowKey(record: T, index: number): string | number {
  if (typeof props.rowKey === 'function') return props.rowKey(record)
  return record[props.rowKey] ?? index
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
    const width = resolveActionColumnWidth(column.width)
    return withCellProps({
      ...column,
      width,
      className: mergeClassName(column.className, 'responsive-data-list-actions-column')
    }, {
      class: 'responsive-data-list-actions-column',
      style: fixedColumnStyle(width)
    })
  }
  if (isFlexColumn) {
    const minWidth = resolveColumnMinWidth(column)
    const { width: _width, ...restColumn } = column
    return withCellProps({
      ...restColumn,
      minWidth,
      className: mergeClassName(column.className, 'responsive-data-list-flex-column')
    }, {
      class: 'responsive-data-list-flex-column',
      style: { minWidth: `${minWidth}px` }
    })
  }
  return column
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

function resolveActionColumnWidth(value: unknown): number {
  const width = typeof value === 'number' ? value : Number.parseFloat(String(value ?? ''))
  if (!Number.isFinite(width) || width <= 0) return 120
  return Math.min(Math.max(width, 96), 128)
}

function resolveColumnMinWidth(column: Record<string, any>): number {
  const minWidth = typeof column.minWidth === 'number' ? column.minWidth : Number.parseFloat(String(column.minWidth ?? ''))
  if (Number.isFinite(minWidth) && minWidth > 0) return minWidth
  const width = typeof column.width === 'number' ? column.width : Number.parseFloat(String(column.width ?? ''))
  if (Number.isFinite(width) && width > 0) return width
  return 160
}

function fixedColumnStyle(width: number): Record<string, string> {
  const size = `${width}px`
  return {
    width: size,
    minWidth: size,
    maxWidth: size
  }
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
    style: mergeStyle(base.style, propsToMerge.style)
  }
}

function mergeClassName(...values: unknown[]): string {
  return values.filter(Boolean).map(String).join(' ')
}

function mergeStyle(baseStyle: unknown, styleToMerge: Record<string, string>): unknown {
  if (!baseStyle) return styleToMerge
  if (typeof baseStyle === 'string') {
    return `${baseStyle};${Object.entries(styleToMerge).map(([key, value]) => `${toKebabCase(key)}:${value}`).join(';')}`
  }
  if (Array.isArray(baseStyle)) return [...baseStyle, styleToMerge]
  if (typeof baseStyle === 'object') return { ...(baseStyle as Record<string, unknown>), ...styleToMerge }
  return styleToMerge
}

function toKebabCase(value: string): string {
  return value.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)
}

function changeBodyScrollLock(delta: number) {
  if (!props.lockBodyScroll) return
  const body = document.body as HTMLBodyElement & Record<string, any>
  const nextCount = Math.max(0, Number(body[scrollLockCountKey] ?? 0) + delta)
  body[scrollLockCountKey] = nextCount
  body.classList.toggle(scrollLockClassName, nextCount > 0)
}

function handleMobileScroll(event: Event) {
  if (!props.mobilePagination || props.loadingMore || props.refreshing || !props.mobileHasMore) return
  const target = event.currentTarget as HTMLElement
  const distanceToBottom = target.scrollHeight - target.scrollTop - target.clientHeight
  if (distanceToBottom <= 80) emit('mobile-load-more')
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

onMounted(() => {
  updateViewportState()
  updateListHeight()
  changeBodyScrollLock(1)
  if (typeof ResizeObserver !== 'undefined' && listRootRef.value) {
    listResizeObserver = new ResizeObserver(updateListHeight)
    listResizeObserver.observe(listRootRef.value)
  }
  window.addEventListener('resize', updateViewportState, { passive: true })
  window.addEventListener('resize', updateListHeight, { passive: true })
})

onBeforeUnmount(() => {
  changeBodyScrollLock(-1)
  listResizeObserver?.disconnect()
  window.removeEventListener('resize', updateViewportState)
  window.removeEventListener('resize', updateListHeight)
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

.responsive-data-list-table :deep(.ant-spin-nested-loading),
.responsive-data-list-table :deep(.ant-spin-container) {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}

.responsive-data-list-table :deep(.ant-table) {
  min-height: 0;
  flex: 1 1 auto;
}

.responsive-data-list-table :deep(.ant-table-body) {
  min-height: 0;
  overscroll-behavior: contain;
}

.responsive-data-list-table :deep(.responsive-data-list-actions-column) {
  white-space: nowrap;
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
</style>
