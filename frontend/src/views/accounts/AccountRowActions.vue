<template>
  <RowActions :actions="actions" :more-actions="moreItems" @action-click="handleActionClick" />
</template>

<script setup lang="ts">
import { computed } from 'vue'

import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { AccountSummary } from '@/types/domain'
import type { AccountMenuItem } from './accountActionTypes'
import { buildAccountMoreActions, buildAccountRowActions, type AccountRowActionOptions } from './accountRowActions'

const props = defineProps<{
  account: AccountSummary
  canClone: boolean
  canDelete: boolean
  canEdit: boolean
  groupName?: string
  menuItems: AccountMenuItem[]
}>()

const emit = defineEmits<{
  (event: 'bind-group'): void
  (event: 'clone'): void
  (event: 'delete'): void
  (event: 'edit'): void
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'return-authorization'): void
  (event: 'test'): void
}>()

const actionOptions = computed<AccountRowActionOptions>(() => ({
  account: props.account,
  canClone: props.canClone,
  canDelete: props.canDelete,
  canEdit: props.canEdit,
  groupName: props.groupName,
  menuItems: props.menuItems
}))

const actions = computed<RowActionItem[]>(() => buildAccountRowActions(actionOptions.value))
const moreItems = computed<RowActionItem[]>(() => buildAccountMoreActions(actionOptions.value))

function handleActionClick(key: string) {
  if (key === 'bind-group') {
    emit('bind-group')
    return
  }
  if (key === 'return-authorization') {
    emit('return-authorization')
    return
  }
  if (key === 'delete') {
    emit('delete')
    return
  }
  if (key === 'edit') {
    emit('edit')
    return
  }
  if (key === 'clone') {
    emit('clone')
    return
  }
  if (key === 'test') {
    emit('test')
    return
  }
  emit('menu-click', { key })
}
</script>
