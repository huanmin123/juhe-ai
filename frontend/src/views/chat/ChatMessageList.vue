<template>
  <div ref="scrollElement" class="message-scroll" @scroll="emit('scroll')">
    <div v-if="loading" class="message-loading"><a-spin size="small" /><span>正在加载对话</span></div>
    <div v-else-if="!messages.length" class="message-empty">
      <MessageOutlined />
      <span>发送一条消息开始对话</span>
    </div>
    <div v-else class="message-virtual-space" :style="{ height: `${virtualizer.getTotalSize()}px` }">
      <article
        v-for="item in virtualItems"
        :key="messages[item.index].id"
        :ref="measureElement"
        :data-index="item.index"
        class="message-row"
        :class="`message-row-${messages[item.index].role}`"
        :style="{ transform: `translateY(${item.start}px)` }"
      >
        <div class="message-avatar" aria-hidden="true">
          <UserOutlined v-if="messages[item.index].role === 'user'" />
          <RobotOutlined v-else />
        </div>
        <div class="message-body">
          <div class="message-meta">
            <span>{{ messages[item.index].role === 'user' ? '我' : messages[item.index].model }}</span>
            <a-tag v-if="messages[item.index].status !== 'completed'" :color="statusColor(messages[item.index].status)">{{ statusLabel(messages[item.index].status) }}</a-tag>
          </div>
          <ChatMarkdown :content="messages[item.index].contentText || (messages[item.index].status === 'streaming' ? '正在思考…' : '')" />
        </div>
      </article>
    </div>
  </div>
</template>

<script setup lang="ts">
import { MessageOutlined, RobotOutlined, UserOutlined } from '@ant-design/icons-vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { computed, nextTick, ref, watch } from 'vue'
import type { ChatMessage, ChatMessageStatus } from '@/types/domain/chat'
import ChatMarkdown from './ChatMarkdown.vue'

const props = defineProps<{ messages: ChatMessage[]; loading: boolean }>()
const emit = defineEmits<{ (event: 'scroll'): void }>()
const scrollElement = ref<HTMLElement>()
const virtualizer = useVirtualizer(computed(() => ({
  count: props.messages.length,
  getScrollElement: () => scrollElement.value ?? null,
  estimateSize: () => 104,
  overscan: 6,
  getItemKey: (index: number) => props.messages[index]?.id ?? index
})))
const virtualItems = computed(() => virtualizer.value.getVirtualItems())

function measureElement(element: unknown): void {
  if (element instanceof Element) virtualizer.value.measureElement(element)
}
function scrollToBottom(): void { nextTick(() => virtualizer.value.scrollToIndex(Math.max(0, props.messages.length - 1), { align: 'end' })) }
function statusLabel(status: ChatMessageStatus): string { return ({ streaming: '生成中', failed: '失败', canceled: '已停止', completed: '' })[status] }
function statusColor(status: ChatMessageStatus): string { return ({ streaming: 'processing', failed: 'error', canceled: 'default', completed: 'default' })[status] }

watch(() => [props.messages.length, props.messages.at(-1)?.contentText.length], scrollToBottom)
defineExpose({ scrollToBottom })
</script>

<style scoped>
.message-scroll { position: relative; flex: 1; min-height: 0; overflow-y: auto; background: #fff; scrollbar-gutter: stable; }
.message-virtual-space { position: relative; width: 100%; }
.message-row { position: absolute; top: 0; left: 0; width: 100%; display: grid; grid-template-columns: 32px minmax(0, 1fr); gap: 12px; padding: 14px clamp(14px, 3vw, 36px); }
.message-row-user { background: #f8fafc; }
.message-avatar { width: 32px; height: 32px; display: grid; place-items: center; color: #475569; background: #fff; border: 1px solid #dfe5ec; border-radius: 6px; }
.message-row-assistant .message-avatar { color: #1677ff; background: #eef6ff; border-color: #b9d7ff; }
.message-body { min-width: 0; padding-top: 3px; }
.message-meta { min-height: 24px; display: flex; align-items: center; gap: 8px; margin-bottom: 4px; color: #475569; font-size: 12px; font-weight: 600; }
.message-meta :deep(.ant-tag) { margin: 0; }
.message-loading, .message-empty { height: 100%; min-height: 260px; display: flex; align-items: center; justify-content: center; gap: 10px; color: #64748b; }
.message-empty :deep(.anticon) { font-size: 24px; color: #94a3b8; }
@media (max-width: 720px) { .message-row { grid-template-columns: 28px minmax(0, 1fr); gap: 9px; padding: 12px; } .message-avatar { width: 28px; height: 28px; } }
</style>
