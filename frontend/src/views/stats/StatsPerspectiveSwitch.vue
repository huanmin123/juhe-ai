<template>
  <a-segmented
    :value="value"
    :options="options"
    :disabled="disabled"
    class="stats-perspective-switch"
    aria-label="统计视角"
    @change="handleChange"
  />
</template>

<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'

import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useStatsWorkspaceLink } from './useStatsWorkspaceFilters'

type Perspective = 'overview' | 'account'

const props = defineProps<{
  value: Perspective
  disabled?: boolean
}>()

const options = [
  { label: '总览', value: 'overview' },
  { label: '按账户看', value: 'account' }
] satisfies Array<{ label: string; value: Perspective }>

const router = useRouter()
const route = useRoute()
const { isManagementView } = useScopedMenuView()
const workspaceLink = useStatsWorkspaceLink()

function handleChange(next: string | number) {
  if (props.disabled || next === props.value) return
  const perspective = String(next) as Perspective
  if (perspective !== 'overview' && perspective !== 'account') return

  const target = perspective === 'overview'
    ? (isManagementView.value ? '/stats' : '/my-stats')
    : (isManagementView.value ? '/usage-stats' : '/my-usage-stats')

  if (route.path !== target) void router.push(workspaceLink(target))
}
</script>

<style scoped>
.stats-perspective-switch {
  flex: 0 0 auto;
}
</style>
