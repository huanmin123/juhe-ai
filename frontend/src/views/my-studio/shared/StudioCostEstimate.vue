<script setup lang="ts">
/**
 * 成本预估行（规划 F2 通用组件）：文本由工作台 display 纯函数产出；
 * 目录无价时显示"无法估算"，不编造数字（媒体工作室设计 §4.1）。
 */
defineProps<{
  estimate: string
}>()
</script>

<template>
  <div class="studio-cost-estimate">
    <span class="studio-cost-estimate__label">预估成本</span>
    <span class="studio-cost-estimate__value" :class="{ 'studio-cost-estimate__value--unknown': estimate === '无法估算' }">{{ estimate }}</span>
  </div>
</template>

<style scoped>
.studio-cost-estimate {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--juhe-text-secondary, #6b7280);
}

.studio-cost-estimate__value {
  color: var(--juhe-text-color, #374151);
  font-weight: 500;
}

.studio-cost-estimate__value--unknown {
  color: var(--juhe-warning-color, #d46b08);
  font-weight: 400;
}
</style>
