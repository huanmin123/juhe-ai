<template>
  <div class="chat-thinking">
    <span class="chat-thinking-mark" aria-hidden="true"><i /><i /><i /></span>
    <span role="status" aria-live="polite" aria-atomic="true">{{ label }}</span>
    <time class="chat-thinking-elapsed" :datetime="`PT${elapsedSeconds}S`" aria-hidden="true">{{ elapsedLabel }}</time>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { ChatGenerationLivenessState } from './chatGenerationRuntime'

const props = defineProps<{ startedAt: number; livenessState: ChatGenerationLivenessState }>()
const now = ref(Date.now())
let timer: number | undefined
const elapsedSeconds = computed(() => Math.max(0, Math.floor((now.value - props.startedAt) / 1_000)))
const elapsedLabel = computed(() => `${String(Math.min(9_999, elapsedSeconds.value)).padStart(2, '0')} 秒`)
const label = computed(() => ({
  active: '思考中',
  checking: '正在确认生成状态',
  reconnecting: '正在恢复连接'
})[props.livenessState])

onMounted(() => { timer = window.setInterval(() => { now.value = Date.now() }, 1_000) })
onBeforeUnmount(() => { if (timer !== undefined) window.clearInterval(timer) })
</script>

<style scoped>
.chat-thinking { min-height: 32px; display: inline-flex; align-items: center; gap: 8px; color: #64748b; font-size: 13px; }
.chat-thinking-mark { display: inline-flex; align-items: center; gap: 3px; }
.chat-thinking-mark i { width: 4px; height: 4px; border-radius: 50%; background: #1677ff; animation: chat-thinking-dot 1.2s ease-in-out infinite; }
.chat-thinking-mark i:nth-child(2) { animation-delay: .16s; }
.chat-thinking-mark i:nth-child(3) { animation-delay: .32s; }
.chat-thinking-elapsed { min-width: 4.5em; color: #98a2b3; font-variant-numeric: tabular-nums; }
@keyframes chat-thinking-dot { 0%, 70%, 100% { opacity: .28; transform: translateY(0); } 35% { opacity: 1; transform: translateY(-2px); } }
@media (prefers-reduced-motion: reduce) { .chat-thinking-mark i { animation: none; opacity: .7; } }
</style>
