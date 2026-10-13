<script setup lang="ts">
import { onActivated, onDeactivated, onMounted, ref } from 'vue'

import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import { studioApi } from '@/api/domains/studio'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import type { StudioAccountOption, StudioModelOption, StudioTask } from '@/types/domain'
import { adaptStudioListPage } from '../studioDisplayCommon'
import { useStudioNarrowLayout } from '../shared/useStudioNarrowLayout'
import { useStudioTaskQueue } from '../shared/useStudioTaskQueue'
import VideoQuickForm from './VideoQuickForm.vue'
import VideoTaskList from './VideoTaskList.vue'
import { studioVideoTaskPageSize, VIDEO_WORKBENCH_SCOPE, videoWorkbenchDomain } from './studioVideoOptions'

/**
 * 视频单镜快造容器（规划 F2.3）：上表单下任务/作品。页头"项目工作台"入口
 * 为 P1 占位（disabled + 即将上线）。keepAlive 激活恢复轮询并补一轮同步、
 * 停用暂停轮询与计时（§8 强制点 4）。
 */
const { isNarrow } = useStudioNarrowLayout()

const accounts = ref<StudioAccountOption[]>([])
const loadingAccounts = ref(false)
const models = ref<StudioModelOption[]>([])
const modelsLoaded = ref(false)
const loadingModels = ref(false)
const generating = ref(false)

async function loadAccounts(): Promise<void> {
  loadingAccounts.value = true
  try {
    const result = await studioApi.listAccounts(videoWorkbenchDomain)
    accounts.value = result.rows
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载账户候选失败'))
  } finally {
    loadingAccounts.value = false
  }
}

async function loadModels(): Promise<void> {
  if (modelsLoaded.value || loadingModels.value) return
  loadingModels.value = true
  try {
    const result = await studioApi.listModels(videoWorkbenchDomain)
    models.value = result.rows
    modelsLoaded.value = true
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载模型目录失败'))
  } finally {
    loadingModels.value = false
  }
}

const {
  items: historyTasks,
  loading,
  mobileLoadingMore,
  pagination,
  loadData,
  loadMoreMobile,
  handleTableChange
} = useResponsivePagedList<StudioTask>({
  pageSize: studioVideoTaskPageSize,
  showTotal: (total) => `共 ${total} 条任务`,
  fetchPage: async (_options, pageState) => {
    const result = await studioApi.listTasks({
      limit: pageState.pageSize,
      offset: (pageState.current - 1) * pageState.pageSize,
      kind: 'video'
    })
    return adaptStudioListPage(result, pageState)
  },
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载视频任务失败'))
  }
})

const taskQueue = useStudioTaskQueue({
  scopeId: VIDEO_WORKBENCH_SCOPE,
  kind: 'video',
  fallbackErrorMessage: '创建视频生成任务失败',
  submit: (clientOperationId) => {
    const payload = pendingPayload.value
    if (!payload) return Promise.reject(new Error('生成参数缺失'))
    return studioApi.generateVideo({ ...payload, clientOperationId })
  },
  onSettled: () => {
    // 产物结算落定：补一轮任务列表首页同步（成片进入作品区）。
    pagination.current = 1
    void loadData()
  }
})

const pendingPayload = ref<{ accountId: string; model: string; prompt: string; firstFrameAssetId?: string; seconds?: string | number; size?: string }>()

async function handleGenerate(payload: { accountId: string; model: string; prompt: string; firstFrameAssetId?: string; seconds?: string | number; size?: string }): Promise<void> {
  pendingPayload.value = payload
  generating.value = true
  try {
    await taskQueue.submitTask({ model: payload.model, promptSummary: payload.prompt.slice(0, 120) })
  } finally {
    generating.value = false
  }
}

const quickFormRef = ref<InstanceType<typeof VideoQuickForm>>()

function handleRetry(task: StudioTask): void {
  // 重试 = 换新 clientOperationId 新建任务（决策 #9②）：回填失败任务参数草稿。
  void loadModels().then(() => {
    quickFormRef.value?.applyRetryDraft({ model: task.model, prompt: task.promptSummary })
  })
}

async function handleCancelTask(task: StudioTask): Promise<void> {
  await taskQueue.cancelTask(task)
}

onMounted(() => {
  void loadAccounts()
  void loadData()
  void taskQueue.refreshActiveTasks().then(() => taskQueue.start())
})

let activated = false

onActivated(() => {
  // keepAlive 激活：恢复轮询与计时，并立即补一轮状态同步（§8 强制点 4）。
  if (!activated) {
    activated = true
    return
  }
  void taskQueue.refreshActiveTasks().then(() => taskQueue.start())
  pagination.current = 1
  void loadData()
})

onDeactivated(() => {
  taskQueue.pause()
})
</script>

<template>
  <a-card class="page-card" :body-style="{ padding: '16px' }">
    <div class="studio-video-view__header">
      <div>
        <h2 class="studio-video-view__title">视频生成</h2>
        <p class="studio-video-view__desc">单镜快造：文生视频或图生视频；成片自动保存到素材库。</p>
      </div>
      <a-tooltip title="即将上线">
        <a-button disabled>项目工作台</a-button>
      </a-tooltip>
    </div>

    <div class="studio-video-view__form" :class="{ 'studio-video-view__form--narrow': isNarrow }">
      <VideoQuickForm
        ref="quickFormRef"
        :accounts="accounts"
        :models="models"
        :loading-accounts="loadingAccounts"
        :loading-models="loadingModels"
        :generating="generating"
        @load-models="loadModels"
        @generate="handleGenerate"
      />
    </div>

    <a-divider />

    <VideoTaskList
      :queue-tasks="taskQueue.queueTasks.value"
      :history-tasks="historyTasks"
      :now-ms="taskQueue.nowMs.value"
      :loading="loading"
      :pagination="pagination"
      :has-more="pagination.total > historyTasks.length"
      :loading-more="mobileLoadingMore"
      :cancelling-task-id="taskQueue.cancellingTaskId.value"
      @retry="handleRetry"
      @cancel-task="handleCancelTask"
      @page-change="(page) => handleTableChange({ current: page })"
      @load-more="loadMoreMobile"
    />
  </a-card>
</template>

<style scoped>
.studio-video-view__header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 12px;
}

.studio-video-view__title {
  font-size: 18px;
  margin: 0;
}

.studio-video-view__desc {
  font-size: 12px;
  color: var(--juhe-text-tertiary, #9ca3af);
  margin: 4px 0 0;
}

.studio-video-view__form--narrow {
  max-width: 100%;
}
</style>
