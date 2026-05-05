<template>
  <a-space class="row-actions" :size="8">
    <template v-if="isAuthorizedAccount(account)">
      <a-button type="link" size="small" @click="$emit('bind-group')">{{ groupName ? '调整分组' : '绑定分组' }}</a-button>
      <a-button type="link" size="small" @click="$emit('test')">测试</a-button>
    </template>
    <template v-else>
      <a-button v-if="canEdit" type="link" size="small" @click="$emit('edit')">编辑</a-button>
      <a-popconfirm v-if="canDelete" title="确认删除这个账户？" @confirm="$emit('delete')">
        <a-button type="link" size="small" danger>删除</a-button>
      </a-popconfirm>
      <a-dropdown v-if="menuItems.length">
        <a-button type="link" size="small">更多</a-button>
        <template #overlay>
          <a-menu @click="$emit('menu-click', $event)">
            <a-menu-item v-for="item in menuItems" :key="item.key" :danger="item.danger">{{ item.label }}</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
    </template>
  </a-space>
</template>

<script setup lang="ts">
import type { AccountSummary } from '@/types/domain'
import type { AccountMenuItem } from './accountActionTypes'
import { isAuthorizedAccount } from './accountFormatters'

defineProps<{
  account: AccountSummary
  canDelete: boolean
  canEdit: boolean
  groupName?: string
  menuItems: AccountMenuItem[]
}>()

defineEmits<{
  (event: 'bind-group'): void
  (event: 'delete'): void
  (event: 'edit'): void
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'test'): void
}>()
</script>

<style scoped>
.row-actions :deep(.ant-btn-link) {
  padding-inline: 2px;
}
</style>
