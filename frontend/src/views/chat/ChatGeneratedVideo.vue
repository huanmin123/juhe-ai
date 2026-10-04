<template>
  <figure class="chat-generated-video">
    <video v-if="sourceUrl" class="chat-generated-video-player" :src="sourceUrl" controls preload="metadata" playsinline @error="videoFailed = true" />
    <div v-if="videoFailed" class="chat-generated-video-fallback" role="status">视频加载失败</div>
    <figcaption v-if="sourceUrl && !videoFailed" class="chat-generated-video-actions">
      <a-button type="text" size="small" :disabled="downloading" aria-label="下载生成视频" @click="download">
        <template #icon><DownloadOutlined /></template>
        下载视频
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

// output_media_task 终态（completed + assetId）的播放器（问答音视频工具设计 §5）：
// 宽度撑满消息列上限，卡片样式沿 ChatGeneratedImage；src 走会话资产内容端点
// original 变体。
const props = defineProps<{ conversationId: string; block: Extract<ChatMessageContentBlock, { type: 'output_media_task' }> }>()
const videoFailed = ref(false)
const downloading = ref(false)
const sourceUrl = computed(() => props.block.assetId ? chatAssetContentUrl(props.conversationId, props.block.assetId, 'original') : '')
watch(sourceUrl, () => { videoFailed.value = false })

async function download(): Promise<void> {
  if (downloading.value || !props.block.assetId) return
  downloading.value = true
  try {
    await downloadGeneratedMedia(props.conversationId, props.block.assetId, 'video')
  } catch {
    message.error('视频下载失败，请稍后重试')
  } finally {
    downloading.value = false
  }
}
</script>

<style scoped>
.chat-generated-video { width: 100%; max-width: 100%; margin: 10px 0 2px; }
.chat-generated-video-player { display: block; width: 100%; max-height: 560px; border: 1px solid var(--juhe-border); border-radius: 6px; background: #000; }
.chat-generated-video-fallback { padding: 16px; color: var(--juhe-danger); background: var(--juhe-danger-soft); border: 1px solid var(--juhe-danger-line); border-radius: 6px; }
.chat-generated-video-actions { display: flex; justify-content: flex-end; margin-top: 2px; }
.chat-generated-video-actions :deep(.ant-btn) { color: var(--juhe-muted); }
.chat-generated-video-actions :deep(.ant-btn:hover), .chat-generated-video-actions :deep(.ant-btn:focus-visible) { color: var(--juhe-fg-soft); }
</style>
