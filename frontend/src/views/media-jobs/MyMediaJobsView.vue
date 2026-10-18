<template>
  <a-card class="page-card responsive-page-card">
    <MediaJobsFilterToolbar
      v-model:kind-filter="kindFilter"
      v-model:status-filter="statusFilter"
      :active-filter-count="activeFilterCount"
      :loading="loading"
      :kind-options="mediaJobKindFilterOptions"
      :status-options="mediaJobStatusFilterOptions"
      @refresh="refreshRecords"
      @reset="resetFilters"
      @search="applyFilters"
    />

    <MediaJobsTable
      :columns="mediaJobColumns"
      :records="records"
      :loading="loading"
      :mobile-has-more="mobileHasMore"
      :mobile-loading-more="mobileLoadingMore"
      :pagination="tablePagination"
      @change="handleTableChange"
      @copy-job-id="copyJobId"
      @open-detail="openDetail"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    />

    <MediaJobDetailDrawer v-model:open="detailOpen" :job-id="detailJobId" scope="self" />
  </a-card>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { MediaJobListItem } from '@/types/domain'
import { adaptMediaJobListResult } from './mediaJobsDisplay'
import {
  mediaJobColumns,
  mediaJobKindFilterOptions,
  mediaJobStatusFilterOptions,
  type MediaJobKindFilter,
  type MediaJobStatusFilter
} from './mediaJobsOptions'
import MediaJobDetailDrawer from './MediaJobDetailDrawer.vue'
import MediaJobsFilterToolbar from './MediaJobsFilterToolbar.vue'
import MediaJobsTable from './MediaJobsTable.vue'

// 任务中心（viewScope: 'self'，AI 账户组）：归属由后端钉死当前登录用户
// （/my-media-jobs），行与详情不携带上游任务号等平台内部信息。

type MyMediaJobsPageState = {
  kindFilter: MediaJobKindFilter
  pagination: { current: number; pageSize: number }
  statusFilter: MediaJobStatusFilter
}

const pageSize = 50
const defaultPageState = (): MyMediaJobsPageState => ({
  kindFilter: 'all',
  pagination: { current: 1, pageSize },
  statusFilter: 'all'
})
const pageStateCache = usePageStateCache<MyMediaJobsPageState>(undefined, defaultPageState, { version: 1 })
const initialState = pageStateCache.read()

const kindFilter = ref<MediaJobKindFilter>(initialState.kindFilter)
const statusFilter = ref<MediaJobStatusFilter>(initialState.statusFilter)

const {
  items: records,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileRecords,
  refreshMobile: refreshMobileRecords,
  resetPagination
} = useResponsivePagedList<MediaJobListItem>({
  pageSize,
  initialPagination: initialState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条媒体任务，还有更多`
    : `共 ${total} 条媒体任务`,
  fetchPage: async (_options, pageState) => {
    const result = await api.mediaJobs.myList({
      limit: pageState.pageSize,
      offset: (pageState.current - 1) * pageState.pageSize,
      status: statusFilter.value === 'all' ? undefined : statusFilter.value,
      kind: kindFilter.value === 'all' ? undefined : kindFilter.value
    })
    return adaptMediaJobListResult(result, pageState)
  },
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载媒体任务失败'))
  }
})

const activeFilterCount = computed(() => (kindFilter.value !== 'all' ? 1 : 0) + (statusFilter.value !== 'all' ? 1 : 0))

function applyFilters(): void {
  resetPagination()
  void loadData()
}

function refreshRecords(): void {
  resetPagination()
  void loadData()
}

function resetFilters(): void {
  const defaults = defaultPageState()
  kindFilter.value = defaults.kindFilter
  statusFilter.value = defaults.statusFilter
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

async function copyJobId(jobId: string): Promise<void> {
  await copyTextToClipboard(jobId, '任务 ID 已复制')
}

const detailJobId = ref<string | null>(null)
const detailOpen = ref(false)

function openDetail(jobId: string): void {
  detailJobId.value = jobId
  detailOpen.value = true
}

function snapshotPageState(): MyMediaJobsPageState {
  return {
    kindFilter: kindFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    statusFilter: statusFilter.value
  }
}

watch(snapshotPageState, () => {
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })

void loadData()
</script>
