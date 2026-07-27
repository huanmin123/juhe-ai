<template>
  <a-card class="page-card announcement-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadData">
      <template #actions>
        <a-button type="primary" @click="openCreate">新增公告</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table announcement-table" :columns="columns" :data-source="announcements" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="1080" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileAnnouncements" @mobile-refresh="refreshMobileAnnouncements">
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无公告，新增发布后用户会在顶部铃铛看到提醒。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'level'">
          <a-tag :color="announcementLevelColor(record.level)">{{ announcementLevelText(record.level) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'title'">
          <span class="announcement-title-cell" :title="record.title">{{ record.title }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="announcementStatusColor(record.status)">{{ announcementStatusText(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'content'">
          <span class="announcement-content-cell" :title="record.contentPreview">{{ record.contentPreview }}</span>
        </template>
        <template v-else-if="column.key === 'publishedAt'">
          <span class="muted-cell">{{ formatDateTime(record.publishedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'updatedAt'">
          <span class="muted-cell">{{ formatDateTime(record.updatedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'updatedByName'">
          <span>{{ record.updatedByName || record.createdByName || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="rowActions(record)" @action-click="handleAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.title }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="announcementLevelColor(record.level)">{{ announcementLevelText(record.level) }}</a-tag>
              <a-tag :color="announcementStatusColor(record.status)">{{ announcementStatusText(record.status) }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>发布时间</span>
              <strong>{{ formatDateTime(record.publishedAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>更新人</span>
              <strong>{{ record.updatedByName || record.createdByName || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>内容</span>
              <strong>{{ record.contentPreview }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="rowActions(record)" @action-click="handleAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑公告' : '新增公告'" width="720px" :confirm-loading="announcementSaving || detailLoading" :ok-button-props="{ disabled: announcementSaving || detailLoading }" @ok="saveAnnouncement">
      <a-form layout="vertical" class="modal-form">
        <a-form-item label="标题" required>
          <a-input v-model:value="form.title" :maxlength="120" show-count placeholder="请输入公告标题" />
        </a-form-item>
        <a-form-item label="内容" required>
          <a-textarea v-model:value="form.content" :rows="8" :maxlength="5000" show-count placeholder="请输入公告内容" />
        </a-form-item>
        <a-row :gutter="16">
          <a-col :xs="24" :sm="12">
            <a-form-item label="重要性">
              <a-select v-model:value="form.level" :options="levelOptions" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="12">
            <a-form-item label="状态">
              <a-select v-model:value="form.status" :options="statusOptions" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-alert type="info" show-icon message="已发布公告会对全部登录用户可见；普通编辑不会重新提醒用户；公告弹窗仅展示最新 30 条公告。" />
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import { sanitizePaginationState, type PagePaginationState } from '@/shared/pageStateSanitizers'
import type { AnnouncementLevel, AnnouncementListItem, AnnouncementStatus } from '@/types/domain'
import {
  announcementLevelColor,
  announcementLevelText,
  announcementStatusColor,
  announcementStatusText
} from './announcementFormatters'

interface AnnouncementsPageState {
  pagination: PagePaginationState
}

const { submitAction, submittingRef } = useSubmitAction('announcements')
const announcementSaving = submittingRef('announcements.save')
const modalOpen = ref(false)
const detailLoading = ref(false)
const editingId = ref<string>()
let announcementDetailRequestGeneration = 0
const pageSize = 50
const pageStateCache = usePageStateCache<AnnouncementsPageState>(undefined, defaultAnnouncementsPageState, {
  sanitize: sanitizeAnnouncementsPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const {
  items: announcements,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileAnnouncements,
  refreshMobile: refreshMobileAnnouncements
} = useResponsivePagedList<AnnouncementListItem>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条公告，还有更多`
    : `共 ${total} 条公告`,
  fetchPage: (_options, pageState) => api.announcements.listPage({
    page: pageState.current,
    pageSize: pageState.pageSize
  }),
  onError: (error) => {
    console.error(error)
    message.error('加载公告失败')
  }
})

const form = reactive({
  title: '',
  content: '',
  level: 'info' as AnnouncementLevel,
  status: 'draft' as AnnouncementStatus
})

const levelOptions = [
  { label: '重要', value: 'critical' },
  { label: '提醒', value: 'warning' },
  { label: '通知', value: 'info' },
  { label: '普通', value: 'normal' }
]

const statusOptions = [
  { label: '草稿', value: 'draft' },
  { label: '已发布', value: 'published' },
  { label: '已下线', value: 'archived' }
]

const titleColumnWidth = 220
const contentColumnWidth = 300

function fixedColumnCellProps(className: string, width: number) {
  return {
    class: className,
    style: {
      width: `${width}px`,
      minWidth: `${width}px`,
      maxWidth: `${width}px`
    }
  }
}

const columns = [
  {
    title: '标题',
    dataIndex: 'title',
    key: 'title',
    width: titleColumnWidth,
    minWidth: titleColumnWidth,
    responsiveFlex: false,
    customHeaderCell: () => fixedColumnCellProps('announcement-title-column', titleColumnWidth),
    customCell: () => fixedColumnCellProps('announcement-title-column', titleColumnWidth)
  },
  { title: '重要性', key: 'level', width: 100 },
  { title: '状态', key: 'status', width: 100 },
  {
    title: '内容',
    key: 'content',
    width: contentColumnWidth,
    minWidth: contentColumnWidth,
    responsiveFlex: false,
    customHeaderCell: () => fixedColumnCellProps('announcement-content-column', contentColumnWidth),
    customCell: () => fixedColumnCellProps('announcement-content-column', contentColumnWidth)
  },
  { title: '发布时间', key: 'publishedAt', width: 180 },
  { title: '更新人', key: 'updatedByName', width: 130 },
  { title: '更新时间', key: 'updatedAt', width: 180 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

function rowActions(record: AnnouncementListItem): RowActionItem[] {
  const publishAction: RowActionItem = record.status === 'published'
    ? { key: 'unpublish', label: '下线', icon: 'disable', tone: 'warning' }
    : { key: 'publish', label: '发布', icon: 'enable', tone: 'success' }
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    publishAction,
    {
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: '确认删除这条公告？',
      confirmOkText: '删除'
    }
  ]
}

function defaultAnnouncementsPageState(): AnnouncementsPageState {
  return {
    pagination: { current: 1, pageSize }
  }
}

function sanitizeAnnouncementsPageState(value: unknown, fallback: AnnouncementsPageState): AnnouncementsPageState {
  const source = value && typeof value === 'object' ? value as Partial<AnnouncementsPageState> : {}
  return {
    pagination: sanitizePaginationState(source.pagination, fallback.pagination)
  }
}

function snapshotPageState(): AnnouncementsPageState {
  return {
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

function openCreate() {
  announcementDetailRequestGeneration += 1
  detailLoading.value = false
  editingId.value = undefined
  Object.assign(form, { title: '', content: '', level: 'info', status: 'draft' })
  modalOpen.value = true
}

async function openEdit(record: AnnouncementListItem) {
  const requestGeneration = ++announcementDetailRequestGeneration
  detailLoading.value = true
  editingId.value = record.id
  try {
    const detail = await api.announcements.detail(record.id)
    if (requestGeneration !== announcementDetailRequestGeneration || editingId.value !== record.id) return
    Object.assign(form, {
      title: detail.title,
      content: detail.content,
      level: detail.level,
      status: detail.status
    })
    modalOpen.value = true
  } catch (error) {
    if (requestGeneration !== announcementDetailRequestGeneration || editingId.value !== record.id) return
    editingId.value = undefined
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载公告详情失败'))
  } finally {
    if (requestGeneration === announcementDetailRequestGeneration) {
      detailLoading.value = false
    }
  }
}

function invalidatePendingAnnouncementDetail(id: string): void {
  if (editingId.value !== id) return
  announcementDetailRequestGeneration += 1
  editingId.value = undefined
  detailLoading.value = false
  modalOpen.value = false
}

const saveAnnouncement = submitAction('announcements.save', async () => {
  const title = form.title.trim()
  const content = form.content.trim()
  if (!title || !content) {
    message.warning('请填写公告标题和内容')
    return
  }
  try {
    const payload = { title, content, level: form.level, status: form.status }
    if (editingId.value) {
      const targetId = editingId.value
      await api.announcements.update(targetId, payload)
      invalidatePendingAnnouncementDetail(targetId)
      message.success('公告已更新')
    } else {
      await api.announcements.create(payload)
      message.success(form.status === 'published' ? '公告已发布' : '公告草稿已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存公告失败'))
  } finally {
  }
})

async function publishAnnouncement(id: string) {
  try {
    await api.announcements.publish(id)
    invalidatePendingAnnouncementDetail(id)
    message.success('公告已发布')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '发布公告失败'))
  }
}

async function unpublishAnnouncement(id: string) {
  try {
    await api.announcements.unpublish(id)
    invalidatePendingAnnouncementDetail(id)
    message.success('公告已下线')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '下线公告失败'))
  }
}

async function removeAnnouncement(id: string) {
  try {
    await api.announcements.delete(id)
    invalidatePendingAnnouncementDetail(id)
    message.success('公告已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除公告失败'))
  }
}

function handleAction(key: string, record: AnnouncementListItem) {
  if (key === 'edit') {
    void openEdit(record)
    return
  }
  if (key === 'publish') {
    void publishAnnouncement(record.id)
    return
  }
  if (key === 'unpublish') {
    void unpublishAnnouncement(record.id)
    return
  }
  if (key === 'delete') {
    void removeAnnouncement(record.id)
  }
}

onMounted(loadData)
</script>

<style scoped>
.announcement-card {
  margin-top: 4px;
}

.announcement-table :deep(.ant-table-cell) {
  vertical-align: top;
}

.announcement-table :deep(.announcement-title-column) {
  width: 220px;
  min-width: 220px;
  max-width: 220px;
}

.announcement-table :deep(.announcement-content-column) {
  width: 300px;
  min-width: 300px;
  max-width: 300px;
}

.announcement-title-cell,
.announcement-content-cell {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mobile-list-card :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
</style>
