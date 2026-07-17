<template>
  <div ref="scrollElement" class="message-scroll" tabindex="0" aria-label="对话消息" @scroll="handleScroll" @wheel.passive="handleWheel" @keydown="handleScrollKeyDown" @touchstart.passive="handleTouchStart" @touchmove.passive="handleTouchMove">
    <div v-if="loading" class="message-loading"><a-spin size="small" /><span>正在加载对话</span></div>
    <div v-else-if="!messages.length" class="message-empty">
      <MessageOutlined />
      <span>发送一条消息开始对话</span>
    </div>
    <div v-else ref="virtualSpace" class="message-virtual-space" :data-message-count="messages.length" :style="{ height: `${virtualizer.getTotalSize()}px` }">
      <article
        v-for="item in virtualItems"
        :key="messages[item.index].id"
        :ref="measureElement"
        :data-index="item.index"
        class="message-row"
        :class="[`message-row-${messages[item.index].role}`, { 'is-editing-turn': messages[item.index].turnId === editingTurnId }]"
        :style="{ transform: `translateY(${item.start}px)` }"
      >
        <div class="message-body">
          <div :class="messages[item.index].role === 'user' ? 'message-bubble-user' : 'message-bubble-assistant'">
            <ChatUserMessageContent v-if="messages[item.index].role === 'user'" :message="messages[item.index]" />
            <ChatMarkdown v-else :content="messages[item.index].contentText" />
            <ChatToolEvent v-if="messages[item.index].role === 'assistant' && (messages[item.index].toolEvents?.length || messages[item.index].reasoningText || messages[item.index].contentBlocks?.length)" :message="messages[item.index]" />
            <div v-if="messages[item.index].status !== 'completed'" class="message-status-text" role="status">
              {{ statusLabel(messages[item.index].status) }}
            </div>
          </div>
          <div v-if="messages[item.index].role === 'user'" class="message-actions">
            <div class="message-actions-controls">
              <time :datetime="messages[item.index].createdAt">{{ formatMessageTime(messages[item.index].createdAt) }}</time>
              <a-tooltip title="复制消息">
                <a-button type="text" class="message-action-button" aria-label="复制这条消息" @click="copyMessage(messages[item.index].contentText)">
                  <template #icon><CopyOutlined /></template>
                </a-button>
              </a-tooltip>
              <a-tooltip v-if="messages[item.index].id === editableMessageId" title="编辑并重新生成">
                <a-button type="text" class="message-action-button" aria-label="编辑这条消息" @click="emit('edit-message', messages[item.index])">
                  <template #icon><EditOutlined /></template>
                </a-button>
              </a-tooltip>
            </div>
          </div>
          <div v-if="messages[item.index].role === 'assistant'" class="message-actions message-actions-assistant">
            <div class="message-actions-controls">
              <time :datetime="messages[item.index].createdAt">{{ formatMessageTime(messages[item.index].createdAt) }}</time>
              <a-tooltip title="复制回答">
                <a-button type="text" class="message-action-button" aria-label="复制 AI 回答" @click="copyMessage(messages[item.index].contentText)">
                  <template #icon><CopyOutlined /></template>
                </a-button>
              </a-tooltip>
            </div>
          </div>
        </div>
      </article>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CopyOutlined, EditOutlined, MessageOutlined } from '@ant-design/icons-vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { message as antdMessage } from 'ant-design-vue'
import dayjs from 'dayjs'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { writeTextToClipboard } from '@/shared/clipboard'
import type { ChatMessage, ChatMessageStatus } from '@/types/domain/chat'
import ChatMarkdown from './ChatMarkdown.vue'
import ChatToolEvent from './ChatToolEvent.vue'
import ChatUserMessageContent from './ChatUserMessageContent.vue'
import { chatDistanceFromBottom, resolveChatFollowState, resolveChatViewportResizeTransition, shouldBreakChatFollowOnWheel, shouldDetachChatFollowOnScroll, shouldShowChatJumpButton } from './chatScrollPolicy'

