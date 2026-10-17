<template>
  <svg
    v-if="entry"
    :viewBox="entry.viewBox"
    :style="{ height: sizePx, width: widthPx }"
    aria-hidden="true"
  >
    <path
      v-for="(p, index) in entry.paths"
      :key="index"
      :d="p.d"
      :fill="p.fill ?? 'currentColor'"
      :fill-rule="p.fillRule"
    />
  </svg>
  <span v-else class="provider-icon-fallback" :style="fallbackStyle">{{ fallbackChar }}</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import { providerIconFallbackChar, providerIconPaths } from '@/shared/providerIcons'
import { normalizeProviderToken } from '@/shared/providerProtocol'

// 供应商图标：命中 providerIconPaths 渲染品牌内联 SVG（高度 size，宽度按 viewBox 纵横比自适应）；
// 未命中渲染字母圆标（--juhe-* 变量配色），字符取 name / providerCode 首字符。
const props = withDefaults(defineProps<{
  providerCode?: string
  name?: string
  size?: number
}>(), {
  size: 16
})

const entry = computed(() => {
  const code = normalizeProviderToken(props.providerCode)
  return code ? providerIconPaths[code] : undefined
})

const sizePx = computed(() => props.size + 'px')

const widthPx = computed(() => {
  if (!entry.value) return ''
  const [, , viewBoxWidth, viewBoxHeight] = entry.value.viewBox.split(/\s+/).map(Number)
  const ratio = viewBoxWidth / viewBoxHeight
  return Math.round(ratio * props.size * 100) / 100 + 'px'
})

const fallbackChar = computed(() => providerIconFallbackChar(props.name, props.providerCode))

const fallbackStyle = computed(() => ({
  width: sizePx.value,
  height: sizePx.value,
  fontSize: Math.max(9, Math.round(props.size * 0.55)) + 'px'
}))
</script>

<style scoped>
/* 内联场景（a-tag、表格单元格、下拉选项）的图标默认按文本基线对齐会明显偏高；
   vertical-align: middle 对齐的是 x-height 中线，仍比文字视觉中心低约 2px。
   这里用 -0.2em 负偏移（与 antd 图标 -0.125em 同思路、按本图标 14px/正文字号
   校准），使图标视觉中心与文字大写高度中心重合；flex 容器内该属性不生效。 */
svg {
  vertical-align: -0.2em;
}

.provider-icon-fallback {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  background: var(--juhe-accent-soft);
  color: var(--juhe-accent);
  font-weight: 600;
  line-height: 1;
  vertical-align: -0.2em;
  user-select: none;
}
</style>
