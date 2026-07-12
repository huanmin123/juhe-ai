<template>
  <div ref="scrollElement" class="message-scroll" @scroll="handleScroll">
    <div v-if="loading" class="message-loading"><a-spin size="small" /><span>正在加载对话</span></div>
    <div v-else-if="!messages.length" class="message-empty">
      <MessageOutlined />
      <span>发送一条消息开始对话</span>
    </div>
    <div v-else class="message-virtual-space" :data-message-count="messages.length" :style="{ height: `${virtualizer.getTotalSize()}px` }">
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
        <div class="message-body" :class="messages[item.index].role === 'user' ? 'message-bubble-user' : 'message-bubble-assistant'">
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
const emit = defineEmits<{ (event: 'near-top'): void }>()
const scrollElement = ref<HTMLElement>()
const followLatest = ref(true)
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
function handleScroll(): void {
  const element = scrollElement.value
  if (!element) return
  followLatest.value = element.scrollHeight - element.scrollTop - element.clientHeight < 96
  if (element.scrollTop < 120) emit('near-top')
}
function captureScrollAnchor(): { offset: number; totalSize: number } {
  return { offset: scrollElement.value?.scrollTop ?? 0, totalSize: virtualizer.value.getTotalSize() }
}
async function restoreScrollAnchor(anchor: { offset: number; totalSize: number }): Promise<void> {
  await nextTick()
  virtualizer.value.measure()
  virtualizer.value.scrollToOffset(anchor.offset + Math.max(0, virtualizer.value.getTotalSize() - anchor.totalSize))
}
function statusLabel(status: ChatMessageStatus): string { return ({ streaming: '生成中', failed: '失败', canceled: '已停止', completed: '' })[status] }
function statusColor(status: ChatMessageStatus): string { return ({ streaming: 'processing', failed: 'error', canceled: 'default', completed: 'default' })[status] }

watch(() => [props.messages.length, props.messages.at(-1)?.contentText.length], () => { if (followLatest.value) scrollToBottom() })
defineExpose({ scrollToBottom, captureScrollAnchor, restoreScrollAnchor })
</script>

<style scoped>
.message-scroll { position: relative; flex: 1; min-height: 0; overflow-y: auto; background: #fff; scrollbar-gutter: stable; }
.message-virtual-space { position: relative; width: 100%; }
.message-row { position: absolute; top: 0; left: 0; width: 100%; display: flex; align-items: flex-start; gap: 10px; padding: 14px clamp(14px, 3vw, 36px); }
.message-row-user { justify-content: flex-end; background: #f8fafc; }
.message-row-assistant { justify-content: flex-start; }
.message-row-user .message-avatar { order: 2; }
.message-row-user .message-body { order: 1; }
.message-avatar { width: 32px; height: 32px; display: grid; place-items: center; color: #475569; background: #fff; border: 1px solid #dfe5ec; border-radius: 6px; }
.message-row-assistant .message-avatar { color: #1677ff; background: #eef6ff; border-color: #b9d7ff; }
.message-body { min-width: 0; max-width: min(78%, 860px); padding: 10px 13px; }
.message-bubble-user { background: #eaf3ff; border: 1px solid #c7ddff; border-radius: 12px 12px 3px 12px; }
.message-bubble-assistant { background: #fff; border: 1px solid #e5eaf0; border-radius: 3px 12px 12px 12px; }
.message-meta { min-height: 24px; display: flex; align-items: center; gap: 8px; margin-bottom: 4px; color: #475569; font-size: 12px; font-weight: 600; }
.message-meta :deep(.ant-tag) { margin: 0; }
.message-loading, .message-empty { height: 100%; min-height: 260px; display: flex; align-items: center; justify-content: center; gap: 10px; color: #64748b; }
.message-empty :deep(.anticon) { font-size: 24px; color: #94a3b8; }
@media (max-width: 720px) { .message-row { gap: 8px; padding: 12px; } .message-body { max-width: 84%; padding: 8px 10px; } .message-avatar { width: 28px; height: 28px; } }
</style>
