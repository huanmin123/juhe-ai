<template>
  <nav class="stats-workspace-nav" aria-label="统计与监控视图">
    <button
      v-for="item in items"
      :key="item.key"
      type="button"
      class="stats-workspace-nav-item"
      :class="{ active: item.key === activeView }"
      :aria-current="item.key === activeView ? 'page' : undefined"
      @click="navigate(item.key)"
    >
      <component :is="item.icon" />
      <span>{{ item.label }}</span>
    </button>
  </nav>
</template>

<script setup lang="ts">
import { BarChartOutlined, SafetyCertificateOutlined, ThunderboltOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useStatsWorkspaceLink } from '@/views/stats/useStatsWorkspaceFilters'

const props = defineProps<{
  activeView: 'usage' | 'performance' | 'health'
  viewScope: 'admin' | 'self'
}>()

const router = useRouter()
const workspaceLink = useStatsWorkspaceLink()
const items = [
  { key: 'usage' as const, label: '用量分析', icon: BarChartOutlined },
  { key: 'performance' as const, label: '请求性能', icon: ThunderboltOutlined },
  { key: 'health' as const, label: '账户健康', icon: SafetyCertificateOutlined }
]

const paths = computed<Record<typeof items[number]['key'], string>>(() => {
  const prefix = props.viewScope === 'self' ? '/my-' : '/'
  return {
    usage: props.viewScope === 'self' ? '/my-stats' : '/stats',
    performance: `${prefix}ai-performance`,
    health: `${prefix}ai-health`
  }
})

function navigate(view: typeof items[number]['key']): void {
  if (view !== props.activeView) {
    void router.push(workspaceLink(paths.value[view]))
  }
}
</script>

<style scoped>
.stats-workspace-nav {
  display: flex;
  align-items: center;
  gap: 4px;
  margin: 0 0 16px;
  padding: 0 2px;
  border-bottom: 1px solid var(--juhe-border);
}

.stats-workspace-nav-item {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-height: 42px;
  padding: 0 14px;
  color: var(--juhe-muted);
  background: transparent;
  border: 0;
  border-bottom: 2px solid transparent;
  font-size: 13px;
  cursor: pointer;
  transition: color 160ms ease, border-color 160ms ease, background 160ms ease;
}

.stats-workspace-nav-item:hover,
.stats-workspace-nav-item.active {
  color: var(--juhe-fg);
}

.stats-workspace-nav-item.active {
  border-bottom-color: var(--juhe-accent);
  font-weight: 600;
}

@media (max-width: 600px) {
  .stats-workspace-nav {
    gap: 0;
    margin-bottom: 12px;
  }

  .stats-workspace-nav-item {
    flex: 1;
    justify-content: center;
    padding: 0 6px;
    font-size: 12px;
  }
}
</style>
