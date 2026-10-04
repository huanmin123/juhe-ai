<template>
  <figure class="chat-generated-audio">
    <audio v-if="sourceUrl" class="chat-generated-audio-player" :src="sourceUrl" controls preload="metadata" @error="audioFailed = true" />
    <div v-if="audioFailed" class="chat-generated-audio-fallback" role="status">音频加载失败</div>
    <figcaption v-if="sourceUrl && !audioFailed" class="chat-generated-audio-actions">
      <a-button type="text" size="small" :disabled="downloading" aria-label="下载生成音频" @click="download">
        <template #icon><DownloadOutlined /></template>
        下载音频
      </a-button>
    </figcaption>
  </figure>
</template>

<script setup lang="ts">
import { DownloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, ref, watch } from 'vue'
import { chatAssetContentUrl } from '@/api/domains/chat'
import type { ChatMessageContentBlock } from '@/types/domain/chat'
import { downloadGeneratedMedia } from './chatGeneratedMediaActions'

// output_audio 块播放器（问答音视频工具设计 §5）：音频全宽 + 下载，卡片样式沿
// ChatGeneratedImage 的圆角卡片口径。
const props = defineProps<{ conversationId: string; block: Extract<ChatMessageContentBlock, { type: 'output_audio' }> }>()
const audioFailed = ref(false)
const downloading = ref(false)
const sourceUrl = computed(() => props.block.assetId ? chatAssetContentUrl(props.conversationId, props.block.assetId, 'original') : '')
watch(sourceUrl, () => { audioFailed.value = false })

async function download(): Promise<void> {
  if (downloading.value || !props.block.assetId) return
  downloading.value = true
  try {
    await downloadGeneratedMedia(props.conversationId, props.block.assetId, 'audio')
  } catch {
    message.error('音频下载失败，请稍后重试')
  } finally {
    downloading.value = false
  }
}
</script>

<style scoped>
.chat-generated-audio { width: 100%; max-width: min(100%, 640px); margin: 10px 0 2px; }
.chat-generated-audio-player { display: block; width: 100%; height: 40px; border: 1px solid var(--juhe-border); border-radius: 6px; background: var(--juhe-surface-soft); }
.chat-generated-audio-fallback { padding: 12px 16px; color: var(--juhe-danger); background: var(--juhe-danger-soft); border: 1px solid var(--juhe-danger-line); border-radius: 6px; }
.chat-generated-audio-actions { display: flex; justify-content: flex-end; margin-top: 2px; }
.chat-generated-audio-actions :deep(.ant-btn) { color: var(--juhe-muted); }
.chat-generated-audio-actions :deep(.ant-btn:hover), .chat-generated-audio-actions :deep(.ant-btn:focus-visible) { color: var(--juhe-fg-soft); }
</style>
