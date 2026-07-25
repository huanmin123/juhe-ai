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
  </figure>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { chatAssetContentUrl } from '@/api/domains/chat'
import type { ChatMessageContentBlock } from '@/types/domain/chat'

const props = defineProps<{ conversationId: string; block: Extract<ChatMessageContentBlock, { type: 'output_image' }> }>()
const imageFailed = ref(false)
const previewUrl = computed(() => props.block.assetId ? chatAssetContentUrl(props.conversationId, props.block.assetId, 'preview') : '')
const originalUrl = computed(() => props.block.assetId ? chatAssetContentUrl(props.conversationId, props.block.assetId, 'original') : '')
watch([previewUrl, originalUrl], () => {
  imageFailed.value = false
})
</script>

<style scoped>
.chat-generated-image { max-width: min(100%, 640px); margin: 10px 0 2px; }
.chat-generated-image-preview { display: block; width: fit-content; max-width: 100%; }
.chat-generated-image :deep(.ant-image-img) { display: block; max-width: 100%; max-height: 560px; border: 1px solid #e4e7ec; border-radius: 6px; object-fit: contain; background: #f8fafc; cursor: zoom-in; }
.chat-generated-image-fallback { padding: 16px; color: #b42318; background: #fff7f6; border: 1px solid #ffd8d3; border-radius: 6px; }
</style>
