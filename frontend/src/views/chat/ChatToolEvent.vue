<template>
  <div ref="processRoot" class="chat-process">
    <template v-for="tool in process.toolGroups" :key="tool.key">
      <details v-if="tool.summaries.length || tool.duplicateCount || tool.progress" class="chat-process-group" :open="isExpanded(tool)">
        <summary @click.prevent="toggleExpanded(tool)">
          <span class="chat-process-status" :class="`is-${tool.status}`" aria-hidden="true" />
          <span>{{ toolLabel(tool.type) }} {{ statusLabel(tool.status, tool.type) }}<template v-if="tool.statusDetail"> · {{ tool.statusDetail }}</template><template v-if="tool.callCount > 1"> · {{ tool.callCount }} 次</template><template v-if="terminalStageTimingsLabel(tool)"> · {{ terminalStageTimingsLabel(tool) }}</template></span>
        </summary>
        <div class="chat-process-details" :class="{ 'is-streaming': isActiveToolGroup(tool) }">
          <div v-if="tool.progress" class="chat-subagent">
            <p class="chat-subagent-stage">{{ progressStageLabel(tool) }}<template v-if="isRunningToolGroup(tool)"> · 已等待 {{ waitedSeconds }}s</template></p>
            <ChatMarkdown v-if="tool.progress.reasoning" class="chat-subagent-reasoning" :content="tool.progress.reasoning" />
            <ul v-if="tool.progress.actions?.length" class="chat-subagent-actions">
              <li v-for="action in tool.progress.actions" :key="action">{{ action }}</li>
            </ul>
            <p v-if="tool.progress.answer" class="chat-subagent-answer">{{ tool.progress.answer }}</p>
          </div>
          <ul v-if="tool.summaries.length">
            <li v-for="summary in sourceSummariesOf(tool).texts" :key="summary">
              <a v-if="isSourceSummaryLink(summary)" :href="summary" target="_blank" rel="noopener noreferrer">{{ summary }}</a>
              <template v-else>{{ summary }}</template>
            </li>
            <li v-for="domain in sourceSummariesOf(tool).domains" :key="domain.host">
              <button type="button" class="chat-source-domain" :aria-expanded="isSourceDomainExpanded(tool, domain)" @click="toggleSourceDomain(tool, domain)">
                {{ domain.host }}<template v-if="domain.count > 1"> ×{{ domain.count }}</template>
              </button>
              <ul v-if="isSourceDomainExpanded(tool, domain)" class="chat-source-domain-urls">
                <li v-for="url in domain.urls" :key="url">
                  <a :href="url" target="_blank" rel="noopener noreferrer">{{ url }}</a>
                </li>
              </ul>
            </li>
          </ul>
          <p v-if="tool.duplicateCount">相同条件重复 {{ tool.duplicateCount }} 次</p>
        </div>
      </details>
      <div v-else class="chat-process-group chat-process-summary-only">
        <span class="chat-process-status" :class="`is-${tool.status}`" aria-hidden="true" />
        <span>{{ toolLabel(tool.type) }} {{ statusLabel(tool.status, tool.type) }}<template v-if="tool.statusDetail"> · {{ tool.statusDetail }}</template><template v-if="tool.callCount > 1"> · {{ tool.callCount }} 次</template></span>
      </div>
    </template>
    <details v-if="process.reasoningText" class="chat-reasoning">
      <summary>思考摘要</summary>
      <div><ChatMarkdown class="chat-reasoning-body" :content="process.reasoningText" /></div>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import type { ChatMessage, ChatToolStatus } from '@/types/domain/chat'
import { serverDateTimeTimestamp } from '@/shared/formatters'
import ChatMarkdown from './ChatMarkdown.vue'
import { aggregateSourceSummaries, isSourceSummaryLink, projectChatMessageProcess, type ChatSourceDomainGroup, type ChatSourceSummaries, type ChatToolProgress, type ChatToolProcessGroup } from './chatMessageProcess'

const props = defineProps<{ message: ChatMessage }>()
const process = computed(() => projectChatMessageProcess(props.message))
const processRoot = ref<HTMLElement>()
// 手动折叠意图（契约 §10.4）：Map 存于 ref，Vue 3 对集合代理后 set/delete 触发重渲染；
// details 的 :open 完全受控于 isExpanded，click.prevent 接管原生 toggle，消除流式重渲染竞态。
const manuallyToggled = ref(new Map<string, boolean>())
const expandedSourceDomains = ref(new Set<string>())