const props = defineProps<{ messages: ChatMessage[]; loading: boolean; editableMessageId?: string; editingTurnId?: string }>()
const emit = defineEmits<{ (event: 'near-top'): void; (event: 'jump-visibility', visible: boolean): void; (event: 'edit-message', message: ChatMessage): void }>()
const scrollElement = ref<HTMLElement>()
const virtualSpace = ref<HTMLElement>()
const followLatest = ref(true)
let lastJumpVisible = false
let touchStartY = 0
let userDetachedFromBottom = false
let previousScrollTop = 0
let programmaticScrollUntil = 0
let resizeObserver: ResizeObserver | undefined
let previousClientHeight = 0
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
function scrollToBottom(): void { userDetachedFromBottom = false; followLatest.value = true; emitJumpVisibility(false); followStream() }
function followStream(): void {
  if (!followLatest.value) return
  programmaticScrollUntil = Date.now() + 250
  nextTick(() => virtualizer.value.scrollToIndex(Math.max(0, props.messages.length - 1), { align: 'end' }))
}
function handleScroll(): void {
  const element = scrollElement.value
  if (!element) return
  const viewportChanged = previousClientHeight > 0 && previousClientHeight !== element.clientHeight
  if (!viewportChanged && shouldDetachChatFollowOnScroll({ previousOffset: previousScrollTop, currentOffset: element.scrollTop, now: Date.now(), programmaticScrollUntil })) {
    userDetachedFromBottom = true
    followLatest.value = false
  }
  previousScrollTop = element.scrollTop
  const distance = chatDistanceFromBottom(element)
  if (viewportChanged && !userDetachedFromBottom) {
    const transition = resolveChatViewportResizeTransition({ followLatest: followLatest.value, userDetached: userDetachedFromBottom })
    followLatest.value = transition.followLatest
    userDetachedFromBottom = transition.userDetached
  } else {
    const next = resolveChatFollowState({ distance, userDetached: userDetachedFromBottom })
    followLatest.value = next.followLatest
    userDetachedFromBottom = next.userDetached
  }
  emitJumpVisibility(shouldShowChatJumpButton(distance))
  if (element.scrollTop < 120) emit('near-top')
}
function handleWheel(event: WheelEvent): void { if (shouldBreakChatFollowOnWheel(event.deltaY)) { userDetachedFromBottom = true; followLatest.value = false } }
function handleScrollKeyDown(event: KeyboardEvent): void { if (event.key === 'ArrowUp' || event.key === 'PageUp' || event.key === 'Home') { userDetachedFromBottom = true; followLatest.value = false } }
function handleTouchStart(event: TouchEvent): void { touchStartY = event.touches[0]?.clientY ?? 0 }
function handleTouchMove(event: TouchEvent): void { const current = event.touches[0]?.clientY ?? touchStartY; if (current > touchStartY + 4) { userDetachedFromBottom = true; followLatest.value = false }; touchStartY = current }
function emitJumpVisibility(visible: boolean): void { if (visible === lastJumpVisible) return; lastJumpVisible = visible; emit('jump-visibility', visible) }
function captureScrollAnchor(): { offset: number; totalSize: number } {
  return { offset: scrollElement.value?.scrollTop ?? 0, totalSize: virtualizer.value.getTotalSize() }
}
async function restoreScrollAnchor(anchor: { offset: number; totalSize: number }): Promise<void> {
  await nextTick()
  programmaticScrollUntil = Date.now() + 250
  virtualizer.value.measure()
  virtualizer.value.scrollToOffset(anchor.offset + Math.max(0, virtualizer.value.getTotalSize() - anchor.totalSize))
}
function statusLabel(status: ChatMessageStatus): string {
  return ({ streaming: '正在生成', failed: '生成失败', canceled: '已停止', completed: '' })[status]
}
function formatMessageTime(value: string): string { return dayjs(value).format('HH:mm') }
async function copyMessage(content: string): Promise<void> {
  try { await writeTextToClipboard(content) } catch { antdMessage.error('复制失败，请稍后重试') }
}

