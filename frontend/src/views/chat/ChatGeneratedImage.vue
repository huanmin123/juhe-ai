<template>
  <figure class="chat-generated-image">
    <a-image
      v-if="originalUrl"
      class="chat-generated-image-preview"
      :src="previewUrl"
      :preview="{ src: originalUrl }"
      :alt="block.revisedPrompt || '生成图片'"
      loading="lazy"
      @error="imageFailed = true"
    />
    <div v-if="imageFailed" class="chat-generated-image-fallback" role="status">图片加载失败</div>
    <figcaption>
      <span>{{ block.status === 'started' ? '图片生成中' : block.status === 'completed' ? '已生成图片' : block.status === 'canceled' ? '图片生成已停止' : '图片生成失败' }}</span>
      <button
        v-if="block.status === 'completed' && originalUrl"
        type="button"
        class="chat-generated-image-download"
        :disabled="downloading"
        aria-label="下载生成图片"
        title="下载生成图片"
        @click="downloadOriginal"
      >
        <DownloadOutlined />
      </button>
    </figcaption>
  </figure>
</template>

<script setup lang="ts">
import { DownloadOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { computed, ref, watch } from 'vue'
import { chatAssetContentUrl } from '@/api/domains/chat'
import type { ChatMessageContentBlock } from '@/types/domain/chat'

const props = defineProps<{ conversationId: string; block: Extract<ChatMessageContentBlock, { type: 'output_image' }> }>()
const imageFailed = ref(false)
const downloading = ref(false)
const previewUrl = computed(() => props.block.assetId ? chatAssetContentUrl(props.conversationId, props.block.assetId, 'preview') : '')
const originalUrl = computed(() => props.block.assetId ? chatAssetContentUrl(props.conversationId, props.block.assetId, 'original') : '')
const downloadFilename = computed(() => {
  const mimeType = props.block.mimeType?.toLowerCase()
  const extension = mimeType === 'image/png' ? 'png' : mimeType === 'image/jpeg' ? 'jpg' : 'webp'
  return `generated-${props.block.assetId}.${extension}`
})
watch([previewUrl, originalUrl], () => {
  imageFailed.value = false
})

async function downloadOriginal(): Promise<void> {
  if (!originalUrl.value || downloading.value) return
  downloading.value = true
  let objectUrl = ''
  try {
    const response = await fetch(originalUrl.value, {
      credentials: 'same-origin',
      headers: { Accept: 'image/*,*/*' }
    })
    if (!response.ok) throw new Error(`download failed: ${response.status}`)
    const blob = await response.blob()
    objectUrl = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = objectUrl
    anchor.download = downloadFilename.value
    anchor.rel = 'noopener'
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
  } catch {
    message.error('图片下载失败，请稍后重试')
  } finally {
    if (objectUrl) URL.revokeObjectURL(objectUrl)
    downloading.value = false
  }
}
</script>

<style scoped>
.chat-generated-image { max-width: min(100%, 640px); margin: 10px 0 2px; }
.chat-generated-image-preview { display: block; width: fit-content; max-width: 100%; }
.chat-generated-image :deep(.ant-image-img) { display: block; max-width: 100%; max-height: 560px; border: 1px solid #e4e7ec; border-radius: 6px; object-fit: contain; background: #f8fafc; cursor: zoom-in; }
.chat-generated-image-fallback { padding: 16px; color: #b42318; background: #fff7f6; border: 1px solid #ffd8d3; border-radius: 6px; }
.chat-generated-image figcaption { display: flex; align-items: center; gap: 8px; margin-top: 5px; color: #7a8491; font-size: 12px; }
.chat-generated-image-download { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; color: #667085; background: transparent; border: 0; border-radius: 4px; cursor: pointer; }
.chat-generated-image-download:hover, .chat-generated-image-download:focus-visible { color: #182230; background: #eef2f6; }
.chat-generated-image-download:disabled { opacity: .55; cursor: wait; }
</style>
