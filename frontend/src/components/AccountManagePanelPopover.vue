<template>
  <a-popover
    v-model:open="open"
    trigger="click"
    placement="bottomLeft"
    :overlay-style="{ width: '380px', maxWidth: 'calc(100vw - 24px)' }"
    @open-change="handleOpenChange"
  >
    <template #content>
      <AccountManagePanel
        :collection-items="collectionItems"
        :options="options"
        :max-added-accounts="maxAddedAccounts"
        :collection-max="collectionMax"
        :loading="loading"
        :disabled="disabled"
        :search-hint="searchHint"
        @add="emit('add', $event)"
        @remove="emit('remove', $event)"
        @toggle="emit('toggle', $event)"
        @search="emit('search', $event)"
        @reset-defaults="emit('reset-defaults')"
      />
    </template>
    <a-button class="account-manage-panel-trigger" :disabled="disabled">
      {{ triggerLabel ?? '账户' }}
      <span class="account-manage-panel-trigger-count">{{ countText }}</span>
      <DownOutlined class="account-manage-panel-trigger-caret" />
    </a-button>
  </a-popover>
</template>

<script setup lang="ts">
import { DownOutlined } from '@ant-design/icons-vue'
import { ref } from 'vue'

import AccountManagePanel from '@/components/AccountManagePanel.vue'
import type { AccountManagePanelItem, AccountManagePanelOption } from '@/components/AccountManagePanel.vue'

defineProps<{
  collectionItems: AccountManagePanelItem[]
  options: AccountManagePanelOption[]
  maxAddedAccounts: number
  collectionMax?: number
  loading?: boolean
  disabled?: boolean
  searchHint?: string
  countText: string
  triggerLabel?: string
}>()

const emit = defineEmits<{
  (event: 'open-change', open: boolean): void
  (event: 'toggle', id: string): void
  (event: 'remove', id: string): void
  (event: 'add', id: string): void
  (event: 'search', keyword: string): void
  (event: 'reset-defaults'): void
}>()

// Popover 开合由组件内部自持；父级通过 open-change 感知开合并按需懒加载账户候选。
const open = ref(false)

function handleOpenChange(next: boolean) {
  emit('open-change', next)
}
</script>

<style scoped>
.account-manage-panel-trigger {
  flex: 0 0 auto;
}

.account-manage-panel-trigger-count {
  margin-left: 2px;
  color: var(--juhe-muted);
  font-size: 12px;
}

.account-manage-panel-trigger-caret {
  margin-left: 4px;
  color: var(--juhe-muted);
  font-size: 10px;
}
</style>
