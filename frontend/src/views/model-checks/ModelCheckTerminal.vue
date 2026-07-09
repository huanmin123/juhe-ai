<template>
  <div v-if="visible" class="model-check-terminal">
    <div class="model-check-terminal-head">
      <div>
        <div class="terminal-title">AI 测试终端</div>
        <div class="terminal-subtitle">按真实检测进度输出探针请求、响应、评分和 Trace ID</div>
      </div>
      <a-space>
        <a-tag :color="statusColor">{{ statusText }}</a-tag>
        <a-button v-if="submitting" size="small" danger @click="$emit('stop')">停止检测</a-button>
      </a-space>
    </div>
    <div ref="terminalBodyRef" class="model-check-terminal-body">
      <div v-for="line in lines" :key="line.id" class="terminal-line" :class="`terminal-line-${line.level}`">
        <span class="terminal-time">[{{ line.time }}]</span>
        <span class="terminal-prompt">$</span>
        <span class="terminal-text">{{ line.text }}</span>
      </div>
      <div v-if="submitting" class="terminal-line terminal-line-muted">
        <span class="terminal-time">[{{ terminalNow }}]</span>
        <span class="terminal-prompt">_</span>
        <span class="terminal-text terminal-cursor">{{ waitingText }}</span>
      </div>
    </div>
  </div>
</template>

<script lang="ts">
import type { ModelCheckTerminalLineLevel } from './modelCheckFormatters'

export interface ModelCheckTerminalLine {
  id: number
  time: string
  level: ModelCheckTerminalLineLevel
  text: string
}
</script>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { formatClockTime } from './modelCheckFormatters'

const props = withDefaults(defineProps<{
  visible: boolean
  submitting: boolean
  lines: ModelCheckTerminalLine[]
  statusText: string
  statusColor: string
  waitingText?: string
}>(), {
  waitingText: '等待下一个检测事件'
})

defineEmits<{
  stop: []
}>()

const terminalBodyRef = ref<HTMLElement>()
const terminalNow = ref(formatClockTime(new Date()))
let terminalClockTimer: number | undefined

watch(() => [props.lines.length, props.submitting, props.visible], () => {
  if (!props.visible) return
  void nextTick(scrollTerminalToBottom)
})

function updateTerminalNow(): void {
  terminalNow.value = formatClockTime(new Date())
}

function scrollTerminalToBottom(): void {
  if (!terminalBodyRef.value) return
  terminalBodyRef.value.scrollTop = terminalBodyRef.value.scrollHeight
}

onMounted(() => {
  updateTerminalNow()
  terminalClockTimer = window.setInterval(updateTerminalNow, 1000)
})

onBeforeUnmount(() => {
  if (terminalClockTimer !== undefined) {
    window.clearInterval(terminalClockTimer)
    terminalClockTimer = undefined
  }
})
</script>

<style scoped>
.model-check-terminal {
  display: flex;
  height: 344px;
  margin-top: 14px;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid #1e293b;
  border-radius: 10px;
  background: #020617;
}

.model-check-terminal-head {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-bottom: 1px solid #1e293b;
  background: #0f172a;
}

.terminal-title {
  color: #e2e8f0;
  font-size: 13px;
  font-weight: 700;
}

.terminal-subtitle {
  margin-top: 2px;
  color: #94a3b8;
  font-size: 12px;
}

.model-check-terminal-body {
  min-height: 0;
  flex: 1 1 auto;
  padding: 12px;
  overflow: auto;
  color: #cbd5e1;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.7;
}

.terminal-line {
  display: grid;
  grid-template-columns: auto auto minmax(0, 1fr);
  gap: 8px;
  align-items: start;
  word-break: break-word;
}

.terminal-time {
  color: #64748b;
  white-space: nowrap;
}

.terminal-prompt {
  color: #38bdf8;
}

.terminal-line-success .terminal-text {
  color: #86efac;
}

.terminal-line-warning .terminal-text {
  color: #fde68a;
}

.terminal-line-error .terminal-text {
  color: #fca5a5;
}

.terminal-line-muted .terminal-text {
  color: #94a3b8;
}

.terminal-cursor::after {
  display: inline-block;
  width: 6px;
  height: 12px;
  margin-left: 4px;
  vertical-align: -2px;
  background: #38bdf8;
  content: '';
  animation: terminal-cursor-blink 1s steps(1) infinite;
}

@keyframes terminal-cursor-blink {
  50% {
    opacity: 0;
  }
}

@media (max-width: 900px) {
  .model-check-terminal-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .model-check-terminal {
    height: 320px;
  }
}
</style>
