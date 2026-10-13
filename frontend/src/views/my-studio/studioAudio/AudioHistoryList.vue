<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import { studioApi, studioAssetContentUrl } from '@/api/domains/studio'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import type { StudioAsset } from '@/types/domain'
import { adaptStudioListPage, formatStudioDateTime } from '../studioDisplayCommon'
import { studioAudioHistoryPageSize } from './studioAudioOptions'
import { formatAudioDuration, studioTtsParamsSummary } from './studioAudioDisplay'

/**
 * 音频历史列表（规划 F2.2）：页码/增量分页（useResponsivePagedList），
 * 条目 = 播放器 + 参数摘要 + 时长 + 删除。TTS 同步结果已入素材库，这里只读。
 */
const emit = defineEmits<{
  deleted: [asset: StudioAsset]
}>()

const deletingAssetId = ref<string>()

const {
  items,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile,
  refreshMobile,
  removeItems
} = useResponsivePagedList<StudioAsset>({
  pageSize: studioAudioHistoryPageSize,
  showTotal: (total) => `共 ${total} 条音频`,
  fetchPage: async (_options, pageState) => {
    const result = await studioApi.listAssets({
      limit: pageState.pageSize,
      offset: (pageState.current - 1) * pageState.pageSize,
      kind: 'audio'
    })
    return adaptStudioListPage(result, pageState)
  },
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载音频历史失败'))
  }
})

async function handleDelete(asset: StudioAsset): Promise<void> {
  deletingAssetId.value = asset.id
  try {
    await studioApi.deleteAsset(asset.id)
    removeItems((item) => item.id === asset.id)
    message.success('素材已删除')
    emit('deleted', asset)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '删除素材失败'))
  } finally {
    deletingAssetId.value = undefined
  }
}

onMounted(() => {
  void loadData()
})

defineExpose({
  /** TTS 合成成功后由父级调用：回到首页补一轮同步。 */
  reload: () => {
    pagination.current = 1
    return loadData()
  }
})
</script>

<template>
  <section class="audio-history">
    <h3 class="audio-history__title">合成历史（素材库音频）</h3>
    <a-spin :spinning="loading">
      <template v-if="items.length > 0">
        <div class="audio-history__grid">
          <div v-for="asset in items" :key="asset.id" class="audio-history__item">
            <audio controls preload="none" :src="studioAssetContentUrl(asset.id)" />
            <div class="audio-history__meta">
              <span>{{ formatStudioDateTime(asset.createdAt) }}</span>
              <span>{{ studioTtsParamsSummary(asset.params) }}</span>
              <span>{{ formatAudioDuration(asset.durationMs) }}</span>
            </div>
            <div class="audio-history__actions">
              <a :href="studioAssetContentUrl(asset.id, { download: true })" class="ant-btn-link ant-btn-sm">下载</a>
              <a-popconfirm title="删除该音频？删除后不可恢复。" @confirm="handleDelete(asset)">
                <a-button size="small" type="link" danger :loading="deletingAssetId === asset.id">删除</a-button>
              </a-popconfirm>
            </div>
          </div>
        </div>
        <div class="audio-history__pagination">
          <a-pagination
            :current="pagination.current"
            :page-size="pagination.pageSize"
            :total="pagination.total"
            :show-size-changer="false"
            :hide-on-single-page="false"
            @change="(page: number) => handleTableChange({ current: page })"
          />
        </div>
        <div v-if="mobileHasMore" class="audio-history__more">
          <a-button size="small" :loading="mobileLoadingMore" @click="loadMoreMobile">加载更多</a-button>
          <a-button size="small" type="link" @click="refreshMobile">刷新</a-button>
        </div>
      </template>
      <a-empty v-else-if="!loading" description="还没有音频，从上方合成第一段" />
    </a-spin>
  </section>
</template>

<style scoped>
.audio-history__title {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 8px;
}

.audio-history__grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 10px;
}

.audio-history__item {
  border: 1px solid var(--juhe-border-color, #e5e7eb);
  border-radius: 8px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  background: var(--juhe-bg-container, #fff);
}

.audio-history__item audio {
  max-width: 480px;
  width: 100%;
  height: 36px;
}

.audio-history__meta {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--juhe-text-secondary, #6b7280);
}

.audio-history__actions {
  display: flex;
  gap: 8px;
}

.audio-history__pagination {
  margin-top: 12px;
  text-align: right;
}

.audio-history__more {
  margin-top: 8px;
  display: flex;
  gap: 8px;
}
</style>
