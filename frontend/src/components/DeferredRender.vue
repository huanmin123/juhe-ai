<template>
  <div v-if="ready || !deferred" class="deferred-render-content">
    <slot />
  </div>
  <div v-else class="deferred-render-placeholder" :style="placeholderStyle" aria-hidden="true">
    <slot name="placeholder" />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'

type IdleWindow = Window & {
  requestIdleCallback?: (callback: () => void, options?: { timeout?: number }) => number
  cancelIdleCallback?: (handle: number) => void
}

const props = withDefaults(defineProps<{
  active?: boolean
  deferred?: boolean
  delayFrames?: number
  idle?: boolean
  idleTimeout?: number
  minHeight?: number | string
  resetKey?: string | number
  resetOnDeactivate?: boolean
  resetOnKeyChange?: boolean
}>(), {
  active: true,
  deferred: true,
  delayFrames: 1,
  idle: false,
  idleTimeout: 500,
  minHeight: 0,
  resetKey: '',
  resetOnDeactivate: false,
  resetOnKeyChange: true
})

const emit = defineEmits<{
  (event: 'ready'): void
}>()

const ready = ref(!props.deferred)
const placeholderStyle = computed(() => {
  const minHeight = typeof props.minHeight === 'number' ? `${props.minHeight}px` : props.minHeight
  return minHeight ? { minHeight } : undefined
})
let animationFrame = 0
let idleHandle: number | undefined

function cancelSchedule() {
  if (typeof window === 'undefined') return
  window.cancelAnimationFrame(animationFrame)
  animationFrame = 0
  const idleWindow = window as IdleWindow
  if (idleHandle !== undefined) {
    idleWindow.cancelIdleCallback?.(idleHandle)
    idleHandle = undefined
  }
}

function markReady() {
  if (!props.active) return
  ready.value = true
  emit('ready')
}

function scheduleReady() {
  if (!props.deferred) {
    markReady()
    return
  }
  if (ready.value || !props.active || typeof window === 'undefined') return
  cancelSchedule()

  let remainingFrames = Math.max(1, props.delayFrames)
  const step = () => {
    if (!props.active) return
    remainingFrames -= 1
    if (remainingFrames > 0) {
      animationFrame = window.requestAnimationFrame(step)
      return
    }
    animationFrame = 0
    if (props.idle) {
      const idleWindow = window as IdleWindow
      if (idleWindow.requestIdleCallback) {
        idleHandle = idleWindow.requestIdleCallback(() => {
          idleHandle = undefined
          markReady()
        }, { timeout: props.idleTimeout })
        return
      }
    }
    markReady()
  }

  animationFrame = window.requestAnimationFrame(step)
}

function resetAndSchedule() {
  cancelSchedule()
  ready.value = !props.deferred
  scheduleReady()
}

watch(() => props.active, (active) => {
  if (active) {
    scheduleReady()
  } else {
    cancelSchedule()
    if (props.resetOnDeactivate) {
      ready.value = !props.deferred
    }
  }
}, { immediate: true })

watch(() => props.resetKey, () => {
  if (props.resetOnKeyChange) {
    resetAndSchedule()
  }
})

onActivated(scheduleReady)

onDeactivated(() => {
  cancelSchedule()
  if (props.resetOnDeactivate) {
    ready.value = !props.deferred
  }
})

onBeforeUnmount(cancelSchedule)
</script>

<style scoped>
.deferred-render-content {
  display: contents;
}

.deferred-render-placeholder {
  width: 100%;
}
</style>