function isActiveToolGroup(tool: ChatToolProcessGroup): boolean {
  return tool.status === 'started' || tool.status === 'updated' || tool.status === 'failed'
}
function isRunningToolGroup(tool: ChatToolProcessGroup): boolean {
  return tool.status === 'started' || tool.status === 'updated'
}
function isTerminalToolGroup(tool: ChatToolProcessGroup): boolean {
  return tool.status === 'completed' || tool.status === 'failed' || tool.status === 'canceled'
}
function isExpanded(tool: ChatToolProcessGroup): boolean {
  const manual = manuallyToggled.value.get(tool.key)
  return manual ?? isActiveToolGroup(tool)
}
function toggleExpanded(tool: ChatToolProcessGroup): void {
  manuallyToggled.value.set(tool.key, !isExpanded(tool))
}
// 子代理过程区阶段提示（契约 §10.3）：执行中按阶段展示；终态/历史回看为
// 持久化快照，用中性标题，避免完成的轮次仍显示「…中」。
function progressStageLabel(tool: ChatToolProcessGroup): string {
  if (isTerminalToolGroup(tool)) return '子代理执行过程'
  const stage = tool.progress?.stage
  return ({ reasoning: '子代理思考中…', searching: '子代理联网搜索中…', answering: '子代理汇总结果中…', generating: '正在生成图片…', submitting: '正在提交视频生成任务…', synthesizing: '正在合成语音…' }[stage ?? '']) ?? '子代理执行中…'
}

// 来源主域聚合（契约 §10.4）：同 hostname 的 URL 聚合一行，非链接文本与解析失败 URL 原样单列。
const sourceSummariesByKey = computed(() => new Map(process.value.toolGroups.map((tool) => [tool.key, aggregateSourceSummaries(tool.summaries)])))
function sourceSummariesOf(tool: ChatToolProcessGroup): ChatSourceSummaries {
  return sourceSummariesByKey.value.get(tool.key) ?? { texts: tool.summaries, domains: [] }
}
function sourceDomainKey(tool: ChatToolProcessGroup, domain: ChatSourceDomainGroup): string {
  return `${tool.key}\n${domain.host}`
}
function isSourceDomainExpanded(tool: ChatToolProcessGroup, domain: ChatSourceDomainGroup): boolean {
  return expandedSourceDomains.value.has(sourceDomainKey(tool, domain))
}
function toggleSourceDomain(tool: ChatToolProcessGroup, domain: ChatSourceDomainGroup): void {
  const key = sourceDomainKey(tool, domain)
  if (expandedSourceDomains.value.has(key)) expandedSourceDomains.value.delete(key)
  else expandedSourceDomains.value.add(key)
}

// 阶段耗时时间线（契约 §10.3 stageTimings）：终态折叠行尾展示，>1s 的阶段才显示，秒取整。
function terminalStageTimingsLabel(tool: ChatToolProcessGroup): string {
  if (!isTerminalToolGroup(tool)) return ''
  const timings = tool.progress?.stageTimings
  if (!timings) return ''
  const stageLabels: Array<[keyof NonNullable<ChatToolProgress['stageTimings']>, string]> = [['reasoning', '思考'], ['searching', '搜索'], ['answering', '回答']]
  return stageLabels
    .filter(([key]) => (timings[key] ?? 0) > 1000)
    .map(([key, label]) => `${label} ${Math.round((timings[key] ?? 0) / 1000)}s`)
    .join(' · ')
}

// 执行中等待时长（契约 §10.4）：仅存在 started/updated 分组时启用单个每秒 interval，
// 起算时间为消息 streaming 开始时间（createdAt 可解析时），否则回退组件挂载时间。
const mountedAt = Date.now()
const nowTick = ref(Date.now())
let nowTimer: ReturnType<typeof setInterval> | undefined
const stageWaitStartedAt = computed(() => serverDateTimeTimestamp(props.message.createdAt) ?? mountedAt)
const waitedSeconds = computed(() => Math.max(0, Math.floor((nowTick.value - stageWaitStartedAt.value) / 1000)))
const hasRunningToolGroup = computed(() => process.value.toolGroups.some((tool) => isRunningToolGroup(tool)))
watch(hasRunningToolGroup, (running) => { if (running) startNowTimer(); else stopNowTimer() }, { immediate: true })
onUnmounted(stopNowTimer)
function startNowTimer(): void {
  if (nowTimer !== undefined) return
  nowTimer = setInterval(() => { nowTick.value = Date.now() }, 1000)
}
function stopNowTimer(): void {
  if (nowTimer === undefined) return
  clearInterval(nowTimer)
  nowTimer = undefined
}
watch(() => props.message, async () => {
  await nextTick()
  const root = processRoot.value
  if (!root) return
  for (const details of [...root.querySelectorAll<HTMLDetailsElement>('details.chat-process-group[open]')]) {
    const body = details.querySelector<HTMLElement>('.chat-process-details.is-streaming')
    if (body) body.scrollTop = body.scrollHeight
  }
}, { flush: 'post' })

