<template>
  <div v-if="inputBlocks.length" class="chat-user-content">
    <template v-for="block in inputBlocks" :key="`${block.type}-${block.order}`">
      <ChatMarkdown v-if="block.type === 'input_text'" :content="block.text" />
      <img
        v-else
        class="chat-user-image"
        :src="chatAssetContentUrl(message.conversationId, block.assetId)"
        alt="用户上传的图片"
        loading="lazy"
      />
    </template>
  </div>
  <ChatMarkdown v-else :content="message.contentText" />
</template>

<script setup lang="ts">
import { computed } from 'vue'

import { chatAssetContentUrl } from '@/api/domains/chat'
import type { ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'
import ChatMarkdown from './ChatMarkdown.vue'

const props = defineProps<{ message: ChatMessage }>()

const inputBlocks = computed(() => (props.message.contentBlocks ?? [])
  .filter((block): block is Extract<ChatMessageContentBlock, { type: 'input_text' | 'input_image' }> => block.type === 'input_text' || block.type === 'input_image')
  .sort((left, right) => left.order - right.order))
</script>

<style scoped>
.chat-user-content { display: flex; min-width: 0; flex-direction: column; gap: 7px; }
.chat-user-image { width: auto; max-width: min(360px, 100%); max-height: 320px; display: block; object-fit: contain; border: 1px solid #e1e5ea; border-radius: 6px; }
</style>
