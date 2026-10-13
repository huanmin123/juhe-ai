<script setup lang="ts">
import { computed } from 'vue'

import type { StudioTask } from '@/types/domain'
import { studioTaskKindLabels, studioTaskStatusColors, studioTaskStatusLabels } from '@/types/domain'
import { formatStudioTaskElapsed } from '../studioDisplayCommon'
import { studioTaskArtifactState } from './studioTaskState'

/**
 * 工作室统一任务卡（规划 F2 通用组件）：状态 Tag、模型、提示词摘要、已耗时、
 * 失败原因（后端已脱敏）。仅明确失败终态展示"重试"（换新 clientOperationId
 * 新建任务由父级负责）；queued 态提供取消入口（本地取消不调上游）。
 */
const props = defineProps<{
  task: StudioTask
  nowMs: number
  cancelling?: boolean
}>()

const emit = defineEmits<{
  retry: [task: StudioTask]
  cancel: [task: StudioTask]
}>()

const statusLabel = computed(() => studioTaskStatusLabels[props.task.status] ?? props.task.status)
const statusColor = computed(() => studioTaskStatusColors[props.task.status] ?? 'default')
const elapsedText = computed(() => formatStudioTaskElapsed(props.task.createdAt, props.nowMs))
const artifact = computed(() => studioTaskArtifactState(props.task))
const errorMessage = computed(() => props.task.error?.message || props.task.error?.code || '')
const canRetry = computed(() => props.task.status === 'failed' && Boolean(props.task.error))
const canCancel = computed(() => props.task.status === 'queued' || props.task.status === 'in_progress')
</script>

<template>
  <div class="studio-task-card" :class="{ 'studio-task-card--failed': task.status === 'failed' }">
    <div class="studio-task-card__head">
      <a-tag :color="statusColor">{{ statusLabel }}</a-tag>
      <span class="studio-task-card__kind">{{ studioTaskKindLabels[task.kind] ?? task.kind }}</span>
      <span class="studio-task-card__model">{{ task.model }}</span>
      <span v-if="task.providerCode" class="studio-task-card__provider">{{ task.providerCode }}</span>
      <span class="studio-task-card__elapsed">已耗时 {{ elapsedText }}</span>
    </div>
    <div class="studio-task-card__prompt" :title="task.promptSummary">{{ task.promptSummary }}</div>
    <div v-if="errorMessage" class="studio-task-card__error">{{ errorMessage }}</div>
    <div v-else-if="artifact === 'missing'" class="studio-task-card__missing">产物缺失：素材可能已被删除，可回填参数重新生成</div>
    <div class="studio-task-card__actions">
      <a-button
        v-if="canRetry"
        size="small"
        type="primary"
        danger
        @click="emit('retry', task)"
      >重试</a-button>
      <a-popconfirm
        v-if="canCancel"
        title="取消该任务？已受理任务会尽力取消上游，本地不再等待结果。"
        @confirm="emit('cancel', task)"
      >
        <a-button size="small" :loading="cancelling">取消</a-button>
      </a-popconfirm>
    </div>
  </div>
</template>

<style scoped>
.studio-task-card {
  border: 1px solid var(--juhe-border-color, #e5e7eb);
  border-radius: 8px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  background: var(--juhe-bg-container, #fff);
}

.studio-task-card--failed {
  border-color: var(--juhe-error-border-color, #ffa39e);
}

.studio-task-card__head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.studio-task-card__kind,
.studio-task-card__model,
.studio-task-card__provider {
  font-size: 12px;
  color: var(--juhe-text-secondary, #6b7280);
}

.studio-task-card__elapsed {
  margin-left: auto;
  font-size: 12px;
  color: var(--juhe-text-tertiary, #9ca3af);
}

.studio-task-card__prompt {
  font-size: 13px;
  color: var(--juhe-text-color, #374151);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.studio-task-card__error {
  font-size: 12px;
  color: var(--juhe-error-color, #cf1322);
}

.studio-task-card__missing {
  font-size: 12px;
  color: var(--juhe-warning-color, #d46b08);
}

.studio-task-card__actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
</style>
