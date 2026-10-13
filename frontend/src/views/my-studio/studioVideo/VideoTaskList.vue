<script setup lang="ts">
import { computed } from 'vue'

import { studioAssetContentUrl } from '@/api/domains/studio'
import type { StudioTask } from '@/types/domain'
import { formatStudioDateTime } from '../studioDisplayCommon'
import StudioTaskCard from '../shared/StudioTaskCard.vue'
import { studioTaskArtifactState } from '../shared/studioTaskState'
import { videoTaskArtifactLabel } from './studioVideoDisplay'

/**
 * 视频任务与作品列表（规划 F2.3）：进行中任务卡（含乐观卡）轮询 +
 * 已完成任务内联 video 播放器与下载；产物缺失按提示文案渲染，不报系统错误。
 */
const props = defineProps<{
  queueTasks: StudioTask[]
  historyTasks: StudioTask[]
  nowMs: number
  loading: boolean
  pagination: { current: number; pageSize: number; total: number }
  hasMore: boolean
  loadingMore: boolean
  cancellingTaskId?: string
}>()

const emit = defineEmits<{
  retry: [task: StudioTask]
  'cancel-task': [task: StudioTask]
  'page-change': [page: number]
  'load-more': []
}>()

const pendingTasks = computed(() => props.queueTasks.filter((task) =>
  task.id.startsWith('pending-') || studioTaskArtifactState(task) === 'pending'))

/** 历史区只展示终态行：进行中/未结算行由队列卡负责（settled 前不显示成品）。 */
const finishedHistoryTasks = computed(() => props.historyTasks.filter((task) =>
  !task.id.startsWith('pending-') && studioTaskArtifactState(task) !== 'pending'))

function artifactOf(task: StudioTask) {
  return studioTaskArtifactState(task)
}

function artifactTextOf(task: StudioTask): string {
  return videoTaskArtifactLabel(artifactOf(task))
}
</script>

<template>
  <section class="video-task-list">
    <h3 class="video-task-list__title">任务与作品</h3>

    <div v-if="pendingTasks.length > 0" class="video-task-list__queue">
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

    <a-spin :spinning="loading">
      <template v-if="finishedHistoryTasks.length > 0">
        <div class="video-task-list__rows">
          <div v-for="task in finishedHistoryTasks" :key="task.id" class="video-task-list__row">
            <template v-if="artifactOf(task) === 'ready' && task.assetId">
              <video controls preload="metadata" :src="studioAssetContentUrl(task.assetId)" class="video-task-list__player" />
              <div class="video-task-list__meta">
                <span>{{ task.model }}</span>
                <span :title="task.promptSummary">{{ task.promptSummary }}</span>
                <span>{{ formatStudioDateTime(task.createdAt, nowMs) }}</span>
                <a :href="studioAssetContentUrl(task.assetId, { download: true })" class="ant-btn-link ant-btn-sm">下载</a>
              </div>
            </template>
            <template v-else-if="artifactOf(task) === 'missing'">
              <div class="video-task-list__placeholder video-task-list__placeholder--missing">
                {{ artifactTextOf(task) }}：素材可能已被删除，可回填参数重新生成
              </div>
              <div class="video-task-list__meta">
                <span>{{ task.model }}</span>
                <span>{{ task.promptSummary }}</span>
                <a-button size="small" type="link" @click="emit('retry', task)">重新生成</a-button>
              </div>
            </template>
            <template v-else-if="artifactOf(task) === 'failed' || artifactOf(task) === 'stopped'">
              <div class="video-task-list__placeholder">{{ artifactTextOf(task) }}</div>
              <div class="video-task-list__meta">
                <span>{{ task.model }}</span>
                <span>{{ task.error?.message ?? task.error?.code ?? task.promptSummary }}</span>
                <a-button v-if="artifactOf(task) === 'failed'" size="small" type="link" @click="emit('retry', task)">重试</a-button>
              </div>
            </template>
          </div>
        </div>
        <div class="video-task-list__pagination">
          <a-pagination
            :current="pagination.current"
            :page-size="pagination.pageSize"
            :total="pagination.total"
            :show-size-changer="false"
            @change="(page: number) => emit('page-change', page)"
          />
        </div>
        <div v-if="hasMore" class="video-task-list__more">
          <a-button size="small" :loading="loadingMore" @click="emit('load-more')">加载更多</a-button>
        </div>
      </template>
      <a-empty v-else-if="!loading && finishedHistoryTasks.length === 0 && pendingTasks.length === 0" description="还没有视频任务，从上方表单发起第一单镜生成" />
    </a-spin>
  </section>
</template>

<style scoped>
.video-task-list__title {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 8px;
}

.video-task-list__queue {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 10px;
  margin-bottom: 16px;
}

.video-task-list__rows {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.video-task-list__row {
  border: 1px solid var(--juhe-border-color, #e5e7eb);
  border-radius: 8px;
  padding: 10px 12px;
  background: var(--juhe-bg-container, #fff);
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.video-task-list__player {
  width: 100%;
  max-width: 480px;
  border-radius: 6px;
  background: #000;
}

.video-task-list__meta {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--juhe-text-secondary, #6b7280);
}

.video-task-list__placeholder {
  font-size: 13px;
  color: var(--juhe-text-secondary, #6b7280);
}

.video-task-list__placeholder--missing {
  color: var(--juhe-warning-color, #d46b08);
}

.video-task-list__pagination {
  margin-top: 12px;
  text-align: right;
}

.video-task-list__more {
  margin-top: 8px;
}
</style>
