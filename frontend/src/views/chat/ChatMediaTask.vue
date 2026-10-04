<template>
  <div class="chat-media-task" :class="`is-${block.status}`" role="status">
    <div class="chat-media-task-head">
      <VideoCameraOutlined class="chat-media-task-icon" aria-hidden="true" />
      <span class="chat-media-task-title">视频生成</span>
      <a-tag class="chat-media-task-tag" :color="statusColor">{{ statusText }}</a-tag>
      <span v-if="block.model" class="chat-media-task-model">{{ block.model }}</span>
    </div>
    <p v-if="block.promptSummary" class="chat-media-task-prompt" :title="block.promptSummary">{{ block.promptSummary }}</p>
    <a-progress
      v-if="block.status === 'in_progress' && progressPercent !== undefined"
      class="chat-media-task-progress"
      :percent="progressPercent"
      size="small"
      :show-info="false"
      status="active"
    />
    <p v-if="block.status === 'failed' && block.error" class="chat-media-task-error">{{ block.error }}</p>
    <p v-if="!isTerminal" class="chat-media-task-hint">后台生成中，完成后自动展示</p>
  </div>
</template>

<script setup lang="ts">
import { VideoCameraOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'
import type { ChatMessageContentBlock, ChatMediaTaskStatus } from '@/types/domain/chat'
import { isTerminalChatMediaTaskStatus } from '@/types/domain/chat'

// output_media_task 未终态任务卡（问答音视频工具设计 §5）：排队/生成中/失败态
// 卡片；completed + assetId 由上层替换为 ChatGeneratedVideo 播放器。状态 Tag 用
// 色沿项目语义 Tag 习惯（default/processing/success/error，global.css 有浅色覆盖）。
const props = defineProps<{ block: Extract<ChatMessageContentBlock, { type: 'output_media_task' }> }>()

const isTerminal = computed(() => isTerminalChatMediaTaskStatus(props.block.status))
const statusText = computed(() => ({ queued: '排队中', in_progress: '生成中', completed: '已完成', failed: '生成失败' }[props.block.status]))
const statusColor = computed(() => ({ queued: 'default', in_progress: 'processing', completed: 'success', failed: 'error' }[props.block.status]))
const progressPercent = computed(() => {
  const value = props.block.progress
  return typeof value === 'number' && Number.isFinite(value) ? Math.min(100, Math.max(0, Math.round(value))) : undefined
})
</script>

<style scoped>
.chat-media-task { max-width: min(100%, 640px); margin: 10px 0 2px; padding: 10px 12px; border: 1px solid var(--juhe-border); border-radius: 6px; background: var(--juhe-surface-soft); }
.chat-media-task.is-failed { border-color: var(--juhe-danger-line); background: var(--juhe-danger-soft); }
.chat-media-task-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.chat-media-task-icon { flex: 0 0 auto; color: var(--juhe-muted); }
.chat-media-task-title { flex: 0 0 auto; color: var(--juhe-fg-soft); font-size: 13px; font-weight: 600; }
.chat-media-task-tag { flex: 0 0 auto; margin-inline-end: 0; }
.chat-media-task-model { min-width: 0; margin-left: auto; overflow: hidden; color: var(--juhe-faint); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.chat-media-task-prompt { margin: 6px 0 0; overflow: hidden; color: var(--juhe-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.chat-media-task-progress { margin: 8px 0 0; }
.chat-media-task-progress :deep(.ant-progress-outer) { margin: 0; }
.chat-media-task-error { margin: 6px 0 0; color: var(--juhe-danger); font-size: 12px; overflow-wrap: anywhere; }
.chat-media-task-hint { margin: 6px 0 0; color: var(--juhe-faint); font-size: 12px; }
</style>
