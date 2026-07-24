<template>
  <div class="chat-process">
    <template v-for="tool in process.toolGroups" :key="tool.key">
      <details v-if="tool.summaries.length || tool.duplicateCount" class="chat-process-group" :open="isExpanded(tool)">
        <summary @click="rememberToggleIntent(tool, $event)">
          <span class="chat-process-status" :class="`is-${tool.status}`" aria-hidden="true" />
          <span>{{ toolLabel(tool.type) }} {{ statusLabel(tool.status) }}<template v-if="tool.callCount > 1"> · {{ tool.callCount }} 次</template></span>
        </summary>
        <div class="chat-process-details">
          <ul v-if="tool.summaries.length">
            <li v-for="summary in tool.summaries" :key="summary">{{ summary }}</li>
          </ul>
          <p v-if="tool.duplicateCount">相同条件重复 {{ tool.duplicateCount }} 次</p>
        </div>
      </details>
      <div v-else class="chat-process-group chat-process-summary-only">
        <span class="chat-process-status" :class="`is-${tool.status}`" aria-hidden="true" />
        <span>{{ toolLabel(tool.type) }} {{ statusLabel(tool.status) }}<template v-if="tool.callCount > 1"> · {{ tool.callCount }} 次</template></span>
      </div>
    </template>
    <details v-if="process.reasoningText" class="chat-reasoning">
      <summary>思考摘要</summary>
      <div>{{ process.reasoningText }}</div>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ChatMessage, ChatToolStatus } from '@/types/domain/chat'
import { projectChatMessageProcess, type ChatToolProcessGroup } from './chatMessageProcess'

const props = defineProps<{ message: ChatMessage }>()
const process = computed(() => projectChatMessageProcess(props.message))
const manuallyToggled = ref(new Map<string, boolean>())

function isActiveToolGroup(tool: ChatToolProcessGroup): boolean {
  return tool.status === 'started' || tool.status === 'updated' || tool.status === 'failed'
}
function isExpanded(tool: ChatToolProcessGroup): boolean {
  const manual = manuallyToggled.value.get(tool.key)
  return manual ?? isActiveToolGroup(tool)
}
function rememberToggleIntent(tool: ChatToolProcessGroup, event: MouseEvent): void {
  const details = (event.currentTarget as HTMLElement).parentElement as HTMLDetailsElement | null
  if (details) manuallyToggled.value.set(tool.key, !details.open)
}

function toolLabel(type: string): string {
  return ({ web_search_call: '联网搜索', image_generation: '图片生成', generate_image: '图片生成', file_search_call: '文件检索', function_call: '函数调用', computer_call: '计算机操作' }[type] ?? '工具调用')
}
function statusLabel(status: ChatToolStatus): string {
  return ({ started: '准备中', updated: '执行中', completed: '已完成', failed: '失败', canceled: '已停止' })[status]
}
</script>

<style scoped>
.chat-process { margin-top: 8px; color: #718096; font-size: 12px; }
.chat-process-group, .chat-reasoning { margin-top: 4px; }
.chat-process summary, .chat-process-summary-only { display: flex; width: fit-content; max-width: 100%; align-items: center; gap: 7px; color: #718096; user-select: none; }
.chat-process summary { cursor: pointer; }
.chat-process-status { width: 6px; height: 6px; flex: 0 0 6px; border-radius: 50%; background: #94a3b8; }
.chat-process-status.is-started, .chat-process-status.is-updated { background: #4b8fe8; }
.chat-process-status.is-started, .chat-process-status.is-updated { animation: chat-process-pulse 1.4s ease-in-out infinite; }
.chat-process-status.is-completed { background: #52a447; }
.chat-process-status.is-failed, .chat-process-status.is-canceled { background: #d9534f; }
.chat-process-details { max-height: 168px; margin: 5px 0 0 13px; padding-left: 9px; overflow: auto; border-left: 2px solid #edf1f5; color: #7b8796; }
.chat-process-details ul { margin: 0; padding-left: 17px; }
.chat-process-details li { margin: 2px 0; overflow-wrap: anywhere; }
.chat-process-details p { margin: 4px 0 0; color: #98a2b3; }
.chat-reasoning { color: #8995a5; }
.chat-reasoning summary { color: #8995a5; }
.chat-reasoning div { max-height: 168px; margin: 5px 0 0 13px; padding: 5px 9px; overflow: auto; white-space: pre-wrap; border-left: 2px solid #edf1f5; color: #7b8796; }
@keyframes chat-process-pulse { 0%, 100% { opacity: .45; } 50% { opacity: 1; box-shadow: 0 0 0 4px rgba(75, 143, 232, .12); } }
@media (prefers-reduced-motion: reduce) { .chat-process-status.is-started, .chat-process-status.is-updated { animation: none; } }
</style>
