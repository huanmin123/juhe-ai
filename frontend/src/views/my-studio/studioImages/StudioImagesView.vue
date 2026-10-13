<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, ref } from 'vue'

import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import { studioApi } from '@/api/domains/studio'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import type { StudioAccountOption, StudioAsset, StudioModelOption, StudioTask } from '@/types/domain'
import { adaptStudioListPage } from '../studioDisplayCommon'
import { useStudioNarrowLayout } from '../shared/useStudioNarrowLayout'
import { useStudioTaskQueue } from '../shared/useStudioTaskQueue'
import ImageParamsPanel from './ImageParamsPanel.vue'
import StudioGalleryPanel from './StudioGalleryPanel.vue'
import { imageWorkbenchDomain, IMAGE_WORKBENCH_SCOPE, studioImageGalleryPageSize } from './studioImageOptions'
import { pickStudioImageParams } from './studioImageDisplay'

/**
 * 图像工作台容器（规划 F2.1）：左参数区 360px / 右队列+画廊双面板；
 * 992px 以下参数区折叠为顶部。keepAlive 激活恢复轮询并补一轮同步、
 * 停用暂停轮询与计时（§8 强制点 4）。
 */
const { isNarrow } = useStudioNarrowLayout()

const accounts = ref<StudioAccountOption[]>([])
const loadingAccounts = ref(false)
const models = ref<StudioModelOption[]>([])
const modelsLoaded = ref(false)
const loadingModels = ref(false)
const generating = ref(false)
const deletingAssetId = ref<string>()
const paramsPanelRef = ref<InstanceType<typeof ImageParamsPanel>>()
const galleryVisible = ref(true)

async function loadAccounts(): Promise<void> {
  loadingAccounts.value = true
  try {
    const result = await studioApi.listAccounts(imageWorkbenchDomain)
    accounts.value = result.rows
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载账户候选失败'))
  } finally {
    loadingAccounts.value = false
  }
}

/** 模型目录按需加载（规划 F3.1：打开下拉时加载，缓存于页面内存）。 */
async function loadModels(): Promise<void> {
  if (modelsLoaded.value || loadingModels.value) return
  loadingModels.value = true
  try {
    const result = await studioApi.listModels(imageWorkbenchDomain)
    models.value = result.rows
    modelsLoaded.value = true
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载模型目录失败'))
  } finally {
    loadingModels.value = false
  }
}

const {
  items: galleryAssets,
  loading: galleryLoading,
  mobileLoadingMore,
  pagination,
  loadData,
  loadMoreMobile,
  removeItems
} = useResponsivePagedList<StudioAsset>({
  pageSize: studioImageGalleryPageSize,
  showTotal: (total) => `共 ${total} 件作品`,
  fetchPage: async (_options, pageState) => {
    const result = await studioApi.listAssets({
      limit: pageState.pageSize,
      offset: (pageState.current - 1) * pageState.pageSize,
      kind: imageWorkbenchDomain
    })
    return adaptStudioListPage(result, pageState)
  },
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载作品失败'))
  }
})

const taskQueue = useStudioTaskQueue({
  scopeId: IMAGE_WORKBENCH_SCOPE,
  kind: 'image',
  fallbackErrorMessage: '创建图像生成任务失败',
  submit: (clientOperationId) => {
    const payload = pendingPayload.value
    if (!payload) return Promise.reject(new Error('生成参数缺失'))
    return studioApi.generateImage({ ...payload, clientOperationId })
  },
  onSettled: (task) => {
    if (task.status === 'completed' && task.settlement === 'settled' && task.assetId) {
      // 产物入库完成：补一轮画廊首页同步（新作品落今天分组）。
      pagination.current = 1
      void loadData()
    }
  }
})

const pendingPayload = ref<{ accountId: string; model: string; prompt: string; negativePrompt?: string; size?: string; referenceAssetId?: string }>()

async function handleGenerate(payload: { accountId: string; model: string; prompt: string; negativePrompt?: string; size?: string; referenceAssetId?: string }): Promise<void> {
  pendingPayload.value = payload
  generating.value = true
  try {
    await taskQueue.submitTask({ model: payload.model, promptSummary: payload.prompt.slice(0, 120) })
  } finally {
    generating.value = false
  }
}

function handleRetry(task: StudioTask): void {
  // 重试 = 换新 clientOperationId 新建任务：把失败任务的参数回填参数区。
  void loadModels().then(() => {
    paramsPanelRef.value?.applyParamsSnapshot(pickStudioImageParams({ model: task.model, prompt: task.promptSummary }))
  })
}

async function handleDelete(asset: StudioAsset): Promise<void> {
  deletingAssetId.value = asset.id
  try {
    await studioApi.deleteAsset(asset.id)
    removeItems((item) => item.id === asset.id)
    message.success('素材已删除')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '删除素材失败'))
  } finally {
    deletingAssetId.value = undefined
  }
}

function handleReuse(asset: StudioAsset): void {
  void loadModels().then(() => {
    paramsPanelRef.value?.applyParamsSnapshot(pickStudioImageParams(asset.params))
    if (isNarrow.value) galleryVisible.value = false
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

const hasMore = computed(() => pagination.total > galleryAssets.value.length)
</script>

<template>
  <a-card class="page-card studio-images-view" :body-style="{ padding: '16px' }">
    <div class="studio-images-view__header">
      <div>
        <h2 class="studio-images-view__title">图像生成</h2>
        <p class="studio-images-view__desc">生成结果长期保留在素材库，可回填参数重新生成。</p>
      </div>
      <a-button v-if="isNarrow" size="small" @click="galleryVisible = !galleryVisible">
        {{ galleryVisible ? '收起画廊' : '展开画廊' }}
      </a-button>
    </div>

    <div class="studio-images-view__body" :class="{ 'studio-images-view__body--narrow': isNarrow }">
      <aside v-show="!isNarrow || !galleryVisible" class="studio-images-view__params">
        <ImageParamsPanel
          ref="paramsPanelRef"
          :accounts="accounts"
          :models="models"
          :loading-accounts="loadingAccounts"
          :loading-models="loadingModels"
          :generating="generating"
          @load-models="loadModels"
          @generate="handleGenerate"
        />
      </aside>
      <main v-show="!isNarrow || galleryVisible" class="studio-images-view__gallery">
        <StudioGalleryPanel
          :queue-tasks="taskQueue.queueTasks.value"
          :assets="galleryAssets"
          :loading="galleryLoading"
          :now-ms="taskQueue.nowMs.value"
          :has-more="hasMore"
          :loading-more="mobileLoadingMore"
          :deleting-asset-id="deletingAssetId"
          :cancelling-task-id="taskQueue.cancellingTaskId.value"
          @reuse="handleReuse"
          @delete="handleDelete"
          @retry="handleRetry"
          @cancel-task="handleCancelTask"
          @load-more="loadMoreMobile"
        />
      </main>
    </div>
  </a-card>
</template>

<style scoped>
.studio-images-view__header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 12px;
}

.studio-images-view__title {
  font-size: 18px;
  margin: 0;
}

.studio-images-view__desc {
  font-size: 12px;
  color: var(--juhe-text-tertiary, #9ca3af);
  margin: 4px 0 0;
}

.studio-images-view__body {
  display: flex;
  gap: 16px;
  align-items: flex-start;
}

.studio-images-view__params {
  width: 360px;
  flex-shrink: 0;
}

.studio-images-view__gallery {
  flex: 1;
  min-width: 0;
}

.studio-images-view__body--narrow {
  flex-direction: column;
}

.studio-images-view__body--narrow .studio-images-view__params {
  width: 100%;
}
</style>
