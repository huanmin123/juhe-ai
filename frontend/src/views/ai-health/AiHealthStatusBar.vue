<template>
  <div ref="containerRef" class="ai-health-status-bar">
    <canvas
      ref="canvasRef"
      class="ai-health-status-canvas"
      :aria-label="ariaLabel"
      role="img"
      @pointermove="handlePointerMove"
      @pointerleave="handlePointerLeave"
      @click="handleClick"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import type { AiHealthHourPoint } from '@/types/domain'

const props = defineProps<{ accountName: string; hours: AiHealthHourPoint[] }>()
const emit = defineEmits<{ select: [point: AiHealthHourPoint] }>()
const containerRef = ref<HTMLElement>()
const canvasRef = ref<HTMLCanvasElement>()
const containerWidth = ref(0)
let resizeObserver: ResizeObserver | undefined

const successCount = computed(() => props.hours.filter((hour) => hour.status === 'success').length)
const failureCount = computed(() => props.hours.filter((hour) => hour.status === 'failure').length)
const ariaLabel = computed(() => `${props.accountName} 健康状态：可用 ${successCount.value} 小时，不可用 ${failureCount.value} 小时，无记录 ${Math.max(0, props.hours.length - successCount.value - failureCount.value)} 小时`)

function draw(): void {
  const canvas = canvasRef.value
  if (!canvas) return
  const logicalWidth = Math.max(containerWidth.value, props.hours.length * 3, 1)
  const logicalHeight = 24
  const pixelRatio = Math.min(window.devicePixelRatio || 1, 2)
  canvas.width = Math.ceil(logicalWidth * pixelRatio)
  canvas.height = Math.ceil(logicalHeight * pixelRatio)
  canvas.style.width = `${logicalWidth}px`
  canvas.style.height = `${logicalHeight}px`
  const context = canvas.getContext('2d')
  if (!context) return
  context.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0)
  context.clearRect(0, 0, logicalWidth, logicalHeight)
  if (!props.hours.length) return
  const pitch = logicalWidth / props.hours.length
  const gap = Math.min(2, Math.max(0.75, pitch * 0.28))
  const barWidth = Math.max(1, pitch - gap)
  props.hours.forEach((hour, index) => {
    context.fillStyle = statusColor(hour.status)
    context.fillRect(index * pitch + gap / 2, 3, barWidth, 18)
  })
}

function handlePointerMove(event: PointerEvent): void {
  const hour = pointAtClientX(event.clientX)
  if (!hour) return
  const details = [hour.statHour.replace('T', ' '), statusLabel(hour.status)]
  if (hour.statusCode) details.push(`HTTP ${hour.statusCode}`)
  if (hour.errorCode) details.push(hour.errorCode)
  if (hour.errorMessage) details.push(hour.errorMessage)
  if (canvasRef.value) canvasRef.value.title = details.join(' · ')
}

function handleClick(event: MouseEvent): void {
  const hour = pointAtClientX(event.clientX)
  if (hour) emit('select', hour)
}

function pointAtClientX(clientX: number): AiHealthHourPoint | undefined {
  const canvas = canvasRef.value
  if (!canvas || !props.hours.length) return undefined
  const bounds = canvas.getBoundingClientRect()
  if (bounds.width <= 0) return undefined
  const index = Math.min(props.hours.length - 1, Math.max(0, Math.floor(((clientX - bounds.left) / bounds.width) * props.hours.length)))
  return props.hours[index]
}

function handlePointerLeave(): void {
  if (canvasRef.value) canvasRef.value.title = ariaLabel.value
}

function scrollToLatest(): void {
  window.requestAnimationFrame(() => {
    const container = containerRef.value
    if (container) container.scrollLeft = Math.max(0, container.scrollWidth - container.clientWidth)
  })
}

function statusColor(status: AiHealthHourPoint['status']): string {
  if (status === 'success') return '#10b981'
  if (status === 'failure') return '#ef4444'
  return '#d7dde5'
}

function statusLabel(status: AiHealthHourPoint['status']): string {
  if (status === 'success') return '可用'
  if (status === 'failure') return '不可用'
  return '无检查记录'
}

watch(() => props.hours, () => void nextTick(() => {
  draw()
  scrollToLatest()
}), { deep: false })

onMounted(() => {
  const container = containerRef.value
  if (!container) return
  const updateWidth = () => {
    containerWidth.value = container.clientWidth
    draw()
  }
  resizeObserver = new ResizeObserver(updateWidth)
  resizeObserver.observe(container)
  updateWidth()
  scrollToLatest()
  handlePointerLeave()
})

onBeforeUnmount(() => resizeObserver?.disconnect())
</script>

<style scoped>
.ai-health-status-bar {
  width: 100%;
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: thin;
}

.ai-health-status-canvas {
  display: block;
  max-width: 100%;
  cursor: pointer;
}
</style>