watch(() => [props.messages.length, props.messages.at(-1)?.contentText.length, props.messages.at(-1)?.toolEvents?.length, props.messages.at(-1)?.reasoningText?.length], followStream)
function handleObservedResize(entries: ResizeObserverEntry[]): void {
  for (const entry of entries) {
    if (entry.target !== scrollElement.value) {
      followStream()
      continue
    }
    const transition = resolveChatViewportResizeTransition({ followLatest: followLatest.value, userDetached: userDetachedFromBottom })
    followLatest.value = transition.followLatest
    userDetachedFromBottom = transition.userDetached
    previousClientHeight = scrollElement.value?.clientHeight ?? 0
    if (transition.shouldScroll) followStream()
  }
}
onMounted(() => {
  resizeObserver = new ResizeObserver(handleObservedResize)
  previousClientHeight = scrollElement.value?.clientHeight ?? 0
  if (scrollElement.value) resizeObserver.observe(scrollElement.value)
  if (virtualSpace.value) resizeObserver.observe(virtualSpace.value)
})
watch(virtualSpace, (next, previous) => { if (previous) resizeObserver?.unobserve(previous); if (next) resizeObserver?.observe(next) })
onBeforeUnmount(() => resizeObserver?.disconnect())
defineExpose({ scrollToBottom, followStream, captureScrollAnchor, restoreScrollAnchor })
</script>

<style scoped>
.message-scroll { position: relative; flex: 1; min-height: 0; overflow-y: auto; outline: none; background: #fff; scrollbar-gutter: stable; }
.message-virtual-space { position: relative; width: 100%; }
.message-row { position: absolute; top: 0; left: 0; width: 100%; display: flex; align-items: flex-start; padding: 14px clamp(14px, 3vw, 36px); }
.message-row-user { justify-content: flex-end; }
.message-row-assistant { justify-content: flex-start; }
.message-row.is-editing-turn { opacity: .42; }
.message-body { min-width: 0; }
.message-row-user .message-body { max-width: min(78%, 720px); display: flex; flex-direction: column; align-items: flex-end; }
.message-row-assistant .message-body { width: min(100%, 960px); max-width: min(100%, 960px); }
.message-bubble-user { width: fit-content; max-width: 100%; padding: 9px 13px; background: #f5f5f5; border-radius: 10px 10px 3px 10px; }
.message-bubble-assistant { padding: 4px 0; background: transparent; border: 0; box-shadow: none; }
.message-status-text { margin-top: 6px; color: #98a2b3; font-size: 12px; }
.message-actions { min-height: 32px; display: flex; justify-content: flex-end; }
.message-actions-assistant { justify-content: flex-start; }
.message-actions-controls { min-height: 32px; display: flex; align-items: center; gap: 2px; color: #98a2b3; font-size: 11px; opacity: 0; pointer-events: none; transition: opacity .12s ease; }
.message-row-user:hover .message-actions-controls,
.message-row-user:focus-within .message-actions-controls,
.message-row-assistant:hover .message-actions-controls,
.message-row-assistant:focus-within .message-actions-controls { opacity: 1; pointer-events: auto; }
.message-action-button { min-width: 32px; min-height: 32px; padding: 0; color: #8b95a3; }
.message-action-button:hover, .message-action-button:focus-visible { color: #344054; }
.message-loading, .message-empty { height: 100%; min-height: 260px; display: flex; align-items: center; justify-content: center; gap: 10px; color: #64748b; }
.message-empty :deep(.anticon) { font-size: 24px; color: #94a3b8; }
@media (hover: none), (pointer: coarse) {
  .message-actions-controls { opacity: 1; pointer-events: auto; }
}
@media (pointer: coarse) {
  .message-action-button { min-width: 44px; min-height: 44px; }
  .message-actions, .message-actions-controls { min-height: 44px; }
}
@media (max-width: 720px) {
  .message-row { padding: 12px; }
  .message-row[data-index="0"] { padding-top: calc(64px + env(safe-area-inset-top)); }
  .message-row-user .message-body { max-width: 88%; }
  .message-bubble-user { padding: 8px 11px; }
  .message-loading, .message-empty { min-height: 0; padding-top: calc(52px + env(safe-area-inset-top)); }
}
</style>
