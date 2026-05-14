<template>
  <RowActions :actions="actions" :more-actions="moreItems" @action-click="handleActionClick" />
</template>

<script setup lang="ts">
import { computed } from 'vue'

import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { AccountSummary } from '@/types/domain'
import type { AccountMenuItem } from './accountActionTypes'
import { isAuthorizedAccount } from './accountFormatters'

const props = defineProps<{
  account: AccountSummary
  canDelete: boolean
  canEdit: boolean
  groupName?: string
  menuItems: AccountMenuItem[]
}>()

const emit = defineEmits<{
  (event: 'bind-group'): void
  (event: 'delete'): void
  (event: 'edit'): void
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'test'): void
}>()

const actions = computed<RowActionItem[]>(() => {
  if (isAuthorizedAccount(props.account)) {
    const authorizedList: RowActionItem[] = []
    if (props.account.status !== 'disabled') {
      authorizedList.push({ key: 'test', label: '测试', icon: 'test', tone: 'info' })
    }
    if (props.account.status === 'error') {
      return authorizedList
    }
    authorizedList.push({ key: 'bind-group', label: props.groupName ? '调整分组' : '绑定分组', icon: 'bind', tone: 'purple' })
    return authorizedList
  }
  const list: RowActionItem[] = []
  if (props.canEdit) {
    list.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  if (props.canDelete) {
    list.push({
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: '确认删除这个账户？',
      confirmOkText: '删除'
    })
  }
  return list
})

const moreItems = computed(() => props.menuItems)

function handleActionClick(key: string) {
  if (key === 'bind-group') {
    emit('bind-group')
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
  if (key === 'test') {
    emit('test')
    return
  }
  emit('menu-click', { key })
}
</script>
