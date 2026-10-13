<script setup lang="ts">
import { computed } from 'vue'

import { studioAssetContentUrl } from '@/api/domains/studio'
import type { StudioAsset, StudioTask } from '@/types/domain'
import StudioTaskCard from '../shared/StudioTaskCard.vue'
import { groupImageGalleryByDate, formatImageBytes, formatImageDimensions } from './studioImageDisplay'

/**
 * 图像画廊双面板（规划 F2.1）：上方进行中任务卡（含乐观卡），下方历史作品
 * 按日期分组网格；a-image 预览；悬浮操作：回填参数 / 下载 / 删除。
 */
const props = defineProps<{
  queueTasks: StudioTask[]
  assets: StudioAsset[]
  loading: boolean
  nowMs: number
  hasMore: boolean
  loadingMore: boolean
  deletingAssetId?: string
  cancellingTaskId?: string
}>()

const emit = defineEmits<{
  reuse: [asset: StudioAsset]
  delete: [asset: StudioAsset]
  retry: [task: StudioTask]
  'cancel-task': [task: StudioTask]
  'load-more': []
}>()

const groups = computed(() => groupImageGalleryByDate(props.assets, props.nowMs))
const pendingTasks = computed(() => props.queueTasks.filter((task) => task.status === 'queued' || task.status === 'in_progress' || task.id.startsWith('pending-')))
</script>

<template>
  <div class="studio-gallery">
    <section class="studio-gallery__queue">
      <h3 class="studio-gallery__title">生成队列</h3>
      <div v-if="pendingTasks.length > 0" class="studio-gallery__queue-grid">
        <StudioTaskCard
          v-for="task in pendingTasks"
          :key="task.id"
          :task="task"
          :now-ms="nowMs"
          :cancelling="cancellingTaskId === task.id"
          @retry="emit('retry', $event)"
          @cancel="emit('cancel-task', $event)"
        />
      </div>
      <a-empty v-else description="暂无进行中的任务，配置参数后点击生成" :image-style="{ height: '48px' }" />
    </section>

    <section class="studio-gallery__history">
      <h3 class="studio-gallery__title">作品画廊</h3>
      <a-spin :spinning="loading">
        <template v-if="groups.length > 0">
          <div v-for="group in groups" :key="group.dateLabel" class="studio-gallery__group">
            <div class="studio-gallery__date">{{ group.dateLabel }}</div>
            <div class="studio-gallery__grid">
              <div v-for="asset in group.assets" :key="asset.id" class="studio-gallery__item">
                <a-image
                  :src="studioAssetContentUrl(asset.id)"
                  :alt="asset.id"
                  :width="'100%'"
                  :height="160"
                  fit="cover"
                  :preview="true"
                />
                <div class="studio-gallery__meta">{{ formatImageDimensions(asset) }} · {{ formatImageBytes(asset) }}</div>
                <div class="studio-gallery__actions">
                  <a-button size="small" type="link" @click="emit('reuse', asset)">回填参数</a-button>
                  <a :href="studioAssetContentUrl(asset.id, { download: true })" class="ant-btn-link ant-btn-sm">下载</a>
                  <a-popconfirm title="删除该素材？删除后不可恢复。" @confirm="emit('delete', asset)">
                    <a-button size="small" type="link" danger :loading="deletingAssetId === asset.id">删除</a-button>
                  </a-popconfirm>
                </div>
              </div>
            </div>
          </div>
          <div v-if="hasMore" class="studio-gallery__more">
            <a-button size="small" :loading="loadingMore" @click="emit('load-more')">加载更多</a-button>
          </div>
        </template>
        <a-empty v-else-if="!loading" description="还没有作品，从左侧配置参数生成第一张" />
      </a-spin>
    </section>
  </div>
</template>

<style scoped>
.studio-gallery {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.studio-gallery__title {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 8px;
}

.studio-gallery__queue-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 10px;
}

.studio-gallery__group {
  margin-bottom: 16px;
}

.studio-gallery__date {
  font-size: 13px;
  color: var(--juhe-text-secondary, #6b7280);
  margin-bottom: 8px;
}

.studio-gallery__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 12px;
}

.studio-gallery__item {
  position: relative;
  border: 1px solid var(--juhe-border-color, #e5e7eb);
  border-radius: 8px;
  padding: 6px;
  background: var(--juhe-bg-container, #fff);
}

.studio-gallery__meta {
  font-size: 12px;
  color: var(--juhe-text-tertiary, #9ca3af);
  margin-top: 4px;
}

.studio-gallery__actions {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 4px;
}

.studio-gallery__more {
  text-align: center;
  margin-top: 8px;
}
</style>
