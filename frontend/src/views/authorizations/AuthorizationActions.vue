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
import type { AuthorizationDirectionFilter } from './authorizationTableColumns'
import { activeTeamSources, canRevokeAuthorization, hasManualSource } from './authorizationFormatters'

const emit = defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }): void
}>()

const props = defineProps<{
  authorization: ResourceAuthorizationSummary
  compact?: boolean
  direction: AuthorizationDirectionFilter
  isManagementView: boolean
}>()

const canManageAuthorization = computed(() => props.isManagementView || props.authorization.permissions?.canEdit === true)
const canReturnAuthorization = computed(() => {
  if (props.isManagementView || props.direction !== 'inbound') return false
  if (props.authorization.granteeType !== 'system_account') return false
  return props.authorization.status !== 'revoked' && props.authorization.status !== 'returned'
})
const actions = computed<RowActionItem[]>(() => {
  if (canReturnAuthorization.value) {
    return [{
      key: 'return',
      label: '归还',
      icon: 'revoke',
      tone: 'danger',
      confirmTitle: `确认归还授权「${props.authorization.resourceName || props.authorization.resourceId}」？归还后你将不再看到或使用它，不影响授权方原资源。`,
      confirmOkText: '归还'
    }]
  }
  if (!canManageAuthorization.value) return []
  if (!canRevokeAuthorization(props.authorization)) {
    return []
  }
  const items: RowActionItem[] = [
    ...revokeActions.value
  ]
  return items
})
const moreActions = computed<RowActionItem[]>(() => {
  if (!canManageAuthorization.value) return []
  const items: RowActionItem[] = [
    { key: 'edit-expire', label: '修改配置', icon: 'settings', tone: 'primary' }
  ]
  if (props.authorization.status === 'active') {
    items.push({ key: 'pause', label: '暂停授权', icon: 'pause', tone: 'warning' })
  }
  if (props.authorization.status === 'paused' || props.authorization.status === 'expired') {
    items.push({ key: 'resume', label: '恢复授权', icon: 'resume', tone: 'success' })
  }
  if (props.authorization.status === 'revoked' || props.authorization.status === 'returned') {
    items.push({ key: 'resume', label: '重新授权', icon: 'resume', tone: 'success' })
  }
  return items
})
const revokeActions = computed<RowActionItem[]>(() => {
  if (!canRevokeAuthorization(props.authorization)) return []
  if (props.authorization.granteeType === 'team') {
    return [{
      key: 'revoke-team-grant',
      label: '回收',
      icon: 'revoke',
      tone: 'danger',
      confirmTitle: `确认回收团队授权「${authorizationName.value}」？回收后团队成员将不能继续使用该资源，授权记录会保留。`,
      confirmOkText: '回收'
    }]
  }
  const items: RowActionItem[] = []
  let hasSourceRevokeAction = false
  if (hasManualSource(props.authorization)) {
    items.push({
      key: 'revoke-manual',
      label: '回收',
      icon: 'revoke',
      tone: 'danger',
      confirmTitle: `确认回收授权「${authorizationName.value}」？回收后被授权人将不能继续使用该资源，授权记录会保留。`,
      confirmOkText: '回收'
    })
    hasSourceRevokeAction = true
  }
  const teamSources = activeTeamSources(props.authorization)
  for (const teamSource of teamSources) {
    items.push({
      key: `team:${teamSource.sourceTeamId}`,
      label: teamSource.sourceTeamName ? `回收${teamSource.sourceTeamName}` : '回收团队',
      icon: 'revoke',
      tone: 'danger',
      confirmTitle: `确认回收${teamSource.sourceTeamName ? `「${teamSource.sourceTeamName}」` : '该团队'}授权来源？回收后该团队来源不再让成员使用此资源。`,
      confirmOkText: '回收'
    })
    hasSourceRevokeAction = true
  }
  if (!hasSourceRevokeAction) {
    items.push({
      key: 'revoke-authorization',
      label: '回收',
      icon: 'revoke',
      tone: 'danger',
      confirmTitle: `确认回收授权「${authorizationName.value}」？回收后被授权人将不能继续使用该资源，授权记录会保留。`,
      confirmOkText: '回收'
    })
  }
  return items
})
const authorizationName = computed(() => props.authorization.resourceName || props.authorization.resourceId)

function handleActionClick(key: string) {
  emit('menu-click', { key })
}
</script>
