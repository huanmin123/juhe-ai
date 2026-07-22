<template>
  <div class="chat-assistant-content">
    <template v-for="entry in timelineEntries" :key="entry.key">
      <ChatMarkdown v-if="entry.kind === 'block' && entry.block.type === 'output_text'" :content="entry.block.text" />
      <details
        v-else-if="entry.kind === 'block' && entry.block.type === 'reasoning'"
        class="chat-process-block"
        :open="isReasoningExpanded(entry.block)"
      >
        <summary @click="rememberReasoningToggleIntent(entry.block, $event)">
          <span class="chat-process-status" :class="`is-${entry.block.status ?? 'started'}`" aria-hidden="true" />
          <span>思考 · {{ statusLabel(entry.block.status ?? 'started') }}</span>
        </summary>
        <div class="chat-process-details">{{ entry.block.text || '正在准备' }}</div>
      </details>
      <ChatToolEvent
        v-else-if="entry.kind === 'tools'"
        :message="entry.message"
      />
      <ChatGeneratedImage
        v-else-if="entry.kind === 'block' && entry.block.type === 'output_image'"
        :conversation-id="message.conversationId"
        :block="entry.block"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ChatMessage, ChatMessageContentBlock, ChatToolStatus } from '@/types/domain/chat'
import ChatGeneratedImage from './ChatGeneratedImage.vue'
import ChatMarkdown from './ChatMarkdown.vue'
import ChatToolEvent from './ChatToolEvent.vue'

const props = defineProps<{ message: ChatMessage }>()
const manuallyToggled = ref(new Map<string, boolean>())
type ToolBlock = Extract<ChatMessageContentBlock, { type: 'tool_call' }>
type TimelineEntry =
  | { kind: 'block'; key: string; block: Exclude<ChatMessageContentBlock, ToolBlock> }
  | { kind: 'tools'; key: string; message: ChatMessage }

const timelineEntries = computed<TimelineEntry[]>(() => {
  const blocks = [...(props.message.contentBlocks ?? [])]
    .sort((left, right) => Number(('order' in left ? left.order : 0) ?? 0) - Number(('order' in right ? right.order : 0) ?? 0))
  const entries: TimelineEntry[] = []
  for (let index = 0; index < blocks.length;) {
    const block = blocks[index]!
    if (block.type !== 'tool_call') {
      entries.push({ kind: 'block', key: blockKey(block), block })
      index += 1
      continue
    }
    const segment: ToolBlock[] = []
    while (index < blocks.length && blocks[index]?.type === 'tool_call') {
      segment.push(blocks[index] as ToolBlock)
      index += 1
    }
    entries.push({
      kind: 'tools',
      key: `${props.message.id}:tools:${segment.map((item) => item.blockId ?? item.callId ?? item.id ?? item.order ?? 0).join(':')}`,
      message: { ...props.message, reasoningText: '', toolEvents: [], contentBlocks: segment }
    })
  }
  return entries
})

function blockKey(block: ChatMessageContentBlock): string {
  return `${props.message.id}:${('blockId' in block && block.blockId) || `${block.type}:${'order' in block ? block.order : 0}`}`
}
function isReasoningExpanded(block: Extract<ChatMessageContentBlock, { type: 'reasoning' }>): boolean {
  const manual = manuallyToggled.value.get(blockKey(block))
  return manual ?? block.status === 'started'
}
function rememberReasoningToggleIntent(block: Extract<ChatMessageContentBlock, { type: 'reasoning' }>, event: MouseEvent): void {
  const details = (event.currentTarget as HTMLElement).parentElement as HTMLDetailsElement | null
  if (details) manuallyToggled.value.set(blockKey(block), !details.open)
}
function statusLabel(status: ChatToolStatus | 'started'): string {
  return ({ started: '执行中', updated: '执行中', completed: '已完成', failed: '失败', canceled: '已停止' }[status])
}
</script>

<style scoped>
.chat-assistant-content { min-width: 0; }
.chat-process-block { margin: 7px 0; color: #718096; font-size: 12px; }
.chat-process-block summary { display: flex; width: fit-content; max-width: 100%; align-items: center; gap: 7px; cursor: pointer; user-select: none; }
.chat-process-status { width: 6px; height: 6px; flex: 0 0 6px; border-radius: 50%; background: #94a3b8; }
.chat-process-status.is-started, .chat-process-status.is-updated { background: #1677ff; }
.chat-process-status.is-started, .chat-process-status.is-updated { animation: chat-process-pulse 1.4s ease-in-out infinite; }
.chat-process-status.is-completed { background: #52a447; }
.chat-process-status.is-failed, .chat-process-status.is-canceled { background: #d9534f; }
.chat-process-details { max-height: 220px; margin: 5px 0 0 13px; padding: 5px 9px; overflow: auto; white-space: pre-wrap; border-left: 2px solid #edf1f5; color: #7b8796; }
@keyframes chat-process-pulse { 0%, 100% { opacity: .45; } 50% { opacity: 1; box-shadow: 0 0 0 4px rgba(22, 119, 255, .12); } }
@media (prefers-reduced-motion: reduce) { .chat-process-status.is-started, .chat-process-status.is-updated { animation: none; } }
</style>