function toolLabel(type: string): string {
  return ({ web_search_call: '联网搜索', web_search: '联网搜索', image_generation: '图片生成', generate_image: '图片生成', video_generation: '视频生成', generate_video: '视频生成', audio_generation: '语音合成', generate_audio: '语音合成', file_search_call: '文件检索', function_call: '函数调用', computer_call: '计算机操作' }[type] ?? '工具调用')
}
function statusLabel(status: ChatToolStatus, toolType?: string): string {
  // 视频生成是异步任务工具：调用终态只代表任务受理成功，结果状态由
  // output_media_task 任务卡展示；这里若写「已完成」会和任务卡的
  // 「生成失败」等终态文案互相矛盾。
  if (status === 'completed' && (toolType === 'video_generation' || toolType === 'generate_video')) return '已提交'
  return ({ started: '准备中', updated: '执行中', completed: '已完成', failed: '失败', canceled: '已停止' })[status]
}
</script>

<style scoped>
.chat-process { margin-top: 8px; color: var(--juhe-muted); font-size: 12px; }
.chat-process-group, .chat-reasoning { margin-top: 4px; }
.chat-process summary, .chat-process-summary-only { display: flex; width: fit-content; max-width: 100%; align-items: center; gap: 7px; color: var(--juhe-muted); user-select: none; }
.chat-process summary { cursor: pointer; }
.chat-process-status { width: 6px; height: 6px; flex: 0 0 6px; border-radius: 50%; background: var(--juhe-muted); }
.chat-process-status.is-started, .chat-process-status.is-updated { background: var(--juhe-accent); }
.chat-process-status.is-started, .chat-process-status.is-updated { animation: chat-process-pulse 1.4s ease-in-out infinite; }
.chat-process-status.is-completed { background: var(--juhe-ok); }
.chat-process-status.is-failed, .chat-process-status.is-canceled { background: var(--juhe-danger); }
.chat-process-details { max-height: 168px; margin: 5px 0 0 13px; padding-left: 9px; overflow: auto; border-left: 2px solid var(--juhe-border); color: var(--juhe-muted); }
.chat-subagent { margin: 0 0 4px; }
.chat-subagent-stage { margin: 0; color: var(--juhe-faint); }
.chat-subagent-reasoning { margin: 4px 0 0; color: var(--juhe-muted); font-size: 12px; line-height: 1.6; }
.chat-subagent-reasoning :deep(p) { margin: 0 0 6px; }
.chat-subagent-reasoning :deep(p:last-child) { margin-bottom: 0; }
.chat-subagent-actions { margin: 4px 0 0; padding-left: 17px; }
.chat-subagent-actions li { margin: 2px 0; }
.chat-subagent-answer { margin: 4px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--juhe-muted); }
.chat-process-details ul { margin: 0; padding-left: 17px; }
.chat-process-details li { margin: 2px 0; overflow-wrap: anywhere; }
.chat-process-details a { color: var(--juhe-accent); }
.chat-process-details p { margin: 4px 0 0; color: var(--juhe-faint); }
.chat-source-domain { padding: 0; border: 0; background: none; color: var(--juhe-accent); font: inherit; cursor: pointer; }
.chat-source-domain:hover, .chat-source-domain:focus-visible { text-decoration: underline; }
.chat-source-domain-urls { margin: 2px 0 0; padding-left: 15px; list-style: none; }
.chat-reasoning { color: var(--juhe-muted); }
.chat-reasoning summary { color: var(--juhe-muted); }
.chat-reasoning div { max-height: 168px; margin: 5px 0 0 13px; padding: 5px 9px; overflow: auto; border-left: 2px solid var(--juhe-border); color: var(--juhe-muted); }
.chat-reasoning .chat-reasoning-body { color: var(--juhe-muted); font-size: 12px; line-height: 1.6; }
.chat-reasoning .chat-reasoning-body :deep(p) { margin: 0 0 6px; }
.chat-reasoning .chat-reasoning-body :deep(p:last-child) { margin-bottom: 0; }
@keyframes chat-process-pulse { 0%, 100% { opacity: .45; } 50% { opacity: 1; box-shadow: 0 0 0 4px rgba(83, 105, 107, .14); } }
@media (prefers-reduced-motion: reduce) { .chat-process-status.is-started, .chat-process-status.is-updated { animation: none; } }
</style>
