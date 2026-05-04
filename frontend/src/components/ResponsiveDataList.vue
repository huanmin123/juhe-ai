<template>
  <div class="responsive-data-list">
    <a-table
      v-if="!isMobile"
      :class="['responsive-data-list-table', tableClass]"
      :size="size"
      :columns="columns"
      :data-source="dataSource"
      :row-key="rowKey"
      :loading="loading"
      :pagination="pagination"
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
  pagination: false,
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
const mobileListRef = ref<HTMLElement>()
const pullDistance = ref(0)
const pullRefreshRequested = ref(false)
const touchStartY = ref(0)
const touchStartedAtTop = ref(false)
const pullThreshold = 64

const mobileDataSource = computed(() => props.mobileDataSource ?? props.dataSource)

const tableScroll = computed(() => {
  const scroll: Record<string, number | string> = { y: props.tableScrollY }
  if (props.scrollX) scroll.x = props.scrollX
  return scroll
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

function initialMobileState() {
  return typeof window !== 'undefined' && window.innerWidth <= props.mobileBreakpoint
}

function resolveRowKey(record: T, index: number): string | number {
  if (typeof props.rowKey === 'function') return props.rowKey(record)
  return record[props.rowKey] ?? index
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
  changeBodyScrollLock(1)
  window.addEventListener('resize', updateViewportState, { passive: true })
})

onBeforeUnmount(() => {
  changeBodyScrollLock(-1)
  window.removeEventListener('resize', updateViewportState)
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
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
}

.responsive-data-list-table :deep(.ant-spin-nested-loading),
.responsive-data-list-table :deep(.ant-spin-container),
.responsive-data-list-table :deep(.ant-table) {
  height: 100%;
}

.responsive-data-list-table :deep(.ant-table-body) {
  min-height: 0;
  overscroll-behavior: contain;
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
