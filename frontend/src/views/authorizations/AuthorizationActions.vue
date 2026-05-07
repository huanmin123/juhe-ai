<template>
  <RowActions
    :actions="actions"
    :more-actions="moreActions"
    :variant="compact ? 'icon' : 'button'"
    @action-click="handleActionClick"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'

import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { ResourceAuthorizationSummary } from '@/types/domain'
import { activeTeamSources, hasManualSource } from './authorizationFormatters'

const emit = defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'usage-detail'): void
}>()

const props = defineProps<{
  authorization: ResourceAuthorizationSummary
  compact?: boolean
  isManagementView: boolean
}>()

const canManageAuthorization = computed(() => props.isManagementView || props.authorization.permissions?.canEdit === true)
const actions = computed<RowActionItem[]>(() => [
  { key: 'usage-detail', label: '明细', icon: 'detail', tone: 'info' }
])
const moreActions = computed<RowActionItem[]>(() => {
  if (!canManageAuthorization.value) return []
  const items: RowActionItem[] = [
    { key: 'edit-expire', label: '修改配置', icon: 'settings', tone: 'primary' }
  ]
  if (props.authorization.status === 'active') {
    items.push({ key: 'pause', label: '暂停授权', icon: 'pause', tone: 'warning' })
  }
  if (props.authorization.status === 'paused') {
    items.push({ key: 'resume', label: '恢复授权', icon: 'resume', tone: 'success' })
  }
  if (props.authorization.status === 'active' && hasManualSource(props.authorization)) {
    items.push({ key: 'revoke-manual', label: '回收', icon: 'revoke', tone: 'danger' })
  }
  const teamSources = activeTeamSources(props.authorization)
  if (teamSources.length) {
    items.push({
      key: 'revoke-team',
      label: '回收团队授权',
      icon: 'revoke',
      tone: 'danger',
      children: teamSources.map((teamSource) => ({
        key: `team:${teamSource.sourceTeamId}`,
        label: teamSource.sourceTeamName || teamSource.sourceTeamId || '未知团队',
        icon: 'revoke',
        tone: 'danger'
      }))
    })
  }
  return items
})

function handleActionClick(key: string) {
  if (key === 'usage-detail') {
    emit('usage-detail')
    return
  }
  emit('menu-click', { key })
}
</script>
