<template>
  <div class="chat-process">
    <div v-for="tool in message.toolEvents ?? []" :key="tool.id" class="chat-process-row">
      <span class="chat-process-status" :class="`is-${tool.status}`" aria-hidden="true" />
      <details>
        <summary>{{ toolLabel(tool.type) }}<span>{{ statusLabel(tool.status) }}</span></summary>
        <pre v-if="tool.item">{{ compactItem(tool.item) }}</pre>
      </details>
    </div>
    <details v-if="message.reasoningText" class="chat-reasoning">
      <summary>思考过程</summary>
      <div>{{ message.reasoningText }}</div>
    </details>
  </div>
</template>

<script setup lang="ts">
import type { ChatMessage, ChatToolStatus } from '@/types/domain/chat'
defineProps<{ message: ChatMessage }>()
function toolLabel(type: string): string { return ({ web_search_call: '联网搜索', function_call: '函数调用', computer_call: '计算机操作' }[type] ?? '工具调用') }
function statusLabel(status: ChatToolStatus): string { return ({ started: '准备中', updated: '执行中', completed: '已完成', failed: '失败' })[status] }
function compactItem(item: Record<string, unknown>): string { return JSON.stringify(item, null, 2).slice(0, 4096) }
</script>

<style scoped>
.chat-process { margin-top: 8px; color: #64748b; font-size: 12px; }
.chat-process-row { display: flex; align-items: flex-start; gap: 6px; margin-top: 5px; }
.chat-process-status { width: 6px; height: 6px; flex: 0 0 6px; margin-top: 7px; border-radius: 50%; background: #94a3b8; }
.chat-process-status.is-started, .chat-process-status.is-updated { background: #1677ff; }
.chat-process-status.is-completed { background: #52c41a; }
.chat-process-status.is-failed { background: #ff4d4f; }
.chat-process details { min-width: 0; flex: 1; }
.chat-process summary, .chat-reasoning summary { cursor: pointer; color: #64748b; user-select: none; }
.chat-process summary span { margin-left: 8px; color: #94a3b8; }
.chat-process pre { max-height: 180px; margin: 5px 0 0; padding: 7px; overflow: auto; background: #f8fafc; border: 1px solid #eef2f7; border-radius: 5px; font-size: 11px; }
.chat-reasoning { margin-top: 7px; padding-top: 5px; border-top: 1px solid #f1f5f9; color: #8492a6; }
.chat-reasoning div { max-height: 180px; margin-top: 5px; padding: 6px 8px; overflow: auto; white-space: pre-wrap; background: #fafafa; border-left: 2px solid #dbe3ec; }
</style>
