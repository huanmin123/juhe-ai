<template>
  <div class="bench-card" :class="{ open }">
    <button type="button" class="bench-card-head" @click="$emit('toggle')">
      <span class="bench-card-icon" :class="tone"><component :is="icon" /></span>
      <span class="bench-card-main">
        <span class="bench-card-name">{{ name }}</span>
        <span class="bench-card-sum" :class="{ configured: count > 0 }">{{ summary }}</span>
      </span>
      <span class="bench-card-badge" :class="{ on: count > 0 }">{{ badgeText }}</span>
      <span class="bench-arrow"><RightOutlined /></span>
    </button>
    <div v-show="open" class="bench-card-body">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import { RightOutlined } from '@ant-design/icons-vue'
import { computed, type Component } from 'vue'

const props = withDefaults(defineProps<{
  open: boolean
  name: string
  summary: string
  count: number
  icon: Component
  tone?: '' | 'ok' | 'warn'
}>(), {
  tone: ''
})

defineEmits<{
  (event: 'toggle'): void
}>()

const badgeText = computed(() => (props.count > 0 ? `已配置 ${props.count} 项` : '默认值'))
</script>

<style scoped>
/* 折叠摘要卡壳（从 AccountEditModal.vue 逐字搬移）：头部显示当前值摘要，不展开也能掌握配置状态；编辑态与创建向导第 3 步共用。 */

.bench-card {
  min-width: 0;
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius);
  overflow: hidden;
  background: var(--juhe-surface);
}

.bench-card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  text-align: left;
  padding: 12px 16px;
  border: 0;
  border-radius: 0;
  background: var(--juhe-surface);
  cursor: pointer;
  font: inherit;
  color: inherit;
}

.bench-card-head:hover {
  background: var(--juhe-rail);
}

.bench-card.open .bench-card-head {
  background: var(--juhe-rail);
  border-bottom: 1px solid var(--juhe-border);
}

.bench-card-icon {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  background: var(--juhe-accent-soft);
  color: var(--juhe-accent);
  display: grid;
  place-items: center;
  font-size: 14px;
  flex: 0 0 auto;
}

.bench-card-icon.ok {
  background: var(--juhe-ok-soft);
  color: var(--juhe-ok);
}

.bench-card-icon.warn {
  background: var(--juhe-surface-soft);
  color: var(--juhe-coral);
}

.bench-card-main {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.bench-card-name {
  font-size: 13.5px;
  font-weight: 700;
  color: var(--juhe-fg);
}

.bench-card-sum {
  font-size: 11.5px;
  color: var(--juhe-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bench-card-sum.configured {
  color: var(--juhe-accent-mid);
}

.bench-card-badge {
  flex: 0 0 auto;
  font-size: 10.5px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--juhe-soft);
  color: var(--juhe-faint);
}

.bench-card-badge.on {
  background: var(--juhe-accent-soft);
  color: var(--juhe-accent);
}

.bench-arrow {
  flex: 0 0 auto;
  color: var(--juhe-faint);
  font-size: 11px;
  transition: transform 0.18s;
}

.bench-card.open .bench-arrow {
  transform: rotate(90deg);
}

.bench-card-body {
  padding: 16px;
  background: var(--juhe-rail);
}

@media (max-width: 640px) {
  .bench-card-sum {
    display: none;
  }
}
</style>
